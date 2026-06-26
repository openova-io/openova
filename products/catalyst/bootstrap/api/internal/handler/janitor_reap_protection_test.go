package handler

import (
	"io"
	"log/slog"
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
