package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
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

// BillingSettings is the single-row billing configuration. Today it holds
// the discount combination rule; a later setting joins as another column,
// never as a second row.
type BillingSettings struct {
	DiscountRule string    `json:"discount_rule"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// DefaultBillingSettings is what the migration seeds.
func DefaultBillingSettings() BillingSettings {
	return BillingSettings{DiscountRule: DefaultDiscountRule}
}

// Normalize trims and lower-cases the rule so "Stack" and "stack" are one.
func (b *BillingSettings) Normalize() {
	b.DiscountRule = strings.ToLower(strings.TrimSpace(b.DiscountRule))
}

// Validate reports the first rule the settings break, wrapped in ErrInvalid.
func (b BillingSettings) Validate() error {
	if !ValidDiscountRule(b.DiscountRule) {
		return fmt.Errorf("%w: discount_rule must be one of %s", ErrInvalid, strings.Join(DiscountRules, ", "))
	}
	return nil
}

// GetBillingSettings reads the single settings row. A missing row (the
// migration seeds it; a wiped table can lose it) reads as the defaults — a
// single-row configuration is never "not found".
func (s *Store) GetBillingSettings(ctx context.Context) (BillingSettings, error) {
	var b BillingSettings
	err := s.db.QueryRowContext(ctx, `SELECT discount_rule, updated_at FROM billing_settings WHERE id = 1`).Scan(&b.DiscountRule, &b.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultBillingSettings(), nil
	}
	if err != nil {
		return b, mapErr(err)
	}
	b.UpdatedAt = b.UpdatedAt.UTC()
	return b, nil
}

// UpdateBillingSettings validates and replaces the settings row. An unknown
// rule is ErrInvalid; the caller answers 400 with the message.
func (s *Store) UpdateBillingSettings(ctx context.Context, in BillingSettings) (BillingSettings, error) {
	in.Normalize()
	if err := in.Validate(); err != nil {
		return BillingSettings{}, err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE billing_settings SET discount_rule = $1, updated_at = now() WHERE id = 1`, in.DiscountRule)
	if err != nil {
		return BillingSettings{}, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// The migration seeds the row; a missing one is a wiped table.
		if _, err := s.db.ExecContext(ctx, `INSERT INTO billing_settings (id, discount_rule) VALUES (1, $1)`, in.DiscountRule); err != nil {
			return BillingSettings{}, mapErr(err)
		}
	}
	return s.GetBillingSettings(ctx)
}
