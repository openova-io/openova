#!/usr/bin/env bash
# check-adoption-endpoint-region.sh — CI guard that the Crossplane
# cloudadoption module composes its Huawei API endpoint hosts from the
# REAL HCS region, never the kom4dc 2-VPC-mimic pseudo-region.
#
# History (#5270, the class this guard kills): kom4dc (single-region HCS)
# mimics a 2-region topology with two isolated VPCs whose Kubernetes-side
# region identity is the PSEUDO-region `me-east-215-a` / `me-east-215-b`.
# That value is correct for placement/labels/region-affinity — but HCS
# publishes API DNS ONLY for the real region:
#   {iam,ecs,vpc,elb}.me-east-215.kom4dc.nationalcloud.om   -> 212.72.2.97
#   {iam,ecs,vpc,elb}.me-east-215-a.kom4dc.nationalcloud.om -> NXDOMAIN
# The adoption Workspaces inherited the claim's pseudo-region into their
# endpoint URLs, so 16/24 adoptions could never Observe (live-proven
# hw278, 2026-07-19, UAT rows 206/207/239). Chart 1.3.4 maps mimic->real
# via `local.hw_api_region` (strip a trailing -a/-b after a digit-ending
# base) and builds every endpoint host from it.
#
# Phase 1 (structural) — render the chart, extract the inline OpenTofu
#   module from the cloudadoption Composition, assert every huaweicloud
#   endpoint host is built from `local.hw_api_region` (and none from
#   `local.hw_region`), and that the hw_api_region local + output exist.
# Phase 2 (hermetic evaluation) — lift the EXACT hw_region/hw_api_region
#   expressions out of the rendered module into a minimal provider-less
#   OpenTofu config and evaluate them with the real tofu runtime, offline:
#     me-east-215-a -> me-east-215     (mimic zone a)
#     me-east-215-b -> me-east-215     (mimic zone b)
#     me-east-215   -> me-east-215     (real region — unchanged)
#     ae-ad-1       -> ae-ad-1         (real non-mimic region — unchanged)
#     ""            -> me-east-215     (var-empty default path)
#   This evaluates the shipped expression itself, not a copy — if the
#   locals are renamed/moved the extraction fails loudly (exit 2).
# Phase 3 (full-module validate, network) — `tofu init` + `tofu validate`
#   the complete extracted module (downloads the huaweicloud + hcloud
#   providers). Kills the #4739 class (OpenTofu rejecting single-line
#   multi-argument blocks) that a YAML-only lint can never see.
#   Skippable for offline runs: OFFLINE=1 ./scripts/check-adoption-endpoint-region.sh
#
# Usage:  scripts/check-adoption-endpoint-region.sh
# Needs:  helm, yq (v4), tofu
# Exit:   0 = clean, 1 = a regression, 2 = setup/extraction error.

set -euo pipefail

ROOT="${ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
CHART="${ROOT}/platform/crossplane-claims/chart"
COMPOSITION="opentofu-cloud-adoption.compose.openova.io"

for tool in helm yq tofu; do
  command -v "${tool}" >/dev/null 2>&1 || { echo "ERROR: ${tool} not installed" >&2; exit 2; }
done
[ -d "${CHART}" ] || { echo "ERROR: chart not found at ${CHART}" >&2; exit 2; }

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

EXIT=0
fail() {
  echo "FAIL: $1" >&2
  EXIT=1
}

# ─── Phase 1 — render + structural assertions ────────────────────────────
echo "== Phase 1: render chart + extract the cloudadoption inline module =="
helm template adoption-region-check "${CHART}" > "${WORK}/rendered.yaml"

yq ea "select(.kind == \"Composition\" and .metadata.name == \"${COMPOSITION}\") \
  | .spec.resources[] | select(.name == \"adoption-workspace\") \
  | .base.spec.forProvider.module" "${WORK}/rendered.yaml" > "${WORK}/module.tf"

if [ ! -s "${WORK}/module.tf" ] || [ "$(head -c4 "${WORK}/module.tf")" = "null" ]; then
  echo "ERROR: could not extract the inline module from Composition ${COMPOSITION}" >&2
  exit 2
fi

for svc in iam ecs vpc elb; do
  if ! grep -Eq "${svc} = \"https://${svc}\.\\\$\{local\.hw_api_region\}\.kom4dc\.nationalcloud\.om\"" "${WORK}/module.tf"; then
    fail "endpoint host for '${svc}' is not built from local.hw_api_region (#5270 regression)"
  fi
done
if grep -E '\.\$\{local\.hw_region\}\.' "${WORK}/module.tf" | grep -q .; then
  fail "an endpoint host is built from local.hw_region (the claim/mimic region) — must use local.hw_api_region"
fi
grep -Eq '^\s*hw_api_region\s+=' "${WORK}/module.tf" \
  || fail "module no longer defines the hw_api_region local"
grep -Eq '^output "hw_api_region"' "${WORK}/module.tf" \
  || fail "module no longer exposes the hw_api_region output (Phase-2 hook + live debuggability)"

# ─── Phase 2 — hermetic evaluation of the shipped expressions ────────────
echo "== Phase 2: evaluate the mimic->real region mapping with tofu (offline) =="
mkdir -p "${WORK}/eval"
HW_REGION_LINE="$(grep -E '^\s*hw_region\s+=' "${WORK}/module.tf" || true)"
HW_API_REGION_LINE="$(grep -E '^\s*hw_api_region\s+=' "${WORK}/module.tf" || true)"
if [ -z "${HW_REGION_LINE}" ] || [ -z "${HW_API_REGION_LINE}" ]; then
  echo "ERROR: could not extract the hw_region/hw_api_region locals from the rendered module" >&2
  exit 2
fi
{
  echo 'variable "huawei_region" { type = string }'
  echo 'locals {'
  echo "${HW_REGION_LINE}"
  echo "${HW_API_REGION_LINE}"
  echo '}'
  echo 'output "hw_api_region" { value = local.hw_api_region }'
} > "${WORK}/eval/main.tf"

( cd "${WORK}/eval" && tofu init -backend=false -input=false >/dev/null )

check_case() {
  local input="$1" want="$2" got
  ( cd "${WORK}/eval" && tofu apply -auto-approve -input=false -var "huawei_region=${input}" >/dev/null )
  got="$(cd "${WORK}/eval" && tofu output -raw hw_api_region)"
  if [ "${got}" = "${want}" ]; then
    echo "  PASS  huawei_region='${input}' -> '${got}'"
  else
    fail "huawei_region='${input}' -> '${got}' (want '${want}')"
  fi
}

check_case "me-east-215-a" "me-east-215"   # kom4dc mimic zone a
check_case "me-east-215-b" "me-east-215"   # kom4dc mimic zone b
check_case "me-east-215"   "me-east-215"   # real region — pass-through
check_case "ae-ad-1"       "ae-ad-1"       # real non-mimic region — pass-through
check_case ""              "me-east-215"   # var-empty default path

# ─── Phase 3 — full-module validate (needs provider downloads) ───────────
if [ "${OFFLINE:-0}" = "1" ]; then
  echo "== Phase 3: SKIPPED (OFFLINE=1) =="
else
  echo "== Phase 3: tofu validate the complete inline module =="
  mkdir -p "${WORK}/full"
  cp "${WORK}/module.tf" "${WORK}/full/main.tf"
  ( cd "${WORK}/full" && tofu init -backend=false -input=false >/dev/null && tofu validate ) \
    || fail "full-module tofu validate failed (#4739 class: module syntax broken)"
fi

if [ "${EXIT}" -eq 0 ]; then
  echo "OK: adoption endpoints are composed from the real HCS region (mimic -a/-b stripped, real regions untouched)."
fi
exit "${EXIT}"
