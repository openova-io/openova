// authorize.go — the ONE place that decides whether an inbound request
// is authenticated, and by which credential.
//
// Before #5991 the decision was a single inline `Verifier.VerifyRequest`
// call inside the HTTP handler, so "which credentials does this proxy
// accept?" was answerable only by reading the handler. With two
// presentations of the same authority (HMAC headers, mTLS client
// certificate) that inline call becomes a policy, and a policy that
// lives inside an HTTP handler is a policy no unit test can pin
// without standing up a server. Authorizer is that policy, extracted:
// one struct, one method, no net/http server required to exercise it.
//
// The rules, in order:
//
//  1. If the caller PRESENTED a client certificate and certificate auth
//     is enabled, the certificate decides — accept or deny, no fallback.
//     Falling through to HMAC on a rejected certificate would let a
//     caller mask a denied identity behind a second credential, and
//     would make the 401 reason unreadable in logs.
//  2. Otherwise the HMAC headers decide. This is byte-for-byte the
//     pre-#5991 behaviour, and it is what every plaintext-listener
//     caller (catalyst-api, the console SPA) continues to use.
//
// Consequence worth stating plainly: turning TLS on does NOT turn
// certificate auth on. Certificate auth exists only when an operator
// configures an explicit subject allowlist (see NewCertVerifier), so
// the default posture of a TLS-enabled proxy is still HMAC-only.
package auth

import (
	"errors"
	"net/http"
)

// Mode names the credential that authenticated a request. Returned by
// Authorize so the handler can log + meter the two legs separately
// instead of inferring which one fired.
type Mode string

const (
	// ModeHMAC — authenticated by the X-Catalyst-Timestamp +
	// X-Catalyst-HMAC header pair.
	ModeHMAC Mode = "hmac"

	// ModeClientCert — authenticated by a TLS client certificate that
	// chains to the configured client CA and whose subject is
	// allowlisted.
	ModeClientCert Mode = "mtls"

	// ModeNone — no credential was accepted. Always accompanied by a
	// non-nil error.
	ModeNone Mode = ""
)

// ErrNoVerifier is returned by NewAuthorizer when neither leg is
// configured. A proxy that can authenticate nothing must fail at
// startup, not serve an open exec endpoint.
var ErrNoVerifier = errors.New("auth: authorizer needs at least an HMAC verifier")

// Authorizer holds the configured credential legs. cert may be nil,
// which means client-certificate auth is disabled; hmac is required.
type Authorizer struct {
	hmac *Verifier
	cert *CertVerifier
}

// NewAuthorizer wires the legs. hmac is REQUIRED — the HMAC contract is
// the proxy's original and still-primary credential, and every existing
// caller depends on it. cert is optional; pass nil to keep
// certificate auth off.
func NewAuthorizer(hmac *Verifier, cert *CertVerifier) (*Authorizer, error) {
	if hmac == nil {
		return nil, ErrNoVerifier
	}
	return &Authorizer{hmac: hmac, cert: cert}, nil
}

// ClientCertEnabled reports whether the certificate leg is configured.
// Exposed for startup logging and for tests that assert the mode is OFF
// by default rather than merely unused.
func (a *Authorizer) ClientCertEnabled() bool {
	return a != nil && a.cert != nil
}

// Authorize applies the policy above and returns the mode that
// authenticated the request. On failure it returns ModeNone and the
// sentinel error from whichever leg made the decision, so the caller
// can log the real reason (bad signature vs denied subject) instead of
// a generic "unauthorized".
func (a *Authorizer) Authorize(r *http.Request) (Mode, error) {
	if a == nil || a.hmac == nil {
		return ModeNone, ErrNoVerifier
	}
	if a.cert != nil && a.cert.Presented(r) {
		if err := a.cert.VerifyRequest(r); err != nil {
			return ModeNone, err
		}
		return ModeClientCert, nil
	}
	if err := a.hmac.VerifyRequest(r); err != nil {
		return ModeNone, err
	}
	return ModeHMAC, nil
}
