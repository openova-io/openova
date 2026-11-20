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
		{"a render without the policy", []string{"-render", dir}, "no ClusterPolicy flux-managed"},
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
