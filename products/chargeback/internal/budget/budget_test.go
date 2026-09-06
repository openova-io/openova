package budget

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

func in(amount string, thresholds ...int) Input {
	if thresholds == nil {
		thresholds = []int{50, 80, 100}
	}
	return Input{ID: "b-1", Name: "Acme monthly", Amount: store.Decimal(amount), Currency: "OMR", Period: "2026-09", Thresholds: thresholds}
}

func fp(v float64) *float64 { return &v }

func crossed(s Status) []bool {
	out := make([]bool, len(s.Thresholds))
	for i, t := range s.Thresholds {
		out[i] = t.Crossed
	}
	return out
}

func eq(a, b []bool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The boundaries are inclusive (≥), and exceeded outranks warning. Each case
// sits exactly on a boundary so a mutant using > or swapping the two
// statuses fails.
func TestEvaluateBoundaries(t *testing.T) {
	cases := []struct {
		name     string
		actual   string
		forecast *float64
		status   string
		crossed  []bool
		pct      float64
	}{
		{"below every threshold", "49.999999", nil, StatusOK, []bool{false, false, false}, 49.999999},
		{"exactly the lowest threshold", "50", nil, StatusWarning, []bool{true, false, false}, 50},
		{"between thresholds", "79.99", nil, StatusWarning, []bool{true, false, false}, 79.99},
		{"exactly the middle threshold", "80", nil, StatusWarning, []bool{true, true, false}, 80},
		{"just under the cap", "99.999999", nil, StatusWarning, []bool{true, true, false}, 99.999999},
		{"exactly the cap is exceeded, not warning", "100", nil, StatusExceeded, []bool{true, true, true}, 100},
		{"over the cap", "150", nil, StatusExceeded, []bool{true, true, true}, 150},
		{"forecast just under the cap stays ok", "10", fp(99.999), StatusOK, []bool{false, false, false}, 10},
		{"forecast exactly at the cap warns", "10", fp(100), StatusWarning, []bool{false, false, false}, 10},
		{"forecast over the cap warns", "10", fp(250), StatusWarning, []bool{false, false, false}, 10},
		{"actual at the cap with a low forecast is still exceeded", "100", fp(10), StatusExceeded, []bool{true, true, true}, 100},
		{"zero spend", "0", nil, StatusOK, []bool{false, false, false}, 0},
		{"empty actual reads as zero", "", nil, StatusOK, []bool{false, false, false}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := Evaluate(in("100"), store.Decimal(c.actual), c.forecast, nil)
			if s.Status != c.status {
				t.Fatalf("status = %q, want %q", s.Status, c.status)
			}
			if !eq(crossed(s), c.crossed) {
				t.Fatalf("crossed = %v, want %v", crossed(s), c.crossed)
			}
			if s.PctActual != c.pct {
				t.Fatalf("pct_actual = %v, want %v", s.PctActual, c.pct)
			}
			if c.forecast == nil && (s.PctForecast != nil || s.Forecast != nil) {
				t.Fatalf("no forecast given but pct_forecast=%v forecast=%v", s.PctForecast, s.Forecast)
			}
			if c.forecast != nil && (s.PctForecast == nil || *s.PctForecast != *c.forecast || s.Forecast == nil || *s.Forecast != *c.forecast) {
				t.Fatalf("forecast %v → pct_forecast %v", *c.forecast, s.PctForecast)
			}
		})
	}
}

// Crossing is decided on the exact rational, never on float64: 0.0021 of a
// 0.03 budget IS 7%, while float64 computes 6.9999999999999991 and would
// miss the threshold.
func TestEvaluateExactPercentage(t *testing.T) {
	s := Evaluate(in("0.03", 7), "0.0021", nil, nil)
	if !s.Thresholds[0].Crossed || s.Status != StatusWarning {
		t.Fatalf("0.0021 / 0.03 must cross 7%% exactly: %+v", s)
	}
	// One unit of the last money decimal below is not crossed.
	s = Evaluate(in("0.03", 7), "0.002099", nil, nil)
	if s.Thresholds[0].Crossed || s.Status != StatusOK {
		t.Fatalf("0.002099 / 0.03 must not cross 7%%: %+v", s)
	}
	// Large money keeps its precision too.
	s = Evaluate(in("123456789012.345678", 50), "61728394506.172839", nil, nil)
	if !s.Thresholds[0].Crossed {
		t.Fatal("half of the budget, to the micro-unit, must cross 50%")
	}
	s = Evaluate(in("123456789012.345678", 50), "61728394506.172838", nil, nil)
	if s.Thresholds[0].Crossed {
		t.Fatal("one micro-unit under half must not cross 50%")
	}
}

// A zero budget cannot be divided by: pct is 0, nothing is crossed, the
// forecast percentage is absent, and the status is ok.
func TestEvaluateZeroAmount(t *testing.T) {
	s := Evaluate(in("0"), "42", fp(500), nil)
	if s.PctActual != 0 || s.PctForecast != nil || s.Status != StatusOK || !eq(crossed(s), []bool{false, false, false}) {
		t.Fatalf("zero-amount status = %+v", s)
	}
	if s.Forecast == nil || *s.Forecast != 500 {
		t.Fatal("the forecast value itself is still reported")
	}
	s = Evaluate(in(""), "42", nil, nil)
	if s.PctActual != 0 || s.Status != StatusOK {
		t.Fatalf("empty-amount status = %+v", s)
	}
}

func TestEvaluateThresholdsNormalizedAndAlerts(t *testing.T) {
	at := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	s := Evaluate(in("100", 80, 50, 50, 100), "60", nil, []Alert{{Threshold: 50, At: at}, {Threshold: 999, At: at}})
	if len(s.Thresholds) != 3 || s.Thresholds[0].Pct != 50 || s.Thresholds[1].Pct != 80 || s.Thresholds[2].Pct != 100 {
		t.Fatalf("thresholds = %+v", s.Thresholds)
	}
	if s.Thresholds[0].AlertedAt == nil || !s.Thresholds[0].AlertedAt.Equal(at) {
		t.Fatalf("50 alerted_at = %v", s.Thresholds[0].AlertedAt)
	}
	if s.Thresholds[1].AlertedAt != nil || s.Thresholds[2].AlertedAt != nil {
		t.Fatal("un-alerted thresholds must carry alerted_at = nil")
	}
	// Warning is judged against the LOWEST threshold, whatever order they came in.
	if s.Status != StatusWarning || !s.Thresholds[0].Crossed || s.Thresholds[1].Crossed {
		t.Fatalf("status = %+v", s)
	}
	// No thresholds: only the cap and the forecast decide.
	s = Evaluate(Input{Amount: "100", Thresholds: []int{}}, "90", nil, nil)
	if s.Status != StatusOK || len(s.Thresholds) != 0 {
		t.Fatalf("no-threshold status = %+v", s)
	}
	s = Evaluate(Input{Amount: "100", Thresholds: nil}, "100", nil, nil)
	if s.Status != StatusExceeded {
		t.Fatalf("no-threshold at cap = %+v", s)
	}
}

// Every key of types.ts BudgetStatus / BudgetThreshold is on the wire, with
// the nullable ones present as null rather than omitted.
func TestStatusWireShape(t *testing.T) {
	s := Evaluate(in("100"), "50", nil, nil)
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"id", "name", "customer_id", "customer_name", "amount", "currency", "period", "actual", "forecast", "pct_actual", "pct_forecast", "status", "thresholds"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("status JSON lacks %q: %s", k, b)
		}
	}
	if m["customer_id"] != nil || m["forecast"] != nil || m["pct_forecast"] != nil {
		t.Fatalf("nullable fields must be null, got %s", b)
	}
	if _, isNum := m["amount"].(float64); !isNum {
		t.Fatalf("amount must be a JSON number: %s", b)
	}
	if _, isNum := m["actual"].(float64); !isNum {
		t.Fatalf("actual must be a JSON number: %s", b)
	}
	th := m["thresholds"].([]any)[0].(map[string]any)
	for _, k := range []string{"pct", "crossed", "alerted_at"} {
		if _, ok := th[k]; !ok {
			t.Fatalf("threshold JSON lacks %q: %s", k, b)
		}
	}
}

func TestParsePeriodAndMonthStart(t *testing.T) {
	m, err := ParsePeriod("2026-09")
	if err != nil || !m.Equal(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("ParsePeriod = %v, %v", m, err)
	}
	for _, bad := range []string{"2026-9", "2026-09-01", "Sep 2026", ""} {
		if _, err := ParsePeriod(bad); err == nil {
			t.Fatalf("ParsePeriod(%q) accepted", bad)
		}
	}
	if got := MonthStart(time.Date(2026, 9, 17, 23, 59, 0, 0, time.FixedZone("x", 4*3600))); !got.Equal(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("MonthStart = %v", got)
	}
}

func TestTrimDec(t *testing.T) {
	for in, want := range map[store.Decimal]string{"200.000000": "200", "12.500000": "12.5", "0.000000": "0", "": "0", "7": "7", "0.000300": "0.0003"} {
		if got := trimDec(in); got != want {
			t.Fatalf("trimDec(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Evaluator over an in-memory store: once-only per (budget, period, threshold)
// ---------------------------------------------------------------------------

type alertKey struct {
	budget, period string
	threshold      int
}

type fakeStore struct {
	mu      sync.Mutex
	budgets []store.Budget
	actual  map[string]store.Decimal // budget id → month total
	alerts  map[alertKey]time.Time
	audits  []string
}

func (f *fakeStore) Explore(_ context.Context, _ store.Scope, q store.CostQuery) (store.ExploreResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// The fake keys its ledger by customer so the window narrowing is honoured.
	total := f.actual[q.CustomerID]
	if total == "" {
		total = "0"
	}
	return store.ExploreResult{Granularity: "day", Total: store.CostTotal{Current: total}}, nil
}

func (f *fakeStore) ListBudgetAlerts(_ context.Context, id string) ([]store.BudgetAlert, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.BudgetAlert
	for k, at := range f.alerts {
		if k.budget == id {
			out = append(out, store.BudgetAlert{BudgetID: id, Period: k.period, Threshold: k.threshold, At: at})
		}
	}
	return out, nil
}

func (f *fakeStore) ListActiveBudgets(context.Context) ([]store.Budget, error) {
	var out []store.Budget
	for _, b := range f.budgets {
		if b.Active {
			out = append(out, b)
		}
	}
	return out, nil
}

func (f *fakeStore) RecordBudgetAlert(_ context.Context, id, period string, threshold int, _ store.Decimal) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := alertKey{id, period, threshold}
	if _, ok := f.alerts[k]; ok {
		return false, nil
	}
	f.alerts[k] = time.Now()
	return true, nil
}

func (f *fakeStore) Audit(_ context.Context, _ *string, actor, action string, _ any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audits = append(f.audits, actor+":"+action)
	return nil
}

type fakeMail struct {
	mu   sync.Mutex
	sent []string
}

func (m *fakeMail) Send(_ context.Context, to, subject, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, to+"|"+subject)
	return nil
}

func TestEvaluatorRecordsEachCrossingOnce(t *testing.T) {
	cust := "c-acme"
	fs := &fakeStore{
		budgets: []store.Budget{
			{ID: "b-acme", Name: "Acme cap", CustomerID: &cust, Amount: "100", Currency: "OMR", Thresholds: []int{50, 80, 100}, NotifyEmails: []string{"fin@acme.example", "cfo@acme.example"}, Active: true},
			{ID: "b-all", Name: "Sovereign cap", Amount: "1000", Currency: "OMR", Thresholds: []int{50, 80, 100}, NotifyEmails: []string{"ops@nc.example"}, Active: true},
			{ID: "b-off", Name: "Disabled", CustomerID: &cust, Amount: "1", Currency: "OMR", Thresholds: []int{50}, NotifyEmails: []string{"never@acme.example"}, Active: false},
		},
		actual: map[string]store.Decimal{cust: "85", "": "120"},
		alerts: map[alertKey]time.Time{},
	}
	fm := &fakeMail{}
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	ev := &Evaluator{Store: fs, Mail: fm, Now: func() time.Time { return now }}

	rep, err := ev.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Acme: 85% crosses 50 and 80 (two crossings × two recipients); the
	// global budget at 12% crosses nothing; the disabled one is skipped.
	if rep.Budgets != 2 || rep.Crossings != 2 || rep.Mails != 4 || rep.Errors != 0 {
		t.Fatalf("first run = %+v", rep)
	}
	if len(fs.alerts) != 2 || len(fs.audits) != 2 || fs.audits[0] != "system:budget.threshold" {
		t.Fatalf("alerts=%v audits=%v", fs.alerts, fs.audits)
	}
	if _, ok := fs.alerts[alertKey{"b-acme", "2026-09", 80}]; !ok {
		t.Fatalf("80%% crossing not recorded for 2026-09: %v", fs.alerts)
	}
	for _, m := range fm.sent {
		if !strings.HasSuffix(m, "|Budget Acme cap: 50% of 100 OMR reached for 2026-09") && !strings.HasSuffix(m, "|Budget Acme cap: 80% of 100 OMR reached for 2026-09") {
			t.Fatalf("unexpected mail %q", m)
		}
	}

	// Same state, second run: nothing new.
	rep, err = ev.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Crossings != 0 || rep.Mails != 0 || len(fs.alerts) != 2 || len(fm.sent) != 4 || len(fs.audits) != 2 {
		t.Fatalf("second run must be a no-op: rep=%+v alerts=%d mails=%d audits=%d", rep, len(fs.alerts), len(fm.sent), len(fs.audits))
	}

	// Spend grows past the cap: only the NEW crossing (100) is sent.
	fs.actual[cust] = "101"
	rep, _ = ev.RunOnce(context.Background())
	if rep.Crossings != 1 || rep.Mails != 2 || len(fs.alerts) != 3 {
		t.Fatalf("growth run = %+v alerts=%d", rep, len(fs.alerts))
	}

	// A new month starts the ledger over: the same thresholds alert again
	// under the new period key, never under the old one.
	now = time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	rep, _ = ev.RunOnce(context.Background())
	if rep.Crossings != 3 || len(fs.alerts) != 6 {
		t.Fatalf("new-month run = %+v alerts=%d", rep, len(fs.alerts))
	}
	if _, ok := fs.alerts[alertKey{"b-acme", "2026-10", 100}]; !ok {
		t.Fatalf("October crossing not keyed by its period: %v", fs.alerts)
	}
}

// Run honours the initial delay and stops with the context.
func TestEvaluatorRunStopsWithContext(t *testing.T) {
	fs := &fakeStore{alerts: map[alertKey]time.Time{}, actual: map[string]store.Decimal{}}
	ev := &Evaluator{Store: fs, Mail: &fakeMail{}, InitialDelay: 5 * time.Millisecond, Interval: 5 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() { ev.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after the context ended")
	}
}
