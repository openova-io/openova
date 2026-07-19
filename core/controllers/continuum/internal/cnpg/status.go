// Package cnpg reads + patches CNPG Cluster CRs (postgresql.cnpg.io/v1).
//
// The continuum-controller uses CNPG Cluster CRs as the source of
// truth for replication health (status.currentPrimary,
// status.replication.lag) and as the seam for primary-flip during
// switchover (annotation `cnpg.io/cluster.primary` + the replica's
// `spec.replica.enabled` flag).
//
// Per ADR-0001 §2.7 we use the dynamic client + Unstructured rather
// than typed CNPG bindings — the brief explicitly forbids depending
// on a generated CNPG client.
package cnpg

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

// GVR for CNPG Cluster CRs. Cluster is namespaced.
var ClusterGVR = schema.GroupVersionResource{
	Group:    "postgresql.cnpg.io",
	Version:  "v1",
	Resource: "clusters",
}

// PrimaryAnnotation is the annotation the CNPG operator inspects for
// requested-primary changes. The operator reconciles toward the
// annotated value within the same Cluster (intra-cluster failover);
// the cross-cluster cluster-pair switchover is driven by Continuum
// flipping `replica.enabled` on the two CR halves.
const PrimaryAnnotation = "cnpg.io/cluster.primary"

// PairLabel — every cluster-pair half carries this label naming the
// pair. Used by Continuum to fan its single Continuum CR out to the
// two Cluster CRs. C-DB-1 sets it from the chart's fullname.
const PairLabel = "catalyst.openova.io/cnpg-pair"

// PairRoleLabel + values — `primary` | `replica`. Set by C-DB-1's
// chart at install time.
const (
	PairRoleLabel = "openova.io/cnpg-role"
	RolePrimary   = "primary"
	RoleReplica   = "replica"
)

// Status is the parsed view of CNPG Cluster.status that Continuum
// uses for switchover decisions.
type Status struct {
	// CurrentPrimary is the name of the Pod inside this Cluster CR
	// that's currently serving writes. Used to spot intra-Cluster
	// failover (a different Pod within the same CR became primary),
	// distinct from cross-Cluster failover that Continuum drives.
	CurrentPrimary string

	// TargetPrimary is the name of the Pod that the operator is
	// promoting to primary (transient — non-empty during
	// intra-Cluster failover).
	TargetPrimary string

	// IsReplicaCluster reports `spec.replica.enabled` — true means
	// this Cluster CR is the standby half of a cluster-pair.
	IsReplicaCluster bool

	// LagSeconds is the observed replication lag in seconds. Zero on
	// the primary Cluster (no upstream). Sourced from
	// status.timelineID + last-archived/applied-WAL deltas exposed
	// via status.replication summary, or 0 when fields absent.
	LagSeconds int

	// Phase is the operator's terse status phase (Cluster in healthy
	// steady state == "Cluster in healthy state").
	Phase string

	// Ready reports whether the Cluster is operationally Ready as
	// surfaced by the conditions array (type=Ready, status=True).
	Ready bool

	// ReadyInstances is status.readyInstances — the count of the
	// Cluster's Pods currently Ready. CNPG drops it to 0 when every
	// instance is down (the #4901 region-kill state: nodes cordoned,
	// replica Pods deleted). HasReadyInstances reports whether the
	// field was present at all — an absent field means the Cluster
	// status is too old/partial to judge on this axis.
	ReadyInstances    int
	HasReadyInstances bool

	// TimelineID is status.timelineID — the PostgreSQL timeline this
	// Cluster is currently on. CNPG bumps it on every promotion
	// (intra-Cluster failover, or a replica-cluster leaving recovery).
	// Used by #5125 Defect-2 divergence detection (DetectDivergence):
	// a standby (spec.replica.enabled=true) reporting a TimelineID
	// STRICTLY GREATER than the CR currently acting as primary means
	// its own recovery history has advanced past what the primary can
	// serve — the structured form of the CNPG walreceiver crash-loop
	// "highest timeline N of the primary is behind recovery timeline
	// N+1". Zero/absent (HasTimelineID=false) means the field hasn't
	// been populated yet (fresh CR, or a CNPG version/phase that
	// hasn't surfaced it) — callers must never guess on absent data.
	TimelineID    int
	HasTimelineID bool
}

// StandbyAvailable reports whether the replica (standby) half of a
// cnpg cluster-pair is genuinely serving as a hot-standby: its Cluster
// reports Ready=True AND (when the field is present) at least one
// instance is Ready (#4901).
//
// A Ready-but-LAGGING standby is still AVAILABLE — replication lag is
// a separate axis (status.replicationLagSeconds) and must NEVER raise
// a standby-absent alarm. Only an unreachable / down standby (not
// Ready, or zero ready instances) counts as absent.
func StandbyAvailable(replica Status) bool {
	if !replica.Ready {
		return false
	}
	if replica.HasReadyInstances {
		return replica.ReadyInstances >= 1
	}
	return true
}

// StandbyObservation is one reconcile pass's determination of the
// required synchronous hot-standby's availability (#4901). Known=false
// means no determination could be made this pass (the CR names no
// cnpg pair — dr-spine/raft continuums — or the replica half was not
// resolvable, e.g. provisioning in flight); the controller then leaves
// any prior StandbyAvailable condition untouched instead of
// false-alarming.
//
// Gone distinguishes the two unavailable postures (#4901 follow-up):
// false = the replica Cluster CR is PRESENT but not serving (not
// Ready / zero ready instances — "standby temporarily unreachable");
// true = a replica Cluster CR that WAS previously resolvable is no
// longer resolvable at all (cluster deleted / region wiped — "standby
// cluster gone"). Only meaningful when Known && !Available.
type StandbyObservation struct {
	Known          bool
	Available      bool
	Gone           bool
	ReplicaCluster string
}

// Reader reads + writes CNPG Cluster CRs via the dynamic client.
type Reader struct {
	Dyn dynamic.Interface
}

// NewReader returns a Reader. Tests inject a fake.NewSimpleDynamicClient.
func NewReader(dyn dynamic.Interface) *Reader {
	return &Reader{Dyn: dyn}
}

// Get returns the parsed Status of a CNPG Cluster CR.
func (r *Reader) Get(ctx context.Context, namespace, name string) (Status, *unstructured.Unstructured, error) {
	if r == nil || r.Dyn == nil {
		return Status{}, nil, errors.New("cnpg: nil Reader")
	}
	cr, err := r.Dyn.Resource(ClusterGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return Status{}, nil, fmt.Errorf("cnpg: get cluster %s/%s: %w", namespace, name, err)
	}
	return parseStatus(cr), cr, nil
}

// FindPair returns the (primary-Cluster, replica-Cluster)
// Unstructured pair for a given pair name. Lookup uses the canonical
// labels:
//
//	catalyst.openova.io/cnpg-pair = <pair name>
//	openova.io/cnpg-role          = primary | replica
//
// The C-DB-1 blueprint stamps both labels at install time so this
// lookup is byte-stable.
func (r *Reader) FindPair(ctx context.Context, namespace, pairName string) (primary, replica *unstructured.Unstructured, err error) {
	if r == nil || r.Dyn == nil {
		return nil, nil, errors.New("cnpg: nil Reader")
	}
	list, err := r.Dyn.Resource(ClusterGVR).Namespace(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", PairLabel, pairName),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("cnpg: list pair %q: %w", pairName, err)
	}
	for i := range list.Items {
		role, _, _ := unstructured.NestedString(list.Items[i].Object, "metadata", "labels", PairRoleLabel)
		switch role {
		case RolePrimary:
			primary = &list.Items[i]
		case RoleReplica:
			replica = &list.Items[i]
		}
	}
	if primary == nil || replica == nil {
		return primary, replica, fmt.Errorf("cnpg: pair %q incomplete (primary=%v replica=%v)", pairName, primary != nil, replica != nil)
	}
	return primary, replica, nil
}

// SetPrimaryAnnotation sets `cnpg.io/cluster.primary` on a Cluster CR
// to `value`. Used in the switchover sequencer's cordon/uncordon
// steps.
//
// The CNPG operator picks this up and triggers an intra-Cluster
// promotion. For cross-Cluster (cluster-pair) switchover the
// sequencer ALSO flips spec.replica.enabled via SetReplicaEnabled
// on both halves.
func (r *Reader) SetPrimaryAnnotation(ctx context.Context, namespace, name, value string) error {
	if r == nil || r.Dyn == nil {
		return errors.New("cnpg: nil Reader")
	}
	cr, err := r.Dyn.Resource(ClusterGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("cnpg: get for annotate: %w", err)
	}
	annotations := cr.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[PrimaryAnnotation] = value
	cr.SetAnnotations(annotations)
	if _, err := r.Dyn.Resource(ClusterGVR).Namespace(namespace).Update(ctx, cr, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("cnpg: update for annotate: %w", err)
	}
	return nil
}

// ClearPrimaryAnnotation removes the cordon annotation. Idempotent.
func (r *Reader) ClearPrimaryAnnotation(ctx context.Context, namespace, name string) error {
	if r == nil || r.Dyn == nil {
		return errors.New("cnpg: nil Reader")
	}
	cr, err := r.Dyn.Resource(ClusterGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("cnpg: get for clear: %w", err)
	}
	annotations := cr.GetAnnotations()
	if annotations == nil {
		return nil
	}
	if _, ok := annotations[PrimaryAnnotation]; !ok {
		return nil
	}
	delete(annotations, PrimaryAnnotation)
	cr.SetAnnotations(annotations)
	if _, err := r.Dyn.Resource(ClusterGVR).Namespace(namespace).Update(ctx, cr, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("cnpg: update for clear: %w", err)
	}
	return nil
}

// SetReplicaEnabled patches spec.replica.enabled on a Cluster CR.
// During switchover from FSN-primary to HEL-primary the sequencer:
//   - sets enabled=true on the FSN Cluster (it becomes the new replica)
//   - sets enabled=false on the HEL Cluster (it becomes the new primary)
func (r *Reader) SetReplicaEnabled(ctx context.Context, namespace, name string, enabled bool) error {
	if r == nil || r.Dyn == nil {
		return errors.New("cnpg: nil Reader")
	}
	cr, err := r.Dyn.Resource(ClusterGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("cnpg: get for replica flag: %w", err)
	}
	if err := unstructured.SetNestedField(cr.Object, enabled, "spec", "replica", "enabled"); err != nil {
		return fmt.Errorf("cnpg: set replica.enabled: %w", err)
	}
	if _, err := r.Dyn.Resource(ClusterGVR).Namespace(namespace).Update(ctx, cr, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("cnpg: update for replica flag: %w", err)
	}
	return nil
}

// RecloneCluster deletes then re-creates a CNPG Cluster CR from its own
// captured spec/labels/annotations, forcing the CNPG operator to
// re-bootstrap it from scratch (pg_basebackup) instead of trying to
// resume streaming replication.
//
// This is the structured form of the #5125 Defect-2 proven manual heal
// for a walreceiver stuck in timeline-divergence ("highest timeline N
// of the primary is behind recovery timeline N+1"): "delete replica
// Cluster CR + re-apply". Only spec + labels + annotations survive;
// server-set metadata (resourceVersion, uid, creationTimestamp,
// generation, managedFields, status) is stripped so the Create call is
// accepted as a genuinely fresh object — CNPG then re-runs its normal
// bootstrap reconcile, which re-clones via pg_basebackup.
//
// Idempotent: if the CR is already absent (e.g. a prior attempt's
// Delete succeeded but the process crashed before Create), the missing
// Get simply errors and the caller (Sequencer.CheckRejoin) surfaces it
// as this attempt's failure — bounded-attempts + backoff (rejoin.go)
// retries on the next cooldown window rather than looping tightly.
func (r *Reader) RecloneCluster(ctx context.Context, namespace, name string) error {
	if r == nil || r.Dyn == nil {
		return errors.New("cnpg: nil Reader")
	}
	cr, err := r.Dyn.Resource(ClusterGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("cnpg: get for reclone: %w", err)
	}

	fresh := &unstructured.Unstructured{Object: map[string]interface{}{}}
	fresh.SetGroupVersionKind(cr.GroupVersionKind())
	fresh.SetName(cr.GetName())
	fresh.SetNamespace(cr.GetNamespace())
	fresh.SetLabels(cr.GetLabels())
	fresh.SetAnnotations(cr.GetAnnotations())
	if spec, found, _ := unstructured.NestedMap(cr.Object, "spec"); found {
		if err := unstructured.SetNestedMap(fresh.Object, spec, "spec"); err != nil {
			return fmt.Errorf("cnpg: reclone: copy spec: %w", err)
		}
	}

	if err := r.Dyn.Resource(ClusterGVR).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("cnpg: delete for reclone: %w", err)
	}
	if _, err := r.Dyn.Resource(ClusterGVR).Namespace(namespace).Create(ctx, fresh, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("cnpg: recreate for reclone: %w", err)
	}
	return nil
}

// MaxLagSeconds returns the maximum lag observed across the listed
// Clusters' Status. Clusters that aren't replicas (lag=0) are skipped
// per the CNPG semantics.
func MaxLagSeconds(statuses ...Status) int {
	max := 0
	for _, s := range statuses {
		if s.LagSeconds > max {
			max = s.LagSeconds
		}
	}
	return max
}

// parseStatus extracts the subset of fields Continuum uses from a
// CNPG Cluster CR. Tolerant of missing fields — empty Status is
// returned for Cluster CRs without a status block.
func parseStatus(cr *unstructured.Unstructured) Status {
	s := Status{}
	if cr == nil {
		return s
	}
	s.CurrentPrimary, _, _ = unstructured.NestedString(cr.Object, "status", "currentPrimary")
	s.TargetPrimary, _, _ = unstructured.NestedString(cr.Object, "status", "targetPrimary")
	s.Phase, _, _ = unstructured.NestedString(cr.Object, "status", "phase")
	s.IsReplicaCluster, _, _ = unstructured.NestedBool(cr.Object, "spec", "replica", "enabled")

	// Lag fields may live in different sub-paths depending on CNPG
	// version. Try the common ones (status.lag, status.replication.lagSeconds).
	if lag, found, _ := unstructured.NestedInt64(cr.Object, "status", "lag"); found {
		s.LagSeconds = int(lag)
	} else if lag, found, _ := unstructured.NestedInt64(cr.Object, "status", "replication", "lagSeconds"); found {
		s.LagSeconds = int(lag)
	}

	// readyInstances — tolerate the int64/int/float64 encodings the
	// dynamic client + fakes produce (#4901).
	if v, found, _ := unstructured.NestedFieldNoCopy(cr.Object, "status", "readyInstances"); found {
		switch n := v.(type) {
		case int64:
			s.ReadyInstances, s.HasReadyInstances = int(n), true
		case int:
			s.ReadyInstances, s.HasReadyInstances = n, true
		case float64:
			s.ReadyInstances, s.HasReadyInstances = int(n), true
		}
	}

	// timelineID (#5125 Defect-2) — same tolerant int64/int/float64
	// decoding as readyInstances above.
	if v, found, _ := unstructured.NestedFieldNoCopy(cr.Object, "status", "timelineID"); found {
		switch n := v.(type) {
		case int64:
			s.TimelineID, s.HasTimelineID = int(n), true
		case int:
			s.TimelineID, s.HasTimelineID = n, true
		case float64:
			s.TimelineID, s.HasTimelineID = int(n), true
		}
	}

	conds, _, _ := unstructured.NestedSlice(cr.Object, "status", "conditions")
	for _, c := range conds {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		typ, _, _ := unstructured.NestedString(cm, "type")
		stat, _, _ := unstructured.NestedString(cm, "status")
		if typ == "Ready" && stat == "True" {
			s.Ready = true
			break
		}
	}
	return s
}

// NamespacedName helper for keying logs.
func NamespacedName(ns, name string) types.NamespacedName {
	return types.NamespacedName{Namespace: ns, Name: name}
}
