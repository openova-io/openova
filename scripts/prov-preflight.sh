#!/usr/bin/env bash
# prov-preflight.sh — MANDATORY pre-flight gate before ANY POST /deployments.
# Founder mandate (2026-06-30, repeated): NEVER fire a prov without passing the
# FULL checklist, and NEVER do manual ad-hoc steps. This script GATES the fire:
# it exits non-zero if any check fails, so a prov cannot be launched blind.
#
# Usage: scripts/prov-preflight.sh <FQDN> <BUCKET> [<qaTestEnabled>] [<fireCutoverOnHandover>]
#   FQDN must be a NEW incremented env (hw<NNN>.<tld>) — never reuse a failed number.
#
# Requires: COOK (catalyst_session cookie) + HW_TFVARS (path to tofu.auto.tfvars.json
# with Huawei AK/SK) in the environment.
set -uo pipefail
FQDN="${1:?need FQDN}"; BUCKET="${2:?need BUCKET}"
QATEST="${3:-false}"; FIRECUT="${4:-true}"
CONSOLE="https://console.openova.io/sovereign/api/v1"
BASTION_EIP="212.72.24.20"
fail=0; pass(){ echo "  ✅ $1"; }; bad(){ echo "  ❌ $1"; fail=1; }

echo "═══ PROV PRE-FLIGHT for ${FQDN} ═══"

# 1. incremented FQDN (caller asserts; we record it so it's visible)
case "$FQDN" in hw[0-9]*) pass "1. FQDN is hw-numbered: $FQDN (caller must confirm it is a NEW number, never a failed one)";;
  *) bad "1. FQDN '$FQDN' is not hw<NNN>.<tld> — increment the env number";; esac

# 2. zero other active deployments (kom4dc fits ONE prov: VPC+EIP quota)
act=$(curl -s -H "Cookie: catalyst_session=${COOK}" "${CONSOLE}/deployments" 2>/dev/null | python3 -c '
import sys,json
d=json.load(sys.stdin); deps=d if isinstance(d,list) else d.get("deployments",d.get("items",[]))
print(len([x for x in deps if x.get("status") not in ("wiped","deleted","failed","gone",None)]))' 2>/dev/null)
[ "$act" = "0" ] && pass "2. 0 active deployments (capacity free)" || bad "2. ${act} active deployment(s) — wipe first (sequential)"

# 3. EIP headroom: only the bastion may remain; no DOWN/unbound orphans
if [ -n "${HW_TFVARS:-}" ] && [ -s "${HW_TFVARS}" ]; then
  orph=$(python3 - "$HW_TFVARS" "$BASTION_EIP" <<'PY' 2>/dev/null
import sys,json,hashlib,hmac,datetime,urllib.request,ssl
t=json.load(open(sys.argv[1])); bastion=sys.argv[2]
ak=t.get('huawei_access_key') or t.get('access_key') or t.get('huawei_ak'); sk=t.get('huawei_secret_key') or t.get('secret_key') or t.get('huawei_sk')
proj=t.get('huawei_project_id') or t.get('project_id'); region=t.get('huawei_region') or 'me-east-215'
host=f"vpc.{region}.kom4dc.nationalcloud.om"; uri=f"/v1/{proj}/publicips"
now=datetime.datetime.utcnow().strftime('%Y%m%dT%H%M%SZ'); ch=f"host:{host}\nx-sdk-date:{now}\n"; sh="host;x-sdk-date"
cr=f"GET\n{uri}/\n\n{ch}\n{sh}\n"+hashlib.sha256(b'').hexdigest(); sts=f"SDK-HMAC-SHA256\n{now}\n"+hashlib.sha256(cr.encode()).hexdigest()
sig=hmac.new(sk.encode(),sts.encode(),hashlib.sha256).hexdigest()
ctx=ssl.create_default_context(); ctx.check_hostname=False; ctx.verify_mode=ssl.CERT_NONE
req=urllib.request.Request(f"https://{host}{uri}",headers={"X-Sdk-Date":now,"Authorization":f"SDK-HMAC-SHA256 Access={ak}, SignedHeaders={sh}, Signature={sig}","host":host})
eips=json.load(urllib.request.urlopen(req,timeout=20,context=ctx)).get("publicips",[])
print(len([e for e in eips if e.get("public_ip_address")!=bastion and not e.get("port_id")]))
PY
)
  [ "${orph:-x}" = "0" ] && pass "3. EIP headroom: 0 orphaned EIPs (only bastion)" || bad "3. ${orph} orphaned/unbound EIP(s) — delete them first (exclude bastion ${BASTION_EIP})"
else bad "3. HW_TFVARS unset — cannot verify EIP headroom"; fi

# 4. mothership catalyst-api rollout settled
if kubectl -n catalyst rollout status deploy/catalyst-api --timeout=8s >/dev/null 2>&1; then
  pass "4. catalyst-api rollout settled"
else bad "4. catalyst-api mid-roll — wait (a roll mid-prov ABANDONS it)"; fi

# 5. NO in-flight Build-&-Deploy / blueprint-release CI (a merge mid-prov rolls catalyst-api -> abandon)
inflight=$(gh run list --repo openova-io/openova --limit 12 --json name,status -q '[.[]|select(.status!="completed")]|length' 2>/dev/null)
[ "${inflight:-x}" = "0" ] && pass "5. no in-flight CI (and HOLD all merges until ready)" || bad "5. ${inflight} CI run(s) in flight — wait + DO NOT MERGE until prov is ready"

# 6. fresh bucket (caller asserts uniqueness)
pass "6. bucket: ${BUCKET} (caller must confirm it is fresh/unused)"

# 7. flags echoed for confirmation
echo "  ℹ 7. qaTestEnabled=${QATEST} (false=PROD certs/browsable) · fireCutoverOnHandover=${FIRECUT} (true=auto-cutover, zero-touch)"
[ "$QATEST" = "false" ] && [ "$FIRECUT" = "true" ] && pass "   → North-Star combo: browsable PROD cert + AUTONOMOUS cutover" || echo "  ⚠ not the North-Star combo (false/true) — confirm this is intentional"

echo "═══════════════════════════════════"
if [ "$fail" = "0" ]; then echo "PRE-FLIGHT PASS — clear for takeoff."; exit 0
else echo "PRE-FLIGHT FAIL — DO NOT FIRE. Fix the ❌ items first."; exit 1; fi
