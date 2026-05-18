// Package idlescaler hosts the IdleScaler — the Wave 10 (PR #1641 follow-up)
// goroutine that scales pty-server StatefulSets to 0 replicas after the
// configured idle window has elapsed (architecture.md §1 idle policy).
//
// PR #1641 shipped the `openova.io/sandbox-idle-timeout-minutes`
// annotation on every pty-server StatefulSet but no controller was
// reading it. This package closes that loop:
//
//  1. Every Interval (default 60s) the IdleScaler lists every
//     StatefulSet labeled `app.kubernetes.io/component=pty-server` AND
//     `openova.io/managed-by=catalyst` across all `sandbox-*` namespaces
//     visible to the controller's client.
//
//  2. For each StatefulSet, it reads the idle-timeout annotation. If
//     absent or unparseable, it falls back to the controller-level
//     default (typically env SANDBOX_IDLE_TIMEOUT_MINUTES, 30 min).
//
//  3. It polls the StatefulSet's pty-server Service at
//     `http://pty-server.<ns>.svc.cluster.local:7681/idle` (the Service
//     name + port are written by the renderer in
//     core/controllers/sandbox/internal/gitops/manifests.go) — the
//     handler is contributed by products/sandbox/pty-server/internal/
//     server/routes.go and returns the in-memory lastActivityAt +
//     activeSessions counters.
//
//  4. It stamps `openova.io/sandbox-last-activity-at` (RFC3339) onto
//     the StatefulSet so a future operator inspecting `kubectl get
//     statefulset -o yaml` can see what the scaler observed.
//
//  5. If `now - lastActivityAt > idleTimeout` AND activeSessions == 0
//     AND spec.replicas > 0, the IdleScaler patches spec.replicas = 0.
//     The Sandbox reconciler will bump replicas back to
//     spec.quota.concurrentSessions the next time anything touches the
//     parent Sandbox CR (a tab connect, a session create, a CR edit).
//
// Mutation scope: the IdleScaler ONLY ever scales pty-server
// StatefulSets — its OWN managed resource (architecture.md §7 — those
// StatefulSets are written by the sandbox-controller renderer). It
// never patches anything outside the `sandbox-*` namespace + the
// `app.kubernetes.io/component=pty-server` label.
package idlescaler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// LabelComponent is the StatefulSet label the renderer writes on
	// every pty-server StatefulSet (manifests.go ptyServerStatefulSetTemplate).
	LabelComponent = "app.kubernetes.io/component"
	// LabelManagedBy is the secondary safety filter — we never touch
	// a StatefulSet that didn't come from us.
	LabelManagedBy = "openova.io/managed-by"
	// ComponentValue is the LabelComponent value we filter on.
	ComponentValue = "pty-server"
	// ManagedByValue is the LabelManagedBy value we filter on.
	ManagedByValue = "catalyst"

	// AnnIdleTimeoutMinutes — set by the renderer (manifests.go
	// `openova.io/sandbox-idle-timeout-minutes`). Per-StatefulSet
	// override of the controller-level default.
	AnnIdleTimeoutMinutes = "openova.io/sandbox-idle-timeout-minutes"
	// AnnLastActivityAt — written by the IdleScaler. RFC3339 (UTC).
	// External observers (operators, dashboards) can read this without
	// hitting the pty-server endpoint directly.
	AnnLastActivityAt = "openova.io/sandbox-last-activity-at"

	// NamespacePrefix limits the scaler to namespaces the renderer
	// creates (`sandbox-<owner-uid>`). Any StatefulSet that somehow
	// carries our labels outside this prefix is ignored.
	NamespacePrefix = "sandbox-"

	// PtyServicePort mirrors the renderer's ptyServerServiceTemplate
	// (port 7681). If that constant changes, this must change too.
	PtyServicePort = 7681
	// IdlePath is the endpoint exposed by pty-server (routes.go).
	IdlePath = "/idle"
)

// idleDTO mirrors products/sandbox/pty-server/internal/server/routes.go
// idleDTO. Kept local (no cross-module type import) — both sides change
// in the same PR per the architecture-doc cross-reference idiom.
type idleDTO struct {
	LastActivityAt time.Time `json:"lastActivityAt"`
	ActiveSessions int       `json:"activeSessions"`
}

// Options configures the IdleScaler.
type Options struct {
	// Interval is the poll cadence. Defaults to 60s.
	Interval time.Duration
	// DefaultIdleTimeoutMinutes is the fallback when a StatefulSet has
	// no idle-timeout annotation (or it's unparseable). The controller
	// already plumbs SANDBOX_IDLE_TIMEOUT_MINUTES through env — pass
	// the same value here so behaviour is consistent.
	DefaultIdleTimeoutMinutes int
	// HTTPTimeout bounds a single /idle probe. Defaults to 5s.
	HTTPTimeout time.Duration
	// HTTPClient is injectable for tests. Defaults to http.DefaultClient
	// with HTTPTimeout applied.
	HTTPClient *http.Client
	// ProbeURL is injectable for tests. nil = use cluster-DNS form
	// `http://pty-server.<ns>.svc.cluster.local:7681/idle`.
	ProbeURL func(namespace string) string
	// Now is injectable for tests. Defaults to time.Now().UTC.
	Now func() time.Time
}

// Scaler is the IdleScaler runtime. Construct via New, register with
// the controller-runtime manager via mgr.Add(s) (Scaler implements
// manager.Runnable + manager.LeaderElectionRunnable so only the
// elected leader scales — peers stay idle).
type Scaler struct {
	client client.Client
	log    logr.Logger

	interval       time.Duration
	defaultTimeout time.Duration
	httpClient     *http.Client
	probeURL       func(string) string
	now            func() time.Time
}

// New returns a Scaler ready to register with a controller-runtime manager.
func New(c client.Client, log logr.Logger, opts Options) *Scaler {
	if opts.Interval <= 0 {
		opts.Interval = 60 * time.Second
	}
	if opts.DefaultIdleTimeoutMinutes <= 0 {
		opts.DefaultIdleTimeoutMinutes = 30
	}
	if opts.HTTPTimeout <= 0 {
		opts.HTTPTimeout = 5 * time.Second
	}
	httpc := opts.HTTPClient
	if httpc == nil {
		httpc = &http.Client{Timeout: opts.HTTPTimeout}
	}
	probe := opts.ProbeURL
	if probe == nil {
		probe = func(ns string) string {
			return fmt.Sprintf("http://pty-server.%s.svc.cluster.local:%d%s",
				ns, PtyServicePort, IdlePath)
		}
	}
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Scaler{
		client:         c,
		log:            log,
		interval:       opts.Interval,
		defaultTimeout: time.Duration(opts.DefaultIdleTimeoutMinutes) * time.Minute,
		httpClient:     httpc,
		probeURL:       probe,
		now:            now,
	}
}

// Start runs the scaler loop until ctx is cancelled. Satisfies
// controller-runtime's manager.Runnable interface — register via
// `mgr.Add(scaler)`.
func (s *Scaler) Start(ctx context.Context) error {
	s.log.Info("idle-scaler starting",
		"interval", s.interval,
		"default_timeout", s.defaultTimeout)

	// Tick once on startup so we don't wait `interval` before the
	// first reconciliation pass.
	if err := s.runOnce(ctx); err != nil {
		s.log.Error(err, "idle-scaler initial pass failed (non-fatal — will retry)")
	}

	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			s.log.Info("idle-scaler shutting down")
			return nil
		case <-t.C:
			if err := s.runOnce(ctx); err != nil {
				s.log.Error(err, "idle-scaler tick failed (non-fatal — will retry)")
			}
		}
	}
}

// NeedLeaderElection makes the scaler a singleton across HA replicas.
// Peers stay idle so we never race a scale-to-zero against a scale-back-up.
func (s *Scaler) NeedLeaderElection() bool { return true }

// runOnce is one IdleScaler pass.
func (s *Scaler) runOnce(ctx context.Context) error {
	sel, err := labels.Parse(fmt.Sprintf("%s=%s,%s=%s",
		LabelComponent, ComponentValue,
		LabelManagedBy, ManagedByValue))
	if err != nil {
		return fmt.Errorf("build label selector: %w", err)
	}

	var list appsv1.StatefulSetList
	if err := s.client.List(ctx, &list, &client.ListOptions{LabelSelector: sel}); err != nil {
		return fmt.Errorf("list pty-server statefulsets: %w", err)
	}

	now := s.now()
	scanned := 0
	idled := 0
	for i := range list.Items {
		ss := &list.Items[i]
		if !strings.HasPrefix(ss.Namespace, NamespacePrefix) {
			// Defence in depth — we never touch SS outside
			// `sandbox-*` even if a stray label leaked elsewhere.
			continue
		}
		scanned++
		if didScale, err := s.processOne(ctx, ss, now); err != nil {
			s.log.Error(err, "idle-scaler: process statefulset failed",
				"namespace", ss.Namespace, "name", ss.Name)
			continue
		} else if didScale {
			idled++
		}
	}
	s.log.V(1).Info("idle-scaler pass done", "scanned", scanned, "idled", idled)
	return nil
}

// processOne returns (didScale, err). didScale is true if this pass
// scaled spec.replicas to 0.
func (s *Scaler) processOne(ctx context.Context, ss *appsv1.StatefulSet, now time.Time) (bool, error) {
	log := s.log.WithValues("namespace", ss.Namespace, "name", ss.Name)

	timeout := s.timeoutFor(ss)

	// Probe the in-cluster service for the in-memory activity counter.
	dto, probeErr := s.probe(ctx, ss.Namespace)

	// Decide the canonical lastActivity to stamp.
	//
	// Probe-success path: trust the live counter.
	// Probe-failure path: keep the existing annotation (don't reset)
	//                     — a service may be unreachable briefly during
	//                     Pod restart; the next tick will catch up.
	var lastActivity time.Time
	var activeSessions int
	if probeErr == nil {
		lastActivity = dto.LastActivityAt.UTC()
		activeSessions = dto.ActiveSessions
	} else {
		// Existing annotation as fallback.
		if existing, ok := ss.Annotations[AnnLastActivityAt]; ok {
			if t, perr := time.Parse(time.RFC3339, existing); perr == nil {
				lastActivity = t.UTC()
			}
		}
		// If we have neither a probe nor a prior annotation, we
		// can't make an idle decision yet. Skip — next tick.
		if lastActivity.IsZero() {
			log.V(1).Info("idle-scaler: probe failed and no prior annotation, skipping",
				"err", probeErr.Error())
			return false, nil
		}
		log.V(1).Info("idle-scaler: probe failed, using prior annotation",
			"last_activity", lastActivity.Format(time.RFC3339),
			"err", probeErr.Error())
	}

	// Stamp the annotation (probe-success only — otherwise we'd
	// overwrite a stale value with the same stale value, which is a
	// no-op patch but still chatty).
	if probeErr == nil {
		if err := s.stampAnnotation(ctx, ss, lastActivity); err != nil {
			log.Error(err, "idle-scaler: stamp last-activity annotation failed")
			// non-fatal — fall through to the scale decision so a
			// degraded annotation-patch path can't keep a Pod alive
			// forever.
		}
	}

	if activeSessions > 0 {
		// Sessions are open; never scale down even if lastActivity
		// drifts (lastActivity covers the trailing edge after the
		// last WS frame — Touch() also fires on attach/detach).
		return false, nil
	}

	idleFor := now.Sub(lastActivity)
	if idleFor < timeout {
		log.V(1).Info("idle-scaler: not yet idle",
			"idle_for", idleFor.String(),
			"timeout", timeout.String())
		return false, nil
	}

	// Already scaled to zero? Skip the patch — idempotent.
	if ss.Spec.Replicas != nil && *ss.Spec.Replicas == 0 {
		return false, nil
	}

	log.Info("idle-scaler: scaling pty-server to 0",
		"idle_for", idleFor.String(),
		"timeout", timeout.String(),
		"last_activity", lastActivity.Format(time.RFC3339))
	if err := s.scaleToZero(ctx, ss); err != nil {
		return false, fmt.Errorf("scale to zero: %w", err)
	}
	// Wave 15 (PR #1674 follow-up) — emit the canonical idle-timeout
	// counter so the Grafana "Idle-Timeout Scale-Down Events / hour"
	// panel ticks. Labelled by namespace to match the dashboard's
	// `sum by (namespace) (rate(...))` aggregation.
	idleTimeoutEvents.WithLabelValues(ss.Namespace).Inc()
	return true, nil
}

func (s *Scaler) timeoutFor(ss *appsv1.StatefulSet) time.Duration {
	if v, ok := ss.Annotations[AnnIdleTimeoutMinutes]; ok {
		// The renderer writes the annotation as a quoted integer
		// (manifests.go uses {{ .IdleTimeoutMinutes | quote }}); the
		// API server stores annotations verbatim so we get the raw
		// string back. strconv.Atoi handles the unquoted form; we
		// trim quotes defensively in case operators hand-edited.
		v = strings.Trim(strings.TrimSpace(v), "\"'")
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Minute
		}
	}
	return s.defaultTimeout
}

func (s *Scaler) probe(ctx context.Context, namespace string) (idleDTO, error) {
	url := s.probeURL(namespace)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return idleDTO{}, fmt.Errorf("build request: %w", err)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return idleDTO{}, fmt.Errorf("%s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return idleDTO{}, fmt.Errorf("%s: status %d", url, resp.StatusCode)
	}
	var dto idleDTO
	if err := json.NewDecoder(resp.Body).Decode(&dto); err != nil {
		return idleDTO{}, fmt.Errorf("decode %s body: %w", url, err)
	}
	return dto, nil
}

func (s *Scaler) stampAnnotation(ctx context.Context, ss *appsv1.StatefulSet, lastActivity time.Time) error {
	stamp := lastActivity.UTC().Format(time.RFC3339)
	if existing, ok := ss.Annotations[AnnLastActivityAt]; ok && existing == stamp {
		// Already up-to-date (typical when probe + last poll agree
		// within a second). Skip the patch.
		return nil
	}

	// JSON-Merge-Patch body — only the annotation we care about.
	// strategic-merge-patch over annotations is equivalent here since
	// metav1.ObjectMeta.Annotations is a map (additive merge).
	patch := []byte(fmt.Sprintf(
		`{"metadata":{"annotations":{%q:%q}}}`,
		AnnLastActivityAt, stamp))

	if err := s.client.Patch(ctx, ss, client.RawPatch(client.Merge.Type(), patch)); err != nil {
		if apierrors.IsNotFound(err) {
			// The StatefulSet was deleted between List and Patch —
			// nothing to do.
			return nil
		}
		return err
	}
	// Reflect the new value in our in-memory copy too so subsequent
	// runOnce passes within this process don't re-stamp.
	if ss.Annotations == nil {
		ss.Annotations = map[string]string{}
	}
	ss.Annotations[AnnLastActivityAt] = stamp
	return nil
}

func (s *Scaler) scaleToZero(ctx context.Context, ss *appsv1.StatefulSet) error {
	patch := []byte(`{"spec":{"replicas":0}}`)
	if err := s.client.Patch(ctx, ss, client.RawPatch(client.Merge.Type(), patch)); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	// Keep our local copy consistent for the rest of this pass.
	var zero int32 = 0
	ss.Spec.Replicas = &zero
	return nil
}

