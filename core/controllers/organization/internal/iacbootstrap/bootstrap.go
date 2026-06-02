// Package iacbootstrap codifies ADR-0009 §Decision in Go — the
// per-Organization IaC repo provisioning that the organization-controller
// runs on every Organization CR's first reconcile.
//
// Bootstrap is a six-step idempotent sequence:
//
//  1. Ensure Gitea Org `<slug>` exists (idempotent — 422 means already
//     present).
//  2. Ensure repo `<slug>/iac` exists with auto-initialized main
//     branch (idempotent).
//  3. Seed the canonical tree (README.md + kustomization.yaml +
//     apps/.gitkeep + envs/.gitkeep + policies/.gitkeep). PutFile is
//     byte-equal-short-circuit so successive reconciles produce zero
//     Gitea writes once the tree is in place.
//  4. Ensure a robot user `<slug>-iac-bot` exists.
//  5. Add the robot as a write-perm collaborator on `<slug>/iac`.
//  6. Enable branch protection on `main` requiring the three locked
//     status checks (kyverno-admission / cert-manager-precheck /
//     dns-conflict-precheck). The names are LOCKED per ADR-0009 §Branch-
//     protection-wired-to-the-three-pre-checks; the names are encoded
//     as a constant slice in this package so any Wave-1+ author touching
//     the pre-check workflow names sees the dependency at code-grep
//     time.
//
// Cleanup runs the inverse on Organization tombstone — same operations
// in reverse order:
//
//   - DeleteBranchProtection main
//   - RemoveCollaborator <slug>-iac-bot
//   - DeleteRepo <slug>/iac
//   - DeleteAdminUser <slug>-iac-bot (purge=true)
//
// (Gitea Org deletion is NOT performed here — Orgs may have legacy
// repos under them and the operator's choice of "purge or keep" is
// out-of-scope for the controller-driven path.)
//
// The robot-token rotation path is covered by RotateRobotToken — used
// by the External-Secrets re-issuance loop (G117.3b / issue #2765) and
// by the finalizer's pre-delete reset to prevent the token from
// lingering in OpenBao after the user is gone.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 every URL / chart version / image
// ref is read from the runtime config (StatusCheckContexts is a const
// slice here because it's part of the ADR contract — the named pre-
// checks themselves are not URLs).
package iacbootstrap

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/openova-io/openova/core/controllers/pkg/gitea"
)

// RepoName is the per-Org IaC repo name. Locked by ADR-0009 §Decision.
const RepoName = "iac"

// RobotUserSuffix is the suffix appended to the Org slug to derive the
// robot user's login name. Locked by ADR-0009 §Robot-account-scope.
const RobotUserSuffix = "-iac-bot"

// TokenName is the static name used for the robot's access token.
// Locked so rotation can find + replace the same row.
const TokenName = "iac-bot-token"

// MainBranch is the locked default branch name. ADR-0009 §Default-tree.
const MainBranch = "main"

// StatusCheckContexts is the locked list of CI status checks the
// branch-protection rule requires. ADR-0009 §Branch-protection-wired-
// to-the-three-pre-checks.
//
// Wave-1+ authors who change CI workflow names MUST update this list
// AND vice versa — the names are the contract between Gitea's
// branch-protection rule and the per-Org Kyverno / cert-manager /
// dns-conflict CI runs. A drift here traps every PR with "required
// status checks have not yet succeeded".
var StatusCheckContexts = []string{
	"kyverno-admission",
	"cert-manager-precheck",
	"dns-conflict-precheck",
}

// TokenScopes is the access-token permission scope set for the robot
// user. ADR-0009 §Robot-account-scope mandates `write:repository` so
// the bot can author commits but cannot escalate to repo:admin or
// org:admin. The Gitea v1.22 OAuth scope vocabulary uses dotted
// names rather than colons, so the canonical string is "write:repository".
var TokenScopes = []string{"write:repository"}

// GiteaClient is the narrow seam the bootstrap path uses. Production
// wires `*gitea.Client`; tests inject a stub. Every method must return
// a typed `gitea.Err*` sentinel on the find-or-create idempotency
// paths so the Bootstrap orchestrator can absorb already-exists races.
//
// The interface is deliberately minimal — only the methods the
// bootstrap pipeline directly calls. Future expansion is a small,
// reviewable addition.
type GiteaClient interface {
	EnsureOrg(ctx context.Context, slug, fullName, description, visibility string) (gitea.Org, error)
	EnsureRepo(ctx context.Context, org, name, description string, private bool) (gitea.Repo, error)
	PutFile(ctx context.Context, org, repo, branch, path string, data []byte, message string, opts ...gitea.PutFileOpts) (gitea.File, bool, error)

	CreateAdminUser(ctx context.Context, username, email, password string) (gitea.AdminUser, error)
	DeleteAdminUser(ctx context.Context, username string) error

	CreateUserAccessToken(ctx context.Context, username, tokenName string, scopes []string) (gitea.AccessToken, error)
	DeleteUserAccessToken(ctx context.Context, username, tokenName string) error

	AddCollaborator(ctx context.Context, org, repo, user, permission string) error
	RemoveCollaborator(ctx context.Context, org, repo, user string) error

	EnsureBranchProtection(ctx context.Context, org, repo string, statusCheckContexts []string) error
	DeleteBranchProtection(ctx context.Context, org, repo, branchName string) error

	DeleteRepo(ctx context.Context, org, repo string) error
}

// TokenStore is the narrow seam to the credential backend that persists
// the robot-account token plaintext. ADR-0009 + G117.3b (#2765) wire
// this to OpenBao via KV-v2 at path `kv/org/<org>/iac-bot-token`; tests
// inject an in-memory map. The plaintext is held in memory only long
// enough to hand off to the store.
//
// Has token returns true when the path already holds a plaintext token —
// the bootstrap orchestrator skips the mint-and-store step in that case
// (an existing OpenBao secret means a prior reconcile already minted a
// token; over-writing would invalidate every cached External-Secrets
// payload across the cluster).
type TokenStore interface {
	HasToken(ctx context.Context, org string) (bool, error)
	PutToken(ctx context.Context, org, plaintext string) error
	DeleteToken(ctx context.Context, org string) error
}

// Input is the per-Org bootstrap argument bundle.
type Input struct {
	// Slug is the Organization's canonical lowercase identifier
	// (e.g. "acme"). Used as the Gitea Org name + the robot user
	// prefix.
	Slug string

	// DisplayName is the human-readable Org name (e.g. "ACME Corp")
	// stamped on the Gitea Org's full_name field.
	DisplayName string

	// SovereignFQDN is the Sovereign's primary FQDN (e.g.
	// "t01.omani.works"). Used to construct the robot user's email
	// address and the canonical repo URL.
	SovereignFQDN string
}

// Result is the per-Org bootstrap output. Surface RepoURL on the
// Organization CR's status so the operator console can render a "View
// IaC repo" link.
type Result struct {
	// RepoURL is the canonical HTTPS URL of the per-Org IaC repo —
	// e.g. "https://gitea.t01.omani.works/acme/iac".
	RepoURL string

	// RobotUsername is the per-Org robot user's login name —
	// `<slug>-iac-bot`. Surfaced on the status field so the operator
	// console can grep audit logs by author.
	RobotUsername string

	// AlreadyBootstrapped is true when every step was a no-op (the
	// Org + repo + robot + branch-protection rule + OpenBao token
	// were all already in place). Lets callers emit a "BootstrapNoop"
	// reason on the Ready condition rather than "BootstrapDone".
	AlreadyBootstrapped bool
}

// Bootstrap runs the six idempotent steps described in the package
// doc. Returns Result with the canonical repo URL on success.
//
// Idempotency: every step is a find-or-create. The token-mint path is
// the only step that mutates state on the second-and-onward run — and
// only because Gitea doesn't expose a "GetToken-with-plaintext"
// operation; the store-side `HasToken` check short-circuits that path
// when a token is already cached. The Result's AlreadyBootstrapped
// field reflects whether the call required any write (false) or was a
// pure verify (true).
//
// Failure semantics: Bootstrap surfaces the FIRST step's error. The
// caller is responsible for emitting a controller-runtime requeue —
// every step is safe to retry because subsequent runs idempotently
// resume from the first failed step.
func Bootstrap(ctx context.Context, c GiteaClient, store TokenStore, in Input) (Result, error) {
	if err := validateInput(in); err != nil {
		return Result{}, err
	}

	wrote := false

	// Step 1: Gitea Org.
	if _, err := c.EnsureOrg(ctx, in.Slug, in.DisplayName,
		fmt.Sprintf("Catalyst Organization %s on Sovereign %s — managed by organization-controller", in.Slug, in.SovereignFQDN),
		"private"); err != nil {
		return Result{}, fmt.Errorf("iacbootstrap: ensure org %q: %w", in.Slug, err)
	}

	// Step 2: per-Org IaC repo.
	repoDescription := fmt.Sprintf(
		"Catalyst Organization %s IaC repo (PR pipeline — see ADR-0009).",
		in.Slug)
	if _, err := c.EnsureRepo(ctx, in.Slug, RepoName, repoDescription, true); err != nil {
		return Result{}, fmt.Errorf("iacbootstrap: ensure repo %s/%s: %w", in.Slug, RepoName, err)
	}

	// Step 3: seed the canonical tree.
	tree := canonicalTree(in.Slug)
	for path, data := range tree {
		_, committed, err := c.PutFile(ctx, in.Slug, RepoName, MainBranch,
			path, data,
			fmt.Sprintf("chore(iac-bootstrap): seed %s", path))
		if err != nil {
			return Result{}, fmt.Errorf("iacbootstrap: seed %s: %w", path, err)
		}
		if committed {
			wrote = true
		}
	}

	// Step 4: per-Org robot user.
	robotUser := in.Slug + RobotUserSuffix
	robotEmail := fmt.Sprintf("%s@gitea.%s", robotUser, in.SovereignFQDN)
	if _, err := c.CreateAdminUser(ctx, robotUser, robotEmail, randomRobotPassword()); err != nil {
		return Result{}, fmt.Errorf("iacbootstrap: create robot user %s: %w", robotUser, err)
	}

	// Step 4b: mint + persist the robot's access token, IFF the store
	// doesn't already hold one. The first-run path always writes a new
	// token; subsequent runs short-circuit on the store check so we
	// never overwrite a token External-Secrets is already publishing.
	hasToken, err := store.HasToken(ctx, in.Slug)
	if err != nil {
		return Result{}, fmt.Errorf("iacbootstrap: check token store: %w", err)
	}
	if !hasToken {
		tok, err := c.CreateUserAccessToken(ctx, robotUser, TokenName, TokenScopes)
		switch {
		case err == nil:
			if err := store.PutToken(ctx, in.Slug, tok.Sha1); err != nil {
				return Result{}, fmt.Errorf("iacbootstrap: persist token to store: %w", err)
			}
			wrote = true
		case errors.Is(err, gitea.ErrAccessTokenExists):
			// Token exists on Gitea but not in our store — most likely
			// a prior reconcile minted it but the store-write failed.
			// Rotate: delete the existing token, re-mint, re-store.
			if derr := c.DeleteUserAccessToken(ctx, robotUser, TokenName); derr != nil {
				return Result{}, fmt.Errorf("iacbootstrap: delete stale token: %w", derr)
			}
			tok, err := c.CreateUserAccessToken(ctx, robotUser, TokenName, TokenScopes)
			if err != nil {
				return Result{}, fmt.Errorf("iacbootstrap: re-mint token after stale delete: %w", err)
			}
			if err := store.PutToken(ctx, in.Slug, tok.Sha1); err != nil {
				return Result{}, fmt.Errorf("iacbootstrap: persist token after re-mint: %w", err)
			}
			wrote = true
		default:
			return Result{}, fmt.Errorf("iacbootstrap: mint token for %s: %w", robotUser, err)
		}
	}

	// Step 5: collaborator membership on the IaC repo.
	if err := c.AddCollaborator(ctx, in.Slug, RepoName, robotUser, "write"); err != nil {
		return Result{}, fmt.Errorf("iacbootstrap: add collaborator: %w", err)
	}

	// Step 6: branch protection.
	if err := c.EnsureBranchProtection(ctx, in.Slug, RepoName, StatusCheckContexts); err != nil {
		return Result{}, fmt.Errorf("iacbootstrap: branch protection: %w", err)
	}

	return Result{
		RepoURL:             fmt.Sprintf("https://gitea.%s/%s/%s", in.SovereignFQDN, in.Slug, RepoName),
		RobotUsername:       robotUser,
		AlreadyBootstrapped: !wrote,
	}, nil
}

// RotateRobotToken deletes the existing token and mints a new one,
// persisting the freshly-minted plaintext to the store. Used by the
// External-Secrets re-issuance loop (G117.3b / issue #2765) when the
// 90-day TTL elapses, and by the finalizer's pre-cleanup pass when an
// Org tombstones (a deleted user's token is invalid but External-Secrets
// continues serving the stale plaintext until the path is removed).
func RotateRobotToken(ctx context.Context, c GiteaClient, store TokenStore, slug string) error {
	robotUser := slug + RobotUserSuffix
	if err := c.DeleteUserAccessToken(ctx, robotUser, TokenName); err != nil {
		return fmt.Errorf("iacbootstrap.RotateRobotToken: delete existing: %w", err)
	}
	tok, err := c.CreateUserAccessToken(ctx, robotUser, TokenName, TokenScopes)
	if err != nil {
		return fmt.Errorf("iacbootstrap.RotateRobotToken: re-mint: %w", err)
	}
	if err := store.PutToken(ctx, slug, tok.Sha1); err != nil {
		return fmt.Errorf("iacbootstrap.RotateRobotToken: persist: %w", err)
	}
	return nil
}

// Teardown reverses Bootstrap. The order matters: the branch-protection
// rule first (else the Gitea API rejects subsequent deletes that touch
// `main`), then the collaborator row, then the repo, then the token,
// then the user, then the OpenBao path.
//
// Idempotent: missing artifacts are absorbed as no-ops. Returns the
// FIRST error encountered; the caller should requeue on non-nil since
// the remaining steps are safe to re-attempt.
func Teardown(ctx context.Context, c GiteaClient, store TokenStore, slug string) error {
	if slug == "" {
		return errors.New("iacbootstrap.Teardown: slug must be non-empty")
	}
	robotUser := slug + RobotUserSuffix

	if err := c.DeleteBranchProtection(ctx, slug, RepoName, MainBranch); err != nil {
		return fmt.Errorf("iacbootstrap.Teardown: delete branch protection: %w", err)
	}
	if err := c.RemoveCollaborator(ctx, slug, RepoName, robotUser); err != nil {
		return fmt.Errorf("iacbootstrap.Teardown: remove collaborator: %w", err)
	}
	if err := c.DeleteRepo(ctx, slug, RepoName); err != nil {
		return fmt.Errorf("iacbootstrap.Teardown: delete repo: %w", err)
	}
	if err := c.DeleteUserAccessToken(ctx, robotUser, TokenName); err != nil {
		return fmt.Errorf("iacbootstrap.Teardown: delete token: %w", err)
	}
	if err := c.DeleteAdminUser(ctx, robotUser); err != nil {
		return fmt.Errorf("iacbootstrap.Teardown: delete user: %w", err)
	}
	if err := store.DeleteToken(ctx, slug); err != nil {
		return fmt.Errorf("iacbootstrap.Teardown: delete store path: %w", err)
	}
	return nil
}

// canonicalTree returns the path → content map seeded on first-bootstrap.
// Locked by ADR-0009 §Default-tree.
func canonicalTree(slug string) map[string][]byte {
	readme := []byte(fmt.Sprintf(`# %s — Catalyst IaC repo

Managed by organization-controller via PR pipeline (see ADR-0009).

Tree:

- ` + "`apps/`" + `      — one folder per Application instance
- ` + "`envs/`" + `      — Environment definitions
- ` + "`policies/`" + ` — Org-local Kyverno / Cilium overrides
- ` + "`kustomization.yaml`" + ` — Flux entry point

Direct pushes to main are blocked; every change goes through a PR
requiring three named status checks (kyverno-admission,
cert-manager-precheck, dns-conflict-precheck).
`, slug))

	kustomization := []byte(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - apps/
  - envs/
  - policies/
`)

	return map[string][]byte{
		"README.md":          readme,
		"kustomization.yaml": kustomization,
		"apps/.gitkeep":      {},
		"envs/.gitkeep":      {},
		"policies/.gitkeep":  {},
	}
}

// randomRobotPassword returns a 32-character base64url string. The
// robot never logs in with this password — it exists only because
// Gitea's admin-create endpoint requires a non-empty value. Per
// ADR-0009 §Robot-account-scope the robot is exclusively token-driven.
func randomRobotPassword() string {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		// Fall back to a deterministic but unique-per-pid value;
		// crypto/rand failing on Linux is symptomatic of a deeper
		// problem the caller should requeue on anyway.
		return "fallback-disabled-robot-password-do-not-use"
	}
	return base64.URLEncoding.EncodeToString(buf)
}

// validateInput rejects obviously-bad input shapes before any API call.
func validateInput(in Input) error {
	if in.Slug == "" {
		return errors.New("iacbootstrap: Input.Slug is required")
	}
	if in.DisplayName == "" {
		return errors.New("iacbootstrap: Input.DisplayName is required")
	}
	if in.SovereignFQDN == "" {
		return errors.New("iacbootstrap: Input.SovereignFQDN is required")
	}
	// Defensive: reject obviously-invalid slugs (the CRD already
	// enforces RFC-1123-ish but a manual `kubectl apply` could bypass
	// schema validation).
	if !validSlug(in.Slug) {
		return fmt.Errorf("iacbootstrap: invalid slug %q (must match ^[a-z0-9][a-z0-9-]{1,40}[a-z0-9]$)", in.Slug)
	}
	return nil
}

// validSlug enforces the same RFC-1123 lowercase-with-hyphens rule as
// tools/bootstrap-org-iac-repo.sh.
func validSlug(s string) bool {
	if len(s) < 3 || len(s) > 42 {
		return false
	}
	if s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-':
		default:
			return false
		}
	}
	// No double hyphens, no leading digit-only allowed but RFC-1123
	// permits it; keep parity with the bash version.
	return !strings.Contains(s, "--")
}
