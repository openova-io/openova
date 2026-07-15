#!/usr/bin/env bash
# check-no-nodeports.sh — CI guard that NodePorts stay ERADICATED, forever.
#
# Founder 2026-07-03 (verbatim): "YOU CAN NEVER EVER USE NODEPORT EVEN FOR
# TESTING PURPOSE!!! ... FUCK THE NODEPORTS!!!" (#4765). NodePorts are
# ABSOLUTELY FORBIDDEN: no `Service type=NodePort`, no `nodePort:` field,
# no `serviceType: NodePort`, no `type = "NodePort"` in IaC, no ELB/LB pool
# member pointing at a node:nodePort. The gateway is served DIRECT (cilium
# LB-IPAM VIP / shared-EIP, OR gateway-api hostNetwork host ports
# node:443/:80), and clustermesh rides the sovereign-vip VIP on :2379 —
# never a NodePort.
#
# This guard fails ANY future PR that reintroduces a NodePort. It runs two
# phases:
#   Phase 1 (authoritative, always enforced) — a static scan of the repo
#     sources (platform/ products/ clusters/ infra/ core/) for the forbidden
#     NodePort field/literal patterns, ignoring comments and lines that
#     merely DOCUMENT the ban.
#   Phase 2 (best-effort) — `helm template` every chart under platform/*/chart
#     and products/*/charts/* with default values and grep the RENDERED
#     output for `type: NodePort` / `nodePort:` (catches values-driven
#     NodePorts that only appear post-render). A chart that cannot be
#     rendered offline (unvendored subchart deps, required values) is
#     WARN-skipped — Phase 1 already covers its template sources.
#
# Usage:  scripts/check-no-nodeports.sh
# Exit:   0 = clean, 1 = a NodePort reappeared, 2 = usage/setup error.

set -euo pipefail

ROOT="${ROOT:-.}"
cd "${ROOT}"

EXIT=0
fail() {
  echo "FAIL: $1" >&2
  EXIT=1
}

# ─── Phase 1 — static source scan ────────────────────────────────────────
#
# Forbidden real-field / real-literal patterns (NOT comments, NOT template
# guards like `{{- if eq $svcType "NodePort" }}`, NOT the k8s Services-list
# viewer label, NOT `allocateLoadBalancerNodePorts:`):
#   type: NodePort            (k8s Service YAML)
#   serviceType: NodePort     (chart values / bootstrap-kit overlays)
#   nodePort: <nonzero>       (explicit NodePort field; `nodePort: 0` is the
#                             k8s "unspecified" sentinel — inert, ignored)
#   type = "NodePort"         (Terraform / HCL)
PATTERN='(^|[[:space:]])type:[[:space:]]*NodePort|(^|[[:space:]])serviceType:[[:space:]]*NodePort|(^|[[:space:]])nodePort:[[:space:]]*[1-9][0-9]*|type[[:space:]]*=[[:space:]]*"NodePort"'

SCAN_ROOTS=(platform products clusters infra core)

# A candidate line is a VIOLATION unless it is a comment or documents the
# ban. Strip leading whitespace, drop lines whose first char is a comment
# marker (# // *), and drop lines that carry an explicit ban marker.
BAN_MARKERS='FORBIDDEN|forbidden|§854|#4765|NEVER|never use|must not|not a nodeport'

echo "== Phase 1: static NodePort source scan =="
RAW="$(grep -rnIE "${PATTERN}" \
  --include='*.yaml' --include='*.yml' \
  --include='*.tf' --include='*.tftpl' \
  --include='*.go' --include='*.ts' \
  "${SCAN_ROOTS[@]}" 2>/dev/null || true)"

VIOLATIONS=""
if [ -n "${RAW}" ]; then
  while IFS= read -r line; do
    [ -z "${line}" ] && continue
    # Skip the Kyverno forbid-NodePort TEST FIXTURES: they INTENTIONALLY contain
    # `type: NodePort` / `nodePort: <n>` as the "bad" inputs that prove the §854
    # audit ClusterPolicy actually CATCHES a NodePort (#5088/#5089). They are
    # kyverno-cli test resources, never deployed Services — excluding them is
    # correct, not a §854 weakening.
    case "${line%%:*}" in
      platform/kyverno-policies/chart/tests/*) continue ;;
    esac
    # line == path:lineno:content — strip the path:lineno: prefix for the
    # comment/marker tests.
    content="${line#*:}"; content="${content#*:}"
    trimmed="${content#"${content%%[![:space:]]*}"}"
    first="${trimmed:0:1}"
    # Skip pure comment lines (# yaml/tf, // go/ts, * block-comment body).
    case "${first}" in
      '#' | '*') continue ;;
    esac
    case "${trimmed}" in
      '//'*) continue ;;
    esac
    # Skip lines that DOCUMENT the ban (they name the forbidden thing on
    # purpose, e.g. an error_message or a fail-render guard string).
    if printf '%s' "${content}" | grep -qE "${BAN_MARKERS}"; then
      continue
    fi
    VIOLATIONS="${VIOLATIONS}${line}"$'\n'
  done <<< "${RAW}"
fi

if [ -n "${VIOLATIONS}" ]; then
  fail "a NodePort field/literal reappeared in repo sources (#4765):"
  printf '%s' "${VIOLATIONS}" >&2
else
  echo "OK — no NodePort field/literal in ${SCAN_ROOTS[*]} sources."
fi

# ─── Phase 1b — IaC nodePort-range port literals (30000-32767) ───────────
#
# #4765 residue class this phase eradicates: Phase 1 catches the FIELD
# shapes (`type: NodePort`, `nodePort:`, `serviceType: NodePort`,
# `type = "NodePort"`) but MISSED the Hetzner provider's LB→node:nodePort
# pool members (`destination_port = 30053`, health_check `port = 30053`)
# and nodePort firewall rules (`port = "30053"` / `port = "32379"`).
# An LB/firewall/listener targeting ANY port inside the k8s NodePort range
# (30000-32767) IS the forbidden §854 shape "LB pool member pointing at
# node-IP:nodePort" — nothing legitimate listens in that range in a
# §854-clean world (gateway = node:443/:80 direct, clustermesh = VIP:2379,
# clustermesh-proxy = host :12379, DNS = :53). Scan infra/ HCL for
# port-assignment literals inside the range.
echo ""
echo "== Phase 1b: IaC nodePort-range (30000-32767) port-literal scan =="
RAW_1B="$(grep -rnIE '(^|[[:space:]])(port|destination_port|listen_port|protocol_port|port_range_min|port_range_max|default)[[:space:]]*=[[:space:]]*"?[0-9]+"?' \
  --include='*.tf' --include='*.tftpl' infra 2>/dev/null || true)"

VIOLATIONS_1B=""
if [ -n "${RAW_1B}" ]; then
  while IFS= read -r line; do
    [ -z "${line}" ] && continue
    content="${line#*:}"; content="${content#*:}"
    trimmed="${content#"${content%%[![:space:]]*}"}"
    # Skip comment lines and lines that document the ban.
    case "${trimmed:0:1}" in '#') continue ;; esac
    if printf '%s' "${content}" | grep -qE "${BAN_MARKERS}"; then
      continue
    fi
    # Extract the assigned number (first numeric literal after the `=`).
    num="$(printf '%s' "${trimmed}" | sed -nE 's/^[A-Za-z_]+[[:space:]]*=[[:space:]]*"?([0-9]+)"?.*/\1/p')"
    [ -z "${num}" ] && continue
    if [ "${num}" -ge 30000 ] && [ "${num}" -le 32767 ]; then
      VIOLATIONS_1B="${VIOLATIONS_1B}${line}"$'\n'
    fi
  done <<< "${RAW_1B}"
fi

if [ -n "${VIOLATIONS_1B}" ]; then
  fail "an IaC port literal inside the k8s NodePort range (30000-32767) reappeared (#4765 §854 — LB/firewall→node:nodePort is FORBIDDEN):"
  printf '%s' "${VIOLATIONS_1B}" >&2
else
  echo "OK — no nodePort-range port literal in infra HCL."
fi

# ─── Phase 2 — rendered-chart scan (best-effort) ─────────────────────────
echo ""
echo "== Phase 2: rendered-chart NodePort scan =="
if ! command -v helm >/dev/null 2>&1; then
  echo "WARN: helm not on PATH — skipping the rendered-chart phase (Phase 1"
  echo "      already covers chart template sources). Install helm to enforce."
else
  shopt -s nullglob
  # Per-chart render timeout so a pathological chart can never hang CI. We
  # NEVER run `helm dependency build` (it reaches out to the network and can
  # hang); charts render OFFLINE from their vendored charts/*.tgz. A chart
  # with unvendored deps fails template fast and is WARN-skipped.
  HELM_TIMEOUT="${HELM_TIMEOUT:-30s}"
  CHART_DIRS=(platform/*/chart products/*/charts/*)
  for chart in "${CHART_DIRS[@]}"; do
    [ -f "${chart}/Chart.yaml" ] || continue
    rendered="$(timeout "${HELM_TIMEOUT}" helm template "$(basename "${chart}")" "${chart}" 2>/dev/null || true)"
    if [ -z "${rendered}" ]; then
      echo "WARN: ${chart} did not render offline — skipped (Phase 1 covers its sources)."
      continue
    fi
    # `nodePort: 0` is the k8s "unspecified" sentinel (vcluster/upstream
    # ClusterIP Services render it) — inert; only a NONZERO nodePort or a
    # `type: NodePort` Service is a real violation.
    hits="$(printf '%s\n' "${rendered}" \
      | grep -nE '(^|[[:space:]])type:[[:space:]]*NodePort|(^|[[:space:]])nodePort:[[:space:]]*[1-9][0-9]*' \
      || true)"
    if [ -n "${hits}" ]; then
      fail "chart ${chart} RENDERS a NodePort (#4765):"
      printf '%s\n' "${hits}" >&2
    else
      echo "OK — ${chart} renders zero NodePorts."
    fi
  done
fi

echo ""
if [ "${EXIT}" -ne 0 ]; then
  echo "───────────────────────────────────────────────────────────────" >&2
  echo "NodePorts are ABSOLUTELY FORBIDDEN (founder 2026-07-03, #4765)." >&2
  echo "The gateway is served DIRECT (cilium LB-IPAM VIP / shared-EIP, or" >&2
  echo "gateway-api hostNetwork node:443/:80); clustermesh dials the VIP" >&2
  echo ":2379. If a fix appears to need a NodePort, it is the WRONG fix —" >&2
  echo "find the no-NodePort path (LoadBalancer + lbipam.cilium.io/sharing-" >&2
  echo "key: sovereign-vip, or hostNetwork host ports)." >&2
  echo "───────────────────────────────────────────────────────────────" >&2
  exit 1
fi
echo "OK: zero NodePorts across sources + rendered charts (#4765)."
exit 0
