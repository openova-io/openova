// application_controller_hostflux_delete_4902_test.go — #4902 follow-on
// regression coverage for the orphan host-flux-bootstrap cleanup gap.
//
// Defect (sibling of the fan-out-HR leak the merged #4902 fix closed):
// ensureHostFluxBootstrap imperatively upserts a host-cluster Flux
// GitRepository (`catalyst-app-<org>-<app>`) + one Kustomization per region
// (`catalyst-app-<org>-<app>-<region>`) into HostFluxNamespace
// (flux-system). Those CRs carry no ownerReferences (they live in a
// DIFFERENT namespace than the Application CR, so a same-namespace ownerRef
// would be hard-GC'd on creation), so K8s GC never reaps them; and
// handleDeletion's Gitea-file cleanup removes only the per-region manifests,
// not these source/sync CRs themselves. Before the fix, every Application
// delete therefore leaked its GitRepository + Kustomization on the host
// cluster. handleDeletion now cascade-deletes them via the app-uid
// back-pointer label (cascadeDeleteHostFluxBootstrap).
package controller

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// makeSeedHostFluxCR builds a host-flux GitRepository or Kustomization
// shaped like one ensureHostFluxBootstrap would upsert (carries the
// application + app-uid + app-namespace back-pointer labels). Used to seed a
// DECOY belonging to a different Application so the test proves the cascade
// is scoped to exactly one instance.
func makeSeedHostFluxCR(gvr schema.GroupVersionResource, kind, namespace, name, app, appUID, appNS string) *unstructured.Unstructured {
	o := &unstructured.Unstructured{}
	o.SetAPIVersion(gvr.Group + "/" + gvr.Version)
	o.SetKind(kind)
	o.SetNamespace(namespace)
	o.SetName(name)
	o.SetLabels(map[string]string{
		LabelHostFluxApp:          app,
		LabelAppUID:               appUID,
		LabelHostFluxAppNamespace: appNS,
	})
	o.Object["spec"] = map[string]interface{}{"interval": "60s"}
	return o
}

// TestReconcile_DeletionCascade_HostFluxBootstrap_4902 — the #4902 follow-on
// acceptance: after a host-placement Application's reconcile upserts its
// host-cluster Flux GitRepository + per-region Kustomization into
// flux-system, a delete of the Application MUST remove those CRs (and only
// those — a sibling Application's host-flux CRs in the same namespace must
// survive), then release the finalizer.
func TestReconcile_DeletionCascade_HostFluxBootstrap_4902(t *testing.T) {
	bp := makeBlueprint("bp-wp", "1.0.0", nil, []string{"single-region"})
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	// Host-placement Application (no vcluster tier) → the legacy per-region
	// path runs ensureHostFluxBootstrap, which authors the GitRepository +
	// Kustomization in flux-system — a DIFFERENT namespace than the
	// Application CR (`acme`), which is exactly why an ownerRef is unsafe and
	// the finalizer cascade is required.
	app := makeApp("acme", "site", "acme-prod", "bp-wp", "1.0.0", "single-region",
		[]string{"hetzner-fsn-rtz-prod"},
		map[string]interface{}{"replicas": int64(1)})

	// Decoys: a DIFFERENT Application's host-flux CRs in the SAME flux-system
	// namespace. They carry a different app-uid, so the cascade must NOT
	// touch them.
	decoyGR := makeSeedHostFluxCR(FluxGitRepositoryGVR, "GitRepository",
		"flux-system", "catalyst-app-acme-other", "other", "uid-other", "acme")
	decoyKS := makeSeedHostFluxCR(FluxKustomizationGVR, "Kustomization",
		"flux-system", "catalyst-app-acme-other-hetzner-fsn-rtz-prod", "other", "uid-other", "acme")

	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	// ADR-0015: the controller no longer CREATES this pair; it only exists
	// on Sovereigns reconciled by the pre-ADR-0015 controller. Seed it.
	grName := "catalyst-app-acme-site"
	ksName := "catalyst-app-acme-site-hetzner-fsn-rtz-prod"
	ownGR := makeSeedHostFluxCR(FluxGitRepositoryGVR, "GitRepository",
		"flux-system", grName, "site", "uid-site", "acme")
	ownKS := makeSeedHostFluxCR(FluxKustomizationGVR, "Kustomization",
		"flux-system", ksName, "site", "uid-site", "acme")
	r := newReconciler(t, fg, app, env, org, bp, decoyGR, decoyKS, ownGR, ownKS)

	// First reconcile — retires the legacy pair (scoped to this app-uid).
	reconcileFromCluster(t, r, "acme", "site")
	if _, err := r.Dynamic.Resource(FluxGitRepositoryGVR).Namespace("flux-system").
		Get(context.Background(), grName, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("legacy GitRepository %q not retired on reconcile: %v", grName, err)
	}
	if _, err := r.Dynamic.Resource(FluxKustomizationGVR).Namespace("flux-system").
		Get(context.Background(), ksName, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("legacy Kustomization %q not retired on reconcile: %v", ksName, err)
	}

	got := readApp(t, r, "acme", "site")
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

	// Reconcile again — handleDeletion must cascade-delete the host-flux CRs.
	reconcileFromCluster(t, r, "acme", "site")

	// The Application's own GitRepository + Kustomization must be gone.
	if _, err := r.Dynamic.Resource(FluxGitRepositoryGVR).Namespace("flux-system").
		Get(context.Background(), grName, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("host-flux GitRepository %q survived Application delete — orphan leak (#4902); err=%v", grName, err)
	}
	if _, err := r.Dynamic.Resource(FluxKustomizationGVR).Namespace("flux-system").
		Get(context.Background(), ksName, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("host-flux Kustomization %q survived Application delete — orphan leak (#4902); err=%v", ksName, err)
	}

	// The decoys (a DIFFERENT Application) must survive — the cascade is
	// scoped to exactly one instance by app-uid.
	if _, err := r.Dynamic.Resource(FluxGitRepositoryGVR).Namespace("flux-system").
		Get(context.Background(), "catalyst-app-acme-other", metav1.GetOptions{}); err != nil {
		t.Errorf("decoy GitRepository for a DIFFERENT Application was wrongly deleted — cascade over-reached beyond app-uid scope; err=%v", err)
	}
	if _, err := r.Dynamic.Resource(FluxKustomizationGVR).Namespace("flux-system").
		Get(context.Background(), "catalyst-app-acme-other-hetzner-fsn-rtz-prod", metav1.GetOptions{}); err != nil {
		t.Errorf("decoy Kustomization for a DIFFERENT Application was wrongly deleted — cascade over-reached beyond app-uid scope; err=%v", err)
	}

	// Finalizer must be released so the API server can GC the CR.
	got2, err := r.Dynamic.Resource(ApplicationGVR).Namespace("acme").
		Get(context.Background(), "site", metav1.GetOptions{})
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
