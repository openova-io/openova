package cnpg

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func gvrListMap() map[schema.GroupVersionResource]string {
	return map[schema.GroupVersionResource]string{
		ClusterGVR: "ClusterList",
	}
}

func newCluster(ns, name string, labels map[string]string, status map[string]interface{}, replicaEnabled bool) *unstructured.Unstructured {
	c := &unstructured.Unstructured{}
	c.SetGroupVersionKind(schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"})
	c.SetNamespace(ns)
	c.SetName(name)
	c.SetLabels(labels)
	if status == nil {
		status = map[string]interface{}{}
	}
	c.Object["status"] = status
	if replicaEnabled {
		_ = unstructured.SetNestedField(c.Object, true, "spec", "replica", "enabled")
	}
	return c
}

func TestParseStatus_HappyPath(t *testing.T) {
	t.Parallel()
	cr := newCluster("ns", "p", nil, map[string]interface{}{
		"currentPrimary": "p-1",
		"targetPrimary":  "p-2",
		"lag":            int64(7),
		"phase":          "Cluster in healthy state",
		"conditions": []interface{}{
			map[string]interface{}{"type": "Ready", "status": "True"},
		},
	}, false)
	s := parseStatus(cr)
	if s.CurrentPrimary != "p-1" || s.TargetPrimary != "p-2" {
		t.Errorf("primary fields wrong: %+v", s)
	}
	if s.LagSeconds != 7 {
		t.Errorf("lag = %d want 7", s.LagSeconds)
	}
	if !s.Ready {
		t.Errorf("expected Ready=true")
	}
	if s.IsReplicaCluster {
		t.Errorf("expected IsReplicaCluster=false")
	}
}

func TestParseStatus_ReplicationLagSecondsAlternativePath(t *testing.T) {
	t.Parallel()
	cr := newCluster("ns", "r", nil, map[string]interface{}{
		"replication": map[string]interface{}{
			"lagSeconds": int64(42),
		},
	}, true)
	s := parseStatus(cr)
	if s.LagSeconds != 42 {
		t.Errorf("lag = %d want 42", s.LagSeconds)
	}
	if !s.IsReplicaCluster {
		t.Errorf("expected IsReplicaCluster=true")
	}
}

func TestParseStatus_TolerantToMissing(t *testing.T) {
	t.Parallel()
	cr := newCluster("ns", "x", nil, nil, false)
	s := parseStatus(cr)
	if s != (Status{}) {
		t.Errorf("expected zero Status, got %+v", s)
	}
}

func TestReader_GetAndSetAnnotation(t *testing.T) {
	t.Parallel()
	cr := newCluster("ns", "p", nil, nil, false)
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrListMap(), cr)
	r := NewReader(dyn)

	st, _, err := r.Get(context.Background(), "ns", "p")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if st != (Status{}) {
		t.Fatalf("expected empty Status, got %+v", st)
	}

	if err := r.SetPrimaryAnnotation(context.Background(), "ns", "p", "p-2"); err != nil {
		t.Fatalf("SetPrimaryAnnotation: %v", err)
	}
	got, err := dyn.Resource(ClusterGVR).Namespace("ns").Get(context.Background(), "p", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get post-annotate: %v", err)
	}
	if got.GetAnnotations()[PrimaryAnnotation] != "p-2" {
		t.Fatalf("annotation not set: %v", got.GetAnnotations())
	}

	if err := r.ClearPrimaryAnnotation(context.Background(), "ns", "p"); err != nil {
		t.Fatalf("ClearPrimaryAnnotation: %v", err)
	}
	got, _ = dyn.Resource(ClusterGVR).Namespace("ns").Get(context.Background(), "p", metav1.GetOptions{})
	if _, ok := got.GetAnnotations()[PrimaryAnnotation]; ok {
		t.Fatalf("annotation should be cleared: %v", got.GetAnnotations())
	}
	// Idempotent second clear.
	if err := r.ClearPrimaryAnnotation(context.Background(), "ns", "p"); err != nil {
		t.Fatalf("ClearPrimaryAnnotation second: %v", err)
	}
}

func TestReader_FindPair(t *testing.T) {
	t.Parallel()
	primary := newCluster("ns", "demo-primary", map[string]string{
		PairLabel:     "demo",
		PairRoleLabel: RolePrimary,
	}, nil, false)
	replica := newCluster("ns", "demo-replica", map[string]string{
		PairLabel:     "demo",
		PairRoleLabel: RoleReplica,
	}, nil, true)
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrListMap(), primary, replica)
	r := NewReader(dyn)

	p, rr, err := r.FindPair(context.Background(), "ns", "demo")
	if err != nil {
		t.Fatalf("FindPair: %v", err)
	}
	if p == nil || rr == nil {
		t.Fatalf("FindPair returned nil halves: p=%v r=%v", p, rr)
	}
	if p.GetName() != "demo-primary" {
		t.Errorf("primary name = %s", p.GetName())
	}
	if rr.GetName() != "demo-replica" {
		t.Errorf("replica name = %s", rr.GetName())
	}
}

func TestReader_FindPair_Incomplete(t *testing.T) {
	t.Parallel()
	primary := newCluster("ns", "demo-primary", map[string]string{
		PairLabel:     "demo",
		PairRoleLabel: RolePrimary,
	}, nil, false)
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrListMap(), primary)
	r := NewReader(dyn)
	if _, _, err := r.FindPair(context.Background(), "ns", "demo"); err == nil {
		t.Fatal("expected error on incomplete pair")
	}
}

func TestReader_SetReplicaEnabled(t *testing.T) {
	t.Parallel()
	cr := newCluster("ns", "p", nil, nil, false)
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrListMap(), cr)
	r := NewReader(dyn)

	if err := r.SetReplicaEnabled(context.Background(), "ns", "p", true); err != nil {
		t.Fatalf("SetReplicaEnabled true: %v", err)
	}
	got, _ := dyn.Resource(ClusterGVR).Namespace("ns").Get(context.Background(), "p", metav1.GetOptions{})
	en, _, _ := unstructured.NestedBool(got.Object, "spec", "replica", "enabled")
	if !en {
		t.Fatalf("expected replica.enabled=true, got %v", en)
	}

	if err := r.SetReplicaEnabled(context.Background(), "ns", "p", false); err != nil {
		t.Fatalf("SetReplicaEnabled false: %v", err)
	}
	got, _ = dyn.Resource(ClusterGVR).Namespace("ns").Get(context.Background(), "p", metav1.GetOptions{})
	en, _, _ = unstructured.NestedBool(got.Object, "spec", "replica", "enabled")
	if en {
		t.Fatalf("expected replica.enabled=false, got %v", en)
	}
}

// TestParseStatus_TimelineID — #5125 Defect-2: status.timelineID must
// decode (tolerating the int64/int/float64 encodings the dynamic
// client + fakes produce, same as readyInstances), and be absent
// (HasTimelineID=false) when the field simply isn't present yet.
func TestParseStatus_TimelineID(t *testing.T) {
	t.Parallel()
	cr := newCluster("ns", "p", nil, map[string]interface{}{
		"timelineID": int64(3),
	}, false)
	s := parseStatus(cr)
	if !s.HasTimelineID || s.TimelineID != 3 {
		t.Errorf("TimelineID = %d HasTimelineID=%v, want 3/true", s.TimelineID, s.HasTimelineID)
	}

	cr2 := newCluster("ns", "p2", nil, map[string]interface{}{}, false)
	s2 := parseStatus(cr2)
	if s2.HasTimelineID {
		t.Errorf("expected HasTimelineID=false for a Cluster CR with no timelineID field, got TimelineID=%d", s2.TimelineID)
	}
}

// TestReader_RecloneCluster — #5125 Defect-2: RecloneCluster deletes
// then re-creates the Cluster CR, preserving spec/labels/annotations
// but stripping status/resourceVersion so CNPG treats it as a fresh
// object (re-bootstrap via pg_basebackup).
func TestReader_RecloneCluster(t *testing.T) {
	t.Parallel()
	cr := newCluster("ns", "demo-replica", map[string]string{
		PairLabel:     "demo",
		PairRoleLabel: RoleReplica,
	}, map[string]interface{}{
		"timelineID": int64(3),
		"phase":      "Cluster in healthy state",
	}, true)
	cr.SetAnnotations(map[string]string{"some-annotation": "keep-me"})

	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrListMap(), cr)
	r := NewReader(dyn)

	if err := r.RecloneCluster(context.Background(), "ns", "demo-replica"); err != nil {
		t.Fatalf("RecloneCluster: %v", err)
	}

	got, err := dyn.Resource(ClusterGVR).Namespace("ns").Get(context.Background(), "demo-replica", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get post-reclone: %v", err)
	}
	// spec.replica.enabled=true must survive the reclone.
	en, _, _ := unstructured.NestedBool(got.Object, "spec", "replica", "enabled")
	if !en {
		t.Fatalf("expected spec.replica.enabled=true preserved, got %v", en)
	}
	// Labels + annotations must survive.
	if got.GetLabels()[PairLabel] != "demo" {
		t.Fatalf("pair label not preserved: %v", got.GetLabels())
	}
	if got.GetAnnotations()["some-annotation"] != "keep-me" {
		t.Fatalf("annotation not preserved: %v", got.GetAnnotations())
	}
	// status must be wiped — the whole point is a fresh bootstrap.
	if _, found, _ := unstructured.NestedFieldNoCopy(got.Object, "status", "timelineID"); found {
		t.Fatalf("expected status.timelineID wiped after reclone, still present")
	}
}

// TestReader_RecloneCluster_MissingCluster — RecloneCluster on a
// nonexistent Cluster CR errors rather than panicking (bounded
// attempts in rejoin.go treat this as a failed attempt, retried next
// cooldown).
func TestReader_RecloneCluster_MissingCluster(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrListMap())
	r := NewReader(dyn)
	if err := r.RecloneCluster(context.Background(), "ns", "nope"); err == nil {
		t.Fatal("expected error re-cloning a nonexistent Cluster CR")
	}
}

func TestMaxLagSeconds(t *testing.T) {
	t.Parallel()
	if MaxLagSeconds() != 0 {
		t.Errorf("empty MaxLagSeconds should be 0")
	}
	if got := MaxLagSeconds(Status{LagSeconds: 3}, Status{LagSeconds: 7}, Status{LagSeconds: 1}); got != 7 {
		t.Errorf("MaxLagSeconds = %d want 7", got)
	}
}
