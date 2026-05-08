package render

import (
	"strings"
	"testing"
)

func baseInputs() Inputs {
	return Inputs{
		AppName:          "marketing-site",
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

func TestRender_NamespaceIsOrg(t *testing.T) {
	res, err := Render(baseInputs())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(res.HelmReleaseYAML), "namespace: acme") {
		t.Error("HelmRelease namespace should be Org slug")
	}
	if !strings.Contains(string(res.HelmReleaseYAML), "targetNamespace: acme") {
		t.Error("HelmRelease targetNamespace should be Org slug")
	}
	if !strings.Contains(string(res.KustomizationYAML), "namespace: acme") {
		t.Error("Kustomization namespace should be Org slug")
	}
}
