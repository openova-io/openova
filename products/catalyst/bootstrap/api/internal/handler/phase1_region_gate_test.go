package handler

// Tests for the every-region gate (#7902 defect 6) through the Phase-1 watch:
// `ready` means every region, and the hand-over fires only then. The gate's
// own steps (the wait for the cross-region work, the watch of each region,
// what counts as installed) are tested in internal/crossregion.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

const gateFollowerRegion = "hel1-1"

// gateFixture is a two-region deployment whose primary watch reads every
// primary release Ready. Each region has its own fake cluster for the gate's
// direct List.
type gateFixture struct {
	h           *Handler
	dep         *Deployment
	primaryDyn  dynamic.Interface
	followerDyn dynamic.Interface
}

func gateHR(name, readyStatus, reason string) *unstructured.Unstructured {
	return buildTestHelmRelease(name, hrCondition("Ready", readyStatus, reason, time.Now().Add(-time.Minute)))
}

func newGateFixture(t *testing.T, id string, followerObjs ...*unstructured.Unstructured) *gateFixture {
	t.Helper()
	dir := t.TempDir()
	primaryPath := filepath.Join(dir, id+".yaml")
	// The follower's file is deliberately NOT in kubeconfigsDir: the gate
	// finds it through the record's own map, and no follower watcher is
	// spawned (this fixture tests the gate, which judges by a direct List).
	followerPath := filepath.Join(dir, "follower", id+"-"+gateFollowerRegion+".yaml")
	if err := os.MkdirAll(filepath.Dir(followerPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, p := range []string{primaryPath, followerPath} {
		if err := os.WriteFile(p, []byte("fake-kubeconfig: yaml"), 0o600); err != nil {
			t.Fatalf("write kubeconfig: %v", err)
		}
	}

	h := NewWithPDM(silentLogger(), &fakePDM{})
	// The primary watch's cluster. It also answers the hand-over's wait for
	// the wildcard certificate (a List of Certificates, none here), which is
	// kept to milliseconds.
	h.dynamicFactory = func(string) (dynamic.Interface, error) {
		return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
			newFakeSchemeForHandler(),
			map[schema.GroupVersionResource]string{
				helmwatch.HelmReleaseGVR: "HelmReleaseList",
				certificateGVR:           "CertificateList",
			},
			makeReadyHR("bp-cilium"), makeReadyHR("bp-keycloak"), makeReadyHR("bp-catalyst-platform"),
		), nil
	}
	h.handoverCertWaitTimeout = 50 * time.Millisecond
	h.handoverCertPollInterval = 10 * time.Millisecond
	h.phase1WatchTimeout = 5 * time.Second
	// The cross-region loop is not under test here; the tests state its
	// result (crossRegionLanded) themselves.
	h.suppressPostHandoverHooks = true
	h.phase1RegionGateBudget = 400 * time.Millisecond
	h.SetHandoverSigner(loadTestSigner(t))

	primaryDyn := newFakeKustomizationDynClient(t)
	followerDyn := newFakeKustomizationDynClient(t)
	ctx := context.Background()
	for _, name := range []string{"bp-cilium", "bp-keycloak", "bp-catalyst-platform"} {
		if _, err := primaryDyn.Resource(helmReleaseGVR).Namespace(fluxSystemNamespace).
			Create(ctx, gateHR(name, "True", "InstallSucceeded"), metav1.CreateOptions{}); err != nil {
			t.Fatalf("seed primary %s: %v", name, err)
		}
	}
	for _, o := range followerObjs {
		res := helmReleaseGVR
		if o.GetKind() == "Kustomization" {
			res = fluxKustomizationGVR
		}
		if _, err := followerDyn.Resource(res).Namespace(fluxSystemNamespace).Create(ctx, o, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seed follower %s: %v", o.GetName(), err)
		}
	}
	restore := installClusterMeshDynamicClientFactory(map[string]dynamic.Interface{
		primaryPath:  primaryDyn,
		followerPath: followerDyn,
	})
	t.Cleanup(restore)

	dep := &Deployment{
		ID:        id,
		Status:    "phase1-watching",
		StartedAt: time.Now(),
		eventsCh:  make(chan provisioner.Event, 512),
		done:      make(chan struct{}),
		Request: provisioner.Request{
			SovereignFQDN: "gate." + id + ".example",
			OrgEmail:      "owner@test.example",
			Regions: []provisioner.RegionSpec{
				{Provider: "hetzner", CloudRegion: "fsn1"},
				{Provider: "hetzner", CloudRegion: "hel1"},
			},
		},
		Result: &provisioner.Result{
			SovereignFQDN:  "gate." + id + ".example",
			KubeconfigPath: primaryPath,
		},
		OwnerEmail:               "owner@test.example",
		secondaryKubeconfigPaths: map[string]string{gateFollowerRegion: followerPath},
	}
	h.deployments.Store(id, dep)
	return &gateFixture{h: h, dep: dep, primaryDyn: primaryDyn, followerDyn: followerDyn}
}

// TestRunPhase1Watch_NotReadyWhileFollowerRegionIsInstalling is the defect
// as the founder saw it on 15 November 2026: the primary region is installed,
// the follower has a release installing and one failed. Before the repair
// this record became `ready` at once and its hand-over fired.
func TestRunPhase1Watch_NotReadyWhileFollowerRegionIsInstalling(t *testing.T) {
	fx := newGateFixture(t, "gate-follower-short",
		gateHR("bp-cilium", "True", "InstallSucceeded"),
		gateHR("bp-keycloak", "False", "InstallFailed"),
		gateHR("bp-gitea", "Unknown", "Progressing"),
	)
	fx.dep.crossRegionLanded.Set()

	fx.h.runPhase1Watch(fx.dep)

	fx.dep.mu.Lock()
	defer fx.dep.mu.Unlock()
	if fx.dep.Status == "ready" {
		t.Fatalf("Status = ready while region %s has a release installing and one failed: ready must mean every region", gateFollowerRegion)
	}
	if fx.dep.Result.HandoverFiredAt != nil || fx.dep.Result.HandoverURL != "" {
		t.Errorf("the hand-over fired (at %v) although the deployment is not ready", fx.dep.Result.HandoverFiredAt)
	}
	if got := fx.dep.Result.Phase1Outcome; got != helmwatch.OutcomeTimeout {
		t.Errorf("Phase1Outcome = %q, want %q (the recoverable classification: nothing is torn down)", got, helmwatch.OutcomeTimeout)
	}
	for _, want := range []string{gateFollowerRegion, "bp-keycloak (failed)", "bp-gitea (installing)", "Ready means every region"} {
		if !strings.Contains(fx.dep.Error, want) {
			t.Errorf("Error does not name %q:\n%s", want, fx.dep.Error)
		}
	}
	// The record's own census shows the follower as it is.
	var follower *provisioner.RegionHealth
	for i := range fx.dep.Result.Regions {
		if fx.dep.Result.Regions[i].Region == gateFollowerRegion {
			follower = &fx.dep.Result.Regions[i]
		}
	}
	if follower == nil {
		t.Fatalf("the follower region is missing from Result.Regions: %+v", fx.dep.Result.Regions)
	}
	if follower.HRReady != 1 || follower.HRTotal != 3 {
		t.Errorf("follower census = %d/%d, want 1/3", follower.HRReady, follower.HRTotal)
	}
}

// TestRunPhase1Watch_ReadyWhenEveryRegionIsInstalled: every region installed
// and the cross-region work landed gives `ready` and the hand-over.
func TestRunPhase1Watch_ReadyWhenEveryRegionIsInstalled(t *testing.T) {
	fx := newGateFixture(t, "gate-every-region",
		gateHR("bp-cilium", "True", "InstallSucceeded"),
		gateHR("bp-keycloak", "True", "InstallSucceeded"),
	)
	fx.dep.crossRegionLanded.Set()

	fx.h.runPhase1Watch(fx.dep)

	fx.dep.mu.Lock()
	defer fx.dep.mu.Unlock()
	if fx.dep.Status != "ready" {
		t.Fatalf("Status = %q (error %q), want ready: every region is installed", fx.dep.Status, fx.dep.Error)
	}
	if fx.dep.Result.HandoverFiredAt == nil {
		t.Errorf("the hand-over did not fire on a ready deployment")
	}
	if fx.dep.Result.SecondaryDegraded {
		t.Errorf("SecondaryDegraded is set on a deployment whose every region is installed")
	}
	if fx.dep.Result.Phase1Substate != "" {
		t.Errorf("Phase1Substate = %q after the watch ended, want it cleared", fx.dep.Result.Phase1Substate)
	}
	if fx.dep.regionGateActive {
		t.Errorf("the gate is still marked active after the watch ended")
	}
}

// TestRunPhase1Watch_NotReadyBeforeTheCrossRegionWorkLanded: every release
// reads Ready, but the regions are not meshed and the database pair is not
// switched on. The switch renders the shared engines again in both regions;
// a `ready` given before it would be followed by releases installing.
func TestRunPhase1Watch_NotReadyBeforeTheCrossRegionWorkLanded(t *testing.T) {
	fx := newGateFixture(t, "gate-no-cross-region",
		gateHR("bp-cilium", "True", "InstallSucceeded"),
		gateHR("bp-keycloak", "True", "InstallSucceeded"),
	)

	fx.h.runPhase1Watch(fx.dep)

	fx.dep.mu.Lock()
	defer fx.dep.mu.Unlock()
	if fx.dep.Status == "ready" {
		t.Fatalf("Status = ready before the cross-region work landed")
	}
	if !strings.Contains(fx.dep.Error, "not meshed yet or the cross-region database pair is not switched on") {
		t.Errorf("Error does not say what is missing:\n%s", fx.dep.Error)
	}
	if fx.dep.Result.HandoverFiredAt != nil {
		t.Errorf("the hand-over fired although the deployment is not ready")
	}
}

// TestRunConvergedLateRescue_FollowerRegionShortStaysFailed: the check that
// flips a timed-out record to ready on restart counted the primary region
// alone. It must not undo the gate.
func TestRunConvergedLateRescue_FollowerRegionShortStaysFailed(t *testing.T) {
	origCensus := censusHelmReleases
	censusHelmReleases = func(string) (int, int, map[string]bool, error) {
		return 66, 66, map[string]bool{}, nil
	}
	defer func() { censusHelmReleases = origCensus }()

	run := func(t *testing.T, id string, followerObjs ...*unstructured.Unstructured) *Deployment {
		t.Helper()
		fx := newGateFixture(t, id, followerObjs...)
		fx.dep.mu.Lock()
		fx.dep.Status = "failed"
		fx.dep.Result.Phase1Outcome = helmwatch.OutcomeTimeout
		fx.dep.mu.Unlock()
		fx.h.runConvergedLateRescue(fx.dep)
		return fx.dep
	}

	short := run(t, "late-follower-short", gateHR("bp-keycloak", "False", "InstallFailed"))
	short.mu.Lock()
	if short.Status != "failed" || short.Result.HandoverFiredAt != nil {
		t.Errorf("status=%q handoverFiredAt=%v: a record whose follower region has a failed release was flipped to ready", short.Status, short.Result.HandoverFiredAt)
	}
	short.mu.Unlock()

	whole := run(t, "late-follower-whole", gateHR("bp-keycloak", "True", "InstallSucceeded"))
	whole.mu.Lock()
	if whole.Status != "ready" {
		t.Errorf("status=%q, want ready: every region is installed", whole.Status)
	}
	whole.mu.Unlock()
}
