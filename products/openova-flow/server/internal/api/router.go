// Package api wires the HTTP+SSE endpoints. Uses stdlib net/http
// servemux + a tiny path-param helper — we do NOT pull chi in here
// because the surface is five routes and zero middleware chains.
//
// Wire contract — locked across the three OpenovaFlow agents:
//
//	POST   /v1/flows/{flowId}/events           ingest one FlowMessage
//	GET    /v1/flows/{flowId}/snapshot         current FlowInstance + nodes + rels
//	GET    /v1/flows/{flowId}/stream           SSE: replay snapshot + tail
//	DELETE /v1/flows/{flowId}                  purge a flow
//	GET    /healthz                            liveness
//	GET    /readyz                             readiness
//
// Per docs/INVIOLABLE-PRINCIPLES.md #3 the implementation is
// event-driven — POST appends to the per-flow ring, fanout pushes onto
// every subscriber channel, the SSE handler reads from the channel.
// No polling loops.
package api

import (
	"net/http"
	"strings"

	"github.com/openova-io/openova/products/openova-flow/server/internal/store"
)

// NewRouter returns an http.Handler routing the OpenovaFlow surface.
// The store is a long-lived process-global; multiple HTTP servers
// (the main listener and any test server) share the same store.
func NewRouter(s *store.Store) http.Handler {
	mux := http.NewServeMux()

	// Health endpoints are first-class — no auth, no flowId.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	mux.HandleFunc("/v1/flows/", func(w http.ResponseWriter, r *http.Request) {
		// Parse: /v1/flows/{flowId}[/sub]
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
		case sub == "" && r.Method == http.MethodDelete:
			HandleDelete(s, flowID, w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	return mux
}
