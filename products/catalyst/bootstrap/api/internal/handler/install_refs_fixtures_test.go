package handler

import (
	"os"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/regions"
)

// ADR-0015 D4 test fixtures — the live objects every create request must
// name. A create test seeds these (or deliberately omits one) instead of
// relying on the API to fabricate them.

// refTestRegionA / refTestRegionB are the fixture region registry.
const (
	refTestRegionA = "rgn-a"
	refTestRegionB = "rgn-b"
)

// refTestDefaultRegistry is the region registry every handler test sees
// unless it sets SOVEREIGN_REGIONS_JSON itself: the fixture regions plus the
// canonical-shaped ids pre-existing install tests already name.
const refTestDefaultRegistry = `[{"cloudRegion":"rgn-a"},{"cloudRegion":"rgn-b"},` +
	`{"cloudRegion":"hz-fsn-rtz-prod"},{"cloudRegion":"hz-hel-rtz-prod"}]`

func init() {
	regions.SetEnvSource(func(k string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		if k == "SOVEREIGN_REGIONS_JSON" {
			return refTestDefaultRegistry
		}
		return ""
	})
}

// setRefTestRegistry points the region registry at rgn-a (primary) + rgn-b.
func setRefTestRegistry(t *testing.T) {
	t.Helper()
	t.Setenv("SOVEREIGN_REGIONS_JSON", `[{"cloudRegion":"`+refTestRegionA+`"},{"cloudRegion":"`+refTestRegionB+`"}]`)
}

func refTestOrganization(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "orgs.openova.io/v1",
		"kind":       "Organization",
		"metadata":   map[string]interface{}{"name": name},
		"spec": map[string]interface{}{
			"slug":                   name,
			"displayName":            "Org " + name,
			"defaultEnvironmentType": "prod",
		},
		"status": map[string]interface{}{
			"conditions": []interface{}{map[string]interface{}{"type": "Ready", "status": "True"}},
		},
	}}
}

func refTestEnvironment(name, org, envType string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "catalyst.openova.io/v1",
		"kind":       "Environment",
		"metadata":   map[string]interface{}{"name": name},
		"spec":       map[string]interface{}{"organizationRef": org, "envType": envType},
	}}
}

func refTestNamespace(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata":   map[string]interface{}{"name": name},
	}}
}

// refTestBlueprint seeds a Blueprint CR. No modes → every canonical mode.
func refTestBlueprint(name string, modes ...string) *unstructured.Unstructured {
	if len(modes) == 0 {
		modes = []string{"singleton", "active-active", "active-hot-standby", "active-passive"}
	}
	ms := make([]interface{}, 0, len(modes))
	for _, m := range modes {
		ms = append(ms, m)
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "catalyst.openova.io/v1",
		"kind":       "Blueprint",
		"metadata":   map[string]interface{}{"name": name},
		"spec": map[string]interface{}{
			"version":         "1.0.0",
			"placementSchema": map[string]interface{}{"modes": ms},
		},
	}}
}

// refTestReadyOrg seeds a Ready Organization: the CR, its `<org>-prod`
// Environment and its org-controller-owned namespace, plus the named
// Blueprints. The Environment NAME is fixture data, not a derivation the
// API performs.
func refTestReadyOrg(org string, blueprints ...string) []runtime.Object {
	out := []runtime.Object{
		refTestOrganization(org),
		refTestEnvironment(org+"-prod", org, "prod"),
		refTestNamespace(org),
	}
	for _, bp := range blueprints {
		out = append(out, refTestBlueprint(bp))
	}
	return out
}
