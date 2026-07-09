// continuum_dr_extras_4930_test.go — #4930 regression coverage.
//
// Follow-up to #4923/#4927 (which fixed the sibling replication-status path).
// Three DR fallback helpers STILL hardcoded Hetzner region literals
// (hz-fsn-rtz-prod / hz-hel-rtz-prod) plus invented telemetry on a Huawei-only
// Sovereign — the exact synthesized-data anti-theater the founder bans:
//
//   synthesizedSwitchoverHistoryItem -> GET /continuum/{name}/switchover/history
//   synthesizedQuorumStatus          -> GET /dr/quorum/status
//   synthesizedContinuumSettings     -> GET /continuum/{name}/settings
//
// These tests lock in the fix: each endpoint's fallback NEVER emits a `hz-`
// region, derives real regions from SOVEREIGN_PRIMARY_REGION /
// SOVEREIGN_REPLICA_REGION (empty when unknown), and returns an honest
// pending/empty shape rather than invented switchover / quorum / lag numbers.
package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"k8s.io/client-go/dynamic"
)

// errDynamicFactory forces sovereignDynamicClient to fail so the handler takes
// its honest fallback path (the branch where the Hetzner literals used to live).
func errDynamicFactory() func(string) (dynamic.Interface, error) {
	return func(string) (dynamic.Interface, error) {
		return nil, errors.New("sovereign dynamic client bootstrapping")
	}
}

// #4930 — a Sovereign that has not performed a switchover has an EMPTY
// switchover history. The prior code fabricated a "last switchover" row with
// Hetzner from/to regions and an invented 47s/3s duration/RPO so the panel
// rendered a non-empty audit trail. The endpoint must now return the honest
// (empty) trail and NEVER leak a Hetzner region.
func TestSwitchoverHistory_NoFabricatedHetznerRow(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	// No auditBus wired → no real switchover events → honest empty trail.
	dep := installUserAccessDeployment(t, h, "dep-switch-empty")

	r := chi.NewRouter()
	r.Get("/api/v1/sovereigns/{id}/continuum/{name}/switchover/history",
		h.HandleContinuumSwitchoverHistory)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/continuum/dr-catalyst-platform/switchover/history", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "hz-") {
		t.Errorf("switchover-history leaked a Hetzner region on a Huawei Sovereign: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "qa-fixture-seed") {
		t.Errorf("switchover-history leaked a fabricated qa-fixture actor: %s", rec.Body.String())
	}
	var resp continuumSwitchoverHistoryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// No fabricated switchover on a Sovereign that never performed one.
	if resp.Total != 0 || len(resp.Items) != 0 {
		t.Errorf("expected empty switchover history (no switchover performed), got total=%d items=%+v",
			resp.Total, resp.Items)
	}
}

// #4930 — the quorum-status fallback (PDM witnesses not observable) must derive
// the primary region from SOVEREIGN_PRIMARY_REGION, NEVER a Hetzner placeholder,
// carry NO fabricated witnesses / lease holder, and mark source:"pending".
func TestQuorumStatus_PendingFallbackDerivesRegionFromEnvNoHetzner(t *testing.T) {
	t.Setenv("SOVEREIGN_PRIMARY_REGION", "me-east-215-a")
	t.Setenv("SOVEREIGN_REPLICA_REGION", "me-east-215-b")

	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = errDynamicFactory() // force the fallback branch
	dep := installUserAccessDeployment(t, h, "dep-quorum-pending")

	r := chi.NewRouter()
	r.Get("/api/v1/sovereigns/{id}/dr/quorum/status", h.HandleDRQuorumStatus)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/dr/quorum/status?continuum=dr-catalyst-platform", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "hz-") {
		t.Errorf("quorum-status leaked a Hetzner region on a Huawei Sovereign: %s", rec.Body.String())
	}
	var resp drQuorumStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Source != "pending" {
		t.Errorf("source: got %q want pending (witnesses not observable)", resp.Source)
	}
	if resp.PrimaryRegion != "me-east-215-a" {
		t.Errorf("primaryRegion: got %q want me-east-215-a (from SOVEREIGN_PRIMARY_REGION)", resp.PrimaryRegion)
	}
	if strings.HasPrefix(resp.PrimaryRegion, "hz-") {
		t.Errorf("primaryRegion leaked a Hetzner region: %q", resp.PrimaryRegion)
	}
	if len(resp.Witnesses) != 0 {
		t.Errorf("pending quorum must not fabricate witnesses; got %+v", resp.Witnesses)
	}
	if resp.LeaseHolder != "" {
		t.Errorf("pending quorum must not invent a lease holder; got %q", resp.LeaseHolder)
	}
	if resp.Quorum == "in-quorum" {
		t.Errorf("pending quorum must not claim in-quorum; got %q", resp.Quorum)
	}
}

// #4930 — the quorum-status fallback with NO configured regions must leave the
// primary region EMPTY (never a Hetzner placeholder).
func TestQuorumStatus_PendingFallbackEmptyRegionWhenEnvUnset(t *testing.T) {
	t.Setenv("SOVEREIGN_PRIMARY_REGION", "")
	t.Setenv("SOVEREIGN_REPLICA_REGION", "")

	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = errDynamicFactory()
	dep := installUserAccessDeployment(t, h, "dep-quorum-empty")

	r := chi.NewRouter()
	r.Get("/api/v1/sovereigns/{id}/dr/quorum/status", h.HandleDRQuorumStatus)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/dr/quorum/status", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp drQuorumStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.PrimaryRegion != "" {
		t.Errorf("primaryRegion: got %q want empty (no configured region, never a placeholder)", resp.PrimaryRegion)
	}
	if strings.Contains(rec.Body.String(), "hz-") {
		t.Errorf("quorum-status leaked a Hetzner region: %s", rec.Body.String())
	}
}

// #4930 — the settings fallback (no live Continuum CR) must derive the hot-
// standby region from SOVEREIGN_REPLICA_REGION, NEVER a Hetzner placeholder,
// and carry no fabricated notification channel / actor.
func TestContinuumSettings_DefaultFallbackDerivesHotStandbyFromEnvNoHetzner(t *testing.T) {
	t.Setenv("SOVEREIGN_PRIMARY_REGION", "me-east-215-a")
	t.Setenv("SOVEREIGN_REPLICA_REGION", "me-east-215-b")

	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = errDynamicFactory() // force the fallback branch
	dep := installUserAccessDeployment(t, h, "dep-settings-default")

	r := chi.NewRouter()
	r.Get("/api/v1/sovereigns/{id}/continuum/{name}/settings", h.HandleContinuumSettingsGet)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/continuum/dr-catalyst-platform/settings", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "hz-") {
		t.Errorf("settings leaked a Hetzner region on a Huawei Sovereign: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "qa-fixture-seed") {
		t.Errorf("settings leaked a fabricated qa-fixture actor: %s", rec.Body.String())
	}
	var resp continuumSettings
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	sawReplica := false
	for _, hs := range resp.HotStandbyRegions {
		if strings.HasPrefix(hs, "hz-") {
			t.Errorf("hotStandbyRegions leaked a Hetzner region: %q", hs)
		}
		if hs == "me-east-215-b" {
			sawReplica = true
		}
	}
	if !sawReplica {
		t.Errorf("hotStandbyRegions missing derived replica me-east-215-b; got %+v", resp.HotStandbyRegions)
	}
	if resp.UpdatedBy == "qa-fixture-seed" {
		t.Errorf("settings must not carry a fabricated qa-fixture actor")
	}
}

// #4930 — the settings fallback with NO configured replica must return an EMPTY
// hotStandbyRegions list (never a Hetzner placeholder).
func TestContinuumSettings_DefaultFallbackEmptyHotStandbyWhenNoReplica(t *testing.T) {
	t.Setenv("SOVEREIGN_PRIMARY_REGION", "me-east-215-a")
	t.Setenv("SOVEREIGN_REPLICA_REGION", "") // single-region: no distinct standby

	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = errDynamicFactory()
	dep := installUserAccessDeployment(t, h, "dep-settings-single")

	r := chi.NewRouter()
	r.Get("/api/v1/sovereigns/{id}/continuum/{name}/settings", h.HandleContinuumSettingsGet)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/continuum/dr-solo/settings", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp continuumSettings
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.HotStandbyRegions) != 0 {
		t.Errorf("single-region fallback must not fabricate a hot-standby region; got %+v", resp.HotStandbyRegions)
	}
	if strings.Contains(rec.Body.String(), "hz-") {
		t.Errorf("settings leaked a Hetzner region: %s", rec.Body.String())
	}
}

// #4930 — the shared env-region deriver blanks the replica when it equals the
// primary (a single-region prov has no distinct standby) and never invents a
// placeholder.
func TestSovereignRegionsFromEnv_BlanksReplicaWhenEqualPrimary(t *testing.T) {
	t.Setenv("SOVEREIGN_PRIMARY_REGION", "me-east-215-a")
	t.Setenv("SOVEREIGN_REPLICA_REGION", "me-east-215-a")
	primary, replica := sovereignRegionsFromEnv()
	if primary != "me-east-215-a" {
		t.Errorf("primary: got %q want me-east-215-a", primary)
	}
	if replica != "" {
		t.Errorf("replica: got %q want empty (equal to primary → no distinct standby)", replica)
	}
}
