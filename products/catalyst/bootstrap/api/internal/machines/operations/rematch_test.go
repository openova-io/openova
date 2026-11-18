package operations

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/apis/v1alpha1"
)

// One time of the rematch schedule gives one rematch run per policy with the profile
// vulnerability, however often it fires; a policy without that profile, or suspended,
// gets none.
func TestRematchCreatesOneRunPerPolicyAndTime(t *testing.T) {
	suspended := policySpec("", "inventory", "vulnerability")
	suspended["suspended"] = true
	other := object(v1alpha1.ScanPolicyKind, "acme", "hardening-only", map[string]interface{}{"spec": policySpec("", "hardening")})
	paused := object(v1alpha1.ScanPolicyKind, "acme", "paused", map[string]interface{}{"spec": suspended})
	w := newWorld(t, nil, nodesPolicy("inventory", "vulnerability"), other, paused)
	sched, err := ParseSchedule(DefaultRematchSchedule)
	if err != nil {
		t.Fatal(err)
	}
	r := &Rematcher{Client: w.c, Schedule: sched, Text: DefaultRematchSchedule, Now: func() time.Time { return w.now }}
	slot := sched.Next(t0)
	if !slot.Equal(at("2026-12-01 03:30")) {
		t.Fatalf("the next time of %q after %s is %s", DefaultRematchSchedule, t0, slot)
	}
	for i := 0; i < 2; i++ {
		if err := r.Fire(context.Background(), slot); err != nil {
			t.Fatalf("firing %d: %v", i+1, err)
		}
	}
	runs := append(w.list(v1alpha1.MachineRunKind, ns), w.list(v1alpha1.MachineRunKind, "acme")...)
	if len(runs) != 1 || runs[0].GetName() != RematchRunName("nodes", "uid-nodes", slot) || runs[0].GetNamespace() != ns {
		t.Fatalf("two firings of one time gave %d runs: %v", len(runs), runs)
	}
	run, err := v1alpha1.MachineRunFromUnstructured(&runs[0])
	if err != nil {
		t.Fatal(err)
	}
	if run.Spec.Kind != v1alpha1.RunKindRematch || run.Spec.PolicyRef.Name != "nodes" || run.Spec.RequestedBy != v1alpha1.RequestedByController ||
		run.Spec.Reason != "rematch schedule 30 3 * * *" || run.Annotations[v1alpha1.AnnotationScheduledFor] != "" {
		t.Fatalf("the rematch run is %+v", run)
	}
	if _, found, _ := unstructured.NestedMap(runs[0].Object, "status"); found {
		t.Fatal("the rematch run was created with a status")
	}
	if RematchRunName("nodes", "uid-nodes", slot) == RunName("nodes", "uid-nodes", slot) {
		t.Fatal("the rematch run of a time has the name of the scan run of that time")
	}
	if err := r.Fire(context.Background(), sched.Next(slot)); err != nil {
		t.Fatal(err)
	}
	if n := len(w.list(v1alpha1.MachineRunKind, ns)); n != 2 {
		t.Fatalf("the next time of the schedule gave %d runs in all, want 2", n)
	}
}

// The timer stops when its context ends, and a schedule with no time is an error.
func TestRematchTimerEndsWithItsContext(t *testing.T) {
	sched, _ := ParseSchedule(DefaultRematchSchedule)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (&Rematcher{Schedule: sched, Text: DefaultRematchSchedule}).Start(ctx); err != nil {
		t.Fatalf("a cancelled timer returned %v", err)
	}
	never, _ := ParseSchedule("0 0 31 2 *")
	if err := (&Rematcher{Schedule: never, Text: "0 0 31 2 *"}).Start(context.Background()); err == nil {
		t.Fatal("a schedule with no time started a timer")
	}
}
