package handler

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
)

// seedHandoverSeal writes the Sovereign-side handover marker
// (secret/catalyst/tofu-phase0-archive) into the fake store, simulating a
// Sovereign that has received the Phase-0 archive from the mothership.
func (s *fakeKVStore) seedHandoverSeal() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data["catalyst/tofu-phase0-archive"] = map[string]any{
		"archive":  "<base64-tofu-archive>",
		"sealedAt": "2026-06-16T13:36:31Z",
	}
}

// TestRunCutoverReconcilePass drives the #4635 level-triggered reconciler
// through its three pure-state branches. The decision is state, never timing:
// fire iff (handover seal exists) && (cutover-complete seal absent).
func TestRunCutoverReconcilePass(t *testing.T) {
	cases := []struct {
		name       string
		handover   bool // tofu-phase0-archive sealed
		complete   bool // durable cutover-complete sealed
		wantFires  int
		wantSource string
	}{
		{name: "pre-handover: seal absent → no fire", handover: false, complete: false, wantFires: 0},
		{name: "already complete → no fire", handover: true, complete: true, wantFires: 0},
		{name: "sealed-but-uncut → fires with source=reconcile", handover: true, complete: false, wantFires: 1, wantSource: "reconcile"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			objs := []k8sruntime.Object{
				makeCutoverStepCM("cutover-step-01-x", "x", 1, cutoverModeJob, minimalPodSpecYAML, ""),
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{Name: cutoverStatusConfigMapName(), Namespace: cutoverTestNS},
					Data:       map[string]string{"cutoverComplete": "false", "cutoverStartedAt": ""},
				},
			}
			h, _ := fakeHandlerWithCutover(t, objs...)
			ob, store := newFakeOpenbao(t)
			h.openbao = ob
			if tc.handover {
				store.seedHandoverSeal()
			}
			if tc.complete {
				store.seedCutoverSeal("2026-06-16T13:36:31Z", "2026-06-16T14:11:12Z")
			}

			fires := 0
			gotSource := ""
			h.spawnCutoverEngineFn = func(_ context.Context, _ *cutoverDeps, source string) (cutoverSpawnResult, error) {
				fires++
				gotSource = source
				return cutoverSpawnResult{outcome: cutoverSpawnStarted}, nil
			}

			h.runCutoverReconcilePass(context.Background())

			if fires != tc.wantFires {
				t.Fatalf("fires=%d, want %d", fires, tc.wantFires)
			}
			if tc.wantSource != "" && gotSource != tc.wantSource {
				t.Fatalf("source=%q, want %q", gotSource, tc.wantSource)
			}
		})
	}
}

// TestRunCutoverReconcilePass_FailsClosedOnSealReadError proves a transient
// OpenBao read error never half-fires the engine — the pass returns without
// firing and the next tick retries (no give-up).
func TestRunCutoverReconcilePass_FailsClosedOnSealReadError(t *testing.T) {
	objs := []k8sruntime.Object{
		makeCutoverStepCM("cutover-step-01-x", "x", 1, cutoverModeJob, minimalPodSpecYAML, ""),
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: cutoverStatusConfigMapName(), Namespace: cutoverTestNS},
			Data:       map[string]string{"cutoverComplete": "false"},
		},
	}
	h, _ := fakeHandlerWithCutover(t, objs...)
	// A nil OpenBao client makes sovereignHandoverComplete read as
	// not-handed-over (false, nil) — the gate stays closed, no fire. This is
	// the Catalyst-Zero / pre-OpenBao path; the reconciler must never fire
	// without a confirmed seal.
	h.openbao = nil

	fires := 0
	h.spawnCutoverEngineFn = func(_ context.Context, _ *cutoverDeps, _ string) (cutoverSpawnResult, error) {
		fires++
		return cutoverSpawnResult{outcome: cutoverSpawnStarted}, nil
	}
	h.runCutoverReconcilePass(context.Background())
	if fires != 0 {
		t.Fatalf("fires=%d, want 0 (no seal ⇒ never fire)", fires)
	}
}
