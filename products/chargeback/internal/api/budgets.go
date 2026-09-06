package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/budget"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Budgets (#6867, DESIGN.md §3.5).
//
// Reads follow the session scope: the operator sees every budget, a customer
// principal only the budgets naming its customer (a global budget is the
// operator's instrument and is never listed to a customer). Writes are
// operator-only: a budget's recipients and cap are commercial settings.

var (
	defaultThresholds = []int{50, 80, 100}
	currencyShape     = regexp.MustCompile(`^[A-Za-z]{3}$`)
)

const (
	minThreshold = 1
	maxThreshold = 1000
)

// budgetBody is the request shape for POST and PUT. Every field is optional
// on the wire so PUT can be a partial replacement over the stored row;
// validateBudget enforces what must be present after the merge. CustomerID
// is raw so PUT can tell "absent" (keep) from null (make global).
type budgetBody struct {
	Name         *string         `json:"name"`
	CustomerID   json.RawMessage `json:"customer_id"`
	Amount       *store.Decimal  `json:"amount"`
	Currency     *string         `json:"currency"`
	Period       *string         `json:"period"`
	Thresholds   *[]int          `json:"thresholds"`
	NotifyEmails *[]string       `json:"notify_emails"`
	Active       *bool           `json:"active"`
}

// merge overlays the body on base (the stored row for PUT, the defaults for
// POST). The bool reports whether customer_id was given at all.
func (in budgetBody) merge(base store.BudgetInput) (store.BudgetInput, error) {
	out := base
	if in.Name != nil {
		out.Name = *in.Name
	}
	if len(in.CustomerID) > 0 {
		if string(in.CustomerID) == "null" {
			out.CustomerID = nil
		} else {
			var s string
			if err := json.Unmarshal(in.CustomerID, &s); err != nil {
				return out, errors.New("customer_id must be a string or null")
			}
			s = strings.TrimSpace(s)
			if s == "" {
				out.CustomerID = nil
			} else {
				out.CustomerID = &s
			}
		}
	}
	if in.Amount != nil {
		out.Amount = *in.Amount
	}
	if in.Currency != nil {
		out.Currency = *in.Currency
	}
	if in.Period != nil {
		out.Period = *in.Period
	}
	if in.Thresholds != nil {
		out.Thresholds = *in.Thresholds
	}
	if in.NotifyEmails != nil {
		out.NotifyEmails = *in.NotifyEmails
	}
	if in.Active != nil {
		out.Active = *in.Active
	}
	return out, nil
}

// validateBudget normalizes and checks a merged input. The second return is
// the 400 message; the error is a store error (404 for an unknown customer).
func (h *Handler) validateBudget(ctx context.Context, in store.BudgetInput) (store.BudgetInput, string, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return in, "name is required", nil
	}
	amount := strings.TrimSpace(string(in.Amount))
	if amount == "" {
		return in, "amount is required", nil
	}
	r, ok := new(big.Rat).SetString(amount)
	if !ok || strings.ContainsAny(amount, "eE/") {
		return in, "amount must be a decimal number", nil
	}
	if r.Sign() < 0 {
		return in, "amount must be 0 or more", nil
	}
	in.Amount = store.Decimal(amount)

	in.Currency = strings.ToUpper(strings.TrimSpace(in.Currency))
	if in.Currency == "" {
		in.Currency = "OMR"
	}
	if !currencyShape.MatchString(in.Currency) {
		return in, "currency must be a 3-letter code", nil
	}

	in.Period = strings.ToLower(strings.TrimSpace(in.Period))
	if in.Period == "" {
		in.Period = "monthly"
	}
	if in.Period != "monthly" {
		return in, "period must be monthly", nil
	}

	if len(in.Thresholds) == 0 {
		in.Thresholds = append([]int(nil), defaultThresholds...)
	}
	for _, t := range in.Thresholds {
		if t < minThreshold || t > maxThreshold {
			return in, "thresholds must be whole percentages between 1 and 1000", nil
		}
	}
	in.Thresholds = budget.NormalizeThresholds(in.Thresholds)

	emails := make([]string, 0, len(in.NotifyEmails))
	seen := map[string]bool{}
	for _, e := range in.NotifyEmails {
		e = normEmail(e)
		if e == "" {
			continue
		}
		if !validEmail(e) {
			return in, "notify_emails must be valid email addresses", nil
		}
		if seen[e] {
			continue
		}
		seen[e] = true
		emails = append(emails, e)
	}
	in.NotifyEmails = emails

	if in.CustomerID != nil {
		if _, err := h.Store.GetCustomer(ctx, store.OperatorScope, *in.CustomerID); err != nil {
			return in, "", err
		}
	}
	return in, "", nil
}

func budgetAudit(b store.Budget) map[string]any {
	return map[string]any{
		"budget_id": b.ID, "name": b.Name, "customer_id": b.CustomerID, "amount": string(b.Amount),
		"currency": b.Currency, "period": b.Period, "thresholds": b.Thresholds,
		"notify_emails": b.NotifyEmails, "active": b.Active,
	}
}

// listBudgets — GET /api/v1/budgets: every budget for the operator, the
// customer's own for a customer principal.
func (h *Handler) listBudgets(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	bs, err := h.Store.ListBudgets(r.Context(), s.Scope())
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"budgets": bs})
}

// customerBudgets — GET /api/v1/customers/{id}/budgets: the budgets naming
// one customer. requireCustomer already answers 404 for another customer's
// id, so the narrowed scope is right for the operator and the customer alike.
func (h *Handler) customerBudgets(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.requireCustomer(w, r, id, false); !ok {
		return
	}
	bs, err := h.Store.ListBudgets(r.Context(), store.CustomerScope(id))
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"budgets": bs})
}

// createBudget — POST /api/v1/budgets (operator).
func (h *Handler) createBudget(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	var body budgetBody
	if err := decode(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	in, err := body.merge(store.BudgetInput{Currency: "OMR", Period: "monthly", Active: true})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	in, msg, err := h.validateBudget(r.Context(), in)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if err != nil {
		storeErr(w, err)
		return
	}
	b, err := h.Store.CreateBudget(r.Context(), in)
	if err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, b.CustomerID, "budget.create", budgetAudit(b))
	writeJSON(w, http.StatusCreated, b)
}

// getBudget — GET /api/v1/budgets/{id}.
func (h *Handler) getBudget(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	b, err := h.Store.GetBudget(r.Context(), s.Scope(), r.PathValue("id"))
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

// updateBudget — PUT /api/v1/budgets/{id} (operator). Fields absent from the
// body keep their stored value; customer_id: null makes the budget global.
func (h *Handler) updateBudget(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	id := r.PathValue("id")
	cur, err := h.Store.GetBudget(r.Context(), store.OperatorScope, id)
	if err != nil {
		storeErr(w, err)
		return
	}
	var body budgetBody
	if err := decode(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	in, err := body.merge(store.BudgetInput{
		Name: cur.Name, CustomerID: cur.CustomerID, Amount: cur.Amount, Currency: cur.Currency,
		Period: cur.Period, Thresholds: cur.Thresholds, NotifyEmails: cur.NotifyEmails, Active: cur.Active,
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	in, msg, err := h.validateBudget(r.Context(), in)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if err != nil {
		storeErr(w, err)
		return
	}
	b, err := h.Store.UpdateBudget(r.Context(), id, in)
	if err != nil {
		storeErr(w, err)
		return
	}
	details := budgetAudit(b)
	details["previous_customer_id"] = cur.CustomerID
	details["previous_amount"] = string(cur.Amount)
	h.audit(r, b.CustomerID, "budget.update", details)
	writeJSON(w, http.StatusOK, b)
}

// deleteBudget — DELETE /api/v1/budgets/{id} (operator).
func (h *Handler) deleteBudget(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	id := r.PathValue("id")
	cur, err := h.Store.GetBudget(r.Context(), store.OperatorScope, id)
	if err != nil {
		storeErr(w, err)
		return
	}
	if err := h.Store.DeleteBudget(r.Context(), id); err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, cur.CustomerID, "budget.delete", budgetAudit(cur))
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
}

// budgetStatus — GET /api/v1/budgets/{id}/status?period=YYYY-MM (default the
// current month). The forecast is only attached for the current month.
func (h *Handler) budgetStatus(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	b, err := h.Store.GetBudget(r.Context(), s.Scope(), r.PathValue("id"))
	if err != nil {
		storeErr(w, err)
		return
	}
	now := h.Now().UTC()
	month := budget.MonthStart(now)
	if v := r.URL.Query().Get("period"); v != "" {
		if month, err = budget.ParsePeriod(v); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	st, err := budget.StatusFor(r.Context(), h.Store, s.Scope(), b, month, now)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// budgetStatuses evaluates, for the current month, every active budget the
// list scope may see. exploreScope is the session's scope (it bounds the
// spend a status may reveal); listScope may be narrower, e.g. one customer
// on the operator's customer-lens summary. Never nil: the summary renders [].
func (h *Handler) budgetStatuses(ctx context.Context, listScope, exploreScope store.Scope, now time.Time) ([]budget.Status, error) {
	bs, err := h.Store.ListBudgets(ctx, listScope)
	if err != nil {
		return nil, err
	}
	out := []budget.Status{}
	for _, b := range bs {
		if !b.Active {
			continue
		}
		st, err := budget.StatusFor(ctx, h.Store, exploreScope, b, now, now)
		if err != nil {
			slog.Warn("budget status", "budget_id", b.ID, "error", err)
			continue
		}
		out = append(out, st)
	}
	return out, nil
}
