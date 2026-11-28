#!/usr/bin/env bash
# Canonical Sovereign prov lifecycle — MECHANICAL enforcement of the founder
# rules (2026-06-08). NEVER call POST /deployments or POST /wipe directly; go
# through this so the wrong order is impossible:
#   * fire  -> runs the MANDATORY scripts/prov-preflight.sh gate (fail-closed,
#             docs/PROTOCOL.md §5), then POST /deployments; no UAT reset (after ready)
#   * wipe  -> CAPTURES the cloud-init log FIRST (GET /cloudinit-log, #3132),
#             then POST /wipe   <-- debug-before-wipe
#   * sweep -> lists what a wiped deployment left (scripts/flight-sweep.sh) until
#             nothing is left, at most 30 min; fire refuses without a clean sweep
#   * standing, wipe-result, wait-ready, kubeconfig(-match), rules, and before a wipe request-body and
#     preflight (nothing that can refuse the fire is found after the wipe): for .github/workflows/release-train.yaml
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

# get <path> <out file> — GET ${API}<path> as the owner; prints the HTTP code (000 when nothing answered).
get() { curl -sk -o "$2" -w '%{http_code}' -H "Authorization: Bearer $(_tok)" "${API}$1" 2>/dev/null || echo 000; }

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
# The report of the last listing stays at ${FLIGHT_DIR}/<id>-sweep.json: fire reads it.
sweep() {
  local id="${1:-}" vars rc start=$SECONDS timeout="${SWEEP_TIMEOUT:-1800}" interval="${SWEEP_INTERVAL:-60}"
  case "$id" in ''|*[!A-Za-z0-9-]*) echo "usage: $0 sweep <deployment id>"; return 2 ;; esac
  vars="${FLIGHT_DIR}/${id}-vars.json"
  if [ ! -s "$vars" ]; then echo "SWEEP NOT MEASURED ${id}: no kept variables document ${vars} (capture <id> keeps it; wipe runs capture first)"; return 3; fi
  while :; do
    bash "${REPO_ROOT}/scripts/flight-sweep.sh" "$vars" | tee "${FLIGHT_DIR}/${id}-sweep.json"; rc=${PIPESTATUS[0]}
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
# Exit 1 unless the API accepted the wipe (HTTP 202); `wipe-result <id>` then waits for its end.
wipe() {
  local id="$1" fqdn="${2:-}" code
  echo "== capture-before-wipe (founder rule 2026-06-08) =="; capture "$id"
  echo "== wiping $id =="; mkdir -p "$FLIGHT_DIR"
  code=$(curl -sk -o "${FLIGHT_DIR}/${id}-wipe.json" -w '%{http_code}' -X POST -H "Authorization: Bearer $(_tok)" -H "Content-Type: application/json" -H "X-Confirm-Sovereign-FQDN: ${fqdn}" -d '{}' "${API}/${id}/wipe" 2>/dev/null || echo 000)
  cat "${FLIGHT_DIR}/${id}-wipe.json" 2>/dev/null; echo
  if [ "$code" != 202 ]; then echo "!! WIPE REFUSED ${id}: HTTP ${code}"; return 1; fi
  echo "wipe submitted: $id"
}

# wipe-result <id> — waits for the end of the wipe of <id>, reading it every WIPE_INTERVAL s (30) for up to
# WIPE_TIMEOUT s (3600). Exit 0: status wiped, HTTP 410, or 404 once wiping was read. 1: another status, or
# still wiping at the deadline. 2: usage. 3: not measured (no answer, no access, 404 before wiping).
wipe_result() {
  local id="${1:-}" start=$SECONDS seen="" code st out
  case "$id" in ''|*[!A-Za-z0-9-]*) echo "usage: $0 wipe-result <deployment id>"; return 2 ;; esac
  out="$(mktemp)"
  while :; do
    code=$(get "/${id}" "$out"); st=""
    [ "$code" = 200 ] && st="$(jq -r '.status // ""' "$out" 2>/dev/null)"
    case "$code:$st" in
      200:wiped) echo "WIPE DONE ${id}: $(jq -c '.lastWipeReport' "$out")"; rm -f "$out"; return 0 ;;
      410:*) echo "WIPE DONE ${id}: HTTP 410"; rm -f "$out"; return 0 ;;
      404:*) rm -f "$out"; if [ -n "$seen" ]; then echo "WIPE DONE ${id}: the record is gone"; return 0; fi
             echo "WIPE NOT MEASURED ${id}: HTTP 404 before the status wiping was read"; return 3 ;;
      200:wiping) seen=1 ;;
      401:*|403:*) rm -f "$out"; echo "WIPE NOT MEASURED ${id}: HTTP ${code}"; return 3 ;;
      200:*) rm -f "$out"; echo "WIPE FAIL ${id}: status ${st:-none}, not wiping or wiped"; return 1 ;;
    esac
    if [ $((SECONDS - start)) -ge "${WIPE_TIMEOUT:-3600}" ]; then
      rm -f "$out"; if [ -z "$seen" ]; then echo "WIPE NOT MEASURED ${id}: HTTP ${code}"; return 3; fi
      echo "WIPE FAIL ${id}: still wiping after $((SECONDS - start))s"; return 1
    fi
    sleep "${WIPE_INTERVAL:-30}"
  done
}

# wait-ready <id> — every READY_INTERVAL s (60) for up to READY_TIMEOUT s (14400). Exit 0: status ready. 1: failed,
# partial-failure, wiping, wiped, 404, 410, or not ready at the deadline. 2: usage. 3: no access, never answered.
wait_ready() {
  local id="${1:-}" start=$SECONDS code st out last=""
  case "$id" in ''|*[!A-Za-z0-9-]*) echo "usage: $0 wait-ready <deployment id>"; return 2 ;; esac
  out="$(mktemp)"
  while :; do
    code=$(get "/${id}" "$out"); st=""
    [ "$code" = 200 ] && st="$(jq -r '.status // ""' "$out" 2>/dev/null)"
    case "$code:$st" in
      200:ready) rm -f "$out"; echo "READY ${id}"; return 0 ;;
      200:failed|200:partial-failure|200:wiping|200:wiped|404:*|410:*)
        echo "NOT READY ${id}: HTTP ${code} status ${st:-none} $(jq -c '.error // empty' "$out" 2>/dev/null)"; rm -f "$out"; return 1 ;;
      401:*|403:*) rm -f "$out"; echo "READY NOT MEASURED ${id}: HTTP ${code}"; return 3 ;;
      200:*) [ "$st" = "$last" ] || echo "status ${st} after $((SECONDS - start))s"; last="$st" ;;
    esac
    if [ $((SECONDS - start)) -ge "${READY_TIMEOUT:-14400}" ]; then
      rm -f "$out"; if [ -z "$last" ]; then echo "READY NOT MEASURED ${id}: HTTP ${code}"; return 3; fi
      echo "NOT READY ${id}: status ${last} after $((SECONDS - start))s"; return 1
    fi
    sleep "${READY_INTERVAL:-60}"
  done
}

# standing — the live deployments of the owner, one line each: <id> <status> <fqdn> <startedAt>. Live is
# every status but wiped, deleted, failed and gone, as check 2 of scripts/prov-preflight.sh counts them.
# Exit 3 when the list cannot be read.
standing() {
  local out code
  out="$(mktemp)"; code=$(get "" "$out")
  if [ "$code" != 200 ] || ! jq -r '(if type == "array" then . else (.deployments // .items // []) end)[]
      | select((.status // "") as $s | ["", "wiped", "deleted", "failed", "gone"] | index($s) | not)
      | "\(.id) \(.status) \(.sovereignFQDN // "-") \(.startedAt // "-")"' "$out"; then
    rm -f "$out"; echo "STANDING NOT MEASURED: HTTP ${code}" >&2; return 3
  fi
  rm -f "$out"
}

# kubeconfig <id> a|b — prints the kubeconfig of the region of <id> that starts as Primary (a, the first
# region of the request) or as Standby (b, the second). Exit 3 when the API serves none.
kubeconfig() {
  local id="${1:-}" which="${2:-}" q="" out code
  case "$id:$which" in *[!A-Za-z0-9:-]*|:*|*:) echo "usage: $0 kubeconfig <deployment id> a|b" >&2; return 2 ;; esac
  case "$which" in a) ;; b) q="?region=$(request_body x omani.works | jq -r '.regions[1].cloudRegion')" ;; *) echo "usage: $0 kubeconfig <deployment id> a|b" >&2; return 2 ;; esac
  out="$(umask 077; mktemp)"; code=$(get "/${id}/kubeconfig${q}" "$out")
  if [ "$code" != 200 ] || ! grep -q 'server:' "$out"; then rm -f "$out"; echo "KUBECONFIG NOT MEASURED ${id} ${which}: HTTP ${code}" >&2; return 3; fi
  cat "$out"; rm -f "$out"
}

# kubeconfig-match <id> <file a> <file b> — exit 0 when both name the API server and certificate authority of
# the regions of <id> (the gate's ACCEPTANCE_KUBECONFIG_A and _B); 1 when one does not; 3 when unreadable.
kubeconfig_match() {
  local id="${1:-}" w f got want bad=0
  [ "$#" = 3 ] || { echo "usage: $0 kubeconfig-match <deployment id> <file a> <file b>"; return 2; }
  for w in a b; do
    if [ "$w" = a ]; then f="$2"; else f="$3"; fi
    want="$(kubeconfig "$id" "$w" | grep -E '^[[:space:]]*(server|certificate-authority-data):' | tr -d '[:space:]')"
    [ -n "$want" ] || { echo "KUBECONFIG NOT MEASURED ${id} ${w}"; return 3; }
    got="$(grep -E '^[[:space:]]*(server|certificate-authority-data):' "$f" 2>/dev/null | tr -d '[:space:]')"
    if [ "$got" = "$want" ]; then echo "KUBECONFIG ${w} names the region of ${id}"
    else echo "KUBECONFIG ${w} does NOT name the region of ${id}: set it to the output of '$0 kubeconfig ${id} ${w}'"; bad=1; fi
  done
  return "$bad"
}

# preflight_value <sweep report> <pre-flight log> <UTC time> — the gate input preflight (tests/acceptance/sovereign/
# README.md): used and left of each kind of the report, allowed from the passed checks 3c (networks), 3d (volumes)
# and 3e (buckets) of the log, else -. Exit 1 when the report cannot be read.
preflight_value() {
  local n v b
  lim() { sed -n "s/^.*✅ $1\. [^0-9]*[0-9][0-9]*\/\([0-9][0-9]*\).*$/\1/p" "$2" | head -n 1; }
  n="$(lim 3c "$2")"; v="$(lim 3d "$2")"; b="$(lim 3e "$2")"
  jq -er --arg t "$3" --arg networks "${n:--}" --arg volumes "${v:--}" --arg buckets "${b:--}" \
    '$t + ";" + ([.kinds[] | if .kind == "dns_records" then "dns_records=-/-/\(.leftovers | length)"
      else "\(.kind)=\(.used // "-")/\($ARGS.named[.kind] // "-")/\(.leftovers | length)" end] | join(","))' "$1"
}

# fire <subdomain> [pool] — a clean sweep of PREV_ID, the pre-flight gate, then the create
# call. No UAT reset: that runs after the new env is ready (docs/RUNBOOKS.md §0.3).
# QATEST / FIRECUT env vars parameterize qaTestEnabled / fireCutoverOnHandover
# (defaults false/true = the North-Star combo). #5119: these MUST reach the
# POST body — the script used to echo them in the preflight banner while the
# body omitted both, so the server default (fireCutoverOnHandover=false)
# silently disarmed the zero-touch cutover on every fire (hw256: chain sat
# dormant, child logged 'operator-gated Sovereign', fired via RT-10 instead).
# GITOPS_BRANCH (default empty) names the Git ref the new Sovereign follows:
# "main", or a release-candidate ref rc/<id>. When it is set, the POST body
# carries it as gitopsBranch; when it is empty, the body has no such key.
# FIRE_REQUEST (default empty), a JSON object, replaces or adds keys of the body (release-train.yaml passes its
# TRAIN_PROVISION_REQUEST); the pre-flight gets qaTestEnabled and fireCutoverOnHandover from the final body.
# Prints PREFLIGHT=<value>, LEFTOVERS=<count of the last sweep> and, on HTTP 201, DEPLOYMENT_ID=<id>.
fire() {
  local sub="$1" pool="${2:-omani.works}" body qatest firecut log pf left out code id
  # FLIGHT CHECKLIST: PREV_ID names the deployment the last wipe removed; its
  # sweep must be clean. One listing, no waiting; fail-closed.
  if ! SWEEP_TIMEOUT=0 sweep "${PREV_ID:-}"; then
    echo "!! NO CLEAN SWEEP — fire ABORTED: set PREV_ID to the last wiped deployment and run '$0 sweep <that id>' until it prints SWEEP PASS"; return 1
  fi
  body="$(body_checked "$sub" "$pool")" || { echo "!! the request body is refused (above) — fire ABORTED"; return 1; }
  qatest="$(jq -r '.qaTestEnabled' <<<"$body")"; firecut="$(jq -r '.fireCutoverOnHandover' <<<"$body")"
  # MANDATORY pre-flight gate (docs/PROTOCOL.md §5; founder mandate 2026-06-30:
  # never fire blind). Fail-closed, no bypass.
  log="${FLIGHT_DIR}/${PREV_ID}-preflight.log"
  if ! preflight_run "$sub" "$pool" "$body" "$log"; then
    echo "!! PRE-FLIGHT FAIL — fire ABORTED (fix the ❌ items; never fire blind. ONE environment at a time: completely wipe the existing env FIRST — #5111)"; return 1
  fi
  pf="$(preflight_value "${FLIGHT_DIR}/${PREV_ID}-sweep.json" "$log" "$(date -u +%Y-%m-%dT%H:%M:%SZ)")" \
    && left="$(jq -er '.leftoverCount | numbers' "${FLIGHT_DIR}/${PREV_ID}-sweep.json")" \
    || { echo "!! the pre-flight numbers or the leftover count cannot be read from the last sweep report — fire ABORTED"; return 1; }
  echo "PREFLIGHT=${pf}"
  echo "LEFTOVERS=${left}"
  echo "== firing ${sub}.${pool} (CP=${CP_SIZE:-m7n.large.8} worker=${WORKER_SIZE:-m7n.xlarge.8}x${WORKER_COUNT:-3}/region sharedPG=$(jq -r '.enableSharedPostgres' <<<"$body") qaTest=${qatest} fireCutover=${firecut} gitopsBranch=${GITOPS_BRANCH:-server default}) =="
  out="$(mktemp)"
  code=$(curl -sk -o "$out" -w '%{http_code}' -X POST -H "Authorization: Bearer $(_tok)" -H "Content-Type: application/json" -d "$body" "${API}" 2>/dev/null || echo 000)
  cat "$out"; echo
  id="$(jq -r '.id // empty' "$out" 2>/dev/null)"; rm -f "$out"
  if [ "$code" != 201 ] || [ -z "$id" ]; then echo "!! FIRE REFUSED: HTTP ${code}"; return 1; fi
  echo "DEPLOYMENT_ID=${id}"
}

# request_body <subdomain> <pool> — the POST body of a fire, one line of JSON; the
# keys of FIRE_REQUEST replace or add keys. Exit 1 when FIRE_REQUEST is no JSON object.
request_body() {
  local sub="$1" pool="$2" pub base extra="${FIRE_REQUEST:-}"
  # The node key: sshPublicKey of FIRE_REQUEST, else TRAIN_SSH_PUBLIC_KEY, else a key of the bastion user (a run there).
  pub="${TRAIN_SSH_PUBLIC_KEY:-}"; [ -n "$pub" ] || pub=$(cat /home/openova/.ssh/*.pub 2>/dev/null | grep -E "^ssh-" | head -1)
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
  base=$(SUB="$sub" POOL="$pool" PUB="$pub" SHARED_PG="${SHARED_PG:-true}" QATEST="${QATEST:-false}" FIRECUT="${FIRECUT:-true}" \
    CP_SIZE="${CP_SIZE:-m7n.large.8}" WORKER_SIZE="${WORKER_SIZE:-m7n.xlarge.8}" WORKER_COUNT="${WORKER_COUNT:-3}" \
    python3 -c 'import json,os;s=os.environ["SUB"];p=os.environ["POOL"];c=os.environ["CP_SIZE"];w=os.environ["WORKER_SIZE"];wc=int(os.environ["WORKER_COUNT"]);print(json.dumps({"orgName":"Omantel","orgEmail":"emrah.baysal@openova.io","provider":"huawei","sovereignDomainMode":"pool","sovereignPoolDomain":p,"sovereignSubdomain":s,"sovereignFQDN":s+"."+p,"sshPublicKey":os.environ["PUB"],"enableSharedPostgres":os.environ.get("SHARED_PG")=="true","qaTestEnabled":os.environ.get("QATEST")=="true","fireCutoverOnHandover":os.environ.get("FIRECUT")=="true","regions":[{"provider":"huawei","cloudRegion":"me-east-215-a","controlPlaneSize":c,"workerSize":w,"workerCount":wc},{"provider":"huawei","cloudRegion":"me-east-215-b","controlPlaneSize":c,"workerSize":w,"workerCount":wc}],**({"gitopsBranch":os.environ["GITOPS_BRANCH"]} if os.environ.get("GITOPS_BRANCH") else {})}))')
  [ -n "$extra" ] || extra='{}'
  jq -cn --argjson a "$base" --argjson b "$extra" 'if ($b | type) == "object" then $a + $b else error("not an object") end' 2>/dev/null || return 1
}

# request-body <subdomain> [pool] — the body of the fire, checked: prints it, or exit 1 naming each field that
# Request.Validate (products/catalyst/bootstrap/api/internal/provisioner/provisioner.go) refuses when empty.
body_checked() {
  local body miss
  body="$(request_body "$1" "${2:-omani.works}")" || { echo "REQUEST BODY REFUSED: FIRE_REQUEST is no JSON object" >&2; return 1; }
  miss="$(jq -r 'def blank: (. // "" | tostring | test("^\\s*$")); (.regions // []) as $rs
    | (["orgName", "orgEmail", "sshPublicKey", "sovereignFQDN"][] as $k | select(.[$k] | blank) | $k),
      (if $rs == [] then "regions" else empty end),
      ($rs[] as $r | ["provider", "cloudRegion", "controlPlaneSize"][] as $k | select($r[$k] | blank) | "regions[].\($k)"),
      ($rs[] | select((.workerCount // 0) > 0 and (.workerSize | blank)) | "regions[].workerSize"),
      ($rs[] | select((.workerCount // 0) < 0) | "regions[].workerCount")' <<<"$body" | sort -u | paste -sd, -)" \
    || { echo "REQUEST BODY REFUSED: the body cannot be read" >&2; return 1; }
  [ -z "$miss" ] || { echo "REQUEST BODY REFUSED: empty ${miss} (the API answers HTTP 400)" >&2; return 1; }
  printf '%s\n' "$body"
}

# preflight_run <subdomain> <pool> <body> <log> — scripts/prov-preflight.sh with the flags of <body>, its output kept
# in <log>. HW_TFVARS when unset: the variables document kept of PREV_ID, else the newest on the mothership's
# catalyst-api PVC (the only durable cred cache — L1).
preflight_run() {
  local apod tfsrc
  [ -n "${HW_TFVARS:-}" ] || [ ! -s "${FLIGHT_DIR}/${PREV_ID:-}-vars.json" ] || export HW_TFVARS="${FLIGHT_DIR}/${PREV_ID}-vars.json"
  if [ -z "${HW_TFVARS:-}" ]; then
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
  # The banner MUST echo the SAME values the body carries (#5119: echo-vs-body drift hid the disarmed cutover).
  BEARER="$(_tok)" bash "${REPO_ROOT}/scripts/prov-preflight.sh" "${1}.${2}" "auto-${1}" "$(jq -r '.qaTestEnabled' <<<"$3")" "$(jq -r '.fireCutoverOnHandover' <<<"$3")" | tee "$4"
  return "${PIPESTATUS[0]}"
}

# preflight <subdomain> [pool] — before the wipe of PREV_ID: the checked body, the variables document of PREV_ID
# kept, then the checks of scripts/prov-preflight.sh that need no wiped state (PREFLIGHT_BEFORE_WIPE=1).
preflight() {
  local body
  case "${PREV_ID:-}" in ''|*[!A-Za-z0-9-]*) echo "usage: PREV_ID=<the id to wipe> $0 preflight <subdomain> [pool]"; return 2 ;; esac
  body="$(body_checked "${1:-}" "${2:-omani.works}")" || return 1
  keep_vars "$PREV_ID"
  PREFLIGHT_BEFORE_WIPE=1 preflight_run "$1" "${2:-omani.works}" "$body" "${FLIGHT_DIR}/${PREV_ID}-preflight-before-wipe.log" \
    || { echo "!! PRE-FLIGHT (before the wipe) FAIL — nothing wiped, no flight"; return 1; }
}

# rules week <started> <stage_exit> | same <stage_exit> <commit> <standing commit> | due <standing commit> <created>
# The rules of a flight (docs/RUNBOOKS.md section 0.8). Exit 0: the flight may go on (due: none is due); 1: not,
# with the reason; 2: usage. A time or a list of files that cannot be read refuses.
rules() {
  local now="${RULES_NOW:-$(date -u +%s)}" t files
  epoch() { case "$1" in [12][0-9][0-9][0-9]-[01][0-9]-[0-3][0-9]T*) date -u -d "$1" +%s 2>/dev/null ;; *) return 1 ;; esac; }
  case "${1:-}:$#" in
    week:3) [ "$3" != true ] || { echo "stage_exit: not held to the seven days"; return 0; }
      t="$(epoch "$2")" || { echo "the start '$2' of the standing deployment cannot be read: refused"; return 1; }
      [ $((now - t)) -ge 604800 ] || { echo "The standing deployment is $(((now - t) / 86400)) days old: a flight is at most one a week (stage_exit only for the second flight of one candidate)."; return 1; } ;;
    same:4) if [ "$2" = true ]; then [ "$4" != - ] && [ "$3" = "$4" ] || { echo "stage_exit installs the standing candidate (${4}) again, not ${3}."; return 1; }
      elif [ "$3" = "$4" ]; then echo "${3} is the standing candidate: a second flight of it in a row needs stage_exit."; return 1; fi ;;
    due:3) [ "$2" != - ] || { echo "A flight is due: no record on main names the fresh installation of the standing deployment."; return 1; }
      files="$(git -C "$REPO_ROOT" diff --name-only "$2" "${RULES_HEAD:-origin/main}" -- infra/providers clusters/_template/bootstrap-kit scripts/expected-bootstrap-deps.yaml)" \
        && t="$(epoch "$3")" || { echo "the first-boot files since $2, or the time '$3', cannot be read: refused"; return 1; }
      if [ -z "$files" ] || [ $((now - t)) -lt 604800 ]; then echo "No flight due: $(grep -c . <<<"$files") first-boot files changed since $2; the standing deployment is $(((now - t) / 86400)) days old."; return 0; fi
      printf 'A flight is due: these first-boot files changed since %s; start release-train.yaml by hand (docs/RUNBOOKS.md section 0.8):\n%s\n' "$2" "$files"; return 1 ;;
    *) echo "usage: $0 rules week <started> <stage_exit> | same <stage_exit> <commit> <standing commit> | due <standing commit> <created>"; return 2 ;;
  esac
}

# --self-test — no network: a stand-in curl answers each call with the next "<HTTP code> <body file>" of a plan.
self_test() {
  local d failed=0 got
  d="$(mktemp -d)" || return 1
  mkdir -p "$d/bin"
  printf '%s\n' '#!/bin/sh' 'o=""; while [ $# -gt 0 ]; do [ "$1" = -o ] && { o="$2"; shift; }; u="$1"; shift; done; echo "$u" >> "$STUB_PLAN.urls"' \
    'n=$(( $(cat "$STUB_PLAN.n" 2>/dev/null || echo 0) + 1 )); echo "$n" > "$STUB_PLAN.n"' \
    'l=$(sed -n "${n}p" "$STUB_PLAN"); [ -n "$l" ] || l=$(tail -n 1 "$STUB_PLAN")' \
    '[ -n "$o" ] && cat "${l#* }" > "$o" 2>/dev/null; printf "%s" "${l%% *}"' > "$d/bin/curl"
  printf '%s\n' '#!/bin/sh' 'exit 1' > "$d/bin/kubectl"; chmod +x "$d/bin/curl" "$d/bin/kubectl"
  _tok() { echo stand-in; }
  for s in wiping wiped ready provisioning failed; do printf '{"status":"%s","lastWipeReport":{"tofuDestroyed":true}}\n' "$s" > "$d/$s"; done
  printf '{"deployments":[{"id":"aa11","status":"ready","sovereignFQDN":"t1.omani.works","startedAt":"2026-11-20T10:00:00Z"},{"id":"bb22","status":"wiped"},{"id":"cc33","status":"failed"}]}\n' > "$d/list"
  printf 'clusters:\n- cluster:\n    certificate-authority-data: QUFB\n    server: https://192.0.2.1:6443\n' > "$d/ka"
  sed 's/QUFB/QkJC/; s/192.0.2.1/192.0.2.2/' "$d/ka" > "$d/kb"
  case_() { # case_ <name> <wanted exit> <plan lines, ';' between> <command...>
    local name="$1" want="$2"; printf '%s\n' "$3" | tr ';' '\n' | sed "s|@|$d/|" > "$d/plan"; rm -f "$d/plan.n" "$d/plan.urls"; shift 3
    PATH="$d/bin:$PATH" STUB_PLAN="$d/plan" WIPE_INTERVAL=0 READY_INTERVAL=0 "$@" > "$d/out" 2>&1; got=$?
    if [ "$got" = "$want" ]; then echo "ok   $name"; else echo "FAIL $name: exit $got, want $want: $(tr '\n' ' ' < "$d/out")"; failed=1; fi
  }
  case_ "wipe-result: wiping then wiped" 0 "200 @wiping;200 @wiped" wipe_result aa11
  case_ "wipe-result: wiping then the record is gone" 0 "200 @wiping;404 @none" wipe_result aa11
  case_ "wipe-result: 404 before wiping was read is not measured" 3 "404 @none" wipe_result aa11
  case_ "wipe-result: a status other than wiping fails" 1 "200 @wiping;200 @ready" wipe_result aa11
  WIPE_TIMEOUT=0 case_ "wipe-result: still wiping at the deadline fails" 1 "200 @wiping" wipe_result aa11
  case_ "wait-ready: provisioning then ready" 0 "200 @provisioning;200 @ready" wait_ready aa11
  case_ "wait-ready: failed fails" 1 "200 @provisioning;200 @failed" wait_ready aa11
  READY_TIMEOUT=0 case_ "wait-ready: not ready at the deadline fails" 1 "200 @provisioning" wait_ready aa11
  case_ "wait-ready: no access is not measured" 3 "403 @none" wait_ready aa11
  case_ "wait-ready: no login is not measured" 3 "401 @none" wait_ready aa11
  case_ "kubeconfig: none served is not measured" 3 "404 @none" kubeconfig aa11 a
  FIRE_REQUEST='{"regions":[{"cloudRegion":"ra"},{"cloudRegion":"rb"}]}' case_ "kubeconfig b: served" 0 "200 @kb" kubeconfig aa11 b
  grep -q -x -F "${API}/aa11/kubeconfig?region=rb" "$d/plan.urls" && echo "ok   kubeconfig b: the URL names the second region" || { echo "FAIL kubeconfig b: $(cat "$d/plan.urls")"; failed=1; }
  case_ "kubeconfig-match: both files name the regions" 0 "200 @ka;200 @kb" kubeconfig_match aa11 "$d/ka" "$d/kb"
  case_ "kubeconfig-match: file b names region a" 1 "200 @ka;200 @kb" kubeconfig_match aa11 "$d/ka" "$d/ka"
  case_ "kubeconfig-match: a region the API serves no kubeconfig for is not measured" 3 "404 @none" kubeconfig_match aa11 "$d/ka" "$d/kb"
  case_ "standing: a list that cannot be read is not measured" 3 "500 @none" standing
  EVID_DIR="$d" FLIGHT_DIR="$d" case_ "wipe: an answer other than HTTP 202 is refused" 1 "404 @none;412 @none" wipe aa11 t1.omani.works
  FIRE_REQUEST='[1]' case_ "request body: a FIRE_REQUEST that is no JSON object is refused" 1 "200 @none" request_body t1 omani.works
  FIRE_REQUEST=null case_ "request body: a FIRE_REQUEST of null is refused" 1 "200 @none" request_body t1 omani.works
  local k='ssh-ed25519 AAAAstand-in t' f now old new r c1
  FIRE_REQUEST="{\"sshPublicKey\":\"$k\"}" case_ "request-body: a complete body passes" 0 "" body_checked t1 omani.works
  FIRE_REQUEST='{"sshPublicKey":""}' case_ "request-body: an empty sshPublicKey fails" 1 "" body_checked t1 omani.works
  for f in orgName orgEmail sovereignFQDN; do FIRE_REQUEST="{\"sshPublicKey\":\"$k\",\"$f\":\" \"}" case_ "request-body: an empty $f fails" 1 "" body_checked t1 omani.works; done
  for f in provider cloudRegion controlPlaneSize workerSize regions; do
    FIRE_REQUEST="$(jq -cn --arg k "$k" --arg f "$f" '{sshPublicKey: $k, regions: (if $f == "regions" then [] else [{provider: "p", cloudRegion: "r", controlPlaneSize: "c", workerSize: "w", workerCount: 1} | .[$f] = ""] end)}')" \
      case_ "request-body: an empty $f fails" 1 "" body_checked t1 omani.works
  done
  [ "$(TRAIN_SSH_PUBLIC_KEY="$k" request_body t1 omani.works | jq -r .sshPublicKey)" = "$k" ] && echo "ok   request body: the key of TRAIN_SSH_PUBLIC_KEY" || { echo "FAIL request body: TRAIN_SSH_PUBLIC_KEY"; failed=1; }
  now="$(date -u +%s)"; old="$(date -u -d "@$((now - 8 * 86400))" +%FT%TZ)"; new="$(date -u -d "@$((now - 3 * 86400))" +%FT%TZ)"
  case_ "rules week: 8 days old passes" 0 "" rules week "$old" false
  case_ "rules week: 3 days old fails" 1 "" rules week "$new" false
  case_ "rules week: 3 days old with stage_exit passes" 0 "" rules week "$new" true
  case_ "rules same: another commit than the standing candidate passes" 0 "" rules same false c2 c1
  case_ "rules same: the standing candidate again without stage_exit fails" 1 "" rules same false c1 c1
  case_ "rules same: stage_exit with another commit fails" 1 "" rules same true c2 c1
  case_ "rules same: stage_exit with no standing candidate fails" 1 "" rules same true - -
  r="$d/repo"; mkdir -p "$r/infra/providers"; git init -q "$r" 2>/dev/null; echo a > "$r/a"
  g() { git -C "$r" -c user.name=t -c user.email=t@t -c commit.gpgsign=false -c core.hooksPath=/dev/null "$@" > /dev/null 2>&1; }
  g add -A; g commit -qm 1; c1="$(git -C "$r" rev-parse HEAD)"; echo b > "$r/infra/providers/x"; g add -A; g commit -qm 2
  REPO_ROOT="$r" RULES_HEAD=HEAD case_ "rules due: no first-boot file changed passes" 0 "" rules due HEAD "$old"
  REPO_ROOT="$r" RULES_HEAD=HEAD case_ "rules due: a first-boot file changed, 8 days old, fails" 1 "" rules due "$c1" "$old"
  REPO_ROOT="$r" RULES_HEAD=HEAD case_ "rules due: a first-boot file changed, 3 days old, passes" 0 "" rules due "$c1" "$new"
  REPO_ROOT="$r" RULES_HEAD=HEAD case_ "rules due: an installation time that cannot be read fails" 1 "" rules due "$c1" -
  case_ "rules due: no record of the standing installation fails" 1 "" rules due - -
  echo "200 $d/list" > "$d/plan"; rm -f "$d/plan.n"; PATH="$d/bin:$PATH" STUB_PLAN="$d/plan" standing > "$d/out" 2>&1
  [ "$(cat "$d/out")" = "aa11 ready t1.omani.works 2026-11-20T10:00:00Z" ] && echo "ok   standing: only the live deployment, as one line" \
    || { echo "FAIL standing: $(cat "$d/out")"; failed=1; }
  [ "$(FIRE_REQUEST='{"qaTestEnabled":true,"x":"y"}' request_body t1 omani.works | jq -c '[.qaTestEnabled, .x, .sovereignFQDN]')" = '[true,"y","t1.omani.works"]' ] \
    && echo "ok   request body: the keys of FIRE_REQUEST replace and add keys" || { echo "FAIL request body"; failed=1; }
  printf '{"leftoverCount":0,"kinds":[{"kind":"servers","used":1,"leftovers":[]},{"kind":"networks","used":2,"leftovers":["n1"]},{"kind":"volumes","used":38,"leftovers":null},{"kind":"dns_records","asked":3,"leftovers":[]}]}\n' > "$d/report"
  printf '  ✅ 3c. network headroom: 2/5 used (>=2 free)\n  ✅ 3d. volume headroom: 38/400 volumes used (>=100 free)\n  ❌ 3e. bucket headroom insufficient: used=99/100\n' > "$d/log"
  got="$(preflight_value "$d/report" "$d/log" 2026-11-28T10:00:00Z)"
  [ "$got" = '2026-11-28T10:00:00Z;servers=1/-/0,networks=2/5/1,volumes=38/400/0,dns_records=-/-/0' ] \
    && echo "ok   preflight value: the report's counts, the limits of passed checks 3c and 3d, - for the rest" || { echo "FAIL preflight value: $got"; failed=1; }
  echo '{}' > "$d/report"
  preflight_value "$d/report" "$d/log" t > /dev/null 2>&1 && { echo "FAIL preflight value: a report without kinds gave a value"; failed=1; } || echo "ok   preflight value: a report without kinds gives none"
  rm -rf "$d"
  if [ "$failed" = 0 ]; then echo "self-test: every case passed"; else echo "self-test: a case failed"; fi
  return "$failed"
}

case "${1:-}" in
  fire)      shift; fire "$@" ;;
  wipe)      shift; wipe "$@" ;;
  capture)   shift; capture "$@" ;;
  sweep)     shift; sweep "$@" ;;
  reset-uat) shift; reset_uat "$@" ;;
  standing)  shift; standing "$@" ;;
  wipe-result) shift; wipe_result "$@" ;;
  wait-ready)  shift; wait_ready "$@" ;;
  kubeconfig)  shift; kubeconfig "$@" ;;
  kubeconfig-match) shift; kubeconfig_match "$@" ;;
  rules)        shift; rules "$@" ;;
  request-body) shift; body_checked "$@" ;;
  preflight)    shift; preflight "$@" ;;
  --self-test) self_test ;;
  *) echo "usage: $0 {fire <subdomain> [pool] | wipe <id> [fqdn] | capture <id> | sweep <id> | reset-uat <env>"
     echo "          | standing | wipe-result <id> | wait-ready <id> | kubeconfig <id> a|b | kubeconfig-match <id> <a> <b> | --self-test"
     echo "          | rules week|same|due ... | request-body <subdomain> [pool] | preflight <subdomain> [pool]}"
     echo "  fire needs PREV_ID=<last wiped id> with a clean sweep and never resets UAT; wipe ALWAYS captures first."; exit 2 ;;
esac
