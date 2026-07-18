// Tests for #5224 — canonical role-password re-assert on
// acting-primary transition (the hw273 harbor 28P01 lockout shape).

package controller

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/openova-io/openova/core/controllers/continuum/internal/cnpg"
)

// newManagedRolesPair builds the shared-pg pair shape: the
// primary-labeled CR carries managed.roles (bp-postgres cluster.yaml);
// the replica-labeled CR carries none (replica-cluster.yaml).
// actingPrimaryIsLabelPrimary controls the LIVE spec.replica.enabled
// flags (the post-failover reality the static labels do not track).
func newManagedRolesPair(ns, pair, roleSecret string, actingPrimaryIsLabelPrimary bool) (*unstructured.Unstructured, *unstructured.Unstructured) {
	primary := &unstructured.Unstructured{}
	primary.SetGroupVersionKind(clusterGVKForTest())
	primary.SetNamespace(ns)
	primary.SetName(pair)
	primary.SetLabels(map[string]string{cnpg.PairLabel: pair, cnpg.PairRoleLabel: cnpg.RolePrimary})
	_ = unstructured.SetNestedSlice(primary.Object, []interface{}{
		map[string]interface{}{
			"name":           "harbor",
			"ensure":         "present",
			"login":          true,
			"passwordSecret": map[string]interface{}{"name": roleSecret},
		},
	}, "spec", "managed", "roles")

	replica := &unstructured.Unstructured{}
	replica.SetGroupVersionKind(clusterGVKForTest())
	replica.SetNamespace(ns)
	replica.SetName(pair + "-replica")
	replica.SetLabels(map[string]string{cnpg.PairLabel: pair, cnpg.PairRoleLabel: cnpg.RoleReplica})

	_ = unstructured.SetNestedField(primary.Object, !actingPrimaryIsLabelPrimary, "spec", "replica", "enabled")
	_ = unstructured.SetNestedField(replica.Object, actingPrimaryIsLabelPrimary, "spec", "replica", "enabled")
	return primary, replica
}

func newRoleSecretObj(ns, name string) *unstructured.Unstructured {
	s := &unstructured.Unstructured{}
	s.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "Secret"})
	s.SetNamespace(ns)
	s.SetName(name)
	return s
}

func secretAnnotation(t *testing.T, r *ContinuumReconciler, ns, name string) string {
	t.Helper()
	got, err := r.Dyn.Resource(cnpg.SecretGVR).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("readback secret %s/%s: %v", ns, name, err)
	}
	return got.GetAnnotations()[cnpg.RoleReassertAnnotation]
}

func statusesFor(t *testing.T, r *ContinuumReconciler, ns, primaryName, replicaName string) (cnpg.Status, cnpg.Status) {
	t.Helper()
	reader := r.cnpgReader()
	ps, _, err := reader.Get(context.Background(), ns, primaryName)
	if err != nil {
		t.Fatalf("Get %s: %v", primaryName, err)
	}
	rs, _, err := reader.Get(context.Background(), ns, replicaName)
	if err != nil {
		t.Fatalf("Get %s: %v", replicaName, err)
	}
	return ps, rs
}

func TestActingPrimaryName(t *testing.T) {
	t.Parallel()
	p := cnpg.Status{IsReplicaCluster: false}
	s := cnpg.Status{IsReplicaCluster: true}
	if got := actingPrimaryName("a", p, "b", s); got != "a" {
		t.Fatalf("label-primary acting: got %q want a", got)
	}
	if got := actingPrimaryName("a", s, "b", p); got != "b" {
		t.Fatalf("label-replica acting (post-failover): got %q want b", got)
	}
	// Ambiguous shapes must NEVER guess.
	if got := actingPrimaryName("a", p, "b", p); got != "" {
		t.Fatalf("both-primary: got %q want empty", got)
	}
	if got := actingPrimaryName("a", s, "b", s); got != "" {
		t.Fatalf("both-replica: got %q want empty", got)
	}
}

// TestReassertRoles_FirstObservationTouchesActingPrimary — the hw273
// restart shape: the per-CR goroutine restarts mid-flap (18:33:57Z),
// first settled observation must fire ONE re-assert on the acting
// primary's managed-role passwordSecrets (rv touch → CNPG re-applies
// the canonical password, healing the out-of-band clobber).
func TestReassertRoles_FirstObservationTouchesActingPrimary(t *testing.T) {
	t.Parallel()
	const ns, pair, roleSecret = "shared-data", "shared-pg", "shared-pg-harbor"
	cr := newTestContinuumCR(ns, "dr-shared-pg", "region-a", []string{"region-b"}, "in-memory")
	primaryObj, replicaObj := newManagedRolesPair(ns, pair, roleSecret, true)
	r, _, _ := newReconciler(t, cr, primaryObj, replicaObj, newRoleSecretObj(ns, roleSecret))
	clock := time.Date(2026, 7, 18, 18, 34, 0, 0, time.UTC)
	r.Now = func() time.Time { return clock }

	nn := types.NamespacedName{Namespace: ns, Name: "dr-shared-pg"}
	r.activeContinuumsMu.Lock()
	r.activeContinuums[nn.String()] = &continuumGoroutine{}
	r.activeContinuumsMu.Unlock()

	ps, rs := statusesFor(t, r, ns, pair, pair+"-replica")
	r.reassertRolesOnPrimaryTransition(context.Background(), nn, r.cnpgReader(), ns, pair, ps, pair+"-replica", rs)

	if got := secretAnnotation(t, r, ns, roleSecret); got != "2026-07-18T18:34:00Z" {
		t.Fatalf("first observation must touch the acting primary's role Secret; annotation = %q", got)
	}
	r.activeContinuumsMu.Lock()
	last := r.activeContinuums[nn.String()].lastActingPrimary
	r.activeContinuumsMu.Unlock()
	if last != pair {
		t.Fatalf("watermark = %q want %q", last, pair)
	}

	// Second tick with NO transition — must NOT touch again (the
	// no-thrash contract: the annotation stamp stays put).
	clock = clock.Add(time.Minute)
	r.reassertRolesOnPrimaryTransition(context.Background(), nn, r.cnpgReader(), ns, pair, ps, pair+"-replica", rs)
	if got := secretAnnotation(t, r, ns, roleSecret); got != "2026-07-18T18:34:00Z" {
		t.Fatalf("steady-state tick must not re-touch; annotation = %q", got)
	}
}

// TestReassertRoles_TransitionOnFailbackRetouches — a promote then a
// failback each constitute an acting-primary CHANGE and each must
// re-assert (the failback leg is exactly the hw273 heal: region-a
// resumes as acting primary with possibly-clobbered roles).
func TestReassertRoles_TransitionOnFailbackRetouches(t *testing.T) {
	t.Parallel()
	const ns, pair, roleSecret = "shared-data", "shared-pg", "shared-pg-harbor"
	cr := newTestContinuumCR(ns, "dr-shared-pg", "region-a", []string{"region-b"}, "in-memory")
	primaryObj, replicaObj := newManagedRolesPair(ns, pair, roleSecret, true)
	r, _, _ := newReconciler(t, cr, primaryObj, replicaObj, newRoleSecretObj(ns, roleSecret))
	clock := time.Date(2026, 7, 18, 18, 34, 0, 0, time.UTC)
	r.Now = func() time.Time { return clock }

	nn := types.NamespacedName{Namespace: ns, Name: "dr-shared-pg"}
	// Watermark pre-seeded at the label-primary (steady state observed).
	r.activeContinuumsMu.Lock()
	r.activeContinuums[nn.String()] = &continuumGoroutine{lastActingPrimary: pair}
	r.activeContinuumsMu.Unlock()

	// PROMOTE: the replica-labeled half becomes acting primary. It has
	// NO managed roles (replica-cluster.yaml) → transition recorded,
	// zero touches — and critically NOTHING asserts the replica
	// region's local secret set against the pair.
	promoted := cnpg.Status{IsReplicaCluster: false}
	demoted := cnpg.Status{IsReplicaCluster: true}
	r.reassertRolesOnPrimaryTransition(context.Background(), nn, r.cnpgReader(), ns, pair, demoted, pair+"-replica", promoted)
	if got := secretAnnotation(t, r, ns, roleSecret); got != "" {
		t.Fatalf("promote to the roles-less replica half must not touch the primary's Secret; annotation = %q", got)
	}

	// FAILBACK: the label-primary is acting primary again — must fire a
	// fresh re-assert (its DB may carry an out-of-band role clobber
	// accrued during the flap; CNPG cannot detect it without the rv
	// bump).
	clock = time.Date(2026, 7, 18, 19, 0, 0, 0, time.UTC)
	r.reassertRolesOnPrimaryTransition(context.Background(), nn, r.cnpgReader(), ns, pair, promoted, pair+"-replica", demoted)
	if got := secretAnnotation(t, r, ns, roleSecret); got != "2026-07-18T19:00:00Z" {
		t.Fatalf("failback must re-touch the canonical role Secret; annotation = %q", got)
	}
}

// TestReassertRoles_AmbiguousPairNeverTouches — mid-switchover shapes
// (both halves claiming primary, or both replica) must never fire.
func TestReassertRoles_AmbiguousPairNeverTouches(t *testing.T) {
	t.Parallel()
	const ns, pair, roleSecret = "shared-data", "shared-pg", "shared-pg-harbor"
	cr := newTestContinuumCR(ns, "dr-shared-pg", "region-a", []string{"region-b"}, "in-memory")
	primaryObj, replicaObj := newManagedRolesPair(ns, pair, roleSecret, true)
	r, _, _ := newReconciler(t, cr, primaryObj, replicaObj, newRoleSecretObj(ns, roleSecret))

	nn := types.NamespacedName{Namespace: ns, Name: "dr-shared-pg"}
	r.activeContinuumsMu.Lock()
	r.activeContinuums[nn.String()] = &continuumGoroutine{}
	r.activeContinuumsMu.Unlock()

	both := cnpg.Status{IsReplicaCluster: false}
	r.reassertRolesOnPrimaryTransition(context.Background(), nn, r.cnpgReader(), ns, pair, both, pair+"-replica", both)
	if got := secretAnnotation(t, r, ns, roleSecret); got != "" {
		t.Fatalf("ambiguous pair must never touch; annotation = %q", got)
	}
	r.activeContinuumsMu.Lock()
	last := r.activeContinuums[nn.String()].lastActingPrimary
	r.activeContinuumsMu.Unlock()
	if last != "" {
		t.Fatalf("ambiguous pair must not advance the watermark; got %q", last)
	}
}
