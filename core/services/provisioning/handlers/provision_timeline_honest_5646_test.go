package handlers

import (
	"testing"

	"github.com/openova-io/openova/core/services/provisioning/store"
)

// steps is a tiny helper to build a []store.ProvisionStep from (name,status)
// pairs so the tables below read like the live timeline.
func steps(pairs ...[2]string) []store.ProvisionStep {
	out := make([]store.ProvisionStep, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, store.ProvisionStep{Name: p[0], Status: p[1]})
	}
	return out
}

// TestCompletedStepProgress_NeverHundredOnFailedRun pins the #5646 progress
// invariant: progress reaches 100 ONLY when every step is completed. The exact
// live record that filed the issue — "Installing mysql (dependency)" failed
// while WordPress/TLS/Health completed — must compute BELOW 100 so the customer
// never sees a full progress bar above "Provisioning didn't finish".
func TestCompletedStepProgress_NeverHundredOnFailedRun(t *testing.T) {
	cases := []struct {
		name  string
		steps []store.ProvisionStep
		want  int
	}{
		{
			// The verbatim hw292 / UAT-row-86 record: 6 of 7 steps completed,
			// the mysql dependency failed. Old code left Progress at 100; the
			// honest value is 6*100/7 = 85, and crucially NOT 100.
			name: "hw292 failed-mysql record is 85, not 100",
			steps: steps(
				[2]string{"Creating Organization", "completed"},
				[2]string{"Committing manifests to Git", "completed"},
				[2]string{"Provisioning vCluster", "completed"},
				[2]string{"Installing mysql (dependency)", "failed"},
				[2]string{"Deploying WordPress", "completed"},
				[2]string{"Configuring TLS certificates", "completed"},
				[2]string{"Running health checks", "completed"},
			),
			want: 85,
		},
		{
			name: "all completed is exactly 100",
			steps: steps(
				[2]string{"Creating Organization", "completed"},
				[2]string{"Deploying WordPress", "completed"},
				[2]string{"Running health checks", "completed"},
			),
			want: 100,
		},
		{
			name: "a single failed step among otherwise-complete is below 100",
			steps: steps(
				[2]string{"Creating Organization", "completed"},
				[2]string{"Deploying WordPress", "completed"},
				[2]string{"Running health checks", "failed"},
			),
			want: 66,
		},
		{
			name: "a still-pending step keeps progress below 100",
			steps: steps(
				[2]string{"Creating Organization", "completed"},
				[2]string{"Deploying WordPress", "completed"},
				[2]string{"Running health checks", "pending"},
			),
			want: 66,
		},
		{
			name:  "empty step list is 0, not a divide-by-zero",
			steps: nil,
			want:  0,
		},
		{
			name: "nothing completed is 0",
			steps: steps(
				[2]string{"Creating Organization", "running"},
				[2]string{"Deploying WordPress", "pending"},
			),
			want: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := completedStepProgress(tc.steps)
			if got != tc.want {
				t.Fatalf("completedStepProgress = %d, want %d", got, tc.want)
			}
			// The load-bearing invariant, asserted directly: 100 iff every step
			// is completed. Any failed/pending/running step must yield < 100.
			allCompleted := len(tc.steps) > 0
			for _, s := range tc.steps {
				if s.Status != "completed" {
					allCompleted = false
					break
				}
			}
			if got == 100 && !allCompleted {
				t.Fatalf("progress reported 100 on a run that is not fully completed: %+v", tc.steps)
			}
			if allCompleted && got != 100 {
				t.Fatalf("progress must be 100 when every step is completed, got %d", got)
			}
		})
	}
}

// TestPriorStepsComplete_EnforcesDeclaredOrder pins the #5646 ordering
// invariant the pod-truth reconciler leans on: a step may be greened only when
// EVERY earlier step is already completed. This is what stops the reconciler
// from marking "Running health checks" completed while the earlier
// "Installing mysql (dependency)" step is still unresolved (the out-of-order
// timeline defect).
func TestPriorStepsComplete_EnforcesDeclaredOrder(t *testing.T) {
	base := steps(
		[2]string{"Creating Organization", "completed"},     // 0
		[2]string{"Committing manifests to Git", "completed"}, // 1
		[2]string{"Provisioning vCluster", "completed"},       // 2
		[2]string{"Installing mysql (dependency)", "running"}, // 3
		[2]string{"Deploying WordPress", "completed"},         // 4 (pod ready ahead of the dep)
		[2]string{"Configuring TLS certificates", "pending"},  // 5
		[2]string{"Running health checks", "pending"},         // 6
	)

	cases := []struct {
		name string
		i    int
		want bool
	}{
		{"index 0 has no predecessors — trivially true", 0, true},
		{"first three infra steps done — dep step may advance", 3, true},
		{"WordPress (idx 4) is BLOCKED while mysql (idx 3) is unresolved", 4, false},
		{"TLS (idx 5) is BLOCKED while an earlier step is unresolved", 5, false},
		{"health checks (idx 6) is BLOCKED while an earlier step is unresolved", 6, false},
		{"len(steps) is treated as the whole slice", len(base), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := priorStepsComplete(base, tc.i); got != tc.want {
				t.Fatalf("priorStepsComplete(i=%d) = %v, want %v", tc.i, got, tc.want)
			}
		})
	}

	// Positive direction: once the dep is completed, every downstream step is
	// unblocked in declared order — the reconciler can green them one pass at a
	// time without ever showing a later stage above an earlier unresolved one.
	healed := steps(
		[2]string{"Creating Organization", "completed"},
		[2]string{"Committing manifests to Git", "completed"},
		[2]string{"Provisioning vCluster", "completed"},
		[2]string{"Installing mysql (dependency)", "completed"},
		[2]string{"Deploying WordPress", "completed"},
		[2]string{"Configuring TLS certificates", "running"},
		[2]string{"Running health checks", "pending"},
	)
	if !priorStepsComplete(healed, 5) {
		t.Fatalf("TLS step should be unblocked once every earlier step is completed")
	}

	// A FAILED earlier step also blocks — a later step must never green above a
	// failed predecessor (that failed step is separately re-observed and, if its
	// workload recovered, superseded to completed first — #5646).
	withFailed := steps(
		[2]string{"Creating Organization", "completed"},
		[2]string{"Installing mysql (dependency)", "failed"},
		[2]string{"Deploying WordPress", "completed"},
	)
	if priorStepsComplete(withFailed, 2) {
		t.Fatalf("a step must be blocked while an earlier step is failed")
	}
}
