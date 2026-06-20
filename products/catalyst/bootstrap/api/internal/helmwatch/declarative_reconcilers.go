// declarative_reconcilers.go — the one-shot reader that observes the
// NON-Flux *declarative reconcilers* the Reconciliation page (#3925 surface
// B / #3958) must surface as nodes alongside the Flux HelmReleases +
// Kustomizations.
//
// # Why this is a SECOND reader (not folded into reconcilers.go)
//
// reconcilers.go observes the Flux/batch ingestion surface that feeds the
// Jobs canvas: Kustomizations, CronJobs, Jobs, marked Deployments — every
// object carries a Flux-shaped lifecycle the jobs bridge maps onto a leaf.
// This reader observes a different family: the continuous DECLARATIVE
// reconcilers whose desired-state controllers run OUTSIDE Flux —
// cert-manager Certificates, CNPG Clusters, External-Secrets
// ExternalSecrets, and the Catalyst control-plane CRs (Application,
// Environment, Organization, Continuum). They reconcile a CR toward a
// desired state exactly like a HelmRelease does, but they carry NO Flux
// `spec.dependsOn`, so on the Reconciliation DAG they render as edgeless
// nodes — present, coloured by their own Ready/phase status, but with no
// dependency edges (the operator widens the FE's default Flux filter to
// All / a class to see them).
//
// The reader is deliberately GENERIC over a fixed GVR table: a List error
// on any one kind (the CRD absent on a cluster that does not ship that
// component) is swallowed so the others still surface. It reuses the same
// Ready-condition helpers reconcilers.go uses (statusFromReadyCondition /
// messageFromReadyCondition) and the same Obs* lifecycle constants, keeping
// the ReconState vocabulary mapping in the handler package (helmwatch must
// NOT import internal/handler — import cycle).
package helmwatch

import (
	"context"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// Declarative-reconciler GVRs. Each is a continuous desired-state
// controller that lives OUTSIDE Flux and carries no spec.dependsOn.
var (
	// CertificateGVR — cert-manager Certificate. Ready condition drives the
	// node status (Ready=True ⇒ Reconciled).
	CertificateGVR = schema.GroupVersionResource{
		Group: "cert-manager.io", Version: "v1", Resource: "certificates",
	}
	// CNPGClusterGVR — CloudNativePG Cluster. Has NO standard Ready
	// condition; status is read from status.phase (see cnpgStatusFromPhase).
	CNPGClusterGVR = schema.GroupVersionResource{
		Group: "postgresql.cnpg.io", Version: "v1", Resource: "clusters",
	}
	// ExternalSecretGVR — External-Secrets ExternalSecret. v1 is the
	// current served version; v1beta1 is the fallback tried when the v1
	// List errors (older ESO releases serve only v1beta1).
	ExternalSecretGVR = schema.GroupVersionResource{
		Group: "external-secrets.io", Version: "v1", Resource: "externalsecrets",
	}
	ExternalSecretGVRBeta = schema.GroupVersionResource{
		Group: "external-secrets.io", Version: "v1beta1", Resource: "externalsecrets",
	}

	// Catalyst control-plane CRs — the desired-state objects the Catalyst
	// controllers reconcile. GVRs taken verbatim from k8scache/kinds.go.
	ApplicationGVR = schema.GroupVersionResource{
		Group: "apps.openova.io", Version: "v1", Resource: "applications",
	}
	EnvironmentGVR = schema.GroupVersionResource{
		Group: "catalyst.openova.io", Version: "v1", Resource: "environments",
	}
	OrganizationGVR = schema.GroupVersionResource{
		Group: "orgs.openova.io", Version: "v1", Resource: "organizations",
	}
	ContinuumGVR = schema.GroupVersionResource{
		Group: "dr.openova.io", Version: "v1", Resource: "continuums",
	}
)

// DeclarativeReconciler is one observed non-Flux declarative reconciler
// object. The handler maps Status (an Obs* lifecycle constant) onto the
// ReconState vocabulary and emits one edgeless ReconciliationNode per
// observation.
type DeclarativeReconciler struct {
	// Kind — the human node label: "Certificate" | "Cluster" |
	// "ExternalSecret" | "Application" | "Environment" | "Organization" |
	// "Continuum".
	Kind string
	// Name — the object's metadata.name (the node label).
	Name string
	// Namespace — the object's namespace ("" for cluster-scoped kinds like
	// Organization). Part of the stable node ID.
	Namespace string
	// Status — one of the Obs* lifecycle constants (ObsStatusSucceeded /
	// ObsStatusRunning / ObsStatusPending / ObsStatusFailed). The handler
	// maps this onto the ReconState vocabulary.
	Status string
	// Message — a short human line (Ready-condition message, or the CNPG
	// phase text). Becomes the node Message.
	Message string
	// ObservedAt — the most relevant transition timestamp (Ready
	// lastTransitionTime); zero when unknown.
	ObservedAt time.Time
}

// declarativeGVR pairs a GVR with the human Kind label its observations
// carry. The CNPG entry is flagged so its status is read from status.phase
// rather than a Ready condition.
type declarativeGVR struct {
	gvr      schema.GroupVersionResource
	kind     string
	fromCNPG bool // status from status.phase, not a Ready condition
	// fallbackGVR is tried when the primary GVR's List errors (only used
	// for ExternalSecret v1 → v1beta1). Zero value ⇒ no fallback.
	fallbackGVR schema.GroupVersionResource
}

// declarativeReconcilerGVRs is the fixed, stably-ordered table the reader
// enumerates. The order is the order the nodes are appended in (and is kept
// deterministic for tests): cert-manager → CNPG → ESO → Catalyst CRs.
var declarativeReconcilerGVRs = []declarativeGVR{
	{gvr: CertificateGVR, kind: "Certificate"},
	{gvr: CNPGClusterGVR, kind: "Cluster", fromCNPG: true},
	{gvr: ExternalSecretGVR, kind: "ExternalSecret", fallbackGVR: ExternalSecretGVRBeta},
	{gvr: ApplicationGVR, kind: "Application"},
	{gvr: EnvironmentGVR, kind: "Environment"},
	{gvr: OrganizationGVR, kind: "Organization"},
	{gvr: ContinuumGVR, kind: "Continuum"},
}

// ListDeclarativeReconcilers performs a ONE-SHOT list of every declarative
// reconciler GVR via the supplied dynamic client and returns the typed
// observations. It never spins up an informer — it is the surface-B
// counterpart of ListReconcilerObservations.
//
// Best-effort per GVR: a List error on one kind (the CRD absent on a
// cluster that does not ship that component) is swallowed so the other
// kinds still surface. For ExternalSecret the v1 List error triggers a
// v1beta1 retry before giving up. Returns an empty slice (never nil) when
// nothing is found. The slice is in the fixed declarativeReconcilerGVRs
// order so callers/tests see a deterministic sequence.
func ListDeclarativeReconcilers(ctx context.Context, dyn dynamic.Interface) ([]DeclarativeReconciler, error) {
	if dyn == nil {
		return []DeclarativeReconciler{}, nil
	}
	out := make([]DeclarativeReconciler, 0, 32)

	for _, d := range declarativeReconcilerGVRs {
		list, err := dyn.Resource(d.gvr).Namespace("").List(ctx, metav1.ListOptions{})
		if err != nil && d.fallbackGVR.Resource != "" {
			// ExternalSecret v1 absent → retry the v1beta1 served version.
			list, err = dyn.Resource(d.fallbackGVR).Namespace("").List(ctx, metav1.ListOptions{})
		}
		if err != nil {
			// CRD absent / not served on this cluster — swallow so the
			// remaining kinds still surface (best-effort per GVR).
			continue
		}
		for i := range list.Items {
			u := &list.Items[i]
			var status, message string
			if d.fromCNPG {
				status, message = cnpgStatusFromPhase(u)
			} else {
				conds, _ := extractConditions(u)
				status = statusFromReadyCondition(conds)
				message = messageFromReadyCondition(conds)
			}
			out = append(out, DeclarativeReconciler{
				Kind:       d.kind,
				Name:       u.GetName(),
				Namespace:  u.GetNamespace(),
				Status:     status,
				Message:    message,
				ObservedAt: declarativeObservedAt(u, d.fromCNPG),
			})
		}
	}

	return out, nil
}

// cnpgStatusFromPhase maps a CNPG Cluster's status.phase (it has NO
// standard Ready condition) onto the Obs* lifecycle:
//
//   - phase contains "healthy" (e.g. "Cluster in healthy state") ⇒ Succeeded
//   - phase contains "Failed" / "Unrecoverable"                  ⇒ Failed
//   - empty / any other phase (creating, switchover, …)          ⇒ Running
//
// The verbatim phase text is carried as the Message so the operator sees
// CNPG's own wording.
func cnpgStatusFromPhase(u *unstructured.Unstructured) (status, message string) {
	phase, _, _ := unstructured.NestedString(u.Object, "status", "phase")
	trimmed := strings.TrimSpace(phase)
	lower := strings.ToLower(trimmed)
	switch {
	case strings.Contains(lower, "healthy"):
		status = ObsStatusSucceeded
	case strings.Contains(lower, "failed"), strings.Contains(lower, "unrecoverable"):
		status = ObsStatusFailed
	default:
		status = ObsStatusRunning
	}
	if trimmed == "" {
		return status, "no phase reported yet"
	}
	return status, trimmed
}

// declarativeObservedAt returns the most relevant transition timestamp: the
// Ready condition's lastTransitionTime for condition-bearing kinds; zero for
// CNPG (status.phase carries no transition time).
func declarativeObservedAt(u *unstructured.Unstructured, fromCNPG bool) time.Time {
	if fromCNPG {
		return time.Time{}
	}
	conds, _ := extractConditions(u)
	return readyTransitionTime(conds)
}
