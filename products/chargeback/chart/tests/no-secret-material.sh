#!/usr/bin/env bash
# bp-chargeback — no secret material may ever be rendered (Inviolable #4,
# spec §7.4 "no secret in logs or API responses" extended to the chart).
#
#   - the DSN placeholder carries an EMPTY DATABASE_URL — the password only
#     ever exists inside the sync Job's process;
#   - APP_ENCRYPTION_KEY has NO values seam for a literal — the key Job
#     generates it and the Deployment only references the Secret;
#   - the SMTP seam is a Secret NAME (envFrom optional), never SMTP_PASS;
#   - values.yaml carries no key/password/token literal.
#
# Usage: tests/no-secret-material.sh [chart_dir]

set -euo pipefail

chart_dir="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
helm="${HELM_BIN:-helm}"
fail() { echo "FAIL: $*" >&2; exit 1; }

out="$("$helm" template chargeback "$chart_dir" --namespace chargeback \
  --api-versions cilium.io/v2 --api-versions postgresql.cnpg.io/v1 \
  --set sovereignFqdn=hw305.omani.works --set smtp.existingSecret=chargeback-smtp 2>/dev/null)"

# The placeholder must be exactly empty.
grep -q 'DATABASE_URL: ""' <<<"$out" || fail "DSN placeholder is not the empty string"
# No populated-looking DSN or key anywhere in the render.
grep -Eq 'postgres://[^"$]*:[^"$@]+@' <<<"$out" && fail "a DSN with an inline password rendered"
grep -Eq 'APP_ENCRYPTION_KEY:\s*"?[A-Za-z0-9+/=]{16,}' <<<"$out" && fail "an APP_ENCRYPTION_KEY literal rendered"
grep -Eq 'SMTP_PASS' <<<"$out" && fail "SMTP_PASS rendered — the SMTP seam is a Secret name only"
grep -q 'name: chargeback-smtp' <<<"$out" || fail "smtp.existingSecret not wired as envFrom"
grep -q 'optional: true' <<<"$out" || fail "smtp envFrom is not optional"

# values.yaml carries no secret literal.
grep -Eiq '^\s*(password|secretKey|secret_key|token|apiKey|api_key)\s*:\s*\S+' "$chart_dir/values.yaml" \
  && fail "values.yaml carries a secret-shaped literal"

# The secret key seam exists only as a values key NAME (encryptionKey.key).
grep -q 'key: APP_ENCRYPTION_KEY' "$chart_dir/values.yaml" || fail "encryptionKey.key seam missing"

echo "PASS: bp-chargeback renders no secret material"
