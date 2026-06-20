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
//   - HelmRelease       bp-cnpg (in tenant ns)
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
}

// WriteTenantOverlay implements OrganizationGitOpsWriter. Returns the
// commit SHA on success.
func (w DefaultOrganizationGitOpsWriter) WriteTenantOverlay(ctx context.Context, rec store.OrganizationProvisionRecord) (string, error) {
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
	// charts (bp-keycloak / bp-cnpg / bp-wordpress-tenant / bp-openclaw /
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
  name: bp-cnpg
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
# vCluster (loft) — a non-OCI Helm repo for the vcluster chart
# referenced by the per-tenant vcluster.yaml overlay.
apiVersion: source.toolkit.fluxcd.io/v1beta2
kind: HelmRepository
metadata:
  name: loft
  namespace: flux-system
spec:
  interval: 15m
  url: https://charts.loft.sh
`

// DeleteTenantOverlay implements OrganizationGitOpsWriter. Removes the
// per-tenant overlay directory. Idempotent — a missing path commits
// an empty change with `--allow-empty`.
func (w DefaultOrganizationGitOpsWriter) DeleteTenantOverlay(ctx context.Context, rec store.OrganizationProvisionRecord) (string, error) {
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
	Namespace    string
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

func withVersionDefaults(v OrganizationChartVersions) OrganizationChartVersions {
	star := func(s string) string {
		if strings.TrimSpace(s) == "" {
			return "*"
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
	}
}

// orgTenantTemplates is the canonical map of overlay files. Adding a
// new chart = a new entry here. Each template renders a single YAML
// document; kustomization.yaml lists all of them so Flux materialises
// them in topological order.
var orgTenantTemplates = map[string]string{
	"kustomization.yaml":       orgTenantKustomization,
	"namespace.yaml":           orgTenantNamespace,
	"vcluster.yaml":            orgTenantVCluster,
	"bp-keycloak.yaml":         orgTenantBPKeycloak,
	"bp-cnpg.yaml":             orgTenantBPCNPG,
	"bp-newapi.yaml":           orgTenantBPNewAPI,
	"bp-wordpress-tenant.yaml": orgTenantBPWordPress,
	"bp-openclaw.yaml":         orgTenantBPOpenClaw,
	"bp-stalwart-tenant.yaml":  orgTenantBPStalwart,
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
  - namespace.yaml
  - vcluster.yaml
  - bp-keycloak.yaml
  - bp-cnpg.yaml
  - bp-newapi.yaml
  - bp-wordpress-tenant.yaml
  - bp-openclaw.yaml
  - bp-stalwart-tenant.yaml
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
  annotations:
    catalyst.openova.io/admin-email: {{.AdminEmail}}
    catalyst.openova.io/company-name: {{.CompanyName}}
    catalyst.openova.io/console-host: {{.ConsoleHost}}
    catalyst.openova.io/domain-mode: {{.DomainMode}}
`

const orgTenantVCluster = `# vCluster HelmRelease — the Organization's logical cluster lives here.
# Per Inviolable Principle 7 (K8s-native tenancy) every Organization gets its
# own vcluster control plane; the bp-* charts below install INTO that
# vcluster via the vcluster syncer.
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: {{.VClusterName}}
  namespace: {{.Namespace}}
spec:
  interval: 10m
  chart:
    spec:
      chart: vcluster
      version: "0.19.x"
      sourceRef:
        kind: HelmRepository
        name: loft
        namespace: flux-system
  values:
    vcluster:
      image: rancher/k3s:v1.29.1-k3s2
    syncer:
      extraArgs:
        - --name={{.VClusterName}}
        - --tls-san={{.ConsoleHost}}
    storage:
      persistence: true
      size: 5Gi
    sync:
      ingresses:
        enabled: true
`

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
      name: sme-tenant-smtp-credentials
      valuesKey: smtp-user
      targetPath: smtp.user
      optional: true
    - kind: Secret
      name: sme-tenant-smtp-credentials
      valuesKey: smtp-pass
      targetPath: smtp.password
      optional: true
  values:
    # Per-tenant identity zone — drives realm import redirect/origin URIs.
    sovereignFQDN: {{.Subdomain}}.{{.ParentDomain}}
    # Realm import is owned by the chart's keycloak-config-cli post-
    # install Job; the chart materialises a "sovereign" realm and the
    # catalyst-kc-sa-credentials Secret idempotently. Keep enabled so
    # the realm exists from t=0 and SSE/SSO probes find it.
    sovereignRealm:
      enabled: true
    # HTTPRoute exposure on the per-Sovereign cilium-gateway. Without a
    # non-empty gateway.host the chart's templates/httproute.yaml guard
    # renders nothing — that was the user-visible regression in #910.
    gateway:
      enabled: true
      host: keycloak.{{.Subdomain}}.{{.ParentDomain}}
      # backendService defaults to .Release.Name; with releaseName=
      # bp-keycloak the bitnami fullname helper trims the chart suffix
      # and returns "bp-keycloak", matching the default.
      parentRef:
        name: cilium-gateway
        namespace: kube-system
        # sectionName omitted — multi-zone Sovereigns rename HTTPS listeners
        # to https-<sanitised-zone> (e.g. https-omani-works). The bp-keycloak
        # chart template guards the sectionName output with a 'with'
        # conditional on .Values.gateway.parentRef.sectionName, so a blank
        # value drops the field entirely; Cilium Gateway then matches by
        # hostname filter. See PR #1888 / TBD-A40 / issue #1902.
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

const orgTenantBPCNPG = `# bp-cnpg in the Organization tenant namespace — Postgres for WordPress
# + (in future tenants) other apps that need a relational store.
#
# Values contract (issue #910 / B3): bp-cnpg is a pure umbrella subchart
# of cloudnative-pg; per-Sovereign overrides flow through the
# ` + "`cloudnative-pg.*`" + ` namespace (see platform/cnpg/chart/values.yaml).
# Earlier orchestrator versions emitted ` + "`namespace`" + ` and ` + "`operator.enabled`" + `
# at the top level — the chart silently ignored them. Fixed to the
# canonical subchart-keyed shape.
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-cnpg
  namespace: {{.Namespace}}
spec:
  interval: 10m
  chart:
    spec:
      chart: bp-cnpg
      version: "{{.ChartVersions.CNPG}}"
      sourceRef:
        kind: HelmRepository
        name: bp-cnpg
        namespace: flux-system
  values:
    cloudnative-pg:
      # Single replica per tenant — operator-leader-elected, additional
      # replicas are passive standbys; Organization footprint trade-off per
      # docs/INVIOLABLE-PRINCIPLES.md #4 (overridable via per-cluster
      # overlay).
      replicaCount: 1
      crds:
        # CRDs ship with the bootstrap-kit's mothership bp-cnpg already;
        # the per-tenant install must NOT re-create them or apiserver
        # rejects the manifest with "already exists, owned by ...".
        create: false
      monitoring:
        # Default OFF per docs/BLUEPRINT-AUTHORING.md §11.2.
        podMonitorEnabled: false
`

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
//   - database.existingSecret → newapi-pg-app — the Secret bp-cnpg's
//     CNPG Cluster auto-renders for the
//     "newapi" Database (cnpg.enabled=true so
//     per-tenant Postgres auto-provisions per
//     #943).
//   - credentials.existingSecret → newapi-credentials — pulled from
//     OpenBao via ExternalSecret carrying the
//     SESSION_SECRET + CRYPTO_SECRET keys.
//   - defaultChannels.qwenPartner → channel #1 = partner-hosted Qwen
//     auto-seeded at install time (canonical
//     first-otech default per #915 C4 PR #919).
//
// dependsOn: bp-keycloak (OIDC for admin UI) + bp-cnpg (Postgres
// backend). Ordering matters — without it the chart's channel-seed
// post-install Job races the readiness of the dependencies and Flux
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
  install:
    timeout: 15m
  upgrade:
    timeout: 15m
    cleanupOnFail: true
  dependsOn:
    - name: bp-keycloak
      namespace: {{.Namespace}}
    - name: bp-cnpg
      namespace: {{.Namespace}}
  values:
    # Per-tenant identity zone — drives the OpenBao path convention for
    # ExternalSecrets (sovereign/<fqdn>/newapi/...). The sub.parent form
    # makes the path tenant-unique on a multi-tenant Sovereign.
    sovereignFQDN: {{.Subdomain}}.{{.ParentDomain}}
    # ── Postgres backend (bp-cnpg in tenant ns auto-provisions) ────────
    # bp-cnpg renders a per-database app Secret named "<db>-app" (#943).
    # The NewAPI chart consumes the canonical SQL_DSN key.
    database:
      existingSecret: newapi-pg-app
      existingSecretKey: SQL_DSN
    # ── App credentials (SESSION_SECRET + CRYPTO_SECRET) ──────────────
    # Materialised by an ExternalSecret pulling from the per-tenant
    # OpenBao path. The chart's manifest fails render if the Secret is
    # missing both keys; the operator overlay seeds them out-of-band on
    # tenant creation (bootstrap-kit reflector setup).
    credentials:
      existingSecret: newapi-credentials
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
    - name: bp-cnpg
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
    ingress:
      host: {{.WordPressHost}}
      tls:
        issuer: letsencrypt-prod
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
    ingress:
      host: {{.OpenClawHost}}
      tls:
        issuer: letsencrypt-prod
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
      externalSecret:
        enabled: true
        secretStoreRef:
          kind: ClusterSecretStore
          name: vault-region1
        remoteRef:
          key: sovereign/{{.OTECHFQDN}}/stalwart/{{.TenantID}}/admin
          property: ADMIN_PASSWORD
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
