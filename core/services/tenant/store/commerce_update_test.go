package store

// commerce_update_test.go — the update SetCommerce sends, pinned without a
// database: the #6971 scalars and add-ons as before, plus the overage fields
// (founder model 2026-10-10) and the $unset of the grow fields when the
// settled order is capped.

import (
	"reflect"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/openova-io/openova/core/services/shared/events"
)

func TestCommerceUpdate(t *testing.T) {
	now := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	ceil := &events.GrowCeiling{VCPU: 8, MemoryGB: 16, DiskGB: 250, BandwidthMbps: 1000}
	for _, tc := range []struct {
		name string
		in   Commerce
		want bson.D
	}{
		{
			name: "package and add-ons, unchanged shape",
			in:   Commerce{PackageSKU: "plan.m", OrderID: "o1", Addons: []string{"addon.backup"}},
			want: bson.D{
				{Key: "$set", Value: bson.D{{Key: "updated_at", Value: now}, {Key: "package_sku", Value: "plan.m"}, {Key: "order_id", Value: "o1"}}},
				{Key: "$addToSet", Value: bson.D{{Key: "addons", Value: bson.D{{Key: "$each", Value: []string{"addon.backup"}}}}}},
			},
		},
		{
			name: "grow sets the three overage fields",
			in:   Commerce{OverageMode: "grow", GrowCeiling: ceil, SpendLimitMonth: "25.000"},
			want: bson.D{
				{Key: "$set", Value: bson.D{{Key: "updated_at", Value: now}, {Key: "overage_mode", Value: "grow"},
					{Key: "grow_ceiling", Value: *ceil}, {Key: "spend_limit_month", Value: "25.000"}}},
			},
		},
		{
			name: "capped clears the grow fields",
			in:   Commerce{OverageMode: "capped", ClearGrow: true},
			want: bson.D{
				{Key: "$set", Value: bson.D{{Key: "updated_at", Value: now}, {Key: "overage_mode", Value: "capped"}}},
				{Key: "$unset", Value: bson.D{{Key: "grow_ceiling", Value: ""}, {Key: "spend_limit_month", Value: ""}}},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := commerceUpdate(tc.in, now); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("update =\n %v\nwant\n %v", got, tc.want)
			}
			if tc.in.IsZero() {
				t.Errorf("IsZero = true for %+v", tc.in)
			}
		})
	}
	if !(Commerce{GrowCeiling: &events.GrowCeiling{}}).IsZero() {
		t.Error("a zero ceiling alone must be IsZero")
	}

	// The ceiling marshals with its bson keys.
	raw, err := bson.Marshal(bson.D{{Key: "grow_ceiling", Value: *ceil}})
	if err != nil {
		t.Fatal(err)
	}
	var back struct {
		GrowCeiling map[string]float64 `bson:"grow_ceiling"`
	}
	if err := bson.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if want := map[string]float64{"vcpu": 8, "memory_gb": 16, "disk_gb": 250, "bandwidth_mbps": 1000}; !reflect.DeepEqual(back.GrowCeiling, want) {
		t.Errorf("bson grow_ceiling = %v, want %v", back.GrowCeiling, want)
	}
}
