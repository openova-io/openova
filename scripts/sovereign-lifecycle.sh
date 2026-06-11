#!/usr/bin/env bash
# Canonical Sovereign prov lifecycle — MECHANICAL enforcement of the two founder
# rules (2026-06-08). NEVER call POST /deployments or POST /wipe directly; go
# through this so the wrong order is impossible:
#   * fire  -> runs scripts/reset-uat.py FIRST, then POST /deployments
#   * wipe  -> CAPTURES the cloud-init log FIRST (GET /cloudinit-log, #3132),
#             then POST /wipe   <-- debug-before-wipe
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
}

# wipe <id> — DEBUG-BEFORE-WIPE: capture first, ALWAYS, then wipe.
wipe() {
  local id="$1"
  echo "== capture-before-wipe (founder rule 2026-06-08) =="; capture "$id"
  echo "== wiping $id =="
  curl -sk -X POST -H "Authorization: Bearer $(_tok)" -H "Content-Type: application/json" -d '{}' "${API}/${id}/wipe"
  echo "wipe submitted: $id"
}

# fire <subdomain> [pool] — RESET-UAT-ON-FIRE: reset first, ALWAYS, then fire.
fire() {
  local sub="$1" pool="${2:-omani.works}" pub body
  echo "== reset-UAT-on-fire (founder rule 2026-06-08) =="; reset_uat "$sub"
  pub=$(cat /home/openova/.ssh/*.pub 2>/dev/null | grep -E "^ssh-" | head -1)
  # SHARED_PG=true opts the prov into the ADR-0010 reusable shared-Postgres
  # model (#3188 -> SOVEREIGN_ENABLE_SHARED_PG, fire-body wired in #3275). Off by default.
  body=$(SUB="$sub" POOL="$pool" PUB="$pub" SHARED_PG="${SHARED_PG:-false}" python3 -c 'import json,os;s=os.environ["SUB"];p=os.environ["POOL"];print(json.dumps({"orgName":"Omantel","orgEmail":"emrah.baysal@openova.io","provider":"huawei","sovereignDomainMode":"pool","sovereignPoolDomain":p,"sovereignSubdomain":s,"sovereignFQDN":s+"."+p,"sshPublicKey":os.environ["PUB"],"enableSharedPostgres":os.environ.get("SHARED_PG")=="true","regions":[{"provider":"huawei","cloudRegion":"me-east-215-a","controlPlaneSize":"m7n.large.8","workerSize":"m7n.xlarge.8","workerCount":3},{"provider":"huawei","cloudRegion":"me-east-215-b","controlPlaneSize":"m7n.large.8","workerSize":"m7n.xlarge.8","workerCount":3}]}))')
  echo "== firing ${sub}.${pool} =="
  curl -sk -X POST -H "Authorization: Bearer $(_tok)" -H "Content-Type: application/json" -d "$body" "${API}"; echo
}

case "${1:-}" in
  fire)      shift; fire "$@" ;;
  wipe)      shift; wipe "$@" ;;
  capture)   shift; capture "$@" ;;
  reset-uat) shift; reset_uat "$@" ;;
  *) echo "usage: $0 {fire <subdomain> [pool] | wipe <id> | capture <id> | reset-uat <env>}"
     echo "  fire ALWAYS resets UAT first; wipe ALWAYS captures the cloud-init log first (#3132)."; exit 2 ;;
esac
