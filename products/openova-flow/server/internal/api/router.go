// Package api wires the HTTP+SSE endpoints. Uses stdlib net/http
// servemux + a tiny path-param helper.
//
// Wire contract — locked across the three OpenovaFlow agents:
//
//	POST   /v1/flows/{flowId}/events           ingest one FlowMessage
//	GET    /v1/flows/{flowId}/snapshot         current FlowInstance + nodes + rels
//	GET    /v1/flows/{flowId}/stream           SSE: replay snapshot + tail
//	POST   /v1/flows/{flowId}/log-lines        bulk-append exec log lines
//	GET    /v1/flows/{flowId}/log-lines        list log lines for a (node_id, exec_id)
//	DELETE /v1/flows/{flowId}                  purge a flow (CASCADE all children)
//	GET    /healthz                            liveness
//	GET    /readyz                             readiness
package api

import (
	"net/http"
	"strings"

	"github.com/openova-io/openova/products/openova-flow/server/internal/store"
)

// NewRouter returns an http.Handler routing the OpenovaFlow surface.
// The backend is a long-lived process-global (in-memory Store for
// tests/dev, PGStore for production).
func NewRouter(s store.Backend) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	mux.HandleFunc("/v1/flows/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v1/flows/")
		if path == "" {
			http.NotFound(w, r)
			return
		}
		parts := strings.SplitN(path, "/", 2)
		flowID := parts[0]
		if flowID == "" {
			http.NotFound(w, r)
			return
		}
		sub := ""
		if len(parts) == 2 {
			sub = parts[1]
		}
		switch {
		case sub == "events" && r.Method == http.MethodPost:
			HandleIngest(s, flowID, w, r)
		case sub == "snapshot" && r.Method == http.MethodGet:
			HandleSnapshot(s, flowID, w, r)
		case sub == "stream" && r.Method == http.MethodGet:
			HandleStream(s, flowID, w, r)
		case sub == "log-lines" && r.Method == http.MethodPost:
			HandleLogLinesAppend(s, flowID, w, r)
		case sub == "log-lines" && r.Method == http.MethodGet:
			HandleLogLinesList(s, flowID, w, r)
		case sub == "" && r.Method == http.MethodDelete:
			HandleDelete(s, flowID, w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	return mux
}
