// Package api wires the HTTP+SSE endpoints. Uses stdlib net/http
// servemux + a tiny path-param helper.
//
// Wire contract — locked across the three OpenovaFlow agents:
//
//	GET    /                                   API root — JSON descriptor
//	                                           of the surface (no UI is
//	                                           served from this binary).
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

// rootDescriptor is the JSON body returned for GET / so an operator
// (or smoke test) hitting the bare hostname gets a 200 with a
// machine-readable description of the API surface, instead of a bare
// HTTP 404 from net/http.ServeMux.
const rootDescriptor = `{` +
	`"service":"openova-flow-server",` +
	`"description":"OpenovaFlow HTTP+SSE event router. No UI is served from this binary — the React canvas library is consumed by the openova-console product.",` +
	`"docs":"https://docs.openova.io/openova-flow",` +
	`"endpoints":{` +
	`"healthz":"GET /healthz",` +
	`"readyz":"GET /readyz",` +
	`"ingest":"POST /v1/flows/{flowId}/events",` +
	`"snapshot":"GET /v1/flows/{flowId}/snapshot",` +
	`"stream":"GET /v1/flows/{flowId}/stream (SSE)",` +
	`"logLinesAppend":"POST /v1/flows/{flowId}/log-lines",` +
	`"logLinesList":"GET /v1/flows/{flowId}/log-lines",` +
	`"delete":"DELETE /v1/flows/{flowId}"` +
	`}` +
	`}` + "\n"

// NewRouter returns an http.Handler routing the OpenovaFlow surface.
// The backend is a long-lived process-global (in-memory Store for
// tests/dev, PGStore for production).
func NewRouter(s store.Backend) http.Handler {
	mux := http.NewServeMux()

	// `/` is the catch-all in Go's net/http.ServeMux. Guard against
	// unintended fall-through with an exact-path check so unrelated
	// paths still 404 (instead of getting the descriptor body) and a
	// method check so accidental POSTs to `/` 405 loudly.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(rootDescriptor))
		}
	})

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
