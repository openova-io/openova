package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// TestDeployment_AdoptedAtNilByDefault verifies a fresh deployment does
// NOT surface adoptedAt in its State() snapshot. The redirect is gated
// on this field, so an absent value is the canonical "still under
// Catalyst-Zero administration" signal.
func TestDeployment_AdoptedAtNilByDefault(t *testing.T) {
	dep := &Deployment{
		ID:        "test-no-adopt",
		Status:    "ready",
		StartedAt: time.Now(),
		eventsCh:  make(chan provisioner.Event),
		done:      make(chan struct{}),
		Request: provisioner.Request{
			SovereignFQDN: "omantel.omani.works",
		},
	}
	close(dep.eventsCh)
	close(dep.done)

	out := dep.State()
	if _, ok := out["adoptedAt"]; ok {
		t.Errorf("adoptedAt should be absent when nil; got %v", out["adoptedAt"])
	}
}

// TestDeployment_AdoptedAtSurfacedAtTopLevel confirms a non-nil
// AdoptedAt is lifted to the JSON shape, which is the contract the
// UI's beforeLoad redirect depends on.
func TestDeployment_AdoptedAtSurfacedAtTopLevel(t *testing.T) {
	adoptedAt := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	dep := &Deployment{
		ID:        "test-adopted",
		Status:    "ready",
		StartedAt: time.Now(),
		AdoptedAt: &adoptedAt,
		eventsCh:  make(chan provisioner.Event),
		done:      make(chan struct{}),
		Request: provisioner.Request{
			SovereignFQDN: "omantel.omani.works",
		},
	}
	close(dep.eventsCh)
	close(dep.done)

	out := dep.State()
	got, ok := out["adoptedAt"].(string)
	if !ok {
		t.Fatalf("adoptedAt missing or wrong type: %v", out["adoptedAt"])
	}
	if got != "2026-04-30T12:00:00Z" {
		t.Errorf("adoptedAt = %q; want 2026-04-30T12:00:00Z", got)
	}
}

// TestGetDeployment_AdoptedAtRoundTripsThroughJSON verifies the HTTP
// response carries adoptedAt in the same shape as componentStates +
// phase1FinishedAt — the UI consumes them together.
func TestGetDeployment_AdoptedAtRoundTripsThroughJSON(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	adoptedAt := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	dep := &Deployment{
		ID:        "phase8-adopted",
		Status:    "ready",
		StartedAt: time.Now(),
		AdoptedAt: &adoptedAt,
		eventsCh:  make(chan provisioner.Event),
		done:      make(chan struct{}),
		Request: provisioner.Request{
			SovereignFQDN: "omantel.omani.works",
			Region:        "fsn1",
		},
	}
	close(dep.eventsCh)
	close(dep.done)
	h.deployments.Store(dep.ID, dep)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/deployments/"+dep.ID, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", dep.ID)
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))

	h.GetDeployment(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["adoptedAt"] != "2026-05-01T00:00:00Z" {
		t.Errorf("adoptedAt = %v; want 2026-05-01T00:00:00Z", got["adoptedAt"])
	}
	if got["sovereignFQDN"] != "omantel.omani.works" {
		t.Errorf("sovereignFQDN = %v; want omantel.omani.works", got["sovereignFQDN"])
	}
}

// TestDeployment_AdoptedAtPersists round-trips a Deployment through
// toRecord -> fromRecord and verifies the field survives. This is the
// critical guarantee that a Pod restart between handover and
// decommission still shows the redirect.
func TestDeployment_AdoptedAtPersists(t *testing.T) {
	adoptedAt := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	original := &Deployment{
		ID:        "persist-adopt",
		Status:    "ready",
		StartedAt: time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC),
		AdoptedAt: &adoptedAt,
		eventsCh:  make(chan provisioner.Event),
		done:      make(chan struct{}),
		Request: provisioner.Request{
			SovereignFQDN: "omantel.omani.works",
		},
	}
	close(original.eventsCh)
	close(original.done)

	rec := original.toRecord()
	if rec.AdoptedAt == nil {
		t.Fatal("toRecord: AdoptedAt is nil; expected non-nil")
	}
	if !rec.AdoptedAt.Equal(adoptedAt) {
		t.Errorf("toRecord: AdoptedAt = %v; want %v", *rec.AdoptedAt, adoptedAt)
	}

	rehydrated := fromRecord(rec)
	if rehydrated.AdoptedAt == nil {
		t.Fatal("fromRecord: AdoptedAt is nil; expected non-nil")
	}
	if !rehydrated.AdoptedAt.Equal(adoptedAt) {
		t.Errorf("fromRecord: AdoptedAt = %v; want %v", *rehydrated.AdoptedAt, adoptedAt)
	}
}

// TestStoreRecord_AdoptedAtJSONShape confirms the on-disk JSON has
// adoptedAt at the top level (not nested) so an `ls` of the PVC
// directory returns operator-readable timestamps.
func TestStoreRecord_AdoptedAtJSONShape(t *testing.T) {
	adoptedAt := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	rec := store.Record{
		ID:        "shape-adopt",
		Status:    "ready",
		AdoptedAt: &adoptedAt,
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["adoptedAt"] != "2026-04-30T12:00:00Z" {
		t.Errorf("on-disk adoptedAt = %v; want 2026-04-30T12:00:00Z", got["adoptedAt"])
	}
}
