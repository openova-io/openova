package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestCommitFiles_GiteaTarget_RetriesOnRefLock proves the funnel fix for the
// hw158 re-validation (Refs #3376): when Gitea rejects a batch ChangeFiles
// commit to the shared `org-tenants` branch with the git-native
// `cannot lock ref 'refs/heads/org-tenants'` error (a concurrent-writer
// ref-lock race), the client must fetch-rebuild-retry rather than fail the
// whole commit. Pre-fix isGiteaRefRaceError did NOT match that string, so the
// outer retry loop never engaged and the funnel step "Committing manifests to
// Git" went straight to status:"failed".
//
// The fake Gitea server here returns the ref-lock error on the FIRST POST to
// /contents and succeeds on the SECOND, so a passing test demonstrates the
// retry actually fired and the commit ultimately landed.
func TestCommitFiles_GiteaTarget_RetriesOnRefLock(t *testing.T) {
	var (
		mu        sync.Mutex
		postCount int
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/git/refs/heads/org-tenants"):
			// Branch exists; HEAD moves between attempts in reality, but the
			// SHA value is immaterial to this test (the create op carries no
			// stale base for a fresh path).
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"object":{"sha":"c3f4799deadbeef0000000000000000deadbeef"}}]`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/"):
			// Fresh path → 404 → operation=create.
			http.NotFound(w, r)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/contents"):
			mu.Lock()
			postCount++
			n := postCount
			mu.Unlock()
			if n == 1 {
				// First writer lost the compare-and-swap on the shared
				// org-tenants branch — Gitea's git backend surfaces the
				// ref-lock failure. 409 Conflict + the git-native body.
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"message":"cannot lock ref 'refs/heads/org-tenants': is at 1111111111111111111111111111111111111111 but expected 2222222222222222222222222222222222222222"}`))
				return
			}
			// Retry against the moved HEAD succeeds.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"commit":{"sha":"newcommitsha0000000000000000000000000000"}}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewClientWithAPIURL("token", "openova-io", "openova", srv.URL)
	err := c.CommitFiles(context.Background(), "org-tenants", "org-tenant: provision alice", map[string]string{
		"clusters/otech.omani.works/org-tenants/alice/namespace.yaml": "kind: Namespace\nmetadata:\n  name: org-alice\n",
	})
	if err != nil {
		t.Fatalf("CommitFiles should have retried past the ref-lock and succeeded, got: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if postCount != 2 {
		t.Fatalf("expected exactly 2 POST /contents (1 ref-lock reject + 1 success), got %d — retry did not fire as expected", postCount)
	}
}

// shrinkCommitRetryDelays drops the ref-race backoff bounds to near-zero for
// the duration of a test so exhaustion-path tests (#5234: 10 attempts, 4s
// cap) complete in milliseconds. Restores the production values on cleanup.
func shrinkCommitRetryDelays(t *testing.T) {
	t.Helper()
	origBase, origMax := commitRetryBaseDelay, commitRetryMaxDelay
	commitRetryBaseDelay = 1 * time.Millisecond
	commitRetryMaxDelay = 5 * time.Millisecond
	t.Cleanup(func() {
		commitRetryBaseDelay, commitRetryMaxDelay = origBase, origMax
	})
}

// TestCommitFiles_GiteaTarget_RefLockPersists asserts that when the ref-lock
// race NEVER clears, the client exhausts commitAttemptsMax and returns a
// descriptive ref-race error (rather than hanging or masking the failure).
// This guards the bound on the retry so a pathological hot branch can't spin
// forever.
func TestCommitFiles_GiteaTarget_RefLockPersists(t *testing.T) {
	shrinkCommitRetryDelays(t)
	var (
		mu        sync.Mutex
		postCount int
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/git/refs/heads/org-tenants"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"object":{"sha":"c3f4799deadbeef0000000000000000deadbeef"}}]`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/"):
			http.NotFound(w, r)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/contents"):
			mu.Lock()
			postCount++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"message":"cannot lock ref 'refs/heads/org-tenants'"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewClientWithAPIURL("token", "openova-io", "openova", srv.URL)
	err := c.CommitFiles(context.Background(), "org-tenants", "org-tenant: provision bob", map[string]string{
		"clusters/otech.omani.works/org-tenants/bob/namespace.yaml": "kind: Namespace\n",
	})
	if err == nil {
		t.Fatalf("expected a ref-race error after exhausting retries, got nil")
	}
	if !strings.Contains(err.Error(), "ref-race persisted") {
		t.Errorf("error should report ref-race exhaustion, got: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if postCount != commitAttemptsMax {
		t.Errorf("expected exactly %d POST attempts (commitAttemptsMax), got %d", commitAttemptsMax, postCount)
	}
}

// TestCommitFiles_GiteaTarget_RefLockRetryHonoursContext asserts the
// jittered backoff between ref-race retries aborts promptly when the request
// context is cancelled, rather than sleeping out the full backoff.
func TestCommitFiles_GiteaTarget_RefLockRetryHonoursContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/git/refs/heads/org-tenants"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"object":{"sha":"c3f4799deadbeef0000000000000000deadbeef"}}]`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/"):
			http.NotFound(w, r)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/contents"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"message":"cannot lock ref 'refs/heads/org-tenants'"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel shortly after the first failure so the abort lands during the
	// backoff sleep, not the HTTP call.
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	c := NewClientWithAPIURL("token", "openova-io", "openova", srv.URL)
	start := time.Now()
	err := c.CommitFiles(ctx, "org-tenants", "org-tenant: provision carol", map[string]string{
		"clusters/otech.omani.works/org-tenants/carol/namespace.yaml": "kind: Namespace\n",
	})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("expected an error after context cancellation, got nil")
	}
	// Must bail well before all commitAttemptsMax backoffs would elapse.
	if elapsed > 2*time.Second {
		t.Errorf("retry loop did not honour context cancellation promptly (took %s)", elapsed)
	}
}

// TestIsGiteaRefRaceError_RefLockShapes locks in that every git-native
// ref-lock wording Gitea can emit on a contended branch is recognised as a
// retryable ref-race (Refs #3376), while genuinely fatal errors are not.
func TestIsGiteaRefRaceError_RefLockShapes(t *testing.T) {
	retryable := []string{
		"GitHub API POST .../contents: 409 {\"message\":\"cannot lock ref 'refs/heads/org-tenants'\"}",
		"change files: cannot lock ref 'refs/heads/org-tenants': is at aaaa but expected bbbb",
		"unable to create '.../refs/heads/org-tenants.lock': File exists",
		"failed to update ref refs/heads/org-tenants",
		// pre-existing shapes must still match
		"GitHub API POST x: 409 Conflict",
		"the branch has been changed",
		"stale base",
		"ref has been updated by another request",
		"Update is not a fast forward",
		// #5234 — file-level CAS losses from the ChangeFiles per-file SHA
		// preconditions. Gitea returns these as 422 (no " 409 " marker), so
		// they MUST match on wording: a concurrent writer created the same
		// path first (create raced) or changed the file after our probe
		// (stale update SHA). Both resolve on a fresh re-probe + retry.
		`GitHub API POST .../contents: 422 {"message":"repository file already exists [path: vcluster/apps/kustomization.yaml]"}`,
		`GitHub API POST .../contents: 422 {"message":"sha does not match [given: aaaa, expected: bbbb]"}`,
	}
	for _, s := range retryable {
		if !isGiteaRefRaceError(simpleErr(s)) {
			t.Errorf("isGiteaRefRaceError(%q) = false, want true (ref-race should retry)", s)
		}
	}

	fatal := []string{
		"GitHub API POST .../contents: 403 {\"message\":\"token lacks write scope\"}",
		"GitHub API POST .../contents: 422 {\"message\":\"content is not valid base64\"}",
		"context deadline exceeded",
		"dial tcp: connection refused",
		"",
	}
	for _, s := range fatal {
		if isGiteaRefRaceError(simpleErr(s)) {
			t.Errorf("isGiteaRefRaceError(%q) = true, want false (must not retry a fatal error)", s)
		}
	}
}

// TestCommitRetryBackoff_Bounds asserts the backoff helper stays within its
// declared [base, max] envelope and never precedes the first attempt.
func TestCommitRetryBackoff_Bounds(t *testing.T) {
	if d := commitRetryBackoff(1); d != 0 {
		t.Errorf("commitRetryBackoff(1) = %s, want 0 (no backoff before the first attempt)", d)
	}
	for attempt := 2; attempt <= 12; attempt++ {
		for i := 0; i < 64; i++ { // sample the jitter
			d := commitRetryBackoff(attempt)
			if d < commitRetryBaseDelay {
				t.Fatalf("commitRetryBackoff(%d) = %s, below base %s", attempt, d, commitRetryBaseDelay)
			}
			if d > commitRetryMaxDelay {
				t.Fatalf("commitRetryBackoff(%d) = %s, above max %s", attempt, d, commitRetryMaxDelay)
			}
		}
	}
}

// simpleErr is a tiny error wrapper so the table tests above can build errors
// from raw strings without pulling in errors.New everywhere.
type simpleErr string

func (e simpleErr) Error() string { return string(e) }
