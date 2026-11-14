#!/usr/bin/env bash
# Canonical Sovereign prov lifecycle — MECHANICAL enforcement of the two founder
# rules (2026-06-08). NEVER call POST /deployments or POST /wipe directly; go
# through this so the wrong order is impossible:
#   * fire  -> runs the MANDATORY scripts/prov-preflight.sh gate (fail-closed,
#             docs/PROTOCOL.md §5), then scripts/reset-uat.py, then POST /deployments
#   * wipe  -> CAPTURES the cloud-init log FIRST (GET /cloudinit-log, #3132),
#             then POST /wipe   <-- debug-before-wipe
#   * sweep -> lists what a wiped deployment left (scripts/flight-sweep.sh) until
#             nothing is left, at most 30 min; fire refuses without a clean sweep
# The capture relies on the control-plane self-uploading its log (#3132); on
# kom4dc the log cannot be pulled (no console-output API, no reachable sshd), so
# this is the only forensic for a Phase-1 failure. Refs #3132.
#
# Auth: mothership RS256 JWT from $CATALYST_MOTHERSHIP_KEY (default /tmp/hw-priv.pem).
set -uo pipefail

API="https://console.openova.io/sovereign/api/v1/deployments"
export KEY="${CATALYST_MOTHERSHIP_KEY:-/tmp/hw-priv.pem}"
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
EVID_DIR="${REPO_ROOT}/docs/sessions/$(date +%F)/prov-diagnostics"
# Kept variables documents (they hold cloud credentials): outside the repository.
FLIGHT_DIR="${FLIGHT_DIR:-/tmp/flight}"

_tok() {
  python3 -c 'import jwt,time,os;k=open(os.environ["KEY"]).read();n=int(time.time());print(jwt.encode({"email":"emrah.baysal@openova.io","sub":"emrah.baysal@openova.io","realm_access":{"roles":["catalyst-owner"]},"tier":"owner","iat":n,"exp":n+900},k,algorithm="RS256"))'
}

reset_uat() { python3 "${REPO_ROOT}/scripts/reset-uat.py" "${1:-pending fresh prov}"; }

# capture <id> — fetch the self-uploaded cloud-init log to docs/sessions/.../prov-diagnostics/
capture() {
  local id="$1" out code
  mkdir -p "$EVID_DIR"; out="${EVID_DIR}/${id}-cloudinit.log"
  code=$(curl -sk -o "$out" -w '%{http_code}' -H "Authorization: Bearer $(_tok)" "${API}/${id}/cloudinit-log" 2>/dev/null || echo 000)
  if [ "$code" = "200" ] && [ -s "$out" ]; then
    echo "captured cloud-init log -> $out ($(wc -l <"$out") lines)"
  else
    echo "WARN: no cloud-init log (HTTP $code) — env predates #3132 or never uploaded; proceeding."
    rm -f "$out"
  fi
  keep_vars "$id"
}

# keep_vars <id> — copy the deployment's tofu.auto.tfvars.json out of the
# mothership catalyst-api Pod to ${FLIGHT_DIR}/<id>-vars.json. A wipe that
# converges deletes the tofu workdir that holds it, and `sweep <id>` reads it.
# A failed copy leaves an earlier copy in place.
keep_vars() {
  local id="$1" apod tmp out="${FLIGHT_DIR}/${1}-vars.json"
  case "$id" in ''|*[!A-Za-z0-9-]*) echo "WARN: '${id}' is not a deployment id; variables document not kept."; return 0 ;; esac
  mkdir -p "$FLIGHT_DIR" && chmod 700 "$FLIGHT_DIR" && tmp="$(mktemp "${FLIGHT_DIR}/.keep.XXXXXX")" || { echo "WARN: cannot write ${FLIGHT_DIR}; variables document not kept."; return 0; }
  apod=$(kubectl -n catalyst get pods -o name 2>/dev/null | grep -m1 catalyst-api | cut -d/ -f2)
  if [ -n "$apod" ] && kubectl -n catalyst exec "$apod" --request-timeout=25s -- sh -c "cat /var/lib/catalyst/tofu/${id}/tofu.auto.tfvars.json 2>/dev/null || cat /deps/tofu/${id}/tofu.auto.tfvars.json" > "$tmp" 2>/dev/null \
     && python3 -c 'import json,sys; json.load(open(sys.argv[1]))' "$tmp" 2>/dev/null; then
    mv "$tmp" "$out"; echo "kept variables document -> $out"
  else
    rm -f "$tmp"
    if [ -s "$out" ]; then echo "WARN: variables document of ${id} not read now; the earlier copy $out stays."
    else echo "WARN: variables document of ${id} not kept; 'sweep ${id}' will report NOT MEASURED and a fire with PREV_ID=${id} will refuse."; fi
  fi
}

# sweep <id> — FLIGHT CHECKLIST: run scripts/flight-sweep.sh on the kept document
# until it finds nothing; the cloud frees quota ~15 min after a wipe, so it lists
# again every SWEEP_INTERVAL s (60) for up to SWEEP_TIMEOUT s (1800). Exit 0: the
# line "SWEEP PASS <id> leftovers=0", the leftover count for the gate input.
# 1: leftovers at the deadline. 2: usage. 3: not measured (no document, no listing).
sweep() {
  local id="${1:-}" vars rc start=$SECONDS timeout="${SWEEP_TIMEOUT:-1800}" interval="${SWEEP_INTERVAL:-60}"
  case "$id" in ''|*[!A-Za-z0-9-]*) echo "usage: $0 sweep <deployment id>"; return 2 ;; esac
  vars="${FLIGHT_DIR}/${id}-vars.json"
  if [ ! -s "$vars" ]; then echo "SWEEP NOT MEASURED ${id}: no kept variables document ${vars} (capture <id> keeps it; wipe runs capture first)"; return 3; fi
  while :; do
    bash "${REPO_ROOT}/scripts/flight-sweep.sh" "$vars"; rc=$?
    case "$rc" in
      0) echo "SWEEP PASS ${id} leftovers=0"; return 0 ;;
      1) if [ $((SECONDS - start + interval)) -ge "$timeout" ]; then echo "SWEEP FAIL ${id}: leftovers remain after $((SECONDS - start))s"; return 1; fi
         echo "leftovers remain; listing again in ${interval}s"; sleep "$interval" ;;
      *) echo "SWEEP NOT MEASURED ${id}: scripts/flight-sweep.sh exit ${rc}"; return 3 ;;
    esac
  done
}

# wipe <id> [fqdn] — DEBUG-BEFORE-WIPE: capture first, ALWAYS, then wipe.
# A Sovereign that has been handed over is wiped only with its FQDN as the
# second argument, sent as header X-Confirm-Sovereign-FQDN; without it the
# API answers 412 {"error":"confirm-fqdn-required"} and destroys nothing.
wipe() {
  local id="$1" fqdn="${2:-}"
  echo "== capture-before-wipe (founder rule 2026-06-08) =="; capture "$id"
  echo "== wiping $id =="
  curl -sk -X POST -H "Authorization: Bearer $(_tok)" -H "Content-Type: application/json" -H "X-Confirm-Sovereign-FQDN: ${fqdn}" -d '{}' "${API}/${id}/wipe"
  echo "wipe submitted: $id"
}

# fire <subdomain> [pool] — RESET-UAT-ON-FIRE: reset first, ALWAYS, then fire.
# QATEST / FIRECUT env vars parameterize qaTestEnabled / fireCutoverOnHandover
# (defaults false/true = the North-Star combo). #5119: these MUST reach the
# POST body — the script used to echo them in the preflight banner while the
# body omitted both, so the server default (fireCutoverOnHandover=false)
# silently disarmed the zero-touch cutover on every fire (hw256: chain sat
# dormant, child logged 'operator-gated Sovereign', fired via RT-10 instead).
# GITOPS_BRANCH (default empty) names the Git ref the new Sovereign follows:
# "main", or a release-candidate ref rc/<id>. When it is set, the POST body
# carries it as gitopsBranch; when it is empty, the body has no such key.
fire() {
  local sub="$1" pool="${2:-omani.works}" pub body
  local qatest="${QATEST:-false}" firecut="${FIRECUT:-true}"
  # FLIGHT CHECKLIST: PREV_ID names the deployment the last wipe removed; its
  # sweep must be clean. One listing, no waiting; fail-closed.
  if ! SWEEP_TIMEOUT=0 sweep "${PREV_ID:-}"; then
    echo "!! NO CLEAN SWEEP — fire ABORTED: set PREV_ID to the last wiped deployment and run '$0 sweep <that id>' until it prints SWEEP PASS"; return 1
  fi
  # MANDATORY pre-flight gate (docs/PROTOCOL.md §5; founder mandate 2026-06-30:
  # never fire blind). Fail-closed, no bypass. Runs BEFORE reset_uat so a
  # refused fire never flushes ledger evidence. HW_TFVARS auto-fetched from the
  # mothership catalyst-api PVC when unset (the only durable cred cache — L1).
  if [ -z "${HW_TFVARS:-}" ]; then
    local apod tfsrc
    apod=$(kubectl -n catalyst get pods -o name 2>/dev/null | grep -m1 catalyst-api | cut -d/ -f2)
    if [ -n "$apod" ]; then
      tfsrc=$(kubectl -n catalyst exec "$apod" --request-timeout=25s -- sh -c 'ls -t /var/lib/catalyst/tofu/*/tofu.auto.tfvars.json /deps/tofu/*/tofu.auto.tfvars.json 2>/dev/null | head -1' 2>/dev/null)
      if [ -n "$tfsrc" ]; then
        kubectl -n catalyst exec "$apod" --request-timeout=25s -- cat "$tfsrc" > /tmp/preflight-tfvars.json 2>/dev/null
        export HW_TFVARS=/tmp/preflight-tfvars.json
      fi
    fi
  fi
  echo "== MANDATORY prov-preflight gate (docs/PROTOCOL.md §5) =="
  # Preflight banner MUST echo the SAME values the body will carry (#5119:
  # echo-vs-body drift is what hid the disarmed cutover).
  if ! BEARER="$(_tok)" bash "${REPO_ROOT}/scripts/prov-preflight.sh" "${sub}.${pool}" "auto-${sub}" "$qatest" "$firecut"; then
    echo "!! PRE-FLIGHT FAIL — fire ABORTED (fix the ❌ items; never fire blind. ONE environment at a time: completely wipe the existing env FIRST — #5111)"; return 1
  fi
  echo "== reset-UAT-on-fire (founder rule 2026-06-08) =="; reset_uat "$sub"
  pub=$(cat /home/openova/.ssh/*.pub 2>/dev/null | grep -E "^ssh-" | head -1)
  # SHARED_PG drives the ADR-0010 reusable shared-Postgres model (#3188 ->
  # SOVEREIGN_ENABLE_SHARED_PG, fire-body wired in #3275). Default TRUE (#5099):
  # the platform canon is default-true everywhere else — provisioner Request
  # (deployments.go CreateDeployment pre-decode default, Refs #3370) and tofu
  # var enable_shared_pg (variables.tf default "true") — because the shared
  # trio (slots 16a/16c/16d) is founder North-Star-2 AND the only path that
  # self-registers the 3 shared-pg Application CRs on a fresh prov. This script
  # used to default SHARED_PG=false and stamp an EXPLICIT
  # `"enableSharedPostgres": false` into the POST body, which (by the
  # pointer-bool-emulation contract) OVERRIDES the server's default-true — on
  # hw255 that cascaded to SOVEREIGN_ENABLE_SHARED_PG="false" -> the slot-16a/
  # 16c/16d master gate off -> 3 HRs Ready over ZERO rendered resources (no
  # shared-pg Cluster, no Application CRs, ns shared-data empty; 5 Application
  # CRs where prior envs had 8). Set SHARED_PG=false explicitly for the
  # dedicated-per-consumer-cluster path.
  #
  # Node sizing is overridable per-fire (#3952 capacity verdict, 2026-06-20): the
  # default m7n.xlarge.8 x3 worker tier runs ~99% CPU once the full #3642 vc-mgmt
  # stack + 4 catalyst controllers + marketplace/billing/org-pg schedule, chronically
  # blocking pods on 'Insufficient cpu' (no Huawei autoscaler). For a real convergence
  # re-prov pass WORKER_COUNT=5 (stays on the PROVEN m7n.xlarge.8 flavor -> 20 vCPU/
  # region, absorbs the blocked worker-scheduled pods). Do NOT raise WORKER_SIZE to an
  # unverified flavor (e.g. m7n.2xlarge.8): me-east-215a flavor pools are finicky (hw30
  # RCA 2026-05-27 — s7n.large.4 exhausted -> Ecs.0219 No-valid-host, 11 wasted debug
  # waves). Only m7n.large.8 (CP) and m7n.xlarge.8 (worker) are verified-ACTIVE; verify
  # any other flavor via direct HCS API (sub_jobs[0].error_code) BEFORE firing.
  body=$(SUB="$sub" POOL="$pool" PUB="$pub" SHARED_PG="${SHARED_PG:-true}" QATEST="$qatest" FIRECUT="$firecut" \
    CP_SIZE="${CP_SIZE:-m7n.large.8}" WORKER_SIZE="${WORKER_SIZE:-m7n.xlarge.8}" WORKER_COUNT="${WORKER_COUNT:-3}" \
    python3 -c 'import json,os;s=os.environ["SUB"];p=os.environ["POOL"];c=os.environ["CP_SIZE"];w=os.environ["WORKER_SIZE"];wc=int(os.environ["WORKER_COUNT"]);print(json.dumps({"orgName":"Omantel","orgEmail":"emrah.baysal@openova.io","provider":"huawei","sovereignDomainMode":"pool","sovereignPoolDomain":p,"sovereignSubdomain":s,"sovereignFQDN":s+"."+p,"sshPublicKey":os.environ["PUB"],"enableSharedPostgres":os.environ.get("SHARED_PG")=="true","qaTestEnabled":os.environ.get("QATEST")=="true","fireCutoverOnHandover":os.environ.get("FIRECUT")=="true","regions":[{"provider":"huawei","cloudRegion":"me-east-215-a","controlPlaneSize":c,"workerSize":w,"workerCount":wc},{"provider":"huawei","cloudRegion":"me-east-215-b","controlPlaneSize":c,"workerSize":w,"workerCount":wc}],**({"gitopsBranch":os.environ["GITOPS_BRANCH"]} if os.environ.get("GITOPS_BRANCH") else {})}))')
  echo "== firing ${sub}.${pool} (CP=${CP_SIZE:-m7n.large.8} worker=${WORKER_SIZE:-m7n.xlarge.8}x${WORKER_COUNT:-3}/region sharedPG=${SHARED_PG:-true} qaTest=${qatest} fireCutover=${firecut} gitopsBranch=${GITOPS_BRANCH:-server default}) =="
  curl -sk -X POST -H "Authorization: Bearer $(_tok)" -H "Content-Type: application/json" -d "$body" "${API}"; echo
}

case "${1:-}" in
  fire)      shift; fire "$@" ;;
  wipe)      shift; wipe "$@" ;;
  capture)   shift; capture "$@" ;;
  sweep)     shift; sweep "$@" ;;
  reset-uat) shift; reset_uat "$@" ;;
  *) echo "usage: $0 {fire <subdomain> [pool] | wipe <id> [fqdn] | capture <id> | sweep <id> | reset-uat <env>}"
     echo "  fire ALWAYS resets UAT first and needs PREV_ID=<last wiped id> with a clean sweep; wipe ALWAYS captures first."; exit 2 ;;
esac
