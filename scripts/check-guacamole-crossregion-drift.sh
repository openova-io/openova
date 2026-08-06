#!/usr/bin/env bash
# check-guacamole-crossregion-drift.sh — distinguish the KNOWN #5358 Guacamole
# cross-region session-split defect (blocked on a stale live chart pin) from a
# genuinely NEW Guacamole defect (#5750, filed off UAT row 35 / #3374).
#
# WHY THIS EXISTS
#
# platform/guacamole/chart already ships the complete, correct fix for the
# "generic ERROR page after a valid SSO session" defect: the crossRegion
# singleton primitive (Chart.yaml 0.2.36, values.yaml:79-97, #5358). It is
# proven correct on `main` (chart/tests/crossregion-render.sh: 5/5 PASS).
#
# But a Sovereign that cut over to full self-sovereignty BEFORE 0.2.36 shipped
# can be stuck running the pre-fix chart indefinitely — the local Gitea mirror
# is intentionally frozen post-cutover (mirrorResync.enabled=false, Principle
# #14 severance) and #5640's Day-2 CATALOG leg (the mechanism built to close
# that gap) can itself be undeliverable to a Sovereign old enough to predate
# it — a version-skew catch-22, measured live on hw292 (dep
# 1c56518035a83e03, 2026-08-06):
#
#   kubectl get hr bp-guacamole -n flux-system -o jsonpath='{.spec.chart.spec.version}'
#     -> 0.2.33 in BOTH region-A and region-B (main is 0.2.37)
#   kubectl get pods -n guacamole
#     -> BOTH regions independently run guacamole-server + guacd +
#        guacamole-pg-1 + admin-enroll (the pre-0.2.36 split-brain topology)
#   kubectl logs guacamole-server -n guacamole --since=6h
#     -> region-A: `User "emrah.baysal@openova.io" successfully authenticated`
#        at 06:08:14; region-B: the SAME principal, independently, at
#        06:08:05 — two per-Pod HashTokenSessionMaps minted two unrelated
#        tokens for ONE browser page load, which is exactly the mechanism
#        that renders the generic "An error has occurred" banner (UAT row 35).
#
# Without this script, telling "known, already-fixed, blocked-on-delivery"
# apart from "new defect, needs fresh root-cause" took hours of manual
# kubectl archaeology (this session). This script makes that call in seconds,
# from data any operator already has (kubeconfig read access).
#
# VERDICTS
#   CLEAN          — at most one region is actively serving Guacamole. Either
#                    single-region, or crossRegion is correctly suppressing
#                    the secondary's workload set.
#   KNOWN-DRIFT     — more than one region is actively serving AND every
#                    serving region's chart version is below the minimum
#                    version that carries the crossRegion fix. This IS #5358,
#                    blocked on live chart delivery — do not re-investigate
#                    Guacamole itself; the fix is `Refs #5640` chart delivery.
#   UNEXPECTED-NEW  — more than one region is actively serving AND every
#                    serving region's chart version ALREADY carries the fix.
#                    The known remediation (chart delivery) would not explain
#                    this — treat it as a genuinely new defect.
#
# Usage:
#   scripts/check-guacamole-crossregion-drift.sh --self-test
#   scripts/check-guacamole-crossregion-drift.sh --kubeconfig <region-a> [--kubeconfig <region-b> ...]
#   scripts/check-guacamole-crossregion-drift.sh --kubeconfig <a> --kubeconfig <b> --namespace guacamole
#
# Exit: 0 = CLEAN or self-test passed, 1 = KNOWN-DRIFT or UNEXPECTED-NEW
#       detected live, 2 = setup / usage error.
#
# Read-only: every cluster call is `kubectl get`. No mutation, ever.

set -euo pipefail

NAMESPACE="guacamole"
RELEASE_NAMESPACE="flux-system"
MIN_SAFE_VERSION="0.2.36"
SELF_TEST=0
KUBECONFIGS=()

while [ $# -gt 0 ]; do
  case "$1" in
    --kubeconfig)   KUBECONFIGS+=("$2"); shift 2 ;;
    --namespace)    NAMESPACE="$2"; shift 2 ;;
    --min-version)  MIN_SAFE_VERSION="$2"; shift 2 ;;
    --self-test|--selftest) SELF_TEST=1; shift ;;
    *) echo "Unknown arg: $1" >&2
       echo "Usage: $0 [--kubeconfig <path> ...] [--namespace <ns>] [--min-version <semver>] [--self-test]" >&2
       exit 2 ;;
  esac
done

# ─── the detector, shared by --self-test and the live sweep ────────────────
#
# Input: TSV lines "chart_version<TAB>ready(0|1)", one per region (chart
# version empty means the HelmRelease could not be read for that region).
# Output on stdout: one of CLEAN / KNOWN-DRIFT / UNEXPECTED-NEW, plus the
# evidence lines. Kept as a function so --self-test exercises the EXACT same
# code the live sweep uses (a classifier tested only through a copy of itself
# is not tested).
classify_drift() {
  python3 -c '
import sys

def ver_tuple(v):
    v = (v or "").strip()
    if not v:
        return None
    parts = v.split(".")
    out = []
    for p in parts:
        try:
            out.append(int(p))
        except ValueError:
            return None
    return tuple(out)

min_safe = ver_tuple(sys.argv[1])
rows = []
for line in sys.stdin:
    line = line.rstrip("\n")
    if not line:
        continue
    ver, ready = line.split("\t")
    rows.append((ver, ready == "1"))

serving = [r for r in rows if r[1]]

if len(serving) < 2:
    print("VERDICT\tCLEAN")
    print("EVIDENCE\tregions actively serving Guacamole: %d (need >=2 for the split-brain shape)" % len(serving))
    sys.exit(0)

# Every SERVING regions chart version, parsed. An unparsable/missing version
# on a serving region is NOT treated as "safe" -- unknown must fail closed,
# same discipline as check-live-nodeports.sh age handling.
serving_versions = [ver_tuple(r[0]) for r in serving]
all_below_min = all(
    (v is None) or (min_safe is not None and v < min_safe)
    for v in serving_versions
)

if all_below_min:
    print("VERDICT\tKNOWN-DRIFT")
else:
    print("VERDICT\tUNEXPECTED-NEW")
print("EVIDENCE\tregions actively serving Guacamole: %d; chart versions: %s" % (
    len(serving), ",".join(v or "?" for v, _ in serving)))
' "${MIN_SAFE_VERSION}"
}

verdict_of() { grep '^VERDICT' | cut -f2; }

# ─── Phase 0 — detector self-test (vacuity guard) ───────────────────────────
#
# Seven fixtures, each isolating one axis a broken detector could get wrong:
#   pre-fix   (hw292-shaped)   -> MUST be KNOWN-DRIFT
#   post-fix  (delivered)      -> MUST be CLEAN (secondary correctly suppressed)
#   single-region              -> MUST be CLEAN (nothing to split)
#   new-defect (both serving, both already >= fix version) -> MUST be
#     UNEXPECTED-NEW, never silently folded into CLEAN or mis-blamed on drift
st_fail() { echo "SELF-TEST FAIL: $1" >&2; exit 2; }

PRE_FIX=$'0.2.33\t1\n0.2.33\t1'
V="$(printf '%s\n' "${PRE_FIX}" | classify_drift | verdict_of)"
[ "${V}" = "KNOWN-DRIFT" ] || st_fail "pre-fix fixture (both regions 0.2.33, both serving) verdict was '${V}', expected KNOWN-DRIFT — this IS the live hw292 shape"

POST_FIX=$'0.2.37\t1\n0.2.37\t0'
V="$(printf '%s\n' "${POST_FIX}" | classify_drift | verdict_of)"
[ "${V}" = "CLEAN" ] || st_fail "post-fix fixture (0.2.37, secondary correctly suppressed) verdict was '${V}', expected CLEAN"

SINGLE=$'0.2.33\t1'
V="$(printf '%s\n' "${SINGLE}" | classify_drift | verdict_of)"
[ "${V}" = "CLEAN" ] || st_fail "single-region fixture verdict was '${V}', expected CLEAN — nothing to split"

NEW_DEFECT=$'0.2.37\t1\n0.2.37\t1'
V="$(printf '%s\n' "${NEW_DEFECT}" | classify_drift | verdict_of)"
[ "${V}" = "UNEXPECTED-NEW" ] || st_fail "new-defect fixture (both regions ALREADY >= ${MIN_SAFE_VERSION}, both still serving) verdict was '${V}', expected UNEXPECTED-NEW — blaming chart drift here would send the next walker on a wild-goose chase for a delivery gap that isn't the cause"

# Mixed-version edge case: one region caught up, one didn't, both still
# serving (a mid-rollout snapshot). Not fully below min -> must NOT be called
# KNOWN-DRIFT (the known remediation, "wait for chart delivery", is already
# half-applied and won't fully explain two servers by itself); the honest
# call is UNEXPECTED-NEW so a human looks at the partial rollout directly.
MIXED=$'0.2.33\t1\n0.2.37\t1'
V="$(printf '%s\n' "${MIXED}" | classify_drift | verdict_of)"
[ "${V}" = "UNEXPECTED-NEW" ] || st_fail "mixed-version fixture verdict was '${V}', expected UNEXPECTED-NEW"

# Unreadable version on a SERVING region. classify_drift's own comment claims
# "unknown must fail closed", but until #5750 nothing exercised it: deleting
# the `(v is None) or` clause left all five fixtures above green, because none
# of them ever fed the detector a version it could not parse.
#
# This is not a hypothetical input. `kubectl get hr bp-guacamole -n flux-system
# -o jsonpath='{.spec.chart.spec.version}'` prints an EMPTY string whenever the
# HelmRelease is absent or the field unset — precisely the state a half-reverted
# cutover leaves behind (#5759). Fail-open there reports CLEAN on a cluster that
# is genuinely split, which is the one answer this script exists to prevent.
UNKNOWN_BOTH=$'\t1\n\t1'
V="$(printf '%s\n' "${UNKNOWN_BOTH}" | classify_drift | verdict_of)"
[ "${V}" = "KNOWN-DRIFT" ] || st_fail "unreadable-version fixture (both regions serving, neither version parsable) verdict was '${V}', expected KNOWN-DRIFT — a version that cannot be read cannot be proven >= ${MIN_SAFE_VERSION}, so it must fail closed, never CLEAN"

# One region readably below min, one unreadable. The unknown must not RESCUE
# the pair out of KNOWN-DRIFT: `all_below_min` is an AND, so treating unknown
# as safe silently downgrades a real drift to UNEXPECTED-NEW and sends the
# walker hunting a new defect that does not exist.
UNKNOWN_MIXED=$'0.2.33\t1\n\t1'
V="$(printf '%s\n' "${UNKNOWN_MIXED}" | classify_drift | verdict_of)"
[ "${V}" = "KNOWN-DRIFT" ] || st_fail "one-old-one-unreadable fixture verdict was '${V}', expected KNOWN-DRIFT — an unparsable version must not rescue a known-drifted pair"

echo "OK — detector self-test passed (7 fixtures: hw292-shaped pre-fix caught as"
echo "   KNOWN-DRIFT, delivered-fix state reads CLEAN, single-region reads CLEAN,"
echo "   an already-patched-but-still-splitting shape is never mis-blamed on"
echo "   drift, a mixed-version snapshot is not folded into either verdict, and"
echo "   an unreadable chart version fails CLOSED rather than reading CLEAN)."

if [ "${SELF_TEST}" -eq 1 ]; then
  echo "OK: --self-test only; no cluster was contacted."
  exit 0
fi

# ─── Phase 1 — live sweep ────────────────────────────────────────────────
if [ "${#KUBECONFIGS[@]}" -eq 0 ]; then
  echo "Usage: $0 --kubeconfig <region-a> [--kubeconfig <region-b> ...]" >&2
  echo "       (or --self-test to run the fixture-only detector proof)" >&2
  exit 2
fi

if ! command -v kubectl >/dev/null 2>&1; then
  echo "WARN: kubectl not on PATH — cannot sweep a live cluster." >&2
  echo "      This run proves the DETECTOR only, not any cluster." >&2
  exit 0
fi

ROWS=""
echo ""
echo "== bp-guacamole per-region read (namespace=${NAMESPACE}) =="
i=0
for kc in "${KUBECONFIGS[@]}"; do
  i=$((i + 1))
  # APPLIED, not DESIRED. `.spec.chart.spec.version` is what the HelmRelease
  # WANTS; `.status.history[0].chartVersion` is what Helm last actually
  # installed. On hw292 (2026-08-06) region-a read spec=0.2.37 / applied=0.2.33
  # with Ready=False "dependency 'flux-system/bp-cilium' is not ready" — the
  # upgrade was pending, never applied. Reading spec made this script report
  # UNEXPECTED-NEW ("every serving region already carries the fix") on an
  # estate where BOTH regions were in fact running 0.2.33 — a textbook
  # KNOWN-DRIFT it sent the walker away from. A desired version is not
  # evidence of what is running (#5750).
  ver="$(kubectl --kubeconfig "${kc}" get hr bp-guacamole -n "${RELEASE_NAMESPACE}" \
           -o jsonpath='{.status.history[0].chartVersion}' 2>/dev/null || true)"
  # Fall back to the desired pin ONLY when there is no install history at all
  # (never installed). Labelled, because it is a weaker reading.
  ver_src="applied"
  if [ -z "${ver}" ]; then
    ver="$(kubectl --kubeconfig "${kc}" get hr bp-guacamole -n "${RELEASE_NAMESPACE}" \
             -o jsonpath='{.spec.chart.spec.version}' 2>/dev/null || true)"
    [ -n "${ver}" ] && ver_src="desired-no-history"
  fi
  ready="$(kubectl --kubeconfig "${kc}" get deploy guacamole-server -n "${NAMESPACE}" \
             -o jsonpath='{.status.readyReplicas}' 2>/dev/null || true)"
  ready_bit=0
  case "${ready}" in
    ''|0) ready_bit=0 ;;
    *)    ready_bit=1 ;;
  esac
  echo "  region ${i} (${kc}): chart=${ver:-<unknown>} (${ver_src}) guacamole-server ready=${ready_bit}"
  ROWS="${ROWS}${ver}"$'\t'"${ready_bit}"$'\n'
done

RESULT="$(printf '%s' "${ROWS}" | classify_drift)"
VERDICT="$(printf '%s' "${RESULT}" | verdict_of)"
EVIDENCE="$(printf '%s' "${RESULT}" | grep '^EVIDENCE' | cut -f2-)"

echo ""
case "${VERDICT}" in
  CLEAN)
    echo "OK: CLEAN — ${EVIDENCE}."
    exit 0
    ;;
  KNOWN-DRIFT)
    echo "───────────────────────────────────────────────────────────────" >&2
    echo "KNOWN-DRIFT: ${EVIDENCE}." >&2
    echo "This is the #5358 cross-region session-split defect (per-Pod" >&2
    echo "in-memory HashTokenSessionMap — a browser's parallel connections" >&2
    echo "mint a token on one region's Pod and a later REST call lands on" >&2
    echo "the other, which 403s it; the SPA renders the generic 'An error" >&2
    echo "has occurred' page). The chart-side fix (crossRegion singleton," >&2
    echo "platform/guacamole/chart 0.2.36+) is ALREADY merged and correct" >&2
    echo "-- do not re-investigate Guacamole. This Sovereign simply hasn't" >&2
    echo "received the chart bump yet; see #5640 (Day-2 delivery)." >&2
    echo "───────────────────────────────────────────────────────────────" >&2
    exit 1
    ;;
  UNEXPECTED-NEW)
    echo "───────────────────────────────────────────────────────────────" >&2
    echo "UNEXPECTED-NEW: ${EVIDENCE}." >&2
    echo "More than one region is serving Guacamole even though every" >&2
    echo "serving region already carries the crossRegion fix (>= ${MIN_SAFE_VERSION})." >&2
    echo "The known #5358 remediation (wait for / trigger chart delivery)" >&2
    echo "does NOT explain this. Treat as a genuinely new defect: check" >&2
    echo "crossRegion.enabled / crossRegion.role on the live HelmRelease" >&2
    echo "values (kubectl get hr bp-guacamole -o jsonpath='{.spec.values.crossRegion}')" >&2
    echo "in every region before assuming this is #5358 again." >&2
    echo "───────────────────────────────────────────────────────────────" >&2
    exit 1
    ;;
esac
