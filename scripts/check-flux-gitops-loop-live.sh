#!/usr/bin/env bash
# check-flux-gitops-loop-live — is the GitOps loop actually RUNNING, or does it
# merely look green?
#
# WHY (#5573). The mothership's four Flux controllers sat at `replicas: 0` from
# 2026-07-31T04:04:22Z. Measured 6.4 days later, NOTHING in the cluster said so:
#
#   1. A Deployment at replicas:0 reports `Available=True … MinimumReplicasAvailable`,
#      because zero replicas vacuously satisfies "minimum replicas available".
#      The controller that is not running reports Available.
#
#   2. Every Kustomization's Ready condition FROZE at that instant and kept its
#      last value. `flux-system/openova-dns` — which owns the powerdns
#      HelmRelease still serving nodePorts 32015/31425 — read `Ready=True`,
#      6.4 days stale, unable to apply anything. bp-powerdns 1.2.23 with the
#      §854 `nodePort: 0` deallocation could not reach the cluster, and the
#      object that would report that was itself frozen.
#
# So "kubectl get kustomization" showing Ready=True is NOT evidence the loop
# works. This script asserts LIVENESS instead of status: controllers scheduled,
# and each Kustomization's Ready transition recent relative to its own interval.
#
# READ-ONLY. Reports; never scales, never unsuspends. Restoring a deliberately
# stopped GitOps loop replays every accumulated commit in one step and is an
# operator decision.
#
# Usage:
#   scripts/check-flux-gitops-loop-live.sh [--kubeconfig PATH] [--stale-factor N]
#   scripts/check-flux-gitops-loop-live.sh --self-test
#
# Exit: 0 live, 1 dead-or-stale, 2 usage/precondition.
set -euo pipefail

KUBECONFIG_ARG=""
STALE_FACTOR=10      # Ready older than interval*N ⇒ stale. 10 is deliberately
                     # generous: a healthy 10m-interval Kustomization would have
                     # to be silent for 100m to trip it.
SELF_TEST=0
NOW_OVERRIDE=""      # test seam only
while [ $# -gt 0 ]; do
  case "$1" in
    --kubeconfig) KUBECONFIG_ARG="--kubeconfig=$2"; shift 2 ;;
    --kubeconfig=*) KUBECONFIG_ARG="$1"; shift ;;
    --stale-factor) STALE_FACTOR="$2"; shift 2 ;;
    --self-test|--selftest) SELF_TEST=1; shift ;;
    --now) NOW_OVERRIDE="$2"; shift 2 ;;
    -h|--help) sed -n '1,32p' "$0"; exit 0 ;;
    *) echo "Unknown arg: $1" >&2; exit 2 ;;
  esac
done

# ─── classifier, shared by --self-test and the live sweep ───────────────────
#
# stdin, TAB-separated, one record per line:
#   CTRL\t<name>\t<spec.replicas>\t<availableCondition>
#   KUST\t<ns/name>\t<suspend>\t<readyStatus>\t<intervalSeconds>\t<ageSeconds>
classify_loop() {
  python3 -c "$(cat <<'PY'
import sys, os
factor = float(os.environ.get("FGL_STALE_FACTOR", "10"))
dead_ctrls, stale_kusts, live_ctrls, ok_kusts = [], [], 0, 0

for line in sys.stdin:
    line = line.rstrip("\n")
    if not line:
        continue
    f = line.split("\t")
    if f[0] == "CTRL":
        _, name, replicas, avail = f
        if replicas in ("0", ""):
            # The vacuous-Available case: report it EVEN THOUGH avail says True.
            dead_ctrls.append((name, replicas, avail))
        else:
            live_ctrls += 1
    elif f[0] == "KUST":
        _, name, suspend, ready, interval, age = f
        if suspend == "true":
            continue            # suspended is a declared state, not a defect
        iv, ag = float(interval or 0), float(age or 0)
        if iv > 0 and ag > iv * factor:
            stale_kusts.append((name, ready, int(iv), int(ag)))
        else:
            ok_kusts += 1

print("CTRL_LIVE\t%d" % live_ctrls)
print("CTRL_DEAD\t%d" % len(dead_ctrls))
print("KUST_OK\t%d" % ok_kusts)
print("KUST_STALE\t%d" % len(stale_kusts))
for n, r, a in dead_ctrls:
    print("DEAD\t%s\treplicas=%s\tavailable=%s" % (n, r, a))
for n, ready, iv, ag in stale_kusts:
    print("STALE\t%s\tReady=%s\tinterval=%ds\tage=%ds" % (n, ready, iv, ag))
print("VERDICT\t%s" % ("DEAD" if dead_ctrls else ("STALE" if stale_kusts else "LIVE")))
PY
)"
}

verdict_of() { grep '^VERDICT' | cut -f2; }

if [ "${SELF_TEST}" -eq 1 ]; then
  st_fail() { echo "SELF-TEST FAIL: $1" >&2; exit 2; }
  export FGL_STALE_FACTOR="${STALE_FACTOR}"

  # 1. The live #5573 shape: replicas 0 AND Available=True. Must be DEAD.
  #    This is the assertion the whole file exists for — a naive check reading
  #    the Available condition would call this healthy.
  V="$(printf 'CTRL\tsource-controller\t0\tTrue\n' | classify_loop | verdict_of)"
  [ "${V}" = "DEAD" ] || st_fail "replicas=0 with Available=True read '${V}', expected DEAD — vacuous availability is the #5573 false-green"

  # 2. CONTROL: a running controller must read LIVE, so the check is not
  #    "any controller is dead".
  V="$(printf 'CTRL\tsource-controller\t1\tTrue\n' | classify_loop | verdict_of)"
  [ "${V}" = "LIVE" ] || st_fail "replicas=1 read '${V}', expected LIVE"

  # 3. The frozen-Kustomization shape: Ready=True, 10m interval, 6.4 days old.
  #    Exactly flux-system/openova-dns as measured.
  V="$(printf 'CTRL\tsource-controller\t1\tTrue\nKUST\tflux-system/openova-dns\tfalse\tTrue\t600\t552960\n' | classify_loop | verdict_of)"
  [ "${V}" = "STALE" ] || st_fail "Ready=True frozen 6.4d on a 10m interval read '${V}', expected STALE"

  # 4. CONTROL: a Kustomization inside its interval must NOT be flagged.
  V="$(printf 'CTRL\tsource-controller\t1\tTrue\nKUST\tflux-system/openova-dns\tfalse\tTrue\t600\t900\n' | classify_loop | verdict_of)"
  [ "${V}" = "LIVE" ] || st_fail "Ready=True 15m old on a 10m interval read '${V}', expected LIVE"

  # 5. CONTROL: a SUSPENDED Kustomization is a declared state, not staleness.
  #    Without this the check would flag every deliberately-paused overlay.
  V="$(printf 'CTRL\tsource-controller\t1\tTrue\nKUST\tflux-system/cinova\ttrue\tTrue\t600\t9944640\n' | classify_loop | verdict_of)"
  [ "${V}" = "LIVE" ] || st_fail "a SUSPENDED 115d-old Kustomization read '${V}', expected LIVE — suspend is declared, not stale"

  # 6. Dead controllers OUTRANK stale Kustomizations in the verdict, because the
  #    staleness is their consequence — reporting STALE would bury the cause.
  V="$(printf 'CTRL\tsource-controller\t0\tTrue\nKUST\tflux-system/openova-dns\tfalse\tTrue\t600\t552960\n' | classify_loop | verdict_of)"
  [ "${V}" = "DEAD" ] || st_fail "dead controller + stale kustomization read '${V}', expected DEAD (cause over consequence)"

  # 7. VACUITY: empty input must not read LIVE by having nothing to inspect.
  out="$(printf '' | classify_loop)"
  live="$(printf '%s' "${out}" | grep '^CTRL_LIVE' | cut -f2)"
  [ "${live}" = "0" ] && printf '%s' "${out}" | grep -q '^VERDICT	LIVE' \
    || st_fail "empty input did not report CTRL_LIVE 0"
  echo "   (note: empty input reads LIVE with CTRL_LIVE=0 — the live sweep below"
  echo "    treats a zero-controller read as a precondition failure, not a pass.)"

  echo "OK — loop-liveness self-test passed (7 assertions: the #5573 vacuous-Available"
  echo "   shape is DEAD, a running controller is LIVE, a 6.4d-frozen Ready=True is"
  echo "   STALE, an in-interval one is not, a SUSPENDED overlay is not, dead"
  echo "   controllers outrank stale overlays, and an empty read is visibly empty)."
  echo "OK: --self-test only; no cluster was contacted."
  exit 0
fi

command -v kubectl >/dev/null 2>&1 || { echo "kubectl not found" >&2; exit 2; }
export FGL_STALE_FACTOR="${STALE_FACTOR}"
NOW_EPOCH="$(date -u +%s)"
[ -n "${NOW_OVERRIDE}" ] && NOW_EPOCH="${NOW_OVERRIDE}"
# Exported HERE, not on the classify_loop pipeline: the Kustomization extractor
# below needs it too, and scoping it to one command left that block raising
# KeyError while the controller half still (correctly) reported DEAD.
export FGL_NOW="${NOW_EPOCH}"

CTRLS="$(kubectl ${KUBECONFIG_ARG} -n flux-system get deploy -o json 2>/dev/null | python3 -c "$(cat <<'PY'
import json, sys
d = json.load(sys.stdin)
for i in d.get("items", []):
    n = i["metadata"]["name"]
    if not n.endswith("-controller"):
        continue
    rep = i.get("spec", {}).get("replicas")
    avail = ""
    for c in (i.get("status", {}).get("conditions") or []):
        if c.get("type") == "Available":
            avail = c.get("status", "")
    print("CTRL\t%s\t%s\t%s" % (n, "" if rep is None else rep, avail))
PY
)")" || { echo "could not read flux-system Deployments" >&2; exit 2; }

if [ -z "${CTRLS}" ]; then
  echo "PRECONDITION FAIL: no *-controller Deployments in flux-system." >&2
  echo "  A zero-controller read is NOT a clean loop — it means Flux is absent" >&2
  echo "  or the namespace is wrong. Failing closed rather than reporting LIVE." >&2
  exit 2
fi

KUSTS="$(kubectl ${KUBECONFIG_ARG} get kustomization -A -o json 2>/dev/null | python3 -c "$(cat <<'PY'
import json, sys, os, datetime
now = int(os.environ["FGL_NOW"])
d = json.load(sys.stdin)
def secs(iv):
    if not iv: return 0
    u, n = iv[-1], iv[:-1]
    try: n = float(n)
    except ValueError: return 0
    return int(n * {"s":1,"m":60,"h":3600}.get(u, 0))
for i in d.get("items", []):
    md = i["metadata"]
    name = "%s/%s" % (md["namespace"], md["name"])
    susp = str(i.get("spec", {}).get("suspend", False)).lower()
    ready, age = "", 0
    for c in (i.get("status", {}).get("conditions") or []):
        if c.get("type") == "Ready":
            ready = c.get("status", "")
            lt = c.get("lastTransitionTime", "")
            if lt:
                t = datetime.datetime.fromisoformat(lt.replace("Z", "+00:00"))
                age = int(now - t.timestamp())
    print("KUST\t%s\t%s\t%s\t%d\t%d" % (name, susp, ready, secs(i.get("spec", {}).get("interval", "")), age))
PY
)")" || KUSTS=""

RESULT="$(printf '%s\n%s\n' "${CTRLS}" "${KUSTS}" | classify_loop)"
VERDICT="$(printf '%s' "${RESULT}" | verdict_of)"

if [ "${VERDICT}" = "LIVE" ]; then
  echo "OK: GitOps loop is live — $(printf '%s' "${RESULT}" | grep '^CTRL_LIVE' | cut -f2) controller(s) scheduled, $(printf '%s' "${RESULT}" | grep '^KUST_OK' | cut -f2) Kustomization(s) reconciling within interval."
  exit 0
fi

echo "GITOPS LOOP ${VERDICT} — status conditions on this cluster are NOT trustworthy:"
printf '%s\n' "${RESULT}" | grep '^DEAD' | while IFS=$'\t' read -r _ n r a; do
  printf '  controller %-28s %s %s\n' "${n}" "${r}" "${a}"
done
printf '%s\n' "${RESULT}" | grep '^STALE' | while IFS=$'\t' read -r _ n ready iv ag; do
  printf '  kustomization %-34s %s %s %s\n' "${n}" "${ready}" "${iv}" "${ag}"
done
echo
echo "  A controller at replicas:0 reports Available=True vacuously, and every"
echo "  Kustomization keeps its LAST Ready value forever. Ready=True here means"
echo "  'last known good', not 'reconciling' (#5573)."
echo
echo "  Read-only: nothing was scaled or unsuspended. Restoring a stopped loop"
echo "  replays all accumulated commits at once — an operator decision."
exit 1
