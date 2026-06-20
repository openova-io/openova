// Package gitguard centralises the cross-cluster pollution guard that
// keeps the Organization provisioning service from committing per-tenant overlays
// into a foreign cluster's clusters/<fqdn>/ tree (issue #944).
//
// Background:
//
//	The provisioning service runs on Catalyst-Zero (contabo) AND on
//	every franchised Sovereign that flips MARKETPLACE_ENABLED=true.
//	On contabo the canonical write target is
//	`clusters/contabo-mkt/tenants/...`. On a Sovereign it is
//	`clusters/<sovereign-fqdn>/org-tenants/...`. Pre-#944 the
//	GIT_BASE_PATH env defaulted to the contabo path EVERYWHERE — so a
//	Sovereign-side provisioning Pod committed alice's tenant overlay
//	to upstream openova/openova `clusters/contabo-mkt/tenants/<id>/`,
//	which the contabo Flux Kustomization would then install on the
//	contabo cluster. C5-final caught + reverted the alice2 incident
//	at commit 5715db04 (2026-05-05) — without #944 it would have
//	repeated on every alice retry.
//
// The validator runs in TWO places:
//
//  1. main.go startup, before the HTTP listener starts: a misconfigured
//     env crashes the Pod into Pending so the operator notices via a
//     normal kubectl status read.
//
//  2. handlers.Handler.VerifyCommitTargetSafe, which every code path
//     that reaches GitHubClient.CommitFiles* MUST invoke before the
//     commit. Defence in depth — runtime mutations (kubectl exec env
//     change, ConfigMap update without Pod restart, an OTECHFQDN
//     resolution that does not share the configured prefix) can not
//     bypass the startup check.
//
// Per Inviolable Principle #4 the configured base path AND the live
// SOVEREIGN_FQDN are env-driven; the validator never reads from os.Getenv
// itself so unit tests stay deterministic.
package gitguard

import (
	"errors"
	"fmt"
	"strings"
)

// ValidateBasePath enforces the cross-cluster pollution guard from
// issue #944. On Sovereigns the only legitimate write target is
// `clusters/<SOVEREIGN_FQDN>/org-tenants[...]/`. Anything else (e.g.
// `clusters/contabo-mkt/tenants/`, `clusters/otechN.omani.works/...`
// from a stale per-Sovereign overlay) would route the per-tenant
// commit at a foreign cluster's tree.
//
// Catalyst-Zero (contabo) runs with sovereignFQDN unset and the legacy
// path `clusters/contabo-mkt/tenants` is canonical there; the guard
// recognises that case as the only allowed empty-FQDN path.
//
// Returns nil if the path is safe; a descriptive error otherwise.
// Callers fail loud at startup AND before every git commit so a
// runtime env mutation cannot bypass the check.
func ValidateBasePath(gitBasePath, sovereignFQDN string) error {
	if gitBasePath == "" {
		return errors.New("GIT_BASE_PATH must be set")
	}
	clean := strings.TrimSuffix(gitBasePath, "/")
	// Disallow obvious traversal — neither `..` nor absolute paths are
	// ever valid for this env.
	if strings.HasPrefix(clean, "/") || strings.Contains(clean, "..") {
		return fmt.Errorf("GIT_BASE_PATH must be a repo-relative path under clusters/, got %q", gitBasePath)
	}
	if !strings.HasPrefix(clean, "clusters/") {
		return fmt.Errorf("GIT_BASE_PATH must start with clusters/, got %q", gitBasePath)
	}
	if sovereignFQDN == "" {
		// Catalyst-Zero (contabo). The only legitimate path is the
		// canonical contabo-mkt prefix; anything else (especially a
		// stale otechN per-Sovereign overlay leaking into a contabo
		// install) is rejected.
		const contaboPrefix = "clusters/contabo-mkt/"
		if !strings.HasPrefix(clean, contaboPrefix) {
			return fmt.Errorf(
				"GIT_BASE_PATH on Catalyst-Zero (SOVEREIGN_FQDN unset) must start with %q, got %q — refusing to commit to a foreign cluster's tree",
				contaboPrefix, gitBasePath,
			)
		}
		return nil
	}
	wantPrefix := "clusters/" + sovereignFQDN + "/"
	if !strings.HasPrefix(clean, wantPrefix) {
		return fmt.Errorf(
			"GIT_BASE_PATH must start with %q on this Sovereign, got %q — refusing to commit to a foreign cluster's tree (issue #944 cross-cluster pollution guard)",
			wantPrefix, gitBasePath,
		)
	}
	return nil
}

// PlaceholderTokenSubstrings are byte sequences a Helm-templated /
// hand-rolled placeholder Secret might carry that we MUST refuse at
// startup rather than silently accept (issue #940). Empty token already
// trips an explicit warn-and-continue path in main.go; this list catches
// the literal placeholder strings the chart historically shipped.
var PlaceholderTokenSubstrings = []string{
	"<placeholder>",
	"<changeme>",
	"<change-me>",
	"PLACEHOLDER",
	"REPLACE_ME",
	"REPLACEME",
}

// ValidateGitHubToken rejects placeholder Secret bytes at startup
// (issue #940). Returns nil for empty token (handled by an explicit
// warn-and-continue path in main.go) so behaviour stays compatible
// with existing contabo paths that may legitimately run without a
// token (read-only deployments).
func ValidateGitHubToken(token string) error {
	if token == "" {
		return nil
	}
	for _, marker := range PlaceholderTokenSubstrings {
		if strings.Contains(token, marker) {
			return fmt.Errorf("GITHUB_TOKEN looks like a placeholder (matches %q) — refusing to start. Provision the real token via the chart's provisioning-github-token Secret seam (issue #940)", marker)
		}
	}
	return nil
}
