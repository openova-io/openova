package controller

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	accessv1 "github.com/openova-io/openova/core/controllers/access/api/v1alpha1"
)

// targetCase reconciles target acme/shell (or the one given) and returns the
// target after it, the error and the number of service-account creates.
func targetCase(t *testing.T, tg *accessv1.AccessTarget, createErr error, objs ...client.Object) (accessv1.AccessTarget, int, client.Client, error) {
	t.Helper()
	creates := 0
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(append(objs, tg)...).
		WithStatusSubresource(&accessv1.AccessTarget{}).
		WithInterceptorFuncs(interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			creates++
			if createErr != nil {
				return createErr
			}
			return c.Create(ctx, obj, opts...)
		}}).Build()
	r := &TargetReconciler{Client: c, Reader: c, SessionNamespace: "guacamole", DeniedNamespaces: []string{"openbao", "kube-system", "flux-system", "guacamole"}}
	key := client.ObjectKeyFromObject(tg)
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	var got accessv1.AccessTarget
	if gerr := c.Get(context.Background(), key, &got); gerr != nil {
		t.Fatal(gerr)
	}
	return got, creates, c, err
}

func condition(t *testing.T, tg accessv1.AccessTarget, typ string) metav1.Condition {
	t.Helper()
	c := meta.FindStatusCondition(tg.Status.Conditions, typ)
	if c == nil {
		t.Fatalf("target %s has no condition %s: %+v", tg.Name, typ, tg.Status.Conditions)
	}
	return *c
}

func TestTargetReadyCreatesTheSessionServiceAccount(t *testing.T) {
	tg := target(true, 1, "1h")
	tg.Generation = 3
	got, creates, c, err := targetCase(t, tg, nil)
	if err != nil || creates != 1 {
		t.Fatalf("err %v, creates %d; want nil, 1", err, creates)
	}
	for _, typ := range []string{accessv1.ConditionServiceAccountReady, accessv1.ConditionReady} {
		if cond := condition(t, got, typ); cond.Status != metav1.ConditionTrue || cond.Reason != "Present" || cond.ObservedGeneration != 3 {
			t.Errorf("condition %s: %+v; want True, Present, generation 3", typ, cond)
		}
	}
	if got.Status.ObservedGeneration != 3 {
		t.Errorf("observedGeneration %d, want 3", got.Status.ObservedGeneration)
	}
	var sa corev1.ServiceAccount
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "guacamole", Name: SessionServiceAccount}, &sa); err != nil {
		t.Fatalf("service account guacamole/%s: %v", SessionServiceAccount, err)
	}
	if sa.AutomountServiceAccountToken == nil || *sa.AutomountServiceAccountToken {
		t.Errorf("the session service account mounts a token: %v", sa.AutomountServiceAccountToken)
	}
}

func TestTargetWithTheServiceAccountPresentCreatesNothing(t *testing.T) {
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: "guacamole", Name: SessionServiceAccount}}
	got, creates, _, err := targetCase(t, target(true, 1, "1h"), nil, sa)
	if err != nil || creates != 0 || condition(t, got, accessv1.ConditionReady).Status != metav1.ConditionTrue {
		t.Fatalf("err %v, creates %d, Ready %+v; want nil, 0, True", err, creates, condition(t, got, accessv1.ConditionReady))
	}
}

func TestTargetInADeniedNamespace(t *testing.T) {
	for _, ns := range []string{"openbao", "kube-system", "flux-system", "guacamole"} {
		tg := target(true, 1, "1h")
		tg.Namespace = ns
		got, creates, _, err := targetCase(t, tg, nil)
		for _, typ := range []string{accessv1.ConditionServiceAccountReady, accessv1.ConditionReady} {
			if cond := condition(t, got, typ); err != nil || creates != 0 || cond.Status != metav1.ConditionFalse || cond.Reason != "NamespaceNotAllowed" {
				t.Errorf("namespace %s, condition %s: %+v, err %v, creates %d; want False NamespaceNotAllowed, no create", ns, typ, cond, err, creates)
			}
		}
	}
}

func TestTargetWhoseServiceAccountCannotBeCreated(t *testing.T) {
	got, creates, _, err := targetCase(t, target(true, 1, "1h"), errors.New("forbidden"))
	if cond := condition(t, got, accessv1.ConditionReady); err == nil || creates != 1 || cond.Status != metav1.ConditionFalse || cond.Reason != "ServiceAccountWriteFailed" {
		t.Fatalf("err %v, creates %d, Ready %+v; want an error and False ServiceAccountWriteFailed", err, creates, cond)
	}
}
