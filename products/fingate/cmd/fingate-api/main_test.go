package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	ledgerv1 "github.com/openova-io/openova/products/fingate/api/ledger/v1"
	"github.com/openova-io/openova/products/fingate/internal/definitions"
	"github.com/openova-io/openova/products/fingate/internal/ledgertest"
	"github.com/openova-io/openova/products/fingate/internal/store"
	"github.com/openova-io/openova/products/fingate/internal/token"
)

const (
	testNS   = "open-banking"
	host     = "api.bank.example"
	issuer   = "https://sso.bank.example/realms/open-banking"
	clientID = "acme-pay.wallet"
	consents = "/open-banking/v4.0/aisp/account-access-consents"
	accounts = "/open-banking/v4.0/aisp/accounts"
)

// authority is a certificate authority of the test.
type authority struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newAuthority(t *testing.T, name string) *authority {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true, IsCA: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return &authority{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// issue returns a certificate for 127.0.0.1 with the usage given, and its key, both PEM.
func (a *authority) issue(t *testing.T, serial int64, usage x509.ExtKeyUsage) (tls.Certificate, []byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "fingate-api test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, a.cert, &key.PublicKey, a.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return pair, certPEM, keyPEM
}

func write(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// lines is the program's error output, split into lines that a test can wait for.
type lines struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	all  strings.Builder
	next chan string
}

func newLines() *lines { return &lines{next: make(chan string, 256)} }

func (l *lines) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.all.Write(p)
	l.buf.Write(p)
	for {
		line, err := l.buf.ReadString('\n')
		if err != nil {
			l.buf.WriteString(line)
			return len(p), nil
		}
		l.next <- strings.TrimSuffix(line, "\n")
	}
}

func (l *lines) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.all.String()
}

// waitFor returns the first line that starts with prefix, and fails after ten seconds.
func (l *lines) waitFor(t *testing.T, prefix string) string {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case line := <-l.next:
			if strings.HasPrefix(line, prefix) {
				return line
			}
		case <-deadline:
			t.Fatalf("no line %q within ten seconds; output:\n%s", prefix, l.text())
			return ""
		}
	}
}

// memory is the in-memory store with the two methods the program needs of a database.
type memory struct{ *store.Memory }

func (memory) Ping(context.Context) error { return nil }
func (memory) Close()                     {}

func definitionsDoc(thumbprint, state, hostName, target string) string {
	return `profile: rw-4.0.0
host: ` + hostName + `
ledger: {target: "` + target + `"}
products: [accounts]
partners:
  - id: acme-pay
    legalName: Acme Pay Ltd
    state: ` + state + `
    applications:
      - id: wallet
        clientAuth: tls_client_auth
        redirectUris: ["https://wallet.acmepay.example/callback"]
        certificates: [{sha256: "` + thumbprint + `", notAfter: "2027-11-12"}]
        products: [accounts]
`
}

// signer mints PS256 tokens and serves their key set over TLS.
type signer struct {
	key *rsa.PrivateKey
	srv *httptest.Server
}

func newSigner(t *testing.T) *signer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := json.Marshal(map[string]any{"keys": []map[string]string{{"kty": "RSA", "use": "sig", "kid": "k1",
		"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(doc) }))
	t.Cleanup(srv.Close)
	return &signer{key: key, srv: srv}
}

// mint signs a token bound to the certificate with the thumbprint. A consentID that is not
// empty makes it the token of a customer who authorised that consent.
func (s *signer) mint(t *testing.T, thumbprint, consentID string) string {
	t.Helper()
	now := time.Now()
	h, _ := json.Marshal(map[string]string{"alg": "PS256", "typ": "JWT", "kid": "k1"})
	claims := map[string]any{"iss": issuer, "aud": "https://" + host, "exp": now.Add(5 * time.Minute).Unix(),
		"iat": now.Unix(), "client_id": clientID, "scope": "openid accounts", "cnf": map[string]string{"x5t#S256": thumbprint}}
	if consentID != "" {
		claims[token.ConsentClaim] = consentID
	}
	c, _ := json.Marshal(claims)
	in := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	d := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPSS(rand.Reader, s.key, crypto.SHA256, d[:], &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
	if err != nil {
		t.Fatal(err)
	}
	return in + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// newLedger serves the in-memory ledger of package ledgertest over gRPC and TLS on a local port,
// with a certificate that ca issued for 127.0.0.1, and returns its target in the form of the
// definitions' ledger.target.
func newLedger(t *testing.T, ca *authority) string {
	t.Helper()
	pair, _, _ := ca.issue(t, 5, x509.ExtKeyUsageServerAuth)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}})))
	ledgerv1.RegisterLedgerServer(srv, ledgertest.NewMemory(ledgertest.FullCapabilities()))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return "dns:///" + lis.Addr().String()
}

// started is one running program.
type started struct {
	api, health string
	client      *fake.Clientset
	store       *store.Memory
	out         *lines
	stop        func() int
}

// start writes the files of a valid start into dir and runs the program with them. ledgerCA is
// the authority the program trusts for the certificate of the ledger adapter.
func start(t *testing.T, dir string, ca, ledgerCA *authority, sig *signer, configMapData map[string]string) *started {
	t.Helper()
	_, serverCert, serverKey := ca.issue(t, 2, x509.ExtKeyUsageServerAuth)
	jwksCA := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: sig.srv.Certificate().Raw})
	args := []string{"--listen", "127.0.0.1:0", "--health-listen", "127.0.0.1:0",
		"--cert-file", write(t, dir, "tls.crt", serverCert), "--key-file", write(t, dir, "tls.key", serverKey),
		"--client-ca-file", write(t, dir, "client-ca.crt", ca.pem),
		"--database-url-file", write(t, dir, "database-url", []byte("postgres://unused.invalid/fingate\n")),
		"--namespace", testNS, "--profiles-dir", "../../profiles",
		"--issuer", issuer, "--jwks-url", sig.srv.URL + "/certs", "--issuer-ca-file", write(t, dir, "issuer-ca.crt", jwksCA),
		"--ledger-ca-file", write(t, dir, "ledger-ca.crt", ledgerCA.pem)}
	s := &started{client: fake.NewClientset(), store: store.NewMemory(nil), out: newLines()}
	if configMapData != nil {
		cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: definitions.ConfigMapName, Namespace: testNS}, Data: configMapData}
		if _, err := s.client.CoreV1().ConfigMaps(testNS).Create(context.Background(), cm, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	addrs := make(chan [2]string, 1)
	ctx, cancel := context.WithCancel(context.Background())
	code := make(chan int, 1)
	go func() {
		code <- run(ctx, args, s.out, deps{
			kube:      func() (kubernetes.Interface, error) { return s.client, nil },
			openStore: func(context.Context, string) (database, error) { return memory{s.store}, nil },
			listening: func(a, h net.Addr) { addrs <- [2]string{a.String(), h.String()} },
		})
	}()
	select {
	case a := <-addrs:
		s.api, s.health = a[0], a[1]
	case c := <-code:
		cancel()
		t.Fatalf("run returned %d before it listened:\n%s", c, s.out.text())
	}
	var once sync.Once
	result := 0
	s.stop = func() int {
		once.Do(func() { cancel(); result = <-code })
		return result
	}
	t.Cleanup(func() { s.stop() })
	return s
}

func (s *started) updateDefinitions(t *testing.T, doc string) {
	t.Helper()
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: definitions.ConfigMapName, Namespace: testNS},
		Data: map[string]string{definitions.DataKey: doc}}
	if _, err := s.client.CoreV1().ConfigMaps(testNS).Update(context.Background(), cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func get(t *testing.T, address string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, address, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// tlsClient returns a client that trusts ca and presents cert; a zero cert presents none.
func tlsClient(ca *authority, cert tls.Certificate) *http.Client {
	roots := x509.NewCertPool()
	roots.AddCert(ca.cert)
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	if cert.Certificate != nil {
		cfg.Certificates = []tls.Certificate{cert}
	}
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: cfg}}
}

func call(t *testing.T, c *http.Client, method, address, jws, body string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, address, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if jws != "" {
		req.Header.Set("Authorization", "Bearer "+jws)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, address, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

const consentBody = `{"Data":{"Permissions":["ReadAccountsBasic"],"ExpirationDateTime":"2099-01-01T00:00:00+00:00"},"Risk":{}}`

// authorised creates a consent over the connection, has the customer cust-alice of the
// in-memory ledger authorise it for the account acc-1001, and returns the customer's token.
func (s *started) authorised(t *testing.T, partner *http.Client, sig *signer, thumb string) string {
	t.Helper()
	resp, body := call(t, partner, http.MethodPost, "https://"+s.api+consents, sig.mint(t, thumb, ""), consentBody)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	var created struct {
		Data struct {
			ConsentID string `json:"ConsentId"`
		}
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil || created.Data.ConsentID == "" {
		t.Fatalf("create: %s", body)
	}
	if _, err := s.store.Authorise(context.Background(), created.Data.ConsentID, "cust-alice", []string{"acc-1001"}); err != nil {
		t.Fatal(err)
	}
	return sig.mint(t, thumb, created.Data.ConsentID)
}

func TestRunServesTheRoutesWithMutualTLS(t *testing.T) {
	ca, other, sig := newAuthority(t, "partner authority"), newAuthority(t, "unknown authority"), newSigner(t)
	ledgerCA := newAuthority(t, "ledger authority")
	target := newLedger(t, ledgerCA)
	clientPair, _, _ := ca.issue(t, 3, x509.ExtKeyUsageClientAuth)
	strangerPair, _, _ := other.issue(t, 4, x509.ExtKeyUsageClientAuth)
	thumb := token.Thumbprint(clientPair.Certificate[0])
	s := start(t, t.TempDir(), ca, ledgerCA, sig, map[string]string{definitions.DataKey: definitionsDoc(thumb, "approved", host, target)})
	s.out.waitFor(t, "fingate-api: serving the resource server on "+s.api+" with mutual TLS, profile rw-4.0.0, host "+host+", ledger "+target+";")
	if c := get(t, "http://"+s.health+"/healthz"); c != http.StatusOK {
		t.Fatalf("/healthz: %d", c)
	}
	if c := get(t, "http://"+s.health+"/readyz"); c != http.StatusOK {
		t.Fatalf("/readyz: %d", c)
	}
	base := "https://" + s.api
	partner := tlsClient(ca, clientPair)

	// The handshake refuses a caller without a client certificate and one from an unknown authority.
	for name, c := range map[string]*http.Client{"no certificate": tlsClient(ca, tls.Certificate{}), "unknown authority": tlsClient(ca, strangerPair)} {
		req, _ := http.NewRequest(http.MethodGet, base+consents+"/x", nil)
		if resp, err := c.Do(req); err == nil {
			_ = resp.Body.Close()
			t.Fatalf("%s: the call reached the server with status %d", name, resp.StatusCode)
		}
	}

	// Every route checks the token: no token is 401 with the challenge.
	resp, body := call(t, partner, http.MethodGet, base+accounts, "", "")
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") != token.Challenge {
		t.Fatalf("no token: %d %q %s", resp.StatusCode, resp.Header.Get("WWW-Authenticate"), body)
	}

	// A consent is created over the real connection; the certificate bound in the token is the one presented.
	resp, body = call(t, partner, http.MethodPost, base+consents, sig.mint(t, thumb, ""), consentBody)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	var created struct {
		Data struct {
			ConsentID string `json:"ConsentId"`
		}
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil || created.Data.ConsentID == "" {
		t.Fatalf("create: %s", body)
	}
	if c, ok, err := s.store.Get(context.Background(), created.Data.ConsentID); err != nil || !ok || c.ClientID != clientID {
		t.Fatalf("stored: %+v %v %v", c, ok, err)
	}
	resp, body = call(t, partner, http.MethodGet, base+consents+"/"+created.Data.ConsentID, sig.mint(t, thumb, ""), "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"Status":"AWAU"`) {
		t.Fatalf("read: %d %s", resp.StatusCode, body)
	}

	// An account operation reads the ledger adapter of the definitions, over TLS: the customer of
	// an authorised consent sees the account the consent names, as the ledger holds it.
	resp, body = call(t, partner, http.MethodGet, base+accounts, s.authorised(t, partner, sig, thumb), "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"AccountId":"acc-1001"`) || strings.Contains(body, "acc-1002") {
		t.Fatalf("accounts from the ledger: %d %s", resp.StatusCode, body)
	}

	// A suspended partner is refused on the next call.
	s.updateDefinitions(t, definitionsDoc(thumb, "suspended", host, target))
	s.out.waitFor(t, "fingate-api: definitions: loaded")
	if resp, body := call(t, partner, http.MethodPost, base+consents, sig.mint(t, thumb, ""), consentBody); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("suspended partner: %d %s", resp.StatusCode, body)
	}

	// A change of the host, and a change of the ledger, is refused until a restart.
	s.updateDefinitions(t, definitionsDoc(thumb, "approved", "other.bank.example", target))
	if l := s.out.waitFor(t, "fingate-api: definitions: refused"); !strings.Contains(l, "a restart applies profile rw-4.0.0, host other.bank.example, ledger "+target) {
		t.Fatalf("host change: %q", l)
	}
	s.updateDefinitions(t, definitionsDoc(thumb, "approved", host, "dns:///other-adapter:8443"))
	if l := s.out.waitFor(t, "fingate-api: definitions: refused"); !strings.Contains(l, "a restart applies profile rw-4.0.0, host "+host+", ledger dns:///other-adapter:8443") {
		t.Fatalf("ledger change: %q", l)
	}

	if code := s.stop(); code != 0 || !strings.Contains(s.out.text(), "fingate-api: stopped\n") {
		t.Fatalf("stop: exit %d\n%s", code, s.out.text())
	}
}

// The program trusts only the authorities of --ledger-ca-file for the ledger adapter: with
// another authority there, the adapter's certificate is refused and a read of the ledger is a
// server error, while the consent operations, which do not read the ledger, still answer.
func TestRunVerifiesTheLedgersCertificate(t *testing.T) {
	ca, sig := newAuthority(t, "partner authority"), newSigner(t)
	target := newLedger(t, newAuthority(t, "ledger authority"))
	clientPair, _, _ := ca.issue(t, 3, x509.ExtKeyUsageClientAuth)
	thumb := token.Thumbprint(clientPair.Certificate[0])
	s := start(t, t.TempDir(), ca, newAuthority(t, "another authority"), sig,
		map[string]string{definitions.DataKey: definitionsDoc(thumb, "approved", host, target)})
	s.out.waitFor(t, "fingate-api: serving the resource server on "+s.api)
	partner := tlsClient(ca, clientPair)
	resp, body := call(t, partner, http.MethodGet, "https://"+s.api+accounts, s.authorised(t, partner, sig, thumb), "")
	if resp.StatusCode != http.StatusInternalServerError || strings.Contains(body, "acc-1001") {
		t.Fatalf("accounts from a ledger with an untrusted certificate: %d %s", resp.StatusCode, body)
	}
}

func TestRunWaitsForTheDefinitions(t *testing.T) {
	ca, sig := newAuthority(t, "partner authority"), newSigner(t)
	s := start(t, t.TempDir(), ca, ca, sig, map[string]string{definitions.DataKey: "profile: rw-4.0.0\n"})
	s.out.waitFor(t, "fingate-api: definitions: refused")
	if c := get(t, "http://"+s.health+"/readyz"); c != http.StatusServiceUnavailable {
		t.Fatalf("/readyz before the definitions: %d", c)
	}
	if c := get(t, "http://"+s.health+"/healthz"); c != http.StatusOK {
		t.Fatalf("/healthz: %d", c)
	}
	if code := s.stop(); code != 0 || !strings.Contains(s.out.text(), "stopped before the definitions were loaded") {
		t.Fatalf("stop: exit %d\n%s", code, s.out.text())
	}
}

func TestRunRefusesABadStart(t *testing.T) {
	dir := t.TempDir()
	ca := newAuthority(t, "partner authority")
	_, cert, key := ca.issue(t, 2, x509.ExtKeyUsageServerAuth)
	good := map[string]string{"--cert-file": write(t, dir, "tls.crt", cert), "--key-file": write(t, dir, "tls.key", key),
		"--client-ca-file": write(t, dir, "ca.crt", ca.pem), "--database-url-file": write(t, dir, "db", []byte("postgres://x.invalid/db")),
		"--namespace": testNS, "--profiles-dir": "../../profiles", "--issuer": issuer, "--jwks-url": "https://sso.bank.example/certs",
		"--listen": "127.0.0.1:0", "--health-listen": "127.0.0.1:0"}
	empty, noPEM := write(t, dir, "empty", []byte("\n")), write(t, dir, "nopem", []byte("not a certificate\n"))
	cases := []struct {
		name   string
		change map[string]string
		extra  []string
		want   string
	}{
		{"missing flags", map[string]string{"--namespace": "", "--jwks-url": ""}, nil, "required: --namespace, --jwks-url"},
		{"plain issuer", map[string]string{"--issuer": "http://sso.bank.example/realms/x"}, nil, `--issuer "http://sso.bank.example/realms/x" is not an https address`},
		{"plain key set", map[string]string{"--jwks-url": "http://sso.bank.example/certs"}, nil, "is not an https address"},
		{"no server key", map[string]string{"--key-file": filepath.Join(dir, "absent")}, nil, "no such file"},
		{"authority without a certificate", map[string]string{"--client-ca-file": noPEM}, nil, "holds no PEM certificate"},
		{"issuer authority without a certificate", map[string]string{"--issuer-ca-file": noPEM}, nil, "holds no PEM certificate"},
		{"ledger authority without a certificate", map[string]string{"--ledger-ca-file": noPEM}, nil, "holds no PEM certificate"},
		{"empty database address", map[string]string{"--database-url-file": empty}, nil, "is empty"},
		{"empty ConfigMap name", map[string]string{"--definitions-configmap": ""}, nil, "--definitions-configmap is empty"},
		{"argument", nil, []string{"extra"}, `unexpected argument "extra"`},
	}
	for _, c := range cases {
		var args []string
		for k, v := range good {
			if nv, ok := c.change[k]; ok {
				v = nv
			}
			args = append(args, k, v)
		}
		for k, v := range c.change {
			if _, ok := good[k]; !ok {
				args = append(args, k, v)
			}
		}
		args = append(args, c.extra...)
		out := newLines()
		failStore := func(context.Context, string) (database, error) {
			t.Errorf("%s: the program opened the database", c.name)
			return memory{store.NewMemory(nil)}, nil
		}
		if code := run(context.Background(), args, out, deps{openStore: failStore}); code != 2 || !strings.Contains(out.text(), c.want) {
			t.Errorf("%s: exit %d, want 2 and %q in:\n%s", c.name, code, c.want, out.text())
		}
	}
}
