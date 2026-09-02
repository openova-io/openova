#!/usr/bin/env bash
# wipe-recordless-env.sh — BREAK-GLASS teardown of a record-less kom4dc
# Sovereign env's live HCS resources. Refs #5328.
#
# ─────────────────────────────────────────────────────────────────────────────
# WHEN THIS IS ALLOWED (read docs/RUNBOOKS.md §0.7 first)
# ─────────────────────────────────────────────────────────────────────────────
# The canonical wipe endpoint `POST /sovereign/api/v1/deployments/{id}/wipe`
# (docs/PROTOCOL.md §5, RUNBOOKS §0.6) is the ONLY default wipe path. This tool
# is BREAK-GLASS ONLY — usable solely when the canonical wipe is structurally
# impossible: the mothership deployment RECORD 404s AND its tofu state is gone,
# so `tofu destroy` cannot run, yet the env's live kom4dc resources must still
# be torn down before the next fire (one-environment-at-a-time, #5111).
#
# It ONLY deletes; it creates nothing. No NodePorts anywhere (standing rule).
# It does NOT replace the durable server-side lanes #5193 (wipe-path creds
# fallback) / #5285 (informer flood) — it is the last-resort manual teardown.
#
# ─────────────────────────────────────────────────────────────────────────────
# SCOPE (fail-closed) + HARD FENCE
# ─────────────────────────────────────────────────────────────────────────────
# A resource is a candidate ONLY if its name contains BOTH mandatory tokens:
#   (a) the env subdomain token   e.g. hw284-omani-works
#   (b) the 8-char dep-id prefix  e.g. 383db23c
# Both are positional args; empty/malformed values abort before any API call.
#
# HARD FENCE asserted at EVERY delete — refuses regardless of scope match:
#   - name contains `bastion-openova` or `bastion-openova-vpc`
#   - EIP == 212.72.24.20 (bastion EIP)
#   - the two scope tokens are not BOTH present
# The ONLY resources deletable WITHOUT the dep-id token are the two orphan
# classes under --reap-orphans (see below), and even those keep the bastion
# fence + the zero-active-envs precondition.
#
# ─────────────────────────────────────────────────────────────────────────────
# TEARDOWN ORDER (the proven hw284 order — kom4dc quirks baked in)
# ─────────────────────────────────────────────────────────────────────────────
#   1. ELB v3:  healthmonitor → members → pool → listener → loadbalancer
#               (cascade delete unsupported → ELB.8902; pool delete 409s while
#                its healthmonitor exists → healthmonitor FIRST).
#   2. NAT:     snat_rules (nested) → nat_gateways.
#   3. VPC peerings of the scoped VPCs.
#   4. ECS:     POST /v1/{proj}/cloudservers/delete {delete_publicip:true,
#               delete_volume:true} — also removes the attached CSI EVS volumes.
#   5. EVS:     remaining status=available scoped volumes.
#   6. Subnets: DELETE /v1/{proj}/vpcs/{vpc}/subnets/{id} (retry on 409 while
#               ports release).
#   7. Security groups.
#   8. VPCs.
#   9. EIP release.
# Rate-limit discipline: kom4dc black-lists rapid calls (SYS.0429) — pace ~2s
# between deletes, back off 12s on 429, retry up to 5x.
#
# ─────────────────────────────────────────────────────────────────────────────
# USAGE
# ─────────────────────────────────────────────────────────────────────────────
#   HW_TFVARS=/deps/tofu/<dep-id>/tofu.auto.tfvars.json \
#     bash scripts/wipe-recordless-env.sh <ENV_SUBDOMAIN_TOKEN> <DEPID_PREFIX8> [flags]
#
# Positional (both mandatory):
#   ENV_SUBDOMAIN_TOKEN   e.g. hw284-omani-works
#   DEPID_PREFIX8         8-char dep-id prefix, e.g. 383db23c
#
# Flags:
#   (none)          DRY-RUN — enumerate + print what WOULD be deleted; delete NOTHING.
#   --apply         actually delete (in the order above, with pacing/backoff).
#   --reap-orphans  ALSO delete the two orphan classes: status=DOWN unbound EIPs
#                   and status=available pvc-* EVS volumes — ONLY when the
#                   mothership deployments list shows ZERO active envs (fail-closed
#                   if the list cannot be fetched). Requires CONSOLE + AUTH_HDR env.
#   --self-test     offline self-test of the scope truth-table + fence refusals +
#                   fail-closed arg validation. No network. Exits 0 on pass.
#
# Env:
#   HW_TFVARS   tofu.auto.tfvars.json carrying huawei_access_key / huawei_secret_key
#               / huawei_project_id / huawei_region (any prior prov's — mothership
#               PVC catalyst-api-deployments, /deps/tofu/<dep>/). Required except
#               under --self-test.
#   CONSOLE     mothership base, default https://console.openova.io/sovereign/api/v1
#   AUTH_HDR    owner auth header for the deployments list (--reap-orphans only),
#               e.g. "Authorization: Bearer <jwt>" or "Cookie: catalyst_session=<c>".
#
# kom4dc HCS endpoints (private CA → TLS verify off, same as prov-preflight.sh)
# are reachable from mothership/bastion, NOT from dev sandboxes — so a live
# dry-run/apply is a mothership-vantage action; --self-test runs anywhere.
#
# Auth: SDK-HMAC-SHA256 exactly per scripts/prov-preflight.sh; hosts
#   {ecs,vpc,elb,nat,evs}.{region}.kom4dc.nationalcloud.om.
set -euo pipefail

# ── The immutable fence constants (never overridable) ────────────────────────
BASTION_NAME_1="bastion-openova"
BASTION_NAME_2="bastion-openova-vpc"
BASTION_EIP="212.72.24.20"

APPLY=0
REAP=0
SELFTEST=0
POS=()
for a in "$@"; do case "$a" in
  --apply)        APPLY=1 ;;
  --reap-orphans) REAP=1 ;;
  --self-test)    SELFTEST=1 ;;
  --*) echo "unknown flag: $a" >&2; exit 2 ;;
  *)   POS+=("$a") ;;
esac; done

# ── Offline self-test (no network, no creds) ─────────────────────────────────
if [ "$SELFTEST" = "1" ]; then
  BASTION_NAME_1="$BASTION_NAME_1" BASTION_NAME_2="$BASTION_NAME_2" \
  BASTION_EIP="$BASTION_EIP" python3 - <<'PY'
import os,sys
B1=os.environ["BASTION_NAME_1"]; B2=os.environ["BASTION_NAME_2"]; BEIP=os.environ["BASTION_EIP"]

# The exact predicate the live path asserts at every delete.
def in_scope(name, env_token, dep_prefix):
    if not env_token or not dep_prefix: return False
    return (env_token in name) and (dep_prefix in name)

def fenced(name, eip=None):
    # True == PROTECTED, must never be deleted.
    if name and (B1 in name or B2 in name): return True
    if eip and eip == BEIP: return True
    return False

def deletable(name, env_token, dep_prefix, eip=None):
    return in_scope(name, env_token, dep_prefix) and not fenced(name, eip)

ENV="hw284-omani-works"; DEP="383db23c"
fails=0
def check(desc, got, want):
    global fails
    ok = (got == want)
    print(f"  [{'PASS' if ok else 'FAIL'}] {desc}: got={got} want={want}")
    if not ok: fails+=1

print("== scope truth-table ==")
check("both tokens present -> deletable",
      deletable(f"cnpg-{ENV}-{DEP}-me-east-215-a-w1", ENV, DEP), True)
check("env token only (no dep-id) -> NOT deletable",
      deletable(f"cnpg-{ENV}-me-east-215-a-w1", ENV, DEP), False)
check("dep-id only (no env token) -> NOT deletable",
      deletable(f"cnpg-{DEP}-me-east-215-a-w1", ENV, DEP), False)
check("neither token -> NOT deletable",
      deletable("some-other-env-abc12345-w1", ENV, DEP), False)
check("wrong env token (hw28 vs hw284) substring guard is caller's token exactness",
      deletable(f"cnpg-hw28-omani-works-{DEP}-w1", ENV, DEP), False)

print("== hard fence (must refuse even if scoped) ==")
check("bastion-openova name -> fenced",
      deletable(f"{B1}", ENV, DEP), False)
check("bastion-openova-vpc name -> fenced",
      deletable(f"{B2}", ENV, DEP), False)
check("bastion-named ECS even WITH scope tokens -> fenced",
      deletable(f"{B1}-{ENV}-{DEP}", ENV, DEP), False)
check("bastion EIP 212.72.24.20 -> fenced",
      deletable(f"eip-{ENV}-{DEP}", ENV, DEP, eip=BEIP), False)
check("non-bastion scoped EIP -> deletable",
      deletable(f"eip-{ENV}-{DEP}", ENV, DEP, eip="1.2.3.4"), True)

print("== fail-closed arg validation ==")
check("empty env token -> not deletable", deletable(f"x-{DEP}", "", DEP), False)
check("empty dep prefix -> not deletable", deletable(f"{ENV}-x", ENV, ""), False)

print(f"\nself-test: {'PASS' if fails==0 else 'FAIL'} ({fails} failure(s))")
sys.exit(1 if fails else 0)
PY
  exit $?
fi

# ── Positional arg validation (fail-closed, before ANY API call) ─────────────
if [ "${#POS[@]}" -ne 2 ]; then
  echo "usage: HW_TFVARS=/path/tofu.auto.tfvars.json bash $0 <ENV_SUBDOMAIN_TOKEN> <DEPID_PREFIX8> [--apply] [--reap-orphans]" >&2
  echo "       bash $0 --self-test" >&2
  exit 2
fi
ENV_TOKEN="${POS[0]}"
DEP_PREFIX="${POS[1]}"
# env token: hyphenated subdomain form, >= 4 chars, no whitespace.
if ! printf '%s' "$ENV_TOKEN" | grep -qE '^[a-z0-9]([a-z0-9-]{2,})[a-z0-9]$'; then
  echo "FATAL: ENV_SUBDOMAIN_TOKEN '${ENV_TOKEN}' malformed (expect e.g. hw284-omani-works)" >&2
  exit 2
fi
# dep-id prefix: EXACTLY 8 lowercase hex chars.
if ! printf '%s' "$DEP_PREFIX" | grep -qE '^[0-9a-f]{8}$'; then
  echo "FATAL: DEPID_PREFIX8 '${DEP_PREFIX}' must be exactly 8 lowercase hex chars (e.g. 383db23c)" >&2
  exit 2
fi

if [ -z "${HW_TFVARS:-}" ] || [ ! -s "${HW_TFVARS}" ]; then
  echo "FATAL: HW_TFVARS unset or empty — need tofu.auto.tfvars.json with Huawei AK/SK/project/region" >&2
  exit 2
fi

CONSOLE="${CONSOLE:-https://console.openova.io/sovereign/api/v1}"
MODE="DRY-RUN"; [ "$APPLY" = "1" ] && MODE="APPLY"
echo "wipe-recordless-env: env=${ENV_TOKEN} dep=${DEP_PREFIX} mode=${MODE} reap-orphans=${REAP}"
echo "  fence: NEVER ${BASTION_NAME_1}/${BASTION_NAME_2}/EIP ${BASTION_EIP}; candidate ⇔ name has BOTH '${ENV_TOKEN}' AND '${DEP_PREFIX}'"

# ── --reap-orphans precondition: zero active envs (fail-closed) ───────────────
if [ "$REAP" = "1" ]; then
  if [ -z "${AUTH_HDR:-}" ]; then
    echo "FATAL: --reap-orphans requires AUTH_HDR (owner auth for ${CONSOLE}/deployments) to prove zero active envs" >&2
    exit 2
  fi
  ACTIVE=$(curl -s -m 25 -H "${AUTH_HDR}" "${CONSOLE}/deployments" 2>/dev/null | python3 -c '
import sys,json
try:
    d=json.load(sys.stdin)
except Exception:
    print("ERR"); sys.exit(0)
deps=d if isinstance(d,list) else d.get("deployments",d.get("items",[]))
live=[x for x in deps if x.get("status") not in ("wiped","deleted","failed","gone",None)]
print(len(live))
' 2>/dev/null || echo ERR)
  if [ "${ACTIVE}" != "0" ]; then
    echo "FATAL: --reap-orphans fail-closed — deployments list is '${ACTIVE}' (need exactly 0 active envs; unreadable ⇒ refuse)" >&2
    exit 2
  fi
  echo "  --reap-orphans: verified 0 active envs — orphan classes (DOWN EIPs, available pvc-* EVS) eligible"
fi

# ── Live enumerate + teardown (single embedded python, house style) ──────────
HW_TFVARS="$HW_TFVARS" ENV_TOKEN="$ENV_TOKEN" DEP_PREFIX="$DEP_PREFIX" \
APPLY="$APPLY" REAP="$REAP" \
BASTION_NAME_1="$BASTION_NAME_1" BASTION_NAME_2="$BASTION_NAME_2" BASTION_EIP="$BASTION_EIP" \
python3 - <<'PY'
import os,sys,json,time,hashlib,hmac,datetime,ssl,urllib.request,urllib.error

TF=json.load(open(os.environ["HW_TFVARS"]))
def tf(*keys):
    for k in keys:
        if TF.get(k): return TF[k]
    return None
AK=tf("huawei_access_key","access_key","huawei_ak")
SK=tf("huawei_secret_key","secret_key","huawei_sk")
PROJ=tf("huawei_project_id","project_id")
REGION=tf("huawei_region","region") or "me-east-215"
if not (AK and SK and PROJ):
    print("FATAL: tofu.auto.tfvars.json missing huawei_access_key/secret_key/project_id", file=sys.stderr); sys.exit(2)

ENV_TOKEN=os.environ["ENV_TOKEN"]; DEP_PREFIX=os.environ["DEP_PREFIX"]
APPLY=os.environ["APPLY"]=="1"; REAP=os.environ["REAP"]=="1"
B1=os.environ["BASTION_NAME_1"]; B2=os.environ["BASTION_NAME_2"]; BEIP=os.environ["BASTION_EIP"]

CTX=ssl.create_default_context(); CTX.check_hostname=False; CTX.verify_mode=ssl.CERT_NONE
def host(svc): return f"{svc}.{REGION}.kom4dc.nationalcloud.om"

def signed(svc, method, uri, body=None):
    """SDK-HMAC-SHA256 signed request, exactly per prov-preflight.sh."""
    h=host(svc); now=datetime.datetime.utcnow().strftime('%Y%m%dT%H%M%SZ')
    payload=b'' if body is None else json.dumps(body).encode()
    ch=f"host:{h}\nx-sdk-date:{now}\n"; sh="host;x-sdk-date"
    cr=f"{method}\n{uri}/\n\n{ch}\n{sh}\n"+hashlib.sha256(payload).hexdigest()
    sts=f"SDK-HMAC-SHA256\n{now}\n"+hashlib.sha256(cr.encode()).hexdigest()
    sig=hmac.new(SK.encode(),sts.encode(),hashlib.sha256).hexdigest()
    hdrs={"X-Sdk-Date":now,"host":h,
          "Authorization":f"SDK-HMAC-SHA256 Access={AK}, SignedHeaders={sh}, Signature={sig}"}
    if body is not None: hdrs["Content-Type"]="application/json"
    req=urllib.request.Request(f"https://{h}{uri}",data=payload,headers=hdrs,method=method)
    resp=urllib.request.urlopen(req,timeout=25,context=CTX)
    raw=resp.read()
    return resp.status, (json.loads(raw) if raw else {})

def get(svc, uri):
    try:
        st,body=signed(svc,"GET",uri); return body
    except urllib.error.HTTPError as e:
        print(f"    ! GET {svc}{uri} -> HTTP {e.code}", file=sys.stderr); return {}
    except Exception as e:
        print(f"    ! GET {svc}{uri} -> {e}", file=sys.stderr); return {}

# ── scope + fence (the LAW — asserted at every delete) ───────────────────────
def in_scope(name):
    return bool(name) and (ENV_TOKEN in name) and (DEP_PREFIX in name)
def fenced(name, eip=None):
    if name and (B1 in name or B2 in name): return True
    if eip and eip==BEIP: return True
    return False
def guard(kind, name, eip=None):
    """Return True iff safe to delete. Refuses (loudly) otherwise."""
    if fenced(name,eip):
        print(f"    FENCE: refusing {kind} '{name}' (bastion-protected)"); return False
    if not in_scope(name):
        # orphan classes are the only non-dual-token deletes, handled separately
        print(f"    SKIP: {kind} '{name}' not scope-matched (needs BOTH '{ENV_TOKEN}' + '{DEP_PREFIX}')"); return False
    return True

DELETED=[]; PACE=2.0
def delete(svc, uri, kind, name):
    """Paced, backoff-on-429, 5x-retry delete. Honors DRY-RUN."""
    if not APPLY:
        print(f"    WOULD delete {kind}: {name}"); DELETED.append((kind,name)); return True
    for attempt in range(5):
        try:
            st,_=signed(svc,"DELETE",uri)
            print(f"    deleted {kind}: {name} (HTTP {st})"); DELETED.append((kind,name)); time.sleep(PACE); return True
        except urllib.error.HTTPError as e:
            txt=""
            try: txt=e.read().decode()[:160]
            except Exception: pass
            if e.code==429 or "SYS.0429" in txt:
                print(f"    429 black-list on {kind} {name} — backoff 12s (attempt {attempt+1}/5)"); time.sleep(12); continue
            if e.code==409:
                print(f"    409 on {kind} {name} (deps releasing) — retry in 12s (attempt {attempt+1}/5)"); time.sleep(12); continue
            print(f"    ! DELETE {kind} {name} -> HTTP {e.code} {txt}", file=sys.stderr); return False
        except Exception as e:
            print(f"    ! DELETE {kind} {name} -> {e}", file=sys.stderr); time.sleep(PACE); continue
    print(f"    ! gave up on {kind} {name} after 5 attempts", file=sys.stderr); return False

print(f"\n[plan] region={REGION} project={PROJ[:8]}… apply={APPLY}")

# 1. ELB v3: healthmonitor -> members -> pool -> listener -> loadbalancer
print("[1] ELB v3 chain (healthmonitor -> members -> pool -> listener -> loadbalancer)")
elb=get("elb", f"/v3/{PROJ}/elb/loadbalancers")
for lb in elb.get("loadbalancers",[]):
    nm=lb.get("name",""); lbid=lb.get("id")
    if not guard("loadbalancer", nm): continue
    base=f"/v3/{PROJ}/elb"
    det=get("elb", f"{base}/loadbalancers/{lbid}")
    # pools (members + healthmonitor first)
    for pref in (lb.get("pools") or []):
        pid=pref.get("id")
        pool=get("elb", f"{base}/pools/{pid}").get("pool",{})
        for m in pool.get("members",[]) or []:
            delete("elb", f"{base}/pools/{pid}/members/{m.get('id')}", "elb-member", f"{nm}/{m.get('id')}")
        hm=pool.get("healthmonitor_id")
        if hm: delete("elb", f"{base}/healthmonitors/{hm}", "elb-healthmonitor", f"{nm}/{hm}")
        delete("elb", f"{base}/pools/{pid}", "elb-pool", f"{nm}/{pid}")
    for lref in (lb.get("listeners") or []):
        delete("elb", f"{base}/listeners/{lref.get('id')}", "elb-listener", f"{nm}/{lref.get('id')}")
    delete("elb", f"{base}/loadbalancers/{lbid}", "loadbalancer", nm)

# 2. NAT: snat_rules (nested) -> nat_gateways
print("[2] NAT (snat_rules -> nat_gateways)")
nats=get("nat", f"/v2/{PROJ}/nat_gateways")
for n in nats.get("nat_gateways",[]):
    nm=n.get("name",""); nid=n.get("id")
    if not guard("nat_gateway", nm): continue
    rules=get("nat", f"/v2/{PROJ}/snat_rules")
    for r in rules.get("snat_rules",[]) or []:
        if r.get("nat_gateway_id")==nid:
            delete("nat", f"/v2/{PROJ}/snat_rules/{r.get('id')}", "snat_rule", f"{nm}/{r.get('id')}")
    delete("nat", f"/v2/{PROJ}/nat_gateways/{nid}", "nat_gateway", nm)

# gather scoped VPC ids up front (needed for peerings + subnets ordering)
vpcs=get("vpc", f"/v1/{PROJ}/vpcs").get("vpcs",[])
scoped_vpcs=[v for v in vpcs if guard("_probe_vpc", v.get("name","")) ]
scoped_vpc_ids={v.get("id") for v in scoped_vpcs}

# 3. VPC peerings of the scoped VPCs
print("[3] VPC peerings (of scoped VPCs)")
peers=get("vpc", f"/v2.0/vpc/peerings").get("peerings",[])
for p in peers:
    a=(p.get("request_vpc_info") or {}).get("vpc_id"); b=(p.get("accept_vpc_info") or {}).get("vpc_id")
    nm=p.get("name","")
    if a in scoped_vpc_ids or b in scoped_vpc_ids:
        if fenced(nm): print(f"    FENCE: refusing peering '{nm}'"); continue
        delete("vpc", f"/v2.0/vpc/peerings/{p.get('id')}", "vpc_peering", nm or p.get('id'))

# 4. ECS batch delete (delete_publicip + delete_volume — also reaps attached CSI EVS)
print("[4] ECS batch delete (delete_publicip=true, delete_volume=true)")
servers=get("ecs", f"/v1/{PROJ}/cloudservers/detail").get("servers",[]) or \
        get("ecs", f"/v1/{PROJ}/cloudservers").get("servers",[])
ecs_ids=[]
for s in servers:
    nm=s.get("name","")
    if not guard("ecs", nm): continue
    ecs_ids.append(s.get("id"))
if ecs_ids:
    if not APPLY:
        for sid in ecs_ids: print(f"    WOULD delete ecs: {sid}")
        DELETED += [("ecs",sid) for sid in ecs_ids]
    else:
        body={"servers":[{"id":i} for i in ecs_ids],"delete_publicip":True,"delete_volume":True}
        for attempt in range(5):
            try:
                st,_=signed("ecs","POST",f"/v1/{PROJ}/cloudservers/delete",body)
                print(f"    batch-deleted {len(ecs_ids)} ecs (HTTP {st})"); DELETED += [("ecs",i) for i in ecs_ids]; time.sleep(PACE); break
            except urllib.error.HTTPError as e:
                if e.code==429: print("    429 on ecs batch — backoff 12s"); time.sleep(12); continue
                print(f"    ! ecs batch -> HTTP {e.code}", file=sys.stderr); break
            except Exception as e:
                print(f"    ! ecs batch -> {e}", file=sys.stderr); break

# 5. EVS remaining scoped available volumes (belt-and-suspenders after delete_volume)
print("[5] EVS scoped available volumes")
vols=get("evs", f"/v2/{PROJ}/volumes/detail").get("volumes",[])
for v in vols:
    nm=v.get("name",""); vid=v.get("id")
    if not guard("evs", nm): continue
    if v.get("status")=="available":
        delete("evs", f"/v2/{PROJ}/volumes/{vid}", "evs-volume", nm or vid)

# 6. Subnets of scoped VPCs (retry-on-409 handled in delete())
print("[6] subnets (of scoped VPCs)")
for v in scoped_vpcs:
    vid=v.get("id")
    subs=get("vpc", f"/v1/{PROJ}/vpcs/{vid}/subnets").get("subnets",[]) or \
         get("vpc", f"/v1/{PROJ}/subnets").get("subnets",[])
    for s in subs:
        if s.get("vpc_id") and s.get("vpc_id")!=vid: continue
        nm=s.get("name","")
        # subnets inherit VPC scope; still assert the fence on the subnet name if present
        if fenced(nm): print(f"    FENCE: refusing subnet '{nm}'"); continue
        delete("vpc", f"/v1/{PROJ}/vpcs/{vid}/subnets/{s.get('id')}", "subnet", nm or s.get('id'))

# 7. Security groups
print("[7] security groups")
sgs=get("vpc", f"/v1/{PROJ}/security-groups").get("security_groups",[])
for g in sgs:
    nm=g.get("name","")
    if not guard("security_group", nm): continue
    delete("vpc", f"/v1/{PROJ}/security-groups/{g.get('id')}", "security_group", nm)

# 8. VPCs
print("[8] VPCs")
for v in scoped_vpcs:
    delete("vpc", f"/v1/{PROJ}/vpcs/{v.get('id')}", "vpc", v.get("name",""))

# 9. EIP release (scoped) + optional --reap-orphans DOWN/unbound EIPs
print("[9] EIP release")
eips=get("vpc", f"/v1/{PROJ}/publicips").get("publicips",[])
for e in eips:
    ip=e.get("public_ip_address",""); nm=e.get("alias") or e.get("id","")
    if ip==BEIP: print(f"    FENCE: refusing bastion EIP {ip}"); continue
    scoped=in_scope(nm) or in_scope(e.get("id",""))
    if scoped and not fenced(nm,ip):
        delete("vpc", f"/v1/{PROJ}/publicips/{e.get('id')}", "eip", f"{nm}/{ip}")
    elif REAP and (not e.get("port_id")) and e.get("status")=="DOWN":
        # orphan class (a): DOWN unbound EIP — only under --reap-orphans + 0 active envs
        delete("vpc", f"/v1/{PROJ}/publicips/{e.get('id')}", "eip-orphan-DOWN", f"{nm}/{ip}")

# orphan class (b): available pvc-* EVS volumes, --reap-orphans only
if REAP:
    print("[9b] --reap-orphans: available pvc-* EVS volumes")
    for v in vols:
        nm=v.get("name","") or ""
        if v.get("status")=="available" and nm.startswith("pvc-") and not fenced(nm):
            delete("evs", f"/v2/{PROJ}/volumes/{v.get('id')}", "evs-orphan-pvc", nm)

# ── Verification sweep: re-enumerate every plane for scope leftovers ─────────
print("\n[verify] re-enumerating every plane for scope leftovers…")
leftovers=[]
def sweep(svc, uri, key, kindname):
    for it in get(svc, uri).get(key,[]) or []:
        nm=it.get("name","") or it.get("alias","") or it.get("id","")
        if in_scope(nm): leftovers.append(f"{kindname}:{nm}")
sweep("elb", f"/v3/{PROJ}/elb/loadbalancers","loadbalancers","loadbalancer")
sweep("nat", f"/v2/{PROJ}/nat_gateways","nat_gateways","nat_gateway")
sweep("vpc", f"/v1/{PROJ}/vpcs","vpcs","vpc")
sweep("vpc", f"/v1/{PROJ}/security-groups","security_groups","security_group")
sweep("evs", f"/v2/{PROJ}/volumes/detail","volumes","evs-volume")
sweep("vpc", f"/v1/{PROJ}/publicips","publicips","eip")
for s in (get("ecs", f"/v1/{PROJ}/cloudservers/detail").get("servers",[]) or []):
    if in_scope(s.get("name","")): leftovers.append(f"ecs:{s.get('name')}")

print(f"\n== summary ==\n  {'planned' if not APPLY else 'deleted'} {len(DELETED)} resource(s)")
if not APPLY:
    print("  DRY-RUN — nothing was deleted. Re-run with --apply from a mothership vantage to execute.")
    sys.exit(0)
if leftovers:
    print(f"  LEFTOVERS ({len(leftovers)}): "+", ".join(leftovers), file=sys.stderr)
    sys.exit(1)
print("  verification sweep CLEAN — no scoped resource remains.")
sys.exit(0)
PY
