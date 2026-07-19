package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// #5234 (hw274) — the funnel's purchased-app commit to the per-Org
// `<slug>/catalyst-tenant` repo raced the org-controller's per-file PutFile
// commit bursts and reported "ref-race persisted after 5 attempts": the
// purchased WordPress never landed in the Org boundary. The durable fix makes
// every retry a genuine compare-and-swap: re-read the remote head + per-file
// SHAs on EVERY attempt, push with those fresh SHA preconditions, classify
// the file-level CAS-loss rejections (422 "sha does not match" / "repository
// file already exists") as retryable, and widen the attempt budget + jittered
// backoff so the loser's window straddles a whole controller burst.
//
// The fake Gitea below simulates the concurrent writer PRECISELY: it advances
// the tracked file's SHA right after serving each probe (the concurrent
// writer lands between our read and our push), and accepts a batch ONLY when
// the batch's update op carries the CURRENT SHA. A client that reused a stale
// base would re-send the first SHA forever and exhaust; the CAS client
// re-probes per attempt and lands on the first un-raced window.

// casFakeGitea is a minimal contents-API server tracking one file whose SHA a
// simulated concurrent writer keeps advancing.
type casFakeGitea struct {
	t *testing.T

	mu sync.Mutex
	// fileSHA is the CURRENT blob SHA of the tracked file.
	fileSHA int
	// advanceRemaining is how many more times the concurrent writer advances
	// the file right after our probe reads it (-1 = advance forever, the
	// permanently-hot-branch case).
	advanceRemaining int
	postCount        int
	// lastAcceptedSHA records the sha the winning batch carried.
	lastAcceptedSHA string
}

const casTrackedFile = "vcluster/apps/kustomization.yaml"

func (f *casFakeGitea) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/git/refs/heads/main"):
			f.mu.Lock()
			head := fmt.Sprintf("headsha-%040d", f.fileSHA)
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `[{"object":{"sha":"%s"}}]`, head)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/contents/"+casTrackedFile):
			// Per-file SHA probe (getContentSHA). Serve the CURRENT SHA, then
			// let the concurrent writer win the window between this read and
			// the upcoming POST.
			f.mu.Lock()
			sha := fmt.Sprintf("filesha-%d", f.fileSHA)
			if f.advanceRemaining != 0 {
				f.fileSHA++
				if f.advanceRemaining > 0 {
					f.advanceRemaining--
				}
			}
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"sha":"%s","content":"","encoding":"base64"}`, sha)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/contents"):
			body, _ := io.ReadAll(r.Body)
			var payload struct {
				Files []struct {
					Operation string `json:"operation"`
					Path      string `json:"path"`
					SHA       string `json:"sha"`
				} `json:"files"`
			}
			if err := json.Unmarshal(body, &payload); err != nil {
				f.t.Errorf("batch body not JSON: %v", err)
				http.Error(w, "bad body", http.StatusBadRequest)
				return
			}
			var got string
			for _, file := range payload.Files {
				if file.Path == casTrackedFile {
					got = file.SHA
				}
			}
			f.mu.Lock()
			f.postCount++
			want := fmt.Sprintf("filesha-%d", f.fileSHA)
			stale := got != want
			if !stale {
				f.lastAcceptedSHA = got
			}
			f.mu.Unlock()
			if stale {
				// Gitea's file-level CAS rejection: 422 + the git-native
				// wording (NOT a 409, so only the #5234 classifier shape
				// makes the retry loop engage).
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnprocessableEntity)
				fmt.Fprintf(w, `{"message":"sha does not match [given: %s, expected: %s]"}`, got, want)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"commit":{"sha":"newcommitsha00000000000000000000000000"}}`))
		default:
			f.t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}
}

// TestCommitFiles_GiteaTarget_RebasesOntoAdvancedHead_5234 drives the exact
// hw274 race: a concurrent writer advances the tracked file between the
// client's probe and its push, twice. The CAS retry must re-probe the moved
// head on every attempt and land once the writer stops — with the CURRENT
// per-file SHA, proving the push was rebuilt against the new head rather than
// replaying the first attempt's stale base.
func TestCommitFiles_GiteaTarget_RebasesOntoAdvancedHead_5234(t *testing.T) {
	fake := &casFakeGitea{t: t, fileSHA: 1, advanceRemaining: 2}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	c := NewClientWithAPIURL("token", "acme274", "catalyst-tenant", srv.URL)
	err := c.CommitFiles(context.Background(), "main", "day-2: install wordpress", map[string]string{
		casTrackedFile: "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - app-wordpress.yaml\n",
	})
	if err != nil {
		t.Fatalf("CommitFiles should have re-probed the advanced head and succeeded, got: %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.postCount != 3 {
		t.Errorf("expected 3 POSTs (2 CAS losses + 1 win), got %d", fake.postCount)
	}
	want := fmt.Sprintf("filesha-%d", fake.fileSHA)
	if fake.lastAcceptedSHA != want {
		t.Errorf("winning batch carried sha %q, want the CURRENT head's %q — the retry did not rebuild against the advanced head", fake.lastAcceptedSHA, want)
	}
}

// TestCommitFiles_GiteaTarget_PermanentlyAdvancingRefExhausts_5234 asserts the
// bounded terminal outcome: when the concurrent writer NEVER stops advancing
// the file, the client burns exactly commitAttemptsMax attempts and returns
// the descriptive ref-race exhaustion error — a hard failure the caller MUST
// surface (red step / failed Job), never a silent success.
func TestCommitFiles_GiteaTarget_PermanentlyAdvancingRefExhausts_5234(t *testing.T) {
	shrinkCommitRetryDelays(t)
	fake := &casFakeGitea{t: t, fileSHA: 1, advanceRemaining: -1}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	c := NewClientWithAPIURL("token", "acme274", "catalyst-tenant", srv.URL)
	err := c.CommitFiles(context.Background(), "main", "day-2: install wordpress", map[string]string{
		casTrackedFile: "resources: []\n",
	})
	if err == nil {
		t.Fatalf("expected a terminal ref-race error on a permanently-advancing head, got nil (silent success)")
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("ref-race persisted after %d attempts", commitAttemptsMax)) {
		t.Errorf("error should report exhaustion after %d attempts, got: %v", commitAttemptsMax, err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.postCount != commitAttemptsMax {
		t.Errorf("expected exactly %d POST attempts (commitAttemptsMax), got %d", commitAttemptsMax, fake.postCount)
	}
}

// TestCommitAttemptsBudget_5234 pins the widened retry envelope: 10 attempts
// against the org-controller's PutFile bursts (was 5 — exhausted inside a
// single burst on hw274) and a 4s backoff cap so the jittered window
// straddles a whole burst instead of fitting inside one.
func TestCommitAttemptsBudget_5234(t *testing.T) {
	if commitAttemptsMax < 10 {
		t.Errorf("commitAttemptsMax = %d, want >= 10 (#5234 — 5 exhausted inside one org-controller commit burst)", commitAttemptsMax)
	}
	if commitRetryMaxDelay.Milliseconds() < 2000 {
		t.Errorf("commitRetryMaxDelay = %s, want >= 2s (#5234 — 750ms window fit inside a single burst)", commitRetryMaxDelay)
	}
}
