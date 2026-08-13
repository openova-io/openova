package auth

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"
)

// req builds a request whose TLS state is exactly what the Go TLS stack
// would hand a handler. VerifiedChains is the field the verifier keys
// on, so these tests set it explicitly rather than inferring it.
func req(peer []*x509.Certificate, verified bool) *http.Request {
	r := &http.Request{URL: &url.URL{Path: "/proxy/exec/ns/pod/c"}, Header: http.Header{}}
	if peer == nil {
		return r
	}
	st := &tls.ConnectionState{PeerCertificates: peer}
	if verified {
		st.VerifiedChains = [][]*x509.Certificate{peer}
	}
	r.TLS = st
	return r
}

func leaf(cn string, dns ...string) *x509.Certificate {
	c := &x509.Certificate{DNSNames: dns}
	c.Subject.CommonName = cn
	return c
}

func TestNewCertVerifier_EmptyAllowlistDisablesTheMode(t *testing.T) {
	for _, in := range [][]string{nil, {}, {"", "  "}} {
		v, err := NewCertVerifier(in)
		if !errors.Is(err, ErrClientCertAuthDisabled) {
			t.Fatalf("NewCertVerifier(%v) err = %v, want ErrClientCertAuthDisabled", in, err)
		}
		if v != nil {
			t.Fatalf("NewCertVerifier(%v) returned a verifier — an empty allowlist must never produce an accept-anything verifier", in)
		}
	}
}

func TestCertVerifier_AcceptsAllowlistedCN(t *testing.T) {
	v, err := NewCertVerifier([]string{"guacd.guacamole.svc"})
	if err != nil {
		t.Fatal(err)
	}
	if err := v.VerifyRequest(req([]*x509.Certificate{leaf("guacd.guacamole.svc")}, true)); err != nil {
		t.Fatalf("allowlisted CN rejected: %v", err)
	}
}

func TestCertVerifier_AcceptsAllowlistedDNSSAN(t *testing.T) {
	// cert-manager pins identity in dnsNames as often as in commonName,
	// so both must satisfy the allowlist.
	v, err := NewCertVerifier([]string{"guacd.guacamole.svc"})
	if err != nil {
		t.Fatal(err)
	}
	if err := v.VerifyRequest(req([]*x509.Certificate{leaf("some-other-cn", "guacd.guacamole.svc")}, true)); err != nil {
		t.Fatalf("allowlisted DNS SAN rejected: %v", err)
	}
}

// TestCertVerifier_DeniesUnlistedSubject is the control at unit level:
// the certificate is verified (same chain state as the accepted case),
// only the identity differs.
func TestCertVerifier_DeniesUnlistedSubject(t *testing.T) {
	v, err := NewCertVerifier([]string{"guacd.guacamole.svc"})
	if err != nil {
		t.Fatal(err)
	}
	err = v.VerifyRequest(req([]*x509.Certificate{leaf("intruder.guacamole.svc", "also-wrong")}, true))
	if !errors.Is(err, ErrClientCertSubjectDenied) {
		t.Fatalf("err = %v, want ErrClientCertSubjectDenied", err)
	}
}

func TestCertVerifier_DeniesUnverifiedChain(t *testing.T) {
	v, err := NewCertVerifier([]string{"guacd.guacamole.svc"})
	if err != nil {
		t.Fatal(err)
	}
	// Right name, no verified chain — i.e. the peer sent a certificate
	// the TLS stack could not walk to a trusted root. Trusting
	// PeerCertificates alone here would authenticate anyone able to
	// type the CN into a self-signed certificate.
	err = v.VerifyRequest(req([]*x509.Certificate{leaf("guacd.guacamole.svc")}, false))
	if !errors.Is(err, ErrClientCertUnverified) {
		t.Fatalf("err = %v, want ErrClientCertUnverified", err)
	}
}

func TestCertVerifier_DeniesPlaintextAndCertless(t *testing.T) {
	v, err := NewCertVerifier([]string{"guacd.guacamole.svc"})
	if err != nil {
		t.Fatal(err)
	}
	if err := v.VerifyRequest(req(nil, false)); !errors.Is(err, ErrNoTLS) {
		t.Fatalf("plaintext: err = %v, want ErrNoTLS", err)
	}
	r := &http.Request{URL: &url.URL{Path: "/x"}, TLS: &tls.ConnectionState{}}
	if err := v.VerifyRequest(r); !errors.Is(err, ErrNoClientCert) {
		t.Fatalf("TLS without a certificate: err = %v, want ErrNoClientCert", err)
	}
}

func TestCertVerifier_Presented(t *testing.T) {
	v, _ := NewCertVerifier([]string{"x"})
	if v.Presented(req(nil, false)) {
		t.Fatal("Presented true for a plaintext request")
	}
	if v.Presented(&http.Request{TLS: &tls.ConnectionState{}}) {
		t.Fatal("Presented true for a TLS request carrying no certificate")
	}
	if !v.Presented(req([]*x509.Certificate{leaf("x")}, true)) {
		t.Fatal("Presented false when a certificate was on the wire")
	}
}

func TestCertVerifier_AllowedSubjectsIsSortedAndDeduped(t *testing.T) {
	v, err := NewCertVerifier([]string{"b", "a", "b", " "})
	if err != nil {
		t.Fatal(err)
	}
	got := v.AllowedSubjects()
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("AllowedSubjects = %v, want [a b]", got)
	}
}

// ── Authorizer: the policy seam ───────────────────────────────────────

func hmacHeaders(t *testing.T, secret []byte, path string) http.Header {
	t.Helper()
	now := time.Now().Unix()
	h := http.Header{}
	h.Set(HeaderTimestamp, formatInt(now))
	h.Set(HeaderHMAC, ComputeHex(secret, now, path))
	return h
}

func formatInt(v int64) string {
	const digits = "0123456789"
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = digits[v%10]
		v /= 10
	}
	return string(buf[i:])
}

func TestNewAuthorizer_RequiresHMACLeg(t *testing.T) {
	cv, _ := NewCertVerifier([]string{"x"})
	if _, err := NewAuthorizer(nil, cv); !errors.Is(err, ErrNoVerifier) {
		t.Fatalf("err = %v, want ErrNoVerifier — a proxy that can authenticate nothing must fail at startup", err)
	}
}

func TestAuthorizer_HMACOnly_WhenCertLegDisabled(t *testing.T) {
	secret := []byte("s")
	hv, _ := NewVerifier(secret, 0)
	a, err := NewAuthorizer(hv, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.ClientCertEnabled() {
		t.Fatal("ClientCertEnabled true with a nil cert verifier")
	}
	path := "/proxy/exec/ns/pod/c"
	r := req([]*x509.Certificate{leaf("guacd.guacamole.svc")}, true)
	r.URL.Path = path
	// A certificate is on the wire, but the mode is OFF, so it must not
	// authenticate anything — the request falls to the HMAC leg and is
	// denied for a missing header.
	if _, err := a.Authorize(r); err == nil {
		t.Fatal("a client certificate authenticated while the certificate mode was disabled")
	}
	r.Header = hmacHeaders(t, secret, path)
	mode, err := a.Authorize(r)
	if err != nil || mode != ModeHMAC {
		t.Fatalf("mode=%q err=%v, want hmac/nil", mode, err)
	}
}

func TestAuthorizer_CertWins_AndDoesNotFallBackOnDenial(t *testing.T) {
	secret := []byte("s")
	hv, _ := NewVerifier(secret, 0)
	cv, _ := NewCertVerifier([]string{"guacd.guacamole.svc"})
	a, err := NewAuthorizer(hv, cv)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/namespaces/ns/pods/pod/exec"

	ok := req([]*x509.Certificate{leaf("guacd.guacamole.svc")}, true)
	ok.URL.Path = path
	mode, err := a.Authorize(ok)
	if err != nil || mode != ModeClientCert {
		t.Fatalf("allowlisted cert: mode=%q err=%v, want mtls/nil", mode, err)
	}

	// A DENIED certificate accompanied by a perfectly valid HMAC must
	// still be denied. Falling through would let a caller mask a
	// rejected identity behind a second credential and would make the
	// 401 reason unreadable.
	bad := req([]*x509.Certificate{leaf("intruder")}, true)
	bad.URL.Path = path
	bad.Header = hmacHeaders(t, secret, path)
	mode, err = a.Authorize(bad)
	if !errors.Is(err, ErrClientCertSubjectDenied) || mode != ModeNone {
		t.Fatalf("denied cert + valid HMAC: mode=%q err=%v, want ModeNone/ErrClientCertSubjectDenied", mode, err)
	}

	// No certificate presented at all ⇒ the HMAC leg still decides.
	none := req(nil, false)
	none.URL.Path = path
	none.Header = hmacHeaders(t, secret, path)
	mode, err = a.Authorize(none)
	if err != nil || mode != ModeHMAC {
		t.Fatalf("no cert + valid HMAC: mode=%q err=%v, want hmac/nil", mode, err)
	}
}
