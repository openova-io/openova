package spendlimit

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/notify"
	"github.com/openova-io/openova/products/chargeback/internal/platform"
	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

type capture struct {
	mu   sync.Mutex
	sent []string
}

func (c *capture) Send(_ context.Context, to, subject, _ string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, to+" | "+subject)
	return nil
}

func dp(s string) *store.Decimal { v := store.Decimal(s); return &v }

// The spend limit STOPS the usage above the package (DESIGN.md §22.11): the
// evaluator rates the period so far through the statement's own pipeline,
// and when a grow Source's usage above its package reaches the limit it
// records the stop once, holds the Organization at its package until the
// next period start (spec.overageStoppedUntil, through the platform door),
// tells the customer, and lifts the stop at the period start — or earlier
// when the limit is raised above what has accrued.
//
// The arithmetic: M (2 vCPU / 4 GB) in grow, 5 vCPU of limits from 1
// November — 3 above the headline. By 20 November (456 h) the usage above
// the package is 3 × 456 = 1,368 vcpu-h × (1.796 × 12 ÷ 8760 =) 0.00246027
// = 3.365649 ≥ the 3.000 limit.
func TestIntegrationSpendLimitStopsTheUsageAboveThePackage(t *testing.T) {
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
	limit := func(v string) {
		t.Helper()
		if _, err := st.SetSourceOverage(ctx, src.ID, store.OverageInput{Mode: store.OverageModeGrow, SpendLimitMonth: dp(v)}); err != nil {
			t.Fatal(err)
		}
	}
	limit("3.000")
	var recs []store.UsageRecord
	nov := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for at := nov; at.Before(nov.AddDate(0, 0, 19)); at = at.Add(time.Hour) {
		recs = append(recs,
			store.UsageRecord{CustomerID: cust.ID, SourceID: src.ID, ResourceID: "plan/m", ResourceKind: store.PlanKind, SKU: "plan.m", Quantity: "1", Unit: store.PlanUnit, WindowStart: at, WindowEnd: at.Add(time.Hour)},
			store.UsageRecord{CustomerID: cust.ID, SourceID: src.ID, ResourceID: "pod/web", ResourceKind: "k8s-pod", SKU: store.SKUVCPULimit, Quantity: "5", Unit: store.UnitVCPU, WindowStart: at, WindowEnd: at.Add(time.Hour)},
		)
	}
	if _, err := st.UpsertUsage(ctx, recs); err != nil {
		t.Fatal(err)
	}

	plat := &platform.Fake{}
	mail := &capture{}
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	e := &Evaluator{Store: st, Platform: plat, Notify: &notify.Notifier{Store: st, Channels: notify.DefaultChannels(mail)}, Now: func() time.Time { return now }}
	dec1 := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)

	// 20 November: 3.365649 ≥ 3.000 — stopped, applied, told.
	rep, err := e.RunOnce(ctx)
	if err != nil || rep.Stopped != 1 || rep.Applied != 1 || rep.Errors != 0 {
		t.Fatalf("first pass = %+v %v", rep, err)
	}
	calls := plat.Recorded()
	if len(calls) != 1 || calls[0].Action != "overage-stop" || calls[0].Slug != "sohar" || !calls[0].Until.Equal(dec1) || !strings.Contains(calls[0].Reason, "monthly spend limit 3") {
		t.Fatalf("platform calls = %+v, want one overage-stop for sohar until 1 December", calls)
	}
	ss, err := st.GetSpendStop(ctx, src.ID, nov)
	if err != nil || ss.Accrued != "3.365649" || ss.AppliedAt == nil || ss.NotifiedAt == nil {
		t.Fatalf("stop = %+v %v, want 3.365649 accrued, applied and notified", ss, err)
	}
	if len(mail.sent) != 1 || !strings.Contains(mail.sent[0], "ops@sohar.example | Spend limit reached for sohar: 3.000 OMR this month") {
		t.Fatalf("mail = %v", mail.sent)
	}
	got, _ := st.GetSource(ctx, store.OperatorScope, src.ID)
	if got.OverageStoppedUntil == nil || !got.OverageStoppedUntil.Equal(dec1) {
		t.Fatalf("the Source document carries overage_stopped_until = %v, want 1 December", got.OverageStoppedUntil)
	}

	// The next tick: nothing new — recorded once, applied once, told once.
	now = now.Add(time.Hour)
	if rep, err = e.RunOnce(ctx); err != nil || rep.Stopped+rep.Applied+rep.Lifted != 0 || len(plat.Recorded()) != 1 || len(mail.sent) != 1 {
		t.Fatalf("second pass = %+v %v calls %d mails %d", rep, err, len(plat.Recorded()), len(mail.sent))
	}

	// The customer raises the limit above what has accrued: lifted at once,
	// and the period may stop again at the new limit.
	limit("10")
	if rep, err = e.RunOnce(ctx); err != nil || rep.Lifted != 1 {
		t.Fatalf("raised limit = %+v %v", rep, err)
	}
	if c := plat.Recorded(); c[len(c)-1].Action != "overage-resume" {
		t.Fatalf("calls = %+v, want a resume", c)
	}
	if _, err := st.GetSpendStop(ctx, src.ID, nov); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an early lift keeps the stop row: %v", err)
	}
	limit("3")
	if rep, err = e.RunOnce(ctx); err != nil || rep.Stopped != 1 || rep.Applied != 1 {
		t.Fatalf("lowered again = %+v %v", rep, err)
	}

	// 1 December: the period is over, the stop is lifted; December starts in
	// grow (the controller returns to grow at this instant on its own).
	now = dec1
	if rep, err = e.RunOnce(ctx); err != nil || rep.Lifted != 1 {
		t.Fatalf("period start = %+v %v", rep, err)
	}
	if ss, err = st.GetSpendStop(ctx, src.ID, nov); err != nil || ss.ClearedAt == nil {
		t.Fatalf("November's stop = %+v %v, want cleared", ss, err)
	}
	if c := plat.Recorded(); c[len(c)-1].Action != "overage-resume" || c[len(c)-1].Slug != "sohar" {
		t.Fatalf("calls = %+v", c)
	}
}
