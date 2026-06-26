package handler

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
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
