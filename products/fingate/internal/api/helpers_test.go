package api

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	ledgerv1 "github.com/openova-io/openova/products/fingate/api/ledger/v1"
	"github.com/openova-io/openova/products/fingate/internal/definitions"
	"github.com/openova-io/openova/products/fingate/internal/ledgerclient"
	"github.com/openova-io/openova/products/fingate/internal/ledgertest"
	"github.com/openova-io/openova/products/fingate/internal/profile"
	"github.com/openova-io/openova/products/fingate/internal/sandbox"
	"github.com/openova-io/openova/products/fingate/internal/store"
	"github.com/openova-io/openova/products/fingate/internal/token"
)

const (
	profileDir = "../../profiles/rw-4.0.0"
	issuer     = "https://sso.bank.example/realms/open-banking"
	host       = "api.bank.example"
	clientID   = "acme-pay.wallet"
	keyID      = "k1"
)

// clientCert is the client certificate of every test call; only its bytes are hashed.
var clientCert = []byte("the DER bytes of the test client certificate")

var (
	keyOnce sync.Once
	signKey *rsa.PrivateKey
)

func key(t *testing.T) *rsa.PrivateKey {
	keyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		signKey = k
	})
	return signKey
}

func jwks(t *testing.T) []byte {
	pub := key(t).PublicKey
	doc, _ := json.Marshal(map[string]any{"keys": []map[string]string{{"kty": "RSA", "use": "sig", "kid": keyID,
		"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())}}})
	return doc
}

// mint signs claims with PS256. A nil value removes a claim of the defaults.
func mint(t *testing.T, now time.Time, change map[string]any) string {
	claims := map[string]any{"iss": issuer, "aud": "https://" + host, "exp": now.Add(5 * time.Minute).Unix(),
		"iat": now.Unix(), "client_id": clientID, "scope": "openid accounts",
		"cnf": map[string]string{"x5t#S256": token.Thumbprint(clientCert)}}
	for k, v := range change {
		if v == nil {
			delete(claims, k)
		} else {
			claims[k] = v
		}
	}
	h, _ := json.Marshal(map[string]string{"alg": "PS256", "typ": "JWT", "kid": keyID})
	c, _ := json.Marshal(claims)
	in := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	d := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPSS(rand.Reader, key(t), crypto.SHA256, d[:], &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
	if err != nil {
		t.Fatal(err)
	}
	return in + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// defs are the test definitions: one approved partner with one application that pins clientCert.
func defs(products string) string {
	return `profile: rw-4.0.0
host: ` + host + `
ledger: {target: "dns:///ledger-adapter:8443"}
` + products + `
partners:
  - id: acme-pay
    legalName: Acme Pay Ltd
    state: approved
    applications:
      - id: wallet
        clientAuth: tls_client_auth
        redirectUris: ["https://wallet.acmepay.example/callback"]
        certificates: [{sha256: "` + token.Thumbprint(clientCert) + `", notAfter: "2027-11-12"}]
        products: [accounts]
  - id: other
    legalName: Other Ltd
    state: approved
    applications:
      - id: app
        clientAuth: tls_client_auth
        redirectUris: ["https://other.example/cb"]
        certificates: [{sha256: "` + token.Thumbprint(clientCert) + `", notAfter: "2027-11-12"}]
        products: [accounts]
`
}

type fixture struct {
	srv     *Server
	store   *store.Memory
	holder  *definitions.Holder
	profile *profile.Profile
	now     *time.Time
}

// newFixture serves the sandbox ledger of package sandbox, whose codes are the documents' codes.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureOn(t, sandbox.New())
}

// newFixtureOn serves the ledger led.
func newFixtureOn(t *testing.T, led ledgerv1.LedgerServer) *fixture {
	t.Helper()
	conn, stop, err := ledgertest.Serve(led)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(); stop() })
	p, err := profile.LoadDir(profileDir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	f := &fixture{profile: p, now: &now, holder: &definitions.Holder{}}
	clock := func() time.Time { return *f.now }
	f.store = store.NewMemory(clock)
	f.setDefs(t, defs("products: [accounts]"))
	doc := jwks(t)
	checker, err := token.New(token.Config{Profile: p, Issuer: issuer, Audience: "https://" + host,
		Keys:     func(context.Context) ([]byte, error) { return doc, nil },
		Partners: f.holder, Consents: Consents{Store: f.store}, Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	f.srv, err = New(Config{Profile: p, Documents: os.DirFS(profileDir), Definitions: f.holder, Checker: checker,
		Store: f.store, Ledger: ledgerclient.New(conn, 0), Now: clock, NewID: func() string { n++; return fmt.Sprintf("%032d", n) }})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) setDefs(t *testing.T, text string) {
	t.Helper()
	d, err := definitions.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	f.holder.Set(d)
}

// call sends one request over a connection that verified clientCert. jws "" sends no Authorization header.
func (f *fixture) call(t *testing.T, method, path, jws string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, ok := body.([]byte)
		if !ok {
			b, _ = json.Marshal(body)
		}
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, "https://"+host+path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if jws != "" {
		req.Header.Set("Authorization", "Bearer "+jws)
	}
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{Raw: clientCert}}}
	w := httptest.NewRecorder()
	f.srv.ServeHTTP(w, req)
	return w
}

// errorMessage returns the message of the first error entry of an answer.
func errorMessage(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Errors []struct{ ErrorCode, Message string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Errors) == 0 {
		t.Fatalf("status %d: the body is not the error schema: %q", w.Code, w.Body.String())
	}
	return body.Errors[0].Message
}

func sameStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status %d, want %d; body %q", w.Code, want, w.Body.String())
	}
	if w.Header().Get(InteractionHeader) == "" {
		t.Fatalf("status %d: no %s header", w.Code, InteractionHeader)
	}
}
