// http.go — the opt-in HTTP/SSE transport for openova-mcp (#3988 §5 / #899).
//
// This is the MCP Streamable-HTTP transport: a single MCP endpoint (/mcp)
// that accepts a JSON-RPC request over POST and offers a server→client SSE
// stream over GET, plus a /healthz probe endpoint the bp-openova-mcp chart's
// readiness/liveness probes target. It REUSES the SAME dispatch + auth core
// the stdio transport uses (core.handle) — it forks NONE of the resolve /
// two-layer-RBAC / thin-facade logic; the only thing this file adds is the
// wire (HTTP framing + the transport-level bearer gate).
//
// Auth: every request to /mcp MUST carry `Authorization: Bearer <jwt>`. The
// bearer is validated through the SAME identity.Resolver the stdio path uses;
// an absent/invalid bearer is rejected at the transport with HTTP 401 (plus a
// `WWW-Authenticate: Bearer` challenge, the OAuth/MCP idiom) BEFORE any
// dispatch. Application-tier RBAC (the tools/list scope filter and the
// tools/call Org-scope re-auth, including the cross-Org ErrForbidden → MCP 403)
// is enforced inside core.handle exactly as on stdio — an app-tier denial is a
// JSON-RPC error (code -32003, data.status 403) inside a 200 HTTP response, NOT
// an HTTP 403, so MCP clients see identical semantics on both wires.
//
// 🛑 NodePorts are ABSOLUTELY FORBIDDEN. This transport binds a plain in-Pod
// listen address (":8080"); the front door is the Cilium Gateway HTTPRoute the
// chart renders — never a nodePort.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const (
	// mcpEndpoint is the single MCP Streamable-HTTP endpoint: POST for a
	// JSON-RPC request, GET for the server→client SSE stream.
	mcpEndpoint = "/mcp"

	// maxRequestBytes bounds a single JSON-RPC POST body (the tool arguments
	// are small; a create_application body is a few KiB). Mirrors the
	// catalyst-api client's 8MiB read cap order-of-magnitude, kept smaller
	// here since MCP requests are tiny.
	maxRequestBytes = 4 << 20 // 4 MiB

	// sseHeartbeat keeps the GET stream alive through idle proxies.
	sseHeartbeat = 15 * time.Second

	// httpReadHeaderTimeout bounds slow-loris header reads (mirrors the
	// openova-flow server convention).
	httpReadHeaderTimeout = 10 * time.Second

	// shutdownGrace bounds the graceful-drain window on SIGTERM/SIGINT.
	shutdownGrace = 5 * time.Second
)

// resolveHTTPAddr decides the HTTP listen address (empty = run stdio). A
// `--http <addr>` flag wins; otherwise OPENOVA_MCP_HTTP_ADDR. Parsing uses a
// private FlagSet with ContinueOnError and discarded output, and IGNORES parse
// errors, so a stray/unknown arg on the stdio invocation can NEVER break the
// default stdio path (the byte-for-byte-unaffected guarantee).
func resolveHTTPAddr(args []string) string {
	fs := flag.NewFlagSet("openova-mcp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	httpAddr := fs.String("http", "", "listen address for the HTTP/SSE transport (e.g. :8080); empty = stdio")
	_ = fs.Parse(args) // ignore errors: unknown args must not break stdio
	if strings.TrimSpace(*httpAddr) != "" {
		return strings.TrimSpace(*httpAddr)
	}
	return strings.TrimSpace(os.Getenv("OPENOVA_MCP_HTTP_ADDR"))
}

// httpTransport serves the MCP Streamable-HTTP/SSE transport over the shared
// core. It holds no auth/dispatch logic of its own.
type httpTransport struct {
	core *core
	log  *slog.Logger
}

// serveHTTP runs the HTTP/SSE transport on addr until SIGTERM/SIGINT, then
// drains gracefully. It blocks; returns a non-nil error only on a listen
// failure (mirrors the openova-flow server lifecycle).
func serveHTTP(addr string, c *core) error {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	t := &httpTransport{core: c, log: logger}

	srv := &http.Server{
		Addr:              addr,
		Handler:           t.router(),
		ReadHeaderTimeout: httpReadHeaderTimeout,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("openova-mcp HTTP/SSE transport listening",
			"addr", addr, "endpoint", mcpEndpoint, "healthz", "/healthz")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("shutting down HTTP/SSE transport")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// router wires the transport surface. Split out so tests can drive the handler
// with httptest without binding a socket.
//
//	POST /mcp       JSON-RPC request  → JSON-RPC response (application/json)
//	GET  /mcp       server→client SSE stream (text/event-stream)
//	GET  /healthz   liveness  (chart probe target)
//	GET  /readyz    readiness (chart probe target)
//	GET  /          JSON descriptor of the surface
func (t *httpTransport) router() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+mcpEndpoint, t.handlePost)
	mux.HandleFunc("GET "+mcpEndpoint, t.handleGetSSE)
	mux.HandleFunc("GET /healthz", t.handleHealth)
	mux.HandleFunc("GET /readyz", t.handleHealth)
	mux.HandleFunc("GET /{$}", t.handleRoot)
	return mux
}

// handlePost dispatches one JSON-RPC request. The transport-level bearer gate
// runs first (401 on invalid/absent); then core.handle runs the identical
// resolve→RBAC→dispatch flow the stdio path uses.
func (t *httpTransport) handlePost(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "read request body: "+err.Error())
		return
	}

	bearer, ok := t.authOr401(w, r)
	if !ok {
		return
	}

	resp := t.core.handle(r.Context(), bearer, body)
	if resp == nil {
		// A notification (no id) takes no reply — ack at the transport.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	out, err := json.Marshal(resp)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "marshal response")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

// handleGetSSE opens the server→client SSE stream. This slice's tool set has
// no server-initiated messages (capabilities.tools.listChanged=false), so the
// stream is a spec-compliant keep-alive channel: an open comment + periodic
// heartbeats until the client disconnects. Auth is enforced identically to the
// POST path.
func (t *httpTransport) handleGetSSE(w http.ResponseWriter, r *http.Request) {
	if _, ok := t.authOr401(w, r); !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// SSE comment line: signals the stream is open without emitting a
	// JSON-RPC message (there are none to push in this slice).
	_, _ = fmt.Fprint(w, ": openova-mcp stream open\n\n")
	flusher.Flush()

	ticker := time.NewTicker(sseHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if _, err := fmt.Fprint(w, "event: heartbeat\ndata: {}\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (t *httpTransport) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (t *httpTransport) handleRoot(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(rootDescriptor))
}

// authOr401 validates the Authorization: Bearer header (falling back to the
// OPENOVA_MCP_BEARER server bearer when no header is present) through the SAME
// resolver the stdio path uses. On success it returns the raw bearer (which
// core.handle re-resolves for the RBAC decision) and true. On failure it writes
// an HTTP 401 with a WWW-Authenticate challenge and returns ("", false).
func (t *httpTransport) authOr401(w http.ResponseWriter, r *http.Request) (string, bool) {
	bearer := bearerFromHeader(r)
	if bearer == "" {
		bearer = strings.TrimSpace(t.core.fallbackBearer)
	}
	if _, err := t.core.resolveBearer(bearer); err != nil {
		w.Header().Set("WWW-Authenticate", `Bearer realm="openova-mcp"`)
		writeJSONError(w, http.StatusUnauthorized, "unauthorized: "+err.Error())
		return "", false
	}
	return bearer, true
}

// bearerFromHeader extracts the compact JWT from `Authorization: Bearer …`,
// returning "" when the header is absent. Case-insensitive on the scheme.
func bearerFromHeader(r *http.Request) string {
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if h == "" {
		return ""
	}
	if len(h) >= 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return h
}

// writeJSONError writes a minimal JSON error body with an HTTP status. Used for
// TRANSPORT-level failures (bad body, 401); application-tier errors travel as
// JSON-RPC error objects inside a 200 response (see core.toolError).
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// rootDescriptor is the JSON body for GET / — a machine-readable description of
// the surface so an operator (or smoke test) hitting the bare host gets a 200
// instead of a 404.
const rootDescriptor = `{` +
	`"service":"openova-mcp",` +
	`"description":"OpenOva MCP server — RBAC-scoped-per-user thin facade over the catalyst-api. HTTP/SSE (Streamable-HTTP) transport.",` +
	`"transport":"streamable-http",` +
	`"endpoints":{` +
	`"mcp":"POST /mcp (JSON-RPC) | GET /mcp (SSE)",` +
	`"healthz":"GET /healthz",` +
	`"readyz":"GET /readyz"` +
	`},` +
	`"auth":"Authorization: Bearer <jwt> required on every /mcp request"` +
	`}` + "\n"
