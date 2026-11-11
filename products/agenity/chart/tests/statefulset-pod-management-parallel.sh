#!/usr/bin/env bash
# statefulset-pod-management-parallel.sh — prove a chart upgrade can replace a
# bp-agenity-0 that is NOT Ready, without ever asking the apiserver to change
# an immutable field.
#
# WHY THIS TEST EXISTS
#
#   hw307 acmewalk307, 2026-11-11: HelmRelease bp-agenity "Helm upgrade failed
#   ... context deadline exceeded". The StatefulSet held currentRevision
#   bp-agenity-5db55c5cc8 (2026-09-10) while updateRevision
#   bp-agenity-6688c94b8c (the 0.5.34 upgrade, 2026-11-06T05:19:10Z) was never
#   rolled out. The pod was Init:0/1 (seed-claude-creds exits 1 while the
#   Anthropic credential is absent, #6163), and under the default
#   podManagementPolicy OrderedReady the StatefulSet controller never replaces
#   a pod that is not Running+Ready (stateful_set_control.go processReplica:
#   `!isRunningAndReady(pod) && monotonic` returns before the rolling-update
#   loop). So no chart fix could reach the broken pod, and helm --wait burned
#   its 15m on currentRevision != updateRevision.
#
#   The chart now renders podManagementPolicy Parallel for a NEW StatefulSet
#   and renders an EXISTING StatefulSet's live value back unchanged, because
#   the field is immutable and a change would 422 every existing upgrade.
#
# CASES
#   1  fresh render (lookup empty, as on a fresh install): Parallel, replicas 1.
#   2  live StatefulSet with OrderedReady: OrderedReady (no immutable change).
#   3  live StatefulSet with Parallel: Parallel.
#   4  live StatefulSet object without the field: OrderedReady (the apiserver
#      default it was created with).
#   5  values opt-out podManagementPolicy=OrderedReady on a fresh render.
#   6  an invalid value fails the render.
#   7  VACUITY: the pre-fix template (no podManagementPolicy line) must FAIL
#      case 1's predicate, so case 1 cannot pass on a broken chart.
#
# Cases 2-4 stub `lookup` by replacing the template's exact lookup expression
# in a COPY of the chart with a literal live object; the substitution must hit
# exactly once, so a reworded lookup fails here instead of silently testing
# nothing.
#
# Runs under the release/PR toolchain (helm + mikefarah yq). Renders only
# templates/statefulset.yaml (one document).
set -euo pipefail

chart_dir="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
helm="${HELM_BIN:-helm}"
yq="${YQ_BIN:-yq}"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fail=0
ok()  { printf 'PASS  %s\n' "$*"; }
bad() { printf 'FAIL  %s\n' "$*"; fail=1; }

render() { # render <chart> <extra helm args...> -> stdout
  local c="$1"; shift
  "$helm" template bp-agenity "$c" -n acmewalk307 \
    --api-versions "cilium.io/v2" \
    --api-versions "external-secrets.io/v1beta1" \
    --show-only templates/statefulset.yaml \
    "$@"
}

# effective <file> — the podManagementPolicy the apiserver would store: the
# rendered value, or OrderedReady (the apiserver default) when absent.
effective() {
  local v
  v="$("$yq" '.spec.podManagementPolicy // ""' "$1")"
  if [ -z "$v" ]; then v="OrderedReady"; fi
  printf '%s' "$v"
}

expect() { # expect <label> <got> <want>
  if [ "$2" = "$3" ]; then ok "$1 — $3"; else bad "$1 — expected '$3', got '$2'"; fi
}

LOOKUP='(lookup "apps/v1" "StatefulSet" .Release.Namespace (include "agenity.fullname" .))'

# with_live <name> <dict-literal> — writes $WORK/<name>, a chart copy whose
# lookup returns <dict>. Returns non-zero if the anchor is not there exactly
# once (the stub would then test the unmodified chart, i.e. nothing).
with_live() {
  local dst="$WORK/$1"
  cp -R "$chart_dir" "$dst"
  python3 - "$dst/templates/statefulset.yaml" "$LOOKUP" "$2" <<'PY'
import sys
p, anchor, stub = sys.argv[1:4]
s = open(p).read()
n = s.count(anchor)
if n != 1:
    sys.exit("lookup anchor found %d times (want exactly 1)" % n)
open(p, "w").write(s.replace(anchor, stub))
PY
}

# live_case <name> <dict-literal> <label> <want> [extra helm args...]
live_case() {
  local name="$1" stub="$2" label="$3" want="$4"; shift 4
  if ! with_live "$name" "$stub"; then
    bad "$label — the template has no StatefulSet lookup to stub, so it cannot read the live value"
    return 0
  fi
  render "$WORK/$name" "$@" > "$WORK/$name.yaml"
  expect "$label" "$(effective "$WORK/$name.yaml")" "$want"
}

# ── 1. fresh install: Parallel, one replica ─────────────────────────────────
F1="$WORK/fresh.yaml"
render "$chart_dir" > "$F1"
if [ "$("$yq" '.kind' "$F1")" != "StatefulSet" ]; then
  bad "templates/statefulset.yaml did not render a StatefulSet — nothing below can be asserted"
  exit 1
fi
expect "fresh install renders Parallel" "$(effective "$F1")" "Parallel"
expect "the StatefulSet is a singleton (Parallel loses no ordering)" "$("$yq" '.spec.replicas' "$F1")" "1"

# ── 2-4. an existing StatefulSet keeps its live, immutable value ───────────
ORDERED='(dict "spec" (dict "podManagementPolicy" "OrderedReady"))'
live_case live-ordered "$ORDERED" \
  "live OrderedReady StatefulSet is rendered back as OrderedReady (no 422)" "OrderedReady"
live_case live-parallel '(dict "spec" (dict "podManagementPolicy" "Parallel"))' \
  "live Parallel StatefulSet stays Parallel" "Parallel"
# Asserted on the RENDERED field, not effective(): an absent field would read
# OrderedReady through the apiserver default and pass vacuously.
if with_live live-nofield '(dict "spec" (dict "replicas" 1))'; then
  render "$WORK/live-nofield" > "$WORK/live-nofield.yaml"
  expect "live StatefulSet without the field renders the apiserver default OrderedReady" \
    "$("$yq" '.spec.podManagementPolicy' "$WORK/live-nofield.yaml")" "OrderedReady"
else
  bad "live StatefulSet without the field — the template has no StatefulSet lookup to stub"
fi
# Values never override an immutable live field.
live_case live-ordered-explicit "$ORDERED" \
  "live OrderedReady wins over an explicit values Parallel" "OrderedReady" --set podManagementPolicy=Parallel

# ── 5. opt-out on a fresh install ───────────────────────────────────────────
F5="$WORK/optout.yaml"; render "$chart_dir" --set podManagementPolicy=OrderedReady > "$F5"
expect "values podManagementPolicy=OrderedReady is honoured on a fresh install" "$(effective "$F5")" "OrderedReady"

# ── 6. an invalid value fails the render ────────────────────────────────────
if err="$(render "$chart_dir" --set podManagementPolicy=Sequential 2>&1)"; then
  bad "podManagementPolicy=Sequential rendered instead of failing"
elif printf '%s' "$err" | grep -qF 'podManagementPolicy must be Parallel or OrderedReady'; then
  ok "an invalid podManagementPolicy fails the render with the named error"
else
  bad "podManagementPolicy=Sequential failed for an unexpected reason: $err"
fi

# ── 7. VACUITY: the pre-fix template must fail case 1 ───────────────────────
MUT="$WORK/prefix"
cp -R "$chart_dir" "$MUT"
if python3 - "$MUT/templates/statefulset.yaml" <<'PY'
import re, sys
p = sys.argv[1]
s = open(p).read()
s2, n = re.subn(r'(?m)^  podManagementPolicy: .*\n', '', s)
if n != 1:
    sys.exit("podManagementPolicy line found %d times (want exactly 1)" % n)
open(p, "w").write(s2)
PY
then
  F7="$WORK/prefix.yaml"; render "$MUT" > "$F7"
  if [ "$(effective "$F7")" = "Parallel" ]; then
    bad "vacuity: the pre-fix template also reads Parallel — case 1 proves nothing"
  else
    ok "vacuity: without the fix the StatefulSet is OrderedReady — case 1 is a real check"
  fi
else
  bad "vacuity: the template has no single podManagementPolicy line to strip"
fi

echo
if [ "$fail" -ne 0 ]; then
  echo "FAIL — bp-agenity's StatefulSet can wedge on an unhealthy pod, or would ask the apiserver to change an immutable field."
  exit 1
fi
echo "PASS — a new bp-agenity StatefulSet is Parallel (an upgrade can replace a pod that is not Ready); an existing one keeps its live podManagementPolicy."
