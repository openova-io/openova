package store

import (
	"encoding/json"
	"testing"
)

// A pool with nothing selling on it — a brand-new pool, or one whose placements
// were all removed — must report an EMPTY basket, never an absent one.
//
// Found live on hw307: creating a pool through the console returned
// `"items": null`, the page mapped over it, and the whole Capacity page went
// blank. A white screen is a worse failure than the one it replaced, and it
// lands on exactly the state a first-time operator sees.
func TestAnEmptyBasketIsAnEmptyListNotNull(t *testing.T) {
	got := defaultBasket(nil)
	if got == nil {
		t.Fatal("defaultBasket returned nil; an empty basket must be an empty slice so it marshals as [] and a reader can map over it")
	}
	if len(got) != 0 {
		t.Fatalf("want no items, got %d", len(got))
	}

	// The serialised shape is the contract the console actually consumes.
	raw, err := json.Marshal(map[string]any{"items": got})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(raw) != `{"items":[]}` {
		t.Fatalf("serialised as %s, want {\"items\":[]} — null is what crashed the page", raw)
	}
}

// The same guarantee through basketFit, which is what the overview embeds.
func TestBasketFitCarriesAnEmptyListAndSaysWhy(t *testing.T) {
	b := basketFit(nil, defaultBasket(nil), nil)
	if b.Items == nil {
		t.Fatal("basketFit left Items nil; it marshals as null and the console maps over it")
	}
	if b.Reason == "" {
		t.Fatal("an empty basket must say why it is empty rather than rendering a silent blank")
	}
}
