// manage.go — the reader half of the lightweight ArgoCD-like reconciler
// MANAGEMENT surface (issue #3996). Where reconcilers.go observes the
// non-install reconciler activities for the Jobs canvas, this file lists
// the FULL continuous-reconciler set the operator manages from the Cloud
// view's Reconciliation lens — every Flux reconciler object (HelmRelease,
// Kustomization, and the four Flux *source* kinds) plus the declarative
// Catalyst CRs — with the rich per-object status the management UI needs:
// the live reconcile state, the last-reconcile timestamp, the Ready
// condition message, the applied source revision, and the suspended flag.
//
// It is deliberately GENERIC + read-only: it lists a fixed set of
// reconciler GVRs via the SAME dynamic.Interface the rest of helmwatch
// uses and emits one typed ManagedReconciler per object. The handler
// (internal/handler/reconcilers.go) maps each onto the wire shape and
// gates the mutating actions (reconcile / suspend / resume) behind the
// same owner-check + operator RBAC the jobs-retry endpoint uses.
package helmwatch

import (
	"context"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// Flux *source* GVRs (source-controller). These carry no spec.dependsOn
// DAG but ARE first-class Flux reconcilers the operator manages — a stuck
// GitRepository / OCIRepository / HelmRepository / HelmChart is the most
// common reason a downstream HelmRelease never reconciles, so the
// management surface must list + drive them too (issue #3996).
var (
	// GitRepositoryGVR — Flux GitRepository (source-controller).
	GitRepositoryGVR = schema.GroupVersionResource{
		Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories",
	}
	// OCIRepositoryGVR — Flux OCIRepository (source-controller).
	OCIRepositoryGVR = schema.GroupVersionResource{
		Group: "source.toolkit.fluxcd.io", Version: "v1beta2", Resource: "ocirepositories",
	}
	// HelmRepositoryGVR — Flux HelmRepository (source-controller).
	HelmRepositoryGVR = schema.GroupVersionResource{
		Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "helmrepositories",
	}
	// HelmChartGVR — Flux HelmChart (source-controller).
	HelmChartGVR = schema.GroupVersionResource{
		Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "helmcharts",
	}
)

// FluxReconcilerKind — the wire `kind` the management surface uses to
// address an object in the action/log routes. These match the K8s Kind
// 1:1 (PascalCase) so the FE renders them verbatim and the handler maps
// kind→GVR + kind→controller deterministically.
const (
	ManageKindHelmRelease    = "HelmRelease"
	ManageKindKustomization  = "Kustomization"
	ManageKindGitRepository  = "GitRepository"
	ManageKindOCIRepository  = "OCIRepository"
	ManageKindHelmRepository = "HelmRepository"
	ManageKindHelmChart      = "HelmChart"
)

// managedGVRByKind maps each manageable Flux kind onto its GVR. The
// handler reuses this for the reconcile/suspend/resume action routes so
// there is ONE source of truth for kind→GVR. The declarative Catalyst CRs
// (Application/Environment/Organization/Continuum) are intentionally NOT
// here — they are reconciled by the Catalyst controllers, not Flux, and
// carry no spec.suspend, so they are list-only on this surface.
var managedGVRByKind = map[string]schema.GroupVersionResource{
	ManageKindHelmRelease:    HelmReleaseGVR,
	ManageKindKustomization:  KustomizationGVR,
	ManageKindGitRepository:  GitRepositoryGVR,
	ManageKindOCIRepository:  OCIRepositoryGVR,
	ManageKindHelmRepository: HelmRepositoryGVR,
	ManageKindHelmChart:      HelmChartGVR,
}

// ManagedGVRForKind returns the GVR for a manageable Flux kind + whether
// the kind is recognised. The handler uses it to reject an unknown kind
// with 400 before touching the cluster.
func ManagedGVRForKind(kind string) (schema.GroupVersionResource, bool) {
	gvr, ok := managedGVRByKind[strings.TrimSpace(kind)]
	return gvr, ok
}

// ManagedReconciler is one row in the reconciler-management list — the
// rich per-object status the lightweight ArgoCD-like UI renders.
type ManagedReconciler struct {
	// Kind — the K8s Kind (HelmRelease / Kustomization / GitRepository /
	// OCIRepository / HelmRepository / HelmChart). Addresses the object in
	// the action + log routes.
	Kind string `json:"kind"`
	// Name / Namespace — the object's coordinates.
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	// State — the Reconciliation vocabulary (Reconciled / Reconciling /
	// Degraded / Suspended). NEVER Success/Failed (these are continuous
	// reconcilers). Suspended wins over every other state.
	State string `json:"state"`
	// Message — the Flux Ready condition message (the "why" one-liner).
	Message string `json:"message,omitempty"`
	// Revision — the applied/attempted source revision (chart version,
	// git SHA, OCI digest), when the object reports one.
	Revision string `json:"revision,omitempty"`
	// Suspended — true when spec.suspend is set. The FE renders Resume in
	// place of Suspend + a muted row.
	Suspended bool `json:"suspended"`
	// LastReconcile — the Ready condition lastTransitionTime (RFC3339), or
	// empty when the object has never reported Ready.
	LastReconcile string `json:"lastReconcile,omitempty"`
	// Controller — the Flux controller that owns this kind
	// (helm-controller / kustomize-controller / source-controller). The FE
	// shows it; the log route filters that controller's logs to this object.
	Controller string `json:"controller"`
}

// Management state vocabulary (wire contract — matches reconciliation_dag.go
// so the FE renders one set of labels). Exported so the handler counts the
// Reconciled rows against the same constant the reader emits.
const (
	ManageStateReconciled  = "Reconciled"
	ManageStateReconciling = "Reconciling"
	ManageStateDegraded    = "Degraded"
	ManageStateSuspended   = "Suspended"
)

// controllerForKind maps a Flux kind onto the controller whose logs carry
// that object's reconcile output. helm-controller for HelmReleases,
// kustomize-controller for Kustomizations, source-controller for the four
// source kinds. The log route tails this controller filtered to the object.
func controllerForKind(kind string) string {
	switch kind {
	case ManageKindHelmRelease:
		return "helm-controller"
	case ManageKindKustomization:
		return "kustomize-controller"
	case ManageKindGitRepository, ManageKindOCIRepository,
		ManageKindHelmRepository, ManageKindHelmChart:
		return "source-controller"
	default:
		return ""
	}
}

// ControllerForKind is the exported form the handler uses to resolve the
// controller (and its pod selector) for the log route.
func ControllerForKind(kind string) string { return controllerForKind(kind) }

// manageStateForReady maps a Flux object's Ready condition + suspended flag
// onto the management vocabulary. Suspended always wins. Otherwise it
// reuses the same anti-flap rule the DAG uses: Ready=True → Reconciled,
// Ready=False+Stalled → Degraded, everything else → Reconciling.
func manageStateForReady(conds []metav1.Condition, suspended bool) string {
	if suspended {
		return ManageStateSuspended
	}
	switch statusFromReadyCondition(conds) {
	case ObsStatusSucceeded:
		return ManageStateReconciled
	case ObsStatusFailed:
		return ManageStateDegraded
	default:
		return ManageStateReconciling
	}
}

// revisionFromObject extracts the most meaningful applied/attempted source
// revision a Flux object reports, trying the per-kind status fields in
// priority order. Empty when none is present (e.g. an object that has never
// reconciled). The order is: lastAppliedRevision (HelmRelease/Kustomization)
// → lastAttemptedRevision (HelmRelease in-flight) → status.artifact.revision
// (the source kinds).
func revisionFromObject(u *unstructured.Unstructured) string {
	for _, path := range [][]string{
		{"status", "lastAppliedRevision"},
		{"status", "lastAttemptedRevision"},
		{"status", "artifact", "revision"},
		{"status", "history", "0", "chartVersion"}, // best-effort HR chart version
	} {
		if v, ok, _ := unstructured.NestedString(u.Object, path...); ok {
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		}
	}
	return ""
}

// suspendedFromObject reads spec.suspend (default false). All the manageable
// Flux kinds carry this field.
func suspendedFromObject(u *unstructured.Unstructured) bool {
	v, _, _ := unstructured.NestedBool(u.Object, "spec", "suspend")
	return v
}

// ListManagedReconcilers performs a ONE-SHOT list of every manageable Flux
// reconciler kind via the supplied dynamic client and returns the rich
// management rows. Best-effort per GVR: a List error on one kind (e.g. the
// OCIRepository CRD absent on a cluster that uses none) is swallowed so the
// other kinds still surface. Returns an empty slice (never nil). The result
// is sorted by (kind, namespace, name) for a stable render + test.
func ListManagedReconcilers(ctx context.Context, dyn dynamic.Interface) ([]ManagedReconciler, error) {
	if dyn == nil {
		return []ManagedReconciler{}, nil
	}
	out := make([]ManagedReconciler, 0, 64)

	// Stable kind order so the list reads + tests deterministically.
	kinds := []string{
		ManageKindHelmRelease,
		ManageKindKustomization,
		ManageKindGitRepository,
		ManageKindOCIRepository,
		ManageKindHelmRepository,
		ManageKindHelmChart,
	}
	for _, kind := range kinds {
		gvr := managedGVRByKind[kind]
		list, err := dyn.Resource(gvr).Namespace("").List(ctx, metav1.ListOptions{})
		if err != nil {
			// CRD absent / RBAC-denied for this kind — skip, keep the rest.
			continue
		}
		for i := range list.Items {
			u := &list.Items[i]
			conds, _ := extractConditions(u)
			suspended := suspendedFromObject(u)
			out = append(out, ManagedReconciler{
				Kind:          kind,
				Name:          u.GetName(),
				Namespace:     u.GetNamespace(),
				State:         manageStateForReady(conds, suspended),
				Message:       messageFromReadyCondition(conds),
				Revision:      revisionFromObject(u),
				Suspended:     suspended,
				LastReconcile: formatReconcileTime(readyTransitionTime(conds)),
				Controller:    controllerForKind(kind),
			})
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// formatReconcileTime renders a reconcile timestamp as RFC3339, or "" when
// zero (never reconciled).
func formatReconcileTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
