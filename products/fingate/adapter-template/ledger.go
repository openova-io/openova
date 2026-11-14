package main

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	ledgerv1 "github.com/openova-io/openova/products/fingate/api/ledger/v1"
)

// ledger serves the ledger port. Each method below is one operation of the port. Replace the
// body of a method with a call to the core-banking system; the comment above it says what that
// system must supply. The port's error codes are fixed: NOT_FOUND for an unknown customer,
// account or payment, PERMISSION_DENIED for an account that is not the customer's,
// FAILED_PRECONDITION with a message when the bank refuses a payment, UNIMPLEMENTED for a
// resource or payment type the capabilities do not name, UNAVAILABLE when a retry is safe, and
// ALREADY_EXISTS for an idempotency key reused with other content.
type ledger struct {
	ledgerv1.UnimplementedLedgerServer
}

func newLedger() *ledger { return &ledger{} }

// unimplemented is the answer of every operation this adapter does not serve yet.
func unimplemented(operation string) error {
	return status.Errorf(codes.Unimplemented, "%s is not implemented by this adapter yet", operation)
}

// GetCapabilities names the optional resources and payment types this bank serves, and the
// currencies of its accounts. It is the one operation that already answers: with empty lists.
func (l *ledger) GetCapabilities(context.Context, *ledgerv1.GetCapabilitiesRequest) (*ledgerv1.Capabilities, error) {
	return &ledgerv1.Capabilities{Resources: []string{}, PaymentTypes: []string{}, Currencies: []string{}}, nil
}

// ResolveCustomer maps the identity a customer signed in with (scheme and subject) to the
// bank's own customer identifier.
func (l *ledger) ResolveCustomer(context.Context, *ledgerv1.ResolveCustomerRequest) (*ledgerv1.Customer, error) {
	return nil, unimplemented("ResolveCustomer")
}

// ListAccounts lists every account the customer holds.
func (l *ledger) ListAccounts(context.Context, *ledgerv1.ListAccountsRequest) (*ledgerv1.ListAccountsResponse, error) {
	return nil, unimplemented("ListAccounts")
}

// GetAccount reads one account of the customer, exactly as ListAccounts lists it.
func (l *ledger) GetAccount(context.Context, *ledgerv1.GetAccountRequest) (*ledgerv1.Account, error) {
	return nil, unimplemented("GetAccount")
}

// ListBalances lists the balances of one account of the customer.
func (l *ledger) ListBalances(context.Context, *ledgerv1.AccountRequest) (*ledgerv1.ListBalancesResponse, error) {
	return nil, unimplemented("ListBalances")
}

// ListTransactions lists the transactions of one account, booked between from and to, a page
// at a time.
func (l *ledger) ListTransactions(context.Context, *ledgerv1.ListTransactionsRequest) (*ledgerv1.ListTransactionsResponse, error) {
	return nil, unimplemented("ListTransactions")
}

// ListBeneficiaries lists the payees saved on one account, a page at a time. Optional:
// resource beneficiaries.
func (l *ledger) ListBeneficiaries(context.Context, *ledgerv1.AccountPageRequest) (*ledgerv1.ListBeneficiariesResponse, error) {
	return nil, unimplemented("ListBeneficiaries")
}

// ListStandingOrders lists the standing orders of one account, a page at a time. Optional:
// resource standing-orders.
func (l *ledger) ListStandingOrders(context.Context, *ledgerv1.AccountPageRequest) (*ledgerv1.ListStandingOrdersResponse, error) {
	return nil, unimplemented("ListStandingOrders")
}

// ListDirectDebits lists the direct-debit mandates of one account, a page at a time.
// Optional: resource direct-debits.
func (l *ledger) ListDirectDebits(context.Context, *ledgerv1.AccountPageRequest) (*ledgerv1.ListDirectDebitsResponse, error) {
	return nil, unimplemented("ListDirectDebits")
}

// ListScheduledPayments lists the future-dated payments of one account, a page at a time.
// Optional: resource scheduled-payments.
func (l *ledger) ListScheduledPayments(context.Context, *ledgerv1.AccountPageRequest) (*ledgerv1.ListScheduledPaymentsResponse, error) {
	return nil, unimplemented("ListScheduledPayments")
}

// GetParty reads the customer (account_id empty) or the holder of one account. Optional:
// resource party.
func (l *ledger) GetParty(context.Context, *ledgerv1.GetPartyRequest) (*ledgerv1.Party, error) {
	return nil, unimplemented("GetParty")
}

// CheckFunds answers whether one account of the customer can pay an amount now.
func (l *ledger) CheckFunds(context.Context, *ledgerv1.CheckFundsRequest) (*ledgerv1.CheckFundsResponse, error) {
	return nil, unimplemented("CheckFunds")
}

// SubmitPayment hands one payment of a type the capabilities name to the bank, once per
// idempotency key.
func (l *ledger) SubmitPayment(context.Context, *ledgerv1.SubmitPaymentRequest) (*ledgerv1.Payment, error) {
	return nil, unimplemented("SubmitPayment")
}

// GetPayment reads the status of a payment that SubmitPayment returned.
func (l *ledger) GetPayment(context.Context, *ledgerv1.GetPaymentRequest) (*ledgerv1.Payment, error) {
	return nil, unimplemented("GetPayment")
}
