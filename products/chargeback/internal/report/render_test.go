package report

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/openova-io/openova/products/chargeback/internal/budget"
	"github.com/openova-io/openova/products/chargeback/internal/rating"
	"github.com/openova-io/openova/products/chargeback/internal/recommend"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

var update = flag.Bool("update", false, "rewrite the golden report text")

func pctp(v float64) *float64 { return &v }

// fixtureInput is a weekly operator report over 1–7 Sep 2026 with every
// section populated.
func fixtureInput() Input {
	fc := 87.5
	pf := 90.4
	return Input{
		Name:     "Weekly ops",
		Scope:    "All customers",
		Operator: true,
		Cadence:  CadenceWeekly,
		From:     at(2026, 9, 1, 0, 0),
		To:       at(2026, 9, 8, 0, 0),
		Currency: "OMR",
		Sections: Sections(),

		Total: "810.400000", Previous: "790.200000", DeltaPct: pctp(2.556), Resources: 126,
		MTDMonth: "Sep 2026", MTD: "810.400000",
		Forecast: &rating.Forecast{MonthEnd: 2712.5, RunRateDaily: 92.1, Method: "run-rate-7d", DaysObserved: 7, DaysInMonth: 30, Confidence: "medium"},
		Unpriced: []store.UnpricedSKU{{SKU: "k8s.vcpu", Unit: "vcpu-hour", Quantity: "1118.400000", Resources: 14097}},

		Services: []Group{
			{Key: "ecs", Label: "Elastic Cloud Server", Total: "421.100000", Previous: "398.000000", DeltaPct: pctp(5.8), Share: 0.5196, Resources: 12},
			{Key: "evs", Label: "Block storage (EVS)", Total: "212.000000", Previous: "215.500000", DeltaPct: pctp(-1.62), Share: 0.2616, Resources: 102},
			{Key: "eip", Label: "Elastic IP", Total: "96.000000", Previous: "0.000000", DeltaPct: nil, Share: 0.1185, Resources: 6},
		},
		ServicesOther: &Group{Key: "other", Label: "Other", Total: "81.300000", Previous: "176.700000", DeltaPct: pctp(-54.0), Share: 0.1003, Resources: 6},
		Customers: []Group{
			{Key: "c-1", Label: "Acme", Total: "700.000000", Previous: "690.000000", DeltaPct: pctp(1.45), Share: 0.8638, Resources: 100},
			{Key: "c-2", Label: "Bravo Industries With A Very Long Name Ltd", Total: "110.400000", Previous: "100.200000", DeltaPct: pctp(10.18), Share: 0.1362, Resources: 26},
		},

		Budgets: []budget.Status{
			{ID: "b-1", Name: "Compute ceiling", CustomerName: strp("Acme"), Amount: "3000.000000", Currency: "OMR", Period: "2026-09", Actual: "700.000000", Forecast: &fc, PctActual: 23.33, PctForecast: &pf, Status: "ok"},
			{ID: "b-2", Name: "Sovereign cap", Amount: "800.000000", Currency: "OMR", Period: "2026-09", Actual: "810.400000", PctActual: 101.3, Status: "exceeded"},
		},

		AnomalyCount: 3,
		Anomalies: []Anomaly{
			{Day: "2026-09-04", CustomerName: "Acme", Label: "Elastic Cloud Server", Actual: "60.100000", Expected: 12.1, Impact: 48.0, Score: 4.21},
			{Day: "2026-09-02", CustomerName: "Bravo", Label: "Elastic IP", Actual: "9.000000", Expected: 3.0, Impact: 6.0, Score: 3.5},
		},

		RecommendationCount:    5,
		RecommendationSaving:   "120.500000",
		RecommendationCurrency: "OMR",
		Recommendations: []recommend.Recommendation{
			{Type: recommend.TypeStoppedInstanceBilled, Title: "Stopped instance is still billed", CustomerName: "Acme", ResourceName: "vm-batch-01", MonthlySaving: "45.000000", Currency: "OMR"},
			{Type: recommend.TypeUnattachedVolume, Title: "Unattached volume", CustomerName: "Acme", ResourceName: "vol-orphan", MonthlySaving: "40.500000", Currency: "OMR"},
			{Type: recommend.TypeUnboundEIP, Title: "Unbound elastic IP", CustomerName: "Bravo", ResourceName: "eip-3", MonthlySaving: "35.000000", Currency: "OMR"},
		},
		PublicURL: "https://billing.t99.omani.works",
	}
}

func strp(s string) *string { return &s }

func goldenPath(t *testing.T) string {
	t.Helper()
	return filepath.Join("testdata", "weekly-operator.golden.txt")
}

func TestRenderGolden(t *testing.T) {
	subject, body := Render(fixtureInput())
	got := "Subject: " + subject + "\n\n" + body
	path := goldenPath(t)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update to create): %v", err)
	}
	if string(want) != got {
		t.Fatalf("rendered report differs from %s\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}

// The golden must be discriminating: swapping two services changes the text.
func TestRenderDiscriminates(t *testing.T) {
	in := fixtureInput()
	_, base := Render(in)
	in.Services[0], in.Services[1] = in.Services[1], in.Services[0]
	_, swapped := Render(in)
	if base == swapped {
		t.Fatal("swapping two services did not change the rendered report")
	}
	in = fixtureInput()
	in.Total = "810.500000"
	subj, _ := Render(in)
	base0, _ := Render(fixtureInput())
	if subj == base0 {
		t.Fatal("a different total did not change the subject")
	}
}

func TestRenderLineWidthAndContent(t *testing.T) {
	subject, body := Render(fixtureInput())
	if utf8.RuneCountInString(subject) > MaxLineWidth {
		t.Fatalf("subject too long (%d): %q", utf8.RuneCountInString(subject), subject)
	}
	for i, line := range strings.Split(body, "\n") {
		if n := utf8.RuneCountInString(line); n > MaxLineWidth && !strings.HasPrefix(line, "http") {
			t.Errorf("line %d is %d columns: %q", i+1, n, line)
		}
	}
	for _, want := range []string{
		"1–7 Sep 2026",          // window label
		"810.400 OMR",           // total at OMR's 3 decimals
		"+2.6%",                 // vs previous period
		"2712.500",              // forecast
		"Elastic Cloud Server",  // top service
		"52.0%",                 // share
		"Acme",                  // top customer
		"exceeded",              // budget status
		"3 anomalous days",      // count
		"2026-09-04",            // biggest anomaly
		"5 recommendations",     // count
		"120.500 OMR per month", // saving total
		"k8s.vcpu",              // unpriced
		"Open the console:\nhttps://billing.t99.omani.works/reports", // footer, URL unclipped on its own line
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q\n%s", want, body)
		}
	}
	if !strings.Contains(subject, "Weekly ops") || !strings.Contains(subject, "810.400") {
		t.Errorf("subject = %q", subject)
	}
}

func TestRenderSectionsAndCustomerScope(t *testing.T) {
	in := fixtureInput()
	in.Sections = []string{SectionSummary, SectionBudgets}
	_, body := Render(in)
	for _, absent := range []string{"TOP SERVICES", "TOP CUSTOMERS", "ANOMALIES", "RECOMMENDATIONS"} {
		if strings.Contains(body, absent) {
			t.Errorf("section %q rendered although not selected", absent)
		}
	}
	if !strings.Contains(body, "SUMMARY") || !strings.Contains(body, "BUDGETS") {
		t.Error("selected sections missing")
	}
	// A customer-scoped report never lists customers, even if asked.
	in = fixtureInput()
	in.Operator = false
	in.Scope = "Acme"
	_, body = Render(in)
	if strings.Contains(body, "TOP CUSTOMERS") {
		t.Error("customer-scoped report rendered the customers section")
	}
	if !strings.Contains(body, "Scope: Acme") {
		t.Error("scope line missing")
	}
	// Empty sections state their absence in words.
	in = Input{Name: "Empty", Cadence: CadenceDaily, From: at(2026, 9, 6, 0, 0), To: at(2026, 9, 7, 0, 0), Currency: "OMR", Sections: Sections(), Operator: true, Total: "0"}
	_, body = Render(in)
	for _, want := range []string{"No priced usage", "No active budget", "No anomalous day", "Nothing to recommend"} {
		if !strings.Contains(body, want) {
			t.Errorf("empty report lacks %q\n%s", want, body)
		}
	}
}

func TestMoneyFormatting(t *testing.T) {
	cases := []struct {
		d    store.Decimal
		cur  string
		want string
	}{
		{"810.400000", "OMR", "810.400"},
		{"810.4005", "OMR", "810.401"}, // half-up at the minor unit
		{"810.4", "USD", "810.40"},
		{"", "USD", "0.00"},
		{"-12.5", "OMR", "-12.500"},
		{"0.0004", "OMR", "0.000"},
	}
	for _, c := range cases {
		if got := money(c.d, c.cur); got != c.want {
			t.Errorf("money(%q, %s) = %q want %q", c.d, c.cur, got, c.want)
		}
	}
	if pct(nil, true) != "n/a" || pct(pctp(5.84), true) != "+5.8%" || pct(pctp(-3.14), true) != "-3.1%" || pctF(52, false) != "52.0%" {
		t.Error("pct formatting")
	}
}

func TestRenderStatement(t *testing.T) {
	issued := at(2026, 9, 1, 8, 0)
	st := store.Statement{
		ID: "st-1", CustomerID: "c-1", CustomerName: "Acme", PeriodStart: "2026-08-01", PeriodEnd: "2026-08-31",
		Currency: "OMR", Subtotal: "850.000000", DiscountTotal: "150.000000", TaxRate: "0.0500", Tax: "42.500000", Total: "892.500000",
		Status: "issued", IssuedAt: &issued,
		Lines: []store.RatedLine{
			{SKU: "evs.ssd.gb", Unit: "gb-hour", Quantity: "74400.000000", UnitPrice: "0.00013699", Amount: "10.192000", ResourceCount: 3},
			{SKU: "ecs.s6.large.2", Unit: "instance-hour", Quantity: "744.000000", UnitPrice: "0.10000000", Amount: "744.000000", ResourceCount: 1},
			{SKU: "eip", Unit: "hour", Quantity: "744.000000", UnitPrice: "0.02", Amount: "14.880000", ResourceCount: 1},
		},
	}
	subject, body := RenderStatement(st, "https://billing.t99.omani.works/statements/st-1")
	if want := "Statement for Acme — August 2026: 892.500 OMR"; subject != want {
		t.Fatalf("subject = %q want %q", subject, want)
	}
	// Column padding is layout; the assertions read the words.
	flat := strings.Join(strings.Fields(body), " ")
	for _, want := range []string{
		"August 2026", "2026-08-01 to 2026-08-31", "1 Sep 2026 08:00 UTC",
		"List subtotal 1000.000 OMR", // net + discount, exact
		"Discounts -150.000 OMR",
		"Net subtotal 850.000 OMR",
		"Tax (5.0%) 42.500 OMR",
		"TOTAL 892.500 OMR",
		"https://billing.t99.omani.works/statements/st-1",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("body lacks %q\n%s", want, body)
		}
	}
	// Lines are listed largest first.
	if strings.Index(body, "ecs.s6.large.2") > strings.Index(body, "eip") || strings.Index(body, "eip ") > strings.Index(body, "evs.ssd.gb") {
		t.Errorf("lines not ordered by amount desc:\n%s", body)
	}
	for i, line := range strings.Split(body, "\n") {
		if n := utf8.RuneCountInString(line); n > MaxLineWidth && !strings.HasPrefix(line, "http") {
			t.Errorf("line %d is %d columns: %q", i+1, n, line)
		}
	}
	// A long link is never clipped: a cut URL is a dead link.
	long := "https://billing.t99.omani.works/statements/" + strings.Repeat("0123456789abcdef", 4)
	_, body3 := RenderStatement(st, long)
	if !strings.Contains(body3, "View the statement:\n"+long+"\n") {
		t.Errorf("link clipped:\n%s", body3)
	}
	// Swapping the discount changes the list subtotal, not the net.
	st.DiscountTotal = "50.000000"
	_, body2 := RenderStatement(st, "")
	if !strings.Contains(strings.Join(strings.Fields(body2), " "), "List subtotal 900.000 OMR") || strings.Contains(body2, "View the statement") {
		t.Errorf("discount change / no link:\n%s", body2)
	}
	_ = time.Now
}

// Unconverted usage (#6867 follow-up) renders as its own summary lines,
// naming the reporting currency the rate is missing to, and never adds to
// the total. A report with nothing unconverted prints no such line — the
// golden fixture above pins that.
func TestRenderUnconvertedUsage(t *testing.T) {
	in := fixtureInput()
	in.Unconverted = []store.UnconvertedCurrency{{Currency: "EUR", Records: 168, Cost: "67.200000"}, {Currency: "USD", Records: 1, Cost: "84.000000"}}
	_, body := Render(in)
	for _, want := range []string{"Unconverted usage (no exchange rate to OMR; left out of every total):", "67.20 EUR across 168 records", "84.00 USD across 1 record\n"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body lacks %q:\n%s", want, body)
		}
	}
	for _, line := range strings.Split(body, "\n") {
		if utf8.RuneCountInString(line) > MaxLineWidth {
			t.Fatalf("line over %d columns: %q", MaxLineWidth, line)
		}
	}
	_, plain := Render(fixtureInput())
	if strings.Contains(plain, "Unconverted") {
		t.Fatal("a report with nothing unconverted must not mention it")
	}
}
