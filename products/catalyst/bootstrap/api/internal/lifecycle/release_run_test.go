package lifecycle

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
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
// answer. Every call is also kept in STUB_DIR/calls.rec as its arguments and
// the kind and namespace of the object it read on stdin (release_rbac_test.go
// checks each against the RBAC of the account that makes it). A server-side
// dry run stores nothing: the object is kept as STUB_DIR/dry/<region>.<kind>.
// <name>.json for the test, and refused, as the API server would, when that
// name matches a line (an ERE) of STUB_DIR/refuse. STUB_DIR/pods.json, when
// present, is what a read of a Job's Pods answers.
const kubectlStub = `#!/bin/sh
echo "kubectl $*" >>"${STUB_DIR}/calls"
obj=""
case " $* " in
  *" -f - "*)
    cat >"${STUB_DIR}/applied"
    obj=$(jq -r '.kind + " " + (.metadata.namespace // "")' "${STUB_DIR}/applied" 2>/dev/null) \
      || obj=$(awk '/^kind:/ {k = $2} /^  namespace:/ && n == "" {n = $2} END {print k " " n}' "${STUB_DIR}/applied") ;;
esac
{ printf '%s\037' "$@"; printf '\035%s\036' "${obj}"; } >>"${STUB_DIR}/calls.rec"
region=primary
[ "$1" = "--kubeconfig" ] && { region=$(basename "$2" .yaml); shift 2; }
case " $* " in
  *" --dry-run=server "*)
    n="${region}.$(jq -r '.kind + "." + (.metadata.name // .metadata.generateName)' "${STUB_DIR}/applied")"
    mkdir -p "${STUB_DIR}/dry"; cp "${STUB_DIR}/applied" "${STUB_DIR}/dry/${n}.json"
    if [ -s "${STUB_DIR}/refuse" ] && printf '%s\n' "${n}" | grep -qEf "${STUB_DIR}/refuse"; then
      echo "Error from server (Invalid): ${n} is invalid: refused by the test" >&2; exit 1
    fi
    exit 0 ;;
  *" auth whoami "*)
    if [ "${region}" = primary ]; then echo "system:serviceaccount:catalyst:bp-self-sovereign-cutover-runner"; else echo "system:serviceaccount:kube-system:handover-peer"; fi
    exit 0 ;;
  *" get pods "*) cat "${STUB_DIR}/pods.json" 2>/dev/null || echo '{"items": []}'; exit 0 ;;
esac
case " $* " in
  *" get configmap harbor-registry "*) cat "${STUB_DIR}/registry-${region}.yml" 2>/dev/null || exit 1 ;;
  *" get helmreleases.helm.toolkit.fluxcd.io "*) cat "${STUB_DIR}/cutover-hr.json" 2>/dev/null || exit 1 ;;
  *" get configmap sovereign-release "*)
    k=$(printf '%s\n' "$*" | sed -n 's/.*{\.data\.\([A-Za-z0-9.]*\)}.*/\1/p'); [ -n "${k}" ] || k=phase
    jq -rs --arg k "${k}" '[.[] | .data[$k] // empty] | last // ""' "${STUB_DIR}/status" 2>/dev/null ;;
  *" get secret "*jsonpath*) printf '%s' "${STUB_PAT}" | base64 ;;
  *" get secret "*)
    f="${STUB_DIR}/secrets/${region}/$2/$5.json"
    [ -f "${f}" ] || { echo "Error from server (NotFound): secrets \"$5\" not found" >&2; exit 1; }
    case " $* " in *" -o name "*) echo "secret/$5" ;; *) cat "${f}" ;; esac ;;
  *" delete secret "*) rm -f "${STUB_DIR}/secrets/${region}/$2/$5.json" ;;
  *" patch configmap "*)
    while [ $# -gt 0 ]; do [ "$1" = "-p" ] && printf '%s\n' "$2" >>"${STUB_DIR}/status"; shift; done ;;
  *" create configmap "*)
    n=""; p=""; for a in "$@"; do [ "$p" = "-n" ] && n="$a"; p="$a"; done
    printf 'kind: ConfigMap\nmetadata:\n  namespace: %s\n' "$n" ;;
  *" delete job "*) rm -f "${STUB_DIR}/job-$5".* ;;
  *" apply --server-side "*|*" create -f - "*)
    if jq -e '.kind == "Secret"' "${STUB_DIR}/applied" >/dev/null 2>&1; then
      to=$(jq -r '.metadata.namespace + "/" + .metadata.name' "${STUB_DIR}/applied")
      mkdir -p "${STUB_DIR}/secrets/${region}/${to%/*}"
      cp "${STUB_DIR}/applied" "${STUB_DIR}/secrets/${region}/${to}.json"
      exit 0
    fi
    if jq -e '.kind == "ConfigMap"' "${STUB_DIR}/applied" >/dev/null 2>&1; then
      d="${STUB_DIR}/cm"; [ "${region}" = primary ] || d="${STUB_DIR}/cm-${region}"
      mkdir -p "${d}"; cp "${STUB_DIR}/applied" "${d}/$(jq -r .metadata.name "${STUB_DIR}/applied").json"
      exit 0
    fi
    if [ "${region}" = primary ] && jq -e '.kind == "Job"' "${STUB_DIR}/applied" >/dev/null 2>&1; then
      name=$(jq -r .metadata.name "${STUB_DIR}/applied")
      if [ -f "${STUB_DIR}/job-${name}.json" ]; then
        echo "Error from server (AlreadyExists): error when creating \"STDIN\": jobs.batch \"${name}\" already exists" >&2; exit 1
      fi
      cp "${STUB_DIR}/applied" "${STUB_DIR}/job-${name}.json"
      sh "${STUB_DIR}/kubelet" "${STUB_DIR}/job-${name}.json"
      exit 0
    fi
    # A follower Job: YAML (the route's manifest) or JSON (the candidate's).
    if jq -e '.kind == "Job"' "${STUB_DIR}/applied" >/dev/null 2>&1; then
      cp "${STUB_DIR}/applied" "${STUB_DIR}/follower-job.json"
    else
      grep -q '^kind: Job' "${STUB_DIR}/applied" || exit 0
      yq -o=json '.' "${STUB_DIR}/applied" >"${STUB_DIR}/follower-job.json"
    fi
    name=$(jq -r .metadata.name "${STUB_DIR}/follower-job.json")
    cp "${STUB_DIR}/applied" "${STUB_DIR}/job-${name}.yaml"
    env_file="${STUB_DIR}/job-${name}.env"
    jq -r --arg r "${STUB_RELEASE}" '.spec.template.spec.containers[0].env[] | select(.value != null)
      | "\(.name)=\(.value | if startswith("/release/") then $r + "/" + .[9:] else . end | @sh)"' "${STUB_DIR}/follower-job.json" >"${env_file}"
    rc=0
    ( set -a; . "${env_file}"; GITEA_PAT="${STUB_PAT}"; WORK=$(mktemp -d); export GITEA_PAT WORK; sh "${STUB_RELEASE}/release-region.sh" ) \
      >"${STUB_DIR}/job-${name}.log" 2>&1 || rc=$?
    echo "${rc}" >"${STUB_DIR}/job-${name}.rc" ;;
  *" get job "*)
    rc=$(cat "${STUB_DIR}/job-$5.rc" 2>/dev/null || echo running)
    if [ ! -f "${STUB_DIR}/job-$5.json" ] && [ ! -f "${STUB_DIR}/job-$5.yaml" ]; then
      echo "Error from server (NotFound): jobs.batch \"$5\" not found" >&2; exit 1
    fi
    case " $* " in
      *" -o name "*) echo "job.batch/$5" ;;
      *" -o json "*) cat "${STUB_DIR}/job-$5.json" ;;
      *conditions*) case "${rc}" in 0) echo "SuccessCriteriaMet Complete " ;; running) ;; *) echo "FailureTarget Failed " ;; esac ;;
      *) echo "kubectl stub: unexpected read of a Job: $*" >&2; exit 9 ;;
    esac
    exit 0 ;;
  *" logs "*) n="${4#job/}"; cat "${STUB_DIR}/job-${n}.log" ;;
  *) echo "kubectl stub: unexpected call: $*" >&2; exit 9 ;;
esac
`

// kubeletStub runs a Job the release applies as JSON in the primary region (the
// delivery Job of the candidate's mechanism) the way a kubelet would: its
// ConfigMap volumes from what was applied (STUB_DIR/cm), the follower
// kubeconfigs Secret from STUB_KUBECONFIGS, emptyDirs as directories; then each
// init container and container in order, with only its own environment (a
// secretKeyRef read from the stub's Secrets of its namespace) and every
// mountPath in its command, arguments and values mapped to its volume's
// directory. A secretKeyRef to a Secret that does not exist is empty when it is
// optional and keeps the container from starting otherwise, as on a node. The
// first container that fails ends the Pod; job-<name>.rc and job-<name>.log are
// what get job and logs answer. STUB_EVICT=<container> evicts the Pod before
// that container starts (a drained node: DisruptionTarget, its containers
// killed with 143), in the first Pod only, or in every Pod with
// STUB_EVICT_EVERY=1; the Job then replaces the Pod, with new emptyDirs, as its
// pod failure policy and backoffLimit decide, as the Job controller does.
const kubeletStub = `#!/bin/sh
set -u
j="$1"; name=$(jq -r .metadata.name "$j"); ns=$(jq -r .metadata.namespace "$j")
log="${STUB_DIR}/job-${name}.log"; : >"$log"
end() { echo "$1" >"${STUB_DIR}/job-${name}.rc"; exit 0; }
# retried: whether the Job replaces a Pod an eviction took: the first rule of
# its pod failure policy that matches the Pod (its DisruptionTarget, or the 143
# of its killed container) decides; none matching counts toward backoffLimit.
retried() {
  jq -e --argjson n "$1" 'def m: if .onPodConditions then any(.onPodConditions[]; .type == "DisruptionTarget")
      elif .onExitCodes then ((.onExitCodes.values | index(143)) as $i | if .onExitCodes.operator == "In" then $i != null else $i == null end)
      else false end;
    (.spec.backoffLimit // 6) as $b | ([(.spec.podFailurePolicy.rules // [])[] | select(m)] | first // {action: "Count"}) as $r |
    if $r.action == "Ignore" then true elif $r.action == "Count" then $n <= $b else false end' "$j" >/dev/null
}
# pod <attempt>: run one Pod of the Job; its exit is the first failing
# container's, 200 when it was evicted.
pod() {
  root="${STUB_DIR}/pod-${name}-$1"; mkdir -p "$root/vol"
  jq -c '.spec.template.spec.volumes[]' "$j" >"$root/vols"
  while IFS= read -r v; do
    vn=$(printf '%s' "$v" | jq -r .name); d="$root/vol/$vn"; mkdir -p "$d"
    cm=$(printf '%s' "$v" | jq -r '.configMap.name // empty')
    sec=$(printf '%s' "$v" | jq -r '.secret.secretName // empty')
    if [ -n "$cm" ]; then
      f="${STUB_DIR}/cm/${cm}.json"
      [ -f "$f" ] || { echo "kubelet: ConfigMap ${cm} of volume ${vn} does not exist" >>"$log"; return 1; }
      if [ "$(printf '%s' "$v" | jq '.configMap.items // [] | length')" -gt 0 ]; then
        printf '%s' "$v" | jq -r '.configMap.items[] | .key + " " + .path' >"$root/items"
      else
        jq -r '.data | keys[] | . + " " + .' "$f" >"$root/items"
      fi
      while read -r k p; do jq -r --arg k "$k" '.data[$k]' "$f" >"$d/$p"; done <"$root/items"
    elif [ "$sec" = cutover-secondary-kubeconfigs ]; then
      for kc in "${STUB_KUBECONFIGS}"/*.yaml; do [ -e "$kc" ] && cp "$kc" "$d/"; done
    fi
  done <"$root/vols"
  jq -c '(.spec.template.spec.initContainers // [])[], .spec.template.spec.containers[]' "$j" >"$root/ctrs"
  while IFS= read -r c; do
    cn=$(printf '%s' "$c" | jq -r .name)
    if [ "${STUB_EVICT:-}" = "$cn" ] && { [ "$1" = 1 ] || [ "${STUB_EVICT_EVERY:-}" = 1 ]; }; then
      echo "kubelet: Pod $1 of ${name} is evicted before container ${cn} starts (DisruptionTarget)" >>"$log"
      return 200
    fi
    m=$(printf '%s' "$c" | jq -c --arg r "$root/vol" '[(.volumeMounts // [])[] | {key: .mountPath, value: ($r + "/" + .name)}] | from_entries')
    remap='def remap: . as $s | reduce ($m | to_entries[]) as $e ($s; if (. == $e.key or startswith($e.key + "/")) then $e.value + .[($e.key | length):] else . end);'
    { echo "secretval() { f=\"${STUB_DIR}/secrets/primary/${ns}/\$1.json\"; if [ -f \"\$f\" ]; then jq -r --arg k \"\$2\" '.data[\$k]' \"\$f\" | base64 -d; elif [ \"\$3\" != true ]; then echo \"kubelet: CreateContainerConfigError: secret \\\"\$1\\\" not found\" >&2; exit 97; fi; }"
      printf '%s' "$c" | jq -r --argjson m "$m" "$remap"' (.env // [])[] |
        if .value != null then "export \(.name)=\(.value | remap | @sh)"
        elif .valueFrom.secretKeyRef then "_v=\"$(secretval \(.valueFrom.secretKeyRef.name | @sh) \(.valueFrom.secretKeyRef.key | @sh) \(.valueFrom.secretKeyRef.optional // false | tostring))\" || exit 97; export \(.name)=\"$_v\""
        else empty end'; } >"$root/env.sh"
    printf '%s' "$c" | jq -r --argjson m "$m" "$remap"' ((.command // []) + (.args // []))[] | remap' >"$root/argv"
    echo "kubelet: Pod $1, container ${cn}" >>"$log"
    rc=0
    env -i PATH="$PATH" GIT_CONFIG_NOSYSTEM=1 STUB_DIR="$STUB_DIR" STUB_PAT="$STUB_PAT" STUB_RELEASE="$STUB_RELEASE" STUB_KUBECONFIGS="$STUB_KUBECONFIGS" \
      FOLLOWER_POLL_SECONDS=1 FOLLOWER_WAIT_SECONDS=60 sh -c '_kr="$1"; . "$_kr/env.sh"; set --; while IFS= read -r a; do set -- "$@" "$a"; done <"$_kr/argv"; "$@"' kubelet "$root" \
      >>"$log" 2>&1 </dev/null || rc=$?
    [ "$rc" -eq 0 ] || return "$rc"
  done <"$root/ctrs"
  return 0
}
n=1
while :; do
  rc=0; pod "$n" || rc=$?
  [ "$rc" = 200 ] || end "$rc"
  if ! retried "$n"; then echo "kubelet: the Job does not replace the evicted Pod: it fails" >>"$log"; end 1; fi
  echo "kubelet: the Job replaces the evicted Pod $n" >>"$log"
  n=$((n + 1))
done
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
// containers, and extra variables (NAME=value) on top; RELEASE_RUN_SH=<path>
// among them runs that copy of the script instead.
func (r *runFixture) runArgs(args []string, req releaseRequest, extra ...string) (string, int) {
	r.t.Helper()
	cmd := command("sh", append([]string{"-c", `kubectl() { sh "${STUB_DIR}/kubectl" "$@"; }; set -- "$0" "$@"; . "${RELEASE_RUN_SH:-${RELEASE_SCRIPTS}/release-run.sh}"`}, args...)...)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(), "PATH="+r.stub+"/bin:"+os.Getenv("PATH"), "HOME="+r.home, "GIT_CONFIG_NOSYSTEM=1", "STUB_DIR="+r.stub, "STUB_PAT=not-a-real-token", "STUB_RELEASE="+r.scripts, "STUB_KUBECONFIGS="+r.kubeconfigs,
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
	f.write(r.stub, "kubelet", kubeletStub)
	f.write(r.stub, "bin/kubectl", "#!/bin/sh\nexec sh \"${STUB_DIR}/kubectl\" \"$@\"\n")
	if err := os.Chmod(r.stub+"/bin/kubectl", 0o700); err != nil { //nolint:gosec // a fake program must be executable
		t.Fatal(err)
	}
	f.write(r.kubeconfigs, "region-b.yaml", "apiVersion: v1\nkind: Config\n")
	f.write(r.stub, "cutover-hr.json", r.cutoverHelmReleaseJSON())
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
		// The step 03 that runs is the candidate's (#7938): when it mirrors the
		// release's pins into every follower registry, storage does not matter.
		{"the candidate's step 03 covers the follower", registryConfig("sovereign-registry"), registryConfig("region-b-registry"), true,
			"covered: the candidate's step 03 mirrors the release's pins into every follower registry"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			step03 := step03Marker(candidateCutover)
			if c.covered {
				step03 += "echo \"[harbor-prewarm] #7938 secondary region ${region:-}: also mirrors the pins of ${UPSTREAM_BRANCH}\"\n"
			}
			id, _ := f.candidate(1, f.candidateCutoverRelease(step03))
			req := releaseRequest{Candidate: id, DryRun: true}
			r := newRunFixtureFrom(t, f, req, false)
			f.write(r.stub, "registry-primary.yml", c.primary)
			if c.follower != "" {
				f.write(r.stub, "registry-region-b.yml", c.follower)
			}
			out, code := r.run("stage", req)
			st := r.status()
			if code != 0 || st["phase"] != "dry-run" {
				t.Fatalf("stage: exit %d, phase %q\n%s", code, st["phase"], out)
			}
			if !strings.HasPrefix(st["followerPrewarm"], c.want) {
				t.Fatalf("followerPrewarm %q, want it to begin %q\n%s", st["followerPrewarm"], c.want, out)
			}
		})
	}
}

// patchOrder is the position of the first status patch that writes key, or -1.
func (r *runFixture) patchOrder(key string) int {
	r.t.Helper()
	b, _ := os.ReadFile(r.stub + "/status")
	for i, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var p struct{ Data map[string]string }
		if json.Unmarshal([]byte(l), &p) == nil {
			if _, ok := p.Data[key]; ok {
				return i
			}
		}
	}
	return -1
}

func (r *runFixture) followerGit(args ...string) string {
	r.t.Helper()
	return r.git(r.dir, append([]string{"--git-dir=" + r.dir + "/gitea-region-b/openova/openova.git"}, args...)...)
}

// SOV-521: a request that names region b stages every region and promotes b
// alone; the primary's main stays the commit it was. A rollback of b then puts
// b's recorded main back as one commit on top of the promoted one.
func TestReleaseRunPromotesOneRegionAndRollsItBack(t *testing.T) {
	f := newFixture(t)
	id, _ := f.candidate(1, f.laterRelease)
	req := releaseRequest{Candidate: id, Region: "region-b"}
	r := newRunFixtureFrom(t, f, req, false)
	r.env = []string{"RELEASE_REGION=region-b"}

	out, code := r.run("stage", req)
	st := r.status()
	if code != 0 || st["phase"] != "staged" || !strings.Contains(st["primary.stage"], "phase=staged") || !strings.Contains(st["region-b.stage"], "phase=staged") {
		t.Fatalf("stage: exit %d, phase %q, primary %q, region-b %q\n%s", code, st["phase"], st["primary.stage"], st["region-b.stage"], out)
	}
	bStaged := r.followerRef("release/" + id)

	out, code = r.run("promote", req)
	st = r.status()
	if code != 0 || st["phase"] != "delivered" {
		t.Fatalf("promote: exit %d, phase %q, message %q\n%s", code, st["phase"], st["message"], out)
	}
	if got := r.followerRef("main"); got != bStaged {
		t.Fatalf("region b's main is %s, want its staged commit %s", got, bStaged)
	}
	if got := r.remote("main"); got != f.main {
		t.Fatalf("the primary's main moved to %s; a request for region b leaves it at %s", got, f.main)
	}
	if st["primary.check"] != "" || st["primary.promote"] != "" || st["primary.promotedTo"] != "" {
		t.Fatalf("a request for region b checked or promoted the primary: check %q, promote %q", st["primary.check"], st["primary.promote"])
	}
	if st["region-b.promotedFrom"] != r.followerMain || st["region-b.promotedTo"] != bStaged || st["region-b.phase"] != "promoted" {
		t.Fatalf("the state of region b: promotedFrom %q (want %s), promotedTo %q (want %s), phase %q",
			st["region-b.promotedFrom"], r.followerMain, st["region-b.promotedTo"], bStaged, st["region-b.phase"])
	}

	r.env = []string{"RELEASE_REGION=region-b", "PROMOTED_FROM=" + r.followerMain, "PROMOTED_TO=" + bStaged}
	out, code = r.run("rollback", releaseRequest{Candidate: id, Region: "region-b", Rollback: true})
	st = r.status()
	if code != 0 || st["phase"] != "rolled-back" || st["region-b.phase"] != "rolled-back" {
		t.Fatalf("rollback: exit %d, phase %q, region-b %q, message %q\n%s", code, st["phase"], st["region-b.phase"], st["message"], out)
	}
	back := r.followerRef("main")
	if back == bStaged || back == r.followerMain {
		t.Fatalf("region b's main is %s after the rollback; want one new commit", back)
	}
	if parents := strings.Fields(r.followerGit("rev-list", "--parents", "-n", "1", back)); len(parents) != 2 || parents[1] != bStaged {
		t.Fatalf("the rollback commit %s has parents %v, want exactly the promoted commit %s", back, parents[1:], bStaged)
	}
	if err := command("git", "--git-dir="+r.dir+"/gitea-region-b/openova/openova.git", "diff", "--quiet", r.followerMain, back).Run(); err != nil {
		t.Fatalf("git diff --quiet %s %s: %v; the rollback must carry the recorded main's tree", r.followerMain, back, err)
	}
	if err := command("git", "--git-dir="+r.dir+"/gitea-region-b/openova/openova.git", "merge-base", "--is-ancestor", bStaged, back).Run(); err != nil {
		t.Fatal("the promoted commit is no longer in region b's history")
	}
	if r.remote("main") != f.main {
		t.Fatal("a rollback of region b moved the primary")
	}
	if _, err := os.Stat(r.stub + "/job-sovereign-release-rollback-region-b.yaml"); err != nil {
		t.Fatalf("the rollback did not run as a Job in region b: %v", err)
	}
}

// SOV-521: a whole request of a candidate that region b's own request already
// promoted finds b up-to-date and pushes nothing there, so the state keeps what
// b's promote recorded and a rollback of b still restores the main of before.
// A whole request after that rollback leaves b rolled back.
func TestReleaseRunWholeRequestKeepsWhatARegionRequestPromoted(t *testing.T) {
	f := newFixture(t)
	id, _ := f.candidate(1, f.laterRelease)
	region, whole := releaseRequest{Candidate: id, Region: "region-b"}, releaseRequest{Candidate: id}
	r := newRunFixtureFrom(t, f, region, false)
	deliver := func(req releaseRequest, env ...string) {
		t.Helper()
		r.env = env
		for _, ph := range []string{"stage", "promote"} {
			if out, code := r.run(ph, req); code != 0 {
				t.Fatalf("%s of region %q: exit %d\n%s", ph, req.Region, code, out)
			}
		}
	}
	deliver(region, "RELEASE_REGION=region-b")
	bPromoted := r.followerRef("main")
	if bPromoted == r.followerMain {
		t.Fatal("the request for region b did not move it")
	}

	deliver(whole)
	st := r.status()
	if !strings.Contains(st["region-b.stage"], "phase=up-to-date") || r.followerRef("main") != bPromoted || st["phase"] != "delivered" {
		t.Fatalf("the whole request: region-b.stage %q, region b main %s (want %s), phase %q", st["region-b.stage"], r.followerRef("main"), bPromoted, st["phase"])
	}
	if st["region-b.promotedFrom"] != r.followerMain || st["region-b.promotedTo"] != bPromoted || st["region-b.phase"] != "promoted" {
		t.Fatalf("a promote that pushed nothing rewrote region b's record: promotedFrom %q (want %s), promotedTo %q (want %s), phase %q",
			st["region-b.promotedFrom"], r.followerMain, st["region-b.promotedTo"], bPromoted, st["region-b.phase"])
	}

	r.env = []string{"RELEASE_REGION=region-b", "PROMOTED_FROM=" + st["region-b.promotedFrom"], "PROMOTED_TO=" + st["region-b.promotedTo"]}
	out, code := r.run("rollback", releaseRequest{Candidate: id, Region: "region-b", Rollback: true})
	back := r.followerRef("main")
	if st = r.status(); code != 0 || st["region-b.phase"] != "rolled-back" || back == bPromoted {
		t.Fatalf("rollback: exit %d, region-b.phase %q, region b main %s\n%s", code, st["region-b.phase"], back, out)
	}
	if err := command("git", "--git-dir="+r.dir+"/gitea-region-b/openova/openova.git", "diff", "--quiet", r.followerMain, back).Run(); err != nil {
		t.Fatalf("git diff --quiet %s %s: %v; the rollback must restore the main of before region b's promote", r.followerMain, back, err)
	}

	deliver(whole)
	if st = r.status(); st["region-b.phase"] != "rolled-back" || r.followerRef("main") != back || st["region-b.promotedTo"] != bPromoted {
		t.Fatalf("a whole request after the rollback: region-b.phase %q, region b main %s (want %s), promotedTo %q", st["region-b.phase"], r.followerRef("main"), back, st["region-b.promotedTo"])
	}
}

// SOV-521: without a region every region is promoted as before, the followers
// first, and the state records what each promote moved.
func TestReleaseRunWholePromoteIsFollowersFirstAndRecordsEachRegion(t *testing.T) {
	f := newFixture(t)
	id, _ := f.candidate(1, f.laterRelease)
	req := releaseRequest{Candidate: id}
	r := newRunFixtureFrom(t, f, req, false)
	if out, code := r.run("stage", req); code != 0 {
		t.Fatalf("stage: exit %d\n%s", code, out)
	}
	staged, bStaged := r.remote("release/"+id), r.followerRef("release/"+id)
	out, code := r.run("promote", req)
	st := r.status()
	if code != 0 || st["phase"] != "delivered" || r.remote("main") != staged || r.followerRef("main") != bStaged {
		t.Fatalf("promote: exit %d, phase %q\n%s", code, st["phase"], out)
	}
	if b, p := r.patchOrder("region-b.promote"), r.patchOrder("primary.promote"); b < 0 || p < 0 || b > p {
		t.Fatalf("promote order: region b at patch %d, primary at %d; the followers come first", b, p)
	}
	for region, want := range map[string][2]string{"primary": {f.main, staged}, "region-b": {r.followerMain, bStaged}} {
		if st[region+".promotedFrom"] != want[0] || st[region+".promotedTo"] != want[1] || st[region+".phase"] != "promoted" {
			t.Fatalf("%s: promotedFrom %q promotedTo %q phase %q, want %s %s promoted", region, st[region+".promotedFrom"], st[region+".promotedTo"], st[region+".phase"], want[0], want[1])
		}
	}
}

// SOV-521: a rollback is refused, with nothing pushed, when the region's main
// moved after the promote, and when no promote is recorded for it.
func TestReleaseRunRollbackRefusals(t *testing.T) {
	f := newFixture(t)
	id, _ := f.candidate(1, f.laterRelease)
	req := releaseRequest{Candidate: id, Region: "region-b"}
	r := newRunFixtureFrom(t, f, req, false)
	r.env = []string{"RELEASE_REGION=region-b"}
	if out, code := r.run("stage", req); code != 0 {
		t.Fatalf("stage: exit %d\n%s", code, out)
	}
	bStaged := r.followerRef("release/" + id)
	rb := releaseRequest{Candidate: id, Region: "region-b", Rollback: true}

	// Never promoted: region b's main is still the main of the stage.
	r.env = []string{"RELEASE_REGION=region-b", "PROMOTED_FROM=" + r.followerMain, "PROMOTED_TO=" + bStaged}
	out, code := r.run("rollback", rb)
	if st := r.status(); code == 0 || st["phase"] != "moved" || r.followerRef("main") != r.followerMain {
		t.Fatalf("a rollback of a region this candidate never promoted: exit %d, phase %q, main %s\n%s", code, st["phase"], r.followerRef("main"), out)
	}
	// No promote recorded: the request carries no commits.
	r.env = []string{"RELEASE_REGION=region-b"}
	out, code = r.run("rollback", rb)
	if st := r.status(); code == 0 || st["phase"] != "refused" || r.followerRef("main") != r.followerMain {
		t.Fatalf("a rollback without a recorded promote: exit %d, phase %q\n%s", code, st["phase"], out)
	}
	// Promoted, then a commit of region b's own: its main moved.
	r.env = []string{"RELEASE_REGION=region-b"}
	if out, code := r.run("promote", req); code != 0 {
		t.Fatalf("promote: exit %d\n%s", code, out)
	}
	w := r.dir + "/follower-region-b"
	r.git(w, "fetch", "-q", r.dir+"/gitea-region-b/openova/openova.git", "main")
	r.git(w, "checkout", "-q", "FETCH_HEAD")
	r.write(w, "later.txt", "after the promote\n")
	r.git(w, "add", "-A")
	r.git(w, "commit", "-q", "-m", "region b: a commit after the promote")
	r.git(w, "push", "-q", r.dir+"/gitea-region-b/openova/openova.git", "HEAD:refs/heads/main")
	moved := r.followerRef("main")
	r.env = []string{"RELEASE_REGION=region-b", "PROMOTED_FROM=" + r.followerMain, "PROMOTED_TO=" + bStaged}
	out, code = r.run("rollback", rb)
	if st := r.status(); code == 0 || st["phase"] != "moved" || r.followerRef("main") != moved {
		t.Fatalf("a rollback after region b moved: exit %d, phase %q, main %s (want %s)\n%s", code, st["phase"], r.followerRef("main"), moved, out)
	}
	if r.remote("main") != f.main {
		t.Fatal("a refused rollback moved the primary")
	}
}

// SOV-521: a request for one region reaches only a candidate whose
// release-run.sh has the phase rollback; any other promotes every region, so
// the stage refuses it and nothing moves. A whole delivery of the same
// candidate runs as before.
func TestReleaseRunRegionRequestNeedsACandidateWithThePhaseRollback(t *testing.T) {
	f := newFixture(t)
	id, cand := f.candidate(1, func(w string) {
		f.candidateCutoverRelease(step03Marker(candidateCutover))(w)
		sed(t, w+"/"+cutoverChartPath+"/files/release/release-run.sh", "\n  rollback)\n", "\n  not-this-phase)\n")
	})
	req := releaseRequest{Candidate: id, Credential: &testCredential, Region: "region-b"}
	r := newRunFixtureFrom(t, f, req, false)
	r.putPullSecret()
	r.env = []string{"PULL_SECRET=" + releasePull, "RELEASE_REGION=region-b"}
	out, code := r.run("stage", req)
	st := r.status()
	if code == 0 || st["phase"] != "refused" || !strings.Contains(st["message"], "has no phase rollback") || !strings.Contains(st["message"], "nothing moved") {
		t.Fatalf("stage of a region request: exit %d, phase %q, message %q\n%s", code, st["phase"], st["message"], out)
	}
	if r.remote("main") != f.main || r.followerRef("main") != r.followerMain {
		t.Fatal("a refused region request moved a main")
	}
	if r.secretData("primary", testNS, releasePull) != nil {
		t.Fatal("a refused region request left its pull credential")
	}

	whole := releaseRequest{Candidate: id, Credential: &testCredential}
	r.putPullSecret()
	r.env = []string{"PULL_SECRET=" + releasePull}
	if out, code := r.run("stage", whole); code != 0 {
		t.Fatalf("stage of a whole delivery: exit %d\n%s", code, out)
	}
	out, code = r.run("deliver", whole)
	if st := r.status(); code != 0 || st["phase"] != "delivered" {
		t.Fatalf("deliver: exit %d, phase %q\n%s\n%s", code, st["phase"], out, r.jobLog(whole))
	}
	if err := command("git", "--git-dir="+f.repo, "merge-base", "--is-ancestor", cand, r.remote("main")).Run(); err != nil || r.followerRef("main") == r.followerMain {
		t.Fatal("the whole delivery did not move every region")
	}
}

// SOV-521: a region request through the bootstrap and the candidate's
// mechanism (stage, then deliver: compose, prewarm, promote) moves region b
// only; the delivery Job's promote gets the region from compose.
func TestReleaseRunDeliversOneRegionThroughTheCandidatesMechanism(t *testing.T) {
	f := newFixture(t)
	id, cand := f.candidate(1, f.candidateCutoverRelease(step03Marker(candidateCutover)))
	req := releaseRequest{Candidate: id, Credential: &testCredential, Region: "region-b"}
	r := newRunFixtureFrom(t, f, req, false)
	r.putPullSecret()
	r.env = []string{"PULL_SECRET=" + releasePull, "RELEASE_REGION=region-b"}
	if out, code := r.run("stage", req); code != 0 {
		t.Fatalf("stage: exit %d\n%s", code, out)
	}
	bStaged := r.followerRef("release/" + id)
	out, code := r.run("deliver", req)
	st := r.status()
	if code != 0 || st["phase"] != "delivered" {
		t.Fatalf("deliver: exit %d, phase %q, message %q\n%s\n%s", code, st["phase"], st["message"], out, r.jobLog(req))
	}
	b, err := os.ReadFile(r.stub + "/job-sovereign-release-delivery.json")
	if err != nil {
		t.Fatal(err)
	}
	var job batchv1.Job
	if err := json.Unmarshal(b, &job); err != nil {
		t.Fatal(err)
	}
	if e := env(job.Spec.Template.Spec.Containers[0], "RELEASE_REGION"); e == nil || e.Value != "region-b" {
		t.Fatalf("the delivery Job's promote has RELEASE_REGION %v, want region-b", e)
	}
	if r.followerRef("main") != bStaged || r.remote("main") != f.main {
		t.Fatalf("after the delivery: region b main %s (staged %s), primary main %s (want %s)", r.followerRef("main"), bStaged, r.remote("main"), f.main)
	}
	if err := command("git", "--git-dir="+r.dir+"/gitea-region-b/openova/openova.git", "merge-base", "--is-ancestor", cand, bStaged).Run(); err != nil {
		t.Fatal("region b does not run the candidate")
	}
	if st["region-b.promotedTo"] != bStaged || st["primary.promotedTo"] != "" {
		t.Fatalf("the state: region-b.promotedTo %q (want %s), primary.promotedTo %q", st["region-b.promotedTo"], bStaged, st["primary.promotedTo"])
	}
}

// providerPath is the Provider whose package line cutover step 11 rewrote
// when a candidate changed it (SOV-522).
const providerPath = "clusters/_template/infrastructure/providers/base/provider-opentofu.yaml"

func providerYAML(pkg string) string {
	return "apiVersion: pkg.crossplane.io/v1\nkind: Provider\nmetadata:\n  name: provider-opentofu\nspec:\n  package: " + pkg + "\n"
}

// step11 is cutover step 11's ConfigMap: the chart's own provider-pivot.sh and
// the settings of its container.
func step11(t *testing.T) *corev1.ConfigMap {
	return cm(providerStepConfigMap, map[string]string{
		"provider-pivot.sh": chartScript(t, "11-crossplane-provider-pivot-job.yaml", "provider-pivot.sh"),
		"podSpec": "containers:\n  - name: crossplane-provider-pivot\n    env:\n      - {name: SOVEREIGN_FQDN, value: test.example}\n" +
			"      - {name: REGISTRY_PATH, value: proxy-xpkg}\n      - {name: UPSTREAM_HOST, value: xpkg.upbound.io}\n" +
			"      - {name: MOTHERSHIP_PROXY_PREFIX, value: harbor.parent.example/proxy-xpkg}\n"})
}

// The bootstrap's stage runs step 11's provider-pivot.sh with step 11's own
// settings when the installed chart carries it, and nothing of it otherwise.
func TestReleaseBootstrapCarriesTheProviderPivotOfStep11(t *testing.T) {
	for _, c := range []struct {
		extra []runtime.Object
		want  string
	}{{nil, ""}, {[]runtime.Object{step11(t)}, "PROVIDER_PIVOT=/provider-pivot/provider-pivot.sh SOVEREIGN_FQDN=test.example REGISTRY_PATH=proxy-xpkg " +
		"UPSTREAM_HOST=xpkg.upbound.io MOTHERSHIP_PROXY_PREFIX=harbor.parent.example/proxy-xpkg mount=/provider-pivot volume=" + providerStepConfigMap + ":provider-pivot.sh"}} {
		in, err := readReleaseInputs(context.Background(), fake.NewSimpleClientset(append(installed(false, false), c.extra...)...), testNS)
		if err != nil {
			t.Fatal(err)
		}
		job, _, err := composeRelease(in, releaseRequest{Candidate: testCandidate}, testNS, "sovereign-release", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		pod, got := job.Spec.Template.Spec, []string{}
		for _, e := range pod.InitContainers[0].Env {
			if strings.Contains(" PROVIDER_PIVOT SOVEREIGN_FQDN REGISTRY_PATH UPSTREAM_HOST MOTHERSHIP_PROXY_PREFIX ", " "+e.Name+" ") {
				got = append(got, e.Name+"="+e.Value)
			}
		}
		for _, m := range pod.InitContainers[0].VolumeMounts {
			if m.Name == "release-provider-pivot" {
				got = append(got, "mount="+m.MountPath)
			}
		}
		for _, v := range pod.Volumes {
			if v.Name == "release-provider-pivot" && v.ConfigMap != nil && len(v.ConfigMap.Items) == 1 {
				got = append(got, "volume="+v.ConfigMap.Name+":"+v.ConfigMap.Items[0].Key)
			}
		}
		if strings.Join(got, " ") != c.want {
			t.Fatalf("the stage container carries\n%s\nwant\n%s", strings.Join(got, " "), c.want)
		}
	}
}

// A candidate that changes a package line step 11 rewrote: the stage, run with
// the environment the bootstrap gives it, takes the candidate's line and
// applies step 11's rewrite again; without step 11's file it refuses, as before.
func TestReleaseStageReappliesTheProviderPivotOfStep11(t *testing.T) {
	f := newFixture(t)
	in, err := readReleaseInputs(context.Background(), fake.NewSimpleClientset(append(installed(false, false), step11(t))...), testNS)
	if err != nil {
		t.Fatal(err)
	}
	f.write(f.scripts, "provider-pivot.sh", chartScript(t, "11-crossplane-provider-pivot-job.yaml", "provider-pivot.sh"))
	var env []string
	for _, e := range in.providerEnv {
		env = append(env, e.Name+"="+strings.Replace(e.Value, "/provider-pivot/", f.scripts+"/", 1))
	}
	// The installed release gains the Provider; the Sovereign's main merges it
	// and commits step 11's rewrite, as the cutover did.
	w, s := f.dir+"/work", f.dir+"/sovereign"
	f.write(w, providerPath, providerYAML("xpkg.upbound.io/upbound/provider-opentofu@sha256:"+strings.Repeat("a", 64)))
	f.git(w, "add", "-A")
	f.git(w, "commit", "-q", "-m", "installed release with a Provider")
	f.installed = f.git(w, "rev-parse", "HEAD")
	f.git(w, "push", "-q", f.upstream, "main")
	f.git(s, "pull", "-q", "--no-rebase", "--no-edit", f.upstream, "main")
	cmd := command("sh", "-c", `. "$PROVIDER_PIVOT"; provider_pivot_target; lookup_pkg_digest() { :; }; edited=0; provider_pivot_file "$1"`, "step11", providerPath)
	cmd.Dir, cmd.Env = s, append(os.Environ(), env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("step 11: %v\n%s", err, out)
	}
	f.git(s, "commit", "-q", "-am", "cutover: pivot Crossplane Provider spec.package")
	f.main = f.git(s, "rev-parse", "HEAD")
	f.git(s, "push", "-q", f.repo, "HEAD:refs/heads/main")
	newPkg := "upbound/provider-opentofu@sha256:" + strings.Repeat("b", 64)
	id, _ := f.candidate(1, func(w string) { f.write(w, providerPath, providerYAML("xpkg.upbound.io/"+newPkg)) })

	out, code := f.region([]string{"sh"}, "PHASE=stage", "CANDIDATE="+id)
	if r := resultOf(out); code != 3 || r["phase"] != "conflict" || !strings.Contains(out, "release-report region=primary conflict "+providerPath) {
		t.Fatalf("without step 11's file: exit %d, result %v, want a conflict on %s\n%s", code, r, providerPath, out)
	}
	for _, sh := range shells() {
		out, code = f.region(sh, append([]string{"PHASE=stage", "CANDIDATE=" + id}, env...)...)
		r := resultOf(out)
		if code != 0 || r["phase"] != "staged" {
			t.Fatalf("%v: stage: exit %d, result %v\n%s", sh, code, r, out)
		}
		if got, want := f.show(r["staged"], providerPath), strings.TrimSuffix(providerYAML("harbor.test.example/proxy-xpkg/"+newPkg), "\n"); got != want {
			t.Fatalf("%v: the staged Provider is\n%s\nwant the candidate's line with step 11's rewrite\n%s", sh, got, want)
		}
		if !strings.Contains(out, "resolved "+providerPath+": the content of rc/"+id+", this region's registry rewrite (provider) applied again") || f.remote("main") != f.main {
			t.Fatalf("%v: the report does not name the Provider as taken from the candidate, or main moved\n%s", sh, out)
		}
	}
}
