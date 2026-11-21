// Command release-own-tree holds that a job of .github/workflows/blueprint-release.yaml
// judges the tree of its own commit: what an earlier step of the job left in the
// tree, and what main gained after the commit, never decide whether it passes.
//
// The dependency build (run 44837071750, 21 Nov 2026). A release started by hand
// for platform/cert-manager-powerdns-webhook failed at the step "Helm dependency
// build": `Error: no repository definition for https://sigstore.github.io/helm-charts`.
// In a dispatched run the release writer ("Release writer: bump the chart
// version") packages the chart to compare it with the version the registry holds,
// and its `helm dependency build` leaves a Chart.lock in the tree. With a lock
// Helm takes its build path, which refuses an http(s) repository nobody added;
// without one (every push run) it fetches from the URL itself. Checks:
//
//	workflow  every `helm dependency build` of the workflow is in one of those two
//	          steps, after the step sources scripts/lib/helm-dep-repos.sh and calls
//	          add_dep_repos; add_dep_repos is defined once in the tree, in that file;
//	          the step "Helm dependency build" passes on a fixture chart with one
//	          https dependency served by a local stand-in (no network) and an empty
//	          repository list, on a tree without a lock and on a tree an earlier
//	          `helm dependency build` left its lock in; and the dispatched route
//	          passes as a job runs it: the release writer (the registry stand-in
//	          holds the version with other files), then "Helm dependency build".
//	coverage  for every chart of platform/*/chart and products/*/chart, each http(s)
//	          repository Helm lists for its dependencies (`helm dependency list`) is
//	          one that add_dep_repos adds, and no dependency names a repository by
//	          name (@name, alias:name), which nothing adds.
//
// The chart tests (run 44832081080, commit 7dff3b89f). The release writer fetches
// origin/main; tests/step6490-secondary-gitea-mirror.sh of
// platform/self-sovereign-cutover compared the chart with that tip, which already
// held #8549, and failed on #8549's change. Checks:
//
//	charttests  no chart test (platform/*/chart/tests/*.sh, products/*/chart/tests/*.sh)
//	            reads a tree at origin/main with git on one line (archive, show,
//	            diff, ls-tree, cat-file, checkout, switch, restore, read-tree,
//	            worktree); a test that needs a second tree takes
//	            `git merge-base HEAD origin/main`. And step6490 runs in fixture
//	            repositories built from the chart: a release checkout (depth 1) of
//	            a commit after main moved on with a change of the single-region
//	            render, and a pull request's merge ref after main moved on the same
//	            way, must pass with the comparison made; a merge ref whose own change
//	            alters that render must fail.
//
// Not seen: a tree named through a variable (`ref=origin/main; git archive "$ref"`).
//
// It needs bash, git, tar, yq, the python3 the release writer and step6490 call,
// and the Helm that blueprint-release.yaml installs (another version stops it).
//
// Exit 0: every check holds. Exit 1: findings. Exit 2: could not be prepared.
package main

import (
	"bytes"
	"crypto/tls"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	workflowRel  = ".github/workflows/blueprint-release.yaml"
	libRel       = "scripts/lib/helm-dep-repos.sh"
	writerStep   = "Release writer: bump the chart version"
	depBuildStep = "Helm dependency build"
	cutoverChart = "platform/self-sovereign-cutover/chart"
	cutoverTest  = "tests/step6490-secondary-gitea-mirror.sh"
	// The pass line of step6490's comparison: without it the comparison was skipped.
	comparedLine = "the ENTIRE single-region render is byte-identical"
	differsLine  = "single-region render DIFFERS"
)

// Files of the repository the release writer runs, copied into the fixture tree.
var writerFiles = []string{libRel, "scripts/chart-package-diff.py", "scripts/lib/version_order.py"}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

type options struct {
	root, workflow, scratch, chartTest string
	checks                             map[string]bool
}

func run(args []string, out, errw io.Writer) int {
	fs := flag.NewFlagSet("release-own-tree", flag.ContinueOnError)
	fs.SetOutput(errw)
	workflow := fs.String("workflow", "", "the workflow file (default: "+workflowRel+" of the repository)")
	scratch := fs.String("scratch", "", "the folder for the fixtures (default: a new temporary folder)")
	keep := fs.Bool("keep", false, "keep the scratch folder")
	chartTest := fs.String("chart-test", "", "the file run as "+cutoverChart+"/"+cutoverTest+" in the fixtures (default: the repository's)")
	only := fs.String("checks", "workflow,coverage,charttests", "the checks to run, comma-separated")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	root, err := git("", nil, "rev-parse", "--show-toplevel")
	if err != nil {
		say(errw, "release-own-tree: %v\n", err)
		return 2
	}
	o := options{root: strings.TrimSpace(root), workflow: *workflow, chartTest: *chartTest, checks: map[string]bool{}}
	for _, c := range strings.Split(*only, ",") {
		switch c = strings.TrimSpace(c); c {
		case "workflow", "coverage", "charttests":
			o.checks[c] = true
		default:
			say(errw, "release-own-tree: unknown check %q\n", c)
			return 2
		}
	}
	if o.workflow == "" {
		o.workflow = filepath.Join(o.root, workflowRel)
	}
	if o.chartTest == "" {
		o.chartTest = filepath.Join(o.root, cutoverChart, cutoverTest)
	}
	o.scratch = *scratch
	if o.scratch == "" {
		if o.scratch, err = os.MkdirTemp("", "release-own-tree-"); err != nil {
			say(errw, "release-own-tree: %v\n", err)
			return 2
		}
	} else if err := os.MkdirAll(o.scratch, 0o750); err != nil {
		say(errw, "release-own-tree: %v\n", err)
		return 2
	}
	if !*keep {
		defer func() { _ = os.RemoveAll(o.scratch) }()
	}
	findings, err := runChecks(o, out)
	if err != nil {
		say(errw, "release-own-tree: %v\n", err)
		return 2
	}
	for _, f := range findings {
		say(out, "FAIL %s\n", f)
	}
	say(out, "release-own-tree: %d finding(s)\n", len(findings))
	if len(findings) > 0 {
		say(out, "release-own-tree: a release job judges the tree of its own commit; neither what an earlier step left nor what main gained later may decide it.\n")
		return 1
	}
	return 0
}

func runChecks(o options, out io.Writer) ([]string, error) {
	text, err := os.ReadFile(filepath.Clean(o.workflow))
	if err != nil {
		return nil, err
	}
	var findings []string
	if o.checks["workflow"] || o.checks["coverage"] {
		if err := helmIsTheRelease(string(text)); err != nil {
			return nil, err
		}
	}
	if o.checks["workflow"] {
		f, err := checkWorkflow(o, string(text), out)
		if err != nil {
			return nil, err
		}
		findings = append(findings, f...)
	}
	if o.checks["coverage"] {
		f, err := checkCoverage(o.root, filepath.Join(o.scratch, "coverage"), out)
		if err != nil {
			return nil, err
		}
		findings = append(findings, f...)
	}
	if o.checks["charttests"] {
		f, err := checkChartTests(o, out)
		if err != nil {
			return nil, err
		}
		findings = append(findings, f...)
	}
	return findings, nil
}

// helmIsTheRelease stops the command when the helm on PATH is not the version
// the build job installs: the two paths of `helm dependency build` are Helm's.
func helmIsTheRelease(workflow string) error {
	want := ""
	lines := strings.Split(workflow, "\n")
	for i, l := range lines {
		if !strings.Contains(l, "azure/setup-helm@") {
			continue
		}
		for j := i + 1; j < len(lines) && j <= i+4; j++ {
			if v, ok := strings.CutPrefix(strings.TrimSpace(lines[j]), "version: "); ok {
				want = strings.Trim(v, `"'`)
				break
			}
		}
		break
	}
	if want == "" {
		return errors.New("the workflow names no Helm version after azure/setup-helm")
	}
	got, err := command("", nil, "helm", "version", "--template", "{{.Version}}")
	if err != nil {
		return err
	}
	if got = strings.TrimSpace(got); got != want {
		return fmt.Errorf("helm on PATH is %s; the release builds with %s, whose `helm dependency build` this command measures", got, want)
	}
	return nil
}

// ── check workflow ────────────────────────────────────────────────────────────

func checkWorkflow(o options, text string, out io.Writer) ([]string, error) {
	var findings []string
	writer, err := extractStep(text, writerStep)
	if err != nil {
		return nil, err
	}
	dep, err := extractStep(text, depBuildStep)
	if err != nil {
		return nil, err
	}
	findings = append(findings, buildsAfterRepos(text, map[string]step{writerStep: writer, depBuildStep: dep})...)
	f, err := oneDefinition(o.root)
	if err != nil {
		return nil, err
	}
	findings = append(findings, f...)

	dir := filepath.Join(o.scratch, "workflow")
	fx, err := newFixture(o.root, dir)
	if err != nil {
		return nil, err
	}
	defer fx.close()

	// A tree without a lock: the push route.
	tree, err := fx.tree("no-lock")
	if err != nil {
		return nil, err
	}
	findings = append(findings, fx.depBuild(dep, tree, "a tree without a Chart.lock (a push run)", out)...)

	// A tree an earlier dependency build left its lock in.
	tree, err = fx.tree("left-lock")
	if err != nil {
		return nil, err
	}
	if res := fx.runIn(tree, tree, nil, "", "helm", "dependency", "build", fxChart+"/chart"); res.err != nil {
		return nil, fmt.Errorf("the earlier `helm dependency build` of the fixture failed: %v\n%s", res.err, res.out)
	}
	if !exists(filepath.Join(tree, fxChart, "chart", "Chart.lock")) {
		return nil, errors.New("the earlier `helm dependency build` of the fixture left no Chart.lock: the case would prove nothing")
	}
	findings = append(findings, fx.depBuild(dep, tree, "a tree an earlier `helm dependency build` left its Chart.lock in", out)...)

	// The dispatched route as one job runs it.
	tree, err = fx.gitTree("dispatched")
	if err != nil {
		return nil, err
	}
	head, err := git(tree, fx.env(tree), "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	vals := map[string]string{"CHART_PATH": fxChart, "PUSH_BEFORE": "", "PUSH_AFTER": strings.TrimSpace(head)}
	res, outputs, err := fx.runStep(writer, tree, vals, true)
	if err != nil {
		return nil, err
	}
	label := "the dispatched route (the registry holds bp-fx 1.0.0 with other files)"
	switch {
	case res.err != nil:
		findings = append(findings, fmt.Sprintf("workflow: %s: the step %q failed: %v\n%s", label, writerStep, res.err, tail(res.out, 20)))
	case !strings.Contains(outputs, "bumped=true"):
		findings = append(findings, fmt.Sprintf("workflow: %s: the step %q did not write the next version (outputs: %q)\n%s", label, writerStep, outputs, tail(res.out, 20)))
	default:
		say(out, "ok   workflow: %s: the release writer wrote the next version; it left a Chart.lock: %v\n", label, exists(filepath.Join(tree, fxChart, "chart", "Chart.lock")))
		findings = append(findings, fx.depBuild(dep, tree, label+", after the release writer", out)...)
	}
	return findings, nil
}

// buildsAfterRepos: every `helm dependency build` line of the workflow is in one
// of the steps run here, after that step sources the library and calls add_dep_repos.
func buildsAfterRepos(text string, steps map[string]step) []string {
	var findings []string
	total := 0
	for _, l := range strings.Split(text, "\n") {
		if t := strings.TrimSpace(l); !strings.HasPrefix(t, "#") && strings.Contains(t, "helm dependency build") && !strings.HasPrefix(t, "echo ") {
			total++
		}
	}
	inSteps := 0
	names := make([]string, 0, len(steps))
	for n := range steps {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		sourced, added := false, false
		for _, l := range strings.Split(steps[name].body, "\n") {
			t := strings.TrimSpace(l)
			switch {
			case strings.HasPrefix(t, "#") || strings.HasPrefix(t, "echo "):
			case strings.HasPrefix(t, ". "+libRel) || strings.HasPrefix(t, "source "+libRel):
				sourced = true
			case strings.HasPrefix(t, "add_dep_repos "):
				added = sourced
			case strings.Contains(t, "helm dependency build"):
				inSteps++
				if !added {
					findings = append(findings, fmt.Sprintf("workflow: the step %q runs `%s` before it sources %s and calls add_dep_repos: with a Chart.lock in the tree Helm refuses every http(s) repository nobody added", name, t, libRel))
				}
			}
		}
	}
	if total != inSteps {
		findings = append(findings, fmt.Sprintf("workflow: %d line(s) run `helm dependency build`, %d of them in the steps %q and %q that this command runs: run every dependency build of a release after add_dep_repos and add its step here", total, inSteps, writerStep, depBuildStep))
	}
	return findings
}

var defRe = regexp.MustCompile(`^\s*(function\s+)?add_dep_repos\s*\(\)`)

// oneDefinition: add_dep_repos is defined once in the tracked tree, in the library.
func oneDefinition(root string) ([]string, error) {
	files, err := lsFiles(root, "*.sh", "*.bash", "*.yaml", "*.yml")
	if err != nil {
		return nil, err
	}
	var at []string
	for _, f := range files {
		b, err := os.ReadFile(filepath.Clean(filepath.Join(root, f)))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		for i, l := range strings.Split(string(b), "\n") {
			if defRe.MatchString(l) {
				at = append(at, fmt.Sprintf("%s:%d", f, i+1))
			}
		}
	}
	if len(at) == 1 && strings.HasPrefix(at[0], libRel+":") {
		return nil, nil
	}
	return []string{fmt.Sprintf("workflow: add_dep_repos must be defined once, in %s; it is defined at %v", libRel, at)}, nil
}

// ── the fixture of the dependency build ───────────────────────────────────────

const (
	fxChart = "platform/fx"
	fxLib   = "platform/fx-lib"
)

type fixture struct {
	root, dir, repoURL, certFile, published, realHelm, stubDir string
	srv                                                        *httptest.Server
}

// newFixture serves one chart repository over https on 127.0.0.1 and builds the
// package the registry stand-in holds.
func newFixture(root, dir string) (*fixture, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	served := filepath.Join(dir, "served")
	if err := os.MkdirAll(filepath.Join(served, "helm-charts"), 0o750); err != nil {
		return nil, err
	}
	fx := &fixture{root: root, dir: dir}
	fx.srv = httptest.NewUnstartedServer(http.FileServer(http.Dir(served)))
	fx.srv.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
	fx.srv.StartTLS()
	fx.repoURL = fx.srv.URL + "/helm-charts"
	fx.certFile = filepath.Join(dir, "stand-in.pem")
	if err := os.WriteFile(fx.certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: fx.srv.Certificate().Raw}), 0o600); err != nil {
		fx.close()
		return nil, err
	}
	src := filepath.Join(dir, "src")
	if err := writeFiles(src, map[string]string{
		"common/Chart.yaml":            "apiVersion: v2\nname: common\nversion: 0.1.3\ntype: library\n",
		"common/templates/_common.tpl": "{{- define \"common.name\" -}}common{{- end -}}\n",
		"published/Chart.yaml":         "apiVersion: v2\nname: bp-fx\nversion: 1.0.0\n",
		"published/templates/cm.yaml":  "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: fx-published-with-other-files\n",
	}); err != nil {
		fx.close()
		return nil, err
	}
	steps := [][]string{
		{"helm", "package", filepath.Join(src, "common"), "--destination", filepath.Join(served, "helm-charts")},
		{"helm", "repo", "index", filepath.Join(served, "helm-charts"), "--url", fx.repoURL},
		{"helm", "package", filepath.Join(src, "published"), "--destination", filepath.Join(dir, "registry")},
	}
	if err := os.MkdirAll(filepath.Join(dir, "registry"), 0o750); err != nil {
		fx.close()
		return nil, err
	}
	for _, s := range steps {
		if res := fx.runIn(dir, src, nil, "", s[0], s[1:]...); res.err != nil {
			fx.close()
			return nil, fmt.Errorf("%s: %v\n%s", strings.Join(s, " "), res.err, res.out)
		}
	}
	fx.published = filepath.Join(dir, "registry", "bp-fx-1.0.0.tgz")
	// The registry stand-in: `helm pull` hands out the published package, every
	// other command goes to the real helm.
	fx.stubDir = filepath.Join(dir, "registry-stand-in")
	real, err := exec.LookPath("helm")
	if err != nil {
		fx.close()
		return nil, err
	}
	stub := "#!/bin/sh\nif [ \"$1\" = pull ]; then\n  dest=\"\"; prev=\"\"\n  for a in \"$@\"; do [ \"$prev\" = --destination ] && dest=\"$a\"; prev=\"$a\"; done\n" +
		"  cp \"$FX_PUBLISHED\" \"$dest/\" || exit 1\n  echo \"Pulled: $2\"\n  exit 0\nfi\nexec \"$FX_REAL_HELM\" \"$@\"\n"
	if err := os.MkdirAll(fx.stubDir, 0o750); err != nil {
		fx.close()
		return nil, err
	}
	if err := writeExec(filepath.Join(fx.stubDir, "helm"), stub); err != nil {
		fx.close()
		return nil, err
	}
	fx.realHelm = real
	return fx, nil
}

func (fx *fixture) close() { fx.srv.Close() }

// tree writes the fixture tree: platform/fx/chart with a file:// library and one
// https dependency, and the files of the repository the release writer runs.
func (fx *fixture) tree(name string) (string, error) {
	tree := filepath.Join(fx.dir, name)
	files := map[string]string{
		fxChart + "/chart/Chart.yaml": "apiVersion: v2\nname: bp-fx\nversion: 1.0.0\ndependencies:\n" +
			"  - name: fx-lib\n    version: 0.1.0\n    repository: file://../../fx-lib/chart\n" +
			"  - name: common\n    version: 0.1.3\n    repository: " + fx.repoURL + "\n",
		fxChart + "/chart/values.yaml":       "name: fx\n",
		fxChart + "/chart/templates/cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ include \"common.name\" . }}-fx\n",
		fxLib + "/chart/Chart.yaml":          "apiVersion: v2\nname: fx-lib\nversion: 0.1.0\ntype: library\n",
		fxLib + "/chart/templates/_lib.tpl":  "{{- define \"fx-lib.name\" -}}fx{{- end -}}\n",
		".gitignore":                         "platform/*/chart/charts/\nplatform/*/chart/Chart.lock\n",
	}
	for _, f := range writerFiles {
		b, err := os.ReadFile(filepath.Clean(filepath.Join(fx.root, f)))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", err
		}
		files[f] = string(b)
	}
	return tree, writeFiles(tree, files)
}

// gitTree is tree as the checkout of a job: a commit of main in a clone of a bare origin.
func (fx *fixture) gitTree(name string) (string, error) {
	tree, err := fx.tree(name)
	if err != nil {
		return "", err
	}
	origin := tree + ".git"
	env := fx.env(tree)
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"add", "-A"},
		{"commit", "-q", "-m", "fixture"},
		{"init", "-q", "--bare", "-b", "main", origin},
		{"remote", "add", "origin", origin},
		{"push", "-q", "origin", "main"},
	} {
		if _, err := git(tree, env, args...); err != nil {
			return "", err
		}
	}
	return tree, nil
}

// env is the environment of a job of the fixture: its own empty Helm home (no
// repository added), the stand-in's certificate as the only trusted one, no git
// configuration of the host.
func (fx *fixture) env(tree string) []string {
	home := tree + ".helm"
	return append(baseEnv(),
		"HELM_CONFIG_HOME="+filepath.Join(home, "config"),
		"HELM_CACHE_HOME="+filepath.Join(home, "cache"),
		"HELM_DATA_HOME="+filepath.Join(home, "data"),
		"SSL_CERT_FILE="+fx.certFile,
		"FX_PUBLISHED="+fx.published,
		"FX_REAL_HELM="+fx.realHelm,
	)
}

type result struct {
	out string
	err error
}

// runIn runs helm or bash in dir with the Helm home of the case home.
func (fx *fixture) runIn(dir, home string, extra []string, pathFirst string, name string, args ...string) result {
	env := append(fx.env(home), extra...)
	if pathFirst != "" {
		env = append(env, "PATH="+pathFirst+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	var cmd *exec.Cmd
	switch name {
	case "helm":
		cmd = exec.Command("helm", args...)
	case "bash":
		cmd = exec.Command("bash", args...)
	default:
		return result{err: fmt.Errorf("runIn: %s is not run here", name)}
	}
	cmd.Dir = dir
	cmd.Env = env
	b, err := cmd.CombinedOutput()
	return result{out: string(b), err: err}
}

// runStep runs a step's script the way the runner does (bash -eo pipefail) in
// tree, with the step's env and a GITHUB_OUTPUT file; it returns what the step
// wrote there. stub puts the registry stand-in first on PATH.
func (fx *fixture) runStep(st step, tree string, vals map[string]string, stub bool) (result, string, error) {
	env, err := st.envFor(vals)
	if err != nil {
		return result{}, "", err
	}
	script := filepath.Join(filepath.Dir(tree), filepath.Base(tree)+"."+slug(st.name)+".sh")
	if err := os.WriteFile(script, []byte(st.resolve(vals)), 0o600); err != nil {
		return result{}, "", err
	}
	outputs := script + ".output"
	if err := os.WriteFile(outputs, nil, 0o600); err != nil {
		return result{}, "", err
	}
	env = append(env, "GITHUB_OUTPUT="+outputs, "GITHUB_STEP_SUMMARY="+script+".summary")
	first := ""
	if stub {
		first = fx.stubDir
	}
	res := fx.runIn(tree, tree, env, first, "bash", "--noprofile", "--norc", "-eo", "pipefail", script)
	b, err := os.ReadFile(filepath.Clean(outputs))
	if err != nil {
		return result{}, "", err
	}
	return res, string(b), nil
}

// depBuild runs the step "Helm dependency build" in tree and checks that it
// passed and that the https dependency is in the tree.
func (fx *fixture) depBuild(dep step, tree, label string, out io.Writer) []string {
	res, _, err := fx.runStep(dep, tree, map[string]string{"CHART_PATH": fxChart}, false)
	if err != nil {
		return []string{fmt.Sprintf("workflow: %s: %v", label, err)}
	}
	if res.err != nil {
		return []string{fmt.Sprintf("workflow: %s: the step %q failed (%v):\n%s", label, depBuildStep, res.err, tail(res.out, 12))}
	}
	if !exists(filepath.Join(tree, fxChart, "chart", "charts", "common-0.1.3.tgz")) {
		return []string{fmt.Sprintf("workflow: %s: the step %q passed and the https dependency common-0.1.3.tgz is not in the tree", label, depBuildStep)}
	}
	say(out, "ok   workflow: %s: %q built the https dependency\n", label, depBuildStep)
	return nil
}

// ── check coverage ────────────────────────────────────────────────────────────

// checkCoverage walks every chart: the http(s) repositories Helm lists for its
// dependencies are the ones add_dep_repos adds (a helm stand-in records them).
func checkCoverage(root, dir string, out io.Writer) ([]string, error) {
	if err := os.MkdirAll(filepath.Join(dir, "stub"), 0o750); err != nil {
		return nil, err
	}
	logFile := filepath.Join(dir, "added")
	stub := "#!/bin/sh\nif [ \"$1 $2\" = \"repo add\" ]; then\n  for a in \"$@\"; do u=\"$a\"; done\n  printf '%s\\n' \"$u\" >> \"$ADD_LOG\"\nfi\nexit 0\n"
	if err := writeExec(filepath.Join(dir, "stub", "helm"), stub); err != nil {
		return nil, err
	}
	lib := filepath.Join(root, libRel)
	if !exists(lib) {
		return []string{fmt.Sprintf("coverage: %s does not exist: nothing adds the repositories a release's dependency build needs", libRel)}, nil
	}
	charts, err := lsFiles(root, "platform/*/chart/Chart.yaml", "products/*/chart/Chart.yaml")
	if err != nil {
		return nil, err
	}
	helmEnv := append(baseEnv(),
		"HELM_CONFIG_HOME="+filepath.Join(dir, "helm", "config"),
		"HELM_CACHE_HOME="+filepath.Join(dir, "helm", "cache"),
		"HELM_DATA_HOME="+filepath.Join(dir, "helm", "data"))
	var findings []string
	withHTTP, repos := 0, 0
	for _, cy := range charts {
		chartDir := filepath.Dir(cy)
		listed, err := command(root, helmEnv, "helm", "dependency", "list", chartDir)
		if err != nil {
			findings = append(findings, fmt.Sprintf("coverage: %s: helm cannot list its dependencies: %v", chartDir, err))
			continue
		}
		need := map[string]bool{}
		for _, r := range listedRepositories(listed) {
			switch {
			case r == "" || strings.HasPrefix(r, "file://") || strings.HasPrefix(r, "oci://"):
			case strings.HasPrefix(r, "http://") || strings.HasPrefix(r, "https://"):
				need[strings.TrimSuffix(r, "/")] = true
			default:
				findings = append(findings, fmt.Sprintf("coverage: %s: a dependency names the repository %q, which a release adds nowhere: give its URL", chartDir, r))
			}
		}
		if len(need) == 0 {
			continue
		}
		withHTTP++
		repos += len(need)
		if err := os.WriteFile(logFile, nil, 0o600); err != nil {
			return nil, err
		}
		env := append(baseEnv(), "ADD_LOG="+logFile, "PATH="+filepath.Join(dir, "stub")+string(os.PathListSeparator)+os.Getenv("PATH"))
		if _, err := command(root, env, "bash", "--noprofile", "--norc", "-c", `. "$1"; add_dep_repos "$2"`, "add_dep_repos", lib, chartDir); err != nil {
			findings = append(findings, fmt.Sprintf("coverage: %s: add_dep_repos failed: %v", chartDir, err))
			continue
		}
		b, err := os.ReadFile(filepath.Clean(logFile))
		if err != nil {
			return nil, err
		}
		added := map[string]bool{}
		for _, u := range strings.Fields(string(b)) {
			added[strings.TrimSuffix(u, "/")] = true
		}
		for _, u := range sortedKeys(need) {
			if !added[u] {
				findings = append(findings, fmt.Sprintf("coverage: %s: Helm lists the dependency repository %s, and add_dep_repos does not add it (it added %v): the release's dependency build of this chart fails on a Chart.lock", chartDir, u, sortedKeys(added)))
			}
		}
	}
	if withHTTP == 0 {
		findings = append(findings, "coverage: no chart of the tree has an http(s) dependency: the walk measured nothing")
	}
	say(out, "ok   coverage: %d charts walked, %d with an http(s) dependency (%d repositories) checked against add_dep_repos\n", len(charts), withHTTP, repos)
	return findings, nil
}

// listedRepositories reads the REPOSITORY column of `helm dependency list`.
func listedRepositories(listed string) []string {
	var out []string
	for _, l := range strings.Split(listed, "\n") {
		cols := strings.Split(l, "\t")
		if len(cols) < 4 || strings.TrimSpace(cols[0]) == "NAME" {
			continue
		}
		out = append(out, strings.TrimSpace(cols[2]))
	}
	return out
}

// ── check charttests ──────────────────────────────────────────────────────────

var tipRe = regexp.MustCompile(`(^|[^A-Za-z0-9_./-])git\s.*\b(archive|show|diff|ls-tree|cat-file|checkout|switch|restore|read-tree|worktree)\b.*\borigin/main\b`)

func checkChartTests(o options, out io.Writer) ([]string, error) {
	tests, err := lsFiles(o.root, "platform/*/chart/tests/*.sh", "products/*/chart/tests/*.sh")
	if err != nil {
		return nil, err
	}
	var findings []string
	for _, f := range tests {
		b, err := os.ReadFile(filepath.Clean(filepath.Join(o.root, f)))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		findings = append(findings, tipReads(f, string(b))...)
	}
	say(out, "ok   charttests: %d chart tests read for a tree taken at origin/main's tip\n", len(tests))
	f, err := runCutoverShapes(o, filepath.Join(o.scratch, "charttests"), out)
	if err != nil {
		return nil, err
	}
	return append(findings, f...), nil
}

// tipReads names each line of a chart test that reads a tree at origin/main.
func tipReads(name, text string) []string {
	var findings []string
	for i, l := range strings.Split(text, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "#") || strings.Contains(t, "merge-base") || !tipRe.MatchString(t) {
			continue
		}
		findings = append(findings, fmt.Sprintf("charttests: %s:%d reads a tree at origin/main, whose tip can hold commits the tree under test does not: take `git merge-base HEAD origin/main`: %s", name, i+1, t))
	}
	return findings
}

// runCutoverShapes runs step6490 in three fixture repositories built from the chart.
func runCutoverShapes(o options, dir string, out io.Writer) ([]string, error) {
	files, err := lsFiles(o.root, cutoverChart)
	if err != nil {
		return nil, err
	}
	content := map[string]string{}
	for _, f := range files {
		b, err := os.ReadFile(filepath.Clean(filepath.Join(o.root, f)))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		content[f] = string(b)
	}
	b, err := os.ReadFile(filepath.Clean(o.chartTest))
	if err != nil {
		return nil, err
	}
	content[cutoverChart+"/"+cutoverTest] = string(b)
	newer := map[string]string{cutoverChart + "/templates/zz-newer-main.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a-change-main-gained-later\n"}
	unrendered := map[string]string{cutoverChart + "/README.md": content[cutoverChart+"/README.md"] + "\nA line of the pull request.\n"}
	var findings []string

	// 1. A release run for X: a depth-1 checkout of X, then main moves on with a
	// change of the single-region render, the release writer fetches origin/main
	// and bumps the version in the tree.
	origin, seed, err := seedOrigin(filepath.Join(dir, "release"), content)
	if err != nil {
		return nil, err
	}
	clone := filepath.Join(dir, "release", "job")
	if _, err := git("", baseEnv(), "clone", "-q", "--depth", "1", "--branch", "main", "file://"+origin, clone); err != nil {
		return nil, err
	}
	if err := commitOn(seed, newer, "main gains a change of the single-region render"); err != nil {
		return nil, err
	}
	if _, err := git(seed, baseEnv(), "push", "-q", "origin", "main"); err != nil {
		return nil, err
	}
	if _, err := git(clone, baseEnv(), "fetch", "-q", "origin", "main"); err != nil {
		return nil, err
	}
	if err := bumpVersion(filepath.Join(clone, cutoverChart, "Chart.yaml")); err != nil {
		return nil, err
	}
	findings = append(findings, shape(clone, "a release run of X after main moved on", true, out)...)

	// 2. A pull request's merge ref, then main moves on the same way.
	origin, seed, err = seedOrigin(filepath.Join(dir, "pull-request"), content)
	if err != nil {
		return nil, err
	}
	if err := mergeRef(seed, unrendered, "refs/pull/1/merge"); err != nil {
		return nil, err
	}
	if err := commitOn(seed, newer, "main gains a change of the single-region render"); err != nil {
		return nil, err
	}
	if _, err := git(seed, baseEnv(), "push", "-q", "origin", "main"); err != nil {
		return nil, err
	}
	clone, err = checkoutRef(origin, filepath.Join(dir, "pull-request", "job"), "refs/pull/1/merge")
	if err != nil {
		return nil, err
	}
	findings = append(findings, shape(clone, "a pull request's merge ref after main moved on", true, out)...)

	// 3. A merge ref whose own change alters the single-region render.
	origin, seed, err = seedOrigin(filepath.Join(dir, "own-change"), content)
	if err != nil {
		return nil, err
	}
	if err := mergeRef(seed, newer, "refs/pull/2/merge"); err != nil {
		return nil, err
	}
	clone, err = checkoutRef(origin, filepath.Join(dir, "own-change", "job"), "refs/pull/2/merge")
	if err != nil {
		return nil, err
	}
	findings = append(findings, shape(clone, "a pull request whose own change alters the single-region render", false, out)...)
	return findings, nil
}

// shape runs step6490 in clone: pass says whether it must pass with the
// comparison made, or fail on the comparison.
func shape(clone, label string, pass bool, out io.Writer) []string {
	chart := filepath.Join(clone, cutoverChart)
	text, err := combined(clone, baseEnv(), filepath.Join(chart, cutoverTest), chart)
	switch {
	case pass && err != nil:
		return []string{fmt.Sprintf("charttests: %s: %s failed (%v); it must judge the tree of its own commit:\n%s", label, cutoverTest, err, around(text, differsLine, 12))}
	case pass && !strings.Contains(text, comparedLine):
		return []string{fmt.Sprintf("charttests: %s: %s passed without making its comparison (no line %q):\n%s", label, cutoverTest, comparedLine, tail(text, 12))}
	case !pass && (err == nil || !strings.Contains(text, differsLine)):
		return []string{fmt.Sprintf("charttests: %s: %s did not fail on the change (%v); its comparison cannot fail:\n%s", label, cutoverTest, err, tail(text, 12))}
	}
	say(out, "ok   charttests: %s: %s %s\n", label, cutoverTest, map[bool]string{true: "passed with its comparison made", false: "failed on the change"}[pass])
	return nil
}

func seedOrigin(dir string, content map[string]string) (origin, seed string, err error) {
	origin = filepath.Join(dir, "origin.git")
	seed = filepath.Join(dir, "seed")
	if err = writeFiles(seed, content); err != nil {
		return "", "", err
	}
	for _, args := range [][]string{
		{"init", "-q", "--bare", "-b", "main", origin},
		{"-C", seed, "init", "-q", "-b", "main"},
		{"-C", seed, "add", "-A"},
		{"-C", seed, "commit", "-q", "-m", "B"},
		{"-C", seed, "remote", "add", "origin", origin},
		{"-C", seed, "push", "-q", "origin", "main"},
	} {
		if _, err = git("", baseEnv(), args...); err != nil {
			return "", "", err
		}
	}
	return origin, seed, nil
}

func commitOn(repo string, files map[string]string, msg string) error {
	if err := writeFiles(repo, files); err != nil {
		return err
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", msg}} {
		if _, err := git(repo, baseEnv(), args...); err != nil {
			return err
		}
	}
	return nil
}

// mergeRef commits files on a branch from main and pushes its merge into main,
// as GitHub makes a pull request's merge ref, to ref.
func mergeRef(seed string, files map[string]string, ref string) error {
	branch := strings.ReplaceAll(strings.TrimPrefix(ref, "refs/"), "/", "-")
	if _, err := git(seed, baseEnv(), "checkout", "-q", "-b", branch, "main"); err != nil {
		return err
	}
	if err := commitOn(seed, files, "the change of the pull request"); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"checkout", "-q", "--detach", "main"},
		{"merge", "-q", "--no-ff", "-m", "Merge " + branch + " into main", branch},
		{"push", "-q", "origin", "HEAD:" + ref},
		{"checkout", "-q", "main"},
	} {
		if _, err := git(seed, baseEnv(), args...); err != nil {
			return err
		}
	}
	return nil
}

func checkoutRef(origin, clone, ref string) (string, error) {
	for _, s := range []struct {
		dir  string
		args []string
	}{
		{"", []string{"clone", "-q", "file://" + origin, clone}},
		{clone, []string{"fetch", "-q", "origin", ref}},
		{clone, []string{"checkout", "-q", "--detach", "FETCH_HEAD"}},
	} {
		if _, err := git(s.dir, baseEnv(), s.args...); err != nil {
			return "", err
		}
	}
	return clone, nil
}

var versionRe = regexp.MustCompile(`(?m)^version: *"?([0-9]+)\.([0-9]+)\.([0-9]+)"?$`)

// bumpVersion writes the next patch version into Chart.yaml, as the release writer does.
func bumpVersion(chartYAML string) error {
	b, err := os.ReadFile(filepath.Clean(chartYAML))
	if err != nil {
		return err
	}
	m := versionRe.FindSubmatch(b)
	if m == nil {
		return fmt.Errorf("%s has no version line", chartYAML)
	}
	patch := 0
	if _, err := fmt.Sscanf(string(m[3]), "%d", &patch); err != nil {
		return err
	}
	next := fmt.Sprintf("version: %s.%s.%d", m[1], m[2], patch+1)
	return os.WriteFile(filepath.Clean(chartYAML), versionRe.ReplaceAll(b, []byte(next)), 0o600)
}

// ── the steps, read from the workflow ─────────────────────────────────────────

type step struct {
	name string
	body string
	env  []string
}

func (s step) envFor(vals map[string]string) ([]string, error) {
	var out []string
	for _, k := range s.env {
		v, ok := vals[k]
		if !ok {
			return nil, fmt.Errorf("the step %q declares env %s, which this command has no value for", s.name, k)
		}
		out = append(out, k+"="+v)
	}
	return out, nil
}

// resolve gives `${{ matrix.path }}` the chart path, the one expression a run
// of these steps held before it moved into env.
func (s step) resolve(vals map[string]string) string {
	return strings.ReplaceAll(s.body, "${{ matrix.path }}", vals["CHART_PATH"])
}

// extractStep reads the step with the given name from a workflow, by
// indentation, as cmd/release-push reads a step by its id: the keys of its env
// and its run block scalar, dedented as YAML does.
func extractStep(text, name string) (step, error) {
	lines := strings.Split(text, "\n")
	at := -1
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if v, ok := strings.CutPrefix(t, "- name: "); ok && strings.Trim(v, `"'`) == name {
			if at >= 0 {
				return step{}, fmt.Errorf("two steps are named %q (lines %d and %d)", name, at+1, i+1)
			}
			at = i
		}
	}
	if at < 0 {
		return step{}, fmt.Errorf("no step is named %q", name)
	}
	ind := indentOf(lines[at]) + 2
	end := at + 1
	for end < len(lines) && (strings.TrimSpace(lines[end]) == "" || indentOf(lines[end]) >= ind) {
		end++
	}
	st := step{name: name}
	for i := at + 1; i < end; i++ {
		l := lines[i]
		if indentOf(l) != ind {
			continue
		}
		key := strings.TrimSpace(l)
		switch {
		case key == "env:":
			for j := i + 1; j < end && (strings.TrimSpace(lines[j]) == "" || indentOf(lines[j]) > ind); j++ {
				kv := strings.TrimSpace(lines[j])
				if kv == "" || strings.HasPrefix(kv, "#") {
					continue
				}
				k, _, ok := strings.Cut(kv, ":")
				if !ok {
					return step{}, fmt.Errorf("line %d: an env entry without a colon: %q", j+1, kv)
				}
				st.env = append(st.env, strings.TrimSpace(k))
			}
		case key == "run: |" || key == "run: |-" || key == "run: |+":
			var body []string
			cut := -1
			for j := i + 1; j < end && (strings.TrimSpace(lines[j]) == "" || indentOf(lines[j]) > ind); j++ {
				if strings.TrimSpace(lines[j]) == "" {
					body = append(body, "")
					continue
				}
				if cut < 0 {
					cut = indentOf(lines[j])
				}
				if indentOf(lines[j]) < cut {
					return step{}, fmt.Errorf("line %d: a run line less indented than the first", j+1)
				}
				body = append(body, lines[j][cut:])
			}
			st.body = strings.TrimRight(strings.Join(body, "\n"), "\n") + "\n"
		case strings.HasPrefix(key, "run:"):
			return step{}, fmt.Errorf("line %d: the run of the step %q is not a block scalar (run: |)", i+1, name)
		}
	}
	if strings.TrimSpace(st.body) == "" {
		return step{}, fmt.Errorf("the step %q has no run script", name)
	}
	if rest := strings.ReplaceAll(st.body, "${{ matrix.path }}", ""); strings.Contains(rest, "${{") {
		return step{}, fmt.Errorf("the run of the step %q holds a ${{ }} expression this command does not resolve: pass it through env", name)
	}
	return st, nil
}

func indentOf(l string) int { return len(l) - len(strings.TrimLeft(l, " ")) }

// ── helpers ───────────────────────────────────────────────────────────────────

// baseEnv: no git configuration of the host, no prompt, no cluster.
func baseEnv() []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"LANG=C",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=release-own-tree", "GIT_AUTHOR_EMAIL=guard@example.invalid",
		"GIT_COMMITTER_NAME=release-own-tree", "GIT_COMMITTER_EMAIL=guard@example.invalid",
		"KUBECONFIG=/dev/null",
		"NO_COLOR=1",
	}
	if t := os.Getenv("TMPDIR"); t != "" {
		env = append(env, "TMPDIR="+t)
	}
	return env
}

// lsFiles lists the files Git tracks and the new files it does not ignore.
func lsFiles(root string, patterns ...string) ([]string, error) {
	out, err := git(root, nil, append([]string{"ls-files", "-z", "--cached", "--others", "--exclude-standard", "--"}, patterns...)...)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range strings.Split(out, "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	sort.Strings(files)
	return files, nil
}

func git(dir string, env []string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(b), nil
}

// combined runs bash with args in dir and returns what it wrote to both outputs.
func combined(dir string, env []string, args ...string) (string, error) {
	cmd := exec.Command("bash", args...)
	cmd.Dir = dir
	cmd.Env = env
	b, err := cmd.CombinedOutput()
	return string(b), err
}

// command runs helm or bash and returns its standard output.
func command(dir string, env []string, name string, args ...string) (string, error) {
	var cmd *exec.Cmd
	switch name {
	case "helm":
		cmd = exec.Command("helm", args...)
	case "bash":
		cmd = exec.Command("bash", args...)
	default:
		return "", fmt.Errorf("command: %s is not run here", name)
	}
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(b), nil
}

func writeFiles(root string, files map[string]string) error {
	for rel, text := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(text), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// writeExec writes a program the fixture runs: it runs only when its owner may execute it.
func writeExec(path, text string) error {
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return err
	}
	if b, err := exec.Command("chmod", "u+x", path).CombinedOutput(); err != nil {
		return fmt.Errorf("chmod u+x %s: %v: %s", path, err, strings.TrimSpace(string(b)))
	}
	return nil
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func slug(s string) string {
	return strings.Trim(regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(strings.ToLower(s), "-"), "-")
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return "    " + strings.Join(lines, "\n    ")
}

// around returns the lines from the first that holds key, or the tail.
func around(s, key string, n int) string {
	if i := strings.Index(s, key); i >= 0 {
		start := strings.LastIndex(s[:i], "\n") + 1
		lines := strings.Split(s[start:], "\n")
		if len(lines) > n {
			lines = lines[:n]
		}
		return "    " + strings.Join(lines, "\n    ")
	}
	return tail(s, n)
}

func say(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}
