// Reconcile() skeleton smoke test for K-Cont-1.
//
// K-Cont-1 ships a NO-OP Reconcile(); the only invariant we can
// assert here is that calling Reconcile against an empty/missing CR
// does not panic and returns no error + no requeue. K-Cont-2 will
// replace this with envtest-backed integration tests covering the
// lease / switchover / replication-watch state machine.
package controller

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestReconcile_Skeleton_NoOp_NoCR(t *testing.T) {
	t.Parallel()

	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()
	r := &ContinuumReconciler{Client: c, Scheme: s}

	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "openova-system", Name: "missing"},
	})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if res.RequeueAfter != 0 || res.Requeue {
		t.Fatalf("expected zero-value Result, got %+v", res)
	}
}

func TestReconcile_Skeleton_NoOp_WithCR(t *testing.T) {
	t.Parallel()

	cr := &unstructured.Unstructured{}
	cr.SetGroupVersionKind(continuumGVK)
	cr.SetName("test-continuum")
	cr.SetNamespace("openova-system")
	cr.Object["spec"] = map[string]any{
		"applicationRef":    "demo-app",
		"primaryRegion":     "hz-fsn-rtz-prod",
		"hotStandbyRegions": []any{"hz-hel-rtz-prod"},
		"leaseClient": map[string]any{
			"kind": "cloudflare-kv",
		},
	}

	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(cr).Build()
	r := &ContinuumReconciler{Client: c, Scheme: s}

	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "openova-system", Name: "test-continuum"},
	})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if res.RequeueAfter != 0 || res.Requeue {
		t.Fatalf("expected zero-value Result, got %+v", res)
	}
}

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("add clientgo scheme: %v", err)
	}
	return s
}
