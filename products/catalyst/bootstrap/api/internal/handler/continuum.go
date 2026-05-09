// Package handler — continuum.go: EPIC-6 Slice U-DR-1 (#1101) Continuum
// DR REST + SSE surface backed by the dynamic client + the audit Bus.
//
// REST surface added on top of the K-Cont-2 reconciler:
//
//	GET  /api/v1/sovereigns/{id}/continuums/{name}
//	     — Continuum CR snapshot (spec + status) for the DR section
//	POST /api/v1/sovereigns/{id}/continuums/{name}/switchover
//	     — operator-initiated switchover; patches spec.switchover.requested
//	       and spec.switchover.targetRegion, returns 202 Accepted
//	POST /api/v1/sovereigns/{id}/continuums/{name}/failback
//	     — operator-requested failback; patches spec.failback.requested
//	POST /api/v1/sovereigns/{id}/continuums/{name}/failback/approve
//	     — sovereign-admin gate flip; patches spec.failback.approved
//	GET  /api/v1/sovereigns/{id}/audit/continuum
//	     — paginated list of Continuum audit events (mirrors /audit/rbac)
//	GET  /api/v1/sovereigns/{id}/audit/continuum/stream
//	     — SSE live tail of Continuum audit events
//
// Architecture rules:
//
//   - Per ADR-0001 §2.7 the Continuum CR is the source of truth — this
//     handler patches spec; the K-Cont-2 reconciler executes the 7-step
//     sequence and emits NATS audit events. NO direct invocation of the
//     reconciler's Sequencer from here.
//   - Per ADR-0001 §3 audit events flow through the SAME `catalyst.audit`
//     subject that slice U5-U8 reads. The handler reuses the existing
//     in-process audit.Bus + a continuum-specific predicate. The K-Cont-2
//     reconciler publishes events with audit-type prefix `continuum-*`
//     (the 9 reserved type names live in
//     core/controllers/continuum/internal/events) — production wiring
//     binds the BUS adapter so chart-deployed reconcilers fan into the
//     same Bus the catalyst-api serves /audit/continuum* from.
//   - Per INVIOLABLE-PRINCIPLES.md #5 (least-privilege, server-enforced):
//     switchover + failback require owner tier on the Application
//     (REUSES applicationInstallCallerAuthorized — same gate as PUT
//     /applications). Approve requires sovereign-admin (REUSES
//     rbacRequireSovereignAdmin). Audit-list + stream require tier-admin
//     or higher (mirrors /audit/rbac).
//   - Per docs/INVIOLABLE-PRINCIPLES.md #4 every URL is env-derived;
//     nothing is hardcoded.
//
// ── Why reuse the audit.Bus event shape ───────────────────────────────
//
// Slice U5-U8's audit.Event is a SUPERSET schema (per its file-level
// doc: "consumers tag only what's relevant"). Continuum events fit by
// stamping AuditType + Actor + SovereignID + Detail. Per-event wire
// fields specific to switchover (FromPrimary/ToPrimary/Step) are
// surfaced via Detail + a flat `target*` fields slot — the UI parses
// the auditType prefix to interpret semantics.
//
// The K-Cont-2 reconciler's events package defines the 9 reserved
// audit-type names; this handler's IsContinuumAuditType matches the
// `continuum-*` prefix so a future audit-type addition (slice F-1 may
// add 3 more) plugs in without a handler-side code change.
package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/audit"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// ContinuumGVR — the Namespaced Continuum CRD shipped at
// products/catalyst/chart/crds/continuum.yaml.
//
// Mirrors core/controllers/continuum/internal/controller.ContinuumGVR
// — duplicated here because catalyst-api intentionally avoids importing
// `core/controllers/continuum/...` (that package brings the whole
// controller-runtime dep tree). The GVR is a 3-string contract; if it
// drifts, the dynamic client returns 404 immediately on first call.
func ContinuumGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    "dr.openova.io",
		Version:  "v1",
		Resource: "continuums",
	}
}

// ── Audit-type predicate ─────────────────────────────────────────────

// continuumAuditPrefix matches the K-Cont-2 reservation in
// core/controllers/continuum/internal/events/events.go. The 9 reserved
// types all carry the `continuum-` prefix; this predicate is intentionally
// prefix-based so a future addition (F-1 may add 3 more) doesn't require
// a handler-side change.
const continuumAuditPrefix = "continuum-"

// IsContinuumAuditType reports whether `t` belongs to the Continuum
// audit-type namespace. Used by the GET /audit/continuum handler to
// filter the ring buffer + SSE stream.
//
// Per K-Cont-2 the 9 reserved types are:
//
//	continuum-switchover, continuum-failback-pending,
//	continuum-failback-completed, continuum-lease-lost,
//	continuum-lease-acquired, continuum-cnpg-lag-breach,
//	continuum-cnpg-promotable, continuum-error,
//	continuum-reconcile-success
func IsContinuumAuditType(t string) bool {
	return strings.HasPrefix(t, continuumAuditPrefix)
}

// ── Wire shapes ──────────────────────────────────────────────────────

// continuumGetResponse — body of GET /continuums/{name}. Surfaces the
// shape the UI's StatusPanel + LuaRecordView need without leaking the
// raw Unstructured.
type continuumGetResponse struct {
	Name      string                 `json:"name"`
	Namespace string                 `json:"namespace"`
	UID       string                 `json:"uid"`
	Spec      map[string]interface{} `json:"spec,omitempty"`
	Status    map[string]interface{} `json:"status,omitempty"`
}

// continuumSwitchoverRequest — body of POST .../switchover.
//
// `targetRegion` is REQUIRED (handler rejects 400 otherwise). `reason`
// is a free-form short string surfaced on the audit event's Detail
// field. The handler stamps `requestedAt = now` server-side.
type continuumSwitchoverRequest struct {
	TargetRegion string `json:"targetRegion"`
	Reason       string `json:"reason,omitempty"`
}

// continuumSwitchoverResponse — 202 Accepted body. The reconciler picks
// up the spec patch on its next loop iteration and emits the 7-step
// audit events on NATS.
type continuumSwitchoverResponse struct {
	Name         string `json:"name"`
	Namespace    string `json:"namespace"`
	TargetRegion string `json:"targetRegion"`
	Reason       string `json:"reason,omitempty"`
	RequestedAt  string `json:"requestedAt"`
	RequestedBy  string `json:"requestedBy,omitempty"`
	Message      string `json:"message"`
}

// continuumFailbackRequest — body of POST .../failback.
type continuumFailbackRequest struct {
	Reason string `json:"reason,omitempty"`
}

// continuumFailbackResponse — 202 Accepted body.
type continuumFailbackResponse struct {
	Name        string `json:"name"`
	Namespace   string `json:"namespace"`
	RequestedAt string `json:"requestedAt"`
	RequestedBy string `json:"requestedBy,omitempty"`
	// ApprovalRequired echoes the spec.failback.approvalRequired bool
	// so the UI can render the "Awaiting approval" state immediately
	// without re-fetching.
	ApprovalRequired bool   `json:"approvalRequired"`
	Message          string `json:"message"`
}

// continuumFailbackApproveResponse — 202 Accepted body.
type continuumFailbackApproveResponse struct {
	Name       string `json:"name"`
	Namespace  string `json:"namespace"`
	ApprovedAt string `json:"approvedAt"`
	ApprovedBy string `json:"approvedBy,omitempty"`
	Message    string `json:"message"`
}

// continuumAuditListResponse — body returned by GET /audit/continuum.
// Mirrors rbacAuditListResponse so the UI's audit-row renderer can
// reuse the same shape.
type continuumAuditListResponse struct {
	Items      []audit.Event `json:"items"`
	NextOffset int           `json:"nextOffset,omitempty"`
	Total      int           `json:"total"`
}

// ── HTTP handlers ────────────────────────────────────────────────────

// HandleContinuumGet — GET /api/v1/sovereigns/{id}/continuums/{name}
//
// Returns the Continuum CR's spec + status. No auth gate (read) —
// matches the lenient policy the application-status endpoint uses for
// the same reason: viewers need to see DR posture.
func (h *Handler) HandleContinuumGet(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "id")
	name := chi.URLParam(r, "name")
	if strings.TrimSpace(name) == "" {
		writeBadRequest(w, "missing-name", "continuum name is required")
		return
	}
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}
	client, err := h.sovereignDynamicClient(dep)
	if err != nil {
		writeUserAccessUnavailable(w, err)
		return
	}
	ns := strings.TrimSpace(r.URL.Query().Get("namespace"))
	cr, getErr := getContinuumCR(r.Context(), client, name, ns)
	if getErr != nil {
		if apierrors.IsNotFound(getErr) {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error":  "continuum-not-found",
				"detail": fmt.Sprintf("Continuum %q not found", name),
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "continuum-get-failed",
			"detail": getErr.Error(),
		})
		return
	}
	resp := continuumGetResponse{
		Name:      cr.GetName(),
		Namespace: cr.GetNamespace(),
		UID:       string(cr.GetUID()),
	}
	if specObj, ok, _ := unstructured.NestedMap(cr.Object, "spec"); ok {
		resp.Spec = specObj
	}
	if statusObj, ok, _ := unstructured.NestedMap(cr.Object, "status"); ok {
		resp.Status = statusObj
	}
	writeJSON(w, http.StatusOK, resp)
}

// HandleContinuumSwitchoverRequest — POST
// /api/v1/sovereigns/{id}/continuums/{name}/switchover
//
// Patches `spec.switchover.requested = true`, stamps requestedAt + the
// caller's identity in requestedBy, and writes the targetRegion. The
// K-Cont-2 reconciler observes the change on its next renew tick and
// kicks off the 7-step Sequencer.
//
// Auth: owner tier on the Application (REUSES
// applicationInstallCallerAuthorized — same gate as PUT /applications).
//
// Returns 202 Accepted; the response body echoes back what was patched
// so the UI can immediately render an "in flight" state without
// waiting for the reconciler to update status.
func (h *Handler) HandleContinuumSwitchoverRequest(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "id")
	name := chi.URLParam(r, "name")
	if strings.TrimSpace(name) == "" {
		writeBadRequest(w, "missing-name", "continuum name is required")
		return
	}
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		if !applicationInstallCallerAuthorized(claims) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error":  "forbidden",
				"detail": "POST /continuums/{name}/switchover requires owner tier on the Application",
			})
			return
		}
	}
	var body continuumSwitchoverRequest
	if !decodeMutationBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.TargetRegion) == "" {
		writeBadRequest(w, "missing-target-region", "targetRegion is required")
		return
	}
	client, err := h.sovereignDynamicClient(dep)
	if err != nil {
		writeUserAccessUnavailable(w, err)
		return
	}
	ns := strings.TrimSpace(r.URL.Query().Get("namespace"))
	cr, getErr := getContinuumCR(r.Context(), client, name, ns)
	if getErr != nil {
		if apierrors.IsNotFound(getErr) {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error":  "continuum-not-found",
				"detail": fmt.Sprintf("Continuum %q not found", name),
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "continuum-get-failed",
			"detail": getErr.Error(),
		})
		return
	}

	// Reject when the switchover targetRegion equals the current primary
	// — the reconciler would treat it as a no-op but a 409 here gives the
	// UI a clean error instead of a silent failure.
	curPrimary, _, _ := unstructured.NestedString(cr.Object, "spec", "primaryRegion")
	if curPrimary != "" && curPrimary == body.TargetRegion {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "switchover-noop",
			"detail": fmt.Sprintf("targetRegion %q already primary; nothing to switchover", body.TargetRegion),
		})
		return
	}

	now := h.continuumNow()
	requestedBy := actorFromClaims(auth.ClaimsFromContext(r.Context()))

	patched := cr.DeepCopy()
	_ = unstructured.SetNestedField(patched.Object, true, "spec", "switchover", "requested")
	_ = unstructured.SetNestedField(patched.Object, body.TargetRegion, "spec", "switchover", "targetRegion")
	_ = unstructured.SetNestedField(patched.Object, now.UTC().Format(time.RFC3339), "spec", "switchover", "requestedAt")
	if requestedBy != "" {
		_ = unstructured.SetNestedField(patched.Object, requestedBy, "spec", "switchover", "requestedBy")
	}
	if strings.TrimSpace(body.Reason) != "" {
		_ = unstructured.SetNestedField(patched.Object, body.Reason, "spec", "switchover", "reason")
	}

	if err := updateContinuumCR(r.Context(), client, patched); err != nil {
		if apierrors.IsConflict(err) {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error":  "continuum-conflict",
				"detail": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "continuum-update-failed",
			"detail": err.Error(),
		})
		return
	}

	// Publish a UI-facing audit event. K-Cont-2 emits its own
	// `continuum-switchover` event on the NATS subject when the
	// reconciler picks up the patch; this one is the operator-initiated
	// audit row so the audit trail captures who pressed the button even
	// if the reconciler hasn't run yet.
	if h.auditBus != nil {
		h.auditBus.Publish(r.Context(), audit.Event{
			AuditType:         "continuum-switchover-requested",
			SovereignID:       depID,
			Actor:             requestedBy,
			TargetApplication: continuumApplicationRef(cr),
			Detail: fmt.Sprintf(
				"switchover requested: %s → %s (reason: %s)",
				curPrimary, body.TargetRegion, displayReason(body.Reason),
			),
		})
	}

	resp := continuumSwitchoverResponse{
		Name:         patched.GetName(),
		Namespace:    patched.GetNamespace(),
		TargetRegion: body.TargetRegion,
		Reason:       body.Reason,
		RequestedAt:  now.UTC().Format(time.RFC3339),
		RequestedBy:  requestedBy,
		Message:      "switchover requested; reconciler will execute the 7-step sequence",
	}
	writeJSON(w, http.StatusAccepted, resp)
}

// HandleContinuumFailbackRequest — POST
// /api/v1/sovereigns/{id}/continuums/{name}/failback
//
// Patches `spec.failback.requested = true`. If the CR's
// `spec.failback.approvalRequired = true`, the reconciler emits
// `continuum-failback-pending` and waits for the approve endpoint
// below; otherwise the reconciler executes the same 7-step sequence in
// reverse direction immediately.
//
// Auth: owner tier on the Application (REUSES
// applicationInstallCallerAuthorized).
func (h *Handler) HandleContinuumFailbackRequest(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "id")
	name := chi.URLParam(r, "name")
	if strings.TrimSpace(name) == "" {
		writeBadRequest(w, "missing-name", "continuum name is required")
		return
	}
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		if !applicationInstallCallerAuthorized(claims) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error":  "forbidden",
				"detail": "POST /continuums/{name}/failback requires owner tier on the Application",
			})
			return
		}
	}
	// Body is OPTIONAL — the caller may POST without one. Decode only
	// when Content-Length suggests there's content; never use
	// decodeMutationBody (it 400s on empty body).
	var body continuumFailbackRequest
	if r.ContentLength > 0 || r.Header.Get("Content-Type") == "application/json" {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
		// Be lenient — an empty body decodes to a zero-value struct.
		_ = dec.Decode(&body)
	}

	client, err := h.sovereignDynamicClient(dep)
	if err != nil {
		writeUserAccessUnavailable(w, err)
		return
	}
	ns := strings.TrimSpace(r.URL.Query().Get("namespace"))
	cr, getErr := getContinuumCR(r.Context(), client, name, ns)
	if getErr != nil {
		if apierrors.IsNotFound(getErr) {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error":  "continuum-not-found",
				"detail": fmt.Sprintf("Continuum %q not found", name),
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "continuum-get-failed",
			"detail": getErr.Error(),
		})
		return
	}

	now := h.continuumNow()
	requestedBy := actorFromClaims(auth.ClaimsFromContext(r.Context()))
	approvalRequired, _, _ := unstructured.NestedBool(cr.Object, "spec", "failback", "approvalRequired")

	patched := cr.DeepCopy()
	_ = unstructured.SetNestedField(patched.Object, true, "spec", "failback", "requested")
	_ = unstructured.SetNestedField(patched.Object, now.UTC().Format(time.RFC3339), "spec", "failback", "requestedAt")
	if requestedBy != "" {
		_ = unstructured.SetNestedField(patched.Object, requestedBy, "spec", "failback", "requestedBy")
	}
	if strings.TrimSpace(body.Reason) != "" {
		_ = unstructured.SetNestedField(patched.Object, body.Reason, "spec", "failback", "reason")
	}

	if err := updateContinuumCR(r.Context(), client, patched); err != nil {
		if apierrors.IsConflict(err) {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error":  "continuum-conflict",
				"detail": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "continuum-update-failed",
			"detail": err.Error(),
		})
		return
	}

	if h.auditBus != nil {
		h.auditBus.Publish(r.Context(), audit.Event{
			AuditType:         "continuum-failback-requested",
			SovereignID:       depID,
			Actor:             requestedBy,
			TargetApplication: continuumApplicationRef(cr),
			Detail: fmt.Sprintf(
				"failback requested (approvalRequired: %t, reason: %s)",
				approvalRequired, displayReason(body.Reason),
			),
		})
	}

	msg := "failback requested; reconciler will execute the reverse switchover"
	if approvalRequired {
		msg = "failback requested; awaiting sovereign-admin approval"
	}
	resp := continuumFailbackResponse{
		Name:             patched.GetName(),
		Namespace:        patched.GetNamespace(),
		RequestedAt:      now.UTC().Format(time.RFC3339),
		RequestedBy:      requestedBy,
		ApprovalRequired: approvalRequired,
		Message:          msg,
	}
	writeJSON(w, http.StatusAccepted, resp)
}

// HandleContinuumFailbackApprove — POST
// /api/v1/sovereigns/{id}/continuums/{name}/failback/approve
//
// Patches `spec.failback.approved = true`. Required only when the CR's
// `spec.failback.approvalRequired = true`; the K-Cont-2 reconciler
// blocks on this flag before running the failback sequence.
//
// Auth: sovereign-admin (admin or owner tier — REUSES
// rbacRequireSovereignAdmin).
func (h *Handler) HandleContinuumFailbackApprove(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "id")
	name := chi.URLParam(r, "name")
	if strings.TrimSpace(name) == "" {
		writeBadRequest(w, "missing-name", "continuum name is required")
		return
	}
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}
	if !rbacRequireSovereignAdmin(w, r) {
		// Response already written by helper.
		return
	}
	client, err := h.sovereignDynamicClient(dep)
	if err != nil {
		writeUserAccessUnavailable(w, err)
		return
	}
	ns := strings.TrimSpace(r.URL.Query().Get("namespace"))
	cr, getErr := getContinuumCR(r.Context(), client, name, ns)
	if getErr != nil {
		if apierrors.IsNotFound(getErr) {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error":  "continuum-not-found",
				"detail": fmt.Sprintf("Continuum %q not found", name),
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "continuum-get-failed",
			"detail": getErr.Error(),
		})
		return
	}

	now := h.continuumNow()
	approver := actorFromClaims(auth.ClaimsFromContext(r.Context()))

	patched := cr.DeepCopy()
	_ = unstructured.SetNestedField(patched.Object, true, "spec", "failback", "approved")
	_ = unstructured.SetNestedField(patched.Object, now.UTC().Format(time.RFC3339), "spec", "failback", "approvedAt")
	if approver != "" {
		_ = unstructured.SetNestedField(patched.Object, approver, "spec", "failback", "approvedBy")
	}

	if err := updateContinuumCR(r.Context(), client, patched); err != nil {
		if apierrors.IsConflict(err) {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error":  "continuum-conflict",
				"detail": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "continuum-update-failed",
			"detail": err.Error(),
		})
		return
	}

	if h.auditBus != nil {
		h.auditBus.Publish(r.Context(), audit.Event{
			AuditType:         "continuum-failback-approved",
			SovereignID:       depID,
			Actor:             approver,
			TargetApplication: continuumApplicationRef(cr),
			Detail:            fmt.Sprintf("failback approved by %s", approver),
		})
	}

	resp := continuumFailbackApproveResponse{
		Name:       patched.GetName(),
		Namespace:  patched.GetNamespace(),
		ApprovedAt: now.UTC().Format(time.RFC3339),
		ApprovedBy: approver,
		Message:    "failback approved; reconciler will execute the failback sequence",
	}
	writeJSON(w, http.StatusAccepted, resp)
}

// HandleContinuumAuditList — GET
// /api/v1/sovereigns/{id}/audit/continuum
//
// Mirrors HandleRBACAuditList but filters on the continuum-* prefix.
// Auth: tier-admin or higher (mirrors /audit/rbac).
//
// Query params:
//
//	?limit=<n>     — page size, default 100, max 500
//	?offset=<n>    — page offset, default 0
//	?actor=<q>     — filter to actor substring (case-insensitive)
//	?since=<rfc3339> — only events at or after this timestamp
//	?type=<prefix>   — narrow further (e.g. "continuum-switchover")
func (h *Handler) HandleContinuumAuditList(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "id")
	if _, ok := h.lookupDeploymentForInfra(depID); !ok {
		writeNotFound(w, depID)
		return
	}
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		if !rbacAssignCallerAuthorized(claims) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error":  "forbidden",
				"detail": "/audit/continuum requires tier-admin or higher",
			})
			return
		}
	}
	if h.auditBus == nil {
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

	const maxRingPull = 5000
	predicate := IsContinuumAuditType
	if typeQ != "" {
		// Narrow further — exact-match (caller passes the full audit-type name).
		want := typeQ
		predicate = func(t string) bool {
			return IsContinuumAuditType(t) && t == want
		}
	}
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

	resp := continuumAuditListResponse{
		Items: page,
		Total: len(filtered),
	}
	if offset+len(page) < len(filtered) {
		resp.NextOffset = offset + len(page)
	}
	writeJSON(w, http.StatusOK, resp)
}

// HandleContinuumAuditStream — GET
// /api/v1/sovereigns/{id}/audit/continuum/stream
//
// SSE: `text/event-stream`. One JSON document per `data:` frame.
// Heartbeat ping every rbacAuditStreamHeartbeat. Connection closes on
// client disconnect (r.Context().Done()).
//
// Auth: tier-admin or higher (mirrors /audit/rbac/stream).
func (h *Handler) HandleContinuumAuditStream(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "id")
	if _, ok := h.lookupDeploymentForInfra(depID); !ok {
		writeNotFound(w, depID)
		return
	}
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		if !rbacAssignCallerAuthorized(claims) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error":  "forbidden",
				"detail": "/audit/continuum/stream requires tier-admin or higher",
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

	ch, unsub := h.auditBus.Subscribe(depID, IsContinuumAuditType)
	defer unsub()

	_, _ = fmt.Fprintf(w, ": connected sovereign=%s topic=continuum\n\n", depID)
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

// ── Helpers ──────────────────────────────────────────────────────────

// getContinuumCR fetches a Continuum CR by name, falling back to a
// list-across-all-namespaces when `ns` is empty (mirrors
// getApplicationCR's chroot-friendly pattern).
func getContinuumCR(
	ctx context.Context,
	client dynamic.Interface,
	name, ns string,
) (*unstructured.Unstructured, error) {
	if ns != "" {
		return client.Resource(ContinuumGVR()).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	}
	list, err := client.Resource(ContinuumGVR()).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i := range list.Items {
		if list.Items[i].GetName() == name {
			return list.Items[i].DeepCopy(), nil
		}
	}
	return nil, apierrors.NewNotFound(
		schema.GroupResource{Group: ContinuumGVR().Group, Resource: ContinuumGVR().Resource},
		name,
	)
}

// updateContinuumCR writes the patched Unstructured back to the cluster.
func updateContinuumCR(
	ctx context.Context,
	client dynamic.Interface,
	cr *unstructured.Unstructured,
) error {
	_, err := client.Resource(ContinuumGVR()).Namespace(cr.GetNamespace()).Update(
		ctx, cr, metav1.UpdateOptions{})
	return err
}

// continuumApplicationRef extracts spec.applicationRef as a convenience
// for audit-event tagging.
func continuumApplicationRef(cr *unstructured.Unstructured) string {
	v, _, _ := unstructured.NestedString(cr.Object, "spec", "applicationRef")
	return v
}

// actorFromClaims returns the operator email/subject for stamping into
// spec.switchover.requestedBy + audit Actor. Mirrors
// rbacAuditActorFromClaims.
func actorFromClaims(claims *auth.Claims) string {
	if claims == nil {
		return ""
	}
	if claims.Email != "" {
		return claims.Email
	}
	return claims.Sub
}

// displayReason returns a non-empty fallback for log lines.
func displayReason(s string) string {
	if strings.TrimSpace(s) == "" {
		return "n/a"
	}
	return s
}

// continuumNow returns the current time, overridable via h.now (tests).
func (h *Handler) continuumNow() time.Time {
	if h.continuumClock != nil {
		return h.continuumClock()
	}
	return time.Now()
}

// SetContinuumClock — test seam wiring. Production leaves this nil.
func (h *Handler) SetContinuumClock(now func() time.Time) { h.continuumClock = now }
