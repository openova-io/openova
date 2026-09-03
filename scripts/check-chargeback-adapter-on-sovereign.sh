#!/usr/bin/env bash
# check-chargeback-adapter-on-sovereign.sh — #6846
#
# The OpenOva adapter is the reason the Sovereign placement of chargeback
# exists (ADR-0014 D2 case 1, EPIC #6723 lane D): it syncs the Sovereign's
# Organizations in as customers and runs the in-cluster platform collector.
#
# The chart defaults adapter.enabled to false — correct for the standalone and
# operator-central profiles, which never touch a cluster. The Sovereign kit slot
# must turn it ON, and for a year-shaped window it did not.
#
# The failure is SILENT and looks healthy. automountServiceAccountToken follows
# adapter.enabled, so with it false no token is mounted, rest.InClusterConfig()
# fails, and the binary logs `openova adapter off` at INFO and carries on. Pods
# are Ready, the HelmRelease is Ready, the collector ticks `result: ok` — with
# `sources: 0` forever. A signed-in operator sees an app with no customers.
#
# Measured on hw307 2026-09-03, exactly that.
set -euo pipefail

CB="${1:-clusters/_template/bootstrap-kit/13f-bp-chargeback.yaml}"
CHART="${2:-products/chargeback/chart}"
fail=0

[[ -f "$CB" ]] || { echo "FAIL: $CB not found"; exit 1; }

# The slot must declare the sovereign profile — if it does not, this gate is
# checking the wrong file and must say so rather than pass.
if ! grep -qE '^\s*profile:\s*sovereign\s*$' "$CB"; then
  echo "FAIL: $CB does not declare 'profile: sovereign' — gate is looking at the wrong slot"
  exit 1
fi

if awk '/^\s*adapter:/{f=1;next} f&&/^\s*enabled:\s*true\s*$/{print "on";exit} f&&/^\s*[a-zA-Z]/{f=0}' "$CB" | grep -q on; then
  echo "  ok: sovereign slot enables the OpenOva adapter"
else
  echo "FAIL: $CB does not set adapter.enabled: true."
  echo "      The Sovereign placement exists FOR the adapter. With it off no"
  echo "      ServiceAccount token is mounted, the binary logs 'openova adapter off'"
  echo "      at INFO, and the app syncs zero Organizations while looking healthy (#6846)."
  fail=1
fi

# The silent-failure mechanism itself: the token mount must follow the flag.
if command -v helm >/dev/null 2>&1; then
  on=$(helm template cb "$CHART" --set config.sovereignFqdn=t99.omani.works \
        --set adapter.enabled=true 2>/dev/null | grep -c 'automountServiceAccountToken: true' || true)
  off=$(helm template cb "$CHART" --set config.sovereignFqdn=t99.omani.works \
        --set adapter.enabled=false 2>/dev/null | grep -c 'automountServiceAccountToken: false' || true)
  if [[ "$on" -ge 1 && "$off" -ge 1 ]]; then
    echo "  ok: automountServiceAccountToken tracks adapter.enabled (on=$on off=$off)"
  else
    echo "FAIL: automountServiceAccountToken does not track adapter.enabled (on=$on off=$off)."
    echo "      Either the adapter gets no token when enabled, or it keeps one when disabled."
    fail=1
  fi
  # The adapter needs its ClusterRole when enabled, or it 403s instead of syncing.
  if ! helm template cb "$CHART" --set config.sovereignFqdn=t99.omani.works \
         --set adapter.enabled=true 2>/dev/null | grep -q '^kind: ClusterRole$'; then
    echo "FAIL: adapter enabled but no ClusterRole rendered — it cannot read Organizations."
    fail=1
  else
    echo "  ok: ClusterRole rendered when the adapter is enabled"
  fi
else
  echo "  note: helm not present, skipped the render checks"
fi

[[ $fail -eq 0 ]] || { echo "FAILED — the Sovereign would run chargeback with no Organization sync."; exit 1; }
echo "PASS — the Sovereign slot runs chargeback with its adapter on."
