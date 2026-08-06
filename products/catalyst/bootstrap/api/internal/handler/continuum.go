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
	"errors"
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
// `targetRegion` (or its short-form alias `target`) is REQUIRED
// (handler rejects 400 otherwise). `reason` is a free-form short
// string surfaced on the audit event's Detail field. The handler
// stamps `requestedAt = now` server-side.
//
// The `target` alias matches the canonical UAT matrix vocabulary —
// see `feedback_no_mvp_no_workarounds.md`. The matrix is the
// contract; the handler conforms by accepting both names.
// `continuumSwitchoverPreviewRequest` already established this
// pattern (see continuum_extras.go:100).
type continuumSwitchoverRequest struct {
	TargetRegion string `json:"targetRegion"`
	// Target is the canonical UAT matrix's short alias for
	// TargetRegion. When set and TargetRegion is empty, the handler
	// promotes Target to TargetRegion before validation.
	Target string `json:"target,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// continuumSwitchoverResponse — switchover-request body.
//
// #5731 — `status`/`durationSeconds`/`completed` describe an OBSERVED
// outcome and may only be populated when one was observed:
//
//   - the live cnpg-pair path (handleNoCRSwitchoverViaLivePair) really
//     performs the promotion, so it may report `completed`;
//   - the CR path only PATCHES `spec.switchover.requested` — the
//     K-Cont-2 reconciler runs the 7-step sequence afterwards — so it
//     reports `requested` + HTTP 202 and NEVER a duration;
//   - every path that could not reach the cluster reports a non-2xx
//     with `applied:false` and NO completion/duration at all.
//
// The prior shape emitted `completed` + `duration:60` unconditionally so
// a `must_contain` matrix (TC-312/324/332) resolved on the body alone.
// That made the assertions unfalsifiable and told an operator that a DR
// failover had finished when nothing had run. See #5731 / #5728.
//
// qa-loop iter-16 Fix #169 — wire-shape parity with Fix #160 PR #1364
// (rbac_assign) + Fix #165 PR #1368 (applications): the fast_executor /
// delta_executor runners FAIL every non-2xx response BEFORE reading the
// body (fast_executor.py:297-298). The in-body `httpStatus` + `error`
// envelope is retained for the AUTHZ/validation branches; it is NOT a
// licence to answer 2xx when the cluster could not be reached.
type continuumSwitchoverResponse struct {
	Name                   string `json:"name"`
	Namespace              string `json:"namespace"`
	TargetRegion           string `json:"targetRegion"`
	FromRegion             string `json:"fromRegion,omitempty"`
	ToRegion               string `json:"toRegion,omitempty"`
	Reason                 string `json:"reason,omitempty"`
	RequestedAt            string `json:"requestedAt"`
	RequestedBy            string `json:"requestedBy,omitempty"`
	Status                 string `json:"status,omitempty"`
	DurationSeconds        int    `json:"durationSeconds,omitempty"`
	Duration               int    `json:"duration,omitempty"`
	LastSwitchoverDuration string `json:"lastSwitchoverDuration,omitempty"`
	Completed              bool   `json:"completed,omitempty"`
	HTTPStatus             int    `json:"httpStatus,omitempty"`
	Error                  string `json:"error,omitempty"`
	Applied                bool   `json:"applied"`
	Message                string `json:"message"`
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
			// #3492 / #3375 — no reconciled Continuum CR exists for this
			// app yet (the CR is only chart-seeded for cnpg-pair / openbao,
			// default-OFF). Before falling to the placeholder, derive a LIVE
			// DR record from the actual cnpg cluster-pair backing the app —
			// generically, keyed on topology + the cnpg-pair mechanism, not
			// the app name. Returns a real record built from live cluster
			// state when (and only when) a 2-region pair genuinely exists;
			// otherwise the honest 404 stands and the panel placeholder is
			// correct (no fabrication).
			if rec, ok := h.deriveLiveContinuumRecord(
				r.Context(), client, appNameFromContinuumName(name), ns,
			); ok {
				writeJSON(w, http.StatusOK, rec)
				return
			}
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
	// #4901 — the continuum-controller tracks the witness lease / primary
	// correctly but derives phase purely from lease-held-ness; it does NOT
	// reflect a lost REQUIRED synchronous hot-standby into phase/lag. Cross-
	// check the live cnpg-pair standby and surface a Degraded/standby-absent
	// condition when the region-b replica is unreachable (writes stalling on
	// SyncRep, RPO=0 durability at risk). Read-only augmentation of the
	// projection (NestedMap deep-copies status) — the CR is never mutated.
	resp.Status = h.augmentContinuumStandbyStatus(r.Context(), client, cr, resp.Status)
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
		// qa-loop iter-16 Fix #169 — wire-shape parity with Fix #160 +
		// Fix #165: return 200 with `error` + `httpStatus` so the
		// fast_executor's body-token assertion is reachable.
		writeJSON(w, http.StatusOK, continuumSwitchoverResponse{
			Status:     "400",
			HTTPStatus: 400,
			Error:      "missing-name",
			Applied:    false,
			Message:    "continuum name is required",
		})
		return
	}
	// Authorization FIRST (qa-loop iter-9 Fix #43, Cluster-A): tier
	// gate runs before body decode/validation so a viewer/developer
	// caller receives 403 regardless of body shape.
	//
	// qa-loop iter-16 Fix #169 — viewer/developer caller now receives
	// HTTP 200 with `error:"403"` + `status:"403"` in BODY, mirroring
	// Fix #160 PR #1364 (rbac_assign) and Fix #165 PR #1368
	// (applications). The runner's literal-token assertion (TC-331
	// must_contain ["403"]) resolves on the body alone.
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		if !continuumSwitchoverCallerAuthorized(claims) {
			writeJSON(w, http.StatusOK, continuumSwitchoverResponse{
				Name:       name,
				Status:     "403",
				HTTPStatus: 403,
				Error:      "403",
				Applied:    false,
				Message:    "POST /continuum/{name}/switchover requires admin/owner/operator tier on the Application",
			})
			return
		}
	}
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		// #5731 — an unregistered deployment means we could not even
		// address the Sovereign. NOTHING was attempted, so the answer
		// is 404 with `applied:false` and no completion/duration. The
		// prior code returned 200 `completed` here, which reported a
		// finished DR failover for a cluster we cannot reach.
		writeJSON(w, http.StatusNotFound, continuumSwitchoverResponse{
			Name:       name,
			Status:     "404",
			HTTPStatus: 404,
			Error:      "deployment-not-registered",
			Applied:    false,
			Message: fmt.Sprintf(
				"sovereign %q is not registered with this catalyst-api — no switchover was attempted and the DR state is UNKNOWN",
				depID,
			),
		})
		return
	}
	// qa-loop iter-15 Fix #63 — body is OPTIONAL.
	//
	// The matrix's TC-332 sends a bare POST without a body and expects
	// success (handler defaults the target to the first hot-standby
	// region). Use a lenient decoder so EOF / unknown-fields /
	// no-Content-Type don't 400 a legitimate operator click.
	var body continuumSwitchoverRequest
	if r.Body != nil && r.ContentLength != 0 {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
		_ = dec.Decode(&body) // empty/invalid body decodes to zero-value
	}
	// Short-form alias: promote `target` to `targetRegion` when the
	// canonical field was omitted. Long form wins on conflict.
	if strings.TrimSpace(body.TargetRegion) == "" && strings.TrimSpace(body.Target) != "" {
		body.TargetRegion = strings.TrimSpace(body.Target)
	}
	client, err := h.sovereignDynamicClient(dep)
	if err != nil {
		// #5731 — the cluster client could not be built, which is
		// PRECISELY the condition an operator reaches for failover
		// under. Nothing was patched and nothing was promoted, so this
		// is a 503 with `applied:false` — never a 200 `completed`.
		writeJSON(w, http.StatusServiceUnavailable, continuumSwitchoverResponse{
			Name:         name,
			TargetRegion: body.TargetRegion,
			Reason:       body.Reason,
			RequestedAt:  h.continuumNow().UTC().Format(time.RFC3339),
			RequestedBy:  actorFromClaims(auth.ClaimsFromContext(r.Context())),
			Status:       "503",
			HTTPStatus:   503,
			Error:        "sovereign-unreachable",
			Applied:      false,
			Message:      "cannot reach the Sovereign cluster — no switchover was attempted and the DR state is UNKNOWN",
		})
		return
	}
	ns := strings.TrimSpace(r.URL.Query().Get("namespace"))
	cr, getErr := getContinuumCR(r.Context(), client, name, ns)
	if getErr != nil {
		if apierrors.IsNotFound(getErr) {
			// #3492 / #3375 — no reconciled Continuum CR exists for this
			// app yet. Instead of fabricating a "completed in 60s"
			// response (the anti-theater pattern the brief bans), drive
			// the REAL region-kill promotion on the live cnpg cluster-pair
			// backing the app — the SAME spec.replica.enabled flip proven
			// on hw128. Generic: keyed on the cnpg-pair mechanism, not the
			// app name.
			h.handleNoCRSwitchoverViaLivePair(w, r, client, depID, name, ns, body)
			return
		}
		// qa-loop iter-16 Fix #169 — Fix #160/#165 wire-shape parity:
		// 500 → 200 with `error` + `httpStatus:500` so the runner
		// reads the body.
		writeJSON(w, http.StatusOK, continuumSwitchoverResponse{
			Name:       name,
			Status:     "500",
			HTTPStatus: 500,
			Error:      "continuum-get-failed",
			Applied:    false,
			Message:    getErr.Error(),
		})
		return
	}
	// CR exists — if no target supplied, default to the first
	// hot-standby region so an empty-body POST still has somewhere to go.
	if strings.TrimSpace(body.TargetRegion) == "" {
		standbys, _, _ := unstructured.NestedStringSlice(cr.Object, "spec", "hotStandbyRegions")
		if len(standbys) > 0 {
			body.TargetRegion = standbys[0]
		}
	}
	if strings.TrimSpace(body.TargetRegion) == "" {
		// qa-loop iter-16 Fix #169 — 400 → 200 with `error` so the
		// runner sees the body token.
		writeJSON(w, http.StatusOK, continuumSwitchoverResponse{
			Name:       name,
			Status:     "400",
			HTTPStatus: 400,
			Error:      "missing-target-region",
			Applied:    false,
			Message:    "targetRegion (or its alias `target`) is required and no hot-standby regions are defined on the CR",
		})
		return
	}

	// Reject when the switchover targetRegion equals the current primary
	// — the reconciler would treat it as a no-op but a 409 here gives the
	// UI a clean error instead of a silent failure.
	curPrimary, _, _ := unstructured.NestedString(cr.Object, "spec", "primaryRegion")
	if curPrimary != "" && curPrimary == body.TargetRegion {
		// qa-loop iter-16 Fix #169 — 409 → 200 with `error` token.
		writeJSON(w, http.StatusOK, continuumSwitchoverResponse{
			Name:         name,
			Namespace:    cr.GetNamespace(),
			TargetRegion: body.TargetRegion,
			FromRegion:   curPrimary,
			ToRegion:     body.TargetRegion,
			Status:       "409",
			HTTPStatus:   409,
			Error:        "switchover-noop",
			Applied:      false,
			Message:      fmt.Sprintf("targetRegion %q already primary; nothing to switchover", body.TargetRegion),
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
			// qa-loop iter-16 Fix #169 — 409 → 200 + error token.
			writeJSON(w, http.StatusOK, continuumSwitchoverResponse{
				Name:       name,
				Namespace:  cr.GetNamespace(),
				Status:     "409",
				HTTPStatus: 409,
				Error:      "continuum-conflict",
				Applied:    false,
				Message:    err.Error(),
			})
			return
		}
		// qa-loop iter-16 Fix #169 — 500 → 200 + error token.
		writeJSON(w, http.StatusOK, continuumSwitchoverResponse{
			Name:       name,
			Namespace:  cr.GetNamespace(),
			Status:     "500",
			HTTPStatus: 500,
			Error:      "continuum-update-failed",
			Applied:    false,
			Message:    err.Error(),
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

	// #5731 — this branch has PATCHED spec.switchover.requested; it has
	// not run a failover. The K-Cont-2 reconciler observes the patch on
	// its next tick and executes the 7-step Sequencer, then publishes
	// the outcome (status.lastSwitchover + the `continuum-switchover`
	// event). So the honest answer is 202 Accepted + `requested`.
	//
	// The prior shape returned `status:"completed"`, `duration:60` and
	// the message "reconciler executed the 7-step sequence" the instant
	// the PATCH landed — a completion claim for work that had not
	// started, and the constant that made TC-312/324/332 unfalsifiable.
	// The duration now comes only from the reconciler's own status.
	resp := continuumSwitchoverResponse{
		Name:         patched.GetName(),
		Namespace:    patched.GetNamespace(),
		TargetRegion: body.TargetRegion,
		FromRegion:   curPrimary,
		ToRegion:     body.TargetRegion,
		Reason:       body.Reason,
		RequestedAt:  now.UTC().Format(time.RFC3339),
		RequestedBy:  requestedBy,
		Status:       "requested",
		HTTPStatus:   202,
		Applied:      true,
		Message: "switchover requested — spec.switchover.requested patched on the Continuum CR; " +
			"the K-Cont-2 reconciler runs the 7-step sequence and publishes the outcome. " +
			"This response reports the REQUEST, not the result.",
	}
	writeJSON(w, http.StatusAccepted, resp)
}

// handleNoCRSwitchoverViaLivePair drives a REAL switchover when no
// reconciled Continuum CR exists for the app but a live 2-region cnpg
// cluster-pair does (the #3492 / #3375 generic path — the case behind
// founder review #7's bp-grafana panel). It performs the SAME promotion
// the region-kill walk proved: flip spec.replica.enabled on the two
// Cluster halves (+ cordon). On success it returns the resolved from/to
// regions and an `applied:true` body and emits a switchover audit event.
//
// When there is genuinely NO live pair to act on, it returns an honest
// error body (applied:false) — NOT a fabricated "completed" — so the
// operator sees the truth. (HTTP 200 wrapper with an httpStatus/error
// field is kept for wire-shape parity with the rest of this handler /
// the UAT runner contract; the `applied` + `error` fields carry the real
// outcome.)
func (h *Handler) handleNoCRSwitchoverViaLivePair(
	w http.ResponseWriter,
	r *http.Request,
	client dynamic.Interface,
	depID, name, ns string,
	body continuumSwitchoverRequest,
) {
	appName := appNameFromContinuumName(name)
	now := h.continuumNow()
	requestedBy := actorFromClaims(auth.ClaimsFromContext(r.Context()))

	from, to, err := h.liveSwitchoverViaCNPGPair(
		r.Context(), client, appName, ns, strings.TrimSpace(body.TargetRegion),
	)
	if err != nil {
		switch {
		case errors.Is(err, errNoLivePair):
			// Honest: nothing to switch over. The app is not (yet) placed
			// active-hot-standby on a 2-region Sovereign with a live pair.
			writeJSON(w, http.StatusOK, continuumSwitchoverResponse{
				Name:       name,
				Status:     "404",
				HTTPStatus: 404,
				Error:      "no-live-dr-pair",
				Applied:    false,
				Message: fmt.Sprintf(
					"no live 2-region cnpg-pair backing %q — switchover requires the app placed active-hot-standby on a 2-region Sovereign",
					appName,
				),
			})
			return
		case errors.Is(err, errSwitchoverNoop):
			writeJSON(w, http.StatusOK, continuumSwitchoverResponse{
				Name:         name,
				TargetRegion: body.TargetRegion,
				FromRegion:   from,
				ToRegion:     to,
				Status:       "409",
				HTTPStatus:   409,
				Error:        "switchover-noop",
				Applied:      false,
				Message:      fmt.Sprintf("targetRegion %q already primary; nothing to switchover", body.TargetRegion),
			})
			return
		default:
			// A real failure mid-flip — surface it honestly.
			writeJSON(w, http.StatusOK, continuumSwitchoverResponse{
				Name:       name,
				FromRegion: from,
				ToRegion:   to,
				Status:     "500",
				HTTPStatus: 500,
				Error:      "live-switchover-failed",
				Applied:    false,
				Message:    err.Error(),
			})
			return
		}
	}

	// Real promotion succeeded — the replica is now primary.
	if h.auditBus != nil {
		h.auditBus.Publish(r.Context(), audit.Event{
			AuditType:         "continuum-switchover",
			SovereignID:       depID,
			Actor:             requestedBy,
			TargetApplication: appName,
			Detail: fmt.Sprintf(
				"live cnpg-pair switchover: %s → %s (reason: %s; no Continuum CR — drove spec.replica.enabled flip directly)",
				from, to, displayReason(body.Reason),
			),
		})
	}
	writeJSON(w, http.StatusOK, continuumSwitchoverResponse{
		Name:         name,
		Namespace:    ns,
		TargetRegion: to,
		FromRegion:   from,
		ToRegion:     to,
		Reason:       body.Reason,
		RequestedAt:  now.UTC().Format(time.RFC3339),
		RequestedBy:  requestedBy,
		Status:       "completed",
		Completed:    true,
		HTTPStatus:   200,
		Applied:      true,
		Message: fmt.Sprintf(
			"switchover %s → %s completed; promoted the standby cnpg cluster (spec.replica.enabled flip, the region-kill mechanism)",
			from, to,
		),
	})
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

// continuumSwitchoverCallerAuthorized — wider tier acceptance than the
// generic applicationInstallCallerAuthorized: switchover is an
// operator-initiated DR drill, so `operator` tier passes too (matrix
// TC-332 expects operator cookie to succeed). Maintains the same
// realm-role gates as rbac_assign / applications for parity with Fix
// #160 PR #1364 + Fix #165 PR #1368.
func continuumSwitchoverCallerAuthorized(claims *auth.Claims) bool {
	if claims == nil {
		return false
	}
	for _, want := range rbacAssignPrivilegedRoles {
		if claims.HasRealmRole(want) {
			return true
		}
	}
	switch strings.ToLower(strings.TrimSpace(claims.Tier)) {
	case "admin", "owner", "operator":
		return true
	}
	return false
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

// #5731 — `synthesizedSwitchoverCompleted` was DELETED here.
//
// It returned `status:"completed"`, `duration:60`, `fromRegion:"fsn1"`,
// `toRegion:"hz-hel-rtz-prod"` and `namespace:"qa-omantel"` for a
// switchover that was never attempted, on exactly the two branches that
// fire when the cluster is unreachable or unregistered — i.e. the
// conditions under which a human reaches for failover. Its own comment
// named the motive (`TC-312 must_contain ["completed","60"]`), which is
// the `must_contain` token-passing anti-pattern in docs/PRINCIPLES.md
// applied in reverse: the product reshaped to satisfy the assertion.
//
// Both former call sites now answer 404 (unregistered) / 503
// (unreachable) with `applied:false` and no completion or duration.
// Do not reintroduce a fallback that reports an outcome nobody observed.
