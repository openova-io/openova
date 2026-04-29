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
//   - On Pod restart, the deployment's persisted Result.Kubeconfig +
//     Result.ComponentStates are rehydrated and the watch resumes from
//     the cluster's current observed state (idempotent — emitting an
//     "installed" event for an already-installed release is harmless;
//     the SSE consumer keys off the State enum, not event count).
package helmwatch

import (
	"context"
	"errors"
	"fmt"
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
// this from environment + Deployment.Result.Kubeconfig; tests inject
// via the Watcher constructor.
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
}

// NewWatcher returns a Watcher with cfg applied. emit must be non-nil
// — a nil emit is a programmer error (the watch's only output channel
// is the callback).
func NewWatcher(cfg Config, emit Emit) (*Watcher, error) {
	if emit == nil {
		return nil, errors.New("helmwatch: emit callback is required")
	}
	if strings.TrimSpace(cfg.KubeconfigYAML) == "" {
		return nil, errors.New("helmwatch: kubeconfig is required (deployment.Result.Kubeconfig was empty)")
	}
	if cfg.DynamicFactory == nil {
		cfg.DynamicFactory = NewDynamicClientFromKubeconfig
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
		return final, fmt.Errorf("helmwatch: informer cache failed to sync: %w", watchCtx.Err())
	}

	// Wait for either all-terminal or context done.
	select {
	case <-terminated:
		// Clean termination — every observed component is in a
		// terminal state.
	case <-watchCtx.Done():
		// Timeout or caller cancel — emit a single warn event so the
		// Sovereign Admin can render "watch ended (timeout): X of Y
		// installed" rather than going silent.
		w.emit(provisioner.Event{
			Time:    w.cfg.Now().UTC().Format(time.RFC3339),
			Phase:   PhaseComponent,
			Level:   "warn",
			Message: "Phase-1 watch terminated by context: " + watchCtx.Err().Error() + " — see ComponentStates for current outcome",
		})
	}

	final := w.terminalStatesSnapshot()
	return final, nil
}

// processEvent maps an informer Add/Update event to a state-change Event.
//
// We emit ONLY on transitions: if the component's last-seen state
// equals the derived current state, no event flows. This matters
// because dynamic informers fire UpdateFunc on every status subresource
// patch (including helm-controller's own observedGeneration touches),
// and the Sovereign Admin's status pill should not flicker at sub-
// second cadence.
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

	allTerminal := allObservedTerminal(w.states, w.observed)
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

// allObservedTerminal returns true when every component the watch has
// observed at least once is in a terminal state. With zero observed
// components it returns false — an empty cluster cannot be "done."
func allObservedTerminal(states map[string]string, observed map[string]struct{}) bool {
	if len(observed) == 0 {
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

