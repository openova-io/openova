package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// policy is a ClusterPolicy of the shape of flux-managed as the kit renders it
// (platform/kyverno-policies/chart/templates/baseline/10-flux-managed.yaml).
// The tests change it to show that the guard reads its rule from it.
func policy() obj {
	var p obj
	text := `{"apiVersion":"kyverno.io/v1","kind":"ClusterPolicy","metadata":{"name":"flux-managed"},
"spec":{"validationFailureAction":"Audit","rules":[{"name":"workload-must-be-flux-managed",
"match":{"any":[{"resources":{"kinds":["Deployment","Job","CronJob","Service"]}}]},
"exclude":{"any":[
 {"resources":{"namespaces":["kube-system","flux-system"]}},
 {"resources":{"selector":{"matchLabels":{"app.kubernetes.io/managed-by":"cloudnative-pg"}}}},
 {"resources":{"namespaceSelector":{"matchExpressions":[{"key":"openova.io/organization","operator":"Exists"}]}}},
 {"resources":{"kinds":["Service"],"names":["test-service-delete-me-*"]}},
 {"resources":{"annotations":{"helm.sh/hook":"*"}}}]},
"validate":{"message":"not Flux-managed","deny":{"conditions":{
 "all":[
  {"key":"{{ request.object.metadata.labels.\"app.kubernetes.io/managed-by\" || '' }}","operator":"NotEquals","value":"flux"},
  {"key":"{{ request.object.metadata.labels.\"helm.toolkit.fluxcd.io/name\" || '' }}","operator":"Equals","value":""},
  {"key":"{{ request.object.metadata.labels.\"kustomize.toolkit.fluxcd.io/name\" || '' }}","operator":"Equals","value":""}],
 "any":[
  {"key":"{{ request.object.spec.template.metadata.labels.\"pkg.crossplane.io/revision\" || request.object.spec.selector.\"pkg.crossplane.io/revision\" || '' }}","operator":"Equals","value":""},
  {"key":"{{ request.object.metadata.ownerReferences[?controller && starts_with(apiVersion, 'pkg.crossplane.io/')].kind || '' }}","operator":"AnyNotIn","value":["ProviderRevision","FunctionRevision"]}]}}}}]}}`
	if err := json.Unmarshal([]byte(text), &p); err != nil {
		panic(err)
	}
	return p
}

// cronJob renders a CronJob; jobLabels nil leaves jobTemplate.metadata out.
func cronJob(name string, jobLabels, jobAnnotations map[string]string) obj {
	jt := obj{"spec": obj{"template": obj{
		"metadata": obj{"labels": obj{"catalyst.openova.io/component": name}},
		"spec":     obj{"restartPolicy": "Never"},
	}}}
	if jobLabels != nil || jobAnnotations != nil {
		jt["metadata"] = obj{"labels": toAny(jobLabels), "annotations": toAny(jobAnnotations)}
	}
	return obj{
		"apiVersion": "batch/v1", "kind": "CronJob",
		"metadata": obj{"name": name, "labels": obj{"catalyst.openova.io/component": name}},
		"spec":     obj{"schedule": "17 * * * *", "jobTemplate": jt},
	}
}

// writeRender writes one release render file a key, "<chart>.<namespace>".
func writeRender(t *testing.T, files map[string][]obj) string {
	t.Helper()
	dir := t.TempDir()
	n := 0
	for key, objects := range files {
		n++
		var b strings.Builder
		b.WriteString("null\n")
		for _, o := range objects {
			line, err := json.Marshal(o)
			if err != nil {
				t.Fatal(err)
			}
			b.Write(line)
			b.WriteString("\n")
		}
		name := filepath.Join(dir, strings.Join([]string{itoa(n), key, "jsonl"}, "."))
		if err := os.WriteFile(name, []byte(b.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func runGuard(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errw bytes.Buffer
	code := run(args, &out, &errw)
	return code, out.String(), errw.String()
}

// The kit's policy release and one CronJob release.
func render(t *testing.T, ns string, p obj, cron obj) string {
	t.Helper()
	return writeRender(t, map[string][]obj{
		"bp-kyverno-policies.kyverno": {p},
		"bp-harbor." + ns:             {cron},
	})
}

func TestVerdicts(t *testing.T) {
	flux := map[string]string{"app.kubernetes.io/managed-by": "flux"}
	cases := []struct {
		name string
		ns   string
		p    func() obj
		cron obj
		code int
		want []string
	}{
		{"a CronJob without the label fails and is named", "harbor", policy,
			cronJob("harbor-proxy-cache-integrity", nil, nil), 1,
			[]string{"FAIL bp-harbor CronJob harbor/harbor-proxy-cache-integrity: its Job would be denied", "jobTemplate.metadata.labels carries none of app.kubernetes.io/managed-by: flux"}},
		{"labels on the jobTemplate without the one the policy names fail", "harbor", policy,
			cronJob("c", map[string]string{"app.kubernetes.io/managed-by": "Helm", "app.kubernetes.io/name": "x"}, nil), 1,
			[]string{"FAIL bp-harbor CronJob harbor/c: its Job"}},
		{"with the label it passes", "harbor", policy,
			cronJob("c", flux, nil), 0,
			[]string{"PASS bp-harbor CronJob harbor/c: its Job is admitted (it carries one of app.kubernetes.io/managed-by: flux"}},
		{"in a namespace the policy excludes it passes", "flux-system", policy,
			cronJob("c", nil, nil), 0,
			[]string{"PASS bp-harbor CronJob flux-system/c: its Job is admitted (excluded by the policy: the filter of namespaces (flux-system))"}},
		{"the Flux label the rule also accepts passes", "harbor", policy,
			cronJob("c", map[string]string{"helm.toolkit.fluxcd.io/name": "bp-harbor"}, nil), 0, nil},
		{"the hook annotation the policy excludes passes", "harbor", policy,
			cronJob("c", nil, map[string]string{"helm.sh/hook": "post-install"}), 0, nil},
		{"the CNPG operator label the policy excludes passes", "harbor", policy,
			cronJob("c", map[string]string{"app.kubernetes.io/managed-by": "cloudnative-pg"}, nil), 0, nil},
		{"the required value is read from the policy, never a copy", "harbor",
			func() obj {
				p := policy()
				c := dig(p, "spec", "rules").([]any)[0].(obj)
				dig(c, "validate", "deny", "conditions", "all").([]any)[0].(obj)["value"] = "argo"
				return p
			},
			cronJob("c", flux, nil), 1, []string{"carries none of app.kubernetes.io/managed-by: argo"}},
		{"the exclusions are read from the policy, never a copy", "harbor",
			func() obj {
				p := policy()
				ex := dig(p, "spec", "rules").([]any)[0].(obj)["exclude"].(obj)["any"].([]any)[0].(obj)
				ex["resources"].(obj)["namespaces"] = []any{"harbor"}
				return p
			},
			cronJob("c", nil, nil), 0, []string{"namespaces (harbor)"}},
		{"a policy that matches Pods judges the Pod template too", "harbor",
			func() obj {
				p := policy()
				m := dig(p, "spec", "rules").([]any)[0].(obj)["match"].(obj)["any"].([]any)[0].(obj)
				m["resources"].(obj)["kinds"] = []any{"Job", "Pod"}
				return p
			},
			cronJob("c", flux, nil), 1, []string{"FAIL bp-harbor CronJob harbor/c: the Pod of its Job would be denied by the rule: jobTemplate.spec.template.metadata.labels"}},
		{"a condition the guard cannot read holds, so the label cannot be skipped", "harbor",
			func() obj {
				p := policy()
				c := dig(p, "spec", "rules").([]any)[0].(obj)
				all := dig(c, "validate", "deny", "conditions", "all").([]any)
				all[1].(obj)["key"] = "{{ request.object.metadata.labels[?x] }}"
				all[2].(obj)["key"] = "{{ to_upper(request.object.kind) }}"
				return p
			},
			cronJob("c", map[string]string{"helm.toolkit.fluxcd.io/name": "x"}, nil), 1, []string{"carries none of app.kubernetes.io/managed-by: flux"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := render(t, c.ns, c.p(), c.cron)
			code, out, errw := runGuard(t, "-render", dir)
			if code != c.code {
				t.Fatalf("exit %d, want %d\nout:\n%s\nerr:\n%s", code, c.code, out, errw)
			}
			for _, w := range c.want {
				if !strings.Contains(out, w) {
					t.Errorf("the report lacks %q:\n%s", w, out)
				}
			}
		})
	}
}

func TestPolicyCannotBeRead(t *testing.T) {
	dir := writeRender(t, map[string][]obj{"bp-harbor.harbor": {cronJob("c", nil, nil)}})
	bad := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(bad, []byte("apiVersion: kyverno.io/v1\nkind: ClusterPolicy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	noRule := policy()
	dig(noRule, "spec", "rules").([]any)[0].(obj)["name"] = "another-rule"
	noLabel := policy()
	dig(noLabel, "spec", "rules").([]any)[0].(obj)["validate"] = obj{"deny": obj{"conditions": obj{"all": []any{
		obj{"key": "{{ request.object.kind }}", "operator": "Equals", "value": "Job"}}}}}
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"a policy file that does not exist", []string{"-render", dir, "-policy", filepath.Join(t.TempDir(), "missing.json")}, "the policy file cannot be read"},
		{"a policy file that is not JSON", []string{"-render", dir, "-policy", bad}, "the policy file cannot be read"},
		{"a render without the policy", []string{"-render", dir}, "no ValidatingPolicy or ClusterPolicy flux-managed"},
		{"a policy without the rule", []string{"-render", render(t, "harbor", noRule, cronJob("c", nil, nil))}, "no rule workload-must-be-flux-managed"},
		{"a rule that names no label", []string{"-render", render(t, "harbor", noLabel, cronJob("c", nil, nil))}, "names no label"},
		{"a render that does not exist", []string{"-render", filepath.Join(t.TempDir(), "none")}, "the render cannot be read"},
		{"an empty render", []string{"-render", t.TempDir()}, "holds no release render"},
		{"a render without a CronJob", []string{"-render", writeRender(t, map[string][]obj{"bp-kyverno-policies.kyverno": {policy()}})}, "holds no CronJob"},
		{"no render given", nil, "usage"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, out, errw := runGuard(t, c.args...)
			if code != 2 {
				t.Fatalf("exit %d, want 2 (never a pass)\nout:\n%s\nerr:\n%s", code, out, errw)
			}
			if !strings.Contains(errw, c.want) {
				t.Errorf("the error lacks %q: %s", c.want, errw)
			}
		})
	}
}

// A policy file given with -policy is read in place of the render's.
func TestPolicyFile(t *testing.T) {
	dir := writeRender(t, map[string][]obj{"bp-harbor.harbor": {cronJob("c", nil, nil)}})
	file := filepath.Join(t.TempDir(), "policy.json")
	b, _ := json.Marshal(policy())
	if err := os.WriteFile(file, b, 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errw := runGuard(t, "-render", dir, "-policy", file)
	if code != 1 || !strings.Contains(out, "FAIL bp-harbor CronJob harbor/c") {
		t.Fatalf("exit %d, want 1 naming the CronJob\nout:\n%s\nerr:\n%s", code, out, errw)
	}
}

func TestWildcardAndKind(t *testing.T) {
	for _, c := range []struct {
		p, s string
		want bool
	}{{"*", "", true}, {"test-*", "test-x", true}, {"test-*", "tes", false}, {"a?c", "abc", true}, {"", "x", false}} {
		if got := wildcard(c.p, c.s); got != c.want {
			t.Errorf("wildcard(%q, %q) = %v", c.p, c.s, got)
		}
	}
	for _, c := range []struct {
		p    string
		want bool
	}{{"Job", true}, {"batch/v1/Job", true}, {"batch/*/Job", true}, {"v1/Job", true}, {"apps/v1/Job", false}, {"Job/status", false}, {"CronJob", false}, {"*", true}} {
		if got := kindMatches(c.p, "batch/v1", "Job"); got != c.want {
			t.Errorf("kindMatches(%q) = %v", c.p, got)
		}
	}
}

// validatingPolicy is the ValidatingPolicy flux-managed of the shape the kit
// renders (templates/baseline/10-flux-managed.yaml of
// platform/kyverno-policies/chart; tests/flux-managed/policy.yaml there is its
// render), with a shorter namespace list. The tests change it to show that
// the guard reads it.
func validatingPolicy() obj {
	var p obj
	text := `{"apiVersion":"policies.kyverno.io/v1","kind":"ValidatingPolicy","metadata":{"name":"flux-managed"},"spec":{"validationActions":["Audit"],
"matchConstraints":{"resourceRules":[
 {"apiGroups":["apps"],"apiVersions":["v1"],"operations":["CREATE","UPDATE"],"resources":["deployments","statefulsets","daemonsets"]},
 {"apiGroups":["batch"],"apiVersions":["v1"],"operations":["CREATE","UPDATE"],"resources":["jobs","cronjobs"]},
 {"apiGroups":[""],"apiVersions":["v1"],"operations":["CREATE","UPDATE"],"resources":["services"]}]},
"matchConditions":[
 {"name":"exclude-namespaces","expression":"!(object.metadata.?namespace.orValue('') in ['kube-system', 'flux-system'])"},
 {"name":"exclude-cnpg-operator-objects","expression":"object.metadata.?labels.orValue({})[?'app.kubernetes.io/managed-by'].orValue('') != 'cloudnative-pg'"},
 {"name":"exclude-organization-namespaces","expression":"!('openova.io/organization' in namespaceObject.?metadata.?labels.orValue({}))"},
 {"name":"exclude-vcluster-cidr-probe","expression":"!(object.kind == 'Service' && object.metadata.?name.orValue('').startsWith('test-service-delete-me-'))"},
 {"name":"exclude-helm-hooks","expression":"!('helm.sh/hook' in object.metadata.?annotations.orValue({}))"}],
"variables":[
 {"name":"labels","expression":"object.metadata.?labels.orValue({})"},
 {"name":"crossplaneRevisionLabelled","expression":"object.?spec.?template.?metadata.?labels[?'pkg.crossplane.io/revision'].orValue('') != '' || object.?spec.?selector[?'pkg.crossplane.io/revision'].orValue('') != ''"},
 {"name":"crossplaneOwnerKinds","expression":"object.metadata.?ownerReferences.orValue([]).filter(r, r.?controller.orValue(false) == true && r.?apiVersion.orValue('').startsWith('pkg.crossplane.io/')).map(r, r.?kind.orValue(''))"}],
"validations":[{"expression":"variables.labels[?'app.kubernetes.io/managed-by'].orValue('') == 'flux' || variables.labels[?'helm.toolkit.fluxcd.io/name'].orValue('') != '' || variables.labels[?'kustomize.toolkit.fluxcd.io/name'].orValue('') != '' || (variables.crossplaneRevisionLabelled && size(variables.crossplaneOwnerKinds) > 0 && variables.crossplaneOwnerKinds.all(k, k in ['ProviderRevision', 'FunctionRevision']))"}]}}`
	if err := json.Unmarshal([]byte(text), &p); err != nil {
		panic(err)
	}
	return p
}

func vpList(p obj, key string) []any { return dig(p, "spec", key).([]any) }

// changeVP returns the policy with one change.
func changeVP(change func(p obj)) func() obj {
	return func() obj {
		p := validatingPolicy()
		change(p)
		return p
	}
}

// renderWith writes the kit's policy release and one release of the objects.
func renderWith(t *testing.T, ns string, p obj, objects ...obj) string {
	t.Helper()
	return writeRender(t, map[string][]obj{
		"bp-kyverno-policies.kyverno": {p},
		"bp-harbor." + ns:             objects,
	})
}

func TestValidatingPolicyVerdicts(t *testing.T) {
	flux := map[string]string{"app.kubernetes.io/managed-by": "flux"}
	named := "carries none of app.kubernetes.io/managed-by: flux, helm.toolkit.fluxcd.io/name (any value), kustomize.toolkit.fluxcd.io/name (any value)"
	acme := obj{"apiVersion": "v1", "kind": "Namespace", "metadata": obj{"name": "acme", "labels": obj{"openova.io/organization": "acme"}}}
	cases := []struct {
		name    string
		ns      string
		p       func() obj
		objects []obj
		code    int
		want    []string
	}{
		{"a Job without a label the policy accepts is denied, and the labels are named", "harbor", validatingPolicy,
			[]obj{cronJob("harbor-proxy-cache-integrity", nil, nil)}, 1,
			[]string{"FAIL bp-harbor CronJob harbor/harbor-proxy-cache-integrity: its Job would be denied by the policy: jobTemplate.metadata.labels " + named}},
		{"a Job of another manager is denied", "harbor", validatingPolicy,
			[]obj{cronJob("c", map[string]string{"app.kubernetes.io/managed-by": "Helm"}, nil)}, 1, []string{"FAIL bp-harbor CronJob harbor/c: its Job would be denied by the policy"}},
		{"a release label with an empty value is denied", "harbor", validatingPolicy,
			[]obj{cronJob("c", map[string]string{"helm.toolkit.fluxcd.io/name": ""}, nil)}, 1, []string{"FAIL bp-harbor CronJob harbor/c: its Job"}},
		{"a Job with the label is admitted; the Pod is not matched", "harbor", validatingPolicy,
			[]obj{cronJob("c", flux, nil)}, 0,
			[]string{"PASS bp-harbor CronJob harbor/c: its Job is admitted (every validation of the policy holds)",
				"PASS bp-harbor CronJob harbor/c: the Pod of its Job is admitted (the matchConstraints of the policy do not select it)",
				"judged against ValidatingPolicy flux-managed (validationActions [Audit] in this render; the hand-over makes it [Deny]); 0 denied."}},
		{"the Flux release label admits", "harbor", validatingPolicy,
			[]obj{cronJob("c", map[string]string{"helm.toolkit.fluxcd.io/name": "bp-harbor"}, nil)}, 0, []string{"its Job is admitted (every validation"}},
		{"the Kustomization label admits", "harbor", validatingPolicy,
			[]obj{cronJob("c", map[string]string{"kustomize.toolkit.fluxcd.io/name": "apps"}, nil)}, 0, []string{"its Job is admitted (every validation"}},
		{"a namespace the policy excludes is not judged", "flux-system", validatingPolicy,
			[]obj{cronJob("c", nil, nil)}, 0, []string{"PASS bp-harbor CronJob flux-system/c: its Job is admitted (the match condition exclude-namespaces of the policy is false)"}},
		{"the hook annotation is not judged", "harbor", validatingPolicy,
			[]obj{cronJob("c", nil, map[string]string{"helm.sh/hook": "post-install"})}, 0, []string{"(the match condition exclude-helm-hooks of the policy is false)"}},
		{"the CNPG operator label is not judged", "harbor", validatingPolicy,
			[]obj{cronJob("c", map[string]string{"app.kubernetes.io/managed-by": "cloudnative-pg"}, nil)}, 0, []string{"(the match condition exclude-cnpg-operator-objects of the policy is false)"}},
		{"an Organization's namespace of the render is not judged", "acme", validatingPolicy,
			[]obj{acme, cronJob("c", nil, nil)}, 0, []string{"(the match condition exclude-organization-namespaces of the policy is false)"}},
		{"a namespace whose labels the render lacks is judged", "acme", validatingPolicy,
			[]obj{cronJob("c", nil, nil)}, 1, []string{"FAIL bp-harbor CronJob acme/c: its Job would be denied"}},
		{"the required value is read from the policy, never a copy", "harbor",
			changeVP(func(p obj) {
				v := vpList(p, "validations")[0].(obj)
				v["expression"] = strings.Replace(str(v["expression"]), "== 'flux'", "== 'argo'", 1)
			}),
			[]obj{cronJob("c", flux, nil)}, 1, []string{"carries none of app.kubernetes.io/managed-by: argo, "}},
		{"the resource rules are read from the policy", "harbor",
			changeVP(func(p obj) {
				mc := dig(p, "spec", "matchConstraints").(obj)
				mc["resourceRules"] = mc["resourceRules"].([]any)[:1] // apps only
			}),
			[]obj{cronJob("c", nil, nil)}, 0, []string{"its Job is admitted (the matchConstraints of the policy do not select it)"}},
		{"an exclusion of the match constraints is read", "harbor",
			changeVP(func(p obj) {
				dig(p, "spec", "matchConstraints").(obj)["excludeResourceRules"] = []any{obj{"apiGroups": []any{"batch"}, "apiVersions": []any{"*"}, "operations": []any{"*"}, "resources": []any{"jobs"}}}
			}),
			[]obj{cronJob("c", nil, nil)}, 0, []string{"its Job is admitted (the matchConstraints of the policy do not select it)"}},
		{"a policy that matches Pods judges the Pod too", "harbor",
			changeVP(func(p obj) {
				mc := dig(p, "spec", "matchConstraints").(obj)
				mc["resourceRules"] = append(mc["resourceRules"].([]any), obj{"apiGroups": []any{""}, "apiVersions": []any{"v1"}, "operations": []any{"CREATE"}, "resources": []any{"pods"}})
			}),
			[]obj{cronJob("c", flux, nil)}, 1, []string{"FAIL bp-harbor CronJob harbor/c: the Pod of its Job would be denied by the policy: jobTemplate.spec.template.metadata.labels"}},
		{"a validation the guard cannot read denies", "harbor",
			changeVP(func(p obj) {
				p["spec"].(obj)["validations"] = append(vpList(p, "validations"), obj{"expression": "image('nginx').registry() == 'docker.io'"})
			}),
			[]obj{cronJob("c", flux, nil)}, 1, []string{"its Job would be denied by the policy: jobTemplate.metadata.labels " + named + " (validation 2 cannot be read by the guard: the function image is not read by the guard)"}},
		{"a validation that gives no bool denies", "harbor",
			changeVP(func(p obj) {
				p["spec"].(obj)["validations"] = append(vpList(p, "validations"), obj{"expression": "object.kind"})
			}),
			[]obj{cronJob("c", flux, nil)}, 1, []string{"(validation 2 gives no bool)"}},
		{"a match condition the guard cannot read holds, so the namespace cannot be skipped", "flux-system",
			changeVP(func(p obj) {
				vpList(p, "matchConditions")[0].(obj)["expression"] = "!authorizer.serviceAccount('flux-system', 'x').allowed()"
			}),
			[]obj{cronJob("c", nil, nil)}, 1, []string{"FAIL bp-harbor CronJob flux-system/c: its Job would be denied by the policy"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := renderWith(t, c.ns, c.p(), c.objects...)
			code, out, errw := runGuard(t, "-render", dir)
			if code != c.code {
				t.Fatalf("exit %d, want %d\nout:\n%s\nerr:\n%s", code, c.code, out, errw)
			}
			for _, w := range c.want {
				if !strings.Contains(out, w) {
					t.Errorf("the report lacks %q:\n%s", w, out)
				}
			}
		})
	}
}

func TestValidatingPolicyCannotBeRead(t *testing.T) {
	noLabel := changeVP(func(p obj) { p["spec"].(obj)["validations"] = []any{obj{"expression": "object.kind == 'Job'"}} })()
	noValidation := changeVP(func(p obj) { delete(p["spec"].(obj), "validations") })()
	noMatch := changeVP(func(p obj) { delete(p["spec"].(obj), "matchConstraints") })()
	otherGroup := changeVP(func(p obj) { p["apiVersion"] = "admissionregistration.k8s.io/v1" })()
	cases := []struct {
		name string
		p    obj
		want string
	}{
		{"validations that name no label", noLabel, "its validations name no label of object.metadata.labels"},
		{"a policy without validations", noValidation, "it has no spec.validations"},
		{"a policy without matchConstraints", noMatch, "it has no spec.matchConstraints"},
		{"a ValidatingPolicy of another group is not the policy", otherGroup, "no ValidatingPolicy or ClusterPolicy flux-managed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, out, errw := runGuard(t, "-render", renderWith(t, "harbor", c.p, cronJob("c", nil, nil)))
			if code != 2 {
				t.Fatalf("exit %d, want 2 (never a pass)\nout:\n%s\nerr:\n%s", code, out, errw)
			}
			if !strings.Contains(errw, c.want) {
				t.Errorf("the error lacks %q: %s", c.want, errw)
			}
		})
	}
}

// The CEL subset: each expression gives the value CEL gives, and what the
// guard cannot read is an error, never a value.
func TestCEL(t *testing.T) {
	o := obj{"apiVersion": "batch/v1", "kind": "Job",
		"metadata": obj{"name": "j", "namespace": "ns", "labels": obj{"a": "x", "b": ""}},
		"spec":     obj{"parallelism": 2.0, "items": []any{"p", "q"}}}
	e := newEnv(o, map[string]string{"team": "t"}, map[string]*parsed{"two": parse("1 + 1"), "loop": parse("variables.loop")})
	for _, c := range []struct {
		expr string
		want any
	}{
		{`object.metadata.labels['a'] == 'x'`, true},
		{`object.metadata.?labels.orValue({})[?'c'].orValue('none')`, "none"},
		{`object.metadata.?labels.orValue({})[?'b'].orValue('none')`, ""},
		{`object.?spec.?nope.?deeper.orValue(7)`, int64(7)},
		{`object.spec.parallelism == 2 && object.spec.parallelism > 1`, true},
		{`object.spec.items.map(i, i + '!')`, []any{"p!", "q!"}},
		{`object.spec.items.filter(i, i != 'p')`, []any{"q"}},
		{`object.spec.items.all(i, size(i) == 1)`, true},
		{`object.spec.items.exists(i, i == 'z')`, false},
		{`{'k': 1}.size() == 1 && 'k' in {'k': 1} && !('j' in {'k': 1})`, true},
		{`'k' in ['a', 'k']`, true},
		{`has(object.metadata.name) && !has(object.metadata.uid)`, true},
		{`object.kind == 'Job' ? 'job' : 'other'`, "job"},
		{`true || object.nope`, true},
		{`object.nope || true`, true},
		{`false && object.nope`, false},
		{`'abc'.startsWith('a') && 'abc'.endsWith('c') && 'abc'.contains('b') && 'abc'.matches('^a.c$')`, true},
		{`r'\d+' == '\\d+'`, true},
		{`"it's" == 'it\'s'`, true},
		{`namespaceObject.metadata.labels.team`, "t"},
		{`request.operation`, "CREATE"},
		{`oldObject == null`, true},
		{`variables.two == 2`, true},
		{`object.metadata.?name.hasValue() && object.metadata.?name.value() == 'j' && !object.metadata.?uid.hasValue()`, true},
		{`[1] + [2] == [1, 2] && 'a' + 'b' == 'ab' && 1 + 2 == 3 && 1 + 0.5 == 1.5 && -1 < 0 && 'a' < 'b' && 2 >= 2 && !(2 > 2)`, true},
		{`size('añb')`, int64(3)},
		{`[1, 2,].size() == 2 && {'a': 1,}.size() == 1`, true},
	} {
		p := parse(c.expr)
		got, err := e.run(p)
		if err != nil {
			t.Errorf("%s: %v", c.expr, err)
			continue
		}
		if !celEqual(got, c.want) {
			t.Errorf("%s = %#v, want %#v", c.expr, got, c.want)
		}
	}
	for _, expr := range []string{
		`object.nope`, `object.`, `1 +`, `'unterminated`, `1 / 0`, `object.metadata.name.size`, `variables.loop`,
		`variables.none`, `unknownIdent`, `object.kind.reverse()`, `'''x'''`, `b'x'`, `1u`, `[1].all(1, true)`,
		`size(1)`, `'a' < 1`, `object.kind && true`, `[1][5]`, `optional.none().value()`, `object.metadata.labels.a.orValue('')`,
		`1 ? 2 : 3`, `has(object.?metadata)`, `'a' # 'b'`, `optional.none()`, `[1, 2].exists_one(x, x == 1)`,
		`[1].map(x, true, x)`, `2 * 3`, `0x10`, `1e3`, `'A'.lowerAscii()`,
	} {
		if got, err := e.run(parse(expr)); err == nil {
			t.Errorf("%s = %#v, want an error the guard reports", expr, got)
		}
	}
}

// A policy file given with -policy may hold a ValidatingPolicy.
func TestValidatingPolicyFile(t *testing.T) {
	dir := writeRender(t, map[string][]obj{"bp-harbor.harbor": {cronJob("c", nil, nil)}})
	file := filepath.Join(t.TempDir(), "policy.json")
	b, _ := json.Marshal(validatingPolicy())
	if err := os.WriteFile(file, b, 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errw := runGuard(t, "-render", dir, "-policy", file)
	if code != 1 || !strings.Contains(out, "FAIL bp-harbor CronJob harbor/c: its Job would be denied by the policy") {
		t.Fatalf("exit %d, want 1 naming the CronJob\nout:\n%s\nerr:\n%s", code, out, errw)
	}
}
