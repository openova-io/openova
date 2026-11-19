#!/usr/bin/env bash
# crossplane-runtime-suite-current.sh - the suite crossplane-package-runtime-admitted
# judges the policies and the runtime as they are, not a stale copy of them.
#
# The suite is a `kyverno test` directory: it holds a COPY of the two policies
# (policy.yaml) and fixtures of the Deployments Crossplane builds
# (resources.yaml). A copy that no longer equals the templates, or fixtures
# whose probes are not the probes the runtime configs give, would keep the
# suite green while the real admission changed. This test fails then:
#   C1 policy.yaml equals the render of templates/baseline/10-flux-managed.yaml
#      and templates/baseline/04-probes.yaml of this chart;
#   C2 the probes of the runtime container in the two Crossplane Deployments of
#      resources.yaml equal the probes of the runtime configs in
#      clusters/_template/infrastructure/providers/base.
#
# Usage:
#   bash crossplane-runtime-suite-current.sh              the chart above this file
#   bash crossplane-runtime-suite-current.sh <chart>      that chart directory
#                                                         (Chart tests (PR) passes it)
#   bash crossplane-runtime-suite-current.sh --self-test  prove both checks can fail
#
# Exit code: 0 pass. 1 a check failed. 2 wrong usage or a render failed.
set -euo pipefail
export LC_ALL=C

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SELF_TEST=0
CHART="$(cd "$HERE/.." && pwd)"
case "${1:-}" in
  --self-test) SELF_TEST=1 ;;
  "") ;;
  *) [ -d "$1" ] || { echo "usage: $0 [<chart>|--self-test]" >&2; exit 2; }
     CHART="$(cd "$1" && pwd)" ;;
esac
REPO="$(cd "$CHART/../../.." && pwd)"
SUITE="$CHART/tests/crossplane-package-runtime-admitted"
CONFIGS="$REPO/clusters/_template/infrastructure/providers/base"
TAG="[crossplane-runtime-suite-current]"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

render() {
  helm template "$1" \
    --show-only templates/baseline/10-flux-managed.yaml \
    --show-only templates/baseline/04-probes.yaml \
    | grep -v '^# Source:' | sed '1{/^---$/d}'
}

# check_copy <chart> <suite>: C1.
check_copy() {
  render "$1" > "$TMP/render.yaml" || { echo "$TAG render of $1 failed"; return 2; }
  if ! grep -q '^  name: flux-managed$' "$TMP/render.yaml" || ! grep -q '^  name: probes-present$' "$TMP/render.yaml"; then
    echo "$TAG FAIL C1: the render holds not both policies"
    return 1
  fi
  if ! diff -u "$2/policy.yaml" "$TMP/render.yaml" > "$TMP/diff.txt"; then
    cat "$TMP/diff.txt"
    echo "$TAG FAIL C1: $2/policy.yaml is not the current render of the two templates. Re-render it (see kyverno-test.yaml)."
    return 1
  fi
  echo "$TAG PASS C1: policy.yaml is the current render"
}

# check_probes <resources.yaml> <configs dir>: C2.
check_probes() {
  python3 - "$1" "$2" <<'EOF'
import sys, yaml
res, cfg = sys.argv[1], sys.argv[2]
pairs = {
    "provider-opentofu-81ec1e8dd865": "provider-opentofu.yaml",
    "function-patch-and-transform-e9fec78aeef5": "function-patch-and-transform.yaml",
}
def runtime(containers):
    for c in containers:
        if c.get("name") == "package-runtime":
            return c
    return {}
deps = {d["metadata"]["name"]: d for d in yaml.safe_load_all(open(res))
        if d and d.get("kind") == "Deployment"}
bad = 0
for dep, f in pairs.items():
    drc = [d for d in yaml.safe_load_all(open(f"{cfg}/{f}")) if d and d.get("kind") == "DeploymentRuntimeConfig"]
    if len(drc) != 1 or dep not in deps:
        print(f"FAIL C2: {f} or Deployment {dep} not found"); bad += 1; continue
    want = runtime(drc[0]["spec"]["deploymentTemplate"]["spec"]["template"]["spec"]["containers"])
    got = runtime(deps[dep]["spec"]["template"]["spec"]["containers"])
    for p in ("livenessProbe", "readinessProbe"):
        if not want.get(p) or want.get(p) != got.get(p):
            print(f"FAIL C2: {p} of {dep} in resources.yaml is {got.get(p)}, {f} gives {want.get(p)}")
            bad += 1
sys.exit(1 if bad else 0)
EOF
  local rc=$?
  [ "$rc" -eq 0 ] && echo "$TAG PASS C2: the fixtures carry the probes of the runtime configs"
  return "$rc"
}

if [ "$SELF_TEST" -eq 0 ]; then
  rc=0
  check_copy "$CHART" "$SUITE" || rc=1
  check_probes "$SUITE/resources.yaml" "$CONFIGS" || rc=1
  exit "$rc"
fi

# Self-test: each check must go red on a copy with one change.
fails=0
mkdir -p "$TMP/chart" "$TMP/suite" "$TMP/configs"
cp -R "$CHART/." "$TMP/chart/"
cp "$SUITE/"* "$TMP/suite/"
cp "$CONFIGS/"*.yaml "$TMP/configs/"
sed -i 's/operator: AnyNotIn/operator: AnyIn/' "$TMP/chart/templates/baseline/10-flux-managed.yaml"
if check_copy "$TMP/chart" "$TMP/suite" > "$TMP/st1.txt" 2>&1; then
  echo "self-test FAIL: C1 passed with a changed template"; fails=$((fails + 1))
else echo "self-test ok: C1 fails when the template changes and the copy does not"; fi
python3 - "$TMP/configs/provider-opentofu.yaml" <<'EOF'
import sys
p = sys.argv[1]
t = open(p).read()
assert "initialDelaySeconds: 30" in t
open(p, "w").write(t.replace("initialDelaySeconds: 30", "initialDelaySeconds: 31"))
EOF
if check_probes "$TMP/suite/resources.yaml" "$TMP/configs" > "$TMP/st2.txt" 2>&1; then
  echo "self-test FAIL: C2 passed with a changed runtime config"; fails=$((fails + 1))
else echo "self-test ok: C2 fails when a runtime config's probe changes and the fixture does not"; fi
[ "$fails" -eq 0 ] || exit 1
echo "self-test: 2 cases passed"
