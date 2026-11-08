#!/usr/bin/env bash
# ADR-0015 D9 — live Sovereign acceptance suite: install, data, move, move back.
#
#   scripts/acceptance-sovereign.sh <env> [--record] [--discover] [suite flags…]
#
# Drives tests/acceptance/sovereign against https://console.<env>.omani.works
# through the SAME API the console uses, and asserts FUNCTION: WordPress
# installed active-hot-standby across both regions serves, takes a write and an
# upload, keeps both through a move to the other region and back, then is
# deleted. Prints a step table; with --record writes
# docs/ledger/acceptance/<env>-<run_id>.json (the run id a function-clause UAT
# row must cite as `acceptance:<run_id>`).
#
#   --discover   READ-ONLY: resolve Org/Environment/Regions and probe every
#                endpoint the run uses. The API client refuses non-GET calls.
#
# Inputs (environment; nothing secret is ever printed):
#   CATALYST_SESSION     catalyst_session cookie value of an owner session.
#   ACCEPT_MINT=1        …or mint one: the Sovereign's session signer is read with
#                        `kubectl exec` from region A's catalyst-api and PIPED into
#                        the minter (it never touches disk). Same claims as PIN login.
#   ACCEPT_KUBECONFIG_A  default ~/.kube/sovereigns/<env>-a.yaml (starts Primary)
#   ACCEPT_KUBECONFIG_B  default ~/.kube/sovereigns/<env>-b.yaml (starts Standby)
#   ACCEPT_CONSOLE       default https://console.<env>.omani.works
#   ACCEPT_REGION_IPS    'regionA=ip,ip;regionB=ip' for the DNS assertion
#                        (default: each region's bootstrap-kit CILIUM_LB_IPAM_CIDRS)
#   ACCEPT_JOURNEYS      default J1,J2,J3,J4,J5
#   ACCEPT_*_TIMEOUT, ACCEPT_COMMIT_BUDGET, ACCEPT_KEEP_ON_FAIL=1 … see README.
#
# Exit: 0 PASS · 1 an assertion failed · 2 usage · 3 harness error.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SUITE="$REPO/tests/acceptance/sovereign"

ENV_LABEL="${1:-}"
[ -n "$ENV_LABEL" ] && [ "${ENV_LABEL#-}" = "$ENV_LABEL" ] || {
  sed -n '2,30p' "${BASH_SOURCE[0]}" >&2; exit 2; }
shift

MODE=run
ARGS=()
for a in "$@"; do
  case "$a" in
    --discover) MODE=discover ;;
    --record)   ARGS+=(--record --record-dir "$REPO/docs/ledger/acceptance") ;;
    *)          ARGS+=("$a") ;;
  esac
done

export ACCEPT_KUBECONFIG_A="${ACCEPT_KUBECONFIG_A:-$HOME/.kube/sovereigns/${ENV_LABEL}-a.yaml}"
export ACCEPT_KUBECONFIG_B="${ACCEPT_KUBECONFIG_B:-$HOME/.kube/sovereigns/${ENV_LABEL}-b.yaml}"
export ACCEPT_CONSOLE="${ACCEPT_CONSOLE:-https://console.${ENV_LABEL}.omani.works}"
for k in "$ACCEPT_KUBECONFIG_A" "$ACCEPT_KUBECONFIG_B"; do
  [ -r "$k" ] || { echo "kubeconfig not readable: $k" >&2; exit 2; }
done
command -v kubectl >/dev/null || { echo "kubectl not on PATH" >&2; exit 2; }
command -v go >/dev/null || { echo "go not on PATH" >&2; exit 2; }

BIN_DIR="$(mktemp -d)"; trap 'rm -rf "$BIN_DIR"' EXIT
( cd "$SUITE" && go build -o "$BIN_DIR/sovereign-acceptance" ./cmd/sovereign-acceptance )
BIN="$BIN_DIR/sovereign-acceptance"

if [ -z "${CATALYST_SESSION:-}" ]; then
  if [ "${ACCEPT_MINT:-0}" = "1" ]; then
    # The signer stays in the pipe: kubectl exec → minter stdin. Issuer is the
    # Sovereign's own CATALYST_PIN_ISSUER, read from its catalyst-api env.
    ISSUER="$(kubectl --kubeconfig "$ACCEPT_KUBECONFIG_A" -n catalyst-system get deploy catalyst-api \
      -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="CATALYST_PIN_ISSUER")].value}')"
    ISSUER="${ISSUER:-$ACCEPT_CONSOLE}"
    SIGNER_PATH="$(kubectl --kubeconfig "$ACCEPT_KUBECONFIG_A" -n catalyst-system get deploy catalyst-api \
      -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="CATALYST_HANDOVER_SIGNER_PATH")].value}')"
    SIGNER_PATH="${SIGNER_PATH:-/var/lib/catalyst/handover-jwt-private.pem}"
    CATALYST_SESSION="$(kubectl --kubeconfig "$ACCEPT_KUBECONFIG_A" -n catalyst-system exec deploy/catalyst-api -- \
      cat "$SIGNER_PATH" | "$BIN" mint-session --key - --issuer "$ISSUER")"
    [ -n "$CATALYST_SESSION" ] || { echo "minting the session failed" >&2; exit 3; }
  else
    echo "CATALYST_SESSION is not set. Export an owner session cookie value, or set ACCEPT_MINT=1." >&2
    exit 2
  fi
fi
export CATALYST_SESSION

set +e
"$BIN" "$MODE" --env "$ENV_LABEL" "${ARGS[@]}"
rc=$?
set -e
exit $rc
