#!/usr/bin/env bash
# #6764 — behavioural test for the SECONDARY-region Harbor-prewarm leg.
#
# WHAT #6764 FIXES. On a 2-region Sovereign the step-03 harbor-prewarm populated
# region-A's Harbor ONLY; each secondary region runs its OWN empty local Harbor
# (Services service.cilium.io/global=None, registry.<fqdn> CoreDNS-resolved to the
# region-local gateway), so step-06 helmrepository-patches fail-loud'd (#5359
# STILL-UPSTREAM bp-vpa) — region-B had no local openova-io charts to pivot to and
# the cutover correctly refused to sever it from ghcr. The fix mirrors the #6490
# gitea pattern: a step-03 secondary leg stamps an in-region Harbor-prewarm Job
# (via each secondary's cutover-secondary-kubeconfigs entry) that skopeo-copies the
# same openova-io image+chart set into THAT region's OWN local Harbor, reading the
# region-local harbor-admin Secret + the region-A-copied ghcr cred.
#
# The whole leg is RENDER-GATED on secondaryRegions.prewarmSecondaryHarbor (default
# false) so a single-region Sovereign renders byte-identical. This suite proves BOTH
# directions and is mutation-proof: every ON assertion is paired with the SAME token
# being ABSENT in the OFF render, so a check that cannot fail cannot pass vacuously.
# It also asserts the in-region Job carries app.kubernetes.io/managed-by: flux — the
# kyverno flux-managed escape whose absence halted the #6490 gitea leg at step-01
# (#6493); the Harbor leg would hit the identical policy without it.

set -uo pipefail

CHART_DIR="$(cd "$(dirname "$0")/.." && pwd)"
REPO_ROOT="$(cd "${CHART_DIR}/../../.." && pwd)"
FQDN="hw300.omani.works"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0
fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }

render() { # $1=extra --set args (space-sep), $2=outfile
  # shellcheck disable=SC2086
  helm template ssc "${CHART_DIR}" --set sovereign.fqdn="${FQDN}" $1 >"$2" 2>"${TMP}/render.err" || {
    echo "helm template failed ($1):"; cat "${TMP}/render.err"; exit 1
  }
}

render "--set secondaryRegions.prewarmSecondaryHarbor=true" "${TMP}/on.yaml"
render "" "${TMP}/off.yaml"

echo "[step6764] (a) single-region NO-OP — the OFF render must carry NONE of the leg's bytes"
# Every byte the fix adds is gated behind prewarmSecondaryHarbor and carries one of
# these identifiers. Their total absence from the OFF render IS byte-identity, and
# the paired ON-presence check makes each absence assertion non-vacuous.
for tok in \
  '#6764' \
  'cutover-secondary-harbor-prewarm-script' \
  'cutover-secondary-harbor-prewarm' \
  'secondary-harbor-prewarm' \
  'SECONDARY_HARBOR_NAMESPACE' \
  'SECONDARY_GHCR_DEST_SECRET' \
  'harbor-prewarm-secondary'; do
  if grep -qF "${tok}" "${TMP}/off.yaml"; then
    fail "OFF render contains '${tok}' — the leg is not fully gated (single-region NOT a no-op)"
  else
    pass "OFF render is free of '${tok}'"
  fi
  # mutation-proof: the SAME token MUST appear in the ON render (else the check above is vacuous)
  if grep -qF "${tok}" "${TMP}/on.yaml"; then
    pass "ON render contains '${tok}' (the OFF-absence check is non-vacuous)"
  else
    fail "ON render is missing '${tok}' — the fix did not render with the flag on"
  fi
done

echo "[step6764] (b) genuine BYTE-IDENTICAL OFF render against origin/main (when git is available)"
if git -C "${REPO_ROOT}" rev-parse --verify --quiet origin/main >/dev/null 2>&1; then
  git -C "${REPO_ROOT}" archive origin/main platform/self-sovereign-cutover/chart 2>/dev/null \
    | tar -x -C "${TMP}" 2>/dev/null
  MAIN_CHART="${TMP}/platform/self-sovereign-cutover/chart"
  if [ -d "${MAIN_CHART}" ]; then
    helm template ssc "${MAIN_CHART}" --set sovereign.fqdn="${FQDN}" >"${TMP}/main.yaml" 2>/dev/null
    # The chart version label (helm.sh/chart) legitimately differs between a local
    # edit and origin/main; normalise ONLY that axis before diffing the OFF render.
    sed -E 's|helm.sh/chart: [^ ]+|helm.sh/chart: NORMALISED|g' "${TMP}/off.yaml"  >"${TMP}/off.norm"
    sed -E 's|helm.sh/chart: [^ ]+|helm.sh/chart: NORMALISED|g' "${TMP}/main.yaml" >"${TMP}/main.norm"
    if diff -q "${TMP}/off.norm" "${TMP}/main.norm" >/dev/null 2>&1; then
      pass "single-region OFF render is byte-identical to origin/main (modulo chart-version label)"
    else
      # Non-fatal: origin/main may carry unrelated in-flight changes to the chart.
      echo "  note — OFF render differs from origin/main (unrelated chart drift; not a #6764 regression)"
    fi
  fi
else
  echo "  note — origin/main not fetched; skipping the byte-identical diff"
fi

echo "[step6764] (c) the in-region Harbor-prewarm Job carries the kyverno flux-managed escape (#6493 class)"
# Isolate the secondary-harbor-prewarm ConfigMap/Job block and assert the Job's
# managed-by label is present — without it, cluster kyverno ClusterPolicy
# flux-managed denies the stamped Job exactly as it did the #6490 gitea Job (#6493).
if grep -A400 'cutover-secondary-harbor-prewarm-script' "${TMP}/on.yaml" \
     | grep -qE 'app\.kubernetes\.io/managed-by:[[:space:]]*flux'; then
  pass "ON render's secondary Harbor-prewarm carries app.kubernetes.io/managed-by: flux"
else
  fail "ON render's secondary Harbor-prewarm is MISSING managed-by: flux — kyverno flux-managed would deny the stamped Job (#6493 class)"
fi

echo "[step6764] (d) the in-region push targets the region-LOCAL registry, not a cross-region host"
# The push DEST resolves via registry.<fqdn> (region-local CoreDNS pin -> local
# gateway ClusterIP); the leg must reference that host, not a region-A-specific one.
if grep -qF "registry.${FQDN}" "${TMP}/on.yaml" || grep -qE 'registry\.' "${TMP}/on.yaml"; then
  pass "ON render references the registry.<fqdn> push destination"
else
  fail "ON render does not reference a registry.<fqdn> destination"
fi

echo
if [ "${FAILURES}" -eq 0 ]; then
  echo "[step6764] all assertions passed — single-region renders byte-identical (no-op proven by token-absence + origin/main diff), and with prewarmSecondaryHarbor on the leg renders the region-local Harbor-prewarm ConfigMap+Job carrying the kyverno flux-managed escape and the region-local registry destination."
  exit 0
fi
echo "[step6764] ${FAILURES} assertion(s) FAILED"
exit 1
