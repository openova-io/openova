package orgtenantrepos

// The re-render before the egress hold (handler repointOrgTenantSources)
// clones and pushes the tenants branch while the cutover waits to start step
// 08. Without a deadline a Gitea that does not answer held the cutover there
// with no event; Repoint bounds it and names the outcome.

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// RepointDeadline bounds the re-render. A healthy run is one depth-1 clone of
// the tenants branch from the in-cluster Gitea and at most one push (the same
// clone every Organization write makes): seconds. Two minutes is many times
// that, so only a Gitea that does not answer reaches it, and it holds the
// cutover for less than step 08's own pivot poll (300 s).
const RepointDeadline = 2 * time.Minute

// RepointTimedOut names the warning the cutover records when the re-render
// does not finish within its deadline. The cutover goes on: step 08 judges
// the objects and names the owner of any left on the public catalog.
const RepointTimedOut = "org-tenants-rerender-timed-out"

// Repoint runs rerender under deadline, on a child of ctx, and returns the
// level ("info" or "warn") and the message of the cutover event that records
// the outcome, after logging it on log (when not nil). It never fails the
// cutover, and it returns at the deadline also when rerender does not: the
// deadline kills git (exec.CommandContext), but a process git started can keep
// git's output open, and Wait then blocks until that process ends. Measured on
// the GitHub runner (the silent-remote test held 30 s against a 1 s deadline)
// and here with a git that leaves such a child. rerender then finishes on its
// own; the cutover does not wait for it.
func Repoint(ctx context.Context, log *slog.Logger, deadline time.Duration, rerender func(context.Context) (string, error)) (string, string) {
	rctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	type outcome struct {
		sha string
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		sha, err := rerender(rctx)
		done <- outcome{sha, err}
	}()
	var out outcome
	select {
	case out = <-done:
	case <-rctx.Done():
		select {
		case out = <-done:
		default:
			out.err = fmt.Errorf("the re-render did not return: %w", rctx.Err())
		}
	}
	sha, err := out.sha, out.err
	level, msg := slog.LevelWarn, ""
	switch {
	case err != nil && ctx.Err() == nil && rctx.Err() != nil:
		msg = fmt.Sprintf("%s: org-tenants shared HelmRepositories not re-rendered within %s (the tenants Git remote did not answer) — step egress-block-test judges them and names the owner: %v", RepointTimedOut, deadline, err)
	case err != nil:
		msg = "org-tenants shared HelmRepositories re-render failed — step egress-block-test judges them and names the owner: " + err.Error()
	case sha != "":
		level, msg = slog.LevelInfo, "org-tenants shared HelmRepositories re-rendered on the Sovereign's own registry, commit "+sha
	default:
		level, msg = slog.LevelInfo, "org-tenants shared HelmRepositories need no re-render (none names the public catalog, or the Sovereign's own source is not usable yet)"
	}
	if log != nil {
		log.Log(ctx, level, "cutover: "+msg)
	}
	return strings.ToLower(level.String()), msg
}
