package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/commercial/external"
	"github.com/openova-io/openova/products/chargeback/internal/settle"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// gatewayCallback — POST /api/v1/gateways/{name}/callback (DESIGN.md §9.2).
//
// The route that books a gateway's payment confirmation WITHOUT an operator
// click. It is unauthenticated: the gateway is a machine in someone else's
// estate, so the request is verified by the gateway's OWN signature scheme
// through settle.Gateway.VerifyCallback — over the raw bytes, before the
// body is decoded — and refused when the gateway has none (the manual
// gateway: a transfer is recorded by the operator).
//
// What the confirmation names decides how it is booked: a statement_id is a
// COLLECTION and goes through the commercial provider (refused 409 when the
// external billing system owns the receivable); no statement is a CHECKOUT
// — a top-up — and lands as credit on the account in every mode. It is
// idempotent on the gateway's reference: a redelivered confirmation answers
// 200 with the payment already booked, never a second one.
func (h *Handler) gatewayCallback(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	g, ok := h.Settlement.Gateway(name)
	if !ok || g == nil {
		writeErr(w, http.StatusNotFound, "no gateway is registered under "+name)
		return
	}
	conf, err := g.VerifyCallback(r)
	switch {
	case errors.Is(err, settle.ErrCallbackNotSupported):
		writeErr(w, http.StatusNotImplemented, err.Error())
		return
	case errors.Is(err, settle.ErrCallbackRejected):
		slog.Warn("gateway callback: rejected", "gateway", name, "remote", r.RemoteAddr, "error", err)
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	case errors.Is(err, store.ErrInvalid):
		writeErr(w, http.StatusBadRequest, invalidMessage(err))
		return
	case err != nil:
		storeErr(w, err)
		return
	}
	if strings.TrimSpace(conf.Reference) == "" {
		writeErr(w, http.StatusBadRequest, "a gateway confirmation carries the reference that proves it")
		return
	}
	status, err := external.MapPaymentStatus(conf.Status)
	if err != nil {
		writeErr(w, http.StatusBadRequest, invalidMessage(err))
		return
	}
	ctx := r.Context()
	// Resolve what it is about.
	var st store.Statement
	if conf.StatementID != "" {
		if st, err = h.Store.GetStatement(ctx, store.OperatorScope, conf.StatementID); err != nil {
			storeErr(w, err)
			return
		}
		conf.CustomerID = st.CustomerID
	}
	if conf.CustomerID == "" && conf.IntentID != "" {
		intent, err := h.Store.GetPaymentIntent(ctx, store.OperatorScope, conf.IntentID)
		if err != nil {
			storeErr(w, err)
			return
		}
		conf.CustomerID = intent.CustomerID
		if conf.StatementID == "" && intent.StatementID != "" {
			if st, err = h.Store.GetStatement(ctx, store.OperatorScope, intent.StatementID); err != nil {
				storeErr(w, err)
				return
			}
			conf.StatementID = st.ID
		}
	}
	var c store.Customer
	switch {
	case conf.CustomerID != "":
		c, err = h.Store.GetCustomer(ctx, store.OperatorScope, conf.CustomerID)
	case conf.CustomerSlug != "":
		c, err = h.Store.GetCustomerBySlug(ctx, strings.ToLower(conf.CustomerSlug))
	default:
		writeErr(w, http.StatusBadRequest, "the confirmation names no customer, statement or intent")
		return
	}
	if err != nil {
		storeErr(w, err)
		return
	}
	// Idempotent on the reference: already booked is already booked.
	if existing, err := h.Store.FindPaymentByReference(ctx, c.ID, conf.Reference); err == nil {
		writeJSON(w, http.StatusOK, map[string]any{"payment": existing, "duplicate": true})
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		storeErr(w, err)
		return
	}
	conf.Customer, conf.Statement = c, st
	pay, err := g.ConfirmSettlement(ctx, conf)
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeErr(w, http.StatusBadRequest, invalidMessage(err))
		return
	case err != nil:
		storeErr(w, err)
		return
	}
	pay.Status = status
	if pay.Gateway == "" || pay.Gateway == "manual" {
		pay.Gateway = name
	}
	actor := "gateway:" + name
	var booked store.Payment
	if st.ID != "" {
		// A collection: the provider owns it.
		provider, _, err := h.Commercial.For(ctx)
		if err != nil {
			storeErr(w, err)
			return
		}
		var got store.Statement
		got, booked, err = provider.RecordPayment(ctx, st.ID, store.PaymentInput{
			Amount: pay.Amount, PaidAt: pay.PaidAt, Method: store.PaymentMethodGateway, Reference: pay.Reference, Status: pay.Status, Gateway: pay.Gateway, Actor: actor,
		})
		if err != nil {
			storeErr(w, err)
			return
		}
		if conf.IntentID != "" {
			if _, uerr := h.Store.UpdatePaymentIntent(ctx, conf.IntentID, intentStatusFor(pay.Status), name, pay.Reference, "", "confirmed by the gateway"); uerr != nil {
				slog.Warn("gateway callback: intent update", "intent", conf.IntentID, "error", uerr)
			}
		}
		h.Store.Audit(ctx, &c.ID, actor, "gateway.callback", map[string]any{"gateway": name, "statement_id": got.ID, "invoice_number": got.InvoiceNumber, "payment_id": booked.ID, "amount": booked.Amount, "reference": booked.Reference, "payment_status": booked.Status, "paid_total": got.Paid, "balance": got.Balance, "status": got.Status})
		h.afterAccountChange(r, c.ID, nil)
		writeJSON(w, http.StatusOK, map[string]any{"statement": got, "payment": booked})
		return
	}
	// A checkout: credit on the account, in every mode.
	booked, err = h.Store.RecordCustomerPayment(ctx, store.CustomerPaymentInput{
		CustomerID: c.ID, Purpose: store.PurposeCheckout, IntentID: conf.IntentID, Note: "confirmed by the gateway",
		Payment: store.PaymentInput{Amount: pay.Amount, PaidAt: pay.PaidAt, Method: store.PaymentMethodGateway, Reference: pay.Reference, Status: pay.Status, Gateway: pay.Gateway, Actor: actor},
	})
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeErr(w, http.StatusBadRequest, invalidMessage(err))
		return
	case err != nil:
		storeErr(w, err)
		return
	}
	h.Store.Audit(ctx, &c.ID, actor, "gateway.callback", map[string]any{"gateway": name, "payment_id": booked.ID, "amount": booked.Amount, "reference": booked.Reference, "payment_status": booked.Status, "purpose": booked.Purpose, "unallocated": booked.Unallocated})
	h.afterAccountChange(r, c.ID, &booked)
	writeJSON(w, http.StatusOK, map[string]any{"payment": booked})
}

// intentStatusFor maps a booked payment's status onto the intent's.
func intentStatusFor(paymentStatus string) string {
	switch paymentStatus {
	case store.PaymentReceived:
		return store.IntentSettled
	case store.PaymentPending:
		return store.IntentPending
	case store.PaymentFailed:
		return store.IntentFailed
	}
	return store.IntentPending
}
