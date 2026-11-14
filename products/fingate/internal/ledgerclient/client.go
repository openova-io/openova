// Package ledgerclient is the resource server's client of the ledger port
// (products/fingate/api/ledger/v1).
//
// Core banking is reached only through the port. Dial opens the connection to the adapter over
// TLS; New wraps a connection. Every call carries a deadline, and every error of the port is
// mapped to one of the errors below, so that a caller decides on the kind of failure and never
// on a gRPC code:
//
//	NOT_FOUND            ErrNotFound      unknown customer, account or payment
//	PERMISSION_DENIED    ErrNotOwned      the account is not the customer's
//	FAILED_PRECONDITION  ErrRefused       the ledger refuses the payment
//	UNIMPLEMENTED        ErrNotOffered    a resource or payment type absent from the capabilities
//	ALREADY_EXISTS       ErrConflict      an idempotency key reused with different content
//	INVALID_ARGUMENT     ErrBadRequest    a page token the ledger did not issue, a bad value
//	anything else        ErrUnavailable   the ledger could not answer; retry is safe
//
// The error's text is the operation and the ledger's message, for the log; it is never written
// to a third party.
package ledgerclient

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	ledgerv1 "github.com/openova-io/openova/products/fingate/api/ledger/v1"
)

// DefaultTimeout bounds a call when New is given no timeout.
const DefaultTimeout = 5 * time.Second

// The kinds of failure. Every error a Client returns wraps exactly one of them.
var (
	ErrNotFound    = errors.New("not found")
	ErrNotOwned    = errors.New("not the customer's")
	ErrRefused     = errors.New("refused")
	ErrNotOffered  = errors.New("not offered")
	ErrConflict    = errors.New("conflict")
	ErrBadRequest  = errors.New("bad request")
	ErrUnavailable = errors.New("unavailable")
)

// Client calls the ledger port. It is safe for concurrent use.
type Client struct {
	c       ledgerv1.LedgerClient
	timeout time.Duration
}

// New returns a Client over cc. A timeout of zero or less is DefaultTimeout.
func New(cc grpc.ClientConnInterface, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Client{c: ledgerv1.NewLedgerClient(cc), timeout: timeout}
}

// Dial opens a connection to the adapter at target (the form dns:///<host>:<port> of the
// definitions' ledger.target) over TLS. tc holds the roots that verify the adapter's
// certificate; serverName, when not empty, is the name that certificate must carry. tc is not
// changed. The connection is made on the first call.
func Dial(target, serverName string, tc *tls.Config) (*grpc.ClientConn, error) {
	if tc == nil {
		return nil, errors.New("ledgerclient: a TLS configuration is required")
	}
	c := tc.Clone()
	if c.MinVersion < tls.VersionTLS12 {
		c.MinVersion = tls.VersionTLS12
	}
	if serverName != "" {
		c.ServerName = serverName
	}
	cc, err := grpc.NewClient(target, grpc.WithTransportCredentials(credentials.NewTLS(c)))
	if err != nil {
		return nil, fmt.Errorf("ledgerclient: %w", err)
	}
	return cc, nil
}

// kinds maps the port's codes to the errors above.
var kinds = map[codes.Code]error{
	codes.NotFound:           ErrNotFound,
	codes.PermissionDenied:   ErrNotOwned,
	codes.FailedPrecondition: ErrRefused,
	codes.Unimplemented:      ErrNotOffered,
	codes.AlreadyExists:      ErrConflict,
	codes.InvalidArgument:    ErrBadRequest,
}

// mapErr returns nil for nil, otherwise an error that wraps the kind of failure.
func mapErr(op string, err error) error {
	if err == nil {
		return nil
	}
	st, _ := status.FromError(err)
	kind, ok := kinds[st.Code()]
	if !ok {
		kind = ErrUnavailable
	}
	return fmt.Errorf("ledger %s: %w: %s", op, kind, st.Message())
}

// call runs f with the deadline of one call and maps its error.
func call[T any](ctx context.Context, c *Client, op string, f func(context.Context) (T, error)) (T, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	out, err := f(ctx)
	if err != nil {
		var zero T
		return zero, mapErr(op, err)
	}
	return out, nil
}

// Capabilities returns what the ledger offers.
func (c *Client) Capabilities(ctx context.Context) (*ledgerv1.Capabilities, error) {
	return call(ctx, c, "GetCapabilities", func(ctx context.Context) (*ledgerv1.Capabilities, error) {
		return c.c.GetCapabilities(ctx, &ledgerv1.GetCapabilitiesRequest{})
	})
}

// ResolveCustomer maps an identity of the authorisation server to a customer.
func (c *Client) ResolveCustomer(ctx context.Context, scheme, subject string, attributes map[string]string) (*ledgerv1.Customer, error) {
	return call(ctx, c, "ResolveCustomer", func(ctx context.Context) (*ledgerv1.Customer, error) {
		return c.c.ResolveCustomer(ctx, &ledgerv1.ResolveCustomerRequest{Scheme: scheme, Subject: subject, Attributes: attributes})
	})
}

// Accounts lists the customer's accounts.
func (c *Client) Accounts(ctx context.Context, customer string) ([]*ledgerv1.Account, error) {
	return call(ctx, c, "ListAccounts", func(ctx context.Context) ([]*ledgerv1.Account, error) {
		r, err := c.c.ListAccounts(ctx, &ledgerv1.ListAccountsRequest{CustomerId: customer})
		return r.GetAccounts(), err
	})
}

// Account returns one of the customer's accounts.
func (c *Client) Account(ctx context.Context, customer, account string) (*ledgerv1.Account, error) {
	return call(ctx, c, "GetAccount", func(ctx context.Context) (*ledgerv1.Account, error) {
		return c.c.GetAccount(ctx, &ledgerv1.GetAccountRequest{CustomerId: customer, AccountId: account})
	})
}

// Balances returns the balances of one of the customer's accounts.
func (c *Client) Balances(ctx context.Context, customer, account string) ([]*ledgerv1.Balance, error) {
	return call(ctx, c, "ListBalances", func(ctx context.Context) ([]*ledgerv1.Balance, error) {
		r, err := c.c.ListBalances(ctx, &ledgerv1.AccountRequest{CustomerId: customer, AccountId: account})
		return r.GetBalances(), err
	})
}

// Page is one page of a list and the token of the next page; "" means it is the last page.
type Page[T any] struct {
	Items []T
	Next  string
}

// Transactions returns one page of an account's transactions booked from from to to (RFC 3339;
// "" leaves the bound open). page is "" for the first page, otherwise a Next of an earlier page.
func (c *Client) Transactions(ctx context.Context, customer, account, from, to, page string) (Page[*ledgerv1.Transaction], error) {
	return call(ctx, c, "ListTransactions", func(ctx context.Context) (Page[*ledgerv1.Transaction], error) {
		r, err := c.c.ListTransactions(ctx, &ledgerv1.ListTransactionsRequest{CustomerId: customer, AccountId: account, From: from, To: to, PageToken: page})
		return Page[*ledgerv1.Transaction]{Items: r.GetTransactions(), Next: r.GetNextPageToken()}, err
	})
}

func pageRequest(customer, account, page string) *ledgerv1.AccountPageRequest {
	return &ledgerv1.AccountPageRequest{CustomerId: customer, AccountId: account, PageToken: page}
}

// Beneficiaries returns one page of an account's beneficiaries.
func (c *Client) Beneficiaries(ctx context.Context, customer, account, page string) (Page[*ledgerv1.Beneficiary], error) {
	return call(ctx, c, "ListBeneficiaries", func(ctx context.Context) (Page[*ledgerv1.Beneficiary], error) {
		r, err := c.c.ListBeneficiaries(ctx, pageRequest(customer, account, page))
		return Page[*ledgerv1.Beneficiary]{Items: r.GetItems(), Next: r.GetNextPageToken()}, err
	})
}

// StandingOrders returns one page of an account's standing orders.
func (c *Client) StandingOrders(ctx context.Context, customer, account, page string) (Page[*ledgerv1.StandingOrder], error) {
	return call(ctx, c, "ListStandingOrders", func(ctx context.Context) (Page[*ledgerv1.StandingOrder], error) {
		r, err := c.c.ListStandingOrders(ctx, pageRequest(customer, account, page))
		return Page[*ledgerv1.StandingOrder]{Items: r.GetItems(), Next: r.GetNextPageToken()}, err
	})
}

// DirectDebits returns one page of an account's direct debits.
func (c *Client) DirectDebits(ctx context.Context, customer, account, page string) (Page[*ledgerv1.DirectDebit], error) {
	return call(ctx, c, "ListDirectDebits", func(ctx context.Context) (Page[*ledgerv1.DirectDebit], error) {
		r, err := c.c.ListDirectDebits(ctx, pageRequest(customer, account, page))
		return Page[*ledgerv1.DirectDebit]{Items: r.GetItems(), Next: r.GetNextPageToken()}, err
	})
}

// ScheduledPayments returns one page of an account's scheduled payments.
func (c *Client) ScheduledPayments(ctx context.Context, customer, account, page string) (Page[*ledgerv1.ScheduledPayment], error) {
	return call(ctx, c, "ListScheduledPayments", func(ctx context.Context) (Page[*ledgerv1.ScheduledPayment], error) {
		r, err := c.c.ListScheduledPayments(ctx, pageRequest(customer, account, page))
		return Page[*ledgerv1.ScheduledPayment]{Items: r.GetItems(), Next: r.GetNextPageToken()}, err
	})
}

// Party returns the holder of one of the customer's accounts, or the customer when account is "".
func (c *Client) Party(ctx context.Context, customer, account string) (*ledgerv1.Party, error) {
	return call(ctx, c, "GetParty", func(ctx context.Context) (*ledgerv1.Party, error) {
		return c.c.GetParty(ctx, &ledgerv1.GetPartyRequest{CustomerId: customer, AccountId: account})
	})
}
