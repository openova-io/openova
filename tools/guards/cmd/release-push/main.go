// Command release-push holds that a release run which publishes several charts
// never loses one of them on the way to main.
//
// Each job of the matrix of .github/workflows/blueprint-release.yaml publishes one
// chart and then pushes its own lockstep commit to main in the step with the id
// lockstep_push. On 20 Nov 2026 (run 44821486879) the six jobs of one run pushed
// within seconds of each other: five landed, and the commit of
// bp-cert-manager-powerdns-webhook 1.1.3 was refused three times, the step's
// bound. The chart was in the registry while main kept 1.1.2 with its new files,
// nothing said so, and hours later the cut of a release candidate refused.
//
// This command does not read the step for a number. It takes the step's own run
// script and env keys from the workflow and RUNS it the way one release run does:
// one job per chart, all at once, each in a clone of one bare origin whose
// pre-receive hook refuses as many pushes of each chart as the case says. Cases:
//
//  1. six charts, every push but the first chart's refused twice: all six land
//     at every site, and every refused try is logged.
//  2. six charts, the k-th refused k-1 times (the sixth five times, two more
//     than the three tries of 20 Nov): all six land.
//  3. one chart whose every push is refused: the step fails, an ::error line
//     names the chart and its version, the step summary names them and the
//     release to start again, main keeps the old version, and the step tried at
//     least five times as many pushes as case 1 has charts before it gave up.
//
// It needs bash, git and python3 (the step compares versions with
// scripts/lib/version_order.py and reads what a package holds with
// scripts/lib/chart-file-deps.sh, both copied from the repository into the
// scratch origin, stepFiles). The step's env keys are those the workflow declares: a key this
// command has no value for stops it (exit 2), and a key the workflow does not
// declare is not set, so a step that loses an input is run without it.
//
//	cd tools/guards && go run ./cmd/release-push [-workflow <file>] [-scratch <dir>] [-keep]
//
// Exit 0: every case holds. Exit 1: findings. Exit 2: the step or the scratch
// could not be prepared.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	stepID      = "lockstep_push"
	oldVersion  = "1.0.0"
	newVersion  = "1.0.1"
	chartsInRun = 6
	// The fewest pushes case 3 must see before the step gives up.
	minBound = 5 * chartsInRun
	// A push the origin of case 3 never stops refusing.
	alwaysRefused = 100000
	caseTimeout   = 10 * time.Minute
)

// Files of the repository the step runs, copied into the scratch origin.
var stepFiles = []string{"scripts/lib/version_order.py", "scripts/lib/chart-file-deps.sh"}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, out, errw io.Writer) int {
	fs := flag.NewFlagSet("release-push", flag.ContinueOnError)
	fs.SetOutput(errw)
	workflow := fs.String("workflow", "", "the workflow file (default: .github/workflows/blueprint-release.yaml of the repository)")
	scratch := fs.String("scratch", "", "the folder for the scratch origins (default: a new temporary folder)")
	keep := fs.Bool("keep", false, "keep the scratch folder")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	root, err := git("", "rev-parse", "--show-toplevel")
	if err != nil {
		say(errw, "release-push: %v\n", err)
		return 2
	}
	root = strings.TrimSpace(root)
	if *workflow == "" {
		*workflow = filepath.Join(root, ".github", "workflows", "blueprint-release.yaml")
	}
	text, err := os.ReadFile(filepath.Clean(*workflow))
	if err != nil {
		say(errw, "release-push: %v\n", err)
		return 2
	}
	st, err := extractStep(string(text), stepID)
	if err != nil {
		say(errw, "release-push: %s: %v\n", *workflow, err)
		return 2
	}
	repoFiles := map[string]string{}
	for _, rel := range stepFiles {
		b, err := os.ReadFile(filepath.Clean(filepath.Join(root, rel)))
		if err != nil {
			say(errw, "release-push: %v\n", err)
			return 2
		}
		repoFiles[rel] = string(b)
	}
	base := *scratch
	if base == "" {
		base, err = os.MkdirTemp("", "release-push-")
		if err != nil {
			say(errw, "release-push: %v\n", err)
			return 2
		}
	} else if err := os.MkdirAll(base, 0o750); err != nil {
		say(errw, "release-push: %v\n", err)
		return 2
	}
	if !*keep {
		defer func() { _ = os.RemoveAll(base) }()
	}

	findings, err := runCases(st, repoFiles, base, out)
	if err != nil {
		say(errw, "release-push: %v\n", err)
		return 2
	}
	for _, f := range findings {
		say(out, "FAIL %s\n", f)
	}
	say(out, "release-push: %d finding(s) in 3 cases of the step %s of %s\n", len(findings), stepID, *workflow)
	if len(findings) > 0 {
		say(out, "release-push: a chart that one release run publishes must land on main at every site, or the run must fail naming it and the release to start again.\n")
		return 1
	}
	return 0
}

// runCases runs the three cases in folders under base and returns the findings.
func runCases(st step, repoFiles map[string]string, base string, out io.Writer) ([]string, error) {
	var findings []string

	twice := make([]int, chartsInRun)
	growing := make([]int, chartsInRun)
	for k := 1; k < chartsInRun; k++ {
		twice[k] = 2
		growing[k] = k
	}
	for n, c := range []struct {
		name     string
		refusals []int
	}{
		{"case 1 (six charts, every push but the first refused twice)", twice},
		{"case 2 (six charts, the k-th refused k-1 times)", growing},
	} {
		dir := uniqueDir(filepath.Join(base, fmt.Sprintf("case%d", n+1)))
		res, err := runJobs(st, repoFiles, dir, c.refusals)
		if err != nil {
			return nil, err
		}
		before := len(findings)
		logged := 0
		for k, r := range res {
			chart := chartName(k)
			if r.exit != 0 {
				findings = append(findings, fmt.Sprintf("%s: the step of %s ended with exit %d:\n%s", c.name, chart, r.exit, tail(r.output, 8)))
			}
			for _, site := range sites(k) {
				v, err := originVersion(dir, site.file, site.prefix)
				if err != nil {
					return nil, err
				}
				if v != newVersion {
					findings = append(findings, fmt.Sprintf("%s: %s is LOST: main carries %s %s at %s, not %s", c.name, chart, site.name, v, site.file, newVersion))
				}
			}
			n := strings.Count(r.output, "push attempt ")
			logged += n
			if r.exit == 0 && n < c.refusals[k] {
				findings = append(findings, fmt.Sprintf("%s: the origin refused %d push(es) of %s, but the step logged %d refused try", c.name, c.refusals[k], chart, n))
			}
			// A commit that landed went through the hook once more than it was
			// refused; fewer pushes there mean the hook did not run.
			if r.exit == 0 && r.pushes < c.refusals[k]+1 {
				return nil, fmt.Errorf("%s: the origin's hook saw %d push(es) of %s, which it was to refuse %d times: the case did not run as planned", c.name, r.pushes, chart, c.refusals[k])
			}
		}
		if len(findings) == before {
			say(out, "ok   %s: all %d charts landed at every site; %d refused tries logged\n", c.name, len(res), logged)
		}
	}

	name := "case 3 (one chart, every push refused)"
	dir := uniqueDir(filepath.Join(base, "case3"))
	res, err := runJobs(st, repoFiles, dir, []int{alwaysRefused})
	if err != nil {
		return nil, err
	}
	before := len(findings)
	r := res[0]
	chart := chartName(0)
	if r.exit == 0 {
		findings = append(findings, fmt.Sprintf("%s: the step ended with exit 0 although main never took the commit of %s", name, chart))
	}
	if !namedOnErrorLine(r.output, chart, newVersion) {
		findings = append(findings, fmt.Sprintf("%s: no ::error line names %s %s:\n%s", name, chart, newVersion, tail(r.output, 4)))
	}
	again := fmt.Sprintf("gh workflow run blueprint-release.yaml --ref main -f blueprint=%s -f tree=platform", strings.TrimPrefix(chart, "bp-"))
	if !strings.Contains(r.summary, chart) || !strings.Contains(r.summary, newVersion) || !strings.Contains(r.summary, again) {
		findings = append(findings, fmt.Sprintf("%s: the step summary does not name %s %s and the release to start again (%s); it reads %q", name, chart, newVersion, again, strings.TrimSpace(r.summary)))
	}
	if r.pushes < minBound {
		findings = append(findings, fmt.Sprintf("%s: the step gave up after %d push(es); one run here has %d charts, so it must try at least %d", name, r.pushes, chartsInRun, minBound))
	}
	for _, site := range sites(0) {
		v, err := originVersion(dir, site.file, site.prefix)
		if err != nil {
			return nil, err
		}
		if v != oldVersion {
			findings = append(findings, fmt.Sprintf("%s: main carries %s %s at %s although every push was refused", name, site.name, v, site.file))
		}
	}
	if len(findings) == before {
		say(out, "ok   %s: the step failed after %d pushes, named %s %s on an ::error line and in its summary with %q\n", name, r.pushes, chart, newVersion, again)
	}
	return findings, nil
}

func uniqueDir(dir string) string {
	for i := 0; ; i++ {
		d := dir
		if i > 0 {
			d = fmt.Sprintf("%s-%d", dir, i)
		}
		if _, err := os.Stat(d); errors.Is(err, os.ErrNotExist) {
			return d
		}
	}
}

func namedOnErrorLine(output, chart, version string) bool {
	for _, l := range strings.Split(output, "\n") {
		if strings.HasPrefix(l, "::error") && strings.Contains(l, chart) && strings.Contains(l, version) {
			return true
		}
	}
	return false
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return "    " + strings.Join(lines, "\n    ")
}

// ── the scratch origin and the jobs ──────────────────────────────────────────

type site struct {
	name, file, prefix string
}

func chartName(k int) string { return fmt.Sprintf("bp-c%d", k+1) }
func chartPath(k int) string { return fmt.Sprintf("platform/c%d", k+1) }

// sites are the three lockstep sites of chart k in the scratch tree, with the
// prefix of the line that carries the version.
func sites(k int) []site {
	return []site{
		{"chart version", chartPath(k) + "/chart/Chart.yaml", "version: "},
		{"blueprint spec.version", chartPath(k) + "/blueprint.yaml", "  version: "},
		{"kit pin", fmt.Sprintf("clusters/_template/bootstrap-kit/%02d-%s.yaml", k+1, chartName(k)), "      version: "},
	}
}

func siteText(k int, i int, version string) string {
	switch i {
	case 0:
		return fmt.Sprintf("apiVersion: v2\nname: %s\nversion: %s\n", chartName(k), version)
	case 1:
		return fmt.Sprintf("apiVersion: catalyst.openova.io/v1alpha1\nkind: Blueprint\nmetadata:\n  name: %s\nspec:\n  version: %s\n", chartName(k), version)
	default:
		return fmt.Sprintf("apiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata:\n  name: %s\nspec:\n  chart:\n    spec:\n      chart: %s\n      version: %s\n", chartName(k), chartName(k), version)
	}
}

// The pre-receive hook of the scratch origin: it counts the pushes of each chart
// and refuses as many as the plan file of the chart says.
const hookText = `#!/usr/bin/env bash
plan=%q
while read -r _old new _ref; do
  chart="$(git log -1 --format=%%s "$new" | sed -n -E 's/^deploy\(([^)]+)\).*/\1/p')"
  if [ -z "$chart" ] || [ ! -f "$plan/$chart" ]; then
    continue
  fi
  echo push >> "$plan/$chart.pushes"
  owed="$(cat "$plan/$chart")"
  if [ "$owed" -gt 0 ]; then
    echo "$((owed - 1))" > "$plan/$chart"
    echo "release-push origin: this push of $chart is refused" >&2
    exit 1
  fi
done
exit 0
`

type result struct {
	exit            int
	output, summary string
	pushes          int
}

// runJobs prepares one origin with len(refusals) charts at oldVersion and runs
// the step for every chart at once, each in its own clone, as the jobs of one
// release run do.
func runJobs(st step, repoFiles map[string]string, dir string, refusals []int) ([]result, error) {
	origin := filepath.Join(dir, "origin.git")
	seed := filepath.Join(dir, "seed")
	plan := filepath.Join(dir, "plan")
	for _, d := range []string{seed, plan} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return nil, err
		}
	}
	env := baseEnv(dir)
	files := map[string]string{}
	for rel, text := range repoFiles {
		files[rel] = text
	}
	for k := range refusals {
		for i, s := range sites(k) {
			files[s.file] = siteText(k, i, oldVersion)
		}
	}
	if err := writeFiles(seed, files); err != nil {
		return nil, err
	}
	for _, c := range [][]string{
		{"init", "-q", "--bare", "-b", "main", origin},
		{"-C", seed, "init", "-q", "-b", "main"},
		{"-C", seed, "add", "-A"},
		{"-C", seed, "-c", "user.name=release-push", "-c", "user.email=release-push@example.invalid", "commit", "-q", "-m", "seed"},
		{"-C", seed, "push", "-q", origin, "main"},
	} {
		if _, err := gitEnv(dir, env, c...); err != nil {
			return nil, err
		}
	}
	hook := filepath.Join(origin, "hooks", "pre-receive")
	if err := writeExec(hook, fmt.Sprintf(hookText, plan)); err != nil {
		return nil, err
	}
	// The run script sits beside the clones of the jobs, so each job runs it as
	// ../step.sh from its clone, outside the tree it commits.
	if err := os.WriteFile(filepath.Join(dir, "step.sh"), []byte(st.body), 0o600); err != nil {
		return nil, err
	}

	jobs := make([]string, len(refusals))
	stepEnvs := make([][]string, len(refusals))
	for k, n := range refusals {
		if err := os.WriteFile(filepath.Join(plan, chartName(k)), []byte(strconv.Itoa(n)+"\n"), 0o600); err != nil {
			return nil, err
		}
		jobs[k] = filepath.Join(dir, fmt.Sprintf("job%d", k+1))
		if _, err := gitEnv(dir, env, "clone", "-q", origin, jobs[k]); err != nil {
			return nil, err
		}
		// The earlier steps of the job wrote the new version at every site.
		edits := map[string]string{}
		for i, s := range sites(k) {
			edits[s.file] = siteText(k, i, newVersion)
		}
		if err := writeFiles(jobs[k], edits); err != nil {
			return nil, err
		}
		se, err := st.envFor(values(k))
		if err != nil {
			return nil, err
		}
		stepEnvs[k] = append(append([]string{}, env...), se...)
		stepEnvs[k] = append(stepEnvs[k],
			"GITHUB_OUTPUT="+filepath.Join(dir, fmt.Sprintf("job%d.output", k+1)),
			"GITHUB_STEP_SUMMARY="+filepath.Join(dir, fmt.Sprintf("job%d.summary", k+1)),
		)
	}

	ctx, cancel := context.WithTimeout(context.Background(), caseTimeout)
	defer cancel()
	results := make([]result, len(refusals))
	var wg sync.WaitGroup
	for k := range refusals {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			var buf bytes.Buffer
			cmd := exec.CommandContext(ctx, "bash", "-e", "../step.sh")
			cmd.Dir = jobs[k]
			cmd.Env = stepEnvs[k]
			cmd.Stdout = &buf
			cmd.Stderr = &buf
			err := cmd.Run()
			results[k].output = buf.String()
			var ee *exec.ExitError
			switch {
			case err == nil:
			case errors.As(err, &ee):
				results[k].exit = ee.ExitCode()
			default:
				results[k].exit = -1
				results[k].output += "\n" + err.Error()
			}
		}(k)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("the jobs of %s did not end within %s", dir, caseTimeout)
	}
	for k := range refusals {
		b, err := os.ReadFile(filepath.Clean(filepath.Join(dir, fmt.Sprintf("job%d.summary", k+1))))
		if err == nil {
			results[k].summary = string(b)
		}
		pushes, err := os.ReadFile(filepath.Clean(filepath.Join(plan, chartName(k)+".pushes")))
		if err == nil {
			results[k].pushes = strings.Count(string(pushes), "push\n")
		}
	}
	return results, nil
}

// values are what the earlier steps of the job of chart k hand to the step.
func values(k int) map[string]string {
	s := sites(k)
	return map[string]string{
		"CHART_PATH":         chartPath(k),
		"CHART_BUMPED":       "true",
		"CHART_FILE":         s[0].file,
		"CHART_PREV_VERSION": oldVersion,
		"CHART_NAME":         chartName(k),
		"CHART_VERSION":      newVersion,
		"PIN_FILE":           s[2].file,
		"PREV_VERSION":       oldVersion,
		"BP_FILE":            s[1].file,
		"BP_PREV_VERSION":    oldVersion,
		"PIN_BUMPED":         "true",
		"BP_BUMPED":          "true",
		"SEED_BUMPED":        "false",
	}
}

// baseEnv is the environment of every command of a case: no git configuration
// of the host (no signing, no identity), no prompt, and no pause between tries.
func baseEnv(dir string) []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + dir,
		"LANG=C",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"KUBECONFIG=/dev/null",
		"LOCKSTEP_PUSH_PAUSE_MAX=0",
	}
}

func originVersion(dir, file, prefix string) (string, error) {
	text, err := gitEnv(dir, baseEnv(dir), "--git-dir", filepath.Join(dir, "origin.git"), "show", "main:"+file)
	if err != nil {
		return "", err
	}
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, prefix) {
			return strings.Trim(strings.TrimSpace(strings.TrimPrefix(l, prefix)), `"`), nil
		}
	}
	return "", fmt.Errorf("main:%s has no line %q", file, prefix)
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

// writeExec writes a hook that git runs: git runs a hook only when its owner may
// execute it.
func writeExec(path, text string) error {
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return err
	}
	if b, err := exec.Command("chmod", "u+x", path).CombinedOutput(); err != nil {
		return fmt.Errorf("chmod u+x %s: %v: %s", path, err, strings.TrimSpace(string(b)))
	}
	return nil
}

func say(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

func git(dir string, args ...string) (string, error) {
	return gitEnv(dir, nil, args...)
}

func gitEnv(dir string, env []string, args ...string) (string, error) {
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

// ── the step, read from the workflow ─────────────────────────────────────────

type step struct {
	body string
	env  []string // the keys of the step's env, in order
}

// envFor gives every key the step declares its value; a key without one stops
// the command, since the step would then run with an input this command does
// not know.
func (s step) envFor(vals map[string]string) ([]string, error) {
	var out []string
	for _, k := range s.env {
		v, ok := vals[k]
		if !ok {
			return nil, fmt.Errorf("the step %s declares env %s, which this command has no value for: give it one in values()", stepID, k)
		}
		out = append(out, k+"="+v)
	}
	return out, nil
}

// extractStep reads the step with the given id from a workflow: the keys of its
// env and its run script. It reads the block YAML workflows are written in by
// indentation: the step is the item that holds `id: <id>`, and its run is a
// block scalar (`run: |`), dedented as YAML does.
func extractStep(text, id string) (step, error) {
	lines := strings.Split(text, "\n")
	at := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "id: "+id {
			if at >= 0 {
				return step{}, fmt.Errorf("two steps have the id %s (lines %d and %d)", id, at+1, i+1)
			}
			at = i
		}
	}
	if at < 0 {
		return step{}, fmt.Errorf("no step has the id %s", id)
	}
	ind := indentOf(lines[at])
	start := at
	for start > 0 && (indentOf(lines[start]) != ind-2 || !strings.HasPrefix(strings.TrimSpace(lines[start]), "- ")) {
		start--
	}
	end := at + 1
	for end < len(lines) && (strings.TrimSpace(lines[end]) == "" || indentOf(lines[end]) >= ind) {
		end++
	}
	var st step
	haveRun := false
	for i := start + 1; i < end; i++ {
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
			for len(body) > 0 && body[len(body)-1] == "" {
				body = body[:len(body)-1]
			}
			st.body = strings.Join(body, "\n") + "\n"
			haveRun = true
		case strings.HasPrefix(key, "run:"):
			return step{}, fmt.Errorf("line %d: the run of the step %s is not a block scalar (run: |)", i+1, id)
		}
	}
	if !haveRun || strings.TrimSpace(st.body) == "" {
		return step{}, fmt.Errorf("the step %s has no run script", id)
	}
	if strings.Contains(st.body, "${{") {
		return step{}, fmt.Errorf("the run of the step %s holds a ${{ }} expression, which this command does not resolve: pass it through env", id)
	}
	return st, nil
}

func indentOf(l string) int { return len(l) - len(strings.TrimLeft(l, " ")) }
