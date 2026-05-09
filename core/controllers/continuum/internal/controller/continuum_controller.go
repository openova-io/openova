// Package controller — continuum-controller reconciler.
//
// Reconciles `Continuum.dr.openova.io/v1` CRs into per-Application DR
// orchestration: lease maintenance, replication-health watching, and
// switchover sequence (drain HTTP traffic via HTTPRoute weight=0,
// flip lua-record probe target via PDM /v1/commit, flip CNPG primary,
// publish audit event on NATS `catalyst.audit`).
//
// K-Cont-1 (this slice) ships the SKELETON ONLY. Reconcile() is a
// no-op that logs at V(1). The full reconcile loop — lease state
// machine, switchover sequencer, lua-record body synthesizer — lands
// in K-Cont-2 (#1101). The lease-witness client (Cloudflare KV +
// 3-DNS quorum fallback) lands in K-Cont-3.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #3 + ADR-0001 §2.7 (matching the
// pattern of all 5 Catalyst controllers + useraccess-controller) the
// CRD shape is owned by the chart and authored by hand. We do NOT
// generate Go types via controller-gen. The dynamic client +
// `unstructured.Unstructured` suffices for read/write needs and
// avoids a code-generation step in the build pipeline.
//
// Wire shape mirrors the existing CRD at
//
//	products/catalyst/chart/crds/continuum.yaml
//
// The Continuum CR is namespaced (per the CRD: scope=Namespaced) and
// the reconciler watches every namespace. Per-Continuum-CR state
// (lease holder, last-renew time, switchover in-flight) lives in
// memory in K-Cont-2; on controller restart the lease is re-acquired
// from the witness if quorum says we still hold it, else relinquished.
package controller

import (
	"context"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Continuum GVK / GVR — pinned to the CRD shipped by
// products/catalyst/chart/crds/continuum.yaml. A drift here vs the
// chart YAML is a packaging bug and surfaces as a 404 on the dynamic
// client at startup.
const (
	ContinuumGroup    = "dr.openova.io"
	ContinuumVersion  = "v1"
	ContinuumKind     = "Continuum"
	ContinuumListKind = "ContinuumList"
	ContinuumResource = "continuums"
)

// ContinuumGVR is consumed by the dynamic client + by tests building
// fake clients.
var ContinuumGVR = schema.GroupVersionResource{
	Group:    ContinuumGroup,
	Version:  ContinuumVersion,
	Resource: ContinuumResource,
}

// continuumGVK is the corresponding GroupVersionKind, used to set up
// the controller-runtime watch via unstructured.Unstructured.
var continuumGVK = schema.GroupVersionKind{
	Group:   ContinuumGroup,
	Version: ContinuumVersion,
	Kind:    ContinuumKind,
}

// ContinuumReconciler is the no-op skeleton for K-Cont-1. K-Cont-2
// extends this with:
//   - PDMClient: pool-domain-manager /v1/commit client
//   - NATSPublisher: catalyst.audit event publisher
//   - WitnessClient: lease witness (cloudflare-kv | dns-quorum) — wired
//     in K-Cont-3
//   - per-Continuum-CR goroutine map keyed by NamespacedName
type ContinuumReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// Reconcile is a no-op for K-Cont-1. K-Cont-2 ships the actual
// reconcile loop:
//
//  1. Fetch the Continuum CR (namespaced). Not-found → cancel any
//     running per-CR goroutine + relinquish lease.
//  2. Validate the referenced Application has placement:
//     active-hotstandby. Otherwise → status.phase=Failed.
//  3. Validate primaryRegion ∈ Application.spec.regions[].
//  4. Start (or update) the per-CR goroutine that:
//     - acquires the lease via WitnessClient
//     - watches CNPG `cnpg.io/cluster.replicationLag` per replica
//     - on autoFailover trigger or operator-initiated switchover,
//     executes the §9.3 sequence
//  5. Patch status (phase, primaryRegion, leaseHolder, leaseExpiresAt,
//     replicationLag map, conditions).
func (r *ContinuumReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(1).Info("continuum reconcile (skeleton K-Cont-1 — no-op)",
		"namespace", req.Namespace,
		"name", req.Name)
	return ctrl.Result{}, nil
}

// SetupWithManager wires the reconciler into the controller-runtime
// Manager. K-Cont-1 watches Continuum CRs only — K-Cont-2 will add
// `Watches(&unstructured.Unstructured{...Application}, ...)` for the
// referenced active-hotstandby Application CRs (with EnqueueRequestsFromMapFunc
// translating Application name → owning Continuum CR).
//
// The watch uses unstructured.Unstructured per ADR-0001 §2.7 — no
// generated types, no controller-gen. The Manager's caching layer
// works fine with unstructured.
func (r *ContinuumReconciler) SetupWithManager(mgr ctrl.Manager) error {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(continuumGVK)

	return ctrl.NewControllerManagedBy(mgr).
		Named("continuum").
		For(u).
		Complete(r)
}
