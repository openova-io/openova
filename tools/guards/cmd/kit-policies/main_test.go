package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The cases that render and judge need helm, yq, kubectl, bash, Go and the
// Kyverno CLI v1.19.2 on PATH. Without them they skip, unless
// KIT_POLICIES_TOOLS=required (the tree-guards step sets it): then a missing
// tool fails them.
func needTools(t *testing.T) {
	t.Helper()
	missing := ""
	for _, tool := range []string{"helm", "yq", "kubectl", "bash", "go", "kyverno"} {
		if _, err := exec.LookPath(tool); err != nil {
			missing = tool
		}
	}
	if missing == "" {
		out, _ := exec.Command("kyverno", "version").Output()
		if m := cliVersion.FindSubmatch(out); m == nil || string(m[1]) != "1.19.2" {
			missing = "kyverno v1.19.2"
		}
	}
	if missing == "" {
		return
	}
	if os.Getenv("KIT_POLICIES_TOOLS") == "required" {
		t.Fatalf("%s is not on PATH, and KIT_POLICIES_TOOLS=required", missing)
	}
	t.Skipf("%s is not on PATH", missing)
}

// The fixture tree: an engine with an admission and a reports controller, a
// policy chart whose probe policy denies only after the window, and an
// application chart with an upstream sub-chart, a test hook and a QA fixture.
var fixture = map[string]string{
	"tests/fixtures/variables/today.env": "SOVEREIGN_CONSUME=origin\nQA_FIXTURES_ENABLED=true\n",
	"platform/kyverno/chart/Chart.yaml":  "apiVersion: v2\nname: bp-kyverno\nversion: 0.1.0\nappVersion: \"v1.19.2\"\n",
	"platform/kyverno/chart/templates/engine.yaml": `{{- range $c := list "admission-controller" "reports-controller" }}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kyverno-{{ $c }}
  labels: {app.kubernetes.io/part-of: kyverno, app.kubernetes.io/component: {{ $c }}}
spec:
  selector: {matchLabels: {c: {{ $c }}}}
  template:
    metadata: {labels: {c: {{ $c }}}}
    spec:
      serviceAccountName: kyverno-{{ $c }}
      containers:
        - name: kyverno
          image: registry.example/proxy-ghcr/kyverno/kyverno:v1.19.2
          args: ["--enablePolicyException=true", "--exceptionNamespace=policy-exceptions"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata: {name: "kyverno:{{ $c }}"}
aggregationRule:
  clusterRoleSelectors:
    - matchLabels: {rbac.kyverno.io/aggregate-to-{{ $c }}: "true"}
rules: []
{{- range $role := list (printf "kyverno:%s" $c) "view" }}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata: {name: "{{ $role }}-{{ $c }}"}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: "{{ $role }}"}
subjects: [{kind: ServiceAccount, name: "kyverno-{{ $c }}", namespace: {{ $.Release.Namespace }}}]
{{- end }}
{{- end }}
{{- if .Values.grantPV }}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: reports-pv-read
  labels: {rbac.kyverno.io/aggregate-to-reports-controller: "true"}
rules:
  - {apiGroups: [""], resources: [persistentvolumes], verbs: [get, list, watch]}
{{- end }}
`,
	"platform/policies/chart/Chart.yaml": "apiVersion: v2\nname: bp-policies\nversion: 0.1.0\n",
	"platform/policies/chart/templates/policies.yaml": `apiVersion: policies.kyverno.io/v1
kind: ValidatingPolicy
metadata: {name: probes}
spec:
  validationActions: [{{ if eq .Values.consume "local" }}Deny{{ else }}Audit{{ end }}]
  matchConstraints:
    resourceRules:
      - {apiGroups: [apps], apiVersions: [v1], operations: [CREATE, UPDATE], resources: [deployments]}
      - {apiGroups: [""], apiVersions: [v1], operations: [CREATE, UPDATE], resources: [pods]}
  matchConditions:
    - name: exclude-namespaces
      expression: "!(object.metadata.?namespace.orValue('') in ['kyverno'])"
  validations:
    - expression: "object.kind != 'Pod' || object.spec.containers.all(c, size(c.?livenessProbe.orValue({})) > 0 && size(c.?readinessProbe.orValue({})) > 0)"
      message: "a container of the Pod lacks a probe"
    - expression: "object.kind == 'Pod' || object.spec.template.spec.containers.all(c, size(c.?livenessProbe.orValue({})) > 0 && size(c.?readinessProbe.orValue({})) > 0)"
      message: "a container of the workload lacks a probe"
---
apiVersion: policies.kyverno.io/v1
kind: ValidatingPolicy
metadata: {name: refuses-all}
spec:
  validationActions: [{{ .Values.refusesAll }}]
  matchConstraints:
    resourceRules:
      - {apiGroups: [apps], apiVersions: [v1], operations: [CREATE, UPDATE], resources: [deployments]}
  matchConditions:
    - name: exclude-namespaces
      expression: "!(object.metadata.?namespace.orValue('') in ['kyverno'])"
  validations:
    - expression: "false"
      message: "refuses every Deployment"
---
apiVersion: policies.kyverno.io/v1
kind: ValidatingPolicy
metadata: {name: flux-labelled}
spec:
  validationActions: [Deny]
  matchConstraints:
    resourceRules:
      - {apiGroups: [apps], apiVersions: [v1], operations: [CREATE, UPDATE], resources: [deployments]}
  matchConditions:
    - name: exclude-namespaces
      expression: "!(object.metadata.?namespace.orValue('') in ['kyverno'])"
  validations:
    - expression: "'helm.toolkit.fluxcd.io/name' in object.metadata.?labels.orValue({})"
      message: "helm-controller's origin label is missing"
---
apiVersion: policies.kyverno.io/v1
kind: ValidatingPolicy
metadata: {name: local-path}
spec:
  validationActions: [Deny]
  matchConstraints:
    resourceRules:
      - {apiGroups: [""], apiVersions: [v1], operations: [CREATE, UPDATE], resources: [persistentvolumes]}
  validations:
    - expression: "true"
{{- with .Values.exception }}
---
apiVersion: policies.kyverno.io/v1
kind: PolicyException
metadata: {name: web-probes, namespace: {{ . }}}
spec:
  policyRefs: [{name: probes, kind: ValidatingPolicy}]
  matchConditions:
    - name: scope
      expression: "object.metadata.?namespace.orValue('') == 'apps' && object.kind == 'Deployment' && object.metadata.name == 'web'"
{{- end }}
`,
	"platform/app/chart/Chart.yaml": "apiVersion: v2\nname: bp-app\nversion: 0.1.0\ndependencies:\n  - name: upstream\n    version: 0.1.0\n",
	"platform/app/chart/templates/web.yaml": `{{- define "probes" }}
          livenessProbe: {tcpSocket: {port: 80}}
          readinessProbe: {tcpSocket: {port: 80}}
{{- end }}
apiVersion: apps/v1
kind: Deployment
metadata: {name: web}
spec:
  selector: {matchLabels: {app: web}}
  template:
    metadata: {labels: {app: web}}
    spec:
      containers:
        - name: web
          image: registry.example/proxy-dockerhub/library/nginx:1.27
          {{- if .Values.webProbes }}{{ include "probes" . }}{{ end }}
---
apiVersion: v1
kind: Pod
metadata:
  name: app-test
  annotations: {helm.sh/hook: test}
spec:
  restartPolicy: Never
  containers:
    - {name: test, image: "registry.example/proxy-dockerhub/library/busybox:1.36"}
{{- if eq (toString .Values.qa) "true" }}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: qa-fixture}
spec:
  selector: {matchLabels: {app: qa}}
  template:
    metadata: {labels: {app: qa}}
    spec:
      containers:
        - {name: qa, image: "registry.example/proxy-dockerhub/library/busybox:1.36"}
{{- end }}
`,
	"platform/app/chart/charts/upstream/Chart.yaml": "apiVersion: v2\nname: upstream\nversion: 0.1.0\n",
	"platform/app/chart/charts/upstream/templates/worker.yaml": `apiVersion: apps/v1
kind: Deployment
metadata: {name: upstream-worker}
spec:
  selector: {matchLabels: {app: worker}}
  template:
    metadata: {labels: {app: worker}}
    spec:
      containers:
        - name: worker
          image: registry.example/proxy-dockerhub/library/nginx:1.27
          livenessProbe: {tcpSocket: {port: 80}}
          readinessProbe: {tcpSocket: {port: 80}}
        - name: sidecar
          image: registry.example/proxy-dockerhub/library/busybox:1.36
          {{- if .Values.sidecarProbes }}
          livenessProbe: {tcpSocket: {port: 81}}
          readinessProbe: {tcpSocket: {port: 81}}
          {{- end }}
`,
	"clusters/_template/bootstrap-kit/kustomization.yaml": "resources:\n  - 01-engine.yaml\n  - 02-policies.yaml\n  - 03-app.yaml\n",
}

// kit is the HelmReleases of the fixture kit; a case changes them.
type kit struct {
	engine, policies, app obj
}

func baseKit() kit {
	hr := func(name, chart, target string, values obj) obj {
		return obj{"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
			"metadata": obj{"name": name, "namespace": "flux-system"},
			"spec": obj{"releaseName": strings.TrimPrefix(name, "bp-"), "targetNamespace": target,
				"chart": obj{"spec": obj{"chart": chart, "version": "0.1.0"}}, "values": values}}
	}
	return kit{
		engine:   hr("bp-kyverno", "bp-kyverno", "kyverno", obj{"grantPV": true}),
		policies: hr("bp-policies", "bp-policies", "kyverno", obj{"consume": "${SOVEREIGN_CONSUME:=origin}", "refusesAll": "Audit"}),
		app: hr("bp-app", "bp-app", "apps", obj{"webProbes": true, "qa": "${QA_FIXTURES_ENABLED:-false}",
			"upstream": obj{"sidecarProbes": true}}),
	}
}

// tree writes the fixture with the kit and the register, and returns its root.
func tree(t *testing.T, k kit, gaps string) (string, string) {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{}
	for p, c := range fixture {
		files[p] = c
	}
	for name, hr := range map[string]obj{"01-engine.yaml": k.engine, "02-policies.yaml": k.policies, "03-app.yaml": k.app} {
		b, err := json.Marshal(hr)
		if err != nil {
			t.Fatal(err)
		}
		files[filepath.Join(kitDir, name)] = string(b) + "\n"
	}
	for p, c := range files {
		path := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reg := filepath.Join(root, "known-gaps.json")
	if err := os.WriteFile(reg, []byte(gaps), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, reg
}

const noGaps = `{"gaps": []}`

func runOn(t *testing.T, k kit, gaps string) (int, string) {
	t.Helper()
	root, reg := tree(t, k, gaps)
	var out, errb bytes.Buffer
	code := run([]string{"-root", root, "-gaps", reg}, &out, &errb)
	return code, out.String() + errb.String()
}

func set(o obj, v any, path ...string) {
	for _, p := range path[:len(path)-1] {
		next, ok := o[p].(obj)
		if !ok {
			next = obj{}
			o[p] = next
		}
		o = next
	}
	o[path[len(path)-1]] = v
}

// lines returns the lines of the output that start with the prefix.
func lines(out, prefix string) []string {
	var got []string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, prefix) {
			got = append(got, l)
		}
	}
	return got
}

func TestKit(t *testing.T) {
	needTools(t)
	type check struct {
		name  string
		edit  func(k *kit)
		gaps  string
		code  int
		lines []string // every line that starts with "kit-policies: " and a word in capitals, in order
	}
	refusedWeb := "kit-policies: REFUSED Deployment apps/web by probes: a container of the workload lacks a probe; rendered by 03-app.yaml (release flux-system/bp-app, chart bp-app)"
	cases := []check{
		{name: "the kit as it is passes: the QA fixture is off after the overlay, the test hook is not created, the origin labels are set", code: 0},
		{name: "a refused workload", edit: func(k *kit) { set(k.app, false, "spec", "values", "webProbes") }, code: 1, lines: []string{refusedWeb}},
		{name: "a sidecar of an upstream sub-chart without probes", edit: func(k *kit) { set(k.app, false, "spec", "values", "upstream", "sidecarProbes") }, code: 1,
			lines: []string{"kit-policies: REFUSED Deployment apps/upstream-worker by probes: a container of the workload lacks a probe; rendered by 03-app.yaml (release flux-system/bp-app, chart bp-app)"}},
		{name: "a workload an exception covers", edit: func(k *kit) {
			set(k.app, false, "spec", "values", "webProbes")
			set(k.policies, "policy-exceptions", "spec", "values", "exception")
		}, code: 0},
		{name: "an exception in a namespace the engine does not honour covers nothing", edit: func(k *kit) {
			set(k.app, false, "spec", "values", "webProbes")
			set(k.policies, "apps", "spec", "values", "exception")
		}, code: 1, lines: []string{refusedWeb}},
		{name: "a test hook of a release with tests is judged", edit: func(k *kit) { set(k.app, true, "spec", "test", "enable") }, code: 1,
			lines: []string{"kit-policies: REFUSED Pod apps/app-test by probes: a container of the Pod lacks a probe; rendered by 03-app.yaml (release flux-system/bp-app, chart bp-app)"}},
		{name: "a test hook the release's test filters exclude is not created", edit: func(k *kit) {
			set(k.app, true, "spec", "test", "enable")
			set(k.app, []any{obj{"name": "app-test", "exclude": true}}, "spec", "test", "filters")
		}, code: 0},
		{name: "a test hook a filter without exclude does not name is not created", edit: func(k *kit) {
			set(k.app, true, "spec", "test", "enable")
			set(k.app, []any{obj{"name": "other-test"}}, "spec", "test", "filters")
		}, code: 0},
		{name: "a test hook a filter without exclude names is judged", edit: func(k *kit) {
			set(k.app, true, "spec", "test", "enable")
			set(k.app, []any{obj{"name": "app-test"}, obj{"name": "other-test", "exclude": true}}, "spec", "test", "filters")
		}, code: 1,
			lines: []string{"kit-policies: REFUSED Pod apps/app-test by probes: a container of the Pod lacks a probe; rendered by 03-app.yaml (release flux-system/bp-app, chart bp-app)"}},
		{name: "a postRenderer that changes the verdict", edit: func(k *kit) {
			set(k.app, false, "spec", "values", "webProbes")
			set(k.app, []any{obj{"kustomize": obj{"patches": []any{obj{
				"target": obj{"kind": "Deployment", "name": "web"},
				"patch":  "apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: web}\nspec:\n  template:\n    spec:\n      containers:\n        - name: web\n          livenessProbe: {tcpSocket: {port: 80}}\n          readinessProbe: {tcpSocket: {port: 80}}\n",
			}}}}}, "spec", "postRenderers")
		}, code: 0},
		{name: "a policy that is not enforcing refuses nothing, the same policy denying does", edit: func(k *kit) { set(k.policies, "Deny", "spec", "values", "refusesAll") }, code: 1,
			lines: []string{
				"kit-policies: REFUSED Deployment apps/upstream-worker by refuses-all: refuses every Deployment; rendered by 03-app.yaml (release flux-system/bp-app, chart bp-app)",
				"kit-policies: REFUSED Deployment apps/web by refuses-all: refuses every Deployment; rendered by 03-app.yaml (release flux-system/bp-app, chart bp-app)",
			}},
		{name: "a kind a policy matches that its controller cannot get, list and watch", edit: func(k *kit) { set(k.engine, false, "spec", "values", "grantPV") }, code: 1,
			lines: []string{
				"kit-policies: PERMISSION ValidatingPolicy local-path: it matches /persistentvolumes; the reports-controller lacks get on /persistentvolumes (no ClusterRole of the render bound or aggregated to it grants it; the policy is never ready)",
				"kit-policies: PERMISSION ValidatingPolicy local-path: it matches /persistentvolumes; the reports-controller lacks list on /persistentvolumes (no ClusterRole of the render bound or aggregated to it grants it; the policy is never ready)",
				"kit-policies: PERMISSION ValidatingPolicy local-path: it matches /persistentvolumes; the reports-controller lacks watch on /persistentvolumes (no ClusterRole of the render bound or aggregated to it grants it; the policy is never ready)",
			}},
		{name: "a refusal the register names passes", edit: func(k *kit) { set(k.app, false, "spec", "values", "webProbes") },
			gaps: `{"gaps": [{"kind": "Deployment", "namespace": "apps", "name": "web", "policy": "probes", "reason": "r", "since": "2026-11-26", "retiredBy": "FND-193"}]}`, code: 0,
			lines: []string{"kit-policies: KNOWN Deployment apps/web, refused by probes, in the register (since 2026-11-26, retired by FND-193): 03-app.yaml (release flux-system/bp-app, chart bp-app)"}},
		{name: "a gap row whose object is not rendered",
			gaps: `{"gaps": [{"kind": "Deployment", "namespace": "apps", "name": "gone", "policy": "probes", "reason": "r", "since": "2026-11-26", "retiredBy": "FND-193"}]}`, code: 1,
			lines: []string{"kit-policies: GAP ROW Deployment apps/gone (probes): the render holds no such object; delete the row from tools/guards/cmd/kit-policies/known-gaps.json"}},
		{name: "a gap row whose object passes",
			gaps: `{"gaps": [{"kind": "Deployment", "namespace": "apps", "name": "web", "policy": "probes", "reason": "r", "since": "2026-11-26", "retiredBy": "FND-193"}]}`, code: 1,
			lines: []string{"kit-policies: GAP ROW Deployment apps/web (probes): the policy admits it; delete the row from tools/guards/cmd/kit-policies/known-gaps.json"}},
	}
	marked := regexp.MustCompile(`^kit-policies: (REFUSED|KNOWN|GAP ROW|PERMISSION) `)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			k := baseKit()
			if c.edit != nil {
				c.edit(&k)
			}
			gaps := c.gaps
			if gaps == "" {
				gaps = noGaps
			}
			code, out := runOn(t, k, gaps)
			var got []string
			for _, l := range strings.Split(out, "\n") {
				if marked.MatchString(l) {
					got = append(got, l)
				}
			}
			if code != c.code || strings.Join(got, "\n") != strings.Join(c.lines, "\n") {
				t.Fatalf("exit %d, want %d\ngot lines:\n%s\nwant:\n%s\noutput:\n%s", code, c.code, strings.Join(got, "\n"), strings.Join(c.lines, "\n"), out)
			}
			// Three releases, the engine's six objects and more: the render is not empty.
			if c.code == 0 && len(lines(out, "kit-policies: 3 release render(s)")) != 1 {
				t.Fatalf("the summary does not count the three releases of the fixture:\n%s", out)
			}
		})
	}
}

func TestKitStops(t *testing.T) {
	needTools(t)
	t.Run("a CLI of another version than the engine's", func(t *testing.T) {
		k := baseKit()
		root, reg := tree(t, k, noGaps)
		chart := filepath.Join(root, "platform/kyverno/chart/Chart.yaml")
		if err := os.WriteFile(chart, []byte("apiVersion: v2\nname: bp-kyverno\nversion: 0.1.0\nappVersion: \"v1.18.0\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if code := run([]string{"-root", root, "-gaps", reg}, &out, &out); code != 2 || !strings.Contains(out.String(), "is not the engine's version v1.18.0") {
			t.Fatalf("exit %d, want 2 naming the version:\n%s", code, out.String())
		}
	})
	t.Run("a render with no enforcing policy", func(t *testing.T) {
		k := baseKit()
		set(k.policies, true, "spec", "suspend")
		var out bytes.Buffer
		root, reg := tree(t, k, noGaps)
		if code := run([]string{"-root", root, "-gaps", reg}, &out, &out); code != 2 || !strings.Contains(out.String(), "holds no enforcing policy") {
			t.Fatalf("exit %d, want 2:\n%s", code, out.String())
		}
	})
}
