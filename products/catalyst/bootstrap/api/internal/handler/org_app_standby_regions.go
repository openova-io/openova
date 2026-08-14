// org_app_standby_regions.go — #6268, the MISSING PRODUCER for UAT row 60.
//
// THE GAP, MEASURED
// -----------------
// A Catalog-provisioned `active-hot-standby` Application emits two per-cluster
// HelmReleases and BOTH land in region A. Live on hw296 (dep e689e3b34a75fdec),
// Org `walkfour`, app `r60fresh`:
//
//	region-a walkfour/r60fresh-rtz-a   role=active   cluster=rtz-A
//	region-a walkfour/r60fresh-rtz-b   role=passive  cluster=rtz-B  (kubeConfig ABSENT)
//	region-b walkfour                  the Namespace does not exist
//	region-b HelmReleases for this app ZERO
//
// The standby leg therefore installs beside its own primary, in the same
// cluster and the same namespace, scaled to zero. `status.regions` reports
// region-b `ready:0 replicas:0` — literally honest, because the standby was
// asked for zero replicas in the wrong cluster.
//
// WHY THE RENDERER COULD NOT FIX IT
// ---------------------------------
// #6287 corrected two real renderer defects (a hot standby was handed the COLD
// `replicas: 0` overlay; a Roles-less multi-cluster variant rendered two
// standbys and no primary) and deliberately left the leg cold while it is
// undelivered — booting it hot in region A would install a DUPLICATE PRIMARY
// beside the active leg, which is strictly worse than an inert copy. That is
// the correct behaviour for a leg with nowhere to go, and it is why the row did
// not move: `clusterregistry.Resolver.RemoteRegionSecrets` has no producer, so
// `SecretFor("rtz-B")` from a region-A controller returns ("","") and the
// application-controller has no secondary-region writer at all.
//
// WHY THIS PRODUCER LIVES IN catalyst-api AND NOT IN THE application-controller
// ----------------------------------------------------------------------------
// Because that is where the platform already decided cross-region per-Org
// writes belong, and the mechanism is proven on this very cluster. The
// org-controller creates each Org's Flux loop through its own single-cluster
// client, so a secondary region has neither the per-Org GitRepository nor the
// tree (see org_app_surface_mesh.go's header, which states it outright: "The
// only process that can write to a secondary region is catalyst-api"). Region B
// of hw296 carries `walkone`…`walkfive` Namespaces labelled
// `catalyst.openova.io/component: org-app-crossregion` with projected Services
// inside them — written from region A by that emitter, through the same
// `orgConsoleTLSTargets` client set this file uses. Adding a second,
// independent secondary-region client stack to the application-controller
// (new RBAC, new env, new chart lockstep) would duplicate a seam that already
// works.
//
// WHAT IS DELIBERATELY NOT BUILT — the region-A-owned remote pivot
// ----------------------------------------------------------------
// The obvious alternative is to stamp a remote-region kubeconfig on the standby
// HelmRelease and leave the object in region A, so region A's helm-controller
// applies the chart into region B. It is REJECTED, not skipped.
//
// It makes region A the OWNER of region B's standby. The Helm release keeps
// running through a region-A outage, but nothing can re-apply, re-configure or
// re-render it — during precisely the outage the standby exists to survive. The
// platform's proven model is split-side: `shared-pg`'s region-B replica is
// reconciled by REGION B's OWN Flux from `bp-postgres-shared` in region-b
// `flux-system`, and region B runs a complete Flux (helm-controller Running,
// 64 `bp-*` HelmRepositories, `bootstrap-kit` Kustomization Ready — all read
// live on hw296). So the object is WRITTEN INTO region B and region B's own
// helm-controller owns it.
//
// That is also why the projected HelmRelease carries NO `spec.kubeConfig`. A
// kubeConfig block means "pivot into a DIFFERENT apiserver than the one holding
// this object". Once the object is in region B there is nothing to pivot to,
// and the source leg's block (if any) names a region-A `vc-*` Secret that does
// not exist there — so it is stripped rather than copied. The #4282 host-CNPG
// invariant is SATISFIED, not bypassed, by that: it requires the `Cluster` CR
// to land where a CNPG operator and its CRD live, and region B's HOST cluster
// runs `cnpg-system/cnpg-cloudnative-pg` with `clusters.postgresql.cnpg.io`
// registered (read live). Its over-reach was suppressing the CROSS-REGION leg's
// delivery along with the same-region vCluster pivot — see
// `application_controller.go`'s narrowed rule.
//
// ANTI-VACUITY
// ------------
// Three states are reported rather than folded into silence, because each one
// is a way this producer could appear to have run while writing nothing:
//
//   - an Application with passive legs but NO active leg — the host region is
//     then underivable, so the leg is NOT projected and the app is named;
//   - a passive leg whose cluster ID shares the active leg's region — that is
//     a same-region standby and has no cross-region delivery to make;
//   - a secondary region that is registered but whose write failed — logged at
//     Error with the region, never counted as delivered.
//
// A single-region Sovereign returns before it reads anything, so its object
// graph and API-call profile are byte-identical to before this file existed.
//
// Refs #6268 #3375 #5635 #6287.

package handler

import (
	"context"
	"fmt"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// The per-cluster fan-out labels the application-controller stamps on every
// HelmRelease it renders (core/controllers/application/internal/render/
// topology.go). This file only ever READS them, so the constants are a mirror
// of that contract, not a second source of truth for it.
const (
	fanoutLabelApp             = "catalyst.openova.io/app"
	fanoutLabelRole            = "catalyst.openova.io/role"
	fanoutLabelCluster         = "catalyst.openova.io/cluster"
	fanoutLabelTopology        = "catalyst.openova.io/topology"
	fanoutLabelStandbyDelivery = "catalyst.openova.io/standby-delivery"

	fanoutRoleActive    = "active"
	fanoutRolePassive   = "passive"
	fanoutRoleSingleton = "singleton"

	// fanoutStandbyDeliveryRemote is the value that says the standby leg
	// reached a cluster of its OWN, rather than installing beside its active
	// peer. This producer is what makes it true.
	fanoutStandbyDeliveryRemote = "remote"

	// bcpActiveHotStandby is the ONE posture whose standby streams. Every
	// other posture's standby is cold by definition, so its `replicas: 0`
	// overlay is carried through to the secondary region unchanged.
	bcpActiveHotStandby = "active-hot-standby"

	// standbyMarker is the canonical Openova standby signal. It stays TRUE on
	// a hot standby — the marker says "this leg is the standby", which is true
	// of a hot standby and a cold one alike, and charts whose standby semantic
	// is a boolean rather than an integer (CNPG `replica.enabled`) key off it.
	standbyMarker = "_openova_standby"

	// replicasKey is the COLD-standby half of the overlay: `replicas: 0` means
	// rebuild-on-failover. A replica scaled to zero cannot stream, so it is
	// removed for a hot standby and the chart's declared count applies.
	replicasKey = "replicas"
)

// orgAppStandbyComponent labels every object this producer writes, so an
// operator (and the reap below) can tell the projected standby apart from a
// GitOps-delivered or locally-rendered HelmRelease.
const orgAppStandbyComponent = "org-app-standby-crossregion"

// orgAppStandbyHelmRepositoryGVR — the Flux v1 HelmRepository the standby
// leg's chart resolves through, addressed via the dynamic client the same way
// every other seam in this package is. (The HelmRelease GVR is the package's
// existing `helmReleaseGVR` var — one declaration, not a second one.)
func orgAppStandbyHelmRepositoryGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "helmrepositories"}
}

// standbyLeg is one cross-region standby HelmRelease to project, paired with
// the region its active peer occupies (which is what makes it CROSS-region).
type standbyLeg struct {
	// HR is the source object as it stands in the host region.
	HR unstructured.Unstructured
	// App is the parent Application name (the fan-out back-pointer).
	App string
	// Topology is the resolved BCP posture label — `active-hot-standby` is the
	// only value that makes the standby hot.
	Topology string
	// ActiveRegion / StandbyRegion are the region suffixes of the canonical
	// cluster IDs (`rtz-A` → "A"). They differ, by construction.
	ActiveRegion  string
	StandbyRegion string
}

// reconcileOrgAppStandbyAcrossRegions projects one Org's CROSS-REGION standby
// HelmReleases from the host region into every secondary region, so the standby
// leg of an `active-hot-standby` Application actually exists in the region the
// placement names — reconciled there by that region's OWN Flux.
//
// Best-effort and non-gating throughout, matching its sibling
// reconcileOrgAppSurfaceAcrossRegions: a failure is logged with the region that
// failed and the next ticker pass retries. Never returns an error — the caller
// is a reconcile loop over every Org and one bad Org must not starve the rest.
func (h *Handler) reconcileOrgAppStandbyAcrossRegions(ctx context.Context, deps *sovereignDeps, rec store.OrganizationProvisionRecord) {
	if h == nil || deps == nil || deps.dyn == nil {
		return
	}
	names, ok := resolveOrgConsoleTLSNames(rec)
	if !ok {
		return
	}

	targets, _ := h.orgConsoleTLSTargets(deps)
	if len(targets) < 2 {
		// Single-region Sovereign (or a mothership, where orgConsoleTLSTargets
		// deliberately refuses to fan out). Return BEFORE any read so the
		// single-region object graph and API-call profile are unchanged.
		return
	}
	host := targets[0]

	legs, orphans := listOrgAppStandbyLegs(ctx, host.dyn, names.Slug)
	for _, app := range orphans {
		// An Application with a passive leg and NO active leg has no derivable
		// host region, so nothing here can decide where its standby belongs.
		// That is the #6287 roleFor shape (a Roles-less multi-cluster variant
		// rendering two standbys and no primary) and it must be NAMED, not
		// folded into "nothing to project".
		h.log.Error("org-app-standby: an Application has a passive leg but NO active leg, so its standby's own region cannot be derived and it is NOT projected — the placement has no primary to be a standby OF (#6268)",
			"org_tenant_id", rec.OrganizationID, "namespace", names.Slug, "app", app)
	}
	if len(legs) == 0 {
		// Nothing cross-region to deliver. Still run the reap so a leg that
		// was withdrawn in the host region does not linger in a secondary.
		h.reapOrgAppStandbyProjections(ctx, targets[1:], names, nil)
		return
	}

	keep := make(map[string]struct{}, len(legs))
	for i := range legs {
		keep[legs[i].HR.GetName()] = struct{}{}
	}

	for _, tgt := range targets[1:] {
		if err := ensureOrgBoundaryNamespaceForApps(ctx, tgt.dyn, names, rec); err != nil {
			h.log.Error("org-app-standby: could not ensure the Org boundary namespace in a secondary region — its standby legs cannot be written there and the Application stays single-region (#6268)",
				"org_tenant_id", rec.OrganizationID,
				"region", tgt.region, "clusterID", tgt.clusterID,
				"namespace", names.Slug, "err", err)
			continue
		}
		delivered := 0
		for i := range legs {
			if err := ensureStandbyChartSource(ctx, host.dyn, tgt.dyn, &legs[i].HR); err != nil {
				h.log.Error("org-app-standby: the standby leg's chart source could not be projected into a secondary region — its HelmRelease would never resolve a chart there (#6268)",
					"org_tenant_id", rec.OrganizationID,
					"region", tgt.region, "clusterID", tgt.clusterID,
					"namespace", names.Slug, "hr", legs[i].HR.GetName(), "err", err)
				continue
			}
			if err := ensureOrgAppStandbyHelmRelease(ctx, tgt.dyn, names, rec, &legs[i]); err != nil {
				h.log.Error("org-app-standby: could not write the standby HelmRelease into a secondary region — the standby leg stays absent there and the Topology tab cannot show a pair (#6268)",
					"org_tenant_id", rec.OrganizationID,
					"region", tgt.region, "clusterID", tgt.clusterID,
					"namespace", names.Slug, "hr", legs[i].HR.GetName(), "err", err)
				continue
			}
			delivered++
		}
		h.log.Info("org-app-standby: cross-region standby legs projected into a secondary region, reconciled there by that region's OWN Flux (#6268)",
			"org_tenant_id", rec.OrganizationID,
			"region", tgt.region, "clusterID", tgt.clusterID,
			"namespace", names.Slug,
			"legs", len(legs), "delivered", delivered)
	}

	h.reapOrgAppStandbyProjections(ctx, targets[1:], names, keep)
}

// listOrgAppStandbyLegs returns the CROSS-REGION standby HelmReleases in the
// Org's own boundary namespace, plus the names of Applications that carry a
// passive leg with no active leg (which the caller reports).
//
// "Cross-region" is derived from the DATA, never from an assumption about which
// region this process runs in: the legs of one Application are grouped by the
// fan-out's `catalyst.openova.io/app` label, the ACTIVE (or singleton) leg's
// canonical cluster ID supplies the host region suffix, and a passive leg
// qualifies only when its own suffix DIFFERS. A same-region standby (both legs
// `rtz-A`) is correctly excluded — there is no cross-region delivery to make.
func listOrgAppStandbyLegs(ctx context.Context, dyn dynamic.Interface, ns string) (legs []standbyLeg, orphanApps []string) {
	list, err := dyn.Resource(helmReleaseGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil || list == nil {
		return nil, nil
	}

	type appLegs struct {
		activeRegion string
		passive      []unstructured.Unstructured
	}
	byApp := map[string]*appLegs{}
	order := []string{}
	for i := range list.Items {
		item := list.Items[i]
		labels := item.GetLabels()
		app := strings.TrimSpace(labels[fanoutLabelApp])
		region := clusterIDRegionSuffix(labels[fanoutLabelCluster])
		if app == "" || region == "" {
			// Not a per-cluster fan-out HelmRelease (a bootstrap slot HR, a
			// vcluster HR, a per-Org product chart). Never a candidate.
			continue
		}
		e, ok := byApp[app]
		if !ok {
			e = &appLegs{}
			byApp[app] = e
			order = append(order, app)
		}
		switch labels[fanoutLabelRole] {
		case fanoutRoleActive, fanoutRoleSingleton:
			e.activeRegion = region
		case fanoutRolePassive:
			e.passive = append(e.passive, item)
		}
	}

	sort.Strings(order)
	for _, app := range order {
		e := byApp[app]
		if len(e.passive) == 0 {
			continue
		}
		if e.activeRegion == "" {
			orphanApps = append(orphanApps, app)
			continue
		}
		for i := range e.passive {
			hr := e.passive[i]
			standbyRegion := clusterIDRegionSuffix(hr.GetLabels()[fanoutLabelCluster])
			if standbyRegion == e.activeRegion {
				continue
			}
			legs = append(legs, standbyLeg{
				HR:            hr,
				App:           app,
				Topology:      strings.TrimSpace(hr.GetLabels()[fanoutLabelTopology]),
				ActiveRegion:  e.activeRegion,
				StandbyRegion: standbyRegion,
			})
		}
	}
	sort.Slice(legs, func(i, j int) bool { return legs[i].HR.GetName() < legs[j].HR.GetName() })
	return legs, orphanApps
}

// clusterIDRegionSuffix returns the REGION half of a canonical cluster ID
// (`rtz-B` → "B"), upper-cased so `rtz-b` and `rtz-B` compare equal — the
// same tolerance clusterregistry.Parse applies. Returns "" for anything that
// is not `<tier>-<region>`, which is how a non-fan-out HelmRelease is
// excluded without needing to enumerate what those look like.
func clusterIDRegionSuffix(clusterID string) string {
	id := strings.TrimSpace(clusterID)
	i := strings.LastIndex(id, "-")
	if i <= 0 || i == len(id)-1 {
		return ""
	}
	tier := id[:i]
	switch tier {
	case "mgmt", "dmz", "rtz":
	default:
		return ""
	}
	return strings.ToUpper(id[i+1:])
}

// ensureOrgAppStandbyHelmRelease writes one cross-region standby HelmRelease
// into a secondary region: same name, same namespace, the source spec carried
// forward with two corrections and no `spec.kubeConfig`.
//
// Upsert rather than create-if-absent. The projected object is DESIRED STATE
// owned by this producer — a chart-version bump or a parameter edit in the host
// region has to reach the standby, and a create-only write would freeze the
// standby at whatever it was on the day it first appeared. Only `spec` and the
// labels are written; `status` is left to that region's helm-controller.
func ensureOrgAppStandbyHelmRelease(ctx context.Context, dyn dynamic.Interface, names orgConsoleTLSNames, rec store.OrganizationProvisionRecord, leg *standbyLeg) error {
	spec, found, err := unstructured.NestedMap(leg.HR.Object, "spec")
	if err != nil || !found {
		return fmt.Errorf("read spec of HelmRelease %s/%s", names.Slug, leg.HR.GetName())
	}

	// The source leg's kubeConfig, when it has one, names a HOST-region
	// vCluster Secret (`vc-rtz`) that does not exist in the secondary. The
	// object is already IN the region it targets, so there is nothing left to
	// pivot into — strip it rather than carry a dangling secretRef.
	delete(spec, "kubeConfig")

	spec["values"] = standbyValuesFor(spec, leg.Topology)

	labels := orgConsoleTLSStringLabels(names, rec, orgAppStandbyComponent)
	for k, v := range leg.HR.GetLabels() {
		// Carry the fan-out's own labels so the projected object answers the
		// same questions the host-region leg does (which app, which cluster,
		// which role, which posture). The component/managed-by keys above are
		// re-applied after, so a source label can never subvert the identity
		// this producer stamps.
		labels[k] = v
	}
	for k, v := range orgConsoleTLSStringLabels(names, rec, orgAppStandbyComponent) {
		labels[k] = v
	}
	// The whole point of the write: this standby leg reached a cluster of its
	// OWN. #6287 stamps `local-undelivered` on the host-region copy precisely
	// because it did not.
	labels[fanoutLabelStandbyDelivery] = fanoutStandbyDeliveryRemote

	desired := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2",
		"kind":       "HelmRelease",
		"metadata": map[string]any{
			"name":      leg.HR.GetName(),
			"namespace": names.Slug,
			"labels":    toAnyMap(labels),
		},
		"spec": spec,
	}}

	current, err := dyn.Resource(helmReleaseGVR).Namespace(names.Slug).Get(ctx, leg.HR.GetName(), metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("read standby HelmRelease %s/%s: %w", names.Slug, leg.HR.GetName(), err)
		}
		if _, cerr := dyn.Resource(helmReleaseGVR).Namespace(names.Slug).Create(ctx, desired, metav1.CreateOptions{}); cerr != nil {
			if apierrors.IsAlreadyExists(cerr) {
				return nil
			}
			return fmt.Errorf("create standby HelmRelease %s/%s: %w", names.Slug, leg.HR.GetName(), cerr)
		}
		return nil
	}

	current.Object["spec"] = spec
	current.SetLabels(labels)
	if _, err := dyn.Resource(helmReleaseGVR).Namespace(names.Slug).Update(ctx, current, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update standby HelmRelease %s/%s: %w", names.Slug, leg.HR.GetName(), err)
	}
	return nil
}

// standbyValuesFor returns the values map the SECONDARY region's copy must
// carry, given the source leg's spec.values and the resolved posture.
//
// A HOT standby is a live, streaming replica. `replicas: 0` is the COLD
// (`active-passive`, rebuild-on-failover) semantic and a workload scaled to
// zero cannot stream, so the key is REMOVED and the chart's declared count
// applies. The `_openova_standby` marker stays true in both cases — it says
// "this leg is the standby", which is equally true of either kind, and is what
// a chart with a boolean standby semantic (CNPG `replica.enabled`) reads.
//
// A COLD standby's overlay is carried through byte-for-byte: `replicas: 0` is
// the posture the operator chose, and this producer's job is to put it in the
// right region, not to change what it is.
//
// The source map is never mutated — it belongs to the object listed from the
// host region's apiserver.
func standbyValuesFor(spec map[string]any, topology string) map[string]any {
	src, _, _ := unstructured.NestedMap(spec, "values")
	out := make(map[string]any, len(src)+1)
	for k, v := range src {
		out[k] = v
	}
	if strings.TrimSpace(topology) == bcpActiveHotStandby {
		delete(out, replicasKey)
	}
	out[standbyMarker] = true
	return out
}

// ensureStandbyChartSource projects the HelmRepository the standby leg's chart
// resolves through into the secondary region, when that region does not already
// carry one by that name.
//
// This is not defensive padding. Measured on hw296: region B's `flux-system`
// holds 64 `bp-*` HelmRepositories but NOT `openova-catalog` — which is exactly
// the source every Catalog-provisioned Application's HelmRelease references. A
// standby HelmRelease written there without it resolves no chart at all, so the
// object would exist and install nothing, which is the one outcome worse than
// the object being absent: a leg that LOOKS delivered.
//
// Create-if-absent. A secondary region that already has a source by that name
// (its own bootstrap's, or a post-cutover one pointing at the local Harbor) is
// left exactly as it is — this must never redirect a region's chart source.
func ensureStandbyChartSource(ctx context.Context, hostDyn, dstDyn dynamic.Interface, hr *unstructured.Unstructured) error {
	name, _, _ := unstructured.NestedString(hr.Object, "spec", "chart", "spec", "sourceRef", "name")
	kind, _, _ := unstructured.NestedString(hr.Object, "spec", "chart", "spec", "sourceRef", "kind")
	ns, _, _ := unstructured.NestedString(hr.Object, "spec", "chart", "spec", "sourceRef", "namespace")
	name, kind, ns = strings.TrimSpace(name), strings.TrimSpace(kind), strings.TrimSpace(ns)
	if name == "" || ns == "" {
		// No sourceRef to satisfy (an inline chartRef shape, or a malformed
		// leg). Nothing to project; the HelmRelease write below reports
		// whatever the region makes of it.
		return nil
	}
	if kind != "" && kind != "HelmRepository" {
		// A GitRepository / OCIRepository source is a different contract with
		// a different reachability story from a secondary region. Refuse to
		// guess at it rather than project a shape this producer has not
		// reasoned about.
		return fmt.Errorf("standby chart source kind %q is not a HelmRepository — not projected", kind)
	}

	if _, err := dstDyn.Resource(orgAppStandbyHelmRepositoryGVR()).Namespace(ns).Get(ctx, name, metav1.GetOptions{}); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("read HelmRepository %s/%s in the secondary region: %w", ns, name, err)
	}

	src, err := hostDyn.Resource(orgAppStandbyHelmRepositoryGVR()).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("read host-region HelmRepository %s/%s to project: %w", ns, name, err)
	}
	srcSpec, found, _ := unstructured.NestedMap(src.Object, "spec")
	if !found {
		return fmt.Errorf("host-region HelmRepository %s/%s carries no spec", ns, name)
	}
	labels := map[string]string{
		"catalyst.openova.io/component":  orgAppStandbyComponent,
		"catalyst.openova.io/managed-by": "catalyst-api",
		"app.kubernetes.io/managed-by":   "catalyst-api",
	}
	desired := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "source.toolkit.fluxcd.io/v1",
		"kind":       "HelmRepository",
		"metadata": map[string]any{
			"name":      name,
			"namespace": ns,
			"labels":    toAnyMap(labels),
		},
		"spec": srcSpec,
	}}
	// The source's pull Secret, when it has one, is a separate object this
	// producer does not copy — a secondary region provisioned by the same IaC
	// already carries the registry credential its own 64 bp-* sources use.
	if _, err := dstDyn.Resource(orgAppStandbyHelmRepositoryGVR()).Namespace(ns).Create(ctx, desired, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("project HelmRepository %s/%s into the secondary region: %w", ns, name, err)
	}
	return nil
}

// reapOrgAppStandbyProjections deletes standby HelmReleases THIS producer wrote
// into a secondary region whose source leg no longer exists in the host region.
//
// Level-triggered in both directions, matching reapOrgConsoleTLSOrphans: the
// ensure pass above is the only thing that creates these, and without a delete
// pass a topology downgrade (active-hot-standby → singleton) or an Application
// deletion would leave a live standby running in the secondary region with
// nothing left to be a standby of.
//
// Only objects carrying THIS producer's component label are candidates, so a
// HelmRelease that region owns for any other reason is never touched. `keep`
// nil means "the host region has no cross-region standby legs at all", which is
// a legitimate reap-everything instruction — it is reached only after the host
// region's HelmReleases were successfully listed.
func (h *Handler) reapOrgAppStandbyProjections(ctx context.Context, secondaries []orgConsoleTLSTarget, names orgConsoleTLSNames, keep map[string]struct{}) {
	for _, tgt := range secondaries {
		list, err := tgt.dyn.Resource(helmReleaseGVR).Namespace(names.Slug).List(ctx, metav1.ListOptions{
			LabelSelector: "catalyst.openova.io/component=" + orgAppStandbyComponent,
		})
		if err != nil || list == nil {
			continue
		}
		for i := range list.Items {
			name := list.Items[i].GetName()
			if _, ok := keep[name]; ok {
				continue
			}
			if err := tgt.dyn.Resource(helmReleaseGVR).Namespace(names.Slug).
				Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				h.log.Error("org-app-standby: could not reap a projected standby HelmRelease whose source leg is gone — it keeps running in the secondary region with nothing to be a standby of (#6268)",
					"region", tgt.region, "clusterID", tgt.clusterID,
					"namespace", names.Slug, "hr", name, "err", err)
				continue
			}
			h.log.Info("org-app-standby: reaped a projected standby HelmRelease whose source leg no longer exists in the host region (#6268)",
				"region", tgt.region, "clusterID", tgt.clusterID,
				"namespace", names.Slug, "hr", name)
		}
	}
}
