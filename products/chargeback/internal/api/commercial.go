package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/openova-io/openova/products/chargeback/internal/commercial"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The external system of record (DESIGN.md §8.10).
//
//	POST /commercial/import/invoice-status   the billing system reports back
//	GET  /commercial/outbox                  what we have queued for it
//	POST /commercial/outbox/{id}/retry       push one row now
//
// The import is authenticated by an HMAC over the RAW body rather than by a
// session, because the caller is a machine in the operator's estate. The two
// outbox endpoints are operator-only like everything else here.

// importInvoiceStatus applies one status report from the operator's billing
// system. In external mode this is the only thing that moves a statement
// after issue.
func (h *Handler) importInvoiceStatus(w http.ResponseWriter, r *http.Request) {
	if h.Importer == nil {
		writeErr(w, http.StatusServiceUnavailable, commercial.ErrImportNotConfigured.Error())
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "could not read the body")
		return
	}
	// Verify BEFORE decoding: the signature is over the bytes as sent, and
	// nothing unverified may reach the ledger.
	switch err := commercial.VerifySignature(h.Importer.Secret, r.Header.Get(commercial.SignatureHeader), body); {
	case errors.Is(err, commercial.ErrImportNotConfigured):
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	case err != nil:
		slog.Warn("commercial import: rejected", "remote", r.RemoteAddr, "error", err)
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	var in commercial.InvoiceStatusImport
	if err := json.Unmarshal(body, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	st, err := h.Importer.Apply(r.Context(), in, "billing-system")
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeErr(w, http.StatusBadRequest, invalidMessage(err))
		return
	case err != nil:
		storeErr(w, err)
		return
	}
	h.audit(r, &st.CustomerID, "commercial.import.invoice-status", map[string]any{
		"statement_id": st.ID, "external_ref": st.ExternalInvoiceRef, "state": in.State,
		"status": st.Status, "paid_total": st.Paid, "balance": st.Balance,
	})
	writeJSON(w, http.StatusOK, st)
}

// listOutbox shows what is queued for the operator's billing system, and why
// anything is stuck. `?all=1` includes the delivered rows.
func (h *Handler) listOutbox(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	pending := r.URL.Query().Get("all") != "1"
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := h.Store.ListOutbox(r.Context(), pending, limit)
	if err != nil {
		storeErr(w, err)
		return
	}
	settings, err := h.Store.GetBillingSettings(r.Context())
	if err != nil {
		storeErr(w, err)
		return
	}
	pendingCount, failed := 0, 0
	for _, e := range list {
		if e.DeliveredAt != nil {
			continue
		}
		pendingCount++
		if e.LastError != "" {
			failed++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": list, "pending": pendingCount, "failed": failed,
		"commercial_provider": settings.CommercialProvider,
	})
}

// retryOutbox makes one queued document due now and, when a deliverer is
// wired, pushes it immediately so the operator sees the outcome rather than
// waiting for the next tick. A far-end failure is reported ON THE ROW, not as
// a failed request: the retry did happen.
func (h *Handler) retryOutbox(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	entry, err := h.Store.RequeueOutbox(r.Context(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	if h.Deliverer != nil {
		if delivered, derr := h.Deliverer.DeliverOne(r.Context(), id); derr == nil {
			entry = delivered
		} else {
			slog.Warn("commercial outbox: retry could not deliver", "entry", id, "error", derr)
		}
	}
	h.audit(r, nil, "commercial.outbox.retry", map[string]any{
		"entry": entry.ID, "idempotency_key": entry.IdempotencyKey, "attempts": entry.Attempts,
		"delivered": entry.DeliveredAt != nil, "last_error": entry.LastError,
	})
	writeJSON(w, http.StatusOK, entry)
}
