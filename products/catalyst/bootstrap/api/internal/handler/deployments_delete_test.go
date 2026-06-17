// Tests for DELETE /api/v1/deployments/{id} — record-only delete
// (issue #178). The destructive "deep delete" path lives on /wipe and is
// tested in wipe_test.go; this file covers the record-only seam and the
// refusal policies (adopted → 422, in-flight → 409, unknown → 404).
package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// newDepForDelete builds a Deployment with a chi-url-param-friendly id +
// the closed channels every "settled" deployment needs.
func newDepForDelete(id, status, owner string, adoptedAt *time.Time) *Deployment {
	dep := &Deployment{
		ID:         id,
		Status:     status,
		OwnerEmail: owner,
		Request: provisioner.Request{
			SovereignFQDN: id + ".example.com",
			Region:        "fsn1",
		},
		StartedAt: time.Now().Add(-1 * time.Hour),
		eventsCh:  make(chan provisioner.Event),
		done:      make(chan struct{}),
		AdoptedAt: adoptedAt,
	}
	close(dep.eventsCh)
	close(dep.done)
	return dep
}

// routerForDelete wires the DeleteDeployment handler into a chi router so
// the {id} URL param is parsed the same way it is in production.
func routerForDelete(h *Handler) *chi.Mux {
	r := chi.NewRouter()
	r.Delete("/api/v1/deployments/{id}", h.DeleteDeployment)
	return r
}

// TestDeleteDeployment_RecordOnly_HappyPath — terminal deployment is
// successfully removed from the in-memory map. The wipe path is NOT
// invoked.
func TestDeleteDeployment_RecordOnly_HappyPath(t *testing.T) {
	h := &Handler{log: slog.Default()}
	dep := newDepForDelete("dep-1", "failed", "", nil)
	h.deployments.Store(dep.ID, dep)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/deployments/dep-1", nil)
	rec := httptest.NewRecorder()
	routerForDelete(h).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out deleteDeploymentResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if out.DeploymentID != "dep-1" {
		t.Errorf("DeploymentID=%q, want dep-1", out.DeploymentID)
	}
	if out.Mode != "record-only" {
		t.Errorf("Mode=%q, want record-only", out.Mode)
	}
	if !out.LocalCleaned {
		t.Errorf("LocalCleaned=false, want true")
	}
	if _, present := h.deployments.Load("dep-1"); present {
		t.Errorf("deployments map still contains dep-1 after DELETE")
	}
}

// TestDeleteDeployment_AdoptedReturns422 — handover breadcrumb stays
// intact so the post-handover Sovereign isn't orphaned in the operator's
// admin view.
func TestDeleteDeployment_AdoptedReturns422(t *testing.T) {
	h := &Handler{log: slog.Default()}
	adopted := time.Now().Add(-10 * time.Minute)
	dep := newDepForDelete("dep-adopted", "ready", "", &adopted)
	h.deployments.Store(dep.ID, dep)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/deployments/dep-adopted", nil)
	rec := httptest.NewRecorder()
	routerForDelete(h).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, present := h.deployments.Load("dep-adopted"); !present {
		t.Errorf("adopted deployment was deleted from in-memory map; must stay")
	}
}

// TestDeleteDeployment_InFlightReturns409 — runProvisioning may still
// commit, so the record can't disappear mid-flight.
func TestDeleteDeployment_InFlightReturns409(t *testing.T) {
	h := &Handler{log: slog.Default()}
	dep := newDepForDelete("dep-running", "phase1-watching", "", nil)
	h.deployments.Store(dep.ID, dep)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/deployments/dep-running", nil)
	rec := httptest.NewRecorder()
	routerForDelete(h).ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, present := h.deployments.Load("dep-running"); !present {
		t.Errorf("in-flight deployment was deleted; must stay until terminal")
	}
}

// TestDeleteDeployment_UnknownReturns404 — also covers idempotency: a
// second DELETE after a successful first one returns 404.
func TestDeleteDeployment_UnknownReturns404(t *testing.T) {
	h := &Handler{log: slog.Default()}

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/deployments/never-existed", nil)
	rec := httptest.NewRecorder()
	routerForDelete(h).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestDeleteDeployment_TerminalWipedAllowed — the "wiped" status is
// terminal, not in-flight; the operator IS allowed to drop the row
// after a wipe so the admin list isn't permanently cluttered with
// already-purged Sovereigns.
func TestDeleteDeployment_TerminalWipedAllowed(t *testing.T) {
	h := &Handler{log: slog.Default()}
	dep := newDepForDelete("dep-wiped", "wiped", "", nil)
	h.deployments.Store(dep.ID, dep)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/deployments/dep-wiped", nil)
	rec := httptest.NewRecorder()
	routerForDelete(h).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, present := h.deployments.Load("dep-wiped"); present {
		t.Errorf("wiped deployment was not removed from map")
	}
}

// newPoolDepForDelete builds a pool-mode Deployment with PDM pointers set,
// for the #3728 record-only-delete release tests.
func newPoolDepForDelete(id, status, sub string) *Deployment {
	dep := &Deployment{
		ID:     id,
		Status: status,
		Request: provisioner.Request{
			SovereignFQDN:       sub + ".omani.works",
			SovereignDomainMode: "pool",
			Region:              "fsn1",
		},
		StartedAt:     time.Now().Add(-1 * time.Hour),
		eventsCh:      make(chan provisioner.Event),
		done:          make(chan struct{}),
		pdmPoolDomain: "omani.works",
		pdmSubdomain:  sub,
	}
	close(dep.eventsCh)
	close(dep.done)
	return dep
}

// TestDeleteDeployment_WipedPoolReleasesSubdomain is the #3728 closure for
// the record-only path: deleting the breadcrumb of an ALREADY-WIPED pool
// Sovereign must release its pool subdomain so the slot is re-fireable.
// Previously this path never called PDM release, leaking the active row.
func TestDeleteDeployment_WipedPoolReleasesSubdomain(t *testing.T) {
	fpdm := &fakePDM{}
	h := NewWithPDM(silentLogger(), fpdm)
	dep := newPoolDepForDelete("dep-wiped-pool", "wiped", "hw155")
	h.deployments.Store(dep.ID, dep)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/deployments/dep-wiped-pool", nil)
	rec := httptest.NewRecorder()
	routerForDelete(h).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out deleteDeploymentResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.PDMReleased {
		t.Errorf("PDMReleased=false; a record-only delete of a wiped pool Sovereign must release the subdomain (body=%s)", rec.Body.String())
	}
	if got := len(fpdm.releases); got != 1 || fpdm.releases[0].sub != "hw155" {
		t.Errorf("expected one Release(omani.works, hw155), got %+v", fpdm.releases)
	}
}

// TestDeleteDeployment_LivePoolDoesNotReleaseSubdomain is the safety
// invariant: a record-only delete of a LIVE pool Sovereign (status=ready,
// deliberately orphaned by the operator) must NOT release its subdomain —
// the running cluster still serves DNS from that pool child zone, and
// dropping it would break the live Sovereign. Refs #3728.
func TestDeleteDeployment_LivePoolDoesNotReleaseSubdomain(t *testing.T) {
	fpdm := &fakePDM{}
	h := NewWithPDM(silentLogger(), fpdm)
	dep := newPoolDepForDelete("dep-live-pool", "ready", "hw160")
	h.deployments.Store(dep.ID, dep)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/deployments/dep-live-pool", nil)
	rec := httptest.NewRecorder()
	routerForDelete(h).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out deleteDeploymentResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.PDMReleased {
		t.Errorf("PDMReleased=true for a LIVE pool Sovereign; releasing would break its DNS")
	}
	if got := len(fpdm.releases); got != 0 {
		t.Errorf("PDM Release fired for a live orphaned Sovereign (%d times) — must not", got)
	}
}
