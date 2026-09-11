package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/config"
	"github.com/openova-io/openova/products/chargeback/internal/einvoice"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// E-invoicing at issue (DESIGN.md §17).
//
// The step runs only where all three hold: a profile is configured
// (EINVOICE_PROFILE), this Sovereign is the system of record (commercial
// provider internal — in external mode the operator's billing system issues
// the legal invoice and a second signed copy from us would be a second
// invoice), and the statement is being issued rather than re-issued.
//
// The order is BUILD → VALIDATE → (issue) → BUILD → SIGN → ARCHIVE → SUBMIT.
// Validation runs BEFORE the status flips, because a refusal after the flip
// would be a refusal of something that already happened: an invoice number
// is gapless per year and an issued statement is a document the customer
// holds. The first build uses a provisional number for exactly that reason
// and the second, after the flip, uses the real one.
//
//	GET /api/v1/statements/{id}/einvoice      → the structured document
//	GET /api/v1/statements/{id}/einvoice.xml  → the signed archival copy
//
// Both follow reading the STATEMENT: whoever may read the statement may read
// its e-invoice, and the scope check is the store's, not a second path.

// provisionalNumber stands in for the invoice number during the pre-issue
// validation. It is never archived: the document that is signed carries the
// real number, taken inside the transaction that flipped the status.
const provisionalNumber = "(assigned at issue)"

// eInvoiceEnabled reports whether the step runs at all.
func (h *Handler) eInvoiceEnabled(settings store.BillingSettings) bool {
	return h.EInvoice != nil && !settings.ExternalCommercial()
}

// buildEInvoice maps a statement onto the profile's input and builds the
// document. It computes nothing: every amount is the decimal string the
// ledger settled.
func (h *Handler) buildEInvoice(st store.Statement, c store.Customer, settings store.BillingSettings, number string, issuedAt time.Time) (einvoice.Document, error) {
	// The per-rule summary, and the rate each LINE was taxed at. A statement
	// rated before §17 has no summary, and every line then carries the one
	// rate the statement was rated at — which is what those invoices have
	// always said.
	byRule := map[string]store.TaxLine{}
	for _, t := range st.TaxLines {
		byRule[t.RuleID] = t
	}
	seller := einvoice.Party{
		Name:               settings.LegalName,
		RegistrationNumber: settings.TaxRegistrationNumber,
		Country:            settings.TaxCountry,
		Address:            settings.Address,
	}
	buyer := einvoice.Party{
		Name:               firstNonBlank(st.CustomerName, c.Name, c.Slug),
		RegistrationNumber: c.TaxRegistrationNumber,
		Country:            c.TaxCountry,
		Region:             c.TaxRegion,
		Email:              c.AdminEmail,
	}
	// The snapshot is authoritative over anything live: an invoice the
	// customer holds must keep saying what it said when it was issued.
	if snap := st.TaxSnapshot; snap != nil {
		if snap.SellerLegalName != "" {
			seller.Name = snap.SellerLegalName
		}
		if snap.SellerTaxNumber != "" {
			seller.RegistrationNumber = snap.SellerTaxNumber
		}
		if snap.SellerAddress != "" {
			seller.Address = snap.SellerAddress
		}
		if snap.SellerCountry != "" {
			seller.Country = snap.SellerCountry
		}
		if snap.CustomerName != "" {
			buyer.Name = snap.CustomerName
		}
		if snap.CustomerTaxNumber != "" {
			buyer.RegistrationNumber = snap.CustomerTaxNumber
		}
		if snap.CustomerCountry != "" {
			buyer.Country = snap.CustomerCountry
		}
	}

	lines := make([]einvoice.Line, 0, len(st.Lines))
	for _, l := range st.Lines {
		kind, rate := store.TaxKindStandard, string(st.TaxRate)
		if t, ok := byRule[l.TaxRuleID]; ok {
			kind, rate = t.Kind, string(t.Rate)
		}
		lines = append(lines, einvoice.Line{
			SKU:         l.SKU,
			Unit:        l.Unit,
			Quantity:    string(l.Quantity),
			UnitPrice:   string(l.UnitPrice),
			Amount:      string(l.Amount),
			TaxCategory: l.TaxCategory,
			TaxKind:     kind,
			TaxRate:     rate,
		})
	}
	subs := make([]einvoice.TaxSubtotal, 0, len(st.TaxLines))
	var notes []string
	for _, t := range st.TaxLines {
		subs = append(subs, einvoice.TaxSubtotal{
			RuleID: t.RuleID, RuleName: t.RuleName, Kind: t.Kind, Category: t.Category,
			Rate: string(t.Rate), Base: string(t.Base), Tax: string(t.Tax), Note: t.Note,
		})
		if strings.TrimSpace(t.Note) != "" {
			notes = append(notes, t.Note)
		}
	}
	if len(subs) == 0 && st.TaxSnapshot != nil && st.TaxSnapshot.Exempt && st.TaxSnapshot.ExemptReason != "" {
		notes = append(notes, st.TaxSnapshot.ExemptReason)
	}
	in := einvoice.Input{
		Seller:   seller,
		Buyer:    buyer,
		IssuedAt: issuedAt,
		Statement: einvoice.Statement{
			ID:            st.ID,
			InvoiceNumber: number,
			Currency:      st.Currency,
			PeriodStart:   st.PeriodStart,
			PeriodEnd:     st.PeriodEnd,
			PORef:         st.PORef,
			ListSubtotal:  addDecimals(st.Subtotal, st.DiscountTotal),
			DiscountTotal: string(st.DiscountTotal),
			NetSubtotal:   string(st.Subtotal),
			TaxTotal:      string(st.Tax),
			Total:         string(st.Total),
			Lines:         lines,
			TaxSubtotals:  subs,
			Notes:         dedupe(notes),
		},
	}
	if st.DueAt != nil {
		in.Statement.DueAt = st.DueAt.UTC().Format("2006-01-02")
	}
	return h.EInvoice.Build(in)
}

// preflightEInvoice builds the document a statement WOULD produce and
// validates it, so an issue that cannot yield a compliant e-invoice is
// refused before anything is numbered. It returns the customer the refusal
// belongs to — a compliance refusal that cannot be found on the customer's
// own audit trail is one nobody will ever explain — and the problems;
// empty = go.
func (h *Handler) preflightEInvoice(ctx context.Context, id string, settings store.BillingSettings) (string, []einvoice.Problem, error) {
	st, err := h.Store.GetStatement(ctx, store.OperatorScope, id)
	if err != nil {
		return "", nil, err
	}
	if st.Status != store.StatusDraft {
		// Already issued: the real document exists (or is rebuilt below).
		return st.CustomerID, nil, nil
	}
	c, err := h.Store.GetCustomer(ctx, store.OperatorScope, st.CustomerID)
	if err != nil {
		return st.CustomerID, nil, err
	}
	number := st.InvoiceNumber
	if number == "" {
		number = provisionalNumber
	}
	doc, err := h.buildEInvoice(st, c, settings, number, h.Now().UTC())
	if err != nil {
		return st.CustomerID, nil, err
	}
	return st.CustomerID, h.EInvoice.Validate(doc), nil
}

// finalizeEInvoice builds the real document, signs it, archives it with its
// hash, and records the submission outcome. Called AFTER the status flipped,
// with the number the flip assigned.
func (h *Handler) finalizeEInvoice(ctx context.Context, st store.Statement, c store.Customer, settings store.BillingSettings) (store.EInvoiceRecord, error) {
	issued := h.Now().UTC()
	if st.IssuedAt != nil {
		issued = st.IssuedAt.UTC()
	}
	number := st.InvoiceNumber
	if number == "" {
		number = st.ExternalInvoiceRef
	}
	doc, err := h.buildEInvoice(st, c, settings, number, issued)
	if err != nil {
		return store.EInvoiceRecord{}, err
	}
	if problems := h.EInvoice.Validate(doc); len(problems) > 0 {
		return store.EInvoiceRecord{}, einvoice.ProblemsError(problems)
	}
	signed, err := h.EInvoice.Sign(doc)
	if err != nil {
		return store.EInvoiceRecord{}, err
	}
	body, err := json.Marshal(signed.Document)
	if err != nil {
		return store.EInvoiceRecord{}, err
	}
	rec := store.EInvoiceRecord{
		StatementID: st.ID,
		Document:    body,
		XML:         string(signed.XML),
		EInvoiceState: store.EInvoiceState{
			Profile:            h.EInvoice.Name(),
			InvoiceNumber:      number,
			State:              store.EInvoiceArchived,
			Hash:               signed.Hash,
			Signature:          signed.Signature,
			SignatureAlgorithm: signed.Algorithm,
			KeyID:              signed.KeyID,
			QRPayload:          signed.Document.QRPayload,
		},
	}
	// Submission. A profile whose authority has published no endpoint says
	// so and the document stops at archived — which is a complete, compliant
	// outcome, not a failure.
	receipt, err := h.EInvoice.Submit(ctx, signed)
	switch {
	case err != nil:
		rec.State, rec.SubmitReason = store.EInvoiceNotSubmitted, err.Error()
	case receipt.Status == einvoice.StatusSubmitted:
		rec.State, rec.SubmitReference = store.EInvoiceSubmitted, receipt.Reference
		at := receipt.SubmittedAt
		if at.IsZero() {
			at = issued
		}
		at = at.UTC()
		rec.SubmittedAt = &at
	default:
		rec.State, rec.SubmitReason = store.EInvoiceNotSubmitted, receipt.Reason
	}
	return h.Store.PutEInvoice(ctx, rec)
}

// getEInvoice — GET /statements/{id}/einvoice. The structured document.
func (h *Handler) getEInvoice(w http.ResponseWriter, r *http.Request) {
	rec, ok := h.loadEInvoice(w, r)
	if !ok {
		return
	}
	// The archive row's own JSON plus the state, so one call answers both
	// "what does the document say" and "where has it got to".
	writeJSON(w, http.StatusOK, map[string]any{
		"statement_id": rec.StatementID,
		"state":        rec.EInvoiceState,
		"document":     rec.Document,
	})
}

// getEInvoiceXML — GET /statements/{id}/einvoice.xml. The signed archival
// copy, byte for byte as it was stored.
func (h *Handler) getEInvoiceXML(w http.ResponseWriter, r *http.Request) {
	rec, ok := h.loadEInvoice(w, r)
	if !ok {
		return
	}
	if strings.TrimSpace(rec.XML) == "" {
		writeErr(w, http.StatusNotFound, "this e-invoice has no signed XML")
		return
	}
	name := rec.InvoiceNumber
	if name == "" {
		name = rec.StatementID
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, safeFilename(name)+".xml"))
	w.Header().Set("Content-Length", strconv.Itoa(len(rec.XML)))
	if _, err := w.Write([]byte(rec.XML)); err != nil {
		slog.Warn("write e-invoice xml", "statement", rec.StatementID, "error", err)
	}
}

// loadEInvoice resolves the statement inside the caller's scope and then the
// archive row. Reading an e-invoice follows reading the statement: the scope
// check IS GetStatement's.
func (h *Handler) loadEInvoice(w http.ResponseWriter, r *http.Request) (store.EInvoiceRecord, bool) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return store.EInvoiceRecord{}, false
	}
	id := strings.TrimSuffix(r.PathValue("id"), ".xml")
	st, err := h.Store.GetStatement(r.Context(), s.Scope(), id)
	if err != nil {
		storeErr(w, err)
		return store.EInvoiceRecord{}, false
	}
	rec, err := h.Store.GetEInvoice(r.Context(), st.ID)
	if err != nil {
		storeErr(w, err)
		return store.EInvoiceRecord{}, false
	}
	return rec, true
}

// ── small helpers ──────────────────────────────────────────────────────────

func firstNonBlank(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// addDecimals is a + b at the money scale, exactly. The list subtotal is
// net + discount and is DERIVED here rather than stored, because the two
// figures it is derived from are the ones the ledger settled.
func addDecimals(a, b store.Decimal) string {
	x, okx := new(big.Rat).SetString(strings.TrimSpace(string(a)))
	y, oky := new(big.Rat).SetString(strings.TrimSpace(string(b)))
	if !okx {
		x = new(big.Rat)
	}
	if !oky {
		y = new(big.Rat)
	}
	return new(big.Rat).Add(x, y).FloatString(6)
}

// einvoiceSigningKey reads the signing key: the mounted Secret FILE wins over
// the literal, which exists only for a local run. A read failure is logged
// WITHOUT the file's contents and returns nothing, so the profile is built
// keyless and Validate refuses the issue with a sentence naming the variable
// to set — never a half-signed invoice.
func einvoiceSigningKey(cfg config.Config) []byte {
	if path := strings.TrimSpace(cfg.EInvoiceSigningKeyFile); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			slog.Error("e-invoice signing key could not be read", "file", path, "error", err)
			return nil
		}
		return raw
	}
	if key := strings.TrimSpace(cfg.EInvoiceSigningKey); key != "" {
		return []byte(key)
	}
	return nil
}

func safeFilename(s string) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		}
		return '-'
	}, s)
	name = strings.Trim(name, "-.")
	if name == "" {
		name = "einvoice"
	}
	return name
}
