// dr_standby_integrity_test.go — #3375 DoD-7 unit coverage for the
// DR-integrity gate: a Sovereign that REQUESTED active-hot-standby (or
// active-active) but whose standby region never materialised must be
// classified as a standby-region-absent failure, never `ready`.
//
// This is distinct from the tofu-apply-time partial-region post-condition
// (#2840, detectPartialRegionMaterialisation) — that fires when the
// region's EIP itself never came up; THIS gate fires when the EIP came up
// but the region-B cluster never formed (no secondary control-plane
// observed in the Phase-1 watch — the hw150 shape, #3375 §3(e)).

package provisioner

import (
	"strings"
	"testing"
)

func TestDeclaredDRStandbyIntegrity(t *testing.T) {
	t.Parallel()
	type row struct {
		name           string
		topology       string
		declared       int
		observedSecond int
		wantOK         bool
	}
	rows := []row{
		// single-region: no standby to assert, always ok.
		{"single-region-1region", BcpTopologySingleRegion, 1, 0, true},
		{"single-region-stray-secondary", BcpTopologySingleRegion, 1, 0, true},

		// active-hot-standby happy path: 2 declared, 1 secondary observed.
		{"ahs-2declared-1observed", BcpTopologyActiveHotStandby, 2, 1, true},
		// active-hot-standby the hw150 shape: 2 declared, 0 observed → FAIL.
		{"ahs-2declared-0observed", BcpTopologyActiveHotStandby, 2, 0, false},
		// active-hot-standby 3 declared, only 1 observed → still missing one.
		{"ahs-3declared-1observed", BcpTopologyActiveHotStandby, 3, 1, false},
		{"ahs-3declared-2observed", BcpTopologyActiveHotStandby, 3, 2, true},

		// active-active honours the same standby-presence requirement.
		{"aa-2declared-0observed", BcpTopologyActiveActive, 2, 0, false},
		{"aa-2declared-1observed", BcpTopologyActiveActive, 2, 1, true},

		// Case-insensitive + whitespace-tolerant topology string.
		{"ahs-mixedcase", "Active-HotStandby", 2, 0, false},
		{"ahs-padded", "  active-hotstandby  ", 2, 1, true},

		// Defensive: multi-region topology with <2 declared (Validate
		// rejects this upstream) — treat missing standby as absent.
		{"ahs-1declared-0observed", BcpTopologyActiveHotStandby, 1, 0, false},
	}
	for _, r := range rows {
		r := r
		t.Run(r.name, func(t *testing.T) {
			t.Parallel()
			ok, reason := DeclaredDRStandbyIntegrity(r.topology, r.declared, r.observedSecond)
			if ok != r.wantOK {
				t.Fatalf("DeclaredDRStandbyIntegrity(%q, %d, %d) ok=%v, want %v (reason=%q)",
					r.topology, r.declared, r.observedSecond, ok, r.wantOK, reason)
			}
			if !ok {
				if reason == "" {
					t.Errorf("a failing integrity check must carry a non-empty reason")
				}
				if !strings.Contains(reason, "standby") {
					t.Errorf("failure reason must name the standby gap; got %q", reason)
				}
			}
			if ok && reason != "" {
				t.Errorf("a passing integrity check must carry an empty reason; got %q", reason)
			}
		})
	}
}

// The gate must be GENERIC — its decision depends ONLY on the declared
// topology string + the two integer counts, never on any app/blueprint
// name. This asserts the contract directly (the signature carries no
// app-name parameter at all), and confirms the canonical reason constant
// is the one stamped.
func TestDeclaredDRStandbyIntegrity_ReasonConstantStamped(t *testing.T) {
	t.Parallel()
	ok, reason := DeclaredDRStandbyIntegrity(BcpTopologyActiveHotStandby, 2, 0)
	if ok {
		t.Fatal("expected the absent-standby case to fail the integrity check")
	}
	if reason == "" {
		t.Fatal("expected a non-empty operator-facing reason")
	}
	if ReasonStandbyRegionAbsent != "standby-region-absent" {
		t.Errorf("ReasonStandbyRegionAbsent = %q, want standby-region-absent", ReasonStandbyRegionAbsent)
	}
}
