package budget

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/notify"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The threshold-crossing notice moved from crossingMail here to the
// budget.threshold template (DESIGN.md §21). The payload this package builds
// and that template have to keep agreeing; the expectations below are
// transcribed from crossingMail as it stood before §21.

type capture struct {
	mu   sync.Mutex
	sent []string
}

func (c *capture) Send(_ context.Context, to, subject, body string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, to+"\x00"+subject+"\x00"+body)
	return nil
}

func emitCrossing(t *testing.T, b store.Budget, st Status, pct int) (subject, body string) {
	t.Helper()
	cap := &capture{}
	n := &notify.Notifier{Channels: notify.DefaultChannels(cap)}
	if _, err := n.Send(context.Background(), notify.Request{
		Event: notify.EventBudgetThreshold, To: "fin@acme.example", Payload: crossingPayload(b, st, pct),
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(cap.sent) != 1 {
		t.Fatalf("captured %d messages", len(cap.sent))
	}
	parts := strings.SplitN(cap.sent[0], "\x00", 3)
	return parts[1], parts[2]
}

func TestCrossingRendersExactlyWhatTheOldRendererDid(t *testing.T) {
	cust := "c-acme"
	name := "Acme Org"
	forecast, pctForecast := 121.4286, 121.4286

	b := store.Budget{ID: "b-acme", Name: "Acme cap", CustomerID: &cust, CustomerName: &name, Amount: "100.000000", Currency: "OMR"}
	st := Status{Period: "2026-09", Actual: "85.000000", PctActual: 85.0, Status: "over", Forecast: &forecast, PctForecast: &pctForecast}

	subject, body := emitCrossing(t, b, st, 80)
	if want := "Budget Acme cap: 80% of 100 OMR reached for 2026-09"; subject != want {
		t.Errorf("subject\n got %q\nwant %q", subject, want)
	}
	want := "Budget \"Acme cap\" (Acme Org) has reached 80% of its 100 OMR cap for 2026-09.\n\n" +
		"Actual so far: 85 OMR (85.0% of the budget)\n" +
		"Month-end forecast: 121.43 OMR (121.4% of the budget)\n" +
		"Status: over\n"
	if body != want {
		t.Errorf("body\n got %q\nwant %q", body, want)
	}

	// No customer at all: the scope reads "all customers".
	global := store.Budget{ID: "b-all", Name: "Sovereign cap", Amount: "1000.000000", Currency: "OMR"}
	plain := Status{Period: "2026-09", Actual: "500.000000", PctActual: 50.0, Status: "on track"}
	subject, body = emitCrossing(t, global, plain, 50)
	if want := "Budget Sovereign cap: 50% of 1000 OMR reached for 2026-09"; subject != want {
		t.Errorf("subject\n got %q\nwant %q", subject, want)
	}
	want = "Budget \"Sovereign cap\" (all customers) has reached 50% of its 1000 OMR cap for 2026-09.\n\n" +
		"Actual so far: 500 OMR (50.0% of the budget)\n" +
		"Status: on track\n"
	if body != want {
		t.Errorf("body without a forecast\n got %q\nwant %q", body, want)
	}

	// A customer with no NAME on the budget falls back to its id, exactly
	// as the old renderer did.
	noName := store.Budget{ID: "b-x", Name: "Cap", CustomerID: &cust, Amount: "10.000000", Currency: "OMR"}
	_, body = emitCrossing(t, noName, plain, 50)
	if !strings.Contains(body, "(customer c-acme)") {
		t.Errorf("body = %q", body)
	}

	// A forecast of EXACTLY zero is still a forecast and must print. This is
	// why the payload carries a pre-formatted string rather than a number
	// the template tests for truthiness.
	zero := 0.0
	zeroPct := 0.0
	withZero := Status{Period: "2026-09", Actual: "0.000000", PctActual: 0, Status: "on track", Forecast: &zero, PctForecast: &zeroPct}
	_, body = emitCrossing(t, global, withZero, 50)
	if !strings.Contains(body, "Month-end forecast: 0.00 OMR (0.0% of the budget)") {
		t.Errorf("a zero forecast must still print: %q", body)
	}
}

// The budget alert is NOT mandatory — a recipient may switch it off. It is
// an advisory, unlike an invoice or a dunning notice.
func TestBudgetThresholdIsOptionalButDefaultsOn(t *testing.T) {
	e, ok := notify.Lookup(notify.EventBudgetThreshold)
	if !ok {
		t.Fatal("budget.threshold is not in the catalogue")
	}
	if e.Mandatory {
		t.Fatal("a budget alert is an advisory; it must be switchable")
	}
	if !e.DefaultOn {
		t.Fatal("it was sent unconditionally before §21, so an unset preference must still receive it")
	}
}
