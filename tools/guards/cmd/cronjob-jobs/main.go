// Command cronjob-jobs judges the Jobs that the CronJobs of the kit create, and
// the Pods of those Jobs, against the policy flux-managed as the kit renders
// it: a ValidatingPolicy of policies.kyverno.io since CMP-084, or a legacy
// ClusterPolicy of kyverno.io.
//
// Flux's helm-controller labels the objects of a release (the labels
// helm.toolkit.fluxcd.io/name and namespace on their metadata), so a CronJob
// that a chart renders is admitted by flux-managed. The Job that the CronJob
// controller creates every hour is not an object of the release: it carries
// only the labels and annotations of the CronJob's jobTemplate.metadata. When
// those do not satisfy the policy, every Job is denied once the policy
// enforces (the template's action helper gives Audit until the hand-over and
// Enforce after it), and the CronJob only ever ran before the hand-over. The
// guard therefore judges the rule whatever its action.
//
// Input: -render <dir>, the render of the kit that
// `KIT_RENDER_DIR=<dir> bash scripts/check-freshprov-image-registry.sh --list-images`
// writes: one file <n>.<chart>.<namespace>.jsonl a release render, one JSON
// object a line (the namespace the release installs into). The policy is the
// ValidatingPolicy or ClusterPolicy -policy-name (flux-managed) of that
// render, or of the file -policy (JSON, one object or one a line). The guard
// reads from the policy, never from a copy of its own.
//
// For each CronJob of the render it builds the Job the controller creates
// (kind Job, the CronJob's namespace, the labels and annotations of
// jobTemplate.metadata, jobTemplate.spec, a controller ownerReference to the
// CronJob) and the Pod of that Job (the labels and annotations of
// jobTemplate.spec.template.metadata, with job-name, and a controller
// ownerReference to the Job). Each is judged as Kyverno does on CREATE.
//
// A ValidatingPolicy: an object its matchConstraints do not select
// (resourceRules and excludeResourceRules by group, version, operation,
// resource, resource name and scope; namespaceSelector; objectSelector) is
// admitted; else an object for which a match condition is false is admitted;
// else it is admitted only when every validation is true. The guard evaluates
// the CEL of the match conditions, the variables and the validations itself,
// as a subset of CEL (the guards module has no CEL library): literals (decimal
// numbers, quoted and raw strings), lists and maps; field selection and
// indexing, with the optional forms .? and [?]; unary ! and -, and the
// operators + == != < <= > >= in && || and ?: (&& and || absorb an error as
// CEL does); the functions size, has, orValue, hasValue, value, startsWith,
// endsWith, contains and matches; the macros all, exists, map and filter (two
// arguments); over object, oldObject (null on CREATE), namespaceObject,
// request and variables. Anything else is an expression it cannot read.
//
// A ClusterPolicy: the rule -rule (workload-must-be-flux-managed). An object
// the match of the rule does not select, or that a filter of exclude (kinds,
// names, namespaces, annotations, selector, namespaceSelector, operations)
// selects, is admitted; else it is denied when every condition of
// deny.conditions.all holds and, when there are any, one of
// deny.conditions.any holds.
//
// Where the guard cannot read the policy it errs toward a denial, never a
// pass. A ValidatingPolicy: a field of matchConstraints or of a resource rule
// it does not read selects the object, and excludes nothing; a match condition
// it cannot evaluate holds; a validation it cannot evaluate, or that gives no
// bool, denies. A ClusterPolicy: a match filter it does not understand
// (subjects, roles, clusterRoles) selects the object; an exclude filter it
// does not understand excludes nothing; a condition it cannot evaluate holds.
// A condition key it evaluates is a JMESPath of request.object fields
// (`request.object.a."b/c"`), joined by `||`, with string literals ('x');
// operators Equals, NotEquals (wildcards in the value, as Kyverno), AnyIn,
// AllIn, AnyNotIn, AllNotIn, In and NotIn. A namespaceSelector and
// namespaceObject read the labels of a Namespace object of the render and
// kubernetes.io/metadata.name; the Namespaces the kit applies outside a chart
// are not in the render, so their labels are not read (a selector or a match
// condition on them excludes nothing).
//
// A denial names the chart, the CronJob and the labels the policy names; one
// of them on jobTemplate.metadata.labels admits the Job. A ValidatingPolicy
// names each alternative of the top-level || of a validation that compares
// one label of object.metadata.labels (directly or through a variable) with a
// string: `label: value` (== the value) or `label (any value)` (!= the empty
// string, or 'label' in the labels). A ClusterPolicy names each condition of
// deny.conditions.all that reads one label of request.object.metadata.labels,
// as `label: value` (NotEquals) or `label (any value)` (Equals the empty
// string). When the denial is a validation the guard cannot read, the line
// says so.
//
// Not covered: a Job that a script creates at run time (kubectl create job,
// kubectl apply of a Job manifest, a Job created from a CronJob with --from)
// is not in the render. Those are found by reading the scripts; the Jobs that
// bp-self-sovereign-cutover applies to the secondary regions carry the label.
//
// Usage, from tools/guards:
//
//	go run ./cmd/cronjob-jobs -render <dir> [-policy <file>]
//
// Exit codes: 0 every Job and Pod is admitted; 1 at least one is denied (each
// is named); 2 nothing could be judged: the render or the policy cannot be
// read, the policy, its rule (a ClusterPolicy) or its validations (a
// ValidatingPolicy) are not there, the policy names no label the guard can
// read, or the render holds no CronJob.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func say(w io.Writer, format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }

type obj = map[string]any

// release is one file of the render.
type release struct {
	file, chart, namespace string
	objects                []obj
}

var renderFile = regexp.MustCompile(`^[0-9]+\.([a-z0-9-]+)\.([a-z0-9-]+)\.jsonl$`)

func run(args []string, out, errw io.Writer) int {
	flags := flag.NewFlagSet("cronjob-jobs", flag.ContinueOnError)
	flags.SetOutput(errw)
	render := flags.String("render", "", "the folder of the kit render (KIT_RENDER_DIR of scripts/check-freshprov-image-registry.sh)")
	policyFile := flags.String("policy", "", "a file of the policy in JSON (default: the policy of the render)")
	policyName := flags.String("policy-name", "flux-managed", "the name of the ValidatingPolicy or ClusterPolicy")
	ruleName := flags.String("rule", "workload-must-be-flux-managed", "the name of the rule of a ClusterPolicy (a ValidatingPolicy has none)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() > 0 || *render == "" {
		say(errw, "cronjob-jobs: usage: cronjob-jobs -render <dir> [-policy <file>]\n")
		return 2
	}
	releases, err := readRender(*render)
	if err != nil {
		say(errw, "cronjob-jobs: %v\n", err)
		return 2
	}
	var policies []obj
	if *policyFile != "" {
		objects, err := readObjects(*policyFile)
		if err != nil {
			say(errw, "cronjob-jobs: the policy file cannot be read: %v\n", err)
			return 2
		}
		policies = findPolicies(objects, *policyName)
	} else {
		for _, r := range releases {
			policies = append(policies, findPolicies(r.objects, *policyName)...)
		}
	}
	if len(policies) == 0 {
		say(errw, "cronjob-jobs: no ValidatingPolicy or ClusterPolicy %s in %s: nothing can be judged\n", *policyName, orRender(*policyFile, *render))
		return 2
	}
	var rules []rule
	seen := map[string]bool{}
	for _, p := range policies {
		r, err := readRule(p, *ruleName)
		if err != nil {
			say(errw, "cronjob-jobs: %s %s: %v\n", str(p["kind"]), *policyName, err)
			return 2
		}
		key, _ := json.Marshal(r.raw)
		if !seen[string(key)] {
			seen[string(key)] = true
			rules = append(rules, r)
		}
	}
	nsLabels := namespaceLabels(releases)
	judged, denied := 0, 0
	lines := map[string]bool{}
	var report []string
	for _, rel := range releases {
		for _, o := range rel.objects {
			if str(o["kind"]) != "CronJob" {
				continue
			}
			judged++
			for _, r := range rules {
				for _, v := range judgeCronJob(rel, o, r, nsLabels) {
					if !lines[v.line] {
						lines[v.line] = true
						report = append(report, v.line)
						if v.denied {
							denied++
						}
					}
				}
			}
		}
	}
	if judged == 0 {
		say(errw, "cronjob-jobs: the render in %s holds no CronJob: a render without one is not the kit\n", *render)
		return 2
	}
	sort.Strings(report)
	for _, l := range report {
		say(out, "%s\n", l)
	}
	if str(policies[0]["kind"]) == "ValidatingPolicy" {
		say(out, "cronjob-jobs: %d CronJob render(s) of %d release render(s) judged against ValidatingPolicy %s (validationActions %s in this render; the hand-over makes it [Deny]); %d denied.\n",
			judged, len(releases), *policyName, orDash(actions(policies[0])), denied)
	} else {
		action := str(dig(policies[0], "spec", "validationFailureAction"))
		say(out, "cronjob-jobs: %d CronJob render(s) of %d release render(s) judged against ClusterPolicy %s rule %s (validationFailureAction %s in this render; the hand-over makes it Enforce); %d denied.\n",
			judged, len(releases), *policyName, *ruleName, orDash(action), denied)
	}
	if denied > 0 {
		return 1
	}
	return 0
}

func orRender(policyFile, render string) string {
	if policyFile != "" {
		return policyFile
	}
	return "the render " + render
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// readRender reads every <n>.<chart>.<namespace>.jsonl of dir. A folder
// without one, a file of another name or a line that is not JSON is an error.
func readRender(dir string) ([]release, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("the render cannot be read: %w", err)
	}
	var out []release
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := renderFile.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("the render holds %s, which is not <n>.<chart>.<namespace>.jsonl", e.Name())
		}
		objects, err := readObjects(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, release{file: e.Name(), chart: m[1], namespace: m[2], objects: objects})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the render %s holds no release render", dir)
	}
	return out, nil
}

// readObjects reads a file of JSON objects, one a line or one in the file.
// null documents (an empty YAML document) are skipped.
func readObjects(path string) ([]obj, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- the path is the guard's own input
	if err != nil {
		return nil, err
	}
	var whole any
	if json.Unmarshal(b, &whole) == nil {
		if o, ok := whole.(obj); ok {
			return []obj{o}, nil
		}
	}
	var out []obj
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || line == "null" {
			continue
		}
		var o obj
		if err := json.Unmarshal([]byte(line), &o); err != nil {
			return nil, fmt.Errorf("%s line %d is not a JSON object: %v", path, n, err)
		}
		out = append(out, o)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	return out, nil
}

// findPolicies gives the ValidatingPolicies of policies.kyverno.io and the
// ClusterPolicies of the name.
func findPolicies(objects []obj, name string) []obj {
	var out []obj
	for _, o := range objects {
		kind := str(o["kind"])
		vp := kind == "ValidatingPolicy" && strings.HasPrefix(str(o["apiVersion"]), "policies.kyverno.io/")
		if (vp || kind == "ClusterPolicy") && str(dig(o, "metadata", "name")) == name {
			out = append(out, o)
		}
	}
	return out
}

// actions gives the validationActions of a ValidatingPolicy as `[A, B]`, or "".
func actions(policy obj) string {
	list, _ := dig(policy, "spec", "validationActions").([]any)
	if len(list) == 0 {
		return ""
	}
	var names []string
	for _, x := range list {
		names = append(names, fmt.Sprint(x))
	}
	return "[" + strings.Join(names, ", ") + "]"
}

// rule is what the guard judges by: the rule of a ClusterPolicy, or a
// ValidatingPolicy (vp).
type rule struct {
	raw     obj
	match   obj
	exclude obj
	all     []any
	any     []any
	labels  []string // the labels the policy names, `label: value` or `label (any value)`
	vp      *vpolicy
}

func readRule(policy obj, name string) (rule, error) {
	if str(policy["kind"]) == "ValidatingPolicy" {
		return readValidatingPolicy(policy)
	}
	rules, _ := dig(policy, "spec", "rules").([]any)
	for _, x := range rules {
		r, _ := x.(obj)
		if str(r["name"]) != name {
			continue
		}
		out := rule{raw: r}
		out.match, _ = r["match"].(obj)
		out.exclude, _ = r["exclude"].(obj)
		if out.match == nil {
			return rule{}, fmt.Errorf("rule %s has no match", name)
		}
		conds := dig(r, "validate", "deny", "conditions")
		switch c := conds.(type) {
		case []any:
			out.all = c
		case obj:
			out.all, _ = c["all"].([]any)
			out.any, _ = c["any"].([]any)
		default:
			return rule{}, fmt.Errorf("rule %s has no validate.deny.conditions", name)
		}
		for _, x := range out.all {
			if l := namedLabel(x); l != "" {
				out.labels = append(out.labels, l)
			}
		}
		if len(out.labels) == 0 {
			return rule{}, fmt.Errorf("rule %s names no label of request.object.metadata.labels in deny.conditions.all", name)
		}
		return out, nil
	}
	return rule{}, fmt.Errorf("no rule %s", name)
}

// namedLabel gives `label: value` or `label (any value)` for a condition of
// the form {{ request.object.metadata.labels."<label>" || <empty string> }}
// NotEquals <value>, or Equals the empty string; else "".
func namedLabel(x any) string {
	c, _ := x.(obj)
	alts, ok := parseKey(str(c["key"]))
	if !ok || len(alts) == 0 || len(alts) > 2 || alts[0].literal {
		return ""
	}
	p := alts[0].path
	if len(p) != 5 || p[0] != "request" || p[1] != "object" || p[2] != "metadata" || p[3] != "labels" {
		return ""
	}
	if len(alts) == 2 && (!alts[1].literal || alts[1].text != "") {
		return ""
	}
	v, isString := c["value"].(string)
	if !isString {
		return ""
	}
	switch str(c["operator"]) {
	case "NotEquals":
		return p[4] + ": " + v
	case "Equals":
		if v == "" {
			return p[4] + " (any value)"
		}
	}
	return ""
}

// verdict is one line of the report.
type verdict struct {
	line   string
	denied bool
}

// judgeCronJob judges the Job a CronJob creates and the Pod of that Job.
func judgeCronJob(rel release, cron obj, r rule, nsLabels map[string]map[string]string) []verdict {
	name := str(dig(cron, "metadata", "name"))
	ns := str(dig(cron, "metadata", "namespace"))
	if ns == "" {
		ns = rel.namespace
	}
	jt, _ := dig(cron, "spec", "jobTemplate").(obj)
	jobName := name + "-29919857"
	jobLabels := stringMap(dig(jt, "metadata", "labels"))
	jobAnn := stringMap(dig(jt, "metadata", "annotations"))
	jobAnn["batch.kubernetes.io/cronjob-scheduled-timestamp"] = "2026-11-20T17:17:00Z"
	job := obj{
		"apiVersion": "batch/v1", "kind": "Job",
		"metadata": obj{
			"name": jobName, "namespace": ns, "labels": toAny(jobLabels), "annotations": toAny(jobAnn),
			"ownerReferences": []any{obj{"apiVersion": "batch/v1", "kind": "CronJob", "name": name, "controller": true}},
		},
		"spec": dig(jt, "spec"),
	}
	pt, _ := dig(jt, "spec", "template").(obj)
	podLabels := stringMap(dig(pt, "metadata", "labels"))
	podLabels["job-name"] = jobName
	podLabels["batch.kubernetes.io/job-name"] = jobName
	pod := obj{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": obj{
			"name": jobName + "-x7k2p", "namespace": ns, "labels": toAny(podLabels),
			"annotations":     toAny(stringMap(dig(pt, "metadata", "annotations"))),
			"ownerReferences": []any{obj{"apiVersion": "batch/v1", "kind": "Job", "name": jobName, "controller": true}},
		},
		"spec": dig(pt, "spec"),
	}
	who := fmt.Sprintf("%s CronJob %s/%s", rel.chart, ns, name)
	var out []verdict
	for _, c := range []struct {
		o     obj
		what  string
		where string
	}{
		{job, "its Job", "jobTemplate.metadata.labels"},
		{pod, "the Pod of its Job", "jobTemplate.spec.template.metadata.labels"},
	} {
		admitted, why := judge(c.o, r, labelsOfNamespace(nsLabels, ns))
		if admitted {
			out = append(out, verdict{line: fmt.Sprintf("PASS %s: %s is admitted (%s)", who, c.what, why)})
			continue
		}
		by := "the rule"
		if r.vp != nil {
			by = "the policy"
		}
		line := fmt.Sprintf("FAIL %s: %s would be denied by %s: %s carries none of %s", who, c.what, by, c.where, strings.Join(r.labels, ", "))
		if why != "" {
			line += " (" + why + ")"
		}
		out = append(out, verdict{denied: true, line: line})
	}
	return out
}

// judge admits or denies one object on CREATE, and says why. A denial by the
// rule of a ClusterPolicy says nothing more.
func judge(o obj, r rule, nsLabels map[string]string) (bool, string) {
	if r.vp != nil {
		return r.vp.judge(o, nsLabels)
	}
	if !selects(r.match, o, nsLabels, true) {
		return true, "the match of the rule does not select it"
	}
	if r.exclude != nil && selects(r.exclude, o, nsLabels, false) {
		return true, "excluded by the policy: " + excludedBy(r.exclude, o, nsLabels)
	}
	if denies(r, o) {
		return false, ""
	}
	return true, "it carries one of " + strings.Join(r.labels, ", ")
}

func excludedBy(exclude obj, o obj, nsLabels map[string]string) string {
	for _, f := range filters(exclude) {
		if ok, known := filterSelects(f, o, nsLabels); ok && known {
			res, _ := f["resources"].(obj)
			var keys []string
			for k := range res {
				if k == "namespaces" {
					k = "namespaces (" + str(dig(o, "metadata", "namespace")) + ")"
				}
				keys = append(keys, k)
			}
			sort.Strings(keys)
			return "the filter of " + strings.Join(keys, " and ")
		}
	}
	return "a filter"
}

// filters gives the resource filters of a match or exclude block: those of
// any and all, or the block itself in the old form (resources at its top).
func filters(block obj) []obj {
	var out []obj
	for _, k := range []string{"any", "all"} {
		list, _ := block[k].([]any)
		for _, x := range list {
			if f, ok := x.(obj); ok {
				out = append(out, f)
			}
		}
	}
	if _, ok := block["resources"]; ok {
		out = append(out, block)
	}
	return out
}

// selects tells whether a match (forMatch) or an exclude block selects the
// object. What it cannot read selects on a match and not on an exclude.
func selects(block obj, o obj, nsLabels map[string]string, forMatch bool) bool {
	pick := func(f obj) bool {
		ok, known := filterSelects(f, o, nsLabels)
		if !known {
			return forMatch
		}
		return ok
	}
	if list, ok := block["any"].([]any); ok && len(list) > 0 {
		for _, x := range list {
			f, _ := x.(obj)
			if pick(f) {
				return true
			}
		}
		return false
	}
	if list, ok := block["all"].([]any); ok && len(list) > 0 {
		for _, x := range list {
			f, _ := x.(obj)
			if !pick(f) {
				return false
			}
		}
		return true
	}
	if _, ok := block["resources"]; ok {
		return pick(block)
	}
	return forMatch
}

// filterSelects reads one resource filter; known is false when the filter
// holds a field the guard does not read.
func filterSelects(f obj, o obj, nsLabels map[string]string) (ok, known bool) {
	for _, k := range []string{"subjects", "roles", "clusterRoles"} {
		if v, has := f[k]; has && !empty(v) {
			return false, false
		}
	}
	res, _ := f["resources"].(obj)
	if res == nil {
		return false, false
	}
	labels := stringMap(dig(o, "metadata", "labels"))
	annotations := stringMap(dig(o, "metadata", "annotations"))
	name := str(dig(o, "metadata", "name"))
	ns := str(dig(o, "metadata", "namespace"))
	for k, v := range res {
		switch k {
		case "kinds":
			if !anyString(v, func(p string) bool { return kindMatches(p, str(o["apiVersion"]), str(o["kind"])) }) {
				return false, true
			}
		case "names":
			if !anyString(v, func(p string) bool { return wildcard(p, name) }) {
				return false, true
			}
		case "name":
			if !wildcard(str(v), name) {
				return false, true
			}
		case "namespaces":
			if !anyString(v, func(p string) bool { return wildcard(p, ns) }) {
				return false, true
			}
		case "operations":
			if !anyString(v, func(p string) bool { return p == "CREATE" }) {
				return false, true
			}
		case "annotations":
			want, isMap := v.(obj)
			if !isMap {
				return false, false
			}
			for wk, wv := range want {
				hit := false
				for ak, av := range annotations {
					if wildcard(wk, ak) && wildcard(str(wv), av) {
						hit = true
					}
				}
				if !hit {
					return false, true
				}
			}
		case "selector":
			m, known := labelSelector(v, labels)
			if !known || !m {
				return false, known
			}
		case "namespaceSelector":
			m, known := labelSelector(v, nsLabels)
			if !known || !m {
				return false, known
			}
		default:
			return false, false
		}
	}
	return true, true
}

func labelSelector(v any, labels map[string]string) (ok, known bool) {
	sel, isMap := v.(obj)
	if !isMap {
		return false, false
	}
	for k, x := range sel {
		switch k {
		case "matchLabels":
			want, isMap := x.(obj)
			if !isMap {
				return false, false
			}
			for wk, wv := range want {
				got, has := labels[wk]
				if !has || !wildcard(str(wv), got) {
					return false, true
				}
			}
		case "matchExpressions":
			list, isList := x.([]any)
			if !isList {
				return false, false
			}
			for _, e := range list {
				ex, _ := e.(obj)
				key := str(ex["key"])
				got, has := labels[key]
				values, _ := ex["values"].([]any)
				in := false
				for _, val := range values {
					if str(val) == got {
						in = true
					}
				}
				switch str(ex["operator"]) {
				case "Exists":
					if !has {
						return false, true
					}
				case "DoesNotExist":
					if has {
						return false, true
					}
				case "In":
					if !has || !in {
						return false, true
					}
				case "NotIn":
					if has && in {
						return false, true
					}
				default:
					return false, false
				}
			}
		default:
			return false, false
		}
	}
	return true, true
}

// kindMatches reads a Kyverno kind: Kind, Version/Kind or Group/Version/Kind,
// each part a wildcard. A Kind/subresource selects no object itself.
func kindMatches(p, apiVersion, kind string) bool {
	group, version := "", apiVersion
	if i := strings.Index(apiVersion, "/"); i >= 0 {
		group, version = apiVersion[:i], apiVersion[i+1:]
	}
	parts := strings.Split(p, "/")
	switch len(parts) {
	case 1:
		return wildcard(parts[0], kind)
	case 2:
		if parts[0] != "" && parts[0][0] >= 'A' && parts[0][0] <= 'Z' {
			return false // a subresource
		}
		return wildcard(parts[0], version) && wildcard(parts[1], kind)
	case 3:
		return wildcard(parts[0], group) && wildcard(parts[1], version) && wildcard(parts[2], kind)
	}
	return false
}

// wildcard matches s with a pattern of * (any run) and ? (one character).
func wildcard(p, s string) bool {
	if p == "" {
		return s == ""
	}
	switch p[0] {
	case '*':
		for i := 0; i <= len(s); i++ {
			if wildcard(p[1:], s[i:]) {
				return true
			}
		}
		return false
	case '?':
		return s != "" && wildcard(p[1:], s[1:])
	}
	return s != "" && s[0] == p[0] && wildcard(p[1:], s[1:])
}

// denies holds when every condition of all holds and, when any is not empty,
// one of any holds. A condition the guard cannot evaluate holds.
func denies(r rule, o obj) bool {
	for _, c := range r.all {
		if !holds(c, o) {
			return false
		}
	}
	if len(r.any) == 0 {
		return true
	}
	for _, c := range r.any {
		if holds(c, o) {
			return true
		}
	}
	return false
}

func holds(x any, o obj) bool {
	c, _ := x.(obj)
	key, ok := evalKey(c["key"], o)
	if !ok {
		return true
	}
	value := c["value"]
	switch str(c["operator"]) {
	case "Equals":
		eq, known := equals(key, value)
		return !known || eq
	case "NotEquals":
		eq, known := equals(key, value)
		return !known || !eq
	case "AnyIn", "AllIn", "In", "AnyNotIn", "AllNotIn", "NotIn":
		return setOp(str(c["operator"]), key, value)
	}
	return true
}

// equals compares a key and a value as Kyverno's Equals does for strings: the
// value is a wildcard. known is false for other types.
func equals(key, value any) (eq, known bool) {
	k, kok := key.(string)
	v, vok := value.(string)
	if !kok || !vok {
		return false, false
	}
	return wildcard(v, k), true
}

func setOp(op string, key, value any) bool {
	var keys []string
	switch k := key.(type) {
	case string:
		keys = []string{k}
	case []any:
		for _, x := range k {
			s, ok := x.(string)
			if !ok {
				return true
			}
			keys = append(keys, s)
		}
	default:
		return true
	}
	var values []string
	switch v := value.(type) {
	case string:
		values = []string{v}
	case []any:
		for _, x := range v {
			s, ok := x.(string)
			if !ok {
				return true
			}
			values = append(values, s)
		}
	default:
		return true
	}
	in := func(k string) bool {
		for _, v := range values {
			if wildcard(v, k) {
				return true
			}
		}
		return false
	}
	anyIn, allIn := false, true
	for _, k := range keys {
		if in(k) {
			anyIn = true
		} else {
			allIn = false
		}
	}
	switch op {
	case "AnyIn":
		return anyIn
	case "AllIn", "In":
		return allIn
	case "AnyNotIn":
		return !allIn
	default: // AllNotIn, NotIn
		return !anyIn
	}
}

// alt is one alternative of a key joined by ||: a path or a string literal.
type alt struct {
	path    []string
	literal bool
	text    string
}

var (
	ident   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*`)
	quoted  = regexp.MustCompile(`^"((?:[^"\\]|\\.)*)"`)
	rawText = regexp.MustCompile(`^'((?:[^'\\]|\\.)*)'$`)
)

// parseKey reads `{{ a.b."c" || 'x' }}`. ok is false for any other form.
func parseKey(key string) ([]alt, bool) {
	k := strings.TrimSpace(key)
	if !strings.HasPrefix(k, "{{") || !strings.HasSuffix(k, "}}") {
		return nil, false
	}
	k = strings.TrimSpace(k[2 : len(k)-2])
	var out []alt
	for _, part := range splitOr(k) {
		part = strings.TrimSpace(part)
		if m := rawText.FindStringSubmatch(part); m != nil {
			out = append(out, alt{literal: true, text: m[1]})
			continue
		}
		var path []string
		rest := part
		for {
			if m := ident.FindString(rest); m != "" {
				path = append(path, m)
				rest = rest[len(m):]
			} else if m := quoted.FindStringSubmatch(rest); m != nil {
				path = append(path, m[1])
				rest = rest[len(m[0]):]
			} else {
				return nil, false
			}
			if rest == "" {
				break
			}
			if rest[0] != '.' {
				return nil, false
			}
			rest = rest[1:]
		}
		out = append(out, alt{path: path})
	}
	return out, len(out) > 0
}

// splitOr splits at || outside quotes.
func splitOr(s string) []string {
	var out []string
	var quote byte
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0 && c == '\\':
			i++
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '|' && i+1 < len(s) && s[i+1] == '|':
			out = append(out, s[start:i])
			start = i + 2
			i++
		}
	}
	return append(out, s[start:])
}

// evalKey evaluates a key on the object: the first alternative that is not
// empty (JMESPath ||). A key that is not a template is its own value.
func evalKey(key any, o obj) (any, bool) {
	s, isString := key.(string)
	if !isString {
		return key, true
	}
	if !strings.Contains(s, "{{") {
		return s, true
	}
	alts, ok := parseKey(s)
	if !ok {
		return nil, false
	}
	request := obj{"object": o, "operation": "CREATE"}
	var last any
	for _, a := range alts {
		if a.literal {
			last = a.text
		} else {
			var cur any = obj{"request": request}
			for _, p := range a.path {
				m, isMap := cur.(obj)
				if !isMap {
					cur = nil
					break
				}
				cur = m[p]
			}
			last = cur
		}
		if !empty(last) {
			return last, true
		}
	}
	if last == nil {
		return "", true
	}
	return last, true
}

// empty is JMESPath's false: null, "", false, an empty list or map.
func empty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case bool:
		return !x
	case []any:
		return len(x) == 0
	case obj:
		return len(x) == 0
	}
	return false
}

// namespaceLabels gives the labels of each Namespace object of the render.
func namespaceLabels(releases []release) map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, r := range releases {
		for _, o := range r.objects {
			if str(o["kind"]) != "Namespace" {
				continue
			}
			out[str(dig(o, "metadata", "name"))] = stringMap(dig(o, "metadata", "labels"))
		}
	}
	return out
}

// labelsOfNamespace adds the label the API server sets on every Namespace.
func labelsOfNamespace(known map[string]map[string]string, ns string) map[string]string {
	out := map[string]string{}
	for k, v := range known[ns] {
		out[k] = v
	}
	out["kubernetes.io/metadata.name"] = ns
	return out
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func dig(v any, path ...string) any {
	for _, p := range path {
		m, ok := v.(obj)
		if !ok {
			return nil
		}
		v = m[p]
	}
	return v
}

// stringMap copies a map of strings; a value that is not a string is kept as
// its JSON text.
func stringMap(v any) map[string]string {
	out := map[string]string{}
	m, _ := v.(obj)
	for k, x := range m {
		if s, ok := x.(string); ok {
			out[k] = s
		} else {
			b, _ := json.Marshal(x)
			out[k] = string(b)
		}
	}
	return out
}

func toAny(m map[string]string) obj {
	out := obj{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

// anyString holds when v is a list with a string for which f holds.
func anyString(v any, f func(string) bool) bool {
	list, _ := v.([]any)
	for _, x := range list {
		if s, ok := x.(string); ok && f(s) {
			return true
		}
	}
	return false
}

// vpolicy is a ValidatingPolicy of policies.kyverno.io as the guard reads it.
type vpolicy struct {
	match       obj // spec.matchConstraints
	conditions  []namedExpr
	variables   map[string]*parsed
	validations []*parsed
}

type namedExpr struct {
	name string
	expr *parsed
}

// parsed is one CEL expression: its tree, or why the guard cannot read it.
type parsed struct {
	root node
	err  error
}

func parse(text string) *parsed {
	root, err := parseCEL(text)
	return &parsed{root: root, err: err}
}

func readValidatingPolicy(policy obj) (rule, error) {
	spec, _ := policy["spec"].(obj)
	vp := &vpolicy{variables: map[string]*parsed{}}
	vp.match, _ = spec["matchConstraints"].(obj)
	if vp.match == nil {
		return rule{}, fmt.Errorf("it has no spec.matchConstraints")
	}
	conditions, _ := spec["matchConditions"].([]any)
	for _, x := range conditions {
		c, _ := x.(obj)
		vp.conditions = append(vp.conditions, namedExpr{name: str(c["name"]), expr: parse(str(c["expression"]))})
	}
	variables, _ := spec["variables"].([]any)
	for _, x := range variables {
		v, _ := x.(obj)
		vp.variables[str(v["name"])] = parse(str(v["expression"]))
	}
	validations, _ := spec["validations"].([]any)
	if len(validations) == 0 {
		return rule{}, fmt.Errorf("it has no spec.validations")
	}
	for _, x := range validations {
		v, _ := x.(obj)
		vp.validations = append(vp.validations, parse(str(v["expression"])))
	}
	out := rule{raw: spec, vp: vp}
	seen := map[string]bool{}
	for _, v := range vp.validations {
		if v.err != nil {
			continue
		}
		for _, l := range vp.namedLabels(v.root) {
			if !seen[l] {
				seen[l] = true
				out.labels = append(out.labels, l)
			}
		}
	}
	if len(out.labels) == 0 {
		return rule{}, fmt.Errorf("its validations name no label of object.metadata.labels the guard can read")
	}
	return out, nil
}

// judge admits or denies one object on CREATE, and says why.
func (vp *vpolicy) judge(o obj, nsLabels map[string]string) (bool, string) {
	if !vp.selects(o, nsLabels) {
		return true, "the matchConstraints of the policy do not select it"
	}
	e := newEnv(o, nsLabels, vp.variables)
	for _, c := range vp.conditions {
		v, err := e.run(c.expr)
		if b, isBool := v.(bool); err == nil && isBool && !b {
			return true, "the match condition " + c.name + " of the policy is false"
		}
	}
	for i, v := range vp.validations {
		got, err := e.run(v)
		if err != nil {
			return false, fmt.Sprintf("validation %d cannot be read by the guard: %v", i+1, err)
		}
		b, isBool := got.(bool)
		if !isBool {
			return false, fmt.Sprintf("validation %d gives no bool", i+1)
		}
		if !b {
			return false, ""
		}
	}
	return true, "every validation of the policy holds"
}

// selects reads the matchConstraints. A field the guard does not read selects.
func (vp *vpolicy) selects(o obj, nsLabels map[string]string) bool {
	for k, v := range vp.match {
		switch k {
		case "resourceRules":
			if !anyRule(v, o, true) {
				return false
			}
		case "excludeResourceRules":
			if anyRule(v, o, false) {
				return false
			}
		case "namespaceSelector":
			if m, known := labelSelector(v, nsLabels); known && !m {
				return false
			}
		case "objectSelector":
			if m, known := labelSelector(v, stringMap(dig(o, "metadata", "labels"))); known && !m {
				return false
			}
		}
	}
	return true
}

// anyRule tells whether a rule of a list of resource rules selects the object
// on CREATE. A rule the guard cannot read selects for a match (forMatch) and
// not for an exclusion.
func anyRule(v any, o obj, forMatch bool) bool {
	list, isList := v.([]any)
	if !isList {
		return forMatch
	}
	for _, x := range list {
		r, _ := x.(obj)
		hit, known := ruleSelects(r, o)
		if !known {
			hit = forMatch
		}
		if hit {
			return true
		}
	}
	return false
}

// ruleSelects reads one resource rule. The resource of a kind is its lower
// case plural with s, which holds for the two kinds the guard builds (Job and
// Pod).
func ruleSelects(r obj, o obj) (hit, known bool) {
	if r == nil {
		return false, false
	}
	group, version := splitAPIVersion(str(o["apiVersion"]))
	for k, v := range r {
		var want string
		switch k {
		case "apiGroups":
			want = group
		case "apiVersions":
			want = version
		case "operations":
			want = "CREATE"
		case "resources":
			want = strings.ToLower(str(o["kind"])) + "s"
		case "resourceNames":
			if list, isList := v.([]any); isList && len(list) == 0 {
				continue
			}
			want = str(dig(o, "metadata", "name"))
		case "scope":
			switch str(v) {
			case "", "*", "Namespaced":
				continue
			case "Cluster":
				return false, true
			}
			return false, false
		default:
			return false, false
		}
		list, isList := v.([]any)
		if !isList {
			return false, false
		}
		in := false
		for _, x := range list {
			s, isString := x.(string)
			if !isString {
				return false, false
			}
			if s == want || s == "*" || (k == "resources" && s == "*/*") {
				in = true
			}
		}
		if !in {
			return false, true
		}
	}
	return true, true
}

func splitAPIVersion(apiVersion string) (group, version string) {
	if i := strings.Index(apiVersion, "/"); i >= 0 {
		return apiVersion[:i], apiVersion[i+1:]
	}
	return "", apiVersion
}

// namedLabels gives the labels a validation names: each alternative of its
// top-level || that compares one label of object.metadata.labels with a
// string literal.
func (vp *vpolicy) namedLabels(n node) []string {
	var out []string
	for _, alt := range disjuncts(n) {
		b, isBinary := alt.(binaryNode)
		if !isBinary {
			continue
		}
		if b.op == "in" {
			if k, isLit := b.l.(litNode); isLit {
				if s, isString := k.v.(string); isString && vp.isLabels(b.r, 0) {
					out = append(out, s+" (any value)")
				}
			}
			continue
		}
		key, ok := vp.labelOf(b.l)
		lit, isLit := b.r.(litNode)
		if !ok || !isLit {
			continue
		}
		s, isString := lit.v.(string)
		switch {
		case !isString:
		case b.op == "==" && s != "":
			out = append(out, key+": "+s)
		case b.op == "!=" && s == "":
			out = append(out, key+" (any value)")
		}
	}
	return out
}

func disjuncts(n node) []node {
	if b, isBinary := n.(binaryNode); isBinary && b.op == "||" {
		return append(disjuncts(b.l), disjuncts(b.r)...)
	}
	return []node{n}
}

// labelOf reads L[?'k'].orValue of the empty string, or L['k'], L the labels
// of the object.
func (vp *vpolicy) labelOf(n node) (string, bool) {
	var ix indexNode
	switch x := n.(type) {
	case callNode:
		lit, isLit := oneArg(x).(litNode)
		inner, isIndex := x.target.(indexNode)
		if x.fn != "orValue" || !isLit || lit.v != "" || !isIndex || !inner.opt {
			return "", false
		}
		ix = inner
	case indexNode:
		if x.opt {
			return "", false
		}
		ix = x
	default:
		return "", false
	}
	k, isLit := ix.i.(litNode)
	s, isString := k.v.(string)
	if !isLit || !isString || !vp.isLabels(ix.x, 0) {
		return "", false
	}
	return s, true
}

func oneArg(c callNode) node {
	if len(c.args) != 1 {
		return nil
	}
	return c.args[0]
}

// isLabels tells whether n is object.metadata.labels, the same with .? and
// orValue({}), or a variable defined so.
func (vp *vpolicy) isLabels(n node, depth int) bool {
	if depth > len(vp.variables) {
		return false
	}
	switch x := n.(type) {
	case selectNode:
		if id, isIdent := x.x.(identNode); isIdent && id.name == "variables" {
			def := vp.variables[x.field]
			return !x.opt && def != nil && def.err == nil && vp.isLabels(def.root, depth+1)
		}
		m, isSelect := x.x.(selectNode)
		if x.field != "labels" || !isSelect || m.field != "metadata" {
			return false
		}
		id, isIdent := m.x.(identNode)
		return isIdent && id.name == "object"
	case callNode:
		m, isMap := oneArg(x).(mapNode)
		s, isSelect := x.target.(selectNode)
		return x.fn == "orValue" && isMap && len(m.keys) == 0 && isSelect && s.opt && vp.isLabels(s, depth)
	}
	return false
}

// The CEL subset: values are nil, bool, int64, float64 (numbers of the
// render), string, []any, obj, optVal and varScope.

type optVal struct {
	ok bool
	v  any
}

// varScope is the value of the identifier variables.
type varScope struct{ e *env }

type env struct {
	vars    map[string]any
	defs    map[string]*parsed
	values  map[string]any
	errs    map[string]error
	running map[string]bool
}

func newEnv(o obj, nsLabels map[string]string, defs map[string]*parsed) *env {
	ns := str(dig(o, "metadata", "namespace"))
	group, version := splitAPIVersion(str(o["apiVersion"]))
	e := &env{defs: defs, values: map[string]any{}, errs: map[string]error{}, running: map[string]bool{}}
	var nsObject any
	if ns != "" {
		nsObject = obj{"apiVersion": "v1", "kind": "Namespace", "metadata": obj{"name": ns, "labels": toAny(nsLabels)}}
	}
	e.vars = map[string]any{
		"object": o, "oldObject": nil, "namespaceObject": nsObject,
		"request": obj{"operation": "CREATE", "namespace": ns, "name": str(dig(o, "metadata", "name")),
			"kind": obj{"group": group, "version": version, "kind": str(o["kind"])}},
		"variables": varScope{e: e},
	}
	return e
}

func (e *env) run(p *parsed) (any, error) {
	if p.err != nil {
		return nil, p.err
	}
	return e.eval(p.root, nil)
}

func (e *env) variable(name string) (any, error) {
	if v, done := e.values[name]; done {
		return v, nil
	}
	if err, done := e.errs[name]; done {
		return nil, err
	}
	def := e.defs[name]
	if def == nil {
		return nil, fmt.Errorf("no variable %s", name)
	}
	if e.running[name] {
		return nil, fmt.Errorf("variable %s refers to itself", name)
	}
	e.running[name] = true
	v, err := e.run(def)
	delete(e.running, name)
	if err != nil {
		e.errs[name] = err
		return nil, err
	}
	e.values[name] = v
	return v, nil
}

type node any

type (
	litNode    struct{ v any }
	identNode  struct{ name string }
	selectNode struct {
		x     node
		field string
		opt   bool
	}
	indexNode struct {
		x, i node
		opt  bool
	}
	hasNode  struct{ s selectNode }
	callNode struct {
		target node // nil for a global function
		fn     string
		args   []node
	}
	listNode  struct{ elems []node }
	mapNode   struct{ keys, vals []node }
	unaryNode struct {
		op string
		x  node
	}
	binaryNode struct {
		op   string
		l, r node
	}
	condNode struct{ c, t, f node }
	compNode struct {
		macro  string
		target node
		v      string
		body   node
	}
)

type token struct {
	kind byte // 'i' identifier, 's' string, 'n' number, 'p' punctuation, 0 the end
	text string
	val  any
	pos  int
}

func isIdentStart(c byte) bool { return c == '_' || (c|0x20 >= 'a' && c|0x20 <= 'z') }
func isIdentPart(c byte) bool  { return isIdentStart(c) || (c >= '0' && c <= '9') }

func lex(s string) ([]token, error) {
	var out []token
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case isIdentStart(c):
			j := i + 1
			for j < len(s) && isIdentPart(s[j]) {
				j++
			}
			word := s[i:j]
			if j < len(s) && (s[j] == '\'' || s[j] == '"') {
				if word != "r" && word != "R" {
					return nil, fmt.Errorf("the literal prefix %s at %d is not read by the guard", word, i)
				}
				text, n, err := lexString(s[j:], true)
				if err != nil {
					return nil, fmt.Errorf("%v at %d", err, i)
				}
				out = append(out, token{kind: 's', text: s[i : j+n], val: text, pos: i})
				i = j + n
				continue
			}
			out = append(out, token{kind: 'i', text: word, pos: i})
			i = j
		case c >= '0' && c <= '9':
			// A decimal int or a decimal with a fraction; no exponent, hex or unsigned.
			j := i
			for j < len(s) && (s[j] >= '0' && s[j] <= '9' || (s[j] == '.' && j+1 < len(s) && s[j+1] >= '0' && s[j+1] <= '9')) {
				j++
			}
			text := s[i:j]
			var val any
			var err error
			if strings.Contains(text, ".") {
				val, err = strconv.ParseFloat(text, 64)
			} else {
				val, err = strconv.ParseInt(text, 10, 64)
			}
			if err != nil || (j < len(s) && isIdentPart(s[j])) {
				return nil, fmt.Errorf("the number %s at %d is not read by the guard", text, i)
			}
			out = append(out, token{kind: 'n', text: text, val: val, pos: i})
			i = j
		case c == '\'' || c == '"':
			text, n, err := lexString(s[i:], false)
			if err != nil {
				return nil, fmt.Errorf("%v at %d", err, i)
			}
			out = append(out, token{kind: 's', text: s[i : i+n], val: text, pos: i})
			i += n
		default:
			two := ""
			if i+1 < len(s) {
				two = s[i : i+2]
			}
			switch {
			case two == "==" || two == "!=" || two == "<=" || two == ">=" || two == "&&" || two == "||":
				out = append(out, token{kind: 'p', text: two, pos: i})
				i += 2
			case strings.IndexByte("!<>+-?:.,()[]{}", c) >= 0:
				out = append(out, token{kind: 'p', text: string(c), pos: i})
				i++
			default:
				return nil, fmt.Errorf("the character %q at %d is not read by the guard", c, i)
			}
		}
	}
	return append(out, token{pos: len(s)}), nil
}

// lexString reads a quoted string at the start of s; it gives the text and
// the length read.
func lexString(s string, raw bool) (string, int, error) {
	q := s[0]
	if strings.HasPrefix(s, string([]byte{q, q, q})) {
		return "", 0, fmt.Errorf("a triple-quoted string is not read by the guard")
	}
	var b strings.Builder
	for j := 1; j < len(s); j++ {
		c := s[j]
		switch {
		case c == q:
			return b.String(), j + 1, nil
		case c == '\n':
			return "", 0, fmt.Errorf("a line break in a string")
		case c == '\\' && !raw:
			j++
			if j == len(s) {
				return "", 0, fmt.Errorf("an unterminated string")
			}
			switch s[j] {
			case '\\', '\'', '"', '`', '?':
				b.WriteByte(s[j])
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			default:
				return "", 0, fmt.Errorf("the escape \\%c is not read by the guard", s[j])
			}
		default:
			b.WriteByte(c)
		}
	}
	return "", 0, fmt.Errorf("an unterminated string")
}

type parser struct {
	toks []token
	i    int
}

func parseCEL(s string) (node, error) {
	toks, err := lex(s)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	n, err := p.expr()
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.kind != 0 {
		return nil, fmt.Errorf("unexpected %q at %d", t.text, t.pos)
	}
	return n, nil
}

func (p *parser) peek() token { return p.toks[p.i] }

func (p *parser) is(text string) bool {
	t := p.peek()
	return t.kind == 'p' && t.text == text
}

func (p *parser) accept(text string) bool {
	if p.is(text) {
		p.i++
		return true
	}
	return false
}

func (p *parser) expect(text string) error {
	if !p.accept(text) {
		t := p.peek()
		return fmt.Errorf("want %q at %d, found %q", text, t.pos, t.text)
	}
	return nil
}

func (p *parser) expr() (node, error) {
	c, err := p.binary(0)
	if err != nil || !p.accept("?") {
		return c, err
	}
	t, err := p.binary(0)
	if err != nil {
		return nil, err
	}
	if err := p.expect(":"); err != nil {
		return nil, err
	}
	f, err := p.expr()
	if err != nil {
		return nil, err
	}
	return condNode{c: c, t: t, f: f}, nil
}

var levels = [][]string{{"||"}, {"&&"}, {"==", "!=", "<", "<=", ">", ">=", "in"}, {"+"}}

func (p *parser) binary(level int) (node, error) {
	if level == len(levels) {
		return p.unary()
	}
	l, err := p.binary(level + 1)
	if err != nil {
		return nil, err
	}
	for {
		op := ""
		t := p.peek()
		for _, o := range levels[level] {
			if t.text == o && (t.kind == 'p' || (t.kind == 'i' && o == "in")) {
				op = o
			}
		}
		if op == "" {
			return l, nil
		}
		p.i++
		r, err := p.binary(level + 1)
		if err != nil {
			return nil, err
		}
		l = binaryNode{op: op, l: l, r: r}
	}
}

func (p *parser) unary() (node, error) {
	for _, op := range []string{"!", "-"} {
		if p.accept(op) {
			x, err := p.unary()
			if err != nil {
				return nil, err
			}
			return unaryNode{op: op, x: x}, nil
		}
	}
	return p.member()
}

func (p *parser) member() (node, error) {
	n, err := p.primary()
	if err != nil {
		return nil, err
	}
	for {
		switch {
		case p.accept("."):
			opt := p.accept("?")
			t := p.peek()
			if t.kind != 'i' {
				return nil, fmt.Errorf("want a field name at %d", t.pos)
			}
			p.i++
			if opt || !p.is("(") {
				n = selectNode{x: n, field: t.text, opt: opt}
				continue
			}
			args, err := p.args()
			if err != nil {
				return nil, err
			}
			if n, err = method(n, t.text, args); err != nil {
				return nil, err
			}
		case p.accept("["):
			opt := p.accept("?")
			i, err := p.expr()
			if err != nil {
				return nil, err
			}
			if err := p.expect("]"); err != nil {
				return nil, err
			}
			n = indexNode{x: n, i: i, opt: opt}
		default:
			return n, nil
		}
	}
}

// method builds a call on a target, or the macro of that name.
func method(target node, fn string, args []node) (node, error) {
	switch fn {
	case "all", "exists", "map", "filter":
		v, isIdent := firstArg(args).(identNode)
		if !isIdent || len(args) != 2 {
			return nil, fmt.Errorf("the macro %s is read by the guard with a variable name and one expression only", fn)
		}
		return compNode{macro: fn, target: target, v: v.name, body: args[1]}, nil
	}
	return callNode{target: target, fn: fn, args: args}, nil
}

func firstArg(args []node) node {
	if len(args) == 0 {
		return nil
	}
	return args[0]
}

func (p *parser) args() ([]node, error) {
	return p.list("(", ")")
}

// list reads open, expressions separated by commas (a trailing comma
// allowed), close.
func (p *parser) list(open, close string) ([]node, error) {
	if err := p.expect(open); err != nil {
		return nil, err
	}
	var out []node
	for !p.accept(close) {
		n, err := p.expr()
		if err != nil {
			return nil, err
		}
		out = append(out, n)
		if !p.accept(",") {
			if err := p.expect(close); err != nil {
				return nil, err
			}
			break
		}
	}
	return out, nil
}

func (p *parser) primary() (node, error) {
	t := p.peek()
	switch {
	case t.kind == 's' || t.kind == 'n':
		p.i++
		return litNode{v: t.val}, nil
	case t.kind == 'i':
		p.i++
		switch t.text {
		case "true":
			return litNode{v: true}, nil
		case "false":
			return litNode{v: false}, nil
		case "null":
			return litNode{v: nil}, nil
		}
		if !p.is("(") {
			return identNode{name: t.text}, nil
		}
		args, err := p.args()
		if err != nil {
			return nil, err
		}
		if t.text != "has" {
			return callNode{fn: t.text, args: args}, nil
		}
		s, isSelect := firstArg(args).(selectNode)
		if len(args) != 1 || !isSelect || s.opt {
			return nil, fmt.Errorf("has() needs one field selection")
		}
		return hasNode{s: s}, nil
	case p.is("("):
		p.i++
		n, err := p.expr()
		if err != nil {
			return nil, err
		}
		return n, p.expect(")")
	case p.is("["):
		elems, err := p.list("[", "]")
		return listNode{elems: elems}, err
	case p.accept("{"):
		var m mapNode
		for !p.accept("}") {
			k, err := p.expr()
			if err != nil {
				return nil, err
			}
			if err := p.expect(":"); err != nil {
				return nil, err
			}
			v, err := p.expr()
			if err != nil {
				return nil, err
			}
			m.keys, m.vals = append(m.keys, k), append(m.vals, v)
			if !p.accept(",") {
				if err := p.expect("}"); err != nil {
					return nil, err
				}
				break
			}
		}
		return m, nil
	}
	return nil, fmt.Errorf("unexpected %q at %d", t.text, t.pos)
}

func (e *env) eval(n node, scope map[string]any) (any, error) {
	switch n := n.(type) {
	case litNode:
		return n.v, nil
	case identNode:
		if v, bound := scope[n.name]; bound {
			return v, nil
		}
		if v, known := e.vars[n.name]; known {
			return v, nil
		}
		return nil, fmt.Errorf("the identifier %s is not known to the guard", n.name)
	case selectNode:
		x, err := e.eval(n.x, scope)
		if err != nil {
			return nil, err
		}
		return selectField(x, n.field, n.opt)
	case indexNode:
		x, err := e.eval(n.x, scope)
		if err != nil {
			return nil, err
		}
		i, err := e.eval(n.i, scope)
		if err != nil {
			return nil, err
		}
		return indexValue(x, i, n.opt)
	case hasNode:
		x, err := e.eval(n.s.x, scope)
		if err != nil {
			return nil, err
		}
		m, isMap := x.(obj)
		if !isMap {
			return nil, fmt.Errorf("has() of a field of a value that is not a map")
		}
		_, has := m[n.s.field]
		return has, nil
	case listNode:
		out := []any{}
		for _, x := range n.elems {
			v, err := e.eval(x, scope)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case mapNode:
		out := obj{}
		for i := range n.keys {
			k, err := e.eval(n.keys[i], scope)
			if err != nil {
				return nil, err
			}
			ks, isString := k.(string)
			if !isString {
				return nil, fmt.Errorf("a map key that is not a string is not read by the guard")
			}
			if out[ks], err = e.eval(n.vals[i], scope); err != nil {
				return nil, err
			}
		}
		return out, nil
	case unaryNode:
		x, err := e.eval(n.x, scope)
		if err != nil {
			return nil, err
		}
		if b, isBool := x.(bool); isBool && n.op == "!" {
			return !b, nil
		}
		if i, isInt := x.(int64); isInt && n.op == "-" {
			return -i, nil
		}
		if f, isFloat := x.(float64); isFloat && n.op == "-" {
			return -f, nil
		}
		return nil, fmt.Errorf("%s of %T", n.op, x)
	case binaryNode:
		if n.op == "&&" || n.op == "||" {
			return e.logic(n, scope)
		}
		l, err := e.eval(n.l, scope)
		if err != nil {
			return nil, err
		}
		r, err := e.eval(n.r, scope)
		if err != nil {
			return nil, err
		}
		return binaryOp(n.op, l, r)
	case condNode:
		c, err := e.eval(n.c, scope)
		if err != nil {
			return nil, err
		}
		b, isBool := c.(bool)
		if !isBool {
			return nil, fmt.Errorf("the condition of ?: is not a bool")
		}
		if b {
			return e.eval(n.t, scope)
		}
		return e.eval(n.f, scope)
	case callNode:
		return e.call(n, scope)
	case compNode:
		return e.comprehension(n, scope)
	}
	return nil, fmt.Errorf("an expression the guard does not read")
}

// logic is && and || as CEL has them: the deciding value wins over an error
// on the other side.
func (e *env) logic(n binaryNode, scope map[string]any) (any, error) {
	decides := n.op == "||"
	l, lerr := e.eval(n.l, scope)
	if b, isBool := l.(bool); lerr == nil && isBool && b == decides {
		return decides, nil
	}
	r, rerr := e.eval(n.r, scope)
	if b, isBool := r.(bool); rerr == nil && isBool && b == decides {
		return decides, nil
	}
	if lerr != nil {
		return nil, lerr
	}
	if rerr != nil {
		return nil, rerr
	}
	_, lb := l.(bool)
	_, rb := r.(bool)
	if !lb || !rb {
		return nil, fmt.Errorf("%s of a value that is not a bool", n.op)
	}
	return !decides, nil
}

func selectField(x any, field string, opt bool) (any, error) {
	switch v := x.(type) {
	case optVal:
		if !v.ok {
			return optVal{}, nil
		}
		got, err := selectField(v.v, field, opt)
		if o, isOpt := got.(optVal); err != nil || isOpt {
			return o, err
		}
		return optVal{ok: true, v: got}, nil
	case obj:
		got, has := v[field]
		if opt {
			return optVal{ok: has, v: got}, nil
		}
		if !has {
			return nil, fmt.Errorf("no such key: %s", field)
		}
		return got, nil
	case varScope:
		if opt {
			return nil, fmt.Errorf("variables.?%s is not read by the guard", field)
		}
		return v.e.variable(field)
	}
	return nil, fmt.Errorf("the field %s of a value of type %T", field, x)
}

func indexValue(x, i any, opt bool) (any, error) {
	switch v := x.(type) {
	case optVal:
		if !v.ok {
			return optVal{}, nil
		}
		got, err := indexValue(v.v, i, opt)
		if o, isOpt := got.(optVal); err != nil || isOpt {
			return o, err
		}
		return optVal{ok: true, v: got}, nil
	case obj:
		k, isString := i.(string)
		if !isString {
			return nil, fmt.Errorf("a map index that is not a string")
		}
		return selectField(v, k, opt)
	case []any:
		n, isInt := intOf(i)
		in := isInt && n >= 0 && n < int64(len(v))
		switch {
		case in && opt:
			return optVal{ok: true, v: v[n]}, nil
		case in:
			return v[n], nil
		case isInt && opt:
			return optVal{}, nil
		}
		return nil, fmt.Errorf("the index %v of a list of %d", i, len(v))
	}
	return nil, fmt.Errorf("an index of a value of type %T", x)
}

func (e *env) call(n callNode, scope map[string]any) (any, error) {
	var target any
	if n.target != nil {
		v, err := e.eval(n.target, scope)
		if err != nil {
			return nil, err
		}
		target = v
	}
	var args []any
	for _, a := range n.args {
		v, err := e.eval(a, scope)
		if err != nil {
			return nil, err
		}
		args = append(args, v)
	}
	if n.target == nil {
		if n.fn == "size" && len(args) == 1 {
			return sizeOf(args[0])
		}
		return nil, fmt.Errorf("the function %s is not read by the guard", n.fn)
	}
	if o, isOpt := target.(optVal); isOpt {
		switch {
		case n.fn == "orValue" && len(args) == 1:
			if o.ok {
				return o.v, nil
			}
			return args[0], nil
		case n.fn == "hasValue" && len(args) == 0:
			return o.ok, nil
		case n.fn == "value" && len(args) == 0 && o.ok:
			return o.v, nil
		}
		return nil, fmt.Errorf("%s of an optional value", n.fn)
	}
	if n.fn == "size" && len(args) == 0 {
		return sizeOf(target)
	}
	s, isString := target.(string)
	if !isString {
		return nil, fmt.Errorf("the method %s of a value of type %T is not read by the guard", n.fn, target)
	}
	var a string
	if len(args) == 1 {
		a, isString = args[0].(string)
	}
	switch {
	case !isString || len(args) != 1:
	case n.fn == "startsWith":
		return strings.HasPrefix(s, a), nil
	case n.fn == "endsWith":
		return strings.HasSuffix(s, a), nil
	case n.fn == "contains":
		return strings.Contains(s, a), nil
	case n.fn == "matches":
		re, err := regexp.Compile(a)
		if err != nil {
			return nil, err
		}
		return re.MatchString(s), nil
	}
	return nil, fmt.Errorf("the method %s is not read by the guard", n.fn)
}

func (e *env) comprehension(n compNode, scope map[string]any) (any, error) {
	x, err := e.eval(n.target, scope)
	if err != nil {
		return nil, err
	}
	var items []any
	switch v := x.(type) {
	case []any:
		items = v
	case obj:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			items = append(items, k)
		}
	default:
		return nil, fmt.Errorf("%s over a value that is not a list or a map", n.macro)
	}
	inner := map[string]any{}
	for k, v := range scope {
		inner[k] = v
	}
	var firstErr error
	out := []any{}
	for _, it := range items {
		inner[n.v] = it
		switch n.macro {
		case "map":
			v, err := e.eval(n.body, inner)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		case "filter":
			keep, err := boolOf(e.eval(n.body, inner))
			if err != nil {
				return nil, err
			}
			if keep {
				out = append(out, it)
			}
		default:
			b, err := boolOf(e.eval(n.body, inner))
			switch {
			case err != nil:
				if firstErr == nil {
					firstErr = err
				}
			case n.macro == "all" && !b:
				return false, nil
			case n.macro == "exists" && b:
				return true, nil
			}
		}
	}
	switch {
	case n.macro == "map" || n.macro == "filter":
		return out, nil
	case firstErr != nil:
		return nil, firstErr
	}
	return n.macro == "all", nil
}

func boolOf(v any, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	b, isBool := v.(bool)
	if !isBool {
		return false, fmt.Errorf("a predicate that is not a bool")
	}
	return b, nil
}

func sizeOf(v any) (any, error) {
	switch x := v.(type) {
	case string:
		return int64(utf8.RuneCountInString(x)), nil
	case []any:
		return int64(len(x)), nil
	case obj:
		return int64(len(x)), nil
	}
	return nil, fmt.Errorf("size of a value of type %T", v)
}

// num gives a number as a float64.
func num(v any) (float64, bool) {
	switch x := v.(type) {
	case int64:
		return float64(x), true
	case float64:
		return x, true
	}
	return 0, false
}

// intOf gives an int64, or a whole float64 of the render, as an int64.
func intOf(v any) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case float64:
		if x == float64(int64(x)) {
			return int64(x), true
		}
	}
	return 0, false
}

func binaryOp(op string, l, r any) (any, error) {
	switch op {
	case "==":
		return celEqual(l, r), nil
	case "!=":
		return !celEqual(l, r), nil
	case "in":
		switch c := r.(type) {
		case []any:
			for _, x := range c {
				if celEqual(l, x) {
					return true, nil
				}
			}
			return false, nil
		case obj:
			k, isString := l.(string)
			if !isString {
				return nil, fmt.Errorf("in of a map with a key that is not a string")
			}
			_, has := c[k]
			return has, nil
		}
		return nil, fmt.Errorf("in of a value of type %T", r)
	case "<", "<=", ">", ">=":
		a, aNum := num(l)
		b, bNum := num(r)
		as, aStr := l.(string)
		bs, bStr := r.(string)
		less, more := a < b, a > b
		switch {
		case aStr && bStr:
			less, more = as < bs, as > bs
		case !aNum || !bNum:
			return nil, fmt.Errorf("%s of %T and %T", op, l, r)
		}
		return map[string]bool{"<": less, "<=": !more, ">": more, ">=": !less}[op], nil
	case "+":
		as, aStr := l.(string)
		bs, bStr := r.(string)
		al, aList := l.([]any)
		bl, bList := r.([]any)
		ai, aInt := l.(int64)
		bi, bInt := r.(int64)
		a, aNum := num(l)
		b, bNum := num(r)
		switch {
		case aStr && bStr:
			return as + bs, nil
		case aList && bList:
			return append(append([]any{}, al...), bl...), nil
		case aInt && bInt:
			return ai + bi, nil
		case aNum && bNum:
			return a + b, nil
		}
	}
	return nil, fmt.Errorf("%s of %T and %T", op, l, r)
}

// celEqual is CEL's equality: numbers by value, other values of different
// types are not equal.
func celEqual(a, b any) bool {
	if x, isNum := num(a); isNum {
		y, both := num(b)
		return both && x == y
	}
	switch x := a.(type) {
	case nil:
		return b == nil
	case bool:
		y, same := b.(bool)
		return same && x == y
	case string:
		y, same := b.(string)
		return same && x == y
	case []any:
		y, same := b.([]any)
		if !same || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !celEqual(x[i], y[i]) {
				return false
			}
		}
		return true
	case obj:
		y, same := b.(obj)
		if !same || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			w, has := y[k]
			if !has || !celEqual(v, w) {
				return false
			}
		}
		return true
	}
	return false
}
