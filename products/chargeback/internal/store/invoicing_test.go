package store_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The lifecycle table, as prose, is the whole contract of DESIGN.md §8:
//
//	draft     → issued, cancelled
//	issued    → sent, paid, cancelled
//	sent      → paid, overdue
//	overdue   → paid
//	paid      → (final)
//	cancelled → (final)
//
// Pinned here so a later edit that quietly widens it fails a test rather than
// letting a paid invoice be re-opened.
func TestLegalStatementTransitions(t *testing.T) {
	legal := map[string][]string{
		store.StatusDraft:     {store.StatusIssued, store.StatusCancelled},
		store.StatusIssued:    {store.StatusSent, store.StatusPaid, store.StatusCancelled},
		store.StatusSent:      {store.StatusPaid, store.StatusOverdue},
		store.StatusOverdue:   {store.StatusPaid},
		store.StatusPaid:      {},
		store.StatusCancelled: {},
	}
	allowed := func(from, to string) bool {
		for _, s := range legal[from] {
			if s == to {
				return true
			}
		}
		return false
	}
	for _, from := range store.StatementStatuses {
		for _, to := range store.StatementStatuses {
			want := allowed(from, to)
			if got := store.LegalStatementTransition(from, to); got != want {
				t.Errorf("%s → %s = %v, want %v", from, to, got, want)
			}
		}
	}
	// The two terminal states accept nothing at all, including themselves.
	for _, final := range []string{store.StatusPaid, store.StatusCancelled} {
		if n := store.NextStatementStatuses(final); len(n) != 0 {
			t.Errorf("%s is terminal but offers %v", final, n)
		}
	}
	// A status nobody defined is not a doorway into the lifecycle.
	if store.LegalStatementTransition("weird", store.StatusPaid) {
		t.Error("an unknown status must reach nothing")
	}
}

// Overdue is DERIVED from the due date and the outstanding balance, so it is
// true of the clock rather than of the last sweep: no cron, no drift.
func TestEffectiveStatusIsDerivedFromTheDueDate(t *testing.T) {
	due := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	before := due.Add(-time.Minute)
	after := due.Add(time.Minute)
	sent := store.Statement{Status: store.StatusSent, Total: "100.000000", Paid: "0.000000", DueAt: &due}

	if got := sent.EffectiveStatusAt(before); got != store.StatusSent {
		t.Errorf("a minute before the due date = %s, want sent", got)
	}
	if got := sent.EffectiveStatusAt(due); got != store.StatusSent {
		t.Errorf("exactly at the due date = %s, want sent — the day it is due is not late", got)
	}
	if got := sent.EffectiveStatusAt(after); got != store.StatusOverdue {
		t.Errorf("a minute after the due date = %s, want overdue", got)
	}

	// Money settles the question whatever the clock says.
	settled := sent
	settled.Paid = "100.000000"
	if got := settled.EffectiveStatusAt(after); got != store.StatusSent {
		t.Errorf("a fully paid statement past its due date = %s, want sent (nothing is outstanding)", got)
	}
	part := sent
	part.Paid = "99.999999"
	if got := part.EffectiveStatusAt(after); got != store.StatusOverdue {
		t.Errorf("a part-paid statement past its due date = %s, want overdue", got)
	}

	// Only a SENT invoice can go overdue: a draft has no due date and an
	// issued one has not reached the customer yet.
	for _, st := range []string{store.StatusDraft, store.StatusIssued, store.StatusPaid, store.StatusCancelled} {
		s := sent
		s.Status = st
		if got := s.EffectiveStatusAt(after); got != st {
			t.Errorf("%s past the due date = %s, want %s", st, got, st)
		}
	}
	// A sent statement with no due date at all (terms of 0 days are still a
	// date; a missing one is a statement issued before invoicing existed).
	noDue := sent
	noDue.DueAt = nil
	if got := noDue.EffectiveStatusAt(after); got != store.StatusSent {
		t.Errorf("no due date = %s, want sent", got)
	}
	// Balance is exact, never float: 0.1 + 0.2 must leave 0.7 of 1.0.
	third := store.Statement{Status: store.StatusSent, Total: "1.000000", Paid: "0.300000"}
	if got := third.OutstandingAt(); string(got) != "0.700000" {
		t.Errorf("outstanding = %s, want 0.700000", got)
	}
}

// The retired trio maps onto the four fields exactly, and back — which is
// what lets billing_mode stay a derived column no one writes (DESIGN.md §8).
func TestBillingModeMapsBothWaysExactly(t *testing.T) {
	cases := []struct {
		mode string
		want store.Commercial
	}{
		{store.BillingModeShowback, store.Commercial{Charging: store.ChargingInformational}},
		{store.BillingModeChargeback, store.Commercial{Charging: store.ChargingBilled, PaymentModel: store.PaymentModelPostpaid, PaymentMethod: store.PaymentMethodInternal}},
		{store.BillingModeReal, store.Commercial{Charging: store.ChargingBilled, PaymentModel: store.PaymentModelPrepaid, PaymentMethod: store.PaymentMethodGateway, GatewayName: store.GatewayStripe}},
		// Anything unknown — including a CR with no billingMode at all —
		// lands on informational: visibility without invoicing is the safe
		// floor, which is what the old default meant.
		{"", store.Commercial{Charging: store.ChargingInformational}},
		{"nonsense", store.Commercial{Charging: store.ChargingInformational}},
	}
	for _, c := range cases {
		got := store.CommercialFromBillingMode(c.mode)
		if got != c.want {
			t.Errorf("%q → %+v, want %+v", c.mode, got, c.want)
		}
		if err := got.Validate(); err != nil {
			t.Errorf("%q maps to a combination that does not validate: %v", c.mode, err)
		}
	}
	// And back, for the three that round-trip.
	for _, mode := range []string{store.BillingModeShowback, store.BillingModeChargeback, store.BillingModeReal} {
		if back := store.CommercialFromBillingMode(mode).BillingMode(); back != mode {
			t.Errorf("%s → %+v → %s", mode, store.CommercialFromBillingMode(mode), back)
		}
	}
	// A billed customer paying by TRANSFER has no mode of its own — it was
	// exactly the case the three labels could not express — and derives
	// `real`, the closest of the three.
	transfer := store.Commercial{Charging: store.ChargingBilled, PaymentModel: store.PaymentModelPostpaid, PaymentMethod: store.PaymentMethodTransfer}
	if got := transfer.BillingMode(); got != store.BillingModeReal {
		t.Errorf("post-paid transfer derives %s, want real", got)
	}
}

// The "only meaningful when billed" rules, enforced in the store rather than
// left to the UI. Every refusal names the field.
func TestCommercialValidation(t *testing.T) {
	billed := func(model, method, gateway string) store.Commercial {
		return store.Commercial{Charging: store.ChargingBilled, PaymentModel: model, PaymentMethod: method, GatewayName: gateway}
	}
	ok := []store.Commercial{
		{Charging: store.ChargingInformational},
		billed(store.PaymentModelPrepaid, store.PaymentMethodGateway, store.GatewayStripe),
		billed(store.PaymentModelPrepaid, store.PaymentMethodGateway, "omantel"),
		billed(store.PaymentModelPostpaid, store.PaymentMethodTransfer, ""),
		billed(store.PaymentModelPostpaid, store.PaymentMethodInternal, ""),
		billed(store.PaymentModelPrepaid, store.PaymentMethodInternal, ""),
	}
	for _, c := range ok {
		if err := c.Validate(); err != nil {
			t.Errorf("%+v must be accepted: %v", c, err)
		}
	}
	bad := []struct {
		c    store.Commercial
		says string
	}{
		{store.Commercial{Charging: "free"}, "charging"},
		{store.Commercial{Charging: store.ChargingInformational, PaymentModel: store.PaymentModelPrepaid}, "payment_model"},
		{store.Commercial{Charging: store.ChargingInformational, PaymentMethod: store.PaymentMethodTransfer}, "payment_method"},
		{store.Commercial{Charging: store.ChargingInformational, GatewayName: store.GatewayStripe}, "gateway_name"},
		{billed("", store.PaymentMethodTransfer, ""), "payment_model"},
		{billed(store.PaymentModelPrepaid, "", ""), "payment_method"},
		{billed(store.PaymentModelPrepaid, "cheque", ""), "payment_method"},
		{billed(store.PaymentModelPrepaid, store.PaymentMethodGateway, ""), "gateway_name"},
		{billed(store.PaymentModelPostpaid, store.PaymentMethodTransfer, store.GatewayStripe), "gateway_name"},
	}
	for _, b := range bad {
		err := b.c.Validate()
		if err == nil {
			t.Errorf("%+v must be refused", b.c)
			continue
		}
		if !errors.Is(err, store.ErrInvalid) || !strings.Contains(err.Error(), b.says) {
			t.Errorf("%+v: message %q must name %s and wrap ErrInvalid", b.c, err, b.says)
		}
	}
	// Normalizing case-folds and defaults an unset charging to informational.
	n := store.Commercial{Charging: " BILLED ", PaymentModel: "Prepaid", PaymentMethod: "Gateway", GatewayName: " Stripe "}.Normalized()
	if n != (store.Commercial{Charging: store.ChargingBilled, PaymentModel: store.PaymentModelPrepaid, PaymentMethod: store.PaymentMethodGateway, GatewayName: store.GatewayStripe}) {
		t.Errorf("normalised = %+v", n)
	}
	if got := (store.Commercial{}).Normalized().Charging; got != store.ChargingInformational {
		t.Errorf("nothing chosen normalises to %s, want informational", got)
	}
}

// A partial patch merges onto what is stored, and switching to
// informational clears the fields that then have no meaning rather than
// leaving a combination the database would refuse.
func TestCommercialMergeClearsWhatBecomesMeaningless(t *testing.T) {
	sp := func(s string) *string { return &s }
	stripe := store.Commercial{Charging: store.ChargingBilled, PaymentModel: store.PaymentModelPrepaid, PaymentMethod: store.PaymentMethodGateway, GatewayName: store.GatewayStripe}

	// Switching a gateway customer to a bank transfer drops the gateway name.
	got := stripe.Merge(nil, sp(store.PaymentModelPostpaid), sp(store.PaymentMethodTransfer), nil)
	want := store.Commercial{Charging: store.ChargingBilled, PaymentModel: store.PaymentModelPostpaid, PaymentMethod: store.PaymentMethodTransfer}
	if got != want {
		t.Errorf("to transfer = %+v, want %+v", got, want)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("the merged value must validate: %v", err)
	}
	// Switching to informational clears all three.
	got = stripe.Merge(sp(store.ChargingInformational), nil, nil, nil)
	if got != (store.Commercial{Charging: store.ChargingInformational}) {
		t.Errorf("to informational = %+v", got)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("the merged value must validate: %v", err)
	}
	// Nothing given leaves the stored value alone.
	if got := stripe.Merge(nil, nil, nil, nil); got != stripe {
		t.Errorf("empty patch = %+v, want unchanged", got)
	}
	// Switching an informational customer to billed still has to be told
	// how — the merge does not invent a model or a method.
	if err := (store.Commercial{Charging: store.ChargingInformational}).Merge(sp(store.ChargingBilled), nil, nil, nil).Validate(); err == nil {
		t.Error("billed with no payment model or method must be refused")
	}
}

func TestInvoiceNumberFormatAndPrefix(t *testing.T) {
	if got := store.InvoiceNumberFor("INV", 2026, 1); got != "INV-2026-00001" {
		t.Errorf("first number of the year = %s", got)
	}
	if got := store.InvoiceNumberFor("OMT-CB", 2026, 4213); got != "OMT-CB-2026-04213" {
		t.Errorf("with a configured prefix = %s", got)
	}
	// Zero-padded to five so a year's invoices sort as text.
	if a, b := store.InvoiceNumberFor("INV", 2026, 9), store.InvoiceNumberFor("INV", 2026, 10); !(a < b) {
		t.Errorf("%s must sort before %s", a, b)
	}
	for _, ok := range []string{"INV", "OMT-CB", "A", "X9", "123456789012"} {
		if !store.ValidInvoicePrefix(ok) {
			t.Errorf("%q must be a valid prefix", ok)
		}
	}
	for _, bad := range []string{"", "-INV", "in v", "inv", "TOO-LONG-PREFIX", "IN_V"} {
		if store.ValidInvoicePrefix(bad) {
			t.Errorf("%q must not be a valid prefix", bad)
		}
	}
	if got := store.NormalizeInvoicePrefix("  omt-cb "); got != "OMT-CB" {
		t.Errorf("normalised = %q", got)
	}
}
