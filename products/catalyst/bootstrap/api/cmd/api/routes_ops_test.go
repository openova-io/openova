package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/opsproxy"
)

// TestOpsRoutesForwardThroughTheSessionGroup: every console route of the
// operations records reaches ops-records through the session group with the
// identity the proxy sets, and an agent token reaches it on none of them.
func TestOpsRoutesForwardThroughTheSessionGroup(t *testing.T) {
	var mu sync.Mutex
	var got []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Method+" "+r.URL.Path+" "+r.Header.Get(opsproxy.HeaderOrganization)+"|"+r.Header.Get(opsproxy.HeaderTier))
		mu.Unlock()
	}))
	defer up.Close()
	t.Setenv(opsproxy.URLEnv, up.URL)
	router := buildRouter(goldenDeps())

	send := func(c *auth.Claims, route opsproxy.Route) int {
		path := strings.ReplaceAll(route.Path, "{id}", "i-1")
		r := httptest.NewRequest(route.Method, path, nil)
		r.Header.Set(opsproxy.HeaderOrganization, "other")
		r = r.WithContext(context.WithValue(r.Context(), auth.ClaimsKey, c))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w.Code
	}
	agent := &auth.Claims{Email: "a@acme.test", Org: "acme", Tier: "developer", Typ: "agent", Audience: []string{"catalyst-api"}}
	var want []string
	for _, route := range opsproxy.Routes {
		path := strings.ReplaceAll(route.Path, "{id}", "i-1")
		if code := send(&auth.Claims{Email: "op@sov.test", Tier: "owner"}, route); code != http.StatusOK {
			t.Errorf("sovereign-admin %s %s: status %d", route.Method, route.Path, code)
		}
		if code := send(&auth.Claims{Email: "dev@acme.test", Org: "acme", Tier: "developer"}, route); code != http.StatusOK {
			t.Errorf("Organization session %s %s: status %d", route.Method, route.Path, code)
		}
		if code := send(agent, route); code != http.StatusForbidden {
			t.Errorf("agent token %s %s: status %d, want 403", route.Method, route.Path, code)
		}
		want = append(want, route.Method+" "+path+" |sovereign-admin", route.Method+" "+path+" acme|developer")
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("ops-records received:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
