// Package handler — shells/issue endpoint.
//
// HandleShellsIssue is the canonical UAT-matrix vocabulary surface for
// "open a remote-shell session into a Sovereign-side Pod". It accepts
// the same business inputs as HandleK8sExecSession (Sovereign ID,
// namespace, pod, optional container) but exposes them as query
// parameters (matrix shape) and returns the matrix-canonical response
// fields `sessionId`, `guacamoleUrl`, `recordingPath`.
//
// Per qa-loop iter-7 Fix #39 — closes the URL+vocabulary mismatch
// between the test matrix's `POST /sovereigns/{id}/shells/issue?...`
// shape and the existing chi-routed `POST /sovereigns/{id}/k8s/exec/{ns}/{pod}/{container}/session`
// path. Both routes share the underlying business logic; only the
// router-binding shape + the JSON wire field names differ.
//
// Contract:
//
//	POST /api/v1/sovereigns/{id}/shells/issue?namespace=&pod=&container=
//
// Query params:
//
//	namespace  — required; target Pod's namespace
//	pod        — required; target Pod name
//	container  — optional; defaults to first container if omitted
//
// Optional JSON body (same shape as k8s_exec.go's k8sExecSessionRequest):
//
//	{ "command": ["/bin/sh"] }
//
// Response (HTTP 200):
//
//	{
//	  "sessionId":     "sess-...",      // matrix-canonical `sessionId`
//	  "guacamoleUrl":  "https://...",   // matrix-canonical (was `embedURL`)
//	  "recordingPath": "/recordings/...", // matrix-canonical
//	  "namespace":     "...",
//	  "pod":           "...",
//	  "container":     "...",
//	  "fallbackWebSocketUrl": "...",
//	  "recording":     true|false,
//	  "issued":        "2026-05-09T...Z"
//	}
//
// Authorization: tier-developer or higher (same as HandleK8sExecSession).
//
// Audit: emits `guacamole-session-opened` (same audit type — unified
// audit ledger across both URL shapes so SREs see one stream).

package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/audit"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// shellsIssueResponse is the wire shape returned to callers using the
// matrix-canonical `/shells/issue` URL form. Field names mirror the
// qa-loop test matrix (TC-228, TC-246) verbatim; the underlying
// business object is shared with k8s_exec.go's k8sExecSessionResponse,
// so behavior parity is enforced at the GuacamoleSession level.
type shellsIssueResponse struct {
	SessionID            string `json:"sessionId"`
	GuacamoleURL         string `json:"guacamoleUrl"`
	RecordingPath        string `json:"recordingPath"`
	ConnectionID         string `json:"connectionId,omitempty"`
	Namespace            string `json:"namespace"`
	Pod                  string `json:"pod"`
	Container            string `json:"container"`
	FallbackWebSocketURL string `json:"fallbackWebSocketUrl,omitempty"`
	Recording            bool   `json:"recording"`
	Issued               string `json:"issued"`
}

// HandleShellsIssue — POST /api/v1/sovereigns/{id}/shells/issue?...
//
// See package doc-comment for the full contract. This handler is a
// thin matrix-canonical re-projection over the same underlying
// business logic as HandleK8sExecSession (k8s_exec.go); both share the
// GuacamoleClient interface and the in-memory fallback store, so they
// stay in lock-step on session storage + audit emission.
func (h *Handler) HandleShellsIssue(w http.ResponseWriter, r *http.Request) {
	sovereignID := chi.URLParam(r, "id")
	if sovereignID == "" {
		writeBadRequest(w, "missing-id", "sovereign id is required in path")
		return
	}
	sovereignID = h.resolveChrootClusterID(sovereignID)

	q := r.URL.Query()
	ns := strings.TrimSpace(q.Get("namespace"))
	pod := strings.TrimSpace(q.Get("pod"))
	container := strings.TrimSpace(q.Get("container"))
	if ns == "" || pod == "" {
		writeBadRequest(w, "missing-query-params",
			"namespace and pod query params are required (container is optional)")
		return
	}

	claims := auth.ClaimsFromContext(r.Context())
	if !execSessionCallerAuthorized(claims) {
		// 403 — matches the matrix viewer-cookie expectation in TC-245.
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":  "forbidden",
			"detail": "POST /shells/issue requires tier-developer or higher",
		})
		return
	}

	// Optional JSON body — same shape as k8s_exec.go.
	var body k8sExecSessionRequest
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

	// Identical business-logic branch as HandleK8sExecSession — call
	// the GuacamoleClient when wired, fall back to the in-memory
	// synthesizer otherwise. This keeps both URL shapes' behavior
	// coupled at the storage layer; an audit query for "all sessions
	// opened today" sees both surfaces.
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
		sess = synthesizeSession(sovereignID, params)
		h.ensureInMemoryStore().Add(sovereignID, sess)
	}

	if sess.FallbackWebSocketURL == "" {
		sess.FallbackWebSocketURL = fallbackExecWebSocketURL(sovereignID, ns, pod, container, cmd)
	}
	if sess.Started.IsZero() {
		sess.Started = time.Now().UTC()
	}

	// Recording path — guacd writes session captures here (per
	// platform/guacamole/chart/values.yaml `recordings.mountPath`,
	// canonical default `/recordings`). The session ID anchors the
	// per-session subdirectory so SREs can stream back individual
	// recordings via /sessions/{id}/replay.
	recordingPath := fmt.Sprintf("/recordings/%s.guac", sess.SessionID)

	if h.auditBus != nil {
		h.auditBus.Publish(r.Context(), audit.Event{
			AuditType:   AuditTypeGuacamoleSessionOpened,
			SovereignID: sovereignID,
			Actor:       user,
			Detail: fmt.Sprintf(
				"shell session opened (shells/issue): %s/%s/%s (cmd=%s)",
				ns, pod, container, strings.Join(cmd, " "),
			),
		})
	}

	writeJSON(w, http.StatusOK, shellsIssueResponse{
		SessionID:            sess.SessionID,
		GuacamoleURL:         sess.EmbedURL,
		RecordingPath:        recordingPath,
		ConnectionID:         sess.ConnectionID,
		Namespace:            ns,
		Pod:                  pod,
		Container:            container,
		FallbackWebSocketURL: sess.FallbackWebSocketURL,
		Recording:            sess.Recording,
		Issued:               sess.Started.UTC().Format(time.RFC3339),
	})
}
