package api

import (
	"encoding/json"
	"net/http"

	"github.com/openova-io/openova/products/openova-flow/server/internal/store"
	"github.com/openova-io/openova/products/openova-flow/server/internal/types"
)

// HandleSnapshot folds the per-flow ring into a `snapshot` envelope
// and writes it as a single JSON object. 404 when the flow id has
// never been ingested.
func HandleSnapshot(s *store.Store, flowID string, w http.ResponseWriter, r *http.Request) {
	flow, nodes, rels := s.Snapshot(flowID)
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
func HandleDelete(s *store.Store, flowID string, w http.ResponseWriter, r *http.Request) {
	s.Drop(flowID)
	w.WriteHeader(http.StatusNoContent)
}
