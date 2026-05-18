// Tests for the public /api/v1/deployments/in-flight-count endpoint.
//
// Contract:
//   - Counts ONLY Phase-0 in-flight statuses (pending, provisioning,
//     tofu-applying, flux-bootstrapping). NOT phase1-watching (Phase-1
//     resumes across catalyst-api Pod restarts via resumePhase1Watch).
//   - Excludes adopted deployments (post-handover; customer-owned).
//   - Returns 200 + {count, ids} on every call. No auth gate.
//
// The CI deploy-bot gates the catalyst-api image-SHA bump on this
// endpoint returning count=0, to avoid rolling the Pod mid-tofu-apply
// (t13/t17/t21 incident, 2026-05-17).
package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

func newDepForInFlight(h *Handler, id, status string, adopted bool) {
	dep := &Deployment{
		ID:        id,
		Status:    status,
		Request:   provisioner.Request{SovereignFQDN: id + ".example.com"},
		StartedAt: time.Now().Add(-1 * time.Minute),
		eventsCh:  make(chan provisioner.Event),
		done:      make(chan struct{}),
	}
	close(dep.eventsCh)
	close(dep.done)
	if adopted {
		now := time.Now()
		dep.AdoptedAt = &now
	}
	h.deployments.Store(id, dep)
}

// Phase-0 in-flight statuses MUST be counted. Phase-1 + terminal +
// adopted statuses MUST be excluded.
func TestInFlightCount_OnlyPhase0InFlightCounts(t *testing.T) {
	h := &Handler{log: slog.Default()}

	// Phase-0 in-flight — every one of these counts.
	newDepForInFlight(h, "dep-pending", "pending", false)
	newDepForInFlight(h, "dep-provisioning", "provisioning", false)
	newDepForInFlight(h, "dep-tofu-applying", "tofu-applying", false)
	newDepForInFlight(h, "dep-flux-bootstrapping", "flux-bootstrapping", false)

	// Phase-1 — does NOT count (resumable across Pod restarts).
	newDepForInFlight(h, "dep-phase1-watching", "phase1-watching", false)

	// Terminal — does NOT count.
	newDepForInFlight(h, "dep-ready", "ready", false)
	newDepForInFlight(h, "dep-failed", "failed", false)
	newDepForInFlight(h, "dep-wiped", "wiped", false)

	// Adopted — does NOT count even if status somehow regressed.
	newDepForInFlight(h, "dep-adopted-ready", "ready", true)
	newDepForInFlight(h, "dep-adopted-stuck", "phase1-watching", true)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/deployments/in-flight-count", nil)
	h.InFlightCount(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200; got %d body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected Content-Type application/json; got %q", got)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("expected Cache-Control no-store; got %q", got)
	}

	var body inFlightCountResponse
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if body.Count != 4 {
		t.Fatalf("expected count=4 (Phase-0 in-flight only); got %d ids=%v", body.Count, body.IDs)
	}
	wantIDs := []string{
		"dep-flux-bootstrapping",
		"dep-pending",
		"dep-provisioning",
		"dep-tofu-applying",
	}
	if !reflect.DeepEqual(body.IDs, wantIDs) {
		t.Fatalf("expected sorted ids=%v; got %v", wantIDs, body.IDs)
	}
}

// No deployments → count=0, ids=[]. This is the green-light state the
// deploy-bot gates on.
func TestInFlightCount_EmptyStore(t *testing.T) {
	h := &Handler{log: slog.Default()}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/deployments/in-flight-count", nil)
	h.InFlightCount(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200; got %d", w.Code)
	}
	var body inFlightCountResponse
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if body.Count != 0 {
		t.Fatalf("expected count=0; got %d", body.Count)
	}
	if body.IDs == nil {
		t.Fatalf("expected ids=[] (non-nil); got nil — wire shape MUST be a JSON array, not null")
	}
	if len(body.IDs) != 0 {
		t.Fatalf("expected ids=[] empty; got %v", body.IDs)
	}
}

// Only terminal/adopted deployments → count=0. Same green-light state.
func TestInFlightCount_AllTerminal(t *testing.T) {
	h := &Handler{log: slog.Default()}

	newDepForInFlight(h, "dep-ready", "ready", false)
	newDepForInFlight(h, "dep-failed", "failed", false)
	newDepForInFlight(h, "dep-adopted", "ready", true)
	// phase1-watching is observational — Pod restart resumes it via
	// resumePhase1Watch, so it MUST NOT block a deploy bump.
	newDepForInFlight(h, "dep-phase1", "phase1-watching", false)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/deployments/in-flight-count", nil)
	h.InFlightCount(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200; got %d", w.Code)
	}
	var body inFlightCountResponse
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if body.Count != 0 {
		t.Fatalf("expected count=0 (terminal + phase1 + adopted excluded); got %d ids=%v", body.Count, body.IDs)
	}
}
