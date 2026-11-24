package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The tests need git and yq on the PATH, as the guard does. A missing tool fails them.

const (
	modelYAML = `version: "1.0.0"
levels:
  - name: L2
    min: 50
  - name: L1
    min: 0
mandatoryCap:
  value: 74
  environmentTypes: [prod]
severityWeights: {critical: 8, high: 4, medium: 2, low: 1}
groups:
  - id: run
    title: Build and run
    weight: 1
axes:
  - id: all
    title: All
    group: run
    type: checks
    naText: Nothing in view.
`
	checkTail = `    source: kyverno
    axis: all
    severity: high
    ownerSide: organization
    description: d
    remediation: Do the right thing on every object.
`
	valuesYAML = `compliancePolicies:
  bootstrapMode: true
  alpha:
    enabled: true
    excludeNamespaces: &d
      - kube-system
  beta:
    excludeNamespaces: *d
    extraExcludeNamespaces:
      - cnpg
  mutator:
    excludeNamespaces: *d
`
	reasonsYAML = `namespaces:
  kube-system:
    reason: Platform namespace installed before the engine.
    owner: sovereign-admin
  cnpg:
    reason: The operator creates objects with no Flux owner.
    owner: sovereign-admin
`
	moduleCRD = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: compliancemodels.compliance.openova.io
spec:
  group: compliance.openova.io
  names:
    kind: ComplianceModel
    listKind: ComplianceModelList
    plural: compliancemodels
  scope: Cluster
`
	otherCRD = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: blueprints.catalyst.openova.io
spec:
  group: catalyst.openova.io
  names:
    kind: Blueprint
`
)

func checkEntry(id, valuesKey string) string {
	return "  - id: " + id + "\n    title: " + id + "\n    valuesKey: " + valuesKey + "\n" + checkTail
}

func checksYAML(entries ...string) string { return "checks:\n" + strings.Join(entries, "") }

func policy(name, values, rules string) string {
	return `{{/* a comment that says kind: ClusterPolicy and validate: is no policy */}}
{{- if .Values.` + values + `.enabled }}
apiVersion: kyverno.io/v1
kind: ClusterPolicy
metadata:
  name: ` + name + `
  annotations:
    policies.kyverno.io/severity: high
spec:
  rules:
` + rules + `{{- end }}
`
}

func validateRule(values string, exclude bool) string {
	r := "    - name: r\n"
	if exclude {
		r += "      exclude:\n        any:\n          - resources:\n              namespaces:\n" +
			"                {{- range .Values." + values + ".excludeNamespaces }}\n                - {{ . }}\n                {{- end }}\n"
	}
	return r + "      validate:\n        message: m\n"
}

const mutateRule = "    - name: stamp\n      mutate:\n        patchStrategicMerge: {}\n"

func baselineJSON(metric string, names ...string) string {
	var entries []string
	for _, n := range names {
		entries = append(entries, `"`+n+`": 1`)
	}
	return `{"schema": "openova.io/guard-baseline/v1", "allow": [], "counts": {"` + metric + `": {` + strings.Join(entries, ", ") + `}}}`
}

// realSchemas reads the schemas of the repository this test runs in.
func realSchemas(t *testing.T) map[string]string {
	t.Helper()
	top, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	out := map[string]string{}
	for _, name := range []string{"model", "checks", "exemptions"} {
		rel := SchemaDir + "/" + name + ".schema.json"
		b, err := os.ReadFile(filepath.Join(strings.TrimSpace(string(top)), rel)) // #nosec G304 -- a schema file of this repository
		if err != nil {
			t.Fatalf("schema of the repository: %v", err)
		}
		out[rel] = string(b)
	}
	return out
}

// fixture is a tree on which every rule holds: three checks, two policies in the chart,
// one in the claims chart, one mutate-only policy, two exempt namespaces, one suite.
func fixture(t *testing.T) map[string]string {
	files := map[string]string{
		ModelDir + "/model.yaml":                  modelYAML,
		ModelDir + "/checks.yaml":                 checksYAML(checkEntry("alpha-check", "alpha"), checkEntry("beta-check", "beta"), checkEntry("outside-check", "outsideKey")),
		ModelDir + "/exemptions.yaml":             reasonsYAML,
		Values:                                    valuesYAML,
		Templates + "/a.yaml":                     policy("alpha-check", "compliancePolicies.alpha", validateRule("compliancePolicies.alpha", true)),
		Templates + "/sub/b.yaml":                 policy("beta-check", "compliancePolicies.beta", validateRule("compliancePolicies.beta", true)),
		Templates + "/m.yaml":                     policy("mutator", "compliancePolicies.mutator", mutateRule),
		ClaimsPolicies + "/u.yaml":                policy("outside-check", "outsideKey", validateRule("outsideKey", false)),
		Suites + "/alpha-check/kyverno-test.yaml": "name: alpha\n",
		SuitesBaseline:                            baselineJSON(SuitesMetric, "beta-check", "outside-check"),
		ExemptBaseline:                            baselineJSON(ExemptMetric, "cnpg", "kube-system"),
		ModuleCRDs + "/compliancemodel.yaml":      moduleCRD,
		PlatformCRDs + "/blueprint.yaml":          otherCRD,
	}
	for k, v := range realSchemas(t) {
		files[k] = v
	}
	return files
}

// runOn writes the fixture with the edits (an empty value removes the file) into a new
// Git work tree and runs the guard on it.
func runOn(t *testing.T, edits map[string]string, args ...string) (int, string) {
	t.Helper()
	dir := t.TempDir()
	files := fixture(t)
	for k, v := range edits {
		files[k] = v
	}
	for rel, text := range files {
		if text == "" {
			continue
		}
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	initCmd := exec.Command("git", "-C", dir, "init", "-q") // #nosec G204 -- the test's own temporary folder
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	var stdout, stderr bytes.Buffer
	code := run(append([]string{"--root", dir}, args...), &stdout, &stderr)
	return code, stdout.String() + stderr.String()
}

func TestTheFixturePasses(t *testing.T) {
	code, out := runOn(t, nil)
	want := "check-compliance-model: ok; 3 checks (3 of source kyverno), 3 validating policy templates, 2 exempt namespaces, 1 module definitions"
	if code != 0 || strings.TrimSpace(out) != want {
		t.Fatalf("exit %d, output:\n%s\nwant exit 0 and:\n%s", code, out, want)
	}
}

func TestEveryRuleCanFail(t *testing.T) {
	ab := checkEntry("alpha-check", "alpha") + checkEntry("beta-check", "beta")
	cases := []struct {
		name  string
		edits map[string]string
		want  string
	}{
		{"M1 a title over 80 characters breaks the schema",
			map[string]string{ModelDir + "/checks.yaml": checksYAML(checkEntry(strings.Repeat("x", 81), "alpha"), checkEntry("beta-check", "beta"), checkEntry("outside-check", "outsideKey"))},
			"M1 " + ModelDir + "/checks.yaml breaks its schema at $.checks[0].title: is 81 characters, want at most 80"},
		{"M1 a key the schema does not name breaks it",
			map[string]string{ModelDir + "/model.yaml": modelYAML + "weights: {}\n"},
			"M1 " + ModelDir + "/model.yaml breaks its schema at $: weights is not an allowed key"},
		{"M1 a kyverno check with no valuesKey breaks the schema",
			map[string]string{ModelDir + "/checks.yaml": checksYAML(ab, "  - id: outside-check\n    title: t\n"+checkTail)},
			"M1 " + ModelDir + "/checks.yaml breaks its schema at $.checks[2]: valuesKey is required"},
		{"M1 a reason under 20 characters breaks the schema",
			map[string]string{ModelDir + "/exemptions.yaml": strings.Replace(reasonsYAML, "Platform namespace installed before the engine.", "Too short.", 1)},
			"M1 " + ModelDir + "/exemptions.yaml breaks its schema at $.namespaces.kube-system.reason: is 10 characters, want at least 20"},
		{"M1 a data file with no schema",
			map[string]string{ModelDir + "/extra.yaml": "a: 1\n"},
			"M1 " + ModelDir + "/extra.yaml has no schema " + SchemaDir + "/extra.schema.json"},
		{"M1 a schema with no data file",
			map[string]string{SchemaDir + "/frameworks.schema.json": `{"type": "object"}`},
			"M1 " + SchemaDir + "/frameworks.schema.json has no data file " + ModelDir + "/frameworks.yaml"},
		{"M1 a schema keyword the validator does not know is an error, not a pass",
			map[string]string{SchemaDir + "/exemptions.schema.json": `{"type": "object", "minProperties": 1}`},
			"FAIL, " + SchemaDir + "/exemptions.schema.json cannot be applied: schema keyword minProperties is not supported by this validator"},
		{"M2 a check on an axis that does not exist",
			map[string]string{ModelDir + "/checks.yaml": checksYAML(ab, strings.Replace(checkEntry("outside-check", "outsideKey"), "axis: all", "axis: nowhere", 1))},
			`M2 the check outside-check names the axis "nowhere", which is not an axis of type checks`},
		{"M2 levels out of order",
			map[string]string{ModelDir + "/model.yaml": strings.Replace(modelYAML, "min: 50", "min: 0", 1)},
			"M2 levels[1] (L1): levels are not sorted by min, highest first"},
		{"M2 the last level above 0",
			map[string]string{ModelDir + "/model.yaml": strings.Replace(modelYAML, "    min: 0\n", "    min: 10\n", 1)},
			"M2 the last level has min 10, want 0"},
		{"M2 an axis of a group that does not exist",
			map[string]string{ModelDir + "/model.yaml": strings.Replace(modelYAML, "    group: run\n", "    group: ops\n", 1)},
			`M2 the axis all names the group "ops", which is not a group`},
		{"M2 two checks with one id",
			map[string]string{ModelDir + "/checks.yaml": checksYAML(ab, checkEntry("outside-check", "outsideKey"), checkEntry("alpha-check", "alpha"))},
			`M2 the check id "alpha-check" is not unique`},
		{"M3 a validating ClusterPolicy with no check",
			map[string]string{Templates + "/c.yaml": policy("gamma-check", "compliancePolicies.alpha", validateRule("compliancePolicies.alpha", false))},
			"M3 the validating policy gamma-check (" + Templates + "/c.yaml) has no check"},
		{"M3 a ValidatingPolicy with no check, whatever its rules",
			map[string]string{Templates + "/v.yaml": "apiVersion: policies.kyverno.io/v1\nkind: ValidatingPolicy\nmetadata:\n  name: delta-check\nspec:\n  validations: []\n"},
			"M3 the validating policy delta-check (" + Templates + "/v.yaml) has no check"},
		{"M3 a check whose template is gone",
			map[string]string{Templates + "/sub/b.yaml": ""},
			"M3 the kyverno check beta-check names no validating policy template"},
		{"M3 a policy name made by template logic",
			map[string]string{Templates + "/c.yaml": policy("{{ .Values.name }}", "compliancePolicies.alpha", validateRule("compliancePolicies.alpha", false))},
			"M3 the validating policy <not literal: {{ .Values.name }}> (" + Templates + "/c.yaml) has no check"},
		{"M3 two templates of one check",
			map[string]string{Templates + "/a2.yaml": policy("alpha-check", "compliancePolicies.alpha", validateRule("compliancePolicies.alpha", true))},
			"M3 the kyverno check alpha-check names 2 policy templates: " + Templates + "/a.yaml, " + Templates + "/a2.yaml"},
		{"M3 a mutate rule beside a validate rule still makes a check",
			map[string]string{Templates + "/m.yaml": policy("mutator", "compliancePolicies.mutator", mutateRule+validateRule("compliancePolicies.mutator", false))},
			"M3 the validating policy mutator (" + Templates + "/m.yaml) has no check"},
		{"M4 a valuesKey that is no block of the values",
			map[string]string{ModelDir + "/checks.yaml": checksYAML(checkEntry("alpha-check", "gamma"), checkEntry("beta-check", "beta"), checkEntry("outside-check", "outsideKey"))},
			"M4 the check alpha-check names valuesKey gamma, which is no block under compliancePolicies of " + Values},
		{"M4 a template that reads another block than its valuesKey",
			map[string]string{Templates + "/sub/b.yaml": policy("beta-check", "compliancePolicies.alpha", validateRule("compliancePolicies.alpha", true))},
			"M4 the template " + Templates + "/sub/b.yaml of the check beta-check does not read .Values.compliancePolicies.beta"},
		{"M4 a template outside the chart that excludes namespaces",
			map[string]string{ClaimsPolicies + "/u.yaml": policy("outside-check", "outsideKey", validateRule("outsideKey", true))},
			"M4 the template " + ClaimsPolicies + "/u.yaml of the check outside-check is outside " + Chart + " and has an exclude block"},
		{"M4 a template outside the chart that reads another key",
			map[string]string{ClaimsPolicies + "/u.yaml": policy("outside-check", "otherKey", validateRule("otherKey", false))},
			"M4 the template " + ClaimsPolicies + "/u.yaml of the check outside-check does not read .Values.outsideKey"},
		{"M5 a check with no suite that the baseline does not list",
			map[string]string{SuitesBaseline: baselineJSON(SuitesMetric, "outside-check")},
			"M5 the kyverno check beta-check has no suite " + Suites + "/beta-check/kyverno-test.yaml and is not listed in " + SuitesBaseline},
		{"M5 a listed check that has a suite now",
			map[string]string{Suites + "/beta-check/kyverno-test.yaml": "name: beta\n"},
			"M5 the kyverno check beta-check has a suite and is still listed in " + SuitesBaseline + ": remove the entry"},
		{"M5 a listed name that is no check",
			map[string]string{SuitesBaseline: baselineJSON(SuitesMetric, "beta-check", "outside-check", "gone-check")},
			"M5 " + SuitesBaseline + " lists gone-check, which is no kyverno check: remove the entry"},
		{"M6 a new exempt namespace",
			map[string]string{Values: strings.Replace(valuesYAML, "      - cnpg\n", "      - cnpg\n      - newns\n", 1),
				ModelDir + "/exemptions.yaml": reasonsYAML + "  newns:\n    reason: A reason of twenty characters.\n    owner: sovereign-admin\n"},
			"M6 the namespace newns is exempt in " + Values + " and not in " + ExemptBaseline + ": the set of exempt namespaces may only shrink"},
		{"M6 an exempt namespace with no reason",
			map[string]string{Values: strings.Replace(valuesYAML, "      - cnpg\n", "      - cnpg\n      - newns\n", 1),
				ExemptBaseline: baselineJSON(ExemptMetric, "cnpg", "kube-system", "newns")},
			"M6 the exempt namespace newns has no reason in " + ModelDir + "/exemptions.yaml"},
		{"M6 a baseline entry no list holds",
			map[string]string{ExemptBaseline: baselineJSON(ExemptMetric, "cnpg", "kube-system", "old")},
			"M6 " + ExemptBaseline + " lists old, which no exclude list of " + Values + " holds any more: remove the entry"},
		{"M6 a reason no list uses",
			map[string]string{ModelDir + "/exemptions.yaml": reasonsYAML + "  old:\n    reason: A reason of twenty characters.\n    owner: sovereign-admin\n"},
			"M6 " + ModelDir + "/exemptions.yaml gives a reason for old, which no exclude list of " + Values + " holds: remove it"},
		{"M6 a namespace only a policy without a check excludes is still exempt",
			map[string]string{Values: valuesYAML + "    extraExcludeNamespaces: [mutns]\n"},
			"M6 the namespace mutns is exempt in " + Values + " and not in " + ExemptBaseline},
		{"M7 a module kind in the platform chart's definitions",
			map[string]string{PlatformCRDs + "/compliancemodel.yaml": moduleCRD},
			"M7 " + PlatformCRDs + "/compliancemodel.yaml defines a module kind of the policy Blueprint (ComplianceModel): it belongs in " + ModuleCRDs},
		{"M7 a module kind in the platform group",
			map[string]string{ModuleCRDs + "/compliancedecision.yaml": strings.ReplaceAll(strings.ReplaceAll(moduleCRD, "ComplianceModel", "ComplianceDecision"), "group: compliance.openova.io", "group: catalyst.openova.io")},
			`M7 ` + ModuleCRDs + `/compliancedecision.yaml defines ComplianceDecision in group "catalyst.openova.io", want compliance.openova.io`},
		{"M7 a module kind defined by another chart",
			map[string]string{"platform/other/chart/crds/model.yaml": moduleCRD},
			"M7 platform/other/chart/crds/model.yaml defines ComplianceModel outside " + ModuleCRDs},
		{"a baseline entry that is not 1 cannot be read",
			map[string]string{ExemptBaseline: strings.Replace(baselineJSON(ExemptMetric, "cnpg", "kube-system"), `"cnpg": 1`, `"cnpg": 2`, 1)},
			"FAIL, " + ExemptBaseline + ": the entry cnpg is 2, every entry is 1"},
		{"a baseline of another metric cannot be read",
			map[string]string{SuitesBaseline: baselineJSON(ExemptMetric, "beta-check", "outside-check")},
			"FAIL, " + SuitesBaseline + " must be"},
		{"a missing data file cannot be judged",
			map[string]string{ModelDir + "/exemptions.yaml": "", SchemaDir + "/exemptions.schema.json": ""},
			"FAIL, " + ModelDir + "/exemptions.yaml is missing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out := runOn(t, tc.edits)
			if code != 1 || !strings.Contains(out, tc.want) {
				t.Fatalf("exit %d, output:\n%s\nwant exit 1 and the text:\n%s", code, out, tc.want)
			}
		})
	}
}

func TestWrongUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"extra"}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), usageText) {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
}
