package handlers

// POST /billing/metering/record (#798 §C).
//
// Synchronous "validate → INSERT credit_ledger → return balance_after"
// path. Same payload shape as the NATS subscriber consumes; same
// idempotency guard (request_id → external_ref → UNIQUE partial index).
//
// Use case: Organization-admin pre-flight checks and the future bp-aider worker
// that wants the balance materialised before issuing an LLM request.
// The async NATS path is the canonical metering channel; this HTTP
// path is a sync escape hatch for callers that need immediate balance
// feedback.
//
// Auth: superadmin OR sovereign-admin (operator-admin middleware,
// matching requireVoucherIssuer's franchisee-friendly model). End
// users do NOT call this endpoint; their LLM traffic flows through
// NewAPI → metering sidecar → NATS, where they have no opportunity
// to forge the customer_id.

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/openova-io/openova/core/services/billing/store"
	"github.com/openova-io/openova/core/services/shared/events"
	"github.com/openova-io/openova/core/services/shared/respond"
)

// MeteringRecordResponse is the success body for POST
// /billing/metering/record.
type MeteringRecordResponse struct {
	// LedgerEntryID is the credit_ledger.id of the row that was
	// inserted (or the pre-existing row on a duplicate request_id).
	LedgerEntryID string `json:"ledger_entry_id"`

	// BalanceAfterOMR is the post-write balance in whole OMR (the
	// legacy view kept for back-compat with admin UIs that don't yet
	// understand micro-OMR).
	BalanceAfterOMR int `json:"balance_after_omr"`

	// BalanceAfterMicroOMR is the canonical post-write balance in
	// micro-OMR (1 OMR = 1,000,000 micro-OMR). Use this for any math.
	BalanceAfterMicroOMR int64 `json:"balance_after_micro_omr"`

	// Duplicate is true when the request_id was already recorded; the
	// caller's amount was NOT applied a second time.
	Duplicate bool `json:"duplicate"`
}

// RecordMetering is the HTTP handler for POST /billing/metering/record.
func (h *Handler) RecordMetering(w http.ResponseWriter, r *http.Request) {
	if err := requireVoucherIssuer(r); err != nil {
		// Same auth posture as voucher issuance: superadmin (Catalyst-
		// Zero) or sovereign-admin (Franchisee Sovereign). End users
		// never reach this endpoint.
		respond.Error(w, http.StatusForbidden, err.Error())
		return
	}

	var payload events.UsageRecordedPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if payload.Metadata.RequestID == "" {
		respond.Error(w, http.StatusBadRequest, "metadata.request_id is required for idempotency")
		return
	}
	if payload.CustomerID == "" {
		respond.Error(w, http.StatusBadRequest, "customer_id is required")
		return
	}
	if payload.Reason == "" {
		respond.Error(w, http.StatusBadRequest, "reason is required")
		return
	}

	// Compute canonical micro-OMR amount.
	micro := payload.AmountMicroOMR
	if micro == 0 && payload.AmountOMR != 0 {
		if payload.AmountOMR < 0 {
			micro = int64(payload.AmountOMR*1_000_000 - 0.5)
		} else {
			micro = int64(payload.AmountOMR*1_000_000 + 0.5)
		}
	}
	if micro >= 0 {
		respond.Error(w, http.StatusBadRequest,
			"amount must be negative (usage spend); top-ups go through /billing/checkout")
		return
	}

	resolver := h.MeteringCustomerResolver
	if resolver == nil {
		resolver = DefaultCustomerResolver{Store: h.Store}
	}
	cust, err := resolver.Resolve(r.Context(), payload.CustomerID, payload.Metadata.TenantID)
	if err != nil {
		// Surface as 404 — the caller can correct the customer_id and
		// retry. Do NOT 500 here: an invalid customer_id is a caller
		// bug, not a server failure.
		respond.Error(w, http.StatusNotFound, "customer not found: "+err.Error())
		return
	}

	metaJSON, err := json.Marshal(payload.Metadata)
	if err != nil {
		// Should never happen — payload.Metadata round-tripped JSON to
		// arrive here.
		respond.Error(w, http.StatusInternalServerError, "failed to encode metadata")
		return
	}

	ledgerID, balanceMicroOMR, dup, err := h.Store.RecordUsage(r.Context(), store.UsageEntry{
		CustomerID:     cust.ID,
		AmountMicroOMR: micro,
		Reason:         payload.Reason,
		ExternalRef:    payload.Metadata.RequestID,
		Metadata:       metaJSON,
	})
	if err != nil {
		slog.Error("metering: record usage failed",
			"request_id", payload.Metadata.RequestID, "error", err)
		respond.Error(w, http.StatusInternalServerError, "failed to record usage")
		return
	}

	respond.OK(w, MeteringRecordResponse{
		LedgerEntryID:        ledgerID,
		BalanceAfterOMR:      int(balanceMicroOMR / 1_000_000),
		BalanceAfterMicroOMR: balanceMicroOMR,
		Duplicate:            dup,
	})
}
