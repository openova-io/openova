package orgapi

// types_commerce_test.go — #6971 item 8. spec.commerce is a POINTER with
// `omitempty` so the typed client round-trips an absent block as absent (the
// #4471 value-struct lesson), DeepCopy owns its own add-on slice, and the JSON
// keys are exactly the field paths the chargeback OpenOva adapter reads.

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestOrganizationCommerce_JSONKeysAreTheAdapterFieldPaths(t *testing.T) {
	org := Organization{Spec: OrganizationSpec{
		Slug: "acme", DisplayName: "ACME", Kind: "customer", Tier: "org", BillingMode: "real",
		SovereignRef: "omantel.omani.works",
		Owners:       []OrganizationOwner{{Email: "o@acme.example", Role: "owner"}},
		Commerce: &OrganizationCommerce{
			PackageSKU:  "plan.m",
			Addons:      []string{"addon.backup", "addon.dedicated-ip"},
			PriceSource: "bss:OpenOva plans@2026-09-11",
			OrderID:     "4f7c2a1e-9b3d-4c5e-8f6a-1d2e3f4a5b6c",
		},
	}}
	wire, err := json.Marshal(org)
	if err != nil {
		t.Fatal(err)
	}
	for _, frag := range []string{
		`"commerce":{`,
		`"packageSKU":"plan.m"`,
		`"addons":["addon.backup","addon.dedicated-ip"]`,
		`"priceSource":"bss:OpenOva plans@2026-09-11"`,
		`"orderID":"4f7c2a1e-9b3d-4c5e-8f6a-1d2e3f4a5b6c"`,
	} {
		if !strings.Contains(string(wire), frag) {
			t.Errorf("wire missing %s: %s", frag, wire)
		}
	}
	// Decode a CRD-shaped document (what the provisioning consumer POSTs).
	var got Organization
	if err := json.Unmarshal([]byte(`{"spec":{"slug":"acme","displayName":"ACME","kind":"customer","tier":"org",
		"billingMode":"real","sovereignRef":"omantel.omani.works","owners":[{"email":"o@acme.example","role":"owner"}],
		"commerce":{"packageSKU":"plan.m","addons":["addon.backup"],"priceSource":"catalog","orderID":"ord-1"}}}`), &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Commerce == nil || got.Spec.Commerce.PackageSKU != "plan.m" || got.Spec.Commerce.OrderID != "ord-1" ||
		got.Spec.Commerce.PriceSource != "catalog" || !reflect.DeepEqual(got.Spec.Commerce.Addons, []string{"addon.backup"}) {
		t.Errorf("decoded commerce = %+v", got.Spec.Commerce)
	}
}

func TestOrganizationCommerce_AbsentRoundTripsAsAbsent(t *testing.T) {
	// The sovereign-admin door and every pre-#6971 Organization: no block.
	// A typed Update must not invent `commerce: {}` (the #4471 trap).
	wire, err := json.Marshal(Organization{Spec: OrganizationSpec{Slug: "acme"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), `"commerce"`) {
		t.Errorf("absent commerce leaked onto the wire: %s", wire)
	}
	var got Organization
	if err := json.Unmarshal([]byte(`{"spec":{"slug":"acme"}}`), &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Commerce != nil {
		t.Errorf("absent commerce decoded as %+v, want nil", got.Spec.Commerce)
	}
}

func TestOrganizationCommerce_DeepCopyOwnsItsSlice(t *testing.T) {
	src := &Organization{Spec: OrganizationSpec{
		Slug:     "acme",
		Commerce: &OrganizationCommerce{PackageSKU: "plan.m", Addons: []string{"addon.backup"}},
	}}
	cp := src.DeepCopyObject().(*Organization)
	if cp.Spec.Commerce == src.Spec.Commerce {
		t.Fatal("DeepCopy shared the Commerce pointer")
	}
	cp.Spec.Commerce.Addons[0] = "addon.mutated"
	cp.Spec.Commerce.PackageSKU = "plan.xl"
	if src.Spec.Commerce.Addons[0] != "addon.backup" || src.Spec.Commerce.PackageSKU != "plan.m" {
		t.Errorf("mutating the copy changed the original: %+v", src.Spec.Commerce)
	}
	// nil stays nil.
	if (&Organization{}).DeepCopyObject().(*Organization).Spec.Commerce != nil {
		t.Error("DeepCopy of a nil Commerce produced a non-nil block")
	}
}

// The overage fields (founder model 2026-10-10): the JSON keys are the field
// paths the provisioning consumer writes and the renderer + BSS read.
func TestOrganizationCommerce_OverageJSONKeys(t *testing.T) {
	c := OrganizationCommerce{
		PackageSKU:      "plan.m",
		OverageMode:     OverageModeGrow,
		GrowCeiling:     &OrganizationGrowCeiling{VCPU: 4, MemoryGB: 8, DiskGB: 120, BandwidthMbps: 500},
		SpendLimitMonth: "25.000",
	}
	wire, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, frag := range []string{
		`"overageMode":"grow"`,
		`"growCeiling":{"vcpu":4,"memoryGB":8,"diskGB":120,"bandwidthMbps":500}`,
		`"spendLimitMonth":"25.000"`,
	} {
		if !strings.Contains(string(wire), frag) {
			t.Errorf("wire missing %s: %s", frag, wire)
		}
	}
	var got OrganizationCommerce
	if err := json.Unmarshal([]byte(`{"overageMode":"grow","growCeiling":{"vcpu":2.5,"memoryGB":6},"spendLimitMonth":"10.5"}`), &got); err != nil {
		t.Fatal(err)
	}
	if got.OverageMode != "grow" || got.SpendLimitMonth != "10.5" || got.GrowCeiling == nil ||
		*got.GrowCeiling != (OrganizationGrowCeiling{VCPU: 2.5, MemoryGB: 6}) {
		t.Errorf("decoded = %+v ceiling=%+v", got, got.GrowCeiling)
	}
}

// A capped order (or one that predates the overage fields) carries none of
// the three keys, and decodes with a nil ceiling.
func TestOrganizationCommerce_OverageAbsentRoundTripsAsAbsent(t *testing.T) {
	wire, err := json.Marshal(OrganizationCommerce{PackageSKU: "plan.s"})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{`"overageMode"`, `"growCeiling"`, `"spendLimitMonth"`} {
		if strings.Contains(string(wire), k) {
			t.Errorf("absent %s leaked onto the wire: %s", k, wire)
		}
	}
	var got OrganizationCommerce
	if err := json.Unmarshal([]byte(`{"packageSKU":"plan.s"}`), &got); err != nil {
		t.Fatal(err)
	}
	if got.OverageMode != "" || got.GrowCeiling != nil || got.SpendLimitMonth != "" {
		t.Errorf("absent overage fields decoded as %+v", got)
	}
}

func TestOrganizationCommerce_DeepCopyOwnsItsGrowCeiling(t *testing.T) {
	src := &Organization{Spec: OrganizationSpec{
		Slug: "acme",
		Commerce: &OrganizationCommerce{
			OverageMode: OverageModeGrow,
			GrowCeiling: &OrganizationGrowCeiling{VCPU: 4, MemoryGB: 8},
		},
	}}
	cp := src.DeepCopyObject().(*Organization)
	if cp.Spec.Commerce.GrowCeiling == src.Spec.Commerce.GrowCeiling {
		t.Fatal("DeepCopy shared the GrowCeiling pointer")
	}
	cp.Spec.Commerce.GrowCeiling.VCPU = 8
	if src.Spec.Commerce.GrowCeiling.VCPU != 4 {
		t.Errorf("mutating the copy's ceiling changed the original: %+v", src.Spec.Commerce.GrowCeiling)
	}
	// nil ceiling stays nil.
	nc := (&Organization{Spec: OrganizationSpec{Commerce: &OrganizationCommerce{}}}).DeepCopyObject().(*Organization)
	if nc.Spec.Commerce.GrowCeiling != nil {
		t.Error("DeepCopy of a nil GrowCeiling produced a non-nil one")
	}
}
