package api

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/openova-io/openova/products/chargeback/internal/access"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// overview is the operator landing payload. Since #6867 it is the cost
// summary document (DESIGN.md §3.2): the earlier three-block payload used
// keys the page never read, which is how hw307 rendered every KPI as zero.
func (h *Handler) overview(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireCrossCustomer(w, r, access.MeteringRead)
	if !ok {
		return
	}
	h.writeSummary(w, r, s.Scope(), "")
}

// enrichSummary is the seam where the budgets and anomalies lanes add their
// blocks to the summary (parts.Budgets / parts.Anomalies). Kept as a method
// so each lane extends it without touching the composition.
//
// Each lane has two halves around the request's one ledger read
// (store.CostBatch): here it queues its questions on the batch, and the
// finisher returned — called after the batch has run — reads the answers
// back. A lane that cannot ask (a scope that cannot see the budget's
// customer, a bad window) is logged and leaves its block empty rather than
// failing the whole overview; the KPIs above it are computed regardless.
//
// Anomalies: the last 7 days, top 5 by impact (DESIGN.md §3.2).
func (h *Handler) enrichSummary(ctx context.Context, batch *store.CostBatch, scope store.Scope, customerID string, parts *summaryParts) func(context.Context) {
	// Budgets (#6867 §3.5): the current-month status of every active budget
	// the scope may see. On the customer lens (the operator's
	// /customers/{id}/cost/summary, or a customer principal's own) only the
	// budgets naming that customer; the operator's overview lists them all,
	// global budgets included.
	listScope := scope
	if customerID != "" {
		listScope = store.CustomerScope(customerID)
	}
	finishBudgets, err := h.queueBudgetStatuses(ctx, batch, listScope, scope, parts.Now)
	if err != nil {
		slog.Warn("summary budgets", "error", err)
	}
	// Anomalies (#6867 §3.6): independent of the budgets block — one failing
	// must not empty the other.
	finishAnomalies, err := h.queueSummaryAnomalies(batch, scope, customerID)
	if err != nil {
		slog.Error("summary anomalies", "error", err)
	}
	return func(ctx context.Context) {
		if finishBudgets != nil {
			parts.Budgets = finishBudgets(ctx)
		}
		if finishAnomalies == nil {
			return
		}
		if rows, err := finishAnomalies(ctx); err != nil {
			slog.Error("summary anomalies", "error", err)
		} else {
			parts.Anomalies = rows
		}
	}
}
