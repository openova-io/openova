package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/openova-io/openova/products/chargeback/internal/access"
	"github.com/openova-io/openova/products/chargeback/internal/commercial"
	"github.com/openova-io/openova/products/chargeback/internal/commercial/external"
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
	body, ok := h.verifiedImport(w, r)
	if !ok {
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

// verifiedImport reads the raw body and verifies its signature BEFORE it is
// decoded: the signature is over the bytes as sent, and nothing unverified
// may reach the ledger. Shared by the whole webhook family.
func (h *Handler) verifiedImport(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if h.Importer == nil {
		writeErr(w, http.StatusServiceUnavailable, commercial.ErrImportNotConfigured.Error())
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "could not read the body")
		return nil, false
	}
	switch err := commercial.VerifySignature(h.Importer.Secret, r.Header.Get(commercial.SignatureHeader), body); {
	case errors.Is(err, commercial.ErrImportNotConfigured):
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return nil, false
	case err != nil:
		slog.Warn("commercial import: rejected", "remote", r.RemoteAddr, "path", r.URL.Path, "error", err)
		writeErr(w, http.StatusUnauthorized, err.Error())
		return nil, false
	}
	return body, true
}

// importPaymentStatus — POST /commercial/import/payment-status (TMF676): a
// payment the billing system took, against an exported invoice or as credit
// on the account.
func (h *Handler) importPaymentStatus(w http.ResponseWriter, r *http.Request) {
	body, ok := h.verifiedImport(w, r)
	if !ok {
		return
	}
	var in external.PaymentStatus
	if err := json.Unmarshal(body, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	p, err := h.Importer.ApplyPaymentStatus(r.Context(), in, "billing-system")
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeErr(w, http.StatusBadRequest, invalidMessage(err))
		return
	case err != nil:
		storeErr(w, err)
		return
	}
	h.audit(r, &p.CustomerID, "commercial.import.payment-status", map[string]any{"payment_id": p.ID, "amount": p.Amount, "status": p.Status, "reference": p.Reference, "allocated": p.Allocated, "unallocated": p.Unallocated})
	writeJSON(w, http.StatusOK, p)
}

// importAccountBalance — POST /commercial/import/account-balance (TMF666).
func (h *Handler) importAccountBalance(w http.ResponseWriter, r *http.Request) {
	body, ok := h.verifiedImport(w, r)
	if !ok {
		return
	}
	var in external.AccountBalanceImport
	if err := json.Unmarshal(body, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	c, err := h.Importer.ApplyAccountBalance(r.Context(), in)
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeErr(w, http.StatusBadRequest, invalidMessage(err))
		return
	case err != nil:
		storeErr(w, err)
		return
	}
	h.audit(r, &c.ID, "commercial.import.account-balance", map[string]any{"external_balance": c.ExternalBalance, "as_of": c.ExternalBalanceAt})
	writeJSON(w, http.StatusOK, c)
}

// importEnforcement — POST /commercial/import/enforcement: the EXPLICIT
// suspend / resume command (DESIGN.md §9.7). Never inferred from a payment
// status; executed here, decided there.
func (h *Handler) importEnforcement(w http.ResponseWriter, r *http.Request) {
	body, ok := h.verifiedImport(w, r)
	if !ok {
		return
	}
	var in external.Enforcement
	if err := json.Unmarshal(body, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	rec, err := h.Importer.ApplyEnforcement(r.Context(), in, "billing-system")
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeErr(w, http.StatusBadRequest, invalidMessage(err))
		return
	case err != nil && rec.ID == 0:
		storeErr(w, err)
		return
	}
	status := http.StatusOK
	if err != nil {
		status = http.StatusBadGateway
	}
	writeJSON(w, status, rec)
}

// listOutbox shows what is queued for the operator's billing system, and why
// anything is stuck. `?all=1` includes the delivered rows.
func (h *Handler) listOutbox(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.MeteringRead); !ok {
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
	if _, ok := h.requireSovereign(w, r, access.BillingIssue); !ok {
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
