package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// A region and a zone are EDITED, not only created and deleted (#6946): the
// code and the name change in place, the default moves in one transaction and
// never leaves a region without one, and every refusal is a sentence.
func TestIntegrationCapacityRegionAndZoneEdits(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	region, err := st.CreateCapacityRegion(ctx, "me-east-215", "Muscat", "")
	if err != nil {
		t.Fatal(err)
	}
	a, err := st.CreateCapacityZone(ctx, region.ID, "me-east-215a", "AZ 1", false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateCapacityZone(ctx, region.ID, "me-east-215b", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !a.IsDefault || b.IsDefault {
		t.Fatalf("first zone is the default: a=%v b=%v", a.IsDefault, b.IsDefault)
	}

	// The region: code normalised as on create, the name changed, the kind
	// kept because it was not sent; the previous state comes back for audit.
	code, name := " ME-East-216 ", " Salalah "
	got, prev, err := st.SetCapacityRegion(ctx, region.ID, store.CapacityRegionPatch{Code: &code, Name: &name})
	if err != nil {
		t.Fatal(err)
	}
	if got.Code != "me-east-216" || got.Name != "Salalah" || got.CloudSourceKind != store.SourceKindHuaweiProject || len(got.Zones) != 2 {
		t.Fatalf("region after edit = %+v", got)
	}
	if prev.Code != "me-east-215" || prev.Name != "Muscat" {
		t.Fatalf("previous = %+v", prev)
	}
	if again, err := st.GetCapacityRegion(ctx, region.ID); err != nil || again.Code != "me-east-216" || again.Name != "Salalah" {
		t.Fatalf("edit did not persist: %+v %v", again, err)
	}
	kind := store.SourceKindFile
	if got, _, err = st.SetCapacityRegion(ctx, region.ID, store.CapacityRegionPatch{CloudSourceKind: &kind}); err != nil || got.CloudSourceKind != store.SourceKindFile || got.Code != "me-east-216" {
		t.Fatalf("kind edit = %+v %v", got, err)
	}
	platform := store.SourceKindOrg
	if _, _, err := st.SetCapacityRegion(ctx, region.ID, store.CapacityRegionPatch{CloudSourceKind: &platform}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("a platform kind on a region = %v, want invalid", err)
	}
	empty := "  "
	if _, _, err := st.SetCapacityRegion(ctx, region.ID, store.CapacityRegionPatch{Code: &empty}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("an empty code = %v, want invalid", err)
	}
	if _, _, err := st.SetCapacityRegion(ctx, "00000000-0000-0000-0000-000000000000", store.CapacityRegionPatch{Name: &name}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown region = %v, want not found", err)
	}
	// A duplicate code is the schema's sentence, never its constraint name.
	other, err := st.CreateCapacityRegion(ctx, "eu-west-101", "", "")
	if err != nil {
		t.Fatal(err)
	}
	taken := "ME-EAST-216"
	_, _, err = st.SetCapacityRegion(ctx, other.ID, store.CapacityRegionPatch{Code: &taken})
	if !errors.Is(err, store.ErrConflict) || !strings.Contains(err.Error(), "a region with that code already exists") || strings.Contains(err.Error(), "_key") {
		t.Fatalf("duplicate region code = %v", err)
	}

	// The zone: rename, then MOVE the default onto b — a loses it in the same
	// transaction, and at no point does the region hold two or none.
	zcode, zname := "ME-EAST-216B", "AZ 2"
	zb, zprev, err := st.SetCapacityZone(ctx, b.ID, store.CapacityZonePatch{Code: &zcode, Name: &zname})
	if err != nil {
		t.Fatal(err)
	}
	if zb.Code != "me-east-216b" || zb.Name != "AZ 2" || zb.IsDefault || zprev.Code != "me-east-215b" {
		t.Fatalf("zone after rename = %+v (was %+v)", zb, zprev)
	}
	yes, no := true, false
	zb, _, err = st.SetCapacityZone(ctx, b.ID, store.CapacityZonePatch{Default: &yes})
	if err != nil || !zb.IsDefault {
		t.Fatalf("make b default = %+v %v", zb, err)
	}
	za, err := st.GetCapacityZone(ctx, a.ID)
	if err != nil || za.IsDefault {
		t.Fatalf("a still default after the handoff: %+v %v", za, err)
	}
	var defaults int
	if err := st.DB().QueryRowContext(ctx, `SELECT count(*) FROM capacity_zones WHERE region_id = $1 AND is_default`, region.ID).Scan(&defaults); err != nil || defaults != 1 {
		t.Fatalf("defaults in the region = %d %v, want exactly 1", defaults, err)
	}
	// Making the default the default again is a no-op, not an error.
	if zb, _, err = st.SetCapacityZone(ctx, b.ID, store.CapacityZonePatch{Default: &yes}); err != nil || !zb.IsDefault {
		t.Fatalf("default again = %+v %v", zb, err)
	}
	// Taking the default OFF the default zone is refused in words.
	_, _, err = st.SetCapacityZone(ctx, b.ID, store.CapacityZonePatch{Default: &no})
	if !errors.Is(err, store.ErrConflict) || !strings.Contains(err.Error(), "a region always has a default zone") || !strings.Contains(err.Error(), "me-east-216b") {
		t.Fatalf("unset the only default = %v", err)
	}
	if zb, err = st.GetCapacityZone(ctx, b.ID); err != nil || !zb.IsDefault {
		t.Fatalf("the refusal changed the row: %+v %v", zb, err)
	}
	// default:false on a zone that is NOT the default changes nothing.
	if za, _, err = st.SetCapacityZone(ctx, a.ID, store.CapacityZonePatch{Default: &no}); err != nil || za.IsDefault {
		t.Fatalf("unset on a non-default = %+v %v", za, err)
	}
	// A duplicate zone code within the region, and an empty one.
	dup := "me-east-216b"
	_, _, err = st.SetCapacityZone(ctx, a.ID, store.CapacityZonePatch{Code: &dup})
	if !errors.Is(err, store.ErrConflict) || !strings.Contains(err.Error(), "a zone with that code already exists in this region") || strings.Contains(err.Error(), "_key") {
		t.Fatalf("duplicate zone code = %v", err)
	}
	if _, _, err := st.SetCapacityZone(ctx, a.ID, store.CapacityZonePatch{Code: &empty}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("an empty zone code = %v, want invalid", err)
	}
	if _, _, err := st.SetCapacityZone(ctx, "00000000-0000-0000-0000-000000000000", store.CapacityZonePatch{Name: &zname}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown zone = %v, want not found", err)
	}
}
