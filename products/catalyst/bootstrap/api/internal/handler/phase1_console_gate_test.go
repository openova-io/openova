package handler

import (
	"fmt"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// TestMarkPhase1DoneConsoleGate locks in #4706: a Phase-1 OutcomeReady is
// promoted to Status="ready" ONLY when the external console-reachability probe
// succeeds. A converged-but-unreachable console (the console-000 symptom that
// killed hw217 while the mothership still claimed "ready") must be classified
// "failed" — never a false-green "ready" that redirects the operator to a dead
// URL. The consoleProbe seam lets us assert both arms without a network.
func TestMarkPhase1DoneConsoleGate(t *testing.T) {
	mkDep := func() *Deployment {
		return &Deployment{
			ID:     "console-gate",
			Status: "phase1-watching",
			Request: provisioner.Request{
				SovereignFQDN: "hw999.omani.works",
				Regions:       []provisioner.RegionSpec{{Provider: "huawei"}},
			},
			Result: &provisioner.Result{},
		}
	}
	finalStates := map[string]string{"cilium": helmwatch.StateInstalled}

	t.Run("console reachable -> ready", func(t *testing.T) {
		h := NewWithPDM(silentLogger(), &fakePDM{})
		h.consoleProbe = func(fqdn string) error { return nil }
		dep := mkDep()
		h.markPhase1Done(dep, finalStates, helmwatch.OutcomeReady)
		if dep.Status != "ready" {
			t.Fatalf("reachable console must flip ready: got Status=%q (err=%q)", dep.Status, dep.Error)
		}
	})

	t.Run("console unreachable -> failed, not false-ready", func(t *testing.T) {
		h := NewWithPDM(silentLogger(), &fakePDM{})
		h.consoleProbe = func(fqdn string) error { return fmt.Errorf("connection refused") }
		dep := mkDep()
		h.markPhase1Done(dep, finalStates, helmwatch.OutcomeReady)
		if dep.Status == "ready" {
			t.Fatalf("unreachable console must NOT flip ready (false-green) — got Status=ready")
		}
		if dep.Status != "failed" {
			t.Fatalf("unreachable console: got Status=%q, want failed", dep.Status)
		}
		if !strings.Contains(dep.Error, "externally reachable") {
			t.Fatalf("failed error must explain the console is unreachable, got %q", dep.Error)
		}
	})

	// The probe is skipped for non-Ready outcomes — a genuine hard failure
	// upstream must stay failed regardless of console state.
	t.Run("non-ready outcome is untouched by the gate", func(t *testing.T) {
		h := NewWithPDM(silentLogger(), &fakePDM{})
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
	})
}
