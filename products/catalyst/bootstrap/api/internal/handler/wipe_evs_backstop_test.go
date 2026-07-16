package handler

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// writeTfvars writes a tofu.auto.tfvars.json with the given Huawei creds into
// dir and returns dir (the per-deployment workdir the fallback reads).
func writeTfvars(t *testing.T, dir, ak, sk, projectID, region string) string {
	t.Helper()
	body := `{`
	body += `"huawei_access_key":"` + ak + `",`
	body += `"huawei_secret_key":"` + sk + `",`
	body += `"huawei_project_id":"` + projectID + `",`
	body += `"huawei_region":"` + region + `"`
	body += `}`
	if err := os.WriteFile(filepath.Join(dir, "tofu.auto.tfvars.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write tfvars: %v", err)
	}
	return dir
}

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

// TestApplyWorkdirEVSCredsFallback_BodylessWipeAfterPodRoll — #5135. On the
// canonical body-less wipe after a catalyst-api Pod roll, buildWipeCredsRaw
// yields an empty huawei creds map (dep.Request secrets are gone). Without the
// fallback, huaweiSweepCredsFromRaw returns ok=false and the #5028 EVS backstop
// silently skips — stranding CSI pvc-* volumes toward the HCS quota. The
// fallback must recover AK/SK/project_id/region from the workdir tfvars so the
// backstop is invoked with non-empty creds.
func TestApplyWorkdirEVSCredsFallback_BodylessWipeAfterPodRoll(t *testing.T) {
	workdir := writeTfvars(t, t.TempDir(), "ak-disk", "sk-disk", "proj-disk", "me-east-777")

	// Body-less wipe + Pod-rolled dep.Request (no huawei secrets) → all empty.
	credsRaw := buildWipeCredsRaw("huawei", wipeRequest{}, provisioner.Request{})

	// PROVE THE WEDGE: before the fallback the backstop would skip.
	if _, ok := huaweiSweepCredsFromRaw(credsRaw); ok {
		t.Fatal("precondition wrong: creds resolved without the fallback — test would not exercise #5135")
	}

	if !applyWorkdirEVSCredsFallback("huawei", credsRaw, workdir) {
		t.Fatal("fallback did NOT fire on empty creds with a valid workdir tfvars (#5135 — backstop would silently skip)")
	}

	sweep, ok := huaweiSweepCredsFromRaw(credsRaw)
	if !ok {
		t.Fatal("creds still incomplete after the workdir fallback — huaweiSweepCredsFromRaw ok=false")
	}
	if sweep.AK != "ak-disk" || sweep.SK != "sk-disk" || sweep.ProjectID != "proj-disk" || sweep.Region != "me-east-777" {
		t.Fatalf("recovered creds wrong: %+v", sweep)
	}

	// End-to-end wiring: the recovered creds must actually drive the reaper.
	h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	reaper := &recordingEVSReaper{ret: 4}
	if got := h.reapDeploymentEVSBackstop(context.Background(), reaper, "cccccccc55556666", sweep, nil); got != 4 {
		t.Fatalf("reaped = %d, want 4", got)
	}
	if !reaper.called {
		t.Fatal("EVS backstop reaper NOT invoked with the workdir-recovered creds (#5135)")
	}
	if reaper.gotRegion != "me-east-777" {
		t.Fatalf("region = %q, want me-east-777", reaper.gotRegion)
	}
}

// TestApplyWorkdirEVSCredsFallback_BodyCredsWin — a wipe whose body carries the
// creds must NOT be overwritten by the workdir tfvars. Only empty keys fill.
func TestApplyWorkdirEVSCredsFallback_BodyCredsWin(t *testing.T) {
	workdir := writeTfvars(t, t.TempDir(), "ak-disk", "sk-disk", "proj-disk", "me-east-777")

	credsRaw := buildWipeCredsRaw("huawei", wipeRequest{
		HuaweiAccessKey: "ak-body", HuaweiSecretKey: "sk-body",
		HuaweiProjectID: "proj-body", HuaweiRegion: "me-east-body",
	}, provisioner.Request{})

	if applyWorkdirEVSCredsFallback("huawei", credsRaw, workdir) {
		t.Fatal("fallback fired even though body creds were complete — it must not overwrite operator-supplied values")
	}
	if credsRaw["access_key"] != "ak-body" || credsRaw["region"] != "me-east-body" {
		t.Fatalf("body creds were clobbered: %+v", credsRaw)
	}
}

// TestApplyWorkdirEVSCredsFallback_NonHuaweiAndMissingTfvars guards the honest
// skips: a mothership/hetzner wipe and a huawei wipe whose workdir has no
// tfvars must both no-op (no panic, no spurious backstop invocation).
func TestApplyWorkdirEVSCredsFallback_NonHuaweiAndMissingTfvars(t *testing.T) {
	// Non-huawei: even WITH a valid tfvars on disk, a hetzner wipe never fills.
	workdir := writeTfvars(t, t.TempDir(), "ak-disk", "sk-disk", "proj-disk", "me-east-777")
	hetznerCreds := buildWipeCredsRaw("hetzner", wipeRequest{}, provisioner.Request{})
	if applyWorkdirEVSCredsFallback("hetzner", hetznerCreds, workdir) {
		t.Fatal("hetzner wipe must not harvest huawei tfvars creds")
	}

	// Huawei but missing tfvars → honest skip; backstop stays un-invocable.
	empty := t.TempDir() // no tofu.auto.tfvars.json written
	huaweiCreds := buildWipeCredsRaw("huawei", wipeRequest{}, provisioner.Request{})
	if applyWorkdirEVSCredsFallback("huawei", huaweiCreds, empty) {
		t.Fatal("missing tfvars must yield a no-op, not a filled creds map")
	}
	if _, ok := huaweiSweepCredsFromRaw(huaweiCreds); ok {
		t.Fatal("creds must stay incomplete when the workdir tfvars is absent — the backstop honestly skips")
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
