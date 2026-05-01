// Tests for the subdomain-release path introduced by issue #489.
//
// Two independent contracts:
//
//  1. DELETE /api/v1/deployments/{id}/release-subdomain releases the PDM
//     allocation row for a failed-or-abandoned deployment without
//     requiring the operator's HetznerToken. This is the franchise-retry
//     ergonomic — botched provisions must not permanently lock a pool
//     subdomain.
//
//  2. restoreFromStore fires a best-effort PDM Release for any deployment
//     it rewrites from "in-flight" to "failed" because of a Pod restart.
//     The runProvisioning goroutine that would have called Release died
//     with the previous Pod; the rehydration path is the next best seam.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/pdm"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// callReleaseSubdomain wires a chi router so chi.URLParam("id") resolves
// to the supplied id and returns the recorded response.
func callReleaseSubdomain(h *Handler, id string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Delete("/api/v1/deployments/{id}/release-subdomain", h.ReleaseSubdomain)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/deployments/"+id+"/release-subdomain", nil)
	r.ServeHTTP(w, req)
	return w
}

// TestReleaseSubdomain_FailedDeploymentReleasesPDMSlot is the headline
// invariant: a failed deployment with a still-locked PDM allocation gets
// its slot released so the next attempt under the same subdomain
// succeeds.
func TestReleaseSubdomain_FailedDeploymentReleasesPDMSlot(t *testing.T) {
	fpdm := &fakePDM{}
	h := NewWithPDM(silentLogger(), fpdm)

	dep := &Deployment{
		ID:                  "dep-failed-1",
		Status:              "failed",
		Request:             provisioner.Request{SovereignFQDN: "otech5.omani.works", SovereignDomainMode: "pool"},
		pdmReservationToken: "rt-otech5",
		pdmPoolDomain:       "omani.works",
		pdmSubdomain:        "otech5",
	}
	h.deployments.Store(dep.ID, dep)

	w := callReleaseSubdomain(h, dep.ID)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var resp releaseSubdomainResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.PDMReleased {
		t.Errorf("PDMReleased=false, want true (body=%s)", w.Body.String())
	}
	if resp.PoolDomain != "omani.works" || resp.Subdomain != "otech5" {
		t.Errorf("response pool/sub = %q/%q, want omani.works/otech5", resp.PoolDomain, resp.Subdomain)
	}

	// PDM client received exactly one Release call with the correct args.
	if got := len(fpdm.releases); got != 1 {
		t.Fatalf("expected 1 PDM Release call, got %d (%+v)", got, fpdm.releases)
	}
	if fpdm.releases[0].pool != "omani.works" || fpdm.releases[0].sub != "otech5" {
		t.Errorf("Release args = %+v, want omani.works/otech5", fpdm.releases[0])
	}

	// Deployment's PDM pointers were cleared so a follow-up Cancel & Wipe
	// doesn't try to release a slot we just released.
	if dep.pdmPoolDomain != "" || dep.pdmSubdomain != "" || dep.pdmReservationToken != "" {
		t.Errorf("PDM pointers not cleared: pool=%q sub=%q token=%q",
			dep.pdmPoolDomain, dep.pdmSubdomain, dep.pdmReservationToken)
	}
}

// TestReleaseSubdomain_PDMNotFoundIsIdempotent — calling release twice
// must succeed the second time. PDM returns 404/ErrNotFound when the
// allocation has already been released; we treat that as success.
func TestReleaseSubdomain_PDMNotFoundIsIdempotent(t *testing.T) {
	fpdm := &fakePDM{
		release: func(ctx context.Context, pool, sub string) error {
			return pdm.ErrNotFound
		},
	}
	h := NewWithPDM(silentLogger(), fpdm)

	dep := &Deployment{
		ID:            "dep-not-found",
		Status:        "failed",
		Request:       provisioner.Request{SovereignFQDN: "otech.omani.works", SovereignDomainMode: "pool"},
		pdmPoolDomain: "omani.works",
		pdmSubdomain:  "otech",
	}
	h.deployments.Store(dep.ID, dep)

	w := callReleaseSubdomain(h, dep.ID)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s — pdm.ErrNotFound must be treated as success", w.Code, w.Body.String())
	}
	var resp releaseSubdomainResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.PDMReleased {
		t.Errorf("PDMReleased=false on ErrNotFound; idempotent contract requires true")
	}
}

// TestReleaseSubdomain_InFlightDeploymentRefuses prevents stripping a
// reservation out from under a runProvisioning goroutine that may yet
// call Commit.
func TestReleaseSubdomain_InFlightDeploymentRefuses(t *testing.T) {
	fpdm := &fakePDM{}
	h := NewWithPDM(silentLogger(), fpdm)

	for _, status := range []string{"pending", "provisioning", "tofu-applying", "phase1-watching"} {
		t.Run(status, func(t *testing.T) {
			fpdm.releases = nil
			dep := &Deployment{
				ID:            "dep-" + status,
				Status:        status,
				Request:       provisioner.Request{SovereignFQDN: "otech.omani.works", SovereignDomainMode: "pool"},
				pdmPoolDomain: "omani.works",
				pdmSubdomain:  "otech",
			}
			h.deployments.Store(dep.ID, dep)

			w := callReleaseSubdomain(h, dep.ID)

			if w.Code != http.StatusConflict {
				t.Errorf("status=%d, want 409 (in-flight) — body=%s", w.Code, w.Body.String())
			}
			if got := len(fpdm.releases); got != 0 {
				t.Errorf("PDM Release was called for in-flight deployment (%d times)", got)
			}
		})
	}
}

// TestReleaseSubdomain_AdoptedDeploymentRefuses protects customer-owned
// Sovereigns from accidental subdomain release. The Sovereign console
// is reachable on the committed subdomain; releasing would 404 every
// customer request until the operator runs a re-commit.
func TestReleaseSubdomain_AdoptedDeploymentRefuses(t *testing.T) {
	fpdm := &fakePDM{}
	h := NewWithPDM(silentLogger(), fpdm)

	now := time.Now().UTC()
	dep := &Deployment{
		ID:            "dep-adopted",
		Status:        "ready",
		Request:       provisioner.Request{SovereignFQDN: "acme.omani.works", SovereignDomainMode: "pool"},
		pdmPoolDomain: "omani.works",
		pdmSubdomain:  "acme",
		AdoptedAt:     &now,
	}
	h.deployments.Store(dep.ID, dep)

	w := callReleaseSubdomain(h, dep.ID)

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status=%d, want 422 (adopted) — body=%s", w.Code, w.Body.String())
	}
	if got := len(fpdm.releases); got != 0 {
		t.Errorf("PDM Release fired for adopted deployment (%d times)", got)
	}
}

// TestReleaseSubdomain_BYODeploymentNoOp — BYO domains never have a PDM
// allocation. Calling release should return 200 with a noOp marker so
// wizard UI flows can call this unconditionally.
func TestReleaseSubdomain_BYODeploymentNoOp(t *testing.T) {
	fpdm := &fakePDM{}
	h := NewWithPDM(silentLogger(), fpdm)

	dep := &Deployment{
		ID:      "dep-byo",
		Status:  "failed",
		Request: provisioner.Request{SovereignFQDN: "k8s.customer.example", SovereignDomainMode: "byo"},
	}
	h.deployments.Store(dep.ID, dep)

	w := callReleaseSubdomain(h, dep.ID)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp releaseSubdomainResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.PDMReleased {
		t.Errorf("PDMReleased=true on BYO; want false (no allocation to release)")
	}
	if resp.NoOp == "" {
		t.Errorf("NoOp empty on BYO; want explanatory marker")
	}
	if got := len(fpdm.releases); got != 0 {
		t.Errorf("PDM Release fired on BYO deployment (%d times)", got)
	}
}

// TestReleaseSubdomain_UnknownDeploymentIs404
func TestReleaseSubdomain_UnknownDeploymentIs404(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	w := callReleaseSubdomain(h, "no-such-deployment")
	if w.Code != http.StatusNotFound {
		t.Errorf("status=%d, want 404 — body=%s", w.Code, w.Body.String())
	}
}

// TestReleaseSubdomain_PDMTransientFailureSurfaces502 — when PDM is up
// but returns an error other than NotFound (e.g. 500 or network blip),
// surface 502 so the operator knows to retry.
func TestReleaseSubdomain_PDMTransientFailureSurfaces502(t *testing.T) {
	fpdm := &fakePDM{
		release: func(ctx context.Context, pool, sub string) error {
			return errors.New("connection refused")
		},
	}
	h := NewWithPDM(silentLogger(), fpdm)

	dep := &Deployment{
		ID:            "dep-transient",
		Status:        "failed",
		Request:       provisioner.Request{SovereignFQDN: "otech.omani.works", SovereignDomainMode: "pool"},
		pdmPoolDomain: "omani.works",
		pdmSubdomain:  "otech",
	}
	h.deployments.Store(dep.ID, dep)

	w := callReleaseSubdomain(h, dep.ID)

	if w.Code != http.StatusBadGateway {
		t.Errorf("status=%d, want 502 — body=%s", w.Code, w.Body.String())
	}
}

// TestRestoreFromStore_PodRestartOrphanReleasesPDMSlot covers the
// rehydration path: when a Pod dies mid-provisioning, fromRecord rewrites
// the on-disk status to "failed". restoreFromStore must, in that exact
// case, fire pdm.Release for the orphaned reservation. Without this,
// the "12-otech" symptom from issue #489 reproduces — every Pod restart
// during provisioning permanently locks a subdomain.
func TestRestoreFromStore_PodRestartOrphanReleasesPDMSlot(t *testing.T) {
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}

	// Seed an on-disk record that looks like a Pod was killed during
	// `tofu apply`: status="tofu-applying" (in-flight), with a PDM
	// reservation already taken.
	rec := store.Record{
		ID:                  "pod-killed-deployment",
		Status:              "tofu-applying", // in-flight at kill time
		PDMReservationToken: "rt-orphan",
		PDMPoolDomain:       "omani.works",
		PDMSubdomain:        "otech7",
		Request: store.RedactedRequest{
			SovereignFQDN:       "otech7.omani.works",
			SovereignDomainMode: "pool",
		},
		StartedAt: time.Now().UTC(),
	}
	if err := st.Save(rec); err != nil {
		t.Fatalf("seed record: %v", err)
	}

	// New PDM that records every Release call.
	fpdm := &fakePDM{}

	// NewWithStore runs restoreFromStore; the rewrite-to-failed branch
	// fires releaseOrphanedReservation as a goroutine.
	h := NewWithStore(silentLogger(), fpdm, st)

	// Wait briefly for the async release to fire — capped at 2s.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(fpdm.releases) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if got := len(fpdm.releases); got != 1 {
		t.Fatalf("expected 1 orphan PDM Release; got %d (%+v)", got, fpdm.releases)
	}
	if fpdm.releases[0].pool != "omani.works" || fpdm.releases[0].sub != "otech7" {
		t.Errorf("orphan Release args = %+v, want omani.works/otech7", fpdm.releases[0])
	}

	// Deployment landed in sync.Map with status rewritten to "failed".
	val, ok := h.deployments.Load("pod-killed-deployment")
	if !ok {
		t.Fatalf("deployment not in sync.Map after restore")
	}
	dep := val.(*Deployment)
	if dep.Status != "failed" {
		t.Errorf("Status=%q, want failed (Pod-restart rewrite contract)", dep.Status)
	}

	// Wait for the success-path field clear (the goroutine clears the
	// pointers + persists after a successful release).
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		dep.mu.Lock()
		cleared := dep.pdmPoolDomain == "" && dep.pdmSubdomain == ""
		dep.mu.Unlock()
		if cleared {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	dep.mu.Lock()
	defer dep.mu.Unlock()
	if dep.pdmPoolDomain != "" || dep.pdmSubdomain != "" {
		t.Errorf("PDM pointers not cleared after orphan-release: pool=%q sub=%q",
			dep.pdmPoolDomain, dep.pdmSubdomain)
	}
}

// TestRestoreFromStore_TerminalRecordDoesNotReleasePDM proves we DON'T
// re-release a deployment that finished cleanly (status="failed" was
// already the terminal state on disk, not a rewrite). A clean failure
// already had its release path run by the original Pod's
// runProvisioning goroutine; firing a second Release at restart time
// would be a noisy duplicate.
func TestRestoreFromStore_TerminalRecordDoesNotReleasePDM(t *testing.T) {
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}

	// Record was already "failed" on disk — NOT an in-flight kill.
	rec := store.Record{
		ID:                  "clean-fail",
		Status:              "failed",
		PDMReservationToken: "rt-clean",
		PDMPoolDomain:       "omani.works",
		PDMSubdomain:        "otech-clean",
		Request: store.RedactedRequest{
			SovereignFQDN:       "otech-clean.omani.works",
			SovereignDomainMode: "pool",
		},
		StartedAt: time.Now().UTC(),
	}
	if err := st.Save(rec); err != nil {
		t.Fatalf("seed record: %v", err)
	}

	fpdm := &fakePDM{}
	_ = NewWithStore(silentLogger(), fpdm, st)

	// Allow a generous window for any spurious goroutine to fire.
	time.Sleep(150 * time.Millisecond)

	if got := len(fpdm.releases); got != 0 {
		t.Errorf("expected 0 PDM Release calls for already-terminal record, got %d (%+v)",
			got, fpdm.releases)
	}
}
