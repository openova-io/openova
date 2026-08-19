package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/openbao"
)

// #6317 — the Agenity Anthropic credential expires every ~5h and, until this
// file's subject existed, nothing renewed it. Measured on hw296 2026-08-14: the
// workspace agent answered its own PTY with "401 OAuth access token has been
// revoked" while the refresh token sat alive and unspent for another four weeks.
//
// Every assertion below is vacuity-proven in scripts/mutation-6317.sh, which
// mutates ONE behaviour at a time in the subject and requires the NAMED test to
// go red for each. A test that cannot fail is not evidence.

// ─── fixtures ──────────────────────────────────────────────────────────────

// oauthServer — a fake console.anthropic.com token endpoint that records how
// many times it was called, so "the exchange did not happen" is an assertable
// fact rather than an inference from an unchanged value.
type oauthServer struct {
	mu       sync.Mutex
	calls    int
	lastBody map[string]any
	srv      *httptest.Server
}

func newOAuthServer(t *testing.T, status int, respond func() string) *oauthServer {
	t.Helper()
	o := &oauthServer{}
	o.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		_ = json.Unmarshal(body, &parsed)
		o.mu.Lock()
		o.calls++
		o.lastBody = parsed
		o.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respond()))
	}))
	t.Cleanup(o.srv.Close)

	prevURL := anthropicOAuthTokenEndpoint
	prevClient := anthropicOAuthHTTPClient
	anthropicOAuthTokenEndpoint = o.srv.URL
	anthropicOAuthHTTPClient = o.srv.Client()
	t.Cleanup(func() {
		anthropicOAuthTokenEndpoint = prevURL
		anthropicOAuthHTTPClient = prevClient
	})
	return o
}

func (o *oauthServer) callCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.calls
}

func (o *oauthServer) sentRefreshToken() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	s, _ := o.lastBody["refresh_token"].(string)
	return s
}

// okTokenResponse — a well-formed refresh response.
func okTokenResponse(access, refresh string, expiresIn int) func() string {
	return func() string {
		return fmt.Sprintf(`{"access_token":%q,"refresh_token":%q,"expires_in":%d,"token_type":"Bearer"}`,
			access, refresh, expiresIn)
	}
}

// credWithRemaining builds a claudeAiOauth document whose accessToken expires
// `remaining` from now. Carries the extra fields real blobs carry, so the
// preservation assertion has something to lose.
func credWithRemaining(access, refresh string, remaining time.Duration) string {
	return fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q,"refreshToken":%q,"expiresAt":%d,`+
		`"scopes":["user:inference","user:profile"],"subscriptionType":"max","rateLimitTier":"default_max_20x",`+
		`"refreshTokenExpiresAt":%d}}`,
		access, refresh,
		time.Now().Add(remaining).UnixMilli(),
		time.Now().Add(28*24*time.Hour).UnixMilli())
}

// statefulKVServer — a KV-v2 fake that STORES what is written and serves it
// back on read.
//
// The package's captureKVServer records the last request of any method and
// always answers reads with a fixed envelope. That is fine for asserting a
// single producer write, but it cannot model the read-after-write the seed
// reconciler performs, and a read would overwrite the captured write. A whole
// reconcile pass needs a fake that behaves like the store it stands in for.
type statefulKVServer struct {
	mu     sync.Mutex
	writes int
	data   map[string]map[string]any // path -> payload
	srv    *httptest.Server
}

func newStatefulKVServer(t *testing.T) *statefulKVServer {
	t.Helper()
	s := &statefulKVServer{data: map[string]map[string]any{}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// KV-v2 URLs are /v1/<mount>/data/<path...>.
		path := strings.TrimPrefix(r.URL.Path, "/v1/")
		w.Header().Set("Content-Type", "application/json")

		if r.Method == http.MethodGet {
			s.mu.Lock()
			payload, ok := s.data[path]
			s.mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"errors":[]}`))
				return
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"data": payload}})
			return
		}

		body, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		_ = json.Unmarshal(body, &parsed)
		payload, _ := parsed["data"].(map[string]any)
		s.mu.Lock()
		s.data[path] = payload
		s.writes++
		s.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"version":1}}`))
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// stored returns a property of the anthropic KV path as it would be read by the
// agenity ExternalSecret.
func (s *statefulKVServer) stored(key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	payload, ok := s.data["secret/data/"+anthropicSeedSecretPath]
	if !ok {
		return "", false
	}
	v, ok := payload[key].(string)
	return v, ok
}

// refreshFixture wires a Handler with a fake OpenBao, a fake clientset holding
// the root Secret, and returns both so a test can read back what was stored.
type refreshFixture struct {
	h      *Handler
	kv     *statefulKVServer
	client *fake.Clientset
	logs   *bytes.Buffer
}

func newRefreshFixture(t *testing.T, apiKey, credsJSON string) *refreshFixture {
	t.Helper()
	kv := newStatefulKVServer(t)
	logs := &bytes.Buffer{}
	h := &Handler{log: slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	h.openbao = &openbao.Client{Addr: kv.srv.URL, Token: "test-token", HTTP: kv.srv.Client()}

	cs := fake.NewSimpleClientset(anthropicSecret(apiKey, credsJSON))
	withAnthropicSecretClient(t, cs, nil)

	// The env fallback must not leak a credential in from another test.
	t.Setenv(anthropicSeedAPIKeyEnv, "")
	t.Setenv(anthropicSeedCredentialsJSONEnv, "")

	return &refreshFixture{h: h, kv: kv, client: cs, logs: logs}
}

// storedRootSecret reads the root Secret back out of the fake clientset.
func (f *refreshFixture) storedRootSecret(t *testing.T) (string, string) {
	t.Helper()
	sec, err := f.client.CoreV1().Secrets(anthropicCredentialNamespace).
		Get(context.Background(), anthropicCredentialSecret, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read back root Secret: %v", err)
	}
	return string(sec.Data[anthropicCredentialAPIKeyKey]), string(sec.Data[anthropicCredentialJSONKey])
}

func oauthField(t *testing.T, credsJSON, key string) string {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal([]byte(credsJSON), &root); err != nil {
		t.Fatalf("stored credential is not JSON: %v", err)
	}
	o, ok := root["claudeAiOauth"].(map[string]any)
	if !ok {
		t.Fatalf("stored credential has no claudeAiOauth object")
	}
	s, _ := o[key].(string)
	return s
}

// ─── 1. the hw296 state: EXPIRED access token, live refresh token ──────────

// THE BUG, exactly as measured. Before #6317 this returned with the credential
// untouched and the agent kept 401ing. Vacuity: mutation M1 makes an expired
// credential fall through to "not due" — this test must then fail.
func TestRefresh_ExpiredCredentialIsRenewed_6317(t *testing.T) {
	f := newRefreshFixture(t, "sk-ant-oat-OLD", credWithRemaining("sk-ant-oat-OLD", "rt-ALIVE", -3*time.Hour))
	srv := newOAuthServer(t, 200, okTokenResponse("sk-ant-oat-NEW", "rt-ROTATED", 18000))

	if got := f.h.refreshAnthropicCredential(context.Background()); got != anthropicRefreshed {
		t.Fatalf("outcome = %q, want %q — an EXPIRED credential with a live refreshToken is exactly the state that must renew", got, anthropicRefreshed)
	}
	if srv.callCount() != 1 {
		t.Fatalf("oauth endpoint called %d times, want 1", srv.callCount())
	}
	_, creds := f.storedRootSecret(t)
	if got := oauthField(t, creds, "accessToken"); got != "sk-ant-oat-NEW" {
		t.Fatalf("stored accessToken = %q, want the refreshed one", got)
	}
}

// ─── 2. refresh happens BEFORE expiry, not after ───────────────────────────

// Vacuity: mutation M2 sets the lead time to 0, so a credential with 1h left is
// no longer due — this test goes red.
func TestRefresh_FiresInsideLeadTimeBeforeExpiry_6317(t *testing.T) {
	f := newRefreshFixture(t, "", credWithRemaining("acc-OLD", "rt-1", 1*time.Hour))
	srv := newOAuthServer(t, 200, okTokenResponse("acc-NEW", "rt-2", 18000))

	if got := f.h.refreshAnthropicCredential(context.Background()); got != anthropicRefreshed {
		t.Fatalf("outcome = %q, want %q — 1h remaining is inside the %v lead time", got, anthropicRefreshed, anthropicRefreshLeadTime)
	}
	if srv.callCount() != 1 {
		t.Fatalf("oauth endpoint called %d times, want 1", srv.callCount())
	}
}

// The margin is the whole point: renewing only after expiry would leave a dead
// window on every cycle. Asserted on the constant so a later edit that shrinks
// it below the measured propagation cost fails here rather than in production.
func TestRefresh_LeadTimeCoversPropagationChain_6317(t *testing.T) {
	// Worst-case propagation, measured: ESO refreshInterval 15m + kubelet
	// Secret re-projection ~1m + emptyDir re-sync 2m = 18m.
	const worstCasePropagation = 18 * time.Minute
	if anthropicRefreshLeadTime <= worstCasePropagation {
		t.Fatalf("lead time %v does not cover the %v propagation chain — a refreshed credential would reach the workspace after it expired",
			anthropicRefreshLeadTime, worstCasePropagation)
	}
	// And it must leave room for repeated attempts at the 10m reconcile cadence.
	if attempts := int(anthropicRefreshLeadTime / defaultSeedReconcileInterval); attempts < 6 {
		t.Fatalf("lead time %v gives only %d attempts at the %v cadence — a brief provider outage would burn the whole window",
			anthropicRefreshLeadTime, attempts, defaultSeedReconcileInterval)
	}
}

// ─── 3. a healthy credential is left alone (no churn) ──────────────────────

// Vacuity: mutation M3 removes the not-due short-circuit, so a credential with
// 4h left refreshes anyway — this test goes red.
func TestRefresh_HealthyCredentialIsNotChurned_6317(t *testing.T) {
	f := newRefreshFixture(t, "", credWithRemaining("acc-FRESH", "rt-1", 4*time.Hour))
	srv := newOAuthServer(t, 200, okTokenResponse("acc-NEW", "rt-2", 18000))

	if got := f.h.refreshAnthropicCredential(context.Background()); got != anthropicRefreshNotDue {
		t.Fatalf("outcome = %q, want %q", got, anthropicRefreshNotDue)
	}
	if srv.callCount() != 0 {
		t.Fatalf("oauth endpoint called %d times for a credential with 4h left — that is churn", srv.callCount())
	}
}

// ─── 4. every other field survives the rewrite ─────────────────────────────

// The Axon shell implementation rebuilds the document from the fields it greps
// for, silently dropping the rest. Vacuity: mutation M4 rebuilds the blob from
// scratch instead of editing in place — this test goes red.
func TestRefresh_PreservesUnknownCredentialFields_6317(t *testing.T) {
	f := newRefreshFixture(t, "", credWithRemaining("acc-OLD", "rt-1", -1*time.Hour))
	newOAuthServer(t, 200, okTokenResponse("acc-NEW", "rt-2", 18000))

	if got := f.h.refreshAnthropicCredential(context.Background()); got != anthropicRefreshed {
		t.Fatalf("outcome = %q, want refreshed", got)
	}
	_, creds := f.storedRootSecret(t)

	var root map[string]any
	if err := json.Unmarshal([]byte(creds), &root); err != nil {
		t.Fatalf("stored credential is not JSON: %v", err)
	}
	o := root["claudeAiOauth"].(map[string]any)

	if o["subscriptionType"] != "max" {
		t.Errorf("subscriptionType = %v, want it preserved across a refresh", o["subscriptionType"])
	}
	if o["rateLimitTier"] != "default_max_20x" {
		t.Errorf("rateLimitTier = %v, want it preserved", o["rateLimitTier"])
	}
	scopes, ok := o["scopes"].([]any)
	if !ok || len(scopes) != 2 {
		t.Errorf("scopes = %v, want the original two preserved", o["scopes"])
	}
	if _, ok := o["refreshTokenExpiresAt"]; !ok {
		t.Errorf("refreshTokenExpiresAt was DROPPED — the field that proves the refresh material is alive")
	}
}

// ─── 5. a rotated refresh token is stored, never dropped ───────────────────

// Losing a rotated refresh token bricks the credential permanently. Vacuity:
// mutation M5 keeps the old refresh token instead of the rotated one — red.
func TestRefresh_StoresRotatedRefreshToken_6317(t *testing.T) {
	f := newRefreshFixture(t, "", credWithRemaining("acc-OLD", "rt-OLD", -1*time.Hour))
	srv := newOAuthServer(t, 200, okTokenResponse("acc-NEW", "rt-ROTATED", 18000))

	if got := f.h.refreshAnthropicCredential(context.Background()); got != anthropicRefreshed {
		t.Fatalf("outcome = %q, want refreshed", got)
	}
	if sent := srv.sentRefreshToken(); sent != "rt-OLD" {
		t.Fatalf("exchange sent refresh_token %q, want the stored one", sent)
	}
	_, creds := f.storedRootSecret(t)
	if got := oauthField(t, creds, "refreshToken"); got != "rt-ROTATED" {
		t.Fatalf("stored refreshToken = %q, want the ROTATED one — keeping the spent token bricks the credential", got)
	}
}

// A provider that does NOT rotate must not have its token blanked.
func TestRefresh_CarriesForwardUnrotatedRefreshToken_6317(t *testing.T) {
	f := newRefreshFixture(t, "", credWithRemaining("acc-OLD", "rt-KEEP", -1*time.Hour))
	newOAuthServer(t, 200, func() string {
		return `{"access_token":"acc-NEW","expires_in":18000}`
	})

	if got := f.h.refreshAnthropicCredential(context.Background()); got != anthropicRefreshed {
		t.Fatalf("outcome = %q, want refreshed", got)
	}
	_, creds := f.storedRootSecret(t)
	if got := oauthField(t, creds, "refreshToken"); got != "rt-KEEP" {
		t.Fatalf("stored refreshToken = %q, want the original carried forward", got)
	}
}

// ─── 6. failures are LOUD, and never overwrite a credential ────────────────

// Vacuity: mutation M6 makes a non-2xx response return success — red.
func TestRefresh_ExchangeHTTPErrorIsLoudAndLeavesCredentialIntact_6317(t *testing.T) {
	original := credWithRemaining("acc-OLD", "rt-1", -1*time.Hour)
	f := newRefreshFixture(t, "sk-ant-oat-OLD", original)
	newOAuthServer(t, 400, func() string { return `{"error":"invalid_grant"}` })

	if got := f.h.refreshAnthropicCredential(context.Background()); got != anthropicRefreshExchangeFailed {
		t.Fatalf("outcome = %q, want %q", got, anthropicRefreshExchangeFailed)
	}
	if _, creds := f.storedRootSecret(t); creds != original {
		t.Fatalf("a FAILED refresh rewrote the credential; it must be left untouched")
	}
	if !strings.Contains(f.logs.String(), "anthropic refresh FAILED") {
		t.Fatalf("a failed refresh did not log loudly — a silent no-op is indistinguishable from success.\nlogs: %s", f.logs.String())
	}
	// The response must be rejected ON ITS STATUS, before any attempt to read a
	// token out of it. Asserting merely on "HTTP 400" would pass either way —
	// the no-access-token path names the status too — so this pins the
	// status-gate's own message, which also records that the body was WITHHELD
	// from the log rather than printed (an error body may carry credential
	// material).
	if !strings.Contains(f.logs.String(), "body withheld") {
		t.Fatalf("the 400 was not rejected on its status — it fell through to token parsing, so an error body would reach the log.\nlogs: %s", f.logs.String())
	}
	if !strings.Contains(f.logs.String(), "HTTP 400") {
		t.Fatalf("the failure log does not name the HTTP status the endpoint returned.\nlogs: %s", f.logs.String())
	}
}

// THE DOMINANT DEFECT SHAPE: a 200 that carries no token is a FAILED refresh
// wearing a success status. Vacuity: mutation M7 drops the empty-access-token
// check, so this stores an empty credential and reports success — red.
func TestRefresh_HTTP200WithoutAccessTokenIsAFailure_6317(t *testing.T) {
	original := credWithRemaining("acc-OLD", "rt-1", -1*time.Hour)
	f := newRefreshFixture(t, "", original)
	newOAuthServer(t, 200, func() string { return `{"token_type":"Bearer","expires_in":18000}` })

	if got := f.h.refreshAnthropicCredential(context.Background()); got != anthropicRefreshExchangeFailed {
		t.Fatalf("outcome = %q, want %q — a 200 with no access_token must never be stored as a credential", got, anthropicRefreshExchangeFailed)
	}
	if _, creds := f.storedRootSecret(t); creds != original {
		t.Fatalf("an empty-token 200 overwrote the stored credential")
	}
}

// A blob that CANNOT be refreshed must say so, loudly, rather than returning
// quietly and letting the credential die on schedule. Vacuity: mutation M8
// downgrades this to a silent return — red.
func TestRefresh_MissingRefreshTokenIsLoud_6317(t *testing.T) {
	f := newRefreshFixture(t, "", `{"claudeAiOauth":{"accessToken":"acc-only","expiresAt":1}}`)
	srv := newOAuthServer(t, 200, okTokenResponse("acc-NEW", "rt-2", 18000))

	if got := f.h.refreshAnthropicCredential(context.Background()); got != anthropicRefreshNoRefreshToken {
		t.Fatalf("outcome = %q, want %q", got, anthropicRefreshNoRefreshToken)
	}
	if srv.callCount() != 0 {
		t.Fatalf("exchanged with no refresh token available")
	}
	if !strings.Contains(f.logs.String(), "anthropic refresh IMPOSSIBLE") {
		t.Fatalf("an unrefreshable credential was not reported loudly.\nlogs: %s", f.logs.String())
	}
}

// A persist failure is the worst case — the refresh token is already spent —
// so it must be reported, not swallowed. Vacuity: mutation M9 ignores the root
// Secret write error — red.
func TestRefresh_RootSecretWriteFailureIsReported_6317(t *testing.T) {
	f := newRefreshFixture(t, "", credWithRemaining("acc-OLD", "rt-1", -1*time.Hour))
	newOAuthServer(t, 200, okTokenResponse("acc-NEW", "rt-2", 18000))

	f.client.PrependReactor("patch", "secrets", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
		return true, nil, fmt.Errorf("secrets is forbidden: RBAC denies patch")
	})

	if got := f.h.refreshAnthropicCredential(context.Background()); got != anthropicRefreshPersistFailed {
		t.Fatalf("outcome = %q, want %q", got, anthropicRefreshPersistFailed)
	}
	if !strings.Contains(f.logs.String(), "FAILED TO PERSIST") {
		t.Fatalf("a persist failure was not reported loudly.\nlogs: %s", f.logs.String())
	}
}

// ─── 6b. never SPEND a refresh token that cannot be stored ────────────────

// The exchange consumes the old refresh token whether or not the result can be
// kept. Attempting it with nowhere durable to write does not fail harmlessly —
// it burns the only material that could renew this credential later, turning a
// credential that merely EXPIRES into one that needs a human re-issue. So the
// exchange must not even be attempted.
//
// Vacuity: mutation M18 removes the pre-flight — the endpoint is then called
// and this test goes red on the call count.
func TestRefresh_DoesNotSpendRefreshTokenWithoutADurableStore_6317(t *testing.T) {
	logs := &bytes.Buffer{}
	h := &Handler{log: slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}

	// No in-cluster identity: the credential can only come from the env, and
	// there is no Secret to write a renewed one back to.
	withAnthropicSecretClient(t, nil, fmt.Errorf("in-cluster config unavailable"))
	t.Setenv(anthropicSeedAPIKeyEnv, "")
	t.Setenv(anthropicSeedCredentialsJSONEnv, credWithRemaining("acc-OLD", "rt-PRECIOUS", -1*time.Hour))

	srv := newOAuthServer(t, 200, okTokenResponse("acc-NEW", "rt-2", 18000))

	if got := h.refreshAnthropicCredential(context.Background()); got != anthropicRefreshNoDurableStore {
		t.Fatalf("outcome = %q, want %q", got, anthropicRefreshNoDurableStore)
	}
	if srv.callCount() != 0 {
		t.Fatalf("the refresh token was SPENT (%d exchange call(s)) with nowhere to store the result — the credential is now unrecoverable", srv.callCount())
	}
	if !strings.Contains(logs.String(), "nowhere durable to store") {
		t.Fatalf("the skip was not reported loudly.\nlogs: %s", logs.String())
	}
}

// ─── 7. apiKey is rewritten ONLY when it was a copy of the access token ────

// Measured on hw296: apiKey and accessToken were byte-identical, so a refresh
// that updated only the blob would leave a stale key behind. Vacuity: mutation
// M10 stops rewriting apiKey — red.
func TestRefresh_RewritesApiKeyWhenItMirrorsTheAccessToken_6317(t *testing.T) {
	f := newRefreshFixture(t, "acc-OLD", credWithRemaining("acc-OLD", "rt-1", -1*time.Hour))
	newOAuthServer(t, 200, okTokenResponse("acc-NEW", "rt-2", 18000))

	if got := f.h.refreshAnthropicCredential(context.Background()); got != anthropicRefreshed {
		t.Fatalf("outcome = %q, want refreshed", got)
	}
	key, _ := f.storedRootSecret(t)
	if key != "acc-NEW" {
		t.Fatalf("apiKey = %q, want the refreshed access token — it was a byte-identical copy and would otherwise go stale", key)
	}
}

// The inverse, which is what makes the rule safe: an INDEPENDENT long-lived key
// must survive. Vacuity: mutation M11 rewrites apiKey unconditionally — red.
func TestRefresh_PreservesIndependentApiKey_6317(t *testing.T) {
	f := newRefreshFixture(t, "sk-ant-api03-LONGLIVED", credWithRemaining("acc-OLD", "rt-1", -1*time.Hour))
	newOAuthServer(t, 200, okTokenResponse("acc-NEW", "rt-2", 18000))

	if got := f.h.refreshAnthropicCredential(context.Background()); got != anthropicRefreshed {
		t.Fatalf("outcome = %q, want refreshed", got)
	}
	key, _ := f.storedRootSecret(t)
	if key != "sk-ant-api03-LONGLIVED" {
		t.Fatalf("apiKey = %q — an independent long-lived key was destroyed by a 5h OAuth access token", key)
	}
}

// ─── 8. both stores receive the refreshed value ────────────────────────────

// OpenBao is what the per-Org ExternalSecret reads; the root Secret is what
// survives a catalyst-api restart. Vacuity: mutation M12 skips the OpenBao
// write — red.
func TestRefresh_WritesBothRootSecretAndOpenBao_6317(t *testing.T) {
	f := newRefreshFixture(t, "", credWithRemaining("acc-OLD", "rt-1", -1*time.Hour))
	newOAuthServer(t, 200, okTokenResponse("acc-NEW", "rt-2", 18000))

	if got := f.h.refreshAnthropicCredential(context.Background()); got != anthropicRefreshed {
		t.Fatalf("outcome = %q, want refreshed", got)
	}
	stored, ok := f.kv.stored("credentialsJson")
	if !ok {
		t.Fatalf("OpenBao never received a credentialsJson write — the per-Org ExternalSecret would keep serving the expired blob")
	}
	if got := oauthField(t, stored, "accessToken"); got != "acc-NEW" {
		t.Fatalf("OpenBao credentialsJson accessToken = %q, want the refreshed one", got)
	}
	if _, creds := f.storedRootSecret(t); oauthField(t, creds, "accessToken") != "acc-NEW" {
		t.Fatalf("root Secret did not receive the refreshed access token")
	}
}

// ─── 9. credential hygiene — no token material in any log line ─────────────

// The repo is public and these logs are shipped to operators. Vacuity: mutation
// M13 logs the access token — red.
func TestRefresh_NeverLogsTokenMaterial_6317(t *testing.T) {
	const secretAccess = "sk-ant-oat01-SUPERSECRETACCESSVALUE"
	const secretRefresh = "sk-ant-ort01-SUPERSECRETREFRESHVALUE"
	f := newRefreshFixture(t, secretAccess, credWithRemaining(secretAccess, secretRefresh, -1*time.Hour))
	newOAuthServer(t, 200, okTokenResponse("sk-ant-oat01-BRANDNEWSECRET", "sk-ant-ort01-BRANDNEWREFRESH", 18000))

	if got := f.h.refreshAnthropicCredential(context.Background()); got != anthropicRefreshed {
		t.Fatalf("outcome = %q, want refreshed", got)
	}
	logs := f.logs.String()
	for _, material := range []string{secretAccess, secretRefresh, "sk-ant-oat01-BRANDNEWSECRET", "sk-ant-ort01-BRANDNEWREFRESH"} {
		if strings.Contains(logs, material) {
			t.Fatalf("token material leaked into the logs (%q…)", material[:12])
		}
	}
	// And the fingerprints that replace it must actually be there, or this test
	// would pass on a subject that logs nothing at all.
	if !strings.Contains(logs, credFingerprint(secretAccess)) {
		t.Fatalf("no sha256 fingerprint of the old access token in the logs — the refresh is unauditable.\nlogs: %s", logs)
	}
}

// ─── 10. the reconcile pass renews BEFORE it seeds ─────────────────────────

// The ordering is the fix. Renewing after the seed leg would propagate the dead
// credential for another full interval. Vacuity: mutation M14 moves the refresh
// call after reconcileGlobalSeed — red.
func TestReconcilePass_RefreshesBeforeSeeding_6317(t *testing.T) {
	f := newRefreshFixture(t, "", credWithRemaining("acc-OLD", "rt-1", -1*time.Hour))
	newOAuthServer(t, 200, okTokenResponse("acc-NEW", "rt-2", 18000))

	f.h.runSeedReconcilePass(context.Background())

	stored, ok := f.kv.stored("credentialsJson")
	if !ok {
		t.Fatalf("the pass wrote no credentialsJson at all.\nlogs: %s", f.logs.String())
	}
	if got := oauthField(t, stored, "accessToken"); got != "acc-NEW" {
		t.Fatalf("the seed leg propagated accessToken %q — the pass seeded the EXPIRED credential it had just renewed", got)
	}

	// ORDER, asserted on WHAT THE SEED LEG SAW rather than on the final stored
	// value. The end state alone cannot see the ordering: with the refresh
	// running LAST, the seed still writes the expired blob and the refresh then
	// overwrites it — same fresh value, same green assertion.
	//
	// What differs is the producer's own verdict. Renewed first, the OpenBao
	// path is already healthy when the seed leg reads it, so that leg is a
	// silent no-op — the correct outcome, and the reason no "wrote OpenBao
	// path" line appears here. Renewed last, the seed reads the EXPIRED root
	// Secret, classifies it unusable, finds nothing stored to protect, and
	// writes it anyway under the line asserted against below.
	logs := f.logs.String()
	if strings.Contains(logs, "CANNOT authenticate an agent") {
		t.Fatalf("the seed leg ran BEFORE the refresh: it read the expired credential and propagated one that cannot authenticate.\nlogs: %s", logs)
	}
	if strings.Contains(logs, `"seed":"anthropic"`) && strings.Contains(logs, "self-heal did NOT take") {
		t.Fatalf("the anthropic leg reported an unhealed path — it ran against the expired credential instead of the renewed one.\nlogs: %s", logs)
	}
	refreshAt := strings.Index(logs, "[ANTHROPIC-REFRESH] ✅")
	if refreshAt < 0 {
		t.Fatalf("the pass did not renew the credential at all.\nlogs: %s", logs)
	}
	if seedAt := strings.Index(logs, "anthropic seed: wrote OpenBao path"); seedAt >= 0 && seedAt < refreshAt {
		t.Fatalf("the seed leg propagated BEFORE the refresh renewed (seedAt=%d refreshAt=%d).\nlogs: %s", seedAt, refreshAt, logs)
	}
}

// ─── 11. classifier agreement — a refreshed blob must read as valid ────────

// The refresh and the health predicate must not disagree about what they just
// produced, or the reconciler would re-seed over a credential it had renewed.
func TestRefresh_ProducesACredentialTheClassifierCallsValid_6317(t *testing.T) {
	f := newRefreshFixture(t, "", credWithRemaining("acc-OLD", "rt-1", -1*time.Hour))
	newOAuthServer(t, 200, okTokenResponse("acc-NEW", "rt-2", 18000))

	if got := f.h.refreshAnthropicCredential(context.Background()); got != anthropicRefreshed {
		t.Fatalf("outcome = %q, want refreshed", got)
	}
	key, creds := f.storedRootSecret(t)
	class, detail := classifyAnthropicCredential(key, creds)
	if !class.usable() {
		t.Fatalf("a freshly refreshed credential classifies as %q (%s) — the reconciler would immediately re-seed over it", class, detail)
	}
	if !anthropicStoredCredentialUsable(map[string]any{"apiKey": key, "credentialsJson": creds}) {
		t.Fatalf("the seed reconciler's health predicate rejects a credential the refresh just produced")
	}
}
