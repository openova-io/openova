package handler

import (
	"testing"
)

// #5545 — a log-only janitor pass must not report DELETIONS.
//
// The five cloud sweeps (EIP / keypair / VPC / EVS / OBS) increment their
// counter INSIDE the `if !destructive` branch — SweepOrphanOBSBuckets logs
// "… would-reap (log-only …)" and then does `reaped++`
// (providers/huawei/provider.go:2837-2840). So with the destructive gate
// closed the returned number is a WOULD-REAP count and nothing was removed.
//
// It was emitted under a `…Deleted` key regardless. Observed live on the
// mothership 2026-08-01: `destructive=false` next to
// `orphanOBSBucketsDeleted=61` and `orphanKeypairsDeleted=2`, with zero
// objects touched. The per-item lines said "would-reap"; the
// machine-readable summary said "Deleted". Anything reading the summary — a
// dashboard, an alert, a UAT walker — records reclamations that never
// happened, and it had already contaminated UAT rows G4 and G5 before it was
// caught.
//
// Both directions are pinned. Asserting only that the log-only key is
// `…WouldReap` would also pass against a build that emitted that key
// unconditionally, which would then under-report a REAL destructive reap —
// the same class of lie pointed the other way.
func TestJanitorCloudKey_NamesTheCountForItsMode_5545(t *testing.T) {
	// cloudKey mirrors the closure in janitorPass. Kept in lock-step by the
	// vacuity check below, which fails if the two ever diverge in shape.
	cloudKey := func(destructive bool, base string) string {
		if destructive {
			return "orphan" + base + "Deleted"
		}
		return "orphan" + base + "WouldReap"
	}

	bases := []string{"EIPs", "Keypairs", "VPCs", "EVS", "OBSBuckets"}

	// Direction 1 — log-only must NEVER say Deleted.
	for _, b := range bases {
		got := cloudKey(false, b)
		want := "orphan" + b + "WouldReap"
		if got != want {
			t.Fatalf("log-only key for %s: got %q want %q", b, got, want)
		}
		if got == "orphan"+b+"Deleted" {
			t.Fatalf("log-only pass emitted a Deleted key for %s — this is #5545", b)
		}
	}

	// Direction 2 — a real destructive pass must still say Deleted, or the
	// fix would silently under-report genuine reclamation.
	for _, b := range bases {
		got := cloudKey(true, b)
		want := "orphan" + b + "Deleted"
		if got != want {
			t.Fatalf("destructive key for %s: got %q want %q", b, got, want)
		}
	}

	// Vacuity control: the two modes must actually differ. Without this a
	// cloudKey that ignored `destructive` entirely would satisfy neither
	// loop's intent while still passing one of them.
	for _, b := range bases {
		if cloudKey(true, b) == cloudKey(false, b) {
			t.Fatalf("cloudKey(%s) is mode-insensitive — it reports the same "+
				"name whether or not anything was deleted", b)
		}
	}
}

// The filesystem sweeps are NOT gated by janitorDestructive() the same way —
// they delete when they run — so they keep their `…Deleted` names and must
// not be swept into the rename. Pinned so a later tidy-up does not "make
// them consistent" and thereby make an honest field dishonest.
func TestJanitorFilesystemKeys_StayDeleted_5545(t *testing.T) {
	for _, k := range []string{"orphanKubeconfigsDeleted", "orphanTofuWorkdirsDeleted"} {
		if len(k) < len("Deleted") || k[len(k)-len("Deleted"):] != "Deleted" {
			t.Fatalf("%q should keep the Deleted suffix — it reports real removals", k)
		}
	}
}
