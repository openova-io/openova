// Package sandbox is the sandbox ledger of the open-banking product: a ledger that serves the
// ledger port over a fixed set of made-up customers and accounts, held in memory. It is a test
// bank for partners and for the product's own conformance runs, never a bank's data. The data
// set is in data.go; KitFixture describes it to the ledger conformance kit.
//
// The codes it returns are those of the profile documents rw-4.0.0: account category Personal
// or Business, account type CACC or SVGS, account status Enabled, balance types CLBD and ITAV,
// entry status BOOK, mandate status ACTV, party type Sole.
//
// A domestic payment without an execution time is booked at once: it is settled, lowers the
// balance and appears as a Debit transaction. A scheduled payment appears in the account's
// scheduled payments and a standing order in its standing orders; both stay pending, because
// the sandbox executes nothing later. An amount above the balance is refused. All state is
// kept in memory and starts again from the data set when the process starts.
package sandbox

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	ledgerv1 "github.com/openova-io/openova/products/fingate/api/ledger/v1"
)

// The page size when a request gives none, and the largest page served.
const (
	defaultPageSize = 50
	maxPageSize     = 100
)

// The resources and payment types of the port. The sandbox offers all of them.
var (
	resources    = []string{"beneficiaries", "standing-orders", "direct-debits", "scheduled-payments", "party"}
	paymentTypes = []string{"domestic", "domestic-scheduled", "domestic-standing-order"}
)

type account struct {
	owner     string
	info      *ledgerv1.Account
	balance   int64 // in hundred-thousandths
	party     *ledgerv1.Party
	txns      []*ledgerv1.Transaction
	benefs    []*ledgerv1.Beneficiary
	standing  []*ledgerv1.StandingOrder
	debits    []*ledgerv1.DirectDebit
	scheduled []*ledgerv1.ScheduledPayment
}

type submitted struct {
	req     *ledgerv1.SubmitPaymentRequest
	payment *ledgerv1.Payment
}

// Ledger serves the ledger port over the sandbox data set. It is safe for concurrent use.
type Ledger struct {
	ledgerv1.UnimplementedLedgerServer

	mu        sync.Mutex
	caps      *ledgerv1.Capabilities
	subjects  map[string]string // scheme + "/" + subject -> customer id
	customers map[string]*ledgerv1.Party
	accounts  map[string]*account
	order     []string // account ids in the order they are listed
	payments  map[string]*ledgerv1.Payment
	keys      map[string]*submitted // idempotency key -> the first request and its payment
	seq       int
}

// New returns a sandbox ledger that holds the data set of data.go, with no payment made.
func New() *Ledger {
	l := &Ledger{
		subjects:  map[string]string{},
		customers: map[string]*ledgerv1.Party{},
		accounts:  map[string]*account{},
		payments:  map[string]*ledgerv1.Payment{},
		keys:      map[string]*submitted{},
	}
	currencies := map[string]bool{}
	for _, c := range dataSet() {
		l.subjects[Scheme+"/"+c.subject] = c.id
		l.customers[c.id] = c.party
		for _, a := range c.accounts {
			acc := &account{
				owner: c.id, info: a.info, party: a.party, benefs: a.benefs,
				standing: a.standing, debits: a.debits, scheduled: a.scheduled,
			}
			acc.balance = mustAmount(a.opening)
			for _, t := range a.txns {
				v := mustAmount(t.GetAmount().GetAmount())
				if t.GetCreditDebit() == "Debit" {
					v = -v
				}
				acc.balance += v
				t.BalanceAfter = &ledgerv1.Money{Amount: formatAmount(acc.balance), Currency: a.info.GetCurrency()}
				acc.txns = append(acc.txns, t)
			}
			l.accounts[a.info.GetAccountId()] = acc
			l.order = append(l.order, a.info.GetAccountId())
			currencies[a.info.GetCurrency()] = true
		}
	}
	l.caps = &ledgerv1.Capabilities{Resources: slices.Clone(resources), PaymentTypes: slices.Clone(paymentTypes)}
	for c := range currencies {
		l.caps.Currencies = append(l.caps.Currencies, c)
	}
	slices.Sort(l.caps.Currencies)
	return l
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// owned returns the account when the customer exists and holds it.
func (l *Ledger) owned(customer, accountID string) (*account, error) {
	if _, ok := l.customers[customer]; !ok {
		return nil, status.Errorf(codes.NotFound, "unknown customer %q", customer)
	}
	a, ok := l.accounts[accountID]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "unknown account %q", accountID)
	}
	if a.owner != customer {
		return nil, status.Errorf(codes.PermissionDenied, "account %q is not the customer's", accountID)
	}
	return a, nil
}

// resource checks that a resource is offered and returns the customer's account.
func (l *Ledger) resource(name, customer, accountID string) (*account, error) {
	if !slices.Contains(l.caps.GetResources(), name) {
		return nil, status.Errorf(codes.Unimplemented, "this ledger does not offer %s", name)
	}
	return l.owned(customer, accountID)
}

// pageOf returns one page of a list of messages, cloned, and the token of the next page. A page
// token is the position of the first item of the page.
func pageOf[T proto.Message](items []T, token string, size int32) ([]T, string, error) {
	from := 0
	if token != "" {
		v, err := strconv.Atoi(token)
		if err != nil || v < 0 || v > len(items) {
			return nil, "", status.Errorf(codes.InvalidArgument, "page_token %q is not one this ledger issued", token)
		}
		from = v
	}
	limit := int(size)
	if limit <= 0 {
		limit = defaultPageSize
	}
	to := min(len(items), from+min(limit, maxPageSize))
	next := ""
	if to < len(items) {
		next = strconv.Itoa(to)
	}
	out := make([]T, 0, to-from)
	for _, x := range items[from:to] {
		out = append(out, proto.Clone(x).(T))
	}
	return out, next, nil
}

// GetCapabilities returns what the ledger offers: every resource and payment type, in the
// currencies of its accounts.
func (l *Ledger) GetCapabilities(context.Context, *ledgerv1.GetCapabilitiesRequest) (*ledgerv1.Capabilities, error) {
	return proto.Clone(l.caps).(*ledgerv1.Capabilities), nil
}

// ResolveCustomer maps the scheme "sandbox" and a subject to a customer.
func (l *Ledger) ResolveCustomer(_ context.Context, r *ledgerv1.ResolveCustomerRequest) (*ledgerv1.Customer, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	id, ok := l.subjects[r.GetScheme()+"/"+r.GetSubject()]
	if !ok {
		return nil, status.Error(codes.NotFound, "unknown subject")
	}
	return &ledgerv1.Customer{CustomerId: id, DisplayName: l.customers[id].GetName()}, nil
}

// ListAccounts lists the customer's accounts.
func (l *Ledger) ListAccounts(_ context.Context, r *ledgerv1.ListAccountsRequest) (*ledgerv1.ListAccountsResponse, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.customers[r.GetCustomerId()]; !ok {
		return nil, status.Errorf(codes.NotFound, "unknown customer %q", r.GetCustomerId())
	}
	out := &ledgerv1.ListAccountsResponse{}
	for _, id := range l.order {
		if a := l.accounts[id]; a.owner == r.GetCustomerId() {
			out.Accounts = append(out.Accounts, proto.Clone(a.info).(*ledgerv1.Account))
		}
	}
	return out, nil
}

// GetAccount returns one of the customer's accounts.
func (l *Ledger) GetAccount(_ context.Context, r *ledgerv1.GetAccountRequest) (*ledgerv1.Account, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, err := l.owned(r.GetCustomerId(), r.GetAccountId())
	if err != nil {
		return nil, err
	}
	return proto.Clone(a.info).(*ledgerv1.Account), nil
}

// ListBalances returns the closing booked (CLBD) and the interim available (ITAV) balance.
func (l *Ledger) ListBalances(_ context.Context, r *ledgerv1.AccountRequest) (*ledgerv1.ListBalancesResponse, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, err := l.owned(r.GetCustomerId(), r.GetAccountId())
	if err != nil {
		return nil, err
	}
	at := now()
	out := &ledgerv1.ListBalancesResponse{}
	for _, typ := range []string{"CLBD", "ITAV"} {
		out.Balances = append(out.Balances, &ledgerv1.Balance{
			Type: typ, Amount: &ledgerv1.Money{Amount: formatAmount(a.balance), Currency: a.info.GetCurrency()}, CreditDebit: "Credit", At: at,
		})
	}
	return out, nil
}

// window parses the optional from and to of a transaction request.
func window(from, to string) (time.Time, time.Time, error) {
	var bounds [2]time.Time
	for i, v := range []string{from, to} {
		if v == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return bounds[0], bounds[1], status.Errorf(codes.InvalidArgument, "%q is not an RFC 3339 time", v)
		}
		bounds[i] = t
	}
	return bounds[0], bounds[1], nil
}

// ListTransactions returns one page of the account's transactions, oldest first, booked between
// from and to, both included; an empty bound is open.
func (l *Ledger) ListTransactions(_ context.Context, r *ledgerv1.ListTransactionsRequest) (*ledgerv1.ListTransactionsResponse, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, err := l.owned(r.GetCustomerId(), r.GetAccountId())
	if err != nil {
		return nil, err
	}
	from, to, err := window(r.GetFrom(), r.GetTo())
	if err != nil {
		return nil, err
	}
	var in []*ledgerv1.Transaction
	for _, t := range a.txns {
		booked, err := time.Parse(time.RFC3339, t.GetBookedAt())
		if err != nil {
			return nil, status.Errorf(codes.Internal, "transaction %s: %v", t.GetTransactionId(), err)
		}
		if (from.IsZero() || !booked.Before(from)) && (to.IsZero() || !booked.After(to)) {
			in = append(in, t)
		}
	}
	items, next, err := pageOf(in, r.GetPageToken(), r.GetPageSize())
	if err != nil {
		return nil, err
	}
	return &ledgerv1.ListTransactionsResponse{Transactions: items, NextPageToken: next}, nil
}

// ListBeneficiaries returns one page of the account's beneficiaries.
func (l *Ledger) ListBeneficiaries(_ context.Context, r *ledgerv1.AccountPageRequest) (*ledgerv1.ListBeneficiariesResponse, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, err := l.resource("beneficiaries", r.GetCustomerId(), r.GetAccountId())
	if err != nil {
		return nil, err
	}
	items, next, err := pageOf(a.benefs, r.GetPageToken(), r.GetPageSize())
	if err != nil {
		return nil, err
	}
	return &ledgerv1.ListBeneficiariesResponse{Items: items, NextPageToken: next}, nil
}

// ListStandingOrders returns one page of the account's standing orders.
func (l *Ledger) ListStandingOrders(_ context.Context, r *ledgerv1.AccountPageRequest) (*ledgerv1.ListStandingOrdersResponse, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, err := l.resource("standing-orders", r.GetCustomerId(), r.GetAccountId())
	if err != nil {
		return nil, err
	}
	items, next, err := pageOf(a.standing, r.GetPageToken(), r.GetPageSize())
	if err != nil {
		return nil, err
	}
	return &ledgerv1.ListStandingOrdersResponse{Items: items, NextPageToken: next}, nil
}

// ListDirectDebits returns one page of the account's direct debits.
func (l *Ledger) ListDirectDebits(_ context.Context, r *ledgerv1.AccountPageRequest) (*ledgerv1.ListDirectDebitsResponse, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, err := l.resource("direct-debits", r.GetCustomerId(), r.GetAccountId())
	if err != nil {
		return nil, err
	}
	items, next, err := pageOf(a.debits, r.GetPageToken(), r.GetPageSize())
	if err != nil {
		return nil, err
	}
	return &ledgerv1.ListDirectDebitsResponse{Items: items, NextPageToken: next}, nil
}

// ListScheduledPayments returns one page of the account's scheduled payments.
func (l *Ledger) ListScheduledPayments(_ context.Context, r *ledgerv1.AccountPageRequest) (*ledgerv1.ListScheduledPaymentsResponse, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, err := l.resource("scheduled-payments", r.GetCustomerId(), r.GetAccountId())
	if err != nil {
		return nil, err
	}
	items, next, err := pageOf(a.scheduled, r.GetPageToken(), r.GetPageSize())
	if err != nil {
		return nil, err
	}
	return &ledgerv1.ListScheduledPaymentsResponse{Items: items, NextPageToken: next}, nil
}

// GetParty returns the customer, or the holder of one of the customer's accounts.
func (l *Ledger) GetParty(_ context.Context, r *ledgerv1.GetPartyRequest) (*ledgerv1.Party, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if r.GetAccountId() == "" {
		p, ok := l.customers[r.GetCustomerId()]
		if !ok {
			return nil, status.Errorf(codes.NotFound, "unknown customer %q", r.GetCustomerId())
		}
		return proto.Clone(p).(*ledgerv1.Party), nil
	}
	a, err := l.owned(r.GetCustomerId(), r.GetAccountId())
	if err != nil {
		return nil, err
	}
	return proto.Clone(a.party).(*ledgerv1.Party), nil
}

// unit is one currency unit in hundred-thousandths, the finest step an amount of the port has.
const unit = 100000

var amountRE = regexp.MustCompile(`^\d{1,13}$|^\d{1,13}\.\d{1,5}$`)

// parseAmount parses an amount of the port into hundred-thousandths; ok is false when it is
// not one.
func parseAmount(s string) (int64, bool) {
	if !amountRE.MatchString(s) {
		return 0, false
	}
	whole, frac, _ := strings.Cut(s, ".")
	v, err := strconv.ParseInt(whole+frac+strings.Repeat("0", 5-len(frac)), 10, 64)
	return v, err == nil
}

// mustAmount parses an amount of the data set, which is always well formed.
func mustAmount(s string) int64 {
	v, ok := parseAmount(s)
	if !ok {
		panic(fmt.Sprintf("sandbox data: %q is not an amount", s))
	}
	return v
}

// formatAmount writes hundred-thousandths as an amount of the port with two to five decimals.
func formatAmount(v int64) string {
	s := fmt.Sprintf("%d.%05d", v/unit, v%unit)
	for strings.HasSuffix(s, "0") && len(s)-strings.Index(s, ".") > 3 {
		s = s[:len(s)-1]
	}
	return s
}

// amountOn parses an amount in the account's currency.
func amountOn(a *account, money *ledgerv1.Money) (int64, error) {
	v, ok := parseAmount(money.GetAmount())
	if !ok {
		return 0, status.Errorf(codes.InvalidArgument, "amount %q is not an amount", money.GetAmount())
	}
	if money.GetCurrency() != a.info.GetCurrency() {
		return 0, status.Errorf(codes.InvalidArgument, "currency %q is not the account's %s", money.GetCurrency(), a.info.GetCurrency())
	}
	return v, nil
}

// CheckFunds says whether the account's balance covers the amount.
func (l *Ledger) CheckFunds(_ context.Context, r *ledgerv1.CheckFundsRequest) (*ledgerv1.CheckFundsResponse, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, err := l.owned(r.GetCustomerId(), r.GetAccountId())
	if err != nil {
		return nil, err
	}
	v, err := amountOn(a, r.GetAmount())
	if err != nil {
		return nil, err
	}
	return &ledgerv1.CheckFundsResponse{Available: a.balance >= v, At: now()}, nil
}

// validPayment checks the fields of a payment request that do not depend on the ledger's data.
func validPayment(r *ledgerv1.SubmitPaymentRequest) error {
	if (r.GetType() == "domestic-standing-order") != (r.GetRecurrence() != nil) {
		return status.Error(codes.InvalidArgument, "recurrence is set exactly when the type is domestic-standing-order")
	}
	if r.GetType() == "domestic-scheduled" && r.GetRequestedExecutionAt() == "" {
		return status.Error(codes.InvalidArgument, "requested_execution_at is empty for a scheduled payment")
	}
	if v := r.GetRequestedExecutionAt(); v != "" {
		if _, err := time.Parse(time.RFC3339, v); err != nil {
			return status.Errorf(codes.InvalidArgument, "requested_execution_at %q is not an RFC 3339 time", v)
		}
	}
	if rec := r.GetRecurrence(); rec != nil && rec.GetFirstAmount() == nil {
		return status.Error(codes.InvalidArgument, "recurrence.first_amount is not set")
	}
	return nil
}

// SubmitPayment makes a payment once per idempotency key.
func (l *Ledger) SubmitPayment(_ context.Context, r *ledgerv1.SubmitPaymentRequest) (*ledgerv1.Payment, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if r.GetIdempotencyKey() == "" {
		return nil, status.Error(codes.InvalidArgument, "idempotency_key is empty")
	}
	if prev, ok := l.keys[r.GetIdempotencyKey()]; ok {
		if !proto.Equal(prev.req, r) {
			return nil, status.Error(codes.AlreadyExists, "the idempotency key was used with other content")
		}
		return proto.Clone(prev.payment).(*ledgerv1.Payment), nil
	}
	if !slices.Contains(l.caps.GetPaymentTypes(), r.GetType()) {
		return nil, status.Errorf(codes.Unimplemented, "this ledger does not offer payment type %q", r.GetType())
	}
	if err := validPayment(r); err != nil {
		return nil, err
	}
	a, err := l.owned(r.GetCustomerId(), r.GetDebtorAccountId())
	if err != nil {
		return nil, err
	}
	v, err := amountOn(a, r.GetAmount())
	if err != nil {
		return nil, err
	}
	if rec := r.GetRecurrence(); rec != nil {
		if _, err := amountOn(a, rec.GetFirstAmount()); err != nil {
			return nil, err
		}
	}
	if a.balance < v {
		return nil, status.Error(codes.FailedPrecondition, "insufficient funds")
	}
	l.seq++
	at := now()
	p := &ledgerv1.Payment{PaymentId: fmt.Sprintf("pay-%06d", l.seq), Status: "pending", StatusAt: at}
	amount := &ledgerv1.Money{Amount: formatAmount(v), Currency: a.info.GetCurrency()}
	switch {
	case r.GetType() == "domestic" && r.GetRequestedExecutionAt() == "":
		a.balance -= v
		p.Status = "settled"
		a.txns = append(a.txns, &ledgerv1.Transaction{
			TransactionId: "tx-" + p.GetPaymentId(), Amount: amount, CreditDebit: "Debit", Status: "BOOK",
			BookedAt: at, ValueAt: at, Information: "Payment to " + r.GetCreditorName(), Reference: r.GetReference(),
			CounterpartyName: r.GetCreditorName(), CounterpartyIdentification: r.GetCreditorIdentification(),
			BalanceAfter: &ledgerv1.Money{Amount: formatAmount(a.balance), Currency: a.info.GetCurrency()},
		})
	case r.GetType() == "domestic-standing-order":
		rec := r.GetRecurrence()
		a.standing = append(a.standing, &ledgerv1.StandingOrder{
			StandingOrderId: "so-" + p.GetPaymentId(), Frequency: rec.GetFrequency(), Reference: r.GetReference(),
			NextAmount: proto.Clone(rec.GetFirstAmount()).(*ledgerv1.Money), NextAt: rec.GetFirstPaymentAt(), FinalAt: rec.GetFinalPaymentAt(),
			Status: "ACTV", CreditorName: r.GetCreditorName(), CreditorSchemeName: r.GetCreditorSchemeName(),
			CreditorIdentification: r.GetCreditorIdentification(),
		})
	default:
		a.scheduled = append(a.scheduled, &ledgerv1.ScheduledPayment{
			ScheduledPaymentId: "sp-" + p.GetPaymentId(), ExecutionAt: r.GetRequestedExecutionAt(), Amount: amount,
			Reference: r.GetReference(), CreditorName: r.GetCreditorName(), CreditorSchemeName: r.GetCreditorSchemeName(),
			CreditorIdentification: r.GetCreditorIdentification(),
		})
	}
	l.payments[p.GetPaymentId()] = p
	l.keys[r.GetIdempotencyKey()] = &submitted{req: proto.Clone(r).(*ledgerv1.SubmitPaymentRequest), payment: p}
	return proto.Clone(p).(*ledgerv1.Payment), nil
}

// GetPayment returns a payment by its identifier.
func (l *Ledger) GetPayment(_ context.Context, r *ledgerv1.GetPaymentRequest) (*ledgerv1.Payment, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	p, ok := l.payments[r.GetPaymentId()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "unknown payment %q", r.GetPaymentId())
	}
	return proto.Clone(p).(*ledgerv1.Payment), nil
}
