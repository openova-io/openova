// endpoint_handler_test.go — unit coverage for the G117.3 endpoint
// CRUD + IaC PR pipeline + Launch URL + multi-instance create.
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/core/controllers/pkg/gitea"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/giteapr"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/precheck"
)

// ── Test helpers ───────────────────────────────────────────────────

// seedApp builds an Application unstructured object owned by `org`
// targeting `blueprint`, with a known UID so the by-UID lookup works.
func seedApp(uid, name, ns, blueprint string) *unstructured.Unstructured {
	app := &unstructured.Unstructured{}
	app.SetAPIVersion("apps.openova.io/v1")
	app.SetKind("Application")
	app.SetName(name)
	app.SetNamespace(ns)
	app.SetUID(types.UID(uid))
	app.SetLabels(map[string]string{
		"catalyst.openova.io/organization": ns,
		"catalyst.openova.io/blueprint":    blueprint,
	})
	spec := map[string]interface{}{
		"environmentRef": ns + "-prod",
		"blueprintRef": map[string]interface{}{
			"name":    "bp-" + blueprint,
			"version": "1.0.0",
		},
		"placement": "singleton",
		"regions":   []interface{}{"primary"},
	}
	_ = unstructured.SetNestedMap(app.Object, spec, "spec")
	return app
}

// fakeEndpointDynamic returns a dynamic-client factory + the fake
// client; seeded with the given Applications.
func fakeEndpointDynamic(seed ...runtime.Object) (func() (dynamic.Interface, error), *dynamicfake.FakeDynamicClient) {
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		ApplicationGVR(): "ApplicationList",
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, seed...)
	return func() (dynamic.Interface, error) {
		return client, nil
	}, client
}

// fakeBlueprintInCatalog wires a fake CatalogClient that returns a
// blueprint with endpoints[] / sso / multiInstance / topology.
func fakeBlueprintInCatalog(name string, endpoints []map[string]interface{}, multiInst bool, topologies []string) *fakeCatalogClient {
	bp := &CatalogBlueprint{
		Name:    name,
		Version: "1.0.0",
		Raw: map[string]interface{}{
			"spec": map[string]interface{}{
				"version":   "1.0.0",
				"endpoints": endpoints,
				"sso": map[string]interface{}{
					"realm":       "sovereign",
					"silentLogin": true,
				},
				"multiInstance": map[string]interface{}{
					"enabled": multiInst,
				},
				"topology": map[string]interface{}{
					"supported": topologies,
					"defaults": map[string]interface{}{
						"multi-region":  topologies[0],
						"single-region": topologies[len(topologies)-1],
					},
				},
			},
		},
	}
	return newFakeCatalog(bp)
}

// fakeIaCAndStatus builds a fake gitea client + status checker that
// scripts all-pass — sufficient for happy-path tests.
type fakeIaCWriter struct {
	mu        sync.Mutex
	branches  map[string]bool
	files     map[string][]byte
	prs       map[string]gitea.PullRequest
	prSeq     int64
	merged    map[int64]bool
}

func newFakeIaCWriter() *fakeIaCWriter {
	return &fakeIaCWriter{
		branches: map[string]bool{},
		files:    map[string][]byte{},
		prs:      map[string]gitea.PullRequest{},
		merged:   map[int64]bool{},
	}
}

func (f *fakeIaCWriter) EnsureBranch(_ context.Context, org, repo, branch string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.branches[org+"/"+repo+"/"+branch] = true
	return nil
}

func (f *fakeIaCWriter) PutFile(_ context.Context, org, repo, branch, path string, data []byte, _ string, _ ...gitea.PutFileOpts) (gitea.File, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[org+"/"+repo+"/"+branch+"/"+path] = data
	return gitea.File{Path: path}, true, nil
}

func (f *fakeIaCWriter) DeleteFile(_ context.Context, org, repo, branch, path, _ string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.files, org+"/"+repo+"/"+branch+"/"+path)
	return true, nil
}

func (f *fakeIaCWriter) CreatePullRequest(_ context.Context, org, repo, head, base, title, body string) (gitea.PullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := org + "/" + repo + "/" + head + "→" + base
	if pr, ok := f.prs[key]; ok {
		return pr, nil
	}
	f.prSeq++
	pr := gitea.PullRequest{
		Number: f.prSeq,
		URL:    "https://gitea.t01.test/" + org + "/" + repo + "/pulls/" + itoaTest(f.prSeq),
		Title:  title,
		Body:   body,
	}
	pr.Head.Ref = head
	pr.Base.Ref = base
	f.prs[key] = pr
	return pr, nil
}

func (f *fakeIaCWriter) MergePullRequest(_ context.Context, _, _ string, number int64, _ gitea.MergePROpts) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.merged[number] = true
	return nil
}

type allPassStatus struct{}

func (allPassStatus) GetStatuses(_ context.Context, _, _, _ string) (map[string]giteapr.CheckStatus, error) {
	return map[string]giteapr.CheckStatus{
		"kyverno-admission":     giteapr.CheckPass,
		"cert-manager-precheck": giteapr.CheckPass,
		"dns-conflict-precheck": giteapr.CheckPass,
	}, nil
}

func newWriterFactoryAllPass(t *testing.T) (func(string) (*giteapr.Writer, error), *fakeIaCWriter) {
	t.Helper()
	iac := newFakeIaCWriter()
	w := giteapr.NewWriter(iac, allPassStatus{}, giteapr.PollConfig{Interval: 1, Budget: 50})
	return func(_ string) (*giteapr.Writer, error) { return w, nil }, iac
}

// passthroughCertDNS returns lookups that always indicate no existing
// owner/record — i.e. precheck pass.
func passthroughCertDNS() (precheck.CertConflictLookup, precheck.DNSConflictLookup) {
	cert := func(_ context.Context, _ string) (precheck.CertOwner, bool, error) {
		return precheck.CertOwner{}, false, nil
	}
	dns := func(_ context.Context, _ string) (string, bool, error) {
		return "", false, nil
	}
	return cert, dns
}

func itoaTest(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// newTestHandler returns a Handler with the endpoint deps prewired.
func newTestHandlerWithEndpoint(t *testing.T, seed ...runtime.Object) (*Handler, *fakeIaCWriter, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	h := NewWithPDM(slog.New(slog.NewTextHandler(io_discard{}, nil)), nil)
	dynFactory, dyn := fakeEndpointDynamic(seed...)
	factory, iac := newWriterFactoryAllPass(t)
	cert, dns := passthroughCertDNS()
	h.SetEndpointPrecheckDeps(EndpointPrecheckDeps{
		CertLookup:    cert,
		DNSLookup:     dns,
		WriterFactory: factory,
		DynamicClient: dynFactory,
		Requester:     func(_ context.Context) string { return "tester" },
		SovereignFQDN: "t01.omani.works",
	})
	return h, iac, dyn
}

// io_discard is a minimal io.Writer that drops bytes — slog-compatible.
type io_discard struct{}

func (io_discard) Write(p []byte) (int, error) { return len(p), nil }

// newTestRouter builds a chi.Router with the endpoint routes mounted.
func newTestRouter(h *Handler) *chi.Mux {
	r := chi.NewMux()
	r.Get("/catalyst/v1/apps/{id}/endpoints", h.HandleListAppEndpoints)
	r.Post("/catalyst/v1/apps/{id}/endpoints", h.HandleCreateAppEndpoint)
	r.Patch("/catalyst/v1/apps/{id}/endpoints/{name}", h.HandlePatchAppEndpoint)
	r.Delete("/catalyst/v1/apps/{id}/endpoints/{name}", h.HandleDeleteAppEndpoint)
	r.Get("/catalyst/v1/apps/{id}/launch-url", h.HandleGetLaunchURL)
	r.Post("/catalyst/v1/apps/instances", h.HandleCreateInstance)
	r.Get("/catalyst/v1/catalog/{blueprint}/instances", h.HandleListBlueprintInstances)
	return r
}

// ── Tests ──────────────────────────────────────────────────────────

func TestListAppEndpoints_HappyPath(t *testing.T) {
	app := seedApp("uid-001", "wp-prod", "acme", "wordpress")
	h, _, _ := newTestHandlerWithEndpoint(t, app)
	h.SetCatalogClient(fakeBlueprintInCatalog("wordpress",
		[]map[string]interface{}{
			{"name": "ui", "hostnameTemplate": "{AppName}.{OrgSlug}.{SovereignFQDN}", "port": 443, "protocol": "https", "tls": true, "ssoEnabled": true, "launchDefault": true},
		}, false, []string{"singleton"}))
	r := newTestRouter(h)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/catalyst/v1/apps/uid-001/endpoints", nil)
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	var resp listEndpointsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("expected 1 endpoint, got %d", len(resp.Items))
	}
	if resp.Items[0].Hostname != "wp-prod.acme.t01.omani.works" {
		t.Fatalf("unexpected hostname %q", resp.Items[0].Hostname)
	}
	if resp.Items[0].LaunchURL == "" || !strings.Contains(resp.Items[0].LaunchURL, "prompt=none") {
		t.Fatalf("expected silent-SSO LaunchURL, got %q", resp.Items[0].LaunchURL)
	}
}

func TestListAppEndpoints_NotFound(t *testing.T) {
	h, _, _ := newTestHandlerWithEndpoint(t)
	h.SetCatalogClient(newFakeCatalog())
	r := newTestRouter(h)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/catalyst/v1/apps/missing-uid/endpoints", nil)
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestCreateAppEndpoint_HappyPath_OpensAndMerges(t *testing.T) {
	app := seedApp("uid-002", "wp", "acme", "wordpress")
	h, iac, _ := newTestHandlerWithEndpoint(t, app)
	h.SetCatalogClient(newFakeCatalog())
	r := newTestRouter(h)

	body := []byte(`{"name":"ui","hostname":"wp.acme.example.com","port":443,"protocol":"https","tls":true,"visibility":"public","ssoEnabled":true}`)
	req := httptest.NewRequest("POST", "/catalyst/v1/apps/uid-002/endpoints", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	var resp endpointPRResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != string(giteapr.StatusMerged) {
		t.Fatalf("expected merged, got %s", resp.Status)
	}
	if !strings.HasPrefix(resp.PRURL, "https://gitea.t01.test/acme/iac/pulls/") {
		t.Fatalf("unexpected PR URL: %s", resp.PRURL)
	}
	if resp.PreCheckResults.CertManager != "pass" || resp.PreCheckResults.DNSConflict != "pass" || resp.PreCheckResults.Kyverno != "pass" {
		t.Fatalf("expected all checks pass, got %+v", resp.PreCheckResults)
	}
	// File landed
	found := false
	for k := range iac.files {
		if strings.HasSuffix(k, "apps/wp/endpoints/ui.yaml") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected endpoint manifest in fake IaC")
	}
}

func TestCreateAppEndpoint_DuplicateOpensSamePR(t *testing.T) {
	app := seedApp("uid-003", "wp", "acme", "wordpress")
	h, iac, _ := newTestHandlerWithEndpoint(t, app)
	h.SetCatalogClient(newFakeCatalog())
	r := newTestRouter(h)

	body := []byte(`{"name":"ui","hostname":"wp.acme.example.com","port":443,"protocol":"https","tls":true,"visibility":"public","ssoEnabled":true}`)
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("POST", "/catalyst/v1/apps/uid-003/endpoints", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("iter %d: expected 202, got %d", i, rec.Code)
		}
	}
	// Should have opened exactly ONE PR (idempotent on same manifest hash).
	if len(iac.prs) != 1 {
		t.Fatalf("expected 1 PR (idempotent); got %d", len(iac.prs))
	}
}

func TestPatchAppEndpoint_BodyNameDefaultsFromPath(t *testing.T) {
	app := seedApp("uid-004", "wp", "acme", "wordpress")
	h, iac, _ := newTestHandlerWithEndpoint(t, app)
	h.SetCatalogClient(newFakeCatalog())
	r := newTestRouter(h)

	body := []byte(`{"hostname":"wp-new.acme.example.com","port":443,"protocol":"https","tls":true,"visibility":"public","ssoEnabled":true}`)
	req := httptest.NewRequest("PATCH", "/catalyst/v1/apps/uid-004/endpoints/ui", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	found := false
	for k := range iac.files {
		if strings.HasSuffix(k, "apps/wp/endpoints/ui.yaml") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected ui.yaml manifest landed on patch")
	}
}

func TestDeleteAppEndpoint_OpensDeletePR(t *testing.T) {
	app := seedApp("uid-005", "wp", "acme", "wordpress")
	h, iac, _ := newTestHandlerWithEndpoint(t, app)
	h.SetCatalogClient(newFakeCatalog())
	// Seed the file so DeleteFile path is the one exercised
	iac.files["acme/iac/main/apps/wp/endpoints/ui.yaml"] = []byte("seed")
	r := newTestRouter(h)
	req := httptest.NewRequest("DELETE", "/catalyst/v1/apps/uid-005/endpoints/ui", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if len(iac.prs) != 1 {
		t.Fatalf("expected 1 PR for delete; got %d", len(iac.prs))
	}
}

func TestCreateAppEndpoint_FailedPrecheckDoesNotOpenPR(t *testing.T) {
	app := seedApp("uid-006", "wp", "acme", "wordpress")
	h, iac, _ := newTestHandlerWithEndpoint(t, app)
	h.SetCatalogClient(newFakeCatalog())
	// Override the cert lookup to return a different owner.
	deps := h.EndpointPrecheckDepsForTest()
	deps.CertLookup = func(_ context.Context, _ string) (precheck.CertOwner, bool, error) {
		return precheck.CertOwner{Org: "competitor", App: "x"}, true, nil
	}
	h.SetEndpointPrecheckDeps(deps)
	r := newTestRouter(h)

	body := []byte(`{"name":"ui","hostname":"wp.acme.example.com","port":443,"protocol":"https","tls":true,"visibility":"public","ssoEnabled":true}`)
	req := httptest.NewRequest("POST", "/catalyst/v1/apps/uid-006/endpoints", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if len(iac.prs) != 0 {
		t.Fatalf("expected 0 PRs on failed precheck; got %d", len(iac.prs))
	}
	var resp endpointPRResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Status != "failed-precheck" {
		t.Fatalf("expected failed-precheck status; got %s", resp.Status)
	}
	if resp.PreCheckResults.CertManager != "fail" {
		t.Fatalf("expected cert-manager fail; got %s", resp.PreCheckResults.CertManager)
	}
}

func TestCreateAppEndpoint_BadBody(t *testing.T) {
	app := seedApp("uid-007", "wp", "acme", "wordpress")
	h, _, _ := newTestHandlerWithEndpoint(t, app)
	h.SetCatalogClient(newFakeCatalog())
	r := newTestRouter(h)
	req := httptest.NewRequest("POST", "/catalyst/v1/apps/uid-007/endpoints", strings.NewReader("not-json"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestGetLaunchURL_HappyPath(t *testing.T) {
	app := seedApp("uid-008", "wp", "acme", "wordpress")
	h, _, _ := newTestHandlerWithEndpoint(t, app)
	h.SetCatalogClient(fakeBlueprintInCatalog("wordpress",
		[]map[string]interface{}{
			{"name": "ui", "hostnameTemplate": "{AppName}.{OrgSlug}.{SovereignFQDN}", "tls": true, "ssoEnabled": true, "launchDefault": true},
		}, false, []string{"singleton"}))
	r := newTestRouter(h)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/catalyst/v1/apps/uid-008/launch-url", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	var resp launchURLResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if !strings.Contains(resp.URL, "prompt=none") || !strings.Contains(resp.URL, "kc_idp_hint=catalyst-pin") {
		t.Fatalf("URL missing silent-SSO params: %s", resp.URL)
	}
	if resp.Endpoint != "ui" {
		t.Fatalf("expected endpoint=ui (launchDefault); got %s", resp.Endpoint)
	}
}

func TestGetLaunchURL_NoSSOEnabledIs409(t *testing.T) {
	app := seedApp("uid-009", "wp", "acme", "wordpress")
	h, _, _ := newTestHandlerWithEndpoint(t, app)
	h.SetCatalogClient(fakeBlueprintInCatalog("wordpress",
		[]map[string]interface{}{
			{"name": "ui", "hostnameTemplate": "ui.example.com", "tls": true, "ssoEnabled": false, "launchDefault": true},
		}, false, []string{"singleton"}))
	r := newTestRouter(h)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/catalyst/v1/apps/uid-009/launch-url?endpoint=ui", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for non-SSO endpoint; got %d", rec.Code)
	}
}

func TestCreateInstance_HappyPath(t *testing.T) {
	h, _, dyn := newTestHandlerWithEndpoint(t)
	h.SetCatalogClient(fakeBlueprintInCatalog("wordpress",
		[]map[string]interface{}{}, true, []string{"singleton", "active-hot-standby"}))
	r := newTestRouter(h)

	body := []byte(`{"blueprint":"wordpress","org":"acme","name":"wp-prod","topology":"singleton"}`)
	req := httptest.NewRequest("POST", "/catalyst/v1/apps/instances", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	var resp application
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Name != "wp-prod" || resp.Topology != "singleton" || resp.Org != "acme" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	// Confirm the CR landed
	got, err := dyn.Resource(ApplicationGVR()).Namespace("acme").Get(context.Background(), "wp-prod", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected CR landed; got %v", err)
	}
	if got.GetName() != "wp-prod" {
		t.Fatalf("got %q", got.GetName())
	}
}

func TestCreateInstance_TopologyNotSupported(t *testing.T) {
	h, _, _ := newTestHandlerWithEndpoint(t)
	h.SetCatalogClient(fakeBlueprintInCatalog("wordpress",
		[]map[string]interface{}{}, true, []string{"singleton"}))
	r := newTestRouter(h)
	body := []byte(`{"blueprint":"wordpress","org":"acme","name":"wp-prod","topology":"active-hot-standby"}`)
	req := httptest.NewRequest("POST", "/catalyst/v1/apps/instances", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestCreateInstance_MultiInstanceDisabled409(t *testing.T) {
	existing := seedApp("uid-010", "wp", "acme", "wordpress")
	h, _, _ := newTestHandlerWithEndpoint(t, existing)
	h.SetCatalogClient(fakeBlueprintInCatalog("wordpress",
		[]map[string]interface{}{}, false, []string{"singleton"}))
	r := newTestRouter(h)
	body := []byte(`{"blueprint":"wordpress","org":"acme","name":"wp-2","topology":"singleton"}`)
	req := httptest.NewRequest("POST", "/catalyst/v1/apps/instances", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestCreateInstance_NameConflict(t *testing.T) {
	existing := seedApp("uid-011", "wp", "acme", "wordpress")
	h, _, _ := newTestHandlerWithEndpoint(t, existing)
	h.SetCatalogClient(fakeBlueprintInCatalog("wordpress",
		[]map[string]interface{}{}, true, []string{"singleton"}))
	r := newTestRouter(h)
	body := []byte(`{"blueprint":"wordpress","org":"acme","name":"wp","topology":"singleton"}`)
	req := httptest.NewRequest("POST", "/catalyst/v1/apps/instances", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 (name conflict), got %d (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestListBlueprintInstances_FilterByOrg(t *testing.T) {
	a1 := seedApp("uid-100", "wp1", "acme", "wordpress")
	a2 := seedApp("uid-101", "wp2", "bigcorp", "wordpress")
	a3 := seedApp("uid-102", "graf", "acme", "grafana")
	h, _, _ := newTestHandlerWithEndpoint(t, a1, a2, a3)
	h.SetCatalogClient(newFakeCatalog())
	r := newTestRouter(h)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/catalyst/v1/catalog/wordpress/instances", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp listInstancesResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Items) != 2 {
		t.Fatalf("expected 2 wordpress instances, got %d", len(resp.Items))
	}

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/catalyst/v1/catalog/wordpress/instances?org=acme", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Items) != 1 {
		t.Fatalf("expected 1 instance with org=acme, got %d", len(resp.Items))
	}
	if resp.Items[0].Name != "wp1" {
		t.Fatalf("expected wp1, got %s", resp.Items[0].Name)
	}
}

func TestBuildLaunchURL_Format(t *testing.T) {
	u := buildLaunchURL("ui.acme.example.com", true)
	if !strings.HasPrefix(u, "https://ui.acme.example.com/?") {
		t.Fatalf("unexpected URL: %s", u)
	}
	for _, want := range []string{"prompt=none", "kc_idp_hint=catalyst-pin"} {
		if !strings.Contains(u, want) {
			t.Fatalf("URL missing %q: %s", want, u)
		}
	}
}

func TestEvaluateHostnameTemplate_BothSyntaxes(t *testing.T) {
	got := evaluateHostnameTemplate("{AppName}.{OrgSlug}.{SovereignFQDN}", "t01.example.com", "acme", "wp")
	if got != "wp.acme.t01.example.com" {
		t.Fatalf("single-curly substitution wrong: %s", got)
	}
	got2 := evaluateHostnameTemplate("{{.AppName}}.{{.OrgSlug}}.{{.SovereignFQDN}}", "t01.example.com", "acme", "wp")
	if got2 != "wp.acme.t01.example.com" {
		t.Fatalf("double-curly substitution wrong: %s", got2)
	}
}

func TestPickEndpoint_LaunchDefaultWins(t *testing.T) {
	bp := &blueprintMeta{Endpoints: []endpointDecl{
		{Name: "metrics", Protocol: "http"},
		{Name: "ui", Protocol: "https", SSOEnabled: true, LaunchDefault: true},
	}}
	ep := pickEndpoint(bp, "")
	if ep == nil || ep.Name != "ui" {
		t.Fatalf("expected ui (launchDefault); got %+v", ep)
	}
}

func TestPickEndpoint_FallbackToFirstSSOHttps(t *testing.T) {
	bp := &blueprintMeta{Endpoints: []endpointDecl{
		{Name: "metrics", Protocol: "http"},
		{Name: "api", Protocol: "https", SSOEnabled: true},
	}}
	ep := pickEndpoint(bp, "")
	if ep == nil || ep.Name != "api" {
		t.Fatalf("expected api; got %+v", ep)
	}
}

func TestChooseTopology_OverrideRespected(t *testing.T) {
	bp := &blueprintMeta{Topology: &topologyDecl{Supported: []string{"singleton", "active-active"}}}
	got, err := chooseTopology(bp, "active-active")
	if err != nil || got != "active-active" {
		t.Fatalf("expected active-active, got %q err=%v", got, err)
	}
}

func TestChooseTopology_OverrideRejectedWhenUnsupported(t *testing.T) {
	bp := &blueprintMeta{Topology: &topologyDecl{Supported: []string{"singleton"}}}
	_, err := chooseTopology(bp, "active-active")
	if err == nil {
		t.Fatal("expected error for unsupported override")
	}
}

