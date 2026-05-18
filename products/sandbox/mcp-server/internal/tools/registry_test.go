package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	sharedauth "github.com/openova-io/openova/core/services/shared/auth"
)

// TestNewRegistry_NilEnvOK — historical contract: callers + tests that
// just want to enumerate the catalogue pass nil.
func TestNewRegistry_NilEnvOK(t *testing.T) {
	r := NewRegistry(nil)
	if r == nil {
		t.Fatal("NewRegistry(nil) returned nil")
	}
	if got := r.Env(); got == nil {
		t.Error("Env() should never be nil even when input was nil")
	}
}

// TestRegistry_ListContainsCatalogue confirms every namespace from
// architecture.md §3 is present (sorted), regardless of whether the
// handler is real or still a stub.
func TestRegistry_ListContainsCatalogue(t *testing.T) {
	r := NewRegistry(&Env{})
	got := r.List()

	want := []string{
		"gitea.issue.comment",
		"gitea.issue.create",
		"gitea.issue.get",
		"gitea.issue.list",
		"gitea.pr.create",
		"gitea.pr.get",
		"gitea.pr.list",
		"gitea.pr.merge",
		"gitea.release.list",
		"gitea.repo.get",
		"gitea.repo.list",
		"k8s.read.get",
		"k8s.read.list",
		"k8s.read.logs",
		"k8s.read.watch",
		"sandbox.auth.listClients",
		"sandbox.auth.provisionRealm",
		"sandbox.auth.registerClient",
		"sandbox.db.drop",
		"sandbox.db.dump",
		"sandbox.db.get",
		"sandbox.db.list",
		"sandbox.db.provision",
		"sandbox.session.info",
		"sandbox.session.whoami",
	}
	have := map[string]bool{}
	for _, t := range got {
		have[t.Name] = true
	}
	for _, w := range want {
		if !have[w] {
			t.Errorf("expected tool %q in catalogue", w)
		}
	}
	// Sorted order.
	for i := 1; i < len(got); i++ {
		if got[i-1].Name > got[i].Name {
			t.Errorf("catalogue not sorted: %q > %q", got[i-1].Name, got[i].Name)
		}
	}
}

// TestRegistry_StubsReturnNotImplemented covers every tool we explicitly
// kept stubbed.
//
// Wave 11 dropped sandbox.db.* (sandbox_db.go wired real handlers) AND
// gitea.pr.create/merge + gitea.issue.* + k8s.read.logs (this PR wired
// real handlers) off this list. sandbox.auth.* and gitea.release.list
// stay stubbed until later waves.
func TestRegistry_StubsReturnNotImplemented(t *testing.T) {
	r := NewRegistry(&Env{})
	stubs := []string{
		"sandbox.auth.provisionRealm",
		"sandbox.auth.listClients",
		"sandbox.auth.registerClient",
		"gitea.release.list",
	}
	for _, name := range stubs {
		res, err := r.Call(context.Background(), name, nil, CallOpts{})
		if err != nil {
			t.Errorf("%s: unexpected err: %v", name, err)
			continue
		}
		m, ok := res.(map[string]any)
		if !ok {
			t.Errorf("%s: result type %T, want map", name, res)
			continue
		}
		if m["status"] != "not_implemented" {
			t.Errorf("%s: status=%v want not_implemented", name, m["status"])
		}
	}
}

// TestRegistry_AuthGate_NoSecretBypass — when JWTSecret + OrgID are
// both empty, the auth gate is off (test mode) and the call goes
// through with nil claims.
func TestRegistry_AuthGate_NoSecretBypass(t *testing.T) {
	r := NewRegistry(&Env{}) // empty env = test mode
	// sandbox.session.info has Handler!=nil
	res, err := r.Call(context.Background(), "sandbox.session.info", nil, CallOpts{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if _, ok := res.(map[string]any); !ok {
		t.Errorf("result type %T, want map", res)
	}
}

// TestRegistry_AuthGate_NoClaimsRejects — when JWTSecret is set
// (production mode), calling without Claims is rejected.
func TestRegistry_AuthGate_NoClaimsRejects(t *testing.T) {
	r := NewRegistry(&Env{OrgID: "acme", JWTSecret: []byte("x")})
	_, err := r.Call(context.Background(), "sandbox.session.info", nil, CallOpts{Claims: nil})
	if err == nil || !strings.Contains(err.Error(), "unauthenticated") {
		t.Errorf("err = %v, want unauthenticated", err)
	}
}

// TestRegistry_AuthGate_OrgMismatch — claim org_id != env.OrgID = 403,
// EXCEPT for sandbox.session.* which is exempt.
func TestRegistry_AuthGate_OrgMismatch(t *testing.T) {
	r := NewRegistry(&Env{
		OrgID:        "acme",
		JWTSecret:    []byte("x"),
		GiteaBaseURL: "http://gitea",
		GiteaToken:   "tok",
	})
	cl := &sharedauth.Claims{
		Email: "u@x",
		OrgID: "bankdhofar",
	}
	_, err := r.Call(context.Background(), "gitea.repo.list", nil, CallOpts{Claims: cl})
	if err == nil || !strings.Contains(err.Error(), "org_id mismatch") {
		t.Errorf("gitea.repo.list with wrong org: err = %v, want org_id mismatch", err)
	}
	// session.whoami is exempt — should pass through (no Gitea hop).
	if _, err := r.Call(context.Background(), "sandbox.session.whoami", nil, CallOpts{Claims: cl}); err != nil {
		t.Errorf("session.whoami should be org-scope exempt; got %v", err)
	}
}

// TestRegistry_AuthGate_RequiredCapability — when a tool sets
// RequiredCapability, claims must carry it.
func TestRegistry_AuthGate_RequiredCapability(t *testing.T) {
	r := NewRegistry(&Env{OrgID: "acme", JWTSecret: []byte("x")})
	r.Register(Tool{
		Name:               "test.cap",
		InputSchema:        map[string]any{},
		Handler:            func(context.Context, json.RawMessage) (any, error) { return "ok", nil },
		RequiredCapability: "sandbox:db:provision",
	})
	cl := &sharedauth.Claims{OrgID: "acme", Capabilities: []string{"sandbox:db:list"}}
	if _, err := r.Call(context.Background(), "test.cap", nil, CallOpts{Claims: cl}); err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Errorf("wrong cap: err = %v, want forbidden", err)
	}
	cl.Capabilities = append(cl.Capabilities, "sandbox:db:provision")
	if _, err := r.Call(context.Background(), "test.cap", nil, CallOpts{Claims: cl}); err != nil {
		t.Errorf("with cap: err = %v, want nil", err)
	}
}

// TestSessionWhoami_AuthenticatedShape — the whoami handler echoes
// every Claim field the bearer carried.
func TestSessionWhoami_AuthenticatedShape(t *testing.T) {
	r := NewRegistry(&Env{OrgID: "acme", JWTSecret: []byte("x")})
	now := time.Now().UTC().Truncate(time.Second)
	cl := &sharedauth.Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user-uuid",
			ID:        "jti-1",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		},
		Email:        "ops@acme",
		Role:         "member",
		OrgID:        "acme",
		Groups:       []string{"/acme/admins"},
		Capabilities: []string{"sandbox:db:list"},
		Typ:          "pat",
	}
	res, err := r.Call(context.Background(), "sandbox.session.whoami", nil, CallOpts{Claims: cl})
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	m, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("type %T, want map", res)
	}
	if m["authenticated"] != true {
		t.Errorf("authenticated=%v want true", m["authenticated"])
	}
	if m["sub"] != "user-uuid" || m["org_id"] != "acme" || m["typ"] != "pat" {
		t.Errorf("claim echo wrong: %+v", m)
	}
	if m["jti"] != "jti-1" {
		t.Errorf("jti=%v want jti-1", m["jti"])
	}
}

// TestSessionInfo_ReturnsEnv — session.info surfaces the env-bound
// scope so the agent can correlate its repos + Sovereign FQDN.
func TestSessionInfo_ReturnsEnv(t *testing.T) {
	r := NewRegistry(&Env{
		OrgID:            "acme",
		SandboxID:        "emrah",
		SandboxNamespace: "sandbox-uid-123",
		SovereignFQDN:    "acme.openova.io",
		SandboxRepos:     []string{"acme/eventforge", "acme/internal-tools"},
		GiteaBaseURL:     "http://gitea.gitea.svc:3000",
	})
	res, err := r.Call(context.Background(), "sandbox.session.info", nil, CallOpts{})
	if err != nil {
		t.Fatalf("info: %v", err)
	}
	m := res.(map[string]any)
	if m["sandbox_id"] != "emrah" || m["org_id"] != "acme" {
		t.Errorf("info wrong: %+v", m)
	}
	if m["kubeconfig"] != "in-cluster" {
		t.Errorf("kubeconfig=%v want in-cluster", m["kubeconfig"])
	}
	repos, ok := m["repos"].([]string)
	if !ok || len(repos) != 2 {
		t.Errorf("repos=%v (%T)", m["repos"], m["repos"])
	}
}

// TestExtractBearer covers the three paths: _auth.token, env fallback,
// neither.
func TestExtractBearer(t *testing.T) {
	t.Parallel()
	envWithTok := &Env{SandboxToken: "env-tok"}

	// (1) _auth.token wins.
	args1 := json.RawMessage(`{"foo":"bar","_auth":{"token":"call-tok"}}`)
	if got := ExtractBearer(envWithTok, args1); got != "call-tok" {
		t.Errorf("got %q want call-tok", got)
	}
	// (2) env fallback.
	args2 := json.RawMessage(`{"foo":"bar"}`)
	if got := ExtractBearer(envWithTok, args2); got != "env-tok" {
		t.Errorf("got %q want env-tok", got)
	}
	// (3) neither.
	if got := ExtractBearer(&Env{}, args2); got != "" {
		t.Errorf("got %q want empty", got)
	}
	// (4) nil env, raw with token.
	if got := ExtractBearer(nil, args1); got != "call-tok" {
		t.Errorf("got %q want call-tok (nil env)", got)
	}
}

// TestValidateBearer_HappyPath — round-trip a PAT through HS256 and
// confirm the parsed Claims carry the org_id.
func TestValidateBearer_HappyPath(t *testing.T) {
	t.Parallel()
	secret := []byte("super-secret")
	env := &Env{JWTSecret: secret, OrgID: "acme"}
	now := time.Now()
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, &sharedauth.Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user-1",
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(now),
		},
		Email: "e@e",
		OrgID: "acme",
		Typ:   "pat",
	}).SignedString(secret)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	cl, err := ValidateBearer(env, signed)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if cl == nil {
		t.Fatal("claims nil")
	}
	if cl.OrgID != "acme" || cl.Email != "e@e" || cl.Typ != "pat" {
		t.Errorf("claims wrong: %+v", cl)
	}
}

// TestValidateBearer_TestModeBypass — empty JWTSecret means no
// validation; any token (incl. empty) is accepted with nil claims.
func TestValidateBearer_TestModeBypass(t *testing.T) {
	t.Parallel()
	cl, err := ValidateBearer(&Env{}, "")
	if err != nil || cl != nil {
		t.Errorf("test mode: got (%v, %v), want (nil, nil)", cl, err)
	}
	cl, err = ValidateBearer(&Env{}, "garbage")
	if err != nil || cl != nil {
		t.Errorf("test mode garbage: got (%v, %v), want (nil, nil)", cl, err)
	}
}

// TestValidateBearer_WrongSig — flipped secret rejects.
func TestValidateBearer_WrongSig(t *testing.T) {
	t.Parallel()
	signed, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "x",
		"exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString([]byte("good"))
	_, err := ValidateBearer(&Env{JWTSecret: []byte("bad")}, signed)
	if err == nil {
		t.Error("want validation error on wrong sig")
	}
}

// TestValidateBearer_Expired — expired token rejects.
func TestValidateBearer_Expired(t *testing.T) {
	t.Parallel()
	secret := []byte("s")
	signed, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "x",
		"exp": time.Now().Add(-time.Hour).Unix(),
	}).SignedString(secret)
	_, err := ValidateBearer(&Env{JWTSecret: secret}, signed)
	if err == nil {
		t.Error("want error on expired token")
	}
}

// TestStripAuthEnvelope — _auth removed, other fields preserved.
func TestStripAuthEnvelope(t *testing.T) {
	t.Parallel()
	in := json.RawMessage(`{"foo":"bar","_auth":{"token":"x"},"n":42}`)
	out := StripAuthEnvelope(in)
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, has := m["_auth"]; has {
		t.Error("_auth should be stripped")
	}
	if m["foo"] != "bar" || m["n"].(float64) != 42 {
		t.Errorf("other fields lost: %+v", m)
	}
}

// TestNewEnvFromOS — env vars round-trip into Env, repos CSV split.
func TestNewEnvFromOS(t *testing.T) {
	t.Setenv("SANDBOX_ORG_ID", "acme")
	t.Setenv("SANDBOX_ID", "emrah")
	t.Setenv("SANDBOX_NAMESPACE", "sandbox-uid-123")
	t.Setenv("SANDBOX_SOVEREIGN_FQDN", "acme.openova.io")
	t.Setenv("SANDBOX_REPOS", "acme/a, acme/b , acme/c")
	t.Setenv("SANDBOX_TOKEN", "pat-1")
	t.Setenv("SANDBOX_JWT_SECRET", "shh")
	t.Setenv("SANDBOX_GITEA_BASE_URL", "http://gitea")
	t.Setenv("SANDBOX_GITEA_TOKEN", "gtk")

	env := NewEnvFromOS()
	if env.OrgID != "acme" || env.SandboxID != "emrah" || env.SandboxNamespace != "sandbox-uid-123" {
		t.Errorf("scalar fields wrong: %+v", env)
	}
	if string(env.JWTSecret) != "shh" {
		t.Errorf("jwt secret=%q", env.JWTSecret)
	}
	if len(env.SandboxRepos) != 3 || env.SandboxRepos[0] != "acme/a" || env.SandboxRepos[2] != "acme/c" {
		t.Errorf("repos parsed wrong: %v", env.SandboxRepos)
	}
}
