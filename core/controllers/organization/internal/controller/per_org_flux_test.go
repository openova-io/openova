// per_org_flux_test.go — PR #3700 §4.3 (deferred half of #3687). Locks the
// contract that reconciling an Organization CR find-or-creates the
// host-cluster Flux GitRepository + Kustomization that reconcile the
// per-Org `catalyst-tenant` repo's ./vcluster tree onto the host cluster —
// i.e. the wiring that makes the vCluster manifest the controller PUT to
// Gitea actually get APPLIED (closing the orphaned-bytes / phase=Pending
// gap slice 2 surfaced honestly).
package controller

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
)

// fluxScheme registers the core types + the unstructured Flux GitRepository
// and Kustomization (and their List kinds) so the fake client can serve
// Get/Create/Update/Delete on them.
func fluxScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("add client-go scheme: %v", err)
	}
	if err := orgapi.AddToScheme(s); err != nil {
		t.Fatalf("add orgapi scheme: %v", err)
	}
	for _, gvk := range []schema.GroupVersionKind{fluxGitRepositoryGVK, fluxKustomizationGVK} {
		s.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		s.AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
	}
	return s
}

func newTestOrg(slug string) *orgapi.Organization {
	o := &orgapi.Organization{}
	o.SetName(slug)
	o.Spec.Slug = slug
	o.Spec.SovereignRef = "t99.omani.works"
	o.Spec.Tier = "org"
	return o
}

func getFlux(t *testing.T, cl client.Client, gvk schema.GroupVersionKind, ns, name string) *unstructured.Unstructured {
	t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	if err := cl.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, u); err != nil {
		t.Fatalf("get %s %s/%s: %v", gvk.Kind, ns, name, err)
	}
	return u
}

func TestReconcilePerOrgFlux_CreatesGitRepositoryAndKustomization(t *testing.T) {
	const slug = "acme"
	cl := fake.NewClientBuilder().WithScheme(fluxScheme(t)).Build()

	r := &Reconciler{
		Log:                 logr.Discard(),
		HostCluster:         "hz-fsn-rtz-prod",
		GiteaInClusterURL:   "http://gitea-http.gitea.svc.cluster.local:3000",
		FluxNamespace:       "flux-system",
		FluxIntervalSeconds: 60,
		Branch:              "main",
	}
	r.Client = cl

	if err := r.reconcilePerOrgFlux(context.Background(), newTestOrg(slug)); err != nil {
		t.Fatalf("reconcilePerOrgFlux: %v", err)
	}

	grName, ksName := perOrgFluxNames(slug)

	// --- GitRepository assertions ---
	gr := getFlux(t, cl, fluxGitRepositoryGVK, "flux-system", grName)
	if got := gr.GetAPIVersion(); got != "source.toolkit.fluxcd.io/v1" {
		t.Errorf("GitRepository apiVersion = %q, want source.toolkit.fluxcd.io/v1", got)
	}
	url, _, _ := unstructured.NestedString(gr.Object, "spec", "url")
	wantURL := "http://gitea-http.gitea.svc.cluster.local:3000/acme/catalyst-tenant.git"
	if url != wantURL {
		t.Errorf("GitRepository spec.url = %q, want %q", url, wantURL)
	}
	br, _, _ := unstructured.NestedString(gr.Object, "spec", "ref", "branch")
	if br != "main" {
		t.Errorf("GitRepository spec.ref.branch = %q, want main", br)
	}
	if _, found, _ := unstructured.NestedMap(gr.Object, "spec", "secretRef"); found {
		t.Errorf("GitRepository spec.secretRef must be absent when FluxGiteaSecretRef is empty")
	}
	if org, _, _ := unstructured.NestedString(gr.Object, "metadata", "labels", "openova.io/organization"); org != slug {
		t.Errorf("GitRepository missing openova.io/organization=%s label (got %q)", slug, org)
	}

	// --- Kustomization assertions ---
	ks := getFlux(t, cl, fluxKustomizationGVK, "flux-system", ksName)
	if got := ks.GetAPIVersion(); got != "kustomize.toolkit.fluxcd.io/v1" {
		t.Errorf("Kustomization apiVersion = %q, want kustomize.toolkit.fluxcd.io/v1", got)
	}
	path, _, _ := unstructured.NestedString(ks.Object, "spec", "path")
	if path != "./vcluster" {
		t.Errorf("Kustomization spec.path = %q, want ./vcluster", path)
	}
	prune, _, _ := unstructured.NestedBool(ks.Object, "spec", "prune")
	if !prune {
		t.Errorf("Kustomization spec.prune must be true (so deleting the Org GCs the vCluster)")
	}
	srcKind, _, _ := unstructured.NestedString(ks.Object, "spec", "sourceRef", "kind")
	srcName, _, _ := unstructured.NestedString(ks.Object, "spec", "sourceRef", "name")
	if srcKind != "GitRepository" || srcName != grName {
		t.Errorf("Kustomization spec.sourceRef = {kind:%q name:%q}, want {GitRepository %s}", srcKind, srcName, grName)
	}
	// No targetNamespace — the rendered manifests carry their own namespace.
	if tns, found, _ := unstructured.NestedString(ks.Object, "spec", "targetNamespace"); found && tns != "" {
		t.Errorf("Kustomization spec.targetNamespace must be unset (manifests declare their own ns), got %q", tns)
	}

	// No cross-namespace ownerRefs (the Fix #42 trap) on either CR.
	if len(gr.GetOwnerReferences()) != 0 {
		t.Errorf("GitRepository must carry NO ownerReferences (cross-namespace GC trap)")
	}
	if len(ks.GetOwnerReferences()) != 0 {
		t.Errorf("Kustomization must carry NO ownerReferences (cross-namespace GC trap)")
	}
}

func TestReconcilePerOrgFlux_SecretRefWhenConfigured(t *testing.T) {
	const slug = "globex"
	cl := fake.NewClientBuilder().WithScheme(fluxScheme(t)).Build()
	r := &Reconciler{
		Log:                logr.Discard(),
		GiteaInClusterURL:  "http://gitea-http.gitea.svc.cluster.local:3000",
		FluxGiteaSecretRef: "catalyst-gitea-flux-auth",
	}
	r.Client = cl
	if err := r.reconcilePerOrgFlux(context.Background(), newTestOrg(slug)); err != nil {
		t.Fatalf("reconcilePerOrgFlux: %v", err)
	}
	grName, _ := perOrgFluxNames(slug)
	gr := getFlux(t, cl, fluxGitRepositoryGVK, "flux-system", grName)
	name, found, _ := unstructured.NestedString(gr.Object, "spec", "secretRef", "name")
	if !found || name != "catalyst-gitea-flux-auth" {
		t.Errorf("GitRepository spec.secretRef.name = %q (found=%v), want catalyst-gitea-flux-auth", name, found)
	}
}

func TestReconcilePerOrgFlux_NoopWhenGiteaURLUnset(t *testing.T) {
	const slug = "acme"
	cl := fake.NewClientBuilder().WithScheme(fluxScheme(t)).Build()
	r := &Reconciler{Log: logr.Discard()} // GiteaInClusterURL empty
	r.Client = cl
	if err := r.reconcilePerOrgFlux(context.Background(), newTestOrg(slug)); err != nil {
		t.Fatalf("reconcilePerOrgFlux (no-op path) returned error: %v", err)
	}
	grName, ksName := perOrgFluxNames(slug)
	// Neither CR must exist.
	gr := &unstructured.Unstructured{}
	gr.SetGroupVersionKind(fluxGitRepositoryGVK)
	if err := cl.Get(context.Background(), client.ObjectKey{Namespace: "flux-system", Name: grName}, gr); err == nil {
		t.Errorf("GitRepository must NOT be created when GiteaInClusterURL is unset")
	}
	ks := &unstructured.Unstructured{}
	ks.SetGroupVersionKind(fluxKustomizationGVK)
	if err := cl.Get(context.Background(), client.ObjectKey{Namespace: "flux-system", Name: ksName}, ks); err == nil {
		t.Errorf("Kustomization must NOT be created when GiteaInClusterURL is unset")
	}
}

func TestReconcilePerOrgFlux_IdempotentSecondPass(t *testing.T) {
	const slug = "acme"
	cl := fake.NewClientBuilder().WithScheme(fluxScheme(t)).Build()
	r := &Reconciler{
		Log:               logr.Discard(),
		GiteaInClusterURL: "http://gitea-http.gitea.svc.cluster.local:3000",
	}
	r.Client = cl
	ctx := context.Background()
	org := newTestOrg(slug)

	if err := r.reconcilePerOrgFlux(ctx, org); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	grName, _ := perOrgFluxNames(slug)
	rv1 := getFlux(t, cl, fluxGitRepositoryGVK, "flux-system", grName).GetResourceVersion()

	// Second pass on a steady-state CR must NOT rewrite the GitRepository
	// (byte-equal spec → no Update → resourceVersion unchanged).
	if err := r.reconcilePerOrgFlux(ctx, org); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	rv2 := getFlux(t, cl, fluxGitRepositoryGVK, "flux-system", grName).GetResourceVersion()
	if rv1 != rv2 {
		t.Errorf("idempotency violated: GitRepository resourceVersion changed %q → %q on a no-op second pass", rv1, rv2)
	}
}

// TestReconcile_WiresPerOrgFluxEndToEnd drives the FULL Reconcile loop
// (Keycloak + Gitea + vCluster manifests + the new step 3b) with
// GiteaInClusterURL set, and asserts the per-Org Flux GitRepository +
// Kustomization land on the host cluster. This is the integration proof
// that creating an Organization CR now closes the Flux loop — the deferred
// half of #3687 — rather than leaving the vCluster manifest as orphaned
// bytes. Uses the shared makeReconciler harness (full Gitea/Keycloak fakes).
func TestReconcile_WiresPerOrgFluxEndToEnd(t *testing.T) {
	org := sampleOrg() // slug "acme"
	r, _, _ := makeReconciler(t, org)
	r.GiteaInClusterURL = "http://gitea-http.gitea.svc.cluster.local:3000"
	r.FluxNamespace = "flux-system"
	r.FluxIntervalSeconds = 60

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "acme"},
	}); err != nil {
		t.Fatalf("full Reconcile: %v", err)
	}

	grName, ksName := perOrgFluxNames("acme")
	gr := getFlux(t, r.Client, fluxGitRepositoryGVK, "flux-system", grName)
	url, _, _ := unstructured.NestedString(gr.Object, "spec", "url")
	if url != "http://gitea-http.gitea.svc.cluster.local:3000/acme/catalyst-tenant.git" {
		t.Errorf("end-to-end: GitRepository spec.url = %q", url)
	}
	ks := getFlux(t, r.Client, fluxKustomizationGVK, "flux-system", ksName)
	if path, _, _ := unstructured.NestedString(ks.Object, "spec", "path"); path != "./vcluster" {
		t.Errorf("end-to-end: Kustomization spec.path = %q, want ./vcluster", path)
	}
}

func TestTeardownPerOrgFlux_DeletesBoth(t *testing.T) {
	const slug = "acme"
	cl := fake.NewClientBuilder().WithScheme(fluxScheme(t)).Build()
	r := &Reconciler{
		Log:               logr.Discard(),
		GiteaInClusterURL: "http://gitea-http.gitea.svc.cluster.local:3000",
	}
	r.Client = cl
	ctx := context.Background()
	org := newTestOrg(slug)

	if err := r.reconcilePerOrgFlux(ctx, org); err != nil {
		t.Fatalf("setup reconcile: %v", err)
	}
	if err := r.teardownPerOrgFlux(ctx, org); err != nil {
		t.Fatalf("teardownPerOrgFlux: %v", err)
	}
	grName, ksName := perOrgFluxNames(slug)
	gr := &unstructured.Unstructured{}
	gr.SetGroupVersionKind(fluxGitRepositoryGVK)
	if err := cl.Get(ctx, client.ObjectKey{Namespace: "flux-system", Name: grName}, gr); err == nil {
		t.Errorf("GitRepository must be deleted after teardown")
	}
	ks := &unstructured.Unstructured{}
	ks.SetGroupVersionKind(fluxKustomizationGVK)
	if err := cl.Get(ctx, client.ObjectKey{Namespace: "flux-system", Name: ksName}, ks); err == nil {
		t.Errorf("Kustomization must be deleted after teardown")
	}

	// Teardown is absent-as-success — a second teardown on already-gone CRs
	// must not error.
	if err := r.teardownPerOrgFlux(ctx, org); err != nil {
		t.Errorf("second teardown (absent) must succeed, got %v", err)
	}
}
