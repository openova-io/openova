package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

func postJSON(t *testing.T, h http.Handler, path, body, email string) (int, string) {
	t.Helper()
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if email != "" {
		r.Header.Set("X-Forwarded-Email", email)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code, w.Body.String()
}

// #6859 — the scope column shipped with no way to write it, so the #6855 fix
// was inert and every source kept billing the whole project. This pins that a
// scope set at creation is persisted and read back.
func TestSourceScopeTokenIsPersisted(t *testing.T) {
	h, st := setupGateAPI(t, "X-Forwarded-Email")
	c, err := st.CreateCustomer(context.Background(), store.CustomerInput{
		Slug: "acme", Name: "Acme", AdminEmail: "a@acme.test", Kind: "organization", BillingMode: "showback",
	})
	if err != nil {
		t.Fatal(err)
	}
	code, body := postJSON(t, h, "/api/v1/customers/"+c.ID+"/sources",
		`{"kind":"huawei-project","region":"me-east-215","project_id":"p1","scope_token":"9a1f230f"}`, opEmail)
	if code != 201 && code != 200 {
		t.Fatalf("create source: %d %s", code, body)
	}
	if !strings.Contains(body, "9a1f230f") {
		t.Fatalf("scope_token not returned: %s", body)
	}

	srcs, err := st.ListSources(context.Background(), store.OperatorScope, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(srcs) != 1 || srcs[0].ScopeToken != "9a1f230f" {
		t.Fatalf("persisted scope = %+v, want 9a1f230f — the scoping filter never activates without it", srcs)
	}
}

// Clearing the scope must stay possible: billing the whole project is a
// legitimate configuration, and an operator has to be able to get back to it.
func TestSourceScopeTokenCanBeCleared(t *testing.T) {
	h, st := setupGateAPI(t, "X-Forwarded-Email")
	c, _ := st.CreateCustomer(context.Background(), store.CustomerInput{
		Slug: "acme2", Name: "Acme2", AdminEmail: "b@acme.test", Kind: "organization", BillingMode: "showback",
	})
	body := `{"kind":"huawei-project","region":"r","project_id":"p2","scope_token":"tok"}`
	if code, b := postJSON(t, h, "/api/v1/customers/"+c.ID+"/sources", body, opEmail); code >= 300 {
		t.Fatalf("create: %d %s", code, b)
	}
	if code, b := postJSON(t, h, "/api/v1/customers/"+c.ID+"/sources",
		`{"kind":"huawei-project","region":"r","project_id":"p2","scope_token":""}`, opEmail); code >= 300 {
		t.Fatalf("clear: %d %s", code, b)
	}
	srcs, _ := st.ListSources(context.Background(), store.OperatorScope, c.ID)
	if len(srcs) != 1 || srcs[0].ScopeToken != "" {
		t.Fatalf("scope = %q, want cleared", srcs[0].ScopeToken)
	}
}
