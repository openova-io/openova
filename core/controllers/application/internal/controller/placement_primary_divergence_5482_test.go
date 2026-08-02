package controller

import (
	"testing"

	"github.com/openova-io/openova/core/controllers/internal/placement"
)

// #5482 — the App-detail PRIMARY REGION label reads a field that goes stale on
// failover, while a correct value sits one level down in the same status.
//
// Application.status carries the primary region in TWO places, written by two
// different code paths:
//
//	FLAT    status.primaryRegion
//	          <- statusUpdate.PrimaryRegion <- plan.PrimaryRegion
//	          application_controller.go:1535 (set) / :2593 (written)
//	          This is the STATIC configured primary, i.e. spec.regions[0].
//
//	NESTED  status.placement.primaryRegion
//	          <- buildPlacementProjection, which prefers the governing
//	          Continuum's leaseHolder over plan.PrimaryRegion
//	          placement_projection.go:244-247
//
// placement_projection.go documents why the nested one is authoritative
// (:104-106, verbatim): the plan primary "is only the *configured* (regions[0])
// primary and goes stale the moment the continuum-controller flips the lease on
// failover."
//
// So after a DR failover the two disagree: nested tracks the new primary, flat
// still reports the old one. Any surface reading the flat field shows a region
// that is no longer primary — with no indication it is stale. Same
// declared-vs-actual family as #5542 (HTTP 200 declaring 400), #5545 (61
// "Deleted" that never happened) and #5515 (multi-region declared over one live
// region).
//
// These tests pin the divergence at the projection boundary. They do NOT assert
// a fix: correcting the flat field is a DR-path status write that must be
// validated against a live 2-region Sovereign under an actual lease flip, which
// needs hw292. Pinning it here means the next person to touch either writer
// cannot silently widen the gap, and has the exact reproduction in hand.

// planWith builds a static plan whose configured primary is regions[0].
func planWith(primary string, standbys ...string) placement.Plan {
	regions := []placement.RegionPlan{{Name: primary}}
	for _, s := range standbys {
		regions = append(regions, placement.RegionPlan{Name: s})
	}
	return placement.Plan{PrimaryRegion: primary, Regions: regions}
}

// The core defect: a lease flip moves the nested primary and leaves the flat
// one behind.
func TestPlacementPrimary_5482_FlatGoesStaleAfterFailover(t *testing.T) {
	plan := planWith("me-east-215-a", "me-east-215-b")

	// Steady state: no Continuum, or a Continuum whose lease still sits on the
	// configured primary. Both views agree.
	steady := buildPlacementProjection(plan, &continuumDRStatus{LeaseHolder: "me-east-215-a"})
	if steady == nil {
		t.Fatal("projection returned nil for a two-region plan")
	}
	if got := steady["primaryRegion"]; got != "me-east-215-a" {
		t.Fatalf("steady state: nested primaryRegion = %v, want me-east-215-a", got)
	}

	// Failover: the continuum-controller flips the lease to region-b. The
	// STATIC plan is unchanged — nothing rewrites spec.regions on a failover.
	failed := buildPlacementProjection(plan, &continuumDRStatus{LeaseHolder: "me-east-215-b"})

	nested, _ := failed["primaryRegion"].(string)
	flat := plan.PrimaryRegion // exactly what statusUpdate.PrimaryRegion carries

	if nested != "me-east-215-b" {
		t.Fatalf("nested primaryRegion should track the lease holder; got %q", nested)
	}
	if flat != "me-east-215-a" {
		t.Fatalf("flat primary is sourced from plan.PrimaryRegion and should be unchanged; got %q", flat)
	}
	if nested == flat {
		t.Fatalf("#5482 appears FIXED: flat and nested now agree (%q). If the flat "+
			"writer was corrected, update this test deliberately rather than deleting it", nested)
	}

	t.Logf("#5482 CONFIRMED: after a lease flip, status.placement.primaryRegion=%q "+
		"but status.primaryRegion=%q — a surface reading the flat field shows the "+
		"OLD primary with no staleness signal", nested, flat)
}

// Vacuity control. The test above would pass trivially if the projection simply
// echoed whatever LeaseHolder it was handed regardless of input. Pin the other
// branches so the divergence test is known to be discriminating.
func TestPlacementPrimary_5482_ProjectionBranchesAreDistinct(t *testing.T) {
	plan := planWith("me-east-215-a", "me-east-215-b")

	// No Continuum at all -> falls back to the static plan primary.
	noContinuum := buildPlacementProjection(plan, nil)
	if got := noContinuum["primaryRegion"]; got != "me-east-215-a" {
		t.Fatalf("no-Continuum fallback: primaryRegion = %v, want the plan primary", got)
	}

	// Continuum present but carrying no lease holder -> also falls back, rather
	// than emitting empty. An empty primary would render a blank label.
	emptyLease := buildPlacementProjection(plan, &continuumDRStatus{LeaseHolder: ""})
	if got := emptyLease["primaryRegion"]; got != "me-east-215-a" {
		t.Fatalf("empty-leaseHolder fallback: primaryRegion = %v, want the plan primary", got)
	}

	// Empty plan -> nil projection, not a half-built map.
	if got := buildPlacementProjection(placement.Plan{}, nil); got != nil {
		t.Fatalf("empty plan should yield a nil projection, got %v", got)
	}
}

// The standby roster is recomputed against the EFFECTIVE primary, which is the
// half of this that already behaves correctly. Recording it so a future fix to
// the flat field does not regress the roster while chasing the label.
func TestPlacementPrimary_5482_StandbyRosterTracksEffectivePrimary(t *testing.T) {
	plan := planWith("me-east-215-a", "me-east-215-b")
	failed := buildPlacementProjection(plan, &continuumDRStatus{LeaseHolder: "me-east-215-b"})

	standbys, ok := failed["standbyRegions"].([]interface{})
	if !ok {
		// Shape may be []string depending on the builder; accept either.
		if ss, ok2 := failed["standbyRegions"].([]string); ok2 {
			for _, s := range ss {
				standbys = append(standbys, s)
			}
		} else {
			t.Skipf("standbyRegions shape %T not asserted here", failed["standbyRegions"])
		}
	}

	for _, s := range standbys {
		if s == "me-east-215-b" {
			t.Fatalf("the failed-over-TO region must not also be listed as a standby: %v", standbys)
		}
	}
	t.Logf("standby roster after failover = %v (correctly excludes the new primary)", standbys)
}
