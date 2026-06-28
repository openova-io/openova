// placement_projection.go — #4601 (Refs #3986 #3375): project the
// EFFECTIVE per-region DR placement onto Application.status.placement so the
// per-app Topology DR strip (#932 / #3986 / #3375, UAT rows 51/52/55/56/57)
// has a primary↔standby split to render for HelmRelease-backed apps.
//
// THE GAP this closes:
//
// An Application CR carries `spec.placement` (active-hot-standby /
// active-passive / singleton) but, before this, `status.placement` carried
// only the static {vcluster, source, regions, clusters} shape (#3373) — and
// the bootstrap-owned path (the path EVERY DR spine app — spine-gitea,
// spine-harbor, spine-keycloak, spine-openbao — traverses) never wrote even
// that. So the Topology UI had nothing to derive primary/standby from.
//
// Meanwhile the per-app Continuum CR (`continuums.dr.openova.io`, minted by
// continuum.go + back-referenced via `status.continuumRef`) carries the LIVE
// truth: `status.leaseHolder` (= the region currently primary),
// `status.replicationLagSeconds`, and the `LeaseHeld` condition. This
// projection READS that Continuum status and folds it into
// `status.placement` so the UI reads one place.
//
// Strictly OBSERVATIONAL: this code only REPORTS placement; it never changes
// which region is primary, never touches an HR replica count, never writes
// the Continuum. It derives:
//
//	status.placement.mode                  — the resolved placement mode
//	status.placement.primaryRegion         — Continuum leaseHolder, else the
//	                                          static plan primary
//	status.placement.standbyRegions[]      — every plan region except primary
//	status.placement.replicationLagSeconds — from the Continuum status (when present)
//	status.placement.leaseHeld             — Continuum LeaseHeld condition (when present)
//
// When no Continuum exists (singleton, single-region, non-DR app) the
// projection falls back to the static placement plan: primary = plan
// primary, standbys = the plan's standby regions, lag/leaseHeld omitted.

package controller

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/core/controllers/internal/placement"
)

// placementProjectionForPlan resolves the DR placement projection for an
// already-resolved placement plan, folding in the live Continuum status read
// from continuumRef (#4601). Used by the normal (non-bootstrap) reconcile
// path, where the plan is resolved as part of the fan-out. A Continuum read
// error degrades to the static-plan projection (still a valid primary/standby
// split) rather than failing the reconcile. Returns nil when the plan has no
// regions to project.
func (r *Reconciler) placementProjectionForPlan(
	ctx context.Context,
	plan placement.Plan,
	continuumRef string,
) map[string]interface{} {
	cs, err := r.readContinuumDRStatus(ctx, continuumRef)
	if err != nil {
		r.Log.Warn("Continuum status read failed; static placement projection",
			"continuumRef", continuumRef, "err", err)
		cs = nil
	}
	return buildPlacementProjection(plan, cs)
}

// continuumDRStatus is the minimal projection of a per-app Continuum CR's
// status the placement projection consumes. A nil value means "no Continuum
// for this app" — the projection then falls back to the static plan.
type continuumDRStatus struct {
	// LeaseHolder is the region currently holding the DR lease — i.e. the
	// live primary. Authoritative over the static plan primary, which is
	// only the *configured* (regions[0]) primary and goes stale the moment
	// the continuum-controller flips the lease on failover.
	LeaseHolder string

	// ReplicationLagSeconds mirrors the Continuum status field. -1 means
	// "the Continuum reported no lag value" (omit from the projection).
	ReplicationLagSeconds int64

	// HasLag reports whether ReplicationLagSeconds was present on the CR
	// status (distinguishes a genuine 0 from an absent field).
	HasLag bool

	// LeaseHeld is the Continuum's `LeaseHeld` condition status == "True".
	LeaseHeld bool

	// HasLeaseHeld reports whether the LeaseHeld condition was present.
	HasLeaseHeld bool
}

// readContinuumDRStatus fetches the per-app Continuum CR named by the
// `<namespace>/<name>` continuumRef and extracts the DR status fields the
// placement projection needs. Returns (nil, nil) when continuumRef is empty
// or the CR is not found (a non-DR app, or the producer has not minted the CR
// yet) — the caller then falls back to the static plan. A genuine read error
// is returned so the caller can decide whether to surface it; the projection
// itself never fails a reconcile over a missing Continuum.
func (r *Reconciler) readContinuumDRStatus(ctx context.Context, continuumRef string) (*continuumDRStatus, error) {
	ns, name, ok := splitNamespacedName(continuumRef)
	if !ok {
		return nil, nil
	}
	cr, err := r.Dynamic.Resource(ContinuumGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return continuumDRStatusFromCR(cr), nil
}

// continuumDRStatusFromCR is the pure extraction of the DR status fields from
// an already-fetched Continuum CR. Split out so unit tests can exercise the
// projection against a constructed Unstructured without a client.
func continuumDRStatusFromCR(cr *unstructured.Unstructured) *continuumDRStatus {
	if cr == nil {
		return nil
	}
	out := &continuumDRStatus{ReplicationLagSeconds: -1}
	out.LeaseHolder, _, _ = unstructured.NestedString(cr.Object, "status", "leaseHolder")
	if lag, found, _ := unstructured.NestedInt64(cr.Object, "status", "replicationLagSeconds"); found {
		out.ReplicationLagSeconds = lag
		out.HasLag = true
	}
	conds, _, _ := unstructured.NestedSlice(cr.Object, "status", "conditions")
	for _, c := range conds {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if t, _ := cm["type"].(string); t == "LeaseHeld" {
			s, _ := cm["status"].(string)
			out.LeaseHeld = s == "True"
			out.HasLeaseHeld = true
			break
		}
	}
	return out
}

// buildPlacementProjection projects the effective per-region DR placement
// from the resolved placement plan, optionally overridden by the LIVE
// Continuum status. PURE — no client calls — so the controller status writer
// and the unit tests share one code path.
//
// Derivation:
//
//   - mode               — plan.Mode (the canonical placement token).
//   - primaryRegion      — Continuum leaseHolder when present + non-empty
//     (the live primary survives a failover flip); otherwise the static
//     plan primary (regions[0] for the single-primary modes; empty for
//     active-active, which has no primary concept).
//   - standbyRegions[]   — every plan region that is NOT the effective
//     primary, in plan (priority) order. This is recomputed against the
//     effective (possibly failed-over) primary so a post-failover projection
//     correctly lists the OLD primary as a standby.
//   - replicationLagSeconds / leaseHeld — surfaced only when the Continuum
//     status carried them (a non-DR/static projection omits both).
//
// Returns nil only when the plan has no regions at all (nothing to project) —
// the caller then leaves status.placement untouched.
func buildPlacementProjection(plan placement.Plan, cs *continuumDRStatus) map[string]interface{} {
	if len(plan.Regions) == 0 {
		return nil
	}

	// Effective primary: the live lease holder wins over the configured
	// plan primary so the projection tracks failover, not just config.
	primary := plan.PrimaryRegion
	if cs != nil && cs.LeaseHolder != "" {
		primary = cs.LeaseHolder
	}

	// Standbys: every plan region except the effective primary, in plan
	// (priority) order. Recomputing against the EFFECTIVE primary (not the
	// static plan.PrimaryRegion) means a failed-over deployment lists the
	// old primary as a standby and the new primary is excluded.
	standbys := make([]interface{}, 0, len(plan.Regions))
	for _, rp := range plan.Regions {
		if rp.Name == primary {
			continue
		}
		standbys = append(standbys, rp.Name)
	}

	proj := map[string]interface{}{
		"mode":           plan.Mode,
		"primaryRegion":  primary,
		"standbyRegions": standbys,
	}
	if cs != nil {
		if cs.HasLag {
			proj["replicationLagSeconds"] = cs.ReplicationLagSeconds
		}
		if cs.HasLeaseHeld {
			proj["leaseHeld"] = cs.LeaseHeld
		}
	}
	return proj
}

// mergePlacementProjection folds the DR projection (mode / primaryRegion /
// standbyRegions / replicationLagSeconds / leaseHeld) onto an existing
// status.placement object (the #3373 {vcluster, source, regions, clusters}
// shape), or returns the projection alone when there is no base object. The
// DR keys are additive — they never overwrite the #3373 keys. Returns nil
// when there is nothing to write (no base + nil projection).
func mergePlacementProjection(base, projection map[string]interface{}) map[string]interface{} {
	if projection == nil {
		return base
	}
	if base == nil {
		return projection
	}
	for k, v := range projection {
		base[k] = v
	}
	return base
}

// splitNamespacedName splits a `<namespace>/<name>` ref. Returns ok=false for
// any input that is not exactly one `/`-separated namespace+name pair.
func splitNamespacedName(ref string) (namespace, name string, ok bool) {
	for i := 0; i < len(ref); i++ {
		if ref[i] == '/' {
			ns, nm := ref[:i], ref[i+1:]
			if ns == "" || nm == "" {
				return "", "", false
			}
			// Reject a second slash (not a simple ns/name).
			for j := i + 1; j < len(ref); j++ {
				if ref[j] == '/' {
					return "", "", false
				}
			}
			return ns, nm, true
		}
	}
	return "", "", false
}
