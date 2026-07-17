#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# region-kill-drill.sh — canonical, reusable Region-Kill (G12 / DoD Pillar-3)
# DR-failover drill for a live 2-region Huawei kom4dc Sovereign.
#
# Mechanics proven on hw256 (2026-07-15) + hw261 (2026-07-16):
#   batch HARD os-stop ALL region-a ECS via HCS ECS action API → region-a
#   apiserver dies (~4-5s) → promote region-b CNPG replicas by flipping the
#   region-B HelmRelease DESIRED state (spec.values.cnpgPair.replica.promoted=
#   true → chart renders replica.enabled:false), NOT a live Cluster-CR patch
#   flux drift-correction reverts mid-outage (#5125 Defect-1, bp-cnpg-pair
#   ≥0.2.12; see RUNBOOKS §6.1) → RTO ~5.7s, RPO=0 (pre-kill sentinel survives)
#   → post-kill write accepted → openbao auto-unseal via the */2 reconciler
#   CronJob (bp-openbao ≥1.2.62) → os-start region-a to recover.
#
# 🛑 SAFETY (fail-closed, layered):
#   * DRY-RUN by default. The destructive os-stop fires ONLY with --arm.
#   * The kill set is RESTRICTED to ECS whose name contains the region-a token
#     (default `-me-east-215-a-`). Servers without it are NEVER touched.
#   * The bastion is hard-excluded by name-prefix `bastion`, by EIP
#     212.72.24.20, AND by explicit id-prefix guard. NEVER stopped. (founder:
#     bastion-openova is the ONE protected Huawei resource.)
#   * If discovery finds 0 region-a ECS, or if any target matches a guard, the
#     script ABORTS rather than proceed.
#
# Usage:
#   KUBECONFIG=/tmp/hwNNN.kubeconfig HW_TFVARS=/tmp/preflight-tfvars.json \
#     scripts/region-kill-drill.sh discover                 # list kill-set (safe)
#   ... region-kill-drill.sh kill --arm                     # os-stop region-a
#   ... region-kill-drill.sh recover                        # os-start region-a
#
# Env:
#   KUBECONFIG   — the Sovereign kubeconfig (region-a control-plane).
#   HW_TFVARS    — tfvars json with huawei_access_key/secret_key/project_id/region.
#   REGION_A_TOKEN (default "-me-east-215-a-") — substring identifying region-a ECS.
# ─────────────────────────────────────────────────────────────────────────────
set -uo pipefail

HW_TFVARS="${HW_TFVARS:-/tmp/preflight-tfvars.json}"
REGION_A_TOKEN="${REGION_A_TOKEN:--me-east-215-a-}"
BASTION_EIP="212.72.24.20"
CMD="${1:-discover}"; ARM=""; [ "${2:-}" = "--arm" ] && ARM="1"
K(){ timeout 25 kubectl --request-timeout=20s "$@"; }

[ -f "$HW_TFVARS" ] || { echo "!! HW_TFVARS not found: $HW_TFVARS"; exit 2; }

# discover_region_a — print JSON array [{id,name,status}] of region-a ECS,
# with the bastion + non-region-a servers filtered OUT. Fails closed.
discover_region_a() {
  python3 - "$HW_TFVARS" "$REGION_A_TOKEN" "$BASTION_EIP" <<'PY'
import sys,json,hashlib,hmac,datetime,ssl,urllib.request
tf=json.load(open(sys.argv[1])); tok=sys.argv[2]; beip=sys.argv[3]
ak=tf.get("huawei_access_key") or tf.get("huawei_ak")
sk=tf.get("huawei_secret_key") or tf.get("huawei_sk")
proj=tf.get("huawei_project_id"); region=tf.get("huawei_region")
host=f"ecs.{region}.kom4dc.nationalcloud.om"; uri=f"/v1/{proj}/cloudservers/detail"
now=datetime.datetime.utcnow().strftime("%Y%m%dT%H%M%SZ")
sh="host;x-sdk-date"; ch=f"host:{host}\nx-sdk-date:{now}\n"
cr=f"GET\n{uri}/\n\n{ch}\n{sh}\n"+hashlib.sha256(b'').hexdigest()
sts=f"SDK-HMAC-SHA256\n{now}\n"+hashlib.sha256(cr.encode()).hexdigest()
sig=hmac.new(sk.encode(),sts.encode(),hashlib.sha256).hexdigest()
ctx=ssl.create_default_context(); ctx.check_hostname=False; ctx.verify_mode=ssl.CERT_NONE
req=urllib.request.Request(f"https://{host}{uri}",headers={"X-Sdk-Date":now,"Authorization":f"SDK-HMAC-SHA256 Access={ak}, SignedHeaders={sh}, Signature={sig}","host":host})
servers=json.load(urllib.request.urlopen(req,timeout=25,context=ctx)).get("servers",[])
out=[]
for s in servers:
    name=s.get("name",""); sid=s.get("id","")
    # LAYER 1: must be a region-a ECS (token present)
    if tok not in name: continue
    # LAYER 2: bastion hard-guard (name prefix)
    if name.startswith("bastion"): continue
    # LAYER 3: bastion hard-guard (EIP attached)
    addrs=s.get("addresses",{}) or {}
    eips=[a.get("addr") for grp in addrs.values() for a in grp if a.get("OS-EXT-IPS:type")=="floating"]
    if beip in eips: continue
    out.append({"id":sid,"name":name,"status":s.get("status")})
print(json.dumps(out))
PY
}

# ecs_action <json-action-body> — POST signed action to the ECS action API.
ecs_action() {
  python3 - "$HW_TFVARS" "$1" <<'PY'
import sys,json,hashlib,hmac,datetime,ssl,urllib.request
tf=json.load(open(sys.argv[1])); body=sys.argv[2].encode()
ak=tf.get("huawei_access_key") or tf.get("huawei_ak")
sk=tf.get("huawei_secret_key") or tf.get("huawei_sk")
proj=tf.get("huawei_project_id"); region=tf.get("huawei_region")
host=f"ecs.{region}.kom4dc.nationalcloud.om"; uri=f"/v1/{proj}/cloudservers/action"
now=datetime.datetime.utcnow().strftime("%Y%m%dT%H%M%SZ")
sh="content-type;host;x-sdk-date"; ch=f"content-type:application/json\nhost:{host}\nx-sdk-date:{now}\n"
cr=f"POST\n{uri}/\n\n{ch}\n{sh}\n"+hashlib.sha256(body).hexdigest()
sts=f"SDK-HMAC-SHA256\n{now}\n"+hashlib.sha256(cr.encode()).hexdigest()
sig=hmac.new(sk.encode(),sts.encode(),hashlib.sha256).hexdigest()
ctx=ssl.create_default_context(); ctx.check_hostname=False; ctx.verify_mode=ssl.CERT_NONE
req=urllib.request.Request(f"https://{host}{uri}",data=body,method="POST",headers={"Content-Type":"application/json","X-Sdk-Date":now,"Authorization":f"SDK-HMAC-SHA256 Access={ak}, SignedHeaders={sh}, Signature={sig}","host":host})
try:
    r=urllib.request.urlopen(req,timeout=25,context=ctx); print("HTTP",r.status,r.read().decode()[:200])
except urllib.error.HTTPError as e: print("HTTP",e.code,e.read().decode()[:300])
PY
}

targets_or_die() {
  local js; js=$(discover_region_a) || { echo "!! discovery failed"; exit 3; }
  local n; n=$(printf '%s' "$js" | python3 -c 'import sys,json;print(len(json.load(sys.stdin)))')
  [ "$n" -ge 1 ] || { echo "!! 0 region-a ECS discovered (token=$REGION_A_TOKEN) — ABORT (nothing to kill / wrong env)"; exit 3; }
  echo "$js"
}

case "$CMD" in
  discover)
    echo "== region-a kill-set (token=$REGION_A_TOKEN; bastion $BASTION_EIP hard-excluded) =="
    targets_or_die | python3 -c 'import sys,json;[print("  ",x["id"],x["name"],x["status"]) for x in json.load(sys.stdin)]'
    echo "(dry-run — no action taken; run 'kill --arm' to os-stop)"
    ;;
  kill)
    js=$(targets_or_die)
    echo "== KILL region-a (HARD os-stop) =="
    printf '%s' "$js" | python3 -c 'import sys,json;[print("  target:",x["id"],x["name"],x["status"]) for x in json.load(sys.stdin)]'
    ids=$(printf '%s' "$js" | python3 -c 'import sys,json;print(",".join(x["id"] for x in json.load(sys.stdin)))')
    body=$(printf '%s' "$js" | python3 -c 'import sys,json;s=json.load(sys.stdin);print(json.dumps({"os-stop":{"type":"HARD","servers":[{"id":x["id"]} for x in s]}}))')
    if [ -z "$ARM" ]; then echo "DRY-RUN (no --arm): would POST os-stop for [$ids]"; echo "$body"; exit 0; fi
    echo "T0=$(date -u +%Y-%m-%dT%H:%M:%S.%3NZ) — issuing HARD os-stop"
    ecs_action "$body"
    echo "-- polling region-a apiserver death --"
    for i in $(seq 1 30); do
      if ! K get --raw='/readyz' >/dev/null 2>&1; then echo "region-a apiserver DEAD at poll $i ($(date -u +%H:%M:%S.%3NZ))"; break; fi
      sleep 2
    done
    ;;
  recover)
    js=$(targets_or_die)
    body=$(printf '%s' "$js" | python3 -c 'import sys,json;s=json.load(sys.stdin);print(json.dumps({"os-start":{"servers":[{"id":x["id"]} for x in s]}}))')
    echo "== RECOVER region-a (os-start) =="
    if [ -z "$ARM" ]; then echo "DRY-RUN (no --arm): would POST os-start"; echo "$body"; exit 0; fi
    ecs_action "$body"
    ;;
  *) echo "usage: $0 {discover | kill --arm | recover --arm}"; exit 2 ;;
esac
