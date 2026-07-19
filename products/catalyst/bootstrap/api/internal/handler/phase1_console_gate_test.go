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
//   - A genuinely-failed primary (outcome != OutcomeReady) stays "failed",
//     fires NO handover, and never runs the console probe — the honest
//     failure detection #4706/#3018 pinned is untouched.
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

	// The probe is skipped for non-Ready outcomes — a genuine hard failure
	// upstream must stay failed regardless of console state, and must NOT
	// fire the producer chain (the fire condition is OutcomeReady, i.e.
	// every primary HR installed).
	t.Run("genuinely-failed primary: stays failed, no fire, probe not run", func(t *testing.T) {
		h := NewWithPDM(silentLogger(), &fakePDM{})
		h.suppressPostHandoverHooks = true
		h.SetHandoverSigner(loadTestSigner(t))
		called := false
		h.consoleProbe = func(fqdn string) error { called = true; return nil }
		dep := mkDep()
		h.markPhase1Done(dep, finalStates, helmwatch.OutcomeFluxNotReconciling)
		if called {
			t.Fatalf("console probe must not run for a non-Ready outcome")
		}
		if dep.Status != "failed" {
			t.Fatalf("OutcomeFluxNotReconciling must stay failed, got %q", dep.Status)
		}
		if dep.Result.HandoverFiredAt != nil || dep.Result.HandoverURL != "" {
			t.Fatalf("a genuinely-failed primary must NOT fire handover: firedAt=%v url=%q",
				dep.Result.HandoverFiredAt, dep.Result.HandoverURL)
		}
		if dep.Result.ConsoleDegraded {
			t.Fatalf("ConsoleDegraded must not be set on a genuine failure")
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
		// failed + OutcomeFailed is excluded from the #5253 heal arms too —
		// only the failed+OutcomeReady console-downgrade signature (records
		// persisted by pre-#5253 builds) is rescuable.
		if h.clusterMeshReconcileStatusGate(dep) {
			t.Fatalf("failed+OutcomeFailed must NOT pass the mesh status gate")
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
