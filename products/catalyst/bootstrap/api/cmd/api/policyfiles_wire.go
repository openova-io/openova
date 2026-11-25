package main

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/openova-io/openova/core/controllers/pkg/gitwrite"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/compliance"
)

// policyFilesTimeout bounds the alignment of the policy files at start-up.
const policyFilesTimeout = 2 * time.Minute

// starter is the part of the compliance aggregator that main() starts.
type starter interface {
	Start(ctx context.Context)
}

// startCompliance starts the compliance aggregator agg and then, in the
// background, aligns the policy files of every region of this Sovereign in
// the fleet repository (compliance.EnsurePolicyFiles; contract C4, section
// 2.2, policy files). The alignment never delays or stops the API: it
// runs once, and a failure is logged. getenv reads the environment
// (os.Getenv in production).
func startCompliance(ctx context.Context, log *slog.Logger, agg starter, getenv func(string) string) {
	agg.Start(ctx)
	go func() {
		_, _ = alignPolicyFilesFromEnv(ctx, log, getenv)
	}()
}

// alignPolicyFilesFromEnv builds the commit library from the configuration
// of package intent (CATALYST_GITEA_URL, CATALYST_GITEA_TOKEN and
// SOVEREIGN_FQDN) and aligns the policy files. A missing setting is logged
// and returned, and nothing is called.
func alignPolicyFilesFromEnv(ctx context.Context, log *slog.Logger, getenv func(string) string) (gitwrite.Result, error) {
	cfg, err := intentGitConfig(getenv)
	if err == nil {
		var w *gitwrite.Client
		if w, err = gitwrite.New(cfg); err == nil {
			return alignPolicyFiles(ctx, log, w, strings.TrimSpace(getenv("SOVEREIGN_FQDN")))
		}
	}
	log.Warn("compliance: policy files not aligned at start-up", "err", err)
	return gitwrite.Result{}, err
}

// alignPolicyFiles runs compliance.EnsurePolicyFiles once, within
// policyFilesTimeout, and logs its outcome.
func alignPolicyFiles(ctx context.Context, log *slog.Logger, w gitwrite.Writer, fqdn string) (gitwrite.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, policyFilesTimeout)
	defer cancel()
	res, err := compliance.EnsurePolicyFiles(ctx, w, fqdn)
	if err != nil {
		log.Warn("compliance: policy files not aligned at start-up", "fqdn", fqdn, "err", err)
		return res, err
	}
	log.Info("compliance: policy files aligned", "fqdn", fqdn, "committed", res.Committed, "commit", res.Commit)
	return res, nil
}
