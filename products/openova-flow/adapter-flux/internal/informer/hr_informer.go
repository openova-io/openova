// hr_informer.go — watches HelmRelease + HelmChart CRs in the
// configured namespace, maps each state change into a FlowMessage via
// mapper.go, and posts to the openova-flow-server via the emit.Client.
//
// Mirror canonical pattern at products/catalyst/bootstrap/api/internal/k8scache/factory.go:
//   - dynamicinformer.NewDynamicSharedInformerFactory keyed off
//     dynamic.NewForConfig(restCfg).
//   - One informer per GVR; event handler dispatches per
//     ADDED/UPDATED/DELETED.
//   - Resync set to 0 — event-driven only (per
//     docs/INVIOLABLE-PRINCIPLES.md #3 event-driven, no polling).
package informer

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/openova-io/openova/products/openova-flow/adapter-flux/internal/config"
	"github.com/openova-io/openova/products/openova-flow/adapter-flux/internal/emit"
	"github.com/openova-io/openova/products/openova-flow/adapter-flux/internal/types"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

// Runtime wraps the informer goroutine + the emit client. Constructed
// from a Config + a *rest.Config (in-cluster or test fake).
type Runtime struct {
	cfg     config.Config
	dyn     dynamic.Interface
	emit    *emit.Client
	log     *slog.Logger
	factory dynamicinformer.DynamicSharedInformerFactory

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
		cfg:        cfg,
		dyn:        dyn,
		emit:       emit.NewClient(cfg.FlowServerURL, cfg.FlowID, cfg.PostTimeout, log),
		log:        log,
		factory:    dynamicinformer.NewFilteredDynamicSharedInformerFactory(dyn, 0, cfg.NamespaceFilter, nil),
		lastStatus: map[string]string{},
	}, nil
}

// NewRuntimeForTest is the dynamic-client-injection seam used by
// mapper_test.go-adjacent integration tests. Production calls
// NewRuntime.
func NewRuntimeForTest(cfg config.Config, dyn dynamic.Interface, log *slog.Logger) *Runtime {
	return &Runtime{
		cfg:        cfg,
		dyn:        dyn,
		emit:       emit.NewClient(cfg.FlowServerURL, cfg.FlowID, cfg.PostTimeout, log),
		log:        log,
		factory:    dynamicinformer.NewFilteredDynamicSharedInformerFactory(dyn, 0, cfg.NamespaceFilter, nil),
		lastStatus: map[string]string{},
	}
}

// Start kicks off the HR informer + emits the synthetic region node
// envelope so the server has a parent bubble before the first HR
// arrives. Blocks on the supplied context — returns when ctx is cancelled.
func (r *Runtime) Start(ctx context.Context) error {
	// 1) Emit synthetic FlowInstance + region node up-front. The
	//    canvas needs the per-region container before the first HR
	//    upsert lands.
	if err := r.bootstrap(ctx); err != nil {
		r.log.Warn("informer: bootstrap emit failed; continuing", "err", err)
	}

	// 2) Spawn the HR informer.
	hrInf := r.factory.ForResource(HRGVR).Informer()
	_, err := hrInf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { r.handle(ctx, obj, false) },
		UpdateFunc: func(_, obj any) { r.handle(ctx, obj, false) },
		DeleteFunc: func(obj any) { r.handle(ctx, obj, true) },
	})
	if err != nil {
		return err
	}

	stop := make(chan struct{})
	r.factory.Start(stop)
	r.factory.WaitForCacheSync(stop)

	r.log.Info("informer: started",
		"namespace", r.cfg.NamespaceFilter,
		"flowServerURL", r.cfg.FlowServerURL,
		"flowID", r.cfg.FlowID,
		"regionKey", r.cfg.RegionKey)

	<-ctx.Done()
	close(stop)
	return nil
}

// bootstrap emits the FlowInstance + the per-region synthetic
// FlowNode so the canvas has a stable parent for the HR nodes
// before any HR event has flowed.
func (r *Runtime) bootstrap(ctx context.Context) error {
	region := r.cfg.RegionKey
	now := time.Now().UnixMilli()
	flow := &types.FlowInstance{
		ID:        r.cfg.FlowID,
		Status:    "running",
		StartedAt: now,
		Meta: map[string]interface{}{
			"emittedBy": "openova-flow-adapter-flux",
			"region":    region,
		},
	}
	if err := r.emit.Emit(ctx, types.FlowMessage{Type: types.TypeUpsertFlow, Flow: flow}); err != nil {
		return err
	}
	regionNode := BuildRegionNode(region)
	regionNode.FlowID = r.cfg.FlowID
	return r.emit.Emit(ctx, types.FlowMessage{
		Type:  types.TypeUpsertNodes,
		Nodes: []types.FlowNode{regionNode},
	})
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
		nodeID := r.cfg.RegionKey + "/" + u.GetName()
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
		if err := r.emit.Emit(ctx, types.FlowMessage{
			Type:          types.TypeUpsertRels,
			Relationships: res.Relationships,
		}); err != nil {
			r.log.Warn("informer: rel emit failed", "err", err)
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
	if err := r.emit.Emit(ctx, types.FlowMessage{
		Type:          types.TypeUpsertRels,
		Relationships: res.Relationships,
	}); err != nil {
		r.log.Warn("informer: rel emit failed", "err", err)
	}
}
