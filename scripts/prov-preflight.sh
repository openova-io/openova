#!/usr/bin/env bash
# prov-preflight.sh — MANDATORY pre-flight gate before ANY POST /deployments.
# Founder mandate (2026-06-30, repeated): NEVER fire a prov without passing the
# FULL checklist, and NEVER do manual ad-hoc steps. This script GATES the fire:
# it exits non-zero if any check fails, so a prov cannot be launched blind.
#
# Usage: scripts/prov-preflight.sh <FQDN> <BUCKET> [<qaTestEnabled>] [<fireCutoverOnHandover>]
#   FQDN must be a NEW incremented env (hw<NNN>.<tld>) — never reuse a failed number.
#
# Requires: COOK (catalyst_session cookie) OR BEARER (owner RS256 JWT) for the
# deployments check, + HW_TFVARS (path to tofu.auto.tfvars.json with Huawei
# AK/SK). PREFLIGHT_PROTECTED (comma-separated env stems, default hw240) names
# Sovereigns that legitimately coexist — never counted against capacity and
# NEVER to be wiped to make room (docs/PROTOCOL.md §5.0 protect-list).
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

# 2. zero other active deployments beyond the protect-list (kom4dc fits ONE
#    working prov alongside the protected production Sovereign: VPC quota 5,
#    2 VPCs per 2-region prov). Auth: BEARER (owner JWT) preferred, else COOK —
#    the list is owner-scoped either way (mint as emrah.baysal@openova.io).
if [ -n "${BEARER:-}" ]; then AUTH_HDR="Authorization: Bearer ${BEARER}"; else AUTH_HDR="Cookie: catalyst_session=${COOK:-}"; fi
act=$(curl -s -H "$AUTH_HDR" "${CONSOLE}/deployments" 2>/dev/null | PROT="${PREFLIGHT_PROTECTED:-hw240}" python3 -c '
import sys,json,os
prot=[p.strip() for p in os.environ.get("PROT","").split(",") if p.strip()]
d=json.load(sys.stdin); deps=d if isinstance(d,list) else d.get("deployments",d.get("items",[]))
live=[x for x in deps if x.get("status") not in ("wiped","deleted","failed","gone",None)]
def protected(x):
    f=str(x.get("sovereignFQDN") or x.get("fqdn") or x.get("sovereignSubdomain") or "")
    return any(f.startswith(p) for p in prot)
print(len([x for x in live if not protected(x)]))' 2>/dev/null)
[ "${act:-x}" = "0" ] && pass "2. 0 active non-protected deployments (capacity free; protect-list: ${PREFLIGHT_PROTECTED:-hw240})" || bad "2. ${act:-unknown} active non-protected deployment(s) — wipe first (sequential; NEVER wipe protect-list envs to make room)"

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

  # 3b. GROUND-TRUTH no-foreign-Sovereign gate (#4675 — the omantel.biz-loss root cause).
  #     Check #2 above trusts GET /deployments, which returns a FALSE 0 when the
  #     catalyst-api store hasn't reloaded after a recycle → the gate cleared a fire
  #     into a project still hosting a LIVE Sovereign → the tofu-init VPC-quota-reclaim
  #     (#4614) reaped it. The authoritative signal is the live cloud inventory: query
  #     Huawei ECS in the target project and FAIL if ANY node's name prefix is NOT this
  #     FQDN and NOT bastion-*. A foreign live Sovereign in the project = DO NOT FIRE.
  stem=$(printf '%s' "$FQDN" | tr '.' '-')
  foreign=$(python3 - "$HW_TFVARS" "$stem" "${PREFLIGHT_PROTECTED:-hw240}" <<'PY' 2>/dev/null
import sys,json,hashlib,hmac,datetime,urllib.request,ssl
t=json.load(open(sys.argv[1])); stem=sys.argv[2]
prot=[p.strip() for p in sys.argv[3].split(",") if p.strip()]
ak=t.get('huawei_access_key') or t.get('access_key') or t.get('huawei_ak'); sk=t.get('huawei_secret_key') or t.get('secret_key') or t.get('huawei_sk')
proj=t.get('huawei_project_id') or t.get('project_id'); region=t.get('huawei_region') or 'me-east-215'
host=f"ecs.{region}.kom4dc.nationalcloud.om"; uri=f"/v1/{proj}/cloudservers/detail"
now=datetime.datetime.utcnow().strftime('%Y%m%dT%H%M%SZ'); ch=f"host:{host}\nx-sdk-date:{now}\n"; sh="host;x-sdk-date"
cr=f"GET\n{uri}/\n\n{ch}\n{sh}\n"+hashlib.sha256(b'').hexdigest(); sts=f"SDK-HMAC-SHA256\n{now}\n"+hashlib.sha256(cr.encode()).hexdigest()
sig=hmac.new(sk.encode(),sts.encode(),hashlib.sha256).hexdigest()
ctx=ssl.create_default_context(); ctx.check_hostname=False; ctx.verify_mode=ssl.CERT_NONE
req=urllib.request.Request(f"https://{host}{uri}",headers={"X-Sdk-Date":now,"Authorization":f"SDK-HMAC-SHA256 Access={ak}, SignedHeaders={sh}, Signature={sig}","host":host})
servers=json.load(urllib.request.urlopen(req,timeout=25,context=ctx)).get("servers",[])
# foreign = any ECS not part of THIS prov's stem, not the bastion, and not a
# protect-listed Sovereign (which legitimately coexists — never wipe it).
foreign=[s.get('name','') for s in servers if stem not in s.get('name','') and not s.get('name','').startswith('bastion') and not any(s.get('name','').startswith(p) for p in prot)]
print("\n".join(foreign) if foreign else "0")
PY
)
  if [ "${foreign:-x}" = "0" ]; then pass "3b. no foreign Sovereign in project (live Huawei ECS ground-truth; protect-list excluded)"
  else bad "3b. FOREIGN live ECS in target project (a live Sovereign shares it — firing WILL risk #4614 VPC-reclaim): $(printf '%s' "$foreign" | tr '\n' ' ' | cut -c1-160)"; fi

  # 3c. VPC headroom (the ACTUAL #4614 reclaim trigger): a 2-region prov
  #     consumes 2 VPCs; kom4dc quota is 5. Require >=2 free so tofu-init
  #     never quota-reclaims a live VPC. Override quota via PREFLIGHT_VPC_QUOTA.
  vused=$(python3 - "$HW_TFVARS" <<'PY' 2>/dev/null
import sys,json,hashlib,hmac,datetime,urllib.request,ssl
t=json.load(open(sys.argv[1]))
ak=t.get('huawei_access_key') or t.get('access_key') or t.get('huawei_ak'); sk=t.get('huawei_secret_key') or t.get('secret_key') or t.get('huawei_sk')
proj=t.get('huawei_project_id') or t.get('project_id'); region=t.get('huawei_region') or 'me-east-215'
host=f"vpc.{region}.kom4dc.nationalcloud.om"; uri=f"/v1/{proj}/vpcs"
now=datetime.datetime.utcnow().strftime('%Y%m%dT%H%M%SZ'); ch=f"host:{host}\nx-sdk-date:{now}\n"; sh="host;x-sdk-date"
cr=f"GET\n{uri}/\n\n{ch}\n{sh}\n"+hashlib.sha256(b'').hexdigest(); sts=f"SDK-HMAC-SHA256\n{now}\n"+hashlib.sha256(cr.encode()).hexdigest()
sig=hmac.new(sk.encode(),sts.encode(),hashlib.sha256).hexdigest()
ctx=ssl.create_default_context(); ctx.check_hostname=False; ctx.verify_mode=ssl.CERT_NONE
req=urllib.request.Request(f"https://{host}{uri}",headers={"X-Sdk-Date":now,"Authorization":f"SDK-HMAC-SHA256 Access={ak}, SignedHeaders={sh}, Signature={sig}","host":host})
print(len(json.load(urllib.request.urlopen(req,timeout=20,context=ctx)).get("vpcs",[])))
PY
)
  vq="${PREFLIGHT_VPC_QUOTA:-5}"
  if [ -n "${vused:-}" ] && [ "$vused" -le $((vq-2)) ] 2>/dev/null; then pass "3c. VPC headroom: ${vused}/${vq} used (>=2 free for a 2-region fire)"
  else bad "3c. VPC headroom insufficient: used=${vused:-unknown}/${vq} — need >=2 free (wipe stale envs + wait ~15min release lag; NEVER reclaim live VPCs)"; fi
else bad "3. HW_TFVARS unset — cannot verify EIP/VPC headroom or foreign-Sovereign ground truth"; fi

# 4. mothership catalyst-api rollout settled
if kubectl -n catalyst rollout status deploy/catalyst-api --timeout=8s >/dev/null 2>&1; then
  pass "4. catalyst-api rollout settled"
else bad "4. catalyst-api mid-roll — wait (a roll mid-prov ABANDONS it)"; fi

# 5. NO in-flight Build-&-Deploy / blueprint-release CI (a merge mid-prov rolls catalyst-api -> abandon)
inflight=$(gh run list --repo openova-io/openova --limit 12 --json name,status -q '[.[]|select(.status!="completed")]|length' 2>/dev/null)
[ "${inflight:-x}" = "0" ] && pass "5. no in-flight CI (and HOLD all merges until ready)" || bad "5. ${inflight:-unknown} CI run(s) in flight — wait + DO NOT MERGE until prov is ready"

# 6. fresh bucket (caller asserts uniqueness)
pass "6. bucket: ${BUCKET} (caller must confirm it is fresh/unused)"

# 7. flags echoed for confirmation
echo "  ℹ 7. qaTestEnabled=${QATEST} (false=PROD certs/browsable) · fireCutoverOnHandover=${FIRECUT} (true=auto-cutover, zero-touch)"
[ "$QATEST" = "false" ] && [ "$FIRECUT" = "true" ] && pass "   → North-Star combo: browsable PROD cert + AUTONOMOUS cutover" || echo "  ⚠ not the North-Star combo (false/true) — confirm this is intentional"

echo "═══════════════════════════════════"
if [ "$fail" = "0" ]; then echo "PRE-FLIGHT PASS — clear for takeoff."; exit 0
else echo "PRE-FLIGHT FAIL — DO NOT FIRE. Fix the ❌ items first."; exit 1; fi
