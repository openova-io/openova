#!/usr/bin/env bash
# wipe-recordless-env.sh — BREAK-GLASS teardown of a Sovereign whose mothership
# deployment record AND tofu state are BOTH lost (store defect). Refs #5328.
#
# 🛑 THE CANONICAL WIPE COMES FIRST — ALWAYS. This tool is allowed ONLY when
# the canonical path is structurally impossible:
#   POST https://console.openova.io/sovereign/api/v1/deployments/{id}/wipe → 404
#   (record lost)  AND  /var/lib/catalyst/tofu/<dep-id>/ absent (state lost).
# If either exists, STOP and use the canonical endpoint (docs/RUNBOOKS.md §0.6,
# docs/PROTOCOL.md §5, feedback_canonical_wipe_endpoints.md). Using this tool
# when the canonical path works is itself a protocol violation.
#
# Proven live: hw284 (dep 383db23ccaf13be5), 2026-07-22 ~20:45–21:03Z — clean
# teardown via direct kom4dc HCS API enumeration scoped to the env's resource
# names. This script is that procedure, generalized + fenced.
#
# SCOPE (fail-closed): a resource is a candidate ONLY if its name contains BOTH
#   (a) the env subdomain token  e.g. hw284-omani-works  (dots auto→hyphens)
#   (b) the 8-char dep-id prefix e.g. 383db23c
# Child resources without their own scoped names (ELB listeners/pools/
# healthmonitors/members, snat/dnat rules, subnets) are selected ONLY via
# their scope-matched parent (LB / NAT gateway / VPC).
#
# 🛑 HARD FENCE (asserted at every delete):
#   - NEVER bastion-openova / bastion-openova-vpc / EIP 212.72.24.20 — a
#     scope-selected candidate whose name contains "bastion" ABORTS the run
#     (a bastion name passing the scope filter means scoping is broken —
#     stop everything).
#   - NEVER any resource NOT matching the scope tokens, with exactly TWO
#     orphan exceptions, BOTH gated behind --reap-orphans AND a live check
#     that the mothership deployments list shows ZERO active envs:
#       (1) status=DOWN, unbound (no port_id) EIPs
#       (2) status=available  pvc-*  EVS volumes (detached CSI leftovers —
#           CSI volume names never carry the env tokens, so the scoped pass
#           cannot see them)
#     The gate is fail-closed: list unreachable / auth missing ⇒ no reap.
#
# TEARDOWN ORDER (proven hw284; kom4dc quirks baked in — endpoint paths mirror
# the canonical purge cascade in products/catalyst/bootstrap/api/internal/
# providers/huawei/provider.go):
#   1. ELB v3: healthmonitor → members → pool → listener → loadbalancer.
#      Cascade delete is NOT supported (ELB.8902). Pool delete 409s while its
#      healthmonitor exists — healthmonitor FIRST.
#   2. NAT: snat_rules + dnat_rules via the NESTED kom4dc path
#      (DELETE /v2/{proj}/nat_gateways/{nid}/snat_rules/{rid} — the flat spec
#      path returns APIGW.0101), then the nat_gateways (VPC.2016 retry).
#   3. VPC peerings involving the scoped VPCs (VPC delete 409s while peered).
#   4. ECS batch: POST /v1/{proj}/cloudservers/delete with delete_publicip=true
#      + delete_volume=true (also removes the attached CSI EVS volumes — ~85
#      on hw284). Apply mode polls until the servers are actually gone.
#   5. EVS: scoped, status=available volumes.
#   6. Subnets: DELETE /v1/{proj}/vpcs/{vpc}/subnets/{id}, retried on 409
#      while ports release.
#   7. Security groups (409-retried — ports settle asynchronously, #4046).
#   8. VPCs (409-retried).
#   9. EIP release (scoped via bandwidth_name; 3 verify-passes — kom4dc can
#      200 a delete and keep the EIP, hw40 G13 lesson).
#  10. (--reap-orphans only) DOWN unbound EIPs + available pvc-* EVS.
#  Then a VERIFICATION SWEEP re-enumerates every plane; leftovers ⇒ exit 1.
#  Keypairs are deliberately NOT swept (their names lack the dep-id token; the
#  dual-token law is absolute — the next canonical wipe's sweep reaps them).
#
# RATE-LIMIT DISCIPLINE: kom4dc black-lists rapid calls (SYS.0429) — ~2s pace
# between deletes, 12s back-off on 429/SYS.0429, up to 5 attempts per call.
#
# Usage:
#   HW_TFVARS=/path/tofu.auto.tfvars.json \
#     bash scripts/wipe-recordless-env.sh <subdomain-token> <depid8> \
#       [--apply] [--reap-orphans] [--dry-run]
#   bash scripts/wipe-recordless-env.sh --self-test
#
#   <subdomain-token>  e.g. hw284-omani-works or hw284.omani.works
#   <depid8>           first 8 hex chars of the deployment id, e.g. 383db23c
#   --apply            actually delete. DEFAULT IS DRY-RUN (plan only).
#   --reap-orphans     also reap the two orphan classes above (needs BEARER or
#                      COOK for the mothership zero-active-deployments gate).
#   --self-test        offline fence/scope assertions — no creds, no network.
#
# Env:
#   HW_TFVARS   tofu.auto.tfvars.json with huawei_access_key/huawei_secret_key/
#               huawei_project_id/huawei_region (mothership PVC
#               catalyst-api-deployments: /var/lib/catalyst/tofu/<dep>/…)
#   BEARER|COOK owner JWT / catalyst_session cookie — only for --reap-orphans
#   WIPE_PACE / WIPE_BACKOFF / WIPE_ECS_WAIT  pacing overrides (s; 2 / 12 / 900)
#
# kom4dc API endpoints ({ecs,vpc,elb,nat,evs}.<region>.kom4dc.nationalcloud.om)
# are reachable from the mothership/bastion, NOT from dev sandboxes (private
# HCS, private CA → TLS verify off — same idiom as scripts/prov-preflight.sh).
set -uo pipefail

APPLY=0; REAP=0; SELFTEST=0; POS=()
usage() { sed -n '/^# Usage:/,/^# kom4dc API/p' "$0" | sed 's/^# \{0,1\}//'; }
for a in "$@"; do case "$a" in
  --apply) APPLY=1 ;;
  --reap-orphans) REAP=1 ;;
  --self-test) SELFTEST=1 ;;
  --dry-run) ;;
  -h|--help) usage; exit 0 ;;
  --*) echo "unknown flag: $a" >&2; usage >&2; exit 2 ;;
  *) POS+=("$a") ;;
esac; done

if [ "$SELFTEST" = "1" ]; then
  WIPE_MODE=selftest WIPE_SUB="hw284-omani-works" WIPE_DEP="383db23c" \
  WIPE_APPLY=0 WIPE_REAP=0 TFVARS_PATH=/dev/null
else
  # ── fail-closed argument validation ───────────────────────────────────────
  if [ "${#POS[@]}" -ne 2 ]; then
    echo "need exactly 2 positional args: <subdomain-token> <depid8>" >&2
    usage >&2; exit 2
  fi
  SUB="$(printf '%s' "${POS[0]}" | tr '.' '-' | tr '[:upper:]' '[:lower:]')"
  DEP="$(printf '%s' "${POS[1]}" | tr '[:upper:]' '[:lower:]')"
  if [ -z "$SUB" ] || [ -z "$DEP" ]; then
    echo "FAIL-CLOSED: empty scope token — refusing" >&2; exit 2
  fi
  if ! printf '%s' "$SUB" | grep -Eq '^(hw|t)[0-9]+-[a-z0-9-]+$'; then
    echo "FAIL-CLOSED: subdomain token '$SUB' must look like hw<NNN>-<parent-domain> (e.g. hw284-omani-works)" >&2
    exit 2
  fi
  if ! printf '%s' "$DEP" | grep -Eq '^[0-9a-f]{8}$'; then
    echo "FAIL-CLOSED: dep-id token '$DEP' must be exactly 8 hex chars (e.g. 383db23c)" >&2
    exit 2
  fi
  case "$SUB$DEP" in *bastion*) echo "HARD FENCE: scope token contains 'bastion' — refusing" >&2; exit 2 ;; esac
  if [ -z "${HW_TFVARS:-}" ] || [ ! -s "${HW_TFVARS}" ]; then
    echo "HW_TFVARS unset or empty — need tofu.auto.tfvars.json with Huawei AK/SK" >&2
    exit 2
  fi
  if [ "$REAP" = "1" ] && [ -z "${BEARER:-}" ] && [ -z "${COOK:-}" ]; then
    echo "FAIL-CLOSED: --reap-orphans needs BEARER or COOK to prove the mothership deployments list is empty" >&2
    exit 2
  fi
  echo "═══ RECORD-LESS ENV WIPE — scope: name ⊇ '${SUB}' AND ⊇ '${DEP}' ═══"
  if [ "$APPLY" = "1" ]; then
    echo "MODE: APPLY — resources WILL be deleted (bastion fence + dual-token scope enforced at every delete)"
  else
    echo "MODE: DRY-RUN — plan only, nothing is deleted (pass --apply to execute)"
  fi
  WIPE_MODE=run; WIPE_SUB="$SUB"; WIPE_DEP="$DEP"; WIPE_APPLY="$APPLY"; WIPE_REAP="$REAP"
  TFVARS_PATH="$HW_TFVARS"
fi

WIPE_MODE="$WIPE_MODE" WIPE_SUB="$WIPE_SUB" WIPE_DEP="$WIPE_DEP" \
WIPE_APPLY="$WIPE_APPLY" WIPE_REAP="$WIPE_REAP" \
BEARER="${BEARER:-}" COOK="${COOK:-}" \
WIPE_CONSOLE="${WIPE_CONSOLE:-https://console.openova.io/sovereign/api/v1}" \
WIPE_PACE="${WIPE_PACE:-2}" WIPE_BACKOFF="${WIPE_BACKOFF:-12}" \
WIPE_ECS_WAIT="${WIPE_ECS_WAIT:-900}" \
python3 - "$TFVARS_PATH" <<'PY'
import sys, os, json, time, ssl, hmac, hashlib, datetime
import urllib.request, urllib.error

# ── constants / scope / fence ────────────────────────────────────────────────
BASTION_EIP = "212.72.24.20"
MODE   = os.environ["WIPE_MODE"]
SUB    = os.environ["WIPE_SUB"]
DEP    = os.environ["WIPE_DEP"]
APPLY  = os.environ.get("WIPE_APPLY") == "1"
REAP   = os.environ.get("WIPE_REAP") == "1"
PACE    = float(os.environ.get("WIPE_PACE", "2"))
BACKOFF = float(os.environ.get("WIPE_BACKOFF", "12"))
ECSWAIT = int(os.environ.get("WIPE_ECS_WAIT", "900"))

def scoped(name):
    """Dual-token scope law: BOTH the subdomain token AND the dep-id prefix."""
    n = (name or "").lower()
    return bool(SUB) and bool(DEP) and (SUB in n) and (DEP in n)

def is_bastion(name, ip=""):
    return "bastion" in (name or "").lower() or ip == BASTION_EIP

class FenceViolation(SystemExit):
    pass

def fence_abort(kind, name, ip=""):
    raise FenceViolation(
        f"🛑 HARD FENCE VIOLATION: scope-selected {kind} '{name}' {ip} matches the "
        f"bastion protect-list (bastion-openova / bastion-openova-vpc / EIP {BASTION_EIP}). "
        f"Scoping is broken — ABORTING the entire run. NOTHING further is deleted.")

# ── self-test: offline fence/scope assertions (no creds, no network) ─────────
if MODE == "selftest":
    failures = []
    def check(desc, got, want):
        ok = got == want
        print(f"  {'✅' if ok else '❌'} {desc} → {got} (want {want})")
        if not ok:
            failures.append(desc)
    print("═══ SELF-TEST: scope truth-table (SUB=hw284-omani-works DEP=383db23c) ═══")
    check("scoped: full env resource name",
          scoped("catalyst-hw284-omani-works-383db23c-cp-region-a"), True)
    check("scoped: dep-id token missing ⇒ NOT a candidate",
          scoped("catalyst-hw284-omani-works-cp-region-a"), False)
    check("scoped: subdomain token missing ⇒ NOT a candidate",
          scoped("catalyst-383db23c-something"), False)
    check("scoped: sibling env hw285 with same dep prefix ⇒ NOT a candidate",
          scoped("catalyst-hw285-omani-works-383db23c-x"), False)
    check("scoped: hyphen-delimited token never matches a longer env number (hw28 vs hw284)",
          "hw28-omani-works" in "catalyst-hw284-omani-works-383db23c-x", False)
    check("scoped: empty name ⇒ NOT a candidate", scoped(""), False)
    check("scoped: CSI pvc-* volume (no env tokens) ⇒ NOT a candidate",
          scoped("pvc-1c0a9c3e-6b2f-4c0d-9df0-aaaaaaaaaaaa"), False)
    print("═══ SELF-TEST: hard fence ═══")
    check("fence: bastion-openova is bastion", is_bastion("bastion-openova"), True)
    check("fence: bastion-openova-vpc is bastion", is_bastion("bastion-openova-vpc"), True)
    check("fence: bastion EIP by address alone", is_bastion("unnamed", BASTION_EIP), True)
    check("fence: scope-matched name containing 'bastion' still fenced",
          is_bastion("catalyst-hw284-omani-works-383db23c-bastion-x"), True)
    check("fence: ordinary scoped node is NOT fenced",
          is_bastion("catalyst-hw284-omani-works-383db23c-cp-a"), False)
    try:
        fence_abort("ECS", "bastion-openova")
        print("  ❌ fence_abort did NOT abort"); failures.append("fence_abort no-abort")
    except FenceViolation as e:
        print("  ✅ fence_abort raises + names the protect-list:")
        print("     " + str(e)[:120] + "…")
    print("═══ SELF-TEST: teardown plan order (static) ═══")
    for i, s in enumerate([
        "ELB v3: healthmonitor → members → pool → listener → loadbalancer (ELB.8902: no cascade)",
        "NAT: nested snat_rules + dnat_rules → nat_gateways (VPC.2016 retry)",
        "VPC peerings of the scoped VPCs",
        "ECS batch delete (delete_publicip=true, delete_volume=true) + gone-poll",
        "EVS: scoped status=available volumes",
        "Subnets (409-retry while ports release)",
        "Security groups (409-retry)",
        "VPCs (409-retry)",
        "EIP release (scoped bandwidth_name, 3 verify-passes)",
        "--reap-orphans gate: DOWN unbound EIPs + available pvc-* EVS (zero-active-envs proof required)",
        "verification sweep: leftovers ⇒ exit 1",
    ], 1):
        print(f"  {i:2d}. {s}")
    if failures:
        print(f"SELF-TEST FAIL: {len(failures)} assertion(s): {failures}")
        sys.exit(1)
    print("SELF-TEST PASS: scope law + hard fence + plan order all hold (offline)")
    sys.exit(0)

# ── creds + signed requests (SDK-HMAC-SHA256, prov-preflight.sh idiom) ───────
t = json.load(open(sys.argv[1]))
AK = t.get('huawei_access_key') or t.get('access_key') or t.get('huawei_ak') or t.get('huaweiAccessKey')
SK = t.get('huawei_secret_key') or t.get('secret_key') or t.get('huawei_sk') or t.get('huaweiSecretKey')
PROJ = t.get('huawei_project_id') or t.get('project_id') or t.get('huaweiProjectId')
REGION = t.get('huawei_region') or t.get('huaweiRegion') or 'me-east-215'
if not AK or not SK or not PROJ:
    sys.exit("no Huawei AK/SK/project in tfvars — refusing to continue")

CTX = ssl.create_default_context(); CTX.check_hostname = False; CTX.verify_mode = ssl.CERT_NONE

def call(method, service, uri, body=None, timeout=30):
    """One signed kom4dc request. Returns (status, parsed-json-or-raw-dict)."""
    host = f"{service}.{REGION}.kom4dc.nationalcloud.om"
    payload = b"" if body is None else json.dumps(body).encode()
    now = datetime.datetime.utcnow().strftime("%Y%m%dT%H%M%SZ")
    ch = f"host:{host}\nx-sdk-date:{now}\n"; sh = "host;x-sdk-date"
    q = ""
    canon = uri
    if "?" in uri:
        canon, q = uri.split("?", 1)
    if not canon.endswith("/"):
        canon += "/"
    cr = f"{method}\n{canon}\n{q}\n{ch}\n{sh}\n" + hashlib.sha256(payload).hexdigest()
    sts = f"SDK-HMAC-SHA256\n{now}\n" + hashlib.sha256(cr.encode()).hexdigest()
    sig = hmac.new(SK.encode(), sts.encode(), hashlib.sha256).hexdigest()
    hdrs = {"X-Sdk-Date": now, "host": host,
            "Authorization": f"SDK-HMAC-SHA256 Access={AK}, SignedHeaders={sh}, Signature={sig}"}
    if body is not None:
        hdrs["Content-Type"] = "application/json"
    req = urllib.request.Request(f"https://{host}{uri}", data=(payload or None),
                                 headers=hdrs, method=method)
    try:
        r = urllib.request.urlopen(req, timeout=timeout, context=CTX)
        raw = r.read()
        return r.status, (json.loads(raw) if raw.strip() else {})
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            return e.code, json.loads(raw)
        except Exception:
            return e.code, {"raw": raw.decode(errors="replace")[:400]}
    except Exception as e:
        return 599, {"transport_error": str(e)[:200]}

def rate_limited(st, out):
    blob = json.dumps(out)[:300] if isinstance(out, dict) else str(out)[:300]
    return st == 429 or "SYS.0429" in blob or "black list" in blob.lower()

def paced(method, service, uri, body=None, retries=5):
    """Delete-pace discipline: ~2s between calls, 12s back-off on SYS.0429."""
    st, out = 599, {}
    for attempt in range(1, retries + 1):
        st, out = call(method, service, uri, body)
        if rate_limited(st, out):
            print(f"    …kom4dc rate-limit (SYS.0429/429), back-off {BACKOFF:.0f}s "
                  f"(attempt {attempt}/{retries})")
            time.sleep(BACKOFF)
            continue
        time.sleep(PACE)
        return st, out
    return st, out

def get(service, uri, key):
    st, out = call("GET", service, uri)
    if st >= 400:
        print(f"  ⚠️  GET {service}{uri} → {st}: {json.dumps(out)[:160]}")
        return []
    return out.get(key, []) if isinstance(out, dict) else []

DELETED, FAILED, PLANNED = [], [], []

def act(kind, name, do, via=None, ip="", orphan=False, retry409=0, retry409_sleep=15):
    """Single delete gate. EVERY delete goes through here.
    Fence + scope asserted per the law; dry-run only records the plan."""
    if is_bastion(name, ip) or (via and is_bastion(via)):
        if orphan:  # orphan sweeps ENUMERATE the bastion EIP — skip it loudly
            print(f"  FENCE-SKIP {kind} '{name}' {ip} — bastion protect-list, NEVER touched")
            return
        fence_abort(kind, name, ip)   # scope-selected bastion ⇒ scoping broken ⇒ abort
    anchor = via or name
    if not orphan and not scoped(anchor):
        # Second line of defense: candidates are pre-filtered by scoped();
        # reaching here un-scoped means a selection bug — abort, do not skip.
        raise FenceViolation(f"🛑 HARD FENCE: {kind} '{name}' (anchor '{anchor}') is not "
                             f"scope-matched ('{SUB}' + '{DEP}') — ABORTING")
    label = f"{kind} '{name}'" + (f" (via scoped parent '{via}')" if via else "") + (f" {ip}" if ip else "")
    if not APPLY:
        print(f"  PLAN  delete {label}")
        PLANNED.append(label)
        return
    st, out = do()
    for _ in range(retry409):
        if st not in (409, 400):
            break
        print(f"    …{st} on {label} (deps still releasing), retry in {retry409_sleep}s: "
              f"{json.dumps(out)[:120]}")
        time.sleep(retry409_sleep)
        st, out = do()
    if st < 400 or st == 404:
        print(f"  ✅ deleted {label} ({st})")
        DELETED.append(label)
    else:
        print(f"  ❌ FAILED {label} → {st}: {json.dumps(out)[:200]}")
        FAILED.append(label)

# ── --reap-orphans gate: mothership deployments list must be EMPTY ───────────
def assert_zero_active_deployments():
    console = os.environ["WIPE_CONSOLE"]
    bearer, cook = os.environ.get("BEARER", ""), os.environ.get("COOK", "")
    if not bearer and not cook:
        sys.exit("FAIL-CLOSED: --reap-orphans without BEARER/COOK")
    hdr = ({"Authorization": f"Bearer {bearer}"} if bearer
           else {"Cookie": f"catalyst_session={cook}"})
    try:
        req = urllib.request.Request(f"{console}/deployments", headers=hdr)
        d = json.load(urllib.request.urlopen(req, timeout=30))
    except Exception as e:
        sys.exit(f"FAIL-CLOSED: cannot fetch mothership deployments list ({e}) — refusing --reap-orphans")
    deps = d if isinstance(d, list) else d.get("deployments", d.get("items", []))
    live = [x for x in deps if x.get("status") not in ("wiped", "deleted", "failed", "gone", None)]
    if live:
        sys.exit(f"FAIL-CLOSED: {len(live)} active deployment(s) on the mothership — "
                 f"--reap-orphans is allowed ONLY at zero active envs")
    print(f"  reap gate: mothership deployments list shows 0 active envs — orphan reap permitted")

if REAP:
    assert_zero_active_deployments()

ELB = f"/v3/{PROJ}/elb"

# ── 1. ELB cascade: healthmonitor → members → pool → listener → LB ──────────
print("── step 1/10: ELB v3 (no cascade delete on kom4dc: ELB.8902) ──")
lbs = [l for l in get("elb", f"{ELB}/loadbalancers", "loadbalancers") if scoped(l.get("name"))]
for lb in lbs:
    lbn, lbid = lb.get("name", ""), lb["id"]
    st, detail = call("GET", "elb", f"{ELB}/loadbalancers/{lbid}")
    d = detail.get("loadbalancer", {}) if st < 400 else {}
    pools = [p["id"] for p in d.get("pools", [])]
    listeners = [x["id"] for x in d.get("listeners", [])]
    for pid in pools:
        pst, pd = call("GET", "elb", f"{ELB}/pools/{pid}")
        pool = pd.get("pool", {}) if pst < 400 else {}
        hm = pool.get("healthmonitor_id") or ""
        if hm:  # healthmonitor FIRST — pool delete 409s while it exists
            act("ELB healthmonitor", hm, lambda h=hm: paced("DELETE", "elb", f"{ELB}/healthmonitors/{h}"), via=lbn)
        for m in pool.get("members", []):
            act("ELB member", m["id"],
                lambda p=pid, mm=m["id"]: paced("DELETE", "elb", f"{ELB}/pools/{p}/members/{mm}"), via=lbn)
        act("ELB pool", pid, lambda p=pid: paced("DELETE", "elb", f"{ELB}/pools/{p}"), via=lbn, retry409=3)
    for lid in listeners:
        act("ELB listener", lid, lambda L=lid: paced("DELETE", "elb", f"{ELB}/listeners/{L}"), via=lbn, retry409=3)
    act("ELB loadbalancer", lbn, lambda i=lbid: paced("DELETE", "elb", f"{ELB}/loadbalancers/{i}"), retry409=3)

# ── 2. NAT: nested snat/dnat rules, then gateways ────────────────────────────
print("── step 2/10: NAT gateways (nested rule paths — flat path is APIGW.0101) ──")
NATB = f"/v2/{PROJ}"
nats = [n for n in get("nat", f"{NATB}/nat_gateways", "nat_gateways") if scoped(n.get("name"))]
for n in nats:
    nn, nid = n.get("name", ""), n["id"]
    for r in get("nat", f"{NATB}/snat_rules?nat_gateway_id={nid}", "snat_rules"):
        act("NAT snat_rule", r["id"],
            lambda g=nid, rr=r["id"]: paced("DELETE", "nat", f"{NATB}/nat_gateways/{g}/snat_rules/{rr}"), via=nn)
    for r in get("nat", f"{NATB}/dnat_rules?nat_gateway_id={nid}", "dnat_rules"):
        act("NAT dnat_rule", r["id"],
            lambda g=nid, rr=r["id"]: paced("DELETE", "nat", f"{NATB}/nat_gateways/{g}/dnat_rules/{rr}"), via=nn)
    # VPC.2016 eventual-consistency: rules 204 but NAT delete 400s briefly
    act("NAT gateway", nn, lambda i=nid: paced("DELETE", "nat", f"{NATB}/nat_gateways/{i}"), retry409=4, retry409_sleep=6)

# ── 3. VPC peerings involving the scoped VPCs ────────────────────────────────
print("── step 3/10: VPC peerings ──")
vpcs = [v for v in get("vpc", f"/v1/{PROJ}/vpcs", "vpcs") if scoped(v.get("name"))]
vpc_ids = {v["id"] for v in vpcs}
for pr in get("vpc", "/v2.0/vpc/peerings", "peerings"):
    req_v = (pr.get("request_vpc_info") or {}).get("vpc_id", "")
    acc_v = (pr.get("accept_vpc_info") or {}).get("vpc_id", "")
    if req_v in vpc_ids or acc_v in vpc_ids or scoped(pr.get("name")):
        act("VPC peering", pr.get("name") or pr["id"],
            lambda i=pr["id"]: paced("DELETE", "vpc", f"/v2.0/vpc/peerings/{i}"),
            via=(next((v["name"] for v in vpcs if v["id"] in (req_v, acc_v)), None) or pr.get("name")))

# ── 4. ECS batch delete (+ attached CSI EVS via delete_volume=true) ──────────
print("── step 4/10: ECS batch delete (delete_publicip=true, delete_volume=true) ──")
def scoped_servers():
    return [s for s in get("ecs", f"/v1/{PROJ}/cloudservers/detail?limit=200", "servers")
            if scoped(s.get("name"))]
servers = scoped_servers()
for s in servers:  # fence every member of the batch individually
    if is_bastion(s.get("name", "")):
        fence_abort("ECS", s.get("name", ""))
if servers:
    names = [s.get("name", "") for s in servers]
    if not APPLY:
        for nm in names:
            print(f"  PLAN  delete ECS '{nm}' (batch, delete_publicip+delete_volume)")
            PLANNED.append(f"ECS '{nm}'")
    else:
        body = {"servers": [{"id": s["id"]} for s in servers],
                "delete_publicip": True, "delete_volume": True}
        st, out = paced("POST", "ecs", f"/v1/{PROJ}/cloudservers/delete", body)
        if st < 400:
            print(f"  ✅ batch delete accepted for {len(servers)} ECS ({st}): {', '.join(names)}")
            DELETED.extend(f"ECS '{nm}'" for nm in names)
        else:
            print(f"  ❌ ECS batch delete → {st}: {json.dumps(out)[:200]}")
            FAILED.extend(f"ECS '{nm}'" for nm in names)
        deadline = time.time() + ECSWAIT
        while time.time() < deadline:
            left = scoped_servers()
            if not left:
                print("  ECS gone-poll: all scoped servers deleted")
                break
            print(f"  ECS gone-poll: {len(left)} scoped server(s) still present…")
            time.sleep(20)
        else:
            print(f"  ⚠️  ECS gone-poll timed out after {ECSWAIT}s — downstream 409s likely")
else:
    print("  (no scoped ECS)")

# ── 5. EVS: scoped, available volumes ────────────────────────────────────────
print("── step 5/10: EVS scoped available volumes ──")
def all_volumes():
    vols, marker = [], ""
    for _ in range(20):
        q = f"?limit=500{'&marker=' + marker if marker else ''}"
        page = get("evs", f"/v2/{PROJ}/cloudvolumes{q}", "volumes")
        vols += page
        if len(page) < 500 or not page[-1].get("id"):
            break
        marker = page[-1]["id"]
    return vols
vols = all_volumes()
for v in [v for v in vols if scoped(v.get("name")) and v.get("status") == "available"]:
    act("EVS volume", v.get("name", v["id"]),
        lambda i=v["id"]: paced("DELETE", "evs", f"/v2/{PROJ}/cloudvolumes/{i}"))

# ── 6. Subnets of the scoped VPCs (409-retry while ports release) ────────────
print("── step 6/10: subnets (409-retry while ports release) ──")
for v in vpcs:
    for s in get("vpc", f"/v1/{PROJ}/subnets?vpc_id={v['id']}", "subnets"):
        act("subnet", s.get("name", s["id"]),
            lambda vv=v["id"], ss=s["id"]: paced("DELETE", "vpc", f"/v1/{PROJ}/vpcs/{vv}/subnets/{ss}"),
            via=v["name"], retry409=8, retry409_sleep=15)

# ── 7. Security groups (#4046: 409s while ports settle) ─────────────────────
print("── step 7/10: security groups ──")
for sg in [g for g in get("vpc", f"/v1/{PROJ}/security-groups?limit=200", "security_groups")
           if scoped(g.get("name"))]:
    act("security group", sg.get("name", ""),
        lambda i=sg["id"]: paced("DELETE", "vpc", f"/v1/{PROJ}/security-groups/{i}"),
        retry409=3, retry409_sleep=10)

# ── 8. VPCs ──────────────────────────────────────────────────────────────────
print("── step 8/10: VPCs ──")
for v in vpcs:
    act("VPC", v.get("name", ""),
        lambda i=v["id"]: paced("DELETE", "vpc", f"/v1/{PROJ}/vpcs/{i}"),
        retry409=6, retry409_sleep=15)

# ── 9. EIPs (scoped via bandwidth_name; kom4dc can 200-and-keep → 3 passes) ──
print("── step 9/10: scoped EIP release ──")
def eip_name(e):
    return e.get("bandwidth_name") or e.get("alias") or ""
def scoped_eips():
    return [e for e in get("vpc", f"/v1/{PROJ}/publicips", "publicips") if scoped(eip_name(e))]
for p in range(1, 4):
    todo = scoped_eips()
    if not todo:
        break
    for e in todo:
        act("EIP", eip_name(e), lambda i=e["id"]: paced("DELETE", "vpc", f"/v1/{PROJ}/publicips/{i}"),
            ip=e.get("public_ip_address", ""))
    if not APPLY:
        break
    time.sleep(2 * p)

# ── 10. --reap-orphans: the ONLY two un-scoped delete classes ────────────────
if REAP:
    print("── step 10/10: orphan reap (zero-active-envs gate already proven) ──")
    for e in get("vpc", f"/v1/{PROJ}/publicips", "publicips"):
        ip = e.get("public_ip_address", "")
        if e.get("status") == "DOWN" and not e.get("port_id"):
            act("orphan EIP (DOWN, unbound)", eip_name(e) or ip,
                lambda i=e["id"]: paced("DELETE", "vpc", f"/v1/{PROJ}/publicips/{i}"),
                ip=ip, orphan=True)
    for v in all_volumes():
        if (v.get("name") or "").startswith("pvc-") and v.get("status") == "available":
            act("orphan EVS (available pvc-*)", v.get("name", ""),
                lambda i=v["id"]: paced("DELETE", "evs", f"/v2/{PROJ}/cloudvolumes/{i}"),
                orphan=True)
else:
    print("── step 10/10: orphan reap SKIPPED (no --reap-orphans) ──")

# ── verification sweep ───────────────────────────────────────────────────────
if not APPLY:
    print(f"═══ DRY-RUN COMPLETE: {len(PLANNED)} deletion(s) planned, nothing touched. "
          f"Re-run with --apply to execute. ═══")
    sys.exit(0)

print("── verification sweep: re-enumerating every plane for scope matches ──")
left = []
left += [f"ECS {s.get('name')}" for s in scoped_servers()]
left += [f"ELB {l.get('name')}" for l in get("elb", f"{ELB}/loadbalancers", "loadbalancers") if scoped(l.get("name"))]
left += [f"NAT {n.get('name')}" for n in get("nat", f"{NATB}/nat_gateways", "nat_gateways") if scoped(n.get("name"))]
left += [f"VPC {v.get('name')}" for v in get("vpc", f"/v1/{PROJ}/vpcs", "vpcs") if scoped(v.get("name"))]
left += [f"subnet {s.get('name')}" for s in get("vpc", f"/v1/{PROJ}/subnets", "subnets") if scoped(s.get("name"))]
left += [f"SG {g.get('name')}" for g in get("vpc", f"/v1/{PROJ}/security-groups?limit=200", "security_groups") if scoped(g.get("name"))]
left += [f"EIP {eip_name(e)} {e.get('public_ip_address')}" for e in scoped_eips()]
left += [f"EVS {v.get('name')}" for v in all_volumes() if scoped(v.get("name"))]

print(f"═══ RESULT: deleted={len(DELETED)} failed={len(FAILED)} leftovers={len(left)} ═══")
for x in FAILED:
    print(f"  FAILED: {x}")
for x in left:
    print(f"  LEFTOVER: {x}")
if FAILED or left:
    print("Non-clean teardown — re-run (idempotent), or pick up the residue with the "
          "next canonical wipe. EIP release lag is ~15min (RUNBOOKS §0.2) before re-fire.")
    sys.exit(1)
print("Clean teardown. Observe the ~15min EIP/quota release lag (RUNBOOKS §0.2) before any fire.")
PY
exit $?
