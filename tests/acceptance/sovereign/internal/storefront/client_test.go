package storefront

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// shop answers as the gateway of marketplace.sov.test does. The bearer token
// "old" has expired; POST /auth/refresh with "r1" renews it as "new".
func shop(t *testing.T, seen *[]string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = append(*seen, r.Method+" "+r.URL.RequestURI()+" auth="+r.Header.Get("Authorization")+" cookie="+r.Header.Get("Cookie"))
		b, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/api/auth/verify":
			_, _ = io.WriteString(w, `{"token":"old","refresh_token":"r1","user":{"email":"a@b.test","role":"member"}}`)
		case "/api/auth/refresh":
			if string(b) != `{"refresh_token":"r1"}` || r.Header.Get("Authorization") != "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `{"token":"new","refresh_token":"r2"}`)
		case "/api/auth/me":
			if r.Header.Get("Authorization") != "Bearer new" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `{"user":{"email":"a@b.test","role":"member"}}`)
		case "/api/billing/checkout":
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = io.WriteString(w, `{"error":"insufficient credit"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	c := New("sov.test", nil)
	c.Base = srv.URL + "/api"
	return c
}

func TestClientSignsInAndRenewsTheToken(t *testing.T) {
	var seen []string
	c := shop(t, &seen)
	ctx := context.Background()
	if _, err := c.Verify(ctx, "a@b.test", "123456"); err != nil {
		t.Fatal(err)
	}
	me, err := c.Me(ctx)
	if err != nil || me.Email != "a@b.test" || c.Token != "new" || c.Refresh != "r2" {
		t.Fatalf("me %+v err %v token %q refresh %q", me, err, c.Token, c.Refresh)
	}
	want := []string{
		"POST /api/auth/verify auth= cookie=",
		"GET /api/auth/me auth=Bearer old cookie=",
		"POST /api/auth/refresh auth= cookie=",
		"GET /api/auth/me auth=Bearer new cookie=",
	}
	if strings.Join(seen, "\n") != strings.Join(want, "\n") {
		t.Errorf("requests:\n%s\nwant:\n%s", strings.Join(seen, "\n"), strings.Join(want, "\n"))
	}
	creds := []string{}
	for _, call := range c.Calls {
		creds = append(creds, call.Path+" "+call.Credential)
	}
	if got := strings.Join(creds, ","); got != "/auth/verify none,/auth/me customer,/auth/refresh none,/auth/me customer" {
		t.Errorf("calls: %s", got)
	}
}

func TestClientErrorCarriesTheStatus(t *testing.T) {
	var seen []string
	c := shop(t, &seen)
	_, err := c.Checkout(context.Background(), CheckoutRequest{PlanID: "p", TenantID: "t"})
	var e *Error
	if !errors.As(err, &e) || e.Status != http.StatusPaymentRequired || !strings.Contains(e.Error(), "insufficient credit") {
		t.Errorf("want an *Error with HTTP 402, got %v", err)
	}
	if err := c.DeleteOrg(context.Background(), "t-1"); !errors.As(err, &e) || e.Status != http.StatusNotFound {
		t.Errorf("want an *Error with HTTP 404, got %v", err)
	}
}
