// flow_emitter.go — periodic snapshot emit loop. Each active
// deployment gets a goroutine that, every emitInterval, composes the
// current snapshot via flowSnapshotFromJobs (the same composer the
// legacy /snapshot endpoint used) and POSTs it to openova-flow-server
// as a `snapshot` envelope.
//
// Why a periodic loop instead of per-HR-event push:
//   1. Snapshot composition includes synthetic group nodes (provisioner,
//      bootstrap-kit, per-region sub-groups, cutover/handover/apps phase
//      groups) that don't map 1:1 to helmwatch events. Composing the
//      full snapshot on each emit keeps that logic in one place.
//   2. Idempotent — openova-flow-server's TypeSnapshot replaces all
//      nodes+rels in one transactional write. A missed event is
//      reconciled within emitInterval seconds.
//   3. Decouples emit cadence from event volume — high-frequency
//      HR transitions don't flood the emit path.
//
// The loop also fires on-demand when Bridge.OnProvisionerEvent posts
// to the emitTrigger channel, so state changes propagate to the
// canvas within ~1s rather than waiting for the full tick.

package handler

import (
	"context"
	"sync"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/flowemit"
)

const (
	defaultEmitInterval = 5 * time.Second
	defaultEmitTimeout  = 4 * time.Second
)

// flowEmitter — per-deployment periodic emit loop state. Owned by
// the Handler; one entry per deployment that has reached
// status=phase1-watching at least once.
type flowEmitter struct {
	cancel  context.CancelFunc
	trigger chan struct{}
}

// startFlowEmitLoop spawns the per-deployment emit goroutine.
// Idempotent: a deployment already with an active loop is a no-op.
//
// The loop terminates when:
//   - the Handler's lifecycle context is cancelled (Pod shutdown), OR
//   - StopFlowEmitLoop(depID) is invoked (deployment wiped).
func (h *Handler) startFlowEmitLoop(depID string) {
	if h.flowEmit == nil || depID == "" {
		return
	}
	h.flowEmitMu.Lock()
	if _, ok := h.flowEmitters[depID]; ok {
		h.flowEmitMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	emitter := &flowEmitter{
		cancel:  cancel,
		trigger: make(chan struct{}, 4),
	}
	if h.flowEmitters == nil {
		h.flowEmitters = map[string]*flowEmitter{}
	}
	h.flowEmitters[depID] = emitter
	h.flowEmitMu.Unlock()

	go h.runFlowEmitLoop(ctx, depID, emitter)
}

func (h *Handler) runFlowEmitLoop(ctx context.Context, depID string, e *flowEmitter) {
	ticker := time.NewTicker(defaultEmitInterval)
	defer ticker.Stop()
	// Initial emit so the canvas sees data immediately without waiting
	// for the first tick.
	h.emitSnapshotOnce(depID)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.emitSnapshotOnce(depID)
		case <-e.trigger:
			h.emitSnapshotOnce(depID)
		}
	}
}

// stopFlowEmitLoop cancels and removes the per-deployment goroutine.
// Idempotent — unknown depIDs are a no-op.
func (h *Handler) stopFlowEmitLoop(depID string) {
	h.flowEmitMu.Lock()
	e, ok := h.flowEmitters[depID]
	if ok {
		delete(h.flowEmitters, depID)
	}
	h.flowEmitMu.Unlock()
	if e != nil {
		e.cancel()
	}
}

// triggerFlowEmit signals the emit loop for one deployment to fire
// out-of-cycle. Non-blocking — if the trigger channel is full, the
// existing pending emit covers this state change too.
func (h *Handler) triggerFlowEmit(depID string) {
	h.flowEmitMu.Lock()
	e := h.flowEmitters[depID]
	h.flowEmitMu.Unlock()
	if e == nil {
		return
	}
	select {
	case e.trigger <- struct{}{}:
	default:
	}
}

// emitSnapshotOnce composes the current snapshot from jobs.Store and
// POSTs it to openova-flow-server. Errors are logged and dropped;
// the next tick reconciles.
func (h *Handler) emitSnapshotOnce(depID string) {
	if h.flowEmit == nil {
		return
	}
	msg, ok := h.flowSnapshotFromJobs(depID)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultEmitTimeout)
	defer cancel()
	// Translate the local snapshot wire shape into flowemit's local
	// wire shape (same fields; the two packages are independent so
	// each owns its own DTO).
	fi := &flowemit.FlowInstanceWire{}
	if msg.Flow != nil {
		fi.ID = msg.Flow.ID
		fi.Status = msg.Flow.Status
		fi.StartedAt = msg.Flow.StartedAt
	}
	nodes := make([]flowemit.FlowNodeWire, 0, len(msg.Nodes))
	for _, n := range msg.Nodes {
		fn := flowemit.FlowNodeWire{
			ID:        n.ID,
			FlowID:    n.FlowID,
			Label:     n.Label,
			Status:    n.Status,
			Family:    n.Family,
			Region:    n.Region,
			StartedAt: n.StartedAt,
			EndedAt:   n.EndedAt,
		}
		nodes = append(nodes, fn)
	}
	rels := make([]flowemit.RelationshipWire, 0, len(msg.Relationships))
	for _, r := range msg.Relationships {
		rels = append(rels, flowemit.RelationshipWire{
			FromID: r.FromID,
			ToID:   r.ToID,
			Type:   r.Type,
		})
	}
	if err := h.flowEmit.Snapshot(ctx, depID, fi, nodes, rels); err != nil {
		h.log.Warn("flow emit snapshot failed", "id", depID, "err", err)
	}
}

// flowEmitMu + flowEmitters live on Handler; declare them as package-
// level fields via a small shim file so we don't reflow the giant
// Handler struct above.
var _ = sync.Mutex{}
