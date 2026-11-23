package suite

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/api"
	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/session"
)

const (
	signInIssuer = "https://console.sov.test"
	signInOwner  = "owner@sov.test"
	signerPath   = "/var/lib/catalyst/keys/handover.pem"
)

// expiringConsole is catalyst-api's session check: a call passes when its
// catalyst_session is an RS256 JWT signed by key, of issuer signInIssuer and
// not past its exp; every other call answers 401 unauthenticated.
type expiringConsole struct {
	key   *rsa.PublicKey
	mu    sync.Mutex
	calls int
}

func (c *expiringConsole) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	ck, err := r.Cookie("catalyst_session")
	if err != nil || c.valid(ck.Value) != nil {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"unauthenticated"}`)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	_, _ = io.WriteString(w, `{"org_tenant_id":"t-1","tenant_namespace":"iso-a-1","state":"pending"}`)
}

func (c *expiringConsole) valid(tok string) error {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return errors.New("not a JWT")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(c.key, crypto.SHA256, sum[:], sig); err != nil {
		return err
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return err
	}
	var claims struct {
		Iss string `json:"iss"`
		Exp int64  `json:"exp"`
	}
	if err := json.Unmarshal(body, &claims); err != nil {
		return err
	}
	if claims.Iss != signInIssuer || time.Now().Unix() >= claims.Exp {
		return errors.New("expired or of another issuer")
	}
	return nil
}

// signerCluster answers as kubectl does in two regions: region a cannot be
// reached, region b runs catalyst-api with its signer at signerPath. It keeps
// every call.
type signerCluster struct {
	pem   []byte
	mu    sync.Mutex
	calls []string
}

func (s *signerCluster) kubectl(r kube.Region, _ context.Context, _ io.Reader, args ...string) ([]byte, error) {
	s.mu.Lock()
	s.calls = append(s.calls, r.ID+" "+strings.Join(args, " "))
	s.mu.Unlock()
	if r.ID == "region-a" {
		return nil, errors.New("[region-a] kubectl get deployments: exit status 1: Unable to connect to the server")
	}
	switch strings.Join(args, " ") {
	case "get deployments -n catalyst-system --field-selector metadata.name=catalyst-api -o json":
		return []byte(`{"items":[{"metadata":{"name":"catalyst-api"},"spec":{"template":{"spec":{"containers":[{"name":"api","env":[` +
			`{"name":"CATALYST_PIN_ISSUER","value":"https://elsewhere.test"},{"name":"CATALYST_HANDOVER_SIGNER_PATH","value":"` + signerPath + `"}]}]}}}}]}`), nil
	case "-n catalyst-system exec deploy/catalyst-api -- cat " + signerPath:
		return s.pem, nil
	}
	return nil, errors.New("[" + r.ID + "] unexpected kubectl " + strings.Join(args, " "))
}

func signInKey(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

// The session given to the run was minted 3 h 16 min ago with a lifetime of
// 3 h, as at NET-G07 of gate run 44952720479: the console refuses it, the
// client mints one like it with the Sovereign's own signer, from the region
// that gives it, and the call passes.
func TestTheSuiteSignsInAgainWhenItsSessionExpires(t *testing.T) {
	key, keyPEM := signInKey(t)
	previous, err := session.Mint(key, session.Claims{Issuer: signInIssuer, Email: signInOwner, TTL: 3 * time.Hour,
		Now: time.Now().Add(-(3*time.Hour + 16*time.Minute))})
	if err != nil {
		t.Fatal(err)
	}
	console := &expiringConsole{key: &key.PublicKey}
	srv := httptest.NewServer(console)
	t.Cleanup(srv.Close)
	cluster := &signerCluster{pem: keyPEM}
	regions := []kube.Region{{ID: "region-a"}, {ID: "region-b"}}
	c := api.New(srv.URL, previous)
	c.Renew = ownerRenewal(func() []kube.Region { return regions }, previous, cluster.kubectl)

	res, err := c.Do(context.Background(), http.MethodPost, "/api/v1/organizations", map[string]string{"subdomain": "iso-a-1"})
	if err != nil || res.Status != http.StatusAccepted || console.calls != 2 || c.Renew.Minted() != 1 {
		t.Fatalf("want 202 after one renewal and 2 calls, got %v %v after %d calls and %d renewals", res, err, console.calls, c.Renew.Minted())
	}
	renewed := c.SessionValue()
	issuer, email, err := sessionClaims(renewed)
	if err != nil || issuer != signInIssuer || email != signInOwner || console.valid(renewed) != nil {
		t.Errorf("the renewed session: issuer %q, e-mail %q, %v: want those of the session it replaces, valid", issuer, email, err)
	}
	want := "region-a get deployments -n catalyst-system --field-selector metadata.name=catalyst-api -o json," +
		"region-b get deployments -n catalyst-system --field-selector metadata.name=catalyst-api -o json," +
		"region-b -n catalyst-system exec deploy/catalyst-api -- cat " + signerPath
	if got := strings.Join(cluster.calls, ","); got != want {
		t.Errorf("kubectl calls:\n got %s\nwant %s", got, want)
	}
	// Later calls send the renewed session and mint nothing.
	if _, err := c.Get(context.Background(), "/api/v1/whoami", nil); err != nil || c.Renew.Minted() != 1 {
		t.Errorf("a later call: %v after %d renewals", err, c.Renew.Minted())
	}
}

// When no region gives the signer, or the session is not a JWT, the call
// fails saying why; no message holds the key or a session.
func TestTheSuiteSaysWhyItCannotSignInAgain(t *testing.T) {
	key, keyPEM := signInKey(t)
	previous, err := session.Mint(key, session.Claims{Issuer: signInIssuer, Email: signInOwner, TTL: time.Hour,
		Now: time.Now().Add(-2 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	console := &expiringConsole{key: &key.PublicKey}
	srv := httptest.NewServer(console)
	t.Cleanup(srv.Close)
	for name, c := range map[string]struct {
		previous string
		regions  []kube.Region
		pem      []byte
		want     string
	}{
		"no region gives the signer": {previous, []kube.Region{{ID: "region-a"}}, keyPEM,
			"POST /api/v1/organizations: HTTP 401, and the session could not be renewed: no region gave the session signer: [region-a] kubectl get deployments: exit status 1: Unable to connect to the server"},
		"the signer is not a key": {previous, []kube.Region{{ID: "region-b"}}, []byte("not a key"),
			"the session could not be renewed: no region gave the session signer: [region-b] the session signer of catalyst-api: session: no PEM block in key"},
		"the session is not a JWT": {"opaque-session-value", []kube.Region{{ID: "region-b"}}, keyPEM,
			"the session could not be renewed: the session of the run cannot be renewed: it is not a JWT"},
	} {
		cluster := &signerCluster{pem: c.pem}
		cl := api.New(srv.URL, c.previous)
		cl.Renew = ownerRenewal(func() []kube.Region { return c.regions }, c.previous, cluster.kubectl)
		_, err := cl.Do(context.Background(), http.MethodPost, "/api/v1/organizations", nil)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want an error with %q, got %v", name, c.want, err)
			continue
		}
		if strings.Contains(err.Error(), c.previous) || strings.Contains(err.Error(), "PRIVATE KEY") {
			t.Errorf("%s: the error holds the session or the key: %v", name, err)
		}
	}
}
