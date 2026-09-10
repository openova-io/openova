package api

import (
	"errors"
	"net/http"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Billing settings (DESIGN.md §2.11) — operator-only. Today the one setting
// is the discount combination rule: how several percent discounts on one
// rated line combine at statement time.
//
// GET /api/v1/billing-settings → {discount_rule, updated_at}
// PUT /api/v1/billing-settings {discount_rule} → the stored settings;
//     400 names the accepted rules when the value is unknown;
//     audit billing.settings {discount_rule, previous}.
//
// The rule is read at statement run time and recorded on every statement
// the run writes, so changing it never rewrites an issued bill — the next
// run states the new rule.

type billingSettingsBody struct {
	DiscountRule string `json:"discount_rule"`
	// InvoicePrefix is what an issued statement's invoice number starts with
	// (DESIGN.md §8). Absent leaves the stored prefix alone, so a client that
	// only knows about the discount rule cannot silently reset it.
	InvoicePrefix *string `json:"invoice_prefix"`
	// Every key below is likewise ABSENT = unchanged (DESIGN.md §9): the
	// commercial provider and its external-ingest variant, the Sovereign's
	// tax identity and default rate, the credit-note prefix, and the
	// collections schedule.
	CommercialProvider    *string        `json:"commercial_provider"`
	ExternalIngest        *string        `json:"external_ingest"`
	TaxRate               *store.Decimal `json:"tax_rate"`
	TaxRegistrationNumber *string        `json:"tax_registration_number"`
	LegalName             *string        `json:"legal_name"`
	Address               *string        `json:"address"`
	CreditNotePrefix      *string        `json:"credit_note_prefix"`
	ReminderDays          *[]int         `json:"reminder_days"`
	EscalationDays        *int           `json:"escalation_days"`
	EscalationAction      *string        `json:"escalation_action"`
}

func (h *Handler) getBillingSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	s, err := h.Store.GetBillingSettings(r.Context())
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func (h *Handler) putBillingSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	var in billingSettingsBody
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	prev, err := h.Store.GetBillingSettings(r.Context())
	if err != nil {
		storeErr(w, err)
		return
	}
	// discount_rule stays REQUIRED, as lane 1 defined the body: a client
	// that sends nothing is answered 400, never silently kept.
	next := prev
	next.DiscountRule = in.DiscountRule
	if in.InvoicePrefix != nil {
		next.InvoicePrefix = *in.InvoicePrefix
	}
	if in.CommercialProvider != nil {
		next.CommercialProvider = *in.CommercialProvider
	}
	if in.ExternalIngest != nil {
		next.ExternalIngest = *in.ExternalIngest
	}
	if in.TaxRate != nil {
		next.TaxRate = *in.TaxRate
	}
	if in.TaxRegistrationNumber != nil {
		next.TaxRegistrationNumber = *in.TaxRegistrationNumber
	}
	if in.LegalName != nil {
		next.LegalName = *in.LegalName
	}
	if in.Address != nil {
		next.Address = *in.Address
	}
	if in.CreditNotePrefix != nil {
		next.CreditNotePrefix = *in.CreditNotePrefix
	}
	if in.ReminderDays != nil {
		next.ReminderDays = append([]int{}, (*in.ReminderDays)...)
	}
	if in.EscalationDays != nil {
		next.EscalationDays = *in.EscalationDays
	}
	if in.EscalationAction != nil {
		next.EscalationAction = *in.EscalationAction
	}
	s, err := h.Store.UpdateBillingSettings(r.Context(), next)
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeErr(w, http.StatusBadRequest, invalidMessage(err))
		return
	case err != nil:
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "billing.settings", map[string]any{"discount_rule": s.DiscountRule, "previous": prev.DiscountRule,
		"invoice_prefix": s.InvoicePrefix, "previous_invoice_prefix": prev.InvoicePrefix,
		"commercial_provider": s.CommercialProvider, "external_ingest": s.ExternalIngest, "tax_rate": s.TaxRate, "tax_registration_number": s.TaxRegistrationNumber,
		"credit_note_prefix": s.CreditNotePrefix, "reminder_days": s.ReminderDays, "escalation_days": s.EscalationDays, "escalation_action": s.EscalationAction})
	writeJSON(w, http.StatusOK, s)
}
