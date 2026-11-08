// per_org_flux.go — the per-Org Flux loop, COMMITTED TO GIT for every region
// (ADR-0015 D1).
//
// WHAT CHANGED (ADR-0015)
// -----------------------
// Before ADR-0015 this hook created the per-Org GitRepository + Kustomizations
// by API call, through the controller's own single-cluster client — so only
// the region the controller runs in ever had them. Region B had no source, no
// Kustomization and no Org namespace, and everything an Organization installed
// was invisible there (verified on hw307, 2026-11-08).
//
// Now the hook RENDERS those Flux objects once per region of the Sovereign
// (pkg/regions, the one registry) and commits each region's copy to the
// Sovereign region repo:
//
//	openova/sovereign-regions @ main
//	└── clusters/<sovereign-fqdn>/regions/<region-id>/orgs/<slug>.yaml
//
// Every region's Flux reconciles its own `clusters/<fqdn>/regions/<region-id>`
// path from its OWN Gitea (the `sovereign-region` Kustomization wired by
// clusters/_template/sovereign-region for every region at bootstrap), so every
// region gets every Organization's Flux objects. Each region's copy points at
// the in-cluster Gitea URL, which resolves to THAT region's Gitea (headless
// Service, region-local DNS), and carries a fourth Kustomization —
// `catalyst-tenant-<slug>-region-apps` — that applies the region's own
// Application folder `regions/<region-id>/apps` of the Org repo (written by
// the application-controller, the single writer of placement).
//
// The per-region source Secret name is left to the region:
// `${SOVEREIGN_REGION_GIT_SECRET:=<FluxGiteaSecretRef>}` is substituted by the
// region's `sovereign-region` Kustomization postBuild, so a region whose Gitea
// auth Secret is named differently overrides it without the controller
// knowing.
//
// NO API WRITES
// -------------
// Per Inviolable Principle #3 and ADR-0015 D1 the controller writes only Git.
// Teardown deletes the region files; each region's Flux prunes the Org's Flux
// objects (and through them the Org's trees). The one API delete left is the
// retirement of the objects the pre-ADR-0015 controller created in its own
// region, so an Organization removed before its region adopted them does not
// leak them.

package controller

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"

	"github.com/openova-io/openova/core/controllers/organization/internal/gitops"
	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
	"github.com/openova-io/openova/core/controllers/pkg/fluxsource"
	"github.com/openova-io/openova/core/controllers/pkg/gitea"
)

// perOrgTenantRepoName is the per-Org Gitea repo the vCluster manifests
// land in (must match the repoName const in organization_controller.go
// step 3). The Flux GitRepository tracks this repo.
const perOrgTenantRepoName = "catalyst-tenant"

// Defaults for the Sovereign region repo (ADR-0015 D1).
const (
	defaultSovereignRegionRepoOwner = "openova"
	defaultSovereignRegionRepo      = "sovereign-regions"
	// regionGitSecretVar is the postBuild variable each region's
	// `sovereign-region` Kustomization may set to name its Gitea auth Secret.
	regionGitSecretVar = "SOVEREIGN_REGION_GIT_SECRET"
)

// RegionGit is the Gitea surface the per-region writer needs. *gitea.Client
// satisfies it; tests inject an in-memory fake.
type RegionGit interface {
	EnsureOrg(ctx context.Context, slug, fullName, description, visibility string) (gitea.Org, error)
	EnsureRepo(ctx context.Context, org, name, description string, private bool) (gitea.Repo, error)
	PutFile(ctx context.Context, org, repo, branch, path string, data []byte, message string, opts ...gitea.PutFileOpts) (gitea.File, bool, error)
	DeleteFile(ctx context.Context, org, repo, branch, path, message string) (bool, error)
}

// fluxGitRepositoryGVK / fluxKustomizationGVK are the Flux v1 GVKs rendered.
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

// parkIfSuspended sets `spec.suspend: true` on a per-Org Flux Kustomization
// spec while the Organization carries the billing-enforcement flag
// (products/chargeback DESIGN.md §9.7). It never writes the key when the
// Organization is not suspended, so every other spec stays byte-identical.
func parkIfSuspended(spec map[string]any, org *orgapi.Organization) {
	if org != nil && org.Spec.Suspended {
		spec["suspend"] = true
	}
}

// perOrgFluxNames returns the deterministic (GitRepository, Kustomization)
// names for an Org slug.
func perOrgFluxNames(slug string) (gitRepo, kustomization string) {
	return fmt.Sprintf("catalyst-tenant-%s", slug),
		fmt.Sprintf("catalyst-tenant-%s-vcluster", slug)
}

// perOrgAppsKustomizationName — #4293 MAJOR-2: reconciles `./vcluster/apps`
// INTO the Org's vCluster (spec.kubeConfig).
func perOrgAppsKustomizationName(slug string) string {
	return fmt.Sprintf("catalyst-tenant-%s-apps", slug)
}

// perOrgHostAppsKustomizationName — #4475 §1: reconciles
// `./vcluster/host-apps` host-side onto the `<slug>` ns (no kubeConfig: a
// CiliumNetworkPolicy cannot apply inside a vanilla vcluster).
func perOrgHostAppsKustomizationName(slug string) string {
	return fmt.Sprintf("catalyst-tenant-%s-host-apps", slug)
}

// perOrgRegionAppsKustomizationName — ADR-0015 D1: reconciles the region's
// own Application folder `regions/<region-id>/apps` of the Org repo. The
// HelmReleases there carry their own namespace and (when placed in a tier
// vCluster) their own kubeConfig pivot, so this Kustomization applies
// host-side with no targetNamespace.
func perOrgRegionAppsKustomizationName(slug string) string {
	return fmt.Sprintf("catalyst-tenant-%s-region-apps", slug)
}

// perOrgKubeconfigSecretName is the flux-system Secret holding the per-Org
// vcluster's exported kubeconfig. Produced in EVERY region from that region's
// own vCluster by the boundary tree's kubeconfig-mirror.yaml (reflector pull
// of `<slug>/vc-vcluster`, gitops/region_local.go) — not only by the region-A
// provisioning funnel's imperative copy.
func perOrgKubeconfigSecretName(slug string) string {
	return fmt.Sprintf("tenant-%s-kubeconfig", slug)
}

// perOrgHomeRegion is the region that reconciles the Organization's SHARED,
// placement-less trees (`vcluster/apps`, `vcluster/host-apps`): the
// Sovereign's first region (provisioning index 0 of the one registry). That
// is where those trees have always been applied — the pre-ADR-0015 controller
// and the provisioning funnel ran there — so the installs they carry stay
// where their data already lives. ADR-0015 D1, Refs #7067.
//
// The funnel's placement-less installs name no region. Reconciled by two
// regions they would run twice (two databases; a global route splitting
// traffic between them), so they are bound to exactly one region here. A
// multi-region Application is never placement-less: the application-controller
// writes it per region under `regions/<region-id>/apps` (D1/D3). Moving a
// placement-less install to another region means re-expressing it as an
// Application with a placement — not changing this function.
func (r *Reconciler) perOrgHomeRegion() string {
	home, _ := r.Regions.At(0)
	return home
}

// perOrgSharedTreePaths returns the apps / host-apps Kustomization paths for
// one region: the shared, funnel-written trees in the home region; the
// controller-owned baseline copies (gitops/region_local.go) everywhere else.
func (r *Reconciler) perOrgSharedTreePaths(region string) (apps, hostApps string) {
	if region == r.perOrgHomeRegion() {
		return "./" + gitops.SharedAppsDir, "./" + gitops.SharedHostAppsDir
	}
	return "./" + gitops.RegionBaselineAppsDir, "./" + gitops.RegionBaselineHostAppsDir
}

// perOrgRegionFilePath is the path of one Org's Flux objects for one region
// in the Sovereign region repo.
func perOrgRegionFilePath(fqdn, region, slug string) string {
	return fmt.Sprintf("clusters/%s/regions/%s/orgs/%s.yaml", fqdn, region, slug)
}

// regionRootReadmePath seeds each region's folder so the region's
// `sovereign-region` Kustomization has a path to reconcile before the first
// Organization exists.
func regionRootReadmePath(fqdn, region string) string {
	return fmt.Sprintf("clusters/%s/regions/%s/README.md", fqdn, region)
}

// perOrgRegionAppsReadmePath seeds the region's Application folder in the Org
// repo so the region-apps Kustomization's path exists from the first commit.
func perOrgRegionAppsReadmePath(region string) string {
	return fmt.Sprintf("regions/%s/apps/README.md", region)
}

// regionGit returns the Gitea writer (RegionGit override, else GiteaClient).
func (r *Reconciler) regionGit() (RegionGit, error) {
	if r.RegionGit != nil {
		return r.RegionGit, nil
	}
	if r.GiteaClient != nil {
		return r.GiteaClient, nil
	}
	return nil, fmt.Errorf("per-Org region Flux: no Gitea client configured")
}

func (r *Reconciler) sovereignRegionRepo() (owner, repo string) {
	owner, repo = r.SovereignRegionRepoOwner, r.SovereignRegionRepo
	if owner == "" {
		owner = defaultSovereignRegionRepoOwner
	}
	if repo == "" {
		repo = defaultSovereignRegionRepo
	}
	return owner, repo
}

// regionFQDN is the Sovereign FQDN the region paths are keyed on.
func (r *Reconciler) regionFQDN(org *orgapi.Organization) string {
	if s := strings.TrimSpace(org.Spec.SovereignRef); s != "" {
		return s
	}
	return r.SovereignFQDN
}

// renderPerOrgRegionFlux renders one Organization's Flux objects for ONE
// region: the GitRepository and the four Kustomizations. Pure: no I/O.
func (r *Reconciler) renderPerOrgRegionFlux(org *orgapi.Organization, region string) ([]*unstructured.Unstructured, error) {
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
		strings.TrimRight(r.GiteaInClusterURL, "/"), slug, perOrgTenantRepoName)

	// #4285 — a Sovereign-local Gitea source MUST carry a secretRef
	// (bp-gitea REQUIRE_SIGNIN_VIEW=true). The configured name is the
	// default; the region may override it at apply time.
	secretRef := ""
	if strings.TrimSpace(r.FluxGiteaSecretRef) != "" {
		secretRef = fmt.Sprintf("${%s:=%s}", regionGitSecretVar, r.FluxGiteaSecretRef)
	}
	grSpec, err := fluxsource.BuildGitRepositorySpec(fluxsource.GitRepositorySpecInput{
		URL:             repoURL,
		Branch:          branch,
		IntervalSeconds: interval,
		SecretRef:       secretRef,
	})
	if err != nil {
		return nil, fmt.Errorf("build per-Org GitRepository spec for %s: %w", gitRepoName, err)
	}

	labels := map[string]any{
		"app.kubernetes.io/managed-by":  "organization-controller",
		"openova.io/organization":       slug,
		"openova.io/host-cluster":       r.HostCluster,
		"openova.io/sovereign":          org.Spec.SovereignRef,
		"openova.io/region":             region,
		"catalyst.openova.io/component": "per-org-vcluster-source",
	}
	obj := func(gvk schema.GroupVersionKind, name, component string, spec map[string]any) *unstructured.Unstructured {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(gvk)
		u.SetNamespace(ns)
		u.SetName(name)
		l := copyLabels(labels)
		l["catalyst.openova.io/component"] = component
		_ = unstructured.SetNestedMap(u.Object, l, "metadata", "labels")
		u.Object["spec"] = spec
		return u
	}
	sourceRef := func() map[string]any {
		return map[string]any{"kind": "GitRepository", "name": gitRepoName, "namespace": ns}
	}
	ks := func(path string, extra map[string]any) map[string]any {
		spec := map[string]any{
			"interval":      fmt.Sprintf("%ds", interval),
			"retryInterval": fmt.Sprintf("%ds", interval),
			"path":          path,
			"prune":         true,
			"sourceRef":     sourceRef(),
		}
		for k, v := range extra {
			spec[k] = v
		}
		parkIfSuspended(spec, org)
		return spec
	}

	appsPath, hostAppsPath := r.perOrgSharedTreePaths(region)

	return []*unstructured.Unstructured{
		obj(fluxGitRepositoryGVK, gitRepoName, "per-org-vcluster-source", grSpec),
		// ./vcluster → host (ns/quota/limitrange/vCluster HR + the region-local
		// kubeconfig producer carry their own namespace; no targetNamespace).
		obj(fluxKustomizationGVK, kustomizationName, "per-org-vcluster-reconciler", ks("./vcluster", nil)),
		// #4293 MAJOR-2 — the apps tree INTO the Org vCluster. The shared,
		// funnel-written ./vcluster/apps only in the home region; every other
		// region the controller-owned baseline (a placement-less install must
		// never run in two regions — perOrgSharedTreePaths).
		obj(fluxKustomizationGVK, perOrgAppsKustomizationName(slug), "per-org-apps-reconciler", ks(appsPath, map[string]any{
			"targetNamespace": slug,
			"kubeConfig": map[string]any{
				"secretRef": map[string]any{"name": perOrgKubeconfigSecretName(slug), "key": "config"},
			},
		})),
		// #4475 §1 — the host-apps tree host-side, every tier (same home-region
		// binding as the apps tree: it carries the funnel's per-app routes).
		obj(fluxKustomizationGVK, perOrgHostAppsKustomizationName(slug), "per-org-host-apps-reconciler", ks(hostAppsPath, map[string]any{
			"targetNamespace": slug,
		})),
		// ADR-0015 D1 — this region's own Application folder.
		obj(fluxKustomizationGVK, perOrgRegionAppsKustomizationName(slug), "per-org-region-apps-reconciler", ks("./regions/"+region+"/apps", nil)),
	}, nil
}

// marshalDocs serialises objects as one multi-document YAML file.
func marshalDocs(objs []*unstructured.Unstructured) ([]byte, error) {
	var buf bytes.Buffer
	for i, o := range objs {
		b, err := yaml.Marshal(o.Object)
		if err != nil {
			return nil, err
		}
		if i > 0 {
			buf.WriteString("---\n")
		}
		buf.Write(b)
	}
	return buf.Bytes(), nil
}

// reconcilePerOrgFlux commits the Organization's Flux objects for EVERY
// region of the Sovereign (ADR-0015 D1). Idempotent: PutFile short-circuits
// byte-equal content, so a steady-state Org produces zero commits.
func (r *Reconciler) reconcilePerOrgFlux(ctx context.Context, org *orgapi.Organization) error {
	if strings.TrimSpace(r.GiteaInClusterURL) == "" {
		// No in-cluster Gitea URL (unit-test Reconciler / manual install):
		// vclusterReadiness() keeps reporting Pending, the honest state.
		return nil
	}
	slug := org.Spec.Slug
	if r.Regions.Len() == 0 {
		return fmt.Errorf("per-Org region Flux for %s: the Sovereign region registry is empty (SOVEREIGN_REGIONS_JSON / SOVEREIGN_REGION_ID unset) — refusing to guess a region", slug)
	}
	fqdn := r.regionFQDN(org)
	if fqdn == "" {
		return fmt.Errorf("per-Org region Flux for %s: no Sovereign FQDN (spec.sovereignRef empty)", slug)
	}
	git, err := r.regionGit()
	if err != nil {
		return err
	}
	branch := r.Branch
	if branch == "" {
		branch = "main"
	}

	// Render every region first so a render error writes nothing.
	files := map[string][]byte{}
	for _, region := range r.Regions.IDs() {
		objs, err := r.renderPerOrgRegionFlux(org, region)
		if err != nil {
			return err
		}
		data, err := marshalDocs(objs)
		if err != nil {
			return fmt.Errorf("marshal per-Org region Flux %s/%s: %w", region, slug, err)
		}
		files[region] = data
	}

	owner, repo := r.sovereignRegionRepo()
	if _, err := git.EnsureOrg(ctx, owner, owner, "Sovereign-local platform repositories", "private"); err != nil {
		return fmt.Errorf("ensure Gitea org %s: %w", owner, err)
	}
	if _, err := git.EnsureRepo(ctx, owner, repo,
		"Per-region Flux desired state (ADR-0015 D1) — written by the Catalyst controllers, reconciled by every region's own Flux. Do not edit manually.",
		true); err != nil {
		return fmt.Errorf("ensure %s/%s: %w", owner, repo, err)
	}

	regionIDs := r.Regions.IDs()
	sort.Strings(regionIDs)
	for _, region := range regionIDs {
		readme := []byte(fmt.Sprintf("# %s — region %s\n\nReconciled by this region's own Flux (`sovereign-region` Kustomization) from this region's own Gitea. Written by the Catalyst controllers (ADR-0015 D1).\n", fqdn, region))
		if _, _, err := git.PutFile(ctx, owner, repo, branch, regionRootReadmePath(fqdn, region), readme,
			fmt.Sprintf("organization-controller: seed region %s", region)); err != nil {
			return fmt.Errorf("seed region %s: %w", region, err)
		}
		if _, _, err := git.PutFile(ctx, owner, repo, branch, perOrgRegionFilePath(fqdn, region, slug), files[region],
			fmt.Sprintf("organization-controller: Flux objects of %s in %s", slug, region)); err != nil {
			if isConcurrentWriterCASLoss(err) {
				continue // next reconcile re-verifies
			}
			return fmt.Errorf("commit per-Org Flux of %s in %s: %w", slug, region, err)
		}
		appsReadme := []byte(fmt.Sprintf("# Applications of %s in region %s\n\nOne file per Application, written by the application-controller from the Application's spec.placement (ADR-0015 D1/D3). Applied by region %s's own Flux.\n", slug, region, region))
		if _, _, err := git.PutFile(ctx, slug, perOrgTenantRepoName, branch, perOrgRegionAppsReadmePath(region), appsReadme,
			fmt.Sprintf("organization-controller: seed region %s apps folder", region)); err != nil {
			if isConcurrentWriterCASLoss(err) {
				continue
			}
			return fmt.Errorf("seed %s apps folder in %s: %w", slug, region, err)
		}
	}

	r.Log.Info("committed per-Org Flux objects for every region",
		"organization", slug, "regions", r.Regions.IDs(),
		"repo", owner+"/"+repo, "plan_slug", org.Spec.PlanSlug)
	return nil
}

// teardownPerOrgFlux removes the Organization's region files (each region's
// Flux then prunes the Org's Flux objects and, through prune:true, the Org's
// trees), then retires any pre-ADR-0015 API-created copies in this region.
// Absent-as-success throughout.
func (r *Reconciler) teardownPerOrgFlux(ctx context.Context, org *orgapi.Organization) error {
	if strings.TrimSpace(r.GiteaInClusterURL) == "" {
		return nil
	}
	slug := org.Spec.Slug
	if slug == "" {
		return nil
	}
	if fqdn := r.regionFQDN(org); fqdn != "" && r.Regions.Len() > 0 {
		git, err := r.regionGit()
		if err != nil {
			return err
		}
		owner, repo := r.sovereignRegionRepo()
		branch := r.Branch
		if branch == "" {
			branch = "main"
		}
		for _, region := range r.Regions.IDs() {
			if _, err := git.DeleteFile(ctx, owner, repo, branch, perOrgRegionFilePath(fqdn, region, slug),
				fmt.Sprintf("organization-controller: remove %s from %s", slug, region)); err != nil && !gitea.IsNotFound(err) {
				return fmt.Errorf("remove per-Org Flux of %s in %s: %w", slug, region, err)
			}
		}
	}

	ns := r.FluxNamespace
	if ns == "" {
		ns = "flux-system"
	}
	gitRepoName, kustomizationName := perOrgFluxNames(slug)
	for _, target := range []struct {
		gvk  schema.GroupVersionKind
		name string
	}{
		{fluxKustomizationGVK, perOrgRegionAppsKustomizationName(slug)},
		{fluxKustomizationGVK, perOrgHostAppsKustomizationName(slug)},
		{fluxKustomizationGVK, perOrgAppsKustomizationName(slug)},
		{fluxKustomizationGVK, kustomizationName},
		{fluxGitRepositoryGVK, gitRepoName},
	} {
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(target.gvk)
		obj.SetNamespace(ns)
		obj.SetName(target.name)
		if r.Client == nil {
			break
		}
		if err := r.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) && !isNoKindMatch(err) {
			return fmt.Errorf("delete per-Org %s %s/%s: %w", target.gvk.Kind, ns, target.name, err)
		}
	}
	r.Log.Info("torn down per-Org Flux loop in every region", "organization", slug)
	return nil
}

// isNoKindMatch tolerates a cluster (or fake client) without the Flux CRDs.
func isNoKindMatch(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no kind")
}

// copyLabels returns a shallow copy of a label map.
func copyLabels(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
