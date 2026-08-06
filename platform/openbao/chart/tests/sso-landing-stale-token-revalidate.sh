#!/usr/bin/env bash
# bp-openbao — SSO landing MUST re-validate a cached token server-side before
# trusting it (#5459, UAT row 183 regression, measured live hw292
# 2026-08-06T06:06Z).
#
# WHY: the landing page's "already signed in? -> straight through" re-entry
# fast path used to trust a CLIENT-SIDE tokenExpirationEpoch alone. That
# value is computed once, at mint time, from the auth response's
# lease_duration — it has no way to learn that the token was later revoked
# server-side (explicit revoke, entity/alias churn, any other Vault-internal
# invalidation) before its programmed TTL elapsed. The landing page still
# shows no login form (session-authenticity "holds") but redirects straight
# into the authenticated Ember UI with a dead token, which then 403s on its
# first real call (`sys/internal/ui/mounts`) — "Not authorized" instead of
# the Secrets Engines list. Independently confirmed on hw292: the server-side
# ACL/OIDC-role/identity-group state was correct and converged in BOTH
# regions throughout the exact failure window (zero WARNs in the
# openbao-sso-configure reconciler log across 06:00-06:12Z) — the defect is
# the CLIENT never re-checking a cache it blindly trusted.
#
# This guard extracts the ACTUAL landing-page <script> body from a live
# `helm template` render (never a hand-copied snippet — a copy would mask
# the exact regression this pins) and runs it in a Node vm with a mocked
# fetch/localStorage/window, seeding a token that LOOKS locally valid (24h
# future tokenExpirationEpoch) while a mocked `auth/token/lookup-self`
# reports it dead server-side (403). It must NOT be a `must_contain`
# string-grep — this proves BEHAVIOR: does the script actually ask the
# server before deciding to enter, and does it correctly refuse to enter on
# a 403 answer.
#
# Two scenarios + a vacuity control so the guard cannot pass on nothing:
#   1. revoked -> lookup-self 403: the landing page MUST call lookup-self
#      BEFORE any window.location.replace, and MUST NOT end up navigating to
#      /ui/vault/secrets with the dead token (must fall through to a fresh
#      OIDC round-trip instead).
#   2. valid   -> lookup-self 200: the SAME code path MUST still complete
#      normally and land on /ui/vault/secrets (proves the fix isn't just
#      "never redirect" — a genuinely-valid cached token still short-circuits).

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
test_dir="$(cd "$(dirname "$0")" && pwd)"
helm="${HELM_BIN:-helm}"
node_bin="${NODE_BIN:-node}"

command -v "$node_bin" >/dev/null 2>&1 || { echo "SKIP: node not available"; exit 0; }

"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

FQDN="hw133.omani.works"
HOST="bao.${FQDN}"

"$helm" template smoke "$chart_dir" \
  --set gateway.enabled=true \
  --set "gateway.host=${HOST}" \
  --set "sso.sovereignFqdn=${FQDN}" \
  > "$work/render.out"

# Extract the landing page's inline <script> body. index.html is embedded
# in the openbao-sso-landing ConfigMap as a literal-block; the <script> tag
# is unique in the whole render.
awk '/<script>/{f=1;next} /<\/script>/{f=0} f' "$work/render.out" > "$work/landing.js"

if [ ! -s "$work/landing.js" ]; then
  echo "FAIL: could not extract the landing page <script> body from the render"
  exit 1
fi
"$node_bin" --check "$work/landing.js" || { echo "FAIL: extracted landing.js is not valid JavaScript"; exit 1; }

run_scenario() {
  # $1 = scenario name
  "$node_bin" "$test_dir/_sso_landing_stale_token_check.js" "$work/landing.js" "$1"
}

echo "[bp-openbao] Case 7: cached-but-revoked token — re-entry must re-validate before entering"
out_revoked="$(run_scenario revoked)"
echo "  observed: $out_revoked"

case "$out_revoked" in
  *'"fetchCalls":[]'*)
    echo "FAIL: re-entry never called the server at all (blind trust of the cached token — the exact #5459 regression)"
    exit 1
    ;;
esac
case "$out_revoked" in
  *'lookup-self'*) : ;;
  *)
    echo "FAIL: re-entry did not call auth/token/lookup-self to validate the cached token"
    exit 1
    ;;
esac
# The dead token must NEVER be trusted into /ui/vault/secrets.
case "$out_revoked" in
  *'"replaceCalls":['*'/ui/vault/secrets'*)
    echo "FAIL: landing page navigated to /ui/vault/secrets with a server-revoked cached token (regressed — will 403 on mounts, UAT row 183)"
    exit 1
    ;;
esac
# It MUST still recover — falling through to a fresh OIDC round-trip
# (begin() -> auth_url), not just silently dead-ending.
case "$out_revoked" in
  *'realms/sovereign/protocol/openid-connect/auth'*) : ;;
  *)
    echo "FAIL: a revoked cached token did not fall through to a fresh OIDC login (begin()/auth_url never reached)"
    exit 1
    ;;
esac
echo "[bp-openbao] Case 7: PASS"

echo "[bp-openbao] Case 8: vacuity control — a GENUINELY valid cached token must still short-circuit straight through"
out_valid="$(run_scenario valid)"
echo "  observed: $out_valid"
case "$out_valid" in
  *'lookup-self'*) : ;;
  *) echo "FAIL: valid-token scenario never called lookup-self either (harness broken)"; exit 1 ;;
esac
case "$out_valid" in
  *'"replaceCalls":['*'/ui/vault/secrets'*) : ;;
  *)
    echo "FAIL: a server-CONFIRMED-valid cached token failed to short-circuit into /ui/vault/secrets (guard would trivially pass by banning ALL redirects — that is not this fix)"
    exit 1
    ;;
esac
echo "[bp-openbao] Case 8: PASS"

echo "[bp-openbao] All stale-token-revalidate cases PASS"
