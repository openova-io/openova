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
//   1. composes one idempotent Application CR (spec.blueprintRef → the spine
//      Blueprint, spec.environmentRef → the Sovereign control-plane env,
//      spec.regions[] → the deployment's declared regions),
//   2. SERVER-SIDE APPLIES it onto the Sovereign cluster via the kubeconfig
//      the cloud-init posted back.
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
}

// drCapableSpine — the canonical DR-capable bootstrap-spine roster. The
// Blueprint name/version mirror the pins in platform/<chart>/blueprint.yaml
// (`metadata.name` + `spec.version`). harbor's Blueprint is named bare
// `harbor` (legacy — its blueprint.yaml metadata.name is `harbor`, not
// `bp-harbor`); the other three carry the `bp-` prefix. The HelmRelease is
// `bp-<chart>` for all four (the bootstrap-kit names every spine HR with the
// `bp-` prefix regardless of the Blueprint's own metadata.name).
//
// Versions are intentionally the floor the bootstrap-kit installs; the
// application-controller resolves the Blueprint by name+version from the
// per-Sovereign catalog (Gitea-mirrored), and an exact-semver miss surfaces
// as Pending (ReasonBlueprintMissing) rather than a hard failure — so a
// catalog that has rolled the spine pin forward simply needs the Application
// CR's version bumped on the next enrollment pass (idempotent).
var drCapableSpine = []spineComponent{
	{Chart: "openbao", HRName: "bp-openbao", BlueprintName: "bp-openbao", BlueprintVersion: "1.2.51"},
	{Chart: "keycloak", HRName: "bp-keycloak", BlueprintName: "bp-keycloak", BlueprintVersion: "1.5.2"},
	{Chart: "harbor", HRName: "bp-harbor", BlueprintName: "harbor", BlueprintVersion: "1.2.36"},
	{Chart: "gitea", HRName: "bp-gitea", BlueprintName: "bp-gitea", BlueprintVersion: "1.2.40"},
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
	regions := spineRegions(dep)
	if len(regions) == 0 {
		h.log.Warn("spine-apps: deployment carries no regions; skipping (spec.regions[] is required)", "id", dep.ID)
		return
	}
	ownerLabels := spineOwnerLabels(dep)

	// Wait for the spine HRs to appear, then enroll each present one. The
	// Application CRD ships with catalyst (present immediately); the spine
	// HRs may still be reconciling at first OutcomeReady.
	deadline := time.Now().Add(spineApplyMaxWait)
	enrolled := 0
	wantPresent := len(drCapableSpine)
	for {
		present := h.presentSpineHRs(dyn, dep)
		enrolled = h.enrollSpineApplications(dyn, dep, present, envRef, regions, ownerLabels)
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
	regions []string,
	ownerLabels map[string]string,
) int {
	enrolled := 0
	for _, sc := range present {
		obj := renderSpineApplicationCR(sc, envRef, regions, ownerLabels)
		err := applySpineApplicationCR(dyn, obj)
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
var applySpineApplicationCR = func(dyn dynamic.Interface, obj *unstructured.Unstructured) error {
	data, err := obj.MarshalJSON()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = dyn.Resource(ApplicationGVR()).Namespace(spineApplicationNamespace).
		Patch(ctx, obj.GetName(), types.ApplyPatchType, data, metav1.PatchOptions{
			FieldManager: spineApplyFieldManager,
			Force:        boolPtr(true),
		})
	return err
}

// renderSpineApplicationCR composes the idempotent Application CR for one
// spine component. Pure function — no client calls. The shape mirrors the
// REST install path (newApplicationUnstructured) + parseSpec's required set
// (environmentRef, blueprintRef.{name,version}, regions[]). spec.placement
// is intentionally OMITTED so the application-controller derives the
// effective default from the Blueprint's defaultOnMultiRegion + the
// Sovereign-wide BCP topology (SOVEREIGN_BCP_TOPOLOGY) — exactly the seam
// that lands a multi-region Sovereign's spine on active-passive zero-touch,
// which buildContinuumPlan needs to mint the Continuum contract.
//
// The CR name is `spine-<chart>` (e.g. spine-openbao) so an operator's
// `kubectl get applications.apps.openova.io -A` reads as a spine roster
// distinct from Org / signup apps. The
// `catalyst.openova.io/adopts-helmrelease` label names the EXISTING spine
// HR the CR adopts (Invariant #3 — adopt, never roll).
func renderSpineApplicationCR(sc spineComponent, envRef string, regions []string, ownerLabels map[string]string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(ApplicationGVR().Group + "/" + ApplicationGVR().Version)
	obj.SetKind("Application")
	obj.SetName(spineApplicationName(sc.Chart))
	obj.SetNamespace(spineApplicationNamespace)

	lbls := map[string]string{}
	for k, v := range ownerLabels {
		lbls[k] = v
	}
	lbls["catalyst.openova.io/managed-by"] = "catalyst-api"
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
		"environmentRef": envRef,
		"blueprintRef": map[string]interface{}{
			"name":    sc.BlueprintName,
			"version": sc.BlueprintVersion,
		},
		"regions": rgs,
	}
	obj.Object["spec"] = spec
	return obj
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
