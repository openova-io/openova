package settle

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// fake is a second gateway implementation — the shape "adding Omantel's
// gateway" takes: two methods and one Register call.
type fake struct {
	name  string
	calls int
}

func (f *fake) RequestSettlement(_ context.Context, req Request) (Result, error) {
	f.calls++
	return Result{Outcome: Pending, Gateway: f.name, Reference: req.Statement.ID, PayURL: "https://pay.example/" + req.Statement.ID}, nil
}

func (f *fake) ConfirmSettlement(_ context.Context, c Confirmation) (Payment, error) {
	return Normalise(c, f.name)
}

func billed(model, method, gateway string) store.Customer {
	return store.Customer{Slug: "c", Charging: store.ChargingBilled, PaymentModel: model, PaymentMethod: method, GatewayName: gateway}
}

// The registry routes on the customer's commercial fields alone.
func TestRegistryRoutesOnTheCommercialFields(t *testing.T) {
	ctx := context.Background()
	stripe := &fake{name: "stripe"}
	omantel := &fake{name: "omantel"}
	r := NewRegistry()
	r.Register(GatewayStripe, stripe)
	r.Register("omantel", omantel)

	if got := r.Gateways(); len(got) != 2 || got[0] != "omantel" || got[1] != "stripe" {
		t.Fatalf("registered gateways = %v", got)
	}

	st := store.Statement{ID: "s-1", Currency: "OMR", Total: "10.000000", InvoiceNumber: "INV-2026-00001", PORef: "PO-1"}

	// Gateway customers reach the implementation their gateway_name names,
	// and nobody else's.
	res, err := r.RequestSettlement(ctx, st, billed(store.PaymentModelPrepaid, MethodGateway, GatewayStripe))
	if err != nil || res.Gateway != "stripe" || res.Outcome != Pending || stripe.calls != 1 || omantel.calls != 0 {
		t.Fatalf("stripe route: %+v err=%v stripe=%d omantel=%d", res, err, stripe.calls, omantel.calls)
	}
	res, err = r.RequestSettlement(ctx, st, billed(store.PaymentModelPrepaid, MethodGateway, "omantel"))
	if err != nil || res.Gateway != "omantel" || omantel.calls != 1 || stripe.calls != 1 {
		t.Fatalf("omantel route: %+v err=%v", res, err)
	}

	// Transfer and internal are served by the built-in manual gateway; no
	// registration, and no external call.
	for _, method := range []string{MethodTransfer, MethodInternal} {
		res, err := r.RequestSettlement(ctx, st, billed(store.PaymentModelPostpaid, method, ""))
		if err != nil || res.Gateway != "manual" || res.Outcome != AwaitingTransfer {
			t.Fatalf("%s route: %+v err=%v", method, res, err)
		}
	}
	if stripe.calls != 1 || omantel.calls != 1 {
		t.Fatalf("a manual route must call no gateway: stripe=%d omantel=%d", stripe.calls, omantel.calls)
	}

	// An informational customer settles nowhere, and that is not an error.
	res, err = r.RequestSettlement(ctx, st, store.Customer{Charging: store.ChargingInformational})
	if err != nil || res.Outcome != NotApplicable {
		t.Fatalf("informational: %+v err=%v", res, err)
	}

	// A gateway name nothing is registered under is an error the operator
	// can see, never a silent success.
	res, err = r.RequestSettlement(ctx, st, billed(store.PaymentModelPrepaid, MethodGateway, "not-wired"))
	if !errors.Is(err, ErrNoGateway) || res.Outcome != NotApplicable {
		t.Fatalf("unregistered gateway: %+v err=%v", res, err)
	}
}

// The manual gateway collects nothing and says what happens next; its
// confirmation is the normalised fact the store books.
func TestManualGateway(t *testing.T) {
	ctx := context.Background()
	due := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	st := store.Statement{ID: "s-1", Total: "100.000000", InvoiceNumber: "INV-2026-00007", PORef: "PO-42", DueAt: &due}

	res, err := Manual{}.RequestSettlement(ctx, Request{Statement: st, Method: MethodTransfer})
	if err != nil || res.Outcome != AwaitingTransfer {
		t.Fatalf("transfer: %+v err=%v", res, err)
	}
	for _, want := range []string{"INV-2026-00007", "PO-42", "2026-10-01"} {
		if !strings.Contains(res.Detail, want) {
			t.Errorf("the detail must name %s: %q", want, res.Detail)
		}
	}
	res, _ = Manual{}.RequestSettlement(ctx, Request{Statement: st, Method: MethodInternal})
	if !strings.Contains(res.Detail, "internal recharge") {
		t.Errorf("internal detail = %q", res.Detail)
	}

	paid := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	p, err := Manual{}.ConfirmSettlement(ctx, Confirmation{Amount: " 40.500000 ", PaidAt: paid, Reference: " TRF-9 "})
	if err != nil || string(p.Amount) != "40.500000" || p.Reference != "TRF-9" || !p.PaidAt.Equal(paid) || p.Gateway != "manual" {
		t.Fatalf("confirmation = %+v err=%v", p, err)
	}
	// A missing date is now; a missing or non-numeric amount is refused.
	if p, err := (Manual{}).ConfirmSettlement(ctx, Confirmation{Amount: "1"}); err != nil || p.PaidAt.IsZero() {
		t.Fatalf("no date: %+v err=%v", p, err)
	}
	for _, bad := range []store.Decimal{"", "   ", "forty"} {
		if _, err := (Manual{}).ConfirmSettlement(ctx, Confirmation{Amount: bad}); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("amount %q = %v, want ErrInvalid", bad, err)
		}
	}
}

// A pre-seam StatementHook still works as the stripe gateway.
func TestFromHookAdaptsTheLegacyStatementHook(t *testing.T) {
	calls := 0
	g := FromHook(hookFunc(func() error { calls++; return nil }))
	res, err := g.RequestSettlement(context.Background(), Request{Statement: store.Statement{ID: "s-1"}})
	if err != nil || res.Outcome != Settled || calls != 1 {
		t.Fatalf("hook gateway: %+v err=%v calls=%d", res, err, calls)
	}
	boom := errors.New("billing said no")
	g = FromHook(hookFunc(func() error { return boom }))
	if _, err := g.RequestSettlement(context.Background(), Request{}); !errors.Is(err, boom) {
		t.Fatalf("a hook error must surface: %v", err)
	}
	if FromHook(nil) != nil {
		t.Error("no hook is no gateway")
	}
}

type hookFunc func() error

func (f hookFunc) StatementIssued(context.Context, store.Statement, store.Customer) error { return f() }
