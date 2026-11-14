#!/usr/bin/env bash
# Render test of templates/registry-projects-job.yaml (SOV-160, SOV-336). CI runs every
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

# Prints "<job name> | <public projects> | <private projects> | <database project> | <origin URL>"
# of one rendered Job and checks its shape.
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
assert '-X POST "${api}/registries" --data "${body}"' in script, "the Job must POST /registries"
assert '{"name":"%s","type":"docker-registry","url":"%s","insecure":false}' in script, "the endpoint body changed"
assert '"${api}/registries?q=name%3D${reg}"' in script, "the endpoint must be read by its exact name"
assert '{"project_name":"%s","public":true,"registry_id":%s,"metadata":{"public":"true"}}' in script, "the pull-through project body changed"
assert script.count('"${api}/registries') == 2, "the Job may create and read one endpoint only"
assert 'if [ -n "${VULNDB_PROJECT}" ]; then' in script, "the endpoint must be made only for the database project"
print("%s | %s | %s | %s | %s" % (job["metadata"]["name"], env["PUBLIC_PROJECTS"], env["PRIVATE_PROJECTS"],
                                 env["VULNDB_PROJECT"], env["VULNDB_ORIGIN_URL"]))
PY
}

PUBLIC_DEFAULT="proxy-dockerhub proxy-ecr proxy-gcr proxy-ghcr proxy-k8s proxy-quay"
PRIVATE_DEFAULT="node-artifacts openova-io tofu-providers"

echo "[registry-projects] Case 1: default render"
render > "$TMP/1.yaml"
r1="$(inspect "$TMP/1.yaml")"
[ "${r1#* | }" = "$PUBLIC_DEFAULT | $PRIVATE_DEFAULT |  | " ] || fail "default projects: $r1"
render > "$TMP/1b.yaml"
[ "$(inspect "$TMP/1b.yaml")" = "$r1" ] || fail "two renders of the same values differ"

echo "[registry-projects] Case 2: createXpkgProject=true"
render --set registryProjects.createXpkgProject=true > "$TMP/2.yaml"
r2="$(inspect "$TMP/2.yaml")"
[ "${r2#* | }" = "$PUBLIC_DEFAULT proxy-xpkg | $PRIVATE_DEFAULT |  | " ] || fail "with createXpkgProject: $r2"

echo "[registry-projects] Case 3: proxyProjectsJSON as a JSON string"
cat > "$TMP/3-values.yaml" <<'EOF'
registryProjects:
  proxyProjectsJSON: '{"cgr.dev":"proxy-chainguard","quay.io":"proxy-quay","q.example":"proxy-quay/sub","xpkg.upbound.io":"proxy-xpkg","root.example":""}'
EOF
render -f "$TMP/3-values.yaml" > "$TMP/3.yaml"
r3="$(inspect "$TMP/3.yaml")"
[ "${r3#* | }" = "proxy-chainguard proxy-quay | $PRIVATE_DEFAULT |  | " ] || fail "from the JSON string: $r3"
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
  if [ "$set" = full ]; then
    want="$PUBLIC_DEFAULT | $PRIVATE_DEFAULT | proxy-vulndb | https://ghcr.io"
  else
    want="$PUBLIC_DEFAULT | $PRIVATE_DEFAULT |  | "
  fi
  [ "${r6#* | }" = "$want" ] || fail "slot 19 with the $set values: $r6"
done

echo "[registry-projects] Case 7: an origin host makes the database project"
render --set registryProjects.origin.host=registry.parent.example.test > "$TMP/7.yaml"
r7="$(inspect "$TMP/7.yaml")"
[ "${r7#* | }" = "$PUBLIC_DEFAULT | $PRIVATE_DEFAULT | proxy-vulndb | https://registry.parent.example.test" ] \
  || fail "with an origin host: $r7"
[ "${r7%% | *}" != "${r1%% | *}" ] || fail "the Job name did not change with the database project"

echo "[registry-projects] Case 8: origin scheme http and another database path"
render --set registryProjects.origin.host=registry.parent.example.test:5000 \
  --set registryProjects.origin.scheme=http --set registryProjects.vulnDBPath=db-cache/aquasec/trivy-db > "$TMP/8.yaml"
r8="$(inspect "$TMP/8.yaml")"
[ "${r8#* | }" = "$PUBLIC_DEFAULT | $PRIVATE_DEFAULT | db-cache | http://registry.parent.example.test:5000" ] \
  || fail "with scheme http: $r8"

echo "[registry-projects] Case 9: an empty database path makes no database project"
render --set registryProjects.origin.host=registry.parent.example.test --set registryProjects.vulnDBPath= > "$TMP/9.yaml"
r9="$(inspect "$TMP/9.yaml")"
[ "${r9#* | }" = "$PUBLIC_DEFAULT | $PRIVATE_DEFAULT |  | " ] || fail "with an empty database path: $r9"

echo "[registry-projects] Case 10: an origin scheme that is neither https nor http"
if render --set registryProjects.origin.host=registry.parent.example.test \
  --set registryProjects.origin.scheme=ftp > "$TMP/10.yaml" 2>&1; then
  fail "scheme ftp rendered"
fi
grep -q 'must be https or http' "$TMP/10.yaml" || fail "the render failed for another reason: $(cat "$TMP/10.yaml")"

echo "[registry-projects] Case 11: a database project that is also a hosted project"
for clash in proxy-quay/aquasec/trivy-db node-artifacts/trivy-db; do
  if render --set registryProjects.origin.host=registry.parent.example.test \
    --set "registryProjects.vulnDBPath=$clash" > "$TMP/11.yaml" 2>&1; then
    fail "the database path $clash rendered"
  fi
  grep -q 'is also a hosted project' "$TMP/11.yaml" || fail "the render failed for another reason: $(cat "$TMP/11.yaml")"
done

echo "registry-projects-job: all cases passed"
