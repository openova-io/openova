// Package gitops renders the per-Sandbox manifests the sandbox-controller
// writes into the per-Org `catalyst-tenant` Gitea repo under
// `sandbox/<owner-uid>/`.
//
// Per products/sandbox/docs/architecture.md §7 the sandbox-controller
// is the sister of organization-controller — it reconciles a
// per-Sandbox namespace + RBAC + PVCs + placeholder Secret INSIDE the
// Org vcluster (not the host cluster). The controller writes manifests
// to the per-Org Gitea repo following the SAME idiom
// organization-controller uses for vcluster manifests
// (core/controllers/organization/internal/gitops/manifests.go) — Flux
// on the host picks them up and reconciles into the Org vcluster.
//
// Wave 1 (this slice) materializes only:
//   - Namespace `sandbox-<owner-uid>` (uniquely scoped per Sandbox owner)
//   - ResourceQuota (mirrors spec.quota)
//   - ServiceAccount `sandbox`
//   - Role + RoleBinding scoping the SA to the sandbox namespace
//   - One PVC per spec.repos[] entry (repo clone target)
//   - Placeholder Secret `sandbox-tokens` (filled in Wave 2 by the
//     long-lived org-scoped token issuance flow — architecture.md §6)
//
// Wave 2 will add the pty-server StatefulSet + openova-sandbox-mcp
// Deployment + HTTPRoutes for `<pr>.<app>.<sb-<owner>>.<sov>.openova.io`.
//
// Per Inviolable Principle #4 (no hardcoded values) every knob comes
// from Inputs — nothing in the template literals encodes a cluster /
// region / version.
package gitops

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"text/template"

	sandboxapi "github.com/openova-io/openova/core/controllers/sandbox/internal/sandboxapi"
)

// Inputs is the subset of Sandbox spec + controller-level metadata the
// renderer needs. The reconciler builds this from the CR + reconciler
// configuration. Mirrors the shape of organization/internal/gitops.Inputs
// so future maintainers can move helpers between the two without
// re-learning a different convention.
type Inputs struct {
	// Name is the Sandbox resource name (used as a stable suffix when
	// the controller writes manifest filenames).
	Name string

	// OwnerUID is the DNS-1123-safe label derived from
	// spec.owner.email (e.g. "ceo-at-acme-com"). Drives the per-Sandbox
	// namespace `sandbox-<OwnerUID>` so two Sandboxes in the same Org
	// stay isolated.
	OwnerUID string

	// OwnerEmail is the authoritative owner email (stored verbatim on
	// the Namespace as an annotation for operator-debug; not used in
	// any DNS-bound name).
	OwnerEmail string

	// OrgSlug is the parent Organization slug. Drives the
	// openova.io/organization label so the Sovereign UI can filter
	// Sandbox resources per-Org without a label-graph lookup.
	OrgSlug string

	// SovereignFQDN is the Sovereign domain (e.g. omantel.omani.works).
	// Goes onto the openova.io/sovereign label for fleet-wide queries.
	SovereignFQDN string

	// Quota mirrors spec.quota — surfaced as a ResourceQuota CR.
	Quota sandboxapi.SandboxQuota

	// Repos is the (deterministically-ordered) list of repo entries the
	// reconciler renders PVCs for.
	Repos []sandboxapi.SandboxRepo

	// PreviewDomain is spec.previewDomain — written as an annotation on
	// the Namespace so Wave 2's HTTPRoute renderer can find it without
	// re-reading the CR.
	PreviewDomain string

	// NewAPIToken is the HS256 bearer minted by the catalyst-api bridge
	// handler (POST /admin/tokens/sandbox, shipped by PR #1638). When
	// non-empty, the renderer emits a dedicated
	// `secret-newapi-token.yaml` manifest containing the token under
	// the LLM_GATEWAY_TOKEN key. Empty disables the manifest — the
	// reconciler renders Wave 1 RBAC/PVCs even when the bridge is
	// unreachable so operators can debug a half-wired Sovereign
	// without losing the namespace-create step.
	NewAPIToken string

	// NewAPITokenSecretName is the Secret name the Sandbox Pod (Wave 2)
	// mounts as env LLM_GATEWAY_TOKEN. Conventionally
	// `sandbox-<owner-uid>-newapi-token`.
	NewAPITokenSecretName string

	// NewAPITokenExpiresAt is the absolute expiry of NewAPIToken (RFC3339
	// string ready for annotation use — caller pre-formats so the
	// renderer does not depend on time pkg). Surfaced as an annotation
	// on the rendered Secret so operators + Wave 2's restart-on-rotate
	// path can both inspect it without parsing the JWT.
	NewAPITokenExpiresAt string

	// NewAPITokenRotatedAt is the controller's bump-on-rotate marker
	// (RFC3339 instant). When non-empty, the renderer drops a
	// `kubectl.kubernetes.io/restartedAt` annotation on the rendered
	// Secret. Wave 2's pty-server StatefulSet will read the same value
	// off the mounted Secret to trigger a rolling restart on rotation.
	NewAPITokenRotatedAt string
}

const namespaceTemplate = `apiVersion: v1
kind: Namespace
metadata:
  name: {{ .NamespaceName }}
  labels:
    openova.io/organization: {{ .OrgSlug }}
    openova.io/sovereign: {{ .SovereignFQDN }}
    openova.io/sandbox: {{ .Name }}
    openova.io/sandbox-owner: {{ .OwnerUID }}
    openova.io/managed-by: catalyst
  annotations:
    openova.io/sandbox-owner-email: {{ .OwnerEmail | quote }}
{{- if .PreviewDomain }}
    openova.io/sandbox-preview-domain: {{ .PreviewDomain | quote }}
{{- end }}
`

const resourceQuotaTemplate = `apiVersion: v1
kind: ResourceQuota
metadata:
  name: sandbox-quota
  namespace: {{ .NamespaceName }}
  labels:
    openova.io/sandbox: {{ .Name }}
    openova.io/managed-by: catalyst
spec:
  hard:
    requests.cpu: {{ .Quota.CPU | quote }}
    limits.cpu: {{ .Quota.CPU | quote }}
    requests.memory: {{ .Quota.Memory | quote }}
    limits.memory: {{ .Quota.Memory | quote }}
    requests.storage: {{ .Quota.Storage | quote }}
    count/pods: {{ .Quota.ConcurrentSessions | quote }}
`

const serviceAccountTemplate = `apiVersion: v1
kind: ServiceAccount
metadata:
  name: sandbox
  namespace: {{ .NamespaceName }}
  labels:
    openova.io/sandbox: {{ .Name }}
    openova.io/managed-by: catalyst
`

const roleTemplate = `apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: sandbox
  namespace: {{ .NamespaceName }}
  labels:
    openova.io/sandbox: {{ .Name }}
    openova.io/managed-by: catalyst
rules:
  # Wave 1 RBAC: the sandbox ServiceAccount can see + manage its own
  # namespace's workloads. Pty-server + MCP Deployments + per-session
  # Pods (Wave 2) live here. Per architecture.md §3 the MCP server's
  # k8s.write.* tools are restricted to this namespace — RBAC enforces
  # that boundary.
  - apiGroups: [""]
    resources: ["pods", "pods/log", "pods/exec", "services", "configmaps", "secrets", "persistentvolumeclaims", "events"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["apps"]
    resources: ["deployments", "statefulsets", "replicasets"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["batch"]
    resources: ["jobs", "cronjobs"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
`

const roleBindingTemplate = `apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: sandbox
  namespace: {{ .NamespaceName }}
  labels:
    openova.io/sandbox: {{ .Name }}
    openova.io/managed-by: catalyst
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: sandbox
subjects:
  - kind: ServiceAccount
    name: sandbox
    namespace: {{ .NamespaceName }}
`

// pvcTemplate renders one PVC per repo. The repo clone (initContainer
// driven by the pty-server pod spec) lands in Wave 2; Wave 1 only
// reserves the storage.
const pvcTemplate = `apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: {{ .PVCName }}
  namespace: {{ .NamespaceName }}
  labels:
    openova.io/sandbox: {{ .Name }}
    openova.io/sandbox-repo: {{ .RepoSlug | quote }}
    openova.io/managed-by: catalyst
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: {{ .RepoStorage | quote }}
`

// secretTemplate renders the placeholder Secret architecture.md §7
// calls out as "Sandbox-scoped secrets (never echoes)". Wave 2 fills
// it with the long-lived org-scoped token (architecture.md §6) and the
// MCP server's bearer credentials.
const secretTemplate = `apiVersion: v1
kind: Secret
metadata:
  name: sandbox-tokens
  namespace: {{ .NamespaceName }}
  labels:
    openova.io/sandbox: {{ .Name }}
    openova.io/managed-by: catalyst
type: Opaque
stringData:
  placeholder: ""
`

// newapiTokenSecretTemplate renders the per-Sandbox NewAPI bearer
// Secret. Materialized into the Org vcluster's sandbox-<owner-uid>
// namespace by Flux; Wave 2's pty-server StatefulSet mounts the
// LLM_GATEWAY_TOKEN key as an env var on every Sandbox-agent Pod.
//
// The Secret carries TWO operator-visible annotations:
//   - openova.io/sandbox-token-expires-at — absolute expiry of the
//     embedded JWT (operator + rotation observer).
//   - kubectl.kubernetes.io/restartedAt   — rotation marker; Wave 2's
//     pty-server StatefulSet propagates this onto its Pod template via
//     a stringData → annotation reference so a fresh Secret triggers
//     a rolling restart.
const newapiTokenSecretTemplate = `apiVersion: v1
kind: Secret
metadata:
  name: {{ .SecretName }}
  namespace: {{ .NamespaceName }}
  labels:
    openova.io/sandbox: {{ .Name }}
    openova.io/sandbox-owner: {{ .OwnerUID }}
    openova.io/managed-by: catalyst
  annotations:
    openova.io/sandbox-token-expires-at: {{ .ExpiresAt | quote }}
{{- if .RotatedAt }}
    kubectl.kubernetes.io/restartedAt: {{ .RotatedAt | quote }}
{{- end }}
type: Opaque
stringData:
  LLM_GATEWAY_TOKEN: {{ .Token | quote }}
`

// kustomizationTemplate stitches every rendered manifest into one
// Kustomization the host-cluster Flux Kustomization picks up.
const kustomizationTemplate = `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - namespace.yaml
  - resourcequota.yaml
  - serviceaccount.yaml
  - role.yaml
  - rolebinding.yaml
  - secret.yaml
{{- if .HasNewAPIToken }}
  - secret-newapi-token.yaml
{{- end }}
{{- range .RepoPaths }}
  - {{ . }}
{{- end }}
`

// pvcRepoStorageDefault is the per-repo PVC size when SandboxQuota does
// not budget per-repo storage explicitly. Wave 2's storage allocator
// will split spec.quota.storage across repos; Wave 1 uses a sane
// fixed default so the PVC schema is well-formed.
const pvcRepoStorageDefault = "5Gi"

// Render returns (path, bytes) tuples the reconciler writes into the
// per-Org Gitea repo under `sandbox/<owner-uid>/`. The caller prepends
// that prefix — the renderer returns repo-relative paths.
//
// Idempotency: the output is deterministic for a given Inputs (sorted
// repo iteration, no time.Now() / rand). Re-rendering on a steady-state
// CR produces byte-equal output, which the Gitea PutFile path
// short-circuits without a write.
func Render(in Inputs) (map[string][]byte, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, fmt.Errorf("Inputs.Name is required")
	}
	if strings.TrimSpace(in.OwnerUID) == "" {
		return nil, fmt.Errorf("Inputs.OwnerUID is required")
	}
	if strings.TrimSpace(in.OrgSlug) == "" {
		return nil, fmt.Errorf("Inputs.OrgSlug is required")
	}
	ns := fmt.Sprintf("sandbox-%s", in.OwnerUID)

	// Deterministic repo ordering by GiteaRepo string — guarantees the
	// kustomization.yaml + PVC filenames don't churn between reconciles.
	repos := make([]sandboxapi.SandboxRepo, len(in.Repos))
	copy(repos, in.Repos)
	sort.SliceStable(repos, func(i, j int) bool {
		return repos[i].GiteaRepo < repos[j].GiteaRepo
	})

	type baseCtx struct {
		Inputs
		NamespaceName string
	}
	base := baseCtx{Inputs: in, NamespaceName: ns}

	out := make(map[string][]byte, 8+len(repos))

	// Non-repo templates.
	for path, raw := range map[string]string{
		"namespace.yaml":      namespaceTemplate,
		"resourcequota.yaml":  resourceQuotaTemplate,
		"serviceaccount.yaml": serviceAccountTemplate,
		"role.yaml":           roleTemplate,
		"rolebinding.yaml":    roleBindingTemplate,
		"secret.yaml":         secretTemplate,
	} {
		buf, err := renderTemplate(path, raw, base)
		if err != nil {
			return nil, err
		}
		out[path] = buf
	}

	// PVC per repo. RepoSlug is the gitea slug with "/" replaced by
	// "-" — DNS-label safe and stable across renames (rename would
	// produce a new CR anyway).
	type pvcCtx struct {
		Inputs
		NamespaceName string
		PVCName       string
		RepoSlug      string
		RepoStorage   string
	}
	repoPaths := make([]string, 0, len(repos))
	for _, repo := range repos {
		slug := sanitizeRepoSlug(repo.GiteaRepo)
		pvcName := fmt.Sprintf("repo-%s", slug)
		path := fmt.Sprintf("pvc-%s.yaml", slug)
		ctx := pvcCtx{
			Inputs:        in,
			NamespaceName: ns,
			PVCName:       pvcName,
			RepoSlug:      repo.GiteaRepo,
			RepoStorage:   pvcRepoStorageDefault,
		}
		buf, err := renderTemplate(path, pvcTemplate, ctx)
		if err != nil {
			return nil, err
		}
		out[path] = buf
		repoPaths = append(repoPaths, path)
	}

	// NewAPI per-Sandbox bearer Secret — opt-in (only when the caller
	// supplied a non-empty token; reconciler skips this manifest when
	// the bridge is unreachable so namespace + RBAC + PVCs still land
	// without the token-mint side-effect).
	if strings.TrimSpace(in.NewAPIToken) != "" {
		secretName := strings.TrimSpace(in.NewAPITokenSecretName)
		if secretName == "" {
			secretName = fmt.Sprintf("sandbox-%s-newapi-token", in.OwnerUID)
		}
		type tokenCtx struct {
			Inputs
			NamespaceName string
			SecretName    string
			Token         string
			ExpiresAt     string
			RotatedAt     string
		}
		buf, err := renderTemplate("secret-newapi-token.yaml", newapiTokenSecretTemplate, tokenCtx{
			Inputs:        in,
			NamespaceName: ns,
			SecretName:    secretName,
			Token:         in.NewAPIToken,
			ExpiresAt:     in.NewAPITokenExpiresAt,
			RotatedAt:     in.NewAPITokenRotatedAt,
		})
		if err != nil {
			return nil, err
		}
		out["secret-newapi-token.yaml"] = buf
	}

	// Kustomization stitching — sorted repoPaths keeps output stable.
	sort.Strings(repoPaths)
	type kustCtx struct {
		Inputs
		NamespaceName  string
		RepoPaths      []string
		HasNewAPIToken bool
	}
	kustBuf, err := renderTemplate("kustomization.yaml", kustomizationTemplate, kustCtx{
		Inputs:         in,
		NamespaceName:  ns,
		RepoPaths:      repoPaths,
		HasNewAPIToken: strings.TrimSpace(in.NewAPIToken) != "",
	})
	if err != nil {
		return nil, err
	}
	out["kustomization.yaml"] = kustBuf

	return out, nil
}

func renderTemplate(name, raw string, data any) ([]byte, error) {
	t, err := template.New(name).Funcs(funcs()).Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("template parse %s: %w", name, err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("template execute %s: %w", name, err)
	}
	return buf.Bytes(), nil
}

func funcs() template.FuncMap {
	return template.FuncMap{
		// quote stringifies and double-quotes any value. Accepts `any`
		// so int fields (e.g. SandboxQuota.ConcurrentSessions) work the
		// same as strings — matching helm's `quote` pipe semantics.
		"quote": func(v any) string { return fmt.Sprintf("%q", fmt.Sprintf("%v", v)) },
	}
}

// sanitizeRepoSlug converts "acme/eventforge" → "acme-eventforge" and
// trims any character that isn't [a-z0-9-]. The result is RFC 1123
// DNS-label-safe so it can be embedded in resource names. Reversibility
// is not required — the authoritative slug is on the PVC label
// openova.io/sandbox-repo.
func sanitizeRepoSlug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '/' || r == '_' || r == '.' || r == ' ':
			b.WriteRune('-')
		case r == '-':
			b.WriteRune('-')
		}
	}
	out := b.String()
	// Collapse runs of dashes, trim leading/trailing.
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	out = strings.Trim(out, "-")
	if len(out) > 200 {
		out = strings.Trim(out[:200], "-")
	}
	return out
}
