package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
)

// #6092 — when the apiserver REFUSES the Secret delete, the teardown must
// report it and the finalizer must stay.
//
// WHY THIS FILE EXISTS: the test next door cannot fail on this defect.
// tenant_console_tls_secret_reap_r17_test.go covers the same function and is
// otherwise well built — pre-condition control, blast-radius control — but it
// builds its client with a bare `fake.NewClientBuilder()`, which carries no
// RBAC. Its Delete can therefore never be refused. Measured: both R17 tests
// passed GREEN on 8d8ea5440 while hw293 (dep a0077ba47e3720e5) was returning
//
//	delete Secret kube-system/org-wildcard-tls-p474del1-omani-rest: ... is
//	forbidden: User "system:serviceaccount:catalyst-system:catalyst-organization-controller"
//	cannot delete resource "secrets" in API group "" in the namespace "kube-system"
//
// for that exact call, and Organization `p474del1` sat in Terminating with its
// namespace already gone. A test that proves the code CALLS Delete says nothing
// about what happens when Delete is DENIED, and the denial is the whole defect.
//
// #6096 grants the verb, which stops this particular denial at its source. What
// no test anywhere asserts is the behaviour #6096's own body claims and relies
// on: that a refused delete PROPAGATES rather than being swallowed, and that the
// finalizer consequently SURVIVES. That pair is what keeps a failed teardown
// from tombstoning the CR with the Secret still on the cluster — a silent
// cross-Org identity leak, because the Secret name is derived from
// slug+parentDomain and the next Org on that subdomain would inherit it. These
// tests pin that contract independent of any one grant, so the next teardown arm
// that gains a mutation without a verb is caught here rather than on a Sovereign.
//
// The interceptor denies ONLY Delete, and only for the one Secret. Reads stay
// permitted, mirroring the real grant exactly (`secrets: get,list,watch` with no
// `delete`). That fidelity matters: consoleRegionTargets Gets two Secrets to
// resolve the region set, so a blanket Secret denial would fail the teardown in
// a DIFFERENT place and the test would pass for the wrong reason.
//
// MUTATION-TESTED — recorded because a guard nobody watched fail is decoration.
// Both mutations were run and the output below is what they actually printed:
//
//  1. Remove the interceptor, leaving a permissive client. Proves the denial is
//     what creates the failing state and that it reaches the call site:
//     propagation -> RED  "expected teardown to return the apiserver's refusal,
//     got nil"
//     finalizer   -> RED  "the Organization is gone after a FAILED teardown ...
//     organizations.orgs.openova.io \"p474del1\" not found"
//     control     -> GREEN (it must be — nothing is denied in this variant)
//
//  2. Make deleteOrgWildcardTLSSecret swallow the Forbidden and return
//     (false, nil) — the exact "cascade reports progress it did not make" shape:
//     propagation -> RED (same message)
//     finalizer   -> RED (same message: CR tombstoned over the live Secret)
//     control     -> GREEN
//     and, the reason this file exists:
//     TestDeleteOrgWildcardTLSSecret_ReapsTheOrphan_R17   -> GREEN
//     TestDeleteOrgWildcardTLSSecret_AbsentIsSuccess_R17  -> GREEN
//
// Mutation 2 is the one that matters. The pre-existing R17 pair passes cleanly
// under the very defect it looks like it covers, because a client with no RBAC
// cannot produce a refusal for the code to mishandle. That is the failing state
// these tests make reachable.

const (
	denySlug   = "p474del1"
	denyParent = "omani.rest"
	// Derived by orgConsoleTLSNamesForOrg the same way the up-path derives it:
	// org-wildcard-tls-<slug>-<parent-dashed>. Spelled out rather than computed
	// so a change to the derivation shows up here as a failure instead of being
	// silently tracked by the test.
	denyCertName = "org-wildcard-tls-p474del1-omani-rest"
	denyCertNS   = "kube-system"
)

// forbidSecretDelete refuses Delete for exactly one Secret with the apiserver's
// own Forbidden error, and passes every other call — including all reads —
// straight through. This is the RBAC state #6092 measured, expressed as an
// interceptor.
func forbidSecretDelete(ns, name string) interceptor.Funcs {
	return interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if _, isSecret := obj.(*corev1.Secret); isSecret &&
				obj.GetNamespace() == ns && obj.GetName() == name {
				return apierrors.NewForbidden(
					schema.GroupResource{Group: "", Resource: "secrets"}, name,
					fmt.Errorf(`User "system:serviceaccount:catalyst-system:catalyst-organization-controller" `+
						`cannot delete resource "secrets" in API group "" in the namespace %q`, ns))
			}
			return c.Delete(ctx, obj, opts...)
		},
	}
}

// denyingOrg is an Organization already marked for deletion and still holding
// the tenant-networking finalizer — the state the reconciler is in when the
// cascade runs.
func denyingOrg() *orgapi.Organization {
	o := &orgapi.Organization{}
	o.Name = denySlug
	o.Spec.Slug = denySlug
	o.Spec.TenantPublic.ParentDomain = denyParent
	o.Spec.TenantPublic.Subdomain = denySlug
	o.Finalizers = []string{TenantNetworkingFinalizer}
	now := metav1.NewTime(time.Now())
	o.DeletionTimestamp = &now
	return o
}

// denialReconciler builds a Reconciler whose fake client holds the orphaned TLS
// Secret and the Organization, optionally with Delete on that Secret refused.
//
// The Gateway and Certificate GVKs are registered even though neither object is
// seeded. Without them the fake client fails those two teardown hops with a
// scheme error BEFORE the Secret delete is ever attempted, and every assertion
// below would pass while testing the wrong hop entirely.
func denialReconciler(t *testing.T, deny bool) (*Reconciler, client.Client) {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add clientgo scheme: %v", err)
	}
	if err := orgapi.AddToScheme(scheme); err != nil {
		t.Fatalf("add orgapi scheme: %v", err)
	}
	for _, gvk := range []schema.GroupVersionKind{certificateGVK, gatewayGVK} {
		scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		scheme.AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
	}

	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: denyCertNS, Name: denyCertName}}

	b := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(denyingOrg(), secret)
	if deny {
		b = b.WithInterceptorFuncs(forbidSecretDelete(denyCertNS, denyCertName))
	}
	c := b.Build()

	return &Reconciler{Client: c, Log: logf.Log.WithName("test-6092")}, c
}

func secretExists(t *testing.T, c client.Client) bool {
	t.Helper()
	err := c.Get(context.Background(),
		types.NamespacedName{Namespace: denyCertNS, Name: denyCertName}, &corev1.Secret{})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("probing the Secret failed for a reason other than absence: %v", err)
	}
	return err == nil
}

// A refused Secret delete must come back OUT of the cascade. teardownTenantNetworking
// runs all four arms and keeps the FIRST error; if the refusal is dropped anywhere
// between the Delete call and that return value, the caller strips the finalizer
// and the Organization tombstones with the Secret still on the cluster.
func TestTeardownTenantNetworking_ForbiddenSecretDelete_Propagates_6092(t *testing.T) {
	r, c := denialReconciler(t, true)

	// PRE-CONDITION: the Secret must be present, or "still present afterwards"
	// below would be satisfied by an object that never existed.
	if !secretExists(t, c) {
		t.Fatal("precondition: the orphaned TLS Secret must exist before teardown")
	}

	err := r.teardownTenantNetworking(context.Background(), denyingOrg())
	if err == nil {
		t.Fatal("expected teardown to return the apiserver's refusal, got nil — " +
			"a swallowed denial lets the caller strip the finalizer and tombstone the CR " +
			"while the Secret is still on the cluster")
	}

	// WHICH hop failed, not merely THAT one did. All four arms are absent-as-success
	// against this client, so any other error here means the cascade broke somewhere
	// upstream and the Secret delete was never reached.
	if !strings.Contains(err.Error(), "delete org wildcard TLS secret") {
		t.Errorf("error did not come from the Secret arm — the test is not exercising the "+
			"hop it claims to: %v", err)
	}
	if !apierrors.IsForbidden(errCause(err)) {
		t.Errorf("the propagated error lost its Forbidden identity; a caller cannot tell a "+
			"permission failure from a transient one: %v", err)
	}
	if !secretExists(t, c) {
		t.Error("the Secret was deleted despite the refusal — the interceptor is not reaching " +
			"the call site, so every other assertion in this file is vacuous")
	}
}

// THE DECISIVE ONE. With the delete refused, the reconciler must leave the
// Organization holding its finalizer — Terminating is the correct outcome of an
// unretryable teardown error, and is strictly better than a clean tombstone over
// a leaked Secret.
func TestReconcile_ForbiddenSecretDelete_KeepsFinalizer_6092(t *testing.T) {
	r, c := denialReconciler(t, true)

	res, err := r.Reconcile(context.Background(),
		ctrl.Request{NamespacedName: types.NamespacedName{Name: denySlug}})
	if err != nil {
		t.Fatalf("reconcile returned a hard error: %v", err)
	}

	// The 30s flat requeue, asserted so the cadence cannot drift unnoticed.
	//
	// Note what this pins: organization_controller.go:484-487 logs the teardown
	// error and returns (RequeueAfter, nil) — a NIL error. The finalizer is held
	// correctly, but controller-runtime never counts a reconcile error and never
	// engages backoff, so an Organization wedged in Terminating forever is
	// visible only as a recurring log line and registers on no error-rate alert.
	// That observability gap is real and is deliberately NOT changed here:
	// returning the error would also alter this cadence, which the orchestrator
	// comment chose for the transient-PowerDNS case. Someone should own it
	// separately; this test documents the behaviour rather than blessing it.
	if res.RequeueAfter != 30*time.Second {
		t.Errorf("expected a 30s requeue after a failed teardown, got %v", res.RequeueAfter)
	}

	var org orgapi.Organization
	if err := c.Get(context.Background(), types.NamespacedName{Name: denySlug}, &org); err != nil {
		t.Fatalf("the Organization is gone after a FAILED teardown — the CR tombstoned over a "+
			"Secret that is still on the cluster, which is the leak this guards: %v", err)
	}
	if !containsFinalizer(org.Finalizers, TenantNetworkingFinalizer) {
		t.Errorf("Organization lost its tenant-networking finalizer while the Secret it was "+
			"holding for is still present — got finalizers %v", org.Finalizers)
	}
	if !secretExists(t, c) {
		t.Error("precondition for this assertion collapsed: the Secret is gone, so the " +
			"finalizer check above was not testing a failed teardown")
	}
}

// CONTROL — the same path with the delete PERMITTED must both reap the Secret and
// release the finalizer. Without this the two tests above would be satisfied by a
// teardown that is simply broken for everyone, which is not the contract.
func TestReconcile_PermittedSecretDelete_ReapsAndReleases_6092(t *testing.T) {
	r, c := denialReconciler(t, false)

	if !secretExists(t, c) {
		t.Fatal("precondition: the Secret must exist before the permitted teardown")
	}

	if _, err := r.Reconcile(context.Background(),
		ctrl.Request{NamespacedName: types.NamespacedName{Name: denySlug}}); err != nil {
		t.Fatalf("reconcile returned a hard error on the permitted path: %v", err)
	}

	if secretExists(t, c) {
		t.Error("the orphaned TLS Secret survived a teardown that was ALLOWED to delete it " +
			"— this is the R17 leak, and it means the reap arm is not running at all")
	}

	// The CR is either already tombstoned by the fake client (last finalizer gone)
	// or still present with the finalizer stripped. Both are a released CR; what
	// must NOT happen is it still holding the finalizer after a clean teardown.
	var org orgapi.Organization
	switch err := c.Get(context.Background(), types.NamespacedName{Name: denySlug}, &org); {
	case apierrors.IsNotFound(err):
		// Released and collected.
	case err != nil:
		t.Fatalf("get organization: %v", err)
	default:
		if containsFinalizer(org.Finalizers, TenantNetworkingFinalizer) {
			t.Error("the tenant-networking finalizer survived a SUCCESSFUL teardown — the CR " +
				"would hang in Terminating forever with nothing left to clean up")
		}
	}
}

// errCause unwraps to the innermost error so the Forbidden identity can be
// checked through the two fmt.Errorf %w wraps the cascade adds.
func errCause(err error) error {
	for {
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return err
		}
		next := u.Unwrap()
		if next == nil {
			return err
		}
		err = next
	}
}
