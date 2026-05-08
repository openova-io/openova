// gitea/client_test.go — exercises the SUPERSET surface with httptest
// server fakes. The patterns mirror C1's gs.handle / C3's
// fakeGitea.handler / C4's fakeGiteaServer.handler so consumers
// migrating from per-controller clients see the same coverage.

package gitea

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// ----------------------------------------------------------------------
// In-memory Gitea-API stub
// ----------------------------------------------------------------------

type fakeGitea struct {
	mu       sync.Mutex
	orgs     map[string]Org
	repos    map[string]Repo
	branches map[string]map[string]bool
	files    map[string]fakeFile
	calls    map[string]int
}

type fakeFile struct {
	content []byte
	sha     string
}

func newFake() *fakeGitea {
	return &fakeGitea{
		orgs:     map[string]Org{},
		repos:    map[string]Repo{},
		branches: map[string]map[string]bool{},
		files:    map[string]fakeFile{},
		calls:    map[string]int{},
	}
}

func (f *fakeGitea) callCount(method, path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[method+" "+path]
}

func (f *fakeGitea) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls[r.Method+" "+r.URL.Path]++
		f.mu.Unlock()

		if r.Header.Get("Authorization") == "" {
			http.Error(w, "no auth", http.StatusUnauthorized)
			return
		}

		p := r.URL.Path

		// GET /api/v1/orgs/{slug}
		if r.Method == http.MethodGet && strings.HasPrefix(p, "/api/v1/orgs/") && !strings.Contains(p[len("/api/v1/orgs/"):], "/") {
			slug := p[len("/api/v1/orgs/"):]
			f.mu.Lock()
			o, ok := f.orgs[slug]
			f.mu.Unlock()
			if !ok {
				http.Error(w, "no such org", http.StatusNotFound)
				return
			}
			writeJSON(w, http.StatusOK, o)
			return
		}

		// POST /api/v1/admin/orgs
		if r.Method == http.MethodPost && p == "/api/v1/admin/orgs" {
			var body adminOrgCreate
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			defer f.mu.Unlock()
			if _, dup := f.orgs[body.Username]; dup {
				http.Error(w, "exists", http.StatusUnprocessableEntity)
				return
			}
			o := Org{
				ID:          int64(len(f.orgs) + 1),
				Username:    body.Username,
				FullName:    body.FullName,
				Description: body.Description,
				Visibility:  body.Visibility,
			}
			f.orgs[body.Username] = o
			writeJSON(w, http.StatusCreated, o)
			return
		}

		// POST /api/v1/orgs/{org}/repos
		if r.Method == http.MethodPost && strings.HasPrefix(p, "/api/v1/orgs/") && strings.HasSuffix(p, "/repos") {
			owner := strings.TrimSuffix(strings.TrimPrefix(p, "/api/v1/orgs/"), "/repos")
			f.mu.Lock()
			defer f.mu.Unlock()
			if _, ok := f.orgs[owner]; !ok {
				http.Error(w, "no such org", http.StatusNotFound)
				return
			}
			var body repoCreate
			_ = json.NewDecoder(r.Body).Decode(&body)
			key := owner + "/" + body.Name
			if _, dup := f.repos[key]; dup {
				http.Error(w, "exists", http.StatusUnprocessableEntity)
				return
			}
			rp := Repo{
				ID:            int64(len(f.repos) + 1),
				Name:          body.Name,
				FullName:      key,
				Description:   body.Description,
				Private:       body.Private,
				DefaultBranch: body.DefaultBranch,
			}
			f.repos[key] = rp
			f.branches[key] = map[string]bool{"main": true}
			writeJSON(w, http.StatusCreated, rp)
			return
		}

		// GET /api/v1/repos/{owner}/{repo}
		if r.Method == http.MethodGet && strings.HasPrefix(p, "/api/v1/repos/") && !strings.Contains(p, "/contents/") && !strings.Contains(p, "/branches") {
			rest := strings.TrimRight(strings.TrimPrefix(p, "/api/v1/repos/"), "/")
			parts := strings.Split(rest, "/")
			if len(parts) != 2 {
				http.Error(w, "bad", http.StatusBadRequest)
				return
			}
			f.mu.Lock()
			rp, ok := f.repos[parts[0]+"/"+parts[1]]
			f.mu.Unlock()
			if !ok {
				http.Error(w, "no such repo", http.StatusNotFound)
				return
			}
			writeJSON(w, http.StatusOK, rp)
			return
		}

		// GET /api/v1/repos/{owner}/{repo}/branches/{branch}
		if r.Method == http.MethodGet && strings.Contains(p, "/branches/") {
			rest := strings.TrimPrefix(p, "/api/v1/repos/")
			parts := strings.Split(rest, "/")
			if len(parts) < 4 {
				http.Error(w, "bad", http.StatusBadRequest)
				return
			}
			repoKey := parts[0] + "/" + parts[1]
			branch := parts[3]
			f.mu.Lock()
			ok := f.branches[repoKey] != nil && f.branches[repoKey][branch]
			f.mu.Unlock()
			if !ok {
				http.Error(w, "no branch", http.StatusNotFound)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"name": branch})
			return
		}

		// POST /api/v1/repos/{owner}/{repo}/branches
		if r.Method == http.MethodPost && strings.HasSuffix(p, "/branches") {
			rest := strings.TrimPrefix(p, "/api/v1/repos/")
			parts := strings.Split(rest, "/")
			if len(parts) < 3 {
				http.Error(w, "bad", http.StatusBadRequest)
				return
			}
			repoKey := parts[0] + "/" + parts[1]
			var body struct {
				NewBranchName string `json:"new_branch_name"`
				OldBranchName string `json:"old_branch_name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			if f.branches[repoKey] == nil {
				f.branches[repoKey] = map[string]bool{}
			}
			if f.branches[repoKey][body.NewBranchName] {
				f.mu.Unlock()
				http.Error(w, "exists", http.StatusConflict)
				return
			}
			f.branches[repoKey][body.NewBranchName] = true
			f.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
			return
		}

		// /api/v1/repos/{owner}/{repo}/contents/{path}
		if strings.HasPrefix(p, "/api/v1/repos/") && strings.Contains(p, "/contents/") {
			rest := strings.TrimPrefix(p, "/api/v1/repos/")
			idx := strings.Index(rest, "/contents/")
			if idx < 0 {
				http.Error(w, "bad", http.StatusBadRequest)
				return
			}
			ownerRepo := rest[:idx]
			filePath := rest[idx+len("/contents/"):]
			ownerRepoParts := strings.SplitN(ownerRepo, "/", 2)
			if len(ownerRepoParts) != 2 {
				http.Error(w, "bad", http.StatusBadRequest)
				return
			}
			repoKey := ownerRepoParts[0] + "/" + ownerRepoParts[1]

			// Branch resolution: query for GET, body for POST/PUT/DELETE.
			branch := r.URL.Query().Get("ref")
			var bodyBytes []byte
			if r.Method != http.MethodGet {
				bodyBytes, _ = io.ReadAll(r.Body)
				if branch == "" {
					var probe commitFilePayload
					_ = json.Unmarshal(bodyBytes, &probe)
					branch = probe.Branch
				}
			}
			if branch == "" {
				branch = "main"
			}
			fileKey := repoKey + "/" + branch + "/" + filePath

			f.mu.Lock()
			defer f.mu.Unlock()
			_, repoOK := f.repos[repoKey]
			if !repoOK {
				http.Error(w, `{"message":"The target couldn't be found, repository missing."}`, http.StatusNotFound)
				return
			}

			switch r.Method {
			case http.MethodGet:
				ff, ok := f.files[fileKey]
				if !ok {
					http.Error(w, "no such file", http.StatusNotFound)
					return
				}
				writeJSON(w, http.StatusOK, File{
					Path:          filePath,
					SHA:           ff.sha,
					Type:          "file",
					ContentBase64: base64.StdEncoding.EncodeToString(ff.content),
				})
			case http.MethodPost, http.MethodPut:
				var body commitFilePayload
				_ = json.Unmarshal(bodyBytes, &body)
				decoded, _ := base64.StdEncoding.DecodeString(body.Content)
				if r.Method == http.MethodPost {
					if _, dup := f.files[fileKey]; dup {
						http.Error(w, "exists", http.StatusUnprocessableEntity)
						return
					}
				}
				prevSHA := body.SHA
				if prevSHA == "" {
					prevSHA = "sha"
				}
				newSHA := prevSHA + "+1"
				f.files[fileKey] = fakeFile{content: decoded, sha: newSHA}
				writeJSON(w, http.StatusCreated, map[string]any{
					"content": File{
						Path: filePath,
						SHA:  newSHA,
						Type: "file",
					},
				})
			case http.MethodDelete:
				if _, ok := f.files[fileKey]; !ok {
					http.Error(w, "no", http.StatusNotFound)
					return
				}
				delete(f.files, fileKey)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{}`))
			}
			return
		}

		http.Error(w, "unhandled "+r.Method+" "+p, http.StatusNotFound)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func newClientWithFake(t *testing.T, fake *fakeGitea) *Client {
	t.Helper()
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)
	c := New(srv.URL, "test-token")
	c.HTTP = srv.Client()
	return c
}

// ----------------------------------------------------------------------
// Org tests
// ----------------------------------------------------------------------

func TestEnsureOrg_FindHits(t *testing.T) {
	t.Parallel()
	fake := newFake()
	fake.orgs["acme"] = Org{ID: 1, Username: "acme"}
	c := newClientWithFake(t, fake)

	o, err := c.EnsureOrg(context.Background(), "acme", "ACME", "desc", "private")
	if err != nil {
		t.Fatalf("EnsureOrg: %v", err)
	}
	if o.ID != 1 || o.Username != "acme" {
		t.Fatalf("got %+v", o)
	}
	if got := fake.callCount(http.MethodGet, "/api/v1/orgs/acme"); got != 1 {
		t.Errorf("expected 1 GET, got %d", got)
	}
	if got := fake.callCount(http.MethodPost, "/api/v1/admin/orgs"); got != 0 {
		t.Errorf("expected 0 POST when org pre-exists, got %d", got)
	}
}

func TestEnsureOrg_CreatesWhenMissing(t *testing.T) {
	t.Parallel()
	fake := newFake()
	c := newClientWithFake(t, fake)

	o, err := c.EnsureOrg(context.Background(), "newone", "NewOne", "", "private")
	if err != nil {
		t.Fatalf("EnsureOrg: %v", err)
	}
	if o.Username != "newone" || o.ID == 0 {
		t.Errorf("expected created org, got %+v", o)
	}
	if got := fake.callCount(http.MethodPost, "/api/v1/admin/orgs"); got != 1 {
		t.Errorf("expected 1 POST, got %d", got)
	}
}

func TestEnsureOrg_422Race(t *testing.T) {
	t.Parallel()
	step := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/orgs/raced":
			step++
			if step == 1 {
				http.Error(w, "miss", http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(Org{ID: 99, Username: "raced"})
		case "POST /api/v1/admin/orgs":
			http.Error(w, "duplicate", http.StatusUnprocessableEntity)
		default:
			http.Error(w, "unhandled", http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")
	c.HTTP = srv.Client()

	o, err := c.EnsureOrg(context.Background(), "raced", "Raced", "", "private")
	if err != nil {
		t.Fatalf("EnsureOrg: %v", err)
	}
	if o.ID != 99 {
		t.Errorf("expected re-find ID 99, got %d", o.ID)
	}
}

func TestGetOrg_NotFound(t *testing.T) {
	t.Parallel()
	fake := newFake()
	c := newClientWithFake(t, fake)
	_, err := c.GetOrg(context.Background(), "ghost")
	if !errors.Is(err, ErrOrgNotFound) {
		t.Errorf("expected ErrOrgNotFound, got %v", err)
	}
}

func TestGetOrg_ServerError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"message":"boom"}`)
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")
	c.HTTP = srv.Client()

	_, err := c.GetOrg(context.Background(), "acme")
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, ErrOrgNotFound) {
		t.Errorf("500 should not map to ErrOrgNotFound")
	}
}

func TestGetOrg_EmptySlug(t *testing.T) {
	t.Parallel()
	c := New("http://nope", "tok")
	_, err := c.GetOrg(context.Background(), "")
	if err == nil {
		t.Error("expected error on empty slug")
	}
}

// ----------------------------------------------------------------------
// Repo tests
// ----------------------------------------------------------------------

func TestEnsureRepo_FindHits(t *testing.T) {
	t.Parallel()
	fake := newFake()
	fake.orgs["acme"] = Org{ID: 1, Username: "acme"}
	fake.repos["acme/site"] = Repo{ID: 7, Name: "site", FullName: "acme/site"}
	c := newClientWithFake(t, fake)

	r, err := c.EnsureRepo(context.Background(), "acme", "site", "desc", false)
	if err != nil {
		t.Fatalf("EnsureRepo: %v", err)
	}
	if r.ID != 7 {
		t.Errorf("expected ID 7, got %d", r.ID)
	}
	if got := fake.callCount(http.MethodPost, "/api/v1/orgs/acme/repos"); got != 0 {
		t.Errorf("expected 0 POST when repo pre-exists, got %d", got)
	}
}

func TestEnsureRepo_CreatesWithPrivate(t *testing.T) {
	t.Parallel()
	fake := newFake()
	fake.orgs["acme"] = Org{Username: "acme"}
	c := newClientWithFake(t, fake)

	r, err := c.EnsureRepo(context.Background(), "acme", "site", "desc", true)
	if err != nil {
		t.Fatalf("EnsureRepo: %v", err)
	}
	if r.Name != "site" || !r.Private {
		t.Errorf("expected created private repo, got %+v", r)
	}
	if got := fake.callCount(http.MethodPost, "/api/v1/orgs/acme/repos"); got != 1 {
		t.Errorf("expected 1 POST, got %d", got)
	}
}

func TestEnsureRepo_OrgMissing(t *testing.T) {
	t.Parallel()
	fake := newFake()
	c := newClientWithFake(t, fake)
	_, err := c.EnsureRepo(context.Background(), "missing", "site", "", false)
	if !errors.Is(err, ErrOrgNotFound) {
		t.Errorf("expected ErrOrgNotFound, got %v", err)
	}
}

func TestEnsureRepo_422Race(t *testing.T) {
	t.Parallel()
	step := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/acme/site":
			step++
			if step == 1 {
				http.Error(w, "miss", http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(Repo{ID: 42, Name: "site"})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/orgs/acme/repos":
			http.Error(w, "duplicate", http.StatusUnprocessableEntity)
		default:
			http.Error(w, "unhandled", http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")
	c.HTTP = srv.Client()

	r, err := c.EnsureRepo(context.Background(), "acme", "site", "", false)
	if err != nil {
		t.Fatalf("EnsureRepo: %v", err)
	}
	if r.ID != 42 {
		t.Errorf("expected re-find ID 42, got %d", r.ID)
	}
}

// ----------------------------------------------------------------------
// Branch tests
// ----------------------------------------------------------------------

func TestEnsureBranch_MainNoOp(t *testing.T) {
	t.Parallel()
	fake := newFake()
	c := newClientWithFake(t, fake)
	if err := c.EnsureBranch(context.Background(), "acme", "site", "main"); err != nil {
		t.Errorf("main: %v", err)
	}
	if err := c.EnsureBranch(context.Background(), "acme", "site", ""); err != nil {
		t.Errorf("empty: %v", err)
	}
	for k, v := range fake.calls {
		if v != 0 {
			t.Errorf("EnsureBranch main/empty issued %s: %d calls", k, v)
		}
	}
}

func TestEnsureBranch_CreatesDevelop(t *testing.T) {
	t.Parallel()
	fake := newFake()
	fake.orgs["acme"] = Org{Username: "acme"}
	c := newClientWithFake(t, fake)

	if _, err := c.EnsureRepo(context.Background(), "acme", "site", "", false); err != nil {
		t.Fatalf("EnsureRepo: %v", err)
	}
	if err := c.EnsureBranch(context.Background(), "acme", "site", "develop"); err != nil {
		t.Fatalf("EnsureBranch develop: %v", err)
	}
	if !fake.branches["acme/site"]["develop"] {
		t.Error("develop should have been created")
	}
}

func TestEnsureBranch_Idempotent(t *testing.T) {
	t.Parallel()
	fake := newFake()
	fake.orgs["acme"] = Org{Username: "acme"}
	c := newClientWithFake(t, fake)

	if _, err := c.EnsureRepo(context.Background(), "acme", "site", "", false); err != nil {
		t.Fatalf("EnsureRepo: %v", err)
	}
	if err := c.EnsureBranch(context.Background(), "acme", "site", "develop"); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := c.EnsureBranch(context.Background(), "acme", "site", "develop"); err != nil {
		t.Fatalf("second (idempotent): %v", err)
	}
}

// ----------------------------------------------------------------------
// File tests
// ----------------------------------------------------------------------

func TestPutFile_CreateNew(t *testing.T) {
	t.Parallel()
	fake := newFake()
	fake.orgs["acme"] = Org{Username: "acme"}
	c := newClientWithFake(t, fake)
	if _, err := c.EnsureRepo(context.Background(), "acme", "site", "", false); err != nil {
		t.Fatalf("EnsureRepo: %v", err)
	}

	_, committed, err := c.PutFile(context.Background(), "acme", "site", "main",
		"clusters/x/y.yaml", []byte("hello\n"), "init")
	if err != nil {
		t.Fatalf("PutFile: %v", err)
	}
	if !committed {
		t.Error("expected committed=true on create")
	}
	if got := fake.callCount(http.MethodPost, "/api/v1/repos/acme/site/contents/clusters/x/y.yaml"); got != 1 {
		t.Errorf("expected 1 POST, got %d", got)
	}
}

func TestPutFile_UpdateExisting(t *testing.T) {
	t.Parallel()
	fake := newFake()
	fake.orgs["acme"] = Org{Username: "acme"}
	c := newClientWithFake(t, fake)
	if _, err := c.EnsureRepo(context.Background(), "acme", "site", "", false); err != nil {
		t.Fatalf("EnsureRepo: %v", err)
	}

	if _, _, err := c.PutFile(context.Background(), "acme", "site", "main", "f.yaml", []byte("v1\n"), "init"); err != nil {
		t.Fatalf("create: %v", err)
	}
	_, committed, err := c.PutFile(context.Background(), "acme", "site", "main",
		"f.yaml", []byte("v2\n"), "bump")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !committed {
		t.Error("expected committed=true on update")
	}
	if got := fake.callCount(http.MethodPut, "/api/v1/repos/acme/site/contents/f.yaml"); got != 1 {
		t.Errorf("expected 1 PUT, got %d", got)
	}
}

func TestPutFile_ByteEqualNoOp(t *testing.T) {
	t.Parallel()
	fake := newFake()
	fake.orgs["acme"] = Org{Username: "acme"}
	c := newClientWithFake(t, fake)
	if _, err := c.EnsureRepo(context.Background(), "acme", "site", "", false); err != nil {
		t.Fatalf("EnsureRepo: %v", err)
	}

	if _, _, err := c.PutFile(context.Background(), "acme", "site", "main", "f.yaml", []byte("same\n"), "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	postsBefore := fake.callCount(http.MethodPost, "/api/v1/repos/acme/site/contents/f.yaml")
	putsBefore := fake.callCount(http.MethodPut, "/api/v1/repos/acme/site/contents/f.yaml")

	_, committed, err := c.PutFile(context.Background(), "acme", "site", "main",
		"f.yaml", []byte("same\n"), "noop")
	if err != nil {
		t.Fatalf("noop: %v", err)
	}
	if committed {
		t.Error("expected committed=false on byte-equal write")
	}

	postsAfter := fake.callCount(http.MethodPost, "/api/v1/repos/acme/site/contents/f.yaml")
	putsAfter := fake.callCount(http.MethodPut, "/api/v1/repos/acme/site/contents/f.yaml")
	if postsAfter != postsBefore || putsAfter != putsBefore {
		t.Errorf("byte-equal PutFile issued writes: POST %d→%d, PUT %d→%d",
			postsBefore, postsAfter, putsBefore, putsAfter)
	}
}

func TestPutFile_WithAuthor(t *testing.T) {
	t.Parallel()
	captured := struct {
		body []byte
		sync.Mutex
	}{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			http.Error(w, "miss", http.StatusNotFound)
		case http.MethodPost:
			b, _ := io.ReadAll(r.Body)
			captured.Lock()
			captured.body = b
			captured.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"content":{"path":"f.yaml","sha":"sha1"}}`)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")
	c.HTTP = srv.Client()

	_, _, err := c.PutFile(context.Background(), "acme", "site", "main",
		"f.yaml", []byte("hello"), "init",
		PutFileOpts{AuthorName: "bot", AuthorEmail: "bot@x"})
	if err != nil {
		t.Fatalf("PutFile: %v", err)
	}
	captured.Lock()
	defer captured.Unlock()
	var got commitFilePayload
	if err := json.Unmarshal(captured.body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Author == nil || got.Author.Name != "bot" || got.Author.Email != "bot@x" {
		t.Errorf("expected author=bot/bot@x, got %+v", got.Author)
	}
	if got.Committer == nil || got.Committer.Name != "bot" {
		t.Errorf("expected committer mirrored, got %+v", got.Committer)
	}
}

func TestPutFile_RepoNotFound(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"The target couldn't be found, repository missing."}`)
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")
	c.HTTP = srv.Client()

	_, _, err := c.PutFile(context.Background(), "acme", "missing", "main", "f.yaml", []byte("x"), "msg")
	if !errors.Is(err, ErrRepoNotFound) {
		t.Errorf("expected ErrRepoNotFound, got %v", err)
	}
}

func TestGetFile_OK(t *testing.T) {
	t.Parallel()
	fake := newFake()
	fake.orgs["acme"] = Org{Username: "acme"}
	c := newClientWithFake(t, fake)
	if _, err := c.EnsureRepo(context.Background(), "acme", "site", "", false); err != nil {
		t.Fatalf("EnsureRepo: %v", err)
	}
	if _, _, err := c.PutFile(context.Background(), "acme", "site", "main", "f.yaml", []byte("hello\n"), "init"); err != nil {
		t.Fatalf("seed PutFile: %v", err)
	}

	f, err := c.GetFile(context.Background(), "acme", "site", "main", "f.yaml")
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if f.Path != "f.yaml" {
		t.Errorf("expected path f.yaml, got %q", f.Path)
	}
	dec, err := f.Decoded()
	if err != nil {
		t.Fatalf("Decoded: %v", err)
	}
	if string(dec) != "hello\n" {
		t.Errorf("expected decoded=hello, got %q", dec)
	}
}

func TestGetFile_FileNotFound(t *testing.T) {
	t.Parallel()
	fake := newFake()
	fake.orgs["acme"] = Org{Username: "acme"}
	c := newClientWithFake(t, fake)
	if _, err := c.EnsureRepo(context.Background(), "acme", "site", "", false); err != nil {
		t.Fatalf("EnsureRepo: %v", err)
	}

	_, err := c.GetFile(context.Background(), "acme", "site", "main", "absent.yaml")
	if !errors.Is(err, ErrFileNotFound) {
		t.Errorf("expected ErrFileNotFound, got %v", err)
	}
}

func TestGetFile_RepoNotFound(t *testing.T) {
	t.Parallel()
	fake := newFake()
	fake.orgs["acme"] = Org{Username: "acme"}
	c := newClientWithFake(t, fake)

	_, err := c.GetFile(context.Background(), "acme", "missing", "main", "f.yaml")
	if !errors.Is(err, ErrRepoNotFound) {
		t.Errorf("expected ErrRepoNotFound, got %v", err)
	}
}

func TestDeleteFile_Present(t *testing.T) {
	t.Parallel()
	fake := newFake()
	fake.orgs["acme"] = Org{Username: "acme"}
	c := newClientWithFake(t, fake)
	if _, err := c.EnsureRepo(context.Background(), "acme", "site", "", false); err != nil {
		t.Fatalf("EnsureRepo: %v", err)
	}
	if _, _, err := c.PutFile(context.Background(), "acme", "site", "main", "f.yaml", []byte("x"), "init"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	deleted, err := c.DeleteFile(context.Background(), "acme", "site", "main", "f.yaml", "withdraw")
	if err != nil || !deleted {
		t.Fatalf("DeleteFile: %v deleted=%v", err, deleted)
	}
}

func TestDeleteFile_AbsentIsIdempotent(t *testing.T) {
	t.Parallel()
	fake := newFake()
	fake.orgs["acme"] = Org{Username: "acme"}
	c := newClientWithFake(t, fake)
	if _, err := c.EnsureRepo(context.Background(), "acme", "site", "", false); err != nil {
		t.Fatalf("EnsureRepo: %v", err)
	}

	deleted, err := c.DeleteFile(context.Background(), "acme", "site", "main", "absent.yaml", "no-op")
	if err != nil || !deleted {
		t.Fatalf("idempotent DeleteFile: %v deleted=%v", err, deleted)
	}
}

// ----------------------------------------------------------------------
// Error helpers
// ----------------------------------------------------------------------

func TestIsNotFound(t *testing.T) {
	t.Parallel()
	if IsNotFound(nil) {
		t.Error("nil should not be NotFound")
	}
	if !IsNotFound(ErrOrgNotFound) || !IsNotFound(ErrRepoNotFound) || !IsNotFound(ErrFileNotFound) {
		t.Error("typed sentinels should be NotFound")
	}
	if !IsNotFound(&HTTPError{Status: 404}) {
		t.Error("HTTPError 404 should be NotFound")
	}
	if IsNotFound(&HTTPError{Status: 500}) {
		t.Error("HTTPError 500 should not be NotFound")
	}
	if IsNotFound(errors.New("plain")) {
		t.Error("plain error should not be NotFound")
	}
}

func TestIsConflict(t *testing.T) {
	t.Parallel()
	if IsConflict(nil) {
		t.Error("nil should not be Conflict")
	}
	if !IsConflict(&HTTPError{Status: 409}) {
		t.Error("HTTPError 409 should be Conflict")
	}
	if IsConflict(&HTTPError{Status: 404}) {
		t.Error("HTTPError 404 should not be Conflict")
	}
}

func TestFile_Decoded(t *testing.T) {
	t.Parallel()
	f := File{ContentBase64: base64.StdEncoding.EncodeToString([]byte("abc"))}
	dec, err := f.Decoded()
	if err != nil {
		t.Fatalf("Decoded: %v", err)
	}
	if string(dec) != "abc" {
		t.Errorf("expected abc, got %q", dec)
	}

	f2 := File{ContentBase64: "YWJj\nZGVm\n"}
	dec2, err := f2.Decoded()
	if err != nil {
		t.Fatalf("Decoded with newlines: %v", err)
	}
	if string(dec2) != "abcdef" {
		t.Errorf("expected abcdef, got %q", dec2)
	}

	var nilF *File
	if dec, err := nilF.Decoded(); err != nil || dec != nil {
		t.Errorf("nil File should return nil bytes, got %v %v", dec, err)
	}
}
