package events

// tenant_created_overage_test.go — the overage fields on the `tenant.created`
// wire shape (founder model 2026-10-10): overage_mode, grow_ceiling and
// spend_limit_month ride beside the package, normalised, and a producer that
// never sets them emits the exact pre-change bytes.

import (
	"encoding/json"
	"strings"
	"testing"
)

// The exact bytes a legacy producer (no purchase at all) emits — pinned, so a
// new field that forgets omitempty fails here.
const legacyTenantCreatedBytes = `{"id":"tid","slug":"acme","name":"ACME","owner_id":"u1","owner_email":"o@acme.example","plan_id":"plan-m","parent_domain":"omani.homes"}`

func TestTenantCreatedPayload_Legacy_ExactBytes(t *testing.T) {
	p := NewTenantCreatedPayload("tid", "acme", "ACME", "u1", "o@acme.example", "plan-m", "", "", "omani.homes").
		WithCommerce(TenantCommerce{})
	wire, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(wire) != legacyTenantCreatedBytes {
		t.Fatalf("legacy bytes changed:\n got %s\nwant %s", wire, legacyTenantCreatedBytes)
	}
	if p.HasCommerce() {
		t.Fatal("HasCommerce = true on a legacy payload")
	}
}

func TestTenantCreatedPayload_Overage_RoundTrip(t *testing.T) {
	ceiling := &GrowCeiling{VCPU: 8, MemoryGB: 16, DiskGB: 250, BandwidthMbps: 1000}
	p := NewTenantCreatedPayload("tid", "acme", "ACME", "u1", "o@acme.example", "plan-m", "", "", "omani.homes").
		WithCommerce(TenantCommerce{
			PackageSKU:      "plan.m",
			OverageMode:     " GROW ",
			GrowCeiling:     ceiling,
			SpendLimitMonth: " 25.000 ",
		})
	// WithCommerce copies the ceiling: a later change to the caller's value
	// does not reach the payload.
	ceiling.VCPU = 99
	wire, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, frag := range []string{
		`"overage_mode":"grow"`,
		`"grow_ceiling":{"vcpu":8,"memory_gb":16,"disk_gb":250,"bandwidth_mbps":1000}`,
		`"spend_limit_month":"25.000"`,
	} {
		if !strings.Contains(string(wire), frag) {
			t.Errorf("wire missing %s: %s", frag, wire)
		}
	}
	var got TenantCreatedPayload
	if err := json.Unmarshal(wire, &got); err != nil {
		t.Fatal(err)
	}
	if got.OverageMode != "grow" || got.SpendLimitMonth != "25.000" || got.GrowCeiling == nil ||
		*got.GrowCeiling != (GrowCeiling{VCPU: 8, MemoryGB: 16, DiskGB: 250, BandwidthMbps: 1000}) {
		t.Errorf("round trip: %+v ceiling=%+v", got, got.GrowCeiling)
	}
}

func TestTenantCreatedPayload_Overage_Normalisation(t *testing.T) {
	cases := []struct {
		name string
		in   TenantCommerce
		mode string
		has  bool
	}{
		{"capped alone counts", TenantCommerce{OverageMode: "Capped"}, "capped", true},
		{"unknown mode dropped", TenantCommerce{OverageMode: "unlimited"}, "", false},
		{"zero ceiling dropped", TenantCommerce{GrowCeiling: &GrowCeiling{}}, "", false},
		{"ceiling alone counts", TenantCommerce{GrowCeiling: &GrowCeiling{VCPU: 4}}, "", true},
		{"spend limit alone counts", TenantCommerce{SpendLimitMonth: "10.000"}, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := TenantCreatedPayload{Slug: "acme"}.WithCommerce(c.in)
			if p.OverageMode != c.mode {
				t.Errorf("mode = %q, want %q", p.OverageMode, c.mode)
			}
			if p.HasCommerce() != c.has {
				t.Errorf("HasCommerce = %v, want %v", p.HasCommerce(), c.has)
			}
			if c.in.GrowCeiling != nil && c.in.GrowCeiling.IsZero() && p.GrowCeiling != nil {
				t.Errorf("a zero ceiling must not reach the payload: %+v", p.GrowCeiling)
			}
		})
	}
}
