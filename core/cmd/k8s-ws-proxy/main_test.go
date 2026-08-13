// main_test.go — end-to-end assertions over the REAL route table
// (newMux), the REAL TLS configuration (buildTLSConfig) and the REAL
// exec handler, with real certificates and a real TLS handshake.
//
// WHY HERE AND NOT IN internal/ (#5991). The mTLS credential is only
// worth anything if three separate things line up: the handler
// understands the apiserver-shaped path guacd emits, the mux ROUTES
// that path to the handler, and the TLS listener actually requests and
// verifies client certificates. Testing any one of those in isolation
// is the "helper tested, call site unpinned" defect this repo keeps
// re-learning — a test of parseAPIServerExecPath passes happily while
// nothing routes /api/v1/... anywhere. So these tests assemble the
// binary's own wiring functions and drive them over TCP+TLS.
//
// How "authentication succeeded" is asserted without a kube-apiserver:
// the namespace allowlist gate runs AFTER authorization and BEFORE any
// apiserver dial. A request that authenticates and then hits a denied
// namespace returns 403; a request that fails authentication returns
// 401. So 403 is positive evidence that the credential was accepted,
// and the two verdicts are distinguishable without a cluster. One test
// additionally uses an ALLOWED namespace and asserts the WebSocket
// upgrade completes, which proves the path runs all the way to the
// bridge on the certificate credential alone.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"k8s.io/client-go/rest"

	"github.com/openova-io/openova/core/cmd/k8s-ws-proxy/internal/auth"
	"github.com/openova-io/openova/core/cmd/k8s-ws-proxy/internal/proxy"
	wsruntime "github.com/openova-io/openova/core/cmd/k8s-ws-proxy/internal/runtime"
)

const (
	testAllowedSubject = "guacd.guacamole.svc.cluster.local"
	testIntruderCN     = "intruder.guacamole.svc.cluster.local"
	testHMACSecret     = "shared-secret-for-tests"
	testAllowedNS      = "catalyst-system"
	testDeniedNS       = "kube-system"
)

// ── certificate helpers ───────────────────────────────────────────────

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newTestCA(t *testing.T, cn string) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &testCA{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// issue mints a leaf signed by ca. serverNames non-empty ⇒ a serving
// certificate (with SANs); otherwise a client-auth certificate.
func (ca *testCA) issue(t *testing.T, cn string, serverNames []string) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
	}
	if len(serverNames) > 0 {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		tmpl.DNSNames = serverNames
		tmpl.IPAddresses = []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	} else {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
}

func writeFile(t *testing.T, dir, name string, b []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// ── the rig ───────────────────────────────────────────────────────────

type rig struct {
	srv      *httptest.Server
	ca       *testCA
	rogueCA  *testCA
	allowed  tls.Certificate
	intruder tls.Certificate
	rogue    tls.Certificate
}

// newRig assembles the binary's own wiring: buildTLSConfig over
// on-disk PEM files, proxy.New with both credential legs, newMux as the
// route table.
func newRig(t *testing.T, allowedNamespaces []string) *rig {
	t.Helper()
	dir := t.TempDir()
	ca := newTestCA(t, "k8s-ws-proxy-test-ca")
	rogueCA := newTestCA(t, "rogue-ca")

	srvCert, srvKey := ca.issue(t, "k8s-ws-proxy.catalyst-system.svc", []string{"localhost", "k8s-ws-proxy.catalyst-system.svc"})
	cfg := wsruntime.Config{
		TLSCertFile:        writeFile(t, dir, "tls.crt", srvCert),
		TLSKeyFile:         writeFile(t, dir, "tls.key", srvKey),
		TLSClientCAFile:    writeFile(t, dir, "ca.crt", ca.pem),
		ClientCertSubjects: []string{testAllowedSubject},
	}
	tlsCfg, err := buildTLSConfig(cfg)
	if err != nil {
		t.Fatalf("buildTLSConfig: %v", err)
	}
	// Pin the mode itself, not merely the downstream behaviour: an
	// accidental switch to tls.NoClientCert would leave every assertion
	// below passing for the wrong reason (no cert ever verified, HMAC
	// carrying the tests).
	if tlsCfg.ClientAuth != tls.VerifyClientCertIfGiven {
		t.Fatalf("ClientAuth = %v, want VerifyClientCertIfGiven", tlsCfg.ClientAuth)
	}
	if tlsCfg.ClientCAs == nil {
		t.Fatal("ClientCAs is nil — no chain could ever be verified")
	}

	hmacVerifier, err := auth.NewVerifier([]byte(testHMACSecret), 0)
	if err != nil {
		t.Fatal(err)
	}
	certVerifier, err := auth.NewCertVerifier(cfg.ClientCertSubjects)
	if err != nil {
		t.Fatal(err)
	}
	h, err := proxy.New(proxy.HandlerOptions{
		Verifier:          hmacVerifier,
		CertVerifier:      certVerifier,
		RESTConfig:        &rest.Config{Host: "https://127.0.0.1:1"}, // unroutable on purpose
		AllowedNamespaces: allowedNamespaces,
	})
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewUnstartedServer(newMux(h))
	srv.TLS = tlsCfg
	srv.StartTLS()
	t.Cleanup(srv.Close)

	load := func(c, k []byte) tls.Certificate {
		pair, err := tls.X509KeyPair(c, k)
		if err != nil {
			t.Fatal(err)
		}
		return pair
	}
	aC, aK := ca.issue(t, testAllowedSubject, nil)
	iC, iK := ca.issue(t, testIntruderCN, nil)
	rC, rK := rogueCA.issue(t, testAllowedSubject, nil) // right NAME, wrong CA

	return &rig{
		srv: srv, ca: ca, rogueCA: rogueCA,
		allowed:  load(aC, aK),
		intruder: load(iC, iK),
		rogue:    load(rC, rK),
	}
}

// clientTLS builds the caller side. GetClientCertificate — rather than
// Certificates — is deliberate: Go's TLS client filters Certificates
// against the certificate_authorities the server advertises in its
// CertificateRequest, and silently sends NOTHING when nothing matches.
// That filtering made the untrusted-CA control pass for the wrong
// reason (the certificate was never transmitted, so the request was
// denied as "no credential" rather than as "bad chain"). Returning the
// certificate from the callback bypasses the filter, so the SERVER is
// the thing being tested.
func (r *rig) clientTLS(clientCert *tls.Certificate) *tls.Config {
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(r.ca.pem)
	tc := &tls.Config{RootCAs: pool, ServerName: "localhost"}
	if clientCert != nil {
		tc.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return clientCert, nil
		}
	}
	return tc
}

func (r *rig) client(clientCert *tls.Certificate) *http.Client {
	return &http.Client{
		Transport: &http.Transport{TLSClientConfig: r.clientTLS(clientCert)},
		Timeout:   10 * time.Second,
	}
}

// guacdPath is the URL guacd builds and cannot be told not to build:
// guacamole-server 1.5.5 src/protocols/kubernetes/url.c writes
// "/api/v1/namespaces/%s/pods/%s/exec" and appends the query itself.
func guacdPath(ns, pod string) string {
	return "/api/v1/namespaces/" + ns + "/pods/" + pod + "/exec"
}

// ── the assertions ────────────────────────────────────────────────────

// TestMTLS_AllowedClientCert_Authenticates is the load-bearing positive:
// a client certificate ALONE, with no X-Catalyst-HMAC header anywhere,
// gets past authorization. 403 (namespace denied) is the proof — the
// namespace gate is downstream of authorization, so reaching it means
// the credential was accepted.
func TestMTLS_AllowedClientCert_Authenticates(t *testing.T) {
	r := newRig(t, []string{testAllowedNS})
	resp, err := r.client(&r.allowed).Get(r.srv.URL + guacdPath(testDeniedNS, "some-pod") + "?command=%2Fbin%2Fsh&stdin=true&stdout=true&tty=true")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (authenticated, then namespace-denied). 401 means the certificate was NOT accepted", resp.StatusCode)
	}
}

// TestMTLS_IntruderCert_Denied is THE control: a certificate issued by
// the SAME CA the proxy trusts, presented over the same handshake, with
// a subject that is not allowlisted. It shares every suspect property
// with the accepted certificate except identity, so a proxy that
// "accepts everything with a valid chain" fails here while passing the
// positive test above.
func TestMTLS_IntruderCert_Denied(t *testing.T) {
	r := newRig(t, []string{testAllowedNS})
	resp, err := r.client(&r.intruder).Get(r.srv.URL + guacdPath(testDeniedNS, "some-pod"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 — a chain-valid certificate with a non-allowlisted subject must NOT authenticate", resp.StatusCode)
	}
}

// TestMTLS_RogueCA_FailsHandshake — a certificate carrying the exact
// allowlisted CommonName but signed by a CA the proxy does not trust,
// and FORCED onto the wire (see clientTLS). Rejected by the TLS stack
// before any handler runs, so the subject allowlist is never the only
// thing standing between a forged name and a session.
func TestMTLS_RogueCA_FailsHandshake(t *testing.T) {
	r := newRig(t, []string{testAllowedNS})
	resp, err := r.client(&r.rogue).Get(r.srv.URL + guacdPath(testDeniedNS, "some-pod"))
	if err == nil {
		defer resp.Body.Close()
		t.Fatalf("handshake succeeded (status %d) with a certificate from an untrusted CA carrying the allowlisted CN",
			resp.StatusCode)
	}
	if !strings.Contains(err.Error(), "certificate") && !strings.Contains(err.Error(), "tls:") {
		t.Fatalf("connection failed, but not for a TLS certificate reason: %v", err)
	}
}

// TestMTLS_NoCredentialAtAll_Denied — TLS, no client certificate, no
// HMAC headers. Enabling TLS must not make the endpoint open.
func TestMTLS_NoCredentialAtAll_Denied(t *testing.T) {
	r := newRig(t, []string{testAllowedNS})
	resp, err := r.client(nil).Get(r.srv.URL + guacdPath(testDeniedNS, "some-pod"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for a request carrying no credential at all", resp.StatusCode)
	}
}

// TestMTLS_HMACStillWorks_NoRegression — the original credential, over
// the new TLS listener, with no client certificate. The mTLS leg is
// additive; this is the regression guard that says so.
func TestMTLS_HMACStillWorks_NoRegression(t *testing.T) {
	r := newRig(t, []string{testAllowedNS})
	path := "/proxy/exec/" + testDeniedNS + "/web/web"
	now := time.Now().Unix()
	req, _ := http.NewRequest(http.MethodGet, r.srv.URL+path, nil)
	req.Header.Set(auth.HeaderTimestamp, strconv.FormatInt(now, 10))
	req.Header.Set(auth.HeaderHMAC, auth.ComputeHex([]byte(testHMACSecret), now, path))
	resp, err := r.client(nil).Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 — the HMAC credential must still authenticate", resp.StatusCode)
	}
}

// TestMux_RoutesAPIServerShapedPath is the CALL-SITE pin. parseAPIServer
// ExecPath being correct is worth nothing if newMux never sends
// /api/v1/... to the handler; this asserts the route table itself, and
// it is the assertion that fails if someone removes the mux.Handle line.
func TestMux_RoutesAPIServerShapedPath(t *testing.T) {
	r := newRig(t, []string{testAllowedNS})
	// No credential ⇒ 401 from the exec handler. A path that reached no
	// handler at all would 404 from the mux instead, so 401 proves the
	// route exists AND lands on the exec handler.
	resp, err := r.client(nil).Get(r.srv.URL + guacdPath(testDeniedNS, "some-pod"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		t.Fatal("mux returned 404 for the apiserver-shaped path — the route is not registered")
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 from the exec handler", resp.StatusCode)
	}

	// …and the health endpoints still answer, so the new route did not
	// shadow them.
	for _, p := range []string{"/healthz", "/readyz"} {
		hr, err := r.client(nil).Get(r.srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		hr.Body.Close()
		if hr.StatusCode != http.StatusOK {
			t.Fatalf("%s = %d, want 200", p, hr.StatusCode)
		}
	}
}

// TestMTLS_ReachesWebSocketUpgrade drives the whole path on the
// certificate credential alone: guacd's URL shape, guacd's subprotocol
// (v4.channel.k8s.io, guacamole-server 1.5.5 kubernetes.h
// GUAC_KUBERNETES_LWS_PROTOCOL), an allowed namespace, and no HMAC
// header. A 101 upgrade means authorization, namespace policy and
// subprotocol negotiation all succeeded — everything up to the
// apiserver dial, which is deliberately unroutable here.
func TestMTLS_ReachesWebSocketUpgrade(t *testing.T) {
	r := newRig(t, []string{testAllowedNS})
	dialer := websocket.Dialer{
		Subprotocols:     []string{"v4.channel.k8s.io"},
		TLSClientConfig:  r.clientTLS(&r.allowed),
		HandshakeTimeout: 10 * time.Second,
	}
	wsURL := "wss" + strings.TrimPrefix(r.srv.URL, "https") +
		guacdPath(testAllowedNS, "k8s-ws-proxy") + "?command=%2Fbin%2Fsh&stdin=true&stdout=true&tty=true"
	conn, resp, err := dialer.Dial(wsURL, nil)
	if err != nil {
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		t.Fatalf("ws dial over mTLS failed: %v (http status %d)", err, code)
	}
	defer conn.Close()
	if got := conn.Subprotocol(); got != "v4.channel.k8s.io" {
		t.Fatalf("subprotocol = %q, want v4.channel.k8s.io (what guacd requests)", got)
	}
}

// TestBuildTLSConfig_NoClientCA_LeavesClientAuthOff — without a CA
// bundle the proxy must not pretend to verify anything.
func TestBuildTLSConfig_NoClientCA_LeavesClientAuthOff(t *testing.T) {
	dir := t.TempDir()
	ca := newTestCA(t, "ca")
	c, k := ca.issue(t, "srv", []string{"localhost"})
	cfg := wsruntime.Config{
		TLSCertFile: writeFile(t, dir, "tls.crt", c),
		TLSKeyFile:  writeFile(t, dir, "tls.key", k),
	}
	tlsCfg, err := buildTLSConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if tlsCfg.ClientAuth != tls.NoClientCert || tlsCfg.ClientCAs != nil {
		t.Fatalf("ClientAuth=%v ClientCAs!=nil=%v — client-cert verification must stay off without a CA bundle",
			tlsCfg.ClientAuth, tlsCfg.ClientCAs != nil)
	}
}

// TestBuildTLSConfig_UnusableCA_Fails — an empty or non-PEM CA file must
// fail startup rather than produce an empty pool that verifies nothing.
func TestBuildTLSConfig_UnusableCA_Fails(t *testing.T) {
	dir := t.TempDir()
	ca := newTestCA(t, "ca")
	c, k := ca.issue(t, "srv", []string{"localhost"})
	cfg := wsruntime.Config{
		TLSCertFile:     writeFile(t, dir, "tls.crt", c),
		TLSKeyFile:      writeFile(t, dir, "tls.key", k),
		TLSClientCAFile: writeFile(t, dir, "ca.crt", []byte("not a certificate")),
	}
	if _, err := buildTLSConfig(cfg); err == nil {
		t.Fatal("buildTLSConfig accepted a CA file containing no certificate")
	}
}
