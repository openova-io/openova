package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// objs parses a JSON array of objects.
func objs(t *testing.T, s string) []obj {
	t.Helper()
	var out []obj
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// engine: the reports and the admission controller, each bound to its own
// aggregating role and to view.
const engine = `
 {"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {"name": "kyverno-reports-controller", "namespace": "kyverno",
   "labels": {"app.kubernetes.io/part-of": "kyverno", "app.kubernetes.io/component": "reports-controller"}},
  "spec": {"template": {"spec": {"serviceAccountName": "kyverno-reports-controller"}}}},
 {"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {"name": "kyverno-admission-controller", "namespace": "kyverno",
   "labels": {"app.kubernetes.io/part-of": "kyverno", "app.kubernetes.io/component": "admission-controller"}},
  "spec": {"template": {"spec": {"serviceAccountName": "kyverno-admission-controller"}}}},
 {"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole", "metadata": {"name": "kyverno:reports-controller"},
  "aggregationRule": {"clusterRoleSelectors": [{"matchLabels": {"rbac.kyverno.io/aggregate-to-reports-controller": "true"}}]}},
 {"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole", "metadata": {"name": "kyverno:admission-controller"},
  "aggregationRule": {"clusterRoleSelectors": [{"matchLabels": {"rbac.kyverno.io/aggregate-to-admission-controller": "true"}}]}},
 {"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding", "metadata": {"name": "r"},
  "roleRef": {"kind": "ClusterRole", "name": "kyverno:reports-controller"},
  "subjects": [{"kind": "ServiceAccount", "name": "kyverno-reports-controller", "namespace": "kyverno"}]},
 {"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding", "metadata": {"name": "rv"},
  "roleRef": {"kind": "ClusterRole", "name": "view"},
  "subjects": [{"kind": "ServiceAccount", "name": "kyverno-reports-controller", "namespace": "kyverno"}]},
 {"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding", "metadata": {"name": "a"},
  "roleRef": {"kind": "ClusterRole", "name": "kyverno:admission-controller"},
  "subjects": [{"kind": "ServiceAccount", "name": "kyverno-admission-controller", "namespace": "kyverno"}]}`

// grant is a ClusterRole aggregated to a controller.
func grant(ctl, group, resource string, verbs ...string) string {
	b, _ := json.Marshal(obj{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole",
		"metadata": obj{"name": "g-" + ctl + "-" + resource, "labels": obj{"rbac.kyverno.io/aggregate-to-" + ctl: "true"}},
		"rules":    []any{obj{"apiGroups": []any{group}, "resources": []any{resource}, "verbs": toList(verbs)}}})
	return string(b)
}

func toList(s []string) []any {
	out := []any{}
	for _, x := range s {
		out = append(out, x)
	}
	return out
}

const pvPolicy = `{"apiVersion": "policies.kyverno.io/v1", "kind": "ValidatingPolicy", "metadata": {"name": "forbid-local-path-storage"},
  "spec": {"validationActions": ["Deny"], "matchConstraints": {"resourceRules": [
    {"apiGroups": [""], "apiVersions": ["v1"], "resources": ["persistentvolumeclaims", "persistentvolumes"]}]}}}`

const lookupPolicy = `{"apiVersion": "policies.kyverno.io/v1", "kind": "ValidatingPolicy", "metadata": {"name": "class-exists"},
  "spec": {"validationActions": ["Deny"], "matchConstraints": {"resourceRules": [
    {"apiGroups": [""], "apiVersions": ["v1"], "resources": ["persistentvolumeclaims"]}]},
   "validations": [{"expression": "resource.Get(\"storage.k8s.io/v1\", \"storageclasses\", \"\", object.spec.storageClassName) != null"}]}}`

const podsPolicy = `{"apiVersion": "policies.kyverno.io/v1", "kind": "ValidatingPolicy", "metadata": {"name": "pods"},
  "spec": {"validationActions": ["Deny"], "matchConstraints": {"resourceRules": [
    {"apiGroups": [""], "apiVersions": ["v1"], "resources": ["pods"]}]}}}`

func TestPermissions(t *testing.T) {
	cases := []struct {
		name     string
		objects  string
		findings []string
		matched  int
		lookups  int
	}{
		{name: "every matched kind granted: persistentvolumes by an aggregated role, persistentvolumeclaims by view",
			objects: engine + "," + grant("reports-controller", "", "persistentvolumes", "get", "list", "watch") + "," + pvPolicy, matched: 2},
		{name: "a kind a policy matches that its controller cannot get, list and watch (PR 8854)",
			objects: engine + "," + grant("reports-controller", "", "persistentvolumes", "get") + "," + pvPolicy, matched: 2,
			findings: []string{
				"kit-policies: PERMISSION ValidatingPolicy forbid-local-path-storage: it matches /persistentvolumes; the reports-controller lacks list on /persistentvolumes (no ClusterRole of the render bound or aggregated to it grants it; the policy is never ready)",
				"kit-policies: PERMISSION ValidatingPolicy forbid-local-path-storage: it matches /persistentvolumes; the reports-controller lacks watch on /persistentvolumes (no ClusterRole of the render bound or aggregated to it grants it; the policy is never ready)",
			}},
		{name: "a lookup its controller cannot read: the reports controller has it, the admission controller not",
			objects: engine + "," + grant("reports-controller", "storage.k8s.io", "storageclasses", "get") + "," + lookupPolicy, matched: 1, lookups: 1,
			findings: []string{
				"kit-policies: PERMISSION ValidatingPolicy class-exists: it looks up storage.k8s.io/storageclasses (resource.Get); the admission-controller lacks get on storage.k8s.io/storageclasses (no ClusterRole of the render bound or aggregated to it grants it; the policy is never ready)",
			}},
		{name: "a lookup both controllers can read",
			objects: engine + "," + grant("reports-controller", "storage.k8s.io", "storageclasses", "get") + "," +
				grant("admission-controller", "storage.k8s.io", "*", "*") + "," + lookupPolicy, matched: 1, lookups: 1},
		{name: "the autogen targets of a pods policy, granted by view",
			objects: engine + "," + podsPolicy, matched: 7},
		{name: "without the binding to view, pods and the autogen targets are not granted",
			objects: strings.Replace(engine, `"name": "view"`, `"name": "not-view"`, 1) + "," + podsPolicy, matched: 7,
			findings: func() []string {
				var out []string
				for _, t := range []string{"/pods", "apps/daemonsets", "apps/deployments", "apps/replicasets", "apps/statefulsets", "batch/cronjobs", "batch/jobs"} {
					for _, v := range []string{"get", "list", "watch"} {
						out = append(out, "kit-policies: PERMISSION ValidatingPolicy pods: it matches "+t+"; the reports-controller lacks "+v+" on "+t+" (no ClusterRole of the render bound or aggregated to it grants it; the policy is never ready)")
					}
				}
				return out
			}()},
		{name: "a background scan switched off needs no read of what it matches",
			objects: engine + "," + strings.Replace(pvPolicy, `"validationActions"`, `"evaluation": {"background": {"enabled": false}}, "validationActions"`, 1), matched: 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := checkPermissions(objs(t, "["+c.objects+"]"))
			if c.matched == 0 {
				if err == nil || !strings.Contains(err.Error(), "inspect nothing") {
					t.Fatalf("want the error that nothing is inspected, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if p.matched != c.matched || p.lookups != c.lookups || strings.Join(p.findings, "\n") != strings.Join(c.findings, "\n") {
				t.Fatalf("matched %d (want %d), lookups %d (want %d), findings:\n%s\nwant:\n%s", p.matched, c.matched, p.lookups, c.lookups,
					strings.Join(p.findings, "\n"), strings.Join(c.findings, "\n"))
			}
		})
	}
}

func TestReadPolicies(t *testing.T) {
	ps, err := readPolicies(objs(t, `[
	 {"apiVersion": "kyverno.io/v1", "kind": "ClusterPolicy", "metadata": {"name": "mixed"},
	  "spec": {"validationFailureAction": "Audit", "rules": [
	   {"name": "audits", "validate": {"message": "m"}},
	   {"name": "enforces", "validate": {"failureAction": "Enforce", "message": "m"}}]}},
	 {"apiVersion": "kyverno.io/v1", "kind": "ClusterPolicy", "metadata": {"name": "stamps"},
	  "spec": {"rules": [{"name": "stamp", "mutate": {"patchStrategicMerge": {}}}]}},
	 {"apiVersion": "policies.kyverno.io/v1", "kind": "ValidatingPolicy", "metadata": {"name": "audit"}, "spec": {"validationActions": ["Audit"]}},
	 {"apiVersion": "policies.kyverno.io/v1", "kind": "ValidatingPolicy", "metadata": {"name": "deny"}, "spec": {"validationActions": ["Deny", "Audit"]}},
	 {"apiVersion": "policies.kyverno.io/v1", "kind": "MutatingPolicy", "metadata": {"name": "inject"}, "spec": {}}]`))
	if err != nil {
		t.Fatal(err)
	}
	var given []string
	for _, d := range ps.docs {
		given = append(given, str(dig(d, "metadata", "name")))
	}
	if ps.enforcing != 2 || ps.mutating != 2 || strings.Join(given, ",") != "mixed,stamps,deny,inject" {
		t.Fatalf("enforcing %d (want 2), mutating %d (want 2), given to the CLI %v (want mixed, stamps, deny, inject)", ps.enforcing, ps.mutating, given)
	}
	res := func(source, policy, rule, r string) result {
		x := result{Source: source, Policy: policy, Rule: rule, Result: r, Message: "m"}
		x.Resources = append(x.Resources, struct {
			Kind      string `json:"kind"`
			Namespace string `json:"namespace"`
			Name      string `json:"name"`
		}{"Deployment", "n", "d"})
		return x
	}
	for _, c := range []struct {
		r    result
		want bool
	}{
		{res("kyverno", "mixed", "enforces", "fail"), true},
		{res("kyverno", "mixed", "autogen-cronjob-enforces", "error"), true},
		{res("kyverno", "mixed", "autogen-audits", "fail"), false},
		{res("kyverno", "mixed", "enforces", "pass"), false},
		{res("kyverno", "stamps", "stamp", "fail"), false},
		{res("KyvernoValidatingPolicy", "deny", "", "fail"), true},
		{res("KyvernoMutatingPolicy", "inject", "", "fail"), false},
		{res("KyvernoValidatingPolicy", "unknown", "", "fail"), true},
	} {
		if _, got := ps.refusal(c.r); got != c.want {
			t.Errorf("%s %s rule %q result %s: refusal %v, want %v", c.r.Source, c.r.Policy, c.r.Rule, c.r.Result, got, c.want)
		}
	}
	for _, bad := range []string{
		`[{"apiVersion": "kyverno.io/v1", "kind": "ClusterPolicy", "metadata": {"name": "o"}, "spec": {"validationFailureActionOverrides": [{"action": "Audit"}], "rules": []}}]`,
		`[{"apiVersion": "policies.kyverno.io/v1", "kind": "ImageValidatingPolicy", "metadata": {"name": "i"}, "spec": {"validationActions": ["Deny"]}}]`,
		`[{"apiVersion": "policies.kyverno.io/v1", "kind": "ValidatingPolicy", "metadata": {"name": "a"}, "spec": {"validationActions": ["Audit"]}}]`,
	} {
		if _, err := readPolicies(objs(t, bad)); err == nil {
			t.Errorf("no error for %s", bad)
		}
	}
}

func TestHonoured(t *testing.T) {
	ctl := func(args string) string {
		return `{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {"name": "a", "labels": {"app.kubernetes.io/part-of": "kyverno", "app.kubernetes.io/component": "admission-controller"}},
		 "spec": {"template": {"spec": {"containers": [{"name": "kyverno", "args": [` + args + `]}]}}}}`
	}
	exc := `{"apiVersion": "policies.kyverno.io/v1", "kind": "PolicyException", "metadata": {"name": "in", "namespace": "catalyst-policy-exceptions"}},
	 {"apiVersion": "kyverno.io/v2", "kind": "PolicyException", "metadata": {"name": "out", "namespace": "apps"}}`
	for _, c := range []struct {
		name, args string
		want       []string
	}{
		{"the namespace of the flag", `"--enablePolicyException=true", "--exceptionNamespace=catalyst-policy-exceptions"`, []string{"in"}},
		{"every namespace without the flag", `"--enablePolicyException=true"`, []string{"in", "out"}},
		{"none when exceptions are off", `"--exceptionNamespace=catalyst-policy-exceptions"`, nil},
	} {
		got, err := honoured(objs(t, "["+ctl(c.args)+","+exc+"]"))
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, o := range got {
			names = append(names, str(dig(o, "metadata", "name")))
		}
		if strings.Join(names, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s: honoured %v, want %v", c.name, names, c.want)
		}
	}
	if _, err := honoured(objs(t, "["+exc+"]")); err == nil {
		t.Error("no error for a render without an admission controller")
	}
}

func TestReadGaps(t *testing.T) {
	dir := t.TempDir()
	row := `{"kind": "CronJob", "namespace": "n", "name": "c", "policy": "p", "reason": "r", "since": "2026-11-26", "retiredBy": "FND-193"}`
	cases := map[string]string{
		"":                             `{"gaps": [` + row + `]}`,
		"no field gaps":                `{}`,
		"unknown field":                `{"gaps": [], "extra": 1}`,
		"has no reason":                `{"gaps": [` + strings.Replace(row, `"reason": "r"`, `"reason": ""`, 1) + `]}`,
		"is not a day":                 `{"gaps": [` + strings.Replace(row, "2026-11-26", "26 November", 1) + `]}`,
		"is not a package of the plan": `{"gaps": [` + strings.Replace(row, "FND-193", "soon", 1) + `]}`,
		"repeats":                      `{"gaps": [` + row + `, ` + row + `]}`,
	}
	for want, text := range cases {
		path := filepath.Join(dir, "gaps.json")
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		rows, err := readGaps(path)
		switch {
		case want == "" && (err != nil || len(rows) != 1 || rows[0].RetiredBy != "FND-193"):
			t.Errorf("a valid register: %v, %v", rows, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("want an error with %q, got %v", want, err)
		}
	}
	if _, err := readGaps(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("no error for a missing register")
	}
	// The register of the tree reads.
	if rows, err := readGaps("known-gaps.json"); err != nil || len(rows) == 0 {
		t.Errorf("known-gaps.json: %v, %d rows", err, len(rows))
	}
}
