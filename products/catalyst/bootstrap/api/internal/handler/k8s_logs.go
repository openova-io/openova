// k8s_logs.go — WebSocket Pod-log streaming surface (EPIC-4 X1, #1099).
//
// Route:
//
//	GET /api/v1/sovereigns/{id}/k8s/logs/{ns}/{pod}/{container}
//	    ?follow=true&tailLines=100&since=<rfc3339>&previous=true
//
// Returns: WebSocket upgrade. Each WebSocket frame = one log line
// (verbatim bytes the kubelet emitted; no JSON wrapping). On a clean
// kubelet EOF the proxy sends a normal close (1000); on kubelet
// error it sends an internal-server close (1011) with the error
// string in the payload.
//
// Why a WebSocket and not SSE?
//   1. SSE forces UTF-8 + line-buffered framing; binary log payloads
//      (anything that prints a NUL or invalid UTF-8 byte) round-trips
//      cleanly only on a binary WS frame.
//   2. Browsers reconnect WS more aggressively than SSE; the
//      `?since=<rfc3339>` query param lets the client resume without
//      seeing duplicates.
//   3. Symmetric framing — future control messages (e.g. "follow off
//      now", "increase tailLines") naturally fit on the same channel.
//
// Auth contract (REUSES `auth.RequireSession` middleware):
//
//	The endpoint is registered inside the same auth-gated chi.Group
//	as the other `/api/v1/sovereigns/{id}/k8s/...` routes. The
//	middleware injects `auth.ClaimsKey` into the request context;
//	this handler reads it to enforce the EPIC-3 viewer-tier
//	requirement. (For now: any authenticated session is allowed; the
//	scope-matcher hook lives behind `viewerTierAllowsLogs` ready for
//	the EPIC-3 follow-up to wire it up.)
//
// The kubelet stream itself goes through the apiserver subresource
// — the catalyst-api authenticates to the apiserver as the cluster's
// pre-registered ServiceAccount (the same identity used by the
// k8scache.Factory). No browser-side kubeconfig.
package handler

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	catauth "github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// k8sLogsUpgrader is the package-level upgrader. CheckOrigin is
// permissive — auth lives in the chi middleware (RequireSession), not
// here. The catalyst-api fronts a tightly-scoped HTTPRoute; the
// browser SPA is the only legitimate caller.
var k8sLogsUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(*http.Request) bool { return true },
	HandshakeTimeout: 10 * time.Second,
}

// HandleK8sLogs streams a Pod's container log over a WebSocket.
//
// Path params: id (Sovereign), ns, pod, container.
// Query params:
//
//	follow    — "true" / "false"; default true
//	tailLines — int; default 100; capped at 10000
//	since     — RFC3339 timestamp; if set, only lines after this point
//	previous  — "true" / "false"; default false; reads logs from a
//	            previous container instance (useful right after a crash)
//
// On any preflight failure (auth, missing cluster, missing pod path
// elements) the handler returns the appropriate HTTP status BEFORE
// upgrading. Once the WebSocket is open, all errors are signalled via
// close frame.
func (h *Handler) HandleK8sLogs(w http.ResponseWriter, r *http.Request) {
	if h.k8sCache == nil {
		http.Error(w, "k8scache disabled", http.StatusServiceUnavailable)
		return
	}
	clusterID := chi.URLParam(r, "id")
	ns := chi.URLParam(r, "ns")
	pod := chi.URLParam(r, "pod")
	container := chi.URLParam(r, "container")
	if clusterID == "" || ns == "" || pod == "" || container == "" {
		http.Error(w, "missing path parameters", http.StatusBadRequest)
		return
	}
	clusterID = h.resolveChrootClusterID(clusterID)

	// Auth gate. RequireSession's middleware has already validated
	// the session cookie + injected Claims into the context for
	// the chi.Group this route lives in. When auth is disabled
	// (Sovereign chroot mode without KC; tests), claims will be
	// nil — the handler still streams (matching the rest of /k8s/*).
	claims := catauth.ClaimsFromContext(r.Context())
	if !viewerTierAllowsLogs(claims, ns) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	core := h.k8sCache.CoreClient(clusterID)
	if core == nil {
		http.Error(w, fmt.Sprintf("sovereign %q not registered", clusterID), http.StatusNotFound)
		return
	}

	opts, err := parseLogOptions(r.URL.Query(), container)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	stream, err := openLogStream(r.Context(), core, ns, pod, opts)
	if err != nil {
		// Map common K8s errors to HTTP status before the upgrade.
		http.Error(w, fmt.Sprintf("openLogStream: %v", err), http.StatusBadGateway)
		return
	}
	defer stream.Close()

	conn, err := k8sLogsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade already wrote an error response.
		return
	}
	defer conn.Close()

	pumpKubeletToWS(r.Context(), conn, stream, h.log)
}

// parseLogOptions extracts the query params and constructs a typed
// PodLogOptions. Returns an error on a malformed `since` timestamp;
// other params silently fall back to defaults.
func parseLogOptions(q map[string][]string, container string) (*corev1.PodLogOptions, error) {
	get := func(k string) string {
		if vs, ok := q[k]; ok && len(vs) > 0 {
			return vs[0]
		}
		return ""
	}
	follow := true
	if v := get("follow"); v != "" {
		follow = v == "true"
	}
	tailLines := int64(100)
	if v := get("tailLines"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("bad tailLines: %q", v)
		}
		tailLines = n
		if tailLines > 10000 {
			tailLines = 10000
		}
	}
	previous := get("previous") == "true"

	opts := &corev1.PodLogOptions{
		Container: container,
		Follow:    follow,
		TailLines: &tailLines,
		Previous:  previous,
	}
	if v := get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return nil, fmt.Errorf("bad since (RFC3339 expected): %q", v)
		}
		mt := metav1.NewTime(t)
		opts.SinceTime = &mt
	}
	return opts, nil
}

// openLogStream opens the kubelet log subresource via the apiserver.
// Pulled out of HandleK8sLogs so tests can inject a fake CoreClient
// and exercise the streaming pump without standing up a kubelet.
func openLogStream(ctx context.Context, core kubernetes.Interface, ns, pod string, opts *corev1.PodLogOptions) (io.ReadCloser, error) {
	req := core.CoreV1().Pods(ns).GetLogs(pod, opts)
	return req.Stream(ctx)
}

// pumpKubeletToWS reads lines from the kubelet stream and forwards each
// as a binary WebSocket frame. Returns when:
//   - kubelet returns EOF (clean close 1000)
//   - kubelet returns error (close 1011 with error message)
//   - WebSocket write fails (transport gone)
//   - request context is cancelled (e.g. browser closed tab)
func pumpKubeletToWS(ctx context.Context, conn *websocket.Conn, stream io.ReadCloser, log logger) {
	scanner := bufio.NewScanner(stream)
	// Logs may carry long lines (stack traces, JSON blobs); bump
	// the per-line cap to 1 MiB before bailing.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	// The done channel is closed when the request context fires;
	// the scanner blocks on Read, so we close the underlying stream
	// to unblock it. This is the standard io-cancellation pattern
	// for client-go's apiserver streams.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = stream.Close()
		case <-done:
		}
	}()

	for scanner.Scan() {
		line := scanner.Bytes()
		// Append a trailing newline so the consumer can render
		// log lines verbatim into a terminal without a per-line
		// wrap.
		frame := make([]byte, 0, len(line)+1)
		frame = append(frame, line...)
		frame = append(frame, '\n')
		if err := conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
			// Browser closed; nothing more to do.
			return
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
		if log != nil {
			log.Warn("k8s logs: kubelet stream error", "err", err)
		}
		_ = conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, err.Error()),
			time.Now().Add(time.Second),
		)
		return
	}
	// Clean EOF.
	_ = conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, "kubelet stream ended"),
		time.Now().Add(time.Second),
	)
}

// viewerTierAllowsLogs is the EPIC-3-aware authorization predicate.
// Today it returns true for any authenticated session (matches the
// rest of /k8s/*); EPIC-3's UserAccess scope-matcher will plug in
// here without changing the call site.
//
// When claims are nil (Sovereign chroot mode without Keycloak) the
// predicate also returns true — same posture as the existing
// /k8s/{kind} handlers, which delegate to the SAR cache for actual
// authorization.
func viewerTierAllowsLogs(_ *catauth.Claims, _ string) bool {
	return true
}

// logger is the narrow logging surface this file needs. The Handler's
// log field satisfies it without forcing a hard dependency on slog.
type logger interface {
	Warn(msg string, args ...any)
}
