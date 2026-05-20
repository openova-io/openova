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
// Wave 1 materialized only namespace + RBAC + PVCs + placeholder
// Secret. Wave 8 (this slice — PR follow-up to #1622) extends the
// renderer to ALSO spawn the per-Sandbox runtime:
//
//   - Namespace `sandbox-<owner-uid>`
//   - ResourceQuota (mirrors spec.quota)
//   - ServiceAccount `sandbox` + Role + RoleBinding
//   - One PVC per spec.repos[] entry
//   - Placeholder Secret `sandbox-tokens`
//   - NEW: StatefulSet `pty-server` (replicas = spec.quota.concurrentSessions)
//   - NEW: Deployment `openova-sandbox-mcp`
//   - NEW: Service `pty-server` ClusterIP :7681
//   - NEW: HTTPRoute exposing `sandbox.<sov-fqdn>/sessions/<owner-uid>/*`
//
// Per Inviolable Principle #4 (no hardcoded values) every knob comes
// from Inputs — nothing in the template literals encodes a cluster /
// region / version / image / hostname.
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
// renderer needs.
type Inputs struct {
	Name                  string
	OwnerUID              string
	OwnerEmail            string
	OrgSlug               string
	SovereignFQDN         string
	Quota                 sandboxapi.SandboxQuota
	Repos                 []sandboxapi.SandboxRepo
	PreviewDomain         string
	AgentCatalogue        []string
	PtyServerImage        string
	MCPImage              string
	NewapiURL             string
	LLMGatewayTokenSecret string
	BYOSSecretPrefix      string
	IdleTimeoutMinutes    int

	// IdleScalingDisabled (TBD-D8b #1725) — when true the renderer
	// stamps `openova.io/sandbox-idle-scaling-disabled=true` on the
	// pty-server StatefulSet so the cluster-wide idle scaler skips it
	// on every pass. Default false preserves the existing scale-to-zero
	// policy. Sourced from Sandbox.spec.idleScaling.enabled (false →
	// disabled true; nil OR true → disabled false).
	IdleScalingDisabled bool

	// Wave 9 — per-Sandbox NewAPI bearer rendered into a dedicated
	// Secret manifest. When NewAPIToken is non-empty the renderer
	// emits secret-newapi-token.yaml carrying stringData
	// LLM_GATEWAY_TOKEN + openova.io/sandbox-token-expires-at
	// annotation; when NewAPITokenRotatedAt is also non-empty the
	// rendered Secret additionally carries
	// kubectl.kubernetes.io/restartedAt so Wave 8's pty-server
	// StatefulSet picks up rolling restarts on token rotation.
	NewAPIToken           string
	NewAPITokenSecretName string
	NewAPITokenExpiresAt  string
	NewAPITokenRotatedAt  string

	// D31 active-hot-standby — Sovereign-level toggle + region pair the
	// sandbox-controller propagates into every per-Sandbox MCP Pod via
	// SOVEREIGN_ENABLE_HOT_STANDBY / SOVEREIGN_PRIMARY_REGION /
	// SOVEREIGN_REPLICA_REGION env. The MCP server's sandbox.db.provision
	// handler reads them at call time and, when valid, materialises a
	// primary + replica Cluster.postgresql.cnpg.io pair instead of a
	// single Cluster (mirrors the bp-cnpg-pair pattern). Default empty
	// (zero regression): every Sandbox stays on single-Cluster CNPG.
	// Sourced from the sandbox-controller's own env (chart values
	// `cnpg.activeHotStandby.*` plumbed by bootstrap-kit slot 61 from
	// the per-Sovereign overlay's envsubst placeholders).
	EnableHotStandby string
	PrimaryRegion    string
	ReplicaRegion    string

	// TBD-P4 B4 — canonical SANDBOX_* env-var wiring for the MCP plugin
	// (products/sandbox/mcp-server/internal/tools/env.go). Without these,
	// every tool family (gitea / domain / storage / keycloak) silently
	// degrades to "not configured" at call time because the controller
	// previously emitted bare `ORG_ID` / `SOVEREIGN_FQDN` while the MCP
	// binary reads `SANDBOX_ORG_ID` / `SANDBOX_SOVEREIGN_FQDN` etc.
	//
	// Each value is plumbed by the controller from its chart-level env
	// (deployment.yaml `runtime.*` + new `*Secret` blocks). Empty leaves
	// the canonical var as an empty string on the MCP Pod, which the
	// MCP's per-tool requireX guard surfaces as a clear "not configured"
	// error — same behaviour as before, just now reachable instead of
	// silently misnamed.
	GiteaBaseURL              string
	GiteaTokenSecretName      string
	GiteaTokenSecretKey       string
	DomainAPIURL              string
	MarketplaceAPIURL         string
	StorageS3Endpoint         string
	StorageS3Region           string
	StorageS3UseTLS           string
	StorageS3CredsSecretName  string
	StorageS3AccessKeyKey     string
	StorageS3SecretKeyKey     string
	KeycloakAdminURL          string
	KeycloakParentRealm       string
	KeycloakAdminTokenSecret  string
	KeycloakAdminTokenSecretKey string

	// TBD-V21 — SANDBOX_JWT_SECRET wiring. Defaults below pick the
	// canonical bp-newapi-emitted Secret + key (Render fills the defaults
	// when caller passes empty). Mounted with `optional: true` on the MCP
	// Pod so a Sovereign mid-reflector-rollout doesn't crash-loop the
	// MCP. SIGNING_KEY material is reflected into every per-Sandbox
	// namespace via the bp-newapi chart's
	// `sandboxTokenSigningKey.reflectorNamespaces` default
	// (`catalyst-system,sandbox,sandbox-.*` regex).
	JWTSigningKeySecretName string
	JWTSigningKeySecretKey  string

	// TBD-V21 — SANDBOX_REPOS rendered into the MCP env as a comma-joined
	// list of `<org>/<repo>` slugs from sb.Spec.Repos. Empty list emits
	// an empty value (the MCP's CSV-parse contract treats empty as "no
	// repo filter"). Populated by Render() from in.Repos so callers do
	// not need to compute this themselves.
	SandboxRepos string
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

// mcpConfigMapTemplate renders the canonical `mcp.json` config that
// agent CLIs (claude-code, qwen-code, cursor-agent, …) read on session
// start to auto-discover the `openova-sandbox-mcp` server.
//
// TBD-P4 B3 (#1986) — Pillar-4 audit Surface B / finding B1 caught that
// NO MCP config file is injected anywhere. Even after PR #1988 bundled
// the agent binaries (B1) and PR #1992 wired slug→binary spawn (the
// other B3), the agents had zero discovery for the MCP server. This
// ConfigMap closes that gap.
//
// Schema is the canonical "claude-code / standard MCP" shape:
//
//	{
//	  "mcpServers": {
//	    "openova-sandbox-mcp": {
//	      "command": "/usr/local/bin/openova-sandbox-mcp",
//	      "args": [],
//	      "env": {}
//	    }
//	  }
//	}
//
// The MCP binary path matches the canonical install location the MCP
// Dockerfile uses (products/sandbox/mcp-server/Dockerfile:46). NOTE:
// for the stdio child shape to work end-to-end, the MCP binary must
// also be installed INTO the pty-server agent-runner image — that is
// follow-up work (TBD-P4 audit B2, separate PR). This ConfigMap is the
// FOUNDATION wire: when B2 lands, the journey works without further
// controller changes.
//
// The agents pick their config up from multiple paths:
//   - claude-code: project-level `./.mcp.json` (CWD) + user-level
//     `~/.claude.json` with a `mcpServers` key
//   - qwen-code:  `~/.qwen/settings.json` with `mcpServers` (qwen-code
//     is a fork of gemini-cli; same shape)
//   - cursor-agent: project-level `.cursor/mcp.json`
//
// We mount the SAME ConfigMap key at all canonical paths via multiple
// volumeMount entries. Empty `env: {}` lets the MCP binary inherit the
// per-Sandbox env vars the controller already plumbs (SANDBOX_*,
// LLM_GATEWAY_*, etc.) so credentials do NOT live in the ConfigMap.
const mcpConfigMapTemplate = `apiVersion: v1
kind: ConfigMap
metadata:
  name: sandbox-mcp-config
  namespace: {{ .NamespaceName }}
  labels:
    openova.io/sandbox: {{ .Name }}
    openova.io/sandbox-owner: {{ .OwnerUID }}
    openova.io/managed-by: catalyst
    app.kubernetes.io/name: sandbox-mcp-config
    app.kubernetes.io/component: mcp-config
  annotations:
    openova.io/sandbox-mcp-config-version: "v1"
data:
  # Canonical MCP config per the standard "mcpServers" schema documented
  # at https://modelcontextprotocol.io/. Claude Code, qwen-code, and
  # cursor-agent all read this shape; aider does not natively support
  # MCP (no-op for that agent, by design).
  #
  # TBD-P4 B3 (#1986) — foundation wire. Pairs with TBD-P4 audit B2:
  # the MCP binary must be installed INTO the pty-server agent-runner
  # image at /usr/local/bin/openova-sandbox-mcp. Until B2 ships the
  # binary into the image, this config will reference a path that
  # ENOENTs at spawn — the agent surfaces a clean "mcp server not found"
  # error rather than the current silent-no-discovery state.
  mcp.json: |
    {
      "mcpServers": {
        "openova-sandbox-mcp": {
          "command": "/usr/local/bin/openova-sandbox-mcp",
          "args": [],
          "env": {}
        }
      }
    }
`

// newapiTokenSecretTemplate renders the per-Sandbox NewAPI bearer
// Secret (Wave 9). Materialized into the Org vcluster's
// sandbox-<owner-uid> namespace by Flux; Wave 8's pty-server
// StatefulSet mounts the LLM_GATEWAY_TOKEN key as an env var on
// every Sandbox-agent Pod.
//
// The Secret carries TWO operator-visible annotations:
//   - openova.io/sandbox-token-expires-at — absolute expiry of the
//     embedded JWT (operator + rotation observer).
//   - kubectl.kubernetes.io/restartedAt   — rotation marker; Wave 8's
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

const ptyServerStatefulSetTemplate = `apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: pty-server
  namespace: {{ .NamespaceName }}
  labels:
    openova.io/sandbox: {{ .Name }}
    openova.io/sandbox-owner: {{ .OwnerUID }}
    openova.io/managed-by: catalyst
    app.kubernetes.io/name: pty-server
    app.kubernetes.io/component: pty-server
  annotations:
    openova.io/sandbox-idle-timeout-minutes: {{ .IdleTimeoutMinutes | quote }}
{{- if .IdleScalingDisabled }}
    openova.io/sandbox-idle-scaling-disabled: "true"
{{- end }}
spec:
  serviceName: pty-server
  replicas: {{ .Replicas }}
  selector:
    matchLabels:
      app.kubernetes.io/name: pty-server
      openova.io/sandbox: {{ .Name }}
  template:
    metadata:
      labels:
        app.kubernetes.io/name: pty-server
        app.kubernetes.io/component: pty-server
        openova.io/sandbox: {{ .Name }}
        openova.io/sandbox-owner: {{ .OwnerUID }}
        openova.io/managed-by: catalyst
    spec:
      serviceAccountName: sandbox
      automountServiceAccountToken: true
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
        runAsGroup: 65532
        fsGroup: 65532
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: pty-server
          image: {{ .PtyServerImage | quote }}
          imagePullPolicy: IfNotPresent
          ports:
            - name: http
              containerPort: 7681
          env:
            - name: PTY_SERVER_ADDR
              value: ":7681"
            - name: SANDBOX_OWNER_UID
              value: {{ .OwnerUID | quote }}
            - name: SANDBOX_OWNER_EMAIL
              value: {{ .OwnerEmail | quote }}
            - name: ORG_ID
              value: {{ .OrgSlug | quote }}
            - name: SOVEREIGN_FQDN
              value: {{ .SovereignFQDN | quote }}
            - name: NEWAPI_URL
              value: {{ .NewapiURL | quote }}
            - name: OPENAI_BASE_URL
              value: {{ .NewapiURL | quote }}
            - name: LLM_GATEWAY_URL
              value: {{ .NewapiURL | quote }}
            # TBD-V21 — key case alignment with newapiTokenSecretTemplate
            # (line 270 stringData: LLM_GATEWAY_TOKEN). Pre-fix the key
            # ref was lowercase 'llm-gateway-token' while the Secret writes
            # uppercase 'LLM_GATEWAY_TOKEN'. With 'optional: true' the
            # mismatch silently no-opped to an empty value -- every agent
            # CLI spawned in the pty-server shell ran without an LLM
            # bearer (LLM_GATEWAY_TOKEN inherited via os.Environ lands
            # empty), defeating the newapi-proxy gating contract.
            - name: LLM_GATEWAY_TOKEN
              valueFrom:
                secretKeyRef:
                  name: {{ .LLMGatewayTokenSecret | quote }}
                  key: LLM_GATEWAY_TOKEN
                  optional: true
            - name: OPENAI_API_KEY
              valueFrom:
                secretKeyRef:
                  name: {{ .LLMGatewayTokenSecret | quote }}
                  key: LLM_GATEWAY_TOKEN
                  optional: true
{{- if .ClaudeCodeBYOSActive }}
            - name: ANTHROPIC_API_KEY
              valueFrom:
                secretKeyRef:
                  name: {{ .BYOSSecretName | quote }}
                  key: access_token
                  optional: true
            - name: ANTHROPIC_BASE_URL
              value: ""
{{- end }}
          volumeMounts:
{{- range .RuntimeRepos }}
            - name: repo-{{ .Slug }}
              mountPath: /workspace/{{ .Slug }}
{{- end }}
            # TBD-P4 B3 (#1986) MCP config mounts. ConfigMap
            # sandbox-mcp-config carries a single mcp.json key in the
            # canonical "mcpServers" schema. We project it at every
            # canonical agent-config path so claude-code (user-level
            # ~/.claude.json + project ./.mcp.json), qwen-code
            # (~/.qwen/settings.json), and cursor-agent (.cursor/mcp.json)
            # all auto-discover the openova-sandbox-mcp server without
            # any user-typed config. Aider does not natively support MCP
            # so the mounts are inert there (by design).
            #
            # subPath is used so each mount stays a single file (not a
            # whole directory) and does NOT shadow other entries the
            # agent might write into the same parent dir at runtime.
            - name: mcp-config
              mountPath: /workspace/.mcp.json
              subPath: mcp.json
              readOnly: true
            - name: mcp-config
              mountPath: /home/node/.claude.json
              subPath: mcp.json
              readOnly: true
            - name: mcp-config
              mountPath: /home/node/.qwen/settings.json
              subPath: mcp.json
              readOnly: true
            - name: mcp-config
              mountPath: /workspace/.cursor/mcp.json
              subPath: mcp.json
              readOnly: true
          readinessProbe:
            httpGet:
              path: /healthz
              port: http
            initialDelaySeconds: 3
            periodSeconds: 5
          livenessProbe:
            httpGet:
              path: /healthz
              port: http
            initialDelaySeconds: 10
            periodSeconds: 15
          resources:
            requests:
              cpu: "100m"
              memory: "256Mi"
            limits:
              cpu: {{ .Quota.CPU | quote }}
              memory: {{ .Quota.Memory | quote }}
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop: ["ALL"]
            readOnlyRootFilesystem: false
      volumes:
{{- range .RuntimeRepos }}
        - name: repo-{{ .Slug }}
          persistentVolumeClaim:
            claimName: repo-{{ .Slug }}
{{- end }}
        # TBD-P4 B3 (#1986) — MCP config ConfigMap source. Projected at
        # multiple agent-canonical paths via the volumeMounts above.
        - name: mcp-config
          configMap:
            name: sandbox-mcp-config
            items:
              - key: mcp.json
                path: mcp.json
      terminationGracePeriodSeconds: 30
`

const mcpDeploymentTemplate = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: openova-sandbox-mcp
  namespace: {{ .NamespaceName }}
  labels:
    openova.io/sandbox: {{ .Name }}
    openova.io/sandbox-owner: {{ .OwnerUID }}
    openova.io/managed-by: catalyst
    app.kubernetes.io/name: openova-sandbox-mcp
    app.kubernetes.io/component: mcp-server
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: openova-sandbox-mcp
      openova.io/sandbox: {{ .Name }}
  template:
    metadata:
      labels:
        app.kubernetes.io/name: openova-sandbox-mcp
        app.kubernetes.io/component: mcp-server
        openova.io/sandbox: {{ .Name }}
        openova.io/sandbox-owner: {{ .OwnerUID }}
        openova.io/managed-by: catalyst
    spec:
      serviceAccountName: sandbox
      automountServiceAccountToken: true
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
        runAsGroup: 65532
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: mcp
          image: {{ .MCPImage | quote }}
          imagePullPolicy: IfNotPresent
          env:
            - name: SANDBOX_OWNER_UID
              value: {{ .OwnerUID | quote }}
            - name: SANDBOX_OWNER_EMAIL
              value: {{ .OwnerEmail | quote }}
            # ── TBD-P4 B4 — canonical SANDBOX_* names the MCP plugin
            # reads (products/sandbox/mcp-server/internal/tools/env.go).
            # ORG_ID + SOVEREIGN_FQDN were the original names and
            # silently degraded every MCP tool family to "not configured".
            - name: SANDBOX_ORG_ID
              value: {{ .OrgSlug | quote }}
            - name: SANDBOX_SOVEREIGN_FQDN
              value: {{ .SovereignFQDN | quote }}
            - name: SANDBOX_ID
              value: {{ .Name | quote }}
            - name: SANDBOX_NAMESPACE
              value: {{ .NamespaceName | quote }}
            # SANDBOX_TENANT_ID scopes the MCP's domain/byod handler
            # (marketplace.go:93). The per-Org slug is the tenant key in
            # the chroot Organization controller's wiring; the MCP
            # treats this opaquely.
            - name: SANDBOX_TENANT_ID
              value: {{ .OrgSlug | quote }}
            # ── Gitea wiring — SANDBOX_GITEA_BASE_URL is unauthed in
            # the in-cluster path; the matching token is mounted from
            # the existing catalyst-gitea-token Secret (single source
            # of truth shared with the controller itself; never written
            # here per Inviolable Principle #4 — Secrets are owned by
            # bp-catalyst-platform seed jobs).
            - name: SANDBOX_GITEA_BASE_URL
              value: {{ .GiteaBaseURL | quote }}
            {{- if .GiteaTokenSecretName }}
            - name: SANDBOX_GITEA_TOKEN
              valueFrom:
                secretKeyRef:
                  name: {{ .GiteaTokenSecretName | quote }}
                  key: {{ .GiteaTokenSecretKey | quote }}
                  optional: true
            {{- end }}
            # ── Domain + marketplace REST surfaces (services that
            # already run in-cluster; per-Sovereign overlays may pin
            # alternate ClusterIPs via the bp-sandbox HR values).
            - name: SANDBOX_DOMAIN_API_URL
              value: {{ .DomainAPIURL | quote }}
            - name: SANDBOX_MARKETPLACE_API_URL
              value: {{ .MarketplaceAPIURL | quote }}
            # ── Storage (SeaweedFS S3). Endpoint + region are public;
            # credentials are sourced from an existing per-Sandbox IAM
            # Secret when present. Empty creds surface a clear "not
            # configured" error from the MCP's storage tool family.
            - name: SANDBOX_STORAGE_S3_ENDPOINT
              value: {{ .StorageS3Endpoint | quote }}
            - name: SANDBOX_STORAGE_S3_REGION
              value: {{ .StorageS3Region | quote }}
            - name: SANDBOX_STORAGE_S3_USE_TLS
              value: {{ .StorageS3UseTLS | quote }}
            {{- if .StorageS3CredsSecretName }}
            - name: SANDBOX_STORAGE_S3_ACCESS_KEY
              valueFrom:
                secretKeyRef:
                  name: {{ .StorageS3CredsSecretName | quote }}
                  key: {{ .StorageS3AccessKeyKey | quote }}
                  optional: true
            - name: SANDBOX_STORAGE_S3_SECRET_KEY
              valueFrom:
                secretKeyRef:
                  name: {{ .StorageS3CredsSecretName | quote }}
                  key: {{ .StorageS3SecretKeyKey | quote }}
                  optional: true
            {{- end }}
            # ── Keycloak admin surface. URL + parent realm are public;
            # the admin bearer is sourced from an existing Secret.
            - name: KEYCLOAK_ADMIN_URL
              value: {{ .KeycloakAdminURL | quote }}
            - name: KEYCLOAK_PARENT_REALM
              value: {{ .KeycloakParentRealm | quote }}
            {{- if .KeycloakAdminTokenSecret }}
            - name: KEYCLOAK_ADMIN_TOKEN
              valueFrom:
                secretKeyRef:
                  name: {{ .KeycloakAdminTokenSecret | quote }}
                  key: {{ .KeycloakAdminTokenSecretKey | quote }}
                  optional: true
            {{- end }}
            - name: PTY_SERVER_URL
              value: "http://pty-server.{{ .NamespaceName }}.svc.cluster.local:7681"
            - name: LLM_GATEWAY_TOKEN
              valueFrom:
                secretKeyRef:
                  name: {{ .LLMGatewayTokenSecret | quote }}
                  key: LLM_GATEWAY_TOKEN
                  optional: true
            # TBD-V21 P1 — SANDBOX_TOKEN is the bearer the MCP plugin's
            # marketplace.* tool family expects (products/sandbox/mcp-
            # server/internal/tools/marketplace.go:95 requireMarketplaceProxy).
            # The same per-Sandbox JWT lives in the per-Sandbox Secret under
            # key LLM_GATEWAY_TOKEN (newapiTokenSecretTemplate, line 270).
            # Mounting it under a SECOND env name is the cheapest single-
            # source-of-truth wiring (Principle #4 — no Secret writes from
            # the controller). optional: true so the per-Sandbox Secret can
            # be absent on a Sovereign mid-bridge-rollout without crash-
            # looping the MCP Pod (the MCP surfaces a clean "not configured"
            # error from the affected tool family).
            - name: SANDBOX_TOKEN
              valueFrom:
                secretKeyRef:
                  name: {{ .LLMGatewayTokenSecret | quote }}
                  key: LLM_GATEWAY_TOKEN
                  optional: true
            # TBD-V21 P1 — SANDBOX_JWT_SECRET is the HS256 signing key the
            # MCP plugin's registry uses to validate every bearer claim
            # (registry.go:71 -- empty AND SANDBOX_ORG_ID also empty triggers
            # test-dev mode, but since #1987 SANDBOX_ORG_ID is now always
            # set; without SANDBOX_JWT_SECRET the auth gate degrades to
            # silently passing every call through unvalidated). Key material
            # lives in 'newapi-bp-newapi-token-signing-key' Secret (auto-
            # provisioned by platform/newapi/chart/templates/sandbox-token-
            # signing-key-secret.yaml) and is mirrored into per-Sandbox
            # namespaces via emberstack/reflector -- the chart's
            # 'sandboxTokenSigningKey.reflectorNamespaces' default was
            # extended to 'catalyst-system,sandbox,sandbox-.*' so every
            # per-Sandbox namespace receives a reflected copy. optional:
            # true preserves the MCP's behaviour on Sovereigns mid-reflector-
            # rollout (the MCP detects empty + nonempty SANDBOX_ORG_ID and
            # surfaces a clear "not configured" error rather than test-
            # mode).
            - name: SANDBOX_JWT_SECRET
              valueFrom:
                secretKeyRef:
                  name: {{ .JWTSigningKeySecretName | quote }}
                  key: {{ .JWTSigningKeySecretKey | quote }}
                  optional: true
            # TBD-V21 P3 — SANDBOX_REPOS scopes the MCP plugin's gitea.repos.
            # list handler (env.go:98-106). Without it the handler returns
            # the un-filtered org repo list -- functional but over-discloses.
            # Comma-joined sb.Spec.Repos[].giteaRepo matches the env.go
            # CSV-parse contract.
            - name: SANDBOX_REPOS
              value: {{ .SandboxRepos | quote }}
            # ── D31 active-hot-standby — Sovereign-level toggle + region
            # pair. When SOVEREIGN_ENABLE_HOT_STANDBY parses truthy AND
            # both region values are non-empty AND distinct, sandbox.db.
            # provision materialises a primary + replica Cluster.
            # postgresql.cnpg.io pair instead of a single Cluster (DoD
            # D31). Default-off keeps every existing Sandbox on single-
            # Cluster CNPG (zero regression). The values flow:
            #   bootstrap-kit slot 19a envsubst (per-Sovereign overlay)
            #   -> bp-sandbox HelmRelease values
            #   -> sandbox-controller env (host cluster)
            #   -> here, into every per-Sandbox MCP Pod
            - name: SOVEREIGN_ENABLE_HOT_STANDBY
              value: {{ .EnableHotStandby | quote }}
            - name: SOVEREIGN_PRIMARY_REGION
              value: {{ .PrimaryRegion | quote }}
            - name: SOVEREIGN_REPLICA_REGION
              value: {{ .ReplicaRegion | quote }}
          resources:
            requests:
              cpu: "50m"
              memory: "128Mi"
            limits:
              cpu: "500m"
              memory: "512Mi"
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop: ["ALL"]
            readOnlyRootFilesystem: true
      terminationGracePeriodSeconds: 10
`

const ptyServerServiceTemplate = `apiVersion: v1
kind: Service
metadata:
  name: pty-server
  namespace: {{ .NamespaceName }}
  labels:
    openova.io/sandbox: {{ .Name }}
    openova.io/managed-by: catalyst
    app.kubernetes.io/name: pty-server
spec:
  type: ClusterIP
  selector:
    app.kubernetes.io/name: pty-server
    openova.io/sandbox: {{ .Name }}
  ports:
    - name: http
      port: 7681
      targetPort: 7681
      protocol: TCP
`

const httpRouteTemplate = `apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: pty-server
  namespace: {{ .NamespaceName }}
  labels:
    openova.io/sandbox: {{ .Name }}
    openova.io/managed-by: catalyst
spec:
  # Attach to the canonical Cilium Gateway on the host cluster. PR #1641
  # originally targeted "catalyst-public/catalyst-system/https" — that
  # Gateway does not exist on a Sovereign. The real public Gateway is
  # cilium-gateway/kube-system (clusters/_template/sovereign-tls/
  # cilium-gateway.yaml), matching the placement organization-controller's
  # tenant_route.go and products/catalyst/chart/templates/httproute.yaml
  # already use. sectionName is intentionally omitted so the HTTPRoute
  # attaches to every listener whose hostname matches "sandbox.<sov-fqdn>"
  # — currently the wildcard *.${SOVEREIGN_FQDN} HTTPS listener
  # (https-<sov-fqdn-dashed>) per infra/hetzner/main.tf
  # locals.parent_domains_listeners_yaml fallback path.
  parentRefs:
    - name: cilium-gateway
      namespace: kube-system
  hostnames:
    - "sandbox.{{ .SovereignFQDN }}"
  rules:
    - matches:
        - path:
            type: PathPrefix
            value: /sessions/{{ .OwnerUID }}/
      filters:
        - type: URLRewrite
          urlRewrite:
            path:
              type: ReplacePrefixMatch
              replacePrefixMatch: /sessions/
      backendRefs:
        - name: pty-server
          port: 7681
`

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
  - configmap-mcp-config.yaml
  - statefulset-pty-server.yaml
  - service-pty-server.yaml
  - deployment-mcp.yaml
  - httproute-pty-server.yaml
`

const pvcRepoStorageDefault = "5Gi"

const (
	defaultLLMGatewayTokenSecret = "sandbox-tokens"
	defaultBYOSSecretPrefix      = "sandbox-byos-claude-code"
	defaultIdleTimeoutMinutes    = 30
	defaultConcurrentSessions    = 1

	// TBD-V21 — defaults for SANDBOX_JWT_SECRET wiring. The bp-newapi
	// chart auto-provisions the `newapi-bp-newapi-token-signing-key`
	// Secret carrying SIGNING_KEY and reflects it into every per-Sandbox
	// namespace (sandbox-.* regex pattern in reflectorNamespaces, default
	// since this PR). Operator override flows through chart values to the
	// controller env then into Inputs.
	defaultJWTSigningKeySecretName = "newapi-bp-newapi-token-signing-key"
	defaultJWTSigningKeySecretKey  = "SIGNING_KEY"
)

// Render returns (path, bytes) tuples the reconciler writes into the
// per-Org Gitea repo under `sandbox/<owner-uid>/`.
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
	if strings.TrimSpace(in.PtyServerImage) == "" {
		return nil, fmt.Errorf("Inputs.PtyServerImage is required (Wave 8 pty-server StatefulSet has no default image)")
	}
	if strings.TrimSpace(in.MCPImage) == "" {
		return nil, fmt.Errorf("Inputs.MCPImage is required (Wave 8 openova-sandbox-mcp Deployment has no default image)")
	}
	if strings.TrimSpace(in.NewapiURL) == "" {
		return nil, fmt.Errorf("Inputs.NewapiURL is required (newapi-proxy-contract.md §1 — pty-server env LLM_GATEWAY_URL)")
	}
	if strings.TrimSpace(in.SovereignFQDN) == "" {
		return nil, fmt.Errorf("Inputs.SovereignFQDN is required (HTTPRoute hostname binding)")
	}

	if strings.TrimSpace(in.LLMGatewayTokenSecret) == "" {
		in.LLMGatewayTokenSecret = defaultLLMGatewayTokenSecret
	}
	if strings.TrimSpace(in.BYOSSecretPrefix) == "" {
		in.BYOSSecretPrefix = defaultBYOSSecretPrefix
	}
	if in.IdleTimeoutMinutes <= 0 {
		in.IdleTimeoutMinutes = defaultIdleTimeoutMinutes
	}
	// TBD-V21 — JWTSigningKey defaults pick the canonical bp-newapi
	// Secret + key when caller passes empty. The chart-level override
	// flows through the controller env into Inputs; explicit empty falls
	// back here.
	if strings.TrimSpace(in.JWTSigningKeySecretName) == "" {
		in.JWTSigningKeySecretName = defaultJWTSigningKeySecretName
	}
	if strings.TrimSpace(in.JWTSigningKeySecretKey) == "" {
		in.JWTSigningKeySecretKey = defaultJWTSigningKeySecretKey
	}

	ns := fmt.Sprintf("sandbox-%s", in.OwnerUID)

	repos := make([]sandboxapi.SandboxRepo, len(in.Repos))
	copy(repos, in.Repos)
	sort.SliceStable(repos, func(i, j int) bool {
		return repos[i].GiteaRepo < repos[j].GiteaRepo
	})

	// TBD-V21 — SANDBOX_REPOS env value: comma-joined list of giteaRepo
	// slugs from sb.Spec.Repos (stable sort order via `repos`). MCP's
	// env.go:98-106 splits on comma + trims whitespace, so we emit a
	// canonical CSV that round-trips through the consumer parse.
	repoSlugs := make([]string, 0, len(repos))
	for _, r := range repos {
		if s := strings.TrimSpace(r.GiteaRepo); s != "" {
			repoSlugs = append(repoSlugs, s)
		}
	}
	in.SandboxRepos = strings.Join(repoSlugs, ",")

	type baseCtx struct {
		Inputs
		NamespaceName string
	}
	base := baseCtx{Inputs: in, NamespaceName: ns}

	out := make(map[string][]byte, 12+len(repos))

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

	// Wave 8 runtime — pty-server StatefulSet, MCP Deployment,
	// pty-server Service, HTTPRoute.
	type runtimeRepo struct {
		Slug string
	}
	runtimeRepos := make([]runtimeRepo, 0, len(repos))
	for _, r := range repos {
		runtimeRepos = append(runtimeRepos, runtimeRepo{Slug: sanitizeRepoSlug(r.GiteaRepo)})
	}
	replicas := in.Quota.ConcurrentSessions
	if replicas <= 0 {
		replicas = defaultConcurrentSessions
	}
	byosActive := agentInCatalogue(in.AgentCatalogue, "claude-code")
	byosSecretName := fmt.Sprintf("%s-%s", in.BYOSSecretPrefix, in.OwnerUID)

	type runtimeCtx struct {
		Inputs
		NamespaceName        string
		Replicas             int
		RuntimeRepos         []runtimeRepo
		ClaudeCodeBYOSActive bool
		BYOSSecretName       string
	}
	rctx := runtimeCtx{
		Inputs:               in,
		NamespaceName:        ns,
		Replicas:             replicas,
		RuntimeRepos:         runtimeRepos,
		ClaudeCodeBYOSActive: byosActive,
		BYOSSecretName:       byosSecretName,
	}
	for path, raw := range map[string]string{
		"statefulset-pty-server.yaml": ptyServerStatefulSetTemplate,
		"service-pty-server.yaml":     ptyServerServiceTemplate,
		"deployment-mcp.yaml":         mcpDeploymentTemplate,
		"httproute-pty-server.yaml":   httpRouteTemplate,
		// TBD-P4 B3 (#1986) — `configmap-mcp-config.yaml` carries the
		// canonical `mcp.json` that agent CLIs read on session start to
		// auto-discover openova-sandbox-mcp. The pty-server StatefulSet
		// mounts this ConfigMap at every canonical per-agent path
		// (~/.claude.json, ~/.qwen/settings.json, ./.mcp.json,
		// .cursor/mcp.json). See mcpConfigMapTemplate for the full
		// design discussion.
		"configmap-mcp-config.yaml": mcpConfigMapTemplate,
	} {
		buf, err := renderTemplate(path, raw, rctx)
		if err != nil {
			return nil, err
		}
		out[path] = buf
	}
	_ = base

	return out, nil
}

func agentInCatalogue(catalogue []string, agent string) bool {
	want := strings.ToLower(strings.TrimSpace(agent))
	for _, a := range catalogue {
		if strings.ToLower(strings.TrimSpace(a)) == want {
			return true
		}
	}
	return false
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
		"quote": func(v any) string { return fmt.Sprintf("%q", fmt.Sprintf("%v", v)) },
	}
}

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
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	out = strings.Trim(out, "-")
	if len(out) > 200 {
		out = strings.Trim(out[:200], "-")
	}
	return out
}
