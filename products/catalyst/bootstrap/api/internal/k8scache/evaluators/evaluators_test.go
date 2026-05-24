// evaluators_test.go — unit tests for the 5 evaluators + the engine.
//
// EPIC-1 (#1096) Slice W2.
//
// Coverage matrix per evaluator:
//
//	hpa     : pass (HPA satisfies floor) + fail (currentReplicas < min)
//	          + skip (no HPA) + skip (Job-owned Pod)
//	otel    : pass (sidecar) + pass (auto-inject + Instrumentation CR)
//	          + fail (no sidecar, no annotation) + fail (annotation but no CR)
//	hubble  : skip (Hubble disabled) + pass (FlowsSeen=true)
//	          + fail (FlowsSeen=false) + warn (Probe error)
//	harbor  : pass (Harbor-prefixed) + pass (allowed-prefix)
//	          + fail (docker.io) + skip (HarborDomain empty)
//	          + skip (no containers)
//	flux    : pass (managed-by label) + pass (HelmRelease ownerRef)
//	          + pass (controller is Flux-owned) + fail (neither)
//
// Engine:
//   - subscribe + tick path emits events to the Publisher
//   - resolveSnapshot error path is logged + suppressed
//   - cancellation cleanly shuts the engine down
//
// Plus 1 EvaluateAll concatenation test.
package evaluators

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
)

// quietLogger discards log output.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// fakeSnapshot is an in-memory Snapshot keyed by canonical kind name.
type fakeSnapshot struct {
	by map[string][]*unstructured.Unstructured
}

func (f *fakeSnapshot) List(kind string, _ labels.Selector) ([]*unstructured.Unstructured, error) {
	if f.by == nil {
		return nil, nil
	}
	v, ok := f.by[kind]
	if !ok {
		// Mirror the production Factory.List behaviour — unknown
		// kind returns an error (the evaluator must skip rather
		// than crash).
		return nil, errKindNotRegistered
	}
	return v, nil
}

var errKindNotRegistered = errors.New("kind not registered")

// recorder is a Publisher that captures every emitted (cluster, report) pair.
type recorder struct {
	mu      sync.Mutex
	entries []recordedEntry
}

type recordedEntry struct {
	cluster string
	report  SyntheticReport
}

func (r *recorder) Publish(cluster string, report SyntheticReport) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, recordedEntry{cluster: cluster, report: report})
}

func (r *recorder) Snapshot() []recordedEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedEntry, len(r.entries))
	copy(out, r.entries)
	return out
}

// ── builders ─────────────────────────────────────────────────────

func uPod(ns, name string, opts ...podOpt) *unstructured.Unstructured {
	p := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"namespace": ns,
			"name":      name,
			"uid":       "uid-" + ns + "-" + name,
		},
		"spec": map[string]any{
			"containers": []any{},
		},
	}}
	for _, o := range opts {
		o(p)
	}
	return p
}

type podOpt func(*unstructured.Unstructured)

func withOwner(kind, name string) podOpt {
	return func(p *unstructured.Unstructured) {
		owners := p.GetOwnerReferences()
		owners = append(owners, metav1.OwnerReference{Kind: kind, Name: name, APIVersion: "apps/v1"})
		p.SetOwnerReferences(owners)
	}
}

func withFluxOwner() podOpt {
	return func(p *unstructured.Unstructured) {
		owners := p.GetOwnerReferences()
		owners = append(owners, metav1.OwnerReference{Kind: "HelmRelease", Name: "x", APIVersion: "helm.toolkit.fluxcd.io/v2"})
		p.SetOwnerReferences(owners)
	}
}

func withLabels(m map[string]string) podOpt {
	return func(p *unstructured.Unstructured) { p.SetLabels(m) }
}

func withAnnotations(m map[string]string) podOpt {
	return func(p *unstructured.Unstructured) { p.SetAnnotations(m) }
}

func withContainerImages(images ...string) podOpt {
	return func(p *unstructured.Unstructured) {
		raw := []any{}
		for i, img := range images {
			raw = append(raw, map[string]any{
				"name":  imageContainerName(i),
				"image": img,
			})
		}
		_ = unstructured.SetNestedSlice(p.Object, raw, "spec", "containers")
	}
}

func imageContainerName(i int) string {
	if i == 0 {
		return "main"
	}
	return "side"
}

func uHPA(ns, name, targetKind, targetName string, minReplicas, currentReplicas int64) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "autoscaling/v2",
		"kind":       "HorizontalPodAutoscaler",
		"metadata": map[string]any{
			"namespace": ns,
			"name":      name,
		},
		"spec": map[string]any{
			"minReplicas": minReplicas,
			"scaleTargetRef": map[string]any{
				"kind": targetKind,
				"name": targetName,
			},
		},
		"status": map[string]any{
			"currentReplicas": currentReplicas,
		},
	}}
}

func uReplicaSet(ns, name, deploymentName string) *unstructured.Unstructured {
	rs := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "ReplicaSet",
		"metadata": map[string]any{
			"namespace": ns,
			"name":      name,
		},
	}}
	if deploymentName != "" {
		rs.SetOwnerReferences([]metav1.OwnerReference{{
			Kind:       "Deployment",
			Name:       deploymentName,
			APIVersion: "apps/v1",
		}})
	}
	return rs
}

func uDeployment(ns, name string, fluxLabel bool) *unstructured.Unstructured {
	d := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]any{
			"namespace": ns,
			"name":      name,
		},
	}}
	if fluxLabel {
		d.SetLabels(map[string]string{"app.kubernetes.io/managed-by": "flux"})
	}
	return d
}

func uInstrumentation(ns, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "opentelemetry.io/v1alpha1",
		"kind":       "Instrumentation",
		"metadata": map[string]any{
			"namespace": ns,
			"name":      name,
		},
	}}
}

// ── HPA evaluator ────────────────────────────────────────────────

func TestHPA_Pass_HPASatisfiesFloor(t *testing.T) {
	hpa := uHPA("acme", "frontend-hpa", "Deployment", "frontend", 3, 5)
	rs := uReplicaSet("acme", "frontend-7c5f", "frontend")
	pod := uPod("acme", "frontend-7c5f-abc", withOwner("ReplicaSet", "frontend-7c5f"))
	snap := &fakeSnapshot{by: map[string][]*unstructured.Unstructured{
		"horizontalpodautoscaler": {hpa},
		"replicaset":              {rs},
	}}
	rep := newHPAEvaluator().Evaluate(context.Background(), snap, pod)
	if len(rep) != 1 {
		t.Fatalf("want 1 row got %d", len(rep))
	}
	if rep[0].Result != ResultPass {
		t.Fatalf("want pass got %s (%s)", rep[0].Result, rep[0].Message)
	}
}

func TestHPA_Fail_CurrentBelowMin(t *testing.T) {
	hpa := uHPA("acme", "frontend-hpa", "Deployment", "frontend", 3, 1)
	rs := uReplicaSet("acme", "frontend-7c5f", "frontend")
	pod := uPod("acme", "frontend-7c5f-abc", withOwner("ReplicaSet", "frontend-7c5f"))
	snap := &fakeSnapshot{by: map[string][]*unstructured.Unstructured{
		"horizontalpodautoscaler": {hpa},
		"replicaset":              {rs},
	}}
	rep := newHPAEvaluator().Evaluate(context.Background(), snap, pod)
	if rep[0].Result != ResultFail {
		t.Fatalf("want fail got %s (%s)", rep[0].Result, rep[0].Message)
	}
	if rep[0].Properties["minReplicas"] != "3" || rep[0].Properties["currentReplicas"] != "1" {
		t.Fatalf("properties not populated: %+v", rep[0].Properties)
	}
}

func TestHPA_Skip_NoHPA(t *testing.T) {
	rs := uReplicaSet("acme", "control-plane-rs", "control-plane")
	pod := uPod("acme", "control-plane-pod", withOwner("ReplicaSet", "control-plane-rs"))
	snap := &fakeSnapshot{by: map[string][]*unstructured.Unstructured{
		"horizontalpodautoscaler": {},
		"replicaset":              {rs},
	}}
	rep := newHPAEvaluator().Evaluate(context.Background(), snap, pod)
	if rep[0].Result != ResultSkip {
		t.Fatalf("want skip got %s", rep[0].Result)
	}
}

func TestHPA_Skip_JobOwnedPod(t *testing.T) {
	pod := uPod("acme", "batch-pod", withOwner("Job", "nightly-cron"))
	snap := &fakeSnapshot{by: map[string][]*unstructured.Unstructured{}}
	rep := newHPAEvaluator().Evaluate(context.Background(), snap, pod)
	if rep[0].Result != ResultSkip {
		t.Fatalf("want skip for Job-owned Pod got %s", rep[0].Result)
	}
}

func newHPAEvaluator() *HPAEvaluator {
	cfg := Config{Logger: quietLogger()}
	cfg.defaults()
	return NewHPAEvaluator(cfg)
}

// ── OTel evaluator ───────────────────────────────────────────────

func TestOTel_Pass_Sidecar(t *testing.T) {
	pod := uPod("acme", "app", withContainerImages(
		"docker.io/library/nginx:1.25",
		"otel/opentelemetry-collector-contrib:0.95",
	))
	snap := &fakeSnapshot{}
	rep := newOTelEvaluator().Evaluate(context.Background(), snap, pod)
	if rep[0].Result != ResultPass {
		t.Fatalf("want pass got %s", rep[0].Result)
	}
	if rep[0].Properties["detection"] != "sidecar" {
		t.Fatalf("want sidecar detection got %v", rep[0].Properties)
	}
}

func TestOTel_Pass_AutoInjectAnnotation(t *testing.T) {
	pod := uPod("acme", "app",
		withContainerImages("docker.io/library/nginx:1.25"),
		withAnnotations(map[string]string{
			"instrumentation.opentelemetry.io/inject-go": "true",
		}),
	)
	snap := &fakeSnapshot{by: map[string][]*unstructured.Unstructured{
		"instrumentation.opentelemetry.io": {uInstrumentation("acme", "default")},
	}}
	rep := newOTelEvaluator().Evaluate(context.Background(), snap, pod)
	if rep[0].Result != ResultPass {
		t.Fatalf("want pass got %s (%s)", rep[0].Result, rep[0].Message)
	}
	if rep[0].Properties["detection"] != "auto-inject" {
		t.Fatalf("want auto-inject got %v", rep[0].Properties)
	}
}

func TestOTel_Fail_NoSidecarNoAnnotation(t *testing.T) {
	pod := uPod("acme", "app", withContainerImages("docker.io/library/nginx:1.25"))
	snap := &fakeSnapshot{}
	rep := newOTelEvaluator().Evaluate(context.Background(), snap, pod)
	if rep[0].Result != ResultFail {
		t.Fatalf("want fail got %s", rep[0].Result)
	}
}

func TestOTel_Fail_AnnotationButNoInstrumentationCR(t *testing.T) {
	pod := uPod("acme", "app",
		withContainerImages("docker.io/library/nginx:1.25"),
		withAnnotations(map[string]string{
			"instrumentation.opentelemetry.io/inject-go": "true",
		}),
	)
	snap := &fakeSnapshot{by: map[string][]*unstructured.Unstructured{
		"instrumentation.opentelemetry.io": {}, // operator installed, no CR in this ns
	}}
	rep := newOTelEvaluator().Evaluate(context.Background(), snap, pod)
	if rep[0].Result != ResultFail {
		t.Fatalf("want fail (orphan annotation) got %s", rep[0].Result)
	}
	if rep[0].Properties["detection"] != "auto-inject-orphan" {
		t.Fatalf("want auto-inject-orphan got %v", rep[0].Properties)
	}
}

func newOTelEvaluator() *OTelEvaluator {
	cfg := Config{Logger: quietLogger()}
	cfg.defaults()
	return NewOTelEvaluator(cfg)
}

// ── Hubble evaluator ─────────────────────────────────────────────

func TestHubble_Skip_Disabled(t *testing.T) {
	pod := uPod("acme", "app")
	rep := newHubbleEvaluator(false, nil).Evaluate(context.Background(), nil, pod)
	if rep[0].Result != ResultSkip {
		t.Fatalf("want skip when disabled got %s", rep[0].Result)
	}
}

func TestHubble_Pass_FlowsSeen(t *testing.T) {
	pod := uPod("acme", "app")
	probe := &fakeHubbleProbe{seen: true}
	rep := newHubbleEvaluator(true, probe).Evaluate(context.Background(), nil, pod)
	if rep[0].Result != ResultPass {
		t.Fatalf("want pass got %s", rep[0].Result)
	}
}

func TestHubble_Fail_NoFlows(t *testing.T) {
	pod := uPod("acme", "app")
	probe := &fakeHubbleProbe{seen: false}
	rep := newHubbleEvaluator(true, probe).Evaluate(context.Background(), nil, pod)
	if rep[0].Result != ResultFail {
		t.Fatalf("want fail got %s", rep[0].Result)
	}
}

func TestHubble_Warn_ProbeError(t *testing.T) {
	pod := uPod("acme", "app")
	probe := &fakeHubbleProbe{err: errors.New("connection refused")}
	rep := newHubbleEvaluator(true, probe).Evaluate(context.Background(), nil, pod)
	if rep[0].Result != ResultWarn {
		t.Fatalf("want warn got %s", rep[0].Result)
	}
}

type fakeHubbleProbe struct {
	seen bool
	err  error
}

func (f *fakeHubbleProbe) FlowsSeen(_ context.Context, _ *unstructured.Unstructured, _ int64) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.seen, nil
}

func newHubbleEvaluator(enabled bool, probe HubbleProbe) *HubbleEvaluator {
	cfg := Config{Logger: quietLogger(), HubbleEnabled: enabled}
	cfg.defaults()
	e := NewHubbleEvaluator(cfg)
	e.Probe = probe
	return e
}

// ── Harbor evaluator ─────────────────────────────────────────────

func TestHarbor_Pass_HarborPrefixed(t *testing.T) {
	pod := uPod("acme", "app", withContainerImages(
		"harbor.omantel.omani.works/proxy-ghcr/openova-io/openova/website:abc123",
	))
	rep := newHarborEvaluator("harbor.omantel.omani.works", nil).Evaluate(context.Background(), nil, pod)
	if rep[0].Result != ResultPass {
		t.Fatalf("want pass got %s (%s)", rep[0].Result, rep[0].Message)
	}
}

func TestHarbor_Pass_AllowedPrefix(t *testing.T) {
	pod := uPod("acme", "app", withContainerImages(
		"mirror.openova.io/internal/sidecar:1.0",
	))
	rep := newHarborEvaluator("harbor.omantel.omani.works",
		[]string{"mirror.openova.io/"}).Evaluate(context.Background(), nil, pod)
	if rep[0].Result != ResultPass {
		t.Fatalf("want pass got %s", rep[0].Result)
	}
}

func TestHarbor_Fail_DockerHub(t *testing.T) {
	pod := uPod("acme", "app", withContainerImages(
		"harbor.omantel.omani.works/proxy-ghcr/openova-io/openova/api:abc",
		"docker.io/library/redis:7.0",
	))
	rep := newHarborEvaluator("harbor.omantel.omani.works", nil).Evaluate(context.Background(), nil, pod)
	if rep[0].Result != ResultFail {
		t.Fatalf("want fail got %s", rep[0].Result)
	}
	if rep[0].Properties["rejectedImages"] != "docker.io/library/redis:7.0" {
		t.Fatalf("rejectedImages wrong: %v", rep[0].Properties)
	}
}

func TestHarbor_Skip_DomainEmpty(t *testing.T) {
	pod := uPod("acme", "app", withContainerImages("docker.io/library/nginx:1.25"))
	rep := newHarborEvaluator("", nil).Evaluate(context.Background(), nil, pod)
	if rep[0].Result != ResultSkip {
		t.Fatalf("want skip got %s", rep[0].Result)
	}
}

func TestHarbor_Skip_NoContainers(t *testing.T) {
	pod := uPod("acme", "app") // empty containers slice
	rep := newHarborEvaluator("harbor.openova.io", nil).Evaluate(context.Background(), nil, pod)
	if rep[0].Result != ResultSkip {
		t.Fatalf("want skip got %s", rep[0].Result)
	}
}

func newHarborEvaluator(domain string, allowed []string) *HarborEvaluator {
	cfg := Config{
		Logger:                quietLogger(),
		HarborDomain:          domain,
		HarborAllowedPrefixes: allowed,
	}
	cfg.defaults()
	return NewHarborEvaluator(cfg)
}

// ── Flux evaluator ───────────────────────────────────────────────

func TestFlux_Pass_LabelOnPod(t *testing.T) {
	pod := uPod("acme", "app", withLabels(map[string]string{
		"app.kubernetes.io/managed-by": "flux",
	}))
	snap := &fakeSnapshot{}
	rep := newFluxEvaluator().Evaluate(context.Background(), snap, pod)
	if rep[0].Result != ResultPass {
		t.Fatalf("want pass got %s", rep[0].Result)
	}
}

func TestFlux_Pass_HelmReleaseOwnerRef(t *testing.T) {
	pod := uPod("acme", "app", withFluxOwner())
	snap := &fakeSnapshot{}
	rep := newFluxEvaluator().Evaluate(context.Background(), snap, pod)
	if rep[0].Result != ResultPass {
		t.Fatalf("want pass got %s", rep[0].Result)
	}
}

func TestFlux_Pass_ControllerIsFluxOwned(t *testing.T) {
	dep := uDeployment("acme", "frontend", true)
	rs := uReplicaSet("acme", "frontend-7c5f", "frontend")
	pod := uPod("acme", "frontend-pod", withOwner("ReplicaSet", "frontend-7c5f"))
	snap := &fakeSnapshot{by: map[string][]*unstructured.Unstructured{
		"deployment": {dep},
		"replicaset": {rs},
	}}
	rep := newFluxEvaluator().Evaluate(context.Background(), snap, pod)
	if rep[0].Result != ResultPass {
		t.Fatalf("want pass got %s (%s)", rep[0].Result, rep[0].Message)
	}
	if rep[0].Properties["detection"] != "controller-flux-owned" {
		t.Fatalf("want controller-flux-owned detection got %v", rep[0].Properties)
	}
}

func TestFlux_Fail_NoLabelNoOwnerRef(t *testing.T) {
	dep := uDeployment("acme", "frontend", false)
	rs := uReplicaSet("acme", "frontend-7c5f", "frontend")
	pod := uPod("acme", "frontend-pod", withOwner("ReplicaSet", "frontend-7c5f"))
	snap := &fakeSnapshot{by: map[string][]*unstructured.Unstructured{
		"deployment": {dep},
		"replicaset": {rs},
	}}
	rep := newFluxEvaluator().Evaluate(context.Background(), snap, pod)
	if rep[0].Result != ResultFail {
		t.Fatalf("want fail got %s", rep[0].Result)
	}
}

func newFluxEvaluator() *FluxEvaluator {
	cfg := Config{Logger: quietLogger()}
	cfg.defaults()
	return NewFluxEvaluator(cfg)
}

// ── EvaluateAll concatenation ────────────────────────────────────

func TestEvaluateAll_ConcatenatesAndPreservesOrder(t *testing.T) {
	pod := uPod("acme", "app", withContainerImages("harbor.openova.io/proxy/x:1"))
	snap := &fakeSnapshot{}

	cfg := Config{Logger: quietLogger(), HarborDomain: "harbor.openova.io"}
	cfg.defaults()
	evals := []Evaluator{
		NewHarborEvaluator(cfg),
		NewFluxEvaluator(cfg),
	}
	rows := EvaluateAll(context.Background(), snap, pod, evals)
	if len(rows) != 2 {
		t.Fatalf("want 2 rows got %d", len(rows))
	}
	if rows[0].Policy != "harbor-proxy-pull" || rows[1].Policy != "flux-managed" {
		t.Fatalf("order not preserved: %v %v", rows[0].Policy, rows[1].Policy)
	}
}

func TestEvaluateAll_SkipsNonPodTargets(t *testing.T) {
	dep := uDeployment("acme", "x", false)
	cfg := Config{Logger: quietLogger(), HarborDomain: "harbor.openova.io"}
	cfg.defaults()
	rows := EvaluateAll(context.Background(), &fakeSnapshot{}, dep, []Evaluator{
		NewHarborEvaluator(cfg),
		NewFluxEvaluator(cfg),
	})
	if len(rows) != 0 {
		t.Fatalf("Pod-only evaluators should ignore Deployments — got %d rows", len(rows))
	}
}

// ── Engine ───────────────────────────────────────────────────────

func TestEngine_PublishesOnSubscribedEvent(t *testing.T) {
	rec := &recorder{}
	pod := uPod("acme", "app", withContainerImages("harbor.openova.io/proxy/x:1"))

	eventCh := make(chan Event, 4)
	cfg := Config{
		Logger:       quietLogger(),
		HarborDomain: "harbor.openova.io",
		TickInterval: 0, // event-only path for this test
		Now:          func() time.Time { return time.Unix(1700000000, 0) },
	}
	eng, err := NewEngine(cfg,
		[]Evaluator{NewHarborEvaluator(cfg)},
		rec,
		func(_ string) (Snapshot, []string, error) { return &fakeSnapshot{}, nil, nil },
		func(_ map[string]struct{}) (<-chan Event, func()) {
			return eventCh, func() {}
		},
	)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = eng.Run(ctx); close(done) }()

	eventCh <- Event{Cluster: "alpha", Kind: "pod", Object: pod}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("recorder never saw the event-driven publish")
		default:
			if len(rec.Snapshot()) >= 1 {
				cancel()
				<-done
				if rec.Snapshot()[0].cluster != "alpha" {
					t.Fatalf("cluster id not propagated")
				}
				if rec.Snapshot()[0].report.Policy != "harbor-proxy-pull" {
					t.Fatalf("unexpected policy %q", rec.Snapshot()[0].report.Policy)
				}
				if rec.Snapshot()[0].report.Time.IsZero() {
					t.Fatalf("report timestamp not stamped by engine")
				}
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestEngine_TickerEvaluatesAllPodsAcrossClusters(t *testing.T) {
	rec := &recorder{}
	pod1 := uPod("acme", "p1", withContainerImages("harbor.openova.io/proxy/x:1"))
	pod2 := uPod("widgets", "p2", withContainerImages("docker.io/library/nginx:1"))

	snaps := map[string]*fakeSnapshot{
		"alpha": {by: map[string][]*unstructured.Unstructured{"pod": {pod1}}},
		"beta":  {by: map[string][]*unstructured.Unstructured{"pod": {pod2}}},
	}
	eventCh := make(chan Event, 1)

	cfg := Config{
		Logger:       quietLogger(),
		HarborDomain: "harbor.openova.io",
		TickInterval: 50 * time.Millisecond,
		Now:          func() time.Time { return time.Unix(1700000000, 0) },
	}
	eng, err := NewEngine(cfg,
		[]Evaluator{NewHarborEvaluator(cfg)},
		rec,
		func(id string) (Snapshot, []string, error) {
			if id == "" {
				return nil, []string{"alpha", "beta"}, nil
			}
			s, ok := snaps[id]
			if !ok {
				return nil, nil, errors.New("no snap")
			}
			return s, nil, nil
		},
		func(_ map[string]struct{}) (<-chan Event, func()) {
			return eventCh, func() {}
		},
	)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() { _ = eng.Run(ctx); close(done) }()

	deadline := time.After(1 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("ticker never produced both pass + fail rows; got %v", rec.Snapshot())
		default:
			entries := rec.Snapshot()
			seenPass, seenFail := false, false
			for _, e := range entries {
				if e.cluster == "alpha" && e.report.Result == ResultPass {
					seenPass = true
				}
				if e.cluster == "beta" && e.report.Result == ResultFail {
					seenFail = true
				}
			}
			if seenPass && seenFail {
				cancel()
				<-done
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func TestEngine_RejectsInvalidConfig(t *testing.T) {
	cases := map[string]Config{
		"missing-logger": {},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := NewEngine(cfg, []Evaluator{}, &recorder{}, nil, nil)
			if err == nil {
				t.Fatalf("want error for %q, got nil", name)
			}
		})
	}
	// Logger-OK but missing other deps → still error.
	cfgOK := Config{Logger: quietLogger()}
	if _, err := NewEngine(cfgOK, []Evaluator{}, nil, nil, nil); err == nil {
		t.Fatalf("missing publisher should error")
	}
	if _, err := NewEngine(cfgOK, []Evaluator{}, &recorder{}, nil, nil); err == nil {
		t.Fatalf("missing resolveSnapshot should error")
	}
	if _, err := NewEngine(cfgOK, []Evaluator{}, &recorder{},
		func(string) (Snapshot, []string, error) { return nil, nil, nil },
		nil); err == nil {
		t.Fatalf("missing subscribe should error")
	}
	if _, err := NewEngine(cfgOK, nil, &recorder{},
		func(string) (Snapshot, []string, error) { return nil, nil, nil },
		func(map[string]struct{}) (<-chan Event, func()) { return nil, nil }); err == nil {
		t.Fatalf("empty evaluator slice should error")
	}
}

func TestEngine_DoubleRunRefused(t *testing.T) {
	cfg := Config{Logger: quietLogger()}
	cfg.defaults()
	rec := &recorder{}
	eventCh := make(chan Event)
	eng, err := NewEngine(cfg, []Evaluator{NewFluxEvaluator(cfg)}, rec,
		func(string) (Snapshot, []string, error) { return &fakeSnapshot{}, nil, nil },
		func(map[string]struct{}) (<-chan Event, func()) {
			return eventCh, func() {}
		},
	)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = eng.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)
	if err := eng.Run(context.Background()); err == nil {
		t.Fatalf("second Run should error")
	}
	cancel()
}

// Sanity that the package's helper conversions don't drop fields.
func TestEventFromUnstructured(t *testing.T) {
	pod := uPod("ns", "n")
	ev := EventFromUnstructured("alpha", "pod", pod)
	if ev.Cluster != "alpha" || ev.Kind != "pod" || ev.Object != pod {
		t.Fatalf("EventFromUnstructured dropped a field: %+v", ev)
	}
}
