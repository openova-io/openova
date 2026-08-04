// Tests for #5477 — a Continuum whose CNPG standby was never probed must
// publish NO replicationLagSeconds at all, rather than the Go zero value
// dressed up as a measurement.
//
// The bug: the gate at continuum_controller.go:420 only runs
// observeCNPGStandby when spec.cnpgPair.name is set. On hw291, 7 of 8
// Continuum CRs had no cnpgPair, so primaryStatus/replicaStatus stayed
// zero-value structs, maxLag computed to 0, and patchStatus emitted
// status.replicationLagSeconds = 0 UNCONDITIONALLY — unlike every
// neighbouring field, each of which is guarded on non-emptiness.
//
// Live consequence: dr-shared-pg reported lag 0 while its region-b
// replica was 16s behind (measured via pg_last_xact_replay_timestamp),
// and dr-spine-openbao — a raft-transition app with no PostgreSQL
// whatsoever — reported a PostgreSQL replication lag of 0.
//
// Anti-theater: TestPatchStatus_UnprobedLagIsNotPublished FAILS against
// the pre-fix code, because the old line emitted the key unconditionally.
// It is the assertion that distinguishes "not measured" from "measured
// zero", which is the entire defect.

package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// A never-probed pair must leave the field absent, not zero.
func TestPatchStatus_UnprobedLagIsNotPublished(t *testing.T) {
	t.Parallel()
	cr := newTestContinuumCR("ns", "cr1", "fsn", []string{"hel"}, "in-memory")
	objs := append([]runtime.Object{cr}, newTestClusterPair("ns", "demo", 0)...)
	r, _, _ := newReconciler(t, objs...)

	// The dr-spine-openbao shape: healthy lease, no CNPG pair, so the
	// standby was never observed this tick.
	if err := r.patchStatus(context.Background(), cr, statusUpdate{
		Phase:                 PhaseHealthy,
		LeaseHolder:           "fsn",
		PrimaryRegion:         "fsn",
		ReplicationLagSeconds: 0,
		LagObserved:           false,
	}); err != nil {
		t.Fatalf("patchStatus: %v", err)
	}

	got, _ := r.Dyn.Resource(ContinuumGVR).Namespace("ns").Get(context.Background(), "cr1", metav1.GetOptions{})
	_, found, err := unstructured.NestedInt64(got.Object, "status", "replicationLagSeconds")
	if err != nil {
		t.Fatalf("reading status.replicationLagSeconds: %v", err)
	}
	if found {
		t.Errorf("status.replicationLagSeconds was published for a pair that was never probed — " +
			"an un-measured zero is indistinguishable from a caught-up standby, which is the #5477 defect")
	}
}

// A genuinely observed lag must still be published, including a real zero.
func TestPatchStatus_ObservedLagIsPublished(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		lag  int
	}{
		{"real non-zero lag", 16},
		{"genuinely caught up", 0},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cr := newTestContinuumCR("ns", "cr1", "fsn", []string{"hel"}, "in-memory")
			objs := append([]runtime.Object{cr}, newTestClusterPair("ns", "demo", 0)...)
			r, _, _ := newReconciler(t, objs...)

			if err := r.patchStatus(context.Background(), cr, statusUpdate{
				Phase:                 PhaseHealthy,
				LeaseHolder:           "fsn",
				PrimaryRegion:         "fsn",
				ReplicationLagSeconds: tc.lag,
				LagObserved:           true,
			}); err != nil {
				t.Fatalf("patchStatus: %v", err)
			}

			got, _ := r.Dyn.Resource(ContinuumGVR).Namespace("ns").Get(context.Background(), "cr1", metav1.GetOptions{})
			lag, found, err := unstructured.NestedInt64(got.Object, "status", "replicationLagSeconds")
			if err != nil {
				t.Fatalf("reading status.replicationLagSeconds: %v", err)
			}
			if !found {
				t.Fatalf("an OBSERVED lag of %d must be published", tc.lag)
			}
			if lag != int64(tc.lag) {
				t.Errorf("status.replicationLagSeconds = %d want %d", lag, tc.lag)
			}
		})
	}
}

// An unobserved tick must not erase a previously-measured value. The
// status patch is a merge, so omitting the key preserves the last real
// reading — the same contract LastLuaRecord relies on.
func TestPatchStatus_UnprobedTickPreservesPriorLag(t *testing.T) {
	t.Parallel()
	cr := newTestContinuumCR("ns", "cr1", "fsn", []string{"hel"}, "in-memory")
	objs := append([]runtime.Object{cr}, newTestClusterPair("ns", "demo", 0)...)
	r, _, _ := newReconciler(t, objs...)

	if err := r.patchStatus(context.Background(), cr, statusUpdate{
		Phase: PhaseHealthy, LeaseHolder: "fsn", PrimaryRegion: "fsn",
		ReplicationLagSeconds: 42, LagObserved: true,
	}); err != nil {
		t.Fatalf("seed patchStatus: %v", err)
	}
	if err := r.patchStatus(context.Background(), cr, statusUpdate{
		Phase: PhaseHealthy, LeaseHolder: "fsn", PrimaryRegion: "fsn",
		ReplicationLagSeconds: 0, LagObserved: false,
	}); err != nil {
		t.Fatalf("unobserved patchStatus: %v", err)
	}

	got, _ := r.Dyn.Resource(ContinuumGVR).Namespace("ns").Get(context.Background(), "cr1", metav1.GetOptions{})
	lag, found, _ := unstructured.NestedInt64(got.Object, "status", "replicationLagSeconds")
	if !found || lag != 42 {
		t.Errorf("an unobserved tick clobbered the last real reading: found=%v lag=%d want 42", found, lag)
	}
}
