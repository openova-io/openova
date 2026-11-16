package lifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A delivery runs the mechanism of the release it delivers (#7938). The second
// delivery to hw307 failed in the INSTALLED step 03, whose repair was in the
// candidate it delivered: a broken step 03 locked the Sovereign out of every
// later release. Here the installed step 03 fails whenever it runs, and the
// delivery still succeeds, with the candidate's step 03 and release scripts.
func TestReleaseDeliveryRunsTheCandidatesMechanism(t *testing.T) {
	f := newFixture(t)
	id, cand := f.candidate(1, f.candidateCutoverRelease(step03Marker(candidateCutover)))
	req := releaseRequest{Candidate: id, PullSecret: releasePull}
	r := newRunFixtureFrom(t, f, req, false)
	r.env = []string{"PULL_SECRET=" + releasePull}
	r.putSecret("primary", testNS, releasePull, "openova-bot")
	// The installed step 03, as the cluster holds it: it fails whenever it runs.
	f.write(r.stub+"/cm", "cutover-step-03-harbor-prewarm.json", `{"apiVersion": "v1", "kind": "ConfigMap",
  "metadata": {"name": "cutover-step-03-harbor-prewarm", "namespace": "catalyst"},
  "data": {"run.sh": "echo installed step 03 ran; touch \"${STUB_DIR}/step03-ran-installed\"; exit 1\n"}}`)

	out, code := r.run("stage", req)
	st := r.status()
	if code != 0 || st["phase"] != "staged" {
		t.Fatalf("stage: exit %d, phase %q, message %q\n%s", code, st["phase"], st["message"], out)
	}
	if !strings.HasPrefix(st["mechanism"], "bp-self-sovereign-cutover "+candidateCutover+" (chart tree ") || !strings.Contains(st["mechanism"], "the candidate's") {
		t.Fatalf("the state does not name the candidate's mechanism: %q", st["mechanism"])
	}

	out, code = r.run("deliver", req)
	logs := r.jobLog(req)
	st = r.status()
	if code != 0 || st["phase"] != "delivered" {
		t.Fatalf("deliver: exit %d, phase %q\n%s\n--- delivery Job ---\n%s", code, st["phase"], out, logs)
	}
	if _, err := os.Stat(r.stub + "/step03-ran-" + candidateCutover); err != nil {
		t.Fatalf("the candidate's step 03 did not run:\n%s", logs)
	}
	for _, v := range []string{"installed", installedCutover} {
		if _, err := os.Stat(r.stub + "/step03-ran-" + v); err == nil {
			t.Fatalf("the %s step 03 ran", v)
		}
	}
	if st["deliveryJob"] != "sovereign-release-delivery" {
		t.Fatalf("the state does not name the delivery Job: %q", st["deliveryJob"])
	}
	if err := command("git", "--git-dir="+f.repo, "merge-base", "--is-ancestor", cand, r.remote("main")).Run(); err != nil {
		t.Fatal("the primary does not run the candidate after the delivery")
	}
	if r.followerRef("main") == r.followerMain {
		t.Fatal("the follower was not promoted")
	}
	// What the delivery mounted is the render's, under the -release names; no
	// installed ConfigMap was replaced.
	b, err := os.ReadFile(filepath.Clean(r.stub + "/cm/cutover-step-03-harbor-prewarm.json"))
	if err != nil || !strings.Contains(string(b), "installed step 03 ran") {
		t.Fatalf("the delivery replaced the installed step 03 ConfigMap: %s", b)
	}
	b, err = os.ReadFile(filepath.Clean(r.stub + "/cm/cutover-step-03-harbor-prewarm-release.json"))
	if err != nil || !strings.Contains(string(b), "bp-self-sovereign-cutover "+candidateCutover) {
		t.Fatalf("the delivery did not mount the candidate's step 03 (%v): %s", err, b)
	}
}

// The bootstrap refuses, by name, a mechanism it cannot verify; a dry run says
// so too, and nothing moves.
func TestReleaseStageRefusesAMechanismItCannotVerify(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(f *fixture) func(w string)
		setup  func(r *runFixture)
		want   string
	}{
		{"a chart that is not the one the candidate pins", func(f *fixture) func(w string) {
			return func(w string) {
				f.candidateCutoverRelease(step03Marker(candidateCutover))(w)
				sed(f.t, w+"/"+cutoverSlotFile, "version: "+candidateCutover, "version: 0.1.901")
			}
		}, nil, "is bp-self-sovereign-cutover " + candidateCutover + ", but " + cutoverSlotFile + " pins bp-self-sovereign-cutover 0.1.901"},
		{"a candidate whose chart predates the mechanism", func(f *fixture) func(w string) {
			return func(w string) {
				f.candidateCutoverRelease(step03Marker(candidateCutover))(w)
				if err := os.Remove(w + "/" + cutoverChartPath + "/files/release/contract"); err != nil {
					f.t.Fatal(err)
				}
			}
		}, nil, "carries no release mechanism of contract 1"},
		{"a mechanism a commit of the Sovereign changed", func(f *fixture) func(w string) {
			return f.candidateCutoverRelease(step03Marker(candidateCutover))
		}, func(r *runFixture) {
			s := r.dir + "/sovereign"
			r.write(s, cutoverChartPath+"/templates/extra.yaml", "# a Sovereign's own change\n")
			r.git(s, "add", "-A")
			r.git(s, "commit", "-q", "-m", "sovereign-admin: change the cutover chart")
			r.git(s, "push", "-q", r.repo, "HEAD:refs/heads/main")
		}, "differs from the candidate's: a commit of this Sovereign changes the release mechanism"},
		{"no installed cutover HelmRelease", func(f *fixture) func(w string) {
			return f.candidateCutoverRelease(step03Marker(candidateCutover))
		}, func(r *runFixture) {
			if err := os.Remove(r.stub + "/cutover-hr.json"); err != nil {
				r.t.Fatal(err)
			}
		}, "cannot read the installed cutover HelmRelease flux-system/bp-self-sovereign-cutover"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			id, _ := f.candidate(1, c.change(f))
			req := releaseRequest{Candidate: id, DryRun: true}
			r := newRunFixtureFrom(t, f, req, false)
			if c.setup != nil {
				c.setup(r)
			}
			before := r.remote("main")
			out, code := r.run("stage", req)
			st := r.status()
			if code == 0 || st["phase"] != "failed" || st["failedStep"] != "stage" || !strings.Contains(st["mechanism"], c.want) || !strings.Contains(st["message"], c.want) {
				t.Fatalf("stage: exit %d, phase %q, failedStep %q, mechanism %q; want a refusal naming %q\n%s", code, st["phase"], st["failedStep"], st["mechanism"], c.want, out)
			}
			if r.remote("main") != before || r.remote("release/"+id) != "" {
				t.Fatal("a refused mechanism moved or pushed a branch")
			}
		})
	}
}
