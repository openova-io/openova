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
//     Kustomization, which materialises the canonical Phase-0 HelmReleases
//     (bp-cilium, bp-cert-manager, bp-flux, bp-crossplane, bp-spire,
//     bp-nats-jetstream, bp-openbao, bp-external-secrets, bp-keycloak,
//     bp-gitea, bp-cnpg, bp-powerdns, bp-external-dns, bp-crossplane-claims,
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
// because it depends on Crossplane CRDs settling. 60 minutes is the
// upper bound observed in DoD runs against omantel.omani.works; the
// median is closer to 8 minutes.
const DefaultWatchTimeout = 60 * time.Minute

// MinComponentCount — the bootstrap-kit ships exactly 11 bp-* HelmReleases
// (clusters/_template/bootstrap-kit/01-cilium → 11-bp-catalyst-platform).
// The Watch terminates when all OBSERVED HelmReleases reach terminal
// state, not when N have appeared, but tests assert this constant so
// any future drift in the kit count surfaces here too.
const MinComponentCount = 11

// DefaultMinBootstrapKitHRs — the lower bound on number of bp-*
// HelmReleases that must have appeared in the informer cache before
// the terminate-on-all-done check is even considered. This is the
// bug-fix gate for omantel-class deployments where the watcher used
// to exit "ready" one second after flux-bootstrap because zero HRs
// were yet reconciled (Flux hadn't materialised the bootstrap-kit
// Kustomization on the new cluster).
//
// Operator override: CATALYST_PHASE1_MIN_BOOTSTRAP_KIT_HRS. A future
// bootstrap-kit that ships more or fewer components only needs this
// env flipped on the catalyst-api Deployment — no code change
// required.
const DefaultMinBootstrapKitHRs = 11

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

	// FirstSeenTimeout — duration after watch start during which the
	// Watcher waits for the FIRST bp-* HelmRelease. If zero HRs are
	// observed within this window, a single warn event fires and the
	// watch CONTINUES (HRs may still appear). Defaults to
	// DefaultFirstSeenTimeout (15m). The catalyst-api wires
	// CATALYST_PHASE1_FIRST_SEEN_TIMEOUT into this.
	FirstSeenTimeout time.Duration
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
			w.emit(provisioner.Event{
				Time:    w.cfg.Now().UTC().Format(time.RFC3339),
				Phase:   PhaseComponent,
				Level:   "warn",
				Message: "helm-controller log tailing disabled: build core client: " + err.Error(),
			})
		} else {
			tailer := newLogTailer(core, w.emit, w.cfg.Now)
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
			final := w.terminalStatesSnapshot()
			w.setOutcome(w.classifyOutcomeOnTerminate(final))
			return final, nil

		case <-watchCtx.Done():
			// Timeout or caller cancel — emit a single warn event so
			// the Sovereign Admin can render "watch ended (timeout):
			// X of Y installed" rather than going silent.
			w.emit(provisioner.Event{
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

	w.emit(provisioner.Event{
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

// processEvent maps an informer Add/Update event to a state-change Event.
//
// We emit ONLY on transitions: if the component's last-seen state
// equals the derived current state, no event flows. This matters
// because dynamic informers fire UpdateFunc on every status subresource
// patch (including helm-controller's own observedGeneration touches),
// and the Sovereign Admin's status pill should not flicker at sub-
// second cadence.
//
// The first observed bp-* HelmRelease stamps Watcher.firstSeenAt —
// the terminate-on-all-done gate refuses to consider termination
// until firstSeenAt is non-zero AND len(observed) ≥
// MinBootstrapKitHRs.
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
	level := levelFromState(state)
	message := messageFromConditions(conds, state)

	w.mu.Lock()
	prev := w.states[componentID]
	w.states[componentID] = state
	w.observed[componentID] = struct{}{}
	if w.firstSeenAt.IsZero() {
		w.firstSeenAt = w.cfg.Now()
	}

	allTerminal := allObservedTerminal(w.states, w.observed, w.cfg.MinBootstrapKitHRs, w.firstSeenAt)
	w.mu.Unlock()

	if prev != state {
		w.emit(provisioner.Event{
			Time:      w.cfg.Now().UTC().Format(time.RFC3339),
			Phase:     PhaseComponent,
			Level:     level,
			Component: componentID,
			State:     state,
			Message:   message,
		})
	}

	if allTerminal {
		closeOnce.Do(func() { close(terminated) })
	}
}

// allObservedTerminal returns true when:
//
//   - Watcher.firstSeenAt is non-zero (at least one bp-* HelmRelease
//     has been observed in the informer cache), AND
//   - len(observed) ≥ minBootstrapKitHRs (the watch has seen at least
//     the documented bootstrap-kit count, so it can't be fooled by a
//     partial early reconcile), AND
//   - every observed component is in terminalStates (installed |
//     failed).
//
// All three are required. The first two together close the
// "informer hadn't seen the bootstrap-kit yet" bug surfaced on
// omantel where a watch returned ready 1s after flux-bootstrap
// because zero HRs had reconciled.
func allObservedTerminal(states map[string]string, observed map[string]struct{}, minBootstrapKitHRs int, firstSeenAt time.Time) bool {
	if firstSeenAt.IsZero() {
		return false
	}
	if len(observed) < minBootstrapKitHRs {
		return false
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
		})
	}
	return out
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

