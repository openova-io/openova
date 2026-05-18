// hr_informer.go — watches HelmRelease CRs in the configured namespace,
// maps each state change into a FlowMessage via mapper.go, and posts to
// the openova-flow-server via the emit.Client.
//
// Mirror canonical pattern at products/catalyst/bootstrap/api/internal/k8scache/factory.go:
//   - dynamicinformer.NewDynamicSharedInformerFactory keyed off
//     dynamic.NewForConfig(restCfg).
//   - One informer per GVR; event handler dispatches per
//     ADDED/UPDATED/DELETED.
//   - Resync set to 0 — event-driven only (per
//     docs/INVIOLABLE-PRINCIPLES.md #3 event-driven, no polling).
//
// Post-2026-05-11 revert: no synthetic region / phase parent nodes are
// emitted — Agent #6's scaffolding was rejected by the founder. The
// adapter emits one leaf per HR plus dependsOn FS edges; the canvas
// does its own force layout.
package informer

import (
	"context"
	"log/slog"
	"sync"

	"github.com/openova-io/openova/products/openova-flow/adapter-flux/internal/config"
	"github.com/openova-io/openova/products/openova-flow/adapter-flux/internal/emit"
	"github.com/openova-io/openova/products/openova-flow/adapter-flux/internal/types"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
)

// HRGVR — Flux v2 HelmRelease.
var HRGVR = schema.GroupVersionResource{
	Group:    "helm.toolkit.fluxcd.io",
	Version:  "v2",
	Resource: "helmreleases",
}

// HCGVR — Flux v2 HelmChart (one per HelmRelease). Currently unused
// by the mapper but watched so the adapter could surface chart-pull
// progress in a future slice.
var HCGVR = schema.GroupVersionResource{
	Group:    "helm.toolkit.fluxcd.io",
	Version:  "v2",
	Resource: "helmcharts",
}

// SandboxGVR — sandbox.openova.io/v1 Sandbox CR. The sandbox-controller
// reconciles per-user agent-coding workspaces inside an Org's vcluster
// (products/sandbox/docs/architecture.md §7). Watching this GVR makes
// every Sandbox lifecycle visible on the flow canvas alongside the
// Flux HRs.
var SandboxGVR = schema.GroupVersionResource{
	Group:    "sandbox.openova.io",
	Version:  "v1",
	Resource: "sandboxes",
}

// PodGVR — core/v1 Pod. The adapter only emits FlowNodes for Pods
// whose `app.kubernetes.io/component` label marks them as part of a
// Sandbox (pty-server / openova-sandbox-mcp). Everything else is
// dropped at the mapper boundary.
var PodGVR = schema.GroupVersionResource{
	Group:    "",
	Version:  "v1",
	Resource: "pods",
}

// idSepBytes — must match mapper.go's idSep. Kept here only for the
// delete path that needs to reconstruct the leaf id without going
// through BuildFromHR.
const idSepInformer = ":"

// Runtime wraps the informer goroutine + the emit client. Constructed
// from a Config + a *rest.Config (in-cluster or test fake).
type Runtime struct {
	cfg     config.Config
	dyn     dynamic.Interface
	emit    *emit.Client
	log     *slog.Logger
	factory dynamicinformer.DynamicSharedInformerFactory
	// sandboxFactory — separate cluster-scoped factory for the
	// Sandbox CR + sandbox-component Pods. Sandboxes land in Org
	// vcluster namespaces (not `flux-system`), so the canonical
	// NamespaceFilter-scoped `factory` cannot see them. Using a
	// second factory with empty namespace keeps the HR watcher
	// scoped tightly while still letting the Sandbox watcher see
	// every Org vcluster's CRs.
	sandboxFactory dynamicinformer.DynamicSharedInformerFactory

	// dedupe — last-emitted status keyed by node ID. The informer
	// fires on every Update including no-op resyncs; dedupe collapses
	// (same id, same status) into a single POST.
	mu         sync.Mutex
	lastStatus map[string]string
}

// NewRuntime constructs the adapter runtime. The caller supplies the
// REST config (typically rest.InClusterConfig() in prod, a fake in tests).
func NewRuntime(cfg config.Config, restCfg *rest.Config, log *slog.Logger) (*Runtime, error) {
	dyn, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return nil, err
	}
	return &Runtime{
		cfg:            cfg,
		dyn:            dyn,
		emit:           emit.NewClient(cfg.FlowServerURL, cfg.FlowID, cfg.PostTimeout, log),
		log:            log,
		factory:        dynamicinformer.NewFilteredDynamicSharedInformerFactory(dyn, 0, cfg.NamespaceFilter, nil),
		sandboxFactory: dynamicinformer.NewFilteredDynamicSharedInformerFactory(dyn, 0, "", nil),
		lastStatus:     map[string]string{},
	}, nil
}

// NewRuntimeForTest is the dynamic-client-injection seam used by
// mapper_test.go-adjacent integration tests. Production calls
// NewRuntime.
func NewRuntimeForTest(cfg config.Config, dyn dynamic.Interface, log *slog.Logger) *Runtime {
	return &Runtime{
		cfg:            cfg,
		dyn:            dyn,
		emit:           emit.NewClient(cfg.FlowServerURL, cfg.FlowID, cfg.PostTimeout, log),
		log:            log,
		factory:        dynamicinformer.NewFilteredDynamicSharedInformerFactory(dyn, 0, cfg.NamespaceFilter, nil),
		sandboxFactory: dynamicinformer.NewFilteredDynamicSharedInformerFactory(dyn, 0, "", nil),
		lastStatus:     map[string]string{},
	}
}

// Start kicks off the HR + Sandbox + sandbox-Pod informers. Blocks
// on the supplied context — returns when ctx is cancelled.
//
// No bootstrap emit — the canvas now does its own force layout and
// does not need synthetic region/phase parents. The first HR event
// drives the first upsert-nodes envelope.
//
// Sandbox + Pod informers are wired on a SECOND factory that watches
// across all namespaces (Sandboxes live inside Org vclusters, not in
// `flux-system`). The factories share the dynamic client + REST
// QPS/Burst tuning.
func (r *Runtime) Start(ctx context.Context) error {
	hrInf := r.factory.ForResource(HRGVR).Informer()
	if _, err := hrInf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { r.handle(ctx, obj, false) },
		UpdateFunc: func(_, obj any) { r.handle(ctx, obj, false) },
		DeleteFunc: func(obj any) { r.handle(ctx, obj, true) },
	}); err != nil {
		return err
	}

	sbInf := r.sandboxFactory.ForResource(SandboxGVR).Informer()
	if _, err := sbInf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { r.handleSandbox(ctx, obj, false) },
		UpdateFunc: func(_, obj any) { r.handleSandbox(ctx, obj, false) },
		DeleteFunc: func(obj any) { r.handleSandbox(ctx, obj, true) },
	}); err != nil {
		return err
	}

	podInf := r.sandboxFactory.ForResource(PodGVR).Informer()
	if _, err := podInf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { r.handleSandboxPod(ctx, obj, false) },
		UpdateFunc: func(_, obj any) { r.handleSandboxPod(ctx, obj, false) },
		DeleteFunc: func(obj any) { r.handleSandboxPod(ctx, obj, true) },
	}); err != nil {
		return err
	}

	stop := make(chan struct{})
	r.factory.Start(stop)
	r.sandboxFactory.Start(stop)
	r.factory.WaitForCacheSync(stop)
	r.sandboxFactory.WaitForCacheSync(stop)

	r.log.Info("informer: started",
		"namespace", r.cfg.NamespaceFilter,
		"flowServerURL", r.cfg.FlowServerURL,
		"flowID", r.cfg.FlowID,
		"regionKey", r.cfg.RegionKey)

	<-ctx.Done()
	close(stop)
	return nil
}

// handle is invoked on every HR informer event. Maps to FlowMessage
// envelopes and emits, with same-status dedupe so a noisy informer
// (Flux churns conditions on every reconcile) doesn't spam the
// server.
func (r *Runtime) handle(ctx context.Context, obj any, deleted bool) {
	if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = d.Obj
	}
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		r.log.Warn("informer: non-unstructured event payload")
		return
	}

	if deleted {
		region := r.cfg.RegionKey
		if region == "" {
			region = "default"
		}
		nodeID := region + idSepInformer + u.GetName()
		r.mu.Lock()
		delete(r.lastStatus, nodeID)
		r.mu.Unlock()
		if err := r.emit.Emit(ctx, types.FlowMessage{
			Type: types.TypeDeleteNodes,
			IDs:  []string{nodeID},
		}); err != nil {
			r.log.Warn("informer: delete emit failed", "err", err, "id", nodeID)
		}
		return
	}

	res, ok := BuildFromHR(u, r.cfg.RegionKey)
	if !ok {
		return
	}
	res.Node.FlowID = r.cfg.FlowID

	// Dedupe per (id, status).
	r.mu.Lock()
	last, seen := r.lastStatus[res.Node.ID]
	if seen && last == res.Node.Status {
		r.mu.Unlock()
		// Still re-emit relationships once in a while because
		// dependsOn can mutate. For v1: relationships are emitted
		// every event; dedupe only the node update. (Server is
		// idempotent on upsert-rels.)
		if len(res.Relationships) > 0 {
			if err := r.emit.Emit(ctx, types.FlowMessage{
				Type:          types.TypeUpsertRels,
				Relationships: res.Relationships,
			}); err != nil {
				r.log.Warn("informer: rel emit failed", "err", err)
			}
		}
		return
	}
	r.lastStatus[res.Node.ID] = res.Node.Status
	r.mu.Unlock()

	if err := r.emit.Emit(ctx, types.FlowMessage{
		Type:  types.TypeUpsertNodes,
		Nodes: []types.FlowNode{res.Node},
	}); err != nil {
		r.log.Warn("informer: node emit failed", "err", err, "id", res.Node.ID)
	}
	if len(res.Relationships) > 0 {
		if err := r.emit.Emit(ctx, types.FlowMessage{
			Type:          types.TypeUpsertRels,
			Relationships: res.Relationships,
		}); err != nil {
			r.log.Warn("informer: rel emit failed", "err", err)
		}
	}
}

// handleSandbox is the Sandbox CR informer callback. Mirrors handle()
// — converts unstructured → FlowNode via BuildFromSandbox, dedupes on
// (id, status), POSTs upsert-nodes + upsert-rels (or delete-nodes on
// removal).
func (r *Runtime) handleSandbox(ctx context.Context, obj any, deleted bool) {
	if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = d.Obj
	}
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		r.log.Warn("informer: non-unstructured Sandbox event payload")
		return
	}

	if deleted {
		region := r.cfg.RegionKey
		if region == "" {
			region = "default"
		}
		nodeID := sandboxNodeID(region, u.GetName())
		r.mu.Lock()
		delete(r.lastStatus, nodeID)
		r.mu.Unlock()
		if err := r.emit.Emit(ctx, types.FlowMessage{
			Type: types.TypeDeleteNodes,
			IDs:  []string{nodeID},
		}); err != nil {
			r.log.Warn("informer: sandbox delete emit failed", "err", err, "id", nodeID)
		}
		return
	}

	res, ok := BuildFromSandbox(u, r.cfg.RegionKey)
	if !ok {
		return
	}
	res.Node.FlowID = r.cfg.FlowID

	r.mu.Lock()
	last, seen := r.lastStatus[res.Node.ID]
	if seen && last == res.Node.Status {
		r.mu.Unlock()
		// Re-emit relationships unconditionally (server is idempotent).
		if len(res.Relationships) > 0 {
			if err := r.emit.Emit(ctx, types.FlowMessage{
				Type:          types.TypeUpsertRels,
				Relationships: res.Relationships,
			}); err != nil {
				r.log.Warn("informer: sandbox rel emit failed", "err", err)
			}
		}
		return
	}
	r.lastStatus[res.Node.ID] = res.Node.Status
	r.mu.Unlock()

	if err := r.emit.Emit(ctx, types.FlowMessage{
		Type:  types.TypeUpsertNodes,
		Nodes: []types.FlowNode{res.Node},
	}); err != nil {
		r.log.Warn("informer: sandbox node emit failed", "err", err, "id", res.Node.ID)
	}
	if len(res.Relationships) > 0 {
		if err := r.emit.Emit(ctx, types.FlowMessage{
			Type:          types.TypeUpsertRels,
			Relationships: res.Relationships,
		}); err != nil {
			r.log.Warn("informer: sandbox rel emit failed", "err", err)
		}
	}
}

// handleSandboxPod is the Pod informer callback. Filters out any Pod
// that is not a sandbox component (pty-server / openova-sandbox-mcp)
// at the mapper boundary, so the dedupe map is not polluted by every
// Pod in the cluster.
func (r *Runtime) handleSandboxPod(ctx context.Context, obj any, deleted bool) {
	if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = d.Obj
	}
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		r.log.Warn("informer: non-unstructured Pod event payload")
		return
	}
	pod, err := podFromUnstructured(u)
	if err != nil {
		r.log.Warn("informer: pod decode", "err", err, "name", u.GetName())
		return
	}

	if deleted {
		if !isSandboxComponentPod(pod) {
			return
		}
		region := r.cfg.RegionKey
		if region == "" {
			region = "default"
		}
		nodeID := sandboxPodNodeID(region, pod.Namespace, pod.Name)
		r.mu.Lock()
		delete(r.lastStatus, nodeID)
		r.mu.Unlock()
		if err := r.emit.Emit(ctx, types.FlowMessage{
			Type: types.TypeDeleteNodes,
			IDs:  []string{nodeID},
		}); err != nil {
			r.log.Warn("informer: sandbox-pod delete emit failed", "err", err, "id", nodeID)
		}
		return
	}

	res, ok := BuildFromSandboxPod(pod, r.cfg.RegionKey)
	if !ok {
		return
	}
	res.Node.FlowID = r.cfg.FlowID

	r.mu.Lock()
	last, seen := r.lastStatus[res.Node.ID]
	if seen && last == res.Node.Status {
		r.mu.Unlock()
		if len(res.Relationships) > 0 {
			if err := r.emit.Emit(ctx, types.FlowMessage{
				Type:          types.TypeUpsertRels,
				Relationships: res.Relationships,
			}); err != nil {
				r.log.Warn("informer: sandbox-pod rel emit failed", "err", err)
			}
		}
		return
	}
	r.lastStatus[res.Node.ID] = res.Node.Status
	r.mu.Unlock()

	if err := r.emit.Emit(ctx, types.FlowMessage{
		Type:  types.TypeUpsertNodes,
		Nodes: []types.FlowNode{res.Node},
	}); err != nil {
		r.log.Warn("informer: sandbox-pod node emit failed", "err", err, "id", res.Node.ID)
	}
	if len(res.Relationships) > 0 {
		if err := r.emit.Emit(ctx, types.FlowMessage{
			Type:          types.TypeUpsertRels,
			Relationships: res.Relationships,
		}); err != nil {
			r.log.Warn("informer: sandbox-pod rel emit failed", "err", err)
		}
	}
}

// podFromUnstructured re-decodes an unstructured Pod into the typed
// corev1.Pod shape — needed so the mapper can read the strongly-typed
// Status.Phase + ContainerStatuses without re-implementing the
// unstructured-path dance for each field.
func podFromUnstructured(u *unstructured.Unstructured) (*corev1.Pod, error) {
	pod := &corev1.Pod{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.UnstructuredContent(), pod); err != nil {
		return nil, err
	}
	return pod, nil
}
