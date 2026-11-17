package lifecycle

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// These tests run the release scripts as the release Job runs them, against
// Git repositories on disk that stand for the upstream repository and for a
// Sovereign's Gitea, with the kit rewrite and the pin extractor taken from the
// cutover chart in this checkout (not copies). They need git and sh.

const (
	upstreamPrefix = "oci://ghcr.io/openova-io"
	localPrefix    = "oci://registry.test.example/openova-io"
	trustSecret    = "cutover-harbor-ca"
	kitDir         = "clusters/_template/bootstrap-kit"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../../../../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "platform/self-sovereign-cutover/chart/templates")); err != nil {
		t.Fatalf("not the repository root: %s", root)
	}
	return root
}

var (
	helmComment = regexp.MustCompile(`(?s)\s*\{\{-\s*/\*.*?\*/\}\}`)
	gitPush     = regexp.MustCompile(`(^|[ (!])(g|git)( +-{1,2}[^ ]+)* +push( |$)`)
)

// chartScript is the shell a key of a cutover chart ConfigMap renders to. The
// blocks the release reuses hold only Helm comments, so removing them as Helm
// does gives the rendered bytes; any other action fails the test.
func chartScript(t *testing.T, file, key string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(filepath.Join(repoRoot(t), "platform/self-sovereign-cutover/chart/templates", file)))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(b), "\n")
	start := -1
	for i, l := range lines {
		if l == "  "+key+": |" {
			start = i + 1
			break
		}
	}
	if start < 0 {
		t.Fatalf("%s has no key %s", file, key)
	}
	var body []string
	for _, l := range lines[start:] {
		if strings.TrimSpace(l) != "" && !strings.HasPrefix(l, "    ") {
			break
		}
		body = append(body, l)
	}
	text := helmComment.ReplaceAllString(strings.Join(body, "\n"), "")
	if strings.Contains(text, "{{") {
		t.Fatalf("%s %s holds a Helm action this test does not render", file, key)
	}
	out := make([]string, 0, len(body))
	for _, l := range strings.Split(text, "\n") {
		out = append(out, strings.TrimPrefix(l, "    "))
	}
	return strings.Join(out, "\n") + "\n"
}

type fixture struct {
	t         *testing.T
	dir       string
	upstream  string
	gitea     string
	repo      string
	scripts   string
	home      string
	installed string // the commit the Sovereign was installed from
	main      string // the Sovereign's main after its own commits
}

// command is exec.Command; the tests start every process through it.
func command(name string, args ...string) *exec.Cmd { return exec.Command(name, args...) }

func (f *fixture) git(dir string, args ...string) string {
	f.t.Helper()
	cmd := command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "HOME="+f.home, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f *fixture) write(dir, path, text string) {
	f.t.Helper()
	p := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func slot(name, version, url string) string {
	return fmt.Sprintf(`apiVersion: source.toolkit.fluxcd.io/v1
kind: HelmRepository
metadata:
  name: %[1]s
  namespace: flux-system
spec:
  type: oci
  interval: 10m
  url: %[3]s
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: %[1]s
  namespace: flux-system
spec:
  interval: 15m
  chart:
    spec:
      chart: %[1]s
      version: %[2]s
      sourceRef:
        kind: HelmRepository
        name: %[1]s
`, name, version, url)
}

const platformSlot = `  values:
    global:
      imageRegistry: ""
    sovereign:
      fqdn: test.example
`

func rcID(sha string, n int) string { return fmt.Sprintf("20261116T0000%02dZ-%s", n, sha[:7]) }

// newFixture: upstream main with commit installed (rc for it too); the
// Sovereign's Gitea main = installed + the cutover pivot (cutover step 06's own
// rewrite_kit_tree) + one commit of the sovereign-admin that sets imageRegistry.
func newFixture(t *testing.T) *fixture {
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal("git is needed to run the release scripts")
	}
	d := t.TempDir()
	f := &fixture{t: t, dir: d, upstream: d + "/upstream.git", gitea: "file://" + d + "/gitea", repo: d + "/gitea/openova/openova.git",
		scripts: d + "/scripts", home: d + "/home"}
	for _, p := range []string{f.home, f.scripts} {
		if err := os.MkdirAll(p, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	f.write(f.scripts, "kit-rewrite.sh", chartScript(t, "06-helmrepository-patches-job.yaml", "kit-rewrite.sh"))
	f.write(f.scripts, "extract-pins.sh", chartScript(t, "03b-bootstrap-kit-pins.yaml", "extract-pins.sh"))
	for _, n := range []string{"release-region.sh", "release-run.sh"} {
		b, err := releaseScripts.ReadFile("release/" + n)
		if err != nil {
			t.Fatal(err)
		}
		f.write(f.scripts, n, string(b))
	}
	for _, bare := range []string{f.upstream, f.repo} {
		f.git(d, "init", "-q", "--bare", bare)
		f.git(d, "--git-dir="+bare, "symbolic-ref", "HEAD", "refs/heads/main")
	}
	w := d + "/work"
	f.git(d, "init", "-q", w)
	f.git(w, "checkout", "-q", "-b", "main")
	f.write(w, kitDir+"/10-bp-foo.yaml", slot("bp-foo", "1.0.0", upstreamPrefix))
	f.write(w, kitDir+"/13-bp-catalyst-platform.yaml", slot("bp-catalyst-platform", "1.4.0", upstreamPrefix)+platformSlot)
	f.write(w, "platform/foo/chart/values.yaml", "image:\n  repository: ghcr.io/openova-io/foo\n  tag: \"1.0.0\"\n")
	f.writeCutoverChart(w, installedCutover, step03Marker(installedCutover))
	f.git(w, "add", "-A")
	f.git(w, "commit", "-q", "-m", "installed release")
	f.installed = f.git(w, "rev-parse", "HEAD")
	f.git(w, "push", "-q", f.upstream, "main", "HEAD:refs/heads/rc/"+rcID(f.installed, 0))

	// The Sovereign: its Gitea main is the installed release, then cutover
	// step 06's pivot, then a commit of its own.
	s := d + "/sovereign"
	f.git(d, "clone", "-q", f.upstream, s)
	cmd := exec.Command("sh", "-c", `. "$KIT"; edited=0; local_prefix="$LP"; trust_secret="$TS"; rewrite_kit_tree `+kitDir)
	cmd.Dir = s
	cmd.Env = append(os.Environ(), "KIT="+f.scripts+"/kit-rewrite.sh", "LP="+localPrefix, "TS="+trustSecret, "UPSTREAM_PREFIX="+upstreamPrefix)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("step 06 pivot: %v\n%s", err, out)
	}
	f.git(s, "commit", "-q", "-am", "cutover: pivot HelmRepository URLs to local Harbor")
	sed(t, s+"/"+kitDir+"/13-bp-catalyst-platform.yaml", `      imageRegistry: ""`, `      imageRegistry: "registry.test.example"`)
	f.git(s, "commit", "-q", "-am", "sovereign-admin: own registry for the platform images")
	f.main = f.git(s, "rev-parse", "HEAD")
	f.git(s, "push", "-q", f.repo, "HEAD:refs/heads/main")
	return f
}

func sed(t *testing.T, path, from, to string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), from) {
		t.Fatalf("%s does not hold %q", path, from)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(b), from, to, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
}

// candidate commits change(work) on top of the installed release upstream and
// publishes it as rc/<id>; it returns the id and the commit.
func (f *fixture) candidate(n int, change func(w string)) (string, string) {
	f.t.Helper()
	w := fmt.Sprintf("%s/cand%d", f.dir, n)
	f.git(f.dir, "clone", "-q", f.upstream, w)
	f.git(w, "checkout", "-q", f.installed)
	change(w)
	f.git(w, "add", "-A")
	f.git(w, "commit", "-q", "-m", fmt.Sprintf("candidate %d", n))
	sha := f.git(w, "rev-parse", "HEAD")
	id := rcID(sha, n)
	f.git(w, "push", "-q", f.upstream, "HEAD:refs/heads/rc/"+id)
	return id, sha
}

func (f *fixture) laterRelease(w string) {
	sed(f.t, w+"/"+kitDir+"/10-bp-foo.yaml", "version: 1.0.0", "version: 1.1.0")
	f.write(w, kitDir+"/20-bp-bar.yaml", slot("bp-bar", "2.0.0", upstreamPrefix))
	sed(f.t, w+"/platform/foo/chart/values.yaml", `tag: "1.0.0"`, `tag: "1.1.0"`)
	sed(f.t, w+"/platform/foo/chart/values.yaml", "image:\n", "image:\n  # the repository: and tag: of a comment are no image line\n")
}

func (f *fixture) remote(ref string) string {
	f.t.Helper()
	out := f.git(f.dir, "ls-remote", f.repo, "refs/heads/"+ref)
	if out == "" {
		return ""
	}
	return strings.Fields(out)[0]
}

func (f *fixture) show(sha, path string) string {
	return f.git(f.dir, "--git-dir="+f.repo, "show", sha+":"+path)
}

// region runs release-region.sh like the release Job does and returns its
// output and exit code.
func (f *fixture) region(shell []string, env ...string) (string, int) {
	f.t.Helper()
	cmd := command(shell[0], append(append([]string{}, shell[1:]...), f.scripts+"/release-region.sh")...)
	cmd.Dir = f.dir
	cmd.Env = append(os.Environ(), "HOME="+f.home, "GIT_CONFIG_NOSYSTEM=1", "REGION=primary",
		"UPSTREAM_REPO_URL=file://"+f.upstream, "UPSTREAM_BRANCH=main", "UPSTREAM_PREFIX="+upstreamPrefix,
		"GITEA_INTERNAL_URL="+f.gitea, "GITEA_ORG=openova", "GITEA_REPO=openova", "GITEA_USERNAME=gitea_admin",
		"GITEA_PAT=not-a-real-token", "KIT_REWRITE="+f.scripts+"/kit-rewrite.sh", "PIN_EXTRACTOR="+f.scripts+"/extract-pins.sh")
	cmd.Env = append(cmd.Env, env...)
	cmd.Env = append(cmd.Env, "WORK="+f.t.TempDir())
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		f.t.Fatal(err)
	}
	return string(out), code
}

func resultOf(out string) map[string]string {
	m := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "release-result ") {
			m = map[string]string{}
			for _, kv := range strings.Fields(l)[1:] {
				if k, v, ok := strings.Cut(kv, "="); ok {
					m[k] = v
				}
			}
		}
	}
	return m
}

// shells are the shells the scripts are run with: sh, and busybox sh (the shell
// of the cutover image) where this machine has it.
func shells() [][]string {
	s := [][]string{{"sh"}}
	if _, err := exec.LookPath("busybox"); err == nil {
		s = append(s, []string{"busybox", "sh"})
	}
	return s
}

func TestReleaseStagesMergesAndPromotes(t *testing.T) {
	for _, sh := range shells() {
		t.Run(strings.Join(sh, "-"), func(t *testing.T) {
			f := newFixture(t)
			id, cand := f.candidate(1, f.laterRelease)

			out, code := f.region(sh, "PHASE=stage", "CANDIDATE="+id)
			r := resultOf(out)
			if code != 0 || r["phase"] != "staged" {
				t.Fatalf("stage: exit %d, result %v\n%s", code, r, out)
			}
			staged := r["staged"]
			if r["main"] != f.main || r["candidate"] != cand {
				t.Fatalf("stage recorded main=%s candidate=%s, want %s %s", r["main"], r["candidate"], f.main, cand)
			}
			if got := f.remote("main"); got != f.main {
				t.Fatalf("stage moved main to %s", got)
			}
			if f.remote("release/"+id) != staged || f.remote("rc/"+id) != cand {
				t.Fatalf("stage pushed release/%s=%s rc=%s, want %s %s", id, f.remote("release/"+id), f.remote("rc/"+id), staged, cand)
			}
			for _, anc := range []string{f.main, cand} {
				cmd := command("git", "--git-dir="+f.repo, "merge-base", "--is-ancestor", anc, staged)
				if err := cmd.Run(); err != nil {
					t.Fatalf("%s is not in the staged commit %s", anc, staged)
				}
			}
			foo := f.show(staged, kitDir+"/10-bp-foo.yaml")
			if !strings.Contains(foo, "version: 1.1.0") || !strings.Contains(foo, "url: "+localPrefix+"\n") || !strings.Contains(foo, "name: "+trustSecret) {
				t.Fatalf("bp-foo lost the candidate's version or the Sovereign's pivot:\n%s", foo)
			}
			bar := f.show(staged, kitDir+"/20-bp-bar.yaml")
			if !strings.Contains(bar, "url: "+localPrefix+"\n") || strings.Contains(bar, upstreamPrefix) || !strings.Contains(bar, "name: "+trustSecret) {
				t.Fatalf("the candidate's new slot is not pivoted to this Sovereign's registry:\n%s", bar)
			}
			if p := f.show(staged, kitDir+"/13-bp-catalyst-platform.yaml"); !strings.Contains(p, `imageRegistry: "registry.test.example"`) {
				t.Fatalf("the sovereign-admin's own commit is gone:\n%s", p)
			}
			for _, want := range []string{
				"release-report region=primary chart bp-foo 1.0.0 -> 1.1.0",
				"release-report region=primary chart bp-bar new 2.0.0",
				`release-report region=primary image platform/foo/chart/values.yaml: tag: "1.1.0"`,
				"release-report region=primary commit " + cand[:7] + " candidate 1",
			} {
				if !strings.Contains(out, want) {
					t.Fatalf("the report lacks %q:\n%s", want, out)
				}
			}
			if strings.Contains(out, "image platform/foo/chart/values.yaml: #") {
				t.Fatalf("the report names a comment line as an image:\n%s", out)
			}

			out, code = f.region(sh, "PHASE=check", "CANDIDATE="+id, "MAIN_SHA="+f.main, "STAGED_SHA="+staged)
			if r := resultOf(out); code != 0 || r["phase"] != "checked" {
				t.Fatalf("check: exit %d, result %v\n%s", code, r, out)
			}
			out, code = f.region(sh, "PHASE=promote", "CANDIDATE="+id, "MAIN_SHA="+f.main, "STAGED_SHA="+staged)
			if r := resultOf(out); code != 0 || r["phase"] != "promoted" || f.remote("main") != staged {
				t.Fatalf("promote: exit %d, result %v, main %s, want %s\n%s", code, r, f.remote("main"), staged, out)
			}
			// A second promote of the same release is a no-op.
			out, code = f.region(sh, "PHASE=promote", "CANDIDATE="+id, "MAIN_SHA="+f.main, "STAGED_SHA="+staged)
			if r := resultOf(out); code != 0 || r["phase"] != "promoted" {
				t.Fatalf("second promote: exit %d, result %v\n%s", code, r, out)
			}
		})
	}
}

func TestReleaseConflictMovesNothing(t *testing.T) {
	f := newFixture(t)
	id, cand := f.candidate(1, func(w string) {
		sed(t, w+"/"+kitDir+"/13-bp-catalyst-platform.yaml", `      imageRegistry: ""`, `      imageRegistry: "harbor.example"`)
	})
	out, code := f.region([]string{"sh"}, "PHASE=stage", "CANDIDATE="+id)
	r := resultOf(out)
	if code != 3 || r["phase"] != "conflict" || r["candidate"] != cand {
		t.Fatalf("stage: exit %d, result %v, want exit 3 conflict\n%s", code, r, out)
	}
	if !strings.Contains(out, "release-report region=primary conflict "+kitDir+"/13-bp-catalyst-platform.yaml") {
		t.Fatalf("the conflict is not named by file:\n%s", out)
	}
	if f.remote("main") != f.main || f.remote("release/"+id) != "" || f.remote("rc/"+id) != "" {
		t.Fatalf("a conflict moved a ref: main=%s release=%s rc=%s", f.remote("main"), f.remote("release/"+id), f.remote("rc/"+id))
	}
}

func TestReleaseRefusesWhenMainMovedAfterStaging(t *testing.T) {
	f := newFixture(t)
	id, _ := f.candidate(1, f.laterRelease)
	out, code := f.region([]string{"sh"}, "PHASE=stage", "CANDIDATE="+id)
	staged := resultOf(out)["staged"]
	if code != 0 || staged == "" {
		t.Fatalf("stage: exit %d\n%s", code, out)
	}
	s := f.dir + "/sovereign"
	f.write(s, "notes.txt", "a commit after staging\n")
	f.git(s, "add", "-A")
	f.git(s, "commit", "-q", "-m", "sovereign-admin: later commit")
	moved := f.git(s, "rev-parse", "HEAD")
	f.git(s, "push", "-q", f.repo, "HEAD:refs/heads/main")
	for _, phase := range []string{"check", "promote"} {
		out, code = f.region([]string{"sh"}, "PHASE="+phase, "CANDIDATE="+id, "MAIN_SHA="+f.main, "STAGED_SHA="+staged)
		if r := resultOf(out); code != 4 || r["phase"] != "moved" {
			t.Fatalf("%s: exit %d, result %v, want exit 4 moved\n%s", phase, code, r, out)
		}
		if f.remote("main") != moved {
			t.Fatalf("%s changed main to %s; the sovereign-admin's commit %s must stay", phase, f.remote("main"), moved)
		}
	}
}

func TestReleaseDryRunPushesNothing(t *testing.T) {
	f := newFixture(t)
	id, cand := f.candidate(1, f.laterRelease)
	out, code := f.region([]string{"sh"}, "PHASE=stage", "CANDIDATE="+id, "DRY_RUN=true")
	r := resultOf(out)
	if code != 0 || r["phase"] != "dry-run" || r["candidate"] != cand || r["staged"] == "" {
		t.Fatalf("dry run: exit %d, result %v\n%s", code, r, out)
	}
	if !strings.Contains(out, "chart bp-foo 1.0.0 -> 1.1.0") || !strings.Contains(out, "dry run: nothing was pushed") {
		t.Fatalf("the dry run does not report what would change:\n%s", out)
	}
	if f.remote("main") != f.main || f.remote("release/"+id) != "" || f.remote("rc/"+id) != "" {
		t.Fatal("a dry run pushed a ref")
	}
}

func TestReleaseRefusals(t *testing.T) {
	f := newFixture(t)
	id, cand := f.candidate(1, f.laterRelease)
	quoted, _ := f.candidate(2, func(w string) {
		f.write(w, kitDir+"/30-bp-baz.yaml", strings.Replace(slot("bp-baz", "1.0.0", upstreamPrefix), "url: "+upstreamPrefix, `url: "`+upstreamPrefix+`"`, 1))
	})
	wrongSuffix := id[:len(id)-7] + "0000000"
	f.git(f.dir, "--git-dir="+f.upstream, "update-ref", "refs/heads/rc/"+wrongSuffix, cand)
	for _, c := range []struct {
		name, candidate, want string
		code                  int
		env                   []string
	}{
		{"not an id", "latest", "is not a candidate id", 2, nil},
		{"absent upstream", "20261116T235959Z-abcdef0", "does not exist at", 2, nil},
		{"ref does not end the id", wrongSuffix, "which does not end the id", 2, nil},
		{"an upstream url the pivot does not rewrite", quoted, "unpivoted " + kitDir + "/30-bp-baz.yaml", 2, nil},
		{"no token", id, "no Gitea token", 1, []string{"GITEA_PAT="}},
		{"promote without a recorded stage", id, "no staged commit is recorded", 2, []string{"PHASE=promote"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := append([]string{"PHASE=stage", "CANDIDATE=" + c.candidate}, c.env...)
			out, code := f.region([]string{"sh"}, env...)
			if code != c.code || !strings.Contains(out, c.want) {
				t.Fatalf("exit %d, want %d and %q:\n%s", code, c.code, c.want, out)
			}
			if f.remote("main") != f.main {
				t.Fatal("a refusal moved main")
			}
		})
	}
}

func TestReleaseOfTheInstalledCandidateIsUpToDate(t *testing.T) {
	f := newFixture(t)
	id := rcID(f.installed, 0)
	out, code := f.region([]string{"sh"}, "PHASE=stage", "CANDIDATE="+id)
	r := resultOf(out)
	if code != 0 || r["phase"] != "up-to-date" || r["staged"] != f.main || f.remote("release/"+id) != f.main {
		t.Fatalf("exit %d, result %v, release/%s=%s, want main %s\n%s", code, r, id, f.remote("release/"+id), f.main, out)
	}
	out, code = f.region([]string{"sh"}, "PHASE=promote", "CANDIDATE="+id, "MAIN_SHA="+f.main, "STAGED_SHA="+f.main)
	if r := resultOf(out); code != 0 || r["phase"] != "promoted" || f.remote("main") != f.main {
		t.Fatalf("promote of an up-to-date release: exit %d, result %v\n%s", code, r, out)
	}
}

// TestReleaseNeverForcesTheBranchFluxFollows reads the scripts: no push names
// --force, and the only forced refspec is the staging branch release/<id>.
func TestReleaseNeverForcesTheBranchFluxFollows(t *testing.T) {
	for _, n := range []string{"release-region.sh", "release-run.sh"} {
		b, err := releaseScripts.ReadFile("release/" + n)
		if err != nil {
			t.Fatal(err)
		}
		pushes := 0
		for i, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(strings.TrimSpace(l), "#") || !gitPush.MatchString(l) {
				continue
			}
			pushes++
			if strings.Contains(l, "--force") || strings.Contains(l, " -f ") {
				t.Fatalf("%s:%d forces a push: %s", n, i+1, l)
			}
			for _, w := range strings.Fields(l) {
				if strings.HasPrefix(strings.Trim(w, `"`), "+") && !strings.Contains(w, "refs/heads/${staging}") {
					t.Fatalf("%s:%d forces refspec %s; only the staging branch may be replaced", n, i+1, w)
				}
			}
		}
		if n == "release-region.sh" && pushes != 2 {
			t.Fatalf("%s has %d push lines, want 2 (stage and promote); a new push needs this test to read it", n, pushes)
		}
	}
}

// The pivot report counts the files the pivot commit changes. rewrite_kit_tree
// raises its edit counter for every trust re-injection, also one that changes
// nothing: the first delivery to hw307 reported 66 and 67 "edits" for a pivot
// of a few new files.
func TestReleasePivotReportCountsTheFilesItChanges(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(f *fixture) func(w string)
		want   []string
	}{
		{"a candidate that only moves a pin", func(f *fixture) func(w string) {
			return func(w string) { sed(f.t, w+"/"+kitDir+"/10-bp-foo.yaml", "version: 1.0.0", "version: 1.1.0") }
		}, nil},
		{"a candidate that adds a slot", func(f *fixture) func(w string) { return f.laterRelease }, []string{kitDir + "/20-bp-bar.yaml"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			id, _ := f.candidate(1, c.change(f))
			out, code := f.region(shells()[0], "PHASE=stage", "CANDIDATE="+id)
			r := resultOf(out)
			if code != 0 || (r["phase"] != "staged" && r["phase"] != "up-to-date") {
				t.Fatalf("stage: exit %d, result %v\n%s", code, r, out)
			}
			want := fmt.Sprintf("release-report region=primary pivot %d file(s) changed, registry %s", len(c.want), localPrefix)
			if !strings.Contains(out, want) {
				t.Fatalf("the report lacks %q:\n%s", want, out)
			}
			// The count is the pivot commit's: the files it changes, nothing else.
			top := f.git(f.dir, "--git-dir="+f.repo, "log", "-1", "--format=%s", r["staged"])
			var files []string
			if strings.HasPrefix(top, "release: this Sovereign's pivot") {
				files = strings.Fields(f.git(f.dir, "--git-dir="+f.repo, "diff", "--name-only", r["staged"]+"^", r["staged"]))
			}
			if strings.Join(files, " ") != strings.Join(c.want, " ") {
				t.Fatalf("the pivot commit (%q) changes %v, want %v", top, files, c.want)
			}
		})
	}
}
