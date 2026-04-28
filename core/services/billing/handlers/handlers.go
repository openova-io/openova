package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/stripe/stripe-go/v81"
	billingportal "github.com/stripe/stripe-go/v81/billingportal/session"
	checkoutsession "github.com/stripe/stripe-go/v81/checkout/session"
	stripecustomer "github.com/stripe/stripe-go/v81/customer"
	"github.com/stripe/stripe-go/v81/webhook"

	"github.com/openova-io/openova/core/services/billing/store"
	"github.com/openova-io/openova/core/services/shared/events"
	"github.com/openova-io/openova/core/services/shared/middleware"
	"github.com/openova-io/openova/core/services/shared/respond"
)

// Handler holds dependencies for billing HTTP handlers.
type Handler struct {
	Store      *store.Store
	Producer   *events.Producer
	SuccessURL string
	CancelURL  string
	CatalogURL string // internal URL to catalog service, e.g. http://catalog.sme.svc.cluster.local:8082
	TenantURL  string // internal URL to tenant service (to dispatch provisioning without broker)
}

// ---------------------------------------------------------------------------
// POST /billing/checkout
// ---------------------------------------------------------------------------

type checkoutRequest struct {
	PlanID    string   `json:"plan_id"`
	Apps      []string `json:"apps"`
	Addons    []string `json:"addons"`
	TenantID  string   `json:"tenant_id"`
	PromoCode string   `json:"promo_code"`
}

type checkoutResponse struct {
	SessionURL    string `json:"session_url,omitempty"`
	OrderID       string `json:"order_id,omitempty"`
	PaidByCredit  bool   `json:"paid_by_credit,omitempty"`
	CreditBalance int    `json:"credit_balance,omitempty"`
}

// Checkout creates an order and either (a) covers it fully from credit, or
// (b) creates a Stripe checkout session for any remaining amount.
//
// Promo codes are redeemed as credits before charging — they never bypass
// Stripe, they only add credit. If the total is larger than available credit
// and Stripe isn't configured, the request fails with a real error.
func (h *Handler) Checkout(w http.ResponseWriter, r *http.Request) {
	var req checkoutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.PlanID == "" || req.TenantID == "" {
		respond.Error(w, http.StatusBadRequest, "plan_id and tenant_id are required")
		return
	}

	ctx := r.Context()
	userID := middleware.UserIDFromContext(ctx)
	if userID == "" {
		respond.Error(w, http.StatusUnauthorized, "missing user identity")
		return
	}

	cust, err := h.Store.GetCustomerByUserID(ctx, userID)
	if err != nil {
		slog.Error("checkout: get customer", "error", err)
		respond.Error(w, http.StatusInternalServerError, "failed to look up customer")
		return
	}

	claims, _ := middleware.ClaimsFromContext(ctx)
	email, _ := claims["email"].(string)

	if cust == nil {
		cust = &store.Customer{UserID: userID, TenantID: req.TenantID, Email: email}
		if err := h.Store.CreateCustomer(ctx, cust); err != nil {
			slog.Error("checkout: create customer", "error", err)
			respond.Error(w, http.StatusInternalServerError, "failed to create customer")
			return
		}
	}

	// Compute total price from catalog FIRST — before touching the promo
	// redemption counter (#93).
	//
	// The previous order was: redeem promo → compute total. That meant a
	// request that failed catalog lookup (missing plan, stale addon ID, etc.)
	// still burned a promo_redemption slot and incremented times_redeemed.
	// The user saw a 400, retried, and discovered their "one-per-customer"
	// promo was already consumed with no order to show for it.
	//
	// New order: compute total → validate → then redeem. If catalog fails,
	// the promo stays untouched. The admin's redemption cap accounting
	// matches the number of orders that actually exist.
	totalOMR, err := h.computeOrderTotal(ctx, req.PlanID, req.Apps, req.Addons)
	if err != nil {
		slog.Error("checkout: compute total", "error", err)
		respond.Error(w, http.StatusBadRequest, "failed to compute order total: "+err.Error())
		return
	}

	// Redeem promo code → credit (if one was provided and valid). Runs only
	// after the total has been computed successfully, so a catalog failure
	// cannot burn a redemption slot (#93).
	if req.PromoCode != "" {
		credit, redeemErr := h.Store.RedeemPromoCode(ctx, cust.ID, req.PromoCode)
		if redeemErr != nil {
			slog.Info("checkout: promo not redeemed",
				"customer_id", cust.ID, "code", req.PromoCode, "reason", redeemErr.Error())
			// Invalid promo is not fatal — surface as error to user.
			respond.Error(w, http.StatusBadRequest, "invalid promo code: "+redeemErr.Error())
			return
		}
		slog.Info("checkout: promo redeemed",
			"customer_id", cust.ID, "code", req.PromoCode, "credit_omr", credit)
	}

	// Check available credit balance.
	creditBalance, err := h.Store.GetCreditBalance(ctx, cust.ID)
	if err != nil {
		slog.Error("checkout: credit balance", "error", err)
		respond.Error(w, http.StatusInternalServerError, "failed to check credit balance")
		return
	}

	appsJSON, _ := json.Marshal(req.Apps)
	addonsJSON, _ := json.Marshal(req.Addons)

	// If credits cover the full order, settle in-place — no Stripe needed.
	// #92 — the order insert, credit spend, and subscription insert are
	// wrapped in a single transaction via CreditOnlyCheckout so we cannot
	// leave the customer with debited credit and no subscription (or
	// vice-versa).
	if creditBalance >= totalOMR {
		order := &store.Order{
			CustomerID: cust.ID, TenantID: req.TenantID, PlanID: req.PlanID,
			Apps: appsJSON, Addons: addonsJSON,
			AmountOMR: totalOMR, Status: "completed",
			PromoCode: req.PromoCode,
		}
		sub := &store.Subscription{
			CustomerID: cust.ID, TenantID: req.TenantID, PlanID: req.PlanID, Status: "active",
		}
		if err := h.Store.CreditOnlyCheckout(ctx, order, sub); err != nil {
			slog.Error("checkout: credit-only checkout", "error", err)
			respond.Error(w, http.StatusInternalServerError, "failed to complete credit-only checkout")
			return
		}
		h.dispatchOrderPlaced(req.TenantID, order)

		respond.OK(w, checkoutResponse{
			OrderID: order.ID, PaidByCredit: true, CreditBalance: creditBalance - totalOMR,
		})
		return
	}

	// Not covered — need Stripe. Load settings.
	settings, err := h.Store.GetSettings(ctx)
	if err != nil {
		slog.Error("checkout: get settings", "error", err)
		respond.Error(w, http.StatusInternalServerError, "failed to load billing settings")
		return
	}
	if settings.StripeSecretKey == "" {
		respond.Error(w, http.StatusServiceUnavailable,
			"payment processor is not configured yet. Please contact support or use a promo code that covers the full amount.")
		return
	}

	// Pending order for Stripe.
	order := &store.Order{
		CustomerID: cust.ID, TenantID: req.TenantID, PlanID: req.PlanID,
		Apps: appsJSON, Addons: addonsJSON,
		AmountOMR: totalOMR, Status: "pending",
		PromoCode: req.PromoCode,
	}
	if err := h.Store.CreateOrder(ctx, order); err != nil {
		slog.Error("checkout: create order", "error", err)
		respond.Error(w, http.StatusInternalServerError, "failed to create order")
		return
	}

	// Stripe customer.
	stripe.Key = settings.StripeSecretKey
	if cust.StripeCustomerID == "" {
		cp := &stripe.CustomerParams{Email: stripe.String(cust.Email)}
		cp.AddMetadata("user_id", userID)
		cp.AddMetadata("tenant_id", req.TenantID)
		sc, err := stripecustomer.New(cp)
		if err != nil {
			slog.Error("checkout: create stripe customer", "error", err)
			respond.Error(w, http.StatusBadGateway, "payment processor rejected the request: "+err.Error())
			return
		}
		if err := h.Store.UpdateStripeCustomerID(ctx, cust.ID, sc.ID); err != nil {
			slog.Error("checkout: update stripe customer id", "error", err)
		}
		cust.StripeCustomerID = sc.ID
	}

	priceID, err := h.resolvePlanStripePriceID(ctx, req.PlanID)
	if err != nil {
		slog.Error("checkout: resolve stripe price", "error", err, "plan_id", req.PlanID)
		respond.Error(w, http.StatusBadRequest, "plan not configured for payment: "+err.Error())
		return
	}

	params := &stripe.CheckoutSessionParams{
		Customer:            stripe.String(cust.StripeCustomerID),
		Mode:                stripe.String(string(stripe.CheckoutSessionModeSubscription)),
		AllowPromotionCodes: stripe.Bool(true),
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{Price: stripe.String(priceID), Quantity: stripe.Int64(1)},
		},
		SuccessURL: stripe.String(h.SuccessURL + "?order_id=" + order.ID),
		CancelURL:  stripe.String(h.CancelURL + "?order_id=" + order.ID),
	}
	params.AddMetadata("order_id", order.ID)
	params.AddMetadata("tenant_id", req.TenantID)
	params.AddMetadata("credit_applied_omr", fmt.Sprintf("%d", creditBalance))

	sess, err := checkoutsession.New(params)
	if err != nil {
		slog.Error("checkout: create stripe session", "error", err)
		respond.Error(w, http.StatusBadGateway, "payment processor rejected the request: "+err.Error())
		return
	}
	_ = h.Store.UpdateOrderStatus(ctx, order.ID, "pending", sess.ID)

	respond.OK(w, checkoutResponse{SessionURL: sess.URL, OrderID: order.ID, CreditBalance: creditBalance})
}

// ---------------------------------------------------------------------------
// POST /billing/webhook
// ---------------------------------------------------------------------------

// Webhook handles Stripe callbacks. Public (no JWT) — auth via signature.
//
// Error semantics (contract with Stripe):
//   - 200: event was either fresh and processed, or a confirmed duplicate.
//   - 400: malformed body or invalid signature — Stripe will not retry.
//   - 500: transient error (DB failure). Stripe WILL retry, and the
//     idempotency guard (#77) ensures the retry is safe.
//
// The body is read, verified, recorded as processed atomically, and ONLY
// THEN dispatched to the type-specific handler. Each handler returns an
// error; a non-nil error propagates to a 500 so Stripe retries (fixes #80).
func (h *Handler) Webhook(w http.ResponseWriter, r *http.Request) {
	const maxBodyBytes = 65536
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	settings, err := h.Store.GetSettings(r.Context())
	if err != nil || settings.StripeWebhookSecret == "" {
		slog.Warn("webhook: secret not configured", "err", err)
		respond.Error(w, http.StatusServiceUnavailable, "webhook not configured")
		return
	}

	sig := r.Header.Get("Stripe-Signature")
	// IgnoreAPIVersionMismatch: Stripe webhook endpoints are pinned to whatever
	// API version was active when they were created in the Stripe dashboard.
	// We don't use any API-version-specific fields in the handlers below
	// (only id, type, customer, amount_paid, currency, metadata), so the
	// version mismatch warning the SDK emits by default is noise that would
	// flip genuine deliveries to 400. Signature + timestamp tolerance still
	// enforced.
	event, err := webhook.ConstructEventWithOptions(body, sig, settings.StripeWebhookSecret,
		webhook.ConstructEventOptions{IgnoreAPIVersionMismatch: true})
	if err != nil {
		slog.Warn("webhook: invalid signature", "error", err)
		respond.Error(w, http.StatusBadRequest, "invalid webhook signature")
		return
	}

	ctx := r.Context()

	// Idempotency (#77): record the event atomically. If the insert conflicts
	// (duplicate delivery), return 200 without re-running side effects.
	fresh, err := h.Store.MarkWebhookEventProcessed(ctx, event.ID, string(event.Type))
	if err != nil {
		slog.Error("webhook: idempotency record failed",
			"event_id", event.ID, "event_type", event.Type, "error", err)
		respond.Error(w, http.StatusInternalServerError, "failed to record event")
		return
	}
	if !fresh {
		slog.Info("webhook: duplicate event ignored",
			"event_id", event.ID, "event_type", event.Type)
		w.WriteHeader(http.StatusOK)
		return
	}

	var handlerErr error
	switch event.Type {
	case "checkout.session.completed":
		handlerErr = h.handleCheckoutCompleted(ctx, event)
	case "invoice.paid":
		handlerErr = h.handleInvoicePaid(ctx, event)
	case "customer.subscription.updated":
		handlerErr = h.handleSubscriptionUpdated(ctx, event)
	case "customer.subscription.deleted":
		handlerErr = h.handleSubscriptionDeleted(ctx, event)
	default:
		slog.Debug("webhook: unhandled event type", "type", event.Type)
	}

	if handlerErr != nil {
		// The event row was inserted before the handler ran. Remove it so
		// Stripe's retry hits the handler cleanly instead of short-circuiting
		// as a "duplicate".
		if delErr := h.Store.DeleteWebhookEvent(ctx, event.ID); delErr != nil {
			slog.Error("webhook: failed to clear event after handler error",
				"event_id", event.ID, "original_error", handlerErr, "delete_error", delErr)
		}
		slog.Error("webhook: handler failed",
			"event_id", event.ID, "event_type", event.Type, "error", handlerErr)
		respond.Error(w, http.StatusInternalServerError, "webhook handler failed")
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (h *Handler) handleCheckoutCompleted(ctx context.Context, event stripe.Event) error {
	var sess stripe.CheckoutSession
	if err := json.Unmarshal(event.Data.Raw, &sess); err != nil {
		slog.Error("webhook: unmarshal checkout session", "event_id", event.ID, "error", err)
		// Malformed event body is not retryable; swallow so Stripe sees 200.
		return nil
	}
	orderID := sess.Metadata["order_id"]
	tenantID := sess.Metadata["tenant_id"]

	if orderID != "" {
		if err := h.Store.UpdateOrderStatus(ctx, orderID, "completed", sess.ID); err != nil {
			slog.Error("webhook: update order status",
				"event_id", event.ID, "order_id", orderID, "error", err)
			return err
		}
	}

	var stripeCustID string
	if sess.Customer != nil {
		stripeCustID = sess.Customer.ID
	}
	if stripeCustID == "" {
		return nil
	}
	cust, err := h.Store.GetCustomerByStripeID(ctx, stripeCustID)
	if err != nil {
		slog.Error("webhook: get customer by stripe id",
			"event_id", event.ID, "stripe_customer_id", stripeCustID, "error", err)
		return err
	}
	if cust == nil {
		slog.Error("webhook: customer not found",
			"event_id", event.ID, "stripe_customer_id", stripeCustID)
		return nil
	}

	var subID string
	if sess.Subscription != nil {
		subID = sess.Subscription.ID
	}
	sub := &store.Subscription{
		CustomerID: cust.ID, TenantID: tenantID,
		StripeSubscriptionID: subID, Status: "active",
	}
	if err := h.Store.CreateSubscription(ctx, sub); err != nil {
		slog.Error("webhook: create subscription",
			"event_id", event.ID, "customer_id", cust.ID, "error", err)
		return err
	}

	if orderID != "" {
		order, err := h.Store.GetOrder(ctx, orderID)
		if err != nil {
			slog.Error("webhook: get order for dispatch",
				"event_id", event.ID, "order_id", orderID, "error", err)
			return err
		}
		if order != nil {
			h.dispatchOrderPlaced(tenantID, order)
		}
	}
	return nil
}

func (h *Handler) handleInvoicePaid(ctx context.Context, event stripe.Event) error {
	var inv stripe.Invoice
	if err := json.Unmarshal(event.Data.Raw, &inv); err != nil {
		slog.Error("webhook: unmarshal invoice", "event_id", event.ID, "error", err)
		return nil
	}
	var cid string
	if inv.Customer != nil {
		cid = inv.Customer.ID
	}
	if cid == "" {
		return nil
	}
	cust, err := h.Store.GetCustomerByStripeID(ctx, cid)
	if err != nil {
		slog.Error("webhook: get customer for invoice",
			"event_id", event.ID, "stripe_customer_id", cid, "error", err)
		return err
	}
	if cust == nil {
		slog.Error("webhook: invoice customer not found",
			"event_id", event.ID, "stripe_customer_id", cid)
		return nil
	}

	// Currency sanity check. Stripe emits the currency as a lower-case ISO
	// code. We only support OMR today; anything else is a config/pricing bug
	// and the amount should NOT be trusted as a baisa value.
	currency := strings.ToLower(string(inv.Currency))
	if currency != "" && currency != "omr" {
		slog.Error("webhook: unexpected invoice currency — refusing to store",
			"event_id", event.ID, "stripe_invoice_id", inv.ID, "currency", currency)
		// Not retryable — it will always be the wrong currency.
		return nil
	}

	// #78 fix: Stripe returns AmountPaid in the smallest currency unit
	// (baisa, 1/1000 OMR). Store baisa as authoritative and derive the
	// whole-OMR view for legacy consumers.
	baisa := inv.AmountPaid
	if err := h.Store.CreateInvoice(ctx, &store.Invoice{
		CustomerID: cust.ID, TenantID: cust.TenantID,
		StripeInvoiceID: inv.ID,
		AmountBaisa:     baisa,
		AmountOMR:       int(baisa / 1000),
		Currency:        "omr",
		Status:          "paid",
		PeriodStart:     time.Unix(inv.PeriodStart, 0),
		PeriodEnd:       time.Unix(inv.PeriodEnd, 0),
		PDFURL:          inv.InvoicePDF,
	}); err != nil {
		slog.Error("webhook: create invoice",
			"event_id", event.ID, "stripe_invoice_id", inv.ID, "error", err)
		return err
	}
	return nil
}

func (h *Handler) handleSubscriptionUpdated(ctx context.Context, event stripe.Event) error {
	var sub stripe.Subscription
	if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
		slog.Error("webhook: unmarshal subscription (updated)", "event_id", event.ID, "error", err)
		return nil
	}
	var cid string
	if sub.Customer != nil {
		cid = sub.Customer.ID
	}
	if cid == "" {
		return nil
	}
	cust, err := h.Store.GetCustomerByStripeID(ctx, cid)
	if err != nil {
		slog.Error("webhook: get customer for sub-updated",
			"event_id", event.ID, "stripe_customer_id", cid, "error", err)
		return err
	}
	if cust == nil {
		return nil
	}
	existing, err := h.Store.GetSubscriptionByTenant(ctx, cust.TenantID)
	if err != nil {
		slog.Error("webhook: get existing sub", "event_id", event.ID, "error", err)
		return err
	}
	if existing == nil {
		return nil
	}
	fields := map[string]any{
		"status":                 string(sub.Status),
		"stripe_subscription_id": sub.ID,
		"current_period_start":   time.Unix(sub.CurrentPeriodStart, 0),
		"current_period_end":     time.Unix(sub.CurrentPeriodEnd, 0),
	}
	if len(sub.Items.Data) > 0 {
		fields["plan_id"] = sub.Items.Data[0].Price.ID
	}
	if err := h.Store.UpdateSubscription(ctx, existing.ID, fields); err != nil {
		slog.Error("webhook: update subscription",
			"event_id", event.ID, "subscription_id", existing.ID, "error", err)
		return err
	}
	return nil
}

func (h *Handler) handleSubscriptionDeleted(ctx context.Context, event stripe.Event) error {
	var sub stripe.Subscription
	if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
		slog.Error("webhook: unmarshal subscription (deleted)", "event_id", event.ID, "error", err)
		return nil
	}
	var cid string
	if sub.Customer != nil {
		cid = sub.Customer.ID
	}
	if cid == "" {
		return nil
	}
	cust, err := h.Store.GetCustomerByStripeID(ctx, cid)
	if err != nil {
		slog.Error("webhook: get customer for sub-deleted",
			"event_id", event.ID, "stripe_customer_id", cid, "error", err)
		return err
	}
	if cust == nil {
		return nil
	}
	existing, err := h.Store.GetSubscriptionByTenant(ctx, cust.TenantID)
	if err != nil {
		slog.Error("webhook: get existing sub (deleted)", "event_id", event.ID, "error", err)
		return err
	}
	if existing == nil {
		return nil
	}
	if err := h.Store.UpdateSubscription(ctx, existing.ID, map[string]any{"status": "canceled"}); err != nil {
		slog.Error("webhook: cancel subscription",
			"event_id", event.ID, "subscription_id", existing.ID, "error", err)
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// Credits
// ---------------------------------------------------------------------------

// GetBalance returns the current credit balance for the signed-in user along
// with the most recent ledger entries so the Console billing page can show the
// history inline.
func (h *Handler) GetBalance(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.UserIDFromContext(ctx)
	if userID == "" {
		respond.Error(w, http.StatusUnauthorized, "missing user identity")
		return
	}
	cust, err := h.Store.GetCustomerByUserID(ctx, userID)
	if err != nil || cust == nil {
		// #85 — emit both legacy (credit_omr) + canonical (credit_baisa).
		respond.OK(w, map[string]any{
			"credit_omr":   0,
			"credit_baisa": 0,
			"entries":      []any{},
		})
		return
	}
	bal, err := h.Store.GetCreditBalance(ctx, cust.ID)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to get balance")
		return
	}
	entries, err := h.Store.ListCreditEntries(ctx, cust.ID, 20)
	if err != nil {
		slog.Warn("list credit entries failed", "error", err)
		entries = nil
	}
	// #85 — emit per-entry amount_baisa alongside the legacy amount_omr so UI
	// clients can normalize to the canonical unit. The credit_ledger currently
	// stores only whole OMR; multiplying by 1000 is safe (no precision loss).
	type legacyEntry struct {
		ID          string    `json:"id"`
		AmountOMR   int       `json:"amount_omr"`
		AmountBaisa int64     `json:"amount_baisa"`
		Reason      string    `json:"reason"`
		OrderID     string    `json:"order_id,omitempty"`
		CreatedAt   time.Time `json:"created_at"`
	}
	view := make([]legacyEntry, 0, len(entries))
	for _, e := range entries {
		view = append(view, legacyEntry{
			ID:          e.ID,
			AmountOMR:   e.AmountOMR,
			AmountBaisa: store.OMRToBaisa(e.AmountOMR),
			Reason:      e.Reason,
			OrderID:     e.OrderID,
			CreatedAt:   e.CreatedAt,
		})
	}
	respond.OK(w, map[string]any{
		"credit_omr":   bal,
		"credit_baisa": store.OMRToBaisa(bal),
		"entries":      view,
	})
}

// ---------------------------------------------------------------------------
// GET /billing/subscription/{tenantId}
// GET /billing/invoices/{tenantId}
// POST /billing/portal/{tenantId}
// ---------------------------------------------------------------------------

func (h *Handler) GetSubscription(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenantId")
	sub, err := h.Store.GetSubscriptionByTenant(r.Context(), tenantID)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to get subscription")
		return
	}
	if sub == nil {
		respond.Error(w, http.StatusNotFound, "no subscription found")
		return
	}
	respond.OK(w, sub)
}

func (h *Handler) ListInvoices(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenantId")
	invoices, err := h.Store.ListInvoicesByTenant(r.Context(), tenantID)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to list invoices")
		return
	}
	respond.OK(w, invoices)
}

func (h *Handler) CreatePortalSession(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenantId")
	ctx := r.Context()
	sub, err := h.Store.GetSubscriptionByTenant(ctx, tenantID)
	if err != nil || sub == nil {
		respond.Error(w, http.StatusNotFound, "no subscription found")
		return
	}
	cust, err := h.Store.GetCustomerByUserID(ctx, middleware.UserIDFromContext(ctx))
	if err != nil || cust == nil || cust.StripeCustomerID == "" {
		respond.Error(w, http.StatusNotFound, "customer not found")
		return
	}
	settings, err := h.Store.GetSettings(ctx)
	if err != nil || settings.StripeSecretKey == "" {
		respond.Error(w, http.StatusServiceUnavailable, "payment processor not configured")
		return
	}
	stripe.Key = settings.StripeSecretKey
	sess, err := billingportal.New(&stripe.BillingPortalSessionParams{
		Customer:  stripe.String(cust.StripeCustomerID),
		ReturnURL: stripe.String(h.SuccessURL),
	})
	if err != nil {
		respond.Error(w, http.StatusBadGateway, "failed to create portal session")
		return
	}
	respond.OK(w, map[string]string{"portal_url": sess.URL})
}

// ---------------------------------------------------------------------------
// Admin — Settings
// ---------------------------------------------------------------------------

// GetAdminSettings returns Stripe key config.  Secret values are masked except
// for the last 4 characters so the admin can verify a key is present.
func (h *Handler) GetAdminSettings(w http.ResponseWriter, r *http.Request) {
	if err := requireAdmin(r); err != nil {
		respond.Error(w, http.StatusForbidden, err.Error())
		return
	}
	s, err := h.Store.GetSettings(r.Context())
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to load settings")
		return
	}
	respond.OK(w, map[string]any{
		"stripe_secret_key_configured":     s.StripeSecretKey != "",
		"stripe_webhook_secret_configured": s.StripeWebhookSecret != "",
		"stripe_secret_key_last4":          last4(s.StripeSecretKey),
		"stripe_webhook_secret_last4":      last4(s.StripeWebhookSecret),
		"stripe_public_key":                s.StripePublicKey,
		"updated_at":                       s.UpdatedAt,
	})
}

// UpdateAdminSettings accepts new Stripe keys. Empty string = clear.
func (h *Handler) UpdateAdminSettings(w http.ResponseWriter, r *http.Request) {
	if err := requireAdmin(r); err != nil {
		respond.Error(w, http.StatusForbidden, err.Error())
		return
	}
	var in store.Settings
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := h.Store.UpdateSettings(r.Context(), &in); err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to save settings")
		return
	}
	respond.OK(w, map[string]bool{"ok": true})
}

// ---------------------------------------------------------------------------
// Admin — Promo Codes
// ---------------------------------------------------------------------------

func (h *Handler) AdminListPromos(w http.ResponseWriter, r *http.Request) {
	if err := requireAdmin(r); err != nil {
		respond.Error(w, http.StatusForbidden, err.Error())
		return
	}
	list, err := h.Store.ListPromoCodes(r.Context())
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to list promos")
		return
	}
	respond.OK(w, list)
}

func (h *Handler) AdminUpsertPromo(w http.ResponseWriter, r *http.Request) {
	if err := requireAdmin(r); err != nil {
		respond.Error(w, http.StatusForbidden, err.Error())
		return
	}
	var p store.PromoCode
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if p.Code == "" || p.CreditOMR <= 0 {
		respond.Error(w, http.StatusBadRequest, "code and credit_omr are required")
		return
	}
	if err := h.Store.UpsertPromoCode(r.Context(), &p); err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to save promo")
		return
	}
	respond.OK(w, p)
}

func (h *Handler) AdminDeletePromo(w http.ResponseWriter, r *http.Request) {
	if err := requireAdmin(r); err != nil {
		respond.Error(w, http.StatusForbidden, err.Error())
		return
	}
	code := r.PathValue("code")
	if code == "" {
		respond.Error(w, http.StatusBadRequest, "code path parameter is required")
		return
	}
	if err := h.Store.DeletePromoCode(r.Context(), code); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			respond.Error(w, http.StatusNotFound, "promo code not found")
			return
		}
		slog.Error("admin delete promo failed", "code", code, "err", err.Error())
		respond.Error(w, http.StatusInternalServerError, "failed to delete promo")
		return
	}
	respond.OK(w, map[string]bool{"ok": true})
}

func (h *Handler) AdminRevenue(w http.ResponseWriter, r *http.Request) {
	if err := requireAdmin(r); err != nil {
		respond.Error(w, http.StatusForbidden, err.Error())
		return
	}
	summary, err := h.Store.GetRevenueSummary(r.Context())
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to get revenue summary")
		return
	}
	// #85 — emit the canonical baisa value alongside the legacy total_mrr
	// (integer OMR). UI clients that understand baisa pick total_mrr_baisa;
	// stale cached clients fall back to total_mrr * 1000.
	respond.OK(w, map[string]any{
		"total_mrr":            summary.TotalMRR,
		"total_mrr_baisa":      store.OMRToBaisa(summary.TotalMRR),
		"total_customers":      summary.TotalCustomers,
		"new_this_month":       summary.NewThisMonth,
		"active_subscriptions": summary.ActiveSubscriptions,
	})
}

func (h *Handler) AdminOrders(w http.ResponseWriter, r *http.Request) {
	if err := requireAdmin(r); err != nil {
		respond.Error(w, http.StatusForbidden, err.Error())
		return
	}
	orders, err := h.Store.ListRecentOrders(r.Context())
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to list orders")
		return
	}
	respond.OK(w, orders)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func requireAdmin(r *http.Request) error {
	if middleware.RoleFromContext(r.Context()) != "superadmin" {
		return fmt.Errorf("superadmin role required")
	}
	return nil
}

func last4(s string) string {
	if len(s) <= 4 {
		return ""
	}
	return s[len(s)-4:]
}

// catalogPlan / catalogApp / catalogAddon are minimal subsets of catalog data.
type catalogPlan struct {
	ID            string `json:"id"`
	StripePriceID string `json:"stripe_price_id"`
	PriceOMR      int    `json:"price_omr"`
}
type catalogAddon struct {
	ID       string `json:"id"`
	PriceOMR int    `json:"price_omr"`
}

func (h *Handler) computeOrderTotal(ctx context.Context, planID string, apps, addons []string) (int, error) {
	if h.CatalogURL == "" {
		return 0, fmt.Errorf("catalog URL not configured")
	}
	plans, err := getCatalog[catalogPlan](ctx, h.CatalogURL+"/catalog/plans")
	if err != nil {
		return 0, err
	}
	var planPrice int
	found := false
	for _, p := range plans {
		if p.ID == planID {
			planPrice = p.PriceOMR
			found = true
			break
		}
	}
	if !found {
		return 0, fmt.Errorf("plan %q not found", planID)
	}

	addonTotal := 0
	if len(addons) > 0 {
		cats, err := getCatalog[catalogAddon](ctx, h.CatalogURL+"/catalog/addons")
		if err == nil {
			byID := make(map[string]int, len(cats))
			for _, a := range cats {
				byID[a.ID] = a.PriceOMR
			}
			for _, id := range addons {
				addonTotal += byID[id]
			}
		}
	}
	// Apps are free for now (catalog app records have no price field).
	_ = apps
	return planPrice + addonTotal, nil
}

func (h *Handler) resolvePlanStripePriceID(ctx context.Context, planID string) (string, error) {
	plans, err := getCatalog[catalogPlan](ctx, h.CatalogURL+"/catalog/plans")
	if err != nil {
		return "", err
	}
	for _, p := range plans {
		if p.ID == planID {
			if p.StripePriceID == "" {
				return "", fmt.Errorf("plan %q has no stripe_price_id configured", planID)
			}
			return p.StripePriceID, nil
		}
	}
	return "", fmt.Errorf("plan %q not found", planID)
}

func getCatalog[T any](ctx context.Context, url string) ([]T, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("catalog request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("catalog returned %d", resp.StatusCode)
	}
	var out []T
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// dispatchOrderPlaced publishes the order.placed event. Best-effort: the
// caller doesn't depend on delivery, since the marketplace frontend also
// triggers /provisioning/start directly for paid-by-credit flows.
//
// Enriches the payload with the tenant's subdomain (looked up from the
// tenant service) because store.Order doesn't carry it and provisioning's
// orderPlacedData has a `subdomain` field. Without this enrichment the
// field arrives empty and the manifest generator produces paths like
// `clusters/.../tenants//namespace.yaml` that GitHub rejects with HTTP 422
// "tree.path contains a malformed path component". Issue #105.
func (h *Handler) dispatchOrderPlaced(tenantID string, order *store.Order) {
	if h.Producer == nil {
		return
	}
	subdomain := h.lookupTenantSubdomain(tenantID)
	payload := map[string]any{
		"id":               order.ID,
		"customer_id":      order.CustomerID,
		"tenant_id":        order.TenantID,
		"plan_id":          order.PlanID,
		"apps":             order.Apps,
		"addons":           order.Addons,
		"amount_omr":       order.AmountOMR,
		"amount_baisa":     order.AmountBaisa,
		"status":           order.Status,
		"subdomain":        subdomain,
	}
	evt, err := events.NewEvent("order.placed", "billing", tenantID, payload)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := h.Producer.Publish(ctx, "sme.order.events", evt); err != nil {
		slog.Warn("dispatch order.placed", "error", err)
	}
}

// lookupTenantSubdomain fetches the tenant's subdomain from the tenant
// service. Returns "" if the call fails — the provisioning consumer's
// validTenantSlug guard will then refuse the event rather than producing a
// malformed git path. Short timeout so we don't block checkout response.
func (h *Handler) lookupTenantSubdomain(tenantID string) string {
	if h.TenantURL == "" || tenantID == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		h.TenantURL+"/tenant/internal/tenants/"+tenantID+"/subdomain", nil)
	if err != nil {
		return ""
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("lookupTenantSubdomain: tenant fetch", "tenant_id", tenantID, "error", err)
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		slog.Warn("lookupTenantSubdomain: non-200", "tenant_id", tenantID, "status", resp.StatusCode)
		return ""
	}
	var t struct {
		Subdomain string `json:"subdomain"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return ""
	}
	return t.Subdomain
}
