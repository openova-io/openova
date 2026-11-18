package operations

import (
	"context"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/apis/v1alpha1"
)

// DefaultRematchSchedule is the time of the daily rematch runs, in UTC: the default of
// the chart value rematch.schedule and of the flag --rematch-schedule.
const DefaultRematchSchedule = "30 3 * * *"

// Rematcher creates, at each time of its schedule, one MachineRun of kind rematch for
// every ScanPolicy that has the profile vulnerability, is valid and is not suspended. A
// rematch run touches no machine: it matches the newest stored bill of materials of each
// machine against the database again, so a new advisory reaches the reports within a day
// of the database's update. It holds one timer, set to the next time of the schedule;
// nothing reads state in a loop. It runs on the leader only.
type Rematcher struct {
	Client   client.Client
	Schedule Schedule
	Text     string // the schedule as written; it goes into spec.reason
	Now      func() time.Time
}

// RematchRunName is the name of the rematch run of one policy and one time of the
// schedule. The same policy and time give the same name, so a time is never run twice,
// and the name differs from the name of the policy's scan run of the same time.
func RematchRunName(policy, uid string, slot time.Time) string {
	return RunName(policy, uid+"/"+v1alpha1.RunKindRematch, slot)
}

// Start waits for each time of the schedule and fires it, until ctx ends. A firing that
// fails is logged; the next time of the schedule comes all the same.
func (r *Rematcher) Start(ctx context.Context) error {
	log := ctrl.LoggerFrom(ctx).WithName("rematch")
	for {
		now := r.now()
		next := r.Schedule.Next(now)
		if next.IsZero() {
			return fmt.Errorf("the rematch schedule %q has no time in the next five years", r.Text)
		}
		timer := time.NewTimer(next.Sub(now))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		if err := r.Fire(ctx, next); err != nil {
			log.Error(err, "the rematch runs of a scheduled time were not all created", "scheduledFor", next.Format(time.RFC3339))
		}
	}
}

func (r *Rematcher) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Fire creates the rematch runs of one time of the schedule. A run that exists already
// is the run of that time and is left as it is.
func (r *Rematcher) Fire(ctx context.Context, slot time.Time) error {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(v1alpha1.ScanPolicyKind.GroupVersion().WithKind(v1alpha1.ScanPolicyKind.Kind + "List"))
	if err := r.Client.List(ctx, list); err != nil {
		return fmt.Errorf("list ScanPolicies: %w", err)
	}
	var errs []error
	for i := range list.Items {
		p, err := v1alpha1.ScanPolicyFromUnstructured(&list.Items[i])
		if err != nil || p.Spec.Suspended || !NeedsMatch(p) {
			continue
		}
		if _, err := PlanOf(p); err != nil {
			continue
		}
		run := &v1alpha1.MachineRun{
			ObjectMeta: metav1.ObjectMeta{Name: RematchRunName(p.Name, string(p.UID), slot), Namespace: p.Namespace},
			Spec: v1alpha1.MachineRunSpec{
				Kind:        v1alpha1.RunKindRematch,
				PolicyRef:   v1alpha1.PolicyRef{Kind: v1alpha1.ScanPolicyKind.Kind, Name: p.Name},
				RequestedBy: v1alpha1.RequestedByController,
				Reason:      "rematch schedule " + r.Text,
			},
		}
		obj, err := v1alpha1.MachineRunToUnstructured(run)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		unstructured.RemoveNestedField(obj.Object, "status")
		if err := r.Client.Create(ctx, obj); err != nil && !apierrors.IsAlreadyExists(err) {
			errs = append(errs, fmt.Errorf("create MachineRun %s/%s: %w", run.Namespace, run.Name, err))
		}
	}
	return errors.Join(errs...)
}
