package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"strings"
	"time"
)

// ErrFileNotFound is returned by ReadFile when the contents API answers
// 404 (the path genuinely does not exist on the branch). Callers MUST
// distinguish this from a transient read failure (network error, 5xx,
// decode failure): a 404 means "no such file yet" and an empty fallback
// is safe, whereas a transient error means "we don't KNOW the file's
// content" — fabricating an empty fallback there is the #4250 bug, because
// the parent org-tenants kustomization.yaml is rebuilt via read-modify-
// write and a phantom-empty read collapses it to a single-Org list, which
// Flux's prune=true Kustomization then garbage-collects every SIBLING
// Org's namespace from. Test/wrap with errors.Is(err, ErrFileNotFound).
var ErrFileNotFound = errors.New("file not found")

// Client provides methods to commit files to a GitHub repository via the
// Git Data API. The API base URL is configurable so the client can target
// either upstream github.com (the contabo path) or a local Gitea REST API
// running inside a Sovereign cluster (the issue #940 fix path).
//
// Gitea exposes a GitHub-compatible Git Data API at /api/v1 (the same
// shape this client already uses). The only difference is the base URL —
// `https://api.github.com` vs e.g.
// `http://gitea-http.gitea.svc.cluster.local:3000/api/v1`. apiURL on the
// client struct lets a caller override the prefix without re-implementing
// the whole client.
type Client struct {
	Token  string
	Owner  string
	Repo   string
	APIURL string // optional — defaults to https://api.github.com when empty.
}

// NewClient creates a GitHub API client targeting upstream github.com.
// Use NewClientWithAPIURL when targeting a Gitea-compatible REST API on
// a Sovereign cluster.
func NewClient(token, owner, repo string) *Client {
	return &Client{Token: token, Owner: owner, Repo: repo}
}

// NewClientWithAPIURL creates a Git Data API client with a custom base
// URL — used to target an in-cluster Gitea on Sovereigns (issue #940).
// Pass apiURL="" or apiURL="https://api.github.com" to fall back to the
// canonical upstream endpoint.
func NewClientWithAPIURL(token, owner, repo, apiURL string) *Client {
	return &Client{Token: token, Owner: owner, Repo: repo, APIURL: strings.TrimRight(apiURL, "/")}
}

// WithRepo returns a shallow copy of the client retargeted at a different
// owner/repo on the SAME API host + token. Used by the funnel/day-2 cart
// install (#4384) to commit the customer's purchased Applications into the
// per-Org `<slug>/catalyst-tenant` repo the org-controller bootstrapped —
// instead of the globally-configured catalog repo (`openova/openova`), which
// is the WRONG target for a Sovereign's per-Org apps (and 404s on the
// empty-SHA tree path). The receiver is untouched, so the global client keeps
// serving the legacy contabo per-tenant overlays.
func (c *Client) WithRepo(owner, repo string) *Client {
	clone := *c
	clone.Owner = owner
	clone.Repo = repo
	return &clone
}

// commitAttemptsMax is how many times CommitFilesWithPrune retries a full
// rebuild (getRef → tree → commit → updateRef) when updateRef returns
// "Update is not a fast forward". Concurrent day-2 installs race at the
// branch-ref level; a clean rebuild against the new HEAD succeeds on the
// next try.
//
// #5234 (hw274): 5 attempts was sized for a burst of ~5 parallel one-shot
// commits — but the org-controller's per-Org reconcile advances the SAME
// `<slug>/catalyst-tenant@main` head with a BURST of sequential single-file
// PutFile commits (~10+ per reconcile, re-fired on every requeue while the
// funnel Org is converging). The funnel's cart-install batch commit kept
// landing mid-burst and lost the compare-and-swap 5/5 times inside the old
// ~1.5s retry window ("ref-race persisted after 5 attempts" → the purchased
// app never deployed). 10 attempts paired with the widened backoff below
// gives the loser a multi-second window that straddles a whole burst.
const commitAttemptsMax = 10

// Backoff bounds for the ref-race retry. Without a delay every concurrent
// writer that lost the compare-and-swap retries simultaneously and re-loses
// against the SAME winner — a thundering herd that can burn all
// commitAttemptsMax attempts in milliseconds on the shared `org-tenants`
// branch (Refs #3376). A jittered exponential backoff staggers the racers so
// each gets a clear shot at the moved HEAD.
//
// #5234: the cap was 750ms, sized for one-shot racers. Against the
// org-controller's sequential PutFile bursts the whole 4-backoff window
// (~1.5s worst case) fit INSIDE a single burst, so every attempt re-lost.
// The 4s cap stretches the worst-case total window to ~18s across 9
// backoffs (expected ~9s with full jitter) — longer than any observed
// reconcile burst, while staying well inside the day-2 consumer's budget.
// Vars (not consts) so tests can shrink the delays to keep the
// exhaustion-path tests fast.
var (
	commitRetryBaseDelay = 50 * time.Millisecond
	commitRetryMaxDelay  = 4 * time.Second
)

// commitRetryBackoff returns the jittered backoff to wait before the given
// 1-based retry attempt. attempt==1 is the first try (no preceding backoff);
// the returned duration applies BEFORE attempt N (N>=2). Exponential in the
// attempt index, capped at commitRetryMaxDelay, with full jitter in
// [base, computed] so concurrent writers de-correlate.
func commitRetryBackoff(attempt int) time.Duration {
	if attempt < 2 {
		return 0
	}
	// 2^(attempt-2) * base: attempt 2 → 1x, attempt 3 → 2x, attempt 4 → 4x …
	mult := time.Duration(1) << uint(attempt-2)
	d := commitRetryBaseDelay * mult
	if d > commitRetryMaxDelay {
		d = commitRetryMaxDelay
	}
	// Full jitter in [base, d] — never below base so we always yield the
	// goroutine, never above the capped ceiling.
	if d <= commitRetryBaseDelay {
		return commitRetryBaseDelay
	}
	jitterSpan := int64(d - commitRetryBaseDelay)
	return commitRetryBaseDelay + time.Duration(rand.Int63n(jitterSpan+1))
}

// sleepWithContext waits for d or until ctx is cancelled, whichever comes
// first. Returns ctx.Err() if the context fired during the wait so the
// caller can abort the retry loop promptly instead of blocking on a timer.
func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// CommitFiles creates an atomic commit with multiple files on the given branch.
// files maps path (e.g. "clusters/contabo-mkt/tenants/slug/namespace.yaml") to content.
func (c *Client) CommitFiles(ctx context.Context, branch, message string, files map[string]string) error {
	return c.CommitFilesWithPrune(ctx, branch, message, files, nil)
}

// CommitFilesWithPrune is like CommitFiles but deletes any blobs under the given
// prefixes in the existing tree that are NOT present in files. This turns the
// commit into a mirror operation for the managed prefixes — callers use it to
// ensure uninstalled apps no longer have orphan app-*.yaml files hanging around.
//
// prunePrefixes must not be empty strings; each must end with "/" (e.g.,
// "clusters/contabo-mkt/tenants/haty/apps/"). Paths outside the prefixes are
// preserved via base_tree as before.
//
// On ref-race retry, the SAME files map is committed against the new HEAD —
// safe when the file content is independent of HEAD (e.g. tenant manifests
// scoped to a fresh tenant subdirectory). NOT safe when the content is a
// read-modify-write of an existing file (e.g. parent kustomization.yaml that
// needs to merge in a slug). For that case use CommitFilesWithPruneAndRebuild.
func (c *Client) CommitFilesWithPrune(ctx context.Context, branch, message string, files map[string]string, prunePrefixes []string) error {
	return c.CommitFilesWithPruneAndRebuild(ctx, branch, message, files, prunePrefixes, nil)
}

// CommitFilesWithPruneAndRebuild is like CommitFilesWithPrune but lets the
// caller participate in the ref-race retry. If non-nil, `rebuild` is called
// at the START of each attempt (including the first) to (re)compute the file
// map. This gives the caller a chance to read the latest HEAD content of any
// merge-target file (e.g. parent kustomization.yaml) and re-apply the merge
// against the post-race state, preventing stale read-modify-write resurrection.
//
// Issue #1031 — observed live 2026-05-06: tenant teardown of "bookcheck"
// committed at T, then a multitest provision committed at T+5s. Multitest's
// first commit attempt got the ref-race rejection because teardown landed
// first; the retry re-used the SAME files map (containing a stale parent
// kustomization that still listed "bookcheck"), wedging Flux's `tenants`
// Kustomization in a build-failure loop because the bookcheck/ directory
// was already gone but the slug was back in resources:.
func (c *Client) CommitFilesWithPruneAndRebuild(
	ctx context.Context,
	branch, message string,
	files map[string]string,
	prunePrefixes []string,
	rebuild func(ctx context.Context) (map[string]string, error),
) error {
	if rebuild == nil && len(files) == 0 && len(prunePrefixes) == 0 {
		return fmt.Errorf("no files to commit")
	}

	var lastErr error
	for attempt := 1; attempt <= commitAttemptsMax; attempt++ {
		// Each attempt starts from a fresh files map when the caller has
		// supplied a rebuilder. Without one, the static files map flows
		// through unchanged — preserving the old behaviour.
		attemptFiles := files
		if rebuild != nil {
			fresh, rebuildErr := rebuild(ctx)
			if rebuildErr != nil {
				return fmt.Errorf("rebuild files for commit: %w", rebuildErr)
			}
			attemptFiles = fresh
			if len(attemptFiles) == 0 && len(prunePrefixes) == 0 {
				return fmt.Errorf("rebuild returned no files to commit")
			}
		}
		err := c.commitOnce(ctx, branch, message, attemptFiles, prunePrefixes)
		if err == nil {
			return nil
		}
		lastErr = err
		// Retry on ref-race: updateRef failed because the branch moved between
		// our getRef and our updateRef. Rebuild the whole tree against the new
		// HEAD on the next attempt. Everything else is fatal.
		if !isFastForwardRejection(err) {
			return err
		}
		slog.Warn("commit: ref moved under us — retrying with fresh HEAD",
			"attempt", attempt, "branch", branch, "error", err)
		// Jittered backoff before the next attempt so concurrent writers on
		// the shared branch de-correlate instead of re-colliding immediately
		// (Refs #3376). Skipped after the final attempt. Honours ctx so a
		// cancelled request aborts promptly rather than sleeping.
		if attempt < commitAttemptsMax {
			if waitErr := sleepWithContext(ctx, commitRetryBackoff(attempt+1)); waitErr != nil {
				return fmt.Errorf("commit: ref-race retry aborted: %w (last commit error: %v)", waitErr, lastErr)
			}
		}
	}
	return fmt.Errorf("commit: ref-race persisted after %d attempts: %w", commitAttemptsMax, lastErr)
}

// isFastForwardRejection returns true iff the wrapped error is GitHub's
// "Update is not a fast forward" 422 from the update-a-reference endpoint,
// OR the Gitea-equivalent shape returned by commitOnceContents. Keeping
// the check on the textual payload is intentional — the REST API's
// response body is the canonical signal here, not a distinct HTTP status.
func isFastForwardRejection(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return (contains(s, "not a fast forward") || contains(s, "Reference cannot be updated")) &&
		contains(s, "update ref")
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	// naive; strings.Contains would work but avoids importing strings twice.
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// targetsGitea returns true iff this client is configured against a non-empty
// custom APIURL (the Sovereign Git target = in-cluster Gitea). The empty case
// targets upstream api.github.com (the contabo path).
//
// TBD-C18d (2026-05-18): Gitea 1.22 does NOT implement the GitHub Git Data
// write API (`POST /git/blobs`, `POST /git/trees`, `POST /git/commits`,
// `PATCH /git/refs/heads/<branch>`). All four return 404. Gitea DOES expose
// a GitHub-compatible Repository Contents API (`POST/PUT/DELETE
// /repos/{owner}/{repo}/contents/{filepath}`) plus a batch ChangeFiles
// endpoint (`POST /repos/{owner}/{repo}/contents`, Gitea ≥ 1.21) that does
// the same multi-file atomic commit semantics we need.
//
// Upstream api.github.com retains the Git Data write API (and does NOT
// expose a batch ChangeFiles endpoint), so we keep the original
// blob+tree+commit+updateRef dance for that path.
func (c *Client) targetsGitea() bool {
	return c.APIURL != ""
}

// commitOnce runs a single atomic multi-file commit on `branch`. On Gitea
// targets (Sovereign clusters, TBD-C18d) it uses the batch ChangeFiles
// contents API; on upstream GitHub it uses the Git Data API blob+tree+
// commit+updateRef dance.
func (c *Client) commitOnce(ctx context.Context, branch, message string, files map[string]string, prunePrefixes []string) error {
	if c.targetsGitea() {
		return c.commitOnceContents(ctx, branch, message, files, prunePrefixes)
	}
	return c.commitOnceGitData(ctx, branch, message, files, prunePrefixes)
}

// commitOnceGitData runs a single getRef → tree → commit → updateRef cycle
// against the GitHub Git Data API. Kept for the contabo / upstream-GitHub
// path; Gitea callers go through commitOnceContents instead.
func (c *Client) commitOnceGitData(ctx context.Context, branch, message string, files map[string]string, prunePrefixes []string) error {
	// 1. Get the current branch ref SHA.
	refSHA, err := c.getRef(ctx, branch)
	if err != nil {
		return fmt.Errorf("get ref: %w", err)
	}
	slog.Debug("got branch ref", "branch", branch, "sha", refSHA)

	// 2. Get the commit's tree SHA.
	treeSHA, err := c.getCommitTree(ctx, refSHA)
	if err != nil {
		return fmt.Errorf("get commit tree: %w", err)
	}

	// 3. Create blobs for each file and build tree entries.
	var treeEntries []treeEntry
	for path, content := range files {
		blobSHA, blobErr := c.createBlob(ctx, content)
		if blobErr != nil {
			return fmt.Errorf("create blob for %s: %w", path, blobErr)
		}
		treeEntries = append(treeEntries, treeEntry{
			Path: path,
			Mode: "100644",
			Type: "blob",
			SHA:  blobSHA,
		})
	}

	// 3b. Walk the existing tree for each prune prefix and mark orphan paths
	// for deletion. A path is orphan when it's under a managed prefix but not
	// in `files`. Delete entries use sha=null (encoded as omitempty null).
	if len(prunePrefixes) > 0 {
		existing, listErr := c.listTreeBlobs(ctx, refSHA, prunePrefixes)
		if listErr != nil {
			return fmt.Errorf("list existing tree: %w", listErr)
		}
		for _, path := range existing {
			if _, keep := files[path]; keep {
				continue
			}
			treeEntries = append(treeEntries, treeEntry{
				Path:   path,
				Mode:   "100644",
				Type:   "blob",
				Delete: true,
			})
		}
	}

	// 4. Create a new tree.
	newTreeSHA, err := c.createTree(ctx, treeSHA, treeEntries)
	if err != nil {
		return fmt.Errorf("create tree: %w", err)
	}

	// 5. Create the commit.
	commitSHA, err := c.createCommit(ctx, message, newTreeSHA, refSHA)
	if err != nil {
		return fmt.Errorf("create commit: %w", err)
	}

	// 6. Update the branch ref.
	if err := c.updateRef(ctx, branch, commitSHA); err != nil {
		return fmt.Errorf("update ref: %w", err)
	}

	slog.Info("committed files to GitHub",
		"branch", branch,
		"commit", commitSHA,
		"files", len(files),
		"prune_prefixes", len(prunePrefixes),
	)
	return nil
}

// commitOnceContents runs a single atomic multi-file commit against the
// Gitea-compatible Contents API (TBD-C18d, 2026-05-18). It mirrors the
// public contract of commitOnceGitData (same retry semantics, same prune
// behaviour, same ref-race detection via isFastForwardRejection) but talks
// to endpoints Gitea 1.22 actually implements:
//
//   - POST /repos/{owner}/{repo}/contents  (batch ChangeFiles) — atomic
//     multi-file create/update/delete in one commit.
//
// File SHAs needed for update/delete operations are sourced from the
// recursive git-tree listing (GET /git/trees/{sha}?recursive=1), which IS
// supported by Gitea (only the WRITE side of the Git Data API is missing).
func (c *Client) commitOnceContents(ctx context.Context, branch, message string, files map[string]string, prunePrefixes []string) error {
	// 1. Get current branch ref SHA — used for prune listing AND for an
	// optional concurrency probe before the atomic batch commit.
	//
	// TBD-C18e (2026-05-18): when the target branch doesn't exist yet
	// (first tenant commit after switching from `main` to a dedicated
	// `org-tenants` branch), Gitea returns 404 here. Auto-create the
	// branch from the repo's default branch (typically `main`) and
	// retry the ref lookup so the rest of the commit flow can proceed.
	// This makes the customer-journey fix self-bootstrapping — no
	// out-of-band branch seeding required.
	refSHA, err := c.getRef(ctx, branch)
	if err != nil {
		if isBranchMissingError(err) {
			if createErr := c.ensureBranchExists(ctx, branch); createErr != nil {
				return fmt.Errorf("auto-create branch %q: %w", branch, createErr)
			}
			// Retry the ref lookup — the branch should now exist.
			refSHA, err = c.getRef(ctx, branch)
			if err != nil {
				return fmt.Errorf("get ref after auto-create: %w", err)
			}
			slog.Info("auto-created branch for tenant commits", "branch", branch)
		} else {
			return fmt.Errorf("get ref: %w", err)
		}
	}
	slog.Debug("got branch ref", "branch", branch, "sha", refSHA)

	// 2. List existing blobs (with their SHAs) under managed prefixes so
	// we can compute update-vs-create AND prune deletions.
	existing := map[string]string{} // path → blob SHA
	if len(files) > 0 || len(prunePrefixes) > 0 {
		// We always need existing SHAs for update operations on files that
		// already exist. Use the same recursive tree listing path as the
		// Git Data path. Collect ALL blobs whose path is either a managed
		// prefix candidate OR an exact match in `files`.
		needs := prunePrefixes
		// For ANY files that are NOT under a prune prefix we still need
		// existing-SHA lookup when the path exists; do an exact-path probe.
		var perFileProbe []string
		for path := range files {
			covered := false
			for _, pfx := range prunePrefixes {
				if strings.HasPrefix(path, pfx) {
					covered = true
					break
				}
			}
			if !covered {
				perFileProbe = append(perFileProbe, path)
			}
		}
		if len(needs) > 0 {
			existing, err = c.listTreeBlobsWithSHA(ctx, refSHA, needs)
			if err != nil {
				return fmt.Errorf("list existing tree: %w", err)
			}
		}
		// For files outside prune prefixes, fetch each one's SHA on-demand
		// (HEAD-style GET /contents/{path}?ref=). 404 means "create".
		for _, path := range perFileProbe {
			sha, lookupErr := c.getContentSHA(ctx, branch, path)
			if lookupErr != nil {
				return fmt.Errorf("probe %s: %w", path, lookupErr)
			}
			if sha != "" {
				existing[path] = sha
			}
		}
	}

	// 3. Build the batch ChangeFiles operations list.
	type changeFile struct {
		Operation string `json:"operation"`
		Path      string `json:"path"`
		Content   string `json:"content,omitempty"` // base64 for create/update
		SHA       string `json:"sha,omitempty"`     // required for update/delete
	}
	var ops []changeFile
	for path, content := range files {
		op := changeFile{
			Path:    path,
			Content: base64.StdEncoding.EncodeToString([]byte(content)),
		}
		if sha, ok := existing[path]; ok {
			op.Operation = "update"
			op.SHA = sha
		} else {
			op.Operation = "create"
		}
		ops = append(ops, op)
	}
	// Prune: any blob under a managed prefix that is not in `files` gets
	// a delete op. existing only contains paths under prunePrefixes (plus
	// the per-file probes), so this loop is well-bounded.
	for path, sha := range existing {
		if _, keep := files[path]; keep {
			continue
		}
		underPrune := false
		for _, pfx := range prunePrefixes {
			if strings.HasPrefix(path, pfx) {
				underPrune = true
				break
			}
		}
		if !underPrune {
			continue
		}
		ops = append(ops, changeFile{
			Operation: "delete",
			Path:      path,
			SHA:       sha,
		})
	}

	if len(ops) == 0 {
		// Nothing to commit (no creates, no updates, no deletes).
		slog.Info("commitOnceContents: no-op (nothing to write or prune)",
			"branch", branch, "prune_prefixes", len(prunePrefixes))
		return nil
	}

	// 4. POST the batch. Gitea ≥ 1.21 endpoint.
	payload := map[string]any{
		"branch":  branch,
		"message": message,
		"files":   ops,
	}
	_, err = c.doRequest(ctx, http.MethodPost, c.apiURL("/contents"), payload)
	if err != nil {
		// Surface a ref-race-style retry signal when the server complains
		// the branch moved (Gitea's wording differs from GitHub's; map both
		// shapes onto the existing isFastForwardRejection guard so the
		// outer retry loop keeps working).
		if isGiteaRefRaceError(err) {
			return fmt.Errorf("update ref: %w: not a fast forward (gitea contents API)", err)
		}
		return fmt.Errorf("change files: %w", err)
	}

	slog.Info("committed files via Gitea contents API",
		"branch", branch,
		"files", len(files),
		"ops", len(ops),
		"prune_prefixes", len(prunePrefixes),
	)
	return nil
}

// isBranchMissingError returns true iff the wrapped error from c.getRef
// indicates the target branch simply doesn't exist yet (a fresh
// `org-tenants` branch on a Sovereign that has only ever had a `main`).
// Two shapes are accepted: the Gitea/GitHub 404 ("Not Found") and the
// empty-array case getRef reports for an array response with zero
// elements. TBD-C18e (2026-05-18).
func isBranchMissingError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, " 404 ") ||
		strings.Contains(s, "Not Found") ||
		strings.Contains(s, "empty refs array")
}

// ensureBranchExists creates `branch` on the remote by branching from
// the repository's default branch. Used by commitOnceContents when the
// first tenant commit lands on a branch that hasn't been seeded yet.
// The two-step (read default branch SHA → POST /branches) pattern is
// supported by both Gitea (>= 1.13) and api.github.com.
//
// Gitea endpoint: POST /repos/{owner}/{repo}/branches with body
// `{old_branch_name, new_branch_name}` OR `{old_ref_name, new_branch_name}`.
// We use the second shape since it accepts an arbitrary commit-ish.
//
// Failure modes propagated as errors:
//   - default branch lookup fails (repo is empty or token lacks read).
//   - branch creation rejected (token lacks write or branch already
//     exists — which is a benign race we treat as success).
func (c *Client) ensureBranchExists(ctx context.Context, branch string) error {
	// Identify the source branch to fork from. Default to `main` (the
	// chart's bootstrap branch) — operators who run a different default
	// can flip the source branch by pre-creating `org-tenants` manually
	// once, after which this auto-create path is a no-op.
	const sourceBranch = "main"
	sourceSHA, err := c.getRef(ctx, sourceBranch)
	if err != nil {
		return fmt.Errorf("read source branch %q: %w", sourceBranch, err)
	}

	// Gitea + GitHub both accept `{ref: "refs/heads/<branch>", sha: "<sha>"}`
	// on POST /git/refs to create a new ref. Stick to that shape so this
	// path stays portable across both targets.
	_, err = c.doRequest(ctx, http.MethodPost, c.apiURL("/git/refs"), map[string]any{
		"ref": "refs/heads/" + branch,
		"sha": sourceSHA,
	})
	if err != nil {
		// 422 with "already exists" body means we lost a race with a
		// concurrent provisioner — treat as success.
		if strings.Contains(err.Error(), "already exists") ||
			strings.Contains(err.Error(), "Reference already exists") {
			return nil
		}
		return err
	}
	return nil
}

// isGiteaRefRaceError maps Gitea's branch-moved error wording onto the same
// "not a fast forward" signal the GitHub Git Data path uses, so the outer
// retry loop in CommitFilesWithPruneAndRebuild treats both equivalently.
// Gitea returns 409 Conflict or 422 with a body containing phrases like
// "branch has been changed" / "stale base" / "ref has been updated".
//
// hw158 funnel re-validation (Refs #3376): the production Organization funnel commits
// every per-tenant overlay to the SINGLE shared `org-tenants` branch. When two
// provisioning commits (or the funnel's own multi-step commit racing a
// concurrent teardown) try to update refs/heads/org-tenants at once, Gitea's
// git backend rejects the loser at the ref-lock layer with the git-native
// message `cannot lock ref 'refs/heads/org-tenants': ...` (a `.lock` file /
// compare-and-swap contention) rather than any of the higher-level phrases
// above. That string was NOT matched here, so the error fell through to the
// fatal `change files` branch and the outer retry loop never engaged — the
// funnel step "Committing manifests to Git" went straight to status:"failed".
// The git ref-lock shapes below restore the fetch-rebuild-retry for that case.
// Gitea surfaces several wordings depending on which failure path it takes:
//   - "cannot lock ref 'refs/heads/<branch>'"                       (lock held / CAS lost)
//   - "is at <sha> but expected <sha>"                              (CAS mismatch)
//   - "unable to create '...refs/heads/<branch>.lock': File exists" (lock-file race)
//   - "failed to update ref"                                       (generic ref-update wrap)
//
// #5234 — two MORE CAS-loss shapes come from the per-file SHA preconditions
// the batch ChangeFiles payload carries (the file-level compare-and-swap):
//   - "repository file already exists [path: …]" — our `create` op raced a
//     concurrent create of the same path (e.g. both the org-controller and
//     the funnel seeding/merging `vcluster/apps/kustomization.yaml`);
//   - "sha does not match [given: …, expected: …]" — our `update` op carried
//     a per-file SHA that went stale because a concurrent writer changed the
//     file after our tree/contents probe.
//
// Both mean a concurrent writer won; a fresh re-probe on the next attempt
// converts the op (create→update / stale-SHA→current-SHA) and succeeds.
// Before this they fell through to the fatal `change files` branch and the
// retry loop never engaged.
//
// #5387 (hw290) — THE KEYSTONE SHAPE. Every funnel Org that installed an app
// failed provisioning on:
//
//	POST …/repos/<slug>/catalyst-tenant/contents: 500
//	{"message":"PushOutOfDate Error … ! [rejected] a480a0e7… -> main (non-fast-forward)"}
//
// Gitea's ChangeFiles handler builds the commit in a temp clone of the branch
// head and then `git push`es it back to the bare repo. When a concurrent writer
// (the org-controller's PutFile burst, or a sibling cart-app commit) advanced
// the head between our clone and that push, git rejects the push and Gitea
// wraps it in `git.ErrPushOutOfDate`, whose Error() renders as
// `PushOutOfDate Error: <err>: <git stderr>` — the stderr carrying git's own
// `! [rejected] <sha> -> <branch> (non-fast-forward)`. That error type is NOT
// in Gitea's handleCreateOrUpdateFileError mapping table, so it falls through
// to a bare **500**, not a 409/422. None of the shapes above matched it: the
// status is not 409, and git spells it `non-fast-forward` (hyphenated), not
// `not a fast forward`. So the error was classified FATAL, the retry loop
// never ran even once, and the funnel step went straight to red on attempt 1 —
// with no "ref-race persisted after N attempts" in the message, which is how
// we know the CAS retry never engaged. This is a pure branch-head CAS loss:
// re-reading the head and re-pushing (what the outer loop already does) is
// exactly the right remedy.
//
// Deliberately NOT matched: Gitea's sibling `git.ErrPushRejected`
// ("PushRejected Error", stderr `! [remote rejected] … (pre-receive hook
// declined)`) — a branch-protection / hook refusal that no amount of retrying
// can convert into a success. Gitea itself splits the two on exactly the
// `non-fast-forward` substring, so keying on it keeps us precise.
func isGiteaRefRaceError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, " 409 ") ||
		strings.Contains(s, "branch has been changed") ||
		strings.Contains(s, "stale base") ||
		strings.Contains(s, "ref has been updated") ||
		strings.Contains(s, "not a fast forward") ||
		// git-native ref-lock contention on the shared org-tenants branch
		// (Refs #3376). Any of these means a concurrent writer won the CAS;
		// a fresh fetch-rebuild-retry against the new HEAD resolves it.
		strings.Contains(s, "cannot lock ref") ||
		strings.Contains(s, "but expected") ||
		(strings.Contains(s, "unable to create") && strings.Contains(s, ".lock")) ||
		strings.Contains(s, "failed to update ref") ||
		// file-level CAS losses from the ChangeFiles per-file SHA
		// preconditions (#5234) — see doc comment above.
		strings.Contains(s, "repository file already exists") ||
		strings.Contains(s, "sha does not match") ||
		// branch-head CAS loss inside Gitea's own ChangeFiles push, served
		// as a bare 500 (#5387) — see doc comment above.
		strings.Contains(s, "PushOutOfDate") ||
		strings.Contains(s, "non-fast-forward")
}

// getContentSHA returns the blob SHA of `path` at `branch`, or "" if the
// file does not exist (404). Used by commitOnceContents to decide
// create-vs-update for paths outside any prune prefix.
func (c *Client) getContentSHA(ctx context.Context, branch, path string) (string, error) {
	url := c.apiURL(fmt.Sprintf("/contents/%s?ref=%s", path, branch))
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", nil
	}
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return "", readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("getContentSHA: %d %s", resp.StatusCode, string(body))
	}
	var meta struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(body, &meta); err != nil {
		return "", err
	}
	return meta.SHA, nil
}

// resolveTreeish returns the tree-ish to feed `GET /git/trees/{sha}` for a
// recursive listing of the commit `commitSHA`'s tree.
//
// #4384 — the EMPTY-SHA 404. On the upstream GitHub Git Data path the tree
// endpoint expects a TREE SHA, so we first resolve commit→tree via
// `GET /git/commits/{sha}`. But Gitea does NOT serve the Git Data commit-read
// endpoint the same way: `getCommitTree` came back with an EMPTY tree SHA,
// which then built `GET /git/trees/?recursive=1` (no SHA component) → 404 —
// the exact failure the live day-2 cart install hit. Gitea's
// `GET /git/trees/{sha}` accepts a COMMIT SHA (and even a branch name)
// directly and resolves the tree itself, so on the Gitea path we skip the
// broken commit-read hop and pass the commit SHA straight through. We also
// fail LOUD on an empty resolved tree-ish instead of silently emitting the
// SHA-less URL that 404s.
func (c *Client) resolveTreeish(ctx context.Context, commitSHA string) (string, error) {
	if strings.TrimSpace(commitSHA) == "" {
		return "", fmt.Errorf("resolveTreeish: empty commit SHA (cannot list tree)")
	}
	if c.targetsGitea() {
		// Gitea resolves commit→tree inside GET /git/trees/{sha}; no
		// separate commit-read round-trip (which returns an empty tree SHA
		// and 404s the listing — #4384).
		return commitSHA, nil
	}
	treeSHA, err := c.getCommitTree(ctx, commitSHA)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(treeSHA) == "" {
		return "", fmt.Errorf("resolveTreeish: commit %q resolved to an empty tree SHA", commitSHA)
	}
	return treeSHA, nil
}

// listTreeBlobsWithSHA is like listTreeBlobs but also returns each blob's
// SHA, keyed by path. Used by commitOnceContents to populate update/delete
// operations in the ChangeFiles batch.
func (c *Client) listTreeBlobsWithSHA(ctx context.Context, commitSHA string, prefixes []string) (map[string]string, error) {
	treeSHA, err := c.resolveTreeish(ctx, commitSHA)
	if err != nil {
		return nil, err
	}
	body, err := c.doRequest(ctx, http.MethodGet,
		c.apiURL("/git/trees/"+treeSHA+"?recursive=1"), nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
			SHA  string `json:"sha"`
		} `json:"tree"`
		Truncated bool `json:"truncated"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	if resp.Truncated {
		slog.Warn("git tree listing was truncated — some orphan files may survive",
			"commit", commitSHA)
	}
	out := map[string]string{}
	for _, entry := range resp.Tree {
		if entry.Type != "blob" {
			continue
		}
		for _, pfx := range prefixes {
			if strings.HasPrefix(entry.Path, pfx) {
				out[entry.Path] = entry.SHA
				break
			}
		}
	}
	return out, nil
}

// listTreeBlobs returns all blob paths in commitSHA's tree that begin with any
// of the given prefixes. Uses the recursive tree API for efficiency.
func (c *Client) listTreeBlobs(ctx context.Context, commitSHA string, prefixes []string) ([]string, error) {
	treeSHA, err := c.resolveTreeish(ctx, commitSHA)
	if err != nil {
		return nil, err
	}
	body, err := c.doRequest(ctx, http.MethodGet,
		c.apiURL("/git/trees/"+treeSHA+"?recursive=1"), nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"tree"`
		Truncated bool `json:"truncated"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	if resp.Truncated {
		slog.Warn("git tree listing was truncated — some orphan files may survive", "commit", commitSHA)
	}
	var out []string
	for _, entry := range resp.Tree {
		if entry.Type != "blob" {
			continue
		}
		for _, pfx := range prefixes {
			if strings.HasPrefix(entry.Path, pfx) {
				out = append(out, entry.Path)
				break
			}
		}
	}
	return out, nil
}

// --- GitHub Git Data API types ---

type treeEntry struct {
	Path   string `json:"path"`
	Mode   string `json:"mode"`
	Type   string `json:"type"`
	SHA    string `json:"-"`
	Delete bool   `json:"-"`
}

// MarshalJSON emits sha:null for delete entries (GitHub's Git Data API uses
// sha=null to indicate "remove this path from the tree") and a string SHA
// otherwise. Using the default struct marshaler would emit empty strings,
// which GitHub rejects.
func (t treeEntry) MarshalJSON() ([]byte, error) {
	base := map[string]any{
		"path": t.Path,
		"mode": t.Mode,
		"type": t.Type,
	}
	if t.Delete {
		base["sha"] = nil
	} else {
		base["sha"] = t.SHA
	}
	return json.Marshal(base)
}

func (c *Client) apiURL(path string) string {
	base := c.APIURL
	if base == "" {
		base = "https://api.github.com"
	}
	return fmt.Sprintf("%s/repos/%s/%s%s", base, c.Owner, c.Repo, path)
}

func (c *Client) doRequest(ctx context.Context, method, url string, body any) ([]byte, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(b)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, method, url, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GitHub API %s %s: %d %s", method, url, resp.StatusCode, string(respBody))
	}
	return respBody, nil
}

func (c *Client) getRef(ctx context.Context, branch string) (string, error) {
	// TBD-C18c (2026-05-18): Use plural /git/refs/heads/<branch>.
	//
	// GitHub supports BOTH /git/ref/<ref> (singular, returns one object) and
	// /git/refs/<ref> (plural, prefix-match — returns one object for an exact
	// ref, array for a prefix). Gitea 1.22 ONLY implements the plural form
	// and ALWAYS returns an array even for an exact `heads/<branch>` match
	// (https://docs.gitea.com/api#tag/repository/operation/repoGetGitRefs).
	//
	// The previous singular path 404'd on Gitea, blocking customer journey
	// step 14 ("Committing manifests to Git") and downstream Org CR creation
	// during day-2 install onto a Sovereign cluster's in-cluster Gitea.
	//
	// We try the array shape first (Gitea + GitHub prefix-style) and fall
	// back to a single-object decode (GitHub exact-ref shape — kept for
	// forward compatibility against api.github.com behavior).
	body, err := c.doRequest(ctx, http.MethodGet, c.apiURL("/git/refs/heads/"+branch), nil)
	if err != nil {
		return "", err
	}
	type refResp struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	var arr []refResp
	if err := json.Unmarshal(body, &arr); err == nil {
		if len(arr) == 0 {
			return "", fmt.Errorf("getRef: empty refs array for heads/%s", branch)
		}
		return arr[0].Object.SHA, nil
	}
	var single refResp
	if err := json.Unmarshal(body, &single); err != nil {
		return "", fmt.Errorf("getRef: response neither array nor object: %w", err)
	}
	return single.Object.SHA, nil
}

func (c *Client) getCommitTree(ctx context.Context, commitSHA string) (string, error) {
	body, err := c.doRequest(ctx, http.MethodGet, c.apiURL("/git/commits/"+commitSHA), nil)
	if err != nil {
		return "", err
	}
	var resp struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", err
	}
	return resp.Tree.SHA, nil
}

func (c *Client) createBlob(ctx context.Context, content string) (string, error) {
	body, err := c.doRequest(ctx, http.MethodPost, c.apiURL("/git/blobs"), map[string]string{
		"content":  content,
		"encoding": "utf-8",
	})
	if err != nil {
		return "", err
	}
	var resp struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", err
	}
	return resp.SHA, nil
}

func (c *Client) createTree(ctx context.Context, baseTreeSHA string, entries []treeEntry) (string, error) {
	body, err := c.doRequest(ctx, http.MethodPost, c.apiURL("/git/trees"), map[string]any{
		"base_tree": baseTreeSHA,
		"tree":      entries,
	})
	if err != nil {
		return "", err
	}
	var resp struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", err
	}
	return resp.SHA, nil
}

func (c *Client) createCommit(ctx context.Context, message, treeSHA, parentSHA string) (string, error) {
	body, err := c.doRequest(ctx, http.MethodPost, c.apiURL("/git/commits"), map[string]any{
		"message": message,
		"tree":    treeSHA,
		"parents": []string{parentSHA},
	})
	if err != nil {
		return "", err
	}
	var resp struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", err
	}
	return resp.SHA, nil
}

func (c *Client) updateRef(ctx context.Context, branch, commitSHA string) error {
	_, err := c.doRequest(ctx, http.MethodPatch, c.apiURL("/git/refs/heads/"+branch), map[string]any{
		"sha":   commitSHA,
		"force": false,
	})
	return err
}

// ListDir returns the basenames of the FILES directly under dir on the given
// branch, via the GitHub/Gitea-compatible repository contents API (a GET on a
// directory path returns a JSON array of entries on both providers). Returns
// a wrapped ErrFileNotFound when the directory does not exist on the branch
// (a fresh per-Org repo before the org-controller seeded the tree). Used by
// the funnel cart install (#5104) to derive the org-controller baseline docs
// actually committed in `vcluster/apps/` / `vcluster/host-apps/` so the
// kustomization merge never orphans a boundary file it doesn't know by name.
func (c *Client) ListDir(ctx context.Context, branch, dir string) ([]string, error) {
	url := c.apiURL(fmt.Sprintf("/contents/%s?ref=%s", dir, branch))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return nil, fmt.Errorf("%w: %s", ErrFileNotFound, dir)
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API: %d %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var entries []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, fmt.Errorf("decode contents listing %s: %w", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Type != "file" {
			continue
		}
		names = append(names, e.Name)
	}
	return names, nil
}

// ReadFile reads a file's content from the repository at the given branch.
func (c *Client) ReadFile(ctx context.Context, branch, path string) (string, error) {
	url := c.apiURL(fmt.Sprintf("/contents/%s?ref=%s", path, branch))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github.raw+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		// Wrap the sentinel so callers can errors.Is(err, ErrFileNotFound)
		// to safely treat a genuine 404 as "no such file yet" — distinct
		// from a transient read failure where an empty fallback is unsafe
		// (#4250).
		return "", fmt.Errorf("%w: %s", ErrFileNotFound, path)
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("GitHub API: %d %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	// #4206 #3785 — the Gitea contents API (/api/v1/repos/.../contents/<path>)
	// ALWAYS returns a JSON envelope `{"content":"<base64>","encoding":"base64",
	// ...}` regardless of the `Accept: application/vnd.github.raw+json` header
	// (which only GitHub.com honours). Returning that envelope verbatim is the
	// bug that corrupted the org-tenants PARENT kustomization.yaml: the
	// rebuild closure read this envelope, spliced the new Org subdir onto it,
	// and committed the JSON-wrapped string back as the file — which Flux /
	// kustomize cannot parse, so the whole org-tenants tree (incl. the demo
	// Org) stopped reconciling and its namespace was pruned. Decode the
	// envelope to the raw file content. If the body is NOT a Gitea envelope
	// (e.g. a genuine GitHub raw response), fall back to the raw body.
	var meta struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if jerr := json.Unmarshal(body, &meta); jerr != nil || meta.Encoding == "" {
		return string(body), nil
	}
	if meta.Encoding == "base64" {
		decoded, derr := base64.StdEncoding.DecodeString(strings.ReplaceAll(meta.Content, "\n", ""))
		if derr != nil {
			return "", fmt.Errorf("decode gitea contents %s: %w", path, derr)
		}
		return string(decoded), nil
	}
	return meta.Content, nil
}
