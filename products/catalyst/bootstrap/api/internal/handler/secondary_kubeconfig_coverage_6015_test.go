// secondary_kubeconfig_coverage_6015_test.go — #6015.
//
// Three defects, one shape: a Sovereign whose catalyst-api holds FEWER region
// kubeconfigs than the deployment declares answers exactly like a correct
// single-region one, and every guard downstream is scoped by the same step that
// lost the region.
//
// Measured live on hw293 (dep a0077ba47e3720e5, 2026-08-10):
//
//   - Phase-1 terminated `finalStatus=failed` on ONE HelmRelease out of 65 —
//     `self-sovereign-cutover`, the chart that installs DORMANT at slot 06a.
//     That gate skipped fireHandover AND runAutoEstablishClusterMesh, and both
//     producers of the chroot's kubeconfigs dir hang off those two.
//   - 🛑 The dir is NOT empty. It holds exactly ONE file,
//     `a0077ba47e3720e5.yaml` — the REGION-A config. Only the
//     `-me-east-215-b-1.yaml` secondary is missing, and region B's apiserver
//     answered 6 nodes to a direct query throughout: a missing CREDENTIAL, not
//     a reachability problem. Every assertion here is therefore about COVERAGE
//     (the kubeconfig/cluster set vs the DECLARED region set), never about
//     presence — a `dir is non-empty` / `pool == 0` check reads the region-A
//     file, passes, and leaves the defect live.
//   - `…/applications/bp-alloy/placement` returned ONE region-A Primary under
//     `derivedFromRuntime: true` while alloy ran 6 pods in A and 6 in B;
//     `cilium` (14/14) returned `targets: []` under the same flag.
//   - `orgConsoleTLSPoolRegions` excludes the primary by design, so that
//     one-file dir yields a SHORT pool → empty `unreached` → `listener pair
//     admitted in every region` over regions="host".
//
// Every assertion below is written to be RED against the pre-#6015 code:
//   - Coverage_*: pre-fix HandleApplicationPlacement hardcoded `true`.
//   - ShortRegionPool_*: pre-fix a short pool produced no `unreached` entries,
//     so the success line was emitted.
//   - Delivery_*: pre-fix reforwardSecondaryKubeconfigsToChild had exactly one
//     caller, gated behind mesh+cnpg convergence behind status=ready.
//
// Each negative case is paired with a CONTROL that shares its fixture shape and
// must stay GREEN, so a red negative proves the defect and not the harness.
package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// ---------------------------------------------------------------------------
// Guard A — derivedFromRuntime must not be assertable from a narrow cache.
// ---------------------------------------------------------------------------

// THE NEGATIVE CASE, built first. A deployment that DECLARES two regions whose
// cache holds only region A must NOT claim its answer was runtime-derived: the
// one target it can see is indistinguishable from an honest singleton, and the
// FE has no other signal to fall back on.
//
// This is the exact hw293 `bp-alloy` shape — 6 pods in each region, one region
// visible.
func TestPlacementCoverage_TwoDeclaredOneCached_NotDerived_6015(t *testing.T) {
	depID := "a0077ba47e3720e5"
	regionA, regionB := "me-east-215-a", "me-east-215-b"

	h := newSingleClusterPlacementHandler(t, depID, regionA, regionB,
		[]*unstructured.Unstructured{
			placementFixturePod("alloy", "alloy-aaa", "alloy", regionA, ""),
		})

	resp := callPlacement(t, h, depID, "bp-alloy")

	if resp.DerivedFromRuntime {
		t.Fatalf("#6015: derivedFromRuntime must be FALSE when the cache covers %d of %d declared regions — "+
			"the lone region-A target reads identically to an honest singleton (targets=%+v)",
			resp.RegionsObserved, resp.RegionsDeclared, resp.Targets)
	}
	if resp.RegionsDeclared != 2 {
		t.Fatalf("#6015: regionsDeclared = %d, want 2 (the deployment record declares two regions)", resp.RegionsDeclared)
	}
	if resp.RegionsObserved != 1 {
		t.Fatalf("#6015: regionsObserved = %d, want 1 (only the region-A cluster is registered)", resp.RegionsObserved)
	}
}

// THE CONTROL for the test above — the SAME component, the SAME two-region
// deployment, but with BOTH region clusters in the cache. It must stay GREEN.
//
// Without this control the negative case is worthless: a coverage check that
// returns false for every fixture passes the negative test while breaking the
// product. This pins that the gate fires on the SHORTFALL, not on the fixture.
func TestPlacementCoverage_TwoDeclaredTwoCached_Derived_6015(t *testing.T) {
	depID := "a0077ba47e3720e5"
	regionA, regionB := "me-east-215-a", "me-east-215-b"

	h := newPlacementHandler(t, depID, regionA, regionB,
		[]*unstructured.Unstructured{placementFixturePod("alloy", "alloy-aaa", "alloy", regionA, "")},
		[]*unstructured.Unstructured{placementFixturePod("alloy", "alloy-bbb", "alloy", regionB, "")},
	)

	resp := callPlacement(t, h, depID, "bp-alloy")

	if !resp.DerivedFromRuntime {
		t.Fatalf("control: derivedFromRuntime must stay TRUE on full coverage (declared=%d observed=%d unobserved=%v)",
			resp.RegionsDeclared, resp.RegionsObserved, resp.UnobservedClusters)
	}
	if len(resp.Targets) != 2 {
		t.Fatalf("control: got %d targets want 2 — alloy runs in BOTH regions (%+v)", len(resp.Targets), resp.Targets)
	}
	if resp.RegionsObserved != 2 || resp.RegionsDeclared != 2 {
		t.Fatalf("control: declared/observed = %d/%d, want 2/2", resp.RegionsDeclared, resp.RegionsObserved)
	}
}

// The #5568 sibling the `k8sCache == nil` check structurally cannot see: the
// cache is PRESENT but the deployment's cluster is NOT REGISTERED in it. The
// List error is swallowed by `continue`, so pre-#6015 the handler answered
// `derivedFromRuntime: true` while its own cache said the cluster is unknown —
// exactly what QuarantineDeployment leaves behind on the mother (hw293:
// "deployment quarantined (terminal failure) … clustersRemoved: 2").
func TestPlacementCoverage_UnregisteredCluster_NotDerived_6015(t *testing.T) {
	depID := "a0077ba47e3720e5"

	// A live cache that holds SOMEBODY ELSE's clusters, so h.k8sCache != nil
	// and the #5568 early return does not fire, but nothing of this deployment.
	h := newPlacementHandler(t, "some-other-dep", "eu-central-1", "eu-west-1",
		[]*unstructured.Unstructured{placementFixturePod("apps", "x-aaa", "x", "eu-central-1", "")},
		[]*unstructured.Unstructured{},
	)
	h.deployments.Store(depID, &Deployment{
		ID: depID,
		Request: provisioner.Request{
			SovereignFQDN: "hw293.omantel.biz",
			Regions: []provisioner.RegionSpec{
				{CloudRegion: "me-east-215-a"},
				{CloudRegion: "me-east-215-b"},
			},
		},
	})

	resp := callPlacement(t, h, depID, "bp-alloy")

	if resp.DerivedFromRuntime {
		t.Fatalf("#6015/#5568: derivedFromRuntime must be FALSE when NONE of the deployment's clusters could be listed "+
			"(observed=%d unobserved=%v) — the empty answer is a cache miss, not a runtime observation",
			resp.RegionsObserved, resp.UnobservedClusters)
	}
	if resp.RegionsObserved != 0 {
		t.Fatalf("#6015: regionsObserved = %d, want 0 — no cluster of this deployment is registered", resp.RegionsObserved)
	}
}

// A genuinely SINGLE-region Sovereign must keep claiming a runtime derivation:
// declared 1, observed 1 is full coverage. Without this the fix would trade a
// false `true` for a false `false` on every single-region prov.
func TestPlacementCoverage_SingleRegionDeployment_StillDerived_6015(t *testing.T) {
	depID := "dep-single-region"
	regionA := "me-east-215-a"

	h := newSingleClusterPlacementHandler(t, depID, regionA, "",
		[]*unstructured.Unstructured{placementFixturePod("apps", "solo-xyz", "solo", regionA, "")})

	resp := callPlacement(t, h, depID, "solo")

	if !resp.DerivedFromRuntime {
		t.Fatalf("single-region: derivedFromRuntime must stay TRUE (declared=%d observed=%d) — 1 of 1 is FULL coverage",
			resp.RegionsDeclared, resp.RegionsObserved)
	}
	if len(resp.Targets) != 1 {
		t.Fatalf("single-region: got %d targets want 1 (%+v)", len(resp.Targets), resp.Targets)
	}
}

// THE DENOMINATOR. hw293's Sovereign never received the mother's record —
// handover never fired — so on the chroot the record is whatever
// chrootEnsureDeployment could synthesize, and that can carry ONE region while
// the Sovereign was provisioned with two. Taking the record as "declared" hands
// the coverage gate a denominator produced by the same blindness it exists to
// detect: declared 1, observed 1, gate satisfied, false singleton preserved.
//
// The declared count must therefore be the MAX of the record and the chart-
// baked CATALYST_CONFIGURED_REGIONS — an INDEPENDENT declaration the IaC writes
// into the `sovereign-fqdn` ConfigMap at provision time, verified live on hw293
// as `hw-me-east-215-a-rtz-prod,hw-me-east-215-b-rtz-prod` while the cache held
// ONE cluster and the record knew nothing.
//
// Falsifiable two ways: it is RED if the denominator comes from the record
// alone, AND red if the coverage comparison is dropped.
func TestPlacementCoverage_DeclaredCountIgnoresAnUndercountingRecord_6015(t *testing.T) {
	depID := "a0077ba47e3720e5"
	regionA := "me-east-215-a"

	t.Setenv("SOVEREIGN_FQDN", "hw293.omantel.biz")
	t.Setenv("CATALYST_CONFIGURED_REGIONS", "hw-me-east-215-a-rtz-prod,hw-me-east-215-b-rtz-prod")

	// regionB == "" → the stored record declares exactly ONE region, the
	// undercount a chroot-synthesized record produces.
	h := newSingleClusterPlacementHandler(t, depID, regionA, "",
		[]*unstructured.Unstructured{placementFixturePod("alloy", "alloy-aaa", "alloy", regionA, "")})

	// Premise check: the record really does undercount. Without this the test
	// could pass for the wrong reason.
	val, _ := h.deployments.Load(depID)
	if got := len(val.(*Deployment).Request.Regions); got != 1 {
		t.Fatalf("test premise broken: record declares %d regions, want 1", got)
	}

	resp := callPlacement(t, h, depID, "bp-alloy")

	if resp.RegionsDeclared != 2 {
		t.Fatalf("#6015: regionsDeclared = %d, want 2 from CATALYST_CONFIGURED_REGIONS — "+
			"a denominator taken from the undercounting record makes this gate unable to fail", resp.RegionsDeclared)
	}
	if resp.DerivedFromRuntime {
		t.Fatalf("#6015: derivedFromRuntime must be FALSE — the chart declares 2 regions, the cache covers 1")
	}
}

// newSingleClusterPlacementHandler mirrors newPlacementHandler but registers
// ONLY the primary region's cluster while the deployment record still declares
// two regions when regionB is non-empty. This is the hw293 cache shape.
func newSingleClusterPlacementHandler(t *testing.T, depID, regionA, regionB string, podsA []*unstructured.Unstructured) *Handler {
	t.Helper()

	r := k8scache.NewRegistry()
	_ = r.Add(k8scache.Kind{Name: "pod", GVR: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, Namespaced: true})
	_ = r.Add(k8scache.Kind{Name: "namespace", GVR: schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}, Namespaced: false})
	_ = r.Add(k8scache.Kind{Name: "node", GVR: schema.GroupVersionResource{Version: "v1", Resource: "nodes"}, Namespaced: false})

	dynA, coreA := dashFixtureClients(toRuntimeObjs(podsA)...)

	f, err := k8scache.NewFactory(k8scache.Config{
		Logger:   quietHandlerLogger(),
		Registry: r,
		Clusters: []k8scache.ClusterRef{{ID: depID, DynamicClient: dynA, CoreClient: coreA}},
	})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	if err := f.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(f.Stop)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, _, _ := f.List(depID, "pod", labels.Everything())
		if len(got) >= len(podsA) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	h := NewWithPDM(quietHandlerLogger(), &fakePDM{})
	h.SetK8sCache(f, k8scache.NewSARCache(), "")

	regions := []provisioner.RegionSpec{{CloudRegion: regionA}}
	if regionB != "" {
		regions = append(regions, provisioner.RegionSpec{CloudRegion: regionB})
	}
	h.deployments.Store(depID, &Deployment{
		ID:      depID,
		Request: provisioner.Request{SovereignFQDN: "hw293.omantel.biz", Regions: regions},
	})
	return h
}

// ---------------------------------------------------------------------------
// Guard B — an EMPTY region pool is a FAILURE, not a satisfied precondition.
// ---------------------------------------------------------------------------

// The #5246 `unreached` accounting can only report regions the POOL named, and
// the pool is derived from the same on-disk `<depID>-<regionKey>.yaml` set the
// targets are. When that set is SHORT the pool is short, `unreached` is empty,
// and the emitter logs "listener pair admitted in every region" over a target
// list that never contained the missing region — a guard whose scope is decided
// by the step that lost the region cannot fail on this defect.
//
// 🛑 The gate must be `declared > written`, NOT `pool == 0`. Measured on hw293,
// the Sovereign's kubeconfigs dir is NOT empty: it holds exactly one file,
// `a0077ba47e3720e5.yaml` — the REGION-A config. The secondary is what is
// missing. `seedELBPoolKubeconfigs` reproduces that faithfully (it always
// writes `<depID>.yaml`), and `onDiskSecondaryKubeconfigKeys` excludes the
// primary, so a NON-EMPTY directory still yields a pool that is short. A
// presence check would read the file, find the directory populated, and pass
// while the defect is live. The `pool spans 3, one delivered` case below pins
// that the gate fires on a short-but-NON-EMPTY pool too.
//
// The DECLARED region count comes from CATALYST_CONFIGURED_REGIONS, written
// into the `sovereign-fqdn` ConfigMap by the IaC at provision time. It does not
// read the kubeconfig set or h.k8sCache, so it can contradict them — which is
// the only reason the shortfall is detectable at all.
//
// Drives the REAL provisionOrgConsoleTLS. Each control changes exactly one
// variable, so a missing success line in a negative case can only be caused by
// the undelivered region.
func TestProvisionOrgConsoleTLS_ShortRegionPoolIsNotReady_6015(t *testing.T) {
	for _, tc := range []struct {
		name             string
		configuredRegion string
		delivered        []string
		wantSuccessLine  bool
	}{
		{
			name:             "control: the chart declares ONE region, the host target covers it",
			configuredRegion: "hw-me-east-215-a-rtz-prod",
			wantSuccessLine:  true,
		},
		{
			name:             "control: two declared, the secondary IS delivered — full coverage",
			configuredRegion: "hw-me-east-215-a-rtz-prod,hw-me-east-215-b-rtz-prod",
			delivered:        []string{"me-east-215-b-1"},
			wantSuccessLine:  true,
		},
		{
			name:             "hw293: two declared, dir holds ONLY the region-A primary, secondary never delivered",
			configuredRegion: "hw-me-east-215-a-rtz-prod,hw-me-east-215-b-rtz-prod",
			wantSuccessLine:  false,
		},
		{
			name:             "short but NON-EMPTY pool: three declared, one secondary delivered",
			configuredRegion: "hw-a-rtz-prod,hw-b-rtz-prod,hw-c-rtz-prod",
			delivered:        []string{"me-east-215-b-1"},
			wantSuccessLine:  false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SOVEREIGN_FQDN", "hw293.omantel.biz")
			t.Setenv("CATALYST_SELF_DEPLOYMENT_ID", "dep292")
			t.Setenv("CATALYST_CONFIGURED_REGIONS", tc.configuredRegion)
			// Always writes `dep292.yaml` (the region-A primary), plus any
			// delivered secondaries — the real on-disk shape, never an empty dir.
			seedELBPoolKubeconfigs(t, "dep292", tc.delivered...)

			// Every region in the target list ADMITS the pair, so the #5511
			// read-back is satisfied throughout and cannot be the reason a
			// success line goes missing. The ONLY variable across the cases is
			// how many regions the chart declares vs how many were delivered.
			hostDyn := fakeDynForConsoleTLSWithApexPorts(t, 8443, 8080)
			admitPerOrgListenersOnGet(hostDyn, "console-https-uatco", "console-http-uatco")
			hostCore := k8sfake.NewSimpleClientset(issuedOrgWildcardSecret("org-wildcard-tls-uatco-omani-homes"))

			stubs := map[string]struct {
				dyn  dynamic.Interface
				core kubernetes.Interface
			}{}
			for _, region := range tc.delivered {
				regionDyn := fakeDynForConsoleTLSWithApexPorts(t, 8443, 8080)
				admitPerOrgListenersOnGet(regionDyn, "console-https-uatco", "console-http-uatco")
				stubs["dep292-"+region+".yaml"] = struct {
					dyn  dynamic.Interface
					core kubernetes.Interface
				}{dyn: regionDyn, core: k8sfake.NewSimpleClientset()}
			}
			stubPoolRegionClients(t, stubs)

			h, logs := newPoolRegionHandler(t, "dep292", hostDyn, hostCore)
			h.provisionOrgConsoleTLS(context.Background(), uatcoRecord())

			out := logs.String()
			gotSuccess := strings.Contains(out, "listener pair admitted in every region")
			if gotSuccess != tc.wantSuccessLine {
				t.Fatalf("#6015: success line present = %t, want %t — a pool SHORTER than the declared region set is an "+
					"undelivered kubeconfig, not a satisfied precondition (a `pool == 0` check would pass here: the "+
					"kubeconfigs dir holds the region-A primary); logs:\n%s",
					gotSuccess, tc.wantSuccessLine, out)
			}
			if !tc.wantSuccessLine && !strings.Contains(out, "DECLARES more regions") {
				t.Fatalf("#6015: the shortfall is not named anywhere an operator can grep for it; logs:\n%s", out)
			}
		})
	}
}

// THE TRAP, pinned explicitly: on hw293 the kubeconfigs DIRECTORY is populated
// (one file, the region-A primary) while the region POOL is empty, because
// onDiskSecondaryKubeconfigKeys excludes the primary by design. Any guard
// phrased over the directory — "the dir is non-empty", "a kubeconfig exists" —
// reads the region-A file and passes while region B has no credential at all.
// This is why the gate above compares against the DECLARED region set.
func TestOrgConsoleTLSPool_PopulatedDirCanStillYieldAnEmptyPool_6015(t *testing.T) {
	depID := "a0077ba47e3720e5"
	dir := t.TempDir()
	t.Setenv("CATALYST_K8SCACHE_KUBECONFIGS_DIR", dir)

	// The hw293 dir, verified live: region-A primary present, secondary absent.
	mustWrite(t, filepath.Join(dir, depID+".yaml"), "apiVersion: v1\nkind: Config\n")

	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("test premise broken: want exactly 1 file on disk, got %d (err=%v)", len(entries), err)
	}
	if pool := orgConsoleTLSPoolRegions(depID); len(pool) != 0 {
		t.Fatalf("pool must be EMPTY with no SECONDARY kubeconfig on disk (the primary is excluded by design), got %v", pool)
	}

	// After delivery the same enumeration must surface the region.
	mustWrite(t, filepath.Join(dir, depID+"-me-east-215-b-1.yaml"), "apiVersion: v1\nkind: Config\n")
	pool := orgConsoleTLSPoolRegions(depID)
	if len(pool) != 1 {
		t.Fatalf("pool must hold the delivered secondary region, got %v", pool)
	}
	if _, ok := pool["me-east-215-b-1"]; !ok {
		t.Fatalf("pool key = %v, want me-east-215-b-1", pool)
	}
}

// ---------------------------------------------------------------------------
// The population seam — delivery must not be gated on the Phase-1 outcome.
// ---------------------------------------------------------------------------

// THE NEGATIVE CASE. A deployment whose Phase-1 concluded `failed` still has a
// live secondary region whose kubeconfig sits on the mother's disk. Pre-#6015
// nothing ever forwarded it: exportSecondaryKubeconfigsToChild is inside
// fireHandover (`finalStatus == "ready"`), and the only caller of
// reforwardSecondaryKubeconfigsToChild was runClusterMeshSteadyStateHeal, which
// runAutoEstablishClusterMesh reaches only after full mesh + cnpg-pair
// convergence — itself spawned only on the same ready branch.
//
// A failed HelmRelease census says NOTHING about whether region B's apiserver
// answers. On hw293 the single failure was `self-sovereign-cutover`, a chart
// that is DORMANT by design.
func TestSecondaryKubeconfigDelivery_RunsOnFailedDeployment_6015(t *testing.T) {
	depID := "a0077ba47e3720e5"
	fqdn := "hw293.omantel.biz"
	dir := t.TempDir()
	t.Setenv("CATALYST_K8SCACHE_KUBECONFIGS_DIR", dir)

	chroot := &fakeChrootServer{}
	srv := httptest.NewServer(chroot.handler())
	defer srv.Close()
	withChrootForwardClient(t, srv)

	mustWrite(t, filepath.Join(dir, depID+"-me-east-215-b-1.yaml"),
		"apiVersion: v1\nkind: Config\nclusters:\n- cluster:\n    server: https://212.72.24.6:6443\n  name: c\n")

	h := newExportTestHandler(t)
	h.secondaryKubeconfigDeliveryInterval = 5 * time.Millisecond
	dep := makeDep(depID, fqdn, []string{"me-east-215-a", "me-east-215-b"})

	// The hw293 state: Phase-1 concluded failed on one dormant chart.
	dep.mu.Lock()
	dep.Status = "failed"
	dep.mu.Unlock()

	// The cleanup must WAIT for this goroutine, not merely signal it.
	// It ticks every 5ms and reads the package-level
	// secondaryKubeconfigForwardClient (via
	// reforwardSecondaryKubeconfigsToChild); withChrootForwardClient's own
	// cleanup RESTORES that var. Cleanups run LIFO and this one is
	// registered later, so it runs first — but setting Status alone only
	// asks the loop to stop, it does not observe it stopping. Under -race
	// the restore then landed while the goroutine was still mid-tick:
	//
	//   WARNING: DATA RACE
	//   Write ... withChrootForwardClient.func2()   (this file, cleanup)
	//   Previous read ... reforwardSecondaryKubeconfigsToChild()
	//                     deployment_handover_export.go:503
	//   --- FAIL: TestSecondaryKubeconfigDelivery_RunsOnFailedDeployment_6015
	//       race detected during execution of test
	//
	// Timing-dependent, so it only fires in the full-package -race run —
	// which is exactly the required `test` gate.
	deliveryDone := make(chan struct{})
	go func() {
		defer close(deliveryDone)
		h.runSecondaryKubeconfigDelivery(dep)
	}()
	t.Cleanup(func() {
		dep.mu.Lock()
		dep.Status = "wiped" // ends the loop
		dep.mu.Unlock()
		select {
		case <-deliveryDone:
		case <-time.After(10 * time.Second):
			t.Error("runSecondaryKubeconfigDelivery did not exit after Status=wiped — " +
				"the goroutine outlives the test and races the forward-client restore")
		}
	})

	if !waitForRegionPost(chroot, "me-east-215-b-1", 3*time.Second) {
		t.Fatalf("#6015: the Sovereign never received region-b's kubeconfig for a status=failed deployment — "+
			"delivery is still gated behind the Phase-1 outcome; regionsPosted=%v", chroot.regionsPosted())
	}
}

// THE WIRING. The loop above is only worth anything if something STARTS it on
// the path hw293 actually took: Phase-1 concluding FAILED. Pre-#6015 the only
// spawn sites were inside `if outcome == OutcomeReady && finalStatus ==
// "ready"`, so this drives the REAL markPhase1Done with a failed outcome and
// asserts the Sovereign still gets its peer's kubeconfig.
//
// Without this test the hook could be deleted from phase1_watch.go and every
// other #6015 assertion would stay green — a guard that cannot fail on the
// delivered defect.
//
// CONTROL: the identical fixture with a SINGLE region must deliver NOTHING, so
// a green negative cannot be produced by a loop that simply POSTs always.
func TestMarkPhase1Done_FailedOutcomeStillStartsDelivery_6015(t *testing.T) {
	for _, tc := range []struct {
		name       string
		regions    []string
		wantPosted bool
	}{
		{name: "control: single-region Sovereign has no peer to deliver to", regions: []string{"me-east-215-a"}, wantPosted: false},
		{name: "hw293: two regions, Phase-1 concluded FAILED on one dormant chart", regions: []string{"me-east-215-a", "me-east-215-b"}, wantPosted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			depID := "a0077ba47e3720e5"
			dir := t.TempDir()
			t.Setenv("CATALYST_K8SCACHE_KUBECONFIGS_DIR", dir)

			chroot := &fakeChrootServer{}
			srv := httptest.NewServer(chroot.handler())
			defer srv.Close()
			withChrootForwardClient(t, srv)

			// Region B's kubeconfig IS on the mother's disk — the hw293 state.
			mustWrite(t, filepath.Join(dir, depID+"-me-east-215-b-1.yaml"),
				"apiVersion: v1\nkind: Config\nclusters:\n- cluster:\n    server: https://212.72.24.6:6443\n  name: c\n")

			h := NewWithPDM(silentLogger(), &fakePDM{})
			h.secondaryKubeconfigDeliveryInterval = 5 * time.Millisecond
			dep := makeDep(depID, "hw293.omantel.biz", tc.regions)
			dep.mu.Lock()
			dep.Status = "phase1-watching"
			dep.Result = &provisioner.Result{SovereignFQDN: "hw293.omantel.biz"}
			dep.mu.Unlock()
			h.deployments.Store(depID, dep)

			// The hw293 census: 64 of 65 installed, `self-sovereign-cutover`
			// (dormant at slot 06a by design) failed → OutcomeFailed.
			h.markPhase1Done(dep, map[string]string{
				"cilium":                 helmwatch.StateInstalled,
				"catalyst-platform":      helmwatch.StateInstalled,
				"self-sovereign-cutover": helmwatch.StateFailed,
			}, helmwatch.OutcomeFailed)

			dep.mu.Lock()
			status := dep.Status
			dep.mu.Unlock()
			if status != "failed" {
				t.Fatalf("precondition: markPhase1Done did not latch Status=failed (got %q) — the test would not exercise the gate it targets", status)
			}
			t.Cleanup(func() {
				dep.mu.Lock()
				dep.Status = "wiped"
				dep.mu.Unlock()
			})

			got := waitForRegionPost(chroot, "me-east-215-b-1", 3*time.Second)
			if got != tc.wantPosted {
				t.Fatalf("#6015: region-b kubeconfig delivered = %t, want %t — a failed HelmRelease census must not decide "+
					"whether a Sovereign may see its own peer region; regionsPosted=%v", got, tc.wantPosted, chroot.regionsPosted())
			}
		})
	}
}

// CONTROL: the loop must STOP once the Sovereign is being torn down, or a wiped
// deployment would keep POSTing at a dead endpoint forever. Also pins that
// `failed` is NOT a stop reason — the whole point of the fix.
func TestSecondaryKubeconfigDelivery_StopsOnlyOnTeardown_6015(t *testing.T) {
	for _, st := range []string{"wiping", "wiped", "aborted"} {
		if !secondaryKubeconfigDeliveryStopped(st) {
			t.Errorf("#6015: status %q must STOP the delivery loop", st)
		}
	}
	for _, st := range []string{"failed", "ready", "provisioning", "phase1-watching", ""} {
		if secondaryKubeconfigDeliveryStopped(st) {
			t.Errorf("#6015: status %q must NOT stop the delivery loop — it says nothing about apiserver reachability", st)
		}
	}
}

// CONTROL: a SINGLE-region deployment has no peer kubeconfig to deliver, so the
// loop must decline rather than spin. Pins that the new loop is not a fire-hose.
func TestSecondaryKubeconfigDelivery_DeclinesSingleRegion_6015(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CATALYST_K8SCACHE_KUBECONFIGS_DIR", dir)

	chroot := &fakeChrootServer{}
	srv := httptest.NewServer(chroot.handler())
	defer srv.Close()
	withChrootForwardClient(t, srv)

	depID := "dep-single"
	mustWrite(t, filepath.Join(dir, depID+"-me-east-215-b-1.yaml"), "apiVersion: v1\nkind: Config\n")

	h := newExportTestHandler(t)
	h.secondaryKubeconfigDeliveryInterval = 5 * time.Millisecond
	dep := makeDep(depID, "t99.omani.works", []string{"me-east-215-a"})

	done := make(chan struct{})
	go func() { h.runSecondaryKubeconfigDelivery(dep); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("#6015: the delivery loop must RETURN immediately for a single-region deployment, not spin")
	}
	if n := len(chroot.regionsPosted()); n != 0 {
		t.Fatalf("#6015: single-region deployment posted %d kubeconfig(s), want 0", n)
	}
}

// withChrootForwardClient routes the production forward client at the httptest
// server for the duration of the test, so the assertions bind to the REAL
// reforwardSecondaryKubeconfigsToChild rather than to a local re-implementation
// of it (a test that re-implements the production loop cannot fail on a defect
// in the production loop).
func withChrootForwardClient(t *testing.T, srv *httptest.Server) {
	t.Helper()
	prev := secondaryKubeconfigForwardClient
	secondaryKubeconfigForwardClient = func() *http.Client {
		return &http.Client{Timeout: 5 * time.Second, Transport: newRoundTripperToServer(srv)}
	}
	t.Cleanup(func() { secondaryKubeconfigForwardClient = prev })
}

func waitForRegionPost(chroot *fakeChrootServer, region string, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		for _, r := range chroot.regionsPosted() {
			if r == region {
				return true
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}
