// Package giteapr implements the catalyst-api side of the per-Org IaC
// repo PR pipeline (ADR-0009 + G117.3).
//
// The pipeline:
//
//  1. Open a branch on `gitea.<sov>/<org>/iac` named
//     `endpoint/<app>/<name>/<sha8>` (sha8 is a 8-char digest of the
//     manifest bytes so successive identical writes idempotently land
//     the same branch).
//  2. Write the endpoint manifest at
//     `apps/<app>/endpoints/<name>.yaml` (create / update) or remove
//     it (delete).
//  3. Open a PR from that branch → `main`.
//  4. Poll the three named status checks until all pass, fail, or
//     the poll budget elapses.
//  5. On all-pass: auto-merge with style=squash. On any-fail: return
//     a `Status: failed-precheck` result and leave the PR open for
//     human triage.
//
// This package is intentionally a NARROW WRAPPER over the existing
// `core/controllers/pkg/gitea.Client` plus the wire shape every
// endpoint handler needs to return. Tests inject a stub GiteaIaCClient
// + StatusChecker; production wires the real Gitea client +
// status-check poller.
//
// The branch-protection contract — three named status checks — is set
// at Org bootstrap time per `tools/bootstrap-org-iac-repo.sh`. The
// names are LOCKED:
//
//	kyverno-admission
//	cert-manager-precheck
//	dns-conflict-precheck
//
// PR auto-merge only fires when ALL THREE flip to "success". Until
// then the PR sits with `pending`/`failure` checks visible in the
// Gitea UI.
package giteapr

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openova-io/openova/core/controllers/pkg/gitea"
)

// IaCRepoName is the canonical per-Org IaC repo name. Per ADR-0009.
const IaCRepoName = "iac"

// DefaultBranch is the protected base branch every mutation PR
// targets. Set at Org bootstrap; locked.
const DefaultBranch = "main"

// LockedStatusCheckNames are the three status-check names branch
// protection requires. Order is stable for test assertions.
var LockedStatusCheckNames = []string{
	"kyverno-admission",
	"cert-manager-precheck",
	"dns-conflict-precheck",
}

// Mutation captures the proposed manifest write. The handler builds
// this from the API request body + the Org/App context.
type Mutation struct {
	Org           string
	App           string
	EndpointName  string
	ManifestYAML  []byte // nil/empty when Op=Delete
	Op            Op
	CommitMessage string
	PRTitle       string
	PRBody        string
}

// Op identifies the manifest change kind.
type Op int

const (
	OpUnknown Op = iota
	OpCreate
	OpUpdate
	OpDelete
)

// Result is the synchronous output of OpenAndMerge. The handler
// converts this into the OpenAPI `EndpointPR` response shape.
type Result struct {
	PRNumber int64
	PRURL    string
	Branch   string
	Path     string
	Status   Status

	// PerCheck records the LAST-observed status for each named check.
	// Stable order matches LockedStatusCheckNames so a JSON dump is
	// deterministic.
	PerCheck map[string]CheckStatus
}

// Status — final outcome of the pipeline.
type Status string

const (
	StatusOpen           Status = "open"            // PR opened, checks still running
	StatusMerged         Status = "merged"          // auto-merge succeeded
	StatusFailedPrecheck Status = "failed-precheck" // at least one check failed
	StatusClosed         Status = "closed"          // PR was closed without merge
)

// CheckStatus mirrors the Gitea commit-status states. We collapse to
// three for the API surface (matching the OpenAPI EndpointPR schema):
//
//	pending  → still running, no decision yet
//	pass     → success
//	fail     → failure | error | cancelled
type CheckStatus string

const (
	CheckPending CheckStatus = "pending"
	CheckPass    CheckStatus = "pass"
	CheckFail    CheckStatus = "fail"
)

// GiteaIaCClient is the narrow seam this package needs from the
// unified Gitea client. Tests inject a fake; production wires the
// real `gitea.Client`.
type GiteaIaCClient interface {
	EnsureBranch(ctx context.Context, org, repo, branch string) error
	PutFile(ctx context.Context, org, repo, branch, path string, data []byte, message string, opts ...gitea.PutFileOpts) (gitea.File, bool, error)
	DeleteFile(ctx context.Context, org, repo, branch, path, message string) (bool, error)
	CreatePullRequest(ctx context.Context, org, repo, head, base, title, body string) (gitea.PullRequest, error)
	MergePullRequest(ctx context.Context, org, repo string, number int64, opts gitea.MergePROpts) error
}

// StatusChecker abstracts the per-commit status-check lookup. The
// production binding hits Gitea's `/repos/{owner}/{repo}/statuses/{sha}`
// endpoint; tests inject a fake that returns a script of states.
type StatusChecker interface {
	// GetStatuses returns the LATEST status per check name for the
	// given branch HEAD. The result map is keyed by check name; values
	// must be a member of CheckStatus.
	GetStatuses(ctx context.Context, org, repo, branchOrSHA string) (map[string]CheckStatus, error)
}

// PollConfig controls the status-check loop. Defaults are
// production-safe; tests pass tiny values.
type PollConfig struct {
	Interval time.Duration // between probes; default 5s
	Budget   time.Duration // overall ceiling; default 90s
}

// Writer is the production wrapper. Use NewWriter to construct.
type Writer struct {
	gitea  GiteaIaCClient
	status StatusChecker
	poll   PollConfig
	now    func() time.Time
}

// NewWriter assembles a Writer. nil clients return errors at OpenAndMerge.
func NewWriter(g GiteaIaCClient, s StatusChecker, poll PollConfig) *Writer {
	if poll.Interval <= 0 {
		poll.Interval = 5 * time.Second
	}
	if poll.Budget <= 0 {
		poll.Budget = 90 * time.Second
	}
	return &Writer{gitea: g, status: s, poll: poll, now: time.Now}
}

// ErrNotWired is returned when one of the required clients is nil.
var ErrNotWired = errors.New("giteapr: writer not wired (gitea or status checker missing)")

// EndpointManifestPath returns the canonical path inside the iac repo
// for an endpoint manifest. Stable so tests / migration tooling can
// address it.
func EndpointManifestPath(app, endpoint string) string {
	return fmt.Sprintf("apps/%s/endpoints/%s.yaml", app, endpoint)
}

// BranchName builds the head-branch name for a mutation. The 8-char
// content digest makes the branch deterministic per manifest body so
// retried opens of the same write idempotently address the same
// branch.
func BranchName(m Mutation) string {
	h := sha1.New()
	h.Write([]byte(string(rune(m.Op))))
	h.Write([]byte(m.App))
	h.Write([]byte(m.EndpointName))
	h.Write(m.ManifestYAML)
	digest := hex.EncodeToString(h.Sum(nil))[:8]
	op := "endpoint"
	switch m.Op {
	case OpDelete:
		op = "endpoint-del"
	}
	return fmt.Sprintf("%s/%s/%s/%s", op, m.App, m.EndpointName, digest)
}

// OpenAndMerge is the end-to-end pipeline: branch → write → PR → poll
// → merge.
//
// Returns a Result describing the final state. The Result.Status
// field is the authoritative answer the caller maps onto the OpenAPI
// EndpointPR.status field.
func (w *Writer) OpenAndMerge(ctx context.Context, m Mutation) (Result, error) {
	if w == nil || w.gitea == nil || w.status == nil {
		return Result{}, ErrNotWired
	}
	if strings.TrimSpace(m.Org) == "" || strings.TrimSpace(m.App) == "" || strings.TrimSpace(m.EndpointName) == "" {
		return Result{}, errors.New("giteapr: Org/App/EndpointName required")
	}
	if m.Op == OpUnknown {
		return Result{}, errors.New("giteapr: Op required (create|update|delete)")
	}
	if m.Op != OpDelete && len(m.ManifestYAML) == 0 {
		return Result{}, errors.New("giteapr: ManifestYAML required for create/update")
	}

	branch := BranchName(m)
	path := EndpointManifestPath(m.App, m.EndpointName)

	// 1. Ensure branch exists (idempotent).
	if err := w.gitea.EnsureBranch(ctx, m.Org, IaCRepoName, branch); err != nil {
		return Result{}, fmt.Errorf("giteapr: ensure branch %q: %w", branch, err)
	}

	// 2. Write or delete the manifest on the branch.
	switch m.Op {
	case OpCreate, OpUpdate:
		_, _, err := w.gitea.PutFile(ctx, m.Org, IaCRepoName, branch, path, m.ManifestYAML, m.CommitMessage)
		if err != nil {
			return Result{}, fmt.Errorf("giteapr: put file: %w", err)
		}
	case OpDelete:
		_, err := w.gitea.DeleteFile(ctx, m.Org, IaCRepoName, branch, path, m.CommitMessage)
		if err != nil {
			return Result{}, fmt.Errorf("giteapr: delete file: %w", err)
		}
	}

	// 3. Open PR (idempotent — Gitea returns the existing PR on
	//    409 race).
	pr, err := w.gitea.CreatePullRequest(ctx, m.Org, IaCRepoName, branch, DefaultBranch, m.PRTitle, m.PRBody)
	if err != nil {
		return Result{}, fmt.Errorf("giteapr: open PR: %w", err)
	}

	// 4. Poll status checks until terminal or budget exhausted.
	final := w.pollUntilTerminal(ctx, m.Org, branch)
	res := Result{
		PRNumber: pr.Number,
		PRURL:    pr.URL,
		Branch:   branch,
		Path:     path,
		PerCheck: final.perCheck,
		Status:   StatusOpen,
	}

	if final.allFail {
		res.Status = StatusFailedPrecheck
		// Leave the PR open for human triage. Don't merge a broken
		// gate.
		return res, nil
	}

	if !final.allPass {
		// Budget elapsed; PR remains open with pending/in-flight
		// checks. Caller can retry the merge once checks finish.
		res.Status = StatusOpen
		return res, nil
	}

	// 5. All checks green — auto-merge with squash.
	mergeOpts := gitea.MergePROpts{Style: "squash"}
	if mErr := w.gitea.MergePullRequest(ctx, m.Org, IaCRepoName, pr.Number, mergeOpts); mErr != nil {
		// Merge race / temporary unavailable — leave PR open and let
		// the caller decide. We do NOT downgrade to failed-precheck
		// because the checks themselves passed.
		res.Status = StatusOpen
		return res, fmt.Errorf("giteapr: merge PR #%d: %w", pr.Number, mErr)
	}
	res.Status = StatusMerged
	return res, nil
}

// pollOutcome — internal helper struct.
type pollOutcome struct {
	allPass  bool
	allFail  bool
	perCheck map[string]CheckStatus
}

// pollUntilTerminal blocks until either (a) every named check is in
// {pass,fail}, OR (b) the poll budget elapses.
//
// `allPass` is true only when every check is `pass`. `allFail` is
// true when at least one check is `fail` AND every remaining check
// is terminal. Pending checks past budget yield allPass=false +
// allFail=false → caller decides.
func (w *Writer) pollUntilTerminal(ctx context.Context, org, branch string) pollOutcome {
	deadline := w.now().Add(w.poll.Budget)
	perCheck := map[string]CheckStatus{}
	for _, n := range LockedStatusCheckNames {
		perCheck[n] = CheckPending
	}

	for {
		if ctx.Err() != nil {
			return pollOutcome{perCheck: perCheck}
		}
		latest, err := w.status.GetStatuses(ctx, org, IaCRepoName, branch)
		if err == nil {
			for _, n := range LockedStatusCheckNames {
				if s, ok := latest[n]; ok {
					perCheck[n] = s
				}
			}
		}
		// Terminal?
		allTerminal := true
		anyFail := false
		allPass := true
		for _, n := range LockedStatusCheckNames {
			s := perCheck[n]
			if s != CheckPass && s != CheckFail {
				allTerminal = false
			}
			if s == CheckFail {
				anyFail = true
			}
			if s != CheckPass {
				allPass = false
			}
		}
		if allTerminal {
			return pollOutcome{allPass: allPass, allFail: anyFail, perCheck: perCheck}
		}
		// Budget exhausted?
		if !w.now().Before(deadline) {
			return pollOutcome{perCheck: perCheck}
		}
		// Sleep, but bail if ctx cancels mid-sleep.
		select {
		case <-ctx.Done():
			return pollOutcome{perCheck: perCheck}
		case <-time.After(w.poll.Interval):
		}
	}
}

// FormatPRBody builds a stable, machine-readable PR body. catalyst-api
// emits one of these per mutation so a sovereign-admin scanning Gitea
// can correlate every IaC PR back to the originating console action.
func FormatPRBody(m Mutation, requester string) string {
	op := "update"
	switch m.Op {
	case OpCreate:
		op = "create"
	case OpDelete:
		op = "delete"
	}
	if requester == "" {
		requester = "(catalyst-api system)"
	}
	return fmt.Sprintf(`Endpoint %s for Application **%s** (Org %s).

- endpoint:    %s
- action:      %s
- requester:   %s
- manifest:    %s
- pipeline:    catalyst-api G117.3 (ADR-0009)

Three named status checks gate auto-merge:

- kyverno-admission
- cert-manager-precheck
- dns-conflict-precheck

On all-green the PR auto-merges; on any-fail it stays open for
sovereign-admin triage. Closing this PR without merge is a no-op on the
cluster state — Flux reconciles from main only.
`, op, m.App, m.Org, m.EndpointName, op, requester, EndpointManifestPath(m.App, m.EndpointName))
}
