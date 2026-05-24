// post_handover_policy_enforce.go — Wave 5.90 phase 2b (#2441).
//
// After Phase 1 reaches OutcomeReady + fireHandover + clustermesh
// auto-establish, this hook PATCHes the Sovereign-side bp-kyverno-
// policies HelmRelease values to flip `compliancePolicies.bootstrapMode`
// from true (fresh-prov default) → false. Flux reconciles; Helm
// upgrades the 6 canonically-Enforce ClusterPolicies (probesPresent /
// resourceRequests / ciliumL7Mtls / fluxManaged / harborProxyPull /
// imageTagPinned) from Audit (their bootstrap render) to Enforce
// (their target-state action).
//
// Why this hook: Wave 5.90 phase 2a (#2440) introduced the bootstrap-
// mode override on the chart side — a fresh Sovereign installs every
// policy at Audit regardless of per-policy canonical action, so
// admission cascade never blocks Phase 1 Pod creation (#2436 multi-
// layer RCA). But without an automatic post-handover flip, every
// Sovereign stays at Audit forever, defeating the governance value of
// the 6 Enforce policies. This hook IS that automatic flip.
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

// runPostHandoverPolicyEnforceFlip drives the Wave 5.90 phase 2b
// PATCH against the Sovereign-side bp-kyverno-policies HelmRelease.
// Idempotent — re-running on a Sovereign whose bootstrapMode is
// already false is a no-op merge.
//
// Called from runPhase1Watch's OutcomeReady terminal block (see
// phase1_watch.go after runSecondaryBridgeBackfill). Background
// goroutine so the Phase-1 terminate path's own SSE event ordering
// is not blocked by the per-request REST timeout (30s) of the
// HelmRelease PATCH.
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

	kcPath := filepath.Join(h.kubeconfigsDir, dep.ID+".yaml")
	kcRaw, err := os.ReadFile(kcPath)
	if err != nil {
		h.log.Warn("phase2b: read kubeconfig failed; skipping bootstrapMode flip",
			"id", dep.ID,
			"path", kcPath,
			"err", err,
		)
		return
	}

	cfg, err := clientcmd.RESTConfigFromKubeConfig(kcRaw)
	if err != nil {
		h.log.Warn("phase2b: parse kubeconfig failed",
			"id", dep.ID,
			"err", err,
		)
		return
	}
	cfg.Timeout = 30 * time.Second

	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		h.log.Warn("phase2b: build dynamic client failed",
			"id", dep.ID,
			"err", err,
		)
		return
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
			"err", err,
		)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	hr := dyn.Resource(helmReleaseGVR).Namespace(bpKyvernoPoliciesHRNamespace)
	_, err = hr.Patch(ctx, bpKyvernoPoliciesHRName, types.MergePatchType, patch, metav1.PatchOptions{
		FieldManager: "catalyst-api/phase2b-policy-enforce-flip",
	})
	if err != nil {
		if errors.IsNotFound(err) {
			h.log.Warn("phase2b: bp-kyverno-policies HR not found (Sovereign may not have it installed); skipping",
				"id", dep.ID,
			)
			return
		}
		h.log.Error("phase2b: HelmRelease PATCH failed",
			"id", dep.ID,
			"err", err,
		)
		return
	}

	h.log.Info("phase2b: bp-kyverno-policies bootstrapMode flipped false; Flux will reconcile 6 Enforce policies",
		"id", dep.ID,
	)

	// SSE event so the wizard's post-handover banner can render
	// "Compliance policies upgraded to Enforce mode". Mirrors the
	// fireHandover SSE pattern.
	dep.recordEvent(provisioner.Event{
		Time:    time.Now().UTC().Format(time.RFC3339),
		Phase:   "post-handover",
		Level:   "info",
		Message: "Wave 5.90 phase 2b: bp-kyverno-policies bootstrapMode → false; Flux reconciling 6 ClusterPolicies to Enforce target-state action",
	})
}
