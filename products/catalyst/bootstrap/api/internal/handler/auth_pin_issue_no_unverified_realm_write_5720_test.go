// auth_pin_issue_no_unverified_realm_write_5720_test.go — regression guard
// for issue #5720.
//
// # The defect
//
// `POST /api/v1/auth/pin/issue` is UNAUTHENTICATED. Until 2026-08-06 it
// called `kc.EnsureUser(email, "openova-users")` as step 2 — before the
// PIN was generated, before it was mailed, and before anything was
// verified. So typing any address into the console login form was
// sufficient to write a permanent, enabled, emailVerified principal into
// the Sovereign realm. The per-email 60s rate limiter did not constrain
// it, because a caller who varies the address never repeats a key.
//
// Found on hw292 while re-walking UAT row 41, which asserts the sovereign
// realm holds exactly the seeded owner principal. It held two: the owner
// `emrah.baysal@openova.io` and `emrha.baysal@openova.io` — a
// transposition typo that became a permanent realm user because somebody
// mistyped it at the login form once, a day after the realm was seeded.
//
// # Why this file drives a fake Keycloak SERVER, not a fake client
//
// The assertion that matters is "no realm user exists after an unverified
// issue call". A stub that records EnsureUser CALLS would assert on the
// call, not on the effect, and would be satisfied by any refactor that
// reaches Keycloak by another route. So these tests wire the REAL
// *keycloak.Client (the production type, exercising its real
// findUserByEmail / createUser / addUserToGroup HTTP sequence) against an
// httptest server that keeps an actual realm-user table. The tests then
// QUERY THAT TABLE. If a principal is created by any path, the table
// shows it.
//
// # Vacuity
//
// Three of these tests fail RED on the pre-fix tree (they observe the
// injected principal) and pass GREEN after. Two are CONTROLS that pass on
// BOTH trees, so the suite cannot be satisfied by disabling the realm
// write wholesale: TestPinVerify_LegitimateOwnerSignsInEndToEnd_5720
// requires that a real sign-in still provisions the principal and still
// mints an admin session. A "fix" that stopped the injection by breaking
// login turns that control red.
package handler

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handoverjwt"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/keycloak"
)

// ─── fake Keycloak realm (server-side state, queried by the assertions) ───────

// fakeRealmUser is one principal in the fake realm's user table — the
// same fields the live Keycloak Users list renders in the UAT row 41
// walk.
type fakeRealmUser struct {
	ID            string
	Email         string
	Enabled       bool
	EmailVerified bool
	GroupPaths    []string
}

// fakeRealm is a minimal Keycloak Admin REST implementation covering the
// exact endpoint set *keycloak.Client touches for EnsureUser +
// UserGroupPaths. It holds real state so a test can ask "who exists in
// this realm right now?" rather than "was a method called?".
type fakeRealm struct {
	mu     sync.Mutex
	realm  string
	users  map[string]*fakeRealmUser // keyed by lowercased email
	groups map[string]string         // group name → id
	nextID int

	srv *httptest.Server
}

func newFakeRealm(t *testing.T, realm string) *fakeRealm {
	t.Helper()
	f := &fakeRealm{
		realm:  realm,
		users:  map[string]*fakeRealmUser{},
		groups: map[string]string{},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.route))
	t.Cleanup(f.srv.Close)
	return f
}

// client returns the PRODUCTION Keycloak client pointed at this fake
// realm. Everything the handler does goes through the real
// EnsureUser/UserGroupPaths implementations and real HTTP.
func (f *fakeRealm) client() *keycloak.Client {
	return keycloak.NewWithHTTP(f.srv.URL, f.realm, "catalyst-api-server",
		"not-a-real-secret", f.srv.Client())
}

// seedUser pre-creates a principal, as the realm import / handover seed
// does for the Sovereign owner.
func (f *fakeRealm) seedUser(email string, groupPaths ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	f.users[strings.ToLower(email)] = &fakeRealmUser{
		ID:            fmt.Sprintf("seeded-%d", f.nextID),
		Email:         email,
		Enabled:       true,
		EmailVerified: true,
		GroupPaths:    append([]string{}, groupPaths...),
	}
}

// emails returns the sorted set of principal emails currently in the
// realm — this is the value the assertions read.
func (f *fakeRealm) emails() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.users))
	for _, u := range f.users {
		out = append(out, u.Email)
	}
	sort.Strings(out)
	return out
}

func (f *fakeRealm) user(email string) *fakeRealmUser {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.users[strings.ToLower(email)]
}

func (f *fakeRealm) route(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path

	// Service-account token endpoint.
	if strings.HasSuffix(p, "/protocol/openid-connect/token") {
		writeJSONRaw(w, http.StatusOK, map[string]any{
			"access_token": "fake-sa-token",
			"expires_in":   300,
		})
		return
	}

	adminBase := "/admin/realms/" + f.realm
	if !strings.HasPrefix(p, adminBase) {
		http.NotFound(w, r)
		return
	}
	rest := strings.TrimPrefix(p, adminBase)

	switch {
	// GET/POST /users
	case rest == "/users" && r.Method == http.MethodGet:
		f.handleFindUser(w, r)
	case rest == "/users" && r.Method == http.MethodPost:
		f.handleCreateUser(w, r)

	// GET /users/{id}/groups   |   PUT /users/{id}/groups/{gid}
	case strings.HasPrefix(rest, "/users/") && strings.HasSuffix(rest, "/groups") && r.Method == http.MethodGet:
		f.handleUserGroups(w, strings.TrimSuffix(strings.TrimPrefix(rest, "/users/"), "/groups"))
	case strings.HasPrefix(rest, "/users/") && strings.Contains(rest, "/groups/") && r.Method == http.MethodPut:
		seg := strings.SplitN(strings.TrimPrefix(rest, "/users/"), "/groups/", 2)
		f.handleJoinGroup(w, seg[0], seg[1])

	// GET/POST /groups
	case rest == "/groups" && r.Method == http.MethodGet:
		f.handleFindGroup(w, r)
	case rest == "/groups" && r.Method == http.MethodPost:
		f.handleCreateGroup(w, r)

	default:
		http.NotFound(w, r)
	}
}

func (f *fakeRealm) handleFindUser(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(r.URL.Query().Get("email"))
	f.mu.Lock()
	defer f.mu.Unlock()
	if u, ok := f.users[email]; ok {
		writeJSONRaw(w, http.StatusOK, []map[string]any{{
			"id": u.ID, "email": u.Email, "username": u.Email,
			"enabled": u.Enabled, "emailVerified": u.EmailVerified,
		}})
		return
	}
	writeJSONRaw(w, http.StatusOK, []map[string]any{})
}

func (f *fakeRealm) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Email         string `json:"email"`
		Enabled       bool   `json:"enabled"`
		EmailVerified bool   `json:"emailVerified"`
	}
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &payload)

	f.mu.Lock()
	defer f.mu.Unlock()
	key := strings.ToLower(payload.Email)
	if _, exists := f.users[key]; exists {
		w.WriteHeader(http.StatusConflict)
		return
	}
	f.nextID++
	id := fmt.Sprintf("user-%d", f.nextID)
	f.users[key] = &fakeRealmUser{
		ID:            id,
		Email:         payload.Email,
		Enabled:       payload.Enabled,
		EmailVerified: payload.EmailVerified,
	}
	w.Header().Set("Location", f.srv.URL+"/admin/realms/"+f.realm+"/users/"+id)
	w.WriteHeader(http.StatusCreated)
}

func (f *fakeRealm) handleUserGroups(w http.ResponseWriter, userID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []map[string]any{}
	for _, u := range f.users {
		if u.ID != userID {
			continue
		}
		for _, gp := range u.GroupPaths {
			out = append(out, map[string]any{
				"id": gp, "name": strings.TrimPrefix(gp, "/"), "path": gp,
			})
		}
	}
	writeJSONRaw(w, http.StatusOK, out)
}

func (f *fakeRealm) handleJoinGroup(w http.ResponseWriter, userID, groupID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := ""
	for n, id := range f.groups {
		if id == groupID {
			name = n
			break
		}
	}
	for _, u := range f.users {
		if u.ID != userID {
			continue
		}
		path := "/" + name
		for _, existing := range u.GroupPaths {
			if existing == path {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		u.GroupPaths = append(u.GroupPaths, path)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeRealm) handleFindGroup(w http.ResponseWriter, r *http.Request) {
	name, _ := url.QueryUnescape(r.URL.Query().Get("search"))
	f.mu.Lock()
	defer f.mu.Unlock()
	if id, ok := f.groups[name]; ok {
		writeJSONRaw(w, http.StatusOK, []map[string]any{{"id": id, "name": name, "path": "/" + name}})
		return
	}
	writeJSONRaw(w, http.StatusOK, []map[string]any{})
}

func (f *fakeRealm) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Name string `json:"name"`
	}
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &payload)

	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	id := fmt.Sprintf("group-%d", f.nextID)
	f.groups[payload.Name] = id
	w.Header().Set("Location", f.srv.URL+"/admin/realms/"+f.realm+"/groups/"+id)
	w.WriteHeader(http.StatusCreated)
}

func writeJSONRaw(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// ─── handler wired to the fake realm ─────────────────────────────────────────

// newHandlerOnFakeRealm builds a Handler whose openova-realm client AND
// sovereign-realm client are the production *keycloak.Client talking to
// `realm`. Both are wired because pin/issue provisions via openovaKC
// while pin/verify resolves authority via kc — a realm write by EITHER
// path lands in the same observable table.
func newHandlerOnFakeRealm(t *testing.T, realm *fakeRealm) *Handler {
	t.Helper()
	dir := t.TempDir()

	privPEM, pubJWK, err := handoverjwt.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair: %v", err)
	}
	keyPath := filepath.Join(dir, "handover.pem")
	pubPath := filepath.Join(dir, "public.jwk")
	if err := os.WriteFile(keyPath, privPEM, 0o600); err != nil {
		t.Fatalf("write privPEM: %v", err)
	}
	if err := os.WriteFile(pubPath, pubJWK, 0o644); err != nil {
		t.Fatalf("write pubJWK: %v", err)
	}
	signer, err := handoverjwt.LoadOrGenerate(keyPath, pubPath, "https://console.openova.io", 5*time.Minute)
	if err != nil {
		t.Fatalf("LoadOrGenerate: %v", err)
	}

	kc := realm.client()
	return &Handler{
		log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		handoverSigner: signer,
		openovaKC:      kc,
		kc:             kc,
		pinStore:       newPinStoreNoSweeper(),
	}
}

// postPinIssue drives the production HandlePinIssue exactly as the router
// mounts it (cmd/api/main.go: r.Post("/api/v1/auth/pin/issue", h.HandlePinIssue)).
func postPinIssue(t *testing.T, h *Handler, email string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/pin/issue",
		strings.NewReader(`{"email":`+pin5720JSONQuote(email)+`}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandlePinIssue(w, req)
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w.Code, body
}

func postPinVerify(t *testing.T, h *Handler, email, pin, requestID string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/pin/verify",
		strings.NewReader(`{"email":`+pin5720JSONQuote(email)+`,"pin":`+pin5720JSONQuote(pin)+
			`,"requestId":`+pin5720JSONQuote(requestID)+`}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandlePinVerify(w, req)
	return w.Result()
}

func pin5720JSONQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// ─── RED-on-pre-fix guards ───────────────────────────────────────────────────

// TestPinIssue_NoRealmUserForUnverifiedEmail_5720 is the primary guard.
//
// One unauthenticated POST /pin/issue for an address nobody has verified
// must leave the identity store untouched. The assertion reads the realm
// user table — not the HTTP status, not a call counter — because the
// defect's observable effect IS the principal.
//
// Pre-fix output (EnsureUser in HandlePinIssue):
//
//	realm users after unverified pin/issue: got [drive-by@t99.omani.works] want none
func TestPinIssue_NoRealmUserForUnverifiedEmail_5720(t *testing.T) {
	realm := newFakeRealm(t, "sovereign")
	h := newHandlerOnFakeRealm(t, realm)
	defer withSendPinEmail(noopSendPin)()

	const victim = "drive-by@t99.omani.works"

	code, body := postPinIssue(t, h, victim)
	if code != http.StatusOK {
		t.Fatalf("pin/issue status: got %d want 200 (body: %v)", code, body)
	}

	// THE assertion: query the realm.
	if got := realm.emails(); len(got) != 0 {
		t.Errorf("realm users after unverified pin/issue: got %v want none\n"+
			"  #5720: pin/issue is unauthenticated — it must never write a principal.\n"+
			"  The principal may only appear after a correct PIN proves mailbox control.",
			got)
	}
}

// TestPinIssue_UnauthenticatedFloodCreatesNoPrincipals_5720 models the
// actual abuse: a caller who VARIES the address. The per-email 60s
// rate limiter is keyed on the email, so it never fires here — which is
// exactly why the pre-fix write was unbounded.
//
// Pre-fix this leaves 8 enabled principals in the realm.
func TestPinIssue_UnauthenticatedFloodCreatesNoPrincipals_5720(t *testing.T) {
	realm := newFakeRealm(t, "sovereign")
	h := newHandlerOnFakeRealm(t, realm)
	defer withSendPinEmail(noopSendPin)()

	for i := 0; i < 8; i++ {
		email := fmt.Sprintf("flood-%d@t99.omani.works", i)
		if code, body := postPinIssue(t, h, email); code != http.StatusOK {
			t.Fatalf("pin/issue[%d] status: got %d want 200 (body: %v)", i, code, body)
		}
	}

	if got := realm.emails(); len(got) != 0 {
		t.Errorf("realm users after 8 varied unverified pin/issue calls: got %d %v want 0\n"+
			"  The 60s limiter is keyed per-email, so varying the address bypasses it entirely.",
			len(got), got)
	}
}

// TestPinVerify_WrongPINNeverCreatesPrincipal_5720 closes the other half:
// a caller who issues AND answers, but answers wrong, has proven nothing
// and must leave no trace. Runs the attempt budget to lockout.
//
// Pre-fix the principal was already written at issue-time, so it survives
// every wrong answer.
func TestPinVerify_WrongPINNeverCreatesPrincipal_5720(t *testing.T) {
	realm := newFakeRealm(t, "sovereign")
	h := newHandlerOnFakeRealm(t, realm)
	defer withSendPinEmail(noopSendPin)()

	const victim = "wrong-pin@t99.omani.works"
	code, body := postPinIssue(t, h, victim)
	if code != http.StatusOK {
		t.Fatalf("pin/issue status: got %d want 200 (body: %v)", code, body)
	}
	requestID, _ := body["requestId"].(string)

	for i := 0; i < pinMaxAttempts; i++ {
		resp := postPinVerify(t, h, victim, "000000", requestID)
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("attempt %d: wrong PIN unexpectedly accepted", i+1)
		}
	}

	if got := realm.emails(); len(got) != 0 {
		t.Errorf("realm users after issue + %d wrong PINs: got %v want none\n"+
			"  A caller who never produced the PIN never proved mailbox control.",
			pinMaxAttempts, got)
	}
}

// ─── CONTROLS — must pass on BOTH the pre-fix and post-fix trees ─────────────

// TestPinVerify_LegitimateOwnerSignsInEndToEnd_5720 is the control that
// makes the guards above un-cheatable. Deleting the EnsureUser call
// outright, or gating login behind something stricter, turns this red.
//
// It asserts the FULL sign-in outcome for the seeded Sovereign owner:
//
//  1. pin/issue → 200 with a requestId
//  2. pin/verify with the correct PIN → 200
//  3. a catalyst_session cookie is set, HttpOnly + SameSite=Lax
//  4. the session carries tier=admin + catalyst-admin, derived from the
//     owner's live /sovereign-admins membership (NOT from a constant)
//  5. the owner is provisioned into openova-users in the realm
//
// Passes on both trees: pre-fix the principal is created at issue,
// post-fix at verify; either way it exists once sign-in completes.
func TestPinVerify_LegitimateOwnerSignsInEndToEnd_5720(t *testing.T) {
	const owner = "emrah.baysal@t99.omani.works"
	t.Setenv("OPERATOR_EMAIL", owner)

	realm := newFakeRealm(t, "sovereign")
	// The realm import / handover seeds the owner into /sovereign-admins.
	realm.seedUser(owner, "/sovereign-admins")

	h := newHandlerOnFakeRealm(t, realm)

	// Capture the PIN off the send seam — the same seam production uses.
	var issued string
	defer withSendPinEmail(func(_, pin string) error {
		issued = pin
		return nil
	})()

	code, body := postPinIssue(t, h, owner)
	if code != http.StatusOK {
		t.Fatalf("owner pin/issue: got %d want 200 (body: %v)", code, body)
	}
	requestID, _ := body["requestId"].(string)
	if requestID == "" {
		t.Fatal("owner pin/issue: empty requestId")
	}
	if issued == "" {
		t.Fatal("owner pin/issue: no PIN reached the send seam")
	}

	resp := postPinVerify(t, h, owner, issued, requestID)
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("owner pin/verify: got %d want 200 (body: %s)", resp.StatusCode, b)
	}

	cookie := findCookie(resp.Cookies(), "catalyst_session")
	if cookie == nil || cookie.Value == "" {
		t.Fatal("owner sign-in did not set catalyst_session — login is broken")
	}
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("catalyst_session hygiene: HttpOnly=%v SameSite=%v", cookie.HttpOnly, cookie.SameSite)
	}

	claims := decodeSessionClaims(t, cookie.Value)
	if claims.Tier != pinSessionAdminTier {
		t.Errorf("owner tier: got %q want %q (group-derived admin)", claims.Tier, pinSessionAdminTier)
	}
	if !claims.HasRealmRole("catalyst-admin") {
		t.Errorf("owner realm roles: got %v want catalyst-admin", claims.RealmAccess.Roles)
	}
	if !policyModeCallerAuthorized(&claims) {
		t.Error("owner session must pass the sovereign-admin gate")
	}

	// The principal exists and carries the openova-users membership the
	// PIN flow is responsible for.
	u := realm.user(owner)
	if u == nil {
		t.Fatalf("owner principal missing from realm after sign-in (have: %v)", realm.emails())
	}
	if !hasPath(u.GroupPaths, "/openova-users") {
		t.Errorf("owner group paths: got %v want to include /openova-users", u.GroupPaths)
	}
	// And still no drive-by principals.
	if got := realm.emails(); len(got) != 1 {
		t.Errorf("realm users after one legitimate sign-in: got %v want exactly [%s]", got, owner)
	}
}

// TestPinIssue_ResponseShapeIdenticalForKnownAndUnknownEmail_5720 pins the
// enumeration-oracle half.
//
// HONEST SCOPE: this is a FORWARD-LOOKING invariant, not a reproduction of
// #5720 — it passes on the pre-fix tree too, because pre-fix pin/issue
// answered 200 for both cases (it created the missing principal rather
// than reporting its absence). It is included because the natural
// "cleanup" after moving EnsureUser is to add a `user not found → 404`
// or `known → different code` shortcut to pin/issue, which would convert
// this endpoint into an account-existence oracle for an unauthenticated
// caller. This test makes that regression fail loudly.
//
// The assertion is on the caller-visible response: status + every field
// except the deliberately-unpredictable requestId.
func TestPinIssue_ResponseShapeIdenticalForKnownAndUnknownEmail_5720(t *testing.T) {
	realm := newFakeRealm(t, "sovereign")
	realm.seedUser("known@t99.omani.works", "/openova-users")
	h := newHandlerOnFakeRealm(t, realm)
	defer withSendPinEmail(noopSendPin)()

	knownCode, knownBody := postPinIssue(t, h, "known@t99.omani.works")
	unknownCode, unknownBody := postPinIssue(t, h, "never-seen@t99.omani.works")

	if knownCode != unknownCode {
		t.Errorf("status differs by account existence: known=%d unknown=%d — account-enumeration oracle",
			knownCode, unknownCode)
	}
	delete(knownBody, "requestId")
	delete(unknownBody, "requestId")
	kj, _ := json.Marshal(knownBody)
	uj, _ := json.Marshal(unknownBody)
	if string(kj) != string(uj) {
		t.Errorf("response body differs by account existence — account-enumeration oracle:\n  known:   %s\n  unknown: %s",
			kj, uj)
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func decodeSessionClaims(t *testing.T, rawJWT string) auth.Claims {
	t.Helper()
	parts := strings.Split(rawJWT, ".")
	if len(parts) != 3 {
		t.Fatalf("session cookie: got %d JWT parts want 3", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode JWT payload: %v", err)
	}
	var claims auth.Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("unmarshal claims: %v", err)
	}
	return claims
}

func hasPath(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}
