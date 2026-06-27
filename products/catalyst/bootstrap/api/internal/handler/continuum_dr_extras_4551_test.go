// continuum_dr_extras_4551_test.go — #4551 regression coverage.
//
// The per-app Topology DR panel rendered for NO app due to two
// data-binding gaps. This file locks the backend half of both fixes:
//
//   GAP 2 (Continuum resolution by applicationRef): the frontend computes
//   the Continuum name as `dr-<app>`, which 404s for a CR named differently
//   (e.g. `cnpg-pair-bp-cnpg-pair-continuum` with spec.applicationRef:<app>)
//   → the old code fell to the synthesized Hetzner/2s shape. The handler
//   must now resolve by applicationRef (and, failing that, derive live off
//   the cnpg-pair) so it returns source:"live" with the real lag.
//
//   GAP 1 helper (Placement-Standby discovery): augmentWithCNPGStandby must
//   surface the region-b cnpg replica as a Standby·Hot target so the panel's
//   hasStandby render gate is satisfied for cross-namespace / differently-
//   named CNPG pairs.
package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	bpv1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
)

func registerReplicationStatusRoute(r chi.Router, h *Handler) {
	r.Get("/api/v1/sovereigns/{id}/continuum/{name}/replication-status",
		h.HandleContinuumReplicationStatus)
}

// GAP 2 — a Continuum CR named `cnpg-pair-...` (NOT `dr-<app>`) but carrying
// spec.applicationRef:<app> must resolve when the frontend queries
// `dr-<app>/replication-status`, returning source:"live".
func TestReplicationStatus_ResolvesByApplicationRef(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	// CR name deliberately != dr-catalyst-platform; only the applicationRef
	// ties it to the app the frontend asks about.
	cr := newContinuumUnstructured(
		"cnpg-pair-bp-cnpg-pair-continuum", "catalyst-system",
		"catalyst-platform", "hz-fsn-rtz-prod", []string{"hz-hel-rtz-prod"})
	factory, _ := fakeContinuumDynamicFactory(cr)
	h.dynamicFactory = factory
	fixed := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	h.SetContinuumClock(func() time.Time { return fixed })
	dep := installUserAccessDeployment(t, h, "dep-repl-appref")

	r := chi.NewRouter()
	registerReplicationStatusRoute(r, h)
	// The frontend's dr-<app> name guess — there is NO CR with this name.
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/continuum/dr-catalyst-platform/replication-status", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp continuumReplicationStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Source != "live" {
		t.Errorf("source: got %q want live (resolved by applicationRef, not synthesized)", resp.Source)
	}
	if resp.PrimaryRegion != "hz-fsn-rtz-prod" {
		t.Errorf("primaryRegion: got %q want hz-fsn-rtz-prod", resp.PrimaryRegion)
	}
}

// GAP 2 (live-pair fallback) — when there is NO Continuum CR at all but a
// real 2-region cnpg-pair backs the app, the replication-status endpoint
// derives the live status off the pair (source:"live"), NOT the synthesized
// Hetzner shape. This is the catalyst-platform / shared-pg live case.
func TestReplicationStatus_DerivesLiveFromCNPGPairWhenNoCR(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	primary, replica := newCNPGPairFixture("shared-pg", "catalyst-system", "hz-fsn-rtz-prod", "hz-hel-rtz-prod")
	factory, _ := fakeContinuumDynamicFactory(primary, replica)
	h.dynamicFactory = factory
	fixed := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	h.SetContinuumClock(func() time.Time { return fixed })
	dep := installUserAccessDeployment(t, h, "dep-repl-livepair")

	r := chi.NewRouter()
	registerReplicationStatusRoute(r, h)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/continuum/dr-shared-pg/replication-status?namespace=catalyst-system", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp continuumReplicationStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Source != "live" {
		t.Errorf("source: got %q want live (derived off cnpg-pair); body=%s", resp.Source, rec.Body.String())
	}
	if resp.PrimaryRegion != "hz-fsn-rtz-prod" {
		t.Errorf("primaryRegion: got %q want hz-fsn-rtz-prod", resp.PrimaryRegion)
	}
	// The standby region must surface in the replicas list so the panel can
	// render the standby card.
	foundReplica := false
	for _, rep := range resp.Replicas {
		if rep.Region == "hz-hel-rtz-prod" {
			foundReplica = true
		}
	}
	if !foundReplica {
		t.Errorf("replicas: missing standby region hz-hel-rtz-prod; got %+v", resp.Replicas)
	}
}

// A single-region pair (both halves same region) must NOT be passed off as
// live cross-region DR — the synthesized fallback stands (honest).
func TestReplicationStatus_NoLivePairSingleRegionFallsBack(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	primary, replica := newCNPGPairFixture("shared-pg", "catalyst-system", "hz-fsn-rtz-prod", "hz-fsn-rtz-prod")
	factory, _ := fakeContinuumDynamicFactory(primary, replica)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-repl-single")

	r := chi.NewRouter()
	registerReplicationStatusRoute(r, h)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/continuum/dr-shared-pg/replication-status?namespace=catalyst-system", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp continuumReplicationStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// No genuine cross-region pair → synthesized fallback (not a fabricated
	// "live").
	if resp.Source != "synthesized" {
		t.Errorf("source: got %q want synthesized (no real cross-region pair)", resp.Source)
	}
}

// GAP 1 — augmentWithCNPGStandby appends the region-b cnpg replica as a
// Standby·Hot target when pod-occupancy produced only a Primary, so the
// frontend's hasStandby render gate is satisfied.
func TestAugmentWithCNPGStandby_AddsReplicaStandby(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	primary, replica := newCNPGPairFixture("shared-pg", "catalyst-system", "hz-fsn-rtz-prod", "hz-hel-rtz-prod")
	factory, _ := fakeContinuumDynamicFactory(primary, replica)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-augment")

	// Only a Primary target from pod occupancy (the cross-namespace replica
	// never matched the app's own identity labels).
	in := []bpv1.PlacementTarget{
		{Region: "hz-fsn-rtz-prod", Cluster: dep.ID, Role: bpv1.DataRolePrimary},
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	out := h.augmentWithCNPGStandby(req, dep.ID, "shared-pg", "catalyst-system", in)

	var standby *bpv1.PlacementTarget
	for i := range out {
		if out[i].Role == bpv1.DataRoleStandby {
			standby = &out[i]
		}
	}
	if standby == nil {
		t.Fatalf("augment: no Standby target added; got %+v", out)
	}
	if standby.Region != "hz-hel-rtz-prod" {
		t.Errorf("standby.Region: got %q want hz-hel-rtz-prod", standby.Region)
	}
	if standby.StandbyType != bpv1.StandbyHot {
		t.Errorf("standby.StandbyType: got %q want Hot", standby.StandbyType)
	}
}

// A genuinely single-region component (no live pair) must NOT gain a phantom
// Standby — augmentWithCNPGStandby returns the input unchanged.
func TestAugmentWithCNPGStandby_NoPairLeavesSingleton(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeContinuumDynamicFactory() // no cnpg clusters seeded
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-augment-none")

	in := []bpv1.PlacementTarget{
		{Region: "hz-fsn-rtz-prod", Cluster: dep.ID, Role: bpv1.DataRolePrimary},
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	out := h.augmentWithCNPGStandby(req, dep.ID, "some-singleton", "catalyst-system", in)
	if len(out) != 1 {
		t.Fatalf("augment: expected unchanged singleton, got %+v", out)
	}
	if out[0].Role != bpv1.DataRolePrimary {
		t.Errorf("role: got %q want Primary", out[0].Role)
	}
}
