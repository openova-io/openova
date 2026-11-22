package spendlimit

import (
	"context"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/notify"
	"github.com/openova-io/openova/products/chargeback/internal/platform"
	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// A book that is not in force for the period leaves ITS Source unrated; it
// never switches off the spend limit of the customer's other Sources. The
// statement run refuses a period that starts before a book's effective date
// (rating.ErrBookNotInForce) and the evaluator rates through the same code:
// without this, one cloud book dated 2 November failed the whole customer
// for all of November, and the platform Source's limit (the arithmetic of
// TestIntegrationSpendLimitStopsTheUsageAboveThePackage: 3.365649 ≥ 3.000
// on 20 November) never stopped anything.
func TestIntegrationABookNotInForceKeepsTheOtherSourcesSpendLimit(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	plans, _, err := st.EnsurePlanBook(ctx)
	if err != nil {
		t.Fatal(err)
	}
	annual := store.Decimal("53.88")
	if _, err := st.UpdatePriceItem(ctx, plans.ID, "plan.m", store.PriceItemPatch{AnnualPrice: &annual}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutPackageSettings(ctx, plans.ID, "plan.m", store.PackageSettingsInput{VCPU: dp("2"), MemoryGB: dp("4"), DiskGB: dp("50"), GrowAllowed: true,
		GrowCeilingVCPU: dp("8"), GrowCeilingMemoryGB: dp("16"), GrowCeilingDiskGB: dp("250"), GrowCeilingBandwidthMbps: dp("1000"),
		OverageVCPUMonth: dp("1.796"), OverageMemGBMonth: dp("0.337")}); err != nil {
		t.Fatal(err)
	}
	cust, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "sohar", Name: "Sohar Trading", AdminEmail: "ops@sohar.example", Kind: "organization", OrgSlug: "sohar", PlanSlug: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetCustomerStatus(ctx, cust.ID, "active"); err != nil {
		t.Fatal(err)
	}
	src, _, err := st.UpsertSource(ctx, cust.ID, store.SourceKindOrg, "", "sohar")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSourceVerified(ctx, src.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSourcePriceBook(ctx, src.ID, plans.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetSourceOverage(ctx, src.ID, store.OverageInput{Mode: store.OverageModeGrow, SpendLimitMonth: dp("3.000")}); err != nil {
		t.Fatal(err)
	}
	// The cloud Source: a book in force from 2 November, so no price on it
	// is in force for the period from 1 November.
	cloudBook, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "National Cloud list", Scope: store.LayerCloud, Currency: "OMR", AnnualDivisor: 8760, BillStopped: "compute", EffectiveFrom: "2026-11-02"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutPriceItems(ctx, cloudBook.ID, []store.PriceItem{{SKU: "ecs.m7n.xlarge.8", Unit: "instance-hour", UnitPrice: "0.5"}}, true); err != nil {
		t.Fatal(err)
	}
	cloud, _, err := st.UpsertSource(ctx, cust.ID, store.SourceKindHuaweiProject, "me-east-215", "proj-sohar")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSourcePriceBook(ctx, cloud.ID, cloudBook.ID); err != nil {
		t.Fatal(err)
	}

	var recs []store.UsageRecord
	nov := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for at := nov; at.Before(nov.AddDate(0, 0, 19)); at = at.Add(time.Hour) {
		recs = append(recs,
			store.UsageRecord{CustomerID: cust.ID, SourceID: src.ID, ResourceID: "plan/m", ResourceKind: store.PlanKind, SKU: "plan.m", Quantity: "1", Unit: store.PlanUnit, WindowStart: at, WindowEnd: at.Add(time.Hour)},
			store.UsageRecord{CustomerID: cust.ID, SourceID: src.ID, ResourceID: "pod/web", ResourceKind: "k8s-pod", SKU: store.SKUVCPULimit, Quantity: "5", Unit: store.UnitVCPU, WindowStart: at, WindowEnd: at.Add(time.Hour)},
			store.UsageRecord{CustomerID: cust.ID, SourceID: cloud.ID, ResourceID: "vm-1", ResourceKind: "ecs", SKU: "ecs.m7n.xlarge.8", Quantity: "1", Unit: "instance-hour", WindowStart: at, WindowEnd: at.Add(time.Hour), Region: "me-east-215"},
		)
	}
	if _, err := st.UpsertUsage(ctx, recs); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	plat := &platform.Fake{}
	e := &Evaluator{Store: st, Platform: plat, Notify: &notify.Notifier{Store: st, Channels: notify.DefaultChannels(&capture{})}, Now: func() time.Time { return now }}
	rep, err := e.RunOnce(ctx)
	if err != nil || rep.Stopped != 1 || rep.Errors != 0 {
		t.Fatalf("pass = %+v %v, want the platform Source stopped and no error", rep, err)
	}
	ss, err := st.GetSpendStop(ctx, src.ID, nov)
	if err != nil || ss.Accrued != "3.365649" {
		t.Fatalf("stop = %+v %v, want 3.365649 accrued on the platform Source", ss, err)
	}
}
