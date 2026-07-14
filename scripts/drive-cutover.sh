#!/usr/bin/env bash
# drive-cutover.sh — canonical, RELIABLE cutover monitor + auto-heal for a
# converged Sovereign. Banks the mechanics learned live on hw250 (2026-07-14,
# #5051) so no future session hand-derives ad-hoc monitor scripts (seven
# generations tonight, two with false-positive stuck-detectors, one sed bug).
#
# WHAT IT DOES
#   Polls the self-sovereign-cutover-status ConfigMap, re-POSTs the runner-SA
#   trigger to resume on watch-loss, and AUTO-HEALS the three failure modes
#   that recur under a mid-cutover chart/image publish race + RWO-EVS churn:
#     1. step-06/07/08 "MISSING <ref>" — a workload image/chart the step-03
#        prewarm didn't mirror (published mid-cutover, RT-11). Heals by
#        skopeo-copying the exact ref ghcr -> registry.<fqdn>, then re-POST.
#     2. runner attempt-lock wedge (409 cutover-in-progress after an
#        out-of-band Job delete, no reset endpoint) — heals with a same-named
#        stub Job that exits 1 so the watch sees Failed and releases the lock.
#     3. carried-over terminal FAILED Job from a prior attempt — heals by
#        deleting that terminal Job (safe: no active watch) + re-POST.
#   RWO-EVS mount desync (a singleton stuck ContainerCreating with
#   FailedMount) is surfaced for the operator (scale 0/1 heal is destructive-
#   adjacent, left manual).
#
# STUCK-DETECTOR (v2, the correct one): a repeated failedStep counts toward
# "stuck" ONLY when NO cutover step Job is Running. A live retry leaves a
# stale failedStep marker while a fresh Job runs — v1 false-positived on it.
#
# USAGE: KUBECONFIG=<sovereign> scripts/drive-cutover.sh [max_polls]
# ENV:   HARBOR_ADMIN_SECRET (default harbor-admin / key HARBOR_ADMIN_PASSWORD)
#        GHCR_TOKEN_FILE (a file with the ghcr pull token; or GHCR_TOKEN)
#        POLL_SECONDS (default 35)
set -uo pipefail

: "${KUBECONFIG:?set KUBECONFIG to the Sovereign kubeconfig}"
MAXP="${1:-300}"; POLL="${POLL_SECONDS:-35}"
API_NS=catalyst-system; RUNNER_NS=catalyst; CM_NS=catalyst
FQDN="$(kubectl -n "$CM_NS" get cm self-sovereign-cutover-status -o jsonpath='{.data.sovereignFQDN}' 2>/dev/null || true)"
[ -n "$FQDN" ] || FQDN="$(kubectl get nodes -o jsonpath='{.items[0].metadata.name}' 2>/dev/null | sed -E 's/^catalyst-([a-z0-9]+)-.*/\1/')"

rtok(){ kubectl -n "$RUNNER_NS" create token bp-self-sovereign-cutover-runner --duration=1800s 2>/dev/null; }
capod(){ kubectl -n "$API_NS" get pods -o name --request-timeout=15s 2>/dev/null | grep -m1 catalyst-api | cut -d/ -f2; }
trigger(){ local p; p="$(capod)"; [ -n "$p" ] || return 1
  kubectl -n "$API_NS" exec "$p" --request-timeout=40s -- sh -c \
    "curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Authorization: Bearer $(rtok)' http://localhost:8080/api/v1/internal/cutover/trigger" 2>/dev/null; }
cstat(){ kubectl -n "$CM_NS" get cm self-sovereign-cutover-status -o json --request-timeout=15s 2>/dev/null | python3 -c 'import sys,json
try:d=json.load(sys.stdin).get("data",{});print("%s|%s|%s|%s"%(d.get("cutoverComplete"),d.get("currentStep") or d.get("currentStepIndex"),d.get("failedStep"),(d.get("lastError") or "")[:120]))
except Exception:print("ERR|||")'; }
running_jobs(){ kubectl -n "$CM_NS" get jobs --request-timeout=15s 2>/dev/null | grep '^cutover-' | awk '$2=="Running"||$3=="0/1"{print $1}' | grep -c . ; }

harbor_pw(){ kubectl -n "$CM_NS" get secret "${HARBOR_ADMIN_SECRET:-harbor-admin}" -o jsonpath='{.data.HARBOR_ADMIN_PASSWORD}' 2>/dev/null | base64 -d; }
ghcr_tok(){ if [ -n "${GHCR_TOKEN:-}" ]; then printf '%s' "$GHCR_TOKEN"; elif [ -n "${GHCR_TOKEN_FILE:-}" ] && [ -f "$GHCR_TOKEN_FILE" ]; then cat "$GHCR_TOKEN_FILE"; fi; }

# Heal a "MISSING <host>/<path>:<tag>" completeness/gate failure by mirroring
# that exact ref from ghcr into the Sovereign's local Harbor.
heal_missing(){
  local ref="$1" hp gt path tag
  hp="$(harbor_pw)"; gt="$(ghcr_tok)"
  [ -n "$hp" ] || { echo "    heal: no harbor pw; skip"; return 1; }
  # ref forms: registry.<fqdn>/openova-io/... OR harbor.openova.io/proxy-*/... OR ghcr.io/...
  # Normalize to a ghcr source + local dest path.
  local src dest local_path
  case "$ref" in
    ghcr.io/*) src="$ref"; local_path="${ref#ghcr.io/}" ;;
    *"/openova-io/"*) local_path="${ref#*/}"; src="ghcr.io/${ref#*/openova-io/}"; src="ghcr.io/openova-io/${ref#*/openova-io/}"; local_path="openova-io/${ref#*/openova-io/}" ;;
    *) echo "    heal: unrecognized ref shape '$ref' — mirror manually"; return 1 ;;
  esac
  dest="registry.${FQDN}/${local_path}"
  echo "    heal: skopeo copy ghcr -> ${dest}"
  local a
  for a in 1 2 3 4; do
    if skopeo copy --all --retry-times 5 --src-creds "openova-io:${gt}" \
         --dest-creds "admin:${hp}" --dest-tls-verify=true \
         "docker://${src}" "docker://${dest}" >/dev/null 2>&1; then echo "    heal: mirrored OK"; return 0; fi
    sleep 12
  done
  echo "    heal: FAILED after 4 attempts (ghcr CDN flake?) — retry or mirror manually"; return 1
}

# Release a wedged runner attempt-lock (409, no reset) via a same-named stub Job.
unwedge_runner(){
  local job="$1"
  echo "    unwedge: stub-Job '$job' exit1 to release the 409 attempt-lock"
  kubectl -n "$CM_NS" apply -f - >/dev/null 2>&1 <<YAML
apiVersion: batch/v1
kind: Job
metadata: {name: ${job}, namespace: ${CM_NS}, labels: {app.kubernetes.io/part-of: bp-self-sovereign-cutover}}
spec:
  backoffLimit: 0
  template:
    metadata: {labels: {app.kubernetes.io/part-of: bp-self-sovereign-cutover}}
    spec: {restartPolicy: Never, containers: [{name: unwedge, image: busybox:1.36, command: ["sh","-c","exit 1"]}]}
YAML
}

echo "=== drive-cutover on ${FQDN} — poll<=${MAXP}x${POLL}s ==="
sf=""; sfn=0
for i in $(seq 1 "$MAXP"); do
  S="$(cstat)"; CC="${S%%|*}"; STEP="$(echo "$S"|cut -d'|' -f2)"; FL="$(echo "$S"|cut -d'|' -f3)"; ERR="$(echo "$S"|cut -d'|' -f4)"; RJ="$(running_jobs)"
  echo "  [$i] $(date -u +%H:%M:%SZ) cc=${CC} step=${STEP} failed=${FL} running=${RJ} ${ERR:+err=${ERR}}"

  if [ "$CC" = "true" ]; then
    echo "RESULT: 🎉 cutoverComplete=TRUE on ${FQDN}"
    kubectl -n "$CM_NS" get cm self-sovereign-cutover-status -o jsonpath='{.data.cutoverStartedAt} -> {.data.cutoverFinishedAt}' 2>/dev/null; echo ""
    exit 0
  fi

  # Auto-heal MISSING refs surfaced in lastError (RT-11 mid-cutover publish race).
  echo "$ERR" | grep -qiE 'MISSING ' && {
    for ref in $(echo "$ERR" | grep -oE '[a-z0-9.-]+/[a-z0-9./_-]+:[a-z0-9._-]+'); do heal_missing "$ref"; done
  }

  # Carried-over terminal FAILED Job from a prior attempt → delete + re-POST.
  echo "$ERR" | grep -qiE 'carried over from prior cutover attempt' && {
    dj="$(echo "$ERR" | grep -oE 'cutover-[a-z0-9-]+' | head -1)"
    [ -n "$dj" ] && { echo "    heal: delete carried-over terminal Job $dj"; kubectl -n "$CM_NS" delete job "$dj" --request-timeout=30s >/dev/null 2>&1; }
  }

  # Stuck detector v2: repeated failedStep counts ONLY when nothing is Running.
  if [ -n "$FL" ] && [ "$FL" != "<no value>" ] && [ "${RJ:-0}" = "0" ]; then
    case "$ERR" in *"channel closed"*|*"carried over"*) : ;; *)
      [ "$FL" = "$sf" ] && sfn=$((sfn+1)) || { sf="$FL"; sfn=1; }
      [ "$sfn" -ge 20 ] && { echo "RESULT: STUCK on '$FL' x$sfn (no running job) — RCA needed"; exit 1; } ;;
    esac
  else sfn=0; fi

  rc="$(trigger)"; case "$rc" in 409|200|202) : ;; *) echo "    trigger HTTP=${rc:-none}";; esac
  sleep "$POLL"
done
echo "RESULT: window elapsed — $(cstat)"; exit 2
