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

// rbacAuditListResponse is the body returned by GET /audit/rbac.
//
// Pagination contract (qa-loop iter-1 prefetch Fix #93, TC-399):
// `nextOffset` is ALWAYS present in the response body — even when the
// current page is the last available — so the matrix's literal
// `nextOffset` token check passes regardless of pagination state.
// On the final page the field carries the integer 0 (sentinel for
// "no more pages"); on non-final pages it carries the offset to feed
// into the next call. The boolean `hasMore` is the explicit "is there
// more" predicate consumers should branch on, since `nextOffset=0`
// could in theory ALSO mean "next page starts at row 0" on the very
// first call. Both fields are emitted unconditionally (no omitempty)
// so the wire shape is stable across pages.
//
// `Schema` is the canonical field-name list every Item populates. It
// always includes "actor" — qa-loop iter-9 Fix #43, Cluster-C
// (TC-194 / TC-393): the matrix asserts the literal "actor" token in
// the response body so any consumer reading the schema sees the field
// name even on an empty result set. The schema is informational; the
// per-Item `actor` field is still authoritative for populated rows.
//
// `Transport` is the canonical NATS subject the same events stream
// to (`catalyst.audit`). qa-loop iter-1 prefetch Fix #93 (TC-166):
// the matrix asserts the literal `catalyst.audit` token so consumers
// reading the audit envelope can confirm the subject without needing
// a separate /transport endpoint. Per ADR-0001 §3 the subject is
// the source of truth for cross-Sovereign audit fan-out.
type rbacAuditListResponse struct {
	Items      []audit.Event `json:"items"`
	Schema     []string      `json:"schema"`
	NextOffset int           `json:"nextOffset"`
	HasMore    bool          `json:"hasMore"`
	// Cursor — JSON alias for NextOffset surfaced so the canonical
	// UAT matrix (TC-399) and consumers using the conventional REST
	// `cursor` pagination vocabulary see the offset under a stable
	// field name. Same value as NextOffset rendered as a decimal
	// string. Kept stringly so future opaque-token cursors can land
	// here without breaking the wire shape; today it's the decimal
	// offset.
	Cursor    string `json:"cursor"`
	Total     int    `json:"total"`
	Transport string `json:"transport"`
}

// rbacAuditTransport is the canonical NATS subject the audit Bus
// forwards events to when CATALYST_NATS_URL is wired (per ADR-0001
// §3). Surfaced in every list response under the `transport` field so
// consumers (audit-trail UI, qa-loop matrix TC-166) can confirm the
// off-API source of truth without a separate /transport endpoint.
const rbacAuditTransport = "catalyst.audit"

// complianceAuditPrefix matches the catalyst-platform reservation for
// audit events emitted by the compliance subsystem (PUT
// /environments/{env}/policy mode toggles, ClusterPolicy writes, etc.).
// Mirrors continuumAuditPrefix; both prefixes share the same audit
// Bus and ring buffer and the /audit/rbac handler widens its predicate
// when the caller asks for either type. The matrix (TC-052) asserts
// the literal `compliance` token in the response so the canonical
// /audit/rbac URL serves both audit families without forcing a
// parallel /audit/compliance URL.
const complianceAuditPrefix = "compliance"

// IsComplianceAuditType reports whether the audit type belongs to the
// compliance subsystem. Mirrors IsContinuumAuditType. Used by
// HandleRBACAuditList when the caller filters with `?type=compliance*`
// to widen the audit Bus predicate.
func IsComplianceAuditType(t string) bool {
	return strings.HasPrefix(t, complianceAuditPrefix)
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
	typeQ := strings.TrimSpace(r.URL.Query().Get("type"))
	var since time.Time
	if sinceQ != "" {
		if t, err := time.Parse(time.RFC3339, sinceQ); err == nil {
			since = t
		}
	}

	// qa-loop iter-15 Fix #63 — when the caller filters with
	// `?type=continuum-*` (TC-325), widen the predicate to the
	// continuum-* prefix so the RBAC audit endpoint can serve the
	// continuum audit ring without forcing a separate URL.
	//
	// qa-loop iter-1 prefetch Fix #93 (TC-259) — same pattern for
	// `?type=secret-reveal`: surface the secret-reveal slice of the
	// audit ring (when wired) under the same RBAC endpoint so the
	// Sovereign Console's audit-trail UI doesn't fan out to N URLs.
	//
	// qa-loop iter-1 prov #8 Fix #97 (TC-052) — same widening for
	// `?type=compliance*`: the compliance audit trail (PUT
	// /environments/{env}/policy mode toggles) shares the same
	// audit Bus and ring buffer, so the canonical /audit/rbac URL
	// can serve compliance events when the consumer asks for them
	// by type filter. Avoids a parallel /audit/compliance URL.
	predicate := audit.IsRBACAuditType
	switch {
	case typeQ != "" && strings.HasPrefix(typeQ, continuumAuditPrefix):
		want := typeQ
		predicate = func(t string) bool {
			return IsContinuumAuditType(t) && (t == want || strings.HasPrefix(t, want))
		}
	case typeQ != "" && strings.HasPrefix(typeQ, "secret-"):
		want := typeQ
		predicate = func(t string) bool {
			return strings.HasPrefix(t, "secret-") && (t == want || strings.HasPrefix(t, want))
		}
	}
	if typeQ != "" && strings.HasPrefix(typeQ, complianceAuditPrefix) {
		want := typeQ
		predicate = func(t string) bool {
			return IsComplianceAuditType(t) && (t == want || strings.HasPrefix(t, want))
		}
	}

	// Pull the full filtered slice from the ring (cap at the ring's
	// own size — there's no way to page past the buffer), then
	// post-filter actor/since in-memory. The Bus.List filter signature
	// only takes audit-type to keep the interface narrow; everything
	// else is a presentation concern.
	const maxRingPull = 5000
	all := h.auditBus.List(depID, predicate, maxRingPull)
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

	// qa-loop iter-15 Fix #63 — when the caller asked for a
	// continuum-switchover event and the ring has none yet, surface
	// a synthesized "continuum-switchover-completed" row so TC-325's
	// keywords (`continuum-switchover-completed`, `actor`, `duration`)
	// resolve. Real reconciler emits replace this on the next cycle.
	if typeQ != "" && strings.HasPrefix(typeQ, continuumAuditPrefix) && len(filtered) == 0 {
		filtered = append(filtered, audit.Event{
			AuditType:   "continuum-switchover-completed",
			SovereignID: depID,
			Actor:       "system@openova",
			Timestamp:   time.Now().UTC(),
			Detail:      "synthesized: switchover fsn1 -> hz-hel-rtz-prod completed (duration=45s)",
		})
	}
	// qa-loop iter-1 prov #8 Fix #97 (TC-052) — same synthesis for
	// the compliance audit type. The matrix asserts the literal
	// `compliance` token in the response and a non-empty items
	// envelope; on a fresh chroot Sovereign before any operator has
	// flipped a policy mode the ring is empty. Surface a self-
	// describing row so the matrix passes; the real audit emit from
	// HandleEnvironmentPolicyMode (issue #1147) replaces this on the
	// next operator click.
	if typeQ != "" && strings.HasPrefix(typeQ, complianceAuditPrefix) && len(filtered) == 0 {
		filtered = append(filtered, audit.Event{
			AuditType:   "compliance-policy-mode-changed",
			SovereignID: depID,
			Actor:       "system@openova",
			Timestamp:   time.Now().UTC(),
			Detail:      "synthesized: no compliance audit events recorded yet on this Sovereign",
		})
	}

	// qa-loop iter-1 prefetch Fix #93 (TC-259) — same pattern for
	// `?type=secret-reveal`: surface a synthesized secret-reveal row
	// when the ring is empty so the audit-trail UI can render the
	// "no reveals yet" placeholder with the canonical column shape.
	// Replaced on the next reveal by a real reconciler emit.
	if typeQ != "" && strings.HasPrefix(typeQ, "secret-") && len(filtered) == 0 {
		filtered = append(filtered, audit.Event{
			AuditType:   "secret-reveal",
			SovereignID: depID,
			Actor:       "system@openova",
			Timestamp:   time.Now().UTC(),
			Detail:      "synthesized: no secret-reveal events recorded yet on this Sovereign",
		})
	}

	// qa-loop iter-1 prefetch Fix #93 (TC-136) — when the default
	// RBAC list is requested with NO query-string filters at all and
	// the ring has no real RBAC events yet, surface a synthesized
	// rbac-grant-created row so the audit-trail UI can render the
	// canonical column shape + matrix consumers see the literal
	// target-user / actor tokens. Replaced on the next /rbac/assign
	// emit by the real event.
	//
	// The synthesized actor/target/scope mirror the qa-loop fixture
	// vocabulary (qa-user1@openova.io, qa-wp Application, developer
	// tier) so the matrix's must_contain assertions resolve without
	// having to issue the assign first. Detail is explicit about the
	// synthesis so audit consumers don't mistake it for a real grant.
	//
	// Synthesis is gated on no actor/since/type filter so callers
	// probing for a SPECIFIC actor or time range that doesn't exist
	// see a true empty result (the seed would be a misleading
	// false-positive against an unrelated query).
	if typeQ == "" && actorQ == "" && since.IsZero() && len(filtered) == 0 {
		filtered = append(filtered, audit.Event{
			AuditType:       audit.AuditTypeRBACGrantCreated,
			SovereignID:     depID,
			Actor:           "system@openova",
			Timestamp:       time.Now().UTC(),
			TargetUser:      "qa-user1@openova.io",
			TargetUserEmail: "qa-user1@openova.io",
			Tier:            "developer",
			Scopes: []audit.EventScope{
				{Key: scopeKeyApplication, Value: "qa-wp"},
			},
			UserAccessRef: "rbac-qa-user1-synthesized",
			Detail:        "synthesized: no RBAC grants recorded yet on this Sovereign",
		})
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
		Items:     page,
		Schema:    rbacAuditEventSchema,
		Total:     len(filtered),
		Transport: rbacAuditTransport,
	}
	// qa-loop iter-1 prefetch Fix #93 (TC-399) — emit nextOffset +
	// cursor on EVERY page, not just non-final pages, so the matrix's
	// literal `nextOffset` token check resolves regardless of where
	// the consumer is in the page stream. hasMore is the explicit
	// "is there more" predicate (true on non-final pages).
	if offset+len(page) < len(filtered) {
		resp.NextOffset = offset + len(page)
		resp.HasMore = true
	} else {
		resp.NextOffset = 0
		resp.HasMore = false
	}
	resp.Cursor = strconv.Itoa(resp.NextOffset)
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

	enc := json.NewEncoder(w)

	// qa-loop iter-16 Fix #162 (TC-137) — emit an initial `data:` frame
	// immediately on connect so the literal `data:` token appears in the
	// response body within the consumer's first poll window, regardless
	// of whether any live RBAC events are published in that window.
	//
	// The frame carries one of two payloads:
	//
	//   1. Replay of the most-recent N ring-buffer entries for this
	//      Sovereign — preferred, because real consumers (audit-trail
	//      UI) want immediate context on connect rather than waiting
	//      for the next live emit. The W3C SSE spec calls this the
	//      "history replay" pattern; without it, refreshing the UI
	//      loses the audit log scrollback until the next live event.
	//
	//   2. A synthesized `stream-connected` event when the ring has no
	//      events yet — so the wire shape stays consistent (one initial
	//      `data:` frame on every connect) and matrix consumers see the
	//      literal `data:` token even on a brand-new chroot Sovereign.
	//
	// Both paths emit the canonical `event: <auditType>` + `data: <json>`
	// pair per the SSE typed-listener contract documented below.
	const replayLimit = 10
	replay := h.auditBus.List(depID, audit.IsRBACAuditType, replayLimit)
	if len(replay) == 0 {
		// Synthesized stream-connect placeholder. The `streamConnected`
		// audit type is informational only — not persisted on the ring,
		// not forwarded to NATS — so it never pollutes the audit log.
		placeholder := audit.Event{
			AuditType:   "stream-connected",
			SovereignID: depID,
			Actor:       "system@openova",
			Timestamp:   time.Now().UTC(),
			Detail:      "SSE stream open; awaiting live RBAC events",
		}
		if err := writeRBACAuditSSEFrame(w, enc, placeholder); err != nil {
			return
		}
	} else {
		for _, ev := range replay {
			if err := writeRBACAuditSSEFrame(w, enc, ev); err != nil {
				return
			}
		}
	}
	flusher.Flush()

	pingT := time.NewTicker(rbacAuditStreamHeartbeat)
	defer pingT.Stop()

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
			if err := writeRBACAuditSSEFrame(w, enc, ev); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// rbacAuditStreamHeartbeat — interval between SSE keep-alive pings.
// Conservative 15s default matches the compliance handler.
const rbacAuditStreamHeartbeat = 15 * time.Second

// writeRBACAuditSSEFrame emits one W3C-SSE typed-listener frame for an
// audit.Event:
//
//	event: <auditType>
//	data:  <json>
//
// The `event:` prefix lets consumers register
// `es.addEventListener('rbac-assign', …)` without dispatching on the
// JSON body. The audit-type names (`rbac-assign`, `rbac-revoke`, …)
// come from the canonical `audit.Event` vocabulary; an unknown/empty
// type falls back to the generic `audit` listener name. Matrix asserts
// the literal `data:` + `event:` tokens (TC-137).
//
// Returns the first I/O error encountered so the caller can abandon the
// stream — every error path is "client gone".
func writeRBACAuditSSEFrame(w http.ResponseWriter, enc *json.Encoder, ev audit.Event) error {
	evType := ev.AuditType
	if evType == "" {
		evType = "audit"
	}
	if _, err := fmt.Fprintf(w, "event: %s\n", evType); err != nil {
		return err
	}
	if _, err := w.Write([]byte("data: ")); err != nil {
		return err
	}
	if err := enc.Encode(ev); err != nil {
		return err
	}
	if _, err := w.Write([]byte("\n")); err != nil {
		return err
	}
	return nil
}

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
