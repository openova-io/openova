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
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/giteapr"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/instances"
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
		// #3188: HandleListBlueprintInstances also projects bootstrap
		// HelmReleases (no-org path) — the fake must know the list kind.
		helmReleaseGVR: "HelmReleaseList",
		// #3370: the generic Context-status check reads reflected
		// credential Secrets.
		secretGVR: "SecretList",
		// #5389: resolveOrgDomain lists Organizations to answer the
		// {OrgDomain} hostname token.
		OrganizationGVR(): "OrganizationList",
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, seed...)
	return func() (dynamic.Interface, error) {
		return client, nil
	}, client
}

// fakeBlueprintInCatalog wires a fake CatalogClient that returns a
// blueprint with endpoints[] / sso / multiInstance / topology.
func fakeBlueprintInCatalog(name string, endpoints []map[string]interface{}, multiInst bool, topologies []string) *fakeCatalogClient {
	return fakeBlueprintInCatalogWithCap(name, endpoints, multiInst, 0, topologies)
}

// fakeBlueprintInCatalogWithCap is the W2.C2-aware variant: takes an
// explicit `multiInstance.maxPerOrg` cap so admission-gate tests can
// trigger the max-per-org-exceeded path.
func fakeBlueprintInCatalogWithCap(name string, endpoints []map[string]interface{}, multiInst bool, maxPerOrg int, topologies []string) *fakeCatalogClient {
	mi := map[string]interface{}{
		"enabled": multiInst,
	}
	if maxPerOrg > 0 {
		mi["maxPerOrg"] = maxPerOrg
	}
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
				"multiInstance": mi,
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
	mu       sync.Mutex
	branches map[string]bool
	files    map[string][]byte
	prs      map[string]gitea.PullRequest
	prSeq    int64
	merged   map[int64]bool
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

// #3150 — bootstrap-kit apps (grafana, harbor, …) install as HelmReleases
// with NO Application CR. The launch-url must resolve them by blueprint /
// release name and emit the app's OIDC-init path so the console Open
// button lands the operator already-logged-in. Here we seed ZERO
// Applications and address the launch-url by the blueprint name "grafana".
func TestGetLaunchURL_HRBacked_NoApplicationCR(t *testing.T) {
	// No Application CR seeded — only the catalog blueprint exists.
	h, _, _ := newTestHandlerWithEndpoint(t)
	h.SetCatalogClient(fakeBlueprintInCatalog("grafana",
		[]map[string]interface{}{
			{"name": "ui", "hostnameTemplate": "grafana.{SovereignFQDN}", "tls": true, "ssoEnabled": true, "launchDefault": true, "ssoInitPath": "/login/generic_oauth"},
		}, false, []string{"singleton"}))
	r := newTestRouter(h)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/catalyst/v1/apps/grafana/launch-url", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for HR-backed launch-url, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	var resp launchURLResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.URL != "https://grafana.t01.omani.works/login/generic_oauth" {
		t.Fatalf("expected OIDC-init URL, got %q", resp.URL)
	}
	if strings.Contains(resp.URL, "?") {
		t.Fatalf("ssoInitPath URL must carry no query string: %s", resp.URL)
	}
	if resp.Endpoint != "ui" {
		t.Fatalf("expected endpoint=ui, got %s", resp.Endpoint)
	}
}

// #3150 — the `bp-` prefixed form must resolve identically (the FE may
// pass either "grafana" or "bp-grafana").
func TestGetLaunchURL_HRBacked_BpPrefixedID(t *testing.T) {
	h, _, _ := newTestHandlerWithEndpoint(t)
	h.SetCatalogClient(fakeBlueprintInCatalog("grafana",
		[]map[string]interface{}{
			{"name": "ui", "hostnameTemplate": "grafana.{SovereignFQDN}", "tls": true, "ssoEnabled": true, "launchDefault": true, "ssoInitPath": "/login/generic_oauth"},
		}, false, []string{"singleton"}))
	r := newTestRouter(h)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/catalyst/v1/apps/bp-grafana/launch-url", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for bp-prefixed launch-url, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	var resp launchURLResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.URL != "https://grafana.t01.omani.works/login/generic_oauth" {
		t.Fatalf("expected OIDC-init URL, got %q", resp.URL)
	}
}

// #3150 back-compat — an HR-backed app whose blueprint declares NO
// ssoInitPath still works, falling back to the legacy app-root+query
// silent-SSO shape.
func TestGetLaunchURL_HRBacked_NoInitPathFallsBackLegacy(t *testing.T) {
	h, _, _ := newTestHandlerWithEndpoint(t)
	h.SetCatalogClient(fakeBlueprintInCatalog("someapp",
		[]map[string]interface{}{
			{"name": "ui", "hostnameTemplate": "someapp.{SovereignFQDN}", "tls": true, "ssoEnabled": true, "launchDefault": true},
		}, false, []string{"singleton"}))
	r := newTestRouter(h)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/catalyst/v1/apps/someapp/launch-url", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	var resp launchURLResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if !strings.Contains(resp.URL, "prompt=none") || !strings.Contains(resp.URL, "kc_idp_hint=catalyst-pin") {
		t.Fatalf("no-initPath HR app must use legacy silent-SSO shape: %s", resp.URL)
	}
}

// #3226 — when an endpoint declares ssoShim:true (OpenBao's SPA can't
// auto-redirect via a static ssoInitPath), the launch-url returns the
// server-side shim URL on the catalyst-api origin instead of the deep-link.
// window.open() on that URL follows the shim's 302 to Keycloak, giving
// grafana/harbor zero-click parity.
func TestGetLaunchURL_SSOShimReturnsShimURL(t *testing.T) {
	h, _, _ := newTestHandlerWithEndpoint(t)
	h.SetCatalogClient(fakeBlueprintInCatalog("openbao",
		[]map[string]interface{}{
			{"name": "api", "hostnameTemplate": "bao.{SovereignFQDN}", "tls": true,
				"ssoEnabled": true, "launchDefault": true,
				"ssoInitPath": "/ui/vault/auth?with=oidc", "ssoShim": true},
		}, false, []string{"singleton"}))
	r := newTestRouter(h)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/catalyst/v1/apps/openbao/launch-url", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	var resp launchURLResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	want := "https://api.t01.omani.works/catalyst/v1/apps/openbao/openbao-sso-init"
	if resp.URL != want {
		t.Fatalf("ssoShim launch URL mismatch:\n got %q\nwant %q", resp.URL, want)
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

// #3599 (EPIC #3597) — the create-from-catalog wizard writes the chosen
// placement (mode + regions + vcluster) onto the Application CR so the
// post-create Topology tab reflects it. Asserts the OBJECT form of
// spec.placement + spec.regions land verbatim.
func TestCreateInstance_PlacementStampedOnCR(t *testing.T) {
	// #5616 — the subject here is "the chosen placement is stamped
	// verbatim", not tier availability. `rtz` is only choosable on a
	// Sovereign that installs the rtz vCluster, so declare it installed;
	// the availability gate itself is pinned in
	// internal/instances/placement_availability_5616_test.go.
	instances.SetAvailableVClusterTiers("rtz")
	t.Cleanup(func() { instances.SetAvailableVClusterTiers("") })
	h, _, dyn := newTestHandlerWithEndpoint(t)
	h.SetCatalogClient(fakeBlueprintInCatalog("wordpress",
		[]map[string]interface{}{}, true, []string{"singleton", "active-hot-standby"}))
	r := newTestRouter(h)

	body := []byte(`{"blueprint":"wordpress","org":"acme","name":"wp-ha",` +
		`"topology":"active-hot-standby",` +
		`"placement":{"vcluster":"rtz","regions":["rgn-a","rgn-b"]}}`)
	req := httptest.NewRequest("POST", "/catalyst/v1/apps/instances", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (body=%s)", rec.Code, rec.Body.String())
	}

	got, err := dyn.Resource(ApplicationGVR()).Namespace("acme").Get(context.Background(), "wp-ha", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected CR landed; got %v", err)
	}

	// spec.placement is the OBJECT form: {mode, vcluster, regions}.
	pl, ok, _ := unstructured.NestedMap(got.Object, "spec", "placement")
	if !ok {
		t.Fatalf("spec.placement not an object: %+v", got.Object["spec"])
	}
	if pl["mode"] != "active-hot-standby" {
		t.Fatalf("spec.placement.mode = %v, want active-hot-standby", pl["mode"])
	}
	if pl["vcluster"] != "rtz" {
		t.Fatalf("spec.placement.vcluster = %v, want rtz", pl["vcluster"])
	}
	plRegions, _, _ := unstructured.NestedStringSlice(got.Object, "spec", "placement", "regions")
	if len(plRegions) != 2 || plRegions[0] != "rgn-a" || plRegions[1] != "rgn-b" {
		t.Fatalf("spec.placement.regions = %v, want [rgn-a rgn-b]", plRegions)
	}
	// spec.regions mirrors the placement regions (what the Topology tab
	// + the application-controller fan-out read).
	regions, _, _ := unstructured.NestedStringSlice(got.Object, "spec", "regions")
	if len(regions) != 2 || regions[0] != "rgn-a" || regions[1] != "rgn-b" {
		t.Fatalf("spec.regions = %v, want [rgn-a rgn-b]", regions)
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

// seedSlotHR builds a bootstrap-kit slot HelmRelease for bp-postgres
// (HR bp-postgres-shared, releaseName shared-pg) carrying the
// Git-committed Context declarations in spec.values.databases — the
// #3370 generic-projection source.
func seedSlotHR(ready bool) *unstructured.Unstructured {
	status := "False"
	if ready {
		status = "True"
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "helm.toolkit.fluxcd.io/v2",
		"kind":       "HelmRelease",
		"metadata": map[string]interface{}{
			"name":      "bp-postgres-shared",
			"namespace": "flux-system",
			"uid":       "uid-hr-postgres-shared",
		},
		"spec": map[string]interface{}{
			"releaseName":     "shared-pg",
			"targetNamespace": "shared-data",
			"chart":           map[string]interface{}{"spec": map[string]interface{}{"chart": "bp-postgres"}},
			"values": map[string]interface{}{
				"topology": map[string]interface{}{"mode": "singleton"},
				"databases": []interface{}{
					map[string]interface{}{
						"name":     "gitea",
						"owner":    "gitea",
						"consumer": map[string]interface{}{"blueprint": "bp-gitea", "mode": "shared"},
						"reflect":  map[string]interface{}{"secretName": "gitea-database-secret", "namespaces": []interface{}{"gitea"}},
					},
					map[string]interface{}{
						"name":     "registry",
						"owner":    "harbor",
						"consumer": map[string]interface{}{"blueprint": "bp-harbor", "mode": "shared"},
						"reflect":  map[string]interface{}{"secretName": "harbor-database-secret", "namespaces": []interface{}{"harbor"}},
					},
				},
			},
		},
		"status": map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{"type": "Ready", "status": status},
			},
		},
	}}
}

// seedSecret builds a reflected credential Secret (what bp-reflector
// pushes into the consumer namespace).
func seedSecret(name, ns string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata":   map[string]interface{}{"name": name, "namespace": ns, "uid": "uid-secret-" + ns + "-" + name},
	}}
}

// seedBootstrapOwnedAppCR mirrors what bp-postgres 0.1.6 self-registers
// for slot 16a: the canonical Application CR named after the instance,
// spec.bootstrap=true, the owning HR ref, and the Context declarations
// riding spec.parameters verbatim.
func seedBootstrapOwnedAppCR() *unstructured.Unstructured {
	app := &unstructured.Unstructured{}
	app.SetAPIVersion("apps.openova.io/v1")
	app.SetKind("Application")
	app.SetName("shared-pg")
	app.SetNamespace("shared-data")
	app.SetUID(types.UID("uid-app-shared-pg"))
	app.SetLabels(map[string]string{
		"apps.openova.io/bootstrap-owned":  "true",
		"catalyst.openova.io/organization": "platform",
	})
	app.Object["spec"] = map[string]interface{}{
		"bootstrap":    true,
		"blueprintRef": map[string]interface{}{"name": "bp-postgres", "version": "0.1.6"},
		"placement":    "single-region",
		"helmRelease":  map[string]interface{}{"name": "bp-postgres-shared", "namespace": "flux-system"},
		"parameters": map[string]interface{}{
			"topology": map[string]interface{}{"mode": "singleton"},
			"databases": []interface{}{
				map[string]interface{}{
					"name":     "gitea",
					"owner":    "gitea",
					"consumer": map[string]interface{}{"blueprint": "bp-gitea", "mode": "shared"},
					"reflect":  map[string]interface{}{"secretName": "gitea-database-secret", "namespaces": []interface{}{"gitea"}},
				},
			},
		},
	}
	app.Object["status"] = map[string]interface{}{"phase": "Ready"}
	return app
}

// shareablePostgresCatalog wires a fake catalog whose bp-postgres
// declares shareable + contextSchema (#3370) — the declaration every
// generic surface renders from.
func shareablePostgresCatalog() *fakeCatalogClient {
	return newFakeCatalog(&CatalogBlueprint{
		Name:    "bp-postgres",
		Version: "0.1.6",
		Raw: map[string]interface{}{
			"spec": map[string]interface{}{
				"version":   "0.1.6",
				"shareable": true,
				"contextSchema": map[string]interface{}{
					"kind":      "db",
					"valuesKey": "databases",
					"needs":     []interface{}{"name", "owner"},
					"produces":  []interface{}{"credentialSecret"},
				},
				"multiInstance": map[string]interface{}{"enabled": true},
				"topology": map[string]interface{}{
					"supported": []interface{}{"singleton", "active-hot-standby"},
					"defaults": map[string]interface{}{
						"multi-region":  "active-hot-standby",
						"single-region": "singleton",
					},
				},
			},
		},
	})
}

// TestListBlueprintInstances_HRFallback_ContextsProjected — #3370. On
// an env whose self-registered Application CR hasn\u2019t materialised yet
// (pre-upgrade), the slot HR projects as ONE instance row named after
// its releaseName, with Contexts read GENERICALLY from spec.values per
// the contextSchema declaration. Status: gitea\u2019s credential Secret is
// reflected \u2192 ready; harbor\u2019s is not yet \u2192 pending.
func TestListBlueprintInstances_HRFallback_ContextsProjected(t *testing.T) {
	h, _, _ := newTestHandlerWithEndpoint(t, seedSlotHR(true), seedSecret("gitea-database-secret", "gitea"))
	h.SetCatalogClient(shareablePostgresCatalog())
	r := newTestRouter(h)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/catalyst/v1/catalog/postgres/instances", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	var resp listInstancesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("expected exactly 1 instance row, got %d: %+v", len(resp.Items), resp.Items)
	}
	row := resp.Items[0]
	if row.Name != "shared-pg" || row.Org != "platform" || row.Status != "Ready" || row.Topology != "singleton" {
		t.Fatalf("row = %+v, want name=shared-pg org=platform status=Ready topology=singleton", row)
	}
	if len(row.Contexts) != 2 {
		t.Fatalf("contexts = %+v, want the 2 declared entries", row.Contexts)
	}
	wantGitea := contextRow{Name: "gitea", Kind: "db", OccupiedBy: "gitea", Credential: "gitea-database-secret", Status: "ready"}
	if row.Contexts[0] != wantGitea {
		t.Fatalf("contexts[0] = %+v, want %+v", row.Contexts[0], wantGitea)
	}
	wantHarbor := contextRow{Name: "registry", Kind: "db", OccupiedBy: "harbor", Credential: "harbor-database-secret", Status: "pending"}
	if row.Contexts[1] != wantHarbor {
		t.Fatalf("contexts[1] = %+v, want %+v", row.Contexts[1], wantHarbor)
	}
}

// TestListBlueprintInstances_AdoptionDedup — #3370 DoD-2/4. When the
// self-registered Application CR exists ALONGSIDE its slot HR, exactly
// ONE row renders (the CR, authoritative by uid) — the HR projection
// dedupes on spec.releaseName. Contexts project from the CR\u2019s
// spec.parameters (the same Git-committed bytes).
func TestListBlueprintInstances_AdoptionDedup(t *testing.T) {
	h, _, _ := newTestHandlerWithEndpoint(t,
		seedBootstrapOwnedAppCR(), seedSlotHR(true), seedSecret("gitea-database-secret", "gitea"))
	h.SetCatalogClient(shareablePostgresCatalog())
	r := newTestRouter(h)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/catalyst/v1/catalog/postgres/instances", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	var resp listInstancesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("ADOPTION DEDUP VIOLATION: expected exactly 1 row for one physical instance, got %d: %+v", len(resp.Items), resp.Items)
	}
	row := resp.Items[0]
	if row.ID != "uid-app-shared-pg" {
		t.Fatalf("row.ID = %q — the Application CR must be authoritative over the HR projection", row.ID)
	}
	if len(row.Contexts) != 1 || row.Contexts[0].Kind != "db" || row.Contexts[0].Name != "gitea" || row.Contexts[0].Status != "ready" {
		t.Fatalf("contexts = %+v, want the CR-declared db/gitea ready row", row.Contexts)
	}
}

// TestCreateInstance_BackingReuse — #3370 ADVANCED journey: reusing an
// existing instance appends the consumer\u2019s Context entry to the
// target\u2019s declared IaC and stamps spec.dependsOn on the consumer. NO
// new backing application.
func TestCreateInstance_BackingReuse(t *testing.T) {
	// An existing controller-managed bp-postgres instance.
	target := seedApp("uid-pg-demo", "demo-pg", "acme", "postgres")
	_ = unstructured.SetNestedMap(target.Object, map[string]interface{}{}, "spec", "parameters")

	h, _, dyn := newTestHandlerWithEndpoint(t, target)
	h.SetCatalogClient(newFakeCatalog(
		shareablePostgresCatalog().byKey["bp-postgres@0.1.6"],
		fakeBlueprintInCatalog("wordpress", []map[string]interface{}{}, true, []string{"singleton"}).byKey["wordpress@1.0.0"],
	))
	r := newTestRouter(h)

	body := []byte(`{"blueprint":"wordpress","org":"acme","name":"wiki","topology":"singleton",` +
		`"backing":[{"blueprint":"postgres","mode":"reuse","instance":"demo-pg"}]}`)
	req := httptest.NewRequest("POST", "/catalyst/v1/apps/instances", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (body=%s)", rec.Code, rec.Body.String())
	}

	// (a) the consumer CR carries the dependsOn edge + Context ref.
	consumer, err := dyn.Resource(ApplicationGVR()).Namespace("acme").Get(context.Background(), "wiki", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("consumer CR missing: %v", err)
	}
	deps, _, _ := unstructured.NestedSlice(consumer.Object, "spec", "dependsOn")
	if len(deps) != 1 {
		t.Fatalf("consumer dependsOn = %+v, want 1 entry", deps)
	}
	dep := deps[0].(map[string]interface{})
	if dep["name"] != "demo-pg" || dep["namespace"] != "acme" || dep["context"] != "db/wiki" {
		t.Fatalf("dependsOn entry = %+v, want demo-pg/acme db/wiki", dep)
	}

	// (b) the TARGET instance gained the Context entry in its IaC.
	got, err := dyn.Resource(ApplicationGVR()).Namespace("acme").Get(context.Background(), "demo-pg", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("target CR: %v", err)
	}
	params, _, _ := unstructured.NestedMap(got.Object, "spec", "parameters")
	entries, _ := params["databases"].([]interface{})
	if len(entries) != 1 {
		t.Fatalf("target databases = %+v, want the appended wiki Context", params)
	}
	entry := entries[0].(map[string]interface{})
	if entry["name"] != "wiki" || entry["owner"] != "wiki" {
		t.Fatalf("context entry = %+v, want name/owner wiki", entry)
	}

	// (c) NO new backing application was created.
	list, _ := dyn.Resource(ApplicationGVR()).Namespace("").List(context.Background(), metav1.ListOptions{})
	if len(list.Items) != 2 {
		t.Fatalf("expected exactly 2 Application CRs (demo-pg + wiki), got %d", len(list.Items))
	}
}

// TestCreateInstance_BackingCreate — #3370 DEFAULT journey: the backing
// service is auto-created as its OWN instance-application (own card)
// with a Context for the consumer; the consumer carries the dependsOn
// edge. The operator sees TWO new cards.
func TestCreateInstance_BackingCreate(t *testing.T) {
	h, _, dyn := newTestHandlerWithEndpoint(t)
	h.SetCatalogClient(newFakeCatalog(
		shareablePostgresCatalog().byKey["bp-postgres@0.1.6"],
		fakeBlueprintInCatalog("wordpress", []map[string]interface{}{}, true, []string{"singleton"}).byKey["wordpress@1.0.0"],
	))
	r := newTestRouter(h)

	body := []byte(`{"blueprint":"wordpress","org":"acme","name":"wiki","topology":"singleton",` +
		`"backing":[{"blueprint":"postgres"}]}`)
	req := httptest.NewRequest("POST", "/catalyst/v1/apps/instances", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (body=%s)", rec.Code, rec.Body.String())
	}

	// The backing instance exists as its OWN Application (own card)
	// with the consumer\u2019s Context declared in its IaC values.
	backing, err := dyn.Resource(ApplicationGVR()).Namespace("acme").Get(context.Background(), "wiki-postgres", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("backing CR missing: %v", err)
	}
	params, _, _ := unstructured.NestedMap(backing.Object, "spec", "parameters")
	entries, _ := params["databases"].([]interface{})
	if len(entries) != 1 {
		t.Fatalf("backing databases = %+v, want 1 Context for the consumer", params)
	}
	entry := entries[0].(map[string]interface{})
	cons, _ := entry["consumer"].(map[string]interface{})
	if cons["blueprint"] != "bp-wordpress" {
		t.Fatalf("backing context consumer = %+v, want bp-wordpress", cons)
	}

	// The consumer carries the dependsOn edge to the backing instance.
	consumer, err := dyn.Resource(ApplicationGVR()).Namespace("acme").Get(context.Background(), "wiki", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("consumer CR missing: %v", err)
	}
	deps, _, _ := unstructured.NestedSlice(consumer.Object, "spec", "dependsOn")
	if len(deps) != 1 {
		t.Fatalf("consumer dependsOn = %+v, want 1 entry", deps)
	}
	dep := deps[0].(map[string]interface{})
	if dep["name"] != "wiki-postgres" || dep["context"] != "db/wiki" {
		t.Fatalf("dependsOn entry = %+v, want wiki-postgres db/wiki", dep)
	}
}

// TestCreateInstance_BackingReuse_BootstrapOwnedRejected — #3370. A
// bootstrap-owned instance\u2019s Contexts live in the bootstrap-kit slot
// values in Git; the API must refuse to append to the CR.
func TestCreateInstance_BackingReuse_BootstrapOwnedRejected(t *testing.T) {
	h, _, _ := newTestHandlerWithEndpoint(t, seedBootstrapOwnedAppCR())
	h.SetCatalogClient(newFakeCatalog(
		shareablePostgresCatalog().byKey["bp-postgres@0.1.6"],
		fakeBlueprintInCatalog("wordpress", []map[string]interface{}{}, true, []string{"singleton"}).byKey["wordpress@1.0.0"],
	))
	r := newTestRouter(h)

	body := []byte(`{"blueprint":"wordpress","org":"acme","name":"wiki","topology":"singleton",` +
		`"backing":[{"blueprint":"postgres","mode":"reuse","instance":"shared-pg"}]}`)
	req := httptest.NewRequest("POST", "/catalyst/v1/apps/instances", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "backing-bootstrap-owned") {
		t.Fatalf("expected backing-bootstrap-owned code, body=%s", rec.Body.String())
	}
}

func TestBuildLaunchURL_Format(t *testing.T) {
	u := buildLaunchURL("ui.acme.example.com", true, "")
	if !strings.HasPrefix(u, "https://ui.acme.example.com/?") {
		t.Fatalf("unexpected URL: %s", u)
	}
	for _, want := range []string{"prompt=none", "kc_idp_hint=catalyst-pin"} {
		if !strings.Contains(u, want) {
			t.Fatalf("URL missing %q: %s", want, u)
		}
	}
}

// #3150 — when an endpoint declares an ssoInitPath, the launch URL must
// target the app's OWN OIDC-init route (e.g. Grafana
// `/login/generic_oauth`) with NO query string, so the app immediately
// 302s to Keycloak (kc_idp_hint pre-baked into the app's auth_url) and
// the browser lands inside the app already logged in — no app login form.
func TestBuildLaunchURL_SSOInitPath(t *testing.T) {
	u := buildLaunchURL("grafana.t01.example.com", true, "/login/generic_oauth")
	if u != "https://grafana.t01.example.com/login/generic_oauth" {
		t.Fatalf("ssoInitPath URL wrong: %s", u)
	}
	if strings.Contains(u, "?") || strings.Contains(u, "prompt=none") || strings.Contains(u, "kc_idp_hint") {
		t.Fatalf("ssoInitPath URL must carry no query string: %s", u)
	}
	// Leading-slash normalisation: a path without a leading slash still
	// renders a valid absolute path.
	u2 := buildLaunchURL("harbor.t01.example.com", true, "c/oidc/login")
	if u2 != "https://harbor.t01.example.com/c/oidc/login" {
		t.Fatalf("ssoInitPath normalisation wrong: %s", u2)
	}
	// Empty ssoInitPath falls back to the legacy app-root + query shape.
	u3 := buildLaunchURL("app.t01.example.com", true, "")
	if !strings.HasPrefix(u3, "https://app.t01.example.com/?") {
		t.Fatalf("empty ssoInitPath must fall back to legacy shape: %s", u3)
	}
}

func TestEvaluateHostnameTemplate_BothSyntaxes(t *testing.T) {
	v := hostnameVars{SovereignFQDN: "t01.example.com", OrgSlug: "acme", AppName: "wp", OrgDomain: "acme.omani.homes"}
	got := evaluateHostnameTemplate("{AppName}.{OrgSlug}.{SovereignFQDN}", v)
	if got != "wp.acme.t01.example.com" {
		t.Fatalf("single-curly substitution wrong: %s", got)
	}
	got2 := evaluateHostnameTemplate("{{.AppName}}.{{.OrgSlug}}.{{.SovereignFQDN}}", v)
	if got2 != "wp.acme.t01.example.com" {
		t.Fatalf("double-curly substitution wrong: %s", got2)
	}
	// #5389 — the OrgDomain token in all three accepted syntaxes.
	for _, tmpl := range []string{"{AppName}.{OrgDomain}", "{{.AppName}}.{{.OrgDomain}}", "{{ .AppName }}.{{ .OrgDomain }}"} {
		if got := evaluateHostnameTemplate(tmpl, v); got != "wp.acme.omani.homes" {
			t.Fatalf("OrgDomain substitution wrong for %q: %s", tmpl, got)
		}
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
	got, err := chooseTopology(bp, "active-active", false)
	if err != nil || got != "active-active" {
		t.Fatalf("expected active-active, got %q err=%v", got, err)
	}
}

func TestChooseTopology_OverrideRejectedWhenUnsupported(t *testing.T) {
	bp := &blueprintMeta{Topology: &topologyDecl{Supported: []string{"singleton"}}}
	_, err := chooseTopology(bp, "active-active", false)
	if err == nil {
		t.Fatal("expected error for unsupported override")
	}
}

// TestChooseTopology_CanonicalisesEditorDialect (#3648) — the catalyst-ui
// posts the placement-editor dialect (single-region / active-hotstandby)
// while the Blueprint declares the canonical vocabulary (singleton /
// active-hot-standby). chooseTopology must canonicalise the override so the
// create / placement-change resolves instead of failing "not in supported"
// (the founder's live postgres-create failure).
func TestChooseTopology_CanonicalisesEditorDialect(t *testing.T) {
	bp := &blueprintMeta{Topology: &topologyDecl{Supported: []string{"singleton", "active-hot-standby"}}}
	cases := []struct {
		override string
		want     string
	}{
		{"active-hotstandby", "active-hot-standby"},  // the founder's failing case
		{"active-hot-standby", "active-hot-standby"}, // already canonical
		{"single-region", "singleton"},               // editor dialect → singleton
		{"singleton", "singleton"},                   // already canonical
	}
	for _, c := range cases {
		got, err := chooseTopology(bp, c.override, true)
		if err != nil || got != c.want {
			t.Fatalf("override %q: expected %q, got %q err=%v", c.override, c.want, got, err)
		}
	}
	// An unknown value is still rejected against Supported.
	if _, err := chooseTopology(bp, "active-active", true); err == nil {
		t.Fatal("expected error: active-active not in [singleton active-hot-standby]")
	}
}

// ── G117 W2.C2 — multi-instance admission gate integration tests ───

// TestCreateInstance_MultiInstanceEnabled_3Instances exercises the
// happy path of the DoD: 3 grafana instances succeed in same Org when
// multiInstance.enabled=true. Each instance lands with a distinct
// metadata.uid AND a distinct spec.instanceId.
func TestCreateInstance_MultiInstanceEnabled_3Instances(t *testing.T) {
	h, _, dyn := newTestHandlerWithEndpoint(t)
	h.SetCatalogClient(fakeBlueprintInCatalogWithCap("grafana", nil, true, 5, []string{"singleton"}))
	r := newTestRouter(h)

	instanceIDs := map[string]bool{}
	names := []string{"obs-1", "obs-2", "obs-3"}

	for _, name := range names {
		body := []byte(`{"blueprint":"grafana","org":"acme","name":"` + name + `"}`)
		req := httptest.NewRequest("POST", "/catalyst/v1/apps/instances", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("name=%s: expected 201, got %d (body=%s)", name, rec.Code, rec.Body.String())
		}
		var resp application
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("name=%s decode: %v", name, err)
		}
		if resp.Name != name {
			t.Fatalf("name=%s: response Name=%q", name, resp.Name)
		}
	}

	// Verify the 3 CRs landed with distinct instanceIds.
	list, err := dyn.Resource(ApplicationGVR()).Namespace("acme").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list apps: %v", err)
	}
	if len(list.Items) != 3 {
		t.Fatalf("expected 3 Applications, got %d", len(list.Items))
	}
	for _, item := range list.Items {
		id, found, _ := unstructured.NestedString(item.Object, "spec", "instanceId")
		if !found || id == "" {
			t.Fatalf("Application %s missing spec.instanceId", item.GetName())
		}
		if instanceIDs[id] {
			t.Fatalf("duplicate instanceId %s on Application %s", id, item.GetName())
		}
		instanceIDs[id] = true
		iso, _, _ := unstructured.NestedString(item.Object, "spec", "isolationLevel")
		if iso != "namespace" {
			t.Fatalf("expected isolationLevel=namespace default, got %q", iso)
		}
		nt, _, _ := unstructured.NestedString(item.Object, "spec", "namingTemplate")
		if nt != "{{.AppName}}-{{.InstanceID}}" {
			t.Fatalf("expected namingTemplate default, got %q", nt)
		}
	}
}

// TestCreateInstance_MultiInstanceDisabled_BlocksSecond verifies the
// 409 + multi-instance-disabled contract.
func TestCreateInstance_MultiInstanceDisabled_BlocksSecond(t *testing.T) {
	existing := seedApp("uid-mi-disabled-1", "marketing-1", "acme", "wordpress")
	h, _, _ := newTestHandlerWithEndpoint(t, existing)
	h.SetCatalogClient(fakeBlueprintInCatalogWithCap("wordpress", nil, false, 0, []string{"singleton"}))
	r := newTestRouter(h)

	body := []byte(`{"blueprint":"wordpress","org":"acme","name":"marketing-2"}`)
	req := httptest.NewRequest("POST", "/catalyst/v1/apps/instances", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	var out map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode err: %v", err)
	}
	if c, _ := out["code"].(string); c != "multi-instance-disabled" {
		t.Fatalf("expected code=multi-instance-disabled, got %v (msg=%v)", out["code"], out["message"])
	}
}

// TestCreateInstance_MaxPerOrgExceeded verifies the 409 +
// max-per-org-exceeded contract.
func TestCreateInstance_MaxPerOrgExceeded(t *testing.T) {
	a := seedApp("uid-cap-1", "obs-1", "acme", "grafana")
	b := seedApp("uid-cap-2", "obs-2", "acme", "grafana")
	c := seedApp("uid-cap-3", "obs-3", "acme", "grafana")
	h, _, _ := newTestHandlerWithEndpoint(t, a, b, c)
	h.SetCatalogClient(fakeBlueprintInCatalogWithCap("grafana", nil, true, 3, []string{"singleton"}))
	r := newTestRouter(h)

	body := []byte(`{"blueprint":"grafana","org":"acme","name":"obs-4"}`)
	req := httptest.NewRequest("POST", "/catalyst/v1/apps/instances", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	var out map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode err: %v", err)
	}
	if c, _ := out["code"].(string); c != "max-per-org-exceeded" {
		t.Fatalf("expected code=max-per-org-exceeded, got %v (msg=%v)", out["code"], out["message"])
	}
}

// TestCreateInstance_NameCollision verifies that name collision is
// rejected regardless of multiInstance.enabled.
func TestCreateInstance_NameCollision_MultiInstanceEnabled(t *testing.T) {
	existing := seedApp("uid-coll-1", "obs-1", "acme", "grafana")
	h, _, _ := newTestHandlerWithEndpoint(t, existing)
	h.SetCatalogClient(fakeBlueprintInCatalogWithCap("grafana", nil, true, 5, []string{"singleton"}))
	r := newTestRouter(h)

	body := []byte(`{"blueprint":"grafana","org":"acme","name":"obs-1"}`)
	req := httptest.NewRequest("POST", "/catalyst/v1/apps/instances", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	var out map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode err: %v", err)
	}
	if c, _ := out["code"].(string); c != "name-collision" {
		t.Fatalf("expected code=name-collision, got %v (msg=%v)", out["code"], out["message"])
	}
}

// TestCreateInstance_IsolationLevelInvalid verifies the 422
// isolation-level-invalid contract.
func TestCreateInstance_IsolationLevelInvalid(t *testing.T) {
	h, _, _ := newTestHandlerWithEndpoint(t)
	h.SetCatalogClient(fakeBlueprintInCatalogWithCap("grafana", nil, true, 5, []string{"singleton"}))
	r := newTestRouter(h)

	body := []byte(`{"blueprint":"grafana","org":"acme","name":"obs-1","isolationLevel":"host"}`)
	req := httptest.NewRequest("POST", "/catalyst/v1/apps/instances", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	var out map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode err: %v", err)
	}
	if c, _ := out["code"].(string); c != "isolation-level-invalid" {
		t.Fatalf("expected code=isolation-level-invalid, got %v (msg=%v)", out["code"], out["message"])
	}
}

// TestCreateInstance_VClusterIsolation verifies the vCluster
// isolation flow stamps the AppName-only namingTemplate.
func TestCreateInstance_VClusterIsolation_NamingTemplateDefault(t *testing.T) {
	h, _, dyn := newTestHandlerWithEndpoint(t)
	h.SetCatalogClient(fakeBlueprintInCatalogWithCap("grafana", nil, true, 5, []string{"singleton"}))
	r := newTestRouter(h)

	body := []byte(`{"blueprint":"grafana","org":"acme","name":"obs-1","isolationLevel":"vcluster"}`)
	req := httptest.NewRequest("POST", "/catalyst/v1/apps/instances", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	list, _ := dyn.Resource(ApplicationGVR()).Namespace("acme").List(context.Background(), metav1.ListOptions{})
	if len(list.Items) != 1 {
		t.Fatalf("expected 1 Application, got %d", len(list.Items))
	}
	iso, _, _ := unstructured.NestedString(list.Items[0].Object, "spec", "isolationLevel")
	if iso != "vcluster" {
		t.Fatalf("expected isolationLevel=vcluster, got %q", iso)
	}
	nt, _, _ := unstructured.NestedString(list.Items[0].Object, "spec", "namingTemplate")
	if nt != "{{.AppName}}" {
		t.Fatalf("expected namingTemplate={{.AppName}} for vcluster, got %q", nt)
	}
}

// ── G117.3d #2780 — topology multi-region detection from
// Sovereign.spec.regions count, not env var.

func TestChooseTopology_MultiRegionPicksMultiRegionDefault(t *testing.T) {
	bp := &blueprintMeta{Topology: &topologyDecl{
		Supported: []string{"singleton", "active-hot-standby", "active-active"},
		Defaults:  topologyDefaults{MultiRegion: "active-hot-standby", SingleRegion: "singleton"},
	}}
	got, err := chooseTopology(bp, "", true)
	if err != nil || got != "active-hot-standby" {
		t.Fatalf("expected active-hot-standby (multi-region default), got %q err=%v", got, err)
	}
}

func TestChooseTopology_SingleRegionPicksSingleRegionDefault(t *testing.T) {
	bp := &blueprintMeta{Topology: &topologyDecl{
		Supported: []string{"singleton", "active-hot-standby"},
		Defaults:  topologyDefaults{MultiRegion: "active-hot-standby", SingleRegion: "singleton"},
	}}
	got, err := chooseTopology(bp, "", false)
	if err != nil || got != "singleton" {
		t.Fatalf("expected singleton (single-region default), got %q err=%v", got, err)
	}
}

func TestDetectMultiRegion_CounterTakesPrecedenceOverEnv(t *testing.T) {
	t.Setenv("SOVEREIGN_REGIONS", "a,b,c") // env says multi
	h := &Handler{}
	h.SetEndpointPrecheckDeps(EndpointPrecheckDeps{
		RegionsCounter: func(_ context.Context) (int, error) { return 1, nil }, // CR says single
	})
	if h.detectMultiRegion(context.Background()) {
		t.Fatal("expected single-region (CR has 1 region) despite env=multi")
	}
}

func TestDetectMultiRegion_CounterMultiReturnsTrue(t *testing.T) {
	h := &Handler{}
	h.SetEndpointPrecheckDeps(EndpointPrecheckDeps{
		RegionsCounter: func(_ context.Context) (int, error) { return 3, nil },
	})
	if !h.detectMultiRegion(context.Background()) {
		t.Fatal("expected multi-region when counter returns 3")
	}
}

func TestDetectMultiRegion_CounterZeroIsSingle(t *testing.T) {
	h := &Handler{}
	h.SetEndpointPrecheckDeps(EndpointPrecheckDeps{
		RegionsCounter: func(_ context.Context) (int, error) { return 0, nil },
	})
	if h.detectMultiRegion(context.Background()) {
		t.Fatal("expected single-region when counter returns 0")
	}
}

func TestDetectMultiRegion_FallbackToEnvWhenCounterNil(t *testing.T) {
	t.Setenv("SOVEREIGN_REGIONS", "code-a,code-b")
	h := &Handler{}
	if !h.detectMultiRegion(context.Background()) {
		t.Fatal("expected multi-region from env fallback")
	}
	t.Setenv("SOVEREIGN_REGIONS", "code-a")
	if h.detectMultiRegion(context.Background()) {
		t.Fatal("expected single-region from env fallback")
	}
	t.Setenv("SOVEREIGN_REGIONS", "")
	if h.detectMultiRegion(context.Background()) {
		t.Fatal("expected single-region when env empty")
	}
}

func TestCountSovereignRegions_PicksLargest(t *testing.T) {
	makeSov := func(name string, regions []string) *unstructured.Unstructured {
		s := &unstructured.Unstructured{}
		s.SetAPIVersion("catalyst.openova.io/v1alpha1")
		s.SetKind("Sovereign")
		s.SetName(name)
		regs := make([]interface{}, len(regions))
		for i, r := range regions {
			regs[i] = map[string]interface{}{"code": r}
		}
		_ = unstructured.SetNestedSlice(s.Object, regs, "spec", "regions")
		return s
	}
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		SovereignGVR(): "SovereignList",
	}
	c := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds,
		makeSov("hw01", []string{"r1"}),
		makeSov("hw02", []string{"r1", "r2", "r3"}),
	)
	got, err := CountSovereignRegions(context.Background(), c)
	if err != nil {
		t.Fatalf("count failed: %v", err)
	}
	if got != 3 {
		t.Fatalf("expected 3 (largest of the two Sovereigns), got %d", got)
	}
}

// ── G117.3a #2757 — Org-membership gate on endpoint mutation.

// withTestClaims wraps a request with auth.Claims in the chi context.
// The existing handler reads `auth.ClaimsFromContext(r.Context())` —
// the gate fires only when claims != nil. Named with the `Test` infix
// to avoid collision with `withClaims` in continuum_test.go which has
// a different (ctx, *Claims) -> ctx signature.
func withTestClaims(req *http.Request, c *testClaimsSpec) *http.Request {
	if c == nil {
		return req
	}
	claims := &auth.Claims{
		Email:  c.Email,
		Tier:   c.Tier,
		Org:    c.Org,
		Groups: append([]string{}, c.Groups...),
	}
	for _, r := range c.RealmRoles {
		claims.RealmAccess.Roles = append(claims.RealmAccess.Roles, r)
	}
	return req.WithContext(withClaims(req.Context(), claims))
}

// testClaimsSpec is a minimal builder for auth.Claims so tests stay
// readable without setting the dozen unrelated fields the real struct
// carries.
type testClaimsSpec struct {
	Email      string
	Tier       string
	Org        string
	Groups     []string
	RealmRoles []string
}

func TestCreateAppEndpoint_DenyCrossOrgWhenCallerNotMember(t *testing.T) {
	// Application owned by `acme`; caller is tier-admin in `beta`.
	app := seedApp("uid-201", "wp", "acme", "wordpress")
	h, _, _ := newTestHandlerWithEndpoint(t, app)
	h.SetCatalogClient(newFakeCatalog())
	r := newTestRouter(h)

	body := []byte(`{"name":"ui","hostname":"wp.acme.example.com","port":443,"protocol":"https","tls":true,"visibility":"public","ssoEnabled":true}`)
	req := httptest.NewRequest("POST", "/catalyst/v1/apps/uid-201/endpoints", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withTestClaims(req, &testClaimsSpec{
		Tier:       "admin",
		Org:        "beta", // ← different Org
		RealmRoles: []string{"catalyst-admin-beta"},
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "forbidden-cross-org") {
		t.Fatalf("expected forbidden-cross-org code, got body=%s", rec.Body.String())
	}
}

func TestCreateAppEndpoint_AllowSameOrgTierAdmin(t *testing.T) {
	app := seedApp("uid-202", "wp", "acme", "wordpress")
	h, _, _ := newTestHandlerWithEndpoint(t, app)
	h.SetCatalogClient(newFakeCatalog())
	r := newTestRouter(h)

	body := []byte(`{"name":"ui","hostname":"wp.acme.example.com","port":443,"protocol":"https","tls":true,"visibility":"public","ssoEnabled":true}`)
	req := httptest.NewRequest("POST", "/catalyst/v1/apps/uid-202/endpoints", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withTestClaims(req, &testClaimsSpec{
		Tier: "admin",
		Org:  "acme",
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestCreateAppEndpoint_AllowSovereignCrossOrgRole(t *testing.T) {
	app := seedApp("uid-203", "wp", "acme", "wordpress")
	h, _, _ := newTestHandlerWithEndpoint(t, app)
	h.SetCatalogClient(newFakeCatalog())
	r := newTestRouter(h)

	body := []byte(`{"name":"ui","hostname":"wp.acme.example.com","port":443,"protocol":"https","tls":true,"visibility":"public","ssoEnabled":true}`)
	req := httptest.NewRequest("POST", "/catalyst/v1/apps/uid-203/endpoints", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// caller in beta but holds sovereign-admin → allowed.
	req = withTestClaims(req, &testClaimsSpec{
		Tier:       "admin",
		Org:        "beta",
		RealmRoles: []string{"sovereign-admin"},
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 (sovereign-admin bypass), got %d (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestCreateAppEndpoint_AllowViaGroupsClaim(t *testing.T) {
	app := seedApp("uid-204", "wp", "acme", "wordpress")
	h, _, _ := newTestHandlerWithEndpoint(t, app)
	h.SetCatalogClient(newFakeCatalog())
	r := newTestRouter(h)

	body := []byte(`{"name":"ui","hostname":"wp.acme.example.com","port":443,"protocol":"https","tls":true,"visibility":"public","ssoEnabled":true}`)
	req := httptest.NewRequest("POST", "/catalyst/v1/apps/uid-204/endpoints", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// claims.Org empty (multi-Org user) but groups carries /acme/admins.
	req = withTestClaims(req, &testClaimsSpec{
		Tier:   "admin",
		Groups: []string{"/acme/admins", "/beta/developers"},
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 (groups membership), got %d (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestPatchAppEndpoint_DenyCrossOrg(t *testing.T) {
	app := seedApp("uid-205", "wp", "acme", "wordpress")
	h, _, _ := newTestHandlerWithEndpoint(t, app)
	h.SetCatalogClient(newFakeCatalog())
	r := newTestRouter(h)

	body := []byte(`{"hostname":"wp.acme.example.com","port":443,"protocol":"https","tls":true,"visibility":"public","ssoEnabled":true}`)
	req := httptest.NewRequest("PATCH", "/catalyst/v1/apps/uid-205/endpoints/ui", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withTestClaims(req, &testClaimsSpec{Tier: "admin", Org: "beta"})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 on PATCH cross-Org, got %d", rec.Code)
	}
}

func TestDeleteAppEndpoint_DenyCrossOrg(t *testing.T) {
	app := seedApp("uid-206", "wp", "acme", "wordpress")
	h, _, _ := newTestHandlerWithEndpoint(t, app)
	h.SetCatalogClient(newFakeCatalog())
	r := newTestRouter(h)

	req := httptest.NewRequest("DELETE", "/catalyst/v1/apps/uid-206/endpoints/ui", nil)
	req = withTestClaims(req, &testClaimsSpec{Tier: "admin", Org: "beta"})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 on DELETE cross-Org, got %d", rec.Code)
	}
}

func TestCallerInOrg_RejectsEmptyOrg(t *testing.T) {
	// Defence-in-depth — empty Org must not match a buggy mapper's
	// empty claims.Org.
	h := &Handler{}
	got := h.callerInOrg(context.Background(), &auth.Claims{Org: ""}, "")
	if got {
		t.Fatal("expected false for empty Org / empty claims.Org")
	}
}

func TestCallerInOrg_AcceptsOrgMembershipCallback(t *testing.T) {
	h := &Handler{}
	h.SetEndpointPrecheckDeps(EndpointPrecheckDeps{
		OrgMembership: func(_ context.Context, c *auth.Claims, org string) bool {
			return org == "acme" && c.Email == "ali@acme.io"
		},
	})
	if !h.callerInOrg(context.Background(), &auth.Claims{Email: "ali@acme.io"}, "acme") {
		t.Fatal("expected callback to accept member")
	}
	if h.callerInOrg(context.Background(), &auth.Claims{Email: "rogue@evil.io"}, "acme") {
		t.Fatal("expected callback to reject non-member")
	}
}

// TestFindApplicationByUID_NameFallback — #3374. The AppsPage grid INSTANCE
// card keys its silent-SSO launch on the Application CR's NAME (the instance
// identity it projects), not the uid the AppDetail page carries. The
// launch-url resolver must match BOTH so the grid Open button resolves the
// real CR (correct org + blueprint) instead of falling through to the
// HR-backed branch with an empty org → wrong hostname → login form.
func TestFindApplicationByUID_NameFallback(t *testing.T) {
	app := seedApp("uid-qa-wp-123", "qa-wp", "qa-omantel", "wordpress")
	_, client := fakeEndpointDynamic(app)

	t.Run("resolves by uid (primary)", func(t *testing.T) {
		got, err := findApplicationByUID(context.Background(), client, "uid-qa-wp-123")
		if err != nil {
			t.Fatalf("uid lookup: unexpected error %v", err)
		}
		if got.GetName() != "qa-wp" {
			t.Fatalf("uid lookup: got name %q, want qa-wp", got.GetName())
		}
	})

	t.Run("resolves by name (fallback — the grid instance-card key)", func(t *testing.T) {
		got, err := findApplicationByUID(context.Background(), client, "qa-wp")
		if err != nil {
			t.Fatalf("name lookup: unexpected error %v — grid Open button would bypass SSO", err)
		}
		if string(got.GetUID()) != "uid-qa-wp-123" {
			t.Fatalf("name lookup: got uid %q, want uid-qa-wp-123", got.GetUID())
		}
		// The resolved CR must carry the org label so the launch-url builds
		// the correct org-scoped hostname (not the empty-org HR fallback).
		if org := extractOrgFromApp(got); org != "qa-omantel" {
			t.Fatalf("name lookup: org %q, want qa-omantel", org)
		}
	})

	t.Run("unknown key still errors", func(t *testing.T) {
		if _, err := findApplicationByUID(context.Background(), client, "does-not-exist"); err == nil {
			t.Fatal("expected errAppNotFound for an unknown uid/name")
		}
	})
}
