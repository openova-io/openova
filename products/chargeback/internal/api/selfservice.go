package api

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/access"
	"github.com/openova-io/openova/products/chargeback/internal/commercial"
	"github.com/openova-io/openova/products/chargeback/internal/settle"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Customer self-service (DESIGN.md §16) — the three things a paying customer
// does without asking the operator:
//
//	GET  /statements/{id}.pdf                        download the invoice (statements.go)
//	POST /customers/{id}/payment-methods             start a setup through the gateway seam
//	GET  /customers/{id}/payment-methods             what is on file
//	POST /customers/{id}/payment-methods/{mid}/confirm   the setup completed
//	DELETE /customers/{id}/payment-methods/{mid}     take it off file
//	GET|DELETE /payment-methods/{id}                 the same two by id alone
//	POST /statements/{id}/disputes {reason, lines?}  dispute an invoice
//	GET  /statements/{id}/disputes · GET /customers/{id}/disputes · GET /disputes/{id}
//	POST /disputes/{id}/resolve {outcome, note}      the operator's answer
//
// A customer principal acts on its OWN customer and nothing else: every
// handler here resolves the target through the session's scope, so another
// customer's id is 404 — not a filtered answer, not a 403 that confirms the
// id exists.

// ---------------------------------------------------------------------------
// saved payment methods
// ---------------------------------------------------------------------------

// createPaymentMethod — POST /customers/{id}/payment-methods.
//
// It asks the customer's OWN gateway, through the existing settle.Registry,
// to begin saving a method, and records what comes back. The card is entered
// on the gateway's page: nothing in this request, this process or this
// database ever holds a card number, and the audit entry names the gateway
// and the method id only.
//
// account.topup is the permission — the same one that lets a customer ask the
// gateway for a top-up — so an owner or a billing user manages its own
// methods and a viewer cannot; an operator with billing.collect may do it on
// the customer's behalf.
func (h *Handler) createPaymentMethod(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := h.requireAnyPermission(w, r, id, access.AccountTopup, access.BillingCollect)
	if !ok {
		return
	}
	var in struct {
		Label     string `json:"label"`
		ReturnURL string `json:"return_url"`
	}
	if err := decode(r, &in); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	c, err := h.Store.GetCustomer(r.Context(), s.Scope(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	returnURL := strings.TrimSpace(in.ReturnURL)
	if returnURL == "" {
		returnURL = strings.TrimRight(h.Config.PublicURL, "/") + "/my/payment-methods"
	}
	res, err := h.Settlement.SetupMethod(r.Context(), settle.SetupRequest{
		Customer: c, ReturnURL: returnURL, Label: strings.TrimSpace(in.Label), Actor: s.Email,
	})
	switch {
	case errors.Is(err, settle.ErrMethodSetupNotSupported):
		// Not a fault: a customer that pays by transfer has no instrument to
		// keep, and saying so is the answer the console shows.
		writeErr(w, http.StatusConflict, "this customer's payment method cannot keep an instrument on file: "+err.Error())
		return
	case errors.Is(err, settle.ErrNoGateway):
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	case err != nil:
		writeErr(w, http.StatusBadGateway, "the gateway refused to start a setup: "+err.Error())
		return
	}
	input := store.PaymentMethodInput{
		CustomerID: c.ID, Gateway: res.Gateway, SetupID: res.SetupID, SetupURL: res.SetupURL,
		Label: strings.TrimSpace(in.Label), Actor: s.Email,
	}
	if m := res.Method; m != nil {
		// The gateway saved it during the call: the display triple and the
		// token land straight away and the method is active.
		input.Token, input.Brand, input.Last4, input.ExpMonth, input.ExpYear = m.Token, m.Brand, m.Last4, m.ExpMonth, m.ExpYear
		if m.Label != "" {
			input.Label = m.Label
		}
	}
	saved, err := h.Store.StartPaymentMethod(r.Context(), input)
	if err != nil {
		storeErr(w, err)
		return
	}
	h.auditPaymentMethod(r, saved, "payment_method.setup", map[string]any{"detail": res.Detail})
	writeJSON(w, http.StatusCreated, saved)
}

// confirmPaymentMethod — POST /customers/{id}/payment-methods/{mid}/confirm.
// The customer has returned from the gateway's page; this asks the gateway
// what is now on file and records the display record.
func (h *Handler) confirmPaymentMethod(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := h.requireAnyPermission(w, r, id, access.AccountTopup, access.BillingCollect)
	if !ok {
		return
	}
	m, err := h.Store.GetPaymentMethod(r.Context(), s.Scope(), r.PathValue("mid"))
	if err != nil {
		storeErr(w, err)
		return
	}
	if m.CustomerID != id {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	c, err := h.Store.GetCustomer(r.Context(), s.Scope(), m.CustomerID)
	if err != nil {
		storeErr(w, err)
		return
	}
	got, err := h.Settlement.ConfirmMethod(r.Context(), settle.MethodConfirmation{Customer: c, SetupID: m.SetupID, Actor: s.Email})
	switch {
	case errors.Is(err, settle.ErrMethodSetupNotSupported):
		writeErr(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeErr(w, http.StatusBadGateway, "the gateway did not confirm the method: "+err.Error())
		return
	}
	saved, err := h.Store.ConfirmPaymentMethod(r.Context(), s.Scope(), m.ID, store.PaymentMethodConfirm{
		Token: got.Token, Brand: got.Brand, Last4: got.Last4, ExpMonth: got.ExpMonth, ExpYear: got.ExpYear, Label: got.Label, Actor: s.Email,
	})
	if err != nil {
		storeErr(w, err)
		return
	}
	h.auditPaymentMethod(r, saved, "payment_method.confirm", nil)
	writeJSON(w, http.StatusOK, saved)
}

// listPaymentMethods — GET /customers/{id}/payment-methods. A read, so
// metering.read on the customer: an operator sees THAT a method exists,
// with its brand, last four and expiry, and never its token — store.
// PaymentMethod has no JSON field for one.
func (h *Handler) listPaymentMethods(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := h.requireCustomer(w, r, id, false)
	if !ok {
		return
	}
	list, err := h.Store.ListPaymentMethods(r.Context(), s.Scope(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"payment_methods": list})
}

// getPaymentMethod — GET /payment-methods/{id}, the same read by id alone.
func (h *Handler) getPaymentMethod(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	m, err := h.Store.GetPaymentMethod(r.Context(), s.Scope(), r.PathValue("id"))
	if err != nil {
		storeErr(w, err)
		return
	}
	if !access.Has(access.Bindings(s), access.MeteringRead, m.CustomerID) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// deleteCustomerPaymentMethod — DELETE /customers/{id}/payment-methods/{mid}.
func (h *Handler) deleteCustomerPaymentMethod(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := h.requireAnyPermission(w, r, id, access.AccountTopup, access.BillingCollect)
	if !ok {
		return
	}
	h.removePaymentMethod(w, r, s, r.PathValue("mid"), id)
}

// deletePaymentMethod — DELETE /payment-methods/{id}: the same removal
// addressed by the method's own id. The permission is still asked on the
// customer the method belongs to, resolved through the session's scope.
func (h *Handler) deletePaymentMethod(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	m, err := h.Store.GetPaymentMethod(r.Context(), s.Scope(), r.PathValue("id"))
	if err != nil {
		storeErr(w, err)
		return
	}
	if _, ok := h.requireAnyPermission(w, r, m.CustomerID, access.AccountTopup, access.BillingCollect); !ok {
		return
	}
	h.removePaymentMethod(w, r, s, m.ID, m.CustomerID)
}

func (h *Handler) removePaymentMethod(w http.ResponseWriter, r *http.Request, s store.Session, methodID, customerID string) {
	m, err := h.Store.GetPaymentMethod(r.Context(), s.Scope(), methodID)
	if err != nil {
		storeErr(w, err)
		return
	}
	if m.CustomerID != customerID {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	removed, err := h.Store.RemovePaymentMethod(r.Context(), s.Scope(), methodID, s.Email)
	if err != nil {
		storeErr(w, err)
		return
	}
	h.auditPaymentMethod(r, removed, "payment_method.remove", nil)
	writeJSON(w, http.StatusOK, removed)
}

// auditPaymentMethod records one payment-method event. The details carry the
// method id, the gateway and the display triple — never the token, and never
// anything that could be used to charge the instrument.
func (h *Handler) auditPaymentMethod(r *http.Request, m store.PaymentMethod, action string, extra map[string]any) {
	details := map[string]any{
		"payment_method_id": m.ID, "gateway": m.Gateway, "status": m.Status,
		"brand": m.Brand, "last4": m.Last4, "saved": m.Saved,
	}
	for k, v := range extra {
		if v == nil || v == "" {
			continue
		}
		details[k] = v
	}
	cid := m.CustomerID
	h.audit(r, &cid, action, details)
}

// ---------------------------------------------------------------------------
// invoice disputes
// ---------------------------------------------------------------------------

// createDispute — POST /statements/{id}/disputes {reason, lines?}.
//
// The customer says what it objects to on its own invoice. The disputed
// amount STAYS on the balance — a dispute is not a credit — and the invoice
// is excluded from collections chasing (the aging report names it disputed,
// the evaluator passes over it) until an operator resolves it.
//
// The statement is read through the SESSION's scope first, so a customer
// disputing another customer's invoice gets 404 and learns nothing.
func (h *Handler) createDispute(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	st, err := h.Store.GetStatement(r.Context(), s.Scope(), r.PathValue("id"))
	if err != nil {
		storeErr(w, err)
		return
	}
	if _, ok := h.requireAnyPermission(w, r, st.CustomerID, access.AccountTopup, access.BillingCollect); !ok {
		return
	}
	var in struct {
		Reason string   `json:"reason"`
		Lines  []string `json:"lines"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if strings.TrimSpace(in.Reason) == "" {
		writeErr(w, http.StatusBadRequest, "a dispute carries the reason it was raised")
		return
	}
	d, err := h.Store.OpenDispute(r.Context(), st.ID, store.DisputeInput{Reason: in.Reason, Lines: in.Lines, Actor: s.Email})
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeErr(w, http.StatusBadRequest, invalidMessage(err))
		return
	case err != nil:
		storeErr(w, err)
		return
	}
	h.audit(r, &d.CustomerID, "dispute.open", map[string]any{"dispute_id": d.ID, "statement_id": d.StatementID, "invoice_number": d.InvoiceNumber,
		"reason": d.Reason, "amount": d.Amount, "currency": d.Currency, "lines": len(in.Lines)})
	writeJSON(w, http.StatusCreated, d)
}

// resolveDispute — POST /disputes/{id}/resolve {outcome, note}.
//
// UPHELD issues a credit note for the disputed amount through the EXISTING
// credit-note machinery — the same numbering, the same allocation against
// the invoice, the same ledger entry — so a dispute has no settlement path
// of its own. REJECTED clears the flag and collections resume from the age
// the invoice then is. Either way the flag is cleared and the outcome is
// audited.
func (h *Handler) resolveDispute(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSovereign(w, r, access.BillingCollect)
	if !ok {
		return
	}
	var in struct {
		Outcome string `json:"outcome"`
		Note    string `json:"note"`
	}
	if err := decode(r, &in); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	outcome := strings.ToLower(strings.TrimSpace(in.Outcome))
	if outcome != store.DisputeUpheld && outcome != store.DisputeRejected {
		writeErr(w, http.StatusBadRequest, "outcome must be upheld or rejected")
		return
	}
	d, err := h.Store.GetDispute(r.Context(), store.OperatorScope, r.PathValue("id"))
	if err != nil {
		storeErr(w, err)
		return
	}
	if d.Status != store.DisputeOpen {
		writeErr(w, http.StatusConflict, "this dispute was already "+d.Status)
		return
	}
	if outcome == store.DisputeUpheld {
		// The invoice is the billing system's in external mode; so is any
		// correction to it — the same refusal createCreditNote makes.
		if _, settings, err := h.Commercial.For(r.Context()); err != nil {
			storeErr(w, err)
			return
		} else if settings.ExternalCommercial() && !settings.SummaryCharge() {
			writeErr(w, http.StatusConflict, "crediting the invoice is "+commercial.ErrExternallyOwned.Error())
			return
		}
	}
	// The store issues the credit note and records the outcome in ONE
	// transaction, so an upheld dispute is never credited-but-open nor
	// resolved-without-the-credit.
	resolved, err := h.Store.ResolveDispute(r.Context(), d.ID, outcome, in.Note, s.Email)
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeErr(w, http.StatusBadRequest, invalidMessage(err))
		return
	case err != nil:
		storeErr(w, err)
		return
	}
	if resolved.CreditNoteID != "" {
		if note, nerr := h.Store.GetCreditNote(r.Context(), store.OperatorScope, resolved.CreditNoteID); nerr == nil {
			h.audit(r, &note.CustomerID, "statement.credit_note", map[string]any{"statement_id": note.StatementID, "invoice_number": note.InvoiceNumber,
				"credit_note_id": note.ID, "number": note.Number, "kind": note.Kind, "reason": note.Reason, "total": note.Total, "dispute_id": resolved.ID})
		} else {
			slog.Warn("dispute credit note read-back", "dispute", resolved.ID, "credit_note", resolved.CreditNoteID, "error", nerr)
		}
	}
	h.audit(r, &resolved.CustomerID, "dispute.resolve", map[string]any{"dispute_id": resolved.ID, "statement_id": resolved.StatementID,
		"invoice_number": resolved.InvoiceNumber, "outcome": resolved.Status, "amount": resolved.Amount, "note": resolved.Note, "credit_note_id": resolved.CreditNoteID})
	h.afterAccountChange(r, resolved.CustomerID, nil)
	writeJSON(w, http.StatusOK, resolved)
}

// getDispute — GET /disputes/{id}, inside the session's scope.
func (h *Handler) getDispute(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	d, err := h.Store.GetDispute(r.Context(), s.Scope(), r.PathValue("id"))
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// listStatementDisputes — GET /statements/{id}/disputes.
func (h *Handler) listStatementDisputes(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	// The statement read is the scope check: another customer's invoice is
	// 404 before any dispute of it is listed.
	st, err := h.Store.GetStatement(r.Context(), s.Scope(), r.PathValue("id"))
	if err != nil {
		storeErr(w, err)
		return
	}
	list, err := h.Store.ListStatementDisputes(r.Context(), s.Scope(), st.ID)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"disputes": list})
}

// listCustomerDisputes — GET /customers/{id}/disputes.
func (h *Handler) listCustomerDisputes(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := h.requireCustomer(w, r, id, false)
	if !ok {
		return
	}
	list, err := h.Store.ListCustomerDisputes(r.Context(), s.Scope(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"disputes": list})
}
