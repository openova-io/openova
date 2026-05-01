// Package objectstorage is the vendor-agnostic seam for Object Storage
// credential validation (issue #425).
//
// The Provider interface below is the canonical Go-side seam every cloud
// integration plugs into. Hetzner is the only impl shipped as of #425 —
// AWS/GCP/Azure/OCI follow as separate tickets, each adding a sibling
// package under internal/objectstorage/<provider>/ that returns its own
// Provider implementation. NOTHING above this package (handler/, the
// wizard payload field names, the chart values block, the Sealed Secret
// name) carries the vendor name; only the impl directory does.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #3 (Crossplane is the only Day-2
// cloud-API mutation seam) the Provider does READ-ONLY validation —
// ListBuckets to confirm a credential pair authenticates and has S3
// permissions. Mutation (bucket creation, ACL set, etc.) belongs in
// either Phase-0 OpenTofu (one-shot at provision) or Day-2 Crossplane
// XRC writes against the Provider+ProviderConfig planted by cloud-init.
//
// Why this lives at internal/objectstorage/ and not internal/<provider>/:
// the wizard's Object Storage validation handler resolves the right
// Provider implementation by `provider` field at request time. If each
// cloud's impl lived in its own top-level package, the handler would
// switch on every new vendor — the same vendor-coupling violation #425
// is closing. Centralising the Provider interface here keeps the seam
// vendor-agnostic at the call site.
package objectstorage

import (
	"context"
	"errors"
	"fmt"
)

// Provider validates Object Storage credentials against a cloud
// provider's S3 endpoint without mutating any state. Implementations
// MUST treat the call as read-only — ListBuckets is the canonical
// probe; uploading a sentinel object to confirm write permission is
// out of scope (the wizard only gates on "credentials authenticate +
// can list", and the upstream chart's first real upload surfaces a
// permission failure with full context).
type Provider interface {
	// Endpoint returns the canonical S3 endpoint hostname (no scheme)
	// for a region. Returns the empty string for unrecognised regions
	// so callers can surface "unknown region" before a doomed network
	// request.
	Endpoint(region string) string

	// Validate runs ListBuckets against the provider's S3 endpoint
	// with the operator-supplied access/secret pair.
	//   (true,  nil)  — credentials authenticate AND can list buckets
	//   (false, nil)  — credentials rejected (401/403/InvalidAccessKey)
	//   (false, err)  — network/upstream failure (timeout, DNS, 5xx)
	//
	// Per docs/INVIOLABLE-PRINCIPLES.md #10 the keys are NEVER logged
	// inside the impl. Only the failure category surfaces to the
	// handler's structured log.
	Validate(ctx context.Context, region, accessKey, secretKey string) (bool, error)
}

// ErrUnsupportedProvider is returned by Resolve when the vendor name
// has no compiled-in Provider implementation. The wizard surfaces this
// as a 400-level config error rather than retrying upstream.
var ErrUnsupportedProvider = errors.New("unsupported object storage provider")

// providerRegistry holds one entry per compiled-in cloud provider.
// Implementations register themselves at package init time via Register.
var providerRegistry = map[string]Provider{}

// Register makes a Provider available under name (case-insensitive).
// Called from the impl package's init() — see internal/objectstorage/
// hetzner/hetzner.go for the canonical pattern.
func Register(name string, p Provider) {
	if p == nil {
		panic("objectstorage: cannot Register nil Provider for " + name)
	}
	providerRegistry[name] = p
}

// Resolve returns the Provider for a given vendor name (e.g. "hetzner").
// Returns ErrUnsupportedProvider if no impl is registered.
func Resolve(name string) (Provider, error) {
	if p, ok := providerRegistry[name]; ok {
		return p, nil
	}
	return nil, fmt.Errorf("%w: %q", ErrUnsupportedProvider, name)
}

// MustResolve is the convenience wrapper for handlers that have already
// validated the provider name through the wizard payload. Panics on
// unknown — which surfaces as a 500 the operator sees as "wizard out
// of sync with backend"; the handler-level validation at call sites
// SHOULD always Resolve first and fail with 400.
func MustResolve(name string) Provider {
	p, err := Resolve(name)
	if err != nil {
		panic(err)
	}
	return p
}
