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
	"log/slog"
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

	// #5014: on a Job-watch channel close (or watch-establish failure) the
	// engine re-checks the Job's ACTUAL state and re-establishes the watch
	// rather than failing the step — a closed watch is NORMAL k8s behaviour
	// (watch expiry, apiserver churn) and the catalyst-api Pod itself ROLLS
	// mid-cutover at step-07, which closes the engine's watch on a Job that
	// may already be Complete. These bound that re-watch loop so a Job that
	// genuinely never terminates cannot spin forever.
	defaultCutoverJobWatchRewatchMax     = 10
	defaultCutoverJobWatchRewatchBackoff = 2 * time.Second

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

	// Well-known step slugs (data.stepName on the step ConfigMaps). Keyed
	// by NAME — never positional index — because the step count has drifted
	// (8→11) as gitea-token-mint / vcluster-registry-pivot /
	// crossplane-provider-pivot were appended, so an index hook would have
	// silently fired against the wrong step. #3671: the engine flips
	// registriesYamlActive=v2 the moment harbor-prewarm succeeds and the
	// registry-pivot DaemonSet-wait then asserts per-node v2 acks.
	cutoverStepHarborPrewarm = "harbor-prewarm"
	cutoverStepRegistryPivot = "registry-pivot"
	// #5437: the SNAPSHOT step (its success is a point-in-time capture of a
	// moving upstream — see cutover_snapshot_steps.go) and the PIVOT BOUNDARY
	// step (from which the mirrored repo becomes the live GitOps source and
	// later steps commit sovereign-local changes onto it).
	cutoverStepGiteaMirror      = "gitea-mirror"
	cutoverStepFluxGitRepoPatch = "flux-gitrepository-patch"
	// #5014: the step whose Job applies the 10-min deny-egress hold. The
	// driver reaps its CiliumClusterwideNetworkPolicy on ANY step exit —
	// the Job's own TERM/EXIT traps cover clean paths, but a SIGKILLed pod
	// (activeDeadlineSeconds hard-kill after the grace window), a watch
	// loss, or an engine error LEAKED the policy 3x on hw242 (2026-07-12),
	// freezing every CSI volume attach until hand-healed.
	cutoverStepEgressBlockTest = "egress-block-test"

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
	// #5014 re-watch tuning: max number of times watchJobToCompletion
	// re-establishes a Job watch after a transient channel-close /
	// establish-error before it gives up, and the backoff between attempts.
	envCutoverJobWatchRewatchMax     = "CATALYST_CUTOVER_JOBWATCH_REWATCH_MAX"
	envCutoverJobWatchRewatchBackoff = "CATALYST_CUTOVER_JOBWATCH_REWATCH_BACKOFF"
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

// cutoverJobWatchRewatchMax is the #5014 re-watch budget: the maximum number
// of times watchJobToCompletion re-establishes a Job watch after a transient
// channel-close / watch-establish failure before it gives up. The overall
// step deadline (wctx) still caps wall-clock time; this bound additionally
// guards against a tight re-close loop (a chronically flaky apiserver, or a
// fake in a unit test) spinning forever without ever making progress.
// Runtime-overridable per Inviolable-Principle #4; malformed / non-positive
// values fall back to the default.
func cutoverJobWatchRewatchMax() int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(envCutoverJobWatchRewatchMax))); err == nil && v > 0 {
		return v
	}
	return defaultCutoverJobWatchRewatchMax
}

// cutoverJobWatchRewatchBackoff is the pause between re-watch attempts. A
// small delay keeps a synchronous re-close (e.g. an apiserver returning an
// immediately-closed watch, or a fake clientset in tests) from busy-spinning
// the CPU. Bounded by wctx so the overall step deadline still wins. Non-
// positive / malformed → default.
func cutoverJobWatchRewatchBackoff() time.Duration {
	if v, _ := time.ParseDuration(strings.TrimSpace(os.Getenv(envCutoverJobWatchRewatchBackoff))); v > 0 {
		return v
	}
	return defaultCutoverJobWatchRewatchBackoff
}

// cutoverStepDeadline returns the wall-clock budget for a single step's
// Job — the larger of the global default (cutoverStepTimeout) and the
// step's OWN Pod-template activeDeadlineSeconds carried in its PodSpec
// ConfigMap (the chart's stepTimeouts.<step>Seconds value).
//
// #3379 step-03 (hw139, 2026-06-15): the chart ships a deliberately
// generous per-step deadline for the heavy steps (harbor-prewarm 5400s,
// gitea-mirror 1200s, egress-block-test 1200s) by stamping
// `activeDeadlineSeconds` INSIDE each step's embedded PodSpec. But
// createCutoverJob / watchJobToCompletion previously used ONLY the global
// 15-minute cutoverStepTimeout for the Job-level deadline + the watch
// timeout — and the Job-level activeDeadlineSeconds is authoritative over
// the Pod-template one, so when global (900s) < per-step (e.g. 5400s) the
// Job was silently killed with reason=DeadlineExceeded at 15 minutes no
// matter what the chart asked for. harbor-prewarm genuinely needs the
// longer window (29 multi-layer private-image copies over throttled
// egress); the latent gitea-mirror/egress-block-test mismatch survived
// only because their real runtime happened to stay under 900s.
//
// Taking the MAX keeps the global default as a floor (a short step still
// gets the generous 15-minute cap; an env override of
// CATALYST_CUTOVER_STEP_TIMEOUT still raises every step) while letting a
// step opt INTO a longer budget via its chart value. The Job stays
// event-driven — watchJobToCompletion returns the instant the Job reaches
// a terminal condition — so a fast run never waits out the ceiling.
func cutoverStepDeadline(step cutoverStep) time.Duration {
	base := cutoverStepTimeout()
	if step.podSpec != nil && step.podSpec.ActiveDeadlineSeconds != nil {
		if d := time.Duration(*step.podSpec.ActiveDeadlineSeconds) * time.Second; d > base {
			return d
		}
	}
	return base
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
//
// TBD-V55: wraps the List in the same bounded-retry helper used by
// readCutoverStatus so a transient `apiserver not ready` during k3s
// warmup does not surface as a 502 from /cutover/start.
func listCutoverSteps(ctx context.Context, deps *cutoverDeps) ([]cutoverStep, error) {
	selector := fmt.Sprintf("%s=%s,%s=%s",
		cutoverStepPartOfLabel, cutoverStepPartOfValue,
		cutoverStepComponentLabel, cutoverStepComponentValue,
	)
	var cms *corev1.ConfigMapList
	err := wait.ExponentialBackoffWithContext(ctx, cutoverApiserverReadyBackoff, func(c context.Context) (bool, error) {
		got, lerr := deps.core.CoreV1().ConfigMaps(deps.ns).List(c, metav1.ListOptions{
			LabelSelector: selector,
		})
		if lerr == nil {
			cms = got
			return true, nil
		}
		if isApiserverNotReadyTransient(lerr) {
			return false, nil
		}
		return false, lerr
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

// One-shot read contract (TBD-V55 / #2131)
// ────────────────────────────────────────
//
// Every read in this section talks to the apiserver via the direct typed
// client (deps.core, built in cutoverDepsFromEnv via
// `kubernetes.NewForConfig(rest.InClusterConfig())`). These calls
// deliberately do NOT route through k8scache's in-process informer
// SharedInformerFactory — that factory is for streaming/list endpoints
// (SSE, dashboard) where cache-coherency matters more than first-byte
// latency. One-shot reads here (handful per HTTP request) belong on the
// direct path so a freshly-booted catalyst-api Pod can answer
// /cutover/start, /cutover/status, and /cutover/events before the
// informer factories have finished their initial LIST + WaitForCacheSync.
//
// The wrinkle that motivated TBD-V55 (t40, 2026-05-21 06:38Z):
//   POST /api/v1/sovereign/cutover/start
//     → HTTP 502 {"error":"status-read-failed",
//        "detail":"get status ConfigMap catalyst/self-sovereign-cutover-status:
//                  apiserver not ready"}
// while at the same instant `kubectl get cm` against the public LB
// kubeconfig from the bastion succeeded. The apiserver itself was up,
// but k3s on the Sovereign briefly returned `apiserver not ready` (HTTP
// 503 with that exact body) to in-cluster discovery + aggregated-API
// probes while admission webhooks finished warming. Returning 502 on a
// transient 503 is wrong: by the time the operator's wizard re-fetches
// 30 s later the apiserver is healthy. So one-shot reads here retry
// with a short bounded backoff (cutoverApiserverReadyBackoff) on
// recognised "transient apiserver-not-ready" errors and surface a
// terminal error only after the budget elapses.
//
// Direct client + bounded retry is the target-state per
// docs/PRINCIPLES.md #14 — no new abstraction, no informer-cache fall-
// back, no defensive null-guard ladder. Just the small retry the
// k3s-warm-up window requires.

// cutoverApiserverReadyBackoff is the per-call retry schedule applied
// to one-shot reads when the apiserver returns a transient
// `apiserver not ready` (HTTP 503) response. The total budget is
// ~5 s — long enough to cover the k3s admission-webhook warmup
// observed on t40 (typically <1 s, occasionally up to 3 s), short
// enough that a genuinely-broken apiserver still surfaces a clean
// 502 to the operator's UI within one wizard tick.
var cutoverApiserverReadyBackoff = wait.Backoff{
	Steps:    5,
	Duration: 150 * time.Millisecond,
	Factor:   2.0,
	Jitter:   0.1,
	Cap:      2 * time.Second,
}

// isApiserverNotReadyTransient reports whether the err is a transient
// "apiserver not ready" response that should be retried by a one-shot
// read. It recognises:
//
//   - apierrors.IsServiceUnavailable(err) — typed 503 from client-go.
//   - apierrors.IsTooManyRequests(err)     — typed 429 (admission rate-
//     limit during warm-up); same backoff is appropriate.
//   - err.Error() containing "apiserver not ready" — the literal k3s
//     warmup body that surfaced on t40. Substring match because some
//     client-go versions wrap the 503 as a generic StatusError with the
//     body in .Message rather than a typed sentinel.
//
// NotFound, Forbidden, Unauthorized, malformed-request etc. are NOT
// transient and must surface immediately. The retry budget caps how
// long the handler waits in any case.
func isApiserverNotReadyTransient(err error) bool {
	if err == nil {
		return false
	}
	if apierrors.IsServiceUnavailable(err) || apierrors.IsTooManyRequests(err) {
		return true
	}
	msg := err.Error()
	if strings.Contains(msg, "apiserver not ready") {
		return true
	}
	// k3s sometimes returns the warmup error with this prefix when the
	// aggregated-API layer is still loading SAs; bucket it the same way.
	if strings.Contains(msg, "the server is currently unable to handle the request") {
		return true
	}
	return false
}

// getCutoverStatusConfigMap is the direct-client one-shot Get with
// bounded retry-on-transient-503. Returns the ConfigMap, a typed
// NotFound (caller treats as benign empty state), or a terminal error
// after the backoff budget elapses.
func getCutoverStatusConfigMap(ctx context.Context, deps *cutoverDeps) (*corev1.ConfigMap, error) {
	name := cutoverStatusConfigMapName()
	var cm *corev1.ConfigMap
	err := wait.ExponentialBackoffWithContext(ctx, cutoverApiserverReadyBackoff, func(c context.Context) (bool, error) {
		got, err := deps.core.CoreV1().ConfigMaps(deps.ns).Get(c, name, metav1.GetOptions{})
		if err == nil {
			cm = got
			return true, nil
		}
		if apierrors.IsNotFound(err) {
			// NotFound is the terminal benign case — return it so the
			// caller's IsNotFound branch fires without burning the
			// retry budget.
			return false, err
		}
		if isApiserverNotReadyTransient(err) {
			// Retry — apiserver still warming up.
			return false, nil
		}
		// Any other error is terminal.
		return false, err
	})
	return cm, err
}

// readCutoverStatus returns the status ConfigMap or a zero map if it
// does not exist yet. NotFound is benign: the chart pre-creates it,
// but a misordered Helm install + first-time POST /start race would
// otherwise 503.
//
// TBD-V55: uses getCutoverStatusConfigMap so a transient
// `apiserver not ready` during k3s warmup retries within a short
// budget instead of bubbling up as a 502 to the operator's wizard.
func readCutoverStatus(ctx context.Context, deps *cutoverDeps) (map[string]string, error) {
	name := cutoverStatusConfigMapName()
	cm, err := getCutoverStatusConfigMap(ctx, deps)
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

// cutoverStepLabelKey is the label every cutover Job (created by
// createCutoverJob) carries with value == stepName. The reconcile
// loop in runCutoverStep queries by this label to detect Jobs that
// completed during a prior process lifetime — that's how the
// state-machine becomes idempotent across catalyst-api Pod restarts
// (TBD-V56 / #2132).
const cutoverStepLabelKey = "cutover.openova.io/step"

// findExistingJobsForStep returns every Job in the cutover namespace
// whose `cutover.openova.io/step` label matches stepName, with the
// most-recently-created Job first.
//
// This is the read-side seam that makes the state-machine idempotent
// across catalyst-api restarts. Pre-fix the engine only ever consulted
// the in-memory runEpoch when checking what Job to attach to — a Pod
// restart lost the epoch and a NEW Job got minted even when the
// previous Job had already reached Complete=True. That's how t40
// (2026-05-21) ran the 10-minute `egress-block-test` hold TWICE on
// the same Sovereign: Job 1 completed at 06:54:24Z, catalyst-api
// restarted at 07:07Z, the resume path created Job 2 from scratch at
// 07:07:23Z instead of attaching to the already-Complete Job 1.
//
// Post-fix the engine asks "is there already a Job for this step?"
// via this helper BEFORE creating a fresh one. Any Job stamped by
// createCutoverJob carries `cutover.openova.io/step=<stepName>`, so
// the lookup is exact.
func findExistingJobsForStep(ctx context.Context, deps *cutoverDeps, stepName string) ([]batchv1.Job, error) {
	selector := fmt.Sprintf("%s=%s", cutoverStepLabelKey, stepName)
	jobs, err := deps.core.BatchV1().Jobs(deps.ns).List(ctx, metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		return nil, fmt.Errorf("list existing Jobs for step %q in %q: %w", stepName, deps.ns, err)
	}
	// Newest first. The fake clientset rarely populates CreationTimestamp
	// but every real Job has one stamped by the apiserver, so the sort
	// is meaningful in production. Stable so equal timestamps preserve
	// input order.
	sort.SliceStable(jobs.Items, func(i, j int) bool {
		return jobs.Items[i].CreationTimestamp.After(jobs.Items[j].CreationTimestamp.Time)
	})
	return jobs.Items, nil
}

// findExistingTerminalJobForStep scans existing Jobs for a step and
// returns the first one that reached a terminal condition (Complete
// or Failed). Returns (nil, "", false) if no Job has terminated yet
// — the caller is then free to either attach to a still-running Job
// or create a fresh one.
//
// Prefers Complete over Failed when both exist for the same step,
// because a re-fired step (chart's auto-trigger Job retries after a
// transient failure) is a valid "success eventually" path. If only
// Failed exists, the caller surfaces that as the cutover failure
// — exactly the engine's existing semantics.
func findExistingTerminalJobForStep(ctx context.Context, deps *cutoverDeps, stepName string) (*batchv1.Job, batchv1.JobConditionType, bool) {
	jobs, err := findExistingJobsForStep(ctx, deps, stepName)
	if err != nil {
		return nil, "", false
	}
	var failedJob *batchv1.Job
	for i := range jobs {
		j := &jobs[i]
		cond, ok := terminalJobCondition(j)
		if !ok {
			continue
		}
		if cond == batchv1.JobComplete {
			return j, batchv1.JobComplete, true
		}
		// Hold the first Failed in case no Complete exists.
		if failedJob == nil {
			failedJob = j
		}
	}
	if failedJob != nil {
		return failedJob, batchv1.JobFailed, true
	}
	return nil, "", false
}

// findExistingRunningJobForStep returns the first non-terminal Job
// (still in flight) for a step, or nil. The engine attaches its watch
// to a running Job instead of minting a fresh one when this returns
// non-nil — covers the case where a previous process kicked off the
// Job and the apiserver / kubelet are still progressing it after the
// catalyst-api Pod restarted.
func findExistingRunningJobForStep(ctx context.Context, deps *cutoverDeps, stepName string) *batchv1.Job {
	jobs, err := findExistingJobsForStep(ctx, deps, stepName)
	if err != nil {
		return nil
	}
	for i := range jobs {
		j := &jobs[i]
		if _, terminal := terminalJobCondition(j); terminal {
			continue
		}
		return j
	}
	return nil
}

// jobCompletionTime returns the Job's completion timestamp in RFC3339,
// falling back to the latest condition's LastTransitionTime, then to
// time.Now() if the Job is malformed. The fallbacks are defensive — a
// terminal condition without timing data is a chart bug, but we'd
// rather emit a slightly-imprecise audit row than drop the success
// signal entirely.
func jobCompletionTime(job *batchv1.Job) time.Time {
	if job == nil {
		return time.Now().UTC()
	}
	if job.Status.CompletionTime != nil {
		return job.Status.CompletionTime.Time.UTC()
	}
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobComplete && c.Status == corev1.ConditionTrue && !c.LastTransitionTime.IsZero() {
			return c.LastTransitionTime.Time.UTC()
		}
	}
	return time.Now().UTC()
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
	// #3379: honor the step's OWN chart deadline (stepTimeouts.<step>Seconds,
	// carried on the PodSpec) when it exceeds the global default — otherwise
	// the Job-level cap silently kills heavy steps (harbor-prewarm) at the
	// 15-minute global before the chart's generous budget can apply. See
	// cutoverStepDeadline.
	activeDeadline := int64(cutoverStepDeadline(step).Seconds())
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
// or an error on genuine failure / context end. It watches the Job by
// name for an event-driven completion signal, but the step outcome is a
// function of the Job's ACTUAL state — never the watch channel's
// liveness.
//
// #5014 (hw242 step-03 harbor-prewarm, hw251 egress-block-test): a watch
// channel CLOSE is normal k8s behaviour — server-side watches expire /
// time out periodically, AND the catalyst-api Pod (which hosts this
// engine) ROLLS mid-cutover at step-07 (catalyst-api-env-patch), which
// restarts the engine and closes its Job watch. The Job itself may have
// already SUCCEEDED or may still be Running. The pre-fix code failed the
// STEP on any channel close whose immediate one-shot Get didn't yet show
// a terminal condition — a false-negative that halted a cutover which
// would otherwise reach cutoverComplete (harbor-prewarm ran ~80 min and
// its watch churned; the manual recovery was a single re-POST of the
// internal trigger that simply re-read the already-Complete Job).
//
// Post-fix: on a channel close OR a watch-establish error we RE-POLL the
// Job's real condition via Get and, if it is not yet terminal, RE-WATCH.
// This loops until the Job is genuinely terminal, the context ends
// (deadline / cancel), or the bounded re-watch budget is exhausted —
// only then does the step fail. A real JobFailed still fails the step; a
// cancelled/expired ctx still returns ctx.Err().
func watchJobToCompletion(ctx context.Context, deps *cutoverDeps, jobName string, timeout time.Duration) (batchv1.JobConditionType, error) {
	if timeout <= 0 {
		timeout = cutoverStepTimeout()
	}
	wctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	rewatchBudget := cutoverJobWatchRewatchMax()
	rewatches := 0

	for {
		// (Re-)poll the Job's ACTUAL state before (re-)establishing the
		// watch. Recognises a Job that completed between Create and Watch
		// (small clusters) OR during a watch gap after the channel closed
		// (the #5014 case) without waiting for a fresh event.
		if existing, err := deps.core.BatchV1().Jobs(deps.ns).Get(wctx, jobName, metav1.GetOptions{}); err == nil {
			if t, ok := terminalJobCondition(existing); ok {
				return t, nil
			}
		}

		w, err := deps.core.BatchV1().Jobs(deps.ns).Watch(wctx, metav1.ListOptions{
			FieldSelector: "metadata.name=" + jobName,
		})
		if err != nil {
			// #5014: a watch-establish failure is transient (apiserver
			// flake, or the catalyst-api Pod restarting mid-cutover). The
			// Job's real state was just polled above; retry establishing
			// the watch within budget instead of failing the step.
			retry, rerr := cutoverJobWatchRewatch(wctx, &rewatches, rewatchBudget, deps.ns, jobName,
				fmt.Errorf("establish watch: %w", err))
			if !retry {
				return "", rerr
			}
			continue
		}

		cond, closed, loopErr := awaitTerminalJobEvent(wctx, w, deps.ns, jobName)
		switch {
		case loopErr != nil:
			// ctx end (deadline/cancel) or a genuine watch.Error event.
			return "", loopErr
		case cond != "":
			// Terminal condition observed on the wire (Complete or Failed).
			return cond, nil
		case closed:
			// #5014: transient channel close. Loop back to re-Get + re-watch,
			// bounded by the re-watch budget.
			retry, rerr := cutoverJobWatchRewatch(wctx, &rewatches, rewatchBudget, deps.ns, jobName,
				fmt.Errorf("watch channel closed before terminal condition"))
			if !retry {
				return "", rerr
			}
			continue
		}
	}
}

// awaitTerminalJobEvent drains a single Job watch until it observes a
// terminal condition, the channel closes, the context ends, or a
// watch.Error event arrives. It Stops the watcher before returning.
//
// Returns exactly one of:
//   - (cond, false, nil)  — the Job reached a terminal condition (Complete|Failed)
//   - ("",   true,  nil)  — the watch channel closed (caller re-watches per #5014)
//   - ("",   false, err)  — the context ended, or a genuine watch.Error event
func awaitTerminalJobEvent(wctx context.Context, w watch.Interface, ns, jobName string) (batchv1.JobConditionType, bool, error) {
	defer w.Stop()
	for {
		select {
		case <-wctx.Done():
			return "", false, fmt.Errorf("watch Job %s/%s: %w", ns, jobName, wctx.Err())
		case ev, ok := <-w.ResultChan():
			if !ok {
				return "", true, nil
			}
			if ev.Type == watch.Error {
				return "", false, fmt.Errorf("watch Job %s/%s: error event: %#v", ns, jobName, ev.Object)
			}
			job, ok := ev.Object.(*batchv1.Job)
			if !ok {
				continue
			}
			if t, ok := terminalJobCondition(job); ok {
				return t, false, nil
			}
		}
	}
}

// cutoverJobWatchRewatch decides whether watchJobToCompletion should
// re-establish its Job watch after a transient failure (#5014). It
// increments the re-watch counter, enforces the budget, and applies a
// bounded backoff. Returns (true, nil) to signal "retry the watch"; on
// budget exhaustion or context end it returns (false, err) with a clear,
// operator-legible message that names the transient cause.
func cutoverJobWatchRewatch(wctx context.Context, rewatches *int, budget int, ns, jobName string, cause error) (bool, error) {
	if *rewatches >= budget {
		return false, fmt.Errorf(
			"watch Job %s/%s: %v; re-watch budget of %d exhausted without a terminal Job condition",
			ns, jobName, cause, budget)
	}
	*rewatches++
	select {
	case <-wctx.Done():
		// Overall step deadline / cancel wins over a re-watch attempt.
		return false, fmt.Errorf("watch Job %s/%s: %w", ns, jobName, wctx.Err())
	case <-time.After(cutoverJobWatchRewatchBackoff()):
		return true, nil
	}
}

// terminalJobCondition returns (Complete|Failed, true) if the Job has
// a terminal condition with status=True; otherwise (_, false).
// cutoverEgressBlockPolicyAbsPath is the REST path of the step-08 deny-egress
// CiliumClusterwideNetworkPolicy (cluster-scoped CRD object; cutoverDeps only
// carries a typed clientset, so the delete goes through the discovery
// RESTClient rather than adding a dynamic-client dependency to every test).
const cutoverEgressBlockPolicyAbsPath = "/apis/cilium.io/v2/ciliumclusterwidenetworkpolicies/cutover-egress-block"

// reapCutoverEgressPolicy deletes the step-08 deny-egress hold policy.
// #5014: called via defer on EVERY exit of the egress-block-test step —
// success, Job-Failed, watch loss, deadline, engine error — so a leaked
// cutover-egress-block CCNP can never outlive the step and freeze CSI
// attaches cluster-wide (leaked 3x on hw242, 2026-07-12: the Job's own
// TERM/EXIT trap does not run when the pod is SIGKILLed after the
// termination grace window, and a driver watch-loss abandons the Job
// entirely). NotFound is success (the Job's trap usually got there first).
// Overridable seam for unit tests (fake clientsets expose no REST client).
var reapCutoverEgressPolicy = func(ctx context.Context, deps *cutoverDeps) error {
	rc := deps.core.Discovery().RESTClient()
	if rc == nil {
		// Fake clientsets (unit tests) have no discovery REST client.
		return nil
	}
	if err := rc.Delete().AbsPath(cutoverEgressBlockPolicyAbsPath).Do(ctx).Error(); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

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

// jobFailedTransiently reports whether a terminal-Failed Job failed for a
// TRANSIENT reason that a retry can plausibly clear — specifically the
// activeDeadlineSeconds timeout (reason "DeadlineExceeded"), as opposed to
// a genuine non-zero application exit (reason "BackoffLimitExceeded",
// "PodFailurePolicy", etc.) where re-running the same idempotent PodSpec
// would just fail the same way and an operator must intervene.
//
// #3379 step-03 (hw139, 2026-06-15): Step-03 harbor-prewarm blew its
// deadline mid-copy (DeadlineExceeded) → the engine recorded the step
// result=failed → on every re-fire findExistingTerminalJobForStep
// re-surfaced that Failed Job and runCutoverStep refused to re-create it
// ("operator intervention required"), so the step NEVER re-executed and
// the cutover was permanently wedged at 03. A DeadlineExceeded is exactly
// the kind of failure a longer budget + parallel copies (this same PR)
// fix on the next attempt — treat it as transient so the resume/re-fire
// path deletes the timed-out Job and re-runs the step cleanly.
//
// We match on the Job's terminal JobFailed condition Reason, falling back
// to a Message substring scan for older API servers that populate only the
// message. Anything we don't positively recognise as transient is treated
// as a genuine failure (fail-closed: we'd rather halt for an operator than
// loop on a real error).
func jobFailedTransiently(job *batchv1.Job) bool {
	if job == nil {
		return false
	}
	for _, c := range job.Status.Conditions {
		if c.Type != batchv1.JobFailed || c.Status != corev1.ConditionTrue {
			continue
		}
		if c.Reason == batchv1.JobReasonDeadlineExceeded {
			return true
		}
		if strings.Contains(c.Message, "DeadlineExceeded") ||
			strings.Contains(c.Message, "exceeded active deadline") ||
			strings.Contains(c.Message, "activeDeadlineSeconds") {
			return true
		}
	}
	return false
}

// deleteCutoverJobsForStep removes every existing Job for a step (with
// background propagation so the Pods go too) so the engine can re-create a
// fresh Job for an idempotent re-run. Used when a prior Job failed
// transiently (jobFailedTransiently) — a leftover DeadlineExceeded Job
// would otherwise be re-discovered by findExistingTerminalJobForStep and
// re-surfaced as terminal forever. Best-effort: a delete error is logged
// by the caller but does not by itself abort the re-run attempt (the fresh
// Create would only fail on a true name collision, which the create path
// already tolerates via AlreadyExists).
func deleteCutoverJobsForStep(ctx context.Context, deps *cutoverDeps, stepName string) error {
	jobs, err := findExistingJobsForStep(ctx, deps, stepName)
	if err != nil {
		return err
	}
	propagation := metav1.DeletePropagationBackground
	var firstErr error
	for i := range jobs {
		j := &jobs[i]
		if delErr := deps.core.BatchV1().Jobs(deps.ns).Delete(ctx, j.Name, metav1.DeleteOptions{
			PropagationPolicy: &propagation,
		}); delErr != nil && !apierrors.IsNotFound(delErr) && firstErr == nil {
			firstErr = fmt.Errorf("delete prior Job %s/%s: %w", deps.ns, j.Name, delErr)
		}
	}
	return firstErr
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

// registryPivotNodeAckPrefix / Suffix bracket the per-node ack key the
// registry-pivot DaemonSet writes into the status ConfigMap AFTER it
// atomically swaps /etc/rancher/k3s/registries.yaml on its node. The key
// is `node.<nodeName>.registriesYaml = "v2"`. A Ready DaemonSet only
// proves its pods are running — NOT that each pod rewrote the file; this
// ack closes that gap (#3671 §5).
const (
	registryPivotNodeAckPrefix = "node."
	registryPivotNodeAckSuffix = ".registriesYaml"
)

// waitForRegistryPivotNodeAcks blocks until the number of per-node v2 acks
// in the status ConfigMap equals the DaemonSet's DesiredNumberScheduled
// (#3671). This is the REAL verification step-04 was missing: a Ready
// DaemonSet whose reconcile loop wrote the v1 (mothership) file — because
// registriesYamlActive was never flipped — would pass the bare
// waitForDaemonSetReady. With registriesYamlActive=v2 set before this DS
// runs (see runCutover), each node's reconcile loop swaps the file to the
// LOCAL Harbor and writes its ack; we count acks == desired so the cutover
// fails (rather than greens) if ANY node is still held at v1.
func waitForRegistryPivotNodeAcks(ctx context.Context, deps *cutoverDeps, dsName string) error {
	// Only meaningful once the engine has flipped registriesYamlActive=v2 (via
	// the harbor-prewarm hook). If it is still v1, the DaemonSet has NOT been
	// instructed to write the local file, so no v2 ack will ever arrive — the
	// ack-wait would just burn the whole timeout. In that case skip the wait;
	// the END-of-run invariant gate (#3671) fails the cutover for "v2 never
	// reached", which is the correct, fast verdict.
	if st, _ := readCutoverStatus(ctx, deps); st["registriesYamlActive"] != "v2" {
		return nil
	}
	timeout := cutoverDaemonSetTimeout()
	wctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return wait.PollUntilContextCancel(wctx, 5*time.Second, true, func(c context.Context) (bool, error) {
		ds, err := deps.core.AppsV1().DaemonSets(deps.ns).Get(c, dsName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("get DaemonSet %s/%s: %w", deps.ns, dsName, err)
		}
		desired := int(ds.Status.DesiredNumberScheduled)
		if desired == 0 {
			return false, nil
		}

		status, err := readCutoverStatus(c, deps)
		if err != nil {
			// Transient read failure — keep polling; the outer timeout bounds it.
			return false, nil
		}
		acks := countRegistryPivotV2Acks(status)
		// #4674 — tolerate a bounded number of laggard nodes. A single node
		// whose dfdaemon is persistently dead (stale resolver cache) never
		// writes its v2 ack, and the strict acks==desired gate would wedge the
		// WHOLE cutover on that one node for the full timeout. With a tolerance
		// of T, the gate proceeds once (desired-acks) <= T; the #4635
		// level-triggered reconciler keeps re-running the cutover so a laggard
		// that recovers still converges. Default T=0 preserves the strict
		// all-nodes invariant; raise via CATALYST_CUTOVER_ACK_TOLERANCE only for
		// clouds with flaky per-node dfdaemons (kom4dc). Un-acked nodes are
		// logged so the operator/reconciler can see exactly which node lagged.
		if desired-acks <= cutoverAckTolerance() {
			if acks < desired {
				slog.Warn("cutover: proceeding past registry-pivot ack-gate with laggard node(s) within tolerance",
					"acked", acks, "desired", desired,
					"tolerance", cutoverAckTolerance(),
					"unackedNodes", strings.Join(unackedRegistryPivotNodes(status), ","),
				)
			}
			return true, nil
		}
		return false, nil
	})
}

// envCutoverAckTolerance is the number of registry-pivot nodes allowed to miss
// their v2 ack before the step-04 gate still proceeds (#4674). Default 0 =
// strict all-nodes invariant.
const envCutoverAckTolerance = "CATALYST_CUTOVER_ACK_TOLERANCE"

// cutoverAckTolerance reads the laggard-node tolerance for the step-04 ack
// gate. Non-negative; malformed / negative → 0 (strict).
func cutoverAckTolerance() int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(envCutoverAckTolerance))); err == nil && v > 0 {
		return v
	}
	return 0
}

// unackedRegistryPivotNodes returns the node names present as ack keys whose
// value is NOT "v2" (still v1 / unset) — for operator-visible diagnostics when
// the gate proceeds within tolerance.
func unackedRegistryPivotNodes(status map[string]string) []string {
	var out []string
	for k, v := range status {
		if strings.HasPrefix(k, registryPivotNodeAckPrefix) &&
			strings.HasSuffix(k, registryPivotNodeAckSuffix) && v != "v2" {
			node := strings.TrimSuffix(strings.TrimPrefix(k, registryPivotNodeAckPrefix), registryPivotNodeAckSuffix)
			out = append(out, node)
		}
	}
	return out
}

// countRegistryPivotV2Acks counts the per-node ack keys in the status map
// whose value is exactly "v2".
func countRegistryPivotV2Acks(status map[string]string) int {
	n := 0
	for k, v := range status {
		if strings.HasPrefix(k, registryPivotNodeAckPrefix) &&
			strings.HasSuffix(k, registryPivotNodeAckSuffix) &&
			v == "v2" {
			n++
		}
	}
	return n
}

// ── Cutover engine ──────────────────────────────────────────────────────────

// runCutover executes the discovered steps in order. It is run in a
// background goroutine spawned by HandleCutoverStart. Every state
// transition (started, finished, failed) is published to the
// broadcaster AND patched onto the status ConfigMap.
//
// operatorRetry (#3379, hw139): when true — set ONLY by an operator-
// initiated POST /api/v1/sovereign/cutover/start (a deliberate human/CTA
// "retry" behind RequireSession) — a step whose prior Job failed GENUINELY
// (non-transient, e.g. BackoffLimitExceeded) is DELETED and RE-RUN rather
// than re-surfaced as a terminal wedge. This is the zero-touch path that
// lets a freshly-rolled chart fix re-drive a step the timeouts-only
// auto-resume (#3558) refuses to. The auto-resume + in-cluster auto-trigger
// pass false (fail-closed) so a genuinely-broken step never auto-loops.
func (h *Handler) runCutover(ctx context.Context, deps *cutoverDeps, steps []cutoverStep, operatorRetry bool) {
	bus := h.cutoverBusFor()
	defer bus.endRun()

	runEpoch := time.Now().Unix()
	totalSteps := len(steps)

	// Read the durable prior status BEFORE seeding so the audit start-time
	// (#3681) is written ONCE — at the first run — and never re-stamped by
	// a resume or re-fire. Before this fix the seed unconditionally
	// overwrote cutoverStartedAt with time.Now() at the TOP of every
	// runCutover call; a mid-run catalyst-api roll (which triggers
	// ResumeInterruptedCutover → runCutover) re-stamped it to the resume
	// moment, making the durable record claim an 11-minute cutover that
	// really took 35 (hw150: first step at 13:36, cutoverStartedAt rewritten
	// to 14:00 by the resume). The per-step rows were ground truth; the
	// top-level start was fiction. Now the FIRST attempt seeds
	// cutoverStartedAt and every attempt (incl. this one) advances a
	// separate cutoverLastAttemptStartedAt — the field used as the
	// resume/re-fire guard keeps its original meaning ("cutover began at
	// T0") while the new field carries "latest attempt began at Tn".
	priorStatus, _ := readCutoverStatus(ctx, deps)

	startedAt := time.Now().UTC()
	seed := map[string]string{
		"cutoverLastAttemptStartedAt": startedAt.Format(time.RFC3339),
		"totalSteps":                  strconv.Itoa(totalSteps),
		"cutoverComplete":             "false",
		"failedStep":                  "",
		"lastError":                   "",
	}
	if priorStatus["cutoverStartedAt"] == "" {
		// First run only — anchor the true start. On a resume/re-fire the
		// existing value is preserved (NOT in the seed map ⇒ untouched).
		seed["cutoverStartedAt"] = startedAt.Format(time.RFC3339)
	}

	// ── #5437 stale-snapshot re-run ─────────────────────────────────────
	//
	// Skip-on-success is correct idempotency for a step that ENSURES state,
	// and WRONG for a step whose output is a snapshot of a moving upstream.
	// gitea-mirror is the latter: its success only means "at time T the local
	// Gitea matched upstream", and every later step consumes that snapshot.
	// On hw290 (2026-07-27) a re-attempt 14h later skipped it while
	// harbor-prewarm re-ran fresh, so the mirror pinned catalyst-api:b1b472d
	// which the fresh prewarm never pushed → post-pivot ImagePullBackOff →
	// control plane down. Re-take the snapshot, and cascade-invalidate every
	// later step so no consumer carries a success computed from the old one.
	// The judgement lives in cutover_snapshot_steps.go.
	forceRerun := map[string]bool{}
	var snapshotNotices []string
	stepHasJobs := func(stepName string) bool {
		jobs, err := findExistingJobsForStep(ctx, deps, stepName)
		return err == nil && len(jobs) > 0
	}
	for i, step := range steps {
		d := decideSnapshotRerun(step, steps, priorStatus, startedAt, stepHasJobs)
		if d.reason != "" {
			snapshotNotices = append(snapshotNotices, d.reason)
		}
		if !d.rerun {
			continue
		}
		for _, s := range steps[i:] {
			forceRerun[s.stepName] = true
		}
		for k, v := range snapshotRerunCascade(steps, priorStatus, i) {
			seed[k] = v
		}
		break
	}

	if err := patchCutoverStatus(ctx, deps, seed); err != nil {
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

	// #5437 — surface the snapshot judgement on the wizard's event stream so
	// an operator sees WHY the mirror re-ran (or why it was kept).
	for _, notice := range snapshotNotices {
		h.publishCutoverEvent(bus, cutoverEvent{
			Time:    startedAt.Format(time.RFC3339),
			Phase:   cutoverPhaseStarted,
			Level:   "info",
			Step:    cutoverStepGiteaMirror,
			Message: notice,
		})
		h.log.Info("cutover: snapshot-step judgement", "detail", notice)
	}

	// ── #5359 secondary-region credential bridge ────────────────────────
	// Materialize every secondary region's kubeconfig into the
	// cutover-secondary-kubeconfigs Secret BEFORE any step runs, so the
	// chart's region-B legs (steps 05/06/08, chart 0.1.151) can pivot +
	// deny-egress-prove the secondary cluster(s) via `kubectl
	// --kubeconfig`. Runs on every entry (fresh fire, auto-trigger,
	// resume) — idempotent. FAIL-LOUD: a multi-region deployment whose
	// secondary kubeconfigs cannot all be materialized aborts here —
	// running the chain anyway would re-mint the exact #5359 false
	// positive (cc=true with region-B still tethered to github/ghcr).
	if nSecondaries, err := h.materializeSecondaryKubeconfigsSecret(ctx, deps); err != nil {
		finishedAt := time.Now().UTC()
		_ = patchCutoverStatus(ctx, deps, map[string]string{
			"currentStep": "",
			"failedStep":  "secondary-kubeconfigs",
			"lastError":   err.Error(),
		})
		h.publishCutoverEvent(bus, cutoverEvent{
			Time:    finishedAt.Format(time.RFC3339),
			Phase:   cutoverPhaseStepFailed,
			Level:   "error",
			Message: fmt.Sprintf("secondary-region kubeconfig materialization failed — cutover aborted before step 1 (#5359): %v", err),
		})
		return
	} else if nSecondaries > 0 {
		h.publishCutoverEvent(bus, cutoverEvent{
			Time:    time.Now().UTC().Format(time.RFC3339),
			Phase:   cutoverPhaseStarted,
			Level:   "info",
			Message: fmt.Sprintf("multi-region Sovereign: %d secondary region kubeconfig(s) materialized — steps 05/06/08 will pivot + deny-egress-prove every region (#5359)", nSecondaries),
		})
	}

	// priorStatus (read above, before the seed) is the durable record used
	// for resume-after-restart: if a previous run completed step 1 and 2
	// then crashed before step 3, a fresh /start picks up at step 3. The
	// seed never touches the per-step rows, so the snapshot read at the top
	// is the correct skip-success basis.

	// Project the Cutover group + a pending step Job per discovered step
	// (with the linear dependsOn chain) into the jobs/activity store so
	// the console /jobs canvas surfaces the EXECUTION — not just the
	// dormant chart-install row (issue #3646). Best-effort; never blocks
	// the cutover. Re-projecting the durable prior results first means a
	// resume already shows the real per-step states before this run's
	// transitions land.
	h.projectCutoverResumeSeed(stepNamesFromSteps(steps), priorStatus)

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
			// Already projected as succeeded by projectCutoverResumeSeed
			// above (the durable status carried result=success); nothing
			// more to do for the activity view.
			continue
		}

		// #5437 defence in depth — NEVER point Flux at a mirror snapshot older
		// than the attempt that pushed the artifacts into the local registry.
		// That pairing is the hw290 outage (stale mirror pins tags the fresh
		// harbor-prewarm never pushed → post-pivot ImagePullBackOff on the
		// control plane). Read the LIVE status so a mirror re-run earlier in
		// THIS attempt counts. Fail loud and early instead of silently
		// rolling the Sovereign onto unpullable tags.
		var stepErr error
		if step.stepName == cutoverStepFluxGitRepoPatch {
			live, readErr := readCutoverStatus(ctx, deps)
			if readErr != nil {
				h.log.Warn("cutover: could not re-read status for the pre-pivot mirror-freshness assert (#5437)",
					"err", readErr)
			} else {
				stepErr = assertMirrorSnapshotFresh(steps, live, startedAt)
			}
		}
		if stepErr == nil {
			stepErr = h.runCutoverStep(ctx, deps, step, runEpoch, operatorRetry, forceRerun[step.stepName])
		}
		if err := stepErr; err != nil {
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
			// Project the terminal failure onto the Cutover group so the
			// canvas shows the failed step (and the group rolls up to
			// failed) — never a misleading "succeeded" (issue #3646).
			h.projectCutoverStepFinished(step, "failed",
				fmt.Sprintf("step %s failed: %v", step.stepName, err), finishedAt)
			h.log.Error("cutover: step failed", "step", step.stepName, "err", err)
			return
		}

		// Faithful registry pivot (#3671). The node-level pivot that points
		// containerd at harbor.<fqdn> is gated by the registriesYamlActive
		// status key, which the registry-pivot DaemonSet's reconcile loop
		// reads to decide WHICH registries.yaml (v1=mothership, v2=local) to
		// write. Before this fix NOTHING in the engine ever set "v2", so the
		// DaemonSet dutifully (re)wrote the v1 (mothership) file on every
		// node — step registry-pivot greened while pivoting nothing. We flip
		// it to "v2" the moment harbor-prewarm SUCCEEDS (the local Harbor now
		// serves every bootstrap-kit image) and BEFORE the registry-pivot
		// DaemonSet-wait step, so the DS writes the LOCAL file. Keyed on the
		// step NAME, not a positional index (steps are label-ordered and the
		// numbering has drifted 8→11).
		if step.stepName == cutoverStepHarborPrewarm {
			if err := patchCutoverStatus(ctx, deps, map[string]string{
				"registriesYamlActive": "v2",
			}); err != nil {
				h.log.Warn("cutover: failed to flip registriesYamlActive=v2 after harbor-prewarm",
					"err", err)
			} else {
				h.publishCutoverEvent(bus, cutoverEvent{
					Time:    time.Now().UTC().Format(time.RFC3339),
					Phase:   cutoverPhaseStepFinished,
					Level:   "info",
					Step:    step.stepName,
					Message: "registriesYamlActive flipped to v2 — registry-pivot DaemonSet will write node containerd to local Harbor",
				})
			}
		}
	}

	finishedAt := time.Now().UTC()

	// Invariant gate (#3671): the cutover MUST NOT reach
	// cutoverComplete=true while the node-level registry pivot is still on
	// v1 (mothership Harbor). registriesYamlActive is flipped to "v2" by the
	// harbor-prewarm hook above and the registry-pivot DaemonSet acks per
	// node. If every step ran but the key is still "v1", the pivot silently
	// pivoted nothing — exactly the hw150 face-2 defect (step registry-pivot
	// result=success, node containerd still pointed at harbor.openova.io).
	// Fail the cutover here rather than seal a false sovereignty fact over a
	// still-tethered node.
	//
	// The gate applies ONLY when this chain actually CONTAINS a registry-pivot
	// step — i.e. the cutover is responsible for the node pivot. A partial /
	// custom chain that legitimately has no registry-pivot step (and never
	// flips the key) is not force-failed; there is nothing to assert.
	chainHasRegistryPivot := false
	for _, s := range steps {
		if s.stepName == cutoverStepRegistryPivot {
			chainHasRegistryPivot = true
			break
		}
	}
	final, _ := readCutoverStatus(ctx, deps)
	if active := final["registriesYamlActive"]; chainHasRegistryPivot && active != "v2" {
		failMsg := fmt.Sprintf("cutover refuses cutoverComplete=true: registriesYamlActive=%q (want v2) — node containerd still on mothership Harbor", active)
		_ = patchCutoverStatus(ctx, deps, map[string]string{
			"currentStep": "",
			"failedStep":  "registry-pivot",
			"lastError":   failMsg,
		})
		h.publishCutoverEvent(bus, cutoverEvent{
			Time:    finishedAt.Format(time.RFC3339),
			Phase:   cutoverPhaseStepFailed,
			Level:   "error",
			Step:    "registry-pivot",
			Message: failMsg,
		})
		h.log.Error("cutover: registry pivot invariant violated", "registriesYamlActive", active)
		return
	}

	// Determine the TRUE first-run start (#3681): if a prior attempt already
	// anchored cutoverStartedAt, that is the durable start; otherwise this run
	// was the first and startedAt is the anchor. This is the value the durable
	// seal records and the UI shows as total elapsed.
	trueStartedAt := priorStatus["cutoverStartedAt"]
	if trueStartedAt == "" {
		trueStartedAt = startedAt.Format(time.RFC3339)
	}

	// Seal the DURABLE, revert-immune sovereignty fact (#3667) in the SAME
	// transaction that flips the ConfigMap. The OpenBao seal survives a
	// `helm upgrade bp-self-sovereign-cutover` that reverts the ConfigMap
	// key to "false" — the resume hook + auto-trigger then read the seal
	// FIRST and no-op, so a routine chart pin never re-fires the 600s hold.
	if err := h.sealCutoverComplete(ctx, trueStartedAt, finishedAt.Format(time.RFC3339)); err != nil {
		// Non-fatal: the ConfigMap still records cutoverComplete=true so the
		// run is functionally done, but WITHOUT the seal a chart upgrade
		// could revert + re-fire. Surface loudly so the operator re-runs (the
		// re-run is idempotent and will re-attempt the seal).
		h.log.Error("cutover: durable cutover-complete seal FAILED; flag is chart-revertible until re-sealed", "err", err)
		h.publishCutoverEvent(bus, cutoverEvent{
			Time:    finishedAt.Format(time.RFC3339),
			Phase:   cutoverPhaseStepFailed,
			Level:   "error",
			Message: fmt.Sprintf("durable cutover-complete seal failed: %v — re-run /start to seal", err),
		})
	}

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

// forceRerun (#5437): the engine has decided this step must produce a FRESH
// result on this attempt — either it is the snapshot step whose recorded
// success predates the attempt, or it is a consumer invalidated by that
// snapshot being re-taken. It suppresses the Complete-Job adoption below,
// which would otherwise re-adopt yesterday's Job and hand back the very
// success the engine just decided to discard.
func (h *Handler) runCutoverStep(ctx context.Context, deps *cutoverDeps, step cutoverStep, runEpoch int64, operatorRetry, forceRerun bool) error {
	bus := h.cutoverBusFor()

	// ── #5014: deny-egress hold backstop ────────────────────────────────
	// Whatever way the egress-block-test step exits — success, Job Failed,
	// watch loss, step deadline, status-patch error — the cluster-wide
	// cutover-egress-block CCNP MUST NOT outlive it: a leaked hold policy
	// black-holes the IaaS provider API and freezes every CSI volume
	// attach on the Sovereign (leaked 3x live on hw242, 2026-07-12). The
	// Job's own TERM/EXIT traps cover the clean paths; this defer covers
	// the SIGKILL/watch-loss/error paths the traps cannot. A fresh context
	// is used deliberately: on watch-loss the step ctx is already dead —
	// exactly the leak path this closes.
	if step.stepName == cutoverStepEgressBlockTest {
		defer func() {
			rctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			// #5359 — reap the region-B hold policies too (best-effort;
			// same SIGKILL/watch-loss leak class, per-secondary cluster).
			h.reapSecondaryCutoverEgressPolicies(rctx)
			if err := reapCutoverEgressPolicy(rctx, deps); err != nil {
				h.publishCutoverEvent(bus, cutoverEvent{
					Time:    time.Now().UTC().Format(time.RFC3339),
					Phase:   cutoverPhaseStepFinished,
					Level:   "warn",
					Step:    step.stepName,
					Message: fmt.Sprintf("deny-egress policy backstop reap FAILED (manual `kubectl delete ccnp cutover-egress-block` required or CSI attaches stay frozen): %v", err),
				})
				return
			}
			h.publishCutoverEvent(bus, cutoverEvent{
				Time:    time.Now().UTC().Format(time.RFC3339),
				Phase:   cutoverPhaseStepFinished,
				Level:   "info",
				Step:    step.stepName,
				Message: "deny-egress hold policy reaped on step exit (#5014 backstop; NotFound = Job trap already cleaned)",
			})
		}()
	}

	// ── Idempotency check (TBD-V56 / #2132) ─────────────────────────────
	//
	// BEFORE writing `step.<name>.result=running` and minting a new Job,
	// look for a Job from a prior process lifetime that already settled
	// the step. This is the seam that survives catalyst-api Pod restarts:
	//
	//   1. If a Complete=True Job exists with the cutover.openova.io/step
	//      label, write success to the durable ConfigMap and return
	//      WITHOUT creating a new Job (no re-running 10-min holds).
	//   2. If only a Failed Job exists, surface the failure — the
	//      engine's halt semantics are unchanged from the pre-fix path.
	//   3. If a non-terminal Job exists, attach the watch to that Job
	//      name instead of minting a new one.
	//   4. If no Job exists, mint a fresh Job under the current runEpoch.
	//
	// For DaemonSet-wait steps this is a no-op (the chart owns the
	// DaemonSet lifecycle; we only wait for ready).
	if step.mode == cutoverModeJob {
		// #5437 — the Complete-Job adoption below is a SECOND skip path, and it
		// would silently undo the engine's re-run decision: a snapshot step
		// whose durable row was just invalidated would be handed right back the
		// stale success by yesterday's leftover Job. When the engine has ruled
		// the result stale, delete the step's Jobs + blank its rows FIRST, so
		// the adoption finds nothing and a fresh Job is minted (same shape as
		// the #3379 transient-retry reset below).
		if forceRerun {
			if job, cond, terminal := findExistingTerminalJobForStep(ctx, deps, step.stepName); terminal && cond == batchv1.JobComplete {
				h.publishCutoverEvent(bus, cutoverEvent{
					Time:    time.Now().UTC().Format(time.RFC3339),
					Phase:   cutoverPhaseStepStarted,
					Level:   "info",
					Step:    step.stepName,
					JobName: job.Name,
					Message: fmt.Sprintf("step %s prior Job %s completed on an earlier attempt but its result is a stale snapshot; deleting + re-running (#5437)", step.stepName, job.Name),
				})
				if delErr := deleteCutoverJobsForStep(ctx, deps, step.stepName); delErr != nil {
					h.log.Warn("cutover: failed to delete prior completed Job before a stale-snapshot re-run (#5437)",
						"step", step.stepName, "job", job.Name, "err", delErr)
				}
				if err := patchCutoverStatus(ctx, deps, map[string]string{
					"step." + step.stepName + ".result":     "",
					"step." + step.stepName + ".startedAt":  "",
					"step." + step.stepName + ".finishedAt": "",
					"step." + step.stepName + ".jobName":    "",
				}); err != nil {
					return fmt.Errorf("status patch (stale-snapshot reset): %w", err)
				}
			}
		}
		if job, cond, terminal := findExistingTerminalJobForStep(ctx, deps, step.stepName); terminal {
			finishedAt := jobCompletionTime(job)
			if cond == batchv1.JobComplete {
				// Recover the step's startedAt from the durable ConfigMap
				// if present, otherwise stamp it with the Job's CreationTimestamp
				// as a fallback so audit rows remain populated.
				priorStatus, _ := readCutoverStatus(ctx, deps)
				startedAt := priorStatus["step."+step.stepName+".startedAt"]
				if startedAt == "" {
					if !job.CreationTimestamp.IsZero() {
						startedAt = job.CreationTimestamp.UTC().Format(time.RFC3339)
					} else {
						startedAt = finishedAt.Format(time.RFC3339)
					}
				}
				if err := patchCutoverStatus(ctx, deps, map[string]string{
					"step." + step.stepName + ".startedAt":  startedAt,
					"step." + step.stepName + ".finishedAt": finishedAt.Format(time.RFC3339),
					"step." + step.stepName + ".result":     "success",
					"step." + step.stepName + ".jobName":    job.Name,
				}); err != nil {
					return fmt.Errorf("status patch (resume-success): %w", err)
				}
				h.publishCutoverEvent(bus, cutoverEvent{
					Time:    finishedAt.Format(time.RFC3339),
					Phase:   cutoverPhaseStepFinished,
					Level:   "info",
					Step:    step.stepName,
					JobName: job.Name,
					Message: fmt.Sprintf("step %s already completed by prior Job %s; advancing without re-running", step.stepName, job.Name),
				})
				// Project the carried-over success onto the Cutover group
				// so a resumed cutover shows the step succeeded (#3646).
				h.projectCutoverStepStarted(step,
					fmt.Sprintf("step %s recovered from prior Job %s", step.stepName, job.Name), finishedAt)
				h.projectCutoverStepFinished(step, "success",
					fmt.Sprintf("step %s already completed by prior Job %s", step.stepName, job.Name), finishedAt)
				return nil
			}
			// cond == batchv1.JobFailed.
			//
			// #3379 (hw139): re-run the step (delete the prior Job + fall
			// through to mint a fresh one) when EITHER:
			//
			//   (a) the prior Job failed TRANSIENTLY — it hit its
			//       activeDeadlineSeconds (reason DeadlineExceeded). The prior
			//       run simply ran out of wall-clock (e.g. step-03
			//       harbor-prewarm's image copies over throttled egress); a
			//       re-run with the now-larger budget + parallel copies can
			//       complete. Fires on BOTH auto-resume and operator /start
			//       (jobFailedTransiently is source-agnostic).
			//
			//   (b) operatorRetry — a human/CTA explicitly re-POSTed
			//       /cutover/start (behind RequireSession). A deliberate
			//       operator retry is the signal that the underlying cause was
			//       addressed out-of-band — typically a freshly-rolled chart
			//       fix that the timeouts-only auto-resume (#3558) won't
			//       re-drive on a GENUINE BackoffLimitExceeded. This is the
			//       zero-touch re-trigger for the hw139 step-10 residual-tether
			//       FATAL once 0.1.62 lands: the operator (or the BSS "Achieve
			//       True Sovereignty" CTA) re-fires /start and the now-fixed
			//       step re-runs clean. The in-cluster auto-trigger + startup
			//       resume pass operatorRetry=false, so a genuinely-broken step
			//       still fails-closed for them and never auto-loops.
			//
			// Every step's PodSpec is idempotent state-ensure logic, so
			// re-running is safe (already-pushed images are no-op skopeo
			// copies; already-pivoted HRs are SKIP patches).
			if jobFailedTransiently(job) || operatorRetry {
				reason := "failed transiently (deadline exceeded)"
				if !jobFailedTransiently(job) {
					reason = "failed (operator-initiated retry)"
				}
				h.publishCutoverEvent(bus, cutoverEvent{
					Time:    time.Now().UTC().Format(time.RFC3339),
					Phase:   cutoverPhaseStepStarted,
					Level:   "info",
					Step:    step.stepName,
					JobName: job.Name,
					Message: fmt.Sprintf("step %s prior Job %s %s; deleting + re-running", step.stepName, job.Name, reason),
				})
				if delErr := deleteCutoverJobsForStep(ctx, deps, step.stepName); delErr != nil {
					// Non-fatal: log via event and proceed. createCutoverJob
					// tolerates an AlreadyExists collision; a genuinely stuck
					// delete surfaces as the fresh watch timing out, not a
					// silent wedge.
					h.log.Warn("cutover: failed to delete prior failed Job before re-run",
						"step", step.stepName, "job", job.Name, "err", delErr)
				}
				// Clear the step's durable rows so the audit trail shows a
				// clean re-attempt and the engine doesn't treat it as
				// already-settled. Fall through to the normal create path.
				if err := patchCutoverStatus(ctx, deps, map[string]string{
					"step." + step.stepName + ".result":     "",
					"step." + step.stepName + ".startedAt":  "",
					"step." + step.stepName + ".finishedAt": "",
					"step." + step.stepName + ".jobName":    "",
				}); err != nil {
					return fmt.Errorf("status patch (failed-job reset): %w", err)
				}
				// Do not return — drop out of the idempotency block and
				// create a fresh Job below.
			} else {
				// Genuine failure (non-zero application exit, backoff-limit
				// exhausted, …) on a NON-operator path (auto-resume /
				// in-cluster auto-trigger, operatorRetry=false): surface as
				// the step's terminal failure — the engine halts at this step.
				// We DO NOT re-create because a second unattended attempt of an
				// idempotent PodSpec usually fails the same way; the operator
				// must address the cause then re-fire /start (which sets
				// operatorRetry=true and takes the re-run branch above).
				if err := patchCutoverStatus(ctx, deps, map[string]string{
					"step." + step.stepName + ".finishedAt": finishedAt.Format(time.RFC3339),
					"step." + step.stepName + ".result":     "failed",
					"step." + step.stepName + ".jobName":    job.Name,
				}); err != nil {
					return fmt.Errorf("status patch (resume-failed): %w", err)
				}
				return fmt.Errorf("Job %s/%s reported Failed condition (carried over from prior cutover attempt)", deps.ns, job.Name)
			}
		}
	}

	startedAt := time.Now().UTC()
	jobOrDS := ""
	var attachToExisting *batchv1.Job

	switch step.mode {
	case cutoverModeJob:
		// Prefer attaching to a still-running Job from a prior process
		// lifetime over minting a new one. This avoids the t40 failure
		// mode where two Jobs ran the SAME 10-min hold back-to-back.
		attachToExisting = findExistingRunningJobForStep(ctx, deps, step.stepName)
		if attachToExisting != nil {
			jobOrDS = attachToExisting.Name
		} else {
			jobOrDS = cutoverJobName(step.stepName, runEpoch)
		}
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

	startMsg := fmt.Sprintf("step %s started (%s)", step.stepName, step.mode)
	if attachToExisting != nil {
		startMsg = fmt.Sprintf("step %s resumed by attaching to in-flight Job %s", step.stepName, attachToExisting.Name)
	}
	h.publishCutoverEvent(bus, cutoverEvent{
		Time:    startedAt.Format(time.RFC3339),
		Phase:   cutoverPhaseStepStarted,
		Level:   "info",
		Step:    step.stepName,
		JobName: jobOrDS,
		Message: startMsg,
	})
	// Project the running transition onto the Cutover group's step Job
	// (issue #3646). The actual batch/v1 Job (jobOrDS) is what runs; this
	// row is its projection on the unified jobs canvas.
	h.projectCutoverStepStarted(step, startMsg, startedAt)

	switch step.mode {
	case cutoverModeJob:
		if attachToExisting == nil {
			if _, err := createCutoverJob(ctx, deps, step, runEpoch); err != nil {
				return err
			}
		}
		cond, err := watchJobToCompletion(ctx, deps, jobOrDS, cutoverStepDeadline(step))
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
		// #3671: for the registry-pivot DaemonSet, Ready is necessary but
		// NOT sufficient — a Ready DS whose loop wrote the v1 (mothership)
		// file passes the bare ready-wait. Assert every node ACKED the v2
		// (local-Harbor) swap; fail the step if any node is still on v1.
		if step.stepName == cutoverStepRegistryPivot {
			if err := waitForRegistryPivotNodeAcks(ctx, deps, jobOrDS); err != nil {
				return fmt.Errorf("registry-pivot DaemonSet %s/%s ready but not all nodes acked v2 registries.yaml in %s: %w",
					deps.ns, jobOrDS, cutoverDaemonSetTimeout(), err)
			}
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
	// Project the success transition onto the Cutover group's step Job
	// (issue #3646) so the canvas advances the step + rolls the group's
	// status up correctly.
	h.projectCutoverStepFinished(step, "success",
		fmt.Sprintf("step %s completed", step.stepName), finishedAt)
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
//  1. Reset every step whose `.result == "running"` back to `""` so the
//     engine re-runs that step from scratch (the corresponding Job from
//     the previous attempt may have completed, failed, or still be
//     running; we don't trust the orphan and create a fresh Job — the
//     step's PodSpec must be idempotent, which it is by chart design —
//     every step's PodSpec is "ensure target state X").
//  2. List the cutover step ConfigMaps. If any are missing (chart
//     uninstalled mid-flight) we log and bail.
//  3. Claim the in-process running flag (always succeeds on a fresh Pod
//     since the broadcaster is freshly constructed).
//  4. Spawn runCutover with a background context so an init signal or
//     brief HTTP server hiccup doesn't cancel a multi-step resume.
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

	// Durable, revert-immune sovereignty gate (#3667) — checked FIRST, before
	// the status ConfigMap. If a `helm upgrade bp-self-sovereign-cutover`
	// reverted the CM's cutoverComplete to "false" (and cutoverStartedAt to
	// ""), the CM-only checks below would NOT early-return on
	// cutoverComplete, but the cutoverStartedAt=="" guard would defer to the
	// auto-trigger Job — which then re-fires the whole cutover. The sealed
	// OpenBao fact survives the upgrade, so once it exists we backfill the CM
	// and return without resuming anything. A seal-read error fails closed
	// (skip resume) rather than risk a spurious re-fire.
	if sealed, serr := h.sovereignCutoverComplete(ctx); serr != nil {
		h.log.Warn("cutover-resume: durable seal check failed; skipping startup resume to avoid spurious re-fire",
			"err", serr,
		)
		return
	} else if sealed {
		if deps != nil {
			if status, rerr := readCutoverStatus(ctx, deps); rerr == nil && status["cutoverComplete"] != "true" {
				_ = patchCutoverStatus(ctx, deps, map[string]string{"cutoverComplete": "true"})
				h.log.Info("cutover-resume: already complete (sealed); backfilled reverted status ConfigMap, fired nothing")
			} else {
				h.log.Info("cutover-resume: already complete (sealed); nothing to resume")
			}
		}
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
	// and resuming re-enters the failed step (runCutover skips only
	// result=="success" rows). What happens there now depends on WHY the
	// step failed (#3379, hw139):
	//   - TRANSIENT failure (the prior Job hit its activeDeadlineSeconds,
	//     reason DeadlineExceeded — e.g. step-03 harbor-prewarm running out
	//     of wall-clock): runCutoverStep's idempotency block deletes the
	//     timed-out Job and re-runs the step from scratch. With the larger
	//     budget + parallel copies (same PR) the re-run can complete, so the
	//     cutover proceeds instead of wedging at the failed step forever.
	//   - GENUINE failure (non-zero application exit / backoff-limit
	//     exhausted): runCutoverStep re-surfaces it as terminal and the run
	//     halts again with the same error — operator intervention required.
	// Either way the transient/genuine decision is centralized in
	// runCutoverStep (via jobFailedTransiently), so both the startup-resume
	// path and an explicit /start re-fire get identical behavior.
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
	// operatorRetry=false (#3379): startup-resume is unattended — a GENUINE
	// prior failure (BackoffLimitExceeded) must fail-closed, not auto-loop.
	// Only an explicit operator /start re-fire (operatorRetry=true) re-runs a
	// genuinely-failed step. Transient (DeadlineExceeded) failures still
	// re-run here via jobFailedTransiently inside runCutoverStep.
	go h.runCutover(context.Background(), deps, steps, false)
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
	State             string `json:"state"`
	CutoverComplete   bool   `json:"cutoverComplete"`
	CutoverStartedAt  string `json:"cutoverStartedAt,omitempty"`
	CutoverFinishedAt string `json:"cutoverFinishedAt,omitempty"`
	// CutoverLastAttemptStartedAt (#3681) carries the most-recent attempt's
	// start (resume/re-fire), distinct from CutoverStartedAt which is the
	// TRUE first-run anchor. The UI shows total elapsed from CutoverStartedAt
	// and "current attempt" from this field.
	CutoverLastAttemptStartedAt string              `json:"cutoverLastAttemptStartedAt,omitempty"`
	CurrentStep                 string              `json:"currentStep,omitempty"`
	CurrentStepIndex            int                 `json:"currentStepIndex"`
	TotalSteps                  int                 `json:"totalSteps"`
	ProgressPercent             int                 `json:"progressPercent"`
	FailedStep                  string              `json:"failedStep,omitempty"`
	LastError                   string              `json:"lastError,omitempty"`
	Steps                       []cutoverStepStatus `json:"steps"`
	Raw                         map[string]string   `json:"raw,omitempty"`
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
	//
	// operatorRetry=true (#3379): this is the operator-session CTA path
	// (behind RequireSession). A deliberate human re-POST of /start after a
	// step failed GENUINELY (e.g. the hw139 step-10 residual-tether FATAL,
	// now fixed in chart 0.1.62) deletes the prior failed Job + re-runs the
	// step. The in-cluster auto-trigger + startup-resume keep this false
	// (fail-closed) so an unattended genuine failure never auto-loops.
	go h.runCutover(context.Background(), deps, steps, true)

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
	// Durable-fact backfill (#3667). If a `helm upgrade
	// bp-self-sovereign-cutover` reverted the status ConfigMap's
	// cutoverComplete to "false" but the OpenBao seal exists, /status MUST
	// still answer "sovereign" so the SovereigntyCard never flickers back to
	// the "Achieve True Sovereignty" CTA. We re-derive from the seal here
	// (read-only — the spawn/resume paths perform the durable backfill) and
	// recover cutoverStartedAt/FinishedAt from the seal payload if the CM
	// lost them too. Best-effort: a seal-read error leaves the CM-derived
	// answer unchanged.
	if status["cutoverComplete"] != "true" {
		if sealed, serr := h.sovereignCutoverComplete(r.Context()); serr == nil && sealed {
			status["cutoverComplete"] = "true"
			if h.openbao != nil {
				if data, derr := h.openbao.GetKVv2(r.Context(), "secret", cutoverCompleteSecretPath); derr == nil {
					if status["cutoverStartedAt"] == "" {
						if v, ok := data["cutoverStartedAt"].(string); ok {
							status["cutoverStartedAt"] = v
						}
					}
					if status["cutoverFinishedAt"] == "" {
						if v, ok := data["cutoverFinishedAt"].(string); ok {
							status["cutoverFinishedAt"] = v
						}
					}
				}
			}
		}
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
	resp.CutoverLastAttemptStartedAt = status["cutoverLastAttemptStartedAt"]
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
