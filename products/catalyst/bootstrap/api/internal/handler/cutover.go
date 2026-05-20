// cutover.go — Self-Sovereignty Cutover endpoints (issue #792).
//
// The "cutover" is the post-handover step that severs a Sovereign's
// remaining tethers to the openova-io mothership: it mirrors the
// GitOps repo into local Gitea, configures Harbor proxy-cache projects,
// pre-warms the bootstrap-kit images through the local Harbor, pivots
// containerd's registries.yaml on every Node so all upstream traffic
// flows through harbor.<sovereign-fqdn>, patches the flux-system
// GitRepository to point at the local Gitea, patches every
// HelmRepository's OCI URL to point at local Harbor, patches the
// catalyst-api Deployment env to forget the upstream GitOps URL, and
// finally proves the result with an egress-block self-test.
//
// The chart that ships these steps is bp-self-sovereign-cutover
// (issue #791). It installs DORMANT — no Helm post-install hook fires
// the Jobs. Instead, it publishes one "PodSpec ConfigMap" per step
// and a status ConfigMap. The Jobs are created HERE, by catalyst-api,
// when the operator hits POST /api/v1/sovereign/cutover/start.
//
// ── Contract with bp-self-sovereign-cutover ─────────────────────────────────
//
// Namespace:
//   - Default `catalyst` (the namespace catalyst-api itself runs in
//     after handover). Override via env CATALYST_CUTOVER_NAMESPACE.
//
// PodSpec ConfigMaps (one per step):
//   - Selected by labels:
//     app.kubernetes.io/part-of=self-sovereign-cutover
//     app.kubernetes.io/component=cutover-step
//   - Order encoded as the integer label `bp.openova.io/cutover-order`
//     (e.g. "1", "2", ..., "8"). Steps are sorted ascending.
//   - Mode encoded as the label `bp.openova.io/cutover-mode`:
//     "job" (default) — data.podSpec is YAML of corev1.PodSpec; the
//     handler creates a fresh batchv1.Job per run, watches to
//     completion, and treats Failed as a terminal cutover failure.
//     "daemonset-wait" — the chart already deployed a DaemonSet
//     with name = label `bp.openova.io/cutover-daemonset` (default
//     the ConfigMap name without the "cutover-step-NN-" prefix).
//     The handler waits for `.status.numberReady == .status.desiredNumberScheduled`
//     on every Node before declaring the step done.
//   - Step ConfigMap data keys:
//     data.stepName  — short slug used in the status ConfigMap and
//     Job naming (e.g. "gitea-mirror"). Required.
//     data.podSpec   — corev1.PodSpec YAML. Required for mode=job.
//
// Status ConfigMap:
//   - Name: `self-sovereign-cutover-status` (override via
//     CATALYST_CUTOVER_STATUS_CONFIGMAP).
//   - Created by the chart with `data.cutoverComplete = "false"`.
//   - Patched after every step transition by this handler. Keys:
//     cutoverComplete          "true" | "false"
//     cutoverStartedAt         RFC3339, set by /start on first run
//     cutoverFinishedAt        RFC3339, set when last step succeeds
//     currentStep              <stepName> | "" when idle
//     currentStepIndex         "0".."N"
//     totalSteps               "N"
//     progressPercent          "0".."100"
//     failedStep               <stepName> | "" when no failure
//     lastError                operator-actionable string | ""
//     step.<stepName>.startedAt
//     step.<stepName>.finishedAt
//     step.<stepName>.result   "success" | "failed" | "skipped"
//     step.<stepName>.jobName  Job/DaemonSet name (audit pointer)
//   - Idempotency anchor: when `cutoverComplete == "true"` POST /start
//     returns 200 with the existing state and does NOT re-run.
//
// ── Endpoints ───────────────────────────────────────────────────────────────
//
// POST /api/v1/sovereign/cutover/start
//   - Operator-admin only (RequireSession middleware in main.go).
//   - Returns 200 with the status snapshot. The cutover runs in a
//     background goroutine; the operator polls /status or subscribes
//     to /events for live progress.
//   - Idempotent: if cutoverComplete=true, returns the existing state.
//   - 409 if a cutover is already in progress on this Pod (in-process
//     mutex). A fresh Pod after a restart will see the partially-
//     written status ConfigMap and resume from the next pending step.
//
// GET /api/v1/sovereign/cutover/status
//   - Returns the status ConfigMap as JSON (keys promoted to top-level,
//     plus a typed `steps` array with each step's startedAt/finishedAt/
//     result/jobName).
//
// GET /api/v1/sovereign/cutover/events
//   - SSE stream of state-change events. Uses an in-memory broadcaster
//     per process (the cutover goroutine emits, the handler subscribes).
//   - Replay-on-connect from a small ring buffer so a wizard tab opened
//     after the cutover started sees the prior steps.
//
// ── Constraints honoured ────────────────────────────────────────────────────
//
//   - IaC-first: every cluster mutation goes via the in-cluster
//     kubernetes.Interface (Create Job / Patch ConfigMap / Get DaemonSet
//     / List ConfigMaps). NEVER bespoke cloud-API calls.
//   - Credential hygiene: this handler does NOT read any secrets. The
//     ConfigMaps it consumes carry only PodSpecs; secrets referenced by
//     those PodSpecs are mounted via secretRef envFrom — that's the
//     chart's concern.
//   - Event-driven: SSE not polling. The Watch on each Job uses the
//     informer-style Watch verb, not periodic GETs.
//   - Runtime-configurable: namespace, status ConfigMap name, per-step
//     timeouts all read from env per docs/INVIOLABLE-PRINCIPLES.md #4.
package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"
)

// ── Config knobs (every value runtime-overridable per principle #4) ─────────

const (
	// Default namespace the chart installs into and where the Jobs run.
	defaultCutoverNamespace = "catalyst"

	// Default name of the status ConfigMap. The chart pre-creates it
	// with `cutoverComplete: "false"`.
	defaultCutoverStatusConfigMap = "self-sovereign-cutover-status"

	// Per-step Job watch timeout. The cutover Jobs are short-running
	// (gitea-mirror is the longest at a few minutes for a clone of the
	// public openova repo). 15 minutes is a generous worst case.
	defaultCutoverStepTimeout = 15 * time.Minute

	// DaemonSet ready-wait timeout. registry-pivot rolls in seconds on
	// a small Sovereign and a few minutes on a large one.
	defaultDaemonSetReadyTimeout = 10 * time.Minute

	// Selector labels for cutover step ConfigMaps.
	cutoverStepPartOfLabel    = "app.kubernetes.io/part-of"
	cutoverStepPartOfValue    = "self-sovereign-cutover"
	cutoverStepComponentLabel = "app.kubernetes.io/component"
	cutoverStepComponentValue = "cutover-step"
	cutoverStepOrderLabel     = "bp.openova.io/cutover-order"
	cutoverStepModeLabel      = "bp.openova.io/cutover-mode"
	cutoverStepDaemonSetLabel = "bp.openova.io/cutover-daemonset"

	// Cutover modes.
	cutoverModeJob           = "job"
	cutoverModeDaemonSetWait = "daemonset-wait"

	// SSE phase names.
	cutoverPhaseStepStarted  = "cutover-step-started"
	cutoverPhaseStepFinished = "cutover-step-finished"
	cutoverPhaseStepFailed   = "cutover-step-failed"
	cutoverPhaseCompleted    = "cutover-completed"
	cutoverPhaseAlreadyDone  = "cutover-already-complete"
	cutoverPhaseStarted      = "cutover-started"
	cutoverPhaseSnapshot     = "cutover-status"

	// Env override names.
	envCutoverNamespace     = "CATALYST_CUTOVER_NAMESPACE"
	envCutoverStatusCM      = "CATALYST_CUTOVER_STATUS_CONFIGMAP"
	envCutoverStepTimeout   = "CATALYST_CUTOVER_STEP_TIMEOUT"
	envCutoverDaemonSetWait = "CATALYST_CUTOVER_DAEMONSET_TIMEOUT"
)

// ── In-process state ────────────────────────────────────────────────────────

// cutoverEvent is the SSE payload shape (a superset of provisioner.Event
// for visual parity with phase-1 events but without the Component/State
// fields the helmwatch bridge uses).
type cutoverEvent struct {
	Time    string `json:"time"`
	Phase   string `json:"phase"`
	Level   string `json:"level"` // info | warn | error
	Message string `json:"message"`
	Step    string `json:"step,omitempty"`
	JobName string `json:"jobName,omitempty"`
}

// cutoverBroadcaster is the in-process pub/sub for cutover events.
// One instance is wired into the Handler the first time a cutover-
// related endpoint is hit. The cutover goroutine emits via Publish;
// every active SSE subscriber receives a copy via Subscribe; a small
// ring buffer drives replay-on-connect for wizard tabs that open
// after the cutover started.
type cutoverBroadcaster struct {
	mu      sync.Mutex
	buf     []cutoverEvent
	subs    map[chan cutoverEvent]struct{}
	bufCap  int
	running bool // set true while a cutover goroutine is executing
}

func newCutoverBroadcaster() *cutoverBroadcaster {
	return &cutoverBroadcaster{
		subs:   make(map[chan cutoverEvent]struct{}),
		bufCap: 256,
	}
}

func (b *cutoverBroadcaster) Publish(ev cutoverEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.buf) >= b.bufCap {
		// Drop oldest — the cutover is short-lived and a wizard
		// reconnecting late just sees the most recent events.
		b.buf = append(b.buf[1:], ev)
	} else {
		b.buf = append(b.buf, ev)
	}
	for ch := range b.subs {
		// Non-blocking; subscribers must drain quickly. A slow
		// consumer drops messages rather than stalling the cutover.
		select {
		case ch <- ev:
		default:
		}
	}
}

func (b *cutoverBroadcaster) Subscribe() (chan cutoverEvent, []cutoverEvent, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan cutoverEvent, 64)
	b.subs[ch] = struct{}{}
	replay := append([]cutoverEvent(nil), b.buf...)
	cancel := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
	}
	return ch, replay, cancel
}

// tryStartRun atomically claims the in-process "cutover running" flag.
// Returns true if the caller acquired it; false if a previous goroutine
// still holds it. The caller MUST defer endRun on success.
func (b *cutoverBroadcaster) tryStartRun() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.running {
		return false
	}
	b.running = true
	return true
}

func (b *cutoverBroadcaster) endRun() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.running = false
}

// ── Handler wiring ──────────────────────────────────────────────────────────

// cutoverDeps bundles every external dependency the cutover engine
// needs. Production wires this from rest.InClusterConfig + a real
// kubernetes.Clientset; tests inject a fake.NewSimpleClientset.
type cutoverDeps struct {
	core kubernetes.Interface
	ns   string
}

// CutoverDepsFactory returns a fresh cutoverDeps. nil in production
// means "build from in-cluster config"; tests inject a closure that
// returns a fake clientset bound to a t.TempDir() namespace.
type CutoverDepsFactory func() (*cutoverDeps, error)

// SetCutoverDepsFactory wires a test-only factory. Production code
// leaves this nil and cutoverDepsFromEnv runs.
func (h *Handler) SetCutoverDepsFactory(f CutoverDepsFactory) {
	h.cutoverDepsFactory = f
}

// SetCutoverBroadcaster wires a test-only broadcaster. Useful when a
// test wants to seed a buffer or assert post-condition invariants.
// Production leaves this nil and the lazy lookup in cutoverBus()
// builds one on first use.
func (h *Handler) SetCutoverBroadcaster(b *cutoverBroadcaster) {
	h.cutoverBus = b
}

func (h *Handler) cutoverBusFor() *cutoverBroadcaster {
	h.cutoverBusOnce.Do(func() {
		if h.cutoverBus == nil {
			h.cutoverBus = newCutoverBroadcaster()
		}
	})
	return h.cutoverBus
}

func (h *Handler) cutoverDepsFor() (*cutoverDeps, error) {
	if h.cutoverDepsFactory != nil {
		return h.cutoverDepsFactory()
	}
	return cutoverDepsFromEnv()
}

func cutoverDepsFromEnv() (*cutoverDeps, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("cutover: in-cluster config unavailable: %w", err)
	}
	core, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("cutover: build core client: %w", err)
	}
	ns := os.Getenv(envCutoverNamespace)
	if ns == "" {
		ns = defaultCutoverNamespace
	}
	return &cutoverDeps{core: core, ns: ns}, nil
}

func cutoverStatusConfigMapName() string {
	if v := os.Getenv(envCutoverStatusCM); v != "" {
		return v
	}
	return defaultCutoverStatusConfigMap
}

func cutoverStepTimeout() time.Duration {
	if v, _ := time.ParseDuration(os.Getenv(envCutoverStepTimeout)); v > 0 {
		return v
	}
	return defaultCutoverStepTimeout
}

func cutoverDaemonSetTimeout() time.Duration {
	if v, _ := time.ParseDuration(os.Getenv(envCutoverDaemonSetWait)); v > 0 {
		return v
	}
	return defaultDaemonSetReadyTimeout
}

// ── Step discovery ──────────────────────────────────────────────────────────

// cutoverStep is the resolved view of a step ConfigMap.
type cutoverStep struct {
	order        int
	cmName       string
	stepName     string
	mode         string
	daemonsetRef string
	podSpec      *corev1.PodSpec
}

// listCutoverSteps reads every ConfigMap with the cutover labels in
// the cutover namespace and parses them into ordered cutoverStep
// structs. An invalid ConfigMap (missing required keys, malformed
// PodSpec YAML) is surfaced as an error so the operator can fix the
// chart values rather than silently skipping the step.
func listCutoverSteps(ctx context.Context, deps *cutoverDeps) ([]cutoverStep, error) {
	selector := fmt.Sprintf("%s=%s,%s=%s",
		cutoverStepPartOfLabel, cutoverStepPartOfValue,
		cutoverStepComponentLabel, cutoverStepComponentValue,
	)
	cms, err := deps.core.CoreV1().ConfigMaps(deps.ns).List(ctx, metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		return nil, fmt.Errorf("list cutover step ConfigMaps in %q: %w", deps.ns, err)
	}
	var out []cutoverStep
	for _, cm := range cms.Items {
		step, err := parseCutoverStep(cm)
		if err != nil {
			return nil, fmt.Errorf("ConfigMap %s/%s: %w", cm.Namespace, cm.Name, err)
		}
		out = append(out, step)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].order != out[j].order {
			return out[i].order < out[j].order
		}
		// Stable secondary sort on name so two steps with the same
		// order (chart bug) still produce a deterministic sequence
		// rather than a flake.
		return out[i].cmName < out[j].cmName
	})
	return out, nil
}

func parseCutoverStep(cm corev1.ConfigMap) (cutoverStep, error) {
	step := cutoverStep{cmName: cm.Name}
	if v := cm.Labels[cutoverStepOrderLabel]; v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return step, fmt.Errorf("label %s=%q is not an integer", cutoverStepOrderLabel, v)
		}
		step.order = n
	} else {
		return step, fmt.Errorf("missing required label %s", cutoverStepOrderLabel)
	}
	step.mode = cm.Labels[cutoverStepModeLabel]
	if step.mode == "" {
		step.mode = cutoverModeJob
	}
	step.stepName = strings.TrimSpace(cm.Data["stepName"])
	if step.stepName == "" {
		return step, fmt.Errorf("missing required data key %q", "stepName")
	}
	switch step.mode {
	case cutoverModeJob:
		raw := cm.Data["podSpec"]
		if strings.TrimSpace(raw) == "" {
			return step, fmt.Errorf("missing required data key %q for mode=%s", "podSpec", step.mode)
		}
		var ps corev1.PodSpec
		if err := yaml.Unmarshal([]byte(raw), &ps); err != nil {
			return step, fmt.Errorf("parse podSpec YAML: %w", err)
		}
		// Defensive: a zero-value PodSpec with no containers is a
		// chart bug.
		if len(ps.Containers) == 0 {
			return step, fmt.Errorf("podSpec has no containers")
		}
		// Cutover Jobs MUST NOT auto-restart on failure — a failed
		// step must surface as a terminal cutover failure, not silently
		// retry forever. Force restartPolicy=Never if the chart left
		// it empty (corev1 default for PodSpec is "Always" which is
		// invalid for batch Jobs anyway).
		if ps.RestartPolicy == "" {
			ps.RestartPolicy = corev1.RestartPolicyNever
		}
		step.podSpec = &ps
	case cutoverModeDaemonSetWait:
		ref := cm.Labels[cutoverStepDaemonSetLabel]
		if ref == "" {
			// Fallback: derive from cm name by stripping the
			// "cutover-step-NN-" prefix the chart uses by convention.
			ref = stripCutoverStepPrefix(cm.Name)
		}
		if ref == "" {
			return step, fmt.Errorf("mode=%s requires label %s or a derivable cm name", step.mode, cutoverStepDaemonSetLabel)
		}
		step.daemonsetRef = ref
	default:
		return step, fmt.Errorf("unknown cutover mode %q (want %q or %q)", step.mode, cutoverModeJob, cutoverModeDaemonSetWait)
	}
	return step, nil
}

// stripCutoverStepPrefix turns "cutover-step-04-registry-pivot" into
// "registry-pivot" so a daemonset-wait step can default its target by
// convention.
func stripCutoverStepPrefix(name string) string {
	const pfx = "cutover-step-"
	if !strings.HasPrefix(name, pfx) {
		return name
	}
	rest := name[len(pfx):]
	// Strip leading "NN-" if present.
	if i := strings.IndexByte(rest, '-'); i > 0 {
		// Confirm the first segment is numeric.
		head := rest[:i]
		if _, err := strconv.Atoi(head); err == nil {
			return rest[i+1:]
		}
	}
	return rest
}

// ── Status ConfigMap ────────────────────────────────────────────────────────

// readCutoverStatus returns the status ConfigMap or a zero map if it
// does not exist yet. NotFound is benign: the chart pre-creates it,
// but a misordered Helm install + first-time POST /start race would
// otherwise 503.
func readCutoverStatus(ctx context.Context, deps *cutoverDeps) (map[string]string, error) {
	name := cutoverStatusConfigMapName()
	cm, err := deps.core.CoreV1().ConfigMaps(deps.ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get status ConfigMap %s/%s: %w", deps.ns, name, err)
	}
	if cm.Data == nil {
		return map[string]string{}, nil
	}
	out := make(map[string]string, len(cm.Data))
	for k, v := range cm.Data {
		out[k] = v
	}
	return out, nil
}

// patchCutoverStatus issues a strategic-merge patch to the status
// ConfigMap, creating it with `cutoverComplete: "false"` if absent.
func patchCutoverStatus(ctx context.Context, deps *cutoverDeps, updates map[string]string) error {
	name := cutoverStatusConfigMapName()
	if updates == nil {
		updates = map[string]string{}
	}
	// Use a strategic-merge patch on .data — Server-Side Apply or
	// Update would race against a chart re-reconciliation; a merge
	// patch keys-by-key is the smallest possible blast radius.
	body := map[string]any{
		"data": updates,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal patch: %w", err)
	}
	_, err = deps.core.CoreV1().ConfigMaps(deps.ns).Patch(
		ctx, name, types.StrategicMergePatchType, raw, metav1.PatchOptions{},
	)
	if apierrors.IsNotFound(err) {
		// Create on the fly if the chart has not landed yet (the
		// chart will subsequently adopt the existing object via
		// metadata.ownerReferences when it next reconciles, since
		// our merge patch carries no ownerRef of its own).
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: deps.ns,
				Labels: map[string]string{
					cutoverStepPartOfLabel: cutoverStepPartOfValue,
				},
			},
			Data: updates,
		}
		// Always anchor cutoverComplete to a defined value on first
		// create so the idempotency check can read it without nil
		// checks.
		if _, ok := cm.Data["cutoverComplete"]; !ok {
			cm.Data["cutoverComplete"] = "false"
		}
		_, err = deps.core.CoreV1().ConfigMaps(deps.ns).Create(ctx, cm, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create status ConfigMap %s/%s: %w", deps.ns, name, err)
		}
		// AlreadyExists: a parallel writer beat us to it. Re-issue
		// the patch.
		if apierrors.IsAlreadyExists(err) {
			_, err = deps.core.CoreV1().ConfigMaps(deps.ns).Patch(
				ctx, name, types.StrategicMergePatchType, raw, metav1.PatchOptions{},
			)
			if err != nil {
				return fmt.Errorf("re-patch status ConfigMap %s/%s: %w", deps.ns, name, err)
			}
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("patch status ConfigMap %s/%s: %w", deps.ns, name, err)
	}
	return nil
}

// ── Job creation + watch ────────────────────────────────────────────────────

// cutoverJobName returns the deterministic Job name for a step. Each
// /start invocation appends an epoch suffix so a re-run after a
// failure does NOT collide with the leftover Job from the previous
// attempt — we rely on the Job's own GC for cleanup.
func cutoverJobName(stepName string, runEpoch int64) string {
	return fmt.Sprintf("cutover-%s-%d", stepName, runEpoch)
}

// createCutoverJob creates a fresh Job from the step's PodSpec.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #3 (Crossplane is the ONLY day-2
// IaC seam) cutover Jobs are one-shot bootstrap helpers, not declared
// state — they execute and exit. This is consistent with the existing
// hook-style Helm Jobs the bootstrap-kit uses elsewhere.
func createCutoverJob(ctx context.Context, deps *cutoverDeps, step cutoverStep, runEpoch int64) (*batchv1.Job, error) {
	name := cutoverJobName(step.stepName, runEpoch)
	// #968 — backoffLimit raised from 0 to 3 to absorb the gitea-mirror
	// step's known race against gitea-http endpoint publication. The
	// step Pod can land in scheduling within seconds of the gitea Pod
	// reaching Ready, before cluster-DNS endpoint propagation. One DNS
	// miss used to be terminal because the Job had no retry budget;
	// the cutover engine then aborted all 8 steps. With backoffLimit=3
	// + the per-step DNS readiness probe (chart-side), a single miss
	// is recoverable and steps still surface real failures (4× attempts
	// over the activeDeadlineSeconds window).
	backoffLimit := int32(3)
	ttl := int32(24 * 60 * 60) // 24h GC so the Job evidence stays around for audit.
	activeDeadline := int64(cutoverStepTimeout().Seconds())
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: deps.ns,
			Labels: map[string]string{
				cutoverStepPartOfLabel:    cutoverStepPartOfValue,
				cutoverStepComponentLabel: "cutover-job",
				"cutover.openova.io/step": step.stepName,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoffLimit,
			TTLSecondsAfterFinished: &ttl,
			ActiveDeadlineSeconds:   &activeDeadline,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						cutoverStepPartOfLabel:    cutoverStepPartOfValue,
						cutoverStepComponentLabel: "cutover-job",
						"cutover.openova.io/step": step.stepName,
					},
				},
				Spec: *step.podSpec,
			},
		},
	}
	created, err := deps.core.BatchV1().Jobs(deps.ns).Create(ctx, job, metav1.CreateOptions{})
	if err == nil {
		return created, nil
	}
	// TBD-V13: catalyst-api can restart mid-cutover. When the engine
	// resumes (see ResumeInterruptedCutover) it re-enters runCutover with
	// the same runEpoch only if the goroutine truly survived (it didn't),
	// but a NEW runEpoch from a re-trigger collides with a leftover Job
	// from the previous attempt only when names overlap — they don't,
	// because runEpoch is time-based. AlreadyExists therefore implies a
	// rare double-fire from two concurrent triggers (operator CTA +
	// auto-trigger Job hitting catalyst-api simultaneously). Treat the
	// existing Job as the same logical step and proceed — watchJobToCompletion
	// will pick it up.
	if apierrors.IsAlreadyExists(err) {
		existing, getErr := deps.core.BatchV1().Jobs(deps.ns).Get(ctx, name, metav1.GetOptions{})
		if getErr == nil {
			return existing, nil
		}
		return nil, fmt.Errorf("create Job %s/%s: AlreadyExists + Get failed: %w", deps.ns, name, getErr)
	}
	return nil, fmt.Errorf("create Job %s/%s: %w", deps.ns, name, err)
}

// watchJobToCompletion blocks until the Job reports a terminal
// condition (Complete or Failed). Returns the terminal condition type
// or an error on watch / context failure. Uses a single Watch call
// against the Job by name so we get an event-driven completion signal
// rather than polling.
func watchJobToCompletion(ctx context.Context, deps *cutoverDeps, jobName string) (batchv1.JobConditionType, error) {
	timeout := cutoverStepTimeout()
	wctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Sanity-check the current state up front so a Job that completed
	// between Create and Watch (rare but possible on a small cluster)
	// is recognised without waiting for the next event.
	if existing, err := deps.core.BatchV1().Jobs(deps.ns).Get(wctx, jobName, metav1.GetOptions{}); err == nil {
		if t, ok := terminalJobCondition(existing); ok {
			return t, nil
		}
	}

	w, err := deps.core.BatchV1().Jobs(deps.ns).Watch(wctx, metav1.ListOptions{
		FieldSelector: "metadata.name=" + jobName,
	})
	if err != nil {
		return "", fmt.Errorf("watch Job %s/%s: %w", deps.ns, jobName, err)
	}
	defer w.Stop()

	for {
		select {
		case <-wctx.Done():
			return "", fmt.Errorf("watch Job %s/%s: %w", deps.ns, jobName, wctx.Err())
		case ev, ok := <-w.ResultChan():
			if !ok {
				// Watch closed (resource version expired, server
				// flake). Fall back to a one-shot Get + retry the
				// watch up to the deadline.
				if existing, err := deps.core.BatchV1().Jobs(deps.ns).Get(wctx, jobName, metav1.GetOptions{}); err == nil {
					if t, ok := terminalJobCondition(existing); ok {
						return t, nil
					}
				}
				return "", fmt.Errorf("watch Job %s/%s: channel closed before terminal condition", deps.ns, jobName)
			}
			if ev.Type == watch.Error {
				return "", fmt.Errorf("watch Job %s/%s: error event: %#v", deps.ns, jobName, ev.Object)
			}
			job, ok := ev.Object.(*batchv1.Job)
			if !ok {
				continue
			}
			if t, ok := terminalJobCondition(job); ok {
				return t, nil
			}
		}
	}
}

// terminalJobCondition returns (Complete|Failed, true) if the Job has
// a terminal condition with status=True; otherwise (_, false).
func terminalJobCondition(job *batchv1.Job) (batchv1.JobConditionType, bool) {
	for _, c := range job.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		if c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed {
			return c.Type, true
		}
	}
	return "", false
}

// ── DaemonSet ready wait ────────────────────────────────────────────────────

// waitForDaemonSetReady polls (with exponential backoff) until
// `numberReady == desiredNumberScheduled` and `desiredNumberScheduled > 0`,
// or returns an error on context cancel / timeout. The chart already
// deployed the DaemonSet — this is a wait, not a create.
func waitForDaemonSetReady(ctx context.Context, deps *cutoverDeps, dsName string) error {
	timeout := cutoverDaemonSetTimeout()
	wctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return wait.PollUntilContextCancel(wctx, 3*time.Second, true, func(c context.Context) (bool, error) {
		ds, err := deps.core.AppsV1().DaemonSets(deps.ns).Get(c, dsName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			// The chart has not landed yet — keep polling, the
			// outer timeout bounds this.
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("get DaemonSet %s/%s: %w", deps.ns, dsName, err)
		}
		desired := ds.Status.DesiredNumberScheduled
		ready := ds.Status.NumberReady
		if desired > 0 && desired == ready {
			return true, nil
		}
		return false, nil
	})
}

// ── Cutover engine ──────────────────────────────────────────────────────────

// runCutover executes the discovered steps in order. It is run in a
// background goroutine spawned by HandleCutoverStart. Every state
// transition (started, finished, failed) is published to the
// broadcaster AND patched onto the status ConfigMap.
func (h *Handler) runCutover(ctx context.Context, deps *cutoverDeps, steps []cutoverStep) {
	bus := h.cutoverBusFor()
	defer bus.endRun()

	runEpoch := time.Now().Unix()
	totalSteps := len(steps)

	startedAt := time.Now().UTC()
	if err := patchCutoverStatus(ctx, deps, map[string]string{
		"cutoverStartedAt": startedAt.Format(time.RFC3339),
		"totalSteps":       strconv.Itoa(totalSteps),
		"cutoverComplete":  "false",
		"failedStep":       "",
		"lastError":        "",
	}); err != nil {
		h.publishCutoverEvent(bus, cutoverEvent{
			Time:    time.Now().UTC().Format(time.RFC3339),
			Phase:   cutoverPhaseStepFailed,
			Level:   "error",
			Message: fmt.Sprintf("cutover: failed to seed status ConfigMap: %v", err),
		})
		return
	}

	h.publishCutoverEvent(bus, cutoverEvent{
		Time:    startedAt.Format(time.RFC3339),
		Phase:   cutoverPhaseStarted,
		Level:   "info",
		Message: fmt.Sprintf("Self-Sovereignty Cutover started: %d steps", totalSteps),
	})

	// Skip steps already marked success (resume-after-restart). The
	// status ConfigMap is the durable record; if a previous run
	// completed step 1 and 2 then crashed before step 3, a fresh
	// /start picks up at step 3.
	priorStatus, _ := readCutoverStatus(ctx, deps)

	for i, step := range steps {
		percent := int(100 * (float64(i) / float64(totalSteps)))
		_ = patchCutoverStatus(ctx, deps, map[string]string{
			"currentStep":      step.stepName,
			"currentStepIndex": strconv.Itoa(i),
			"progressPercent":  strconv.Itoa(percent),
		})

		if priorStatus["step."+step.stepName+".result"] == "success" {
			h.publishCutoverEvent(bus, cutoverEvent{
				Time:    time.Now().UTC().Format(time.RFC3339),
				Phase:   cutoverPhaseStepFinished,
				Level:   "info",
				Step:    step.stepName,
				Message: fmt.Sprintf("step %s already succeeded on a prior run; skipping", step.stepName),
			})
			continue
		}

		if err := h.runCutoverStep(ctx, deps, step, runEpoch); err != nil {
			finishedAt := time.Now().UTC()
			_ = patchCutoverStatus(ctx, deps, map[string]string{
				"currentStep":                           "",
				"failedStep":                            step.stepName,
				"lastError":                             err.Error(),
				"step." + step.stepName + ".finishedAt": finishedAt.Format(time.RFC3339),
				"step." + step.stepName + ".result":     "failed",
			})
			h.publishCutoverEvent(bus, cutoverEvent{
				Time:    finishedAt.Format(time.RFC3339),
				Phase:   cutoverPhaseStepFailed,
				Level:   "error",
				Step:    step.stepName,
				Message: fmt.Sprintf("step %s failed: %v", step.stepName, err),
			})
			h.log.Error("cutover: step failed", "step", step.stepName, "err", err)
			return
		}
	}

	finishedAt := time.Now().UTC()
	_ = patchCutoverStatus(ctx, deps, map[string]string{
		"cutoverComplete":   "true",
		"cutoverFinishedAt": finishedAt.Format(time.RFC3339),
		"currentStep":       "",
		"currentStepIndex":  strconv.Itoa(totalSteps),
		"progressPercent":   "100",
	})
	h.publishCutoverEvent(bus, cutoverEvent{
		Time:    finishedAt.Format(time.RFC3339),
		Phase:   cutoverPhaseCompleted,
		Level:   "info",
		Message: "Self-Sovereignty Cutover completed successfully",
	})
}

func (h *Handler) runCutoverStep(ctx context.Context, deps *cutoverDeps, step cutoverStep, runEpoch int64) error {
	bus := h.cutoverBusFor()
	startedAt := time.Now().UTC()
	jobOrDS := ""

	switch step.mode {
	case cutoverModeJob:
		jobOrDS = cutoverJobName(step.stepName, runEpoch)
	case cutoverModeDaemonSetWait:
		jobOrDS = step.daemonsetRef
	}

	if err := patchCutoverStatus(ctx, deps, map[string]string{
		"step." + step.stepName + ".startedAt": startedAt.Format(time.RFC3339),
		"step." + step.stepName + ".result":    "running",
		"step." + step.stepName + ".jobName":   jobOrDS,
	}); err != nil {
		return fmt.Errorf("status patch (start): %w", err)
	}

	h.publishCutoverEvent(bus, cutoverEvent{
		Time:    startedAt.Format(time.RFC3339),
		Phase:   cutoverPhaseStepStarted,
		Level:   "info",
		Step:    step.stepName,
		JobName: jobOrDS,
		Message: fmt.Sprintf("step %s started (%s)", step.stepName, step.mode),
	})

	switch step.mode {
	case cutoverModeJob:
		if _, err := createCutoverJob(ctx, deps, step, runEpoch); err != nil {
			return err
		}
		cond, err := watchJobToCompletion(ctx, deps, jobOrDS)
		if err != nil {
			return err
		}
		if cond == batchv1.JobFailed {
			return fmt.Errorf("Job %s/%s reported Failed condition", deps.ns, jobOrDS)
		}
	case cutoverModeDaemonSetWait:
		if err := waitForDaemonSetReady(ctx, deps, jobOrDS); err != nil {
			return fmt.Errorf("DaemonSet %s/%s did not reach ready in %s: %w",
				deps.ns, jobOrDS, cutoverDaemonSetTimeout(), err)
		}
	default:
		return fmt.Errorf("internal error: unknown step mode %q", step.mode)
	}

	finishedAt := time.Now().UTC()
	if err := patchCutoverStatus(ctx, deps, map[string]string{
		"step." + step.stepName + ".finishedAt": finishedAt.Format(time.RFC3339),
		"step." + step.stepName + ".result":     "success",
	}); err != nil {
		return fmt.Errorf("status patch (success): %w", err)
	}

	h.publishCutoverEvent(bus, cutoverEvent{
		Time:    finishedAt.Format(time.RFC3339),
		Phase:   cutoverPhaseStepFinished,
		Level:   "info",
		Step:    step.stepName,
		JobName: jobOrDS,
		Message: fmt.Sprintf("step %s completed", step.stepName),
	})
	return nil
}

func (h *Handler) publishCutoverEvent(bus *cutoverBroadcaster, ev cutoverEvent) {
	if ev.Time == "" {
		ev.Time = time.Now().UTC().Format(time.RFC3339)
	}
	bus.Publish(ev)
}

// ── On-startup resume (TBD-V13) ─────────────────────────────────────────────
//
// ResumeInterruptedCutover is the on-startup auto-resume entrypoint. It is
// called once from cmd/api/main.go after the Handler is wired but BEFORE
// the HTTP server starts accepting requests.
//
// Background — why this exists
// ────────────────────────────
// The cutover engine runs as an in-process goroutine spawned by
// HandleCutoverStart / HandleCutoverInternalTrigger. If the catalyst-api
// Pod restarts mid-cutover (Pod evict, node drain, OOM, image bump), the
// goroutine dies. The status ConfigMap captures the durable record (which
// steps succeeded, which failed, which was running when the Pod died) but
// NOTHING auto-fires runCutover on the fresh Pod. The auto-trigger Helm Job
// only runs on `helm post-install,post-upgrade` — after the chart has
// already installed, a catalyst-api restart leaves the cutover stranded.
//
// TBD-V13 (t38 2026-05-19): exactly this happened — step 5 was mid-flight
// when catalyst-api restarted; on the fresh Pod nothing re-fired the
// engine; step 9 (gitea-token-mint) Job was never created; PR #2008's
// provisioning init-container waited forever for the cutover-step-09
// token annotation; tenant onboarding blocked permanently.
//
// Semantics
// ─────────
// Returns immediately if:
//   - The cutover deps factory is not wired (e.g. no in-cluster config —
//     local dev / tests without a fake factory).
//   - The status ConfigMap is missing (chart never installed; nothing to
//     resume).
//   - `cutoverComplete == "true"` (already done; nothing to do).
//   - `cutoverStartedAt == ""` (never started; the chart's auto-trigger
//     Job is responsible for the first fire — we MUST NOT pre-empt it
//     because the in-cluster trigger has different auth semantics).
//
// Otherwise — durable state shows a cutover was in progress when this
// process started — we:
//   1. Reset every step whose `.result == "running"` back to `""` so the
//      engine re-runs that step from scratch (the corresponding Job from
//      the previous attempt may have completed, failed, or still be
//      running; we don't trust the orphan and create a fresh Job — the
//      step's PodSpec must be idempotent, which it is by chart design —
//      every step's PodSpec is "ensure target state X").
//   2. List the cutover step ConfigMaps. If any are missing (chart
//      uninstalled mid-flight) we log and bail.
//   3. Claim the in-process running flag (always succeeds on a fresh Pod
//      since the broadcaster is freshly constructed).
//   4. Spawn runCutover with a background context so an init signal or
//      brief HTTP server hiccup doesn't cancel a multi-step resume.
//
// Idempotency vs the auto-trigger Job
// ───────────────────────────────────
// The Helm auto-trigger Job (post-install) fires once per chart install.
// On a fresh Pod after an in-flight cutover, BOTH this resume hook AND a
// stale auto-trigger Job retry could race to call runCutover. The
// in-process `tryStartRun()` flag prevents a duplicate goroutine — the
// loser receives `cutover-in-progress`/409 from the HTTP edge or is a
// no-op here.
//
// Concurrency safety
// ──────────────────
// This function is called once on startup with no parallel callers. We
// hold the in-process running flag for the duration of the spawned
// goroutine; HandleCutoverStart called against this Pod while resume is
// in flight returns 409 — the durable state will catch up via SSE.
func (h *Handler) ResumeInterruptedCutover(ctx context.Context) {
	deps, err := h.cutoverDepsFor()
	if err != nil {
		h.log.Info("cutover-resume: deps factory unavailable; skipping startup resume",
			"err", err,
		)
		return
	}

	status, err := readCutoverStatus(ctx, deps)
	if err != nil {
		h.log.Warn("cutover-resume: status read failed; skipping startup resume",
			"err", err,
		)
		return
	}

	if status["cutoverComplete"] == "true" {
		// Already done — nothing to resume.
		return
	}
	if status["cutoverStartedAt"] == "" {
		// Never started — defer to the chart's auto-trigger Job to
		// fire the first attempt. Resuming here would race the trigger
		// path AND mint a runEpoch with no operator audit trail.
		return
	}

	// In-flight detected: cutoverStartedAt != "" AND cutoverComplete != "true".
	// If a previous attempt failed terminally (failedStep != ""), we still
	// resume — the operator/auto-trigger will eventually re-POST anyway,
	// and resuming after a terminal failure surfaces the same final state
	// (the failed step re-runs; if it fails again the run terminates again
	// with the same error; if it passes this time, the cutover proceeds).
	// This matches the existing engine semantics where /start retries a
	// failed cutover by re-running from the next not-success step.
	h.log.Info("cutover-resume: in-flight cutover detected on startup",
		"currentStep", status["currentStep"],
		"currentStepIndex", status["currentStepIndex"],
		"failedStep", status["failedStep"],
	)

	// Reset every step whose .result == "running" back to "" so the
	// engine treats it as not-yet-attempted on this attempt. Without this,
	// the engine's skip-success-only check leaves the in-flight step in
	// a permanently-running state in the durable record.
	resetUpdates := map[string]string{}
	for k, v := range status {
		const pfx = "step."
		const sfx = ".result"
		if !strings.HasPrefix(k, pfx) || !strings.HasSuffix(k, sfx) {
			continue
		}
		if v == "running" {
			resetUpdates[k] = ""
			// Also clear startedAt so the audit trail shows the resume
			// attempt restarted this step cleanly. finishedAt was never
			// written (the step was in flight), so no clear needed.
			stepName := strings.TrimSuffix(strings.TrimPrefix(k, pfx), sfx)
			resetUpdates[pfx+stepName+".startedAt"] = ""
			resetUpdates[pfx+stepName+".jobName"] = ""
		}
	}
	if len(resetUpdates) > 0 {
		if err := patchCutoverStatus(ctx, deps, resetUpdates); err != nil {
			h.log.Warn("cutover-resume: failed to reset in-flight step rows; resume aborted",
				"err", err,
				"updates", len(resetUpdates),
			)
			return
		}
		h.log.Info("cutover-resume: reset in-flight step rows",
			"count", len(resetUpdates)/3, // 3 keys per step (result+startedAt+jobName)
		)
	}

	steps, err := listCutoverSteps(ctx, deps)
	if err != nil {
		h.log.Warn("cutover-resume: step ConfigMap discovery failed; resume aborted",
			"err", err,
		)
		return
	}
	if len(steps) == 0 {
		h.log.Warn("cutover-resume: no cutover-step ConfigMaps found; chart not installed?",
			"namespace", deps.ns,
		)
		return
	}

	bus := h.cutoverBusFor()
	if !bus.tryStartRun() {
		// Another goroutine on this same fresh Pod beat us to it (e.g.
		// auto-trigger Job hit the HTTP edge before main.go finished
		// wiring). Benign no-op.
		h.log.Info("cutover-resume: another run is already in flight on this Pod; skipping resume spawn")
		return
	}

	h.log.Info("cutover-resume: spawning runCutover to resume interrupted cutover",
		"totalSteps", len(steps),
	)
	go h.runCutover(context.Background(), deps, steps)
}

// ── HTTP handlers ───────────────────────────────────────────────────────────

// cutoverStatusResponse is the typed shape /status returns. Promotes
// the well-known keys to top-level fields for ergonomics; the raw
// per-step keys are also surfaced under `raw` so a UI can render
// arbitrary additional metadata the chart adds in the future without
// a client-side change.
//
// `State` is ALWAYS populated: "tethered" when the cutover hasn't
// completed (cold-start, mid-flight, or failed) and "sovereign" when
// cutoverComplete=true. The UI's branded `CutoverState` parser refuses
// any other value (or undefined), so the wire MUST never emit an empty
// state — that's what was rendering `invalid CutoverState: <undefined>`
// on otech113 (issue #933).
type cutoverStatusResponse struct {
	State             string              `json:"state"`
	CutoverComplete   bool                `json:"cutoverComplete"`
	CutoverStartedAt  string              `json:"cutoverStartedAt,omitempty"`
	CutoverFinishedAt string              `json:"cutoverFinishedAt,omitempty"`
	CurrentStep       string              `json:"currentStep,omitempty"`
	CurrentStepIndex  int                 `json:"currentStepIndex"`
	TotalSteps        int                 `json:"totalSteps"`
	ProgressPercent   int                 `json:"progressPercent"`
	FailedStep        string              `json:"failedStep,omitempty"`
	LastError         string              `json:"lastError,omitempty"`
	Steps             []cutoverStepStatus `json:"steps"`
	Raw               map[string]string   `json:"raw,omitempty"`
}

// cutoverStateValue returns the canonical wire string for the overall
// state, derived from the `cutoverComplete` flag. The UI's
// `parseCutoverState` accepts only "tethered" or "sovereign"; any
// other value (including the empty string or undefined) throws the
// `invalid CutoverState: <…>` error that surfaced on otech113. We
// canonicalise here so /status always answers a UI-parseable state
// regardless of the underlying ConfigMap state.
func cutoverStateValue(complete bool) string {
	if complete {
		return "sovereign"
	}
	return "tethered"
}

type cutoverStepStatus struct {
	Name       string `json:"name"`
	Result     string `json:"result"`
	StartedAt  string `json:"startedAt,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
	JobName    string `json:"jobName,omitempty"`
}

// HandleCutoverStart handles POST /api/v1/sovereign/cutover/start.
//
// Idempotent: when the status ConfigMap reports cutoverComplete=true
// the handler returns 200 with the existing snapshot and does NOT
// re-run. When a cutover is already in flight on this Pod the
// handler returns 409 (a fresh Pod after a restart will still be
// able to resume from the on-disk state via /start because the
// in-process running flag is per-Pod).
func (h *Handler) HandleCutoverStart(w http.ResponseWriter, r *http.Request) {
	deps, err := h.cutoverDepsFor()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "cutover-unconfigured",
			"detail": err.Error(),
		})
		return
	}

	status, err := readCutoverStatus(r.Context(), deps)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "status-read-failed",
			"detail": err.Error(),
		})
		return
	}

	if status["cutoverComplete"] == "true" {
		bus := h.cutoverBusFor()
		h.publishCutoverEvent(bus, cutoverEvent{
			Phase:   cutoverPhaseAlreadyDone,
			Level:   "info",
			Message: "cutover already complete; /start is a no-op",
		})
		resp := buildCutoverStatusResponseFromMap(status, listStepNamesFromStatus(status))
		writeJSON(w, http.StatusOK, resp)
		return
	}

	steps, err := listCutoverSteps(r.Context(), deps)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "step-discovery-failed",
			"detail": err.Error(),
		})
		return
	}
	if len(steps) == 0 {
		writeJSON(w, http.StatusFailedDependency, map[string]string{
			"error":  "no-steps-found",
			"detail": fmt.Sprintf("no cutover-step ConfigMaps found in namespace %q — bp-self-sovereign-cutover not installed?", deps.ns),
		})
		return
	}

	bus := h.cutoverBusFor()
	if !bus.tryStartRun() {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "cutover-in-progress",
			"detail": "a cutover is already running on this catalyst-api Pod",
		})
		return
	}

	// Run the engine with a context independent of the request so a
	// client disconnect doesn't cancel a multi-step cutover. The
	// cutoverStepTimeout on each step bounds the overall runtime.
	go h.runCutover(context.Background(), deps, steps)

	// Re-read status so the response reflects the freshly-patched
	// `cutoverStartedAt` from the engine — the goroutine has likely
	// already written it.
	freshStatus, _ := readCutoverStatus(r.Context(), deps)
	stepNames := make([]string, 0, len(steps))
	for _, s := range steps {
		stepNames = append(stepNames, s.stepName)
	}
	resp := buildCutoverStatusResponseFromMap(freshStatus, stepNames)
	resp.TotalSteps = len(steps)
	writeJSON(w, http.StatusOK, resp)
}

// HandleCutoverStatus handles GET /api/v1/sovereign/cutover/status.
func (h *Handler) HandleCutoverStatus(w http.ResponseWriter, r *http.Request) {
	deps, err := h.cutoverDepsFor()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "cutover-unconfigured",
			"detail": err.Error(),
		})
		return
	}
	status, err := readCutoverStatus(r.Context(), deps)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "status-read-failed",
			"detail": err.Error(),
		})
		return
	}
	// Pull step names from the status ConfigMap keys; the chart's
	// step ConfigMaps may be deleted post-cutover, so /status MUST
	// reconstruct from the durable status keys.
	stepNames := listStepNamesFromStatus(status)
	// Fall back to live discovery if the status has no per-step keys
	// yet (cutover never started).
	if len(stepNames) == 0 {
		if steps, err := listCutoverSteps(r.Context(), deps); err == nil {
			for _, s := range steps {
				stepNames = append(stepNames, s.stepName)
			}
		}
	}
	resp := buildCutoverStatusResponseFromMap(status, stepNames)
	writeJSON(w, http.StatusOK, resp)
}

// HandleCutoverEvents handles GET /api/v1/sovereign/cutover/events.
//
// Standard SSE protocol: text/event-stream, replay-on-connect from
// the broadcaster's ring buffer, then live tail. Closes when the
// client disconnects.
func (h *Handler) HandleCutoverEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	bus := h.cutoverBusFor()
	ch, replay, cancel := bus.Subscribe()
	defer cancel()

	// Replay-on-connect.
	for _, ev := range replay {
		writeCutoverSSE(w, ev)
	}
	// Snapshot of current status as the first typed event so a wizard
	// reconnecting after a Pod restart can render the durable state
	// without an extra GET /status call.
	if deps, err := h.cutoverDepsFor(); err == nil {
		if status, err := readCutoverStatus(r.Context(), deps); err == nil {
			snap, _ := json.Marshal(status)
			writeCutoverSSE(w, cutoverEvent{
				Time:    time.Now().UTC().Format(time.RFC3339),
				Phase:   cutoverPhaseSnapshot,
				Level:   "info",
				Message: string(snap),
			})
		}
	}
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, open := <-ch:
			if !open {
				return
			}
			writeCutoverSSE(w, ev)
			flusher.Flush()
			// Close the stream after the terminal event so a wizard
			// EventSource closes cleanly without requiring the
			// browser to time out.
			if ev.Phase == cutoverPhaseCompleted || ev.Phase == cutoverPhaseStepFailed {
				return
			}
		}
	}
}

func writeCutoverSSE(w http.ResponseWriter, ev cutoverEvent) {
	raw, err := json.Marshal(ev)
	if err != nil {
		// Marshalling our own struct should never fail; drop the
		// event with a comment line so the client sees something
		// useful.
		fmt.Fprintf(w, ": cutover: marshal failed: %v\n\n", err)
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Phase, raw)
}

// ── Status response builders ────────────────────────────────────────────────

func listStepNamesFromStatus(status map[string]string) []string {
	seen := map[string]struct{}{}
	for k := range status {
		const pfx = "step."
		if !strings.HasPrefix(k, pfx) {
			continue
		}
		rest := k[len(pfx):]
		// rest is "<stepName>.<field>"; find the last dot — step
		// names should not contain dots, but defend against it.
		idx := strings.LastIndex(rest, ".")
		if idx <= 0 {
			continue
		}
		name := rest[:idx]
		seen[name] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func buildCutoverStatusResponseFromMap(status map[string]string, stepNames []string) cutoverStatusResponse {
	resp := cutoverStatusResponse{
		Raw: status,
	}
	resp.CutoverComplete = status["cutoverComplete"] == "true"
	resp.State = cutoverStateValue(resp.CutoverComplete)
	resp.CutoverStartedAt = status["cutoverStartedAt"]
	resp.CutoverFinishedAt = status["cutoverFinishedAt"]
	resp.CurrentStep = status["currentStep"]
	if v, err := strconv.Atoi(status["currentStepIndex"]); err == nil {
		resp.CurrentStepIndex = v
	}
	if v, err := strconv.Atoi(status["totalSteps"]); err == nil {
		resp.TotalSteps = v
	}
	if v, err := strconv.Atoi(status["progressPercent"]); err == nil {
		resp.ProgressPercent = v
	}
	resp.FailedStep = status["failedStep"]
	resp.LastError = status["lastError"]

	resp.Steps = make([]cutoverStepStatus, 0, len(stepNames))
	for _, name := range stepNames {
		resp.Steps = append(resp.Steps, cutoverStepStatus{
			Name:       name,
			Result:     status["step."+name+".result"],
			StartedAt:  status["step."+name+".startedAt"],
			FinishedAt: status["step."+name+".finishedAt"],
			JobName:    status["step."+name+".jobName"],
		})
	}
	return resp
}
