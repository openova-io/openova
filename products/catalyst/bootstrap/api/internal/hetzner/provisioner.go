// Package hetzner implements a real Hetzner Cloud provisioner.
//
// This file replaces the previous simulated provisioning loop with actual
// hcloud API calls: network + subnet, SSH key, firewall, control-plane
// server with k3s cloud-init, optional worker servers, and a load balancer
// targeting the control plane on port 6443 plus the workload ingress on
// 80/443.
//
// The provisioner runs synchronously inside a goroutine started by the HTTP
// handler. It emits progress events into a channel that the handler streams
// back to the wizard via SSE.
//
// Per docs/PROVISIONING-PLAN.md: this is real provisioning, no mocks. The
// wizard's StepCredentials passes a real Hetzner Cloud API token and a real
// Hetzner project ID; this code uses both to create real billable resources.
package hetzner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/bootstrap"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/dynadot"
)

// ProvisionRequest carries the inputs the wizard captured. The handler maps
// the JSON request body into this struct before kicking off provisioning.
type ProvisionRequest struct {
	// Identity
	OrgName  string `json:"orgName"`
	OrgEmail string `json:"orgEmail"`

	// Sovereign domain — exactly one of SovereignFQDN must be set after
	// resolveSovereignDomain runs in the wizard. The handler is responsible
	// for resolving the wizard's pool/byo state into a single FQDN before
	// calling Provision.
	SovereignFQDN string `json:"sovereignFQDN"`

	// SovereignDomainMode is "pool" or "byo". When "pool" the provisioner
	// writes DNS records via the Dynadot API; when "byo" it skips DNS and
	// the success screen tells the customer to point a CNAME themselves.
	SovereignDomainMode string `json:"sovereignDomainMode"`

	// SovereignPoolDomain is the parent pool domain (e.g. "omani.works")
	// when SovereignDomainMode == "pool". Combined with the subdomain to
	// derive the full FQDN.
	SovereignPoolDomain string `json:"sovereignPoolDomain"`

	// SovereignSubdomain is the user-typed subdomain ("omantel") when
	// SovereignDomainMode == "pool".
	SovereignSubdomain string `json:"sovereignSubdomain"`

	// Hetzner credentials + region (runtime parameter, never hardcoded).
	HetznerToken     string `json:"hetznerToken"`
	HetznerProjectID string `json:"hetznerProjectID"`
	Region           string `json:"region"` // e.g. "fsn1", "nbg1", "hel1", "ash", "hil"

	// Topology / sizing
	ControlPlaneSize string `json:"controlPlaneSize"` // e.g. "cx32"
	WorkerSize       string `json:"workerSize"`       // e.g. "cx32"
	WorkerCount      int    `json:"workerCount"`      // 0 = single-node solo Sovereign
	HAEnabled        bool   `json:"haEnabled"`        // 3 control-plane nodes when true

	// Public SSH key (PEM/OpenSSH format) the wizard captured. Optional —
	// when empty the provisioner generates a one-shot key, attaches it to
	// the project, and publishes the private key into the deployment status
	// (so a sovereign-admin can SSH in for break-glass).
	SSHPublicKey string `json:"sshPublicKey"`

	// Dynadot credentials — read from the dynadot-api-credentials K8s
	// secret by the handler before calling Provision. They are NOT exposed
	// to the wizard; only the controller has access to them.
	DynadotAPIKey    string `json:"-"`
	DynadotAPISecret string `json:"-"`
}

// Validate ensures the request has all the fields the provisioner needs.
// Returns the first violation as an error, or nil when the request is OK.
func (p ProvisionRequest) Validate() error {
	if strings.TrimSpace(p.HetznerToken) == "" {
		return errors.New("hetzner token is required")
	}
	if strings.TrimSpace(p.HetznerProjectID) == "" {
		return errors.New("hetzner project ID is required")
	}
	if strings.TrimSpace(p.Region) == "" {
		return errors.New("hetzner region is required (runtime parameter, never hardcoded)")
	}
	if strings.TrimSpace(p.SovereignFQDN) == "" {
		return errors.New("sovereign FQDN is required (resolved from wizard pool/byo selection)")
	}
	if strings.TrimSpace(p.OrgName) == "" {
		return errors.New("organisation name is required")
	}
	if strings.TrimSpace(p.OrgEmail) == "" {
		return errors.New("organisation email is required (becomes initial sovereign-admin)")
	}
	return nil
}

// SovereignName derives the K8s-safe Sovereign name from the FQDN.
// "omantel.omani.works" → "omantel-omani-works".
func (p ProvisionRequest) SovereignName() string {
	return strings.ReplaceAll(p.SovereignFQDN, ".", "-")
}

// Event is a single progress event streamed back to the wizard via SSE.
type Event struct {
	Time    string `json:"time"`
	Phase   string `json:"phase"`
	Level   string `json:"level"` // info | warn | error
	Message string `json:"message"`
}

// Result captures everything the wizard's success screen needs after a
// successful provisioning run.
type Result struct {
	SovereignFQDN    string `json:"sovereignFQDN"`
	ControlPlaneIP   string `json:"controlPlaneIP"`
	LoadBalancerIP   string `json:"loadBalancerIP"`
	KubeconfigBase64 string `json:"kubeconfigBase64"`
	ConsoleURL       string `json:"consoleURL"`
	GitOpsRepoURL    string `json:"gitopsRepoURL"`
}

// Provisioner orchestrates the full Hetzner-side provisioning sequence
// against a real Hetzner Cloud account.
//
// Method calls are deliberately small and named by phase so a future
// debugger can pinpoint exactly which API call failed.
type Provisioner struct {
	HTTPClient *http.Client
}

// New returns a Provisioner with a sensible default HTTP client.
func New() *Provisioner {
	return &Provisioner{
		HTTPClient: &http.Client{Timeout: 60 * time.Second},
	}
}

// Provision runs the full sequence. It emits events into the events channel
// and returns the Result when complete.
//
// On any error the channel is closed by the caller (the HTTP handler that
// owns the channel lifecycle); Provision itself does not close it.
func (p *Provisioner) Provision(ctx context.Context, req ProvisionRequest, events chan<- Event) (*Result, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}

	emit := func(phase, level, msg string) {
		select {
		case events <- Event{Time: time.Now().UTC().Format(time.RFC3339), Phase: phase, Level: level, Message: msg}:
		default:
			// channel is full or closed; drop the event rather than block
		}
	}

	emit("validate", "info", "Validating Hetzner Cloud API token + project ID")
	if err := p.validateProject(ctx, req); err != nil {
		return nil, fmt.Errorf("validate project: %w", err)
	}

	emit("ssh-key", "info", "Ensuring SSH key registered in Hetzner project")
	sshKeyID, err := p.ensureSSHKey(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("ensure ssh key: %w", err)
	}
	emit("ssh-key", "info", fmt.Sprintf("SSH key id=%d ready", sshKeyID))

	emit("network", "info", "Creating private network 10.0.0.0/16 with control-plane subnet")
	networkID, err := p.createNetwork(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("create network: %w", err)
	}
	emit("network", "info", fmt.Sprintf("Network id=%d created", networkID))

	emit("firewall", "info", "Creating firewall (allow 80/443/6443/icmp inbound, all outbound)")
	firewallID, err := p.createFirewall(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("create firewall: %w", err)
	}
	emit("firewall", "info", fmt.Sprintf("Firewall id=%d created", firewallID))

	emit("control-plane", "info", fmt.Sprintf("Provisioning control-plane server (%s) in %s with k3s cloud-init", req.ControlPlaneSize, req.Region))
	cpServer, err := p.createControlPlaneServer(ctx, req, networkID, firewallID, sshKeyID)
	if err != nil {
		return nil, fmt.Errorf("create control-plane: %w", err)
	}
	emit("control-plane", "info", fmt.Sprintf("Control-plane server id=%d ip=%s — waiting for cloud-init + k3s ready", cpServer.ID, cpServer.PublicIP))

	if err := p.waitForK3sReady(ctx, cpServer, emit); err != nil {
		return nil, fmt.Errorf("wait k3s: %w", err)
	}
	emit("control-plane", "info", "k3s control-plane reachable on :6443")

	if req.WorkerCount > 0 {
		emit("workers", "info", fmt.Sprintf("Provisioning %d worker node(s) (%s)", req.WorkerCount, req.WorkerSize))
		if err := p.createWorkers(ctx, req, networkID, firewallID, sshKeyID, cpServer.PublicIP, emit); err != nil {
			return nil, fmt.Errorf("create workers: %w", err)
		}
	}

	emit("loadbalancer", "info", "Creating Hetzner load balancer (lb11) for ingress 80/443")
	lb, err := p.createLoadBalancer(ctx, req, cpServer.ID, networkID)
	if err != nil {
		return nil, fmt.Errorf("create load balancer: %w", err)
	}
	emit("loadbalancer", "info", fmt.Sprintf("Load balancer id=%d ip=%s", lb.ID, lb.PublicIP))

	// DNS — only for pool-domain Sovereigns where Dynadot manages the
	// parent zone. BYO Sovereigns: the customer points DNS themselves; we
	// surface the LB IP in the Result so the wizard's success screen can
	// show "create a CNAME for sovereign.acme-bank.com → <lb-ip>".
	if req.SovereignDomainMode == "pool" && dynadot.IsManagedDomain(req.SovereignPoolDomain) {
		emit("dns", "info", fmt.Sprintf("Writing DNS records for *.%s.%s → %s via Dynadot",
			req.SovereignSubdomain, req.SovereignPoolDomain, lb.PublicIP))
		if req.DynadotAPIKey == "" || req.DynadotAPISecret == "" {
			return nil, fmt.Errorf("pool domain %q requires Dynadot credentials but none were provided to provisioner", req.SovereignPoolDomain)
		}
		dyn := dynadot.New(req.DynadotAPIKey, req.DynadotAPISecret)
		if err := dyn.AddSovereignRecords(ctx, req.SovereignPoolDomain, req.SovereignSubdomain, lb.PublicIP); err != nil {
			return nil, fmt.Errorf("dynadot dns: %w", err)
		}
		emit("dns", "info", fmt.Sprintf("Wrote 6 A records (apex+console+gitea+harbor+admin+api) to %s", req.SovereignPoolDomain))
	} else {
		emit("dns", "info", fmt.Sprintf("BYO domain mode — customer must create A or CNAME at %s → %s", req.SovereignFQDN, lb.PublicIP))
	}

	emit("kubeconfig", "info", "Fetching kubeconfig from control plane via SSH")
	kubeconfig, err := p.fetchKubeconfig(ctx, cpServer.PublicIP, lb.PublicIP)
	if err != nil {
		return nil, fmt.Errorf("fetch kubeconfig: %w", err)
	}
	emit("kubeconfig", "info", "Kubeconfig retrieved — control plane reachable")

	// Bootstrap kit — installs Cilium → cert-manager → Flux → Crossplane →
	// Sealed Secrets → SPIRE → JetStream → OpenBao → Keycloak → Gitea →
	// bp-catalyst-platform umbrella in dependency order. After this returns
	// successfully, the new Sovereign is self-sufficient (per
	// SOVEREIGN-PROVISIONING.md §4 Phase 1 hand-off).
	emit("bootstrap", "info", "Installing Catalyst bootstrap kit (11 components in dependency order)")
	bootstrapEmit := func(phase, level, msg string) { emit(phase, level, msg) }
	if err := bootstrap.Run(ctx, kubeconfig, bootstrapEmit); err != nil {
		return nil, fmt.Errorf("bootstrap kit: %w", err)
	}
	emit("bootstrap", "info", "Catalyst bootstrap kit fully installed — Sovereign is self-sufficient")

	return &Result{
		SovereignFQDN:    req.SovereignFQDN,
		ControlPlaneIP:   cpServer.PublicIP,
		LoadBalancerIP:   lb.PublicIP,
		KubeconfigBase64: kubeconfig,
		ConsoleURL:       fmt.Sprintf("https://console.%s", req.SovereignFQDN),
		GitOpsRepoURL:    fmt.Sprintf("https://gitea.%s", req.SovereignFQDN),
	}, nil
}

// validateProject calls GET /v1/servers as a lightweight probe that the
// token is valid and the project is reachable.
func (p *Provisioner) validateProject(ctx context.Context, req ProvisionRequest) error {
	body, status, err := p.callHetzner(ctx, http.MethodGet, "/v1/servers", req.HetznerToken, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("hetzner api returned %d on probe: %s", status, string(body))
	}
	return nil
}

// callHetzner is a thin Hetzner Cloud API wrapper. Returns (body, status, err).
// Larger production deployments would use hcloud-go; this in-tree client keeps
// the bootstrap binary dependency-light (no large external SDKs).
func (p *Provisioner) callHetzner(ctx context.Context, method, path, token string, body any) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("marshal body: %w", err)
		}
		reader = strings.NewReader(string(raw))
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://api.hetzner.cloud"+path, reader)
	if err != nil {
		return nil, 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("hetzner request: %w", err)
	}
	defer resp.Body.Close()

	out, _ := io.ReadAll(resp.Body)
	return out, resp.StatusCode, nil
}
