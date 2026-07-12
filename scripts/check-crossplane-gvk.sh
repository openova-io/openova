#!/usr/bin/env bash
# check-crossplane-gvk.sh — CI guard that the Crossplane adoption-seam GVKs
# stay pinned to the group the installed provider actually registers.
#
# History (#4788, the class this guard kills): the cluster template installs
# upbound provider-opentofu, which registers the **opentofu.upbound.io** CRD
# group. Twice, adoption-seam manifests were authored against the DEAD
# **tf.upbound.io** group (the frozen provider-terraform group, never
# registered on a Sovereign):
#   - #4781/#4739 — the cloudadoption Composition's Workspace base GVK
#     (chart 1.3.1 fixed it to opentofu.upbound.io), and
#   - #4788/PR #4789 — clusters/_template ProviderConfig/default on
#     tf.upbound.io/v1beta1 → the CR could never be served → EVERY adopt-*
#     Workspace stuck Synced=False "ProviderConfig ... default not found"
#     (live-proven hw225, UAT rows 206/207/239).
# Nothing structural stopped a third recurrence — this guard does.
#
# Phase 1 (dead-group scan) — fail any LIVE (non-comment) reference to an
#   *.upbound.io API group other than the allowlisted one(s) across the
#   static sources (platform/ products/ clusters/ infra/ core/). Comments
#   documenting the bug history are fine; a real apiVersion/GVK is not.
# Phase 2 (pairing assertion) — the pinned Provider package and its two
#   consumer call-sites must AGREE:
#     Provider spec.package  → upbound/provider-opentofu    (registers group)
#     ProviderConfig default → apiVersion opentofu.upbound.io/v1beta1
#     Composition Workspace  → apiVersion opentofu.upbound.io/v1beta1
#   If someone swaps the provider package, this screams until the consumers
#   (and the allowlist below) move in lockstep.
#
# Adding a genuinely NEW upbound provider later? Extend ALLOWED_GROUPS and
# add a Phase-2 pairing block for it — do not delete the guard.
#
# Usage:  scripts/check-crossplane-gvk.sh
# Exit:   0 = clean, 1 = a dead/mismatched GVK reappeared, 2 = setup error.

set -euo pipefail

ROOT="${ROOT:-.}"
cd "${ROOT}"

EXIT=0
fail() {
  echo "FAIL: $1" >&2
  EXIT=1
}

# The only upbound API group a Sovereign serves today: provider-opentofu's.
ALLOWED_GROUPS='opentofu\.upbound\.io'

SCAN_ROOTS=(platform products clusters infra core)

# ─── Phase 1 — dead-group static scan ────────────────────────────────────
#
# Any *.upbound.io group reference on a LIVE line that is not allowlisted is
# a violation. `xpkg.upbound.io` is the package REGISTRY host (image pulls),
# not an API group — always allowed. Comment lines (# // *) are skipped:
# they document the #4781/#4788 history on purpose.
echo "== Phase 1: dead upbound.io API-group scan =="
RAW="$(grep -rnIE '[a-z0-9-]+\.upbound\.io' \
  --include='*.yaml' --include='*.yml' \
  --include='*.tf' --include='*.tftpl' \
  --include='*.go' --include='*.ts' \
  "${SCAN_ROOTS[@]}" 2>/dev/null || true)"

VIOLATIONS=""
if [ -n "${RAW}" ]; then
  while IFS= read -r line; do
    [ -z "${line}" ] && continue
    # line == path:lineno:content — strip path:lineno: for the tests below.
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
    # Drop the allowed group + the package-registry host, then re-test: any
    # surviving *.upbound.io token is a dead/unknown group.
    stripped="$(printf '%s' "${content}" \
      | sed -E "s/${ALLOWED_GROUPS}//g; s/xpkg\.upbound\.io//g")"
    if printf '%s' "${stripped}" | grep -qE '[a-z0-9-]+\.upbound\.io'; then
      VIOLATIONS="${VIOLATIONS}${line}"$'\n'
    fi
  done <<< "${RAW}"
fi

if [ -n "${VIOLATIONS}" ]; then
  fail "a non-allowlisted *.upbound.io API group reappeared (#4788 class):"
  printf '%s' "${VIOLATIONS}" >&2
else
  echo "OK — every live upbound.io reference is opentofu.upbound.io (or the xpkg registry host)."
fi

# ─── Phase 2 — provider ↔ consumer pairing assertion ─────────────────────
echo ""
echo "== Phase 2: provider package ↔ consumer GVK pairing =="

PROVIDER_FILE=clusters/_template/infrastructure/providers/base/provider-opentofu.yaml
PROVIDERCONFIG_FILE=clusters/_template/infrastructure/base/provider-config-opentofu.yaml
COMPOSITION_FILE=platform/crossplane-claims/chart/templates/compositions/cloudadoption.yaml

for f in "${PROVIDER_FILE}" "${PROVIDERCONFIG_FILE}" "${COMPOSITION_FILE}"; do
  if [ ! -f "${f}" ]; then
    echo "FAIL: expected file missing: ${f} (guard needs updating if it moved)" >&2
    exit 2
  fi
done

# 1. The Provider package must be provider-opentofu (the group registrar).
if grep -qE '^\s*package:\s*\S*/provider-opentofu:' "${PROVIDER_FILE}"; then
  echo "OK — ${PROVIDER_FILE} pins upbound/provider-opentofu."
else
  fail "${PROVIDER_FILE} no longer pins provider-opentofu — update the consumer GVKs + this guard's allowlist in lockstep."
fi

# 2. The template ProviderConfig must sit on the group that provider serves.
if grep -qE '^apiVersion:\s*opentofu\.upbound\.io/v1beta1\s*$' "${PROVIDERCONFIG_FILE}"; then
  echo "OK — ${PROVIDERCONFIG_FILE} ProviderConfig is opentofu.upbound.io/v1beta1."
else
  fail "${PROVIDERCONFIG_FILE} ProviderConfig is NOT opentofu.upbound.io/v1beta1 — the exact #4788 regression (adopt-* Workspaces would fail Sync: 'ProviderConfig default not found')."
fi

# 3. The cloudadoption Composition's Workspace base must match too (#4781).
if grep -qE '^\s*apiVersion:\s*opentofu\.upbound\.io/v1beta1\s*$' "${COMPOSITION_FILE}"; then
  echo "OK — ${COMPOSITION_FILE} Workspace base is opentofu.upbound.io/v1beta1."
else
  fail "${COMPOSITION_FILE} Workspace base is NOT opentofu.upbound.io/v1beta1 — the exact #4781 regression (CloudAdoption never Ready, empty 'kubectl get managed')."
fi

echo ""
if [ "${EXIT}" -ne 0 ]; then
  echo "───────────────────────────────────────────────────────────────" >&2
  echo "The adoption seam installs provider-opentofu, which serves the"     >&2
  echo "opentofu.upbound.io CRD group. tf.upbound.io (provider-terraform)"   >&2
  echo "is NEVER registered on a Sovereign — a GVK on it validates in CI"    >&2
  echo "yaml lint but can never be served live (#4781, #4788, UAT rows"      >&2
  echo "206/207/239). Fix the GVK, or move provider + consumers + this"      >&2
  echo "guard's allowlist in one PR."                                        >&2
  echo "───────────────────────────────────────────────────────────────" >&2
  exit 1
fi
echo "OK: adoption-seam GVKs consistent with the installed provider (#4788)."
exit 0
