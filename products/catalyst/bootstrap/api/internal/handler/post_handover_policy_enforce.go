// post_handover_policy_enforce.go — Wave 5.90 phase 2b (#2441, #5591).
//
// After Phase 1 reaches OutcomeReady + fireHandover + clustermesh
// auto-establish, this hook PATCHes the Sovereign-side bp-kyverno-
// policies HelmRelease values to flip `compliancePolicies.bootstrapMode`
// from true (fresh-prov default) → false — in EVERY region of the
// deployment. Flux reconciles; Helm upgrades the 6 canonically-Enforce
// ClusterPolicies (probesPresent / resourceRequests / ciliumL7Mtls /
// fluxManaged / harborProxyPull / imageTagPinned) from Audit (their
// bootstrap render) to Enforce (their target-state action).
//
// Why this hook: Wave 5.90 phase 2a (#2440) introduced the bootstrap-
// mode override on the chart side — a fresh Sovereign installs every
// policy at Audit regardless of per-policy canonical action, so
// admission cascade never blocks Phase 1 Pod creation (#2436 multi-
// layer RCA). But without an automatic post-handover flip, every
// Sovereign stays at Audit forever, defeating the governance value of
// the 6 Enforce policies. This hook IS that automatic flip.
//
// Why every region (#5591): each region runs its own Flux with its own
// bp-kyverno-policies HelmRelease, so a flip that PATCHes only the
// primary leaves every secondary region at Audit forever — live-proven
// on hw291 + hw292 (region-a Enforce / region-b Audit on a fresh
// 2-region prov). The secondary kubeconfigs come from the SAME
// `<depID>-<regionKey>.yaml` store (#3991/#4000) that ClusterMesh
// establish, the #5244 gateway-ELB reconciler and the #5359 cutover
// secondary bridge already trust, via the #5359 union helper — this
// loop can never disagree with their view of the topology. Per-region
// best-effort: a failed secondary logs loudly but does not abort the
// other regions (the flip is idempotent and re-triggerable per #2441).
//
// Run on a background goroutine from runPhase1Watch's OutcomeReady
// terminal block (alongside fireHandover + runAutoEstablishClusterMesh
// + runSecondaryBridgeBackfill). Failures are logged + emit an SSE
// "warn" event; do not fail the handover. Operators can re-trigger
// via the manual patch documented in #2441.
package handler

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// bpKyvernoPoliciesHRName — the HelmRelease that owns the
// ClusterPolicies. Matches clusters/_template/bootstrap-kit/27a-
// kyverno-policies.yaml (or whichever slot bp-kyverno-policies lives
// in). The flux-system namespace is canonical for every bootstrap-kit
// HelmRelease.
const (
	bpKyvernoPoliciesHRName      = "bp-kyverno-policies"
	bpKyvernoPoliciesHRNamespace = "flux-system"
)

// policyFlipPrimaryRegionLabel labels the primary region's flip target in
// logs + the SSE event. Secondary targets are labelled by their regionKey.
const policyFlipPrimaryRegionLabel = "primary"

// dynamicClientForPolicyFlip builds the per-region dynamic client from raw
// kubeconfig bytes. Package-level var (test-seam convention, mirroring
// applySpineApplicationCR) so the #5591 unit test can substitute fakes and
// assert the per-region PATCHes.
var dynamicClientForPolicyFlip = func(kcRaw []byte) (dynamic.Interface, error) {
	cfg, err := clientcmd.RESTConfigFromKubeConfig(kcRaw)
	if err != nil {
		return nil, err
	}
	cfg.Timeout = 30 * time.Second
	return dynamic.NewForConfig(cfg)
}

// runPostHandoverPolicyEnforceFlip drives the Wave 5.90 phase 2b
// PATCH against the bp-kyverno-policies HelmRelease of EVERY region of
// the deployment (#5591). Idempotent — re-running on a region whose
// bootstrapMode is already false is a no-op merge.
//
// Called from runPhase1Watch's OutcomeReady terminal block (see
// phase1_watch.go after runSecondaryBridgeBackfill). Background
// goroutine so the Phase-1 terminate path's own SSE event ordering
// is not blocked by the per-region REST timeout (30s) of the
// HelmRelease PATCHes.
func (h *Handler) runPostHandoverPolicyEnforceFlip(dep *Deployment) {
	defer func() {
		if r := recover(); r != nil {
			h.log.Error("phase2b: policy enforce flip panic recovered",
				"id", dep.ID,
				"panic", r,
			)
		}
	}()

	if h.kubeconfigsDir == "" {
		h.log.Warn("phase2b: kubeconfigsDir unset; skipping bootstrapMode flip",
			"id", dep.ID,
		)
		return
	}

	// Every region gets the flip: the primary `<depID>.yaml` plus each
	// secondary region's `<depID>-<regionKey>.yaml` from the #5359 union
	// (in-memory map + on-disk files — immune to in-memory loss across
	// catalyst-api restarts, #4000).
	targets := map[string]string{
		policyFlipPrimaryRegionLabel: filepath.Join(h.kubeconfigsDir, dep.ID+".yaml"),
	}
	secondaries, _ := secondaryKubeconfigsForCutover(dep)
	for key, path := range secondaries {
		targets[key] = path
	}

	labels := make([]string, 0, len(targets))
	for label := range targets {
		labels = append(labels, label)
	}
	sort.Strings(labels)

	flipped := make([]string, 0, len(targets))
	for _, label := range labels {
		if h.flipRegionPolicyBootstrapMode(dep, label, targets[label]) {
			flipped = append(flipped, label)
		}
	}
	if len(flipped) == 0 {
		// Every region already logged its own warn/error above.
		return
	}

	h.log.Info("phase2b: bp-kyverno-policies bootstrapMode flipped false; Flux will reconcile 6 Enforce policies",
		"id", dep.ID,
		"regions", strings.Join(flipped, ","),
		"flipped", len(flipped),
		"of", len(targets),
	)

	// SSE event so the wizard's post-handover banner can render
	// "Compliance policies upgraded to Enforce mode". Mirrors the
	// fireHandover SSE pattern.
	dep.recordEvent(provisioner.Event{
		Time:    time.Now().UTC().Format(time.RFC3339),
		Phase:   "post-handover",
		Level:   "info",
		Message: "Wave 5.90 phase 2b: bp-kyverno-policies bootstrapMode → false on " + strings.Join(flipped, ", ") + "; Flux reconciling 6 ClusterPolicies to Enforce target-state action",
	})
}

// flipRegionPolicyBootstrapMode PATCHes one region's bp-kyverno-policies
// HelmRelease. Returns true on success; every failure path logs with the
// region label and returns false without aborting the sibling regions.
func (h *Handler) flipRegionPolicyBootstrapMode(dep *Deployment, region, kcPath string) bool {
	kcRaw, err := os.ReadFile(kcPath)
	if err != nil {
		h.log.Warn("phase2b: read kubeconfig failed; skipping bootstrapMode flip for this region",
			"id", dep.ID,
			"region", region,
			"path", kcPath,
			"err", err,
		)
		return false
	}

	dyn, err := dynamicClientForPolicyFlip(kcRaw)
	if err != nil {
		h.log.Warn("phase2b: build dynamic client failed",
			"id", dep.ID,
			"region", region,
			"err", err,
		)
		return false
	}

	patch, err := json.Marshal(map[string]any{
		"spec": map[string]any{
			"values": map[string]any{
				"compliancePolicies": map[string]any{
					"bootstrapMode": false,
				},
			},
		},
	})
	if err != nil {
		h.log.Warn("phase2b: marshal patch failed",
			"id", dep.ID,
			"region", region,
			"err", err,
		)
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	hr := dyn.Resource(helmReleaseGVR).Namespace(bpKyvernoPoliciesHRNamespace)
	_, err = hr.Patch(ctx, bpKyvernoPoliciesHRName, types.MergePatchType, patch, metav1.PatchOptions{
		FieldManager: "catalyst-api/phase2b-policy-enforce-flip",
	})
	if err != nil {
		if errors.IsNotFound(err) {
			h.log.Warn("phase2b: bp-kyverno-policies HR not found (region may not have it installed); skipping",
				"id", dep.ID,
				"region", region,
			)
			return false
		}
		h.log.Error("phase2b: HelmRelease PATCH failed",
			"id", dep.ID,
			"region", region,
			"err", err,
		)
		return false
	}

	h.log.Info("phase2b: region bootstrapMode flipped false",
		"id", dep.ID,
		"region", region,
	)
	return true
}
