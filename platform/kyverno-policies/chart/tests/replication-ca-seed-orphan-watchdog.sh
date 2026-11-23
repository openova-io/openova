#!/usr/bin/env bash
# replication-ca-seed-orphan-watchdog.sh - the watchdog CronJob restarts the
# Kyverno cleanup controller when the DeletingPolicy replication-ca-seed-orphan
# has stopped running, and only then (check FND-G12).
#
#   R1 the render holds the CronJob, its ConfigMap script and its args;
#   R2 the rights are exactly get on that one DeletingPolicy and list +
#      delete on Pods in the Kyverno namespace;
#   R3 enabled=false renders none of it;
#   W1-W8 the rendered script, run against a stub kubectl (no cluster), under
#      sh and, when present, busybox sh + busybox date (the image's shell):
#      W1 ran 60 s ago                       -> exit 0, nothing deleted
#      W2 ran 1 h ago, Pods 1 h and 60 s old -> exit 1, only the old Pod
#      W3 never ran, created 1 h ago         -> exit 1, the old Pod
#      W4 never ran, created 60 s ago        -> exit 0, nothing deleted
#      W5 the policy GET fails               -> exit 2, nothing deleted
#      W6 the time does not parse            -> exit 2, nothing deleted
#      W7 stale, the Pod has not started     -> exit 1, nothing deleted
#      W8 stale, the Pod LIST fails          -> exit 2, nothing deleted
#
# Usage:
#   bash replication-ca-seed-orphan-watchdog.sh              the chart above this file
#   bash replication-ca-seed-orphan-watchdog.sh <chart>      that chart directory
#   bash replication-ca-seed-orphan-watchdog.sh --self-test  prove W2 and R2 can fail
#
# Needs helm, yq and date. Exit code: 0 pass. 1 a check failed. 2 wrong usage
# or a render failed.
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
T=templates/cleanup/replication-ca-seed-orphan-watchdog.yaml
NAME=bp-kyverno-policies-replication-ca-seed-watchdog
TAG="[replication-ca-seed-orphan-watchdog]"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

if ! ls "$CHART"/charts/kube-access-lib-*.tgz >/dev/null 2>&1; then
  helm dependency build "$CHART" >/dev/null
fi

WANT_CR='[{"apiGroups":["policies.kyverno.io"],"resources":["deletingpolicies"],"resourceNames":["replication-ca-seed-orphan"],"verbs":["get"]}]'
WANT_ROLE='[{"apiGroups":[""],"resources":["pods"],"verbs":["list","delete"]}]'

# doc <file> <kind>: the one document of that kind, as its own file.
doc() {
  yq "select(.kind == \"$2\")" "$1" > "$TMP/doc-$2.yaml"
  echo "$TMP/doc-$2.yaml"
}

# render <chart>: R1, writes $TMP/watchdog.sh and $TMP/args.
render() {
  local f cj
  helm template "$1" --show-only "$T" > "$TMP/render.yaml" || { echo "$TAG render of $T failed"; return 2; }
  cj="$(doc "$TMP/render.yaml" CronJob)"
  f="$(doc "$TMP/render.yaml" ConfigMap)"
  yq '.data["watchdog.sh"]' "$f" > "$TMP/watchdog.sh"
  if [ "$(yq '.metadata.name' "$cj")" != "$NAME" ] || ! grep -q '^#!/bin/sh$' "$TMP/watchdog.sh"; then
    echo "$TAG FAIL R1: no CronJob $NAME or no script in its ConfigMap"
    return 1
  fi
  if [ "$(yq '.spec.jobTemplate.spec.template.spec.containers[0].command[1]' "$cj")" != "/config/watchdog.sh" ]; then
    echo "$TAG FAIL R1: the CronJob does not run /config/watchdog.sh"
    return 1
  fi
  : > "$TMP/args"
  for i in 0 1 2 3; do
    yq ".spec.jobTemplate.spec.template.spec.containers[0].args[$i]" "$cj" >> "$TMP/args"
  done
  if [ "$(sed -n 1p "$TMP/args")" != "replication-ca-seed-orphan" ]; then
    echo "$TAG FAIL R1: the CronJob does not watch replication-ca-seed-orphan"
    return 1
  fi
  echo "$TAG PASS R1: CronJob $NAME runs the ConfigMap script on '$(yq '.spec.schedule' "$cj")' with args $(tr '\n' ' ' < "$TMP/args")"
}

# rbac: R2, on the R1 render.
rbac() {
  local cr role
  cr="$(yq -o=json -I=0 '.rules' "$(doc "$TMP/render.yaml" ClusterRole)")"
  role="$(yq -o=json -I=0 '.rules' "$(doc "$TMP/render.yaml" Role)")"
  if [ "$cr" != "$WANT_CR" ] || [ "$role" != "$WANT_ROLE" ]; then
    echo "$TAG FAIL R2: ClusterRole rules $cr, Role rules $role; want $WANT_CR and $WANT_ROLE"
    return 1
  fi
  if [ "$(yq '.metadata.namespace' "$TMP/doc-Role.yaml")" != "$(sed -n 2p "$TMP/args")" ]; then
    echo "$TAG FAIL R2: the Role is not in the namespace the script deletes Pods in"
    return 1
  fi
  echo "$TAG PASS R2: get on the one DeletingPolicy, list + delete on Pods in $(sed -n 2p "$TMP/args"), nothing more"
}

# off <chart>: R3.
off() {
  helm template "$1" --set cleanupPolicies.replicationCaSeedOrphan.enabled=false > "$TMP/off.yaml" \
    || { echo "$TAG render with enabled=false failed"; return 2; }
  if grep -q "name: $NAME\$" "$TMP/off.yaml"; then
    echo "$TAG FAIL R3: enabled=false still renders the watchdog"
    return 1
  fi
  echo "$TAG PASS R3: enabled=false renders no watchdog"
}

iso() { date -u -d "@$1" +%Y-%m-%dT%H:%M:%SZ; }

# The stub kubectl answers from $STUB and records every call.
mkdir -p "$TMP/bin"
cat > "$TMP/bin/kubectl" <<'EOF'
#!/bin/sh
echo "$*" >> "$STUB/calls"
case "$1 $2" in
  "get deletingpolicies.policies.kyverno.io")
    [ -f "$STUB/policy" ] || { echo "Error from server (ServiceUnavailable)" >&2; exit 1; }
    cat "$STUB/policy" ;;
  "get pods")
    [ -f "$STUB/pods" ] || { echo "Error from server (ServiceUnavailable)" >&2; exit 1; }
    cat "$STUB/pods" ;;
  "delete pod") echo "$5" >> "$STUB/deleted"; echo "pod \"$5\" deleted" ;;
  *) echo "stub kubectl: unexpected call: $*" >&2; exit 3 ;;
esac
EOF
chmod +x "$TMP/bin/kubectl"

# case_run <shell> <id> <want rc> <want deleted> <policy|-> <pods|->
case_run() {
  local sh="$1" id="$2" want_rc="$3" want_del="$4" rc=0 got_del
  export STUB="$TMP/stub-$id"
  rm -rf "$STUB"; mkdir -p "$STUB"; : > "$STUB/deleted"
  [ "$5" = "-" ] || printf '%s' "$5" > "$STUB/policy"
  [ "$6" = "-" ] || printf '%b' "$6" > "$STUB/pods"
  mapfile -t A < "$TMP/args"
  PATH="$SHPATH" $sh "$TMP/watchdog.sh" "${A[@]}" > "$STUB/out" 2>&1 || rc=$?
  got_del="$(tr '\n' ' ' < "$STUB/deleted")"
  got_del="${got_del% }"
  if [ "$rc" != "$want_rc" ] || [ "$got_del" != "$want_del" ]; then
    sed 's/^/    /' "$STUB/out"
    echo "$TAG FAIL $id ($sh): exit $rc deleted '$got_del', want exit $want_rc deleted '$want_del'"
    return 1
  fi
  if [ -n "$want_del" ] && ! grep -q -- "delete pod -n ${A[1]} " "$STUB/calls"; then
    echo "$TAG FAIL $id ($sh): the delete did not name namespace ${A[1]}"
    return 1
  fi
  echo "$TAG PASS $id ($sh): exit $rc deleted '${got_del:-nothing}'"
}

# behave <shell>: W1-W8.
behave() {
  local sh="$1" now old new rc=0
  now="$(date -u +%s)"; old="$(iso $((now - 3600)))"; new="$(iso $((now - 60)))"
  case_run "$sh" W1 0 ""       "$new|$old" "kc-old|$old\n" || rc=1
  case_run "$sh" W2 1 "kc-old" "$old|$old" "kc-old|$old\nkc-new|$new\n" || rc=1
  case_run "$sh" W3 1 "kc-old" "|$old"     "kc-old|$old\n" || rc=1
  case_run "$sh" W4 0 ""       "|$new"     "kc-old|$old\n" || rc=1
  case_run "$sh" W5 2 ""       "-"         "kc-old|$old\n" || rc=1
  case_run "$sh" W6 2 ""       "yesterday|$old" "kc-old|$old\n" || rc=1
  case_run "$sh" W7 1 ""       "$old|$old" "kc-pending|\n" || rc=1
  case_run "$sh" W8 2 ""       "$old|$old" "-" || rc=1
  return "$rc"
}

# all <chart>: every check against that chart.
all() {
  local rc=0
  render "$1" || return 1
  rbac || rc=1
  off "$1" || rc=1
  SHPATH="$TMP/bin:$PATH"
  behave sh || rc=1
  if command -v busybox >/dev/null 2>&1; then
    mkdir -p "$TMP/bb"
    ln -sf "$(command -v busybox)" "$TMP/bb/date"
    SHPATH="$TMP/bin:$TMP/bb:$PATH"
    behave "busybox sh" || rc=1
  fi
  return "$rc"
}

if [ "$SELF_TEST" -eq 0 ]; then
  all "$CHART"
  exit $?
fi

# Self-test: each mutation must turn the run red.
fails=0
mkdir -p "$TMP/chart"
cp -R "$CHART/." "$TMP/chart/"
cp "$TMP/chart/$T" "$TMP/orig.yaml"
# shellcheck disable=SC2016 # the pattern is the script's literal text
sed -i 's/if \[ "$((now - started_s))" -lt "$STALE_AFTER" \]; then/if [ "$((now - started_s))" -lt 0 ]; then/' "$TMP/chart/$T"
if cmp -s "$TMP/orig.yaml" "$TMP/chart/$T" || all "$TMP/chart" > "$TMP/st1.txt" 2>&1; then
  echo "self-test FAIL: the run passed without the Pod-age guard"; fails=$((fails + 1))
else echo "self-test ok: W2 fails when a young Pod is deleted too"; fi
cp "$TMP/orig.yaml" "$TMP/chart/$T"
sed -i 's/verbs: \["list", "delete"\]/verbs: ["get", "list", "delete"]/' "$TMP/chart/$T"
if cmp -s "$TMP/orig.yaml" "$TMP/chart/$T" || all "$TMP/chart" > "$TMP/st2.txt" 2>&1; then
  echo "self-test FAIL: R2 passed with a wider Pod grant"; fails=$((fails + 1))
else echo "self-test ok: R2 fails when the Pod grant widens"; fi
[ "$fails" -eq 0 ] || exit 1
echo "self-test: 2 cases passed"
