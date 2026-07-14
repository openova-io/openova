// Package helmwatch implements the Phase-1 HelmRelease watch loop the
// Sovereign Admin shell consumes for per-component install state and
// per-component logs.
//
// Architecture (per docs/INVIOLABLE-PRINCIPLES.md #3):
//
//   - Phase 0 — OpenTofu provisions Hetzner cloud resources, the
//     control plane boots k3s, cloud-init writes a Flux Kustomization
//     against this monorepo's clusters/<sovereign-fqdn>/. catalyst-api
//     emits SSE events for tofu-init / tofu-plan / tofu-apply /
//     tofu-output / flux-bootstrap.
//
//   - Phase 1 — Flux on the new Sovereign reconciles the bootstrap-kit
//     Kustomization, which materialises 11 HelmReleases (bp-cilium,
//     bp-cert-manager, bp-flux, bp-crossplane, bp-sealed-secrets,
//     bp-spire, bp-nats-jetstream, bp-openbao, bp-keycloak, bp-gitea,
//     bp-catalyst-platform) in flux-system. helm-controller installs
//     each in dependency order. THIS package observes those HelmReleases
//     via a client-go dynamic informer and emits per-component events
//     into the same SSE stream Phase 0 used.
//
// What this package does NOT do (and must not, per principle #3):
//
//   - Apply, mutate, or delete any HelmRelease — it is read-only.
//   - Exec helm or kubectl — it uses client-go.
//   - Hot-poll — it uses an informer's cache + Watch (dynamicinformer).
//   - Produce SSE bytes itself — it returns Events via a callback. The
//     handler package wires them into the durable eventsBuf + SSE
//     channel using the same emit path as Phase-0 events.
//
// Lifecycle:
//
//   - Watch runs from after `flux-bootstrap` until ALL bp-* HelmReleases
//     have reached a terminal state (installed | failed) OR the timeout
//     elapses (60 minutes default, override via
//     CATALYST_PHASE1_WATCH_TIMEOUT).
//   - On Pod restart, the deployment's persisted Result.KubeconfigPath
//     (file pointer; the plaintext kubeconfig lives at the path on
//     the catalyst-api PVC at mode 0600 — issue #183, Option D) +
//     Result.ComponentStates are rehydrated and the watch resumes
//     from the cluster's current observed state (idempotent —
//     emitting an "installed" event for an already-installed
//     release is harmless; the SSE consumer keys off the State
//     enum, not event count).
package helmwatch

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// HelmReleaseGVR is the v2 helm-controller GVR. flux v2.4 ships
// helm.toolkit.fluxcd.io/v2 as the stable HelmRelease API. Catalyst-Zero
// pins flux v2.4.0 in cloudinit-control-plane.tftpl, so this is the
// only GVR the watch needs to know about.
//
// The bootstrap-kit YAMLs in clusters/_template/bootstrap-kit/*.yaml
// already use apiVersion: helm.toolkit.fluxcd.io/v2 (verified
// 2026-04-29). If a future Flux upgrade introduces v3, update both
// here AND the cloud-init pinned Flux release SHA in lockstep.
var HelmReleaseGVR = schema.GroupVersionResource{
	Group:    "helm.toolkit.fluxcd.io",
	Version:  "v2",
	Resource: "helmreleases",
}

// FluxNamespace — bootstrap-kit's HelmReleases all live here. The
// chart bp-catalyst-platform reconciles into other namespaces but the
// HelmRelease object itself stays in flux-system.
const FluxNamespace = "flux-system"

// HelmControllerSelector — selects helm-controller pods for log tailing.
// flux-controllers ship with the canonical app=helm-controller label.
const HelmControllerSelector = "app=helm-controller"

// Default watch timeout — bp-catalyst-platform has the longest install
// because it depends on Crossplane CRDs settling. The watch budget
// must comfortably contain the inner HR install timeout × retries.
//
// F8 fix (2026-05-12, prov #44 RCA): bumped 60m → 120m. The companion
// bp-catalyst-platform HR install/upgrade timeout was bumped 15m → 30m
// (see clusters/_template/bootstrap-kit/13-bp-catalyst-platform.yaml).
// prov #44 (d9399223c3caa4f9) hit the prior 60m phase1 cap with the
// umbrella HR still mid-retry (failures=3) and 41/45 HRs True. The
// outer watch budget MUST be larger than the inner HR's worst-case
// retry chain (30m × 3 = 90m) so the watch never terminates while
// helm-controller still has remediation attempts left.
//
// Runtime override remains via CATALYST_PHASE1_WATCH_TIMEOUT — wired
// into products/catalyst/chart/templates/api-deployment.yaml so the
// operator can dial it down for capacity-bounded environments.
const DefaultWatchTimeout = 120 * time.Minute

// MinComponentCount — historical minimum bootstrap-kit cardinality.
// The Watch terminates when the informer's initial List has synced
// AND every OBSERVED HelmRelease has reached a terminal state, not
// when a specific count has been reached. Kept here as documentation
// of the historical value; new code should rely on the informer
// HasSynced gate (see allObservedTerminal).
const MinComponentCount = 1

// DefaultMinBootstrapKitHRs — defensive floor on the count of bp-*
// HelmReleases that must have appeared in the informer cache before
// the terminate-on-all-done check fires. The load-bearing gate is
// the informer's HasSynced signal (set after WaitForCacheSync
// returns) — this count is a defence-in-depth guard against the
// edge case where HasSynced returns true with a completely empty
// cache (the bootstrap-kit Kustomization on the new cluster never
// reconciled at all).
//
// Default 1 — the watch fires terminate-on-all-done if at least one
// bp-* HelmRelease has been observed AND every observed HR is
// terminal AND the informer has synced. A bootstrap-kit ships at
// least one HR (cilium) so this is a safe floor. Earlier defaults
// (11, then 38) tied this to the kit cardinality; that coupling is
// brittle (otech48 incident, 2026-05-03 — kit had 37 HRs but env
// was 38, so the gate was unsatisfiable). The HasSynced signal is
// the right correctness gate; this constant stays as a sanity floor
// only.
//
// Operator override: CATALYST_PHASE1_MIN_BOOTSTRAP_KIT_HRS. Tests
// inject a higher value (e.g. 3) when they specifically want to
// exercise the floor.
const DefaultMinBootstrapKitHRs = 1

// DefaultReadySentinelComponent — the component id (bp- prefix stripped)
// that MUST have been observed AND reached a terminal state before the
// terminate-on-all-done gate may fire OutcomeReady. catalyst-platform is
// the operator-facing console's OWN server and the terminal node of the
// bootstrap-kit dependency chain (it dependsOn gitea → keycloak → openbao
// → …), so its arrival-and-terminality is the earliest HONEST signal that
// the whole platform — the console backend included — has converged.
//
// #4746 root cause: the count floor above is 1 (deliberately, to stay
// drift-proof against kit cardinality — see otech48 in allObservedTerminal).
// But "≥1 observed AND all-observed-terminal" fires the instant the
// informer's initial List catches a NARROW early set (e.g. only bp-cilium)
// all Ready=True — long before bp-catalyst-platform has even been created
// by Flux. On a slow 2-region prov that premature OutcomeReady then ran the
// #4706 external console-reachability probe against a console whose backend
// had not begun installing, so the probe could only fail: a healthy,
// still-converging Sovereign was stamped status=failed (hw221/hw222). The
// sentinel closes the census so OutcomeReady cannot precede the console
// backend, instead of merely widening the downstream probe budget.
//
// A terminal-FAILED sentinel does NOT block the gate — an all-terminal
// snapshot with a failed component classifies OutcomeFailed downstream, so
// a genuinely broken console still surfaces a failure rather than hanging
// to WatchTimeout. If the sentinel never appears at all, the watch simply
// runs to WatchTimeout → OutcomeTimeout (failed) — the correct verdict for
// a prov whose console backend never installed.
//
// Empty string DISABLES the sentinel gate. applyDefaults leaves it empty so
// existing helmwatch fixtures (which seed kits without catalyst-platform)
// keep exercising the historical path; the PRODUCTION path wires this
// constant via the handler's phase1WatchConfigForDeployment, and operators
// override it with CATALYST_PHASE1_READY_SENTINEL (Inviolable Principle #4).
const DefaultReadySentinelComponent = "catalyst-platform"

// DefaultFirstSeenTimeout — how long the Watcher waits for the FIRST
// bp-* HelmRelease to appear in the informer cache after the watch
// starts. If this elapses with zero HRs observed, the watch emits a
// single warn event pointing the operator at `flux get kustomization
// -n flux-system` (the bootstrap-kit Kustomization isn't reconciling)
// and CONTINUES watching — late HRs are still honoured.
//
// 15 minutes is sized off omantel-class observed latency: a healthy
// flux-bootstrap → first bp-* HelmRelease materialisation completes
// in well under 5 minutes; 15 leaves headroom for slow image pulls
// + a flux-system reconcile interval.
//
// Operator override: CATALYST_PHASE1_FIRST_SEEN_TIMEOUT.
const DefaultFirstSeenTimeout = 15 * time.Minute

// DefaultLatePollTimeout — how long the watcher keeps re-checking
// HelmRelease status AFTER the all-terminal gate fires with at least
// one failed component. The Helm-controller `remediation.retries: N`
// path can flip a failed HR back to installing (and eventually to
// installed) when a NEW chart version is reconciled or a transient
// dependency catches up. Without late-poll, the watch returns
// OutcomeFailed the moment all HRs are terminal — even if Flux is
// about to retry and converge.
//
// Issue #910 (otech105 incident, 2026-05-05): bp-catalyst-platform
// 1.4.17 hit the missing-sme-namespace InstallFailed on first install,
// 1.4.18 (chart-version bump) succeeded a few minutes later — the
// cluster reached 40/40 HRs Ready=True but the orchestrator had
// already marked the deployment FAILED at the moment of the 1.4.17
// terminal-failure observation. 10 minutes is the upper bound on
// observed Flux retry recovery for bp-catalyst-platform on otech105.
//
// Operator override: CATALYST_PHASE1_LATE_POLL_TIMEOUT.
const DefaultLatePollTimeout = 10 * time.Minute

// DefaultLatePollInterval — cadence at which the watcher's late-poll
// loop emits progress events to the SSE stream. The actual state map
// is updated event-driven via processEvent; the ticker's only role is
// to surface "still waiting for component X to recover" diagnostics
// so the wizard log pane shows the late-poll is alive instead of
// going silent during the recovery window.
//
// Operator override: CATALYST_PHASE1_LATE_POLL_INTERVAL.
const DefaultLatePollInterval = 30 * time.Second

// DefaultReachabilityProbeTimeout — per-attempt timeout for the
// pre-flight apiserver reachability probe (issue #923). After a
// catalyst-api Pod restart, the new Pod's TLS handshake to the
// Sovereign apiserver can hang for the full WatchTimeout (60m
// default) when the LB is mid-warm-up or kube-vip is flapping. The
// probe forces individual attempts to fail fast so we can retry with
// backoff while emitting "watcher-reconnecting" diagnostics into the
// SSE stream.
//
// 10 seconds is sized well above the healthy handshake observed in
// production (sub-second on otech10x and on the partner-hosted Qwen
// probe over a similar network position), with headroom for slow
// Hetzner regions.
const DefaultReachabilityProbeTimeout = 10 * time.Second

// DefaultReachabilityRetryInitialInterval — initial backoff for the
// pre-flight reachability probe (issue #923). Each failed attempt
// doubles the wait up to DefaultReachabilityRetryMaxInterval.
const DefaultReachabilityRetryInitialInterval = 5 * time.Second

// DefaultReachabilityRetryMaxInterval — upper bound on the pre-flight
// reachability probe backoff (issue #923). Caps the inter-attempt
// gap so a long unreachable window still emits a "watcher-
// reconnecting" event at the wizard log pane at least once a minute.
const DefaultReachabilityRetryMaxInterval = 60 * time.Second

// DefaultReachabilityOverallBudget — overall budget for the pre-flight
// reachability loop before falling through to factory.Start (issue
// #923). 10 minutes is generous: a healthy Sovereign answers within
// seconds. After this, the informer cache-sync path takes over with
// classifyOutcomeOnContextEnd → OutcomeFluxNotReconciling as the
// terminal classification — exactly the right diagnostic for a
// genuinely unreachable apiserver.
//
// Operator override: CATALYST_PHASE1_REACHABILITY_BUDGET.
const DefaultReachabilityOverallBudget = 10 * time.Minute

// DefaultRESTConfigTimeout — per-request timeout we set on the REST
// config built by NewDynamicClientFromKubeconfig /
// NewKubernetesClientFromKubeconfig (issue #923). Without this, the
// rest.Config has no per-request deadline and a hung TLS handshake
// can block the informer's List call until the overall WatchTimeout
// fires (60m). 30 seconds caps individual REST calls so the
// informer's internal retry loop has a chance to recover when
// transient TLS / LB flaps clear.
const DefaultRESTConfigTimeout = 30 * time.Second

// Phase-1 outcome strings — Watcher.Outcome() returns one of these so
// the handler can set Result.Phase1Outcome (read by the Sovereign
// Admin banner). Empty string means the watch has not yet terminated.
const (
	// OutcomeReady — every observed component reached "installed",
	// ≥ MinBootstrapKitHRs were observed, no failures.
	OutcomeReady = "ready"

	// OutcomeFailed — every observed component reached terminal state
	// AND ≥ MinBootstrapKitHRs were observed, but at least one was
	// "failed".
	OutcomeFailed = "failed"

	// OutcomeTimeout — overall WatchTimeout elapsed before
	// terminate-on-all-done fired. ≥ 1 HelmRelease was observed at
	// some point — partial state is in ComponentStates.
	OutcomeTimeout = "timeout"

	// OutcomeFluxNotReconciling — overall WatchTimeout elapsed AND
	// not a single bp-* HelmRelease was ever observed. The
	// bootstrap-kit Kustomization on the new Sovereign isn't
	// reconciling. Operator playbook in
	// docs/RUNBOOK-PROVISIONING.md §"Phase 1 watch shows 0
	// HelmReleases".
	OutcomeFluxNotReconciling = "flux-not-reconciling"

	// OutcomeKubeconfigMissing — Phase-1 watch never ran because no
	// kubeconfig was available on the catalyst-api side. The new
	// Sovereign's cloud-init has not yet PUT its kubeconfig to
	// /api/v1/deployments/{id}/kubeconfig. catalyst-api cannot observe
	// per-HelmRelease state and MUST NOT report Status=ready: the
	// cluster's reconciliation could be at any state including total
	// failure. Issue #488 — Phase-8a bug #8.
	OutcomeKubeconfigMissing = "kubeconfig-missing"

	// OutcomeWatcherStartFailed — helmwatch.NewWatcher returned an
	// error before the informer ever ran (e.g. malformed kubeconfig,
	// dynamic-factory init panic). Treated as a hard failure for the
	// same reason as OutcomeKubeconfigMissing — without an informer,
	// no per-component state can be observed.
	OutcomeWatcherStartFailed = "watcher-start-failed"

	// OutcomeFluxCRDsAbsent — the new Sovereign PUT its kubeconfig and
	// its CNI is healthy (apiserver reachable, nodes Ready), but the
	// Flux HelmRelease CRD (helmreleases.helm.toolkit.fluxcd.io) was
	// never installed: cloud-init's flux-install stage did not land.
	// Without that CRD there is nothing for the Phase-1 watcher to
	// observe — the deployment would otherwise idle "phase1-watching"
	// for the full WatchTimeout with no fast, named diagnostic. The
	// handler's bounded Flux-CRD-presence probe (runPhase1Watch, before
	// NewWatcher) classifies this on a POSITIVE "CRD absent + cluster
	// reachable" observation after its budget elapses, so the operator
	// gets an immediate, actionable failure instead of a multi-hour
	// hang. Issue #5042. Operator playbook: SSH the control-plane and
	// inspect /var/log/cloud-init-output.log for the flux-install step
	// + ls /var/lib/rancher/k3s/server/manifests/ for the flux
	// manifest (docs/RUNBOOK-PROVISIONING.md).
	OutcomeFluxCRDsAbsent = "flux-crds-absent"
)

// State enums — kept as constants so callers (handler, tests) compare
// against them by identifier rather than literal strings.
const (
	StatePending    = "pending"
	StateInstalling = "installing"
	StateInstalled  = "installed"
	StateDegraded   = "degraded"
	StateFailed     = "failed"
)

// Phase-1 sub-state strings — surfaced via OnSubstate so the handler
// can stamp them onto Deployment.Result.Phase1Substate. Read by the
// Sovereign Admin's wizard banner to render a more granular status
// pill while Status itself stays "phase1-watching" (issue #923).
const (
	// SubstateReconnecting — set while the pre-flight reachability
	// probe is retrying after a Pod restart. Cleared (set to "") once
	// the apiserver answers and the informer's first sync completes.
	SubstateReconnecting = "watcher-reconnecting"

	// SubstateWatching — set immediately after a successful
	// reachability probe + WaitForCacheSync return. The watch is now
	// observing live HelmRelease transitions; the wizard's "X of Y
	// installed" pill takes over from this point.
	SubstateWatching = "watcher-watching"
)

// terminalStates — once a component reaches one of these, the watch
// stops emitting state-change events for it. "degraded" is NOT terminal
// — a degraded component can recover (Flux retries automatically); the
// watch keeps emitting until it converges to installed or failed. This
// is the documented Sovereign Admin contract: "X of Y components
// installed" excludes degraded from the installed count.
var terminalStates = map[string]bool{
	StateInstalled: true,
	StateFailed:    true,
}

// Phase identifiers — kept here so the handler and the watch agree on
// the wire format byte-for-byte. The Sovereign Admin's Logs tab keys
// off Phase == "component-log" to filter helm-controller noise from
// bp-* HelmRelease lifecycle events.
const (
	PhaseComponent    = "component"
	PhaseComponentLog = "component-log"
)

// Emit is the callback the Watcher invokes for every event it derives.
// The handler's runProvisioning tee passes provisioner.recordEventAndPersist
// here so the durable eventsBuf + SSE channel get every component event
// the same way they get Phase-0 OpenTofu events.
type Emit func(ev provisioner.Event)

// Config — runtime configuration the Watcher reads. Production wires
// this from environment + the kubeconfig YAML loaded from
// Deployment.Result.KubeconfigPath (the file the catalyst-api
// reads at runPhase1Watch time, populated by the cloud-init PUT
// per issue #183); tests inject via the Watcher constructor.
type Config struct {
	// KubeconfigYAML — raw bytes of the new Sovereign's k3s kubeconfig.
	// Empty string is invalid (Watch returns an error immediately).
	KubeconfigYAML string

	// WatchTimeout — overall budget for Phase 1. After this, the watch
	// terminates regardless of HelmRelease state. Defaults to
	// DefaultWatchTimeout; the catalyst-api's main reads
	// CATALYST_PHASE1_WATCH_TIMEOUT and passes the parsed Duration.
	WatchTimeout time.Duration

	// Now — clock injection point. Production passes time.Now; tests
	// inject a fake clock so termination-on-timeout is deterministic.
	Now func() time.Time

	// DynamicFactory — produces a dynamic.Interface from the kubeconfig.
	// Production passes NewDynamicClientFromKubeconfig; tests inject a
	// closure returning a fake.NewSimpleDynamicClient so no real cluster
	// is needed.
	DynamicFactory func(kubeconfigYAML string) (dynamic.Interface, error)

	// CoreFactory — produces a kubernetes.Interface for log tailing
	// and Pod listing (helm-controller log tail uses
	// CoreV1().Pods().GetLogs()). Optional: a nil factory disables log
	// streaming and the watch emits only state-change events. Tests
	// keep this nil unless they're specifically exercising the log
	// path.
	CoreFactory func(kubeconfigYAML string) (kubernetes.Interface, error)

	// Resync — informer resync period. Defaults to 30s. Tests override
	// to 0 (event-driven only) to keep test runtime tight.
	Resync time.Duration

	// MinBootstrapKitHRs — the lower bound on the count of observed
	// bp-* HelmReleases below which the terminate-on-all-done check
	// is suppressed. Defaults to DefaultMinBootstrapKitHRs (11). The
	// catalyst-api wires CATALYST_PHASE1_MIN_BOOTSTRAP_KIT_HRS into
	// this; tests inject a smaller value (e.g. 3) so they can prove
	// the gate without seeding the full kit.
	//
	// Rationale: an empty informer cache (zero observed HRs) MUST NOT
	// satisfy "all observed are terminal" — see helmwatch.go for the
	// bug-fix narrative.
	MinBootstrapKitHRs int

	// ReadySentinelComponent — component id (bp- prefix stripped) that
	// MUST have been observed before the terminate-on-all-done gate may
	// fire (#4746). When non-empty, allObservedTerminal additionally
	// requires this id to be present in the observed set; the same
	// all-terminal loop then guarantees it is in a terminal state. This
	// closes the narrow-census false-ready on slow multi-region provs
	// where an early HR reaches Ready before the console's own backend
	// (catalyst-platform) has even been created. Empty (the applyDefaults
	// default) disables the requirement so existing fixtures are
	// unaffected. Production wires DefaultReadySentinelComponent via the
	// handler; CATALYST_PHASE1_READY_SENTINEL overrides.
	ReadySentinelComponent string

	// FirstSeenTimeout — duration after watch start during which the
	// Watcher waits for the FIRST bp-* HelmRelease. If zero HRs are
	// observed within this window, a single warn event fires and the
	// watch CONTINUES (HRs may still appear). Defaults to
	// DefaultFirstSeenTimeout (15m). The catalyst-api wires
	// CATALYST_PHASE1_FIRST_SEEN_TIMEOUT into this.
	FirstSeenTimeout time.Duration

	// LatePollTimeout — duration the watcher keeps the informer running
	// AFTER the all-terminal gate has fired with at least one failed
	// component, giving Flux helm-controller's remediation.retries
	// path time to flip the failed HR back to installing → installed.
	// Defaults to DefaultLatePollTimeout (10m). Zero means "fall back
	// to default"; the catalyst-api wires CATALYST_PHASE1_LATE_POLL_TIMEOUT
	// into this. Tests inject a tiny value (e.g. 200ms) to exercise the
	// late-poll-exhausted path deterministically.
	//
	// Issue #910 — see DefaultLatePollTimeout for the otech105 incident
	// that motivated this.
	LatePollTimeout time.Duration

	// LatePollInterval — cadence at which the late-poll mode emits the
	// "still waiting for X to recover" progress event so the wizard log
	// pane stays alive during the recovery window. The actual state map
	// is updated event-driven via processEvent; this is purely the
	// diagnostic emit cadence. Defaults to DefaultLatePollInterval
	// (30s). Zero means "fall back to default"; tests inject a tiny
	// value (e.g. 50ms).
	LatePollInterval time.Duration

	// Reachability — pre-flight apiserver reachability probe (issue
	// #923). Production wires NewReachabilityProbeFromKubeconfig which
	// builds a discovery client and calls ServerVersion() with a short
	// per-attempt timeout. Tests inject a closure that returns N
	// transient errors then nil to exercise the reconnect path
	// deterministically.
	//
	// The contract is: a non-nil return means "apiserver did not
	// answer this attempt — retry with backoff and emit a watcher-
	// reconnecting event"; nil means "ready to start informer".
	//
	// nil here means: fall back to NewReachabilityProbeFromKubeconfig
	// (production default). Setting to an explicit no-op
	// (`func(_ string) func(context.Context) error { return func(context.Context) error { return nil } }`)
	// disables the probe — used by tests that exercise the
	// post-reachability informer path without dragging the probe
	// retry loop into the test runtime.
	Reachability func(kubeconfigYAML string) func(ctx context.Context) error

	// ReachabilityProbeTimeout — per-attempt timeout for the pre-flight
	// reachability probe. Defaults to DefaultReachabilityProbeTimeout
	// (10s).
	ReachabilityProbeTimeout time.Duration

	// ReachabilityRetryInitialInterval — initial inter-attempt gap for
	// the reachability backoff. Defaults to
	// DefaultReachabilityRetryInitialInterval (5s).
	ReachabilityRetryInitialInterval time.Duration

	// ReachabilityRetryMaxInterval — upper bound on the reachability
	// backoff. Defaults to DefaultReachabilityRetryMaxInterval (60s).
	ReachabilityRetryMaxInterval time.Duration

	// ReachabilityOverallBudget — overall budget for the reachability
	// loop. After this, Watch falls through to factory.Start +
	// WaitForCacheSync, which will then time out via WatchTimeout and
	// classify as OutcomeFluxNotReconciling (correct diagnostic for a
	// genuinely unreachable apiserver). Defaults to
	// DefaultReachabilityOverallBudget (10m).
	ReachabilityOverallBudget time.Duration

	// Sleep — sleep injection for the reachability backoff (issue
	// #923). Production passes a closure backed by time.NewTimer that
	// honours ctx; tests inject a no-op so the retry loop runs in
	// microseconds. Defaults to a real-time sleep that blocks until
	// the timer or ctx fires, whichever first.
	Sleep func(ctx context.Context, d time.Duration)

	// OnSubstate — fired whenever the Phase-1 substate transitions
	// (issue #923). Production wires this to a callback that updates
	// Deployment.Result.Phase1Substate under dep.mu so the Sovereign
	// Admin's wizard sees the live "reconnecting…" pill instead of a
	// stale "phase1-watching". Optional; nil disables substate
	// notifications (the Watcher still runs the probe, just without
	// surfacing the field on the deployment record).
	OnSubstate func(substate string)
}

func (c *Config) applyDefaults() {
	if c.WatchTimeout <= 0 {
		c.WatchTimeout = DefaultWatchTimeout
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Resync <= 0 {
		c.Resync = 30 * time.Second
	}
	if c.MinBootstrapKitHRs <= 0 {
		c.MinBootstrapKitHRs = DefaultMinBootstrapKitHRs
	}
	if c.FirstSeenTimeout <= 0 {
		c.FirstSeenTimeout = DefaultFirstSeenTimeout
	}
	if c.LatePollTimeout <= 0 {
		c.LatePollTimeout = DefaultLatePollTimeout
	}
	if c.LatePollInterval <= 0 {
		c.LatePollInterval = DefaultLatePollInterval
	}
	if c.ReachabilityProbeTimeout <= 0 {
		c.ReachabilityProbeTimeout = DefaultReachabilityProbeTimeout
	}
	if c.ReachabilityRetryInitialInterval <= 0 {
		c.ReachabilityRetryInitialInterval = DefaultReachabilityRetryInitialInterval
	}
	if c.ReachabilityRetryMaxInterval <= 0 {
		c.ReachabilityRetryMaxInterval = DefaultReachabilityRetryMaxInterval
	}
	if c.ReachabilityOverallBudget <= 0 {
		c.ReachabilityOverallBudget = DefaultReachabilityOverallBudget
	}
	if c.Sleep == nil {
		c.Sleep = sleepWithContext
	}
}

// noopReachability is the test-only default for Config.Reachability
// when a fake DynamicFactory is also supplied (issue #923). It
// returns a probe that always succeeds, so a dynamic-fake-client
// test is not forced to also stand up a fake reachability probe
// just to exercise the post-probe informer path. A test that
// specifically exercises the reconnect loop sets Config.Reachability
// to a closure that returns transient errors first.
func noopReachability(_ string) func(ctx context.Context) error {
	return func(_ context.Context) error { return nil }
}

// sleepWithContext sleeps for d, returning early when ctx is done.
// Used as the production default for Config.Sleep so the reachability
// retry loop wakes immediately on context cancel (Pod shutdown,
// caller cancel, overall WatchTimeout). Tests inject a no-op closure
// so the backoff runs in microseconds.
func sleepWithContext(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// Watcher observes bp-* HelmReleases in flux-system on the new
// Sovereign cluster and emits per-component events. Construct via
// NewWatcher; call Watch to run until termination.
type Watcher struct {
	cfg  Config
	emit Emit

	// states is the in-flight state map keyed by componentId. Mutated
	// only inside processEvent; readers (terminalStatesSnapshot) take
	// the mutex.
	mu     sync.Mutex
	states map[string]string

	// observed tracks every componentId the watch has seen at least
	// once. The all-installed-or-failed termination check iterates
	// over this set, not the static MinComponentCount, so the watch
	// terminates correctly on a future bootstrap-kit that ships more
	// or fewer components without code change.
	observed map[string]struct{}

	// firstSeenAt — wall-clock instant at which the Watcher first
	// observed a bp-* HelmRelease in the informer cache. Zero
	// (`time.Time{}`) means "no HelmRelease has been observed yet,
	// the bootstrap-kit Kustomization may not be reconciling on the
	// new Sovereign". Mutated under w.mu; the terminate-on-all-done
	// check refuses to even consider termination while firstSeenAt
	// is zero so an empty informer cache cannot be misread as "all
	// done".
	firstSeenAt time.Time

	// firstSeenWarnEmitted — set true once the
	// "Phase 1 watch saw 0 HelmReleases" warn event has been emitted.
	// Guards against re-emission if the Watcher is hypothetically
	// driven across multiple FirstSeenTimeout boundaries.
	firstSeenWarnEmitted bool

	// informerSynced — true after WaitForCacheSync returns successfully,
	// signalling that the initial List() against the apiserver has
	// completed. processEvent refuses to consider terminate-on-all-done
	// while this is false: during the initial sync the informer fires
	// AddFunc once per HelmRelease in arbitrary (often alphabetical)
	// order, so allObservedTerminal({first 12: installed}) would
	// otherwise fire before the remaining 25+ HRs entered the cache.
	// This was the root cause of the otech48 incident (2026-05-03)
	// where 37 HRs reached Ready=True on the Sovereign cluster but
	// the deployment record stayed phase1-watching: the catalyst-api
	// Deployment shipped CATALYST_PHASE1_MIN_BOOTSTRAP_KIT_HRS=38 to
	// patch around the early-termination bug, but the actual
	// bootstrap-kit cardinality had drifted to 37 — so the count gate
	// was permanently unsatisfiable. Using the informer's HasSynced
	// signal instead is robust to bootstrap-kit cardinality changes
	// without touching the chart.
	informerSynced bool

	// readyTransitionEmitted — set true the first time the watcher
	// emits the "Sovereign ready for handover" terminal event so a
	// post-sync stabilisation tick that re-evaluates the gate cannot
	// double-emit. Mutated under w.mu.
	readyTransitionEmitted bool

	// latePollActive — set true while the watcher is in the
	// eventual-consistency late-poll mode (issue #910): the all-
	// terminal trip fired with at least one failed component, but the
	// watcher is still attached and waiting for Flux helm-controller
	// to retry/recover before declaring the deployment failed.
	// Exposed via LatePollActive() for the SnapshotComponents-style
	// callers that want to render "recovering after install
	// timeout" in the wizard. Mutated under w.mu.
	latePollActive bool

	// outcome — terminal classification of the watch run, set
	// exactly once by Watch on the path that returns. Read via
	// Outcome() by the handler so it can set
	// Deployment.Result.Phase1Outcome. Empty string while Watch is
	// running; populated to one of OutcomeReady / OutcomeFailed /
	// OutcomeTimeout / OutcomeFluxNotReconciling at the moment Watch
	// returns.
	outcome string

	// informer — reference to the live dynamic informer driving
	// processEvent. Captured under w.mu inside Watch right after the
	// informer is constructed so SnapshotComponents() can walk the
	// local cache without re-listing the apiserver. nil while
	// Watch() has not yet started; readers must check.
	informer cache.SharedIndexInformer

	// cancel — context.CancelFunc the running Watch loop installs
	// on entry and clears on return. Cancel() (handover finalisation
	// per issue #317) reads this under w.mu and invokes it so the
	// informer's WaitForCacheSync wakes and the main select loop
	// drops into the `<-watchCtx.Done()` branch. Nil while Watch()
	// has not yet started or has already returned — Cancel() is
	// then a no-op.
	cancel context.CancelFunc

	// onSyncedOnce fires exactly once when WaitForCacheSync returns
	// true. The handler subscribes to it via OnInitialListSynced so
	// it can call jobs.Bridge.SeedJobsFromInformerList immediately
	// after the informer's initial-list ADDs have been processed —
	// this is the load-bearing hook for the table-view UX, where a
	// HR that has been Ready=True for an hour MUST seed a Job row
	// even though processEvent's per-transition emit already wrote
	// the same.
	//
	// The hook list is populated via OnInitialListSynced under
	// w.mu, then drained (under the lock) on the first Sync. Late
	// subscribers (registered AFTER Sync fires) are invoked
	// immediately so the call is order-independent at the handler
	// caller site.
	mu2          sync.Mutex
	onSyncedHooks []func([]ComponentSnapshot)
	syncedOnce   bool

	// runtimeSubscribers — fan-out callbacks invoked on EVERY event the
	// Watcher emits (per-component state transitions, post-sync ready
	// transition, first-seen warn, all watch-level diagnostics). The
	// jobs Bridge subscribes here so its per-Job state map is updated on
	// every transition, not just on the post-sync seed snapshot.
	//
	// Issue #695: the historical wiring fanned events out only through
	// the single `emit` callback (handler.emitWatchEvent), which routed
	// to bridge.OnProvisionerEvent via the durable-buffer + SSE emit
	// path. That path is correct in steady state but is brittle to any
	// future refactor that decouples the bridge from emitWatchEvent
	// — so Watcher.Subscribe gives the bridge a direct, dedicated event
	// stream that does not depend on the SSE pipeline being wired.
	//
	// Mutated under mu2; dispatched under no lock (the slice is
	// snapshotted under the lock then iterated without it so a
	// subscriber that calls Subscribe re-entrantly cannot deadlock).
	runtimeSubscribers []func(provisioner.Event)
}

// ComponentSnapshot is one entry in the informer's local cache — the
// value the SnapshotComponents() / OnInitialListSynced() paths emit.
//
// AppID is the normalised component id ("cilium" — bp- prefix
// stripped); Status is the helmwatch state enum
// (StatePending|StateInstalling|StateInstalled|StateDegraded|StateFailed);
// HelmReleaseName is the cluster-side metadata.name ("bp-cilium");
// Namespace is always FluxNamespace; LastTransitionAt is the
// Ready-condition lastTransitionTime when present (zero when Ready is
// missing, e.g. pre-first-reconcile pending); Message is the Ready
// condition message verbatim.
type ComponentSnapshot struct {
	AppID            string    `json:"appId"`
	Status           string    `json:"status"`
	HelmReleaseName  string    `json:"helmReleaseName"`
	Namespace        string    `json:"namespace"`
	LastTransitionAt time.Time `json:"lastTransitionAt"`
	Message          string    `json:"message"`
	// DependsOn — sibling AppIDs this HelmRelease depends on, sourced
	// from spec.dependsOn[].name with the "bp-" prefix stripped.
	// Drives the Jobs Flow view's edge graph (issue #204).
	DependsOn        []string  `json:"dependsOn,omitempty"`
	// Stalled — true when Status==StateFailed AND Flux has exhausted its
	// remediation.retries (Stalled=True / RetriesExceeded). The READ-side
	// /jobs seed uses this to distinguish a STABLY-failed HR (terminal
	// Failed leaf) from a still-retrying transient failure (in-progress
	// leaf), so a healthy-but-converging HR never flaps Failed↔Succeeded
	// on the page (issue #3916). Meaningless when Status != StateFailed.
	Stalled          bool      `json:"stalled,omitempty"`
}

// NewWatcher returns a Watcher with cfg applied. emit must be non-nil
// — a nil emit is a programmer error (the watch's only output channel
// is the callback).
func NewWatcher(cfg Config, emit Emit) (*Watcher, error) {
	if emit == nil {
		return nil, errors.New("helmwatch: emit callback is required")
	}
	if strings.TrimSpace(cfg.KubeconfigYAML) == "" {
		return nil, errors.New("helmwatch: kubeconfig is required (deployment.Result.KubeconfigPath was empty or the file was unreadable)")
	}
	usingTestDynamicFactory := cfg.DynamicFactory != nil
	if cfg.DynamicFactory == nil {
		cfg.DynamicFactory = NewDynamicClientFromKubeconfig
	}
	if cfg.CoreFactory == nil {
		// Production default: same kubeconfig → typed clientset for the
		// helm-controller log tailer. Without this fallback, every
		// `bp-*` HelmRelease's raw helm-controller stdout would be
		// dropped — the FE log viewer would only ever render synthetic
		// `[seeded]` / `[<state>]` summary lines. See issue #305.
		// Tests still inject a fake.NewSimpleClientset via Config.CoreFactory.
		cfg.CoreFactory = NewKubernetesClientFromKubeconfig
	}
	if cfg.Reachability == nil {
		// Production default: discovery-client ServerVersion() against
		// the Sovereign apiserver, with the per-attempt timeout taken
		// from cfg.ReachabilityProbeTimeout (issue #923).
		//
		// Tests that inject a fake DynamicFactory but do NOT explicitly
		// set Reachability get a no-op probe by default — there is no
		// real apiserver to probe and we don't want every existing
		// dynamic-fake-client test to hang against the production
		// probe trying to hit "fake-kubeconfig: bytes". A test that
		// specifically exercises the reconnect path overrides
		// Reachability with its own closure.
		if usingTestDynamicFactory {
			cfg.Reachability = noopReachability
		} else {
			cfg.Reachability = NewReachabilityProbeFromKubeconfig
		}
	}
	cfg.applyDefaults()
	return &Watcher{
		cfg:      cfg,
		emit:     emit,
		states:   make(map[string]string),
		observed: make(map[string]struct{}),
	}, nil
}

// Watch runs the informer-backed watch loop until termination.
// Returns the final per-component state map and nil on clean
// termination (all terminal OR timeout); returns ctx.Err() if the
// caller cancels.
//
// Concurrency: Watch is single-shot. Calling it twice on the same
// Watcher is a programmer error (the informer would double-register).
//
// Cancellation: callers can stop a long-running Watch by calling
// Cancel() on the same Watcher (issue #317 handover finalisation).
// Cancel triggers the same `<-watchCtx.Done()` path as a timeout —
// the informer is torn down, a final synthetic warn event is emitted,
// and Outcome() returns OutcomeTimeout (or OutcomeFluxNotReconciling
// if no HelmRelease was ever observed). Internally Cancel saves the
// `cancel` function under w.mu so the handover handler can stop the
// watch atomically without forcing every caller to thread a context
// through their layers.
//
// The state machine that maps HelmRelease.status.conditions →
// State enum lives in deriveState, which is exported for tests.
func (w *Watcher) Watch(ctx context.Context) (map[string]string, error) {
	dyn, err := w.cfg.DynamicFactory(w.cfg.KubeconfigYAML)
	if err != nil {
		return nil, fmt.Errorf("helmwatch: build dynamic client: %w", err)
	}

	// Per-watch context with the configured timeout. We derive from
	// the caller's ctx so the handler's parent context cancel
	// (deployment delete, Pod shutdown) propagates.
	watchCtx, cancel := context.WithTimeout(ctx, w.cfg.WatchTimeout)
	defer cancel()

	// Pre-flight reachability probe — issue #923. Catalyst-api Pod
	// restart in the middle of Phase-1 leaves the new Pod with no live
	// connection to the Sovereign apiserver. If we go straight to
	// factory.Start, an unreachable apiserver causes
	// cache.WaitForCacheSync to block silently for the entire
	// WatchTimeout (60m) — the deployment record's componentStates
	// stays empty and the wizard log pane goes dark. Probe with a
	// short per-attempt timeout, retry with backoff, and emit
	// "watcher-reconnecting" events so the operator sees live
	// progress.
	//
	// On success: substate flips to SubstateWatching and we proceed to
	// factory.Start. On overall-budget exhaustion: we still proceed
	// to factory.Start (the informer's own timeout path will then
	// classify as OutcomeFluxNotReconciling — exactly the right
	// terminal diagnostic for a genuinely unreachable apiserver).
	if !w.runReachabilityProbe(watchCtx) {
		// runReachabilityProbe returns false only when watchCtx is
		// done — meaning the overall WatchTimeout fired during the
		// probe loop. Surface terminal classification immediately
		// rather than building an informer that will time out on the
		// same condition.
		final := w.terminalStatesSnapshot()
		w.setOutcome(w.classifyOutcomeOnContextEnd())
		return final, fmt.Errorf("helmwatch: pre-flight reachability probe cancelled by context: %w", watchCtx.Err())
	}

	// Stash the cancel func so Watcher.Cancel() can interrupt a
	// long-running watch from another goroutine — the handover
	// finalisation handler (issue #317) calls this when
	// bp-catalyst-platform.Ready=True so the informer stops watching
	// the new Sovereign's apiserver.
	w.mu.Lock()
	w.cancel = cancel
	w.mu.Unlock()
	defer func() {
		// Clear the cancel pointer on Watch return so a late
		// Cancel() call on a finished Watcher is a no-op rather
		// than dereferencing a defunct context.
		w.mu.Lock()
		w.cancel = nil
		w.mu.Unlock()
	}()

	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
		dyn,
		w.cfg.Resync,
		FluxNamespace,
		nil,
	)
	informer := factory.ForResource(HelmReleaseGVR).Informer()

	// Stash the informer for SnapshotComponents() / OnInitialListSynced
	// readers. Captured under w.mu so the read path
	// (SnapshotComponents) sees a non-nil pointer at the linearisation
	// point of "informer is constructed".
	w.mu.Lock()
	w.informer = informer
	w.mu.Unlock()

	// terminated fires when every observed component has reached a
	// terminal state. processEvent closes it under w.mu so a
	// double-close is impossible.
	terminated := make(chan struct{})
	var closeOnce sync.Once

	handler := cache.FilteringResourceEventHandler{
		FilterFunc: func(obj any) bool {
			u, ok := obj.(*unstructured.Unstructured)
			if !ok {
				return false
			}
			return strings.HasPrefix(u.GetName(), "bp-")
		},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj any) {
				w.processEvent(obj, terminated, &closeOnce)
			},
			UpdateFunc: func(_, obj any) {
				w.processEvent(obj, terminated, &closeOnce)
			},
			// DeleteFunc — a HelmRelease being deleted mid-bootstrap
			// is operator action (or a Flux suspend/delete
			// reconciliation). We don't emit a synthetic state for
			// it because that would race the operator's intent. The
			// Sovereign Admin treats absent-from-cluster as "n/a"
			// in its overall percentage.
		},
	}
	if _, err := informer.AddEventHandler(handler); err != nil {
		return nil, fmt.Errorf("helmwatch: register event handler: %w", err)
	}

	// Optional log tailer — runs in parallel with the state watch and
	// terminates on the same context.
	if w.cfg.CoreFactory != nil {
		core, err := w.cfg.CoreFactory(w.cfg.KubeconfigYAML)
		if err != nil {
			// Non-fatal — emit a warn event and continue without log
			// streaming. The state watch is the load-bearing part.
			w.dispatch(provisioner.Event{
				Time:    w.cfg.Now().UTC().Format(time.RFC3339),
				Phase:   PhaseComponent,
				Level:   "warn",
				Message: "helm-controller log tailing disabled: build core client: " + err.Error(),
			})
		} else {
			tailer := newLogTailer(core, w.dispatch, w.cfg.Now)
			go tailer.run(watchCtx)
		}
	}

	// Start the informer. factory.Start launches a goroutine per
	// resource type — we have one (HelmRelease) so it's a single
	// goroutine that reads from the apiserver Watch endpoint.
	factory.Start(watchCtx.Done())
	if !cache.WaitForCacheSync(watchCtx.Done(), informer.HasSynced) {
		// Sync failed — usually because watchCtx already cancelled
		// (timeout or caller). We still emit a final state event for
		// every observed component so the SSE consumer can render the
		// stuck-where-we-got-to view.
		final := w.terminalStatesSnapshot()
		w.setOutcome(w.classifyOutcomeOnContextEnd())
		return final, fmt.Errorf("helmwatch: informer cache failed to sync: %w", watchCtx.Err())
	}

	// Initial-list synced — flip the gate that lets processEvent
	// consider terminate-on-all-done, then re-evaluate the gate once
	// against the synced cache so a Watcher attaching to a cluster
	// where every HR is already Ready=True still terminates.
	//
	// processEvent fires AddFunc for every HelmRelease in the initial
	// List BEFORE WaitForCacheSync returns. Those events accumulate
	// state in w.states + w.observed but MUST NOT close `terminated`
	// — otherwise the watcher exits as soon as the alphabetically
	// first batch of HRs land Ready, before the remaining HRs even
	// enter the cache. We park the all-terminal check until here.
	w.mu.Lock()
	w.informerSynced = true
	allTerminalAtSync := allObservedTerminal(w.states, w.observed, w.cfg.MinBootstrapKitHRs, w.firstSeenAt, true, w.cfg.ReadySentinelComponent)
	w.mu.Unlock()

	if allTerminalAtSync {
		// Resume-after-restart path: every bp-* HR is already
		// Ready=True at attach time. Emit the terminal "ready" event
		// immediately so the wizard's auto-redirect fires, then close
		// `terminated` so the main loop returns through the clean
		// path with OutcomeReady.
		w.maybeEmitReadyTransition()
		closeOnce.Do(func() { close(terminated) })
	}

	// Initial-list synced — fire the post-sync hooks. The bridge
	// uses this to seed Jobs from every HelmRelease the informer
	// already has in its cache, including HRs that have been
	// Ready=True for an hour (the AddFunc → processEvent path emits
	// a state-change event for those too, but the hook gives the
	// bridge a single atomic batch instead of N transitions to
	// process — which makes idempotency cheaper to reason about).
	w.fireOnSyncedHooks()

	// First-seen ticker — periodically checks whether the watch has
	// observed at least one bp-* HelmRelease within FirstSeenTimeout.
	// If not, emits a single warn event so the Sovereign Admin can
	// render "Phase 1: bootstrap-kit not reconciling" and the
	// operator can run `flux get kustomization -n flux-system` on
	// the new cluster. The watch CONTINUES — late HRs still flow.
	//
	// We use a ticker (not a one-shot timer) so the check is
	// independent of the informer event clock — an idle informer
	// cannot starve the timeout check.
	firstSeenStart := w.cfg.Now()
	firstSeenCheckInterval := w.cfg.FirstSeenTimeout / 4
	if firstSeenCheckInterval < 100*time.Millisecond {
		firstSeenCheckInterval = 100 * time.Millisecond
	}
	if firstSeenCheckInterval > 30*time.Second {
		firstSeenCheckInterval = 30 * time.Second
	}
	firstSeenTicker := time.NewTicker(firstSeenCheckInterval)
	defer firstSeenTicker.Stop()

	// Wait for either all-terminal or context done.
	for {
		select {
		case <-terminated:
			// Clean termination — every observed component reached a
			// terminal state AND ≥ MinBootstrapKitHRs were observed.
			//
			// Issue #910 — eventual-consistency late-poll: if the
			// terminal snapshot contains at least one failed component,
			// keep the informer running for LatePollTimeout to give
			// Flux helm-controller's remediation.retries path room to
			// flip the failed HR back to installing → installed (e.g.
			// chart-version bump from 1.4.17 → 1.4.18 in the otech105
			// incident). The informer is already attached and
			// processEvent keeps updating w.states, so we just need to
			// re-read terminalStatesSnapshot until it converges to
			// all-installed or the late-poll deadline fires. If at the
			// end of the late-poll window any HR is still failed, we
			// classify as before (OutcomeFailed). If during the late-
			// poll all HRs reach installed, classify as OutcomeReady.
			final := w.terminalStatesSnapshot()
			if hasFailures(final) {
				final = w.runLatePoll(watchCtx, final)
			}
			w.setOutcome(w.classifyOutcomeOnTerminate(final))
			return final, nil

		case <-watchCtx.Done():
			// Timeout or caller cancel — emit a single warn event so
			// the Sovereign Admin can render "watch ended (timeout):
			// X of Y installed" rather than going silent.
			w.dispatch(provisioner.Event{
				Time:    w.cfg.Now().UTC().Format(time.RFC3339),
				Phase:   PhaseComponent,
				Level:   "warn",
				Message: "Phase-1 watch terminated by context: " + watchCtx.Err().Error() + " — see ComponentStates for current outcome",
			})
			final := w.terminalStatesSnapshot()
			w.setOutcome(w.classifyOutcomeOnContextEnd())
			return final, nil

		case <-firstSeenTicker.C:
			w.maybeEmitFirstSeenWarn(firstSeenStart)
			// Loop and keep waiting — first-seen timeout does NOT
			// terminate the watch.
		}
	}
}

// hasFailures returns true when at least one component in the state
// map sits in StateFailed. Used by the late-poll gate (issue #910) to
// decide whether to enter eventual-consistency recovery mode after the
// all-terminal trip fires.
func hasFailures(states map[string]string) bool {
	for _, s := range states {
		if s == StateFailed {
			return true
		}
	}
	return false
}

// runLatePoll keeps the informer running for w.cfg.LatePollTimeout
// AFTER the main all-terminal gate fired with at least one failed
// component. The informer is already attached + processEvent keeps
// updating w.states on every helm-controller transition; runLatePoll
// re-reads terminalStatesSnapshot on each tick until the state
// converges to all-installed (return early with OutcomeReady-shaped
// state) OR the late-poll deadline fires (return current state, which
// the caller then classifies as OutcomeFailed). Issue #910.
//
// The function takes the current `final` snapshot the caller observed
// at the moment of the all-terminal trip, emits a single info-level
// "late-poll started" event so the wizard surfaces the recovery
// window, then ticks at w.cfg.LatePollInterval emitting progress
// events naming the failed components still pending. On every tick
// the live snapshot is re-read; the moment every observed component
// is StateInstalled (no failed, no installing, no degraded), the
// function emits a single info-level "late-poll succeeded" event and
// returns. If the deadline elapses first, a single warn-level "late-
// poll exhausted" event fires and the function returns the current
// snapshot (which will still contain failures the caller classifies
// as OutcomeFailed).
//
// watchCtx is the same context driving the informer + log tailer; if
// it is cancelled (overall WatchTimeout, caller cancel) the late-poll
// returns immediately without waiting for the deadline. The Watch()
// caller's context.Done branch is NOT reachable from inside late-
// poll because we're already on the <-terminated path; we still honor
// watchCtx so a Cancel() racing the late-poll path tears down cleanly.
func (w *Watcher) runLatePoll(watchCtx context.Context, initial map[string]string) map[string]string {
	timeout := w.cfg.LatePollTimeout
	interval := w.cfg.LatePollInterval

	failed := failedComponents(initial)
	w.dispatch(provisioner.Event{
		Time:  w.cfg.Now().UTC().Format(time.RFC3339),
		Phase: PhaseComponent,
		Level: "warn",
		Message: fmt.Sprintf(
			"Phase 1 watch: all components reached terminal state but %d failed (%s). Entering eventual-consistency late-poll for up to %s — Flux helm-controller may retry and recover.",
			len(failed),
			strings.Join(failed, ", "),
			timeout,
		),
	})

	deadline := w.cfg.Now().Add(timeout)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Mark late-poll active so processEvent / SnapshotComponents
	// callers can observe the mode (kept under w.mu for the same
	// reason every other state field is — concurrent reads from
	// /components/state etc.).
	w.mu.Lock()
	w.latePollActive = true
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		w.latePollActive = false
		w.mu.Unlock()
	}()

	for {
		// Snapshot the current state under w.mu — processEvent has
		// been updating it concurrently as Flux retries land.
		current := w.terminalStatesSnapshot()
		if isAllInstalled(current) {
			w.dispatch(provisioner.Event{
				Time:  w.cfg.Now().UTC().Format(time.RFC3339),
				Phase: PhaseComponent,
				Level: "info",
				Message: fmt.Sprintf(
					"Phase 1 watch: late-poll succeeded — all %d components reached installed after Flux retry recovery. Sovereign ready for handover.",
					len(current),
				),
			})
			// Re-emit the per-component "ready transition" so the
			// terminal status pill flips green even though
			// maybeEmitReadyTransition already fired at the moment
			// of the original <-terminated trip (it fires only on
			// the first all-terminal observation; we want a clean
			// success message after late-poll recovery).
			w.maybeEmitReadyTransition()
			return current
		}

		// Not yet all-installed — emit a progress event naming the
		// components still pending so the wizard log pane stays
		// alive during the recovery window. The state map mirrors
		// reality, so we surface the per-component pending list.
		stillFailed := failedComponents(current)
		stillInstalling := installingComponents(current)

		select {
		case <-watchCtx.Done():
			// Overall watch timeout fired during late-poll — return
			// whatever we have. Classification follows the
			// classifyOutcomeOnContextEnd path via the caller (we're
			// on the <-terminated branch here, but this defends
			// against a Cancel() racing the late-poll loop).
			w.dispatch(provisioner.Event{
				Time:  w.cfg.Now().UTC().Format(time.RFC3339),
				Phase: PhaseComponent,
				Level: "warn",
				Message: fmt.Sprintf(
					"Phase 1 watch: late-poll cancelled by context (%s). Failed components at exit: %s.",
					watchCtx.Err().Error(),
					strings.Join(stillFailed, ", "),
				),
			})
			return current

		case <-ticker.C:
			if w.cfg.Now().After(deadline) {
				w.dispatch(provisioner.Event{
					Time:  w.cfg.Now().UTC().Format(time.RFC3339),
					Phase: PhaseComponent,
					Level: "error",
					Message: fmt.Sprintf(
						"Phase 1 watch: late-poll exhausted after %s — %d component(s) still failed (%s). Marking deployment failed.",
						timeout,
						len(stillFailed),
						strings.Join(stillFailed, ", "),
					),
				})
				return current
			}

			// Progress emit — only if there's something still
			// pending recovery (otherwise we'd have hit
			// isAllInstalled at the top of the loop).
			parts := []string{}
			if len(stillFailed) > 0 {
				parts = append(parts, fmt.Sprintf("failed=[%s]", strings.Join(stillFailed, ", ")))
			}
			if len(stillInstalling) > 0 {
				parts = append(parts, fmt.Sprintf("installing=[%s]", strings.Join(stillInstalling, ", ")))
			}
			w.dispatch(provisioner.Event{
				Time:  w.cfg.Now().UTC().Format(time.RFC3339),
				Phase: PhaseComponent,
				Level: "info",
				Message: fmt.Sprintf(
					"Phase 1 watch: late-poll waiting for recovery (%s remaining); %s.",
					deadline.Sub(w.cfg.Now()).Round(time.Second),
					strings.Join(parts, ", "),
				),
			})
		}
	}
}

// failedComponents returns the sorted list of component IDs sitting in
// StateFailed in the supplied snapshot. Used by runLatePoll to surface
// the per-component pending list in progress events.
func failedComponents(states map[string]string) []string {
	out := []string{}
	for id, s := range states {
		if s == StateFailed {
			out = append(out, id)
		}
	}
	sortStrings(out)
	return out
}

// installingComponents returns the sorted list of component IDs in
// any non-installed, non-failed state — these are the components that
// Flux has flipped back to a transient state during late-poll
// recovery.
func installingComponents(states map[string]string) []string {
	out := []string{}
	for id, s := range states {
		if s != StateInstalled && s != StateFailed {
			out = append(out, id)
		}
	}
	sortStrings(out)
	return out
}

// isAllInstalled returns true when every component in the snapshot is
// StateInstalled (no failed, no installing, no degraded). Used by the
// late-poll loop to detect successful recovery.
func isAllInstalled(states map[string]string) bool {
	if len(states) == 0 {
		return false
	}
	for _, s := range states {
		if s != StateInstalled {
			return false
		}
	}
	return true
}

// sortStrings — small in-place sort so the test assertions and the
// progress-event message ordering are deterministic. Pulled out so
// helmwatch doesn't pull in the sort package transitively just for
// the late-poll path; uses a 6-line bubble sort which is O(n²) but
// fine for the bounded (~40) bp-* component count.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// maybeEmitFirstSeenWarn emits the "Phase 1 watch saw 0 HelmReleases
// in <timeout>" warn event the first time FirstSeenTimeout elapses
// with zero observed components. Re-emission is suppressed via
// firstSeenWarnEmitted so a long-running stuck watch does not flood
// the SSE buffer with the same diagnostic.
//
// The watch CONTINUES after this — late HelmReleases still flow into
// processEvent. The point of the warn is to surface "the
// bootstrap-kit Kustomization isn't reconciling on the new cluster"
// to the operator while preserving recoverability.
func (w *Watcher) maybeEmitFirstSeenWarn(firstSeenStart time.Time) {
	w.mu.Lock()
	alreadyEmitted := w.firstSeenWarnEmitted
	hasFirstSeen := !w.firstSeenAt.IsZero()
	w.mu.Unlock()

	if alreadyEmitted || hasFirstSeen {
		return
	}
	if w.cfg.Now().Sub(firstSeenStart) < w.cfg.FirstSeenTimeout {
		return
	}

	w.mu.Lock()
	// Re-check under the lock to avoid a double-emit race with
	// processEvent flipping firstSeenAt.
	if w.firstSeenWarnEmitted || !w.firstSeenAt.IsZero() {
		w.mu.Unlock()
		return
	}
	w.firstSeenWarnEmitted = true
	w.mu.Unlock()

	w.dispatch(provisioner.Event{
		Time:  w.cfg.Now().UTC().Format(time.RFC3339),
		Phase: PhaseComponent,
		Level: "warn",
		// No Component field — this is a watch-level diagnostic, not
		// a per-component state change. The Sovereign Admin's banner
		// reducer keys off Phase=="component" + Component=="" to
		// surface this as a top-level alert.
		Message: fmt.Sprintf(
			"Phase 1 watch saw 0 HelmReleases in %s; the bootstrap-kit Kustomization may not be reconciling. Operator: run `flux get kustomization -n flux-system` on the new cluster.",
			w.cfg.FirstSeenTimeout,
		),
	})
}

// runReachabilityProbe drives the pre-flight apiserver reachability
// loop (issue #923). Returns true on success (apiserver answered OR
// the overall reachability budget elapsed — caller should fall
// through to factory.Start either way). Returns false only when
// watchCtx fires during the probe loop — caller exits early with
// classifyOutcomeOnContextEnd.
//
// The first attempt is a single info-level "starting reachability
// probe" event so the wizard log pane shows the watch is alive.
// Each subsequent failed attempt emits a warn-level
// "watcher-reconnecting" event naming the attempt number + the
// observed error. On success a single info-level "reachable" event
// fires and substate flips to SubstateWatching. The first failed
// attempt also flips substate to SubstateReconnecting so the wizard
// banner can render "reconnecting…" while the loop runs.
//
// Backoff: starts at ReachabilityRetryInitialInterval (5s), doubles
// each failed attempt, caps at ReachabilityRetryMaxInterval (60s).
// Sleeps via cfg.Sleep so tests can inject a no-op and exercise the
// loop in microseconds.
func (w *Watcher) runReachabilityProbe(watchCtx context.Context) bool {
	probe := w.cfg.Reachability(w.cfg.KubeconfigYAML)
	budgetDeadline := w.cfg.Now().Add(w.cfg.ReachabilityOverallBudget)
	interval := w.cfg.ReachabilityRetryInitialInterval

	attempt := 0
	for {
		attempt++
		// Per-attempt context with the configured probe timeout, so a
		// hung TLS handshake fails fast and we can retry.
		probeCtx, probeCancel := context.WithTimeout(watchCtx, w.cfg.ReachabilityProbeTimeout)
		err := probe(probeCtx)
		probeCancel()

		if err == nil {
			// Apiserver reachable — flip substate to watching, emit a
			// concise "reachable" diagnostic, return success.
			if attempt > 1 {
				w.dispatch(provisioner.Event{
					Time:  w.cfg.Now().UTC().Format(time.RFC3339),
					Phase: PhaseComponent,
					Level: "info",
					Message: fmt.Sprintf(
						"Phase-1 watch: Sovereign apiserver reachable on attempt %d — starting per-component HelmRelease watch.",
						attempt,
					),
				})
			}
			w.notifySubstate(SubstateWatching)
			return true
		}

		// Probe failed — surface diagnostic. First failure is the
		// transition from "starting" to "reconnecting"; subsequent
		// failures emit a re-attempt summary so the wizard log pane
		// shows the loop is alive.
		w.notifySubstate(SubstateReconnecting)
		w.dispatch(provisioner.Event{
			Time:  w.cfg.Now().UTC().Format(time.RFC3339),
			Phase: PhaseComponent,
			Level: "warn",
			Message: fmt.Sprintf(
				"Phase-1 watch: Sovereign apiserver unreachable on attempt %d (%s); retrying in %s — typically a transient TLS handshake / LB warm-up after Pod restart.",
				attempt,
				err.Error(),
				interval,
			),
		})

		// Check the overall reachability budget BEFORE sleeping so we
		// don't sleep past the budget for nothing. Once the budget is
		// exhausted we still return true and let the caller fall
		// through to factory.Start — the informer's own retry path
		// + WaitForCacheSync timeout will then classify as
		// OutcomeFluxNotReconciling on a genuinely unreachable
		// apiserver, exactly the right operator-actionable diagnostic.
		if w.cfg.Now().After(budgetDeadline) {
			w.dispatch(provisioner.Event{
				Time:  w.cfg.Now().UTC().Format(time.RFC3339),
				Phase: PhaseComponent,
				Level: "warn",
				Message: fmt.Sprintf(
					"Phase-1 watch: reachability budget %s exhausted after %d attempts; falling through to informer (will time out as flux-not-reconciling on a genuinely unreachable apiserver).",
					w.cfg.ReachabilityOverallBudget,
					attempt,
				),
			})
			return true
		}

		// Sleep with cancellation. cfg.Sleep wakes immediately if
		// watchCtx is done so a Pod-shutdown / overall-WatchTimeout
		// during a long backoff returns the loop early.
		w.cfg.Sleep(watchCtx, interval)
		if watchCtx.Err() != nil {
			return false
		}

		// Exponential backoff up to the cap.
		interval = interval * 2
		if interval > w.cfg.ReachabilityRetryMaxInterval {
			interval = w.cfg.ReachabilityRetryMaxInterval
		}
	}
}

// notifySubstate fires the OnSubstate callback if one is configured
// (issue #923). Production wires this to a closure on the handler
// that updates Deployment.Result.Phase1Substate under dep.mu so a
// /deployments/<id> GET returns the live substate. A nil OnSubstate
// is the test-only path — the watcher still runs the probe + emits
// the corresponding events.
func (w *Watcher) notifySubstate(substate string) {
	if w.cfg.OnSubstate == nil {
		return
	}
	w.cfg.OnSubstate(substate)
}

// classifyOutcomeOnTerminate maps a clean terminate-on-all-done into
// OutcomeReady or OutcomeFailed. Called only on the `<-terminated`
// branch, where the gate guarantees len(observed) ≥
// MinBootstrapKitHRs and every observed state is in
// terminalStates.
func (w *Watcher) classifyOutcomeOnTerminate(final map[string]string) string {
	for _, s := range final {
		if s == StateFailed {
			return OutcomeFailed
		}
	}
	return OutcomeReady
}

// classifyOutcomeOnContextEnd maps a context-cancelled exit into
// OutcomeTimeout or OutcomeFluxNotReconciling depending on whether
// any HelmRelease was ever observed. The handler reads this through
// Watcher.Outcome() and copies into Deployment.Result.Phase1Outcome
// so the Sovereign Admin's UI banner can render the right diagnostic.
func (w *Watcher) classifyOutcomeOnContextEnd() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.firstSeenAt.IsZero() {
		return OutcomeFluxNotReconciling
	}
	return OutcomeTimeout
}

// setOutcome stores the terminal classification under the lock.
// Called exactly once per watch run from Watch() before returning.
func (w *Watcher) setOutcome(out string) {
	w.mu.Lock()
	w.outcome = out
	w.mu.Unlock()
}

// Outcome returns the terminal classification of the watch run, or ""
// if Watch has not yet returned. The handler reads this immediately
// after Watch returns to copy onto Deployment.Result.Phase1Outcome.
func (w *Watcher) Outcome() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.outcome
}

// LatePollActive reports whether the watcher is currently inside the
// eventual-consistency late-poll loop (issue #910). Returns true
// between the moment runLatePoll dispatches the "entering late-poll"
// event and the moment it returns (either success or exhaustion).
// Used by the /components/state handler to render a "recovering
// after install timeout" pill in the wizard so the operator does not
// see a stale "FAILED" while Flux is still retrying.
//
// Always false after Watch() returns — the deferred unlock-and-clear
// in runLatePoll guarantees the flag is reset before the caller
// reads Outcome().
func (w *Watcher) LatePollActive() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.latePollActive
}

// Cancel interrupts a running Watch loop by invoking the per-watch
// context's cancel function. Issue #317's handover finalisation calls
// this when bp-catalyst-platform.Ready=True on the new Sovereign so the
// informer's connection to the new cluster's apiserver is torn down
// (zero operational footprint on Catalyst-Zero post-handover).
//
// Cancel is safe to call from any goroutine and idempotent — calling it
// before Watch starts, after Watch returns, or twice in succession is a
// no-op. The Watch loop sees the cancellation through `<-watchCtx.Done()`
// and exits via the standard timeout path: a single synthetic warn
// event is emitted, the informer goroutine returns, and Outcome() ends
// up OutcomeTimeout (or OutcomeFluxNotReconciling if no HelmRelease was
// observed).
func (w *Watcher) Cancel() {
	w.mu.Lock()
	cancel := w.cancel
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// processEvent maps an informer Add/Update event to a state-change Event.
//
// Emission policy — TWO conditions cause a dispatch:
//
//  1. FIRST OBSERVATION: this Watcher has never observed `componentID`
//     before (`prev == ""` because the map default is the empty string).
//     We ALWAYS dispatch in this case so the downstream Subscribe stream
//     and openova-flow-server job state machine learn about every HR
//     the cluster currently has — including HRs that are ALREADY
//     Ready=True at attach time (the Pod-restart resume path: the
//     previous catalyst-api Pod died after the HR converged, the new
//     Pod's informer initial-list returns the HR in `installed` state,
//     and the bridge MUST receive a terminal Succeeded event so the
//     mothership Jobs.Status stops reporting "running" / "failed").
//
//  2. TRANSITION: the derived state differs from the last-seen state.
//     This is the steady-state behaviour — the dynamic informer fires
//     UpdateFunc on every status subresource patch (including
//     helm-controller's own observedGeneration touches) and we MUST NOT
//     re-emit at sub-second cadence; the wizard's status pill would
//     flicker.
//
// The first-observation branch is the load-bearing fix for the prov
// t112.omani.works incident (f2e7f02e6ffb6a18, 2026-05-15): 4 HRs
// converged AFTER a catalyst-api Pod restart at 19:16, but the
// mothership Jobs API kept reporting `install-self-sovereign-cutover`,
// `install-powerdns`, `install-catalyst-platform` as `running` and
// `install-sin-2:reloader` as `failed`. The pre-fix
// `if prev != state` path was IMPLICITLY equivalent to "first
// observation OR transition" because `prev == ""` differs from any
// non-empty state — but a future refactor that initialised `w.states`
// pre-emptively (e.g. seeding from a prior snapshot on restart) would
// silently break the first-observation guarantee. Making it explicit
// turns that into a test failure rather than a regression hunt.
//
// The first observed bp-* HelmRelease stamps Watcher.firstSeenAt —
// the terminate-on-all-done gate refuses to consider termination
// until firstSeenAt is non-zero AND len(observed) ≥
// MinBootstrapKitHRs.
//
// Downstream idempotency: the jobs.Bridge's `lastState` map dedupes
// (componentID, state) pairs across re-emissions, so a re-emit of an
// already-Succeeded job is a no-op at the bridge layer. The
// openova-flow-server's TypeSnapshot envelope is idempotent at the
// receiver, so any re-emit propagated by the flow_emitter is also
// safe.
func (w *Watcher) processEvent(obj any, terminated chan struct{}, closeOnce *sync.Once) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return
	}
	name := u.GetName()
	if !strings.HasPrefix(name, "bp-") {
		return
	}
	componentID := ComponentIDFromHelmRelease(name)

	conds, _ := extractConditions(u)
	state := DeriveState(conds)

	// Wave 5.103 (#2447): spec.suspend=true means the operator/cloudinit
	// has intentionally taken this HR out of reconciliation (e.g.,
	// Hetzner-only HRs on a Huawei Sovereign per Wave 5.102). Without
	// this check the HR stays at StatePending forever → the per-Sovereign
	// progress UI shows install-X as "running" indefinitely. Treat
	// suspended HRs as StateInstalled so they don't block Phase 1's
	// "all HRs Ready" readiness check.
	if suspended, ok, _ := unstructured.NestedBool(u.Object, "spec", "suspend"); ok && suspended {
		state = StateInstalled
	}

	level := levelFromState(state)
	message := messageFromConditions(conds, state)

	w.mu.Lock()
	prev, hadPrev := w.states[componentID]
	w.states[componentID] = state
	w.observed[componentID] = struct{}{}
	if w.firstSeenAt.IsZero() {
		w.firstSeenAt = w.cfg.Now()
	}

	// The all-terminal check is gated on informerSynced — an event
	// fired during the initial List MUST NOT close `terminated` even
	// if the alphabetically-first HRs all happen to be Ready=True.
	// The post-WaitForCacheSync block in Watch() owns the first
	// terminal-check after sync; from then on every transition
	// re-evaluates here.
	allTerminal := allObservedTerminal(w.states, w.observed, w.cfg.MinBootstrapKitHRs, w.firstSeenAt, w.informerSynced, w.cfg.ReadySentinelComponent)
	w.mu.Unlock()

	// Dispatch on first observation (this watcher has never seen
	// componentID — `!hadPrev` is true even when state happens to be
	// the empty-string default) OR on a real transition. See the
	// emission-policy comment on this function for the t112.omani.works
	// rationale.
	if !hadPrev || prev != state {
		w.dispatch(provisioner.Event{
			Time:      w.cfg.Now().UTC().Format(time.RFC3339),
			Phase:     PhaseComponent,
			Level:     level,
			Component: componentID,
			State:     state,
			Message:   message,
			// Plumb spec.dependsOn so the bridge can write Job.DependsOn
			// on every event (not only on the seed). Closes the
			// "sibling deps lost on every fresh provision" gap that
			// PR #1431 / PR #1470 worked around at the wrong layer.
			DependsOn: extractDependsOn(u),
		})
	}

	if allTerminal {
		// Emit the operator-visible "ready for handover" message
		// before signalling termination so the SSE consumer renders
		// the transition to ready BEFORE the wizard's poll picks up
		// status=ready and triggers the auto-redirect to the
		// Sovereign Console handover page. maybeEmitReadyTransition
		// is idempotent — at-most-once per Watcher lifetime.
		w.maybeEmitReadyTransition()
		closeOnce.Do(func() { close(terminated) })
	}
}

// maybeEmitReadyTransition emits the operator-visible "Sovereign
// ready for handover" event the first time the watcher converges on
// all-Ready. Idempotent: a follow-up call after the first emit is a
// no-op so a flicker that re-triggers the gate cannot spam the SSE
// buffer with duplicate ready messages.
//
// The level is "info" so the wizard's banner reducer renders it as a
// success milestone (warn/error would surface as red). The phase is
// PhaseComponent so the existing event reducers (wizard log pane,
// banner) pick it up without new wiring; Component is left empty
// because this is a watch-level transition, not a per-component
// state change.
//
// Status flips on the deployment record live in markPhase1Done in
// the handler package — the watcher's only role is to surface the
// transition through the same SSE pipe Phase 0 used.
func (w *Watcher) maybeEmitReadyTransition() {
	w.mu.Lock()
	if w.readyTransitionEmitted {
		w.mu.Unlock()
		return
	}
	w.readyTransitionEmitted = true
	componentCount := len(w.observed)
	w.mu.Unlock()

	w.dispatch(provisioner.Event{
		Time:    w.cfg.Now().UTC().Format(time.RFC3339),
		Phase:   PhaseComponent,
		Level:   "info",
		Message: fmt.Sprintf("All %d blueprints reconciled. Sovereign ready for handover.", componentCount),
	})
}

// allObservedTerminal returns true when ALL of the following hold:
//
//   - synced is true (the dynamic informer's initial List has
//     completed — every HelmRelease that exists at watch-start time
//     is now in the cache, AND
//   - Watcher.firstSeenAt is non-zero (at least one bp-* HelmRelease
//     has been observed in the informer cache), AND
//   - len(observed) ≥ minBootstrapKitHRs (defensive floor — a sane
//     bootstrap-kit ships ≥ 1 HelmRelease; production sets this to
//     1 via the chart default to guard the "Phase-0 succeeded but
//     Flux on the new cluster never started" footgun), AND
//   - readySentinel (when non-empty) has been OBSERVED — #4746: the
//     console's own backend (catalyst-platform) must be present before
//     the platform can be called ready, so a narrow initial List of
//     early HRs cannot fire OutcomeReady before it even exists, AND
//   - every observed component is in terminalStates (installed |
//     failed).
//
// The synced gate is the load-bearing one. Before it fires, AddFunc
// events fired during the initial-List can't close `terminated`
// — otherwise the watcher would exit as soon as the alphabetically
// first batch of HRs landed Ready=True (issue #547 root cause). The
// minBootstrapKitHRs gate stays as a defence-in-depth check for the
// unusual case where the informer's HasSynced returns true with a
// completely empty cache (e.g. flux-system has no HelmReleases yet
// — the bootstrap-kit Kustomization on the new cluster is broken or
// stuck).
//
// otech48 incident (2026-05-03): catalyst-api Deployment shipped
// CATALYST_PHASE1_MIN_BOOTSTRAP_KIT_HRS=38 to patch the early-
// termination bug, but the actual bootstrap-kit had drifted to 37
// HRs — making the count gate permanently unsatisfiable. Sound the
// alarm bell from this comment when reading: never gate on a count
// that can drift independently of the kit. The synced gate is
// drift-proof.
func allObservedTerminal(states map[string]string, observed map[string]struct{}, minBootstrapKitHRs int, firstSeenAt time.Time, synced bool, readySentinel string) bool {
	if !synced {
		return false
	}
	if firstSeenAt.IsZero() {
		return false
	}
	if len(observed) < minBootstrapKitHRs {
		return false
	}
	// #4746 — the console's own backend sentinel (readySentinel, e.g.
	// catalyst-platform) must have been OBSERVED before we may declare the
	// platform done. Without this a narrow initial List that caught only
	// the alphabetically-early HRs (bp-cilium) all Ready=True would fire
	// OutcomeReady while catalyst-platform had not even been created — the
	// slow-2-region false-"failed". The all-observed-terminal loop below
	// then enforces that the sentinel (and every sibling) is in a terminal
	// state; a terminal-FAILED sentinel still trips the gate so a genuinely
	// broken console classifies OutcomeFailed downstream rather than hanging
	// to WatchTimeout. Empty readySentinel disables the check.
	if readySentinel != "" {
		if _, seen := observed[readySentinel]; !seen {
			return false
		}
	}
	for id := range observed {
		if !terminalStates[states[id]] {
			return false
		}
	}
	return true
}

// terminalStatesSnapshot returns a copy of the state map for the caller
// to publish into Deployment.Result.ComponentStates. Holds w.mu.
func (w *Watcher) terminalStatesSnapshot() map[string]string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[string]string, len(w.states))
	for k, v := range w.states {
		out[k] = v
	}
	return out
}

// ComponentIDFromHelmRelease normalises a HelmRelease metadata.name
// ("bp-cilium") to the Sovereign Admin's component id ("cilium").
//
// Bootstrap-kit's bp-catalyst-platform is special: it's the umbrella
// chart for catalyst-platform, so the component id is "catalyst-
// platform". Every other bp-* release strips just the "bp-" prefix.
//
// A name that doesn't start with "bp-" returns the name unchanged —
// the Watcher's filter rejects those before processEvent runs, but
// the function is exported so tests can drive it on arbitrary input.
func ComponentIDFromHelmRelease(name string) string {
	return strings.TrimPrefix(name, "bp-")
}

// extractDependsOn pulls spec.dependsOn[].name from an unstructured
// HelmRelease and returns the sibling AppIDs (with "bp-" prefix
// stripped). Each dependsOn entry's `namespace` field is ignored —
// the bootstrap-kit charts all live in flux-system, and the wizard's
// Flow view shows dependencies by AppID, not namespace-qualified.
//
// Returns nil (NOT empty slice) when no dependsOn array is present
// so the JSON serialiser omits the field cleanly via omitempty.
//
// Schema reference: helm.toolkit.fluxcd.io/v2 HelmRelease.spec.dependsOn
// is `[]CrossNamespaceDependencyReference{ name, namespace? }`.
func extractDependsOn(u *unstructured.Unstructured) []string {
	if u == nil {
		return nil
	}
	raw, found, err := unstructured.NestedSlice(u.Object, "spec", "dependsOn")
	if err != nil || !found || len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, entry := range raw {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		out = append(out, ComponentIDFromHelmRelease(name))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// DeriveState implements the state machine documented on
// provisioner.Event. Exported so tests can drive it on synthetic
// condition slices without spinning a fake informer.
//
// The conditions slice is the HelmRelease's status.conditions, where
// each entry is the standard metav1.Condition shape. The Ready
// condition is the load-bearing one; Reconciling and Released are
// auxiliary signals helm-controller writes alongside.
func DeriveState(conds []metav1.Condition) string {
	ready := findCondition(conds, "Ready")
	if ready == nil {
		return StatePending
	}

	switch ready.Status {
	case metav1.ConditionTrue:
		return StateInstalled

	case metav1.ConditionUnknown:
		return StateInstalling

	case metav1.ConditionFalse:
		// Differentiate "waiting on a dependency" (still pending,
		// not failed) from "install actually broke" (failed).
		if isDependencyMessage(ready.Message) {
			return StatePending
		}
		switch ready.Reason {
		case "InstallFailed",
			"UpgradeFailed",
			"ChartPullError",
			"ChartLoadError",
			"ArtifactFailed":
			return StateFailed
		case "Progressing", "ReconcileStarted", "DependencyNotReady":
			return StateInstalling
		}
		// Fallback: a Ready=False without a reason we recognise as
		// "still working" is degraded — flux flips it to True again
		// once the underlying deployment recovers.
		return StateDegraded
	}

	return StatePending
}

// IsStalledHelmRelease reports whether a HelmRelease has STABLY failed —
// Flux's remediation.retries are exhausted and helm-controller will NOT
// auto-reconcile again without a spec change or manual reconcile. Flux
// signals this with the Stalled=True condition (and/or a RetriesExceeded
// Ready reason).
//
// This is DISTINCT from DeriveState's StateFailed, which fires the instant
// a transient InstallFailed/UpgradeFailed appears — even while retries
// remain. The live Phase-1 Watcher WANTS that eager StateFailed so its
// late-poll/recovery machinery (issue #910) engages; this helper is for
// the READ-side /jobs projection, which must NOT flap a leaf Failed↔
// Succeeded while a healthy-but-still-retrying HR oscillates (issue #3916).
// A failed-but-not-stalled HR is reported to the jobs page as "installing"
// (in-progress); only a stalled one becomes a terminal Failed leaf.
func IsStalledHelmRelease(u *unstructured.Unstructured) bool {
	conds, _ := extractConditions(u)
	if c := findCondition(conds, "Stalled"); c != nil && c.Status == metav1.ConditionTrue {
		return true
	}
	if ready := findCondition(conds, "Ready"); ready != nil &&
		ready.Status == metav1.ConditionFalse &&
		strings.EqualFold(strings.TrimSpace(ready.Reason), "RetriesExceeded") {
		return true
	}
	return false
}

// isDependencyMessage matches helm-controller's standard "dependency 'X'
// is not ready" message family. We pin on substring rather than reason
// because helm-controller emits Reason=DependencyNotReady AND
// Reason=Reconciling depending on the path — but the message is stable.
func isDependencyMessage(msg string) bool {
	if msg == "" {
		return false
	}
	low := strings.ToLower(msg)
	return strings.Contains(low, "dependency '") && strings.Contains(low, "is not ready")
}

func findCondition(conds []metav1.Condition, t string) *metav1.Condition {
	for i := range conds {
		if conds[i].Type == t {
			return &conds[i]
		}
	}
	return nil
}

// extractConditions reads status.conditions out of an unstructured
// HelmRelease. We use the runtime DefaultUnstructuredConverter rather
// than hand-walking the map so a future field reorder in the v2 API
// doesn't silently break the mapping.
func extractConditions(u *unstructured.Unstructured) ([]metav1.Condition, error) {
	raw, found, err := unstructured.NestedSlice(u.Object, "status", "conditions")
	if err != nil || !found {
		return nil, err
	}
	out := make([]metav1.Condition, 0, len(raw))
	for _, c := range raw {
		cMap, ok := c.(map[string]any)
		if !ok {
			continue
		}
		var cond metav1.Condition
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(cMap, &cond); err != nil {
			continue
		}
		out = append(out, cond)
	}
	return out, nil
}

func levelFromState(state string) string {
	switch state {
	case StateFailed:
		return "error"
	case StateDegraded:
		return "warn"
	default:
		return "info"
	}
}

func messageFromConditions(conds []metav1.Condition, state string) string {
	if ready := findCondition(conds, "Ready"); ready != nil && ready.Message != "" {
		return ready.Message
	}
	switch state {
	case StatePending:
		return "HelmRelease observed, waiting for first reconcile"
	case StateInstalling:
		return "Helm install in progress"
	case StateInstalled:
		return "Helm install complete; Ready=True"
	case StateDegraded:
		return "Ready=False without InstallFailed/UpgradeFailed reason"
	case StateFailed:
		return "Helm install failed"
	}
	return ""
}

// CompileWatchTimeout — small helper for the handler so the
// CATALYST_PHASE1_WATCH_TIMEOUT env-var parse path is testable.
// Returns DefaultWatchTimeout for empty / unparseable input.
func CompileWatchTimeout(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultWatchTimeout
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return DefaultWatchTimeout
	}
	return d
}

// CompileFirstSeenTimeout — env-var parse helper for
// CATALYST_PHASE1_FIRST_SEEN_TIMEOUT. Empty / unparseable / non-positive
// input yields DefaultFirstSeenTimeout.
func CompileFirstSeenTimeout(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultFirstSeenTimeout
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return DefaultFirstSeenTimeout
	}
	return d
}

// CompileMinBootstrapKitHRs — env-var parse helper for
// CATALYST_PHASE1_MIN_BOOTSTRAP_KIT_HRS. Empty / unparseable /
// non-positive input yields DefaultMinBootstrapKitHRs.
func CompileMinBootstrapKitHRs(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultMinBootstrapKitHRs
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return DefaultMinBootstrapKitHRs
	}
	return n
}

// CompileLatePollTimeout — env-var parse helper for
// CATALYST_PHASE1_LATE_POLL_TIMEOUT. Empty / unparseable /
// non-positive input yields DefaultLatePollTimeout. Issue #910.
func CompileLatePollTimeout(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultLatePollTimeout
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return DefaultLatePollTimeout
	}
	return d
}

// CompileLatePollInterval — env-var parse helper for
// CATALYST_PHASE1_LATE_POLL_INTERVAL. Empty / unparseable /
// non-positive input yields DefaultLatePollInterval. Issue #910.
func CompileLatePollInterval(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultLatePollInterval
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return DefaultLatePollInterval
	}
	return d
}

// SnapshotComponents returns the current contents of the informer's
// local cache as a slice of ComponentSnapshot, one per bp-* HelmRelease.
// Filters out non-bp-* names (defence in depth — the FilteringResourceEventHandler
// already drops them, but the cache may still hold non-matching items
// observed via a different code path in future).
//
// Returns an empty slice (not nil) when:
//   - Watch() has not yet been called (informer is nil)
//   - The cache is empty
//   - Watch() has returned and the informer was garbage-collected
//
// Concurrency: safe to call from any goroutine. Each call is O(N) over
// the cache; callers expecting hot polling should debounce.
func (w *Watcher) SnapshotComponents() []ComponentSnapshot {
	w.mu.Lock()
	informer := w.informer
	w.mu.Unlock()
	if informer == nil {
		return []ComponentSnapshot{}
	}
	store := informer.GetStore()
	if store == nil {
		return []ComponentSnapshot{}
	}
	items := store.List()
	out := make([]ComponentSnapshot, 0, len(items))
	for _, it := range items {
		u, ok := it.(*unstructured.Unstructured)
		if !ok {
			continue
		}
		name := u.GetName()
		if !strings.HasPrefix(name, "bp-") {
			continue
		}
		conds, _ := extractConditions(u)
		state := DeriveState(conds)
		// Wave 5.103 (#2447): suspended HRs report as StateInstalled to
		// avoid blocking Phase 1 readiness. See SetEventHandler comment.
		if suspended, ok, _ := unstructured.NestedBool(u.Object, "spec", "suspend"); ok && suspended {
			state = StateInstalled
		}
		message := messageFromConditions(conds, state)
		var lastTransitionAt time.Time
		if ready := findCondition(conds, "Ready"); ready != nil {
			lastTransitionAt = ready.LastTransitionTime.Time
		}
		ns := u.GetNamespace()
		if ns == "" {
			ns = FluxNamespace
		}
		out = append(out, ComponentSnapshot{
			AppID:            ComponentIDFromHelmRelease(name),
			Status:           state,
			HelmReleaseName:  name,
			Namespace:        ns,
			LastTransitionAt: lastTransitionAt.UTC(),
			Message:          message,
			DependsOn:        extractDependsOn(u),
			Stalled:          state == StateFailed && IsStalledHelmRelease(u),
		})
	}
	return out
}

// ListAndSnapshotHelmReleases performs a ONE-SHOT list of bp-* HRs
// in FluxNamespace via the supplied dynamic.Interface and returns the
// equivalent of SnapshotComponents() without spinning up an informer.
//
// Used by the chroot Sovereign-side catalyst-api to seed its own
// per-deployment jobs.Store on first ListJobs/GetJob hit, with
// byte-identical ComponentSnapshot shape so the existing
// snapshotsToSeeds + Bridge.SeedJobsFromInformerList pipeline drops
// in unchanged. The chroot has no posted-back kubeconfig and no
// long-running watcher attached to the imported deployment — it just
// reads the live cluster state on demand.
//
// Returns an empty slice (not nil) when the list is empty.
func ListAndSnapshotHelmReleases(ctx context.Context, dyn dynamic.Interface) ([]ComponentSnapshot, error) {
	if dyn == nil {
		return nil, errors.New("helmwatch: dynamic client is required")
	}
	list, err := dyn.Resource(HelmReleaseGVR).Namespace(FluxNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("helmwatch: list HelmReleases: %w", err)
	}
	out := make([]ComponentSnapshot, 0, len(list.Items))
	for i := range list.Items {
		u := &list.Items[i]
		name := u.GetName()
		if !strings.HasPrefix(name, "bp-") {
			continue
		}
		conds, _ := extractConditions(u)
		state := DeriveState(conds)
		// Wave 5.103 (#2447): suspended HRs report as StateInstalled to
		// avoid blocking Phase 1 readiness. See SetEventHandler comment.
		if suspended, ok, _ := unstructured.NestedBool(u.Object, "spec", "suspend"); ok && suspended {
			state = StateInstalled
		}
		message := messageFromConditions(conds, state)
		var lastTransitionAt time.Time
		if ready := findCondition(conds, "Ready"); ready != nil {
			lastTransitionAt = ready.LastTransitionTime.Time
		}
		ns := u.GetNamespace()
		if ns == "" {
			ns = FluxNamespace
		}
		out = append(out, ComponentSnapshot{
			AppID:            ComponentIDFromHelmRelease(name),
			Status:           state,
			HelmReleaseName:  name,
			Namespace:        ns,
			LastTransitionAt: lastTransitionAt.UTC(),
			Message:          message,
			DependsOn:        extractDependsOn(u),
			Stalled:          state == StateFailed && IsStalledHelmRelease(u),
		})
	}
	return out, nil
}

// Subscribe registers a callback the Watcher invokes for EVERY event
// it emits, in addition to the primary `emit` Emit callback supplied at
// construction. Callbacks fire synchronously on the same goroutine that
// invokes processEvent / fireOnSyncedHooks etc., so a slow subscriber
// blocks the watch — subscribers that need decoupled processing must
// shovel events into their own buffered channel and return immediately.
//
// Issue #695: the jobs.Bridge subscribes here (via the handler) so its
// per-Job state map updates on every per-component HelmRelease
// transition the Watcher observes — independent of whether the SSE
// emit pipeline is wired. This is the runtime complement to
// OnInitialListSynced (which only fires once with the initial-list
// snapshot).
//
// Concurrency: safe to call from any goroutine. Subscribers added
// AFTER Watch has begun emitting events will only see future events
// (the Watcher does not replay the history) — but combined with
// OnInitialListSynced + SnapshotComponents the subscriber can
// reconstruct the current state on attach.
func (w *Watcher) Subscribe(fn func(ev provisioner.Event)) {
	if fn == nil {
		return
	}
	w.mu2.Lock()
	w.runtimeSubscribers = append(w.runtimeSubscribers, fn)
	w.mu2.Unlock()
}

// dispatch routes ev to the primary emit callback AND to every
// runtime subscriber registered via Subscribe. Used by every internal
// event-producing path (processEvent, maybeEmitReadyTransition,
// maybeEmitFirstSeenWarn, the watch-cancel diagnostic, the log-tailer
// disabled warn). Subscribers run synchronously after the primary
// emit so a subscriber that panics cannot prevent the SSE pipeline
// from receiving the event.
//
// The subscriber slice is snapshotted under mu2 and iterated outside
// the lock so a callback that re-enters Subscribe (e.g. a deferred
// resubscribe after seeing a particular event) cannot deadlock.
func (w *Watcher) dispatch(ev provisioner.Event) {
	w.emit(ev)

	w.mu2.Lock()
	subs := make([]func(provisioner.Event), len(w.runtimeSubscribers))
	copy(subs, w.runtimeSubscribers)
	w.mu2.Unlock()

	for _, fn := range subs {
		fn(ev)
	}
}

// OnInitialListSynced registers a callback the Watcher will invoke
// exactly once with the SnapshotComponents() result at the moment
// WaitForCacheSync first returns true. This is the canonical hook the
// handler uses to call jobs.Bridge.SeedJobsFromInformerList immediately
// after every helmwatch start — including the resume-after-restart
// path AND the on-demand POST /refresh-watch flow.
//
// If the sync has ALREADY fired by the time this method is called
// (e.g. a late subscriber registering after Watch() has been running
// for a while), the hook fires synchronously with the current cache
// contents. This makes the call order-independent at the handler.
//
// Concurrency: safe to call from any goroutine. The handler invokes
// it once per new Watcher under dep.mu so there is no realistic race
// between subscribe and Sync — but the implementation defends against
// it anyway.
func (w *Watcher) OnInitialListSynced(hook func([]ComponentSnapshot)) {
	if hook == nil {
		return
	}
	w.mu2.Lock()
	if w.syncedOnce {
		w.mu2.Unlock()
		hook(w.SnapshotComponents())
		return
	}
	w.onSyncedHooks = append(w.onSyncedHooks, hook)
	w.mu2.Unlock()
}

// fireOnSyncedHooks drains the registered post-sync hooks under w.mu2
// and invokes each with the current SnapshotComponents result. Marks
// syncedOnce so any subsequent OnInitialListSynced subscriber fires
// synchronously. Idempotent — calling fireOnSyncedHooks twice on the
// same Watcher is a no-op (the second call sees an empty hook slice
// and syncedOnce already true).
func (w *Watcher) fireOnSyncedHooks() {
	w.mu2.Lock()
	hooks := w.onSyncedHooks
	w.onSyncedHooks = nil
	w.syncedOnce = true
	w.mu2.Unlock()

	if len(hooks) == 0 {
		return
	}
	snap := w.SnapshotComponents()
	for _, h := range hooks {
		h(snap)
	}
}

