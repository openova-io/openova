// cutover_engine_lease_6289_test.go — #6289 guards: ONE driver of the cutover
// state machine at a time, across catalyst-api Pods.
package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakek8s "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

func leaseHeldBy(holder string, renewedAgo time.Duration, ttlSeconds int32) *coordinationv1.Lease {
	renew := metav1.NewMicroTime(time.Now().Add(-renewedAgo))
	return &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: cutoverEngineLeaseName, Namespace: cutoverTestNS},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity:       &holder,
			LeaseDurationSeconds: &ttlSeconds,
			AcquireTime:          &renew,
			RenewTime:            &renew,
		},
	}
}

func getLease(t *testing.T, client *fakek8s.Clientset) *coordinationv1.Lease {
	t.Helper()
	l, err := client.CoordinationV1().Leases(cutoverTestNS).Get(context.Background(), cutoverEngineLeaseName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get lease: %v", err)
	}
	return l
}

func TestCutoverEngineLease_AcquireHeldExpiredTakeoverRelease(t *testing.T) {
	_, client := fakeHandlerWithCutover(t)
	deps := &cutoverDeps{core: client, ns: cutoverTestNS}
	ctx := context.Background()

	a, err := acquireCutoverEngineLease(ctx, deps, "pod-a", time.Now())
	if err != nil || a == nil || a.degraded {
		t.Fatalf("pod-a acquire: lease=%+v err=%v", a, err)
	}
	if got := *getLease(t, client).Spec.HolderIdentity; got != "pod-a" {
		t.Fatalf("holder = %q, want pod-a", got)
	}

	// A live holder refuses a second driver.
	if _, err := acquireCutoverEngineLease(ctx, deps, "pod-b", time.Now()); err == nil {
		t.Fatal("pod-b acquired a lease pod-a holds live")
	} else if !strings.Contains(err.Error(), "held by pod-a") {
		t.Errorf("err = %v, want a held-by-pod-a refusal", err)
	}

	// Re-acquire by the same holder is fine (renewal shape).
	if _, err := acquireCutoverEngineLease(ctx, deps, "pod-a", time.Now()); err != nil {
		t.Fatalf("pod-a re-acquire: %v", err)
	}

	// Release hands it back immediately; pod-b then takes it with a transition.
	a.Release()
	if got := *getLease(t, client).Spec.HolderIdentity; got != "" {
		t.Fatalf("holder after release = %q, want empty", got)
	}
	b, err := acquireCutoverEngineLease(ctx, deps, "pod-b", time.Now())
	if err != nil {
		t.Fatalf("pod-b acquire after release: %v", err)
	}
	if l := getLease(t, client); *l.Spec.HolderIdentity != "pod-b" || l.Spec.LeaseTransitions == nil || *l.Spec.LeaseTransitions < 1 {
		t.Errorf("after takeover: holder=%q transitions=%v", *l.Spec.HolderIdentity, l.Spec.LeaseTransitions)
	}
	b.Release()
}

func TestCutoverEngineLease_ExpiredLeaseIsTakenOver(t *testing.T) {
	// pod-a was SIGKILLed 5 minutes ago with a 30s TTL: its lease has lapsed.
	_, client := fakeHandlerWithCutover(t, leaseHeldBy("pod-a", 5*time.Minute, 30))
	deps := &cutoverDeps{core: client, ns: cutoverTestNS}
	b, err := acquireCutoverEngineLease(context.Background(), deps, "pod-b", time.Now())
	if err != nil {
		t.Fatalf("takeover of an expired lease failed: %v", err)
	}
	if got := *getLease(t, client).Spec.HolderIdentity; got != "pod-b" {
		t.Errorf("holder = %q, want pod-b", got)
	}
	b.Release()
}

func TestCutoverEngineLease_ForbiddenIsDegradedNotFatal(t *testing.T) {
	_, client := fakeHandlerWithCutover(t)
	client.PrependReactor("*", "leases", func(action clienttesting.Action) (bool, k8sruntime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "coordination.k8s.io", Resource: "leases"}, cutoverEngineLeaseName, fmt.Errorf("rbac predates #6289"))
	})
	deps := &cutoverDeps{core: client, ns: cutoverTestNS}
	l, err := acquireCutoverEngineLease(context.Background(), deps, "pod-a", time.Now())
	if err != nil || l == nil {
		t.Fatalf("forbidden must degrade, not fail: lease=%v err=%v", l, err)
	}
	if !l.degraded {
		t.Error("lease.degraded = false, want true when the API is forbidden")
	}
	l.Release() // must not block or panic
}

// TestRunCutover_6289_RefusedWhenAnotherPodHoldsTheLease: the engine does not
// drive — no seed, no Jobs — while another live holder owns the Lease.
func TestRunCutover_6289_RefusedWhenAnotherPodHoldsTheLease(t *testing.T) {
	t.Setenv(envCutoverEngineHolder, "pod-new")
	objs := []k8sruntime.Object{
		statusCMWith(map[string]string{}),
		leaseHeldBy("pod-old", 2*time.Second, 30),
	}
	h, client := fakeHandlerWithCutover(t, objs...)
	installJobReactor(t, client, batchv1.JobComplete)
	created := cutoverJobCreateCounter(t, client)
	bus := newCutoverBroadcaster()
	h.SetCutoverBroadcaster(bus)

	h.runCutoverWith(context.Background(), &cutoverDeps{core: client, ns: cutoverTestNS}, hw290Chain(t),
		cutoverRunOpts{operatorRetry: true})

	if got := created(cutoverStepGiteaMirror); got != 0 {
		t.Errorf("Jobs minted = %d, want 0 — a second driver must not step the chain", got)
	}
	st, _ := readCutoverStatus(context.Background(), &cutoverDeps{core: client, ns: cutoverTestNS})
	if st["cutoverLastAttemptStartedAt"] != "" {
		t.Errorf("status was seeded by the refused driver: %v", st)
	}
	_, replay, cancel := bus.Subscribe()
	defer cancel()
	found := false
	for _, ev := range replay {
		if ev.Phase == cutoverPhaseRefused && strings.Contains(ev.Message, "held by pod-old") {
			found = true
		}
	}
	if !found {
		t.Errorf("no refusal event naming the holder; replay=%+v", replay)
	}
}

// TestRunCutover_6289_RecordsHolderAndReleasesOnExit: a normal run stamps the
// holder on the status ConfigMap and hands the Lease back when it ends.
func TestRunCutover_6289_RecordsHolderAndReleasesOnExit(t *testing.T) {
	t.Setenv(envCutoverEngineHolder, "catalyst-api-abc12")
	h, client := fakeHandlerWithCutover(t, statusCMWith(map[string]string{}))
	installJobReactor(t, client, batchv1.JobComplete)

	h.runCutoverWith(context.Background(), &cutoverDeps{core: client, ns: cutoverTestNS}, hw290Chain(t),
		cutoverRunOpts{operatorRetry: true})

	st, _ := readCutoverStatus(context.Background(), &cutoverDeps{core: client, ns: cutoverTestNS})
	if st["engineHolder"] != "catalyst-api-abc12" || st["engineHolderSince"] == "" {
		t.Errorf("holder not recorded: engineHolder=%q since=%q", st["engineHolder"], st["engineHolderSince"])
	}
	if st["cutoverComplete"] != "true" {
		t.Errorf("cutoverComplete = %q, want true", st["cutoverComplete"])
	}
	if got := *getLease(t, client).Spec.HolderIdentity; got != "" {
		t.Errorf("lease holder after the run = %q, want released (empty)", got)
	}
}

// TestStopCutoverEngine_6289_YieldsWithoutStampingFailure: the drain hook
// stops a mid-step engine; the step keeps its running row (no failedStep),
// the Job is left alone, and the Lease is released for the successor.
func TestStopCutoverEngine_6289_YieldsWithoutStampingFailure(t *testing.T) {
	t.Setenv(envCutoverEngineHolder, "pod-outgoing")
	h, client := fakeHandlerWithCutover(t, statusCMWith(map[string]string{}))
	// No Job reactor: the step's Job never completes, so the engine sits in
	// watchJobToCompletion until it is told to yield.
	deps := &cutoverDeps{core: client, ns: cutoverTestNS}
	if !h.cutoverBusFor().tryStartRun() {
		t.Fatal("could not claim the run flag")
	}
	go h.runCutoverWith(context.Background(), deps, hw290Chain(t), cutoverRunOpts{operatorRetry: true})

	// Wait until step 1 is running.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		st, _ := readCutoverStatus(context.Background(), deps)
		if st["step."+cutoverStepGiteaMirror+".result"] == "running" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	sctx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer scancel()
	h.StopCutoverEngine(sctx)

	bus := h.cutoverBusFor()
	bus.mu.Lock()
	running := bus.running
	bus.mu.Unlock()
	if running {
		t.Fatal("engine still running after StopCutoverEngine")
	}
	st, _ := readCutoverStatus(context.Background(), deps)
	if st["failedStep"] != "" || st["step."+cutoverStepGiteaMirror+".result"] != "running" {
		t.Errorf("a yielded engine stamped failure: failedStep=%q step1=%q", st["failedStep"], st["step."+cutoverStepGiteaMirror+".result"])
	}
	if got := *getLease(t, client).Spec.HolderIdentity; got != "" {
		t.Errorf("lease holder after yield = %q, want released", got)
	}
	jobs, _ := client.BatchV1().Jobs(cutoverTestNS).List(context.Background(), metav1.ListOptions{})
	if len(jobs.Items) != 1 {
		t.Errorf("Jobs = %d, want the in-flight Job left for the successor", len(jobs.Items))
	}
}

// TestHandleCutoverStart_6289_409WhenAnotherPodHoldsTheLease.
func TestHandleCutoverStart_6289_409WhenAnotherPodHoldsTheLease(t *testing.T) {
	t.Setenv(envCutoverEngineHolder, "pod-new")
	objs := []k8sruntime.Object{
		makeCutoverStepCM("cutover-step-01-gitea-mirror", cutoverStepGiteaMirror, 1, cutoverModeJob, minimalPodSpecYAML, ""),
		statusCMWith(map[string]string{}),
		leaseHeldBy("pod-old", time.Second, 30),
	}
	h, _ := fakeHandlerWithCutover(t, objs...)
	rec := httptest.NewRecorder()
	h.HandleCutoverStart(rec, httptest.NewRequest(http.MethodPost, "/api/v1/sovereign/cutover/start", nil))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "pod-old") {
		t.Fatalf("status=%d body=%s; want 409 naming pod-old", rec.Code, rec.Body.String())
	}
}

// TestResumeInterruptedCutover_6289_WaitsForPredecessorToLetGo: the startup
// resume does not race a predecessor that still renews; it waits for the
// Lease to lapse (SIGKILL case) and then drives.
func TestResumeInterruptedCutover_6289_WaitsForPredecessorToLetGo(t *testing.T) {
	t.Setenv(envCutoverEngineHolder, "pod-new")
	oldPoll := cutoverLeaseWaitPoll
	cutoverLeaseWaitPoll = 50 * time.Millisecond
	defer func() { cutoverLeaseWaitPoll = oldPoll }()

	attempt := time.Now().Add(-2 * time.Minute).UTC().Truncate(time.Second)
	objs := []k8sruntime.Object{
		makeCutoverStepCM("cutover-step-01-gitea-mirror", cutoverStepGiteaMirror, 1, cutoverModeJob, minimalPodSpecYAML, ""),
		statusCMWith(map[string]string{
			"cutoverStartedAt":                           attempt.Format(time.RFC3339),
			"cutoverLastAttemptStartedAt":                attempt.Format(time.RFC3339),
			"step." + cutoverStepGiteaMirror + ".result": "running",
		}),
		// pod-old renewed 1s ago with a 2s TTL — it lapses ~1s from now.
		leaseHeldBy("pod-old", time.Second, 2),
	}
	h, client := fakeHandlerWithCutover(t, objs...)
	installJobReactor(t, client, batchv1.JobComplete)
	created := cutoverJobCreateCounter(t, client)

	start := time.Now()
	h.ResumeInterruptedCutover(context.Background())
	waitForCutoverEngineIdle(t, h, 15*time.Second)

	if got := created(cutoverStepGiteaMirror); got != 1 {
		t.Fatalf("Jobs minted = %d, want 1 after the predecessor's lease lapsed", got)
	}
	if time.Since(start) < 500*time.Millisecond {
		t.Errorf("resume drove after %s — it did not wait for the live predecessor's lease", time.Since(start))
	}
	st, _ := readCutoverStatus(context.Background(), &cutoverDeps{core: client, ns: cutoverTestNS})
	if st["cutoverComplete"] != "true" || st["engineHolder"] != "pod-new" {
		t.Errorf("after resume: complete=%q holder=%q", st["cutoverComplete"], st["engineHolder"])
	}
}
