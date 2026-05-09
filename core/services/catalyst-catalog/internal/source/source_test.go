package source

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/core/controllers/pkg/gitea"
	"github.com/openova-io/openova/core/services/catalyst-catalog/internal/cache"
)

// fakeGitea is a focused httptest-backed Gitea fixture for the source
// + resolver tests. It supports:
//
//   GET /orgs/{org}/repos       → list repos
//   GET /repos/{owner}/{repo}/contents/{path}?ref=main → file or dir
//
// Repos register via AddRepo(); files via AddFile(); directories are
// implied by file paths.
type fakeGitea struct {
	mu    sync.Mutex
	repos map[string][]string                // org → repo names
	files map[string]map[string]map[string][]byte // org → repo → path → bytes
}

func newFakeGitea() *fakeGitea {
	return &fakeGitea{
		repos: map[string][]string{},
		files: map[string]map[string]map[string][]byte{},
	}
}

func (f *fakeGitea) AddRepo(org, repo string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repos[org] = append(f.repos[org], repo)
	if f.files[org] == nil {
		f.files[org] = map[string]map[string][]byte{}
	}
	if f.files[org][repo] == nil {
		f.files[org][repo] = map[string][]byte{}
	}
}

func (f *fakeGitea) AddFile(org, repo, path string, content []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.files[org] == nil {
		f.files[org] = map[string]map[string][]byte{}
	}
	if f.files[org][repo] == nil {
		f.files[org][repo] = map[string][]byte{}
		f.repos[org] = append(f.repos[org], repo)
	}
	f.files[org][repo][path] = content
}

func (f *fakeGitea) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			http.Error(w, "no auth", http.StatusUnauthorized)
			return
		}
		p := r.URL.Path
		// GET /api/v1/orgs/{org}/repos
		if r.Method == http.MethodGet && strings.HasPrefix(p, "/api/v1/orgs/") && strings.HasSuffix(p, "/repos") {
			org := strings.TrimSuffix(strings.TrimPrefix(p, "/api/v1/orgs/"), "/repos")
			f.mu.Lock()
			repos, ok := f.repos[org]
			f.mu.Unlock()
			if !ok {
				http.Error(w, "no such org", http.StatusNotFound)
				return
			}
			out := make([]gitea.Repo, 0, len(repos))
			for _, name := range repos {
				out = append(out, gitea.Repo{Name: name, FullName: org + "/" + name})
			}
			writeJSON(w, http.StatusOK, out)
			return
		}
		// /api/v1/repos/{owner}/{repo}/contents/{path}
		if r.Method == http.MethodGet && strings.HasPrefix(p, "/api/v1/repos/") && strings.Contains(p, "/contents/") {
			rest := strings.TrimPrefix(p, "/api/v1/repos/")
			idx := strings.Index(rest, "/contents/")
			ownerRepo := rest[:idx]
			filePath := strings.TrimSuffix(rest[idx+len("/contents/"):], "/")
			parts := strings.SplitN(ownerRepo, "/", 2)
			if len(parts) != 2 {
				http.Error(w, "bad", http.StatusBadRequest)
				return
			}
			org, repo := parts[0], parts[1]
			f.mu.Lock()
			repoFiles, repoOK := f.files[org][repo]
			f.mu.Unlock()
			if !repoOK {
				http.Error(w, `{"message":"repository not found"}`, http.StatusNotFound)
				return
			}
			// File hit?
			if content, ok := repoFiles[filePath]; ok {
				writeJSON(w, http.StatusOK, gitea.File{
					Path:          filePath,
					SHA:           "sha-" + filePath,
					Type:          "file",
					ContentBase64: base64.StdEncoding.EncodeToString(content),
				})
				return
			}
			// Directory listing?
			prefix := filePath
			if prefix != "" {
				prefix += "/"
			}
			entries := []gitea.ContentEntry{}
			seen := map[string]bool{}
			f.mu.Lock()
			for path, body := range repoFiles {
				if !strings.HasPrefix(path, prefix) {
					continue
				}
				rest := strings.TrimPrefix(path, prefix)
				if rest == "" {
					continue
				}
				head, _, hasSlash := strings.Cut(rest, "/")
				if seen[head] {
					continue
				}
				seen[head] = true
				typ := "file"
				size := int64(len(body))
				if hasSlash {
					typ = "dir"
					size = 0
				}
				entries = append(entries, gitea.ContentEntry{
					Name: head,
					Path: prefix + head,
					Type: typ,
					Size: size,
				})
			}
			f.mu.Unlock()
			if len(entries) == 0 && filePath != "" {
				http.Error(w, "no such path", http.StatusNotFound)
				return
			}
			writeJSON(w, http.StatusOK, entries)
			return
		}
		http.Error(w, "unhandled", http.StatusNotFound)
	})
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func newGiteaClient(t *testing.T, fake *fakeGitea) *gitea.Client {
	t.Helper()
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)
	c := gitea.New(srv.URL, "test-token")
	c.HTTP = srv.Client()
	return c
}

// blueprintYAML is a helper that emits a minimal but realistic
// blueprint.yaml fixture.
func blueprintYAML(name, version, title string) []byte {
	return []byte(`apiVersion: catalyst.openova.io/v1
kind: Blueprint
metadata:
  name: ` + name + `
spec:
  version: ` + version + `
  visibility: listed
  card:
    title: ` + title + `
    summary: A blueprint for ` + name + `
  upgrades:
    from:
      - 0.0.1
      - 0.1.0
`)
}

func TestParseBlueprint_HappyPath(t *testing.T) {
	bp, err := ParseBlueprint(blueprintYAML("bp-foo", "1.2.3", "Foo App"), OriginPublic, "", "bp-foo")
	if err != nil {
		t.Fatalf("ParseBlueprint: %v", err)
	}
	if bp.Name != "bp-foo" {
		t.Errorf("Name = %q", bp.Name)
	}
	if bp.Version != "1.2.3" {
		t.Errorf("Version = %q", bp.Version)
	}
	if bp.Card.Title != "Foo App" {
		t.Errorf("Card.Title = %q", bp.Card.Title)
	}
	if bp.Card.Summary == "" {
		t.Error("Card.Summary should be populated")
	}
	if len(bp.UpgradeFrom) != 2 {
		t.Errorf("UpgradeFrom = %v", bp.UpgradeFrom)
	}
	if bp.Source != "public" {
		t.Errorf("Source = %q, want public", bp.Source)
	}
}

func TestParseBlueprint_RejectsNoSpec(t *testing.T) {
	_, err := ParseBlueprint([]byte(`apiVersion: x
kind: Y
metadata:
  name: foo
`), OriginPublic, "", "foo")
	if err == nil {
		t.Fatal("ParseBlueprint without spec should fail")
	}
}

func TestPublic_ListAndGet(t *testing.T) {
	t.Parallel()
	fake := newFakeGitea()
	fake.AddFile("catalog", "bp-wordpress", "blueprint.yaml", blueprintYAML("bp-wordpress", "1.0.0", "WordPress"))
	fake.AddFile("catalog", "bp-redis", "blueprint.yaml", blueprintYAML("bp-redis", "0.5.0", "Redis"))
	gc := newGiteaClient(t, fake)
	c := cache.New(64, 30*time.Second)
	src := NewPublic(gc, "catalog", c)

	list, err := src.List(context.Background())
	if err != nil {
		t.Fatalf("Public.List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len = %d, want 2", len(list))
	}
	for _, bp := range list {
		if bp.Origin != OriginPublic {
			t.Errorf("Origin = %v, want public", bp.Origin)
		}
	}

	bp, err := src.Get(context.Background(), "bp-wordpress", "")
	if err != nil || bp == nil {
		t.Fatalf("Get(bp-wordpress) = (%v, %v)", bp, err)
	}
	if bp.Card.Title != "WordPress" {
		t.Errorf("Card.Title = %q", bp.Card.Title)
	}

	// Version mismatch returns nil (no error).
	bp, err = src.Get(context.Background(), "bp-wordpress", "9.9.9")
	if err != nil {
		t.Fatalf("Get version-pinned err: %v", err)
	}
	if bp != nil {
		t.Errorf("expected nil for unmatched version, got %+v", bp)
	}

	// Unknown name returns nil.
	bp, err = src.Get(context.Background(), "bp-missing", "")
	if err != nil {
		t.Fatalf("Get(bp-missing) err: %v", err)
	}
	if bp != nil {
		t.Errorf("expected nil for missing, got %+v", bp)
	}
}

func TestPublic_OrgNotProvisioned_ReturnsEmpty(t *testing.T) {
	fake := newFakeGitea()
	gc := newGiteaClient(t, fake)
	src := NewPublic(gc, "catalog", cache.New(8, 30*time.Second))

	list, err := src.List(context.Background())
	if err != nil {
		t.Fatalf("List on missing Org: %v", err)
	}
	if list != nil && len(list) != 0 {
		t.Errorf("expected empty list, got %v", list)
	}
}

func TestPublic_RepoWithoutBlueprintYAML_Skipped(t *testing.T) {
	fake := newFakeGitea()
	fake.AddRepo("catalog", "bp-empty")
	fake.AddFile("catalog", "bp-good", "blueprint.yaml", blueprintYAML("bp-good", "1.0.0", "Good"))
	gc := newGiteaClient(t, fake)
	src := NewPublic(gc, "catalog", cache.New(8, 30*time.Second))

	list, err := src.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Name != "bp-good" {
		t.Errorf("expected only bp-good, got %v", list)
	}
}

func TestSovereign_ReadsFromSovereignOrg(t *testing.T) {
	fake := newFakeGitea()
	fake.AddFile("catalog-sovereign", "bp-omantel-edge", "blueprint.yaml",
		blueprintYAML("bp-omantel-edge", "2.0.0", "Omantel Edge"))
	gc := newGiteaClient(t, fake)
	src := NewSovereign(gc, "catalog-sovereign", cache.New(8, 30*time.Second))

	list, err := src.List(context.Background())
	if err != nil {
		t.Fatalf("Sovereign.List: %v", err)
	}
	if len(list) != 1 || list[0].Origin != OriginSovereign {
		t.Errorf("expected 1 sovereign-origin entry, got %+v", list)
	}
}

func TestOrgPrivate_ListsPerOrg(t *testing.T) {
	fake := newFakeGitea()
	fake.AddFile("acme", "shared-blueprints", "bp-internal-app/blueprint.yaml",
		blueprintYAML("bp-internal-app", "0.1.0", "Internal App"))
	fake.AddFile("acme", "shared-blueprints", "bp-team-tool/blueprint.yaml",
		blueprintYAML("bp-team-tool", "0.2.0", "Team Tool"))
	gc := newGiteaClient(t, fake)
	src := NewOrgPrivate(gc, "acme", "shared-blueprints", cache.New(8, 30*time.Second))

	list, err := src.List(context.Background())
	if err != nil {
		t.Fatalf("OrgPrivate.List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len = %d, want 2", len(list))
	}
	// Every Blueprint must be tagged with the Org slug for downstream
	// access enforcement.
	for _, bp := range list {
		if bp.Org != "acme" {
			t.Errorf("Org = %q, want acme", bp.Org)
		}
		if bp.Origin != OriginOrgPrivate {
			t.Errorf("Origin = %v, want org-private", bp.Origin)
		}
	}

	// Targeted Get.
	bp, err := src.Get(context.Background(), "bp-internal-app", "0.1.0")
	if err != nil || bp == nil {
		t.Fatalf("Get(bp-internal-app, 0.1.0) = (%v, %v)", bp, err)
	}
}

func TestOrgPrivate_RepoNotProvisioned_ReturnsEmpty(t *testing.T) {
	fake := newFakeGitea()
	gc := newGiteaClient(t, fake)
	src := NewOrgPrivate(gc, "no-such-org", "shared-blueprints", cache.New(8, 30*time.Second))

	list, err := src.List(context.Background())
	if err != nil {
		t.Fatalf("OrgPrivate.List on missing repo: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected empty, got %v", list)
	}
}

func TestResolver_PriorityOnCollision(t *testing.T) {
	t.Parallel()
	// Three sources publish the SAME Blueprint name `bp-shared` with
	// different titles to make the priority winner identifiable.
	fake := newFakeGitea()
	fake.AddFile("catalog", "bp-shared", "blueprint.yaml",
		blueprintYAML("bp-shared", "1.0.0-public", "PUBLIC TITLE"))
	fake.AddFile("catalog-sovereign", "bp-shared", "blueprint.yaml",
		blueprintYAML("bp-shared", "1.0.0-sovereign", "SOVEREIGN TITLE"))
	fake.AddFile("acme", "shared-blueprints", "bp-shared/blueprint.yaml",
		blueprintYAML("bp-shared", "1.0.0-private", "PRIVATE TITLE"))
	gc := newGiteaClient(t, fake)
	r := NewResolver(gc, "catalog", "catalog-sovereign", "shared-blueprints", cache.New(64, 30*time.Second))

	// Caller in Org "acme" — should see PRIVATE winning.
	list, err := r.List(context.Background(), []string{"acme"})
	if err != nil {
		t.Fatalf("Resolver.List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 entry (deduped), got %d: %+v", len(list), list)
	}
	if list[0].Card.Title != "PRIVATE TITLE" {
		t.Errorf("Card.Title = %q, want PRIVATE TITLE (private should win)", list[0].Card.Title)
	}
	if list[0].Origin != OriginOrgPrivate {
		t.Errorf("Origin = %v, want org-private", list[0].Origin)
	}

	// Caller in Org "fabrikam" (no private copy) — should see SOVEREIGN winning.
	list, err = r.List(context.Background(), []string{"fabrikam"})
	if err != nil {
		t.Fatalf("Resolver.List(fabrikam): %v", err)
	}
	if list[0].Card.Title != "SOVEREIGN TITLE" {
		t.Errorf("fabrikam Card.Title = %q, want SOVEREIGN TITLE", list[0].Card.Title)
	}

	// Anonymous caller — should see SOVEREIGN winning over PUBLIC.
	list, err = r.List(context.Background(), nil)
	if err != nil {
		t.Fatalf("Resolver.List(anon): %v", err)
	}
	if list[0].Card.Title != "SOVEREIGN TITLE" {
		t.Errorf("anon Card.Title = %q, want SOVEREIGN TITLE", list[0].Card.Title)
	}
}

func TestResolver_PerOrgAccessFilter(t *testing.T) {
	t.Parallel()
	// Two Orgs publish DISTINCT private blueprints.
	fake := newFakeGitea()
	fake.AddFile("acme", "shared-blueprints", "bp-acme-secret/blueprint.yaml",
		blueprintYAML("bp-acme-secret", "1.0.0", "ACME Secret"))
	fake.AddFile("contoso", "shared-blueprints", "bp-contoso-secret/blueprint.yaml",
		blueprintYAML("bp-contoso-secret", "1.0.0", "Contoso Secret"))
	gc := newGiteaClient(t, fake)
	r := NewResolver(gc, "catalog", "catalog-sovereign", "shared-blueprints", cache.New(64, 30*time.Second))

	// User in Org "acme" sees ONLY their private blueprints.
	list, err := r.List(context.Background(), []string{"acme"})
	if err != nil {
		t.Fatalf("List(acme): %v", err)
	}
	names := blueprintNames(list)
	if !contains(names, "bp-acme-secret") {
		t.Errorf("acme should see bp-acme-secret, got %v", names)
	}
	if contains(names, "bp-contoso-secret") {
		t.Errorf("acme MUST NOT see bp-contoso-secret, got %v", names)
	}

	// Cross-check: Contoso user sees only their own.
	list, err = r.List(context.Background(), []string{"contoso"})
	if err != nil {
		t.Fatalf("List(contoso): %v", err)
	}
	names = blueprintNames(list)
	if contains(names, "bp-acme-secret") {
		t.Errorf("contoso MUST NOT see bp-acme-secret, got %v", names)
	}
}

func TestResolver_GetFollowsPriorityOrder(t *testing.T) {
	t.Parallel()
	fake := newFakeGitea()
	fake.AddFile("catalog", "bp-X", "blueprint.yaml", blueprintYAML("bp-X", "1.0.0", "PUBLIC"))
	fake.AddFile("catalog-sovereign", "bp-X", "blueprint.yaml", blueprintYAML("bp-X", "1.0.0", "SOVEREIGN"))
	fake.AddFile("acme", "shared-blueprints", "bp-X/blueprint.yaml", blueprintYAML("bp-X", "1.0.0", "PRIVATE"))
	gc := newGiteaClient(t, fake)
	r := NewResolver(gc, "catalog", "catalog-sovereign", "shared-blueprints", cache.New(64, 30*time.Second))

	bp, err := r.Get(context.Background(), "bp-X", "", []string{"acme"})
	if err != nil || bp == nil {
		t.Fatalf("Get: %v %v", bp, err)
	}
	if bp.Card.Title != "PRIVATE" {
		t.Errorf("Get returned %q, want PRIVATE", bp.Card.Title)
	}
}

func TestResolver_GetUnknownReturnsNotFound(t *testing.T) {
	fake := newFakeGitea()
	gc := newGiteaClient(t, fake)
	r := NewResolver(gc, "catalog", "catalog-sovereign", "shared-blueprints", cache.New(8, 30*time.Second))

	_, err := r.Get(context.Background(), "bp-nope", "", nil)
	if !errors.Is(err, ErrBlueprintNotFound) {
		t.Errorf("Get unknown err = %v, want ErrBlueprintNotFound", err)
	}
}

func TestPublic_CacheReducesGiteaCalls(t *testing.T) {
	// Wrap a fake server to count how many GET requests reach it for
	// the file path. After the first read, cached subsequent reads
	// must NOT hit the server.
	hitCount := 0
	var mu sync.Mutex
	fake := newFakeGitea()
	fake.AddFile("catalog", "bp-foo", "blueprint.yaml", blueprintYAML("bp-foo", "1.0.0", "Foo"))
	upstream := fake.handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/blueprint.yaml") {
			mu.Lock()
			hitCount++
			mu.Unlock()
		}
		upstream.ServeHTTP(w, r)
	}))
	defer srv.Close()
	gc := gitea.New(srv.URL, "test-token")
	gc.HTTP = srv.Client()

	cs := cache.New(8, 30*time.Second)
	src := NewPublic(gc, "catalog", cs)
	for i := 0; i < 5; i++ {
		bp, err := src.Get(context.Background(), "bp-foo", "")
		if err != nil || bp == nil {
			t.Fatalf("iter %d: %v", i, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if hitCount != 1 {
		t.Errorf("expected 1 server hit after caching, got %d", hitCount)
	}
}

func blueprintNames(list []Blueprint) []string {
	out := make([]string, 0, len(list))
	for _, bp := range list {
		out = append(out, bp.Name)
	}
	return out
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
