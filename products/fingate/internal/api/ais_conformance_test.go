package api

// Test C1, document conformance, for the 26 account-information read operations.
//
// For each one: a valid request, itself checked against the document, is answered and the
// answer validates against the document; a request without a token (401), with a token that
// lacks the operation's scope (403) and, where the operation reads the ledger, with a consent
// whose customer and account the ledger does not hold (400) each get the documented answer,
// and each answer validates against the document. The offer, product and statement operations
// answer 404 to the valid request.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/fingate/internal/consent"
	"github.com/openova-io/openova/products/fingate/internal/store"
)

// everyReadPermission is every permission of the account-access consent.
var everyReadPermission = []string{"ReadAccountsBasic", "ReadAccountsDetail", "ReadBalances", "ReadBeneficiariesBasic",
	"ReadBeneficiariesDetail", "ReadDirectDebits", "ReadOffers", "ReadPAN", "ReadParty", "ReadPartyPSU", "ReadProducts",
	"ReadScheduledPaymentsBasic", "ReadScheduledPaymentsDetail", "ReadStandingOrdersBasic", "ReadStandingOrdersDetail",
	"ReadStatementsBasic", "ReadStatementsDetail", "ReadTransactionsBasic", "ReadTransactionsCredits",
	"ReadTransactionsDebits", "ReadTransactionsDetail"}

// grantConsent stores an account-access consent of the test client with the permissions and
// extra fields of Data, authorised by customer for accounts, and returns a token that carries it.
func (f *fixture) grantConsent(t *testing.T, customer string, accounts []string, perms []string, data map[string]any) string {
	t.Helper()
	d := map[string]any{"Permissions": perms}
	for k, v := range data {
		d[k] = v
	}
	raw, err := json.Marshal(map[string]any{"Data": d, "Risk": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id := "aac-" + f.srv.cfg.NewID()
	if _, err := f.store.Create(ctx, store.Consent{ID: id, Family: consent.AccountAccess, ClientID: clientID, Request: raw}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Authorise(ctx, id, customer, accounts); err != nil {
		t.Fatal(err)
	}
	return mint(t, *f.now, map[string]any{"openbanking_intent_id": id})
}

// The read operations of the account-information document and the status of their valid request.
var readOps = []struct {
	path   string
	status int
}{
	{"/accounts", 200}, {"/accounts/{AccountId}", 200}, {"/accounts/{AccountId}/balances", 200}, {"/balances", 200},
	{"/accounts/{AccountId}/transactions", 200}, {"/transactions", 200},
	{"/accounts/{AccountId}/beneficiaries", 200}, {"/beneficiaries", 200},
	{"/accounts/{AccountId}/standing-orders", 200}, {"/standing-orders", 200},
	{"/accounts/{AccountId}/direct-debits", 200}, {"/direct-debits", 200},
	{"/accounts/{AccountId}/scheduled-payments", 200}, {"/scheduled-payments", 200},
	{"/accounts/{AccountId}/party", 200}, {"/accounts/{AccountId}/parties", 200}, {"/party", 200},
	{"/accounts/{AccountId}/offers", 404}, {"/offers", 404}, {"/accounts/{AccountId}/product", 404}, {"/products", 404},
	{"/accounts/{AccountId}/statements", 404}, {"/statements", 404}, {"/accounts/{AccountId}/statements/{StatementId}", 404},
	{"/accounts/{AccountId}/statements/{StatementId}/file", 404}, {"/accounts/{AccountId}/statements/{StatementId}/transactions", 404},
}

func TestReadOperationsConformToTheDocument(t *testing.T) {
	f := newFixture(t)
	doc := loadDocument(t, "account-info-openapi.yaml")
	base := "/open-banking/v4.0/aisp"
	good := f.grantConsent(t, "cust-0001", []string{"acc-0001-01", "acc-0001-02"}, everyReadPermission, nil)
	gone := f.grantConsent(t, "cust-gone", []string{"acc-gone"}, everyReadPermission, nil)
	noScope := mint(t, *f.now, map[string]any{"scope": "openid"})
	if len(readOps) != 26 {
		t.Fatalf("%d read operations, want 26", len(readOps))
	}
	for _, op := range readOps {
		t.Run(op.path, func(t *testing.T) {
			concreteFor := func(account string) (string, map[string]string) {
				params := map[string]string{}
				p := op.path
				if strings.Contains(p, "{AccountId}") {
					params["AccountId"] = account
					p = strings.ReplaceAll(p, "{AccountId}", account)
				}
				if strings.Contains(p, "{StatementId}") {
					params["StatementId"] = "st-1"
					p = strings.ReplaceAll(p, "{StatementId}", "st-1")
				}
				return base + p, params
			}
			p, params := concreteFor("acc-0001-01")
			w := doc.exchange(f, "GET", op.path, params, f.request(t, "GET", p, good, ""), true)
			sameStatus(t, w, op.status)
			if op.status == 200 && !strings.Contains(w.Body.String(), `"Self":"https://`+host+p+`"`) {
				t.Fatalf("Links.Self is not the call: %s", w.Body.String())
			}
			sameStatus(t, doc.exchange(f, "GET", op.path, params, f.request(t, "GET", p, "", ""), false), http.StatusUnauthorized)
			sameStatus(t, doc.exchange(f, "GET", op.path, params, f.request(t, "GET", p, noScope, ""), false), http.StatusForbidden)
			if op.status == 200 {
				gp, gparams := concreteFor("acc-gone")
				w := doc.exchange(f, "GET", op.path, gparams, f.request(t, "GET", gp, gone, ""), false)
				sameStatus(t, w, http.StatusBadRequest)
				if msg := errorMessage(t, w); !strings.Contains(msg, "at the ledger") {
					t.Fatalf("400: %q", msg)
				}
			}
		})
	}
	// Every account-information operation a customer consents to is one of readOps.
	n := 0
	for _, op := range f.srv.Operations() {
		if op.API != "account-info" || op.Consent == "" {
			continue
		}
		n++
		found := false
		for _, r := range readOps {
			found = found || r.path == op.Path
		}
		if !found || !op.Served || len(readPermissions[op.Key]) == 0 {
			t.Errorf("%s: in the test %v, served %v, permissions %v", op.Key, found, op.Served, readPermissions[op.Key])
		}
	}
	if n != 26 || len(readPermissions) != 26 {
		t.Fatalf("%d customer operations and %d permission entries, want 26 each", n, len(readPermissions))
	}
}
