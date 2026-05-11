package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/openova-io/openova/products/openova-flow/server/internal/store"
	"github.com/openova-io/openova/products/openova-flow/server/internal/types"
)

// HeartbeatInterval — sent to every SSE client every 15s so the
// connection survives an intermediate proxy's idle-timeout (most
// public ingresses cut at 60s).
const HeartbeatInterval = 15 * time.Second

// HandleStream serves SSE for a flowId:
//  1. Write a synthetic `snapshot` event from the current state.
//  2. Subscribe to the per-flow fanout.
//  3. Tail the channel forever (until client disconnects or DELETE
//     drops the flow).
//
// Wire format mirrors @openova/flow-core's expected SSE stream:
//
//	event: snapshot
//	id: <seq>
//	data: {"type":"snapshot",...}
//
//	event: upsert-nodes
//	id: <seq>
//	data: {"type":"upsert-nodes",...}
//
//	event: heartbeat
//	data: {}
func HandleStream(s *store.Store, flowID string, w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// SSE headers.
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// Subscribe BEFORE folding the snapshot so any event that
	// arrives during the fold is queued for delivery after the
	// snapshot (no events lost in the gap).
	sub, cancel := s.Subscribe(flowID)
	defer cancel()

	// Initial snapshot. May be empty (no prior events) — in that
	// case we emit a placeholder so the client knows the stream is
	// alive but unseeded.
	flow, nodes, rels := s.Snapshot(flowID)
	snapMsg := types.FlowMessage{
		Type:          types.TypeSnapshot,
		Flow:          flow,
		Nodes:         nodes,
		Relationships: rels,
	}
	if err := writeSSE(w, "snapshot", s.SeqForFlow(flowID), snapMsg); err != nil {
		return
	}
	flusher.Flush()

	heartbeat := time.NewTicker(HeartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, "event: heartbeat\ndata: {}\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case ev, ok := <-sub.Ch:
			if !ok {
				// Channel closed — flow was dropped.
				return
			}
			if ev.Msg == nil {
				continue
			}
			if err := writeSSE(w, string(ev.Msg.Type), ev.Seq, *ev.Msg); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeSSE(w http.ResponseWriter, event string, seq uint64, payload types.FlowMessage) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\nid: %d\ndata: %s\n\n", event, seq, body); err != nil {
		return err
	}
	return nil
}
