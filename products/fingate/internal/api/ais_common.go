package api

// What the account-information read operations share: the consent of the call, the
// permissions each operation needs (rule T9), the mapping of the ledger's errors to answers,
// pages and links, and the shape of the documents' objects.
//
// Rules of every read operation:
//
//   - The token check of the route has already proved the consent: it exists, belongs to the
//     client, is authorised and not expired, grants one of the operation's permissions, and holds
//     the account of the path. The operation reads the consent again for its customer, its
//     accounts and its permissions.
//   - A Detail permission shows the fields the standard reserves for it; Basic leaves them out.
//     Without ReadPAN an identification under the scheme UK.OBIE.PAN shows its last four
//     characters only.
//   - A value of the ledger is written as it is: the adapter writes the documents' codes. An
//     empty value is left out. A text longer than the document allows is cut to its limit. A
//     telephone number that does not have the document's form is left out.
//   - Errors of the ledger: a resource the ledger does not offer answers 404, the documents'
//     answer for a resource the bank does not implement, on every operation that declares 404;
//     an account the ledger does not hold for the customer, and a page the ledger refuses,
//     answer 400; anything else answers 500.
//   - A list answers one page of the ledger. Links.Next names the next page with the query
//     parameter page. An operation over every account of the consent reads the accounts in the
//     consent's order, one page of one account per answer.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	ledgerv1 "github.com/openova-io/openova/products/fingate/api/ledger/v1"
	"github.com/openova-io/openova/products/fingate/internal/gen/accountinfo"
	"github.com/openova-io/openova/products/fingate/internal/ledgerclient"
	"github.com/openova-io/openova/products/fingate/internal/store"
)

// answer is an error of an operation that is answered with its status; see internalError.
type answer struct {
	status  int
	message string
}

func (a *answer) Error() string { return strconv.Itoa(a.status) + " " + a.message }

func badRequest(format string, v ...any) error {
	return &answer{status: http.StatusBadRequest, message: fmt.Sprintf(format, v...)}
}

// notImplemented is the answer of an operation the bank does not implement.
var notImplemented = &answer{status: http.StatusNotFound}

// readPermissions maps each read operation, by its key, to the permissions of which the consent
// must grant one (rule T9). A Detail code grants its Basic code too.
var readPermissions = map[string][]string{}

func init() {
	add := func(perms []string, paths ...string) {
		for _, p := range paths {
			readPermissions["account-info GET "+p] = perms
		}
	}
	basicDetail := func(name string) []string { return []string{"Read" + name + "Basic", "Read" + name + "Detail"} }
	add(basicDetail("Accounts"), "/accounts", "/accounts/{AccountId}")
	add([]string{"ReadBalances"}, "/balances", "/accounts/{AccountId}/balances")
	add(basicDetail("Transactions"), "/transactions", "/accounts/{AccountId}/transactions", "/accounts/{AccountId}/statements/{StatementId}/transactions")
	add(basicDetail("Beneficiaries"), "/beneficiaries", "/accounts/{AccountId}/beneficiaries")
	add(basicDetail("StandingOrders"), "/standing-orders", "/accounts/{AccountId}/standing-orders")
	add([]string{"ReadDirectDebits"}, "/direct-debits", "/accounts/{AccountId}/direct-debits")
	add(basicDetail("ScheduledPayments"), "/scheduled-payments", "/accounts/{AccountId}/scheduled-payments")
	add([]string{"ReadParty"}, "/accounts/{AccountId}/party", "/accounts/{AccountId}/parties")
	add([]string{"ReadPartyPSU"}, "/party")
	add([]string{"ReadOffers"}, "/offers", "/accounts/{AccountId}/offers")
	add([]string{"ReadProducts"}, "/products", "/accounts/{AccountId}/product")
	add(basicDetail("Statements"), "/statements", "/accounts/{AccountId}/statements", "/accounts/{AccountId}/statements/{StatementId}")
	add([]string{"ReadStatementsDetail"}, "/accounts/{AccountId}/statements/{StatementId}/file")
}

// permissions implements the lookup of rule T9 that check calls (router.go).
func (a *accountInfo) permissions(key string) []string { return readPermissions[key] }

// grant is the consent of one call as the read operations use it.
type grant struct {
	customer string
	accounts []string
	perms    []string
	from, to string // the consent's transaction window, RFC 3339; "" is open
}

func (g grant) has(p string) bool { return slices.Contains(g.perms, p) }

// detail says whether the consent grants the Detail code of a resource.
func (g grant) detail(name string) bool { return g.has("Read" + name + "Detail") }

// grantOf reads the consent the token carries.
func (a *accountInfo) grantOf(ctx context.Context) (grant, error) {
	id := claimsOf(ctx).ConsentID
	c, ok, err := a.s.cfg.Store.Get(ctx, id)
	if err != nil {
		return grant{}, err
	}
	if !ok {
		return grant{}, fmt.Errorf("api: consent %q is gone after the token check", id)
	}
	return grantFrom(c)
}

func grantFrom(c store.Consent) (grant, error) {
	var req accountinfo.OBReadConsent1
	if err := json.Unmarshal(c.Request, &req); err != nil {
		return grant{}, fmt.Errorf("api: stored consent %s: %w", c.ID, err)
	}
	g := grant{customer: c.CustomerID, accounts: slices.Clone(c.Accounts)}
	for _, p := range req.Data.Permissions {
		g.perms = append(g.perms, string(p))
	}
	if t := req.Data.TransactionFromDateTime; t != nil {
		g.from = t.UTC().Format(timeLayout)
	}
	if t := req.Data.TransactionToDateTime; t != nil {
		g.to = t.UTC().Format(timeLayout)
	}
	return g, nil
}

// timeLayout is RFC 3339 with seconds, the form of every time of the port.
const timeLayout = "2006-01-02T15:04:05Z07:00"

// ledgerAnswer maps an error of the ledger. optional is true for an operation whose document
// declares 404.
func ledgerAnswer(err error, account string, optional bool) error {
	switch {
	case errors.Is(err, ledgerclient.ErrNotOffered) && optional:
		return notImplemented
	case (errors.Is(err, ledgerclient.ErrNotFound) || errors.Is(err, ledgerclient.ErrNotOwned)) && account == "":
		return badRequest("the customer of the consent is not known at the ledger")
	case errors.Is(err, ledgerclient.ErrNotFound), errors.Is(err, ledgerclient.ErrNotOwned):
		return badRequest("account %q is not held for the customer at the ledger", account)
	case errors.Is(err, ledgerclient.ErrBadRequest):
		return badRequest("the page is not one this server issued")
	}
	return err
}

// self is the address of the call, as Links.Self writes it.
func (a *accountInfo) self(ctx context.Context) string {
	u, _ := ctx.Value(urlKey).(url.URL)
	host := ""
	if d := a.s.cfg.Definitions.Current(); d != nil {
		host = d.Host
	}
	out := "https://" + host + u.EscapedPath()
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	return out
}

// pageParam is the value of the query parameter page of the call.
func pageParam(ctx context.Context) string {
	u, _ := ctx.Value(urlKey).(url.URL)
	return u.Query().Get("page")
}

// links returns the Links of an answer; next is the page parameter of the next page, or "".
func (a *accountInfo) links(ctx context.Context, next string) map[string]any {
	self := a.self(ctx)
	l := map[string]any{"Self": self}
	if next != "" {
		u, err := url.Parse(self)
		if err == nil {
			q := u.Query()
			q.Set("page", next)
			u.RawQuery = q.Encode()
			l["Next"] = u.String()
		}
	}
	return l
}

// listed is one item of a list and the account it belongs to.
type listed[T any] struct {
	account string
	item    T
}

// readList reads one page of a list: of one account, or, when account is "", of every account of
// the consent in turn. In that case the page parameter is "<index of the account>.<ledger token>".
// It returns the items and the page parameter of the next page.
func readList[T any](ctx context.Context, g grant, account string, read func(ctx context.Context, customer, account, page string) (ledgerclient.Page[T], error)) ([]listed[T], string, error) {
	page := pageParam(ctx)
	if account != "" {
		p, err := read(ctx, g.customer, account, page)
		if err != nil {
			return nil, "", ledgerAnswer(err, account, true)
		}
		out := make([]listed[T], 0, len(p.Items))
		for _, x := range p.Items {
			out = append(out, listed[T]{account, x})
		}
		return out, p.Next, nil
	}
	i, token := 0, ""
	if page != "" {
		idx, tok, ok := strings.Cut(page, ".")
		n, err := strconv.Atoi(idx)
		if !ok || err != nil || n < 0 || n >= len(g.accounts) || strconv.Itoa(n) != idx {
			return nil, "", badRequest("the page is not one this server issued")
		}
		i, token = n, tok
	}
	if len(g.accounts) == 0 {
		return nil, "", nil
	}
	for {
		acc := g.accounts[i]
		p, err := read(ctx, g.customer, acc, token)
		if err != nil {
			return nil, "", ledgerAnswer(err, acc, true)
		}
		out := make([]listed[T], 0, len(p.Items))
		for _, x := range p.Items {
			out = append(out, listed[T]{acc, x})
		}
		switch {
		case p.Next != "":
			return out, strconv.Itoa(i) + "." + p.Next, nil
		case i+1 == len(g.accounts):
			return out, "", nil
		case len(out) > 0:
			return out, strconv.Itoa(i+1) + ".", nil
		}
		i, token = i+1, "" // an account without items: read the next one in the same answer
	}
}

// fill turns an object built of maps and lists into the generated type of a document object.
func fill[T any](v any) (T, error) {
	var out T
	raw, err := json.Marshal(v)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("api: the answer does not fit the document: %w", err)
	}
	return out, nil
}

// put sets key to v when v is not empty.
func put(m map[string]any, key, v string) {
	if v != "" {
		m[key] = v
	}
}

// cut shortens s to at most n characters.
func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// money is an amount of the port in the documents' form, or nil.
func money(m *ledgerv1.Money) map[string]any {
	if m.GetAmount() == "" || m.GetCurrency() == "" {
		return nil
	}
	return map[string]any{"Amount": m.GetAmount(), "Currency": m.GetCurrency()}
}

// putMoney sets key to the amount when there is one.
func putMoney(o map[string]any, key string, m *ledgerv1.Money) {
	if v := money(m); v != nil {
		o[key] = v
	}
}

// cashAccount is an account identification in the documents' form, or nil when the scheme or the
// identification is empty. pan says whether the consent grants ReadPAN.
func cashAccount(scheme, identification, name string, pan bool) map[string]any {
	if scheme == "" || identification == "" {
		return nil
	}
	if scheme == "UK.OBIE.PAN" && !pan {
		identification = maskPAN(identification)
	}
	m := map[string]any{"SchemeName": scheme, "Identification": cut(identification, 256)}
	put(m, "Name", cut(name, 350))
	return m
}

// maskPAN leaves the last four characters of a card number and masks the others.
func maskPAN(s string) string {
	r := []rune(s)
	for i := 0; i < len(r)-4; i++ {
		r[i] = '*'
	}
	return string(r)
}

// phoneRE is the form of a telephone number in the documents.
var phoneRE = regexp.MustCompile(`^\+[0-9]{1,3}-[0-9()+\-]{1,30}$`)
