package handler

// #5014 — driver-side deny-egress backstop tests.
//
// The step-08 Job applies a cluster-wide cutover-egress-block
// CiliumClusterwideNetworkPolicy for the 10-minute hold and tears it down
// via TERM/EXIT traps. Those traps CANNOT run when the pod is SIGKILLed
// after the termination grace window (activeDeadlineSeconds hard-kill) and
// are never reached on a driver watch-loss — the policy leaked 3x live on
// hw242 (2026-07-12), freezing every CSI volume attach until hand-healed.
//
// runCutoverStep therefore defers reapCutoverEgressPolicy on EVERY exit of
// the egress-block-test step. These tests pin that contract on both the
// success path and the failure path, via the same resume-harness the
// TBD-V56 idempotency tests use (a prior terminal Job short-circuits the
// step, exercising runCutoverStep's early-return paths — precisely the
// exits a naive success-only cleanup would miss).

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
)

// installEgressReapCounter swaps the reap seam for a counter and restores
// it on test cleanup.
func installEgressReapCounter(t *testing.T) *int64 {
	t.Helper()
	var n int64
	prior := reapCutoverEgressPolicy
	reapCutoverEgressPolicy = func(ctx context.Context, deps *cutoverDeps) error {
		atomic.AddInt64(&n, 1)
		return nil
	}
	t.Cleanup(func() { reapCutoverEgressPolicy = prior })
	return &n
}

// waitEngineDone blocks until the cutover broadcaster reports the run
// ended (mirrors the existing resume tests' wait loop).
func waitEngineDone(t *testing.T, h *Handler) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		bus := h.cutoverBusFor()
		bus.mu.Lock()
		running := bus.running
		bus.mu.Unlock()
		if !running {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("cutover engine still running after 15s")
}

func egressReapPreStatus() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cutoverStatusConfigMapName(),
			Namespace: cutoverTestNS,
		},
		Data: map[string]string{
			"cutoverComplete":                  "false",
			"cutoverStartedAt":                 "2026-07-12T01:00:00Z",
			"totalSteps":                       "1",
			"step.egress-block-test.result":    "running",
			"step.egress-block-test.startedAt": "2026-07-12T01:00:05Z",
			"step.egress-block-test.jobName":   "cutover-egress-block-test-1783805411",
		},
	}
}

// TestRunCutoverStep_ReapsEgressPolicyOnSuccessExit: the step settles as
// success (prior Complete=True Job, resume path) — the backstop MUST still
// fire so a policy the Job's trap failed to remove never outlives the step.
func TestRunCutoverStep_ReapsEgressPolicyOnSuccessExit(t *testing.T) {
	reaps := installEgressReapCounter(t)

	objs := []k8sruntime.Object{
		makeCutoverStepCM("cutover-step-08-egress-block-test", "egress-block-test", 8, cutoverModeJob, minimalPodSpecYAML, ""),
		egressReapPreStatus(),
		makeCompletedJobForStep("egress-block-test", 13*time.Minute),
	}
	h, client := fakeHandlerWithCutover(t, objs...)

	h.ResumeInterruptedCutover(context.Background())
	waitEngineDone(t, h)

	cm, err := client.CoreV1().ConfigMaps(cutoverTestNS).Get(context.Background(),
		cutoverStatusConfigMapName(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get status ConfigMap: %v", err)
	}
	if got := cm.Data["step.egress-block-test.result"]; got != "success" {
		t.Fatalf("step result = %q, want success (harness precondition)", got)
	}
	if atomic.LoadInt64(reaps) < 1 {
		t.Errorf("reapCutoverEgressPolicy calls = %d, want >= 1 on the SUCCESS exit (#5014 backstop)", *reaps)
	}
}

// TestRunCutoverStep_ReapsEgressPolicyOnFailureExit: the step surfaces a
// terminal failure (prior Failed Job, non-transient BackoffLimitExceeded,
// auto-resume source) — runCutoverStep returns an error, and the backstop
// MUST fire on that exit too. This is the hw242 leak shape: the failed
// hold Job left its CCNP behind and nothing removed it.
func TestRunCutoverStep_ReapsEgressPolicyOnFailureExit(t *testing.T) {
	reaps := installEgressReapCounter(t)

	objs := []k8sruntime.Object{
		makeCutoverStepCM("cutover-step-08-egress-block-test", "egress-block-test", 8, cutoverModeJob, minimalPodSpecYAML, ""),
		egressReapPreStatus(),
		makeFailedJobForStep("egress-block-test"),
	}
	h, client := fakeHandlerWithCutover(t, objs...)

	h.ResumeInterruptedCutover(context.Background())
	waitEngineDone(t, h)

	cm, err := client.CoreV1().ConfigMaps(cutoverTestNS).Get(context.Background(),
		cutoverStatusConfigMapName(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get status ConfigMap: %v", err)
	}
	if got := cm.Data["cutoverComplete"]; got == "true" {
		t.Fatalf("cutoverComplete = true with a Failed egress Job (harness precondition broken)")
	}
	if atomic.LoadInt64(reaps) < 1 {
		t.Errorf("reapCutoverEgressPolicy calls = %d, want >= 1 on the FAILURE exit (#5014 backstop — the hw242 leak shape)", *reaps)
	}
}
