// per_org_flux.go — organization-controller hook that closes the
// per-Org vCluster Flux loop (PR #3700 §4.3, the deferred half of #3687).
//
// THE GAP THIS CLOSES
// -------------------
// The Reconcile loop (organization_controller.go step 3) renders the
// vCluster Namespace + HelmRelease + kustomization index and PutFiles them
// into the per-Org `<slug>/catalyst-tenant` Gitea repo. But NOTHING on the
// host cluster watched that repo: the design comment said a Flux
// Kustomization at `clusters/<host>/tenants/<slug>/` was "a one-off, set up
// by the Sovereign-admin once" — a manual step that never ran on a fresh
// prov. So the manifest sat as orphaned bytes in Gitea, the vCluster HR was
// never applied, and `vclusterReadiness()` correctly reported phase=Pending
// ("manifest authored in Gitea; awaiting a Flux source/Kustomization to
// reconcile it") forever. Slice 2 of #3700 made the status HONEST about the
// gap; this file CLOSES it.
//
// WHAT THIS DOES
// --------------
// On every reconcile of a non-deleting Organization CR, find-or-create two
// host-cluster Flux v1 resources (mirroring the application-controller's
// ensureHostFluxBootstrap precedent, which wires Gitea→Flux→workload the
// same way):
//
//	GitRepository  (1 per Org)  — flux-system/catalyst-tenant-<slug>
//	  spec.url:        <GiteaInClusterURL>/<slug>/catalyst-tenant.git
//	  spec.ref.branch: <Branch> (default "main")
//	  spec.interval:   <FluxIntervalSeconds>s
//	  spec.secretRef:  <FluxGiteaSecretRef> (only when non-empty)
//
//	Kustomization  (1 per Org)  — flux-system/catalyst-tenant-<slug>-vcluster
//	  spec.path:       ./vcluster
//	  spec.sourceRef:  GitRepository catalyst-tenant-<slug>
//	  spec.prune:      true
//	  spec.interval:   <FluxIntervalSeconds>s
//	  (NO targetNamespace — the rendered Namespace + HR carry their own
//	   `namespace: <slug>`; the Kustomization applies them to the HOST
//	   cluster, where the vCluster control plane runs as a StatefulSet.)
//
// Once these exist, Flux clones the per-Org repo, applies ./vcluster, the
// vCluster HR reconciles, and vclusterReadiness() walks Pending →
// Provisioning → Ready on its own 30s requeue cadence. The Org becomes a
// LIVE vCluster, not a green badge over orphaned bytes.
//
// REGISTRATION-ONLY, NOT AUTHORITY OVER WORKLOADS
// -----------------------------------------------
// Per Inviolable Principle #3 (Flux is the only reconciler) this hook only
// authors the Flux CR pair — it never `kubectl apply`s the Namespace or the
// HR itself. Flux owns the apply.
//
// NO CROSS-NAMESPACE ownerReferences
// ----------------------------------
// The Flux CRs live in flux-system; the Organization CR is cluster-scoped.
// A cluster-scoped owner CAN own a namespaced dependent, BUT the
// application-controller learned the hard way (Fix #42, live on omantel)
// that mixing owner/dependent across the namespace boundary trips the K8s
// GC into silently deleting the dependent. We follow the same proven
// idiom: NO ownerRefs, reference labels instead, and the deletion path
// (teardownPerOrgFlux) explicitly Deletes the CR pair when the Org is
// removed.
//
// FAILURE SEMANTICS
// -----------------
// Best-effort + non-fatal: a Flux-CR write failure does NOT fail the whole
// Org reconcile (Keycloak group + Gitea Org + vCluster manifest already
// landed). We log + surface via the returned error so the caller requeues,
// matching the iac-bootstrap precedent.

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
)

// perOrgTenantRepoName is the per-Org Gitea repo the vCluster manifests
// land in (must match the repoName const in organization_controller.go
// step 3). The Flux GitRepository tracks this repo.
const perOrgTenantRepoName = "catalyst-tenant"

// fluxGitRepositoryGVK / fluxKustomizationGVK are the Flux v1 GVKs the
// hook find-or-creates. Flux 2.4+ deprecated v1beta2; we pin v1 to match
// the application-controller (FluxGitRepositoryGVR / FluxKustomizationGVR)
// and the chart's catalog-sovereign / org-tenants Flux templates.
var (
	fluxGitRepositoryGVK = schema.GroupVersionKind{
		Group:   "source.toolkit.fluxcd.io",
		Version: "v1",
		Kind:    "GitRepository",
	}
	fluxKustomizationGVK = schema.GroupVersionKind{
		Group:   "kustomize.toolkit.fluxcd.io",
		Version: "v1",
		Kind:    "Kustomization",
	}
)

// perOrgFluxNames returns the deterministic (GitRepository, Kustomization)
// names for an Org slug. Keyed only off slug → generic for any Org, no
// per-org-name special-casing (#3687 §7d).
func perOrgFluxNames(slug string) (gitRepo, kustomization string) {
	return fmt.Sprintf("catalyst-tenant-%s", slug),
		fmt.Sprintf("catalyst-tenant-%s-vcluster", slug)
}

// reconcilePerOrgFlux find-or-creates the host-cluster Flux GitRepository +
// Kustomization that reconcile the per-Org `catalyst-tenant` repo's
// ./vcluster tree onto the host cluster — bringing the vCluster HR the
// controller authored in Gitea actually UP (PR #3700 §4.3).
//
// Idempotent: a steady-state Org produces zero API writes after the first
// pass (every write is a find-or-create with a spec-drift short-circuit).
// Returns a non-nil error iff a write failed in a way that should requeue;
// the caller treats this as non-fatal for the Org's other outputs.
func (r *Reconciler) reconcilePerOrgFlux(ctx context.Context, org *orgapi.Organization) error {
	if strings.TrimSpace(r.GiteaInClusterURL) == "" {
		// No in-cluster Gitea URL configured (e.g. a unit-test Reconciler
		// that only exercises the Keycloak/Gitea-org code, or a deployment
		// that intentionally leaves the per-Org Flux loop to a manual
		// one-off). Skip silently — vclusterReadiness() keeps reporting
		// phase=Pending, which is the honest state.
		return nil
	}

	slug := org.Spec.Slug
	ns := r.FluxNamespace
	if ns == "" {
		ns = "flux-system"
	}
	branch := r.Branch
	if branch == "" {
		branch = "main"
	}
	interval := r.FluxIntervalSeconds
	if interval <= 0 {
		interval = 60
	}
	gitRepoName, kustomizationName := perOrgFluxNames(slug)

	repoURL := fmt.Sprintf("%s/%s/%s.git",
		strings.TrimRight(r.GiteaInClusterURL, "/"),
		slug,
		perOrgTenantRepoName)

	// Reference labels (NOT ownerRefs — see top-of-file note). The
	// deletion path looks these up; an operator can also list every
	// per-Org Flux CR via the organization label selector.
	labels := map[string]any{
		"app.kubernetes.io/managed-by":  "organization-controller",
		"openova.io/organization":       slug,
		"openova.io/host-cluster":       r.HostCluster,
		"openova.io/sovereign":          org.Spec.SovereignRef,
		"catalyst.openova.io/component": "per-org-vcluster-source",
	}

	// --- GitRepository ---
	gr := &unstructured.Unstructured{}
	gr.SetGroupVersionKind(fluxGitRepositoryGVK)
	gr.SetNamespace(ns)
	gr.SetName(gitRepoName)
	if err := unstructured.SetNestedMap(gr.Object, copyLabels(labels), "metadata", "labels"); err != nil {
		return fmt.Errorf("set GitRepository labels: %w", err)
	}
	grSpec := map[string]any{
		"interval": fmt.Sprintf("%ds", interval),
		"url":      repoURL,
		"ref": map[string]any{
			"branch": branch,
		},
	}
	// REQUIRE_SIGNIN_VIEW=true on bp-gitea makes anonymous clone 401 even
	// for the per-Org repo — when an operator wires a Flux basic-auth
	// Secret (the catalyst-gitea-token PAT), reference it so source-
	// controller can authenticate. Empty == anonymous (matches the
	// application-controller's FluxGiteaSecretRef default).
	if s := strings.TrimSpace(r.FluxGiteaSecretRef); s != "" {
		grSpec["secretRef"] = map[string]any{"name": s}
	}
	if err := unstructured.SetNestedMap(gr.Object, grSpec, "spec"); err != nil {
		return fmt.Errorf("set GitRepository spec: %w", err)
	}
	if err := r.upsertFluxResource(ctx, ns, gitRepoName, gr); err != nil {
		return fmt.Errorf("upsert per-Org GitRepository %s/%s: %w", ns, gitRepoName, err)
	}

	// --- Kustomization (./vcluster → host cluster) ---
	ks := &unstructured.Unstructured{}
	ks.SetGroupVersionKind(fluxKustomizationGVK)
	ks.SetNamespace(ns)
	ks.SetName(kustomizationName)
	ksLabels := copyLabels(labels)
	ksLabels["catalyst.openova.io/component"] = "per-org-vcluster-reconciler"
	if err := unstructured.SetNestedMap(ks.Object, ksLabels, "metadata", "labels"); err != nil {
		return fmt.Errorf("set Kustomization labels: %w", err)
	}
	ksSpec := map[string]any{
		"interval":      fmt.Sprintf("%ds", interval),
		"retryInterval": fmt.Sprintf("%ds", interval),
		"path":          "./vcluster",
		"prune":         true,
		// NO targetNamespace: the rendered Namespace + HelmRelease carry
		// their own `namespace: <slug>` (gitops.Render). The vCluster
		// control plane runs on the HOST cluster (a StatefulSet in the
		// <slug> namespace); applying without a targetNamespace honors the
		// per-resource namespace the manifests declare.
		"sourceRef": map[string]any{
			"kind":      "GitRepository",
			"name":      gitRepoName,
			"namespace": ns,
		},
	}
	if err := unstructured.SetNestedMap(ks.Object, ksSpec, "spec"); err != nil {
		return fmt.Errorf("set Kustomization spec: %w", err)
	}
	if err := r.upsertFluxResource(ctx, ns, kustomizationName, ks); err != nil {
		return fmt.Errorf("upsert per-Org Kustomization %s/%s: %w", ns, kustomizationName, err)
	}

	r.Log.Info("reconciled per-Org vCluster Flux loop",
		"organization", slug,
		"git_repository", fmt.Sprintf("%s/%s", ns, gitRepoName),
		"kustomization", fmt.Sprintf("%s/%s", ns, kustomizationName),
		"url", repoURL,
		"branch", branch)
	return nil
}

// teardownPerOrgFlux deletes the per-Org Flux CR pair when the Organization
// is being removed. Because the Flux CRs carry NO ownerRef (cross-namespace
// GC trap — see top-of-file note), the K8s garbage collector won't reap
// them; we delete explicitly. Absent-as-success (a missing CR is fine —
// either never created, or a prior teardown already removed it).
func (r *Reconciler) teardownPerOrgFlux(ctx context.Context, org *orgapi.Organization) error {
	if strings.TrimSpace(r.GiteaInClusterURL) == "" {
		return nil
	}
	slug := org.Spec.Slug
	if slug == "" {
		return nil
	}
	ns := r.FluxNamespace
	if ns == "" {
		ns = "flux-system"
	}
	gitRepoName, kustomizationName := perOrgFluxNames(slug)

	// Delete the Kustomization first so Flux stops reconciling the tree
	// before the source disappears (avoids a transient source-not-found
	// error on the Kustomization's last reconcile). prune:true means the
	// vCluster Namespace + HR are GC'd by Flux as the Kustomization is
	// removed.
	for _, target := range []struct {
		gvk  schema.GroupVersionKind
		name string
	}{
		{fluxKustomizationGVK, kustomizationName},
		{fluxGitRepositoryGVK, gitRepoName},
	} {
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(target.gvk)
		obj.SetNamespace(ns)
		obj.SetName(target.name)
		if err := r.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete per-Org %s %s/%s: %w", target.gvk.Kind, ns, target.name, err)
		}
	}
	r.Log.Info("torn down per-Org vCluster Flux loop", "organization", slug)
	return nil
}

// upsertFluxResource find-or-creates a Flux CR via the controller-runtime
// client, then restores spec when an existing instance has drifted. Mirrors
// the application-controller's upsertHostResource contract: idempotent on a
// byte-equal spec, preserves the Flux controller's status/resourceVersion.
func (r *Reconciler) upsertFluxResource(ctx context.Context, ns, name string, desired *unstructured.Unstructured) error {
	current := &unstructured.Unstructured{}
	current.SetGroupVersionKind(desired.GroupVersionKind())
	err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, current)
	if err != nil {
		if apierrors.IsNotFound(err) {
			if cerr := r.Create(ctx, desired); cerr != nil {
				if apierrors.IsAlreadyExists(cerr) {
					// Race with a parallel reconcile — re-Get + update.
					return r.upsertFluxResource(ctx, ns, name, desired)
				}
				return cerr
			}
			return nil
		}
		return err
	}
	// Drift check: only Update when desired.spec differs from current.spec.
	desiredSpec, _, _ := unstructured.NestedMap(desired.Object, "spec")
	currentSpec, _, _ := unstructured.NestedMap(current.Object, "spec")
	if specsEqualJSON(desiredSpec, currentSpec) {
		return nil
	}
	current.Object["spec"] = desiredSpec
	if l, found, _ := unstructured.NestedMap(desired.Object, "metadata", "labels"); found {
		_ = unstructured.SetNestedMap(current.Object, l, "metadata", "labels")
	}
	return r.Update(ctx, current)
}

// copyLabels returns a shallow copy of a label map so the GitRepository and
// Kustomization don't share (and mutate) the same underlying map.
func copyLabels(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// specsEqualJSON deep-compares two spec maps by JSON-marshaled byte
// equality (the encoder sorts keys, so the comparison is stable). Used to
// short-circuit a no-op Update when an existing Flux CR's spec already
// matches desired.
func specsEqualJSON(a, b map[string]any) bool {
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	return string(aj) == string(bj)
}
