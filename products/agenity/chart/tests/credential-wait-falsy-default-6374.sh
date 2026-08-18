#!/usr/bin/env bash
# #6374 — credentialWait.timeoutSeconds/intervalSeconds are NUMBERS, so sprig's
# `default` is the wrong resolver: it substitutes on any FALSY value and 0 is
# falsy. `timeoutSeconds: 0` therefore rendered as 300 and `intervalSeconds: 0`
# as 5. An operator asking for "do not wait — fail immediately if the credential
# is absent" silently got a 5-minute wait, and the init container's own log line
# echoed 300 back, so the setting looked applied.
#
# This suite deliberately renders the ZERO case FIRST. A suite that only ever
# passes non-zero values cannot fail on this defect — the same shape as the
# bp-guacamole render tests that always supplied the CNPG capability and so
# could never see #5991.
set -euo pipefail
CHART="$(cd "$(dirname "$0")/.." && pwd)"
fail=0

expect() { # expect <desc> <needle> <render...>
  local desc="$1" needle="$2"; shift 2
  local out; out="$("$@" 2>&1)"
  if printf '%s\n' "$out" | grep -qF -- "$needle"; then
    echo "  ok   $desc  ($needle)"
  else
    echo "  FAIL $desc — expected '$needle'"
    printf '%s\n' "$out" | grep -E '_cw_timeout=|_cw_interval=' || true
    fail=1
  fi
}

echo "== #6374 zero must survive (the defect) =="
expect "timeoutSeconds=0 stays 0" "_cw_timeout=0" \
  helm template ag "$CHART" --set anthropic.credentialWait.timeoutSeconds=0
expect "intervalSeconds=0 stays 0" "_cw_interval=0" \
  helm template ag "$CHART" --set anthropic.credentialWait.intervalSeconds=0

echo "== CONTROLS — the defaults must still work, or the fix over-corrected =="
expect "unset -> documented default 300" "_cw_timeout=300" helm template ag "$CHART"
expect "unset -> documented default 5"   "_cw_interval=5"  helm template ag "$CHART"

echo "== CONTROL — a normal explicit value passes through untouched =="
expect "timeoutSeconds=42 -> 42" "_cw_timeout=42" \
  helm template ag "$CHART" --set anthropic.credentialWait.timeoutSeconds=42

echo "== onTimeout KEEPS sprig default: empty is not a meaningful verdict =="
expect "onTimeout unset -> fail" "_cw_ontimeout='fail'" helm template ag "$CHART"
expect "onTimeout=continue honoured" "_cw_ontimeout='continue'" \
  helm template ag "$CHART" --set anthropic.credentialWait.onTimeout=continue

[ "$fail" -eq 0 ] && echo "PASS: credentialWait resolves by key presence, not sprig default." || { echo "FAIL"; exit 1; }

# Re-trigger note (#6374): the version-collision gate reads the OPEN-PR set at
# trigger time, so it needs a fresh synchronize event to observe that #6375 was
# closed and no longer claims 0.5.30. An empty commit does not work here — every
# gate is paths-filtered, so a commit touching no files runs no checks at all.
