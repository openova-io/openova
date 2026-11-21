package bootstrapkit

// The bound of a function test in the kit (docs/RUNBOOKS.md section 4.5).
//
// Flux runs the test of a HelmRelease that sets spec.test.enable once after
// each install or upgrade, and a release whose test failed with
// spec.test.ignoreFailures counts as tested: the test is not run again until
// the next upgrade. On a fresh install the releases arrive over about an hour
// and most install without waiting for their workloads (install.disableWait),
// so a test that gives up before its subject is up fails once and stays
// failed. A function test therefore waits for its subject itself, within one
// bound T, and every release of the kit that enables its test obeys:
//
//	B1  T is the install timeout of the release. T is
//	    spec.values.functionTest.timeoutSeconds, else the chart default of
//	    300 seconds; the install timeout is spec.install.timeout, else
//	    spec.timeout, else Flux's 5 minutes.
//	B2  Flux waits for the test as long: the test timeout (spec.test.timeout,
//	    else spec.timeout, else 5 minutes) equals the install timeout.
//	B3  A release whose test judges what another release delivers, and that
//	    cannot depend on it, has an install timeout no shorter than that
//	    release's (testNeeds).
//	B4  T is stated as a whole number of seconds, not as text.
//	B5  The chart reads T: the test Pod its chart renders with the release's
//	    values has activeDeadlineSeconds T. Without it a stated T would pass
//	    B1 while the Pod still ends at 300 seconds. (Each chart's render test
//	    proves that the wait ends 60 seconds before T.) Every targetPath of
//	    the release's valuesFrom, which Flux reads from the cluster at
//	    install, gets a stand-in value in the render, and a value that is
//	    only a variable without a default, which the cluster's variables
//	    supply, is left to the chart's default.
//
// owed lists the releases that break B1 to B3 and that a later change repairs.
// The list only shrinks: a release on it that breaks no rule any more fails
// the test until it is removed, and a name that is not a tested release of the
// kit fails it too. A release that is not on it and breaks a rule fails it.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// The chart default of T (scripts/check-function-test.py rule FT8) and Flux's
// default of spec.timeout, which spec.install.timeout and spec.test.timeout
// fall back to.
const (
	ftbChartDefault = 300 * time.Second
	ftbFluxDefault  = 5 * time.Minute
)

// testNeeds names, for a release whose function test judges what another
// release delivers, that release. bp-plane-isolation's test needs a ready
// address of bp-keycloak's Service (functionTest.target of
// platform/plane-isolation/chart/templates/tests/function-test.yaml), and a
// dependsOn on bp-keycloak would put the isolation policies behind the identity
// service.
var testNeeds = map[string]string{
	"bp-plane-isolation": "bp-keycloak",
}

// owed: the tested releases of the kit whose function test is still bounded by
// the chart default of 300 seconds under a longer install timeout. They have
// not been measured failing for that reason. bp-openbao, bp-reloader and
// bp-valkey, which did fail their test on the fresh two-region installs of
// 20 November 2026 (a role written a minute late, a change made before
// Reloader watched, a primary that answered after the deadline), left the list
// with SVC-139; bp-catalyst-platform left it with SVC-138, which gave the
// umbrella its first function test.
var owed = map[string]bool{
	"bp-cert-manager":           true,
	"bp-cert-manager-issuers":   true,
	"bp-cilium":                 true,
	"bp-cnpg":                   true,
	"bp-cnpg-pair":              true,
	"bp-continuum":              true,
	"bp-crossplane":             true,
	"bp-crossplane-claims":      true,
	"bp-external-dns":           true,
	"bp-external-secrets":       true,
	"bp-flux":                   true,
	"bp-gateway-api":            true,
	"bp-gitea":                  true,
	"bp-harbor":                 true,
	"bp-keycloak":               true,
	"bp-kyverno":                true,
	"bp-kyverno-policies":       true,
	"bp-oidc-gate":              true,
	"bp-postgres-shared":        true,
	"bp-postgres-shared-b":      true,
	"bp-postgres-shared-c":      true,
	"bp-powerdns":               true,
	"bp-reflector":              true,
	"bp-seaweedfs":              true,
	"bp-self-sovereign-cutover": true,
	"bp-sovereign-tls-vars":     true,
}

// ftbRelease is one HelmRelease of the kit that enables its test, with the
// values rule B1 to B5 read and where each came from.
type ftbRelease struct {
	File, Name, Chart, ReleaseName, Namespace string
	Install, Test                             time.Duration
	InstallFrom, TestFrom                     string
	Bound                                     time.Duration
	BoundFrom                                 string
	BoundBad                                  string // B4: the stated value when it is not a whole number
	Values                                    map[string]any
	StandIns                                  []string // B5: the targetPath of every valuesFrom entry
}

func ftbDig(v any, path ...string) any {
	for _, p := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[p]
	}
	return v
}

// ftbDuration reads the first of the given fields that is set, else the
// fallback. It returns the value and the field it came from.
func ftbDuration(doc map[string]any, fallback time.Duration, fields ...[]string) (time.Duration, string, error) {
	for _, f := range fields {
		raw := ftbDig(doc, f...)
		if raw == nil {
			continue
		}
		s, ok := raw.(string)
		if !ok {
			return 0, "", fmt.Errorf("%s is %v, not a duration", strings.Join(f, "."), raw)
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			return 0, "", fmt.Errorf("%s: %w", strings.Join(f, "."), err)
		}
		return d, strings.Join(f, "."), nil
	}
	return fallback, "Flux's default", nil
}

// ftbParse reads every HelmRelease of a kit text that enables its test, and
// adds the install timeout of every HelmRelease of the text to installs.
func ftbParse(file string, raw []byte, installs map[string]time.Duration) ([]ftbRelease, error) {
	var out []ftbRelease
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		if doc == nil || doc["kind"] != "HelmRelease" {
			continue
		}
		r := ftbRelease{File: file}
		r.Name, _ = ftbDig(doc, "metadata", "name").(string)
		var err error
		if r.Install, r.InstallFrom, err = ftbDuration(doc, ftbFluxDefault, []string{"spec", "install", "timeout"}, []string{"spec", "timeout"}); err != nil {
			return nil, fmt.Errorf("%s %s: %w", file, r.Name, err)
		}
		installs[r.Name] = r.Install
		if ftbDig(doc, "spec", "test", "enable") != true {
			continue
		}
		r.Chart, _ = ftbDig(doc, "spec", "chart", "spec", "chart").(string)
		r.ReleaseName, _ = ftbDig(doc, "spec", "releaseName").(string)
		r.Namespace, _ = ftbDig(doc, "spec", "targetNamespace").(string)
		r.Values, _ = ftbDig(doc, "spec", "values").(map[string]any)
		entries, _ := ftbDig(doc, "spec", "valuesFrom").([]any)
		for _, e := range entries {
			if p, _ := ftbDig(e, "targetPath").(string); p != "" {
				r.StandIns = append(r.StandIns, p)
			}
		}
		if r.Test, r.TestFrom, err = ftbDuration(doc, ftbFluxDefault, []string{"spec", "test", "timeout"}, []string{"spec", "timeout"}); err != nil {
			return nil, fmt.Errorf("%s %s: %w", file, r.Name, err)
		}
		r.Bound, r.BoundFrom = ftbChartDefault, "the chart default"
		if raw := ftbDig(r.Values, "functionTest", "timeoutSeconds"); raw != nil {
			if n, ok := raw.(int); ok {
				r.Bound, r.BoundFrom = time.Duration(n)*time.Second, "spec.values.functionTest.timeoutSeconds"
			} else {
				r.BoundBad = fmt.Sprintf("%v", raw)
			}
		}
		out = append(out, r)
	}
	return out, nil
}

func ftbSeconds(d time.Duration) string { return fmt.Sprintf("%d s", int(d.Seconds())) }

// ftbFindings applies B1 to B4 to the tested releases: release name ->
// findings. installs holds the install timeout of every release of the kit.
func ftbFindings(rels []ftbRelease, installs map[string]time.Duration, needs map[string]string) map[string][]string {
	out := map[string][]string{}
	add := func(r ftbRelease, format string, a ...any) {
		out[r.Name] = append(out[r.Name], fmt.Sprintf("%s (%s): ", r.Name, r.File)+fmt.Sprintf(format, a...))
	}
	for _, r := range rels {
		if r.BoundBad != "" {
			add(r, "B4 spec.values.functionTest.timeoutSeconds is %q, not a whole number of seconds", r.BoundBad)
		} else if r.Bound != r.Install {
			add(r, "B1 T is %s (%s) but the install timeout is %s (%s): the test gives up before the release may still be installing",
				ftbSeconds(r.Bound), r.BoundFrom, ftbSeconds(r.Install), r.InstallFrom)
		}
		if r.Test != r.Install {
			add(r, "B2 the test timeout is %s (%s) but the install timeout is %s (%s)",
				ftbSeconds(r.Test), r.TestFrom, ftbSeconds(r.Install), r.InstallFrom)
		}
		if other, ok := needs[r.Name]; ok {
			install, found := installs[other]
			switch {
			case !found:
				add(r, "B3 its test needs %s, which is not a release of the kit", other)
			case r.Install < install:
				add(r, "B3 its test needs what %s delivers, whose install timeout is %s, but its own is %s",
					other, ftbSeconds(install), ftbSeconds(r.Install))
			}
		}
	}
	return out
}

// ftbOwed checks the findings against the owed list: the problems that fail
// the test, and the findings of owed releases, which are reported only.
func ftbOwed(rels []ftbRelease, findings map[string][]string, owedList map[string]bool) (problems, reported []string) {
	tested := map[string]bool{}
	for _, r := range rels {
		tested[r.Name] = true
	}
	for name, f := range findings {
		if owedList[name] {
			reported = append(reported, f...)
		} else {
			problems = append(problems, f...)
		}
	}
	for name := range owedList {
		switch {
		case !tested[name]:
			problems = append(problems, fmt.Sprintf("%s is owed but is not a release of the kit that enables its test: remove it from owed", name))
		case len(findings[name]) == 0:
			problems = append(problems, fmt.Sprintf("%s is owed but breaks no rule any more: remove it from owed (the list only shrinks)", name))
		}
	}
	sort.Strings(problems)
	sort.Strings(reported)
	return problems, reported
}

// ftbKit reads every tested HelmRelease of clusters/_template/bootstrap-kit,
// and the install timeout of every HelmRelease of the kit.
func ftbKit(t *testing.T) ([]ftbRelease, map[string]time.Duration) {
	t.Helper()
	root := repoRoot(t)
	files, err := filepath.Glob(filepath.Join(root, "clusters", "_template", "bootstrap-kit", "*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no slot file of the kit: %v", err)
	}
	var rels []ftbRelease
	installs := map[string]time.Duration{}
	for _, f := range files {
		raw, err := os.ReadFile(f) //nolint:gosec // a slot file of this repository
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		got, err := ftbParse(filepath.Base(f), raw, installs)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		rels = append(rels, got...)
	}
	if len(rels) < 30 {
		t.Fatalf("the kit has %d HelmReleases that enable their test; expected the 39 or so the kit carries: the reader is broken", len(rels))
	}
	return rels, installs
}

// TestFunctionTestBound_KitHoldsTheBound applies B1 to B4 to every tested
// release of the kit, with the owed list.
func TestFunctionTestBound_KitHoldsTheBound(t *testing.T) {
	rels, installs := ftbKit(t)
	findings := ftbFindings(rels, installs, testNeeds)
	problems, reported := ftbOwed(rels, findings, owed)
	for _, line := range reported {
		t.Logf("owed: %s", line)
	}
	t.Logf("%d tested releases; %d owed with %d findings", len(rels), len(owed), len(reported))
	for _, p := range problems {
		t.Error(p)
	}
}

// ftbSubstitute applies Flux's post-build substitution with no variable set:
// ${NAME:=default}, ${NAME:-default}, ${NAME=default} and ${NAME-default} give
// the default, ${NAME} gives the empty text.
var ftbVariable = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*(?::?[=-]([^}]*))?\}`)

func ftbSubstitute(s string) string {
	// A default may hold a further ${...}: the inner one is left by the first
	// pass, so substitute until nothing changes.
	for i := 0; i < 5; i++ {
		next := ftbVariable.ReplaceAllStringFunc(s, func(m string) string {
			return ftbVariable.FindStringSubmatch(m)[1]
		})
		if next == s {
			break
		}
		s = next
	}
	return s
}

// ftbBareVariable is a value that is only a variable without a default.
var ftbBareVariable = regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*\}$`)

// ftbWithoutBareVariables returns a copy of values without every key whose
// value is only a variable without a default. The cluster's variables supply
// such a value at install, as a ConfigMap supplies a valuesFrom target, and a
// render has not got them: the empty text would be null, which removes the
// chart's default of the key (bp-catalyst-platform's parentZones, a list,
// from ${PARENT_DOMAINS_YAML}). Without the key the chart's default holds.
func ftbWithoutBareVariables(values map[string]any) map[string]any {
	out := make(map[string]any, len(values))
	for k, x := range values {
		switch v := x.(type) {
		case map[string]any:
			x = ftbWithoutBareVariables(v)
		case string:
			if ftbBareVariable.MatchString(v) {
				continue
			}
		}
		out[k] = x
	}
	return out
}

// ftbStandIn is the value B5 gives a valuesFrom target. Flux reads the value
// from a ConfigMap or Secret of the cluster at install, which a render has
// not got, and a chart's test may render only when it is set: bp-openbao's
// needs seal.stanza, which slot 08 takes from the ConfigMap
// sovereign-seal-profile.
const ftbStandIn = "stand-in for a valuesFrom target"

func ftbCopy(v map[string]any) map[string]any {
	out := make(map[string]any, len(v))
	for k, x := range v {
		if m, ok := x.(map[string]any); ok {
			x = ftbCopy(m)
		}
		out[k] = x
	}
	return out
}

// ftbWithStandIns returns a copy of values in which every target path that
// values does not set holds ftbStandIn. Flux merges spec.values over
// valuesFrom, so a value the release sets wins. A path that is not a plain
// dotted one (an index or an escape of Helm's --set syntax) is refused.
func ftbWithStandIns(values map[string]any, paths []string) (map[string]any, error) {
	out := ftbCopy(values)
	for _, p := range paths {
		keys := strings.Split(p, ".")
		for _, k := range keys {
			if k == "" || strings.ContainsAny(k, `[]\=,`) {
				return nil, fmt.Errorf("B5 the valuesFrom targetPath %q is not a plain dotted path: the render cannot give it a stand-in value", p)
			}
		}
		m := out
		for _, k := range keys[:len(keys)-1] {
			next, ok := m[k].(map[string]any)
			if !ok {
				if m[k] != nil {
					return nil, fmt.Errorf("B5 the valuesFrom targetPath %q: %s is set in spec.values and is not a map", p, k)
				}
				next = map[string]any{}
				m[k] = next
			}
			m = next
		}
		if last := keys[len(keys)-1]; m[last] == nil {
			m[last] = ftbStandIn
		}
	}
	return out, nil
}

// ftbPodDeadline applies B5 to a render: the function test Pod's
// activeDeadlineSeconds must be want.
func ftbPodDeadline(docs []map[string]any, want time.Duration) string {
	var pods []map[string]any
	for _, d := range docs {
		if d["kind"] == "Pod" && ftbDig(d, "metadata", "labels", "catalyst.openova.io/function-test") == "true" {
			pods = append(pods, d)
		}
	}
	if len(pods) != 1 {
		return fmt.Sprintf("B5 the chart renders %d function test Pods with the release's values, not 1", len(pods))
	}
	got := ftbDig(pods[0], "spec", "activeDeadlineSeconds")
	if n, ok := got.(int); !ok || time.Duration(n)*time.Second != want {
		return fmt.Sprintf("B5 the test Pod's activeDeadlineSeconds is %v, not T (%s): the chart does not read functionTest.timeoutSeconds", got, ftbSeconds(want))
	}
	return ""
}

// ftbChartDeps builds the dependencies of a chart once, and only when its
// charts/ holds no archive. `helm dependency build` without a Chart.lock
// fetches from a chart repository that was never added, but with the
// Chart.lock an earlier build wrote (a render test of the same chart) it
// refuses one: "no repository definition".
func ftbChartDeps(t *testing.T, helmBin, dir string) {
	t.Helper()
	if archives, _ := filepath.Glob(filepath.Join(dir, "charts", "*.tgz")); len(archives) > 0 {
		chartDepsBuilt.Store(dir, true)
		return
	}
	ensureChartDeps(t, helmBin, dir)
}

// ftbChartDirs maps the name of every chart of platform/ and products/ to its
// folder.
func ftbChartDirs(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, pattern := range []string{"platform/*/chart/Chart.yaml", "products/*/chart/Chart.yaml"} {
		files, _ := filepath.Glob(filepath.Join(root, pattern))
		for _, f := range files {
			raw, err := os.ReadFile(f) //nolint:gosec // a Chart.yaml of this repository
			if err != nil {
				continue
			}
			var c struct {
				Name string `yaml:"name"`
			}
			if yaml.Unmarshal(raw, &c) == nil && c.Name != "" {
				out[c.Name] = filepath.Dir(f)
			}
		}
	}
	return out
}

// TestFunctionTestBound_ChartsReadTheBound applies B5 to every tested release
// that states T: its chart, rendered with the release's values, gives the test
// Pod activeDeadlineSeconds T.
func TestFunctionTestBound_ChartsReadTheBound(t *testing.T) {
	helmBin := os.Getenv("HELM_BIN")
	if helmBin == "" {
		helmBin = "helm"
	}
	if _, err := exec.LookPath(helmBin); err != nil {
		t.Skipf("helm not on PATH (%v)", err)
	}
	root := repoRoot(t)
	dirs := ftbChartDirs(t, root)
	stated := 0
	rels, _ := ftbKit(t)
	for _, r := range rels {
		if r.BoundFrom != "spec.values.functionTest.timeoutSeconds" {
			continue
		}
		stated++
		dir, ok := dirs[r.Chart]
		if !ok {
			t.Errorf("%s (%s): its chart %s is not a chart of platform/ or products/", r.Name, r.File, r.Chart)
			continue
		}
		given, err := ftbWithStandIns(ftbWithoutBareVariables(r.Values), r.StandIns)
		if err != nil {
			t.Errorf("%s (%s): %v", r.Name, r.File, err)
			continue
		}
		text, err := yaml.Marshal(given)
		if err != nil {
			t.Fatalf("%s: %v", r.Name, err)
		}
		values := filepath.Join(t.TempDir(), "values.yaml")
		if err := os.WriteFile(values, []byte(ftbSubstitute(string(text))), 0o600); err != nil {
			t.Fatal(err)
		}
		ftbChartDeps(t, helmBin, dir)
		release := r.ReleaseName
		if release == "" {
			release = r.Name
		}
		cmd := exec.Command(helmBin, "template", release, dir, "--namespace", r.Namespace, "-f", values) //nolint:gosec // helm on PATH or HELM_BIN, arguments from the kit
		out, err := cmd.Output()
		if err != nil {
			t.Errorf("%s (%s): helm template %s: %v", r.Name, r.File, dir, err)
			continue
		}
		var docs []map[string]any
		dec := yaml.NewDecoder(strings.NewReader(string(out)))
		for {
			var doc map[string]any
			if dec.Decode(&doc) != nil {
				break
			}
			if doc != nil {
				docs = append(docs, doc)
			}
		}
		if p := ftbPodDeadline(docs, r.Bound); p != "" {
			t.Errorf("%s (%s): %s", r.Name, r.File, p)
		}
	}
	if stated == 0 {
		t.Error("no tested release of the kit states spec.values.functionTest.timeoutSeconds")
	}
	t.Logf("%d releases state T; each chart renders its test Pod with activeDeadlineSeconds T", stated)
}

// TestFunctionTestBound_EachRuleCanFail shows that each rule fails on a kit
// that breaks it, and passes the one that keeps them.
func TestFunctionTestBound_EachRuleCanFail(t *testing.T) {
	const good = `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-a
spec:
  test:
    enable: true
    timeout: 15m
  install:
    timeout: 15m
  values:
    functionTest:
      timeoutSeconds: 900
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-b
spec:
  test:
    enable: true
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-needed
spec:
  install:
    timeout: 15m
`
	needs := map[string]string{"bp-a": "bp-needed"}
	parse := func(text string) ([]ftbRelease, map[string]time.Duration) {
		installs := map[string]time.Duration{}
		rels, err := ftbParse("fixture.yaml", []byte(text), installs)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		return rels, installs
	}
	findingsOf := func(text string, needs map[string]string) map[string][]string {
		rels, installs := parse(text)
		return ftbFindings(rels, installs, needs)
	}
	if f := findingsOf(good, needs); len(f) != 0 {
		t.Fatalf("the good kit has findings: %v", f)
	}
	cases := []struct{ name, from, to, rule string }{
		{"B1 a stated T that is not the install timeout", "timeoutSeconds: 900", "timeoutSeconds: 600", "B1"},
		{"B1 no T under a longer install timeout", "    functionTest:\n      timeoutSeconds: 900\n", "    functionTest: {}\n", "B1"},
		{"B2 a test timeout that is not the install timeout", "    enable: true\n    timeout: 15m", "    enable: true\n    timeout: 5m", "B2"},
		{"B2 the test timeout left at Flux's default", "    enable: true\n    timeout: 15m", "    enable: true", "B2"},
		{"B3 an install timeout shorter than the needed release's", "  install:\n    timeout: 15m\n  values:", "  install:\n    timeout: 10m\n  values:", "B3"},
		{"B4 T stated as text", "timeoutSeconds: 900", "timeoutSeconds: \"900\"", "B4"},
	}
	for _, c := range cases {
		if strings.Count(good, c.from) != 1 {
			t.Fatalf("%s: the fixture text to change occurs %d times", c.name, strings.Count(good, c.from))
		}
		f := findingsOf(strings.Replace(good, c.from, c.to, 1), needs)
		joined := strings.Join(f["bp-a"], "\n")
		if !strings.Contains(joined, ": "+c.rule+" ") {
			t.Errorf("%s: no %s finding for bp-a; findings: %v", c.name, c.rule, f)
		}
	}
	if f := findingsOf(good, map[string]string{"bp-a": "bp-missing"}); !strings.Contains(strings.Join(f["bp-a"], "\n"), ": B3 ") {
		t.Errorf("B3 a needed release that is not in the kit: no finding; findings: %v", f)
	}

	// The owed list: a breaking release on it is reported, one off it fails,
	// one on it that breaks nothing fails, and a name outside the kit fails.
	broken, brokenInstalls := parse(strings.Replace(good, "timeoutSeconds: 900", "timeoutSeconds: 600", 1))
	findings := ftbFindings(broken, brokenInstalls, needs)
	if p, r := ftbOwed(broken, findings, map[string]bool{"bp-a": true}); len(p) != 0 || len(r) != 1 {
		t.Errorf("an owed release that breaks a rule: problems %v, reported %v; want none and one", p, r)
	}
	if p, _ := ftbOwed(broken, findings, map[string]bool{}); len(p) != 1 {
		t.Errorf("a release off the list that breaks a rule: problems %v; want one", p)
	}
	goodRels, _ := parse(good)
	if p, _ := ftbOwed(goodRels, findingsOf(good, needs), map[string]bool{"bp-a": true}); len(p) != 1 || !strings.Contains(p[0], "breaks no rule") {
		t.Errorf("an owed release that breaks nothing: problems %v; want one that says so", p)
	}
	if p, _ := ftbOwed(goodRels, findingsOf(good, needs), map[string]bool{"bp-gone": true}); len(p) != 1 || !strings.Contains(p[0], "not a release of the kit") {
		t.Errorf("an owed name outside the kit: problems %v; want one that says so", p)
	}

	// B5 on renders: the deadline T passes; 300 under T 900 and no test Pod fail.
	pod := func(deadline int) map[string]any {
		return map[string]any{"kind": "Pod", "metadata": map[string]any{"labels": map[string]any{"catalyst.openova.io/function-test": "true"}},
			"spec": map[string]any{"activeDeadlineSeconds": deadline}}
	}
	if p := ftbPodDeadline([]map[string]any{pod(900)}, 900*time.Second); p != "" {
		t.Errorf("B5 a Pod with the deadline T: %s", p)
	}
	if p := ftbPodDeadline([]map[string]any{pod(300)}, 900*time.Second); !strings.HasPrefix(p, "B5 ") {
		t.Errorf("B5 a Pod that ends at 300 seconds under T 900: no finding")
	}
	if p := ftbPodDeadline(nil, 900*time.Second); !strings.HasPrefix(p, "B5 ") {
		t.Errorf("B5 a render without a test Pod: no finding")
	}
	if got := ftbSubstitute("a: ${X:=1}\nb: ${Y:-x.${Z}}\nc: ${W}\nd: ${V=on}\n"); got != "a: 1\nb: x.\nc: \nd: on\n" {
		t.Errorf("the substitution gives %q", got)
	}

	// B5 stand-ins: every valuesFrom targetPath is read, an unset target gets
	// the stand-in, a value the release sets wins, the release's values are not
	// changed, and a path that is not plain dotted is refused.
	from, _ := parse(strings.Replace(good, "  values:\n", "  valuesFrom:\n    - kind: ConfigMap\n      name: profile\n      valuesKey: stanza\n      targetPath: seal.stanza\n    - kind: ConfigMap\n      name: whole\n      valuesKey: values.yaml\n  values:\n", 1))
	if len(from) != 2 || len(from[0].StandIns) != 1 || from[0].StandIns[0] != "seal.stanza" {
		t.Fatalf("B5 the valuesFrom targetPath of bp-a is not read: %+v", from)
	}
	vals := map[string]any{"functionTest": map[string]any{"timeoutSeconds": 900}, "a": map[string]any{"b": "kept"}}
	got, err := ftbWithStandIns(vals, []string{"seal.stanza", "a.b", "a.c"})
	switch {
	case err != nil:
		t.Errorf("B5 stand-ins: %v", err)
	case ftbDig(got, "seal", "stanza") != ftbStandIn || ftbDig(got, "a", "c") != ftbStandIn:
		t.Errorf("B5 an unset valuesFrom target gets no stand-in: %v", got)
	case ftbDig(got, "a", "b") != "kept" || ftbDig(got, "functionTest", "timeoutSeconds") != 900:
		t.Errorf("B5 the stand-in replaces a value the release sets: %v", got)
	case ftbDig(vals, "a", "c") != nil || ftbDig(vals, "seal") != nil:
		t.Errorf("B5 the stand-ins change the release's own values: %v", vals)
	}
	for _, bad := range []string{"list[0].x", "a\\.b", "a..b", "a.b.c"} {
		if _, err := ftbWithStandIns(vals, []string{bad}); err == nil || !strings.HasPrefix(err.Error(), "B5 ") {
			t.Errorf("B5 the targetPath %q is not refused: %v", bad, err)
		}
	}

	// B5 bare variables: a value that is only a variable without a default is
	// left out at any depth, a variable with a default or inside a text stays,
	// and the release's values are not changed.
	withVars := map[string]any{"parentZones": "${PARENT_DOMAINS_YAML}", "keep": "${X:=1}", "host": "console.${FQDN}",
		"global": map[string]any{"sovereignFQDN": "${FQDN}", "n": 2}}
	bare := ftbWithoutBareVariables(withVars)
	switch _, zones := bare["parentZones"]; {
	case zones || ftbDig(bare, "global", "sovereignFQDN") != nil:
		t.Errorf("B5 a value that is only a variable without a default is kept: %v", bare)
	case bare["keep"] != "${X:=1}" || bare["host"] != "console.${FQDN}" || ftbDig(bare, "global", "n") != 2:
		t.Errorf("B5 a value that is not only a variable without a default is left out: %v", bare)
	case withVars["parentZones"] != "${PARENT_DOMAINS_YAML}" || ftbDig(withVars, "global", "sovereignFQDN") != "${FQDN}":
		t.Errorf("B5 leaving out the bare variables changes the release's own values: %v", withVars)
	}
}
