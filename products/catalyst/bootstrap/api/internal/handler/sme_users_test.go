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

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/newapi"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// fakeKC is a stub SMEKeycloakClient.
type fakeKC struct {
	mu     sync.Mutex
	calls  []string
	failNext bool // when true, return error on next call (then resets)
	id     string
}

func (f *fakeKC) EnsureSMEUser(_ context.Context, _, realm, email, uuid, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, realm+":"+email+":"+uuid)
	if f.failNext {
		f.failNext = false
		return "", errors.New("kc 502")
	}
	if f.id == "" {
		return "kc-uid-" + email, nil
	}
	return f.id, nil
}

// fakeApplier is a stub SMESecretApplier.
type fakeApplier struct {
	mu      sync.Mutex
	applied []string
	err     error
}

func (f *fakeApplier) ApplyNewAPIKeySecret(_ context.Context, ns, _, uuid, key, base string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	name := "newapi-key-" + uuid
	f.applied = append(f.applied, ns+"/"+name+":"+key+":"+base)
	return name, nil
}

// fakeEmitter records emitted events.
type fakeEmitter struct {
	mu       sync.Mutex
	created  []store.UserProvisionRecord
	deleted  []store.UserProvisionRecord
}

func (f *fakeEmitter) EmitSMEUserCreated(_ context.Context, r store.UserProvisionRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, r)
	return nil
}
func (f *fakeEmitter) EmitSMEUserDeleted(_ context.Context, r store.UserProvisionRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, r)
	return nil
}

func newTestHandlerWithSME(t *testing.T) (*Handler, *fakeKC, *fakeApplier, *fakeEmitter, func(req newapi.CreateUserRequest, status int, body string)) {
	t.Helper()
	dir := t.TempDir()
	reg, err := store.NewTenantRegistry(dir)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	if err := reg.Put(store.TenantRegistration{
		Host:                 "console.acme.otech.example",
		TenantID:             "tenant-acme",
		KeycloakRealmURL:     "https://kc.acme.otech.example/realms/sme-acme",
		KeycloakClientID:     "catalyst-ui",
		TenantKind:           store.TenantKindSME,
		OrganizationNamespace:   "sme-acme",
		SMEKeycloakAdminURL:  "http://keycloak-sme-acme.sme-acme.svc:8080",
		SMEKeycloakRealmName: "sme-acme",
	}); err != nil {
		t.Fatalf("put tenant: %v", err)
	}
	if err := reg.Put(store.TenantRegistration{
		Host:             "console.otech.example",
		TenantID:         "tenant-otech",
		TenantKind:       store.TenantKindOTECH,
		KeycloakRealmURL: "https://kc.otech.example/realms/otech",
		KeycloakClientID: "catalyst-ui",
	}); err != nil {
		t.Fatalf("put otech: %v", err)
	}

	ups, err := store.NewUserProvisionStore(dir)
	if err != nil {
		t.Fatalf("ups: %v", err)
	}

	// Stub NewAPI server.
	type respCfg struct {
		status int
		body   string
	}
	var (
		mu  sync.Mutex
		cfg = respCfg{status: http.StatusCreated, body: `{"user_id":"newapi-1","api_key":"sk-test"}`}
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		c := cfg
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(c.status)
		_, _ = w.Write([]byte(c.body))
	}))
	t.Cleanup(srv.Close)

	napi := newapi.NewWithHTTP(srv.URL, "tok", srv.Client())
	kc := &fakeKC{}
	ap := &fakeApplier{}
	em := &fakeEmitter{}

	h := &Handler{log: slog.New(slog.NewJSONHandler(io.Discard, nil))}
	h.SetTenantRegistry(reg)
	h.SetSMEDeps(SMEDeps{
		UserProvisionStore:    ups,
		NewAPIClient:          napi,
		KeycloakClient:        kc,
		SecretApplier:         ap,
		Events:                em,
		SecretBaseURLTemplate: "https://newapi.{otech_fqdn}",
		OTECHFQDN:             "otech.example",
	})

	setNewAPIResp := func(req newapi.CreateUserRequest, status int, body string) {
		_ = req // unused; kept for future per-request matching
		mu.Lock()
		cfg = respCfg{status: status, body: body}
		mu.Unlock()
	}
	return h, kc, ap, em, setNewAPIResp
}

func TestSMEUsers_Create_HappyPath(t *testing.T) {
	h, kc, ap, em, _ := newTestHandlerWithSME(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/org/users",
		bytes.NewReader([]byte(`{"email":"alice@acme.example"}`)))
	req.Header.Set("X-Tenant-Host", "console.acme.otech.example")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleCreateSMEUser(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", w.Code, w.Body.String())
	}
	var resp smeUserResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.State != store.UPSDone {
		t.Errorf("State = %q, want done; resp = %+v", resp.State, resp)
	}
	if resp.Steps.KC != "done" || resp.Steps.NewAPI != "done" || resp.Steps.Secret != "done" {
		t.Errorf("Steps = %+v", resp.Steps)
	}
	if len(kc.calls) != 1 {
		t.Errorf("kc.calls = %d, want 1", len(kc.calls))
	}
	if len(ap.applied) != 1 {
		t.Errorf("applied = %d, want 1", len(ap.applied))
	}
	if !strings.Contains(ap.applied[0], "https://newapi.otech.example") {
		t.Errorf("base-url substitution wrong: %s", ap.applied[0])
	}
	if len(em.created) != 1 {
		t.Errorf("emitter created = %d, want 1", len(em.created))
	}
}

func TestSMEUsers_Create_RejectsOTECHTenant(t *testing.T) {
	h, _, _, _, _ := newTestHandlerWithSME(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/org/users",
		bytes.NewReader([]byte(`{"email":"alice@otech.example"}`)))
	req.Header.Set("X-Tenant-Host", "console.otech.example")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleCreateSMEUser(w, req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422 (otech tenant blocked from SME endpoint); body=%s", w.Code, w.Body.String())
	}
}

func TestSMEUsers_Create_RejectsUnknownTenant(t *testing.T) {
	h, _, _, _, _ := newTestHandlerWithSME(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/org/users",
		bytes.NewReader([]byte(`{"email":"x@y.example"}`)))
	req.Header.Set("X-Tenant-Host", "console.unknown.example")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleCreateSMEUser(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestSMEUsers_Create_RejectsMissingTenantHeader(t *testing.T) {
	h, _, _, _, _ := newTestHandlerWithSME(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/org/users",
		bytes.NewReader([]byte(`{"email":"x@y.example"}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleCreateSMEUser(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestSMEUsers_Create_RejectsBadEmail(t *testing.T) {
	h, _, _, _, _ := newTestHandlerWithSME(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/org/users",
		bytes.NewReader([]byte(`{"email":"not-an-email"}`)))
	req.Header.Set("X-Tenant-Host", "console.acme.otech.example")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleCreateSMEUser(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestSMEUsers_Create_KCFailure_PartialState(t *testing.T) {
	h, kc, _, _, _ := newTestHandlerWithSME(t)
	kc.failNext = true // first call fails

	req := httptest.NewRequest(http.MethodPost, "/api/v1/org/users",
		bytes.NewReader([]byte(`{"email":"alice@acme.example"}`)))
	req.Header.Set("X-Tenant-Host", "console.acme.otech.example")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleCreateSMEUser(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (partial state still 202)", w.Code)
	}
	var resp smeUserResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.State == store.UPSDone {
		t.Errorf("state should NOT be done on kc failure: %+v", resp)
	}
	if !strings.Contains(resp.LastError, "kc_create") {
		t.Errorf("LastError should mention kc_create: %s", resp.LastError)
	}
}

func TestSMEUsers_List_ScopedToTenant(t *testing.T) {
	h, _, _, _, _ := newTestHandlerWithSME(t)

	// Create alice + bob in tenant-acme.
	for _, email := range []string{"alice@acme.example", "bob@acme.example"} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/org/users",
			bytes.NewReader([]byte(`{"email":"`+email+`"}`)))
		req.Header.Set("X-Tenant-Host", "console.acme.otech.example")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.HandleCreateSMEUser(w, req)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/org/users", nil)
	req.Header.Set("X-Tenant-Host", "console.acme.otech.example")
	w := httptest.NewRecorder()
	h.HandleListSMEUsers(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d", w.Code)
	}
	var listResp struct {
		Items []smeUserResponse `json:"items"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &listResp)
	if len(listResp.Items) != 2 {
		t.Errorf("items = %d, want 2", len(listResp.Items))
	}
}

func TestSMEUsers_Delete(t *testing.T) {
	h, _, _, em, _ := newTestHandlerWithSME(t)

	// Create.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/org/users",
		bytes.NewReader([]byte(`{"email":"alice@acme.example"}`)))
	req.Header.Set("X-Tenant-Host", "console.acme.otech.example")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleCreateSMEUser(w, req)
	var created smeUserResponse
	_ = json.Unmarshal(w.Body.Bytes(), &created)

	// Delete.
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("uuid", created.UUID)
	dreq := httptest.NewRequest(http.MethodDelete, "/api/v1/org/users/"+created.UUID, nil)
	dreq.Header.Set("X-Tenant-Host", "console.acme.otech.example")
	dreq = dreq.WithContext(context.WithValue(dreq.Context(), chi.RouteCtxKey, rctx))
	dw := httptest.NewRecorder()
	h.HandleDeleteSMEUser(dw, dreq)

	if dw.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204; body=%s", dw.Code, dw.Body.String())
	}
	if len(em.deleted) != 1 {
		t.Errorf("emitter deleted = %d, want 1", len(em.deleted))
	}
}

func TestTenantDiscover_Public(t *testing.T) {
	h, _, _, _, _ := newTestHandlerWithSME(t)

	// 200 — known SME host.
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/tenant/discover?host=console.acme.otech.example", nil)
	w := httptest.NewRecorder()
	h.HandleTenantDiscover(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp tenantDiscoverResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.TenantKind != store.TenantKindSME {
		t.Errorf("TenantKind = %q, want sme", resp.TenantKind)
	}
	if resp.KeycloakRealmURL == "" || resp.KeycloakClientID == "" {
		t.Errorf("realm/client missing: %+v", resp)
	}
	// Confirm admin URLs are NOT exposed.
	body := w.Body.String()
	if strings.Contains(body, "sme_keycloak_admin_url") || strings.Contains(body, "8080") {
		t.Errorf("admin URL leaked in public response: %s", body)
	}

	// 404 — unknown host.
	req = httptest.NewRequest(http.MethodGet,
		"/api/v1/tenant/discover?host=unknown.example", nil)
	w = httptest.NewRecorder()
	h.HandleTenantDiscover(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown host status = %d, want 404", w.Code)
	}

	// 400 — missing query host AND no Host header. Conformant clients
	// always send Host, but the contract still rejects the empty case.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/tenant/discover", nil)
	req.Host = ""
	w = httptest.NewRecorder()
	h.HandleTenantDiscover(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing host status = %d, want 400", w.Code)
	}
}

// TestTenantDiscover_HostHeaderFallback asserts the canonical
// pre-auth bootstrap flow: when the SPA hits /api/v1/tenant/discover
// without an explicit `?host=` query param, the handler falls back
// to the Host header that the upstream proxy preserved (TC-R-045).
func TestTenantDiscover_HostHeaderFallback(t *testing.T) {
	h, _, _, _, _ := newTestHandlerWithSME(t)

	// No ?host= query param — Host header carries the registered tenant.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenant/discover", nil)
	req.Host = "console.acme.otech.example"
	w := httptest.NewRecorder()
	h.HandleTenantDiscover(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Host-header fallback status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp tenantDiscoverResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Host != "console.acme.otech.example" {
		t.Errorf("Host = %q, want console.acme.otech.example", resp.Host)
	}
	if resp.TenantKind != store.TenantKindSME {
		t.Errorf("TenantKind = %q, want sme", resp.TenantKind)
	}

	// Host header carries port (HTTP/1.1 :443 form) — port must still
	// be stripped before registry lookup, same as the query-param path.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/tenant/discover", nil)
	req.Host = "console.acme.otech.example:443"
	w = httptest.NewRecorder()
	h.HandleTenantDiscover(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("Host-header-with-port status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	// Explicit query param wins over Host header (callers who pass it
	// are looking up a different tenant; preserve that semantic).
	req = httptest.NewRequest(http.MethodGet,
		"/api/v1/tenant/discover?host=unknown.example", nil)
	req.Host = "console.acme.otech.example"
	w = httptest.NewRecorder()
	h.HandleTenantDiscover(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("query-param wins status = %d, want 404", w.Code)
	}
}

func TestTenantDiscover_StripsPort(t *testing.T) {
	h, _, _, _, _ := newTestHandlerWithSME(t)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/tenant/discover?host=console.acme.otech.example:443", nil)
	w := httptest.NewRecorder()
	h.HandleTenantDiscover(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (port stripped); body=%s", w.Code, w.Body.String())
	}
}

func TestTenantDiscover_503WhenUnwired(t *testing.T) {
	h := &Handler{log: slog.New(slog.NewJSONHandler(io.Discard, nil))}
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/tenant/discover?host=anything.example", nil)
	w := httptest.NewRecorder()
	h.HandleTenantDiscover(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}
