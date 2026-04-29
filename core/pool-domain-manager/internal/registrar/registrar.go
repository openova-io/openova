// Package registrar — provider-agnostic interface for the BYO Flow B
// "flip the customer's nameservers to OpenOva" use case.
//
// Registrar adapters live in subpackages (cloudflare/, namecheap/, godaddy/,
// ovh/, dynadot/) and implement the Registrar interface so PDM's HTTP
// handler can dispatch to any of them by name. This is the seam #166 (BYO
// Flow B) needs: catalyst-api hands the customer's API token to PDM, PDM
// asks the right adapter to validate the token + flip the nameservers,
// then catalyst-api carries on with /reserve.
//
// Per docs/INVIOLABLE-PRINCIPLES.md:
//
//   - #2 (never compromise quality): every adapter speaks each provider's
//     real public API; no shelling-out, no scraping, no "just call the
//     CLI for now" workarounds.
//   - #4 (never hardcode): each adapter's API base URL, sandbox flag, and
//     auth shape come from the constructor — defaults exist but are
//     overridable for tests + alternate environments.
//   - #10 (credential hygiene): tokens are scoped to a single Registrar
//     method call. The interface signature deliberately puts `token` on
//     each method (rather than embedding it in the adapter struct), so
//     callers can construct the adapter once and pass per-request tokens
//     through it without state leakage.
//
// Errors: the package defines a typed-error vocabulary so HTTP handlers
// can render appropriate status codes without string-matching. Adapters
// return these via fmt.Errorf("...: %w", ErrXxx) when they detect the
// canonical condition; unknown errors propagate unwrapped.
package registrar

import (
	"context"
	"errors"
)

// Registrar is the interface every supported registrar implements.
//
// Method semantics:
//
//   - Name returns a stable, lowercase, URL-safe identifier
//     ("cloudflare", "godaddy", ...). PDM's HTTP route uses this as the
//     {registrar} path param.
//
//   - ValidateToken proves the supplied credential CAN reach the
//     registrar's API and CAN see the named domain in the customer's
//     account. Returns nil on success, ErrInvalidToken /
//     ErrDomainNotInAccount / ErrRateLimited / ErrAPIUnavailable on
//     known failure modes, or a wrapped raw error otherwise.
//
//   - SetNameservers replaces the domain's nameserver list with the
//     supplied ns slice. Implementations MUST be idempotent: calling
//     SetNameservers twice with the same list is a no-op on the second
//     call (typically because the registrar reports "no change" or the
//     adapter checks before writing).
//
//   - GetNameservers reads the current nameserver list. Used by
//     integration tests + by /api/v1/registrar/{r}/set-ns to confirm the
//     write took effect before returning success.
//
// Token format note: each adapter accepts whatever shape its provider's
// API uses. Cloudflare/GoDaddy/Dynadot use a single string. Namecheap
// needs `apiUser:apiKey:clientIP`. OVH needs `appKey:appSecret:consumer`.
// The token-shape parsing lives in the adapter so the interface stays
// minimal.
type Registrar interface {
	Name() string
	ValidateToken(ctx context.Context, token, domain string) error
	SetNameservers(ctx context.Context, token, domain string, ns []string) error
	GetNameservers(ctx context.Context, token, domain string) ([]string, error)
}

// Typed errors that adapters return so HTTP handlers can map to status
// codes without string matching.
//
// HTTP status mapping convention used by handler.SetNameservers:
//
//	ErrInvalidToken        → 401 Unauthorized
//	ErrDomainNotInAccount  → 403 Forbidden
//	ErrRateLimited         → 429 Too Many Requests
//	ErrAPIUnavailable      → 502 Bad Gateway
//	ErrUnsupportedRegistrar→ 404 Not Found  (router-level; no adapter)
//	other                  → 500 Internal Server Error
var (
	// ErrInvalidToken — adapter contacted the registrar's API and got
	// back a 401/403 indicating the token is wrong/expired/revoked.
	// Distinct from ErrDomainNotInAccount: the token is fine but the
	// domain isn't owned by the account behind that token.
	ErrInvalidToken = errors.New("registrar: invalid token")

	// ErrRateLimited — registrar's API returned 429 (or its provider-
	// specific equivalent, e.g. Cloudflare's 1015). Caller should back
	// off and retry. The handler surfaces this so the wizard can retry
	// without burning the customer's token.
	ErrRateLimited = errors.New("registrar: rate-limited")

	// ErrDomainNotInAccount — the token authenticates fine but the
	// requested domain isn't visible to that account. Common if the
	// customer hands us a token from a different sub-account or scope.
	ErrDomainNotInAccount = errors.New("registrar: domain not in account")

	// ErrAPIUnavailable — registrar's API returned 5xx, timed out, or
	// the network is down. Distinct from ErrInvalidToken: the request
	// never got authoritatively accepted/rejected.
	ErrAPIUnavailable = errors.New("registrar: api unavailable")

	// ErrUnsupportedRegistrar — used by the registry lookup in the
	// HTTP handler when the URL path names a registrar we don't have
	// an adapter for.
	ErrUnsupportedRegistrar = errors.New("registrar: unsupported")
)

// Registry is a name→Registrar map the HTTP handler dispatches against.
// Build once at startup with the adapters the deployment supports; pass
// the resulting Registry into handler.New.
type Registry map[string]Registrar

// Lookup returns the registrar for the given name (case-insensitive on
// the caller's side — handler normalises before calling) or
// ErrUnsupportedRegistrar.
func (r Registry) Lookup(name string) (Registrar, error) {
	if r == nil {
		return nil, ErrUnsupportedRegistrar
	}
	a, ok := r[name]
	if !ok {
		return nil, ErrUnsupportedRegistrar
	}
	return a, nil
}

// Names returns the sorted list of registered adapter names. Used by
// /healthz so operators can see which adapters are wired in.
func (r Registry) Names() []string {
	out := make([]string, 0, len(r))
	for k := range r {
		out = append(out, k)
	}
	// Tiny insertion sort — registry size is at most a handful.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}
