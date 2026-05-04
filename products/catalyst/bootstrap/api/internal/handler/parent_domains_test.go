// parent_domains_test.go — coverage for the admin parent-domain pool
// surface (issue #829).
//
// The HTTP handlers are exercised end-to-end against a stub PDM (no
// network egress, no real registrar API). The propagation panel is
// covered by a unit test against `nsSetsMatch` + `lookupNSAt` (the
// latter requires network egress so it is gated behind a build tag in
// CI; the unit-test path here covers the wire shape only).
package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
)

// resetParentDomainStore is a per-test hygiene helper — the global
// store is shared across handlers so test isolation needs an explicit
// teardown.
func resetParentDomainStore() {
	globalParentDomainStore = &parentDomainStore{entries: sync.Map{}}
}

func newParentDomainsRouter(h *Handler) *chi.Mux {
	r := chi.NewRouter()
	r.Get("/api/v1/sovereign/parent-domains", h.ListParentDomains)
	r.Post("/api/v1/sovereign/parent-domains", h.AddParentDomain)
	r.Delete("/api/v1/sovereign/parent-domains/{name}", h.DeleteParentDomain)
	r.Get("/api/v1/sovereign/parent-domains/{name}/propagation", h.GetPropagation)
	return r
}

func TestListParentDomains_EmptyPool(t *testing.T) {
	resetParentDomainStore()
	t.Setenv("CATALYST_PRIMARY_DOMAIN", "")
	h := &Handler{log: slog.Default()}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereign/parent-domains", nil)
	newParentDomainsRouter(h).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Items []ParentDomain `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) != 0 {
		t.Fatalf("want empty pool, got %d items", len(resp.Items))
	}
}

func TestListParentDomains_PrimaryFromEnv(t *testing.T) {
	resetParentDomainStore()
	t.Setenv("CATALYST_PRIMARY_DOMAIN", "omani.works")
	h := &Handler{log: slog.Default()}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereign/parent-domains", nil)
	newParentDomainsRouter(h).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var resp struct {
		Items []ParentDomain `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) != 1 || resp.Items[0].Name != "omani.works" || resp.Items[0].Role != RolePrimary {
		t.Fatalf("expected synthesised primary row, got %+v", resp.Items)
	}
	if resp.Items[0].FlipStatus != FlipStatusReady {
		t.Fatalf("expected primary FlipStatus=ready, got %s", resp.Items[0].FlipStatus)
	}
}

func TestAddParentDomain_ValidationErrors(t *testing.T) {
	resetParentDomainStore()
	t.Setenv("CATALYST_PRIMARY_DOMAIN", "")
	h := &Handler{log: slog.Default()}

	cases := []struct {
		name    string
		body    string
		wantHTTP int
		wantErr string
	}{
		{
			name:     "missing name",
			body:     `{"name":"","registrarKind":"dynadot","registrarToken":"x"}`,
			wantHTTP: http.StatusUnprocessableEntity,
			wantErr:  "invalid-name",
		},
		{
			name:     "not fqdn",
			body:     `{"name":"foo","registrarKind":"dynadot","registrarToken":"x"}`,
			wantHTTP: http.StatusUnprocessableEntity,
			wantErr:  "invalid-name",
		},
		{
			name:     "missing token",
			body:     `{"name":"omani.works","registrarKind":"dynadot","registrarToken":""}`,
			wantHTTP: http.StatusUnprocessableEntity,
			wantErr:  "missing-token",
		},
		{
			name:     "invalid role",
			body:     `{"name":"omani.works","role":"bogus","registrarKind":"dynadot","registrarToken":"x"}`,
			wantHTTP: http.StatusUnprocessableEntity,
			wantErr:  "invalid-role",
		},
		{
			name:     "unsupported registrar",
			body:     `{"name":"omani.works","role":"sme-pool","registrarKind":"bogus","registrarToken":"x"}`,
			wantHTTP: http.StatusUnprocessableEntity,
			wantErr:  "unsupported-registrar",
		},
		{
			name:     "garbage body",
			body:     `not json`,
			wantHTTP: http.StatusBadRequest,
			wantErr:  "invalid-body",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/sovereign/parent-domains",
				bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			newParentDomainsRouter(h).ServeHTTP(rec, req)
			if rec.Code != tc.wantHTTP {
				t.Fatalf("want %d, got %d body=%s", tc.wantHTTP, rec.Code, rec.Body.String())
			}
			if !bytes.Contains(rec.Body.Bytes(), []byte(tc.wantErr)) {
				t.Fatalf("want body to mention %q, got %s", tc.wantErr, rec.Body.String())
			}
		})
	}
}

func TestAddParentDomain_DuplicateConflict(t *testing.T) {
	resetParentDomainStore()
	t.Setenv("CATALYST_PRIMARY_DOMAIN", "")

	// Stub PDM that always 200s for /set-ns, so the first add succeeds.
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/set-ns") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"valid":true}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer stub.Close()
	t.Setenv("POOL_DOMAIN_MANAGER_URL", stub.URL)

	h := &Handler{log: slog.Default()}

	body := `{"name":"omani.trade","role":"sme-pool","registrarKind":"dynadot","registrarToken":"abc"}`
	first := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sovereign/parent-domains", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	newParentDomainsRouter(h).ServeHTTP(first, req)
	if first.Code != http.StatusCreated {
		t.Fatalf("first add: want 201, got %d body=%s", first.Code, first.Body.String())
	}

	// Second POST with the same name → 409.
	second := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/sovereign/parent-domains", bytes.NewBufferString(body))
	req2.Header.Set("Content-Type", "application/json")
	newParentDomainsRouter(h).ServeHTTP(second, req2)
	if second.Code != http.StatusConflict {
		t.Fatalf("second add: want 409, got %d", second.Code)
	}
}

func TestAddParentDomain_PDMSetNSFail(t *testing.T) {
	resetParentDomainStore()
	t.Setenv("CATALYST_PRIMARY_DOMAIN", "")

	// Stub PDM that 502s the /set-ns call so we exercise the failure path.
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/set-ns") {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":"upstream-rejected"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer stub.Close()
	t.Setenv("POOL_DOMAIN_MANAGER_URL", stub.URL)

	h := &Handler{log: slog.Default()}

	body := `{"name":"oman.tel","role":"sme-pool","registrarKind":"dynadot","registrarToken":"abc"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sovereign/parent-domains", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	newParentDomainsRouter(h).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d body=%s", rec.Code, rec.Body.String())
	}

	// Row should now exist with status=failed.
	pd, ok := globalParentDomainStore.get("oman.tel")
	if !ok {
		t.Fatal("row should be persisted with failed status")
	}
	if pd.FlipStatus != FlipStatusFailed {
		t.Fatalf("want FlipStatus=failed, got %s", pd.FlipStatus)
	}
}

func TestDeleteParentDomain_Removes(t *testing.T) {
	resetParentDomainStore()
	t.Setenv("CATALYST_PRIMARY_DOMAIN", "")
	globalParentDomainStore.put(&ParentDomain{
		Name:       "omani.trade",
		Role:       RoleSMEPool,
		FlipStatus: FlipStatusReady,
	})
	h := &Handler{log: slog.Default()}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/sovereign/parent-domains/omani.trade", nil)
	newParentDomainsRouter(h).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", rec.Code)
	}
	if _, ok := globalParentDomainStore.get("omani.trade"); ok {
		t.Fatal("row should be deleted")
	}
}

func TestDeleteParentDomain_PrimaryLocked(t *testing.T) {
	resetParentDomainStore()
	t.Setenv("CATALYST_PRIMARY_DOMAIN", "omani.works")
	h := &Handler{log: slog.Default()}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/sovereign/parent-domains/omani.works", nil)
	newParentDomainsRouter(h).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestNSSetsMatch(t *testing.T) {
	cases := []struct {
		name     string
		got      []string
		expected []string
		want     bool
	}{
		{"empty expected", []string{"a"}, nil, false},
		{"perfect match", []string{"ns1.x.y", "ns2.x.y"}, []string{"ns1.x.y", "ns2.x.y"}, true},
		{"partial overlap", []string{"ns1.x.y", "ns2.foo"}, []string{"ns1.x.y", "ns2.x.y"}, true},
		{"no overlap", []string{"ns1.foo", "ns2.foo"}, []string{"ns1.x.y", "ns2.x.y"}, false},
		{"trailing dot tolerant", []string{"ns1.x.y."}, []string{"ns1.x.y"}, true},
		{"case insensitive", []string{"NS1.X.Y"}, []string{"ns1.x.y"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nsSetsMatch(tc.got, tc.expected); got != tc.want {
				t.Fatalf("nsSetsMatch(%v, %v) = %v, want %v", tc.got, tc.expected, got, tc.want)
			}
		})
	}
}

func TestResolversFromEnv_Default(t *testing.T) {
	t.Setenv("CATALYST_DNS_PROPAGATION_RESOLVERS", "")
	got := resolversFromEnv()
	if len(got) < 5 {
		t.Fatalf("expected ≥5 default resolvers, got %d", len(got))
	}
}

func TestResolversFromEnv_Override(t *testing.T) {
	t.Setenv("CATALYST_DNS_PROPAGATION_RESOLVERS", "10.0.0.1, 10.0.0.2,, 10.0.0.3")
	got := resolversFromEnv()
	if len(got) != 3 {
		t.Fatalf("expected 3 resolvers from override, got %d", len(got))
	}
	if got[0].IP != "10.0.0.1" || got[2].IP != "10.0.0.3" {
		t.Fatalf("override parse wrong: %+v", got)
	}
}

func TestExpectedNS_FromEnvOverride(t *testing.T) {
	t.Setenv("CATALYST_EXPECTED_NS", "ns1.foo.bar, ns2.foo.bar.")
	h := &Handler{log: slog.Default()}
	got := h.expectedNSFor("any.domain")
	if len(got) != 2 || got[0] != "ns1.foo.bar" || got[1] != "ns2.foo.bar" {
		t.Fatalf("env override parse wrong: %v", got)
	}
}

func TestExpectedNS_DerivedFromPrimary(t *testing.T) {
	t.Setenv("CATALYST_EXPECTED_NS", "")
	t.Setenv("CATALYST_PRIMARY_DOMAIN", "omani.works")
	h := &Handler{log: slog.Default()}
	got := h.expectedNSFor("any.domain")
	if len(got) != 2 || got[0] != "ns1.omani.works" || got[1] != "ns2.omani.works" {
		t.Fatalf("derived NS wrong: %v", got)
	}
}

func TestGetPropagation_InvalidName(t *testing.T) {
	h := &Handler{log: slog.Default()}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereign/parent-domains/foo/propagation", nil)
	newParentDomainsRouter(h).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422 for non-FQDN, got %d", rec.Code)
	}
}

func TestGetPropagation_ResponseShape(t *testing.T) {
	// Restrict to a single non-resolvable IP so the test is fast +
	// deterministic. The 192.0.2.0/24 block is RFC 5737 TEST-NET-1 —
	// guaranteed to never route to a real DNS server.
	t.Setenv("CATALYST_DNS_PROPAGATION_RESOLVERS", "192.0.2.1")
	t.Setenv("CATALYST_EXPECTED_NS", "ns1.example.com,ns2.example.com")
	h := &Handler{log: slog.Default()}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereign/parent-domains/example.com/propagation", nil)
	newParentDomainsRouter(h).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body, _ := io.ReadAll(rec.Body)
	var resp PropagationResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Domain != "example.com" {
		t.Fatalf("domain wrong: %s", resp.Domain)
	}
	if resp.Total != 1 || len(resp.Resolvers) != 1 {
		t.Fatalf("expected 1 resolver, got total=%d resolvers=%d", resp.Total, len(resp.Resolvers))
	}
	// Status must be 'error' since 192.0.2.1 doesn't answer.
	if resp.Resolvers[0].Status != "error" {
		t.Fatalf("expected status=error for blackhole resolver, got %s", resp.Resolvers[0].Status)
	}
	if len(resp.ExpectedNS) != 2 {
		t.Fatalf("expected 2 NS records, got %d", len(resp.ExpectedNS))
	}
	if resp.Percentage != 0 {
		t.Fatalf("expected 0%% propagated when all errored, got %d", resp.Percentage)
	}
}
