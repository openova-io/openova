package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sharedauth "github.com/openova-io/openova/core/services/shared/auth"
)

// TestRequireMarketplaceProxy covers the canonical gate every
// marketplace.* handler runs first.
func TestRequireMarketplaceProxy(t *testing.T) {
	cases := map[string]struct {
		env    *Env
		errSub string
	}{
		"nil env":      {nil, "server env missing"},
		"no api url":   {&Env{TenantID: "t", SandboxToken: "x"}, "SANDBOX_DOMAIN_API_URL"},
		"no tenant":    {&Env{DomainAPIURL: "http://d", SandboxToken: "x"}, "SANDBOX_TENANT_ID"},
		"no token":     {&Env{DomainAPIURL: "http://d", TenantID: "t"}, "SANDBOX_TOKEN"},
		"populated":    {&Env{DomainAPIURL: "http://d", TenantID: "t", SandboxToken: "x"}, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := requireMarketplaceProxy(tc.env)
			if tc.errSub == "" {
				if err != nil {
					t.Errorf("err=%v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.errSub) {
				t.Errorf("err=%v want substring %q", err, tc.errSub)
			}
		})
	}
}

// TestMPFQDNRE covers the FQDN pre-validation regex.
func TestMPFQDNRE(t *testing.T) {
	good := []string{"acme.com", "app.example.org", "foo.bar.baz", "a-b.c-d.io"}
	bad := []string{"", "single", ".leadingdot.com", "trailingdot.", "UPPER.com", "x..y.com", "-leading.com"}
	for _, s := range good {
		if !mpFQDNRE.MatchString(s) {
			t.Errorf("expected match: %q", s)
		}
	}
	for _, s := range bad {
		if mpFQDNRE.MatchString(s) {
			t.Errorf("expected NO match: %q", s)
		}
	}
}

// TestMPSubdomainRE covers the subdomain pre-validation regex.
func TestMPSubdomainRE(t *testing.T) {
	good := []string{"ab", "acme", "dash-board", "x9", "0to9"}
	bad := []string{"", "a", "-leading", "trailing-", "UPPER", "with.dot", strings.Repeat("x", 33)}
	for _, s := range good {
		if !mpSubdomainRE.MatchString(s) {
			t.Errorf("expected match: %q", s)
		}
	}
	for _, s := range bad {
		if mpSubdomainRE.MatchString(s) {
			t.Errorf("expected NO match: %q", s)
		}
	}
}

// TestMarketplaceDomainBYOD_Validation covers arg validation before any
// network call. We use `WithEnv` without populating DomainAPIURL/TenantID/
// SandboxToken so the requireMarketplaceProxy gate also asserts pre-call.
// Cases below all FAIL before any HTTP attempt.
func TestMarketplaceDomainBYOD_Validation(t *testing.T) {
	ctx := WithEnv(context.Background(), &Env{}) // requireMarketplaceProxy will fire on populated calls
	cases := map[string]string{
		`{}`:                "is required",
		`{"fqdn":""}`:       "is required",
		`{"fqdn":"no-dot"}`: "must be a valid lowercase",
		`{"fqdn":"-bad.com"}`: "must be a valid lowercase",
	}
	for args, want := range cases {
		_, err := marketplaceDomainBYOD(ctx, json.RawMessage(args))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("args=%s err=%v want %q", args, err, want)
		}
	}
}

// TestMarketplaceDomainSubdomain_Validation covers arg validation
// before any HTTP roundtrip.
func TestMarketplaceDomainSubdomain_Validation(t *testing.T) {
	ctx := WithEnv(context.Background(), &Env{})
	cases := map[string]string{
		`{}`:                  "is required",
		`{"subdomain":""}`:    "is required",
		`{"subdomain":"-x"}`:  "must match",
		`{"subdomain":"x.y"}`: "must match",
		`{"subdomain":"a--b"}`: "consecutive hyphens",
		`{"subdomain":"ok","parent_zone":"single"}`: "must be a valid FQDN",
	}
	for args, want := range cases {
		_, err := marketplaceDomainSubdomain(ctx, json.RawMessage(args))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("args=%s err=%v want %q", args, err, want)
		}
	}
}

// TestMarketplaceDomainBYOD_Proxy verifies the proxy round-trip:
// validates that body fields + bearer + URL path land correctly on the
// domain service, and that a 201 response is shaped into the
// marketplaceDomainBYODResponse envelope.
func TestMarketplaceDomainBYOD_Proxy(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{
			"domain": {"id": 42, "domain": "acme.com", "type": "byod"},
			"cname_target": "cname.t39.omani.works",
			"registrar": "godaddy",
			"instructions": "Set CNAME at GoDaddy..."
		}`))
	}))
	defer srv.Close()

	ctx := WithEnv(context.Background(), &Env{
		DomainAPIURL: srv.URL,
		TenantID:     "tenant-1",
		SandboxToken: "tok-abc",
	})
	out, err := marketplaceDomainBYOD(ctx, json.RawMessage(`{"fqdn":"acme.com"}`))
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if gotMethod != "POST" || gotPath != "/domain/byod" {
		t.Errorf("method=%q path=%q", gotMethod, gotPath)
	}
	if gotAuth != "Bearer tok-abc" {
		t.Errorf("auth=%q", gotAuth)
	}
	if gotBody["tenant_id"] != "tenant-1" || gotBody["domain"] != "acme.com" {
		t.Errorf("body=%v", gotBody)
	}
	resp, ok := out.(marketplaceDomainBYODResponse)
	if !ok {
		t.Fatalf("out type=%T want marketplaceDomainBYODResponse", out)
	}
	if resp.Status != "Registered" || resp.CNAMETarget != "cname.t39.omani.works" || resp.Registrar != "godaddy" {
		t.Errorf("resp=%+v", resp)
	}
}

// TestMarketplaceDomainBYOD_Conflict verifies the idempotent 409 path.
func TestMarketplaceDomainBYOD_Conflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"domain already registered"}`))
	}))
	defer srv.Close()

	ctx := WithEnv(context.Background(), &Env{
		DomainAPIURL: srv.URL,
		TenantID:     "tenant-1",
		SandboxToken: "tok-abc",
	})
	out, err := marketplaceDomainBYOD(ctx, json.RawMessage(`{"fqdn":"acme.com"}`))
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	m, ok := out.(map[string]any)
	if !ok || m["status"] != "AlreadyExists" {
		t.Errorf("out=%v", out)
	}
}

// TestMarketplaceDomainSubdomain_Proxy verifies the subdomain proxy
// round-trip including the optional parent_zone → tld translation.
func TestMarketplaceDomainSubdomain_Proxy(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{
			"id": 7,
			"domain": "dashboard.omani.homes",
			"type": "subdomain",
			"tld": "omani.homes",
			"subdomain": "dashboard"
		}`))
	}))
	defer srv.Close()
	ctx := WithEnv(context.Background(), &Env{
		DomainAPIURL: srv.URL,
		TenantID:     "tenant-1",
		SandboxToken: "tok-abc",
	})
	out, err := marketplaceDomainSubdomain(ctx, json.RawMessage(`{"subdomain":"dashboard","parent_zone":"omani.homes"}`))
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if gotBody["tenant_id"] != "tenant-1" || gotBody["subdomain"] != "dashboard" || gotBody["tld"] != "omani.homes" {
		t.Errorf("body=%v", gotBody)
	}
	resp, ok := out.(marketplaceDomainSubdomainResponse)
	if !ok {
		t.Fatalf("type=%T", out)
	}
	if resp.Status != "Registered" || resp.FQDN != "dashboard.omani.homes" || resp.ParentZone != "omani.homes" {
		t.Errorf("resp=%+v", resp)
	}
}

// TestMarketplaceCapabilityGate confirms the registry rejects
// marketplace.* calls whose claims lack the `marketplace` capability.
func TestMarketplaceCapabilityGate(t *testing.T) {
	r := NewRegistry(&Env{
		OrgID:        "acme",
		JWTSecret:    []byte("x"),
		DomainAPIURL: "http://d",
		TenantID:     "t",
		SandboxToken: "tok",
	})
	for _, name := range []string{"marketplace.domain.byod", "marketplace.domain.subdomain"} {
		t.Run(name, func(t *testing.T) {
			cl := &sharedauth.Claims{OrgID: "acme", Capabilities: []string{"sandbox.db"}}
			args := json.RawMessage(`{"fqdn":"acme.com","subdomain":"x"}`)
			_, err := r.Call(context.Background(), name, args, CallOpts{Claims: cl})
			if err == nil || !strings.Contains(err.Error(), "forbidden") {
				t.Errorf("missing cap: err=%v want forbidden", err)
			}
		})
	}
}
