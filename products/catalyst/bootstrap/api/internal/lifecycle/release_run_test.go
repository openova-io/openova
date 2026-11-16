package lifecycle

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// kubectlStub stands for kubectl in release-run.sh (a shell function named
// kubectl runs it, and STUB_DIR/bin/kubectl for the processes it starts). It
// answers the token read and the read of each region's registry configuration
// (STUB_DIR/registry-<region>.yml), keeps the Secrets of each region under
// STUB_DIR/secrets/<region>/<ns>/ (the region is the basename of --kubeconfig;
// "primary" without one),
// records every status patch, and runs a follower Job it is asked to apply:
// release-region.sh with the plain environment of the applied manifest (its
// /release paths mapped to STUB_RELEASE) and GITEA_PAT, the way the follower
// region's kubelet would. Its logs and its outcome are then what get and logs
// answer.
const kubectlStub = `#!/bin/sh
echo "kubectl $*" >>"${STUB_DIR}/calls"
region=primary
[ "$1" = "--kubeconfig" ] && { region=$(basename "$2" .yaml); shift 2; }
case " $* " in
  *" get configmap harbor-registry "*) cat "${STUB_DIR}/registry-${region}.yml" 2>/dev/null || exit 1 ;;
  *" get secret "*jsonpath*) printf '%s' "${STUB_PAT}" | base64 ;;
  *" get secret "*)
    f="${STUB_DIR}/secrets/${region}/$2/$5.json"
    [ -f "${f}" ] || { echo "Error from server (NotFound): secrets \"$5\" not found" >&2; exit 1; }
    case " $* " in *" -o name "*) echo "secret/$5" ;; *) cat "${f}" ;; esac ;;
  *" delete secret "*) rm -f "${STUB_DIR}/secrets/${region}/$2/$5.json" ;;
  *" patch configmap "*)
    while [ $# -gt 0 ]; do [ "$1" = "-p" ] && printf '%s\n' "$2" >>"${STUB_DIR}/status"; shift; done ;;
  *" create configmap "*) echo "kind: ConfigMap" ;;
  *" delete job "*) rm -f "${STUB_DIR}/job-$5".* ;;
  *" apply --server-side "*)
    cat >"${STUB_DIR}/applied"
    if jq -e '.kind == "Secret"' "${STUB_DIR}/applied" >/dev/null 2>&1; then
      to=$(jq -r '.metadata.namespace + "/" + .metadata.name' "${STUB_DIR}/applied")
      mkdir -p "${STUB_DIR}/secrets/${region}/${to%/*}"
      cp "${STUB_DIR}/applied" "${STUB_DIR}/secrets/${region}/${to}.json"
      exit 0
    fi
    grep -q '^kind: Job' "${STUB_DIR}/applied" || exit 0
    name=$(awk '/^metadata:/ {m=1; next} m && /^  name:/ {print $2; exit}' "${STUB_DIR}/applied")
    cp "${STUB_DIR}/applied" "${STUB_DIR}/job-${name}.yaml"
    env_file="${STUB_DIR}/job-${name}.env"
    awk '/^ *- name: / {n=$3; next} n != "" && /^ *value:/ {v=$0; sub(/^ *value: */, "", v); gsub(/^["'"'"']|["'"'"']$/, "", v); print n "=" v; n=""; next} {n=""}' \
      "${STUB_DIR}/applied" | sed "s#=/release/#=${STUB_RELEASE}/#" >"${env_file}"
    rc=0
    ( set -a; . "${env_file}"; GITEA_PAT="${STUB_PAT}"; WORK=$(mktemp -d); export GITEA_PAT WORK; sh "${STUB_RELEASE}/release-region.sh" ) \
      >"${STUB_DIR}/job-${name}.log" 2>&1 || rc=$?
    echo "${rc}" >"${STUB_DIR}/job-${name}.rc" ;;
  *" get job "*)
    rc=$(cat "${STUB_DIR}/job-$5.rc" 2>/dev/null || echo running)
    case "$*" in
      *succeeded*) [ "${rc}" = 0 ] && echo 1 ;;
      *failed*) [ "${rc}" != 0 ] && [ "${rc}" != running ] && echo 1 ;;
    esac
    exit 0 ;;
  *" logs "*) n="${4#job/}"; cat "${STUB_DIR}/job-${n}.log" ;;
  *) echo "kubectl stub: unexpected call: $*" >&2; exit 9 ;;
esac
`

// follower is a second region: its own Gitea, whose main is the installed
// release, the follower pivot and a commit of its own.
func (f *fixture) follower(name string) (url, main string) {
	f.t.Helper()
	repo := f.dir + "/gitea-" + name + "/openova/openova.git"
	f.git(f.dir, "init", "-q", "--bare", repo)
	w := f.dir + "/follower-" + name
	f.git(f.dir, "clone", "-q", "-b", "main", f.upstream, w)
	cmd := exec.Command("sh", "-c", `. "$KIT"; edited=0; local_prefix="$LP"; trust_secret="$TS"; rewrite_kit_tree `+kitDir)
	cmd.Dir = w
	cmd.Env = append(os.Environ(), "KIT="+f.scripts+"/kit-rewrite.sh", "LP="+localPrefix, "TS="+trustSecret, "UPSTREAM_PREFIX="+upstreamPrefix)
	if out, err := cmd.CombinedOutput(); err != nil {
		f.t.Fatalf("follower pivot: %v\n%s", err, out)
	}
	f.git(w, "commit", "-q", "-am", "cutover: pivot HelmRepository URLs (secondary region "+name+")")
	f.write(w, "region-notes.txt", name+"\n")
	f.git(w, "add", "-A")
	f.git(w, "commit", "-q", "-m", "a commit of region "+name)
	f.git(w, "push", "-q", repo, "HEAD:refs/heads/main")
	return "file://" + f.dir + "/gitea-" + name, f.git(w, "rev-parse", "HEAD")
}

// followerPivotJob stands for secondary-pivot-job.yaml of the cutover chart.
func followerPivotJob(giteaURL string) *batchv1.Job {
	return &batchv1.Job{
		TypeMeta:   metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"},
		ObjectMeta: metav1.ObjectMeta{Name: "cutover-helmrepo-pivot-secondary-__REGION__", Namespace: "gitea", Labels: map[string]string{"app.kubernetes.io/managed-by": "flux"}},
		Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name: "secondary-helmrepo-pivot", Image: "alpine/k8s:1.31.4",
				Command: []string{"/bin/sh", "/secondary-pivot/secondary-pivot.sh"},
				Env: []corev1.EnvVar{
					plain("HOME", "/tmp"), plain("REGION", "__REGION__"), plain("UPSTREAM_BRANCH", "main"),
					plain("GITEA_INTERNAL_URL", giteaURL), plain("GITEA_ORG", "openova"), plain("GITEA_REPO", "openova"),
					plain("GITEA_USERNAME", "gitea_admin"),
					{Name: "GITEA_PAT", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "cutover-secondary-gitea-pat"}, Key: "token"}}},
					plain("UPSTREAM_PREFIX", upstreamPrefix), plain("LOCAL_PREFIX", "__LOCAL_PREFIX__"), plain("TRUST_SECRET", "__TRUST_SECRET__"),
					plain("FOLLOWER_SOURCE_DIRS", ""), plain("IMAGE_REGISTRY_SLOT_FILES", ""),
				},
				VolumeMounts: []corev1.VolumeMount{{Name: "secondary-pivot", MountPath: "/secondary-pivot"}},
			}},
			Volumes: []corev1.Volume{{Name: "secondary-pivot"}},
		}}},
	}
}

type runFixture struct {
	*fixture
	stub, kubeconfigs, state string
	followerMain             string
	followerURL              string
	args, env                []string // added to every run's arguments and environment
}

func (r *runFixture) run(phase string, req releaseRequest) (string, int) {
	r.t.Helper()
	return r.runArgs(append([]string{phase}, r.args...), req, r.env...)
}

// runArgs runs release-run.sh with args, the environment of the release Job's
// containers, and extra variables (NAME=value) on top.
func (r *runFixture) runArgs(args []string, req releaseRequest, extra ...string) (string, int) {
	r.t.Helper()
	cmd := command("sh", append([]string{"-c", `kubectl() { sh "${STUB_DIR}/kubectl" "$@"; }; set -- "$0" "$@"; . "${RELEASE_SCRIPTS}/release-run.sh"`}, args...)...)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(), "PATH="+r.stub+"/bin:"+os.Getenv("PATH"), "HOME="+r.home, "GIT_CONFIG_NOSYSTEM=1", "STUB_DIR="+r.stub, "STUB_PAT=not-a-real-token", "STUB_RELEASE="+r.scripts,
		"RELEASE_NAMESPACE=catalyst", "RELEASE_STATUS_CM=sovereign-release", "CANDIDATE="+req.Candidate, "DRY_RUN="+boolString(req.DryRun),
		"STATE="+r.state, "SECONDARY_KUBECONFIGS="+r.kubeconfigs, "RELEASE_SCRIPTS="+r.scripts, "FOLLOWER_POLL_SECONDS=1", "FOLLOWER_WAIT_SECONDS=60",
		"KIT_REWRITE="+r.scripts+"/kit-rewrite.sh", "PIN_EXTRACTOR="+r.scripts+"/extract-pins.sh",
		"PAT_SOURCE_NAMESPACE=catalyst-system", "PAT_SOURCE_SECRET_NAME=catalyst-gitea-token", "PAT_SOURCE_KEY=token",
		"UPSTREAM_REPO_URL=file://"+r.upstream, "UPSTREAM_BRANCH=main", "UPSTREAM_PREFIX="+upstreamPrefix,
		"GITEA_INTERNAL_URL="+r.gitea, "GITEA_ORG=openova", "GITEA_REPO=openova", "GITEA_USERNAME=gitea_admin")
	cmd.Env = append(cmd.Env, extra...)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		r.t.Fatal(err)
	}
	return string(out), code
}

// status folds the recorded patches into the ConfigMap data.
func (r *runFixture) status() map[string]string {
	r.t.Helper()
	data := map[string]string{}
	b, _ := os.ReadFile(r.stub + "/status")
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l == "" {
			continue
		}
		var p struct{ Data map[string]string }
		if err := json.Unmarshal([]byte(l), &p); err != nil {
			r.t.Fatalf("a status patch is not JSON: %s", l)
		}
		for k, v := range p.Data {
			data[k] = v
		}
	}
	return data
}

func (r *runFixture) followerRef(ref string) string {
	out := r.git(r.dir, "ls-remote", r.dir+"/gitea-region-b/openova/openova.git", "refs/heads/"+ref)
	if out == "" {
		return ""
	}
	return strings.Fields(out)[0]
}

func TestReleaseRunDeliversToEveryRegion(t *testing.T) {
	f := newFixture(t)
	id, cand := f.candidate(1, f.laterRelease)
	req := releaseRequest{Candidate: id}
	r := newRunFixtureFrom(t, f, req, false)

	out, code := r.run("stage", req)
	st := r.status()
	if code != 0 || st["phase"] != "staged" {
		t.Fatalf("stage: exit %d, phase %q\n%s", code, st["phase"], out)
	}
	if !strings.Contains(st["primary.stage"], "phase=staged") || !strings.Contains(st["region-b.stage"], "phase=staged") {
		t.Fatalf("stage results: primary %q, region-b %q", st["primary.stage"], st["region-b.stage"])
	}
	if !strings.Contains(st["region-b.stage.report"], "chart bp-foo 1.0.0 -> 1.1.0") {
		t.Fatalf("the follower's report is not recorded: %q", st["region-b.stage.report"])
	}
	if r.remote("main") != f.main || r.followerRef("main") != r.followerMain {
		t.Fatal("stage moved a main")
	}
	bStaged := r.followerRef("release/" + id)
	if bStaged == "" || bStaged == r.remote("release/"+id) {
		t.Fatalf("region b staged %q; it must stage its own merge, not the primary's %q", bStaged, r.remote("release/"+id))
	}
	if !strings.Contains(r.git(r.dir, "--git-dir="+r.dir+"/gitea-region-b/openova/openova.git", "show", bStaged+":region-notes.txt"), "region-b") {
		t.Fatal("region b's own commit is not in its staged release")
	}
	applied, err := os.ReadFile(r.stub + "/job-sovereign-release-stage-region-b.yaml")
	if err != nil {
		t.Fatalf("no follower Job was applied: %v", err)
	}
	for _, bad := range []string{"__PHASE__", "__REGION__", "__JOB__", "LOCAL_PREFIX", "/secondary-pivot/"} {
		if strings.Contains(string(applied), bad) {
			t.Fatalf("the applied follower Job still holds %s:\n%s", bad, applied)
		}
	}

	out, code = r.run("promote", req)
	st = r.status()
	if code != 0 || st["phase"] != "delivered" {
		t.Fatalf("promote: exit %d, phase %q\n%s", code, st["phase"], out)
	}
	if r.remote("main") != r.remote("release/"+id) || r.followerRef("main") != bStaged {
		t.Fatalf("after promote: primary main %s (staged %s), region b main %s (staged %s)", r.remote("main"), r.remote("release/"+id), r.followerRef("main"), bStaged)
	}
	if err := command("git", "--git-dir="+f.repo, "merge-base", "--is-ancestor", cand, r.remote("main")).Run(); err != nil {
		t.Fatal("the primary does not run the candidate after promote")
	}
	check, err := os.ReadFile(r.stub + "/job-sovereign-release-check-region-b.yaml")
	if err != nil || !strings.Contains(string(check), bStaged) || !strings.Contains(string(check), r.followerMain) {
		t.Fatalf("the follower check did not get the staged and main commits recorded at stage (%v):\n%s", err, check)
	}
	if _, err := os.Stat(r.stub + "/job-sovereign-release-promote-region-b.yaml"); err != nil {
		t.Fatalf("the follower was not promoted: %v", err)
	}
}

func TestReleaseRunConflictInAFollowerMovesNoRegion(t *testing.T) {
	f := newFixture(t)
	id, _ := f.candidate(1, f.laterRelease)
	req := releaseRequest{Candidate: id}
	r := newRunFixtureFrom(t, f, req, true)
	out, code := r.run("stage", req)
	st := r.status()
	if code == 0 || st["phase"] != "conflict" {
		t.Fatalf("stage: exit %d, phase %q, want a conflict\n%s", code, st["phase"], out)
	}
	if !strings.Contains(st["region-b.stage.report"], "conflict "+kitDir+"/10-bp-foo.yaml") {
		t.Fatalf("the follower's conflict is not named by file: %q", st["region-b.stage.report"])
	}
	if r.remote("main") != f.main || r.followerRef("main") != r.followerMain {
		t.Fatal("a conflict in one region moved a main")
	}
}

func TestReleaseRunDryRun(t *testing.T) {
	f := newFixture(t)
	id, _ := f.candidate(1, f.laterRelease)
	req := releaseRequest{Candidate: id, DryRun: true}
	r := newRunFixtureFrom(t, f, req, false)
	out, code := r.run("stage", req)
	st := r.status()
	if code != 0 || st["phase"] != "dry-run" || !strings.Contains(st["region-b.stage"], "phase=dry-run") {
		t.Fatalf("dry run: exit %d, phase %q, region b %q\n%s", code, st["phase"], st["region-b.stage"], out)
	}
	if r.remote("release/"+id) != "" || r.followerRef("release/"+id) != "" {
		t.Fatal("a dry run pushed a staging branch")
	}
}

func newRunFixtureFrom(t *testing.T, f *fixture, req releaseRequest, conflict bool) *runFixture {
	r := &runFixture{fixture: f, stub: f.dir + "/stub", kubeconfigs: f.dir + "/kubeconfigs", state: f.dir + "/state"}
	r.followerURL, r.followerMain = f.follower("region-b")
	if conflict {
		w := f.dir + "/follower-region-b"
		sed(t, w+"/"+kitDir+"/10-bp-foo.yaml", "version: 1.0.0", "version: 1.0.1")
		f.git(w, "commit", "-q", "-am", "region b pins its own bp-foo")
		f.git(w, "push", "-q", f.dir+"/gitea-region-b/openova/openova.git", "HEAD:refs/heads/main")
		r.followerMain = f.git(w, "rev-parse", "HEAD")
	}
	for _, d := range []string{r.stub, r.kubeconfigs, r.state} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	f.write(r.stub, "kubectl", kubectlStub)
	f.write(r.stub, "bin/kubectl", "#!/bin/sh\nexec sh \"${STUB_DIR}/kubectl\" \"$@\"\n")
	if err := os.Chmod(r.stub+"/bin/kubectl", 0o700); err != nil { //nolint:gosec // a fake program must be executable
		t.Fatal(err)
	}
	f.write(r.kubeconfigs, "region-b.yaml", "apiVersion: v1\nkind: Config\n")
	in := &releaseInputs{mirror: corev1.Container{Env: []corev1.EnvVar{plain("UPSTREAM_REPO_URL", "file://"+f.upstream)}}, follower: followerPivotJob(r.followerURL), followerNS: "gitea"}
	m, err := in.followerJobManifest(req)
	if err != nil {
		t.Fatal(err)
	}
	f.write(f.scripts, "follower-job.yaml", m)
	return r
}

// registryConfig is the config.yml of a region's Harbor registry, storing in
// the bucket named (or on a local disk when bucket is "").
func registryConfig(bucket string) string {
	if bucket == "" {
		return "version: 0.1\nstorage:\n  filesystem:\n    rootdirectory: /storage\n  cache:\n    layerinfo: redis\nhttp:\n  addr: :5000\n"
	}
	return "version: 0.1\nlog:\n  level: info\nstorage:\n  s3:\n    region: me-east-1\n    bucket: " + bucket +
		"\n    regionendpoint: https://obs.example.test\n    secure: true\n  cache:\n    layerinfo: redis\n  maintenance:\n    uploadpurging:\n      enabled: true\nhttp:\n  addr: :5000\n"
}

// A follower whose registry stores in the primary's bucket serves what the
// primary's prewarm copies, even when the installed step 03 mirrors a
// follower's running set only: the state must not report it as a gap (the
// first delivery to hw307 did, though both regions' Harbor store in one bucket).
func TestReleaseStageTellsWhetherAFollowerRegistrySharesThePrimaryStorage(t *testing.T) {
	for _, c := range []struct {
		name, primary, follower string
		covered                 bool
		want                    string
	}{
		{"one bucket", registryConfig("sovereign-registry"), registryConfig("sovereign-registry"), false, "covered by shared storage: every follower's registry stores in the primary's (s3 bucket=sovereign-registry endpoint=https://obs.example.test root=)"},
		{"two buckets", registryConfig("sovereign-registry"), registryConfig("region-b-registry"), false, "not covered: the registry of region-b does not store in the primary's (s3 bucket=sovereign-registry"},
		{"local disks", registryConfig(""), registryConfig(""), false, "not covered: the registry of region-b does not store in the primary's (filesystem"},
		{"unreadable", registryConfig("sovereign-registry"), "", false, "unknown: the registry storage of the primary or of region-b cannot be read (harbor/harbor-registry)"},
		{"step 03 covers the follower", registryConfig("sovereign-registry"), registryConfig("region-b-registry"), true, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			id, _ := f.candidate(1, f.laterRelease)
			req := releaseRequest{Candidate: id, DryRun: true}
			r := newRunFixtureFrom(t, f, req, false)
			f.write(r.stub, "registry-primary.yml", c.primary)
			if c.follower != "" {
				f.write(r.stub, "registry-region-b.yml", c.follower)
			}
			r.env = []string{"FOLLOWER_PREWARM_COVERED=" + boolString(c.covered)}
			out, code := r.run("stage", req)
			st := r.status()
			if code != 0 || st["phase"] != "dry-run" {
				t.Fatalf("stage: exit %d, phase %q\n%s", code, st["phase"], out)
			}
			if c.want == "" {
				if v, ok := st["followerPrewarm"]; ok {
					t.Fatalf("the stage rewrote the route's verdict although step 03 covers the follower: %q", v)
				}
				return
			}
			if !strings.HasPrefix(st["followerPrewarm"], c.want) {
				t.Fatalf("followerPrewarm %q, want it to begin %q\n%s", st["followerPrewarm"], c.want, out)
			}
		})
	}
}
