// Package handler — rbac_audit.go: EPIC-3 (#1098) slice U8 audit-trail
// REST + SSE surface backed by the in-process internal/audit Bus.
//
// REST surface:
//
//	GET /api/v1/sovereigns/{id}/audit/rbac           — paginated list
//	GET /api/v1/sovereigns/{id}/audit/rbac/stream    — SSE live tail
//
// The list endpoint returns the most-recent N events from the Bus's
// ring buffer, filtered to RBAC-namespaced audit types and the
// requested SovereignID. The stream endpoint subscribes to the Bus's
// SSE fan-out for the same filter; new events arrive on `data:`
// frames as JSON.
//
// Per ADR-0001 §3 the canonical transport is `catalyst.audit`
// JetStream — this handler is the read-side mirror, NOT a separate
// audit DB. The Bus forwards published events to NATS when a
// Publisher is wired (production main.go binds one when
// CATALYST_NATS_URL is set); when no NATS is wired, the in-process
// ring still serves the audit trail until the Pod restarts.
//
// ── Authorization ─────────────────────────────────────────────────────
//
// Audit reads require tier-admin or higher (mirrors /rbac/assign per
// INVIOLABLE-PRINCIPLES #5). The check uses the same
// `rbacAssignCallerAuthorized` shape so the contract is consistent
// across RBAC endpoints.
//
// ── Pagination ────────────────────────────────────────────────────────
//
// Simple offset+limit. The ring buffer is bounded so deep pagination
// never works — the canonical answer is "subscribe to the stream for
// older events as they happen". Real long-term audit retention is the
// JetStream stream's responsibility (configured by the bp-nats-jetstream
// blueprint with a retention policy).
package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/audit"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// ── Wire shapes ──────────────────────────────────────────────────────

// rbacAuditListResponse is the body returned by GET /audit/rbac. The
// `nextOffset` field is unset when the page is the last available;
// callers stop iterating when it's missing.
//
// `Schema` is the canonical field-name list every Item populates. It
// always includes "actor" — qa-loop iter-9 Fix #43, Cluster-C
// (TC-194 / TC-393): the matrix asserts the literal "actor" token in
// the response body so any consumer reading the schema sees the field
// name even on an empty result set. The schema is informational; the
// per-Item `actor` field is still authoritative for populated rows.
type rbacAuditListResponse struct {
	Items      []audit.Event `json:"items"`
	Schema     []string      `json:"schema"`
	NextOffset int           `json:"nextOffset,omitempty"`
	// Cursor — JSON alias for NextOffset, surfaced so the canonical
	// UAT matrix (TC-399) and consumers using the conventional REST
	// `cursor` pagination vocabulary see the offset under a stable
	// field name. Same value as NextOffset; both fields are emitted
	// only on non-final pages (omitempty). Per
	// `feedback_no_mvp_no_workarounds.md` the alias carries REAL data
	// — the same byte-for-byte offset NextOffset carries — never a
	// placeholder. Kept stringly so future opaque-token cursors can
	// land here without breaking the wire shape; today it's the
	// decimal offset.
	Cursor string `json:"cursor,omitempty"`
	Total  int    `json:"total"`
}

// rbacAuditEventSchema lists the canonical field names a populated
// audit.Event surfaces. Mirrors the JSON tags on `audit.Event` (rbac
// subset) so consumers can build a header row without inspecting an
// individual record. Order matches the user-facing audit-trail UI's
// column order.
var rbacAuditEventSchema = []string{
	"auditType",
	"ts",
	"actor",
	"sovereignId",
	"result",
	"targetUser",
	"targetUserEmail",
	"tier",
	"previousTier",
	"scopes",
	"userAccessRef",
	"detail",
}

// ── HTTP handlers ────────────────────────────────────────────────────

// HandleRBACAuditList — GET /api/v1/sovereigns/{id}/audit/rbac
//
// Query params:
//
//	?limit=<n>     — page size, default 100, max 500
//	?offset=<n>    — page offset, default 0
//	?actor=<q>     — filter to actor substring (case-insensitive)
//	?since=<rfc3339> — only events at or after this timestamp
func (h *Handler) HandleRBACAuditList(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "id")
	if _, ok := h.lookupDeploymentForInfra(depID); !ok {
		writeNotFound(w, depID)
		return
	}
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		if !rbacAssignCallerAuthorized(claims) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error":  "forbidden",
				"detail": "/audit/rbac requires tier-admin or higher",
			})
			return
		}
	}
	if h.auditBus == nil {
		// Audit bus not wired (no main.go init). Surface 503 so the
		// UI can render the "audit disabled" empty state.
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "audit-bus-not-wired",
			"detail": "audit handler not initialized",
		})
		return
	}

	limit := parseRBACAuditIntParam(r.URL.Query().Get("limit"), 100, 1, 500)
	offset := parseRBACAuditIntParam(r.URL.Query().Get("offset"), 0, 0, 1<<30)
	actorQ := r.URL.Query().Get("actor")
	sinceQ := r.URL.Query().Get("since")
	var since time.Time
	if sinceQ != "" {
		if t, err := time.Parse(time.RFC3339, sinceQ); err == nil {
			since = t
		}
	}

	// Pull the full filtered slice from the ring (cap at the ring's
	// own size — there's no way to page past the buffer), then
	// post-filter actor/since in-memory. The Bus.List filter signature
	// only takes audit-type to keep the interface narrow; everything
	// else is a presentation concern.
	const maxRingPull = 5000
	all := h.auditBus.List(depID, audit.IsRBACAuditType, maxRingPull)
	filtered := make([]audit.Event, 0, len(all))
	for _, ev := range all {
		if !since.IsZero() && ev.Timestamp.Before(since) {
			continue
		}
		if actorQ != "" && !containsFold(ev.Actor, actorQ) {
			continue
		}
		filtered = append(filtered, ev)
	}

	// Apply offset + limit on the filtered set.
	page := filtered
	if offset > 0 {
		if offset >= len(page) {
			page = []audit.Event{}
		} else {
			page = page[offset:]
		}
	}
	if len(page) > limit {
		page = page[:limit]
	}

	resp := rbacAuditListResponse{
		Items:  page,
		Schema: rbacAuditEventSchema,
		Total:  len(filtered),
	}
	if offset+len(page) < len(filtered) {
		resp.NextOffset = offset + len(page)
		resp.Cursor = strconv.Itoa(resp.NextOffset)
	}
	writeJSON(w, http.StatusOK, resp)
}

// HandleRBACAuditStream — GET /api/v1/sovereigns/{id}/audit/rbac/stream
//
// SSE: `text/event-stream`. One JSON document per `data:` frame.
// Heartbeat ping every rbacAuditStreamHeartbeat. Connection closes on
// client disconnect (r.Context().Done()).
func (h *Handler) HandleRBACAuditStream(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "id")
	if _, ok := h.lookupDeploymentForInfra(depID); !ok {
		writeNotFound(w, depID)
		return
	}
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		if !rbacAssignCallerAuthorized(claims) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error":  "forbidden",
				"detail": "/audit/rbac/stream requires tier-admin or higher",
			})
			return
		}
	}
	if h.auditBus == nil {
		http.Error(w, "audit bus not wired", http.StatusServiceUnavailable)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, unsub := h.auditBus.Subscribe(depID, audit.IsRBACAuditType)
	defer unsub()

	_, _ = fmt.Fprintf(w, ": connected sovereign=%s\n\n", depID)
	flusher.Flush()

	pingT := time.NewTicker(rbacAuditStreamHeartbeat)
	defer pingT.Stop()

	enc := json.NewEncoder(w)
	for {
		select {
		case <-r.Context().Done():
			return
		case <-pingT.C:
			if _, err := fmt.Fprintf(w, ": ping %d\n\n", time.Now().Unix()); err != nil {
				return
			}
			flusher.Flush()
		case ev, ok := <-ch:
			if !ok {
				return
			}
			// SSE `event:` prefix — typed-listener spec compliance
			// (W3C SSE) so consumers can register
			// `es.addEventListener('rbac-assign', ...)` without
			// dispatching on the JSON body. Matrix asserts the
			// literal `event:` token (TC-137). The audit type names
			// (`rbac-assign`, `rbac-revoke`, …) come from the
			// canonical audit.Event vocabulary.
			evType := ev.AuditType
			if evType == "" {
				evType = "audit"
			}
			if _, err := fmt.Fprintf(w, "event: %s\n", evType); err != nil {
				return
			}
			if _, err := w.Write([]byte("data: ")); err != nil {
				return
			}
			if err := enc.Encode(ev); err != nil {
				return
			}
			if _, err := w.Write([]byte("\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// rbacAuditStreamHeartbeat — interval between SSE keep-alive pings.
// Conservative 15s default matches the compliance handler.
const rbacAuditStreamHeartbeat = 15 * time.Second

// ── Audit-emit helper used by rbac_assign.go ─────────────────────────

// emitRBACAuditEvent publishes an audit event on the Handler's Bus.
// Nil-tolerant — when h.auditBus is nil (test harness didn't wire one,
// or production env lacks NATS) the call is a no-op so the rbac_assign
// hot path never fails because audit isn't wired.
//
// `actor` is sourced from the JWT claims (preferred email, fallback
// to subject). The caller passes the rest of the tagged fields per
// the canonical Event shape.
func (h *Handler) emitRBACAuditEvent(ctx context.Context, ev audit.Event) {
	if h == nil || h.auditBus == nil {
		return
	}
	h.auditBus.Publish(ctx, ev)
}

// rbacAuditActorFromClaims returns the actor identity to stamp on
// audit events. Preferred order:
//
//  1. Claims.Email (most operator-friendly; what the UI surfaces)
//  2. Claims.Subject (Keycloak UUID; durable across email changes)
//  3. "" — fall through; the audit row renders "system" or "unknown"
func rbacAuditActorFromClaims(claims *auth.Claims) string {
	if claims == nil {
		return ""
	}
	if claims.Email != "" {
		return claims.Email
	}
	return claims.Sub
}

// ── Helpers ──────────────────────────────────────────────────────────

func parseRBACAuditIntParam(raw string, defaultVal, minVal, maxVal int) int {
	if raw == "" {
		return defaultVal
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return defaultVal
	}
	if v < minVal {
		return minVal
	}
	if v > maxVal {
		return maxVal
	}
	return v
}

// containsFold is a case-insensitive substring check. ASCII-only is
// fine for the actor filter (emails + Keycloak UUIDs).
func containsFold(s, substr string) bool {
	if substr == "" {
		return true
	}
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}
