// Package ledgertest holds the conformance kit of the ledger port and an in-memory ledger
// that passes it.
//
// Run calls every operation of the port against a ledger and returns one result per named
// check. A ledger conforms when no check fails. The kit reads the ledger through a fixture:
// two identities that resolve to two different customers, an account of the first customer
// that can pay a small amount and refuses a large one, and a creditor to pay. It submits real
// payments, so it is run against a test ledger only.
//
// What the kit asserts:
//
//   - Capabilities name only the known resources and payment types, and at least one currency.
//   - Every field the port fixes has its format: an amount matches ^\d{1,13}$|^\d{1,13}\.\d{1,5}$,
//     a currency is three capital letters, a time is RFC 3339, a credit or debit indicator is
//     Credit or Debit, a payment status is pending, accepted, settled or rejected.
//   - Pagination: walking a list with pages of one and with pages of two gives the same items in
//     the same order, no page holds more than asked, and no item repeats. At least one account
//     of the customer has two transactions, so that the walk crosses a page.
//   - Idempotent re-submission: the same idempotency key with the same content returns the same
//     payment; the same key with other content is ALREADY_EXISTS.
//   - Error codes: an unknown customer, account or payment is NOT_FOUND; another customer's
//     account is PERMISSION_DENIED; a payment the account cannot pay is FAILED_PRECONDITION with
//     a message; a resource or payment type absent from the capabilities is UNIMPLEMENTED.
//     UNAVAILABLE cannot be provoked by a caller and is not checked.
//   - Every one of the 14 operations was called at least once.
package ledgertest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	ledgerv1 "github.com/openova-io/openova/products/fingate/api/ledger/v1"
)

// The values the port allows in Capabilities.
var (
	Resources    = []string{"beneficiaries", "standing-orders", "direct-debits", "scheduled-payments", "party"}
	PaymentTypes = []string{"domestic", "domestic-scheduled", "domestic-standing-order"}
)

// The payment statuses the port allows.
var paymentStatuses = []string{"pending", "accepted", "settled", "rejected"}

var (
	amountRE   = regexp.MustCompile(`^\d{1,13}$|^\d{1,13}\.\d{1,5}$`)
	currencyRE = regexp.MustCompile(`^[A-Z]{3}$`)
)

// callTimeout bounds every call the kit makes.
const callTimeout = 10 * time.Second

// Identity is what ResolveCustomer receives: a scheme and a subject.
type Identity struct {
	Scheme  string `json:"scheme"`
	Subject string `json:"subject"`
}

// Fixture describes the test data of the ledger under test.
type Fixture struct {
	// Customer resolves to the customer whose data the kit reads and pays from.
	Customer Identity `json:"customer"`
	// OtherCustomer resolves to a second customer that holds at least one account.
	OtherCustomer Identity `json:"other_customer"`
	// DebtorAccountID is an account of Customer.
	DebtorAccountID string `json:"debtor_account_id"`
	// Amount is an amount the debtor account can pay, in the account's currency.
	Amount string `json:"amount"`
	// RefusedAmount is an amount the debtor account cannot pay: CheckFunds answers false and
	// SubmitPayment answers FAILED_PRECONDITION.
	RefusedAmount string `json:"refused_amount"`
	// The creditor every payment of the kit goes to.
	CreditorName           string `json:"creditor_name"`
	CreditorSchemeName     string `json:"creditor_scheme_name"`
	CreditorIdentification string `json:"creditor_identification"`
}

// Validate reports the first field of the fixture that is missing or malformed.
func (f Fixture) Validate() error {
	for _, v := range []struct{ name, value string }{
		{"customer.scheme", f.Customer.Scheme}, {"customer.subject", f.Customer.Subject},
		{"other_customer.scheme", f.OtherCustomer.Scheme}, {"other_customer.subject", f.OtherCustomer.Subject},
		{"debtor_account_id", f.DebtorAccountID}, {"creditor_name", f.CreditorName},
		{"creditor_scheme_name", f.CreditorSchemeName}, {"creditor_identification", f.CreditorIdentification},
	} {
		if v.value == "" {
			return fmt.Errorf("fixture: %s is empty", v.name)
		}
	}
	if f.Customer == f.OtherCustomer {
		return errors.New("fixture: customer and other_customer are the same identity")
	}
	for _, v := range []struct{ name, value string }{{"amount", f.Amount}, {"refused_amount", f.RefusedAmount}} {
		if !amountRE.MatchString(v.value) {
			return fmt.Errorf("fixture: %s %q is not an amount", v.name, v.value)
		}
	}
	return nil
}

// Result is the outcome of one named check. Err is nil when the check passed.
type Result struct {
	Name string
	Err  error
}

// Report is the outcome of one run of the kit.
type Report struct {
	Results []Result
	// Called lists the operations the run called, by method name, sorted.
	Called []string
	// Operations is the number of operations of the port.
	Operations int
}

// Failed returns the results whose check failed.
func (r *Report) Failed() []Result {
	var out []Result
	for _, x := range r.Results {
		if x.Err != nil {
			out = append(out, x)
		}
	}
	return out
}

// Passed reports whether every check passed.
func (r *Report) Passed() bool { return len(r.Failed()) == 0 }

// Print writes one line per check, PASS or FAIL with its reason, then a summary line.
func (r *Report) Print(w io.Writer) error {
	var b strings.Builder
	for _, x := range r.Results {
		if x.Err != nil {
			fmt.Fprintf(&b, "FAIL %s: %v\n", x.Name, x.Err)
		} else {
			fmt.Fprintf(&b, "PASS %s\n", x.Name)
		}
	}
	fmt.Fprintf(&b, "checks passed: %d/%d\noperations called: %d/%d\n",
		len(r.Results)-len(r.Failed()), len(r.Results), len(r.Called), r.Operations)
	_, err := io.WriteString(w, b.String())
	return err
}

// counting records the method of every call made through it.
type counting struct {
	cc   grpc.ClientConnInterface
	mu   sync.Mutex
	seen map[string]bool
}

func (c *counting) Invoke(ctx context.Context, method string, args, reply any, opts ...grpc.CallOption) error {
	c.mu.Lock()
	c.seen[method] = true
	c.mu.Unlock()
	return c.cc.Invoke(ctx, method, args, reply, opts...)
}

func (c *counting) NewStream(ctx context.Context, desc *grpc.StreamDesc, method string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	return c.cc.NewStream(ctx, desc, method, opts...)
}

// kit is the state of one run.
type kit struct {
	ctx     context.Context
	c       ledgerv1.LedgerClient
	fx      Fixture
	run     string
	n       int
	results []Result
	caps    *ledgerv1.Capabilities
	cust    string
	other   string
	accs    []*ledgerv1.Account
	foreign string // an account of the other customer
}

func (k *kit) check(name string, err error) {
	k.results = append(k.results, Result{Name: name, Err: err})
}

// call gives one call its own deadline.
func (k *kit) call() (context.Context, context.CancelFunc) {
	return context.WithTimeout(k.ctx, callTimeout)
}

// key returns a fresh idempotency key of this run.
func (k *kit) key() string {
	k.n++
	return fmt.Sprintf("fingate-conformance-%s-%d", k.run, k.n)
}

// Run checks the ledger behind cc against the port and the fixture.
func Run(ctx context.Context, cc grpc.ClientConnInterface, fx Fixture) *Report {
	cnt := &counting{cc: cc, seen: map[string]bool{}}
	k := &kit{ctx: ctx, c: ledgerv1.NewLedgerClient(cnt), fx: fx, run: runID()}
	if err := fx.Validate(); err != nil {
		k.check("fixture", err)
	} else {
		k.all()
	}
	rep := &Report{Results: k.results, Operations: len(ledgerv1.Ledger_ServiceDesc.Methods)}
	for m := range cnt.seen {
		rep.Called = append(rep.Called, m[strings.LastIndex(m, "/")+1:])
	}
	sort.Strings(rep.Called)
	var missing []string
	for _, m := range ledgerv1.Ledger_ServiceDesc.Methods {
		if !slices.Contains(rep.Called, m.MethodName) {
			missing = append(missing, m.MethodName)
		}
	}
	var err error
	if len(missing) > 0 {
		err = fmt.Errorf("not called: %s", strings.Join(missing, ", "))
	}
	rep.Results = append(rep.Results, Result{Name: "every-operation-called", Err: err})
	return rep
}

func runID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return time.Now().UTC().Format("20060102T150405.000000000")
	}
	return hex.EncodeToString(b)
}

// wantCode returns nil when err carries the code want.
func wantCode(err error, want codes.Code) error {
	if err == nil {
		return fmt.Errorf("the call succeeded; want %s", want)
	}
	if got := status.Code(err); got != want {
		return fmt.Errorf("got %s (%s); want %s", got, status.Convert(err).Message(), want)
	}
	return nil
}

func checkMoney(field string, m *ledgerv1.Money) error {
	if m == nil {
		return fmt.Errorf("%s is not set", field)
	}
	if !amountRE.MatchString(m.GetAmount()) {
		return fmt.Errorf("%s.amount %q is not an amount", field, m.GetAmount())
	}
	if !currencyRE.MatchString(m.GetCurrency()) {
		return fmt.Errorf("%s.currency %q is not an ISO 4217 code", field, m.GetCurrency())
	}
	return nil
}

// checkTime checks an RFC 3339 time; an empty value passes unless it is required.
func checkTime(field, v string, required bool) error {
	if v == "" {
		if required {
			return fmt.Errorf("%s is empty", field)
		}
		return nil
	}
	if _, err := time.Parse(time.RFC3339, v); err != nil {
		return fmt.Errorf("%s %q is not an RFC 3339 time", field, v)
	}
	return nil
}

func checkCreditDebit(field, v string) error {
	if v != "Credit" && v != "Debit" {
		return fmt.Errorf("%s %q is neither Credit nor Debit", field, v)
	}
	return nil
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func (k *kit) all() {
	if !k.capabilities() || !k.customers() || !k.accounts() {
		return
	}
	k.balances()
	k.transactions()
	k.pagedResources()
	k.party()
	k.funds()
	k.payments()
}

func (k *kit) capabilities() bool {
	ctx, cancel := k.call()
	defer cancel()
	caps, err := k.c.GetCapabilities(ctx, &ledgerv1.GetCapabilitiesRequest{})
	if err == nil {
		err = validCapabilities(caps)
	}
	k.check("capabilities", err)
	k.caps = caps
	return err == nil
}

func validCapabilities(c *ledgerv1.Capabilities) error {
	for _, set := range []struct {
		name    string
		got     []string
		allowed []string
	}{{"resources", c.GetResources(), Resources}, {"payment_types", c.GetPaymentTypes(), PaymentTypes}} {
		seen := map[string]bool{}
		for _, v := range set.got {
			if !slices.Contains(set.allowed, v) {
				return fmt.Errorf("%s holds %q, which the port does not know", set.name, v)
			}
			if seen[v] {
				return fmt.Errorf("%s holds %q twice", set.name, v)
			}
			seen[v] = true
		}
	}
	if len(c.GetCurrencies()) == 0 {
		return errors.New("currencies is empty")
	}
	for _, v := range c.GetCurrencies() {
		if !currencyRE.MatchString(v) {
			return fmt.Errorf("currency %q is not an ISO 4217 code", v)
		}
	}
	return nil
}

func (k *kit) resolve(id Identity) (string, error) {
	ctx, cancel := k.call()
	defer cancel()
	c, err := k.c.ResolveCustomer(ctx, &ledgerv1.ResolveCustomerRequest{Scheme: id.Scheme, Subject: id.Subject})
	if err != nil {
		return "", err
	}
	if c.GetCustomerId() == "" {
		return "", errors.New("customer_id is empty")
	}
	return c.GetCustomerId(), nil
}

func (k *kit) customers() bool {
	var err error
	k.cust, err = k.resolve(k.fx.Customer)
	k.check("resolve-customer", err)
	if err != nil {
		return false
	}
	_, err = k.resolve(Identity{Scheme: k.fx.Customer.Scheme, Subject: "fingate-conformance-unknown-" + k.run})
	k.check("resolve-customer-unknown", wantCode(err, codes.NotFound))
	k.other, err = k.resolve(k.fx.OtherCustomer)
	if err == nil && k.other == k.cust {
		err = errors.New("other_customer resolves to the same customer_id as customer")
	}
	k.check("resolve-other-customer", err)
	return err == nil
}

func (k *kit) listAccounts(customer string) ([]*ledgerv1.Account, error) {
	ctx, cancel := k.call()
	defer cancel()
	r, err := k.c.ListAccounts(ctx, &ledgerv1.ListAccountsRequest{CustomerId: customer})
	return r.GetAccounts(), err
}

func validAccount(a *ledgerv1.Account) error {
	if a.GetAccountId() == "" {
		return errors.New("an account has an empty account_id")
	}
	if !currencyRE.MatchString(a.GetCurrency()) {
		return fmt.Errorf("account %s: currency %q is not an ISO 4217 code", a.GetAccountId(), a.GetCurrency())
	}
	return checkTime("account "+a.GetAccountId()+": opened_at", a.GetOpenedAt(), false)
}

func (k *kit) accounts() bool {
	accs, err := k.listAccounts(k.cust)
	if err == nil && len(accs) == 0 {
		err = errors.New("the customer has no account")
	}
	seen := map[string]bool{}
	for _, a := range accs {
		if err != nil {
			break
		}
		err = validAccount(a)
		if err == nil && seen[a.GetAccountId()] {
			err = fmt.Errorf("account %s is listed twice", a.GetAccountId())
		}
		seen[a.GetAccountId()] = true
	}
	if err == nil && !seen[k.fx.DebtorAccountID] {
		err = fmt.Errorf("debtor_account_id %s is not an account of the customer", k.fx.DebtorAccountID)
	}
	k.check("list-accounts", err)
	if err != nil {
		return false
	}
	k.accs = accs

	_, err = k.listAccounts("fingate-conformance-unknown-" + k.run)
	k.check("list-accounts-unknown-customer", wantCode(err, codes.NotFound))

	err = nil
	for _, a := range accs {
		ctx, cancel := k.call()
		got, gerr := k.c.GetAccount(ctx, &ledgerv1.GetAccountRequest{CustomerId: k.cust, AccountId: a.GetAccountId()})
		cancel()
		if gerr != nil {
			err = fmt.Errorf("account %s: %w", a.GetAccountId(), gerr)
			break
		}
		if !proto.Equal(got, a) {
			err = fmt.Errorf("account %s: reading it alone differs from its entry in the list", a.GetAccountId())
			break
		}
	}
	k.check("get-account", err)

	ctx, cancel := k.call()
	_, err = k.c.GetAccount(ctx, &ledgerv1.GetAccountRequest{CustomerId: k.cust, AccountId: "fingate-conformance-unknown-" + k.run})
	cancel()
	k.check("get-account-unknown", wantCode(err, codes.NotFound))

	others, err := k.listAccounts(k.other)
	if err == nil && len(others) == 0 {
		err = errors.New("the other customer has no account")
	}
	if err != nil {
		k.check("get-account-not-owned", fmt.Errorf("listing the other customer's accounts: %w", err))
		return false
	}
	k.foreign = others[0].GetAccountId()
	ctx, cancel = k.call()
	_, err = k.c.GetAccount(ctx, &ledgerv1.GetAccountRequest{CustomerId: k.cust, AccountId: k.foreign})
	cancel()
	k.check("get-account-not-owned", wantCode(err, codes.PermissionDenied))
	return true
}

func (k *kit) balances() {
	var err error
	for _, a := range k.accs {
		ctx, cancel := k.call()
		r, berr := k.c.ListBalances(ctx, &ledgerv1.AccountRequest{CustomerId: k.cust, AccountId: a.GetAccountId()})
		cancel()
		if berr != nil {
			err = fmt.Errorf("account %s: %w", a.GetAccountId(), berr)
			break
		}
		if len(r.GetBalances()) == 0 {
			err = fmt.Errorf("account %s has no balance", a.GetAccountId())
			break
		}
		for _, b := range r.GetBalances() {
			p := "account " + a.GetAccountId() + ": balance"
			if err = firstErr(nonEmpty(p+".type", b.GetType()), checkMoney(p+".amount", b.GetAmount()),
				checkCreditDebit(p+".credit_debit", b.GetCreditDebit()), checkTime(p+".at", b.GetAt(), true)); err != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}
	k.check("list-balances", err)
	ctx, cancel := k.call()
	_, err = k.c.ListBalances(ctx, &ledgerv1.AccountRequest{CustomerId: k.cust, AccountId: k.foreign})
	cancel()
	k.check("list-balances-not-owned", wantCode(err, codes.PermissionDenied))
}

func nonEmpty(field, v string) error {
	if v == "" {
		return fmt.Errorf("%s is empty", field)
	}
	return nil
}

// page is one page of a list: the identifiers of its items, each item's own check, and the
// token of the next page.
type page struct {
	ids  []string
	errs []error
	next string
}

// lister fetches one page of one account's list.
type lister func(ctx context.Context, account, token string, size int32) (page, error)

// walk reads the whole list of an account with pages of the given size.
func (k *kit) walk(l lister, account string, size int32) ([]string, error) {
	var ids []string
	token := ""
	for n := 0; ; n++ {
		if n == 1000 {
			return nil, errors.New("more than 1000 pages")
		}
		ctx, cancel := k.call()
		p, err := l(ctx, account, token, size)
		cancel()
		if err != nil {
			return nil, err
		}
		if len(p.ids) > int(size) {
			return nil, fmt.Errorf("a page of size %d holds %d items", size, len(p.ids))
		}
		if err := firstErr(p.errs...); err != nil {
			return nil, err
		}
		ids = append(ids, p.ids...)
		if p.next == "" {
			break
		}
		token = p.next
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" {
			return nil, errors.New("an item has an empty identifier")
		}
		if seen[id] {
			return nil, fmt.Errorf("item %s appears twice", id)
		}
		seen[id] = true
	}
	return ids, nil
}

// paged checks one list on every account with pages of one and of two. It returns the most
// items one account holds.
func (k *kit) paged(l lister) (int, error) {
	most := 0
	for _, a := range k.accs {
		one, err := k.walk(l, a.GetAccountId(), 1)
		if err != nil {
			return 0, fmt.Errorf("account %s, pages of 1: %w", a.GetAccountId(), err)
		}
		two, err := k.walk(l, a.GetAccountId(), 2)
		if err != nil {
			return 0, fmt.Errorf("account %s, pages of 2: %w", a.GetAccountId(), err)
		}
		if !slices.Equal(one, two) {
			return 0, fmt.Errorf("account %s: pages of 1 give %v, pages of 2 give %v", a.GetAccountId(), one, two)
		}
		most = max(most, len(one))
	}
	return most, nil
}

func (k *kit) transactions() {
	l := func(ctx context.Context, account, token string, size int32) (page, error) {
		r, err := k.c.ListTransactions(ctx, &ledgerv1.ListTransactionsRequest{CustomerId: k.cust, AccountId: account, PageToken: token, PageSize: size})
		p := page{next: r.GetNextPageToken()}
		for _, t := range r.GetTransactions() {
			pre := "transaction " + t.GetTransactionId()
			p.ids = append(p.ids, t.GetTransactionId())
			p.errs = append(p.errs, firstErr(checkMoney(pre+": amount", t.GetAmount()), checkCreditDebit(pre+": credit_debit", t.GetCreditDebit()),
				nonEmpty(pre+": status", t.GetStatus()), checkTime(pre+": booked_at", t.GetBookedAt(), true),
				checkTime(pre+": value_at", t.GetValueAt(), false)))
			if t.GetBalanceAfter() != nil {
				p.errs = append(p.errs, checkMoney(pre+": balance_after", t.GetBalanceAfter()))
			}
		}
		return p, err
	}
	most, err := k.paged(l)
	if err == nil && most < 2 {
		err = errors.New("no account of the customer has two transactions, so no walk crosses a page")
	}
	k.check("list-transactions", err)
	ctx, cancel := k.call()
	_, err = k.c.ListTransactions(ctx, &ledgerv1.ListTransactionsRequest{CustomerId: k.cust, AccountId: k.foreign, PageSize: 1})
	cancel()
	k.check("list-transactions-not-owned", wantCode(err, codes.PermissionDenied))
}

func (k *kit) pagedResources() {
	req := func(account, token string, size int32) *ledgerv1.AccountPageRequest {
		return &ledgerv1.AccountPageRequest{CustomerId: k.cust, AccountId: account, PageToken: token, PageSize: size}
	}
	listers := map[string]lister{
		"beneficiaries": func(ctx context.Context, account, token string, size int32) (page, error) {
			r, err := k.c.ListBeneficiaries(ctx, req(account, token, size))
			p := page{next: r.GetNextPageToken()}
			for _, x := range r.GetItems() {
				p.ids = append(p.ids, x.GetBeneficiaryId())
			}
			return p, err
		},
		"standing-orders": func(ctx context.Context, account, token string, size int32) (page, error) {
			r, err := k.c.ListStandingOrders(ctx, req(account, token, size))
			p := page{next: r.GetNextPageToken()}
			for _, x := range r.GetItems() {
				pre := "standing order " + x.GetStandingOrderId()
				p.ids = append(p.ids, x.GetStandingOrderId())
				p.errs = append(p.errs, checkTime(pre+": next_at", x.GetNextAt(), false), checkTime(pre+": final_at", x.GetFinalAt(), false))
				if x.GetNextAmount() != nil {
					p.errs = append(p.errs, checkMoney(pre+": next_amount", x.GetNextAmount()))
				}
			}
			return p, err
		},
		"direct-debits": func(ctx context.Context, account, token string, size int32) (page, error) {
			r, err := k.c.ListDirectDebits(ctx, req(account, token, size))
			p := page{next: r.GetNextPageToken()}
			for _, x := range r.GetItems() {
				pre := "direct debit " + x.GetDirectDebitId()
				p.ids = append(p.ids, x.GetDirectDebitId())
				p.errs = append(p.errs, checkTime(pre+": previous_at", x.GetPreviousAt(), false))
				if x.GetPreviousAmount() != nil {
					p.errs = append(p.errs, checkMoney(pre+": previous_amount", x.GetPreviousAmount()))
				}
			}
			return p, err
		},
		"scheduled-payments": func(ctx context.Context, account, token string, size int32) (page, error) {
			r, err := k.c.ListScheduledPayments(ctx, req(account, token, size))
			p := page{next: r.GetNextPageToken()}
			for _, x := range r.GetItems() {
				pre := "scheduled payment " + x.GetScheduledPaymentId()
				p.ids = append(p.ids, x.GetScheduledPaymentId())
				p.errs = append(p.errs, checkMoney(pre+": amount", x.GetAmount()), checkTime(pre+": execution_at", x.GetExecutionAt(), true))
			}
			return p, err
		},
	}
	for _, res := range []string{"beneficiaries", "standing-orders", "direct-debits", "scheduled-payments"} {
		l := listers[res]
		if !slices.Contains(k.caps.GetResources(), res) {
			ctx, cancel := k.call()
			_, err := l(ctx, k.accs[0].GetAccountId(), "", 1)
			cancel()
			k.check("list-"+res+"-unimplemented", wantCode(err, codes.Unimplemented))
			continue
		}
		_, err := k.paged(l)
		k.check("list-"+res, err)
		ctx, cancel := k.call()
		_, err = l(ctx, k.foreign, "", 1)
		cancel()
		k.check("list-"+res+"-not-owned", wantCode(err, codes.PermissionDenied))
	}
}

func (k *kit) getParty(account string) (*ledgerv1.Party, error) {
	ctx, cancel := k.call()
	defer cancel()
	return k.c.GetParty(ctx, &ledgerv1.GetPartyRequest{CustomerId: k.cust, AccountId: account})
}

func (k *kit) party() {
	if !slices.Contains(k.caps.GetResources(), "party") {
		_, err := k.getParty("")
		k.check("get-party-unimplemented", wantCode(err, codes.Unimplemented))
		return
	}
	var err error
	for _, account := range append([]string{""}, accountIDs(k.accs)...) {
		p, perr := k.getParty(account)
		if perr == nil && p.GetPartyId() == "" {
			perr = errors.New("party_id is empty")
		}
		if perr != nil {
			err = fmt.Errorf("account %q: %w", account, perr)
			break
		}
	}
	k.check("get-party", err)
	_, err = k.getParty(k.foreign)
	k.check("get-party-not-owned", wantCode(err, codes.PermissionDenied))
}

func accountIDs(accs []*ledgerv1.Account) []string {
	var out []string
	for _, a := range accs {
		out = append(out, a.GetAccountId())
	}
	return out
}

// debtorCurrency is the currency of the fixture's debtor account.
func (k *kit) debtorCurrency() string {
	for _, a := range k.accs {
		if a.GetAccountId() == k.fx.DebtorAccountID {
			return a.GetCurrency()
		}
	}
	return ""
}

func (k *kit) checkFunds(account, amount string) (*ledgerv1.CheckFundsResponse, error) {
	ctx, cancel := k.call()
	defer cancel()
	return k.c.CheckFunds(ctx, &ledgerv1.CheckFundsRequest{CustomerId: k.cust, AccountId: account,
		Amount: &ledgerv1.Money{Amount: amount, Currency: k.debtorCurrency()}})
}

func (k *kit) funds() {
	r, err := k.checkFunds(k.fx.DebtorAccountID, k.fx.Amount)
	if err == nil {
		err = checkTime("at", r.GetAt(), true)
	}
	if err == nil && !r.GetAvailable() {
		err = fmt.Errorf("%s is not available on the debtor account", k.fx.Amount)
	}
	if err == nil {
		r, err = k.checkFunds(k.fx.DebtorAccountID, k.fx.RefusedAmount)
		if err == nil && r.GetAvailable() {
			err = fmt.Errorf("the refused amount %s is available on the debtor account", k.fx.RefusedAmount)
		}
	}
	k.check("check-funds", err)
	_, err = k.checkFunds(k.foreign, k.fx.Amount)
	k.check("check-funds-not-owned", wantCode(err, codes.PermissionDenied))
	_, err = k.checkFunds("fingate-conformance-unknown-"+k.run, k.fx.Amount)
	k.check("check-funds-unknown-account", wantCode(err, codes.NotFound))
}

// paymentRequest builds a payment of the given type from the debtor account.
func (k *kit) paymentRequest(typ, key, amount string) *ledgerv1.SubmitPaymentRequest {
	money := &ledgerv1.Money{Amount: amount, Currency: k.debtorCurrency()}
	r := &ledgerv1.SubmitPaymentRequest{
		IdempotencyKey: key, CustomerId: k.cust, DebtorAccountId: k.fx.DebtorAccountID, Type: typ, Amount: money,
		CreditorName: k.fx.CreditorName, CreditorSchemeName: k.fx.CreditorSchemeName, CreditorIdentification: k.fx.CreditorIdentification,
		Reference: "conformance " + k.run, EndToEndId: key,
	}
	tomorrow := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	switch typ {
	case "domestic-scheduled":
		r.RequestedExecutionAt = tomorrow.Format(time.RFC3339)
	case "domestic-standing-order":
		r.Recurrence = &ledgerv1.Recurrence{Frequency: "MNTH", FirstPaymentAt: tomorrow.Format(time.RFC3339), NumberOfPayments: 3,
			FirstAmount: &ledgerv1.Money{Amount: amount, Currency: money.GetCurrency()}}
	}
	return r
}

func (k *kit) submit(r *ledgerv1.SubmitPaymentRequest) (*ledgerv1.Payment, error) {
	ctx, cancel := k.call()
	defer cancel()
	return k.c.SubmitPayment(ctx, r)
}

func validPayment(p *ledgerv1.Payment) error {
	if p.GetPaymentId() == "" {
		return errors.New("payment_id is empty")
	}
	if !slices.Contains(paymentStatuses, p.GetStatus()) {
		return fmt.Errorf("payment %s: status %q is not one of %s", p.GetPaymentId(), p.GetStatus(), strings.Join(paymentStatuses, ", "))
	}
	return checkTime("payment "+p.GetPaymentId()+": status_at", p.GetStatusAt(), true)
}

func (k *kit) payments() {
	for _, typ := range PaymentTypes {
		if !slices.Contains(k.caps.GetPaymentTypes(), typ) {
			_, err := k.submit(k.paymentRequest(typ, k.key(), k.fx.Amount))
			k.check("submit-payment-"+typ+"-unimplemented", wantCode(err, codes.Unimplemented))
			continue
		}
		req := k.paymentRequest(typ, k.key(), k.fx.Amount)
		p, err := k.submit(req)
		if err == nil {
			err = validPayment(p)
		}
		k.check("submit-payment-"+typ, err)
		if err != nil {
			continue
		}
		again, err := k.submit(proto.Clone(req).(*ledgerv1.SubmitPaymentRequest))
		if err == nil && again.GetPaymentId() != p.GetPaymentId() {
			err = fmt.Errorf("the same key and content gave payment %s, then %s", p.GetPaymentId(), again.GetPaymentId())
		}
		k.check("idempotent-resubmission-"+typ, err)
		ctx, cancel := k.call()
		got, err := k.c.GetPayment(ctx, &ledgerv1.GetPaymentRequest{PaymentId: p.GetPaymentId()})
		cancel()
		if err == nil {
			err = validPayment(got)
		}
		if err == nil && got.GetPaymentId() != p.GetPaymentId() {
			err = fmt.Errorf("reading payment %s back returned payment %s", p.GetPaymentId(), got.GetPaymentId())
		}
		k.check("get-payment-"+typ, err)
		if typ == "domestic" {
			changed := proto.Clone(req).(*ledgerv1.SubmitPaymentRequest)
			changed.Reference += " changed"
			_, err = k.submit(changed)
			k.check("idempotency-key-conflict", wantCode(err, codes.AlreadyExists))
		}
	}
	if slices.Contains(k.caps.GetPaymentTypes(), "domestic") {
		_, err := k.submit(k.paymentRequest("domestic", k.key(), k.fx.RefusedAmount))
		if werr := wantCode(err, codes.FailedPrecondition); werr != nil {
			err = werr
		} else if status.Convert(err).Message() == "" {
			err = errors.New("the refusal carries no status message")
		} else {
			err = nil
		}
		k.check("submit-payment-refused", err)
		r := k.paymentRequest("domestic", k.key(), k.fx.Amount)
		r.DebtorAccountId = k.foreign
		_, err = k.submit(r)
		k.check("submit-payment-not-owned", wantCode(err, codes.PermissionDenied))
		r = k.paymentRequest("domestic", k.key(), k.fx.Amount)
		r.DebtorAccountId = "fingate-conformance-unknown-" + k.run
		_, err = k.submit(r)
		k.check("submit-payment-unknown-account", wantCode(err, codes.NotFound))
	}
	ctx, cancel := k.call()
	_, err := k.c.GetPayment(ctx, &ledgerv1.GetPaymentRequest{PaymentId: "fingate-conformance-unknown-" + k.run})
	cancel()
	k.check("get-payment-unknown", wantCode(err, codes.NotFound))
}
