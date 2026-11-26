#!/usr/bin/env bash
# Render test of templates/platform-repos-job.yaml (SOV-160, SOV-163). CI runs every
# chart/tests/*.sh as `bash <script> <chart dir>`. Each case is named where it runs.
# It needs helm and yq (the chart tests' yq, v4.44.3).
set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
REPO="$(cd "$CHART_DIR/../../.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail() { echo "FAIL: $1" >&2; exit 1; }

cd "$CHART_DIR"
if ! ls charts/*.tgz >/dev/null 2>&1; then
  helm dependency build >/dev/null
fi

render() {
  helm template gitea . --namespace gitea --show-only templates/platform-repos-job.yaml "$@"
}

# The render leaves the Job out: helm refuses --show-only of a template that renders
# nothing, and only that refusal passes (any other error fails the case).
absent() {
  local name="$1"
  shift
  if render "$@" > "$TMP/absent.out" 2>&1; then
    fail "$name: a Job rendered"
  fi
  grep -q -x -F 'Error: could not find template templates/platform-repos-job.yaml in chart' "$TMP/absent.out" \
    || fail "$name: the render failed for another reason: $(cat "$TMP/absent.out")"
}

# Checks the shape of one rendered Job and prints "<org> | <repositories> | <address>".
inspect() {
  python3 - "$1" <<'PY'
import sys, yaml
docs = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
assert len(docs) == 1 and docs[0]["kind"] == "Job", "expected exactly one Job"
job = docs[0]
assert job["metadata"]["name"].startswith("gitea-platform-repos-"), job["metadata"]["name"]
assert job["metadata"]["namespace"] == "gitea"
assert "helm.sh/hook" not in (job["metadata"].get("annotations") or {}), "the Job must not be a Helm hook"
pod = job["spec"]["template"]["spec"]
assert pod["restartPolicy"] == "Never"
assert pod["securityContext"]["runAsNonRoot"] is True
(c,) = pod["containers"]
assert "/proxy-" in c["image"], "the image must come through a proxy project: " + c["image"]
req = c["resources"]["requests"]
assert req.get("cpu") and req.get("memory"), "cpu and memory requests are required"
assert c["securityContext"]["readOnlyRootFilesystem"] is True
env = {e["name"]: e.get("value") for e in c["env"]}
vols = {v["name"]: v for v in pod["volumes"]}
assert vols["gitea-admin"]["secret"]["secretName"] == "gitea-admin-secret"
script = c["args"][0]
for text in ('{\\"username\\":\\"${ORG}\\",\\"visibility\\":\\"public\\"}',
             '\\"private\\":true,\\"auto_init\\":true,\\"default_branch\\":\\"main\\"',
             '"/repos/${ORG}/${r}/contents/README.md"'):
    assert text in script, "the script lacks " + text
assert "DELETE" not in script and "PATCH" not in script, "the Job must never change an existing repository"
print("%s | %s | %s" % (env["ORG"], env["REPOS"], env["GITEA_URL"]))
PY
}

WANT="openova | sovereign-regions provider-pack | http://gitea-http.gitea.svc.cluster.local:3000"

echo "[platform-repos] Case 1: default render"
render > "$TMP/1.yaml"
[ "$(inspect "$TMP/1.yaml")" = "$WANT" ] || fail "default render: $(inspect "$TMP/1.yaml")"

echo "[platform-repos] Case 2: a region that is not the writer"
absent "outside the writer region" --set platformRepos.regionId=region-b --set platformRepos.writerRegionId=region-a

echo "[platform-repos] Case 3: the writer region"
render --set platformRepos.regionId=region-a --set platformRepos.writerRegionId=region-a > "$TMP/3.yaml"
[ "$(inspect "$TMP/3.yaml")" = "$WANT" ] || fail "writer region: $(inspect "$TMP/3.yaml")"

echo "[platform-repos] Case 4: enabled=false"
absent "enabled=false" --set platformRepos.enabled=false

echo "[platform-repos] Case 5: the values of bootstrap-kit slot 10"
for region in same other; do
  python3 - "$REPO" "$region" > "$TMP/5-$region-values.yaml" <<'PY'
import re, sys, yaml
repo, region = sys.argv[1:]
values = {}
for line in open(repo + "/tests/fixtures/variables/today.env"):
    line = line.rstrip("\n")
    if line and not line.startswith("#"):
        k, _, v = line.partition("=")
        values[k] = v
if region == "other":
    values["SOVEREIGN_REGION_ID"] = values["SOVEREIGN_WRITER_REGION_ID"] + "-other"
form = re.compile(r"\$\{([A-Za-z_][A-Za-z0-9_]*)(?::=([^}]*))?\}")
def sub(x):
    if isinstance(x, dict):
        return {k: sub(v) for k, v in x.items()}
    if isinstance(x, str):
        return form.sub(lambda m: values.get(m.group(1)) or (m.group(2) or ""), x)
    return x
docs = yaml.safe_load_all(open(repo + "/clusters/_template/bootstrap-kit/10-gitea.yaml"))
(hr,) = [d for d in docs if d and d.get("kind") == "HelmRelease" and d["metadata"]["name"] == "bp-gitea"]
print(yaml.safe_dump({"platformRepos": sub(hr["spec"]["values"]["platformRepos"])}))
PY
done
render -f "$TMP/5-same-values.yaml" > "$TMP/5-same.yaml"
[ "$(inspect "$TMP/5-same.yaml")" = "$WANT" ] || fail "slot 10 in the writer region: $(inspect "$TMP/5-same.yaml")"
absent "slot 10 in a region that is not the writer" -f "$TMP/5-other-values.yaml"

echo "[platform-repos] Case 6: a follower region"
absent "follower" --set platformRepos.follower=true
absent "follower named the writer" --set platformRepos.follower=true \
  --set platformRepos.regionId=region-a --set platformRepos.writerRegionId=region-a
absent "follower as a string" --set-string platformRepos.follower=true
render --set-string platformRepos.follower=false > "$TMP/6.yaml"
[ "$(inspect "$TMP/6.yaml")" = "$WANT" ] || fail "follower=false as a string: $(inspect "$TMP/6.yaml")"

echo "[platform-repos] Case 7: slot 10 on an install whose cloud-init wrote no region ids"
# Substituted as Flux does, on the text: SECONDARY_HR_SUSPEND set, every other key unset.
slot10() {
  sed -e "s/\${SECONDARY_HR_SUSPEND:=[^}]*}/$1/g" -e 's/\${[A-Za-z_][A-Za-z0-9_]*:=\([^}]*\)}/\1/g' \
    "$REPO/clusters/_template/bootstrap-kit/10-gitea.yaml" \
    | yq 'select(.kind == "HelmRelease" and .metadata.name == "bp-gitea") | {"platformRepos": .spec.values.platformRepos}'
}
for side in true false; do
  slot10 "$side" > "$TMP/7-$side-values.yaml"
  got="$(yq -o=json -I=0 '.platformRepos' "$TMP/7-$side-values.yaml")"
  [ "$got" = "{\"follower\":$side,\"regionId\":\"\",\"writerRegionId\":\"\"}" ] || fail "slot 10 values ($side): $got"
done
absent "slot 10 on a secondary control plane" -f "$TMP/7-true-values.yaml"
render -f "$TMP/7-false-values.yaml" > "$TMP/7-false.yaml"
[ "$(inspect "$TMP/7-false.yaml")" = "$WANT" ] || fail "slot 10 on the primary: $(inspect "$TMP/7-false.yaml")"

echo "[platform-repos] Case 8: the Job's script against a stand-in Gitea"
got="$(yq -o=json -I=0 '.spec.podFailurePolicy' "$TMP/1.yaml")"
[ "$got" = '{"rules":[{"action":"FailJob","onExitCodes":{"containerName":"ensure","operator":"In","values":[3]}}]}' ] \
  || fail "podFailurePolicy: $got"
[ "$(yq '.spec.backoffLimit' "$TMP/1.yaml")" = 6 ] || fail "backoffLimit: $(yq '.spec.backoffLimit' "$TMP/1.yaml")"
mkdir -p "$TMP/bin" "$TMP/admin"
printf gitea_admin > "$TMP/admin/username"
printf secret > "$TMP/admin/password"
printf '#!/bin/sh\nexit 0\n' > "$TMP/bin/sleep"
# Logs "<method> <path>" of each request and answers by FAKE_MODE: refuse = 401 to everything;
# wait = no answer to the first two requests, then 200; broken = 200 to GET /user, 500 to the rest.
cat > "$TMP/bin/curl" <<'SH'
#!/bin/sh
out=/dev/null; method=GET; url=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    -X) method="$2"; shift 2 ;;
    -w|-u|-H|--max-time|--data) shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
req="${method} ${url#*/api/v1}"
echo "$req" >> "$FAKE_LOG"
n="$(grep -c . "$FAKE_LOG")"
case "$FAKE_MODE" in
  refuse) printf '%s' '{"message":"user'"'"'s password is invalid"}' > "$out"; printf 401 ;;
  wait) if [ "$n" -le 2 ]; then printf 000; exit 7; fi; printf '%s' '{"empty":false}' > "$out"; printf 200 ;;
  broken) if [ "$req" = "GET /user" ]; then printf 200; else printf 500; fi ;;
esac
SH
chmod +x "$TMP/bin/curl" "$TMP/bin/sleep"
yq '.spec.template.spec.containers[0].args[0]' "$TMP/1.yaml" \
  | sed -e "s#/etc/gitea-admin/#$TMP/admin/#g" -e "s#/tmp/out#$TMP/out#g" > "$TMP/job.sh"
jobenv() { yq ".spec.template.spec.containers[0].env[] | select(.name == \"$1\") | .value" "$TMP/1.yaml"; }
# Runs the script with the Job's env and prints its exit code.
runjob() {
  local rc=0
  : > "$TMP/$1.log"
  env FAKE_MODE="$1" FAKE_LOG="$TMP/$1.log" PATH="$TMP/bin:$PATH" GITEA_URL="$(jobenv GITEA_URL)" \
    ORG="$(jobenv ORG)" REPOS="$(jobenv REPOS)" ADMIN_SECRET="$(jobenv ADMIN_SECRET)" \
    sh "$TMP/job.sh" > "$TMP/$1.out" 2> "$TMP/$1.err" || rc=$?
  echo "$rc"
}
rc="$(runjob refuse)"
[ "$rc" = 3 ] || fail "a refused login exits $rc, not 3: $(cat "$TMP/refuse.err")"
[ "$(cat "$TMP/refuse.log")" = "GET /user" ] || fail "a refused login was tried again: $(cat "$TMP/refuse.log")"
grep -q -x -F '[platform-repos] FATAL: Gitea refused user gitea_admin (401 on GET /user) with the password of Secret gitea/gitea-admin-secret of this region.' "$TMP/refuse.err" \
  || fail "the refusal does not name the user and the Secret: $(cat "$TMP/refuse.err")"
grep -q -F "user's password is invalid" "$TMP/refuse.err" || fail "the refusal does not show Gitea's answer: $(cat "$TMP/refuse.err")"
rc="$(runjob wait)"
[ "$rc" = 0 ] || fail "a Gitea that answers late: exit $rc: $(cat "$TMP/wait.err")"
[ "$(cat "$TMP/wait.log")" = "$(printf '%s\n' 'GET /user' 'GET /user' 'GET /user' 'GET /orgs/openova' \
  'GET /repos/openova/sovereign-regions' 'GET /repos/openova/provider-pack')" ] || fail "a Gitea that answers late: $(cat "$TMP/wait.log")"
[ "$(tail -n 1 "$TMP/wait.out")" = "[platform-repos] every repository exists" ] || fail "a Gitea that answers late: $(cat "$TMP/wait.out")"
rc="$(runjob broken)"
[ "$rc" = 1 ] || fail "a failure other than a refusal exits $rc, not 1 (retried): $(cat "$TMP/broken.err")"
[ "$(cat "$TMP/broken.log")" = "$(printf '%s\n' 'GET /user' 'GET /orgs/openova')" ] || fail "a 500: $(cat "$TMP/broken.log")"

echo "platform-repos-job: all cases passed"
