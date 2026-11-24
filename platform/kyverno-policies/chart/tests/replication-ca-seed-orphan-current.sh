#!/usr/bin/env bash
# replication-ca-seed-orphan-current.sh - the suite replication-ca-seed-orphan
# judges the DeletingPolicy as it is, and the cleanup controller may do what
# the policy asks (check FND-G12).
#
#   C1 tests/replication-ca-seed-orphan/policy.yaml equals the render of
#      templates/cleanup/replication-ca-seed-orphan.yaml;
#   C2 the render of templates/rbac-cleanup-replication-ca-seed.yaml is a
#      ClusterRole aggregated to the cleanup controller that grants exactly
#      list + delete on Secrets and list on HelmReleases and Certificates;
#   C3 the schedule runs at least every 2 minutes (the check waits 10);
#   C4 enabled=false renders neither the policy nor the ClusterRole.
#
# Usage:
#   bash replication-ca-seed-orphan-current.sh              the chart above this file
#   bash replication-ca-seed-orphan-current.sh <chart>      that chart directory
#                                                           (Chart tests (PR) passes it)
#   bash replication-ca-seed-orphan-current.sh --self-test  prove C1, C2 and C4 can fail
#
# Needs helm and yq. Exit code: 0 pass. 1 a check failed. 2 wrong usage or a
# render failed.
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
POLICY_T=templates/cleanup/replication-ca-seed-orphan.yaml
RBAC_T=templates/rbac-cleanup-replication-ca-seed.yaml
TAG="[replication-ca-seed-orphan-current]"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# The chart includes the library kube-access-lib (file://); a render needs it.
if ! ls "$CHART"/charts/kube-access-lib-*.tgz >/dev/null 2>&1; then
  helm dependency build "$CHART" >/dev/null
fi

WANT_RULES='[{"apiGroups":[""],"resources":["secrets"],"verbs":["list","delete"]},{"apiGroups":["helm.toolkit.fluxcd.io"],"resources":["helmreleases"],"verbs":["list"]},{"apiGroups":["cert-manager.io"],"resources":["certificates"],"verbs":["list"]}]'

# check_copy <chart> <suite dir>: C1.
check_copy() {
  helm template "$1" --show-only "$POLICY_T" | grep -v '^# Source:' | sed '1{/^---$/d}' > "$TMP/policy.yaml" \
    || { echo "$TAG render of $POLICY_T failed"; return 2; }
  if [ "$(yq '.kind' "$TMP/policy.yaml")" != "DeletingPolicy" ]; then
    echo "$TAG FAIL C1: the render is not a DeletingPolicy"
    return 1
  fi
  if ! diff -u "$2/policy.yaml" "$TMP/policy.yaml" > "$TMP/diff.txt"; then
    cat "$TMP/diff.txt"
    echo "$TAG FAIL C1: $2/policy.yaml is not the current render of $POLICY_T. Re-render it (see kyverno-test.yaml)."
    return 1
  fi
  echo "$TAG PASS C1: policy.yaml is the current render"
}

# check_rbac <chart>: C2.
check_rbac() {
  local agg rules
  helm template "$1" --show-only "$RBAC_T" > "$TMP/rbac.yaml" || { echo "$TAG render of $RBAC_T failed"; return 2; }
  agg="$(yq '.kind + " " + .metadata.labels["rbac.kyverno.io/aggregate-to-cleanup-controller"]' "$TMP/rbac.yaml")"
  rules="$(yq -o=json -I=0 '.rules' "$TMP/rbac.yaml")"
  if [ "$agg" != "ClusterRole true" ]; then
    echo "$TAG FAIL C2: want a ClusterRole labelled rbac.kyverno.io/aggregate-to-cleanup-controller=true, got '$agg'"
    return 1
  fi
  if [ "$rules" != "$WANT_RULES" ]; then
    echo "$TAG FAIL C2: rules are $rules, want exactly $WANT_RULES"
    return 1
  fi
  echo "$TAG PASS C2: the cleanup controller gets list + delete on Secrets and list on HelmReleases and Certificates, nothing more"
}

# check_schedule: C3, on the C1 render.
check_schedule() {
  local s
  s="$(yq '.spec.schedule' "$TMP/policy.yaml")"
  case "$s" in
    "* * * * *"|"*/1 * * * *"|"*/2 * * * *") echo "$TAG PASS C3: schedule '$s'" ;;
    *) echo "$TAG FAIL C3: schedule '$s' is not every 2 minutes or less"; return 1 ;;
  esac
}

# check_off <chart>: C4.
check_off() {
  helm template "$1" --set cleanupPolicies.replicationCaSeedOrphan.enabled=false > "$TMP/off.yaml" \
    || { echo "$TAG render with enabled=false failed"; return 2; }
  if grep -q -e 'name: replication-ca-seed-orphan$' -e 'name: bp-kyverno-policies-cleanup-replication-ca-seed$' "$TMP/off.yaml"; then
    echo "$TAG FAIL C4: enabled=false still renders the policy or its ClusterRole"
    return 1
  fi
  echo "$TAG PASS C4: enabled=false renders neither"
}

if [ "$SELF_TEST" -eq 0 ]; then
  rc=0
  check_copy "$CHART" "$CHART/tests/replication-ca-seed-orphan" || rc=1
  check_rbac "$CHART" || rc=1
  check_schedule || rc=1
  check_off "$CHART" || rc=1
  exit "$rc"
fi

# Self-test: each check must go red on a copy with one change.
fails=0
mkdir -p "$TMP/chart"
cp -R "$CHART/." "$TMP/chart/"
sed -i 's/variables.releases.all(hr,/variables.releases.exists(hr,/' "$TMP/chart/$POLICY_T"
if check_copy "$TMP/chart" "$CHART/tests/replication-ca-seed-orphan" > "$TMP/st1.txt" 2>&1; then
  echo "self-test FAIL: C1 passed with a changed template"; fails=$((fails + 1))
else echo "self-test ok: C1 fails when the template changes and the copy does not"; fi
sed -i 's/verbs: \["list", "delete"\]/verbs: ["get", "list", "delete"]/' "$TMP/chart/$RBAC_T"
if check_rbac "$TMP/chart" > "$TMP/st2.txt" 2>&1; then
  echo "self-test FAIL: C2 passed with a wider Secret grant"; fails=$((fails + 1))
else echo "self-test ok: C2 fails when the Secret grant widens"; fi
sed -i 's/^{{- if .Values.cleanupPolicies.replicationCaSeedOrphan.enabled }}$/{{- if true }}/' "$TMP/chart/$RBAC_T"
if check_off "$TMP/chart" > "$TMP/st3.txt" 2>&1; then
  echo "self-test FAIL: C4 passed with an ungated ClusterRole"; fails=$((fails + 1))
else echo "self-test ok: C4 fails when the ClusterRole ignores enabled"; fi
[ "$fails" -eq 0 ] || exit 1
echo "self-test: 3 cases passed"
