package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAnthropicAPI stands in for api.anthropic.com. It answers /v1/models
// with a canned status + body and records the auth headers it saw, so a test
// can assert the probe authenticated the way the runtime does (Bearer +
// oauth beta for an sk-ant-oat token, x-api-key for a real key) — sending the
// wrong header 401s a LIVE credential and would manufacture a false "invalid".
type fakeAnthropicAPI struct {
	mu        sync.Mutex
	status    int
	body      string
	calls     int
	gotAuth   string
	gotAPIKey string
	gotBeta   string
	srv       *httptest.Server
}

func newFakeAnthropicAPI(t *testing.T, status int, body string) *fakeAnthropicAPI {
	t.Helper()
	f := &fakeAnthropicAPI{status: status, body: body}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls++
		f.gotAuth = r.Header.Get("Authorization")
		f.gotAPIKey = r.Header.Get("x-api-key")
		f.gotBeta = r.Header.Get("anthropic-beta")
		status, body := f.status, f.body
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAnthropicAPI) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// oauthBlob builds a credentials.json exactly as the chart's
// seed-claude-creds init container materialises it, with expiresAt placed
// `in` from now (negative ⇒ already expired).
func oauthBlob(token string, in time.Duration) string {
	b, _ := json.Marshal(map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken":  token,
			"refreshToken": "sk-ant-ort01-refresh",
			"expiresAt":    time.Now().Add(in).UnixMilli(),
			"scopes":       []string{"user:inference"},
		},
	})
	return string(b)
}

// The API's real 401 envelopes, kept verbatim as the discriminating control
// (#5956): a token that never existed reads "is invalid"; a real token killed
// upstream reads "has been revoked". A classifier that folded both into "bad"
// would give the operator the wrong cue (fix the typo vs re-seed).
const (
	revokedEnvelope = `{"type":"error","error":{"type":"authentication_error","message":"OAuth access token has been revoked"}}`
	invalidEnvelope = `{"type":"error","error":{"type":"authentication_error","message":"OAuth authentication is currently not supported. x-api-key header is invalid"}}`
	modelsEnvelope  = `{"data":[{"id":"claude-opus-4-5","type":"model"}],"has_more":false}`
)

// Test_classifyAnthropicCredential_RevokedButUnexpiredIsNotHealthy is THE
// #5956 test: the precise case every pre-fix surface passed.
//
// The credential here is unexpired (expiresAt 7h in the FUTURE), well-formed,
// and fully delivered — so the ExternalSecret says SecretSynced, the chart's
// #4111 pre-flight prints "claude-code OAuth token valid (~7h remaining)."
// and the seed reconciler's presence check says "healthy — no churn". All
// three are wrong: the API rejects it as revoked.
func Test_classifyAnthropicCredential_RevokedButUnexpiredIsNotHealthy(t *testing.T) {
	api := newFakeAnthropicAPI(t, http.StatusUnauthorized, revokedEnvelope)
	creds := oauthBlob("sk-ant-oat01-live-shaped-token", 7*time.Hour)

	// Control 1 — the EXPIRY check, which is all the #4111 pre-flight does,
	// sees nothing wrong. If this ever fails the fixture stopped reproducing
	// the bug and the rest of the test proves nothing.
	if _, expiresAt := anthropicBearer("", creds); expiresAt <= time.Now().UnixMilli() {
		t.Fatalf("fixture is not the #5956 case: token must be UNexpired, got expiresAt=%d now=%d",
			expiresAt, time.Now().UnixMilli())
	}

	health, detail := classifyAnthropicCredential(context.Background(),
		api.srv.Client(), api.srv.URL, "", creds)

	if health.OK() {
		t.Fatalf("revoked-but-unexpired credential reported HEALTHY (%q) — this is exactly #5956", detail)
	}
	if health != AnthropicCredentialRevoked {
		t.Errorf("health = %q, want %q (detail: %s)", health, AnthropicCredentialRevoked, detail)
	}
	if api.callCount() != 1 {
		t.Errorf("probe calls = %d, want 1 — validity must be decided by the API, not locally", api.callCount())
	}
	// Control 2 — the probe must authenticate the way the runtime does.
	api.mu.Lock()
	gotAuth, gotBeta, gotAPIKey := api.gotAuth, api.gotBeta, api.gotAPIKey
	api.mu.Unlock()
	if !strings.HasPrefix(gotAuth, "Bearer ") {
		t.Errorf("Authorization = %q, want a Bearer for an sk-ant-oat token", gotAuth)
	}
	if gotBeta != anthropicOAuthBeta {
		t.Errorf("anthropic-beta = %q, want %q — without it a LIVE OAuth token 401s", gotBeta, anthropicOAuthBeta)
	}
	if gotAPIKey != "" {
		t.Errorf("x-api-key = %q, want empty for an OAuth token", gotAPIKey)
	}
}

// Test_classifyAnthropicCredential_Classes pins every other branch, and in
// particular that NOTHING but a positive API response yields healthy.
func Test_classifyAnthropicCredential_Classes(t *testing.T) {
	live := "sk-ant-oat01-live-shaped-token"

	cases := []struct {
		name       string
		status     int
		body       string
		apiKey     string
		creds      string
		want       AnthropicCredentialHealth
		wantProbes int
	}{
		{"accepted", http.StatusOK, modelsEnvelope, "", oauthBlob(live, 7*time.Hour), AnthropicCredentialHealthy, 1},
		{"rate-limited-after-auth", http.StatusTooManyRequests, `{"error":{"message":"rate_limit"}}`, "", oauthBlob(live, 7*time.Hour), AnthropicCredentialHealthy, 1},
		{"revoked", http.StatusUnauthorized, revokedEnvelope, "", oauthBlob(live, 7*time.Hour), AnthropicCredentialRevoked, 1},
		{"invalid", http.StatusUnauthorized, invalidEnvelope, "", oauthBlob("sk-ant-oat01-typo", 7*time.Hour), AnthropicCredentialInvalid, 1},
		{"forbidden-is-not-healthy", http.StatusForbidden, `{"error":{"message":"permission"}}`, "", oauthBlob(live, 7*time.Hour), AnthropicCredentialInvalid, 1},
		// Expired short-circuits BEFORE the probe: no call can rescue it, and
		// a credless Sovereign must not reach out to Anthropic at all.
		{"expired-skips-the-probe", http.StatusOK, modelsEnvelope, "", oauthBlob(live, -7*time.Hour), AnthropicCredentialExpired, 0},
		{"absent", http.StatusOK, modelsEnvelope, "", "", AnthropicCredentialAbsent, 0},
		{"absent-hollow-blob", http.StatusOK, modelsEnvelope, "", `{"claudeAiOauth":{}}`, AnthropicCredentialAbsent, 0},
		// A 5xx tells us nothing about the credential — "unknown" must never
		// collapse into "green".
		{"server-error-is-unverified", http.StatusBadGateway, `bad gateway`, "", oauthBlob(live, 7*time.Hour), AnthropicCredentialUnverified, 1},
		// A bare API key travels in x-api-key, not Bearer.
		{"bare-api-key-accepted", http.StatusOK, modelsEnvelope, "sk-ant-api03-real-key", "", AnthropicCredentialHealthy, 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := newFakeAnthropicAPI(t, tc.status, tc.body)
			got, detail := classifyAnthropicCredential(context.Background(),
				api.srv.Client(), api.srv.URL, tc.apiKey, tc.creds)
			if got != tc.want {
				t.Errorf("health = %q, want %q (detail: %s)", got, tc.want, detail)
			}
			if api.callCount() != tc.wantProbes {
				t.Errorf("probe calls = %d, want %d", api.callCount(), tc.wantProbes)
			}
			if tc.want != AnthropicCredentialHealthy && got.OK() {
				t.Errorf("OK() true for non-healthy class %q", got)
			}
			if tc.name == "bare-api-key-accepted" {
				api.mu.Lock()
				gotKey, gotAuth := api.gotAPIKey, api.gotAuth
				api.mu.Unlock()
				if gotKey != tc.apiKey {
					t.Errorf("x-api-key = %q, want the bare key", gotKey)
				}
				if gotAuth != "" {
					t.Errorf("Authorization = %q, want empty for a bare API key", gotAuth)
				}
			}
		})
	}
}

// Test_classifyAnthropicCredential_UnreachableIsNotHealthy — the failure mode
// that matters most in production. When the probe cannot be completed the
// answer is "we do not know", and "we do not know" must never be rendered as
// working. This is the same shape as reporting SecretSynced=True as proof the
// runtime can chat.
func Test_classifyAnthropicCredential_UnreachableIsNotHealthy(t *testing.T) {
	creds := oauthBlob("sk-ant-oat01-live-shaped-token", 7*time.Hour)

	// (a) endpoint refuses connections.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	deadClient := dead.Client()
	dead.Close()
	if got, detail := classifyAnthropicCredential(context.Background(), deadClient, deadURL, "", creds); got.OK() {
		t.Errorf("unreachable endpoint reported healthy (%q): %s", got, detail)
	} else if got != AnthropicCredentialUnverified {
		t.Errorf("unreachable endpoint health = %q, want %q", got, AnthropicCredentialUnverified)
	}

	// (b) no probe client at all — the "we never checked" case.
	if got, detail := classifyAnthropicCredential(context.Background(), nil, "", "", creds); got.OK() {
		t.Errorf("nil probe client reported healthy (%q): %s", got, detail)
	} else if got != AnthropicCredentialUnverified {
		t.Errorf("nil probe client health = %q, want %q", got, AnthropicCredentialUnverified)
	}
}

// Test_anthropicBearer_PrefersOAuthTokenOverApiKey pins the credential the
// runtime actually authenticates with.
//
// MEASURED on hw292 2026-08-10: Secret uatco/agenity-anthropic-token carries
// `anthropicApiKey` BYTE-IDENTICAL to credentialsJson.claudeAiOauth
// .accessToken (both 108 bytes, both sk-ant-oat…, same SHA-256). So the
// `apiKey` property is NOT a long-lived fallback — it is a second copy of the
// same revocable OAuth token, and the chart omits ANTHROPIC_API_KEY entirely
// in OAuth mode. Probing the apiKey while the runtime uses the OAuth token
// would test a credential nothing consumes.
func Test_anthropicBearer_PrefersOAuthTokenOverApiKey(t *testing.T) {
	creds := oauthBlob("sk-ant-oat01-oauth-copy", 7*time.Hour)
	tok, exp := anthropicBearer("sk-ant-oat01-oauth-copy", creds)
	if tok != "sk-ant-oat01-oauth-copy" {
		t.Errorf("bearer = %q, want the OAuth accessToken", tok)
	}
	if exp <= 0 {
		t.Errorf("expiresAt = %d, want the blob's expiry to travel with the token", exp)
	}
	// Falls back to the bare key only when there is no OAuth blob.
	if tok, exp := anthropicBearer("sk-ant-api03-real", ""); tok != "sk-ant-api03-real" || exp != 0 {
		t.Errorf("key-only mode: got (%q,%d), want (sk-ant-api03-real,0)", tok, exp)
	}
	// Two identical credentials fingerprint alike; a rotated one does not.
	if anthropicCredentialFingerprint("", creds) != anthropicCredentialFingerprint("", creds) {
		t.Error("fingerprint is not stable")
	}
	if anthropicCredentialFingerprint("", creds) == anthropicCredentialFingerprint("", oauthBlob("sk-ant-oat01-fresh", 7*time.Hour)) {
		t.Error("fingerprint collided across different credentials — rotation would go undetected")
	}
	// It must never be the credential itself.
	if strings.Contains(anthropicCredentialFingerprint("", creds), "sk-ant") {
		t.Error("fingerprint leaks credential material")
	}
}

// Test_reconcileAnthropicCredentialHealth_RevokedIsLoudAndDoesNotChurn walks
// the #5956 case through the ONE periodic surface that is supposed to catch
// it, and asserts the pre-fix check would have missed it.
func Test_reconcileAnthropicCredentialHealth_RevokedIsLoudAndDoesNotChurn(t *testing.T) {
	kv := newRWKVServer(t)
	creds := oauthBlob("sk-ant-oat01-live-shaped-token", 7*time.Hour)
	kv.seedPresent("catalyst/anthropic/token", map[string]any{
		"apiKey":          "sk-ant-oat01-live-shaped-token",
		"credentialsJson": creds,
	})
	api := newFakeAnthropicAPI(t, http.StatusUnauthorized, revokedEnvelope)

	h := &Handler{log: silentLogger()}
	h.openbao = kv.client()
	h.SetAnthropicHealthProbe(api.srv.Client(), api.srv.URL)
	ctx := context.Background()

	// CONTROL — the pre-fix predicate. reconcileGlobalSeed asked exactly this
	// question, and it answers "present ⇒ healthy ⇒ no churn" for a credential
	// the API has revoked. This is the defect, pinned.
	if present, err := h.openbaoPathHasProperty(ctx,
		anthropicSeedMountPath, anthropicSeedSecretPath, "apiKey", "credentialsJson"); err != nil || !present {
		t.Fatalf("control: presence check got (%v,%v), want (true,nil) — the fixture must reproduce the pre-fix green", present, err)
	}

	// The platform env holds the SAME (revoked) credential — the live case.
	t.Setenv(anthropicSeedAPIKeyEnv, "sk-ant-oat01-live-shaped-token")
	t.Setenv(anthropicSeedCredentialsJSONEnv, creds)

	got := h.reconcileAnthropicCredentialHealth(ctx)
	if got.OK() {
		t.Fatalf("reconciler reported the revoked credential as healthy (%q) — #5956 unfixed", got)
	}
	if got != AnthropicCredentialRevoked {
		t.Errorf("health = %q, want %q", got, AnthropicCredentialRevoked)
	}
	// Anti-churn: re-seeding the SAME dead bytes every 10m would grow KV
	// version history forever and re-notify every ExternalSecret for nothing.
	if w := kv.writes(); len(w) != 0 {
		t.Errorf("wrote %v; want NO write when the env holds the same revoked credential", w)
	}
}

// Test_reconcileAnthropicCredentialHealth_ReseedsWhenFounderRotates — the
// other half of the anti-churn rule: a DIFFERENT env credential is a
// rotation, and the loop must push it so the Sovereign self-heals without a
// hand-run bao put.
func Test_reconcileAnthropicCredentialHealth_ReseedsWhenFounderRotates(t *testing.T) {
	kv := newRWKVServer(t)
	kv.seedPresent("catalyst/anthropic/token", map[string]any{
		"apiKey":          "sk-ant-oat01-dead",
		"credentialsJson": oauthBlob("sk-ant-oat01-dead", 7*time.Hour),
	})
	api := newFakeAnthropicAPI(t, http.StatusUnauthorized, revokedEnvelope)

	h := &Handler{log: silentLogger()}
	h.openbao = kv.client()
	h.SetAnthropicHealthProbe(api.srv.Client(), api.srv.URL)

	fresh := oauthBlob("sk-ant-oat01-fresh-from-founder", 7*time.Hour)
	t.Setenv(anthropicSeedAPIKeyEnv, "")
	t.Setenv(anthropicSeedCredentialsJSONEnv, fresh)

	if got := h.reconcileAnthropicCredentialHealth(context.Background()); got != AnthropicCredentialRevoked {
		t.Fatalf("health = %q, want %q", got, AnthropicCredentialRevoked)
	}
	writes := kv.writes()
	if len(writes) != 1 || writes[0] != anthropicSeedSecretPath {
		t.Fatalf("writes = %v, want exactly one to %q — a rotated credential must be pushed", writes, anthropicSeedSecretPath)
	}
}

// Test_reconcileAnthropicCredentialHealth_HealthyIsSilent — a working
// credential must produce zero writes, so the fix cannot be accused of
// trading a false green for constant churn.
func Test_reconcileAnthropicCredentialHealth_HealthyIsSilent(t *testing.T) {
	kv := newRWKVServer(t)
	creds := oauthBlob("sk-ant-oat01-live", 7*time.Hour)
	kv.seedPresent("catalyst/anthropic/token", map[string]any{"apiKey": "", "credentialsJson": creds})
	api := newFakeAnthropicAPI(t, http.StatusOK, modelsEnvelope)

	h := &Handler{log: silentLogger()}
	h.openbao = kv.client()
	h.SetAnthropicHealthProbe(api.srv.Client(), api.srv.URL)
	t.Setenv(anthropicSeedAPIKeyEnv, "")
	t.Setenv(anthropicSeedCredentialsJSONEnv, creds)

	if got := h.reconcileAnthropicCredentialHealth(context.Background()); !got.OK() {
		t.Fatalf("health = %q, want healthy", got)
	}
	if w := kv.writes(); len(w) != 0 {
		t.Errorf("wrote %v; want no write for a healthy credential", w)
	}
}

// Test_reconcileAnthropicCredentialHealth_AbsentStillSelfHeals — the original
// #4877 behaviour must survive: an absent path is still seeded, and no probe
// is issued against a credential that is not there.
func Test_reconcileAnthropicCredentialHealth_AbsentStillSelfHeals(t *testing.T) {
	kv := newRWKVServer(t) // nothing seeded ⇒ GET 404
	api := newFakeAnthropicAPI(t, http.StatusOK, modelsEnvelope)

	h := &Handler{log: silentLogger()}
	h.openbao = kv.client()
	h.SetAnthropicHealthProbe(api.srv.Client(), api.srv.URL)
	t.Setenv(anthropicSeedAPIKeyEnv, "")
	t.Setenv(anthropicSeedCredentialsJSONEnv, oauthBlob("sk-ant-oat01-live", 7*time.Hour))

	if got := h.reconcileAnthropicCredentialHealth(context.Background()); got != AnthropicCredentialAbsent {
		t.Fatalf("health = %q, want %q", got, AnthropicCredentialAbsent)
	}
	if w := kv.writes(); len(w) != 1 || w[0] != anthropicSeedSecretPath {
		t.Errorf("writes = %v, want one seed of %q (#4877 self-heal must survive)", w, anthropicSeedSecretPath)
	}
	if api.callCount() != 0 {
		t.Errorf("probe calls = %d, want 0 — nothing to probe on an absent path", api.callCount())
	}
}

// Test_anthropicHealthClient_ProductionNeverFallsIntoUnverified guards the
// one way this fix could rot into the very shape it removes: if the probe
// seam defaulted to nil in production, every pass would classify
// "unverified" and someone would eventually "fix" that by calling it green.
func Test_anthropicHealthClient_ProductionNeverFallsIntoUnverified(t *testing.T) {
	h := &Handler{log: silentLogger()} // no SetAnthropicHealthProbe call
	if h.anthropicHealthClient() == nil {
		t.Fatal("production probe client is nil — every pass would report unverified")
	}
	if base := h.anthropicAPIBase(); base != anthropicAPIBaseDefault {
		t.Errorf("default probe base = %q, want %q", base, anthropicAPIBaseDefault)
	}
	t.Setenv(anthropicAPIBaseEnv, "https://anthropic.internal")
	if base := h.anthropicAPIBase(); base != "https://anthropic.internal" {
		t.Errorf("env override ignored: base = %q", base)
	}
}

// Test_runSeedReconcilePass_AnthropicLegChecksValidity proves the WIRING —
// that the pass itself (not just the helper) now asks the Anthropic API. The
// helper being correct while the call site still used the presence check is
// the classic "the guard tested a surface that cannot fail" shape.
func Test_runSeedReconcilePass_AnthropicLegChecksValidity(t *testing.T) {
	kv := newRWKVServer(t)
	creds := oauthBlob("sk-ant-oat01-live-shaped-token", 7*time.Hour)
	kv.seedPresent("catalyst/anthropic/token", map[string]any{"apiKey": "", "credentialsJson": creds})
	api := newFakeAnthropicAPI(t, http.StatusUnauthorized, revokedEnvelope)

	h := &Handler{log: silentLogger()}
	h.openbao = kv.client()
	h.SetAnthropicHealthProbe(api.srv.Client(), api.srv.URL)
	t.Setenv(anthropicSeedAPIKeyEnv, "")
	t.Setenv(anthropicSeedCredentialsJSONEnv, creds)

	h.runSeedReconcilePass(context.Background())

	if api.callCount() == 0 {
		t.Fatal("the reconcile pass never probed the Anthropic API — the anthropic leg is still a presence check (#5956)")
	}
	if w := kv.writes(); len(w) != 0 {
		t.Errorf("pass wrote %v; want no churn on an unrotated revoked credential", w)
	}
}

// stubAnthropicProbe points a Handler's #5956 validity probe at a canned
// endpoint. Every test that drives runSeedReconcilePass must call it:
// without a stub the anthropic leg dials the REAL api.anthropic.com, which
// makes the unit suite network-dependent and ships fixture credentials at a
// third party. Pinning the seam is also what keeps "healthy" in those tests
// an asserted fact rather than an accident of the sandbox having no egress.
func stubAnthropicProbe(t *testing.T, h *Handler, status int, body string) {
	t.Helper()
	api := newFakeAnthropicAPI(t, status, body)
	h.SetAnthropicHealthProbe(api.srv.Client(), api.srv.URL)
}
