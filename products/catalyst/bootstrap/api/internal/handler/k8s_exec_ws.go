// k8s_exec_ws.go — EPIC-4 Slice E2 (#1099). Direct WebSocket exec
// fallback for when the Guacamole iframe is blocked (CSP, network
// policy, recording disabled). The UI's ExecPanel detects iframe
// failure via 5s timeout and switches to this URL.
//
// Endpoint:
//
//	GET /api/v1/sovereigns/{id}/k8s/exec/{ns}/{pod}/{container}?command=/bin/sh
//
// The handler upgrades the request to a WebSocket and proxies the
// kube-apiserver `pods/exec` subresource. Bytes flow bidirectionally —
// stdout+stderr from the apiserver to the browser, stdin from the
// browser to the apiserver. The xterm.js client maintains the terminal
// state-machine.
//
// REUSES `auth.RequireSession` and `viewerTierAllowsLogs` (same as
// /k8s/logs). Tier-developer or higher gate matches HandleK8sExecSession.
//
// Implementation note: this handler does NOT need to dial Guacamole.
// The fallback path is intentionally Guacamole-less so a Sovereign with
// `bp-guacamole.enabled = false` still gets a working browser shell —
// just without server-side recording.
package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	catauth "github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// k8sExecUpgrader is the WebSocket upgrader for the exec fallback.
// CheckOrigin is permissive — auth lives in the chi middleware
// (RequireSession), not here.
var k8sExecUpgrader = websocket.Upgrader{
	ReadBufferSize:   4096,
	WriteBufferSize:  4096,
	CheckOrigin:      func(*http.Request) bool { return true },
	HandshakeTimeout: 10 * time.Second,
	Subprotocols:     []string{"channel.k8s.io", "v4.channel.k8s.io"},
}

// ExecStreamFactory returns a duplex io.ReadWriteCloser bridging the
// caller to the apiserver `pods/exec` subresource. Production main.go
// binds this to a real client-go SPDY exec executor; tests bind a fake
// pipe so the pump can be exercised without an apiserver.
//
// The factory is settable via SetExecStreamFactory so the same handler
// works in both wirings.
type ExecStreamFactory func(ctx context.Context, ns, pod, container string, command []string) (io.ReadWriteCloser, error)

// SetExecStreamFactory wires the apiserver exec stream factory.
// Nil-tolerant: when nil, /k8s/exec WebSocket returns 503.
func (h *Handler) SetExecStreamFactory(f ExecStreamFactory) {
	h.execStreamMu.Lock()
	defer h.execStreamMu.Unlock()
	h.execStreamFactory = f
}

func (h *Handler) execStreamFactoryGet() ExecStreamFactory {
	h.execStreamMu.RLock()
	defer h.execStreamMu.RUnlock()
	return h.execStreamFactory
}

// HandleK8sExecWebSocket — GET (WebSocket upgrade)
//
//	/api/v1/sovereigns/{id}/k8s/exec/{ns}/{pod}/{container}?command=/bin/sh
//
// Returns: WebSocket upgrade. Each direction carries raw bytes; the
// xterm.js client renders the terminal. On apiserver error, the proxy
// sends a normal close (1000) with the error string in the payload.
func (h *Handler) HandleK8sExecWebSocket(w http.ResponseWriter, r *http.Request) {
	sovereignID := chi.URLParam(r, "id")
	ns := chi.URLParam(r, "ns")
	pod := chi.URLParam(r, "pod")
	container := chi.URLParam(r, "container")
	if sovereignID == "" || ns == "" || pod == "" || container == "" {
		http.Error(w, "missing path parameters", http.StatusBadRequest)
		return
	}
	sovereignID = h.resolveChrootClusterID(sovereignID)

	claims := catauth.ClaimsFromContext(r.Context())
	if !execSessionCallerAuthorized(claims) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	command := r.URL.Query().Get("command")
	cmd := []string{"/bin/sh"}
	if command != "" {
		// Whitespace-split is intentionally simple — operators can
		// override via the `?command=...` param but most paths use
		// /bin/sh and don't need shell-quoting. Anything more complex
		// uses the Guacamole path.
		cmd = strings.Fields(command)
		if len(cmd) == 0 {
			cmd = []string{"/bin/sh"}
		}
	}

	factory := h.execStreamFactoryGet()
	if factory == nil {
		http.Error(w, "exec stream not wired", http.StatusServiceUnavailable)
		return
	}

	// G95.1 (Refs #2642) — thread the resolved cluster ID through the
	// request context so the ExecStreamFactory closure can resolve the
	// matching rest.Config via k8scache.Factory.RestConfigFor. Without
	// this, the closure would either need a separate path-param parse
	// (duplicating chi-param logic + breaking the resolveChrootClusterID
	// alias) or accept the cluster id as a positional argument (which
	// changes every test-side fake factory). Context-threaded ID keeps
	// the factory signature stable for tests + production.
	ctx := contextWithExecClusterID(r.Context(), sovereignID)
	stream, err := factory(ctx, ns, pod, container, cmd)
	if err != nil {
		http.Error(w, fmt.Sprintf("exec stream: %v", err), http.StatusBadGateway)
		return
	}
	defer stream.Close()

	conn, err := k8sExecUpgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrader already wrote the error.
		return
	}
	defer conn.Close()

	pumpExecStream(ctx, conn, stream, h.log)
}

// execClusterIDKey is the unexported context key under which
// HandleK8sExecWebSocket stamps the resolved cluster ID. Production
// main.go's exec_stream_wire.go reads via ClusterIDFromExecContext.
//
// G95.1 (Refs #2642).
type execClusterIDKey struct{}

func contextWithExecClusterID(ctx context.Context, clusterID string) context.Context {
	return context.WithValue(ctx, execClusterIDKey{}, clusterID)
}

// ClusterIDFromExecContext returns the cluster ID stamped on an exec
// request's context by HandleK8sExecWebSocket. Empty string when not
// stamped (the test-fake-factory path).
//
// G95.1 (Refs #2642).
func ClusterIDFromExecContext(ctx context.Context) string {
	if v, ok := ctx.Value(execClusterIDKey{}).(string); ok {
		return v
	}
	return ""
}

// pumpExecStream is the bidirectional pump between the WebSocket and
// the apiserver-exec stream. Two goroutines: WS→stream (stdin) and
// stream→WS (stdout+stderr). Returns when either side closes.
func pumpExecStream(ctx context.Context, conn *websocket.Conn, stream io.ReadWriteCloser, log logger) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// stdin pump (browser → apiserver). The browser sends raw bytes;
	// we pass through unchanged.
	go func() {
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if _, err := stream.Write(msg); err != nil {
				return
			}
		}
	}()

	// stdout+stderr pump (apiserver → browser). One binary frame per
	// chunk; xterm.js re-assembles cleanly on the client side.
	buf := make([]byte, 4096)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		n, err := stream.Read(buf)
		if n > 0 {
			frame := make([]byte, n)
			copy(frame, buf[:n])
			if werr := conn.WriteMessage(websocket.BinaryMessage, frame); werr != nil {
				return
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				_ = conn.WriteControl(
					websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, "exec stream ended"),
					time.Now().Add(time.Second),
				)
				return
			}
			if log != nil {
				log.Warn("k8s exec: stream error", "err", err)
			}
			_ = conn.WriteControl(
				websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseInternalServerErr, err.Error()),
				time.Now().Add(time.Second),
			)
			return
		}
	}
}
