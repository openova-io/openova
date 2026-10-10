package handlers

// prepaid_handover_test.go — what the checkout collected for the package and
// its BSS add-on lines reaches the Organization (#6971): POST /tenant/orgs and
// the settlement launch body decode prepaid_amount, the settled order wins at
// launch, and tenant.created carries it. A legacy record emits none.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/openova-io/openova/core/services/shared/events"
	"github.com/openova-io/openova/core/services/tenant/store"
)

func TestCreateAndLaunchBodies_DecodePrepaidAmount(t *testing.T) {
	var c createOrgRequest
	if err := json.Unmarshal([]byte(`{"slug":"acme","package_sku":"plan.m","prepaid_amount":"4.490"}`), &c); err != nil {
		t.Fatal(err)
	}
	if c.PrepaidAmount != "4.490" {
		t.Errorf("create body prepaid_amount = %q", c.PrepaidAmount)
	}
	// M 4.490 + backup 1.500 = 5.990.
	got := decodeLaunchRequest(strings.NewReader(`{"order_id":"o1","package_sku":"plan.m","prepaid_amount":"5.990"}`), "tid")
	if got.PrepaidAmount != "5.990" {
		t.Errorf("launch body prepaid_amount = %q", got.PrepaidAmount)
	}
}

func TestApplyLaunchCommerce_PrepaidAmountTheOrderWins(t *testing.T) {
	// A create-time 4.490 is replaced by the settled order's 5.990.
	tn := store.Tenant{ID: "t", PrepaidAmount: "4.490"}
	delta := applyLaunchCommerce(&tn, launchRequest{PrepaidAmount: " 5.990 "})
	if tn.PrepaidAmount != "5.990" || delta.PrepaidAmount != "5.990" || delta.IsZero() {
		t.Errorf("record %q delta %+v, want 5.990 on both", tn.PrepaidAmount, delta)
	}
	// An empty body value leaves the record alone and writes nothing.
	tn = store.Tenant{ID: "t", PrepaidAmount: "4.490"}
	if delta := applyLaunchCommerce(&tn, launchRequest{}); !delta.IsZero() || tn.PrepaidAmount != "4.490" {
		t.Errorf("empty launch body changed the record: %q %+v", tn.PrepaidAmount, delta)
	}
}

func TestLaunchTenant_EmitsPrepaidAmount(t *testing.T) {
	prod := &recordingProducer{}
	h := &Handler{Producer: prod}
	h.launchTenant(context.Background(), &store.Tenant{
		ID: "tid", Slug: "acme", Subdomain: "acme", OwnerEmail: "o@acme.example",
		Apps: []string{"wordpress"}, PackageSKU: "plan.m", PrepaidAmount: "5.990",
	})
	created := eventsByType(prod.published, "tenant.created")
	if len(created) != 1 {
		t.Fatalf("expected 1 tenant.created, got %d", len(created))
	}
	var p events.TenantCreatedPayload
	if err := json.Unmarshal(created[0].Data, &p); err != nil {
		t.Fatal(err)
	}
	if p.PrepaidAmount != "5.990" {
		t.Errorf("tenant.created prepaid_amount = %q, want 5.990", p.PrepaidAmount)
	}

	legacy := &recordingProducer{}
	(&Handler{Producer: legacy}).launchTenant(context.Background(), &store.Tenant{
		ID: "tid", Slug: "acme", Subdomain: "acme", OwnerEmail: "o@acme.example", Apps: []string{"wordpress"},
	})
	for _, e := range eventsByType(legacy.published, "tenant.created") {
		if strings.Contains(string(e.Data), "prepaid_amount") {
			t.Errorf("legacy record leaked prepaid_amount: %s", e.Data)
		}
	}
}
