// #5014 — cutover driver Job-watch re-watch tests.
//
// Live defect (hw242 step-03 harbor-prewarm 2026-07-12, hw251
// egress-block-test): the step-runner treated loss of its Job watch
// channel as a STEP FAILURE. It did a single one-shot Get on close and,
// if the Job had not YET flipped terminal, returned
// "channel closed before terminal condition" — halting the whole cutover
// chain on a step whose Job actually SUCCEEDED moments later (a
// false-negative). A watch close is normal k8s behaviour (server-side
// watch expiry) and is guaranteed on the mid-cutover catalyst-api Pod
// roll at step-07.
//
// Post-fix watchJobToCompletion makes the step outcome a function of the
// Job's ACTUAL state: on a channel close (or a watch-establish error) it
// re-Gets the Job and re-establishes the watch, bounded by a re-watch
// budget + the overall step deadline. These tests pin every branch:
//
//	(a) channel closes, Job then Complete (via re-Get)      → step succeeds
//	(b) channel closes, Job still running, re-watch delivers Complete event → succeeds
//	(c) genuine JobFailed (direct event)                    → returns Failed (step fails)
//	(c2) genuine JobFailed surfaced through a channel close → returns Failed (not masked)
//	(d) re-watch budget exhausted, Job never terminal       → clear budget error
//	(e) watch-establish error is transient, then Complete   → succeeds
//	(f) ctx cancelled mid-watch                             → returns ctx.Err()
//	(g) overall deadline elapses with Job not terminal      → returns DeadlineExceeded
//	(h) end-to-end: a step whose first watch closes still advances the chain
package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	fakek8s "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

// ── helpers ──────────────────────────────────────────────────────────────────

const jobwatchTestJob = "cutover-egress-block-test-1783798334"

func jobWith(name, ns string, cond batchv1.JobConditionType) *batchv1.Job {
	j := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
	if cond != "" {
		j.Status.Conditions = []batchv1.JobCondition{{
			Type:   cond,
			Status: corev1.ConditionTrue,
			Reason: "Test",
		}}
	}
	return j
}

// newClosedJobWatch returns a watcher whose ResultChan is already closed —
// modelling a server-side watch expiry / the catalyst-api Pod roll closing
// the engine's watch. The consumer observes (_, ok=false).
func newClosedJobWatch() watch.Interface {
	fw := watch.NewFake()
	fw.Stop()
	return fw
}

// newJobEventWatch returns an open watcher pre-loaded with a single Modify
// event carrying job, so the consumer observes a terminal condition on the
// wire (buffered so the reactor doesn't block).
func newJobEventWatch(job *batchv1.Job) watch.Interface {
	fw := watch.NewFakeWithChanSize(1, false)
	fw.Modify(job)
	return fw
}

// scriptedGet installs a get/jobs reactor returning conds[i] on the i-th Get,
// holding the last entry for any further calls (so a two-phase running→terminal
// transition is deterministic without racing an UpdateStatus goroutine).
func scriptedGet(client *fakek8s.Clientset, ns, name string, conds ...batchv1.JobConditionType) {
	var calls int32
	client.PrependReactor("get", "jobs", func(clienttesting.Action) (bool, k8sruntime.Object, error) {
		i := int(atomic.AddInt32(&calls, 1)) - 1
		if i >= len(conds) {
			i = len(conds) - 1
		}
		return true, jobWith(name, ns, conds[i]), nil
	})
}

func newJobwatchDeps(objs ...k8sruntime.Object) (*cutoverDeps, *fakek8s.Clientset) {
	client := fakek8s.NewSimpleClientset(objs...)
	return &cutoverDeps{core: client, ns: cutoverTestNS}, client
}

// ── (a) channel close, then Job Complete via re-Get ─────────────────────────

func TestWatchJobToCompletion_ChannelCloseThenComplete_Succeeds(t *testing.T) {
	t.Setenv(envCutoverJobWatchRewatchBackoff, "1ms")
	deps, client := newJobwatchDeps()
	// First Get (before the watch) sees Running; after the close, the re-Get
	// sees Complete — exactly the hw242 timeline (Job Complete 1/1 while the
	// driver's watch channel churned).
	scriptedGet(client, deps.ns, jobwatchTestJob, "", batchv1.JobComplete)
	var watches int32
	client.PrependWatchReactor("jobs", func(clienttesting.Action) (bool, watch.Interface, error) {
		if atomic.AddInt32(&watches, 1) == 1 {
			return true, newClosedJobWatch(), nil
		}
		t.Errorf("unexpected watch #%d: re-Get should observe Complete without a second watch", watches)
		return true, newClosedJobWatch(), nil
	})

	cond, err := watchJobToCompletion(context.Background(), deps, jobwatchTestJob, 5*time.Second)
	if err != nil {
		t.Fatalf("watchJobToCompletion errored on a Complete Job after channel close (the #5014 false-negative): %v", err)
	}
	if cond != batchv1.JobComplete {
		t.Fatalf("cond = %q, want %q", cond, batchv1.JobComplete)
	}
}

// ── (b) channel close, Job still running, re-watch delivers the terminal ────

func TestWatchJobToCompletion_ChannelCloseThenRewatchDeliversComplete(t *testing.T) {
	t.Setenv(envCutoverJobWatchRewatchBackoff, "1ms")
	deps, client := newJobwatchDeps()
	// Get ALWAYS reports Running — the terminal signal must come from the
	// RE-ESTABLISHED watch's event, proving the re-watch (not just re-Get) path.
	scriptedGet(client, deps.ns, jobwatchTestJob, "")
	var watches int32
	client.PrependWatchReactor("jobs", func(clienttesting.Action) (bool, watch.Interface, error) {
		if atomic.AddInt32(&watches, 1) == 1 {
			return true, newClosedJobWatch(), nil
		}
		return true, newJobEventWatch(jobWith(jobwatchTestJob, deps.ns, batchv1.JobComplete)), nil
	})

	cond, err := watchJobToCompletion(context.Background(), deps, jobwatchTestJob, 5*time.Second)
	if err != nil {
		t.Fatalf("watchJobToCompletion errored; re-watch should have observed the terminal event: %v", err)
	}
	if cond != batchv1.JobComplete {
		t.Fatalf("cond = %q, want %q", cond, batchv1.JobComplete)
	}
	if watches < 2 {
		t.Fatalf("expected the watch to be re-established (>=2 calls); got %d", watches)
	}
}

// ── (c) genuine JobFailed via a direct event still fails the step ───────────

func TestWatchJobToCompletion_GenuineFailedEvent_ReturnsFailed(t *testing.T) {
	deps, client := newJobwatchDeps()
	scriptedGet(client, deps.ns, jobwatchTestJob, "") // Running at the up-front Get
	client.PrependWatchReactor("jobs", func(clienttesting.Action) (bool, watch.Interface, error) {
		return true, newJobEventWatch(jobWith(jobwatchTestJob, deps.ns, batchv1.JobFailed)), nil
	})

	cond, err := watchJobToCompletion(context.Background(), deps, jobwatchTestJob, 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cond != batchv1.JobFailed {
		t.Fatalf("cond = %q, want %q (a genuine failure must still surface so the caller fails the step)", cond, batchv1.JobFailed)
	}
}

// ── (c2) a genuine failure surfaced THROUGH a channel close is not masked ───

func TestWatchJobToCompletion_ChannelCloseThenFailed_ReturnsFailed(t *testing.T) {
	t.Setenv(envCutoverJobWatchRewatchBackoff, "1ms")
	deps, client := newJobwatchDeps()
	scriptedGet(client, deps.ns, jobwatchTestJob, "", batchv1.JobFailed)
	client.PrependWatchReactor("jobs", func(clienttesting.Action) (bool, watch.Interface, error) {
		return true, newClosedJobWatch(), nil
	})

	cond, err := watchJobToCompletion(context.Background(), deps, jobwatchTestJob, 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cond != batchv1.JobFailed {
		t.Fatalf("cond = %q, want %q (re-Get must surface a real Failed, not re-watch forever)", cond, batchv1.JobFailed)
	}
}

// ── (d) re-watch budget exhausted with a never-terminal Job ─────────────────

func TestWatchJobToCompletion_RewatchBudgetExhausted_FailsWithBudgetError(t *testing.T) {
	t.Setenv(envCutoverJobWatchRewatchBackoff, "1ms")
	t.Setenv(envCutoverJobWatchRewatchMax, "3")
	deps, client := newJobwatchDeps()
	scriptedGet(client, deps.ns, jobwatchTestJob, "") // never terminal
	var watches int32
	client.PrependWatchReactor("jobs", func(clienttesting.Action) (bool, watch.Interface, error) {
		atomic.AddInt32(&watches, 1)
		return true, newClosedJobWatch(), nil // always closes
	})

	cond, err := watchJobToCompletion(context.Background(), deps, jobwatchTestJob, 5*time.Second)
	if err == nil {
		t.Fatalf("expected a budget-exhausted error; got cond=%q err=nil", cond)
	}
	if cond != "" {
		t.Fatalf("cond = %q, want empty on failure", cond)
	}
	if !strings.Contains(err.Error(), "re-watch budget of 3 exhausted") {
		t.Fatalf("error %q must name the exhausted re-watch budget", err.Error())
	}
	// budget=3 → 3 successful re-watches, then the 4th close fails.
	if got := atomic.LoadInt32(&watches); got != 4 {
		t.Fatalf("watch established %d times, want 4 (budget 3 + the final failing close)", got)
	}
}

// ── (e) transient watch-establish error is retried, then Complete ───────────

func TestWatchJobToCompletion_EstablishErrorThenRewatchSucceeds(t *testing.T) {
	t.Setenv(envCutoverJobWatchRewatchBackoff, "1ms")
	deps, client := newJobwatchDeps()
	scriptedGet(client, deps.ns, jobwatchTestJob, "")
	var watches int32
	client.PrependWatchReactor("jobs", func(clienttesting.Action) (bool, watch.Interface, error) {
		if atomic.AddInt32(&watches, 1) == 1 {
			return true, nil, errors.New("etcdserver: request timed out") // transient establish failure
		}
		return true, newJobEventWatch(jobWith(jobwatchTestJob, deps.ns, batchv1.JobComplete)), nil
	})

	cond, err := watchJobToCompletion(context.Background(), deps, jobwatchTestJob, 5*time.Second)
	if err != nil {
		t.Fatalf("a transient watch-establish error should be retried, not fail the step: %v", err)
	}
	if cond != batchv1.JobComplete {
		t.Fatalf("cond = %q, want %q", cond, batchv1.JobComplete)
	}
}

// ── (f) ctx cancel preserves ctx.Err() semantics ───────────────────────────

func TestWatchJobToCompletion_ContextCancel_ReturnsCtxErr(t *testing.T) {
	deps, client := newJobwatchDeps()
	scriptedGet(client, deps.ns, jobwatchTestJob, "")
	client.PrependWatchReactor("jobs", func(clienttesting.Action) (bool, watch.Interface, error) {
		return true, watch.NewFake(), nil // open, never delivers
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	cond, err := watchJobToCompletion(ctx, deps, jobwatchTestJob, 5*time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want it to wrap context.Canceled", err)
	}
	if cond != "" {
		t.Fatalf("cond = %q, want empty on cancel", cond)
	}
}

// ── (g) overall deadline elapses with the Job not terminal ──────────────────

func TestWatchJobToCompletion_DeadlineExceeded_ReturnsDeadlineErr(t *testing.T) {
	deps, client := newJobwatchDeps()
	scriptedGet(client, deps.ns, jobwatchTestJob, "")
	client.PrependWatchReactor("jobs", func(clienttesting.Action) (bool, watch.Interface, error) {
		return true, watch.NewFake(), nil // open, never delivers
	})

	cond, err := watchJobToCompletion(context.Background(), deps, jobwatchTestJob, 40*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want it to wrap context.DeadlineExceeded", err)
	}
	if cond != "" {
		t.Fatalf("cond = %q, want empty on deadline", cond)
	}
}

// ── (h) end-to-end: a mid-step watch close does NOT halt the chain ──────────

// TestHandleCutoverStart_AdvancesChainDespiteWatchClose is the #5014
// regression at the engine level: the FIRST Job watch of the run closes
// mid-step (as it did live on hw242), yet the created Job completes and the
// engine must advance through every step to cutoverComplete=true instead of
// recording failedStep + halting.
func TestHandleCutoverStart_AdvancesChainDespiteWatchClose(t *testing.T) {
	// Backoff long enough that the installJobReactor's ~20ms UpdateStatus has
	// landed before the post-close re-Get, so the re-Get sees Complete on the
	// first retry (well within the default budget of 10).
	t.Setenv(envCutoverJobWatchRewatchBackoff, "60ms")

	objs := []k8sruntime.Object{
		makeCutoverStepCM("cutover-step-01-gitea-mirror", "gitea-mirror", 1, cutoverModeJob, minimalPodSpecYAML, ""),
		makeCutoverStepCM("cutover-step-02-harbor-prewarm", cutoverStepHarborPrewarm, 2, cutoverModeJob, minimalPodSpecYAML, ""),
		makeCutoverStepCM("cutover-step-03-registry-pivot", "registry-pivot", 3, cutoverModeDaemonSetWait, "", "registry-pivot"),
		makeReadyDaemonSet("registry-pivot"),
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: cutoverStatusConfigMapName(), Namespace: cutoverTestNS},
			Data: map[string]string{
				"cutoverComplete":              "false",
				"node.cp-1.registriesYaml":     "v2",
				"node.worker-1.registriesYaml": "v2",
				"node.worker-2.registriesYaml": "v2",
			},
		},
	}
	h, client := fakeHandlerWithCutover(t, objs...)
	installJobReactor(t, client, batchv1.JobComplete)

	// Close ONLY the first Job watch of the run; every later watch falls
	// through to the tracker-backed default so the remaining steps behave
	// normally.
	var firstWatch int32
	client.PrependWatchReactor("jobs", func(clienttesting.Action) (bool, watch.Interface, error) {
		if atomic.AddInt32(&firstWatch, 1) == 1 {
			return true, newClosedJobWatch(), nil
		}
		return false, nil, nil // delegate to the default tracker watch
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sovereign/cutover/start", nil)
	h.HandleCutoverStart(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("HandleCutoverStart: status %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		bus := h.cutoverBusFor()
		bus.mu.Lock()
		running := bus.running
		bus.mu.Unlock()
		if !running {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	cm, err := client.CoreV1().ConfigMaps(cutoverTestNS).Get(context.Background(), cutoverStatusConfigMapName(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get status ConfigMap: %v", err)
	}
	if got := cm.Data["cutoverComplete"]; got != "true" {
		t.Fatalf("cutoverComplete = %q, want true (chain must advance despite the mid-step watch close); failedStep=%q lastError=%q",
			got, cm.Data["failedStep"], cm.Data["lastError"])
	}
	if fs := cm.Data["failedStep"]; fs != "" {
		t.Fatalf("failedStep = %q, want empty — a watch close must not be recorded as a step failure", fs)
	}
	if got := cm.Data["step.gitea-mirror.result"]; got != "success" {
		t.Fatalf("step.gitea-mirror.result = %q, want success", got)
	}
	// The close path is definitively pinned by the direct watchJobToCompletion
	// unit tests above (b/d/e); here we only assert the chain still completes.
	// The watch normally fires (a Job is Running at its up-front Get), but we
	// don't make that a hard assertion to avoid coupling to CREATE→Get timing.
	if atomic.LoadInt32(&firstWatch) < 1 {
		t.Logf("note: no Job watch was established this run (both step Jobs completed before their up-front Get) — chain-completion still verified")
	}
}
