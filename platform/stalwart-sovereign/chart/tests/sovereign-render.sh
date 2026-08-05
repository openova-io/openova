#!/usr/bin/env bash
# bp-stalwart-sovereign — render audit (#924).
#
# Verifies the chart renders the Sovereign-local mail surface:
#   1. Smoke render (default values, no sovereignFQDN) is clean and
#      smoke-render-safe — every per-template gate skips when the
#      sovereignFQDN helper returns empty.
#   2. Operator-overlay render (with sovereignFQDN supplied via
#      `global.sovereignFQDN`) emits:
#        - StatefulSet with image SHA-pinned via @digest
#        - LoadBalancer Service with SMTP/IMAP ports
#        - ClusterIP Service for the admin HTTP API
#        - Stalwart bootstrap config.toml using `==` (NOT `=`) for
#          every equality match
#        - dns-records ConfigMap with mail.<sovereignFQDN>,
#          v=spf1, v=DMARC1, and the DKIM selector
#        - Setup Job mounting both the admin and submission Secrets
#        - submission Secret carrying the canonical 5-key SMTP shape
#          (smtp-host / smtp-port / smtp-from / smtp-user / smtp-pass)
#   3. Stalwart expression-language audit: NO single-`=` equality
#      anywhere in config.toml (per stalwart_expression_syntax.md
#      memory + bp-stalwart-tenant tests/expression-syntax.sh).

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"

# ── Case 1: smoke render (default values) ─────────────────────────────
echo "[stalwart-sovereign] Case 1: smoke render (default values)"
out=$("$helm" template smoke "$chart_dir" 2>&1)
grep -q "kind: StatefulSet" <<<"$out"   || { echo "FAIL: no StatefulSet"; exit 1; }
grep -q "kind: ServiceAccount" <<<"$out" || { echo "FAIL: no ServiceAccount"; exit 1; }
grep -q "kind: NetworkPolicy" <<<"$out"  || { echo "FAIL: no NetworkPolicy"; exit 1; }
# Smoke-render-safe: dns-records ConfigMap MUST NOT render the actual
# ConfigMap object when sovereignFQDN is empty (skip-on-empty gate). The
# RBAC + Job env templates may still REFERENCE the name (they always
# render so smoke gate passes); we check for the ConfigMap kind in the
# block whose name matches.
if echo "$out" \
  | awk '/^---$/{f=0} /^kind: ConfigMap$/{f=1} f && /name: .*dns-records-required/' \
  | grep -q dns-records-required; then
  echo "FAIL: dns-records ConfigMap rendered without sovereignFQDN"
  exit 1
fi
echo "[stalwart-sovereign] Case 1: PASS"

# ── Case 2: operator-overlay render ───────────────────────────────────
echo "[stalwart-sovereign] Case 2: operator-overlay render"
values=$(mktemp)
trap 'rm -f "$values"' EXIT
cat > "$values" <<'EOF'
global:
  sovereignFQDN: omantel.omani.works
EOF
out=$("$helm" template smoke "$chart_dir" -f "$values" 2>&1)

# Image SHA-pin
grep -q "stalwartlabs/stalwart@sha256:" <<<"$out" \
  || { echo "FAIL: image not SHA-pinned"; exit 1; }

# LoadBalancer Service with SMTP ports
grep -q "type: LoadBalancer" <<<"$out" \
  || { echo "FAIL: no LoadBalancer Service"; exit 1; }
grep -q "name: smtp" <<<"$out" \
  || { echo "FAIL: no smtp port name"; exit 1; }
grep -q "name: submission" <<<"$out" \
  || { echo "FAIL: no submission port name"; exit 1; }

# §854 / #5348: the public mail LoadBalancer MUST carry the explicit
# nodePort:0 guard on every port + allocateLoadBalancerNodePorts:false on the
# spec — a clean render is NOT platform-clean (the apiserver silently
# auto-allocates a nodePort per port otherwise, exactly the powerdns-anycast
# violation #5348 caught). Assert BOTH directions: the positive checks prove
# the LB path actually rendered the guard (a bare "no 3xxxx" would pass
# vacuously on a ClusterIP-only render), and the negative check proves no
# auto-allocated nodePort leaked into the 30000-32767 range.
grep -q "allocateLoadBalancerNodePorts: false" <<<"$out" \
  || { echo "FAIL: mail LoadBalancer missing allocateLoadBalancerNodePorts:false (§854/#5348)"; exit 1; }
np0=$(grep -cE '^[[:space:]]*nodePort: 0[[:space:]]*$' <<<"$out" || true)
[[ "$np0" -eq 5 ]] \
  || { echo "FAIL: expected 5 explicit 'nodePort: 0' (one per mail port), got $np0 (§854/#5348)"; exit 1; }
if grep -qE 'nodePort: 3[0-2][0-9]{3}' <<<"$out"; then
  echo "FAIL: mail Service leaked an auto-allocated nodePort in 30000-32767 (§854)"; exit 1
fi
echo "[stalwart-sovereign] §854 nodePort:0 guard: PASS"

# ClusterIP for admin API
grep -q "stalwart-sovereign-http" <<<"$out" \
  || { echo "FAIL: no http ClusterIP Service"; exit 1; }

# config.toml using == not = for equality (stalwart_expression_syntax.md).
# Spot-check: any "if = " line whose RHS string lacks == is suspect.
toml=$(echo "$out" | awk '/config\.toml: \|/,/^---/' || true)
if echo "$toml" | grep -E '^[[:space:]]*if[[:space:]]*=[[:space:]]*"[^=]+",' >/dev/null 2>&1; then
  echo "FAIL: config.toml has single-eq expression (Stalwart silently ignores it)"
  exit 1
fi

# dns-records ConfigMap with operator content
grep -q "stalwart-sovereign-dns-records-required" <<<"$out" \
  || { echo "FAIL: dns-records ConfigMap missing"; exit 1; }
grep -q "mail.omantel.omani.works" <<<"$out" \
  || { echo "FAIL: mailHost not composed"; exit 1; }
grep -q "v=spf1 a mx" <<<"$out" \
  || { echo "FAIL: SPF record string missing"; exit 1; }
grep -q "v=DMARC1" <<<"$out" \
  || { echo "FAIL: DMARC record string missing"; exit 1; }
grep -q "_domainkey.omantel.omani.works" <<<"$out" \
  || { echo "FAIL: DKIM record name missing"; exit 1; }

# Submission Secret with canonical 5-key shape (b64-encoded values).
# Decode and assert the sender + host bytes look right.
b64host=$(echo "$out" | awk '/smtp-host: /{print $2; exit}' | tr -d '"')
host_dec=$(echo "$b64host" | base64 -d 2>/dev/null || true)
[[ "$host_dec" == "mail.omantel.omani.works" ]] \
  || { echo "FAIL: smtp-host decoded to '$host_dec', expected 'mail.omantel.omani.works'"; exit 1; }

b64from=$(echo "$out" | awk '/smtp-from: /{print $2; exit}' | tr -d '"')
from_dec=$(echo "$b64from" | base64 -d 2>/dev/null || true)
[[ "$from_dec" == "noreply@omantel.omani.works" ]] \
  || { echo "FAIL: smtp-from decoded to '$from_dec'"; exit 1; }

# Setup Job mounts both Secrets
grep -q "name: ADMIN_PASSWORD" <<<"$out" \
  || { echo "FAIL: ADMIN_PASSWORD env missing"; exit 1; }
grep -q "name: SUBMISSION_PASSWORD" <<<"$out" \
  || { echo "FAIL: SUBMISSION_PASSWORD env missing"; exit 1; }

# Setup Job creates mirror Secret with both key shapes
grep -q '"smtp-user": "${B64_USER}"' <<<"$out" \
  || { echo "FAIL: mirror Secret missing smtp-user key"; exit 1; }
grep -q '"user": "${B64_USER}"' <<<"$out" \
  || { echo "FAIL: mirror Secret missing legacy user key"; exit 1; }

echo "[stalwart-sovereign] Case 2: PASS"

# ── Case 3: mirror disable opts out cleanly ───────────────────────────
echo "[stalwart-sovereign] Case 3: sovereignSMTPCredentialsMirror.enabled=false"
cat > "$values" <<'EOF'
global:
  sovereignFQDN: omantel.omani.works
sovereignSMTPCredentialsMirror:
  enabled: false
EOF
out=$("$helm" template smoke "$chart_dir" -f "$values" 2>&1)
# Mirror disabled — cross-namespace Role into catalyst-system MUST NOT render.
if grep -q "namespace: catalyst-system" <<<"$out"; then
  echo "FAIL: mirror Role rendered into catalyst-system despite enabled=false"
  exit 1
fi
echo "[stalwart-sovereign] Case 3: PASS"

echo "[stalwart-sovereign] all cases PASS"
