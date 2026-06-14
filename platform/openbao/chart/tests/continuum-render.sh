#!/usr/bin/env bash
# bp-openbao continuum-render test (DR / #3375 DoD-8, #3492).
#
# Verifies the openbao DR contract (templates/continuum.yaml) — the
# Continuum CR that tells the bp-continuum engine to promote bp-openbao via
# `raft-transition` (`bao operator raft snapshot restore` +
# `transition-to-primary` on the surviving standby) on a region-kill.
#
#   Case 1 — default render: NO Continuum CR (skip-render, never `{{ fail }}`
#            per #402). The default single-region render is byte-identical.
#   Case 2 — continuum.enabled=true but role=primary: STILL no CR. The CR
#            describes promotion of the STANDBY, so it belongs only on the
#            secondary (region-B) cluster.
#   Case 3 — continuum.enabled=true + role=secondary: the Continuum CR
#            renders with switchover.mechanism=raft-transition + the
#            raftTransition target (namespace / podSelector / snapshotPath).
#
# Usage: bash tests/continuum-render.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

echo "[continuum-render] Case 1: default render produces NO Continuum CR"
helm template smoke-bao . > "$TMP/default.yaml"
if grep -qE "^kind: Continuum$" "$TMP/default.yaml"; then
  echo "FAIL: default render contains a Continuum CR — skip-render is broken." >&2
  exit 1
fi
echo "  PASS"

echo "[continuum-render] Case 2: continuum.enabled=true + role=primary still emits NO CR (CR belongs on the secondary)"
helm template smoke-bao . \
  --set continuum.enabled=true \
  --set snapshotReplication.role=primary \
  --set continuum.primaryRegion=hz-fsn-rtz-prod \
  --set continuum.standbyRegion=hz-hel-rtz-prod \
  --set continuum.pdmZone=t99.omani.works > "$TMP/primary.yaml"
if grep -qE "^kind: Continuum$" "$TMP/primary.yaml"; then
  echo "FAIL: continuum.enabled + role=primary rendered a Continuum CR — it must only render on the secondary." >&2
  exit 1
fi
echo "  PASS"

echo "[continuum-render] Case 3: continuum.enabled=true + role=secondary emits the raft-transition Continuum CR"
helm template smoke-bao . \
  --set continuum.enabled=true \
  --set snapshotReplication.enabled=true \
  --set snapshotReplication.role=secondary \
  --set continuum.primaryRegion=hz-fsn-rtz-prod \
  --set continuum.standbyRegion=hz-hel-rtz-prod \
  --set continuum.pdmZone=t99.omani.works > "$TMP/secondary.yaml"

cr_block="$(awk '/^---$/{f=0} /^kind: Continuum$/{f=1} f' "$TMP/secondary.yaml")"
if [ -z "$cr_block" ]; then
  echo "FAIL: role=secondary did not render the Continuum CR." >&2
  exit 1
fi
# The mechanism MUST be raft-transition (NOT the cnpg-pair default).
if ! echo "$cr_block" | grep -q "mechanism: raft-transition"; then
  echo "FAIL: Continuum CR is missing switchover.mechanism=raft-transition." >&2
  echo "$cr_block" >&2
  exit 1
fi
# The raftTransition target must carry the standby Pod selector + snapshot path.
if ! echo "$cr_block" | grep -q 'podSelector: "app.kubernetes.io/name=openbao"'; then
  echo "FAIL: Continuum CR raftTransition.podSelector is wrong/missing." >&2
  exit 1
fi
if ! echo "$cr_block" | grep -q 'snapshotPath: "/snapshots/latest.snap"'; then
  echo "FAIL: Continuum CR raftTransition.snapshotPath is wrong/missing (must match the snapshot-replication restore-staging path)." >&2
  exit 1
fi
# applicationRef must be openbao, and the regions wired through.
if ! echo "$cr_block" | grep -q "applicationRef: openbao"; then
  echo "FAIL: Continuum CR applicationRef is not openbao." >&2
  exit 1
fi
if ! echo "$cr_block" | grep -q "hz-fsn-rtz-prod"; then
  echo "FAIL: Continuum CR did not wire the primaryRegion." >&2
  exit 1
fi
echo "  PASS"

echo "[continuum-render] All bp-openbao continuum-render gates green."
