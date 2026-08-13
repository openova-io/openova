// clientcert.go — mTLS client-certificate authentication, the SECOND
// credential presentation the proxy accepts. It is an alternative to
// the X-Catalyst-HMAC header pair, never a replacement: both produce
// the same authorization outcome, and hmac.go is untouched by this
// file.
//
// WHY THIS EXISTS (#5991 / UAT row 115). The HMAC contract is
// "compute a signature over (timestamp, path) and put it in two
// request headers". A browser SPA and catalyst-api can both do that.
// Apache guacd CANNOT: its `kubernetes` protocol handler builds the
// WebSocket upgrade itself via libwebsockets and exposes no hook for
// custom HTTP headers (guacamole-server 1.5.5,
// src/protocols/kubernetes/kubernetes.c — the lws_client_connect_info
// it fills in carries host/address/origin/port/protocol/path and
// nothing else). What it DOES expose is TLS client-certificate
// material: the `client-cert`, `client-key` and `ca-cert` connection
// parameters, read as in-memory PEM in
// src/protocols/kubernetes/ssl.c. So the only credential guacd can
// present to this proxy is an X.509 client certificate, and until it
// could present one, no Guacamole connection through this proxy could
// ever authenticate — which is why row 115 had no producer that
// survived the "does it work when clicked?" test.
//
// Wire contract:
//
//	TLS handshake on the proxy's TLS listener, with the caller
//	presenting a certificate that chains to TLS_CLIENT_CA_FILE.
//	The Go TLS stack does the chain verification (tls.ClientAuth =
//	VerifyClientCertIfGiven) BEFORE any HTTP handler runs, so a
//	certificate signed by an unknown CA fails the handshake and
//	never reaches this code.
//
// This file adds the SECOND gate on top of that: chain-verified is not
// enough, the certificate's identity must also be on an explicit
// allowlist. Without that second gate the proxy would accept every
// certificate the CA ever issues, which for a CA that also signs
// server certs is close to accepting anything. The allowlist is what
// makes "auth accepted" a statement about WHO, not merely about WHETHER
// a handshake completed.
//
// Fail-closed: an empty allowlist DISABLES client-certificate auth
// outright (NewCertVerifier returns ErrClientCertAuthDisabled and
// main.go leaves the verifier nil). Merely turning TLS on therefore
// never opens a new authentication mode by accident.
package auth

import (
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// Sentinel errors for the client-certificate leg. Callers map these to
// a 401 exactly as they do the HMAC sentinels.
var (
	// ErrClientCertAuthDisabled is returned by NewCertVerifier when no
	// subject is allowlisted. It is a CONSTRUCTION error, never a
	// per-request verdict: with no allowlist there is no identity the
	// proxy could accept, so the mode stays off.
	ErrClientCertAuthDisabled = errors.New("auth: client-certificate auth disabled (no allowed subjects configured)")

	// ErrNoTLS is returned when the request did not arrive over TLS at
	// all — the plaintext listener can never authenticate by
	// certificate.
	ErrNoTLS = errors.New("auth: request did not arrive over TLS")

	// ErrNoClientCert is returned when the TLS peer presented no
	// certificate.
	ErrNoClientCert = errors.New("auth: no client certificate presented")

	// ErrClientCertUnverified is returned when the TLS stack accepted
	// the connection but produced no verified chain. With
	// VerifyClientCertIfGiven this means the certificate was optional
	// and unverifiable; treat it as no credential at all.
	ErrClientCertUnverified = errors.New("auth: client certificate has no verified chain")

	// ErrClientCertSubjectDenied is returned when the certificate
	// chains correctly but its identity is not allowlisted. This is
	// the verdict that makes the mode about WHO rather than WHETHER.
	ErrClientCertSubjectDenied = errors.New("auth: client certificate subject not allowed")
)

// CertVerifier decides whether a chain-verified client certificate
// belongs to a caller this proxy accepts. Safe for concurrent use;
// the allowlist is immutable after construction.
type CertVerifier struct {
	allowed map[string]struct{}
}

// NewCertVerifier builds a verifier from an explicit subject
// allowlist. A subject matches when it equals the certificate's
// Subject CommonName OR one of its DNS SANs — both are stable
// cert-manager outputs (`commonName:` and `dnsNames:` on the
// Certificate CR), so an operator can allowlist whichever the issuing
// policy pins.
//
// Returns ErrClientCertAuthDisabled when the allowlist is empty, so a
// caller cannot accidentally build an accept-everything verifier by
// passing nil.
func NewCertVerifier(subjects []string) (*CertVerifier, error) {
	allowed := make(map[string]struct{}, len(subjects))
	for _, s := range subjects {
		s = strings.TrimSpace(s)
		if s != "" {
			allowed[s] = struct{}{}
		}
	}
	if len(allowed) == 0 {
		return nil, ErrClientCertAuthDisabled
	}
	return &CertVerifier{allowed: allowed}, nil
}

// AllowedSubjects returns the allowlist in sorted order. Exposed for
// startup logging and for tests that pin the configured identity
// rather than merely the accept/reject outcome.
func (v *CertVerifier) AllowedSubjects() []string {
	out := make([]string, 0, len(v.allowed))
	for s := range v.allowed {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Presented reports whether the request carried a TLS client
// certificate at all. It is deliberately separate from VerifyRequest:
// the Authorizer uses it to decide whether the caller is ATTEMPTING
// certificate auth, so that a TLS request with no certificate falls
// through to the HMAC leg instead of being denied for the wrong
// reason.
func (v *CertVerifier) Presented(r *http.Request) bool {
	return r != nil && r.TLS != nil && len(r.TLS.PeerCertificates) > 0
}

// VerifyRequest returns nil iff the request arrived over TLS, carried
// a client certificate with a chain the TLS stack verified against the
// configured client CA, and that certificate's identity is
// allowlisted.
func (v *CertVerifier) VerifyRequest(r *http.Request) error {
	if r == nil || r.TLS == nil {
		return ErrNoTLS
	}
	if len(r.TLS.PeerCertificates) == 0 {
		return ErrNoClientCert
	}
	// VerifiedChains is populated by the Go TLS stack only after it
	// has walked the presented chain to a root in ClientCAs. Checking
	// it (rather than trusting PeerCertificates, which is whatever the
	// peer sent) is what keeps an unsigned or self-signed certificate
	// from authenticating.
	if len(r.TLS.VerifiedChains) == 0 {
		return ErrClientCertUnverified
	}
	leaf := r.TLS.PeerCertificates[0]
	if subject, ok := v.match(leaf); ok {
		_ = subject
		return nil
	}
	return fmt.Errorf("%w: cn=%q dns=%v", ErrClientCertSubjectDenied, leaf.Subject.CommonName, leaf.DNSNames)
}

// Subject returns the allowlisted identity the certificate matched, or
// "" when it matches nothing. Used for structured logging so an
// operator can see WHICH caller authenticated, not just that one did.
func (v *CertVerifier) Subject(cert *x509.Certificate) string {
	s, _ := v.match(cert)
	return s
}

func (v *CertVerifier) match(cert *x509.Certificate) (string, bool) {
	if cert == nil {
		return "", false
	}
	if cn := strings.TrimSpace(cert.Subject.CommonName); cn != "" {
		if _, ok := v.allowed[cn]; ok {
			return cn, true
		}
	}
	for _, dns := range cert.DNSNames {
		if _, ok := v.allowed[strings.TrimSpace(dns)]; ok {
			return dns, true
		}
	}
	return "", false
}
