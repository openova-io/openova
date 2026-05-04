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
	writes   []store.SMETenantProvisionRecord
	deletes  []store.SMETenantProvisionRecord
	failNext bool
	terminal bool // when true return a terminal-class error
}

func (f *fakeGitOps) WriteTenantOverlay(_ context.Context, rec store.SMETenantProvisionRecord) (string, error) {
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
	return "sha-" + rec.SMETenantID, nil
}

func (f *fakeGitOps) DeleteTenantOverlay(_ context.Context, rec store.SMETenantProvisionRecord) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes = append(f.deletes, rec)
	return "sha-del-" + rec.SMETenantID, nil
}

// fakeDNS records calls + returns canned responses.
type fakeDNS struct {
	mu             sync.Mutex
	provisionCalls []string
	cnameCalls     []string
	provisionErr   error
	cnameErr       error
}

func (f *fakeDNS) ProvisionFreeSubdomain(_ context.Context, sub, otech, ip string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.provisionCalls = append(f.provisionCalls, sub+":"+otech+":"+ip)
	return f.provisionErr
}

func (f *fakeDNS) ValidateBYOCNAME(_ context.Context, byo, otech string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cnameCalls = append(f.cnameCalls, byo+"->"+otech)
	return f.cnameErr
}

// fakeKCClients tracks calls.
type fakeKCClients struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (f *fakeKCClients) ProvisionSMEClients(_ context.Context, rec store.SMETenantProvisionRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, rec.SMETenantID)
	return f.err
}

// fakeTenantEmitter records events.
type fakeTenantEmitter struct {
	mu      sync.Mutex
	created []store.SMETenantProvisionRecord
	deleted []store.SMETenantProvisionRecord
	changed []store.SMETenantProvisionRecord
}

func (f *fakeTenantEmitter) EmitSMETenantCreated(_ context.Context, r store.SMETenantProvisionRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, r)
	return nil
}
func (f *fakeTenantEmitter) EmitSMETenantStateChanged(_ context.Context, r store.SMETenantProvisionRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.changed = append(f.changed, r)
	return nil
}
func (f *fakeTenantEmitter) EmitSMETenantDeleted(_ context.Context, r store.SMETenantProvisionRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, r)
	return nil
}

func newTestHandlerWithSMETenantDeps(t *testing.T) (*Handler, *fakeGitOps, *fakeDNS, *fakeKCClients, *fakeTenantEmitter, *store.TenantRegistry) {
	t.Helper()
	dir := t.TempDir()
	tenantStore, err := store.NewSMETenantProvisionStore(dir)
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
	h.SetSMETenantDeps(SMETenantDeps{
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

func TestCreateSMETenant_HappyPathFreeSubdomain(t *testing.T) {
	h, gitops, dns, kc, emitter, registry := newTestHandlerWithSMETenantDeps(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sme/tenants",
		bytes.NewReader([]byte(`{
			"subdomain":    "acme",
			"admin_email":  "admin@acme.test",
			"company_name": "Acme",
			"domain_mode":  "free-subdomain"
		}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleCreateSMETenant(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status: want 202 got %d body=%s", w.Code, w.Body.String())
	}
	var got smeTenantResponse
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
	if reg.SMETenantNamespace == "" || !strings.HasPrefix(reg.SMETenantNamespace, "sme-") {
		t.Errorf("registry namespace: %s", reg.SMETenantNamespace)
	}
}

func TestCreateSMETenant_BYOValidationSuccess(t *testing.T) {
	h, _, dns, _, _, registry := newTestHandlerWithSMETenantDeps(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sme/tenants",
		bytes.NewReader([]byte(`{
			"subdomain":   "acme",
			"admin_email": "admin@acme.com",
			"domain_mode": "byo",
			"byo_domain":  "acme.com"
		}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleCreateSMETenant(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	if got := dns.cnameCalls; len(got) != 1 || got[0] != "acme.com->otech.example" {
		t.Errorf("byo cname call shape: %v", got)
	}
	if _, ok := registry.Get("console.acme.com"); !ok {
		t.Errorf("byo tenant not registered")
	}
}

func TestCreateSMETenant_BYOValidationFailureMarksTerminal(t *testing.T) {
	h, _, dns, _, _, registry := newTestHandlerWithSMETenantDeps(t)
	dns.cnameErr = errBYOCNAMEMismatch

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sme/tenants",
		bytes.NewReader([]byte(`{
			"subdomain":   "acme",
			"admin_email": "admin@acme.com",
			"domain_mode": "byo",
			"byo_domain":  "acme.com"
		}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleCreateSMETenant(w, req)

	var got smeTenantResponse
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

func TestCreateSMETenant_GitOpsTransientFailure_Retryable(t *testing.T) {
	h, gitops, _, _, _, _ := newTestHandlerWithSMETenantDeps(t)
	// First call fails transiently; reconcile should retry and succeed.
	gitops.failNext = true

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sme/tenants",
		bytes.NewReader([]byte(`{
			"subdomain":   "acme",
			"admin_email": "admin@acme.com"
		}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleCreateSMETenant(w, req)

	var first smeTenantResponse
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

	r2 := httptest.NewRequest(http.MethodGet, "/api/v1/sme/tenants/"+first.SMETenantID, nil)
	r2.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", first.SMETenantID)
	r2 = r2.WithContext(context.WithValue(r2.Context(), chi.RouteCtxKey, rctx))
	w2 := httptest.NewRecorder()
	h.HandleGetSMETenant(w2, r2)
	var second smeTenantResponse
	_ = json.Unmarshal(w2.Body.Bytes(), &second)
	if second.State != store.STSDone {
		t.Fatalf("after reconcile: want done got %s lastError=%s", second.State, second.LastError)
	}
}

func TestCreateSMETenant_ValidationErrors(t *testing.T) {
	h, _, _, _, _, _ := newTestHandlerWithSMETenantDeps(t)
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
			req := httptest.NewRequest(http.MethodPost, "/api/v1/sme/tenants",
				bytes.NewReader([]byte(tc.body)))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.HandleCreateSMETenant(w, req)
			if w.Code != tc.want {
				t.Errorf("status: want %d got %d body=%s", tc.want, w.Code, w.Body.String())
			}
		})
	}
}

func TestCreateSMETenant_NoStoreReturns503(t *testing.T) {
	h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h.SetSMETenantDeps(SMETenantDeps{}) // empty -> no store
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sme/tenants",
		bytes.NewReader([]byte(`{"subdomain":"acme","admin_email":"a@b.test"}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleCreateSMETenant(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("want 503 got %d", w.Code)
	}
}

func TestCreateSMETenant_OTECHFQDNUnconfiguredReturns503(t *testing.T) {
	dir := t.TempDir()
	tenantStore, _ := store.NewSMETenantProvisionStore(dir)
	h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h.SetSMETenantDeps(SMETenantDeps{Store: tenantStore})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sme/tenants",
		bytes.NewReader([]byte(`{"subdomain":"acme","admin_email":"a@b.test"}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleCreateSMETenant(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("want 503 got %d body=%s", w.Code, w.Body.String())
	}
}

func TestListSMETenants(t *testing.T) {
	h, _, _, _, _, _ := newTestHandlerWithSMETenantDeps(t)
	for _, sub := range []string{"acme", "globex"} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/sme/tenants",
			bytes.NewReader([]byte(`{"subdomain":"`+sub+`","admin_email":"admin@`+sub+`.test"}`)))
		req.Header.Set("Content-Type", "application/json")
		h.HandleCreateSMETenant(httptest.NewRecorder(), req)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sme/tenants", nil)
	w := httptest.NewRecorder()
	h.HandleListSMETenants(w, req)
	var got struct {
		Items []smeTenantResponse `json:"items"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if len(got.Items) != 2 {
		t.Errorf("items: want 2 got %d", len(got.Items))
	}
}

func TestDeleteSMETenant_RemovesFromRegistry(t *testing.T) {
	h, gitops, _, _, _, registry := newTestHandlerWithSMETenantDeps(t)

	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/sme/tenants",
		bytes.NewReader([]byte(`{"subdomain":"acme","admin_email":"a@b.test"}`)))
	createReq.Header.Set("Content-Type", "application/json")
	createW := httptest.NewRecorder()
	h.HandleCreateSMETenant(createW, createReq)
	var created smeTenantResponse
	_ = json.Unmarshal(createW.Body.Bytes(), &created)

	// Sanity: registry has the host.
	if _, ok := registry.Get("console.acme.otech.example"); !ok {
		t.Fatalf("precondition: registry missing host")
	}

	delReq := httptest.NewRequest(http.MethodDelete,
		"/api/v1/sme/tenants/"+created.SMETenantID, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", created.SMETenantID)
	delReq = delReq.WithContext(context.WithValue(delReq.Context(), chi.RouteCtxKey, rctx))
	delW := httptest.NewRecorder()
	h.HandleDeleteSMETenant(delW, delReq)
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

func TestRenderSMETenantOverlay_FreeSubdomain_AllChartsPresent(t *testing.T) {
	rec := store.SMETenantProvisionRecord{
		SMETenantID:     "t-acme",
		Subdomain:       "acme",
		DomainMode:      store.SMEDomainFreeSubdomain,
		AdminEmail:      "admin@acme.test",
		CompanyName:     "Acme Corp",
		OTECHFQDN:       "otech.example",
		VClusterName:    "vc-acme",
		TenantNamespace: "sme-t-acme",
	}
	files, err := renderSMETenantOverlay(rec, SMETenantChartVersions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, name := range []string{
		"kustomization.yaml",
		"namespace.yaml",
		"vcluster.yaml",
		"bp-keycloak.yaml",
		"bp-cnpg.yaml",
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

func TestRenderSMETenantOverlay_BYO_EmitsCertificate(t *testing.T) {
	rec := store.SMETenantProvisionRecord{
		SMETenantID:     "t-acme",
		Subdomain:       "acme",
		DomainMode:      store.SMEDomainBYO,
		BYODomain:       "acme.com",
		AdminEmail:      "admin@acme.com",
		OTECHFQDN:       "otech.example",
		VClusterName:    "vc-acme",
		TenantNamespace: "sme-t-acme",
	}
	files, err := renderSMETenantOverlay(rec, SMETenantChartVersions{})
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

func TestRenderSMETenantOverlay_VersionsApplied(t *testing.T) {
	rec := store.SMETenantProvisionRecord{
		SMETenantID:     "t-acme",
		Subdomain:       "acme",
		DomainMode:      store.SMEDomainFreeSubdomain,
		AdminEmail:      "admin@acme.test",
		OTECHFQDN:       "otech.example",
		VClusterName:    "vc-acme",
		TenantNamespace: "sme-t-acme",
	}
	versions := SMETenantChartVersions{
		Keycloak: "1.2.3", CNPG: "0.5.0", WordPress: "0.1.0", OpenClaw: "0.1.0", Stalwart: "0.1.0",
	}
	files, err := renderSMETenantOverlay(rec, versions)
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

func TestRenderSMETenantOverlay_NoVersionsDefaultsToStar(t *testing.T) {
	rec := store.SMETenantProvisionRecord{
		SMETenantID:     "t-acme",
		Subdomain:       "acme",
		DomainMode:      store.SMEDomainFreeSubdomain,
		AdminEmail:      "admin@acme.test",
		OTECHFQDN:       "otech.example",
		VClusterName:    "vc-acme",
		TenantNamespace: "sme-t-acme",
	}
	files, _ := renderSMETenantOverlay(rec, SMETenantChartVersions{})
	if !strings.Contains(files["bp-keycloak.yaml"], `version: "*"`) {
		t.Errorf("expected default '*' version: %s", files["bp-keycloak.yaml"])
	}
}

func TestStepsForState(t *testing.T) {
	cases := []struct {
		state store.SMETenantProvisionState
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
	p := DefaultSMETenantDNSProvisioner{Resolver: stubResolver{cname: "ingress.otech.example."}}
	if err := p.ValidateBYOCNAME(context.Background(), "acme.com", "otech.example"); err != nil {
		t.Errorf("expected nil err got %v", err)
	}
}

func TestValidateBYOCNAME_Mismatch(t *testing.T) {
	p := DefaultSMETenantDNSProvisioner{Resolver: stubResolver{cname: "elsewhere.invalid."}}
	if err := p.ValidateBYOCNAME(context.Background(), "acme.com", "otech.example"); err == nil {
		t.Errorf("expected error for mismatched CNAME")
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
