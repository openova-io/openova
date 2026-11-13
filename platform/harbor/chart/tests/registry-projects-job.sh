#!/usr/bin/env bash
# Render test of templates/registry-projects-job.yaml (SOV-160). CI runs every
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
  helm template harbor . --namespace harbor --show-only templates/registry-projects-job.yaml "$@"
}

# Prints "<job name> | <public projects> | <private projects>" of one rendered Job and checks its shape.
inspect() {
  python3 - "$1" <<'PY'
import sys, yaml
docs = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
assert len(docs) == 1 and docs[0]["kind"] == "Job", "expected exactly one Job"
job = docs[0]
assert job["metadata"]["name"].startswith("harbor-registry-projects-"), job["metadata"]["name"]
assert job["metadata"]["namespace"] == "harbor"
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
assert env["HARBOR_CORE_URL"] == "http://harbor-core.harbor.svc.cluster.local", env["HARBOR_CORE_URL"]
vols = {v["name"]: v for v in pod["volumes"]}
assert vols["harbor-admin"]["secret"]["secretName"] == "harbor-admin"
script = c["args"][0]
assert '"${api}/projects" --data "${body}"' in script, "the Job must POST /projects"
assert '{"project_name":"%s","public":%s,"metadata":{"public":"%s"}}' in script, "the project body changed"
assert "registries" not in script, "the Job must not create a registry endpoint"
print("%s | %s | %s" % (job["metadata"]["name"], env["PUBLIC_PROJECTS"], env["PRIVATE_PROJECTS"]))
PY
}

PUBLIC_DEFAULT="proxy-dockerhub proxy-ecr proxy-gcr proxy-ghcr proxy-k8s proxy-quay"
PRIVATE_DEFAULT="node-artifacts openova-io tofu-providers"

echo "[registry-projects] Case 1: default render"
render > "$TMP/1.yaml"
r1="$(inspect "$TMP/1.yaml")"
[ "${r1#* | }" = "$PUBLIC_DEFAULT | $PRIVATE_DEFAULT" ] || fail "default projects: $r1"
render > "$TMP/1b.yaml"
[ "$(inspect "$TMP/1b.yaml")" = "$r1" ] || fail "two renders of the same values differ"

echo "[registry-projects] Case 2: createXpkgProject=true"
render --set registryProjects.createXpkgProject=true > "$TMP/2.yaml"
r2="$(inspect "$TMP/2.yaml")"
[ "${r2#* | }" = "$PUBLIC_DEFAULT proxy-xpkg | $PRIVATE_DEFAULT" ] || fail "with createXpkgProject: $r2"

echo "[registry-projects] Case 3: proxyProjectsJSON as a JSON string"
cat > "$TMP/3-values.yaml" <<'EOF'
registryProjects:
  proxyProjectsJSON: '{"cgr.dev":"proxy-chainguard","quay.io":"proxy-quay","q.example":"proxy-quay/sub","xpkg.upbound.io":"proxy-xpkg","root.example":""}'
EOF
render -f "$TMP/3-values.yaml" > "$TMP/3.yaml"
r3="$(inspect "$TMP/3.yaml")"
[ "${r3#* | }" = "proxy-chainguard proxy-quay | $PRIVATE_DEFAULT" ] || fail "from the JSON string: $r3"
[ "${r3%% | *}" != "${r1%% | *}" ] || fail "the Job name did not change with its projects"

echo "[registry-projects] Case 4: proxyProjectsJSON that is not a JSON object"
printf '%s\n' 'registryProjects:' "  proxyProjectsJSON: '[\"proxy-quay\"]'" > "$TMP/4-values.yaml"
if render -f "$TMP/4-values.yaml" > "$TMP/4.yaml" 2>&1; then
  fail "a JSON array rendered"
fi
grep -q 'is not a JSON object' "$TMP/4.yaml" || fail "the render failed for another reason: $(cat "$TMP/4.yaml")"

echo "[registry-projects] Case 5: enabled=false"
if render --set registryProjects.enabled=false > "$TMP/5.yaml" 2>&1; then
  fail "a Job rendered with enabled=false"
fi

echo "[registry-projects] Case 6: the values of bootstrap-kit slot 19"
for set in full kit; do
  python3 - "$REPO" "$set" > "$TMP/6-$set-values.yaml" <<'PY'
import re, sys, yaml
repo, which = sys.argv[1:]
values = {}
for line in open(repo + "/tests/fixtures/variables/today.env"):
    line = line.rstrip("\n")
    if line and not line.startswith("#"):
        k, _, v = line.partition("=")
        values[k] = v
if which == "kit":
    keep = {l.strip() for l in open(repo + "/docs/contracts/c5-kit-keys.txt") if l.strip()}
    values = {k: v for k, v in values.items() if k in keep}
form = re.compile(r"\$\{([A-Za-z_][A-Za-z0-9_]*)(?::=([^}]*))?\}")
def sub(x):
    if isinstance(x, dict):
        return {k: sub(v) for k, v in x.items()}
    if isinstance(x, str):
        return form.sub(lambda m: values.get(m.group(1)) or (m.group(2) or ""), x)
    return x
docs = yaml.safe_load_all(open(repo + "/clusters/_template/bootstrap-kit/19-harbor.yaml"))
(hr,) = [d for d in docs if d and d.get("kind") == "HelmRelease" and d["metadata"]["name"] == "bp-harbor"]
print(yaml.safe_dump({"registryProjects": sub(hr["spec"]["values"]["registryProjects"])}))
PY
  render -f "$TMP/6-$set-values.yaml" > "$TMP/6-$set.yaml"
  r6="$(inspect "$TMP/6-$set.yaml")"
  [ "${r6#* | }" = "$PUBLIC_DEFAULT | $PRIVATE_DEFAULT" ] || fail "slot 19 with the $set values: $r6"
done

echo "registry-projects-job: all cases passed"
