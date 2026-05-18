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
            - name: LLM_GATEWAY_TOKEN
              valueFrom:
                secretKeyRef:
                  name: {{ .LLMGatewayTokenSecret | quote }}
                  key: llm-gateway-token
                  optional: true
            - name: OPENAI_API_KEY
              valueFrom:
                secretKeyRef:
                  name: {{ .LLMGatewayTokenSecret | quote }}
                  key: llm-gateway-token
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
            - name: ORG_ID
              value: {{ .OrgSlug | quote }}
            - name: SOVEREIGN_FQDN
              value: {{ .SovereignFQDN | quote }}
            - name: PTY_SERVER_URL
              value: "http://pty-server.{{ .NamespaceName }}.svc.cluster.local:7681"
            - name: LLM_GATEWAY_TOKEN
              valueFrom:
                secretKeyRef:
                  name: {{ .LLMGatewayTokenSecret | quote }}
                  key: llm-gateway-token
                  optional: true
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
  parentRefs:
    - name: catalyst-public
      namespace: catalyst-system
      sectionName: https
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
{{- range .RepoPaths }}
  - {{ . }}
{{- end }}
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

	ns := fmt.Sprintf("sandbox-%s", in.OwnerUID)

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

	out := make(map[string][]byte, 11+len(repos))

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

	sort.Strings(repoPaths)
	type kustCtx struct {
		Inputs
		NamespaceName string
		RepoPaths     []string
	}
	kustBuf, err := renderTemplate("kustomization.yaml", kustomizationTemplate, kustCtx{
		Inputs:        in,
		NamespaceName: ns,
		RepoPaths:     repoPaths,
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
