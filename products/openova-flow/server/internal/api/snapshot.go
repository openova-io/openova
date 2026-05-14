package api

import (
	"encoding/json"
	"net/http"

	"github.com/openova-io/openova/products/openova-flow/server/internal/store"
	"github.com/openova-io/openova/products/openova-flow/server/internal/types"
)

func HandleSnapshot(s store.Backend, flowID string, w http.ResponseWriter, r *http.Request) {
	flow, nodes, rels, err := s.Snapshot(flowID)
	if err != nil {
		http.Error(w, "snapshot read failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if flow == nil && len(nodes) == 0 && len(rels) == 0 {
		http.NotFound(w, r)
		return
	}
	msg := types.FlowMessage{
		Type:          types.TypeSnapshot,
		Flow:          flow,
		Nodes:         nodes,
		Relationships: rels,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(msg)
}

// HandleDelete drops a flow's state. 204 on success regardless of
// whether the flow existed (idempotent).
func HandleDelete(s store.Backend, flowID string, w http.ResponseWriter, r *http.Request) {
	if err := s.Drop(flowID); err != nil {
		http.Error(w, "drop failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
