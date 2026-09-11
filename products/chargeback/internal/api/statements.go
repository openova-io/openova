package api

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/access"
	"github.com/openova-io/openova/products/chargeback/internal/einvoice"
	"github.com/openova-io/openova/products/chargeback/internal/notify"
	"github.com/openova-io/openova/products/chargeback/internal/rating"
	"github.com/openova-io/openova/products/chargeback/internal/report"
	"github.com/openova-io/openova/products/chargeback/internal/settle"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

var periodShape = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)

// runStatements rates a period for every customer (or one) into drafts.
func (h *Handler) runStatements(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.BillingIssue); !ok {
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
	// DESIGN.md §18.4 — a closed period does not move. Re-rating one would
	// write new drafts over figures the books were closed on, so the run is
	// refused here, before the rating engine is entered at all.
	if h.refuseClosedPeriod(w, r, in.Period, "re-running the statements for "+in.Period) {
		return
	}
	results, err := rating.Run(r.Context(), h.Store, in.Period, in.CustomerID)
	if err != nil {
		if errors.Is(err, rating.ErrMixedCurrency) {
			// DESIGN.md §2.9: a statement is issued in ONE currency; a
			// customer whose sources are on books of different currencies
			// cannot be rated until the operator assigns books of one.
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
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

// redactPartner strips the partner BUY and MARGIN figures from statements a
// principal may read but must not see them on (DESIGN.md §13): they
// are shown to Sovereign roles and to the partner's own roles only. The
// partner assignment itself stays — it is the customer's own commercial
// relationship.
func (h *Handler) redactPartner(s store.Session, sts []store.Statement) {
	bindings := access.Bindings(s)
	if access.IsSovereign(bindings) {
		return
	}
	for i := range sts {
		if sts[i].PartnerID == nil || !access.OnPartner(bindings, *sts[i].PartnerID) {
			sts[i].RedactPartner()
		}
	}
}

func (h *Handler) listAllStatements(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	qs := r.URL.Query()
	period := qs.Get("period")
	if period != "" && !periodShape.MatchString(period) {
		writeErr(w, http.StatusBadRequest, "period must be YYYY-MM")
		return
	}
	if !s.Scope().Operator {
		// A customer principal gets its own list here too; a partner
		// principal its customers' statements AND its own party's.
		list, err := h.Store.ListStatementsInScope(r.Context(), s.Scope(), period)
		if err != nil {
			storeErr(w, err)
			return
		}
		h.redactPartner(s, list)
		writeJSON(w, http.StatusOK, map[string]any{"statements": list})
		return
	}
	// Optional `customer_id` (alias `customer`): one customer's statements,
	// still narrowed by `period` when both are given. The hw307 walk found
	// the parameter silently ignored — the operator got every customer's.
	customerID := strings.TrimSpace(qs.Get("customer_id"))
	if customerID == "" {
		customerID = strings.TrimSpace(qs.Get("customer"))
	}
	if customerID == "" {
		list, err := h.Store.ListAllStatements(r.Context(), period)
		if err != nil {
			storeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"statements": list})
		return
	}
	list, err := h.Store.ListStatements(r.Context(), s.Scope(), customerID)
	if errors.Is(err, store.ErrNotFound) {
		// An id no customer has — or one that is not a UUID at all — is a
		// filter that selected nothing, not a missing document.
		list, err = []store.Statement{}, nil
	}
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"statements": statementsInPeriod(list, period)})
}

// statementsInPeriod keeps the statements whose month is `period` (YYYY-MM);
// an empty period keeps them all. The per-customer store query has no
// period argument, so the narrowing happens here. The month is compared
// whole, so a shorter string (a bare year) selects nothing rather than
// every month it prefixes.
func statementsInPeriod(list []store.Statement, period string) []store.Statement {
	if period == "" {
		return list
	}
	out := []store.Statement{}
	for _, st := range list {
		if len(st.PeriodStart) >= 7 && st.PeriodStart[:7] == period {
			out = append(out, st)
		}
	}
	return out
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
	h.redactPartner(s, list)
	writeJSON(w, http.StatusOK, map[string]any{"statements": list})
}

// getStatement serves JSON, CSV when the id carries a .csv suffix, or a PDF
// when it carries .pdf (EPIC #6867).
//
// All three are the SAME read: the id is resolved through the session's scope
// by the store, so a customer principal can download its own invoice and
// gets 404 on anybody else's — the permission to download a document is
// exactly the permission to read the statement it is made of, expressed as
// one code path rather than two that could drift apart.
func (h *Handler) getStatement(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	asCSV := strings.HasSuffix(id, ".csv")
	asPDF := strings.HasSuffix(id, ".pdf")
	id = strings.TrimSuffix(strings.TrimSuffix(id, ".csv"), ".pdf")
	st, err := h.Store.GetStatement(r.Context(), s.Scope(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	// Redaction runs BEFORE any representation is chosen: a partner reads a
	// statement of its customer with the provider's figures removed, and the
	// PDF it downloads is made from exactly that redacted statement.
	one := []store.Statement{st}
	h.redactPartner(s, one)
	st = one[0]
	if asPDF {
		h.statementPDF(w, r, st)
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

// statementPDF renders the statement through the document renderer
// (EPIC #6867) and streams the PDF back.
//
// The renderer is OPTIONAL infrastructure. With DOCRENDER_URL unset this
// answers 503 and says so plainly, because a Sovereign that has not enabled
// the renderer has not lost a document — it has not turned the feature on,
// and an operator reading the response needs to be told which of the two it
// is. Nothing else on this route changes.
func (h *Handler) statementPDF(w http.ResponseWriter, r *http.Request, st store.Statement) {
	if h.Docs == nil || !h.Docs.Enabled() {
		writeErr(w, http.StatusServiceUnavailable, "document renderer not configured")
		return
	}
	// The statement was already scope-checked above, so loading its customer
	// and the Sovereign's billing settings is a read the caller has earned.
	c, err := h.Store.GetCustomer(r.Context(), store.OperatorScope, st.CustomerID)
	if err != nil {
		storeErr(w, err)
		return
	}
	settings, err := h.Store.GetBillingSettings(r.Context())
	if err != nil {
		storeErr(w, err)
		return
	}
	body, filename, err := h.Docs.RenderInvoice(r.Context(), st, c, settings)
	if err != nil {
		// The renderer's own message names what it refused (a missing seller
		// legal name, say), which is what an operator needs; the customer
		// gets the plain answer.
		slog.Error("render statement document", "statement", st.ID, "invoice_number", st.InvoiceNumber, "error", err)
		writeErr(w, http.StatusBadGateway, "the document could not be rendered")
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, filename))
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if _, err := w.Write(body); err != nil {
		slog.Warn("write statement document", "statement", st.ID, "error", err)
	}
}

// issueStatement — POST /statements/{id}/issue, optional body
// {"notify": bool} (default true). Issuing is idempotent; the customer is
// mailed only on the draft → issued transition, so a re-POST (to repeat the
// billing hook, say) never mails twice.
func (h *Handler) issueStatement(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.BillingIssue); !ok {
		return
	}
	var in struct {
		Notify *bool `json:"notify"`
	}
	if err := decode(r, &in); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	// Named notifyCustomer, not notify: the notify PACKAGE is imported in
	// this file, and a local that shadows it would make every later
	// notify.X in this function fail to compile for a puzzling reason.
	notifyCustomer := in.Notify == nil || *in.Notify
	// DESIGN.md §18.4 — issuing an invoice INTO a closed period would post a
	// receivable the books have already been signed off without.
	if st, err := h.Store.GetStatement(r.Context(), store.OperatorScope, r.PathValue("id")); err == nil {
		if h.refuseClosedPeriod(w, r, st.PeriodStart, "issuing this invoice") {
			return
		}
	}
	// DESIGN.md §8.10 — WHO invoices. Internally this numbers the invoice and
	// runs our own lifecycle; externally it queues the rated bill for the
	// operator's billing system and takes no number at all.
	provider, settings, err := h.Commercial.For(r.Context())
	if err != nil {
		storeErr(w, err)
		return
	}
	// DESIGN.md §17 — the e-invoicing PRE-FLIGHT. A statement that cannot
	// yield a compliant e-invoice is refused here, BEFORE anything is
	// numbered, with every problem listed: exactly as the commercial outbox
	// refuses a malformed export. Refusing after the flip would be refusing
	// something that already happened.
	if h.eInvoiceEnabled(settings) {
		customerID, problems, perr := h.preflightEInvoice(r.Context(), r.PathValue("id"), settings)
		if perr != nil && !errors.Is(perr, store.ErrNotFound) {
			storeErr(w, perr)
			return
		}
		if len(problems) > 0 {
			var owner *string
			if customerID != "" {
				owner = &customerID
			}
			h.audit(r, owner, "statement.einvoice.refused", map[string]any{"statement_id": r.PathValue("id"), "problems": problems})
			writeErr(w, http.StatusBadRequest, einvoice.ProblemsError(problems).Error())
			return
		}
	}
	st, transitioned, err := provider.Issue(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeErr(w, http.StatusBadRequest, invalidMessage(err))
		return
	case err != nil:
		storeErr(w, err)
		return
	}
	if settings.ExternalCommercial() {
		// The billing system sends its own invoice and collects on it; a
		// second copy from us would confuse the customer it reached.
		notifyCustomer = false
	}
	h.audit(r, &st.CustomerID, "statement.issue", map[string]any{"statement_id": st.ID, "period": st.PeriodStart[:7], "total": st.Total, "transitioned": transitioned, "notify": notifyCustomer,
		"invoice_number": st.InvoiceNumber, "po_reference": st.PORef, "due_at": st.DueAt, "commercial_provider": provider.Name()})
	c, cerr := h.Store.GetCustomer(r.Context(), store.OperatorScope, st.CustomerID)
	if cerr != nil {
		slog.Warn("issue statement: load customer", "statement", st.ID, "customer", st.CustomerID, "error", cerr)
	}
	// ADR-0014 D6 / DESIGN.md §8: an issued statement is handed to whoever
	// collects for THIS customer — the Stripe-backed billing hook when the
	// payment method is the stripe gateway, the built-in manual gateway when
	// it is a transfer or an internal recharge, nothing at all when charging
	// is informational. Requesting settlement is idempotent on the statement
	// id, so a failure here leaves the statement issued and the operator
	// re-POSTs issue to repeat it.
	//
	// DESIGN.md §9.2 (founder refinement (a)): this is a COLLECTION — the
	// receivable's owner pursues it — so with an external billing system the
	// gateway is NEVER called for an invoice; only its settled status is
	// imported. And an invoice already settled from account credit at issue
	// (prepaid, or auto-apply) has nothing left to collect.
	if cerr == nil && settings.ExternalCommercial() {
		h.audit(r, &st.CustomerID, "statement.settlement.skipped", map[string]any{"statement_id": st.ID, "reason": "collection is owned by the external billing system"})
	} else if cerr == nil && st.Status == store.StatusPaid {
		h.audit(r, &st.CustomerID, "statement.settlement.skipped", map[string]any{"statement_id": st.ID, "reason": "settled from account credit at issue", "paid_total": st.Paid})
	} else if cerr == nil {
		if res, serr := h.Settlement.RequestSettlement(r.Context(), st, c); serr != nil {
			slog.Warn("settlement request failed; the statement stays issued and a re-issue repeats the idempotent request", "statement", st.ID, "payment_method", c.PaymentMethod, "gateway", c.GatewayName, "error", serr)
			h.audit(r, &st.CustomerID, "statement.hook.error", map[string]any{"statement_id": st.ID, "payment_method": c.PaymentMethod, "gateway_name": c.GatewayName, "error": serr.Error()})
		} else if transitioned && res.Outcome != settle.NotApplicable {
			h.audit(r, &st.CustomerID, "statement.settlement.requested", map[string]any{
				"statement_id": st.ID, "charging": c.Charging, "payment_model": c.PaymentModel, "payment_method": c.PaymentMethod,
				"outcome": string(res.Outcome), "gateway": res.Gateway, "reference": res.Reference, "detail": res.Detail,
			})
		}
	}
	// DESIGN.md §17 — BUILD, SIGN, ARCHIVE, SUBMIT. The statement is issued
	// by now: a failure here leaves it issued and a re-POST of issue repeats
	// the step, exactly as the settlement request does. The pre-flight above
	// has already refused every problem this could find, so what is left is
	// a signing or a storage failure, which is an operator incident rather
	// than a customer-visible refusal.
	if h.eInvoiceEnabled(settings) && cerr == nil {
		rec, eerr := h.finalizeEInvoice(r.Context(), st, c, settings)
		if eerr != nil {
			slog.Error("e-invoice could not be produced; the statement stays issued and a re-issue repeats the step", "statement", st.ID, "invoice_number", st.InvoiceNumber, "error", eerr)
			h.audit(r, &st.CustomerID, "statement.einvoice.error", map[string]any{"statement_id": st.ID, "error": eerr.Error()})
		} else {
			h.audit(r, &st.CustomerID, "statement.einvoice", map[string]any{"statement_id": st.ID, "profile": rec.Profile, "state": rec.State,
				"hash": rec.Hash, "signature_algorithm": rec.SignatureAlgorithm, "key_id": rec.KeyID, "submit_reason": rec.SubmitReason})
			st.EInvoice = &rec.EInvoiceState
		}
	}
	if transitioned && notifyCustomer && cerr == nil {
		h.notifyStatement(r, st, c)
	}
	// DESIGN.md §9.5 — a prepaid customer's service depends on the balance
	// the issue just drew on: the low-balance alert and suspend-at-zero.
	if transitioned {
		h.afterAccountChange(r, st.CustomerID, nil)
	}
	writeJSON(w, http.StatusOK, st)
}

// notifyStatement mails the plain-text statement summary to the customer's
// admin_email and every customer_users admin, and audits statement.notified
// with the recipients. Send failures are noted in the audit entry, never
// surfaced as a request failure: the statement is issued either way.
func (h *Handler) notifyStatement(r *http.Request, st store.Statement, c store.Customer) {
	recipients := []string{}
	seen := map[string]bool{}
	add := func(e string) {
		e = normEmail(e)
		if e == "" || seen[e] || !validEmail(e) {
			return
		}
		seen[e] = true
		recipients = append(recipients, e)
	}
	add(c.AdminEmail)
	if users, err := h.Store.ListCustomerUsers(r.Context(), c.ID); err != nil {
		slog.Warn("statement notify: list users", "customer", c.ID, "error", err)
	} else {
		for _, u := range users {
			if u.Role == "admin" {
				add(u.Email)
			}
		}
	}
	link := strings.TrimRight(h.Config.PublicURL, "/") + "/statements/" + st.ID
	// DESIGN.md §21 — statement.issued, a MANDATORY event. The DOCUMENT is
	// still report.RenderStatement's: the waterfall, the ranked lines and
	// the currency's minor unit belong to the renderer. The template carries
	// it, which puts the send on the catalogue, the delivery log and the
	// preference rule without changing one byte of the invoice.
	subject, body := report.RenderStatement(st, link)
	payload := map[string]any{"subject": subject, "document": body, "statement_id": st.ID, "period": st.PeriodStart[:7]}
	n := h.notifier()
	sent := []string{}
	var failed []string
	for _, to := range recipients {
		res, err := n.Send(r.Context(), notify.Request{Event: notify.EventStatementIssued, To: to, CustomerID: &st.CustomerID, Payload: payload})
		if err != nil {
			slog.Warn("statement notify: send", "statement", st.ID, "to", to, "error", err)
			failed = append(failed, to+": "+err.Error())
			continue
		}
		if !res.Sent {
			continue
		}
		sent = append(sent, to)
	}
	details := map[string]any{"statement_id": st.ID, "period": st.PeriodStart[:7], "recipients": sent, "subject": subject}
	if len(failed) > 0 {
		details["failed"] = failed
	}
	h.audit(r, &st.CustomerID, "statement.notified", details)
}

// deleteStatement removes a DRAFT and its rated lines so the period can be
// re-run from nothing. An issued statement is refused (409): it is the bill
// the customer received.
func (h *Handler) deleteStatement(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.BillingIssue); !ok {
		return
	}
	id := r.PathValue("id")
	st, err := h.Store.GetStatement(r.Context(), store.OperatorScope, id)
	if err != nil {
		storeErr(w, err)
		return
	}
	if err := h.Store.DeleteDraftStatement(r.Context(), id); err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, &st.CustomerID, "statement.delete", map[string]any{"statement_id": id, "period": st.PeriodStart[:7], "lines": len(st.Lines), "total": st.Total})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
}
