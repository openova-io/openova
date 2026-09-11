package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/access"
	"github.com/openova-io/openova/products/chargeback/internal/commercial/external"
	"github.com/openova-io/openova/products/chargeback/internal/finance"
	"github.com/openova-io/openova/products/chargeback/internal/settle"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The FINANCE HANDOVER API (DESIGN.md §18). Three surfaces, all at the
// SOVEREIGN scope — a customer and a partner never reach any of them, which
// is enforced by the scope the guards ask at, not by a filter on the rows.
//
//	GET  /api/v1/finance/journal?period=YYYY-MM[&format=csv|json]
//	POST /api/v1/finance/journal/export {period}     queue the TMF-shaped document
//	GET  /api/v1/finance/accounts                    the account map
//	PUT  /api/v1/finance/accounts {mappings:[...]}   edit it (settings.manage)
//	GET  /api/v1/finance/periods                     status per period
//	GET  /api/v1/finance/periods/{period}            one period + what blocks a close
//	POST /api/v1/finance/periods/{period}/close      (settings.manage)
//	POST /api/v1/finance/periods/{period}/reopen     (settings.manage, reason required)
//	GET  /api/v1/finance/reconciliations             the runs
//	POST /api/v1/finance/reconciliation              a settlement file, or a gateway fetch
//	GET  /api/v1/finance/reconciliation/{id}         one stored run
//
// Reading and reconciling need audit.read AND metering.read at the Sovereign
// — the finance-viewer bundle, and nothing weaker. Closing, reopening and
// editing the account map need settings.manage.

// requireFinance is the read gate: BOTH audit.read and metering.read, at the
// Sovereign. Two permissions rather than one because this surface is the
// whole ledger of every customer in one document: a principal that may read
// one customer's costs has not thereby been given the books.
func (h *Handler) requireFinance(w http.ResponseWriter, r *http.Request) (store.Session, bool) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return s, false
	}
	bindings := access.Bindings(s)
	for _, p := range []access.Permission{access.AuditRead, access.MeteringRead} {
		if !access.Has(bindings, p, "") {
			writeErr(w, http.StatusForbidden, "permission "+string(access.AuditRead)+" and "+string(access.MeteringRead)+" are required at the Sovereign to read the finance handover")
			return s, false
		}
	}
	return s, true
}

// ---------------------------------------------------------------------------
// the account map
// ---------------------------------------------------------------------------

func (h *Handler) listAccountMappings(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireFinance(w, r); !ok {
		return
	}
	list, err := h.Store.ListAccountMappings(r.Context())
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mappings": list, "keys": store.AccountKeys, "revenue_key_prefix": store.RevenueKeyPrefix})
}

// putAccountMappings upserts the rows it is given; a key it does not send is
// left alone, so an operator edits one account without re-sending the chart.
func (h *Handler) putAccountMappings(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSovereign(w, r, access.SettingsManage)
	if !ok {
		return
	}
	var in struct {
		Mappings []store.AccountMapping `json:"mappings"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if len(in.Mappings) == 0 {
		writeErr(w, http.StatusBadRequest, "send at least one mapping")
		return
	}
	list, err := h.Store.PutAccountMappings(r.Context(), in.Mappings, s.Email)
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeErr(w, http.StatusBadRequest, invalidMessage(err))
		return
	case err != nil:
		storeErr(w, err)
		return
	}
	changed := make([]map[string]any, 0, len(in.Mappings))
	for _, m := range in.Mappings {
		changed = append(changed, map[string]any{"key": m.Key, "account_code": m.AccountCode})
	}
	h.audit(r, nil, "finance.accounts", map[string]any{"op": "put", "count": len(in.Mappings), "mappings": changed})
	writeJSON(w, http.StatusOK, map[string]any{"mappings": list, "keys": store.AccountKeys, "revenue_key_prefix": store.RevenueKeyPrefix})
}

// ---------------------------------------------------------------------------
// the journal
// ---------------------------------------------------------------------------

// journalFor builds a period's journal: the FROZEN one when the period is
// closed, otherwise the live derivation. That is the whole of "a closed
// period's export is stable" — it is not recomputed, it is read.
func (h *Handler) journalFor(ctx context.Context, period string) (finance.Batch, store.FinancePeriod, error) {
	p, err := h.Store.GetFinancePeriod(ctx, period)
	if err != nil {
		return finance.Batch{}, p, err
	}
	if p.IsClosed() {
		rows, err := h.Store.StoredJournal(ctx, period)
		if err != nil {
			return finance.Batch{}, p, err
		}
		return finance.FromStored(period, rows), p, nil
	}
	batch, err := h.liveJournal(ctx, period)
	return batch, p, err
}

// liveJournal derives the period from the ledger and asserts the balance.
func (h *Handler) liveJournal(ctx context.Context, period string) (finance.Batch, error) {
	mappings, err := h.Store.ListAccountMappings(ctx)
	if err != nil {
		return finance.Batch{}, err
	}
	events, err := h.Store.JournalEvents(ctx, period)
	if err != nil {
		return finance.Batch{}, err
	}
	return finance.Emit(period, events, finance.AccountsOf(mappings))
}

// journal — GET /finance/journal?period=&format=csv|json.
func (h *Handler) journal(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireFinance(w, r); !ok {
		return
	}
	period := strings.TrimSpace(r.URL.Query().Get("period"))
	if !periodShape.MatchString(period) {
		writeErr(w, http.StatusBadRequest, "period must be YYYY-MM")
		return
	}
	batch, p, err := h.journalFor(r.Context(), period)
	switch {
	case errors.Is(err, store.ErrInvalid):
		// The refusal a caller most needs to read verbatim: the journal did
		// not balance, and the message names the difference.
		writeErr(w, http.StatusUnprocessableEntity, invalidMessage(err))
		return
	case err != nil:
		storeErr(w, err)
		return
	}
	if strings.ToLower(r.URL.Query().Get("format")) == "csv" {
		body, err := batch.CSV()
		if err != nil {
			storeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Disposition", `attachment; filename="journal-`+period+`.csv"`)
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(body); err != nil {
			return
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"period": period, "status": p.Status, "journal": batch, "period_state": p})
}

// exportJournal queues the journal as a document on the COMMERCIAL OUTBOX —
// the same at-least-once lane the bills leave by (DESIGN.md §8.10), so an
// operator receives the month's journal the way it receives every other
// document, through one transport it already configured.
func (h *Handler) exportJournal(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireFinance(w, r); !ok {
		return
	}
	var in struct {
		Period string `json:"period"`
	}
	if err := decode(r, &in); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	period := strings.TrimSpace(in.Period)
	if period == "" {
		period = strings.TrimSpace(r.URL.Query().Get("period"))
	}
	if !periodShape.MatchString(period) {
		writeErr(w, http.StatusBadRequest, "period must be YYYY-MM")
		return
	}
	batch, p, err := h.journalFor(r.Context(), period)
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeErr(w, http.StatusUnprocessableEntity, invalidMessage(err))
		return
	case err != nil:
		storeErr(w, err)
		return
	}
	doc := external.BuildJournal(period, p.Status, journalDocLines(batch), batch.TotalDebit, batch.TotalCredit, currencyOf(batch))
	env, err := external.Wrap(external.DocJournal, doc.IdempotencyKey, doc)
	if err != nil {
		storeErr(w, err)
		return
	}
	if err := h.Store.QueueOutbox(r.Context(), "", "", store.OutboxDocument{DocType: env.Type, IdempotencyKey: env.IdempotencyKey, Document: env.Document}); err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "finance.journal.export", map[string]any{"period": period, "status": p.Status, "lines": len(batch.Lines), "total_debit": batch.TotalDebit, "total_credit": batch.TotalCredit})
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true, "period": period, "document_type": external.DocJournal, "idempotency_key": env.IdempotencyKey, "lines": len(batch.Lines)})
}

// journalDocLines renders the batch into the TMF-shaped document's lines.
func journalDocLines(b finance.Batch) []external.JournalLine {
	out := make([]external.JournalLine, 0, len(b.Lines))
	for _, l := range b.Lines {
		out = append(out, external.JournalLine{
			Seq: l.Seq, Date: l.Date, Event: l.EventKind,
			AccountKey: l.AccountKey, AccountCode: l.AccountCode, AccountName: l.AccountName,
			Debit: external.Money{Value: l.Debit, Unit: l.Currency}, Credit: external.Money{Value: l.Credit, Unit: l.Currency},
			CustomerSlug: l.CustomerSlug, CustomerName: l.CustomerName,
			SourceKind: l.SourceKind, SourceID: l.SourceID, Reference: l.Reference, Memo: l.Memo,
		})
	}
	return out
}

func currencyOf(b finance.Batch) string {
	if len(b.ByCurrency) == 1 {
		return b.ByCurrency[0].Currency
	}
	for _, c := range b.ByCurrency {
		if c.Currency != "" {
			return c.Currency
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// the period close
// ---------------------------------------------------------------------------

func (h *Handler) listFinancePeriods(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireFinance(w, r); !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := h.Store.ListFinancePeriods(r.Context(), limit)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"periods": list})
}

// getFinancePeriod reports one period with WHAT BLOCKS ITS CLOSE, so the
// operator reads the answer before pressing the button rather than after.
func (h *Handler) getFinancePeriod(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireFinance(w, r); !ok {
		return
	}
	period := r.PathValue("period")
	if !periodShape.MatchString(period) {
		writeErr(w, http.StatusBadRequest, "period must be YYYY-MM")
		return
	}
	p, err := h.Store.GetFinancePeriod(r.Context(), period)
	if err != nil {
		storeErr(w, err)
		return
	}
	blockers, err := h.Store.PeriodBlockers(r.Context(), period)
	if err != nil {
		storeErr(w, err)
		return
	}
	body := map[string]any{"period": p, "blockers": blockers, "can_close": !p.IsClosed() && len(blockers) == 0}
	// The balance check as a FIGURE, which is what an operator is being asked
	// to trust. A period that does not balance says so here rather than at
	// the moment the close is refused. A CLOSED period reports the journal it
	// was closed on, not a fresh derivation — that is what it was signed off
	// against.
	if batch, _, jerr := h.journalFor(r.Context(), period); jerr == nil {
		body["total_debit"], body["total_credit"], body["lines"] = batch.TotalDebit, batch.TotalCredit, len(batch.Lines)
		body["balanced"] = batch.Balanced
	} else if errors.Is(jerr, store.ErrInvalid) {
		body["balanced"], body["balance_error"] = false, invalidMessage(jerr)
	}
	writeJSON(w, http.StatusOK, body)
}

// closePeriod refuses while any statement in the period is a draft or any
// dispute on it is open, NAMING them, and otherwise stamps the period closed
// with the journal it was closed on frozen beside it.
func (h *Handler) closePeriod(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSovereign(w, r, access.SettingsManage)
	if !ok {
		return
	}
	period := r.PathValue("period")
	if !periodShape.MatchString(period) {
		writeErr(w, http.StatusBadRequest, "period must be YYYY-MM")
		return
	}
	blockers, err := h.Store.PeriodBlockers(r.Context(), period)
	if err != nil {
		storeErr(w, err)
		return
	}
	if len(blockers) > 0 {
		drafts, disputes := 0, 0
		names := make([]string, 0, len(blockers))
		for _, b := range blockers {
			if b.Kind == store.BlockerDraftStatement {
				drafts++
			} else {
				disputes++
			}
			names = append(names, b.Detail)
		}
		writeErrDetails(w, http.StatusConflict,
			fmt.Sprintf("%s cannot be closed: %s. Issue or cancel the drafts and resolve the disputes first", period, countPhrase(drafts, disputes)),
			map[string]any{"blockers": blockers, "reasons": names})
		return
	}
	batch, err := h.liveJournal(r.Context(), period)
	switch {
	case errors.Is(err, store.ErrInvalid):
		// The balance assertion is a HARD refusal: a period whose journal
		// does not balance is never closed on it.
		writeErr(w, http.StatusUnprocessableEntity, invalidMessage(err))
		return
	case err != nil:
		storeErr(w, err)
		return
	}
	p, err := h.Store.ClosePeriod(r.Context(), period, batch.Stored(), batch.TotalDebit, batch.TotalCredit, s.Email)
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeErr(w, http.StatusBadRequest, invalidMessage(err))
		return
	case err != nil:
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "finance.period.close", map[string]any{"period": period, "lines": len(batch.Lines), "total_debit": batch.TotalDebit, "total_credit": batch.TotalCredit})
	writeJSON(w, http.StatusOK, map[string]any{"period": p, "journal": batch})
}

func countPhrase(drafts, disputes int) string {
	parts := []string{}
	if drafts > 0 {
		parts = append(parts, plural(drafts, "statement is still a draft", "statements are still drafts"))
	}
	if disputes > 0 {
		parts = append(parts, plural(disputes, "invoice is disputed", "invoices are disputed"))
	}
	return strings.Join(parts, " and ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// reopenPeriod lifts a close. It needs settings.manage, it needs a REASON,
// and it is audited — reopening a closed month is a thing a finance
// department must be able to see happened, and by whom.
func (h *Handler) reopenPeriod(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSovereign(w, r, access.SettingsManage)
	if !ok {
		return
	}
	period := r.PathValue("period")
	if !periodShape.MatchString(period) {
		writeErr(w, http.StatusBadRequest, "period must be YYYY-MM")
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err := decode(r, &in); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if strings.TrimSpace(in.Reason) == "" {
		writeErr(w, http.StatusBadRequest, "reopening a closed period needs a reason")
		return
	}
	before, err := h.Store.GetFinancePeriod(r.Context(), period)
	if err != nil {
		storeErr(w, err)
		return
	}
	p, err := h.Store.ReopenPeriod(r.Context(), period, in.Reason, s.Email)
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeErr(w, http.StatusBadRequest, invalidMessage(err))
		return
	case err != nil:
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "finance.period.reopen", map[string]any{"period": period, "reason": strings.TrimSpace(in.Reason), "closed_by": before.ClosedBy, "closed_at": before.ClosedAt})
	writeJSON(w, http.StatusOK, map[string]any{"period": p})
}

// ---------------------------------------------------------------------------
// the closed-period guard
// ---------------------------------------------------------------------------

// refuseClosedPeriod is the ONE check every financial write asks before it
// changes a month: issuing, re-running, cancelling and crediting are all
// refused once the books are closed, with a message that says who closed it
// and when. It answers true when it has already written the refusal.
//
// It is deliberately a helper called from the existing handlers rather than a
// rule buried in the store: the store's job is to record what happened, and
// "this month is shut" is a policy the operator sets and lifts.
func (h *Handler) refuseClosedPeriod(w http.ResponseWriter, r *http.Request, period, what string) bool {
	period = strings.TrimSpace(period)
	if len(period) > 7 {
		period = period[:7]
	}
	if !periodShape.MatchString(period) {
		return false
	}
	p, err := h.Store.GetFinancePeriod(r.Context(), period)
	if err != nil {
		// A month nobody closed reads open with no row behind it, so an error
		// here is a real failure to READ the policy — and a policy that
		// cannot be read stops the write rather than waving it through.
		storeErr(w, err)
		return true
	}
	if !p.IsClosed() {
		return false
	}
	by := p.ClosedBy
	if strings.TrimSpace(by) == "" {
		by = "an operator"
	}
	when := ""
	if p.ClosedAt != nil {
		when = " on " + p.ClosedAt.Format("2006-01-02")
	}
	writeErr(w, http.StatusConflict, what+" is refused: the "+period+" period was closed by "+by+when+
		". Reopen it with a reason if this has to change")
	return true
}

// ---------------------------------------------------------------------------
// reconciliation
// ---------------------------------------------------------------------------

func (h *Handler) listReconciliations(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireFinance(w, r); !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := h.Store.ListReconciliations(r.Context(), limit)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": list, "buckets": store.ReconciliationBuckets})
}

func (h *Handler) getReconciliation(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireFinance(w, r); !ok {
		return
	}
	run, err := h.Store.GetReconciliation(r.Context(), r.PathValue("id"))
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": run, "buckets": store.ReconciliationBuckets})
}

const maxSettlementBytes = 8 << 20

// runReconciliation compares a gateway's settlement with the ledger and
// stores the report. Two ways in, one comparison:
//
//   - a settlement FILE — multipart `file`, or a raw `text/csv` body;
//   - a gateway FETCH — `?gateway=&from=&to=` with no body, through
//     settle.Gateway.Settlements, when that gateway publishes a feed.
//
// Nothing is corrected: no payment is created, amended or reallocated. The
// four buckets are the deliverable.
func (h *Handler) runReconciliation(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireFinance(w, r)
	if !ok {
		return
	}
	qs := r.URL.Query()
	gateway := strings.TrimSpace(qs.Get("gateway"))
	from, to, msg := reconcileWindow(qs.Get("from"), qs.Get("to"))
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	settlements, source, fileName, msg := h.readSettlements(r, gateway, from, to)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	payments, err := h.Store.GatewayPayments(r.Context(), gateway, from, to)
	if err != nil {
		storeErr(w, err)
		return
	}
	run := finance.Reconcile(gateway, source, fileName, from.Format("2006-01-02"), to.Format("2006-01-02"), settlements, payments, s.Email)
	stored, err := h.Store.SaveReconciliation(r.Context(), run)
	if err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "finance.reconciliation", map[string]any{
		"run_id": stored.ID, "gateway": stored.Gateway, "source": stored.Source, "from": stored.From, "to": stored.To,
		"matched": stored.Matched, "amount_mismatched": stored.Mismatched, "missing_in_ledger": stored.MissingInLedger,
		"missing_in_settlement": stored.MissingInSettlement, "duplicates": stored.Duplicates, "fee_total": stored.FeeTotal,
	})
	writeJSON(w, http.StatusCreated, map[string]any{"run": stored, "buckets": store.ReconciliationBuckets})
}

// readSettlements takes the settlement lines from the request body, or from
// the gateway when the request names one and carries no file.
func (h *Handler) readSettlements(r *http.Request, gateway string, from, to time.Time) ([]finance.Settlement, string, string, string) {
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	switch {
	case strings.HasPrefix(ct, "multipart/"):
		if err := r.ParseMultipartForm(maxSettlementBytes); err != nil {
			return nil, "", "", "could not read the uploaded settlement file: " + err.Error()
		}
		f, hdr, err := r.FormFile("file")
		if err != nil {
			return nil, "", "", "the upload carries no `file` part"
		}
		defer f.Close()
		lines, err := finance.ParseSettlementCSV(io.LimitReader(f, maxSettlementBytes))
		if err != nil {
			return nil, "", "", invalidMessage(err)
		}
		return lines, store.ReconcileFromFile, nameOf(hdr), ""
	case ct == "text/csv" || ct == "application/csv":
		lines, err := finance.ParseSettlementCSV(io.LimitReader(r.Body, maxSettlementBytes))
		if err != nil {
			return nil, "", "", invalidMessage(err)
		}
		return lines, store.ReconcileFromFile, strings.TrimSpace(r.URL.Query().Get("file_name")), ""
	}
	// No file: ask the gateway itself.
	if gateway == "" {
		return nil, "", "", "send the settlement file (multipart `file` or a text/csv body), or name a `gateway` that publishes a settlement feed"
	}
	lines, err := h.Settlement.Settlements(r.Context(), gateway, from, to)
	switch {
	case errors.Is(err, settle.ErrSettlementsNotSupported):
		return nil, "", "", "the " + gateway + " gateway publishes no settlement feed; upload its settlement file instead"
	case errors.Is(err, settle.ErrNoGateway):
		return nil, "", "", "no gateway named " + gateway + " is registered on this Sovereign"
	case err != nil:
		return nil, "", "", "the gateway could not be asked for its settlements: " + err.Error()
	}
	out := make([]finance.Settlement, 0, len(lines))
	for i, l := range lines {
		out = append(out, finance.Settlement{Reference: l.Reference, Amount: l.Amount, Currency: l.Currency, SettledAt: l.SettledAt, Fee: l.Fee, Line: i + 1})
	}
	if len(out) == 0 {
		return nil, "", "", "the " + gateway + " gateway reported no settlements between " + from.Format("2006-01-02") + " and " + to.Format("2006-01-02")
	}
	return out, store.ReconcileFromGateway, "", ""
}

func nameOf(hdr *multipart.FileHeader) string {
	if hdr == nil {
		return ""
	}
	return strings.TrimSpace(hdr.Filename)
}

// reconcileWindow reads `from` and `to` as days, defaulting to the last 31
// days. `to` is EXCLUSIVE at the end of the named day, so a window given as
// two dates includes both of them.
func reconcileWindow(fromRaw, toRaw string) (time.Time, time.Time, string) {
	now := time.Now().UTC().Truncate(24 * time.Hour)
	from, to := now.AddDate(0, 0, -31), now.AddDate(0, 0, 1)
	if s := strings.TrimSpace(fromRaw); s != "" {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			return time.Time{}, time.Time{}, "from must be YYYY-MM-DD"
		}
		from = t.UTC()
	}
	if s := strings.TrimSpace(toRaw); s != "" {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			return time.Time{}, time.Time{}, "to must be YYYY-MM-DD"
		}
		to = t.UTC().AddDate(0, 0, 1)
	}
	if !to.After(from) {
		return time.Time{}, time.Time{}, "to must not be before from"
	}
	return from, to, ""
}
