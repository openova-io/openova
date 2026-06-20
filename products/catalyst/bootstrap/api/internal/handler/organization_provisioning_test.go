package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// fakeGitOps records overlay writes/deletes and returns deterministic
// SHAs.
type fakeGitOps struct {
	mu       sync.Mutex
	writes   []store.OrganizationProvisionRecord
	deletes  []store.OrganizationProvisionRecord
	failNext bool
	terminal bool // when true return a terminal-class error
}

func (f *fakeGitOps) WriteTenantOverlay(_ context.Context, rec store.OrganizationProvisionRecord) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext {
		f.failNext = false
		if f.terminal {
			return "", errors.New("terminal-config-error")
		}
		return "", errors.New("transient-network-error")
	}
	f.writes = append(f.writes, rec)
	return "sha-" + rec.OrganizationID, nil
}

func (f *fakeGitOps) DeleteTenantOverlay(_ context.Context, rec store.OrganizationProvisionRecord) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes = append(f.deletes, rec)
	return "sha-del-" + rec.OrganizationID, nil
}

// fakeDNS records calls + returns canned responses.
type fakeDNS struct {
	mu             sync.Mutex
	provisionCalls []string
	cnameCalls     []string
	provisionErr   error
	cnameErr       error
}

func (f *fakeDNS) ProvisionFreeSubdomain(_ context.Context, sub, parent, ip string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.provisionCalls = append(f.provisionCalls, sub+":"+parent+":"+ip)
	return f.provisionErr
}

func (f *fakeDNS) ValidateBYOCNAME(_ context.Context, byo, legacy string, accepted ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	tail := strings.Join(accepted, ",")
	f.cnameCalls = append(f.cnameCalls, byo+"->"+legacy+"|"+tail)
	return f.cnameErr
}

// fakeKCClients tracks calls.
type fakeKCClients struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (f *fakeKCClients) ProvisionOrganizationClients(_ context.Context, rec store.OrganizationProvisionRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, rec.OrganizationID)
	return f.err
}

// fakeTenantEmitter records events.
type fakeTenantEmitter struct {
	mu      sync.Mutex
	created []store.OrganizationProvisionRecord
	deleted []store.OrganizationProvisionRecord
	changed []store.OrganizationProvisionRecord
}

func (f *fakeTenantEmitter) EmitOrganizationCreated(_ context.Context, r store.OrganizationProvisionRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, r)
	return nil
}
func (f *fakeTenantEmitter) EmitOrganizationStateChanged(_ context.Context, r store.OrganizationProvisionRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.changed = append(f.changed, r)
	return nil
}
func (f *fakeTenantEmitter) EmitOrganizationDeleted(_ context.Context, r store.OrganizationProvisionRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, r)
	return nil
}

func newTestHandlerWithOrganizationDeps(t *testing.T) (*Handler, *fakeGitOps, *fakeDNS, *fakeKCClients, *fakeTenantEmitter, *store.TenantRegistry) {
	t.Helper()
	dir := t.TempDir()
	tenantStore, err := store.NewOrganizationProvisionStore(dir)
	if err != nil {
		t.Fatalf("tenant store: %v", err)
	}
	registry, err := store.NewTenantRegistry(dir)
	if err != nil {
		t.Fatalf("tenant registry: %v", err)
	}
	gitops := &fakeGitOps{}
	dns := &fakeDNS{}
	kc := &fakeKCClients{}
	emitter := &fakeTenantEmitter{}
	h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h.SetTenantRegistry(registry)
	h.SetOrganizationDeps(OrganizationDeps{
		Store:            tenantStore,
		GitOps:           gitops,
		DNS:              dns,
		KeycloakClients:  kc,
		Events:           emitter,
		TenantRegistry:   registry,
		OTECHFQDN:        "otech.example",
		OTECHIngressIPv4: "192.0.2.10",
		MaxRetryCount:    5,
	})
	return h, gitops, dns, kc, emitter, registry
}

func TestCreateOrganization_HappyPathFreeSubdomain(t *testing.T) {
	h, gitops, dns, kc, emitter, registry := newTestHandlerWithOrganizationDeps(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/organizations",
		bytes.NewReader([]byte(`{
			"subdomain":    "acme",
			"admin_email":  "admin@acme.test",
			"company_name": "Acme",
			"domain_mode":  "free-subdomain"
		}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleCreateOrganization(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status: want 202 got %d body=%s", w.Code, w.Body.String())
	}
	var got orgTenantResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.State != store.STSDone {
		t.Fatalf("state: want done got %s (lastError=%s)", got.State, got.LastError)
	}
	if got.ConsoleHost != "console.acme.otech.example" {
		t.Errorf("console_host: %s", got.ConsoleHost)
	}
	if got.CommitSHA == "" {
		t.Errorf("commit_sha empty")
	}
	if len(gitops.writes) != 1 {
		t.Errorf("gitops writes: want 1 got %d", len(gitops.writes))
	}
	if len(dns.provisionCalls) != 1 {
		t.Errorf("dns provision calls: want 1 got %d", len(dns.provisionCalls))
	}
	if got := dns.provisionCalls[0]; got != "acme:otech.example:192.0.2.10" {
		t.Errorf("dns call shape: %s", got)
	}
	if len(kc.calls) != 1 {
		t.Errorf("kc calls: want 1 got %d", len(kc.calls))
	}
	if len(emitter.created) != 1 || len(emitter.changed) < 5 {
		t.Errorf("events: created=%d changed=%d", len(emitter.created), len(emitter.changed))
	}

	// Tenant registry must have been populated.
	reg, ok := registry.Get("console.acme.otech.example")
	if !ok {
		t.Fatalf("registry: tenant not registered")
	}
	if reg.TenantKind != store.TenantKindSME {
		t.Errorf("registry kind: %s", reg.TenantKind)
	}
	if reg.OrganizationNamespace == "" || !strings.HasPrefix(reg.OrganizationNamespace, "sme-") {
		t.Errorf("registry namespace: %s", reg.OrganizationNamespace)
	}
}

func TestCreateOrganization_BYOValidationSuccess(t *testing.T) {
	h, _, dns, _, _, registry := newTestHandlerWithOrganizationDeps(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/organizations",
		bytes.NewReader([]byte(`{
			"subdomain":   "acme",
			"admin_email": "admin@acme.com",
			"domain_mode": "byo",
			"byo_domain":  "acme.com"
		}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleCreateOrganization(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	// Multi-domain Sovereign: the call now carries legacy=otech.example
	// AND the accepted-targets list (which on a single-domain Sovereign
	// degenerates to OTECHFQDN). The "|" separator is the test harness
	// shape — see fakeDNS.ValidateBYOCNAME.
	if got := dns.cnameCalls; len(got) != 1 || !strings.HasPrefix(got[0], "acme.com->otech.example|") {
		t.Errorf("byo cname call shape: %v", got)
	}
	if _, ok := registry.Get("console.acme.com"); !ok {
		t.Errorf("byo tenant not registered")
	}
}

func TestCreateOrganization_BYOValidationFailureMarksTerminal(t *testing.T) {
	h, _, dns, _, _, registry := newTestHandlerWithOrganizationDeps(t)
	dns.cnameErr = errBYOCNAMEMismatch

	req := httptest.NewRequest(http.MethodPost, "/api/v1/organizations",
		bytes.NewReader([]byte(`{
			"subdomain":   "acme",
			"admin_email": "admin@acme.com",
			"domain_mode": "byo",
			"byo_domain":  "acme.com"
		}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleCreateOrganization(w, req)

	var got orgTenantResponse
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got.State != store.STSFailed {
		t.Fatalf("state: want failed got %s", got.State)
	}
	if !strings.HasPrefix(got.LastError, "dns:terminal:") {
		t.Errorf("lastError prefix: %s", got.LastError)
	}
	if got.Steps.DNS != "failed" {
		t.Errorf("steps.dns: %s", got.Steps.DNS)
	}
	if _, ok := registry.Get("console.acme.com"); ok {
		t.Errorf("registry should NOT contain failed tenant")
	}
}

func TestCreateOrganization_GitOpsTransientFailure_Retryable(t *testing.T) {
	h, gitops, _, _, _, _ := newTestHandlerWithOrganizationDeps(t)
	// First call fails transiently; reconcile should retry and succeed.
	gitops.failNext = true

	req := httptest.NewRequest(http.MethodPost, "/api/v1/organizations",
		bytes.NewReader([]byte(`{
			"subdomain":   "acme",
			"admin_email": "admin@acme.com"
		}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleCreateOrganization(w, req)

	var first orgTenantResponse
	_ = json.Unmarshal(w.Body.Bytes(), &first)
	if first.State == store.STSDone {
		t.Fatalf("expected partial state on first call")
	}
	// Transient gitops failure leaves State=STSPending (retry budget not yet exhausted)
	// + LastError populated. The reconciler picks this up on the next pass.
	if first.State != store.STSPending {
		t.Errorf("first state: want pending got %s", first.State)
	}
	if !strings.HasPrefix(first.LastError, "vcluster:transient:") {
		t.Errorf("last_error prefix: %s", first.LastError)
	}

	// Now run reconciler — gitops returns SHA on second call.
	h.ReconcileAllPending(context.Background())

	r2 := httptest.NewRequest(http.MethodGet, "/api/v1/organizations/"+first.OrganizationID, nil)
	r2.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", first.OrganizationID)
	r2 = r2.WithContext(context.WithValue(r2.Context(), chi.RouteCtxKey, rctx))
	w2 := httptest.NewRecorder()
	h.HandleGetOrganization(w2, r2)
	var second orgTenantResponse
	_ = json.Unmarshal(w2.Body.Bytes(), &second)
	if second.State != store.STSDone {
		t.Fatalf("after reconcile: want done got %s lastError=%s", second.State, second.LastError)
	}
}

func TestCreateOrganization_ValidationErrors(t *testing.T) {
	h, _, _, _, _, _ := newTestHandlerWithOrganizationDeps(t)
	cases := []struct {
		name string
		body string
		want int
	}{
		{"missing-subdomain", `{"admin_email":"a@b.test"}`, 400},
		{"invalid-subdomain", `{"subdomain":"Bad_NAME","admin_email":"a@b.test"}`, 400},
		{"missing-email", `{"subdomain":"acme"}`, 400},
		{"bad-email", `{"subdomain":"acme","admin_email":"no-at"}`, 400},
		{"byo-missing-domain", `{"subdomain":"acme","admin_email":"a@b.test","domain_mode":"byo"}`, 400},
		{"bad-mode", `{"subdomain":"acme","admin_email":"a@b.test","domain_mode":"oops"}`, 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/organizations",
				bytes.NewReader([]byte(tc.body)))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.HandleCreateOrganization(w, req)
			if w.Code != tc.want {
				t.Errorf("status: want %d got %d body=%s", tc.want, w.Code, w.Body.String())
			}
		})
	}
}

func TestCreateOrganization_NoStoreReturns503(t *testing.T) {
	h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h.SetOrganizationDeps(OrganizationDeps{}) // empty -> no store
	req := httptest.NewRequest(http.MethodPost, "/api/v1/organizations",
		bytes.NewReader([]byte(`{"subdomain":"acme","admin_email":"a@b.test"}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleCreateOrganization(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("want 503 got %d", w.Code)
	}
}

func TestCreateOrganization_OTECHFQDNUnconfiguredReturns503(t *testing.T) {
	dir := t.TempDir()
	tenantStore, _ := store.NewOrganizationProvisionStore(dir)
	h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h.SetOrganizationDeps(OrganizationDeps{Store: tenantStore})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/organizations",
		bytes.NewReader([]byte(`{"subdomain":"acme","admin_email":"a@b.test"}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleCreateOrganization(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("want 503 got %d body=%s", w.Code, w.Body.String())
	}
}

func TestListOrganizations(t *testing.T) {
	h, _, _, _, _, _ := newTestHandlerWithOrganizationDeps(t)
	for _, sub := range []string{"acme", "globex"} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/organizations",
			bytes.NewReader([]byte(`{"subdomain":"`+sub+`","admin_email":"admin@`+sub+`.test"}`)))
		req.Header.Set("Content-Type", "application/json")
		h.HandleCreateOrganization(httptest.NewRecorder(), req)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/organizations", nil)
	w := httptest.NewRecorder()
	h.HandleListOrganizations(w, req)
	var got struct {
		Items []orgTenantResponse `json:"items"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if len(got.Items) != 2 {
		t.Errorf("items: want 2 got %d", len(got.Items))
	}
}

func TestDeleteOrganization_RemovesFromRegistry(t *testing.T) {
	h, gitops, _, _, _, registry := newTestHandlerWithOrganizationDeps(t)

	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/organizations",
		bytes.NewReader([]byte(`{"subdomain":"acme","admin_email":"a@b.test"}`)))
	createReq.Header.Set("Content-Type", "application/json")
	createW := httptest.NewRecorder()
	h.HandleCreateOrganization(createW, createReq)
	var created orgTenantResponse
	_ = json.Unmarshal(createW.Body.Bytes(), &created)

	// Sanity: registry has the host.
	if _, ok := registry.Get("console.acme.otech.example"); !ok {
		t.Fatalf("precondition: registry missing host")
	}

	delReq := httptest.NewRequest(http.MethodDelete,
		"/api/v1/organizations/"+created.OrganizationID, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", created.OrganizationID)
	delReq = delReq.WithContext(context.WithValue(delReq.Context(), chi.RouteCtxKey, rctx))
	delW := httptest.NewRecorder()
	h.HandleDeleteOrganization(delW, delReq)
	if delW.Code != http.StatusNoContent {
		t.Errorf("delete: want 204 got %d", delW.Code)
	}
	if _, ok := registry.Get("console.acme.otech.example"); ok {
		t.Errorf("registry should be cleared after delete")
	}
	if len(gitops.deletes) != 1 {
		t.Errorf("expected 1 gitops delete got %d", len(gitops.deletes))
	}
}

func TestRenderOrganizationOverlay_FreeSubdomain_AllChartsPresent(t *testing.T) {
	rec := store.OrganizationProvisionRecord{
		OrganizationID:  "t-acme",
		Subdomain:       "acme",
		DomainMode:      store.OrganizationDomainFreeSubdomain,
		AdminEmail:      "admin@acme.test",
		CompanyName:     "Acme Corp",
		OTECHFQDN:       "otech.example",
		VClusterName:    "vc-acme",
		TenantNamespace: "sme-t-acme",
	}
	files, err := renderOrganizationOverlay(rec, OrganizationChartVersions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, name := range []string{
		"kustomization.yaml",
		"namespace.yaml",
		"vcluster.yaml",
		"bp-keycloak.yaml",
		"bp-cnpg.yaml",
		"bp-newapi.yaml",
		"bp-wordpress-tenant.yaml",
		"bp-openclaw.yaml",
		"bp-stalwart-tenant.yaml",
		"certificate.yaml",
	} {
		body, ok := files[name]
		if !ok {
			t.Errorf("file %s missing", name)
			continue
		}
		if !strings.Contains(body, "acme") && !strings.Contains(body, "t-acme") {
			t.Errorf("file %s has no tenant identifier", name)
		}
	}
	// Free-subdomain mode must NOT emit a Certificate.
	if strings.Contains(files["certificate.yaml"], "kind: Certificate") {
		t.Errorf("free-subdomain should not emit a Certificate; got: %s", files["certificate.yaml"])
	}
}

func TestRenderOrganizationOverlay_BYO_EmitsCertificate(t *testing.T) {
	rec := store.OrganizationProvisionRecord{
		OrganizationID:  "t-acme",
		Subdomain:       "acme",
		DomainMode:      store.OrganizationDomainBYO,
		BYODomain:       "acme.com",
		AdminEmail:      "admin@acme.com",
		OTECHFQDN:       "otech.example",
		VClusterName:    "vc-acme",
		TenantNamespace: "sme-t-acme",
	}
	files, err := renderOrganizationOverlay(rec, OrganizationChartVersions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body := files["certificate.yaml"]
	if !strings.Contains(body, "kind: Certificate") {
		t.Errorf("byo mode: Certificate missing")
	}
	if !strings.Contains(body, "console.acme.com") {
		t.Errorf("byo cert: dnsNames missing console.acme.com — got %s", body)
	}
}

func TestRenderOrganizationOverlay_VersionsApplied(t *testing.T) {
	rec := store.OrganizationProvisionRecord{
		OrganizationID:  "t-acme",
		Subdomain:       "acme",
		DomainMode:      store.OrganizationDomainFreeSubdomain,
		AdminEmail:      "admin@acme.test",
		OTECHFQDN:       "otech.example",
		VClusterName:    "vc-acme",
		TenantNamespace: "sme-t-acme",
	}
	versions := OrganizationChartVersions{
		Keycloak: "1.2.3", CNPG: "0.5.0", WordPress: "0.1.0", OpenClaw: "0.1.0", Stalwart: "0.1.0",
	}
	files, err := renderOrganizationOverlay(rec, versions)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(files["bp-keycloak.yaml"], `version: "1.2.3"`) {
		t.Errorf("keycloak version missing: %s", files["bp-keycloak.yaml"])
	}
	if !strings.Contains(files["bp-cnpg.yaml"], `version: "0.5.0"`) {
		t.Errorf("cnpg version missing")
	}
}

func TestRenderOrganizationOverlay_NoVersionsDefaultsToStar(t *testing.T) {
	rec := store.OrganizationProvisionRecord{
		OrganizationID:  "t-acme",
		Subdomain:       "acme",
		DomainMode:      store.OrganizationDomainFreeSubdomain,
		AdminEmail:      "admin@acme.test",
		OTECHFQDN:       "otech.example",
		VClusterName:    "vc-acme",
		TenantNamespace: "sme-t-acme",
	}
	files, _ := renderOrganizationOverlay(rec, OrganizationChartVersions{})
	if !strings.Contains(files["bp-keycloak.yaml"], `version: "*"`) {
		t.Errorf("expected default '*' version: %s", files["bp-keycloak.yaml"])
	}
}

// TestRenderOrganizationOverlay_OpenClawOIDCAndLLMBlocks asserts that the
// bp-openclaw HelmRelease emits the canonical oidc.{issuerURL,clientId,
// clientSecret} + llm.{baseURL,apiKey,defaultModel} blocks per umbrella
// epic openova-io/openova#915. These blocks pre-wire OpenClaw to:
//   - per-tenant Keycloak (alice's users log in via alice's Keycloak)
//   - per-tenant NewAPI as the OpenAI-compatible LLM gateway
//     (alice's OpenClaw chats route through alice's NewAPI which
//     proxies to the configured channel — partner-hosted Qwen wired
//     by C4 of #915).
func TestRenderOrganizationOverlay_OpenClawOIDCAndLLMBlocks(t *testing.T) {
	rec := store.OrganizationProvisionRecord{
		OrganizationID:  "t-alice",
		Subdomain:       "alice",
		ParentDomain:    "omantel.omani.works",
		DomainMode:      store.OrganizationDomainFreeSubdomain,
		AdminEmail:      "admin@alice.test",
		CompanyName:     "Alice Corp",
		OTECHFQDN:       "otech107.omani.works",
		VClusterName:    "vc-alice",
		TenantNamespace: "sme-t-alice",
	}
	files, err := renderOrganizationOverlay(rec, OrganizationChartVersions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body, ok := files["bp-openclaw.yaml"]
	if !ok {
		t.Fatalf("bp-openclaw.yaml missing from rendered overlay")
	}
	// OIDC block (canonical).
	wantOIDC := []string{
		"    oidc:",
		"      issuerURL: https://keycloak.alice.omantel.omani.works/realms/sme-alice",
		"      clientId: openclaw",
		"      clientSecret:",
		"        name: openclaw-oidc-client-secret",
		"        key: OIDC_CLIENT_SECRET",
	}
	for _, line := range wantOIDC {
		if !strings.Contains(body, line) {
			t.Errorf("bp-openclaw oidc block missing line %q\n--- rendered ---\n%s", line, body)
		}
	}
	// LLM block (canonical) — per-tenant NewAPI endpoint, NOT direct
	// OpenAI; defaultModel is the placeholder NewAPI maps to the
	// partner-hosted Qwen channel C4 wires at tenant-create time.
	wantLLM := []string{
		"    llm:",
		"      baseURL: https://api.alice.omantel.omani.works/v1",
		"      apiKey:",
		"        name: openclaw-newapi-controller-token",
		"        key: NEWAPI_KEY",
		"      defaultModel: qwen3.6",
	}
	for _, line := range wantLLM {
		if !strings.Contains(body, line) {
			t.Errorf("bp-openclaw llm block missing line %q\n--- rendered ---\n%s", line, body)
		}
	}
	// Per-tenant LLM endpoint MUST be the SME's own api.<sub>.<parent>,
	// NEVER the otech-wide newapi.<otech-fqdn> (that would route every
	// SME's traffic through one shared gateway, defeating per-tenant
	// channel routing).
	if strings.Contains(body, "https://newapi.otech107.omani.works") {
		t.Errorf("bp-openclaw llm.baseURL must be per-tenant api.<sub>.<parent>, not otech-wide newapi: %s", body)
	}
}

// TestRenderOrganizationOverlay_NewAPIEmitted asserts the SME tenant
// overlay emits a per-tenant bp-newapi HelmRelease (#945). Without it
// the bp-openclaw HR points at https://api.<sub>.<parent>/v1 with no
// chart materialising that ingress — alice's OpenClaw boots and gets
// NXDOMAIN on every chat request.
//
// The chart values must:
//   - dependsOn bp-keycloak (admin-UI OIDC) AND bp-cnpg (Postgres backend).
//   - Emit ingress.host = api.<sub>.<parent> + ingress.adminHost =
//     admin.<sub>.<parent> so OpenClaw's llm.baseURL resolves.
//   - Wire auth.adminUI to the per-tenant Keycloak realm
//     (alice's tenant Keycloak), NOT a shared otech-level IdP.
//   - Enable defaultChannels.qwenPartner so the chart's channel-seed
//     post-install Job auto-seeds the partner-hosted Qwen at install time
//     (canonical first-otech default per #915 C4 PR #919).
//   - Reference newapi-pg-app for the database (bp-cnpg renders the
//     app secret in tenant ns) + newapi-credentials for app secrets.
func TestRenderOrganizationOverlay_NewAPIEmitted(t *testing.T) {
	rec := store.OrganizationProvisionRecord{
		OrganizationID:  "t-alice",
		Subdomain:       "alice",
		ParentDomain:    "omantel.omani.works",
		DomainMode:      store.OrganizationDomainFreeSubdomain,
		AdminEmail:      "admin@alice.test",
		CompanyName:     "Alice Corp",
		OTECHFQDN:       "otech113.omani.works",
		VClusterName:    "vc-alice",
		TenantNamespace: "sme-t-alice",
	}
	files, err := renderOrganizationOverlay(rec, OrganizationChartVersions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body, ok := files["bp-newapi.yaml"]
	if !ok {
		t.Fatalf("bp-newapi.yaml missing from rendered overlay")
	}

	// HelmRelease metadata.
	for _, want := range []string{
		"kind: HelmRelease",
		"name: bp-newapi",
		"namespace: sme-t-alice",
		"chart: bp-newapi",
		`version: "*"`,    // unconfigured chart version falls back to "*"
		"name: bp-newapi", // sourceRef.name
	} {
		if !strings.Contains(body, want) {
			t.Errorf("bp-newapi.yaml missing %q\n--- rendered ---\n%s", want, body)
		}
	}

	// dependsOn: bp-keycloak + bp-cnpg (BOTH required).
	wantDependsOn := []string{
		"  dependsOn:",
		"    - name: bp-keycloak",
		"      namespace: sme-t-alice",
		"    - name: bp-cnpg",
		"      namespace: sme-t-alice",
	}
	for _, line := range wantDependsOn {
		if !strings.Contains(body, line) {
			t.Errorf("bp-newapi.yaml dependsOn missing line %q\n--- rendered ---\n%s", line, body)
		}
	}

	// Ingress hosts — customer-API + admin UI.
	wantIngress := []string{
		"      host: api.alice.omantel.omani.works",
		"      adminHost: admin.alice.omantel.omani.works",
		"        issuer: letsencrypt-prod",
	}
	for _, line := range wantIngress {
		if !strings.Contains(body, line) {
			t.Errorf("bp-newapi.yaml ingress missing line %q\n--- rendered ---\n%s", line, body)
		}
	}

	// Admin UI gated by the PER-TENANT Keycloak realm (NOT otech-wide).
	wantAdminAuth := []string{
		"        mode: keycloak",
		"          issuer: https://keycloak.alice.omantel.omani.works/realms/sme-alice",
		"          clientId: newapi-admin",
		"          existingSecret: newapi-oidc-client-secret",
	}
	for _, line := range wantAdminAuth {
		if !strings.Contains(body, line) {
			t.Errorf("bp-newapi.yaml auth.adminUI missing line %q\n--- rendered ---\n%s", line, body)
		}
	}

	// Customer-API issuer = catalyst (Catalyst mints per-user keys; the
	// upstream's self-serve portal stays OFF on Catalyst Sovereigns).
	if !strings.Contains(body, "        keyIssuer: catalyst") {
		t.Errorf("bp-newapi.yaml customerAPI.keyIssuer must be 'catalyst' to disable the self-serve portal")
	}

	// Per-tenant database + credentials Secrets.
	wantSecrets := []string{
		"      existingSecret: newapi-pg-app",
		"      existingSecretKey: SQL_DSN",
		"      existingSecret: newapi-credentials",
	}
	for _, line := range wantSecrets {
		if !strings.Contains(body, line) {
			t.Errorf("bp-newapi.yaml DB/credentials missing line %q", line)
		}
	}

	// defaultChannels.qwenPartner — channel #1 auto-seeded by the
	// chart's post-install Helm hook Job (per #915 C4 PR #919).
	wantChannel := []string{
		"      qwenPartner:",
		"        enabled: true",
		"        name: qwen",
		// endpoint defaults to empty in the generated template; the
		// per-Sovereign overlay populates it via the operator-supplied
		// `newapi-channel-qwen-partner` Secret. See
		// docs/RUNBOOKS.md §Operator-setup.
		`        endpoint: ""`,
		"          - qwen3.6",
		"          - qwen3-coder",
		"        existingSecret: newapi-channel-qwen-partner",
		"        existingSecretKey: API_KEY",
		"          kind: commercial-contract",
	}
	for _, line := range wantChannel {
		if !strings.Contains(body, line) {
			t.Errorf("bp-newapi.yaml defaultChannels.qwenPartner missing line %q\n--- rendered ---\n%s", line, body)
		}
	}

	// MUST NOT point at the otech-wide newapi.<otech-fqdn> — that would
	// defeat per-tenant channel routing (alice + bob would share a
	// single channel set, audit log, and commercial-contract).
	if strings.Contains(body, "newapi.otech113.omani.works") {
		t.Errorf("bp-newapi.yaml must be per-tenant api.<sub>.<parent>, not otech-wide newapi: %s", body)
	}

	// kustomization.yaml MUST list bp-newapi.yaml so Flux materialises it.
	kust, ok := files["kustomization.yaml"]
	if !ok {
		t.Fatalf("kustomization.yaml missing")
	}
	if !strings.Contains(kust, "  - bp-newapi.yaml") {
		t.Errorf("kustomization.yaml resources list must include bp-newapi.yaml — got:\n%s", kust)
	}
}

// TestRenderOrganizationOverlay_NewAPIChartVersion asserts the NewAPI
// chart version is overridable via OrganizationChartVersions.NewAPI (per
// Inviolable Principle 4 — never hardcode versions in source).
func TestRenderOrganizationOverlay_NewAPIChartVersion(t *testing.T) {
	rec := store.OrganizationProvisionRecord{
		OrganizationID:  "t-alice",
		Subdomain:       "alice",
		ParentDomain:    "omantel.omani.works",
		DomainMode:      store.OrganizationDomainFreeSubdomain,
		AdminEmail:      "admin@alice.test",
		OTECHFQDN:       "otech113.omani.works",
		VClusterName:    "vc-alice",
		TenantNamespace: "sme-t-alice",
	}
	versions := OrganizationChartVersions{NewAPI: "1.3.0"}
	files, err := renderOrganizationOverlay(rec, versions)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(files["bp-newapi.yaml"], `version: "1.3.0"`) {
		t.Errorf("bp-newapi version override missing — got:\n%s", files["bp-newapi.yaml"])
	}
}

// #915 (D1) — bp-wordpress-tenant.yaml MUST emit per-tenant OIDC values
// so the chart's post-install wp-cli Job seeds openid-connect-generic
// against the per-tenant Keycloak realm. PR #918 registers the matching
// `wordpress` client + Secret on the bp-keycloak side; this test pins
// the orchestrator-side contract that consumes them.
func TestRenderOrganizationOverlay_WordPressEmitsOIDC(t *testing.T) {
	rec := store.OrganizationProvisionRecord{
		OrganizationID:  "t-alice",
		Subdomain:       "alice",
		ParentDomain:    "omantel.omani.works",
		DomainMode:      store.OrganizationDomainFreeSubdomain,
		AdminEmail:      "admin@alice.test",
		CompanyName:     "Alice Corp",
		OTECHFQDN:       "otech107.omani.works",
		VClusterName:    "vc-alice",
		TenantNamespace: "sme-t-alice",
	}
	files, err := renderOrganizationOverlay(rec, OrganizationChartVersions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body, ok := files["bp-wordpress-tenant.yaml"]
	if !ok {
		t.Fatalf("bp-wordpress-tenant.yaml missing from rendered overlay")
	}
	// Canonical oidc.* block — chart >= 0.2.0 contract.
	wantOIDC := []string{
		"    oidc:",
		"      enabled: true",
		"      issuerURL: https://keycloak.alice.omantel.omani.works/realms/sme-alice",
		"      clientId: wordpress",
		"      clientSecretName: wordpress-oidc-client-secret",
		"      defaultRole: subscriber",
		"      identityKey: preferred_username",
	}
	for _, line := range wantOIDC {
		if !strings.Contains(body, line) {
			t.Errorf("bp-wordpress-tenant oidc block missing line %q\n--- rendered ---\n%s", line, body)
		}
	}
	// Legacy keycloak.* alias — chart 0.1.x back-compat. Removed in 0.3.0.
	wantLegacy := []string{
		"    keycloak:",
		"      realmURL: https://keycloak.alice.omantel.omani.works/realms/sme-alice",
		"      clientID: wordpress",
		"      clientSecretName: wordpress-oidc-client-secret",
	}
	for _, line := range wantLegacy {
		if !strings.Contains(body, line) {
			t.Errorf("bp-wordpress-tenant legacy keycloak block missing line %q (back-compat)", line)
		}
	}
	// Per-tenant realm URL MUST be the per-SME Keycloak, not a shared
	// otech-level IdP. Same guardrail as the OpenClaw/Stalwart tests.
	bad := "https://keycloak.otech107.omani.works"
	if strings.Contains(body, bad) {
		t.Errorf("bp-wordpress-tenant issuerURL must be per-tenant, not otech-wide: %s", body)
	}
	// Ingress + admin-user blocks are unchanged (kept here so a regression
	// to those surfaces in the same test).
	if !strings.Contains(body, "host: wordpress.alice.omantel.omani.works") {
		t.Errorf("wordpress ingress host missing")
	}
	if !strings.Contains(body, "email: admin@alice.test") {
		t.Errorf("admin email missing")
	}
	// dependsOn: bp-keycloak so the wp-cli Job runs AFTER the realm
	// import + Secret materialisation.
	if !strings.Contains(body, "name: bp-keycloak") {
		t.Errorf("expected dependsOn bp-keycloak in bp-wordpress-tenant.yaml")
	}
}

// #3785 (Refs #3376 #3761) — the WordPress HelmRelease MUST set
// global.imageRegistry to the Sovereign Harbor DockerHub proxy-cache so the
// chart's main + wp-cli images (Docker Hub `wordpress`) route through
// `<registry>/proxy-dockerhub/...` and pass the harbor-proxy-pull Kyverno
// ClusterPolicy (Enforce). Without this the customer's purchased app is
// admission-denied and never Runs — the funnel's terminal acceptance.
func TestRenderOrganizationOverlay_WordPressImageProxiedThroughHarbor(t *testing.T) {
	rec := store.OrganizationProvisionRecord{
		OrganizationID:  "t-alice",
		Subdomain:       "alice",
		ParentDomain:    "omantel.omani.works",
		DomainMode:      store.OrganizationDomainFreeSubdomain,
		AdminEmail:      "admin@alice.test",
		CompanyName:     "Alice Corp",
		OTECHFQDN:       "otech107.omani.works",
		VClusterName:    "vc-alice",
		TenantNamespace: "sme-t-alice",
	}

	// Default registry (env unset) → harbor.openova.io.
	t.Setenv("CATALYST_VCLUSTER_IMAGE_REGISTRY", "")
	files, err := renderOrganizationOverlay(rec, OrganizationChartVersions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body := files["bp-wordpress-tenant.yaml"]
	if !strings.Contains(body, "    global:") ||
		!strings.Contains(body, "      imageRegistry: harbor.openova.io/proxy-dockerhub") {
		t.Fatalf("WordPress HR must proxy images via global.imageRegistry harbor.openova.io/proxy-dockerhub\n--- rendered ---\n%s", body)
	}

	// Operator override (Principle #4) → the post-cutover Harbor host.
	t.Setenv("CATALYST_VCLUSTER_IMAGE_REGISTRY", "harbor.alice.omantel.omani.works")
	files2, err := renderOrganizationOverlay(rec, OrganizationChartVersions{})
	if err != nil {
		t.Fatalf("render (override): %v", err)
	}
	if !strings.Contains(files2["bp-wordpress-tenant.yaml"],
		"      imageRegistry: harbor.alice.omantel.omani.works/proxy-dockerhub") {
		t.Errorf("CATALYST_VCLUSTER_IMAGE_REGISTRY override not honoured in WordPress HR\n%s",
			files2["bp-wordpress-tenant.yaml"])
	}
}

// #915 (D1) — BYO domain mode must emit OIDC against the BYO host, not
// the otech default zone. Mirrors the BYO certificate-emission test for
// wordpress.
func TestRenderOrganizationOverlay_WordPressOIDC_BYOMode(t *testing.T) {
	rec := store.OrganizationProvisionRecord{
		OrganizationID:  "t-acme",
		Subdomain:       "acme",
		DomainMode:      store.OrganizationDomainBYO,
		BYODomain:       "acme.com",
		AdminEmail:      "admin@acme.com",
		OTECHFQDN:       "otech.example",
		VClusterName:    "vc-acme",
		TenantNamespace: "sme-t-acme",
	}
	files, err := renderOrganizationOverlay(rec, OrganizationChartVersions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body := files["bp-wordpress-tenant.yaml"]
	// BYO ingress host is wordpress.acme.com (derived from BYO domain).
	if !strings.Contains(body, "host: wordpress.acme.com") {
		t.Errorf("byo wordpress host missing — got:\n%s", body)
	}
	// smeDomain (BYO mode) is the bare BYO domain. The producer keeps the
	// chart-consumed data-value key smeDomain (#3383 WIRE-STABLE invariant);
	// the bp-wordpress-tenant chart reads .Values.smeDomain.
	if !strings.Contains(body, "smeDomain: acme.com") {
		t.Errorf("byo smeDomain missing")
	}
}

// #915 (C2) — bp-stalwart-tenant.yaml MUST emit per-tenant Keycloak OIDC
// values so the chart's setup Job seeds the OIDC directory entry against
// the per-tenant Keycloak realm. Without these the chart falls back to
// its empty default `keycloak.realmURL` and Stalwart's webmail / IMAP /
// SMTP login flow can't reach Keycloak.
func TestRenderOrganizationOverlay_StalwartEmitsKeycloakOIDC(t *testing.T) {
	rec := store.OrganizationProvisionRecord{
		OrganizationID:  "t-acme",
		Subdomain:       "acme",
		DomainMode:      store.OrganizationDomainFreeSubdomain,
		AdminEmail:      "admin@acme.test",
		CompanyName:     "Acme Corp",
		OTECHFQDN:       "omantel.omani.works",
		VClusterName:    "vc-acme",
		TenantNamespace: "sme-t-acme",
	}
	files, err := renderOrganizationOverlay(rec, OrganizationChartVersions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body, ok := files["bp-stalwart-tenant.yaml"]
	if !ok {
		t.Fatalf("bp-stalwart-tenant.yaml missing")
	}
	// Per-tenant realm URL — must point at the SME's vcluster Keycloak,
	// not a shared otech-level IdP.
	wantRealmURL := "https://keycloak.acme.omantel.omani.works/realms/sme-acme"
	if !strings.Contains(body, wantRealmURL) {
		t.Errorf("realmURL missing — want %s in body", wantRealmURL)
	}
	// Confidential client ID + ExternalSecret-store remoteRef path.
	for _, want := range []string{
		"clientID: stalwart",
		"clientSecretName: stalwart-oidc-client-secret",
		"sovereign/omantel.omani.works/stalwart/t-acme/oidc",
		"sovereign/omantel.omani.works/stalwart/t-acme/admin",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in bp-stalwart-tenant.yaml — got:\n%s", want, body)
		}
	}
	// dependsOn: bp-keycloak so the Helm install order is deterministic.
	if !strings.Contains(body, "name: bp-keycloak") {
		t.Errorf("expected dependsOn bp-keycloak in bp-stalwart-tenant.yaml")
	}
	// Setup Job MUST be enabled — that's what seeds the OIDC directory
	// into Stalwart's runtime settings store at t=0.
	if !strings.Contains(body, "setupJob:\n        enabled: true") {
		t.Errorf("expected mailboxProvisioner.setupJob.enabled=true")
	}
	// Webmail ingress host correctly composed for free-subdomain.
	if !strings.Contains(body, "host: mail.acme.omantel.omani.works") {
		t.Errorf("mail host missing in bp-stalwart-tenant.yaml")
	}
}

func TestStepsForState(t *testing.T) {
	cases := []struct {
		state store.OrganizationProvisionState
		err   string
		want  string // serialised steps for sanity
	}{
		{store.STSDone, "", `{"vcluster":"done","bp_charts":"done","dns":"done","certs":"done","keycloak_clients":"done","registry":"done"}`},
		{store.STSDNSProvisioned, "", `{"vcluster":"done","bp_charts":"done","dns":"done","certs":"pending","keycloak_clients":"pending","registry":"pending"}`},
		{store.STSFailed, "dns:transient:powerdns down", `{"vcluster":"done","bp_charts":"done","dns":"failed","certs":"pending","keycloak_clients":"pending","registry":"pending"}`},
		{store.STSFailed, "registry:terminal:nope", `{"vcluster":"done","bp_charts":"done","dns":"done","certs":"done","keycloak_clients":"done","registry":"failed"}`},
	}
	for _, tc := range cases {
		got := stepsForState(tc.state, tc.err)
		raw, _ := json.Marshal(got)
		if string(raw) != tc.want {
			t.Errorf("state=%s err=%s: want %s got %s", tc.state, tc.err, tc.want, raw)
		}
	}
}

// Verify Keycloak client probe handles 404 + present clients correctly.
func TestVerifyKeycloakClients_Present(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/admin/realms/sme-acme/clients") {
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`[{"clientId":"catalyst-ui"},{"clientId":"wordpress"},{"clientId":"openclaw"},{"clientId":"stalwart"}]`))
	}))
	defer srv.Close()
	missing, err := verifyKeycloakClients(context.Background(), srv.Client(), srv.URL, "sme-acme", "tok",
		[]string{"catalyst-ui", "wordpress", "openclaw", "stalwart"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(missing) != 0 {
		t.Errorf("missing: %v", missing)
	}
}

func TestVerifyKeycloakClients_Missing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"clientId":"catalyst-ui"}]`))
	}))
	defer srv.Close()
	missing, err := verifyKeycloakClients(context.Background(), srv.Client(), srv.URL, "sme-acme", "tok",
		[]string{"catalyst-ui", "wordpress"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(missing) != 1 || missing[0] != "wordpress" {
		t.Errorf("missing: %v", missing)
	}
}

// Verifies the BYO CNAME validator's success/failure shapes against a
// stub Resolver.
type stubResolver struct {
	cname string
	err   error
}

func (s stubResolver) LookupCNAME(_ context.Context, _ string) (string, error) {
	return s.cname, s.err
}

func TestValidateBYOCNAME_Success(t *testing.T) {
	p := DefaultOrganizationDNSProvisioner{Resolver: stubResolver{cname: "ingress.otech.example."}}
	if err := p.ValidateBYOCNAME(context.Background(), "acme.com", "otech.example"); err != nil {
		t.Errorf("expected nil err got %v", err)
	}
}

func TestValidateBYOCNAME_Mismatch(t *testing.T) {
	p := DefaultOrganizationDNSProvisioner{Resolver: stubResolver{cname: "elsewhere.invalid."}}
	if err := p.ValidateBYOCNAME(context.Background(), "acme.com", "otech.example"); err == nil {
		t.Errorf("expected error for mismatched CNAME")
	}
}

// Multi-domain Sovereign (#828): a CNAME pointing at any parent in
// the role:sme-pool list MUST validate, not just OTECHFQDN.
func TestValidateBYOCNAME_MultiDomainAccepted(t *testing.T) {
	// Resolver returns a CNAME ending in omani.trade — one of the
	// pool entries, not the legacy primary OTECHFQDN.
	p := DefaultOrganizationDNSProvisioner{Resolver: stubResolver{cname: "ingress.omani.trade."}}
	err := p.ValidateBYOCNAME(context.Background(), "acme.com", "otech.example",
		"otech.example", "omani.works", "omani.trade")
	if err != nil {
		t.Errorf("expected nil err for multi-domain match got %v", err)
	}
}

// And: a CNAME pointing at NONE of the parents in the pool fails.
func TestValidateBYOCNAME_MultiDomainRejected(t *testing.T) {
	p := DefaultOrganizationDNSProvisioner{Resolver: stubResolver{cname: "ingress.attacker.invalid."}}
	err := p.ValidateBYOCNAME(context.Background(), "acme.com", "otech.example",
		"otech.example", "omani.works", "omani.trade")
	if err == nil {
		t.Errorf("expected mismatch error when CNAME doesn't match any pool entry")
	}
}

// Quick sanity that the PowerDNS writer returns a graceful error
// when the upstream PATCH 4xx's (no panic, useful detail).
func TestPowerDNSWriter_PATCH_NonOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no zone", http.StatusNotFound)
	}))
	defer srv.Close()
	w := NewPowerDNSWriter(srv.URL, "key")
	if w == nil {
		t.Fatal("writer nil")
	}
	err := w.PatchRRSets(context.Background(), "otech.example", []pdnsRRSet{
		{Name: "console.acme.otech.example.", Type: "A", TTL: 300, ChangeType: "REPLACE"},
	})
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("expected 404 error got %v", err)
	}
}

// Read-once ioutil-equivalent helper for body inspection in tests.
func readAll(r io.Reader) string {
	b, _ := io.ReadAll(r)
	return string(b)
}

// keep readAll unused-warning at bay for some builds.
var _ = readAll

/* ── Multi-domain Sovereign tests (epic #825 / MD-3 #828) ─────────── */

// newTestHandlerWithMultiDomainPool spins up a Handler whose SME-tenant
// deps include a 2-entry sme-pool (omani.works ready + omani.trade
// ready) plus a primary OTECHFQDN — exercising the full #828 path.
func newTestHandlerWithMultiDomainPool(t *testing.T, pool []OrganizationParentDomain) (*Handler, *fakeGitOps, *fakeDNS, *fakeKCClients, *fakeTenantEmitter, *store.TenantRegistry) {
	t.Helper()
	dir := t.TempDir()
	tenantStore, err := store.NewOrganizationProvisionStore(dir)
	if err != nil {
		t.Fatalf("tenant store: %v", err)
	}
	registry, err := store.NewTenantRegistry(dir)
	if err != nil {
		t.Fatalf("tenant registry: %v", err)
	}
	gitops := &fakeGitOps{}
	dns := &fakeDNS{}
	kc := &fakeKCClients{}
	emitter := &fakeTenantEmitter{}
	h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h.SetTenantRegistry(registry)
	h.SetOrganizationDeps(OrganizationDeps{
		Store:            tenantStore,
		GitOps:           gitops,
		DNS:              dns,
		KeycloakClients:  kc,
		Events:           emitter,
		TenantRegistry:   registry,
		OTECHFQDN:        "otech.example",
		OTECHIngressIPv4: "192.0.2.10",
		ParentDomains:    pool,
		MaxRetryCount:    5,
	})
	return h, gitops, dns, kc, emitter, registry
}

// TestCreateOrganization_MultiDomain_OperatorPicksPool — the operator
// sets parent_domain=omani.trade. The resulting console host is
// console.acme.omani.trade and the DNS provisioner writes under that
// zone (NOT the primary OTECHFQDN).
func TestCreateOrganization_MultiDomain_OperatorPicksPool(t *testing.T) {
	h, _, dns, _, _, registry := newTestHandlerWithMultiDomainPool(t, []OrganizationParentDomain{
		{Name: "otech.example", Role: "primary", NSFlipReady: true},
		{Name: "omani.works", Role: "sme-pool", NSFlipReady: true},
		{Name: "omani.trade", Role: "sme-pool", NSFlipReady: true},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/organizations",
		bytes.NewReader([]byte(`{
			"subdomain":     "acme",
			"admin_email":   "admin@acme.test",
			"domain_mode":   "free-subdomain",
			"parent_domain": "omani.trade"
		}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleCreateOrganization(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	var got orgTenantResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ParentDomain != "omani.trade" {
		t.Errorf("parent_domain in response: %q want omani.trade", got.ParentDomain)
	}
	if got.ConsoleHost != "console.acme.omani.trade" {
		t.Errorf("console_host: %q want console.acme.omani.trade", got.ConsoleHost)
	}
	if len(dns.provisionCalls) != 1 || dns.provisionCalls[0] != "acme:omani.trade:192.0.2.10" {
		t.Errorf("dns provision call shape: %v", dns.provisionCalls)
	}
	if _, ok := registry.Get("console.acme.omani.trade"); !ok {
		t.Errorf("registry lookup miss for console.acme.omani.trade")
	}
}

// TestCreateOrganization_MultiDomain_DefaultsToFirstReady — when the
// operator omits parent_domain on a multi-entry pool the orchestrator
// picks the first NS-flip-ready sme-pool entry.
func TestCreateOrganization_MultiDomain_DefaultsToFirstReady(t *testing.T) {
	h, _, _, _, _, _ := newTestHandlerWithMultiDomainPool(t, []OrganizationParentDomain{
		{Name: "otech.example", Role: "primary", NSFlipReady: true},
		{Name: "omani.works", Role: "sme-pool", NSFlipReady: true},
		{Name: "omani.trade", Role: "sme-pool", NSFlipReady: true},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/organizations",
		bytes.NewReader([]byte(`{
			"subdomain":   "acme",
			"admin_email": "admin@acme.test",
			"domain_mode": "free-subdomain"
		}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleCreateOrganization(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	var got orgTenantResponse
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got.ParentDomain != "omani.works" {
		t.Errorf("default parent_domain: %q want omani.works (first ready sme-pool entry)", got.ParentDomain)
	}
}

// TestCreateOrganization_MultiDomain_RejectsUnknownParent — picking a
// parent that isn't in the sme-pool list = 400.
func TestCreateOrganization_MultiDomain_RejectsUnknownParent(t *testing.T) {
	h, _, _, _, _, _ := newTestHandlerWithMultiDomainPool(t, []OrganizationParentDomain{
		{Name: "omani.works", Role: "sme-pool", NSFlipReady: true},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/organizations",
		bytes.NewReader([]byte(`{
			"subdomain":     "acme",
			"admin_email":   "admin@acme.test",
			"domain_mode":   "free-subdomain",
			"parent_domain": "evil.example"
		}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleCreateOrganization(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: %d body=%s want 400", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "parent-domain-invalid") {
		t.Errorf("body: %s", w.Body.String())
	}
}

// TestCreateOrganization_MultiDomain_RejectsNotReady — picking a parent
// whose NS-flip is still pending returns 503 + Retry-After.
func TestCreateOrganization_MultiDomain_RejectsNotReady(t *testing.T) {
	h, _, _, _, _, _ := newTestHandlerWithMultiDomainPool(t, []OrganizationParentDomain{
		{Name: "omani.works", Role: "sme-pool", NSFlipReady: true},
		{Name: "omani.trade", Role: "sme-pool", NSFlipReady: false},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/organizations",
		bytes.NewReader([]byte(`{
			"subdomain":     "acme",
			"admin_email":   "admin@acme.test",
			"domain_mode":   "free-subdomain",
			"parent_domain": "omani.trade"
		}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleCreateOrganization(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: %d body=%s want 503", w.Code, w.Body.String())
	}
	if w.Header().Get("Retry-After") == "" {
		t.Errorf("Retry-After header missing")
	}
	if !strings.Contains(w.Body.String(), "ns-flip-pending") {
		t.Errorf("body: %s", w.Body.String())
	}
}

// TestLoadOrganizationParentDomainsFromEnv_StubFallback — when the env
// knob is unset and OTECH_FQDN is set, the env loader returns the
// hardcoded 4-domain stub (omani.homes + omani.rest + omani.trade +
// omani.works) per DoD D30 (issue #1830). The pool entries mirror
// core/services/domain/store.AllowedTLDs and the marketplace
// /addons subdomain picker. The historical 2-entry stub (#828, MD-1
// in flight) was widened on 2026-05-18 to surface every TLD the
// customer-facing UI offers — keeping seed + UI + AllowedTLDs locked
// together prevents 422s on signup when the picker lists a TLD the
// catalyst-api validator doesn't recognise.
func TestLoadOrganizationParentDomainsFromEnv_StubFallback(t *testing.T) {
	t.Setenv("CATALYST_ORG_POOL_DOMAINS", "")
	t.Setenv("CATALYST_OTECH_FQDN", "otech.example")
	got := LoadOrganizationParentDomainsFromEnv()

	primary := 0
	pool := 0
	for _, p := range got {
		switch p.Role {
		case "primary":
			primary++
		case "sme-pool":
			pool++
		}
	}
	if primary != 1 {
		t.Errorf("primary: want 1 got %d (%v)", primary, got)
	}
	if pool != 4 {
		t.Errorf("sme-pool: want 4 got %d (%v)", pool, got)
	}
}

// TestLoadOrganizationParentDomainsFromEnv_Custom — operator-supplied
// CATALYST_ORG_POOL_DOMAINS overrides the stub.
func TestLoadOrganizationParentDomainsFromEnv_Custom(t *testing.T) {
	t.Setenv("CATALYST_ORG_POOL_DOMAINS", "acme.io:primary,acme.shop,acme.cloud")
	got := LoadOrganizationParentDomainsFromEnv()
	if len(got) != 3 {
		t.Fatalf("items: want 3 got %d (%v)", len(got), got)
	}
	if got[0].Name != "acme.io" || got[0].Role != "primary" {
		t.Errorf("primary entry: %+v", got[0])
	}
	for _, p := range got[1:] {
		if p.Role != "sme-pool" {
			t.Errorf("non-primary entry should be sme-pool: %+v", p)
		}
	}
}
