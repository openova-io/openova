package ledgertest

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	ledgerv1 "github.com/openova-io/openova/products/fingate/api/ledger/v1"
)

// The page size when a request gives none, and the largest page served.
const (
	defaultPageSize = 50
	maxPageSize     = 100
)

// FullCapabilities offers every resource and payment type of the port, in pound sterling.
func FullCapabilities() *ledgerv1.Capabilities {
	return &ledgerv1.Capabilities{Resources: slices.Clone(Resources), PaymentTypes: slices.Clone(PaymentTypes), Currencies: []string{"GBP"}}
}

type memAccount struct {
	owner     string
	account   *ledgerv1.Account
	balance   int64 // in hundred-thousandths
	party     *ledgerv1.Party
	txns      []*ledgerv1.Transaction
	benefs    []*ledgerv1.Beneficiary
	standing  []*ledgerv1.StandingOrder
	debits    []*ledgerv1.DirectDebit
	scheduled []*ledgerv1.ScheduledPayment
}

type memPayment struct {
	req     *ledgerv1.SubmitPaymentRequest
	payment *ledgerv1.Payment
}

// Memory is a ledger held in memory. It serves the port over a fixed data set:
//
//	scheme kit, subject alice  customer cust-alice, accounts acc-1001 (1000.00 GBP, three
//	                           transactions and one item of each other list) and acc-1002
//	                           (50.00 GBP, one transaction)
//	scheme kit, subject bob    customer cust-bob, account acc-2001 (10.00 GBP)
//
// MemoryFixture describes that data set to the kit. A domestic payment is accepted and lowers
// the balance; a scheduled payment and a standing order are pending. An amount above the
// balance is refused. Memory is safe for concurrent use.
type Memory struct {
	ledgerv1.UnimplementedLedgerServer

	mu        sync.Mutex
	caps      *ledgerv1.Capabilities
	subjects  map[string]string // scheme + "/" + subject -> customer id
	customers map[string]*ledgerv1.Party
	accounts  map[string]*memAccount
	order     []string // account ids in the order they are listed
	payments  map[string]*ledgerv1.Payment
	keys      map[string]*memPayment // idempotency key -> the first request and its payment
	seq       int
}

// MemoryFixture is the fixture of the data set of NewMemory.
func MemoryFixture() Fixture {
	return Fixture{
		Customer:               Identity{Scheme: "kit", Subject: "alice"},
		OtherCustomer:          Identity{Scheme: "kit", Subject: "bob"},
		DebtorAccountID:        "acc-1001",
		Amount:                 "1.00",
		RefusedAmount:          "1000000.00",
		CreditorName:           "Kit Creditor",
		CreditorSchemeName:     "UK.OBIE.SortCodeAccountNumber",
		CreditorIdentification: "40400411111111",
	}
}

// NewMemory returns the in-memory ledger with the given capabilities. Its data set holds every
// list, whatever the capabilities say; a list absent from them answers UNIMPLEMENTED.
func NewMemory(caps *ledgerv1.Capabilities) *Memory {
	m := &Memory{
		caps:      proto.Clone(caps).(*ledgerv1.Capabilities),
		subjects:  map[string]string{"kit/alice": "cust-alice", "kit/bob": "cust-bob"},
		customers: map[string]*ledgerv1.Party{},
		accounts:  map[string]*memAccount{},
		payments:  map[string]*ledgerv1.Payment{},
		keys:      map[string]*memPayment{},
	}
	m.customers["cust-alice"] = &ledgerv1.Party{PartyId: "party-alice", Type: "Sole", Name: "Alice Example", Email: "alice@example.invalid"}
	m.customers["cust-bob"] = &ledgerv1.Party{PartyId: "party-bob", Type: "Sole", Name: "Bob Example"}
	gbp := func(a string) *ledgerv1.Money { return &ledgerv1.Money{Amount: a, Currency: "GBP"} }
	tx := func(id, amount, cd, at string) *ledgerv1.Transaction {
		return &ledgerv1.Transaction{TransactionId: id, Amount: gbp(amount), CreditDebit: cd, Status: "Booked", BookedAt: at, ValueAt: at, Reference: id}
	}
	m.add(&memAccount{
		owner:   "cust-alice",
		account: &ledgerv1.Account{AccountId: "acc-1001", Currency: "GBP", Type: "Personal", SubType: "CurrentAccount", Nickname: "Main", SchemeName: "UK.OBIE.SortCodeAccountNumber", Identification: "40400412345678", HolderName: "Alice Example", Status: "Enabled", OpenedAt: "2020-01-15T09:00:00Z"},
		balance: 1000 * unit,
		party:   &ledgerv1.Party{PartyId: "party-alice-1001", Type: "Sole", Name: "Alice Example"},
		txns: []*ledgerv1.Transaction{
			tx("tx-1001-1", "2500.00", "Credit", "2026-01-02T08:00:00Z"),
			tx("tx-1001-2", "1200.00", "Debit", "2026-01-05T08:00:00Z"),
			tx("tx-1001-3", "300.00", "Debit", "2026-01-09T08:00:00Z"),
		},
		benefs:    []*ledgerv1.Beneficiary{{BeneficiaryId: "ben-1001-1", Name: "Kit Creditor", SchemeName: "UK.OBIE.SortCodeAccountNumber", Identification: "40400411111111", Reference: "rent"}},
		standing:  []*ledgerv1.StandingOrder{{StandingOrderId: "so-1001-1", Frequency: "MNTH", Reference: "rent", NextAmount: gbp("750.00"), NextAt: "2026-02-01T00:00:00Z", Status: "Active", CreditorName: "Kit Creditor", CreditorSchemeName: "UK.OBIE.SortCodeAccountNumber", CreditorIdentification: "40400411111111"}},
		debits:    []*ledgerv1.DirectDebit{{DirectDebitId: "dd-1001-1", MandateId: "mandate-1", Name: "Kit Utility", Status: "Active", PreviousAt: "2026-01-20T00:00:00Z", PreviousAmount: gbp("42.50")}},
		scheduled: []*ledgerv1.ScheduledPayment{{ScheduledPaymentId: "sp-1001-1", ExecutionAt: "2026-03-01T00:00:00Z", Amount: gbp("99.00"), Reference: "insurance", CreditorName: "Kit Creditor", CreditorSchemeName: "UK.OBIE.SortCodeAccountNumber", CreditorIdentification: "40400411111111"}},
	})
	m.add(&memAccount{
		owner:   "cust-alice",
		account: &ledgerv1.Account{AccountId: "acc-1002", Currency: "GBP", Type: "Personal", SubType: "Savings", SchemeName: "UK.OBIE.SortCodeAccountNumber", Identification: "40400487654321", HolderName: "Alice Example", Status: "Enabled", OpenedAt: "2021-06-01T09:00:00Z"},
		balance: 50 * unit,
		party:   &ledgerv1.Party{PartyId: "party-alice-1002", Type: "Sole", Name: "Alice Example"},
		txns:    []*ledgerv1.Transaction{tx("tx-1002-1", "50.00", "Credit", "2026-01-03T08:00:00Z")},
	})
	m.add(&memAccount{
		owner:   "cust-bob",
		account: &ledgerv1.Account{AccountId: "acc-2001", Currency: "GBP", Type: "Personal", SubType: "CurrentAccount", SchemeName: "UK.OBIE.SortCodeAccountNumber", Identification: "40400422222222", HolderName: "Bob Example", Status: "Enabled", OpenedAt: "2022-03-10T09:00:00Z"},
		balance: 10 * unit,
		party:   &ledgerv1.Party{PartyId: "party-bob-2001", Type: "Sole", Name: "Bob Example"},
	})
	return m
}

func (m *Memory) add(a *memAccount) {
	m.accounts[a.account.GetAccountId()] = a
	m.order = append(m.order, a.account.GetAccountId())
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func (m *Memory) has(list []string, v string) bool { return slices.Contains(list, v) }

// owned returns the account when the customer exists and holds it.
func (m *Memory) owned(customer, account string) (*memAccount, error) {
	if _, ok := m.customers[customer]; !ok {
		return nil, status.Errorf(codes.NotFound, "unknown customer %q", customer)
	}
	a, ok := m.accounts[account]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "unknown account %q", account)
	}
	if a.owner != customer {
		return nil, status.Errorf(codes.PermissionDenied, "account %q is not the customer's", account)
	}
	return a, nil
}

// window returns the bounds of one page of n items and the token of the next page.
func window(n int, token string, size int32) (int, int, string, error) {
	from := 0
	if token != "" {
		v, err := strconv.Atoi(token)
		if err != nil || v < 0 || v > n {
			return 0, 0, "", status.Errorf(codes.InvalidArgument, "page_token %q is not one this ledger issued", token)
		}
		from = v
	}
	limit := int(size)
	if limit <= 0 {
		limit = defaultPageSize
	}
	limit = min(limit, maxPageSize)
	to := min(n, from+limit)
	next := ""
	if to < n {
		next = strconv.Itoa(to)
	}
	return from, to, next, nil
}

// pageOf returns one page of a list of messages, cloned.
func pageOf[T proto.Message](items []T, token string, size int32) ([]T, string, error) {
	from, to, next, err := window(len(items), token, size)
	if err != nil {
		return nil, "", err
	}
	out := make([]T, 0, to-from)
	for _, x := range items[from:to] {
		out = append(out, proto.Clone(x).(T))
	}
	return out, next, nil
}

// resource checks that a resource is offered and returns the customer's account.
func (m *Memory) resource(name, customer, account string) (*memAccount, error) {
	if !m.has(m.caps.GetResources(), name) {
		return nil, status.Errorf(codes.Unimplemented, "this ledger does not offer %s", name)
	}
	return m.owned(customer, account)
}

// GetCapabilities returns what the ledger offers.
func (m *Memory) GetCapabilities(context.Context, *ledgerv1.GetCapabilitiesRequest) (*ledgerv1.Capabilities, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return proto.Clone(m.caps).(*ledgerv1.Capabilities), nil
}

// ResolveCustomer maps a scheme and a subject to a customer.
func (m *Memory) ResolveCustomer(_ context.Context, r *ledgerv1.ResolveCustomerRequest) (*ledgerv1.Customer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.subjects[r.GetScheme()+"/"+r.GetSubject()]
	if !ok {
		return nil, status.Error(codes.NotFound, "unknown subject")
	}
	return &ledgerv1.Customer{CustomerId: id, DisplayName: m.customers[id].GetName()}, nil
}

// ListAccounts lists the customer's accounts.
func (m *Memory) ListAccounts(_ context.Context, r *ledgerv1.ListAccountsRequest) (*ledgerv1.ListAccountsResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.customers[r.GetCustomerId()]; !ok {
		return nil, status.Errorf(codes.NotFound, "unknown customer %q", r.GetCustomerId())
	}
	out := &ledgerv1.ListAccountsResponse{}
	for _, id := range m.order {
		if a := m.accounts[id]; a.owner == r.GetCustomerId() {
			out.Accounts = append(out.Accounts, proto.Clone(a.account).(*ledgerv1.Account))
		}
	}
	return out, nil
}

// GetAccount returns one of the customer's accounts.
func (m *Memory) GetAccount(_ context.Context, r *ledgerv1.GetAccountRequest) (*ledgerv1.Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, err := m.owned(r.GetCustomerId(), r.GetAccountId())
	if err != nil {
		return nil, err
	}
	return proto.Clone(a.account).(*ledgerv1.Account), nil
}

// ListBalances returns the closing booked balance of the account.
func (m *Memory) ListBalances(_ context.Context, r *ledgerv1.AccountRequest) (*ledgerv1.ListBalancesResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, err := m.owned(r.GetCustomerId(), r.GetAccountId())
	if err != nil {
		return nil, err
	}
	return &ledgerv1.ListBalancesResponse{Balances: []*ledgerv1.Balance{{
		Type: "ClosingBooked", Amount: &ledgerv1.Money{Amount: formatAmount(a.balance), Currency: a.account.GetCurrency()}, CreditDebit: "Credit", At: now(),
	}}}, nil
}

// ListTransactions returns one page of the account's transactions, oldest first.
func (m *Memory) ListTransactions(_ context.Context, r *ledgerv1.ListTransactionsRequest) (*ledgerv1.ListTransactionsResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, err := m.owned(r.GetCustomerId(), r.GetAccountId())
	if err != nil {
		return nil, err
	}
	items, next, err := pageOf(a.txns, r.GetPageToken(), r.GetPageSize())
	if err != nil {
		return nil, err
	}
	return &ledgerv1.ListTransactionsResponse{Transactions: items, NextPageToken: next}, nil
}

// ListBeneficiaries returns one page of the account's beneficiaries.
func (m *Memory) ListBeneficiaries(_ context.Context, r *ledgerv1.AccountPageRequest) (*ledgerv1.ListBeneficiariesResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, err := m.resource("beneficiaries", r.GetCustomerId(), r.GetAccountId())
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
func (m *Memory) ListStandingOrders(_ context.Context, r *ledgerv1.AccountPageRequest) (*ledgerv1.ListStandingOrdersResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, err := m.resource("standing-orders", r.GetCustomerId(), r.GetAccountId())
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
func (m *Memory) ListDirectDebits(_ context.Context, r *ledgerv1.AccountPageRequest) (*ledgerv1.ListDirectDebitsResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, err := m.resource("direct-debits", r.GetCustomerId(), r.GetAccountId())
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
func (m *Memory) ListScheduledPayments(_ context.Context, r *ledgerv1.AccountPageRequest) (*ledgerv1.ListScheduledPaymentsResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, err := m.resource("scheduled-payments", r.GetCustomerId(), r.GetAccountId())
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
func (m *Memory) GetParty(_ context.Context, r *ledgerv1.GetPartyRequest) (*ledgerv1.Party, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.has(m.caps.GetResources(), "party") {
		return nil, status.Error(codes.Unimplemented, "this ledger does not offer party")
	}
	if r.GetAccountId() == "" {
		p, ok := m.customers[r.GetCustomerId()]
		if !ok {
			return nil, status.Errorf(codes.NotFound, "unknown customer %q", r.GetCustomerId())
		}
		return proto.Clone(p).(*ledgerv1.Party), nil
	}
	a, err := m.owned(r.GetCustomerId(), r.GetAccountId())
	if err != nil {
		return nil, err
	}
	return proto.Clone(a.party).(*ledgerv1.Party), nil
}

// unit is one currency unit in hundred-thousandths, the finest step an amount of the port has.
const unit = 100000

// decimal parses an amount of the port into hundred-thousandths; ok is false when it is not one.
func decimal(s string) (int64, bool) {
	if !amountRE.MatchString(s) {
		return 0, false
	}
	whole, frac, _ := strings.Cut(s, ".")
	v, err := strconv.ParseInt(whole+frac+strings.Repeat("0", 5-len(frac)), 10, 64)
	return v, err == nil
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
func amountOn(a *memAccount, money *ledgerv1.Money) (int64, error) {
	v, ok := decimal(money.GetAmount())
	if !ok {
		return 0, status.Errorf(codes.InvalidArgument, "amount %q is not an amount", money.GetAmount())
	}
	if money.GetCurrency() != a.account.GetCurrency() {
		return 0, status.Errorf(codes.InvalidArgument, "currency %q is not the account's %s", money.GetCurrency(), a.account.GetCurrency())
	}
	return v, nil
}

// CheckFunds says whether the account's balance covers the amount.
func (m *Memory) CheckFunds(_ context.Context, r *ledgerv1.CheckFundsRequest) (*ledgerv1.CheckFundsResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, err := m.owned(r.GetCustomerId(), r.GetAccountId())
	if err != nil {
		return nil, err
	}
	v, err := amountOn(a, r.GetAmount())
	if err != nil {
		return nil, err
	}
	return &ledgerv1.CheckFundsResponse{Available: a.balance >= v, At: now()}, nil
}

// SubmitPayment makes a payment once per idempotency key.
func (m *Memory) SubmitPayment(_ context.Context, r *ledgerv1.SubmitPaymentRequest) (*ledgerv1.Payment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r.GetIdempotencyKey() == "" {
		return nil, status.Error(codes.InvalidArgument, "idempotency_key is empty")
	}
	if prev, ok := m.keys[r.GetIdempotencyKey()]; ok {
		if !proto.Equal(prev.req, r) {
			return nil, status.Error(codes.AlreadyExists, "the idempotency key was used with other content")
		}
		return proto.Clone(prev.payment).(*ledgerv1.Payment), nil
	}
	if !m.has(m.caps.GetPaymentTypes(), r.GetType()) {
		return nil, status.Errorf(codes.Unimplemented, "this ledger does not offer payment type %q", r.GetType())
	}
	if (r.GetType() == "domestic-standing-order") != (r.GetRecurrence() != nil) {
		return nil, status.Error(codes.InvalidArgument, "recurrence is set exactly when the type is domestic-standing-order")
	}
	a, err := m.owned(r.GetCustomerId(), r.GetDebtorAccountId())
	if err != nil {
		return nil, err
	}
	v, err := amountOn(a, r.GetAmount())
	if err != nil {
		return nil, err
	}
	if a.balance < v {
		return nil, status.Error(codes.FailedPrecondition, "insufficient funds")
	}
	m.seq++
	p := &ledgerv1.Payment{PaymentId: fmt.Sprintf("pay-%d", m.seq), Status: "pending", StatusAt: now()}
	if r.GetType() == "domestic" && r.GetRequestedExecutionAt() == "" {
		a.balance -= v
		p.Status = "accepted"
	}
	m.payments[p.GetPaymentId()] = p
	m.keys[r.GetIdempotencyKey()] = &memPayment{req: proto.Clone(r).(*ledgerv1.SubmitPaymentRequest), payment: p}
	return proto.Clone(p).(*ledgerv1.Payment), nil
}

// GetPayment returns a payment by its identifier.
func (m *Memory) GetPayment(_ context.Context, r *ledgerv1.GetPaymentRequest) (*ledgerv1.Payment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.payments[r.GetPaymentId()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "unknown payment %q", r.GetPaymentId())
	}
	return proto.Clone(p).(*ledgerv1.Payment), nil
}

// Serve serves a ledger over an in-process connection, with no network and no TLS, and
// returns a connection to it. Close the connection, then call stop.
func Serve(srv ledgerv1.LedgerServer, opts ...grpc.ServerOption) (conn *grpc.ClientConn, stop func(), err error) {
	lis := bufconn.Listen(1 << 20)
	s := grpc.NewServer(opts...)
	ledgerv1.RegisterLedgerServer(s, srv)
	go func() { _ = s.Serve(lis) }()
	conn, err = grpc.NewClient("passthrough:///inmemory",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		s.Stop()
		return nil, nil, err
	}
	return conn, s.Stop, nil
}
