package api

import (
	"encoding/csv"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/access"
	"github.com/openova-io/openova/products/chargeback/internal/rating"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Cost centres (DESIGN.md §19) — a customer's own labelling of its spend.
//
//	GET    /api/v1/customers/{id}/cost-centres                       metering.read (customer)
//	POST   /api/v1/customers/{id}/cost-centres                       customers.manage
//	PUT    /api/v1/cost-centres/{id}                                 customers.manage
//	DELETE /api/v1/cost-centres/{id}                                 customers.manage
//	GET    /api/v1/customers/{id}/cost-centres/rules                 metering.read (customer)
//	PUT    /api/v1/customers/{id}/cost-centres/rules                 customers.manage
//	DELETE /api/v1/cost-centres/rules/{id}                           customers.manage
//	GET    /api/v1/customers/{id}/cost-centres/resources             metering.read (customer)
//	PUT    /api/v1/customers/{id}/cost-centres/resources/{rid...}    customers.manage
//	DELETE /api/v1/customers/{id}/cost-centres/resources/{rid...}    customers.manage
//	GET    /api/v1/customers/{id}/cost-centres/report                metering.read (customer)
//	GET    /api/v1/customers/{id}/cost-centres/report.csv            metering.read (customer)
//
// READING is metering.read on the customer, so a customer OWNER reads its
// own cost centres, its rules and its breakdown — which is the whole point
// of showback. WRITING is customers.manage, exactly where the budgets and
// report schedules of a customer already sit (DESIGN.md §10.2): a cost
// centre decides how a bill is read by the people who pay it, and that is
// the operator's configuration of the account, not a self-service setting.
// No new permission is introduced; §10.2 lists ten, §13 added two, and this
// needs none of its own.
//
// NOTHING here writes money. A cost centre cannot change a statement — the
// breakdown is frozen on the statement by the rating run, and the run is
// already refused on a CLOSED period (DESIGN.md §18.4). So no route here can
// make a closed period editable, and the integration test walks exactly that:
// rules edited after a close leave the issued invoice's breakdown as it was.

func (h *Handler) listCostCentres(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := h.requirePermission(w, r, access.MeteringRead, id)
	if !ok {
		return
	}
	centres, err := h.Store.ListCostCentres(r.Context(), s.Scope(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"cost_centres": centres,
		"unassigned":   map[string]any{"code": store.CostCentreUnassigned, "name": store.CostCentreUnassignedName},
		"code_rule":    store.CostCentreCodeRule,
	})
}

func (h *Handler) createCostCentre(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.requirePermission(w, r, access.CustomersManage, id); !ok {
		return
	}
	var in store.CostCentreInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	c, err := h.Store.CreateCostCentre(r.Context(), id, in)
	if !h.costCentreErr(w, err) {
		return
	}
	h.audit(r, &id, "costcentre.create", map[string]any{"id": c.ID, "code": c.Code, "name": c.Name, "active": c.Active})
	writeJSON(w, http.StatusCreated, c)
}

func (h *Handler) updateCostCentre(w http.ResponseWriter, r *http.Request) {
	existing, ok := h.costCentreForWrite(w, r)
	if !ok {
		return
	}
	var in store.CostCentreInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	c, err := h.Store.UpdateCostCentre(r.Context(), existing.ID, in)
	if !h.costCentreErr(w, err) {
		return
	}
	h.audit(r, &c.CustomerID, "costcentre.update", map[string]any{"id": c.ID, "code": c.Code, "name": c.Name, "active": c.Active})
	writeJSON(w, http.StatusOK, c)
}

func (h *Handler) deleteCostCentre(w http.ResponseWriter, r *http.Request) {
	existing, ok := h.costCentreForWrite(w, r)
	if !ok {
		return
	}
	if !h.costCentreErr(w, h.Store.DeleteCostCentre(r.Context(), existing.ID)) {
		return
	}
	// The rules and overrides go with it and that usage reads as unassigned
	// from now on. Invoices already issued keep the breakdown they were
	// issued with: it stores codes, not a foreign key.
	h.audit(r, &existing.CustomerID, "costcentre.delete", map[string]any{"id": existing.ID, "code": existing.Code, "rules": existing.Rules, "resources": existing.Resources})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listCostCentreRules(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := h.requirePermission(w, r, access.MeteringRead, id)
	if !ok {
		return
	}
	rules, err := h.Store.ListCostCentreRules(r.Context(), s.Scope(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": rules, "tag_key_rule": store.TagKeyRule, "default_priority": store.DefaultCostCentreRulePriority})
}

func (h *Handler) putCostCentreRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.requirePermission(w, r, access.CustomersManage, id); !ok {
		return
	}
	var in store.CostCentreRuleInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	rule, err := h.Store.PutCostCentreRule(r.Context(), id, in)
	if !h.costCentreErr(w, err) {
		return
	}
	h.audit(r, &id, "costcentre.rule.put", map[string]any{"id": rule.ID, "code": rule.Code, "tag_key": rule.TagKey, "tag_value": rule.TagValue, "priority": rule.Priority})
	writeJSON(w, http.StatusOK, rule)
}

func (h *Handler) deleteCostCentreRule(w http.ResponseWriter, r *http.Request) {
	rule, err := h.Store.GetCostCentreRule(r.Context(), store.OperatorScope, r.PathValue("id"))
	if err != nil {
		storeErr(w, err)
		return
	}
	if _, ok := h.requirePermission(w, r, access.CustomersManage, rule.CustomerID); !ok {
		return
	}
	if !h.costCentreErr(w, h.Store.DeleteCostCentreRule(r.Context(), rule.ID)) {
		return
	}
	h.audit(r, &rule.CustomerID, "costcentre.rule.delete", map[string]any{"id": rule.ID, "code": rule.Code, "tag_key": rule.TagKey, "tag_value": rule.TagValue})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listCostCentreResources(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := h.requirePermission(w, r, access.MeteringRead, id)
	if !ok {
		return
	}
	rows, err := h.Store.ListCostCentreResources(r.Context(), s.Scope(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"resources": rows})
}

// costCentreResourceID is the {rid...} path value. A resource id may contain
// SLASHES (a Kubernetes object is namespace/name), which is why the pattern
// is a multi-segment wildcard and why the value is read exactly as the
// existing per-resource route reads its own — the mux has already unescaped
// it, and unescaping twice would corrupt an id that legitimately carries a
// percent sign.
func costCentreResourceID(r *http.Request) string { return r.PathValue("rid") }

func (h *Handler) putResourceCostCentre(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := h.requirePermission(w, r, access.CustomersManage, id)
	if !ok {
		return
	}
	var in store.CostCentreRef
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	row, err := h.Store.SetResourceCostCentre(r.Context(), id, costCentreResourceID(r), in, s.Email)
	if !h.costCentreErr(w, err) {
		return
	}
	h.audit(r, &id, "costcentre.resource.set", map[string]any{"resource_id": row.ResourceID, "code": row.Code})
	writeJSON(w, http.StatusOK, row)
}

func (h *Handler) deleteResourceCostCentre(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.requirePermission(w, r, access.CustomersManage, id); !ok {
		return
	}
	rid := costCentreResourceID(r)
	if !h.costCentreErr(w, h.Store.ClearResourceCostCentre(r.Context(), id, rid)) {
		return
	}
	h.audit(r, &id, "costcentre.resource.clear", map[string]any{"resource_id": rid})
	w.WriteHeader(http.StatusNoContent)
}

// costCentreForWrite loads the centre an id-addressed write names and checks
// the caller may manage ITS customer. A centre outside the caller's bindings
// reads as 404, so ids of other customers are not confirmed.
func (h *Handler) costCentreForWrite(w http.ResponseWriter, r *http.Request) (store.CostCentre, bool) {
	c, err := h.Store.GetCostCentre(r.Context(), store.OperatorScope, r.PathValue("id"))
	if err != nil {
		storeErr(w, err)
		return store.CostCentre{}, false
	}
	if _, ok := h.requirePermission(w, r, access.CustomersManage, c.CustomerID); !ok {
		return store.CostCentre{}, false
	}
	return c, true
}

// costCentreErr writes the response for a store error and reports whether
// the handler may continue — the same mapping the tax routes use.
func (h *Handler) costCentreErr(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, store.ErrInvalid):
		writeErr(w, http.StatusBadRequest, invalidMessage(err))
	case errors.Is(err, store.ErrConflict):
		writeErr(w, http.StatusConflict, invalidMessage(err))
	default:
		storeErr(w, err)
	}
	return false
}

// ---------------------------------------------------------------------------
// the report
// ---------------------------------------------------------------------------

// costCentreReport is what a period's cost centres look like. Source names
// where the figures came from and is the whole reason the document is
// trustworthy: `statement` means these are the invoice's OWN figures,
// frozen when it was rated and summing to it exactly; `usage` means the
// period has not been rated yet and the rows are this month's usage so far,
// which no invoice has confirmed.
type costCentreReport struct {
	CustomerID  string                 `json:"customer_id"`
	Period      string                 `json:"period"`
	Currency    string                 `json:"currency"`
	Source      string                 `json:"source"`
	StatementID string                 `json:"statement_id,omitempty"`
	Status      string                 `json:"status,omitempty"`
	Lines       []store.CostCentreLine `json:"lines"`
	Totals      costCentreTotals       `json:"totals"`
	// Invoice is the statement's own subtotal, tax and total, so a reader
	// can check the rows against the bill without a second request.
	Invoice *costCentreInvoice `json:"invoice,omitempty"`
}

type costCentreTotals struct {
	Usage    store.Decimal `json:"usage"`
	List     store.Decimal `json:"list"`
	Discount store.Decimal `json:"discount"`
	Net      store.Decimal `json:"net"`
	Tax      store.Decimal `json:"tax"`
	Total    store.Decimal `json:"total"`
}

type costCentreInvoice struct {
	Subtotal store.Decimal `json:"subtotal"`
	Discount store.Decimal `json:"discount_total"`
	Tax      store.Decimal `json:"tax"`
	Total    store.Decimal `json:"total"`
	Agrees   bool          `json:"agrees"`
}

const (
	costCentreSourceStatement = "statement"
	costCentreSourceUsage     = "usage"
)

// sameDecimal compares two money strings by VALUE. A decimal that is not a
// number at all is not equal to anything, which is the safe answer for a
// check whose false means "tell the reader these disagree".
func sameDecimal(a, b store.Decimal) bool {
	x, ok := new(big.Rat).SetString(string(a))
	if !ok {
		return false
	}
	y, ok := new(big.Rat).SetString(string(b))
	if !ok {
		return false
	}
	return x.Cmp(y) == 0
}

// buildCostCentreReport serves the FROZEN breakdown when the period has a
// statement, and the live usage weights when it has not. Two provenances,
// one document — and never a recomputation of an invoice that already
// exists, which is what keeps the report and the bill from disagreeing.
func (h *Handler) buildCostCentreReport(w http.ResponseWriter, r *http.Request, customerID, period string) (costCentreReport, bool) {
	s, ok := h.requirePermission(w, r, access.MeteringRead, customerID)
	if !ok {
		return costCentreReport{}, false
	}
	if !periodShape.MatchString(period) {
		writeErr(w, http.StatusBadRequest, "period must be YYYY-MM")
		return costCentreReport{}, false
	}
	from, to, err := store.PeriodBounds(period)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return costCentreReport{}, false
	}
	doc := costCentreReport{CustomerID: customerID, Period: period, Source: costCentreSourceUsage}
	statements, err := h.Store.ListStatements(r.Context(), s.Scope(), customerID)
	if err != nil {
		storeErr(w, err)
		return costCentreReport{}, false
	}
	for _, st := range statements {
		if !strings.HasPrefix(st.PeriodStart, period) || len(st.CostCentreLines) == 0 {
			continue
		}
		doc.Source, doc.StatementID, doc.Status = costCentreSourceStatement, st.ID, st.Status
		doc.Currency, doc.Lines = st.Currency, st.CostCentreLines
		doc.Invoice = &costCentreInvoice{Subtotal: st.Subtotal, Discount: st.DiscountTotal, Tax: st.Tax, Total: st.Total}
		break
	}
	if doc.Source == costCentreSourceUsage {
		weights, err := h.Store.CostCentreWeights(r.Context(), s.Scope(), customerID, from, to)
		if err != nil {
			storeErr(w, err)
			return costCentreReport{}, false
		}
		// Nothing is rated yet, so there is no net, discount or tax to
		// attribute: the rows carry the usage alone and every money column
		// is zero, which is honest rather than a guess at a bill.
		lines, err := rating.CostCentreBreakdown(weights, "0", "0", "0")
		if err != nil {
			storeErr(w, err)
			return costCentreReport{}, false
		}
		doc.Lines = lines
		if doc.Currency == "" {
			doc.Currency, err = h.Store.ReportingCurrency(r.Context())
			if err != nil {
				storeErr(w, err)
				return costCentreReport{}, false
			}
		}
	}
	usage, list, discount, net, tax, total, err := rating.CostCentreTotals(doc.Lines)
	if err != nil {
		storeErr(w, err)
		return costCentreReport{}, false
	}
	doc.Totals = costCentreTotals{Usage: usage, List: list, Discount: discount, Net: net, Tax: tax, Total: total}
	if doc.Invoice != nil {
		// The identity the apportionment guarantees, stated rather than
		// assumed: a reader is told when the rows and the invoice disagree.
		// Compared as NUMBERS — the two sides are rendered by different
		// code (Postgres numeric and the rounder), and "0" against
		// "0.000000" is agreement, not a defect to report.
		doc.Invoice.Agrees = sameDecimal(doc.Totals.Net, doc.Invoice.Subtotal) &&
			sameDecimal(doc.Totals.Tax, doc.Invoice.Tax) &&
			sameDecimal(doc.Totals.Total, doc.Invoice.Total)
	}
	if doc.Lines == nil {
		doc.Lines = []store.CostCentreLine{}
	}
	return doc, true
}

func (h *Handler) costCentreReport(w http.ResponseWriter, r *http.Request) {
	doc, ok := h.buildCostCentreReport(w, r, r.PathValue("id"), r.URL.Query().Get("period"))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func (h *Handler) costCentreReportCSV(w http.ResponseWriter, r *http.Request) {
	doc, ok := h.buildCostCentreReport(w, r, r.PathValue("id"), r.URL.Query().Get("period"))
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="cost-centres-%s.csv"`, doc.Period))
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"code", "name", "usage", "list", "discount", "net", "tax", "total", "currency", "source"})
	for _, l := range doc.Lines {
		_ = cw.Write([]string{l.Code, l.Name, string(l.Usage), string(l.List), string(l.Discount), string(l.Net), string(l.Tax), string(l.Total), doc.Currency, doc.Source})
	}
	// The total row is the point of the export: it is what proves the rows
	// add up to the invoice, in the file the finance team opens.
	t := doc.Totals
	_ = cw.Write([]string{"TOTAL", "", string(t.Usage), string(t.List), string(t.Discount), string(t.Net), string(t.Tax), string(t.Total), doc.Currency, doc.Source})
	cw.Flush()
}
