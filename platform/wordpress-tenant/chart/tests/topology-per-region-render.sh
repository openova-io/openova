#!/usr/bin/env bash
# bp-wordpress-tenant — per-region topology render gate (ADR-0015 D2, #7067).
#
# Replaces tests/active-hot-standby-render.sh, which ASSERTED the broken shape
# (two CNPG Cluster CRs in ONE release, separated by node affinity — the replica
# was unschedulable for 58 days on hw307 and, with dataDurability required,
# every write blocked).
#
# The chart is rendered ONCE PER REGION from the shared contract
# `openova.topology.{mode,self,primary,peers}` and every render must contain
# ONLY its own half:
#
#   R1  exactly ONE Cluster.postgresql.cnpg.io per render;
#   R2  its name is the per-region name `wordpress-db-<sha256(self)[0:8]>`;
#   R3  NO requiredDuringScheduling region affinity / nodeSelector / region
#       label naming a region other than `self` (anywhere in the render);
#   R4  spec.replica = {self: <own>, primary: <primary's cluster>, source};
#       no legacy `replica.enabled`;
#   R5  primary member: synchronous {dataDurability: required,
#       standbyNamesPre: [<peer cluster>], maxStandbyNamesFromCluster: 0} +
#       synchronous_commit remote_apply (ADR-0004); bootstrap.initdb.
#       replica member: NO synchronous block; bootstrap.pg_basebackup from the
#       primary's cluster;
#   R6  managed `<self>-mesh` service uses selectorType rw (never r, #3740);
#       a zero-backend `<peer>-mesh` stub exists for each peer;
#   R7  externalClusters list every member over its `-mesh` alias;
#   R8  WordPress Deployment replicas = N on the primary, 0 elsewhere; the
#       DB-writing hook Jobs render only on the primary; the WordPress Service
#       is mesh-global in multi-region;
#   R9  promotionToken rendered only on the member named primary;
#   R10 swapped primary (B primary / A replica) mirrors every assertion;
#   R11 contract absent ⇒ the legacy singleton (one `wordpress-db`, no replica
#       / synchronous / mesh fields), WordPress serving;
#   R12 fail-closed: multi-region without `self`; primary not self/peer; peer
#       == self; active-active; legacy `database.mode` / `pg.activeHotStandby`;
#       undeclared promotion mechanism.
#
# Every positive case runs a VACUITY twin: the same assertion body run against
# the OTHER region's render must FAIL (a check that passes on both regions'
# renders is not checking `self`).
#
# Usage: bash tests/topology-per-region-render.sh [CHART_DIR]
set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
cd "$CHART_DIR"

if [ ! -d charts ] || [ "$(ls charts 2>/dev/null | wc -l)" -lt 2 ]; then
  helm dependency build >/dev/null
fi

A="hw-me-east-215-a-rtz-prod"
B="hw-me-east-215-b-rtz-prod"
COMMON=(
  --namespace acme
  --set smeDomain=acme.example.local
  --set adminUser.email=admin@acme.example.local
  --api-versions postgresql.cnpg.io/v1
  --api-versions cilium.io/v2
)
topo() {  # $1 self  $2 primary  $3 peer
  printf '%s\n' --set openova.topology.mode=active-passive \
    --set "openova.topology.self=$1" --set "openova.topology.primary=$2" \
    --set "openova.topology.peers={$3}" \
    --set database.topology.promotion.mechanism=manual
}
render() {  # $1 out, rest = args
  local out="$1"; shift
  helm template wp . "${COMMON[@]}" "$@" > "$out"
}

mapfile -t TA < <(topo "$A" "$A" "$B")
mapfile -t TB < <(topo "$B" "$A" "$A")
mapfile -t SA < <(topo "$A" "$B" "$B")
mapfile -t SB < <(topo "$B" "$B" "$A")
render "$TMP/a-primary.yaml" "${TA[@]}"
render "$TMP/b-replica.yaml" "${TB[@]}"
render "$TMP/a-replica.yaml" "${SA[@]}"
render "$TMP/b-primary.yaml" "${SB[@]}" --set database.topology.promotionToken=TOKEN-FROM-A
render "$TMP/a-replica-token.yaml" "${SA[@]}" --set database.topology.promotionToken=TOKEN-FROM-A
render "$TMP/legacy.yaml"

cat > "$TMP/check.py" <<'PYEOF'
import sys, yaml, hashlib
path, self_r, primary_r, peer_r, mode = sys.argv[1:6]
token = sys.argv[6] if len(sys.argv) > 6 else ""
docs = [d for d in yaml.safe_load_all(open(path)) if d]
name = lambda r: "wordpress-db-" + hashlib.sha256(r.encode()).hexdigest()[:8]
errs = []
def ok(c, m):
    if not c: errs.append(m)

clusters = [d for d in docs if d.get("kind") == "Cluster"]
if mode == "legacy":
    ok(len(clusters) == 1, f"R11 legacy: want 1 Cluster, got {len(clusters)}")
    c = clusters[0] if clusters else {"metadata": {}, "spec": {}}
    s = c["spec"]
    ok(c["metadata"].get("name") == "wordpress-db", "R11 legacy name must stay wordpress-db")
    for k in ("replica", "externalClusters", "certificates", "managed"):
        ok(k not in s, f"R11 legacy leaked spec.{k}")
    ok("synchronous" not in s.get("postgresql", {}), "R11 legacy leaked synchronous")
    ok("initdb" in s.get("bootstrap", {}), "R11 legacy must initdb")
    dep = [d for d in docs if d.get("kind") == "Deployment"]
    ok(dep and dep[0]["spec"]["replicas"] == 1, "R11 legacy WordPress must serve (replicas 1)")
    jobs = [d for d in docs if d.get("kind") == "Job"]
    ok(len(jobs) == 2, f"R11 legacy must keep both hook Jobs, got {len(jobs)}")
    svc = [d for d in docs if d.get("kind") == "Service"]
    ok(all("service.cilium.io/global" not in (d["metadata"].get("annotations") or {}) for d in svc),
       "R11 legacy must not render mesh-global Services")
else:
    is_primary = self_r == primary_r
    ok(len(clusters) == 1, f"R1 want exactly 1 Cluster, got {len(clusters)}")
    c = clusters[0] if clusters else {"metadata": {}, "spec": {}}
    s = c["spec"]
    ok(c["metadata"].get("name") == name(self_r), f"R2 Cluster name {c['metadata'].get('name')} != {name(self_r)}")
    rep = s.get("replica") or {}
    ok("enabled" not in rep, "R4 legacy replica.enabled must not render")
    ok(rep.get("self") == name(self_r), f"R4 replica.self {rep.get('self')} != {name(self_r)}")
    ok(rep.get("primary") == name(primary_r), f"R4 replica.primary {rep.get('primary')} != {name(primary_r)}")
    ok(rep.get("source") == (name(peer_r) if is_primary else name(primary_r)), f"R4 replica.source {rep.get('source')}")
    if token:
        ok((rep.get("promotionToken") == token) == is_primary, f"R9 promotionToken on primary only (got {rep.get('promotionToken')!r})")
    else:
        ok("promotionToken" not in rep, "R9 no token configured, none must render")
    pg = s.get("postgresql") or {}
    sync = pg.get("synchronous")
    if is_primary:
        ok(sync is not None, "R5 primary must render synchronous (ADR-0004)")
        sync = sync or {}
        ok(sync.get("dataDurability") == "required", "R5 dataDurability must be required")
        ok(sync.get("standbyNamesPre") == [name(peer_r)], f"R5 standbyNamesPre {sync.get('standbyNamesPre')} != [{name(peer_r)}]")
        ok(sync.get("maxStandbyNamesFromCluster") == 0, "R5 local pods must be excluded")
        ok((pg.get("parameters") or {}).get("synchronous_commit") == "remote_apply", "R5 synchronous_commit remote_apply")
        ok("initdb" in (s.get("bootstrap") or {}), "R5 primary bootstraps initdb")
    else:
        ok(sync is None, "R5 replica must NOT render a synchronous fence")
        ok("synchronous_commit" not in (pg.get("parameters") or {}), "R5 replica must not set synchronous_commit")
        bb = (s.get("bootstrap") or {}).get("pg_basebackup") or {}
        ok(bb.get("source") == name(primary_r), f"R5 replica pg_basebackup.source {bb.get('source')}")
    add = (((s.get("managed") or {}).get("services") or {}).get("additional")) or []
    ok(len(add) == 1 and add[0].get("selectorType") == "rw", "R6 managed mesh service must be selectorType rw")
    ok(add and add[0]["serviceTemplate"]["metadata"]["name"] == name(self_r) + "-mesh", "R6 managed service must be <self>-mesh")
    stubs = [d for d in docs if d.get("kind") == "Service" and d["metadata"]["name"].endswith("-mesh")]
    ok([d["metadata"]["name"] for d in stubs] == [name(peer_r) + "-mesh"], f"R6 peer stub set {[d['metadata']['name'] for d in stubs]}")
    for st in stubs:
        ok(st["spec"].get("selector") == {"cnpg.io/cluster": name(peer_r)}, "R6 stub selector must be the peer cluster")
    ext = {e["name"]: e["connectionParameters"]["host"] for e in s.get("externalClusters") or []}
    ok(ext == {name(self_r): name(self_r) + "-mesh", name(peer_r): name(peer_r) + "-mesh"}, f"R7 externalClusters {ext}")
    ok(s.get("certificates", {}).get("clientCASecret") == "wordpress-db-replication-ca", "R7 shared client CA must be pinned")
    dep = [d for d in docs if d.get("kind") == "Deployment"]
    ok(dep and dep[0]["spec"]["replicas"] == (1 if is_primary else 0), f"R8 WordPress replicas {dep[0]['spec']['replicas'] if dep else None}")
    jobs = [d for d in docs if d.get("kind") == "Job"]
    ok(len(jobs) == (2 if is_primary else 0), f"R8 hook Jobs on primary only, got {len(jobs)}")
    wsvc = [d for d in docs if d.get("kind") == "Service" and d["metadata"]["name"] == "wp-bp-wordpress-tenant"]
    ok(wsvc and (wsvc[0]["metadata"].get("annotations") or {}).get("service.cilium.io/global") == "true", "R8 WordPress Service must be mesh-global")
    if not is_primary:
        ok((c["metadata"].get("annotations") or {}).get("catalyst.openova.io/dr-promotion-mechanism") == "manual", "#5623 standby must be stamped")

# R3 — nothing pinned to a region other than self (all modes).
def walk(o, p=""):
    if isinstance(o, dict):
        for k, v in o.items():
            yield p + "/" + str(k), k, v
            yield from walk(v, p + "/" + str(k))
    elif isinstance(o, list):
        for i, v in enumerate(o):
            yield from walk(v, f"{p}[{i}]")
REGION_KEYS = ("openova.io/region", "topology.kubernetes.io/region")
for d in docs:
    for p, k, v in walk(d):
        if k in REGION_KEYS and isinstance(v, str):
            ok(v == self_r or mode == "legacy", f"R3 {d['kind']}/{d['metadata'].get('name')} {p} = {v} (self {self_r})")
        if isinstance(v, dict) and v.get("key") in REGION_KEYS:
            for val in v.get("values") or []:
                ok(val == self_r, f"R3 {d['kind']}/{d['metadata'].get('name')} affinity {p} pins {val} (self {self_r})")
if errs:
    print("\n".join("    " + e for e in errs))
    sys.exit(1)
PYEOF

fails=0
expect_pass() {  # label, check args...
  local label="$1"; shift
  if python3 "$TMP/check.py" "$@"; then echo "  PASS  $label"; else echo "  FAIL  $label"; fails=$((fails+1)); fi
}
expect_fail() {  # vacuity: the assertion body must go RED on the wrong render
  local label="$1"; shift
  if python3 "$TMP/check.py" "$@" >/dev/null; then echo "  FAIL  vacuity: $label passed on the wrong render"; fails=$((fails+1)); else echo "  PASS  vacuity: $label"; fi
}

echo "[topology] A primary / B replica"
expect_pass "A renders only A (primary)" "$TMP/a-primary.yaml" "$A" "$A" "$B" multi
expect_pass "B renders only B (replica of A)" "$TMP/b-replica.yaml" "$B" "$A" "$A" multi
expect_fail "A-as-self assertions on B's render" "$TMP/b-replica.yaml" "$A" "$A" "$B" multi
expect_fail "B-as-self assertions on A's render" "$TMP/a-primary.yaml" "$B" "$A" "$A" multi

echo "[topology] swapped: B primary / A replica (primary move)"
expect_pass "B renders only B (primary, token)" "$TMP/b-primary.yaml" "$B" "$B" "$A" multi TOKEN-FROM-A
expect_pass "A renders only A (demoted replica of B)" "$TMP/a-replica.yaml" "$A" "$B" "$B" multi
expect_pass "token never renders on a replica" "$TMP/a-replica-token.yaml" "$A" "$B" "$B" multi TOKEN-FROM-A
expect_fail "pre-move primary assertions on the swapped A render" "$TMP/a-replica.yaml" "$A" "$A" "$B" multi

echo "[topology] contract absent ⇒ legacy singleton"
expect_pass "legacy singleton unchanged" "$TMP/legacy.yaml" "-" "-" "-" legacy
expect_fail "multi-region assertions on the legacy render" "$TMP/legacy.yaml" "$A" "$A" "$B" multi

echo "[topology] fail-closed"
must_fail() {  # label, grep-pattern, helm args...
  local label="$1" pat="$2"; shift 2
  if helm template wp . "${COMMON[@]}" "$@" >/dev/null 2>"$TMP/err"; then
    echo "  FAIL  $label rendered"; fails=$((fails+1))
  elif grep -q -- "$pat" "$TMP/err"; then echo "  PASS  $label"
  else echo "  FAIL  $label: wrong error: $(tail -1 "$TMP/err")"; fails=$((fails+1)); fi
}
must_fail "multi-region without self" "openova.topology.self is REQUIRED" \
  --set openova.topology.mode=active-passive --set openova.topology.primary="$A" --set "openova.topology.peers={$B}" \
  --set database.topology.promotion.mechanism=manual
must_fail "primary neither self nor peer" "is neither self" \
  --set openova.topology.mode=active-passive --set openova.topology.self="$A" --set openova.topology.primary=hw-x \
  --set "openova.topology.peers={$B}" --set database.topology.promotion.mechanism=manual
must_fail "self listed as peer" "must not contain self" \
  --set openova.topology.mode=active-passive --set openova.topology.self="$A" --set openova.topology.primary="$A" \
  --set "openova.topology.peers={$A}" --set database.topology.promotion.mechanism=manual
must_fail "active-active refused" "not supported by bp-wordpress-tenant" \
  --set openova.topology.mode=active-active --set openova.topology.self="$A" --set openova.topology.primary="$A" \
  --set "openova.topology.peers={$B}"
must_fail "legacy database.mode refused" "REMOVED" --set database.mode=active-hot-standby
must_fail "legacy pg.activeHotStandby refused" "REMOVED" --set pg.activeHotStandby.enabled=true
must_fail "undeclared promotion mechanism" "promotion.mechanism is REQUIRED" \
  --set openova.topology.mode=active-passive --set openova.topology.self="$A" --set openova.topology.primary="$A" \
  --set "openova.topology.peers={$B}"

if [ "$fails" -gt 0 ]; then echo "FAIL: $fails topology assertion(s)"; exit 1; fi
echo "PASS: bp-wordpress-tenant renders only its own region half (ADR-0015 D2)"
