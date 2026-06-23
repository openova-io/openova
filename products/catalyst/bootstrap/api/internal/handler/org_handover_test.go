package handler

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handoverjwt"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// orgHandoverTestSetup wires a Handler for AuthOrgHandover tests: a real
// RS256 handover signer (mints the catalyst_session), the org HS256 secret
// (validates the member token), and a tenant registry with one Org console
// host so resolveOrgScope succeeds.
func orgHandoverTestSetup(t *testing.T) (*Handler, []byte) {
	t.Helper()
	dir := t.TempDir()

	privPEM, _, err := handoverjwt.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair: %v", err)
	}
	signer, err := handoverjwt.New(privPEM, "https://console.openova.io", 8*time.Hour)
	if err != nil {
		t.Fatalf("handoverjwt.New: %v", err)
	}

	reg, err := store.NewTenantRegistry(dir)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	if err := reg.Put(store.TenantRegistration{
		Host:       "console.demo.omani.homes",
		TenantID:   "7283eb4a",
		TenantKind: store.TenantKindOrg,
	}); err != nil {
		t.Fatalf("put: %v", err)
	}

	orgSecret := []byte("test-org-jwt-secret-aaaaaaaaaaaaaaaaaaaa")
	h := &Handler{
		log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		handoverSigner: signer,
		orgJWTSecret:   orgSecret,
		tenantRegistry: reg,
	}
	return h, orgSecret
}

// mintMemberToken signs an HS256 member-session token shaped like the one the
// marketplace `auth` service emits.
func mintMemberToken(t *testing.T, secret []byte, email string, exp time.Time) string {
	t.Helper()
	claims := jwt.MapClaims{
		"sub":   "fb6e3ed7-0000-0000-0000-000000000001",
		"email": email,
		"role":  "member",
		"typ":   "session",
		"iat":   time.Now().Unix(),
		"exp":   exp.Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(secret)
	if err != nil {
		t.Fatalf("sign member token: %v", err)
	}
	return signed
}

func orgHandoverRequest(token string) *http.Request {
	url := "/auth/org-handover"
	if token != "" {
		url += "?token=" + token
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("X-Forwarded-Host", "console.demo.omani.homes")
	req.Header.Set("X-Forwarded-Proto", "https")
	return req
}

// TestAuthOrgHandover_HappyPath is the core #4182/#4186 assertion: a valid
// member token on an Org console host mints an Org-scoped catalyst_session
// cookie and 302s to a CLEAN /jobs (NO token in the Location), and the
// Referrer-Policy header is set.
func TestAuthOrgHandover_HappyPath(t *testing.T) {
	h, secret := orgHandoverTestSetup(t)
	token := mintMemberToken(t, secret, "demo@openova.io", time.Now().Add(15*time.Minute))

	rec := httptest.NewRecorder()
	h.AuthOrgHandover(rec, orgHandoverRequest(token))

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if loc != "/jobs" {
		t.Fatalf("Location = %q, want clean /jobs (no token)", loc)
	}
	if strings.Contains(loc, "token") || strings.Contains(loc, "refresh") {
		t.Fatalf("Location leaks a credential: %q", loc)
	}
	if rp := rec.Header().Get("Referrer-Policy"); rp != "no-referrer" {
		t.Fatalf("Referrer-Policy = %q, want no-referrer", rp)
	}

	// Cookie set, HttpOnly + Secure + Lax, carrying an RS256 Org-scoped JWT.
	var sess *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "catalyst_session" {
			sess = c
		}
	}
	if sess == nil {
		t.Fatal("catalyst_session cookie not set")
	}
	if !sess.HttpOnly {
		t.Error("catalyst_session must be HttpOnly")
	}
	if !sess.Secure {
		t.Error("catalyst_session must be Secure")
	}
	if sess.SameSite != http.SameSiteLaxMode {
		t.Errorf("catalyst_session SameSite = %v, want Lax", sess.SameSite)
	}
	if sess.Value == "" || len(strings.Split(sess.Value, ".")) != 3 {
		t.Fatalf("catalyst_session is not a JWT: %q", sess.Value)
	}

	// The minted session must be Org-scoped (tier=org-admin, org=demo) so the
	// OrgScopeGuard confines it — never sovereign-admin.
	pub, err := h.handoverSigner.PublicRSAKey()
	if err != nil {
		t.Fatalf("PublicRSAKey: %v", err)
	}
	var claims jwt.MapClaims
	if _, err := jwt.ParseWithClaims(sess.Value, &claims, func(*jwt.Token) (any, error) {
		return pub, nil
	}, jwt.WithValidMethods([]string{"RS256"})); err != nil {
		t.Fatalf("parse minted session: %v", err)
	}
	if claims["tier"] != orgScopedTier {
		t.Errorf("tier = %v, want %s", claims["tier"], orgScopedTier)
	}
	if claims["org"] != "demo" {
		t.Errorf("org = %v, want demo", claims["org"])
	}
	if claims["email"] != "demo@openova.io" {
		t.Errorf("email = %v, want demo@openova.io", claims["email"])
	}
}

// TestAuthOrgHandover_RefusesNonOrgHost: a handoff that lands on the
// Sovereign's own front door (or any non-Org host) must be refused — never
// escalate a member token to a session on a non-Org host.
func TestAuthOrgHandover_RefusesNonOrgHost(t *testing.T) {
	h, secret := orgHandoverTestSetup(t)
	token := mintMemberToken(t, secret, "demo@openova.io", time.Now().Add(15*time.Minute))

	req := orgHandoverRequest(token)
	req.Header.Set("X-Forwarded-Host", "console.omantel.biz") // not in registry

	rec := httptest.NewRecorder()
	h.AuthOrgHandover(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 on non-Org host", rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "catalyst_session" && c.Value != "" {
			t.Fatal("must NOT mint a session on a non-Org host")
		}
	}
}

// TestAuthOrgHandover_RejectsBadToken: a token signed with the wrong secret
// (or an RS256 token) is rejected.
func TestAuthOrgHandover_RejectsBadToken(t *testing.T) {
	h, _ := orgHandoverTestSetup(t)
	bad := mintMemberToken(t, []byte("WRONG-secret-bbbbbbbbbbbbbbbbbbbbbbbbbbbb"), "demo@openova.io", time.Now().Add(15*time.Minute))

	rec := httptest.NewRecorder()
	h.AuthOrgHandover(rec, orgHandoverRequest(bad))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 on bad-signature token", rec.Code)
	}
}

// TestAuthOrgHandover_RejectsExpiredToken.
func TestAuthOrgHandover_RejectsExpiredToken(t *testing.T) {
	h, secret := orgHandoverTestSetup(t)
	expired := mintMemberToken(t, secret, "demo@openova.io", time.Now().Add(-1*time.Minute))

	rec := httptest.NewRecorder()
	h.AuthOrgHandover(rec, orgHandoverRequest(expired))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 on expired token", rec.Code)
	}
}

// TestAuthOrgHandover_MissingToken returns 401 (no session).
func TestAuthOrgHandover_MissingToken(t *testing.T) {
	h, _ := orgHandoverTestSetup(t)
	rec := httptest.NewRecorder()
	h.AuthOrgHandover(rec, orgHandoverRequest(""))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 on missing token", rec.Code)
	}
}

// TestAuthOrgHandover_NoOrgSecret returns 401 when CATALYST_ORG_JWT_SECRET is
// unwired (cannot validate any member token).
func TestAuthOrgHandover_NoOrgSecret(t *testing.T) {
	h, secret := orgHandoverTestSetup(t)
	h.orgJWTSecret = nil
	token := mintMemberToken(t, secret, "demo@openova.io", time.Now().Add(15*time.Minute))

	rec := httptest.NewRecorder()
	h.AuthOrgHandover(rec, orgHandoverRequest(token))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 when org secret unwired", rec.Code)
	}
}
