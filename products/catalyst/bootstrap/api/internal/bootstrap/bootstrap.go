// Package bootstrap installs the 11-component Catalyst bootstrap kit into
// a freshly-provisioned k3s cluster, in the dependency order specified in
// docs/SOVEREIGN-PROVISIONING.md §3 Phase 0.
//
// The order is non-negotiable:
//   1. Cilium                  (CNI must come first, k3s is started with --flannel-backend=none)
//   2. cert-manager            (TLS for everything below)
//   3. Flux                    (host-level GitOps engine — all subsequent installs go via Git)
//   4. Crossplane + provider   (cloud resource control plane)
//   5. Sealed Secrets          (transient — only for bootstrap secrets)
//   6. SPIRE server + agent    (workload identity)
//   7. NATS JetStream cluster  (3 nodes, event spine)
//   8. OpenBao cluster         (3 nodes, region-local Raft)
//   9. Keycloak                (per Sovereign-CRD keycloakTopology)
//  10. Gitea                   (with public Blueprint mirror seeded)
//  11. bp-catalyst-platform    (umbrella that registers Catalyst CRDs)
//
// Each step uses a small kubectl/helm wrapper that talks to the cluster
// via the kubeconfig the Hetzner provisioner returned. Steps 1–4 are
// authored as direct apply; step 5 onward use Flux Kustomizations against
// the public OpenOva repo so they self-update as new Blueprint versions
// publish.
package bootstrap

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Step represents a single bootstrap-kit installation phase.
type Step struct {
	// Name is human-readable for the wizard's progress UI.
	Name string
	// Phase is the machine-readable phase identifier matching the
	// hetzner.Event.Phase value.
	Phase string
	// Install installs the step. Returns an error to halt the whole
	// bootstrap (each step is required for the next).
	Install func(ctx context.Context, kubeconfig string, emit EmitFunc) error
}

// EmitFunc is how a Step reports progress events back to the wizard.
type EmitFunc func(phase, level, message string)

// Run installs the full kit in order. Aborts on the first error.
func Run(ctx context.Context, kubeconfig string, emit EmitFunc) error {
	steps := DefaultSteps()
	for i, step := range steps {
		emit("bootstrap", "info", fmt.Sprintf("[%d/%d] Installing %s", i+1, len(steps), step.Name))
		stepCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		err := step.Install(stepCtx, kubeconfig, emit)
		cancel()
		if err != nil {
			return fmt.Errorf("step %s failed: %w", step.Name, err)
		}
		emit("bootstrap", "info", fmt.Sprintf("[%d/%d] %s installed", i+1, len(steps), step.Name))
	}
	return nil
}

// DefaultSteps returns the canonical 11-step bootstrap kit. Tests may
// override entries; production always uses this exact list.
func DefaultSteps() []Step {
	return []Step{
		{Name: "Cilium CNI + Service Mesh", Phase: "cilium", Install: installCilium},
		{Name: "cert-manager", Phase: "cert-manager", Install: installCertManager},
		{Name: "Flux GitOps", Phase: "flux", Install: installFlux},
		{Name: "Crossplane + provider-hcloud", Phase: "crossplane", Install: installCrossplane},
		{Name: "Sealed Secrets (transient)", Phase: "sealed-secrets", Install: installSealedSecrets},
		{Name: "SPIRE workload identity", Phase: "spire", Install: installSpire},
		{Name: "NATS JetStream (3-node)", Phase: "nats-jetstream", Install: installNATS},
		{Name: "OpenBao (3-node Raft)", Phase: "openbao", Install: installOpenBao},
		{Name: "Keycloak", Phase: "keycloak", Install: installKeycloak},
		{Name: "Gitea (per-Sovereign Git server)", Phase: "gitea", Install: installGitea},
		{Name: "bp-catalyst-platform umbrella", Phase: "catalyst-platform", Install: installCatalystPlatform},
	}
}

// installCilium applies the Cilium Helm chart with Catalyst-curated values
// from the public bp-cilium OCI artifact. Cilium replaces both flannel and
// kube-proxy that k3s normally ships with — we passed --flannel-backend=none
// at k3s install time precisely so Cilium can take over.
func installCilium(ctx context.Context, kubeconfig string, emit EmitFunc) error {
	emit("cilium", "info", "Adding Cilium Helm repo and installing chart with --kubeProxyReplacement=true")
	return runHelm(ctx, kubeconfig, "install", []string{
		"cilium",
		"oci://ghcr.io/openova-io/bp-cilium",
		"--version", "1.16.5",
		"--namespace", "kube-system",
		"--values", "-",
	}, ciliumValues())
}

func ciliumValues() string {
	// Per platform/cilium/README.md: kubeProxyReplacement, hubble UI, gateway API,
	// WireGuard mTLS, L2 announcements, OTel-friendly metrics.
	return `
kubeProxyReplacement: true
k8sServiceHost: 127.0.0.1
k8sServicePort: 6443
encryption:
  enabled: true
  type: wireguard
hubble:
  enabled: true
  relay:
    enabled: true
  ui:
    enabled: true
  metrics:
    enabled:
      - dns
      - drop
      - tcp
      - flow
      - icmp
      - http
gatewayAPI:
  enabled: true
envoy:
  enabled: true
ipam:
  mode: kubernetes
operator:
  replicas: 1
`
}

// installCertManager via Helm. Issuer (Let's Encrypt with DNS-01 via Dynadot)
// is applied as a follow-up manifest after the chart's CRDs land.
func installCertManager(ctx context.Context, kubeconfig string, emit EmitFunc) error {
	emit("cert-manager", "info", "Installing cert-manager with CRDs")
	if err := runHelm(ctx, kubeconfig, "install", []string{
		"cert-manager",
		"oci://ghcr.io/openova-io/bp-cert-manager",
		"--version", "1.16.2",
		"--namespace", "cert-manager",
		"--create-namespace",
		"--set", "crds.enabled=true",
	}, ""); err != nil {
		return err
	}
	emit("cert-manager", "info", "Waiting for cert-manager webhook to be Ready")
	return waitForDeployment(ctx, kubeconfig, "cert-manager", "cert-manager-webhook", 5*time.Minute)
}

// installFlux bootstraps Flux pointing at this monorepo (the public OpenOva
// repo). The Sovereign-specific Kustomizations live at clusters/{sovereignFQDN}/
// in this repo and are Crossplane-managed thereafter.
func installFlux(ctx context.Context, kubeconfig string, emit EmitFunc) error {
	emit("flux", "info", "Installing Flux components (source/kustomize/helm controllers)")
	return runHelm(ctx, kubeconfig, "install", []string{
		"flux",
		"oci://ghcr.io/openova-io/bp-flux",
		"--version", "2.4.0",
		"--namespace", "flux-system",
		"--create-namespace",
	}, "")
}

// installCrossplane installs Crossplane core + provider-hcloud so the new
// Sovereign can manage its own Hetzner resources via CRDs going forward.
// This is the "Phase 1 Hand-off" point in SOVEREIGN-PROVISIONING.md §4 —
// after this step, Catalyst is self-sufficient and no longer depends on
// Catalyst-Zero (the catalyst-provisioner that did the initial bootstrap).
func installCrossplane(ctx context.Context, kubeconfig string, emit EmitFunc) error {
	emit("crossplane", "info", "Installing Crossplane core")
	if err := runHelm(ctx, kubeconfig, "install", []string{
		"crossplane",
		"oci://ghcr.io/openova-io/bp-crossplane",
		"--version", "1.18.0",
		"--namespace", "crossplane-system",
		"--create-namespace",
	}, ""); err != nil {
		return err
	}
	emit("crossplane", "info", "Installing provider-hcloud + ProviderConfig")
	return applyManifest(ctx, kubeconfig, crossplaneProviderHcloudManifest())
}

func installSealedSecrets(ctx context.Context, kubeconfig string, emit EmitFunc) error {
	emit("sealed-secrets", "info", "Installing transient bootstrap-only Sealed Secrets controller")
	return runHelm(ctx, kubeconfig, "install", []string{
		"sealed-secrets",
		"oci://ghcr.io/openova-io/bp-sealed-secrets",
		"--version", "2.16.1",
		"--namespace", "kube-system",
	}, "")
}

func installSpire(ctx context.Context, kubeconfig string, emit EmitFunc) error {
	emit("spire", "info", "Installing SPIRE server + agent (5-min SVID rotation)")
	return runHelm(ctx, kubeconfig, "install", []string{
		"spire",
		"oci://ghcr.io/openova-io/bp-spire",
		"--version", "0.21.0",
		"--namespace", "spire-system",
		"--create-namespace",
	}, "")
}

func installNATS(ctx context.Context, kubeconfig string, emit EmitFunc) error {
	emit("nats-jetstream", "info", "Installing 3-node NATS JetStream cluster (control-plane event spine)")
	return runHelm(ctx, kubeconfig, "install", []string{
		"nats",
		"oci://ghcr.io/openova-io/bp-nats-jetstream",
		"--version", "1.2.0",
		"--namespace", "nats-system",
		"--create-namespace",
	}, "")
}

func installOpenBao(ctx context.Context, kubeconfig string, emit EmitFunc) error {
	emit("openbao", "info", "Installing OpenBao 3-node Raft cluster (region-local, no stretched cluster — see SECURITY.md §5)")
	return runHelm(ctx, kubeconfig, "install", []string{
		"openbao",
		"oci://ghcr.io/openova-io/bp-openbao",
		"--version", "2.1.0",
		"--namespace", "openbao",
		"--create-namespace",
	}, "")
}

func installKeycloak(ctx context.Context, kubeconfig string, emit EmitFunc) error {
	emit("keycloak", "info", "Installing Keycloak (topology decided by Sovereign CRD spec.keycloakTopology)")
	return runHelm(ctx, kubeconfig, "install", []string{
		"keycloak",
		"oci://ghcr.io/openova-io/bp-keycloak",
		"--version", "25.0.6",
		"--namespace", "keycloak",
		"--create-namespace",
	}, "")
}

func installGitea(ctx context.Context, kubeconfig string, emit EmitFunc) error {
	emit("gitea", "info", "Installing Gitea (per-Sovereign Git server, mirrors public Blueprint catalog)")
	return runHelm(ctx, kubeconfig, "install", []string{
		"gitea",
		"oci://ghcr.io/openova-io/bp-gitea",
		"--version", "10.5.0",
		"--namespace", "gitea",
		"--create-namespace",
	}, "")
}

func installCatalystPlatform(ctx context.Context, kubeconfig string, emit EmitFunc) error {
	emit("catalyst-platform", "info", "Installing bp-catalyst-platform umbrella — registers Catalyst CRDs and starts the per-Sovereign control plane")
	return runHelm(ctx, kubeconfig, "install", []string{
		"catalyst",
		"oci://ghcr.io/openova-io/bp-catalyst-platform",
		"--version", "1.0.0",
		"--namespace", "catalyst-system",
		"--create-namespace",
	}, "")
}

func crossplaneProviderHcloudManifest() string {
	return strings.TrimSpace(`
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: provider-hcloud
spec:
  package: xpkg.upbound.io/crossplane-contrib/provider-hcloud:v0.4.0
`) + "\n"
}
