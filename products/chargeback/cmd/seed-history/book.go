package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/synth"
)

// Which cloud rate card the showcase is priced from (founder, hw307,
// 2026-09-10).
//
// The defect: the command CREATED "National Cloud list 2026" with nine rates
// even though the operator's own "National Cloud 2026 list" — 134 items — was
// already there. One word apart, and not equivalent: nat.1 was 0.11322489
// against the operator's 0.06037935, bill_stopped `compute` against `none`,
// and several rates differed in the last digits because the two cards were
// derived independently. So the three showcase cloud customers were priced
// from one card and the real customer beside them from another, and the demo
// was not comparing like with like.
//
// The rule now is: BORROW, never mint. A book is created only when the
// Sovereign has none that can be resolved, and then under a name nobody can
// mistake for their own.

// bookRule names which step of the resolution order answered, for the log.
type bookRule string

const (
	ruleFlag     bookRule = "--cloud-book"          // (a) the operator said so
	ruleLandlord bookRule = "landlord"              // (b) the landlord's own card
	ruleNational bookRule = "national-cloud"        // (c) the one card that looks like one
	ruleSeeded   bookRule = "existing seed-history" // (d) a card this tool made earlier
	ruleCreate   bookRule = "created"               // (e) nothing to borrow
)

// bookChoice is the resolved cloud book and why it won.
type bookChoice struct {
	ID   string // empty = nothing resolved, the caller must create one
	Name string
	Rule bookRule
	Why  string
}

// resolveCloudBook picks the cloud rate card the showcase is priced from.
//
// The order is deliberate. The showcase exists to be compared against the
// Sovereign's real usage, so the card the LANDLORD is billed on is the
// principled default: same rates, same bill_stopped, same currency, therefore
// a like-for-like comparison. Everything below it is a fallback, and creating
// a card is the last resort rather than the first move.
//
//	(a) --cloud-book <name or id>  — the operator overrides everything
//	(b) the price book on the landlord customer's real cloud source
//	(c) the ONE cloud-scope book whose name reads as a National Cloud card
//	    (books this tool made are excluded, or the duplicate it is trying to
//	    repair would make this step ambiguous forever)
//	(d) a cloud book this tool made on an earlier run — reuse it rather than
//	    add a second one
//	(e) nothing: create ShowcaseCloudBookName
//
// landlordBookID is the book on the landlord's real cloud source, or empty
// when there is no landlord, no such source, or no book on it.
func resolveCloudBook(explicit string, books []apiPriceBook, landlordBookID string) (bookChoice, error) {
	cloud := make([]apiPriceBook, 0, len(books))
	for _, b := range books {
		// A book with no scope is from a build older than the two-layer
		// split, where every book was a cloud book.
		if b.Scope == "" || b.Scope == synth.LayerCloud {
			cloud = append(cloud, b)
		}
	}

	if want := strings.TrimSpace(explicit); want != "" {
		for _, b := range cloud {
			if b.ID == want || strings.EqualFold(b.Name, want) {
				return bookChoice{b.ID, b.Name, ruleFlag, "named on the command line"}, nil
			}
		}
		for _, b := range books {
			if b.ID == want || strings.EqualFold(b.Name, want) {
				return bookChoice{}, fmt.Errorf("--cloud-book %q is a %s book; the showcase's cloud sources need a cloud-scope one", want, b.Scope)
			}
		}
		return bookChoice{}, fmt.Errorf("--cloud-book %q matches no price book; cloud books here: %s", want, bookNames(cloud))
	}

	if landlordBookID != "" {
		for _, b := range cloud {
			if b.ID == landlordBookID {
				return bookChoice{b.ID, b.Name, ruleLandlord,
					"the card the landlord's own cloud source is billed on, so the showcase compares like with like"}, nil
			}
		}
	}

	var national []apiPriceBook
	for _, b := range cloud {
		if synth.LooksLikeNationalCloudBook(b.Name) && !synth.IsSeederCloudBookName(b.Name) {
			national = append(national, b)
		}
	}
	if len(national) == 1 {
		return bookChoice{national[0].ID, national[0].Name, ruleNational,
			"the only National Cloud rate card on this Sovereign"}, nil
	}
	if len(national) > 1 {
		return bookChoice{}, fmt.Errorf("%d National Cloud rate cards here (%s); name the one the showcase should use with --cloud-book",
			len(national), bookNames(national))
	}

	// A card this tool made on an earlier run. Reuse it: a second one would be
	// the very duplication this resolution order exists to stop.
	for _, want := range synth.SeederCloudBookNames() {
		for _, b := range cloud {
			if strings.EqualFold(b.Name, want) {
				return bookChoice{b.ID, b.Name, ruleSeeded,
					"made by an earlier seed-history run; reused rather than duplicated"}, nil
			}
		}
	}

	return bookChoice{Name: synth.ShowcaseCloudBookName, Rule: ruleCreate,
		Why: "this Sovereign has no cloud rate card to borrow"}, nil
}

func bookNames(books []apiPriceBook) string {
	if len(books) == 0 {
		return "none"
	}
	out := make([]string, 0, len(books))
	for _, b := range books {
		out = append(out, strconv.Quote(b.Name))
	}
	return strings.Join(out, ", ")
}

// duplicateCloudBooks are the cards this tool made that are NOT the resolved
// one — the rows to repair away. A book the seeder did not make is never in
// this list, whatever it is called.
func duplicateCloudBooks(books []apiPriceBook, canonicalID string) []apiPriceBook {
	var out []apiPriceBook
	for _, b := range books {
		if b.ID == canonicalID || b.ID == "" {
			continue
		}
		if b.Scope != "" && b.Scope != synth.LayerCloud {
			continue
		}
		if synth.IsSeederCloudBookName(b.Name) {
			out = append(out, b)
		}
	}
	return out
}
