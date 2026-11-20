package agenttoken

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"gopkg.in/yaml.v3"
	authnv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	fakek8s "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/audit"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handler"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handoverjwt"
)

// The caller of the tests and its entry.
const (
	testCaller  = "system:serviceaccount:ops:ops-records"
	testCallers = `[{"caller": "system:serviceaccount:ops:ops-records", "organizations": ["acme"], "maxTier": "developer", "agents": ["correlator"]}]`
	testFQDN    = "t42.omani.works"
)

// The conformance entry, the first entry of the default of the chart value
// agentTokens.callers.
const chartDefault = `[{"caller": "system:serviceaccount:catalyst-system:catalyst-conformance", "organizations": ["conformance"], "maxTier": "viewer", "agents": ["conformance-probe"]}]`

func newSigner(t *testing.T) *handoverjwt.Signer {
	t.Helper()
	priv, _, err := handoverjwt.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	s, err := handoverjwt.New(priv, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// reviewer is a cluster that holds objects and whose TokenReview resolves
// every token to user, or refuses it when user is "".
func reviewer(user string, objects ...k8sruntime.Object) Clients {
	client := fakek8s.NewClientset(objects...)
	client.PrependReactor("create", "tokenreviews", func(action clienttesting.Action) (bool, k8sruntime.Object, error) {
		out := action.(clienttesting.CreateAction).GetObject().(*authnv1.TokenReview).DeepCopy()
		if user == "" {
			out.Status = authnv1.TokenReviewStatus{Error: "token expired"}
		} else {
			out.Status = authnv1.TokenReviewStatus{Authenticated: true, User: authnv1.UserInfo{Username: user}}
		}
		return true, out, nil
	})
	return func() (kubernetes.Interface, error) { return client, nil }
}

type fixture struct {
	signer *handoverjwt.Signer
	bus    *audit.Bus
	tokens *Tokens
}

func newFixture(t *testing.T, user string) fixture {
	t.Helper()
	t.Setenv(CallersEnv, testCallers)
	t.Setenv("SOVEREIGN_FQDN", testFQDN)
	t.Setenv("CATALYST_PIN_ISSUER", "")
	s := newSigner(t)
	bus := audit.NewBus(audit.BusConfig{})
	return fixture{signer: s, bus: bus, tokens: New(s, reviewer(user), bus, nil)}
}

func issue(t *testing.T, tk *Tokens, bearer, body string) (int, map[string]string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/agent-tokens", strings.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	tk.HandleIssue(rec, req)
	out := map[string]string{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func segment(t *testing.T, token string, i int) map[string]any {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[i])
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]any{}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func keysOf(m map[string]any) string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

// TestIssueHeaderAndClaims: a token has exactly the header and the claims of
// section 2.1 of the contract, its signature verifies with the signer's key,
// and the issue writes one audit event that holds no token.
func TestIssueHeaderAndClaims(t *testing.T) {
	f := newFixture(t, testCaller)
	code, out := issue(t, f.tokens, "projected", `{"organization": "acme", "agent": "correlator", "tier": "viewer"}`)
	if code != http.StatusOK {
		t.Fatalf("issue = %d %v", code, out)
	}
	token := out["token"]
	header, claims := segment(t, token, 0), segment(t, token, 1)
	if got := keysOf(header); got != "alg kid typ" {
		t.Fatalf("header keys %q", got)
	}
	if header["alg"] != "RS256" || header["typ"] != "JWT" || header["kid"] != f.signer.KeyID() {
		t.Fatalf("header %v", header)
	}
	if got := keysOf(claims); got != "agent aud caller email email_verified exp iat iss jti org org_id realm_access role sub tier typ" {
		t.Fatalf("claim keys %q", got)
	}
	want := map[string]any{
		"iss": "https://console." + testFQDN, "sub": "agent:acme/correlator", "typ": "agent", "org": "acme", "org_id": "acme",
		"agent": "correlator", "tier": "viewer", "role": "openova-user", "email": "correlator@acme.agents.invalid",
		"email_verified": false, "caller": testCaller,
	}
	for k, v := range want {
		if claims[k] != v {
			t.Errorf("claim %s = %v, want %v", k, claims[k], v)
		}
	}
	if b, _ := json.Marshal(claims["aud"]); string(b) != `["model-gateway","platform-tools","catalyst-api"]` {
		t.Errorf("aud = %s", b)
	}
	if b, _ := json.Marshal(claims["realm_access"]); string(b) != `{"roles":["catalyst-viewer"]}` {
		t.Errorf("realm_access = %s", b)
	}
	if exp, iat := claims["exp"].(float64), claims["iat"].(float64); exp-iat != 1800 {
		t.Errorf("life %v seconds, want 1800", exp-iat)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`).MatchString(claims["jti"].(string)) {
		t.Errorf("jti %v is not a UUID", claims["jti"])
	}
	if out["expiresAt"] != time.Unix(int64(claims["exp"].(float64)), 0).UTC().Format(time.RFC3339) {
		t.Errorf("expiresAt %q is not exp", out["expiresAt"])
	}
	pub, _ := f.signer.PublicRSAKey()
	if _, err := jwt.Parse(token, func(*jwt.Token) (any, error) { return pub, nil }, jwt.WithValidMethods([]string{"RS256"})); err != nil {
		t.Fatalf("the signature does not verify: %v", err)
	}
	events := f.bus.List("", func(t string) bool { return t == AuditType }, 0)
	if len(events) != 1 || events[0].Actor != testCaller || events[0].Tier != "viewer" || events[0].TargetUser != "agent:acme/correlator" ||
		!strings.Contains(events[0].Detail, "organization=acme agent=correlator") || !strings.Contains(events[0].Detail, "jti="+claims["jti"].(string)) {
		t.Fatalf("audit events %+v", events)
	}
	if b, _ := json.Marshal(events); strings.Contains(string(b), token) {
		t.Fatal("the audit event holds the token")
	}
}

// TestIssueLife: the life is 60 to 3600 seconds; outside that the request is refused.
func TestIssueLife(t *testing.T) {
	f := newFixture(t, testCaller)
	for life, want := range map[string]int{"60": 200, "3600": 200, "59": 422, "3601": 422, "0": 422, "60.5": 422} {
		code, out := issue(t, f.tokens, "projected", `{"organization": "acme", "agent": "correlator", "tier": "developer", "ttlSeconds": `+life+`}`)
		if code != want {
			t.Errorf("ttlSeconds %s: %d %v, want %d", life, code, out, want)
			continue
		}
		if code == 200 {
			c := segment(t, out["token"], 1)
			if got := c["exp"].(float64) - c["iat"].(float64); got != float64(map[string]int{"60": 60, "3600": 3600}[life]) {
				t.Errorf("ttlSeconds %s: life %v", life, got)
			}
		} else if out["error"] != "invalid-request" {
			t.Errorf("ttlSeconds %s: error %q", life, out["error"])
		}
	}
}

// TestIssueRefusals: every refusal of the contract, with its status and
// code, and no audit event for any of them.
func TestIssueRefusals(t *testing.T) {
	good := `{"organization": "acme", "agent": "correlator", "tier": "viewer"}`
	cases := []struct {
		name   string
		user   string // the TokenReview user; "" refuses the token
		env    func(t *testing.T)
		bearer string
		body   string
		status int
		code   string
	}{
		{"no bearer", testCaller, nil, "", good, 401, "missing-bearer"},
		{"the review refuses the token", "", nil, "projected", good, 502, "token-review-failed"},
		{"a caller of no entry", "system:serviceaccount:default:other", nil, "projected", good, 403, "unauthorized-sa"},
		{"an Organization outside the entry", testCaller, nil, "projected", `{"organization": "other", "agent": "correlator", "tier": "viewer"}`, 403, "caller-not-allowed"},
		{"an agent outside the entry", testCaller, nil, "projected", `{"organization": "acme", "agent": "planner", "tier": "viewer"}`, 403, "caller-not-allowed"},
		{"a tier above the entry", testCaller, func(t *testing.T) {
			t.Setenv(CallersEnv, strings.Replace(testCallers, "developer", "viewer", 1))
		}, "projected", `{"organization": "acme", "agent": "correlator", "tier": "developer"}`, 403, "caller-not-allowed"},
		{"tier admin", testCaller, nil, "projected", `{"organization": "acme", "agent": "correlator", "tier": "admin"}`, 422, "invalid-request"},
		{"an unknown field", testCaller, nil, "projected", `{"organization": "acme", "agent": "correlator", "tier": "viewer", "person": "x"}`, 422, "invalid-request"},
		{"two objects", testCaller, nil, "projected", good + good, 422, "invalid-request"},
		{"an agent name of the wrong form", testCaller, nil, "projected", `{"organization": "acme", "agent": "Correlator", "tier": "viewer"}`, 422, "invalid-request"},
		{"no Organization", testCaller, nil, "projected", `{"agent": "correlator", "tier": "viewer"}`, 422, "invalid-request"},
		{"the callers are not set", testCaller, func(t *testing.T) { t.Setenv(CallersEnv, ""); _ = os.Unsetenv(CallersEnv) }, "projected", good, 503, "agent-tokens-misconfigured"},
		{"the callers are not JSON", testCaller, func(t *testing.T) { t.Setenv(CallersEnv, "[{") }, "projected", good, 503, "agent-tokens-misconfigured"},
		{"an entry without agents", testCaller, func(t *testing.T) {
			t.Setenv(CallersEnv, `[{"caller": "`+testCaller+`", "organizations": ["acme"], "maxTier": "viewer"}]`)
		}, "projected", good, 503, "agent-tokens-misconfigured"},
		{"no SOVEREIGN_FQDN", testCaller, func(t *testing.T) { t.Setenv("SOVEREIGN_FQDN", "") }, "projected", good, 503, "agent-tokens-misconfigured"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, c.user)
			if c.env != nil {
				c.env(t)
			}
			code, out := issue(t, f.tokens, c.bearer, c.body)
			if code != c.status || out["error"] != c.code {
				t.Fatalf("%d %v, want %d %s", code, out, c.status, c.code)
			}
			if out["token"] != "" {
				t.Fatal("a refusal holds a token")
			}
			if n := len(f.bus.List("", nil, 0)); n != 0 {
				t.Fatalf("a refusal wrote %d audit events", n)
			}
		})
	}
	t.Run("no signer", func(t *testing.T) {
		f := newFixture(t, testCaller)
		code, out := issue(t, New(nil, reviewer(testCaller), f.bus, nil), "projected", good)
		if code != 503 || out["error"] != "signer-unavailable" {
			t.Fatalf("%d %v", code, out)
		}
	})
}

// TestParseCallers: the chart's default parses to its one entry, and a
// value that breaks a rule of the entry is refused.
func TestParseCallers(t *testing.T) {
	got, err := ParseCallers(chartDefault)
	if err != nil || len(got) != 1 || got[0].Caller != "system:serviceaccount:catalyst-system:catalyst-conformance" ||
		strings.Join(got[0].Organizations, ",") != "conformance" || got[0].MaxTier != "viewer" || strings.Join(got[0].Agents, ",") != "conformance-probe" {
		t.Fatalf("the chart default parses to %+v, %v", got, err)
	}
	for _, bad := range []string{
		``, `null`, `{}`, `[1]`,
		`[{"caller": "a", "organizations": ["x"], "maxTier": "admin", "agents": ["b1"]}]`,
		`[{"caller": "a", "organizations": [], "maxTier": "viewer", "agents": ["b1"]}]`,
		`[{"caller": "a", "organizations": ["X"], "maxTier": "viewer", "agents": ["b1"]}]`,
		`[{"caller": "a", "organizations": ["x"], "maxTier": "viewer", "agents": ["1b"]}]`,
		`[{"caller": "", "organizations": ["x"], "maxTier": "viewer", "agents": ["b1"]}]`,
		`[{"caller": "a", "organizations": ["x"], "maxTier": "viewer", "agents": ["b1"], "extra": 1}]`,
		`[{"caller": "a", "organizations": ["x"], "maxTier": "viewer", "agents": ["b1"]}, {"caller": "a", "organizations": ["*"], "maxTier": "viewer", "agents": ["*"]}]`,
	} {
		if _, err := ParseCallers(bad); err == nil {
			t.Errorf("ParseCallers(%s) accepted it", bad)
		}
	}
	if got, err := ParseCallers(`[]`); err != nil || len(got) != 0 {
		t.Errorf("an empty list: %v, %v", got, err)
	}
}

// TestKeySet: the key set route serves one key, the key a token names,
// with the cache header of the contract; without a signer it answers 503.
func TestKeySet(t *testing.T) {
	f := newFixture(t, testCaller)
	rec := httptest.NewRecorder()
	f.tokens.HandleJWKS(rec, httptest.NewRequest(http.MethodGet, "/api/v1/auth/signers.jwks", nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" || rec.Header().Get("Cache-Control") != "max-age=300" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	var set struct {
		Keys []map[string]string `json:"keys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &set); err != nil || len(set.Keys) != 1 {
		t.Fatalf("key set %s: %v", rec.Body, err)
	}
	k := set.Keys[0]
	_, out := issue(t, f.tokens, "projected", `{"organization": "acme", "agent": "correlator", "tier": "viewer"}`)
	if k["kid"] != segment(t, out["token"], 0)["kid"] || k["kty"] != "RSA" || k["use"] != "sig" || k["alg"] != "RS256" || k["e"] != "AQAB" {
		t.Fatalf("key %v", k)
	}
	n, _ := base64.RawURLEncoding.DecodeString(k["n"])
	pub, _ := f.signer.PublicRSAKey()
	if new(big.Int).SetBytes(n).Cmp(pub.N) != 0 {
		t.Fatal("the key set holds another modulus")
	}
	rec = httptest.NewRecorder()
	New(nil, nil, nil, nil).HandleJWKS(rec, httptest.NewRequest(http.MethodGet, "/api/v1/auth/signers.jwks", nil))
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "signer-unavailable") {
		t.Fatalf("no signer: %d %s", rec.Code, rec.Body)
	}
}

// restricted runs Restrict for a request carrying claims (nil: none) and
// returns the status and whether the next handler ran.
func restricted(claims *auth.Claims, method, path string) (int, bool) {
	reached := false
	h := Restrict(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(method, path, nil)
	if claims != nil {
		req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, reached
}

// TestRestrict: an agent token reads and changes nothing; a token without
// the audience is refused; sessions and requests without claims pass.
func TestRestrict(t *testing.T) {
	if len(MutationsAllowed) != 0 {
		t.Fatalf("MutationsAllowed has %d entries; it is empty", len(MutationsAllowed))
	}
	agent := &auth.Claims{Typ: "agent", Tier: "viewer", Org: "acme", Audience: []string{AudienceGateway, AudienceTools, AudienceAPI}}
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		if code, reached := restricted(agent, m, "/api/v1/org/applications"); code != 200 || !reached {
			t.Errorf("agent %s: %d reached=%v", m, code, reached)
		}
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if code, reached := restricted(agent, m, "/api/v1/org/applications"); code != 403 || reached {
			t.Errorf("agent %s: %d reached=%v, want 403 agent-token-forbidden", m, code, reached)
		}
	}
	noAudience := &auth.Claims{Typ: "agent", Tier: "viewer", Org: "acme", Audience: []string{AudienceGateway}}
	if code, reached := restricted(noAudience, http.MethodGet, "/api/v1/org/applications"); code != 401 || reached {
		t.Errorf("no audience: %d reached=%v, want 401", code, reached)
	}
	for _, c := range []*auth.Claims{nil, {Tier: "admin", Email: "operator@openova.io"}, {Typ: "Bearer", Tier: "admin"}} {
		if code, reached := restricted(c, http.MethodPost, "/api/v1/org/applications"); code != 200 || !reached {
			t.Errorf("claims %+v: %d reached=%v, want a pass", c, code, reached)
		}
	}
	MutationsAllowed = []Mutation{{http.MethodPost, regexp.MustCompile(`^/api/v1/org/applications$`), "developer"}}
	defer func() { MutationsAllowed = []Mutation{} }()
	if code, _ := restricted(agent, http.MethodPost, "/api/v1/org/applications"); code != 403 {
		t.Errorf("a viewer on a developer entry: %d", code)
	}
	developer := &auth.Claims{Typ: "agent", Tier: "developer", Org: "acme", Audience: []string{AudienceAPI}}
	if code, reached := restricted(developer, http.MethodPost, "/api/v1/org/applications"); code != 200 || !reached {
		t.Errorf("a developer on a developer entry: %d", code)
	}
	if code, _ := restricted(developer, http.MethodPost, "/api/v1/org/applications/x"); code != 403 {
		t.Errorf("another path: %d", code)
	}
}

// TestAgentTokenThroughTheSessionChain: a token from the issue route passes
// the session check, is confined by the Organization scope guard and by
// Restrict, in the order of the session route group.
func TestAgentTokenThroughTheSessionChain(t *testing.T) {
	f := newFixture(t, testCaller)
	_, out := issue(t, f.tokens, "projected", `{"organization": "acme", "agent": "correlator", "tier": "viewer"}`)
	pub, _ := f.signer.PublicRSAKey()
	cfg := &auth.Config{LocalPublicKey: pub, LocalKeyID: f.signer.KeyID()} // no key-set cache: a fetch would panic
	chain := auth.RequireSession(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))((&handler.Handler{}).OrgScopeGuard(Restrict(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))))
	for _, c := range []struct {
		method, path string
		status       int
		code         string
	}{
		{http.MethodGet, "/api/v1/org/applications", 200, ""},
		{http.MethodGet, "/api/v1/sovereigns/29b7e14918178f7e/applications", 403, "org-scoped-forbidden"},
		{http.MethodGet, "/api/v1/deployments/29b7e14918178f7e", 403, "org-scoped-forbidden"},
		{http.MethodPost, "/api/v1/org/applications", 403, "agent-token-forbidden"},
	} {
		req := httptest.NewRequest(c.method, c.path, nil)
		req.Header.Set("Authorization", "Bearer "+out["token"])
		rec := httptest.NewRecorder()
		chain.ServeHTTP(rec, req)
		if rec.Code != c.status || !strings.Contains(rec.Body.String(), c.code) {
			t.Errorf("%s %s: %d %s, want %d %s", c.method, c.path, rec.Code, rec.Body, c.status, c.code)
		}
	}
}

// The default of the chart value agentTokens.callers: the conformance entry
// and the entry of the agent workspace of each Organization.
const shippedDefault = `[{"caller": "system:serviceaccount:catalyst-system:catalyst-conformance", "organizations": ["conformance"], "maxTier": "viewer", "agents": ["conformance-probe"]}, {"caller": "system:serviceaccount:{organization}:agenity", "organizations": ["{organization}"], "maxTier": "developer", "agents": ["agenity"]}]`

// The ServiceAccount the agent workspace runs as, and its agent name.
const workspaceAccount = "agenity"

func account(ns, name string) string { return "system:serviceaccount:" + ns + ":" + name }

func namespace(name, org string) *corev1.Namespace {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if org != "" {
		ns.Labels = map[string]string{OrganizationLabel: org}
	}
	return ns
}

// newWorkspaceFixture is newFixture with the shipped default as the callers,
// in a cluster where acme and acme-data are namespaces of Organization acme,
// globex one of globex, catalyst-system no namespace of an Organization, and
// odd carries a label that is no slug.
func newWorkspaceFixture(t *testing.T, user string) fixture {
	t.Helper()
	f := newFixture(t, user)
	t.Setenv(CallersEnv, shippedDefault)
	f.tokens = New(f.signer, reviewer(user, namespace("acme", "acme"), namespace("acme-data", "acme"), namespace("globex", "globex"),
		namespace("catalyst-system", ""), namespace("odd", "Not A Slug")), f.bus, nil)
	return f
}

func workspaceBody(org, tier string) string {
	return `{"organization": "` + org + `", "agent": "` + workspaceAccount + `", "tier": "` + tier + `"}`
}

// TestWorkspaceCallerGetsItsOwnOrganization: under the shipped default the
// workspace's account in a namespace of an Organization gets a token for that
// Organization, at viewer and at developer, with no entry that names it; the
// token and the audit event name the account. The conformance entry still
// admits its caller exactly.
func TestWorkspaceCallerGetsItsOwnOrganization(t *testing.T) {
	for _, c := range []struct{ ns, org, tier string }{
		{"acme", "acme", "developer"}, {"acme", "acme", "viewer"}, {"acme-data", "acme", "developer"}, {"globex", "globex", "developer"},
	} {
		user := account(c.ns, workspaceAccount)
		f := newWorkspaceFixture(t, user)
		code, out := issue(t, f.tokens, "projected", workspaceBody(c.org, c.tier))
		if code != http.StatusOK {
			t.Fatalf("%s for %s: %d %v", user, c.org, code, out)
		}
		claims := segment(t, out["token"], 1)
		if claims["org"] != c.org || claims["org_id"] != c.org || claims["sub"] != "agent:"+c.org+"/"+workspaceAccount ||
			claims["tier"] != c.tier || claims["caller"] != user {
			t.Errorf("%s for %s: claims %v", user, c.org, claims)
		}
		if events := f.bus.List("", func(t string) bool { return t == AuditType }, 0); len(events) != 1 || events[0].Actor != user {
			t.Errorf("%s for %s: audit events %+v", user, c.org, events)
		}
	}
	f := newWorkspaceFixture(t, account("catalyst-system", "catalyst-conformance"))
	if code, out := issue(t, f.tokens, "projected", `{"organization": "conformance", "agent": "conformance-probe", "tier": "viewer"}`); code != http.StatusOK {
		t.Fatalf("the conformance entry: %d %v", code, out)
	}
}

// TestWorkspaceCallerIsRefusedAnotherOrganization: the workspace's account of
// Organization acme gets 403 caller-not-allowed for any other Organization,
// also one whose namespace exists, and for an agent outside the entry; no
// refusal holds a token or writes an audit event.
func TestWorkspaceCallerIsRefusedAnotherOrganization(t *testing.T) {
	for _, body := range []string{
		workspaceBody("globex", "viewer"), workspaceBody("globex", "developer"), workspaceBody("conformance", "viewer"),
		workspaceBody("acme-data", "viewer"), workspaceBody("nowhere", "viewer"),
		`{"organization": "acme", "agent": "planner", "tier": "viewer"}`,
	} {
		f := newWorkspaceFixture(t, account("acme", workspaceAccount))
		code, out := issue(t, f.tokens, "projected", body)
		if code != http.StatusForbidden || out["error"] != "caller-not-allowed" || out["token"] != "" {
			t.Errorf("%s: %d %v, want 403 caller-not-allowed", body, code, out)
		}
		if n := len(f.bus.List("", nil, 0)); n != 0 {
			t.Errorf("%s: a refusal wrote %d audit events", body, n)
		}
	}
}

// TestWorkspaceCallerOutsideAnOrganizationIsRefused: the workspace's account
// in a namespace of no Organization, in a namespace that does not exist or
// whose label is no slug, every other account of a namespace of an
// Organization, and a user that is no ServiceAccount get 403 unauthorized-sa.
func TestWorkspaceCallerOutsideAnOrganizationIsRefused(t *testing.T) {
	for _, user := range []string{
		account("catalyst-system", workspaceAccount), account("missing", workspaceAccount), account("odd", workspaceAccount),
		account(OwnOrganization, workspaceAccount), account("acme", "default"), account("acme", "agenity-x"),
		account("acme", "catalyst-conformance"), workspaceAccount, "system:node:acme",
	} {
		f := newWorkspaceFixture(t, user)
		code, out := issue(t, f.tokens, "projected", workspaceBody("acme", "viewer"))
		if code != http.StatusForbidden || out["error"] != "unauthorized-sa" || out["token"] != "" {
			t.Errorf("%s: %d %v, want 403 unauthorized-sa", user, code, out)
		}
		if n := len(f.bus.List("", nil, 0)); n != 0 {
			t.Errorf("%s: a refusal wrote %d audit events", user, n)
		}
	}
}

// TestWorkspaceCallerNeverAboveDeveloper: a tier above developer is refused
// to the workspace's account, an entry of the own-Organization form of
// maxTier viewer refuses developer, and none may have a maxTier above
// developer.
func TestWorkspaceCallerNeverAboveDeveloper(t *testing.T) {
	user := account("acme", workspaceAccount)
	f := newWorkspaceFixture(t, user)
	for _, tier := range []string{"admin", "owner", "superadmin"} {
		if code, out := issue(t, f.tokens, "projected", workspaceBody("acme", tier)); code != http.StatusUnprocessableEntity || out["error"] != "invalid-request" {
			t.Errorf("tier %s: %d %v, want 422 invalid-request", tier, code, out)
		}
	}
	viewerOnly := `[{"caller": "system:serviceaccount:{organization}:agenity", "organizations": ["{organization}"], "maxTier": "viewer", "agents": ["agenity"]}]`
	t.Setenv(CallersEnv, viewerOnly)
	if code, out := issue(t, f.tokens, "projected", workspaceBody("acme", "developer")); code != http.StatusForbidden || out["error"] != "caller-not-allowed" {
		t.Errorf("developer on a viewer entry: %d %v", code, out)
	}
	if code, out := issue(t, f.tokens, "projected", workspaceBody("acme", "viewer")); code != http.StatusOK {
		t.Errorf("viewer on a viewer entry: %d %v", code, out)
	}
	for _, tier := range []string{"admin", "owner"} {
		if _, err := ParseCallers(strings.Replace(viewerOnly, `"viewer"`, `"`+tier+`"`, 1)); err == nil {
			t.Errorf("an entry of maxTier %s parsed", tier)
		}
	}
}

// TestParseOwnOrganizationForm: OwnOrganization is accepted only as the whole
// namespace of the caller together with organizations exactly
// ["{organization}"]; an entry that names its caller has no account.
func TestParseOwnOrganizationForm(t *testing.T) {
	entry := func(caller, orgs string) string {
		return `{"caller": "` + caller + `", "organizations": ` + orgs + `, "maxTier": "developer", "agents": ["agenity"]}`
	}
	own := "system:serviceaccount:{organization}:agenity"
	for _, bad := range []string{
		entry(own, `["acme"]`), entry(own, `["*"]`), entry(own, `["{organization}", "acme"]`),
		entry("system:serviceaccount:acme:agenity", `["{organization}"]`), entry("system:serviceaccount:{organization}:", `["{organization}"]`),
		entry("system:serviceaccount:{organization}:{organization}", `["{organization}"]`), entry("system:serviceaccount:x{organization}:agenity", `["{organization}"]`),
		entry("{organization}", `["{organization}"]`), entry(own, `["{organization}"]`) + ", " + entry(own, `["{organization}"]`),
	} {
		if _, err := ParseCallers("[" + bad + "]"); err == nil {
			t.Errorf("ParseCallers([%s]) accepted it", bad)
		}
	}
	got, err := ParseCallers(shippedDefault)
	if err != nil || len(got) != 2 || got[0].account != "" || got[1].account != workspaceAccount {
		t.Fatalf("the shipped default parses to %+v, %v", got, err)
	}
}

// TestShippedCallersDefault: the default of agentTokens.callers in the
// chart's values and its literal in the raw Kustomize deployment both equal
// shippedDefault, which keeps the conformance entry and names no
// Organization wildcard.
func TestShippedCallersDefault(t *testing.T) {
	// values.yaml is read line by line: the file has a duplicate key elsewhere,
	// which Helm accepts and a strict YAML parser refuses.
	b, err := os.ReadFile("../../../../chart/values.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var values struct {
		Callers string `yaml:"callers"`
	}
	lines, found := strings.Split(string(b), "\n"), 0
	for i := 1; i < len(lines); i++ {
		if lines[i-1] == "agentTokens:" && strings.HasPrefix(lines[i], "  callers: ") {
			found++
			err = yaml.Unmarshal([]byte(strings.TrimSpace(lines[i])), &values)
		}
	}
	if found != 1 || err != nil || values.Callers != shippedDefault {
		t.Errorf("values.yaml agentTokens.callers (%d lines) = %q, %v", found, values.Callers, err)
	}
	var deployment struct {
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Env []struct {
							Name  string `yaml:"name"`
							Value string `yaml:"value"`
						} `yaml:"env"`
					} `yaml:"containers"`
				} `yaml:"spec"`
			} `yaml:"template"`
		} `yaml:"spec"`
	}
	if b, err = os.ReadFile("../../../../chart/templates/api-deployment-kustomize.yaml"); err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(b, &deployment); err != nil {
		t.Fatal(err)
	}
	literals := []string{}
	for _, c := range deployment.Spec.Template.Spec.Containers {
		for _, e := range c.Env {
			if e.Name == CallersEnv {
				literals = append(literals, e.Value)
			}
		}
	}
	if len(literals) != 1 || literals[0] != shippedDefault {
		t.Errorf("api-deployment-kustomize.yaml %s = %q", CallersEnv, literals)
	}
	if !strings.Contains(shippedDefault, chartDefault[1:len(chartDefault)-1]) || strings.Contains(shippedDefault, `"*"`) {
		t.Errorf("the shipped default drops the conformance entry or holds a wildcard")
	}
}
