package suite

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/api"
	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// agentFake is a catalyst-api that issues and judges agent tokens as the
// contract says, on a clock of its own that the check's wait moves. Each
// fault field makes one answer wrong.
type agentFake struct {
	t     *testing.T
	key   *rsa.PrivateKey
	mu    sync.Mutex
	clock time.Time

	kubectlFails bool // kubectl cannot create the token
	issueRefuses bool // the issue route refuses the caller
	wrongKid     bool // the key set names another key
	otherKey     bool // the key set holds another public key
	otherListed  bool // the read lists a second Organization
	outsideOpen  bool // the deployment route answers 200
	writeOpen    bool // the write route answers 201
	adminMinted  bool // a token of tier admin is minted
	otherMinted  bool // a token of Organization other is minted
	noExpiry     bool // an expired token still reads

	tokenArgs []string // the kubectl calls
	waited    []time.Duration
	callers   []string // the bearer of every issue request
	minted    []string // every token the fake issued
}

const agentFakeKid = "fake-kid"

func (f *agentFake) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.clock
}

func (f *agentFake) wait(_ context.Context, d time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.waited = append(f.waited, d)
	f.clock = f.clock.Add(d)
	return nil
}

func (f *agentFake) kubectl(_ kube.Region, _ context.Context, _ io.Reader, args ...string) ([]byte, error) {
	f.tokenArgs = append(f.tokenArgs, strings.Join(args, " "))
	if f.kubectlFails {
		return nil, errors.New(`[region-a] kubectl -n catalyst-system create token catalyst-conformance --duration=10m: exit status 1: error: failed to create token: serviceaccounts "catalyst-conformance" not found`)
	}
	return []byte("sa.token.of-the-caller\n"), nil
}

func b64(v any) string {
	b, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (f *agentFake) sign(claims map[string]any) string {
	input := b64(map[string]any{"alg": "RS256", "typ": "JWT", "kid": agentFakeKid}) + "." + b64(claims)
	digest := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, digest[:])
	if err != nil {
		f.t.Fatal(err)
	}
	token := input + "." + base64.RawURLEncoding.EncodeToString(sig)
	f.minted = append(f.minted, token)
	return token
}

// agent returns the claims of a valid agent token of the request, or nil.
func (f *agentFake) agent(r *http.Request) map[string]any {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(&f.key.PublicKey, crypto.SHA256, digest[:], sig) != nil {
		return nil
	}
	_, claims, err := jwtParts(token)
	if err != nil {
		return nil
	}
	if exp, _ := claims["exp"].(float64); !f.noExpiry && int64(exp) <= f.now().Unix() {
		return nil
	}
	return claims
}

func answer(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func refuse(w http.ResponseWriter, status int, code string) {
	answer(w, status, map[string]string{"error": code, "detail": "fake"})
}

func (f *agentFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	route := r.Method + " " + r.URL.Path
	if route == "POST "+aipG07Issue {
		f.callers = append(f.callers, r.Header.Get("Authorization"))
		var req struct {
			Organization, Agent, Tier string
			TTLSeconds                int
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			refuse(w, http.StatusUnprocessableEntity, "invalid-request")
			return
		}
		switch {
		case r.Header.Get("Authorization") != "Bearer sa.token.of-the-caller":
			refuse(w, http.StatusUnauthorized, "missing-bearer")
			return
		case f.issueRefuses:
			refuse(w, http.StatusForbidden, "unauthorized-sa")
			return
		case req.Tier != "viewer" && req.Tier != "developer" && !f.adminMinted:
			refuse(w, http.StatusUnprocessableEntity, "invalid-request")
			return
		case req.Organization != "conformance" && !f.otherMinted:
			refuse(w, http.StatusForbidden, "caller-not-allowed")
			return
		}
		life := 1800
		if req.TTLSeconds != 0 {
			life = req.TTLSeconds
		}
		exp := f.now().Add(time.Duration(life) * time.Second)
		token := f.sign(map[string]any{"typ": "agent", "org": req.Organization, "org_id": req.Organization, "agent": req.Agent,
			"tier": req.Tier, "aud": []string{"model-gateway", "platform-tools", "catalyst-api"},
			"sub": "agent:" + req.Organization + "/" + req.Agent, "exp": exp.Unix()})
		answer(w, http.StatusOK, map[string]string{"token": token, "expiresAt": exp.UTC().Format(time.RFC3339)})
		return
	}
	if route == "GET "+aipG07KeySet {
		pub := f.key.PublicKey
		if f.otherKey {
			other, _ := rsa.GenerateKey(rand.Reader, 2048)
			pub = other.PublicKey
		}
		kid := agentFakeKid
		if f.wrongKid {
			kid = "another-kid"
		}
		answer(w, http.StatusOK, map[string]any{"keys": []map[string]string{{"kty": "RSA", "use": "sig", "alg": "RS256", "kid": kid,
			"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())}}})
		return
	}
	claims := f.agent(r)
	if claims == nil {
		refuse(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	switch {
	case route == "GET "+aipG07Read:
		orgs := []map[string]string{{"name": "conformance"}}
		if f.otherListed {
			orgs = append(orgs, map[string]string{"name": "acme"})
		}
		answer(w, http.StatusOK, map[string]any{"organizations": orgs})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/sovereigns/") && strings.HasSuffix(r.URL.Path, "/applications"):
		if f.outsideOpen {
			answer(w, http.StatusOK, map[string]any{"applications": []any{}})
			return
		}
		refuse(w, http.StatusForbidden, "org-scoped-forbidden")
	case route == "POST "+aipG07Write:
		if f.writeOpen {
			answer(w, http.StatusCreated, map[string]any{})
			return
		}
		refuse(w, http.StatusForbidden, "agent-token-forbidden")
	default:
		refuse(w, http.StatusNotFound, "not-found")
	}
}

func newAgentFake(t *testing.T) *agentFake {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return &agentFake{t: t, key: key, clock: time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)}
}

// runAgentCheck runs the check against the fake and returns the runner.
func runAgentCheck(t *testing.T, f *agentFake) *runner {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := agentTokenCheck{kubectl: f.kubectl, now: f.now, wait: f.wait}
	return runCheck(t, "agent-tokens", c, &CheckContext{API: api.New(srv.URL, ""), Deploy: &DeploymentInfo{ID: "29b7e14918178f7e"}})
}

// TestAgentTokenCheckPasses: a catalyst-api that keeps the contract passes
// all nine steps, the token is asked for once, the late read is made 90
// seconds after the mint, and no token reaches the record.
func TestAgentTokenCheckPasses(t *testing.T) {
	f := newAgentFake(t)
	r := runAgentCheck(t, f)
	if failed := failedSteps(r); len(failed) != 0 {
		t.Fatalf("a catalyst-api that keeps the contract failed: %q", failed)
	}
	ids := []string{}
	for _, s := range r.run.Steps {
		ids = append(ids, s.ID)
	}
	if got := strings.Join(ids, " "); got != "AIP-G07.1 AIP-G07.2 AIP-G07.3 AIP-G07.4 AIP-G07.5 AIP-G07.6 AIP-G07.7 AIP-G07.8 AIP-G07.9" {
		t.Fatalf("steps %q", got)
	}
	if got := strings.Join(f.tokenArgs, "|"); got != "-n catalyst-system create token catalyst-conformance --duration=10m" {
		t.Errorf("kubectl calls %q", got)
	}
	if len(f.waited) != 1 || f.waited[0] != 90*time.Second {
		t.Errorf("waited %v, want one wait of 90s", f.waited)
	}
	for _, c := range f.callers {
		if c != "Bearer sa.token.of-the-caller" {
			t.Errorf("an issue request carried %q", c)
		}
	}
	if len(f.minted) != 2 {
		t.Errorf("the fake minted %d tokens, want 2", len(f.minted))
	}
	record, err := json.Marshal(r.run.Steps)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range append([]string{"sa.token.of-the-caller"}, f.minted...) {
		if strings.Contains(string(record), secret) {
			t.Fatal("a token reached the record")
		}
	}
}

// TestAgentTokenCheckFails: every broken answer fails the step that asks
// for it, and says why.
func TestAgentTokenCheckFails(t *testing.T) {
	cases := []struct {
		name  string
		fault func(f *agentFake)
		want  string
	}{
		{"the ServiceAccount is missing", func(f *agentFake) { f.kubectlFails = true }, `serviceaccounts "catalyst-conformance" not found`},
		{"the issue route refuses the caller", func(f *agentFake) { f.issueRefuses = true }, "answered 403 unauthorized-sa, want 200"},
		{"the key set names another key", func(f *agentFake) { f.wrongKid = true }, "the key set names key another-kid, the token names fake-kid"},
		{"the key set holds another key", func(f *agentFake) { f.otherKey = true }, "does not verify with the key of the key set"},
		{"the read lists another Organization", func(f *agentFake) { f.otherListed = true }, `lists Organization "acme"`},
		{"a route outside the Organization answers", func(f *agentFake) { f.outsideOpen = true }, `GET /api/v1/sovereigns/29b7e14918178f7e/applications answered 200 "", want 403 "org-scoped-forbidden"`},
		{"a write answers", func(f *agentFake) { f.writeOpen = true }, `POST /api/v1/org/applications answered 201 "", want 403 "agent-token-forbidden"`},
		{"a token of tier admin is minted", func(f *agentFake) { f.adminMinted = true }, `for tier admin answered 200 "", want 422 "invalid-request"`},
		{"a token of another Organization is minted", func(f *agentFake) { f.otherMinted = true }, `for Organization other answered 200 "", want 403 "caller-not-allowed"`},
		{"an expired token still reads", func(f *agentFake) { f.noExpiry = true }, "with a token that expired answered 200, want 401"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAgentFake(t)
			tc.fault(f)
			failed := failedSteps(runAgentCheck(t, f))
			if len(failed) == 0 {
				t.Fatal("the check passed")
			}
			if all := strings.Join(failed, "\n"); !strings.Contains(all, tc.want) {
				t.Fatalf("no failed step says %q; failed: %q", tc.want, failed)
			}
		})
	}
}

// TestAgentTokenCheckNeedsTheConsole: a run without a console address
// cannot measure anything, and that is a failure, never a pass.
func TestAgentTokenCheckNeedsTheConsole(t *testing.T) {
	f := newAgentFake(t)
	r := runCheck(t, "agent-tokens", agentTokenCheck{kubectl: f.kubectl, now: f.now, wait: f.wait}, &CheckContext{})
	if failed := failedSteps(r); len(failed) != 1 || failed[0] != "the run has no console address" {
		t.Fatalf("failed: %q", failed)
	}
}

func TestAgentTokenCheckRegistered(t *testing.T) {
	for _, c := range RegisteredChecks() {
		if c.ID() == aipG07ID {
			return
		}
	}
	t.Fatal("AIP-G07 is not registered")
}
