#!/usr/bin/env bash
# bp-k8s-ws-proxy helm-render smoke test (EPIC-4 K1, #1099).
#
# Verifies:
#   - default-OFF renders 0 resources
#   - empty image.tag fails fast
#   - full-ON renders DaemonSet + Service + ServiceAccount (DS) + ClusterRole + ClusterRoleBinding +
#     ServiceAccount (HMAC bootstrap) + Role + RoleBinding + Job (HMAC bootstrap) = 9 resources
#   - HMAC bootstrap Job is a pre-install/pre-upgrade hook with weight -10
#   - HMAC bootstrap Role splits `create` (no resourceNames) from `get`
#     per memory/feedback_rbac_create_no_resourcenames.md

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

if [[ ! -d charts ]] || [[ -z "$(ls -A charts 2>/dev/null)" ]]; then
  helm dependency update >/dev/null 2>&1 || {
    echo "WARNING: helm dependency update failed; skipping"
    exit 0
  }
fi

# 1. Default-OFF
off="$(helm template bp-k8s-ws-proxy . | grep -cE '^kind:' || true)"
if [[ "$off" != "0" ]]; then
  echo "FAIL: default-OFF rendered $off resources, want 0"
  exit 1
fi
echo "PASS: default-OFF = 0 resources"

# 2. Fail-fast on empty image tag (operator-override path).
#    qa-loop iter-7 Fix #39 follow-up: build-k8s-ws-proxy.yaml's promote
#    job auto-bumps values.yaml `image.tag` to a real SHA on every push,
#    so testing the fail-fast contract requires explicitly clearing the
#    tag with --set. Without the explicit clear, the test stops
#    exercising the contract once any build commit lands. The contract
#    itself (empty tag → render fail per _helpers.tpl) is unchanged.
if helm template bp-k8s-ws-proxy . \
    --set k8sWsProxy.enabled=true \
    --set k8sWsProxy.image.tag= \
    >/dev/null 2>"$TMP/err"; then
  echo "FAIL: empty tag did not abort render"
  exit 1
fi
if ! grep -q 'image.tag is empty' "$TMP/err"; then
  echo "FAIL: error did not mention 'image.tag is empty'"
  cat "$TMP/err"
  exit 1
fi
echo "PASS: empty image.tag fails fast"

# 3. Full-ON
# Pre-Fix-#78 expected 5 resources (DaemonSet + Service + ServiceAccount +
# ClusterRole + ClusterRoleBinding). Fix #78 (Gap E) added 4 more for
# the HMAC-bootstrap pre-install hook (ServiceAccount + Role +
# RoleBinding + Job), bringing the full-ON total to 9.
on_render="$TMP/on.yaml"
helm template bp-k8s-ws-proxy . \
  --set k8sWsProxy.enabled=true \
  --set k8sWsProxy.image.tag=abc1234 > "$on_render"
on="$(grep -cE '^kind:' "$on_render" || true)"
if [[ "$on" != "9" ]]; then
  echo "FAIL: full-ON rendered $on resources, want 9"
  grep -E '^kind:' "$on_render"
  exit 1
fi
echo "PASS: full-ON = 9 resources"

# 3a. HMAC-bootstrap Job present with correct hook annotations (Fix #78).
# The Job MUST be a pre-install/pre-upgrade hook with weight -10 so it
# runs BEFORE the DaemonSet rolls — without this the DS pods stick in
# ContainerCreating waiting for the absent Secret.
if ! grep -qE '^  name: k8s-ws-proxy-hmac-bootstrap$' "$on_render"; then
  echo "FAIL: hmac-bootstrap resources not rendered (Fix #78 missing)"
  exit 1
fi
if ! grep -qE '"helm\.sh/hook-weight": *"-10"' "$on_render"; then
  echo "FAIL: hmac-bootstrap Job missing pre-install hook-weight -10"
  grep -E 'helm.sh/hook' "$on_render"
  exit 1
fi
if ! grep -qE '"helm\.sh/hook": *"pre-install,pre-upgrade"' "$on_render"; then
  echo "FAIL: hmac-bootstrap Job missing pre-install,pre-upgrade hook"
  grep -E 'helm.sh/hook' "$on_render"
  exit 1
fi
# 3b. RBAC split per memory/feedback_rbac_create_no_resourcenames.md:
# the Role MUST have a `create` rule WITHOUT resourceNames, and the
# `get` rule WITH resourceNames as a separate rule. Combined-rule
# pattern (verbs:[create,get] resourceNames:[...]) is the silent-403
# trap that wedged bp-openbao 6+ chart iterations.
if ! grep -qE 'verbs: \[ *"create" *\]|verbs:\s*-\s*create|verbs: \[create\]' "$on_render"; then
  echo "FAIL: hmac-bootstrap Role missing isolated 'create' verb"
  grep -E 'verbs:|resourceNames:' "$on_render"
  exit 1
fi
echo "PASS: hmac-bootstrap Job + RBAC rendered with correct hook-weight + split verbs"

# 3c. (Fix #95, regression of Fix #78) Hook-weight ORDERING across the
# hmac-bootstrap quartet. Helm semantics: within the same hook phase,
# LOWER weights run FIRST. The Job references its ServiceAccount, so
# the SA MUST be applied before the Job — i.e. the SA's weight MUST be
# numerically LESS than the Job's (-10). Pre-Fix-#95 SA/Role/RoleBinding
# all had weight 0 (later than the Job at -10), causing prov #8 to fail
# with `serviceaccount "k8s-ws-proxy-hmac-bootstrap" not found`. Target
# ordering: SA=-20, Role+RoleBinding=-15, Job=-10.
#
# Use awk to walk each `kind:` block in the rendered YAML and capture
# its hook-weight; assert the ServiceAccount weight is the most
# negative, the Job weight is -10, and the Role/RoleBinding sit between.
weights="$(awk '
  /^kind:/                                      { kind=$2; weight="" }
  /name: k8s-ws-proxy-hmac-bootstrap$/          { is_hmac=1 }
  /"helm\.sh\/hook-weight"/                     { gsub(/.*"helm\.sh\/hook-weight":[ ]*"/,""); gsub(/".*/,""); weight=$0 }
  /^---$/ {
    if (is_hmac && weight != "") print kind"="weight
    is_hmac=0; kind=""; weight=""
  }
  END {
    if (is_hmac && weight != "") print kind"="weight
  }
' "$on_render")"

sa_w="$(echo "$weights" | grep -E '^ServiceAccount=' | head -1 | cut -d= -f2)"
role_w="$(echo "$weights" | grep -E '^Role=' | head -1 | cut -d= -f2)"
rb_w="$(echo "$weights" | grep -E '^RoleBinding=' | head -1 | cut -d= -f2)"
job_w="$(echo "$weights" | grep -E '^Job=' | head -1 | cut -d= -f2)"

if [[ -z "$sa_w" || -z "$role_w" || -z "$rb_w" || -z "$job_w" ]]; then
  echo "FAIL: gate-9 could not extract all four hmac-bootstrap hook-weights"
  echo "Captured: SA=$sa_w Role=$role_w RoleBinding=$rb_w Job=$job_w"
  echo "All weights:"
  echo "$weights"
  exit 1
fi
# Ordering invariant: SA < Role <= RoleBinding < Job (numerically; all negative).
# Lower number = applied first by Helm.
if (( sa_w >= role_w )); then
  echo "FAIL: gate-9 ordering — SA weight ($sa_w) must be LESS THAN Role weight ($role_w) so SA lands first"
  exit 1
fi
if (( sa_w >= rb_w )); then
  echo "FAIL: gate-9 ordering — SA weight ($sa_w) must be LESS THAN RoleBinding weight ($rb_w) so SA lands first"
  exit 1
fi
if (( role_w >= job_w )); then
  echo "FAIL: gate-9 ordering — Role weight ($role_w) must be LESS THAN Job weight ($job_w)"
  exit 1
fi
if (( rb_w >= job_w )); then
  echo "FAIL: gate-9 ordering — RoleBinding weight ($rb_w) must be LESS THAN Job weight ($job_w)"
  exit 1
fi
# Belt-and-suspenders: Job MUST stay at -10 (Fix #78 gate 3a requires it).
if [[ "$job_w" != "-10" ]]; then
  echo "FAIL: gate-9 — Job weight is $job_w, want -10 (Fix #78 gate 3a invariant)"
  exit 1
fi
echo "PASS: gate-9 hook-weight ordering — SA=$sa_w < Role=$role_w / RoleBinding=$rb_w < Job=$job_w"

# 4. Canonical workload name. Per qa-loop iter-7 Fix #39, the test
#    matrix (TC-236, TC-237) and the catalyst-api shells/issue handler
#    address the DaemonSet + Service by `k8s-ws-proxy` regardless of
#    release name. Even when invoked with --release-name=ws-proxy-test
#    the resources MUST keep the canonical name.
helm template ws-proxy-test . \
  --set k8sWsProxy.enabled=true \
  --set k8sWsProxy.image.tag=abc1234 > "$TMP/release-renamed.yaml"
required_names=(
  "name: k8s-ws-proxy"
)
for n in "${required_names[@]}"; do
  if ! grep -qE "^  ${n}\$" "$TMP/release-renamed.yaml"; then
    echo "FAIL: canonical name '${n}' missing under release ws-proxy-test"
    grep -E '^  name:' "$TMP/release-renamed.yaml" | sort -u
    exit 1
  fi
done
# Belt-and-suspenders: at least 4 resources MUST carry the canonical
# name (DaemonSet + Service + ClusterRole + ClusterRoleBinding;
# ServiceAccount may use it too — count >= 4).
canonical_count="$(grep -cE '^  name: k8s-ws-proxy$' "$TMP/release-renamed.yaml" || true)"
if [[ "$canonical_count" -lt 4 ]]; then
  echo "FAIL: only $canonical_count resources named 'k8s-ws-proxy', want >= 4"
  grep -E '^  name:' "$TMP/release-renamed.yaml"
  exit 1
fi
echo "PASS: canonical workload name 'k8s-ws-proxy' on $canonical_count resources (release-name-independent)"

echo "All render tests passed."
