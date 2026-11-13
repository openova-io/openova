#!/usr/bin/env bash
# Render test of templates/platform-repos-job.yaml (SOV-160). CI runs every
# chart/tests/*.sh as `bash <script> <chart dir>`. Each case is named where it runs.
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
if render --set platformRepos.regionId=region-b --set platformRepos.writerRegionId=region-a > "$TMP/2.yaml" 2>&1; then
  fail "a Job rendered outside the writer region"
fi

echo "[platform-repos] Case 3: the writer region"
render --set platformRepos.regionId=region-a --set platformRepos.writerRegionId=region-a > "$TMP/3.yaml"
[ "$(inspect "$TMP/3.yaml")" = "$WANT" ] || fail "writer region: $(inspect "$TMP/3.yaml")"

echo "[platform-repos] Case 4: enabled=false"
if render --set platformRepos.enabled=false > "$TMP/4.yaml" 2>&1; then
  fail "a Job rendered with enabled=false"
fi

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
if render -f "$TMP/5-other-values.yaml" > "$TMP/5-other.yaml" 2>&1; then
  fail "slot 10 rendered the Job in a region that is not the writer"
fi

echo "platform-repos-job: all cases passed"
