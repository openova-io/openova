// post_handover_spine_apps.go — the SPINE Application-CR PRODUCER seam
// (#4212 Seam 3, folds #3829; #3969 placement.targets[] rides on it).
//
// THE BREAK THIS CLOSES. The object-model/DR backbone is keyed off the
// Application CR: the application-controller's Reconcile fan-out resolves
// the Blueprint topology, and — for any DR-capable Application — mints one
// `Continuum.dr.openova.io` CR per app (core/controllers/application/
// internal/controller/continuum.go reconcileContinuumCR). But the
// bootstrap SPINE (openbao / keycloak / harbor / gitea) installs as bare
// Flux HelmReleases with NO companion Application CR — the ONLY producer of
// Application CRs today is the signup / Org-install path (applications.go
// installApplicationCore). So the spine never entered the reconcile fan-out
// and `kubectl get applications.apps.openova.io -A` listed only Org / signup
// apps + the shared-pg CNPG pairs — never the DR-capable spine. The
// continuum-controller therefore had nothing to drive for the spine, and
// `kubectl get continuums.dr.openova.io -A` carried no spine row.
//
// This hook is that producer. After Phase-1 reaches OutcomeReady it, for
// each DR-capable spine HelmRelease present on the Sovereign:
//  1. composes one idempotent Application CR (spec.blueprintRef → the spine
//     Blueprint, spec.environmentRef → the Sovereign control-plane env,
//     spec.regions[] → the deployment's declared regions),
//  2. SERVER-SIDE APPLIES it onto the Sovereign cluster via the kubeconfig
//     the cloud-init posted back.
//
// ADOPT, never roll (Invariant #3). The CR carries a
// `catalyst.openova.io/adopts-helmrelease` label naming the EXISTING spine
// HR. The application-controller's upsertHostResource contract is byte-equal
// short-circuiting, so stamping the CR over an already-converged spine HR
// ADOPTS it (label/own) — it NEVER re-renders or rolls a healthy spine pod.
// The CR is purely additive: it makes the spine VISIBLE to the object model
// + mints its Continuum contract; it does not change the spine's install.
//
// Observe / autoFailover:false locked (Invariant #4). The produced
// Continuum CR defaults to k8s-lease witness + autoFailover:false (set by
// the continuum-controller / continuum.go DefaultContinuumLeaseKind) —
// lighting Healthy + lease-holder status WITHOUT arming unattended
// switchover.
//
// Idempotent + retrying: re-running on an already-enrolled Sovereign is a
// no-op server-side-apply merge. The Application CRD ships via the catalyst
// chart (slot 0) and is present the moment catalyst-api is up, but the apply
// retries until it registers (bounded) for parity with the adoption applier.
// Failures log + emit an SSE warn but never fail the handover (spine
// enrollment is a day-2 object-model surface, not on the bootstrap critical
// path).
//
// Run on a background goroutine from runPhase1Watch's OutcomeReady terminal
// block (phase1_watch.go) + the converged-late path (phase1_converged_late.go),
// alongside runPostHandoverAdoptionApply.
package handler

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// spineComponent describes one DR-capable bootstrap-spine component the
// producer enrolls. `Chart` is the bare upstream chart identity; `HRName`
// is the Flux HelmRelease name (always `bp-<chart>` in flux-system — the
// canonical bootstrap-kit convention, same as jobs_retry's reconcile
// target). `BlueprintName` / `BlueprintVersion` pin the catalog Blueprint
// the application-controller fetches to resolve the topology variant that
// drives the Continuum contract.
//
// These four are the spine set the EPIC #4212 acceptance walk asserts:
// "every DR-capable spine app (openbao/keycloak/harbor/gitea) carries a CR;
// zero DR-capable HR on the no-CR fallback". The list is keyed off the
// Blueprints that declare a `spec.topology` block with a switchover
// mechanism (platform/{openbao,keycloak,harbor,gitea}/blueprint.yaml) — the
// SAME gate buildContinuumPlan applies controller-side, so a spine app whose
// Blueprint is not DR-capable produces no Continuum CR even once enrolled.
type spineComponent struct {
	Chart            string
	HRName           string
	BlueprintName    string
	BlueprintVersion string

	// MultiRegionMode is the canonical placement mode this spine
	// component resolves to on a MULTI-region Sovereign — it mirrors the
	// Blueprint's `spec.topology.defaults.multi-region`
	// (platform/<chart>/blueprint.yaml). The producer stamps this verbatim
	// onto the Application CR's spec.placement when the deployment carries
	// ≥2 regions (singleton otherwise). This is load-bearing: the
	// application-controller's per-app Continuum producer
	// (continuum.go buildContinuumPlan) mints a Continuum CR ONLY when the
	// resolved placement plan carries a standby region — i.e. an
	// active-passive / active-hot-standby plan. Omitting placement made the
	// controller derive `singleton` (the spine Blueprints declare no
	// placementSchema.defaultOnMultiRegion), so the plan had no standby and
	// NO Continuum CR was ever minted — the producer-write reached no
	// consumer. Stamping the explicit multi-region mode here closes that
	// half of Seam 3 so the write actually flows to the Continuum reader.
	MultiRegionMode string

	// Config is the minimal spec.config the producer must stamp so the
	// Application passes the Blueprint configSchema validation in the
	// application-controller (#4402). Most spine Blueprints declare no
	// required config; bp-keycloak's live catalog-seed configSchema has
	// `required: [realmName]`, so a spine-keycloak CR with no config Fails
	// validation (`missing properties: 'realmName'`) BEFORE it can reach the
	// Continuum producer. The spine adopts the already-installed bootstrap
	// HR (catalyst.openova.io/adopts-helmrelease) and never re-renders it, so
	// this value only has to satisfy schema validation — the realm value is
	// the platform SSO realm the bootstrap Keycloak already serves.
	Config map[string]interface{}
}

// drCapableSpine — the canonical DR-capable bootstrap-spine roster.
//
// BlueprintName is the name the application-controller resolves the live
// Blueprint CR by (fetchBlueprint is a by-NAME Get). On a Sovereign the
// resolvable Blueprint CRs come from the catalog-seed
// (products/catalyst/chart/templates/catalog-seed/blueprints.yaml), which
// names all four with the `bp-` prefix (bp-openbao / bp-keycloak / bp-harbor
// / bp-gitea) — so the roster uses those names. (Note: platform/harbor/
// blueprint.yaml's own metadata.name is the bare `harbor`, but that file is
// only mirrored to Gitea post-cutover; the LIVE catalog CR is bp-harbor.)
// The HelmRelease the CR adopts is `bp-<chart>` for all four (the
// bootstrap-kit convention).
//
// BlueprintVersion is the catalog-seed `spec.version` (the version present in
// the live catalog at bootstrap). fetchBlueprint is by-name not version-
// pinned, so the pin only needs to be a valid exact semver the Application CR
// can carry; tracking the seeded value keeps `kubectl get applications`
// honest about the version actually in the catalog. An exact-semver miss
// surfaces as Pending (ReasonBlueprintMissing), never a hard failure — the
// next enrollment pass picks up the rolled-forward pin (idempotent).
//
// MultiRegionMode mirrors each Blueprint's `spec.topology.defaults.multi-region`:
// openbao's raft stream is async → active-passive; keycloak/harbor/gitea ride
// a synchronous cnpg-pair → active-hot-standby. These are the SAME modes
// ResolveTopology picks from `defaults.multi-region`, so stamping them
// explicitly keeps the placement plan (placement.Resolve) and the topology
// variant (ResolveTopology) in agreement — both then describe a primary +
// standby pair, which buildContinuumPlan needs to mint the Continuum CR.
var drCapableSpine = []spineComponent{
	{Chart: "openbao", HRName: "bp-openbao", BlueprintName: "bp-openbao", BlueprintVersion: "1.2.25", MultiRegionMode: "active-passive"},
	{Chart: "keycloak", HRName: "bp-keycloak", BlueprintName: "bp-keycloak", BlueprintVersion: "1.0.0", MultiRegionMode: "active-hot-standby", Config: map[string]interface{}{"realmName": "sovereign"}},
	{Chart: "harbor", HRName: "bp-harbor", BlueprintName: "bp-harbor", BlueprintVersion: "1.2.26", MultiRegionMode: "active-hot-standby"},
	{Chart: "gitea", HRName: "bp-gitea", BlueprintName: "bp-gitea", BlueprintVersion: "1.2.24", MultiRegionMode: "active-hot-standby"},
}

// spineApplicationNamespace — where the spine Application CRs land. The
// control-plane env namespace (`catalyst`) is the Sovereign-self namespace
// the catalyst-api + its CRDs live in; placing the spine Application CRs
// here keeps them out of any per-Org namespace and groups them with the
// rest of the platform object model. The application-controller is
// cluster-scoped (watches Applications in every namespace), so the
// namespace choice does not gate reconcile.
const spineApplicationNamespace = "catalyst"

// ApplicationGVR is declared in applications.go; reused here.

const (
	// spineApplyFieldManager — distinct field manager so a re-run
	// server-side-apply cleanly owns + merges the same fields without
	// fighting any other writer.
	spineApplyFieldManager = "catalyst-api/spine-application-producer"
	// spineApplyMaxWait — total budget waiting for the Application CRD to
	// register + the spine HRs to appear. The Application CRD ships with
	// catalyst (present immediately), but the spine HRs may still be
	// reconciling at first OutcomeReady; generous since this is off the
	// bootstrap critical path.
	spineApplyMaxWait = 15 * time.Minute
	spineApplyPoll    = 30 * time.Second
)

// shouldStartupSpineReconcile reports whether a rehydrated deployment needs
// the spine Application-CR producer kicked at catalyst-api startup — the
// level-triggered sibling of shouldStartupClusterMeshReconcile (#3241) and
// shouldConvergedLateRescue (#3319).
//
// THE GAP THIS CLOSES (#4212). runPostHandoverSpineApplications is wired ONLY
// into the Phase-1 OutcomeReady terminal block (phase1_watch.go) + the
// converged-late rescue (phase1_converged_late.go) — both ONE-SHOT, fired
// once as a deployment first reaches handover. A Sovereign that handed over
// BEFORE this producer shipped (or before a catalyst-api roll that carries a
// later spine roster) therefore never enrolls its spine into the object
// model: `kubectl get applications.apps.openova.io -n catalyst` is empty,
// the DR-capable spine mints NO Continuum CR, and the seam stays inert with
// no path to heal short of a fresh prov. Live-confirmed on omantel.biz (dep
// 4635277cae4ffed9, 2026-06-26): the chart-templated bootstrap-owned spine
// CRs (singleton, adopt-only) are present but the multi-region `spine-*`
// producer CRs are absent, so `kubectl get continuums.dr.openova.io -A`
// carries no spine row.
//
// The fix mirrors the ClusterMesh-reconcile heal: re-run the idempotent
// producer at startup for any ready, handed-over deployment whose primary
// kubeconfig is still readable on the PVC. Every step of the producer is a
// force:true server-side-apply (CREATE-or-MERGE), so a re-run on an
// already-enrolled Sovereign is a no-op merge — and one that handed over
// pre-producer enrolls zero-touch on the next mothership roll. Status=ready
// AND Result.HandoverFiredAt!=nil is the same "cleanly handed over" idiom the
// handover-export resume gate uses (deployments.go). Mothership-self records
// (no SovereignFQDN, no regions) and unresolvable-kubeconfig records are
// warn-and-skipped so the loop never spins a budget on a lost file.
func (h *Handler) shouldStartupSpineReconcile(dep *Deployment) bool {
	dep.mu.Lock()
	status := dep.Status
	fired := dep.Result != nil && dep.Result.HandoverFiredAt != nil
	regionCount := len(dep.Request.Regions)
	if regionCount == 0 && strings.TrimSpace(dep.Request.Region) != "" {
		regionCount = 1
	}
	dep.mu.Unlock()
	if status != "ready" || !fired {
		return false
	}
	if regionCount == 0 {
		// Mothership-self / record with no declared regions — nothing to
		// enroll a DR-capable spine over.
		return false
	}
	if _, ok := h.resolvePrimaryKubeconfigPath(dep); !ok {
		h.log.Warn("spine-apps: ready record has no resolvable kubeconfig; skipping startup enrollment",
			"id", dep.ID)
		return false
	}
	return true
}

// runPostHandoverSpineApplications enrolls every DR-capable bootstrap-spine
// HelmRelease into the object model by stamping one idempotent Application
// CR each. See file header.
func (h *Handler) runPostHandoverSpineApplications(dep *Deployment) {
	defer func() {
		if r := recover(); r != nil {
			h.log.Error("spine-apps: panic recovered", "id", dep.ID, "panic", r)
		}
	}()

	if h.kubeconfigsDir == "" {
		h.log.Warn("spine-apps: kubeconfigsDir unset; skipping", "id", dep.ID)
		return
	}

	// Build the dynamic client from the posted-back kubeconfig.
	kcPath := filepath.Join(h.kubeconfigsDir, dep.ID+".yaml")
	kcRaw, err := os.ReadFile(kcPath)
	if err != nil {
		h.log.Warn("spine-apps: read kubeconfig failed; skipping", "id", dep.ID, "path", kcPath, "err", err)
		return
	}
	cfg, err := clientcmd.RESTConfigFromKubeConfig(kcRaw)
	if err != nil {
		h.log.Warn("spine-apps: parse kubeconfig failed", "id", dep.ID, "err", err)
		return
	}
	cfg.Timeout = 30 * time.Second
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		h.log.Warn("spine-apps: build dynamic client failed", "id", dep.ID, "err", err)
		return
	}

	// Compose the deployment-derived fields shared by every spine CR.
	envRef := spineEnvironmentRef(dep)
	orgRef := spineOrganizationSlug(dep)
	regions := spineRegions(dep)
	if len(regions) == 0 {
		h.log.Warn("spine-apps: deployment carries no regions; skipping (spec.regions[] is required)", "id", dep.ID)
		return
	}
	ownerLabels := spineOwnerLabels(dep)

	// Ensure the CONSUMER prerequisites the application-controller hard-
	// requires before it will reconcile any spine Application CR. The
	// controller resolves spec.environmentRef → Environment CR →
	// spec.organizationRef → Organization CR; a miss leaves the Application
	// at phase=Pending (ReasonEnvironmentMissing / ReasonOrganizationMissing)
	// forever and NO Continuum CR is minted. Per-Org Environments are minted
	// by the organization-controller off a Ready vCluster (environment_ensure.go),
	// but the SPINE is the Sovereign-self control plane — it has no per-Org
	// vCluster, so nothing ever stamps its Environment/Organization. We stamp
	// both here, idempotently, so the producer-write actually reaches the
	// Continuum reader. The control-plane Environment carries the deployment's
	// REAL region count so the controller resolves multi-region topology
	// (len(env.spec.regions) > 1) — the gate ResolveTopology + buildContinuumPlan
	// both key off.
	// #5476 — wire the object-model ownership chain (Application → Environment
	// → Organization) so cascade + GC are Kubernetes-native, not label-only.
	// Each ensure returns the server-assigned UID from its apply response; we
	// thread it down as the ownerReference of the next resource. A "" UID
	// (ensure failed / apiserver returned no UID) makes the next resource SKIP
	// the ownerReference rather than write a dangling one — no regression on
	// the degraded path, the chain simply wires on the next idempotent pass.
	orgUID, err := h.ensureSpineOrganization(dyn, dep, orgRef)
	if err != nil {
		h.log.Warn("spine-apps: ensure control-plane Organization failed; spine apps will sit Pending until it lands",
			"id", dep.ID, "org", orgRef, "err", err)
	}
	envUID, err := h.ensureSpineEnvironment(dyn, dep, envRef, orgRef, regions, orgUID)
	if err != nil {
		h.log.Warn("spine-apps: ensure control-plane Environment failed; spine apps will sit Pending until it lands",
			"id", dep.ID, "env", envRef, "err", err)
	}

	// Wait for the spine HRs to appear, then enroll each present one. The
	// Application CRD ships with catalyst (present immediately); the spine
	// HRs may still be reconciling at first OutcomeReady.
	deadline := time.Now().Add(spineApplyMaxWait)
	enrolled := 0
	wantPresent := len(drCapableSpine)
	for {
		present := h.presentSpineHRs(dyn, dep)
		enrolled = h.enrollSpineApplications(dyn, dep, present, envRef, orgRef, regions, ownerLabels, envUID)
		// Done once we have enrolled an Application CR for every spine HR
		// that is actually present. If a spine HR never appears (a Sovereign
		// that does not install all four) we still finish on the budget.
		if enrolled >= len(present) && len(present) >= wantPresent {
			break
		}
		if time.Now().After(deadline) {
			h.log.Warn("spine-apps: budget exhausted; partial enrollment",
				"id", dep.ID, "enrolled", enrolled, "present", len(present), "wantPresent", wantPresent)
			break
		}
		time.Sleep(spineApplyPoll)
	}

	if enrolled == 0 {
		dep.recordEvent(provisioner.Event{
			Time:    time.Now().UTC().Format(time.RFC3339),
			Phase:   "post-handover",
			Level:   "warn",
			Message: "Spine object-model enrollment: no DR-capable spine HelmRelease (openbao/keycloak/harbor/gitea) was present after budget; no spine Application CR could be stamped. The object model will pick them up on the next reconcile once they install.",
		})
		return
	}

	h.log.Info("spine-apps: enrolled DR-capable spine into the object model",
		"id", dep.ID, "enrolled", enrolled)
	dep.recordEvent(provisioner.Event{
		Time:  time.Now().UTC().Format(time.RFC3339),
		Phase: "post-handover",
		Level: "info",
		Message: fmt.Sprintf(
			"Spine object-model enrollment: stamped %d idempotent Application CR(s) over the DR-capable bootstrap spine (openbao/keycloak/harbor/gitea). Each enters the application-controller reconcile fan-out, which mints its Continuum DR contract — without re-rendering or rolling the healthy spine pod (adopt, not roll; #3829/#4212).",
			enrolled),
	})
}

// presentSpineHRs returns the subset of the DR-capable spine roster whose
// HelmRelease actually exists on the Sovereign (flux-system). We do NOT
// gate on Ready=True — an HR that is installed but still reconciling should
// still be enrolled so its Continuum contract is minted as soon as it
// converges (the application-controller drives reconcile-on-change). A
// missing HR is simply skipped (a Sovereign that does not install all four
// spine components produces fewer CRs — honest, never fabricated).
func (h *Handler) presentSpineHRs(dyn dynamic.Interface, dep *Deployment) []spineComponent {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	list, err := dyn.Resource(helmwatch.HelmReleaseGVR).Namespace(helmwatch.FluxNamespace).
		List(ctx, metav1.ListOptions{LabelSelector: labels.Everything().String()})
	if err != nil {
		h.log.Info("spine-apps: list HelmReleases failed; will retry", "id", dep.ID, "err", err)
		return nil
	}
	have := map[string]struct{}{}
	for i := range list.Items {
		have[list.Items[i].GetName()] = struct{}{}
	}
	var present []spineComponent
	for _, sc := range drCapableSpine {
		if _, ok := have[sc.HRName]; ok {
			present = append(present, sc)
		}
	}
	return present
}

// enrollSpineApplications server-side-applies one Application CR per present
// spine component, returning how many landed. On the Application-CRD-not-
// registered error it returns early so the caller retries; per-object
// errors are logged but do not abort the batch.
func (h *Handler) enrollSpineApplications(
	dyn dynamic.Interface,
	dep *Deployment,
	present []spineComponent,
	envRef string,
	orgRef string,
	regions []string,
	ownerLabels map[string]string,
	envUID string,
) int {
	enrolled := 0
	for _, sc := range present {
		obj := renderSpineApplicationCR(sc, envRef, orgRef, regions, ownerLabels, envUID)
		_, err := applySpineApplicationCR(dyn, obj)
		if err != nil {
			if isNoMatchOrCRDMissing(err) {
				h.log.Info("spine-apps: Application CRD not registered yet; will retry",
					"id", dep.ID, "enrolled", enrolled)
				return enrolled
			}
			h.log.Warn("spine-apps: apply Application CR failed (continuing)",
				"id", dep.ID, "chart", sc.Chart, "err", err)
			continue
		}
		enrolled++
	}
	return enrolled
}

// applySpineApplicationCR server-side-applies ONE spine Application CR. It is
// a package-level var (test-seam convention, mirroring censusHelmReleases /
// timeNewTicker) so unit tests can substitute a Create/Update-based applier
// — the dynamic-fake client cannot decode an ApplyPatchType body (it tries to
// map the unstructured JSON onto a typed struct). In production this is a
// force:true server-side-apply, which CREATES-or-MERGES idempotently against
// a real apiserver (the adopt-not-roll contract: re-running merges the same
// fields rather than re-rendering).
var applySpineApplicationCR = func(dyn dynamic.Interface, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	data, err := obj.MarshalJSON()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return dyn.Resource(ApplicationGVR()).Namespace(spineApplicationNamespace).
		Patch(ctx, obj.GetName(), types.ApplyPatchType, data, metav1.PatchOptions{
			FieldManager: spineApplyFieldManager,
			Force:        boolPtr(true),
		})
}

// renderSpineApplicationCR composes the idempotent Application CR for one
// spine component. Pure function — no client calls. The shape mirrors the
// REST install path (newApplicationUnstructured) + parseSpec's required set
// (environmentRef, blueprintRef.{name,version}, regions[], placement).
//
// spec.placement is stamped EXPLICITLY (singleton on a single-region
// Sovereign, the Blueprint's topology.defaults.multi-region —
// active-passive / active-hot-standby — on a multi-region one). It is NOT
// omitted: the spine Blueprints declare no `placementSchema.defaultOnMultiRegion`,
// so an omitted placement makes the application-controller's EffectiveDefault
// derive `singleton` regardless of region count — and a singleton plan has
// no standby region, so buildContinuumPlan returns (zero,false) and NO
// Continuum CR is ever minted. Worse, a singleton placement with >1 region
// fails placement.Resolve outright (Application → Failed). Stamping the
// explicit multi-region mode makes placement.Resolve emit a primary+standby
// plan that agrees with the topology variant ResolveTopology picks from the
// same `defaults.multi-region` — the two halves the Continuum producer reads
// — so the producer-write actually reaches the Continuum reader (Seam 3).
//
// spec.organizationRef pins the control-plane Organization the spine
// Environment references, so the application-controller's
// fetchOrganization existence check passes (it would otherwise leave the
// Application at Pending/ReasonOrganizationMissing).
//
// The CR name is `spine-<chart>` (e.g. spine-openbao) so an operator's
// `kubectl get applications.apps.openova.io -A` reads as a spine roster
// distinct from Org / signup apps. The
// `catalyst.openova.io/adopts-helmrelease` label names the EXISTING spine
// HR the CR adopts (Invariant #3 — adopt, never roll).
func renderSpineApplicationCR(sc spineComponent, envRef, orgRef string, regions []string, ownerLabels map[string]string, envUID string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(ApplicationGVR().Group + "/" + ApplicationGVR().Version)
	obj.SetKind("Application")
	obj.SetName(spineApplicationName(sc.Chart))
	obj.SetNamespace(spineApplicationNamespace)

	// #5476 — own the spine Application by its control-plane Environment so
	// deletion cascade + GC are Kubernetes-native rather than resting entirely
	// on label hygiene (which is how orphans survive a wipe). The Environment
	// is cluster-scoped, so a namespaced Application may reference it as an
	// owner. Only wire the ref when the Environment's UID is known — an empty
	// UID would be a dangling owner reference, worse than none.
	if envUID != "" {
		obj.SetOwnerReferences([]metav1.OwnerReference{{
			APIVersion: EnvironmentGVR().Group + "/" + EnvironmentGVR().Version,
			Kind:       "Environment",
			Name:       envRef,
			UID:        types.UID(envUID),
		}})
	}

	lbls := map[string]string{}
	for k, v := range ownerLabels {
		lbls[k] = v
	}
	lbls["catalyst.openova.io/managed-by"] = "catalyst-api"
	lbls["catalyst.openova.io/organization"] = orgRef
	lbls["catalyst.openova.io/blueprint"] = sc.BlueprintName
	lbls["catalyst.openova.io/blueprint-version"] = sc.BlueprintVersion
	// Spine marker + adopt back-pointer: the object model can tell a spine
	// CR apart from an Org install, and the adopt label names the existing
	// HelmRelease this CR ENROLLS (never re-renders).
	lbls["catalyst.openova.io/spine"] = "true"
	lbls["catalyst.openova.io/adopts-helmrelease"] = sc.HRName
	obj.SetLabels(lbls)

	rgs := make([]interface{}, 0, len(regions))
	for _, r := range regions {
		rgs = append(rgs, r)
	}

	spec := map[string]interface{}{
		"environmentRef":  envRef,
		"organizationRef": orgRef,
		"placement":       spinePlacementMode(sc, len(regions)),
		"blueprintRef": map[string]interface{}{
			"name":    sc.BlueprintName,
			"version": sc.BlueprintVersion,
		},
		"regions": rgs,
		// #4416 — ADOPT, never roll (Invariant #3). The spine HelmRelease is
		// ALREADY installed + healthy by the bootstrap-kit (flux-system/bp-<chart>);
		// stamping spec.bootstrap=true + spec.helmRelease routes the
		// application-controller down its bootstrap-owned ADOPTION path
		// (reconcileBootstrapOwned), which observes that existing HR's Ready
		// condition for status and NEVER renders a fan-out HelmRelease.
		//
		// THE BUG THIS FIXES. The prior shape stamped only the
		// `catalyst.openova.io/adopts-helmrelease` LABEL and left
		// spec.bootstrap=false — but the controller's adoption guard
		// (isBootstrapOwned) keys ONLY off spec.bootstrap, never the label. So
		// the controller ran the full fan-out render and stamped DUPLICATE
		// HelmReleases (spine-<chart>, spine-<chart>-mgmt-a/-b) carrying the
		// producer's STALE catalog-seed chart pin (e.g. bp-harbor@1.2.26 vs the
		// live healthy bootstrap bp-harbor@1.2.40). Those duplicate installs
		// failed `context deadline exceeded`, wedging the spine app at
		// Degraded/Provisioning and violating Invariant #3. The adoption path
		// makes the spine app a pure status-mirror of the healthy bootstrap HR.
		"bootstrap": true,
		"helmRelease": map[string]interface{}{
			"name":      sc.HRName,
			"namespace": spineHRNamespace,
		},
	}
	// #4402/#4416 — stamp the minimal spec.parameters the Blueprint configSchema
	// requires (e.g. bp-keycloak's required `realmName`) so the Application
	// passes validation and reaches the Continuum producer. The controller
	// validates spec.PARAMETERS (parseSpec reads `spec.parameters`; the CRD
	// declares `spec.parameters`; the REST install path writes `spec.parameters`)
	// — the prior `spec.config` key was never read, so spine-keycloak Failed
	// validation `missing properties: 'realmName'` despite carrying the value
	// under the wrong key. Components with no required config carry a nil Config
	// and stamp nothing.
	if len(sc.Config) > 0 {
		params := make(map[string]interface{}, len(sc.Config))
		for k, v := range sc.Config {
			params[k] = v
		}
		spec["parameters"] = params
	}
	obj.Object["spec"] = spec
	return obj
}

// spineHRNamespace — the namespace the bootstrap-spine HelmRelease CRs live in.
// The bootstrap-kit installs every spine HR (bp-gitea/-harbor/-keycloak/-openbao)
// as a Flux HelmRelease object in `flux-system` (the canonical bootstrap-kit
// convention — the HR OBJECT is in flux-system even though it targets a
// per-component release namespace). The adoption path (reconcileBootstrapOwned)
// GETs the HelmRelease at spec.helmRelease.{name,namespace}; this is that
// namespace.
const spineHRNamespace = "flux-system"

// spinePlacementMode picks the explicit placement mode to stamp on a spine
// Application CR. On a single-region Sovereign the spine is a singleton; on
// a multi-region Sovereign it rides the Blueprint's declared
// topology.defaults.multi-region (active-passive for the async raft spine,
// active-hot-standby for the synchronous cnpg-pair spine). The number of
// regions is authoritative — a spine component whose roster row carries no
// MultiRegionMode (defensive) falls back to singleton so the producer never
// stamps an unrenderable mode.
func spinePlacementMode(sc spineComponent, regionCount int) string {
	if regionCount > 1 && sc.MultiRegionMode != "" {
		return sc.MultiRegionMode
	}
	return "singleton"
}

// spineApplicationName composes the spine Application CR name (`spine-<chart>`).
// Exported-style helper so tests reference the same convention.
func spineApplicationName(chart string) string {
	return "spine-" + chart
}

// spineEnvironmentRef returns the RFC-1123-label control-plane Environment
// ref for the spine Application CRs. The application-controller's parseSpec
// requires a non-empty environmentRef; the spine is the Sovereign-self
// control plane, so we use a stable `<sovereign>-cp`-shaped label derived
// from the deployment, slugged the same way the REST install path slugs its
// env ref (sanitizeEnvironmentRef) so the apiserver's pattern-validated
// spec.environmentRef accepts it.
func spineEnvironmentRef(dep *Deployment) string {
	dep.mu.Lock()
	fqdn := strings.TrimSpace(dep.Request.SovereignFQDN)
	dep.mu.Unlock()
	base := fqdn
	if base == "" {
		base = dep.ID
	}
	return sanitizeEnvironmentRef(base + "-cp")
}

// spineRegions returns the deployment's declared cloud regions, in order,
// for the spine Application CR spec.regions[]. Falls back to the single
// Request.Region when Regions[] is empty. Mirrors the region derivation in
// the adoption applier + clusterRegionMap.
func spineRegions(dep *Deployment) []string {
	dep.mu.Lock()
	regs := append([]provisioner.RegionSpec(nil), dep.Request.Regions...)
	single := strings.TrimSpace(dep.Request.Region)
	dep.mu.Unlock()

	out := make([]string, 0, len(regs))
	for _, rs := range regs {
		if r := strings.TrimSpace(rs.CloudRegion); r != "" {
			out = append(out, r)
		}
	}
	if len(out) == 0 && single != "" {
		out = append(out, single)
	}
	return out
}

// spineOwnerLabels mirrors the org/env identity onto the spine Application
// CRs so they participate in the same observability rollup the Org apps use.
// The spine is Sovereign-self-owned (no per-Org tenant), so the org label is
// the Sovereign FQDN.
func spineOwnerLabels(dep *Deployment) map[string]string {
	dep.mu.Lock()
	fqdn := strings.TrimSpace(dep.Request.SovereignFQDN)
	dep.mu.Unlock()
	out := map[string]string{}
	if fqdn != "" {
		out["catalyst.openova.io/organization"] = fqdn
	}
	out["catalyst.openova.io/environment"] = spineEnvironmentRef(dep)
	return out
}

// spineOrganizationSlug returns the control-plane Organization slug the
// spine Environment + Application CRs reference. It is the slugged Sovereign
// FQDN (e.g. "t99-omani-works" for "t99.omani.works") — an RFC-1123 label
// the Organization CRD's slug pattern accepts, derived the same way the REST
// install path slugs its namespace. This is the Sovereign-self "platform"
// Organization, distinct from any customer tenant Org.
func spineOrganizationSlug(dep *Deployment) string {
	dep.mu.Lock()
	fqdn := strings.TrimSpace(dep.Request.SovereignFQDN)
	dep.mu.Unlock()
	base := fqdn
	if base == "" {
		base = dep.ID
	}
	return orgNamespace(base)
}

// ensureSpineOrganization server-side-applies the Sovereign-self
// (platform-scope) Organization CR the spine Environment references. It is
// existence-only from the application-controller's perspective (fetchOrganization
// is a presence check), so we stamp the minimal valid spec the Organization
// CRD requires (slug, displayName, kind, tier, billingMode, sovereignRef +
// one owner). The `openova.io/scope: platform` label marks it as the
// control-plane self-org, distinct from a customer tenant Org. Idempotent:
// a re-run server-side-applies the same fields. NotFound of the CRD (e.g.
// the Organization CRD not yet registered) is surfaced so the caller logs +
// the next enrollment pass retries.
func (h *Handler) ensureSpineOrganization(dyn dynamic.Interface, dep *Deployment, orgRef string) (string, error) {
	dep.mu.Lock()
	fqdn := strings.TrimSpace(dep.Request.SovereignFQDN)
	email := strings.TrimSpace(dep.Request.OrgEmail)
	name := strings.TrimSpace(dep.Request.OrgName)
	dep.mu.Unlock()
	if fqdn == "" {
		fqdn = orgRef
	}
	display := name
	if display == "" {
		display = fqdn + " (platform spine)"
	}
	owners := []interface{}{}
	if email != "" {
		owners = append(owners, map[string]interface{}{"email": email, "role": "owner"})
	}
	// CRD-valid enums (#4402): the Organization CRD admits tier∈{org,corporate},
	// billingMode∈{real,chargeback,showback}, kind∈{customer,internal}. An earlier
	// draft stamped tier:"platform"/billingMode:"free" — both rejected by the live
	// CRD, so the control-plane Organization never landed → the spine Environment
	// could not resolve → spine apps wedged and minted no Continuum. The spine is
	// the Sovereign-self internal control plane, billed to no one: tier:"org",
	// billingMode:"showback" (cost-attribution-only, never invoiced), kind:"internal".
	// The `openova.io/scope: platform` label below is what marks it the control-plane
	// self-org, distinct from a customer tenant Org.
	spec := map[string]interface{}{
		"slug":         orgRef,
		"displayName":  display,
		"kind":         "internal",
		"tier":         "org",
		"billingMode":  "showback",
		"sovereignRef": fqdn,
	}
	if len(owners) > 0 {
		spec["owners"] = owners
	}
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion("orgs.openova.io/v1")
	obj.SetKind("Organization")
	obj.SetName(orgRef)
	obj.SetLabels(map[string]string{
		"catalyst.openova.io/managed-by": "catalyst-api",
		"openova.io/scope":               "platform",
		"openova.io/sovereign":           fqdn,
	})
	obj.Object["spec"] = spec
	applied, err := applySpineClusterResource(dyn, organizationGVR(), "", obj)
	if err != nil {
		return "", err
	}
	return spineAppliedUID(applied), nil
}

// ensureSpineEnvironment server-side-applies the Sovereign-self control-plane
// Environment CR (`<sovereign>-cp`) the spine Application CRs reference. The
// application-controller reads ONLY spec.organizationRef, spec.envType,
// spec.placement, and len(spec.regions) off the Environment (parseEnvSpec) —
// the per-region provider/region/buildingBlock VALUES are only used by the
// environment-controller to derive Gitea host-cluster paths, and the spine
// install pivots through the Blueprint topology, not those strings. So the
// load-bearing field for Seam 3 is len(spec.regions): it MUST equal the
// deployment's real region count so ResolveTopology resolves multi-region
// (defaults.multi-region) on a ≥2-region Sovereign. We emit one CRD-valid
// region triple per real region (provider/region/buildingBlock satisfying
// the CRD enums + the `^[a-z]{3}[a-z0-9]?$` region pattern). Idempotent.
func (h *Handler) ensureSpineEnvironment(dyn dynamic.Interface, dep *Deployment, envRef, orgRef string, regions []string, orgUID string) (string, error) {
	regionObjs := make([]interface{}, 0, len(regions))
	for _, sc := range spineRegionSpecs(dep) {
		regionObjs = append(regionObjs, spineEnvRegionObject(sc))
	}
	// Defensive: if the deployment carried region strings but no RegionSpec
	// rows (single Request.Region path), synthesise CRD-valid triples from
	// the count so len(spec.regions) still drives the topology resolution.
	for len(regionObjs) < len(regions) {
		regionObjs = append(regionObjs, spineEnvRegionObject(provisioner.RegionSpec{}))
	}
	// #5476 — make the region codes pairwise distinct. spineEnvRegionCode
	// collapses me-east-215-a and me-east-215-b to the SAME 3-letter base
	// ("meea"), so a 2-region Sovereign wrote two IDENTICAL region triples and
	// the -cp Environment could not tell its regions apart (issue Part 2). De-
	// dupe in place, staying CRD-pattern-valid (`^[a-z]{3}[a-z0-9]?$`).
	dedupeSpineEnvRegionCodes(regionObjs)
	placement := "single-region"
	if len(regionObjs) > 1 {
		placement = "multi-region"
	}
	spec := map[string]interface{}{
		"organizationRef": orgRef,
		"envType":         "prod",
		"placement":       placement,
		"regions":         regionObjs,
	}
	dep.mu.Lock()
	fqdn := strings.TrimSpace(dep.Request.SovereignFQDN)
	dep.mu.Unlock()
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(EnvironmentGVR().Group + "/" + EnvironmentGVR().Version)
	obj.SetKind("Environment")
	obj.SetName(envRef)
	obj.SetLabels(map[string]string{
		"catalyst.openova.io/managed-by": "catalyst-api",
		"openova.io/scope":               "platform",
		"openova.io/organization":        orgRef,
		"openova.io/sovereign":           fqdn,
	})
	// #5476 — own the -cp Environment by its control-plane Organization, matching
	// the sibling per-Org `-prod` Environment (which the organization-controller
	// already stamps with this exact owner ref in environment_ensure.go). Both
	// CRs are cluster-scoped, so a cluster-scoped controller owner ref is GC-safe
	// and ties the Environment's lifecycle to the Org. Skip when the Org UID is
	// unknown rather than write a dangling ref.
	if orgUID != "" {
		obj.SetOwnerReferences([]metav1.OwnerReference{{
			APIVersion: organizationGVR().Group + "/" + organizationGVR().Version,
			Kind:       "Organization",
			Name:       orgRef,
			UID:        types.UID(orgUID),
			Controller: boolPtr(true),
		}})
	}
	obj.Object["spec"] = spec
	applied, err := applySpineClusterResource(dyn, EnvironmentGVR(), "", obj)
	if err != nil {
		return "", err
	}
	return spineAppliedUID(applied), nil
}

// spineRegionSpecs returns the deployment's RegionSpec rows (provider +
// cloud-region), falling back to a single synthesised row from the umbrella
// Request.Region when Regions[] is empty. Mirrors spineRegions but keeps the
// provider so the Environment region triple carries a real provider enum.
func spineRegionSpecs(dep *Deployment) []provisioner.RegionSpec {
	dep.mu.Lock()
	regs := append([]provisioner.RegionSpec(nil), dep.Request.Regions...)
	single := strings.TrimSpace(dep.Request.Region)
	dep.mu.Unlock()
	out := make([]provisioner.RegionSpec, 0, len(regs))
	for _, rs := range regs {
		if strings.TrimSpace(rs.CloudRegion) != "" {
			out = append(out, rs)
		}
	}
	if len(out) == 0 && single != "" {
		out = append(out, provisioner.RegionSpec{CloudRegion: single})
	}
	return out
}

// spineEnvRegionObject maps a RegionSpec onto a CRD-valid Environment
// region triple. The Environment CRD constrains region to `^[a-z]{3}[a-z0-9]?$`
// (3-4 chars) and provider/buildingBlock to fixed enums; a cloud-region like
// "me-east-215-a" or "fsn1" does not satisfy the pattern, so we derive a
// short canonical code. The VALUES are cosmetic for the spine path (the
// application-controller never reads them; the install pivots through the
// Blueprint topology) — only len(spec.regions) is load-bearing — so a safe
// canonical triple per row is sufficient and always schema-valid.
func spineEnvRegionObject(sc provisioner.RegionSpec) map[string]interface{} {
	return map[string]interface{}{
		"provider":      spineEnvProvider(sc.Provider),
		"region":        spineEnvRegionCode(sc.CloudRegion),
		"buildingBlock": "rtz",
	}
}

// spineEnvProviderAllowed mirrors the Environment CRD provider enum.
var spineEnvProviderAllowed = map[string]struct{}{
	"hetzner": {}, "huawei": {}, "oci": {}, "aws": {}, "gcp": {}, "azure": {}, "contabo": {},
}

// spineEnvProvider lower-cases/trims the provider and falls back to "huawei"
// (the canonical default, same as environment_ensure.go) when it is empty or
// not in the CRD enum.
func spineEnvProvider(provider string) string {
	p := strings.ToLower(strings.TrimSpace(provider))
	if _, ok := spineEnvProviderAllowed[p]; ok {
		return p
	}
	return "huawei"
}

// spineEnvRegionCode derives a CRD-pattern-valid (`^[a-z]{3}[a-z0-9]?$`)
// region code from a cloud-region string. It takes the first run of
// lowercase letters/digits, keeps at most the first 3 letters + an optional
// trailing alphanumeric, and falls back to "reg" when the input yields no
// pattern-valid prefix. The exact code is cosmetic for the spine path
// (only len(spec.regions) is read by the application-controller), so a
// stable schema-valid value is all that's required.
func spineEnvRegionCode(cloudRegion string) string {
	s := strings.ToLower(strings.TrimSpace(cloudRegion))
	letters := make([]rune, 0, 3)
	var tail rune
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			if len(letters) < 3 {
				letters = append(letters, r)
			} else if tail == 0 {
				tail = r
			}
		case r >= '0' && r <= '9':
			if len(letters) == 3 && tail == 0 {
				tail = r
			}
		}
		if len(letters) == 3 && tail != 0 {
			break
		}
	}
	if len(letters) < 3 {
		return "reg"
	}
	code := string(letters)
	if tail != 0 {
		code += string(tail)
	}
	return code
}

// applySpineClusterResource server-side-applies a cluster-scoped (ns="")
// or namespaced spine prerequisite (Organization / Environment). Like
// applySpineApplicationCR it is a package-level var so unit tests can swap a
// Create/Update-based applier (the dynamic-fake client cannot decode an
// ApplyPatchType body). force:true CREATES-or-MERGES idempotently against a
// real apiserver.
var applySpineClusterResource = func(dyn dynamic.Interface, gvr schema.GroupVersionResource, namespace string, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	data, err := obj.MarshalJSON()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ri := dyn.Resource(gvr)
	var applied dynamic.ResourceInterface = ri
	if namespace != "" {
		applied = ri.Namespace(namespace)
	}
	return applied.Patch(ctx, obj.GetName(), types.ApplyPatchType, data, metav1.PatchOptions{
		FieldManager: spineApplyFieldManager,
		Force:        boolPtr(true),
	})
}

// spineAppliedUID extracts the server-assigned UID off an apply response so the
// producer can wire the ownership chain (Organization → Environment →
// Application, #5476). Returns "" for a nil response or an object that carries
// no UID — the caller then SKIPS the ownerReference rather than emitting one
// with an empty UID (a dangling owner ref GC cannot resolve).
func spineAppliedUID(obj *unstructured.Unstructured) string {
	if obj == nil {
		return ""
	}
	return string(obj.GetUID())
}

// dedupeSpineEnvRegionCodes makes the -cp Environment's region codes pairwise
// distinct in place (#5476, issue Part 2). spineEnvRegionCode derives a code
// from the first three letters + the first trailing alnum, so it collapses
// me-east-215-a and me-east-215-b to the SAME "meea" — the -a/-b discriminator
// is never reached — and a 2-region Sovereign ends up with two identical region
// triples that the Environment cannot tell apart. On a collision we keep the
// 3-letter stem and swap the 4th character for the first unused value from
// [a-z0-9], which stays CRD-pattern-valid (`^[a-z]{3}[a-z0-9]?$`). The region
// VALUES are cosmetic for the spine path (the application-controller reads only
// len(spec.regions)); distinctness is what the object model needs.
func dedupeSpineEnvRegionCodes(regionObjs []interface{}) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	seen := map[string]bool{}
	for _, ro := range regionObjs {
		m, ok := ro.(map[string]interface{})
		if !ok {
			continue
		}
		code, _ := m["region"].(string)
		if code != "" && !seen[code] {
			seen[code] = true
			continue
		}
		stem := code
		if len(stem) > 3 {
			stem = stem[:3]
		}
		if stem == "" {
			stem = "reg"
		}
		for _, c := range alphabet {
			cand := stem + string(c)
			if !seen[cand] {
				code = cand
				break
			}
		}
		seen[code] = true
		m["region"] = code
	}
}
