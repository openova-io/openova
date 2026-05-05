// Package handler — sme_tenant_gitops.go: GitOps overlay writer for
// the SME tenant provisioning pipeline (issue #804).
//
// Per docs/INVIOLABLE-PRINCIPLES.md #3 the orchestrator NEVER calls
// `kubectl apply`. Every per-tenant resource is materialised by:
//
//  1. Cloning the openova-public GitOps repo to a temp directory.
//  2. Generating the per-tenant Kustomize overlay under
//     clusters/<otech-fqdn>/sme-tenants/<sme_tenant_id>/.
//  3. git add + commit (committer "catalyst-api <ops@openova.io>").
//  4. git push.
//  5. Flux on the OTECH cluster reconciles within ~1 min and the
//     per-tenant HelmReleases come up.
//
// The overlay materialises every artifact issue #804 specifies:
//
//   - Namespace          sme-<sme_tenant_id>
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

// DefaultSMETenantGitOpsWriter is the production SMETenantGitOpsWriter
// implementation. It uses the same gitops env contract as
// marketplace_settings.go (CATALYST_GITOPS_REPO_URL,
// CATALYST_GITOPS_BRANCH, CATALYST_GITOPS_TOKEN, ...). Tests inject
// a stub.
type DefaultSMETenantGitOpsWriter struct {
	Log *slog.Logger
	// ChartVersions maps each bp-* chart slug to the SemVer the
	// generator emits. Per Inviolable Principle 4 these are read from
	// env at startup; the orchestrator caller is expected to provide
	// non-empty values when wiring.
	ChartVersions SMETenantChartVersions
}

// SMETenantChartVersions enumerates the SemVer strings the overlay
// generator emits for each bp-* HelmRelease. The orchestrator's
// wiring (main.go) reads each from env (CATALYST_SME_BP_KEYCLOAK_VER,
// CATALYST_SME_BP_CNPG_VER, CATALYST_SME_BP_WORDPRESS_VER,
// CATALYST_SME_BP_OPENCLAW_VER, CATALYST_SME_BP_STALWART_VER); when
// any is empty the generator falls back to "*" so Flux pulls the
// latest matching chart in the repository.
type SMETenantChartVersions struct {
	Keycloak  string
	CNPG      string
	WordPress string
	OpenClaw  string
	Stalwart  string
}

// WriteTenantOverlay implements SMETenantGitOpsWriter. Returns the
// commit SHA on success.
func (w DefaultSMETenantGitOpsWriter) WriteTenantOverlay(ctx context.Context, rec store.SMETenantProvisionRecord) (string, error) {
	cfg := loadGitOpsConfig()
	if cfg.Token == "" {
		return "", errors.New("gitops token unconfigured — set CATALYST_GITOPS_TOKEN")
	}
	scratch, err := os.MkdirTemp(envOr("CATALYST_GITOPS_TMPDIR", os.TempDir()), "sme-tenant-overlay-*")
	if err != nil {
		return "", fmt.Errorf("mktempdir: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(scratch); err != nil && w.Log != nil {
			w.Log.Warn("sme-tenant: scratch cleanup failed", "dir", scratch, "err", err)
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
	//   clusters/<otech-fqdn>/sme-tenants/<sme_tenant_id>/
	overlayDir := filepath.Join(repoDir, "clusters", rec.OTECHFQDN, "sme-tenants", rec.SMETenantID)
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir overlay: %w", err)
	}

	files, err := renderSMETenantOverlay(rec, w.ChartVersions)
	if err != nil {
		return "", fmt.Errorf("render overlay: %w", err)
	}
	for name, contents := range files {
		path := filepath.Join(overlayDir, name)
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			return "", fmt.Errorf("write %s: %w", name, err)
		}
	}

	relRoot := filepath.Join("clusters", rec.OTECHFQDN, "sme-tenants", rec.SMETenantID)
	if err := runGit(ctx, repoDir, "add", relRoot); err != nil {
		return "", fmt.Errorf("git add: %w", err)
	}

	// Issue #889 — Flux Kustomization at clusters/<fqdn>/sme-tenants/
	// requires a parent kustomization.yaml that enumerates the tenant
	// subdirectories. Regenerate it after every Write so the index is
	// always current. Without this, Flux fails with "kustomization path
	// not found" on a fresh Sovereign that has never had a tenant.
	parentDir := filepath.Join(repoDir, "clusters", rec.OTECHFQDN, "sme-tenants")
	if err := writeParentTenantsIndex(parentDir); err != nil {
		return "", fmt.Errorf("write parent index: %w", err)
	}
	parentRel := filepath.Join("clusters", rec.OTECHFQDN, "sme-tenants", "kustomization.yaml")
	if err := runGit(ctx, repoDir, "add", parentRel); err != nil {
		return "", fmt.Errorf("git add parent index: %w", err)
	}
	parentHRRel := filepath.Join("clusters", rec.OTECHFQDN, "sme-tenants", "helmrepositories.yaml")
	if err := runGit(ctx, repoDir, "add", parentHRRel); err != nil {
		return "", fmt.Errorf("git add parent helmrepositories: %w", err)
	}

	msg := fmt.Sprintf("sme-tenant: provision %s (%s) on %s",
		rec.Subdomain, rec.SMETenantID, rec.OTECHFQDN)
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
// clusters/<fqdn>/sme-tenants/kustomization.yaml index file. The file
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
	// Also emit the shared HelmRepositories file (#893) — the SME-tenant
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
	if err := os.WriteFile(hrFilePath, []byte(smeTenantSharedHelmRepositories), 0o644); err != nil {
		return fmt.Errorf("write shared helmrepositories: %w", err)
	}

	var b bytes.Buffer
	b.WriteString("# Generated by catalyst-api/sme-tenant pipeline (#804/#889/#893).\n")
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

// smeTenantSharedHelmRepositories is the canonical HelmRepository
// declarations the SME tenant overlays sourceRef into. Issue #893 —
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
const smeTenantSharedHelmRepositories = `# Generated by catalyst-api/sme-tenant pipeline (#893).
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

// DeleteTenantOverlay implements SMETenantGitOpsWriter. Removes the
// per-tenant overlay directory. Idempotent — a missing path commits
// an empty change with `--allow-empty`.
func (w DefaultSMETenantGitOpsWriter) DeleteTenantOverlay(ctx context.Context, rec store.SMETenantProvisionRecord) (string, error) {
	cfg := loadGitOpsConfig()
	if cfg.Token == "" {
		return "", errors.New("gitops token unconfigured — set CATALYST_GITOPS_TOKEN")
	}
	scratch, err := os.MkdirTemp(envOr("CATALYST_GITOPS_TMPDIR", os.TempDir()), "sme-tenant-delete-*")
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
	overlayDir := filepath.Join(repoDir, "clusters", rec.OTECHFQDN, "sme-tenants", rec.SMETenantID)
	if err := os.RemoveAll(overlayDir); err != nil {
		return "", fmt.Errorf("remove overlay: %w", err)
	}
	relRoot := filepath.Join("clusters", rec.OTECHFQDN, "sme-tenants", rec.SMETenantID)
	// `git add -A <path>` records the deletions.
	if err := runGit(ctx, repoDir, "add", "-A", relRoot); err != nil {
		return "", fmt.Errorf("git add: %w", err)
	}

	// Issue #889 — regenerate the parent kustomization.yaml index after
	// removing the tenant subdir, so Flux's Kustomization sees the
	// reduced resources list. If no tenants remain, the parent index is
	// rewritten with `resources: []` (still a valid Kustomization root).
	parentDir := filepath.Join(repoDir, "clusters", rec.OTECHFQDN, "sme-tenants")
	if err := writeParentTenantsIndex(parentDir); err != nil {
		return "", fmt.Errorf("write parent index: %w", err)
	}
	parentRel := filepath.Join("clusters", rec.OTECHFQDN, "sme-tenants", "kustomization.yaml")
	if err := runGit(ctx, repoDir, "add", parentRel); err != nil {
		return "", fmt.Errorf("git add parent index: %w", err)
	}
	parentHRRel := filepath.Join("clusters", rec.OTECHFQDN, "sme-tenants", "helmrepositories.yaml")
	if err := runGit(ctx, repoDir, "add", parentHRRel); err != nil {
		return "", fmt.Errorf("git add parent helmrepositories: %w", err)
	}

	msg := fmt.Sprintf("sme-tenant: tear down %s (%s) on %s",
		rec.Subdomain, rec.SMETenantID, rec.OTECHFQDN)
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

// smeTenantTemplateData is the input the rendering templates consume.
type smeTenantTemplateData struct {
	TenantID      string
	Subdomain     string
	Namespace     string
	VClusterName  string
	OTECHFQDN     string
	// ParentDomain — the chosen sme-pool parent (multi-domain
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
	ChartVersions SMETenantChartVersions
	GeneratedAt   string
}

// renderSMETenantOverlay turns a record into a map<filename, contents>
// for the orchestrator to commit. Returned filenames are relative to
// the per-tenant overlay directory.
func renderSMETenantOverlay(rec store.SMETenantProvisionRecord, versions SMETenantChartVersions) (map[string]string, error) {
	if strings.TrimSpace(rec.SMETenantID) == "" {
		return nil, errors.New("render: sme_tenant_id required")
	}
	if strings.TrimSpace(rec.OTECHFQDN) == "" {
		return nil, errors.New("render: otech fqdn required")
	}
	if strings.TrimSpace(rec.Subdomain) == "" {
		return nil, errors.New("render: subdomain required")
	}
	versions = withVersionDefaults(versions)
	// Multi-domain Sovereign (#825): the chosen sme-pool parent zone
	// drives every derived host. Falls back to OTECHFQDN for single-
	// domain back-compat (#804).
	parentZone := strings.TrimSpace(rec.ParentDomain)
	if parentZone == "" {
		parentZone = rec.OTECHFQDN
	}
	host := ""
	if rec.DomainMode == store.SMEDomainBYO {
		host = "console." + rec.BYODomain
	} else {
		host = "console." + rec.Subdomain + "." + parentZone
	}
	wpHost := strings.Replace(host, "console.", "wordpress.", 1)
	owHost := strings.Replace(host, "console.", "openclaw.", 1)
	mailHost := strings.Replace(host, "console.", "mail.", 1)

	data := smeTenantTemplateData{
		TenantID:      rec.SMETenantID,
		Subdomain:     rec.Subdomain,
		Namespace:     rec.TenantNamespace,
		VClusterName:  rec.VClusterName,
		OTECHFQDN:     rec.OTECHFQDN,
		ParentDomain:  parentZone,
		ConsoleHost:   host,
		WordPressHost: wpHost,
		OpenClawHost:  owHost,
		MailHost:      mailHost,
		AdminEmail:    rec.AdminEmail,
		CompanyName:   rec.CompanyName,
		DomainMode:    string(rec.DomainMode),
		BYODomain:     rec.BYODomain,
		IsBYO:         rec.DomainMode == store.SMEDomainBYO,
		ChartVersions: versions,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
	}

	out := map[string]string{}
	for name, tpl := range smeTenantTemplates {
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

func withVersionDefaults(v SMETenantChartVersions) SMETenantChartVersions {
	star := func(s string) string {
		if strings.TrimSpace(s) == "" {
			return "*"
		}
		return s
	}
	return SMETenantChartVersions{
		Keycloak:  star(v.Keycloak),
		CNPG:      star(v.CNPG),
		WordPress: star(v.WordPress),
		OpenClaw:  star(v.OpenClaw),
		Stalwart:  star(v.Stalwart),
	}
}

// smeTenantTemplates is the canonical map of overlay files. Adding a
// new chart = a new entry here. Each template renders a single YAML
// document; kustomization.yaml lists all of them so Flux materialises
// them in topological order.
var smeTenantTemplates = map[string]string{
	"kustomization.yaml":       smeTenantKustomization,
	"namespace.yaml":           smeTenantNamespace,
	"vcluster.yaml":            smeTenantVCluster,
	"bp-keycloak.yaml":         smeTenantBPKeycloak,
	"bp-cnpg.yaml":             smeTenantBPCNPG,
	"bp-wordpress-tenant.yaml": smeTenantBPWordPress,
	"bp-openclaw.yaml":         smeTenantBPOpenClaw,
	"bp-stalwart-tenant.yaml":  smeTenantBPStalwart,
	"certificate.yaml":         smeTenantCertificate,
}

const smeTenantKustomization = `# Generated at {{.GeneratedAt}} by catalyst-api/sme-tenant pipeline (#804).
# DO NOT EDIT — re-run the orchestrator to regenerate.
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - namespace.yaml
  - vcluster.yaml
  - bp-keycloak.yaml
  - bp-cnpg.yaml
  - bp-wordpress-tenant.yaml
  - bp-openclaw.yaml
  - bp-stalwart-tenant.yaml
  - certificate.yaml
commonLabels:
  catalyst.openova.io/sme-tenant: {{.TenantID}}
  catalyst.openova.io/sme-subdomain: {{.Subdomain}}
`

const smeTenantNamespace = `apiVersion: v1
kind: Namespace
metadata:
  name: {{.Namespace}}
  labels:
    catalyst.openova.io/sme-tenant: {{.TenantID}}
    catalyst.openova.io/sme-subdomain: {{.Subdomain}}
    catalyst.openova.io/managed-by: catalyst-api
  annotations:
    catalyst.openova.io/admin-email: {{.AdminEmail}}
    catalyst.openova.io/company-name: {{.CompanyName}}
    catalyst.openova.io/console-host: {{.ConsoleHost}}
    catalyst.openova.io/domain-mode: {{.DomainMode}}
`

const smeTenantVCluster = `# vCluster HelmRelease — the SME's logical cluster lives here.
# Per Inviolable Principle 7 (K8s-native tenancy) every SME gets its
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

const smeTenantBPKeycloak = `# bp-keycloak per-tenant (issue #800/#803/#804/#910) — the SME's
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
        sectionName: https
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
        realmName: sme-{{.Subdomain}}
        displayName: {{.CompanyName}}
        adminEmail: {{.AdminEmail}}
        groups:
          - sme-admin
          - sme-user
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

const smeTenantBPCNPG = `# bp-cnpg in the SME tenant namespace — Postgres for WordPress
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
      # replicas are passive standbys; SME footprint trade-off per
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

const smeTenantBPWordPress = `# bp-wordpress-tenant (#800) — SSO-pre-wired WordPress per SME.
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
    smeDomain: {{if .IsBYO}}{{.BYODomain}}{{else}}{{.Subdomain}}.{{.ParentDomain}}{{end}}
    keycloak:
      realmURL: https://keycloak.{{.Subdomain}}.{{.ParentDomain}}/realms/sme-{{.Subdomain}}
      clientID: wordpress
      clientSecretName: wordpress-oidc-client-secret
    adminUser:
      email: {{.AdminEmail}}
      displayName: {{.CompanyName}}
    ingress:
      host: {{.WordPressHost}}
      tls:
        issuer: letsencrypt-prod
`

const smeTenantBPOpenClaw = `# bp-openclaw (#803) — workspace controller pre-wired to the SME
# Keycloak + the otech NewAPI gateway.
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
    keycloak:
      realmURL: https://keycloak.{{.Subdomain}}.{{.ParentDomain}}/realms/sme-{{.Subdomain}}
      clientID: openclaw
      clientSecretName: openclaw-oidc-client-secret
    newapi:
      # newapi runs on the otech (Sovereign) ingress regardless of the
      # SME's chosen parent zone — there's exactly one NewAPI per
      # Sovereign and it's anchored to OTECHFQDN.
      baseURL: https://newapi.{{.OTECHFQDN}}
      # Default model alias for OpenClaw → NewAPI requests. Channel #1
      # in the per-Sovereign NewAPI is Qwen3.6 hosted at BankDhofar
      # (issue #915 / bp-newapi 1.3.0 defaultChannels.qwenBankDhofar).
      # Both qwen3.6 (canonical UI alias) and qwen3-coder (upstream
      # model id) route to the same channel; OpenClaw's UI surfaces
      # the friendlier name.
      defaultModel: qwen3.6
    tenant:
      namespace: {{.Namespace}}
    ingress:
      host: {{.OpenClawHost}}
      tls:
        issuer: letsencrypt-prod
`

const smeTenantBPStalwart = `# bp-stalwart-tenant (#801) — dedicated mail server per SME.
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
  values:
    domain:
      primary: {{if .IsBYO}}{{.BYODomain}}{{else}}{{.Subdomain}}.{{.ParentDomain}}{{end}}
      mode: {{.DomainMode}}
    ingress:
      host: {{.MailHost}}
      tls:
        issuer: letsencrypt-prod
    adminEmail: {{.AdminEmail}}
`

const smeTenantCertificate = `{{- if .IsBYO}}
# Per-host Certificate (BYO mode only). Free-subdomain SMEs are
# covered by the per-parent-zone wildcard *.{{.ParentDomain}} that
# cert-manager + powerdns-webhook issues per epic #825 sub-2 (one
# wildcard per parent in the role:sme-pool list).
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
# every SME's console.<sub>.<parent_domain>; this file is intentionally
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
    Free-subdomain SMEs use the per-parent-zone wildcard certificate
    issued by cert-manager + powerdns-webhook. The parent zone is
    one of the Sovereign's role:sme-pool entries (epic #825).
    No per-tenant Certificate resource is required.
{{- end}}
`
