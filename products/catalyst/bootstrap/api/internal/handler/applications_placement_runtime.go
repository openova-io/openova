// Package handler — applications_placement_runtime.go (#3982).
//
// #3969 shipped the application-centric Placement MODEL (PlacementTarget,
// DerivePattern, the {Placement, Status} FE shape) but NOTHING populated
// the placement targets from the live runtime — so every component's
// placement was empty and `DerivePattern([])` returned `singleton`
// uniformly, even for grafana / keycloak that demonstrably run in BOTH
// regions of a 2-region prov.
//
// This file is the OTHER half: it DERIVES each component's REAL placement
// from where its workloads actually run, across BOTH region clusters the
// catalyst-api already holds in k8scache. It is purely additive — it does
// NOT touch the #3969 model files (placement_target.go / recon_status.go),
// the FE {Placement, Status} shape, or the contradiction-removal. It ADDS
// runtime-derivation.
//
//	GET /api/v1/sovereigns/{id}/applications/{name}/placement
//	  → { "targets": [ {region, cluster, vcluster, role, standbyType}, … ],
//	      "derivedFromRuntime": true }
//
// The derivation is GENERIC across every component the Topology tab renders
// — bootstrap-kit HelmReleases with NO Application CR (grafana/keycloak/
// harbor/gitea/…), CNPG pairs, components in mgmt/dmz/rtz vClusters,
// host-placed components, single-region AND multi-region — because it keys
// on the LIVE Pods (matched by the same identity the Resources tab uses),
// never on an Application CR that may not exist.
//
// Role inference (honest, never fabricated):
//   - CNPG Pods carrying the openova.io/cnpg-role label → the `primary`
//     region's target is Primary; `replica` regions are Standby·Hot (the
//     pair streams). This mirrors continuum_live.go's cnpg contract.
//   - Stateless workloads replicated across N>1 regions (grafana, keycloak)
//     → EACH region is a Primary (multi-primary / active-active) — that is
//     the truthful reading of "the same app serves traffic in both
//     regions".
//   - A genuinely single-region component → ONE target → DerivePattern
//     yields `singleton` (correct, honest — NOT the old false uniform
//     singleton).
//
// Ref #3982, #3969.
package handler

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/dynamic"

	bpv1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// runtimePlacementResponse is the body of GET
// /applications/{name}/placement. `Targets` is the #3969 PlacementTarget
// list derived from the live runtime; the FE feeds it straight into
// derivePattern. `DerivedFromRuntime` lets the FE distinguish "real,
// observed placement" from the legacy spec/status projection it falls back
// to when this endpoint could not consult the runtime.
//
// #5568 — it is true ONLY when the live k8s cache was actually queried (a
// genuine runtime observation, even if that query legitimately found zero
// targets). It MUST be false on the no-data-plane path (k8sCache == nil),
// where no runtime is consulted: pairing `targets: []` with
// `derivedFromRuntime: true` there falsely asserts the empty list was
// runtime-observed, when the branch is taken precisely because it was not —
// the exact fabrication UAT row 55 leans on this flag to rule out.
type runtimePlacementResponse struct {
	Targets            []bpv1.PlacementTarget `json:"targets"`
	DerivedFromRuntime bool                   `json:"derivedFromRuntime"`
}

// HandleApplicationPlacement — GET
// /api/v1/sovereigns/{id}/applications/{name}/placement
//
// Derives the component's REAL placement targets from the live runtime.
// Works for bootstrap HelmReleases (no Application CR) and Application-CR
// installs alike because it keys on the component's live Pods, not a CR.
func (h *Handler) HandleApplicationPlacement(w http.ResponseWriter, r *http.Request) {
	urlID := chi.URLParam(r, "id")
	name := strings.TrimSpace(chi.URLParam(r, "name"))
	if name == "" {
		writeBadRequest(w, "missing-name", "application name is required")
		return
	}
	if h.k8sCache == nil {
		// No data plane (pre-handover mothership / CI) — no runtime is
		// consulted on this path, so DerivedFromRuntime MUST be false (#5568):
		// the empty list is NOT a runtime observation, and the honest signal
		// tells the FE to keep its legacy spec/status fallback rather than
		// treat `targets: []` as an authoritative "no placement" verdict.
		writeJSON(w, http.StatusOK, runtimePlacementResponse{Targets: []bpv1.PlacementTarget{}, DerivedFromRuntime: false})
		return
	}

	primaryID := h.resolveChrootClusterID(urlID)

	// Optional namespace scoping. When provided the match is restricted
	// to the component's install namespace (the FE passes the app's
	// targetNamespace), which de-noises multi-tenant clusters. Empty =
	// match across every namespace (the safe default for bootstrap
	// components whose namespace the FE may not know).
	ns := strings.TrimSpace(r.URL.Query().Get("namespace"))

	clusterRegion := h.clusterRegionMap(urlID)

	targets := h.derivePlacementTargets(name, ns, primaryID, clusterRegion)

	// #4551 — Standby discovery for cross-region CNPG-backed components.
	//
	// derivePlacementTargets keys on the component's OWN live Pods. For an
	// app whose cross-region replica lives in a DIFFERENTLY-NAMED CNPG
	// Cluster (e.g. `<app>-replica`) and/or a SEPARATE namespace, those
	// replica Pods do not carry the app's identity labels, so the standby
	// region never surfaced as a target → the Topology DR panel's
	// `hasStandby` render gate was never satisfied (catalyst-platform,
	// shared-pg, every HR-backed app showed `singleton` / single Primary).
	//
	// The DR data plane already knows the truth: findCNPGPairForApp
	// resolves the cnpg cluster-pair backing the app (label-driven,
	// Organization-isolation-safe) and reports the replica's region.
	// Surface that as a Standby·Hot
	// target so the panel renders honestly — but ONLY when the pair is a
	// genuine 2-DISTINCT-region active-hot-standby (the same invariant
	// deriveLiveContinuumRecord enforces). Purely additive: when no live
	// pair exists, the runtime targets are returned unchanged.
	targets = h.augmentWithCNPGStandby(r, urlID, name, ns, targets)

	writeJSON(w, http.StatusOK, runtimePlacementResponse{Targets: targets, DerivedFromRuntime: true})
}

// augmentWithCNPGStandby appends a Standby·Hot target for the region-b
// half of the live CNPG cluster-pair backing `name`, when the pod-occupancy
// derivation did not already surface a Standby and a genuine 2-region pair
// exists. Generic + Organization-isolation-safe (it reuses
// findCNPGPairForApp, which only resolves an app-labelled pair across
// namespaces). Returns the input
// unchanged on any miss — no fabrication.
//
// #4551 (Standby render-gate completion): findCNPGPairForApp requires BOTH
// CNPG Cluster halves to be listable from the SAME (region-a) apiserver. In
// the real 2-region topology the primary Cluster CR lives on the region-a
// apiserver while the replica Cluster CR lives on the region-B apiserver, so
// only the primary half is visible to sovereignDynamicClient → the pair
// resolves to a lone primary → no Standby → `hasStandby:false` → the panel's
// DR section never renders. The Continuum CR (chart-seeded OR derived) lives
// in region-a and ALREADY carries the truth in spec.hotStandbyRegions. When
// the cnpg-pair path can't surface a standby we read that region off the
// Continuum CR and synthesize the Standby·Hot target — no cross-region
// replica-half read required.
func (h *Handler) augmentWithCNPGStandby(
	r *http.Request,
	urlID, name, ns string,
	targets []bpv1.PlacementTarget,
) []bpv1.PlacementTarget {
	// Already has a Standby from pod occupancy — nothing to add.
	for _, t := range targets {
		if t.Role == bpv1.DataRoleStandby {
			return targets
		}
	}
	dep, ok := h.lookupDeploymentForInfra(urlID)
	if !ok {
		return targets
	}
	client, err := h.sovereignDynamicClient(dep)
	if err != nil {
		// No in-cluster client (pre-handover / CI) — leave runtime targets.
		return targets
	}
	// The Topology route id for bootstrap-kit components is `bp-<chart>`;
	// the cnpg-pair app label carries the bare identity. Try both forms so
	// the pair resolves whether the FE passed `bp-shared-pg` or `shared-pg`.
	var st *cnpgPairState
	for _, cand := range componentNameCandidates(name) {
		if s, ferr := h.findCNPGPairForApp(r.Context(), client, cand, ns); ferr == nil && s != nil {
			st = s
			break
		}
	}
	// CNPG-pair path: only usable when BOTH halves were listable on this
	// apiserver AND they pin two DISTINCT non-empty regions (the same
	// invariant deriveLiveContinuumRecord enforces). When the replica half
	// lives on the other region's apiserver, st is nil OR carries an empty
	// ReplicaRegion — both fall through to the Continuum-CR path below.
	if st != nil && st.PrimaryRegion != "" && st.ReplicaRegion != "" && st.PrimaryRegion != st.ReplicaRegion {
		// Don't double-list a region the occupancy derivation already covers
		// as the replica region (defensive — occupancy had no Standby, but
		// the replica region could coincide with a stateless Primary
		// occupancy).
		for _, t := range targets {
			if t.Role == bpv1.DataRoleStandby && t.Region == st.ReplicaRegion {
				return targets
			}
		}
		return append(targets, bpv1.PlacementTarget{
			Region:      st.ReplicaRegion,
			Cluster:     st.ReplicaClusterName,
			VCluster:    "",
			Role:        bpv1.DataRoleStandby,
			StandbyType: bpv1.StandbyHot,
		})
	}

	// Continuum-CR fallback (the #4551 render-gate fix). The cnpg-pair path
	// could not surface a cross-region standby (replica Cluster half lives on
	// the other region's apiserver). Read the standby region straight off the
	// Continuum CR, which lives in region-a and carries it in
	// spec.hotStandbyRegions.
	return h.augmentWithContinuumStandby(r, client, name, ns, targets)
}

// augmentWithContinuumStandby synthesizes a Standby·Hot PlacementTarget from
// the app's Continuum CR `spec.hotStandbyRegions`, when no Standby is yet
// present. This is the #4551 fix: it does NOT require the cross-region
// replica `Cluster` half (which sits on the standby region's apiserver and is
// therefore NotFound from the region-a sovereignDynamicClient) — the
// Continuum CR carries the standby region locally in region-a.
//
// The CR is matched on spec.applicationRef (label-free, the controller's own
// association) OR the conventional `dr-<app>` name, tried for every
// componentNameCandidate so a `bp-`-prefixed route id resolves to a bare
// applicationRef. Returns targets unchanged when no Continuum CR (with a
// distinct, non-empty standby region) exists — no fabrication.
func (h *Handler) augmentWithContinuumStandby(
	r *http.Request,
	client dynamic.Interface,
	name, ns string,
	targets []bpv1.PlacementTarget,
) []bpv1.PlacementTarget {
	region := h.continuumStandbyRegion(r.Context(), client, name, ns, targets)
	if region == "" {
		return targets
	}
	// Don't double-list a region already represented as a Standby.
	for _, t := range targets {
		if t.Role == bpv1.DataRoleStandby && t.Region == region {
			return targets
		}
	}
	return append(targets, bpv1.PlacementTarget{
		Region:      region,
		Cluster:     "", // the standby Cluster half lives on the other region's apiserver; the region is the load-bearing field the panel renders
		VCluster:    "",
		Role:        bpv1.DataRoleStandby,
		StandbyType: bpv1.StandbyHot,
	})
}

// continuumStandbyRegion resolves the app's hot-standby region from its
// Continuum CR. It lists Continuum CRs (cluster-wide when ns is empty,
// chroot-friendly), matches by spec.applicationRef OR the `dr-<app>` name
// convention against every componentNameCandidate, and returns the first
// spec.hotStandbyRegions entry that is non-empty AND distinct from the app's
// already-derived Primary region(s) — never claiming a same-region "standby".
// Returns "" on any miss (no CR, empty list, no distinct standby).
func (h *Handler) continuumStandbyRegion(
	ctx context.Context,
	client dynamic.Interface,
	name, ns string,
	targets []bpv1.PlacementTarget,
) string {
	cr := h.findContinuumCRForApp(ctx, client, name, ns)
	if cr == nil {
		return ""
	}
	standbys, _, _ := unstructured.NestedStringSlice(cr.Object, "spec", "hotStandbyRegions")
	if len(standbys) == 0 {
		return ""
	}
	// The set of Primary regions the runtime derivation already surfaced —
	// a "standby" that equals a live Primary region is not an honest
	// cross-region standby (single-region prov, or label mismatch).
	primaryRegions := map[string]struct{}{}
	for _, t := range targets {
		if t.Role == bpv1.DataRolePrimary && t.Region != "" {
			primaryRegions[t.Region] = struct{}{}
		}
	}
	for _, s := range standbys {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, isPrimary := primaryRegions[s]; isPrimary {
			continue
		}
		return s
	}
	return ""
}

// findContinuumCRForApp locates the Continuum CR backing `name`. It lists CRs
// (scoped to ns when known, else cluster-wide) and accepts a CR whose
// spec.applicationRef equals — or whose name is `dr-<app>` for — any
// componentNameCandidate (handling the `bp-`-prefixed Topology route id vs the
// bare applicationRef). Returns nil on any miss; never errors out the panel.
func (h *Handler) findContinuumCRForApp(
	ctx context.Context,
	client dynamic.Interface,
	name, ns string,
) *unstructured.Unstructured {
	cands := componentNameCandidates(name)
	if len(cands) == 0 {
		return nil
	}
	candSet := map[string]struct{}{}
	nameSet := map[string]struct{}{}
	for _, c := range cands {
		candSet[c] = struct{}{}
		nameSet["dr-"+c] = struct{}{}
	}

	ri := client.Resource(ContinuumGVR())
	var (
		list *unstructured.UnstructuredList
		err  error
	)
	if strings.TrimSpace(ns) != "" {
		list, err = ri.Namespace(ns).List(ctx, metav1.ListOptions{})
	} else {
		list, err = ri.Namespace("").List(ctx, metav1.ListOptions{})
	}
	if err != nil || list == nil {
		return nil
	}
	for i := range list.Items {
		it := &list.Items[i]
		appRef, _, _ := unstructured.NestedString(it.Object, "spec", "applicationRef")
		if _, ok := candSet[appRef]; ok && appRef != "" {
			return it
		}
		if _, ok := nameSet[it.GetName()]; ok {
			return it
		}
	}
	return nil
}

// clusterRegionMap builds the clusterID→cloudRegion map from the
// deployment's declared Regions[], mirroring HandleTreemap's G77 #2624
// fallback. The primary cluster id (the deployment id, or the
// `sovereign-<fqdn>` self-registered chroot id) maps to the first declared
// region; each secondary `<depID>-<cloudRegion>` maps to its own region.
// Used so a region label can be attached to every cluster even when the
// Pod/Node `topology.kubernetes.io/region` label is absent (common on HCS,
// where Huawei CCM doesn't stamp it).
func (h *Handler) clusterRegionMap(urlID string) map[string]string {
	out := map[string]string{}
	var dep *Deployment
	if val, ok := h.deployments.Load(urlID); ok {
		dep = val.(*Deployment)
	} else {
		dep = h.chrootEnsureDeployment(urlID)
	}
	if dep == nil {
		return out
	}
	dep.mu.Lock()
	regs := append([]provisioner.RegionSpec(nil), dep.Request.Regions...)
	fqdn := dep.Request.SovereignFQDN
	depID := dep.ID
	dep.mu.Unlock()

	if len(regs) > 0 && regs[0].CloudRegion != "" {
		out[depID] = regs[0].CloudRegion
		out[urlID] = regs[0].CloudRegion
		if fqdn != "" {
			out["sovereign-"+fqdn] = regs[0].CloudRegion
		}
	}
	for _, rs := range regs {
		if rs.CloudRegion == "" {
			continue
		}
		out[depID+"-"+rs.CloudRegion] = rs.CloudRegion
	}
	return out
}

// placementOccupancy is the per-(region × cluster × vcluster) live presence
// of a component's workloads, accumulated across the fan-out. Role is
// inferred at the end from the cnpg signals; `anyCNPG`/`anyPrimary`/
// `anyReplica` capture the CNPG instance-role labels we observed for this
// occupancy.
type placementOccupancy struct {
	region   string
	cluster  string
	vcluster string

	anyCNPG    bool
	anyPrimary bool // observed a cnpg primary instance here
	anyReplica bool // observed a cnpg replica instance here
}

// derivePlacementTargets is the core: fan out across every registered
// cluster, list its Pods + Namespaces + Nodes, keep the Pods that belong to
// `name`, and collapse them into one PlacementTarget per (region × cluster
// × vcluster) the component actually occupies, with an HONEST role.
func (h *Handler) derivePlacementTargets(name, ns, primaryID string, clusterRegion map[string]string) []bpv1.PlacementTarget {
	// Fan out across primary + every secondary region cluster.
	clusterIDs := []string{primaryID}
	seen := map[string]struct{}{primaryID: {}}
	for _, cid := range h.k8sCache.Clusters() {
		if _, ok := seen[cid]; ok {
			continue
		}
		seen[cid] = struct{}{}
		clusterIDs = append(clusterIDs, cid)
	}

	// key = region|cluster|vcluster → occupancy.
	occ := map[string]*placementOccupancy{}
	for _, cid := range clusterIDs {
		pods, _, err := h.k8sCache.List(cid, "pod", labels.Everything())
		if err != nil {
			// A secondary that can't be listed degrades silently — render
			// N-1 regions rather than nothing. The primary's pods are the
			// floor; a missing primary just yields no targets (honest).
			continue
		}
		nsList, _, _ := h.k8sCache.List(cid, "namespace", labels.Everything())
		nodes, _, _ := h.k8sCache.List(cid, "node", labels.Everything())
		nsByName := indexByName(nsList)
		nodeByName := indexByName(nodes)

		for _, p := range pods {
			if !podBelongsToComponent(p, name, ns) {
				continue
			}
			region := podRegion(p, nsByName, nodeByName, clusterRegion[cid])
			vcluster := podVCluster(p, nsByName)
			key := region + "|" + cid + "|" + vcluster
			o := occ[key]
			if o == nil {
				o = &placementOccupancy{region: region, cluster: cid, vcluster: vcluster}
				occ[key] = o
			}
			if role := cnpgInstanceRole(p); role != "" {
				o.anyCNPG = true
				switch role {
				case cnpgRolePrimary:
					o.anyPrimary = true
				case cnpgRoleReplica:
					o.anyReplica = true
				}
			}
		}
	}

	if len(occ) == 0 {
		return []bpv1.PlacementTarget{}
	}

	// Collapse occupancies → targets with honest roles.
	//
	// CNPG path: if ANY occupancy carried a cnpg-role label, the component
	// is a stateful pair — the occupancy(ies) that hold the primary
	// instance are Primary; the rest are Standby·Hot (the pair streams).
	// Stateless path: every region the component runs in is a Primary
	// (multi-region stateless = active-active; single region = singleton).
	anyCNPG := false
	for _, o := range occ {
		if o.anyCNPG {
			anyCNPG = true
			break
		}
	}

	targets := make([]bpv1.PlacementTarget, 0, len(occ))
	for _, o := range occ {
		t := bpv1.PlacementTarget{
			Region:   o.region,
			Cluster:  o.cluster,
			VCluster: o.vcluster,
		}
		switch {
		case anyCNPG && o.anyPrimary:
			t.Role = bpv1.DataRolePrimary
		case anyCNPG && o.anyReplica:
			t.Role = bpv1.DataRoleStandby
			t.StandbyType = bpv1.StandbyHot
		case anyCNPG:
			// A CNPG occupancy whose instance-role we couldn't read (label
			// absent on these pods) — treat as a Hot standby follower so it
			// never masquerades as a second writer. Honest + conservative.
			t.Role = bpv1.DataRoleStandby
			t.StandbyType = bpv1.StandbyHot
		default:
			// Stateless: every occupied region serves traffic → Primary.
			t.Role = bpv1.DataRolePrimary
		}
		targets = append(targets, t)
	}

	// Stable, deterministic order: Primaries first, then by region/cluster/
	// vcluster — keeps the FE card order + DerivePattern repeatable.
	sort.SliceStable(targets, func(i, j int) bool {
		a, b := targets[i], targets[j]
		if (a.Role == bpv1.DataRolePrimary) != (b.Role == bpv1.DataRolePrimary) {
			return a.Role == bpv1.DataRolePrimary
		}
		if a.Region != b.Region {
			return a.Region < b.Region
		}
		if a.Cluster != b.Cluster {
			return a.Cluster < b.Cluster
		}
		return a.VCluster < b.VCluster
	})
	return targets
}

// componentNameCandidates returns the identity strings a Pod may carry for
// the component the FE asked about. The Topology tab passes the AppDetail
// ROUTE id, which for bootstrap-kit components is the Blueprint name
// `bp-<chart>` (e.g. `bp-grafana`) — the route + Dashboard lookup both key
// on the `bp-`-prefixed id (see Dashboard.test.tsx "route param is
// `bp-harbor`, NOT bare `harbor`"). But the live Pods — including the
// loft-synced ones in mgmt/dmz/rtz host namespaces — carry the BARE upstream
// chart identity (`app.kubernetes.io/instance=grafana`,
// `app.kubernetes.io/name=grafana`, in-vCluster object name `grafana-…`).
//
// #3986: the pre-#3986 matcher compared the raw `bp-grafana` against the
// bare `grafana` label/key and matched NOTHING → empty targets → false
// singleton (proven live on hw173: `…/applications/bp-grafana/placement`
// returned `{"targets":[]}` while `…/applications/grafana/placement`
// returned a target). We normalise the same way every other handler does
// (`strings.TrimPrefix(name, "bp-")`, mirroring LogsTab's
// `blueprint.replace(/^bp-/, '')` and the resource-list path) and accept
// EITHER form, so a wizard-installed app named without the prefix AND a
// bootstrap-kit `bp-`-routed app both resolve to the same pods the Resources
// tab shows. Deduped, non-empty entries only.
func componentNameCandidates(name string) []string {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	out := []string{name}
	if bare := strings.TrimPrefix(name, "bp-"); bare != "" && bare != name {
		out = append(out, bare)
	}
	return out
}

// podBelongsToComponent reports whether Pod p is part of the component
// `name`. It mirrors the Resources tab's identity join: the
// app.kubernetes.io/instance|name label (preserved verbatim by the loft
// syncer even when the host Pod NAME is mangled), the de-mangled
// in-vCluster object name, or the top-level ownerRef name. When `ns` is
// non-empty the Pod must also belong to that app namespace (host ns OR the
// loft-synced in-vCluster ns), reusing objectInAppNamespace.
//
// #3986: the FE passes the AppDetail route id, which for bootstrap-kit
// components is `bp-<chart>` while the live (incl. loft-synced) Pods carry
// the BARE chart identity. We therefore match against BOTH the raw and the
// `bp-`-stripped form (componentNameCandidates) so grafana et al. — synced
// as `grafana-…-x-grafana-x-mgmt-vcluster` in host ns `mgmt` but still
// labelled `app.kubernetes.io/{instance,name}=grafana` — resolve instead of
// yielding a false singleton.
func podBelongsToComponent(p *unstructured.Unstructured, name, ns string) bool {
	if p == nil || name == "" {
		return false
	}
	if ns != "" && !objectInAppNamespace(p, ns) {
		return false
	}
	// nil ReplicaSet index: this join has no cache handle, so a Pod owned
	// by a ReplicaSet keeps the ReplicaSet name (pre-#5485 behavior). The
	// instance/name labels below carry the match in the cases this
	// function is called for.
	appKey := applicationKey(p, nil)
	chartName := p.GetLabels()["app.kubernetes.io/name"]
	// The de-mangled in-vCluster object name (loft annotation), e.g. a host
	// Pod `grafana-…-x-grafana-x-mgmt-vcluster` carries object-name
	// `grafana-…`. The instance/name labels match in the common case; this
	// resolves charts that omit the instance label via the synced
	// Deployment/StatefulSet owner name prefix.
	displayName := vClusterSyncedDisplayName(p)
	for _, cand := range componentNameCandidates(name) {
		if appKey == cand {
			return true
		}
		if chartName == cand {
			return true
		}
		if displayName != "" && strings.HasPrefix(displayName, cand) {
			return true
		}
	}
	return false
}

// podRegion derives the region a Pod runs in: pod label → namespace label →
// host Node's region labels → the cluster's declared cloudRegion fallback.
// Mirrors buildPodRows' region join (the dashboard's proven path) so the
// Topology tab and the treemap agree on region naming.
func podRegion(p *unstructured.Unstructured, nsByName, nodeByName map[string]*unstructured.Unstructured, clusterCloudRegion string) string {
	if v := p.GetLabels()["openova.io/region"]; v != "" {
		return v
	}
	if ns, ok := nsByName[p.GetNamespace()]; ok {
		if v := ns.GetLabels()["openova.io/region"]; v != "" {
			return v
		}
	}
	if nodeName, _, _ := unstructured.NestedString(p.Object, "spec", "nodeName"); nodeName != "" {
		if n, ok := nodeByName[nodeName]; ok {
			nl := n.GetLabels()
			if v := nl["openova.io/region"]; v != "" {
				return v
			}
			if v := nl["topology.kubernetes.io/region"]; v != "" {
				return v
			}
			if v := nl["failure-domain.beta.kubernetes.io/region"]; v != "" {
				return v
			}
		}
	}
	return clusterCloudRegion
}

// podVCluster derives the vCluster tier a Pod sits in (mgmt|dmz|rtz, or ""
// for host-placed). For a loft-synced Pod the host namespace IS the tier
// (mgmt/dmz/rtz) and the loft annotation confirms it; otherwise the host
// namespace's catalyst.openova.io/vcluster-role label carries it (same join
// buildPodRows uses). Empty = host-placed (no vCluster).
func podVCluster(p *unstructured.Unstructured, nsByName map[string]*unstructured.Unstructured) string {
	hostNS := p.GetNamespace()
	for _, tier := range vClusterHostSyncNamespaces {
		if hostNS == tier {
			return tier
		}
	}
	if ns, ok := nsByName[hostNS]; ok {
		if v := ns.GetLabels()["catalyst.openova.io/vcluster-role"]; v != "" {
			return v
		}
	}
	return ""
}

// cnpgInstanceRole returns the CNPG instance role of a Pod (primary|replica)
// from the openova.io/cnpg-role label the bp-cnpg-pair / bp-postgres-shared
// charts stamp on each half. Empty for non-CNPG Pods. Mirrors the
// continuum_live.go cnpg contract.
func cnpgInstanceRole(p *unstructured.Unstructured) string {
	lbls := p.GetLabels()
	if v := lbls[cnpgRoleLabel]; v == cnpgRolePrimary || v == cnpgRoleReplica {
		return v
	}
	// CNPG's own operator label (upstream) — a secondary signal when the
	// OpenOva pair-role label is absent on a shared/standalone Cluster.
	if v := lbls["cnpg.io/instanceRole"]; v == "primary" || v == "replica" {
		return v
	}
	if v := lbls["role"]; v == "primary" || v == "replica" {
		// Legacy CNPG label; only trust it when paired with a cnpg cluster
		// label so a generic `role` label on an unrelated pod can't leak in.
		if lbls["cnpg.io/cluster"] != "" {
			return v
		}
	}
	return ""
}

// indexByName builds a name→object map for the namespace/node join lists.
func indexByName(objs []*unstructured.Unstructured) map[string]*unstructured.Unstructured {
	m := make(map[string]*unstructured.Unstructured, len(objs))
	for _, o := range objs {
		if o != nil {
			m[o.GetName()] = o
		}
	}
	return m
}
