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
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/core/controllers/internal/placement"
)

// platformBootstrapOwnedHostRegion is the placeholder region literal every
// bootstrap-owned Application CR carries in spec.regions
// (`platform/<x>/chart/templates/application-cr.yaml`). It is NOT a real
// host-cluster region — it exists only to satisfy the pre-#3370 CRD's
// region-pattern requirement (4 letter-only dash tokens) for OLD-CRD
// compatibility. A CNPG-backed / shared-data bootstrap app (shared-pg) carries
// EXACTLY this single placeholder, so its resolved placement plan has no real
// primary/standby regions — the live DR truth must come from the governing
// Continuum, not from this literal. (#4605 follow-up.)
const platformBootstrapOwnedHostRegion = "platform-bootstrap-owned-host"

// clusterNameProviders / clusterNameBuildingBlocks / clusterNameEnvTypes are
// the closed token sets a host-cluster name (`{prov}-{reg}-{bb}-{env_type}`,
// docs/ARCHITECTURE.md §4 Naming) is built from. normalizeRegion uses the
// `-{bb}-{env_type}` TAIL + a leading {prov} segment to recognise a FULL
// cluster name (e.g. hw-me-east-215-a-rtz-prod, hz-fsn-rtz-prod) and strip it
// to the bare region — WITHOUT mis-firing on an already-bare region label
// (me-east-215-a), which carries no provider prefix and no bb token. Anchoring
// on the known token sets (rather than raw segment count) is what disambiguates
// the two 4-segment shapes me-east-215-a (region) and hz-fsn-rtz-prod
// (cluster). Mirrors the openova.io/{building-block,environment-type} label
// vocabulary.
var (
	clusterNameProviders      = map[string]bool{"hw": true, "hz": true, "aws": true, "gcp": true, "azure": true}
	clusterNameBuildingBlocks = map[string]bool{"rtz": true, "dmz": true, "mgt": true, "mgmt": true}
	clusterNameEnvTypes       = map[string]bool{"prod": true, "stg": true, "staging": true, "dev": true, "uat": true}
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
	// the continuum-controller flips the lease on failover. Always a
	// NORMALIZED region label (a cnpg-pair Continuum stamps the full cluster
	// name `hw-me-east-215-a-rtz-prod`; continuumDRStatusFromCR folds it to
	// the bare region `me-east-215-a` so the projection is consistent with
	// the per-app `dr-<app>` Continuums, which already carry bare regions).
	LeaseHolder string

	// StandbyRegions are the NORMALIZED standby region labels read from the
	// Continuum spec.hotStandbyRegions. Used to populate
	// status.placement.standbyRegions when the static placement plan has no
	// REAL standby regions to offer — i.e. a CNPG-backed bootstrap app whose
	// spec.regions is the `platform-bootstrap-owned-host` placeholder, so the
	// resolved plan carries no second region. (#4605 follow-up.)
	StandbyRegions []string

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
	rawHolder, _, _ := unstructured.NestedString(cr.Object, "status", "leaseHolder")
	out.LeaseHolder = normalizeRegion(rawHolder)
	// spec.hotStandbyRegions is the configured standby roster. Normalize each
	// (the cnpg-pair Continuum stamps full cluster names) so a CNPG-backed app
	// whose static plan has no real standby can still surface its standby
	// region(s). Distinct from the live leaseHolder, which is what the standby
	// roster is computed AGAINST in buildPlacementProjection.
	if standbys, found, _ := unstructured.NestedStringSlice(cr.Object, "spec", "hotStandbyRegions"); found {
		for _, s := range standbys {
			if n := normalizeRegion(s); n != "" {
				out.StandbyRegions = append(out.StandbyRegions, n)
			}
		}
	}
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

	// Whether the static plan carries REAL host-cluster regions. A CNPG-backed
	// bootstrap app (shared-pg) resolves its plan from the placeholder literal
	// `platform-bootstrap-owned-host`, so plan.Regions holds no real region —
	// the live DR truth (primary + standby roster) must come entirely from the
	// governing Continuum. A spine app carries real spec.regions, so its plan
	// IS the source of truth (the Continuum only overrides the primary on a
	// failover flip).
	planHasRealRegions := false
	for _, rp := range plan.Regions {
		if rp.Name != platformBootstrapOwnedHostRegion {
			planHasRealRegions = true
			break
		}
	}

	// Effective primary: the live lease holder (normalized) wins over the
	// configured plan primary so the projection tracks failover, not just
	// config. For a placeholder-region plan it is the ONLY primary signal.
	primary := plan.PrimaryRegion
	if cs != nil && cs.LeaseHolder != "" {
		primary = cs.LeaseHolder
	}

	// Standbys: every standby region except the effective primary, in priority
	// order. Recomputing against the EFFECTIVE primary (not the static plan
	// primary) means a failed-over deployment lists the OLD primary as a
	// standby and the new primary is excluded.
	//
	// Source of the standby roster:
	//   - a plan WITH real regions ⇒ the plan regions (the spine path; the
	//     Continuum only flips which one is primary, never the membership).
	//   - a plan WITHOUT real regions (placeholder) ⇒ the Continuum's
	//     hotStandbyRegions (the CNPG-backed shared-data path). Without this a
	//     CNPG-backed app would project an EMPTY standbyRegions even though the
	//     Continuum knows the standby region (the #4605 follow-up gap).
	standbyNames := make([]string, 0, len(plan.Regions))
	if planHasRealRegions {
		for _, rp := range plan.Regions {
			standbyNames = append(standbyNames, rp.Name)
		}
	} else if cs != nil {
		standbyNames = append(standbyNames, cs.StandbyRegions...)
	}
	standbys := make([]interface{}, 0, len(standbyNames))
	for _, name := range standbyNames {
		if name == primary || name == "" {
			continue
		}
		standbys = append(standbys, name)
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

// normalizeRegion folds a host-cluster name (`{prov}-{reg}-{bb}-{env_type}`,
// e.g. hw-me-east-215-a-rtz-prod, hz-fsn-rtz-prod) down to its bare region
// label (me-east-215-a, fsn) so the placement projection speaks ONE region
// vocabulary regardless of which Continuum produced the value (the cnpg-pair
// Continuum stamps full cluster names; the per-app `dr-<app>` Continuums stamp
// bare regions). It is IDEMPOTENT: a value that is already a bare region is
// returned unchanged.
//
// Detection anchors on the CLOSED token sets, NOT raw segment count — that is
// what disambiguates the two 4-segment shapes me-east-215-a (region:
// non-provider first segment, non-env last segment) and hz-fsn-rtz-prod
// (cluster: provider `hz` first, building-block `rtz` second-last, env `prod`
// last). A value is a full cluster name iff:
//   - it has ≥4 dash segments, AND
//   - the first segment is a known provider, AND
//   - the second-to-last segment is a known building-block, AND
//   - the last segment is a known env_type.
//
// When all hold, strip the leading provider + the trailing {bb}-{env_type},
// leaving the region. Otherwise the value is returned verbatim (a bare region,
// the `platform-bootstrap-owned-host` placeholder, or anything unexpected) —
// deliberately conservative so an unrecognised value is surfaced visibly rather
// than silently mangled.
func normalizeRegion(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	parts := strings.Split(s, "-")
	if len(parts) < 4 {
		return s
	}
	prov := parts[0]
	bb := parts[len(parts)-2]
	env := parts[len(parts)-1]
	if !clusterNameProviders[prov] || !clusterNameBuildingBlocks[bb] || !clusterNameEnvTypes[env] {
		return s
	}
	region := strings.Join(parts[1:len(parts)-2], "-")
	if region == "" {
		return s
	}
	return region
}

// resolvePlatformScopeContinuumStatus reads the SINGLE platform-scope
// Continuum (label `openova.io/scope=platform`) — the cnpg-pair DR contract
// that governs the shared CNPG data plane backing the shared-data bootstrap
// apps (shared-pg). It is the fallback the bootstrap projection uses when a
// CNPG-backed app has NEITHER a back-referenced continuumRef NOR a per-app
// `dr-<app>` CR: those apps are governed by the cnpg-pair Continuum, not a
// per-app one. Returns (nil, nil) when no platform-scope Continuum exists (a
// single-region or non-CNPG Sovereign) — the caller then keeps the static
// projection. A genuine list error is returned so the caller can degrade
// gracefully rather than fail the reconcile. (#4605 follow-up.)
func (r *Reconciler) resolvePlatformScopeContinuumStatus(ctx context.Context) (*continuumDRStatus, error) {
	list, err := r.Dynamic.Resource(ContinuumGVR).Namespace("").List(ctx, metav1.ListOptions{
		LabelSelector: "openova.io/scope=platform",
	})
	if err != nil {
		return nil, err
	}
	if list == nil || len(list.Items) == 0 {
		return nil, nil
	}
	// Exactly one platform-scope Continuum is expected (the cnpg-pair). If a
	// future Sovereign carries more, the first deterministically-ordered one
	// is the shared data plane's contract — the List is name-ordered.
	cr := list.Items[0]
	return continuumDRStatusFromCR(&cr), nil
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
