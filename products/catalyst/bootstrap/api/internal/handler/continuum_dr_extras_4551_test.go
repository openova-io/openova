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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

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

// #4886 — the Continuum controller records the ACTIVE region as
// `status.leaseHolder` and the lag as `status.replicationLagSeconds` (NOT the
// CNPGPair-only `status.currentPrimary` / `status.walLagSeconds`). The spine
// continuums (openbao raft, keycloak/gitea/harbor) carry no cnpgPair link, so
// the replication-status endpoint must read those continuum-level fields
// directly — otherwise the panel shows the wrong active region (after a
// switchover) and a hardcoded 0 lag. Fixture: a spine Continuum whose lease has
// flipped to region-b (leaseHolder != spec.primaryRegion) with a 7s lag.
func TestReplicationStatus_ReadsLeaseHolderAndReplicationLagSeconds(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := newContinuumUnstructured(
		"dr-spine-openbao", "openbao", "openbao",
		"me-east-215-a", []string{"me-east-215-b"})
	// Lease has flipped to region-b (a switchover); the numeric lag lives in
	// status.replicationLagSeconds (int64), the continuum-controller spelling.
	_ = unstructured.SetNestedField(cr.Object, "me-east-215-b", "status", "leaseHolder")
	_ = unstructured.SetNestedField(cr.Object, int64(7), "status", "replicationLagSeconds")
	factory, _ := fakeContinuumDynamicFactory(cr)
	h.dynamicFactory = factory
	fixed := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	h.SetContinuumClock(func() time.Time { return fixed })
	dep := installUserAccessDeployment(t, h, "dep-repl-leaseholder")

	r := chi.NewRouter()
	registerReplicationStatusRoute(r, h)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/continuum/dr-spine-openbao/replication-status?namespace=openbao", nil)
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
		t.Errorf("source: got %q want live", resp.Source)
	}
	// Active region = leaseHolder (region-b after the switchover), NOT the
	// static spec.primaryRegion.
	if resp.CurrentPrimary != "me-east-215-b" {
		t.Errorf("currentPrimary: got %q want me-east-215-b (=status.leaseHolder)", resp.CurrentPrimary)
	}
	// Numeric lag from status.replicationLagSeconds.
	if resp.WALLagSeconds != 7 {
		t.Errorf("walLagSeconds: got %v want 7 (=status.replicationLagSeconds)", resp.WALLagSeconds)
	}
	// The standby region surfaces so the panel can render the standby card.
	foundStandby := false
	for _, rep := range resp.Replicas {
		if rep.Region == "me-east-215-b" {
			foundStandby = true
		}
	}
	if !foundStandby {
		t.Errorf("replicas: missing standby me-east-215-b; got %+v", resp.Replicas)
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

// Continuum-CR fallback — the #4551 render-gate completion. The
// REAL 2-region topology splits the CNPG halves across two apiservers: the
// primary Cluster CR lives on the region-a apiserver, the replica Cluster CR
// on the region-B apiserver. The region-a sovereignDynamicClient therefore
// lists ONLY the primary half → findCNPGPairForApp returns no usable pair →
// the cnpg-pair path can't surface a Standby. The Continuum CR (in region-a)
// carries the standby region in spec.hotStandbyRegions; augmentWithCNPGStandby
// must read it and synthesize the Standby·Hot target so hasStandby is true.
//
// Fixture: ONLY the primary cnpg Cluster half is seeded (the replica half is
// deliberately absent — it lives on the unreachable region-B apiserver) plus
// the Continuum CR carrying hotStandbyRegions. This reproduces dep
// 91dc05917e44d1c1, where `kubectl get cluster cnpg-pair-...-replica` was
// NotFound on region-a.
func TestAugmentWithCNPGStandby_ContinuumFallback_AddsStandbyWhenReplicaHalfUnlistable(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	// Only the PRIMARY cnpg Cluster half is reachable (region-a apiserver);
	// the replica half is absent (lives on the region-B apiserver). The
	// Continuum CR — readable in region-a — carries the standby region.
	primary, _ := newCNPGPairFixture("cnpg-pair-bp-cnpg-pair", "catalyst-system", "hw-me-east-215-a-rtz-prod", "hw-me-east-215-b-rtz-prod")
	cr := newContinuumUnstructured(
		"cnpg-pair-bp-cnpg-pair-continuum", "catalyst-system",
		"catalyst-platform", "hw-me-east-215-a-rtz-prod",
		[]string{"hw-me-east-215-b-rtz-prod"})
	factory, _ := fakeContinuumDynamicFactory(primary, cr)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-augment-continuum")

	// Pod occupancy surfaced ONLY the Primary (region-a). The FE sends the
	// bp-prefixed route id; the Continuum applicationRef is the bare form.
	in := []bpv1.PlacementTarget{
		{Region: "hw-me-east-215-a-rtz-prod", Cluster: dep.ID, Role: bpv1.DataRolePrimary},
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	out := h.augmentWithCNPGStandby(req, dep.ID, "bp-catalyst-platform", "catalyst-system", in)

	var standby *bpv1.PlacementTarget
	for i := range out {
		if out[i].Role == bpv1.DataRoleStandby {
			standby = &out[i]
		}
	}
	if standby == nil {
		t.Fatalf("Continuum fallback: no Standby target added (hasStandby would be false → DR panel never renders); got %+v", out)
	}
	if standby.Region != "hw-me-east-215-b-rtz-prod" {
		t.Errorf("standby.Region: got %q want hw-me-east-215-b-rtz-prod (off Continuum spec.hotStandbyRegions)", standby.Region)
	}
	if standby.StandbyType != bpv1.StandbyHot {
		t.Errorf("standby.StandbyType: got %q want Hot", standby.StandbyType)
	}
}

// A singleton app whose Continuum CR (if any) names NO distinct standby — or
// has no Continuum CR at all — must NOT gain a phantom Standby. Here there is
// no Continuum CR and no cnpg pair: the input is returned unchanged.
func TestAugmentWithCNPGStandby_ContinuumFallback_SingletonNoStandby(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeContinuumDynamicFactory() // no Continuum CR, no cnpg clusters
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-augment-continuum-none")

	in := []bpv1.PlacementTarget{
		{Region: "hw-me-east-215-a-rtz-prod", Cluster: dep.ID, Role: bpv1.DataRolePrimary},
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	out := h.augmentWithCNPGStandby(req, dep.ID, "bp-grafana", "mgmt", in)
	if len(out) != 1 || out[0].Role != bpv1.DataRolePrimary {
		t.Fatalf("singleton: expected unchanged single Primary, got %+v", out)
	}
}

// A Continuum CR whose only hotStandbyRegion EQUALS a live Primary region must
// NOT produce a same-region "standby" — that is dishonest (single-region prov
// or a label echo). continuumStandbyRegion filters it out.
func TestAugmentWithCNPGStandby_ContinuumFallback_SameRegionNotStandby(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := newContinuumUnstructured(
		"dr-solo", "catalyst-system", "solo",
		"hw-me-east-215-a-rtz-prod",
		[]string{"hw-me-east-215-a-rtz-prod"}) // same as the Primary region
	factory, _ := fakeContinuumDynamicFactory(cr)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-augment-continuum-sameregion")

	in := []bpv1.PlacementTarget{
		{Region: "hw-me-east-215-a-rtz-prod", Cluster: dep.ID, Role: bpv1.DataRolePrimary},
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	out := h.augmentWithCNPGStandby(req, dep.ID, "solo", "catalyst-system", in)
	if len(out) != 1 {
		t.Fatalf("same-region standby must be suppressed; got %+v", out)
	}
}
