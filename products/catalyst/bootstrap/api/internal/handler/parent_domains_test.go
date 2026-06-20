// parent_domains_test.go — coverage for the admin parent-domain pool
// surface (issues #829 + #837).
//
// The HTTP handlers are exercised end-to-end against a stub PDM (no
// network egress, no real registrar API). The propagation panel is
// covered by a unit test against `nsSetsMatch` + `lookupNSAt` (the
// latter requires network egress so it is gated behind a build tag in
// CI; the unit-test path here covers the wire shape only).
//
// Issue #837 swapped the in-memory parentDomainStore placeholder for the
// persistent Deployment.parentDomains[] slice on the adopted deployment.
// Tests that previously seeded data via globalParentDomainStore.put now
// seed via the active Deployment record; tests that previously inspected
// globalParentDomainStore.get inspect dep.Request.ParentDomains. The
// persistence round-trip across simulated catalyst-api Pod restarts
// is covered by TestAddParentDomain_PersistsAcrossRestart.
package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

func newParentDomainsRouter(h *Handler) *chi.Mux {
	r := chi.NewRouter()
	r.Get("/api/v1/sovereign/parent-domains", h.ListParentDomains)
	r.Post("/api/v1/sovereign/parent-domains", h.AddParentDomain)
	r.Delete("/api/v1/sovereign/parent-domains/{name}", h.DeleteParentDomain)
	r.Get("/api/v1/sovereign/parent-domains/{name}/propagation", h.GetPropagation)
	return r
}

// seedActiveDeployment registers an adopted Deployment on h whose
// Request.ParentDomains slice is the durable parent-domain pool the
// admin handlers read/write. Mirrors the production lifecycle (a
// Sovereign with finalised handover) so the swap from in-memory store
// to Deployment.parentDomains[] (issue #837) exercises the same code
// path tests do.
func seedActiveDeployment(t *testing.T, h *Handler, primaryFQDN string, smePool ...string) *Deployment {
	t.Helper()
	now := time.Now().UTC()
	pds := []provisioner.ParentDomain{}
	for _, name := range smePool {
		pds = append(pds, provisioner.ParentDomain{
			Name:          name,
			Role:          provisioner.ParentDomainRoleOrgPool,
			RegistrarKind: "dynadot",
			AddedAt:       now,
		})
	}
	dep := &Deployment{
		ID:        "test-active-" + primaryFQDN,
		Status:    "ready",
		StartedAt: now,
		AdoptedAt: &now,
		eventsCh:  make(chan provisioner.Event, 16),
		done:      make(chan struct{}),
		Request: provisioner.Request{
			SovereignFQDN: primaryFQDN,
			ParentDomains: pds,
		},
	}
	close(dep.done)
	close(dep.eventsCh)
	h.deployments.Store(dep.ID, dep)
	return dep
}

func TestListParentDomains_EmptyPool(t *testing.T) {
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
	t.Setenv("CATALYST_PRIMARY_DOMAIN", "")
	h := &Handler{log: slog.Default()}

	cases := []struct {
		name     string
		body     string
		wantHTTP int
		wantErr  string
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
			body:     `{"name":"omani.works","role":"org-pool","registrarKind":"bogus","registrarToken":"x"}`,
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
	// Adopted Sovereign required for the persistent pool — without an
	// adopted deployment AddParentDomain still runs the pipeline but the
	// row is non-durable; the duplicate check uses findParentDomain
	// against the adopted record, so we need one for the second-POST
	// 409 to fire.
	seedActiveDeployment(t, h, "omani.works")

	body := `{"name":"omani.trade","role":"org-pool","registrarKind":"dynadot","registrarToken":"abc"}`
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
	dep := seedActiveDeployment(t, h, "omani.works")

	body := `{"name":"oman.tel","role":"org-pool","registrarKind":"dynadot","registrarToken":"abc"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sovereign/parent-domains", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	newParentDomainsRouter(h).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d body=%s", rec.Code, rec.Body.String())
	}

	// Failed pipeline does NOT persist a row — the wipe-and-restart
	// rule (see file-level Persistence comment). The active deployment's
	// ParentDomains slice must be untouched.
	dep.mu.Lock()
	persisted := append([]provisioner.ParentDomain(nil), dep.Request.ParentDomains...)
	dep.mu.Unlock()
	if len(persisted) != 0 {
		t.Fatalf("failed pipeline must not persist a row; got %+v", persisted)
	}

	// Wire shape: the response carries an in-line ParentDomain with
	// FlipStatus=failed for the wizard's progress UI.
	var resp struct {
		Item ParentDomain `json:"item"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Item.FlipStatus != FlipStatusFailed {
		t.Fatalf("want FlipStatus=failed in response item, got %s", resp.Item.FlipStatus)
	}
}

func TestDeleteParentDomain_Removes(t *testing.T) {
	t.Setenv("CATALYST_PRIMARY_DOMAIN", "")

	h := &Handler{log: slog.Default()}
	dep := seedActiveDeployment(t, h, "omani.works", "omani.trade")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/sovereign/parent-domains/omani.trade", nil)
	newParentDomainsRouter(h).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", rec.Code)
	}
	if _, ok := findParentDomain(dep, "omani.trade"); ok {
		t.Fatal("row should be deleted from active deployment's ParentDomains")
	}
}

func TestDeleteParentDomain_PrimaryLocked(t *testing.T) {
	t.Setenv("CATALYST_PRIMARY_DOMAIN", "omani.works")
	h := &Handler{log: slog.Default()}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/sovereign/parent-domains/omani.works", nil)
	newParentDomainsRouter(h).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestAddParentDomain_PersistsAcrossRestart proves the issue #837 DoD:
// "Create a domain → restart catalyst-api → list endpoint still
// returns it". A first Handler instance (h1) writes the new domain
// onto the adopted Deployment record; the underlying flat-file store
// persists. A second Handler instance (h2) constructed against the
// SAME store directory rehydrates the deployment via restoreFromStore
// + fromRecord, and a fresh GET /parent-domains MUST surface the
// persisted row through ListParentDomains.
//
// The fixture pattern follows deployments_persist_test.go: a real
// store rooted at t.TempDir(), NewWithStore for both Pods, the
// adopted deployment seeded with a non-zero AdoptedAt so
// activeDeployment() picks it up.
func TestAddParentDomain_PersistsAcrossRestart(t *testing.T) {
	t.Setenv("CATALYST_PRIMARY_DOMAIN", "")

	// Stub PDM — every call succeeds so the per-domain pipeline
	// commits cleanly.
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"valid":true}`))
	}))
	defer stub.Close()
	t.Setenv("POOL_DOMAIN_MANAGER_URL", stub.URL)

	dir := t.TempDir()
	st1, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}

	// First "Pod" — register an adopted deployment, POST a new
	// parent domain via the admin handler, persist.
	h1 := NewWithStore(silentLogger(), &fakePDM{}, st1)
	now := time.Now().UTC()
	dep := &Deployment{
		ID:        "persist-parent-domains-id",
		Status:    "ready",
		StartedAt: now,
		AdoptedAt: &now,
		eventsCh:  make(chan provisioner.Event, 16),
		done:      make(chan struct{}),
		Request: provisioner.Request{
			SovereignFQDN: "omani.works",
		},
	}
	close(dep.done)
	close(dep.eventsCh)
	h1.deployments.Store(dep.ID, dep)
	// Persist the row to disk so the rehydrate path has something to
	// load. Without this h2's restoreFromStore would not see the
	// deployment record at all.
	h1.persistDeployment(dep)

	body := `{"name":"omani.trade","role":"org-pool","registrarKind":"dynadot","registrarToken":"abc"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sovereign/parent-domains", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	newParentDomainsRouter(h1).ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first Pod add: want 201, got %d body=%s", rec.Code, rec.Body.String())
	}

	// Sanity — persisted row visible to h1's own list handler.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/sovereign/parent-domains", nil)
	newParentDomainsRouter(h1).ServeHTTP(rec, req)
	var pre struct {
		Items []ParentDomain `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pre); err != nil {
		t.Fatal(err)
	}
	if !containsName(pre.Items, "omani.trade") {
		t.Fatalf("h1 list missing omani.trade; got %+v", pre.Items)
	}

	// Second "Pod" — fresh handler, SAME store directory. Rehydrates
	// via restoreFromStore + fromRecord; the active deployment's
	// ParentDomains slice should round-trip through
	// store.RedactedRequest.ToProvisionerRequest.
	st2, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New (Pod 2): %v", err)
	}
	h2 := NewWithStore(silentLogger(), &fakePDM{}, st2)

	// The rehydrated deployment must be reachable via h2.deployments
	// AND its ParentDomains slice must carry the persisted entry.
	val, ok := h2.deployments.Load(dep.ID)
	if !ok {
		t.Fatalf("rehydration failed: %s not in h2.deployments", dep.ID)
	}
	rehydrated := val.(*Deployment)
	rehydrated.mu.Lock()
	pds := append([]provisioner.ParentDomain(nil), rehydrated.Request.ParentDomains...)
	rehydrated.mu.Unlock()
	foundOnDep := false
	for _, pd := range pds {
		if strings.EqualFold(pd.Name, "omani.trade") &&
			pd.Role == provisioner.ParentDomainRoleOrgPool {
			foundOnDep = true
			break
		}
	}
	if !foundOnDep {
		t.Fatalf("rehydrated Deployment.ParentDomains missing omani.trade; got %+v", pds)
	}

	// And the canonical proof: GET /api/v1/sovereign/parent-domains on
	// the SECOND Pod still surfaces the persisted row.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/sovereign/parent-domains", nil)
	newParentDomainsRouter(h2).ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second Pod list: want 200, got %d body=%s", rec2.Code, rec2.Body.String())
	}
	var post struct {
		Items []ParentDomain `json:"items"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &post); err != nil {
		t.Fatal(err)
	}
	if !containsName(post.Items, "omani.trade") {
		t.Fatalf("h2 list missing omani.trade after restart; got %+v", post.Items)
	}
}

// containsName reports whether items contains a row with the given
// (case-insensitive) Name. Local helper for the persistence test
// above; trivial enough to keep close to the test rather than
// promote into a shared fixture.
func containsName(items []ParentDomain, name string) bool {
	for _, it := range items {
		if strings.EqualFold(it.Name, name) {
			return true
		}
	}
	return false
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

// ── Issue #900 — glue IP forwarding through pdmFlipNS ───────────────────

// TestAddParentDomain_ForwardsGlueIP — when SOVEREIGN_LB_IP is set on
// the Pod env, AddParentDomain's pdmFlipNS step MUST include `glueIP`
// in the JSON body it POSTs to PDM /set-ns. This is the carrier wire
// that unblocks the Dynadot register_ns pre-step on the PDM side.
func TestAddParentDomain_ForwardsGlueIP(t *testing.T) {
	t.Setenv("CATALYST_PRIMARY_DOMAIN", "")
	t.Setenv("SOVEREIGN_LB_IP", "203.0.113.42")

	// Capture the raw body PDM receives so we can assert wire shape.
	var observedBody []byte
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/set-ns") {
			observedBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"valid":true}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer stub.Close()
	t.Setenv("POOL_DOMAIN_MANAGER_URL", stub.URL)

	h := &Handler{log: slog.Default()}
	seedActiveDeployment(t, h, "omani.works")

	body := `{"name":"customer-acme.tld","role":"org-pool","registrarKind":"dynadot","registrarToken":"abc"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sovereign/parent-domains", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	newParentDomainsRouter(h).ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d body=%s", rec.Code, rec.Body.String())
	}

	var got map[string]any
	if err := json.Unmarshal(observedBody, &got); err != nil {
		t.Fatalf("PDM body not JSON: %v (raw=%s)", err, observedBody)
	}
	if got["domain"] != "customer-acme.tld" {
		t.Errorf("PDM body domain=%v, want customer-acme.tld", got["domain"])
	}
	if got["glueIP"] != "203.0.113.42" {
		t.Errorf("PDM body glueIP=%v, want 203.0.113.42 (from SOVEREIGN_LB_IP env)", got["glueIP"])
	}
	// nameservers must still be present — glue IP doesn't replace it.
	if _, ok := got["nameservers"].([]any); !ok {
		t.Errorf("PDM body missing nameservers array; raw=%s", observedBody)
	}
}

// TestAddParentDomain_OmitsGlueIPWhenNoLBIP — when SOVEREIGN_LB_IP is
// empty (Catalyst-Zero / contabo / pre-#900 Sovereigns) AND no adopted
// deployment carries a Result.LoadBalancerIP, pdmFlipNS MUST omit the
// glueIP field entirely (so PDM falls through to plain set_ns and
// non-Dynadot adapters never see a stray field). Backward-compat
// guarantee for the existing pool of franchised Sovereigns.
func TestAddParentDomain_OmitsGlueIPWhenNoLBIP(t *testing.T) {
	t.Setenv("CATALYST_PRIMARY_DOMAIN", "")
	t.Setenv("SOVEREIGN_LB_IP", "")

	var observedBody []byte
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/set-ns") {
			observedBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"valid":true}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer stub.Close()
	t.Setenv("POOL_DOMAIN_MANAGER_URL", stub.URL)

	h := &Handler{log: slog.Default()}
	// Adopted deployment WITHOUT Result.LoadBalancerIP — fallback chain
	// resolves to "" so glueIP is omitted from the wire payload.
	seedActiveDeployment(t, h, "omani.works")

	body := `{"name":"customer-acme.tld","role":"org-pool","registrarKind":"dynadot","registrarToken":"abc"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sovereign/parent-domains", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	newParentDomainsRouter(h).ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d body=%s", rec.Code, rec.Body.String())
	}

	var got map[string]any
	if err := json.Unmarshal(observedBody, &got); err != nil {
		t.Fatalf("PDM body not JSON: %v", err)
	}
	if _, present := got["glueIP"]; present {
		t.Errorf("expected no glueIP in PDM body when SOVEREIGN_LB_IP is unset; got %v", got["glueIP"])
	}
}

// TestAddParentDomain_GlueIPFromDeploymentResult — when SOVEREIGN_LB_IP
// is empty but the adopted deployment's Result.LoadBalancerIP is set,
// pdmFlipNS MUST fall through to the deployment-record source and
// still forward the IP to PDM. Covers the Catalyst-Zero (contabo) path
// during single-Sovereign sandbox runs of the Day-2 add-domain flow.
func TestAddParentDomain_GlueIPFromDeploymentResult(t *testing.T) {
	t.Setenv("CATALYST_PRIMARY_DOMAIN", "")
	t.Setenv("SOVEREIGN_LB_IP", "")

	var observedBody []byte
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/set-ns") {
			observedBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"valid":true}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer stub.Close()
	t.Setenv("POOL_DOMAIN_MANAGER_URL", stub.URL)

	h := &Handler{log: slog.Default()}
	dep := seedActiveDeployment(t, h, "omani.works")
	dep.Result = &provisioner.Result{LoadBalancerIP: "198.51.100.7"}

	body := `{"name":"customer-acme.tld","role":"org-pool","registrarKind":"dynadot","registrarToken":"abc"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sovereign/parent-domains", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	newParentDomainsRouter(h).ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d body=%s", rec.Code, rec.Body.String())
	}

	var got map[string]any
	if err := json.Unmarshal(observedBody, &got); err != nil {
		t.Fatalf("PDM body not JSON: %v", err)
	}
	if got["glueIP"] != "198.51.100.7" {
		t.Errorf("PDM body glueIP=%v, want 198.51.100.7 (from Deployment.Result.LoadBalancerIP)", got["glueIP"])
	}
}
