package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// reqWithOwnerAndClaims builds a request carrying the X-User-Email header
// (the session-email auth.RequireSession injects) plus an optional
// auth.Claims in context (the tier/role transport).
func reqWithOwnerAndClaims(sessionEmail string, claims *auth.Claims) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/deployments/dep-1/reconcilers", nil)
	if sessionEmail != "" {
		r.Header.Set("X-User-Email", sessionEmail)
	}
	if claims != nil {
		r = r.WithContext(context.WithValue(r.Context(), auth.ClaimsKey, claims))
	}
	return r
}

func ownedDep(owner string) *Deployment {
	return &Deployment{ID: "dep-1", OwnerEmail: owner}
}

// TestCheckOwnership_ChrootCoAdminBypass — #4193. On a chroot Sovereign
// (SOVEREIGN_FQDN set) a privileged-tier sovereign-admin who is NOT the
// deployment's OwnerEmail must PASS the ownership gate so the #3996
// reconciler drill-in (logs / reconcile / suspend / resume) stops 404ing.
func TestCheckOwnership_ChrootCoAdminBypass(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "omantel.biz")
	h := &Handler{}

	cases := []struct {
		name   string
		claims *auth.Claims
		want   bool
	}{
		{
			name:   "sovereign-admin tier (the uat215 live case)",
			claims: &auth.Claims{Email: "uat215@omani.works", Tier: "sovereign-admin"},
			want:   true,
		},
		{
			name:   "owner tier (PIN-verify stamp)",
			claims: &auth.Claims{Email: "co-admin@omani.works", Tier: "owner"},
			want:   true,
		},
		{
			name:   "admin tier",
			claims: &auth.Claims{Email: "admin2@omani.works", Tier: "admin"},
			want:   true,
		},
		{
			name: "privileged realm role, empty tier",
			claims: &auth.Claims{
				Email:       "role-admin@omani.works",
				RealmAccess: auth.RealmAccess{Roles: []string{"catalyst-admin"}},
			},
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := reqWithOwnerAndClaims(tc.claims.Email, tc.claims)
			got := h.checkOwnership(w, r, ownedDep("emrah.baysal@openova.io"))
			if got != tc.want {
				t.Fatalf("checkOwnership = %v, want %v (status %d)", got, tc.want, w.Code)
			}
			if got && w.Code != http.StatusOK {
				t.Fatalf("pass path wrote a body (status %d); expected no write", w.Code)
			}
		})
	}
}

// TestCheckOwnership_ChrootNonPrivilegedStillRejected — a non-privileged
// session (e.g. an Org-scoped customer) on the chroot is STILL rejected
// when it isn't the owner, so the bypass doesn't open the gate to anyone.
func TestCheckOwnership_ChrootNonPrivilegedStillRejected(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "omantel.biz")
	h := &Handler{}
	w := httptest.NewRecorder()
	claims := &auth.Claims{Email: "customer@some-org.omani.homes", Tier: "org-admin"}
	r := reqWithOwnerAndClaims(claims.Email, claims)

	if h.checkOwnership(w, r, ownedDep("emrah.baysal@openova.io")) {
		t.Fatal("non-privileged non-owner should be rejected on the chroot")
	}
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// TestCheckOwnership_MothershipUnchanged — with SOVEREIGN_FQDN UNSET
// (mothership, multi-tenant) the bypass MUST NOT fire: a non-owner
// session is rejected even with an admin tier, preserving the #689
// cross-tenant guard.
func TestCheckOwnership_MothershipUnchanged(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "")
	h := &Handler{}

	// Non-owner admin → rejected (no chroot bypass on the mother).
	w := httptest.NewRecorder()
	claims := &auth.Claims{Email: "other-operator@example.com", Tier: "admin"}
	r := reqWithOwnerAndClaims(claims.Email, claims)
	if h.checkOwnership(w, r, ownedDep("creator@example.com")) {
		t.Fatal("mothership: a non-owner admin must NOT bypass the ownership gate")
	}
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}

	// The genuine owner still passes on the mother.
	w2 := httptest.NewRecorder()
	oc := &auth.Claims{Email: "creator@example.com", Tier: "owner"}
	r2 := reqWithOwnerAndClaims(oc.Email, oc)
	if !h.checkOwnership(w2, r2, ownedDep("creator@example.com")) {
		t.Fatal("mothership: the owner must pass")
	}
}

// TestCheckOwnership_LegacyEmptyOwnerSkips — an empty OwnerEmail (legacy
// pre-#689 record) still skips the check on both mother and chroot.
func TestCheckOwnership_LegacyEmptyOwnerSkips(t *testing.T) {
	h := &Handler{}
	w := httptest.NewRecorder()
	r := reqWithOwnerAndClaims("anyone@example.com", nil)
	if !h.checkOwnership(w, r, ownedDep("")) {
		t.Fatal("legacy empty-owner record must skip the ownership check")
	}
}
