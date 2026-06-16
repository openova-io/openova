// continuum.go — #3375 DoD-3: per-app Continuum CR producer.
//
// The #3698 spine landed the engine (generic `stateless` Promoter,
// `canonicalizeTopology`, the standby-region integrity gate, the
// `replicas:0` standby overlay on passive HRs) but EXPLICITLY DEFERRED
// the producer: the thing that mints one `Continuum.dr.openova.io/v1`
// CR per DR-capable Application so `kubectl get continuums.dr.openova.io
// -A` shows a per-app row (dr-grafana, dr-sso-bridge, …) and the
// continuum-controller (bootstrap-kit slot 62) has a real DR contract
// to reconcile for EVERY DR app — not just the cnpg-pair (which mints
// its own CR from platform/cnpg-pair/chart/templates/continuum.yaml).
//
// Before this, ONLY a bp-cnpg-pair install produced a Continuum CR (via
// its own chart template). A stateless DR app (bp-sso-bridge, bp-livekit,
// …) declared `switchover.mechanism` in its Blueprint topology but no CR
// was ever created, so the continuum-controller had nothing to drive and
// the DNS-flip-only switchover the #3698 stateless Promoter implements
// could never fire. This producer closes that gap GENERICALLY — zero
// app-name literal, keyed entirely off the resolved topology variant.
//
// Where it runs: in the topology fan-out persist block of Reconcile
// (application_controller.go), right after the per-cluster HelmReleases
// are upserted. It is additive — a singleton / single-region app, or a
// Blueprint whose variant declares no switchover mechanism, produces NO
// Continuum CR (the bool gate below returns nil).
//
// Lifecycle: upserted idempotently via upsertHostResource (byte-equal
// short-circuit, same as the HRs) and cascade-deleted in handleDeletion.

package controller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	topo "github.com/openova-io/openova/core/controllers/application/internal/render"
	"github.com/openova-io/openova/core/controllers/internal/placement"
	bpv1alpha1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
)

// Continuum CR pin — mirrors the continuum-controller's constants
// (core/controllers/continuum/internal/controller/continuum_controller.go)
// + the CRD at products/catalyst/chart/crds/continuum.yaml. Kept local so
// the application-controller does not import the continuum-controller
// package (they are separate binaries).
const (
	ContinuumGroup    = "dr.openova.io"
	ContinuumVersion  = "v1"
	ContinuumKind     = "Continuum"
	ContinuumResource = "continuums"
)

// ContinuumGVR — the dynamic-client resource the producer upserts.
var ContinuumGVR = schema.GroupVersionResource{
	Group:    ContinuumGroup,
	Version:  ContinuumVersion,
	Resource: ContinuumResource,
}

// ContinuumNamePrefix — every per-app Continuum CR is named
// `dr-<app>` so an operator's `kubectl get continuums.dr.openova.io -A`
// reads as a per-app DR roster (dr-grafana, dr-sso-bridge). The cnpg-pair
// chart's own CR uses the `<fullname>-continuum` suffix convention, so
// the two never collide.
const ContinuumNamePrefix = "dr-"

// DefaultContinuumLeaseKind is the witness backend stamped on a produced
// Continuum CR when the Blueprint variant does not name one. `dns-quorum`
// is the canonical air-gappable witness (no Cloudflare dependency) and is
// one of the two values the CRD's leaseClient.kind enum accepts
// (`[cloudflare-kv, dns-quorum]` — `in-memory` is test-only and NOT a
// valid CRD value). Per Inviolable Principle #4 the controller could be
// taught to read this from env later; the default keeps a fresh prov's
// DR contract self-contained.
const DefaultContinuumLeaseKind = "dns-quorum"

// continuumPlan is the minimal, resolved input the producer needs. It is
// computed by the reconciler from the SAME placement.Plan + topology
// variant the fan-out already resolved — no second resolution pass.
type continuumPlan struct {
	// AppRef is `<namespace>/<name>` of the governed Application
	// (Continuum.spec.applicationRef — the continuum-controller keys its
	// per-CR goroutine off this).
	AppRef string

	// PrimaryRegion is the canonical 4-segment region name currently
	// active (placement.Plan.PrimaryRegion). Must satisfy the CRD region
	// pattern `^[a-z][a-z0-9]*(-[a-z0-9]+){3,}$`.
	PrimaryRegion string

	// StandbyRegions are the passive regions in priority order
	// (placement.Plan standby rows). Index 0 is the preferred failover
	// target.
	StandbyRegions []string

	// Mechanism is the resolved switchover mechanism the
	// continuum-controller dispatches through (cnpg-pair | raft-transition
	// | stateless). Derived from the Blueprint variant's
	// switchover.mechanism + replication.backend — see
	// resolveSwitchoverMechanism.
	Mechanism string

	// RTOSeconds / RPOSeconds — recovery objectives surfaced from the
	// Blueprint variant's switchover block. 0 = omit (CRD defaults apply).
	RTOSeconds int
	RPOSeconds int

	// OwnerLabels — org / env-type / app-uid, mirrored from the fan-out
	// so the CR participates in the same observability rollup + cascade.
	OwnerLabels map[string]string
}

// buildContinuumPlan derives the per-app Continuum contract from the
// resolved topology variant + placement plan. Returns (plan, true) only
// when the app is a DR-capable MULTI-region topology that declares a
// switchover mechanism; otherwise (zero, false) — the caller then skips
// CR production entirely.
//
// The DR-capable gate is intentionally strict + generic (zero app-name
// literal):
//
//   - the resolved topology must be active-hot-standby OR active-passive
//     (active-active is multi-master — no primary/standby flip to drive;
//     singleton is single-cluster — nothing to fail over);
//   - the placement plan must carry at least one standby region (a
//     hot-standby topology that resolved to a single region on this
//     Sovereign is NOT a DR contract — the #3698 standby-region integrity
//     gate already fails such a deployment, and we must not mint a CR
//     whose hotStandbyRegions[] would be empty, violating the CRD's
//     required field);
//   - the variant must declare a switchover mechanism (the Blueprint
//     author opting the app into orchestrated failover).
func buildContinuumPlan(
	appRef string,
	choice bpv1alpha1.BcpTopology,
	variant *bpv1alpha1.TopologyVariant,
	plan placement.Plan,
	ownerLabels map[string]string,
) (continuumPlan, bool) {
	if variant == nil {
		return continuumPlan{}, false
	}
	// Only the two single-primary DR topologies have a switchover to drive.
	if choice != bpv1alpha1.BcpActiveHotStandby && choice != bpv1alpha1.BcpActivePassive {
		return continuumPlan{}, false
	}
	// The Blueprint variant must opt the app into orchestrated failover.
	if variant.Switchover == nil || variant.Switchover.Mechanism == "" {
		return continuumPlan{}, false
	}

	primary := plan.PrimaryRegion
	standbys := standbyRegions(plan)
	// A DR contract needs both a primary AND ≥1 standby region. If the
	// hot-standby topology collapsed to one region on this Sovereign,
	// there is no second region to fail over to — skip (the #3698
	// integrity gate flags the deployment; we simply do not mint an
	// invalid CR with empty hotStandbyRegions[]).
	if primary == "" || len(standbys) == 0 {
		return continuumPlan{}, false
	}

	cp := continuumPlan{
		AppRef:         appRef,
		PrimaryRegion:  primary,
		StandbyRegions: standbys,
		Mechanism:      resolveSwitchoverMechanism(variant),
		OwnerLabels:    ownerLabels,
	}
	if variant.Switchover.RtoSeconds != nil {
		cp.RTOSeconds = *variant.Switchover.RtoSeconds
	}
	if variant.Switchover.RpoSeconds != nil {
		cp.RPOSeconds = *variant.Switchover.RpoSeconds
	}
	return cp, true
}

// standbyRegions returns the passive region names from the placement
// plan in plan order (priority order). A region is a standby when its
// Role is not the primary/active role (Standby flag OR Role==standby).
func standbyRegions(plan placement.Plan) []string {
	out := make([]string, 0, len(plan.Regions))
	for _, rp := range plan.Regions {
		if rp.Name == plan.PrimaryRegion {
			continue
		}
		if rp.Standby || rp.Role == placement.RoleStandby {
			out = append(out, rp.Name)
		}
	}
	return out
}

// resolveSwitchoverMechanism maps the Blueprint variant's declared
// switchover mechanism + replication backend onto the canonical
// continuum-controller mechanism enum (cnpg-pair | raft-transition |
// stateless). This is the SAME three-way the switchover sequencer
// dispatches through (core/controllers/continuum/internal/switchover/
// promoter.go Mechanism), resolved here so the produced CR carries the
// concrete mechanism rather than the Blueprint-authoring alias
// `bp-continuum`.
//
// Resolution (zero app-name literal — keyed only off the topology
// declaration):
//
//   - an explicit concrete mechanism (cnpg-pair | raft-transition |
//     stateless) on the variant passes through verbatim;
//   - the Blueprint-authoring alias `bp-continuum` (the convention in
//     docs/topology-matrix.md for "continuum drives it") resolves by the
//     replication backend: a cnpg-pair backend → cnpg-pair; a raft
//     backend → raft-transition; ANY OTHER backend, or no replication
//     block at all (the DNS-flip-only stateless app) → stateless;
//   - anything else (manual, bp-velero-restore — rows continuum does NOT
//     execute) falls through to stateless so the produced CR is at least
//     a valid, no-op-promotable contract rather than an unrecognised
//     mechanism the controller would reject.
func resolveSwitchoverMechanism(variant *bpv1alpha1.TopologyVariant) string {
	const (
		mechCNPGPair  = "cnpg-pair"
		mechRaft      = "raft-transition"
		mechStateless = "stateless"
	)
	if variant == nil || variant.Switchover == nil {
		return mechStateless
	}
	switch variant.Switchover.Mechanism {
	case mechCNPGPair, mechRaft, mechStateless:
		return variant.Switchover.Mechanism
	}
	// Alias / unknown — resolve by replication backend.
	if variant.Replication != nil {
		switch variant.Replication.Backend {
		case "cnpg-pair":
			return mechCNPGPair
		case "raft", "openbao-raft", "raft-transition":
			return mechRaft
		}
	}
	// No cross-region state store (or an unrecognised backend) — the
	// generic DNS-flip-only stateless contract the #3698 Promoter drives.
	return mechStateless
}

// renderContinuumCR builds the Continuum CR Unstructured from a resolved
// continuumPlan. Pure function — no client calls. The shape mirrors the
// CRD's required set (applicationRef, primaryRegion, hotStandbyRegions,
// leaseClient) + the optional switchover.mechanism the continuum-
// controller reads (parseSpec, spec.go). Status is NEVER seeded — the
// running continuum-controller owns status per ADR-0001 §2.7.
func renderContinuumCR(appName, namespace string, cp continuumPlan) *unstructured.Unstructured {
	cr := &unstructured.Unstructured{}
	cr.SetAPIVersion(ContinuumGroup + "/" + ContinuumVersion)
	cr.SetKind(ContinuumKind)
	cr.SetName(ContinuumNameFor(appName))
	cr.SetNamespace(namespace)

	labels := map[string]string{}
	for k, v := range cp.OwnerLabels {
		labels[k] = v
	}
	// Back-pointer to the owning Application (cascade-delete + rollup),
	// mirroring the fan-out HR's LabelApp contract.
	labels[topo.LabelApp] = appName
	// Scope marker — the cnpg-pair chart stamps openova.io/scope:platform
	// on its CR; per-app DR CRs are application-scope so an operator can
	// tell the two producers apart.
	labels["openova.io/scope"] = "application"
	cr.SetLabels(labels)

	hotStandby := make([]interface{}, 0, len(cp.StandbyRegions))
	for _, s := range cp.StandbyRegions {
		hotStandby = append(hotStandby, s)
	}

	spec := map[string]interface{}{
		"applicationRef":    cp.AppRef,
		"primaryRegion":     cp.PrimaryRegion,
		"hotStandbyRegions": hotStandby,
		"leaseClient": map[string]interface{}{
			"kind": DefaultContinuumLeaseKind,
		},
	}
	if cp.Mechanism != "" {
		spec["switchover"] = map[string]interface{}{
			"mechanism": cp.Mechanism,
		}
	}
	if cp.RTOSeconds > 0 {
		spec["rto"] = fmt.Sprintf("%ds", cp.RTOSeconds)
	}
	if cp.RPOSeconds > 0 {
		spec["rpo"] = fmt.Sprintf("%ds", cp.RPOSeconds)
	}
	cr.Object["spec"] = spec
	return cr
}

// ContinuumNameFor composes the per-app Continuum CR name (`dr-<app>`).
// Exported so handleDeletion + tests reference the same convention.
func ContinuumNameFor(app string) string {
	return ContinuumNamePrefix + app
}

// reconcileContinuumCR upserts the per-app Continuum CR for a DR-capable
// Application, idempotently. Called from the fan-out persist block once
// the per-cluster HelmReleases are written. Returns nil (no-op) when the
// app is not a DR contract (buildContinuumPlan gate). Errors are surfaced
// to the caller, which downgrades the Application to Degraded so the
// reconcile retries — a missing DR contract must not silently pass.
func (r *Reconciler) reconcileContinuumCR(
	ctx context.Context,
	appName, namespace string,
	choice bpv1alpha1.BcpTopology,
	variant *bpv1alpha1.TopologyVariant,
	plan placement.Plan,
	ownerLabels map[string]string,
) error {
	appRef := namespace + "/" + appName
	cp, ok := buildContinuumPlan(appRef, choice, variant, plan, ownerLabels)
	if !ok {
		// Not a DR contract (singleton / active-active / single-region /
		// no switchover mechanism). If a stale CR exists from a PRIOR
		// topology (operator downgraded active-hot-standby → singleton),
		// remove it so the roster reflects reality.
		return r.deleteContinuumCRIfExists(ctx, appName, namespace)
	}
	cr := renderContinuumCR(appName, namespace, cp)
	if err := r.upsertHostResource(ctx, ContinuumGVR, namespace, cr.GetName(), cr); err != nil {
		return fmt.Errorf("upsert Continuum CR %s/%s: %w", namespace, cr.GetName(), err)
	}
	r.Log.Info("upserted per-app Continuum CR",
		"namespace", namespace, "name", cr.GetName(),
		"app", appName, "topology", string(choice),
		"mechanism", cp.Mechanism,
		"primaryRegion", cp.PrimaryRegion,
		"hotStandbyRegions", cp.StandbyRegions,
	)
	return nil
}

// deleteContinuumCRIfExists removes the per-app Continuum CR if present.
// Used both on a topology downgrade (DR → non-DR) and on Application
// deletion (handleDeletion). NotFound is not an error.
func (r *Reconciler) deleteContinuumCRIfExists(ctx context.Context, appName, namespace string) error {
	name := ContinuumNameFor(appName)
	err := r.Dynamic.Resource(ContinuumGVR).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete Continuum CR %s/%s: %w", namespace, name, err)
	}
	return nil
}
