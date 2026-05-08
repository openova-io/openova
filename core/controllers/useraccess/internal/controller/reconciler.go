// reconciler.go — UserAccessReconciler.Reconcile.
//
// Steady-state algorithm:
//
//   1. Fetch the UserAccess CR (cluster-scoped). Not-found → no-op
//      (garbage-collected by ownerRef cascade).
//   2. ParseSpec(). On invalid spec → status.phase=Failed + Condition
//      Ready=False, Reason=Invalid, Message=<text>. Stop.
//   3. Build the desired RoleBinding / ClusterRoleBinding set:
//        - For each AppGrant entry:
//            - if IsClusterWide() → 1 ClusterRoleBinding per
//              (UserAccess × app × role) tuple, named
//              `useraccess-<ua>-<app>-<role>`
//            - else → 1 RoleBinding per (UserAccess × app × role × ns)
//              tuple in each MaterializedNamespaces() entry.
//        - All carry ownerReferences to the UserAccess CR.
//   4. Diff desired vs live (List by label-selector
//      `access.openova.io/useraccess=<ua-name>` so we capture
//      orphaned bindings from past reconciles).
//        - Create what's missing.
//        - Update what's drifted (drift = roleRef changed OR subjects
//          set changed) — surface a Condition Drift=True for the
//          observability hop, then revert.
//        - Delete the orphan set (objects with matching label that are
//          NOT in the desired set — happens when the operator removes
//          a namespace from spec.applications[].namespaces).
//   5. Update status:
//        - phase: Pending → Active on first successful pass; Failed
//          on hard error.
//        - roleBindings[]: list of (kind, namespace, name, uid).
//        - rolebindingsCreated: int (total count).
//        - conditions: Ready, Drift (if any), Synced.
//        - observedGeneration = metadata.generation.
//
// Idempotency: re-reconciling on a steady state computes the same
// desired set and finds zero diffs → 0 K8s writes.
//
// Drift detection: a RoleBinding the controller created carries
// label `openova.io/managed-by=useraccess-controller`. The reconciler
// reads it back, compares roleRef and Subjects to desired, and on
// mismatch emits a Drift condition + restores the desired shape on
// the same pass (single-pass convergence).
//
// Cascading delete: every materialized binding owns a UID-pinned
// metav1.OwnerReference back to the UserAccess CR. K8s
// garbage-collects them automatically when the UserAccess CR is
// deleted — no finalizer needed for the binding lifecycle. The
// controller does NOT take a finalizer on the UserAccess CR itself
// (no out-of-K8s side effects to clean up).

package controller

import (
	"context"
	"fmt"
	"sort"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// UserAccessReconciler is the controller-runtime reconciler.
//
// The reconciler uses controller-runtime's client.Client (which
// transparently dispatches to a typed cached client for
// rbac.authorization.k8s.io/v1 RoleBinding/ClusterRoleBinding and to
// the unstructured cached client for our cluster-scoped UserAccess
// resource registered via SetupWithManager).
type UserAccessReconciler struct {
	Client client.Client
	Scheme *runtime.Scheme
}

// Condition reasons. Keep the strings stable — UI code (slice C5
// follow-up + EPIC-3 #1098 admin pages) reads them.
const (
	condReady        = "Ready"
	condSynced       = "Synced"
	condDrift        = "Drift"
	reasonInvalid    = "Invalid"
	reasonReconciled = "Reconciled"
	reasonDriftFixed = "DriftRestored"
)

// Phase strings — surfaced on .status.phase.
const (
	phasePending = "Pending"
	phaseActive  = "Active"
	phaseFailed  = "Failed"
)

// +kubebuilder:rbac:groups=access.openova.io,resources=useraccesses,verbs=get;list;watch
// +kubebuilder:rbac:groups=access.openova.io,resources=useraccesses/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings;clusterrolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,verbs=get
//
// Reconcile is the controller-runtime entrypoint. Exposed so the
// controller can be wired both from the leader-elected manager (cmd/
// main.go) and from tests using a fake client (reconciler_test.go).
func (r *UserAccessReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("useraccess", req.Name)

	ua := &unstructured.Unstructured{}
	ua.SetGroupVersionKind(UserAccessGVK())
	if err := r.Client.Get(ctx, types.NamespacedName{Name: req.Name}, ua); err != nil {
		if apierrors.IsNotFound(err) {
			// UserAccess deleted — owner refs garbage-collect bindings.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get useraccess: %w", err)
	}

	spec, vmsg := ParseSpec(ua)
	if vmsg != "" {
		logger.Info("invalid useraccess spec", "msg", vmsg)
		// Status update is best-effort — a Conflict means a concurrent
		// edit, the next reconcile loop will retry on the next event.
		_ = r.writeFailedStatus(ctx, ua, vmsg)
		return ctrl.Result{}, nil
	}

	// ── Slice T3: tier-driven scope auto-injection (annotation-driven,
	//    generic across tiers per docs/EPICS-1-6-unified-design.md §6.2).
	autoInject, err := r.reconcileTierAutoInject(ctx, ua, spec)
	if err != nil {
		// Errors here surface as condition + status; we still attempt
		// the binding emission step so a transient annotation read
		// failure doesn't block grant materialization.
		logger.Info("tier auto-inject failed (continuing)", "err", err)
	}
	// If auto-inject patched the CR, refresh the parsed spec so the
	// emission path sees the merged scope set on the same pass. Without
	// this, the post-patch ResourceVersion and the auto-injected scope
	// would only be visible on the NEXT reconcile.
	if len(autoInject.Added) > 0 {
		// The Patch returned, rewriting spec.scopes to the combined
		// list. Re-fetch + re-parse so subsequent steps see the merged
		// view.
		fresh := &unstructured.Unstructured{}
		fresh.SetGroupVersionKind(UserAccessGVK())
		if getErr := r.Client.Get(ctx, types.NamespacedName{Name: req.Name}, fresh); getErr == nil {
			ua = fresh
			if reparsed, vmsg2 := ParseSpec(ua); vmsg2 == "" {
				spec = reparsed
			}
		}
	}

	// ── C5-followup: tier-aware emission (when spec.tierRoleRef set).
	tierRBs, tierCRBs, tierRes, tierErr := r.computeTierBindings(ctx, ua, spec)
	if tierErr != nil {
		_ = r.writeFailedStatus(ctx, ua, tierErr.Error())
		return ctrl.Result{}, tierErr
	}

	// Build the desired sets. When the tier path is in use, prefer it
	// (per the brief: tier path wins; legacy applications[] ignored
	// with a status note).
	var desiredRBs []rbacv1.RoleBinding
	var desiredCRBs []rbacv1.ClusterRoleBinding
	if tierRes.Used {
		desiredRBs = tierRBs
		desiredCRBs = tierCRBs
	} else {
		desiredRBs = desiredRoleBindings(ua, spec)
		desiredCRBs = desiredClusterRoleBindings(ua, spec)
	}

	// Read live sets via label selector.
	liveRBs, err := r.listLiveRoleBindings(ctx, ua.GetName())
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("list live rolebindings: %w", err)
	}
	liveCRBs, err := r.listLiveClusterRoleBindings(ctx, ua.GetName())
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("list live clusterrolebindings: %w", err)
	}

	// Apply diff (create, update, delete orphans).
	driftSeen := false
	var lastErr error
	for _, want := range desiredRBs {
		drifted, err := r.upsertRoleBinding(ctx, want, liveRBs)
		if err != nil {
			lastErr = err
		}
		if drifted {
			driftSeen = true
		}
	}
	for _, want := range desiredCRBs {
		drifted, err := r.upsertClusterRoleBinding(ctx, want, liveCRBs)
		if err != nil {
			lastErr = err
		}
		if drifted {
			driftSeen = true
		}
	}
	if err := r.deleteOrphanRoleBindings(ctx, desiredRBs, liveRBs); err != nil {
		lastErr = err
	}
	if err := r.deleteOrphanClusterRoleBindings(ctx, desiredCRBs, liveCRBs); err != nil {
		lastErr = err
	}

	if lastErr != nil {
		_ = r.writeFailedStatus(ctx, ua, lastErr.Error())
		return ctrl.Result{}, lastErr
	}

	// Status: Pending → Active. Drift surfaces as a Condition the UI
	// can render but does not block Active (the controller restored
	// the desired state in the same pass). New conditions:
	// EnforcedScopeApplied, TierResolved, BindingsReconciled.
	if err := r.writeActiveStatusWithTier(ctx, ua, desiredRBs, desiredCRBs, driftSeen, autoInject, tierRes); err != nil {
		// Non-fatal: log and let the next reconcile retry.
		logger.V(1).Info("status update failed (will retry)", "err", err)
	}
	return ctrl.Result{}, nil
}

// SetupWithManager wires the reconciler into a controller-runtime
// Manager. Watches UserAccess (cluster-scoped) and owns RoleBinding +
// ClusterRoleBinding so direct edits (drift) trigger a reconcile of
// the parent UserAccess via ownerRef indexing — same pattern Owns()
// gives for typed CRs.
func (r *UserAccessReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ua := &unstructured.Unstructured{}
	ua.SetGroupVersionKind(UserAccessGVK())
	rb := &rbacv1.RoleBinding{}
	crb := &rbacv1.ClusterRoleBinding{}
	return ctrl.NewControllerManagedBy(mgr).
		For(ua).
		Owns(rb).
		Owns(crb).
		Named("useraccess-controller").
		Complete(r)
}

// listLiveRoleBindings reads every RoleBinding tagged for the given
// UserAccess across all namespaces. The label-selector LIST is one
// call regardless of namespace count — controller-runtime's cached
// client serves it from the informer cache.
func (r *UserAccessReconciler) listLiveRoleBindings(ctx context.Context, uaName string) ([]rbacv1.RoleBinding, error) {
	var list rbacv1.RoleBindingList
	sel := client.MatchingLabels{LabelUserAccessName: uaName}
	if err := r.Client.List(ctx, &list, sel); err != nil {
		return nil, err
	}
	return list.Items, nil
}

func (r *UserAccessReconciler) listLiveClusterRoleBindings(ctx context.Context, uaName string) ([]rbacv1.ClusterRoleBinding, error) {
	var list rbacv1.ClusterRoleBindingList
	sel := client.MatchingLabels{LabelUserAccessName: uaName}
	if err := r.Client.List(ctx, &list, sel); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// upsertRoleBinding creates the binding if missing or restores its
// shape if drifted. Returns drift=true when a hand-mutation was
// detected and reverted.
func (r *UserAccessReconciler) upsertRoleBinding(ctx context.Context, want rbacv1.RoleBinding, live []rbacv1.RoleBinding) (drifted bool, err error) {
	for _, l := range live {
		if l.Namespace == want.Namespace && l.Name == want.Name {
			if !roleBindingMatches(l, want) {
				// Restore desired shape via a full replace — this is
				// what reverts a hand-edit. Preserve the ResourceVersion
				// to surface concurrent edits as 409 (next reconcile
				// retries from a fresh GET).
				want.ResourceVersion = l.ResourceVersion
				want.UID = l.UID
				if err := r.Client.Update(ctx, &want); err != nil {
					return true, fmt.Errorf("rolebinding %s/%s update: %w", want.Namespace, want.Name, err)
				}
				return true, nil
			}
			return false, nil
		}
	}
	if err := r.Client.Create(ctx, &want); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Race with another reconcile or stale cache — re-read
			// and update. The next loop will converge.
			return false, nil
		}
		return false, fmt.Errorf("rolebinding %s/%s create: %w", want.Namespace, want.Name, err)
	}
	return false, nil
}

func (r *UserAccessReconciler) upsertClusterRoleBinding(ctx context.Context, want rbacv1.ClusterRoleBinding, live []rbacv1.ClusterRoleBinding) (drifted bool, err error) {
	for _, l := range live {
		if l.Name == want.Name {
			if !clusterRoleBindingMatches(l, want) {
				want.ResourceVersion = l.ResourceVersion
				want.UID = l.UID
				if err := r.Client.Update(ctx, &want); err != nil {
					return true, fmt.Errorf("clusterrolebinding %s update: %w", want.Name, err)
				}
				return true, nil
			}
			return false, nil
		}
	}
	if err := r.Client.Create(ctx, &want); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return false, nil
		}
		return false, fmt.Errorf("clusterrolebinding %s create: %w", want.Name, err)
	}
	return false, nil
}

// deleteOrphanRoleBindings removes label-tagged bindings that are NOT
// in the desired set. Called after upsert so the desired set is
// stable. Idempotent: re-running on a clean orphan-free state is a
// no-op.
func (r *UserAccessReconciler) deleteOrphanRoleBindings(ctx context.Context, desired []rbacv1.RoleBinding, live []rbacv1.RoleBinding) error {
	want := map[string]struct{}{}
	for _, w := range desired {
		want[w.Namespace+"/"+w.Name] = struct{}{}
	}
	for i := range live {
		key := live[i].Namespace + "/" + live[i].Name
		if _, ok := want[key]; ok {
			continue
		}
		if err := r.Client.Delete(ctx, &live[i]); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("rolebinding %s delete: %w", key, err)
		}
	}
	return nil
}

func (r *UserAccessReconciler) deleteOrphanClusterRoleBindings(ctx context.Context, desired []rbacv1.ClusterRoleBinding, live []rbacv1.ClusterRoleBinding) error {
	want := map[string]struct{}{}
	for _, w := range desired {
		want[w.Name] = struct{}{}
	}
	for i := range live {
		if _, ok := want[live[i].Name]; ok {
			continue
		}
		if err := r.Client.Delete(ctx, &live[i]); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("clusterrolebinding %s delete: %w", live[i].Name, err)
		}
	}
	return nil
}

// roleBindingMatches reports whether the live shape is byte-equal to
// the desired shape on the fields the controller owns: roleRef and
// subjects. Labels and annotations are not compared because external
// mutators (e.g. Kyverno auto-labelling) may add fields the controller
// does not need to revert.
func roleBindingMatches(live, want rbacv1.RoleBinding) bool {
	if live.RoleRef != want.RoleRef {
		return false
	}
	return subjectsEqual(live.Subjects, want.Subjects)
}

func clusterRoleBindingMatches(live, want rbacv1.ClusterRoleBinding) bool {
	if live.RoleRef != want.RoleRef {
		return false
	}
	return subjectsEqual(live.Subjects, want.Subjects)
}

// subjectsEqual reports whether two subject slices represent the same
// set, regardless of order. Subjects are matched by (Kind, APIGroup,
// Name, Namespace).
func subjectsEqual(a, b []rbacv1.Subject) bool {
	if len(a) != len(b) {
		return false
	}
	keyOf := func(s rbacv1.Subject) string {
		return s.Kind + "|" + s.APIGroup + "|" + s.Namespace + "|" + s.Name
	}
	ka := make([]string, len(a))
	kb := make([]string, len(b))
	for i := range a {
		ka[i] = keyOf(a[i])
	}
	for i := range b {
		kb[i] = keyOf(b[i])
	}
	sort.Strings(ka)
	sort.Strings(kb)
	for i := range ka {
		if ka[i] != kb[i] {
			return false
		}
	}
	return true
}

// writeFailedStatus posts phase=Failed + Ready=False on the UA. Best-
// effort: a Conflict here is normal (concurrent edit) and the next
// reconcile retries.
func (r *UserAccessReconciler) writeFailedStatus(ctx context.Context, ua *unstructured.Unstructured, msg string) error {
	patch := newStatusPatch(ua, phaseFailed, 0, false, false, msg, ua.GetGeneration())
	return r.Client.Status().Patch(ctx, ua, client.RawPatch(types.MergePatchType, patch))
}

// writeActiveStatus posts phase=Active + Ready=True + Synced=True (and
// Drift=True if any was seen this pass + restored).
func (r *UserAccessReconciler) writeActiveStatus(ctx context.Context, ua *unstructured.Unstructured, rbs []rbacv1.RoleBinding, crbs []rbacv1.ClusterRoleBinding, drift bool) error {
	count := len(rbs) + len(crbs)
	patch := newStatusPatch(ua, phaseActive, count, true, drift, "", ua.GetGeneration())
	return r.Client.Status().Patch(ctx, ua, client.RawPatch(types.MergePatchType, patch))
}

// writeActiveStatusWithTier extends writeActiveStatus to include the
// EPIC-3 slice T3 + C5-followup conditions (EnforcedScopeApplied,
// TierResolved, BindingsReconciled). The tier inputs may be a no-op
// (auto.Tier == "" + tier.Used == false) — in which case the new
// conditions are simply omitted.
func (r *UserAccessReconciler) writeActiveStatusWithTier(
	ctx context.Context,
	ua *unstructured.Unstructured,
	rbs []rbacv1.RoleBinding,
	crbs []rbacv1.ClusterRoleBinding,
	drift bool,
	auto tierAutoInjectResult,
	tier tierEmissionResult,
) error {
	count := len(rbs) + len(crbs)
	patch := newStatusPatchWithTier(ua, phaseActive, count, true, drift, "", ua.GetGeneration(), &auto, &tier)
	return r.Client.Status().Patch(ctx, ua, client.RawPatch(types.MergePatchType, patch))
}

// metaConditions exposes a no-op metav1 reference so the import is
// retained for documentation generation.
var _ = metav1.Time{}
