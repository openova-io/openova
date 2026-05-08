package gitea

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeGitea is a tiny in-memory Gitea-API stub. It supports only the
// endpoints the Client uses + records request count per (method, path)
// for assertion.
type fakeGitea struct {
	mu    sync.Mutex
	files map[string][]byte // key: org/repo/branch/path
	repos map[string]bool   // key: org/repo
	calls map[string]int    // key: METHOD path
}

func newFakeGitea() *fakeGitea {
	return &fakeGitea{
		files: make(map[string][]byte),
		repos: make(map[string]bool),
		calls: make(map[string]int),
	}
}

func (f *fakeGitea) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls[r.Method+" "+r.URL.Path] = f.calls[r.Method+" "+r.URL.Path] + 1
		f.mu.Unlock()

		// /api/v1/repos/<org>/<repo>          GET (probe), POST (create from /orgs/.../repos)
		// /api/v1/orgs/<org>/repos            POST
		// /api/v1/repos/<org>/<repo>/contents/<path...>  GET/POST/PUT/DELETE
		path := r.URL.Path

		switch {
		case strings.HasPrefix(path, "/api/v1/orgs/") && strings.HasSuffix(path, "/repos") && r.Method == http.MethodPost:
			parts := strings.Split(path, "/")
			// /api/v1/orgs/<org>/repos -> parts: ["", "api", "v1", "orgs", <org>, "repos"]
			if len(parts) != 6 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			org := parts[4]
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			repo, _ := body["name"].(string)
			f.mu.Lock()
			f.repos[org+"/"+repo] = true
			f.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"name": repo})
			return

		case strings.HasPrefix(path, "/api/v1/repos/"):
			rest := strings.TrimPrefix(path, "/api/v1/repos/")
			segs := strings.SplitN(rest, "/", 4)
			if len(segs) < 2 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			org, repo := segs[0], segs[1]

			// Probe: /api/v1/repos/<org>/<repo>
			if len(segs) == 2 {
				f.mu.Lock()
				exists := f.repos[org+"/"+repo]
				f.mu.Unlock()
				if !exists {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]any{"name": repo})
				return
			}

			// /api/v1/repos/<org>/<repo>/contents/<path...>
			if len(segs) == 4 && segs[2] == "contents" {
				p := segs[3]
				branch := r.URL.Query().Get("ref")
				if branch == "" {
					branch = "main"
				}
				key := org + "/" + repo + "/" + branch + "/" + p

				switch r.Method {
				case http.MethodGet:
					f.mu.Lock()
					content, ok := f.files[key]
					f.mu.Unlock()
					if !ok {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					_ = json.NewEncoder(w).Encode(FileResponse{
						Path:    p,
						SHA:     "sha-" + key,
						Content: base64.StdEncoding.EncodeToString(content),
						Type:    "file",
					})
					return
				case http.MethodPost, http.MethodPut:
					body, _ := io.ReadAll(r.Body)
					var p commitFilePayload
					_ = json.Unmarshal(body, &p)
					decoded, _ := base64.StdEncoding.DecodeString(p.Content)
					f.mu.Lock()
					f.files[key] = decoded
					f.mu.Unlock()
					_ = json.NewEncoder(w).Encode(FileResponse{
						Path: segs[3], SHA: "sha-" + key, Content: p.Content, Type: "file",
					})
					return
				case http.MethodDelete:
					f.mu.Lock()
					delete(f.files, key)
					f.mu.Unlock()
					w.WriteHeader(http.StatusOK)
					return
				}
			}
		}
		w.WriteHeader(http.StatusNotFound)
	})
}

func newClientFor(srv *httptest.Server) *Client {
	c := NewClient(srv.URL, "test-token")
	c.HTTP = srv.Client()
	return c
}

func TestEnsureRepo_CreateAndIdempotent(t *testing.T) {
	t.Parallel()
	fake := newFakeGitea()
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)

	c := newClientFor(srv)
	ctx := context.Background()

	if err := c.EnsureRepo(ctx, "catalog", "bp-test"); err != nil {
		t.Fatalf("first EnsureRepo: %v", err)
	}
	if err := c.EnsureRepo(ctx, "catalog", "bp-test"); err != nil {
		t.Fatalf("second EnsureRepo (idempotent): %v", err)
	}
	// Probe count: 2 GETs, 1 POST.
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if got := fake.calls["GET /api/v1/repos/catalog/bp-test"]; got != 2 {
		t.Errorf("expected 2 GETs, got %d", got)
	}
	if got := fake.calls["POST /api/v1/orgs/catalog/repos"]; got != 1 {
		t.Errorf("expected 1 POST, got %d", got)
	}
}

func TestPutFile_CreateUpdateIdempotent(t *testing.T) {
	t.Parallel()
	fake := newFakeGitea()
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)

	c := newClientFor(srv)
	ctx := context.Background()
	_ = c.EnsureRepo(ctx, "catalog", "bp-test")

	// First PutFile creates.
	if _, err := c.PutFile(ctx, "catalog", "bp-test", "main", "blueprint.yaml", []byte("v1\n"), "init"); err != nil {
		t.Fatalf("first PutFile: %v", err)
	}
	// Re-PutFile with identical content is a no-op (no PUT, only the
	// probe GET).
	fake.mu.Lock()
	beforePuts := fake.calls["PUT /api/v1/repos/catalog/bp-test/contents/blueprint.yaml"]
	fake.mu.Unlock()

	if _, err := c.PutFile(ctx, "catalog", "bp-test", "main", "blueprint.yaml", []byte("v1\n"), "noop"); err != nil {
		t.Fatalf("idempotent PutFile: %v", err)
	}

	fake.mu.Lock()
	afterPuts := fake.calls["PUT /api/v1/repos/catalog/bp-test/contents/blueprint.yaml"]
	fake.mu.Unlock()
	if afterPuts != beforePuts {
		t.Errorf("idempotent PutFile triggered %d new PUTs", afterPuts-beforePuts)
	}

	// PutFile with new content updates.
	if _, err := c.PutFile(ctx, "catalog", "bp-test", "main", "blueprint.yaml", []byte("v2\n"), "bump"); err != nil {
		t.Fatalf("update PutFile: %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if got := fake.calls["PUT /api/v1/repos/catalog/bp-test/contents/blueprint.yaml"]; got != 1 {
		t.Errorf("expected 1 update PUT, got %d", got)
	}
}

func TestDeleteFile_PresentAndAbsent(t *testing.T) {
	t.Parallel()
	fake := newFakeGitea()
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)

	c := newClientFor(srv)
	ctx := context.Background()
	_ = c.EnsureRepo(ctx, "catalog", "bp-test")
	if _, err := c.PutFile(ctx, "catalog", "bp-test", "main", "blueprint.yaml", []byte("x"), "init"); err != nil {
		t.Fatalf("PutFile: %v", err)
	}

	// Delete present file.
	deleted, err := c.DeleteFile(ctx, "catalog", "bp-test", "main", "blueprint.yaml", "withdraw")
	if err != nil || !deleted {
		t.Fatalf("DeleteFile: %v deleted=%v", err, deleted)
	}

	// Delete already-absent file → idempotent.
	deleted, err = c.DeleteFile(ctx, "catalog", "bp-test", "main", "blueprint.yaml", "withdraw-again")
	if err != nil || !deleted {
		t.Fatalf("idempotent DeleteFile: %v deleted=%v", err, deleted)
	}
}

func TestIsNotFound(t *testing.T) {
	t.Parallel()
	if IsNotFound(nil) {
		t.Error("IsNotFound(nil) = true")
	}
	notFound := &HTTPError{Status: 404}
	if !IsNotFound(notFound) {
		t.Error("IsNotFound(404) = false")
	}
	other := &HTTPError{Status: 500}
	if IsNotFound(other) {
		t.Error("IsNotFound(500) = true")
	}
}
