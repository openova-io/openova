package openova

import (
	"context"
	"testing"

	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The checkout's prepaid amount (DESIGN.md §22.11): spec.commerce.
// prepaidAmount is what the checkout collected for the package and its
// add-ons; the adapter records it on the platform Source so the first
// statement credits it. Read tolerantly, written only when it differs.

func TestReadOrgCommercePrepaidAmount(t *testing.T) {
	f, err := readOrg(orgUnstructured("acme", withCommerce("plan.m", nil, map[string]any{"prepaidAmount": " 5.990 "})))
	if err != nil || f.Commerce.PrepaidAmount == nil || string(*f.Commerce.PrepaidAmount) != "5.990" {
		t.Fatalf("prepaid = %v err %v", f.Commerce.PrepaidAmount, err)
	}
	for _, bad := range []any{"0", "-1", "abc", 4.49, ""} {
		f, _ = readOrg(orgUnstructured("acme", withCommerce("plan.m", nil, map[string]any{"prepaidAmount": bad})))
		if f.Commerce.PrepaidAmount != nil {
			t.Fatalf("%v read as %v, want absent", bad, *f.Commerce.PrepaidAmount)
		}
	}
}

func TestSyncOrganizationRecordsThePrepaidAmountOnce(t *testing.T) {
	repo := growRepo(t)
	s := &OrgSync{Core: k8sfake.NewSimpleClientset(), Repo: repo, Keys: testKeys(t), Metrics: metrics.New()}
	ctx := context.Background()
	org := func(amount string) func(map[string]any) {
		return func(spec map[string]any) {
			spec["planSlug"] = "m"
			withCommerce("plan.m", nil, map[string]any{"prepaidAmount": amount})(spec)
		}
	}
	if err := s.SyncOrganization(ctx, orgUnstructured("acme", org("4.490"))); err != nil {
		t.Fatal(err)
	}
	if src := orgSourceOf(t, repo, "acme"); src.PrepaidAmount == nil || string(*src.PrepaidAmount) != "4.490" || repo.prepaidWrites != 1 {
		t.Fatalf("source prepaid %v writes %d", src.PrepaidAmount, repo.prepaidWrites)
	}
	// The same figure written differently is the same figure: no write.
	if err := s.SyncOrganization(ctx, orgUnstructured("acme", org("4.49"))); err != nil || repo.prepaidWrites != 1 {
		t.Fatalf("resync wrote again: %d %v", repo.prepaidWrites, err)
	}
	// A block without the field drives nothing.
	if err := s.SyncOrganization(ctx, orgUnstructured("acme", func(spec map[string]any) { spec["planSlug"] = "m" })); err != nil || repo.prepaidWrites != 1 {
		t.Fatalf("absent block wrote: %d %v", repo.prepaidWrites, err)
	}
}

// The retired flexi plan reads as S in grow mode; a mode the order states
// wins over the mapping.
func TestReadOrgMapsTheRetiredFlexiPlanToSGrow(t *testing.T) {
	f, _ := readOrg(orgUnstructured("acme", func(spec map[string]any) { spec["planSlug"] = "Flexi" }))
	if f.PlanSlug != "s" || !f.FlexiRetired || f.Commerce.OverageMode != store.OverageModeGrow {
		t.Fatalf("flexi = plan %q retired %v mode %q, want s / true / grow", f.PlanSlug, f.FlexiRetired, f.Commerce.OverageMode)
	}
	f, _ = readOrg(orgUnstructured("acme", func(spec map[string]any) {
		spec["planSlug"] = "flexi"
		withCommerce("", nil, map[string]any{"overageMode": "capped"})(spec)
	}))
	if f.PlanSlug != "s" || f.Commerce.OverageMode != store.OverageModeCapped {
		t.Fatalf("flexi with a stated mode = %q / %q", f.PlanSlug, f.Commerce.OverageMode)
	}
	f, _ = readOrg(orgUnstructured("acme", func(spec map[string]any) { spec["planSlug"] = "m" }))
	if f.FlexiRetired || f.Commerce.OverageMode != "" {
		t.Fatalf("m = %+v", f)
	}
}
