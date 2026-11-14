package main

// This file is the platform repository's own check of the template. It imports the conformance
// kit, an internal package of the platform, so it builds only there. Delete it in your copy and
// run the program fingate-ledger-conformance against your adapter instead.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/fingate/internal/ledgertest"
)

// unimplementedOperations are the operations of the port that the template does not serve, in
// the order of the port.
var unimplementedOperations = []string{
	"ResolveCustomer", "ListAccounts", "GetAccount", "ListBalances", "ListTransactions",
	"ListBeneficiaries", "ListStandingOrders", "ListDirectDebits", "ListScheduledPayments",
	"GetParty", "CheckFunds", "SubmitPayment", "GetPayment",
}

// TestKitFailsWithTheUnimplementedOperations runs the conformance kit against the template. The
// kit fails twice, and only twice: the capabilities name no currency, and the 13 operations the
// template does not serve are never called.
func TestKitFailsWithTheUnimplementedOperations(t *testing.T) {
	conn, stop, err := ledgertest.Serve(newLedger())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	rep := ledgertest.Run(ctx, conn, ledgertest.MemoryFixture())

	var failed []string
	for _, r := range rep.Failed() {
		failed = append(failed, r.Name+": "+r.Err.Error())
	}
	want := []string{
		"capabilities: currencies is empty",
		"every-operation-called: not called: " + strings.Join(unimplementedOperations, ", "),
	}
	if strings.Join(failed, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the kit failed with\n%s\nwant\n%s", strings.Join(failed, "\n"), strings.Join(want, "\n"))
	}
	if len(rep.Results) != len(want) || rep.Passed() {
		t.Fatalf("the kit ran %d checks; want %d, both failed", len(rep.Results), len(want))
	}
	if got := strings.Join(rep.Called, ","); got != "GetCapabilities" {
		t.Fatalf("the kit called %s; want GetCapabilities only", got)
	}
	if rep.Operations != len(unimplementedOperations)+1 {
		t.Fatalf("the port has %d operations; want %d", rep.Operations, len(unimplementedOperations)+1)
	}
}
