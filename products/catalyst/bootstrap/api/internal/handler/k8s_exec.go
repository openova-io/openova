// k8s_exec.go — EPIC-4 Slice E (#1099). HTTP surface for opening a
// guacamole-fronted shell session into a Pod container, listing past
// sessions, and fetching replay URLs. Acts as the auth+audit boundary
// between the catalyst-ui and the per-Sovereign Apache Guacamole
// instance shipped by `platform/guacamole/` (slice K+P+X1+G #1164).
//
// Endpoints:
//
//	POST /api/v1/sovereigns/{id}/k8s/exec/{ns}/{pod}/{container}/session
//	GET  /api/v1/sovereigns/{id}/sessions
//	GET  /api/v1/sovereigns/{id}/sessions/{sessionId}/replay
//
// Auth contract (REUSES the canonical seam map):
//
//   - POST /session ........ tier-developer or higher
//                            (workloads.exec/console per EPIC-3 §6.2);
//                            mirrors the lenient applicationInstall
//                            gate — claims==nil falls through to allow,
//                            session middleware is the source of truth.
//   - GET  /sessions ....... tier-admin or higher (same surface as
//                            other session-listing tooling).
//   - GET  /sessions/{}/replay ........ admin OR owner tier
//                                       (`sessions.playback`).
//
// Per ADR-0001 §3 + §11 the session metadata travels on
// `catalyst.audit` — `guacamole-session-opened`, `-closed`, `-replayed`.
// The audit Bus from EPIC-3 U5-U8 (#1157) is the in-process surface;
// the same Publisher fan-out forwards to NATS when configured.
//
// Per ADR-0001 §11 there is exactly one Guacamole per Sovereign — the
// embed URL is constructed from the per-deployment Guacamole hostname
// stored on the deployment record (or derived via the standard
// `guacamole.<sov-fqdn>` convention when not set). NEVER cross-Sovereign.
package handler

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/audit"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// ── Audit-type names ─────────────────────────────────────────────────

const (
	// AuditTypeGuacamoleSessionOpened fires when the catalyst-api creates
	// a Guacamole connection record on behalf of an operator clicking
	// "Open Shell" in the Pod Exec tab.
	AuditTypeGuacamoleSessionOpened = "guacamole-session-opened"

	// AuditTypeGuacamoleSessionClosed fires when an open session ends
	// (either operator-driven, idle-timeout, or controller-detected).
	// Reserved for slice U-Audit's later wiring to Guacamole's webhook
	// (see G1 chart — recording `OnDisconnect` posts back here).
	AuditTypeGuacamoleSessionClosed = "guacamole-session-closed"

	// AuditTypeGuacamoleSessionReplayed fires when a session recording
	// is fetched for playback. The replay URL is a one-shot token issued
	// by Guacamole's REST API and SHOULD only be embedded for the
	// signed-in operator who requested it.
	AuditTypeGuacamoleSessionReplayed = "guacamole-session-replayed"
)

// IsGuacamoleAuditType is the audit-type predicate companion to
// `IsRBACAuditType` and `IsContinuumAuditType`. Listing/SSE handlers
// pass this into `audit.Bus.List`/`Subscribe` to filter the ring buffer
// to just session events.
func IsGuacamoleAuditType(t string) bool {
	switch t {
	case AuditTypeGuacamoleSessionOpened,
		AuditTypeGuacamoleSessionClosed,
		AuditTypeGuacamoleSessionReplayed:
		return true
	}
	return false
}

// ── Wire shapes ──────────────────────────────────────────────────────

// k8sExecSessionRequest is the POST body for opening a shell session.
type k8sExecSessionRequest struct {
	// Command is the optional argv to exec. When omitted the session
	// defaults to `["/bin/sh"]`.
	Command []string `json:"command,omitempty"`
}

// k8sExecSessionResponse is the wire shape returned to the UI.
type k8sExecSessionResponse struct {
	// SessionID is the catalyst-api's UUID for the session row in the
	// audit log; the UI uses it to drive subsequent /replay calls.
	SessionID string `json:"sessionId"`
	// ConnectionID is the Guacamole-issued connection identifier. The UI
	// passes this back to the Guacamole iframe via embedURL.
	ConnectionID string `json:"connectionId"`
	// EmbedURL — the iframe `src` the UI mounts (Guacamole-issued
	// short-lived auth token in the URL).
	EmbedURL string `json:"embedURL"`
	// Pod / Container / Namespace echo so the UI doesn't have to track
	// them across the round-trip.
	Namespace string `json:"namespace"`
	Pod       string `json:"pod"`
	Container string `json:"container"`
	// FallbackWebSocketURL — the X1-style direct-WebSocket URL the UI
	// switches to when the iframe fails (per E2 brief). Always populated
	// so the UI can pre-stage the fallback handler.
	FallbackWebSocketURL string `json:"fallbackWebSocketUrl,omitempty"`
	// Recording — true when guacamole's recording PVC is mounted (so
	// the UI surfaces "session recording: ON" badge).
	Recording bool `json:"recording"`
	// Issued — RFC3339 timestamp of the session creation.
	Issued string `json:"issued"`
}

// k8sExecSessionListItem mirrors the row shape SessionsPage renders.
type k8sExecSessionListItem struct {
	SessionID          string `json:"sessionId"`
	Namespace          string `json:"namespace"`
	Pod                string `json:"pod"`
	Container          string `json:"container"`
	User               string `json:"user"`
	Started            string `json:"started"`
	Ended              string `json:"ended,omitempty"`
	DurationSeconds    int    `json:"durationSeconds"`
	RecordingAvailable bool   `json:"recordingAvailable"`
}

// k8sExecSessionList is the GET /sessions response shape.
type k8sExecSessionList struct {
	Items     []k8sExecSessionListItem `json:"items"`
	Total     int                      `json:"total"`
	Page      int                      `json:"page"`
	PageSize  int                      `json:"pageSize"`
	NextPage  int                      `json:"nextPage,omitempty"`
}

// k8sExecSessionReplay is the GET /sessions/{id}/replay response.
type k8sExecSessionReplay struct {
	SessionID string `json:"sessionId"`
	EmbedURL  string `json:"embedURL"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// ── Guacamole client interface ───────────────────────────────────────

// GuacamoleClient is the narrow surface k8s_exec needs against the
// per-Sovereign Apache Guacamole instance. Production main.go binds
// this to a real Guacamole REST API client; tests bind a recorder.
//
// Per ADR-0001 §11 (one Guacamole per Sovereign) every method takes a
// `sovereignID` so the client knows which deployment's Guacamole base
// URL to dial. A nil GuacamoleClient on the Handler causes /session
// endpoints to return 503 — the mode-toggle is on the chart's
// `enabled: false` default.
type GuacamoleClient interface {
	// CreateSession provisions a new Guacamole connection record and
	// issues a short-lived auth token. Returns the embed URL the UI
	// should iframe + the underlying connection identifier.
	CreateSession(sovereignID string, p GuacamoleSessionParams) (GuacamoleSession, error)

	// ListSessions returns recorded sessions for the Sovereign,
	// optionally filtered by pod / user / time-window. Pagination via
	// (offset, limit).
	ListSessions(sovereignID string, f GuacamoleListFilter) ([]GuacamoleSession, int, error)

	// GetReplay returns a one-shot replay embed URL for the session.
	// Empty URL with Available=false means recording missing or expired.
	GetReplay(sovereignID, sessionID string) (GuacamoleReplay, error)
}

// GuacamoleSessionParams is the per-session create input.
type GuacamoleSessionParams struct {
	Namespace string
	Pod       string
	Container string
	Command   []string
	User      string
}

// GuacamoleSession is the per-session Guacamole metadata returned to
// the catalyst-api.
type GuacamoleSession struct {
	SessionID            string
	ConnectionID         string
	EmbedURL             string
	FallbackWebSocketURL string
	Recording            bool
	Started              time.Time
	Ended                time.Time
	User                 string
	Namespace            string
	Pod                  string
	Container            string
	RecordingAvailable   bool
}

// GuacamoleListFilter is the GET /sessions filter envelope.
type GuacamoleListFilter struct {
	From   time.Time
	To     time.Time
	Pod    string
	User   string
	Offset int
	Limit  int
}

// GuacamoleReplay is the GET /sessions/{id}/replay return shape.
type GuacamoleReplay struct {
	EmbedURL  string
	Available bool
	Reason    string
}

// SetGuacamoleClient wires the Guacamole HTTP client into the Handler.
// Called from main.go at startup once `CATALYST_GUACAMOLE_URL` is set.
// Nil-tolerant — when nil, all three /exec endpoints return 503.
func (h *Handler) SetGuacamoleClient(c GuacamoleClient) {
	h.guacamoleMu.Lock()
	defer h.guacamoleMu.Unlock()
	h.guacamole = c
}

// guacamoleClient returns the wired client (nil when unconfigured).
func (h *Handler) guacamoleClient() GuacamoleClient {
	h.guacamoleMu.RLock()
	defer h.guacamoleMu.RUnlock()
	return h.guacamole
}

// ── In-memory fallback session store ─────────────────────────────────
//
// When no GuacamoleClient is wired the handler falls back to a tiny
// in-memory session store so the UI can still demonstrate the flow
// against a chroot Sovereign or a CI environment without standing up
// Guacamole. The store records every session in the audit Bus + the
// /sessions endpoint reads back from it. This is NOT a substitute for
// Guacamole — actual recording requires the real chart deployed.
//
// Production deployments wire SetGuacamoleClient and the in-memory
// store stays empty.

type inMemoryGuacamoleStore struct {
	mu       sync.RWMutex
	sessions map[string][]GuacamoleSession // key: sovereignID
}

func newInMemoryGuacamoleStore() *inMemoryGuacamoleStore {
	return &inMemoryGuacamoleStore{sessions: make(map[string][]GuacamoleSession)}
}

func (s *inMemoryGuacamoleStore) Add(sovereignID string, sess GuacamoleSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sovereignID] = append(s.sessions[sovereignID], sess)
}

func (s *inMemoryGuacamoleStore) List(sovereignID string, f GuacamoleListFilter) ([]GuacamoleSession, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	all := s.sessions[sovereignID]
	out := make([]GuacamoleSession, 0, len(all))
	for _, ss := range all {
		if !f.From.IsZero() && ss.Started.Before(f.From) {
			continue
		}
		if !f.To.IsZero() && ss.Started.After(f.To) {
			continue
		}
		if f.Pod != "" && ss.Pod != f.Pod {
			continue
		}
		if f.User != "" && ss.User != f.User {
			continue
		}
		out = append(out, ss)
	}
	// Newest-first.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	total := len(out)
	if f.Offset > 0 {
		if f.Offset >= len(out) {
			return nil, total
		}
		out = out[f.Offset:]
	}
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, total
}

func (s *inMemoryGuacamoleStore) Get(sovereignID, sessionID string) (GuacamoleSession, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, ss := range s.sessions[sovereignID] {
		if ss.SessionID == sessionID {
			return ss, true
		}
	}
	return GuacamoleSession{}, false
}

// ensureInMemoryStore lazy-allocates the store on first use. Called
// from the handler hot path only when no GuacamoleClient is wired.
func (h *Handler) ensureInMemoryStore() *inMemoryGuacamoleStore {
	h.guacamoleMu.Lock()
	defer h.guacamoleMu.Unlock()
	if h.guacamoleStore == nil {
		h.guacamoleStore = newInMemoryGuacamoleStore()
	}
	return h.guacamoleStore
}

// ── HandleK8sExecSession ─────────────────────────────────────────────

// HandleK8sExecSession — POST
//
//	/api/v1/sovereigns/{id}/k8s/exec/{ns}/{pod}/{container}/session
//
// Creates a Guacamole connection for the operator and returns the
// embed URL. Authorization: tier-developer or higher.
func (h *Handler) HandleK8sExecSession(w http.ResponseWriter, r *http.Request) {
	sovereignID := chi.URLParam(r, "id")
	ns := chi.URLParam(r, "ns")
	pod := chi.URLParam(r, "pod")
	container := chi.URLParam(r, "container")
	if sovereignID == "" || ns == "" || pod == "" {
		writeBadRequest(w, "missing-path-params", "id, ns, pod are required")
		return
	}
	// qa-loop iter-9 Fix #43, Cluster-A (TC-376): the canonical
	// kubectl-style alias POST /k8s/pods/{ns}/{pod}/exec omits
	// `{container}`. Default to the conventional first-container name
	// when the URL doesn't pin one — matches `kubectl exec POD --`.
	if container == "" {
		container = "default"
	}
	sovereignID = h.resolveChrootClusterID(sovereignID)

	// Authorization FIRST (qa-loop iter-9 Fix #43, Cluster-A).
	claims := auth.ClaimsFromContext(r.Context())
	if !execSessionCallerAuthorized(claims) {
		// G78a (2026-06-01): surface ACTUAL claims in the 403 body so
		// operators can see WHY their session was rejected (catalyst-
		// admin/owner realm-role mapper might be misconfigured; tier
		// claim might be empty/viewer). This is a diagnosability
		// improvement on the way to a permanent fix.
		var rolesDbg string
		var tierDbg string
		if claims != nil {
			rolesDbg = strings.Join(claims.RealmAccess.Roles, ",")
			tierDbg = claims.Tier
		}
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":          "forbidden",
			"code":           "403",
			"detail":         "POST /k8s/exec/.../session requires realm role catalyst-admin/catalyst-owner/application-admin OR tier developer/operator/admin/owner",
			"sessionRoles":   rolesDbg,
			"sessionTier":    tierDbg,
		})
		return
	}

	var body k8sExecSessionRequest
	// Body is OPTIONAL — empty body decodes to zero-value command.
	if r.ContentLength > 0 || strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
		if err := dec.Decode(&body); err != nil && err.Error() != "EOF" {
			writeBadRequest(w, "decode-body", err.Error())
			return
		}
	}
	cmd := body.Command
	if len(cmd) == 0 {
		cmd = []string{"/bin/sh"}
	}

	user := actorFromClaims(claims)
	params := GuacamoleSessionParams{
		Namespace: ns,
		Pod:       pod,
		Container: container,
		Command:   cmd,
		User:      user,
	}

	var sess GuacamoleSession
	var err error
	if c := h.guacamoleClient(); c != nil {
		sess, err = c.CreateSession(sovereignID, params)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{
				"error":  "guacamole-create-failed",
				"detail": err.Error(),
			})
			return
		}
	} else {
		// In-memory fallback: synthesize a session with the canonical
		// `guacamole.<sov-fqdn>` URL convention so the UI's iframe
		// branch + audit trail still exercise.
		sess = synthesizeSession(sovereignID, params)
		h.ensureInMemoryStore().Add(sovereignID, sess)
	}

	// Always populate the FallbackWebSocketURL so the UI can pre-stage
	// the X1-style direct-WebSocket fallback handler. Per E2 brief.
	if sess.FallbackWebSocketURL == "" {
		sess.FallbackWebSocketURL = fallbackExecWebSocketURL(sovereignID, ns, pod, container, cmd)
	}
	if sess.Started.IsZero() {
		sess.Started = time.Now().UTC()
	}

	// Audit the session-open.
	if h.auditBus != nil {
		h.auditBus.Publish(r.Context(), audit.Event{
			AuditType:   AuditTypeGuacamoleSessionOpened,
			SovereignID: sovereignID,
			Actor:       user,
			Detail: fmt.Sprintf(
				"shell session opened: %s/%s/%s (cmd=%s)",
				ns, pod, container, strings.Join(cmd, " "),
			),
		})
	}

	writeJSON(w, http.StatusOK, k8sExecSessionResponse{
		SessionID:            sess.SessionID,
		ConnectionID:         sess.ConnectionID,
		EmbedURL:             sess.EmbedURL,
		Namespace:            ns,
		Pod:                  pod,
		Container:            container,
		FallbackWebSocketURL: sess.FallbackWebSocketURL,
		Recording:            sess.Recording,
		Issued:               sess.Started.UTC().Format(time.RFC3339),
	})
}

// ── HandleK8sSessionsList ────────────────────────────────────────────

// HandleK8sSessionsList — GET
//
//	/api/v1/sovereigns/{id}/sessions?from=&to=&pod=&user=&page=&pageSize=
//
// Lists guacamole sessions for the Sovereign. Authorization: tier-admin
// or higher.
func (h *Handler) HandleK8sSessionsList(w http.ResponseWriter, r *http.Request) {
	sovereignID := chi.URLParam(r, "id")
	if sovereignID == "" {
		writeBadRequest(w, "missing-id", "sovereign id is required")
		return
	}
	sovereignID = h.resolveChrootClusterID(sovereignID)

	claims := auth.ClaimsFromContext(r.Context())
	if !sessionsListCallerAuthorized(claims) {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":  "forbidden",
			"detail": "GET /sessions requires tier-admin or higher",
		})
		return
	}

	q := r.URL.Query()
	filter := GuacamoleListFilter{
		Pod:    q.Get("pod"),
		User:   q.Get("user"),
		Offset: 0,
		Limit:  defaultSessionsPageSize,
	}
	if v := q.Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeBadRequest(w, "bad-from", "from must be RFC3339")
			return
		}
		filter.From = t
	}
	if v := q.Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeBadRequest(w, "bad-to", "to must be RFC3339")
			return
		}
		filter.To = t
	}
	page := 1
	pageSize := defaultSessionsPageSize
	if v := q.Get("page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeBadRequest(w, "bad-page", "page must be >= 1")
			return
		}
		page = n
	}
	if v := q.Get("pageSize"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeBadRequest(w, "bad-pageSize", "pageSize must be >= 1")
			return
		}
		if n > maxSessionsPageSize {
			n = maxSessionsPageSize
		}
		pageSize = n
	}
	filter.Offset = (page - 1) * pageSize
	filter.Limit = pageSize

	var sessions []GuacamoleSession
	var total int
	if c := h.guacamoleClient(); c != nil {
		s, n, err := c.ListSessions(sovereignID, filter)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{
				"error":  "guacamole-list-failed",
				"detail": err.Error(),
			})
			return
		}
		sessions = s
		total = n
	} else {
		s, n := h.ensureInMemoryStore().List(sovereignID, filter)
		sessions = s
		total = n
	}

	resp := k8sExecSessionList{
		Items:    make([]k8sExecSessionListItem, 0, len(sessions)),
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}
	for _, ss := range sessions {
		duration := 0
		if !ss.Ended.IsZero() && !ss.Started.IsZero() {
			duration = int(ss.Ended.Sub(ss.Started).Seconds())
		}
		item := k8sExecSessionListItem{
			SessionID:          ss.SessionID,
			Namespace:          ss.Namespace,
			Pod:                ss.Pod,
			Container:          ss.Container,
			User:               ss.User,
			Started:            ss.Started.UTC().Format(time.RFC3339),
			DurationSeconds:    duration,
			RecordingAvailable: ss.RecordingAvailable,
		}
		if !ss.Ended.IsZero() {
			item.Ended = ss.Ended.UTC().Format(time.RFC3339)
		}
		resp.Items = append(resp.Items, item)
	}
	if total > page*pageSize {
		resp.NextPage = page + 1
	}
	writeJSON(w, http.StatusOK, resp)
}

// ── HandleK8sSessionReplay ───────────────────────────────────────────

// HandleK8sSessionReplay — GET
//
//	/api/v1/sovereigns/{id}/sessions/{sessionId}/replay
//
// Returns the embed URL for the session recording. Authorization:
// admin OR owner tier (`sessions.playback`).
func (h *Handler) HandleK8sSessionReplay(w http.ResponseWriter, r *http.Request) {
	sovereignID := chi.URLParam(r, "id")
	sessionID := chi.URLParam(r, "sessionId")
	if sovereignID == "" || sessionID == "" {
		writeBadRequest(w, "missing-path-params", "id and sessionId are required")
		return
	}
	sovereignID = h.resolveChrootClusterID(sovereignID)

	if !rbacRequireSovereignAdmin(w, r) {
		return
	}

	var rep GuacamoleReplay
	if c := h.guacamoleClient(); c != nil {
		r2, err := c.GetReplay(sovereignID, sessionID)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{
				"error":  "guacamole-replay-failed",
				"detail": err.Error(),
			})
			return
		}
		rep = r2
	} else {
		// In-memory fallback: derive a deterministic replay URL when
		// the session exists; otherwise 404.
		sess, ok := h.ensureInMemoryStore().Get(sovereignID, sessionID)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error":  "session-not-found",
				"detail": "no session with that id",
			})
			return
		}
		rep = GuacamoleReplay{
			EmbedURL:  fmt.Sprintf("https://guacamole.%s/#/replay/%s", sovereignFQDN(sovereignID), sess.SessionID),
			Available: sess.RecordingAvailable || sess.Recording,
		}
		if !rep.Available {
			rep.Reason = "recording disabled (in-memory fallback session)"
		}
	}

	if h.auditBus != nil {
		claims := auth.ClaimsFromContext(r.Context())
		h.auditBus.Publish(r.Context(), audit.Event{
			AuditType:   AuditTypeGuacamoleSessionReplayed,
			SovereignID: sovereignID,
			Actor:       actorFromClaims(claims),
			Detail:      fmt.Sprintf("session replay URL fetched: %s", sessionID),
		})
	}

	writeJSON(w, http.StatusOK, k8sExecSessionReplay{
		SessionID: sessionID,
		EmbedURL:  rep.EmbedURL,
		Available: rep.Available,
		Reason:    rep.Reason,
	})
}

// ── Authorization gates ──────────────────────────────────────────────

// execSessionCallerAuthorized — tier-developer or higher gate. Reuses
// applicationInstallCallerAuthorized as the base and additionally
// accepts `developer` tier (per EPIC-3 §6.2 `workloads.exec/console`).
//
// G78 #2625 (2026-05-31): the tier hierarchy per Claims.Tier comment is
// `viewer < developer < operator < admin < owner` — admin and owner are
// HIGHER than developer/operator. The fallback switch only accepted
// developer + operator, so a caller with tier=admin or owner but no
// matching realm role (e.g. fresh Keycloak session before the realm-role
// mapper has propagated) was incorrectly denied with
// "requires tier-developer or higher". Expand the switch to cover the
// full "developer or higher" hierarchy as documented.
func execSessionCallerAuthorized(claims *auth.Claims) bool {
	// Lenient nil-claims fall-through (matches every other endpoint;
	// session middleware is the source of truth for whether auth was
	// required).
	if claims == nil {
		return true
	}
	if applicationInstallCallerAuthorized(claims) {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(claims.Tier)) {
	case "developer", "operator", "admin", "owner":
		return true
	}
	return false
}

// sessionsListCallerAuthorized — tier-admin or higher gate. Mirrors
// rbacAssignCallerAuthorized.
func sessionsListCallerAuthorized(claims *auth.Claims) bool {
	if claims == nil {
		return true
	}
	return applicationInstallCallerAuthorized(claims)
}

// ── Helpers ──────────────────────────────────────────────────────────

const (
	defaultSessionsPageSize = 25
	maxSessionsPageSize     = 100
)

// synthesizeSession is the in-memory fallback session generator. It
// constructs the canonical `guacamole.<sov-fqdn>` URL shape so the UI
// shows a real-looking embed URL even when Guacamole isn't deployed.
// Recording is disabled in this mode (the bytes go nowhere) — the UI
// shows the "recording disabled" banner.
func synthesizeSession(sovereignID string, p GuacamoleSessionParams) GuacamoleSession {
	sid := newSessionID()
	cid := newConnectionID()
	now := time.Now().UTC()
	return GuacamoleSession{
		SessionID: sid,
		ConnectionID: cid,
		EmbedURL: fmt.Sprintf(
			"https://guacamole.%s/#/client/%s",
			sovereignFQDN(sovereignID),
			cid,
		),
		Recording:          false,
		RecordingAvailable: false,
		Started:            now,
		User:               p.User,
		Namespace:          p.Namespace,
		Pod:                p.Pod,
		Container:          p.Container,
	}
}

// fallbackExecWebSocketURL is the X1-style direct-WebSocket URL the UI
// switches to when the Guacamole iframe fails to load (per E2 brief).
// The handler for this URL is HandleK8sExecWebSocket below.
func fallbackExecWebSocketURL(sovereignID, ns, pod, container string, cmd []string) string {
	q := fmt.Sprintf("command=%s", strings.Join(cmd, " "))
	return fmt.Sprintf(
		"/api/v1/sovereigns/%s/k8s/exec/%s/%s/%s?%s",
		sovereignID, ns, pod, container, q,
	)
}

// sovereignFQDN returns the canonical hostname suffix for the Sovereign
// — a real deployment lookup would use the deployment record, but for
// the in-memory fallback we use a conventional shape that matches the
// chroot routing rule.
func sovereignFQDN(sovereignID string) string {
	if strings.Contains(sovereignID, ".") {
		return sovereignID
	}
	return sovereignID + ".sovereign.local"
}

// newSessionID returns a 16-byte hex session id. Crypto-random for
// audit-trail uniqueness.
func newSessionID() string { return randHex(16) }

// newConnectionID returns a 12-byte hex connection id (matches the
// shape Guacamole uses for short connection identifiers).
func newConnectionID() string { return randHex(12) }

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// Should never fail in practice; fall back to time-based id so
		// the handler doesn't 500. Determinism is not required here.
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
