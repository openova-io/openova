// gitea/client_test.go — exercise the find-or-create paths against an
// in-process httptest stub. These are unit-level — the bigger
// integration tests live in the reconciler package which exercises
// this client end-to-end.

package gitea

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newStub(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(srv.URL, "tok")
}

func TestEnsureOrg_FindHits(t *testing.T) {
	t.Parallel()
	calls := map[string]int{}
	c := newStub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls[r.Method+" "+r.URL.Path]++
		if r.URL.Path == "/api/v1/orgs/acme" && r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(Org{ID: 1, Username: "acme"})
			return
		}
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	o, err := c.EnsureOrg(context.Background(), "acme", "ACME", "desc", "private")
	if err != nil {
		t.Fatalf("EnsureOrg: %v", err)
	}
	if o.ID != 1 || o.Username != "acme" {
		t.Fatalf("got %+v", o)
	}
	if calls["GET /api/v1/orgs/acme"] != 1 {
		t.Errorf("expected 1 GET, got %d", calls["GET /api/v1/orgs/acme"])
	}
	if calls["POST /api/v1/admin/orgs"] != 0 {
		t.Errorf("expected 0 POST when org pre-exists, got %d", calls["POST /api/v1/admin/orgs"])
	}
}

func TestEnsureOrg_CreatesWhenMissing(t *testing.T) {
	t.Parallel()
	state := map[string]Org{}
	c := newStub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/orgs/"):
			slug := strings.TrimPrefix(r.URL.Path, "/api/v1/orgs/")
			if o, ok := state[slug]; ok {
				_ = json.NewEncoder(w).Encode(o)
				return
			}
			http.Error(w, "nope", http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/admin/orgs":
			var b struct {
				Username string `json:"username"`
			}
			_ = json.NewDecoder(r.Body).Decode(&b)
			o := Org{ID: 7, Username: b.Username}
			state[b.Username] = o
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(o)
		default:
			http.Error(w, "unhandled", http.StatusInternalServerError)
		}
	}))
	o, err := c.EnsureOrg(context.Background(), "newone", "NewOne", "", "private")
	if err != nil {
		t.Fatalf("EnsureOrg: %v", err)
	}
	if o.ID != 7 {
		t.Errorf("expected new ID 7, got %d", o.ID)
	}
}

func TestEnsureOrg_409Race(t *testing.T) {
	t.Parallel()
	// First GET → 404; POST → 422 (raced); second GET → 200.
	step := 0
	c := newStub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	o, err := c.EnsureOrg(context.Background(), "raced", "Raced", "", "private")
	if err != nil {
		t.Fatalf("EnsureOrg: %v", err)
	}
	if o.ID != 99 {
		t.Errorf("expected re-find to return existing ID 99, got %d", o.ID)
	}
}

func TestPutFile_ByteEqualNoOp(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"acme/repo/foo.yaml": "abc",
	}
	postCalls := 0
	putCalls := 0
	c := newStub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/api/v1/repos/")
		// Strip /contents/ to get owner/repo + path.
		idx := strings.Index(key, "/contents/")
		if idx < 0 {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		ownerRepo := key[:idx]
		filePath := key[idx+len("/contents/"):]
		joined := ownerRepo + "/" + filePath
		switch r.Method {
		case http.MethodGet:
			if v, ok := files[joined]; ok {
				_ = json.NewEncoder(w).Encode(FileContent{
					Path:    filePath,
					SHA:     "old-sha",
					Type:    "file",
					Content: base64.StdEncoding.EncodeToString([]byte(v)),
				})
				return
			}
			http.Error(w, "no", http.StatusNotFound)
		case http.MethodPost:
			postCalls++
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"content": FileContent{Path: filePath, SHA: "new-sha"}})
		case http.MethodPut:
			putCalls++
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"content": FileContent{Path: filePath, SHA: "updated-sha"}})
		}
	}))

	// Existing file, byte-equal write — no PUT.
	if _, err := c.PutFile(context.Background(), "acme", "repo", "foo.yaml", "main", []byte("abc"), "no-op"); err != nil {
		t.Fatalf("PutFile noop: %v", err)
	}
	if putCalls != 0 || postCalls != 0 {
		t.Errorf("byte-equal PutFile should not write: got POST=%d PUT=%d", postCalls, putCalls)
	}

	// Missing file → POST.
	if _, err := c.PutFile(context.Background(), "acme", "repo", "bar.yaml", "main", []byte("xyz"), ""); err != nil {
		t.Fatalf("PutFile create: %v", err)
	}
	if postCalls != 1 {
		t.Errorf("expected 1 POST for new file, got %d", postCalls)
	}

	// Mutated existing → PUT.
	if _, err := c.PutFile(context.Background(), "acme", "repo", "foo.yaml", "main", []byte("changed"), ""); err != nil {
		t.Fatalf("PutFile update: %v", err)
	}
	if putCalls != 1 {
		t.Errorf("expected 1 PUT for changed file, got %d", putCalls)
	}
}

func TestGetOrg_NotFound(t *testing.T) {
	t.Parallel()
	c := newStub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusNotFound)
	}))
	_, err := c.GetOrg(context.Background(), "ghost")
	if !errors.Is(err, ErrOrgNotFound) {
		t.Errorf("expected ErrOrgNotFound, got %v", err)
	}
}
