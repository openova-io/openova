// Tests for the /api/v1/deployments list endpoint (issue #747).
//
// Contract: GET /api/v1/deployments returns the slim shape of every
// deployment owned by the authenticated session, optionally narrowed by
// ?owner=. The session header is the security boundary; the query param
// is a hint that is silently overridden when the session is set so a
// signed-in attacker cannot escalate by passing ?owner=victim@.
//
// Adopted deployments (post-handover) are excluded from the list because
// the customer's Sovereign owns them — Catalyst-Zero only retains the
// minimum-retention breadcrumb required for the redirect contract.
package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// newDepListEntry constructs a Deployment with a deterministic id so a
// test can register multiple owners and assert they get filtered.
func newDepListEntry(h *Handler, id, owner, status string, adopted bool) {
	dep := &Deployment{
		ID:        id,
		Status:    status,
		Request:   provisioner.Request{SovereignFQDN: id + ".example.com", Region: "fsn1"},
		StartedAt: time.Now().Add(-5 * time.Minute),
		eventsCh:  make(chan provisioner.Event),
		done:      make(chan struct{}),
		OwnerEmail: owner,
	}
	close(dep.eventsCh)
	close(dep.done)
	if adopted {
		now := time.Now()
		dep.AdoptedAt = &now
	}
	h.deployments.Store(id, dep)
}

// TestListDeployments_FilterBySessionEmail proves that when a session
// header is set, the list is scoped to that email. The original #747
// implementation silently overrode a mismatching ?owner= param to the
// session.email; #748 tightened the policy: a cross-tenant ?owner=
// returns 200 + empty rather than collapsing to session-only rows so
// the security boundary is explicit (TestListDeployments_OwnerQueryParam
// covers the cross-tenant case).
//
// This test now exercises the "no ?owner= or matching ?owner=" path —
// alice's session sees only her two rows, bob's row never leaks.
func TestListDeployments_FilterBySessionEmail(t *testing.T) {
	h := &Handler{log: slog.Default()}
	newDepListEntry(h, "dep-mine-1", "alice@example.com", "phase1-watching", false)
	newDepListEntry(h, "dep-mine-2", "alice@example.com", "ready", false)
	newDepListEntry(h, "dep-other", "bob@example.com", "phase1-watching", false)

	// alice's session, no ?owner= → her rows only.
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/deployments", nil)
	r.Header.Set("X-User-Email", "alice@example.com")
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
	if len(body.Deployments) != 2 {
		t.Fatalf("expected 2 deployments owned by alice; got %d (%+v)", len(body.Deployments), body.Deployments)
	}
	for _, d := range body.Deployments {
		if d["ownerEmail"] != "alice@example.com" {
			t.Fatalf("session-email filter failed: leaked %v", d)
		}
	}
}

// Issue #748 — ?owner=<other-email> while session is alice@example.com
// MUST return an empty list (200 + []), NOT silently collapse to
// alice's rows. The response shape itself must NOT differentiate
// "exists but not yours" from "doesn't exist" — same posture as the
// issue #689 404-not-403 rule on /deployments/{id}. This is the
// "more secure of the two" option the issue spec offered.
func TestListDeployments_CrossTenantOwnerQueryReturnsEmpty(t *testing.T) {
	h := &Handler{log: slog.Default()}
	newDepListEntry(h, "dep-mine", "alice@example.com", "phase1-watching", false)
	newDepListEntry(h, "dep-theirs", "bob@example.com", "phase1-watching", false)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/deployments?owner=bob@example.com", nil)
	r.Header.Set("X-User-Email", "alice@example.com")
	h.ListDeployments(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("cross-tenant ?owner MUST be 200 (not 403 — never leak existence); got %d", w.Code)
	}
	var body struct {
		Deployments []map[string]any `json:"deployments"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Deployments) != 0 {
		t.Fatalf("cross-tenant ?owner MUST return empty list; got %d (%+v)", len(body.Deployments), body.Deployments)
	}
}

// TestListDeployments_ExcludesAdopted proves that post-handover
// deployments (AdoptedAt != nil) are not returned. The wizard redirect
// must not pull the operator back into Catalyst-Zero once the customer
// has accepted the Sovereign.
func TestListDeployments_ExcludesAdopted(t *testing.T) {
	h := &Handler{log: slog.Default()}
	newDepListEntry(h, "dep-adopted", "alice@example.com", "adopted", true)
	newDepListEntry(h, "dep-live", "alice@example.com", "phase1-watching", false)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/deployments", nil)
	r.Header.Set("X-User-Email", "alice@example.com")
	h.ListDeployments(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200; got %d", w.Code)
	}
	var body struct {
		Deployments []map[string]any `json:"deployments"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if len(body.Deployments) != 1 {
		t.Fatalf("expected 1 deployment (adopted excluded); got %d", len(body.Deployments))
	}
	if body.Deployments[0]["id"] != "dep-live" {
		t.Fatalf("adopted deployment leaked into list: %+v", body.Deployments[0])
	}
}

// TestListDeployments_OwnerQueryWithoutSession proves the no-middleware
// fallback: in CI / tests / Sovereign-side bootstrap without
// RequireSession in the chain, the ?owner= query param IS the filter.
// A consumer that wants every row passes no ?owner= and gets the lot.
func TestListDeployments_OwnerQueryWithoutSession(t *testing.T) {
	h := &Handler{log: slog.Default()}
	newDepListEntry(h, "dep-a", "alice@example.com", "phase1-watching", false)
	newDepListEntry(h, "dep-b", "bob@example.com", "ready", false)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/deployments?owner=alice@example.com", nil)
	h.ListDeployments(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200; got %d", w.Code)
	}
	var body struct {
		Deployments []map[string]any `json:"deployments"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if len(body.Deployments) != 1 {
		t.Fatalf("expected 1 deployment owned by alice; got %d", len(body.Deployments))
	}
	if body.Deployments[0]["id"] != "dep-a" {
		t.Fatalf("wrong deployment returned: %+v", body.Deployments[0])
	}
}

// TestListDeployments_EmptyOwnerNoSessionReturnsAll — smoke test for the
// CI passthrough. No session, no ?owner= → list everything (including
// legacy empty-owner rows).
func TestListDeployments_EmptyOwnerNoSessionReturnsAll(t *testing.T) {
	h := &Handler{log: slog.Default()}
	newDepListEntry(h, "dep-legacy", "", "ready", false)
	newDepListEntry(h, "dep-owned", "alice@example.com", "phase1-watching", false)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/deployments", nil)
	h.ListDeployments(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200; got %d", w.Code)
	}
	var body struct {
		Deployments []map[string]any `json:"deployments"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if len(body.Deployments) != 2 {
		t.Fatalf("expected 2 deployments (passthrough); got %d", len(body.Deployments))
	}
}

// TestListDeployments_LegacyRowsExcludedWhenSessionSet — a signed-in
// operator must NOT see legacy rows (empty OwnerEmail) belonging to
// some other migration window. They get only their own rows.
func TestListDeployments_LegacyRowsExcludedWhenSessionSet(t *testing.T) {
	h := &Handler{log: slog.Default()}
	newDepListEntry(h, "dep-legacy", "", "ready", false)
	newDepListEntry(h, "dep-mine", "alice@example.com", "phase1-watching", false)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/deployments", nil)
	r.Header.Set("X-User-Email", "alice@example.com")
	h.ListDeployments(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200; got %d", w.Code)
	}
	var body struct {
		Deployments []map[string]any `json:"deployments"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if len(body.Deployments) != 1 || body.Deployments[0]["id"] != "dep-mine" {
		t.Fatalf("legacy row leaked or mine missing: %+v", body.Deployments)
	}
}
