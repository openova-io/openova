package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// #4384 — the EMPTY-SHA tree 404. On the Gitea path commitOnceContents listed
// the existing tree to compute prune/update SHAs; getCommitTree
// (GET /git/commits/{sha}) returned an EMPTY tree SHA on Gitea, so the listing
// built `GET /git/trees/?recursive=1` (no SHA segment) → 404, the live failure.
//
// The fix: on a Gitea target resolveTreeish passes the COMMIT SHA straight
// through to GET /git/trees/{sha} (Gitea resolves commit→tree itself) and never
// emits the SHA-less URL. This test asserts a Gitea-target prune commit lists
// the tree at /git/trees/<commit-sha> (NOT /git/trees/?recursive=1) and never
// reads /git/commits/.
func TestGiteaPrune_NoEmptySHATreePath_4384(t *testing.T) {
	const commitSHA = "c3f4799deadbeef0000000000000000deadbeef"
	var (
		mu            sync.Mutex
		treePath      string
		sawEmpty      bool
		sawCommitRead bool
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/git/refs/heads/main"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"object":{"sha":"` + commitSHA + `"}}]`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/commits/"):
			sawCommitRead = true
			// Gitea's broken-for-us shape: no usable tree SHA.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/trees/"):
			treePath = r.URL.Path
			if strings.HasSuffix(r.URL.Path, "/git/trees/") {
				sawEmpty = true
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tree":[],"truncated":false}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/contents"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"commit":{"sha":"newcommit000000000000000000000000000000"}}`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/"):
			http.NotFound(w, r)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	c := NewClientWithAPIURL("token", "g6wpwalk", "catalyst-tenant", srv.URL)
	// Commit WITH a prune prefix → forces the tree-listing path that 404'd.
	err := c.CommitFilesWithPrune(context.Background(), "main", "cart install",
		map[string]string{"vcluster/apps/app-wordpress.yaml": "kind: HelmRelease\n"},
		[]string{"vcluster/apps/app-", "vcluster/apps/db-"})
	if err != nil {
		t.Fatalf("CommitFilesWithPrune returned error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if sawEmpty {
		t.Errorf("regression: hit the empty-SHA tree path /git/trees/ (the live 404)")
	}
	if sawCommitRead {
		t.Errorf("Gitea path must NOT call GET /git/commits/ (it returns an empty tree SHA → empty-SHA 404); resolveTreeish passes the commit SHA through")
	}
	if !strings.Contains(treePath, commitSHA) {
		t.Errorf("tree listing must use the commit SHA directly on Gitea, got path %q", treePath)
	}
}
