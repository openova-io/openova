package controller

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	accessv1 "github.com/openova-io/openova/core/controllers/access/api/v1alpha1"
	"github.com/openova-io/openova/core/controllers/access/internal/events"
	"github.com/openova-io/openova/core/controllers/pkg/natsbus"
)

// sendTimeout bounds the wait for the acknowledgement of one event.
const sendTimeout = 10 * time.Second

// GrantReconciler computes the phase of an AccessGrant and writes it to its
// status. When a grant enters Active it sends grant_activated, and when it
// leaves Active grant_ended, before it writes the status: a send that fails
// returns the error, the reconcile is retried and the status keeps the phase
// it had. It requeues itself for the next edge of the window; it has no timer.
type GrantReconciler struct {
	client.Client
	Events events.Sender
	Source string // the source attribute of the envelopes, events.Source
	Now    func() time.Time

	mu sync.Mutex
	// active holds the grants this process last wrote as Active, so that the
	// reconcile of a deleted grant can send grant_ended with its subject. A
	// grant deleted while no controller runs is not seen.
	active map[types.NamespacedName]events.GrantEnded
	// unsettled holds the envelope of a transition whose status is not written
	// yet: a retry sends the same id again, and the stream drops the copy.
	unsettled map[types.NamespacedName]sent
}

type sent struct {
	subject string
	ce      natsbus.CloudEvent
	grant   events.GrantEnded // names the grant for the grant_ended that closes it
}

// Phase computes the phase of a grant by the rules of the access design, in
// their order: Invalid, Suspended, Expired, Pending, Scheduled, Active. target
// is nil when the grant's target does not exist. reason is set for Invalid.
func Phase(g *accessv1.AccessGrant, target *accessv1.AccessTarget, now time.Time) (phase, reason string, err error) {
	if target == nil {
		return accessv1.PhaseInvalid, accessv1.ReasonTargetNotFound, nil
	}
	longest, err := target.MaxDuration()
	if err != nil {
		return "", "", err
	}
	self, twice, seen := false, false, map[string]bool{}
	for _, a := range g.Spec.Approvals {
		self = self || a.By == g.Spec.Subject.Email
		twice = twice || seen[a.By]
		seen[a.By] = true
	}
	w := g.Spec.Window
	switch {
	case w.NotAfter.Sub(w.NotBefore.Time) > longest:
		return accessv1.PhaseInvalid, accessv1.ReasonWindowTooLong, nil
	case self:
		return accessv1.PhaseInvalid, accessv1.ReasonSelfApproval, nil
	case twice:
		return accessv1.PhaseInvalid, accessv1.ReasonDuplicateApprover, nil
	case target.Spec.Kind != accessv1.KindPod || g.Spec.Account != "":
		// A target of kind pod offers no account; it is the only kind.
		return accessv1.PhaseInvalid, accessv1.ReasonAccountNotAllowed, nil
	case g.Spec.Suspended:
		return accessv1.PhaseSuspended, "", nil
	case !now.Before(w.NotAfter.Time):
		return accessv1.PhaseExpired, "", nil
	case target.ApprovalRequired() && len(g.Spec.Approvals) < target.ApprovalCount():
		return accessv1.PhasePending, "", nil
	case now.Before(w.NotBefore.Time):
		return accessv1.PhaseScheduled, "", nil
	}
	return accessv1.PhaseActive, "", nil
}

// requeueAfter is the time to the next edge of the window that can change the
// phase, or 0 when time changes it no more.
func requeueAfter(g *accessv1.AccessGrant, phase string, now time.Time) time.Duration {
	if phase != accessv1.PhasePending && phase != accessv1.PhaseScheduled && phase != accessv1.PhaseActive {
		return 0
	}
	for _, edge := range []time.Time{g.Spec.Window.NotBefore.Time, g.Spec.Window.NotAfter.Time} {
		if now.Before(edge) {
			return edge.Sub(now)
		}
	}
	return 0
}

// Reconcile writes the phase of one grant.
func (r *GrantReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	key := req.NamespacedName
	var g accessv1.AccessGrant
	if err := r.Get(ctx, key, &g); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.gone(ctx, key)
		}
		return ctrl.Result{}, err
	}
	var target *accessv1.AccessTarget
	var t accessv1.AccessTarget
	switch err := r.Get(ctx, types.NamespacedName{Namespace: g.Namespace, Name: g.Spec.TargetRef.Name}, &t); {
	case err == nil:
		target = &t
	case !apierrors.IsNotFound(err):
		return ctrl.Result{}, err
	}
	now := r.Now()
	phase, reason, err := Phase(&g, target, now)
	if err != nil {
		return ctrl.Result{}, err
	}
	next, was := g.DeepCopy().Status, g.Status.Phase
	next.Phase, next.Reason, next.ObservedGeneration = phase, reason, g.Generation
	at := metav1.NewTime(now)
	// The stream holds what the status says, or what an envelope sent before a failed status write announced.
	announced, err := r.resend(ctx, key, was == accessv1.PhaseActive)
	if phase == accessv1.PhaseActive && !announced && err == nil {
		err = r.send(ctx, key, events.SubjectGrantActivated, activated(&g), ended(&g, ""))
	} else if phase != accessv1.PhaseActive && announced && err == nil {
		err = r.send(ctx, key, events.SubjectGrantEnded, ended(&g, strings.ToLower(phase)), ended(&g, ""))
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	switch {
	case phase == accessv1.PhaseActive && was != accessv1.PhaseActive:
		next.ActivatedAt, next.EndedAt = &at, nil
	case was == accessv1.PhaseActive && phase != accessv1.PhaseActive:
		next.EndedAt = &at
	}
	if !equality.Semantic.DeepEqual(next, g.Status) {
		g.Status = next
		if err := r.Status().Update(ctx, &g); err != nil {
			return ctrl.Result{}, err
		}
	}
	r.settle(key, phase, &g)
	return ctrl.Result{RequeueAfter: requeueAfter(&g, phase, now)}, nil
}

// resend sends an unsettled envelope again; then the stream holds what it announced.
func (r *GrantReconciler) resend(ctx context.Context, key types.NamespacedName, statusActive bool) (bool, error) {
	r.mu.Lock()
	s, ok := r.unsettled[key]
	r.mu.Unlock()
	if !ok {
		return statusActive, nil
	}
	return s.subject == events.SubjectGrantActivated, r.send(ctx, key, s.subject, nil, s.grant)
}

// gone sends grant_ended for a deleted grant whose grant_activated the stream holds open.
func (r *GrantReconciler) gone(ctx context.Context, key types.NamespacedName) error {
	r.mu.Lock()
	last, ok := r.active[key]
	if s, unsettled := r.unsettled[key]; unsettled {
		last = s.grant
	}
	r.mu.Unlock()
	if open, err := r.resend(ctx, key, ok); err != nil {
		return err
	} else if open {
		last.Reason = events.ReasonDeleted
		if err := r.send(ctx, key, events.SubjectGrantEnded, last, last); err != nil {
			return err
		}
	}
	r.mu.Lock()
	delete(r.active, key)
	delete(r.unsettled, key)
	r.mu.Unlock()
	return nil
}

// send sends one event of a grant and waits for its acknowledgement. The
// envelope of an unsettled transition with the same subject is sent again.
func (r *GrantReconciler) send(ctx context.Context, key types.NamespacedName, subject string, data any, grant events.GrantEnded) error {
	r.mu.Lock()
	if r.unsettled == nil {
		r.unsettled = map[types.NamespacedName]sent{}
	}
	s, ok := r.unsettled[key]
	r.mu.Unlock()
	if !ok || s.subject != subject {
		ce, err := events.Envelope(subject, r.Source, data)
		if err != nil {
			return err
		}
		s = sent{subject: subject, ce: ce, grant: grant}
		r.mu.Lock()
		r.unsettled[key] = s
		r.mu.Unlock()
	}
	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	if err := r.Events.SendCloudEvent(sendCtx, s.ce); err != nil {
		return fmt.Errorf("grant %s: send %s: %w", key, subject, err)
	}
	return nil
}

// settle records the phase that was written for a grant.
func (r *GrantReconciler) settle(key types.NamespacedName, phase string, g *accessv1.AccessGrant) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active == nil {
		r.active = map[types.NamespacedName]events.GrantEnded{}
	}
	delete(r.unsettled, key)
	if phase == accessv1.PhaseActive {
		r.active[key] = ended(g, "")
	} else {
		delete(r.active, key)
	}
}

func activated(g *accessv1.AccessGrant) events.GrantActivated {
	approvers := make([]string, 0, len(g.Spec.Approvals))
	for _, a := range g.Spec.Approvals {
		approvers = append(approvers, a.By)
	}
	return events.GrantActivated{
		Namespace: g.Namespace, Grant: g.Name, Target: g.Spec.TargetRef.Name, Subject: g.Spec.Subject.Email,
		Account: g.Spec.Account, Reason: g.Spec.Reason, Ticket: g.Spec.Ticket,
		NotBefore: events.Time(g.Spec.Window.NotBefore.Time), NotAfter: events.Time(g.Spec.Window.NotAfter.Time),
		Approvers: approvers,
	}
}

func ended(g *accessv1.AccessGrant, reason string) events.GrantEnded {
	return events.GrantEnded{Namespace: g.Namespace, Grant: g.Name, Subject: g.Spec.Subject.Email, Reason: reason}
}

// SetupWithManager registers the reconciler; a change of a target reconciles
// the grants that name it.
func (r *GrantReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).Named("accessgrant").
		For(&accessv1.AccessGrant{}).
		Watches(&accessv1.AccessTarget{}, handler.EnqueueRequestsFromMapFunc(r.grantsOf)).
		Complete(r)
}

// grantsOf lists the grants of a target.
func (r *GrantReconciler) grantsOf(ctx context.Context, obj client.Object) []reconcile.Request {
	var list accessv1.AccessGrantList
	if err := r.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
		log.FromContext(ctx).Error(err, "list the grants of a target", "target", client.ObjectKeyFromObject(obj))
		return nil
	}
	out := []reconcile.Request{}
	for _, g := range list.Items {
		if g.Spec.TargetRef.Name == obj.GetName() {
			out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&g)})
		}
	}
	return out
}
