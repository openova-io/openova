package chartupgrade

// The helpers of the chart cases: a chart of an earlier release, read from the
// history of the repository (the commit where its Chart.yaml has that version,
// found by git log -S on the version line), rendered with the helm on PATH,
// which must be Helm 3 (the workflows install the Helm of the release).
//
// A case needs helm, git and the full history. Without them it is skipped,
// unless UPGRADE_PROOF=required, and then it fails: the chart tests and the job
// chart-upgrade of test-ungated-trees.yaml set it. UPGRADE_CHART names the
// chart under test (default: the chart of the case in this checkout).

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// needTools returns the root of the repository, or stops the test: skipped,
// or failed under UPGRADE_PROOF=required.
func needTools(t *testing.T) string {
	t.Helper()
	stop := t.Skipf
	if os.Getenv("UPGRADE_PROOF") == "required" {
		stop = t.Fatalf
	}
	out, err := exec.Command("helm", "version", "--short").Output()
	if err != nil {
		stop("helm: %v", err)
	}
	if v := strings.TrimSpace(string(out)); !strings.HasPrefix(v, "v3.") {
		stop("helm %s: the patch modelled is the one of Helm 3", v)
	} else {
		t.Logf("helm %s", v)
	}
	top, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		stop("git: %v", err)
	}
	root := strings.TrimSpace(string(top))
	if git(t, root, "rev-parse", "--is-shallow-repository") == "true" {
		stop("the checkout is shallow: the charts of the earlier releases are read from the history (check out with fetch-depth 0)")
	}
	return root
}

// chartUnderTest is UPGRADE_CHART, or the chart at chartPath in this checkout.
func chartUnderTest(root, chartPath string) string {
	if c := os.Getenv("UPGRADE_CHART"); c != "" {
		return c
	}
	return filepath.Join(root, chartPath)
}

func git(t *testing.T, root string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", root}, args...)...).Output() // #nosec G204 -- git on the repository of the test
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}

// releaseCommit is the newest commit whose Chart.yaml reads that version.
func releaseCommit(t *testing.T, root, chartPath, version string) string {
	t.Helper()
	line := "version: " + version
	for _, c := range strings.Fields(git(t, root, "log", "--format=%H", "-S", line, "--", chartPath+"/Chart.yaml")) {
		if slices.Contains(strings.Split(git(t, root, "show", c+":"+chartPath+"/Chart.yaml"), "\n"), line) {
			return c
		}
	}
	t.Fatalf("no commit of the history has %s/Chart.yaml at %s", chartPath, line)
	return ""
}

// extract writes the chart at a commit (without its tests) into a new folder.
func extract(t *testing.T, root, chartPath, commit string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range strings.Fields(git(t, root, "ls-tree", "-r", "--name-only", commit, "--", chartPath)) {
		rel := strings.TrimPrefix(name, chartPath+"/")
		if strings.HasPrefix(rel, "tests/") || strings.Contains(rel, "..") {
			continue
		}
		out, err := exec.Command("git", "-C", root, "show", commit+":"+name).Output() // #nosec G204 -- git on the repository of the test
		if err != nil {
			t.Fatalf("git show %s:%s: %v", commit, name, err)
		}
		dst := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, out, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// render is helm template of a chart, as the manifest of a release holds it.
func render(t *testing.T, chart, release string, values []string) map[string]Object {
	t.Helper()
	args := append([]string{"template", release, chart, "--namespace", release}, values...)
	var stderr bytes.Buffer
	c := exec.Command("helm", args...) // #nosec G204 -- helm on a chart of the repository
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		t.Fatalf("helm template %s: %v\n%s", chart, err, stderr.String())
	}
	objs, err := Parse(out)
	if err != nil {
		t.Fatalf("the render of %s: %v", chart, err)
	}
	return objs
}

// upgradeAll is Helm's upgrade of every object of the new render.
func upgradeAll(t *testing.T, deployed, live, modified map[string]Object) map[string]Outcome {
	t.Helper()
	out := map[string]Outcome{}
	for k, m := range modified {
		var d, l *Object
		if o, ok := deployed[k]; ok {
			d = &o
		}
		if o, ok := live[k]; ok {
			l = &o
		}
		o, err := Upgrade(d, l, m)
		if err != nil {
			t.Fatal(err)
		}
		out[k] = o
	}
	return out
}

// converges: no object is refused, each object after the upgrade is the new
// render, and every probe of every container names one handler.
func converges(t *testing.T, outcomes map[string]Outcome, next map[string]Object) {
	t.Helper()
	for k, o := range outcomes {
		if o.Refusal != "" {
			t.Errorf("%s: the upgrade is refused: %s", k, o.Refusal)
			continue
		}
		drift, err := Drift(o.Object, next[k])
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range drift {
			t.Errorf("%s after the upgrade is not the new render: %s", k, d)
		}
		errs, err := Validate(o.Object)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range errs {
			t.Errorf("%s: %s", k, e)
		}
	}
}
