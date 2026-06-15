// store_test.go — #3602 (EPIC #3597) coverage for the editable-catalog
// fields added to store.App: SupportedTopologies, IconLight, IconDark.
//
// The store's mutations run against FerretDB (MongoDB wire protocol), so a
// full UpdateApp round-trip needs a live database. What we CAN pin without
// a DB — and what actually guards the persistence contract — is that the
// new fields carry the right bson tags so they (a) serialise into the
// document an InsertOne/UpdateOne writes and (b) decode back on the next
// read. A missing/typo'd bson tag is exactly the failure that would make an
// admin edit silently NOT survive a service restart; this round-trip test
// fires on that.
package store

import (
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestApp_EditableFields_BSONRoundTrip(t *testing.T) {
	in := App{
		ID:                  "app-1",
		Slug:                "grafana",
		Name:                "Grafana (Edited)",
		Icon:                "grafana.svg",
		IconLight:           "https://cdn/grafana-light.svg",
		IconDark:            "https://cdn/grafana-dark.svg",
		SupportedTopologies: []string{"single-region", "active-active"},
	}

	raw, err := bson.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// The persisted document MUST carry the canonical snake_case keys the
	// UpdateApp $set writes, so a future read decodes them. Assert the keys
	// exist in the marshalled doc.
	var doc bson.M
	if err := bson.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	for _, key := range []string{"icon_light", "icon_dark", "supported_topologies"} {
		if _, ok := doc[key]; !ok {
			t.Errorf("persisted document missing %q key — an edit to this field would not survive a restart", key)
		}
	}

	// Round-trip back into App and confirm the values are intact.
	var out App
	if err := bson.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal to App: %v", err)
	}
	if out.IconLight != in.IconLight {
		t.Errorf("IconLight round-trip: got %q want %q", out.IconLight, in.IconLight)
	}
	if out.IconDark != in.IconDark {
		t.Errorf("IconDark round-trip: got %q want %q", out.IconDark, in.IconDark)
	}
	if len(out.SupportedTopologies) != 2 || out.SupportedTopologies[1] != "active-active" {
		t.Errorf("SupportedTopologies round-trip: got %v want %v", out.SupportedTopologies, in.SupportedTopologies)
	}
	// Back-compat: the legacy single Icon survives alongside the new ones.
	if out.Icon != "grafana.svg" {
		t.Errorf("legacy Icon must be preserved alongside theme icons: got %q", out.Icon)
	}
}

// TestApp_EditableFields_OmittedWhenEmpty pins the omitempty contract: a
// catalog row that was never edited must NOT serialise empty theme-icon /
// topology keys, so the overlay's hasOverlay() check (which treats their
// presence as "edited") stays meaningful and pre-existing rows don't bloat.
func TestApp_EditableFields_OmittedWhenEmpty(t *testing.T) {
	in := App{ID: "app-2", Slug: "redis", Name: "Redis", Icon: "redis.svg"}
	raw, err := bson.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var doc bson.M
	if err := bson.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"icon_light", "icon_dark", "supported_topologies"} {
		if _, ok := doc[key]; ok {
			t.Errorf("empty %q must be omitted (omitempty) on an un-edited row", key)
		}
	}
}
