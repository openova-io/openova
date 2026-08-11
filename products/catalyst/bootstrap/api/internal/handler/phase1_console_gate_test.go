package handler

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// TestMarkPhase1DoneConsoleGate locks in the #5253 contract for the #4706
// console-reachability signal (family #4486):
//
//   - A converged PRIMARY (OutcomeReady) whose console probe FAILS stays
//     Status="ready" and STILL fires the producer chain (fireHandover +
//     the mesh/spine/adoption/policy hooks) — the console condition rides
//     the NON-FATAL ConsoleDegraded surface instead of latching the whole
//     cross-region topology inert (the hw276 defect: failed+OutcomeReady
//     matched no terminal block and no heal path, so region-b never meshed).
//   - A genuinely-failed primary (outcome != OutcomeReady) stays "failed"
//     and fires NO handover — the honest failure detection #4706/#3018
//     pinned is untouched. It DOES run the console probe (UAT row 241): the
//     flag is a surface, so a failed record must still state whether the
//     front door answers instead of being silent about a door nobody
//     opened. Nothing in the failed arms reads the probe result.
func TestMarkPhase1DoneConsoleGate(t *testing.T) {
	twoRegions := []provisioner.RegionSpec{{Provider: "huawei"}, {Provider: "huawei"}}
	mkDep := func() *Deployment {
		return &Deployment{
			ID:        "console-gate",
			Status:    "phase1-watching",
			StartedAt: time.Now(),
			eventsCh:  make(chan provisioner.Event, 256),
			done:      make(chan struct{}),
			Request: provisioner.Request{
				SovereignFQDN: "hw999.omani.works",
				OrgEmail:      "operator@test.example.com",
				Regions:       twoRegions,
			},
			Result:     &provisioner.Result{},
			OwnerEmail: "operator@test.example.com",
		}
	}
	finalStates := map[string]string{"cilium": helmwatch.StateInstalled}

	t.Run("console reachable -> ready, no degraded surface", func(t *testing.T) {
		h := NewWithPDM(silentLogger(), &fakePDM{})
		h.suppressPostHandoverHooks = true
		h.consoleProbe = func(fqdn string) error { return nil }
		dep := mkDep()
		h.markPhase1Done(dep, finalStates, helmwatch.OutcomeReady)
		if dep.Status != "ready" {
			t.Fatalf("reachable console must flip ready: got Status=%q (err=%q)", dep.Status, dep.Error)
		}
		if dep.Result.ConsoleDegraded || dep.Result.ConsoleDegradedDetail != "" {
			t.Fatalf("reachable console must not surface ConsoleDegraded: got %v / %q",
				dep.Result.ConsoleDegraded, dep.Result.ConsoleDegradedDetail)
		}
	})

	// #5253 — the hw276 shape: converged primary + console probe failure.
	// The producer chain must STILL fire and the record must stay a mesh
	// candidate; the console signal surfaces non-fatally.
	t.Run("console unreachable + converged primary -> ready, chain fires, ConsoleDegraded surfaced", func(t *testing.T) {
		h := NewWithPDM(silentLogger(), &fakePDM{})
		h.suppressPostHandoverHooks = true
		h.SetHandoverSigner(loadTestSigner(t))
		h.consoleProbe = func(fqdn string) error { return fmt.Errorf("https://console.%s/ returned HTTP 404", fqdn) }
		dep := mkDep()
		h.markPhase1Done(dep, finalStates, helmwatch.OutcomeReady)

		if dep.Status != "ready" {
			t.Fatalf("converged primary must stay ready despite console probe failure (#5253): got Status=%q (err=%q)", dep.Status, dep.Error)
		}
		if dep.Error != "" {
			t.Fatalf("console condition must NOT ride dep.Error (FailureCard renders it as a hard failure): got %q", dep.Error)
		}
		if !dep.Result.ConsoleDegraded {
			t.Fatalf("ConsoleDegraded surface must be set for an unconfirmed console")
		}
		if !strings.Contains(dep.Result.ConsoleDegradedDetail, "HTTP 404") {
			t.Fatalf("ConsoleDegradedDetail must carry the probe diagnostic, got %q", dep.Result.ConsoleDegradedDetail)
		}
		if dep.Result.Phase1Outcome != helmwatch.OutcomeReady {
			t.Fatalf("Phase1Outcome must stay %q, got %q", helmwatch.OutcomeReady, dep.Result.Phase1Outcome)
		}
		// The producer chain fired: fireHandover ran inline in the
		// OutcomeReady terminal block — the same block that spawns
		// runAutoEstablishClusterMesh + the spine/adoption/policy hooks.
		if dep.Result.HandoverFiredAt == nil {
			t.Fatalf("handover must auto-fire for a converged primary despite the console downgrade (#5253)")
		}
		if !strings.HasPrefix(dep.Result.HandoverURL, "https://console.hw999.omani.works/auth/handover?token=") {
			t.Fatalf("HandoverURL has unexpected shape: %q", dep.Result.HandoverURL)
		}
		// And the record remains a mesh candidate for the level-triggered
		// startup heal paths.
		if !h.clusterMeshReconcileStatusGate(dep) {
			t.Fatalf("a ready 2-region console-degraded record must pass the mesh status gate")
		}
	})

	// The probe RUNS for non-Ready outcomes and changes NOTHING about the
	// lifecycle — a genuine hard failure upstream stays failed regardless of
	// console state and must NOT fire the producer chain (the fire condition
	// is OutcomeReady, i.e. every primary HR installed).
	//
	// This subtest previously asserted the opposite ("console probe must not
	// run for a non-Ready outcome"). That assertion is what made UAT row 241
	// unsatisfiable in one of the two directions it names: a `failed`-latched
	// record never ran the probe, so ConsoleDegraded stayed zero-valued and
	// `omitempty` dropped it, and the record reported no console problem
	// about a console nobody had looked at. Measured on hw293, dep
	// a0077ba47e3720e5: `status: failed`, consoleDegraded absent, while
	// https://console.<fqdn>/ answered 200 from the public internet.
	//
	// Skipping the probe was never what protected the lifecycle. The
	// remaining assertions here are, and they are unchanged: Status, the
	// handover fire, and the mesh gate are all decided without reading the
	// probe result.
	t.Run("genuinely-failed primary: stays failed, no fire, probe runs and surfaces", func(t *testing.T) {
		h := NewWithPDM(silentLogger(), &fakePDM{})
		h.suppressPostHandoverHooks = true
		h.SetHandoverSigner(loadTestSigner(t))
		called := false
		h.consoleProbe = func(fqdn string) error { called = true; return nil }
		dep := mkDep()
		h.markPhase1Done(dep, finalStates, helmwatch.OutcomeFluxNotReconciling)
		if !called {
			t.Fatalf("console probe must run on a non-Ready outcome — the flag is a surface, not a gate (#5253)")
		}
		if dep.Status != "failed" {
			t.Fatalf("OutcomeFluxNotReconciling must stay failed, got %q", dep.Status)
		}
		if dep.Result.HandoverFiredAt != nil || dep.Result.HandoverURL != "" {
			t.Fatalf("a genuinely-failed primary must NOT fire handover: firedAt=%v url=%q",
				dep.Result.HandoverFiredAt, dep.Result.HandoverURL)
		}
		// The door answered, so the surface must say so — not stay silent.
		if dep.Result.ConsoleDegraded {
			t.Fatalf("ConsoleDegraded must be false when the probe succeeded")
		}
	})

	t.Run("hard-failed component: stays failed, no fire, not a mesh candidate", func(t *testing.T) {
		h := NewWithPDM(silentLogger(), &fakePDM{})
		h.suppressPostHandoverHooks = true
		h.SetHandoverSigner(loadTestSigner(t))
		h.consoleProbe = func(fqdn string) error { return nil }
		dep := mkDep()
		h.markPhase1Done(dep, map[string]string{"cilium": helmwatch.StateFailed}, helmwatch.OutcomeFailed)
		if dep.Status != "failed" {
			t.Fatalf("hard-failed component must stay failed, got %q", dep.Status)
		}
		if dep.Result.HandoverFiredAt != nil {
			t.Fatalf("hard failure must NOT fire handover")
		}
		// #6040 INVERTED. The LIFECYCLE stays failed (asserted above: no
		// handover, no fire) — but the TOPOLOGY must keep converging. A
		// component census is a statement about HelmReleases, never about
		// apiserver reachability, and OutcomeFailed is only reachable when
		// Phase 1 watched every component to a terminal state on both
		// regions' live apiservers. hw293 (dep a0077ba47e3720e5) is the
		// evidence: one DORMANT chart failing the census left both regions
		// unmeshed, which turned the secondary's by-design endpoint-less
		// edge-route stubs into "no healthy upstream" for half of every
		// hostname's fresh TCP connections.
		if !h.clusterMeshReconcileStatusGate(dep) {
			t.Fatalf("failed+OutcomeFailed on a 2-region record must still pass the mesh status gate (#6040)")
		}
	})
}

// TestConsoleReachabilityReprobe pins the #5253 background re-probe: it
// clears the non-fatal ConsoleDegraded surface the moment the front door
// answers, refreshes the surfaced diagnostic while it does not, and gives up
// (surface intact) after its bounded attempts.
func TestConsoleReachabilityReprobe(t *testing.T) {
	mkDegraded := func() *Deployment {
		return &Deployment{
			ID:     "console-reprobe",
			Status: "ready",
			Request: provisioner.Request{
				SovereignFQDN: "hw999.omani.works",
			},
			Result: &provisioner.Result{
				Phase1Outcome:         helmwatch.OutcomeReady,
				ConsoleDegraded:       true,
				ConsoleDegradedDetail: "https://console.hw999.omani.works/ returned HTTP 404",
			},
		}
	}

	t.Run("clears the surface once the console answers", func(t *testing.T) {
		h := NewWithPDM(silentLogger(), &fakePDM{})
		calls := 0
		h.consoleProbe = func(fqdn string) error {
			calls++
			if calls < 2 {
				return errors.New("still HTTP 404")
			}
			return nil
		}
		dep := mkDegraded()
		h.runConsoleReachabilityReprobe(dep)
		if calls != 2 {
			t.Fatalf("re-probe must retry until reachable: got %d calls, want 2", calls)
		}
		if dep.Result.ConsoleDegraded || dep.Result.ConsoleDegradedDetail != "" {
			t.Fatalf("surface must clear once the console answers: got %v / %q",
				dep.Result.ConsoleDegraded, dep.Result.ConsoleDegradedDetail)
		}
	})

	t.Run("bounded give-up keeps the surface + freshest diagnostic", func(t *testing.T) {
		h := NewWithPDM(silentLogger(), &fakePDM{})
		calls := 0
		h.consoleProbe = func(fqdn string) error {
			calls++
			return fmt.Errorf("attempt-%d: connection refused", calls)
		}
		dep := mkDegraded()
		h.runConsoleReachabilityReprobe(dep)
		if calls != consoleReprobeAttempts {
			t.Fatalf("re-probe must stop after %d attempts, got %d", consoleReprobeAttempts, calls)
		}
		if !dep.Result.ConsoleDegraded {
			t.Fatalf("surface must stay set while the console never answers")
		}
		if !strings.Contains(dep.Result.ConsoleDegradedDetail, fmt.Sprintf("attempt-%d", consoleReprobeAttempts)) {
			t.Fatalf("detail must carry the freshest diagnostic, got %q", dep.Result.ConsoleDegradedDetail)
		}
	})
}
