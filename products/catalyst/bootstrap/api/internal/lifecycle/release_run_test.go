package lifecycle

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/yaml"
)

// kubectlStub stands for kubectl in release-run.sh (a shell function named
// kubectl runs it, and STUB_DIR/bin/kubectl for the processes it starts). It
// answers the token read and the read of each region's registry configuration
// (STUB_DIR/registry-<region>.yml), keeps the Secrets of each region under
// STUB_DIR/secrets/<region>/<ns>/ (the region is the basename of --kubeconfig;
// "primary" without one),
// records every status patch (a read of the state as JSON answers them folded,
// any other read its last phase), and runs a follower Job it is asked to apply:
// release-region.sh with the plain environment of the applied manifest (its
// /release paths mapped to STUB_RELEASE) and GITEA_PAT, the way the follower
// region's kubelet would. Its logs and its outcome are then what get and logs
// answer. Every call is also kept in STUB_DIR/calls.rec as its arguments and
// the kind and namespace of the object it read on stdin (release_rbac_test.go
// checks each against the RBAC of the account that makes it). A server-side
// dry run stores nothing: the object is kept as STUB_DIR/dry/<region>.<kind>.
// <name>.json for the test, and refused, as the API server would, when that
// name matches a line (an ERE) of STUB_DIR/refuse. STUB_DIR/pods.json, when
// present, is what a read of a Job's Pods answers. A status patch that matches
// a line of STUB_DIR/patch-fail fails and is not kept; the first that matches
// a line of STUB_DIR/patch-kill kills the process that sends it (its Pod
// killed before the write) and is not kept, and so does one of patch-evict,
// whose kill is an eviction (STUB_DIR/evicted, the Pod's DisruptionTarget). A
// read whose "pod=<STUB_POD> <arguments>" matches a line of STUB_DIR/read-fail
// fails, and the first that matches one of read-fail-once. A Job's Pods,
// pods-<job>.json (podsStub), are what a read of them answers, a Pod's log
// what its logs answer, and logs job/<job> answers the oldest, as kubectl
// does; a Job's end is its condition. The first follower Job whose name
// matches STUB_DIR/evict-follower loses its first Pod to an eviction.
const kubectlStub = `#!/bin/sh
. "${STUB_DIR}/pods.sh"
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
for f in read-fail read-fail-once; do
  case " $* " in *" get "*) ;; *) break ;; esac
  if [ -s "${STUB_DIR}/$f" ] && printf 'pod=%s %s\n' "${STUB_POD:-}" "$*" | grep -qEf "${STUB_DIR}/$f"; then
    [ "$f" = read-fail ] || rm -f "${STUB_DIR}/$f"; echo "Error from server (InternalError): the test fails this read" >&2; exit 1
  fi
done
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
  *" get pods "*) j=$(printf '%s\n' "$*" | sed -n 's/.*job-name=\([^ ]*\).*/\1/p')
    grep -q "\"${j}-" "${STUB_DIR}/pods.json" 2>/dev/null && cat "${STUB_DIR}/pods.json" || cat "${STUB_DIR}/pods-${j}.json" 2>/dev/null || echo '{"items": []}'; exit 0 ;;
esac
case " $* " in
  *" get configmap harbor-registry "*) cat "${STUB_DIR}/registry-${region}.yml" 2>/dev/null || exit 1 ;;
  *" get helmreleases.helm.toolkit.fluxcd.io "*) cat "${STUB_DIR}/cutover-hr.json" 2>/dev/null || exit 1 ;;
  *" get configmap sovereign-release "*)
    case " $* " in
      *" -o json "*) jq -s '{data: (map(.data // {}) | add // {})}' "${STUB_DIR}/status" ;;
      *) grep -o '"phase":"[^"]*"' "${STUB_DIR}/status" | tail -n 1 | sed 's/.*:"//; s/"$//' ;;
    esac ;;
  *" get secret "*jsonpath*) printf '%s' "${STUB_PAT}" | base64 ;;
  *" get secret "*)
    f="${STUB_DIR}/secrets/${region}/$2/$5.json"
    [ -f "${f}" ] || { echo "Error from server (NotFound): secrets \"$5\" not found" >&2; exit 1; }
    case " $* " in *" -o name "*) echo "secret/$5" ;; *) cat "${f}" ;; esac ;;
  *" delete secret "*) rm -f "${STUB_DIR}/secrets/${region}/$2/$5.json" ;;
  *" patch configmap "*)
    p=""; while [ $# -gt 0 ]; do [ "$1" = "-p" ] && p="$2"; shift; done
    for k in kill evict; do
      if [ -s "${STUB_DIR}/patch-$k" ] && printf '%s\n' "$p" | grep -qEf "${STUB_DIR}/patch-$k"; then
        rm -f "${STUB_DIR}/patch-$k"; [ "$k" = kill ] || : >"${STUB_DIR}/evicted"
        echo "kubectl stub: the Pod is ${k}ed before it writes ${p}" >&2; kill -9 "${PPID}"; exit 137
      fi
    done
    if [ -s "${STUB_DIR}/patch-fail" ] && printf '%s\n' "$p" | grep -qEf "${STUB_DIR}/patch-fail"; then
      echo "Error from server (InternalError): the test fails this patch" >&2; exit 1
    fi
    printf '%s\n' "$p" >>"${STUB_DIR}/status" ;;
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
      case " $* " in *" create "*) [ ! -e "${STUB_DIR}/job-${name}.rc" ] || { echo "Error from server (AlreadyExists): jobs.batch \"${name}\" already exists" >&2; exit 1; } ;; esac
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
    rc=0; a=1; rm -f "${STUB_DIR}/pods-${name}.json"
    if [ -s "${STUB_DIR}/evict-follower" ] && printf '%s\n' "${name}" | grep -qEf "${STUB_DIR}/evict-follower"; then
      rm -f "${STUB_DIR}/evict-follower"; echo "kubelet: the Pod is evicted before it starts" >"${STUB_DIR}/podlog-${name}-1"
      pod_add "${name}" 1 Failed true; rc=137; ! retries "${STUB_DIR}/follower-job.json" || { rc=0; a=2; }
    fi
    if [ "${rc}" = 0 ]; then
      ( set -a; . "${env_file}"; GITEA_PAT="${STUB_PAT}"; WORK=$(mktemp -d); export GITEA_PAT WORK; sh "${STUB_RELEASE}/release-region.sh" ) \
        >"${STUB_DIR}/podlog-${name}-${a}" 2>&1 || rc=$?
      pod_add "${name}" "${a}" "$([ "${rc}" = 0 ] && echo Succeeded || echo Failed)" false
    fi
    echo "${rc}" >"${STUB_DIR}/job-${name}.rc" ;;
  *" get job "*)
    rc=$(cat "${STUB_DIR}/job-$5.rc" 2>/dev/null || echo running)
    case "$*" in
      *conditions*) [ "${rc}" = 0 ] && echo Complete; [ "${rc}" != 0 ] && [ "${rc}" != running ] && echo Failed ;;
      *succeeded*) [ "${rc}" = 0 ] && echo 1 ;;
      *failed*) [ "${rc}" != 0 ] && [ "${rc}" != running ] && echo 1 ;;
      *" -o json "*) cat "${STUB_DIR}/job-$5.json" 2>/dev/null ;;
    esac
    exit 0 ;;
  *" logs "*) n="${4#*/}"; case "$4" in pod/*) cat "${STUB_DIR}/podlog-${n}" ;; *) cat "${STUB_DIR}/podlog-${n}-1" ;; esac ;;
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
// directory. The first container that fails ends the Pod (<name>-<attempt>,
// with fresh emptyDirs and STUB_POD=<attempt>); job-<name>.rc is how the Job
// ended. STUB_KILL=<container> kills the Pod before that container starts (a
// lost node): no eviction, so the Job fails, and no container of it recorded
// why. A Pod an eviction took (STUB_DIR/evicted) the Job replaces once when its
// spec says so (retries), as the Job controller does.
const kubeletStub = `#!/bin/sh
set -u
. "${STUB_DIR}/pods.sh"
j="$1"; name=$(jq -r .metadata.name "$j"); ns=$(jq -r .metadata.namespace "$j")
: >"${STUB_DIR}/job-${name}.log"; rm -f "${STUB_DIR}/pods-${name}.json"
a=0
pod() {
root="${STUB_DIR}/pod-${name}-${a}"; mkdir -p "$root/vol"
log="${STUB_DIR}/podlog-${name}-${a}"; : >"$log"
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
  m=$(printf '%s' "$c" | jq -c --arg r "$root/vol" '[(.volumeMounts // [])[] | {key: .mountPath, value: ($r + "/" + .name)}] | from_entries')
  remap='def remap: . as $s | reduce ($m | to_entries[]) as $e ($s; if (. == $e.key or startswith($e.key + "/")) then $e.value + .[($e.key | length):] else . end);'
  { echo "secretval() { jq -r --arg k "\$2" '.data[\$k]' "${STUB_DIR}/secrets/primary/${ns}/\$1.json" | base64 -d; }"
    printf '%s' "$c" | jq -r --argjson m "$m" "$remap"' (.env // [])[] |
      if .value != null then "export \(.name)=\(.value | remap | @sh)"
      elif .valueFrom.secretKeyRef then "export \(.name)=\"$(secretval \(.valueFrom.secretKeyRef.name | @sh) \(.valueFrom.secretKeyRef.key | @sh))\""
      else empty end'; } >"$root/env.sh"
  printf '%s' "$c" | jq -r --argjson m "$m" "$remap"' ((.command // []) + (.args // []))[] | remap' >"$root/argv"
  if [ -n "${STUB_KILL:-}" ] && [ "$(printf '%s' "$c" | jq -r .name)" = "${STUB_KILL}" ]; then
    echo "kubelet: the Pod is killed before container ${STUB_KILL} starts" >>"$log"
    return 137
  fi
  echo "kubelet: container $(printf '%s' "$c" | jq -r .name)" >>"$log"
  rc=0
  env -i PATH="$PATH" GIT_CONFIG_NOSYSTEM=1 STUB_DIR="$STUB_DIR" STUB_PAT="$STUB_PAT" STUB_RELEASE="$STUB_RELEASE" STUB_KUBECONFIGS="$STUB_KUBECONFIGS" STUB_POD="$a" \
    FOLLOWER_POLL_SECONDS=1 FOLLOWER_WAIT_SECONDS=60 sh -c '_kr="$1"; . "$_kr/env.sh"; set --; while IFS= read -r a; do set -- "$@" "$a"; done <"$_kr/argv"; "$@"' kubelet "$root" \
    >>"$log" 2>&1 </dev/null || rc=$?
  [ "$rc" -eq 0 ] || return "$rc"
done <"$root/ctrs"
}
while a=$((a + 1)); do
  rc=0; pod || rc=$?
  cat "$log" >>"${STUB_DIR}/job-${name}.log"
  ev=false; [ "$rc" = 0 ] || [ ! -e "${STUB_DIR}/evicted" ] || { rm -f "${STUB_DIR}/evicted"; ev=true; }
  pod_add "$name" "$a" "$([ "$rc" = 0 ] && echo Succeeded || echo Failed)" "$ev"
  if [ "$ev" = true ] && [ "$a" = 1 ] && retries "$j"; then echo "kubelet: the Job replaces the evicted Pod" >>"${STUB_DIR}/job-${name}.log"; continue; fi
  echo "$rc" >"${STUB_DIR}/job-${name}.rc"; exit 0
done
`

// podsStub: pod_add <job> <attempt> <phase> <disrupted> adds Pod
// <job>-<attempt> to STUB_DIR/pods-<job>.json, each attempt a second younger;
// retries <job.json> says whether the Job replaces a Pod a disruption took: a
// backoffLimit of 1 or more, and a first pod failure rule that counts it.
const podsStub = `pod_add() {
  f="${STUB_DIR}/pods-$1.json"; [ -s "$f" ] || echo '{"items": []}' >"$f"
  jq --arg n "$1-$2" --arg t "2026-01-01T00:00:0$2Z" --arg p "$3" --argjson d "$4" '.items += [{metadata: {name: $n, creationTimestamp: $t},
    status: {phase: $p, conditions: (if $d then [{type: "DisruptionTarget", status: "True"}] else [] end)}}]' "$f" >"$f.new" && mv "$f.new" "$f"
}
retries() { jq -e '(.spec.backoffLimit // 6) >= 1 and (.spec.podFailurePolicy.rules[0] | .action == "Count" and any(.onPodConditions[]?; .type == "DisruptionTarget"))' "$1" >/dev/null; }
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
	f.write(r.stub, "pods.sh", podsStub)
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

// promote records phase promoting before it checks or pushes any region, and
// each region it reaches (promoteRegion) before it pushes it. A write of either
// that fails stops it there: no region it did not record is pushed.
func TestReleaseRunPromoteStopsWhereItCannotRecordWhatItReaches(t *testing.T) {
	for _, c := range []struct {
		name, fail string
		region     string // the region whose promoteRegion write fails; "" for phase promoting
		bMoves     bool
	}{
		{"phase promoting", `"phase":"promoting"`, "", false},
		{"that it reached the follower", `"promoteRegion":"region-b"`, "region-b", false},
		{"that it reached the primary", `"promoteRegion":"primary"`, "primary", true},
	} {
		t.Run("cannot record "+c.name, func(t *testing.T) {
			f := newFixture(t)
			id, _ := f.candidate(1, f.laterRelease)
			req := releaseRequest{Candidate: id}
			r := newRunFixtureFrom(t, f, req, false)
			if out, code := r.run("stage", req); code != 0 {
				t.Fatalf("stage: exit %d\n%s", code, out)
			}
			bStaged := r.followerRef("release/" + id)
			f.write(r.stub, "patch-fail", c.fail+"\n")
			out, code := r.run("promote", req)
			st := r.status()
			wantB := r.followerMain
			if c.bMoves {
				wantB = bStaged
			}
			if code == 0 || r.remote("main") != f.main || r.followerRef("main") != wantB {
				t.Fatalf("promote: exit %d, primary main moved %v, region-b main %s want %s\n%s", code, r.remote("main") != f.main, r.followerRef("main"), wantB, out)
			}
			if c.region == "" {
				if st["phase"] != "staged" || st["primary.check"] != "" || st["region-b.check"] != "" || !strings.Contains(out, "cannot record phase promoting") {
					t.Fatalf("a promote that cannot record phase promoting checks no region: phase %q, primary.check %q, region-b.check %q\n%s", st["phase"], st["primary.check"], st["region-b.check"], out)
				}
				return
			}
			if st["phase"] != "failed" || st["failedStep"] != "promote" ||
				!strings.HasPrefix(st["message"], "promote could not record that it reached region "+c.region+", so neither it nor a region after it was pushed") {
				t.Fatalf("phase %q, failedStep %q, message %q\n%s", st["phase"], st["failedStep"], st["message"], out)
			}
			if c.bMoves && st["region-b.promotedTo"] != bStaged {
				t.Fatalf("region-b.promotedTo %q, want %s", st["region-b.promotedTo"], bStaged)
			}
		})
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

// retriesOneEviction: the Job the scripts made (STUB_DIR/<file>) replaces a Pod
// exactly as the route's bootstrap Job does (retryOneEviction).
func (r *runFixture) retriesOneEviction(file string) {
	r.t.Helper()
	var want, got batchv1.Job
	retryOneEviction(&want.Spec)
	b, err := os.ReadFile(filepath.Clean(r.stub + "/" + file))
	if err == nil {
		err = yaml.Unmarshal(b, &got)
	}
	w, _ := json.Marshal([]any{want.Spec.BackoffLimit, want.Spec.PodReplacementPolicy, want.Spec.PodFailurePolicy})
	g, _ := json.Marshal([]any{got.Spec.BackoffLimit, got.Spec.PodReplacementPolicy, got.Spec.PodFailurePolicy})
	if err != nil || string(g) != string(w) || want.Spec.PodFailurePolicy.Rules[0].OnPodConditions[0].Type != corev1.DisruptionTarget {
		r.t.Errorf("%s (%v) replaces a Pod as %s, the bootstrap as %s", file, err, g, w)
	}
}

// A delivery Job whose Pod an eviction takes resumes in its second Pod from the
// state it reads with certainty: it prewarms again only when no Pod recorded
// that it did (prewarmedBy, written before the credential goes, or the prewarm
// stops), and keeps the region an earlier promote reached. A read that fails is
// read again; when none answers the Pod stops, and the state names what moved.
func TestReleaseDeliveryJobResumesAfterAnEviction(t *testing.T) {
	const moved = "Promoted: region-b."
	for _, c := range []struct{ name, evict, fail, read, phase, want string }{
		{"evicted before it records its prewarm", `"prewarmedBy"`, "", "", "delivered", ""},
		{"evicted after it promoted the follower", `"promoteRegion":"primary"`, "", "", "delivered", "resumed: the promote an earlier Pod began, which reached region-b"},
		{"its first read of the state fails", `"promoteRegion":"primary"`, "", "read-fail-once", "delivered", "reached region-b"},
		{"no read of the state answers", `"promoteRegion":"primary"`, "", "read-fail", "failed", moved},
		{"its record of the prewarm cannot be written", "", `"prewarmedBy"`, "", "failed", "it cannot record its end (prewarmedBy)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, req := credentialFixture(t, false, false)
			if out, code := r.run("stage", req); code != 0 {
				t.Fatalf("stage: exit %d\n%s", code, out)
			}
			staged := r.followerRef("release/" + req.Candidate)
			for file, line := range map[string]string{"patch-evict": c.evict, "patch-fail": c.fail, c.read: "^pod=2 .*get configmap sovereign-release "} {
				if file != "" && line != "" { // an empty line of a pattern file matches every call
					r.write(r.stub, file, line+"\n")
				}
			}
			out, code := r.run("deliver", req)
			logs, st := r.jobLog(req), r.status()
			if (c.evict != "") != strings.Contains(logs, "kubelet: the Job replaces the evicted Pod") || st["phase"] != c.phase || (code == 0) != (c.phase == "delivered") {
				t.Fatalf("exit %d, phase %q, message %q, resume %q\n%s\n%s", code, st["phase"], st["message"], st["resume"], out, logs)
			}
			if !strings.Contains(st["resume"]+st["message"], c.want) || strings.Contains(st["message"], "no region was promoted") {
				t.Errorf("the state does not say %q: message %q, resume %q", c.want, st["message"], st["resume"])
			}
			r.noCredentialLeft(c.name)
			r.retriesOneEviction("job-sovereign-release-delivery.json")
			b, p := r.followerRef("main"), r.remote("main")
			switch {
			case c.phase == "delivered" && (p != r.remote("release/"+req.Candidate) || b != staged || st["region-b.promotedFrom"] != r.followerMain || st["region-b.promotedTo"] != staged):
				t.Errorf("after the resume: primary %s, region-b %s (staged %s), region-b promoted %q -> %q", p, b, staged, st["region-b.promotedFrom"], st["region-b.promotedTo"])
			case c.want == moved && (p != r.main || b != staged || !strings.Contains(st["message"], "promote never reached it: primary") || !strings.Contains(st["resume"], "stopped: it cannot read the state")):
				t.Errorf("primary moved %v, region-b promoted %v; message %q; resume %q", p != r.main, b == staged, st["message"], st["resume"])
			case c.fail != "" && (p != r.main || b != r.followerMain || st["failedStep"] != "prewarm"):
				t.Errorf("a prewarm that cannot record its end moved a main or names step %q", st["failedStep"])
			}
		})
	}
}

// A follower Job whose first Pod an eviction takes runs a second, and its result
// is that Pod's log: kubectl logs job/<job> would read the evicted one.
func TestReleaseFollowerJobResultIsItsCurrentPods(t *testing.T) {
	for _, job := range []string{"sovereign-release-stage-region-b", "sovereign-release-promote-region-b"} {
		t.Run(job, func(t *testing.T) {
			r, req := credentialFixture(t, false, false)
			r.write(r.stub, "evict-follower", "^"+job+"$\n")
			out, code := r.run("stage", req)
			if code == 0 {
				more, c := r.run("deliver", req)
				out, code = out+more, c
			}
			if st := r.status(); code != 0 || st["phase"] != "delivered" || !strings.Contains(st["region-b.promote"], "phase=promoted") {
				t.Fatalf("exit %d, phase %q, region-b %q / %q\n%s\n%s", code, st["phase"], st["region-b.stage"], st["region-b.promote"], out, r.jobLog(req))
			}
			r.retriesOneEviction("job-" + job + ".yaml")
		})
	}
}

// A bootstrap Job whose Pod an eviction takes: the next Pod (its own STATE)
// stages again while no delivery Job exists; once one does, it stages nothing
// and deliver waits for that Job; when it cannot read whether one exists, it
// stops, and nothing is staged again.
func TestReleaseBootstrapResumesAfterAnEviction(t *testing.T) {
	r, req := credentialFixture(t, false, false)
	r.write(r.stub, "patch-kill", `"region-b\.stage"`+"\n")
	if out, code := r.run("stage", req); code == 0 {
		t.Fatalf("the stage was not interrupted\n%s", out)
	}
	for i, want := range []string{"", "resumed: Job sovereign-release-delivery exists", "stopped: it cannot read whether Job sovereign-release-delivery exists"} {
		r.state = r.dir + "/state-" + string(rune('a'+i))
		if i == 2 {
			r.write(r.stub, "read-fail", "^pod= .*get job sovereign-release-delivery \n")
		}
		staged := r.remote("release/" + req.Candidate)
		out, code := r.run("stage", req)
		if code == 0 {
			more, c := r.run("deliver", req)
			out, code = out+more, c
		}
		st := r.status()
		if (code == 0) != (i < 2) || st["phase"] != "delivered" || !strings.Contains(st["resume"], want) || (i > 0 && r.remote("release/"+req.Candidate) != staged) {
			t.Fatalf("Pod %d: exit %d, phase %q, resume %q, staged again %v\n%s", i+2, code, st["phase"], st["resume"], r.remote("release/"+req.Candidate) != staged, out)
		}
	}
	r.noCredentialLeft("after the bootstrap resumed")
	r.retriesOneEviction("job-sovereign-release-stage-region-b.yaml")
}
