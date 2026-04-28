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
	Region           string `json:"region"`

	ControlPlaneSize string `json:"controlPlaneSize"`
	WorkerSize       string `json:"workerSize"`
	WorkerCount      int    `json:"workerCount"`
	HAEnabled        bool   `json:"haEnabled"`

	SSHPublicKey string `json:"sshPublicKey"`

	// Dynadot DNS credentials are passed through to the OpenTofu module as
	// variables when SovereignDomainMode is "pool" (the module only writes
	// DNS for managed pool domains; BYO Sovereigns require the customer to
	// point their own CNAME at the LB IP shown in the success screen).
	DynadotAPIKey    string `json:"-"`
	DynadotAPISecret string `json:"-"`
}

// Validate ensures the wizard payload is complete enough for OpenTofu to run.
func (r Request) Validate() error {
	if strings.TrimSpace(r.HetznerToken) == "" {
		return errors.New("hetzner token is required")
	}
	if strings.TrimSpace(r.HetznerProjectID) == "" {
		return errors.New("hetzner project ID is required")
	}
	if strings.TrimSpace(r.Region) == "" {
		return errors.New("hetzner region is required (runtime parameter, never hardcoded)")
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
type Event struct {
	Time    string `json:"time"`
	Phase   string `json:"phase"`
	Level   string `json:"level"` // info | warn | error
	Message string `json:"message"`
}

// Result captures the OpenTofu outputs the wizard's success screen needs.
type Result struct {
	SovereignFQDN  string `json:"sovereignFQDN"`
	ControlPlaneIP string `json:"controlPlaneIP"`
	LoadBalancerIP string `json:"loadBalancerIP"`
	ConsoleURL     string `json:"consoleURL"`
	GitOpsRepoURL  string `json:"gitopsRepoURL"`
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

		// Topology
		"control_plane_size": req.ControlPlaneSize,
		"worker_size":        req.WorkerSize,
		"worker_count":       req.WorkerCount,
		"ha_enabled":         req.HAEnabled,

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
