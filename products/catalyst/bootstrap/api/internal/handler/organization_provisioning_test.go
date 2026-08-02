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
	deproviCalls   []string
	cnameCalls     []string
	provisionErr   error
	deprovisionErr error
	cnameErr       error
}

func (f *fakeDNS) ProvisionFreeSubdomain(_ context.Context, sub, parent, ip, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.provisionCalls = append(f.provisionCalls, sub+":"+parent+":"+ip)
	return f.provisionErr
}

func (f *fakeDNS) DeprovisionFreeSubdomain(_ context.Context, sub, parent string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deproviCalls = append(f.deproviCalls, sub+":"+parent)
	return f.deprovisionErr
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
	// #5501 — the happy path is that every ORCHESTRATOR-side side effect ran
	// (asserted below: overlay committed, DNS written, Keycloak clients
	// created, registry row present). It is NOT that the Organization is
	// provisioned: the boundary is authored downstream by the org-controller
	// and this handler has no in-cluster client here, so the substrate was
	// never observed. Terminal success over an unobserved substrate is the
	// defect (`state:"done"` in zero seconds over an empty cluster), so the
	// record holds at the highest non-terminal state.
	if got.State != store.STSTenantRegistered {
		t.Fatalf("state: want %s (all side effects committed, boundary unobserved) got %s (lastError=%s)",
			store.STSTenantRegistered, got.State, got.LastError)
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
	if reg.TenantKind != store.TenantKindOrg {
		t.Errorf("registry kind: %s", reg.TenantKind)
	}
	// #4290 — the per-Organization host namespace is the org-controller-owned
	// `<slug>` (the single boundary), NOT a stray `org-<uuid>`. The registry
	// row must reference that `<slug>` namespace so day-2 surfaces resolve to
	// the boundary the org-controller actually builds.
	if reg.OrganizationNamespace != "acme" {
		t.Errorf("registry namespace: want <slug> \"acme\" got %q", reg.OrganizationNamespace)
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
	// The retry is proven by the record having MOVED PAST the failing gitops
	// step to the end of the orchestrator's own ladder. It does not reach
	// STSDone here because no in-cluster client exists to observe the
	// boundary (#5501) — the terminal promotion is exercised against an
	// observed-Ready CR in org_create_fake_green_5501_test.go.
	if second.State != store.STSTenantRegistered {
		t.Fatalf("after reconcile: want %s got %s lastError=%s", store.STSTenantRegistered, second.State, second.LastError)
	}
	if second.LastError != "" {
		t.Errorf("after a successful retry last_error must be cleared, got %q", second.LastError)
	}
	if second.CommitSHA == "" {
		t.Errorf("after a successful retry the overlay commit SHA must be recorded")
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

// TestRenderOrganizationOverlay_AgenityChartVersionBounded is the #4922 guard.
// The bp-agenity per-Org HelmRelease MUST pin a BOUNDED SemVer range
// (>=0.5.0 <0.6.0), never the unbounded "*". The bp-agenity CHART OCI repo was
// historically squatted by the dashboard IMAGE (tag == appVersion 0.9.7, a
// 1-descriptor Docker manifest) before #4706 moved the image to
// ghcr.io/openova-io/agenity. A "*" pin resolves the HIGHEST semver tag, so
// that lingering image (0.9.7 > 0.5.20 chart) out-ranks the real chart and
// Flux dies with "manifest does not contain minimum number of descriptors (2),
// found: 1" — bp-agenity never installs (rows 218/219). The bound makes the
// resolution deterministic even before the squatting tags are pruned. An
// explicit CATALYST_ORG_BP_AGENITY_VER still wins verbatim.
func TestRenderOrganizationOverlay_AgenityChartVersionBounded(t *testing.T) {
	rec := store.OrganizationProvisionRecord{
		OrganizationID:  "t-acme",
		Subdomain:       "acme",
		DomainMode:      store.OrganizationDomainFreeSubdomain,
		AdminEmail:      "admin@acme.test",
		OTECHFQDN:       "otech.example",
		ParentDomain:    "omani.homes",
		TenantNamespace: "org-t-acme",
	}

	// Unconfigured version → the bounded range, NOT "*".
	files, err := renderOrganizationOverlay(rec, OrganizationChartVersions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body, ok := files["bp-agenity.yaml"]
	if !ok {
		t.Fatalf("bp-agenity.yaml missing")
	}
	if !strings.Contains(body, `version: ">=0.5.0 <0.6.0"`) {
		t.Errorf("bp-agenity.yaml must pin the bounded chart range, got:\n%s", body)
	}
	if strings.Contains(body, `version: "*"`) {
		t.Errorf("bp-agenity.yaml must NOT pin the unbounded \"*\" (would resolve the squatting 0.9.7 image, #4922):\n%s", body)
	}

	// An explicit env-provided version still wins verbatim.
	filesPinned, err := renderOrganizationOverlay(rec, OrganizationChartVersions{Agenity: "0.5.20"})
	if err != nil {
		t.Fatalf("render pinned: %v", err)
	}
	if !strings.Contains(filesPinned["bp-agenity.yaml"], `version: "0.5.20"`) {
		t.Errorf("explicit Agenity version should render verbatim, got:\n%s", filesPinned["bp-agenity.yaml"])
	}
}

// TestRenderOrganizationOverlay_AgenityMCPBearerWiring is the #4276 hop 7/7b
// emitter guard. The bp-agenity overlay MUST set openovaMCP.bearerSecret +
// rs256PubkeySecret + enable the per-Org mcpBearer ExternalSecret pointed at
// secret/catalyst/agenity/<slug>/mcp-bearer, so the StatefulSet projects
// OPENOVA_MCP_BEARER + OPENOVA_MCP_RS256_PUBKEY_PEM. Without this the spawned
// claude-code agent reaches the openova MCP with no bearer → -32001
// unauthenticated, even after the Anthropic key (#4277) is seeded.
func TestRenderOrganizationOverlay_AgenityMCPBearerWiring(t *testing.T) {
	rec := store.OrganizationProvisionRecord{
		OrganizationID:  "t-acme",
		Subdomain:       "acme",
		DomainMode:      store.OrganizationDomainFreeSubdomain,
		AdminEmail:      "admin@acme.test",
		OTECHFQDN:       "otech.example",
		ParentDomain:    "omani.homes",
		TenantNamespace: "org-t-acme",
	}
	files, err := renderOrganizationOverlay(rec, OrganizationChartVersions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body, ok := files["bp-agenity.yaml"]
	if !ok {
		t.Fatalf("bp-agenity.yaml missing")
	}
	for _, want := range []string{
		"openovaMCP:",
		// #4624 — the ORG console host pinned as the MCP tenant host
		// (X-Tenant-Host). MUST be the Org host, never the Sovereign console
		// host (which is not a registered tenant → create_application 404s).
		"tenantHost: console.acme.omani.homes",
		"bearerSecret:",
		"name: agenity-mcp-bearer",
		"key: bearer",
		"rs256PubkeySecret:",
		"key: pubkeyPem",
		"mcpBearer:",
		"externalSecret:",
		"enabled: true",
		"remoteKey: catalyst/agenity/acme/mcp-bearer",
		"remoteBearerProperty: bearer",
		"remotePubkeyProperty: pubkeyPem",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("bp-agenity.yaml missing %q\n---\n%s", want, body)
		}
	}
}

// TestRenderOrganizationOverlay_AgenityOIDCGateWiring is the #4553/#4556
// durable-gate guard. The bp-agenity overlay MUST enable the chart's
// bp-oidc-gate companion (oidcGate.enabled) and pin the per-Org clientId to
// agenity-<slug>, so EVERY per-Org agenity install gets a chart-managed
// zero-click SSO gate + spaTokenSeed no-paste landing — NOT the agnstar walk's
// hand-created drift-disabled live instance. Without this a fresh agenity Org
// or a re-prov serves the dashboard un-gated.
func TestRenderOrganizationOverlay_AgenityOIDCGateWiring(t *testing.T) {
	rec := store.OrganizationProvisionRecord{
		OrganizationID:  "t-acme",
		Subdomain:       "acme",
		DomainMode:      store.OrganizationDomainFreeSubdomain,
		AdminEmail:      "admin@acme.test",
		OTECHFQDN:       "otech.example",
		ParentDomain:    "omani.homes",
		TenantNamespace: "org-t-acme",
	}
	files, err := renderOrganizationOverlay(rec, OrganizationChartVersions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body, ok := files["bp-agenity.yaml"]
	if !ok {
		t.Fatalf("bp-agenity.yaml missing")
	}
	for _, want := range []string{
		"oidcGate:",
		"clientId: agenity-acme",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("bp-agenity.yaml missing durable oidc-gate wiring %q\n---\n%s", want, body)
		}
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
		TenantNamespace: "org-t-acme",
	}
	files, err := renderOrganizationOverlay(rec, OrganizationChartVersions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, name := range []string{
		"kustomization.yaml",
		"namespace.yaml",
		// vcluster.yaml deliberately absent (#4188): the per-Org vCluster
		// is owned by the CRD org-controller, not this overlay.
		"bp-keycloak.yaml",
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

// TestRenderOrganizationOverlay_NoVClusterDuplicate is the #4188
// idempotency guard. The bootstrap-api org-tenant overlay used to render
// a SECOND, orphaned vCluster HelmRelease (`vc-<subdomain>`, chart
// vcluster@0.19.x, raw `rancher/k3s` image) alongside the canonical
// per-Org vCluster the CRD org-controller owns (chart vcluster@0.33.x,
// ns `<subdomain>`). On a Harbor-only Sovereign the rancher/k3s image
// could never pull, so the duplicate sat in Init:ImagePullBackOff for
// good and its HelmRelease stayed False on the spine — a fake-green trap.
//
// This test pins the overlay to NEVER emit a vCluster: no vcluster.yaml
// file, no `vc-<subdomain>` HelmRelease in any rendered doc, no
// `rancher/k3s` image, no `chart: vcluster` reference, and no `loft`
// HelmRepository sourceRef. A re-render of any existing Org therefore
// can't leave a stale-version vCluster duplicate behind (prune=true on
// the org-tenants Kustomization then reaps any pre-existing one). Refs
// #4188.
func TestRenderOrganizationOverlay_NoVClusterDuplicate(t *testing.T) {
	rec := store.OrganizationProvisionRecord{
		OrganizationID:  "t-acme",
		Subdomain:       "acme",
		DomainMode:      store.OrganizationDomainFreeSubdomain,
		AdminEmail:      "admin@acme.test",
		CompanyName:     "Acme Corp",
		OTECHFQDN:       "otech.example",
		VClusterName:    "vc-acme",
		TenantNamespace: "org-t-acme",
	}
	files, err := renderOrganizationOverlay(rec, OrganizationChartVersions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// 1. No standalone vcluster overlay file.
	if body, ok := files["vcluster.yaml"]; ok {
		t.Errorf("#4188 regression: overlay emitted vcluster.yaml — the per-Org vCluster belongs to the CRD org-controller, not this path; got:\n%s", body)
	}

	// 2. No rendered doc may carry the legacy vc-<subdomain> HelmRelease,
	//    the rancher/k3s distro, the bare `chart: vcluster` ref, or a loft
	//    sourceRef — any of those reintroduces the duplicate.
	banned := []string{
		"vc-acme",            // the legacy vc-<subdomain> HelmRelease name
		"rancher/k3s",        // the un-mirrorable distro image
		"chart: vcluster",    // the upstream vcluster chart
		"name: loft",         // the loft HelmRepository sourceRef
		`version: "0.19`,     // the stale 0.19.x pin
	}
	for name, body := range files {
		for _, b := range banned {
			if strings.Contains(body, b) {
				t.Errorf("#4188 regression: overlay file %s contains banned vCluster token %q — the duplicate vCluster is back:\n%s", name, b, body)
			}
		}
	}

	// 3. The kustomization resource list must not reference vcluster.yaml.
	if strings.Contains(files["kustomization.yaml"], "vcluster.yaml") {
		t.Errorf("#4188 regression: kustomization.yaml still lists vcluster.yaml:\n%s", files["kustomization.yaml"])
	}

	// 4. Re-rendering the SAME record is byte-identical and STILL carries
	//    no vCluster — idempotency across repeated provision re-runs.
	files2, err := renderOrganizationOverlay(rec, OrganizationChartVersions{})
	if err != nil {
		t.Fatalf("re-render: %v", err)
	}
	if _, ok := files2["vcluster.yaml"]; ok {
		t.Errorf("#4188 regression: re-render re-introduced vcluster.yaml")
	}
	if files["kustomization.yaml"] != files2["kustomization.yaml"] {
		t.Errorf("#4188: kustomization.yaml not idempotent across re-render")
	}
}

// TestOrgTenantSharedHelmRepositories_NoLoft pins the shared
// HelmRepository set to NOT reference the upstream loft chart repo. The
// loft repo was the only source the legacy vc-<subdomain> overlay pulled
// from; with the vCluster removed (#4188) it is dead weight and its
// presence would imply the duplicate could come back.
func TestOrgTenantSharedHelmRepositories_NoLoft(t *testing.T) {
	if strings.Contains(orgTenantSharedHelmRepositories, "charts.loft.sh") {
		t.Errorf("#4188 regression: shared HelmRepositories still reference charts.loft.sh:\n%s", orgTenantSharedHelmRepositories)
	}
	if strings.Contains(orgTenantSharedHelmRepositories, "name: loft") {
		t.Errorf("#4188 regression: shared HelmRepositories still declare the loft HelmRepository:\n%s", orgTenantSharedHelmRepositories)
	}
}

// TestOrgTenantSharedHelmRepositories_DeclaresAgenity pins the #4180/#4239
// gap: the per-Org overlay emits a bp-agenity HelmRelease whose sourceRef
// is HelmRepository/bp-agenity/flux-system, but the shared HelmRepository
// set declared blocks ONLY for keycloak/cnpg/newapi/wordpress-tenant/
// openclaw/stalwart-tenant — no bp-agenity. A fresh per-Org prov therefore
// failed `HelmRepository bp-agenity not found`, only resolving when someone
// hand-applied the HelmRepository (the forbidden kubectl patch). This test
// asserts the block is present and shape-matches its siblings, and that
// EVERY chart the overlay sourceRefs into has a declared HelmRepository.
func TestOrgTenantSharedHelmRepositories_DeclaresAgenity(t *testing.T) {
	// 1. bp-agenity must be declared.
	if !strings.Contains(orgTenantSharedHelmRepositories, "name: bp-agenity") {
		t.Fatalf("#4180 regression: shared HelmRepositories missing bp-agenity block:\n%s", orgTenantSharedHelmRepositories)
	}

	// 2. Every chart whose overlay HelmRelease pins
	//    sourceRef: HelmRepository/bp-* MUST have a matching block, or the
	//    rendered HR fails `HelmRepository <name> not found` on a fresh Org.
	for _, want := range []string{
		"name: bp-keycloak",
		"name: bp-newapi",
		"name: bp-wordpress-tenant",
		"name: bp-openclaw",
		"name: bp-stalwart-tenant",
		"name: bp-agenity",
	} {
		if !strings.Contains(orgTenantSharedHelmRepositories, want) {
			t.Errorf("shared HelmRepositories missing %q", want)
		}
	}
	// #4920 — the per-Org bp-cnpg operator was removed; its HelmRepository
	// must no longer be declared in the shared block.
	if strings.Contains(orgTenantSharedHelmRepositories, "name: bp-cnpg") {
		t.Errorf("#4920 regression: shared HelmRepositories still declares bp-cnpg (the per-Org operator was removed)")
	}

	// 3. Shape parity with the siblings: the bp-agenity block must carry
	//    the canonical oci type + ghcr url + ghcr-pull secretRef + the
	//    flux-system namespace, so Flux owns it (GitOps-managed, never a
	//    hand-applied patch).
	idx := strings.Index(orgTenantSharedHelmRepositories, "name: bp-agenity")
	block := orgTenantSharedHelmRepositories[idx:]
	for _, want := range []string{
		"namespace: flux-system",
		"type: oci",
		"url: oci://ghcr.io/openova-io",
		"name: ghcr-pull",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("bp-agenity HelmRepository block missing %q\n--- block ---\n%s", want, block)
		}
	}
}

// TestRenderOrganizationOverlay_AgenityHRResolvesToDeclaredRepo proves the
// fresh-prov fix end-to-end: a rendered per-Org overlay emits the
// bp-agenity HelmRelease AND its sourceRef HelmRepository is declared in
// the shared set written alongside it (#4180).
func TestRenderOrganizationOverlay_AgenityHRResolvesToDeclaredRepo(t *testing.T) {
	rec := store.OrganizationProvisionRecord{
		OrganizationID:  "t-acme",
		Subdomain:       "acme",
		DomainMode:      store.OrganizationDomainFreeSubdomain,
		AdminEmail:      "admin@acme.test",
		OTECHFQDN:       "otech.example",
		VClusterName:    "vc-acme",
		TenantNamespace: "org-t-acme",
	}
	files, err := renderOrganizationOverlay(rec, OrganizationChartVersions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	hr, ok := files["bp-agenity.yaml"]
	if !ok {
		t.Fatalf("bp-agenity.yaml missing from rendered overlay")
	}
	// The HR pins this exact source.
	for _, want := range []string{
		"kind: HelmRepository",
		"name: bp-agenity",
		"namespace: flux-system",
	} {
		if !strings.Contains(hr, want) {
			t.Errorf("rendered bp-agenity HR missing sourceRef line %q\n--- rendered ---\n%s", want, hr)
		}
	}
	// And that source is declared in the shared set written to the same
	// overlay parent, so Flux resolves it (no `not found`, no hand-patch).
	if !strings.Contains(orgTenantSharedHelmRepositories, "name: bp-agenity") {
		t.Fatalf("#4180: rendered HR sourceRefs HelmRepository/bp-agenity but the shared set never declares it")
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
		TenantNamespace: "org-t-acme",
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
		TenantNamespace: "org-t-acme",
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
	// #4920 — the per-Org bp-cnpg operator overlay was removed; no bp-cnpg.yaml
	// is rendered (the cluster-wide platform operator reconciles Org Clusters).
	if _, ok := files["bp-cnpg.yaml"]; ok {
		t.Errorf("#4920 regression: bp-cnpg.yaml must not be rendered")
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
		TenantNamespace: "org-t-acme",
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
		TenantNamespace: "org-t-alice",
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
		"      issuerURL: https://keycloak.alice.omantel.omani.works/realms/org-alice",
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
	// Per-tenant LLM endpoint MUST be the Organization's own api.<sub>.<parent>,
	// NEVER the otech-wide newapi.<otech-fqdn> (that would route every
	// Organization's traffic through one shared gateway, defeating per-tenant
	// channel routing).
	if strings.Contains(body, "https://newapi.otech107.omani.works") {
		t.Errorf("bp-openclaw llm.baseURL must be per-tenant api.<sub>.<parent>, not otech-wide newapi: %s", body)
	}
}

// TestRenderOrganizationOverlay_NewAPIEmitted asserts the Organization tenant
// overlay emits a per-tenant bp-newapi HelmRelease (#945). Without it
// the bp-openclaw HR points at https://api.<sub>.<parent>/v1 with no
// chart materialising that ingress — alice's OpenClaw boots and gets
// NXDOMAIN on every chat request.
//
// The chart values must:
//   - dependsOn bp-keycloak (admin-UI OIDC). Postgres is reconciled by the
//     cluster-wide platform cnpg-system operator (#4920 removed the per-Org
//     bp-cnpg operator); the chart still OWNS its CNPG Cluster via cnpg.enabled.
//   - Emit ingress.host = api.<sub>.<parent> + ingress.adminHost =
//     admin.<sub>.<parent> so OpenClaw's llm.baseURL resolves.
//   - Wire auth.adminUI to the per-tenant Keycloak realm
//     (alice's tenant Keycloak), NOT a shared otech-level IdP.
//   - Enable defaultChannels.qwenPartner so the chart's channel-seed
//     post-install Job auto-seeds the partner-hosted Qwen at install time
//     (canonical first-otech default per #915 C4 PR #919).
//   - NOT pin database.existingSecret / credentials.existingSecret to
//     names nothing creates (#3858) — leave them unset so the chart's
//     own cnpg.enabled + database-secret-sync-job + credentials.
//     autoProvision path converges, and set valkey.enabled: false
//     (the default cross-vCluster Redis is unreachable from the Org vc).
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
		TenantNamespace: "org-t-alice",
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
		"namespace: org-t-alice",
		"chart: bp-newapi",
		`version: "*"`,    // unconfigured chart version falls back to "*"
		"name: bp-newapi", // sourceRef.name
	} {
		if !strings.Contains(body, want) {
			t.Errorf("bp-newapi.yaml missing %q\n--- rendered ---\n%s", want, body)
		}
	}

	// dependsOn: bp-keycloak only. #4920 removed the per-Org bp-cnpg operator;
	// Postgres is reconciled by the cluster-wide platform cnpg-system operator,
	// so bp-newapi must NOT dependsOn a (non-existent) per-Org bp-cnpg HR.
	wantDependsOn := []string{
		"  dependsOn:",
		"    - name: bp-keycloak",
		"      namespace: org-t-alice",
	}
	for _, line := range wantDependsOn {
		if !strings.Contains(body, line) {
			t.Errorf("bp-newapi.yaml dependsOn missing line %q\n--- rendered ---\n%s", line, body)
		}
	}
	if strings.Contains(body, "name: bp-cnpg") {
		t.Errorf("#4920 regression: bp-newapi.yaml must not dependsOn a per-Org bp-cnpg HR\n--- rendered ---\n%s", body)
	}

	// #4246 — install.disableWait + upgrade.disableWait MUST be set. The Pod
	// gates on a non-empty SQL_DSN that the chart's POST-INSTALL db-dsn-sync hook
	// PATCHes in; with wait enabled Helm blocks on Pod-Ready before running that
	// hook → permanent deadlock + 15m install timeout. disableWait breaks it.
	for _, line := range []string{
		"  install:",
		"  upgrade:",
		"    disableWait: true",
	} {
		if !strings.Contains(body, line) {
			t.Errorf("bp-newapi.yaml missing disableWait line %q (#4246 DSN deadlock)\n--- rendered ---\n%s", line, body)
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
		"          issuer: https://keycloak.alice.omantel.omani.works/realms/org-alice",
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

	// #3858 / #3374 — DB/credentials/Valkey convergence.
	//
	// The overlay MUST NOT pin database.existingSecret / credentials.
	// existingSecret to names nothing creates. On a real Org the in-vCluster
	// CNPG renders `bp-newapi-newapi-pg-app` (key `uri`), NOT a
	// `newapi-pg-app`/SQL_DSN Secret, and nothing creates `newapi-credentials`.
	// Pinning either crashed the Pod (FailedMount / CreateContainerConfigError).
	// Leaving both UNSET lets the chart's canonical auto-provision path own
	// them: cnpg.enabled + database-secret-sync-job for the
	// `postgres://...?sslmode=require` DSN, credentials.autoProvision for the
	// random SESSION/CRYPTO secret. Validated live on the omantel.biz demo Org
	// (newapi 3/3 Running, GET /api/status → 200).
	// Scan only ACTIVE (non-comment) YAML lines — the rationale comments
	// intentionally name the old broken values, so a naive substring search
	// over the whole body would false-positive on the documentation itself.
	forbiddenSecretValues := []string{
		"existingSecret: newapi-pg-app",     // nothing creates this name
		"existingSecret: newapi-credentials", // nothing creates this name
	}
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "#") { // skip rationale comments
			continue
		}
		for _, bad := range forbiddenSecretValues {
			if strings.Contains(line, bad) {
				t.Errorf("bp-newapi.yaml must NOT pin a never-created Secret (%q) (#3858) — let the chart auto-provision\n--- rendered ---\n%s", bad, body)
			}
		}
	}
	// Valkey MUST be disabled for the per-Org overlay: the chart default
	// valkey.url is a host-synced rtz-vCluster Redis, unreachable from inside
	// an Org vCluster → NewAPI FATALs on the Redis ping probe.
	if !strings.Contains(body, "    valkey:\n      enabled: false") {
		t.Errorf("bp-newapi.yaml must set valkey.enabled: false for the per-Org overlay (#3858 — cross-vCluster Redis is unreachable)\n--- rendered ---\n%s", body)
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
		TenantNamespace: "org-t-alice",
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
		TenantNamespace: "org-t-alice",
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
		"      issuerURL: https://keycloak.alice.omantel.omani.works/realms/org-alice",
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
		"      realmURL: https://keycloak.alice.omantel.omani.works/realms/org-alice",
		"      clientID: wordpress",
		"      clientSecretName: wordpress-oidc-client-secret",
	}
	for _, line := range wantLegacy {
		if !strings.Contains(body, line) {
			t.Errorf("bp-wordpress-tenant legacy keycloak block missing line %q (back-compat)", line)
		}
	}
	// Per-tenant realm URL MUST be the per-Organization Keycloak, not a shared
	// otech-level IdP. Same guardrail as the OpenClaw/Stalwart tests.
	bad := "https://keycloak.otech107.omani.works"
	if strings.Contains(body, bad) {
		t.Errorf("bp-wordpress-tenant issuerURL must be per-tenant, not otech-wide: %s", body)
	}
	// #3785: the WordPress public host now flows through the Cilium Gateway
	// HTTPRoute (gateway.host: ...) instead of the dead traefik ingress block;
	// the host string is asserted here so a regression to the exposure surface
	// trips in the same test.
	if !strings.Contains(body, "host: wordpress.alice.omantel.omani.works") {
		t.Errorf("wordpress gateway host missing")
	}
	// And the gateway block MUST parent the dedicated console Gateway.
	if !strings.Contains(body, "name: cilium-gateway-console") {
		t.Errorf("wordpress HTTPRoute must parent cilium-gateway-console (#3785)")
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
		TenantNamespace: "org-t-alice",
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
		TenantNamespace: "org-t-acme",
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
	// orgDomain (BYO mode) is the bare BYO domain. The producer keeps the
	// chart-consumed data-value key orgDomain (#3383 WIRE-STABLE invariant);
	// the bp-wordpress-tenant chart reads .Values.orgDomain.
	if !strings.Contains(body, "orgDomain: acme.com") {
		t.Errorf("byo orgDomain missing")
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
		TenantNamespace: "org-t-acme",
	}
	files, err := renderOrganizationOverlay(rec, OrganizationChartVersions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body, ok := files["bp-stalwart-tenant.yaml"]
	if !ok {
		t.Fatalf("bp-stalwart-tenant.yaml missing")
	}
	// Per-tenant realm URL — must point at the Organization's vcluster Keycloak,
	// not a shared otech-level IdP.
	wantRealmURL := "https://keycloak.acme.omantel.omani.works/realms/org-acme"
	if !strings.Contains(body, wantRealmURL) {
		t.Errorf("realmURL missing — want %s in body", wantRealmURL)
	}
	// Confidential client ID + the OIDC ExternalSecret-store remoteRef path.
	// (The OIDC client secret IS materialised on a fresh Org — it is created by
	// the per-Org Keycloak SSO bridge — so sourcing it from the store is valid.)
	for _, want := range []string{
		"clientID: stalwart",
		"clientSecretName: stalwart-oidc-client-secret",
		"sovereign/omantel.omani.works/stalwart/t-acme/oidc",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in bp-stalwart-tenant.yaml — got:\n%s", want, body)
		}
	}
	// #4246 — the ADMIN_PASSWORD must NOT be sourced from an ExternalSecret on a
	// fresh per-Org install (nothing seeds OpenBao at .../stalwart/<tenant>/admin
	// → CreateContainerConfigError). admin.externalSecret MUST be disabled so the
	// chart auto-provisions a persistent random ADMIN_PASSWORD.
	if strings.Contains(body, "sovereign/omantel.omani.works/stalwart/t-acme/admin") {
		t.Errorf("admin ExternalSecret remoteRef must NOT be emitted (#4246) — fresh Org has no OpenBao admin seed")
	}
	if !strings.Contains(body, "admin:\n      # #4246") {
		t.Errorf("expected admin block to carry the #4246 disabled-externalSecret note")
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
	// #4307 — the webmail HTTPRoute MUST parent the DEDICATED
	// cilium-gateway-console (NOT the apps cilium-gateway), else
	// mail.<slug>.<pool> matches no listener → NoMatchingListenerHostname →
	// the gateway serves a 404 despite a healthy Pod. The overlay restates the
	// parentRef explicitly (belt-and-suspenders over the chart default).
	for _, want := range []string{
		"parentRef:",
		"name: cilium-gateway-console",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected webmail %q in bp-stalwart-tenant.yaml (#4307 console-gateway) — got:\n%s", want, body)
		}
	}
	// #4307 — install.disableWait + upgrade.disableWait MUST be set. The
	// StatefulSet mounts the non-optional stalwart-tls Secret so the Pod blocks
	// at ContainerCreating until cert-manager issues the DNS-01 leaf; with
	// Flux's default --wait, helm blocks on StatefulSet readiness BEFORE running
	// the post-install setup Job (a Helm hook) → install times out, HR wedges
	// InstallFailed, Job never fires. disableWait breaks the deadlock.
	for _, line := range []string{
		"  install:",
		"  upgrade:",
		"    disableWait: true",
	} {
		if !strings.Contains(body, line) {
			t.Errorf("bp-stalwart-tenant.yaml missing disableWait line %q (#4307 cert-mount deadlock)\n--- rendered ---\n%s", line, body)
		}
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
		if !strings.Contains(r.URL.Path, "/admin/realms/org-acme/clients") {
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`[{"clientId":"catalyst-ui"},{"clientId":"wordpress"},{"clientId":"openclaw"},{"clientId":"stalwart"}]`))
	}))
	defer srv.Close()
	missing, err := verifyKeycloakClients(context.Background(), srv.Client(), srv.URL, "org-acme", "tok",
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
	missing, err := verifyKeycloakClients(context.Background(), srv.Client(), srv.URL, "org-acme", "tok",
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
// the role:org-pool list MUST validate, not just OTECHFQDN.
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

// TestProvisionFreeSubdomain_WritesWildcardREPLACE locks the #4075 fix:
// the per-Org DNS write MUST emit an explicit `*.<sub>.<parent>` A record
// (so the Org's whole subtree shadows any stale apex pool wildcard left by
// a wiped prior env) AND must use ChangeType=REPLACE on every record so a
// same-name stale record is overwritten rather than skipped.
func TestProvisionFreeSubdomain_WritesWildcardREPLACE(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody = readAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	p := DefaultOrganizationDNSProvisioner{Writer: NewPowerDNSWriter(srv.URL, "key")}
	if err := p.ProvisionFreeSubdomain(context.Background(), "demo", "omani.homes", "212.72.24.33", ""); err != nil {
		t.Fatalf("ProvisionFreeSubdomain: %v", err)
	}

	// The explicit per-Org wildcard pinned to the current ingress IP — the
	// record that shadows a stale `*.omani.homes` apex wildcard.
	if !strings.Contains(gotBody, `"name":"*.demo.omani.homes."`) {
		t.Errorf("regression (#4075): missing explicit `*.demo.omani.homes.` wildcard A record\nbody: %s", gotBody)
	}
	// The console host itself.
	if !strings.Contains(gotBody, `"name":"console.demo.omani.homes."`) {
		t.Errorf("missing console A record\nbody: %s", gotBody)
	}
	// Pinned to THIS Sovereign's ingress, not a stale IP.
	if !strings.Contains(gotBody, `"content":"212.72.24.33"`) {
		t.Errorf("records not pinned to the supplied ingress IP\nbody: %s", gotBody)
	}
	// Every write is an unconditional upsert.
	if strings.Contains(gotBody, `"changetype":"CREATE"`) || !strings.Contains(gotBody, `"changetype":"REPLACE"`) {
		t.Errorf("expected ChangeType=REPLACE on every rrset (upsert/overwrite stale)\nbody: %s", gotBody)
	}
}

// TestProvisionFreeSubdomain_PrefersPoolWriter locks the #4218 fix: when a
// dedicated PoolWriter (central pdns.openova.io) is wired, the pool-domain
// console A-record write MUST go to the pool server, NOT the Sovereign-local
// Writer (powerdns.powerdns.svc) — which has no omani.* zone and would 404.
// The local Writer must receive ZERO requests for this pool write.
func TestProvisionFreeSubdomain_PrefersPoolWriter(t *testing.T) {
	var localHits, poolHits int
	localSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		localHits++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer localSrv.Close()
	var poolBody string
	poolSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		poolHits++
		poolBody = readAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer poolSrv.Close()

	p := DefaultOrganizationDNSProvisioner{
		Writer:     NewPowerDNSWriter(localSrv.URL, "local-key"),
		PoolWriter: NewPowerDNSWriter(poolSrv.URL, "central-key"),
	}
	if err := p.ProvisionFreeSubdomain(context.Background(), "f4179close", "omani.works", "212.72.24.33", ""); err != nil {
		t.Fatalf("ProvisionFreeSubdomain: %v", err)
	}
	if poolHits != 1 {
		t.Errorf("expected exactly 1 write to the CENTRAL pool pdns, got %d", poolHits)
	}
	if localHits != 0 {
		t.Errorf("regression (#4218): pool-domain write leaked to the Sovereign-LOCAL pdns (%d hits) — would 404 (no omani.works zone)", localHits)
	}
	if !strings.Contains(poolBody, `"name":"console.f4179close.omani.works."`) {
		t.Errorf("central pdns did not receive the console A record\nbody: %s", poolBody)
	}
}

// TestProvisionFreeSubdomain_FallsBackToLocalWriter locks the single-PowerDNS
// Sovereign back-compat path: with no PoolWriter wired, the free-subdomain
// write uses the local Writer (the pool IS the local zone there).
func TestProvisionFreeSubdomain_FallsBackToLocalWriter(t *testing.T) {
	var localHits int
	localSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		localHits++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer localSrv.Close()

	p := DefaultOrganizationDNSProvisioner{Writer: NewPowerDNSWriter(localSrv.URL, "local-key")}
	if err := p.ProvisionFreeSubdomain(context.Background(), "demo", "t01.omani.works", "212.72.24.33", ""); err != nil {
		t.Fatalf("ProvisionFreeSubdomain: %v", err)
	}
	if localHits != 1 {
		t.Errorf("expected the single-PowerDNS fallback to write to the local pdns, got %d hits", localHits)
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

// newTestHandlerWithMultiDomainPool spins up a Handler whose Organization
// deps include a 2-entry org-pool (omani.works ready + omani.trade
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
		{Name: "omani.works", Role: "org-pool", NSFlipReady: true},
		{Name: "omani.trade", Role: "org-pool", NSFlipReady: true},
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
// picks the first NS-flip-ready org-pool entry.
func TestCreateOrganization_MultiDomain_DefaultsToFirstReady(t *testing.T) {
	h, _, _, _, _, _ := newTestHandlerWithMultiDomainPool(t, []OrganizationParentDomain{
		{Name: "otech.example", Role: "primary", NSFlipReady: true},
		{Name: "omani.works", Role: "org-pool", NSFlipReady: true},
		{Name: "omani.trade", Role: "org-pool", NSFlipReady: true},
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
		t.Errorf("default parent_domain: %q want omani.works (first ready org-pool entry)", got.ParentDomain)
	}
}

// TestCreateOrganization_MultiDomain_RejectsUnknownParent — picking a
// parent that isn't in the org-pool list = 400.
func TestCreateOrganization_MultiDomain_RejectsUnknownParent(t *testing.T) {
	h, _, _, _, _, _ := newTestHandlerWithMultiDomainPool(t, []OrganizationParentDomain{
		{Name: "omani.works", Role: "org-pool", NSFlipReady: true},
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
		{Name: "omani.works", Role: "org-pool", NSFlipReady: true},
		{Name: "omani.trade", Role: "org-pool", NSFlipReady: false},
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
		case "org-pool":
			pool++
		}
	}
	if primary != 1 {
		t.Errorf("primary: want 1 got %d (%v)", primary, got)
	}
	if pool != 4 {
		t.Errorf("org-pool: want 4 got %d (%v)", pool, got)
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
		if p.Role != "org-pool" {
			t.Errorf("non-primary entry should be org-pool: %+v", p)
		}
	}
}

// TestDeleteOrganization_DeprovisionsDNS (#4459) — an Org delete must remove
// the per-Org pool DNS records the provision path wrote, so a later same-slug
// re-prov does not inherit a stale console/app A-record → Console 000.
func TestDeleteOrganization_DeprovisionsDNS(t *testing.T) {
	h, _, dns, _, _, _ := newTestHandlerWithOrganizationDeps(t)

	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/organizations",
		bytes.NewReader([]byte(`{"subdomain":"acme","admin_email":"a@b.test"}`)))
	createReq.Header.Set("Content-Type", "application/json")
	createW := httptest.NewRecorder()
	h.HandleCreateOrganization(createW, createReq)
	var created orgTenantResponse
	_ = json.Unmarshal(createW.Body.Bytes(), &created)

	delReq := httptest.NewRequest(http.MethodDelete,
		"/api/v1/organizations/"+created.OrganizationID, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", created.OrganizationID)
	delReq = delReq.WithContext(context.WithValue(delReq.Context(), chi.RouteCtxKey, rctx))
	delW := httptest.NewRecorder()
	h.HandleDeleteOrganization(delW, delReq)

	dns.mu.Lock()
	defer dns.mu.Unlock()
	if len(dns.deproviCalls) != 1 {
		t.Fatalf("delete must call DeprovisionFreeSubdomain exactly once (#4459 — stale DNS poisons re-prov); got %d: %v", len(dns.deproviCalls), dns.deproviCalls)
	}
	if !strings.HasPrefix(dns.deproviCalls[0], "acme:") {
		t.Errorf("deprovision must target the Org subdomain 'acme'; got %q", dns.deproviCalls[0])
	}
}

// TestProvisionFreeSubdomain_ConsoleTargetsConsoleIPv4 locks #4732 item 3:
// the `console.<slug>.<parent>` record must target the DEDICATED console
// gateway/ELB EIP (consoleIPv4) while the per-Org wildcard + app hosts stay
// on the shared-gateway ingress IP. Writing the console record with the
// shared IP is the nstar failure (pool-wildcard cert + 404 — the shared
// gateway has no console listener for the Org zone).
func TestProvisionFreeSubdomain_ConsoleTargetsConsoleIPv4(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody = readAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	p := DefaultOrganizationDNSProvisioner{Writer: NewPowerDNSWriter(srv.URL, "key")}
	if err := p.ProvisionFreeSubdomain(context.Background(), "nstar", "omani.homes", "212.72.24.14", "212.72.24.33"); err != nil {
		t.Fatalf("ProvisionFreeSubdomain: %v", err)
	}

	// Decode the rrsets and assert per-record targeting.
	var body struct {
		RRSets []struct {
			Name    string `json:"name"`
			Records []struct {
				Content string `json:"content"`
			} `json:"records"`
		} `json:"rrsets"`
	}
	if err := json.Unmarshal([]byte(gotBody), &body); err != nil {
		t.Fatalf("unmarshal PATCH body: %v\nbody: %s", err, gotBody)
	}
	byName := map[string]string{}
	for _, rr := range body.RRSets {
		if len(rr.Records) > 0 {
			byName[rr.Name] = rr.Records[0].Content
		}
	}
	if got := byName["console.nstar.omani.homes."]; got != "212.72.24.33" {
		t.Errorf("console record = %q, want console ELB EIP 212.72.24.33", got)
	}
	for _, appHost := range []string{"*.nstar.omani.homes.", "wordpress.nstar.omani.homes.", "keycloak.nstar.omani.homes."} {
		if got := byName[appHost]; got != "212.72.24.14" {
			t.Errorf("%s = %q, want shared ingress 212.72.24.14", appHost, got)
		}
	}
}

// TestProvisionFreeSubdomain_EmptyConsoleIPFallsBack locks the back-compat
// contract: consoleIPv4=="" keeps the pre-#4732 single-IP behaviour.
func TestProvisionFreeSubdomain_EmptyConsoleIPFallsBack(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody = readAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	p := DefaultOrganizationDNSProvisioner{Writer: NewPowerDNSWriter(srv.URL, "key")}
	if err := p.ProvisionFreeSubdomain(context.Background(), "demo", "omani.rest", "212.72.24.14", ""); err != nil {
		t.Fatalf("ProvisionFreeSubdomain: %v", err)
	}
	if strings.Contains(gotBody, `"content":"212.72.24.33"`) {
		t.Errorf("unexpected console IP with empty consoleIPv4\nbody: %s", gotBody)
	}
	if c := strings.Count(gotBody, `"content":"212.72.24.14"`); c != len(theFreeSubdomainPrefixes) {
		t.Errorf("expected every rrset on the ingress IP (%d), got %d\nbody: %s", len(theFreeSubdomainPrefixes), c, gotBody)
	}
}

// TestResolveSovereignConsoleIPv4 locks the console-EIP discovery seam: the
// Sovereign's own `console.<fqdn>` A record is the zero-config source of
// truth for the console front door; failures degrade to "" (caller falls
// back to the shared ingress IP).
func TestResolveSovereignConsoleIPv4(t *testing.T) {
	orig := lookupHostFn
	defer func() { lookupHostFn = orig }()

	lookupHostFn = func(_ context.Context, host string) ([]string, error) {
		if host != "console.hw220.omani.works" {
			t.Errorf("lookup host = %q, want console.hw220.omani.works", host)
		}
		return []string{"2a01:db8::1", "212.72.24.33"}, nil
	}
	if got := resolveSovereignConsoleIPv4(context.Background(), "hw220.omani.works."); got != "212.72.24.33" {
		t.Errorf("resolve = %q, want first IPv4 212.72.24.33", got)
	}

	lookupHostFn = func(_ context.Context, _ string) ([]string, error) {
		return nil, errors.New("nx")
	}
	if got := resolveSovereignConsoleIPv4(context.Background(), "hw220.omani.works"); got != "" {
		t.Errorf("resolve on lookup error = %q, want empty", got)
	}
	if got := resolveSovereignConsoleIPv4(context.Background(), "  "); got != "" {
		t.Errorf("resolve on blank fqdn = %q, want empty", got)
	}
}
