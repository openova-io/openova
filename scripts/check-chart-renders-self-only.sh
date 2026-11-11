#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# ADR-0015 D2 CLASS GATE — a multi-region chart renders ONLY its `self` half.
# ─────────────────────────────────────────────────────────────────────────────
#
# THE DEFECT THIS EXISTS TO PREVENT
# ---------------------------------
# bp-wordpress-tenant ≤0.4.26 rendered BOTH CNPG halves (primary + replica)
# into ONE release applied to ONE cluster, separated only by node affinity. On
# hw307 the replica could never schedule in region A (its affinity named region
# B) for 58 days, and with ADR-0004 `dataDurability: required` every write
# blocked. Its own render test asserted exactly that broken shape ("2 Cluster
# CRs"), so CI was green the whole time. bp-cnpg-pair hit the same class on
# hw126 (chart ≤0.1.x) and fixed it per chart; nothing stopped the next chart.
#
# WHAT IT CHECKS
# --------------
# Each registered chart is rendered ONCE PER REGION (as each region's own
# HelmRelease would render it). For every render:
#   S1  no resource is pinned to a region other than `self` — any
#       `openova.io/region` / `topology.kubernetes.io/region` label, selector,
#       nodeSelector value, or node-affinity matchExpression value that is not
#       `self` FAILS (requiredDuringScheduling and preferred alike);
#   S2  at most ONE CNPG `Cluster` (and exactly one when the case says so) —
#       a region's release owns one database member, never its peer's.
# Chart-specific semantics (replica.primary, sync standby = peer, …) live in
# each chart's own tests; this gate is the catalog-wide floor.
#
# A VACUITY pass re-checks every render against the OTHER region of its case
# pair and requires S1 to FAIL there — a gate that cannot tell regions apart is
# not checking `self`.
#
# REGISTRY — charts that declare multi-region support:
#   platform/wordpress-tenant/chart  — openova.topology contract (ADR-0015 D2)
#   platform/cnpg-pair/chart         — openova.topology contract (ADR-0015 D8,
#                                      slot 16b via valuesFrom) + the legacy
#                                      cnpgPair.side vocabulary it maps onto
#   platform/postgres/chart          — openova.topology contract (ADR-0015 D8,
#                                      slots 16c/16d via valuesFrom)
# TODO (migrate to openova.topology via platform/topology-lib, then register):
#   platform/gitea, platform/openbao, platform/harbor,
#   platform/newapi, platform/guacamole, platform/sandbox.
#
# Usage: bash scripts/check-chart-renders-self-only.sh
# Refs #7067
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

A="hw-me-east-215-a-rtz-prod"
B="hw-me-east-215-b-rtz-prod"
fails=0
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; fails=$((fails + 1)); }

ensure_deps() {
  local dir="$1"
  if grep -q '^dependencies:' "$dir/Chart.yaml" 2>/dev/null; then
    (cd "$dir" && helm dependency build >/dev/null 2>&1) || true
  fi
}

cat > "$TMP/self_only.py" <<'PYEOF'
import sys, yaml
path, self_r, want_clusters = sys.argv[1], sys.argv[2], sys.argv[3]
REGION_KEYS = {"openova.io/region", "topology.kubernetes.io/region"}
docs = [d for d in yaml.safe_load_all(open(path)) if isinstance(d, dict)]
errs = []
def walk(o, p):
    if isinstance(o, dict):
        if o.get("key") in REGION_KEYS:
            for v in o.get("values") or []:
                if v != self_r:
                    errs.append(f"S1 {p}: affinity/selector expression pins region {v!r}")
        for k, v in o.items():
            if k in REGION_KEYS and isinstance(v, str) and v != self_r:
                errs.append(f"S1 {p}/{k} = {v!r}")
            walk(v, f"{p}/{k}")
    elif isinstance(o, list):
        for i, v in enumerate(o):
            walk(v, f"{p}[{i}]")
for d in docs:
    walk(d, f"{d.get('kind')}/{(d.get('metadata') or {}).get('name')}")
clusters = [d for d in docs if d.get("kind") == "Cluster" and str(d.get("apiVersion", "")).startswith("postgresql.cnpg.io/")]
if len(clusters) > 1:
    errs.append(f"S2 {len(clusters)} CNPG Clusters in one region's render: {[c['metadata']['name'] for c in clusters]}")
if want_clusters != "any" and len(clusters) != int(want_clusters):
    errs.append(f"S2 want {want_clusters} CNPG Cluster(s), got {len(clusters)}")
if not docs:
    errs.append("render is EMPTY — nothing was checked")
for e in errs[:20]:
    print("        " + e)
sys.exit(1 if errs else 0)
PYEOF

# case <label> <chart> <self> <other> <want-clusters> <helm args...>
case_() {
  local label="$1" chart="$2" self="$3" other="$4" want="$5"; shift 5
  local out="$TMP/$(echo "$label" | tr -c 'a-zA-Z0-9' '_').yaml"
  if ! helm template self-only "$chart" --namespace self-only "$@" > "$out" 2> "$out.err"; then
    fail "$label: render errored: $(tail -1 "$out.err")"
    return
  fi
  if python3 "$TMP/self_only.py" "$out" "$self" "$want"; then
    pass "$label renders only self=$self"
  else
    fail "$label pins resources outside self=$self"
  fi
  if python3 "$TMP/self_only.py" "$out" "$other" any > /dev/null; then
    fail "$label: VACUITY — the same render also passes as self=$other (gate cannot tell regions apart)"
  else
    pass "$label: vacuity — render is rejected as self=$other"
  fi
}

echo "══ bp-wordpress-tenant (openova.topology) ══"
WP=platform/wordpress-tenant/chart
ensure_deps "$WP"
WP_BASE=(--set smeDomain=a.example --set adminUser.email=a@b.c
  --api-versions postgresql.cnpg.io/v1 --api-versions cilium.io/v2
  --set openova.topology.mode=active-passive --set database.topology.promotion.mechanism=manual)
case_ "wordpress A primary" "$WP" "$A" "$B" 1 "${WP_BASE[@]}" \
  --set openova.topology.self="$A" --set openova.topology.primary="$A" --set "openova.topology.peers={$B}"
case_ "wordpress B replica" "$WP" "$B" "$A" 1 "${WP_BASE[@]}" \
  --set openova.topology.self="$B" --set openova.topology.primary="$A" --set "openova.topology.peers={$A}"
case_ "wordpress B primary (swapped)" "$WP" "$B" "$A" 1 "${WP_BASE[@]}" \
  --set openova.topology.self="$B" --set openova.topology.primary="$B" --set "openova.topology.peers={$A}"
case_ "wordpress A replica (swapped)" "$WP" "$A" "$B" 1 "${WP_BASE[@]}" \
  --set openova.topology.self="$A" --set openova.topology.primary="$B" --set "openova.topology.peers={$B}"

echo "══ bp-cnpg-pair (cnpgPair.side + openova.topology) ══"
CP=platform/cnpg-pair/chart
ensure_deps "$CP"
CP_BASE=(--set cnpgPair.enabled=true --set cnpgPair.primary.region="$A" --set cnpgPair.replica.region="$B"
  --set cnpgPair.image.tag=16.3-23)
case_ "cnpg-pair side=primary" "$CP" "$A" "$B" 1 "${CP_BASE[@]}" --set cnpgPair.side=primary
case_ "cnpg-pair side=replica" "$CP" "$B" "$A" 1 "${CP_BASE[@]}" --set cnpgPair.side=replica
case_ "cnpg-pair side=primary distributed" "$CP" "$A" "$B" 1 "${CP_BASE[@]}" --set cnpgPair.side=primary --set cnpgPair.distributed.enabled=true
case_ "cnpg-pair side=replica distributed" "$CP" "$B" "$A" 1 "${CP_BASE[@]}" --set cnpgPair.side=replica --set cnpgPair.distributed.enabled=true
# ADR-0015 D8 — the contract, with the slot's legacy `side` substitute left at
# primary in BOTH regions: `self` alone must decide which half renders.
CP_TOPO=(--set openova.topology.mode=active-hot-standby --set cnpgPair.side=primary)
case_ "cnpg-pair contract A primary" "$CP" "$A" "$B" 1 "${CP_BASE[@]}" "${CP_TOPO[@]}" \
  --set openova.topology.self="$A" --set openova.topology.primary="$A" --set "openova.topology.peers={$B}"
case_ "cnpg-pair contract B standby" "$CP" "$B" "$A" 1 "${CP_BASE[@]}" "${CP_TOPO[@]}" \
  --set openova.topology.self="$B" --set openova.topology.primary="$A" --set "openova.topology.peers={$A}"
case_ "cnpg-pair contract B primary (swapped)" "$CP" "$B" "$A" 1 "${CP_BASE[@]}" "${CP_TOPO[@]}" \
  --set openova.topology.self="$B" --set openova.topology.primary="$B" --set "openova.topology.peers={$A}" --set database.topology.promotionToken=T
case_ "cnpg-pair contract A standby (swapped)" "$CP" "$A" "$B" 1 "${CP_BASE[@]}" "${CP_TOPO[@]}" \
  --set openova.topology.self="$A" --set openova.topology.primary="$B" --set "openova.topology.peers={$B}"

echo "══ bp-postgres (openova.topology, shared instance) ══"
PG=platform/postgres/chart
ensure_deps "$PG"
PG_BASE=(--set enabled=true --set instance.name=shared-pg --set instance.namespace=shared-data
  --set topology.crossRegion=true --set topology.side=primary
  --set topology.primary.region="$A" --set topology.replica.region="$B"
  --api-versions postgresql.cnpg.io/v1 --set openova.topology.mode=active-hot-standby)
case_ "postgres contract A primary" "$PG" "$A" "$B" 1 "${PG_BASE[@]}" \
  --set openova.topology.self="$A" --set openova.topology.primary="$A" --set "openova.topology.peers={$B}"
case_ "postgres contract B standby" "$PG" "$B" "$A" 1 "${PG_BASE[@]}" \
  --set openova.topology.self="$B" --set openova.topology.primary="$A" --set "openova.topology.peers={$A}"
case_ "postgres contract B primary (swapped)" "$PG" "$B" "$A" 1 "${PG_BASE[@]}" \
  --set openova.topology.self="$B" --set openova.topology.primary="$B" --set "openova.topology.peers={$A}" --set database.topology.promotionToken=T
case_ "postgres contract A standby (swapped)" "$PG" "$A" "$B" 1 "${PG_BASE[@]}" \
  --set openova.topology.self="$A" --set openova.topology.primary="$B" --set "openova.topology.peers={$B}"

echo
if [ "$fails" -gt 0 ]; then
  echo "FAIL: $fails self-only assertion(s) (ADR-0015 D2)"
  exit 1
fi
echo "PASS: every registered multi-region chart renders only its self half"
