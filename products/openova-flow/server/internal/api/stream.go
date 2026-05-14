package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/openova-io/openova/products/openova-flow/server/internal/store"
	"github.com/openova-io/openova/products/openova-flow/server/internal/types"
)

const HeartbeatInterval = 15 * time.Second

func HandleStream(s store.Backend, flowID string, w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	sub, cancel := s.Subscribe(flowID)
	defer cancel()

	flow, nodes, rels, err := s.Snapshot(flowID)
	if err != nil {
		fmt.Fprintf(w, "event: error\ndata: %q\n\n", err.Error())
		return
	}
	snapMsg := types.FlowMessage{
		Type:          types.TypeSnapshot,
		Flow:          flow,
		Nodes:         nodes,
		Relationships: rels,
	}
	seq, _ := s.SeqForFlow(flowID)
	if err := writeSSE(w, "snapshot", seq, snapMsg); err != nil {
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
