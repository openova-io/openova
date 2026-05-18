package github

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

// TestCommitFiles_GiteaTarget_UsesContentsAPI asserts that when the client is
// configured against a custom API URL (the Sovereign in-cluster Gitea target),
// CommitFiles routes through `POST /repos/{owner}/{repo}/contents` (the Gitea
// batch ChangeFiles endpoint) — NOT the Git Data write API endpoints
// (POST /git/blobs, POST /git/trees, POST /git/commits, PATCH /git/refs/...)
// which Gitea 1.22 returns 404 for.
//
// TBD-C18d (2026-05-18). The previous singular→plural fix (#1712) unblocked
// `getRef`, but Journey v4 then hit `POST /repos/.../git/blobs → 404` because
// Gitea simply does not implement the GitHub git-data WRITE API. This test
// freezes the new contents-API behavior so the regression can't sneak back.
func TestCommitFiles_GiteaTarget_UsesContentsAPI(t *testing.T) {
	type recordedRequest struct {
		Method string
		Path   string
		Body   []byte
	}

	var (
		mu       sync.Mutex
		requests []recordedRequest
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, recordedRequest{Method: r.Method, Path: r.URL.Path, Body: body})
		mu.Unlock()

		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/git/refs/heads/main"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"object":{"sha":"c3f4799deadbeef0000000000000000deadbeef"}}]`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/"):
			// File doesn't exist yet → 404 → commitOnceContents will use
			// operation=create. Mirrors Gitea behaviour for a fresh path.
			http.NotFound(w, r)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/contents"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"commit":{"sha":"newcommitsha0000000000000000000000000000"}}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewClientWithAPIURL("token", "owner", "repo", srv.URL)
	err := c.CommitFiles(context.Background(), "main", "test commit", map[string]string{
		"clusters/sovereign/tenants/alice/apps/hello.yaml": "kind: HelmRelease\nmetadata:\n  name: hello\n",
	})
	if err != nil {
		t.Fatalf("CommitFiles returned error: %v", err)
	}

	// Inspect the recorded requests.
	mu.Lock()
	defer mu.Unlock()

	var sawBatchContentsPost, sawGitBlob, sawGitTree, sawGitCommit, sawPatchRef bool
	for _, req := range requests {
		switch {
		case req.Method == http.MethodPost && strings.HasSuffix(req.Path, "/contents"):
			sawBatchContentsPost = true
			// Validate batch payload shape.
			var payload struct {
				Branch  string `json:"branch"`
				Message string `json:"message"`
				Files   []struct {
					Operation string `json:"operation"`
					Path      string `json:"path"`
					Content   string `json:"content"`
					SHA       string `json:"sha"`
				} `json:"files"`
			}
			if err := json.Unmarshal(req.Body, &payload); err != nil {
				t.Fatalf("batch payload not JSON: %v\nbody: %s", err, req.Body)
			}
			if payload.Branch != "main" {
				t.Errorf("batch payload branch = %q, want main", payload.Branch)
			}
			if payload.Message != "test commit" {
				t.Errorf("batch payload message = %q, want test commit", payload.Message)
			}
			if len(payload.Files) != 1 {
				t.Fatalf("batch payload files len = %d, want 1", len(payload.Files))
			}
			op := payload.Files[0]
			if op.Operation != "create" {
				t.Errorf("op[0].operation = %q, want create (file did not exist)", op.Operation)
			}
			if op.Path != "clusters/sovereign/tenants/alice/apps/hello.yaml" {
				t.Errorf("op[0].path = %q, want the input path", op.Path)
			}
			decoded, decErr := base64.StdEncoding.DecodeString(op.Content)
			if decErr != nil {
				t.Errorf("op[0].content is not base64: %v", decErr)
			}
			want := "kind: HelmRelease\nmetadata:\n  name: hello\n"
			if string(decoded) != want {
				t.Errorf("op[0].content decoded = %q, want %q", string(decoded), want)
			}
		case req.Method == http.MethodPost && strings.HasSuffix(req.Path, "/git/blobs"):
			sawGitBlob = true
		case req.Method == http.MethodPost && strings.HasSuffix(req.Path, "/git/trees"):
			sawGitTree = true
		case req.Method == http.MethodPost && strings.HasSuffix(req.Path, "/git/commits"):
			sawGitCommit = true
		case req.Method == http.MethodPatch && strings.Contains(req.Path, "/git/refs/heads/"):
			sawPatchRef = true
		}
	}

	if !sawBatchContentsPost {
		t.Errorf("Gitea target did NOT POST to /repos/.../contents — regression of TBD-C18d")
	}
	if sawGitBlob {
		t.Errorf("Gitea target POSTed to /git/blobs — must use Contents API instead (TBD-C18d)")
	}
	if sawGitTree {
		t.Errorf("Gitea target POSTed to /git/trees — must use Contents API instead (TBD-C18d)")
	}
	if sawGitCommit {
		t.Errorf("Gitea target POSTed to /git/commits — must use Contents API instead (TBD-C18d)")
	}
	if sawPatchRef {
		t.Errorf("Gitea target PATCHed /git/refs/heads/... — must use Contents API instead (TBD-C18d)")
	}
}

// TestCommitFiles_UpstreamTarget_KeepsGitDataAPI asserts that when no custom
// APIURL is set (the contabo / upstream-github path), the client STILL uses
// the original Git Data API (blob+tree+commit+updateRef). The Contents API
// fork only fires for Gitea targets — upstream GitHub does not implement
// the batch ChangeFiles endpoint, so we must not unconditionally switch.
func TestCommitFiles_UpstreamTarget_KeepsGitDataAPI(t *testing.T) {
	type recordedRequest struct {
		Method string
		Path   string
	}

	var (
		mu       sync.Mutex
		requests []recordedRequest
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, recordedRequest{Method: r.Method, Path: r.URL.Path})
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/git/refs/heads/main"):
			_, _ = w.Write([]byte(`[{"object":{"sha":"c3f4799deadbeef0000000000000000deadbeef"}}]`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/commits/"):
			_, _ = w.Write([]byte(`{"tree":{"sha":"treesha0000000000000000000000000000000000"}}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/blobs"):
			_, _ = w.Write([]byte(`{"sha":"blobsha0000000000000000000000000000000000"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/trees"):
			_, _ = w.Write([]byte(`{"sha":"newtreesha000000000000000000000000000000000"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/commits"):
			_, _ = w.Write([]byte(`{"sha":"newcommitsha0000000000000000000000000000"}`))
		case r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/git/refs/heads/main"):
			_, _ = w.Write([]byte(`{"ref":"refs/heads/main"}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// Set APIURL to the test server — but trick targetsGitea() into false by
	// using NewClient(...) with the upstream behaviour. Since targetsGitea
	// keys off APIURL!="" and we DO need APIURL to point at the test server,
	// this test instead asserts: "in the historical Git Data flow, all five
	// expected endpoints are hit". The targetsGitea==true path is covered by
	// TestCommitFiles_GiteaTarget_UsesContentsAPI above.
	//
	// To test the upstream path without hitting real github.com, we directly
	// call commitOnceGitData (the unconditional Git Data branch).
	c := NewClientWithAPIURL("token", "owner", "repo", srv.URL)
	err := c.commitOnceGitData(context.Background(), "main", "test commit",
		map[string]string{"foo/bar.yaml": "kind: ConfigMap\n"}, nil)
	if err != nil {
		t.Fatalf("commitOnceGitData returned error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	var sawBlob, sawTree, sawCommit, sawPatch bool
	for _, req := range requests {
		switch {
		case req.Method == http.MethodPost && strings.HasSuffix(req.Path, "/git/blobs"):
			sawBlob = true
		case req.Method == http.MethodPost && strings.HasSuffix(req.Path, "/git/trees"):
			sawTree = true
		case req.Method == http.MethodPost && strings.HasSuffix(req.Path, "/git/commits"):
			sawCommit = true
		case req.Method == http.MethodPatch && strings.Contains(req.Path, "/git/refs/heads/"):
			sawPatch = true
		}
	}
	if !sawBlob {
		t.Errorf("upstream Git Data path skipped POST /git/blobs")
	}
	if !sawTree {
		t.Errorf("upstream Git Data path skipped POST /git/trees")
	}
	if !sawCommit {
		t.Errorf("upstream Git Data path skipped POST /git/commits")
	}
	if !sawPatch {
		t.Errorf("upstream Git Data path skipped PATCH /git/refs/heads/<branch>")
	}
}

// TestCommitFiles_GiteaTarget_UpdateUsesExistingSHA asserts that when a file
// already exists on the Gitea target, the batch ChangeFiles op uses
// operation="update" + the existing blob SHA (Gitea requires the SHA on
// update; without it the API returns 422).
func TestCommitFiles_GiteaTarget_UpdateUsesExistingSHA(t *testing.T) {
	const existingSHA = "existingsha000000000000000000000000000000"

	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/git/refs/heads/main"):
			_, _ = w.Write([]byte(`[{"object":{"sha":"c3f4799deadbeef0000000000000000deadbeef"}}]`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/"):
			// Existing file → return SHA.
			_, _ = w.Write([]byte(`{"sha":"` + existingSHA + `"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/contents"):
			capturedBody, _ = io.ReadAll(r.Body)
			_, _ = w.Write([]byte(`{"commit":{"sha":"newcommitsha"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewClientWithAPIURL("token", "owner", "repo", srv.URL)
	err := c.CommitFiles(context.Background(), "main", "update existing", map[string]string{
		"clusters/sovereign/tenants/alice/apps/hello.yaml": "kind: HelmRelease\n",
	})
	if err != nil {
		t.Fatalf("CommitFiles returned error: %v", err)
	}

	var payload struct {
		Files []struct {
			Operation string `json:"operation"`
			SHA       string `json:"sha"`
		} `json:"files"`
	}
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("batch body not JSON: %v", err)
	}
	if len(payload.Files) != 1 {
		t.Fatalf("expected 1 op, got %d", len(payload.Files))
	}
	if payload.Files[0].Operation != "update" {
		t.Errorf("op.operation = %q, want update (file existed)", payload.Files[0].Operation)
	}
	if payload.Files[0].SHA != existingSHA {
		t.Errorf("op.sha = %q, want %q (existing blob SHA)", payload.Files[0].SHA, existingSHA)
	}
}
