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
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

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

	cfg := helmwatch.Config{
		KubeconfigYAML:            kubeconfig,
		WatchTimeout:              timeout,
		MinBootstrapKitHRs:        minHRs,
		FirstSeenTimeout:          firstSeen,
		LatePollTimeout:           latePollTimeout,
		LatePollInterval:          latePollInterval,
		ReachabilityOverallBudget: reachabilityBudget,
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
		raw, err := os.ReadFile(kcPath)
		if err != nil {
			mu.Unlock()
			h.log.Warn("secondary kubeconfig read failed",
				"id", dep.ID, "region", region, "path", kcPath, "err", err)
			return
		}
		cfg := h.phase1WatchConfigForDeployment(dep, string(raw))
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
			mu.Unlock()
			h.log.Error("secondary helmwatch.NewWatcher failed",
				"id", dep.ID, "region", region, "err", err)
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
		wctx, wcancel := context.WithCancel(ctx)
		stopWatchers[region] = wcancel
		dep.mu.Lock()
		if dep.secondaryWatchers == nil {
			dep.secondaryWatchers = make(map[string]*helmwatch.Watcher)
		}
		dep.secondaryWatchers[region] = watcher
		dep.mu.Unlock()
		mu.Unlock()

		wg.Add(1)
		go func() {
			defer wg.Done()
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
func (h *Handler) markPhase1Done(dep *Deployment, finalStates map[string]string, outcome string) {
	now := time.Now().UTC()

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

	finalStatus := dep.Status
	dep.mu.Unlock()

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
	)

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
		go h.runAutoEstablishClusterMesh(dep)
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
		go h.runPostHandoverPolicyEnforceFlip(dep)
	}
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
// (fully meshed) stop with a final success event. Otherwise sleep an
// exponential backoff (clusterMeshRetryInitialBackoff doubling to
// clusterMeshRetryMaxBackoff) and re-run, bounded by the
// clusterMeshRetryBudget total and by the deployment staying
// status=ready (a wipe mid-loop stops the retries — the kubeconfigs
// are gone or going). Each retry emits a clustermesh-progress event
// (attempt N, fullyMeshed X/Y) so the operator watches convergence,
// not silence.
func (h *Handler) runAutoEstablishClusterMesh(dep *Deployment) {
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
		statuses, err := h.AutoEstablishClusterMesh(attemptCtx, dep)
		cancelAttempt()

		fullyMeshed := countFullyMeshedRegions(statuses)
		if err == nil && len(statuses) >= 2 && fullyMeshed == len(statuses) {
			h.emitClusterMeshProgress(dep, "info",
				fmt.Sprintf("ClusterMesh reconcile: fully meshed (%d/%d regions) on attempt %d — reconcile loop complete", fullyMeshed, len(statuses), attempt))
			h.log.Info("clustermesh: reconcile loop converged",
				"id", dep.ID,
				"attempt", attempt,
				"regions", len(statuses),
			)
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
		h.emitClusterMeshProgress(dep, "warn",
			fmt.Sprintf("ClusterMesh reconcile: attempt %d ended with %d/%d regions fully meshed — retrying in %s", attempt, fullyMeshed, total, backoff))
		if sleepErr := sleepCtx(ctx, backoff); sleepErr != nil {
			h.emitClusterMeshProgress(dep, "warn",
				fmt.Sprintf("ClusterMesh reconcile: retry budget exhausted after %d attempt(s) with %d/%d regions fully meshed — next trigger is a catalyst-api restart or the post-handover finalisation", attempt, fullyMeshed, total))
			h.log.Warn("clustermesh: reconcile loop retry budget exhausted",
				"id", dep.ID,
				"attempts", attempt,
				"fullyMeshed", fullyMeshed,
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
