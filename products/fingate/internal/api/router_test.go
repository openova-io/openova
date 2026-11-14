package api

import (
	"net/http"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/fingate/internal/token"
)

// unrouted is the closed list of paths this server answers without the token check. It is
// empty: every path under the basePath is a resource path.
var unrouted = []string{}

// pathParam fills every {Name} of a document path.
var pathParam = regexp.MustCompile(`\{[A-Za-z]+\}`)

func concrete(op Operation) string {
	return pathParam.ReplaceAllString(strings.TrimPrefix(op.Pattern, op.Method+" "), "p-1")
}

// TestEveryRouteAuthenticated walks every route of the router. Each one refuses a call without
// a token with 401 and the challenge. Each route the server serves also refuses a verified
// token that carries no certificate binding (rule T6), which only the full token check on the
// route itself can do. Each route it does not serve answers notImplementedStatus once the
// token is verified, and never reaches an operation.
func TestEveryRouteAuthenticated(t *testing.T) {
	f := newFixture(t)
	if len(unrouted) != 0 {
		t.Fatalf("unrouted paths: %v", unrouted)
	}
	ops := f.srv.Operations()
	total := 0
	for _, d := range f.profile.Documents {
		if _, ok := families[d.API]; ok {
			total += d.Operations
		}
	}
	if len(ops) != total || total != 88 {
		t.Fatalf("the router has %d routes; the served documents have %d operations, want 88", len(ops), total)
	}
	served := 0
	for _, op := range ops {
		t.Run(op.Key, func(t *testing.T) {
			path := concrete(op)
			w := f.call(t, op.Method, path, "", nil)
			sameStatus(t, w, http.StatusUnauthorized)
			if got := w.Header().Get("WWW-Authenticate"); got != token.Challenge {
				t.Fatalf("WWW-Authenticate is %q", got)
			}
			w = f.call(t, op.Method, path, mint(t, *f.now, map[string]any{"cnf": nil}), nil)
			if op.Served {
				sameStatus(t, w, http.StatusUnauthorized)
				if msg := errorMessage(t, w); !strings.HasPrefix(msg, "rule T6:") {
					t.Fatalf("a served route did not apply rule T6: %q", msg)
				}
			} else {
				sameStatus(t, w, f.profile.NotImplementedStatus)
			}
		})
		if op.Served {
			served++
		}
	}
	if served == 0 {
		t.Fatal("no route is served")
	}
}

// TestAPathThatIsNoRouteNeedsATokenToo: the token is verified before routing.
func TestAPathThatIsNoRouteNeedsATokenToo(t *testing.T) {
	f := newFixture(t)
	for _, path := range []string{"/open-banking/v4.0/event-notifications", "/open-banking/v4.0/aisp/nothing", "/open-banking/v4.0"} {
		sameStatus(t, f.call(t, http.MethodPost, path, "", nil), http.StatusUnauthorized)
		if w := f.call(t, http.MethodPost, path, mint(t, *f.now, nil), nil); w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s with a token: status %d", path, w.Code)
		}
	}
}

func TestEveryOperationHasItsDocumentsSecurity(t *testing.T) {
	f := newFixture(t)
	grants := map[string]int{}
	for _, op := range f.srv.Operations() {
		if len(op.Scopes) == 0 || (op.Grant == token.GrantCustomer) != (op.Consent != "") {
			t.Errorf("%s: scopes %v, grant %d, consent family %q", op.Key, op.Scopes, op.Grant, op.Consent)
		}
		grants[op.API+map[token.Grant]string{token.GrantClient: " client", token.GrantCustomer: " customer"}[op.Grant]]++
	}
	want := map[string]int{"account-info client": 3, "account-info customer": 26, "payment-initiation client": 31,
		"payment-initiation customer": 10, "vrp client": 9, "confirmation-funds client": 3, "confirmation-funds customer": 1, "events client": 5}
	for k, v := range want {
		if grants[k] != v {
			t.Errorf("%s: %d operations, want %d", k, grants[k], v)
		}
	}
}

func TestUnimplementedListIsChecked(t *testing.T) {
	for _, bad := range []string{"account-info GET", "account-info  GET /accounts", "account-info GET /accounts\naccount-info GET /accounts", " account-info GET /accounts"} {
		if _, err := parseUnimplemented(bad + "\n"); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	got, err := parseUnimplemented(unimplementedList)
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if strings.Join(keys, "\n")+"\n" != unimplementedList {
		t.Fatal("unimplemented.txt is not sorted")
	}
}
