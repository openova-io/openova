package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lib/pq"
)

// Discount combination rules (DESIGN.md §2.11). The rule decides what
// happens when more than one PERCENT discount applies to the same rated
// line; fixed amounts always come off afterwards, whatever the rule.
//
// The constants live in store rather than rating because rating imports
// store (for Discount and RatedLine) and the setting is validated here
// before it is ever persisted.
const (
	// DiscountRuleMostSpecific — per line, the one percent discount with the
	// narrowest scope wins: a SKU-scoped discount beats a whole-bill one;
	// at the same scope the higher percent wins. Stackable discounts add on
	// top of the winner.
	DiscountRuleMostSpecific = "most-specific"
	// DiscountRuleHighest — per line, the highest applicable percent wins
	// regardless of scope. Stackable discounts add on top of the winner.
	DiscountRuleHighest = "highest"
	// DiscountRuleStack — every applicable percent is summed against the
	// untouched base (10 % + 20 % = 30 %). This was the only behaviour
	// before the rule became a setting.
	DiscountRuleStack = "stack"
	// DiscountRuleCompound — percents multiply: 10 % then 20 % takes
	// 1 − 0.9 × 0.8 = 28 % off.
	DiscountRuleCompound = "compound"

	// DefaultDiscountRule is what a fresh install and a wiped settings row
	// read as. Summing surprised the founder; the narrowest scope winning is
	// what a contract reader expects.
	DefaultDiscountRule = DiscountRuleMostSpecific
)

// DiscountRules lists every rule the setting accepts, in display order.
var DiscountRules = []string{DiscountRuleMostSpecific, DiscountRuleHighest, DiscountRuleStack, DiscountRuleCompound}

// ValidDiscountRule reports whether s names a rule.
func ValidDiscountRule(s string) bool {
	for _, r := range DiscountRules {
		if r == s {
			return true
		}
	}
	return false
}

// BillingSettings is the single-row billing configuration. Every setting
// joins as another column, never as a second row.
type BillingSettings struct {
	DiscountRule string `json:"discount_rule"`
	// InvoicePrefix is what an issued statement's invoice number starts
	// with: <prefix>-<year>-<sequence> (DESIGN.md §8). Changing it changes
	// the NEXT number only — numbers already assigned are on documents the
	// customer holds.
	InvoicePrefix string `json:"invoice_prefix"`
	// CommercialProvider says WHICH system of record owns invoicing on this
	// Sovereign: `internal` (this product) or `external` (the operator's own
	// billing system). DESIGN.md §8.10.
	CommercialProvider string `json:"commercial_provider"`
	// ExternalIngest is the external-mode variant (DESIGN.md §9.1, founder
	// refinement (b)): rated_bill exports the full TMF678 bill and never
	// numbers an invoice; summary_charge has THIS product number and
	// produce the invoice document and exports one summary charge line per
	// statement for the billing system to book.
	ExternalIngest string `json:"external_ingest"`

	// The Sovereign's tax identity — the seller block of every tax invoice
	// — and the default rate a customer's own rate overrides (DESIGN.md
	// §9.4). Snapshotted onto each invoice at issue.
	TaxRate               Decimal `json:"tax_rate"`
	TaxRegistrationNumber string  `json:"tax_registration_number"`
	LegalName             string  `json:"legal_name"`
	Address               string  `json:"address"`
	// CreditNotePrefix numbers credit notes, gaplessly per year, apart from
	// invoices (DESIGN.md §9.3).
	CreditNotePrefix string `json:"credit_note_prefix"`

	// The collections schedule (DESIGN.md §9.6): the days relative to the
	// due date a reminder goes out (negative = before), the overdue age at
	// which the escalation fires, and what it does.
	ReminderDays     []int  `json:"reminder_days"`
	EscalationDays   int    `json:"escalation_days"`
	EscalationAction string `json:"escalation_action"`

	UpdatedAt time.Time `json:"updated_at"`
}

// Commercial providers — who owns invoicing, payment and collections.
const (
	// ProviderInternal — this product invoices, records payments and runs
	// the lifecycle. The default, so an upgrade changes nothing.
	ProviderInternal = "internal"
	// ProviderExternal — the operator's billing system is the system of
	// record. We rate and export; it invoices, collects, and tells us what
	// happened. We never number an invoice for it (unless it asks for a
	// summary charge, DESIGN.md §9.1).
	ProviderExternal = "external"
)

// CommercialProviders lists the accepted values in display order.
var CommercialProviders = []string{ProviderInternal, ProviderExternal}

// ValidCommercialProvider reports whether s names a provider.
func ValidCommercialProvider(s string) bool {
	return s == ProviderInternal || s == ProviderExternal
}

// ExternalIngests lists the external-mode variants.
var ExternalIngests = []string{IngestRatedBill, IngestSummaryCharge}

// EscalationActions lists what the collections escalation may do.
var EscalationActions = []string{EscalationNotify, EscalationSuspend}

// ExternalCommercial reports whether the operator's billing system owns
// invoicing on this Sovereign.
func (b BillingSettings) ExternalCommercial() bool { return b.CommercialProvider == ProviderExternal }

// SummaryCharge reports whether external mode runs the summary-charge
// variant: we number and document the invoice, they book one line.
func (b BillingSettings) SummaryCharge() bool {
	return b.ExternalCommercial() && b.ExternalIngest == IngestSummaryCharge
}

// DefaultBillingSettings is what the migration seeds.
func DefaultBillingSettings() BillingSettings {
	return BillingSettings{
		DiscountRule: DefaultDiscountRule, InvoicePrefix: DefaultInvoicePrefix, CommercialProvider: ProviderInternal, ExternalIngest: IngestRatedBill,
		TaxRate: DefaultTaxRate, CreditNotePrefix: DefaultCreditNotePrefix,
		ReminderDays: append([]int{}, DefaultReminderDays...), EscalationDays: DefaultEscalationDays, EscalationAction: EscalationNotify,
	}
}

// Normalize trims and case-folds every enumerated value, applies the
// defaults to anything empty, and sorts / de-duplicates the reminder days.
func (b *BillingSettings) Normalize() {
	b.DiscountRule = strings.ToLower(strings.TrimSpace(b.DiscountRule))
	b.InvoicePrefix = NormalizeInvoicePrefix(b.InvoicePrefix)
	if b.InvoicePrefix == "" {
		b.InvoicePrefix = DefaultInvoicePrefix
	}
	b.CommercialProvider = strings.ToLower(strings.TrimSpace(b.CommercialProvider))
	if b.CommercialProvider == "" {
		b.CommercialProvider = ProviderInternal
	}
	b.ExternalIngest = strings.ToLower(strings.TrimSpace(b.ExternalIngest))
	if b.ExternalIngest == "" {
		b.ExternalIngest = IngestRatedBill
	}
	b.TaxRate = Decimal(strings.TrimSpace(string(b.TaxRate)))
	if b.TaxRate == "" {
		b.TaxRate = DefaultTaxRate
	}
	b.TaxRegistrationNumber = strings.TrimSpace(b.TaxRegistrationNumber)
	b.LegalName = strings.TrimSpace(b.LegalName)
	b.Address = strings.TrimSpace(b.Address)
	b.CreditNotePrefix = NormalizeInvoicePrefix(b.CreditNotePrefix)
	if b.CreditNotePrefix == "" {
		b.CreditNotePrefix = DefaultCreditNotePrefix
	}
	if b.ReminderDays == nil {
		b.ReminderDays = append([]int{}, DefaultReminderDays...)
	}
	b.ReminderDays = normalizeDays(b.ReminderDays)
	if b.EscalationDays == 0 && b.EscalationAction == "" {
		b.EscalationDays = DefaultEscalationDays
	}
	b.EscalationAction = strings.ToLower(strings.TrimSpace(b.EscalationAction))
	if b.EscalationAction == "" {
		b.EscalationAction = EscalationNotify
	}
}

// normalizeDays sorts and de-duplicates a list of day offsets.
func normalizeDays(in []int) []int {
	seen := map[int]bool{}
	out := make([]int, 0, len(in))
	for _, d := range in {
		if seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	sort.Ints(out)
	return out
}

// Validate reports the first rule the settings break, wrapped in ErrInvalid.
func (b BillingSettings) Validate() error {
	if !ValidDiscountRule(b.DiscountRule) {
		return fmt.Errorf("%w: discount_rule must be one of %s", ErrInvalid, strings.Join(DiscountRules, ", "))
	}
	if !ValidInvoicePrefix(b.InvoicePrefix) {
		return fmt.Errorf("%w: invoice_prefix must be 1-12 upper-case letters, digits or dashes, starting with a letter or digit", ErrInvalid)
	}
	if !ValidCommercialProvider(b.CommercialProvider) {
		return fmt.Errorf("%w: commercial_provider must be %s", ErrInvalid, strings.Join(CommercialProviders, " or "))
	}
	if !oneOf(b.ExternalIngest, ExternalIngests) {
		return fmt.Errorf("%w: external_ingest must be %s", ErrInvalid, strings.Join(ExternalIngests, " or "))
	}
	if _, err := b.TaxRate.MarshalJSON(); err != nil || !validTaxRate(b.TaxRate) {
		return fmt.Errorf("%w: tax_rate must be a fraction between 0 and 1 (0.05 is 5%%)", ErrInvalid)
	}
	if !ValidInvoicePrefix(b.CreditNotePrefix) {
		return fmt.Errorf("%w: credit_note_prefix must be 1-12 upper-case letters, digits or dashes, starting with a letter or digit", ErrInvalid)
	}
	if b.CreditNotePrefix == b.InvoicePrefix {
		return fmt.Errorf("%w: credit_note_prefix must differ from invoice_prefix, so a credit note is never mistaken for an invoice", ErrInvalid)
	}
	for _, d := range b.ReminderDays {
		if d < -365 || d > 3650 {
			return fmt.Errorf("%w: reminder_days must be between -365 (before the due date) and 3650 (after)", ErrInvalid)
		}
	}
	if b.EscalationDays < 0 || b.EscalationDays > 3650 {
		return fmt.Errorf("%w: escalation_days must be between 0 and 3650", ErrInvalid)
	}
	if !oneOf(b.EscalationAction, EscalationActions) {
		return fmt.Errorf("%w: escalation_action must be %s", ErrInvalid, strings.Join(EscalationActions, " or "))
	}
	return nil
}

// GetBillingSettings reads the single settings row. A missing row (the
// migration seeds it; a wiped table can lose it) reads as the defaults — a
// single-row configuration is never "not found".
func (s *Store) GetBillingSettings(ctx context.Context) (BillingSettings, error) {
	return billingSettingsFrom(ctx, s.db.QueryRowContext(ctx, billingSettingsQuery))
}

const billingSettingsQuery = `SELECT discount_rule, invoice_prefix, commercial_provider, external_ingest, tax_rate::text, tax_registration_number, legal_name, address, credit_note_prefix,
	reminder_days, escalation_days, escalation_action, updated_at FROM billing_settings WHERE id = 1`

// billingSettingsTx reads the settings inside a transaction — the issuing
// path needs the invoice prefix in the same transaction that takes the
// number.
func billingSettingsTx(ctx context.Context, tx *sql.Tx) (BillingSettings, error) {
	return billingSettingsFrom(ctx, tx.QueryRowContext(ctx, billingSettingsQuery))
}

func billingSettingsFrom(_ context.Context, row interface{ Scan(...any) error }) (BillingSettings, error) {
	var b BillingSettings
	var rate string
	var days []int64
	err := row.Scan(&b.DiscountRule, &b.InvoicePrefix, &b.CommercialProvider, &b.ExternalIngest, &rate, &b.TaxRegistrationNumber, &b.LegalName, &b.Address, &b.CreditNotePrefix,
		pq.Array(&days), &b.EscalationDays, &b.EscalationAction, &b.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultBillingSettings(), nil
	}
	if err != nil {
		return b, mapErr(err)
	}
	b.TaxRate = Decimal(rate)
	b.ReminderDays = make([]int, 0, len(days))
	for _, d := range days {
		b.ReminderDays = append(b.ReminderDays, int(d))
	}
	b.UpdatedAt = b.UpdatedAt.UTC()
	return b, nil
}

// UpdateBillingSettings validates and replaces the settings row. An unknown
// value is ErrInvalid; the caller answers 400 with the message.
func (s *Store) UpdateBillingSettings(ctx context.Context, in BillingSettings) (BillingSettings, error) {
	in.Normalize()
	if err := in.Validate(); err != nil {
		return BillingSettings{}, err
	}
	days := make([]int64, 0, len(in.ReminderDays))
	for _, d := range in.ReminderDays {
		days = append(days, int64(d))
	}
	args := []any{in.DiscountRule, in.InvoicePrefix, in.CommercialProvider, in.ExternalIngest, string(in.TaxRate), in.TaxRegistrationNumber, in.LegalName, in.Address, in.CreditNotePrefix,
		pq.Array(days), in.EscalationDays, in.EscalationAction}
	res, err := s.db.ExecContext(ctx, `UPDATE billing_settings SET discount_rule = $1, invoice_prefix = $2, commercial_provider = $3, external_ingest = $4, tax_rate = $5::numeric,
		tax_registration_number = $6, legal_name = $7, address = $8, credit_note_prefix = $9, reminder_days = $10, escalation_days = $11, escalation_action = $12, updated_at = now() WHERE id = 1`, args...)
	if err != nil {
		return BillingSettings{}, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// The migration seeds the row; a missing one is a wiped table.
		if _, err := s.db.ExecContext(ctx, `INSERT INTO billing_settings (id, discount_rule, invoice_prefix, commercial_provider, external_ingest, tax_rate, tax_registration_number, legal_name, address, credit_note_prefix, reminder_days, escalation_days, escalation_action)
			VALUES (1, $1, $2, $3, $4, $5::numeric, $6, $7, $8, $9, $10, $11, $12)`, args...); err != nil {
			return BillingSettings{}, mapErr(err)
		}
	}
	return s.GetBillingSettings(ctx)
}
