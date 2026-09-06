package api

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/anomaly"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Anomalies (#6867, DESIGN.md §3.6).
//
// Daily cost per (customer, resource kind) is judged against its trailing
// 14 days by internal/anomaly; each flagged day is explained by the SKUs and
// resources whose cost moved most (store.DayDrivers). The window asked for
// is [from, to) in whole UTC days; the 14 days before `from` are fetched
// too, so the first day of the window has a baseline.

const (
	anomalyBaselineDays  = anomaly.DefaultLookback
	anomalyDefaultDays   = 30
	anomalySummaryDays   = 7
	anomalySummaryTop    = 5
	maxAnomalyRows       = 100
	maxAnomalyWindowDays = 400
)

// anomalyRow is the wire shape (ui/src/api/types.ts Anomaly). Actual is the
// ledger's exact number for the day; expected, impact and score are
// statistics and float64.
type anomalyRow struct {
	Day          string         `json:"day"`
	CustomerID   string         `json:"customer_id"`
	CustomerName string         `json:"customer_name"`
	Dimension    string         `json:"dimension"`
	Key          string         `json:"key"`
	Label        string         `json:"label"`
	Expected     float64        `json:"expected"`
	Actual       store.Decimal  `json:"actual"`
	Impact       float64        `json:"impact"`
	Score        float64        `json:"score"`
	Drivers      []store.Driver `json:"drivers"`
}

// parseDayWindow reads from/to as YYYY-MM-DD; default = the last
// defaultDays days including today. The second return is the 400 message.
func (h *Handler) parseDayWindow(r *http.Request, defaultDays int) (time.Time, time.Time, string) {
	today := dateOnly(h.Now())
	from, to := today.AddDate(0, 0, -(defaultDays-1)), today.AddDate(0, 0, 1)
	qs := r.URL.Query()
	if v := qs.Get("from"); v != "" {
		t, err := time.Parse("2006-01-02", strings.TrimSpace(v))
		if err != nil {
			return from, to, "from must be YYYY-MM-DD"
		}
		from = t.UTC()
	}
	if v := qs.Get("to"); v != "" {
		t, err := time.Parse("2006-01-02", strings.TrimSpace(v))
		if err != nil {
			return from, to, "to must be YYYY-MM-DD"
		}
		to = t.UTC()
	}
	if !to.After(from) {
		return from, to, "from must be before to"
	}
	if days := int(to.Sub(from).Hours() / 24); days > maxAnomalyWindowDays {
		return from, to, fmt.Sprintf("window too long: %d days, maximum %d", days, maxAnomalyWindowDays)
	}
	return from, to, ""
}

// detectAnomalies flags the days in [from, to) per (customer, kind) inside
// the scope, sorted by day desc then impact desc, with drivers attached to
// the first maxAnomalyRows.
func (h *Handler) detectAnomalies(ctx context.Context, scope store.Scope, customerID string, from, to time.Time) ([]anomalyRow, error) {
	daily, err := h.Store.DailyCostByCustomerKind(ctx, scope, customerID, from.AddDate(0, 0, -anomalyBaselineDays), to)
	if err != nil {
		return nil, err
	}
	type pair struct{ customer, kind string }
	series := map[pair][]anomaly.DayValue{}
	actual := map[pair]map[string]store.Decimal{}
	names := map[string]string{}
	var order []pair
	for _, d := range daily {
		p := pair{d.CustomerID, d.ResourceKind}
		if _, ok := series[p]; !ok {
			order = append(order, p)
			actual[p] = map[string]store.Decimal{}
		}
		series[p] = append(series[p], anomaly.DayValue{Day: d.Day, Value: decFloat(d.Cost)})
		actual[p][d.Day] = d.Cost
		names[d.CustomerID] = d.CustomerName
	}
	fromDay, toDay := from.Format("2006-01-02"), to.Format("2006-01-02")
	rows := []anomalyRow{}
	for _, p := range order {
		for _, f := range anomaly.Detect(series[p], anomaly.Options{}) {
			if f.Day < fromDay || f.Day >= toDay {
				continue
			}
			rows = append(rows, anomalyRow{
				Day: f.Day, CustomerID: p.customer, CustomerName: names[p.customer],
				Dimension: "kind", Key: p.kind, Label: store.KindLabel(p.kind),
				Expected: round6(f.Expected), Actual: actual[p][f.Day], Impact: round6(f.Impact), Score: math.Round(f.Score*100) / 100,
				Drivers: []store.Driver{},
			})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Day != rows[j].Day {
			return rows[i].Day > rows[j].Day
		}
		if rows[i].Impact != rows[j].Impact {
			return rows[i].Impact > rows[j].Impact
		}
		if rows[i].CustomerID != rows[j].CustomerID {
			return rows[i].CustomerID < rows[j].CustomerID
		}
		return rows[i].Key < rows[j].Key
	})
	if len(rows) > maxAnomalyRows {
		rows = rows[:maxAnomalyRows]
	}
	for i := range rows {
		drivers, err := h.Store.DayDrivers(ctx, scope, rows[i].CustomerID, rows[i].Key, rows[i].Day)
		if err != nil {
			return nil, err
		}
		rows[i].Drivers = drivers
	}
	return rows, nil
}

// summaryAnomalies is the overview block: the last 7 days, top 5 by impact.
func (h *Handler) summaryAnomalies(ctx context.Context, scope store.Scope, customerID string) ([]anomalyRow, error) {
	today := dateOnly(h.Now())
	rows, err := h.detectAnomalies(ctx, scope, customerID, today.AddDate(0, 0, -(anomalySummaryDays-1)), today.AddDate(0, 0, 1))
	if err != nil {
		return nil, err
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Impact != rows[j].Impact {
			return rows[i].Impact > rows[j].Impact
		}
		return rows[i].Day > rows[j].Day
	})
	if len(rows) > anomalySummaryTop {
		rows = rows[:anomalySummaryTop]
	}
	return rows, nil
}

func round6(f float64) float64 { return math.Round(f*1e6) / 1e6 }

func (h *Handler) anomalies(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireOperator(w, r)
	if !ok {
		return
	}
	h.writeAnomalies(w, r, s.Scope(), "")
}

func (h *Handler) customerAnomalies(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := h.requireCustomer(w, r, id, false)
	if !ok {
		return
	}
	h.writeAnomalies(w, r, s.Scope(), id)
}

func (h *Handler) writeAnomalies(w http.ResponseWriter, r *http.Request, scope store.Scope, customerID string) {
	from, to, msg := h.parseDayWindow(r, anomalyDefaultDays)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	rows, err := h.detectAnomalies(r.Context(), scope, customerID, from, to)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from.Format("2006-01-02"), "to": to.Format("2006-01-02"), "rows": rows})
}
