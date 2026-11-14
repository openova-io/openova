package main

import (
	"context"
	"net"
	"slices"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	ledgerv1 "github.com/openova-io/openova/products/fingate/api/ledger/v1"
)

// serve serves the adapter in process and returns a client of it.
func serve(t *testing.T) ledgerv1.LedgerClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	s := grpc.NewServer()
	ledgerv1.RegisterLedgerServer(s, newLedger())
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)
	conn, err := grpc.NewClient("passthrough:///adapter",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return ledgerv1.NewLedgerClient(conn)
}

// TestOperationsAnswerUnimplemented calls every operation of the port. GetCapabilities answers
// empty lists and every other operation answers UNIMPLEMENTED. It describes the template as it
// is: replace it with tests of your own as you fill in the operations.
func TestOperationsAnswerUnimplemented(t *testing.T) {
	c := serve(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	caps, err := c.GetCapabilities(ctx, &ledgerv1.GetCapabilitiesRequest{})
	if err != nil {
		t.Fatalf("GetCapabilities: %v", err)
	}
	if n := len(caps.GetResources()) + len(caps.GetPaymentTypes()) + len(caps.GetCurrencies()); n != 0 {
		t.Fatalf("GetCapabilities names %d values; the template names none", n)
	}

	calls := []struct {
		name string
		call func() error
	}{
		{"ResolveCustomer", func() error { _, err := c.ResolveCustomer(ctx, &ledgerv1.ResolveCustomerRequest{}); return err }},
		{"ListAccounts", func() error { _, err := c.ListAccounts(ctx, &ledgerv1.ListAccountsRequest{}); return err }},
		{"GetAccount", func() error { _, err := c.GetAccount(ctx, &ledgerv1.GetAccountRequest{}); return err }},
		{"ListBalances", func() error { _, err := c.ListBalances(ctx, &ledgerv1.AccountRequest{}); return err }},
		{"ListTransactions", func() error { _, err := c.ListTransactions(ctx, &ledgerv1.ListTransactionsRequest{}); return err }},
		{"ListBeneficiaries", func() error { _, err := c.ListBeneficiaries(ctx, &ledgerv1.AccountPageRequest{}); return err }},
		{"ListStandingOrders", func() error { _, err := c.ListStandingOrders(ctx, &ledgerv1.AccountPageRequest{}); return err }},
		{"ListDirectDebits", func() error { _, err := c.ListDirectDebits(ctx, &ledgerv1.AccountPageRequest{}); return err }},
		{"ListScheduledPayments", func() error { _, err := c.ListScheduledPayments(ctx, &ledgerv1.AccountPageRequest{}); return err }},
		{"GetParty", func() error { _, err := c.GetParty(ctx, &ledgerv1.GetPartyRequest{}); return err }},
		{"CheckFunds", func() error { _, err := c.CheckFunds(ctx, &ledgerv1.CheckFundsRequest{}); return err }},
		{"SubmitPayment", func() error { _, err := c.SubmitPayment(ctx, &ledgerv1.SubmitPaymentRequest{}); return err }},
		{"GetPayment", func() error { _, err := c.GetPayment(ctx, &ledgerv1.GetPaymentRequest{}); return err }},
	}
	names := []string{"GetCapabilities"}
	for _, x := range calls {
		names = append(names, x.name)
		if got := status.Code(x.call()); got != codes.Unimplemented {
			t.Errorf("%s answered %s; want Unimplemented", x.name, got)
		}
	}
	var port []string
	for _, m := range ledgerv1.Ledger_ServiceDesc.Methods {
		port = append(port, m.MethodName)
	}
	if !slices.Equal(names, port) {
		t.Fatalf("the test calls %v; the port has %v", names, port)
	}
}
