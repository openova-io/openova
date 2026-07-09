#!/usr/bin/env bash
# tests/integration/services-build-rewrite.sh
#
# Regression test for the deploy step of
# `.github/workflows/services-build.yaml`.
#
# History
# -------
# * #953 (2026-05-05): the deploy step's image-rewrite loop only matched
#   hardcoded `image: ghcr.io/openova-io/openova/services-<svc>:<sha>`
#   lines. Most org-services templates are written in the templated form
#   `image: "{{ ... }}/services-<svc>:{{ .Values.images.orgTag }}"` — the
#   sed regex never matched those, so each services-build run bumped only
#   the one remaining hardcoded template (auth.yaml) while reporting a
#   successful deploy of all services. Caught live on otech113:
#   services-catalog Pod still pulled `:95a06f5` after PR #951's commit
#   claimed it rolled out. Fix: bump `images.orgTag` in values.yaml (the
#   single source of truth for every templated service).
# * #3383 (2026-06-17): the `sme-services` directory + `smeTag` field were
#   renamed to `org-services` + `orgTag` in the SME→Organization refactor.
# * #4885 (2026-07-09): auth.yaml — the last hardcoded holdout — was
#   flipped to the templated `{{ .Values.images.orgTag }}` form so the
#   sovereignty-cutover step-07 patch pivots it through
#   global.imageRegistry. ALL org-services templates are now templated;
#   the workflow's literal-image sed loop is a documented no-op retained
#   for defensive back-compat.
#
# This test reproduces the exact rewrite logic the workflow runs locally
# (no GitHub Actions runner needed) on a snapshot of
# products/catalyst/chart/{templates/org-services/*.yaml,values.yaml} at
# HEAD, then asserts:
#
#   1. Every org-services template is in the templated `orgTag` form
#      (including auth.yaml — the #4885 flip).
#   2. The bump moves `images.orgTag` in values.yaml to the new SHA, so
#      the chart renders every templated `image:` line at the new SHA on
#      the next release.
#   3. Unrelated tag fields (catalystApi.tag, console.tag, marketplaceApi
#      .tag, marketplaceTag) are left untouched.
#   4. A second invocation with a DIFFERENT SHA correctly re-bumps
#      orgTag to the second SHA (idempotency / no first-SHA locking).
#   5. (helm available) the rendered org-services Deployments — auth
#      included — carry the new SHA.

set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
deploy_dir="${repo_root}/products/catalyst/chart/templates/org-services"
values_yaml="${repo_root}/products/catalyst/chart/values.yaml"
image_base="ghcr.io/openova-io/openova/services"

# Sandbox copies — never mutate the live tree.
sandbox=$(mktemp -d)
trap 'rm -rf "$sandbox"' EXIT
mkdir -p "${sandbox}/templates/org-services"
cp -r "${deploy_dir}"/*.yaml "${sandbox}/templates/org-services/"
cp "${values_yaml}" "${sandbox}/values.yaml"

sandbox_deploy_dir="${sandbox}/templates/org-services"
sandbox_values="${sandbox}/values.yaml"

# Apply the workflow's rewrite logic (a faithful copy of the bash block
# in services-build.yaml's deploy step, parameterised on the sandbox
# paths). If the workflow body changes shape, this test must change in
# lockstep. The literal-image loop is a no-op today (every template is
# templated) but is kept here to mirror the workflow exactly.
rewrite() {
  local SHA="$1"
  for svc in auth catalog gateway tenant domain billing provisioning notification; do
    local FILE="${sandbox_deploy_dir}/${svc}.yaml"
    if [ -f "$FILE" ]; then
      sed -i "s|image: ${image_base}-${svc}:.*|image: ${image_base}-${svc}:${SHA}|" "$FILE"
    fi
  done
  if ! grep -Eq '^  orgTag: "[A-Za-z0-9_.-]*"$' "${sandbox_values}"; then
    echo "FAIL: orgTag line not found in values.yaml"
    return 1
  fi
  sed -i "s|^  orgTag: \"[A-Za-z0-9_.-]*\"$|  orgTag: \"${SHA}\"|" "${sandbox_values}"
}

# Capture pre-state so we can assert which fields changed.
pre_catalystApi_tag=$(awk '/^  catalystApi:/{f=1; next} f && /tag:/{print; exit}' "${sandbox_values}")
pre_console_tag=$(awk '/^  console:/{f=1; next} f && /tag:/{print; exit}' "${sandbox_values}")
pre_marketplaceApi_tag=$(awk '/^  marketplaceApi:/{f=1; next} f && /tag:/{print; exit}' "${sandbox_values}")
pre_marketplaceTag=$(grep -E '^  marketplaceTag:' "${sandbox_values}" || true)

# All org-services templates (auth included, post-#4885) must be in the
# templated orgTag form BEFORE any rewrite runs.
# svc -> template file (organization.yaml carries the services-tenant image).
declare -A svc_file=(
  [auth]=auth.yaml [catalog]=catalog.yaml [gateway]=gateway.yaml
  [tenant]=organization.yaml [domain]=domain.yaml [billing]=billing.yaml
  [provisioning]=provisioning.yaml [notification]=notification.yaml
)
for svc in "${!svc_file[@]}"; do
  f="${sandbox_deploy_dir}/${svc_file[$svc]}"
  if ! grep -q '{{ .Values.images.orgTag }}' "$f"; then
    echo "FAIL: ${svc_file[$svc]} is not in the templated orgTag form"
    exit 1
  fi
done
# auth.yaml specifically — the #4885 flip: templated services-auth ref.
if ! grep -q 'services-auth:{{ .Values.images.orgTag }}' "${sandbox_deploy_dir}/auth.yaml"; then
  echo "FAIL: auth.yaml lost its #4885 templated services-auth reference"
  exit 1
fi

# ── Case 1: bump to NEW_SHA_1 ─────────────────────────────────────────
NEW_SHA_1="abc1234"
echo "[services-build-rewrite] Case 1: bump to ${NEW_SHA_1}"
rewrite "${NEW_SHA_1}"

# values.yaml orgTag bumped
if ! grep -q "^  orgTag: \"${NEW_SHA_1}\"$" "${sandbox_values}"; then
  echo "FAIL: values.yaml orgTag not bumped to ${NEW_SHA_1}"
  echo "Live orgTag line:"
  grep "^  orgTag:" "${sandbox_values}"
  exit 1
fi

# All 8 templated services still templated (the no-op sed loop must not
# have rewritten any of them to a hardcoded string).
for svc in "${!svc_file[@]}"; do
  f="${sandbox_deploy_dir}/${svc_file[$svc]}"
  if ! grep -q '{{ .Values.images.orgTag }}' "$f"; then
    echo "FAIL: ${svc_file[$svc]} lost its templated orgTag reference after rewrite"
    exit 1
  fi
done

# Untouched tag fields preserved.
post_catalystApi_tag=$(awk '/^  catalystApi:/{f=1; next} f && /tag:/{print; exit}' "${sandbox_values}")
post_console_tag=$(awk '/^  console:/{f=1; next} f && /tag:/{print; exit}' "${sandbox_values}")
post_marketplaceApi_tag=$(awk '/^  marketplaceApi:/{f=1; next} f && /tag:/{print; exit}' "${sandbox_values}")
post_marketplaceTag=$(grep -E '^  marketplaceTag:' "${sandbox_values}" || true)
if [ "${pre_catalystApi_tag}" != "${post_catalystApi_tag}" ]; then
  echo "FAIL: catalystApi.tag mutated unexpectedly"
  exit 1
fi
if [ "${pre_console_tag}" != "${post_console_tag}" ]; then
  echo "FAIL: console.tag mutated unexpectedly"
  exit 1
fi
if [ "${pre_marketplaceApi_tag}" != "${post_marketplaceApi_tag}" ]; then
  echo "FAIL: marketplaceApi.tag mutated unexpectedly"
  exit 1
fi
# marketplaceTag is owned by the SEPARATE marketplace-build workflow —
# services-build must never touch it.
if [ "${pre_marketplaceTag}" != "${post_marketplaceTag}" ]; then
  echo "FAIL: marketplaceTag mutated by services-build (owned by marketplace-build)"
  exit 1
fi
echo "[services-build-rewrite] Case 1: PASS"

# ── Case 2: re-bump to NEW_SHA_2 (idempotency / second invocation) ────
NEW_SHA_2="def5678"
echo "[services-build-rewrite] Case 2: re-bump to ${NEW_SHA_2}"
rewrite "${NEW_SHA_2}"

if ! grep -q "^  orgTag: \"${NEW_SHA_2}\"$" "${sandbox_values}"; then
  echo "FAIL: values.yaml orgTag not re-bumped to ${NEW_SHA_2}"
  exit 1
fi
if grep -q "^  orgTag: \"${NEW_SHA_1}\"$" "${sandbox_values}"; then
  echo "FAIL: values.yaml orgTag still references first SHA"
  exit 1
fi
echo "[services-build-rewrite] Case 2: PASS"

# ── Case 3: helm-render assertion — org-services Pods carry the new SHA
# after the bump. Skipped when helm is unavailable.
# ──────────────────────────────────────────────────────────────────────
helm="${HELM_BIN:-helm}"
if ! command -v "$helm" >/dev/null 2>&1; then
  echo "[services-build-rewrite] Case 3: SKIP (helm not on PATH)"
  echo "[services-build-rewrite] All runnable cases PASS"
  exit 0
fi
echo "[services-build-rewrite] Case 3: helm-render — Pods carry new orgTag"
chart_root="${repo_root}/products/catalyst/chart"
sandbox_chart="${sandbox}/catalyst-chart"
cp -r "${chart_root}" "${sandbox_chart}"
cp "${sandbox_values}" "${sandbox_chart}/values.yaml"
cp -r "${sandbox_deploy_dir}"/*.yaml "${sandbox_chart}/templates/org-services/"
"$helm" dependency build "${sandbox_chart}" >/dev/null 2>&1 || true

render_values=$(mktemp)
cat > "${render_values}" <<'EOF'
global:
  sovereignFQDN: test.example.test
ingress:
  marketplace:
    enabled: true
EOF
out=$("$helm" template smoke "${sandbox_chart}" -f "${render_values}" 2>&1 || true)
rm -f "${render_values}"

fail=0
# Every org-services image (auth included) must reference NEW_SHA_2.
for pair in auth:services-auth catalog:services-catalog gateway:services-gateway \
            tenant:services-tenant domain:services-domain billing:services-billing \
            provisioning:services-provisioning notification:services-notification; do
  img="${pair#*:}"
  # here-string (not a pipe): under `set -o pipefail`, `echo "$out" | grep -q`
  # makes grep exit early on match → echo gets SIGPIPE (141) → pipefail
  # reports 141 → the `if !` inverts a real match into a false FAIL.
  if ! grep -q "/${img}:${NEW_SHA_2}" <<<"$out"; then
    echo "FAIL: helm-rendered ${img} image does not carry SHA ${NEW_SHA_2}"
    grep -E "image:.*/${img}:" <<<"$out" | head -3 || true
    fail=1
  fi
done

if [ "$fail" -eq 1 ]; then
  exit 1
fi
echo "[services-build-rewrite] Case 3: PASS"

echo "[services-build-rewrite] All cases PASS"
