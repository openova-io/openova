#!/usr/bin/env bash
# bp-storage-ready: render and behaviour test of the storage gate. Needs bash,
# helm and python3 with PyYAML; no cluster. Case 1: the default render works.
# Case 2: the render holds exactly the four hooks with their phases, weights
# and values. Case 3: the Job's script, run against a stub kubectl, exits 0
# only when the expected class carries the default annotation.
# Usage: bash tests/storage-ready-render.sh [CHART_DIR]
set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

echo "[storage-ready] case 1: default render"
helm template storage-ready "$CHART_DIR" --namespace flux-system > "$TMP/default.yaml" \
  || fail "the default render errored"
python3 - "$TMP/default.yaml" <<'PY' || fail "case 1"
import sys, yaml
docs = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
job = [d for d in docs if d["kind"] == "Job"]
assert len(job) == 1, "default render: expected one Job, got %d" % len(job)
env = {e["name"]: e["value"] for e in job[0]["spec"]["template"]["spec"]["containers"][0]["env"]}
assert env["EXPECTED_CLASS"] == "", "default render: EXPECTED_CLASS is %r, expected empty" % env["EXPECTED_CLASS"]
PY
echo "  PASS"

echo "[storage-ready] case 2: render with expectedClass=durable-ssd"
helm template storage-ready "$CHART_DIR" --namespace flux-system \
  --set expectedClass=durable-ssd > "$TMP/set.yaml" || fail "the render with expectedClass errored"
python3 - "$TMP/set.yaml" "$TMP/gate.sh" <<'PY' || fail "case 2"
import sys, yaml
docs = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
kinds = sorted(d["kind"] for d in docs)
assert kinds == ["ClusterRole", "ClusterRoleBinding", "Job", "ServiceAccount"], "objects: %s" % kinds
for d in docs:
    ann = d["metadata"].get("annotations") or {}
    assert ann.get("helm.sh/hook") == "pre-install,pre-upgrade", "%s: hook is %r" % (d["kind"], ann.get("helm.sh/hook"))
    weight = "0" if d["kind"] == "Job" else "-5"
    assert ann.get("helm.sh/hook-weight") == weight, "%s: weight is %r" % (d["kind"], ann.get("helm.sh/hook-weight"))
role = [d for d in docs if d["kind"] == "ClusterRole"][0]
assert role["rules"] == [{"apiGroups": ["storage.k8s.io"], "resources": ["storageclasses"], "verbs": ["get", "list"]}], role["rules"]
job = [d for d in docs if d["kind"] == "Job"][0]
assert job["spec"]["backoffLimit"] == 0, "backoffLimit is %r" % job["spec"]["backoffLimit"]
assert job["metadata"]["labels"].get("app.kubernetes.io/managed-by") == "flux", "Job lacks managed-by: flux"
c = job["spec"]["template"]["spec"]["containers"][0]
env = {e["name"]: e["value"] for e in c["env"]}
assert env == {"EXPECTED_CLASS": "durable-ssd", "TIMEOUT_SECONDS": "600", "INTERVAL_SECONDS": "5"}, env
assert c["command"] == ["/bin/bash", "-c"], c["command"]
open(sys.argv[2], "w").write(c["args"][0])
PY
echo "  PASS"

# A stub kubectl: "get storageclass <name> -o <jsonpath of the default annotation>"
# prints the annotation of the one class named in STUB_CLASS, which is STUB_VALUE.
# Any other class is absent (exit 1). "get storageclass" alone lists nothing.
mkdir -p "$TMP/bin"
cat > "$TMP/bin/kubectl" <<'STUB'
#!/usr/bin/env bash
want='jsonpath={.metadata.annotations.storageclass\.kubernetes\.io/is-default-class}'
if [ "$#" -eq 2 ] && [ "$1 $2" = "get storageclass" ]; then exit 0; fi
[ "$#" -eq 5 ] && [ "$1 $2" = "get storageclass" ] && [ "$4" = "-o" ] && [ "$5" = "$want" ] \
  || { echo "stub kubectl: unexpected arguments: $*" >&2; exit 3; }
[ "$3" = "${STUB_CLASS:-}" ] || { echo "Error from server (NotFound)" >&2; exit 1; }
printf '%s' "${STUB_VALUE:-}"
STUB
chmod +x "$TMP/bin/kubectl"

run_gate() { # <expected class> <timeout> <stub class> <stub value>
  EXPECTED_CLASS="$1" TIMEOUT_SECONDS="$2" INTERVAL_SECONDS=0 STUB_CLASS="$3" STUB_VALUE="$4" \
    PATH="$TMP/bin:$PATH" bash -c "$(cat "$TMP/gate.sh")" > "$TMP/out" 2>&1
}

echo "[storage-ready] case 3a: the expected class is the default"
run_gate durable-ssd 2 durable-ssd true || { cat "$TMP/out" >&2; fail "case 3a: the gate did not exit 0"; }
grep -q 'StorageClass durable-ssd is the cluster default' "$TMP/out" || fail "case 3a: no success line"
echo "  PASS"

echo "[storage-ready] case 3b: the class exists and is not the default"
if run_gate durable-ssd 0 durable-ssd false; then fail "case 3b: the gate exited 0"; fi
grep -q 'is not the cluster default after 0s' "$TMP/out" || { cat "$TMP/out" >&2; fail "case 3b: no time-out line"; }
echo "  PASS"

echo "[storage-ready] case 3c: the class does not exist"
if run_gate durable-ssd 0 other ""; then fail "case 3c: the gate exited 0"; fi
grep -q 'is not the cluster default after 0s' "$TMP/out" || { cat "$TMP/out" >&2; fail "case 3c: no time-out line"; }
echo "  PASS"

echo "[storage-ready] case 3d: expectedClass is empty"
if run_gate "" 2 durable-ssd true; then fail "case 3d: the gate exited 0"; fi
grep -q 'the value expectedClass is empty' "$TMP/out" || { cat "$TMP/out" >&2; fail "case 3d: no empty-value line"; }
echo "  PASS"

echo "[storage-ready] case 3e: only another class is the default"
if run_gate durable-ssd 0 other-class true; then fail "case 3e: the gate exited 0"; fi
echo "  PASS"

echo "[storage-ready] all cases passed"
