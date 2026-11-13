package compliance

import (
	"io/fs"
	"os"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

func loadObject(t *testing.T, file string) *unstructured.Unstructured {
	t.Helper()
	b, err := fs.ReadFile(os.DirFS("testdata"), file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	obj := &unstructured.Unstructured{}
	if err := yaml.Unmarshal(b, &obj.Object); err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	return obj
}

func TestLoadModelDefault(t *testing.T) {
	m, err := LoadModel(loadObject(t, "model-default.yaml"))
	if err != nil {
		t.Fatalf("LoadModel: %v", err)
	}
	if m.Version != "0.0.0" || len(m.Levels) != 5 || len(m.Checks) != 0 || len(m.Exemptions) != 0 {
		t.Fatalf("unexpected model: %+v", m)
	}
	if len(m.Groups) != 1 || m.Groups[0].ID != "all" || len(m.Axes) != 1 || m.Axes[0].ID != "all" || m.Axes[0].Type != AxisTypeChecks {
		t.Fatalf("want one group and one axis named all, got %+v %+v", m.Groups, m.Axes)
	}
	if m.MandatoryCap.Value != 100 || len(m.MandatoryCap.EnvironmentTypes) != 0 {
		t.Fatalf("want cap 100 for no environment type, got %+v", m.MandatoryCap)
	}
	if m.SeverityWeights != (SeverityWeights{Critical: 1, High: 1, Medium: 1, Low: 1}) {
		t.Fatalf("want every severity weight 1, got %+v", m.SeverityWeights)
	}
}

func TestLoadModelSample(t *testing.T) {
	m, err := LoadModel(loadObject(t, "model-sample.yaml"))
	if err != nil {
		t.Fatalf("LoadModel: %v", err)
	}
	if m.Version != "1.0.0" || m.MandatoryCap.Value != 74 || len(m.Groups) != 2 || len(m.Axes) != 3 || len(m.Checks) != 4 {
		t.Fatalf("unexpected model: %+v", m)
	}
	if m.Levels[0] != (Level{Name: "L5", Min: 90}) || m.Levels[4] != (Level{Name: "L1", Min: 0}) {
		t.Fatalf("levels: %+v", m.Levels)
	}
	if a := m.Axes[2]; a.Type != AxisTypeMeasure || a.Measure != "runtime-clean" {
		t.Fatalf("measure axis: %+v", a)
	}
	want := map[string]struct {
		weight                int
		mandatory, switchable bool
		valuesKey             string
	}{
		"multi-replica-drainability": {2, false, true, "multiReplica"},
		"probes-present":             {4, false, true, "probesPresent"}, // weight from severityWeights.high
		"hpa-effective":              {0, false, true, ""},
		"forbid-nodeport-service":    {8, true, false, "forbidNodePort"}, // weight from severityWeights.critical
	}
	for _, c := range m.Checks {
		w, ok := want[c.ID]
		if !ok {
			t.Fatalf("unexpected check %q", c.ID)
		}
		if c.Weight != w.weight || c.Mandatory != w.mandatory || c.Switchable != w.switchable || c.ValuesKey != w.valuesKey {
			t.Errorf("check %s: got weight %d mandatory %v switchable %v valuesKey %q", c.ID, c.Weight, c.Mandatory, c.Switchable, c.ValuesKey)
		}
	}
	if len(m.Exemptions) != 1 || !m.exempts("kube-system", "probes-present") || m.exempts("default", "probes-present") {
		t.Fatalf("exemptions: %+v", m.Exemptions)
	}
}

func specOf(obj *unstructured.Unstructured) map[string]interface{} {
	return obj.Object["spec"].(map[string]interface{})
}

func itemOf(spec map[string]interface{}, list string, i int) map[string]interface{} {
	return spec[list].([]interface{})[i].(map[string]interface{})
}

// TestLoadModelRules breaks one loader rule per case and expects LoadModel to
// name it.
func TestLoadModelRules(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(obj *unstructured.Unstructured)
		want   string
	}{
		{"wrong group", func(o *unstructured.Unstructured) { o.SetAPIVersion("catalyst.openova.io/v1alpha1") }, "want compliance.openova.io/v1alpha1 ComplianceModel"},
		{"wrong kind", func(o *unstructured.Unstructured) { o.SetKind("ConfigMap") }, "want compliance.openova.io/v1alpha1 ComplianceModel"},
		{"no spec", func(o *unstructured.Unstructured) { delete(o.Object, "spec") }, "spec: required"},
		{"version missing", func(o *unstructured.Unstructured) { delete(specOf(o), "version") }, "version: required"},
		{"version not semver", func(o *unstructured.Unstructured) { specOf(o)["version"] = "1.0" }, "is not a semantic version"},
		{"one level", func(o *unstructured.Unstructured) {
			specOf(o)["levels"] = specOf(o)["levels"].([]interface{})[4:]
		}, "levels: 1 items, want 2..10"},
		{"eleven levels", func(o *unstructured.Unstructured) {
			ls := specOf(o)["levels"].([]interface{})
			specOf(o)["levels"] = append(append(append([]interface{}{}, ls...), ls...), ls[0])
		}, "levels: 11 items, want 2..10"},
		{"levels unsorted", func(o *unstructured.Unstructured) { itemOf(specOf(o), "levels", 1)["min"] = 95 }, "levels are not sorted by min"},
		{"level name twice", func(o *unstructured.Unstructured) { itemOf(specOf(o), "levels", 1)["name"] = "L5" }, `levels[1].name: "L5" is not unique`},
		{"last level not 0", func(o *unstructured.Unstructured) { itemOf(specOf(o), "levels", 4)["min"] = 10 }, "the last min is 10, want 0"},
		{"level min above 100", func(o *unstructured.Unstructured) { itemOf(specOf(o), "levels", 0)["min"] = 101 }, "levels[0].min: 101 is outside 0..100"},
		{"cap missing", func(o *unstructured.Unstructured) { delete(specOf(o), "mandatoryCap") }, "mandatoryCap: required"},
		{"cap above 100", func(o *unstructured.Unstructured) {
			specOf(o)["mandatoryCap"].(map[string]interface{})["value"] = 101
		}, "mandatoryCap.value: 101 is outside 0..100"},
		{"cap environment types missing", func(o *unstructured.Unstructured) {
			delete(specOf(o)["mandatoryCap"].(map[string]interface{}), "environmentTypes")
		}, "mandatoryCap.environmentTypes: required"},
		{"severity weights missing", func(o *unstructured.Unstructured) { delete(specOf(o), "severityWeights") }, "severityWeights: required"},
		{"severity weight high missing", func(o *unstructured.Unstructured) {
			delete(specOf(o)["severityWeights"].(map[string]interface{}), "high")
		}, "severityWeights.high: required"},
		{"no group", func(o *unstructured.Unstructured) { specOf(o)["groups"] = []interface{}{} }, "groups: 0 items, want 1..4"},
		{"five groups", func(o *unstructured.Unstructured) {
			gs := specOf(o)["groups"].([]interface{})
			specOf(o)["groups"] = append(append(append([]interface{}{}, gs...), gs...), gs[0])
		}, "groups: 5 items, want 1..4"},
		{"group id pattern", func(o *unstructured.Unstructured) { itemOf(specOf(o), "groups", 0)["id"] = "Run" }, `groups[0].id: "Run" does not match`},
		{"group id twice", func(o *unstructured.Unstructured) { itemOf(specOf(o), "groups", 1)["id"] = "run" }, `groups[1].id: "run" is not unique`},
		{"group weight 0", func(o *unstructured.Unstructured) { itemOf(specOf(o), "groups", 0)["weight"] = 0 }, "groups[0].weight: 0 is outside 1..100"},
		{"no axis", func(o *unstructured.Unstructured) { specOf(o)["axes"] = []interface{}{} }, "axes: 0 items, want 1..30"},
		{"axis id twice", func(o *unstructured.Unstructured) { itemOf(specOf(o), "axes", 1)["id"] = "reliability" }, `axes[1].id: "reliability" is not unique`},
		{"axis group unknown", func(o *unstructured.Unstructured) { itemOf(specOf(o), "axes", 0)["group"] = "people" }, `axes[0].group: "people" is not an id of groups`},
		{"axis type unknown", func(o *unstructured.Unstructured) { itemOf(specOf(o), "axes", 0)["type"] = "ratio" }, `axes[0].type: "ratio" is not checks or measure`},
		{"measure axis without measure", func(o *unstructured.Unstructured) { delete(itemOf(specOf(o), "axes", 2), "measure") }, "axes[2].measure: required"},
		{"measure unknown", func(o *unstructured.Unstructured) { itemOf(specOf(o), "axes", 2)["measure"] = "uptime" }, `axes[2].measure: "uptime" is not a known measure`},
		{"checks axis with measure", func(o *unstructured.Unstructured) { itemOf(specOf(o), "axes", 0)["measure"] = "runtime-clean" }, "axes[0].measure: must be absent when type is checks"},
		{"axis naText missing", func(o *unstructured.Unstructured) { delete(itemOf(specOf(o), "axes", 0), "naText") }, "axes[0].naText: required"},
		{"checks missing", func(o *unstructured.Unstructured) { delete(specOf(o), "checks") }, "checks: required"},
		{"check id twice", func(o *unstructured.Unstructured) {
			itemOf(specOf(o), "checks", 1)["id"] = "multi-replica-drainability"
		}, `checks[1].id: "multi-replica-drainability" is not unique`},
		{"check title too long", func(o *unstructured.Unstructured) {
			itemOf(specOf(o), "checks", 0)["title"] = strings.Repeat("x", 81)
		}, "checks[0].title: longer than 80 characters"},
		{"check source unknown", func(o *unstructured.Unstructured) { itemOf(specOf(o), "checks", 0)["source"] = "falco" }, `checks[0].source: "falco" is not a known source`},
		{"check axis unknown", func(o *unstructured.Unstructured) { itemOf(specOf(o), "checks", 0)["axis"] = "speed" }, `checks[0].axis: "speed" is not an id of an axis of type checks`},
		{"check on a measure axis", func(o *unstructured.Unstructured) { itemOf(specOf(o), "checks", 0)["axis"] = "runtime" }, `checks[0].axis: "runtime" is not an id of an axis of type checks`},
		{"check severity unknown", func(o *unstructured.Unstructured) { itemOf(specOf(o), "checks", 0)["severity"] = "urgent" }, `checks[0].severity: "urgent" is not critical, high, medium or low`},
		{"check weight above 100", func(o *unstructured.Unstructured) { itemOf(specOf(o), "checks", 0)["weight"] = 101 }, "checks[0].weight: 101 is outside 0..100"},
		{"check weight not an integer", func(o *unstructured.Unstructured) { itemOf(specOf(o), "checks", 0)["weight"] = 1.5 }, "spec: json:"},
		{"check owner side unknown", func(o *unstructured.Unstructured) { itemOf(specOf(o), "checks", 0)["ownerSide"] = "vendor" }, `checks[0].ownerSide: "vendor" is not organization or sovereign`},
		{"kyverno check without valuesKey", func(o *unstructured.Unstructured) { delete(itemOf(specOf(o), "checks", 0), "valuesKey") }, "checks[0].valuesKey: required"},
		{"evaluator check with valuesKey", func(o *unstructured.Unstructured) { itemOf(specOf(o), "checks", 2)["valuesKey"] = "hpa" }, "checks[2].valuesKey: must be absent when source is evaluator"},
		{"check description missing", func(o *unstructured.Unstructured) { delete(itemOf(specOf(o), "checks", 0), "description") }, "checks[0].description: required"},
		{"check remediation too short", func(o *unstructured.Unstructured) { itemOf(specOf(o), "checks", 0)["remediation"] = "Fix it." }, "checks[0].remediation: shorter than 20 characters"},
		{"exemptions missing", func(o *unstructured.Unstructured) { delete(specOf(o), "exemptions") }, "exemptions: required"},
		{"exemption without checks", func(o *unstructured.Unstructured) { itemOf(specOf(o), "exemptions", 0)["checks"] = []interface{}{} }, "exemptions[0].checks: required, at least one item"},
		{"exemption star with another check", func(o *unstructured.Unstructured) {
			itemOf(specOf(o), "exemptions", 0)["checks"] = []interface{}{"*", "probes-present"}
		}, `exemptions[0].checks: "*" must be the only item`},
		{"exemption reason too short", func(o *unstructured.Unstructured) { itemOf(specOf(o), "exemptions", 0)["reason"] = "system" }, "exemptions[0].reason: shorter than 20 characters"},
		{"exemption owner missing", func(o *unstructured.Unstructured) { delete(itemOf(specOf(o), "exemptions", 0), "owner") }, "exemptions[0].owner: required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obj := loadObject(t, "model-sample.yaml")
			tc.mutate(obj)
			m, err := LoadModel(obj)
			if err == nil {
				t.Fatalf("LoadModel accepted the object: %+v", m)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
	if _, err := LoadModel(nil); err == nil {
		t.Fatal("LoadModel(nil) returned no error")
	}
}
