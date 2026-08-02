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

echo "== Phase 0: guard self-test (vacuity) =="
# #5517: this guard is an ABSENCE assertion, and absence assertions fail OPEN.
# Two ways this script could have passed while checking nothing:
#   1. SCAN_ROOTS drift (a dir renamed/moved) -> grep matches no files -> RAW
#      empty -> "no violations". Indistinguishable from a clean repo.
#   2. PATTERN rot (an edit that breaks the regex) -> matches nothing, forever
#      green.
# The #5473 lesson generalised: a negative assertion needs a POSITIVE control.
# Phase 0 proves the pattern still bites and the scan still reaches real files
# BEFORE any verdict is trusted. Exit 2 (not 1) so a broken guard is never
# mistaken for a NodePort violation.

# 0a. PATTERN must still match every banned form. If an edit breaks the regex
#     this fails loudly instead of going quietly green.
for probe in \
  '  type: NodePort' \
  '  serviceType: NodePort' \
  '  nodePort: 30853' \
  '  type = "NodePort"'
do
  if ! printf '%s\n' "${probe}" | grep -qE "${PATTERN}"; then
    echo "GUARD BROKEN (#5517): PATTERN no longer matches a banned form: ${probe}" >&2
    echo "  The scan below would report 'clean' while NodePorts pass through." >&2
    exit 2
  fi
done

# 0b. PATTERN must NOT match the k8s inert sentinel or the LB viewer label,
#     or the guard would cry wolf on every LoadBalancer Service.
for negprobe in '  nodePort: 0' '  allocateLoadBalancerNodePorts: true'; do
  if printf '%s\n' "${negprobe}" | grep -qE "${PATTERN}"; then
    echo "GUARD OVER-BROAD (#5517): PATTERN matches an allowed form: ${negprobe}" >&2
    exit 2
  fi
done

# 0c. The scan must actually reach files. A floor, not an exact count, so
#     ordinary repo growth never trips it but a vanished tree does.
# `|| true` is load-bearing: under `set -e` a grep that matches nothing exits
# non-zero and would abort the script HERE, before the check below ever runs --
# making this very vacuity guard unreachable in exactly the scenario it exists
# to catch. Observed while proving the guard: a SCAN_ROOTS drift exited 1 from
# the assignment instead of 2 from the check.
SCANNED="$( { grep -rlIE '.' \
  --include='*.yaml' --include='*.yml' \
  --include='*.tf' --include='*.tftpl' \
  --include='*.go' --include='*.ts' \
  "${SCAN_ROOTS[@]}" 2>/dev/null || true; } | wc -l | tr -d ' ')"
if [ "${SCANNED:-0}" -lt 200 ]; then
  echo "GUARD VACUOUS (#5517): static scan reached only ${SCANNED} files across" >&2
  echo "  SCAN_ROOTS=(${SCAN_ROOTS[*]}) — expected >=200." >&2
  echo "  A root was renamed/moved, so a 'clean' result would prove nothing." >&2
  exit 2
fi
echo "  self-test OK: pattern matches 4/4 banned forms, rejects 2/2 allowed forms, ${SCANNED} files in scope"

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

# ─── Phase 2 — rendered-chart scan ───────────────────────────────────────
#
# Charts that cannot render offline used to be WARN-skipped silently and the
# run still printed "zero NodePorts across sources + rendered charts". On
# 2026-08-01 that was 17 of 89 charts — 19% of the fleet carried NO render
# coverage while the summary claimed it did (#5512). Phase 1 only matches
# literal source patterns, so a values-driven NodePort inside a chart that
# never renders would pass BOTH phases.
#
# The skip is now RATCHETED. RENDER_SKIP_ALLOWLIST records the charts known
# not to render with default values — every one fails a deliberate fail-loud
# `required` / `fail` guard (e.g. bp-anthropic-adapter's "image.tag MUST be
# SHA-pinned by the per-cluster overlay"), which is correct chart design, not
# a defect. A chart OUTSIDE that list failing to render is a NEW blind spot
# and FAILS the guard. A chart INSIDE it that now renders is reported so the
# list cannot rot (same anti-rot reasoning as the Phase 0a pattern self-test).
RENDER_SKIP_ALLOWLIST=(
  platform/anthropic-adapter/chart
  platform/bp-dmz-vcluster/chart
  platform/catalyst-edge-routes/chart
  platform/cilium-policies/chart
  platform/cnpg-pair/chart
  platform/guacamole/chart
  platform/hcloud-csi/chart
  platform/huawei-evs-csi/chart
  platform/k8s-ws-proxy/chart
  platform/knative/chart
  platform/librechat/chart
  platform/netbird/chart
  platform/oidc-gate/chart
  platform/plane-isolation/chart
  platform/postgres/chart
  platform/sandbox/chart
  platform/sovereign-tls-vars/chart
)

is_render_skip_allowed() {
  local needle="$1" c
  for c in "${RENDER_SKIP_ALLOWLIST[@]}"; do
    [ "${c}" = "${needle}" ] && return 0
  done
  return 1
}

RENDERED_COUNT=0
SKIPPED_COUNT=0
RENDER_PHASE_RAN=0
UNEXPECTED_RENDERABLE=""

echo ""
echo "== Phase 2: rendered-chart NodePort scan =="
if ! command -v helm >/dev/null 2>&1; then
  echo "WARN: helm not on PATH — skipping the rendered-chart phase (Phase 1"
  echo "      already covers chart template sources). Install helm to enforce."
else
  RENDER_PHASE_RAN=1
  shopt -s nullglob
  # Per-chart render timeout so a pathological chart can never hang CI.
  #
  # Charts render OFFLINE from their vendored charts/*.tgz — that is the fast
  # path and it is tried first, always. What this used to assume, and what was
  # false, is that the tarballs are THERE. `.gitignore:6` ignores
  # `platform/*/chart/charts/`, and no Chart.lock is committed for any of the
  # 67 dependency-bearing charts, so a clean checkout has neither. The vendored
  # copies exist only on a machine that has previously run a dependency update.
  #
  # That is why this guard passed locally and failed in CI on the SAME commit:
  # locally alloy rendered from a cached alloy-1.8.0.tgz; on the runner helm
  # said "found in Chart.yaml, but missing in charts/ directory: alloy", the
  # render came back empty, and the (correct, #5512) ratchet failed the build.
  # Every one of the 67 would have done the same.
  #
  # So: keep offline-first, and fall back to a bounded `helm dependency update`
  # ONLY when the failure is specifically a missing vendored dependency. Helm
  # resolves the repository URL straight from Chart.yaml — verified against a
  # helm config with zero repos registered, which is what a runner looks like:
  # "Getting updates for unmanaged Helm repositories...". Set NO_NETWORK=1 to
  # suppress the fallback (air-gapped); charts then fail as before.
  HELM_TIMEOUT="${HELM_TIMEOUT:-30s}"
  DEP_TIMEOUT="${DEP_TIMEOUT:-120s}"
  VENDORED_ON_DEMAND=0
  CHART_DIRS=(platform/*/chart products/*/charts/*)
  for chart in "${CHART_DIRS[@]}"; do
    [ -f "${chart}/Chart.yaml" ] || continue
    rendered="$(timeout "${HELM_TIMEOUT}" helm template "$(basename "${chart}")" "${chart}" 2>/dev/null || true)"

    # Missing-dependency recovery. Narrow on purpose: a chart that fails for
    # ANY other reason (a fail-loud values guard, a template bug) must still
    # reach the ratchet below rather than being masked by a network round-trip.
    if [ -z "${rendered}" ] && [ "${NO_NETWORK:-0}" != "1" ]; then
      why="$(timeout "${HELM_TIMEOUT}" helm template "$(basename "${chart}")" "${chart}" 2>&1 || true)"
      if printf '%s' "${why}" | grep -q 'missing in charts/ directory'; then
        if timeout "${DEP_TIMEOUT}" helm dependency update "${chart}" >/dev/null 2>&1; then
          rendered="$(timeout "${HELM_TIMEOUT}" helm template "$(basename "${chart}")" "${chart}" 2>/dev/null || true)"
          [ -n "${rendered}" ] && VENDORED_ON_DEMAND=$((VENDORED_ON_DEMAND + 1))
        fi
      fi
    fi

    if [ -z "${rendered}" ]; then
      SKIPPED_COUNT=$((SKIPPED_COUNT + 1))
      if is_render_skip_allowed "${chart}"; then
        echo "SKIP: ${chart} does not render offline (known fail-loud values guard) — Phase 1 covers its sources."
      else
        fail "chart ${chart} did NOT render offline and is not in RENDER_SKIP_ALLOWLIST — a values-driven NodePort here would evade BOTH phases (#5512). Either make it render offline, or add it to the allowlist with a reason:"
        timeout "${HELM_TIMEOUT}" helm template "$(basename "${chart}")" "${chart}" 2>&1 | head -3 >&2
      fi
      continue
    fi
    RENDERED_COUNT=$((RENDERED_COUNT + 1))
    if is_render_skip_allowed "${chart}"; then
      UNEXPECTED_RENDERABLE="${UNEXPECTED_RENDERABLE}  ${chart}"$'\n'
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
if [ -n "${UNEXPECTED_RENDERABLE}" ]; then
  echo "NOTE: these charts are in RENDER_SKIP_ALLOWLIST but now render offline."
  echo "      Remove them from the list so the allowlist cannot rot:"
  printf '%s' "${UNEXPECTED_RENDERABLE}"
  echo ""
fi

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
# State the ACTUAL coverage. The old summary said "sources + rendered charts"
# unconditionally, including when the render phase never ran or skipped a
# fifth of the fleet (#5512).
if [ "${RENDER_PHASE_RAN}" -eq 1 ]; then
  echo "OK: zero NodePorts — sources clean; ${RENDERED_COUNT} chart(s) rendered and scanned, ${SKIPPED_COUNT} allowlisted chart(s) covered by the source scan only (#4765)."
  # Print it even at 0: the difference between "0 fetched because everything was
  # vendored" and "0 fetched because the fallback never fired" is the whole
  # local-vs-CI divergence this line exists to expose.
  echo "     dependencies fetched on demand this run: ${VENDORED_ON_DEMAND:-0} (NO_NETWORK=1 disables the fetch)"
else
  echo "OK: zero NodePorts in sources (#4765). Rendered-chart phase did NOT run (helm absent) — this run does NOT cover values-driven NodePorts."
fi
exit 0
