package provisioner

import (
	"fmt"
	"os"
	"path/filepath"
	"text/template"
)

// TenantManifests generates the K8s manifests for a tenant's vCluster.
// In production, these are committed to the Git repo for Flux reconciliation.
type TenantManifests struct {
	TenantID    string
	Subdomain   string
	Size        string
	Apps        []string
	RepoPath    string
}

// SizeResources maps t-shirt sizes to resource quotas.
var SizeResources = map[string]struct {
	CPU     string
	Memory  string
	Storage string
}{
	"xs": {CPU: "250m", Memory: "256Mi", Storage: "2Gi"},
	"s":  {CPU: "500m", Memory: "512Mi", Storage: "5Gi"},
	"m":  {CPU: "1", Memory: "1Gi", Storage: "10Gi"},
	"l":  {CPU: "2", Memory: "2Gi", Storage: "20Gi"},
}

const vclusterTemplate = `apiVersion: helm.toolkit.fluxcd.io/v2beta1
kind: HelmRelease
metadata:
  name: vc-{{.Subdomain}}
  namespace: marketplace
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
        - --name=vc-{{.Subdomain}}
    storage:
      persistence: true
      size: {{.Storage}}
`

const resourceQuotaTemplate = `apiVersion: v1
kind: ResourceQuota
metadata:
  name: tenant-quota
  namespace: vc-{{.Subdomain}}
spec:
  hard:
    requests.cpu: "{{.CPU}}"
    requests.memory: "{{.Memory}}"
    requests.storage: "{{.Storage}}"
    limits.cpu: "{{.CPU}}"
    limits.memory: "{{.Memory}}"
`

const networkPolicyTemplate = `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: tenant-isolation
  namespace: vc-{{.Subdomain}}
spec:
  podSelector: {}
  policyTypes:
    - Ingress
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: vc-{{.Subdomain}}
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: traefik
`

const kustomizationTemplate = `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: marketplace
resources:
  - vcluster.yaml
  - resource-quota.yaml
  - network-policy.yaml
{{- range .Apps}}
  - apps/{{.}}/helmrelease.yaml
{{- end}}
`

const appHelmReleaseTemplate = `apiVersion: helm.toolkit.fluxcd.io/v2beta1
kind: HelmRelease
metadata:
  name: {{.AppSlug}}
  namespace: vc-{{.Subdomain}}
spec:
  interval: 10m
  chart:
    spec:
      chart: {{.AppSlug}}
      sourceRef:
        kind: HelmRepository
        name: bitnami
        namespace: flux-system
  values: {}
`

// GenerateManifests writes all K8s manifests for a tenant to the repo path.
func (tm *TenantManifests) GenerateManifests() error {
	resources, ok := SizeResources[tm.Size]
	if !ok {
		return fmt.Errorf("unknown size: %s", tm.Size)
	}

	tenantDir := filepath.Join(tm.RepoPath, "clusters/contabo-mkt/tenants", tm.TenantID)
	appsDir := filepath.Join(tenantDir, "apps")

	if err := os.MkdirAll(appsDir, 0o755); err != nil {
		return fmt.Errorf("create tenant dir: %w", err)
	}

	data := struct {
		Subdomain string
		CPU       string
		Memory    string
		Storage   string
		Apps      []string
	}{
		Subdomain: tm.Subdomain,
		CPU:       resources.CPU,
		Memory:    resources.Memory,
		Storage:   resources.Storage,
		Apps:      tm.Apps,
	}

	templates := map[string]string{
		"vcluster.yaml":        vclusterTemplate,
		"resource-quota.yaml":  resourceQuotaTemplate,
		"network-policy.yaml":  networkPolicyTemplate,
		"kustomization.yaml":   kustomizationTemplate,
	}

	for filename, tmplStr := range templates {
		if err := writeTemplate(filepath.Join(tenantDir, filename), tmplStr, data); err != nil {
			return fmt.Errorf("write %s: %w", filename, err)
		}
	}

	// Generate per-app HelmRelease
	for _, appSlug := range tm.Apps {
		appDir := filepath.Join(appsDir, appSlug)
		if err := os.MkdirAll(appDir, 0o755); err != nil {
			return fmt.Errorf("create app dir: %w", err)
		}

		appData := struct {
			AppSlug   string
			Subdomain string
		}{
			AppSlug:   appSlug,
			Subdomain: tm.Subdomain,
		}

		if err := writeTemplate(filepath.Join(appDir, "helmrelease.yaml"), appHelmReleaseTemplate, appData); err != nil {
			return fmt.Errorf("write app helmrelease for %s: %w", appSlug, err)
		}
	}

	return nil
}

func writeTemplate(path, tmplStr string, data any) error {
	tmpl, err := template.New("").Parse(tmplStr)
	if err != nil {
		return err
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	return tmpl.Execute(f, data)
}
