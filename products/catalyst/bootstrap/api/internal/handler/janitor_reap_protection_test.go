package handler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestBuildActivePrefixes_ProtectsEveryNonWipedStatus — #4454 regression.
//
// The orphan sweeps (EIP / keypair / VPC) reclaim cloud infra whose
// 8-char deployment-ID prefix is NOT in buildActivePrefixes's protected
// set. The original ALLOWLIST protected only in-flight statuses and left
// `ready` (and failed / adopted / cutover-*) reclaimable — so the instant
// a fresh Sovereign flipped to `ready` the janitor reaped its EIP /
// keypair / VPC-peering / VPC and cascaded the node deletion (dep
// b9f9590b, omantel.biz: all 12 ECS nodes DELETED ~2.5 min after
// convergence).
//
// The fix is a DENYLIST: protect every non-`wiped` record (fail-safe),
// reclaim ONLY genuinely-wiped records' leaked infra. This test asserts
// that contract for one record in EACH status, with `ready` (the exact
// production failure) called out explicitly. It FAILS on the old
// allowlist code (ready/failed/adopted/cutover-* unprotected) and PASSES
// on the inverted code.
func TestBuildActivePrefixes_ProtectsEveryNonWipedStatus(t *testing.T) {
	h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	// One record per status. IDs are distinct 16-hex so their 8-char
	// prefixes don't collide — each prefix's presence/absence is a clean
	// per-status signal.
	type rec struct {
		id        string
		status    string
		protected bool // expected: should this prefix be in the set?
	}
	records := []rec{
		{"a0000000pending1", "pending", true},
		{"a1111111provisn1", "provisioning", true},
		{"a2222222tofuappl", "tofu-applying", true},
		{"a3333333fluxboot", "flux-bootstrapping", true},
		{"a4444444phase1wt", "phase1-watching", true},
		{"a5555555readydep", "ready", true}, // the exact b9f9590b production failure
		{"a6666666adopted1", "adopted", true},
		{"a7777777cutoverr", "cutover-running", true},
		{"a8888888cutoverc", "cutover-complete", true},
		{"a9999999faileddp", "failed", true}, // DEBUG-BEFORE-WIPE: protect failed infra
		{"aaaaaaaawipingdp", "wiping", true},
		{"abbbbbbbwipeddpx", "wiped", false}, // wiped → reclaimable
	}

	for _, r := range records {
		h.deployments.Store(r.id, &Deployment{ID: r.id, Status: r.status})
	}

	got := h.buildActivePrefixes()

	for _, r := range records {
		prefix := r.id[:8]
		_, inSet := got[prefix]
		if r.protected && !inSet {
			t.Errorf("status %q (id %s): prefix %q MUST be protected but was reclaimable — its cloud infra would be reaped",
				r.status, r.id, prefix)
		}
		if !r.protected && inSet {
			t.Errorf("status %q (id %s): prefix %q should be reclaimable but was protected",
				r.status, r.id, prefix)
		}
	}

	// Explicit, named assertion on the exact production failure so a
	// regression here reads unambiguously in CI output.
	if _, ok := got["a5555555"]; !ok {
		t.Fatalf("REGRESSION #4454: a `ready` deployment's prefix is NOT protected — the janitor will reap its EIP/keypair/VPC and self-destruct the fresh Sovereign ~2.5 min after convergence")
	}

	// And the inverse: a wiped record must NOT pin its (already-gone)
	// infra, otherwise genuine leaks accumulate to project quota.
	if _, ok := got["abbbbbbb"]; ok {
		t.Fatalf("a `wiped` deployment's prefix is protected — genuine leaked infra would never be reclaimed, piling up against the project quota")
	}
}

// TestBuildActivePrefixes_UnknownFutureStatusFailsSafe — #4454. The whole
// point of the inversion is that a status nobody has added yet still
// protects its infra. Assert a made-up future status is protected (fails
// SAFE), so the next state added to the lifecycle can never silently
// reintroduce the self-destruct.
func TestBuildActivePrefixes_UnknownFutureStatusFailsSafe(t *testing.T) {
	h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h.deployments.Store("deadbeeffuturex", &Deployment{ID: "deadbeeffuturex", Status: "some-future-status-2027"})

	got := h.buildActivePrefixes()
	if _, ok := got["deadbeef"]; !ok {
		t.Fatalf("an unrecognised future status was NOT protected — the inversion must fail SAFE so a new lifecycle state can't reintroduce the b9f9590b self-destruct")
	}
}

// TestJanitorDestructive_DefaultsFalse — #4466. The cloud-resource sweeps
// must default to LOG-ONLY. "deletes prod infra" is opt-in.
func TestJanitorDestructive_DefaultsFalse(t *testing.T) {
	t.Setenv("CATALYST_JANITOR_DESTRUCTIVE", "")
	if janitorDestructive() {
		t.Fatal("janitorDestructive() must default FALSE (log-only) when the env is unset")
	}
	t.Setenv("CATALYST_JANITOR_DESTRUCTIVE", "false")
	if janitorDestructive() {
		t.Fatal("janitorDestructive() must be FALSE for explicit =false")
	}
	t.Setenv("CATALYST_JANITOR_DESTRUCTIVE", "TRUE")
	if !janitorDestructive() {
		t.Fatal("janitorDestructive() must be TRUE for =TRUE (case-insensitive opt-in)")
	}
}

// TestJanitorExtraProtectedPrefixes — #4466 explicit active-dep allowlist.
// Full IDs and 8-char prefixes both collapse to the 8-char protected key.
func TestJanitorExtraProtectedPrefixes(t *testing.T) {
	t.Setenv("CATALYST_JANITOR_ACTIVE_DEPLOYMENT_IDS", "b9f9590b558262b6, deadbeef ffffffffaaaa")
	got := janitorExtraProtectedPrefixes()
	for _, want := range []string{"b9f9590b", "deadbeef", "ffffffff"} {
		if _, ok := got[want]; !ok {
			t.Fatalf("prefix %q missing from explicit allowlist %v", want, got)
		}
	}
	t.Setenv("CATALYST_JANITOR_ACTIVE_DEPLOYMENT_IDS", "")
	if len(janitorExtraProtectedPrefixes()) != 0 {
		t.Fatal("empty env must yield no explicit prefixes")
	}
}

// TestBuildActivePrefixes_MergesExplicitAllowlist — #4466 belt-and-
// suspenders. Even when NO in-memory deployment carries a prefix (the worst
// case: status inference fully regressed / records lost), the explicitly-
// named live deployment id is still hard-protected.
func TestBuildActivePrefixes_MergesExplicitAllowlist(t *testing.T) {
	t.Setenv("CATALYST_JANITOR_ACTIVE_DEPLOYMENT_IDS", "b9f9590b558262b6")
	h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	// No deployments stored at all — only the explicit allowlist should
	// drive protection.
	got := h.buildActivePrefixes()
	if _, ok := got["b9f9590b"]; !ok {
		t.Fatal("explicit active-deployment id was NOT protected — the belt-and-suspenders allowlist must hold even with zero in-memory records")
	}
}

// TestDiscoverHuaweiCreds_EnvFallback — #4466 cred-source gap. After the
// last wipe deletes its own tofu.auto.tfvars.json the tfvars walk finds
// nothing; the sweep must still run by falling back to the
// huawei-operator-creds Secret env. With an EMPTY tofu dir + the env set,
// discovery must succeed.
func TestDiscoverHuaweiCreds_EnvFallback(t *testing.T) {
	emptyTofu := t.TempDir() // no per-deployment tfvars at all (post-wipe)
	t.Setenv("CATALYST_HUAWEI_ACCESS_KEY", "ak-env")
	t.Setenv("CATALYST_HUAWEI_SECRET_KEY", "sk-env")
	t.Setenv("CATALYST_HUAWEI_PROJECT_ID", "proj-env")
	t.Setenv("CATALYST_HUAWEI_REGION", "")

	c, ok := discoverHuaweiCreds(emptyTofu)
	if !ok {
		t.Fatal("post-wipe sweep must fall back to huawei-operator-creds env when no tfvars survive — got ok=false (the 'no huawei deployments' short-circuit that strands orphans)")
	}
	if c.AK != "ak-env" || c.SK != "sk-env" || c.ProjectID != "proj-env" {
		t.Fatalf("env-fallback creds wrong: %+v", c)
	}
	if c.Region != "me-east-215" {
		t.Fatalf("empty region must default to me-east-215, got %q", c.Region)
	}
}

// TestDiscoverHuaweiCreds_TfvarsWins — when a per-deployment tfvars DOES
// carry creds it is used in preference to the env (the normal path).
func TestDiscoverHuaweiCreds_TfvarsWins(t *testing.T) {
	dir := t.TempDir()
	depDir := filepath.Join(dir, "dep1")
	if err := os.MkdirAll(depDir, 0o755); err != nil {
		t.Fatal(err)
	}
	tf := `{"huawei_access_key":"ak-tfvars","huawei_secret_key":"sk-tfvars","huawei_project_id":"proj-tfvars","huawei_region":"me-east-215"}`
	if err := os.WriteFile(filepath.Join(depDir, "tofu.auto.tfvars.json"), []byte(tf), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CATALYST_HUAWEI_ACCESS_KEY", "ak-env-should-not-win")

	c, ok := discoverHuaweiCreds(dir)
	if !ok || c.AK != "ak-tfvars" {
		t.Fatalf("tfvars creds must win over env: ok=%v ak=%q", ok, c.AK)
	}
}

// TestDiscoverHuaweiCreds_NoneAvailable — both sources empty → ok=false (a
// genuine no-Huawei mothership; the sweep correctly no-ops).
func TestDiscoverHuaweiCreds_NoneAvailable(t *testing.T) {
	t.Setenv("CATALYST_HUAWEI_ACCESS_KEY", "")
	t.Setenv("CATALYST_HUAWEI_SECRET_KEY", "")
	t.Setenv("CATALYST_HUAWEI_PROJECT_ID", "")
	if _, ok := discoverHuaweiCreds(t.TempDir()); ok {
		t.Fatal("with neither tfvars nor env creds, discovery must report ok=false")
	}
}

// ---------------------------------------------------------------------------
// #5327 — the Step-1 "failed-too-long" reap must preserve the destroy
// capability (record + tofu state + kubeconfigs) while the deployment's cloud
// infra is still alive.
//
// hw284 (dep 383db23ccaf13be5): phase-1 failure marked the record `failed`
// and — by design (DEBUG-BEFORE-WIPE) — left the full 2-region Huawei infra
// alive. 24h later the ungated reap deleted the record json, the tofu workdir
// INCLUDING STATE, and the kubeconfigs WITHOUT any cloud destroy. The env
// then ran orphaned-alive for ~40h: the canonical wipe endpoint 404'd (record
// gone) and tofu could no longer destroy (state gone) — teardown needed a raw
// Huawei-API break-glass (#5328/PR #5329). These tests pin the gate contract:
//
//	failed + infra ALIVE + log-only     → record & workdir & kubeconfigs preserved
//	failed + infra ALIVE + destructive  → canonical destroy first, reap only
//	                                      after the verified-gone re-probe
//	failed + infra GONE                 → reaped exactly as before
//	probe error                         → preserved (fail-closed)
// ---------------------------------------------------------------------------

// failedReapFixture seeds one deployment record (default status "failed",
// FinishedAt 25h ago — past the 24h failedMaxAge) plus the on-disk artifacts
// reapDeployment would delete: the tofu workdir with a state marker (the
// destroy capability), the primary kubeconfig, and one secondary-region
// kubeconfig.
type failedReapFixture struct {
	h     *Handler
	work  string
	kcDir string
	id    string
}

func makeFailedReapFixture(t *testing.T, id, status string) *failedReapFixture {
	t.Helper()
	work := t.TempDir()
	kcDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, id), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, id, "terraform.tfstate"), []byte(`{"resources":[{"mode":"managed"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kcDir, id+".yaml"), []byte("kubeconfig"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kcDir, id+"-region-b.yaml"), []byte("kubeconfig-b"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &Handler{
		log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		kubeconfigsDir: kcDir,
	}
	h.deployments.Store(id, &Deployment{
		ID:         id,
		Status:     status,
		StartedAt:  time.Now().Add(-26 * time.Hour),
		FinishedAt: time.Now().Add(-25 * time.Hour),
	})
	// Keep the pass's cloud sweeps + cred discovery inert: no tfvars in the
	// workdir (only a bare tfstate marker) and no operator-env creds, so
	// discoverHuaweiCreds reports ok=false and no sweep ever dials out.
	t.Setenv("CATALYST_HUAWEI_ACCESS_KEY", "")
	t.Setenv("CATALYST_HUAWEI_SECRET_KEY", "")
	t.Setenv("CATALYST_HUAWEI_PROJECT_ID", "")
	return &failedReapFixture{h: h, work: work, kcDir: kcDir, id: id}
}

func (f *failedReapFixture) runPass() {
	f.h.runJanitorPass(f.work, 24*time.Hour, time.Hour)
}

func (f *failedReapFixture) recordPresent() bool {
	_, ok := f.h.deployments.Load(f.id)
	return ok
}

func (f *failedReapFixture) tofuStatePresent(t *testing.T) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(f.work, f.id, "terraform.tfstate"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("stat tofu state: %v", err)
	}
	return err == nil
}

func (f *failedReapFixture) kubeconfigsPresent(t *testing.T) bool {
	t.Helper()
	_, perr := os.Stat(filepath.Join(f.kcDir, f.id+".yaml"))
	_, serr := os.Stat(filepath.Join(f.kcDir, f.id+"-region-b.yaml"))
	return perr == nil && serr == nil
}

// TestGateFailedReap_InfraAliveLogOnly_PreservesDestroyCapability — the exact
// hw284 shape: failed record past failedMaxAge, cloud infra ALIVE, janitor in
// its mandated log-only default. The pass must preserve record + tofu state +
// kubeconfigs, never invoke a destroy, and keep re-evaluating on later passes.
func TestGateFailedReap_InfraAliveLogOnly_PreservesDestroyCapability(t *testing.T) {
	t.Setenv("CATALYST_JANITOR_DESTRUCTIVE", "")
	f := makeFailedReapFixture(t, "383db23ccaf13be5", "failed")

	probeCalls, destroyCalls := 0, 0
	f.h.janitorFailedInfraProbe = func(ctx context.Context, dep *Deployment, tofuWorkDir string) (bool, error) {
		probeCalls++
		return true, nil // infra ALIVE
	}
	f.h.janitorFailedDestroy = func(ctx context.Context, dep *Deployment, tofuWorkDir string) error {
		destroyCalls++
		return nil
	}

	// Two passes — the skip must be a re-evaluate-next-pass, not a one-off.
	f.runPass()
	f.runPass()

	if destroyCalls != 0 {
		t.Fatalf("log-only janitor must NEVER destroy cloud infra, destroy ran %d time(s)", destroyCalls)
	}
	if probeCalls != 2 {
		t.Fatalf("expected the infra probe once per pass (2), got %d", probeCalls)
	}
	if !f.recordPresent() {
		t.Fatal("REGRESSION #5327: failed record with LIVE infra was reaped — the canonical wipe endpoint would 404 on a still-alive env")
	}
	if !f.tofuStatePresent(t) {
		t.Fatal("REGRESSION #5327: tofu state (the ONLY destroy capability) was deleted while the cloud infra is alive")
	}
	if !f.kubeconfigsPresent(t) {
		t.Fatal("REGRESSION #5327: kubeconfigs deleted while the cloud infra is alive")
	}
}

// TestGateFailedReap_InfraAliveDestructive_DestroysThenReaps — with
// CATALYST_JANITOR_DESTRUCTIVE=true the gate runs the canonical cloud destroy
// first (the same providers.CloudProvider.Wipe steps the wipe handler
// dispatches), re-probes, and only reaps a verified-gone footprint.
func TestGateFailedReap_InfraAliveDestructive_DestroysThenReaps(t *testing.T) {
	t.Setenv("CATALYST_JANITOR_DESTRUCTIVE", "true")
	f := makeFailedReapFixture(t, "aa5327dstry00001", "failed")

	destroyCalls := 0
	infraGone := false
	f.h.janitorFailedInfraProbe = func(ctx context.Context, dep *Deployment, tofuWorkDir string) (bool, error) {
		return !infraGone, nil
	}
	f.h.janitorFailedDestroy = func(ctx context.Context, dep *Deployment, tofuWorkDir string) error {
		destroyCalls++
		// The destroy must run while the destroy capability still exists.
		if _, err := os.Stat(filepath.Join(tofuWorkDir, dep.ID, "terraform.tfstate")); err != nil {
			t.Errorf("destroy invoked AFTER the tofu state was deleted: %v", err)
		}
		infraGone = true
		return nil
	}

	f.runPass()

	if destroyCalls != 1 {
		t.Fatalf("destructive mode must invoke the canonical destroy exactly once, got %d", destroyCalls)
	}
	if f.recordPresent() {
		t.Fatal("after a VERIFIED destroy the failed record must be reaped")
	}
	if f.tofuStatePresent(t) {
		t.Fatal("after a VERIFIED destroy the tofu workdir must be reaped")
	}
	if f.kubeconfigsPresent(t) {
		t.Fatal("after a VERIFIED destroy the kubeconfigs must be reaped")
	}
}

// TestGateFailedReap_InfraGone_ReapsAsToday — when the cloud footprint is
// verifiably gone the historical reap behaviour is correct local cleanup and
// must proceed unchanged (no destroy, artifacts removed). A sibling `wiped`
// record must also keep reaping WITHOUT ever consulting the infra probe.
func TestGateFailedReap_InfraGone_ReapsAsToday(t *testing.T) {
	t.Setenv("CATALYST_JANITOR_DESTRUCTIVE", "")
	f := makeFailedReapFixture(t, "bb5327gone000001", "failed")

	// Sibling wiped record — its bookkeeping reap must not touch the probe.
	f.h.deployments.Store("cc5327wiped00001", &Deployment{
		ID:         "cc5327wiped00001",
		Status:     "wiped",
		StartedAt:  time.Now().Add(-3 * time.Hour),
		FinishedAt: time.Now().Add(-2 * time.Hour),
	})

	destroyCalls := 0
	f.h.janitorFailedInfraProbe = func(ctx context.Context, dep *Deployment, tofuWorkDir string) (bool, error) {
		if dep.ID != f.id {
			t.Errorf("infra probe consulted for a %q record (id %s) — the gate is for failed-too-long targets only", dep.Status, dep.ID)
		}
		return false, nil // infra GONE
	}
	f.h.janitorFailedDestroy = func(ctx context.Context, dep *Deployment, tofuWorkDir string) error {
		destroyCalls++
		return nil
	}

	f.runPass()

	if destroyCalls != 0 {
		t.Fatalf("infra-gone reap must not destroy anything, destroy ran %d time(s)", destroyCalls)
	}
	if f.recordPresent() {
		t.Fatal("failed record with GONE infra must still be reaped (today's behaviour)")
	}
	if f.tofuStatePresent(t) {
		t.Fatal("tofu workdir of a gone-infra failed record must still be reaped")
	}
	if f.kubeconfigsPresent(t) {
		t.Fatal("kubeconfigs of a gone-infra failed record must still be reaped")
	}
	if _, ok := f.h.deployments.Load("cc5327wiped00001"); ok {
		t.Fatal("wiped-bookkeeping reap regressed — the #5327 gate must only apply to failed-too-long targets")
	}
}

// TestGateFailedReap_ProbeError_FailsClosed — a probe that cannot actually
// see the cloud (HCS API down, creds unresolvable, provider unwired) must
// PRESERVE, even with the janitor armed destructive: no destroy on
// unverifiable state, no reap.
func TestGateFailedReap_ProbeError_FailsClosed(t *testing.T) {
	t.Setenv("CATALYST_JANITOR_DESTRUCTIVE", "true")
	f := makeFailedReapFixture(t, "dd5327proberr001", "failed")

	destroyCalls := 0
	f.h.janitorFailedInfraProbe = func(ctx context.Context, dep *Deployment, tofuWorkDir string) (bool, error) {
		return false, errors.New("HCS ECS API: status 503")
	}
	f.h.janitorFailedDestroy = func(ctx context.Context, dep *Deployment, tofuWorkDir string) error {
		destroyCalls++
		return nil
	}

	f.runPass()

	if destroyCalls != 0 {
		t.Fatalf("probe-error must never authorise a destroy, destroy ran %d time(s)", destroyCalls)
	}
	if !f.recordPresent() || !f.tofuStatePresent(t) || !f.kubeconfigsPresent(t) {
		t.Fatal("REGRESSION #5327: probe error must fail CLOSED (record + tofu state + kubeconfigs preserved)")
	}
}

// TestGateFailedReap_UnverifiedDestroy_Preserves — destructive mode without a
// VERIFIED destroy must preserve: (a) the destroy itself errors, (b) the
// destroy claims success but the re-probe still sees live infra, (c) the
// re-probe errors. "Only reap after a verified destroy."
func TestGateFailedReap_UnverifiedDestroy_Preserves(t *testing.T) {
	cases := []struct {
		name       string
		destroyErr error
		// reProbe is consulted on the post-destroy verification pass.
		reProbeAlive bool
		reProbeErr   error
	}{
		{name: "destroy_errors", destroyErr: errors.New("tofu destroy: exit 1")},
		{name: "reprobe_still_alive", reProbeAlive: true},
		{name: "reprobe_errors", reProbeErr: errors.New("HCS ECS API: timeout")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CATALYST_JANITOR_DESTRUCTIVE", "true")
			f := makeFailedReapFixture(t, "ee5327unverif001", "failed")

			destroyed := false
			f.h.janitorFailedInfraProbe = func(ctx context.Context, dep *Deployment, tofuWorkDir string) (bool, error) {
				if !destroyed {
					return true, nil // pre-destroy: alive
				}
				return tc.reProbeAlive, tc.reProbeErr
			}
			f.h.janitorFailedDestroy = func(ctx context.Context, dep *Deployment, tofuWorkDir string) error {
				destroyed = true
				return tc.destroyErr
			}

			f.runPass()

			if !f.recordPresent() || !f.tofuStatePresent(t) || !f.kubeconfigsPresent(t) {
				t.Fatal("an UNVERIFIED destroy must preserve record + tofu state + kubeconfigs for the next pass / an operator wipe")
			}
		})
	}
}
