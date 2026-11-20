// Command cronjob-jobs judges the Jobs that the CronJobs of the kit create, and
// the Pods of those Jobs, against the ClusterPolicy flux-managed as the kit
// renders it.
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
// ClusterPolicy -policy-name (flux-managed) of that render, or of the file
// -policy (JSON, one object or one a line). The guard reads from the policy,
// never from a copy of its own: the kinds and filters of match, every filter
// of exclude (kinds, names, namespaces, annotations, selector,
// namespaceSelector, operations) and the conditions of validate.deny of the
// rule -rule (workload-must-be-flux-managed).
//
// For each CronJob of the render it builds the Job the controller creates
// (kind Job, the CronJob's namespace, the labels and annotations of
// jobTemplate.metadata, jobTemplate.spec, a controller ownerReference to the
// CronJob) and the Pod of that Job (the labels and annotations of
// jobTemplate.spec.template.metadata, with job-name, and a controller
// ownerReference to the Job). Each is judged as Kyverno does on CREATE: an
// object the match does not select, or that a filter of exclude selects, is
// admitted; else it is denied when every condition of deny.conditions.all
// holds and, when there are any, one of deny.conditions.any holds.
//
// Where the guard cannot read the policy it errs toward a denial, never a
// pass: a match filter it does not understand (subjects, roles, clusterRoles)
// selects the object; an exclude filter it does not understand excludes
// nothing; a condition it cannot evaluate holds. A condition key it evaluates
// is a JMESPath of request.object fields (`request.object.a."b/c"`), joined by
// `||`, with string literals ('x'); operators Equals, NotEquals (wildcards in
// the value, as Kyverno), AnyIn, AllIn, AnyNotIn, AllNotIn, In and NotIn.
// A namespaceSelector reads the labels of a Namespace object of the render and
// kubernetes.io/metadata.name; the Namespaces the kit applies outside a chart
// are not in the render, so their labels are not read (a selector on them
// excludes nothing).
//
// A denial names the chart, the CronJob and the labels the rule names: each
// condition of deny.conditions.all that reads one label of
// request.object.metadata.labels, as `label: value` (NotEquals) or `label
// (any value)` (Equals the empty string); one of them on
// jobTemplate.metadata.labels admits the Job.
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
// read, the policy or its rule is not there, the rule names no label the guard
// can read, or the render holds no CronJob.
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
	"strings"
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
	policyFile := flags.String("policy", "", "a file of the ClusterPolicy in JSON (default: the ClusterPolicy of the render)")
	policyName := flags.String("policy-name", "flux-managed", "the name of the ClusterPolicy")
	ruleName := flags.String("rule", "workload-must-be-flux-managed", "the name of its rule")
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
		say(errw, "cronjob-jobs: no ClusterPolicy %s in %s: nothing can be judged\n", *policyName, orRender(*policyFile, *render))
		return 2
	}
	var rules []rule
	seen := map[string]bool{}
	for _, p := range policies {
		r, err := readRule(p, *ruleName)
		if err != nil {
			say(errw, "cronjob-jobs: ClusterPolicy %s: %v\n", *policyName, err)
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
	action := str(dig(policies[0], "spec", "validationFailureAction"))
	say(out, "cronjob-jobs: %d CronJob render(s) of %d release render(s) judged against ClusterPolicy %s rule %s (validationFailureAction %s in this render; the hand-over makes it Enforce); %d denied.\n",
		judged, len(releases), *policyName, *ruleName, orDash(action), denied)
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

func findPolicies(objects []obj, name string) []obj {
	var out []obj
	for _, o := range objects {
		if str(o["kind"]) == "ClusterPolicy" && str(dig(o, "metadata", "name")) == name {
			out = append(out, o)
		}
	}
	return out
}

// rule is the rule of the policy the guard judges by.
type rule struct {
	raw     obj
	match   obj
	exclude obj
	all     []any
	any     []any
	labels  []string // the labels the rule names, `label: value` or `label (any value)`
}

func readRule(policy obj, name string) (rule, error) {
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
		out = append(out, verdict{denied: true, line: fmt.Sprintf("FAIL %s: %s would be denied by the rule: %s carries none of %s",
			who, c.what, c.where, strings.Join(r.labels, ", "))})
	}
	return out
}

// judge admits or denies one object on CREATE, and says why.
func judge(o obj, r rule, nsLabels map[string]string) (bool, string) {
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
