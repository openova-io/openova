// Phase-1 HelmRelease watch wiring.
//
// runPhase1Watch is the entry point runProvisioning calls after Phase 0
// ("flux-bootstrap") completes successfully. It builds an
// internal/helmwatch.Watcher against the deployment's persisted
// kubeconfig, runs the watch until termination, and writes the final
// per-component states + Phase1FinishedAt onto dep.Result.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #3 the watch is read-only —
// internal/helmwatch never patches/applies/deletes any resource. Its
// only job is to read HelmRelease.status.conditions and turn each
// observed transition into a provisioner.Event the SSE buffer carries.
//
// Lifecycle:
//   - Skipped when dep.Result.KubeconfigPath is empty OR points at a
//     missing file. The Sovereign Admin surfaces the missing-
//     kubeconfig case via a single warn event so the operator can
//     fall back to docs/RUNBOOK-PROVISIONING.md §"Fetch kubeconfig
//     via SSH" and retry.
//   - Times out per CATALYST_PHASE1_WATCH_TIMEOUT (default 60m).
//   - On termination, dep.Status flips to "ready" if every observed
//     component reached "installed" OR there were no components and
//     the watch ran clean. If any component ended in "failed", Status
//     stays "phase1-watching" and Error captures the count — the
//     wizard's FailureCard renders the per-component breakdown.
//   - Result.ComponentStates + Result.Phase1FinishedAt get written
//     under dep.mu so a concurrent State() snapshot is consistent.
package handler

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handoverjwt"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// phase1WatchTimeoutEnv — env var override for the watch budget. The
// default DefaultWatchTimeout (120 minutes after F8 2026-05-12) must
// comfortably contain bp-catalyst-platform's install.timeout × retries
// (30m × 3 = 90m worst case per clusters/_template/bootstrap-kit/13-
// bp-catalyst-platform.yaml). Tests inject a much shorter value via
// Handler.phase1WatchTimeout.
const phase1WatchTimeoutEnv = "CATALYST_PHASE1_WATCH_TIMEOUT"

// phase1MinBootstrapKitHRsEnv — env var override for the lower bound
// on observed bp-* HelmReleases below which the terminate-on-all-done
// gate is suppressed. Default helmwatch.DefaultMinBootstrapKitHRs
// (11) tracks the canonical bootstrap-kit count. A future kit that
// ships more or fewer components only needs this env flipped on the
// catalyst-api Deployment — no code change required.
const phase1MinBootstrapKitHRsEnv = "CATALYST_PHASE1_MIN_BOOTSTRAP_KIT_HRS"

// phase1ReadySentinelEnv — env var override for the component whose
// terminal-install gates OutcomeReady (#4746). Default
// helmwatch.DefaultReadySentinelComponent ("catalyst-platform") — the
// console's own backend + the terminal node of the bootstrap-kit
// dependency chain. Setting this empty on the catalyst-api Deployment
// disables the sentinel gate (reverting to the pre-#4746 narrow census);
// a future kit that renames the console umbrella only needs this flipped,
// no code change.
const phase1ReadySentinelEnv = "CATALYST_PHASE1_READY_SENTINEL"

// phase1FirstSeenTimeoutEnv — env var override for the first-seen
// gate window. If zero bp-* HelmReleases appear within this window,
// the watcher emits a single warn event ("bootstrap-kit not
// reconciling") and CONTINUES the watch (HRs may still appear). The
// watch's overall budget is phase1WatchTimeoutEnv.
const phase1FirstSeenTimeoutEnv = "CATALYST_PHASE1_FIRST_SEEN_TIMEOUT"

// phase1LatePollTimeoutEnv — env var override for the eventual-
// consistency late-poll window (issue #910). When the all-terminal
// gate fires with at least one failed component, the watcher keeps
// the informer running for this duration to give Flux helm-controller
// remediation.retries room to flip the failed HR back to installing
// → installed. Default DefaultLatePollTimeout (10m).
const phase1LatePollTimeoutEnv = "CATALYST_PHASE1_LATE_POLL_TIMEOUT"

// phase1LatePollIntervalEnv — env var override for the cadence at
// which the late-poll mode emits "still waiting for X to recover"
// progress events. Default DefaultLatePollInterval (30s).
const phase1LatePollIntervalEnv = "CATALYST_PHASE1_LATE_POLL_INTERVAL"

// phase1ReachabilityBudgetEnv — env var override for the overall
// budget of the pre-flight reachability probe (issue #923). Default
// DefaultReachabilityOverallBudget (10m). Per docs/INVIOLABLE-
// PRINCIPLES.md #4 every knob is runtime-configurable; production
// reads this on every Pod start.
const phase1ReachabilityBudgetEnv = "CATALYST_PHASE1_REACHABILITY_BUDGET"

// phase1RecensusIntervalEnv — env var override for the cadence of the
// informer-independent periodic re-census (#5269). Every interval the
// Phase-1 watch Lists bp-* HelmReleases directly against the Sovereign
// apiserver, re-runs the same sentinel-gated OutcomeReady decision the
// informer path uses, and emits one structured heartbeat log line — so
// a quiesced/hung informer event stream (the hw278 wedge: sentinel
// Ready=True for 66min, watch never concluded, whole prov stranded at
// phase1-watching ~80min with handover/mesh/cutover all gated behind
// it) can no longer strand a converged prov, and a future wedge is
// visible in the catalyst-api logs within one interval instead of
// silent. Default helmwatch.DefaultRecensusInterval (45s).
const phase1RecensusIntervalEnv = "CATALYST_PHASE1_RECENSUS_INTERVAL"

// phase1ProgressStallWindowEnv — env var override for the progress-guard
// stall window (#5283). Past the soft WatchTimeout the watch concludes
// OutcomeTimeout only once readyHRs has been flat for this long with no
// failed HR; while readyHRs climbed within the window it defers instead
// of stamping a still-converging prov failed (hw279: an intermittent
// external xpkg.upbound.io 503 delayed the bp-crossplane →
// bp-catalyst-platform chain past 120m while readyHRs kept climbing).
// Empty/invalid falls back to helmwatch's WatchTimeout/4 default (30m).
const phase1ProgressStallWindowEnv = "CATALYST_PHASE1_PROGRESS_STALL_WINDOW"

// phase1ProgressGuardCeilingEnv — env var override for the absolute
// wall-clock ceiling on the whole Phase-1 watch INCLUDING progress-guard
// deferrals past WatchTimeout (#5283). Empty/invalid falls back to
// helmwatch's WatchTimeout×2 default (240m).
const phase1ProgressGuardCeilingEnv = "CATALYST_PHASE1_PROGRESS_GUARD_CEILING"

// kubeconfigArrivalTimeoutEnv — how long runPhase1Watch waits for the
// cloud-init PUT to land /var/lib/catalyst/kubeconfigs/<id>.yaml on
// disk before giving up with OutcomeKubeconfigMissing. Cloud-init
// typically completes within 3-6 minutes after `tofu apply` returns;
// 15 minutes is generous headroom for slow Hetzner regions or LB
// reconcile delays. Tests inject a much shorter value via
// Handler.kubeconfigArrivalTimeout.
//
// While waiting, dep.Status stays "phase1-watching" (NOT "failed")
// — markPhase1Done is only called once the timeout elapses. Issue
// #538: previously the watch terminated on the first miss, so when
// runProvisioning launched it moments before cloud-init's PUT
// landed, the deployment latched terminal-failed and the wizard
// showed Install X jobs PENDING forever even when the new Sovereign
// was actually healthy.
const kubeconfigArrivalTimeoutEnv = "CATALYST_PHASE1_KUBECONFIG_ARRIVAL_TIMEOUT"

// kubeconfigArrivalPollIntervalEnv — how often runPhase1Watch
// re-checks for the kubeconfig file on disk while waiting for the
// cloud-init PUT. 15 seconds keeps the wizard log pane responsive
// without thrashing the PVC.
const kubeconfigArrivalPollIntervalEnv = "CATALYST_PHASE1_KUBECONFIG_POLL_INTERVAL"

// DefaultKubeconfigArrivalTimeout — production default for the
// kubeconfig-arrival wait window. Issue #538.
const DefaultKubeconfigArrivalTimeout = 15 * time.Minute

// DefaultKubeconfigArrivalPollInterval — production default for the
// kubeconfig-arrival poll cadence. Issue #538.
const DefaultKubeconfigArrivalPollInterval = 15 * time.Second

// fluxCRDProbeBudgetEnv — env var override for the overall budget of
// the bounded Flux-CRD-presence probe (#5042). AFTER the kubeconfig
// lands and BEFORE the helmwatch informer is built, runPhase1Watch
// probes whether the Flux HelmRelease CRD
// (helmreleases.helm.toolkit.fluxcd.io) is servable on the new
// Sovereign. On an intermittent bootstrap wedge the cluster is
// HEALTHY (nodes Ready, CNI up, kubeconfig PUT) but cloud-init's
// flux-install stage never landed, so the CRD is ABSENT — without this
// probe the deployment idles "phase1-watching" for the full
// WatchTimeout (120m) with no fast, named diagnostic. The probe polls
// until the CRD becomes servable (→ proceed to the watcher) or the
// budget elapses with the CRD still positively absent (→
// OutcomeFluxCRDsAbsent, a fast actionable failure). Per
// docs/INVIOLABLE-PRINCIPLES.md #4 this is runtime-configurable; tests
// inject a sub-second value via Handler.fluxCRDProbeBudget.
const fluxCRDProbeBudgetEnv = "CATALYST_PHASE1_FLUX_CRD_PROBE_BUDGET"

// fluxCRDProbePollIntervalEnv — env var override for the cadence at
// which the Flux-CRD-presence probe re-checks the CRD (#5042). 15s
// keeps the wizard log pane responsive without hammering the new
// Sovereign's apiserver. Tests inject a sub-second value via
// Handler.fluxCRDProbePollInterval.
const fluxCRDProbePollIntervalEnv = "CATALYST_PHASE1_FLUX_CRD_PROBE_POLL_INTERVAL"

// DefaultFluxCRDProbeBudget — production default for the Flux-CRD-
// presence probe budget (#5042).
//
// 15 minutes, NOT less — the original 5m default rested on an inverted
// premise ("flux lands before the kubeconfig is even PUT back"). Since the
// #3129 PUT-early invariant (cloudinit-control-plane.tftpl header), the
// kubeconfig PUT-back runs RIGHT AFTER the k3s apiserver is healthy, and
// flux-install only runs AFTER gateway-api CRDs + the helm binary download +
// the cilium helm install + the FULL cilium DaemonSet rollout across every
// node in the region. On a healthy Huawei 6-node region that is ~6-9 minutes
// of legitimate bootstrap AFTER the kubeconfig arrives. hw255 (#5012
// recurrence, dep 5762118f63abef96, 2026-07-15): region-b PUT ~04:13 UTC,
// flux CRDs landed ~04:21 UTC — a healthy 8-minute gap that the 5m budget
// classified as a positive flux-CRD-absent, permanently freezing the region's
// census at 0/0 degraded while it actually converged to 58/65 HRs Ready.
// 15 minutes still terminates a genuine #5042 wedge ~8x faster than the 120m
// WatchTimeout it exists to shortcut.
//
// The probe is CONSERVATIVE — it only classifies OutcomeFluxCRDsAbsent
// on a POSITIVE "CRD absent + apiserver answered" observation at
// budget end, never on a transient probe error (see probeFluxCRDAbsent).
const DefaultFluxCRDProbeBudget = 15 * time.Minute

// DefaultFluxCRDProbePollInterval — production default for the Flux-
// CRD-presence probe poll cadence (#5042).
const DefaultFluxCRDProbePollInterval = 15 * time.Second

// handoverCertWaitTimeoutEnv — env var override for how long the
// handover auto-fire waits for the new Sovereign's wildcard TLS
// cert (`sovereign-wildcard-tls` in `kube-system`) to reach
// Ready=True before emitting the handoverURL anyway. Issue #780:
// Phase-1 ready does NOT imply the cert has issued — DNS-01 with
// PowerDNS typically takes 30s-3min after Phase-1 terminates.
// Without this gate the wizard renders the redirect button at a
// console URL that fails TLS for ~90s, breaking the operator's
// first impression.
const handoverCertWaitTimeoutEnv = "CATALYST_HANDOVER_CERT_WAIT_TIMEOUT"

// handoverCertPollIntervalEnv — env var override for the cadence
// at which the handover auto-fire polls the cert's
// status.conditions[type=Ready] block while waiting. 10s keeps
// the wizard log pane informative without thrashing the
// Sovereign's apiserver.
const handoverCertPollIntervalEnv = "CATALYST_HANDOVER_CERT_POLL_INTERVAL"

// DefaultHandoverCertWaitTimeout — production default for the
// wildcard-cert wait window. 10 minutes is generous headroom: the
// Phase-1 watch terminates Ready when 38/38 HRs are installed,
// and the cert's DNS-01 challenge against contabo's central
// PowerDNS typically completes within 90 seconds of bp-cert-
// manager-powerdns-webhook becoming ready (which is itself one of
// the 38 HRs). Issue #780.
const DefaultHandoverCertWaitTimeout = 10 * time.Minute

// DefaultHandoverCertPollInterval — production default for the
// wildcard-cert poll cadence. Issue #780.
const DefaultHandoverCertPollInterval = 10 * time.Second

// sovereignWildcardCertName — name of the Certificate resource the
// handover auto-fire waits on. Created by either
// clusters/_template/sovereign-tls/cilium-gateway-cert.yaml
// (single-zone overlay) or
// products/catalyst/chart/templates/sovereign-wildcard-certs.yaml
// (multi-zone overlay) — both produce a Certificate named
// `sovereign-wildcard-tls`. Issue #780.
const sovereignWildcardCertName = "sovereign-wildcard-tls"

// sovereignWildcardCertNamespace — namespace where the Certificate
// resource lives. The Cilium Gateway listener references a Secret
// of the same name in the same namespace, so this MUST match the
// chart + legacy template. Issue #780.
const sovereignWildcardCertNamespace = "kube-system"

// runPhase1Watch builds a helmwatch.Watcher and runs it to completion.
// All emit goes through h.emitWatchEvent so the durable buffer + SSE
// channel get every per-component event.
//
// The watch runs synchronously in the calling goroutine —
// runProvisioning waits here before closing dep.done. This keeps the
// "deployment finished" semantics consistent: a deployment is done
// only when both Phase 0 AND Phase 1 watch have terminated.
func (h *Handler) runPhase1Watch(dep *Deployment) {
	// At-most-once guard. Two callers can race to launch the watch:
	// runProvisioning (after `tofu apply`) and PutKubeconfig (after
	// the cloud-init postback). The first one through claims the
	// goroutine; the second is a no-op. Without this, a duplicate
	// run would spin up a second informer + emit a duplicate set of
	// per-component events into the SSE buffer.
	//
	// Issue #538 nuance: PutKubeconfig clears phase1Started AND
	// resets the terminal kubeconfig-missing markers BEFORE
	// re-launching, so the belt-and-braces kick-the-watch path
	// can run a fresh watch even after a previous attempt
	// terminated kubeconfig-missing. See PutKubeconfig in
	// kubeconfig.go for the reset logic.
	dep.mu.Lock()
	if dep.phase1Started {
		dep.mu.Unlock()
		h.log.Info("phase 1 watch already running for this deployment; skipping duplicate launch",
			"id", dep.ID,
		)
		return
	}
	dep.phase1Started = true
	dep.mu.Unlock()

	// Wait for the kubeconfig file to appear on disk. The path
	// pointer (dep.Result.KubeconfigPath) is set by PutKubeconfig
	// when cloud-init's postback succeeds. While waiting, dep.Status
	// stays "phase1-watching" — markPhase1Done is only called on
	// timeout. Issue #538: previously the watch terminated on the
	// first miss, so when runProvisioning launched it moments before
	// cloud-init's PUT landed, the deployment latched terminal-failed
	// and the wizard showed Install X jobs PENDING forever even when
	// the new Sovereign was actually healthy.
	kubeconfig, ok := h.waitForKubeconfig(dep)
	if !ok {
		// Timeout elapsed — surface kubeconfig-missing as before.
		// The warn event was emitted by waitForKubeconfig at the
		// final tick.
		h.markPhase1Done(dep, nil, helmwatch.OutcomeKubeconfigMissing)
		return
	}

	// #5042 — bounded Flux-CRD-presence probe. On an intermittent
	// fresh-prov bootstrap wedge the new Sovereign is HEALTHY (kubeconfig
	// PUT, nodes Ready, CNI up) but cloud-init's flux-install stage never
	// landed, so the Flux HelmRelease CRD
	// (helmreleases.helm.toolkit.fluxcd.io) is ABSENT. Without this probe
	// the helmwatch informer below would attach and simply observe zero
	// HelmReleases until the 120m WatchTimeout — the deployment idles
	// "phase1-watching" for hours with no fast, named diagnostic (the
	// #5042 forensic gap). probeFluxCRDAbsent polls until the CRD becomes
	// servable (→ proceed to the watcher as normal) or its budget elapses
	// with the CRD still POSITIVELY absent (→ OutcomeFluxCRDsAbsent, a
	// fast actionable failure). It is CONSERVATIVE: a transient probe
	// error (apiserver blip, client-build failure) falls through to the
	// watcher so a flake never invents a failure.
	if h.probeFluxCRDAbsent(dep, kubeconfig) {
		h.markPhase1Done(dep, nil, helmwatch.OutcomeFluxCRDsAbsent)
		return
	}

	cfg := h.phase1WatchConfigForDeployment(dep, kubeconfig)
	watcher, err := helmwatch.NewWatcher(cfg, func(ev provisioner.Event) {
		h.emitWatchEvent(dep, ev)
	})
	if err != nil {
		h.emitWatchEvent(dep, provisioner.Event{
			Time:    time.Now().UTC().Format(time.RFC3339),
			Phase:   helmwatch.PhaseComponent,
			Level:   "error",
			Message: fmt.Sprintf("Phase-1 watch could not start: %v — Sovereign cluster is up (Phase 0 succeeded) but per-component state will not stream from this catalyst-api. Operator may run `kubectl get helmrelease -n flux-system` against the new Sovereign for ad-hoc diagnostics.", err),
		})
		// Short-circuit path — watcher never ran. OutcomeWatcherStartFailed
		// (not empty) keeps markPhase1Done from defaulting to "ready"
		// — issue #488.
		h.markPhase1Done(dep, nil, helmwatch.OutcomeWatcherStartFailed)
		return
	}

	// Subscribe the bridge to the watcher's initial-list-synced hook
	// so a HelmRelease that has been Ready=True for an hour STILL
	// materialises a Job row + a synthetic-log-line Execution as
	// soon as the informer's first list completes. This is what
	// makes the table-view UX backfill correctly when the wizard
	// connects long after the watch's per-transition emits ran.
	//
	// Idempotency is guaranteed by SeedJobsFromInformerList itself
	// — calling it again on every helmwatch start is a no-op when
	// the Job already has a LatestExecutionID.
	h.attachBridgeSeederHook(dep, watcher)

	// Stash the live watcher on the deployment so the
	// /components/state and /refresh-watch endpoints can read its
	// in-memory informer cache without reaching into the cluster.
	dep.mu.Lock()
	dep.liveWatcher = watcher
	dep.mu.Unlock()
	// Spin up the background snapshot-emit loop for this deployment.
	// Idempotent — re-entering phase1 (post-restart resume) is a
	// no-op. The loop POSTs a snapshot every 5s to openova-flow-
	// server's CNPG store + ad-hoc on trigger from event-path code.
	h.startFlowEmitLoop(dep.ID)
	defer func() {
		// Clear the live-watcher pointer so a subsequent
		// /refresh-watch invocation doesn't see a stale reference
		// to a Watcher whose Watch loop has already returned.
		dep.mu.Lock()
		if dep.liveWatcher == watcher {
			dep.liveWatcher = nil
		}
		dep.mu.Unlock()
	}()

	// Multi-region: spawn one helmwatch.Watcher per secondary region
	// whose kubeconfig has been PUT back (cloudinit-control-plane.tftpl
	// passes `?region=<k>` for each secondary CP). The watchers run
	// in background goroutines; their SnapshotComponents() is
	// composed into the flow snapshot per region so the canvas shows
	// install-* HRs from every region. A poll loop catches secondary
	// kubeconfigs that arrive AFTER the primary's watch starts (they
	// typically race within ~10s of each other but not always).
	stopSecondaries := h.spawnSecondaryRegionWatchers(dep)
	defer stopSecondaries()

	// Use the background context so a finished HTTP request from the
	// caller doesn't cancel a multi-minute Phase-1 watch. The watch
	// has its own configured timeout via cfg.WatchTimeout.
	finalStates, watchErr := watcher.Watch(context.Background())
	if watchErr != nil {
		h.log.Error("phase 1 watch returned error",
			"id", dep.ID,
			"err", watchErr,
		)
	}
	// Read the watch's terminal classification BEFORE markPhase1Done
	// — Outcome() must be called after Watch returns so the watcher
	// has set its final value. The Sovereign Admin's wizard banner
	// reads dep.Result.Phase1Outcome to render the right
	// operator-actionable diagnostic (e.g. "Flux on the new cluster
	// isn't reconciling the bootstrap-kit Kustomization").
	outcome := watcher.Outcome()
	h.markPhase1Done(dep, finalStates, outcome)
}

// phase1WatchConfigForDeployment — builds the helmwatch.Config the
// runPhase1Watch entry point uses. Pulled out so tests can call it
// to verify env-var parse + factory wiring without standing up a
// real cluster.
//
// h.phase1WatchTimeout / h.phase1MinBootstrapKitHRs /
// h.phase1FirstSeenTimeout are test-only overrides; production reads
// the env vars unmodified. Per docs/INVIOLABLE-PRINCIPLES.md #4 every
// knob is runtime-configurable — no constant is hardcoded into the
// build that an operator can't override at the catalyst-api
// Deployment level.
func (h *Handler) phase1WatchConfigForDeployment(dep *Deployment, kubeconfig string) helmwatch.Config {
	timeout := h.phase1WatchTimeout
	if timeout == 0 {
		timeout = helmwatch.CompileWatchTimeout(envOrEmpty(phase1WatchTimeoutEnv))
	}

	minHRs := h.phase1MinBootstrapKitHRs
	if minHRs == 0 {
		minHRs = helmwatch.CompileMinBootstrapKitHRs(envOrEmpty(phase1MinBootstrapKitHRsEnv))
	}

	firstSeen := h.phase1FirstSeenTimeout
	if firstSeen == 0 {
		firstSeen = helmwatch.CompileFirstSeenTimeout(envOrEmpty(phase1FirstSeenTimeoutEnv))
	}

	latePollTimeout := h.phase1LatePollTimeout
	if latePollTimeout == 0 {
		latePollTimeout = helmwatch.CompileLatePollTimeout(envOrEmpty(phase1LatePollTimeoutEnv))
	}

	latePollInterval := h.phase1LatePollInterval
	if latePollInterval == 0 {
		latePollInterval = helmwatch.CompileLatePollInterval(envOrEmpty(phase1LatePollIntervalEnv))
	}

	reachabilityBudget := h.phase1ReachabilityBudget
	if reachabilityBudget == 0 {
		if v, _ := time.ParseDuration(envOrEmpty(phase1ReachabilityBudgetEnv)); v > 0 {
			reachabilityBudget = v
		}
	}

	// #5269 — informer-independent re-census cadence. Same
	// field-override-beats-env precedence as every other Phase-1 knob.
	recensusInterval := h.phase1RecensusInterval
	if recensusInterval == 0 {
		recensusInterval = helmwatch.CompileRecensusInterval(envOrEmpty(phase1RecensusIntervalEnv))
	}

	// #5283 — progress-guard knobs. Left zero unless an explicit env
	// override is set, so helmwatch.applyDefaults derives both from the
	// resolved WatchTimeout (stall window = /4, ceiling = ×2). Only a
	// positive, parseable duration overrides.
	var progressStallWindow time.Duration
	if v, _ := time.ParseDuration(envOrEmpty(phase1ProgressStallWindowEnv)); v > 0 {
		progressStallWindow = v
	}
	var progressGuardCeiling time.Duration
	if v, _ := time.ParseDuration(envOrEmpty(phase1ProgressGuardCeilingEnv)); v > 0 {
		progressGuardCeiling = v
	}

	// #4746 — the component whose terminal-install gates OutcomeReady.
	// Base value is the Handler field (production New() sets it to
	// catalyst-platform; tests leave it empty → gate off). An explicitly
	// set CATALYST_PHASE1_READY_SENTINEL wins — including set-to-empty,
	// which disables the gate — so os.LookupEnv (not envOrEmpty) is used to
	// distinguish "unset" from "set empty" (Inviolable Principle #4).
	readySentinel := h.phase1ReadySentinel
	if v, ok := os.LookupEnv(phase1ReadySentinelEnv); ok {
		readySentinel = strings.TrimSpace(v)
	}

	cfg := helmwatch.Config{
		KubeconfigYAML:            kubeconfig,
		WatchTimeout:              timeout,
		ProgressStallWindow:       progressStallWindow,
		ProgressGuardCeiling:      progressGuardCeiling,
		MinBootstrapKitHRs:        minHRs,
		FirstSeenTimeout:          firstSeen,
		LatePollTimeout:           latePollTimeout,
		LatePollInterval:          latePollInterval,
		ReachabilityOverallBudget: reachabilityBudget,
		ReadySentinelComponent:    readySentinel,
		RecensusInterval:          recensusInterval,
		// OnHeartbeat — one structured log line per re-census
		// evaluation cycle (#5269): dep id, HR ready count, sentinel
		// state, all-terminal verdict, informer-event age. This is the
		// forensic surface the hw278 wedge lacked — 80 minutes of
		// silence between "informer synced" and the operator's manual
		// rollout-restart. Secondary-region watchers re-wire this with
		// a region tag in spawnSecondaryRegionWatchers.
		OnHeartbeat: h.phase1HeartbeatLogger(dep, ""),
		// OnSubstate — issue #923. The watcher fires this on every
		// Phase-1 substate transition (reconnecting → watching). We
		// stamp Result.Phase1Substate under dep.mu so a /deployments/
		// {id} GET that races the substate change reads the live
		// value, not a stale "phase1-watching" with no further
		// signal.
		OnSubstate: func(substate string) {
			h.setPhase1Substate(dep, substate)
		},
	}
	if h.dynamicFactory != nil {
		cfg.DynamicFactory = h.dynamicFactory
	}
	if h.coreFactory != nil {
		cfg.CoreFactory = h.coreFactory
	}
	if h.phase1Reachability != nil {
		cfg.Reachability = h.phase1Reachability
	}
	if h.phase1WatchResync > 0 {
		cfg.Resync = h.phase1WatchResync
	}
	if h.phase1ReachabilitySleep != nil {
		cfg.Sleep = h.phase1ReachabilitySleep
	}
	if h.phase1ReachabilityProbeTimeout > 0 {
		cfg.ReachabilityProbeTimeout = h.phase1ReachabilityProbeTimeout
	}
	if h.phase1ReachabilityRetryInitial > 0 {
		cfg.ReachabilityRetryInitialInterval = h.phase1ReachabilityRetryInitial
	}
	if h.phase1ReachabilityRetryMax > 0 {
		cfg.ReachabilityRetryMaxInterval = h.phase1ReachabilityRetryMax
	}
	return cfg
}

// setPhase1Substate stamps the live Phase-1 substate onto the
// deployment's Result under dep.mu, then persists the deployment
// record so a Pod restart between transitions reads the same value
// (issue #923).
//
// The substate field is intentionally informational — it does NOT
// flip dep.Status. Status stays "phase1-watching" until markPhase1Done
// runs the terminal classification. The wizard banner reads
// Result.Phase1Substate to render "reconnecting…" or "watching…"
// while Status itself is unchanged.
func (h *Handler) setPhase1Substate(dep *Deployment, substate string) {
	dep.mu.Lock()
	if dep.Result == nil {
		dep.mu.Unlock()
		return
	}
	if dep.Result.Phase1Substate == substate {
		dep.mu.Unlock()
		return
	}
	dep.Result.Phase1Substate = substate
	dep.mu.Unlock()
	h.persistDeployment(dep)
}

// phase1HeartbeatLogger returns the helmwatch.Config.OnHeartbeat
// callback for a deployment's Phase-1 watch (#5269): one structured
// log line per re-census evaluation cycle so a wedged watch is visible
// in the catalyst-api logs within one interval instead of silently
// idling to WatchTimeout. region is empty for the primary watch and
// the region key for secondary-region watchers (whose heartbeats carry
// no sentinel — the gate is a primary-region concept, see
// spawnSecondaryRegionWatchers).
//
// Level policy: Info for a healthy cycle; Warn when the cycle's direct
// List failed (transient apiserver blip — no census update applied) or
// when the informer event stream has gone stale (the hw278 signature —
// the re-census is then the only thing driving the conclusion).
func (h *Handler) phase1HeartbeatLogger(dep *Deployment, region string) func(helmwatch.Heartbeat) {
	return func(hb helmwatch.Heartbeat) {
		args := []any{
			"id", dep.ID,
			"observedHRs", hb.ObservedHRs,
			"readyHRs", hb.ReadyHRs,
			"failedHRs", hb.FailedHRs,
			"sentinel", hb.SentinelState,
			"allTerminal", hb.AllTerminal,
			"informerEventAge", hb.InformerEventAge.Round(time.Second).String(),
		}
		if region != "" {
			args = append(args, "region", region)
		}
		switch {
		case hb.ListError != "":
			args = append(args, "listErr", hb.ListError)
			h.log.Warn("phase1 watch heartbeat: re-census List failed — no census update this cycle (#5269)", args...)
		case hb.InformerStale:
			h.log.Warn("phase1 watch heartbeat: informer event stream stale — ticker re-census is driving the readiness decision (#5269)", args...)
		default:
			h.log.Info("phase1 watch heartbeat (#5269)", args...)
		}
	}
}

// spawnSecondaryRegionWatchers reads `<kubeconfigsDir>/<id>-*.yaml`
// every 15s for the lifetime of the parent runPhase1Watch and spawns
// one helmwatch.Watcher per secondary region whose kubeconfig has been
// PUT back. Returns a stop function the caller defers; the stop fn
// cancels the ticker AND each per-region watcher's context.
//
// Each per-region watcher emits ordinary helmwatch events into the
// parent SSE channel (so the wizard still sees them) — but it does
// NOT contribute to the parent's `markPhase1Done` terminal call,
// since secondary regions can succeed/fail independently and the
// parent's outcome is taken from the primary's watcher.
//
// The composed `Snapshot` of all regions lives on
// dep.secondaryWatchers, read by flowSnapshotFromJobs to emit
// per-region FlowNodes for the canvas.
func (h *Handler) spawnSecondaryRegionWatchers(dep *Deployment) func() {
	if h.kubeconfigsDir == "" {
		return func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	stopWatchers := make(map[string]context.CancelFunc)
	var mu sync.Mutex

	spawn := func(region, kcPath string) {
		mu.Lock()
		if _, exists := stopWatchers[region]; exists {
			mu.Unlock()
			return
		}
		// Reserve the region slot up front, then run the (potentially long)
		// flux-CRD probe + watcher build OFF the closure lock in a goroutine.
		// #5012: the per-region probe can poll for the full
		// CATALYST_PHASE1_FLUX_CRD_PROBE_BUDGET when a secondary's flux never
		// installs; holding mu (or blocking the synchronous scan loop) for that
		// whole budget would serialise every OTHER region's spawn AND block
		// stopSecondaries (which needs mu to cancel watchers). The placeholder
		// cancel registered here both guards an overlapping 15s scan from
		// double-spawning this region and lets stopSecondaries abort an
		// in-flight probe.
		wctx, wcancel := context.WithCancel(ctx)
		stopWatchers[region] = wcancel
		mu.Unlock()

		// releaseSlot frees the reservation so a LATER scan can retry this
		// region (its kubeconfig only just became readable, or NewWatcher
		// flaked). Deliberately NOT called on the flux-absent path — there the
		// slot stays reserved so a doomed region is probed ONCE, not re-probed
		// every 15s scan.
		releaseSlot := func() {
			// The top-of-spawn guard blocks any concurrent re-reservation of
			// this region while our slot is present, and a fresh spawn for the
			// region can only run AFTER this delete + unlock — so deleting our
			// own reservation unconditionally is safe.
			mu.Lock()
			delete(stopWatchers, region)
			mu.Unlock()
			wcancel()
		}

		wg.Add(1)
		go func() {
			defer wg.Done()

			raw, err := os.ReadFile(kcPath)
			if err != nil {
				h.log.Warn("secondary kubeconfig read failed",
					"id", dep.ID, "region", region, "path", kcPath, "err", err)
				releaseSlot()
				return
			}

			// #5012 — per-secondary-region flux-CRD-absent detection, the
			// mirror of the primary's #5042 probe. A secondary whose cloud-init
			// flux-install stage silently didn't land has a HEALTHY cluster (its
			// kubeconfig was PUT) but NO helmreleases.helm.toolkit.fluxcd.io
			// CRD, so a helmwatch informer here would observe zero HelmReleases
			// until stopSecondaries cancels it — an invisible 0/0 with no named
			// diagnostic. Unlike the primary probe, a secondary must NEVER
			// fail/hang the whole prov (surface-not-gate discipline), so a
			// positive absent verdict records a NAMED, greppable degraded signal
			// (recordSecondaryFluxCRDAbsent: a loud region-tagged warn +
			// Result.SecondaryFluxCRDAbsentRegions + a nil census slot). The
			// slot stays reserved.
			//
			// hw255 recurrence (#5012, dep 5762118f63abef96): the verdict must
			// be a SURFACE, not a terminal — the #3129 PUT-early invariant puts
			// the kubeconfig on the mothership minutes BEFORE the secondary's
			// cloud-init reaches flux-install (gateway-api CRDs + helm download
			// + full cilium DaemonSet rollout sit in between), so flux can land
			// AFTER the probe budget on a perfectly healthy region. Keep
			// waiting (bounded only by this watcher's ctx — phase-1 lifetime);
			// when the CRD lands, clear the degraded record and fall through to
			// spin the real watcher so the #3611 census reports the region's
			// true HR counts instead of a frozen 0/0. A region whose flux never
			// lands keeps the recorded signal and spins no watcher, as before.
			if h.probeFluxCRDAbsentForRegion(wctx, dep, region, string(raw)) {
				h.recordSecondaryFluxCRDAbsent(dep, region)
				if !h.waitFluxCRDServableForRegion(wctx, dep, region, string(raw)) {
					// Phase-1 ended (ctx cancelled) with the CRD still absent —
					// the recorded degraded signal stands.
					return
				}
				h.clearSecondaryFluxCRDAbsent(dep, region)
			}

			cfg := h.phase1WatchConfigForDeployment(dep, string(raw))
			// #4746 — the ready sentinel (catalyst-platform) is a PRIMARY-region
			// concept: the console backend runs only in the primary region, so a
			// secondary-region watcher must NOT wait on catalyst-platform (it
			// would never observe it and could not self-terminate). Secondary
			// watchers do not contribute to markPhase1Done's terminal outcome
			// anyway (see the func doc), so clearing the gate here only lets a
			// converged secondary self-terminate cleanly instead of idling until
			// stopSecondaries cancels it.
			cfg.ReadySentinelComponent = ""
			// #5269 — region-tag the heartbeat so the per-region
			// re-census lines are distinguishable from the primary's
			// in the catalyst-api log stream.
			cfg.OnHeartbeat = h.phase1HeartbeatLogger(dep, region)
			watcher, err := helmwatch.NewWatcher(cfg, func(ev provisioner.Event) {
				// Region-tag the component events so the SSE consumer
				// can group them per region. Bare bp-* names from
				// secondary regions would otherwise collide with the
				// primary's events in the wizard's per-component view.
				// Separator is ":" not "/" so URLs of the form
				// /jobs/install-<region>:<chart> survive TanStack
				// Router's decode (the router decodes %2F→/ and then
				// the `$jobId` param fails to match the multi-segment
				// path → 404). "/" was the legacy separator caught by
				// the founder on prov #73 (install-hel1-2/newapi route
				// 404'd from the JobsTable click). Canonical rule per
				// feedback_natural_view_is_canon.md: "Node id separator
				// `:` not `/`".
				ev.Component = region + ":" + ev.Component
				// Region-prefix the sibling DependsOn entries so the
				// install-<region>:<chart> Jobs get intra-region edges
				// (e.g. install-hel1-2:catalyst-platform depends on
				// install-hel1-2:gitea, NOT install:gitea). Without
				// this, every secondary HR's DependsOn list ends up
				// pointing at the PRIMARY region's bare-named jobs,
				// and the canvas fan-out collapses cross-region edges
				// that aren't real. helmwatch.processEvent populated
				// ev.DependsOn from the live spec.dependsOn (bare chart
				// names like "gitea"); we both region-prefix AND inject
				// the canonical "install-" prefix so the stored Job
				// row's DependsOn matches the JobName scheme exactly.
				//
				// Why "install-<region>:<chart>" not "<region>:<chart>":
				// the FE canvas adapter looks up node ids by exact match;
				// node ids are `<dep>:install-<region>:<chart>` for
				// install Jobs. Storing "<region>:<chart>" as a dep
				// produces a `<dep>:<region>:<chart>` fromId in the
				// finish-to-start relationship, which matches no node →
				// edge invisible. Caught on prov t103.omani.works
				// (005080699326a7ac, 2026-05-15): openova-flow snapshot
				// had 224 finish-to-start rels emitted but their fromIds
				// were `<dep>:hel1-2:seaweedfs` etc., missing "install-"
				// → canvas rendered every secondary HR with no sibling
				// edges despite the rel count being non-zero.
				if len(ev.DependsOn) > 0 {
					rescoped := make([]string, 0, len(ev.DependsOn))
					for _, d := range ev.DependsOn {
						rescoped = append(rescoped, "install-"+region+":"+d)
					}
					ev.DependsOn = rescoped
				}
				h.emitWatchEvent(dep, ev)
			})
			if err != nil {
				h.log.Error("secondary helmwatch.NewWatcher failed",
					"id", dep.ID, "region", region, "err", err)
				releaseSlot()
				return
			}
			// Attach the region-aware seeder hook so SeedJobsFromInformerList
			// runs against this secondary's informer cache and writes
			// `install-<region>/<chart>` Jobs with region-prefixed DependsOn.
			// Without this the secondary install-* Jobs are only created via
			// the per-event OnHelmReleaseEvent path (DependsOn=[]) and the
			// canvas dep graph stays flat for the entire secondary region.
			// Caught on prov #73 (8cd1ff1a80430dc5, 2026-05-14).
			h.attachSecondaryBridgeSeederHook(dep, watcher, region)
			dep.mu.Lock()
			if dep.secondaryWatchers == nil {
				dep.secondaryWatchers = make(map[string]*helmwatch.Watcher)
			}
			dep.secondaryWatchers[region] = watcher
			dep.mu.Unlock()

			h.log.Info("secondary phase1 watch starting", "id", dep.ID, "region", region)
			_, werr := watcher.Watch(wctx)
			if werr != nil && wctx.Err() == nil {
				h.log.Warn("secondary phase1 watch returned error",
					"id", dep.ID, "region", region, "err", werr)
			}
			dep.mu.Lock()
			if dep.secondaryWatchers != nil && dep.secondaryWatchers[region] == watcher {
				delete(dep.secondaryWatchers, region)
			}
			dep.mu.Unlock()
		}()
	}

	scan := func() {
		entries, err := os.ReadDir(h.kubeconfigsDir)
		if err != nil {
			return
		}
		prefix := dep.ID + "-"
		for _, e := range entries {
			n := e.Name()
			if !strings.HasPrefix(n, prefix) || !strings.HasSuffix(n, ".yaml") {
				continue
			}
			region := strings.TrimSuffix(strings.TrimPrefix(n, prefix), ".yaml")
			if region == "" {
				continue
			}
			spawn(region, filepath.Join(h.kubeconfigsDir, n))
		}
	}

	// Initial scan + periodic refresh — secondary CPs may PUT their
	// kubeconfigs ~minutes apart depending on per-region tofu apply
	// timing.
	go func() {
		scan()
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				scan()
			}
		}
	}()

	return func() {
		cancel()
		mu.Lock()
		for _, c := range stopWatchers {
			c()
		}
		mu.Unlock()
		wg.Wait()
		dep.mu.Lock()
		dep.secondaryWatchers = nil
		dep.mu.Unlock()
	}
}

// markPhase1Done writes the watch outcome onto dep.Result and flips
// Status accordingly. Holds dep.mu for the whole transition so a
// State() snapshot from another goroutine can't observe Status=ready
// without ComponentStates yet being committed.
//
// The `outcome` argument is the watcher's terminal classification
// (helmwatch.OutcomeReady / OutcomeFailed / OutcomeTimeout /
// OutcomeFluxNotReconciling), or empty when no watch was ever run
// (kubeconfig short-circuit, NewWatcher failure). The Sovereign
// Admin's wizard banner reads dep.Result.Phase1Outcome to render the
// right operator-actionable diagnostic — in particular,
// "flux-not-reconciling" tells the operator to inspect the
// bootstrap-kit Kustomization on the new cluster instead of
// retrying provisioning.
// consoleProbeBudget / consoleProbeInterval bound the external console-
// reachability gate (#4706). A converged Sovereign whose gateway is not yet
// serving (LB/ELB just being wired) may need a few seconds; a Sovereign whose
// gateway is genuinely broken (e.g. an unwired Huawei ELB) never comes up and
// must be classified failed rather than left probing forever.
//
// #4746 — the budget was 90s on the false premise that "the console is up
// within seconds of OutcomeReady or structurally broken." That is WRONG on a
// slow 2-region prov: the phase1 watch's ready-census is narrow, so OutcomeReady
// fires while the console's own server chain (bp-catalyst-platform → bp-gitea →
// bp-keycloak, whose HR alone has a 30m install budget) is still installing.
// Live evidence: hw221 fired OutcomeReady at T+24m but the console only answered
// at T+48m — a 23-MINUTE gap during which the 90s gate falsely stamped a
// perfectly healthy prov `status=failed` (hw222 repro'd identically). A false
// "failed" is not cosmetic: it invites a wrong wipe of a converging env.
// The budget now covers keycloak's full install budget + the downstream
// gitea/catalyst-platform/cert chain with margin. The gate stays HARD (a
// genuinely dead console still fails — just after a realistic wait, not 90s),
// so the hw217/hw218 false-GREEN this probe exists to kill is unaffected. The
// probe holds no lock while waiting (see markPhase1Done), so a long wait never
// blocks State() readers.
const (
	consoleProbeBudget   = 35 * time.Minute
	consoleProbeInterval = 15 * time.Second
)

// consoleProbePaths are the paths each probe pass tries IN ORDER against
// https://console.<fqdn>; the front door counts as SERVING when ANY of them
// answers < 400 after redirect-following (#5253).
//
//   - "/" — the operator's entry point (silent-SSO: 200 SSR landing or 302 to
//     the Keycloak login on a healthy console).
//   - "/auth/handover" — the catalyst-api-registered handover route every
//     Sovereign console serves (auth_handover.go): a browser-shaped GET
//     without a token answers 302 to the SPA error page. This is the #5253
//     false-negative killer — hw276's healthy console answered 404 at its
//     bare SPA root, and the single-path probe misread the whole front door
//     as dead, tipping a fully-converged Sovereign to "failed". A route the
//     console BACKEND owns still answers < 400 in that shape, while the
//     hw218 no-vhost envoy 404 (#4715 — envoy up, NO route to the console
//     backend) keeps failing on EVERY path, so the false-green bar #4706
//     exists for is unchanged.
var consoleProbePaths = []string{"/", "/auth/handover"}

// newConsoleProbeClient builds the probe's HTTP client. TLS verification is
// skipped on purpose — the probe proves the gateway ANSWERS (TCP + TLS +
// HTTP), not that the cert chains to a public root (a fresh Sovereign may
// still be on an LE-staging/in-cluster wildcard). Redirect-following is
// EXPLICIT and load-bearing (#5253): the reachability decision is made on the
// FINAL status of the redirect chain (a 2xx-after-redirect is a healthy
// silent-SSO front door; a redirect landing on a 4xx/5xx is not). Go's
// default client already follows up to 10 hops — pinned here so a future
// client change can't silently turn the probe into a single-hop reader.
func newConsoleProbeClient() *http.Client {
	return &http.Client{
		Timeout:   8 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, // reachability, not trust
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			return nil
		},
	}
}

// consoleProbeAttempt runs ONE probe pass against base (scheme://host, no
// trailing slash): each consoleProbePaths entry is GET'd browser-shaped
// (Accept: text/html — the gate proves the OPERATOR's browser can open the
// console) with redirect-following. Returns nil when any path's final status
// is < 400; otherwise the last error/status observed.
func consoleProbeAttempt(ctx context.Context, client *http.Client, base string) error {
	var last error
	for _, p := range consoleProbePaths {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+p, nil)
		if err != nil {
			last = err
			continue
		}
		req.Header.Set("Accept", "text/html")
		resp, err := client.Do(req)
		if err != nil {
			last = err
			continue
		}
		code := resp.StatusCode
		resp.Body.Close()
		if code < 400 {
			return nil
		}
		last = fmt.Errorf("%s returned HTTP %d", base+p, code)
	}
	return last
}

// defaultConsoleReachable is the production external-reachability probe for
// #4706: it repeatedly probes https://console.<fqdn> (see consoleProbePaths)
// until a pass observes an HTTP response with FINAL status < 400 after
// redirect-following (the console front door actually SERVES) or the budget
// expires. It returns nil on reachable, a descriptive error otherwise.
//
// STATUS BAR < 400 (tightened from the original < 500 — hw218 evidence, #4706):
// the catalyst console is silent-SSO — its entry routes answer 200 or 302,
// never a 4xx, when the front door routes to the console backend. hw218
// (2026-07-03) exposed the hole in the old < 500 bar: its console gateway
// answered HTTP 404 (envoy up, but NO vhost/route to the console backend — the
// #4715 listener collision), and the < 500 gate mislabelled that broken front
// door "ready" — the exact false-green this probe exists to kill. A 4xx on
// EVERY probed path or 5xx or a transport error (connection refused / timeout
// / NXDOMAIN) counts as down. #5253 widened a pass to the multi-path +
// redirect-following decision above so a healthy console whose bare SPA root
// answers 404 (hw276) is no longer misread as a dead front door.
func defaultConsoleReachable(fqdn string) error {
	fqdn = strings.TrimSpace(fqdn)
	if fqdn == "" {
		return fmt.Errorf("empty sovereign FQDN — cannot probe console reachability")
	}
	base := "https://console." + fqdn
	client := newConsoleProbeClient()
	ctx, cancel := context.WithTimeout(context.Background(), consoleProbeBudget)
	defer cancel()
	var last error
	for {
		if err := consoleProbeAttempt(ctx, client, base); err == nil {
			return nil
		} else {
			last = err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("console %s/ not externally reachable within %s: %v", base, consoleProbeBudget, last)
		case <-time.After(consoleProbeInterval):
		}
	}
}

// consoleReprobeAttempts bounds the #5253 background re-probe that runs after
// a ready-but-ConsoleDegraded Phase-1 termination. Each attempt is a full
// consoleProbeBudget pass of h.consoleProbe, so 4 attempts extend the grace
// window well past any realistic gateway/LB/cert settling lag without leaving
// an unbounded goroutine behind.
const consoleReprobeAttempts = 4

// runConsoleReachabilityReprobe re-runs the console-reachability probe for a
// deployment that terminated Phase-1 ready with the non-fatal ConsoleDegraded
// surface set (#5253). On the first successful pass it clears the surface and
// persists; failed passes refresh ConsoleDegradedDetail so the operator sees
// the CURRENT diagnostic, not the stale termination-time one. This is
// observability-only — the producer chain (handover/mesh/spine/policy) fired
// at termination and is never gated on this loop. Runs on a
// spawnPostHandoverHook goroutine; the h.consoleProbe seam keeps tests off
// the network.
func (h *Handler) runConsoleReachabilityReprobe(dep *Deployment) {
	if h.consoleProbe == nil {
		return
	}
	// dep.Request is immutable after creation — lock-free read is safe
	// (same contract markPhase1Done relies on).
	fqdn := dep.Request.SovereignFQDN
	for attempt := 1; attempt <= consoleReprobeAttempts; attempt++ {
		err := h.consoleProbe(fqdn)
		dep.mu.Lock()
		if dep.Result == nil {
			dep.mu.Unlock()
			return
		}
		if err == nil {
			dep.Result.ConsoleDegraded = false
			dep.Result.ConsoleDegradedDetail = ""
			dep.mu.Unlock()
			h.persistDeployment(dep)
			h.log.Info("console re-probe: front door now serving — ConsoleDegraded cleared (#5253)",
				"id", dep.ID,
				"attempt", attempt,
			)
			return
		}
		dep.Result.ConsoleDegradedDetail = err.Error()
		dep.mu.Unlock()
		h.persistDeployment(dep)
		h.log.Warn("console re-probe: front door still not confirmed reachable (#5253)",
			"id", dep.ID,
			"attempt", attempt,
			"err", err,
		)
	}
	h.log.Warn("console re-probe attempts exhausted — ConsoleDegraded stays surfaced; investigate the gateway / load-balancer wiring (#4706). The producer chain (handover/mesh/spine/policy) already fired at Phase-1 termination and is NOT gated on this (#5253)",
		"id", dep.ID,
		"attempts", consoleReprobeAttempts,
	)
}

// EnableConsoleReachabilityGate wires the production external console-
// reachability probe (#4706). Once called, markPhase1Done refuses to flip a
// converged deployment to "ready" until https://console.<fqdn>/ actually
// answers — closing the false-green where the mothership claimed "ready" while
// the console was TCP-dead (hw217). Call once from main; unit tests leave the
// gate off and inject a stub via h.consoleProbe when they need to exercise it.
func (h *Handler) EnableConsoleReachabilityGate() {
	h.consoleProbe = defaultConsoleReachable
}

// spawnPostHandoverHook runs fn on a background goroutine — the
// fire-and-forget day-2 shape every post-handover hook uses — UNLESS the
// test-only suppressPostHandoverHooks gate is set (see the field doc on
// Handler). Production always spawns; the phase1-watch fixtures suppress so
// no goroutine carrying a 15-min / 6-hour budget outlives the test body and
// races t.TempDir cleanup / trips -race. Behaviour when not suppressed is
// byte-identical to the previous inline `go h.run…(dep)`.
func (h *Handler) spawnPostHandoverHook(fn func()) {
	if h.suppressPostHandoverHooks {
		return
	}
	go fn()
}

func (h *Handler) markPhase1Done(dep *Deployment, finalStates map[string]string, outcome string) {
	now := time.Now().UTC()

	// #4706 — external console-reachability gate. "ready" must mean the
	// operator can actually open the console, not merely that Flux
	// converged in-cluster. We probe BEFORE taking dep.mu so a slow or
	// failing probe never blocks State() readers holding the same lock.
	// dep.Request is immutable after creation, so reading SovereignFQDN
	// lock-free is safe. The gate is OFF unless h.consoleProbe is wired:
	// production calls EnableConsoleReachabilityGate() at startup; unit
	// tests leave it nil (so they never touch the network) and set a stub
	// directly when they want to exercise it.
	var consoleReachErr error
	if outcome == helmwatch.OutcomeReady && h.consoleProbe != nil {
		consoleReachErr = h.consoleProbe(dep.Request.SovereignFQDN)
	}

	dep.mu.Lock()
	if dep.Result == nil {
		// Phase 0 already failed and runProvisioning skipped the
		// watch — markPhase1Done shouldn't have been called, but
		// defend against a future caller anyway.
		dep.mu.Unlock()
		return
	}

	// Refuse to downgrade an already-handover-completed deployment.
	// Status="adopted" means the operator has already minted a
	// handover token and the wizard has redirected to the Sovereign
	// Console. A late helmwatch event (informer flake, transient
	// HR.Ready=False that recovers, watcher Cancel race) MUST NOT
	// flap back to "ready"/"failed"/"phase1-watching" — the
	// handover flow has already taken ownership of the deployment.
	if dep.Status == "adopted" {
		dep.mu.Unlock()
		h.log.Info("phase 1 watch terminated after adoption — preserving adopted status",
			"id", dep.ID,
			"phase1Outcome", outcome,
		)
		return
	}

	dep.Result.ComponentStates = finalStates
	dep.Result.Phase1FinishedAt = &now
	dep.Result.Phase1Outcome = outcome
	// Clear the in-flight substate (issue #923) — the watch has
	// terminated and Phase1Outcome is the authoritative classification
	// from this point. The wizard banner falls back to rendering the
	// Status pill alone once Phase1Substate is empty.
	dep.Result.Phase1Substate = ""

	// Per-region HelmRelease health (#3611). The primary region's terminal
	// states are finalStates; the secondaries are snapshotted from their
	// still-attached watchers (runPhase1Watch's deferred stopSecondaries
	// has not run yet — markPhase1Done is called inline before it). We
	// compute the census + the surface-only secondaryDegraded roll-up and
	// stamp them onto Result so a multi-region "ready" stops hiding a
	// degraded secondary. This does NOT change the Status decision below —
	// it is surface-only by design (a slow secondary must never hang the
	// prov). Computed under dep.mu so a racing State() reads a consistent
	// snapshot. snapshotSecondaryStatesLocked calls SnapshotComponents()
	// on each secondary watcher, which takes only the watcher's own lock
	// (never dep.mu), so there is no lock-ordering hazard.
	dep.Result.Regions, dep.Result.SecondaryDegraded = provisioner.ComputeRegionHealth(
		dep.Request.Provider, // #4086 — exclude provider-inapplicable HRs from the census
		primaryRegionKey(&dep.Request),
		finalStates,
		snapshotSecondaryStatesLocked(dep),
	)

	failed := 0
	for _, s := range finalStates {
		if s == helmwatch.StateFailed {
			failed++
		}
	}

	dep.FinishedAt = time.Now()
	switch {
	case outcome == helmwatch.OutcomeKubeconfigMissing:
		// Issue #488 (Phase-8a bug #8): the kubeconfig short-circuit
		// previously called markPhase1Done with an empty outcome and
		// fell through to the default "ready" branch — the wizard
		// then lied to the operator that a Sovereign was Ready when
		// catalyst-api had never even observed it. Flag it explicitly
		// as failed so the UI tells the truth.
		dep.Status = "failed"
		dep.Error = "Phase 1 watch never ran: the new Sovereign cluster did not PUT its kubeconfig to /api/v1/deployments/{id}/kubeconfig. catalyst-api cannot observe per-HelmRelease state and will not flip status to ready. Operator: SSH to the control-plane and verify cloud-init completed (`cloud-init status`), inspect `/var/log/cloud-init-output.log` for the kubeconfig PUT step (see docs/RUNBOOK-PROVISIONING.md §\"Fetch kubeconfig via SSH\")."
	case outcome == helmwatch.OutcomeWatcherStartFailed:
		// Issue #488 (Phase-8a bug #8): same false-ready failure mode,
		// different upstream cause — informer factory failed to start.
		dep.Status = "failed"
		dep.Error = "Phase 1 watch could not start (e.g. malformed kubeconfig or informer factory init failure). catalyst-api has not observed any HelmRelease state and will not flip status to ready. Operator: run `kubectl get helmrelease -n flux-system` directly against the new Sovereign for ad-hoc diagnostics."
	case outcome == helmwatch.OutcomeFluxNotReconciling:
		// Watch terminated because zero HelmReleases were ever
		// observed on the new Sovereign — Flux on that cluster is
		// not reconciling the bootstrap-kit Kustomization. This is
		// a hard failure; the operator must investigate
		// flux-system before any retry.
		dep.Status = "failed"
		dep.Error = "Phase 1 watch saw zero HelmReleases — the bootstrap-kit Kustomization on the new Sovereign is not reconciling. Operator: inspect `flux get kustomization -n flux-system` and `kubectl describe kustomization -n flux-system` on the new cluster (see docs/RUNBOOK-PROVISIONING.md §\"Phase 1 watch shows 0 HelmReleases\")."
	case outcome == helmwatch.OutcomeFluxCRDsAbsent:
		// #5042 — the new Sovereign PUT its kubeconfig and its CNI is
		// healthy (the pre-flight probe reached the apiserver), but the
		// Flux HelmRelease CRD (helmreleases.helm.toolkit.fluxcd.io) was
		// never installed — cloud-init's flux-install stage did not land.
		// Distinct from OutcomeFluxNotReconciling (CRD present, Flux
		// installed, but zero HelmReleases reconciled): here Flux itself
		// is absent, so the corrective action is on cloud-init's install
		// stage, not the bootstrap-kit Kustomization. Surfaced as a fast,
		// named failure instead of idling "phase1-watching" to the 120m
		// WatchTimeout (the #5042 forensic gap).
		dep.Status = "failed"
		dep.Error = "Phase 1 could not start: the new Sovereign has its kubeconfig and a healthy CNI, but Flux (helmreleases.helm.toolkit.fluxcd.io CRD) was never installed — cloud-init's flux-install stage did not land. Operator: SSH the control-plane and inspect `/var/log/cloud-init-output.log` for the flux-install step + `ls /var/lib/rancher/k3s/server/manifests/` for the flux manifest (see docs/RUNBOOK-PROVISIONING.md). Refs #5042."
	case failed > 0:
		dep.Status = "failed"
		dep.Error = fmt.Sprintf("Phase 1 finished with %d failed component(s); see ComponentStates for the per-component breakdown", failed)
	case outcome == "" && len(finalStates) == 0:
		// Defensive guard for any future caller that forgets to pass
		// a non-empty outcome — better to surface as "failed" with a
		// loud diagnostic than to silently flip to "ready".
		// Issue #488 (Phase-8a bug #8).
		dep.Status = "failed"
		dep.Error = "Phase 1 watch terminated with no observed components and no terminal outcome — this is a programming error in catalyst-api. Please file an issue with the deployment ID and the catalyst-api logs from this run."
	case outcome == helmwatch.OutcomeTimeout:
		// Issue #3018 (caught live on hw91, 2026-06-03): a watch that
		// hits its WatchTimeout with zero hard-FAILED but N still-
		// converging components used to fall through to the default
		// branch below and flip "ready" — the deployment record
		// claimed ready at 39/54 HRs while the console was
		// TCP-closed. A timeout is NOT convergence; "ready" gates the
		// operator-facing D0 handover surface and the UAT walk.
		// Classify honestly. The cluster itself keeps converging
		// (install retries are infinite per #2999); the watch-retry
		// path re-attaches and CAN flip ready later once every
		// component is genuinely terminal-installed.
		dep.Status = "failed"
		dep.Error = fmt.Sprintf("Phase 1 watch timed out before convergence: %d component(s) observed, none hard-failed, but not all reached installed within the watch budget. The Sovereign's Flux keeps retrying cluster-side; re-attach the watch (retry) to re-evaluate. The deployment is NOT ready until every component is installed.", len(finalStates))
	case outcome == helmwatch.OutcomeReady:
		if consoleReachErr != nil {
			// #5253 (family #4486/#4706, root-caused live on hw276) — Flux
			// converged (every PRIMARY HelmRelease installed) but the #4706
			// external console probe did not observe https://console.<fqdn>/
			// serving within its budget. The former handling latched
			// Status="failed" here, which re-introduced for the console cause
			// the EXACT architectural fault the #4486 comment below documents
			// for the absent-secondary cause: finalStatus=="failed" makes the
			// OutcomeReady terminal block skip fireHandover +
			// runAutoEstablishClusterMesh + the spine/adoption/policy hooks,
			// and BOTH heal paths (clusterMeshReconcileStatusGate,
			// shouldConvergedLateRescue) structurally excluded the
			// failed+OutcomeReady shape — so a fully-converged primary never
			// meshed, region-b never converged (no mesh → no CNPG-pair flip →
			// hub secrets never sync → keycloak + the SSO charts wedge). On
			// hw276 the trigger was additionally a FALSE NEGATIVE: the
			// console stack was healthy, its SPA root just answered 404 while
			// the front door served fine one redirect later.
			//
			// The honest, self-healing outcome mirrors #4486: STAY "ready"
			// (OutcomeReady is granted only when every primary HR installed —
			// that is the producer-chain fire condition), fire the chain, and
			// carry the console condition on the NON-FATAL ConsoleDegraded
			// surface (the #3611 surface-not-gate idiom) with a background
			// re-probe (see the OutcomeReady terminal block below) that
			// clears it the moment the front door answers. dep.Error stays
			// empty on purpose — /deployments/{id} renders a non-empty Error
			// as a hard FailureCard (the #4486 NB below). A genuinely-dead
			// console stays loudly visible via ConsoleDegraded + this warn +
			// the re-probe's terminal warn; it no longer latches the
			// cross-region topology inert. A genuinely-unconverged primary
			// never reaches this arm at all (outcome != OutcomeReady) and
			// stays failed with no handover.
			dep.Result.ConsoleDegraded = true
			dep.Result.ConsoleDegradedDetail = consoleReachErr.Error()
			h.log.Warn("phase 1 converged on the PRIMARY but the console front door was not confirmed externally reachable — staying ready + firing the producer chain; surfaced as ConsoleDegraded with a background re-probe (#5253, family #4486/#4706)",
				"id", dep.ID,
				"consoleReachErr", consoleReachErr,
			)
		}
		dep.Status = "ready"
	default:
		// Issue #3018 hardening: "ready" is granted ONLY by an
		// explicit OutcomeReady. Any future outcome constant that
		// reaches here without its own case must surface loudly
		// instead of silently impersonating success — the exact
		// mechanism that let OutcomeTimeout flip ready on hw91.
		dep.Status = "failed"
		dep.Error = fmt.Sprintf("Phase 1 watch terminated with unhandled outcome %q — catalyst-api is missing a status mapping for it. The deployment is NOT marked ready; please file an issue with the deployment ID and the catalyst-api logs from this run.", outcome)
	}

	// #3375 DoD-7 — DR INTEGRITY GATE, made SELF-FIRING for a converged
	// primary (#4486, Refs #3375 #4212). A deployment that REQUESTED
	// active-hot-standby (or active-active) but whose standby region never
	// came up cannot HONESTLY claim the topology — but the corrective
	// action depends entirely on whether the PRIMARY converged:
	//
	//   (a) Primary NOT ready (outcome != OutcomeReady — kubeconfig-missing,
	//       flux-not-reconciling, a hard-FAILED component, timeout): this is
	//       a GENUINE failure. Keep `failed` + the standby-region-absent
	//       reason. Nothing converged; there is nothing to hand over.
	//
	//   (b) Primary IS ready (outcome == OutcomeReady) but the standby is
	//       absent: the primary region is a DEGRADED-BUT-FUNCTIONAL
	//       single-region spine. Latching this to `failed` was an
	//       ARCHITECTURAL FAULT — `finalStatus=="failed"` makes the
	//       OutcomeReady terminal block below skip `fireHandover`, so
	//       `HandoverFiredAt` stays nil and the ENTIRE post-handover
	//       producer chain (spine Applications, adoption-apply, policy-flip,
	//       ClusterMesh) is permanently inert, with NO level-triggered heal
	//       (`runConvergedLateRescue` + `shouldStartupSpineReconcile` both
	//       gate on `HandoverFiredAt != nil`). A single-region-up
	//       multi-region prov could never enroll even its degraded spine.
	//       The honest, self-healing outcome is to STAY `ready`, fire the
	//       handover for the converged primary, run the spine producer, and
	//       record the standby absence as a NON-FATAL `SecondaryDegraded`
	//       condition (the same surface-not-gate idiom #3611 uses for a
	//       slow-but-present secondary) plus the `standby-region-absent`
	//       Phase1Outcome so the operator console + logs stay honest.
	//
	// This re-evaluates ONLY an already-ready deployment (it never upgrades
	// a failed one) and keys on the OBSERVED secondary-region count
	// (non-primary entries in the #3611 census). A SLOW-but-present
	// secondary HAS an observed watcher → it is counted → it does NOT trip
	// this gate at all (its slowness rides the #3611 SecondaryDegraded
	// surface independently). Only a GENUINELY-ABSENT region (no cluster, no
	// kubeconfig, zero observed secondary — the hw150 shape) reaches the
	// branch here. Generic: keys on Request.BcpTopology + region counts, no
	// app name.
	if dep.Status == "ready" {
		observedSecondary := 0
		for _, rh := range dep.Result.Regions {
			if !rh.Primary {
				observedSecondary++
			}
		}
		if ok, reason := provisioner.DeclaredDRStandbyIntegrity(
			dep.Request.BcpTopology, len(dep.Request.Regions), observedSecondary,
		); !ok {
			// The primary converged (dep.Status=="ready" is only reached on
			// outcome==OutcomeReady; the storage/kubeconfig/timeout/failed
			// branches above all set "failed"). So this is case (b): keep
			// the converged primary HANDED-OVER as a degraded single-region
			// spine instead of latching the whole chain off.
			//
			// NB: do NOT set dep.Error here — the deployment is `ready`, and
			// /deployments/{id} surfaces a non-empty Error as a top-level
			// `error` field that the wizard's FailureCard renders as a hard
			// failure. The standby absence rides the honest, non-fatal
			// channels instead: SecondaryDegraded (the #3611 surface-not-gate
			// badge), the standby-region-absent Phase1Outcome, and the loud
			// warn below. `reason` is logged, not stamped as a failure.
			dep.Result.SecondaryDegraded = true
			dep.Result.Phase1Outcome = provisioner.ReasonStandbyRegionAbsent
			h.log.Warn("phase 1 ready on the PRIMARY but the declared DR STANDBY region is ABSENT — handing over the converged primary as a DEGRADED single-region spine (NOT latching failed); DR is INACTIVE until the standby provisions (#4486, Refs #3375)",
				"id", dep.ID,
				"bcpTopology", dep.Request.BcpTopology,
				"declaredRegions", len(dep.Request.Regions),
				"observedSecondary", observedSecondary,
				"reason", reason,
			)
		}
	}

	finalStatus := dep.Status
	// Capture the #3611 region-health summary under the lock for the
	// terminal log line + the secondary-degraded warn below.
	regionHealth := dep.Result.Regions
	secondaryDegraded := dep.Result.SecondaryDegraded
	// #3971 — capture the kubeconfig path + ready-ness under the lock so
	// the storage-durability gate (run AFTER the unlock, off the lock, so
	// its bounded apiserver List never blocks a concurrent State() snapshot)
	// can probe the primary cluster's default StorageClass.
	storageGateKubeconfig := dep.Result.KubeconfigPath
	storageGateEligible := dep.Status == "ready"
	dep.mu.Unlock()

	// #3971 STORAGE DURABILITY GATE — sibling of the #3375 DR gate above,
	// run off-lock because it issues a bounded apiserver call. An otherwise-
	// ready Sovereign whose resolved DEFAULT StorageClass is still the k3s
	// ephemeral local-path (rancher.io/local-path) must NOT be claimed
	// ready: every PVC (prod Postgres 3×100Gi, openbao secrets, customer-Org
	// DB, …) would land on node-local disk and be destroyed by a node
	// replacement. The bootstrap-kit installs the per-provider cloud CSI
	// (bp-hcloud-csi slot 55a on Hetzner) which flips the default to a
	// durable cloud volume; this gate verifies the flip actually took. It is
	// CONSERVATIVE: it only DOWNGRADES ready→failed on a POSITIVE local-path
	// observation. A probe error (unreadable kubeconfig, apiserver blip,
	// missing path) SKIPS the gate — it never invents a failure, mirroring
	// the surface-not-gate discipline for slow/transient conditions.
	if storageGateEligible {
		if reason := h.evaluateStorageDurabilityGate(dep, storageGateKubeconfig); reason != "" {
			dep.mu.Lock()
			// Re-check the deployment hasn't been adopted in the gap.
			if dep.Status == "ready" {
				dep.Status = "failed"
				dep.Error = reason
				dep.Result.Phase1Outcome = provisioner.ReasonDefaultStorageClassEphemeral
				finalStatus = "failed"
			}
			dep.mu.Unlock()
		}
	}

	// Persist the deployment record after the status flip so a
	// concurrent State() snapshot picked up by the wizard's poll
	// reads the same value. Without this, the in-memory record
	// would say "ready" but the on-disk JSON file (read by
	// /deployments/{id} after a Pod restart) would still say
	// "phase1-watching" — a Pod kill in the gap between flip and
	// next event would leave the deployment stuck in the wrong
	// state forever. otech48 verified the gap: the in-memory
	// transition was correct in earlier code paths but never
	// persisted, so any state read from disk was stale.
	h.persistDeployment(dep)

	h.log.Info("phase 1 watch terminated",
		"id", dep.ID,
		"componentCount", len(finalStates),
		"failedCount", failed,
		"finalStatus", finalStatus,
		"phase1Outcome", outcome,
		"regions", formatRegionHealth(regionHealth),
		"secondaryDegraded", secondaryDegraded,
	)

	// #3611 — make a "ready" deployment HONEST about a degraded secondary.
	// Status stays "ready" by design (surface-not-gate, so a slow secondary
	// never hangs the prov), but a degraded secondary is overclaiming if it
	// is invisible. Emit a loud warn so the condition is greppable in the
	// catalyst-api logs even when the operator only ever sees the green
	// "ready" pill. The per-region breakdown rides on Result.Regions for
	// the console/jobs view.
	if secondaryDegraded {
		h.log.Warn("phase 1 ready but a SECONDARY region is degraded — surfaced via Result.Regions, status intentionally left ready (surface-not-gate, #3611)",
			"id", dep.ID,
			"finalStatus", finalStatus,
			"regions", formatRegionHealth(regionHealth),
		)
	}

	// Issues #764 + #768 — auto-fire the handover JWT mint immediately
	// after Phase-1 reaches OutcomeReady. Until this landed, the
	// operator was stranded on the wizard's provision page after a
	// successful provision: the page rendered the apps grid in
	// terminal-completed state but the "Open your Sovereign console →"
	// button + the auto-redirect never appeared because the JWT mint
	// was a manual operator step (POST /deployments/{id}/mint-handover-
	// token, called by a button the wizard never showed unless the SSE
	// banner specifically prompted it).
	//
	// Auto-fire happens here, AFTER the lock is released and AFTER the
	// terminal Status flip is persisted, so the SSE event the
	// fireHandover helper emits is guaranteed to land on the durable
	// buffer ordered AFTER the Phase-1 terminal events. Tests assert
	// the buffer ordering invariant; the wizard's reducer relies on
	// it.
	if outcome == helmwatch.OutcomeReady && finalStatus == "ready" {
		h.fireHandover(dep)
		// Auto-establish Cilium ClusterMesh across regions after
		// Phase-1 converges. The orchestrator wires every region's
		// clustermesh-apiserver into a fully-connected peer mesh.
		// Skipped automatically for single-region provs
		// (len(dep.Request.Regions) < 2) inside the helper.
		//
		// Run on a background goroutine so the helmwatch terminate
		// path (which holds no locks now) doesn't block on the
		// per-region LB IP wait loops (each up to 5 min).
		// docs/SOVEREIGN-MULTI-REGION-DOD.md gates D9-D12.
		h.spawnPostHandoverHook(func() { h.runAutoEstablishClusterMesh(dep) })
		// C10-003 (2026-05-17 t143): when Phase-1 reaches
		// OutcomeReady, the PRIMARY's terminate path persists the
		// final per-Job status from its own helmwatch state map.
		// Secondary regions' install-* Jobs live on the per-region
		// bridge but are wired via separate watcher event streams
		// (spawnSecondaryRegionWatchers above), and stale events
		// (e.g. a transient HelmStatePending observed during initial
		// dep-not-ready cycles, then suppressed by lastState dedup
		// before the Installed transition was ever observed) can
		// leave their Job rows pinned to "pending" even though
		// kubectl reports every HR Ready=True. Founder-flagged on
		// t10 2026-05-17 (install-nbg1-1:*, install-sin-2:* stuck
		// pending despite deployment status=ready).
		//
		// Re-seed every secondary watcher from its current
		// informer cache so each install-<region>:<chart> Job row
		// converges onto the cluster-current HelmState. The seed
		// path is idempotent (mergeJob preserves monotonic
		// timestamps + non-empty DependsOn; SeedJobsFromInformerList
		// matches OnHelmReleaseEvent's Status mapping), so this is
		// safe to call multiple times.
		//
		// CRITICAL: invoke INLINE, not on a goroutine — runPhase1Watch
		// holds `defer stopSecondaries()` which clears
		// dep.secondaryWatchers as soon as markPhase1Done returns.
		// A go-spawned backfill would race the cleanup and observe
		// an empty map ~50% of the time. The backfill itself is
		// in-memory work (informer snapshot + bridge merge), no
		// network I/O — running it on the terminate path's stack
		// adds ≤100ms before markPhase1Done's caller resumes.
		h.runSecondaryBridgeBackfill(dep)

		// Issue #3277 — sweep any still-reconciling bp-* install Job row
		// terminal now that handover has succeeded. The helmwatch derives
		// one Job row per HR from live state, but a row whose last-seen
		// state was non-terminal (informer-dedup loss, a Pod restart that
		// lost the in-memory cursor before the terminal edge, or a
		// secondary watcher whose stream stalled) freezes at
		// "running"/"pending" forever — the mother stops watching after
		// handover, so nothing ever resolves it and the operator's
		// bootstrap view reads "stuck running". The Sovereign is now
		// self-managing every component, so the mother's view should read
		// complete. This runs ONLY on OutcomeReady (a SUCCESSFUL handover)
		// so a failed Phase-1 keeps its failure visible; the sweep itself
		// leaves any already-Failed row untouched (never masks a real
		// per-component failure as handed-over). Run INLINE for the same
		// reason as runSecondaryBridgeBackfill above — in-memory store
		// work, no network I/O, and `defer stopSecondaries()` tears down
		// the watchers as soon as markPhase1Done returns.
		h.runHandoverJobSweep(dep)

		// Wave 5.90 phase 2b (#2441): post-handover flip of bp-kyverno-
		// policies bootstrapMode from true (fresh-prov default — every
		// ClusterPolicy renders Audit) → false (canonical per-policy
		// action restored; 6 policies upgrade to Enforce target state).
		// Without this hook, every Sovereign stays at Audit forever
		// because the chart-side default never gets flipped.
		//
		// Background goroutine so the Phase-1 terminate path's own SSE
		// event ordering is not blocked by the 30s per-request REST
		// timeout. Failures log + emit SSE warn but don't fail the
		// handover (Audit-stuck is non-catastrophic; operators can
		// manually patch per #2441 fallback). See post_handover_policy_
		// enforce.go for the full helper.
		h.spawnPostHandoverHook(func() { h.runPostHandoverPolicyEnforceFlip(dep) })

		// #4212 (Path B of #4018; un-gates #4002): carry the real-id
		// Observe-first CloudAdoption claims from the deploy workdir's
		// terraform.tfstate onto the Sovereign cluster, so Crossplane
		// actually OBSERVES the OpenTofu-provisioned ELB/server/network/
		// eip by external-name (instead of sitting inert with only the
		// `PENDING-GENERATION` placeholder). Observe-first → never
		// re-provisions the running platform. Background goroutine +
		// internal retry budget: the CloudAdoption CRD (bp-crossplane-
		// claims slot 14) + provider-opentofu (infrastructure-providers Flux
		// Kustomization, LAYER 1 of the #4521 two-layer crossplane bootstrap)
		// may still be installing at first OutcomeReady, so
		// this must NOT block the terminate path. Failures log + emit SSE
		// warn but never fail the handover (adoption is a day-2
		// observability surface). See post_handover_adoption_apply.go.
		h.spawnPostHandoverHook(func() { h.runPostHandoverAdoptionApply(dep) })

		// #4212 Seam 3 (folds #3829): enroll the DR-capable bootstrap spine
		// (openbao/keycloak/harbor/gitea) into the object model by stamping
		// one idempotent Application CR per spine HelmRelease. Each enters
		// the application-controller reconcile fan-out, which mints its
		// Continuum DR contract — adopt-not-roll (the CR is additive; the
		// upsertHostResource byte-equal short-circuit never re-renders the
		// healthy spine pod). Background goroutine + internal retry budget:
		// the spine HRs may still be reconciling at first OutcomeReady, so
		// this must NOT block the terminate path. Failures log + emit SSE
		// warn but never fail the handover (spine enrollment is a day-2
		// object-model surface). See post_handover_spine_apps.go.
		h.spawnPostHandoverHook(func() { h.runPostHandoverSpineApplications(dep) })

		// #4690 / #4686: on the CCM-less Huawei provider, reconcile the Huawei
		// gateway ELB's pool MEMBER SET to the live cluster's node IPs at the
		// durable ports (public :443/:80 → node:443/:80, the cilium-envoy
		// hostNetwork host ports — §854, no nodePort anywhere; the former
		// discover-nodePort shape of #4690/#4691 was retired by the cilium
		// 1.19.3 bump). Without this, autoscaler-added nodes are missing from
		// the pools and replaced/removed nodes leave stale members. Background
		// goroutine + internal retry budget: the node set may still be settling
		// at first OutcomeReady, so this must NOT block the terminate path.
		// No-op on Hetzner (hcloud-ccm programs node:443 directly). Failures
		// log + emit SSE warn but never fail the handover. See
		// post_handover_gateway_elb.go.
		h.spawnPostHandoverHook(func() { h.runPostHandoverGatewayELB(dep) })

		// #5253 — when the #4706 console probe did NOT confirm reachability,
		// the record is ready with the non-fatal ConsoleDegraded surface set
		// (see the OutcomeReady case above). Keep re-probing in the
		// background and clear the surface the moment the front door
		// answers — the producer chain above already fired and is NOT gated
		// on this. Failures only refresh the surfaced diagnostic.
		if consoleReachErr != nil {
			h.spawnPostHandoverHook(func() { h.runConsoleReachabilityReprobe(dep) })
		}
	} else if outcome == helmwatch.OutcomeTimeout && len(dep.Request.Regions) >= 2 {
		// #3285/hw130 (2026-06-12): a Phase-1 TIMEOUT is the
		// recoverable classification ("components observed, none
		// hard-failed, not all converged") — Flux keeps reconciling
		// the cluster long after the watch budget expires, and the
		// doctrine forbids wiping such envs. But the mesh orchestrator
		// previously fired ONLY on OutcomeReady, so a timeout record
		// was permanently abandoned: no mesh, no cnpg-pair flip, no
		// Pillar-3 — witnessed live on hw130, fully converged at the
		// cluster level with 0/0 mesh forever. Kick the level-trigger
		// for timeouts too: its first attempts may fail while the
		// cluster catches up (warn-and-retry is the loop's design) and
		// it converges exactly when the env does. Handover, the job
		// sweep, and the policy flip stay ready-only — this rescues
		// the TOPOLOGY, not the lifecycle.
		h.spawnPostHandoverHook(func() { h.runAutoEstablishClusterMesh(dep) })
		// #3277 (founder, 2026-06-12): the secondary-region job rows
		// freeze at their last-seen state forever on a timeout record
		// (witnessed live: 9 "running" rows on hw130 with every
		// component long Ready) because the C10-003 backfill ran only
		// on OutcomeReady. The informer-cache reseed is read-only and
		// converges each install-<region>:<chart> row to the CLUSTER-
		// CURRENT state — exactly as valid at a timeout terminal as at
		// ready. INLINE for the same reason as the ready path: the
		// deferred stopSecondaries clears the watcher map the moment
		// markPhase1Done returns.
		h.runSecondaryBridgeBackfill(dep)
	}
}

// evaluateStorageDurabilityGate probes the primary Sovereign cluster's
// default StorageClass (#3971) and returns a non-empty failure reason ONLY
// when the default is the k3s ephemeral local-path provisioner — the
// non-durable condition the bootstrap-kit's per-provider cloud CSI is
// supposed to flip away from. Empty return = PASS (durable default) or SKIP
// (probe could not run; we never invent a failure from a probe error).
//
// Read-only per docs/PRINCIPLES.md #3 — it lists StorageClasses, patches
// nothing. The probe is bounded by the helmwatch rest.Config timeout (30s).
//
// The actual probe is injectable via h.phase1StorageGate so unit tests can
// drive the ready→failed flip with a canned result; production binds the
// kubeconfig-reading helmwatch.DefaultStorageClassFromKubeconfig.
func (h *Handler) evaluateStorageDurabilityGate(dep *Deployment, kubeconfigPath string) string {
	if kubeconfigPath == "" {
		// No kubeconfig path captured (older record / short-circuit) —
		// cannot probe; SKIP rather than fail a ready deployment.
		return ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	probe := h.phase1StorageGate
	if probe == nil {
		// Production default: read the kubeconfig file off the PVC and
		// query the live cluster's StorageClasses with a typed client.
		probe = func(ctx context.Context, path string) (helmwatch.DefaultStorageClassInfo, error) {
			raw, err := os.ReadFile(path)
			if err != nil {
				return helmwatch.DefaultStorageClassInfo{}, fmt.Errorf("read kubeconfig %s: %w", path, err)
			}
			return helmwatch.DefaultStorageClassFromKubeconfig(ctx, string(raw))
		}
	}

	info, err := probe(ctx, kubeconfigPath)
	if err != nil {
		// Probe failed (apiserver blip, unreadable kubeconfig) — SKIP.
		// A transient probe error must never downgrade a ready Sovereign;
		// the gate fires only on a POSITIVE local-path observation.
		h.log.Warn("phase 1 default-StorageClass durability gate could not probe the new Sovereign — skipping (will not downgrade ready); inspect `kubectl get sc` manually (#3971)",
			"id", dep.ID,
			"err", err,
		)
		return ""
	}

	if !info.Found {
		// No default StorageClass at all — distinct from local-path, but
		// still a misconfiguration (every PVC without an explicit
		// storageClassName stays Pending forever). Fail the prov honestly.
		h.log.Error("phase 1 storage gate: NO default StorageClass on the new Sovereign — every PVC without an explicit storageClassName will hang Pending (#3971)",
			"id", dep.ID,
		)
		return "Phase 1 storage durability gate failed: the new Sovereign has NO default StorageClass. Every PVC that omits storageClassName (cnpg, openbao, keycloak, gitea, harbor) will stay Pending forever. The bootstrap-kit's cloud block-storage CSI (bp-hcloud-csi on Hetzner) did not install or did not flip the cluster default. Operator: inspect `kubectl get sc` and `flux get helmrelease bp-hcloud-csi -n flux-system` on the new cluster (#3971)."
	}

	if info.Ephemeral {
		h.log.Error("phase 1 storage gate: default StorageClass is the EPHEMERAL k3s local-path — production data is non-durable (#3971)",
			"id", dep.ID,
			"defaultStorageClass", info.Name,
			"provisioner", info.Provisioner,
		)
		return fmt.Sprintf("Phase 1 storage durability gate failed: the new Sovereign's default StorageClass is %q (%s) — ephemeral node-local disk with NO cloud block volume behind it. Every PVC (prod Postgres 3×100Gi, openbao secrets, customer-Org DB, keycloak/gitea/harbor) would be destroyed by a node replacement, invalidating the BCP/DR model. The bootstrap-kit's cloud block-storage CSI (bp-hcloud-csi slot 55a on Hetzner) did not install or did not flip the cluster default to a durable cloud volume. Operator: inspect `kubectl get sc` and `flux get helmrelease bp-hcloud-csi -n flux-system` on the new cluster (#3971).", info.Name, info.Provisioner)
	}

	// Durable default (cloud CSI). PASS. Log the success so the condition
	// is greppable in the catalyst-api logs alongside the other gates.
	h.log.Info("phase 1 storage durability gate passed: default StorageClass is a durable cloud volume (#3971)",
		"id", dep.ID,
		"defaultStorageClass", info.Name,
		"provisioner", info.Provisioner,
		"multipleDefaults", info.MultipleDefaults,
	)
	return ""
}

// primaryRegionKey returns the declared primary region's key for the
// #3611 per-region health census — Regions[0].CloudRegion, falling back
// to the legacy singular Request.Region. Empty when neither is set (a
// pre-Regions hand-crafted payload); ComputeRegionHealth then labels the
// primary entry "primary".
func primaryRegionKey(req *provisioner.Request) string {
	if req == nil {
		return ""
	}
	if len(req.Regions) > 0 && req.Regions[0].CloudRegion != "" {
		return req.Regions[0].CloudRegion
	}
	return req.Region
}

// snapshotSecondaryStatesLocked builds region-key → (component-id →
// helmwatch state) for every currently-attached secondary watcher, by
// calling SnapshotComponents() on each (#3611). The CALLER MUST hold
// dep.mu — this reads dep.secondaryWatchers. SnapshotComponents itself
// takes only the watcher's own lock, never dep.mu, so calling it under
// dep.mu is safe (no inverse acquisition path exists).
//
// Returns nil for a single-region deployment (no secondary watchers),
// which ComputeRegionHealth treats as "primary only".
func snapshotSecondaryStatesLocked(dep *Deployment) map[string]map[string]string {
	if len(dep.secondaryWatchers) == 0 {
		return nil
	}
	out := make(map[string]map[string]string, len(dep.secondaryWatchers))
	for region, w := range dep.secondaryWatchers {
		if w == nil {
			// Emit an empty map so the region still shows up as 0/0 in
			// the census (its kubeconfig landed + a watcher slot was
			// allocated, but the watcher object is gone) rather than
			// silently vanishing from the breakdown.
			out[region] = map[string]string{}
			continue
		}
		states := make(map[string]string)
		for _, cs := range w.SnapshotComponents() {
			states[cs.AppID] = cs.Status
		}
		out[region] = states
	}
	return out
}

// formatRegionHealth renders the #3611 per-region census as a compact
// log-friendly string, e.g. "me-east-215-a=60/63(primary)
// me-east-215-b=48/63(DEGRADED)". Used only for structured-log values.
func formatRegionHealth(regions []provisioner.RegionHealth) string {
	if len(regions) == 0 {
		return ""
	}
	parts := make([]string, 0, len(regions))
	for _, r := range regions {
		tag := ""
		switch {
		case r.Primary:
			tag = "(primary)"
		case r.Degraded:
			tag = "(DEGRADED)"
		}
		parts = append(parts, fmt.Sprintf("%s=%d/%d%s", r.Region, r.HRReady, r.HRTotal, tag))
	}
	return strings.Join(parts, " ")
}

// runSecondaryBridgeBackfill walks every secondary watcher attached to
// the deployment, snapshots each one's informer cache, and reseeds the
// shared jobs.Bridge with the cluster-current state. This is the
// recovery path for C10-003 — secondary install Jobs stuck "pending"
// after deployment status=ready, caused by a transient event lost to
// the bridge's lastState dedup (the seed observed HelmStatePending at
// initial-list, the Installed transition never produced a distinct
// event because the watcher attached AFTER the HR had already settled
// at Installed — same state, dedup suppresses, status stays pending).
//
// Run INLINE from markPhase1Done — runPhase1Watch's
// `defer stopSecondaries()` clears dep.secondaryWatchers immediately
// after markPhase1Done returns, so a goroutine-spawned backfill would
// race the cleanup. The work is in-memory only (informer snapshot +
// bridge merge); no network I/O justifies a goroutine.
//
// Errors are logged at warn; this is a best-effort convergence helper,
// not a correctness gate.
func (h *Handler) runSecondaryBridgeBackfill(dep *Deployment) {
	defer func() {
		if r := recover(); r != nil {
			h.log.Error("secondary bridge backfill: panic recovered",
				"id", dep.ID,
				"panic", r,
			)
		}
	}()
	dep.mu.Lock()
	watchers := make(map[string]*helmwatch.Watcher, len(dep.secondaryWatchers))
	for region, w := range dep.secondaryWatchers {
		watchers[region] = w
	}
	bridge := dep.jobsBridge
	dep.mu.Unlock()
	if bridge == nil || len(watchers) == 0 {
		return
	}
	for region, watcher := range watchers {
		if watcher == nil {
			continue
		}
		snap := watcher.SnapshotComponents()
		if len(snap) == 0 {
			continue
		}
		seeds := snapshotsToSeedsForRegion(snap, region)
		jobsCount, execsSeeded, err := bridge.SeedJobsFromInformerList(seeds)
		if err != nil {
			h.log.Warn("secondary bridge backfill: reseed failed",
				"id", dep.ID,
				"region", region,
				"snapshotCount", len(snap),
				"err", err,
			)
			continue
		}
		h.log.Info("secondary bridge backfill: reseeded from informer cache",
			"id", dep.ID,
			"region", region,
			"snapshotCount", len(snap),
			"jobsWritten", jobsCount,
			"executionsSeeded", execsSeeded,
		)
	}
}

// runHandoverJobSweep stamps every still-reconciling bp-* install Job
// row terminal at a successful Phase-1 handover (issue #3277). Delegates
// to Bridge.SweepHandoverInstallJobs, which leaves already-Failed rows
// untouched (a real per-component failure is never masked) and stamps
// each non-terminal install row Succeeded with an honest hand-over
// LogLine.
//
// Called ONLY from markPhase1Done's OutcomeReady branch, so it can only
// ever run on a SUCCESSFUL handover — a failed Phase-1 keeps its failure
// visible. Best-effort: a store error is logged at warn but never
// derails the handover (the row staying "running" is a cosmetic
// regression on a healthy cluster, not a correctness failure).
//
// Resolves the per-deployment Bridge the same way the rest of the
// handler does (bridgeFor get-or-create) so the sweep runs against the
// same jobs.Store the helmwatch feed already wrote to. A nil h.jobs
// (CI without persistence) makes bridgeFor hand back a no-op in-memory
// bridge whose ListJobs is empty — the sweep is a harmless zero-row
// walk there.
func (h *Handler) runHandoverJobSweep(dep *Deployment) {
	defer func() {
		if r := recover(); r != nil {
			h.log.Error("handover job sweep: panic recovered",
				"id", dep.ID,
				"panic", r,
			)
		}
	}()
	if h.jobs == nil {
		return
	}
	bridge := h.bridgeFor(dep)
	if bridge == nil {
		return
	}
	swept, err := bridge.SweepHandoverInstallJobs(time.Now().UTC())
	if err != nil {
		h.log.Warn("handover job sweep: failed",
			"id", dep.ID,
			"err", err,
		)
		return
	}
	if swept > 0 {
		h.log.Info("handover job sweep: stamped still-reconciling install rows terminal",
			"id", dep.ID,
			"sweptRows", swept,
		)
	}
}

// runAutoEstablishClusterMesh is the goroutine wrapper around
// AutoEstablishClusterMesh — used by markPhase1Done so the terminate
// path returns immediately, and by restoreFromStore so a catalyst-api
// roll re-converges a partially-meshed ready Sovereign zero-touch
// (#3241). Centralising the recover() + ctx bound + retry loop here
// keeps both call sites one line.
//
// Level-triggered, not edge-triggered. hw126 (c986326a77d391d4)
// proved the prior one-shot shape loses the race against LB-IPAM: the
// primary's clustermesh-apiserver LB IP landed seconds AFTER the
// single 3-second fan-out gave up, nothing ever re-ran the establish
// (next trigger = handover), so a healthy cluster stayed partially
// meshed forever and the #3236 cnpg-pair flip correctly refused
// forever with it. Every step inside AutoEstablishClusterMesh is
// idempotent (Secret writes are get-then-merge, enable/connect
// re-runs are no-ops, the #3236 flip is a same-value merge patch), so
// re-running until fully meshed converges instead of thrashing.
//
// Loop shape: run the establish; when every region reports ReadyAt
// (fully meshed) AND the #3236 cnpg-pair flip has landed, emit the final
// success event ONCE and transition into the steady-state heal phase
// (below) — it does NOT exit. Otherwise sleep an exponential backoff
// (clusterMeshRetryInitialBackoff doubling to clusterMeshRetryMaxBackoff)
// and re-run, bounded by the clusterMeshRetryBudget total and by the
// deployment staying status=ready (a wipe mid-loop stops the retries —
// the kubeconfigs are gone or going). Each retry emits a
// clustermesh-progress event (attempt N, fullyMeshed X/Y) so the operator
// watches convergence, not silence.
//
// Steady-state heal phase (#3583): after first convergence the goroutine
// keeps re-running the SAME idempotent establish at the much slower
// clusterMeshSteadyStateInterval, bounded ONLY by the deployment staying
// status=ready (its own context.Background()-derived ctx, NOT the retry
// budget — the budget only governs the bounded convergence phase). hw144
// proved why a permanent exit was wrong: the 6 shared-pg replica-auth
// `<name>-replication`/`-ca` Secrets were copied primary→replica at
// convergence, the loop converged + exited, then convergence churn
// (gitea/harbor uninstall-remediation loops) collaterally DELETED them
// from the replica's shared-data namespace and NOTHING re-copied (the loop
// was gone, the next trigger is a catalyst-api restart) → the shared-pg
// replicas could never pg_basebackup → cross-region DR went hollow. Every
// step inside AutoEstablishClusterMesh is idempotent and the
// copySecretAcrossClusters write is now skipped when the destination
// already matches (#3583), so a steady-state pass is a cheap Get+compare
// that writes ONLY when it is actually healing a drift. The first-
// convergence success event still fires exactly once (operator signal);
// the status!=ready check ends the goroutine on wipe.
func (h *Handler) runAutoEstablishClusterMesh(dep *Deployment) {
	// #4811 — one establish+heal loop per deployment. A second concurrent
	// trigger (the startup retry racing markPhase1Done / converged-late, or two
	// restore passes) LoadOrStores the same dep ID and becomes a no-op instead
	// of spinning a competing loop that thrashes the same idempotent Secret
	// writes. Cleared on return so a later legitimate re-trigger (the next
	// catalyst-api roll) still runs.
	if _, active := h.clusterMeshLoopActive.LoadOrStore(dep.ID, struct{}{}); active {
		h.log.Info("clustermesh: establish loop already active for deployment — skipping duplicate start",
			"id", dep.ID,
		)
		return
	}
	defer h.clusterMeshLoopActive.Delete(dep.ID)

	defer func() {
		if r := recover(); r != nil {
			h.log.Error("clustermesh: orchestrator panic recovered",
				"id", dep.ID,
				"panic", r,
			)
		}
	}()

	attemptTimeout := h.clusterMeshAttemptTimeout
	if attemptTimeout <= 0 {
		attemptTimeout = clusterMeshAttemptTimeoutDefault
	}

	dep.mu.Lock()
	regionCount := len(dep.Request.Regions)
	dep.mu.Unlock()
	if regionCount < 2 {
		// Single-region: nothing to mesh with — preserve the one-shot
		// shape (AutoEstablishClusterMesh logs the skip) and never
		// retry.
		ctx, cancel := context.WithTimeout(context.Background(), attemptTimeout)
		defer cancel()
		if _, err := h.AutoEstablishClusterMesh(ctx, dep); err != nil {
			h.log.Warn("clustermesh: orchestrator returned error",
				"id", dep.ID,
				"err", err,
			)
		}
		return
	}

	initialBackoff := h.clusterMeshRetryInitialBackoff
	if initialBackoff <= 0 {
		initialBackoff = clusterMeshRetryInitialBackoffDefault
	}
	maxBackoff := h.clusterMeshRetryMaxBackoff
	if maxBackoff <= 0 {
		maxBackoff = clusterMeshRetryMaxBackoffDefault
	}
	budget := h.clusterMeshRetryBudget
	if budget <= 0 {
		budget = clusterMeshRetryBudgetDefault
	}

	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	backoff := initialBackoff
	for attempt := 1; ; attempt++ {
		attemptCtx, cancelAttempt := context.WithTimeout(ctx, attemptTimeout)
		statuses, cnpgPairConverged, err := h.autoEstablishClusterMesh(attemptCtx, dep)
		cancelAttempt()

		fullyMeshed := countFullyMeshedRegions(statuses)
		meshConverged := len(statuses) >= 2 && fullyMeshed == len(statuses)
		// Convergence for a multi-region deployment requires BOTH the mesh
		// fully established AND the #3236 cnpg-pair flip landed. hw126 at
		// 22:38 was mesh-fully-established (2/2) yet the flip correctly
		// REFUSED because the primary's replica-auth Secrets weren't minted
		// for another 10 minutes — the prior `fullyMeshed == regions` exit
		// declared victory anyway and STOPPED, so nothing ever re-ran the
		// copy+flip and a manual catalyst-api restart was needed. Treating
		// "meshed but flip not landed" as NOT-converged keeps the loop
		// retrying on the existing backoff until the flip lands (or the
		// budget exhausts).
		if err == nil && meshConverged && cnpgPairConverged {
			h.emitClusterMeshProgress(dep, "info",
				fmt.Sprintf("ClusterMesh reconcile: fully meshed (%d/%d regions) + cnpg-pair flip landed on attempt %d — reconcile loop complete", fullyMeshed, len(statuses), attempt))
			h.log.Info("clustermesh: reconcile loop converged",
				"id", dep.ID,
				"attempt", attempt,
				"regions", len(statuses),
			)
			// #3583: do NOT exit on first convergence — transition into the
			// steady-state heal phase so a collaterally-deleted replica-auth
			// Secret is re-copied on the next pass (hw144). The success event
			// above fires exactly once (the operator's "converged" signal);
			// the heal phase runs on its own long-lived ctx (not the retry
			// budget) and ends only when the deployment leaves status=ready.
			h.runClusterMeshSteadyStateHeal(dep)
			return
		}
		if err != nil {
			h.log.Warn("clustermesh: orchestrator returned error",
				"id", dep.ID,
				"attempt", attempt,
				"err", err,
			)
		}

		// A deployment that left status=ready mid-loop (wipe, failure
		// rewrite) must not keep getting establish attempts hurled at
		// it for the rest of the budget.
		dep.mu.Lock()
		status := dep.Status
		dep.mu.Unlock()
		if status != "ready" {
			h.log.Info("clustermesh: reconcile loop stopped — deployment no longer ready",
				"id", dep.ID,
				"status", status,
				"attempt", attempt,
			)
			return
		}

		total := len(statuses)
		if total == 0 {
			// nil statuses (orchestrator error or no reachable
			// regions) — report against the region count so the
			// operator-facing X/Y stays meaningful.
			total = regionCount
		}
		// Distinguish the two not-yet-converged shapes so the operator
		// watches the right thing: a partial mesh vs a fully-meshed
		// cluster still waiting on the cnpg-pair flip (the hw126 state).
		progress := fmt.Sprintf("%d/%d regions fully meshed", fullyMeshed, total)
		if meshConverged && !cnpgPairConverged {
			progress = fmt.Sprintf("%d/%d regions fully meshed but the cnpg-pair flip has not landed (primary replica-auth Secrets likely still pending)", fullyMeshed, total)
		}
		h.emitClusterMeshProgress(dep, "warn",
			fmt.Sprintf("ClusterMesh reconcile: attempt %d ended with %s — retrying in %s", attempt, progress, backoff))
		if sleepErr := sleepCtx(ctx, backoff); sleepErr != nil {
			h.emitClusterMeshProgress(dep, "warn",
				fmt.Sprintf("ClusterMesh reconcile: retry budget exhausted after %d attempt(s) with %s — next trigger is a catalyst-api restart or the post-handover finalisation", attempt, progress))
			h.log.Warn("clustermesh: reconcile loop retry budget exhausted",
				"id", dep.ID,
				"attempts", attempt,
				"fullyMeshed", fullyMeshed,
				"cnpgPairConverged", cnpgPairConverged,
				"err", sleepErr,
			)
			return
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// runClusterMeshSteadyStateHeal is the post-convergence phase of
// runAutoEstablishClusterMesh (#3583). Once the bounded retry loop has
// reached full convergence (mesh established + #3236 cnpg-pair flip
// landed) it does NOT exit; it re-runs the idempotent
// autoEstablishClusterMesh at the slow clusterMeshSteadyStateInterval for
// as long as the deployment stays status=ready, so a replica-auth Secret
// that gets collaterally deleted out of the replica's namespace (hw144:
// the shared-pg `<name>-replication`/`-ca` Secrets, deleted by convergence
// churn's gitea/harbor uninstall-remediation loops) is re-copied on the
// next pass instead of dangling until the next catalyst-api restart.
//
// Lifecycle contract (do NOT widen):
//   - Its own ctx is derived from context.Background(), NOT the retry
//     budget — the budget bounds only the convergence phase; steady-state
//     is unbounded in time and ends solely on status != "ready".
//   - status is re-checked before AND after every pass (a wipe that lands
//     mid-sleep or mid-pass exits the goroutine promptly — the kubeconfigs
//     are gone or going, same rationale as the convergence loop's check).
//   - Each pass is bounded by clusterMeshAttemptTimeout, mirroring the
//     convergence loop's per-attempt bound.
//   - Reads dep.Status / dep.Request under dep.mu only; writes NOTHING to
//     dep.Result (the package's -race rule: dep.Result is stamped at
//     startup only). The success event already fired exactly once in the
//     caller; steady-state stays quiet (debug-level log) on the common
//     no-drift pass and only emits a clustermesh-progress event when a
//     pass actually re-heals or regresses.
func (h *Handler) runClusterMeshSteadyStateHeal(dep *Deployment) {
	interval := h.clusterMeshSteadyStateInterval
	if interval <= 0 {
		interval = clusterMeshSteadyStateIntervalDefault
	}
	attemptTimeout := h.clusterMeshAttemptTimeout
	if attemptTimeout <= 0 {
		attemptTimeout = clusterMeshAttemptTimeoutDefault
	}

	// Long-lived ctx: lives until the deployment leaves status=ready (the
	// status check below cancels the work), independent of the convergence
	// budget that bounded the caller's retry loop.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h.log.Info("clustermesh: steady-state heal phase started — re-reconciling on interval until the deployment leaves status=ready",
		"id", dep.ID,
		"interval", interval,
	)

	for {
		dep.mu.Lock()
		status := dep.Status
		dep.mu.Unlock()
		if status != "ready" {
			h.log.Info("clustermesh: steady-state heal phase stopped — deployment no longer ready",
				"id", dep.ID,
				"status", status,
			)
			return
		}

		if sleepErr := sleepCtx(ctx, interval); sleepErr != nil {
			// ctx cancelled (process shutdown) — stop quietly.
			h.log.Info("clustermesh: steady-state heal phase stopped — context cancelled",
				"id", dep.ID,
				"err", sleepErr,
			)
			return
		}

		// Re-check after the sleep: a wipe that landed during the interval
		// must not get an establish attempt hurled at a tearing-down cluster.
		dep.mu.Lock()
		status = dep.Status
		dep.mu.Unlock()
		if status != "ready" {
			h.log.Info("clustermesh: steady-state heal phase stopped — deployment no longer ready",
				"id", dep.ID,
				"status", status,
			)
			return
		}

		// #4000 — DURABLE secondary-kubeconfig delivery. Re-forward every
		// on-disk secondary kubeconfig to the chroot's
		// /api/v1/sovereign/secondary-kubeconfig endpoint on every steady-state
		// pass. The one-shot handover export (exportSecondaryKubeconfigsToChild)
		// fires only at fireHandover + operator re-mint; if both windows missed
		// the chroot's `api.<fqdn>` backend, the chroot's kubeconfigs dir stays
		// EMPTY forever and the placement resolver collapses every multi-region
		// app to a false `singleton` (the hw174 ea30d1d816f2eee2 root cause).
		// The chroot's handler is idempotent (overwrite + AddCluster no-op) and
		// applies the #3991/#4000 EIP→private-IP heal, so this level-triggered
		// re-forward makes a fresh prov converge to active-active zero-touch.
		h.reforwardSecondaryKubeconfigsToChild(dep)

		attemptCtx, cancelAttempt := context.WithTimeout(ctx, attemptTimeout)
		statuses, cnpgPairConverged, err := h.autoEstablishClusterMesh(attemptCtx, dep)
		cancelAttempt()

		fullyMeshed := countFullyMeshedRegions(statuses)
		meshConverged := len(statuses) >= 2 && fullyMeshed == len(statuses)
		if err != nil {
			// A transient error on a steady-state pass is not fatal — the
			// next pass retries. Surface it so a persistent drift is visible.
			h.emitClusterMeshProgress(dep, "warn",
				fmt.Sprintf("ClusterMesh steady-state heal: reconcile pass returned an error (%v) — retrying on the next %s interval", err, interval))
			h.log.Warn("clustermesh: steady-state heal pass returned error",
				"id", dep.ID,
				"err", err,
			)
			continue
		}
		if meshConverged && cnpgPairConverged {
			// Common case: still fully converged, the copy was a no-op
			// (destination already matched). Stay quiet — debug-level only.
			h.log.Debug("clustermesh: steady-state heal pass — still fully converged, no drift",
				"id", dep.ID,
				"regions", len(statuses),
			)
			continue
		}
		// A drift was found and (idempotently) re-healed by this pass — the
		// copy re-created the deleted Secret(s) / the flip was re-applied.
		// Surface it so the operator sees the self-heal fire (hw144 shape).
		h.emitClusterMeshProgress(dep, "info",
			fmt.Sprintf("ClusterMesh steady-state heal: re-reconciled a drift (%d/%d regions meshed, cnpg-pair flip landed=%t) — replica-auth Secrets / gate re-applied", fullyMeshed, len(statuses), cnpgPairConverged))
		h.log.Info("clustermesh: steady-state heal pass re-reconciled a drift",
			"id", dep.ID,
			"fullyMeshed", fullyMeshed,
			"regions", len(statuses),
			"cnpgPairConverged", cnpgPairConverged,
		)
	}
}

// fireHandover mints a handover JWT, persists handoverFiredAt +
// handoverURL onto the deployment record, and emits a typed SSE event
// `event: handover-ready, data: { handoverURL, expiresAt }` so the
// wizard's provision page can render the "Open your Sovereign console
// →" button + auto-redirect immediately (issues #764 + #768).
//
// The mint goes through h.handoverSigner — the same Signer that backs
// the manual POST /deployments/{id}/mint-handover-token endpoint
// (handover_jwt.go). Token claims contract is documented on
// internal/handoverjwt/signer.go (RS256, 5-minute TTL,
// aud=https://console.<sovereignFqdn>). The same private key the
// manual endpoint uses signs the auto-fire token, so Sovereign-side
// /auth/handover (already live on every otech9X provision) accepts it
// without any new key distribution.
//
// Idempotency: the function checks dep.Result.HandoverFiredAt ==
// nil under dep.mu before minting, so a double-fire from a helmwatch
// flake (informer disconnect + reattach + re-emit terminal event)
// does NOT mint a second JWT. The first mint wins; the second call
// returns silently without emitting a duplicate SSE event.
//
// Issue #780 — before minting, fireHandover blocks on the new
// Sovereign's `sovereign-wildcard-tls` Certificate reaching
// Ready=True via waitForWildcardCert. Phase-1 ready means 38/38
// HRs are installed but the cert's DNS-01 challenge is a separate
// downstream watch — it can take 30s-3min to land. Without the
// gate, the handoverURL points at https://console.<fqdn> while
// TLS is still self-signed/issuing, and the operator's first
// click on their new Sovereign hits a browser security warning.
//
// Failure modes:
//   - h.handoverSigner is nil — log + skip. Production catalyst-api
//     always has a wired Signer (cmd/api/main.go LoadOrGenerate's the
//     keypair on first boot); a nil Signer is the test-only or
//     misconfigured-CI path. The wizard falls back to the manual
//     mint-handover-token endpoint that the operator can hit via the
//     existing "Open Sovereign console" button on the AdminPage.
//   - dep.Request.SovereignFQDN empty — log + skip. Same fallback.
//   - h.handoverSigner.MintToken returns an error — log + skip. The
//     UI's status=ready + handoverURL=="" branch renders a manual-
//     mint button so the operator is never silently stranded.
//   - sovereign-wildcard-tls never reaches Ready=True within
//     DefaultHandoverCertWaitTimeout — log + emit a warn event +
//     proceed with the mint. Per issue #780 spec we'd rather emit a
//     handoverURL the operator can retry than leave them stuck with
//     status=ready and no redirect at all.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #10 the JWT itself is NEVER logged
// — only the deployment id + the post-mint expiry timestamp lands in
// structured logs.
func (h *Handler) fireHandover(dep *Deployment) {
	if h.handoverSigner == nil {
		h.log.Warn("handover auto-fire: signer not configured; skipping mint",
			"id", dep.ID,
		)
		return
	}

	dep.mu.Lock()
	if dep.Result == nil || dep.Result.HandoverFiredAt != nil {
		// Already fired (a duplicate markPhase1Done from an informer
		// reattach raced this path) — leave it alone. The original
		// handoverURL + handoverFiredAt remain on the record.
		dep.mu.Unlock()
		return
	}
	fqdn := dep.Request.SovereignFQDN
	depID := dep.ID
	owner := dep.OwnerEmail
	if owner == "" {
		// Fall back to OrgEmail — pre-#689 deployments may have an
		// empty OwnerEmail but still carry a valid OrgEmail (e.g.
		// the wizard's PIN-auth flow stamps both with the same
		// session.email value at CreateDeployment time).
		owner = dep.Request.OrgEmail
	}
	dep.mu.Unlock()

	if strings.TrimSpace(fqdn) == "" {
		h.log.Warn("handover auto-fire: deployment has no SovereignFQDN; skipping",
			"id", depID,
		)
		return
	}
	if strings.TrimSpace(owner) == "" {
		// Sub claim must be non-empty for the Sovereign-side handover
		// handler to bind a session. Surface the misconfiguration
		// loudly — a manual mint via the existing endpoint can recover
		// once the operator re-authenticates.
		h.log.Warn("handover auto-fire: deployment has no owner email; skipping (operator can manual-mint via /mint-handover-token)",
			"id", depID,
		)
		return
	}

	// Issue #780 — block the mint until the new Sovereign's wildcard
	// TLS cert (`sovereign-wildcard-tls` in `kube-system`) reaches
	// Ready=True. Phase-1 ready means 38/38 HRs are installed, but
	// the DNS-01 challenge for the wildcard cert is a separate
	// downstream watch — it can take 30s-3min to land after Phase-1
	// terminates. Without this gate the wizard renders the redirect
	// button at a console URL whose TLS handshake fails for ~90s,
	// making the operator's first contact with their new Sovereign a
	// browser security warning.
	//
	// Bounded timeout (DefaultHandoverCertWaitTimeout, 10m): if the
	// cert never lands, we emit the handoverURL anyway with a warn
	// event. The operator can retry the redirect in their browser
	// once TLS settles. This is the lesser evil vs leaving the
	// deployment stuck with status=ready but no redirect URL.
	h.waitForWildcardCert(dep)

	tokenStr, err := h.handoverSigner.MintToken(fqdn, depID, owner, owner)
	if err != nil {
		h.log.Error("handover auto-fire: MintToken failed",
			"id", depID,
			"fqdn", fqdn,
			"err", err,
		)
		return
	}
	now := time.Now().UTC()
	expiresAt := now.Add(handoverjwt.DefaultTTL)
	handoverURL := "https://console." + fqdn + "/auth/handover?token=" + url.QueryEscape(tokenStr)

	// Persist the URL + timestamp on the deployment record under the
	// lock. Doing this BEFORE the SSE emit guarantees a /deployments/
	// {id} GET that races the SSE event sees the same value the
	// emit carries — no flap window where the typed event arrived
	// but the durable record disagreed.
	dep.mu.Lock()
	if dep.Result != nil {
		dep.Result.HandoverFiredAt = &now
		dep.Result.HandoverURL = handoverURL
	}
	dep.mu.Unlock()
	h.persistDeployment(dep)

	// Mother → child cutover data transfer. POST the full deployment
	// record to the child's catalyst-api so its `/api/v1/deployments/{id}/*`
	// endpoints answer with byte-byte-identical data the operator sees on
	// the mother view. Fire-and-forget: a transient network blip during
	// the POST does not block the JWT mint or SSE emit; mother stays the
	// source of truth (operators can re-fire handover via /mint-handover-token
	// to retry the import). Per docs/INVIOLABLE-PRINCIPLES.md #3 — no
	// silent fallback, the failure is logged loudly so it surfaces in the
	// catalyst-api journal.
	go h.exportDeploymentToChild(dep, fqdn)

	// #3376 — ship the phase0 tofu archive to the child so its
	// ReceiveTofuArchive seals secret/catalyst/tofu-phase0-archive, which
	// un-gates the cutover (425→fires) → Step 09 patches
	// secret/org-services/provisioning-github-token → the org-services/provisioning init
	// container proceeds → the tenant-Org funnel completes. Before this,
	// the archive push lived ONLY in the synchronous FinaliseHandover
	// wizard path; auto-handover never pushed it, so every fresh prov's
	// funnel wedged. Fire-and-forget with its own retry, same as the jobs
	// + deployment exports above.
	go h.exportTofuArchiveToChild(depID, fqdn)

	// Emit the typed SSE event. The Message field IS the data payload
	// (see writeSSEEvent in deployments.go) — a JSON object the
	// wizard parses verbatim. Per #768's contract the payload is
	// `{handoverURL, expiresAt}`.
	payload, _ := json.Marshal(map[string]string{
		"handoverURL": handoverURL,
		"expiresAt":   expiresAt.Format(time.RFC3339),
	})
	h.emitWatchEvent(dep, provisioner.Event{
		Time:    now.Format(time.RFC3339),
		Phase:   PhaseHandoverReady,
		Level:   "info",
		Message: string(payload),
	})

	h.log.Info("handover auto-fire: minted + staged",
		"id", depID,
		"fqdn", fqdn,
		"expiresAt", expiresAt.Format(time.RFC3339),
	)
}

// envOrEmpty — small helper so the tests don't have to set every
// env var the package reads. Returns "" if unset.
func envOrEmpty(key string) string {
	return os.Getenv(key)
}

// waitForKubeconfig polls for the cloud-init kubeconfig postback to
// land on disk. Returns (kubeconfig-bytes, true) when the file
// appears within the timeout, or ("", false) when the timeout
// elapses. While waiting, an info-level component event is emitted
// every poll-interval so the wizard log pane shows progress
// instead of going silent. Issue #538.
//
// Timeout / poll cadence are runtime-configurable per
// docs/INVIOLABLE-PRINCIPLES.md #4 — see kubeconfigArrivalTimeoutEnv
// and kubeconfigArrivalPollIntervalEnv. Tests override via the
// Handler.kubeconfigArrivalTimeout / kubeconfigArrivalPollInterval
// fields so the path is exercised in milliseconds.
func (h *Handler) waitForKubeconfig(dep *Deployment) (string, bool) {
	timeout := h.kubeconfigArrivalTimeout
	if timeout == 0 {
		if v, _ := time.ParseDuration(envOrEmpty(kubeconfigArrivalTimeoutEnv)); v > 0 {
			timeout = v
		} else {
			timeout = DefaultKubeconfigArrivalTimeout
		}
	}
	pollEvery := h.kubeconfigArrivalPollInterval
	if pollEvery == 0 {
		if v, _ := time.ParseDuration(envOrEmpty(kubeconfigArrivalPollIntervalEnv)); v > 0 {
			pollEvery = v
		} else {
			pollEvery = DefaultKubeconfigArrivalPollInterval
		}
	}

	deadline := time.Now().Add(timeout)
	first := true
	for {
		// Snapshot the current kubeconfig path under the lock.
		// PutKubeconfig writes Result.KubeconfigPath under the same
		// lock, so a concurrent PUT is observed atomically.
		dep.mu.Lock()
		path := ""
		if dep.Result != nil {
			path = dep.Result.KubeconfigPath
		}
		dep.mu.Unlock()

		// If the path pointer is set, try to read it. The pointer
		// can be set BEFORE the file is fully fsynced on slow disks
		// — read errors fall through to the next poll tick.
		if path != "" {
			if raw, err := os.ReadFile(path); err == nil && len(raw) > 0 {
				if !first {
					h.emitWatchEvent(dep, provisioner.Event{
						Time:    time.Now().UTC().Format(time.RFC3339),
						Phase:   helmwatch.PhaseComponent,
						Level:   "info",
						Message: fmt.Sprintf("Phase-1 watch: kubeconfig received from cloud-init (%d bytes); starting per-component HelmRelease watch.", len(raw)),
					})
				}
				return string(raw), true
			}
		}

		// First tick is a single "waiting for cloud-init" message
		// so the wizard log pane shows the watch is alive.
		// Subsequent ticks are silent on the event bus (every 15s
		// of "still waiting" log noise would drown the operator's
		// view); we still poll the file system every tick so a
		// PUT lands as soon as it arrives.
		if first {
			h.emitWatchEvent(dep, provisioner.Event{
				Time:    time.Now().UTC().Format(time.RFC3339),
				Phase:   helmwatch.PhaseComponent,
				Level:   "info",
				Message: fmt.Sprintf("Phase-1 watch: waiting for cloud-init to PUT the new Sovereign's kubeconfig (timeout %s, polling every %s).", timeout, pollEvery),
			})
			first = false
		}

		// Check the deadline AFTER an emit so the operator-visible
		// log includes the final timeout reason.
		if time.Now().After(deadline) {
			h.emitWatchEvent(dep, provisioner.Event{
				Time:    time.Now().UTC().Format(time.RFC3339),
				Phase:   helmwatch.PhaseComponent,
				Level:   "warn",
				Message: fmt.Sprintf("Phase-1 watch: timed out after %s waiting for cloud-init kubeconfig postback. The new Sovereign's cloud-init never PUT its kubeconfig to /api/v1/deployments/{id}/kubeconfig — either Phase 0 failed, the LB never routed to the cloud-init endpoint, or cloud-init crashed. Operator can fetch the kubeconfig via SSH (see docs/RUNBOOK-PROVISIONING.md §Fetch kubeconfig via SSH) and re-run the deployment.", timeout),
			})
			return "", false
		}

		time.Sleep(pollEvery)
	}
}

// probeFluxCRDAbsent runs the #5042 bounded Flux-CRD-presence probe.
// It is called AFTER waitForKubeconfig succeeds and BEFORE the
// helmwatch informer is built. It returns true ONLY on a POSITIVE
// "the Flux HelmRelease CRD (helmreleases.helm.toolkit.fluxcd.io) is
// absent AND the apiserver answered" observation that persists to the
// end of the probe budget — the signal that cloud-init's flux-install
// stage never landed on an otherwise-healthy fresh Sovereign. In every
// other case (CRD servable, or the probe never got a clean absent
// answer) it returns false so runPhase1Watch proceeds to the watcher
// exactly as before.
//
// Conservatism (surface-not-gate discipline, mirrors the #3971 storage
// gate and the #4706 console probe): the probe classifies "absent" ONLY
// on a clean NotFound / no-resource-match from a reachable apiserver.
// A dynamic-client build failure, a connection error, a timeout, or any
// other ambiguous/transient error is NEVER treated as a positive absent
// observation — it falls through (returns false) so a flake cannot
// invent a failure. When the CRD really is absent, the dynamic client's
// List against HelmReleaseGVR gets an HTTP 404 the apiserver returns for
// an unserved resource path (apierrors.IsNotFound); a discovery-mapped
// path would surface meta.IsNoMatchError — both count as a positive
// "reachable apiserver says the resource type does not exist".
//
// Budget + poll cadence are runtime-configurable per
// docs/INVIOLABLE-PRINCIPLES.md #4 — see fluxCRDProbeBudgetEnv /
// fluxCRDProbePollIntervalEnv. Tests inject sub-second values via
// Handler.fluxCRDProbeBudget / Handler.fluxCRDProbePollInterval and a
// fake dynamic client (via Handler.dynamicFactory) whose List reactor
// returns the desired error.
func (h *Handler) probeFluxCRDAbsent(dep *Deployment, kubeconfig string) bool {
	budget, pollEvery := h.fluxCRDProbeBudgets()

	// Build the dynamic client the same way the cert-wait path does:
	// the test-only h.dynamicFactory when wired, else the production
	// kubeconfig→dynamic.Interface factory. A build error is NOT a
	// positive absent observation — fall through so NewWatcher (which
	// runs next in runPhase1Watch) surfaces OutcomeWatcherStartFailed
	// with its own diagnostic. Never invent OutcomeFluxCRDsAbsent from a
	// client-construction failure.
	dyn, err := h.buildFluxProbeDynamicClient(kubeconfig)
	if err != nil || dyn == nil {
		h.log.Warn("flux-crd probe: could not build dynamic client; skipping probe (falling through to watcher)",
			"id", dep.ID,
			"err", err,
		)
		return false
	}

	h.emitWatchEvent(dep, provisioner.Event{
		Time:    time.Now().UTC().Format(time.RFC3339),
		Phase:   helmwatch.PhaseComponent,
		Level:   "info",
		Message: fmt.Sprintf("Phase-1 watch: probing for the Flux HelmRelease CRD (helmreleases.helm.toolkit.fluxcd.io) before starting the per-component watch (budget %s, polling every %s). Issue #5042.", budget, pollEvery),
	})

	deadline := time.Now().Add(budget)
	// lastAbsent records whether the MOST RECENT probe was a clean
	// "CRD absent + apiserver reachable" answer. It gates the terminal
	// classification: at deadline we return true only if the last
	// observation was a positive absent — an ambiguous/transient error
	// at deadline leaves lastAbsent false → fall through (fail-open).
	lastAbsent := false
	for {
		getCtx, cancelGet := context.WithTimeout(context.Background(), pollEvery)
		_, listErr := dyn.Resource(helmwatch.HelmReleaseGVR).
			Namespace(helmwatch.FluxNamespace).
			List(getCtx, metav1.ListOptions{Limit: 1})
		cancelGet()

		switch {
		case listErr == nil:
			// The CRD is servable — Flux is installed. Proceed to the
			// watcher exactly as before.
			h.emitWatchEvent(dep, provisioner.Event{
				Time:    time.Now().UTC().Format(time.RFC3339),
				Phase:   helmwatch.PhaseComponent,
				Level:   "info",
				Message: "Phase-1 watch: Flux HelmRelease CRD is present; starting the per-component HelmRelease watch.",
			})
			return false
		case apierrors.IsNotFound(listErr) || meta.IsNoMatchError(listErr):
			// POSITIVE absent observation: the apiserver answered that
			// the resource type does not exist. Keep polling — the
			// flux-install manifest may still land within the budget.
			lastAbsent = true
		default:
			// Ambiguous/transient (connection refused, TLS flap, timeout,
			// forbidden, …). Do NOT let this drive a false absent
			// classification. If the budget elapses while the last
			// observation is ambiguous we fall through (fail-open).
			lastAbsent = false
		}

		if time.Now().After(deadline) {
			if lastAbsent {
				h.emitWatchEvent(dep, provisioner.Event{
					Time:    time.Now().UTC().Format(time.RFC3339),
					Phase:   helmwatch.PhaseComponent,
					Level:   "warn",
					Message: fmt.Sprintf("Phase-1 watch: the Flux HelmRelease CRD (helmreleases.helm.toolkit.fluxcd.io) was still absent after %s — cloud-init's flux-install stage did not land. Classifying flux-crds-absent. Issue #5042.", budget),
				})
				return true
			}
			// Ambiguous at deadline — fail-open: proceed to the watcher
			// so a transient apiserver blip never invents a failure. The
			// watcher's own reachability probe + WatchTimeout path will
			// classify honestly if the cluster is genuinely unreachable.
			h.emitWatchEvent(dep, provisioner.Event{
				Time:    time.Now().UTC().Format(time.RFC3339),
				Phase:   helmwatch.PhaseComponent,
				Level:   "info",
				Message: fmt.Sprintf("Phase-1 watch: Flux HelmRelease CRD probe inconclusive after %s (last observation was a transient error, not a clean absent) — proceeding to the per-component watch. Issue #5042.", budget),
			})
			return false
		}

		time.Sleep(pollEvery)
	}
}

// fluxCRDProbeBudgets resolves the Flux-CRD-presence probe's overall budget +
// poll cadence from the test-only Handler overrides, the
// CATALYST_PHASE1_FLUX_CRD_PROBE_* env knobs, or the production defaults, in
// that precedence. Shared by the PRIMARY-region probe (probeFluxCRDAbsent,
// #5042) and the per-SECONDARY-region probe (probeFluxCRDAbsentForRegion,
// #5012) so both honour the exact same runtime knobs (Inviolable Principle #4)
// — a secondary never invents its own budget.
func (h *Handler) fluxCRDProbeBudgets() (budget, pollEvery time.Duration) {
	budget = h.fluxCRDProbeBudget
	if budget == 0 {
		if v, _ := time.ParseDuration(envOrEmpty(fluxCRDProbeBudgetEnv)); v > 0 {
			budget = v
		} else {
			budget = DefaultFluxCRDProbeBudget
		}
	}
	pollEvery = h.fluxCRDProbePollInterval
	if pollEvery == 0 {
		if v, _ := time.ParseDuration(envOrEmpty(fluxCRDProbePollIntervalEnv)); v > 0 {
			pollEvery = v
		} else {
			pollEvery = DefaultFluxCRDProbePollInterval
		}
	}
	return budget, pollEvery
}

// buildFluxProbeDynamicClient builds the dynamic.Interface both Flux-CRD probes
// List through: the test-only h.dynamicFactory when wired, else the production
// kubeconfig→dynamic.Interface factory. Extracted so the primary (#5042) and
// per-secondary-region (#5012) probes construct their client identically.
func (h *Handler) buildFluxProbeDynamicClient(kubeconfig string) (dynamic.Interface, error) {
	if h.dynamicFactory != nil {
		return h.dynamicFactory(kubeconfig)
	}
	return helmwatch.NewDynamicClientFromKubeconfig(kubeconfig)
}

// probeFluxCRDAbsentForRegion is the per-SECONDARY-region mirror of the
// primary's #5042 probeFluxCRDAbsent (#5012). spawnSecondaryRegionWatchers runs
// it against each secondary control-plane's kubeconfig BEFORE building that
// region's helmwatch informer. It returns true ONLY on a POSITIVE "the Flux
// HelmRelease CRD (helmreleases.helm.toolkit.fluxcd.io) is absent AND the
// apiserver answered" observation that persists to the end of the probe budget
// — the signal that this secondary's cloud-init flux-install stage never
// landed even though its cluster is healthy (its kubeconfig was PUT). In every
// other case (CRD servable, transient/ambiguous error, client-build failure,
// or ctx cancelled) it returns false so the caller proceeds to build the
// watcher exactly as before.
//
// CRUCIAL difference from the primary: a positive verdict here NEVER fails or
// hangs the whole deployment (surface-not-gate discipline — a slow/broken
// secondary must never gate the prov, mirroring #3611/#4486). The caller turns
// a true verdict into a LOUD, region-tagged, NAMED signal via
// recordSecondaryFluxCRDAbsent and skips the doomed watcher — it does NOT call
// markPhase1Done.
//
// Conservatism is identical to the primary probe: only a clean NotFound /
// no-resource-match from a reachable apiserver counts as absent; a client-build
// failure, connection error, timeout, or ctx cancellation is NEVER a positive
// absent observation. Budget + cadence reuse the same
// CATALYST_PHASE1_FLUX_CRD_PROBE_* knobs via fluxCRDProbeBudgets(). The passed
// ctx is the region watcher's context, so stopSecondaries aborts an in-flight
// probe promptly (fail-open on cancellation).
func (h *Handler) probeFluxCRDAbsentForRegion(ctx context.Context, dep *Deployment, region, kubeconfig string) bool {
	budget, pollEvery := h.fluxCRDProbeBudgets()

	dyn, err := h.buildFluxProbeDynamicClient(kubeconfig)
	if err != nil || dyn == nil {
		// A client-build failure is not a positive absent observation — fall
		// through so the caller's NewWatcher surfaces its own error path.
		h.log.Warn("secondary flux-crd probe: could not build dynamic client; skipping probe (falling through to watcher)",
			"id", dep.ID, "region", region, "err", err)
		return false
	}

	h.emitWatchEvent(dep, provisioner.Event{
		Time:    time.Now().UTC().Format(time.RFC3339),
		Phase:   helmwatch.PhaseComponent,
		Level:   "info",
		Message: fmt.Sprintf("Phase-1 watch [region %s]: probing for the Flux HelmRelease CRD (helmreleases.helm.toolkit.fluxcd.io) before starting the secondary per-component watch (budget %s, polling every %s). Issue #5012.", region, budget, pollEvery),
	})

	deadline := time.Now().Add(budget)
	// lastAbsent records whether the MOST RECENT probe was a clean "CRD absent
	// + apiserver reachable" answer — same gate as the primary probe.
	lastAbsent := false
	for {
		getCtx, cancelGet := context.WithTimeout(ctx, pollEvery)
		_, listErr := dyn.Resource(helmwatch.HelmReleaseGVR).
			Namespace(helmwatch.FluxNamespace).
			List(getCtx, metav1.ListOptions{Limit: 1})
		cancelGet()

		switch {
		case listErr == nil:
			// CRD servable — Flux is installed in this region. Proceed to the
			// secondary watcher exactly as before.
			h.emitWatchEvent(dep, provisioner.Event{
				Time:    time.Now().UTC().Format(time.RFC3339),
				Phase:   helmwatch.PhaseComponent,
				Level:   "info",
				Message: fmt.Sprintf("Phase-1 watch [region %s]: Flux HelmRelease CRD is present; starting the secondary per-component watch.", region),
			})
			return false
		case apierrors.IsNotFound(listErr) || meta.IsNoMatchError(listErr):
			// POSITIVE absent observation. Keep polling — the flux-install
			// manifest may still land within the budget.
			lastAbsent = true
		default:
			// Ambiguous/transient (connection refused, TLS flap, timeout,
			// forbidden, per-poll ctx deadline). Never drive a false absent.
			lastAbsent = false
		}

		if time.Now().After(deadline) {
			if lastAbsent {
				// The caller (recordSecondaryFluxCRDAbsent) emits the LOUD warn
				// + records the NAMED degraded signal.
				return true
			}
			// Ambiguous at deadline — fail-open: proceed to the watcher so a
			// transient blip never invents a degraded secondary.
			h.emitWatchEvent(dep, provisioner.Event{
				Time:    time.Now().UTC().Format(time.RFC3339),
				Phase:   helmwatch.PhaseComponent,
				Level:   "info",
				Message: fmt.Sprintf("Phase-1 watch [region %s]: Flux HelmRelease CRD probe inconclusive after %s (last observation was a transient error, not a clean absent) — proceeding to the secondary per-component watch. Issue #5012.", region, budget),
			})
			return false
		}

		// Respect the region watcher's context so stopSecondaries aborts an
		// in-flight probe promptly (fail-open on cancellation — a cancelled
		// probe never invents an absent verdict).
		select {
		case <-ctx.Done():
			return false
		case <-time.After(pollEvery):
		}
	}
}

// recordSecondaryFluxCRDAbsent turns a positive per-secondary-region
// flux-CRD-absent verdict (#5012) into a NAMED, greppable, NON-FATAL signal:
//
//   - a LOUD region-tagged WARN event on the wizard log pane + a structured
//     WARN log (both greppable), naming cloud-init's flux-install stage as the
//     root cause for THAT region — the secondary mirror of the primary's #5042
//     OutcomeFluxCRDsAbsent diagnostic;
//   - the region appended (deduped) to Result.SecondaryFluxCRDAbsentRegions so
//     the condition is durable on the deployment record;
//   - a nil watcher slot registered in dep.secondaryWatchers[region] so the
//     EXISTING #3611 census (snapshotSecondaryStatesLocked emits a 0/0 map for
//     a nil slot → regionDegraded against a populated primary) surfaces the
//     region as a degraded secondary and lights SecondaryDegraded — WITHOUT any
//     change to markPhase1Done's census/classification logic.
//
// It NEVER touches dep.Status and NEVER calls markPhase1Done: a secondary whose
// flux never installed must not fail or hang the whole prov. The caller skips
// spinning a doomed no-CRD watcher for the region.
func (h *Handler) recordSecondaryFluxCRDAbsent(dep *Deployment, region string) {
	dep.mu.Lock()
	if dep.Result != nil {
		already := false
		for _, r := range dep.Result.SecondaryFluxCRDAbsentRegions {
			if r == region {
				already = true
				break
			}
		}
		if !already {
			dep.Result.SecondaryFluxCRDAbsentRegions = append(dep.Result.SecondaryFluxCRDAbsentRegions, region)
		}
	}
	// Register a nil watcher slot so the #3611 census surfaces this region as a
	// 0/0 degraded secondary via snapshotSecondaryStatesLocked's existing
	// nil-slot handling. Only claim the slot when no real watcher is present.
	if dep.secondaryWatchers == nil {
		dep.secondaryWatchers = make(map[string]*helmwatch.Watcher)
	}
	if _, exists := dep.secondaryWatchers[region]; !exists {
		dep.secondaryWatchers[region] = nil
	}
	dep.mu.Unlock()

	h.emitWatchEvent(dep, provisioner.Event{
		Time:    time.Now().UTC().Format(time.RFC3339),
		Phase:   helmwatch.PhaseComponent,
		Level:   "warn",
		Message: fmt.Sprintf("Phase-1 watch [region %s]: the Flux HelmRelease CRD (helmreleases.helm.toolkit.fluxcd.io) was still ABSENT after the probe budget — this SECONDARY region's cloud-init flux-install stage did not land. Recording it DEGRADED (secondary-flux-crds-absent) and skipping its doomed no-CRD watcher; the PRIMARY region is unaffected and the deployment is NOT failed on account of this secondary (surface-not-gate). Operator: SSH region %s's control-plane and inspect /var/log/cloud-init-output.log for the flux-install step. Refs #5012 #5042.", region, region),
	})
	h.log.Warn("secondary region flux HelmRelease CRD absent after probe budget — recording degraded, skipping doomed watcher (non-fatal, surface-not-gate)",
		"id", dep.ID, "region", region)
}

// waitFluxCRDServableForRegion keeps polling a SECONDARY region's apiserver for
// the Flux HelmRelease CRD AFTER probeFluxCRDAbsentForRegion classified it
// absent-at-budget (#5012, hw255 recurrence). Under the #3129 PUT-early
// invariant a healthy secondary legitimately installs flux minutes after its
// kubeconfig lands on the mothership (gateway-api CRDs + helm download + the
// full cilium DaemonSet rollout run in between), so an absent-at-budget verdict
// must not permanently freeze the region's census at 0/0. Bounded only by ctx
// (the region watcher's context — phase-1 lifetime): returns true the moment a
// List against HelmReleaseGVR succeeds (flux landed; caller clears the degraded
// record and spins the real watcher), false when ctx is cancelled first (the
// recorded degraded signal stands). Any error — absent or transient — just
// keeps polling; this waiter never invents a verdict of its own.
func (h *Handler) waitFluxCRDServableForRegion(ctx context.Context, dep *Deployment, region, kubeconfig string) bool {
	_, pollEvery := h.fluxCRDProbeBudgets()

	dyn, err := h.buildFluxProbeDynamicClient(kubeconfig)
	if err != nil || dyn == nil {
		h.log.Warn("secondary flux-crd late-landing wait: could not build dynamic client; leaving region recorded degraded",
			"id", dep.ID, "region", region, "err", err)
		return false
	}

	for {
		getCtx, cancelGet := context.WithTimeout(ctx, pollEvery)
		_, listErr := dyn.Resource(helmwatch.HelmReleaseGVR).
			Namespace(helmwatch.FluxNamespace).
			List(getCtx, metav1.ListOptions{Limit: 1})
		cancelGet()
		if listErr == nil {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(pollEvery):
		}
	}
}

// clearSecondaryFluxCRDAbsent reverses recordSecondaryFluxCRDAbsent for a
// SECONDARY region whose Flux CRD landed after the probe budget (#5012, hw255
// recurrence): removes the region from Result.SecondaryFluxCRDAbsentRegions,
// releases the nil census slot (the caller registers the REAL watcher next, so
// the #3611 census reports true HR counts instead of a frozen 0/0), and emits
// an info event so the wizard log shows the recovery next to the earlier warn.
func (h *Handler) clearSecondaryFluxCRDAbsent(dep *Deployment, region string) {
	dep.mu.Lock()
	if dep.Result != nil && len(dep.Result.SecondaryFluxCRDAbsentRegions) > 0 {
		kept := dep.Result.SecondaryFluxCRDAbsentRegions[:0]
		for _, r := range dep.Result.SecondaryFluxCRDAbsentRegions {
			if r != region {
				kept = append(kept, r)
			}
		}
		if len(kept) == 0 {
			dep.Result.SecondaryFluxCRDAbsentRegions = nil
		} else {
			dep.Result.SecondaryFluxCRDAbsentRegions = kept
		}
	}
	// Only release the slot recordSecondaryFluxCRDAbsent registered (nil); a
	// real watcher registered concurrently must never be dropped.
	if w, ok := dep.secondaryWatchers[region]; ok && w == nil {
		delete(dep.secondaryWatchers, region)
	}
	dep.mu.Unlock()

	h.emitWatchEvent(dep, provisioner.Event{
		Time:    time.Now().UTC().Format(time.RFC3339),
		Phase:   helmwatch.PhaseComponent,
		Level:   "info",
		Message: fmt.Sprintf("Phase-1 watch [region %s]: the Flux HelmRelease CRD landed AFTER the probe budget — this secondary's flux-install was slow, not missing. Clearing the degraded (secondary-flux-crds-absent) record and starting its per-component watch. Refs #5012.", region),
	})
	h.log.Info("secondary region flux HelmRelease CRD landed after probe budget — clearing degraded record, starting watcher",
		"id", dep.ID, "region", region)
}

// certificateGVR — GroupVersionResource for cert-manager.io/v1.Certificate.
// Pulled out as a package-level var so tests can override the GVR if a
// future cert-manager release bumps the API version. Not exported —
// the only consumer today is waitForWildcardCert.
var certificateGVR = schema.GroupVersionResource{
	Group:    "cert-manager.io",
	Version:  "v1",
	Resource: "certificates",
}

// waitForWildcardCert polls the new Sovereign's apiserver for the
// `sovereign-wildcard-tls` Certificate's status.conditions[type=Ready]
// = True before the handover auto-fire mints the JWT. Returns when
// the cert is Ready OR when the timeout elapses (whichever first).
//
// The function NEVER blocks the handover indefinitely — the timeout
// is bounded (DefaultHandoverCertWaitTimeout = 10 minutes by default)
// and on timeout we log + emit a warn event but proceed with the
// mint. Per issue #780 spec: "If cert doesn't land in 5 min, log +
// emit handoverURL anyway (operator can retry)".
//
// Graceful degradation when the cert can't be queried:
//
//   - dep.Result.KubeconfigPath empty / unreadable → skip the wait.
//     Sovereign-side / test paths that don't drive a real Sovereign
//     cluster fall through here. The mint proceeds immediately.
//   - dynamic client construction fails → log + skip. Same fallback.
//   - cert not found (404 / NotFound) → keep polling. The cert
//     resource may not have been applied yet — bp-catalyst-platform's
//     templates land it once the chart is installed but we may
//     observe Phase-1 Ready a few seconds before the apply completes.
//   - apiserver transient error → keep polling. Single-shot blips
//     (informer disconnect mid-poll) are recovered by the next tick.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 timeout + poll cadence are
// runtime-configurable via CATALYST_HANDOVER_CERT_WAIT_TIMEOUT and
// CATALYST_HANDOVER_CERT_POLL_INTERVAL. Tests inject sub-second
// values via Handler.handoverCertWaitTimeout +
// Handler.handoverCertPollInterval so the wait path is exercised
// deterministically.
func (h *Handler) waitForWildcardCert(dep *Deployment) {
	timeout := h.handoverCertWaitTimeout
	if timeout == 0 {
		if v, _ := time.ParseDuration(envOrEmpty(handoverCertWaitTimeoutEnv)); v > 0 {
			timeout = v
		} else {
			timeout = DefaultHandoverCertWaitTimeout
		}
	}
	pollEvery := h.handoverCertPollInterval
	if pollEvery == 0 {
		if v, _ := time.ParseDuration(envOrEmpty(handoverCertPollIntervalEnv)); v > 0 {
			pollEvery = v
		} else {
			pollEvery = DefaultHandoverCertPollInterval
		}
	}

	dyn, err := h.sovereignDynamicClientForCertWait(dep)
	if err != nil || dyn == nil {
		// No kubeconfig / no client — fall through. The legacy
		// behaviour (mint immediately) is preserved for Sovereign-side
		// callers and the test suite that injects a Handler with no
		// dynamicFactory wired. Issue #780 only requires the gate when
		// we CAN observe the cert.
		h.log.Info("handover cert-wait: skipping (no Sovereign dynamic client available; mint proceeds)",
			"id", dep.ID,
			"err", err,
		)
		return
	}

	h.emitWatchEvent(dep, provisioner.Event{
		Time:    time.Now().UTC().Format(time.RFC3339),
		Phase:   helmwatch.PhaseComponent,
		Level:   "info",
		Message: fmt.Sprintf("Handover gate: waiting for sovereign-wildcard-tls Certificate Ready=True before emitting handoverURL (timeout %s, polling every %s). Issue #780.", timeout, pollEvery),
	})

	deadline := time.Now().Add(timeout)
	// Use a bounded context for the per-poll Get only — NOT for the
	// outer wait loop. We want the timeout-on-the-loop to be governed
	// by the deadline check below so we ALWAYS get a chance to emit
	// the timeout warn event (a ctx.Done() unblock would skip the
	// emit and the operator-visible reason would never reach the
	// wizard log pane).

	for {
		getCtx, cancelGet := context.WithTimeout(context.Background(), pollEvery)
		ready, observed, certErr := wildcardCertReady(getCtx, dyn)
		cancelGet()
		if certErr == nil && ready {
			h.emitWatchEvent(dep, provisioner.Event{
				Time:    time.Now().UTC().Format(time.RFC3339),
				Phase:   helmwatch.PhaseComponent,
				Level:   "info",
				Message: "Handover gate: sovereign-wildcard-tls Certificate Ready=True. Emitting handoverURL.",
			})
			h.log.Info("handover cert-wait: cert reached Ready=True; proceeding to mint",
				"id", dep.ID,
			)
			return
		}

		if time.Now().After(deadline) {
			// Timeout — emit a warn event and let the mint proceed.
			// Per issue #780 we'd rather emit a handoverURL the
			// operator can retry than leave them stuck with status=
			// ready and no redirect at all.
			h.emitWatchEvent(dep, provisioner.Event{
				Time:    time.Now().UTC().Format(time.RFC3339),
				Phase:   helmwatch.PhaseComponent,
				Level:   "warn",
				Message: fmt.Sprintf("Handover gate: timed out after %s waiting for sovereign-wildcard-tls Ready=True (last observed status=%q, err=%v). Emitting handoverURL anyway — TLS may need a few seconds to settle in the operator's browser. Issue #780.", timeout, observed, certErr),
			})
			h.log.Warn("handover cert-wait: timeout; minting anyway",
				"id", dep.ID,
				"timeout", timeout,
				"observedStatus", observed,
				"err", certErr,
			)
			return
		}

		time.Sleep(pollEvery)
	}
}

// sovereignDynamicClientForCertWait — narrow dynamic-client builder
// the cert-wait path uses. Returns (nil, nil) when the deployment
// has no kubeconfig path set (test fixtures, Sovereign-side paths)
// so the caller can detect "skip the wait" without log noise. Any
// real error (kubeconfig present but unreadable, factory returns
// an error) surfaces as (nil, err).
func (h *Handler) sovereignDynamicClientForCertWait(dep *Deployment) (dynamic.Interface, error) {
	dep.mu.Lock()
	kubeconfigPath := ""
	if dep.Result != nil {
		kubeconfigPath = dep.Result.KubeconfigPath
	}
	dep.mu.Unlock()
	if kubeconfigPath == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("read kubeconfig: %w", err)
	}
	if h.dynamicFactory != nil {
		return h.dynamicFactory(string(raw))
	}
	return helmwatch.NewDynamicClientFromKubeconfig(string(raw))
}

// wildcardCertReady inspects the `sovereign-wildcard-tls` Certificate
// in `kube-system` and returns (ready, observedStatus, err). `ready`
// is true iff status.conditions has an entry with type=Ready,
// status=True. `observedStatus` is the raw Ready condition status
// string (or "<not-found>" / "<no-conditions>" / "<missing-ready>")
// for telemetry.
func wildcardCertReady(ctx context.Context, dyn dynamic.Interface) (bool, string, error) {
	u, err := dyn.Resource(certificateGVR).
		Namespace(sovereignWildcardCertNamespace).
		Get(ctx, sovereignWildcardCertName, metav1.GetOptions{})
	if err == nil {
		return certificateReady(u)
	}
	// PR N (2026-05-17 t143 LE rate-limit incident): when the canonical
	// `sovereign-wildcard-tls` cert is unavailable (404 / 429 LE rate
	// limit on the parent domain / DNS01 propagation lag), fall back to
	// ANY per-FQDN sibling cert matching `sovereign-wildcard-tls-*`
	// that's already Ready=True. The chart renders both names in
	// multi-zone configurations (sovereign-wildcard-tls per-zone +
	// sovereign-wildcard-tls-<fqdn> per-FQDN); either reaching Ready
	// proves the operator's console.<fqdn> TLS handshake will succeed.
	// Without this fallback, handover waits the full 10-min budget
	// before firing degraded — operator browser can't reach the new
	// Sovereign for that whole window.
	list, listErr := dyn.Resource(certificateGVR).
		Namespace(sovereignWildcardCertNamespace).
		List(ctx, metav1.ListOptions{})
	if listErr == nil && list != nil {
		for i := range list.Items {
			item := &list.Items[i]
			name := item.GetName()
			if !strings.HasPrefix(name, sovereignWildcardCertName+"-") {
				continue
			}
			ok, _, _ := certificateReady(item)
			if ok {
				return true, "True (via fallback " + name + ")", nil
			}
		}
	}
	return false, "<not-found>", err
}

// certificateReady — returns (ready, observedStatus, nil) for a
// cert-manager.io/v1.Certificate's status.conditions[type=Ready]
// entry. Mirrors helmReleaseReady's Ready-True scan but on the
// Certificate shape. Pulled out so the wait helper + a future
// cutover-time check can share one parser.
func certificateReady(u *unstructured.Unstructured) (bool, string, error) {
	conds, ok, err := unstructured.NestedSlice(u.Object, "status", "conditions")
	if err != nil {
		return false, "<status-parse-error>", err
	}
	if !ok || len(conds) == 0 {
		return false, "<no-conditions>", nil
	}
	for _, c := range conds {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if m["type"] == "Ready" {
			status, _ := m["status"].(string)
			return status == "True", status, nil
		}
	}
	return false, "<missing-ready>", nil
}

// probeReachableForTest runs defaultConsoleReachable's real status-bar
// decision (consoleProbeAttempt: multi-path + redirect-following + < 400
// final status, #4706/#5253) against an explicit base URL (no console.<fqdn>
// construction, single pass) so the contract is unit-testable without DNS or
// the probe budget. Kept in non-test code because httptest servers need the
// same *http.Client TLS-skip.
func probeReachableForTest(target string) error {
	return consoleProbeAttempt(context.Background(), newConsoleProbeClient(), strings.TrimSuffix(target, "/"))
}
