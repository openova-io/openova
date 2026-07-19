#!/usr/bin/env bash
# check-no-local-path.sh — CI guard that the local-path StorageClass stays
# FORBIDDEN at land, forever (#3971).
#
# local-path (rancher.io/local-path) is a directory on the node's local disk —
# NO cloud block volume behind it — so any node replacement destroys the data.
# The P0 blocker #3971 was that EVERY Sovereign's PVCs landed on local-path.
# The durable fix FORBIDS local-path entirely, in three runtime layers:
#   1. k3s runs `--disable=local-storage` (cloud-init) — the provisioner is
#      NEVER installed on a fresh prov.
#   2. The per-provider durable cloud CSI is the ONLY default StorageClass
#      (bp-hcloud-csi `hcloud-volumes` on Hetzner, bp-huawei-evs-csi `evs-ssd`
#      on Huawei — bootstrap-kit slots 55a/55b).
#   3. The bp-kyverno-policies K23 ClusterPolicy (ENFORCE) DENIES any
#      PVC/StorageClass/PV that names local-path or the rancher.io/local-path
#      provisioner at admission.
#
# This guard is the LAND-TIME layer: it fails ANY future PR that reintroduces
# a local-path reference into shipped charts/manifests/IaC, so the runtime
# layers can never silently regress. Two phases (same shape as
# scripts/check-no-nodeports.sh, #4765):
#   Phase 1 (authoritative, always enforced) — a static scan of the repo
#     sources (platform/ products/ clusters/ infra/ core/) for local-path
#     StorageClass field/literal patterns, ignoring comments and lines that
#     merely DOCUMENT the ban.
#   Phase 2 (best-effort) — `helm template` every chart under platform/*/chart
#     and products/*/charts/* with default values and grep the RENDERED
#     output for storageClassName/storageClass/provisioner fields resolving
#     to local-path (catches values-driven defaults that only appear
#     post-render). A chart that cannot be rendered offline is WARN-skipped —
#     Phase 1 already covers its template sources.
#
# Usage:  scripts/check-no-local-path.sh
# Exit:   0 = clean, 1 = a local-path reference reappeared, 2 = setup error.

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
# Forbidden real-field / real-literal patterns (NOT comments, NOT lines that
# document the ban):
#   storageClassName: local-path      (PVC / StatefulSet volumeClaimTemplate)
#   storageClass: local-path          (chart values / HR values overlays)
#   storage_class = "local-path"      (Terraform / HCL, incl. *_storage_class
#                                     assignment shapes)
#   rancher.io/local-path             (the provisioner literal — a rendered
#                                     StorageClass or an image/provisioner pin)
# The `!=` comparisons in infra/ tf `validation` blocks (the ban's own
# guards) do NOT match: `[:=]` requires the char right after the key to be
# `:` or `=`, and `var.default_storage_class` is not preceded by whitespace.
PATTERN='(^|[[:space:]])(storageClassName|storageClass|default)[[:space:]]*:[[:space:]]*"?local-path"?([[:space:]]|$)|(^|[[:space:]])([A-Za-z_]*storage_class(_name)?|default)[[:space:]]*=[[:space:]]*"local-path"|rancher\.io/local-path'

SCAN_ROOTS=(platform products clusters infra core)

# A candidate line is a VIOLATION unless it is a comment or documents the
# ban. Strip leading whitespace, drop lines whose first char is a comment
# marker (# //), and drop lines that carry an explicit ban marker.
BAN_MARKERS='FORBIDDEN|forbidden|#3971|#892|NEVER|never|must not|DENIED|denied|deny'

echo "== Phase 1: static local-path source scan =="
RAW="$(grep -rnIE "${PATTERN}" \
  --include='*.yaml' --include='*.yml' \
  --include='*.tf' --include='*.tftpl' --include='*.tfvars' \
  "${SCAN_ROOTS[@]}" 2>/dev/null || true)"

VIOLATIONS=""
if [ -n "${RAW}" ]; then
  while IFS= read -r line; do
    [ -z "${line}" ] && continue
    # Skip the K23 forbid-local-path ENFORCE policy + its test fixtures: they
    # INTENTIONALLY name `local-path` / `rancher.io/local-path` as the
    # admission DENY-list values and the "bad" kyverno-cli test inputs that
    # prove the policy actually CATCHES a local-path PVC/SC (#3971). They are
    # the enforcement layer itself, never a deployed local-path consumer —
    # excluding them is correct, not a weakening (same shape as the NodePort
    # gate's kyverno-fixture exclusion, #5088/#5089).
    case "${line%%:*}" in
      platform/kyverno-policies/chart/templates/baseline/23-forbid-local-path.yaml) continue ;;
      platform/kyverno-policies/chart/tests/*) continue ;;
    esac
    # line == path:lineno:content — strip the path:lineno: prefix for the
    # comment/marker tests.
    content="${line#*:}"; content="${content#*:}"
    trimmed="${content#"${content%%[![:space:]]*}"}"
    first="${trimmed:0:1}"
    # Skip pure comment lines (# yaml/tf, // just in case).
    case "${first}" in
      '#' | '*') continue ;;
    esac
    case "${trimmed}" in
      '//'*) continue ;;
    esac
    # Skip lines that DOCUMENT the ban (they name the forbidden thing on
    # purpose, e.g. a Kyverno validate message or a tf error_message).
    if printf '%s' "${content}" | grep -qE "${BAN_MARKERS}"; then
      continue
    fi
    VIOLATIONS="${VIOLATIONS}${line}"$'\n'
  done <<< "${RAW}"
fi

if [ -n "${VIOLATIONS}" ]; then
  fail "a local-path StorageClass reference reappeared in repo sources (#3971):"
  printf '%s' "${VIOLATIONS}" >&2
else
  echo "OK — no local-path StorageClass reference in ${SCAN_ROOTS[*]} sources."
fi

# ─── Phase 2 — rendered-chart scan (best-effort) ─────────────────────────
echo ""
echo "== Phase 2: rendered-chart local-path scan =="
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
  # Field-shaped pattern only: a real rendered violation surfaces as a
  # storageClassName/storageClass field or a StorageClass provisioner
  # resolving to local-path. (The K23 policy's rendered DENY-list values are
  # bare `- local-path` list items — deliberately NOT matched.)
  RENDER_PATTERN='(^|[[:space:]])(storageClassName|storageClass)[[:space:]]*:[[:space:]]*"?local-path"?([[:space:]]|$)|(^|[[:space:]])provisioner[[:space:]]*:[[:space:]]*"?rancher\.io/local-path"?([[:space:]]|$)'
  CHART_DIRS=(platform/*/chart products/*/charts/*)
  for chart in "${CHART_DIRS[@]}"; do
    [ -f "${chart}/Chart.yaml" ] || continue
    rendered="$(timeout "${HELM_TIMEOUT}" helm template "$(basename "${chart}")" "${chart}" 2>/dev/null || true)"
    if [ -z "${rendered}" ]; then
      echo "WARN: ${chart} did not render offline — skipped (Phase 1 covers its sources)."
      continue
    fi
    hits="$(printf '%s\n' "${rendered}" \
      | grep -nE "${RENDER_PATTERN}" \
      | grep -vE '^[0-9]+:[[:space:]]*#' \
      || true)"
    if [ -n "${hits}" ]; then
      fail "chart ${chart} RENDERS a local-path StorageClass reference (#3971):"
      printf '%s\n' "${hits}" >&2
    else
      echo "OK — ${chart} renders zero local-path references."
    fi
  done
fi

echo ""
if [ "${EXIT}" -ne 0 ]; then
  echo "───────────────────────────────────────────────────────────────" >&2
  echo "The local-path StorageClass is FORBIDDEN (#3971). It is ephemeral" >&2
  echo "node-local disk — no cloud block volume — so node replacement" >&2
  echo "DESTROYS the data. k3s runs --disable=local-storage (the provisioner" >&2
  echo "is never installed) and the K23 Kyverno ENFORCE policy denies it at" >&2
  echo "admission. Omit storageClassName to bind the durable cluster-default" >&2
  echo "cloud CSI (hcloud-volumes on Hetzner, evs-ssd on Huawei), or name an" >&2
  echo "explicit durable class. If a change appears to need local-path, it" >&2
  echo "is the WRONG change — use the cloud CSI or an emptyDir." >&2
  echo "───────────────────────────────────────────────────────────────" >&2
  exit 1
fi
echo "OK: zero local-path StorageClass references across sources + rendered charts (#3971)."
exit 0
