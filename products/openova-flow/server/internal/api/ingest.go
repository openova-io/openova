package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/openova-io/openova/products/openova-flow/server/internal/store"
	"github.com/openova-io/openova/products/openova-flow/server/internal/types"
)

// MaxIngestBody — guard against an emitter shipping a runaway
// payload. Hard cap of 1 MiB per envelope. Snapshots that need more
// must split across N upsert-* messages.
const MaxIngestBody = 1 << 20

// HandleIngest accepts one FlowMessage envelope on POST. Returns
// 200 on accept, 400 on schema violation, 405 on bad method.
//
// Response body — {"seq":<assigned-sequence>}. Useful for emitters
// that want to correlate POSTs with the SSE id they'll see.
func HandleIngest(s *store.Store, flowID string, w http.ResponseWriter, r *http.Request) {
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
	seq := s.Append(flowID, m)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"seq": seq})
}
