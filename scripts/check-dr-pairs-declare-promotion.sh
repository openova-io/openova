#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# #5623 CLASS GATE — every CNPG DR pair in this catalog must DECLARE, and
# actually RENDER, the mechanism that promotes its standby on a region kill.
# ─────────────────────────────────────────────────────────────────────────────
#
# THE DEFECT THIS EXISTS TO PREVENT
# ---------------------------------
# hw292 G12 region-kill, 2026-08-03, T0 = 20:01:27Z, hard os-stop of all four
# region-A ECS. `bp-cnpg-pair`'s region-B dr-promoter did exactly what it was
# built to do: primary loss at 20:01:45Z, 120s hold, PROMOTING at 20:03:43Z,
# RPO=0, post-kill write accepted, zero operator action.
#
# The three `bp-postgres` shared-pg pairs did nothing. They render the IDENTICAL
# split-side standby shape — same `replica.enabled: true`, same
# `bootstrap.pg_basebackup`, same WAL stream over the same ClusterMesh `-mesh`
# Service — but shipped no region-local promoter, so `shared-pg-replica`,
# `shared-pg-b-replica` and `shared-pg-c-replica` each stayed
# `pg_is_in_recovery() = t` for the WHOLE outage. keycloak is a shared-pg
# consumer, so the auth path could not recover in region-B even in principle.
#
# Enumerating region-B Deployments during the outage returned exactly ONE DR
# actor for FOUR pairs. Nothing in the source distinguished the pair that
# promotes from the three that cannot: all four rendered valid YAML, all four
# reported Ready, and the difference only became observable by killing a region.
#
# WHY A CATALOG-WIDE GATE AND NOT A PER-CHART TEST
# ------------------------------------------------
# bp-cnpg-pair's own render test asserted its promoter was present, and passed,
# for every one of the months bp-postgres had none. A property proven in ONE
# place and assumed fleet-wide is the exact shape of #5591 and of this issue.
# So the gate ENUMERATES the catalog: any template that renders a CNPG standby
# half is in scope, whether or not its author thought about failover.
#
# WHY THE ASSERTIONS LOOK LIKE THIS
# ---------------------------------
#   1. NEVER assert on a KEY. `grep -q dr-promoter` passes on a comment, and
#      `grep -q 'name: PRIMARY_LIVENESS_ENABLED'` passes on an EMPTY value —
#      that is #5639, where a guard read the key and missed a broken value for
#      two months. Every positive case below pins the rendered VALUE.
#   2. NEVER accept a promoter that would promote unsafely. #5178: a promoter
#      that fires without a positive proof the primary region is gone
#      false-promoted a healthy replica and ran the region-B timeline away
#      (hw266, TL2→TL25). #5220: one that fires before it has ever observed
#      steady-state streaming cements a convergence-time transient. So
#      "declares dr-promoter" is only satisfied by a promoter that carries the
#      positive-liveness gate AND the steady-state arm gate AND drives the
#      HelmRelease desired state rather than patching a live Cluster CR
#      (#5125-D1: a live patch is reverted by flux drift-correction mid-outage).
#   3. Every dr-promoter case has a NEGATIVE twin that must NOT render the
#      promoter (async replication — no synchronous data fence, so automatic
#      promotion could fork committed data). A guard observed only passing is
#      not yet known to be a guard, and a blanket "everything renders" cannot
#      satisfy this gate.
#
# WHAT IT CHECKS
# --------------
#   Phase 1 (registry)  — enumerate every chart template in the repo that emits
#                         a CNPG `bootstrap.pg_basebackup` stanza (the
#                         structural signature of a standby half: a Cluster CR
#                         that seeds itself from another Cluster). Each must be
#                         registered below with its promotion mechanism. A NEW
#                         unregistered one FAILS, so adding a DR pair forces the
#                         author to state what promotes it. A registered file
#                         that no longer emits one also FAILS, so the registry
#                         cannot rot. A registered chart with no Phase-2 case
#                         FAILS, so registering cannot become a rubber stamp.
#   Phase 2 (behavior)  — render each registered chart and prove the declared
#                         mechanism is the one it actually ships, both
#                         directions. See each case for its specific claim.
#
# Usage: bash scripts/check-dr-pairs-declare-promotion.sh
# Refs #5623 #5178 #5220 #5133 #5157 #5125 #5639

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

A="hz-fsn-rtz-prod"
B="hz-hel-rtz-prod"

fails=0
pass() { printf '  PASS  %s\n' "$1"; }
fail() {
  printf '  FAIL  %s\n' "$1"
  fails=$((fails + 1))
}

# Chart deps must be vendored before `helm template` (bp-wordpress-tenant pulls
# `common`). Same pattern the per-chart tests use.
ensure_deps() {
  local dir="$1"
  if [ -f "$dir/Chart.yaml" ] && grep -q '^dependencies:' "$dir/Chart.yaml" 2>/dev/null; then
    if [ ! -d "$dir/charts" ] || [ -z "$(ls -A "$dir/charts" 2>/dev/null)" ]; then
      # Retry once and SHOW the error: a silent failure here turns every
      # render below into "missing in charts/ directory", which hides whether
      # the cause is the chart or the dependency's download host.
      local log; log="$(mktemp)"
      if ! (cd "$dir" && helm dependency build >"$log" 2>&1); then
        sleep 5
        if ! (cd "$dir" && helm dependency build >"$log" 2>&1); then
          fail "helm dependency build failed for $dir (every render of it below is invalid):
        $(tail -3 "$log")"
        fi
      fi
      rm -f "$log"
    fi
  fi
}

# env_value <file> <ENV_NAME> — the rendered VALUE of a container env var.
# Reads the `value:` line that FOLLOWS `- name: <ENV_NAME>`, so an empty or
# missing value is distinguishable from a present key (#5639).
env_value() {
  grep -A1 -E "^[[:space:]]*-[[:space:]]*name:[[:space:]]*$2[[:space:]]*$" "$1" \
    | grep -E "^[[:space:]]*value:" | head -1 \
    | sed -E 's/^[[:space:]]*value:[[:space:]]*//; s/^"//; s/"$//'
}

# promoter_deployments <file> — names of rendered Deployments whose name marks
# them a DR promoter. Parsed as YAML, not grepped, so a comment mentioning
# "dr-promoter" can never satisfy the assertion.
promoter_deployments() {
  python3 - "$1" <<'PYEOF'
import sys, yaml
try:
    docs = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
except Exception as e:
    print(f"__PARSE_ERROR__ {e}")
    sys.exit(0)
for d in docs:
    if d.get('kind') == 'Deployment':
        n = (d.get('metadata') or {}).get('name', '')
        if 'dr-promoter' in n:
            print(n)
PYEOF
}

# ─────────────────────────────────────────────────────────────────────────────
# REGISTRY — every chart template that renders a CNPG standby half, and the
# mechanism that promotes that standby when its source region dies.
#
# Format: <template path>|<chart dir>|<mechanism>
#
# Mechanisms:
#   dr-promoter — a region-local Deployment in the SURVIVING region that
#                 promotes automatically. The only mechanism that works on an
#                 UNPLANNED region kill, because it is the only one that does
#                 not depend on anything in the region that died.
#   manual      — nothing region-local acts; promotion is the sovereign-admin's
#                 RUNBOOKS §6.1 action. Honest, and the RTO is human response
#                 time. Must be DECLARED by the chart so the gap is visible on
#                 the live object, never inferred from an absent promoter.
#   continuum   — ADR-0015 D8. The Continuum delivered from Git in EVERY
#                 region's file of the Application (bp-continuum, slot 62,
#                 `region-role: every-region`, never suspended in a follower)
#                 commits the move as ordered per-region Git commits of
#                 `openova.topology.primary` (+ the CNPG promotionToken), and
#                 detects region loss from the survivor. The CHART therefore
#                 must render no actor of its own — its render must be a pure
#                 function of the committed fields, or it would fight
#                 Continuum's commits.
#
# History: before D8, `continuum` was refused here because continuum-controller
# was a single region-A Deployment (hw292 2026-08-06: region-B carried neither
# the controller nor the CRD) that died with the region it was meant to fail
# away from (#5137). The `continuum` case below therefore also asserts the
# slot-62 HelmRelease is the every-region shape, so that regression re-opens
# this gate rather than silently making the declaration a lie again.
# ─────────────────────────────────────────────────────────────────────────────
REGISTRY="
platform/cnpg-pair/chart/templates/replica-cluster.yaml|platform/cnpg-pair/chart|dr-promoter
platform/cnpg-pair/chart/templates/primary-cluster.yaml|platform/cnpg-pair/chart|dr-promoter
platform/postgres/chart/templates/replica-cluster.yaml|platform/postgres/chart|dr-promoter
platform/postgres/chart/templates/cluster.yaml|platform/postgres/chart|dr-promoter
platform/wordpress-tenant/chart/templates/cnpg-cluster.yaml|platform/wordpress-tenant/chart|continuum
"
# platform/postgres/chart/templates/cluster.yaml joined the registry with #6149:
# in the DEMOTED (rejoin) shape the primary half renders as a pg_basebackup
# replica of region-B, so it is a standby-half renderer by the same structural
# signature the detector uses — exactly as bp-cnpg-pair's primary-cluster.yaml
# has been since #5245.

echo "══ Phase 1 — registry completeness ══"

# Structural signature of a standby half: the CNPG `pg_basebackup` bootstrap
# stanza. A Cluster CR that seeds itself from ANOTHER Cluster is one half of a
# pair, and the other half is somewhere it can be lost.
mapfile -t FOUND < <(
  grep -rlE '^[[:space:]]*pg_basebackup:' --include='*.yaml' --include='*.tpl' \
    platform products 2>/dev/null | sort
)

registered_paths="$(echo "$REGISTRY" | awk -F'|' 'NF{print $1}' | sort)"

if [ "${#FOUND[@]}" -eq 0 ]; then
  fail "detector matched ZERO templates — the pg_basebackup signature changed and
        this gate is now vacuous. Fix the detector before trusting a green run."
fi

for f in "${FOUND[@]}"; do
  if echo "$registered_paths" | grep -qxF "$f"; then
    continue
  fi
  fail "UNREGISTERED DR pair: $f
        This template renders a CNPG standby half (bootstrap.pg_basebackup).
        Something must promote that standby when its source region dies, or the
        database is read-only for the whole outage (#5623, hw292 G12). Add it to
        REGISTRY in this script with its mechanism and add a Phase-2 case that
        PROVES the mechanism renders."
done

for line in $REGISTRY; do
  [ -z "$line" ] && continue
  path="${line%%|*}"
  if [ ! -f "$path" ]; then
    fail "REGISTERED path does not exist: $path (registry rot — remove or fix it)"
  elif ! grep -qE '^[[:space:]]*pg_basebackup:' "$path"; then
    fail "REGISTERED path no longer renders a standby half: $path
        It is registered as a DR pair but emits no bootstrap.pg_basebackup. If
        the pair was removed, drop the registry line; if the shape changed, the
        Phase-1 detector needs updating — do not leave a stale rubber stamp."
  fi
done

# Every registered CHART must have a Phase-2 case. Registering without proving
# is the rubber stamp this gate exists to prevent.
registered_charts="$(echo "$REGISTRY" | awk -F'|' 'NF{print $2}' | sort -u)"
CASED_CHARTS="platform/cnpg-pair/chart platform/postgres/chart platform/wordpress-tenant/chart"
for c in $registered_charts; do
  case " $CASED_CHARTS " in
    *" $c "*) ;;
    *) fail "REGISTERED chart has no Phase-2 behavior case: $c
        A registry entry with nothing rendering it is a claim, not a proof. Add
        a case below and list the chart in CASED_CHARTS." ;;
  esac
done

[ "$fails" -eq 0 ] && pass "registry covers every standby-half renderer (${#FOUND[@]} template(s)), and every registered chart has a behavior case"

# ─────────────────────────────────────────────────────────────────────────────
echo
echo "══ Phase 2 — behavior ══"

# assert_promoter_case <label> <render file> — the dr-promoter contract.
# A promoter only counts if it is SAFE: #5178 positive-liveness gate, #5220
# steady-state arm gate, #5125-D1 HelmRelease desired-state seam.
assert_promoter_case() {
  local label="$1" f="$2"
  local deploys
  deploys="$(promoter_deployments "$f")"
  case "$deploys" in
    __PARSE_ERROR__*) fail "$label: render did not parse as YAML — $deploys"; return ;;
  esac
  if [ -z "$deploys" ]; then
    fail "$label: NO dr-promoter Deployment in the standby-side render.
        This is the #5623 defect exactly: a standby that nothing region-local
        will ever promote. Region-B held one DR actor for four pairs on hw292."
    return
  fi
  pass "$label: renders a dr-promoter Deployment ($(echo "$deploys" | tr '\n' ' '))"

  # #5178 — POSITIVE proof region-A is gone, not merely unreachable-from-
  # replication. Asserted on the VALUE: the key renders either way (#5639).
  local live; live="$(env_value "$f" PRIMARY_LIVENESS_ENABLED)"
  if [ "$live" = "true" ]; then
    pass "$label: #5178 positive region-A liveness gate ARMED (PRIMARY_LIVENESS_ENABLED=true)"
  else
    fail "$label: #5178 liveness gate not armed — PRIMARY_LIVENESS_ENABLED=${live:-<empty/absent>}, want \"true\".
        With peer ClusterMesh names wired the promoter MUST positively probe the
        primary before promoting; without it a link flap reads as a region death
        and the promoter false-promotes (hw266 TL2→TL25, hw273)."
  fi

  # #5220 — ineligible to promote until continuous streaming has been OBSERVED.
  local arm; arm="$(env_value "$f" STEADY_STATE_ARM_SECONDS)"
  if [ -n "$arm" ] && [ "$arm" -gt 0 ] 2>/dev/null; then
    pass "$label: #5220 steady-state arm gate set (STEADY_STATE_ARM_SECONDS=$arm)"
  else
    fail "$label: #5220 arm gate missing/zero — STEADY_STATE_ARM_SECONDS=${arm:-<empty/absent>}, want a positive integer.
        A never-converged pair must never be promotable, else a convergence-time
        transient is cemented by the suspend latch (hw273)."
  fi

  # #5125-D1 — the promote must ride the HelmRelease DESIRED state. A live
  # Cluster-CR patch is reverted by flux drift-correction mid-outage (hw256).
  local hr; hr="$(env_value "$f" HR_NAME)"
  if [ -n "$hr" ]; then
    pass "$label: #5125-D1 HelmRelease desired-state promote seam wired (HR_NAME=$hr)"
  else
    fail "$label: no HR_NAME — the promoter has no HelmRelease desired-state seam.
        Promoting by patching the live Cluster CR is reverted by flux drift
        correction mid-outage (#5125-D1, hw256 G12)."
  fi
}

# assert_no_promoter <label> <render file> <why>
assert_no_promoter() {
  local label="$1" f="$2" why="$3"
  local deploys; deploys="$(promoter_deployments "$f")"
  if [ -z "$deploys" ]; then
    pass "$label: no promoter rendered ($why)"
  else
    fail "$label: promoter rendered where it MUST NOT be ($why) — got: $(echo "$deploys" | tr '\n' ' ')"
  fi
}

# ── bp-cnpg-pair — mechanism: dr-promoter ────────────────────────────────────
# The reference implementation, green on hw292. This case is the VACUITY
# CONTROL: it passes on the pre-#5675 tree AND on today's tree, so a blanket
# failure (bad helm, bad detector, empty renders) cannot satisfy this gate.
ensure_deps platform/cnpg-pair/chart
if helm template dr platform/cnpg-pair/chart \
  --set cnpgPair.enabled=true \
  --set cnpgPair.side=replica \
  --set cnpgPair.primary.region="$A" \
  --set cnpgPair.replica.region="$B" \
  --set cnpgPair.image.tag=16.3-23 \
  --set 'cnpgPair.networkPolicy.crossRegionPeerClusters={peer-mesh}' \
  --api-versions postgresql.cnpg.io/v1 > "$TMP/cnpg-pair-replica.yaml" 2>"$TMP/cnpg-pair-replica.err"; then
  assert_promoter_case "bp-cnpg-pair side=replica" "$TMP/cnpg-pair-replica.yaml"
else
  fail "bp-cnpg-pair side=replica render errored: $(tail -2 "$TMP/cnpg-pair-replica.err")"
fi

# Negative twin — async has NO synchronous data fence, so an automatic promote
# could fork committed data. The promoter must NOT render.
if helm template dr platform/cnpg-pair/chart \
  --set cnpgPair.enabled=true \
  --set cnpgPair.side=replica \
  --set cnpgPair.primary.region="$A" \
  --set cnpgPair.replica.region="$B" \
  --set cnpgPair.image.tag=16.3-23 \
  --set cnpgPair.replication.mode=async \
  --set 'cnpgPair.networkPolicy.crossRegionPeerClusters={peer-mesh}' \
  --api-versions postgresql.cnpg.io/v1 > "$TMP/cnpg-pair-async.yaml" 2>/dev/null; then
  assert_no_promoter "bp-cnpg-pair side=replica replication=async" "$TMP/cnpg-pair-async.yaml" \
    "async has no synchronous fence — automatic promotion could fork committed data"
else
  fail "bp-cnpg-pair async render errored (expected a successful render with no promoter)"
fi

# ── bp-postgres — mechanism: dr-promoter (the three shared-pg pairs) ──────────
# This case FAILS on the pre-#5675 tree (bp-postgres ≤0.2.17 shipped no
# promoter) and PASSES from 0.2.18. It is the #5623 regression lock.
ensure_deps platform/postgres/chart
cat > "$TMP/pg-replica.values.yaml" <<YAML
enabled: true
instance: { name: shared-pg, namespace: shared-data }
topology:
  mode: active-hot-standby
  side: replica
  instances: 3
  primary: { region: $A }
  replica: { region: $B }
  networkPolicy:
    crossRegionPeerClusters: [peer-mesh]
databases:
  - name: keycloak
    owner: keycloak
    reflect: { secretName: keycloak-database-secret, namespaces: [keycloak] }
YAML
if helm template shared-pg platform/postgres/chart -f "$TMP/pg-replica.values.yaml" \
  --namespace shared-data --api-versions postgresql.cnpg.io/v1 \
  > "$TMP/pg-replica.yaml" 2>"$TMP/pg-replica.err"; then
  assert_promoter_case "bp-postgres side=replica (shared-pg)" "$TMP/pg-replica.yaml"
else
  fail "bp-postgres side=replica render errored: $(tail -2 "$TMP/pg-replica.err")"
fi

# Negative twin — async, same reasoning as bp-cnpg-pair.
if helm template shared-pg platform/postgres/chart -f "$TMP/pg-replica.values.yaml" \
  --set topology.replication.mode=async \
  --namespace shared-data --api-versions postgresql.cnpg.io/v1 \
  > "$TMP/pg-async.yaml" 2>/dev/null; then
  assert_no_promoter "bp-postgres side=replica replication=async" "$TMP/pg-async.yaml" \
    "async has no synchronous fence — automatic promotion could fork committed data"
else
  fail "bp-postgres async render errored (expected a successful render with no promoter)"
fi

# ── bp-wordpress-tenant — mechanism: continuum (ADR-0015 D8) ─────────────────
# This chart renders a cross-region standby (the non-primary region's release)
# and ships no promoter: promotion is a Git commit of openova.topology.primary
# (+ database.topology.promotionToken) made by the every-region Continuum
# (`continuum`) or by the sovereign-admin (`manual`). SILENCE is not allowed.
# The gate requires the declaration to be (a) mandatory, (b) visible on the
# standby object, (c) FREE OF ANY CHART-SIDE ACTOR — declaring continuum must
# change nothing but the stamp, so nothing in the render fights Continuum's
# commits — and (d) the render must FOLLOW the committed fields, phase by
# phase, which is the only lever Continuum has.
ensure_deps platform/wordpress-tenant/chart
WP_COMMON=(
  --namespace acme
  --set smeDomain=t99.omani.works
  --set keycloak.realmURL=https://kc.example/realms/x
  --set keycloak.clientSecretName=wp-oidc
  --set adminUser.email=admin@example.org
  --set openova.topology.mode=active-passive
)
# ADR-0015 D2 (0.5.0): the standby is the PEER region's own release —
# rendered with self=B, primary=A from the shared openova.topology contract.
WP_BASE=(
  "${WP_COMMON[@]}"
  --set openova.topology.self="$B"
  --set openova.topology.primary="$A"
  --set "openova.topology.peers={$A}"
)

# wp_cluster <file> <field> — one field of the rendered CNPG Cluster, parsed as
# YAML (never grepped): role|self|primary|token|mechanism|instance|ns|wp_replicas
wp_field() {
  python3 - "$1" "$2" <<'PYEOF'
import sys, yaml
docs = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
f = sys.argv[2]
for d in docs:
    md = d.get('metadata') or {}
    if d.get('kind') == 'Cluster':
        ann = md.get('annotations') or {}
        rep = (d.get('spec') or {}).get('replica') or {}
        v = {
            'role': ann.get('catalyst.openova.io/cnpg-pair-role', ''),
            'self': rep.get('self', ''),
            'name': md.get('name', ''),
            'primary': rep.get('primary', ''),
            'token': rep.get('promotionToken', ''),
            'mechanism': ann.get('catalyst.openova.io/dr-promotion-mechanism', ''),
            'instance': (md.get('labels') or {}).get('app.kubernetes.io/instance', ''),
            'ns': md.get('namespace', ''),
        }.get(f)
        if v is not None:
            print(v)
    if f == 'wp_replicas' and d.get('kind') == 'Deployment' and 'wordpress' in md.get('name', ''):
        print((d.get('spec') or {}).get('replicas', ''))
PYEOF
}

# Negative FIRST — an undeclared pair must FAIL CLOSED. Before #5623 this
# rendered happily and shipped a standby nothing would ever promote.
if helm template wp platform/wordpress-tenant/chart "${WP_BASE[@]}" \
  --api-versions postgresql.cnpg.io/v1 > "$TMP/wp-undeclared.yaml" 2>"$TMP/wp-undeclared.err"; then
  fail "bp-wordpress-tenant: active-passive rendered with NO promotion mechanism declared.
        This is the #5623 defect on the customer-facing Blueprint: a paying
        Organization gets an \"HA\" database whose standby nothing promotes, and
        the render says nothing about it. The chart must fail closed."
elif grep -q 'database.topology.promotion.mechanism' "$TMP/wp-undeclared.err"; then
  pass "bp-wordpress-tenant: undeclared multi-region pair FAILS CLOSED naming database.topology.promotion.mechanism"
else
  fail "bp-wordpress-tenant: undeclared render failed, but not with the promotion-mechanism error —
        got: $(tail -2 "$TMP/wp-undeclared.err")"
fi

# Positive — both declarations render, each stamped on the STANDBY object so an
# operator can answer "who promotes this?" from the live cluster, not from git.
for mech in continuum manual; do
  if helm template wp platform/wordpress-tenant/chart "${WP_BASE[@]}" \
    --set database.topology.promotion.mechanism="$mech" \
    --api-versions postgresql.cnpg.io/v1 > "$TMP/wp-$mech.yaml" 2>"$TMP/wp-$mech.err"; then
    stamped="$(wp_field "$TMP/wp-$mech.yaml" mechanism)"
    if [ "$stamped" = "$mech" ] && [ "$(wp_field "$TMP/wp-$mech.yaml" role)" = "replica" ]; then
      pass "bp-wordpress-tenant: standby Cluster CR stamped catalyst.openova.io/dr-promotion-mechanism=$mech"
    else
      fail "bp-wordpress-tenant: standby not stamped with the declared mechanism — got '${stamped:-<absent>}', want '$mech'.
        The declaration must be answerable from the live object; a values-file-only
        claim is invisible to the operator during an outage."
    fi
    assert_no_promoter "bp-wordpress-tenant mechanism=$mech" "$TMP/wp-$mech.yaml" \
      "the commit is made by $mech, never by a chart-side promoter"
  else
    fail "bp-wordpress-tenant mechanism=$mech render errored: $(tail -2 "$TMP/wp-$mech.err")"
  fi
done

# (c) NOTHING FIGHTS CONTINUUM — the continuum render must equal the manual
# render once the one stamp is normalised. Any extra object (a promoter, a
# Continuum or Lease of the chart's own, a Job that patches a Cluster) shows up
# as a diff here, whatever it is named.
if [ -s "$TMP/wp-continuum.yaml" ] && [ -s "$TMP/wp-manual.yaml" ]; then
  # (client-secret is randAlphaNum-per-render in sso-secret.yaml's lookup-or-
  # generate fallback — normalised too, it differs between ANY two renders.)
  sed -e 's/dr-promotion-mechanism: "continuum"/dr-promotion-mechanism: "X"/' \
    -e 's/^\(  client-secret: \).*/\1RANDOM/' "$TMP/wp-continuum.yaml" > "$TMP/wp-c.norm"
  sed -e 's/dr-promotion-mechanism: "manual"/dr-promotion-mechanism: "X"/' \
    -e 's/^\(  client-secret: \).*/\1RANDOM/' "$TMP/wp-manual.yaml" > "$TMP/wp-m.norm"
  if cmp -s "$TMP/wp-c.norm" "$TMP/wp-m.norm"; then
    pass "bp-wordpress-tenant: mechanism=continuum renders nothing but the stamp (no chart-side actor fights Continuum's commits)"
  else
    fail "bp-wordpress-tenant: mechanism=continuum renders MORE than the stamp — a chart-side actor would race Continuum's Git commits:
        $(diff "$TMP/wp-m.norm" "$TMP/wp-c.norm" | head -6)"
  fi
  # Continuum observes the Cluster by app.kubernetes.io/instance=<release> in
  # the HelmRelease's namespace and reads spec.replica.self == its own name
  # (core/controllers/continuum/internal/observe). Pin those VALUES.
  if [ "$(wp_field "$TMP/wp-continuum.yaml" instance)" = "wp" ] && [ "$(wp_field "$TMP/wp-continuum.yaml" ns)" = "acme" ] \
    && [ "$(wp_field "$TMP/wp-continuum.yaml" self)" = "$(wp_field "$TMP/wp-continuum.yaml" name)" ]; then
    pass "bp-wordpress-tenant: the standby Cluster is where Continuum observes it (instance=wp, ns=acme, replica.self=its name)"
  else
    fail "bp-wordpress-tenant: Continuum could not observe this Cluster (instance='$(wp_field "$TMP/wp-continuum.yaml" instance)' ns='$(wp_field "$TMP/wp-continuum.yaml" ns)' self='$(wp_field "$TMP/wp-continuum.yaml" self)' name='$(wp_field "$TMP/wp-continuum.yaml" name)')"
  fi
fi
# ...and a Cluster moved out of Continuum's sight must fail closed.
if helm template wp platform/wordpress-tenant/chart "${WP_BASE[@]}" \
  --set database.topology.promotion.mechanism=continuum --set database.cluster.namespace=elsewhere \
  --api-versions postgresql.cnpg.io/v1 > /dev/null 2>"$TMP/wp-ns.err"; then
  fail "bp-wordpress-tenant: mechanism=continuum accepted a CNPG Cluster outside the release namespace — Continuum would wait forever on a Cluster it cannot see"
elif grep -q 'needs the CNPG Cluster in the release namespace' "$TMP/wp-ns.err"; then
  pass "bp-wordpress-tenant: mechanism=continuum with the Cluster outside the release namespace fails closed"
else
  # Any OTHER render error (a missing dependency, a typo) must not count.
  fail "bp-wordpress-tenant: out-of-namespace render failed, but not with the Continuum-visibility error — got: $(tail -2 "$TMP/wp-ns.err")"
fi

# (d) THE RENDER FOLLOWS CONTINUUM'S COMMITS, phase by phase (the token value
# is arbitrary; CNPG compares it to the one the old primary published).
wp_phase() { # <out> <self> <primary> [token]
  helm template wp platform/wordpress-tenant/chart "${WP_COMMON[@]}" \
    --set openova.topology.self="$2" --set openova.topology.primary="$3" \
    --set "openova.topology.peers={$([ "$2" = "$A" ] && echo "$B" || echo "$A")}" \
    --set database.topology.promotion.mechanism=continuum \
    ${4:+--set database.topology.promotionToken="$4"} \
    --api-versions postgresql.cnpg.io/v1 > "$1" 2>"$1.err"
}
TOK="tok-from-A-demotion"
# traffic-off: A's file gets primary=B → A demotes (not primary), WordPress 0,
# and A never renders a promotion token even if one is present in its values.
if wp_phase "$TMP/wp-p1.yaml" "$A" "$B" "$TOK"; then
  if [ "$(wp_field "$TMP/wp-p1.yaml" role)" = "replica" ] && [ "$(wp_field "$TMP/wp-p1.yaml" wp_replicas)" = "0" ] \
    && [ -z "$(wp_field "$TMP/wp-p1.yaml" token)" ]; then
    pass "bp-wordpress-tenant: traffic-off commit (A: primary=B) demotes A, scales WordPress to 0, renders no token on A"
  else
    fail "bp-wordpress-tenant: traffic-off commit not followed on A (role='$(wp_field "$TMP/wp-p1.yaml" role)' replicas='$(wp_field "$TMP/wp-p1.yaml" wp_replicas)' token='$(wp_field "$TMP/wp-p1.yaml" token)')"
  fi
else
  fail "bp-wordpress-tenant: traffic-off render errored: $(tail -2 "$TMP/wp-p1.yaml.err")"
fi
# data-primary: B's file gets primary=B + promotionToken → B is the primary
# member carrying exactly that token, WordPress scaled up.
if wp_phase "$TMP/wp-p2.yaml" "$B" "$B" "$TOK"; then
  if [ "$(wp_field "$TMP/wp-p2.yaml" role)" = "primary" ] && [ "$(wp_field "$TMP/wp-p2.yaml" token)" = "$TOK" ] \
    && [ "$(wp_field "$TMP/wp-p2.yaml" self)" = "$(wp_field "$TMP/wp-p2.yaml" primary)" ] \
    && [ "$(wp_field "$TMP/wp-p2.yaml" wp_replicas)" != "0" ]; then
    pass "bp-wordpress-tenant: data-primary commit (B: primary=B + token) promotes B with exactly that token, WordPress up"
  else
    fail "bp-wordpress-tenant: data-primary commit not followed on B (role='$(wp_field "$TMP/wp-p2.yaml" role)' token='$(wp_field "$TMP/wp-p2.yaml" token)' replicas='$(wp_field "$TMP/wp-p2.yaml" wp_replicas)')"
  fi
else
  fail "bp-wordpress-tenant: data-primary render errored: $(tail -2 "$TMP/wp-p2.yaml.err")"
fi

# The actor must EXIST in every region, or declaring it is the pre-D8 lie
# (#5137): slot 62 is the every-region shape and never suspended.
SLOT62=clusters/_template/bootstrap-kit/62-bp-continuum.yaml
slot62="$(python3 - "$SLOT62" <<'PYEOF'
import sys, yaml
for d in yaml.safe_load_all(open(sys.argv[1])):
    if d and d.get('kind') == 'HelmRelease':
        lab = (d.get('metadata') or {}).get('labels') or {}
        spec = d.get('spec') or {}
        print(lab.get('catalyst.openova.io/region-role', ''), str(spec.get('suspend', False)).lower(), len(spec.get('dependsOn') or []))
PYEOF
)"
if [ "$slot62" = "every-region false 0" ]; then
  pass "slot 62 bp-continuum is the every-region shape (region-role=every-region, not suspended, no dependsOn) — continuum is a real actor in every region"
else
  fail "slot 62 bp-continuum is NOT the every-region shape (got '$slot62', want 'every-region false 0').
        bp-wordpress-tenant declares mechanism=continuum; a Continuum that is absent
        or suspended in a region dies with the region it should fail away from (#5137)."
fi

# A mechanism this chart cannot deliver must fail closed rather than assert a
# failover that will not happen.
for bogus in dr-promoter not-a-mechanism; do
  if helm template wp platform/wordpress-tenant/chart "${WP_BASE[@]}" \
    --set database.topology.promotion.mechanism="$bogus" \
    --api-versions postgresql.cnpg.io/v1 > /dev/null 2>"$TMP/wp-$bogus.err"; then
    fail "bp-wordpress-tenant: accepted mechanism=$bogus, which it does not ship.
        Declaring an undelivered mechanism is worse than declaring none — it
        reads as a failover guarantee in every audit that follows."
  elif grep -q "promotion.mechanism=.*$bogus" "$TMP/wp-$bogus.err"; then
    pass "bp-wordpress-tenant: mechanism=$bogus fails closed (chart ships no such actor)"
  else
    # Any OTHER render error (a missing dependency, a typo) must not count.
    fail "bp-wordpress-tenant: mechanism=$bogus render failed, but not with the mechanism error — got: $(tail -2 "$TMP/wp-$bogus.err")"
  fi
done

# ── singleton non-regression — the gate must not penalise non-DR installs ────
if helm template wp platform/wordpress-tenant/chart \
  --set smeDomain=t99.omani.works \
  --set keycloak.realmURL=https://kc.example/realms/x \
  --set keycloak.clientSecretName=wp-oidc \
  --set adminUser.email=admin@example.org \
  --api-versions postgresql.cnpg.io/v1 > "$TMP/wp-singleton.yaml" 2>/dev/null; then
  if grep -q 'dr-promotion-mechanism' "$TMP/wp-singleton.yaml"; then
    fail "bp-wordpress-tenant singleton leaked the DR promotion annotation (single-region installs must be byte-identical)"
  else
    pass "bp-wordpress-tenant singleton renders unchanged (no DR pair, no declaration required)"
  fi
else
  fail "bp-wordpress-tenant singleton render errored — the #5623 declaration must not affect non-DR installs"
fi

# ─────────────────────────────────────────────────────────────────────────────
echo
echo "══ Phase 3 — the PAIRING INVARIANT (#6149) ══"
#
# This phase exists because Phase 2 was GREEN on the tree that produced hw293.
# It proved every pair can PROMOTE and said nothing about coming back, so
# `bp-postgres` shipped four promoters against one failback and the G12 walk of
# 2026-08-11 left `shared-pg` (keycloak's database) and `shared-pg-b`
# permanently DUAL-WRITABLE on divergent timelines — both sides
# pg_is_in_recovery()=false, different row counts of the same table, nothing
# scheduled to reconcile them. A promoter without a failback is not a partial
# feature; it is a data-integrity hazard, and #5623 made the failure mode worse
# rather than better by shipping one alone.
#
# So the assertion is on the PAIR COUNT — promoters == failbacks — never on
# either alone. The two halves render in SEPARATE helm invocations
# (side=primary on cluster-A, side=replica on cluster-B), so both are rendered
# here and the census is taken across the catalog exactly as it was taken live.
#
# The exhaustive form of this check (per-pair census, the operator-gate
# symmetry, the vacuity control, the registry-rot detector) is
# tests/e2e/bootstrap-kit/dr_pair_symmetry_6149_test.go. This phase is the
# same claim in the gate whose promoter-only framing let the defect through.

# failback_deployments <file> — rendered Deployments carrying the
# `catalyst.openova.io/role: dr-failback` LABEL VALUE. Parsed as YAML, so a
# comment or a name substring can never satisfy it (#5639).
failback_deployments() {
  python3 - "$1" <<'PYEOF'
import sys, yaml
try:
    docs = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
except Exception as e:
    print(f"__PARSE_ERROR__ {e}")
    sys.exit(0)
for d in docs:
    if d.get('kind') == 'Deployment':
        md = d.get('metadata') or {}
        if (md.get('labels') or {}).get('catalyst.openova.io/role') == 'dr-failback':
            print(md.get('name', ''))
PYEOF
}

total_promoters=0
total_failbacks=0

# count_pair <label> <promoter-side render> <failback-side render>
count_pair() {
  local label="$1" pfile="$2" ffile="$3"
  local p f np nf
  p="$(promoter_deployments "$pfile")"
  f="$(failback_deployments "$ffile")"
  np=$([ -n "$p" ] && echo "$p" | wc -l || echo 0)
  nf=$([ -n "$f" ] && echo "$f" | wc -l || echo 0)
  total_promoters=$((total_promoters + np))
  total_failbacks=$((total_failbacks + nf))
  if [ "$np" -eq "$nf" ] && [ "$np" -gt 0 ]; then
    pass "$label: promoters=$np failbacks=$nf — a pair that can be promoted can fail back"
  else
    fail "$label: PAIR ASYMMETRY — promoters=$np failbacks=$nf.
        This is the #6149 geometry: region B promotes on a region kill and
        region A returns as a SECOND writable primary on a divergent timeline
        with nothing scheduled to reconcile them. Measured live on hw293
        2026-08-11 — shared-pg and shared-pg-b, both left dual-writable."
  fi
}

# bp-cnpg-pair — THE CONTROL. It shares the suspect property (a CNPG DR pair
# rendering a region-B promoter) and already carried a failback before #6149, so
# it must stay green and unchanged. A red control means this phase cannot tell a
# real green from a vacuous one.
if helm template dr platform/cnpg-pair/chart \
  --set cnpgPair.enabled=true \
  --set cnpgPair.side=primary \
  --set cnpgPair.primary.region="$A" \
  --set cnpgPair.replica.region="$B" \
  --set cnpgPair.image.tag=16.3-23 \
  --set 'cnpgPair.networkPolicy.crossRegionPeerClusters={peer-mesh}' \
  --api-versions postgresql.cnpg.io/v1 > "$TMP/cnpg-pair-primary.yaml" 2>"$TMP/cnpg-pair-primary.err"; then
  count_pair "bp-cnpg-pair (CONTROL)" "$TMP/cnpg-pair-replica.yaml" "$TMP/cnpg-pair-primary.yaml"
else
  fail "bp-cnpg-pair side=primary render errored: $(tail -2 "$TMP/cnpg-pair-primary.err")"
fi

# bp-postgres — the three shared-pg instances. shared-pg carries keycloak.
if helm template shared-pg platform/postgres/chart -f "$TMP/pg-replica.values.yaml" \
  --set topology.side=primary \
  --namespace shared-data --api-versions postgresql.cnpg.io/v1 \
  > "$TMP/pg-primary.yaml" 2>"$TMP/pg-primary.err"; then
  count_pair "bp-postgres (shared-pg)" "$TMP/pg-replica.yaml" "$TMP/pg-primary.yaml"
else
  fail "bp-postgres side=primary render errored: $(tail -2 "$TMP/pg-primary.err")"
fi

# The aggregate census — the exact number the hw293 walk produced (4 vs 1).
if [ "$total_promoters" -eq "$total_failbacks" ] && [ "$total_promoters" -gt 0 ]; then
  pass "catalog census: dr-promoter=$total_promoters dr-failback=$total_failbacks"
else
  fail "CATALOG CENSUS ASYMMETRY — dr-promoter=$total_promoters dr-failback=$total_failbacks.
        hw293 measured 4 and 1, and that asymmetry IS the whole explanation of
        its two permanently dual-writable databases."
fi

# Operator-gate symmetry: disabling exactly ONE half must FAIL THE RENDER. The
# structural preconditions are shared (bp-postgres.drPairCapable), so this is
# the only remaining way to express the hazard by hand — in both directions.
for pair in "topology.failback.enabled|promoter without a failback (the #6149 hazard)" \
            "topology.autoPromote.enabled|failback without a promoter (the inverse trap)"; do
  key="${pair%%|*}"; why="${pair#*|}"
  if helm template shared-pg platform/postgres/chart -f "$TMP/pg-replica.values.yaml" \
    --set "${key}=false" \
    --namespace shared-data --api-versions postgresql.cnpg.io/v1 \
    > /dev/null 2>"$TMP/pg-asym.err"; then
    fail "bp-postgres accepted ${key}=false — $why. The chart must fail closed."
  elif grep -q 'DR PAIRING INVARIANT VIOLATED' "$TMP/pg-asym.err"; then
    pass "bp-postgres: ${key}=false fails closed naming both keys ($why)"
  else
    fail "bp-postgres: ${key}=false failed, but not with the pairing-invariant error —
        got: $(tail -2 "$TMP/pg-asym.err")"
  fi
done

echo
if [ "$fails" -ne 0 ]; then
  echo "══ $fails failure(s) — a DR pair in this catalog cannot promote its standby, or cannot come back ══"
  exit 1
fi
echo "══ OK — every DR pair declares a promotion mechanism, renders it, and can fail back ══"
