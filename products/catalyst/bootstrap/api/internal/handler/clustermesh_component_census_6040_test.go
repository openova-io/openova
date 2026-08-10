package handler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// #6040 — a Phase-1 COMPONENT CENSUS failure must not decide whether two
// healthy apiservers get meshed.
//
// Live evidence (hw293, dep a0077ba47e3720e5, 2026-08-10/11): exactly ONE of
// 67 components failed — self-sovereign-cutover, which is DORMANT by design
// and failed only because its Helm release Secret exceeded the 1 MiB object
// ceiling (#6004). That single row latched Phase1Outcome=failed, and every
// ClusterMesh producer was gated shut behind it:
//
//	markPhase1Done          fired the establish only on OutcomeReady / OutcomeTimeout
//	clusterMeshReconcileStatusGate  admitted only ready / failed+timeout / failed+ready
//	runAutoEstablishClusterMesh     returned as soon as status != "ready"
//	runClusterMeshSteadyStateHeal   returned as soon as status != "ready"
//
// so `cilium-dbg troubleshoot clustermesh` reported "Found 0 cluster
// configurations" in BOTH regions 4h after a fully-materialised 2-region
// prov. The secondary region's bp-catalyst-edge-routes stubs — Services that
// select ZERO local Pods BY DESIGN and rely on the mesh to reach region-a —
// were therefore permanent black holes, and the gateway ELB (6 region-a +
// 6 region-b node members) 503'd exactly 12 of 24 fresh TCP connections on
// every hostname whose backend lives in region-a.
//
// This is the same reasoning #6015 already applied to the secondary-kubeconfig
// producer one branch above: a HelmRelease census is not a statement about
// apiserver reachability. OutcomeFailed specifically means Phase 1 RAN and
// WATCHED components — which requires both regions' apiservers to be up — so
// it is precisely the shape that must keep meshing. Outcomes that mean the
// cluster is NOT usable (OutcomeFluxNotReconciling, the kubeconfig-missing and
// storage-downgrade reasons, and the empty outcome of a record that never
// reached Phase 1) stay excluded; the vacuity sub-tests below pin that.
func TestClusterMeshReconcileStatusGate_ComponentCensusFailure6040(t *testing.T) {
	dir := t.TempDir()
	h := &Handler{log: silentLogger(), kubeconfigsDir: dir}
	kcPath := filepath.Join(dir, "census.yaml")
	if err := os.WriteFile(kcPath, []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	twoRegions := []provisioner.RegionSpec{
		{Provider: "huawei", CloudRegion: "me-east-215-a"},
		{Provider: "huawei", CloudRegion: "me-east-215-b"},
	}

	cases := []struct {
		name    string
		status  string
		outcome string
		regions []provisioner.RegionSpec
		want    bool
	}{
		// The hw293 shape — the defect this test exists for.
		{"failed-component-census-two-region", "failed", helmwatch.OutcomeFailed, twoRegions, true},

		// Vacuity: the gate must still be capable of returning FALSE.
		// Without these a fix that returns `true` unconditionally passes.
		{"failed-component-census-single-region", "failed", helmwatch.OutcomeFailed, twoRegions[:1], false},
		{"failed-flux-not-reconciling", "failed", helmwatch.OutcomeFluxNotReconciling, twoRegions, false},
		{"failed-outcome-never-classified", "failed", "", twoRegions, false},
		{"wiping", "wiping", helmwatch.OutcomeFailed, twoRegions, false},
		{"provisioning", "provisioning", "", twoRegions, false},

		// Pre-existing arms must not regress.
		{"ready-two-region", "ready", helmwatch.OutcomeReady, twoRegions, true},
		{"failed-timeout-3285", "failed", helmwatch.OutcomeTimeout, twoRegions, true},
		{"failed-console-downgrade-5253", "failed", helmwatch.OutcomeReady, twoRegions, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dep := &Deployment{
				ID:      "census-" + tc.name,
				Status:  tc.status,
				Request: provisioner.Request{Regions: tc.regions},
				Result:  &provisioner.Result{KubeconfigPath: kcPath, Phase1Outcome: tc.outcome},
			}
			if got := h.clusterMeshReconcileStatusGate(dep); got != tc.want {
				t.Errorf("clusterMeshReconcileStatusGate(status=%q, outcome=%q, regions=%d) = %v, want %v",
					tc.status, tc.outcome, len(tc.regions), got, tc.want)
			}
		})
	}
}

// TestClusterMeshLoopMayContinue6040 pins the SECOND and THIRD gates. The
// status gate alone is not enough: even when the loop is kicked, both
// runAutoEstablishClusterMesh's retry loop and runClusterMeshSteadyStateHeal
// re-read dep.Status and bail on anything that is not "ready" — so a fix that
// only widened the entry gate would have converged zero times on hw293 (the
// half-landed shape). The wipe guard those checks exist for must survive.
func TestClusterMeshLoopMayContinue6040(t *testing.T) {
	cases := map[string]bool{
		"ready":           true,
		"failed":          true,
		"partial-failure": true,
		// A wipe in flight must still stop the loops — the kubeconfigs are
		// gone or going. This is the invariant the `!= "ready"` checks were
		// protecting, and it must not be lost by widening them.
		"wiping":          false,
		"wiped":           false,
		"provisioning":    false,
		"phase1-watching": false,
		"queued":          false,
	}
	for status, want := range cases {
		if got := clusterMeshLoopMayContinue(status); got != want {
			t.Errorf("clusterMeshLoopMayContinue(%q) = %v, want %v", status, got, want)
		}
	}
}

// TestRunAutoEstablishClusterMesh_CensusFailedRecordSurvivesRetry6040 pins the
// gate that the single-attempt end-to-end test structurally CANNOT reach.
//
// With fully-seeded fakes the establish converges on attempt 1 and emits the
// success event BEFORE the loop ever re-reads dep.Status — so a test built on
// that harness passes whether or not the loop's status check was widened, and
// would have shipped gate 3 untested (verified by reverting
// clusterMeshLoopMayContinue to ready-only: the single-attempt test still
// passed). This variant starts region 0 with NO clustermesh-apiserver LB IP, so
// attempt 1 MUST fail; the loop then hits the status check with status="failed"
// and, pre-#6040, returned there and abandoned the mesh forever. The LB IP is
// stamped only after the first retry event, exactly as LB-IPAM does in
// production.
//
// The wipe invariant is exercised in the same run: watchAndStopSteadyStateOnConverged
// flips the record to "wiped" at convergence, and the loop must then terminate —
// so a fix that simply removed the status check would hang this test.
func TestRunAutoEstablishClusterMesh_CensusFailedRecordSurvivesRetry6040(t *testing.T) {
	fx := newTestFixture(t, []string{"", "203.0.113.20"})
	restore := installClusterMeshClientFactory(fx.clients)
	defer restore()
	restoreDyn := installClusterMeshDynamicClientFactory(fx.dynClients)
	defer restoreDyn()

	// The hw293 record shape: Phase 1 ran, watched a terminal census, and one
	// dormant component failed it.
	fx.dep.mu.Lock()
	fx.dep.Status = "failed"
	fx.dep.Result.Phase1Outcome = helmwatch.OutcomeFailed
	fx.dep.mu.Unlock()

	setClusterMeshLBOverrides(t, 150*time.Millisecond, 25*time.Millisecond)
	fx.handler.clusterMeshRetryInitialBackoff = 20 * time.Millisecond
	fx.handler.clusterMeshRetryMaxBackoff = 60 * time.Millisecond
	fx.handler.clusterMeshRetryBudget = 20 * time.Second
	fx.handler.clusterMeshAttemptTimeout = 5 * time.Second
	fx.handler.clusterMeshSteadyStateInterval = 20 * time.Millisecond

	stopSteady := watchAndStopSteadyStateOnConvergedFrom6040(fx)
	defer stopSteady()

	flipDone := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if hasClusterMeshEvent(fx.dep, "warn", "retrying in") {
				cs := fx.clients[fx.primaryKubeconfigPath]
				svc, err := cs.CoreV1().Services(clusterMeshNamespace).Get(context.Background(), clusterMeshApiserverService, metav1.GetOptions{})
				if err != nil {
					flipDone <- err
					return
				}
				svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "203.0.113.10"}}
				_, err = cs.CoreV1().Services(clusterMeshNamespace).Update(context.Background(), svc, metav1.UpdateOptions{})
				flipDone <- err
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		flipDone <- errNoRetryEvent6040
	}()

	loopDone := make(chan struct{})
	go func() {
		fx.handler.runAutoEstablishClusterMesh(fx.dep)
		close(loopDone)
	}()
	select {
	case <-loopDone:
	case <-time.After(25 * time.Second):
		t.Fatalf("reconcile loop did not terminate; events:\n%s", dumpClusterMeshEvents(fx.dep))
	}
	if err := <-flipDone; err != nil {
		t.Fatalf("LB flip goroutine: %v", err)
	}

	// Attempt 1 failed and the loop RETRIED rather than abandoning the record.
	if !hasClusterMeshEvent(fx.dep, "warn", "attempt 1 ended with", "retrying in") {
		t.Fatalf("a census-failed record must keep retrying past attempt 1; events:\n%s", dumpClusterMeshEvents(fx.dep))
	}
	if !hasClusterMeshEvent(fx.dep, "info", "fully meshed (2/2 regions)", "reconcile loop complete") {
		t.Fatalf("a census-failed record must reach full mesh; events:\n%s", dumpClusterMeshEvents(fx.dep))
	}
	// The wipe invariant held — the loop stopped once the record left the
	// meshable statuses, rather than spinning against a tearing-down cluster.
	fx.dep.mu.Lock()
	finalStatus := fx.dep.Status
	fx.dep.mu.Unlock()
	if finalStatus != "wiped" {
		t.Fatalf("harness should have flipped the record to wiped at convergence, got %q", finalStatus)
	}
}

var errNoRetryEvent6040 = errors.New("never observed a retry progress event")

// watchAndStopSteadyStateOnConvergedFrom6040 mirrors the existing
// watchAndStopSteadyStateOnConverged helper but flips out of "failed" (this
// record never was "ready"), so the steady-state heal phase releases the loop.
func watchAndStopSteadyStateOnConvergedFrom6040(fx *testFixture) func() {
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if hasClusterMeshEvent(fx.dep, "info", "reconcile loop complete") {
					fx.dep.mu.Lock()
					if fx.dep.Status == "failed" {
						fx.dep.Status = "wiped"
					}
					fx.dep.mu.Unlock()
					return
				}
			}
		}
	}()
	return func() { close(stop) }
}

// TestRestoreFromStore_KicksClusterMeshOnComponentCensusFailure6040 is the
// END-TO-END guard: a rehydrated record carrying the exact hw293 shape
// (status=failed, Phase1Outcome=failed, 2 materialised regions, primary
// kubeconfig on the PVC) must reach FULL MESH on the startup reconcile.
//
// It exercises all three gates at once — the entry gate, the convergence
// loop's status check, and the establish itself — and asserts the real
// artifact (the cilium-clustermesh Secret written into BOTH fake regions),
// not merely that a code path was entered.
func TestRestoreFromStore_KicksClusterMeshOnComponentCensusFailure6040(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "")
	dir := t.TempDir()
	caCert, caKey := genCAForTest(t, "test-mesh-ca-6040")

	depID := "dep6040census"
	regions := []provisioner.RegionSpec{
		{Provider: "huawei", CloudRegion: "me-east-215-a"},
		{Provider: "huawei", CloudRegion: "me-east-215-b"},
	}
	lbIPs := []string{"203.0.113.60", "203.0.113.70"}
	clients := map[string]kubernetes.Interface{}
	for i := range regions {
		key := regionKeyFromSpec(regions[i], i)
		kcPath := writeFakeKubeconfig(t, dir, depID, key)
		cs := buildFakeClusterMeshCluster(t, lbIPs[i], caCert, caKey)
		if i == 0 {
			seedCNPGPairSourceSecrets(t, cs)
		} else {
			seedCNPGPairReplicaNamespace(t, cs)
		}
		clients[kcPath] = cs
	}
	primaryKubeconfigPath := filepath.Join(dir, depID+".yaml")
	dynClients := map[string]dynamic.Interface{}
	for i := range regions {
		key := regionKeyFromSpec(regions[i], i)
		kcPath := primaryKubeconfigPath
		if i > 0 {
			kcPath = filepath.Join(dir, depID+"-"+key+".yaml")
		}
		dynClients[kcPath] = newFakeKustomizationDynClient(t,
			buildBootstrapKitKustomization(defaultBootstrapKitSubstitute()))
	}
	restore := installClusterMeshClientFactory(clients)
	defer restore()
	restoreDyn := installClusterMeshDynamicClientFactory(dynClients)
	defer restoreDyn()

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	phase1Done := time.Now().Add(-1 * time.Hour).UTC()
	rec := store.Record{
		ID: depID,
		// The hw293 record verbatim: ONE dormant component failed the
		// census, so the whole deployment latched "failed".
		Status: "failed",
		Error:  "Phase 1 finished with 1 failed component(s); see ComponentStates for the per-component breakdown",
		Request: store.Redact(provisioner.Request{
			SovereignFQDN: "hw293.omantel.biz",
			Regions:       regions,
		}),
		Result: &provisioner.Result{
			SovereignFQDN:    "hw293.omantel.biz",
			KubeconfigPath:   primaryKubeconfigPath,
			Phase1Outcome:    helmwatch.OutcomeFailed,
			Phase1FinishedAt: &phase1Done,
			ComponentStates:  map[string]string{"self-sovereign-cutover": "failed"},
		},
		StartedAt:  time.Now().Add(-2 * time.Hour),
		FinishedAt: time.Now().Add(-90 * time.Minute),
	}
	if err := st.Save(rec); err != nil {
		t.Fatalf("store.Save: %v", err)
	}

	h := &Handler{log: silentLogger(), kubeconfigsDir: dir, store: st}
	h.clusterMeshRetryInitialBackoff = 20 * time.Millisecond
	h.clusterMeshRetryMaxBackoff = 60 * time.Millisecond
	h.clusterMeshRetryBudget = 20 * time.Second
	h.clusterMeshAttemptTimeout = 5 * time.Second
	h.consumerHubSyncVerifyBackoff = 20 * time.Millisecond
	h.consumerHubSyncVerifyAttempts = 3

	h.restoreFromStore()

	val, ok := h.deployments.Load(depID)
	if !ok {
		t.Fatalf("deployment %q not restored", depID)
	}
	dep := val.(*Deployment)

	waitForClusterMeshEvent(t, dep, 10*time.Second, "info", "fully meshed (2/2 regions)", "reconcile loop complete")

	for kcPath, client := range clients {
		secret, err := client.CoreV1().Secrets(clusterMeshNamespace).Get(context.Background(), clusterMeshSecretName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("Get cilium-clustermesh in %q: %v — a component-census failure must not leave the regions unmeshed", kcPath, err)
		}
		if len(secret.Data) != 4 {
			t.Errorf("Secret in %q has %d entries, want 4 (keys %v)", kcPath, len(secret.Data), secretKeys(secret))
		}
	}
	for kcPath, dyn := range dynClients {
		ks := getBootstrapKitKustomization(t, dyn)
		substitute, _, _ := unstructured.NestedStringMap(ks.Object, "spec", "postBuild", "substitute")
		if got := substitute[clusterMeshCNPGPairSubstituteKey]; got != "true" {
			t.Errorf("region %q: substitute[%s] = %q, want \"true\" — the cnpg-pair flip must land on a census-failed record too",
				kcPath, clusterMeshCNPGPairSubstituteKey, got)
		}
	}
}
