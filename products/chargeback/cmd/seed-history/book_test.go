package main

import (
	"strings"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/synth"
)

// The fixture is the hw307 shape that produced the defect, plus the pieces
// that make each resolution rule discriminating:
//
//   - the operator's own card, one word away from the seeder's;
//   - the card an earlier seed-history run made;
//   - a second, unrelated cloud card, so "any cloud book" would be wrong;
//   - a platform card, so scope has to be respected.
var (
	operatorBook = apiPriceBook{ID: "b-operator", Name: "National Cloud 2026 list", Scope: "cloud", Currency: "OMR", BillStopped: "none"}
	seededBook   = apiPriceBook{ID: "b-seeded", Name: synth.CloudBookName, Scope: "cloud", Currency: "OMR", BillStopped: "compute"}
	landlordCard = apiPriceBook{ID: "b-landlord", Name: "Omantel wholesale 2026", Scope: "cloud", Currency: "OMR", BillStopped: "none"}
	otherCloud   = apiPriceBook{ID: "b-other", Name: "Reseller list", Scope: "cloud", Currency: "OMR"}
	planBook     = apiPriceBook{ID: "b-plans", Name: synth.PlanBookName, Scope: "platform", Currency: "OMR"}
)

func TestResolveCloudBookPrefersTheLandlordThenTheNationalCardThenItsOwn(t *testing.T) {
	all := []apiPriceBook{planBook, operatorBook, seededBook, landlordCard, otherCloud}

	// (b) the landlord's card beats everything below it — the whole point:
	// the showcase must be priced exactly as the Sovereign's own usage is.
	got, err := resolveCloudBook("", all, landlordCard.ID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.ID != landlordCard.ID || got.Rule != ruleLandlord {
		t.Fatalf("with a landlord card present, resolved %q via %s; want %q via %s", got.Name, got.Rule, landlordCard.Name, ruleLandlord)
	}

	// (c) no landlord card: the ONE card that reads as a National Cloud list.
	// The seeder's own "National Cloud list 2026" reads that way too and must
	// be excluded, or this rule would be ambiguous forever on hw307.
	got, err = resolveCloudBook("", all, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.ID != operatorBook.ID || got.Rule != ruleNational {
		t.Fatalf("resolved %q via %s; want the operator's %q via %s", got.Name, got.Rule, operatorBook.Name, ruleNational)
	}

	// (d) neither: reuse the card an earlier run made rather than add a second.
	got, err = resolveCloudBook("", []apiPriceBook{planBook, seededBook, otherCloud}, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.ID != seededBook.ID || got.Rule != ruleSeeded {
		t.Fatalf("resolved %q via %s; want the earlier run's %q via %s", got.Name, got.Rule, seededBook.Name, ruleSeeded)
	}

	// (e) nothing at all to borrow: create, under a name nobody would mistake
	// for an operator's card.
	got, err = resolveCloudBook("", []apiPriceBook{planBook}, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.ID != "" || got.Rule != ruleCreate || got.Name != synth.ShowcaseCloudBookName {
		t.Fatalf("resolved %q (%s) via %s; want a new %q", got.Name, got.ID, got.Rule, synth.ShowcaseCloudBookName)
	}
	if synth.LooksLikeNationalCloudBook(synth.ShowcaseCloudBookName) {
		t.Fatalf("%q reads as a National Cloud card; the created book must not be mistakable for the operator's", synth.ShowcaseCloudBookName)
	}
}

// A database that holds ONLY the operator's card must create nothing — the
// defect this whole change exists to stop.
func TestResolveCloudBookCreatesNothingWhenTheOperatorHasOne(t *testing.T) {
	got, err := resolveCloudBook("", []apiPriceBook{planBook, operatorBook}, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.ID != operatorBook.ID {
		t.Fatalf("resolved %q (%s); want the operator's own card, and no new one", got.Name, got.ID)
	}
	if got.Rule == ruleCreate {
		t.Fatal("a book would have been created beside the operator's own")
	}
}

func TestResolveCloudBookFlagWinsAndIsChecked(t *testing.T) {
	all := []apiPriceBook{planBook, operatorBook, seededBook, landlordCard}

	// By name, case-insensitively, over the landlord's card.
	got, err := resolveCloudBook("national cloud 2026 LIST", all, landlordCard.ID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.ID != operatorBook.ID || got.Rule != ruleFlag {
		t.Fatalf("--cloud-book by name resolved %q via %s", got.Name, got.Rule)
	}
	// By id.
	if got, err = resolveCloudBook(otherCloud.ID, append(all, otherCloud), ""); err != nil || got.ID != otherCloud.ID {
		t.Fatalf("--cloud-book by id resolved %q (%v)", got.ID, err)
	}
	// A platform book is refused rather than silently mis-assigned.
	if _, err := resolveCloudBook(synth.PlanBookName, all, ""); err == nil {
		t.Fatal("a platform book was accepted as the showcase's cloud card")
	}
	// An unknown name names what IS there instead of failing blankly.
	_, err = resolveCloudBook("no such card", all, "")
	if err == nil {
		t.Fatal("an unknown --cloud-book was accepted")
	}
	if !strings.Contains(err.Error(), operatorBook.Name) {
		t.Fatalf("the error does not name the cloud books that exist: %v", err)
	}
}

// Two operator cards that both read as National Cloud lists is ambiguous, and
// guessing between them is how the showcase ends up on the wrong rates again.
func TestResolveCloudBookRefusesAnAmbiguousNationalCard(t *testing.T) {
	second := apiPriceBook{ID: "b-second", Name: "National Cloud Duqm list", Scope: "cloud"}
	if _, err := resolveCloudBook("", []apiPriceBook{operatorBook, second}, ""); err == nil {
		t.Fatal("two National Cloud cards resolved without complaint")
	}
}

// A book with no scope is from a build older than the two-layer split, where
// every book was a cloud book; refusing to see it would make the tool useless
// on exactly the Sovereigns that most need repairing.
func TestResolveCloudBookAcceptsAPreTwoLayerBook(t *testing.T) {
	old := apiPriceBook{ID: "b-old", Name: "National Cloud 2026 list"}
	got, err := resolveCloudBook("", []apiPriceBook{old}, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.ID != old.ID {
		t.Fatalf("a scope-less book was skipped; resolved %q", got.Name)
	}
}

// duplicateCloudBooks decides what may be DELETED, so its only safe answer is
// "a card this tool is known to have made, and not the one in force".
func TestDuplicateCloudBooksOnlyNamesTheSeedersOwn(t *testing.T) {
	showcase := apiPriceBook{ID: "b-showcase", Name: synth.ShowcaseCloudBookName, Scope: "cloud"}
	all := []apiPriceBook{planBook, operatorBook, seededBook, landlordCard, otherCloud, showcase}

	got := duplicateCloudBooks(all, operatorBook.ID)
	ids := map[string]bool{}
	for _, b := range got {
		ids[b.ID] = true
	}
	if len(got) != 2 || !ids[seededBook.ID] || !ids[showcase.ID] {
		t.Fatalf("duplicates %v; want exactly the two cards seed-history makes", ids)
	}
	for _, b := range got {
		if b.ID == operatorBook.ID || b.ID == landlordCard.ID || b.ID == otherCloud.ID || b.ID == planBook.ID {
			t.Fatalf("%q is not a book seed-history made and must never be a delete candidate", b.Name)
		}
	}

	// The card in force is never its own duplicate.
	for _, b := range duplicateCloudBooks(all, seededBook.ID) {
		if b.ID == seededBook.ID {
			t.Fatal("the resolved card was listed for deletion")
		}
	}

	// And a database with only the operator's card has nothing to repair.
	if got := duplicateCloudBooks([]apiPriceBook{planBook, operatorBook}, operatorBook.ID); len(got) != 0 {
		t.Fatalf("nothing was seeded here, yet %d book(s) were marked for deletion", len(got))
	}
}
