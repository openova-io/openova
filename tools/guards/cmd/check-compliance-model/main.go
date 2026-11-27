// Command check-compliance-model holds the data files of the compliance model and the
// policies they describe together (CMP-041, ADR-0029; guards 4, 5, 6 and 13 of the
// compliance plane's design).
//
// The policy chart platform/kyverno-policies/chart renders one ComplianceModel object
// from model/model.yaml, model/checks.yaml and model/exemptions.yaml
// (templates/compliance-model.yaml). The read model of catalyst-api scores every
// subject with that object. A policy with no check is never scored; a check with no
// policy is never measured; an exempt namespace with no reason hides a gap.
//
// Rules, each shown to fail by main_test.go:
//
//	M1  Every file model/<name>.yaml has the schema model/schema/<name>.schema.json and
//	    every schema a data file; each data file is valid against its schema (draft
//	    2020-12, the keywords of schema.go; a schema that uses another keyword is an
//	    error, never a pass).
//	M2  The data files form a model the loader accepts: ids of levels, groups, axes and
//	    checks are unique; levels are sorted by min, highest first, and the last min is
//	    0; the group of each axis is a group; the axis of each check is an axis of type
//	    checks.
//	M3  Every validating policy template has a check of its metadata.name, and every
//	    check of source kyverno has exactly one such template. A validating policy
//	    template is a document of kind ValidatingPolicy, or of kind ClusterPolicy with a
//	    validate rule, in a file under templates/ of the policy chart (since CMP-086 no
//	    policy of a check lives in another chart). Its metadata.name must be literal. A
//	    ClusterPolicy whose rules only mutate or only verify images is not a check.
//	M4  The valuesKey of a kyverno check names the values block its own template
//	    reads: a map under compliancePolicies of values.yaml and the text
//	    .Values.compliancePolicies.<valuesKey>. in the template.
//	M5  Every kyverno check has a suite tests/<check id>/kyverno-test.yaml in the policy
//	    chart, or is listed in scripts/baselines/policy-suites.json. The list only falls:
//	    an entry whose check has a suite, or that is not a kyverno check, is a finding.
//	M6  The exempt namespaces (every name in an excludeNamespaces or
//	    extraExcludeNamespaces list of a block under compliancePolicies in values.yaml)
//	    are exactly the names in scripts/baselines/policy-exemptions.json, which only
//	    falls: a new exempt namespace is a finding, and so is an entry no list holds any
//	    more. They are also exactly the namespaces of model/exemptions.yaml: one with no
//	    reason is a finding, and so is a reason no list uses.
//	M7  Module kinds stay in their Blueprint: no file under products/catalyst/chart/crds/
//	    has a line kind: ComplianceModel or kind: ComplianceDecision, and every
//	    CustomResourceDefinition of either kind in the files Git lists (tracked, and new
//	    files Git does not ignore) is in group compliance.openova.io under
//	    platform/kyverno-policies/chart/crds/.
//
// The YAML files are read with yq (mikefarah, v4; `yq -o=json 'explode(.)'`), which
// the tree-guards job has. A template is read as text: Helm comments ({{/* */}}) and
// YAML comment lines are dropped, documents are split at lines that start with ---.
// What it cannot see: a policy object built at run time or rendered by another
// chart, and a policy name produced by template logic (that is a finding of M3).
//
// The baselines are {"schema": "openova.io/guard-baseline/v1", "counts": {<metric>:
// {"<name>": 1, ...}}, "allow": []}, the metric policy_checks_without_suite or
// policy_exempt_namespaces.
//
// Usage (from tools/guards): go run ./cmd/check-compliance-model [--root <repository root>]
// Exit 0 when every rule holds, 1 on a finding or when an input cannot be read, 2 on
// wrong usage. The last line names the counts it judged.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Paths of the tree, relative to the repository root.
const (
	Chart           = "platform/kyverno-policies/chart"
	ModelDir        = Chart + "/model"
	SchemaDir       = ModelDir + "/schema"
	Values          = Chart + "/values.yaml"
	Templates       = Chart + "/templates"
	Suites          = Chart + "/tests"
	ModuleCRDs      = Chart + "/crds"
	PlatformCRDs    = "products/catalyst/chart/crds"
	SuitesBaseline  = "scripts/baselines/policy-suites.json"
	ExemptBaseline  = "scripts/baselines/policy-exemptions.json"
	SuitesMetric    = "policy_checks_without_suite"
	ExemptMetric    = "policy_exempt_namespaces"
	BaselineSchema  = "openova.io/guard-baseline/v1"
	ModuleGroup     = "compliance.openova.io"
	sourceKyverno   = "kyverno"
	axisTypeChecks  = "checks"
	me              = "check-compliance-model: "
	usageText       = "usage: go run ./cmd/check-compliance-model [--root <repository root>]"
	defaultYQBinary = "yq"
)

// The kinds that are modules of the policy Blueprint.
var moduleKinds = []string{"ComplianceModel", "ComplianceDecision"}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func say(w io.Writer, format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }

func run(args []string, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("check-compliance-model", flag.ContinueOnError)
	fl.SetOutput(stderr)
	root := fl.String("root", "", "the repository root (default: git rev-parse --show-toplevel)")
	if err := fl.Parse(args); err != nil || fl.NArg() != 0 {
		say(stderr, "%s\n", usageText)
		return 2
	}
	if *root == "" {
		top, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
		if err != nil {
			say(stderr, "%sthe repository root cannot be found: %v\n", me, err)
			return 1
		}
		*root = strings.TrimSpace(string(top))
	}
	findings, summary, err := Check(*root)
	if err != nil {
		say(stderr, "%sFAIL, %v\n", me, err)
		return 1
	}
	for _, f := range findings {
		say(stdout, "%sFAIL, %s\n", me, f)
	}
	if len(findings) > 0 {
		say(stdout, "%s%d findings; %s\n", me, len(findings), summary)
		return 1
	}
	say(stdout, "%sok; %s\n", me, summary)
	return 0
}

// tree reads files under root.
type tree struct{ root string }

func (t tree) read(rel string) ([]byte, error) {
	return os.ReadFile(filepath.Join(t.root, rel)) // #nosec G304 -- a file of the tree under judgement
}

func (t tree) exists(rel string) bool {
	_, err := os.Stat(filepath.Join(t.root, rel))
	return err == nil
}

// yaml reads one YAML file as JSON, with every alias expanded.
func (t tree) yaml(rel string) (any, error) {
	if _, err := os.Stat(filepath.Join(t.root, rel)); err != nil {
		return nil, fmt.Errorf("%s cannot be read: %v", rel, err)
	}
	cmd := exec.Command(defaultYQBinary, "-o=json", "-I=0", "explode(.)", filepath.Join(t.root, rel)) // #nosec G204 -- a fixed program and expression; the argument is a file of the tree
	var errb bytes.Buffer
	cmd.Stderr = &errb
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s cannot be read with yq: %v %s", rel, err, strings.TrimSpace(errb.String()))
	}
	var doc any
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("%s: yq printed no single JSON document: %v", rel, err)
	}
	return doc, nil
}

// files lists the files Git lists under root: tracked, and new files Git does not ignore.
func (t tree) files() ([]string, error) {
	cmd := exec.Command("git", "-C", t.root, "ls-files", "-z", "--cached", "--others", "--exclude-standard") // #nosec G204 -- fixed arguments but the root the caller names
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %v", err)
	}
	var list []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" && t.exists(p) {
			list = append(list, p)
		}
	}
	sort.Strings(list)
	return list, nil
}

// check is one entry of model/checks.yaml, as far as the rules read it.
type check struct {
	ID, Source, Axis, ValuesKey string
}

// policyTemplate is one validating policy document of a template file.
type policyTemplate struct {
	file, name, text string
}

// Check judges the tree at root. It returns the findings, a summary of what it read,
// and an error when an input cannot be read.
func Check(root string) ([]string, string, error) {
	t := tree{root: root}
	var findings []string
	add := func(rule, format string, args ...any) {
		findings = append(findings, rule+" "+fmt.Sprintf(format, args...))
	}

	docs, err := checkSchemas(t, add)
	if err != nil {
		return nil, "", err
	}
	checks, err := modelChecks(docs["checks"])
	if err != nil {
		return nil, "", err
	}
	checkReferences(docs["model"], checks, add)

	policies, err := readPolicies(t)
	if err != nil {
		return nil, "", err
	}
	byName := map[string][]policyTemplate{}
	for _, p := range policies {
		byName[p.name] = append(byName[p.name], p)
	}
	checkIDs := map[string]check{}
	for _, c := range checks {
		checkIDs[c.ID] = c
	}
	for _, name := range sortedKeys(byName) {
		if _, ok := checkIDs[name]; !ok {
			add("M3", "the validating policy %s (%s) has no check in %s/checks.yaml", name, byName[name][0].file, ModelDir)
		}
	}
	values, err := t.yaml(Values)
	if err != nil {
		return nil, "", err
	}
	blocks := policyBlocks(values)
	kyverno := 0
	for _, c := range checks {
		if c.Source != sourceKyverno {
			continue
		}
		kyverno++
		ps := byName[c.ID]
		switch {
		case len(ps) == 0:
			add("M3", "the kyverno check %s names no validating policy template", c.ID)
			continue
		case len(ps) > 1:
			add("M3", "the kyverno check %s names %d policy templates: %s", c.ID, len(ps), templateFiles(ps))
			continue
		}
		checkValuesKey(c, ps[0], blocks, add)
	}
	if err := checkSuites(t, checks, add); err != nil {
		return nil, "", err
	}
	exempt, err := checkExemptions(t, blocks, docs["exemptions"], add)
	if err != nil {
		return nil, "", err
	}
	crds, err := checkModuleKinds(t, add)
	if err != nil {
		return nil, "", err
	}
	summary := fmt.Sprintf("%d checks (%d of source kyverno), %d validating policy templates, %d exempt namespaces, %d module definitions",
		len(checks), kyverno, len(policies), exempt, crds)
	return findings, summary, nil
}

// checkSchemas applies M1 and returns the data files by name.
func checkSchemas(t tree, add func(string, string, ...any)) (map[string]any, error) {
	data, err := listDir(t, ModelDir, ".yaml")
	if err != nil {
		return nil, err
	}
	schemas, err := listDir(t, SchemaDir, ".schema.json")
	if err != nil {
		return nil, err
	}
	docs := map[string]any{}
	for _, name := range []string{"model", "checks", "exemptions"} {
		if !data[name] {
			return nil, fmt.Errorf("%s/%s.yaml is missing", ModelDir, name)
		}
	}
	for _, name := range sortedKeys(data) {
		if !schemas[name] {
			add("M1", "%s/%s.yaml has no schema %s/%s.schema.json", ModelDir, name, SchemaDir, name)
			continue
		}
		doc, err := t.yaml(ModelDir + "/" + name + ".yaml")
		if err != nil {
			return nil, err
		}
		docs[name] = doc
		raw, err := t.read(SchemaDir + "/" + name + ".schema.json")
		if err != nil {
			return nil, err
		}
		var schema map[string]any
		if err := json.Unmarshal(raw, &schema); err != nil {
			return nil, fmt.Errorf("%s/%s.schema.json is not a JSON object: %v", SchemaDir, name, err)
		}
		lines, err := Validate(schema, doc)
		if err != nil {
			return nil, fmt.Errorf("%s/%s.schema.json cannot be applied: %v", SchemaDir, name, err)
		}
		for _, l := range lines {
			add("M1", "%s/%s.yaml breaks its schema at %s", ModelDir, name, l)
		}
	}
	for _, name := range sortedKeys(schemas) {
		if !data[name] {
			add("M1", "%s/%s.schema.json has no data file %s/%s.yaml", SchemaDir, name, ModelDir, name)
		}
	}
	return docs, nil
}

// listDir returns the names of the regular files of dir with suffix, the suffix cut.
func listDir(t tree, dir, suffix string) (map[string]bool, error) {
	entries, err := os.ReadDir(filepath.Join(t.root, dir))
	if err != nil {
		return nil, fmt.Errorf("%s cannot be read: %v", dir, err)
	}
	out := map[string]bool{}
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), suffix) {
			out[strings.TrimSuffix(e.Name(), suffix)] = true
		}
	}
	return out, nil
}

func str(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func objects(x any) []map[string]any {
	list, _ := x.([]any)
	var out []map[string]any
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// modelChecks reads the checks of checks.yaml. A file that is not a map with a list
// checks cannot be judged.
func modelChecks(doc any) ([]check, error) {
	m, ok := doc.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s/checks.yaml is not a map", ModelDir)
	}
	if _, isList := m["checks"].([]any); !isList {
		return nil, fmt.Errorf("%s/checks.yaml has no list checks", ModelDir)
	}
	var out []check
	for _, c := range objects(m["checks"]) {
		out = append(out, check{ID: str(c, "id"), Source: str(c, "source"), Axis: str(c, "axis"), ValuesKey: str(c, "valuesKey")})
	}
	return out, nil
}

// checkReferences applies M2.
func checkReferences(modelDoc any, checks []check, add func(string, string, ...any)) {
	m, _ := modelDoc.(map[string]any)
	unique := func(what string, ids []string) {
		seen := map[string]bool{}
		for _, id := range ids {
			if seen[id] {
				add("M2", "the %s id %q is not unique", what, id)
			}
			seen[id] = true
		}
	}
	var levelNames, groupIDs, axisIDs, checkIDs []string
	prev := 101.0
	levels := objects(m["levels"])
	for i, l := range levels {
		levelNames = append(levelNames, str(l, "name"))
		lmin, _ := l["min"].(float64)
		if lmin >= prev {
			add("M2", "levels[%d] (%s): levels are not sorted by min, highest first", i, str(l, "name"))
		}
		prev = lmin
	}
	if len(levels) > 0 {
		if last, _ := levels[len(levels)-1]["min"].(float64); last != 0 {
			add("M2", "the last level has min %v, want 0", last)
		}
	}
	groups := map[string]bool{}
	for _, g := range objects(m["groups"]) {
		groupIDs = append(groupIDs, str(g, "id"))
		groups[str(g, "id")] = true
	}
	checkAxes := map[string]bool{}
	for _, a := range objects(m["axes"]) {
		axisIDs = append(axisIDs, str(a, "id"))
		if !groups[str(a, "group")] {
			add("M2", "the axis %s names the group %q, which is not a group", str(a, "id"), str(a, "group"))
		}
		if str(a, "type") == axisTypeChecks {
			checkAxes[str(a, "id")] = true
		}
	}
	for _, c := range checks {
		checkIDs = append(checkIDs, c.ID)
		if !checkAxes[c.Axis] {
			add("M2", "the check %s names the axis %q, which is not an axis of type checks", c.ID, c.Axis)
		}
	}
	unique("level", levelNames)
	unique("group", groupIDs)
	unique("axis", axisIDs)
	unique("check", checkIDs)
}

var (
	helmComment = regexp.MustCompile(`(?s)\{\{-?\s*/\*.*?\*/\s*-?\}\}`)
	docSplit    = regexp.MustCompile(`(?m)^---.*$`)
	policyKind  = regexp.MustCompile(`(?m)^kind:[ \t]*(ClusterPolicy|ValidatingPolicy)[ \t]*$`)
	metaName    = regexp.MustCompile(`(?m)^metadata:[ \t]*\n(?:[ \t]+.*\n|[ \t]*\n)*?  name:[ \t]*(.*?)[ \t]*$`)
	validates   = regexp.MustCompile(`(?m)^[ \t]+validate:`)
)

// stripComments drops Helm comments and YAML comment lines.
func stripComments(text string) string {
	text = helmComment.ReplaceAllString(text, "")
	var keep []string
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		keep = append(keep, line)
	}
	return strings.Join(keep, "\n")
}

// readPolicies returns every validating policy document of the template files (M3). A
// document whose name is not literal is returned with the name "<not literal: ...>",
// which no check carries.
func readPolicies(t tree) ([]policyTemplate, error) {
	var out []policyTemplate
	err := filepath.WalkDir(filepath.Join(t.root, Templates), func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || (!strings.HasSuffix(p, ".yaml") && !strings.HasSuffix(p, ".yml")) {
			return nil
		}
		b, err := os.ReadFile(p) // #nosec G304 -- a file of the tree under judgement
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(t.root, p)
		rel = filepath.ToSlash(rel)
		for _, doc := range docSplit.Split(stripComments(string(b)), -1) {
			k := policyKind.FindStringSubmatch(doc)
			if k == nil || (k[1] == "ClusterPolicy" && !validates.MatchString(doc)) {
				continue
			}
			name := ""
			if m := metaName.FindStringSubmatch(doc); m != nil {
				name = strings.Trim(m[1], `"'`)
			}
			if name == "" || strings.Contains(name, "{{") {
				name = "<not literal: " + strings.TrimSpace(name) + ">"
			}
			out = append(out, policyTemplate{file: rel, name: name, text: doc})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("%s cannot be read: %v", Templates, err)
	}
	return out, nil
}

func templateFiles(ps []policyTemplate) string {
	var fs []string
	for _, p := range ps {
		fs = append(fs, p.file)
	}
	return strings.Join(fs, ", ")
}

// policyBlocks returns the maps under compliancePolicies of the values, by key.
func policyBlocks(values any) map[string]map[string]any {
	out := map[string]map[string]any{}
	v, _ := values.(map[string]any)
	cp, _ := v["compliancePolicies"].(map[string]any)
	for k, b := range cp {
		if m, ok := b.(map[string]any); ok {
			out[k] = m
		}
	}
	return out
}

// checkValuesKey applies M4 to one kyverno check and its template.
func checkValuesKey(c check, p policyTemplate, blocks map[string]map[string]any, add func(string, string, ...any)) {
	if c.ValuesKey == "" {
		add("M4", "the kyverno check %s has no valuesKey", c.ID)
		return
	}
	if _, ok := blocks[c.ValuesKey]; !ok {
		add("M4", "the check %s names valuesKey %s, which is no block under compliancePolicies of %s", c.ID, c.ValuesKey, Values)
	}
	if !strings.Contains(p.text, ".Values.compliancePolicies."+c.ValuesKey+".") {
		add("M4", "the template %s of the check %s does not read .Values.compliancePolicies.%s", p.file, c.ID, c.ValuesKey)
	}
}

// readBaseline reads the names of one baseline file.
func readBaseline(t tree, rel, metric string) (map[string]bool, error) {
	b, err := t.read(rel)
	if err != nil {
		return nil, fmt.Errorf("%s cannot be read: %v", rel, err)
	}
	var doc struct {
		Schema string                    `json:"schema"`
		Counts map[string]map[string]int `json:"counts"`
		Allow  []any                     `json:"allow"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%s is not a baseline: %v", rel, err)
	}
	entries, ok := doc.Counts[metric]
	if doc.Schema != BaselineSchema || !ok || len(doc.Counts) != 1 || doc.Allow == nil || len(doc.Allow) != 0 {
		return nil, fmt.Errorf("%s must be {\"schema\": %q, \"counts\": {%q: {...}}, \"allow\": []}", rel, BaselineSchema, metric)
	}
	out := map[string]bool{}
	for name, n := range entries {
		if n != 1 {
			return nil, fmt.Errorf("%s: the entry %s is %d, every entry is 1", rel, name, n)
		}
		out[name] = true
	}
	return out, nil
}

// checkSuites applies M5.
func checkSuites(t tree, checks []check, add func(string, string, ...any)) error {
	allowed, err := readBaseline(t, SuitesBaseline, SuitesMetric)
	if err != nil {
		return err
	}
	kyverno := map[string]bool{}
	for _, c := range checks {
		if c.Source != sourceKyverno {
			continue
		}
		kyverno[c.ID] = true
		has := t.exists(Suites + "/" + c.ID + "/kyverno-test.yaml")
		switch {
		case !has && !allowed[c.ID]:
			add("M5", "the kyverno check %s has no suite %s/%s/kyverno-test.yaml and is not listed in %s", c.ID, Suites, c.ID, SuitesBaseline)
		case has && allowed[c.ID]:
			add("M5", "the kyverno check %s has a suite and is still listed in %s: remove the entry", c.ID, SuitesBaseline)
		}
	}
	for _, name := range sortedKeys(allowed) {
		if !kyverno[name] {
			add("M5", "%s lists %s, which is no kyverno check: remove the entry", SuitesBaseline, name)
		}
	}
	return nil
}

// checkExemptions applies M6 and returns the number of exempt namespaces.
func checkExemptions(t tree, blocks map[string]map[string]any, reasonsDoc any, add func(string, string, ...any)) (int, error) {
	exempt := map[string]bool{}
	for _, b := range blocks {
		for _, key := range []string{"excludeNamespaces", "extraExcludeNamespaces"} {
			list, present := b[key]
			if !present {
				continue
			}
			names, isList := list.([]any)
			if !isList && list != nil {
				return 0, fmt.Errorf("%s: a %s value is not a list", Values, key)
			}
			for _, n := range names {
				s, isStr := n.(string)
				if !isStr {
					return 0, fmt.Errorf("%s: a %s entry is not a string", Values, key)
				}
				exempt[s] = true
			}
		}
	}
	allowed, err := readBaseline(t, ExemptBaseline, ExemptMetric)
	if err != nil {
		return 0, err
	}
	m, _ := reasonsDoc.(map[string]any)
	reasons, _ := m["namespaces"].(map[string]any)
	for _, ns := range sortedKeys(exempt) {
		if !allowed[ns] {
			add("M6", "the namespace %s is exempt in %s and not in %s: the set of exempt namespaces may only shrink", ns, Values, ExemptBaseline)
		}
		if _, ok := reasons[ns]; !ok {
			add("M6", "the exempt namespace %s has no reason in %s/exemptions.yaml", ns, ModelDir)
		}
	}
	for _, ns := range sortedKeys(allowed) {
		if !exempt[ns] {
			add("M6", "%s lists %s, which no exclude list of %s holds any more: remove the entry", ExemptBaseline, ns, Values)
		}
	}
	for _, ns := range sortedKeys(reasons) {
		if !exempt[ns] {
			add("M6", "%s/exemptions.yaml gives a reason for %s, which no exclude list of %s holds: remove it", ModelDir, ns, Values)
		}
	}
	return len(exempt), nil
}

var (
	crdKind    = regexp.MustCompile(`(?m)^kind:[ \t]*CustomResourceDefinition[ \t]*$`)
	namesKind  = regexp.MustCompile(`(?m)^[ \t]+kind:[ \t]*["']?(\w+)["']?[ \t]*$`)
	groupLine  = regexp.MustCompile(`(?m)^  group:[ \t]*["']?([\w.-]+)["']?[ \t]*$`)
	moduleLine = regexp.MustCompile(`(?m)^[ \t]*kind:[ \t]*["']?(ComplianceModel|ComplianceDecision)["']?[ \t]*$`)
)

// checkModuleKinds applies M7 and returns the number of module definitions found.
func checkModuleKinds(t tree, add func(string, string, ...any)) (int, error) {
	files, err := t.files()
	if err != nil {
		return 0, err
	}
	found := 0
	for _, f := range files {
		if !strings.HasSuffix(f, ".yaml") && !strings.HasSuffix(f, ".yml") {
			continue
		}
		b, err := t.read(f)
		if err != nil {
			return 0, fmt.Errorf("%s cannot be read: %v", f, err)
		}
		text := string(b)
		if strings.HasPrefix(f, PlatformCRDs+"/") && moduleLine.MatchString(stripComments(text)) {
			add("M7", "%s defines a module kind of the policy Blueprint (%s): it belongs in %s", f, moduleLine.FindStringSubmatch(stripComments(text))[1], ModuleCRDs)
		}
		if !strings.Contains(text, "CustomResourceDefinition") {
			continue
		}
		for _, doc := range docSplit.Split(stripComments(text), -1) {
			if !crdKind.MatchString(doc) {
				continue
			}
			for _, k := range namesKind.FindAllStringSubmatch(doc, -1) {
				if !isModuleKind(k[1]) {
					continue
				}
				found++
				group := ""
				if g := groupLine.FindStringSubmatch(doc); g != nil {
					group = g[1]
				}
				if group != ModuleGroup {
					add("M7", "%s defines %s in group %q, want %s", f, k[1], group, ModuleGroup)
				}
				if !strings.HasPrefix(f, ModuleCRDs+"/") {
					add("M7", "%s defines %s outside %s", f, k[1], ModuleCRDs)
				}
			}
		}
	}
	return found, nil
}

func isModuleKind(k string) bool {
	for _, m := range moduleKinds {
		if k == m {
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
