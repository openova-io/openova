package gitwrite

import (
	"context"
	"fmt"

	"github.com/openova-io/openova/core/controllers/pkg/gitea"
)

// ChangeBranch is the branch of a change in ModePullRequest: "change/"
// followed by the change identifier.
func ChangeBranch(changeID string) string {
	return "change/" + changeID
}

// commitPullRequest is Commit for ModePullRequest (semantics 11 of the
// contract); checkFields has made sure the base branch is main. A no-op
// or an IfMatch conflict on main creates nothing. Otherwise the files are
// committed in one call to the branch ChangeBranch(ChangeID), created from
// main or kept when it exists, and a pull request from it into main is
// opened, or the open one returned. When the branch already holds the
// change, an earlier call with the same ChangeID committed it, and nothing
// is committed again. main is never written; nothing waits for the merge.
func commitPullRequest(ctx context.Context, gc *gitea.Client, ch Change, message string, author, committer Identity) (Result, error) {
	base := branchOf(ch.Repo)
	cur, err := readFiles(ctx, gc, ch.Repo, base, ch.Files)
	if err != nil {
		return Result{}, err
	}
	ops, results := planOps(ch.Files, cur)
	if len(ops) == 0 {
		head, err := gc.BranchHead(ctx, ch.Repo.Owner, ch.Repo.Name, base)
		if err != nil {
			return Result{}, fmt.Errorf("gitwrite: read the head of %s: %w", base, classify(ctx, err))
		}
		return Result{Commit: head, Committed: false, Files: results}, nil
	}
	if err := checkIfMatch(ch.Files, cur); err != nil {
		return Result{}, err
	}

	branch := ChangeBranch(ch.ChangeID)
	if err := gc.EnsureBranch(ctx, ch.Repo.Owner, ch.Repo.Name, branch); err != nil {
		return Result{}, fmt.Errorf("gitwrite: create the branch %s: %w", branch, classify(ctx, err))
	}
	cur, err = readFiles(ctx, gc, ch.Repo, branch, ch.Files)
	if err != nil {
		return Result{}, err
	}
	ops, results = planOps(ch.Files, cur)
	res := Result{Files: results}
	if len(ops) == 0 {
		head, err := gc.BranchHead(ctx, ch.Repo.Owner, ch.Repo.Name, branch)
		if err != nil {
			return Result{}, fmt.Errorf("gitwrite: read the head of %s: %w", branch, classify(ctx, err))
		}
		res.Commit = head
	} else {
		if err := checkIfMatch(ch.Files, cur); err != nil {
			return Result{}, err
		}
		resp, err := gc.ChangeFiles(ctx, ch.Repo.Owner, ch.Repo.Name, gitea.ChangeFilesRequest{
			Branch:    branch,
			Message:   message,
			Author:    gitea.CommitIdentity{Name: author.Name, Email: author.Email},
			Committer: gitea.CommitIdentity{Name: committer.Name, Email: committer.Email},
			Files:     ops,
		})
		if err != nil {
			return Result{}, fmt.Errorf("gitwrite: commit to %s: %w", branch, classify(ctx, err))
		}
		for _, w := range resp.Files {
			for i, f := range ch.Files {
				if f.Path == w.Path && f.Op == OpUpsert {
					results[i].BlobSHA = w.SHA
				}
			}
		}
		res.Commit, res.Committed = resp.CommitSHA, true
	}

	pr, err := gc.CreatePullRequest(ctx, ch.Repo.Owner, ch.Repo.Name, branch, base, ch.Summary, message)
	if err != nil {
		return Result{}, fmt.Errorf("gitwrite: open the pull request of %s: %w", branch, classify(ctx, err))
	}
	if pr.URL == "" {
		return Result{}, fmt.Errorf("gitwrite: the pull request of %s has no address", branch)
	}
	res.PullRequestURL = pr.URL
	return res, nil
}
