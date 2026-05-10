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
