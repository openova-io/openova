package api

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/access"
	"github.com/openova-io/openova/products/chargeback/internal/collections"
	"github.com/openova-io/openova/products/chargeback/internal/commercial"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The customer account, payments, credit notes, collections and enforcement
// (DESIGN.md §9).
//
//	GET  /customers/{id}/account                 the ledger, the balance, what is applicable
//	GET  /customers/{id}/payments                every payment with its allocations
//	POST /customers/{id}/payments  · POST /payments
//	                                             a payment: allocated, or credit on account
//	GET  /payments/{id}  · POST /payments/{id}/allocate  · POST /payments/{id}/refund
//	POST /customers/{id}/account/apply-credit    settle invoices from available credit
//	POST /customers/{id}/payment-intents         ask the gateway seam (checkout | collection)
//	GET  /customers/{id}/payment-intents
//	POST /statements/{id}/credit-notes  · GET /statements/{id}/credit-notes
//	GET  /customers/{id}/credit-notes  · GET /credit-notes/{id}
//	GET  /collections/aging                      the aging report
//	POST /collections/run                        run the evaluator now (operator)
//	POST /customers/{id}/suspend  · POST /customers/{id}/resume
//	GET  /customers/{id}/suspensions
//
// Reads follow the session scope (a customer sees its own account); every
// write is operator-only and audited.

// accountDocument is GET /customers/{id}/account.
type accountDocument struct {
	CustomerID string `json:"customer_id"`
	Currency   string `json:"currency"`
	// Balance is the ledger sum: positive owed, negative in credit.
	Balance         store.Decimal `json:"balance"`
	AvailableCredit store.Decimal `json:"available_credit"`
	Outstanding     store.Decimal `json:"outstanding"`
	Overdue         store.Decimal `json:"overdue"`
	OpenInvoices    int           `json:"open_invoices"`
	// Owner says who owns the account: internal, or the external billing
	// system, whose last reported balance is carried beside ours.
	Owner             string               `json:"account_owner"`
	ExternalBalance   *store.Decimal       `json:"external_balance,omitempty"`
	ExternalBalanceAt *time.Time           `json:"external_balance_at,omitempty"`
	Entries           []store.AccountEntry `json:"entries"`
	Payments          []store.Payment      `json:"payments"`
	CreditNotes       []store.CreditNote   `json:"credit_notes"`
	Suspension        *suspensionState     `json:"suspension,omitempty"`
	PaymentModel      string               `json:"payment_model,omitempty"`
	AutoApplyCredit   bool                 `json:"auto_apply_credit"`
	SuspendAtZero     bool                 `json:"suspend_at_zero"`
	LowBalance        *store.Decimal       `json:"low_balance_threshold,omitempty"`
}

type suspensionState struct {
	SuspendedAt time.Time `json:"suspended_at"`
	Source      string    `json:"source"`
	Reason      string    `json:"reason,omitempty"`
}

func (h *Handler) getAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := h.requireCustomer(w, r, id, false)
	if !ok {
		return
	}
	h.writeAccount(w, r, s.Scope(), id)
}

// writeAccount is the account document of ONE party — a customer, or a
// partner's own party row (DESIGN.md §13). It takes the scope and the
// id rather than reading them off the request because the partner endpoint
// has already authorised at the partner scope.
func (h *Handler) writeAccount(w http.ResponseWriter, r *http.Request, scope store.Scope, id string) {
	c, err := h.Store.GetCustomer(r.Context(), scope, id)
	if err != nil {
		storeErr(w, err)
		return
	}
	bal, err := h.Store.GetAccountBalance(r.Context(), scope, id)
	if err != nil {
		storeErr(w, err)
		return
	}
	entries, err := h.Store.ListAccountEntries(r.Context(), scope, id, 0)
	if err != nil {
		storeErr(w, err)
		return
	}
	payments, err := h.Store.ListCustomerPayments(r.Context(), scope, id)
	if err != nil {
		storeErr(w, err)
		return
	}
	notes, err := h.Store.ListCustomerCreditNotes(r.Context(), scope, id)
	if err != nil {
		storeErr(w, err)
		return
	}
	open, err := h.Store.ListOpenInvoices(r.Context(), store.CustomerScope(id))
	if err != nil {
		storeErr(w, err)
		return
	}
	currency, _ := h.Store.CustomerCurrency(r.Context(), id)
	settings, _ := h.Store.GetBillingSettings(r.Context())
	now := h.Now().UTC()
	aging := collections.BuildAging(open, now, nil, settings.CommercialProvider)
	doc := accountDocument{
		CustomerID: id, Currency: currency, Balance: bal.Balance, AvailableCredit: bal.AvailableCredit, Outstanding: bal.Outstanding, Overdue: aging.Overdue,
		OpenInvoices: len(open), Owner: settings.CommercialProvider, ExternalBalance: c.ExternalBalance, ExternalBalanceAt: c.ExternalBalanceAt,
		Entries: entries, Payments: payments, CreditNotes: notes, PaymentModel: c.PaymentModel, AutoApplyCredit: c.AutoApplyCredit, SuspendAtZero: c.SuspendAtZero, LowBalance: c.LowBalanceThreshold,
	}
	if c.PlatformSuspendedAt != nil {
		doc.Suspension = &suspensionState{SuspendedAt: *c.PlatformSuspendedAt, Source: c.SuspensionSource, Reason: c.SuspensionReason}
	}
	writeJSON(w, http.StatusOK, doc)
}

func (h *Handler) listCustomerPayments(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := h.requireCustomer(w, r, id, false)
	if !ok {
		return
	}
	list, err := h.Store.ListCustomerPayments(r.Context(), s.Scope(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"payments": list})
}

func (h *Handler) getPayment(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	p, err := h.Store.GetPayment(r.Context(), s.Scope(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

type allocationBody struct {
	StatementID string        `json:"statement_id"`
	Amount      store.Decimal `json:"amount"`
}

func allocationsFrom(in []allocationBody) []store.AllocationInput {
	out := make([]store.AllocationInput, 0, len(in))
	for _, a := range in {
		out = append(out, store.AllocationInput{StatementID: strings.TrimSpace(a.StatementID), Amount: a.Amount})
	}
	return out
}

// recordPayment — POST /payments and POST /customers/{id}/payments. A
// payment with allocations settles those invoices; one with none is a
// top-up: credit on the account for ANY customer (DESIGN.md §9.5). It goes
// through the customer's gateway ConfirmSettlement first, exactly as the
// per-invoice endpoint does, so a gateway callback and an operator's
// transfer take one path.
func (h *Handler) recordPayment(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSovereign(w, r, access.BillingCollect)
	if !ok {
		return
	}
	var in struct {
		CustomerID  string           `json:"customer_id"`
		Amount      store.Decimal    `json:"amount"`
		PaidAt      string           `json:"paid_at"`
		Method      string           `json:"method"`
		Reference   string           `json:"reference"`
		Status      string           `json:"status"`
		Purpose     string           `json:"purpose"`
		IntentID    string           `json:"intent_id"`
		Note        string           `json:"note"`
		Allocations []allocationBody `json:"allocations"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	customerID := r.PathValue("id")
	if customerID == "" {
		customerID = strings.TrimSpace(in.CustomerID)
	}
	if customerID == "" {
		writeErr(w, http.StatusBadRequest, "customer_id is required")
		return
	}
	paidAt, msg := parsePaidAt(in.PaidAt)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	c, err := h.Store.GetCustomer(r.Context(), store.OperatorScope, customerID)
	if err != nil {
		storeErr(w, err)
		return
	}
	// A payment against invoices in external mode is theirs to record; a
	// top-up (a checkout) is a sale and is ours in every mode.
	purpose := strings.ToLower(strings.TrimSpace(in.Purpose))
	if purpose == "" {
		purpose = store.PurposeCheckout
		if len(in.Allocations) > 0 {
			purpose = store.PurposeCollection
		}
	}
	if purpose == store.PurposeCollection {
		if _, err := h.Commercial.AllowsCollection(r.Context()); err != nil {
			storeErr(w, err)
			return
		}
	}
	pay, err := h.Settlement.ConfirmSettlement(r.Context(), settleConfirmation(c, in.Amount, paidAt, in.Reference, s.Email))
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeErr(w, http.StatusBadRequest, invalidMessage(err))
		return
	case err != nil:
		storeErr(w, err)
		return
	}
	if strings.TrimSpace(in.Method) != "" {
		pay.Method = strings.ToLower(strings.TrimSpace(in.Method))
	}
	if strings.TrimSpace(in.Status) != "" {
		pay.Status = strings.ToLower(strings.TrimSpace(in.Status))
	}
	p, err := h.Store.RecordCustomerPayment(r.Context(), store.CustomerPaymentInput{
		CustomerID: c.ID, Purpose: purpose, IntentID: strings.TrimSpace(in.IntentID), Note: in.Note, Allocations: allocationsFrom(in.Allocations),
		Payment: store.PaymentInput{Amount: pay.Amount, PaidAt: pay.PaidAt, Method: pay.Method, Reference: pay.Reference, Status: pay.Status, Gateway: pay.Gateway, Actor: s.Email},
	})
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeErr(w, http.StatusBadRequest, invalidMessage(err))
		return
	case err != nil:
		storeErr(w, err)
		return
	}
	h.audit(r, &c.ID, "account.payment", map[string]any{"payment_id": p.ID, "amount": p.Amount, "paid_at": p.PaidAt, "method": p.Method, "reference": p.Reference,
		"payment_status": p.Status, "gateway": p.Gateway, "purpose": p.Purpose, "allocated": p.Allocated, "unallocated": p.Unallocated})
	h.afterAccountChange(r, c.ID, &p)
	writeJSON(w, http.StatusCreated, p)
}

// afterAccountChange is what every account movement triggers: a wallet
// check for a prepaid customer, the lifting of a suspension this product
// holds once the customer is settled, and — in external mode — the export
// of a settled checkout payment to the billing system that owns the
// account.
func (h *Handler) afterAccountChange(r *http.Request, customerID string, settled *store.Payment) {
	ctx := r.Context()
	if h.Wallet != nil {
		if out, err := h.Wallet.Check(ctx, customerID, h.Now().UTC()); err != nil {
			slog.Warn("wallet check", "customer", customerID, "error", err)
		} else if out.Alerted || out.Suspended || out.Resumed {
			slog.Info("wallet", "customer", customerID, "alerted", out.Alerted, "suspended", out.Suspended, "resumed", out.Resumed)
		}
	}
	if h.Enforcer != nil {
		if _, err := h.Enforcer.Settle(ctx, customerID, h.Now().UTC()); err != nil {
			slog.Warn("enforcement settle", "customer", customerID, "error", err)
		}
	}
	if settled != nil && settled.Status == store.PaymentReceived && settled.Purpose == store.PurposeCheckout && h.Commercial != nil {
		if err := h.Commercial.QueuePaymentExport(ctx, *settled); err != nil {
			slog.Warn("queue payment export", "payment", settled.ID, "error", err)
		}
	}
}

func (h *Handler) allocatePayment(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSovereign(w, r, access.BillingCollect)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	var in struct {
		Allocations []allocationBody `json:"allocations"`
		Auto        bool             `json:"auto"`
	}
	if err := decode(r, &in); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if _, err := h.Commercial.AllowsCollection(r.Context()); err != nil {
		storeErr(w, err)
		return
	}
	p, err := h.Store.AllocatePayment(r.Context(), id, allocationsFrom(in.Allocations), in.Auto, s.Email)
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeErr(w, http.StatusBadRequest, invalidMessage(err))
		return
	case err != nil:
		storeErr(w, err)
		return
	}
	h.audit(r, &p.CustomerID, "account.allocate", map[string]any{"payment_id": p.ID, "auto": in.Auto, "allocated": p.Allocated, "unallocated": p.Unallocated, "allocations": p.Allocations})
	h.afterAccountChange(r, p.CustomerID, nil)
	writeJSON(w, http.StatusOK, p)
}

func (h *Handler) refundPayment(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSovereign(w, r, access.BillingCollect)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err := decode(r, &in); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	p, err := h.Store.RefundPayment(r.Context(), id, in.Reason, s.Email)
	if err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, &p.CustomerID, "account.refund", map[string]any{"payment_id": p.ID, "amount": p.Amount, "reason": in.Reason, "reference": p.Reference})
	h.afterAccountChange(r, p.CustomerID, nil)
	writeJSON(w, http.StatusOK, p)
}

// applyCredit — POST /customers/{id}/account/apply-credit. The invoices
// named, in that order, or every open invoice oldest due first. Explicit,
// never implicit (DESIGN.md §9.5).
func (h *Handler) applyCredit(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSovereign(w, r, access.BillingCollect)
	if !ok {
		return
	}
	id := r.PathValue("id")
	var in struct {
		StatementIDs []string `json:"statement_ids"`
	}
	if err := decode(r, &in); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if _, err := h.Commercial.AllowsCollection(r.Context()); err != nil {
		storeErr(w, err)
		return
	}
	applied, err := h.Store.ApplyCredit(r.Context(), id, in.StatementIDs, s.Email)
	if err != nil {
		storeErr(w, err)
		return
	}
	bal, err := h.Store.GetAccountBalance(r.Context(), store.OperatorScope, id)
	if err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, &id, "account.apply_credit", map[string]any{"applied": applied, "available_credit": bal.AvailableCredit, "outstanding": bal.Outstanding})
	h.afterAccountChange(r, id, nil)
	writeJSON(w, http.StatusOK, map[string]any{"applied": applied, "balance": bal})
}

// createPaymentIntent — POST /customers/{id}/payment-intents. The gateway
// seam under the provider check (DESIGN.md §9.2). This is the ONE money
// write a customer may make on its own account (DESIGN.md §10,
// account.topup): it asks the gateway to collect — nothing is booked until
// the gateway confirms. An operator with billing.collect may request it on
// the customer's behalf.
func (h *Handler) createPaymentIntent(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireAnyPermission(w, r, r.PathValue("id"), access.AccountTopup, access.BillingCollect)
	if !ok {
		return
	}
	if h.Intents == nil {
		writeErr(w, http.StatusServiceUnavailable, "payment intents are not wired on this Sovereign")
		return
	}
	var in struct {
		Purpose     string        `json:"purpose"`
		StatementID string        `json:"statement_id"`
		Amount      store.Decimal `json:"amount"`
		Reference   string        `json:"reference"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	id := r.PathValue("id")
	intent, err := h.Intents.Request(r.Context(), commercial.IntentRequest{CustomerID: id, Purpose: in.Purpose, StatementID: in.StatementID, Amount: in.Amount, Reference: in.Reference, Actor: s.Email})
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeErr(w, http.StatusBadRequest, invalidMessage(err))
		return
	case err != nil && intent.ID == "":
		storeErr(w, err)
		return
	}
	h.audit(r, &id, "payment.intent", map[string]any{"intent_id": intent.ID, "purpose": intent.Purpose, "statement_id": intent.StatementID, "amount": intent.Amount, "status": intent.Status, "gateway": intent.Gateway, "detail": intent.Detail})
	if intent.Status == store.IntentSettled {
		if p, perr := h.Store.GetPayment(r.Context(), store.OperatorScope, intent.PaymentID); perr == nil {
			h.afterAccountChange(r, id, &p)
		}
	}
	writeJSON(w, http.StatusCreated, intent)
}

func (h *Handler) listPaymentIntents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := h.requireCustomer(w, r, id, false)
	if !ok {
		return
	}
	list, err := h.Store.ListPaymentIntents(r.Context(), s.Scope(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"intents": list})
}

// createCreditNote — POST /statements/{id}/credit-notes (DESIGN.md §9.3).
func (h *Handler) createCreditNote(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSovereign(w, r, access.BillingIssue)
	if !ok {
		return
	}
	var in struct {
		Reason string                 `json:"reason"`
		Amount store.Decimal          `json:"amount"`
		Lines  []store.CreditNoteLine `json:"lines"`
		Kind   string                 `json:"kind"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if strings.TrimSpace(in.Reason) == "" {
		writeErr(w, http.StatusBadRequest, "a credit note carries the reason it was issued")
		return
	}
	// The invoice is the billing system's in external mode; so is any
	// correction to it.
	if _, settings, err := h.Commercial.For(r.Context()); err != nil {
		storeErr(w, err)
		return
	} else if settings.ExternalCommercial() && !settings.SummaryCharge() {
		writeErr(w, http.StatusConflict, "crediting the invoice is "+commercial.ErrExternallyOwned.Error())
		return
	}
	note, err := h.Store.CreateCreditNote(r.Context(), r.PathValue("id"), store.CreditNoteInput{Reason: in.Reason, Amount: in.Amount, Lines: in.Lines, Kind: strings.ToLower(strings.TrimSpace(in.Kind)), Actor: s.Email})
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeErr(w, http.StatusBadRequest, invalidMessage(err))
		return
	case err != nil:
		storeErr(w, err)
		return
	}
	h.audit(r, &note.CustomerID, "statement.credit_note", map[string]any{"statement_id": note.StatementID, "invoice_number": note.InvoiceNumber, "credit_note_id": note.ID, "number": note.Number, "kind": note.Kind, "reason": note.Reason, "total": note.Total, "applied": note.Applied, "unapplied": note.Unapplied})
	h.afterAccountChange(r, note.CustomerID, nil)
	writeJSON(w, http.StatusCreated, note)
}

func (h *Handler) listStatementCreditNotes(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	list, err := h.Store.ListStatementCreditNotes(r.Context(), s.Scope(), r.PathValue("id"))
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"credit_notes": list})
}

func (h *Handler) listCustomerCreditNotes(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := h.requireCustomer(w, r, id, false)
	if !ok {
		return
	}
	list, err := h.Store.ListCustomerCreditNotes(r.Context(), s.Scope(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"credit_notes": list})
}

func (h *Handler) getCreditNote(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	n, err := h.Store.GetCreditNote(r.Context(), s.Scope(), r.PathValue("id"))
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

// aging — GET /collections/aging (DESIGN.md §9.6). Operator-wide, or the
// customer's own rows for a customer principal.
func (h *Handler) aging(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	open, err := h.Store.ListOpenInvoices(r.Context(), s.Scope())
	if err != nil {
		storeErr(w, err)
		return
	}
	settings, err := h.Store.GetBillingSettings(r.Context())
	if err != nil {
		storeErr(w, err)
		return
	}
	facts, err := collections.CustomerFacts(r.Context(), h.Store, s.Scope())
	if err != nil {
		storeErr(w, err)
		return
	}
	now := h.Now().UTC()
	if at := strings.TrimSpace(r.URL.Query().Get("as_of")); at != "" {
		if t, err := time.Parse("2006-01-02", at); err == nil {
			now = t.UTC()
		}
	}
	rep := collections.BuildAging(open, now, facts, settings.CommercialProvider)
	writeJSON(w, http.StatusOK, rep)
}

// runCollections — POST /collections/run: one evaluator pass now, so an
// operator can see the outcome instead of waiting a day.
func (h *Handler) runCollections(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.BillingCollect); !ok {
		return
	}
	if h.Collections == nil {
		writeErr(w, http.StatusServiceUnavailable, "the collections evaluator is not wired on this Sovereign")
		return
	}
	var in struct {
		AsOf string `json:"as_of"`
	}
	if err := decode(r, &in); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	now := h.Now().UTC()
	if strings.TrimSpace(in.AsOf) != "" {
		t, err := time.Parse("2006-01-02", strings.TrimSpace(in.AsOf))
		if err != nil {
			writeErr(w, http.StatusBadRequest, "as_of must be YYYY-MM-DD")
			return
		}
		now = t.UTC()
	}
	rep, err := h.Collections.RunAt(r.Context(), now)
	if err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "collections.run", map[string]any{"as_of": now, "invoices": rep.Invoices, "reminders": rep.Reminders, "escalations": rep.Escalations, "suspended": rep.Suspended, "resumed": rep.Resumed, "skipped": rep.Skipped})
	writeJSON(w, http.StatusOK, rep)
}

// suspendCustomer / resumeCustomer — the operator's explicit enforcement,
// through the same Enforcer the evaluator and the imports use.
func (h *Handler) suspendCustomer(w http.ResponseWriter, r *http.Request) {
	h.enforce(w, r, "suspend")
}

func (h *Handler) resumeCustomer(w http.ResponseWriter, r *http.Request) {
	h.enforce(w, r, "resume")
}

func (h *Handler) enforce(w http.ResponseWriter, r *http.Request, action string) {
	s, ok := h.requireSovereign(w, r, access.BillingCollect)
	if !ok {
		return
	}
	if h.Enforcer == nil {
		writeErr(w, http.StatusServiceUnavailable, "enforcement is not wired on this Sovereign")
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err := decode(r, &in); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	id := r.PathValue("id")
	var rec store.Suspension
	var err error
	if action == "suspend" {
		rec, err = h.Enforcer.Suspend(r.Context(), id, in.Reason, store.SuspendSourceOperator, s.Email)
	} else {
		rec, err = h.Enforcer.Resume(r.Context(), id, in.Reason, store.SuspendSourceOperator, s.Email)
	}
	if err != nil && rec.ID == 0 {
		storeErr(w, err)
		return
	}
	status := http.StatusOK
	if err != nil {
		// Recorded, but the platform refused: the row says so.
		status = http.StatusBadGateway
	}
	writeJSON(w, status, rec)
}

func (h *Handler) listSuspensions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := h.requireCustomer(w, r, id, false)
	if !ok {
		return
	}
	list, err := h.Store.ListSuspensions(r.Context(), s.Scope(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"suspensions": list})
}
