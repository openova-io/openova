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
	"strings"
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
// live cross-region DR — the HONEST pending fallback stands. #4923: that
// fallback must NEVER fabricate Hetzner regions or a fake 2s/8192 lag; it
// carries the Sovereign's real configured regions (empty here — no env set)
// and zero lag, with source:"pending" so the UI's NOT-LIVE gate holds.
func TestReplicationStatus_NoLivePairSingleRegionFallsBack(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	primary, replica := newCNPGPairFixture("shared-pg", "catalyst-system", "me-east-215-a", "me-east-215-a")
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
	// No genuine cross-region pair → honest pending fallback (not a fabricated
	// "live", and NOT the old "synthesized" Hetzner shape).
	if resp.Source != "pending" {
		t.Errorf("source: got %q want pending (no real cross-region pair)", resp.Source)
	}
	// The fabricated Hetzner constants must be GONE.
	for _, banned := range []string{"hz-fsn-rtz-prod", "hz-hel-rtz-prod"} {
		if resp.PrimaryRegion == banned || resp.CurrentPrimary == banned {
			t.Errorf("fallback leaked Hetzner region %q on a Huawei Sovereign", banned)
		}
		for _, rep := range resp.Replicas {
			if rep.Region == banned {
				t.Errorf("fallback replica leaked Hetzner region %q", banned)
			}
		}
	}
	// The byte-identical fake lag must be GONE.
	if resp.WALLagSeconds != 0 || resp.WALLagBytes != 0 {
		t.Errorf("fallback fabricated lag: walLagSeconds=%v walLagBytes=%d (want 0/0)",
			resp.WALLagSeconds, resp.WALLagBytes)
	}
}

// #4923 — when no Continuum CR and no live cnpg-pair resolve, the honest
// pending fallback still derives the primary/replica regions from the
// Sovereign's REAL configured regions (SOVEREIGN_PRIMARY_REGION /
// SOVEREIGN_REPLICA_REGION, threaded in by bp-catalyst-platform), NEVER from
// a hardcoded Hetzner placeholder.
func TestReplicationStatus_PendingFallbackDerivesRegionsFromDeployment(t *testing.T) {
	t.Setenv("SOVEREIGN_PRIMARY_REGION", "me-east-215-a")
	t.Setenv("SOVEREIGN_REPLICA_REGION", "me-east-215-b")

	h := NewWithPDM(silentLogger(), &fakePDM{})
	// No cnpg-pair, no Continuum CR — forces the pending fallback.
	factory, _ := fakeContinuumDynamicFactory()
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-repl-pending-regions")

	r := chi.NewRouter()
	registerReplicationStatusRoute(r, h)
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
	if resp.Source != "pending" {
		t.Fatalf("source: got %q want pending", resp.Source)
	}
	// Regions derive from the deployment's real (Huawei) regions — NOT Hetzner.
	if resp.PrimaryRegion != "me-east-215-a" {
		t.Errorf("primaryRegion: got %q want me-east-215-a (from SOVEREIGN_PRIMARY_REGION)", resp.PrimaryRegion)
	}
	var sawReplica bool
	for _, rep := range resp.Replicas {
		if rep.Region == "me-east-215-b" {
			sawReplica = true
		}
		if strings.HasPrefix(rep.Region, "hz-") {
			t.Errorf("replica leaked a Hetzner region %q", rep.Region)
		}
	}
	if !sawReplica {
		t.Errorf("replicas: missing derived replica region me-east-215-b; got %+v", resp.Replicas)
	}
	if resp.WALLagSeconds != 0 || resp.WALLagBytes != 0 {
		t.Errorf("pending fallback must not fabricate lag; got %v/%d", resp.WALLagSeconds, resp.WALLagBytes)
	}
}

// #4923 — the live path must report syncState=sync when the primary CNPG
// cluster declares synchronous replication (spec.postgresql.synchronous →
// `synchronous_standby_names = FIRST N (...)`, RPO=0), and it must derive the
// regions from the live CLUSTER placement labels (me-east-215-a/-b), NOT the
// old hardcoded Hetzner constants. Fixture: a real 2-region cnpg-pair whose
// primary half carries the synchronous block, no Continuum CR (derive path).
func TestReplicationStatus_LiveSyncStateFromSynchronousCNPGPair(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	primary, replica := newCNPGPairFixture("shared-pg", "catalyst-system", "me-east-215-a", "me-east-215-b")
	// Primary declares synchronous replication — the bp-cnpg-pair sync mode.
	_ = unstructured.SetNestedMap(primary.Object, map[string]interface{}{
		"method":         "first",
		"number":         int64(1),
		"dataDurability": "required",
	}, "spec", "postgresql", "synchronous")
	factory, _ := fakeContinuumDynamicFactory(primary, replica)
	h.dynamicFactory = factory
	fixed := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	h.SetContinuumClock(func() time.Time { return fixed })
	dep := installUserAccessDeployment(t, h, "dep-repl-sync")

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
		t.Errorf("source: got %q want live", resp.Source)
	}
	// Regions derived from the CLUSTER placement labels, never Hetzner.
	if resp.PrimaryRegion != "me-east-215-a" {
		t.Errorf("primaryRegion: got %q want me-east-215-a (from cluster label)", resp.PrimaryRegion)
	}
	if strings.HasPrefix(resp.PrimaryRegion, "hz-") {
		t.Errorf("primaryRegion leaked Hetzner: %q", resp.PrimaryRegion)
	}
	// syncState reflects the synchronous config, NOT a hardcoded async.
	if resp.SyncState != "sync" {
		t.Errorf("syncState: got %q want sync (spec.postgresql.synchronous is set → RPO=0)", resp.SyncState)
	}
}

// #4923 — the mirror of the sync case: a pair WITHOUT the synchronous block
// (forensic async mode) reports syncState=async off the same live path, so the
// signal is genuinely derived, not hardcoded either way.
func TestReplicationStatus_LiveAsyncStateWhenNoSynchronousBlock(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	primary, replica := newCNPGPairFixture("shared-pg", "catalyst-system", "me-east-215-a", "me-east-215-b")
	// No spec.postgresql.synchronous on the primary → async.
	factory, _ := fakeContinuumDynamicFactory(primary, replica)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-repl-async")

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
		t.Errorf("source: got %q want live", resp.Source)
	}
	if resp.SyncState != "async" {
		t.Errorf("syncState: got %q want async (no synchronous block)", resp.SyncState)
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
