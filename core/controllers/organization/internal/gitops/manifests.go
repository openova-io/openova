// Package gitops renders the per-Organization manifests that Flux on
// the host cluster reconciles into a vCluster + supporting resources.
//
// Per ADR-0001 §2.1 (GitOps is the only deployment path) and EPICS-1-6
// §3.7 the controller writes a HelmRelease + Namespace + ingress
// manifest into the per-Org Gitea repo. Flux on the host cluster's
// `clusters/<host>/tenants/<org>/` Kustomization picks them up.
//
// Per docs/NAMING-CONVENTION.md §1.5 Org identity lives in the
// vCluster name (= the Organization slug); the namespace on the host
// cluster is also the slug per §4.6. Resource names below the namespace
// don't repeat the slug.
//
// Per EPICS-1-6 §1.1 every Catalyst-managed resource carries the
// canonical label set; rendered manifests include them.

package gitops

import (
	"bytes"
	"fmt"
	"text/template"

	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
)

// Inputs is the subset of Organization spec the renderer needs. The
// reconciler builds this from the CR + controller-level config (chart
// version, vcluster repo URL).
type Inputs struct {
	// Slug is the Organization slug (= vCluster name = Gitea Org name).
	Slug string

	// DisplayName is the human-readable Organization name (label-safe
	// or annotation only — display strings don't go in resource names).
	DisplayName string

	// Tier is sme | corporate.
	Tier string

	// SovereignFQDN is the Sovereign domain (e.g. omantel.omani.works).
	SovereignFQDN string

	// HostCluster is the canonical name of the host cluster the
	// vCluster lives on (e.g. hz-fsn-rtz-prod). Drives the
	// openova.io/host-cluster label per NAMING §6.2.
	HostCluster string

	// VClusterChartVersion is the SemVer constraint for the upstream
	// vcluster chart. Per Inviolable Principle #4 this is configurable
	// (env var) — never hardcoded in the renderer.
	VClusterChartVersion string

	// VClusterHelmRepoName is the in-cluster Flux HelmRepository to
	// source the vcluster chart from. Default: "loft" in
	// "vcluster-system" — matches clusters/contabo-mkt/tenants/test/.
	VClusterHelmRepoName      string
	VClusterHelmRepoNamespace string

	// VClusterImageRegistry is the Sovereign-local Harbor host every
	// vCluster image (syncer StatefulSet + the k8s-distro init image)
	// pulls through. Default "harbor.openova.io".
	//
	// MIRROR-EVERYTHING (#3760, Refs #3376 #3754): the per-Org vCluster
	// StatefulSet is admission-gated by the `harbor-proxy-pull` Kyverno
	// ClusterPolicy (Enforce), which DENIES any image not matching the
	// `*/proxy-*/*` glob. vcluster 0.33.x renders TWO denied initContainer
	// images off ghcr.io — the k8s distro `loft-sh/kubernetes` AND the
	// `loft-sh/vcluster-oss` syncer — so BOTH must be re-tagged through
	// the Sovereign Harbor proxy-cache (`<registry>/proxy-ghcr/loft-sh/...`),
	// exactly like the platform's own bp-dmz/mgmt/rtz-vcluster charts.
	// Per Inviolable Principle #4 it's read from env, never hardcoded;
	// cutover Step-04 (ADR-0002) flips it to `harbor.<sovereign-fqdn>`
	// post-handover.
	VClusterImageRegistry string
}

// renderTemplates is the named template set the controller uses.
// Keep these inline (text/template) — the rendered output is YAML
// that Flux applies via Kustomization. Per Inviolable Principle #4
// no values are hardcoded inside; every knob comes from Inputs.
const namespaceTemplate = `apiVersion: v1
kind: Namespace
metadata:
  name: {{ .Slug }}
  labels:
    openova.io/organization: {{ .Slug }}
    openova.io/host-cluster: {{ .HostCluster }}
    openova.io/sovereign: {{ .SovereignFQDN }}
    openova.io/managed-by: catalyst
    openova.io/tier: {{ .Tier }}
  annotations:
    openova.io/display-name: {{ .DisplayName | quote }}
`

const vclusterTemplate = `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: vcluster
  namespace: {{ .Slug }}
  labels:
    openova.io/organization: {{ .Slug }}
    openova.io/vcluster: {{ .Slug }}
    openova.io/host-cluster: {{ .HostCluster }}
    openova.io/sovereign: {{ .SovereignFQDN }}
    openova.io/managed-by: flux
    app.kubernetes.io/managed-by: flux
spec:
  interval: 10m
  chart:
    spec:
      chart: vcluster
      version: {{ .VClusterChartVersion | quote }}
      sourceRef:
        kind: HelmRepository
        name: {{ .VClusterHelmRepoName }}
        namespace: {{ .VClusterHelmRepoNamespace }}
  values:
    controlPlane:
      distro:
        k8s:
          enabled: true
          # MIRROR-EVERYTHING (#3760): the k8s-distro image is
          # initContainers[0] of the vcluster StatefulSet (vcluster 0.33.x
          # renders ghcr.io/loft-sh/kubernetes:vX). The harbor-proxy-pull
          # Kyverno ClusterPolicy (Enforce) DENIES it off ghcr.io — re-tag
          # through the Sovereign Harbor proxy-cache so it matches the
          # */proxy-*/* glob, lockstep with bp-dmz/mgmt/rtz-vcluster.
          image:
            registry: {{ .VClusterImageRegistry }}
            repository: proxy-ghcr/loft-sh/kubernetes
      backingStore:
        database:
          embedded:
            enabled: true
      statefulSet:
        # MIRROR-EVERYTHING (#3760): the syncer image is initContainers[1]
        # (+ the main container). Pull it through the Sovereign Harbor
        # proxy-cache too — ghcr.io is denied by harbor-proxy-pull.
        image:
          registry: {{ .VClusterImageRegistry }}
          repository: proxy-ghcr/loft-sh/vcluster-oss
        resources:
          requests:
            cpu: 100m
            memory: 192Mi
          limits:
            cpu: 2000m
            memory: 2Gi
        persistence:
          volumeClaim:
            size: 5Gi
      service:
        enabled: true
        spec:
          type: ClusterIP
    exportKubeConfig:
      context: vcluster
      server: https://vcluster.{{ .Slug }}:443
      insecure: false
      additionalSecrets:
        - name: vc-vcluster
          server: https://vcluster.{{ .Slug }}:443
          insecure: false
          context: vcluster
    sync:
      toHost:
        services:
          enabled: true
        ingresses:
          enabled: false
      fromHost:
        ingressClasses:
          enabled: true
`

const kustomizationTemplate = `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - namespace.yaml
  - vcluster.yaml
`

// Render returns the rendered (path, bytes) tuples the controller
// writes into the per-Org Gitea repo.
func Render(in Inputs) (map[string][]byte, error) {
	if in.VClusterHelmRepoName == "" {
		in.VClusterHelmRepoName = "loft"
	}
	if in.VClusterHelmRepoNamespace == "" {
		in.VClusterHelmRepoNamespace = "vcluster-system"
	}
	if in.VClusterImageRegistry == "" {
		// Bootstrap default — the Sovereign-local Harbor. Matches the
		// `global.registryMirror` default in bp-dmz/mgmt/rtz-vcluster.
		// Cutover Step-04 flips it to harbor.<sovereign-fqdn> per ADR-0002.
		in.VClusterImageRegistry = "harbor.openova.io"
	}
	if in.Tier == "" {
		in.Tier = "sme"
	}
	out := make(map[string][]byte, 3)
	for path, raw := range map[string]string{
		"vcluster/namespace.yaml":     namespaceTemplate,
		"vcluster/vcluster.yaml":      vclusterTemplate,
		"vcluster/kustomization.yaml": kustomizationTemplate,
	} {
		t, err := template.New(path).Funcs(funcs()).Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("template parse %s: %w", path, err)
		}
		var buf bytes.Buffer
		if err := t.Execute(&buf, in); err != nil {
			return nil, fmt.Errorf("template execute %s: %w", path, err)
		}
		out[path] = buf.Bytes()
	}
	return out, nil
}

func funcs() template.FuncMap {
	return template.FuncMap{
		"quote": func(s string) string { return fmt.Sprintf("%q", s) },
	}
}

// OwnersFromOrg extracts spec.owners[] for re-use by useraccess writers.
func OwnersFromOrg(o *orgapi.Organization) []orgapi.OrganizationOwner {
	if o == nil {
		return nil
	}
	out := make([]orgapi.OrganizationOwner, len(o.Spec.Owners))
	copy(out, o.Spec.Owners)
	return out
}
