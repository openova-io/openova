package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/openova-io/openova/products/openova-flow/server/internal/store"
	"github.com/openova-io/openova/products/openova-flow/server/internal/types"
)

const MaxIngestBody = 1 << 20

func HandleIngest(s store.Backend, flowID string, w http.ResponseWriter, r *http.Request) {
	if flowID == "" {
		http.Error(w, "flowId required", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxIngestBody+1))
	if err != nil {
		http.Error(w, fmt.Sprintf("read body: %v", err), http.StatusBadRequest)
		return
	}
	if len(body) > MaxIngestBody {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}
	m, err := types.DecodeFlowMessage(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	seq, err := s.Append(flowID, m)
	if err != nil {
		http.Error(w, "append failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"seq": seq})
}
