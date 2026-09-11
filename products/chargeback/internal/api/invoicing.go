package api

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/access"
	"github.com/openova-io/openova/products/chargeback/internal/settle"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Post-paid invoicing (DESIGN.md §8). An issued statement is an invoice, and
// an invoice has a life after it is issued: it is sent to the customer, it
// falls due, it is paid — possibly in parts — or it is voided. These are the
// four operator-only endpoints that walk it, and every transition is audited.
//
//	PATCH /statements/{id}          {po_reference, payment_terms_days}  (draft only)
//	POST  /statements/{id}/send     {notify}          → issued → sent
//	POST  /statements/{id}/payments {amount, paid_at, reference}
//	POST  /statements/{id}/cancel   {reason}          → draft|issued → cancelled
//	GET   /statements/{id}/payments                    the payment history
//
// The store owns the lifecycle: an illegal transition is refused there with
// ErrConflict and answered 409 here, so the rules cannot be worked around by
// a second client.

// patchStatement edits the invoice fields of a DRAFT: the purchase-order
// reference the invoice must quote, and the payment terms its due date is
// computed from. Both are frozen at issue.
func (h *Handler) patchStatement(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.BillingIssue); !ok {
		return
	}
	var in struct {
		PORef            *string `json:"po_reference"`
		PaymentTermsDays *int    `json:"payment_terms_days"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if in.PORef == nil && in.PaymentTermsDays == nil {
		writeErr(w, http.StatusBadRequest, "give po_reference, payment_terms_days or both")
		return
	}
	st, err := h.Store.UpdateStatementInvoice(r.Context(), r.PathValue("id"), store.StatementInvoicePatch{PORef: in.PORef, PaymentTermsDays: in.PaymentTermsDays})
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeErr(w, http.StatusBadRequest, invalidMessage(err))
		return
	case err != nil:
		storeErr(w, err)
		return
	}
	fields := map[string]any{"statement_id": st.ID}
	if in.PORef != nil {
		fields["po_reference"] = st.PORef
	}
	if in.PaymentTermsDays != nil {
		fields["payment_terms_days"] = *in.PaymentTermsDays
	}
	h.audit(r, &st.CustomerID, "statement.update", fields)
	writeJSON(w, http.StatusOK, st)
}

// sendStatement records that the invoice went to the customer
// (issued → sent), which is the state its due date is measured against.
//
// The mail is OPT-IN here, unlike issue: an operator who has already emailed
// the invoice from their own accounts-payable channel is marking what
// happened, and a second copy of the same invoice in the customer's inbox is
// worse than none.
func (h *Handler) sendStatement(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.BillingIssue); !ok {
		return
	}
	var in struct {
		Notify *bool `json:"notify"`
	}
	if err := decode(r, &in); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	notify := in.Notify != nil && *in.Notify
	provider, _, err := h.Commercial.For(r.Context())
	if err != nil {
		storeErr(w, err)
		return
	}
	st, transitioned, err := provider.Send(r.Context(), r.PathValue("id"))
	if err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, &st.CustomerID, "statement.send", map[string]any{"statement_id": st.ID, "invoice_number": st.InvoiceNumber, "due_at": st.DueAt, "transitioned": transitioned, "notify": notify})
	if transitioned && notify {
		if c, cerr := h.Store.GetCustomer(r.Context(), store.OperatorScope, st.CustomerID); cerr != nil {
			slog.Warn("send statement: load customer", "statement", st.ID, "error", cerr)
		} else {
			h.notifyStatement(r, st, c)
		}
	}
	writeJSON(w, http.StatusOK, st)
}

// cancelStatement voids a draft or an issued invoice. A SENT invoice is not
// cancellable: the customer holds it, and the correction for that is a
// credit note rather than a status flip.
func (h *Handler) cancelStatement(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.BillingIssue); !ok {
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err := decode(r, &in); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	// DESIGN.md §18.4 — a closed period does not move: voiding an invoice in
	// one would take a receivable back off books already signed off.
	if st, err := h.Store.GetStatement(r.Context(), store.OperatorScope, r.PathValue("id")); err == nil {
		if h.refuseClosedPeriod(w, r, st.PeriodStart, "cancelling this invoice") {
			return
		}
	}
	provider, _, err := h.Commercial.For(r.Context())
	if err != nil {
		storeErr(w, err)
		return
	}
	st, transitioned, err := provider.Cancel(r.Context(), r.PathValue("id"), in.Reason)
	if err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, &st.CustomerID, "statement.cancel", map[string]any{"statement_id": st.ID, "invoice_number": st.InvoiceNumber, "reason": st.CancelReason, "transitioned": transitioned})
	writeJSON(w, http.StatusOK, st)
}

// listStatementPayments returns the payment history, inside the session's
// scope — a customer may read what it has paid.
func (h *Handler) listStatementPayments(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	list, err := h.Store.ListStatementPayments(r.Context(), s.Scope(), r.PathValue("id"))
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"payments": list})
}

// recordStatementPayment books one payment against an invoice.
//
// The payment goes through the customer's settlement gateway first
// (ConfirmSettlement), which is the seam an Omantel gateway callback lands
// on too: the gateway validates and normalises the facts, and the store
// books them — carrying a balance on a part payment, refusing an
// overpayment, and flipping the invoice to paid when the balance reaches
// zero.
func (h *Handler) recordStatementPayment(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSovereign(w, r, access.BillingCollect)
	if !ok {
		return
	}
	var in struct {
		Amount    store.Decimal `json:"amount"`
		PaidAt    string        `json:"paid_at"`
		Reference string        `json:"reference"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	paidAt, msg := parsePaidAt(in.PaidAt)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	id := r.PathValue("id")
	provider, _, err := h.Commercial.For(r.Context())
	if err != nil {
		storeErr(w, err)
		return
	}
	st, err := h.Store.GetStatement(r.Context(), store.OperatorScope, id)
	if err != nil {
		storeErr(w, err)
		return
	}
	c, err := h.Store.GetCustomer(r.Context(), store.OperatorScope, st.CustomerID)
	if err != nil {
		storeErr(w, err)
		return
	}
	pay, err := h.Settlement.ConfirmSettlement(r.Context(), settle.Confirmation{
		Statement: st, Customer: c, Amount: in.Amount, PaidAt: paidAt, Reference: in.Reference, Actor: s.Email,
	})
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeErr(w, http.StatusBadRequest, invalidMessage(err))
		return
	case err != nil:
		storeErr(w, err)
		return
	}
	st, recorded, err := provider.RecordPayment(r.Context(), id, store.PaymentInput{
		Amount: pay.Amount, PaidAt: pay.PaidAt, Method: pay.Method, Reference: pay.Reference,
		Status: pay.Status, Gateway: pay.Gateway, Actor: s.Email,
	})
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeErr(w, http.StatusBadRequest, invalidMessage(err))
		return
	case err != nil:
		storeErr(w, err)
		return
	}
	h.audit(r, &st.CustomerID, "statement.payment", map[string]any{
		"statement_id": st.ID, "invoice_number": st.InvoiceNumber, "payment_id": recorded.ID,
		"amount": recorded.Amount, "paid_at": recorded.PaidAt, "method": recorded.Method, "reference": recorded.Reference,
		"payment_status": recorded.Status, "gateway": recorded.Gateway,
		"paid_total": st.Paid, "balance": st.Balance, "status": st.Status,
	})
	// DESIGN.md §9.7 — a settled invoice may lift a suspension this product
	// holds; a prepaid customer's wallet is re-checked.
	h.afterAccountChange(r, st.CustomerID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"statement": st, "payment": recorded})
}

// settleConfirmation builds the gateway confirmation for a payment that is
// not against one invoice (a top-up, a split payment).
func settleConfirmation(c store.Customer, amount store.Decimal, paidAt time.Time, reference, actor string) settle.Confirmation {
	return settle.Confirmation{Customer: c, Amount: amount, PaidAt: paidAt, Reference: reference, Actor: actor}
}

// parsePaidAt accepts a YYYY-MM-DD day or an RFC3339 instant; empty means
// "now". A day is read as midnight UTC, which is how a bank statement line
// dates a transfer.
func parsePaidAt(s string) (time.Time, string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Now().UTC(), ""
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.UTC(), ""
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), ""
	}
	return time.Time{}, "paid_at must be YYYY-MM-DD or an RFC3339 timestamp"
}
