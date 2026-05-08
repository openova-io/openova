package gitea

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeGiteaServer is a tiny in-memory Gitea-compatible HTTP server used
// for unit-testing the client. Mirrors the fakeGiteaCounter shape from
// slice C3 (blueprint-controller).
type fakeGiteaServer struct {
	mu sync.Mutex

	// orgs that exist (Map[org]bool).
	orgs map[string]bool
	// repos that exist (Map[org/repo]bool).
	repos map[string]bool
	// branches per repo (Map[org/repo]Map[branch]bool).
	branches map[string]map[string]bool
	// files per (org, repo, branch, path) → content+sha.
	files map[string]fakeFile
}

type fakeFile struct {
	content []byte
	sha     string
}

func newFakeGiteaServer() *fakeGiteaServer {
	return &fakeGiteaServer{
		orgs:     map[string]bool{},
		repos:    map[string]bool{},
		branches: map[string]map[string]bool{},
		files:    map[string]fakeFile{},
	}
}

func (s *fakeGiteaServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		path := r.URL.Path

		// /api/v1/repos/<org>/<repo>             GET probe
		// /api/v1/orgs/<org>/repos                POST create
		// /api/v1/repos/<org>/<repo>/contents/...
		// /api/v1/repos/<org>/<repo>/branches      POST
		// /api/v1/repos/<org>/<repo>/branches/<n>  GET probe
		switch {
		case strings.HasPrefix(path, "/api/v1/orgs/") && strings.HasSuffix(path, "/repos") && r.Method == http.MethodPost:
			parts := strings.Split(path, "/")
			org := parts[4]
			if !s.orgs[org] {
				http.Error(w, "org not found", http.StatusNotFound)
				return
			}
			// Body has `name`. We don't bother parsing — the test
			// only checks that EnsureRepo's POST went through.
			// The repo name is appended to the org+repo map; we
			// recover it by snooping the body.
			var name string
			b := make([]byte, r.ContentLength)
			r.Body.Read(b)
			// extremely cheap: find `"name":"<n>"`
			if i := strings.Index(string(b), `"name":"`); i >= 0 {
				rest := string(b)[i+8:]
				if j := strings.Index(rest, `"`); j >= 0 {
					name = rest[:j]
				}
			}
			s.repos[org+"/"+name] = true
			s.branches[org+"/"+name] = map[string]bool{"main": true}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
			return

		case strings.HasPrefix(path, "/api/v1/repos/") && r.Method == http.MethodGet && !strings.Contains(path, "/contents/") && !strings.Contains(path, "/branches"):
			// GET /repos/<org>/<repo>
			parts := strings.Split(strings.TrimPrefix(path, "/api/v1/repos/"), "/")
			if len(parts) >= 2 {
				key := parts[0] + "/" + parts[1]
				if s.repos[key] {
					_, _ = w.Write([]byte(`{}`))
					return
				}
			}
			http.Error(w, "not found", http.StatusNotFound)
			return

		case strings.Contains(path, "/branches/") && r.Method == http.MethodGet:
			// GET /repos/<org>/<repo>/branches/<branch>
			parts := strings.Split(strings.TrimPrefix(path, "/api/v1/repos/"), "/")
			if len(parts) >= 4 {
				repoKey := parts[0] + "/" + parts[1]
				branch := parts[3]
				if s.branches[repoKey][branch] {
					_, _ = w.Write([]byte(`{}`))
					return
				}
			}
			http.Error(w, "not found", http.StatusNotFound)
			return

		case strings.HasSuffix(path, "/branches") && r.Method == http.MethodPost:
			parts := strings.Split(strings.TrimPrefix(path, "/api/v1/repos/"), "/")
			if len(parts) >= 3 {
				repoKey := parts[0] + "/" + parts[1]
				if s.branches[repoKey] == nil {
					s.branches[repoKey] = map[string]bool{}
				}
				// snoop new_branch_name
				b := make([]byte, r.ContentLength)
				r.Body.Read(b)
				if i := strings.Index(string(b), `"new_branch_name":"`); i >= 0 {
					rest := string(b)[i+19:]
					if j := strings.Index(rest, `"`); j >= 0 {
						s.branches[repoKey][rest[:j]] = true
					}
				}
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{}`))
				return
			}
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}

		http.Error(w, "unhandled "+r.Method+" "+path, http.StatusNotImplemented)
	})
}

func TestClient_EnsureRepo_OrgMissing(t *testing.T) {
	srv := newFakeGiteaServer()
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	c := NewClient(ts.URL, "test-token")
	err := c.EnsureRepo(context.Background(), "missing-org", "myapp")
	if !errors.Is(err, ErrOrgNotFound) {
		t.Errorf("expected ErrOrgNotFound, got %v", err)
	}
}

func TestClient_EnsureRepo_CreatesIfMissing(t *testing.T) {
	srv := newFakeGiteaServer()
	srv.orgs["acme"] = true
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	c := NewClient(ts.URL, "test-token")
	if err := c.EnsureRepo(context.Background(), "acme", "site"); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !srv.repos["acme/site"] {
		t.Error("repo should have been created")
	}
}

func TestClient_EnsureBranch_Idempotent(t *testing.T) {
	srv := newFakeGiteaServer()
	srv.orgs["acme"] = true
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	c := NewClient(ts.URL, "test-token")
	if err := c.EnsureRepo(context.Background(), "acme", "site"); err != nil {
		t.Fatalf("ensure repo: %v", err)
	}
	// main exists from EnsureRepo's auto_init — short-circuit.
	if err := c.EnsureBranch(context.Background(), "acme", "site", "main"); err != nil {
		t.Errorf("EnsureBranch main should be no-op: %v", err)
	}
	// develop doesn't exist — create.
	if err := c.EnsureBranch(context.Background(), "acme", "site", "develop"); err != nil {
		t.Errorf("EnsureBranch develop: %v", err)
	}
	if !srv.branches["acme/site"]["develop"] {
		t.Error("develop branch should have been created")
	}
	// re-call — idempotent (probe sees branch, no POST).
	if err := c.EnsureBranch(context.Background(), "acme", "site", "develop"); err != nil {
		t.Errorf("EnsureBranch develop (2nd call): %v", err)
	}
}

func TestClient_IsNotFound(t *testing.T) {
	if IsNotFound(nil) {
		t.Error("nil should not be NotFound")
	}
	if IsNotFound(errors.New("plain")) {
		t.Error("plain error should not be NotFound")
	}
	he := &HTTPError{Status: 404}
	if !IsNotFound(he) {
		t.Error("HTTPError 404 should be NotFound")
	}
	if IsNotFound(&HTTPError{Status: 500}) {
		t.Error("HTTPError 500 should not be NotFound")
	}
}
