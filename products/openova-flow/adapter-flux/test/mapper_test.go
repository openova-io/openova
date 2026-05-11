// mapper_test.go — covers every HelmRelease state → FlowNode.status
// transition that the adapter has to ship. Fixtures are inline YAML
// (no testdata files) so the test is self-contained.
package test

import (
	"strings"
	"testing"

	"github.com/openova-io/openova/products/openova-flow/adapter-flux/internal/informer"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

func parseHR(t *testing.T, raw string) *unstructured.Unstructured {
	t.Helper()
	js, err := yaml.YAMLToJSON([]byte(strings.TrimSpace(raw)))
	if err != nil {
		t.Fatalf("yaml->json: %v", err)
	}
	u := &unstructured.Unstructured{}
	if err := u.UnmarshalJSON(js); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return u
}

func TestMapper_Ready_True(t *testing.T) {
	hr := parseHR(t, `
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-cert-manager
  namespace: flux-system
spec:
  dependsOn:
    - name: bp-cilium
status:
  conditions:
    - type: Ready
      status: "True"
      reason: ReconciliationSucceeded
`)
	res, ok := informer.BuildFromHR(hr, "fsn1")
	if !ok {
		t.Fatal("BuildFromHR returned not-ok")
	}
	if res.Node.Status != "succeeded" {
		t.Fatalf("status: %s", res.Node.Status)
	}
	if res.Node.ID != "fsn1/bp-cert-manager" {
		t.Fatalf("id: %s", res.Node.ID)
	}
	if res.Node.Family == nil || *res.Node.Family != "cert-manager" {
		t.Fatalf("family: %+v", res.Node.Family)
	}
	if res.Node.Region == nil || *res.Node.Region != "fsn1" {
		t.Fatalf("region: %+v", res.Node.Region)
	}
	// Two "contains" rels (region + phase) + one dependsOn rel.
	if len(res.Relationships) != 3 {
		t.Fatalf("rels=%d want 3: %+v", len(res.Relationships), res.Relationships)
	}
	var hasRegionContains, hasPhaseContains, hasDep bool
	for _, r := range res.Relationships {
		if r.Type == "contains" && r.FromID == "fsn1" && r.ToID == "fsn1/bp-cert-manager" {
			hasRegionContains = true
		}
		if r.Type == "contains" && r.FromID == "fsn1/phase-1" && r.ToID == "fsn1/bp-cert-manager" {
			hasPhaseContains = true
		}
		if r.Type == "finish-to-start" && r.FromID == "fsn1/bp-cilium" && r.ToID == "fsn1/bp-cert-manager" {
			hasDep = true
		}
	}
	if !hasRegionContains || !hasPhaseContains || !hasDep {
		t.Fatalf("missing rels: regionContains=%v phaseContains=%v dep=%v rels=%+v",
			hasRegionContains, hasPhaseContains, hasDep, res.Relationships)
	}
}

func TestMapper_Ready_False_Progressing(t *testing.T) {
	hr := parseHR(t, `
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-trivy
status:
  conditions:
    - type: Ready
      status: "False"
      reason: Progressing
`)
	res, _ := informer.BuildFromHR(hr, "hel1")
	if res.Node.Status != "running" {
		t.Fatalf("status: %s", res.Node.Status)
	}
}

func TestMapper_Ready_False_InstallFailed(t *testing.T) {
	hr := parseHR(t, `
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-openbao
status:
  conditions:
    - type: Ready
      status: "False"
      reason: InstallFailed
`)
	res, _ := informer.BuildFromHR(hr, "fsn1")
	if res.Node.Status != "failed" {
		t.Fatalf("status: %s", res.Node.Status)
	}
}

func TestMapper_Ready_False_UpgradeFailed(t *testing.T) {
	hr := parseHR(t, `
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-keycloak
status:
  conditions:
    - type: Ready
      status: "False"
      reason: UpgradeFailed
`)
	res, _ := informer.BuildFromHR(hr, "fsn1")
	if res.Node.Status != "failed" {
		t.Fatalf("status: %s", res.Node.Status)
	}
}

func TestMapper_Ready_False_RetriesExhausted(t *testing.T) {
	hr := parseHR(t, `
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-grafana
status:
  conditions:
    - type: Ready
      status: "False"
      reason: RetriesExhausted
`)
	res, _ := informer.BuildFromHR(hr, "fsn1")
	if res.Node.Status != "failed" {
		t.Fatalf("status: %s", res.Node.Status)
	}
}

func TestMapper_Ready_Unknown(t *testing.T) {
	hr := parseHR(t, `
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-cnpg
status:
  conditions:
    - type: Ready
      status: Unknown
      reason: Reconciling
`)
	res, _ := informer.BuildFromHR(hr, "fsn1")
	if res.Node.Status != "running" {
		t.Fatalf("status: %s", res.Node.Status)
	}
}

func TestMapper_NoReadyConditionYet(t *testing.T) {
	hr := parseHR(t, `
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-newapi
`)
	res, _ := informer.BuildFromHR(hr, "fsn1")
	if res.Node.Status != "pending" {
		t.Fatalf("status: %s", res.Node.Status)
	}
}

func TestMapper_FamilyLabelOverride(t *testing.T) {
	hr := parseHR(t, `
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-stalwart-sovereign
  labels:
    catalyst.openova.io/family: mail
status:
  conditions:
    - type: Ready
      status: "True"
`)
	res, _ := informer.BuildFromHR(hr, "fsn1")
	if res.Node.Family == nil || *res.Node.Family != "mail" {
		t.Fatalf("family: %+v", res.Node.Family)
	}
}

func TestMapper_RegionFallback(t *testing.T) {
	hr := parseHR(t, `
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-cilium
`)
	res, _ := informer.BuildFromHR(hr, "")
	if res.Node.ID != "default/bp-cilium" {
		t.Fatalf("id: %s", res.Node.ID)
	}
	if res.Node.Region == nil || *res.Node.Region != "default" {
		t.Fatalf("region: %+v", res.Node.Region)
	}
}

func TestMapper_MultiDependsOn(t *testing.T) {
	hr := parseHR(t, `
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-guacamole
spec:
  dependsOn:
    - name: bp-cilium
    - name: bp-cert-manager
    - name: bp-keycloak
    - name: bp-sealed-secrets
    - name: bp-seaweedfs
    - name: bp-k8s-ws-proxy
status:
  conditions:
    - type: Ready
      status: "True"
`)
	res, _ := informer.BuildFromHR(hr, "fsn1")
	// 2 "contains" (region + phase) + 6 finish-to-start.
	if len(res.Relationships) != 8 {
		t.Fatalf("rels=%d want 8", len(res.Relationships))
	}
}

func TestMapper_RegionNodeBootstrap(t *testing.T) {
	n := informer.BuildRegionNode("hel1")
	if n.ID != "hel1" {
		t.Fatalf("id: %s", n.ID)
	}
	if n.Region == nil || *n.Region != "hel1" {
		t.Fatalf("region: %+v", n.Region)
	}
	// Region root starts pending — rollup is driven by the
	// StatusTracker as child HRs arrive. "pending" is the no-children
	// default per RollupStatus contract.
	if n.Status != "pending" {
		t.Fatalf("status: %s", n.Status)
	}
	if n.Family == nil || *n.Family != "region" {
		t.Fatalf("family: %+v", n.Family)
	}
	if n.Meta["layout"] != "lane-vertical" {
		t.Fatalf("layout: %+v", n.Meta["layout"])
	}
	if n.Meta["isGroup"] != true {
		t.Fatalf("isGroup: %+v", n.Meta["isGroup"])
	}
}
