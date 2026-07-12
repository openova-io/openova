// blueprints_test.go — coverage for EPIC-2 Slice T+O+P (#1097)
// Blueprint publishing + Curate handlers.
package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/core/controllers/pkg/gitea"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// fakeGiteaClient is an in-memory stub of GiteaBlueprintClient.
type fakeGiteaClient struct {
	mu           sync.Mutex
	files        map[string][]byte // org/repo/branch/path → bytes
	repos        map[string]bool   // org/repo → exists
	orgs         map[string]bool   // org → exists
	dirs         map[string][]gitea.ContentEntry
	branches     map[string]bool              // org/repo/branch → exists
	prs          map[string]gitea.PullRequest // org/repo/head→base → PR
	prSeq        int64
	failPRCreate bool // when true, CreatePullRequest returns ErrRepoNotFound

	// #3668 — additive PutFile injection so the catalog-edit-git budget +
	// failure-surfacing tests can simulate a slow / erroring Gitea on
	// PutFile only (the source-of-truth write). Both default nil/0 →
	// unchanged behaviour for every existing caller.
	putFileSleep time.Duration // when >0, PutFile blocks this long (honours ctx cancel)
	putFileErr   error         // when non-nil, PutFile returns this error

	// putFileErrFor — #4896 additive TARGETED injection: when non-nil it is
	// consulted per PutFile target and a non-nil return fails ONLY that
	// write. Lets the dual-write tests fail the Flux aggregator leg while
	// the per-Blueprint source write succeeds. Default nil → unchanged
	// behaviour for every existing caller.
	putFileErrFor func(org, repo, branch, path string) error
}

func newFakeGitea() *fakeGiteaClient {
	return &fakeGiteaClient{
		files:    map[string][]byte{},
		repos:    map[string]bool{},
		orgs:     map[string]bool{},
		dirs:     map[string][]gitea.ContentEntry{},
		branches: map[string]bool{},
		prs:      map[string]gitea.PullRequest{},
	}
}

func giteaKey(org, repo, branch, path string) string {
	return org + "/" + repo + "/" + branch + "/" + path
}

func (f *fakeGiteaClient) EnsureOrg(_ context.Context, slug, _, _, _ string) (gitea.Org, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.orgs[slug] = true
	return gitea.Org{Username: slug}, nil
}

func (f *fakeGiteaClient) EnsureRepo(_ context.Context, org, name, _ string, _ bool) (gitea.Repo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repos[org+"/"+name] = true
	return gitea.Repo{Name: name}, nil
}

func (f *fakeGiteaClient) PutFile(ctx context.Context, org, repo, branch, path string, data []byte, _ string, _ ...gitea.PutFileOpts) (gitea.File, bool, error) {
	// #3668 — simulate a slow Gitea on PutFile (the source write) while
	// still honouring context cancellation, so the budget test can prove
	// the commit deadlines under a too-small budget and succeeds under the
	// dedicated catalogEditGitBudget.
	f.mu.Lock()
	sleep := f.putFileSleep
	putErr := f.putFileErr
	errFor := f.putFileErrFor
	f.mu.Unlock()
	if sleep > 0 {
		select {
		case <-time.After(sleep):
		case <-ctx.Done():
			return gitea.File{}, false, ctx.Err()
		}
	}
	if putErr != nil {
		return gitea.File{}, false, putErr
	}
	if errFor != nil {
		if e := errFor(org, repo, branch, path); e != nil {
			return gitea.File{}, false, e
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[giteaKey(org, repo, branch, path)] = data
	return gitea.File{Path: path, ContentBase64: base64.StdEncoding.EncodeToString(data)}, true, nil
}

func (f *fakeGiteaClient) GetFile(_ context.Context, org, repo, branch, path string) (gitea.File, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.files[giteaKey(org, repo, branch, path)]
	if !ok {
		return gitea.File{}, gitea.ErrFileNotFound
	}
	return gitea.File{
		Path:          path,
		ContentBase64: base64.StdEncoding.EncodeToString(b),
		Type:          "file",
	}, nil
}

func (f *fakeGiteaClient) ListOrgRepos(_ context.Context, org string) ([]gitea.Repo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []gitea.Repo{}
	for k := range f.repos {
		if strings.HasPrefix(k, org+"/") {
			out = append(out, gitea.Repo{Name: strings.TrimPrefix(k, org+"/")})
		}
	}
	return out, nil
}

func (f *fakeGiteaClient) ListContents(_ context.Context, org, repo, _, path string) ([]gitea.ContentEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.dirs[org+"/"+repo+"/"+path]; ok {
		return v, nil
	}
	return nil, gitea.ErrFileNotFound
}

func (f *fakeGiteaClient) EnsureBranch(_ context.Context, org, repo, branch string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.branches[org+"/"+repo+"/"+branch] = true
	return nil
}

func (f *fakeGiteaClient) CreatePullRequest(_ context.Context, org, repo, head, base, title, body string) (gitea.PullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failPRCreate {
		return gitea.PullRequest{}, gitea.ErrRepoNotFound
	}
	key := org + "/" + repo + "/" + head + "→" + base
	if existing, ok := f.prs[key]; ok {
		return existing, nil
	}
	f.prSeq++
	pr := gitea.PullRequest{
		ID:     f.prSeq,
		Number: f.prSeq,
		State:  "open",
		Title:  title,
		Body:   body,
		URL:    "http://gitea.local/" + org + "/" + repo + "/pulls/" + fmt.Sprintf("%d", f.prSeq),
	}
	pr.Head.Ref = head
	pr.Base.Ref = base
	f.prs[key] = pr
	return pr, nil
}

// validBlueprintYAML returns a canonical bp-acme blueprint.yaml.
func validBlueprintYAML(name, version string) string {
	return `apiVersion: catalyst.openova.io/v1
kind: Blueprint
metadata:
  name: ` + name + `
spec:
  version: ` + version + `
  card:
    title: Test Blueprint
  manifests:
    chart: ` + strings.TrimPrefix(name, "bp-") + `
    source:
      kind: HelmRepository
      ref: bitnami
  configSchema:
    type: object
`
}

// ── Publish 200 happy path ───────────────────────────────────────────

func TestHandleBlueprintPublish_WritesToGitea(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	gc := newFakeGitea()
	h.SetGiteaClient(gc)
	dep := installUserAccessDeployment(t, h, "dep-bp-publish")

	body := blueprintPublishRequest{
		Org:           "acme",
		Name:          "bp-acme-internal",
		Version:       "1.0.0",
		BlueprintYAML: validBlueprintYAML("bp-acme-internal", "1.0.0"),
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/blueprints/publish", body, registerBlueprintRoutes)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	wrote, ok := gc.files[giteaKey("acme", sharedBlueprintsRepo, blueprintBranch, "bp-acme-internal/blueprint.yaml")]
	if !ok {
		t.Fatalf("expected blueprint.yaml to be written; gitea state=%v", gc.files)
	}
	if !strings.Contains(string(wrote), "bp-acme-internal") {
		t.Fatalf("written content missing name; got %s", string(wrote))
	}
}

// ── Publish 400 on bad YAML ──────────────────────────────────────────

func TestHandleBlueprintPublish_RejectsBadYAML(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	gc := newFakeGitea()
	h.SetGiteaClient(gc)
	dep := installUserAccessDeployment(t, h, "dep-bp-bad-yaml")

	// kind != Blueprint
	bad := `apiVersion: v1
kind: ConfigMap
metadata:
  name: bp-bad
spec:
  version: 1.0.0
`
	body := blueprintPublishRequest{
		Org:           "acme",
		Name:          "bp-bad",
		Version:       "1.0.0",
		BlueprintYAML: bad,
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/blueprints/publish", body, registerBlueprintRoutes)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// ── Publish 400 on shape mismatch ────────────────────────────────────

func TestHandleBlueprintPublish_RejectsBadShape(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	gc := newFakeGitea()
	h.SetGiteaClient(gc)
	dep := installUserAccessDeployment(t, h, "dep-bp-bad-shape")

	body := blueprintPublishRequest{
		Org:           "acme",
		Name:          "no-prefix", // missing bp- prefix
		Version:       "1.0.0",
		BlueprintYAML: validBlueprintYAML("no-prefix", "1.0.0"),
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/blueprints/publish", body, registerBlueprintRoutes)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// ── Publish 503 when gitea unwired ───────────────────────────────────

func TestHandleBlueprintPublish_503WhenGiteaUnwired(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	dep := installUserAccessDeployment(t, h, "dep-bp-unwired")

	body := blueprintPublishRequest{
		Org:           "acme",
		Name:          "bp-x",
		Version:       "1.0.0",
		BlueprintYAML: validBlueprintYAML("bp-x", "1.0.0"),
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/blueprints/publish", body, registerBlueprintRoutes)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d want 503; body=%s", rec.Code, rec.Body.String())
	}
}

// ── Publish 403 unauthorized ─────────────────────────────────────────

func TestHandleBlueprintPublish_ForbiddenWhenNotTierAdmin(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.SetGiteaClient(newFakeGitea())
	dep := installUserAccessDeployment(t, h, "dep-bp-403")

	body := blueprintPublishRequest{
		Org:           "acme",
		Name:          "bp-x",
		Version:       "1.0.0",
		BlueprintYAML: validBlueprintYAML("bp-x", "1.0.0"),
	}
	r := chi.NewRouter()
	registerBlueprintRoutes(r, h)
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/blueprints/publish", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, &auth.Claims{Tier: "viewer"}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: got %d want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// ── Curate 200 happy path ────────────────────────────────────────────

func TestHandleBlueprintCurate_PromotesToCatalogSovereign(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	gc := newFakeGitea()
	h.SetGiteaClient(gc)
	dep := installUserAccessDeployment(t, h, "dep-bp-curate")

	// Pre-seed source.
	sourceBytes := []byte(validBlueprintYAML("bp-acme-internal", "1.0.0"))
	gc.files[giteaKey("acme", sharedBlueprintsRepo, blueprintBranch, "bp-acme-internal/blueprint.yaml")] = sourceBytes

	body := blueprintCurateRequest{
		SourceOrg:     "acme",
		BlueprintName: "bp-acme-internal",
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/blueprints/curate", body, registerBlueprintRoutes)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	// Verify it landed in catalog-sovereign org.
	wrote, ok := gc.files[giteaKey(catalogSovereignOrg, "bp-acme-internal", blueprintBranch, "blueprint.yaml")]
	if !ok {
		t.Fatalf("expected curated copy; gitea=%v", gc.files)
	}
	if string(wrote) != string(sourceBytes) {
		t.Fatalf("curated content mismatch; got %s", string(wrote))
	}
}

// ── Curate 404 on missing source ─────────────────────────────────────

func TestHandleBlueprintCurate_404OnMissingSource(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.SetGiteaClient(newFakeGitea())
	dep := installUserAccessDeployment(t, h, "dep-bp-curate-404")

	body := blueprintCurateRequest{
		SourceOrg:     "ghost",
		BlueprintName: "bp-nonexistent",
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/blueprints/curate", body, registerBlueprintRoutes)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// ── Curate 403 when not sovereign-admin ──────────────────────────────

func TestHandleBlueprintCurate_403WhenNotSovereignAdmin(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.SetGiteaClient(newFakeGitea())
	dep := installUserAccessDeployment(t, h, "dep-bp-curate-403")

	body := blueprintCurateRequest{
		SourceOrg:     "acme",
		BlueprintName: "bp-x",
	}
	r := chi.NewRouter()
	registerBlueprintRoutes(r, h)
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/blueprints/curate", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, &auth.Claims{Tier: "developer"}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: got %d want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// ── List curatable ───────────────────────────────────────────────────

func TestHandleBlueprintListCuratable_EnumeratesOrgRepos(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	gc := newFakeGitea()
	h.SetGiteaClient(gc)
	dep := installUserAccessDeployment(t, h, "dep-bp-list-curatable")

	// Seed acme/shared-blueprints with two bp-* entries.
	gc.dirs["acme/"+sharedBlueprintsRepo+"/"] = []gitea.ContentEntry{
		{Name: "bp-foo", Type: "dir"},
		{Name: "bp-bar", Type: "dir"},
		{Name: "README.md", Type: "file"},
	}
	gc.files[giteaKey("acme", sharedBlueprintsRepo, blueprintBranch, "bp-foo/blueprint.yaml")] = []byte(validBlueprintYAML("bp-foo", "1.0.0"))
	gc.files[giteaKey("acme", sharedBlueprintsRepo, blueprintBranch, "bp-bar/blueprint.yaml")] = []byte(validBlueprintYAML("bp-bar", "2.0.0"))

	rec := callUserAccess(t, h, http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/blueprints/curatable?orgs=acme,ghost",
		nil, registerBlueprintRoutes)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp curatableBlueprintsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("items: got %d want 2; %v", len(resp.Items), resp.Items)
	}
}

// TestHandleBlueprintListCuratable_GiteaUnwiredReturnsEmptyList — QA-loop
// iter-2 Fix #17. When the Gitea client is unwired (chroot Sovereign mode
// pre-cutover, test environments, or any deploy that hasn't booted the
// embedded Gitea yet), the handler must return a well-formed empty list
// envelope rather than 503. The 503 broke the /blueprints page which
// surfaced "Failed to fetch" to the operator. The empty list lets the
// UI render its "No blueprints yet" empty state.
func TestHandleBlueprintListCuratable_GiteaUnwiredReturnsEmptyList(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	// Deliberately do NOT call h.SetGiteaClient() — h.giteaClient stays nil.
	dep := installUserAccessDeployment(t, h, "dep-bp-no-gitea")

	rec := callUserAccess(t, h, http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/blueprints/curatable?orgs=acme",
		nil, registerBlueprintRoutes)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200 (gitea-unwired graceful path); body=%s", rec.Code, rec.Body.String())
	}
	var resp curatableBlueprintsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Items == nil {
		t.Fatalf("Items must be a non-nil empty slice (so JSON encodes as [])")
	}
	if len(resp.Items) != 0 {
		t.Fatalf("items: got %d want 0", len(resp.Items))
	}
}

// ── Edit-PR (slice Z3) ────────────────────────────────────────────────

func TestHandleBlueprintEditPR_OpensPR(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	gc := newFakeGitea()
	h.SetGiteaClient(gc)
	dep := installUserAccessDeployment(t, h, "dep-bp-edit-pr")

	body := blueprintEditPRRequest{
		Org:     "acme",
		Path:    "clusters/sov/website/deployment.yaml",
		Content: "kind: Deployment\nmetadata:\n  name: web\n",
		Title:   "Bump replicas",
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/blueprints/edit-pr", body, registerBlueprintRoutes)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp blueprintEditPRResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.PRURL == "" {
		t.Errorf("PRURL empty; resp=%+v", resp)
	}
	if resp.PRNumber == 0 {
		t.Errorf("PRNumber zero; resp=%+v", resp)
	}
	if !strings.HasPrefix(resp.Branch, "edit/") {
		t.Errorf("branch should be deterministic edit/<hash>; got %q", resp.Branch)
	}
	// Verify content actually landed on the branch.
	wrote, ok := gc.files[giteaKey("acme", sharedBlueprintsRepo, resp.Branch, body.Path)]
	if !ok {
		t.Fatalf("expected commit on branch %s; gitea files=%v", resp.Branch, gc.files)
	}
	if string(wrote) != body.Content {
		t.Fatalf("committed content mismatch: got %q want %q", string(wrote), body.Content)
	}
}

func TestHandleBlueprintEditPR_DeterministicBranchPerContent(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	gc := newFakeGitea()
	h.SetGiteaClient(gc)
	dep := installUserAccessDeployment(t, h, "dep-bp-edit-pr-det")

	body := blueprintEditPRRequest{
		Org:     "acme",
		Path:    "clusters/sov/website/deployment.yaml",
		Content: "same-content",
		Title:   "edit",
	}
	r1 := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/blueprints/edit-pr", body, registerBlueprintRoutes)
	r2 := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/blueprints/edit-pr", body, registerBlueprintRoutes)
	if r1.Code != http.StatusOK || r2.Code != http.StatusOK {
		t.Fatalf("statuses: %d, %d (want both 200)", r1.Code, r2.Code)
	}
	var resp1, resp2 blueprintEditPRResponse
	_ = json.Unmarshal(r1.Body.Bytes(), &resp1)
	_ = json.Unmarshal(r2.Body.Bytes(), &resp2)
	if resp1.Branch != resp2.Branch {
		t.Errorf("same content should produce same branch; got %q vs %q", resp1.Branch, resp2.Branch)
	}
	// Different content → different branch.
	body.Content = "other-content"
	r3 := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/blueprints/edit-pr", body, registerBlueprintRoutes)
	var resp3 blueprintEditPRResponse
	_ = json.Unmarshal(r3.Body.Bytes(), &resp3)
	if resp1.Branch == resp3.Branch {
		t.Errorf("different content should produce different branch; got %q == %q", resp1.Branch, resp3.Branch)
	}
}

func TestHandleBlueprintEditPR_403WhenNotTierAdmin(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.SetGiteaClient(newFakeGitea())
	dep := installUserAccessDeployment(t, h, "dep-bp-edit-pr-403")

	body := blueprintEditPRRequest{Org: "acme", Path: "x.yaml", Content: "k: v", Title: "x"}
	r := chi.NewRouter()
	registerBlueprintRoutes(r, h)
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/blueprints/edit-pr", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, &auth.Claims{Tier: "viewer"}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: got %d want 403; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleBlueprintEditPR_503WhenGiteaUnwired(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	dep := installUserAccessDeployment(t, h, "dep-bp-edit-pr-503")
	body := blueprintEditPRRequest{Org: "acme", Path: "x.yaml", Content: "k: v", Title: "x"}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/blueprints/edit-pr", body, registerBlueprintRoutes)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d want 503; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleBlueprintEditPR_404WhenRepoMissing(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	gc := newFakeGitea()
	gc.failPRCreate = true // the fake's CreatePullRequest returns ErrRepoNotFound
	h.SetGiteaClient(gc)
	dep := installUserAccessDeployment(t, h, "dep-bp-edit-pr-404")
	body := blueprintEditPRRequest{Org: "ghost", Path: "x.yaml", Content: "k: v", Title: "x"}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/blueprints/edit-pr", body, registerBlueprintRoutes)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleBlueprintEditPR_BadRequest(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.SetGiteaClient(newFakeGitea())
	dep := installUserAccessDeployment(t, h, "dep-bp-edit-pr-400")
	cases := []struct {
		name string
		body blueprintEditPRRequest
	}{
		{"empty org", blueprintEditPRRequest{Path: "x", Content: "y", Title: "z"}},
		{"path traversal", blueprintEditPRRequest{Org: "acme", Path: "../etc/passwd", Content: "y", Title: "z"}},
		{"absolute path", blueprintEditPRRequest{Org: "acme", Path: "/x.yaml", Content: "y", Title: "z"}},
		{"empty content", blueprintEditPRRequest{Org: "acme", Path: "x", Content: "", Title: "z"}},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			rec := callUserAccess(t, h, http.MethodPost,
				"/api/v1/sovereigns/"+dep.ID+"/blueprints/edit-pr", c.body, registerBlueprintRoutes)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status: got %d want 400; body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestEditPRBranchName_DeterministicAndPathSensitive(t *testing.T) {
	t.Parallel()
	a := editPRBranchName("a.yaml", []byte("body"))
	b := editPRBranchName("a.yaml", []byte("body"))
	c := editPRBranchName("b.yaml", []byte("body"))
	d := editPRBranchName("a.yaml", []byte("different-body"))
	if a != b {
		t.Errorf("same (path, content) should yield same branch; got %q vs %q", a, b)
	}
	if a == c || a == d {
		t.Errorf("path / content change should yield different branch; got %q %q %q", a, c, d)
	}
	if !strings.HasPrefix(a, "edit/") {
		t.Errorf("branch should be edit/<hash>; got %q", a)
	}
}

// ── Validators ───────────────────────────────────────────────────────

func TestLooksLikeSemver(t *testing.T) {
	cases := []struct {
		in string
		ok bool
	}{
		{"1.0.0", true},
		{"1.2.3", true},
		{"1.2.3-rc1", true},
		{"1.2.3+build", true},
		{"v1.0.0", false},
		{"latest", false},
		{"1.0", false},
		{"", false},
	}
	for _, tc := range cases {
		got := looksLikeSemver(tc.in)
		if got != tc.ok {
			t.Errorf("looksLikeSemver(%q) = %v, want %v", tc.in, got, tc.ok)
		}
	}
}

func TestParseBlueprintMeta_Roundtrip(t *testing.T) {
	yaml := validBlueprintYAML("bp-x", "5.0.0")
	v, title := parseBlueprintMeta([]byte(yaml))
	if v != "5.0.0" {
		t.Fatalf("version: got %q want 5.0.0", v)
	}
	if title != "Test Blueprint" {
		t.Fatalf("title: got %q", title)
	}
}
