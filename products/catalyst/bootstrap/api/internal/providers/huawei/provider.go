// Package huawei is the providers.CloudProvider adapter for Huawei
// Cloud — STUB IMPLEMENTATION FOR WAVE 2.
//
// Architectural role: same as providers/hetzner — translates the
// providers.CloudProvider interface to the per-cloud SDK + OpenTofu
// module. Wave 2 (this PR) registers a stub so providers.Get("huawei")
// returns a real *Provider that satisfies the interface but every
// method returns errNotImplemented. Wave 3 (planned) fills in the
// concrete bodies against the Huawei Cloud SDK + a new
// infra/providers/huawei/ tofu module.
//
// Tracking: Issue #1841 (DoD A6 — provider-mix multi-cloud canonical
// test case).
//
// Per docs/INVIOLABLE-PRINCIPLES.md #14 (target-state shape, no
// workarounds): the stub MUST satisfy the interface today so the
// rest of the platform can be reshaped to use providers.Get() without
// waiting for Huawei to be production-ready. The stub returns clear
// "not implemented" errors so any handler that calls into it on a
// huawei-typed deployment fails loudly rather than silently
// producing wrong cloud resources.

package huawei

import (
	"context"
	"errors"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
)

// Name is the canonical provider identifier ("huawei"). MUST match
// the enum row in core/controllers/environment/api/v1/types.go.
const Name = "huawei"

// errNotImplemented is the sentinel surfaced by every method of the
// stub. Includes the issue + wave so an operator hitting it on a
// fresh deployment can find the tracking ticket immediately.
var errNotImplemented = errors.New("huawei provider: not implemented yet — Wave 3 (Issue #1841 — DoD A6 provider-mix)")

// Provider is the stub implementation. Satisfies providers.CloudProvider
// so the platform code that switches on dep.Provider compiles + the
// registry can return it; every method body is errNotImplemented.
type Provider struct{}

// New returns a ready-to-use stub. Zero-cost; no I/O.
func New() *Provider { return &Provider{} }

// init registers the stub so providers.Get("huawei") returns a
// usable interface value. Per registry.go RegisterProvider contract.
func init() {
	providers.RegisterProvider(Name, New())
}

// Name returns the canonical provider name.
func (p *Provider) Name() string { return Name }

// Provision — Wave 3.
func (p *Provider) Provision(ctx context.Context, spec providers.ProvisionSpec, events chan<- providers.ProvisionEvent) (*providers.ProvisionResult, error) {
	return nil, errNotImplemented
}

// Wipe — Wave 3.
func (p *Provider) Wipe(ctx context.Context, spec providers.WipeSpec, progress func(msg string)) (*providers.WipeResult, error) {
	return nil, errNotImplemented
}

// ListServers — Wave 3.
func (p *Provider) ListServers(ctx context.Context, deploymentID, sovereignFQDN string, creds providers.ProviderCreds) ([]providers.ServerInfo, error) {
	return nil, errNotImplemented
}

// ValidateCreds — Wave 3. The canonical Huawei credential shape will
// be: ak (access key), sk (secret key), project_id, region. Wave 3
// will issue an IAM token-mint against the project to confirm the
// AK/SK pair authenticates.
func (p *Provider) ValidateCreds(ctx context.Context, creds providers.ProviderCreds) error {
	return errNotImplemented
}
