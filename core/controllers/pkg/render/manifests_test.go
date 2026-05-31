package render

import (
	"strings"
	"testing"
)

func baseInputs() Inputs {
	return Inputs{
		AppName:          "marketing-site",
		AppNamespace:     "acme",
		Org:              "acme",
		EnvType:          "prod",
		Region:           "hetzner-fsn-rtz-prod",
		PlacementRole:    "primary",
		BlueprintName:    "bp-wordpress",
		BlueprintVersion: "1.2.3",
		SourceKind:       SourceKindHelmChart,
		Values: map[string]interface{}{
			"replicas": 3,
			"image":    "wordpress:6.5",
		},
		OwnerAppUID: "11111111-2222-3333-4444-555555555555",
		OwnerAppGen: 1,
	}
}

func TestRender_PrimaryProducesBothFiles(t *testing.T) {
	res, err := Render(baseInputs())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(res.HelmReleaseYAML), "kind: HelmRelease") {
		t.Error("HelmReleaseYAML missing kind: HelmRelease")
	}
	if !strings.Contains(string(res.HelmReleaseYAML), "name: marketing-site") {
		t.Error("HelmReleaseYAML missing application name")
	}
	if !strings.Contains(string(res.HelmReleaseYAML), "replicas: 3") {
		t.Error("HelmReleaseYAML missing user-provided replicas")
	}
	if !strings.Contains(string(res.KustomizationYAML), "kind: Kustomization") {
		t.Error("KustomizationYAML missing kind")
	}
	if !strings.Contains(string(res.KustomizationYAML), "- helmrelease.yaml") {
		t.Error("KustomizationYAML missing helmrelease resource")
	}
}

func TestRender_StandbyOverlaysReplicasZero(t *testing.T) {
	in := baseInputs()
	in.Standby = true
	in.PlacementRole = "standby"
	res, err := Render(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	yaml := string(res.HelmReleaseYAML)
	if !strings.Contains(yaml, "replicas: 0") {
		t.Errorf("standby HelmRelease should set replicas: 0, got:\n%s", yaml)
	}
	if !strings.Contains(yaml, "_openova_standby: true") {
		t.Errorf("standby HelmRelease should set _openova_standby marker")
	}
}

func TestRender_Idempotent(t *testing.T) {
	a, err1 := Render(baseInputs())
	b, err2 := Render(baseInputs())
	if err1 != nil || err2 != nil {
		t.Fatalf("unexpected errors: %v %v", err1, err2)
	}
	if string(a.HelmReleaseYAML) != string(b.HelmReleaseYAML) {
		t.Error("HelmReleaseYAML not byte-stable across calls")
	}
	if string(a.KustomizationYAML) != string(b.KustomizationYAML) {
		t.Error("KustomizationYAML not byte-stable across calls")
	}
}

func TestRender_MissingFieldRejected(t *testing.T) {
	in := baseInputs()
	in.AppName = ""
	if _, err := Render(in); err == nil {
		t.Fatal("expected error for missing AppName")
	}
}

func TestRender_DefaultsApplied(t *testing.T) {
	in := baseInputs()
	in.SourceKind = ""
	in.Chart = ""
	in.SourceRef = ""
	in.IntervalSeconds = 0
	res, err := Render(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	yaml := string(res.HelmReleaseYAML)
	if !strings.Contains(yaml, "kind: HelmRepository") {
		t.Error("default SourceKind should map to HelmRepository")
	}
	if !strings.Contains(yaml, "chart: bp-wordpress") {
		t.Error("default Chart should fall back to BlueprintName")
	}
	if !strings.Contains(yaml, "name: openova-catalog") {
		t.Error("default SourceRef should be openova-catalog")
	}
	if !strings.Contains(yaml, "interval: 600s") {
		t.Error("default IntervalSeconds should be 600")
	}
}

func TestPaths(t *testing.T) {
	if got := HelmReleasePath("hetzner-fsn-rtz-prod", "site"); got != "clusters/hetzner-fsn-rtz-prod/applications/site/helmrelease.yaml" {
		t.Errorf("HelmReleasePath = %q", got)
	}
	if got := KustomizationPath("hetzner-fsn-rtz-prod", "site"); got != "clusters/hetzner-fsn-rtz-prod/applications/site/kustomization.yaml" {
		t.Errorf("KustomizationPath = %q", got)
	}
	all := AllPaths("hetzner-fsn-rtz-prod", "site")
	if len(all) != 2 {
		t.Errorf("AllPaths len = %d", len(all))
	}
	// Sorted: helmrelease.yaml before kustomization.yaml
	if all[0] >= all[1] {
		t.Errorf("AllPaths not sorted: %v", all)
	}
}

func TestRender_GitRepositoryForKustomize(t *testing.T) {
	in := baseInputs()
	in.SourceKind = SourceKindKustomize
	res, err := Render(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(res.HelmReleaseYAML), "kind: GitRepository") {
		t.Error("Kustomize source should map to GitRepository")
	}
}

func TestRender_OCIRepositoryForOAM(t *testing.T) {
	in := baseInputs()
	in.SourceKind = SourceKindOAM
	res, err := Render(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(res.HelmReleaseYAML), "kind: OCIRepository") {
		t.Error("OAM source should map to OCIRepository")
	}
}

// TestRender_NamespaceIsAppNamespace asserts the rendered HelmRelease
// + Kustomization both target the Application's K8s namespace
// (Inputs.AppNamespace), NOT the Organization slug (Inputs.Org). This
// is the regression test for qa-loop iter-10 Fix #44 where on omantel
// the Application CR `qa-wp` lived in `qa-omantel` ns but the Org name
// was `omantel-platform`; the controller used Org for namespace and
// the workload Pod landed in the wrong namespace, breaking matrix
// rows TC-068 / TC-100 / TC-204 / TC-262 / TC-263.
func TestRender_NamespaceIsAppNamespace(t *testing.T) {
	in := baseInputs()
	in.AppNamespace = "qa-omantel"
	in.Org = "omantel-platform"
	res, err := Render(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	hr := string(res.HelmReleaseYAML)
	ks := string(res.KustomizationYAML)

	if !strings.Contains(hr, "namespace: qa-omantel") {
		t.Errorf("HelmRelease metadata.namespace should be the Application namespace (qa-omantel), got:\n%s", hr)
	}
	if !strings.Contains(hr, "targetNamespace: qa-omantel") {
		t.Errorf("HelmRelease spec.targetNamespace should be the Application namespace (qa-omantel), got:\n%s", hr)
	}
	if strings.Contains(hr, "targetNamespace: omantel-platform") {
		t.Errorf("HelmRelease spec.targetNamespace must NOT be the Org slug; got:\n%s", hr)
	}
	if !strings.Contains(ks, "namespace: qa-omantel") {
		t.Errorf("Kustomization namespace should be the Application namespace (qa-omantel), got:\n%s", ks)
	}
	// Org-as-label assertion: the Org is still stamped on labels for
	// traceability, just not used as the K8s namespace.
	if !strings.Contains(hr, "catalyst.openova.io/organization: omantel-platform") {
		t.Errorf("HelmRelease should still stamp the Org slug as a label, got:\n%s", hr)
	}
}

// TestRender_CreateNamespaceTrue asserts the rendered HelmRelease
// installs into a fresh namespace without an operator pre-creating
// it. Per docs/INVIOLABLE-PRINCIPLES.md #1 (target-state) the
// controller must always work — qa-loop iter-10 Fix #44 secondary fix
// after the omantel-platform namespace was missing entirely on a
// fresh provision.
func TestRender_CreateNamespaceTrue(t *testing.T) {
	res, err := Render(baseInputs())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	hr := string(res.HelmReleaseYAML)
	if !strings.Contains(hr, "createNamespace: true") {
		t.Errorf("HelmRelease spec.install.createNamespace must be true so missing namespaces are created on install:\n%s", hr)
	}
}

// TestRender_VClusterPlacementMGMT asserts the rendered HelmRelease
// installs INTO the MGMT vCluster (G92.1 #2660 + #2639 EPIC). The
// vCluster pivot uses Flux v2's spec.kubeConfig.secretRef contract;
// helm-controller authenticates against the vCluster's admin
// kubeconfig and installs the chart inside the vCluster, not on the
// host k3s.
//
// Founder mandate 2026-05-31: "vclusters are not there for fun
// purpose, they are there for containing the applications". Without
// this code path every bp-* landed on the host k3s and the three
// vClusters created at bootstrap (slots 54/58/59) stood empty.
func TestRender_VClusterPlacementMGMT(t *testing.T) {
	in := baseInputs()
	in.VCluster = "mgmt"
	res, err := Render(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	hr := string(res.HelmReleaseYAML)

	// HR lives in the vCluster's host namespace (mgmt) so the
	// kubeconfig Secret lookup is co-located.
	if !strings.Contains(hr, "namespace: mgmt") {
		t.Errorf("vCluster=mgmt: HelmRelease metadata.namespace must be 'mgmt' (vCluster host ns), got:\n%s", hr)
	}
	// kubeConfig pivot (Flux v2 contract).
	if !strings.Contains(hr, "kubeConfig:") {
		t.Errorf("vCluster=mgmt: HelmRelease must carry spec.kubeConfig, got:\n%s", hr)
	}
	if !strings.Contains(hr, "name: vc-mgmt") {
		t.Errorf("vCluster=mgmt: kubeConfig.secretRef.name must be 'vc-mgmt' (loft-sh/vcluster convention), got:\n%s", hr)
	}
	if !strings.Contains(hr, "key: config") {
		t.Errorf("vCluster=mgmt: kubeConfig.secretRef.key must be 'config' (Secret data key for kubeconfig), got:\n%s", hr)
	}
	// targetNamespace stays the Application's INSIDE-vCluster namespace.
	if !strings.Contains(hr, "targetNamespace: acme") {
		t.Errorf("vCluster=mgmt: spec.targetNamespace must remain the Application's inner namespace, got:\n%s", hr)
	}
	// Label for traceability.
	if !strings.Contains(hr, "catalyst.openova.io/vcluster: mgmt") {
		t.Errorf("vCluster=mgmt: HR must stamp catalyst.openova.io/vcluster label, got:\n%s", hr)
	}
}

// TestRender_VClusterPlacementOverrides asserts the Config knob lets
// the operator override the default loft-sh/vcluster convention
// (host-namespace = vCluster name, kubeconfig Secret = `vc-<name>`)
// when a Sovereign customised the bp-*-vcluster chart values.
func TestRender_VClusterPlacementOverrides(t *testing.T) {
	in := baseInputs()
	in.VCluster = "dmz"
	in.VClusterHostNamespace = "dmz-edge"
	in.VClusterKubeconfigSecret = "vc-edge-config"
	res, err := Render(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	hr := string(res.HelmReleaseYAML)
	if !strings.Contains(hr, "namespace: dmz-edge") {
		t.Errorf("VClusterHostNamespace override should be honored, got:\n%s", hr)
	}
	if !strings.Contains(hr, "name: vc-edge-config") {
		t.Errorf("VClusterKubeconfigSecret override should be honored, got:\n%s", hr)
	}
}

// TestRender_NoVClusterUnchanged asserts the host-placement path
// (VCluster empty) produces no kubeConfig pivot — preserving every
// pre-existing Application that lands on the host k3s.
func TestRender_NoVClusterUnchanged(t *testing.T) {
	res, err := Render(baseInputs())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	hr := string(res.HelmReleaseYAML)
	if strings.Contains(hr, "kubeConfig:") {
		t.Errorf("Host placement (VCluster='') must NOT emit kubeConfig, got:\n%s", hr)
	}
	if strings.Contains(hr, "catalyst.openova.io/vcluster:") {
		t.Errorf("Host placement must NOT stamp catalyst.openova.io/vcluster label, got:\n%s", hr)
	}
	// HR namespace stays the Application namespace.
	if !strings.Contains(hr, "namespace: acme") {
		t.Errorf("Host placement HR.namespace should be AppNamespace, got:\n%s", hr)
	}
}

// TestRender_AppNamespaceFallbackToOrg asserts the back-compat default:
// callers that haven't been updated to pass AppNamespace explicitly
// still produce valid output (matching the legacy bug-compatible shape
// before the fix). All in-tree callers should pass AppNamespace; this
// is a safety net for out-of-tree callers and existing tests.
func TestRender_AppNamespaceFallbackToOrg(t *testing.T) {
	in := baseInputs()
	in.AppNamespace = ""
	in.Org = "legacy-org"
	res, err := Render(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	hr := string(res.HelmReleaseYAML)
	if !strings.Contains(hr, "namespace: legacy-org") {
		t.Errorf("AppNamespace fallback should resolve to Org when empty, got:\n%s", hr)
	}
	if !strings.Contains(hr, "targetNamespace: legacy-org") {
		t.Errorf("targetNamespace fallback should resolve to Org when AppNamespace empty, got:\n%s", hr)
	}
}
