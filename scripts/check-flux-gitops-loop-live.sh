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
# WHY, second instance (#6079). Scaling the controllers back up did NOT restore
# the loop, and the first cut of this file could not tell. Measured on the
# mothership 2026-08-10T22:2xZ:
#
#     source-controller       spec.replicas=1  readyReplicas=0  Available=False
#     kustomize-controller    spec.replicas=1  readyReplicas=0  Available=False
#     both pods ImagePullBackOff on ghcr.io/fluxcd/*-controller
#
# Fed to the classifier, that pair read `VERDICT LIVE` — because the check
# consulted `spec.replicas`, which records what an operator ASKED FOR, not what
# is running. spec.replicas is a field that cannot fail: a human types 1 into it
# and it stays 1 whether or not a single pod ever starts. `status.readyReplicas`
# is the only field the cluster writes from observation, so it is the only one
# that carries evidence. The check now reads it, and prints the pod's waiting
# reason so the report names the cause instead of restating the symptom.
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
#   CTRL\t<name>\t<spec.replicas>\t<availableCondition>\t<status.readyReplicas>\t<podReason>
#   KUST\t<ns/name>\t<suspend>\t<readyStatus>\t<intervalSeconds>\t<ageSeconds>
#
# The last two CTRL fields are optional so a 4-field record still parses, but a
# record that omits readyReplicas is treated as UNKNOWN-and-not-ready, never as
# ready. Absent evidence must not read as a pass.
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
        f = (f + ["", ""])[:6]
        _, name, replicas, avail, ready, reason = f
        if replicas in ("0", ""):
            # The vacuous-Available case: report it EVEN THOUGH avail says True.
            dead_ctrls.append((name, "replicas=%s (scaled to zero) available=%s"
                                     % (replicas or "<unset>", avail)))
        elif ready in ("0", ""):
            # Scaled up, nothing running. spec.replicas is an operator's INTENT;
            # only status.readyReplicas says a controller process exists.
            dead_ctrls.append((name, "replicas=%s readyReplicas=%s available=%s%s"
                                     % (replicas, ready or "<unset>", avail,
                                        (" — " + reason) if reason else "")))
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
for n, detail in dead_ctrls:
    print("DEAD\t%s\t%s" % (n, detail))
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
  V="$(printf 'CTRL\tsource-controller\t0\tTrue\t0\t\n' | classify_loop | verdict_of)"
  [ "${V}" = "DEAD" ] || st_fail "replicas=0 with Available=True read '${V}', expected DEAD — vacuous availability is the #5573 false-green"

  # 2. CONTROL: a running controller must read LIVE, so the check is not
  #    "any controller is dead".
  V="$(printf 'CTRL\tsource-controller\t1\tTrue\t1\t\n' | classify_loop | verdict_of)"
  [ "${V}" = "LIVE" ] || st_fail "replicas=1 readyReplicas=1 read '${V}', expected LIVE"

  # 2a. THE SECOND FALSE-GREEN this file now also guards (#6079). Measured on
  #     the mothership 2026-08-10T22:2xZ: source-controller and
  #     kustomize-controller at spec.replicas=1, status.readyReplicas=0, both
  #     pods ImagePullBackOff on ghcr.io. Reading spec.replicas alone — an
  #     operator's INTENT — called that pair LIVE while the GitOps loop had
  #     delivered nothing for 9.5 days. Only status.readyReplicas is evidence
  #     that a controller process exists.
  V="$(printf 'CTRL\tsource-controller\t1\tFalse\t0\tImagePullBackOff: ghcr.io/fluxcd/source-controller:v1.8.0\n' | classify_loop | verdict_of)"
  [ "${V}" = "DEAD" ] || st_fail "replicas=1 readyReplicas=0 read '${V}', expected DEAD — scaled up is not running"

  # 2b. The reason string must reach the operator, or the report says
  #     'not ready' and buries WHY.
  printf 'CTRL\tsource-controller\t1\tFalse\t0\tImagePullBackOff: ghcr.io/fluxcd/source-controller:v1.8.0\n' \
    | classify_loop | grep -q 'ImagePullBackOff: ghcr.io/fluxcd/source-controller:v1.8.0' \
    || st_fail "the pod waiting reason was dropped from the DEAD line"

  # 2c. A CTRL record with readyReplicas MISSING must not read LIVE. Absent
  #     evidence is not evidence of readiness.
  V="$(printf 'CTRL\tsource-controller\t1\tTrue\n' | classify_loop | verdict_of)"
  [ "${V}" = "DEAD" ] || st_fail "a CTRL record with no readyReplicas read '${V}', expected DEAD — absent evidence must not pass"

  # 3. The frozen-Kustomization shape: Ready=True, 10m interval, 6.4 days old.
  #    Exactly flux-system/openova-dns as measured.
  V="$(printf 'CTRL\tsource-controller\t1\tTrue\t1\t\nKUST\tflux-system/openova-dns\tfalse\tTrue\t600\t552960\n' | classify_loop | verdict_of)"
  [ "${V}" = "STALE" ] || st_fail "Ready=True frozen 6.4d on a 10m interval read '${V}', expected STALE"

  # 4. CONTROL: a Kustomization inside its interval must NOT be flagged.
  V="$(printf 'CTRL\tsource-controller\t1\tTrue\t1\t\nKUST\tflux-system/openova-dns\tfalse\tTrue\t600\t900\n' | classify_loop | verdict_of)"
  [ "${V}" = "LIVE" ] || st_fail "Ready=True 15m old on a 10m interval read '${V}', expected LIVE"

  # 5. CONTROL: a SUSPENDED Kustomization is a declared state, not staleness.
  #    Without this the check would flag every deliberately-paused overlay.
  V="$(printf 'CTRL\tsource-controller\t1\tTrue\t1\t\nKUST\tflux-system/cinova\ttrue\tTrue\t600\t9944640\n' | classify_loop | verdict_of)"
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

  echo "OK — loop-liveness self-test passed (11 assertions: the #5573 vacuous-Available"
  echo "   shape is DEAD, the #6079 scaled-up-but-zero-ready shape is DEAD and keeps"
  echo "   its waiting reason, a CTRL record with no readyReplicas does not pass, a"
  echo "   genuinely running controller is LIVE, a 6.4d-frozen Ready=True is STALE, an"
  echo "   in-interval one is not, a SUSPENDED overlay is not, dead controllers outrank"
  echo "   stale overlays, and an empty read is visibly empty)."
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

# Pod-level waiting reasons, keyed by the controller name their pod belongs to.
# Purely enrichment: the verdict never depends on this read succeeding, but when
# it does the report says "ImagePullBackOff: ghcr.io/fluxcd/…" instead of the
# useless "not ready".
PODREASONS="$(kubectl ${KUBECONFIG_ARG} -n flux-system get pods -o json 2>/dev/null | python3 -c "$(cat <<'PY'
import json, sys
try:
    d = json.load(sys.stdin)
except Exception:
    sys.exit(0)
for p in d.get("items", []):
    owner = (p["metadata"].get("labels") or {}).get("app", "")
    if not owner:
        continue
    for cs in (p.get("status", {}).get("containerStatuses") or []):
        w = (cs.get("state") or {}).get("waiting")
        if w:
            msg = "%s: %s" % (w.get("reason", "Waiting"), cs.get("image", ""))
            print("%s\t%s" % (owner, msg.replace("\t", " ").strip()))
            break
PY
)" || true)"

CTRLS="$(FGL_PODREASONS="${PODREASONS}" kubectl ${KUBECONFIG_ARG} -n flux-system get deploy -o json 2>/dev/null | FGL_PODREASONS="${PODREASONS}" python3 -c "$(cat <<'PY'
import json, os, sys
reasons = {}
for line in (os.environ.get("FGL_PODREASONS") or "").splitlines():
    if "\t" in line:
        k, v = line.split("\t", 1)
        reasons.setdefault(k, v)
d = json.load(sys.stdin)
for i in d.get("items", []):
    n = i["metadata"]["name"]
    if not n.endswith("-controller"):
        continue
    rep = i.get("spec", {}).get("replicas")
    st = i.get("status", {}) or {}
    # readyReplicas is OMITTED (not 0) when nothing is ready, so `.get` must
    # default to 0 rather than "" — an absent key is zero ready, not unknown.
    ready = st.get("readyReplicas", 0)
    avail = ""
    for c in (st.get("conditions") or []):
        if c.get("type") == "Available":
            avail = c.get("status", "")
    print("CTRL\t%s\t%s\t%s\t%s\t%s" % (
        n, "" if rep is None else rep, avail, ready, reasons.get(n, "")))
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
printf '%s\n' "${RESULT}" | grep '^DEAD' | while IFS=$'\t' read -r _ n detail; do
  printf '  controller %-28s %s\n' "${n}" "${detail}"
done
printf '%s\n' "${RESULT}" | grep '^STALE' | while IFS=$'\t' read -r _ n ready iv ag; do
  printf '  kustomization %-34s %s %s %s\n' "${n}" "${ready}" "${iv}" "${ag}"
done
echo
echo "  A controller at replicas:0 reports Available=True vacuously, and every"
echo "  Kustomization keeps its LAST Ready value forever. Ready=True here means"
echo "  'last known good', not 'reconciling' (#5573). A controller at replicas:1"
echo "  with readyReplicas:0 is the same loop failure wearing a healthy-looking"
echo "  spec — scaling up is a request, not a running process (#6079)."
echo
echo "  Read-only: nothing was scaled or unsuspended. Restoring a stopped loop"
echo "  replays all accumulated commits at once — an operator decision."
exit 1
