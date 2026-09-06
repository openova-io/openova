package api

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Resources (#6867, DESIGN.md §2.3 / §3.4): inventory joined with cost.
//
// GET /api/v1/resources                                 operator; `customer` filter allowed
// GET /api/v1/resources.csv                             same filters, every row
// GET /api/v1/customers/{id}/resources                  that customer or the operator
// GET /api/v1/customers/{id}/resources.csv
// GET /api/v1/resources/{source_id}/{resource_id...}    scope-checked through the source's customer
//
// Windows are half-open [from, to) in whole UTC days; default the last 30
// days. Pages default to 50 rows, at most 500.

func parseDayParam(s string) (time.Time, bool) {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(s))
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// parseResourceQuery reads the list parameters. The second return is the
// 400 message when the request is malformed.
func (h *Handler) parseResourceQuery(r *http.Request) (store.ResourceQuery, string) {
	qs := r.URL.Query()
	today := dateOnly(h.Now())
	q := store.ResourceQuery{
		From: today.AddDate(0, 0, -29), To: today.AddDate(0, 0, 1),
		Status: "all", Sort: "cost", Order: "desc", Limit: store.DefaultResourceLimit,
	}
	if v := qs.Get("from"); v != "" {
		t, ok := parseDayParam(v)
		if !ok {
			return q, "from must be YYYY-MM-DD"
		}
		q.From = t
	}
	if v := qs.Get("to"); v != "" {
		t, ok := parseDayParam(v)
		if !ok {
			return q, "to must be YYYY-MM-DD"
		}
		q.To = t
	}
	if !q.To.After(q.From) {
		return q, "from must be before to"
	}
	if n := len(store.Buckets(q.From, q.To, "day")); n > maxExploreBuckets {
		return q, fmt.Sprintf("window too long: %d days, maximum %d", n, maxExploreBuckets)
	}
	q.CustomerID = strings.TrimSpace(qs.Get("customer"))
	q.Kind = strings.TrimSpace(qs.Get("kind"))
	q.Region = strings.TrimSpace(qs.Get("region"))
	q.Q = strings.TrimSpace(qs.Get("q"))
	if v := qs.Get("status"); v != "" {
		if !contains(store.ResourceStatuses(), v) {
			return q, "status must be one of " + strings.Join(store.ResourceStatuses(), ", ")
		}
		q.Status = v
	}
	if v := qs.Get("sort"); v != "" {
		if !contains(store.ResourceSorts(), v) {
			return q, "sort must be one of " + strings.Join(store.ResourceSorts(), ", ")
		}
		q.Sort = v
	}
	switch v := strings.ToLower(qs.Get("order")); v {
	case "":
	case "asc", "desc":
		q.Order = v
	default:
		return q, "order must be asc or desc"
	}
	if v := qs.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > store.MaxResourceLimit {
			return q, fmt.Sprintf("limit must be 1..%d", store.MaxResourceLimit)
		}
		q.Limit = n
	}
	if v := qs.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return q, "offset must be >= 0"
		}
		q.Offset = n
	}
	return q, ""
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func (h *Handler) listResources(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireOperator(w, r)
	if !ok {
		return
	}
	q, msg := h.parseResourceQuery(r)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	res, err := h.Store.ListResources(r.Context(), s.Scope(), q)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (h *Handler) customerResources(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := h.requireCustomer(w, r, id, false)
	if !ok {
		return
	}
	q, msg := h.parseResourceQuery(r)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	q.CustomerID = id
	res, err := h.Store.ListResources(r.Context(), s.Scope(), q)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (h *Handler) getResource(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	sourceID, resourceID := r.PathValue("source_id"), r.PathValue("resource_id")
	if sourceID == "" || resourceID == "" {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	q, msg := h.parseResourceQuery(r)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	d, err := h.Store.GetResource(r.Context(), s.Scope(), sourceID, resourceID, q.From, q.To)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// writeResourcesCSV streams every row of the filtered set, paging through
// the store at the maximum page size so the export is not capped at one
// page.
func (h *Handler) writeResourcesCSV(w http.ResponseWriter, r *http.Request, scope store.Scope, q store.ResourceQuery) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="resources-%s-%s.csv"`, q.From.Format("2006-01-02"), q.To.Format("2006-01-02")))
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"customer", "kind", "resource_id", "name", "region", "status", "first_seen", "last_seen", "cost", "currency"})
	q.Limit, q.Offset = store.MaxResourceLimit, 0
	for {
		res, err := h.Store.ListResources(r.Context(), scope, q)
		if err != nil {
			// Headers are out; the truncated file is the only honest signal.
			cw.Flush()
			return
		}
		for _, row := range res.Rows {
			_ = cw.Write([]string{row.CustomerName, row.Kind, row.ResourceID, row.Name, row.Region, row.Status,
				row.FirstSeen.Format(time.RFC3339), row.LastSeen.Format(time.RFC3339), string(row.Cost), row.Currency})
		}
		q.Offset += len(res.Rows)
		if len(res.Rows) == 0 || q.Offset >= res.Total {
			break
		}
	}
	cw.Flush()
}

func (h *Handler) resourcesCSV(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireOperator(w, r)
	if !ok {
		return
	}
	q, msg := h.parseResourceQuery(r)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	h.writeResourcesCSV(w, r, s.Scope(), q)
}

func (h *Handler) customerResourcesCSV(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := h.requireCustomer(w, r, id, false)
	if !ok {
		return
	}
	q, msg := h.parseResourceQuery(r)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	q.CustomerID = id
	h.writeResourcesCSV(w, r, s.Scope(), q)
}
