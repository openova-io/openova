package handler

import (
	"context"
	"os"
	"sort"
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
// Safety: unrecoverable outcomes (flux-not-reconciling, kubeconfig-missing,
// watcher-start-failed, flux-crds-absent) are excluded; the census requires
// BOTH an absolute floor and a 90% ratio of Ready HelmReleases, so a half-dead
// cluster never rescues. Every downstream step (fireHandover, job sweep,
// policy flip) is idempotent and self-guarding.
//
// #5253 extends the same rescue to failed+OutcomeReady — the pre-#5253
// console-downgrade signature (hw276): the primary fully converged
// (every primary HR installed) but the #4706 console-reachability gate
// latched the record "failed", skipping the whole producer chain with
// no heal path (`failed && OutcomeReady` matched neither this gate nor
// the mesh status gate). markPhase1Done no longer produces that shape
// (a console-degraded record stays "ready" with the non-fatal
// ConsoleDegraded surface), so this arm heals records PERSISTED by
// older builds on the next catalyst-api restart. The live census below
// is the primary-converged proof either way, and the stale #4706
// console error is re-homed onto the ConsoleDegraded surface so the
// rescued record is not ready-with-FailureCard.
//
// #6082 extends it a third time, to failed+OutcomeFailed — the LATCH shape,
// measured on hw293 (dep a0077ba47e3720e5). Phase-1 terminated with exactly
// ONE failed component out of 67, and that component was
// `self-sovereign-cutover` itself, at chart 0.1.177, on the 1 MiB Helm
// release-Secret limit (#6004). Flux's infinite install retry landed 0.1.179
// Ready=True three hours later, so the cluster is fully converged — but the
// record is frozen. `fireHandover` runs only under
// `OutcomeReady && finalStatus=="ready"`; `MintHandoverToken` 409s for any
// status outside {ready, adopted}; and those two are the ONLY producers of the
// Sovereign-side handover marker `secret/catalyst/tofu-phase0-archive`.
// Without that marker `/internal/cutover/trigger` answers 425
// handover-incomplete forever and the level-triggered cutover reconciler
// (#4635) returns silently on `!sealed` every 120s. The record could not leave
// "failed" because the only heal path pre-filtered on the FROZEN
// classification and never asked the cluster — a gate testing a condition that
// had become unreachable. The circularity is the sharp edge: the component
// whose install failure latched the record is the cutover chart, dormant by
// design, whose HR health says nothing about the Sovereign's.
//
// The floor + 90% ratio backstop is NOT sufficient on its own for this arm: it
// tolerates ~10% non-Ready, so a genuinely-still-failed component fits inside
// it (hw293's live 71/77 clears both while one bad component hides in the 6).
// So the failed arm additionally demands POSITIVE, per-component proof — every
// component the record recorded as `failed` must be observed Ready=True on the
// live cluster now. A component that is absent, non-Ready, or simply
// unobserved is never counted as recovered.

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
	if status != "failed" || fired {
		return false
	}
	// TIMEOUT (#3319, the original converged-late shape), OutcomeReady
	// (#5253, the pre-fix console-downgrade shape) and FAILED (#6082, the
	// hw293 latch — a component that failed and has since self-healed on
	// Flux's infinite retry) are CANDIDATES; the live census in
	// runConvergedLateRescue decides, not this frozen classification.
	//
	// Every remaining outcome names a cluster the census cannot speak for —
	// no kubeconfig, no Flux, no CRDs, nothing observed — so those stay
	// excluded here rather than being censused into a false rescue.
	if outcome != helmwatch.OutcomeTimeout &&
		outcome != helmwatch.OutcomeReady &&
		outcome != helmwatch.OutcomeFailed {
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
	ready, total, readyIDs, err := censusHelmReleases(path)
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

	// #6082 — the FAILED arm's per-component recovery proof. See the package
	// comment: the floor+ratio above tolerates ~10% non-Ready, which is wide
	// enough to hide the very component whose failure latched the record. A
	// record that named specific failures must show every one of them
	// recovered, by POSITIVE observation, before it may claim ready.
	if stillFailed := notRecoveredFailedComponents(dep, readyIDs); len(stillFailed) > 0 {
		h.log.Info("converged-late: record stays failed — component(s) it recorded as failed are not observed Ready on the live cluster",
			"id", dep.ID, "ready", ready, "total", total, "stillFailed", stillFailed)
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
	if dep.Result != nil {
		if dep.Result.Phase1FinishedAt == nil {
			dep.Result.Phase1FinishedAt = &now
		}
		// #5253 — a pre-fix console-downgrade record (failed +
		// Phase1Outcome=="ready") carries the #4706 "console is NOT
		// externally reachable" text on dep.Error. Re-home it onto the
		// non-fatal ConsoleDegraded surface: the rescued record must not
		// read ready-with-FailureCard (/deployments/{id} renders a
		// non-empty Error as a hard failure), and the console signal
		// stays surfaced instead of being deleted. Timeout records keep
		// their existing Error handling untouched.
		if dep.Result.Phase1Outcome == helmwatch.OutcomeReady && dep.Error != "" {
			dep.Result.ConsoleDegraded = true
			dep.Result.ConsoleDegradedDetail = dep.Error
			dep.Error = ""
		}
		// #6082 — the FAILED arm's stale text ("Phase 1 finished with N failed
		// component(s)…") is now provably obsolete: the census just confirmed
		// every named component Ready. Clear it, for the same reason #5253
		// re-homes the console text — /deployments/{id} renders a non-empty
		// Error as a hard FailureCard, so a rescued record must not carry one.
		// There is no non-fatal surface to re-home it to (unlike the console
		// condition) because the condition itself no longer holds.
		if dep.Result.Phase1Outcome == helmwatch.OutcomeFailed {
			dep.Error = ""
		}
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
	// #4212 — apply the real-id Observe-first CloudAdoption claims on the
	// converged-late path too (idempotent server-side-apply). See
	// post_handover_adoption_apply.go.
	go h.runPostHandoverAdoptionApply(dep)
	// #4212 Seam 3 — enroll the DR-capable spine into the object model on the
	// converged-late path too (idempotent server-side-apply; adopt-not-roll).
	// See post_handover_spine_apps.go.
	go h.runPostHandoverSpineApplications(dep)
	// #4690 / #4686 — reconcile the Huawei gateway ELB members to the live
	// gateway-Service nodePort on the converged-late path too (idempotent; no-op
	// on Hetzner / when already reconciled). See post_handover_gateway_elb.go.
	go h.runPostHandoverGatewayELB(dep)
}

// notRecoveredFailedComponents returns the component ids the deployment
// record stamped as FAILED that the live census did NOT observe Ready=True.
// An empty result means every recorded failure has demonstrably recovered.
//
// Only the #6082 FAILED arm consults it. A TIMEOUT record has no hard-failed
// component by definition (markPhase1Done classifies "some observed, none
// hard-failed, budget exhausted" as OutcomeTimeout), and a #5253
// console-downgrade record converged on every primary HelmRelease before the
// console probe downgraded it — so applying the proof to those arms would
// only re-litigate a question their own outcome already answered.
//
// Evidence direction is deliberate: readyIDs is a POSITIVE set, so a
// component that is absent from the cluster, non-Ready, or simply unobserved
// stays in the not-recovered list. Absence of evidence never reads as
// recovery.
func notRecoveredFailedComponents(dep *Deployment, readyIDs map[string]bool) []string {
	dep.mu.Lock()
	defer dep.mu.Unlock()
	if dep.Result == nil || dep.Result.Phase1Outcome != helmwatch.OutcomeFailed {
		return nil
	}
	var stillFailed []string
	for comp, state := range dep.Result.ComponentStates {
		if state != helmwatch.StateFailed {
			continue
		}
		if !readyIDs[comp] {
			stillFailed = append(stillFailed, comp)
		}
	}
	sort.Strings(stillFailed)
	return stillFailed
}

// censusHelmReleases counts Ready=True vs total HelmReleases on the
// cluster behind the given kubeconfig file, and returns the SET of
// component ids observed Ready=True. Pure helper, test-seamed.
//
// readyIDs is keyed by the Sovereign-Admin component id
// (helmwatch.ComponentIDFromHelmRelease — "bp-cilium" → "cilium"), which is
// exactly how Result.ComponentStates is keyed, so the #6082 per-component
// recovery proof can compare the two directly. Membership is POSITIVE
// evidence only: an absent or non-Ready HelmRelease simply never enters the
// set, so "not recovered" is the default and a missing observation can never
// be mistaken for a recovery.
var censusHelmReleases = func(kubeconfigPath string) (ready, total int, readyIDs map[string]bool, err error) {
	readyIDs = map[string]bool{}
	raw, err := os.ReadFile(kubeconfigPath)
	if err != nil {
		return 0, 0, readyIDs, err
	}
	cfg, err := clientcmd.RESTConfigFromKubeConfig(raw)
	if err != nil {
		return 0, 0, readyIDs, err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return 0, 0, readyIDs, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	list, err := dyn.Resource(helmReleaseGVR).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, 0, readyIDs, err
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
				readyIDs[helmwatch.ComponentIDFromHelmRelease(list.Items[i].GetName())] = true
				break
			}
		}
	}
	return ready, total, readyIDs, nil
}
