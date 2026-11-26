// Package controller holds the reconcilers of the access kinds (ADR-0033).
package controller

import (
	"context"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	accessv1 "github.com/openova-io/openova/core/controllers/access/api/v1alpha1"
)

// SessionServiceAccount is the service account of every session pod
// (ADR-0033 decision A8; it carries no token).
const SessionServiceAccount = "access-session"

// Reasons of the target conditions.
const (
	reasonNamespaceNotAllowed = "NamespaceNotAllowed"
	reasonWriteFailed         = "ServiceAccountWriteFailed"
	reasonPresent             = "Present"
)

// TargetReconciler checks an AccessTarget and makes sure that the service
// account of its session pods exists. The session pods of kind pod run in
// SessionNamespace (decision A10). It writes only the status of a target.
type TargetReconciler struct {
	client.Client
	// Reader reads the service account past the cache, so the controller
	// needs get on service accounts and neither list nor watch.
	Reader           client.Reader
	SessionNamespace string
	// DeniedNamespaces are the namespaces a target of kind pod may not be in
	// (chart value access.session.deniedTargetNamespaces).
	DeniedNamespaces []string
}

// Reconcile sets the conditions ServiceAccountReady and Ready of one target.
func (r *TargetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var t accessv1.AccessTarget
	if err := r.Get(ctx, req.NamespacedName, &t); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	before := t.DeepCopy().Status
	var failed error
	status, reason, message := metav1.ConditionTrue, reasonPresent, fmt.Sprintf("service account %s/%s exists", r.SessionNamespace, SessionServiceAccount)
	switch {
	case slices.Contains(r.DeniedNamespaces, t.Namespace):
		status, reason = metav1.ConditionFalse, reasonNamespaceNotAllowed
		message = fmt.Sprintf("a target of kind %s may not be in namespace %s (access.session.deniedTargetNamespaces)", t.Spec.Kind, t.Namespace)
	default:
		if failed = r.ensureServiceAccount(ctx); failed != nil {
			status, reason, message = metav1.ConditionFalse, reasonWriteFailed, failed.Error()
		}
	}
	for _, typ := range []string{accessv1.ConditionServiceAccountReady, accessv1.ConditionReady} {
		meta.SetStatusCondition(&t.Status.Conditions, metav1.Condition{
			Type: typ, Status: status, Reason: reason, Message: message, ObservedGeneration: t.Generation,
		})
	}
	t.Status.ObservedGeneration = t.Generation
	if !equality.Semantic.DeepEqual(before, t.Status) {
		if err := r.Status().Update(ctx, &t); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, failed
}

// ensureServiceAccount creates the session service account when it is absent.
func (r *TargetReconciler) ensureServiceAccount(ctx context.Context) error {
	key := types.NamespacedName{Namespace: r.SessionNamespace, Name: SessionServiceAccount}
	var sa corev1.ServiceAccount
	err := r.Reader.Get(ctx, key, &sa)
	if err == nil || !apierrors.IsNotFound(err) {
		return err
	}
	no := false
	sa = corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace, Labels: map[string]string{
			"app.kubernetes.io/managed-by": "access-controller",
		}},
		AutomountServiceAccountToken: &no,
	}
	if err := r.Create(ctx, &sa); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create service account %s: %w", key, err)
	}
	return nil
}

// SetupWithManager registers the reconciler.
func (r *TargetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).Named("accesstarget").For(&accessv1.AccessTarget{}).Complete(r)
}
