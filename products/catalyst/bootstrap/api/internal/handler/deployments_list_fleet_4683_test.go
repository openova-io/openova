// Tests for the sovereign-admin fleet visibility on the deployments
// list endpoint (issue #4683).
//
// Contract: a session holding the catalyst-owner realm role (or the
// flattened tier=owner claim) lists EVERY deployment regardless of
// OwnerEmail — including legacy rows with empty OwnerEmail — and may
// use ?owner= as a genuine filter over any owner. Owner-scoping stays
// the boundary for every other session shape (issues #747/#748
// unchanged), and an Org-scoped customer session (tier=org-admin,
// #4110) is never fleet-visible even when realm-config drift leaks a
// privileged role onto its token.
package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// fleetListRequest builds a GET /api/v1/deployments request with the
// given Claims injected the same way auth.RequireSession does (context
// + X-User-Email header).
func fleetListRequest(claims *auth.Claims, query string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/deployments"+query, nil)
	if claims != nil {
		r = r.WithContext(context.WithValue(r.Context(), auth.ClaimsKey, claims))
		if claims.Email != "" {
			r.Header.Set("X-User-Email", claims.Email)
		}
	}
	return r
}

// fleetListRows runs ListDeployments and decodes the row set keyed by id.
func fleetListRows(t *testing.T, h *Handler, r *http.Request) map[string]map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	h.ListDeployments(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200; got %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Deployments []map[string]any `json:"deployments"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	rows := make(map[string]map[string]any, len(body.Deployments))
	for _, d := range body.Deployments {
		id, _ := d["id"].(string)
		rows[id] = d
	}
	return rows
}

// newFleetTestHandler seeds three owners plus one legacy (empty
// OwnerEmail) row — the hw206 shape where the live prov was owned by a
// different identity than the signed-in operator.
func newFleetTestHandler() *Handler {
	h := &Handler{log: slog.Default()}
	newDepListEntry(h, "dep-alice", "alice@example.com", "ready", false)
	newDepListEntry(h, "dep-bob", "bob@example.com", "phase1-watching", false)
	newDepListEntry(h, "dep-legacy", "", "ready", false)
	return h
}

// TestListDeployments_CatalystOwnerRoleSeesAll proves the #4683 fix:
// a catalyst-owner session whose email owns NONE of the deployments
// still lists all of them (fleet visibility is role-driven, not
// email-driven), legacy empty-owner rows included.
func TestListDeployments_CatalystOwnerRoleSeesAll(t *testing.T) {
	h := newFleetTestHandler()
	claims := &auth.Claims{
		Email:       "operator@example.com",
		RealmAccess: auth.RealmAccess{Roles: []string{"catalyst-owner"}},
	}

	rows := fleetListRows(t, h, fleetListRequest(claims, ""))
	if len(rows) != 3 {
		t.Fatalf("catalyst-owner must see all 3 deployments; got %d (%+v)", len(rows), rows)
	}
	for _, id := range []string{"dep-alice", "dep-bob", "dep-legacy"} {
		if _, ok := rows[id]; !ok {
			t.Fatalf("catalyst-owner list missing %s; got %+v", id, rows)
		}
	}
}

// TestListDeployments_OwnerTierSeesAll proves the flattened tier=owner
// claim alone grants fleet visibility — some session mint paths carry
// the tier without the realm role (hw86 2026-06-01 session shape).
func TestListDeployments_OwnerTierSeesAll(t *testing.T) {
	h := newFleetTestHandler()
	claims := &auth.Claims{Email: "operator@example.com", Tier: "owner"}

	rows := fleetListRows(t, h, fleetListRequest(claims, ""))
	if len(rows) != 3 {
		t.Fatalf("tier=owner must see all 3 deployments; got %d (%+v)", len(rows), rows)
	}
}

// TestListDeployments_FleetVisibleOwnerFilter proves ?owner= acts as a
// genuine filter for a fleet-visible session instead of the #748
// cross-tenant empty-list guard: filtering by bob returns bob's row.
func TestListDeployments_FleetVisibleOwnerFilter(t *testing.T) {
	h := newFleetTestHandler()
	claims := &auth.Claims{
		Email:       "operator@example.com",
		RealmAccess: auth.RealmAccess{Roles: []string{"catalyst-owner"}},
	}

	rows := fleetListRows(t, h, fleetListRequest(claims, "?owner=bob@example.com"))
	if len(rows) != 1 {
		t.Fatalf("expected exactly bob's row; got %d (%+v)", len(rows), rows)
	}
	if _, ok := rows["dep-bob"]; !ok {
		t.Fatalf("expected dep-bob; got %+v", rows)
	}
}

// TestListDeployments_OrgScopedTierNeverFleetVisible proves the #4110
// guard: an Org-scoped customer session stays confined to its own email
// scope even when realm-config drift leaks catalyst-owner onto the
// token. alice's org-admin session sees only alice's row.
func TestListDeployments_OrgScopedTierNeverFleetVisible(t *testing.T) {
	h := newFleetTestHandler()
	claims := &auth.Claims{
		Email:       "alice@example.com",
		Tier:        auth.OrgScopedTier,
		RealmAccess: auth.RealmAccess{Roles: []string{"catalyst-owner"}},
	}

	rows := fleetListRows(t, h, fleetListRequest(claims, ""))
	if len(rows) != 1 {
		t.Fatalf("org-scoped session must stay email-scoped; got %d (%+v)", len(rows), rows)
	}
	if _, ok := rows["dep-alice"]; !ok {
		t.Fatalf("expected only dep-alice; got %+v", rows)
	}
}

// TestListDeployments_PlainClaimsSessionStillScoped is the regression
// guard for #747/#748: a claims-bearing session WITHOUT the owner role
// or tier remains scoped to its own email — the fleet seam must not
// widen the default boundary.
func TestListDeployments_PlainClaimsSessionStillScoped(t *testing.T) {
	h := newFleetTestHandler()
	claims := &auth.Claims{Email: "alice@example.com", Tier: "developer"}

	rows := fleetListRows(t, h, fleetListRequest(claims, ""))
	if len(rows) != 1 {
		t.Fatalf("plain session must see only its own row; got %d (%+v)", len(rows), rows)
	}
	if _, ok := rows["dep-alice"]; !ok {
		t.Fatalf("expected only dep-alice; got %+v", rows)
	}

	// And the #748 cross-tenant ?owner= guard still returns 200 + empty.
	w := httptest.NewRecorder()
	h.ListDeployments(w, fleetListRequest(claims, "?owner=bob@example.com"))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200; got %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Deployments []map[string]any `json:"deployments"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if len(body.Deployments) != 0 {
		t.Fatalf("cross-tenant ?owner= must stay empty for a plain session; got %+v", body.Deployments)
	}
}
