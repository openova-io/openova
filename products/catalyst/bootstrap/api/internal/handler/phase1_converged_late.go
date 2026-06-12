package handler

import (
	"context"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
)

// Converged-late handover rescue (#3319, founder decision 2026-06-12).
//
// A Phase-1 watch TIMEOUT is the recoverable classification — Flux keeps
// reconciling long after the budget, and many such clusters reach full
// convergence MINUTES later (hw130: timed out at 120m because the #3285
// secret-starve ate the budget, then converged to 57/57). But every
// lifecycle step is OutcomeReady-gated, so a converged-late env was
// permanently half-armed: mothership shows "failed", handover never
// fires, the chroot's provisioning service waits forever on the cutover
// token, jobs/treemap never see region-b. The founder's call: a record
// whose cluster DEMONSTRABLY converged hands over — the record must
// reflect reality.
//
// Safety: hard failures (OutcomeFailed, flux-not-reconciling) are
// excluded; the census requires BOTH an absolute floor and a 90% ratio
// of Ready HelmReleases, so a half-dead cluster never rescues. Every
// downstream step (fireHandover, job sweep, policy flip) is idempotent
// and self-guarding.

// convergedLateMinReady is the absolute floor of Ready HelmReleases —
// just under the canonical bootstrap-kit size so a kit-trim doesn't
// silently disable the rescue, far above any half-converged wedge.
const convergedLateMinReady = 45

// shouldConvergedLateRescue gates the startup-restore hook. Read-only.
func (h *Handler) shouldConvergedLateRescue(dep *Deployment) bool {
	dep.mu.Lock()
	status := dep.Status
	outcome := ""
	fired := false
	if dep.Result != nil {
		outcome = dep.Result.Phase1Outcome
		fired = dep.Result.HandoverFiredAt != nil
	}
	dep.mu.Unlock()
	if status != "failed" || outcome != helmwatch.OutcomeTimeout || fired {
		return false
	}
	if _, ok := h.resolvePrimaryKubeconfigPath(dep); !ok {
		h.log.Warn("converged-late: timeout record has no resolvable kubeconfig; skipping",
			"id", dep.ID)
		return false
	}
	return true
}

// runConvergedLateRescue censuses the live cluster and, on full
// convergence, flips the record to ready and fires the complete
// handover chain. Run on a goroutine from the restore loop.
func (h *Handler) runConvergedLateRescue(dep *Deployment) {
	path, ok := h.resolvePrimaryKubeconfigPath(dep)
	if !ok {
		return
	}
	ready, total, err := censusHelmReleases(path)
	if err != nil {
		h.log.Warn("converged-late: census failed; leaving record untouched",
			"id", dep.ID, "err", err)
		return
	}
	if ready < convergedLateMinReady || ready*10 < total*9 {
		h.log.Info("converged-late: cluster not (yet) converged; record stays failed",
			"id", dep.ID, "ready", ready, "total", total)
		return
	}

	now := time.Now().UTC()
	dep.mu.Lock()
	// Re-check under lock — a concurrent resume/flip loses.
	if dep.Status != "failed" {
		dep.mu.Unlock()
		return
	}
	dep.Status = "ready"
	if dep.Result != nil && dep.Result.Phase1FinishedAt == nil {
		dep.Result.Phase1FinishedAt = &now
	}
	dep.mu.Unlock()
	h.persistDeployment(dep)
	h.log.Info("converged-late RESCUE: timeout record flipped to ready — firing full handover chain",
		"id", dep.ID, "ready", ready, "total", total)

	// The full OutcomeReady chain, each step idempotent/self-guarding:
	// fireHandover mints + exports the record AND fans out the secondary
	// kubeconfigs (deployment_handover_export.go); the sweep stamps
	// stale install rows; the policy flip restores kyverno Enforce
	// targets. Mesh reconcile is level-triggered separately on startup.
	h.fireHandover(dep)
	h.runHandoverJobSweep(dep)
	go h.runPostHandoverPolicyEnforceFlip(dep)
}

// censusHelmReleases counts Ready=True vs total HelmReleases on the
// cluster behind the given kubeconfig file. Pure helper, test-seamed.
var censusHelmReleases = func(kubeconfigPath string) (ready, total int, err error) {
	raw, err := os.ReadFile(kubeconfigPath)
	if err != nil {
		return 0, 0, err
	}
	cfg, err := clientcmd.RESTConfigFromKubeConfig(raw)
	if err != nil {
		return 0, 0, err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return 0, 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	list, err := dyn.Resource(helmReleaseGVR).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, 0, err
	}
	for i := range list.Items {
		total++
		conds, _, _ := unstructured.NestedSlice(list.Items[i].Object, "status", "conditions")
		for _, c := range conds {
			cm, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			if cm["type"] == "Ready" && cm["status"] == "True" {
				ready++
				break
			}
		}
	}
	return ready, total, nil
}
