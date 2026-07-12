package handler

import (
	"context"
	"io"
	"log/slog"
	"testing"
)

// recordingEVSReaper captures the arguments the wipe path passes to
// SweepOrphanEVS so the wiring (right active set + destructive=true) is
// asserted without an HCS endpoint.
type recordingEVSReaper struct {
	called         bool
	gotActive      map[string]struct{}
	gotDestructive bool
	gotRegion      string
	ret            int
}

func (r *recordingEVSReaper) SweepOrphanEVS(_ context.Context, ak, sk, projectID, region string,
	active map[string]struct{}, destructive bool, _ func(string)) (int, error) {
	r.called = true
	r.gotActive = active
	r.gotDestructive = destructive
	r.gotRegion = region
	return r.ret, nil
}

// TestReapDeploymentEVSBackstop_UnprotectsWipedDepProtectsNeighbors — #5028.
// The post-destroy backstop must reap the wiped deployment's now-detached
// volumes (its prefix removed from the protected set) while every OTHER live
// deployment stays protected — the fail-safe that stops it eating a live
// neighbour's momentarily-detached PVCs (the b9f9590b self-destruct class).
func TestReapDeploymentEVSBackstop_UnprotectsWipedDepProtectsNeighbors(t *testing.T) {
	h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	const wipedID = "aaaaaaaa11112222" // the dep being wiped
	const neighborID = "bbbbbbbb33334444"
	h.deployments.Store(wipedID, &Deployment{ID: wipedID, Status: "wiping"})
	h.deployments.Store(neighborID, &Deployment{ID: neighborID, Status: "ready"})

	reaper := &recordingEVSReaper{ret: 7}
	creds := huaweiSweepCreds{AK: "ak", SK: "sk", ProjectID: "proj", Region: "me-east-215"}

	got := h.reapDeploymentEVSBackstop(context.Background(), reaper, wipedID, creds, nil)

	if !reaper.called {
		t.Fatal("wipe path did NOT invoke the EVS backstop reap (#5028 — orphaned pvc-* volumes never reaped)")
	}
	if !reaper.gotDestructive {
		t.Fatal("backstop must run destructive=true — the operator explicitly asked to wipe")
	}
	if got != 7 {
		t.Fatalf("reaped count = %d, want 7 (return value not propagated)", got)
	}
	if _, protected := reaper.gotActive[neighborID[:8]]; !protected {
		t.Fatalf("neighbour dep %q was NOT protected — the backstop could reap a live env's detached PVCs", neighborID[:8])
	}
	if _, stillProtected := reaper.gotActive[wipedID[:8]]; stillProtected {
		t.Fatalf("wiped dep %q is STILL protected — its orphaned volumes would never be reaped (the #5028 leak)", wipedID[:8])
	}
	if reaper.gotRegion != "me-east-215" {
		t.Fatalf("region = %q, want me-east-215", reaper.gotRegion)
	}
}

// TestReapDeploymentEVSBackstop_NoOpOnMissingCredsOrReaper guards the
// best-effort contract: a nil reaper or incomplete creds must no-op (never
// panic, never a spurious reap).
func TestReapDeploymentEVSBackstop_NoOpOnMissingCredsOrReaper(t *testing.T) {
	h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	full := huaweiSweepCreds{AK: "ak", SK: "sk", ProjectID: "proj", Region: "r"}

	if n := h.reapDeploymentEVSBackstop(context.Background(), nil, "id0", full, nil); n != 0 {
		t.Fatalf("nil reaper must no-op, reaped %d", n)
	}

	reaper := &recordingEVSReaper{ret: 3}
	if n := h.reapDeploymentEVSBackstop(context.Background(), reaper, "id0", huaweiSweepCreds{AK: "ak"}, nil); n != 0 || reaper.called {
		t.Fatalf("incomplete creds must no-op (reaped=%d called=%v)", n, reaper.called)
	}
}

// TestHuaweiSweepCredsFromRaw locks the in-band cred source the backstop uses
// (buildWipeCredsRaw output) + the region default.
func TestHuaweiSweepCredsFromRaw(t *testing.T) {
	// Complete triple + explicit region.
	if c, ok := huaweiSweepCredsFromRaw(map[string]string{
		"access_key": "ak", "secret_key": "sk", "project_id": "proj", "region": "me-east-999",
	}); !ok || c.AK != "ak" || c.SK != "sk" || c.ProjectID != "proj" || c.Region != "me-east-999" {
		t.Fatalf("complete creds mis-parsed: %+v ok=%v", c, ok)
	}
	// Missing region → default.
	if c, ok := huaweiSweepCredsFromRaw(map[string]string{
		"access_key": "ak", "secret_key": "sk", "project_id": "proj",
	}); !ok || c.Region != "me-east-215" {
		t.Fatalf("region default not applied: %+v ok=%v", c, ok)
	}
	// Incomplete triple → not ok (a non-huawei wipe's creds map).
	if _, ok := huaweiSweepCredsFromRaw(map[string]string{"hcloud_token": "x"}); ok {
		t.Fatal("hetzner creds map must not yield huawei sweep creds")
	}
}
