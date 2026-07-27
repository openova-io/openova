// Package handler — organization_gitops.go: GitOps overlay writer for
// the Organization tenant provisioning pipeline (issue #804).
//
// Per docs/INVIOLABLE-PRINCIPLES.md #3 the orchestrator NEVER calls
// `kubectl apply`. Every per-tenant resource is materialised by:
//
//  1. Cloning the openova-public GitOps repo to a temp directory.
//  2. Generating the per-tenant Kustomize overlay under
//     clusters/<otech-fqdn>/org-tenants/<org_tenant_id>/.
//  3. git add + commit (committer "catalyst-api <ops@openova.io>").
//  4. git push.
//  5. Flux on the OTECH cluster reconciles within ~1 min and the
//     per-tenant HelmReleases come up.
//
// The overlay materialises every artifact issue #804 specifies:
//
//   - Namespace          org-<org_tenant_id>
//   - HelmRelease       bp-keycloak (per-organization, fresh realm)
//   - HelmRelease       bp-wordpress-tenant
//   - HelmRelease       bp-openclaw
//   - HelmRelease       bp-stalwart-tenant
//   - Certificate       per-host (BYO mode only; free-subdomain is
//     covered by the otech-wide wildcard)
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 every chart version, image
// tag, and HelmRepository ref is supplied via env (the GitOps overlay
// renders them as Go template substitutions); no version is hardcoded
// in the generator.
//
// This file shares the auth + clone primitives with
// marketplace_settings.go (loadGitOpsConfig, injectTokenIntoURL,
// runGit, runGitOutput, redactArgs, redactString) — those helpers
// live there and are reused verbatim.
package handler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// DefaultOrganizationGitOpsWriter is the production OrganizationGitOpsWriter
// implementation. It uses the same gitops env contract as
// marketplace_settings.go (CATALYST_GITOPS_REPO_URL,
// CATALYST_GITOPS_BRANCH, CATALYST_GITOPS_TOKEN, ...). Tests inject
// a stub.
type DefaultOrganizationGitOpsWriter struct {
	Log *slog.Logger
	// ChartVersions maps each bp-* chart slug to the SemVer the
	// generator emits. Per Inviolable Principle 4 these are read from
	// env at startup; the orchestrator caller is expected to provide
	// non-empty values when wiring.
	ChartVersions OrganizationChartVersions
}

// OrganizationChartVersions enumerates the SemVer strings the overlay
// generator emits for each bp-* HelmRelease. The orchestrator's
// wiring (main.go) reads each from env (CATALYST_ORG_BP_KEYCLOAK_VER,
// CATALYST_ORG_BP_CNPG_VER, CATALYST_ORG_BP_WORDPRESS_VER,
// CATALYST_ORG_BP_OPENCLAW_VER, CATALYST_ORG_BP_STALWART_VER,
// CATALYST_ORG_BP_NEWAPI_VER); when any is empty the generator falls
// back to "*" so Flux pulls the latest matching chart in the
// repository.
type OrganizationChartVersions struct {
	Keycloak  string
	CNPG      string
	WordPress string
	OpenClaw  string
	Stalwart  string
	NewAPI    string
	// Agenity — the per-Org agentic dashboard (bp-agenity, #4180). Read
	// from CATALYST_ORG_BP_AGENITY_VER; when empty it falls back to the
	// BOUNDED range agenityChartConstraint (NOT "*"). Unlike every other
	// per-Org chart, the bp-agenity OCI repo was historically polluted by
	// the dashboard IMAGE (tag == appVersion 0.9.x) squatting the CHART
	// repo before #4706 moved the image to ghcr.io/openova-io/agenity — an
	// unbounded "*" resolves the HIGHEST semver tag and wrongly picks that
	// 1-descriptor image over the real chart (#4922). See
	// agenityChartConstraint.
	Agenity string
}

// perOrgGitopsEnabled reports whether this Sovereign materialises an
// Organization through the per-Org GitOps tree (the `<slug>/catalyst-tenant`
// repo the CRD org-controller bootstraps, #4384/#4827) instead of the legacy
// SHARED `clusters/<fqdn>/org-tenants/<id>/` overlay this file writes.
//
// #5425 — the two pipelines coexisted and only ONE of them was gated. Every
// global-tree write in core/services/provisioning/gitops/ sits behind
// `!PerOrgGitops`, but this second, older base-create leg never received the
// switch: WriteTenantOverlay ran unconditionally on every
// POST /api/v1/organizations. On a per-Org-GitOps Sovereign that leaves a
// full 9-resource overlay on the shared path that NOTHING owns once the Org
// is gone — deleting the namespace does not delete the git source, so Flux
// restores the HelmReleases forever. Measured on hw290: two Organizations'
// leftovers = 13 not-Ready items (10 HelmReleases + 3 workloads) holding the
// sovereignty-cutover pre-flight closed, and 6395m of CPU requests on a
// region already at 99-100% of allocatable.
//
// It reads the SAME env key as the provisioning service
// (core/services/provisioning/main.go, the `perOrgGitops` local) so there is
// one switch name across both pipelines, and NOT a second parallel knob:
//
//	TENANT_GITOPS_PER_ORG=true  → per-Org GitOps owns the Org; this leg is a
//	                              no-op and the legacy tree stays empty.
//	TENANT_GITOPS_PER_ORG=false → this leg writes the overlay (legacy mode).
//	unset (the default)         → this leg writes the overlay.
//
// ── Why the DEFAULT deliberately differs from provisioning's ─────────
//
// provisioning defaults the switch ON whenever SOVEREIGN_FQDN is set. This
// leg MUST NOT: per-Org GitOps does not yet emit an equivalent for four of
// the nine resources this overlay carries, and this overlay is their ONLY
// producer in the entire repo (`chart: bp-agenity`, `chart: bp-keycloak` and
// `chart: bp-wordpress-tenant` each resolve to exactly one emitter — the
// templates in this file):
//
//	bp-agenity           — the per-Org Agenity workspace. DoD Pillar 4.
//	bp-keycloak          — the per-Org Keycloak. The per-Org-realm substitute
//	                       in core/controllers/organization (per_org_realm.go)
//	                       is itself default-OFF (CATALYST_PER_ORG_REALM_ENABLED).
//	bp-wordpress-tenant  — the SSO-pre-wired tenant WordPress. The per-Org
//	                       tree installs a raw upstream `wordpress:6-apache`
//	                       Deployment instead (an intentional divergence
//	                       recorded in gitops/helmrelease_apps.go).
//	continuum.yaml       — the per-Application Continuum CR (#2066). Nothing
//	                       in the per-Org tree emits one.
//
// The org-controller's per-Org tree emits only the Namespace, vCluster,
// ResourceQuota, NetworkPolicies, provisioning RBAC and kustomizations
// (internal/gitops/manifests.go) — no bp-* HelmRelease at all. bp-newapi,
// bp-openclaw and bp-stalwart-tenant do have per-Org emitters, but only when
// the Org has purchased them; this overlay emits them for every Org.
//
// So defaulting ON here would silently strip the per-Org application stack
// from every Organization on every Sovereign — the exact "gating alone
// silently drops resources" failure. The switch is therefore EXPLICIT
// opt-in: the mechanism is in place and guarded, and flipping it to `true`
// becomes correct once the four gaps above are closed in the per-Org tree.
// Deliberately not wired into the catalyst-api Deployment yet for the same
// reason — the chart's existing `perOrgGitops` value defaults to "true" on a
// Sovereign, so wiring it today would enable exactly the drop described here.
//
// Per Inviolable Principle #4 the key is read from env, never hardcoded.
func perOrgGitopsEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("TENANT_GITOPS_PER_ORG")), "true")
}

// WriteTenantOverlay implements OrganizationGitOpsWriter. Returns the
// commit SHA on success.
//
// #5425 — no-ops (empty SHA, nil error) when per-Org GitOps owns the Org.
// The gate is the FIRST statement so no clone, no scratch directory and no
// commit is attempted: the guarantee is zero writes under the legacy overlay
// root, not "writes that are later cleaned up". The empty SHA is only
// surfaced cosmetically (store.OrganizationProvisionRecord.CommitSHA is
// `omitempty` and no step gates on it), and the caller advances the pipeline
// exactly as it does for a real commit — on a per-Org-GitOps Sovereign the
// Org's base stack is materialised by the CRD org-controller, not here.
func (w DefaultOrganizationGitOpsWriter) WriteTenantOverlay(ctx context.Context, rec store.OrganizationProvisionRecord) (string, error) {
	if perOrgGitopsEnabled() {
		if w.Log != nil {
			w.Log.Info("org-tenant: per-Org GitOps owns this Organization — legacy org-tenants overlay write skipped (#5425)",
				"org", rec.OrganizationID, "subdomain", rec.Subdomain, "sovereign", rec.OTECHFQDN)
		}
		return "", nil
	}
	cfg := loadGitOpsConfig()
	if cfg.Token == "" {
		return "", errors.New("gitops token unconfigured — set CATALYST_GITOPS_TOKEN")
	}
	scratch, err := os.MkdirTemp(envOr("CATALYST_GITOPS_TMPDIR", os.TempDir()), "org-tenant-overlay-*")
	if err != nil {
		return "", fmt.Errorf("mktempdir: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(scratch); err != nil && w.Log != nil {
			w.Log.Warn("org-tenant: scratch cleanup failed", "dir", scratch, "err", err)
		}
	}()

	authURL, err := injectTokenIntoURLWithUser(cfg.RepoURL, cfg.User, cfg.Token)
	if err != nil {
		return "", fmt.Errorf("rewrite repo URL: %w", err)
	}
	repoDir := filepath.Join(scratch, "repo")
	if err := runGit(ctx, scratch, "clone",
		"--depth=1",
		"--branch="+cfg.Branch,
		"--single-branch",
		authURL,
		repoDir,
	); err != nil {
		return "", fmt.Errorf("git clone: %w", err)
	}

	if err := runGit(ctx, repoDir, "config", "user.name", cfg.CommitterName); err != nil {
		return "", fmt.Errorf("git config user.name: %w", err)
	}
	if err := runGit(ctx, repoDir, "config", "user.email", cfg.CommitterMail); err != nil {
		return "", fmt.Errorf("git config user.email: %w", err)
	}

	// Per-tenant overlay path:
	//   clusters/<otech-fqdn>/org-tenants/<org_tenant_id>/
	overlayDir := filepath.Join(repoDir, "clusters", rec.OTECHFQDN, "org-tenants", rec.OrganizationID)
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir overlay: %w", err)
	}

	files, err := renderOrganizationOverlay(rec, w.ChartVersions)
	if err != nil {
		return "", fmt.Errorf("render overlay: %w", err)
	}
	for name, contents := range files {
		// #2066 — templates that fully evaluate to whitespace (e.g.
		// continuum.yaml when EnableHotStandby=false) are skipped so
		// the overlay directory only contains live resources. Avoids
		// committing empty-stub files that confuse kustomize and
		// reviewers alike.
		if strings.TrimSpace(contents) == "" {
			continue
		}
		path := filepath.Join(overlayDir, name)
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			return "", fmt.Errorf("write %s: %w", name, err)
		}
	}

	relRoot := filepath.Join("clusters", rec.OTECHFQDN, "org-tenants", rec.OrganizationID)
	if err := runGit(ctx, repoDir, "add", relRoot); err != nil {
		return "", fmt.Errorf("git add: %w", err)
	}

	// Issue #889 — Flux Kustomization at clusters/<fqdn>/org-tenants/
	// requires a parent kustomization.yaml that enumerates the tenant
	// subdirectories. Regenerate it after every Write so the index is
	// always current. Without this, Flux fails with "kustomization path
	// not found" on a fresh Sovereign that has never had a tenant.
	parentDir := filepath.Join(repoDir, "clusters", rec.OTECHFQDN, "org-tenants")
	if err := writeParentTenantsIndex(parentDir); err != nil {
		return "", fmt.Errorf("write parent index: %w", err)
	}
	parentRel := filepath.Join("clusters", rec.OTECHFQDN, "org-tenants", "kustomization.yaml")
	if err := runGit(ctx, repoDir, "add", parentRel); err != nil {
		return "", fmt.Errorf("git add parent index: %w", err)
	}
	parentHRRel := filepath.Join("clusters", rec.OTECHFQDN, "org-tenants", "helmrepositories.yaml")
	if err := runGit(ctx, repoDir, "add", parentHRRel); err != nil {
		return "", fmt.Errorf("git add parent helmrepositories: %w", err)
	}

	msg := fmt.Sprintf("org-tenant: provision %s (%s) on %s",
		rec.Subdomain, rec.OrganizationID, rec.OTECHFQDN)
	// Allow-empty so a re-run that produces identical bytes still
	// succeeds (Flux is idempotent; we don't punish the operator with
	// an error when the manifest didn't drift).
	if err := runGit(ctx, repoDir, "commit", "--allow-empty", "-m", msg); err != nil {
		return "", fmt.Errorf("git commit: %w", err)
	}

	if err := runGit(ctx, repoDir, "push", "origin", "HEAD:"+cfg.Branch); err != nil {
		return "", fmt.Errorf("git push: %w", err)
	}

	out, err := runGitOutput(ctx, repoDir, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("rev-parse HEAD: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// writeParentTenantsIndex (re)generates the parent
// clusters/<fqdn>/org-tenants/kustomization.yaml index file. The file
// lists every immediate subdirectory that contains a kustomization.yaml
// of its own. Sorted lexically for deterministic output (no spurious
// diffs when the orchestrator re-runs).
//
// Per Inviolable Principle #4 (never hardcode), the file format is the
// canonical kustomize.config.k8s.io/v1beta1 Kustomization. Per #3, this
// is a pure file write — Flux owns the apply.
//
// Issue #889, 2026-05-05.
func writeParentTenantsIndex(parentDir string) error {
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		return fmt.Errorf("mkdir parent: %w", err)
	}
	entries, err := os.ReadDir(parentDir)
	if err != nil {
		return fmt.Errorf("readdir parent: %w", err)
	}
	subs := []string{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Only include subdirectories that themselves contain a
		// kustomization.yaml — defends against partial writes that
		// would otherwise produce a parent index referencing a
		// non-existent kustomize root.
		if _, err := os.Stat(filepath.Join(parentDir, e.Name(), "kustomization.yaml")); err == nil {
			subs = append(subs, e.Name())
		}
	}
	// Sort lexically — deterministic order = no spurious diffs across
	// orchestrator runs.
	sortedSubs := append([]string(nil), subs...)
	for i := 1; i < len(sortedSubs); i++ {
		for j := i; j > 0 && sortedSubs[j] < sortedSubs[j-1]; j-- {
			sortedSubs[j], sortedSubs[j-1] = sortedSubs[j-1], sortedSubs[j]
		}
	}
	// Also emit the shared HelmRepositories file (#893) — the Organization
	// charts (bp-keycloak / bp-wordpress-tenant / bp-openclaw /
	// bp-stalwart-tenant + vcluster's loft repo) are NOT shipped by the
	// bootstrap-kit on a Sovereign by default. The orchestrator emits them
	// here at the parent level so every per-tenant HR has a valid
	// sourceRef. Write-once-per-tenant-signup but bytes are stable — Flux
	// dedupes on name. helmrepositories.yaml is added to the parent
	// kustomization.yaml's resources list FIRST so source-controller
	// reconciles them before any tenant HelmChart is requested.
	hrFileName := "helmrepositories.yaml"
	hrFilePath := filepath.Join(parentDir, hrFileName)
	if err := os.WriteFile(hrFilePath, []byte(orgTenantSharedHelmRepositories), 0o644); err != nil {
		return fmt.Errorf("write shared helmrepositories: %w", err)
	}

	var b bytes.Buffer
	b.WriteString("# Generated by catalyst-api/org-tenant pipeline (#804/#889/#893).\n")
	b.WriteString("# DO NOT EDIT — re-run the orchestrator on tenant signup/teardown\n")
	b.WriteString("# to regenerate. Lists every per-tenant overlay subdirectory\n")
	b.WriteString("# under this path so the parent Flux Kustomization\n")
	b.WriteString("# (rendered by bp-catalyst-platform) can enumerate them.\n")
	b.WriteString("# helmrepositories.yaml ships the shared bp-* HelmRepositories\n")
	b.WriteString("# the per-tenant overlays sourceRef into.\n")
	b.WriteString("apiVersion: kustomize.config.k8s.io/v1beta1\n")
	b.WriteString("kind: Kustomization\n")
	b.WriteString("resources:\n")
	b.WriteString("  - ")
	b.WriteString(hrFileName)
	b.WriteString("\n")
	for _, s := range sortedSubs {
		b.WriteString("  - ")
		b.WriteString(s)
		b.WriteString("\n")
	}
	indexPath := filepath.Join(parentDir, "kustomization.yaml")
	if err := os.WriteFile(indexPath, b.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write index: %w", err)
	}
	return nil
}

// orgTenantSharedHelmRepositories is the canonical HelmRepository
// declarations the Organization tenant overlays sourceRef into. Issue #893 —
// these were missing on Sovereigns because the bootstrap-kit doesn't
// ship them (they're tenant-mode-only charts). Emitted once at the
// parent level, shared by every tenant.
//
// Per Inviolable Principle #4 (never hardcode), the URL is the same
// canonical oci://ghcr.io/openova-io that every other bp-* chart pulls
// from. Sovereigns post-cutover have a registries.yaml that rewrites
// pulls through their local Harbor proxy-cache, so this works
// transparently for both pre- and post-cutover phases.
//
// secretRef: ghcr-pull is the canonical name written by cloud-init at
// /var/lib/catalyst/ghcr-pull-secret.yaml and reflected into every
// namespace via bp-reflector (issue #543).
//
// Every chart the orgTenantTemplates overlay sourceRefs into MUST have a
// HelmRepository block here, or its rendered HelmRelease fails with
// `HelmRepository <name> not found` on a fresh per-Org prov. bp-agenity
// was the gap (#4180/#4239): the overlay HR pinned
// sourceRef: HelmRepository/bp-agenity but no block declared it, so a
// fresh Org only resolved when the HelmRepository was hand-applied
// (the forbidden kubectl patch). Refs #4180.
const orgTenantSharedHelmRepositories = `# Generated by catalyst-api/org-tenant pipeline (#893).
# Shared HelmRepositories the per-tenant overlays sourceRef into.
# DO NOT EDIT — re-run the orchestrator to regenerate.
---
apiVersion: source.toolkit.fluxcd.io/v1beta2
kind: HelmRepository
metadata:
  name: bp-keycloak
  namespace: flux-system
spec:
  type: oci
  interval: 15m
  url: oci://ghcr.io/openova-io
  secretRef:
    name: ghcr-pull
---
apiVersion: source.toolkit.fluxcd.io/v1beta2
kind: HelmRepository
metadata:
  name: bp-newapi
  namespace: flux-system
spec:
  type: oci
  interval: 15m
  url: oci://ghcr.io/openova-io
  secretRef:
    name: ghcr-pull
---
apiVersion: source.toolkit.fluxcd.io/v1beta2
kind: HelmRepository
metadata:
  name: bp-wordpress-tenant
  namespace: flux-system
spec:
  type: oci
  interval: 15m
  url: oci://ghcr.io/openova-io
  secretRef:
    name: ghcr-pull
---
apiVersion: source.toolkit.fluxcd.io/v1beta2
kind: HelmRepository
metadata:
  name: bp-openclaw
  namespace: flux-system
spec:
  type: oci
  interval: 15m
  url: oci://ghcr.io/openova-io
  secretRef:
    name: ghcr-pull
---
apiVersion: source.toolkit.fluxcd.io/v1beta2
kind: HelmRepository
metadata:
  name: bp-stalwart-tenant
  namespace: flux-system
spec:
  type: oci
  interval: 15m
  url: oci://ghcr.io/openova-io
  secretRef:
    name: ghcr-pull
---
apiVersion: source.toolkit.fluxcd.io/v1beta2
kind: HelmRepository
metadata:
  name: bp-agenity
  namespace: flux-system
spec:
  type: oci
  interval: 15m
  url: oci://ghcr.io/openova-io
  secretRef:
    name: ghcr-pull
`
// NOTE — the `loft` (https://charts.loft.sh) HelmRepository was removed
// from this shared set in #4188 along with the legacy vc-<subdomain>
// overlay. The per-Org vCluster (chart vcluster@0.33.x) is owned by the
// CRD org-controller, which sources from its own `loft`/vcluster-system
// HelmRepository (Harbor-mirrored). The org-tenant overlay no longer
// pulls any chart from loft, so this entry was dead weight + the only
// place this path referenced the upstream rancher/k3s distro.

// DeleteTenantOverlay implements OrganizationGitOpsWriter. Removes the
// per-tenant overlay directory. Idempotent — a missing path commits
// an empty change with `--allow-empty`.
//
// #5425 — gated on the SAME switch as WriteTenantOverlay, for a reason that
// is easy to miss: this leg is not write-free. Even when the per-tenant
// directory does not exist, it calls writeParentTenantsIndex, which
// (re)creates `clusters/<fqdn>/org-tenants/kustomization.yaml` AND
// `helmrepositories.yaml` — materialising the legacy tree, complete with the
// six shared bp-* HelmRepositories, on a Sovereign whose whole invariant is
// that the tree stays empty. Teardown of an Org that this leg never wrote
// has nothing to remove, so skipping is also the correct semantic: the
// per-Org tree's own teardown owns it.
//
// Consequence to know: flipping a Sovereign from legacy to per-Org GitOps
// mid-life orphans any overlay written before the flip. Flipping
// TENANT_GITOPS_PER_ORG back to `false` restores this leg and lets the
// normal teardown reap them.
func (w DefaultOrganizationGitOpsWriter) DeleteTenantOverlay(ctx context.Context, rec store.OrganizationProvisionRecord) (string, error) {
	if perOrgGitopsEnabled() {
		if w.Log != nil {
			w.Log.Info("org-tenant: per-Org GitOps owns this Organization — legacy org-tenants overlay teardown skipped (#5425)",
				"org", rec.OrganizationID, "subdomain", rec.Subdomain, "sovereign", rec.OTECHFQDN)
		}
		return "", nil
	}
	cfg := loadGitOpsConfig()
	if cfg.Token == "" {
		return "", errors.New("gitops token unconfigured — set CATALYST_GITOPS_TOKEN")
	}
	scratch, err := os.MkdirTemp(envOr("CATALYST_GITOPS_TMPDIR", os.TempDir()), "org-tenant-delete-*")
	if err != nil {
		return "", fmt.Errorf("mktempdir: %w", err)
	}
	defer os.RemoveAll(scratch)

	authURL, err := injectTokenIntoURLWithUser(cfg.RepoURL, cfg.User, cfg.Token)
	if err != nil {
		return "", fmt.Errorf("rewrite repo URL: %w", err)
	}
	repoDir := filepath.Join(scratch, "repo")
	if err := runGit(ctx, scratch, "clone",
		"--depth=1",
		"--branch="+cfg.Branch,
		"--single-branch",
		authURL,
		repoDir,
	); err != nil {
		return "", fmt.Errorf("git clone: %w", err)
	}
	if err := runGit(ctx, repoDir, "config", "user.name", cfg.CommitterName); err != nil {
		return "", fmt.Errorf("git config user.name: %w", err)
	}
	if err := runGit(ctx, repoDir, "config", "user.email", cfg.CommitterMail); err != nil {
		return "", fmt.Errorf("git config user.email: %w", err)
	}
	overlayDir := filepath.Join(repoDir, "clusters", rec.OTECHFQDN, "org-tenants", rec.OrganizationID)
	if err := os.RemoveAll(overlayDir); err != nil {
		return "", fmt.Errorf("remove overlay: %w", err)
	}
	relRoot := filepath.Join("clusters", rec.OTECHFQDN, "org-tenants", rec.OrganizationID)
	// `git add -A <path>` records the deletions.
	if err := runGit(ctx, repoDir, "add", "-A", relRoot); err != nil {
		return "", fmt.Errorf("git add: %w", err)
	}

	// Issue #889 — regenerate the parent kustomization.yaml index after
	// removing the tenant subdir, so Flux's Kustomization sees the
	// reduced resources list. If no tenants remain, the parent index is
	// rewritten with `resources: []` (still a valid Kustomization root).
	parentDir := filepath.Join(repoDir, "clusters", rec.OTECHFQDN, "org-tenants")
	if err := writeParentTenantsIndex(parentDir); err != nil {
		return "", fmt.Errorf("write parent index: %w", err)
	}
	parentRel := filepath.Join("clusters", rec.OTECHFQDN, "org-tenants", "kustomization.yaml")
	if err := runGit(ctx, repoDir, "add", parentRel); err != nil {
		return "", fmt.Errorf("git add parent index: %w", err)
	}
	parentHRRel := filepath.Join("clusters", rec.OTECHFQDN, "org-tenants", "helmrepositories.yaml")
	if err := runGit(ctx, repoDir, "add", parentHRRel); err != nil {
		return "", fmt.Errorf("git add parent helmrepositories: %w", err)
	}

	msg := fmt.Sprintf("org-tenant: tear down %s (%s) on %s",
		rec.Subdomain, rec.OrganizationID, rec.OTECHFQDN)
	if err := runGit(ctx, repoDir, "commit", "--allow-empty", "-m", msg); err != nil {
		return "", fmt.Errorf("git commit: %w", err)
	}
	if err := runGit(ctx, repoDir, "push", "origin", "HEAD:"+cfg.Branch); err != nil {
		return "", fmt.Errorf("git push: %w", err)
	}
	out, err := runGitOutput(ctx, repoDir, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("rev-parse HEAD: %w", err)
	}
	return strings.TrimSpace(out), nil
}

/* ── overlay rendering ───────────────────────────────────────────── */

// orgTenantTemplateData is the input the rendering templates consume.
type orgTenantTemplateData struct {
	TenantID     string
	Subdomain    string
	Namespace string
	// VClusterName — retained for the store record + back-compat, but as
	// of #4188 NO overlay template renders it. The per-Org vCluster is
	// owned by the CRD org-controller (chart vcluster@0.33.x, ns
	// <subdomain>); this path stopped emitting the legacy
	// vc-<subdomain>@0.19.x duplicate. Do NOT wire a new template to it.
	VClusterName string
	OTECHFQDN    string
	// ParentDomain — the chosen org-pool parent (multi-domain
	// Sovereign per epic #825). Falls back to OTECHFQDN for
	// single-domain back-compat. The console/wordpress/openclaw/
	// mail/keycloak hosts are all derived from this zone.
	ParentDomain  string
	ConsoleHost   string
	WordPressHost string
	OpenClawHost  string
	MailHost      string
	// AgenityHost — the per-Org agentic dashboard host
	// (agenity.<slug>.<pool>), derived from ConsoleHost by swapping the
	// `console.` prefix. Drives bp-agenity's httpRoute.hostnames so the
	// chart attaches the route to cilium-gateway-console (#4180).
	AgenityHost string
	AdminEmail    string
	CompanyName   string
	DomainMode    string
	BYODomain     string
	IsBYO         bool
	ChartVersions OrganizationChartVersions
	GeneratedAt   string

	// VClusterImageRegistry is the Sovereign-local Harbor host the
	// per-tenant app images pull THROUGH (proxy-cache). Default
	// "harbor.openova.io".
	//
	// MIRROR-EVERYTHING (#3785, follow-up to #3761, Refs #3376): the
	// bp-wordpress-tenant chart's main + wp-cli images default to the
	// Docker Hub `wordpress` repository (platform/wordpress-tenant/chart/
	// values.yaml). On a kyverno-Enforce Sovereign the `harbor-proxy-pull`
	// ClusterPolicy DENIES any image not matching the `*/proxy-*/*` glob —
	// so a raw `wordpress:6-php8.3-apache` pull is blocked and the
	// customer's purchased app never starts (the funnel's terminal
	// acceptance). #3761 re-tagged the per-Org vCluster images but left the
	// APP images raw; this field closes that gap by feeding
	// `global.imageRegistry: <registry>/proxy-dockerhub` into the WordPress
	// HelmRelease so BOTH WordPress images route through the Sovereign
	// Harbor proxy-cache, exactly like the CNPG image the chart already
	// proxies through `<registry>/proxy-ghcr`. Per Inviolable Principle #4
	// it's read from env (CATALYST_VCLUSTER_IMAGE_REGISTRY — the same knob
	// the org-controller uses), never hardcoded; cutover Step-04 (ADR-0002)
	// flips it to harbor.<sovereign-fqdn> post-handover.
	VClusterImageRegistry string

	// D31 active-hot-standby — opt-in cross-region CNPG ReplicaCluster
	// for CNPG-backed tenant apps. The bp-wordpress-tenant chart
	// (platform/wordpress-tenant/chart/templates/cnpg-cluster.yaml,
	// PR #1562) already supports a `pg.activeHotStandby.{enabled,
	// primaryRegion,replicaRegion}` block: when enabled it renders a
	// primary + replica `Cluster.postgresql.cnpg.io` pair pinned to two
	// distinct openova.io/region values, with WAL streaming over
	// Cilium ClusterMesh via the `service.cilium.io/global` Service the
	// CNPG operator provisions on the primary. Single-cluster
	// (legacy) shape stays the default for every tenant that hasn't
	// opted in.
	//
	// Source: the trio is sourced from the catalyst-api Pod env at
	// render time (env keys: SOVEREIGN_ENABLE_HOT_STANDBY,
	// SOVEREIGN_PRIMARY_REGION, SOVEREIGN_REPLICA_REGION). Bootstrap-
	// kit slot 13 (clusters/_template/bootstrap-kit/13-bp-catalyst-
	// platform.yaml) wires those envsubst placeholders from
	// per-Sovereign overlays so an operator flips one knob and every
	// future tenant install gets the HA shape.
	//
	// Default behaviour (EnableHotStandby=false): the rendered
	// HelmRelease omits the `pg.activeHotStandby` block entirely so the
	// chart falls back to its values.yaml default (enabled=false). Zero
	// regression for any Sovereign that has not opted in.
	//
	// When EnableHotStandby=true at render time we additionally enforce:
	//   - PrimaryRegion non-empty
	//   - ReplicaRegion non-empty AND distinct from PrimaryRegion
	// If any of those fail we fall back to single-cluster shape rather
	// than emitting a HelmRelease the chart's
	// `validateActiveHotStandbyRegions` helper would `fail` at template
	// time — a failed template render blocks the entire tenant overlay
	// reconcile, which is far worse than degrading silently to non-HA.
	// Same shape applies to any future tenant product chart (gitlab-
	// tenant, nextcloud-tenant) that adopts the same value contract;
	// today those charts do not exist in this monorepo so this wiring
	// is exercised only by bp-wordpress-tenant.
	EnableHotStandby bool
	PrimaryRegion    string
	ReplicaRegion    string
}

// renderOrganizationOverlay turns a record into a map<filename, contents>
// for the orchestrator to commit. Returned filenames are relative to
// the per-tenant overlay directory.
func renderOrganizationOverlay(rec store.OrganizationProvisionRecord, versions OrganizationChartVersions) (map[string]string, error) {
	if strings.TrimSpace(rec.OrganizationID) == "" {
		return nil, errors.New("render: org_tenant_id required")
	}
	if strings.TrimSpace(rec.OTECHFQDN) == "" {
		return nil, errors.New("render: otech fqdn required")
	}
	if strings.TrimSpace(rec.Subdomain) == "" {
		return nil, errors.New("render: subdomain required")
	}
	versions = withVersionDefaults(versions)
	// Multi-domain Sovereign (#825): the chosen org-pool parent zone
	// drives every derived host. Falls back to OTECHFQDN for single-
	// domain back-compat (#804).
	parentZone := strings.TrimSpace(rec.ParentDomain)
	if parentZone == "" {
		parentZone = rec.OTECHFQDN
	}
	host := ""
	if rec.DomainMode == store.OrganizationDomainBYO {
		host = "console." + rec.BYODomain
	} else {
		host = "console." + rec.Subdomain + "." + parentZone
	}
	wpHost := strings.Replace(host, "console.", "wordpress.", 1)
	owHost := strings.Replace(host, "console.", "openclaw.", 1)
	mailHost := strings.Replace(host, "console.", "mail.", 1)
	agenityHost := strings.Replace(host, "console.", "agenity.", 1)

	// D31 active-hot-standby — read the Sovereign-level toggle + region
	// pair from the catalyst-api Pod env at render time. Wired by
	// bootstrap-kit slot 13 (clusters/_template/bootstrap-kit/13-bp-
	// catalyst-platform.yaml) from envsubst placeholders the operator
	// sets at provision time. Default-off keeps every existing tenant
	// rendering single-cluster CNPG (no regression).
	enableHotStandby := false
	switch strings.ToLower(strings.TrimSpace(envOr("SOVEREIGN_ENABLE_HOT_STANDBY", ""))) {
	case "true", "1", "yes", "on":
		enableHotStandby = true
	}
	primaryRegion := strings.TrimSpace(envOr("SOVEREIGN_PRIMARY_REGION", ""))
	replicaRegion := strings.TrimSpace(envOr("SOVEREIGN_REPLICA_REGION", ""))
	// Defence-in-depth: if the operator opted in to HA but didn't wire
	// distinct regions, fall back to single-cluster mode rather than
	// rendering a HelmRelease the WordPress chart's
	// validateActiveHotStandbyRegions helper would reject at template
	// time (which would block the entire tenant overlay reconcile).
	if enableHotStandby && (primaryRegion == "" || replicaRegion == "" || primaryRegion == replicaRegion) {
		enableHotStandby = false
	}

	// MIRROR-EVERYTHING (#3785): the Sovereign-local Harbor proxy host every
	// per-tenant app image routes through. Same env knob + default the
	// org-controller uses for the vCluster image (CATALYST_VCLUSTER_IMAGE_
	// REGISTRY, default harbor.openova.io); cutover Step-04 flips it to
	// harbor.<sovereign-fqdn> post-handover (ADR-0002). Read here so the
	// WordPress HelmRelease's `global.imageRegistry` is never hardcoded
	// (Inviolable Principle #4).
	imageRegistry := strings.TrimSpace(envOr("CATALYST_VCLUSTER_IMAGE_REGISTRY", "harbor.openova.io"))

	data := orgTenantTemplateData{
		TenantID:              rec.OrganizationID,
		Subdomain:             rec.Subdomain,
		Namespace:             rec.TenantNamespace,
		VClusterName:          rec.VClusterName,
		OTECHFQDN:             rec.OTECHFQDN,
		ParentDomain:          parentZone,
		ConsoleHost:           host,
		WordPressHost:         wpHost,
		OpenClawHost:          owHost,
		MailHost:              mailHost,
		AgenityHost:           agenityHost,
		AdminEmail:            rec.AdminEmail,
		CompanyName:           rec.CompanyName,
		DomainMode:            string(rec.DomainMode),
		BYODomain:             rec.BYODomain,
		IsBYO:                 rec.DomainMode == store.OrganizationDomainBYO,
		ChartVersions:         versions,
		GeneratedAt:           time.Now().UTC().Format(time.RFC3339),
		VClusterImageRegistry: imageRegistry,
		EnableHotStandby:      enableHotStandby,
		PrimaryRegion:         primaryRegion,
		ReplicaRegion:         replicaRegion,
	}

	out := map[string]string{}
	for name, tpl := range orgTenantTemplates {
		var buf bytes.Buffer
		t, err := template.New(name).Parse(tpl)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		if err := t.Execute(&buf, data); err != nil {
			return nil, fmt.Errorf("execute %s: %w", name, err)
		}
		out[name] = buf.String()
	}
	return out, nil
}

// agenityChartConstraint bounds the per-Org bp-agenity chart resolution to
// the chart's current 0.5.x minor line instead of an unbounded "*". The
// bp-agenity OCI repo was historically polluted: the dashboard IMAGE (tag ==
// appVersion, e.g. 0.9.7) was pushed to the CHART repo before #4706-family
// moved the image to ghcr.io/openova-io/agenity. A "*" pin resolves the
// HIGHEST semver tag, so a lingering 0.9.7 image manifest (a 1-descriptor
// Docker image) out-ranks the real 0.5.20 chart (a 2-descriptor Helm artifact)
// and Flux dies with `manifest does not contain minimum number of descriptors
// (2), found: 1` — bp-agenity never installs (#4922, UAT rows 218/219; the
// same failure the #4706 Chart.yaml note describes). Bounding to <0.6.0 makes
// the resolution DETERMINISTIC — a stray high-semver image tag can never be
// picked, even before the squatting tags are pruned from ghcr.
// Lockstep: bump this upper bound when the bp-agenity chart crosses a minor
// (0.6.0), alongside products/agenity/blueprint.yaml spec.version + the
// catalog-seed source.version.
const agenityChartConstraint = ">=0.5.0 <0.6.0"

func withVersionDefaults(v OrganizationChartVersions) OrganizationChartVersions {
	star := func(s string) string {
		if strings.TrimSpace(s) == "" {
			return "*"
		}
		return s
	}
	// bounded falls back to a SemVer RANGE (not "*") for charts whose OCI
	// repo can carry a higher-semver NON-chart tag that "*" would wrongly
	// resolve. An explicit env-provided version still wins verbatim.
	bounded := func(s, fallback string) string {
		if strings.TrimSpace(s) == "" {
			return fallback
		}
		return s
	}
	return OrganizationChartVersions{
		Keycloak:  star(v.Keycloak),
		CNPG:      star(v.CNPG),
		WordPress: star(v.WordPress),
		OpenClaw:  star(v.OpenClaw),
		Stalwart:  star(v.Stalwart),
		NewAPI:    star(v.NewAPI),
		Agenity:   bounded(v.Agenity, agenityChartConstraint),
	}
}

// orgTenantTemplates is the canonical map of overlay files. Adding a
// new chart = a new entry here. Each template renders a single YAML
// document; kustomization.yaml lists all of them so Flux materialises
// them in topological order.
// NOTE — the per-Org vCluster is intentionally NOT rendered here. The
// canonical per-Organization vCluster backing is owned by the CRD
// org-controller (core/controllers/organization/internal/gitops/
// manifests.go → HelmRelease `vcluster`, chart vcluster@0.33.x, loft
// vcluster-oss distro, Harbor-mirrored images, ns `<subdomain>`). The
// legacy bootstrap-api path used to emit a SECOND vcluster HelmRelease
// (`vc-<subdomain>`, chart 0.19.x, raw `rancher/k3s` image) into the
// org-tenant overlay namespace. Nothing in the overlay consumed it (the
// per-Org app HelmReleases install directly into the host org-tenant
// namespace — none set kubeConfig/dependsOn against it), yet `rancher/
// k3s` is not Harbor-mirrored so its Pod sat in Init:ImagePullBackOff
// forever, the `vc-<subdomain>` HelmRelease stayed False on the spine,
// and it was a fake-green trap for verifiers reading it as THE backing.
//
// Dropping the entry here is the idempotency fix (#4188): because the
// org-tenants Flux Kustomization runs with prune=true, removing the
// resource from the rendered overlay garbage-collects the stale
// `vc-<subdomain>` HelmRelease on the next reconcile, AND a re-render of
// any existing Org never re-creates the duplicate. Refs #4188.
var orgTenantTemplates = map[string]string{
	"kustomization.yaml":       orgTenantKustomization,
	"namespace.yaml":           orgTenantNamespace,
	"bp-keycloak.yaml":         orgTenantBPKeycloak,
	"bp-newapi.yaml":           orgTenantBPNewAPI,
	"bp-wordpress-tenant.yaml": orgTenantBPWordPress,
	"bp-openclaw.yaml":         orgTenantBPOpenClaw,
	"bp-stalwart-tenant.yaml":  orgTenantBPStalwart,
	"bp-agenity.yaml":          orgTenantBPAgenity,
	"certificate.yaml":         orgTenantCertificate,
	// #2066 — per-Application Continuum CR. The template evaluates to
	// the empty string when EnableHotStandby=false; the writer in
	// WriteTenantOverlay skips empty contents so the file never lands
	// in the overlay directory for single-cluster tenants.
	"continuum.yaml": orgTenantContinuum,
}

const orgTenantKustomization = `# Generated at {{.GeneratedAt}} by catalyst-api/org-tenant pipeline (#804).
# DO NOT EDIT — re-run the orchestrator to regenerate.
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  # The per-Org vCluster is owned by the CRD org-controller (chart
  # vcluster@0.33.x, ns <subdomain>), NOT this overlay. The legacy
  # vc-<subdomain> (0.19.x / rancher-k3s) HelmRelease was removed in
  # #4188 — leaving it here re-created a stale ImagePullBackOff duplicate
  # on every render. prune=true on the org-tenants Kustomization reaps
  # the old one.
  - namespace.yaml
  - bp-keycloak.yaml
  - bp-newapi.yaml
  - bp-wordpress-tenant.yaml
  - bp-openclaw.yaml
  - bp-stalwart-tenant.yaml
  - bp-agenity.yaml
  - certificate.yaml
{{- if .EnableHotStandby }}
  # D31 / Pillar 3 — per-Application Continuum CR (Refs #2066) that
  # bp-continuum (Refs #2065) reconciles against. Only included when
  # active-hot-standby is on AND both regions are distinct (validated
  # in renderOrganizationOverlay before this template fires).
  - continuum.yaml
{{- end }}
commonLabels:
  catalyst.openova.io/org-tenant: {{.TenantID}}
  catalyst.openova.io/org-subdomain: {{.Subdomain}}
`

const orgTenantNamespace = `apiVersion: v1
kind: Namespace
metadata:
  name: {{.Namespace}}
  labels:
    catalyst.openova.io/org-tenant: {{.TenantID}}
    catalyst.openova.io/org-subdomain: {{.Subdomain}}
    catalyst.openova.io/managed-by: catalyst-api
    # openova.io/organization — the canonical per-Org join key
    # (ARCHITECTURE.md §org-attribution). The CRD org-controller stamps it
    # via core/controllers/organization/internal/gitops/manifests.go; the
    # bootstrap-api org-tenant pipeline MUST stamp the same label so the
    # two provisioning paths produce identical namespace shape. CRITICAL:
    # the harbor-proxy-pull / customer-vCluster Kyverno exemptions
    # (platform/kyverno-policies/.../11-harbor-proxy.yaml, #3859) key off an
    # openova.io/organization namespaceSelector — without this label the
    # per-Org vCluster's syncer init images are DENIED (they pull upstream
    # k8s images, not Harbor-proxy globs) and vc-<slug> never installs,
    # wedging the whole Org backing stack. Refs #4099 #3859.
    openova.io/organization: {{.Subdomain}}
    openova.io/sovereign: {{.OTECHFQDN}}
    openova.io/managed-by: catalyst
    openova.io/tier: org
  annotations:
    catalyst.openova.io/admin-email: {{.AdminEmail}}
    catalyst.openova.io/company-name: {{.CompanyName}}
    catalyst.openova.io/console-host: {{.ConsoleHost}}
    catalyst.openova.io/domain-mode: {{.DomainMode}}
`

// orgTenantVCluster was removed in #4188. The canonical per-Organization
// vCluster backing is the CRD org-controller's `vcluster` HelmRelease
// (chart vcluster@0.33.x, loft vcluster-oss distro, Harbor-mirrored
// images, ns `<subdomain>`) — see core/controllers/organization/internal/
// gitops/manifests.go. The legacy template here rendered a SECOND,
// orphaned vCluster (`vc-<subdomain>`, chart 0.19.x, raw rancher/k3s
// image) that nothing in the overlay consumed and that could never pull
// its image on a Harbor-only Sovereign → permanent Init:ImagePullBackOff
// + a False HelmRelease on the spine. Do NOT re-introduce it: a per-Org
// vCluster belongs to the CRD org-controller, full stop.

const orgTenantBPKeycloak = `# bp-keycloak per-tenant (issue #800/#803/#804/#910) — the Organization's
# own Keycloak instance. Each tenant runs its own Keycloak Pod +
# PostgreSQL backend in its tenant Namespace; per Inviolable Principle 7
# (K8s-native tenancy) the realm namespace is internal to that one
# Keycloak (no cross-tenant collision).
#
# Values contract (issue #910 / B3 fix):
# The bp-keycloak chart (platform/keycloak/chart/values.yaml) consumes
# the canonical Catalyst-side keys below. Earlier orchestrator versions
# emitted a different shape (` + "`topology`, `realm.*`, `bootstrap.*`, `ingress.*`" + `)
# that the chart did NOT honour — result: tenant Keycloak Pod ran but
# no HTTPRoute was rendered (` + "`gateway.host`" + ` was unset), so tenant
# users could not reach their own Keycloak and downstream WordPress /
# OpenClaw / Stalwart OIDC integration broke.
#
# Chart contract (current):
#   - sovereignFQDN          : the per-tenant identity zone, used by
#                              configmap-sovereign-realm.yaml to render
#                              redirect/origin URIs as
#                              https://console.<sovereignFQDN>/*
#   - sovereignRealm.enabled : when true (default), the chart emits its
#                              realm-import ConfigMap + service-account
#                              Secret. Keep true so the keycloak-config-cli
#                              post-install Job runs and the realm is
#                              materialised idempotently.
#   - gateway.enabled        : true (chart default)
#   - gateway.host           : the tenant Keycloak's public hostname.
#                              Without this the templates/httproute.yaml
#                              guard renders nothing → no exposure.
#   - smtp.{host,port,from,user,password,ssl,starttls,auth}
#                              SMTP for outbound realm email (welcome /
#                              password-reset). Phase-1 default: mothership
#                              relay at mail.openova.io:587. Tenant-local
#                              Stalwart relay is later work.
#
# realmConfig.tenant.* — forward-looking marker (chart does not yet
# consume; Helm silently ignores unknown values). Once the chart adds a
# tenant-mode realm template these keys carry the per-tenant realm name,
# OIDC client list (wordpress/openclaw/stalwart), and group bootstrap.
# Tracked under the bp-keycloak tenant-mode follow-up.
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-keycloak
  namespace: {{.Namespace}}
spec:
  interval: 10m
  chart:
    spec:
      chart: bp-keycloak
      version: "{{.ChartVersions.Keycloak}}"
      sourceRef:
        kind: HelmRepository
        name: bp-keycloak
        namespace: flux-system
  install:
    timeout: 15m
  upgrade:
    timeout: 15m
    cleanupOnFail: true
  # SMTP credentials reference: Phase-1 routes through the mothership
  # Stalwart relay. The per-tenant Secret is reflected from
  # catalyst-system/sovereign-smtp-credentials by the bootstrap-kit
  # reflector setup; until that lands the chart keeps smtp.user /
  # smtp.password empty (chart accepts empty values; outbound mail
  # silently no-ops, login flows still work).
  valuesFrom:
    - kind: Secret
      name: org-tenant-smtp-credentials
      valuesKey: smtp-user
      targetPath: smtp.user
      optional: true
    - kind: Secret
      name: org-tenant-smtp-credentials
      valuesKey: smtp-pass
      targetPath: smtp.password
      optional: true
  values:
    # #4739 W-2: per-Org namespaces enforce Guaranteed-only QoS via the
    # plan-limits LimitRange (maxLimitRequestRatio {cpu:1, memory:1} —
    # #4389/#4292). Every pod this release renders in the Org namespace
    # must ship requests==limits or it is FORBIDDEN at admission (live on
    # hw220 nstar: postgresql primary ratio 1.5 rejected → bp-keycloak
    # Stalled → all dependent funnel apps DependencyNotReady). Sizes keep
    # the chart's existing ceilings (no OOM/throttle regression): the JVM
    # ceiling stays 2Gi, postgresql gets the Bitnami small profile, the
    # config-cli one-shot Job is tiny.
    keycloak:
      resources:
        requests:
          cpu: "1"
          memory: 2Gi
        limits:
          cpu: "1"
          memory: 2Gi
      postgresql:
        primary:
          resources:
            requests:
              cpu: 500m
              memory: 512Mi
            limits:
              cpu: 500m
              memory: 512Mi
      keycloakConfigCli:
        resources:
          requests:
            cpu: 250m
            memory: 256Mi
          limits:
            cpu: 250m
            memory: 256Mi
    # Per-tenant identity zone — drives realm import redirect/origin URIs.
    sovereignFQDN: {{.Subdomain}}.{{.ParentDomain}}
    # Realm import is owned by the chart's keycloak-config-cli post-
    # install Job; the chart materialises a "sovereign" realm and the
    # catalyst-kc-sa-credentials Secret idempotently. Keep enabled so
    # the realm exists from t=0 and SSE/SSO probes find it.
    sovereignRealm:
      enabled: true
    # HTTPRoute exposure for the per-Org Keycloak. Without a non-empty
    # gateway.host the chart's templates/httproute.yaml guard renders
    # nothing — that was the user-visible regression in #910.
    gateway:
      enabled: true
      host: keycloak.{{.Subdomain}}.{{.ParentDomain}}
      # backendService defaults to .Release.Name; with releaseName=
      # bp-keycloak the bitnami fullname helper trims the chart suffix
      # and returns "bp-keycloak", matching the default.
      parentRef:
        # console-isolation (#4054/#4070): keycloak.<slug>.<pool-zone>
        # resolves to the dedicated console-ELB EIP which fronts the
        # ISOLATED cilium-gateway-console (NOT the apps cilium-gateway /
        # apps-ELB). Parenting the apps gateway made envoy on the console
        # ELB return 404 for keycloak.<slug>.<zone> → all Org SSO broke.
        # Per-Org (pool-domain) Keycloak MUST parent cilium-gateway-console.
        # The dedicated Gateway lives in kube-system alongside the apps
        # cilium-gateway (clusters/_template/sovereign-tls/cilium-gateway-
        # console.yaml:54 + org_console_tls.go consoleGatewayNamespace).
        name: cilium-gateway-console
        namespace: kube-system
        # sectionName omitted — the console Gateway exposes a single
        # apex-bound HTTPS listener per Org zone, matched by hostname
        # filter. The bp-keycloak chart template guards the sectionName
        # output with a 'with' conditional, so a blank value drops the
        # field entirely. See #4053 / org_console_tls.go.
        sectionName: ""
    # Outbound realm email — Phase-1 mothership relay. Operator overlay
    # (or future tenant-Stalwart sub-issue) overrides host/port once
    # tenant-local SMTP is shipped.
    smtp:
      host: mail.openova.io
      port: "587"
      from: noreply@{{.Subdomain}}.{{.ParentDomain}}
      ssl: "false"
      starttls: "true"
      auth: "true"
    # Forward-looking tenant-mode marker (issue #910 / B3). Chart does
    # not yet consume — Helm silently accepts unknown values. The
    # canonical realm + clients shape the orchestrator wants once the
    # chart's tenant template lands.
    realmConfig:
      tenant:
        enabled: true
        # subdomain — the Org slug. bp-keycloak >= 1.5.0's
        # configmap-tenant-realm.yaml HARD-REQUIRES this when
        # tenant.enabled=true ("realmConfig.tenant.enabled=true requires
        # realmConfig.tenant.subdomain — Inviolable Principle #4"). Earlier
        # orchestrator versions omitted it because the realmConfig.tenant.*
        # block was a forward-looking marker the chart silently ignored;
        # once the chart's tenant-mode realm template landed, a missing
        # subdomain wedged the per-Org Keycloak install and cascaded to
        # every dependent HR (bp-newapi/openclaw/stalwart/wordpress-tenant).
        # Refs #4099.
        subdomain: {{.Subdomain}}
        realmName: org-{{.Subdomain}}
        displayName: {{.CompanyName}}
        adminEmail: {{.AdminEmail}}
        groups:
          - org-admin
          - org-user
        clients:
          - id: catalyst-ui
            publicClient: true
            redirectURIs:
              - https://{{.ConsoleHost}}/*
          - id: wordpress
            publicClient: false
            redirectURIs:
              - https://{{.WordPressHost}}/*
          - id: openclaw
            publicClient: false
            redirectURIs:
              - https://{{.OpenClawHost}}/*
          - id: stalwart
            publicClient: false
            redirectURIs:
              - https://{{.MailHost}}/*
        parentDomain: {{.ParentDomain}}
`

// orgTenantBPCNPG was REMOVED (#4920). The per-Org bp-cnpg operator is no
// longer emitted into the Organization tenant overlay.
//
// WHY (root cause, verified against upstream CNPG v1.29.0 source):
//   The per-Org operator ran WEBHOOK-LESS + NAMESPACE-SCOPED and, since G65
//   (#4322), with its cluster-scoped webhookconfigurations RBAC stripped so it
//   could not hijack the shared cnpg-{mutating,validating}-webhook-configuration
//   singletons. But at the pinned operator image (1.29.0) the operator's
//   mandatory startup `ensurePKI` UNCONDITIONALLY get+patches those singletons
//   (internal/cmd/manager/controller/controller.go → pkg/certs/k8s.go
//   injectPublicKeyInto{Mutating,Validating}Webhook). The
//   `MANAGE_WEBHOOK_CONFIGURATIONS=false` opt-out that the bp-cnpg chart already
//   sets does NOT exist until a later CNPG release, so on 1.29.0 it is a silent
//   no-op. Result: without the RBAC the per-Org operator CrashLoopBackOff'd on
//   ensurePKI → the bp-cnpg HelmRelease never went Ready → the Flux
//   `dependsOn: bp-cnpg` gate blocked bp-wordpress-tenant / bp-newapi from ever
//   installing (hw233 walk 2026-07-09, #4920). Re-granting the RBAC would fix
//   the crash but re-introduce the exact #4322 cluster-wide caBundle hijack
//   (the per-Org operator injects its own org-ns leaf into the shared caBundle).
//
// FIX (#4920, issue Option B): don't deploy a per-Org operator at all. The
//   platform cnpg-system operator is cluster-wide (upstream subchart default
//   `config.clusterWide: true`, no WATCH_NAMESPACE) and already reconciles
//   postgresql.cnpg.io/v1.Cluster CRs in EVERY namespace — including the host
//   org-tenant namespace the per-Org apps install into. It also owns the
//   cluster-singleton webhook that admits Org Cluster CRs. So the per-Org
//   operator was redundant for reconciliation AND the sole source of the
//   webhook-hijack / crashloop class. Removing it (this const + the
//   bp-cnpg.yaml overlay file + the bp-cnpg HelmRepository + the
//   `dependsOn: bp-cnpg` on bp-newapi/bp-wordpress-tenant) lets the platform
//   operator own the Org's Postgres end-to-end. The per-Org apps still OWN
//   their Postgres via their own chart `cnpg.enabled=true` Cluster CRs; only
//   the redundant per-namespace operator is gone.

// orgTenantBPNewAPI emits the per-tenant bp-newapi HelmRelease (#945).
//
// Architecture: every Organization runs ITS OWN NewAPI gateway. alice's OpenClaw
// boots and points at https://api.alice.<parent>/v1 (set by
// orgTenantBPOpenClaw). That hostname MUST resolve to a per-tenant
// NewAPI Pod with its own Postgres-backed channels list, its own admin
// UI gated by the tenant Keycloak realm, and its own customer-API key
// minted by Catalyst on signup. A shared otech-wide newapi.<otech-fqdn>
// would defeat per-tenant channel routing (alice and bob would share
// the same channel set + audit log + commercial-contract attestation).
//
// Values contract (chart >= 1.3.0, see platform/newapi/chart/values.yaml):
//   - sovereignFQDN          → drives the OpenBao path convention for
//     ExternalSecrets (sovereign/<fqdn>/...).
//   - ingress.host           → customer-facing OpenAI-compatible API at
//     api.<sub>.<parent>/v1
//   - ingress.adminHost      → ops-staff admin UI at
//     admin.<sub>.<parent>
//   - auth.adminUI           → mode=keycloak, issuer = per-tenant realm
//     (alice's tenant Keycloak), clientId
//     "newapi-admin" registered by the tenant
//     realm-config (see #910/#915 C1).
//   - auth.customerAPI       → keyIssuer=catalyst (Catalyst mints
//     per-user bearer keys on signup; the
//     upstream's self-serve portal is OFF).
//   - database (UNSET) → the chart OWNS its Postgres: cnpg.enabled=true
//     (default) renders the per-Org CNPG Cluster +
//     the `bp-newapi-newapi-db-dsn` placeholder Secret
//     that the chart's own database-secret-sync-job
//     PATCHes with the canonical `postgres://...?sslmode=
//     require` DSN. deployment.yaml's $effDbSecret
//     auto-resolves to that Secret when database.
//     existingSecret is left unset (#3858/#3374 — the
//     old `newapi-pg-app`/SQL_DSN override pointed at a
//     name nothing creates and FailedMount'd).
//   - credentials (UNSET) → credentials.autoProvision=true (default)
//     auto-generates `bp-newapi-app-creds` with random
//     SESSION_SECRET + CRYPTO_SECRET (the old
//     `newapi-credentials` override pointed at an
//     uncreated Secret → CreateContainerConfigError).
//   - valkey.enabled=false → the chart's default valkey.url is the
//     host-synced rtz-vCluster Redis, unreachable from
//     inside an Org vCluster; NewAPI runs Postgres-only.
//   - defaultChannels.qwenPartner → channel #1 = partner-hosted Qwen
//     auto-seeded at install time (canonical
//     first-otech default per #915 C4 PR #919).
//
// dependsOn: bp-keycloak (OIDC for admin UI). Postgres is provided by the
// cluster-wide platform cnpg-system operator (#4920 removed the redundant
// per-Org bp-cnpg operator; the chart still OWNS its own CNPG Cluster via
// cnpg.enabled=true). Ordering matters — without the keycloak gate the
// chart's channel-seed post-install Job races the readiness of the dependency and Flux
// retries cost minutes.
const orgTenantBPNewAPI = `# bp-newapi per-tenant (#915, #945) — alice's own NewAPI gateway.
#
# Per umbrella epic #915 + bug #945 (G3 surfaced 2026-05-05): every Organization
# tenant runs its own NewAPI Pod with its own channels list + admin UI.
# OpenClaw's llm.baseURL points here (api.<sub>.<parent>/v1) so each
# tenant's chats route through their own NewAPI which proxies to the
# configured channel — partner-hosted Qwen wired by C4 (PR #919).
#
# A shared otech-wide newapi.<otech-fqdn> would defeat per-tenant
# channel routing, audit isolation, and commercial-contract attestation
# (alice and bob would share the same upstream account). Hence per-
# tenant deployment.
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-newapi
  namespace: {{.Namespace}}
spec:
  interval: 10m
  chart:
    spec:
      chart: bp-newapi
      version: "{{.ChartVersions.NewAPI}}"
      sourceRef:
        kind: HelmRepository
        name: bp-newapi
        namespace: flux-system
  # #4246 — disableWait: the bp-newapi Deployment gates on a non-empty SQL_DSN
  # via its wait-for-sql-dsn initContainer, and that DSN is PATCHed in by the
  # chart's db-dsn-sync POST-INSTALL Helm hook (which composes the DSN from the
  # CNPG cluster-app Secret). With wait enabled Helm blocks on the Deployment
  # becoming Ready BEFORE it runs post-install hooks, so the init never unblocks
  # (SQL_DSN stays empty), the install burns its 15m budget and exits with a
  # context deadline, and the post-install DSN-sync hook never fires: a hard
  # deadlock (verified on the omantel.biz demo Org 2026-06-24). disableWait lets
  # the release reach hook execution, the DSN syncs, and the Pod converges.
  # Keycloak readiness is still gated by dependsOn below; the CNPG Cluster is
  # reconciled by the cluster-wide platform cnpg-system operator (#4920).
  install:
    timeout: 15m
    disableWait: true
  upgrade:
    timeout: 15m
    cleanupOnFail: true
    disableWait: true
  dependsOn:
    - name: bp-keycloak
      namespace: {{.Namespace}}
  values:
    # Per-tenant identity zone — drives the OpenBao path convention for
    # ExternalSecrets (sovereign/<fqdn>/newapi/...). The sub.parent form
    # makes the path tenant-unique on a multi-tenant Sovereign.
    sovereignFQDN: {{.Subdomain}}.{{.ParentDomain}}
    # ── Postgres backend (chart-owned CNPG, auto-provisioned) ─────────
    # The bp-newapi chart OWNS its Postgres: cnpg.enabled=true (chart
    # default) renders a per-Org CNPG Cluster 'bp-newapi-newapi-pg' whose
    # '-app' Secret carries the rotating password, plus the placeholder
    # Secret 'bp-newapi-newapi-db-dsn' that the chart's own
    # database-secret-sync-job PATCHes with the canonical DSN
    #   postgres://newapi:<pw>@bp-newapi-newapi-pg-rw.<orgns>:5432/newapi?sslmode=require
    # (scheme 'postgres://', which new-api's DB-driver detection requires —
    # NOT the CNPG-native 'postgresql://'). deployment.yaml's $effDbSecret
    # auto-resolves to that name+key when database.existingSecret is unset,
    # so we MUST NOT override it.
    #
    # #3858 / #3374: a previous overlay pinned
    #   database.existingSecret: newapi-pg-app  (key SQL_DSN)
    # but NOTHING on a real Org creates a 'newapi-pg-app' Secret with a
    # 'SQL_DSN' key — the in-vCluster CNPG renders 'bp-newapi-newapi-pg-app'
    # (key 'uri', scheme 'postgresql://'). The Pod FailedMount on the missing
    # Secret, then 'references non-existent secret key: SQL_DSN'. Leaving
    # database unset lets the chart's canonical auto-provision path converge
    # zero-touch (validated live on the omantel.biz demo Org: newapi 3/3
    # Running, GET /api/status -> 200).
    # ── App credentials (SESSION_SECRET + CRYPTO_SECRET) ──────────────
    # credentials.autoProvision=true (chart default) auto-generates the
    # 'bp-newapi-app-creds' Secret with random 64-char SESSION_SECRET +
    # CRYPTO_SECRET (persistent across reconciles via Helm lookup +
    # resource-policy: keep). We MUST NOT point credentials.existingSecret
    # at an uncreated name: the previous overlay set
    #   credentials.existingSecret: newapi-credentials
    # which nothing creates -> CreateContainerConfigError: secret
    # "newapi-credentials" not found (#3858 root cause #2). Leaving
    # credentials unset lets the chart auto-provision them.
    # ── Valkey cache: DISABLED for the per-Org overlay ────────────────
    # The chart's default valkey.url points at a HOST-placed valkey
    # synced from the rtz vCluster
    #   redis://valkey-primary-x-valkey-x-rtz-vcluster.rtz.svc.cluster.local:6379
    # which is the correct target for the HOST-placed bp-newapi (#3373
    # Batch A) but is UNREACHABLE from inside an Org's own vCluster. NewAPI
    # treats REDIS_CONN_STRING as required when set and CrashLoops on
    # '[FATAL] Redis ping test failed: context deadline exceeded' (#3858
    # root cause #3). NewAPI falls back to an in-process cache when Valkey
    # is off — Postgres holds all durable state — so disabling it is safe.
    # If a genuinely reachable per-Org valkey ships later, set valkey.url to
    # THAT instead of re-enabling this cross-vCluster host.
    valkey:
      enabled: false
    # ── Auth: ops-staff admin UI gated by per-tenant Keycloak ─────────
    # Customer-facing API uses Catalyst-minted keys (NewAPI's self-serve
    # portal stays OFF on Catalyst Sovereigns per platform/newapi).
    auth:
      adminUI:
        mode: keycloak
        keycloak:
          issuer: https://keycloak.{{.Subdomain}}.{{.ParentDomain}}/realms/org-{{.Subdomain}}
          clientId: newapi-admin
          callbackPath: /oauth/callback
          existingSecret: newapi-oidc-client-secret
          # #4169 — this per-Org install authenticates against its OWN per-Org
          # realm (issuer above: .../realms/org-<sub>). It MUST NOT push a
          # newapi-admin AppRegistration ConfigMap into the SHARED sovereign
          # realm: that client is owned by the host/Sovereign ops-staff admin UI
          # (bootstrap-kit slot 80a), and a per-Org ConfigMap with the same
          # clientId clobbers the host's redirectUris (bp-sso-bridge keys on
          # clientId and PUT-overwrites) -> Keycloak "Invalid parameter:
          # redirect_uri" on newapi.<sovereign-fqdn> SSO. Disable the sync here
          # explicitly. (The chart also defends via an issuer-realm gate in
          # templates/sso-app-registration.yaml, but the overlay states intent.)
          ssoBridgeSync:
            enabled: false
      customerAPI:
        keyIssuer: catalyst
    # ── Ingress + cert-manager TLS ────────────────────────────────────
    # Two virtual hosts:
    #   api.<sub>.<parent>    → customer-facing OpenAI-compatible /v1/*
    #                          endpoint OpenClaw's llm.baseURL points to.
    #   admin.<sub>.<parent>  → ops-staff admin UI (OIDC against tenant
    #                          Keycloak realm).
    # The wildcard *.<parent> Cert covers the free-subdomain mode hosts;
    # BYO mode adds explicit dnsNames in certificate.yaml above.
    ingress:
      enabled: true
      className: traefik
      host: api.{{.Subdomain}}.{{.ParentDomain}}
      adminHost: admin.{{.Subdomain}}.{{.ParentDomain}}
      tls:
        enabled: true
        issuer: letsencrypt-prod
        apiSecretName: newapi-api-tls
        adminSecretName: newapi-admin-tls
    # ── Default channels: partner-hosted Qwen (canonical #915 C4) ────
    # Channel #1 — auto-seeded at install time by the chart's
    # post-install/post-upgrade channel-seed Helm hook Job. The seed
    # Job probes NewAPI's admin API via GET /api/channel/?keyword=NAME
    # (idempotent) and POSTs the channel if missing. The accountId +
    # contractRef carry the commercial-contract attestation per
    # platform/newapi/README.md compliance posture (operator overlay
    # supplies the legal-team-owned contract reference).
    #
    # Per Inviolable Principle #4 (never hardcode) the endpoint + key are
    # operator-supplied via the Kubernetes Secret
    # ` + "`newapi-channel-qwen-partner`" + ` carrying keys API_KEY (upstream API
    # bearer) and BASE_URL (upstream OpenAI-compatible endpoint URL),
    # plus per-Sovereign ExternalSecret + attestation values that the
    # operator overlays at tenant-create time. See docs/RUNBOOKS.md
    # §Operator-setup for the Secret schema.
    defaultChannels:
      qwenPartner:
        enabled: true
        # F1a (TBD-V45 follow-up #2115): row name MUST be 'qwen' so the
        # row matches sandbox-controller default AllowedChannels=["qwen"]
        # (bootstrap-kit slot 19a's SANDBOX_DEFAULT_CHANNELS:-qwen).
        # Pre-fix the row name carried a partner suffix, which produced
        # 404 channel-not-found on /v1/chat/completions.
        name: qwen
        # Endpoint defaults empty — operator overlay supplies the
        # upstream URL at tenant-create time (or via ExternalSecret
        # mirror of newapi-channel-qwen-partner key BASE_URL). The
        # chart's ` + "`assertChannelAttestation`" + ` gate refuses to render the
        # channel until the operator overlay populates this value.
        endpoint: ""
        models:
          - qwen3.6
          - qwen3-coder
        existingSecret: newapi-channel-qwen-partner
        existingSecretKey: API_KEY
        attestation:
          kind: commercial-contract
          # accountId + contractRef populated by per-Sovereign overlay
          # at tenant-create time (legal-team-owned values; chart gates
          # render until both are non-empty).
          accountId: ""
          contractRef: ""
    # ── Catalyst integration — admin token for per-user key minting ──
    # ADR-0003 §3.2: Catalyst's signup hook reads this Secret and POSTs
    # against NewAPI's admin API with Authorization: Bearer header to
    # issue per-user customer-API keys. The token is rotated per the
    # convention in docs/CATALYST-CLI-AGENT.md.
    catalystIntegration:
      enabled: true
      existingSecret: catalyst-newapi-admin-token
      externalSecret:
        enabled: true
        refreshInterval: 1h
        secretStoreRef:
          kind: ClusterSecretStore
          name: vault-region1
        remoteRef:
          key: sovereign/{{.OTECHFQDN}}/newapi/{{.TenantID}}/admin-token
          property: ADMIN_API_TOKEN
`

const orgTenantBPWordPress = `# bp-wordpress-tenant (#800, #915) — SSO-pre-wired WordPress per Organization.
#
# Per umbrella epic #915 (D1 sub-task) the chart's post-install
# oidc-config Job uses wp-cli to install + activate the openid-connect-
# generic plugin and write its option row pointing at the per-tenant
# Keycloak realm. The oidc.* block below is the canonical input contract
# for chart >= 0.2.0; keycloak.* is emitted alongside for back-compat
# with chart 0.1.x clusters that haven't picked up the new release yet.
#
# The wordpress-oidc-client-secret Secret carrying the client-secret key
# is materialised by bp-keycloak's tenant-realm ConfigMap render
# (PR #918, platform/keycloak/chart/templates/configmap-tenant-realm.yaml)
# at the same time as the realm JSON so the two never drift.
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-wordpress-tenant
  namespace: {{.Namespace}}
spec:
  interval: 10m
  chart:
    spec:
      chart: bp-wordpress-tenant
      version: "{{.ChartVersions.WordPress}}"
      sourceRef:
        kind: HelmRepository
        name: bp-wordpress-tenant
        namespace: flux-system
  dependsOn:
    - name: bp-keycloak
      namespace: {{.Namespace}}
  values:
    # MIRROR-EVERYTHING (#3785, Refs #3376 #3761): route BOTH WordPress
    # images (the main 'wordpress' server + the 'wordpress' wp-cli the
    # oidc-config Job runs — platform/wordpress-tenant/chart/templates/
    # _helpers.tpl wordpressImage/wpcliImage) THROUGH the Sovereign Harbor
    # DockerHub proxy-cache. Without this they default to the raw Docker Hub
    # 'wordpress' repository, which the harbor-proxy-pull Kyverno
    # ClusterPolicy (Enforce) DENIES because it doesn't match the
    # '*/proxy-*/*' glob — so the customer's purchased app NEVER starts (the
    # funnel's terminal acceptance, #3376). The chart prepends
    # global.imageRegistry onto each image.repository, so the live Harbor
    # DockerHub proxy project 'proxy-dockerhub' (NOT 'proxy-docker' — see
    # platform/openbao/chart/Chart.yaml) renders e.g.
    # harbor.openova.io/proxy-dockerhub/wordpress:6-php8.3-apache@sha256:...,
    # which matches the glob. Lockstep with the CNPG image the chart already
    # proxies via <registry>/proxy-ghcr. Registry host is operator-
    # overridable (Principle #4) via CATALYST_VCLUSTER_IMAGE_REGISTRY.
    global:
      imageRegistry: {{.VClusterImageRegistry}}/proxy-dockerhub
    # orgDomain is the chart-consumed data-value key — #3383 keeps every
    # .Values.org* overlay key WIRE-STABLE; the bp-wordpress-tenant chart
    # reads .Values.orgDomain in _helpers.tpl + sso-app-registration.yaml,
    # so the producer must keep emitting orgDomain (not a renamed orgDomain,
    # which the chart consumes nowhere → WordPress would fall back to the
    # default wordpress.org.local host).
    orgDomain: {{if .IsBYO}}{{.BYODomain}}{{else}}{{.Subdomain}}.{{.ParentDomain}}{{end}}
    # Canonical OIDC block (chart >= 0.2.0).
    oidc:
      enabled: true
      issuerURL: https://keycloak.{{.Subdomain}}.{{.ParentDomain}}/realms/org-{{.Subdomain}}
      clientId: wordpress
      clientSecretName: wordpress-oidc-client-secret
      defaultRole: subscriber
      identityKey: preferred_username
    # Legacy alias (chart 0.1.x back-compat). Removed in chart 0.3.0.
    keycloak:
      realmURL: https://keycloak.{{.Subdomain}}.{{.ParentDomain}}/realms/org-{{.Subdomain}}
      clientID: wordpress
      clientSecretName: wordpress-oidc-client-secret
    adminUser:
      email: {{.AdminEmail}}
      displayName: {{.CompanyName}}
    # ── Cilium Gateway API exposure (#3785) ───────────────────────────
    # A Catalyst Sovereign serves every customer host through the Cilium
    # Gateway API (HTTPRoute → cilium-gateway-console), NOT traefik. The
    # chart historically shipped only a traefik Ingress, so NO HTTPRoute
    # ever matched wordpress.<slug>.<pool> → envoy returned a bare 404
    # even though the WordPress Pod was Running 1/1 and served HTTP 302
    # internally (root-caused live on the omantel.biz demo Org). This
    # gateway block drives bp-wordpress-tenant chart >= 0.4.13's
    # templates/httproute.yaml — the route that actually makes the host
    # reachable, mirroring the bp-keycloak gateway block above.
    #
    # parentRef = cilium-gateway-console / kube-system (console-isolation
    # #4054/#4070): wordpress.<slug>.<pool> resolves to the console-ELB
    # EIP fronting the DEDICATED console Gateway, which also carries the
    # wildcard *.<pool> TLS listener — so TLS terminates on the wildcard
    # cert and NO per-host Certificate is needed (the chart's legacy
    # ingress.tls path issued a wordpress-tls cert that sat False forever
    # because no HTTP-01 / traefik solver runs on a Cilium Sovereign).
    # Parenting the apps cilium-gateway instead → 404 on the console ELB.
    gateway:
      enabled: true
      host: {{.WordPressHost}}
      parentRef:
        name: cilium-gateway-console
        namespace: kube-system
        sectionName: ""
{{- if .EnableHotStandby }}
    # D31 active-hot-standby — primary + replica Cluster CR pair across
    # the Sovereign's two declared regions, WAL streaming over Cilium
    # ClusterMesh (DoD D11 — clustermesh apiserver via LoadBalancer +
    # peer cert exchange). Toggled at the bootstrap-kit slot 13 layer
    # via SOVEREIGN_ENABLE_HOT_STANDBY=true on the per-Sovereign
    # overlay; the catalyst-api Pod reads SOVEREIGN_PRIMARY_REGION /
    # SOVEREIGN_REPLICA_REGION env at render time and writes them
    # through here. Chart contract: bp-wordpress-tenant chart 0.2.0+'s
    # pg.activeHotStandby.* block (see platform/wordpress-tenant/chart/
    # templates/cnpg-cluster.yaml).
    pg:
      activeHotStandby:
        enabled: true
        primaryRegion: {{ .PrimaryRegion }}
        replicaRegion: {{ .ReplicaRegion }}
{{- end }}
`

// orgTenantContinuum — D31 / Pillar 3 (Refs #2066). Per-Application
// Continuum CR that bp-continuum (Refs #2065) reconciles against to
// orchestrate primary/replica switchover + lua-record flip on region
// kill. Only rendered when EnableHotStandby=true; otherwise the
// template evaluates to whitespace and the overlay writer skips the
// file. The applicationRef points at the bp-wordpress-tenant
// HelmRelease (the per-Application unit in the Organization tenant model).
// CRD shape per products/catalyst/chart/crds/continuum.yaml.
//
// Lease backend defaults to dns-quorum because the Sovereign-internal
// PowerDNS already runs 3 resolver Pods and we don't want to pin
// tenants to Cloudflare KV; operators that want cloudflare-kv can
// follow up post-handover via a Continuum CR edit (the controller
// supports both kinds — see core/controllers/continuum/internal/
// witness/{cloudflarekv,dnsquorum}). Health-check URL points at the
// tenant's WordPress public host.
//
// RTO 30s / RPO 5s match CLAUDE.md §0 deterministic step 10's
// "≤30s failover" claim. autoFailover defaults to false so the first
// fresh-prov walk is operator-driven; Sovereigns that have proven
// the path can flip the CR field after the fact.
const orgTenantContinuum = `{{- if .EnableHotStandby }}
# Continuum.dr.openova.io/v1 — per-Application DR contract (#2066).
# Reconciled by bp-continuum (#2065). One CR per active-hot-standby
# tenant Application. The controller watches replication lag from the
# primary/replica Cluster CR pair (rendered by bp-wordpress-tenant's
# pg.activeHotStandby.* block above), drives the 7-step switchover
# sequencer on region-kill (validate-lease → cordon-old → drain
# HTTPRoute → flip-dns lua-record → swap-lease → uncordon-new →
# audit), and surfaces phase to the operator console.
apiVersion: dr.openova.io/v1
kind: Continuum
metadata:
  name: bp-wordpress-tenant
  namespace: {{.Namespace}}
  labels:
    catalyst.openova.io/org-tenant: {{.TenantID}}
    catalyst.openova.io/org-subdomain: {{.Subdomain}}
    openova.io/application: bp-wordpress-tenant
spec:
  applicationRef: bp-wordpress-tenant
  primaryRegion: {{ .PrimaryRegion }}
  hotStandbyRegions:
    - {{ .ReplicaRegion }}
  # CLAUDE.md §0 — failover must complete ≤30s. RPO 5s matches the
  # synchronous_commit=remote_apply replication shape PR #2071 wired
  # into bp-cnpg-pair / bp-wordpress-tenant.
  rto: 30s
  rpo: 5s
  # Operator-driven for the first fresh-prov walk; flip to true on
  # the CR post-handover once the path is proven.
  autoFailover: false
  # dns-quorum is the canonical Sovereign-internal lease backend
  # (3 in-cluster PowerDNS resolvers). cloudflare-kv is the
  # alternative when a public CF KV namespace is available.
  leaseClient:
    kind: dns-quorum
    config:
      ttlSeconds: 30
      renewSeconds: 10
      resolvers:
        - "10.43.0.10"
        - "10.43.0.11"
        - "10.43.0.12"
  luaRecord:
    selector: ifurlup
    healthCheck:
      url: https://{{.WordPressHost}}/healthz
      intervalSeconds: 5
      timeoutSeconds: 2
{{- end }}
`

const orgTenantBPOpenClaw = `# bp-openclaw (#803, #915) — workspace controller pre-wired to the
# per-tenant Keycloak realm (SSO) and the per-tenant NewAPI gateway
# (OpenAI-compatible LLM endpoint, NOT direct OpenAI).
#
# Per umbrella epic #915:
#   - oidc.issuerURL → per-tenant Keycloak realm (alice's users log in
#     to OpenClaw via alice's own Keycloak).
#   - llm.baseURL    → per-tenant NewAPI /v1 (alice's OpenClaw chats
#     route through alice's NewAPI which proxies to the configured
#     channel — partner-hosted Qwen wired by C4).
#   - llm.defaultModel → "qwen3.6" placeholder; NewAPI maps the model
#     name to a channel.
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-openclaw
  namespace: {{.Namespace}}
spec:
  interval: 10m
  chart:
    spec:
      chart: bp-openclaw
      version: "{{.ChartVersions.OpenClaw}}"
      sourceRef:
        kind: HelmRepository
        name: bp-openclaw
        namespace: flux-system
  dependsOn:
    - name: bp-keycloak
      namespace: {{.Namespace}}
  values:
    # ── OIDC (per-tenant Keycloak SSO) ─────────────────────────────────
    oidc:
      issuerURL: https://keycloak.{{.Subdomain}}.{{.ParentDomain}}/realms/org-{{.Subdomain}}
      clientId: openclaw
      clientSecret:
        name: openclaw-oidc-client-secret
        key: OIDC_CLIENT_SECRET
    # ── LLM gateway (per-tenant NewAPI, OpenAI-compatible) ─────────────
    # newapi runs as a per-tenant HelmRelease (bp-newapi) — alice has
    # her own NewAPI at api.<org-domain>; OpenClaw points its OpenAI
    # client there. The per-user newapi-key-{uuid} Secret carries the
    # end-user's bearer token (ADR-0003 §3.3); the controller-side
    # service token below is used for /readyz probes and any
    # controller-side LLM call that pre-dates a user session.
    llm:
      baseURL: https://api.{{.Subdomain}}.{{.ParentDomain}}/v1
      apiKey:
        name: openclaw-newapi-controller-token
        key: NEWAPI_KEY
      # NewAPI uses the model name to select a backing channel. C4
      # provisions channel #1 = partner-hosted Qwen at tenant-create
      # time so this default routes to the correct upstream.
      defaultModel: qwen3.6
    # ── Legacy aliases (back-compat with chart < 0.2.0) ────────────────
    keycloak:
      realmURL: https://keycloak.{{.Subdomain}}.{{.ParentDomain}}/realms/org-{{.Subdomain}}
      clientID: openclaw
      clientSecretName: openclaw-oidc-client-secret
    newapi:
      baseURL: https://api.{{.Subdomain}}.{{.ParentDomain}}/v1
    tenant:
      namespace: {{.Namespace}}
    # ── Public exposure: Cilium Gateway API (#4272) ────────────────────
    # A Sovereign runs the Cilium Gateway, NOT traefik — the chart's
    # networking.k8s.io/v1 Ingress is INERT here (never reconciled) and
    # its per-host openclaw-tls Certificate sits False forever. Disable it
    # and attach the controller host to the DEDICATED console Gateway whose
    # *.<pool> wildcard TLS listener terminates TLS (no per-host cert),
    # mirroring bp-agenity (#4180).
    ingress:
      enabled: false
    httpRoute:
      enabled: true
      hostnames:
        - {{.OpenClawHost}}
      parentRef:
        # console-isolation (#4054/#4070): openclaw.<slug>.<pool> resolves
        # to the dedicated console-ELB EIP fronting cilium-gateway-console
        # (NOT the apps cilium-gateway). The console Gateway carries the
        # *.<pool> wildcard TLS listener so TLS terminates on the wildcard
        # cert; parenting the apps gateway → envoy 404 on the console ELB.
        name: cilium-gateway-console
        namespace: kube-system
    # CiliumNetworkPolicy fromEntities:[ingress,host,remote-node] — admits
    # BOTH the Cilium Gateway Envoy (so the readyz OIDC-JWKS hairpin to the
    # public host on :443 resolves and the dashboard is reachable) AND the
    # kubelet readiness/liveness probe (else the controller stays 0/1 even
    # after the JWKS egress is fixed). #4272.
    networkPolicy:
      enabled: true
      ingress:
        allowGatewayEntity: true
`

const orgTenantBPAgenity = `# bp-agenity (#4180) — the per-Organization agentic dashboard. The User
# installs Agenity into their own Org and chats with the built-in solo
# agent (Claude Opus, token pre-configured via the Org's openbao); the
# agent creates Applications in the User's own Org through the RBAC-scoped
# openova MCP. This is the founder's North-Star agentic surface served at
# https://agenity.<slug>.<pool>/app/.
#
# Why this HelmRelease has NO dependsOn (verified live, #4180):
#   - The dashboard SPA (/app/) is served by the chepherd daemon's static
#     build and renders INDEPENDENT of keycloak (SSO) and cnpg (DB). The
#     rtz platform agenity install runs with zero dependsOn. Gating agenity
#     on bp-keycloak would needlessly hold the dashboard hostage to the
#     Org's SSO stack — the founder's URL must serve the new dashboard even
#     while keycloak/cnpg converge. The agent runtime authenticates to
#     Anthropic via the Org openbao token (ExternalSecret), not keycloak.
#
# Chart contract (products/agenity/chart/values.yaml):
#   - sovereignFqdn        : the per-Org identity zone (<slug>.<pool>) the
#                            openova-MCP derives https://console.<fqdn> from.
#   - httpRoute.hostnames  : [agenity.<slug>.<pool>] — the per-Org host the
#                            chart attaches to cilium-gateway-console.
#   - httpRoute.parentRef  : cilium-gateway-console / kube-system — the
#                            DEDICATED console Gateway carrying the
#                            *.<pool> wildcard TLS listener (#4054/#4070);
#                            parenting the apps cilium-gateway → envoy 404.
#                            The wildcard listener terminates TLS so NO
#                            per-host Certificate is needed.
#   - networkPolicy.ingress.allowGatewayEntity : emits the CNP
#                            fromEntities:[ingress] carve-out — without it
#                            the gateway→agenity hop is default-denied and
#                            the browser sees envoy 503 (#4180).
#   - anthropic.externalSecret : pulls the sk-ant token (+ claude-code
#                            credentials.json) from the Org's openbao via
#                            vault-region1 so the solo agent can call Claude.
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-agenity
  namespace: {{.Namespace}}
spec:
  interval: 10m
  chart:
    spec:
      chart: bp-agenity
      version: "{{.ChartVersions.Agenity}}"
      sourceRef:
        kind: HelmRepository
        name: bp-agenity
        namespace: flux-system
  install:
    timeout: 15m
  upgrade:
    timeout: 15m
    cleanupOnFail: true
  values:
    # Per-Org identity zone — the openova-MCP derives
    # https://console.<sovereignFqdn> from this when catalystApiUrl is empty.
    sovereignFqdn: {{.Subdomain}}.{{.ParentDomain}}
    # Zero-click SSO gate (#4553) — the bp-oidc-gate companion the chart
    # renders IN FRONT OF this Org's agenity host, chart-managed (NOT a
    # hand-created live instance). It owns {{.AgenityHost}}, runs the full
    # Keycloak sovereign-realm SSO (silent via catalyst-pin), and seeds the
    # SPA bootstrap token (spaTokenSeed -> /app/?token=sso) so the User lands
    # on the workspace with NO paste. clientId is pinned per-Org so each Org
    # registers a distinct KC client (matches the live agnstar
    # agenity-agnstar). The chart derives the same agenity-<slug> from the
    # hostname when clientId is empty; pinning it makes the contract explicit.
    oidcGate:
      enabled: true
      clientId: agenity-{{.Subdomain}}
    # HTTPRoute exposure for the per-Org dashboard. When oidcGate.enabled (the
    # default), the gate OWNS {{.AgenityHost}} and the chart's own HTTPRoute is
    # suppressed (two routes on one host is undefined routing); these
    # hostnames + parentRef still drive the gate's HTTPRoute host + gateway.
    httpRoute:
      enabled: true
      hostnames:
        - {{.AgenityHost}}
      parentRef:
        # console-isolation (#4054/#4070): agenity.<slug>.<pool> resolves to
        # the dedicated console-ELB EIP which fronts the ISOLATED
        # cilium-gateway-console (NOT the apps cilium-gateway / apps-ELB).
        # The console Gateway carries the *.<pool> wildcard TLS listener so
        # TLS terminates on the wildcard cert and no per-host Certificate is
        # needed; parenting the apps gateway → envoy 404 on the console ELB.
        name: cilium-gateway-console
        namespace: kube-system
        # sectionName omitted — the console Gateway exposes a single
        # apex-bound HTTPS listener per Org zone, matched by hostname filter.
        # The chart guards the field with a 'with' conditional.
    # CiliumNetworkPolicy fromEntities:[ingress] — admits the Cilium Gateway
    # Envoy to the agenity Service so the gateway→pod hop is not
    # default-denied (else envoy 503 "upstream connect error", #4180).
    networkPolicy:
      enabled: true
      ingress:
        allowGatewayEntity: true
    # Anthropic token (+ claude-code credentials.json) for the solo agent,
    # pulled from the Org's openbao via the region-1 ClusterSecretStore.
    # The chart's externalsecret-anthropic.yaml reads
    # secret/catalyst/anthropic/token — the path the catalyst-api producer
    # (seedAnthropicToken, #4277) writes at Org-create. It MUST live under
    # the catalyst/ prefix: that is the only KV sub-tree a Sovereign can
    # WRITE (catalyst-api-write policy); the external-secrets role used by
    # vault-region1 is read-only. The path is cluster-shared — one seed
    # serves every Org's agenity install.
    anthropic:
      externalSecret:
        enabled: true
        secretStoreRef: vault-region1
        secretStoreKind: ClusterSecretStore
        remoteKey: catalyst/anthropic/token
        remoteProperty: apiKey
        remoteCredentialsProperty: credentialsJson
    # openova-MCP Catalyst bearer + RS256 verify-pubkey (#4276 hop 7/7b).
    # The catalyst-api org-pipeline producer (seedMCPBearer) mints an
    # Org-scoped session JWT (tier=org-admin, org_id={{.Subdomain}},
    # role=openova-user, typ=session, RS256-signed by the Sovereign handover
    # signer) plus the matching RS256 verify pubkey (PKIX PEM), and writes
    # BOTH to the per-Org OpenBao path secret/catalyst/agenity/{{.Subdomain}}
    # /mcp-bearer (properties bearer + pubkeyPem). The chart's
    # externalsecret-mcp-bearer.yaml pulls them into the per-Org Secret
    # agenity-mcp-bearer; bearerSecret + rs256PubkeySecret below point the
    # StatefulSet at it so it projects OPENOVA_MCP_BEARER +
    # OPENOVA_MCP_RS256_PUBKEY_PEM. Without this the spawned claude-code
    # agent reaches the openova MCP with NO bearer → -32001 unauthenticated,
    # even after the Anthropic key (#4277) is seeded. The path MUST live
    # under catalyst/ (the only KV sub-tree a Sovereign can WRITE via the
    # catalyst-api-write policy; vault-region1's role is read-only).
    openovaMCP:
      # #4624 — the ORG console host the openova-MCP forwards as
      # X-Tenant-Host on the org-scoped install path. The chart would derive
      # the same value from httpRoute.hostnames[0] ({{.AgenityHost}} →
      # console.<slug>.<pool>), but pinning it makes the contract explicit
      # and byte-identical with the Application-CR install door's
      # defaultedParameters stamp. It MUST be the Org host — the catalyst-api
      # URL host (console.<sovereignFqdn>) is NOT a registered tenant, so a
      # Sovereign-host X-Tenant-Host 404s tenant-not-registered on every
      # agent create_application (live-proven on hw220 2026-07-04).
      tenantHost: {{.ConsoleHost}}
      bearerSecret:
        name: agenity-mcp-bearer
        key: bearer
      rs256PubkeySecret:
        name: agenity-mcp-bearer
        key: pubkeyPem
      mcpBearer:
        externalSecret:
          enabled: true
          secretStoreRef: vault-region1
          secretStoreKind: ClusterSecretStore
          remoteKey: catalyst/agenity/{{.Subdomain}}/mcp-bearer
          remoteBearerProperty: bearer
          remotePubkeyProperty: pubkeyPem
`

const orgTenantBPStalwart = `# bp-stalwart-tenant (#801, OIDC wiring #915) — dedicated mail server
# per Organization with Keycloak OIDC SSO against the per-tenant Keycloak realm.
#
# OIDC contract (#915): the per-tenant Keycloak (bp-keycloak above)
# registers a confidential client ` + "`stalwart`" + ` with redirect URI
# ` + "`https://<MailHost>/*`" + `. The realm-config-cli writes the client
# secret into the per-tenant ExternalSecret store under
# ` + "`sovereign/<otech-fqdn>/stalwart/<tenant>/oidc`" + ` (property
# OIDC_CLIENT_SECRET); this HelmRelease wires the chart's
# ` + "`oidcExternalSecret.remoteRef.key`" + ` to that path so the chart
# materialises the in-namespace Secret without operator hand-rolling.
#
# Stalwart's setup Job (mailbox-provision-job in the chart) then POSTs
# the OIDC directory definition to its ` + "`/api/settings`" + ` admin
# endpoint with the camelCase keys the upstream registry schema
# expects (issuerUrl/claimUsername/claimName/claimGroups). End result:
# alice's webmail at https://<MailHost> redirects to her tenant
# Keycloak, signs the JWT, returns to Stalwart, mailbox loads. Same
# flow for IMAP/SMTP via OAuth2 SASL XOAUTH2.
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-stalwart-tenant
  namespace: {{.Namespace}}
spec:
  interval: 10m
  chart:
    spec:
      chart: bp-stalwart-tenant
      version: "{{.ChartVersions.Stalwart}}"
      sourceRef:
        kind: HelmRepository
        name: bp-stalwart-tenant
        namespace: flux-system
  # #4307 — disableWait: the StatefulSet mounts the stalwart-tls Secret
  # NON-optional, so the Pod blocks at ContainerCreating until cert-manager
  # issues the mail.<host> leaf via DNS-01 (minutes). With Flux's default
  # --wait, helm-controller blocks on StatefulSet readiness BEFORE running the
  # post-install setup Job (a Helm hook that seeds the OIDC directory), so the
  # install burns its 15m budget, exits "context deadline exceeded", the HR
  # wedges InstallFailed/RetriesExceeded, and the setup Job never fires
  # (verified live on the omantel.biz demo Org 2026-06-24). disableWait lets
  # the release reach hook execution so the Job runs and the HR converges.
  # Keycloak readiness is still gated by dependsOn below. Mirrors bp-newapi.
  install:
    timeout: 15m
    disableWait: true
  upgrade:
    timeout: 15m
    cleanupOnFail: true
    disableWait: true
  dependsOn:
    - name: bp-keycloak
      namespace: {{.Namespace}}
  values:
    domain:
      primary: {{if .IsBYO}}{{.BYODomain}}{{else}}{{.Subdomain}}.{{.ParentDomain}}{{end}}
      mode: {{.DomainMode}}
    ingress:
      webmail:
        host: {{.MailHost}}
        # #4307 — parent the DEDICATED cilium-gateway-console (NOT the apps
        # cilium-gateway): mail.<slug>.<pool> resolves to the console-ELB EIP
        # fronting the console Gateway, which carries the *.<pool> wildcard
        # listener. Parenting the apps gateway makes the HTTPRoute go
        # Accepted=False/NoMatchingListenerHostname → 404 despite a healthy Pod
        # (matches the wordpress/keycloak/agenity per-Org convention). The
        # chart default is already cilium-gateway-console post-#4307; restated
        # here so the overlay intent is explicit and survives a chart-default
        # regression.
        parentRef:
          name: cilium-gateway-console
          namespace: kube-system
        tls:
          enabled: true
          issuer: letsencrypt-prod
    adminEmail: {{.AdminEmail}}
    # Keycloak OIDC SSO — same realm + ExternalSecret-store path
    # convention as bp-wordpress-tenant + bp-openclaw above so all
    # three Organization apps SSO against ONE tenant Keycloak with distinct
    # client IDs. Realm-config-tenant (#910 C1) registers the
    # ` + "`stalwart`" + ` client with redirect URIs covering the webmail
    # host AND the OIDC callback path.
    keycloak:
      realmURL: https://keycloak.{{.Subdomain}}.{{.ParentDomain}}/realms/org-{{.Subdomain}}
      clientID: stalwart
      clientSecretName: stalwart-oidc-client-secret
      oidcExternalSecret:
        enabled: true
        secretStoreRef:
          kind: ClusterSecretStore
          name: vault-region1
        remoteRef:
          key: sovereign/{{.OTECHFQDN}}/stalwart/{{.TenantID}}/oidc
          property: OIDC_CLIENT_SECRET
    admin:
      # #4246 — DO NOT source ADMIN_PASSWORD from an ExternalSecret on a fresh
      # per-Org install. Nothing seeds OpenBao at the
      # sovereign/<fqdn>/stalwart/<tenant>/admin path when an Organization is
      # franchised, so an enabled admin.externalSecret leaves the stalwart-admin
      # Secret unmaterialised, the StatefulSet hits a missing-secret mount
      # (secret stalwart-admin not found) and CreateContainerConfigError, and
      # the bp-stalwart-tenant HR never converges (verified on the omantel.biz
      # demo Org 2026-06-24; matches the chart's own #898 note in
      # admin-secret.yaml). Leaving externalSecret disabled lets the chart's
      # admin-secret.yaml auto-provision a persistent random ADMIN_PASSWORD
      # (lookup-or-generate with resource-policy: keep). An operator who
      # genuinely manages this credential in OpenBao can re-enable
      # externalSecret in a cluster overlay.
      externalSecret:
        enabled: false
    # The post-install setup Job seeds the OIDC directory entry into
    # Stalwart's runtime settings KV store via ` + "`/api/settings`" + ` so
    # the very first webmail/IMAP/SMTP login flows through Keycloak.
    # Re-uses the upstream Stalwart image (ships stalwart-cli + curl).
    mailboxProvisioner:
      setupJob:
        enabled: true
`

const orgTenantCertificate = `{{- if .IsBYO}}
# Per-host Certificate (BYO mode only). Free-subdomain Organizations are
# covered by the per-parent-zone wildcard *.{{.ParentDomain}} that
# cert-manager + powerdns-webhook issues per epic #825 sub-2 (one
# wildcard per parent in the role:org-pool list).
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: console-{{.Subdomain}}-tls
  namespace: {{.Namespace}}
spec:
  secretName: console-{{.Subdomain}}-tls
  issuerRef:
    name: letsencrypt-prod
    kind: ClusterIssuer
  dnsNames:
    - {{.ConsoleHost}}
    - {{.WordPressHost}}
    - {{.OpenClawHost}}
    - {{.MailHost}}
{{- else}}
# Free-subdomain mode — wildcard *.{{.ParentDomain}} already covers
# every Organization's console.<sub>.<parent_domain>; this file is intentionally
# a placeholder so kustomization.yaml's resource list stays static.
apiVersion: v1
kind: ConfigMap
metadata:
  name: cert-strategy-{{.Subdomain}}
  namespace: {{.Namespace}}
data:
  strategy: wildcard-parent-zone
  parentDomain: {{.ParentDomain}}
  notes: |
    Free-subdomain Organizations use the per-parent-zone wildcard certificate
    issued by cert-manager + powerdns-webhook. The parent zone is
    one of the Sovereign's role:org-pool entries (epic #825).
    No per-tenant Certificate resource is required.
{{- end}}
`
