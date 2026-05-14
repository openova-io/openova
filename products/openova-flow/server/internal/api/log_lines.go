package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/openova-io/openova/products/openova-flow/server/internal/store"
)

// LogLinePayload — wire shape for POST /v1/flows/{flowId}/log-lines.
// Bulk-append a slice of {nodeId, execId, level, message}.
type LogLinePayload struct {
	Lines []struct {
		NodeID  string `json:"nodeId"`
		ExecID  string `json:"execId"`
		Level   string `json:"level"`
		Message string `json:"message"`
	} `json:"lines"`
}

// HandleLogLinesAppend persists exec log lines into CNPG. Returns
// {"written":N}. Only meaningful against a PGStore backend — the
// MemBackend returns 0 written + 503.
func HandleLogLinesAppend(s store.Backend, flowID string, w http.ResponseWriter, r *http.Request) {
	pg, ok := s.(*store.PGStore)
	if !ok {
		http.Error(w, "log-lines persistence requires PGStore backend", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	var p LogLinePayload
	if err := json.Unmarshal(body, &p); err != nil {
		http.Error(w, "parse body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(p.Lines) == 0 {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"written": 0})
		return
	}
	in := make([]store.LogLineInput, 0, len(p.Lines))
	for _, l := range p.Lines {
		in = append(in, store.LogLineInput{
			NodeID:  l.NodeID,
			ExecID:  l.ExecID,
			Level:   l.Level,
			Message: l.Message,
		})
	}
	n, err := pg.AppendLogLines(flowID, in)
	if err != nil {
		http.Error(w, "append failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"written": n})
}

// HandleLogLinesList returns log lines for a given (flow_id, exec_id).
// Query params: execId (required), limit (optional, default 500).
func HandleLogLinesList(s store.Backend, flowID string, w http.ResponseWriter, r *http.Request) {
	pg, ok := s.(*store.PGStore)
	if !ok {
		http.Error(w, "log-lines list requires PGStore backend", http.StatusServiceUnavailable)
		return
	}
	execID := r.URL.Query().Get("execId")
	if execID == "" {
		http.Error(w, "execId query param required", http.StatusBadRequest)
		return
	}
	limit := 500
	if l := r.URL.Query().Get("limit"); l != "" {
		var v int
		_, _ = fmt.Sscanf(l, "%d", &v)
		if v > 0 {
			limit = v
		}
	}
	rows, err := pg.LogLines(flowID, execID, limit)
	if err != nil {
		http.Error(w, "list failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"lines": rows})
}
