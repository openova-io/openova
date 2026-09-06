package api

import (
	"log/slog"
	"net/http"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// overview is the operator landing payload. Since #6867 it is the cost
// summary document (DESIGN.md §3.2): the earlier three-block payload used
// keys the page never read, which is how hw307 rendered every KPI as zero.
func (h *Handler) overview(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireOperator(w, r)
	if !ok {
		return
	}
	h.writeSummary(w, r, s.Scope(), "")
}

// enrichSummary is the seam where the budgets and anomalies lanes add their
// blocks to the summary (parts.Budgets / parts.Anomalies). Kept as a method
// so each lane extends it without touching the composition.
//
// Anomalies: the last 7 days, top 5 by impact (DESIGN.md §3.2). A failure
// here is logged and leaves the block empty rather than failing the whole
// overview — the KPIs above it are already computed and correct.
func (h *Handler) enrichSummary(r *http.Request, scope store.Scope, customerID string, parts *summaryParts) {
	rows, err := h.summaryAnomalies(r.Context(), scope, customerID)
	if err != nil {
		slog.Error("summary anomalies", "error", err)
		return
	}
	parts.Anomalies = rows
}
