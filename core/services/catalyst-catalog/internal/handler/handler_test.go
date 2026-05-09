package handler

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/core/controllers/pkg/gitea"
	"github.com/openova-io/openova/core/services/catalyst-catalog/internal/cache"
	"github.com/openova-io/openova/core/services/catalyst-catalog/internal/config"
	"github.com/openova-io/openova/core/services/catalyst-catalog/internal/source"
)

// fakeGitea is a minimal stand-in matching the source/source_test.go
// fixture; duplicated here to avoid an internal-test cross-package
// import.
type fakeGitea struct {
	mu    sync.Mutex
	repos map[string][]string
	files map[string]map[string]map[string][]byte
}

func newFakeGitea() *fakeGitea {
	return &fakeGitea{
		repos: map[string][]string{},
		files: map[string]map[string]map[string][]byte{},
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
			writeJSONResp(w, http.StatusOK, out)
			return
		}
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
			if content, ok := repoFiles[filePath]; ok {
				writeJSONResp(w, http.StatusOK, gitea.File{
					Path:          filePath,
					SHA:           "sha-" + filePath,
					Type:          "file",
					ContentBase64: base64.StdEncoding.EncodeToString(content),
				})
				return
			}
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
				entries = append(entries, gitea.ContentEntry{Name: head, Path: prefix + head, Type: typ, Size: size})
			}
			f.mu.Unlock()
			if len(entries) == 0 && filePath != "" {
				http.Error(w, "no such path", http.StatusNotFound)
				return
			}
			writeJSONResp(w, http.StatusOK, entries)
			return
		}
		http.Error(w, "unhandled", http.StatusNotFound)
	})
}

func writeJSONResp(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func newTestHandler(t *testing.T, fake *fakeGitea, anonymous bool) (*Handler, func()) {
	srv := httptest.NewServer(fake.handler())
	gc := gitea.New(srv.URL, "test-token")
	gc.HTTP = srv.Client()

	cfg := config.Config{
		ListenAddr:           ":0",
		GiteaURL:             srv.URL,
		GiteaToken:           "test-token",
		PublicCatalogOrg:     "catalog",
		SovereignCatalogOrg:  "catalog-sovereign",
		OrgPrivateRepoSuffix: "shared-blueprints",
		CacheTTL:             30 * time.Second,
		CacheCapacity:        64,
		SessionCookieName:    "catalyst_session",
		AnonymousReads:       anonymous,
	}
	c := cache.New(64, 30*time.Second)
	r := source.NewResolver(gc, cfg.PublicCatalogOrg, cfg.SovereignCatalogOrg, cfg.OrgPrivateRepoSuffix, c)
	h := New(cfg, r, c, slog.Default())
	return h, srv.Close
}

// makeJWT builds an unsigned JWT (signature is gibberish — catalog-svc
// does not verify in-process; see auth/claims.go package doc).
func makeJWT(t *testing.T, claims map[string]interface{}) string {
	t.Helper()
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	hb, _ := json.Marshal(header)
	if _, ok := claims["exp"]; !ok {
		claims["exp"] = time.Now().Add(time.Hour).Unix()
	}
	pb, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	return fmt.Sprintf("%s.%s.%s", enc(hb), enc(pb), enc([]byte("sig")))
}

func blueprintYAML(name, version, title string, upgradeFrom ...string) []byte {
	uf := ""
	if len(upgradeFrom) > 0 {
		var b strings.Builder
		b.WriteString("\n  upgrades:\n    from:\n")
		for _, v := range upgradeFrom {
			b.WriteString("      - " + v + "\n")
		}
		uf = b.String()
	}
	return []byte(fmt.Sprintf(`apiVersion: catalyst.openova.io/v1
kind: Blueprint
metadata:
  name: %s
spec:
  version: %s
  visibility: listed
  card:
    title: %s
    summary: A blueprint for %s%s
`, name, version, title, name, uf))
}

func doRequest(t *testing.T, h *Handler, method, target, bearer string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	body, _ := io.ReadAll(rec.Body)
	return rec.Code, body
}

func TestHealthz(t *testing.T) {
	fake := newFakeGitea()
	h, cleanup := newTestHandler(t, fake, true)
	defer cleanup()
	code, body := doRequest(t, h, http.MethodGet, "/healthz", "")
	if code != 200 {
		t.Errorf("status = %d, body = %s", code, body)
	}
}

func TestList_RequiresAuthByDefault(t *testing.T) {
	fake := newFakeGitea()
	h, cleanup := newTestHandler(t, fake, false)
	defer cleanup()
	code, _ := doRequest(t, h, http.MethodGet, "/api/v1/catalog", "")
	if code != http.StatusUnauthorized {
		t.Errorf("anon list status = %d, want 401", code)
	}
}

func TestList_AnonymousReadsWhenAllowed(t *testing.T) {
	fake := newFakeGitea()
	fake.AddFile("catalog", "bp-foo", "blueprint.yaml", blueprintYAML("bp-foo", "1.0.0", "Foo"))
	h, cleanup := newTestHandler(t, fake, true)
	defer cleanup()
	code, body := doRequest(t, h, http.MethodGet, "/api/v1/catalog", "")
	if code != 200 {
		t.Fatalf("status = %d, body = %s", code, body)
	}
	var resp listResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 1 {
		t.Errorf("len = %d, want 1", len(resp.Items))
	}
}

func TestList_PerOrgPrivateRequiresMembership(t *testing.T) {
	fake := newFakeGitea()
	fake.AddFile("acme", "shared-blueprints", "bp-acme/blueprint.yaml",
		blueprintYAML("bp-acme", "1.0.0", "ACME Internal"))
	fake.AddFile("contoso", "shared-blueprints", "bp-contoso/blueprint.yaml",
		blueprintYAML("bp-contoso", "1.0.0", "Contoso Internal"))
	h, cleanup := newTestHandler(t, fake, false)
	defer cleanup()

	// User in Org acme — sees only bp-acme.
	tok := makeJWT(t, map[string]interface{}{
		"sub":    "alice",
		"groups": []string{"/acme/admins"},
	})
	code, body := doRequest(t, h, http.MethodGet, "/api/v1/catalog", tok)
	if code != 200 {
		t.Fatalf("status = %d, body = %s", code, body)
	}
	var resp listResponse
	json.Unmarshal(body, &resp)
	names := []string{}
	for _, bp := range resp.Items {
		names = append(names, bp.Name)
	}
	hasAcme := false
	for _, n := range names {
		if n == "bp-acme" {
			hasAcme = true
		}
		if n == "bp-contoso" {
			t.Errorf("acme user MUST NOT see bp-contoso, got %v", names)
		}
	}
	if !hasAcme {
		t.Errorf("acme user should see bp-acme, got %v", names)
	}
}

func TestList_OrgFilterDeniesNonMembers(t *testing.T) {
	fake := newFakeGitea()
	fake.AddFile("acme", "shared-blueprints", "bp-acme/blueprint.yaml",
		blueprintYAML("bp-acme", "1.0.0", "ACME"))
	h, cleanup := newTestHandler(t, fake, false)
	defer cleanup()
	tok := makeJWT(t, map[string]interface{}{"sub": "bob", "groups": []string{"/contoso/admins"}})
	code, body := doRequest(t, h, http.MethodGet, "/api/v1/catalog?org=acme", tok)
	if code != http.StatusForbidden {
		t.Errorf("status = %d, body = %s — want 403", code, body)
	}
}

func TestGet_HappyPath(t *testing.T) {
	fake := newFakeGitea()
	fake.AddFile("catalog", "bp-foo", "blueprint.yaml", blueprintYAML("bp-foo", "1.0.0", "Foo"))
	h, cleanup := newTestHandler(t, fake, true)
	defer cleanup()
	code, body := doRequest(t, h, http.MethodGet, "/api/v1/catalog/bp-foo", "")
	if code != 200 {
		t.Fatalf("status = %d, body = %s", code, body)
	}
	var bp source.Blueprint
	if err := json.Unmarshal(body, &bp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if bp.Card.Title != "Foo" {
		t.Errorf("Card.Title = %q", bp.Card.Title)
	}
	if bp.Raw == nil {
		t.Error("Raw should be populated for /catalog/{name}")
	}
}

func TestGet_NotFound(t *testing.T) {
	fake := newFakeGitea()
	h, cleanup := newTestHandler(t, fake, true)
	defer cleanup()
	code, _ := doRequest(t, h, http.MethodGet, "/api/v1/catalog/bp-missing", "")
	if code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", code)
	}
}

func TestListVersions_AggregatesAcrossSources(t *testing.T) {
	fake := newFakeGitea()
	fake.AddFile("catalog", "bp-X", "blueprint.yaml", blueprintYAML("bp-X", "1.0.0", "PUBLIC", "0.9.0"))
	fake.AddFile("catalog-sovereign", "bp-X", "blueprint.yaml", blueprintYAML("bp-X", "1.1.0", "SOVEREIGN", "1.0.0"))
	fake.AddFile("acme", "shared-blueprints", "bp-X/blueprint.yaml", blueprintYAML("bp-X", "1.2.0-acme", "PRIVATE", "1.1.0"))
	h, cleanup := newTestHandler(t, fake, false)
	defer cleanup()

	tok := makeJWT(t, map[string]interface{}{"sub": "alice", "groups": []string{"/acme/admins"}})
	code, body := doRequest(t, h, http.MethodGet, "/api/v1/catalog/bp-X/versions", tok)
	if code != 200 {
		t.Fatalf("status = %d, body = %s", code, body)
	}
	var resp versionsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Versions) != 3 {
		t.Errorf("expected 3 versions, got %d: %+v", len(resp.Versions), resp.Versions)
	}
	if from, ok := resp.UpgradeMatrix["1.2.0-acme"]; !ok || len(from) != 1 || from[0] != "1.1.0" {
		t.Errorf("UpgradeMatrix[1.2.0-acme] = %v, want [1.1.0]", from)
	}
}

func TestGetVersion_FollowsPriority(t *testing.T) {
	fake := newFakeGitea()
	fake.AddFile("catalog", "bp-X", "blueprint.yaml", blueprintYAML("bp-X", "1.0.0", "PUBLIC"))
	fake.AddFile("acme", "shared-blueprints", "bp-X/blueprint.yaml", blueprintYAML("bp-X", "1.0.0", "PRIVATE"))
	h, cleanup := newTestHandler(t, fake, false)
	defer cleanup()

	tok := makeJWT(t, map[string]interface{}{"sub": "alice", "groups": []string{"/acme/admins"}})
	code, body := doRequest(t, h, http.MethodGet, "/api/v1/catalog/bp-X/versions/1.0.0", tok)
	if code != 200 {
		t.Fatalf("status = %d, body = %s", code, body)
	}
	var bp source.Blueprint
	json.Unmarshal(body, &bp)
	if bp.Card.Title != "PRIVATE" {
		t.Errorf("Card.Title = %q, want PRIVATE", bp.Card.Title)
	}
}

func TestGetVersion_VersionMismatch_404(t *testing.T) {
	fake := newFakeGitea()
	fake.AddFile("catalog", "bp-X", "blueprint.yaml", blueprintYAML("bp-X", "1.0.0", "X"))
	h, cleanup := newTestHandler(t, fake, true)
	defer cleanup()
	code, _ := doRequest(t, h, http.MethodGet, "/api/v1/catalog/bp-X/versions/9.9.9", "")
	if code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", code)
	}
}

func TestList_RejectsExpiredToken(t *testing.T) {
	fake := newFakeGitea()
	h, cleanup := newTestHandler(t, fake, false)
	defer cleanup()
	// expired token
	tok := makeJWT(t, map[string]interface{}{"sub": "alice", "exp": time.Now().Add(-time.Hour).Unix()})
	code, _ := doRequest(t, h, http.MethodGet, "/api/v1/catalog", tok)
	if code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", code)
	}
}
