#!/usr/bin/env bash
# bp-gitea — G117.E2E-A1 #2816 regression guard for the
# `gitea admin auth list --vertical-bars` AUTH_ID parsing.
#
# Bug: the post-install/post-upgrade `gitea-sso-configure` Job runs
#   AUTH_ID=$(echo "$AUTH_LIST" | awk -F '|' '... { gsub(/ /,"",$1); print $1 }' )
# which strips SPACES from the first column but not TABS. `gitea admin
# auth list --vertical-bars` separates cells with literal `|` but pads
# each cell with TABS — so $1 reads back as `1\t`, and the subsequent
# `gitea admin auth update-oauth --id "1\t"` aborts with
#   invalid value "1\t" for flag -id: parse error
# Hook exhausts backoffLimit, Helm rolls back the bp-gitea HR, and
# the entire downstream cascade (bp-catalyst-platform → bp-sso-bridge →
# everything else) stalls Ready=False.
#
# Caught live on hw86 2026-06-03 after PR #2821 (1.2.14) unblocked the
# Pod-selector path so the Job actually ran. Same shape of stripped-
# whitespace mismatch as the awk parsing in old Bitnami chart helpers.
#
# Fix: strip all whitespace with gsub(/[[:space:]]+/, "", $1).
#
# Usage: bash tests/g117-e2e-a1-auth-id-tab-strip.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"

# Sample real `gitea admin auth list --vertical-bars` output (tab-padded
# per Gitea source: cmd/admin_auth.go writes "%d\t|%s\t\t|%s\t|%v\t").
SAMPLE_LIST="$(printf 'ID\t|Name\t\t|Type\t|Enabled\n1\t|openova-sso\t|OAuth2\t|true\n')"

# Extract the awk one-liner used in templates/sso-configure-deployment.yaml
# (the #3851 refactor moved the one-shot configure-oauth Job into a
# continuously-reconciling Deployment — the openbao pattern — carrying this
# AUTH_ID awk idiom with it). We assert it strips ALL whitespace from $1
# (not just spaces), so the resulting AUTH_ID is the bare "1" without
# trailing tab.
AUTH_ID="$(printf '%s\n' "$SAMPLE_LIST" \
  | awk -F '|' -v name="openova-sso" \
    '$2 ~ name && $3 ~ /OAuth2/ {gsub(/[[:space:]]+/,"",$1); print $1}' \
  | head -n1)"

if [ "$AUTH_ID" != "1" ]; then
  echo "FAIL: awk extraction produced AUTH_ID='${AUTH_ID}' (expected bare '1' without whitespace)"
  echo "  This means \`gitea admin auth update-oauth --id \"\${AUTH_ID}\"\` will fail with"
  echo "  \`invalid value \"\${AUTH_ID}\" for flag -id: parse error\`."
  exit 1
fi
echo "PASS 1: awk extraction strips tabs+spaces from AUTH_ID (got '${AUTH_ID}')"

# Sanity check: the actual template ALSO uses [[:space:]]+ (not just bare " "),
# so the live reconciler runs the same logic as this unit test.
if ! grep -qF 'gsub(/[[:space:]]+/,"",$1)' \
     "$CHART_DIR/templates/sso-configure-deployment.yaml"; then
  echo "FAIL: sso-configure-deployment.yaml does not use gsub(/[[:space:]]+/,...) for AUTH_ID extraction"
  echo "  The chart template has drifted from this test's contract — re-apply the"
  echo "  whitespace-strip pattern to the AUTH_ID awk script and re-run."
  exit 1
fi
echo "PASS 2: chart template uses the same gsub(/[[:space:]]+/,...) idiom"

echo
echo "[g117-e2e-a1-auth-id-tab-strip] ALL CASES PASS"
