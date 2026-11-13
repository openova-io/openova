package sandbox

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	ledgerv1 "github.com/openova-io/openova/products/fingate/api/ledger/v1"
	"github.com/openova-io/openova/products/fingate/internal/ledgertest"
)

// kitFixture converts KitFixture to the kit's type through its JSON form, as the conformance
// program reads it; an unknown key is an error.
func kitFixture(t *testing.T) ledgertest.Fixture {
	t.Helper()
	b, err := json.Marshal(KitFixture())
	if err != nil {
		t.Fatal(err)
	}
	var fx ledgertest.Fixture
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if err := d.Decode(&fx); err != nil {
		t.Fatalf("the kit cannot read the fixture %s: %v", b, err)
	}
	return fx
}

func TestKitPassesAgainstTheSandbox(t *testing.T) {
	conn, stop, err := ledgertest.Serve(New())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	defer func() { _ = conn.Close() }()
	rep := ledgertest.Run(context.Background(), conn, kitFixture(t))
	var out strings.Builder
	if err := rep.Print(&out); err != nil {
		t.Fatal(err)
	}
	if !rep.Passed() {
		t.Fatalf("the kit failed against the sandbox:\n%s", out.String())
	}
	if !strings.HasSuffix(out.String(), "checks passed: 41/41\noperations called: 14/14\n") {
		t.Fatalf("the summary lines are not those of a ledger that offers everything:\n%s", out.String())
	}
}

func TestDataSetHoldsThreeCustomersAndFiveAccounts(t *testing.T) {
	ctx := context.Background()
	l := New()
	customers, accounts := 0, 0
	for _, subject := range []string{"alice", "bob", "carol"} {
		c, err := l.ResolveCustomer(ctx, &ledgerv1.ResolveCustomerRequest{Scheme: Scheme, Subject: subject})
		if err != nil {
			t.Fatalf("subject %s: %v", subject, err)
		}
		customers++
		accs, err := l.ListAccounts(ctx, &ledgerv1.ListAccountsRequest{CustomerId: c.GetCustomerId()})
		if err != nil || len(accs.GetAccounts()) == 0 {
			t.Fatalf("customer %s: no account (%v)", c.GetCustomerId(), err)
		}
		for _, a := range accs.GetAccounts() {
			accounts++
			checkAccount(t, l, c.GetCustomerId(), a)
		}
	}
	if customers < 3 || accounts < 5 {
		t.Fatalf("%d customers and %d accounts; want at least 3 and 5", customers, accounts)
	}
	caps, _ := l.GetCapabilities(ctx, &ledgerv1.GetCapabilitiesRequest{})
	if !slices.Equal(caps.GetCurrencies(), []string{"EUR", "GBP"}) {
		t.Fatalf("currencies %v, want [EUR GBP]", caps.GetCurrencies())
	}
}

// checkAccount checks the codes of one account and that its transactions lead to its balance.
func checkAccount(t *testing.T, l *Ledger, customer string, a *ledgerv1.Account) {
	t.Helper()
	ctx := context.Background()
	id := a.GetAccountId()
	if a.GetStatus() != "Enabled" || !slices.Contains([]string{"Personal", "Business"}, a.GetType()) || !slices.Contains([]string{"CACC", "SVGS"}, a.GetSubType()) {
		t.Fatalf("account %s: status %q, type %q, sub type %q are not codes of the profile", id, a.GetStatus(), a.GetType(), a.GetSubType())
	}
	bal, err := l.ListBalances(ctx, &ledgerv1.AccountRequest{CustomerId: customer, AccountId: id})
	if err != nil || len(bal.GetBalances()) != 2 {
		t.Fatalf("account %s: balances %v (%v)", id, bal.GetBalances(), err)
	}
	for i, typ := range []string{"CLBD", "ITAV"} {
		if bal.GetBalances()[i].GetType() != typ {
			t.Fatalf("account %s: balance %d is %q, want %s", id, i, bal.GetBalances()[i].GetType(), typ)
		}
	}
	txns, err := l.ListTransactions(ctx, &ledgerv1.ListTransactionsRequest{CustomerId: customer, AccountId: id})
	if err != nil || len(txns.GetTransactions()) == 0 {
		t.Fatalf("account %s: no transaction (%v)", id, err)
	}
	for _, x := range txns.GetTransactions() {
		if x.GetStatus() != "BOOK" || x.GetAmount().GetCurrency() != a.GetCurrency() {
			t.Fatalf("transaction %s: status %q, currency %q", x.GetTransactionId(), x.GetStatus(), x.GetAmount().GetCurrency())
		}
	}
	last := txns.GetTransactions()[len(txns.GetTransactions())-1]
	if got, want := last.GetBalanceAfter().GetAmount(), bal.GetBalances()[0].GetAmount().GetAmount(); got != want {
		t.Fatalf("account %s: the last balance_after is %s, the balance %s", id, got, want)
	}
}

func TestTransactionsBetweenFromAndTo(t *testing.T) {
	ctx := context.Background()
	l := New()
	list := func(from, to string) ([]string, error) {
		r, err := l.ListTransactions(ctx, &ledgerv1.ListTransactionsRequest{CustomerId: "cust-0001", AccountId: "acc-0001-01", From: from, To: to})
		var ids []string
		for _, x := range r.GetTransactions() {
			ids = append(ids, x.GetTransactionId())
		}
		return ids, err
	}
	cases := []struct {
		from, to string
		want     []string
	}{
		{"", "", []string{"tx-0001-01-1", "tx-0001-01-2", "tx-0001-01-3", "tx-0001-01-4", "tx-0001-01-5"}},
		{"2026-01-05T08:00:00Z", "2026-01-12T08:00:00Z", []string{"tx-0001-01-2", "tx-0001-01-3", "tx-0001-01-4"}},
		{"2026-01-10T00:00:00Z", "", []string{"tx-0001-01-4", "tx-0001-01-5"}},
		{"", "2026-01-03T00:00:00Z", []string{"tx-0001-01-1"}},
		{"2027-01-01T00:00:00Z", "", nil},
	}
	for _, c := range cases {
		got, err := list(c.from, c.to)
		if err != nil || !slices.Equal(got, c.want) {
			t.Fatalf("from %q to %q: got %v (%v), want %v", c.from, c.to, got, err, c.want)
		}
	}
	if _, err := list("yesterday", ""); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("a from that is not a time: got %v, want InvalidArgument", err)
	}
}

func TestPaymentsAppearInTheReads(t *testing.T) {
	ctx := context.Background()
	l := New()
	pay := func(key, typ, amount, at string, rec *ledgerv1.Recurrence) *ledgerv1.Payment {
		t.Helper()
		p, err := l.SubmitPayment(ctx, &ledgerv1.SubmitPaymentRequest{
			IdempotencyKey: key, CustomerId: "cust-0001", DebtorAccountId: "acc-0001-01", Type: typ,
			Amount: money(amount, "GBP"), CreditorName: "Bob Sample", CreditorSchemeName: sortCode,
			CreditorIdentification: "00000120000001", Reference: key, RequestedExecutionAt: at, Recurrence: rec,
		})
		if err != nil {
			t.Fatalf("payment %s: %v", key, err)
		}
		return p
	}
	if p := pay("now", "domestic", "43.20", "", nil); p.GetStatus() != "settled" {
		t.Fatalf("an immediate payment is %q, want settled", p.GetStatus())
	}
	bal, _ := l.ListBalances(ctx, &ledgerv1.AccountRequest{CustomerId: "cust-0001", AccountId: "acc-0001-01"})
	if got := bal.GetBalances()[0].GetAmount().GetAmount(); got != "2400.00" {
		t.Fatalf("balance %s after paying 43.20 from 2443.20, want 2400.00", got)
	}
	txns, _ := l.ListTransactions(ctx, &ledgerv1.ListTransactionsRequest{CustomerId: "cust-0001", AccountId: "acc-0001-01"})
	last := txns.GetTransactions()[len(txns.GetTransactions())-1]
	if last.GetCreditDebit() != "Debit" || last.GetAmount().GetAmount() != "43.20" || last.GetBalanceAfter().GetAmount() != "2400.00" {
		t.Fatalf("the payment is not the last transaction: %v", last)
	}
	if p := pay("later", "domestic-scheduled", "10.00", "2027-01-01T00:00:00Z", nil); p.GetStatus() != "pending" {
		t.Fatalf("a scheduled payment is %q, want pending", p.GetStatus())
	}
	sps, _ := l.ListScheduledPayments(ctx, &ledgerv1.AccountPageRequest{CustomerId: "cust-0001", AccountId: "acc-0001-01"})
	if n := len(sps.GetItems()); n != 2 || sps.GetItems()[1].GetExecutionAt() != "2027-01-01T00:00:00Z" {
		t.Fatalf("the scheduled payment is not listed: %v", sps.GetItems())
	}
	rec := &ledgerv1.Recurrence{Frequency: "WEEK", FirstPaymentAt: "2027-01-04T00:00:00Z", FirstAmount: money("5.00", "GBP")}
	if p := pay("weekly", "domestic-standing-order", "5.00", "", rec); p.GetStatus() != "pending" {
		t.Fatalf("a standing order is %q, want pending", p.GetStatus())
	}
	sos, _ := l.ListStandingOrders(ctx, &ledgerv1.AccountPageRequest{CustomerId: "cust-0001", AccountId: "acc-0001-01"})
	if n := len(sos.GetItems()); n != 2 || sos.GetItems()[1].GetFrequency() != "WEEK" || sos.GetItems()[1].GetStatus() != "ACTV" {
		t.Fatalf("the standing order is not listed: %v", sos.GetItems())
	}
	if _, err := New().GetPayment(ctx, &ledgerv1.GetPaymentRequest{PaymentId: "pay-000001"}); status.Code(err) != codes.NotFound {
		t.Fatalf("a new sandbox knows a payment of another: %v", err)
	}
}

func TestPaymentRequestsThatAreRefused(t *testing.T) {
	ctx := context.Background()
	base := func() *ledgerv1.SubmitPaymentRequest {
		return &ledgerv1.SubmitPaymentRequest{
			IdempotencyKey: "k", CustomerId: "cust-0001", DebtorAccountId: "acc-0001-01", Type: "domestic",
			Amount: money("1.00", "GBP"), CreditorName: "Bob Sample", CreditorSchemeName: sortCode, CreditorIdentification: "00000120000001",
		}
	}
	cases := map[string]struct {
		edit func(*ledgerv1.SubmitPaymentRequest)
		want codes.Code
	}{
		"no idempotency key":            {func(r *ledgerv1.SubmitPaymentRequest) { r.IdempotencyKey = "" }, codes.InvalidArgument},
		"an unknown payment type":       {func(r *ledgerv1.SubmitPaymentRequest) { r.Type = "international" }, codes.Unimplemented},
		"a scheduled payment, no time":  {func(r *ledgerv1.SubmitPaymentRequest) { r.Type = "domestic-scheduled" }, codes.InvalidArgument},
		"an execution time of no form":  {func(r *ledgerv1.SubmitPaymentRequest) { r.RequestedExecutionAt = "tomorrow" }, codes.InvalidArgument},
		"a standing order, no schedule": {func(r *ledgerv1.SubmitPaymentRequest) { r.Type = "domestic-standing-order" }, codes.InvalidArgument},
		"a recurrence on a payment":     {func(r *ledgerv1.SubmitPaymentRequest) { r.Recurrence = &ledgerv1.Recurrence{} }, codes.InvalidArgument},
		"no first amount": {func(r *ledgerv1.SubmitPaymentRequest) {
			r.Type, r.Recurrence = "domestic-standing-order", &ledgerv1.Recurrence{Frequency: "MNTH"}
		}, codes.InvalidArgument},
		"another currency":         {func(r *ledgerv1.SubmitPaymentRequest) { r.Amount = money("1.00", "EUR") }, codes.InvalidArgument},
		"an amount of no form":     {func(r *ledgerv1.SubmitPaymentRequest) { r.Amount = money("1,00", "GBP") }, codes.InvalidArgument},
		"more than the balance":    {func(r *ledgerv1.SubmitPaymentRequest) { r.Amount = money("2443.21", "GBP") }, codes.FailedPrecondition},
		"another customer's":       {func(r *ledgerv1.SubmitPaymentRequest) { r.DebtorAccountId = "acc-0002-01" }, codes.PermissionDenied},
		"an unknown account":       {func(r *ledgerv1.SubmitPaymentRequest) { r.DebtorAccountId = "acc-9999-99" }, codes.NotFound},
		"an unknown customer":      {func(r *ledgerv1.SubmitPaymentRequest) { r.CustomerId = "cust-9999" }, codes.NotFound},
		"the whole balance (pass)": {func(r *ledgerv1.SubmitPaymentRequest) { r.Amount = money("2443.20", "GBP") }, codes.OK},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := base()
			c.edit(r)
			if _, err := New().SubmitPayment(ctx, r); status.Code(err) != c.want {
				t.Fatalf("got %v, want %s", err, c.want)
			}
		})
	}
}
