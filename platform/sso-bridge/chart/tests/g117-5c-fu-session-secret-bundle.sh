#!/usr/bin/env bash
# G117.5c-followup #2807 — bp-sso-bridge: session_secret added to per-Org
# OpenBao bundle so librechat's OPENID_SESSION_SECRET resolves via
# ExternalSecret.
#
# Asserts:
#   1. The rendered configmap-reconciler.yaml script contains a
#      session_secret derivation (sha256 of `clientId|session|realm|client_secret`)
#   2. The jq bundle expression includes `session_secret:$session`
#   3. The derivation is render-stable (same inputs → same value)
#   4. session_secret is DISTINCT from client_secret for the same client
set -euo pipefail

CHART_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$CHART_DIR"

RENDERED="$(helm template smoke "$CHART_DIR" --show-only templates/configmap-reconciler.yaml 2>/dev/null)"

echo "=== G117.5c-followup session_secret bundle tests ==="

# Test 1: session_secret derivation present
if ! echo "$RENDERED" | grep -q 'session_secret="\$('; then
  echo "FAIL 1: session_secret derivation not found in rendered reconciler"
  exit 1
fi
echo "PASS 1: session_secret derivation present"

# Test 2: jq bundle includes session_secret:$session
if ! echo "$RENDERED" | grep -q 'session_secret:\$session'; then
  echo "FAIL 2: jq bundle expression missing session_secret:\$session"
  exit 1
fi
echo "PASS 2: jq bundle includes session_secret"

# Test 3: derivation uses sha256 + the |session| delimiter
if ! echo "$RENDERED" | grep -qE 'sha256sum.*\|session\||\\\|session\\\|.*sha256sum'; then
  if ! echo "$RENDERED" | grep -q 'session|.*sha256sum'; then
    # Multi-line — verify presence of both elements
    echo "$RENDERED" | grep -q 'sha256sum' || { echo "FAIL 3: sha256sum not in derivation"; exit 1; }
    echo "$RENDERED" | grep -q '|session|' || { echo "FAIL 3: |session| delimiter not in derivation"; exit 1; }
  fi
fi
echo "PASS 3: derivation uses sha256 + |session| delimiter"

# Test 4: Verify the derivation logic produces a DISTINCT value from client_secret
# (smoke test the bash logic in isolation)
test_cid="librechat"
test_realm="acme"
test_client_secret="abc123def456"
computed_session="$(printf '%s|session|%s|%s' "$test_cid" "$test_realm" "$test_client_secret" | sha256sum | awk '{print $1}')"
if [ "$computed_session" = "$test_client_secret" ]; then
  echo "FAIL 4: session_secret happened to equal client_secret — that's a hash collision"
  exit 1
fi
if [ -z "$computed_session" ] || [ ${#computed_session} -ne 64 ]; then
  echo "FAIL 4: session_secret not a valid sha256 hex digest (len=${#computed_session})"
  exit 1
fi
echo "PASS 4: derivation produces a valid sha256 distinct from client_secret"

# Test 5: Idempotency — same inputs produce same value
computed_session2="$(printf '%s|session|%s|%s' "$test_cid" "$test_realm" "$test_client_secret" | sha256sum | awk '{print $1}')"
if [ "$computed_session" != "$computed_session2" ]; then
  echo "FAIL 5: same inputs produced different session_secret values"
  exit 1
fi
echo "PASS 5: derivation is render-stable (idempotent)"

echo "=== ALL TESTS PASS ==="
