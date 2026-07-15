// http.go — the streamable-HTTP transport of the OpenOva MCP server
// (#3988 §4.3): the standalone per-context Service topology bp-openova-mcp
// deploys (Sovereign MCP / per-Org MCP), selected by OPENOVA_MCP_LISTEN.
//
// Wire contract (MCP streamable-HTTP, request/response subset):
//
//	POST /mcp      one JSON-RPC 2.0 message per request body. The reply is
//	               a single application/json JSON-RPC response — the same
//	               envelope the stdio transport writes, produced by the
//	               SAME dispatch path (server.dispatch), so the two-layer
//	               RBAC + parity-403 semantics are byte-identical across
//	               transports. A notification (no id → no reply frame)
//	               returns 202 Accepted with an empty body.
//	               The caller's bearer travels in `Authorization: Bearer
//	               <jwt>` (preferred; per the MCP HTTP-auth convention).
//	               The stdio `_auth.token` argument envelope keeps working
//	               unchanged — extractBearer prefers it when present.
//	GET  /healthz  liveness  — 200 "ok".
//	GET  /readyz   readiness — 200 "ok" (the server is stateless; ready
//	               once listening).
//	GET  /mcp      405 — server-initiated SSE streams are the documented
//	               #3988 §4.3 follow-up (resumable streaming is explicitly
//	               out of the first slice, design doc O4); a spec-compliant
//	               client falls back to plain request/response POSTs.
//
// NOT here (deliberately): any re-implementation of dispatch, auth, or the
// tool gate — each request is served by the same server struct the stdio
// loop uses, with the response frame captured into a buffer instead of
// stdout. Thin transport, zero divergence.
package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/openova-io/openova/products/openova-mcp/internal/identity"
	"github.com/openova-io/openova/products/openova-mcp/internal/tools"
)

// maxHTTPBodyBytes caps a single JSON-RPC request body. Tool arguments are
// small (names + config blobs); 4 MiB is generous while keeping an abusive
// POST from ballooning memory.
const maxHTTPBodyBytes = 4 << 20

// httpTransport serves the MCP over HTTP. One instance per process; every
// request runs through a per-request server value so the framing/bearer
// state never crosses requests.
type httpTransport struct {
	reg            *tools.Registry
	resolver       *identity.Resolver
	fallbackBearer string
}

func serveHTTP(addr string, reg *tools.Registry, resolver *identity.Resolver, fallbackBearer string) error {
	t := &httpTransport{reg: reg, resolver: resolver, fallbackBearer: fallbackBearer}

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", t.handleMCP)
	mux.HandleFunc("/healthz", handleHealth)
	mux.HandleFunc("/readyz", handleHealth)

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		// Generous request timeouts: a tools/call proxies a live
		// catalyst-api mutation (e.g. create_application) that can take a
		// while server-side.
		ReadTimeout:  2 * time.Minute,
		WriteTimeout: 5 * time.Minute,
	}
	log.Printf("http: MCP streamable-HTTP transport listening on %s (POST /mcp, GET /healthz, GET /readyz)", addr)
	return srv.ListenAndServe()
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok")
}

func (t *httpTransport) handleMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		// GET /mcp = the SSE server-initiated stream — the documented
		// follow-up (see file header). 405 + Allow tells a streamable-HTTP
		// client to stay on request/response POSTs.
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed — POST one JSON-RPC message per request (SSE streaming is a #3988 follow-up)", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxHTTPBodyBytes+1))
	if err != nil {
		http.Error(w, fmt.Sprintf("read body: %v", err), http.StatusBadRequest)
		return
	}
	if len(body) > maxHTTPBodyBytes {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	if len(bytes.TrimSpace(body)) == 0 {
		http.Error(w, "empty body — expected one JSON-RPC 2.0 message", http.StatusBadRequest)
		return
	}

	// Per-request server value: the SAME dispatch (and therefore the SAME
	// identity resolution + two-layer RBAC + parity-403 mapping) as the
	// stdio loop, with the reply frame captured into buf. The caller's
	// Authorization bearer becomes the request-scoped fallback so the
	// stdio `_auth.token` argument envelope still wins when present
	// (extractBearer's precedence, unchanged).
	var buf bytes.Buffer
	srv := &server{
		reg:            t.reg,
		resolver:       t.resolver,
		fallbackBearer: bearerFromRequest(r, t.fallbackBearer),
		out:            &buf,
		lineDelimited:  true, // NDJSON frame — a bare JSON document per reply
	}
	if err := srv.dispatch(body); err != nil {
		// dispatch only errors when the reply frame could not be written;
		// with a bytes.Buffer sink that is effectively unreachable, but
		// surface it honestly rather than replying half a frame.
		http.Error(w, fmt.Sprintf("dispatch: %v", err), http.StatusInternalServerError)
		return
	}

	reply := bytes.TrimSpace(buf.Bytes())
	if len(reply) == 0 {
		// A notification (notifications/initialized, …) produces no reply
		// frame — 202 per the streamable-HTTP convention.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(reply)
}

// bearerFromRequest resolves the request-scoped fallback bearer:
// Authorization: Bearer <jwt> header first, else the process-level
// OPENOVA_MCP_BEARER fallback. The `_auth.token` argument envelope (when a
// caller uses the stdio convention over HTTP) still takes precedence inside
// extractBearer.
func bearerFromRequest(r *http.Request, processFallback string) string {
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if h != "" {
		if strings.HasPrefix(strings.ToLower(h), "bearer ") {
			return strings.TrimSpace(h[len("bearer "):])
		}
		// A raw token without the scheme is accepted too — the resolver
		// trims a literal "Bearer " prefix itself, never a bare token.
		return h
	}
	return processFallback
}
