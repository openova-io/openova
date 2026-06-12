// sme_commerce_test.go — coverage for the Organizations commerce proxy
// transport (issue #3378 DoD 7/8). Locks: the commerceKinds allow-list,
// and AdminProxy's URL composition + method + body + bearer forwarding +
// verbatim status/body relay against an httptest upstream standing in for
// the SME commerce catalog's /catalog/admin/* surface.
package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCommerceKindsAllowList(t *testing.T) {
	for _, k := range []string{"plans", "addons", "bundles", "industries", "apps"} {
		if !commerceKinds[k] {
			t.Errorf("expected %q to be an allowed commerce kind", k)
		}
	}
	for _, k := range []string{"", "users", "../secrets", "Plans", "deployments"} {
		if commerceKinds[k] {
			t.Errorf("expected %q to be REJECTED, but it was allowed", k)
		}
	}
}

func TestAdminProxy_ForwardsMethodPathBodyAndBearer(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"new-1"}`))
	}))
	defer upstream.Close()

	c := &smeCatalogClient{baseURL: upstream.URL, http: upstream.Client()}

	status, body, ct, err := c.AdminProxy(
		context.Background(),
		http.MethodPost,
		"/plans",
		[]byte(`{"slug":"test-w","price_omr":9}`),
		"bridge-token",
	)
	if err != nil {
		t.Fatalf("AdminProxy: %v", err)
	}
	if status != http.StatusCreated {
		t.Errorf("status: got %d want 201", status)
	}
	if string(body) != `{"id":"new-1"}` {
		t.Errorf("body relayed verbatim: got %q", body)
	}
	if ct != "application/json" {
		t.Errorf("content-type relayed: got %q", ct)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("upstream method: got %q want POST", gotMethod)
	}
	if gotPath != "/catalog/admin/plans" {
		t.Errorf("upstream path: got %q want /catalog/admin/plans", gotPath)
	}
	if gotAuth != "Bearer bridge-token" {
		t.Errorf("bearer forwarded: got %q", gotAuth)
	}
	if gotBody != `{"slug":"test-w","price_omr":9}` {
		t.Errorf("body forwarded: got %q", gotBody)
	}
}

func TestAdminProxy_DeleteWithIDSubPathNoBody(t *testing.T) {
	var gotMethod, gotPath string
	hadBody := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		hadBody = len(b) > 0
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	c := &smeCatalogClient{baseURL: upstream.URL, http: upstream.Client()}
	status, _, _, err := c.AdminProxy(context.Background(), http.MethodDelete, "/plans/p-1", nil, "tok")
	if err != nil {
		t.Fatalf("AdminProxy delete: %v", err)
	}
	if status != http.StatusNoContent {
		t.Errorf("status: got %d want 204", status)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method: got %q want DELETE", gotMethod)
	}
	if gotPath != "/catalog/admin/plans/p-1" {
		t.Errorf("path: got %q want /catalog/admin/plans/p-1", gotPath)
	}
	if hadBody {
		t.Errorf("delete must not forward a body")
	}
}
