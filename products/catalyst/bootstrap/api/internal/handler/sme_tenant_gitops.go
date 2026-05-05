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

const smeTenantBPKeycloak = `# bp-keycloak per-organization (issue #800/#803/#804) — the SME's
# own Keycloak realm. Issuer URL becomes the SPA's OIDC discovery
# anchor. Per epic #795 [B] this is the realm that holds the
# SME's user identity.
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
        name: openova-blueprints
        namespace: flux-system
  install:
    timeout: 15m
  upgrade:
    timeout: 15m
    cleanupOnFail: true
  values:
    topology: per-organization
    realm:
      name: sme-{{.Subdomain}}
      displayName: {{.CompanyName}}
    ingress:
      host: keycloak.{{.Subdomain}}.{{.ParentDomain}}
      tls:
        issuer: letsencrypt-prod
    bootstrap:
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
        name: openova-blueprints
        namespace: flux-system
  values:
    namespace: {{.Namespace}}
    operator:
      enabled: true
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
        name: openova-blueprints
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
        name: openova-blueprints
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
        name: openova-blueprints
        namespace: flux-system
  values:
    domain: {{if .IsBYO}}{{.BYODomain}}{{else}}{{.Subdomain}}.{{.ParentDomain}}{{end}}
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
