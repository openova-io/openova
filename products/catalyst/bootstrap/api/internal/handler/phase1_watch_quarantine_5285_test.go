// phase1_watch_quarantine_5285_test.go — #5285 live-conclusion wiring guard.
//
// PR #5287 landed the k8scache reflector teardown+quarantine (factory.go
// QuarantineDeployment) and two of its three call sites already carry
// dedicated tests:
//   - the factory method itself     → informer_teardown_5285_test.go
//   - the restart re-derivation path → k8scache_quarantine_5285_test.go
//     (SetK8sCache re-quarantines persisted Status=="failed" records)
//
// The THIRD call site — the live terminal conclusion in markPhase1Done
// (phase1_watch.go: `if finalStatus == "failed" { QuarantineDeployment }`)
// — had NO test. Deleting those lines broke nothing, so a future refactor
// could silently drop the trigger and the hw279 flood (a failed env's
// per-kind reflectors 404-flooding against its missing Catalyst CRDs, which
// spike catalyst-api CPU and starve the /wipe + /deployments control
// endpoints) would resume unnoticed.
//
// This test closes that gap. It proves the condition is load-bearing in
// BOTH directions:
//   - a deployment that concludes terminal-FAILED IS quarantined — every
//     `<id>` primary AND `<id>-<region>` secondary reflector torn down, and
//     re-registration (the lingering-kubeconfig rescan/POST vector)
//     refused; and
//   - a deployment that concludes READY is NOT quarantined — its reflectors
//     stay live to power the console view.
//
// Anti-theater: removing the QuarantineDeployment call fails the failed-arm
// assertions; widening the guard to quarantine unconditionally fails the
// ready-arm assertion.
package handler

import (
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
	kfake "k8s.io/client-go/kubernetes/fake"
)

// TestMarkPhase1Done_QuarantinesFailedDeploymentReflectors is the #5285
// live-conclusion wiring guard for phase1_watch.go's terminal-failed branch.
func TestMarkPhase1Done_QuarantinesFailedDeploymentReflectors(t *testing.T) {
	// A factory with the failed deployment's primary + secondary clusters and
	// an unrelated healthy deployment already registered (the state a running
	// catalyst-api holds while Phase-1 is still watching). No Start() → the
	// informers are built but never dial, so nothing touches the network.
	f, err := k8scache.NewFactory(k8scache.Config{
		Logger:   quietLog(),
		Registry: minimalRegistry(),
	})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Stop()

	for _, id := range []string{"faildep", "faildep-b", "readydep"} {
		if err := f.AddCluster(k8scache.ClusterRef{
			ID:            id,
			DynamicClient: fakeDynForQuarantineTest(),
			CoreClient:    kfake.NewSimpleClientset(),
		}); err != nil {
			t.Fatalf("AddCluster(%s): %v", id, err)
		}
	}

	h := NewWithPDM(silentLogger(), &fakePDM{})
	// suppressPostHandoverHooks keeps the READY branch's producer chain
	// (mesh/spine/adoption/policy) from spawning — this test only exercises
	// the quarantine wiring, not the handover fan-out.
	h.suppressPostHandoverHooks = true
	// A reachable console keeps the READY deployment genuinely "ready" (the
	// #4706 gate never downgrades it), so the ready-arm assertion below is a
	// clean discriminator for the `finalStatus == "failed"` condition.
	h.consoleProbe = func(string) error { return nil }
	h.k8sCache = f

	mkDep := func(id string) *Deployment {
		return &Deployment{
			ID:        id,
			Status:    "phase1-watching",
			StartedAt: time.Now(),
			eventsCh:  make(chan provisioner.Event, 256),
			done:      make(chan struct{}),
			Request: provisioner.Request{
				SovereignFQDN: id + ".omani.works",
				OrgEmail:      "operator@test.example.com",
			},
			Result:     &provisioner.Result{SovereignFQDN: id + ".omani.works"},
			OwnerEmail: "operator@test.example.com",
		}
	}
	failDep := mkDep("faildep")
	readyDep := mkDep("readydep")
	h.deployments.Store(failDep.ID, failDep)
	h.deployments.Store(readyDep.ID, readyDep)

	// --- Terminal-FAILED conclusion MUST quarantine (primary + secondary). ---
	h.markPhase1Done(failDep, map[string]string{
		"cilium":            helmwatch.StateInstalled,
		"catalyst-platform": helmwatch.StateFailed,
	}, helmwatch.OutcomeFailed)

	failDep.mu.Lock()
	failStatus := failDep.Status
	failDep.mu.Unlock()
	if failStatus != "failed" {
		t.Fatalf("precondition: failed conclusion did not set Status=failed (got %q) — test would not reach the quarantine branch", failStatus)
	}

	if clustersContain(f.Clusters(), "faildep") {
		t.Errorf("terminal-failed deployment primary 'faildep' NOT quarantined — the reflector 404-flood would resume (phase1_watch.go quarantine call removed?)")
	}
	if clustersContain(f.Clusters(), "faildep-b") {
		t.Errorf("terminal-failed deployment SECONDARY 'faildep-b' NOT quarantined — secondary-region reflectors keep flooding")
	}

	// The quarantine must also REFUSE re-registration while the failed env's
	// kubeconfig lingers on the PVC for the eventual wipe (the rescan/POST
	// resurrection vector #5285 closes).
	if err := f.AddCluster(k8scache.ClusterRef{
		ID:            "faildep",
		DynamicClient: fakeDynForQuarantineTest(),
		CoreClient:    kfake.NewSimpleClientset(),
	}); err != nil {
		t.Fatalf("AddCluster(faildep) while quarantined returned error, want nil no-op: %v", err)
	}
	if clustersContain(f.Clusters(), "faildep") {
		t.Errorf("quarantined 'faildep' was re-registered by a later rescan/AddCluster — the flood would resume")
	}

	// --- READY conclusion MUST NOT quarantine (reflectors power the view). ---
	h.markPhase1Done(readyDep, map[string]string{
		"cilium": helmwatch.StateInstalled,
	}, helmwatch.OutcomeReady)

	readyDep.mu.Lock()
	readyStatus := readyDep.Status
	readyDep.mu.Unlock()
	if readyStatus != "ready" {
		t.Fatalf("precondition: ready conclusion did not set Status=ready (got %q, err=%q)", readyStatus, readyDep.Error)
	}
	if !clustersContain(f.Clusters(), "readydep") {
		t.Errorf("READY deployment 'readydep' was wrongly quarantined — its live reflectors (console view) went dark; the `finalStatus == \"failed\"` guard must gate the quarantine")
	}
}
