package main

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/synth"
)

// The showcase contracts (DESIGN.md §7, §15; founder, hw307, 2026-10-04:
// "why is it almost blank, is this a proper example?").
//
// The seeder made customers, sources, price books and three months of usage,
// but no agreement, so the Contracts module had nothing to show on a fresh
// environment. Three of the six showcase customers now carry one — a
// committed-use agreement with allowances, a committed PRICE with a carry-over
// allowance, and a spend commitment — so every kind of line the engine rates
// is on a page somewhere, and the statements the seeder issues are rated under
// them: the committed head at the committed rate, the excess at list, the
// allowance off the top, the true-up where a month falls under the floor.
//
// Everything here goes through the API, as the operator, so the product's
// own validation and audit apply. A contract is matched by NAME on the
// customer, like a discount or a budget, and an existing one is written to
// ONLY when something differs from the scenario: the API audits every write,
// so re-sending identical terms on every run would stack an audit entry per
// run — the decommission-note defect of apply.go, in a new place.

// ensureContracts creates or converges the customer's agreements.
func (s *seeder) ensureContracts(customerID string, c *synth.Customer) error {
	if len(c.Contracts) == 0 {
		return nil
	}
	existing, err := s.api.listContracts(customerID)
	if err != nil {
		return fmt.Errorf("list contracts of %s: %w", c.Slug, err)
	}
	for _, ct := range c.Contracts {
		want := contractBody(ct, c, s.sc.Window)
		wantLines := contractLines(ct)
		var found *apiContract
		for i := range existing {
			if existing[i].Name == ct.Name {
				found = &existing[i]
				break
			}
		}
		if found == nil {
			body := map[string]any{"customer_id": customerID}
			for k, v := range want {
				body[k] = v
			}
			created, err := s.api.createContract(body)
			if err != nil {
				return fmt.Errorf("create contract %q: %w", ct.Name, err)
			}
			if _, err := s.api.putContractItems(created.ID, wantLines); err != nil {
				return fmt.Errorf("lines of contract %q: %w", ct.Name, err)
			}
			s.infof("  contract %q created: %s for %d months, %d line(s)", ct.Name, want["starts_on"], want["term_months"], len(wantLines))
			continue
		}
		changed := false
		if contractHeaderDiffers(*found, want) {
			if _, err := s.api.patchContract(found.ID, want); err != nil {
				return fmt.Errorf("update contract %q: %w", ct.Name, err)
			}
			changed = true
		}
		if contractLinesDiffer(found.Items, ct.Lines) {
			if _, err := s.api.putContractItems(found.ID, wantLines); err != nil {
				return fmt.Errorf("lines of contract %q: %w", ct.Name, err)
			}
			changed = true
		}
		if changed {
			s.infof("  contract %q brought back to the scenario's terms", ct.Name)
		} else {
			s.infof("  contract %q already as the scenario has it; left untouched", ct.Name)
		}
	}
	return nil
}

// contractBody is the create / patch document of one agreement: the term
// derived for the window (synth.Contract.Term), the customer's currency, and
// the agreement signed the day the customer joined, so the record reads as
// one that was in force from the first hour of usage.
func contractBody(ct synth.Contract, c *synth.Customer, w synth.Window) map[string]any {
	starts, months := ct.Term(w)
	body := map[string]any{
		"name":                ct.Name,
		"starts_on":           starts.Format("2006-01-02"),
		"term_months":         months,
		"auto_renew":          ct.AutoRenew,
		"renewal_notice_days": ct.RenewalNoticeDays,
		"currency":            c.Currency,
		"status":              store.ContractActive,
		"signed_at":           c.Joined.UTC().Format(time.RFC3339),
		"po_reference":        ct.PORef,
		"notes":               ct.Notes,
	}
	if ct.MinimumCommitment > 0 {
		body["minimum_commitment"] = num(ct.MinimumCommitment)
	} else {
		// An explicit null CLEARS a minimum an earlier scenario wrote.
		body["minimum_commitment"] = nil
	}
	return body
}

// contractLines is the whole list of one agreement's lines as PUT takes it.
func contractLines(ct synth.Contract) []map[string]any {
	out := make([]map[string]any, 0, len(ct.Lines))
	for _, l := range ct.Lines {
		line := map[string]any{"kind": l.Kind}
		switch l.Kind {
		case synth.ContractSpend:
			line["amount"] = num(l.Amount)
			line["discount_pct"] = num(l.DiscountPct)
		default:
			line["sku"], line["unit"], line["quantity"] = l.SKU, l.Unit, num(l.Quantity)
			if l.Kind == synth.ContractCommitment {
				if l.CommittedPrice > 0 {
					line["committed_price"] = num(l.CommittedPrice)
				} else {
					line["discount_pct"] = num(l.DiscountPct)
				}
			}
			if l.Kind == synth.ContractAllowance {
				line["rollover"] = l.Rollover
			}
		}
		out = append(out, line)
	}
	return out
}

// num renders a quantity or price the way the API's decimals read back, so a
// comparison is string-free on both sides.
func num(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// contractHeaderDiffers compares what the API holds with what the scenario
// wants, field by field, so a re-run writes nothing when nothing changed.
func contractHeaderDiffers(have apiContract, want map[string]any) bool {
	ends, _ := store.AddTerm(want["starts_on"].(string), want["term_months"].(int))
	if have.StartsOn != want["starts_on"] || have.EndsOn != ends || have.TermMonths != want["term_months"] ||
		have.AutoRenew != want["auto_renew"] || have.RenewalNoticeDays != want["renewal_notice_days"] ||
		have.Currency != want["currency"] || have.Status != want["status"] || have.PORef != want["po_reference"] || have.Notes != want["notes"] {
		return true
	}
	if wantMin, _ := want["minimum_commitment"].(string); !numEq(have.MinimumCommitment, wantMin) {
		return true
	}
	wantSigned, _ := time.Parse(time.RFC3339, want["signed_at"].(string))
	if have.SignedAt == nil {
		return true
	}
	haveSigned, err := time.Parse(time.RFC3339, *have.SignedAt)
	return err != nil || !haveSigned.Equal(wantSigned)
}

// contractLinesDiffer compares the stored lines with the scenario's, as sets
// keyed by (kind, sku): order is the server's, not the scenario's.
func contractLinesDiffer(have []apiContractItem, want []synth.ContractLine) bool {
	if len(have) != len(want) {
		return true
	}
	byKey := map[string]apiContractItem{}
	for _, it := range have {
		byKey[it.Kind+"|"+it.SKU] = it
	}
	for _, l := range want {
		it, ok := byKey[l.Kind+"|"+l.SKU]
		if !ok || it.Unit != l.Unit || it.Rollover != (l.Kind == synth.ContractAllowance && l.Rollover) {
			return true
		}
		qty, _ := it.Quantity.Float64()
		if math.Abs(qty-l.Quantity) > 1e-6 {
			return true
		}
		switch l.Kind {
		case synth.ContractSpend:
			if !numEq(it.Amount, num(l.Amount)) || !numEq(it.DiscountPct, num(l.DiscountPct)) {
				return true
			}
		case synth.ContractCommitment:
			if l.CommittedPrice > 0 {
				if !numEq(it.CommittedPrice, num(l.CommittedPrice)) || it.DiscountPct != nil {
					return true
				}
			} else if !numEq(it.DiscountPct, num(l.DiscountPct)) || it.CommittedPrice != nil {
				return true
			}
		}
	}
	return false
}

// numEq compares an optional decimal from the API with the scenario's figure
// ("" = none), numerically, so "1500.000000" equals "1500".
func numEq(have *json.Number, want string) bool {
	if have == nil || want == "" {
		return have == nil && want == ""
	}
	h, err1 := have.Float64()
	w, err2 := strconv.ParseFloat(want, 64)
	return err1 == nil && err2 == nil && math.Abs(h-w) <= 1e-9*math.Max(1, math.Abs(w))
}
