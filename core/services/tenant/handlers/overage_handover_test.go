package handlers

// overage_handover_test.go — the overage mode reaches the Organization
// (founder model 2026-10-10): POST /tenant/orgs and the settlement launch body
// decode overage_mode / grow_ceiling / spend_limit_month, the settled order
// wins at launch, and tenant.created carries them. A legacy record emits none.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/openova-io/openova/core/services/shared/events"
	"github.com/openova-io/openova/core/services/tenant/store"
)

var growCeil = events.GrowCeiling{VCPU: 8, MemoryGB: 16, DiskGB: 250, BandwidthMbps: 1000}

func TestCreateAndLaunchBodies_DecodeOverage(t *testing.T) {
	var c createOrgRequest
	if err := json.Unmarshal([]byte(`{"slug":"acme","package_sku":"plan.m","overage_mode":"grow",
		"grow_ceiling":{"vcpu":4,"memory_gb":16},"spend_limit_month":"25.000"}`), &c); err != nil {
		t.Fatal(err)
	}
	if c.OverageMode != "grow" || c.GrowCeiling == nil || c.GrowCeiling.VCPU != 4 || c.SpendLimitMonth != "25.000" {
		t.Errorf("create body overage: %+v ceiling=%+v", c, c.GrowCeiling)
	}
	got := decodeLaunchRequest(strings.NewReader(`{"order_id":"o1","package_sku":"plan.m","overage_mode":"grow",
		"grow_ceiling":{"vcpu":8,"memory_gb":16,"disk_gb":250,"bandwidth_mbps":1000},"spend_limit_month":"10.000"}`), "tid")
	if got.OverageMode != "grow" || got.GrowCeiling == nil || *got.GrowCeiling != growCeil || got.SpendLimitMonth != "10.000" {
		t.Errorf("launch body overage: %+v", got)
	}
}

func TestApplyLaunchCommerce_Overage(t *testing.T) {
	for _, tc := range []struct {
		name       string
		before     store.Tenant
		req        launchRequest
		wantMode   string
		wantCeil   *events.GrowCeiling
		wantLimit  string
		wantDelta  store.Commerce
		deltaEmpty bool
	}{
		{
			name:      "grow order sets mode, ceiling and limit",
			before:    store.Tenant{ID: "t"},
			req:       launchRequest{OverageMode: " Grow ", GrowCeiling: &growCeil, SpendLimitMonth: " 25.000 "},
			wantMode:  "grow",
			wantCeil:  &growCeil,
			wantLimit: "25.000",
			wantDelta: store.Commerce{OverageMode: "grow", GrowCeiling: &growCeil, SpendLimitMonth: "25.000"},
		},
		{
			name:      "the order wins over a create-time grow: capped clears the grow fields",
			before:    store.Tenant{ID: "t", OverageMode: "grow", GrowCeiling: &events.GrowCeiling{VCPU: 4}, SpendLimitMonth: "5.000"},
			req:       launchRequest{OverageMode: "capped"},
			wantMode:  "capped",
			wantDelta: store.Commerce{OverageMode: "capped", ClearGrow: true},
		},
		{
			name:      "capped on a record without grow fields clears nothing",
			before:    store.Tenant{ID: "t"},
			req:       launchRequest{OverageMode: "capped"},
			wantMode:  "capped",
			wantDelta: store.Commerce{OverageMode: "capped"},
		},
		{
			name:       "no mode on the body leaves the record alone",
			before:     store.Tenant{ID: "t", OverageMode: "grow", GrowCeiling: &growCeil},
			req:        launchRequest{},
			wantMode:   "grow",
			wantCeil:   &growCeil,
			deltaEmpty: true,
		},
		{
			name:       "an unknown mode is ignored",
			before:     store.Tenant{ID: "t"},
			req:        launchRequest{OverageMode: "unlimited", GrowCeiling: &growCeil},
			deltaEmpty: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tn := tc.before
			delta := applyLaunchCommerce(&tn, tc.req)
			if tn.OverageMode != tc.wantMode || tn.SpendLimitMonth != tc.wantLimit {
				t.Errorf("record mode/limit = %q/%q, want %q/%q", tn.OverageMode, tn.SpendLimitMonth, tc.wantMode, tc.wantLimit)
			}
			if (tn.GrowCeiling == nil) != (tc.wantCeil == nil) || (tn.GrowCeiling != nil && *tn.GrowCeiling != *tc.wantCeil) {
				t.Errorf("record ceiling = %+v, want %+v", tn.GrowCeiling, tc.wantCeil)
			}
			if tc.deltaEmpty {
				if !delta.IsZero() {
					t.Errorf("delta = %+v, want zero", delta)
				}
				return
			}
			if delta.OverageMode != tc.wantDelta.OverageMode || delta.SpendLimitMonth != tc.wantDelta.SpendLimitMonth ||
				delta.ClearGrow != tc.wantDelta.ClearGrow ||
				(delta.GrowCeiling == nil) != (tc.wantDelta.GrowCeiling == nil) ||
				(delta.GrowCeiling != nil && *delta.GrowCeiling != *tc.wantDelta.GrowCeiling) {
				t.Errorf("delta = %+v, want %+v", delta, tc.wantDelta)
			}
		})
	}
}

func TestLaunchTenant_EmitsOverageFromPersistedTenant(t *testing.T) {
	prod := &recordingProducer{}
	h := &Handler{Producer: prod}
	h.launchTenant(context.Background(), &store.Tenant{
		ID: "tid", Slug: "acme", Subdomain: "acme", OwnerEmail: "o@acme.example",
		Apps: []string{"wordpress"}, PackageSKU: "plan.m",
		OverageMode: "grow", GrowCeiling: &growCeil, SpendLimitMonth: "25.000",
	})
	created := eventsByType(prod.published, "tenant.created")
	if len(created) != 1 {
		t.Fatalf("expected 1 tenant.created, got %d", len(created))
	}
	var p events.TenantCreatedPayload
	if err := json.Unmarshal(created[0].Data, &p); err != nil {
		t.Fatal(err)
	}
	if p.OverageMode != "grow" || p.GrowCeiling == nil || *p.GrowCeiling != growCeil || p.SpendLimitMonth != "25.000" {
		t.Errorf("tenant.created overage: %+v ceiling=%+v", p, p.GrowCeiling)
	}
}

func TestLaunchTenant_LegacyTenantEmitsNoOverageKeys(t *testing.T) {
	prod := &recordingProducer{}
	h := &Handler{Producer: prod}
	h.launchTenant(context.Background(), &store.Tenant{
		ID: "tid", Slug: "acme", Subdomain: "acme", OwnerEmail: "o@acme.example", Apps: []string{"wordpress"},
	})
	created := eventsByType(prod.published, "tenant.created")
	if len(created) != 1 {
		t.Fatalf("expected 1 tenant.created, got %d", len(created))
	}
	for _, key := range []string{"overage_mode", "grow_ceiling", "spend_limit_month"} {
		if strings.Contains(string(created[0].Data), key) {
			t.Errorf("legacy tenant leaked %s: %s", key, created[0].Data)
		}
	}
}
