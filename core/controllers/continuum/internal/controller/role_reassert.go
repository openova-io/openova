// #5224 — canonical role-password re-assert on acting-primary
// transition (the hw273 harbor 28P01 lockout, a #5220 G12 promote-flap
// casualty).
//
// THE DEFECT: during the hw273 G12 exercise the shared-pg pair's
// promote/failback machinery flipped which side acts as primary
// (region-b shared-pg-replica CR flipped 18:33:33Z; the dr-shared-pg
// per-CR goroutines restarted 18:33:57Z), and the shared primary's
// `harbor` role ended up asserted with the replica region's DIVERGENT
// locally-minted password (the bp-postgres pre-flip mint, fixed by the
// 0.2.13 side-gate in the same PR). CNPG could not self-heal: it
// detects password drift ONLY via the passwordSecret resourceVersion
// (managedRolesStatus stayed "reconciled" at rv 28321), so the
// out-of-band value stood and every NEW consumer connection failed
// with SQLSTATE 28P01 — 4,319 rejections in 80 minutes — until a
// manual Secret touch.
//
// THE RULE this file encodes: after ANY change of which pair half is
// ACTING primary — a promote, a failback, or a controller restart
// mid-flap (first observation) — the DR machinery re-asserts the
// canonical role passwords THROUGH CNPG's managed-roles contract
// (cnpg.ReassertManagedRoles: a metadata-only rv bump on each
// spec.managed.roles[].passwordSecret, which makes CNPG re-apply the
// password it owns). NEVER direct SQL against the write endpoint: a
// DR actor cannot know mid-flap which region's Secret set is
// authoritative, and an out-of-band ALTER is exactly the invisible
// clobber CNPG cannot detect.
//
// Firing on FIRST observation (per goroutine lifetime) is deliberate:
// the per-CR goroutines restart during the very incidents this heals
// (18:33:57Z), and the touch is idempotent — an already-canonical role
// gets a same-value re-apply, no consumer impact.
package controller

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/openova-io/openova/core/controllers/continuum/internal/cnpg"
)

// actingPrimaryName resolves which half of a cnpg-pair is CURRENTLY
// acting as primary from the live spec.replica.enabled flags
// (cnpg.Status.IsReplicaCluster) — the STATIC pair-role labels do NOT
// track post-failover reality (the rejoin.go lesson). Exactly one half
// must be acting primary and the other acting replica; every other
// combination (both / neither — a mid-switchover or malformed pair)
// returns "" so the caller never guesses.
func actingPrimaryName(primaryName string, primaryStatus cnpg.Status, replicaName string, replicaStatus cnpg.Status) string {
	switch {
	case !primaryStatus.IsReplicaCluster && replicaStatus.IsReplicaCluster:
		return primaryName
	case !replicaStatus.IsReplicaCluster && primaryStatus.IsReplicaCluster:
		return replicaName
	default:
		return ""
	}
}

// reassertRolesOnPrimaryTransition runs once per runPerCR tick, on the
// SAME FindPair/Get results the #4901 standby check already fetched (no
// extra CNPG API traffic on the read side). When the acting primary
// differs from the one this goroutine last handled — a promote, a
// failback, or the goroutine's first tick — it touches the acting
// primary's managed-role passwordSecrets (cnpg.ReassertManagedRoles) so
// CNPG re-applies the canonical passwords, healing any out-of-band role
// clobber accrued while the pair was in flux.
//
// The per-goroutine watermark (continuumGoroutine.lastActingPrimary) is
// advanced ONLY on a successful re-assert, so a transient API failure
// retries on the next tick instead of being lost.
func (r *ContinuumReconciler) reassertRolesOnPrimaryTransition(
	ctx context.Context,
	nn types.NamespacedName,
	reader *cnpg.Reader,
	namespace string,
	primaryName string, primaryStatus cnpg.Status,
	replicaName string, replicaStatus cnpg.Status,
) {
	log := ctrl.LoggerFrom(ctx).WithValues("name", nn.Name, "namespace", nn.Namespace)
	acting := actingPrimaryName(primaryName, primaryStatus, replicaName, replicaStatus)
	if acting == "" {
		// Ambiguous pair state (mid-switchover) — never guess; the
		// transition fires once the pair settles.
		return
	}

	key := nn.String()
	r.activeContinuumsMu.Lock()
	g, ok := r.activeContinuums[key]
	last := ""
	if ok {
		last = g.lastActingPrimary
	}
	r.activeContinuumsMu.Unlock()
	if !ok || last == acting {
		return
	}

	touched, err := reader.ReassertManagedRoles(ctx, namespace, acting, r.now())
	if err != nil {
		// Watermark NOT advanced — retried next tick (self-healing on
		// transient conflicts / partial touches).
		log.Error(err, "role re-assert on acting-primary transition failed; retrying next tick (#5224)",
			"actingPrimary", acting, "touched", touched)
		return
	}

	r.activeContinuumsMu.Lock()
	if g, ok := r.activeContinuums[key]; ok {
		g.lastActingPrimary = acting
	}
	r.activeContinuumsMu.Unlock()

	if len(touched) > 0 {
		log.Info("re-asserted canonical role passwords via CNPG managed-roles contract (passwordSecret rv touch) on acting-primary transition (#5224)",
			"actingPrimary", acting, "previous", last, "touchedSecrets", touched)
	}
}
