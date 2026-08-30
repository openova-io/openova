package api

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/rating"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

var periodShape = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)

// runStatements rates a period for every customer (or one) into drafts.
func (h *Handler) runStatements(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	var in struct {
		Period     string `json:"period"`
		CustomerID string `json:"customer_id"`
	}
	if err := decode(r, &in); err != nil || !periodShape.MatchString(in.Period) {
		writeErr(w, http.StatusBadRequest, "period must be YYYY-MM")
		return
	}
	results, err := rating.Run(r.Context(), h.Store, in.Period, in.CustomerID)
	if err != nil {
		storeErr(w, err)
		return
	}
	var cid *string
	if in.CustomerID != "" {
		cid = &in.CustomerID
	}
	h.audit(r, cid, "statements.run", map[string]any{"period": in.Period, "customers": len(results)})
	writeJSON(w, http.StatusOK, map[string]any{"period": in.Period, "results": results})
}

func (h *Handler) listAllStatements(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	if s.Role != store.RoleOperator {
		// Customer principals get their own list here too.
		list, err := h.Store.ListStatements(r.Context(), s.Scope(), *s.CustomerID)
		if err != nil {
			storeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"statements": list})
		return
	}
	period := r.URL.Query().Get("period")
	if period != "" && !periodShape.MatchString(period) {
		writeErr(w, http.StatusBadRequest, "period must be YYYY-MM")
		return
	}
	list, err := h.Store.ListAllStatements(r.Context(), period)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"statements": list})
}

func (h *Handler) listCustomerStatements(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := h.requireCustomer(w, r, id, false)
	if !ok {
		return
	}
	list, err := h.Store.ListStatements(r.Context(), s.Scope(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"statements": list})
}

// getStatement serves JSON, or CSV when the id carries a .csv suffix.
func (h *Handler) getStatement(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	asCSV := strings.HasSuffix(id, ".csv")
	id = strings.TrimSuffix(id, ".csv")
	st, err := h.Store.GetStatement(r.Context(), s.Scope(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	if !asCSV {
		writeJSON(w, http.StatusOK, st)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="statement-%s-%s.csv"`, st.CustomerID[:8], st.PeriodStart[:7]))
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"statement_id", "customer", "period_start", "period_end", "currency", "status", "source_id", "sku", "unit", "quantity", "unit_price", "amount", "resource_count"})
	for _, l := range st.Lines {
		src := ""
		if l.SourceID != nil {
			src = *l.SourceID
		}
		_ = cw.Write([]string{st.ID, st.CustomerName, st.PeriodStart, st.PeriodEnd, st.Currency, st.Status, src, l.SKU, l.Unit, string(l.Quantity), string(l.UnitPrice), string(l.Amount), fmt.Sprint(l.ResourceCount)})
	}
	_ = cw.Write([]string{st.ID, st.CustomerName, st.PeriodStart, st.PeriodEnd, st.Currency, st.Status, "", "subtotal", "", "", "", string(st.Subtotal), ""})
	_ = cw.Write([]string{st.ID, st.CustomerName, st.PeriodStart, st.PeriodEnd, st.Currency, st.Status, "", "tax", "", string(st.TaxRate), "", string(st.Tax), ""})
	_ = cw.Write([]string{st.ID, st.CustomerName, st.PeriodStart, st.PeriodEnd, st.Currency, st.Status, "", "total", "", "", "", string(st.Total), ""})
	cw.Flush()
}

func (h *Handler) issueStatement(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	st, err := h.Store.IssueStatement(r.Context(), r.PathValue("id"))
	if err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, &st.CustomerID, "statement.issue", map[string]any{"statement_id": st.ID, "period": st.PeriodStart[:7], "total": st.Total})
	writeJSON(w, http.StatusOK, st)
}
