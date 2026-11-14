package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	ledgerv1 "github.com/openova-io/openova/products/fingate/api/ledger/v1"
	"github.com/openova-io/openova/products/fingate/internal/ledgertest"
	"github.com/openova-io/openova/products/fingate/internal/sandbox"
)

const aisp = "/open-banking/v4.0/aisp"

// both are alice's two accounts in the sandbox.
var both = []string{"acc-0001-01", "acc-0001-02"}

// body decodes an answer; it fails the test unless the status is want.
func body(t *testing.T, w *httptest.ResponseRecorder, want int) map[string]any {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status %d, want %d: %s", w.Code, want, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("not JSON: %s", w.Body.String())
	}
	return out
}

func (f *fixture) get(t *testing.T, path, jws string, want int) map[string]any {
	t.Helper()
	return body(t, f.call(t, http.MethodGet, path, jws, nil), want)
}

// items returns Data.<key> of an answer.
func items(t *testing.T, out map[string]any, key string) []map[string]any {
	t.Helper()
	list, _ := out["Data"].(map[string]any)[key].([]any)
	res := make([]map[string]any, 0, len(list))
	for _, x := range list {
		res = append(res, x.(map[string]any))
	}
	return res
}

func ids(list []map[string]any, key string) string {
	var out []string
	for _, x := range list {
		out = append(out, x[key].(string))
	}
	return strings.Join(out, ",")
}

func TestBasicHidesWhatDetailShows(t *testing.T) {
	f := newFixture(t)
	basic := f.grantConsent(t, "cust-0001", both, []string{"ReadAccountsBasic", "ReadBeneficiariesBasic", "ReadTransactionsBasic", "ReadTransactionsCredits", "ReadTransactionsDebits"}, nil)
	detail := f.grantConsent(t, "cust-0001", both, []string{"ReadAccountsDetail", "ReadBeneficiariesDetail", "ReadTransactionsDetail", "ReadTransactionsCredits", "ReadTransactionsDebits"}, nil)
	for _, c := range []struct {
		path, list, field string
	}{
		{"/accounts", "Account", "Account"},
		{"/accounts/acc-0001-01/beneficiaries", "Beneficiary", "CreditorAccount"},
		{"/accounts/acc-0001-01/transactions", "Transaction", "TransactionInformation"},
	} {
		for jws, want := range map[string]bool{basic: false, detail: true} {
			list := items(t, f.get(t, aisp+c.path, jws, 200), c.list)
			if len(list) == 0 {
				t.Fatalf("%s: empty", c.path)
			}
			for _, x := range list {
				if _, ok := x[c.field]; ok != want {
					t.Errorf("%s: %s present %v, want %v", c.path, c.field, ok, want)
				}
			}
		}
	}
}

func TestOnlyTheConsentsAccountsAreRead(t *testing.T) {
	f := newFixture(t)
	jws := f.grantConsent(t, "cust-0001", []string{"acc-0001-02"}, []string{"ReadAccountsBasic", "ReadBalances"}, nil)
	if got := ids(items(t, f.get(t, aisp+"/accounts", jws, 200), "Account"), "AccountId"); got != "acc-0001-02" {
		t.Fatalf("accounts: %s", got)
	}
	if got := ids(items(t, f.get(t, aisp+"/balances", jws, 200), "Balance"), "AccountId"); got != "acc-0001-02,acc-0001-02" {
		t.Fatalf("balances: %s", got)
	}
	// An account outside the consent, and a permission the consent does not grant, are refused by rule T9.
	for _, p := range []string{"/accounts/acc-0001-01", "/accounts/acc-0001-02/transactions"} {
		w := f.call(t, http.MethodGet, aisp+p, jws, nil)
		sameStatus(t, w, http.StatusForbidden)
		if msg := errorMessage(t, w); !strings.HasPrefix(msg, "rule T9:") {
			t.Fatalf("%s: %q", p, msg)
		}
	}
}

func TestCreditsDebitsAndTheWindows(t *testing.T) {
	f := newFixture(t)
	credits := f.grantConsent(t, "cust-0001", both, []string{"ReadTransactionsBasic", "ReadTransactionsCredits"}, nil)
	if got := ids(items(t, f.get(t, aisp+"/accounts/acc-0001-01/transactions", credits, 200), "Transaction"), "TransactionId"); got != "tx-0001-01-1" {
		t.Fatalf("credits only: %s", got)
	}
	neither := f.grantConsent(t, "cust-0001", both, []string{"ReadTransactionsBasic"}, nil)
	if got := items(t, f.get(t, aisp+"/accounts/acc-0001-01/transactions", neither, 200), "Transaction"); len(got) != 0 {
		t.Fatalf("neither credits nor debits: %v", got)
	}
	all := []string{"ReadTransactionsBasic", "ReadTransactionsCredits", "ReadTransactionsDebits"}
	windowed := f.grantConsent(t, "cust-0001", both, all, map[string]any{"TransactionFromDateTime": "2026-01-04T00:00:00Z", "TransactionToDateTime": "2026-01-13T00:00:00Z"})
	if got := ids(items(t, f.get(t, aisp+"/accounts/acc-0001-01/transactions", windowed, 200), "Transaction"), "TransactionId"); got != "tx-0001-01-2,tx-0001-01-3,tx-0001-01-4" {
		t.Fatalf("the consent's window: %s", got)
	}
	q := "?fromBookingDateTime=" + url.QueryEscape("2026-01-06T00:00:00+00:00") + "&toBookingDateTime=" + url.QueryEscape("2026-01-20T00:00:00+00:00")
	if got := ids(items(t, f.get(t, aisp+"/accounts/acc-0001-01/transactions"+q, windowed, 200), "Transaction"), "TransactionId"); got != "tx-0001-01-3,tx-0001-01-4" {
		t.Fatalf("both windows: %s", got)
	}
	q = "?fromBookingDateTime=" + url.QueryEscape("2026-02-01T00:00:00+00:00")
	if got := items(t, f.get(t, aisp+"/transactions"+q, windowed, 200), "Transaction"); len(got) != 0 {
		t.Fatalf("an empty window: %v", got)
	}
}

// onePerPage is the sandbox with pages of one item.
type onePerPage struct{ ledgerv1.LedgerServer }

func (o onePerPage) ListTransactions(ctx context.Context, r *ledgerv1.ListTransactionsRequest) (*ledgerv1.ListTransactionsResponse, error) {
	r.PageSize = 1
	return o.LedgerServer.ListTransactions(ctx, r)
}

func (o onePerPage) ListBeneficiaries(ctx context.Context, r *ledgerv1.AccountPageRequest) (*ledgerv1.ListBeneficiariesResponse, error) {
	r.PageSize = 1
	return o.LedgerServer.ListBeneficiaries(ctx, r)
}

// walk follows Links.Next from path and returns the identifiers of every item, in order.
func (f *fixture) walk(t *testing.T, path, jws, list, key string) string {
	t.Helper()
	doc := loadDocument(t, "account-info-openapi.yaml")
	docPath := strings.Replace(strings.TrimPrefix(path, aisp), "acc-0001-01", "{AccountId}", 1)
	params := map[string]string{}
	if strings.Contains(docPath, "{AccountId}") {
		params["AccountId"] = "acc-0001-01"
	}
	var all []string
	next := "https://" + host + path
	for i := 0; next != ""; i++ {
		if i > 20 {
			t.Fatal("the pages do not end")
		}
		u, err := url.Parse(next)
		if err != nil {
			t.Fatal(err)
		}
		w := doc.exchange(f, "GET", docPath, params, f.request(t, "GET", u.RequestURI(), jws, ""), false)
		out := body(t, w, 200)
		if s := ids(items(t, out, list), key); s != "" {
			all = append(all, s)
		}
		next, _ = out["Links"].(map[string]any)["Next"].(string)
	}
	return strings.Join(all, ",")
}

func TestPagesFollowTheLedger(t *testing.T) {
	f := newFixtureOn(t, onePerPage{sandbox.New()})
	jws := f.grantConsent(t, "cust-0001", both, everyReadPermission, nil)
	if got := f.walk(t, aisp+"/accounts/acc-0001-01/transactions", jws, "Transaction", "TransactionId"); got != "tx-0001-01-1,tx-0001-01-2,tx-0001-01-3,tx-0001-01-4,tx-0001-01-5" {
		t.Fatalf("one account: %s", got)
	}
	if got := f.walk(t, aisp+"/transactions", jws, "Transaction", "TransactionId"); got != "tx-0001-01-1,tx-0001-01-2,tx-0001-01-3,tx-0001-01-4,tx-0001-01-5,tx-0001-02-1,tx-0001-02-2" {
		t.Fatalf("every account: %s", got)
	}
	if got := f.walk(t, aisp+"/beneficiaries", jws, "Beneficiary", "BeneficiaryId"); got != "ben-0001-01-1,ben-0001-01-2" {
		t.Fatalf("beneficiaries of every account: %s", got)
	}
	for _, page := range []string{"x", "2.", "-1.", "01.", "0.not-a-token"} {
		w := f.call(t, http.MethodGet, aisp+"/transactions?page="+url.QueryEscape(page), jws, nil)
		sameStatus(t, w, http.StatusBadRequest)
		if msg := errorMessage(t, w); msg != "the page is not one this server issued" {
			t.Fatalf("page %q: %q", page, msg)
		}
	}
}

func TestAResourceTheLedgerDoesNotOfferIsNotFound(t *testing.T) {
	f := newFixtureOn(t, ledgertest.NewMemory(&ledgerv1.Capabilities{Currencies: []string{"GBP"}}))
	jws := f.grantConsent(t, "cust-alice", []string{"acc-1001"}, everyReadPermission, nil)
	for _, p := range []string{"/accounts/acc-1001/beneficiaries", "/beneficiaries", "/accounts/acc-1001/standing-orders", "/standing-orders",
		"/accounts/acc-1001/direct-debits", "/direct-debits", "/accounts/acc-1001/scheduled-payments", "/scheduled-payments",
		"/accounts/acc-1001/party", "/accounts/acc-1001/parties", "/party"} {
		w := f.call(t, http.MethodGet, aisp+p, jws, nil)
		sameStatus(t, w, http.StatusNotFound)
		if w.Body.Len() != 0 {
			t.Fatalf("%s: the 404 has a body: %q", p, w.Body.String())
		}
	}
}

// down is the sandbox with a ledger that cannot answer reads of accounts and balances, and that
// does not offer transactions, which the port always offers.
type down struct{ ledgerv1.LedgerServer }

func (down) ListAccounts(context.Context, *ledgerv1.ListAccountsRequest) (*ledgerv1.ListAccountsResponse, error) {
	return nil, status.Error(codes.Unavailable, "down")
}

func (down) GetAccount(context.Context, *ledgerv1.GetAccountRequest) (*ledgerv1.Account, error) {
	return nil, status.Error(codes.Unavailable, "down")
}

func (down) ListBalances(context.Context, *ledgerv1.AccountRequest) (*ledgerv1.ListBalancesResponse, error) {
	return nil, status.Error(codes.Unavailable, "down")
}

func (down) ListTransactions(context.Context, *ledgerv1.ListTransactionsRequest) (*ledgerv1.ListTransactionsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "no transactions")
}

func TestALedgerThatCannotAnswerIsAServerError(t *testing.T) {
	f := newFixtureOn(t, down{sandbox.New()})
	doc := loadDocument(t, "account-info-openapi.yaml")
	jws := f.grantConsent(t, "cust-0001", both, everyReadPermission, nil)
	params := map[string]string{"AccountId": "acc-0001-01"}
	for docPath, p := range map[string]string{"/accounts": "/accounts", "/accounts/{AccountId}": "/accounts/acc-0001-01",
		"/accounts/{AccountId}/balances": "/accounts/acc-0001-01/balances", "/balances": "/balances",
		"/accounts/{AccountId}/transactions": "/accounts/acc-0001-01/transactions"} {
		w := doc.exchange(f, "GET", docPath, params, f.request(t, "GET", aisp+p, jws, ""), true)
		sameStatus(t, w, http.StatusInternalServerError)
		if msg := errorMessage(t, w); msg != "the answer could not be written" {
			t.Fatalf("%s: %q", p, msg)
		}
	}
}

func TestMaskingAndForms(t *testing.T) {
	if got := cashAccount("UK.OBIE.PAN", "5555444433331111", "", false)["Identification"]; got != "************1111" {
		t.Fatalf("masked PAN: %v", got)
	}
	if got := cashAccount("UK.OBIE.PAN", "5555444433331111", "", true)["Identification"]; got != "5555444433331111" {
		t.Fatalf("PAN with ReadPAN: %v", got)
	}
	if got := cashAccount("UK.OBIE.SortCodeAccountNumber", "00000110000001", "", false)["Identification"]; got != "00000110000001" {
		t.Fatalf("a sort code is not masked: %v", got)
	}
	if cashAccount("", "x", "", true) != nil || cashAccount("UK.OBIE.IBAN", "", "", true) != nil {
		t.Fatal("an account without scheme or identification is shown")
	}
	if _, ok := partyObject(&ledgerv1.Party{PartyId: "p", Phone: "+440000000001"})["Phone"]; ok {
		t.Fatal("a telephone number without the documents' form is shown")
	}
	if got := partyObject(&ledgerv1.Party{PartyId: "p", Phone: "+44-7700900123"})["Phone"]; got != "+44-7700900123" {
		t.Fatalf("a telephone number in the documents' form: %v", got)
	}
	if got := cut("ééé", 2); got != "éé" {
		t.Fatalf("cut: %q", got)
	}
	if !slices.Equal(readPermissions["account-info GET /accounts"], []string{"ReadAccountsBasic", "ReadAccountsDetail"}) {
		t.Fatal("the accounts need ReadAccountsBasic or ReadAccountsDetail")
	}
}
