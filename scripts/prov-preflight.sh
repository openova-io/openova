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
# AK/SK).
#
# 🛑 ONE ENVIRONMENT AT A TIME (founder, 2026-07-15, #5111): "You are allowed
# to create 1 environment at a time and that is the exact definition of flight
# checklist." NO Sovereign may coexist with the one being fired — the existing
# env must be COMPLETELY wiped first. PREFLIGHT_PROTECTED defaults to EMPTY:
# there is no standing protect-list of coexisting Sovereigns (the old hw240
# default was the mechanical enabler of the hw240+hw255 dual-env violation).
# The only protected resource is the bastion, which is not a deployment.
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

# 2. ZERO other active deployments — one environment at a time, NO exceptions
#    (founder 2026-07-15, #5111): the existing env must be COMPLETELY wiped
#    before any fire. Auth: BEARER (owner JWT) preferred, else COOK — the list
#    is owner-scoped either way (mint as emrah.baysal@openova.io).
if [ -n "${BEARER:-}" ]; then AUTH_HDR="Authorization: Bearer ${BEARER}"; else AUTH_HDR="Cookie: catalyst_session=${COOK:-}"; fi
act=$(curl -s -H "$AUTH_HDR" "${CONSOLE}/deployments" 2>/dev/null | PROT="${PREFLIGHT_PROTECTED:-}" python3 -c '
import sys,json,os
prot=[p.strip() for p in os.environ.get("PROT","").split(",") if p.strip()]
d=json.load(sys.stdin); deps=d if isinstance(d,list) else d.get("deployments",d.get("items",[]))
live=[x for x in deps if x.get("status") not in ("wiped","deleted","failed","gone",None)]
def protected(x):
    f=str(x.get("sovereignFQDN") or x.get("fqdn") or x.get("sovereignSubdomain") or "")
    return any(f.startswith(p) for p in prot)
print(len([x for x in live if not protected(x)]))' 2>/dev/null)
[ "${act:-x}" = "0" ] && pass "2. 0 other active deployments (one-env-at-a-time holds)" || bad "2. ${act:-unknown} active deployment(s) still live — COMPLETELY wipe the existing env first (ONE environment at a time, founder 2026-07-15; #5111)"

# 3. EIP headroom: no genuinely-orphaned EIPs (status=DOWN + no port binding).
#    ELB-type EIPs report NO port_id even when serving a live gateway/console
#    ELB (their binding is to the ELB, status=ELB) — a port_id-only heuristic
#    flags a healthy prov's front-door EIPs as orphans and invites deleting the
#    live gateway (near-miss on hw250, 2026-07-13). Only status=DOWN counts.
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
orphaned=len([e for e in eips if e.get("public_ip_address")!=bastion and not e.get("port_id") and e.get("status")=="DOWN"])
# 3f (#5126): ANY non-bastion EIP still allocated in the project means the
# prior env's EIPs have NOT fully released to the shared kom4dc pool. Firing
# now risks VPC.0532 'EIP pool sold out' on tofu apply (hw257 Phase-0 death,
# 2026-07-15): check 3's orphan-only count passed clean while hw256's
# releasing EIPs starved the pool. Emit both counts so the caller sees the
# release-lag distinctly from a genuine orphan.
nonbastion=len([e for e in eips if e.get("public_ip_address")!=bastion])
print(f"{orphaned} {nonbastion}")
PY
)
  read -r orph nonbastion <<<"${orph:-x x}"
  [ "${orph:-x}" = "0" ] && pass "3. EIP headroom: 0 orphaned EIPs (status=DOWN; ELB-bound excluded)" || bad "3. ${orph} orphaned EIP(s) (status=DOWN, unbound) — delete them first (NEVER touch status=ELB/ACTIVE or bastion ${BASTION_EIP})"
  [ "${nonbastion:-x}" = "0" ] && pass "3f. EIP pool recovered: project holds ONLY the bastion EIP (no wipe→fire release lag)" || bad "3f. ${nonbastion:-unknown} non-bastion EIP(s) still allocated — prior env's EIPs not fully released to the shared pool; firing risks VPC.0532 'EIP pool sold out' (#5126). Wait ~10-15min for release."

  # 3b. GROUND-TRUTH no-foreign-Sovereign gate (#4675 — the omantel.biz-loss root cause).
  #     Check #2 above trusts GET /deployments, which returns a FALSE 0 when the
  #     catalyst-api store hasn't reloaded after a recycle → the gate cleared a fire
  #     into a project still hosting a LIVE Sovereign → the tofu-init VPC-quota-reclaim
  #     (#4614) reaped it. The authoritative signal is the live cloud inventory: query
  #     Huawei ECS in the target project and FAIL if ANY node's name prefix is NOT this
  #     FQDN and NOT bastion-*. A foreign live Sovereign in the project = DO NOT FIRE.
  stem=$(printf '%s' "$FQDN" | tr '.' '-')
  foreign=$(python3 - "$HW_TFVARS" "$stem" "${PREFLIGHT_PROTECTED:-}" <<'PY' 2>/dev/null
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
# foreign = any ECS not part of THIS prov's stem and not the bastion. One env
# at a time (#5111): another Sovereign's nodes here mean wipe-first was skipped.
foreign=[s.get('name','') for s in servers if stem not in s.get('name','') and not s.get('name','').startswith('bastion') and not any(s.get('name','').startswith(p) for p in prot)]
print("\n".join(foreign) if foreign else "0")
PY
)
  if [ "${foreign:-x}" = "0" ]; then pass "3b. no foreign Sovereign in project (live Huawei ECS ground-truth)"
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

  # 3d. EVS volume headroom (#5028 fire-side gate): kom4dc caps 400 EVS volumes
  #     project-wide; a fresh 2-region prov dynamically provisions ~70-100 CSI
  #     pvc-* volumes on top of its IaC disks. hw244 wedged mid-convergence at
  #     332/400 (HTTP 413 VolumeLimitExceeded, keycloak CrashLoop, 8 PVCs
  #     Pending). Wipe-side legs landed (#4678 drain, #5029 backstop); this is
  #     the refuse-to-fire half: require PREFLIGHT_EVS_HEADROOM (default 120)
  #     free of PREFLIGHT_EVS_QUOTA (default 400).
  eused=$(python3 - "$HW_TFVARS" <<'PY' 2>/dev/null
import sys,json,hashlib,hmac,datetime,urllib.request,ssl
t=json.load(open(sys.argv[1]))
ak=t.get('huawei_access_key') or t.get('access_key') or t.get('huawei_ak'); sk=t.get('huawei_secret_key') or t.get('secret_key') or t.get('huawei_sk')
proj=t.get('huawei_project_id') or t.get('project_id'); region=t.get('huawei_region') or 'me-east-215'
host=f"evs.{region}.kom4dc.nationalcloud.om"
total=0; marker=""
for _ in range(10):
    uri=f"/v2/{proj}/cloudvolumes"; q=f"?limit=500{('&marker='+marker) if marker else ''}"
    now=datetime.datetime.utcnow().strftime('%Y%m%dT%H%M%SZ'); ch=f"host:{host}\nx-sdk-date:{now}\n"; sh="host;x-sdk-date"
    cr=f"GET\n{uri}/\n{q[1:] if q else ''}\n{ch}\n{sh}\n"+hashlib.sha256(b'').hexdigest()
    sts=f"SDK-HMAC-SHA256\n{now}\n"+hashlib.sha256(cr.encode()).hexdigest()
    sig=hmac.new(sk.encode(),sts.encode(),hashlib.sha256).hexdigest()
    ctx=ssl.create_default_context(); ctx.check_hostname=False; ctx.verify_mode=ssl.CERT_NONE
    req=urllib.request.Request(f"https://{host}{uri}{q}",headers={"X-Sdk-Date":now,"Authorization":f"SDK-HMAC-SHA256 Access={ak}, SignedHeaders={sh}, Signature={sig}","host":host})
    vols=json.load(urllib.request.urlopen(req,timeout=25,context=ctx)).get("volumes",[])
    total+=len(vols)
    if len(vols)<500: break
    marker=vols[-1].get("id","")
    if not marker: break
print(total)
PY
)
  eq="${PREFLIGHT_EVS_QUOTA:-400}"; eh="${PREFLIGHT_EVS_HEADROOM:-120}"
  if [ -n "${eused:-}" ] && [ "$eused" -le $((eq-eh)) ] 2>/dev/null; then pass "3d. EVS headroom: ${eused}/${eq} volumes used (>=${eh} free for the fresh prov's CSI volumes)"
  else bad "3d. EVS headroom insufficient: used=${eused:-unknown}/${eq}, need >=${eh} free — wipe stale envs (drain #4678 + backstop #5029 reclaim their volumes) BEFORE firing (#5028 hw244 413-wedge)"; fi

  # 3e. OBS bucket-quota headroom (#5078 fire-side gate): kom4dc caps 100 OBS
  #     buckets project-wide; every fire creates 1 (the Velero archive bucket)
  #     and tofu-destroy deliberately leaves it (no force_destroy — it holds
  #     the backup archive) → every wiped env orphans a bucket until the
  #     project saturates. hw252 (dep 281715eb) died at Phase-0 with
  #     `Code=TooManyBuckets` at 100/100 (80 catalyst-hw* orphans). Require
  #     PREFLIGHT_OBS_HEADROOM (default 3) free of PREFLIGHT_OBS_QUOTA
  #     (default 100). NOTE: OBS is NOT the SDK-HMAC-SHA256 plane the
  #     EVS/VPC/ECS checks use — it takes S3-style OBS SigV2 auth
  #     (`Authorization: OBS <ak>:<base64(hmac-sha1(sk, StringToSign))>`;
  #     idiom shared with scripts/reclaim-obs-orphans.sh). The endpoint
  #     (obs.<region>.kom4dc.nationalcloud.om, private CA) is private HCS —
  #     resolvable from the mothership, NOT from dev sandboxes — so
  #     unreachability only WARNS (⚠ SKIPPED): the gate fails solely on a
  #     positive over-quota observation or an authenticated error reply.
  oused=$(python3 - "$HW_TFVARS" <<'PY' 2>/dev/null
import sys,json,os,base64,hmac,hashlib,ssl,socket,urllib.request,urllib.error
from email.utils import formatdate
t=json.load(open(sys.argv[1]))
ak=t.get('huawei_access_key') or t.get('access_key') or t.get('huawei_ak'); sk=t.get('huawei_secret_key') or t.get('secret_key') or t.get('huawei_sk')
region=t.get('huawei_region') or 'me-east-215'
stub=os.environ.get("OBS_STUB_FILE")  # test hook (#5078): canned ListAllMyBuckets XML, no network
if stub:
    body=open(stub,'rb').read()
else:
    host=f"obs.{region}.kom4dc.nationalcloud.om"
    date=formatdate(usegmt=True)
    sts=f"GET\n\n\n{date}\n/"
    sig=base64.b64encode(hmac.new(sk.encode(),sts.encode(),hashlib.sha1).digest()).decode()
    ctx=ssl.create_default_context(); ctx.check_hostname=False; ctx.verify_mode=ssl.CERT_NONE
    req=urllib.request.Request(f"https://{host}/",headers={"Date":date,"Authorization":f"OBS {ak}:{sig}","Host":host})
    try:
        body=urllib.request.urlopen(req,timeout=20,context=ctx).read()
    except urllib.error.HTTPError as e:
        print(f"ERR HTTP {e.code}"); sys.exit(0)
    except (urllib.error.URLError,socket.timeout,OSError) as e:
        print(f"SKIP {getattr(e,'reason',e)}"); sys.exit(0)
print(body.count(b"<Bucket>"))
PY
)
  oq="${PREFLIGHT_OBS_QUOTA:-100}"; oh="${PREFLIGHT_OBS_HEADROOM:-3}"
  case "${oused:-}" in
    SKIP*)
      echo "  ⚠ 3e SKIPPED (OBS endpoint unreachable from here — ${oused#SKIP }); private HCS plane — re-run from the mothership for the authoritative bucket count" ;;
    *)
      if [ -n "${oused:-}" ] && [ "$oused" -le $((oq-oh)) ] 2>/dev/null; then pass "3e. OBS bucket headroom: ${oused}/${oq} buckets used (>=${oh} free; a fire creates 1)"
      else bad "3e. OBS bucket headroom insufficient: used=${oused:-unknown}/${oq}, need >=${oh} free — reclaim orphaned catalyst-hw* buckets first (version-aware: scripts/reclaim-obs-orphans.sh; #5078 hw252 Phase-0 TooManyBuckets wedge)"; fi ;;
  esac
else bad "3. HW_TFVARS unset — cannot verify EIP/VPC/EVS/OBS headroom or foreign-Sovereign ground truth"; fi

# 4. mothership catalyst-api rollout settled
if kubectl -n catalyst rollout status deploy/catalyst-api --timeout=8s >/dev/null 2>&1; then
  pass "4. catalyst-api rollout settled"
else bad "4. catalyst-api mid-roll — wait (a roll mid-prov ABANDONS it)"; fi

# 4b. mothership catalyst-api DEPLOYED image == origin/main PINNED tag (no roll
#     PENDING). hw265 died because check-4 "settled" is POINT-IN-TIME: at fire
#     time catalyst-api was 1/1 on the OLD image, then Flux reconciled the
#     already-merged deploybot image-bump a few minutes later — MID-tofu-apply —
#     and the restart abandoned the prov (reference_deploybot_catalyst_api_roll_
#     mid_fire_abandons_prov). Gate on deployed==pinned so no roll can land during
#     the prov: they diverge ONLY when a deploybot bump is merged but not yet
#     reconciled onto the mothership.
git fetch origin main --quiet 2>/dev/null || true
_deployed_tag=$(kubectl -n catalyst get deploy catalyst-api \
  -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null | sed 's/.*://')
_pinned_tag=$(git show origin/main:products/catalyst/chart/values.yaml 2>/dev/null | python3 -c '
import sys,yaml
try:
    print((yaml.safe_load(sys.stdin) or {}).get("images",{}).get("catalystApi",{}).get("tag",""))
except Exception:
    print("")' 2>/dev/null)
if [ -z "$_deployed_tag" ] || [ -z "$_pinned_tag" ]; then
  bad "4b. could not read catalyst-api deployed/pinned tag (deployed='${_deployed_tag}' pinned='${_pinned_tag}') — verify the mothership is on the pinned image before firing"
elif [ "$_deployed_tag" = "$_pinned_tag" ]; then
  pass "4b. catalyst-api on the pinned image (${_deployed_tag}) — NO roll pending mid-prov"
else
  bad "4b. catalyst-api DEPLOYED=${_deployed_tag} != PINNED=${_pinned_tag} — a deploybot roll is PENDING; it WILL reconcile mid-prov and ABANDON it (hw265). Wait for Flux to roll catalyst-api to the pinned image + settle, THEN fire."
fi

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
