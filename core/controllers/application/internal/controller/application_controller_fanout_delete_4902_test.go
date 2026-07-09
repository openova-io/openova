// application_controller_fanout_delete_4902_test.go — #4902 regression
// coverage for the orphan-HelmRelease cleanup gap proven on hw232.
//
// Defect: deleting an Application created via the New-instance flow did
// NOT cascade-clean the per-region HelmReleases the topology fan-out
// imperatively upserted (`<app>-<cluster>`). Those HRs carry no
// ownerReferences (they can live in a DIFFERENT namespace than the
// Application CR — the vCluster pivot lands them in the tier host
// namespace — which makes same-namespace ownerRefs unsafe), so K8s GC
// never reaps them and, because the fan-out suppresses the legacy
// per-region Gitea path, nothing else did either. handleDeletion now
// cascade-deletes them via the app-uid back-pointer label.
package controller

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// makeSeedFanoutHR builds a HelmRelease shaped like one the topology
// fan-out would upsert (carries the app + app-uid + cluster back-pointer
// labels). Used to seed a DECOY belonging to a different Application so
// the test proves the cascade is scoped to exactly one instance.
func makeSeedFanoutHR(namespace, name, app, appUID, cluster string) *unstructured.Unstructured {
	hr := &unstructured.Unstructured{}
	hr.SetAPIVersion("helm.toolkit.fluxcd.io/v2")
	hr.SetKind("HelmRelease")
	hr.SetNamespace(namespace)
	hr.SetName(name)
	hr.SetLabels(map[string]string{
		"catalyst.openova.io/app":     app,
		LabelAppUID:                   appUID,
		"catalyst.openova.io/cluster": cluster,
	})
	hr.Object["spec"] = map[string]interface{}{
		"chart": map[string]interface{}{
			"spec": map[string]interface{}{"chart": app},
		},
	}
	return hr
}

// TestReconcile_DeletionCascade_FanoutHelmReleases_4902 — the central
// #4902 acceptance: after an active-hot-standby Application's fan-out
// upserts its per-region HelmReleases into the tier host namespace, a
// delete of the Application MUST remove those HRs (and only those — a
// sibling Application's HR in the same namespace must survive), then
// release the finalizer.
func TestReconcile_DeletionCascade_FanoutHelmReleases_4902(t *testing.T) {
	bp := makeBlueprintWithActiveHotStandbyTopology(
		"bp-grafana", "1.0.6", "mgmt",
		[]string{"mgmt-A", "mgmt-B"},
	)
	env := makeMultiRegionEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	// The Application CR lives in the Org namespace `acme`; its fan-out
	// HRs land in the tier host namespace `mgmt` — a DIFFERENT namespace,
	// which is exactly why an ownerRef would be unsafe and the finalizer
	// cascade is required.
	app := makeApp("acme", "obs", "acme-prod", "bp-grafana", "1.0.6",
		"active-hotstandby",
		[]string{"hetzner-fsn-rtz-prod", "hetzner-nbg-rtz-prod"},
		nil,
	)

	// Decoy: a DIFFERENT Application's fan-out HR in the SAME host ns.
	// It carries a different app-uid, so the cascade must NOT touch it.
	decoy := makeSeedFanoutHR("mgmt", "other-mgmt-a", "other", "uid-other", "mgmt-A")

	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp, decoy)

	// First reconcile — fan-out upserts 2 HRs into `mgmt` + adds finalizer.
	reconcileFromCluster(t, r, "acme", "obs")

	hrList, err := r.Dynamic.Resource(FluxHelmReleaseGVR).
		Namespace("mgmt").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list HRs in mgmt: %v", err)
	}
	ownHRs := 0
	for _, hr := range hrList.Items {
		if hr.GetLabels()["catalyst.openova.io/app"] != "obs" {
			continue
		}
		ownHRs++
		// The cascade keys off the app-uid back-pointer — assert it was
		// actually stamped by the fan-out (makeApp sets uid = "uid-obs").
		if got := hr.GetLabels()[LabelAppUID]; got != "uid-obs" {
			t.Errorf("fan-out HR %s app-uid label = %q, want uid-obs", hr.GetName(), got)
		}
	}
	if ownHRs != 2 {
		t.Fatalf("want 2 fan-out HRs for obs after first reconcile, got %d", ownHRs)
	}

	got := readApp(t, r, "acme", "obs")
	if !hasFinalizer(got, FinalizerName) {
		t.Fatal("first pass should add the controller finalizer")
	}

	// Mark the app for deletion (what `kubectl delete` does — sets
	// metadata.deletionTimestamp; the finalizer defers GC until removed).
	now := metav1.Now()
	got.SetDeletionTimestamp(&now)
	if _, err := r.Dynamic.Resource(ApplicationGVR).Namespace("acme").
		Update(context.Background(), got, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("set deletion timestamp: %v", err)
	}

	// Reconcile again — handleDeletion must cascade-delete the 2 fan-out HRs.
	reconcileFromCluster(t, r, "acme", "obs")

	afterList, err := r.Dynamic.Resource(FluxHelmReleaseGVR).
		Namespace("mgmt").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list HRs in mgmt after delete: %v", err)
	}
	decoySurvived := false
	for _, hr := range afterList.Items {
		if hr.GetLabels()[LabelAppUID] == "uid-obs" {
			t.Errorf("fan-out HR %s survived Application delete — orphan-HR leak (#4902)", hr.GetName())
		}
		if hr.GetName() == "other-mgmt-a" {
			decoySurvived = true
		}
	}
	if !decoySurvived {
		t.Error("decoy HR for a DIFFERENT Application was wrongly deleted — cascade over-reached beyond app-uid scope")
	}

	// Finalizer must be released so the API server can GC the CR.
	got2, err := r.Dynamic.Resource(ApplicationGVR).Namespace("acme").
		Get(context.Background(), "obs", metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return // already GC'd by the fake — finalizer was released.
		}
		t.Fatalf("read app after delete: %v", err)
	}
	if hasFinalizer(got2, FinalizerName) {
		t.Errorf("finalizer should be released after cascade, still present: %v", got2.GetFinalizers())
	}
}
