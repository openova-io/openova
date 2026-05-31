// Package render produces the YAML manifest set the
// application-controller writes to the per-Org Gitea repo for each
// (Application × region) tuple.
//
// Per slice C4 brief §4 (per-region manifest write): each Application
// reconcile emits, per region:
//
//	clusters/<host-cluster>/applications/<app>/kustomization.yaml
//	clusters/<host-cluster>/applications/<app>/helmrelease.yaml
//
// The HelmRelease references the Blueprint's `spec.manifests.source.ref`
// (or legacy short-form `spec.manifests.chart`) and carries `values:`
// merged from `Application.spec.parameters` + Blueprint defaults at
// `spec.manifests.values`.
//
// Per Inviolable Principle #4 (never hardcode) and slice C2's
// precedent, manifests are rendered as text/template directly. The
// alternative — depending on flux/source-controller and helm-controller
// Go types — would double the binary size and broaden the supply-chain
// attack surface (slice C2 brief §3.1 cited the same trade-off).
//
// The output is stable byte-for-byte across reconcile passes for a
// given input — `text/template` field ordering is deterministic, and
// the values block is rendered through `yaml.Marshal` which sorts map
// keys. Idempotency (the slice C4 brief test #7) hinges on this.
package render

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"text/template"

	"sigs.k8s.io/yaml"
)

// Inputs holds everything needed to render one (Application × region)
// manifest set.
type Inputs struct {
	// AppName is the Application CR's metadata.name. Used for the
	// HelmRelease name + the in-Gitea path segment.
	AppName string

	// AppNamespace is the Application CR's metadata.namespace. The
	// rendered HelmRelease lives in this namespace (so the host-side
	// Flux Kustomization, which targetNamespace = AppNamespace, owns
	// it) AND the workload installs INTO this namespace via
	// `spec.targetNamespace`. qa-loop iter-10 Fix #44 root-cause:
	// previously we used `Org` for both — for omantel where the App
	// CR lives in `qa-omantel` but the Org is `omantel-platform`, the
	// HelmRelease was committed targeting the wrong namespace and the
	// nginx Pod landed in `omantel-platform` instead of `qa-omantel`.
	// Per docs/INVIOLABLE-PRINCIPLES.md #1 (target-state) the
	// controller MUST honour the operator's chosen Application
	// namespace. Defaults to Org when empty (back-compat for callers
	// that haven't been updated yet — controllers should always pass
	// app.GetNamespace()).
	AppNamespace string

	// Org is the parent Organization slug. Stamped on labels for
	// traceability. NOT used as the K8s namespace anymore — see
	// AppNamespace above.
	Org string

	// EnvType is the Environment's type ("dev" / "stg" / "prod" /
	// "uat" / "poc"). Stamped onto labels for traceability.
	EnvType string

	// Region is the host-cluster name (e.g. hetzner-fsn-rtz-prod).
	// Used as the `clusters/<region>/...` path segment in Gitea.
	Region string

	// PlacementRole is one of {primary, standby, active}. Stamped on
	// labels. Standby regions render with `replicas: 0` overlays.
	PlacementRole string

	// Standby reports whether this region is a standby (active-
	// hotstandby's regions[1..]). When true the renderer overlays
	// `replicas: 0` on the values block.
	Standby bool

	// BlueprintName is the parent Blueprint's metadata.name (e.g.
	// bp-wordpress). Stamped on labels + used to compose the
	// HelmRelease.spec.chart.spec.chart reference.
	BlueprintName string

	// BlueprintVersion is the resolved Blueprint version (exact
	// MAJOR.MINOR.PATCH). Stamped on labels + used as the
	// HelmRelease.spec.chart.spec.version.
	BlueprintVersion string

	// SourceKind is the Flux source kind from
	// Blueprint.spec.manifests.source.kind. One of {HelmChart,
	// Kustomize, OAM}. Defaults to HelmChart when unset (matches the
	// CRD's enum).
	SourceKind string

	// SourceRef is the source ref from Blueprint.spec.manifests.source.ref
	// — the Flux GitRepository / HelmRepository / OCIRepository name
	// the source-controller resolves. For the legacy `chart` short-form
	// (Blueprint authored without `source` block) this falls back to
	// the chart name verbatim and the renderer points at the catalog
	// HelmRepository.
	SourceRef string

	// Chart is the chart name (Blueprint.spec.manifests.chart for the
	// legacy short-form, or composed from BlueprintName for the
	// HelmRepository fallback). When empty it defaults to BlueprintName.
	Chart string

	// Values is the merged user parameters + Blueprint defaults,
	// rendered as YAML under `helmrelease.spec.values`. Standby
	// regions get a `replicas: 0` overlay applied here.
	Values map[string]interface{}

	// SourceNamespace is the K8s namespace that holds the source
	// (HelmRepository / GitRepository / OCIRepository) Flux watches.
	// Defaults to "flux-system".
	SourceNamespace string

	// IntervalSeconds is the HelmRelease reconcile interval. Per
	// Inviolable Principle #4 this is configurable. Defaults to 600s
	// — long enough that hand-edits don't cost a constant Helm churn,
	// short enough that legitimate values changes propagate within
	// 10 minutes.
	IntervalSeconds int

	// OwnerAppUID + OwnerAppGen are stamped on labels for traceability
	// (matches slice C2's RenderInputs pattern). The reconciler uses
	// these to confirm a manifest in Gitea was written by THIS
	// Application CR (not a prior CR with the same name that has been
	// deleted + recreated).
	OwnerAppUID string
	OwnerAppGen int64

	// VCluster is the per-region vCluster the rendered HelmRelease
	// targets. Empty string = install onto the host k3s apiserver.
	// "dmz" | "mgmt" | "rtz" = install onto the named vCluster's
	// apiserver via Flux helm-controller's `spec.kubeconfig.secretRef`
	// pivot. Per docs/SOVEREIGN-MULTI-REGION-DOD.md §A4 the three
	// per-region vClusters partition the workloads:
	//   dmz   — public-fronted (Cilium Gateway + WAF + HTTPRoutes + SMTP)
	//   mgmt  — control-plane (catalyst-api/ui, Keycloak, OpenBao, NATS, Gitea, Harbor)
	//   rtz   — tenant Applications + Sandbox + per-Org CNPG
	//   ""    — host substrate (Cilium-agent, Flux, Kyverno, cert-manager, ESO, CNPG-operator)
	//
	// G92.1 #2660 (2026-06-01): without this knob every bp-* HR
	// installed onto the host k3s — the three vClusters were created at
	// bootstrap (slots 54/58/59) but stood empty. Founder direction:
	// "vclusters are not there for fun purpose, they are there for
	// containing the applications".
	VCluster string

	// VClusterHostNamespace is the host-cluster namespace the vCluster's
	// pods + admin kubeconfig Secret live in. By convention
	// (platform/bp-<vcluster>-vcluster chart values) this equals the
	// vCluster's name — `dmz` for dmz, `mgmt` for mgmt, `rtz` for rtz.
	// Stamped on `metadata.namespace` of the rendered HelmRelease when
	// VCluster is non-empty (helm-controller resolves the kubeconfig
	// Secret from the same namespace as the HR).
	VClusterHostNamespace string

	// VClusterKubeconfigSecret is the name of the Secret the vCluster
	// chart exports with the admin kubeconfig. By upstream loft-sh/
	// vcluster convention this is `vc-<vclusterName>` — `vc-dmz`,
	// `vc-mgmt`, `vc-rtz`. Stamped on
	// `spec.kubeconfig.secretRef.name`.
	VClusterKubeconfigSecret string
}

// Source kind constants — mirror the Blueprint CRD enum.
const (
	SourceKindHelmChart = "HelmChart"
	SourceKindKustomize = "Kustomize"
	SourceKindOAM       = "OAM"
)

// Result is the per-region rendered manifest set.
type Result struct {
	// KustomizationYAML is the bytes for clusters/<region>/applications/<app>/kustomization.yaml.
	KustomizationYAML []byte

	// HelmReleaseYAML is the bytes for clusters/<region>/applications/<app>/helmrelease.yaml.
	HelmReleaseYAML []byte
}

// Render produces the per-region manifest set. Pure function; no I/O.
func Render(in Inputs) (Result, error) {
	if err := in.validate(); err != nil {
		return Result{}, err
	}
	in.applyDefaults()

	values := mergeValues(in.Values, in.Standby)
	valuesYAML, err := yaml.Marshal(values)
	if err != nil {
		return Result{}, fmt.Errorf("render: marshal values: %w", err)
	}

	hr, err := renderHelmRelease(in, string(valuesYAML))
	if err != nil {
		return Result{}, fmt.Errorf("render: helmrelease: %w", err)
	}

	kust, err := renderKustomization(in)
	if err != nil {
		return Result{}, fmt.Errorf("render: kustomization: %w", err)
	}

	return Result{
		KustomizationYAML: kust,
		HelmReleaseYAML:   hr,
	}, nil
}

func (in Inputs) validate() error {
	missing := []string{}
	if in.AppName == "" {
		missing = append(missing, "AppName")
	}
	if in.Org == "" {
		missing = append(missing, "Org")
	}
	if in.EnvType == "" {
		missing = append(missing, "EnvType")
	}
	if in.Region == "" {
		missing = append(missing, "Region")
	}
	if in.BlueprintName == "" {
		missing = append(missing, "BlueprintName")
	}
	if in.BlueprintVersion == "" {
		missing = append(missing, "BlueprintVersion")
	}
	if len(missing) > 0 {
		return fmt.Errorf("render: missing required Inputs fields: %s", strings.Join(missing, ", "))
	}
	return nil
}

func (in *Inputs) applyDefaults() {
	if in.SourceKind == "" {
		in.SourceKind = SourceKindHelmChart
	}
	if in.Chart == "" {
		in.Chart = in.BlueprintName
	}
	if in.SourceRef == "" {
		in.SourceRef = "openova-catalog"
	}
	if in.SourceNamespace == "" {
		in.SourceNamespace = "flux-system"
	}
	if in.IntervalSeconds <= 0 {
		in.IntervalSeconds = 600
	}
	if in.PlacementRole == "" {
		in.PlacementRole = "primary"
	}
	// Back-compat: if AppNamespace is unset, fall back to Org so older
	// callers still produce valid output (the legacy bug-compatible
	// shape). All in-tree callers now pass AppNamespace explicitly.
	if in.AppNamespace == "" {
		in.AppNamespace = in.Org
	}
	// vCluster placement defaults: when VCluster is set but the
	// derived host namespace + Secret name are not, fall back to the
	// upstream loft-sh/vcluster convention (vCluster name = host
	// namespace = `vc-<name>` Secret). The controller is free to
	// override either via Config — this default keeps the renderer
	// self-contained for `helm template` smoke + idempotent rendering
	// when called from tests.
	if in.VCluster != "" {
		if in.VClusterHostNamespace == "" {
			in.VClusterHostNamespace = in.VCluster
		}
		if in.VClusterKubeconfigSecret == "" {
			in.VClusterKubeconfigSecret = "vc-" + in.VCluster
		}
	}
}

// mergeValues overlays standby flags onto the user values when the
// region is a standby. The convention `replicas: 0` works for the
// standard Deployment / StatefulSet HPA pattern; CNPG-style resources
// that key off `replica: false` (the bp-cnpg-pair Blueprint) read
// `_openova_standby: true` from a top-level marker — Continuum
// (#1101) sets the boolean directly on switchover.
func mergeValues(user map[string]interface{}, standby bool) map[string]interface{} {
	out := make(map[string]interface{}, len(user)+1)
	for k, v := range user {
		out[k] = v
	}
	if standby {
		// `replicas: 0` is the universal Helm-chart standby signal for
		// Deployment / StatefulSet kinds. We always overlay it; charts
		// that don't read `.Values.replicas` simply ignore it.
		out["replicas"] = 0
		// Marker for downstream operators (CNPG, Cassandra, ...) that
		// need a boolean rather than an integer count. This is the
		// canonical Openova standby marker per
		// docs/EPICS-1-6-unified-design.md §5.
		out["_openova_standby"] = true
	}
	return out
}

// helmReleaseTemplate renders a Flux HelmRelease CR.
//
// Per slice C2 precedent we render YAML directly via text/template and
// pull the values block in pre-marshaled. The values block is indented
// 4 spaces (under `spec.values:`). To keep the indent right, the
// template caller indents every newline-prefixed line of valuesYAML
// before pasting.
const helmReleaseTemplate = `# Generated by application-controller (slice C4 of EPIC-0 #1095).
# Do not edit by hand — every reconcile pass overwrites this file.
#
# Application: {{ .AppName }} (org={{ .Org }}, envType={{ .EnvType }})
# Region:      {{ .Region }} (role={{ .PlacementRole }}{{ if .Standby }}, standby{{ end }})
# Blueprint:   {{ .BlueprintName }}@{{ .BlueprintVersion }}
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: {{ .AppName }}
  # HR.metadata.namespace = vCluster's host namespace when VCluster is
  # set (so helm-controller resolves spec.kubeconfig.secretRef from the
  # SAME namespace as the HR — Flux v2 semantic), else the Application
  # CR's own namespace. G92.1 #2660.
  namespace: {{ .HRNamespace }}
  labels:
    app.kubernetes.io/managed-by: application-controller
    app.kubernetes.io/name: {{ .AppName }}
    catalyst.openova.io/application: {{ .AppName }}
    catalyst.openova.io/organization: {{ .Org }}
    catalyst.openova.io/env-type: {{ .EnvType }}
    catalyst.openova.io/region: {{ .Region }}
    catalyst.openova.io/placement-role: {{ .PlacementRole }}
    catalyst.openova.io/blueprint: {{ .BlueprintName }}
{{- if .VCluster }}
    catalyst.openova.io/vcluster: {{ .VCluster }}
{{- end }}
{{- if .OwnerAppUID }}
    catalyst.openova.io/application-uid: "{{ .OwnerAppUID }}"
{{- end }}
{{- if .OwnerAppGen }}
    catalyst.openova.io/application-generation: "{{ .OwnerAppGen }}"
{{- end }}
spec:
  interval: {{ .IntervalSeconds }}s
  releaseName: {{ .AppName }}
  # targetNamespace = the Application CR's own namespace (NOT the Org
  # slug). qa-loop iter-10 Fix #44: prior versions used Org which on
  # omantel resolved to "omantel-platform" while the Application lived
  # in "qa-omantel" — the workload Pod landed in the wrong namespace.
  #
  # When VCluster is set, this is the namespace INSIDE the vCluster's
  # apiserver (the vCluster syncer mirrors it back to the host as
  # `<inner-ns>-x-<vcluster>`). The host syncer mirror is invisible to
  # helm-controller — it sees the vCluster's own ns view.
  targetNamespace: {{ .AppNamespace }}
{{- if .VCluster }}
  # vCluster pivot — helm-controller pulls the kubeconfig from the
  # named Secret in this HR's own namespace and installs the chart
  # INSIDE the vCluster, not on the host. Per Flux v2
  # helm.toolkit.fluxcd.io/v2 HelmRelease contract
  # (spec.kubeconfig.secretRef). G92.1 #2660 (2026-06-01).
  kubeConfig:
    secretRef:
      name: {{ .VClusterKubeconfigSecret }}
      key: config
{{- end }}
  chart:
    spec:
      chart: {{ .Chart }}
      version: {{ .BlueprintVersion }}
      sourceRef:
        kind: {{ .SourceKindCR }}
        name: {{ .SourceRef }}
        namespace: {{ .SourceNamespace }}
      interval: {{ .IntervalSeconds }}s
  install:
    # createNamespace = true so Flux/helm-controller provisions the
    # targetNamespace if absent. Per docs/INVIOLABLE-PRINCIPLES.md #1
    # (target-state): the controller works without an operator
    # pre-creating the namespace.
    createNamespace: true
    remediation:
      retries: 3
  upgrade:
    remediation:
      retries: 3
{{- if .ValuesYAML }}
  values:
{{ .ValuesIndented }}
{{- end }}
`

// renderHelmRelease assembles the HelmRelease YAML bytes.
func renderHelmRelease(in Inputs, valuesYAML string) ([]byte, error) {
	tmpl, err := template.New("helmrelease").Parse(helmReleaseTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}

	// Map source.kind enum → Flux sourceRef kind. HelmChart per the
	// Blueprint CRD is shorthand for "the catalog HelmRepository";
	// the controller maps it to the Flux CR `HelmRepository`.
	sourceKindCR := "HelmRepository"
	switch in.SourceKind {
	case SourceKindKustomize:
		sourceKindCR = "GitRepository"
	case SourceKindOAM:
		sourceKindCR = "OCIRepository"
	default:
		sourceKindCR = "HelmRepository"
	}

	indented := indentYAMLBlock(valuesYAML, 4)
	// HRNamespace = the host-cluster namespace the HelmRelease CR
	// itself lives in. For vCluster placement this MUST be the vCluster's
	// host namespace because Flux helm-controller resolves
	// spec.kubeConfig.secretRef from the same namespace as the HR (Flux
	// v2 SecretReference contract). For host placement it stays the
	// Application's own namespace (the legacy shape).
	hrNamespace := in.AppNamespace
	if in.VCluster != "" {
		hrNamespace = in.VClusterHostNamespace
	}
	data := struct {
		Inputs
		SourceKindCR   string
		ValuesYAML     string
		ValuesIndented string
		HRNamespace    string
	}{
		Inputs:         in,
		SourceKindCR:   sourceKindCR,
		ValuesYAML:     valuesYAML,
		ValuesIndented: indented,
		HRNamespace:    hrNamespace,
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("execute template: %w", err)
	}
	return buf.Bytes(), nil
}

// kustomizationTemplate renders a minimal Kustomization that wraps the
// helmrelease.yaml in the same directory. Flux on the host cluster
// applies this from the per-vCluster GitRepository (slice C2's seed).
const kustomizationTemplate = `# Generated by application-controller (slice C4 of EPIC-0 #1095).
# Do not edit by hand.
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
# namespace = the Application CR's own namespace. qa-loop iter-10
# Fix #44: was previously Org slug — see helmrelease.yaml comment.
namespace: {{ .AppNamespace }}
commonLabels:
  app.kubernetes.io/managed-by: application-controller
  catalyst.openova.io/application: {{ .AppName }}
  catalyst.openova.io/organization: {{ .Org }}
  catalyst.openova.io/region: {{ .Region }}
resources:
  - helmrelease.yaml
`

func renderKustomization(in Inputs) ([]byte, error) {
	tmpl, err := template.New("kustomization").Parse(kustomizationTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, in); err != nil {
		return nil, fmt.Errorf("execute template: %w", err)
	}
	return buf.Bytes(), nil
}

// indentYAMLBlock prepends `n` spaces to every line of `s` so it can
// be embedded under a YAML key. Empty lines are preserved (no indent
// stamped on them — keeps pure-whitespace lines from accumulating).
func indentYAMLBlock(s string, n int) string {
	if s == "" {
		return ""
	}
	prefix := strings.Repeat(" ", n)
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	out := make([]string, len(lines))
	for i, l := range lines {
		if l == "" {
			out[i] = ""
			continue
		}
		out[i] = prefix + l
	}
	return strings.Join(out, "\n")
}

// HelmReleasePath returns the in-Gitea-repo path the controller commits
// the HelmRelease bytes to.
func HelmReleasePath(region, app string) string {
	return fmt.Sprintf("clusters/%s/applications/%s/helmrelease.yaml", region, app)
}

// KustomizationPath returns the in-Gitea-repo path for the kustomization.
func KustomizationPath(region, app string) string {
	return fmt.Sprintf("clusters/%s/applications/%s/kustomization.yaml", region, app)
}

// AllPaths returns every in-Gitea path the controller writes for this
// (region × app) tuple, in deterministic order. Used by the cascade-
// delete code path to remove the entire manifest set on Application
// deletion.
func AllPaths(region, app string) []string {
	out := []string{
		KustomizationPath(region, app),
		HelmReleasePath(region, app),
	}
	sort.Strings(out)
	return out
}
