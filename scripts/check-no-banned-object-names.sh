#!/usr/bin/env bash
# check-no-banned-object-names.sh — Kubernetes OBJECT NAMES may not be
# banned entity-nouns (docs/GLOSSARY.md §Banned-terms).
#
# WHY THIS EXISTS AND WHY IT IS NOT check-no-banned-words.sh
# ---------------------------------------------------------
# UAT row 121 / #3383 asserts the BSS billing screen carries zero
# "tenant" / "SME" leaks. It failed for months against a console tree
# that has no such string anywhere in its source. The term was not being
# WRITTEN by the console — it was being READ from the cluster:
#
#   products/catalyst/chart/templates/org-services/organization.yaml
#     kind: Deployment / metadata.name: tenant
#   → pod tenant-df955b876-294t5, ownerRef ReplicaSet tenant-df955b876
#   → dashboard.go applicationKey() walks pod → RS → Deployment
#   → org_consumption.go emits appConsumption{Application: "tenant"}
#   → /billing/orders renders `tenant / org-services / 25 / 0.03125`
#
# That is the #5435 class: the banned term reaches an operator surface
# from a runtime object NAME, so a source-side prose grep of the console
# is structurally incapable of seeing it and reports clean forever. This
# guard closes the gap at the only place it can be closed before the
# object exists — the chart template that names it.
#
# SCOPE — deliberately narrow, so it means something when it passes:
#   * Only IDENTITY fields are scanned: metadata.name / a container or SA
#     `name:` / serviceAccountName / an `app:` label value. Those are the
#     fields that become, or select, the runtime object name that
#     applicationKey can surface.
#   * Only whole-token matches. `org-tenants` (the GitOps branch),
#     `tenantRoutes` (a values key), `stalwart-tenant` (an upstream chart
#     name) and prose in comments are NOT object names and are NOT
#     flagged.
#   * This guard does NOT claim to cover the live cluster. A chart clean
#     here can still face a cluster carrying the old name until the
#     release rolls — the same source-clean-is-not-platform-clean caveat
#     that scripts/check-live-nodeports.sh exists for. Verify the rolled
#     result with:
#       kubectl -n org-services get deploy -o name
#
# Usage:
#   scripts/check-no-banned-object-names.sh          # scan chart templates
#   scripts/check-no-banned-object-names.sh --self-test-only
#
set -euo pipefail

ROOT="${ROOT:-$(git rev-parse --show-toplevel 2>/dev/null || echo .)}"
EXIT=0

# Banned entity-nouns as OBJECT NAMES. From docs/GLOSSARY.md
# §Banned-terms: "tenant" → Organization, "SME" → Organization.
# Additive only — names enter this list, they do not leave.
BANNED_NAME_TOKENS='tenant|tenants|sme|smes'

# A Kubernetes identity line whose VALUE is exactly a banned token, or is
# a `<prefix>-<banned>` label value (the `app: org-tenant` selector shape,
# which is what pins the Deployment's own name in practice).
IDENTITY_RE="^[[:space:]]*(name|serviceAccountName|app):[[:space:]]*[\"']?([a-z0-9-]+-)?(${BANNED_NAME_TOKENS})[\"']?[[:space:]]*$"

# ─── Vacuity self-test ────────────────────────────────────────────────
#
# An absence-assertion reports "clean" both when the thing is absent AND
# when the detector stopped working (#5512). scripts/check-no-nodeports.sh
# has carried this protection since #4765; every new absence guard must.
# The POSITIVE samples must still match and the NEGATIVE samples must
# still NOT match, or this exits non-zero before scanning anything.
POSITIVE_SAMPLES=(
  '  name: tenant'
  '      serviceAccountName: tenant'
  '      app: org-tenant'
  '  name: "sme"'
)
NEGATIVE_SAMPLES=(
  '  name: organization'
  '      app: org-organization'
  '  CATALYST_GITOPS_BRANCH: org-tenants-overlay'
  '# the tenant of a building is not an object name'
  '  tenantRoutes: []'
  '  name: stalwart-tenant-admin'
)

for s in "${POSITIVE_SAMPLES[@]}"; do
  if ! grep -Eq "${IDENTITY_RE}" <<<"${s}"; then
    echo "SELF-TEST FAIL: identity pattern no longer matches a known-banned sample" >&2
    echo "  sample: ${s}" >&2
    echo "  This guard would pass every chart forever. Fix IDENTITY_RE." >&2
    exit 2
  fi
done
for s in "${NEGATIVE_SAMPLES[@]}"; do
  if grep -Eq "${IDENTITY_RE}" <<<"${s}"; then
    echo "SELF-TEST FAIL: identity pattern matches a CLEAN sample" >&2
    echo "  sample: ${s}" >&2
    echo "  It is too broad and would fail every chart. Fix IDENTITY_RE." >&2
    exit 2
  fi
done

if [[ "${1:-}" == "--self-test-only" ]]; then
  echo "self-test PASS: identity pattern matches all ${#POSITIVE_SAMPLES[@]} banned samples and none of the ${#NEGATIVE_SAMPLES[@]} clean samples"
  exit 0
fi

# ─── Scan ─────────────────────────────────────────────────────────────
mapfile -t FILES < <(
  find "${ROOT}/products" "${ROOT}/platform" "${ROOT}/core" \
    -type d -name node_modules -prune -o \
    -type f \( -name '*.yaml' -o -name '*.yml' \) \
    -path '*/templates/*' -print 2>/dev/null | sort
)

if [[ ${#FILES[@]} -eq 0 ]]; then
  echo "FAIL: found zero chart template files to scan — the scan path is wrong," >&2
  echo "      and a guard that looks at nothing passes at nothing." >&2
  exit 2
fi

# ─── Tracked exceptions ───────────────────────────────────────────────
#
# Two Blueprint CR names in the catalog seed carry a `-tenant` suffix.
# They are NOT the class this guard was written for: a Blueprint name is a
# catalog identifier, not a workload name, so it never reaches the
# showback table through applicationKey. Renaming them relabels the
# Blueprint identity itself — the OCI artifact at
# ghcr.io/openova-io/bp-<name>, platform/stalwart-tenant/, and the
# spec.blueprintRef.name of every Application already installed from
# them — which is a separate change with its own rollout, not a
# consequence of UAT row 121.
#
# The list is checked BIDIRECTIONALLY below: an entry that no longer
# appears in the tree fails this guard just as loudly as a new violation,
# so it self-cleans on the rename instead of rotting into permanent
# cover. It also cannot absorb the class the row is about — a workload
# `name:`/`serviceAccountName:`/`app:` value is not a Blueprint name and
# has no entry here.
TRACKED_EXCEPTIONS=(
  'products/catalyst/chart/templates/catalog-seed/blueprints.yaml:  name: bp-wordpress-tenant'
  'products/catalyst/chart/templates/catalog-seed/blueprints.yaml:  name: bp-stalwart-tenant'
)
declare -A EXCEPTION_SEEN=()

for f in "${FILES[@]}"; do
  rel="${f#"${ROOT}"/}"
  while IFS= read -r hit; do
    [[ -z "${hit}" ]] && continue
    # hit is "<lineno>:<line>" — key the exception on file + line text so
    # a moved line still matches but a changed VALUE does not.
    key="${rel}:${hit#*:}"
    matched=""
    for ex in "${TRACKED_EXCEPTIONS[@]}"; do
      if [[ "${key}" == "${ex}" ]]; then
        EXCEPTION_SEEN["${ex}"]=1
        matched=1
        break
      fi
    done
    [[ -n "${matched}" ]] && continue
    echo "BANNED OBJECT NAME: ${rel}:${hit}" >&2
    EXIT=1
  done < <(grep -nE "${IDENTITY_RE}" "${f}" || true)
done

# Bidirectional half: a tracked exception that no longer exists must be
# deleted from the list. Without this the list is a place violations go
# to be forgotten.
for ex in "${TRACKED_EXCEPTIONS[@]}"; do
  if [[ -z "${EXCEPTION_SEEN[${ex}]:-}" ]]; then
    echo "STALE EXCEPTION: '${ex}' is in TRACKED_EXCEPTIONS but no longer" >&2
    echo "  appears in the tree. Delete the entry — the rename landed." >&2
    EXIT=1
  fi
done

if [[ ${EXIT} -ne 0 ]]; then
  cat >&2 <<'MSG'

A Kubernetes object name above is a banned entity-noun (docs/GLOSSARY.md
§Banned-terms). Object names are not internal: the showback table on
/billing/orders renders the workload name resolved by dashboard.go
applicationKey (pod -> ReplicaSet -> Deployment), so this string reaches a
sovereign-admin surface verbatim. Rename the object — "tenant" is
"organization". A display alias in the console is not a fix; kubectl would
still answer the banned name.
MSG
  exit 1
fi

echo "OK: scanned ${#FILES[@]} chart template files — no banned entity-noun used as a Kubernetes object name"
