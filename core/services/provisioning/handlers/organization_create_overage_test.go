package handlers

// organization_create_overage_test.go — the overage mode on the Organization
// CR's spec.commerce (founder model 2026-10-10): overageMode, growCeiling
// (camelCase dimensions, non-zero only) and spendLimitMonth; absent when the
// payload carries none.

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/openova-io/openova/core/services/shared/events"
)

func TestOrganizationCommerceBlock_Overage(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   tenantCreatedPayload
		want map[string]any
		ok   bool
	}{
		{
			name: "grow with a full ceiling and a spend limit",
			in: tenantCreatedPayload{PackageSKU: "plan.m", OverageMode: "grow",
				GrowCeiling:     &events.GrowCeiling{VCPU: 8, MemoryGB: 16, DiskGB: 250, BandwidthMbps: 1000},
				SpendLimitMonth: " 25.000 "},
			want: map[string]any{"packageSKU": "plan.m", "overageMode": "grow",
				"growCeiling":     map[string]any{"vcpu": 8.0, "memoryGB": 16.0, "diskGB": 250.0, "bandwidthMbps": 1000.0},
				"spendLimitMonth": "25.000"},
			ok: true,
		},
		{
			name: "only the non-zero dimensions",
			in:   tenantCreatedPayload{OverageMode: "GROW", GrowCeiling: &events.GrowCeiling{VCPU: 4, DiskGB: 100}},
			want: map[string]any{"overageMode": "grow", "growCeiling": map[string]any{"vcpu": 4.0, "diskGB": 100.0}},
			ok:   true,
		},
		{
			name: "capped package order",
			in:   tenantCreatedPayload{PackageSKU: "plan.s", OverageMode: "capped"},
			want: map[string]any{"packageSKU": "plan.s", "overageMode": "capped"},
			ok:   true,
		},
		{
			name: "package without overage fields keeps the pre-change block",
			in:   tenantCreatedPayload{PackageSKU: "plan.s"},
			want: map[string]any{"packageSKU": "plan.s"},
			ok:   true,
		},
		{
			name: "unknown mode and zero ceiling alone mint nothing",
			in:   tenantCreatedPayload{OverageMode: "unlimited", GrowCeiling: &events.GrowCeiling{}},
			ok:   false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			block, ok := organizationCommerceBlock(tc.in)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (block %#v)", ok, tc.ok, block)
			}
			if !tc.ok {
				if block != nil {
					t.Errorf("block = %#v, want nil", block)
				}
				return
			}
			if !reflect.DeepEqual(block, tc.want) {
				t.Errorf("spec.commerce = %#v, want %#v", block, tc.want)
			}
		})
	}
}

// The consumer decodes the producer's wire shape straight into the payload.
func TestOrganizationCommerceBlock_FromTheWire(t *testing.T) {
	var p tenantCreatedPayload
	if err := json.Unmarshal([]byte(`{"id":"tid","slug":"acme","owner_email":"o@acme.example","package_sku":"plan.m",
		"overage_mode":"grow","grow_ceiling":{"vcpu":8,"memory_gb":16,"disk_gb":250,"bandwidth_mbps":1000},"spend_limit_month":"25.000"}`), &p); err != nil {
		t.Fatal(err)
	}
	block, ok := organizationCommerceBlock(p)
	if !ok || block["overageMode"] != "grow" || block["spendLimitMonth"] != "25.000" {
		t.Fatalf("block = %#v", block)
	}
	if c, _ := block["growCeiling"].(map[string]any); c["bandwidthMbps"] != 1000.0 || c["memoryGB"] != 16.0 {
		t.Errorf("growCeiling = %#v", block["growCeiling"])
	}
}
