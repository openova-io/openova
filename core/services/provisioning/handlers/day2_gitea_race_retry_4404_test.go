package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ghclient "github.com/openova-io/openova/core/services/provisioning/github"
	"github.com/openova-io/openova/core/services/provisioning/gitops"
)

// #4404 — the funnel cart dispatches the WordPress day-2 install the instant the
// Org CR is created, which can BEAT organization-controller's per-Org Gitea
// org/repo create. The branch ref read then 404s with
// "user redirect does not exist [name: <slug>]", the step-0 commit fails, and
// (because both transports share one idempotency Job) the failure poisons the
// key so the sibling dispatch is suppressed as a duplicate — the purchased app
// is lost forever.
//
// The fix is a bounded retry in runInstallJob's step-0 (commitInstallWithGiteaReadyRetry):
// while the ONLY thing wrong is that the per-Org repo doesn't exist yet, retry
// the commit; any other error fails immediately.

// fakeGiteaWithDelayedRepo serves a Gitea contents API that 404s the branch ref
// read (the not-yet-created-repo race) for the first `missesBeforeReady` GETs
// against /git/refs/heads/main, then resolves the branch and accepts the commit.
func fakeGiteaWithDelayedRepo(t *testing.T, missesBeforeReady int32) (*httptest.Server, *int32) {
	t.Helper()
	var refReads int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/catalog/plans"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"id":"s","slug":"s"}]`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/git/refs/heads/main"):
			n := atomic.AddInt32(&refReads, 1)
			if n <= missesBeforeReady {
				// The live race shape: Gitea reports the not-yet-created Org as
				// a 404 "user redirect does not exist [name: <slug>]".
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"errors":["user redirect does not exist [name: s3376walk]"],"message":"GetUserByName"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"object":{"sha":"c3f4799deadbeef0000000000000000deadbeef"}}]`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/trees/"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tree":[],"truncated":false}`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/"):
			http.NotFound(w, r)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/contents"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"commit":{"sha":"newcommitsha0000000000000000000000000000"}}`))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &refReads
}

func newHandlerForRaceTest(t *testing.T, apiURL string) *Handler {
	t.Helper()
	gen := gitops.NewManifestGenerator("clusters/sov/org-tenants")
	gen.ParentDomain = "omani.homes"
	return &Handler{
		Generator:    gen,
		GitHubClient: ghclient.NewClientWithAPIURL("token", "s3376walk", "catalyst-tenant", apiURL),
		GitBranch:    "main",
		PerOrgGitops: true,
		CatalogURL:   apiURL,
	}
}

// TestCommitInstall_RetriesWhileGiteaRepoNotReady_4404 proves the install
// recovers from the transient repo-not-ready race instead of dropping the app.
func TestCommitInstall_RetriesWhileGiteaRepoNotReady_4404(t *testing.T) {
	// Drop the backoff to near-zero so the test is fast.
	origDelay := day2GiteaReadyRetryDelay
	day2GiteaReadyRetryDelay = 1 * time.Millisecond
	t.Cleanup(func() { day2GiteaReadyRetryDelay = origDelay })

	// 404 twice (repo still being created), succeed on the third ref read.
	srv, refReads := fakeGiteaWithDelayedRepo(t, 2)
	h := newHandlerForRaceTest(t, srv.URL)

	err := h.commitInstallWithGiteaReadyRetry(context.Background(), appChangeData{
		TenantSlug: "s3376walk",
		AppSlug:    "wordpress",
		Apps:       []string{"wordpress"},
		PlanID:     "s",
	})
	if err != nil {
		t.Fatalf("commitInstallWithGiteaReadyRetry returned error after the repo became ready: %v", err)
	}
	if got := atomic.LoadInt32(refReads); got < 3 {
		t.Errorf("expected at least 3 branch-ref reads (2 race 404s + 1 success), got %d", got)
	}
}

// TestCommitInstall_ExhaustsRetriesThenFails_4404 proves a persistently-missing
// repo still fails (so a genuinely broken Org doesn't hang forever).
func TestCommitInstall_ExhaustsRetriesThenFails_4404(t *testing.T) {
	origDelay := day2GiteaReadyRetryDelay
	origAttempts := day2GiteaReadyRetryAttempts
	day2GiteaReadyRetryDelay = 1 * time.Millisecond
	day2GiteaReadyRetryAttempts = 3
	t.Cleanup(func() {
		day2GiteaReadyRetryDelay = origDelay
		day2GiteaReadyRetryAttempts = origAttempts
	})

	// Never becomes ready.
	srv, refReads := fakeGiteaWithDelayedRepo(t, 1<<30)
	h := newHandlerForRaceTest(t, srv.URL)

	err := h.commitInstallWithGiteaReadyRetry(context.Background(), appChangeData{
		TenantSlug: "s3376walk",
		AppSlug:    "wordpress",
		Apps:       []string{"wordpress"},
		PlanID:     "s",
	})
	if err == nil {
		t.Fatal("expected error when the per-Org repo never becomes ready, got nil")
	}
	if !isGiteaNotReadyError(err) {
		t.Errorf("exhausted error should still be the transient race error, got: %v", err)
	}
	// applyTenantChange may issue several ref reads per attempt; assert the
	// retry is BOUNDED — never an unbounded hammer — and that it actually
	// retried more than once before giving up.
	got := atomic.LoadInt32(refReads)
	if got < 2 {
		t.Errorf("expected the commit to be retried (>=2 ref reads), got %d", got)
	}
	if maxReads := int32(day2GiteaReadyRetryAttempts) * 4; got > maxReads {
		t.Errorf("retry not bounded: %d ref reads exceeds cap %d for %d attempts", got, maxReads, day2GiteaReadyRetryAttempts)
	}
}

// TestIsGiteaNotReadyError_4404 pins the transient-vs-permanent classification.
func TestIsGiteaNotReadyError_4404(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"user-redirect-404", errString("commit to per-Org repo s3376walk/catalyst-tenant: auto-create branch \"main\": read source branch \"main\": GitHub API GET .../git/refs/heads/main: 404 {\"errors\":[\"user redirect does not exist [name: s3376walk]\"]}"), true},
		{"plain-404", errString("read source branch \"main\": 404 Not Found"), true},
		{"permanent-manifest", errString("manifest generation produced no files"), false},
		{"permanent-tier", errString("day-2 install aborted: could not authoritatively resolve plan tier"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isGiteaNotReadyError(c.err); got != c.want {
				t.Errorf("isGiteaNotReadyError(%q) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// guard against an unused-import lint if sync is only used transitively.
var _ = sync.Mutex{}
