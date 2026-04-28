package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/openova-io/openova/core/services/domain/store"
	"github.com/openova-io/openova/core/services/shared/middleware"
)

// These tests cover the IDOR fix for DELETE /domain/domains/{id} (issue #79).
// They exercise the authz helpers directly against a fake tenant service so
// the tests can run without MongoDB, RedPanda, or a real JWT middleware pass.

// fakeTenantServer returns an httptest.Server that accepts GET /tenant/orgs/{id}
// and maps each id to a pre-set status code. It is the stand-in for the real
// tenant service, which bakes membership into its GetOrg handler.
func fakeTenantServer(t *testing.T, statusByTenant map[string]int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/tenant/orgs/") {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/tenant/orgs/")
		if code, ok := statusByTenant[id]; ok {
			w.WriteHeader(code)
			json.NewEncoder(w).Encode(map[string]string{"id": id})
			return
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// ctxWithClaims returns a context carrying the given JWT claims, mirroring
// what middleware.JWTAuth produces on a live request. Used so the handler
// helpers under test can read "sub" and "role" without a real token.
func ctxWithClaims(userID, role string) context.Context {
	claims := jwt.MapClaims{"sub": userID, "role": role}
	// The jwt middleware stores claims under its private key, so run the
	// middleware directly against a recorder to lift the context out.
	handler := middleware.JWTAuth([]byte("test-secret"))(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {},
	))
	_ = handler
	// Easier path: produce a signed token, then parse it via the middleware
	// to reuse the canonical context injection. But that requires a full HTTP
	// request and response, which buys little. Instead we use a tiny
	// round-tripper helper.
	req := httptest.NewRequest(http.MethodGet, "/noop", nil)
	req.Header.Set("Authorization", "Bearer "+signTestJWT(claims))
	rec := httptest.NewRecorder()
	var captured context.Context
	handler = middleware.JWTAuth([]byte("test-secret"))(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			captured = r.Context()
			w.WriteHeader(http.StatusOK)
		},
	))
	handler.ServeHTTP(rec, req)
	return captured
}

func signTestJWT(claims jwt.MapClaims) string {
	claims["exp"] = time.Now().Add(time.Hour).Unix()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := tok.SignedString([]byte("test-secret"))
	if err != nil {
		panic(err)
	}
	return s
}

// TestAuthorizeTenantAccess_Superadmin: a superadmin must always be authorised
// regardless of membership.
func TestAuthorizeTenantAccess_Superadmin(t *testing.T) {
	tenantSrv := fakeTenantServer(t, map[string]int{
		"tenant-B": http.StatusForbidden, // superadmin bypass should make this moot
	})
	h := &Handler{TenantURL: tenantSrv.URL, TenantClient: http.DefaultClient}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/domain/domains/x", nil)
	req = req.WithContext(ctxWithClaims("superadmin-user", "superadmin"))
	if !h.authorizeTenantAccess(rec, req, "tenant-B") {
		t.Fatalf("superadmin should be authorised, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestAuthorizeTenantAccess_Member: a member of the tenant must be authorised.
func TestAuthorizeTenantAccess_Member(t *testing.T) {
	tenantSrv := fakeTenantServer(t, map[string]int{
		"tenant-A": http.StatusOK,
	})
	h := &Handler{TenantURL: tenantSrv.URL, TenantClient: http.DefaultClient}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/domain/domains/x", nil)
	req.Header.Set("Authorization", "Bearer fake-token")
	req = req.WithContext(ctxWithClaims("user-A", "member"))
	if !h.authorizeTenantAccess(rec, req, "tenant-A") {
		t.Fatalf("member should be authorised, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestAuthorizeTenantAccess_NonMember: a user not in the tenant must get 403.
// This is the core IDOR assertion.
func TestAuthorizeTenantAccess_NonMember(t *testing.T) {
	tenantSrv := fakeTenantServer(t, map[string]int{
		"tenant-B": http.StatusForbidden,
	})
	h := &Handler{TenantURL: tenantSrv.URL, TenantClient: http.DefaultClient}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/domain/domains/x", nil)
	req.Header.Set("Authorization", "Bearer fake-token")
	req = req.WithContext(ctxWithClaims("user-A", "member"))
	if h.authorizeTenantAccess(rec, req, "tenant-B") {
		t.Fatalf("non-member must be rejected")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestAuthorizeTenantAccess_NoIdentity: calls without a JWT-derived user_id
// must be rejected with 401 — we must never fall through to a default-allow.
func TestAuthorizeTenantAccess_NoIdentity(t *testing.T) {
	h := &Handler{TenantURL: "http://never-called", TenantClient: http.DefaultClient}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/domain/domains/x", nil)
	// No JWT middleware in path — context has no claims.
	if h.authorizeTenantAccess(rec, req, "tenant-A") {
		t.Fatalf("missing identity must be rejected")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestAuthorizeTenantAccess_TenantUnconfigured: if TenantURL is empty we must
// fail closed (500) rather than fall through to allow.
func TestAuthorizeTenantAccess_TenantUnconfigured(t *testing.T) {
	h := &Handler{TenantURL: "", TenantClient: http.DefaultClient}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/domain/domains/x", nil)
	req.Header.Set("Authorization", "Bearer fake-token")
	req = req.WithContext(ctxWithClaims("user-A", "member"))
	if h.authorizeTenantAccess(rec, req, "tenant-A") {
		t.Fatalf("unconfigured tenant URL must fail closed")
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

// TestAuthorizeTenantAccess_ForwardsAuthHeader: the helper must forward the
// caller's Authorization header so the tenant service can check membership
// against the same JWT.
func TestAuthorizeTenantAccess_ForwardsAuthHeader(t *testing.T) {
	var received string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := &Handler{TenantURL: srv.URL, TenantClient: http.DefaultClient}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/domain/domains/x", nil)
	req.Header.Set("Authorization", "Bearer the-token")
	req = req.WithContext(ctxWithClaims("user-A", "member"))
	if !h.authorizeTenantAccess(rec, req, "tenant-A") {
		t.Fatalf("call should succeed, got %d: %s", rec.Code, rec.Body.String())
	}
	if received != "Bearer the-token" {
		t.Fatalf("expected forwarded Authorization header, got %q", received)
	}
}

// Compile-time check that *store.Domain is a reasonable shape the helpers
// touch. We don't spin up MongoDB, but referencing the type makes this file a
// legitimate handler_test.go compilation unit.
var _ = store.Domain{}
