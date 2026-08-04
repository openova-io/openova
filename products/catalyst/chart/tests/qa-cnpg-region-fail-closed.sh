#!/usr/bin/env bash
# bp-catalyst-platform — qa-fixtures CNPG region fail-closed gate (#5639 class).
#
# WHAT THIS ENCODES
# -----------------
# templates/qa-fixtures/cnpg-clusters-qa.yaml renders TWO postgresql.cnpg.io
# Cluster CRs, each with a REQUIRED nodeAffinity on the `openova.io/region`
# NODE LABEL. A required nodeAffinity whose value no node carries is not a
# scheduling delay — it is unschedulable FOREVER, and CNPG/Helm never notice:
# the render is valid YAML, the API server accepts it, and the HelmRelease
# reports "install succeeded" over two permanently-Pending Pods. That is the
# #5639 failure mode, found live on hw292 where a per-Org bp-postgres primary
# sat Pending for 7+ hours behind a green HelmRelease.
#
# These fixtures previously defaulted BOTH regions to the literal
# `hz-fsn-rtz-prod` — a Hetzner label — in the chart AND in the bootstrap-kit
# slot (`${QA_CNPG_PRIMARY_REGION:-hz-fsn-rtz-prod}`, for a substitution
# variable nothing in this repo has ever defined). On every Huawei Sovereign
# that literal won and pinned a region no node carries. A near-miss region is
# exactly as unschedulable as an empty one.
#
# WHY THIS TEST IS SHAPED THE WAY IT IS
# -------------------------------------
# The defect #5639 fixed survived because the pre-existing bp-postgres render
# assertion was `grep -q 'key: openova.io/region'` — the KEY renders whether
# the value is the real region or `""`, so the assertion passed on the broken
# output. Every assertion below therefore pins the rendered **VALUE**, and the
# suite is vacuity-checked in BOTH directions: the negative cases are required
# to FAIL the render, the positive cases are required to carry the exact
# region string. A guard observed only passing is not yet known to be a guard.
#
# Test framework: pure `helm template` + grep on rendered YAML, matching the
# established tests/api-single-writer-dr-contract.sh pattern. Picked up
# automatically by .github/workflows/blueprint-release.yaml ("Run chart
# integration tests (chart/tests/*.sh)").
#
# Usage: bash tests/qa-cnpg-region-fail-closed.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

ONLY="templates/qa-fixtures/cnpg-clusters-qa.yaml"
PRIMARY="hw-me-east-215-a-rtz-prod"
REPLICA="hw-me-east-215-b-rtz-prod"

fails=0
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; fails=$((fails + 1)); }

render() { # render <outfile> <extra helm --set args...>
  local out="$1"
  shift
  helm template qa . --set qaFixtures.enabled=true -s "$ONLY" "$@" >"$out" 2>&1
}

echo "── #5639 class — qa-fixtures CNPG region fail-closed ──"

# ─────────────────────────────────────────────────────────────────────────
# NEGATIVE 1 — no region supplied at all ⇒ the render MUST FAIL.
# This is the case the bootstrap-kit produced on every Huawei Sovereign.
# ─────────────────────────────────────────────────────────────────────────
if render "$TMP/neg-unset.out"; then
  fail "region unset: render SUCCEEDED — the guard is gone (this is #5639)"
  grep -n 'openova.io/region' "$TMP/neg-unset.out" | head -4 | sed 's/^/        /'
else
  if grep -q 'qaFixtures.cnpgPrimaryRegion is REQUIRED' "$TMP/neg-unset.out"; then
    pass "region unset: render FAILS and names the missing key"
  else
    fail "region unset: render failed but NOT on the region guard — check the message"
    head -5 "$TMP/neg-unset.out" | sed 's/^/        /'
  fi
fi

# ─────────────────────────────────────────────────────────────────────────
# NEGATIVE 2 — region explicitly empty ⇒ the render MUST FAIL.
# Emitting `openova.io/region In [""]` is the literal #5639 defect.
# ─────────────────────────────────────────────────────────────────────────
if render "$TMP/neg-empty.out" --set qaFixtures.cnpgPrimaryRegion=""; then
  fail "region=\"\": render SUCCEEDED — an empty selector matches no node, ever"
  grep -n 'openova.io/region' "$TMP/neg-empty.out" | head -4 | sed 's/^/        /'
else
  pass "region=\"\": render FAILS instead of emitting an empty selector"
fi

# ─────────────────────────────────────────────────────────────────────────
# POSITIVE 1 — real region ⇒ renders, and the VALUE is that exact region.
# This is the control: it proves the guard does not simply always fire.
# ─────────────────────────────────────────────────────────────────────────
if render "$TMP/pos-single.out" --set qaFixtures.cnpgPrimaryRegion="$PRIMARY"; then
  pass "region=$PRIMARY: render succeeds (guard does not over-fire)"

  # The nodeAffinity VALUE — not the key. `values:` is a block list, so the
  # region is on the line after it inside the matchExpressions term.
  got_aff="$(grep -A3 'key: openova.io/region' "$TMP/pos-single.out" |
    grep -E '^[[:space:]]+- [a-z0-9-]+$' | awk '{print $2}' | sort -u | tr '\n' ' ')"
  if [ "$(echo "$got_aff" | tr -d ' ')" = "$PRIMARY" ]; then
    pass "nodeAffinity VALUE on both Clusters == $PRIMARY (replica falls back to primary)"
  else
    fail "nodeAffinity VALUE is [$got_aff], expected only $PRIMARY"
  fi

  got_lbl="$(grep -E '^\s+openova\.io/region:' "$TMP/pos-single.out" |
    awk '{print $2}' | tr -d '"' | sort -u | tr '\n' ' ')"
  if [ "$(echo "$got_lbl" | tr -d ' ')" = "$PRIMARY" ]; then
    pass "Cluster LABEL openova.io/region == $PRIMARY on both halves"
  else
    fail "Cluster LABEL is [$got_lbl], expected only $PRIMARY"
  fi

  if grep -qE 'openova\.io/region: *""|- *""' "$TMP/pos-single.out"; then
    fail "an EMPTY region string reached the render despite the guard"
  else
    pass "no empty region string anywhere in the successful render"
  fi
else
  fail "region=$PRIMARY: render FAILED — the guard over-fires on a valid region"
  head -5 "$TMP/pos-single.out" | sed 's/^/        /'
fi

# ─────────────────────────────────────────────────────────────────────────
# POSITIVE 2 — distinct primary/replica regions are both honoured VERBATIM.
# Proves the replica half reads its own knob rather than silently reusing
# the primary when one IS supplied.
# ─────────────────────────────────────────────────────────────────────────
if render "$TMP/pos-pair.out" \
  --set qaFixtures.cnpgPrimaryRegion="$PRIMARY" \
  --set qaFixtures.cnpgReplicaRegion="$REPLICA"; then
  ok=1
  grep -q "cnpg-role: primary" "$TMP/pos-pair.out" || ok=0
  # primary Cluster block ends where the replica Cluster block begins
  awk '/cnpg-role: replica/{r=1} {if(r) print}' "$TMP/pos-pair.out" >"$TMP/replica-half.out"
  awk '/cnpg-role: replica/{exit} {print}' "$TMP/pos-pair.out" >"$TMP/primary-half.out"
  grep -q "$PRIMARY" "$TMP/primary-half.out" || ok=0
  grep -q "$REPLICA" "$TMP/replica-half.out" || ok=0
  grep -q "$REPLICA" "$TMP/primary-half.out" && ok=0
  if [ "$ok" = "1" ]; then
    pass "distinct regions: primary half carries $PRIMARY, replica half carries $REPLICA"
  else
    fail "distinct regions: the two halves did not carry their own region values"
  fi
else
  fail "distinct regions: render FAILED unexpectedly"
  head -5 "$TMP/pos-pair.out" | sed 's/^/        /'
fi

# ─────────────────────────────────────────────────────────────────────────
# SOURCE GUARD — no provider-specific region literal may come back as a
# default. The whole defect was a Hetzner label hardcoded as the fallback
# on a chart that installs on Huawei, Hetzner and Contabo alike.
# ─────────────────────────────────────────────────────────────────────────
if grep -nE 'cnpg(Primary|Replica)Region[^#]*\|[[:space:]]*default[[:space:]]*"(hz|hw)-' "$ONLY"; then
  fail "a provider-specific region literal is back as a chart default"
else
  pass "no provider-specific region literal defaulted in $ONLY"
fi

echo
if [ "$fails" -ne 0 ]; then
  echo "qa-cnpg-region-fail-closed: $fails FAILED"
  exit 1
fi
echo "qa-cnpg-region-fail-closed: all checks passed"
