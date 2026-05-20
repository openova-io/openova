// Tests for the Self-Sovereignty Cutover endpoints (issue #792).
//
// What this file proves (matches the GATES checklist for #792):
//
//  1. parseCutoverStep — well-formed ConfigMap parses into a step
//     with the expected order/mode/podSpec; malformed ConfigMap
//     returns a typed error rather than silently skipping.
//  2. listCutoverSteps — multiple step ConfigMaps come back ordered
//     by `bp.openova.io/cutover-order`.
//  3. HandleCutoverStart — runs all discovered steps, creates a real
//     Job per `mode=job` step, waits for the DaemonSet ready signal
//     for `mode=daemonset-wait`, and patches the status ConfigMap
//     with `cutoverComplete=true` on success.
//  4. HandleCutoverStart — idempotent: a second invocation against an
//     already-complete status returns 200 + the durable snapshot
//     and does NOT re-run.
//  5. HandleCutoverStart — a Job that ends in JobFailed surfaces as
//     `failedStep` + `lastError` on the status ConfigMap; the engine
//     does NOT continue to subsequent steps.
//  6. HandleCutoverStatus — surfaces every status ConfigMap key as a
//     typed JSON response with promoted top-level fields.
//  7. HandleCutoverEvents — SSE replay-on-connect fires every prior
//     event, then live events stream in.
//  8. parseCutoverStep handles the daemonset-wait mode (registry-
//     pivot's special case) — the DaemonSet ref is derived from the
//     label or from a sane name-strip fallback.
package handler

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	fakek8s "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

// ── Fixtures ────────────────────────────────────────────────────────────────

const cutoverTestNS = "catalyst"

// makeCutoverStepCM builds a properly-labelled ConfigMap that
// listCutoverSteps will pick up.
func makeCutoverStepCM(name, stepName string, order int, mode string, podSpec, daemonset string) *corev1.ConfigMap {
	labels := map[string]string{
		cutoverStepPartOfLabel:    cutoverStepPartOfValue,
		cutoverStepComponentLabel: cutoverStepComponentValue,
		cutoverStepOrderLabel:     fmt.Sprintf("%d", order),
		cutoverStepModeLabel:      mode,
	}
	if daemonset != "" {
		labels[cutoverStepDaemonSetLabel] = daemonset
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cutoverTestNS,
			Labels:    labels,
		},
		Data: map[string]string{
			"stepName": stepName,
		},
	}
	if podSpec != "" {
		cm.Data["podSpec"] = podSpec
	}
	return cm
}

// minimalPodSpecYAML returns a syntactically valid corev1.PodSpec
// YAML with a single busybox container — enough for parseCutoverStep
// to succeed and for the fake clientset to round-trip the Job.
const minimalPodSpecYAML = `containers:
- name: cutover-step
  image: busybox:1.36
  command: ["/bin/sh", "-c", "echo step done"]
restartPolicy: Never
`

// makeReadyDaemonSet builds a DaemonSet whose Status fields claim
// every node is ready, so waitForDaemonSetReady terminates on the
// first poll.
func makeReadyDaemonSet(name string) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cutoverTestNS,
		},
		Status: appsv1.DaemonSetStatus{
			DesiredNumberScheduled: 3,
			NumberReady:            3,
		},
	}
}

// installJobReactor wires a reactor that auto-completes any Created
// Job by stamping a JobComplete=True condition. Without this the
// fake clientset would return the freshly-created Job indefinitely
// and watchJobToCompletion would block.
func installJobReactor(t *testing.T, client *fakek8s.Clientset, terminalCondition batchv1.JobConditionType) {
	t.Helper()
	client.PrependReactor("create", "jobs", func(action clienttesting.Action) (bool, k8sruntime.Object, error) {
		ca, ok := action.(clienttesting.CreateAction)
		if !ok {
			return false, nil, nil
		}
		job, ok := ca.GetObject().(*batchv1.Job)
		if !ok {
			return false, nil, nil
		}
		// Capture identity so the goroutine below can update Status.
		updated := job.DeepCopy()
		updated.Status.Conditions = []batchv1.JobCondition{{
			Type:   terminalCondition,
			Status: corev1.ConditionTrue,
			Reason: "Test",
		}}
		// We let the default tracker create the Job, then schedule an
		// Update so a later Get / Watch observes the terminal status.
		go func() {
			// Tiny delay so the Watch starts before the Update lands.
			time.Sleep(20 * time.Millisecond)
			_, _ = client.BatchV1().Jobs(job.Namespace).UpdateStatus(
				context.Background(), updated, metav1.UpdateOptions{},
			)
		}()
		return false, nil, nil // let default tracker create the Job
	})
}

// fakeHandlerWithCutover wires a Handler bound to a fake clientset
// pre-seeded with the given objects.
func fakeHandlerWithCutover(t *testing.T, objs ...k8sruntime.Object) (*Handler, *fakek8s.Clientset) {
	t.Helper()
	client := fakek8s.NewSimpleClientset(objs...)
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.SetCutoverDepsFactory(func() (*cutoverDeps, error) {
		return &cutoverDeps{core: client, ns: cutoverTestNS}, nil
	})
	return h, client
}

// ── Tests ───────────────────────────────────────────────────────────────────

func TestParseCutoverStep_ValidJob(t *testing.T) {
	cm := makeCutoverStepCM("cutover-step-01-gitea-mirror", "gitea-mirror", 1, cutoverModeJob, minimalPodSpecYAML, "")
	step, err := parseCutoverStep(*cm)
	if err != nil {
		t.Fatalf("parseCutoverStep: %v", err)
	}
	if step.order != 1 {
		t.Errorf("order = %d, want 1", step.order)
	}
	if step.stepName != "gitea-mirror" {
		t.Errorf("stepName = %q, want gitea-mirror", step.stepName)
	}
	if step.mode != cutoverModeJob {
		t.Errorf("mode = %q, want %q", step.mode, cutoverModeJob)
	}
	if step.podSpec == nil || len(step.podSpec.Containers) != 1 {
		t.Fatalf("podSpec must have one container; got %+v", step.podSpec)
	}
	if got := step.podSpec.Containers[0].Image; got != "busybox:1.36" {
		t.Errorf("container image = %q, want busybox:1.36", got)
	}
}

func TestParseCutoverStep_DaemonSetWaitDerivesNameFromCM(t *testing.T) {
	cm := makeCutoverStepCM("cutover-step-04-registry-pivot", "registry-pivot", 4, cutoverModeDaemonSetWait, "", "")
	step, err := parseCutoverStep(*cm)
	if err != nil {
		t.Fatalf("parseCutoverStep: %v", err)
	}
	if step.daemonsetRef != "registry-pivot" {
		t.Errorf("daemonsetRef = %q, want %q (must derive from cm name when label is absent)", step.daemonsetRef, "registry-pivot")
	}
}

func TestParseCutoverStep_MissingPodSpecForJobMode(t *testing.T) {
	cm := makeCutoverStepCM("cutover-step-01-x", "x", 1, cutoverModeJob, "", "")
	if _, err := parseCutoverStep(*cm); err == nil {
		t.Fatalf("expected error for job mode without podSpec")
	}
}

func TestParseCutoverStep_UnknownMode(t *testing.T) {
	cm := makeCutoverStepCM("cutover-step-01-x", "x", 1, "bogus-mode", minimalPodSpecYAML, "")
	if _, err := parseCutoverStep(*cm); err == nil {
		t.Fatalf("expected error for unknown mode")
	}
}

func TestParseCutoverStep_MissingOrderLabel(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cutover-step-x",
			Namespace: cutoverTestNS,
			Labels: map[string]string{
				cutoverStepPartOfLabel:    cutoverStepPartOfValue,
				cutoverStepComponentLabel: cutoverStepComponentValue,
			},
		},
		Data: map[string]string{"stepName": "x", "podSpec": minimalPodSpecYAML},
	}
	if _, err := parseCutoverStep(*cm); err == nil {
		t.Fatalf("expected error when cutover-order label is missing")
	}
}

func TestListCutoverSteps_OrdersByOrderLabel(t *testing.T) {
	objs := []k8sruntime.Object{
		makeCutoverStepCM("cutover-step-03-c", "c", 3, cutoverModeJob, minimalPodSpecYAML, ""),
		makeCutoverStepCM("cutover-step-01-a", "a", 1, cutoverModeJob, minimalPodSpecYAML, ""),
		makeCutoverStepCM("cutover-step-02-b", "b", 2, cutoverModeJob, minimalPodSpecYAML, ""),
	}
	_, client := fakeHandlerWithCutover(t, objs...)

	steps, err := listCutoverSteps(context.Background(), &cutoverDeps{core: client, ns: cutoverTestNS})
	if err != nil {
		t.Fatalf("listCutoverSteps: %v", err)
	}
	if len(steps) != 3 {
		t.Fatalf("got %d steps, want 3", len(steps))
	}
	want := []string{"a", "b", "c"}
	for i, s := range steps {
		if s.stepName != want[i] {
			t.Errorf("steps[%d].stepName = %q, want %q", i, s.stepName, want[i])
		}
	}
}

// TestHandleCutoverStart_RunsAllStepsAndPersistsCompleted runs the
// engine end-to-end against a fake clientset with two job-mode steps
// and one daemonset-wait step, asserts the Job creates land, the
// DaemonSet wait sees a ready DS, and the status ConfigMap finishes
// in cutoverComplete=true with every per-step row marked success.
func TestHandleCutoverStart_RunsAllStepsAndPersistsCompleted(t *testing.T) {
	objs := []k8sruntime.Object{
		makeCutoverStepCM("cutover-step-01-gitea-mirror", "gitea-mirror", 1, cutoverModeJob, minimalPodSpecYAML, ""),
		makeCutoverStepCM("cutover-step-02-harbor-projects", "harbor-projects", 2, cutoverModeJob, minimalPodSpecYAML, ""),
		makeCutoverStepCM("cutover-step-03-registry-pivot", "registry-pivot", 3, cutoverModeDaemonSetWait, "", "registry-pivot"),
		makeReadyDaemonSet("registry-pivot"),
	}
	h, client := fakeHandlerWithCutover(t, objs...)
	installJobReactor(t, client, batchv1.JobComplete)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sovereign/cutover/start", nil)
	h.HandleCutoverStart(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("HandleCutoverStart: status %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	// Wait for the engine goroutine to finish — the in-process running
	// flag flips back to false on completion.
	deadline := time.Now().Add(15 * time.Second)
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

	// Re-read the status ConfigMap.
	cm, err := client.CoreV1().ConfigMaps(cutoverTestNS).Get(context.Background(),
		cutoverStatusConfigMapName(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get status ConfigMap: %v", err)
	}
	if cm.Data["cutoverComplete"] != "true" {
		t.Errorf("cutoverComplete = %q, want true (data=%v)", cm.Data["cutoverComplete"], cm.Data)
	}
	for _, name := range []string{"gitea-mirror", "harbor-projects", "registry-pivot"} {
		key := "step." + name + ".result"
		if cm.Data[key] != "success" {
			t.Errorf("%s = %q, want success", key, cm.Data[key])
		}
	}
	if cm.Data["progressPercent"] != "100" {
		t.Errorf("progressPercent = %q, want 100", cm.Data["progressPercent"])
	}
	if cm.Data["failedStep"] != "" {
		t.Errorf("failedStep = %q, want empty", cm.Data["failedStep"])
	}
}

// TestHandleCutoverStart_IdempotentWhenComplete proves a second
// invocation against an already-complete status returns 200 with the
// durable snapshot and does NOT re-create any Jobs.
func TestHandleCutoverStart_IdempotentWhenComplete(t *testing.T) {
	preComplete := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cutoverStatusConfigMapName(),
			Namespace: cutoverTestNS,
		},
		Data: map[string]string{
			"cutoverComplete":          "true",
			"cutoverFinishedAt":        "2026-05-04T10:00:00Z",
			"step.gitea-mirror.result": "success",
		},
	}
	objs := []k8sruntime.Object{
		makeCutoverStepCM("cutover-step-01-gitea-mirror", "gitea-mirror", 1, cutoverModeJob, minimalPodSpecYAML, ""),
		preComplete,
	}
	h, client := fakeHandlerWithCutover(t, objs...)

	// Track how many Jobs got created — should stay at zero.
	jobCreates := 0
	client.PrependReactor("create", "jobs", func(action clienttesting.Action) (bool, k8sruntime.Object, error) {
		jobCreates++
		return false, nil, nil
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sovereign/cutover/start", nil)
	h.HandleCutoverStart(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("HandleCutoverStart: status %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if jobCreates != 0 {
		t.Errorf("created %d Jobs on idempotent /start call, want 0", jobCreates)
	}
	// Response body should reflect cutoverComplete=true.
	var resp cutoverStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v; body=%s", err, rec.Body.String())
	}
	if !resp.CutoverComplete {
		t.Errorf("response.cutoverComplete = false, want true")
	}
}

// TestHandleCutoverStart_FailsHaltAtFailedStep proves a failed Job
// stops the engine and persists the failure on the status ConfigMap.
// A second step must NOT run.
func TestHandleCutoverStart_FailsHaltAtFailedStep(t *testing.T) {
	objs := []k8sruntime.Object{
		makeCutoverStepCM("cutover-step-01-gitea-mirror", "gitea-mirror", 1, cutoverModeJob, minimalPodSpecYAML, ""),
		makeCutoverStepCM("cutover-step-02-harbor-projects", "harbor-projects", 2, cutoverModeJob, minimalPodSpecYAML, ""),
	}
	h, client := fakeHandlerWithCutover(t, objs...)
	installJobReactor(t, client, batchv1.JobFailed) // every Job fails

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sovereign/cutover/start", nil)
	h.HandleCutoverStart(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("HandleCutoverStart: status %d, want 200 (engine started); body=%s", rec.Code, rec.Body.String())
	}

	// Wait for engine to terminate.
	deadline := time.Now().Add(15 * time.Second)
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

	cm, err := client.CoreV1().ConfigMaps(cutoverTestNS).Get(context.Background(),
		cutoverStatusConfigMapName(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get status ConfigMap: %v", err)
	}
	if cm.Data["cutoverComplete"] == "true" {
		t.Errorf("cutoverComplete = true, want false (run failed)")
	}
	if cm.Data["failedStep"] != "gitea-mirror" {
		t.Errorf("failedStep = %q, want %q", cm.Data["failedStep"], "gitea-mirror")
	}
	if cm.Data["lastError"] == "" {
		t.Errorf("lastError empty; want operator-actionable string")
	}
	if cm.Data["step.gitea-mirror.result"] != "failed" {
		t.Errorf("step.gitea-mirror.result = %q, want failed", cm.Data["step.gitea-mirror.result"])
	}
	// Step 2 must NOT have started — its key should be absent or empty.
	if cm.Data["step.harbor-projects.startedAt"] != "" {
		t.Errorf("step.harbor-projects.startedAt = %q, must be empty (engine should have halted at step 1)", cm.Data["step.harbor-projects.startedAt"])
	}
}

// TestHandleCutoverStart_NoStepsFound surfaces 424 (Failed Dependency)
// when bp-self-sovereign-cutover has not been installed yet.
func TestHandleCutoverStart_NoStepsFound(t *testing.T) {
	h, _ := fakeHandlerWithCutover(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sovereign/cutover/start", nil)
	h.HandleCutoverStart(rec, req)
	if rec.Code != http.StatusFailedDependency {
		t.Errorf("status = %d, want %d (FailedDependency); body=%s",
			rec.Code, http.StatusFailedDependency, rec.Body.String())
	}
}

// TestHandleCutoverStatus_ReturnsTypedSnapshot proves /status promotes
// the well-known keys to typed top-level fields and reconstructs the
// per-step rows from the durable status keys.
func TestHandleCutoverStatus_ReturnsTypedSnapshot(t *testing.T) {
	preStatus := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cutoverStatusConfigMapName(),
			Namespace: cutoverTestNS,
		},
		Data: map[string]string{
			"cutoverComplete":                "false",
			"currentStep":                    "harbor-projects",
			"currentStepIndex":               "1",
			"totalSteps":                     "8",
			"progressPercent":                "12",
			"step.gitea-mirror.result":       "success",
			"step.gitea-mirror.startedAt":    "2026-05-04T10:00:00Z",
			"step.gitea-mirror.finishedAt":   "2026-05-04T10:01:30Z",
			"step.harbor-projects.result":    "running",
			"step.harbor-projects.startedAt": "2026-05-04T10:01:30Z",
		},
	}
	h, _ := fakeHandlerWithCutover(t, preStatus)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereign/cutover/status", nil)
	h.HandleCutoverStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp cutoverStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rec.Body.String())
	}
	if resp.CutoverComplete {
		t.Errorf("cutoverComplete = true, want false")
	}
	if resp.CurrentStep != "harbor-projects" {
		t.Errorf("currentStep = %q, want harbor-projects", resp.CurrentStep)
	}
	if resp.TotalSteps != 8 {
		t.Errorf("totalSteps = %d, want 8", resp.TotalSteps)
	}
	if resp.ProgressPercent != 12 {
		t.Errorf("progressPercent = %d, want 12", resp.ProgressPercent)
	}
	if len(resp.Steps) != 2 {
		t.Fatalf("steps length = %d, want 2", len(resp.Steps))
	}
	stepByName := map[string]cutoverStepStatus{}
	for _, s := range resp.Steps {
		stepByName[s.Name] = s
	}
	if g := stepByName["gitea-mirror"]; g.Result != "success" {
		t.Errorf("gitea-mirror.result = %q, want success", g.Result)
	}
	if g := stepByName["harbor-projects"]; g.Result != "running" {
		t.Errorf("harbor-projects.result = %q, want running", g.Result)
	}
}

// TestHandleCutoverEvents_ReplayAndLive proves the SSE handler replays
// buffered events to a late subscriber, surfaces a snapshot event,
// and tails live events as they're published.
func TestHandleCutoverEvents_ReplayAndLive(t *testing.T) {
	h, _ := fakeHandlerWithCutover(t)
	bus := h.cutoverBusFor()

	// Pre-publish a couple of events so replay-on-connect has something
	// to fire.
	bus.Publish(cutoverEvent{
		Time:    time.Now().UTC().Format(time.RFC3339),
		Phase:   cutoverPhaseStepStarted,
		Level:   "info",
		Step:    "gitea-mirror",
		Message: "step gitea-mirror started",
	})
	bus.Publish(cutoverEvent{
		Time:    time.Now().UTC().Format(time.RFC3339),
		Phase:   cutoverPhaseStepFinished,
		Level:   "info",
		Step:    "gitea-mirror",
		Message: "step gitea-mirror completed",
	})

	// Start the SSE handler in a goroutine and drive the response
	// through an httptest.ResponseRecorder + a custom flushable writer.
	rec := newFlushableRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereign/cutover/events", nil)
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	req = req.WithContext(ctx)

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.HandleCutoverEvents(rec, req)
	}()

	// Give the handler a beat to flush the replay buffer + snapshot.
	time.Sleep(150 * time.Millisecond)

	// Publish a live event after subscription.
	bus.Publish(cutoverEvent{
		Time:    time.Now().UTC().Format(time.RFC3339),
		Phase:   cutoverPhaseCompleted,
		Level:   "info",
		Message: "Self-Sovereignty Cutover completed successfully",
	})

	// Wait for the handler to return on the terminal-cutoverPhaseCompleted
	// auto-close.
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		cancel()
		<-done
	}

	body := rec.Body()
	// Replay must include both pre-published events.
	if !bytes.Contains(body, []byte("step gitea-mirror started")) {
		t.Errorf("replay missing first pre-published event; body=%s", body)
	}
	if !bytes.Contains(body, []byte("step gitea-mirror completed")) {
		t.Errorf("replay missing second pre-published event; body=%s", body)
	}
	// Snapshot event must be present.
	if !bytes.Contains(body, []byte("event: "+cutoverPhaseSnapshot)) {
		t.Errorf("missing snapshot SSE event; body=%s", body)
	}
	// Live event must be present.
	if !bytes.Contains(body, []byte("Self-Sovereignty Cutover completed successfully")) {
		t.Errorf("live event missing; body=%s", body)
	}
	// Each SSE record ends with a blank line — sanity-check that the
	// stream is well-formed by counting `data:` occurrences.
	if c := bytes.Count(body, []byte("data: ")); c < 3 {
		t.Errorf("expected at least 3 SSE data records (2 replay + snapshot + live), got %d", c)
	}
}

// TestStripCutoverStepPrefix exercises the daemonset-name fallback
// derivation used when the chart omits the explicit label.
func TestStripCutoverStepPrefix(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"cutover-step-04-registry-pivot", "registry-pivot"},
		{"cutover-step-12-foo-bar", "foo-bar"},
		{"cutover-step-no-number", "no-number"},
		{"unrelated-name", "unrelated-name"},
	}
	for _, tc := range cases {
		if got := stripCutoverStepPrefix(tc.in); got != tc.want {
			t.Errorf("stripCutoverStepPrefix(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestBuildCutoverStatusResponse_PromotesKeys proves the typed
// response builder recovers all the well-known keys.
func TestBuildCutoverStatusResponse_PromotesKeys(t *testing.T) {
	status := map[string]string{
		"cutoverComplete":   "true",
		"cutoverStartedAt":  "2026-05-04T10:00:00Z",
		"cutoverFinishedAt": "2026-05-04T10:30:00Z",
		"totalSteps":        "8",
		"progressPercent":   "100",
	}
	resp := buildCutoverStatusResponseFromMap(status, []string{"x"})
	if resp.State != "sovereign" {
		t.Errorf("State = %q, want sovereign", resp.State)
	}
	if !resp.CutoverComplete {
		t.Errorf("CutoverComplete = false, want true")
	}
	if resp.TotalSteps != 8 {
		t.Errorf("TotalSteps = %d, want 8", resp.TotalSteps)
	}
	if resp.ProgressPercent != 100 {
		t.Errorf("ProgressPercent = %d, want 100", resp.ProgressPercent)
	}
	if resp.CutoverStartedAt != "2026-05-04T10:00:00Z" {
		t.Errorf("CutoverStartedAt = %q, want %q", resp.CutoverStartedAt, "2026-05-04T10:00:00Z")
	}
	if len(resp.Steps) != 1 || resp.Steps[0].Name != "x" {
		t.Errorf("Steps = %+v, want one step named x", resp.Steps)
	}
}

// TestBuildCutoverStatusResponse_StateAlwaysDefined proves the
// `state` field is ALWAYS one of the two UI-parseable values
// (`tethered` | `sovereign`), regardless of how sparse or empty the
// underlying ConfigMap is. This is the wire-side fix for the
// `invalid CutoverState: <undefined>` regression seen on otech113
// (issue #933) where the UI's branded `parseCutoverState` threw
// because the API never emitted a state field.
func TestBuildCutoverStatusResponse_StateAlwaysDefined(t *testing.T) {
	cases := []struct {
		name   string
		status map[string]string
		want   string
	}{
		{
			name:   "empty status map → tethered",
			status: map[string]string{},
			want:   "tethered",
		},
		{
			name: "cutoverComplete=false → tethered",
			status: map[string]string{
				"cutoverComplete": "false",
			},
			want: "tethered",
		},
		{
			name: "cutoverComplete=true → sovereign",
			status: map[string]string{
				"cutoverComplete": "true",
			},
			want: "sovereign",
		},
		{
			name: "cutoverComplete missing entirely → tethered (default)",
			status: map[string]string{
				"currentStep": "harbor-projects",
			},
			want: "tethered",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := buildCutoverStatusResponseFromMap(tc.status, nil)
			if resp.State != tc.want {
				t.Errorf("State = %q, want %q", resp.State, tc.want)
			}
			if resp.State == "" {
				t.Error("State is empty — UI parseCutoverState would throw `invalid CutoverState: <undefined>`")
			}
		})
	}
}

// TestHandleCutoverStatus_StateFieldEmittedOnFreshSovereign proves the
// HTTP /status endpoint always includes a defined `state` field when
// no cutover has run yet (the otech113 regression scenario). The chart
// pre-creates the status ConfigMap with cutoverComplete="false"; the
// API must emit state="tethered" so the UI's branded parser accepts it.
func TestHandleCutoverStatus_StateFieldEmittedOnFreshSovereign(t *testing.T) {
	freshStatus := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cutoverStatusConfigMapName(),
			Namespace: cutoverTestNS,
		},
		Data: map[string]string{
			"cutoverComplete": "false",
			"totalSteps":      "8",
			"progressPercent": "0",
		},
	}
	h, _ := fakeHandlerWithCutover(t, freshStatus)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereign/cutover/status", nil)
	h.HandleCutoverStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// Decode into a generic map so we can assert the presence of `state`
	// at the JSON level — not the typed struct (which would default to "").
	var rawResp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &rawResp); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rec.Body.String())
	}
	state, ok := rawResp["state"]
	if !ok {
		t.Fatalf("response missing `state` field; body=%s", rec.Body.String())
	}
	stateStr, ok := state.(string)
	if !ok {
		t.Fatalf("state is not a string: %#v", state)
	}
	if stateStr != "tethered" {
		t.Errorf("state = %q, want tethered", stateStr)
	}
}

// ── test helpers ────────────────────────────────────────────────────────────

// flushableRecorder wraps httptest.ResponseRecorder with an http.Flusher
// implementation so the SSE handler's flusher.Flush() calls don't 500.
type flushableRecorder struct {
	*httptest.ResponseRecorder
	mu  []byte
	buf *bytes.Buffer
}

func newFlushableRecorder() *flushableRecorder {
	rec := httptest.NewRecorder()
	return &flushableRecorder{
		ResponseRecorder: rec,
		buf:              new(bytes.Buffer),
	}
}

func (r *flushableRecorder) Write(p []byte) (int, error) {
	n, err := r.ResponseRecorder.Write(p)
	r.buf.Write(p)
	return n, err
}

func (r *flushableRecorder) WriteString(s string) (int, error) {
	return r.Write([]byte(s))
}

func (r *flushableRecorder) Flush() {
	// httptest.ResponseRecorder doesn't implement Flusher pre-Go 1.21;
	// nothing to do — Body() returns whatever has been written so far.
}

func (r *flushableRecorder) Body() []byte {
	return r.buf.Bytes()
}

// scanForSSEEvent returns the data lines for a named SSE event.
// Helper utility for tests that want to assert payload shape rather
// than substring matches.
func scanForSSEEvent(body []byte, eventName string) []string {
	var out []string
	scanner := bufio.NewScanner(bytes.NewReader(body))
	inEvent := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			inEvent = strings.TrimPrefix(line, "event: ") == eventName
			continue
		}
		if inEvent && strings.HasPrefix(line, "data: ") {
			out = append(out, strings.TrimPrefix(line, "data: "))
			inEvent = false
		}
	}
	return out
}

// Suppress unused warnings on test-only helpers.
var _ = scanForSSEEvent

// ── TBD-V13 startup resume ──────────────────────────────────────────────────

// TestResumeInterruptedCutover_ResumesAndCompletes simulates the t38
// (2026-05-19) failure mode: catalyst-api Pod restarts mid-cutover with
// step 1 already succeeded and step 2 stuck in "running". The fresh Pod
// calls ResumeInterruptedCutover at startup; the engine must re-run
// step 2 (NOT skip it — running != success), then step 3, and finish
// with cutoverComplete=true.
func TestResumeInterruptedCutover_ResumesAndCompletes(t *testing.T) {
	preStatus := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cutoverStatusConfigMapName(),
			Namespace: cutoverTestNS,
		},
		Data: map[string]string{
			"cutoverComplete":  "false",
			"cutoverStartedAt": "2026-05-19T07:14:00Z", // in-flight: started but not finished
			"totalSteps":       "3",
			"currentStep":      "step-two",
			"currentStepIndex": "1",
			"progressPercent":  "33",
			// Step 1 already succeeded.
			"step.step-one.result":     "success",
			"step.step-one.startedAt":  "2026-05-19T07:14:00Z",
			"step.step-one.finishedAt": "2026-05-19T07:14:30Z",
			// Step 2 was in flight when the Pod died.
			"step.step-two.result":    "running",
			"step.step-two.startedAt": "2026-05-19T07:14:30Z",
			"step.step-two.jobName":   "cutover-step-two-1747630470",
		},
	}
	objs := []k8sruntime.Object{
		makeCutoverStepCM("cutover-step-01-step-one", "step-one", 1, cutoverModeJob, minimalPodSpecYAML, ""),
		makeCutoverStepCM("cutover-step-02-step-two", "step-two", 2, cutoverModeJob, minimalPodSpecYAML, ""),
		makeCutoverStepCM("cutover-step-03-step-three", "step-three", 3, cutoverModeJob, minimalPodSpecYAML, ""),
		preStatus,
	}
	h, client := fakeHandlerWithCutover(t, objs...)
	installJobReactor(t, client, batchv1.JobComplete)

	// Track which steps actually got Job creates — step-one MUST NOT
	// be re-run (already success); step-two MUST be re-run; step-three
	// MUST be run.
	jobsCreated := map[string]int{}
	var mu_jobs sync.Mutex
	client.PrependReactor("create", "jobs", func(action clienttesting.Action) (bool, k8sruntime.Object, error) {
		ca, ok := action.(clienttesting.CreateAction)
		if !ok {
			return false, nil, nil
		}
		job, ok := ca.GetObject().(*batchv1.Job)
		if !ok {
			return false, nil, nil
		}
		mu_jobs.Lock()
		stepLabel := job.Labels["cutover.openova.io/step"]
		jobsCreated[stepLabel]++
		mu_jobs.Unlock()
		return false, nil, nil
	})

	// Fire the on-startup resume — this is what cmd/api/main.go calls
	// after the Handler is wired.
	h.ResumeInterruptedCutover(context.Background())

	// Wait for engine to terminate.
	deadline := time.Now().Add(15 * time.Second)
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

	// Inspect the durable status ConfigMap.
	cm, err := client.CoreV1().ConfigMaps(cutoverTestNS).Get(context.Background(),
		cutoverStatusConfigMapName(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get status ConfigMap: %v", err)
	}
	if cm.Data["cutoverComplete"] != "true" {
		t.Errorf("cutoverComplete = %q, want true; data=%+v", cm.Data["cutoverComplete"], cm.Data)
	}
	for _, name := range []string{"step-one", "step-two", "step-three"} {
		key := "step." + name + ".result"
		if cm.Data[key] != "success" {
			t.Errorf("%s = %q, want success", key, cm.Data[key])
		}
	}

	// Verify Job-create count: step-one MUST NOT have been re-created.
	mu_jobs.Lock()
	defer mu_jobs.Unlock()
	if jobsCreated["step-one"] != 0 {
		t.Errorf("step-one Job creates = %d, want 0 (was already success — must be skipped on resume)", jobsCreated["step-one"])
	}
	if jobsCreated["step-two"] != 1 {
		t.Errorf("step-two Job creates = %d, want 1 (was running — must be re-run on resume)", jobsCreated["step-two"])
	}
	if jobsCreated["step-three"] != 1 {
		t.Errorf("step-three Job creates = %d, want 1 (never started — must run on resume)", jobsCreated["step-three"])
	}
}

// TestResumeInterruptedCutover_NoOpWhenComplete proves the resume hook
// short-circuits cleanly when the cutover is already done — no Jobs
// created, no engine spawned.
func TestResumeInterruptedCutover_NoOpWhenComplete(t *testing.T) {
	preStatus := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cutoverStatusConfigMapName(),
			Namespace: cutoverTestNS,
		},
		Data: map[string]string{
			"cutoverComplete":   "true",
			"cutoverStartedAt":  "2026-05-19T07:14:00Z",
			"cutoverFinishedAt": "2026-05-19T07:25:00Z",
		},
	}
	objs := []k8sruntime.Object{
		makeCutoverStepCM("cutover-step-01-x", "x", 1, cutoverModeJob, minimalPodSpecYAML, ""),
		preStatus,
	}
	h, client := fakeHandlerWithCutover(t, objs...)

	jobCreates := 0
	client.PrependReactor("create", "jobs", func(action clienttesting.Action) (bool, k8sruntime.Object, error) {
		jobCreates++
		return false, nil, nil
	})

	h.ResumeInterruptedCutover(context.Background())

	// Quick wait — if the resume erroneously spawned the engine, give
	// it time to react before asserting.
	time.Sleep(100 * time.Millisecond)

	if jobCreates != 0 {
		t.Errorf("resume hook created %d Jobs on already-complete cutover, want 0", jobCreates)
	}
}

// TestResumeInterruptedCutover_NoOpWhenNeverStarted proves the resume
// hook does NOT pre-empt the chart's auto-trigger Job — when the
// cutover has never started (cutoverStartedAt empty), the hook is a
// no-op and the engine waits for the legitimate /start trigger.
func TestResumeInterruptedCutover_NoOpWhenNeverStarted(t *testing.T) {
	preStatus := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cutoverStatusConfigMapName(),
			Namespace: cutoverTestNS,
		},
		Data: map[string]string{
			"cutoverComplete": "false",
			// cutoverStartedAt intentionally empty
		},
	}
	objs := []k8sruntime.Object{
		makeCutoverStepCM("cutover-step-01-x", "x", 1, cutoverModeJob, minimalPodSpecYAML, ""),
		preStatus,
	}
	h, client := fakeHandlerWithCutover(t, objs...)

	jobCreates := 0
	client.PrependReactor("create", "jobs", func(action clienttesting.Action) (bool, k8sruntime.Object, error) {
		jobCreates++
		return false, nil, nil
	})

	h.ResumeInterruptedCutover(context.Background())
	time.Sleep(100 * time.Millisecond)

	if jobCreates != 0 {
		t.Errorf("resume hook created %d Jobs on never-started cutover, want 0", jobCreates)
	}
}
