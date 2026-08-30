package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/config"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// newAuthzHandler builds a handler with no database: every request below is
// rejected by the authorization layer before any query would run, so a nil
// store proves the decision does not depend on data.
func newAuthzHandler() http.Handler {
	return New(Deps{Config: config.Config{PublicURL: "http://localhost:8080", Profile: "sovereign"}})
}

func do(t *testing.T, h http.Handler, sess *store.Session, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if sess != nil {
		req = req.WithContext(withSession(req.Context(), *sess))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAuthorizationCustomerSeesOnlyOwnCustomer(t *testing.T) {
	h := newAuthzHandler()
	a, b := "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"
	viewer := &store.Session{Email: "v@a.example", Role: store.RoleCustomerViewer, CustomerID: &a}
	admin := &store.Session{Email: "adm@a.example", Role: store.RoleCustomerAdmin, CustomerID: &a}

	cases := []struct {
		name   string
		sess   *store.Session
		method string
		path   string
		want   int
	}{
		{"anonymous list customers", nil, "GET", "/api/v1/customers", 401},
		{"anonymous overview", nil, "GET", "/api/v1/overview", 401},
		{"anonymous me", nil, "GET", "/api/v1/auth/me", 401},
		{"viewer reads another customer", viewer, "GET", "/api/v1/customers/" + b, 404},
		{"viewer reads another customer's usage", viewer, "GET", "/api/v1/customers/" + b + "/usage", 404},
		{"viewer reads another customer's statements", viewer, "GET", "/api/v1/customers/" + b + "/statements", 404},
		{"viewer reads another customer's sources", viewer, "GET", "/api/v1/customers/" + b + "/sources", 404},
		{"viewer reads another customer's audit", viewer, "GET", "/api/v1/customers/" + b + "/audit", 404},
		{"viewer adds a source to own customer", viewer, "POST", "/api/v1/customers/" + a + "/sources", 403},
		{"viewer adds a user to own customer", viewer, "POST", "/api/v1/customers/" + a + "/users", 403},
		{"admin adds a source to another customer", admin, "POST", "/api/v1/customers/" + b + "/sources", 404},
		{"admin creates a customer", admin, "POST", "/api/v1/customers", 403},
		{"admin patches a customer", admin, "PATCH", "/api/v1/customers/" + a, 403},
		{"admin imports customers", admin, "POST", "/api/v1/customers/import", 403},
		{"admin invites", admin, "POST", "/api/v1/customers/" + a + "/invite", 403},
		{"admin runs statements", admin, "POST", "/api/v1/statements/run", 403},
		{"admin issues a statement", admin, "POST", "/api/v1/statements/x/issue", 403},
		{"admin creates a price book", admin, "POST", "/api/v1/pricebooks", 403},
		{"admin imports a price book", admin, "POST", "/api/v1/pricebooks/x/import", 403},
		{"admin reads overview", admin, "GET", "/api/v1/overview", 403},
		{"unknown api path", admin, "GET", "/api/v1/nope", 404},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(t, h, c.sess, c.method, c.path, "{}")
			if rec.Code != c.want {
				t.Fatalf("%s %s = %d (%s), want %d", c.method, c.path, rec.Code, rec.Body.String(), c.want)
			}
			if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("non-JSON error body: %s", rec.Header().Get("Content-Type"))
			}
		})
	}
}

func TestScopeAllows(t *testing.T) {
	if !store.OperatorScope.Allows("anything") {
		t.Fatal("operator scope denied")
	}
	s := store.CustomerScope("a")
	if !s.Allows("a") || s.Allows("b") || s.Allows("") {
		t.Fatal("customer scope wrong")
	}
	if (store.Scope{}).Allows("") {
		t.Fatal("empty scope allowed an empty id")
	}
	sess := store.Session{Role: store.RoleCustomerViewer}
	if sess.Scope().Allows("a") {
		t.Fatal("customer session without customer id allowed a row")
	}
}

// TestNoSecretFieldsInAPIShapes pins that the types serialized by the API
// carry no secret material: neither the credential view nor the source nor
// the activation result has a field that could hold the secret key.
func TestNoSecretFieldsInAPIShapes(t *testing.T) {
	shapes := []any{store.Credential{}, store.CostSource{}, activationResult{}, store.Customer{}, store.Session{}}
	for _, s := range shapes {
		tp := reflect.TypeOf(s)
		for i := 0; i < tp.NumField(); i++ {
			f := tp.Field(i)
			name := strings.ToLower(f.Name + " " + f.Tag.Get("json"))
			if strings.Contains(name, "secret") || strings.Contains(name, "password") {
				t.Errorf("%s exposes field %s", tp.Name(), f.Name)
			}
		}
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.ToLower(string(b)), "secret") {
			t.Errorf("%s serializes a secret-looking key: %s", tp.Name(), b)
		}
	}
}

func TestVerifyErrorMessageOmitsSecret(t *testing.T) {
	ve := &VerifyError{Code: "APIGW.0301", Message: "verify aksk signature fail", Unauthorized: true}
	if !strings.Contains(ve.Error(), "APIGW.0301") {
		t.Fatal("code missing from error")
	}
	if strings.Contains(ve.Error(), "TESTSECRET") {
		t.Fatal("secret in error")
	}
}
