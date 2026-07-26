#!/usr/bin/env bash
# bp-openbao — ESO push-policy render audit (#5375, UAT row M2).
#
# The auth-bootstrap Job must grant the ESO auth role a NARROW write policy
# (`external-secrets-push`) on exactly `secret/{data,metadata}/catalyst/
# newapi/admin-token`, so bp-newapi's admin-token PushSecret (the REGION-
# LOCAL producer that closes the M2 region-B SecretSyncedError / DR gap) can
# seed the path in every region's vault. Guard rails this test enforces:
#
#   1. The push policy renders in the auth-bootstrap args with BOTH the data
#      and metadata grants for the single canonical path.
#   2. The ESO role attaches BOTH policies (read + push) — a regression that
#      drops either silently re-opens M2 (push 403s) or breaks every
#      ExternalSecret (reads 403).
#   3. The broad ESO read policy stays READ-ONLY — no create/update leaks
#      into the `secret/data/*` tree grant.
#
# The auth-bootstrap Job renders only when autoUnseal.enabled — mirrored from
# the Catalyst overlay (08-openbao.yaml).

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"

"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

out=$("$helm" template openbao "$chart_dir" \
  --set autoUnseal.enabled=true 2>&1)

echo "[bp-openbao] Case 1: external-secrets-push policy present with both path grants"
echo "$out" | grep -q "bao policy write external-secrets-push" || {
  echo "FAIL: auth-bootstrap does not write the external-secrets-push policy"; exit 1; }
# -F: the rendered script text contains the LITERAL string $KV_MOUNT_PATH
# (shell expansion happens at Job runtime, not render time).
# shellcheck disable=SC2016
echo "$out" | grep -qF '"$KV_MOUNT_PATH/data/catalyst/newapi/admin-token"' || {
  echo "FAIL: push policy lacks the data-path grant for catalyst/newapi/admin-token"; exit 1; }
# shellcheck disable=SC2016
echo "$out" | grep -qF '"$KV_MOUNT_PATH/metadata/catalyst/newapi/admin-token"' || {
  echo "FAIL: push policy lacks the metadata-path grant for catalyst/newapi/admin-token"; exit 1; }
echo "  ok"

echo "[bp-openbao] Case 2: ESO role attaches read + push policies"
echo "$out" | grep -q "policies=external-secrets-read,external-secrets-push" || {
  echo "FAIL: ESO role does not attach external-secrets-read,external-secrets-push"; exit 1; }
echo "  ok"

echo "[bp-openbao] Case 3: broad ESO read grant stays read-only"
# The external-secrets-read policy block must not gain write capabilities on
# the wildcard tree. Extract the block between its heredoc markers and assert
# no create/update appears inside it.
read_policy_block=$(echo "$out" | sed -n '/bao policy write external-secrets-read -/,/^ *EOF$/p')
[ -n "$read_policy_block" ] || {
  echo "FAIL: could not locate the external-secrets-read policy block"; exit 1; }
if echo "$read_policy_block" | grep -qE '"(create|update|delete)"'; then
  echo "FAIL: external-secrets-read policy gained write capabilities on the broad tree"; exit 1
fi
echo "  ok"

echo "[bp-openbao] eso-push-policy-render: ALL CASES PASSED"
