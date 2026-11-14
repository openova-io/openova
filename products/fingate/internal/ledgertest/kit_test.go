package ledgertest

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	ledgerv1 "github.com/openova-io/openova/products/fingate/api/ledger/v1"
)

// runKit serves a fresh in-memory ledger, optionally behind an interceptor, and runs the kit.
func runKit(t *testing.T, caps *ledgerv1.Capabilities, fx Fixture, ic grpc.UnaryServerInterceptor) *Report {
	t.Helper()
	var opts []grpc.ServerOption
	if ic != nil {
		opts = append(opts, grpc.UnaryInterceptor(ic))
	}
	conn, stop, err := Serve(NewMemory(caps), opts...)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	defer func() { _ = conn.Close() }()
	return Run(context.Background(), conn, fx)
}

func names(results []Result) []string {
	var out []string
	for _, r := range results {
		out = append(out, r.Name)
	}
	return out
}

// The checks of a run against a ledger that offers everything.
var fullChecks = []string{
	"capabilities", "resolve-customer", "resolve-customer-unknown", "resolve-other-customer",
	"list-accounts", "list-accounts-unknown-customer", "get-account", "get-account-unknown", "get-account-not-owned",
	"list-balances", "list-balances-not-owned", "list-transactions", "list-transactions-not-owned",
	"list-beneficiaries", "list-beneficiaries-not-owned", "list-standing-orders", "list-standing-orders-not-owned",
	"list-direct-debits", "list-direct-debits-not-owned", "list-scheduled-payments", "list-scheduled-payments-not-owned",
	"get-party", "get-party-not-owned", "check-funds", "check-funds-not-owned", "check-funds-unknown-account",
	"submit-payment-domestic", "idempotent-resubmission-domestic", "get-payment-domestic", "idempotency-key-conflict",
	"submit-payment-domestic-scheduled", "idempotent-resubmission-domestic-scheduled", "get-payment-domestic-scheduled",
	"submit-payment-domestic-standing-order", "idempotent-resubmission-domestic-standing-order", "get-payment-domestic-standing-order",
	"submit-payment-refused", "submit-payment-not-owned", "submit-payment-unknown-account", "get-payment-unknown",
	"every-operation-called",
}

func TestMemoryPassesTheKit(t *testing.T) {
	rep := runKit(t, FullCapabilities(), MemoryFixture(), nil)
	var out bytes.Buffer
	if err := rep.Print(&out); err != nil {
		t.Fatal(err)
	}
	t.Log("\n" + out.String())
	if !rep.Passed() {
		t.Fatalf("failed checks: %v", rep.Failed())
	}
	if got := names(rep.Results); !slices.Equal(got, fullChecks) {
		t.Fatalf("checks run:\n%v\nwant:\n%v", got, fullChecks)
	}
	if len(rep.Called) != 14 || rep.Operations != 14 {
		t.Fatalf("operations called %d of %d, want 14 of 14: %v", len(rep.Called), rep.Operations, rep.Called)
	}
	if !strings.Contains(out.String(), "checks passed: 41/41\noperations called: 14/14\n") {
		t.Fatalf("summary lines missing:\n%s", out.String())
	}
}

func TestReducedLedgerAnswersUnimplemented(t *testing.T) {
	caps := &ledgerv1.Capabilities{PaymentTypes: []string{"domestic"}, Currencies: []string{"GBP"}}
	rep := runKit(t, caps, MemoryFixture(), nil)
	if !rep.Passed() {
		t.Fatalf("failed checks: %v", rep.Failed())
	}
	got := names(rep.Results)
	for _, want := range []string{
		"list-beneficiaries-unimplemented", "list-standing-orders-unimplemented", "list-direct-debits-unimplemented",
		"list-scheduled-payments-unimplemented", "get-party-unimplemented",
		"submit-payment-domestic-scheduled-unimplemented", "submit-payment-domestic-standing-order-unimplemented",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("check %s did not run; ran %v", want, got)
		}
	}
	if len(rep.Called) != 14 {
		t.Fatalf("operations called: %v", rep.Called)
	}
}

func TestBadFixtureFails(t *testing.T) {
	fx := MemoryFixture()
	fx.Amount = "1,00"
	rep := runKit(t, FullCapabilities(), fx, nil)
	if got := names(rep.Failed()); !slices.Equal(got, []string{"fixture", "every-operation-called"}) {
		t.Fatalf("failed checks %v, want [fixture every-operation-called]", got)
	}
}

// on returns an interceptor that changes the answer of one method of the port.
func on(method string, change func(resp any, err error) (any, error)) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		resp, err := h(ctx, req)
		if info.FullMethod == method {
			return change(resp, err)
		}
		return resp, err
	}
}

// swapCode replaces one error code by another.
func swapCode(from, to codes.Code) func(any, error) (any, error) {
	return func(resp any, err error) (any, error) {
		if status.Code(err) == from {
			return nil, status.Error(to, "changed by the test")
		}
		return resp, err
	}
}

// TestKitFindsBrokenLedgers proves that the kit can fail: each case breaks one rule of the
// port in the in-memory ledger and names the check that must catch it.
func TestKitFindsBrokenLedgers(t *testing.T) {
	cases := []struct {
		name  string
		check string
		caps  *ledgerv1.Capabilities
		ic    grpc.UnaryServerInterceptor
	}{
		{"an unknown resource in the capabilities", "capabilities", nil,
			on(ledgerv1.Ledger_GetCapabilities_FullMethodName, func(resp any, err error) (any, error) {
				c := resp.(*ledgerv1.Capabilities)
				c.Resources = append(c.Resources, "loans")
				return c, err
			})},
		{"an unknown subject resolves", "resolve-customer-unknown", nil,
			on(ledgerv1.Ledger_ResolveCustomer_FullMethodName, func(resp any, err error) (any, error) {
				if status.Code(err) == codes.NotFound {
					return &ledgerv1.Customer{CustomerId: "cust-ghost"}, nil
				}
				return resp, err
			})},
		{"another customer's account is NOT_FOUND", "get-account-not-owned", nil,
			on(ledgerv1.Ledger_GetAccount_FullMethodName, swapCode(codes.PermissionDenied, codes.NotFound))},
		{"a balance amount with a comma", "list-balances", nil,
			on(ledgerv1.Ledger_ListBalances_FullMethodName, func(resp any, err error) (any, error) {
				if r, ok := resp.(*ledgerv1.ListBalancesResponse); ok && r != nil {
					r.Balances[0].Amount.Amount = "1,000.00"
				}
				return resp, err
			})},
		{"transactions without a next page", "list-transactions", nil,
			on(ledgerv1.Ledger_ListTransactions_FullMethodName, func(resp any, err error) (any, error) {
				if r, ok := resp.(*ledgerv1.ListTransactionsResponse); ok && r != nil {
					r.NextPageToken = ""
				}
				return resp, err
			})},
		{"beneficiaries listed without the capability", "list-beneficiaries-unimplemented",
			&ledgerv1.Capabilities{PaymentTypes: []string{"domestic"}, Currencies: []string{"GBP"}},
			on(ledgerv1.Ledger_ListBeneficiaries_FullMethodName, func(any, error) (any, error) {
				return &ledgerv1.ListBeneficiariesResponse{}, nil
			})},
		{"party offered and not served", "get-party", nil,
			on(ledgerv1.Ledger_GetParty_FullMethodName, func(any, error) (any, error) {
				return nil, status.Error(codes.Unimplemented, "changed by the test")
			})},
		{"funds always available", "check-funds", nil,
			on(ledgerv1.Ledger_CheckFunds_FullMethodName, func(resp any, err error) (any, error) {
				if r, ok := resp.(*ledgerv1.CheckFundsResponse); ok && r != nil {
					r.Available = true
				}
				return resp, err
			})},
		{"a new payment for a re-submission", "idempotent-resubmission-domestic", nil,
			on(ledgerv1.Ledger_SubmitPayment_FullMethodName, func(resp any, err error) (any, error) {
				if p, ok := resp.(*ledgerv1.Payment); ok && p != nil {
					p.PaymentId += "-" + runID()
				}
				return resp, err
			})},
		{"a reused key with other content accepted", "idempotency-key-conflict", nil,
			on(ledgerv1.Ledger_SubmitPayment_FullMethodName, func(resp any, err error) (any, error) {
				if status.Code(err) == codes.AlreadyExists {
					return &ledgerv1.Payment{PaymentId: "pay-1", Status: "accepted", StatusAt: "2026-01-01T00:00:00Z"}, nil
				}
				return resp, err
			})},
		{"a refusal without a message", "submit-payment-refused", nil,
			on(ledgerv1.Ledger_SubmitPayment_FullMethodName, func(resp any, err error) (any, error) {
				if status.Code(err) == codes.FailedPrecondition {
					return nil, status.Error(codes.FailedPrecondition, "")
				}
				return resp, err
			})},
		{"a payment status outside the port", "get-payment-domestic", nil,
			on(ledgerv1.Ledger_GetPayment_FullMethodName, func(resp any, err error) (any, error) {
				if p, ok := resp.(*ledgerv1.Payment); ok && p != nil {
					p.Status = "done"
				}
				return resp, err
			})},
		{"an unknown payment is INTERNAL", "get-payment-unknown", nil,
			on(ledgerv1.Ledger_GetPayment_FullMethodName, swapCode(codes.NotFound, codes.Internal))},
		{"an operation never answered", "list-accounts", nil,
			on(ledgerv1.Ledger_ListAccounts_FullMethodName, func(any, error) (any, error) {
				return nil, status.Error(codes.Unavailable, "changed by the test")
			})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			caps := c.caps
			if caps == nil {
				caps = FullCapabilities()
			}
			rep := runKit(t, caps, MemoryFixture(), c.ic)
			if rep.Passed() {
				t.Fatal("the kit passed a broken ledger")
			}
			if failed := names(rep.Failed()); !slices.Contains(failed, c.check) {
				t.Fatalf("check %s did not fail; failed: %v", c.check, failed)
			}
		})
	}
}
