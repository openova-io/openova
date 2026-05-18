// Package server exposes the pty-server HTTP + WebSocket surface
// described in architecture.md §2.
//
// Endpoints:
//
//	POST   /sessions              spawn a fresh PTY + child
//	GET    /sessions              list active session IDs
//	GET    /sessions/{id}         describe a single session
//	WS     /sessions/{id}/attach  bidi byte stream (raw PTY)
//	WS     /sessions/{id}/cards   JSON card stream (mobile alt surface)
//	POST   /sessions/{id}/resize  cols/rows -> SIGWINCH
//	POST   /sessions/{id}/signal  named signal -> process group
//	DELETE /sessions/{id}         graceful SIGTERM, then SIGKILL
//	GET    /healthz               liveness
//	GET    /metrics               Prometheus text-format scrape (Wave 15)
//
// The router is deliberately framework-free (net/http only) so the
// resulting binary fits in a scratch container without extra surface.
package server

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"

	"github.com/openova-io/openova/products/sandbox/pty-server/internal/session"
)

// Handler is the root http.Handler for pty-server.
type Handler struct {
	mgr      *session.Manager
	upgrader websocket.Upgrader
}

// New returns an HTTP handler wired to the supplied session manager.
func New(mgr *session.Manager) *Handler {
	return &Handler{
		mgr: mgr,
		upgrader: websocket.Upgrader{
			// pty-server is reached through the gateway with same-origin
			// enforcement upstream; permit any origin here so localhost
			// dev (xterm.js in a Vite dev server) works without flags.
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

// ServeHTTP dispatches based on path + method.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/healthz":
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
		return

	case r.URL.Path == "/metrics" && r.Method == http.MethodGet:
		// Wave 15 (PR #1674 follow-up) — Prometheus scrape endpoint.
		// The Gauge `pty_server_websocket_connections` is updated on
		// every WS upgrade / close in attach() + cards(). See metrics.go.
		metricsHandler().ServeHTTP(w, r)
		return

	case r.URL.Path == "/idle" && r.Method == http.MethodGet:
		h.idle(w, r)
		return

	case r.URL.Path == "/sessions" && r.Method == http.MethodPost:
		h.create(w, r)
		return

	case r.URL.Path == "/sessions" && r.Method == http.MethodGet:
		h.list(w, r)
		return

	case strings.HasPrefix(r.URL.Path, "/sessions/"):
		h.dispatchSession(w, r)
		return
	}
	http.NotFound(w, r)
}

// idleDTO is the wire shape returned by GET /idle. The sandbox-controller
// IdleScaler polls this endpoint every 60s and stamps the StatefulSet
// annotation `openova.io/sandbox-last-activity-at` so a subsequent
// scaler tick can scale the StatefulSet to 0 once the configured idle
// window has elapsed (architecture.md §1, PR #1641).
type idleDTO struct {
	LastActivityAt time.Time `json:"lastActivityAt"`
	ActiveSessions int       `json:"activeSessions"`
}

func (h *Handler) idle(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, idleDTO{
		LastActivityAt: h.mgr.LastActivity().UTC(),
		ActiveSessions: h.mgr.Count(),
	})
}

// dispatchSession routes /sessions/{id}/... by path suffix.
func (h *Handler) dispatchSession(w http.ResponseWriter, r *http.Request) {
	// Strip "/sessions/" prefix.
	rest := strings.TrimPrefix(r.URL.Path, "/sessions/")
	parts := strings.SplitN(rest, "/", 2)
	id := parts[0]
	if id == "" {
		http.NotFound(w, r)
		return
	}
	suffix := ""
	if len(parts) == 2 {
		suffix = parts[1]
	}

	switch {
	case suffix == "" && r.Method == http.MethodGet:
		h.describe(w, r, id)
	case suffix == "" && r.Method == http.MethodDelete:
		h.delete(w, r, id)
	case suffix == "attach" && r.Method == http.MethodGet:
		h.attach(w, r, id)
	case suffix == "cards" && r.Method == http.MethodGet:
		h.cards(w, r, id)
	case suffix == "resize" && r.Method == http.MethodPost:
		h.resize(w, r, id)
	case suffix == "signal" && r.Method == http.MethodPost:
		h.signal(w, r, id)
	default:
		http.NotFound(w, r)
	}
}

// --- request / response shapes ----------------------------------------------

type createRequest struct {
	Command []string `json:"command"`
	Env     []string `json:"env,omitempty"`
	Cwd     string   `json:"cwd,omitempty"`
	Rows    uint16   `json:"rows,omitempty"`
	Cols    uint16   `json:"cols,omitempty"`
}

type sessionDTO struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
}

type resizeRequest struct {
	Rows uint16 `json:"rows"`
	Cols uint16 `json:"cols"`
}

type signalRequest struct {
	Signal string `json:"signal"`
}

// --- handlers ----------------------------------------------------------------

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if len(req.Command) == 0 {
		http.Error(w, "command required", http.StatusBadRequest)
		return
	}
	s, err := h.mgr.Create(session.Spec{
		Command: req.Command,
		Env:     req.Env,
		Cwd:     req.Cwd,
		Rows:    req.Rows,
		Cols:    req.Cols,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, sessionDTO{ID: s.ID, CreatedAt: s.CreatedAt})
}

func (h *Handler) list(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"sessions": h.mgr.List()})
}

func (h *Handler) describe(w http.ResponseWriter, _ *http.Request, id string) {
	s, err := h.mgr.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, sessionDTO{ID: s.ID, CreatedAt: s.CreatedAt})
}

func (h *Handler) delete(w http.ResponseWriter, _ *http.Request, id string) {
	if err := h.mgr.Stop(id); err != nil {
		if errors.Is(err, session.ErrNotFound) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) resize(w http.ResponseWriter, r *http.Request, id string) {
	s, err := h.mgr.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	var req resizeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if err := s.Resize(req.Rows, req.Cols); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.mgr.Touch()
	w.WriteHeader(http.StatusNoContent)
}

// signal maps human signal names to syscall.Signal. We deliberately
// allow only the safe / scripted set named in architecture.md §2.
func (h *Handler) signal(w http.ResponseWriter, r *http.Request, id string) {
	s, err := h.mgr.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	var req signalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	sig, ok := allowedSignals[strings.ToUpper(req.Signal)]
	if !ok {
		http.Error(w, "unsupported signal", http.StatusBadRequest)
		return
	}
	if err := s.Signal(sig); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.mgr.Touch()
	w.WriteHeader(http.StatusNoContent)
}

var allowedSignals = map[string]syscall.Signal{
	"INT":  syscall.SIGINT,
	"QUIT": syscall.SIGQUIT,
	"TERM": syscall.SIGTERM,
	"HUP":  syscall.SIGHUP,
}

// attach is the canonical raw-byte WebSocket bridge:
//
//	  WS frames (binary) -> PTY stdin
//	  PTY stdout         -> WS frames (binary)
//
// On connect we first ship the replay buffer (one binary frame) so the
// browser's xterm.js paints the recent screen before the live stream
// resumes.
func (h *Handler) attach(w http.ResponseWriter, r *http.Request, id string) {
	s, err := h.mgr.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade: %v", err)
		return
	}
	defer conn.Close()
	// Wave 15 (PR #1674 follow-up) — track active WS connections for the
	// `pty_server_websocket_connections` Gauge (the WebSocket Connections
	// panel on the Sandbox Runtime Grafana dashboard sums this across
	// the fleet). Inc on successful upgrade, defer Dec so abnormal
	// returns (read errors, close handshake, panic) still decrement.
	websocketConnections.Inc()
	defer websocketConnections.Dec()
	// Attach itself is activity (Wave 10 idle-scaler — architecture.md §1).
	h.mgr.Touch()

	sub, replay, err := s.Subscribe(256)
	if err != nil {
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseGoingAway, err.Error()))
		return
	}
	defer s.Unsubscribe(sub)
	// Detach also counts: the IdleScaler should see the trailing edge.
	defer h.mgr.Touch()

	// Replay first.
	if len(replay) > 0 {
		if err := conn.WriteMessage(websocket.BinaryMessage, replay); err != nil {
			return
		}
	}

	// writeMu serialises concurrent writes from the two pumps.
	var writeMu sync.Mutex
	writeBytes := func(p []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteMessage(websocket.BinaryMessage, p)
	}

	// PTY -> WS pump.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-sub.Done:
				return
			case chunk, ok := <-sub.Ch:
				if !ok {
					return
				}
				if err := writeBytes(chunk); err != nil {
					return
				}
				h.mgr.Touch()
			}
		}
	}()

	// WS -> PTY pump (blocking on Read).
	for {
		mt, payload, err := conn.ReadMessage()
		if err != nil {
			break
		}
		// Both text and binary are user input; resize is its own POST.
		if mt == websocket.TextMessage || mt == websocket.BinaryMessage {
			if _, err := s.Write(payload); err != nil {
				break
			}
			h.mgr.Touch()
		}
	}
	<-done
}

// cards is the mobile alt surface (architecture.md §1). For Wave 2 it
// exposes the same byte stream but framed as JSON {"type":"raw",...}.
// A future card-translator replaces the body with parsed cards.
func (h *Handler) cards(w http.ResponseWriter, r *http.Request, id string) {
	s, err := h.mgr.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade: %v", err)
		return
	}
	defer conn.Close()
	// Wave 15 — track active WS connections (see attach() for rationale).
	// cards is a parallel WS surface so it counts toward the same gauge.
	websocketConnections.Inc()
	defer websocketConnections.Dec()
	h.mgr.Touch()

	sub, replay, err := s.Subscribe(256)
	if err != nil {
		return
	}
	defer s.Unsubscribe(sub)
	defer h.mgr.Touch()

	if len(replay) > 0 {
		_ = conn.WriteJSON(map[string]any{"type": "raw", "bytes": replay})
	}
	for {
		select {
		case <-sub.Done:
			return
		case chunk, ok := <-sub.Ch:
			if !ok {
				return
			}
			if err := conn.WriteJSON(map[string]any{"type": "raw", "bytes": chunk}); err != nil {
				return
			}
			h.mgr.Touch()
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
