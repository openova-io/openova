package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lib/pq"
)

// Tax rules by country, region and category (DESIGN.md §17, EPIC #6867).
//
// Until now tax was ONE rate on billing settings, with a per-customer
// exemption and a per-customer override. That is enough for a Sovereign
// selling one kind of service inside one country and nothing else. It cannot
// express what a tax authority actually asks an issuer for:
//
//   - a rate that DIFFERS BY CATEGORY — storage zero-rated while compute is
//     standard-rated is an ordinary thing for a tax authority to require;
//   - a rate that CHANGES ON A DATE — a rule carries validity, so an invoice
//     for August is rated at August's rate whatever today's rate is;
//   - REVERSE CHARGE — a registered business in another country is invoiced
//     at zero with a legally required note, and accounts for the tax itself;
//   - an EXEMPTION CERTIFICATE that EXPIRES — an expired certificate is not
//     an exemption, and falling back silently is how an issuer under-charges
//     tax and carries the liability;
//   - SEVERAL RATES ON ONE INVOICE, with a tax summary block by rate.
//
// The single rate is not retired: a customer with no country and a Sovereign
// with no rules resolves to exactly the figure EffectiveTaxRate produced
// before this file existed, which is what keeps every statement rated under
// the old model reproducible.

// ---------------------------------------------------------------------------
// migration
// ---------------------------------------------------------------------------

// taxMigrationSQL is one transaction, idempotent against a database that
// already carries the shape. Appended at the very END of the migrations
// slice: migrations are positional, so an entry inserted above a database's
// recorded version is silently skipped. Located by content as MigrationTax.
const taxMigrationSQL = `
-- A tax rule: what rate applies, to whom, to which category of supply, and
-- when. country is ISO 3166-1 alpha-2. A NULL region is the whole country;
-- an empty category is every category. kind is what the rule DOES, which is
-- not derivable from the rate: zero-rated and exempt are both 0 % and are
-- different things on a tax return.
CREATE TABLE IF NOT EXISTS tax_rules (
	id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	name TEXT NOT NULL,
	country TEXT NOT NULL,
	region TEXT,
	category TEXT NOT NULL DEFAULT '',
	rate NUMERIC(6,4) NOT NULL DEFAULT 0 CHECK (rate >= 0 AND rate <= 1),
	kind TEXT NOT NULL DEFAULT 'standard' CHECK (kind IN ('standard','zero_rated','exempt','reverse_charge','out_of_state')),
	note TEXT NOT NULL DEFAULT '',
	effective_from DATE NOT NULL DEFAULT DATE '2000-01-01',
	effective_to DATE,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CONSTRAINT tax_rules_country_check CHECK (country ~ '^[A-Z]{2}$'),
	CONSTRAINT tax_rules_validity_check CHECK (effective_to IS NULL OR effective_to > effective_from)
);
-- One rule per (country, region, category) per start date: two rules that
-- both start on the same day for the same supply is an operator mistake the
-- resolver cannot arbitrate, so the schema refuses it.
CREATE UNIQUE INDEX IF NOT EXISTS tax_rules_key_idx ON tax_rules (country, COALESCE(region, ''), category, effective_from);
CREATE INDEX IF NOT EXISTS tax_rules_country_idx ON tax_rules (country, effective_from);

-- The tax CATEGORY of a supply, per SKU. sku is either an exact SKU or a
-- prefix ending in '*' (evs.* — every block-storage meter), so a whole
-- family is categorised in one row and storage and compute can differ. A SKU
-- matched by nothing is the default category ('').
CREATE TABLE IF NOT EXISTS tax_categories (
	sku TEXT PRIMARY KEY,
	category TEXT NOT NULL,
	note TEXT NOT NULL DEFAULT '',
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CONSTRAINT tax_categories_category_check CHECK (category <> '')
);

-- The customer side of the profile. tax_registration_number, tax_exempt,
-- tax_exempt_reason and tax_rate are already there (DESIGN.md §9.4); what a
-- rule needs on top is WHERE the customer is registered, whether it is a
-- registered BUSINESS (reverse charge applies to a business, never to a
-- consumer), and the exemption CERTIFICATE behind tax_exempt.
ALTER TABLE customers ADD COLUMN IF NOT EXISTS tax_country TEXT NOT NULL DEFAULT '';
ALTER TABLE customers DROP CONSTRAINT IF EXISTS customers_tax_country_check;
ALTER TABLE customers ADD CONSTRAINT customers_tax_country_check CHECK (tax_country = '' OR tax_country ~ '^[A-Z]{2}$');
ALTER TABLE customers ADD COLUMN IF NOT EXISTS tax_region TEXT NOT NULL DEFAULT '';
ALTER TABLE customers ADD COLUMN IF NOT EXISTS tax_business BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE customers ADD COLUMN IF NOT EXISTS tax_exemption_number TEXT NOT NULL DEFAULT '';
ALTER TABLE customers ADD COLUMN IF NOT EXISTS tax_exemption_expires_on DATE;
ALTER TABLE customers ADD COLUMN IF NOT EXISTS tax_exemption_scan_ref TEXT NOT NULL DEFAULT '';

-- The Sovereign's own registration COUNTRY. The seller's legal name, address
-- and registration number are already on billing_settings; the country is
-- what decides whether a customer is domestic or cross-border, so without it
-- reverse charge cannot be determined at all. Empty = not configured, and
-- no cross-border determination is made.
ALTER TABLE billing_settings ADD COLUMN IF NOT EXISTS tax_country TEXT NOT NULL DEFAULT '';
ALTER TABLE billing_settings DROP CONSTRAINT IF EXISTS billing_settings_tax_country_check;
ALTER TABLE billing_settings ADD CONSTRAINT billing_settings_tax_country_check CHECK (tax_country = '' OR tax_country ~ '^[A-Z]{2}$');

-- The per-rule tax summary of a statement, written by the rating run and
-- never recomputed afterwards. NULL on every statement rated before this
-- migration, which is what makes the single-rate reading of those statements
-- (subtotal x tax_rate) still the only reading they have.
ALTER TABLE statements ADD COLUMN IF NOT EXISTS tax_lines JSONB;

-- Every tax determination that was NOT the plain reading of the rule table:
-- an expired exemption certificate above all, a reverse-charge finding, a
-- per-customer rate override. Written by the rating run and copied onto the
-- invoice's snapshot at issue, because "why was this customer charged when
-- it holds an exemption" is a question asked years later by someone who was
-- not there.
ALTER TABLE statements ADD COLUMN IF NOT EXISTS tax_audit JSONB;

-- The tax position of one rated line: the category it was placed in and the
-- rule that priced it. Both empty on lines rated before this migration.
ALTER TABLE rated_lines ADD COLUMN IF NOT EXISTS tax_category TEXT NOT NULL DEFAULT '';
ALTER TABLE rated_lines ADD COLUMN IF NOT EXISTS tax_rule_id TEXT NOT NULL DEFAULT '';
`

// MigrationTax is the schema_migrations version of the tax-rules migration,
// located by content so a migration appended after it cannot move it.
var MigrationTax = func() int {
	for i, m := range migrations {
		if m == taxMigrationSQL {
			return i + 1
		}
	}
	return len(migrations)
}()

// ---------------------------------------------------------------------------
// model
// ---------------------------------------------------------------------------

// What a tax rule DOES. The rate alone cannot say it: zero-rated and exempt
// are both 0 % and are different lines on a tax return, and reverse charge is
// 0 % to this issuer and taxable to the buyer.
const (
	// TaxKindStandard — the ordinary rate for the country and category.
	TaxKindStandard = "standard"
	// TaxKindZeroRated — taxable at 0 %; the supply is IN the tax system.
	TaxKindZeroRated = "zero_rated"
	// TaxKindExempt — outside the tax system; no tax is charged and none is
	// reclaimable.
	TaxKindExempt = "exempt"
	// TaxKindReverseCharge — the buyer accounts for the tax. The invoice
	// carries a zero-amount tax line and the legally required note.
	TaxKindReverseCharge = "reverse_charge"
	// TaxKindOutOfState — the supply falls outside the issuer's taxing
	// jurisdiction (an export of services, a buyer in a state the issuer is
	// not registered in).
	TaxKindOutOfState = "out_of_state"
)

// TaxKinds lists every kind, in display order.
var TaxKinds = []string{TaxKindStandard, TaxKindZeroRated, TaxKindExempt, TaxKindReverseCharge, TaxKindOutOfState}

// ValidTaxKind reports whether s names a kind.
func ValidTaxKind(s string) bool { return oneOf(s, TaxKinds) }

// DefaultReverseChargeNote is printed when a reverse-charge determination is
// made and the operator has authored no rule of its own to carry the wording.
// It is deliberately the generic sentence: the exact words a tax authority
// requires belong in the rule's note, which the operator writes.
const DefaultReverseChargeNote = "Reverse charge: the recipient is liable to account for the tax on this supply."

// ReverseChargeRuleID is the rule id recorded on a statement whose zero tax
// line came from the cross-border determination rather than a stored rule.
const ReverseChargeRuleID = "reverse-charge"

// TaxRule is one rate, for one country (optionally one region), for one
// category of supply (empty = every category), valid over a date range.
type TaxRule struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Country  string  `json:"country"`
	Region   string  `json:"region,omitempty"`
	Category string  `json:"category,omitempty"`
	Rate     Decimal `json:"rate"`
	Kind     string  `json:"kind"`
	// Note is the sentence the invoice must carry when this rule applies —
	// the reverse-charge wording, the exemption article, the zero-rating
	// provision. Free text: only the tax authority knows what it must say.
	Note string `json:"note,omitempty"`
	// EffectiveFrom / EffectiveTo are YYYY-MM-DD; an empty EffectiveTo is
	// open-ended.
	EffectiveFrom string    `json:"effective_from"`
	EffectiveTo   string    `json:"effective_to,omitempty"`
	CreatedAt     time.Time `json:"created_at,omitempty"`
	UpdatedAt     time.Time `json:"updated_at,omitempty"`
}

// TaxCategoryRule places a SKU, or a family of SKUs, in a tax category.
type TaxCategoryRule struct {
	// SKU is an exact SKU (k8s.vcpu) or a prefix ending in '*' (evs.*).
	SKU       string    `json:"sku"`
	Category  string    `json:"category"`
	Note      string    `json:"note,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// TaxLine is ONE RULE's contribution to a statement: the taxable base after
// discounts, and the tax on it. A statement carries one per rule that
// applied, which is the tax summary block by rate an invoice with several
// rates has to show. Frozen with the statement and never recomputed.
type TaxLine struct {
	RuleID   string  `json:"rule_id,omitempty"`
	RuleName string  `json:"rule_name,omitempty"`
	Kind     string  `json:"kind"`
	Category string  `json:"category,omitempty"`
	Rate     Decimal `json:"rate"`
	Base     Decimal `json:"base"`
	Tax      Decimal `json:"tax"`
	Note     string  `json:"note,omitempty"`
}

// TaxParty is the buyer's tax position, as the resolver reads it.
type TaxParty struct {
	Country            string
	Region             string
	RegistrationNumber string
	// Business marks a registered business buyer. Reverse charge applies to
	// a business and never to a consumer, so this is not derivable from the
	// presence of a registration number alone.
	Business bool
	// Exempt and ExemptReason are the existing per-customer exemption; the
	// certificate fields are what make it auditable. ExemptionExpires is a
	// DAY (YYYY-MM-DD), empty = no expiry recorded.
	Exempt           bool
	ExemptReason     string
	ExemptionNumber  string
	ExemptionExpires string
	ExemptionScanRef string
	// RateOverride is the per-customer rate (customers.tax_rate). It
	// overrides the RATE of whatever rule resolved, never the KIND.
	RateOverride *Decimal
}

// TaxPartyOf reads a customer's tax position.
func TaxPartyOf(c Customer) TaxParty {
	return TaxParty{
		Country:            strings.ToUpper(strings.TrimSpace(c.TaxCountry)),
		Region:             strings.TrimSpace(c.TaxRegion),
		RegistrationNumber: strings.TrimSpace(c.TaxRegistrationNumber),
		Business:           c.TaxBusiness,
		Exempt:             c.TaxExempt,
		ExemptReason:       c.TaxExemptReason,
		ExemptionNumber:    strings.TrimSpace(c.TaxExemptionNumber),
		ExemptionExpires:   derefDay(c.TaxExemptionExpiresOn),
		ExemptionScanRef:   strings.TrimSpace(c.TaxExemptionScanRef),
		RateOverride:       c.TaxRate,
	}
}

// TaxEngine resolves a rule for a (party, category, date). It is a VALUE
// built once per rating run from the rules table, the category table and the
// Sovereign's own country, so every statement of a run resolves against the
// same picture.
type TaxEngine struct {
	// SellerCountry is the Sovereign's registration country. Empty = not
	// configured, and no cross-border determination is made.
	SellerCountry string
	// DefaultRate is the single rate of billing settings — what a Sovereign
	// that has authored no rule at all still charges. This is the field that
	// keeps the pre-§17 behaviour exactly intact.
	DefaultRate Decimal
	Rules       []TaxRule
	Categories  []TaxCategoryRule
}

// TaxDecision is one resolution: the rule that applied and why.
type TaxDecision struct {
	Rule TaxLine
	// Audit records every determination that was NOT the plain reading of
	// the rule table — an expired certificate, a reverse-charge finding, a
	// per-customer override. It is frozen onto the invoice's tax snapshot,
	// because "why is this invoice zero-rated" is a question asked years
	// later by someone who was not there.
	Audit []string
}

// CategoryFor places a SKU. An exact row wins; otherwise the LONGEST
// matching prefix row wins, so evs.ssd.* beats evs.*. Nothing matching is
// the default category, "".
func (e *TaxEngine) CategoryFor(sku string) string {
	sku = strings.TrimSpace(sku)
	best, bestLen := "", -1
	for _, c := range e.Categories {
		key := strings.TrimSpace(c.SKU)
		switch {
		case key == sku:
			return c.Category
		case strings.HasSuffix(key, "*"):
			prefix := strings.TrimSuffix(key, "*")
			if strings.HasPrefix(sku, prefix) && len(prefix) > bestLen {
				best, bestLen = c.Category, len(prefix)
			}
		}
	}
	return best
}

// Resolve decides the tax position of one category of supply for one buyer at
// one date. The order is fixed and is the order DESIGN.md §17 states:
//
//  1. the RULE TABLE — country, then region, then category, then validity;
//  2. REVERSE CHARGE — a registered business in another country;
//  3. the EXEMPTION CERTIFICATE, and its expiry;
//  4. the per-customer RATE OVERRIDE.
//
// Each later step can only replace what an earlier one decided, and every
// replacement writes an audit line. A Sovereign with no rules and a buyer
// with no country reach the end holding the single default rate, which is
// what every statement rated before §17 was rated at.
func (e *TaxEngine) Resolve(p TaxParty, category string, at time.Time) TaxDecision {
	d := TaxDecision{Rule: TaxLine{Kind: TaxKindStandard, Rate: normalizedRate(e.DefaultRate), Category: category}}

	// 1. The rule table. The buyer's registration country decides; a buyer
	// with no country of its own is treated as domestic, which is what a
	// Sovereign selling only at home has always assumed.
	country := p.Country
	if country == "" {
		country = strings.ToUpper(strings.TrimSpace(e.SellerCountry))
	}
	if rule, ok := e.match(country, p.Region, category, at); ok {
		d.Rule = TaxLine{RuleID: rule.ID, RuleName: rule.Name, Kind: rule.Kind, Category: category, Rate: normalizedRate(rule.Rate), Note: rule.Note}
		if rule.Kind != TaxKindStandard {
			d.Rule.Rate = "0.0000"
		}
	}

	// 2. Reverse charge. A REGISTERED BUSINESS in ANOTHER country accounts
	// for the tax itself: the invoice carries a zero tax line and the note.
	// Both countries must be known — a determination made against an unknown
	// seller country would be a guess.
	seller := strings.ToUpper(strings.TrimSpace(e.SellerCountry))
	if seller != "" && p.Country != "" && p.Country != seller && p.Business && p.RegistrationNumber != "" {
		note := DefaultReverseChargeNote
		id, name := ReverseChargeRuleID, "Reverse charge"
		if rule, ok := e.matchKind(p.Country, p.Region, category, at, TaxKindReverseCharge); ok {
			id, name = rule.ID, rule.Name
			if strings.TrimSpace(rule.Note) != "" {
				note = rule.Note
			}
		}
		d.Audit = append(d.Audit, fmt.Sprintf("reverse charge: the buyer is a business registered in %s (%s) and the issuer is registered in %s, so the buyer accounts for the tax",
			p.Country, p.RegistrationNumber, seller))
		d.Rule = TaxLine{RuleID: id, RuleName: name, Kind: TaxKindReverseCharge, Category: category, Rate: "0.0000", Note: note}
		return d
	}

	// 3. The exemption certificate. An EXPIRED certificate is not an
	// exemption: the standard rate stands and the audit says why, because
	// the alternative is an issuer quietly carrying the liability.
	if p.Exempt {
		// The comparison is on the DAY, not the instant: the column is a
		// DATE, an expiry is announced as a day, and a certificate is valid
		// THROUGH the day it names. It has lapsed only once the resolution
		// date is past that day.
		if p.ExemptionExpires != "" && p.ExemptionExpires < at.UTC().Format("2006-01-02") {
			d.Audit = append(d.Audit, fmt.Sprintf("exemption certificate %s expired on %s, before %s: the standard rate applies and the exemption was NOT given",
				certificateLabel(p), p.ExemptionExpires, at.UTC().Format("2006-01-02")))
		} else {
			reason := strings.TrimSpace(p.ExemptReason)
			if reason == "" {
				reason = "exempt customer"
			}
			note := reason
			if p.ExemptionNumber != "" {
				note = reason + " (certificate " + p.ExemptionNumber + ")"
			}
			d.Rule = TaxLine{RuleID: d.Rule.RuleID, RuleName: d.Rule.RuleName, Kind: TaxKindExempt, Category: category, Rate: "0.0000", Note: note}
			return d
		}
	}

	// 4. The per-customer rate override. It replaces the RATE and keeps the
	// rule: a customer negotiated a rate, it did not change what the supply
	// is. Never applied to a zero-rated, exempt or reverse-charge line — a
	// rate on those would contradict the determination.
	if p.RateOverride != nil && d.Rule.Kind == TaxKindStandard {
		override := normalizedRate(*p.RateOverride)
		if override != d.Rule.Rate {
			d.Audit = append(d.Audit, fmt.Sprintf("customer rate override %s applied instead of %s", override, d.Rule.Rate))
			d.Rule.Rate = override
		}
	}
	return d
}

func certificateLabel(p TaxParty) string {
	if p.ExemptionNumber != "" {
		return p.ExemptionNumber
	}
	return "(unnumbered)"
}

// match finds the rule that governs (country, region, category) at a date.
// Specificity beats recency: a region rule beats a country-wide rule, an
// exact category beats the catch-all, and only among equally specific rules
// does the latest effective_from win.
//
// A REVERSE-CHARGE rule is deliberately invisible here. Reverse charge is a
// property of the BUYER — a registered business in another country — not of
// the supply, so a rule of that kind must never be picked up by the generic
// country match. Without this exclusion a CONSUMER in that country would be
// invoiced at zero, which is tax the issuer owes and never collected. Step 2
// of Resolve reaches those rules through matchKind, and only after the
// business test has passed.
func (e *TaxEngine) match(country, region, category string, at time.Time) (TaxRule, bool) {
	return e.matchKind(country, region, category, at, "")
}

// matchKind is match restricted to one rule kind ("" = any).
func (e *TaxEngine) matchKind(country, region, category string, at time.Time, kind string) (TaxRule, bool) {
	country = strings.ToUpper(strings.TrimSpace(country))
	region = strings.TrimSpace(region)
	category = strings.TrimSpace(category)
	var best TaxRule
	bestScore, found := -1, false
	for _, r := range e.Rules {
		if !strings.EqualFold(r.Country, country) {
			continue
		}
		switch {
		case kind != "":
			if r.Kind != kind {
				continue
			}
		case r.Kind == TaxKindReverseCharge:
			// Only the buyer test selects these — see match's comment.
			continue
		}
		if r.Region != "" && !strings.EqualFold(r.Region, region) {
			continue
		}
		if r.Category != "" && r.Category != category {
			continue
		}
		if !ruleEffectiveAt(r, at) {
			continue
		}
		score := 0
		if r.Region != "" {
			score += 2
		}
		if r.Category != "" {
			score++
		}
		switch {
		case score > bestScore:
		case score < bestScore:
			continue
		case r.EffectiveFrom <= best.EffectiveFrom:
			continue
		}
		best, bestScore, found = r, score, true
	}
	return best, found
}

// ruleEffectiveAt reports whether a rule governs the given instant. The
// comparison is on the DATE, in UTC, because a rate change is announced as a
// date and not as a moment.
func ruleEffectiveAt(r TaxRule, at time.Time) bool {
	day := at.UTC().Format("2006-01-02")
	if r.EffectiveFrom != "" && day < r.EffectiveFrom {
		return false
	}
	if r.EffectiveTo != "" && day >= r.EffectiveTo {
		return false
	}
	return true
}

// normalizedRate renders a rate at the column's scale so two spellings of the
// same rate ("0.05", "0.0500") group into ONE tax summary row.
func normalizedRate(d Decimal) Decimal {
	s := strings.TrimSpace(string(d))
	if s == "" {
		return "0.0000"
	}
	return Decimal(ratOf(Decimal(s)).FloatString(4))
}

// decodeTaxLines reads the frozen per-rule summary. Unreadable JSON reads as
// "no summary", never as an error: the statement's own tax total is the
// authority, and a summary that cannot be parsed must not cost a customer
// its invoice.
func decodeTaxLines(raw []byte) ([]TaxLine, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var out []TaxLine
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, nil
	}
	return out, nil
}

// taxLinesJSON stores the summary, or SQL NULL when there is none — an empty
// array and "rated before §17" must not read the same.
func taxLinesJSON(lines []TaxLine) any {
	if len(lines) == 0 {
		return nil
	}
	b, err := json.Marshal(lines)
	if err != nil {
		return nil
	}
	return b
}

// ---------------------------------------------------------------------------
// store
// ---------------------------------------------------------------------------

const taxRuleColumns = `id, name, country, COALESCE(region, ''), category, rate::text, kind, note,
	to_char(effective_from, 'YYYY-MM-DD'), COALESCE(to_char(effective_to, 'YYYY-MM-DD'), ''), created_at, updated_at`

func scanTaxRule(row interface{ Scan(...any) error }) (TaxRule, error) {
	var r TaxRule
	var rate string
	if err := row.Scan(&r.ID, &r.Name, &r.Country, &r.Region, &r.Category, &rate, &r.Kind, &r.Note, &r.EffectiveFrom, &r.EffectiveTo, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return r, mapErr(err)
	}
	r.Rate = Decimal(rate)
	r.CreatedAt, r.UpdatedAt = r.CreatedAt.UTC(), r.UpdatedAt.UTC()
	return r, nil
}

// ListTaxRules returns every rule: country, then region, then category, then
// start date.
func (s *Store) ListTaxRules(ctx context.Context) ([]TaxRule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+taxRuleColumns+` FROM tax_rules ORDER BY country, COALESCE(region, ''), category, effective_from`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []TaxRule{}
	for rows.Next() {
		r, err := scanTaxRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetTaxRule reads one rule.
func (s *Store) GetTaxRule(ctx context.Context, id string) (TaxRule, error) {
	return scanTaxRule(s.db.QueryRowContext(ctx, `SELECT `+taxRuleColumns+` FROM tax_rules WHERE id = $1`, id))
}

// Normalize trims and case-folds a rule and fills its defaults.
func (r *TaxRule) Normalize() {
	r.Name = strings.TrimSpace(r.Name)
	r.Country = strings.ToUpper(strings.TrimSpace(r.Country))
	r.Region = strings.TrimSpace(r.Region)
	r.Category = strings.TrimSpace(r.Category)
	r.Kind = strings.ToLower(strings.TrimSpace(r.Kind))
	if r.Kind == "" {
		r.Kind = TaxKindStandard
	}
	r.Note = strings.TrimSpace(r.Note)
	r.Rate = Decimal(strings.TrimSpace(string(r.Rate)))
	if r.Rate == "" {
		r.Rate = "0"
	}
	r.EffectiveFrom = strings.TrimSpace(r.EffectiveFrom)
	if r.EffectiveFrom == "" {
		r.EffectiveFrom = "2000-01-01"
	}
	r.EffectiveTo = strings.TrimSpace(r.EffectiveTo)
}

// Validate reports the first rule a tax rule breaks, wrapped in ErrInvalid.
func (r TaxRule) Validate() error {
	if r.Name == "" {
		return fmt.Errorf("%w: name is required", ErrInvalid)
	}
	if len(r.Country) != 2 || !lettersOnly(r.Country) {
		return fmt.Errorf("%w: country must be a two-letter ISO 3166-1 alpha-2 code", ErrInvalid)
	}
	if !ValidTaxKind(r.Kind) {
		return fmt.Errorf("%w: kind must be one of %s", ErrInvalid, strings.Join(TaxKinds, ", "))
	}
	if !validTaxRate(r.Rate) {
		return fmt.Errorf("%w: rate must be a fraction between 0 and 1 (0.05 is 5%%)", ErrInvalid)
	}
	if r.Kind != TaxKindStandard && ratOf(r.Rate).Sign() != 0 {
		return fmt.Errorf("%w: a %s rule charges nothing, so its rate must be 0", ErrInvalid, r.Kind)
	}
	if _, err := time.Parse("2006-01-02", r.EffectiveFrom); err != nil {
		return fmt.Errorf("%w: effective_from must be YYYY-MM-DD", ErrInvalid)
	}
	if r.EffectiveTo != "" {
		to, err := time.Parse("2006-01-02", r.EffectiveTo)
		if err != nil {
			return fmt.Errorf("%w: effective_to must be YYYY-MM-DD", ErrInvalid)
		}
		from, _ := time.Parse("2006-01-02", r.EffectiveFrom)
		if !to.After(from) {
			return fmt.Errorf("%w: effective_to must be after effective_from", ErrInvalid)
		}
	}
	return nil
}

func lettersOnly(s string) bool {
	for _, c := range s {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	return len(s) > 0
}

// CreateTaxRule inserts a rule.
func (s *Store) CreateTaxRule(ctx context.Context, in TaxRule) (TaxRule, error) {
	in.Normalize()
	if err := in.Validate(); err != nil {
		return TaxRule{}, err
	}
	var id string
	err := s.db.QueryRowContext(ctx, `INSERT INTO tax_rules (name, country, region, category, rate, kind, note, effective_from, effective_to)
		VALUES ($1, $2, $3, $4, $5::numeric, $6, $7, $8::date, $9::date) RETURNING id`,
		in.Name, in.Country, nullIfEmpty(in.Region), in.Category, string(in.Rate), in.Kind, in.Note, in.EffectiveFrom, nullIfEmpty(in.EffectiveTo)).Scan(&id)
	if err != nil {
		return TaxRule{}, taxRuleErr(err)
	}
	return s.GetTaxRule(ctx, id)
}

// UpdateTaxRule replaces a rule.
func (s *Store) UpdateTaxRule(ctx context.Context, id string, in TaxRule) (TaxRule, error) {
	in.Normalize()
	if err := in.Validate(); err != nil {
		return TaxRule{}, err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE tax_rules SET name = $2, country = $3, region = $4, category = $5, rate = $6::numeric, kind = $7, note = $8,
		effective_from = $9::date, effective_to = $10::date, updated_at = now() WHERE id = $1`,
		id, in.Name, in.Country, nullIfEmpty(in.Region), in.Category, string(in.Rate), in.Kind, in.Note, in.EffectiveFrom, nullIfEmpty(in.EffectiveTo))
	if err != nil {
		return TaxRule{}, taxRuleErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return TaxRule{}, ErrNotFound
	}
	return s.GetTaxRule(ctx, id)
}

// DeleteTaxRule removes a rule. Statements already rated under it keep their
// FROZEN tax lines — the rule id on an issued invoice is a record of what
// happened, not a foreign key that must still resolve.
func (s *Store) DeleteTaxRule(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM tax_rules WHERE id = $1`, id)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func taxRuleErr(err error) error {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code.Name() == "unique_violation" {
		return fmt.Errorf("%w: a rule for this country, region and category already starts on that date", ErrConflict)
	}
	return mapErr(err)
}

// ListTaxCategories returns the SKU-to-category placements.
func (s *Store) ListTaxCategories(ctx context.Context) ([]TaxCategoryRule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT sku, category, note, updated_at FROM tax_categories ORDER BY sku`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []TaxCategoryRule{}
	for rows.Next() {
		var c TaxCategoryRule
		if err := rows.Scan(&c.SKU, &c.Category, &c.Note, &c.UpdatedAt); err != nil {
			return nil, mapErr(err)
		}
		c.UpdatedAt = c.UpdatedAt.UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// PutTaxCategory places a SKU (or a SKU family) in a category.
func (s *Store) PutTaxCategory(ctx context.Context, in TaxCategoryRule) (TaxCategoryRule, error) {
	in.SKU = strings.TrimSpace(in.SKU)
	in.Category = strings.TrimSpace(in.Category)
	in.Note = strings.TrimSpace(in.Note)
	if in.SKU == "" {
		return TaxCategoryRule{}, fmt.Errorf("%w: sku is required (an exact SKU, or a prefix ending in *)", ErrInvalid)
	}
	if in.Category == "" {
		return TaxCategoryRule{}, fmt.Errorf("%w: category is required; delete the row to return the SKU to the default category", ErrInvalid)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO tax_categories (sku, category, note) VALUES ($1, $2, $3)
		ON CONFLICT (sku) DO UPDATE SET category = EXCLUDED.category, note = EXCLUDED.note, updated_at = now()`, in.SKU, in.Category, in.Note); err != nil {
		return TaxCategoryRule{}, mapErr(err)
	}
	var out TaxCategoryRule
	if err := s.db.QueryRowContext(ctx, `SELECT sku, category, note, updated_at FROM tax_categories WHERE sku = $1`, in.SKU).
		Scan(&out.SKU, &out.Category, &out.Note, &out.UpdatedAt); err != nil {
		return TaxCategoryRule{}, mapErr(err)
	}
	out.UpdatedAt = out.UpdatedAt.UTC()
	return out, nil
}

// DeleteTaxCategory returns a SKU to the default category.
func (s *Store) DeleteTaxCategory(ctx context.Context, sku string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM tax_categories WHERE sku = $1`, strings.TrimSpace(sku))
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// TaxEngineFor builds the resolver from the rules, the categories and the
// Sovereign's own identity. Read ONCE per rating run.
func (s *Store) TaxEngineFor(ctx context.Context, settings BillingSettings) (*TaxEngine, error) {
	rules, err := s.ListTaxRules(ctx)
	if err != nil {
		return nil, err
	}
	cats, err := s.ListTaxCategories(ctx)
	if err != nil {
		return nil, err
	}
	// Longest key first, so CategoryFor's scan is stable whatever order the
	// rows came back in.
	sort.SliceStable(cats, func(i, j int) bool { return len(cats[i].SKU) > len(cats[j].SKU) })
	return &TaxEngine{SellerCountry: settings.TaxCountry, DefaultRate: settings.TaxRate, Rules: rules, Categories: cats}, nil
}

// derefDay reads an optional day, "" when absent.
func derefDay(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}

// nullIfEmpty renders "" as SQL NULL.
func nullIfEmpty(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

// taxLinesTx reads a statement's frozen per-rule tax summary and its
// determination audit inside a transaction — what the snapshot copies at
// issue.
func taxLinesTx(ctx context.Context, tx *sql.Tx, statementID string) ([]TaxLine, []string, error) {
	var lines, audit []byte
	if err := tx.QueryRowContext(ctx, `SELECT tax_lines, tax_audit FROM statements WHERE id = $1`, statementID).Scan(&lines, &audit); err != nil {
		return nil, nil, mapErr(err)
	}
	out, err := decodeTaxLines(lines)
	if err != nil {
		return nil, nil, err
	}
	return out, decodeTaxAudit(audit), nil
}

// decodeTaxAudit reads the frozen determination audit. Unreadable JSON reads
// as "no audit", never as an error: the money is the authority, and a note
// that cannot be parsed must not cost a customer its invoice.
func decodeTaxAudit(raw []byte) []string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// taxAuditJSON stores the audit, or SQL NULL when there is none.
func taxAuditJSON(audit []string) any {
	if len(audit) == 0 {
		return nil
	}
	b, err := json.Marshal(audit)
	if err != nil {
		return nil
	}
	return b
}
