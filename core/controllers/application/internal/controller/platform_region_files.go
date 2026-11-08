package controller

// platform_region_files.go — ADR-0015 D8 for PLATFORM Applications.
//
// The platform's own database pairs (bp-cnpg-pair `cnpg/cnpg-pair`, bp-postgres
// `shared-data/shared-pg-b`, `shared-pg-c`) are installed by bootstrap-kit slots
// (16b, 16c/16d), not by an Org repo: the platform databases back the very
// Gitea that serves Git-delivered files, so their HelmReleases stay in the slot
// — the one install path. Their Applications are bootstrap-owned
// (spec.bootstrap, adopted, never rendered). `shared-data/shared-pg` (slot 16a)
// is NEVER Git-delivered: it holds Gitea's own database, and a transition is a
// series of Gitea commits — after traffic-off demotes it, the data-primary
// commit could not be written (hostsGiteaDatabase).
//
// What is delivered from Git is the ONE per-region delta: the topology
// contract. Each region's file in the Sovereign region repo
//
//	openova/sovereign-regions @ main
//	└── clusters/<fqdn>/regions/<region-id>/platform/<app>.yaml
//	      ConfigMap flux-system/<slot-hr>-topology   (regionfile carrier:
//	        values.yaml = openova.topology{mode,self,primary,peers}
//	                      [+ database.topology.promotionToken])
//	      Continuum <ns>/dr-<app>                    (desired primary, regions,
//	        transition.repo = this repo + PlatformLayout,
//	        transition.helmRelease = the slot HelmRelease)
//
// is applied by that region's existing `sovereign-region` Flux Kustomization
// (clusters/_template/sovereign-region). The slot HelmRelease merges the
// ConfigMap through `valuesFrom` (optional: a fresh Sovereign installs before
// the file exists, from the slot's own cloud-init defaults), labelled
// `reconcile.fluxcd.io/watch: Enabled` so helm-controller reconciles the moment
// the file changes. Continuum then moves the pair with the same phase machine
// and 3-commit token protocol as an Org Application — its lease lives at
// clusters/<fqdn>/continuum/transitions/<group>.yaml in the same repo.
//
// OPT-IN is the slot itself: a platform Application is Git-delivered only when
// its slot HelmRelease declares the carrier in `valuesFrom` — i.e. when its
// chart consumes the contract. A bootstrap-owned DR app whose chart does not
// (the spine gitea/harbor/keycloak/openbao) gets NO Continuum: one that cannot
// move anything would only report a transition it can never complete. The
// former API creation of these Continuums is deleted; an API-created leftover
// is retired.
//
// ADOPTION (first file): the initial primary is the region whose CNPG member
// is the writer NOW, read from this region's own Cluster — never the placement
// default — so adopting a live pair never moves its writer. Cluster names are
// the slot's existing names (the charts pin them per region; see the charts'
// contract mapping), so adoption renames nothing.

import (
	"context"
	"errors"
	"fmt"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	topo "github.com/openova-io/openova/core/controllers/application/internal/render"
	"github.com/openova-io/openova/core/controllers/internal/placement"
	bpv1alpha1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
	"github.com/openova-io/openova/core/controllers/pkg/gitea"
	"github.com/openova-io/openova/core/controllers/pkg/regionfile"
)

// fluxWatchLabel makes helm-controller (Flux ≥2.4) reconcile a HelmRelease
// as soon as a ConfigMap it reads through valuesFrom changes.
const fluxWatchLabel = "reconcile.fluxcd.io/watch"

// platformBranch is the Sovereign region repo branch every region tracks.
const platformBranch = "main"

// slotTopologyCarrier reports whether the slot HelmRelease reads the region
// file's carrier ConfigMap through valuesFrom (the platform opt-in).
func slotTopologyCarrier(hr *unstructured.Unstructured) bool {
	if hr == nil {
		return false
	}
	want := regionfile.TopologyValuesConfigMapName(hr.GetName())
	vf, _, _ := unstructured.NestedSlice(hr.Object, "spec", "valuesFrom")
	for _, e := range vf {
		m, ok := e.(map[string]interface{})
		if !ok {
			continue
		}
		if m["kind"] == "ConfigMap" && m["name"] == want {
			return true
		}
	}
	return false
}

// platformRegions returns the regions of a platform Application in registry
// order: the placement's own regions when they all resolve to registry ids
// (and there are ≥2), else every region of the Sovereign — a platform pair
// spans the Sovereign; bootstrap-owned specs carry placeholder region tokens.
func (r *Reconciler) platformRegions(spec appSpec) []string {
	declared := spec.PlacementRegions
	if len(declared) == 0 {
		declared = spec.Regions
	}
	set := map[string]bool{}
	for _, d := range declared {
		id, err := r.Cfg.Regions.Canonical(d)
		if err != nil {
			set = nil
			break
		}
		set[id] = true
	}
	out := []string{}
	for _, id := range r.Cfg.Regions.IDs() {
		if set == nil || len(set) < 2 || set[id] {
			out = append(out, id)
		}
	}
	return out
}

// platformDesiredPrimary is the placement's Primary target, else the first
// region (the Sovereign's provisioning-order primary).
func (r *Reconciler) platformDesiredPrimary(spec appSpec, regions []string) string {
	for _, t := range spec.PlacementTargets {
		if t.Role != bpv1alpha1.DataRolePrimary {
			continue
		}
		if id, err := r.Cfg.Regions.Canonical(t.Region); err == nil && containsString(regions, id) {
			return id
		}
	}
	if len(regions) == 0 {
		return ""
	}
	return regions[0]
}

// livePrimary reads which region's CNPG member is the writer NOW, from THIS
// region's own Cluster of the slot release. "" when it cannot be told (no
// local Cluster, no region id, or more than one other region).
func (r *Reconciler) livePrimary(ctx context.Context, hr *unstructured.Unstructured, regions []string) string {
	local := r.Cfg.LocalRegionID
	if local == "" || !containsString(regions, local) {
		return ""
	}
	release, _, _ := unstructured.NestedString(hr.Object, "spec", "releaseName")
	ns, _, _ := unstructured.NestedString(hr.Object, "spec", "targetNamespace")
	if release == "" || ns == "" {
		return ""
	}
	l, err := r.Dynamic.Resource(CNPGClusterGVR).Namespace(ns).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/instance=" + release})
	if err != nil || len(l.Items) != 1 {
		return ""
	}
	isPrimary, known := cnpgMemberIsPrimary(&l.Items[0])
	if !known {
		return ""
	}
	if isPrimary {
		return local
	}
	others := []string{}
	for _, id := range regions {
		if id != local {
			others = append(others, id)
		}
	}
	if len(others) != 1 {
		return ""
	}
	return others[0]
}

// cnpgMemberIsPrimary classifies one CNPG Cluster by its own spec: no replica
// stanza ⇒ primary; `replica.enabled` ⇒ its negation (the legacy replica-
// cluster shape); else the distributed topology's self == primary.
func cnpgMemberIsPrimary(c *unstructured.Unstructured) (primary, known bool) {
	rep, found, _ := unstructured.NestedMap(c.Object, "spec", "replica")
	if !found || len(rep) == 0 {
		return true, true
	}
	if en, ok := rep["enabled"].(bool); ok {
		return !en, true
	}
	self, _ := rep["self"].(string)
	prim, _ := rep["primary"].(string)
	if self == "" || prim == "" {
		return false, false
	}
	return self == prim, true
}

// renderPlatformRegionFiles builds every region's file: the carrier ConfigMap
// and the Continuum.
func renderPlatformRegionFiles(app *unstructured.Unstructured, hr *unstructured.Unstructured, mode, primary string, regions []string, g transitionGovernance) (map[string][]byte, error) {
	out := make(map[string][]byte, len(regions))
	for _, self := range regions {
		peers := []interface{}{}
		for _, p := range regions {
			if p != self {
				peers = append(peers, p)
			}
		}
		values := map[string]interface{}{"openova": map[string]interface{}{"topology": map[string]interface{}{
			"mode": mode, "self": self, "primary": primary, "peers": peers,
		}}}
		cm, err := regionfile.TopologyValuesConfigMap(regionfile.TopologyValuesConfigMapName(hr.GetName()), hr.GetNamespace(),
			map[string]string{topo.LabelApp: app.GetName(), topo.LabelRegion: self, fluxWatchLabel: "Enabled"}, values)
		if err != nil {
			return nil, err
		}
		cr := renderRegionContinuum(app.GetName(), app.GetNamespace(), primary, regions, g)
		l := cr.GetLabels()
		l[topo.LabelRegion] = self
		cr.SetLabels(l)
		b, err := regionfile.Marshal([]*unstructured.Unstructured{cm, cr})
		if err != nil {
			return nil, err
		}
		out[self] = b
	}
	return out, nil
}

// reconcilePlatformRegionFiles delivers a bootstrap-owned Application's
// topology from Git (see the file comment). Returns the `<ns>/<name>` of its
// Continuum when the Application is Git-delivered, "" otherwise.
func (r *Reconciler) reconcilePlatformRegionFiles(ctx context.Context, app, hr *unstructured.Unstructured) (string, error) {
	spec, err := parseSpec(app)
	if err != nil {
		return "", nil
	}
	mode := spec.Placement
	switch mode {
	case string(bpv1alpha1.BcpActiveHotStandby), string(bpv1alpha1.BcpActivePassive):
	default:
		return "", r.retireAPIContinuum(ctx, app)
	}
	if hostsGiteaDatabase(spec) {
		// A transition is a series of Gitea commits; once traffic-off demoted
		// the pair that holds Gitea's own database, the data-primary commit
		// could never be written. Never Git-deliver such a pair.
		r.Log.Warn("platform Application hosts the Gitea database; its topology cannot move through Gitea commits — not delivered",
			"app", app.GetNamespace()+"/"+app.GetName())
		return "", r.retireAPIContinuum(ctx, app)
	}
	if !slotTopologyCarrier(hr) {
		// The slot's chart does not consume the contract: nothing could move
		// it. No Continuum (and the pre-D8 API-created one is retired).
		return "", r.retireAPIContinuum(ctx, app)
	}
	if r.Cfg.SovereignFQDN == "" {
		r.Log.Warn("platform Application opts into Git-delivered topology but SOVEREIGN_FQDN is unset; not delivered",
			"app", app.GetNamespace()+"/"+app.GetName())
		return "", nil
	}
	regions := r.platformRegions(spec)
	if len(regions) < 2 {
		return "", nil // a single-region Sovereign has no transition to deliver
	}
	bp, berr := r.fetchBlueprint(ctx, spec.BlueprintName)
	if berr != nil {
		if isNotFound(berr) {
			return "", nil // Blueprint not in the catalog yet — next pass
		}
		return "", fmt.Errorf("fetch Blueprint %q: %w", spec.BlueprintName, berr)
	}
	choice, variant, terr := topo.ResolveTopology(mode, parseBlueprintTopology(bp), len(regions))
	if terr != nil {
		r.Log.Warn("platform Application: topology resolve failed; not delivered",
			"app", app.GetName(), "blueprint", spec.BlueprintName, "err", terr)
		return "", nil
	}
	desired := r.platformDesiredPrimary(spec, regions)
	plan := placement.Plan{Mode: mode, PrimaryRegion: desired}
	for _, id := range regions {
		if id == desired {
			plan.Regions = append(plan.Regions, placement.RegionPlan{Name: id, Role: placement.RolePrimary})
		} else {
			plan.Regions = append(plan.Regions, placement.RegionPlan{Name: id, Role: placement.RoleStandby, Standby: true})
		}
	}
	cPlan, ok := buildContinuumPlan(app.GetNamespace()+"/"+app.GetName(), choice, variant, plan, map[string]string{
		"catalyst.openova.io/app-uid": string(app.GetUID()),
	})
	if !ok {
		return "", r.retireAPIContinuum(ctx, app)
	}
	if org := app.GetLabels()["catalyst.openova.io/organization"]; org != "" {
		cPlan.OwnerLabels["catalyst.openova.io/organization"] = org
	}

	owner, repo := r.Cfg.SovereignRegionRepoOwner, r.Cfg.SovereignRegionRepo
	if owner == "" {
		owner = "openova"
	}
	if repo == "" {
		repo = "sovereign-regions"
	}
	layout := regionfile.PlatformLayout(r.Cfg.SovereignFQDN)
	if _, err := r.Gitea.EnsureRepo(ctx, owner, repo, "Sovereign region repo (ADR-0015 D1)", true); err != nil {
		return "", fmt.Errorf("ensure %s/%s: %w", owner, repo, err)
	}

	// The initial primary of a FIRST file is the live writer (adoption never
	// moves it); once any file exists commitGovernedRegionFiles keeps what
	// the files name, and Continuum alone moves it toward `desired`.
	initial := desired
	if !r.platformFilesExist(ctx, owner, repo, layout, app.GetName()) {
		if live := r.livePrimary(ctx, hr, regions); live != "" {
			initial = live
		}
	}
	g := transitionGovernance{
		plan: cPlan, repoOwner: owner, repoName: repo, branch: platformBranch,
		layout: layout, helmRelease: hr.GetNamespace() + "/" + hr.GetName(), scope: "platform",
	}
	files, err := renderPlatformRegionFiles(app, hr, mode, initial, regions, g)
	if err != nil {
		return "", err
	}
	// The Continuum's desired primary is the placement's, independently of
	// the files' current primary.
	files, err = withDesired(files, app.GetName(), desired)
	if err != nil {
		return "", err
	}
	if err := r.commitGovernedRegionFiles(ctx, app, owner, repo, platformBranch, layout, files); err != nil {
		return "", err
	}
	return app.GetNamespace() + "/" + ContinuumNameFor(app.GetName()), nil
}

// withDesired sets spec.desiredPrimaryRegion of the Continuum in every file.
func withDesired(files map[string][]byte, appName, desired string) (map[string][]byte, error) {
	out := make(map[string][]byte, len(files))
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		objs, err := regionfile.Parse(files[k])
		if err != nil {
			return nil, err
		}
		for _, o := range objs {
			if o.GetKind() == ContinuumKind && o.GetName() == ContinuumNameFor(appName) {
				if err := unstructured.SetNestedField(o.Object, desired, "spec", "desiredPrimaryRegion"); err != nil {
					return nil, err
				}
			}
		}
		b, err := regionfile.Marshal(objs)
		if err != nil {
			return nil, err
		}
		out[k] = b
	}
	return out, nil
}

func (r *Reconciler) platformFilesExist(ctx context.Context, owner, repo string, layout regionfile.Layout, app string) bool {
	for _, id := range r.Cfg.Regions.IDs() {
		if _, err := r.Gitea.GetFile(ctx, owner, repo, platformBranch, layout.AppPath(id, app)); err == nil {
			return true
		} else if !errors.Is(err, gitea.ErrFileNotFound) && !(r.GiteaErrors != nil && r.GiteaErrors.IsNotFound(err)) {
			// Unknown state: treat as existing so a read error can never
			// re-seed the primary.
			return true
		}
	}
	return false
}

// hostsGiteaDatabase reports whether the data instance declares a binding
// consumed by bp-gitea (spec.parameters.databases[].consumer.blueprint).
func hostsGiteaDatabase(spec appSpec) bool {
	dbs, _ := spec.Parameters["databases"].([]interface{})
	for _, d := range dbs {
		m, _ := d.(map[string]interface{})
		c, _ := m["consumer"].(map[string]interface{})
		if c["blueprint"] == "bp-gitea" {
			return true
		}
	}
	return false
}

// retireAPIContinuum deletes a `dr-<app>` Continuum the pre-D8
// application-controller created through the API. A Continuum another owner
// applies (Helm, a Flux Kustomization) is left alone.
func (r *Reconciler) retireAPIContinuum(ctx context.Context, app *unstructured.Unstructured) error {
	name := ContinuumNameFor(app.GetName())
	cur, err := r.Dynamic.Resource(ContinuumGVR).Namespace(app.GetNamespace()).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("get Continuum %s/%s: %w", app.GetNamespace(), name, err)
	}
	l := cur.GetLabels()
	if l["app.kubernetes.io/managed-by"] == "Helm" || l["kustomize.toolkit.fluxcd.io/name"] != "" {
		return nil
	}
	return r.deleteContinuumCRIfExists(ctx, app.GetName(), app.GetNamespace())
}
