// fleet_dedup_5485_test.go — #5485 defect 6: GET /fleet/applications
// duplicated every application under a dual region key.
//
// Live-observed on hw291 (2026-07-30): 140 items / 70 unique names —
// each app emitted once under the cluster-shaped key
// "hw-me-east-215-a-rtz-prod" and once under the cloudRegion
// "me-east-215-a", with sovereign.id empty. Root shape (same class as
// the closed #2624 — a wrong-source region key): the deployments map
// held TWO records for the SAME Sovereign FQDN — the handover-imported
// canonical record (hex id, wizard cloudRegions, AdoptedAt stamped)
// AND a chroot-synthesised ghost (chrootEnsureDeployment stores a
// record for ANY asserted deployment id, with env-sourced regions that
// on HCS Sovereigns carry cluster names — the hw101 evidence baked
// into chrootRegionsFromPrimaryReplicaEnv's comment). Both records
// resolve to the same cluster, so the per-Sovereign fan-out emitted
// every application once per record.
//
// The fix collapses same-FQDN records to ONE canonical entry before
// the fan-out (adopted > ready > newest, merging empty identity fields
// from the losers) and keeps a row-level (sovereign, namespace, name)
// dedup net, so each application appears exactly once with the
// cloudRegion key and a populated sovereign.id.
//
// Removing the canonicalFleetSovereigns collapse in
// HandleFleetApplications makes these tests fail with 2N rows — that
// is the pre-fix behaviour this file refutes.
package handler

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// installDualKeySovereign seeds the hw291 dual-record shape: the
// canonical adopted record plus the chroot-synthesised ghost, both
// carrying the same Sovereign FQDN.
func installDualKeySovereign(t *testing.T, h *Handler) (canonicalID string) {
	t.Helper()
	adoptedAt := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	canonical := &Deployment{
		ID:     "afc8800bc0375485",
		Status: "ready",
		Request: provisioner.Request{
			SovereignFQDN: "hw291.omani.works",
			Regions: []provisioner.RegionSpec{
				{Provider: "huawei", CloudRegion: "me-east-215-a"},
				{Provider: "huawei", CloudRegion: "me-east-215-b"},
			},
			Region: "me-east-215-a",
		},
		Result: &provisioner.Result{
			SovereignFQDN:  "hw291.omani.works",
			KubeconfigPath: "/dev/null",
		},
		StartedAt: time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC),
		AdoptedAt: &adoptedAt,
		mu:        sync.Mutex{},
	}
	h.deployments.Store(canonical.ID, canonical)

	ghost := &Deployment{
		ID:     "hw-me-east-215-a-rtz-prod",
		Status: "ready",
		Request: provisioner.Request{
			SovereignFQDN: "hw291.omani.works",
			Regions: []provisioner.RegionSpec{
				{CloudRegion: "hw-me-east-215-a-rtz-prod"},
			},
			Region: "hw-me-east-215-a-rtz-prod",
		},
		Result: &provisioner.Result{
			SovereignFQDN:  "hw291.omani.works",
			KubeconfigPath: "/dev/null",
		},
		StartedAt: time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC),
		mu:        sync.Mutex{},
	}
	h.deployments.Store(ghost.ID, ghost)
	return canonical.ID
}

// The dual-key fixture must yield each application EXACTLY once, under
// the canonical cloudRegion key, with sovereign.id populated from the
// canonical deployment record.
func TestHandleFleetApplications_DualRegionKeyDeduped(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	canonicalID := installDualKeySovereign(t, h)
	factory, _ := fakeFleetDynamicFactory(
		newAppCR("blog", "acme", "bp-wordpress", "1.0", topologySingleRegion, "Ready", "me-east-215-a"),
		newAppCR("shop", "acme", "bp-medusa", "2.1", topologySingleRegion, "Ready", "me-east-215-a"),
	)
	h.dynamicFactory = factory

	rec := callUserAccess(t, h, http.MethodGet, "/api/v1/fleet/applications", nil, registerFleetRoutes)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp fleetApplicationsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Pre-fix: 2 apps × 2 same-FQDN records = 4 rows, 2 unique names.
	if resp.Total != 2 {
		t.Fatalf("total: want 2 (one row per app), got %d: %+v", resp.Total, resp.Applications)
	}
	seenNames := map[string]int{}
	for _, row := range resp.Applications {
		seenNames[row.App.Name]++
		if row.Sovereign.ID != canonicalID {
			t.Errorf("row %q sovereign.id: want %q (canonical deployment), got %q",
				row.App.Name, canonicalID, row.Sovereign.ID)
		}
		if row.Sovereign.Region != "me-east-215-a" {
			t.Errorf("row %q sovereign.region: want cloudRegion me-east-215-a, got %q",
				row.App.Name, row.Sovereign.Region)
		}
	}
	for name, n := range seenNames {
		if n != 1 {
			t.Errorf("app %q appears %d times, want exactly 1", name, n)
		}
	}
	// The per-Sovereign rollup must carry ONE entry for the Sovereign,
	// not one per duplicate record.
	if len(resp.Sovereigns) != 1 {
		t.Fatalf("rollup: want 1 Sovereign entry, got %d: %+v", len(resp.Sovereigns), resp.Sovereigns)
	}
	if resp.Sovereigns[0].ID != canonicalID {
		t.Errorf("rollup sovereign id: want %q, got %q", canonicalID, resp.Sovereigns[0].ID)
	}
	if resp.Sovereigns[0].Apps != 2 {
		t.Errorf("rollup apps count: want 2, got %d", resp.Sovereigns[0].Apps)
	}
}

// A same-FQDN group where the ranked winner carries an EMPTY id must
// still emit rows with sovereign.id populated from the deployment
// context (the sibling record / the deployments-map key).
func TestHandleFleetApplications_SovereignIDPopulatedFromContext(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	adoptedAt := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	// Adopted winner with an EMPTY ID field, stored under a map key —
	// the degenerate shape behind the live "sovereign.id": "" symptom.
	winner := &Deployment{
		ID:     "",
		Status: "ready",
		Request: provisioner.Request{
			SovereignFQDN: "hw291.omani.works",
			Regions:       []provisioner.RegionSpec{{CloudRegion: "me-east-215-a"}},
			Region:        "me-east-215-a",
		},
		Result: &provisioner.Result{
			SovereignFQDN:  "hw291.omani.works",
			KubeconfigPath: "/dev/null",
		},
		StartedAt: time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC),
		AdoptedAt: &adoptedAt,
		mu:        sync.Mutex{},
	}
	h.deployments.Store("dep-key-5485", winner)
	factory, _ := fakeFleetDynamicFactory(
		newAppCR("blog", "acme", "bp-wordpress", "1.0", topologySingleRegion, "Ready", "me-east-215-a"),
	)
	h.dynamicFactory = factory

	rec := callUserAccess(t, h, http.MethodGet, "/api/v1/fleet/applications", nil, registerFleetRoutes)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp fleetApplicationsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("total: want 1, got %d: %+v", resp.Total, resp.Applications)
	}
	if got := resp.Applications[0].Sovereign.ID; got != "dep-key-5485" {
		t.Errorf("sovereign.id: want the deployments-map key %q, got %q", "dep-key-5485", got)
	}
}
