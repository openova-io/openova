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
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"

	"github.com/openova-io/openova/products/sandbox/pty-server/internal/agentcatalog"
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

// createRequest is the wire shape of POST /sessions. TBD-P4 #1986 B3
// added `agent` + `extraArgs`; the raw `command` is retained as an
// operator escape hatch (curl-from-pod / debug). Exactly ONE of
// {agent, command} must be set — both empty AND both set return 400.
//
// Env was historically a []string; B3 adds a map<string,string>
// alternative that merges cleanly with the agent's default env. We
// keep both for backward-compat with any pinned operator scripts.
type createRequest struct {
	// Agent is the catalogue slug (claude-code, qwen-code, ...).
	// Looked up in agentcatalog.Lookup; unknown slugs return 400 with
	// the canonical list (NOT a bash fallback — bash fallback masks
	// bundle misconfig, see TBD-P4 B3 design spec §2.3).
	Agent string `json:"agent,omitempty"`
	// ExtraArgs are appended verbatim after the agent's DefaultArgs.
	// Empty for the FE default-paint path; useful for "claude --resume".
	ExtraArgs []string `json:"extraArgs,omitempty"`
	// EnvMap merges onto os.Environ() with later-wins semantics; used
	// when Agent is set.
	EnvMap map[string]string `json:"envMap,omitempty"`

	// Command is the raw argv escape hatch. Setting Agent AND Command
	// returns 400 (ambiguous intent).
	Command []string `json:"command,omitempty"`
	// Env is the historic []string env passthrough; only consulted on
	// the Command path.
	Env []string `json:"env,omitempty"`

	Cwd  string `json:"cwd,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
	Cols uint16 `json:"cols,omitempty"`

	// RingBytes overrides the per-session replay-buffer size (bytes).
	// Zero ⇒ pty-server process default (session.DefaultRingBytes, set
	// at startup from SANDBOX_RING_BUFFER_BYTES). Operator escape hatch;
	// the FE default-paint path never sets this. TBD-V22 #1986 F1.
	RingBytes int `json:"ringBytes,omitempty"`
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
	spec, status, errBody := buildSpecFromCreateRequest(req)
	if errBody != nil {
		writeJSON(w, status, errBody)
		return
	}
	s, err := h.mgr.Create(spec)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, sessionDTO{ID: s.ID, CreatedAt: s.CreatedAt})
}

// buildSpecFromCreateRequest is the shared validation + agent-resolution
// path used by both POST /sessions and the lazy-spawn-on-attach branch
// (see dispatchSession). On a validation failure it returns (zero spec,
// http status, body map) so the caller can writeJSON. On success it
// returns the populated spec.
func buildSpecFromCreateRequest(req createRequest) (session.Spec, int, map[string]string) {
	var argv []string
	var envSlice []string
	cwd := req.Cwd

	switch {
	case req.Agent != "" && len(req.Command) > 0:
		return session.Spec{}, http.StatusBadRequest, map[string]string{
			"error":  "ambiguous-request",
			"detail": "specify agent OR command, not both",
		}
	case req.Agent != "":
		ag, err := agentcatalog.Lookup(req.Agent)
		if err != nil {
			return session.Spec{}, http.StatusBadRequest, map[string]string{
				"error":  "invalid-agent",
				"detail": fmt.Sprintf("agent %q not in {%s}", req.Agent, strings.Join(agentcatalog.AllSlugs(), ", ")),
			}
		}
		// RequiredEnv presence check — surfaces missing wiring at
		// create time rather than as a black-screen exec failure.
		for _, k := range ag.RequiredEnv {
			if os.Getenv(k) == "" {
				return session.Spec{}, http.StatusBadRequest, map[string]string{
					"error":  "missing-env",
					"detail": fmt.Sprintf("agent %s requires env %s", req.Agent, k),
				}
			}
		}
		argv, envSlice = ag.Resolve(req.ExtraArgs, req.EnvMap)
		if cwd == "" {
			cwd = ag.DefaultCwd
		}
	case len(req.Command) > 0:
		argv = req.Command
		// Historic []string env passthrough. nil = inherit os.Environ()
		// in session.New (preserves prior behaviour).
		envSlice = req.Env
	default:
		return session.Spec{}, http.StatusBadRequest, map[string]string{
			"error":  "missing-spec",
			"detail": "agent or command required",
		}
	}

	return session.Spec{
		Command:   argv,
		Env:       envSlice,
		Cwd:       cwd,
		Rows:      req.Rows,
		Cols:      req.Cols,
		RingBytes: req.RingBytes,
	}, 0, nil
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
	if errors.Is(err, session.ErrNotFound) {
		// TBD-P4 #1986 B3 — lazy-spawn on attach. The FE WS path is
		// wss://.../sessions/<sandbox-CRD-name>/attach with no prior
		// POST /sessions; the controller renders SANDBOX_DEFAULT_AGENT
		// (and optional ?agent= query override) onto the StatefulSet
		// env so we know what to spawn. If neither is set we keep
		// the historic 404 behaviour.
		spawned, lazyErr := h.lazySpawn(r, id)
		if lazyErr != nil {
			http.Error(w, lazyErr.Error(), http.StatusNotFound)
			return
		}
		s = spawned
	} else if err != nil {
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

// lazySpawn mints a session under the supplied id (the Sandbox CRD
// name carried in the URL) using the agent slug from either:
//
//  1. ?agent=<slug> query param (explicit FE choice for multi-agent
//     Sandboxes — see design spec §2.2 option (b)).
//  2. SANDBOX_DEFAULT_AGENT env var rendered by the controller from
//     spec.agentCatalogue[0].
//
// If neither is set, returns ErrNotFound so the caller can 404 as
// before. Errors during catalogue lookup or spawn are surfaced
// verbatim so they show up in the operator's "session 404" trace.
func (h *Handler) lazySpawn(r *http.Request, id string) (*session.Session, error) {
	slug := r.URL.Query().Get("agent")
	if slug == "" {
		slug = os.Getenv("SANDBOX_DEFAULT_AGENT")
	}
	if slug == "" {
		return nil, session.ErrNotFound
	}
	spec, _, errBody := buildSpecFromCreateRequest(createRequest{Agent: slug})
	if errBody != nil {
		// Surface the same canonical error string the POST path returns
		// (invalid-agent / missing-env) so the operator sees the same
		// diagnostic via the WS upgrade reject path as they would via
		// curl POST /sessions.
		return nil, fmt.Errorf("%s: %s", errBody["error"], errBody["detail"])
	}
	return h.mgr.CreateWithID(id, spec)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
