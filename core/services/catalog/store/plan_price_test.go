package store

import (
	"encoding/json"
	"strconv"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestPlan_NormalizePrice settles the two price fields the way every reader
// relies on (#6971): baisa is authoritative, price_omr is its decimal mirror,
// and a pre-#6971 row (price_omr only) is lifted into baisa once.
func TestPlan_NormalizePrice(t *testing.T) {
	cases := []struct {
		name            string
		in              Plan
		wantBaisa       int
		wantOMR         float64
		wantOMRJSONText string
	}{
		{"baisa authoritative", Plan{PriceBaisa: 2490}, 2490, 2.49, "2.49"},
		{"baisa wins over a stale omr", Plan{PriceBaisa: 4490, PriceOMR: 9}, 4490, 4.49, "4.49"},
		{"legacy integer omr lifted", Plan{PriceOMR: 9}, 9000, 9, "9"},
		{"legacy decimal omr lifted", Plan{PriceOMR: 13.99}, 13990, 13.99, "13.99"},
		{"free plan", Plan{}, 0, 0, "0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := c.in
			p.NormalizePrice()
			if p.PriceBaisa != c.wantBaisa || p.PriceOMR != c.wantOMR {
				t.Fatalf("got %d baisa / %v OMR, want %d / %v", p.PriceBaisa, p.PriceOMR, c.wantBaisa, c.wantOMR)
			}
			raw, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]json.RawMessage
			_ = json.Unmarshal(raw, &m)
			if got := string(m["price_omr"]); got != c.wantOMRJSONText {
				t.Errorf("price_omr on the wire = %s, want %s", got, c.wantOMRJSONText)
			}
			if got := string(m["price_baisa"]); got != strconv.Itoa(c.wantBaisa) {
				t.Errorf("price_baisa on the wire = %s, want %d", got, c.wantBaisa)
			}
		})
	}
}

// TestOMRToBaisa_RoundsNotTruncates is the float-boundary guard: 2.49 × 1000
// is not exactly 2490 in binary, and a truncating conversion would sell the
// plan for 2489 baisa.
func TestOMRToBaisa_RoundsNotTruncates(t *testing.T) {
	for omr, want := range map[float64]int{2.49: 2490, 4.49: 4490, 7.99: 7990, 13.99: 13990, 9: 9000, 0: 0, 0.0005: 1, 0.0004: 0} {
		if got := OMRToBaisa(omr); got != want {
			t.Errorf("OMRToBaisa(%v) = %d, want %d", omr, got, want)
		}
	}
	if got := BaisaToOMR(2490); got != 2.49 {
		t.Errorf("BaisaToOMR(2490) = %v, want 2.49", got)
	}
}

// TestPlan_PriceFields_BSONRoundTrip: a row seeded before #6971 stores
// price_omr as an INTEGER and has no price_baisa. Decoding it into the float
// field must work, and the read path's NormalizePrice must then lift it into
// baisa — otherwise an already-seeded Sovereign would serve price_baisa: 0.
func TestPlan_PriceFields_BSONRoundTrip(t *testing.T) {
	legacy := bson.D{{Key: "_id", Value: "uuid-m"}, {Key: "slug", Value: "m"}, {Key: "price_omr", Value: int32(9)}}
	raw, err := bson.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	var p Plan
	if err := bson.Unmarshal(raw, &p); err != nil {
		t.Fatalf("a legacy integer price_omr must decode into the float field: %v", err)
	}
	p.NormalizePrice()
	if p.PriceBaisa != 9000 || p.PriceOMR != 9 {
		t.Errorf("legacy row settled to %d baisa / %v OMR, want 9000 / 9", p.PriceBaisa, p.PriceOMR)
	}

	// And a current row round-trips both keys.
	cur := Plan{ID: "uuid-s", Slug: "s", PriceBaisa: 2490}
	cur.NormalizePrice()
	raw, err = bson.Marshal(cur)
	if err != nil {
		t.Fatal(err)
	}
	var doc bson.M
	if err := bson.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["price_baisa"]; !ok {
		t.Errorf("persisted document lacks price_baisa: %v", doc)
	}
	var back Plan
	if err := bson.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.PriceBaisa != 2490 || back.PriceOMR != 2.49 {
		t.Errorf("round-trip = %d baisa / %v OMR, want 2490 / 2.49", back.PriceBaisa, back.PriceOMR)
	}
}
