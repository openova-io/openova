package opsproxy

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// seen is what the upstream received.
type seen struct {
	method, path, query, body string
	header                    http.Header
}

// upstream records every request and answers 200; calls returns what it received.
func upstream(t *testing.T) (srv *httptest.Server, calls func() []seen) {
	t.Helper()
	var mu sync.Mutex
	var got []seen
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, seen{r.Method, r.URL.Path, r.URL.RawQuery, string(body), r.Header.Clone()})
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []seen { mu.Lock(); defer mu.Unlock(); return append([]seen(nil), got...) }
}

// call serves one request with claims in its context, as the session group leaves it.
func call(p *Proxy, c *auth.Claims, method, target, body string, header map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	for k, v := range header {
		r.Header.Set(k, v)
	}
	if c != nil {
		r = r.WithContext(context.WithValue(r.Context(), auth.ClaimsKey, c))
	}
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	return w
}

func identity(s seen) string {
	return s.header.Get(HeaderOrganization) + "|" + s.header.Get(HeaderUser) + "|" + s.header.Get(HeaderTier)
}

// TestCallerIdentityHeadersAreReplaced: the caller's identity headers and credentials never reach ops-records.
func TestCallerIdentityHeadersAreReplaced(t *testing.T) {
	srv, got := upstream(t)
	c := &auth.Claims{Email: "dev@acme.test", Org: "Acme", Tier: "developer"}
	w := call(New(srv.URL, quiet), c, http.MethodPost, "/api/v1/ops/incidents/i-1/actions?state=open&access_token=a.b.c", `{"action":"acknowledge"}`, map[string]string{
		"x-openova-organization": "other", "x-openova-user": "mallory@other.test", "x-openova-tier": "sovereign-admin",
		"X-Openova-Extra": "1", "Cookie": "catalyst_session=x", "Authorization": "Bearer a.b.c",
	})
	if w.Code != http.StatusOK || len(got()) != 1 {
		t.Fatalf("status %d, upstream calls %d", w.Code, len(got()))
	}
	s := got()[0]
	if identity(s) != "acme|dev@acme.test|developer" {
		t.Errorf("identity headers %q", identity(s))
	}
	for _, h := range []string{"X-Openova-Extra", "Cookie", "Authorization"} {
		if s.header.Get(h) != "" {
			t.Errorf("header %s reached ops-records", h)
		}
	}
	if s.method != http.MethodPost || s.path != "/api/v1/ops/incidents/i-1/actions" || s.query != "state=open" || s.body != `{"action":"acknowledge"}` {
		t.Errorf("forwarded %s %s ? %s body %q", s.method, s.path, s.query, s.body)
	}
}

// TestOrganizationSessionGetsItsOrganization: a session that names an Organization is confined to it.
func TestOrganizationSessionGetsItsOrganization(t *testing.T) {
	for _, tc := range []struct{ tier, want string }{
		{"viewer", "acme|u@acme.test|viewer"}, {"owner", "acme|u@acme.test|owner"},
		{auth.OrgScopedTier, "acme|u@acme.test|admin"}, {"", "acme|u@acme.test|viewer"}, {"nonsense", "acme|u@acme.test|viewer"},
	} {
		srv, got := upstream(t)
		w := call(New(srv.URL, quiet), &auth.Claims{Email: "u@acme.test", Org: "acme", Tier: tc.tier}, http.MethodGet, "/api/v1/ops/incidents", "", nil)
		if w.Code != http.StatusOK || len(got()) != 1 || identity(got()[0]) != tc.want {
			t.Errorf("tier %q: status %d, upstream %v, want %s", tc.tier, w.Code, got(), tc.want)
		}
	}
}

// TestSovereignAdminSeesEveryOrganization: a sovereign-admin without an Organization reads every Organization.
func TestSovereignAdminSeesEveryOrganization(t *testing.T) {
	for _, c := range []*auth.Claims{
		{Email: "op@sov.test", Tier: "owner"},
		{Email: "op@sov.test", RealmAccess: auth.RealmAccess{Roles: []string{"catalyst-admin"}}},
	} {
		srv, got := upstream(t)
		w := call(New(srv.URL, quiet), c, http.MethodGet, "/api/v1/ops/judgements", "", nil)
		if w.Code != http.StatusOK || len(got()) != 1 || identity(got()[0]) != "|op@sov.test|sovereign-admin" {
			t.Errorf("%+v: status %d, upstream %v", c, w.Code, got())
			continue
		}
		if _, present := got()[0].header[http.CanonicalHeaderKey(HeaderOrganization)]; !present {
			t.Errorf("%+v: the Organization header is not sent", c)
		}
	}
}

// TestAgentTokenIsRefused: an agent token gets 403 on every method, and nothing is forwarded.
func TestAgentTokenIsRefused(t *testing.T) {
	srv, got := upstream(t)
	agent := &auth.Claims{Email: "agent@acme.test", Org: "acme", Tier: "developer", Typ: "agent", Audience: []string{"catalyst-api"}}
	for _, m := range []string{http.MethodGet, http.MethodPost} {
		w := call(New(srv.URL, quiet), agent, m, "/api/v1/ops/incidents", "", nil)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "agent-token-forbidden") {
			t.Errorf("%s: status %d body %s", m, w.Code, w.Body.String())
		}
	}
	if len(got()) != 0 {
		t.Errorf("an agent token reached ops-records %d times", len(got()))
	}
}

// TestRefusedSessions: no session, no e-mail, or no Organization without being a sovereign-admin.
func TestRefusedSessions(t *testing.T) {
	srv, got := upstream(t)
	for _, tc := range []struct {
		c    *auth.Claims
		code int
		err  string
	}{
		{nil, http.StatusUnauthorized, "unauthenticated"},
		{&auth.Claims{Org: "acme", Tier: "owner"}, http.StatusUnauthorized, "unauthenticated"},
		{&auth.Claims{Email: "u@x.test", Tier: "viewer"}, http.StatusForbidden, "no-organization"},
		{&auth.Claims{Email: "u@x.test", Tier: auth.OrgScopedTier, RealmAccess: auth.RealmAccess{Roles: []string{"catalyst-admin"}}}, http.StatusForbidden, "no-organization"},
	} {
		w := call(New(srv.URL, quiet), tc.c, http.MethodGet, "/api/v1/ops/incidents", "", nil)
		if w.Code != tc.code || !strings.Contains(w.Body.String(), tc.err) {
			t.Errorf("%+v: status %d body %s, want %d %s", tc.c, w.Code, w.Body.String(), tc.code, tc.err)
		}
	}
	if len(got()) != 0 {
		t.Errorf("a refused session reached ops-records %d times", len(got()))
	}
}

// TestStreamIsFlushedAsItArrives: an event reaches the browser while ops-records keeps the stream open.
func TestStreamIsFlushedAsItArrives(t *testing.T) {
	release := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: one\n\n")
		w.(http.Flusher).Flush()
		<-release
	}))
	defer up.Close()
	p := New(up.URL, quiet)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := &auth.Claims{Email: "op@sov.test", Tier: "owner"}
		p.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), auth.ClaimsKey, c)))
	}))
	defer front.Close()
	res, err := http.Get(front.URL + "/api/v1/ops/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	defer close(release) // runs first: ops-records ends the stream, then each server closes
	line := make(chan string, 1)
	go func() {
		l, _ := bufio.NewReader(res.Body).ReadString('\n')
		line <- l
	}()
	select {
	case l := <-line:
		if l != "data: one\n" {
			t.Errorf("first line %q", l)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the event was held back while the stream stayed open")
	}
}

// TestUpstreamFailures: an unreachable ops-records is 502, an unreadable address 503.
func TestUpstreamFailures(t *testing.T) {
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	admin := &auth.Claims{Email: "op@sov.test", Tier: "owner"}
	if w := call(New(gone.URL, quiet), admin, http.MethodGet, "/api/v1/ops/incidents", "", nil); w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "ops-records-unavailable") {
		t.Errorf("unreachable: status %d body %s", w.Code, w.Body.String())
	}
	for _, base := range []string{"ftp://ops-records", "http://", "::"} {
		if w := call(New(base, quiet), admin, http.MethodGet, "/api/v1/ops/incidents", "", nil); w.Code != http.StatusServiceUnavailable {
			t.Errorf("%q: status %d", base, w.Code)
		}
	}
	srv, got := upstream(t)
	t.Setenv(URLEnv, srv.URL)
	if w := call(FromEnv(quiet), admin, http.MethodGet, "/api/v1/ops/incidents", "", nil); w.Code != http.StatusOK || len(got()) != 1 {
		t.Errorf("%s: status %d, upstream calls %d", URLEnv, w.Code, len(got()))
	}
}
