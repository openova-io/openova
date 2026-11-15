package operations

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"reflect"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/apis/v1alpha1"
)

// PlatformNamespace is the one namespace in which a policy may select the Machines of
// other namespaces with spec.namespaces.
const PlatformNamespace = "catalyst-infra"

// ConditionReady is the condition of a ScanPolicy. Its reasons follow.
const (
	ConditionReady    = "Ready"
	ReasonScheduled   = "Scheduled"
	ReasonNoSchedule  = "NoSchedule"
	ReasonSuspended   = "Suspended"
	ReasonInvalidSpec = "InvalidSpec"
)

// Scheduler creates one MachineRun for every time of a ScanPolicy's schedule. It keeps
// the next time in status.nextScheduleAt and asks to be woken exactly then.
type Scheduler struct {
	Client client.Client
	Now    func() time.Time
}

// SetupWithManager registers the controller: one reconcile when a ScanPolicy is created
// or its spec changes, and one at each time the reconcile asked for.
func (s *Scheduler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("scan-schedule").
		For(newObject(v1alpha1.ScanPolicyKind), builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(s)
}

// PolicyPlan is a ScanPolicy read into the form the controllers work with.
type PolicyPlan struct {
	Schedule    *Schedule // nil when spec.schedule is empty
	Jitter      time.Duration
	MaxParallel int
	Namespaces  []string
}

// PlanOf checks a ScanPolicy and reads its schedule, jitter, parallelism and namespaces.
func PlanOf(p *v1alpha1.ScanPolicy) (PolicyPlan, error) {
	plan := PolicyPlan{MaxParallel: v1alpha1.DefaultMaxParallel, Namespaces: []string{p.Namespace}}
	if p.Spec.MaxParallel != nil {
		plan.MaxParallel = int(*p.Spec.MaxParallel)
	}
	if plan.MaxParallel < 1 {
		return plan, fmt.Errorf("spec.maxParallel must be at least 1")
	}
	jitter := p.Spec.Jitter
	if jitter == "" {
		jitter = v1alpha1.DefaultJitter
	}
	var err error
	if plan.Jitter, err = time.ParseDuration(jitter); err != nil || plan.Jitter < 0 {
		return plan, fmt.Errorf("spec.jitter %q is not a duration", p.Spec.Jitter)
	}
	if len(p.Spec.Namespaces) > 0 {
		if p.Namespace != PlatformNamespace {
			return plan, fmt.Errorf("spec.namespaces is honoured only on a policy in the namespace %s", PlatformNamespace)
		}
		plan.Namespaces = p.Spec.Namespaces
	}
	if _, err := metav1.LabelSelectorAsSelector(&p.Spec.MachineSelector); err != nil {
		return plan, fmt.Errorf("spec.machineSelector: %w", err)
	}
	for _, profile := range p.Spec.Profiles {
		if profile == v1alpha1.ProfileHardening && p.Spec.HardeningBaselineRef == nil {
			return plan, fmt.Errorf("spec.hardeningBaselineRef is required when spec.profiles contains hardening")
		}
	}
	if strings.TrimSpace(p.Spec.Schedule) != "" {
		sched, err := ParseSchedule(p.Spec.Schedule)
		if err != nil {
			return plan, err
		}
		plan.Schedule = &sched
	}
	return plan, nil
}

// Reconcile creates the run of a scheduled time that has come and sets the next one.
func (s *Scheduler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	u := newObject(v1alpha1.ScanPolicyKind)
	if err := s.Client.Get(ctx, req.NamespacedName, u); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	p, err := v1alpha1.ScanPolicyFromUnstructured(u)
	if err != nil {
		return ctrl.Result{}, err
	}
	before := p.Status
	before.Conditions = append([]metav1.Condition(nil), p.Status.Conditions...)
	plan, err := PlanOf(p)
	switch {
	case err != nil:
		return ctrl.Result{}, s.write(ctx, p, before, nil, false, ReasonInvalidSpec, err.Error())
	case p.Spec.Suspended:
		return ctrl.Result{}, s.write(ctx, p, before, nil, false, ReasonSuspended, "spec.suspended is true: no run is created")
	case plan.Schedule == nil:
		return ctrl.Result{}, s.write(ctx, p, before, nil, true, ReasonNoSchedule, "spec.schedule is empty: no run is created from a schedule")
	}
	now := s.now()
	at := metav1.NewTime(plan.Schedule.Next(now))
	if at.IsZero() {
		return ctrl.Result{}, s.write(ctx, p, before, nil, false, ReasonInvalidSpec, fmt.Sprintf("schedule %q has no time in the next five years", p.Spec.Schedule))
	}
	next := p.Status.NextScheduleAt
	ready := meta.FindStatusCondition(p.Status.Conditions, ConditionReady)
	switch {
	case next == nil || ready == nil || ready.ObservedGeneration != p.Generation:
		// A new policy or a changed spec: the first run is at the next time of the schedule.
	case now.Before(next.Time):
		return ctrl.Result{RequeueAfter: next.Sub(now)}, nil
	default:
		name, err := s.createRun(ctx, p, next.Time)
		if err != nil {
			return ctrl.Result{}, err
		}
		p.Status.LastRunName = name
	}
	if err := s.write(ctx, p, before, &at, true, ReasonScheduled, "the next run is created at status.nextScheduleAt"); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: at.Sub(now)}, nil
}

func (s *Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// RunName is the name of the run of one scheduled time: the policy name, a dash and a
// ULID in lower case whose time part is the scheduled time and whose other 80 bits come
// from the policy's identifier and that time. The same policy and time give the same
// name, so a time is never run twice.
func RunName(policy, uid string, slot time.Time) string {
	const alphabet = "0123456789abcdefghjkmnpqrstvwxyz"
	sum := sha256.Sum256([]byte(uid + "/" + slot.UTC().Format(time.RFC3339)))
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], uint64(slot.UnixMilli())<<16) // #nosec G115 -- times after 1970
	copy(b[6:], sum[:10])
	hi, lo := binary.BigEndian.Uint64(b[:8]), binary.BigEndian.Uint64(b[8:])
	out := make([]byte, 26)
	for i := 25; i >= 0; i-- {
		out[i] = alphabet[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return policy + "-" + string(out)
}

// createRun creates the MachineRun of one scheduled time. A run that exists already is
// the run of that time and is left as it is.
func (s *Scheduler) createRun(ctx context.Context, p *v1alpha1.ScanPolicy, slot time.Time) (string, error) {
	run := &v1alpha1.MachineRun{
		ObjectMeta: metav1.ObjectMeta{
			Name: RunName(p.Name, string(p.UID), slot), Namespace: p.Namespace,
			Annotations: map[string]string{v1alpha1.AnnotationScheduledFor: slot.UTC().Format(time.RFC3339)},
		},
		Spec: v1alpha1.MachineRunSpec{
			Kind:        v1alpha1.RunKindScan,
			PolicyRef:   v1alpha1.PolicyRef{Kind: v1alpha1.ScanPolicyKind.Kind, Name: p.Name},
			RequestedBy: v1alpha1.RequestedByController,
			Reason:      "schedule " + p.Spec.Schedule,
		},
	}
	obj, err := v1alpha1.MachineRunToUnstructured(run)
	if err != nil {
		return "", err
	}
	unstructured.RemoveNestedField(obj.Object, "status")
	if err := s.Client.Create(ctx, obj); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("create MachineRun %s/%s: %w", run.Namespace, run.Name, err)
	}
	return run.Name, nil
}

// write sets the condition Ready and status.nextScheduleAt, and sends the status only
// when it differs from before.
func (s *Scheduler) write(ctx context.Context, p *v1alpha1.ScanPolicy, before v1alpha1.ScanPolicyStatus, next *metav1.Time, ready bool, reason, message string) error {
	status := metav1.ConditionFalse
	if ready {
		status = metav1.ConditionTrue
	}
	p.Status.NextScheduleAt = next
	meta.SetStatusCondition(&p.Status.Conditions, metav1.Condition{Type: ConditionReady, Status: status, Reason: reason, Message: message, ObservedGeneration: p.Generation})
	if reflect.DeepEqual(before, p.Status) {
		return nil
	}
	obj, err := v1alpha1.ScanPolicyToUnstructured(p)
	if err != nil {
		return err
	}
	if err := s.Client.Status().Update(ctx, obj); err != nil {
		return fmt.Errorf("update the status of ScanPolicy %s/%s: %w", p.Namespace, p.Name, err)
	}
	return nil
}

func newObject(gvk schema.GroupVersionKind) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	return u
}
