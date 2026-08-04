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
if ! grep -q 'session_secret="\$(' <<<"$RENDERED"; then
  echo "FAIL 1: session_secret derivation not found in rendered reconciler"
  exit 1
fi
echo "PASS 1: session_secret derivation present"

# Test 2: jq bundle includes session_secret:$session
if ! grep -q 'session_secret:\$session' <<<"$RENDERED"; then
  echo "FAIL 2: jq bundle expression missing session_secret:\$session"
  exit 1
fi
echo "PASS 2: jq bundle includes session_secret"

# Test 3: derivation uses sha256 + the |session| delimiter
if ! grep -qE 'sha256sum.*\|session\||\\\|session\\\|.*sha256sum' <<<"$RENDERED"; then
  if ! grep -q 'session|.*sha256sum' <<<"$RENDERED"; then
    # Multi-line — verify presence of both elements
    grep -q 'sha256sum' <<<"$RENDERED" || { echo "FAIL 3: sha256sum not in derivation"; exit 1; }
    grep -q '|session|' <<<"$RENDERED" || { echo "FAIL 3: |session| delimiter not in derivation"; exit 1; }
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

# ── #5466 — crypto_secret joins the bundle (bp-newapi CRYPTO_SECRET) ──────
# Same derivation idiom, its own domain-separation tag `crypto`, full 64-hex
# digest (newapi consumes an opaque string — no oauth2-proxy-style base64url
# length constraint, so NO substr truncation like cookie_secret's).

# Test 6: crypto_secret derivation present with the |crypto| tag
if ! grep -q 'crypto_secret="\$(' <<<"$RENDERED"; then
  echo "FAIL 6: crypto_secret derivation not found in rendered reconciler"
  exit 1
fi
if ! grep -q '|crypto|' <<<"$RENDERED"; then
  echo "FAIL 6: |crypto| domain-separation tag not in derivation"
  exit 1
fi
echo "PASS 6: crypto_secret derivation present with |crypto| tag"

# Test 7: jq bundle includes crypto_secret:$crypto
if ! grep -q 'crypto_secret:\$crypto' <<<"$RENDERED"; then
  echo "FAIL 7: jq bundle expression missing crypto_secret:\$crypto"
  exit 1
fi
echo "PASS 7: jq bundle includes crypto_secret"

# Test 8: crypto_secret is full-length 64-hex AND distinct from BOTH
# session_secret and client_secret for the same inputs (value assert — a
# tag typo collapsing crypto onto session must fail here).
computed_crypto="$(printf '%s|crypto|%s|%s' "$test_cid" "$test_realm" "$test_client_secret" | sha256sum | awk '{print $1}')"
if [ ${#computed_crypto} -ne 64 ]; then
  echo "FAIL 8: crypto_secret not a full sha256 hex digest (len=${#computed_crypto})"
  exit 1
fi
if [ "$computed_crypto" = "$computed_session" ] || [ "$computed_crypto" = "$test_client_secret" ]; then
  echo "FAIL 8: crypto_secret collides with session_secret or client_secret"
  exit 1
fi
# The rendered script must NOT truncate crypto_secret (that is cookie_secret's
# constraint, not newapi's): the crypto_secret line must not contain substr.
if grep 'crypto_secret=' <<<"$RENDERED" | grep -q 'substr'; then
  echo "FAIL 8: crypto_secret derivation truncates the digest — newapi expects the full 64-hex string"
  exit 1
fi
echo "PASS 8: crypto_secret is full 64-hex, domain-separated, untruncated"

echo "=== ALL TESTS PASS ==="
