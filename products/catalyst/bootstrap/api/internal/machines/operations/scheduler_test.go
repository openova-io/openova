package operations

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/apis/v1alpha1"
)

func scanPolicy(namespace string, spec map[string]interface{}) *unstructured.Unstructured {
	return object(v1alpha1.ScanPolicyKind, namespace, "nodes", map[string]interface{}{"spec": spec})
}

// The scheduler wakes at the next time of the schedule and at no other time: every wait
// it asks for is the distance from the clock to that time, so no two waits are equal
// unless the clock stood still, and nothing wakes it with a fixed period.
func TestSchedulerWakesAtTheScheduledTimeNotOnAPeriod(t *testing.T) {
	w := newWorld(t, nil, scanPolicy(ns, policySpec("0 2 * * *", "inventory")))
	if got := w.schedule(); got != time.Hour {
		t.Fatalf("at 01:00 the scheduler asked for %s, want 1h", got)
	}
	if next := w.policy(ns).Status.NextScheduleAt; next == nil || !next.Time.Equal(at("2026-12-01 02:00")) {
		t.Fatalf("status.nextScheduleAt = %v, want 02:00", next)
	}
	w.now = at("2026-12-01 01:30")
	if got := w.schedule(); got != 30*time.Minute {
		t.Fatalf("at 01:30 the scheduler asked for %s, want 30m", got)
	}
	if n := len(w.list(v1alpha1.MachineRunKind, ns)); n != 0 {
		t.Fatalf("%d runs exist before the scheduled time", n)
	}
	w.now = at("2026-12-01 02:00").Add(5 * time.Second)
	if got := w.schedule(); got != 24*time.Hour-5*time.Second {
		t.Fatalf("after the run the scheduler asked for %s, want 23h59m55s", got)
	}
	runs := w.list(v1alpha1.MachineRunKind, ns)
	if len(runs) != 1 {
		t.Fatalf("%d runs exist after the scheduled time, want 1", len(runs))
	}
	run, err := v1alpha1.MachineRunFromUnstructured(&runs[0])
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^nodes-[0-9a-hjkmnp-tv-z]{26}$`).MatchString(run.Name) || run.Name != RunName("nodes", "uid-nodes", at("2026-12-01 02:00")) {
		t.Errorf("the run is named %q", run.Name)
	}
	if run.Annotations[v1alpha1.AnnotationScheduledFor] != "2026-12-01T02:00:00Z" {
		t.Errorf("annotation scheduled-for = %q", run.Annotations[v1alpha1.AnnotationScheduledFor])
	}
	want := v1alpha1.MachineRunSpec{Kind: "scan", PolicyRef: v1alpha1.PolicyRef{Kind: "ScanPolicy", Name: "nodes"}, RequestedBy: "controller", Reason: "schedule 0 2 * * *"}
	if run.Spec.Kind != want.Kind || run.Spec.PolicyRef != want.PolicyRef || run.Spec.RequestedBy != want.RequestedBy || run.Spec.Reason != want.Reason || len(run.Spec.Targets) != 0 {
		t.Errorf("the run's spec is %+v, want %+v", run.Spec, want)
	}
	p := w.policy(ns)
	if p.Status.LastRunName != run.Name || !p.Status.NextScheduleAt.Time.Equal(at("2026-12-02 02:00")) {
		t.Errorf("status after the run: lastRunName %q, nextScheduleAt %v", p.Status.LastRunName, p.Status.NextScheduleAt)
	}
	if got := w.schedule(); got != 24*time.Hour-5*time.Second || len(w.list(v1alpha1.MachineRunKind, ns)) != 1 {
		t.Fatalf("a second reconcile at the same time asked for %s and left %d runs", got, len(w.list(v1alpha1.MachineRunKind, ns)))
	}
}

// A scheduled time whose status write failed is reconciled again: the run exists
// already under the same name, and no second run is created.
func TestSchedulerRunsOneTimeOnce(t *testing.T) {
	fail := false
	w := newWorld(t, &interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
		if fail {
			return errors.New("the status write failed")
		}
		return c.SubResource(sub).Update(ctx, obj, opts...)
	}}, scanPolicy(ns, policySpec("0 2 * * *", "inventory")))
	w.schedule()
	w.now, fail = at("2026-12-01 02:00"), true
	if _, err := w.sched.Reconcile(context.Background(), key("nodes")); err == nil {
		t.Fatal("the reconcile hid a failed status write")
	}
	fail = false
	w.schedule()
	if n := len(w.list(v1alpha1.MachineRunKind, ns)); n != 1 {
		t.Fatalf("one scheduled time created %d runs", n)
	}
}

func TestSchedulerStates(t *testing.T) {
	spec := func(change func(map[string]interface{})) map[string]interface{} {
		s := policySpec("0 2 * * *", "inventory")
		change(s)
		return s
	}
	for _, c := range []struct {
		name, namespace string
		spec            map[string]interface{}
		ready           bool
		reason          string
	}{
		{"suspended", ns, spec(func(s map[string]interface{}) { s["suspended"] = true }), false, ReasonSuspended},
		{"no schedule", ns, spec(func(s map[string]interface{}) { s["schedule"] = "" }), true, ReasonNoSchedule},
		{"not cron", ns, spec(func(s map[string]interface{}) { s["schedule"] = "every day" }), false, ReasonInvalidSpec},
		{"a time that never comes", ns, spec(func(s map[string]interface{}) { s["schedule"] = "0 0 31 2 *" }), false, ReasonInvalidSpec},
		{"namespaces outside the platform namespace", "acme", spec(func(s map[string]interface{}) { s["namespaces"] = []interface{}{"other"} }), false, ReasonInvalidSpec},
		{"hardening without a baseline", ns, spec(func(s map[string]interface{}) {
			s["profiles"] = []interface{}{"hardening"}
			delete(s, "hardeningBaselineRef")
		}), false, ReasonInvalidSpec},
		{"a jitter that is no duration", ns, spec(func(s map[string]interface{}) { s["jitter"] = "soon" }), false, ReasonInvalidSpec},
	} {
		w := newWorld(t, nil, scanPolicy(c.namespace, c.spec))
		res, err := w.sched.Reconcile(context.Background(), ctrlKey(c.namespace, "nodes"))
		if err != nil || res.RequeueAfter != 0 {
			t.Errorf("%s: reconcile = %+v, %v; want no wake-up", c.name, res, err)
			continue
		}
		p := w.policy(c.namespace)
		cond := meta.FindStatusCondition(p.Status.Conditions, ConditionReady)
		if cond == nil || cond.Reason != c.reason || (cond.Status == "True") != c.ready || p.Status.NextScheduleAt != nil {
			t.Errorf("%s: condition %+v, nextScheduleAt %v", c.name, cond, p.Status.NextScheduleAt)
		}
		if n := len(w.list(v1alpha1.MachineRunKind, c.namespace)); n != 0 {
			t.Errorf("%s: %d runs were created", c.name, n)
		}
	}
}

// A changed schedule counts from now: the time of the old schedule is not run.
func TestSchedulerStartsAgainWhenTheSpecChanges(t *testing.T) {
	w := newWorld(t, nil, scanPolicy(ns, policySpec("0 2 * * *", "inventory")))
	w.schedule()
	u := newObject(v1alpha1.ScanPolicyKind)
	if err := w.c.Get(context.Background(), key("nodes").NamespacedName, u); err != nil {
		t.Fatal(err)
	}
	_ = unstructured.SetNestedField(u.Object, "0 6 * * *", "spec", "schedule")
	u.SetGeneration(2)
	if err := w.c.Update(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	w.now = at("2026-12-01 03:00")
	if got := w.schedule(); got != 3*time.Hour {
		t.Fatalf("after the change the scheduler asked for %s, want 3h", got)
	}
	if n := len(w.list(v1alpha1.MachineRunKind, ns)); n != 0 {
		t.Fatalf("the time of the old schedule created %d runs", n)
	}
}
