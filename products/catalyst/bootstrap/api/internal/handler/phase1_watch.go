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
	"strings"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handoverjwt"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// phase1WatchTimeoutEnv — env var override for the watch budget. The
// default DefaultWatchTimeout (60 minutes) is sized for bp-catalyst-
// platform's worst-observed install on the omantel.omani.works DoD run.
// Tests inject a much shorter value via Handler.phase1WatchTimeout.
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

	cfg := helmwatch.Config{
		KubeconfigYAML:     kubeconfig,
		WatchTimeout:       timeout,
		MinBootstrapKitHRs: minHRs,
		FirstSeenTimeout:   firstSeen,
		LatePollTimeout:    latePollTimeout,
		LatePollInterval:   latePollInterval,
	}
	if h.dynamicFactory != nil {
		cfg.DynamicFactory = h.dynamicFactory
	}
	if h.coreFactory != nil {
		cfg.CoreFactory = h.coreFactory
	}
	if h.phase1WatchResync > 0 {
		cfg.Resync = h.phase1WatchResync
	}
	return cfg
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
	default:
		dep.Status = "ready"
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
