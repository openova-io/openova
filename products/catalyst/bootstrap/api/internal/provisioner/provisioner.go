// Package provisioner is a thin wrapper around `tofu` (OpenTofu) — the
// canonical Phase 0 IaC layer per docs/ARCHITECTURE.md §10 and
// docs/SOVEREIGN-PROVISIONING.md §3.
//
// Per docs/INVIOLABLE-PRINCIPLES.md principle #3: OpenTofu provisions Phase 0
// cloud resources, Crossplane is the ONLY day-2 IaC, Flux is the ONLY GitOps
// reconciler, Blueprints are the ONLY install unit. This package therefore
// does NOT call cloud APIs directly, does NOT exec helm/kubectl, does NOT
// construct cloud-init inline. All of that lives in the OpenTofu module at
// infra/hetzner/ and in Crossplane Compositions / Flux Kustomizations the
// module bootstraps into the cluster.
//
// What this package DOES:
//   - validate the wizard's request (well-formed inputs)
//   - write a tofu.auto.tfvars.json file for the OpenTofu module
//   - exec `tofu init && tofu apply -auto-approve` and stream stdout to the
//     wizard via SSE events
//   - return tofu output values (control_plane_ip, load_balancer_ip,
//     kubeconfig) as the Result the wizard's success screen consumes
//
// Crossplane adoption (Phase 1 hand-off) and bootstrap-kit installation
// (Cilium → cert-manager → Flux → Crossplane → ... → bp-catalyst-platform)
// happen INSIDE the cluster via Flux reconciling clusters/<sovereign-fqdn>/
// in this monorepo — NOT from this Go process. By the time `tofu apply`
// returns, the cluster is bootstrapping itself.
package provisioner

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// RegionSpec is one entry in Request.Regions — the per-region sizing
// payload the wizard's StepProvider produces. Each topology slot has its
// own provider, its own cloud-region, and its own SKU vocabulary, so the
// canonical request shape carries one of these per slot.
//
// SKU strings are the provider's NATIVE instance-type identifier (cx32,
// VM.Standard.E5.Flex.4.32, m6i.xlarge, Standard_D4s_v5, ...). The
// OpenTofu module receives them verbatim via tofu.auto.tfvars.json and
// the provider's API validates them at apply time. The wizard reads
// every legal id from products/catalyst/bootstrap/ui/src/shared/constants/
// providerSizes.ts (PROVIDER_NODE_SIZES) — there is no SKU literal
// anywhere else in the wizard.
type RegionSpec struct {
	Provider         string `json:"provider"`
	CloudRegion      string `json:"cloudRegion"`
	ControlPlaneSize string `json:"controlPlaneSize"`
	WorkerSize       string `json:"workerSize"`
	WorkerCount      int    `json:"workerCount"`
}

// Request carries the wizard inputs the OpenTofu module needs.
type Request struct {
	OrgName  string `json:"orgName"`
	OrgEmail string `json:"orgEmail"`

	SovereignFQDN       string `json:"sovereignFQDN"`
	SovereignDomainMode string `json:"sovereignDomainMode"` // pool | byo
	SovereignPoolDomain string `json:"sovereignPoolDomain"`
	SovereignSubdomain  string `json:"sovereignSubdomain"`

	HetznerToken     string `json:"hetznerToken"`
	HetznerProjectID string `json:"hetznerProjectID"`

	// Legacy singular fields. When Regions is non-empty Validate()
	// derives these from Regions[0] so writeTfvars()'s single-region
	// apply path keeps working unchanged. When Regions is empty the
	// wizard is from before the per-provider rework migrated, or the
	// payload is hand-crafted (e.g. handler/load_test.go), and these
	// carry the request directly.
	Region           string `json:"region"`
	ControlPlaneSize string `json:"controlPlaneSize"`
	WorkerSize       string `json:"workerSize"`
	WorkerCount      int    `json:"workerCount"`

	HAEnabled bool `json:"haEnabled"`

	// Per-region sizing payload — canonical from the per-provider rework
	// onwards. The wizard always emits this. Multi-region tofu wiring is
	// structural-correct (variables.tf and the cloud-init templates
	// accept the per-region SKU values), but only Regions[0] is end-to-end
	// exercised today against a real Hetzner project: writeTfvars()
	// renders the singular fields below, mirrored from Regions[0]. The
	// for_each iteration that activates the rest lives in the OpenTofu
	// module — this Go struct's role is to carry the data, intact, for
	// that iteration to pick up.
	Regions []RegionSpec `json:"regions,omitempty"`

	SSHPublicKey string `json:"sshPublicKey"`

	// Dynadot DNS credentials are passed through to the OpenTofu module as
	// variables when SovereignDomainMode is "pool" (the module only writes
	// DNS for managed pool domains; BYO Sovereigns require the customer to
	// point their own CNAME at the LB IP shown in the success screen).
	DynadotAPIKey    string `json:"-"`
	DynadotAPISecret string `json:"-"`
}

// Validate ensures the wizard payload is complete enough for OpenTofu to run.
//
// Pointer receiver: when Regions is non-empty, Validate mirrors Regions[0]
// into the legacy singular fields so writeTfvars() can keep using them
// without conditional logic. Callers (handler.CreateDeployment) operate
// on the *Request anyway because the same instance is stored on
// Deployment.Request, so the mutation is intentional and persistent.
func (r *Request) Validate() error {
	if len(r.Regions) > 0 {
		// Each region must carry a provider + cloudRegion + control-plane
		// SKU. Worker SKU is required only when WorkerCount > 0.
		for i, rs := range r.Regions {
			if strings.TrimSpace(rs.Provider) == "" {
				return fmt.Errorf("region[%d] provider is required", i)
			}
			if strings.TrimSpace(rs.CloudRegion) == "" {
				return fmt.Errorf("region[%d] cloudRegion is required", i)
			}
			if strings.TrimSpace(rs.ControlPlaneSize) == "" {
				return fmt.Errorf("region[%d] controlPlaneSize is required", i)
			}
			if rs.WorkerCount < 0 {
				return fmt.Errorf("region[%d] workerCount must be non-negative", i)
			}
			if rs.WorkerCount > 0 && strings.TrimSpace(rs.WorkerSize) == "" {
				return fmt.Errorf("region[%d] workerSize is required when workerCount > 0", i)
			}
		}

		// Mirror Regions[0] into the legacy singular fields for the
		// single-region apply path inside writeTfvars().
		r.Region = r.Regions[0].CloudRegion
		r.ControlPlaneSize = r.Regions[0].ControlPlaneSize
		r.WorkerSize = r.Regions[0].WorkerSize
		r.WorkerCount = r.Regions[0].WorkerCount
	}

	if strings.TrimSpace(r.HetznerToken) == "" {
		return errors.New("hetzner token is required")
	}
	if strings.TrimSpace(r.HetznerProjectID) == "" {
		return errors.New("hetzner project ID is required")
	}
	if strings.TrimSpace(r.Region) == "" {
		return errors.New("region is required (runtime parameter, never hardcoded)")
	}
	if strings.TrimSpace(r.SovereignFQDN) == "" {
		return errors.New("sovereign FQDN is required")
	}
	if strings.TrimSpace(r.OrgName) == "" {
		return errors.New("organisation name is required")
	}
	if strings.TrimSpace(r.OrgEmail) == "" {
		return errors.New("organisation email is required (becomes initial sovereign-admin)")
	}
	if strings.TrimSpace(r.SSHPublicKey) == "" {
		return errors.New("SSH public key is required (sovereign-admin break-glass + cluster bootstrap)")
	}
	return nil
}

// Event is a single progress event streamed back to the wizard via SSE.
//
// Component / State are populated for Phase-1 component events emitted by
// the HelmRelease watch loop (internal/helmwatch). For Phase-0 OpenTofu
// events these stay empty so the existing wire format is unchanged — no
// existing field is removed or renamed; only two optional fields are
// added. The Admin shell's "logs filtered by event.component === id"
// path keys off Component; the per-app status pill keys off State.
//
// State semantics (Phase-1 watch only):
//
//   - "pending"    — HelmRelease appeared in the cluster but Ready
//     condition not yet observed, OR Ready=False with a
//     `dependency 'X' is not ready` message (the
//     component is waiting upstream of itself)
//   - "installing" — Ready=Unknown, or Ready=False with reason
//     `Progressing` / message `Reconciliation in progress`
//   - "installed"  — Ready=True
//   - "degraded"   — Ready=True transitioned to Ready=False without
//     InstallFailed/UpgradeFailed (a healthy install
//     that lost readiness post-install)
//   - "failed"     — Ready=False with reason InstallFailed /
//     UpgradeFailed / ChartPullError /
//     ArtifactFailed (the install actually broke,
//     not waiting on deps)
//
// For phase: "component-log" events, Component is set, State is empty,
// Level carries the helm-controller log level, and Message is the raw
// log line.
type Event struct {
	Time    string `json:"time"`
	Phase   string `json:"phase"`
	Level   string `json:"level"` // info | warn | error
	Message string `json:"message"`

	// Component is the normalised component id for Phase-1 events
	// ("bp-cilium" → "cilium"). Empty for Phase-0 OpenTofu events.
	Component string `json:"component,omitempty"`

	// State is one of pending|installing|installed|degraded|failed for
	// phase: "component" events; empty for Phase-0 events and for
	// phase: "component-log" events (which carry the original log
	// level instead).
	State string `json:"state,omitempty"`
}

// Result captures the OpenTofu outputs the wizard's success screen needs
// PLUS the Phase-1 component watch terminal state.
//
// ComponentStates and Phase1FinishedAt are populated by the HelmRelease
// watch loop in internal/helmwatch. They are the durable per-component
// outcome the Admin shell renders ("X of Y components installed") long
// after the live SSE stream has closed.
//
// Kubeconfig holds the new Sovereign's k3s kubeconfig (raw YAML). It is
// populated at the end of Phase 0 (out-of-band kubeconfig fetch) so the
// HelmRelease watch loop, the wizard's "Download kubeconfig" button, and
// the operator's GET /api/v1/deployments/<id>/kubeconfig all consume the
// same source. The kubeconfig is rotated to a SPIFFE-issued identity in
// Phase 2 — by then this field's role narrows to "first-time bootstrap
// only," but the storage shape stays.
type Result struct {
	SovereignFQDN  string `json:"sovereignFQDN"`
	ControlPlaneIP string `json:"controlPlaneIP"`
	LoadBalancerIP string `json:"loadBalancerIP"`
	ConsoleURL     string `json:"consoleURL"`
	GitOpsRepoURL  string `json:"gitopsRepoURL"`

	// Kubeconfig — raw YAML. Empty until the post-tofu-output fetch
	// populates it. Persisted to the per-deployment store record so a
	// catalyst-api Pod restart does not lose access to the new
	// Sovereign cluster the previous Pod was watching. Per
	// docs/INVIOLABLE-PRINCIPLES.md #10 (credential hygiene), the
	// store directory is 0o700 owned by the catalyst-api process UID
	// — the kubeconfig never touches a wider permission domain than
	// other per-deployment artefacts already on the same PVC.
	Kubeconfig string `json:"kubeconfig,omitempty"`

	// ComponentStates — final state of every bp-* HelmRelease the
	// Phase-1 watch observed, keyed by normalised component id. Set
	// when the watch loop terminates (all-installed, all-installed-or-
	// failed, or timeout).
	ComponentStates map[string]string `json:"componentStates,omitempty"`

	// Phase1FinishedAt — UTC timestamp the watch loop terminated.
	// nil while Phase 1 is in flight or has not started.
	Phase1FinishedAt *time.Time `json:"phase1FinishedAt,omitempty"`
}

// Provisioner runs `tofu init && tofu apply` against the canonical
// infra/hetzner/ module.
type Provisioner struct {
	// ModulePath is the absolute path to the OpenTofu module directory.
	// In the deployed catalyst-api container this is /infra/hetzner/.
	ModulePath string
	// WorkDir is where per-deployment tofu state is kept. In production
	// this is a per-Sovereign subdirectory, persisted via the catalyst-api
	// PVC so re-runs (`tofu apply` again with same vars) are idempotent.
	WorkDir string
}

// New returns a Provisioner with paths read from environment.
func New() *Provisioner {
	return &Provisioner{
		ModulePath: env("CATALYST_TOFU_MODULE_PATH", "/infra/hetzner"),
		WorkDir:    env("CATALYST_TOFU_WORKDIR", "/var/lib/catalyst/tofu"),
	}
}

// Provision runs the full sequence. Emits events into the channel; returns
// Result on success.
func (p *Provisioner) Provision(ctx context.Context, req Request, events chan<- Event) (*Result, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}

	emit := func(phase, level, msg string) {
		select {
		case events <- Event{Time: time.Now().UTC().Format(time.RFC3339), Phase: phase, Level: level, Message: msg}:
		default:
		}
	}

	// Per-deployment workdir keyed by Sovereign FQDN — re-running with the
	// same FQDN is idempotent (tofu apply on existing state).
	deployDir := filepath.Join(p.WorkDir, req.sovereignName())
	if err := os.MkdirAll(deployDir, 0o700); err != nil {
		return nil, fmt.Errorf("create workdir: %w", err)
	}

	// Stage the module by symlinking — keeps state isolated per deployment
	// while sharing the canonical module source.
	if err := stageModule(p.ModulePath, deployDir); err != nil {
		return nil, fmt.Errorf("stage tofu module: %w", err)
	}

	// Write tofu.auto.tfvars.json — OpenTofu auto-loads any *.auto.tfvars.json
	// in the working directory at apply time.
	if err := writeTfvars(deployDir, req); err != nil {
		return nil, fmt.Errorf("write tfvars: %w", err)
	}

	emit("tofu-init", "info", "Initialising OpenTofu working directory")
	if err := p.runTofu(ctx, deployDir, []string{"init", "-input=false", "-no-color"}, emit); err != nil {
		return nil, fmt.Errorf("tofu init: %w", err)
	}

	emit("tofu-plan", "info", "Planning Hetzner resources (network, firewall, server, LB, DNS)")
	if err := p.runTofu(ctx, deployDir, []string{"plan", "-input=false", "-no-color", "-out=tfplan"}, emit); err != nil {
		return nil, fmt.Errorf("tofu plan: %w", err)
	}

	emit("tofu-apply", "info", "Applying — this provisions real Hetzner resources, please wait")
	if err := p.runTofu(ctx, deployDir, []string{"apply", "-input=false", "-no-color", "-auto-approve", "tfplan"}, emit); err != nil {
		return nil, fmt.Errorf("tofu apply: %w", err)
	}

	emit("tofu-output", "info", "Reading OpenTofu outputs")
	out, err := p.readOutputs(ctx, deployDir)
	if err != nil {
		return nil, fmt.Errorf("read tofu outputs: %w", err)
	}

	emit("flux-bootstrap", "info", "Cloud-init has bootstrapped Flux + Crossplane in the new cluster — Flux will now reconcile clusters/"+req.SovereignFQDN+"/ from the public OpenOva monorepo, installing the 11-component bootstrap kit and bp-catalyst-platform umbrella in dependency order. The wizard's progress page will poll Flux Kustomizations on the new cluster for steady-state.")

	return &Result{
		SovereignFQDN:  req.SovereignFQDN,
		ControlPlaneIP: out.ControlPlaneIP,
		LoadBalancerIP: out.LoadBalancerIP,
		ConsoleURL:     fmt.Sprintf("https://console.%s", req.SovereignFQDN),
		GitOpsRepoURL:  fmt.Sprintf("https://gitea.%s", req.SovereignFQDN),
	}, nil
}

// runTofu executes `tofu <args>` in deployDir, streaming stdout/stderr lines
// as Events to the wizard.
func (p *Provisioner) runTofu(ctx context.Context, deployDir string, args []string, emit func(string, string, string)) error {
	cmd := exec.CommandContext(ctx, "tofu", args...)
	cmd.Dir = deployDir
	cmd.Env = append(os.Environ(),
		// HCLOUD_TOKEN must be in the environment for the hcloud provider —
		// OpenTofu's variable system does NOT pass tfvars to the provider's
		// auth flow, only to the module's variable references. So we duplicate
		// it as both a tfvar (module references it) AND env (provider auth).
		// The tfvar value is what gets serialized; we keep it short-lived.
		"TF_INPUT=false",
		"TF_IN_AUTOMATION=true",
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	go streamLines(stdout, "tofu", "info", emit)
	go streamLines(stderr, "tofu", "warn", emit)

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("tofu %s failed: %w", strings.Join(args, " "), err)
	}
	return nil
}

func streamLines(r io.Reader, phase, level string, emit func(string, string, string)) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		emit(phase, level, line)
	}
}

type tofuOutputs struct {
	ControlPlaneIP string `json:"control_plane_ip"`
	LoadBalancerIP string `json:"load_balancer_ip"`
}

func (p *Provisioner) readOutputs(ctx context.Context, deployDir string) (*tofuOutputs, error) {
	cmd := exec.CommandContext(ctx, "tofu", "output", "-json", "-no-color")
	cmd.Dir = deployDir
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	// tofu output -json wraps each output in {"value": ..., "type": ..., "sensitive": ...}
	var raw map[string]struct {
		Value any `json:"value"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse tofu output: %w", err)
	}
	asString := func(key string) string {
		v, ok := raw[key]
		if !ok {
			return ""
		}
		if s, ok := v.Value.(string); ok {
			return s
		}
		return ""
	}
	return &tofuOutputs{
		ControlPlaneIP: asString("control_plane_ip"),
		LoadBalancerIP: asString("load_balancer_ip"),
	}, nil
}

// writeTfvars renders tofu.auto.tfvars.json from the wizard request. The
// module's variables.tf declares every key here; the JSON format is auto-
// loaded by tofu without any -var-file flag.
func writeTfvars(deployDir string, req Request) error {
	vars := map[string]any{
		// Identity
		"sovereign_fqdn":      req.SovereignFQDN,
		"sovereign_subdomain": req.SovereignSubdomain,
		"org_name":            req.OrgName,
		"org_email":           req.OrgEmail,

		// Hetzner — token gets baked into the state file unless the operator
		// configures a remote backend with encryption-at-rest. Per Catalyst
		// the production catalyst-api container's PVC is encrypted; for
		// air-gap installs the operator MUST configure remote backend.
		"hcloud_token":      req.HetznerToken,
		"hcloud_project_id": req.HetznerProjectID,
		"region":            req.Region,

		// Topology — singular fields drive today's solo apply path. The
		// per-region payload (regions, below) is structurally available
		// to the OpenTofu module's for_each iteration when the multi-
		// region wiring is activated; collapsing it back to single-SKU
		// here would break the architectural shape the wizard intends.
		"control_plane_size": req.ControlPlaneSize,
		"worker_size":        req.WorkerSize,
		"worker_count":       req.WorkerCount,
		"ha_enabled":         req.HAEnabled,

		// Per-region payload — emitted as a list of objects so the
		// OpenTofu module can iterate (variable "regions" in
		// infra/hetzner/variables.tf accepts this shape and is currently
		// unused by main.tf; the for_each that consumes it lives behind
		// the multi-region activation work). Structural-correct today;
		// no-op at apply time for solo deployments where len(regions)<=1.
		"regions": req.Regions,

		// SSH key — module creates an hcloud_ssh_key from this and attaches
		// to all servers. We never generate keys here; sovereign-admin
		// supplies the public half from their secrets manager.
		"ssh_public_key": req.SSHPublicKey,

		// Domain
		"domain_mode":   req.SovereignDomainMode, // pool | byo
		"pool_domain":   req.SovereignPoolDomain,
		"dynadot_key":   req.DynadotAPIKey,    // empty when domain_mode != "pool"
		"dynadot_secret": req.DynadotAPISecret,

		// GitOps source — Flux on the new cluster watches this for
		// clusters/<sovereign-fqdn>/. Defaults to the public OpenOva monorepo;
		// override for air-gap (operator-mirrored Gitea).
		"gitops_repo_url": env("CATALYST_GITOPS_REPO_URL", "https://github.com/openova-io/openova"),
		"gitops_branch":   env("CATALYST_GITOPS_BRANCH", "main"),
	}

	raw, err := json.MarshalIndent(vars, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(deployDir, "tofu.auto.tfvars.json"), raw, 0o600)
}

// stageModule copies the canonical module's *.tf files into the per-deployment
// workdir. We copy rather than symlink so each deployment can have its own
// .terraform/ + state, and so OpenTofu's working-directory model works as
// expected.
func stageModule(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tf") && !strings.HasSuffix(e.Name(), ".tftpl") {
			continue
		}
		from := filepath.Join(src, e.Name())
		to := filepath.Join(dst, e.Name())
		// Skip if already there and unchanged — re-runs of the same wizard
		// flow shouldn't re-copy module files.
		srcInfo, _ := os.Stat(from)
		dstInfo, _ := os.Stat(to)
		if dstInfo != nil && dstInfo.ModTime() == srcInfo.ModTime() && dstInfo.Size() == srcInfo.Size() {
			continue
		}
		raw, err := os.ReadFile(from)
		if err != nil {
			return err
		}
		if err := os.WriteFile(to, raw, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func (r Request) sovereignName() string {
	return strings.ReplaceAll(r.SovereignFQDN, ".", "-")
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
