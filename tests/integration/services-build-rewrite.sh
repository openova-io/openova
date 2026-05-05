#!/usr/bin/env bash
# tests/integration/services-build-rewrite.sh
#
# Issue #953 regression test for `.github/workflows/services-build.yaml`.
#
# Why this test exists
# --------------------
# 2026-05-05: the deploy step's image-rewrite loop only matched
# hardcoded `image: ghcr.io/openova-io/openova/services-<svc>:<sha>`
# lines. 7 of 8 sme-services templates are written in the templated
# form `image: "{{ ... }}/services-<svc>:{{ .Values.images.smeTag }}"`
# — the sed regex never matched those, so each services-build run
# bumped only `auth.yaml` while reporting a successful deploy of all
# eight services. Caught live on otech113: services-catalog Pod still
# pulled `:95a06f5` after PR #951's commit `68927688` claimed it had
# rolled out, so #941's catalog migration fix never reached the live
# Pod despite the merge + chart bump + commit chain looking healthy.
#
# This test reproduces the exact rewrite logic the workflow runs
# locally (no GitHub Actions runner needed) on a snapshot of
# products/catalyst/chart/{templates/sme-services/*.yaml,values.yaml}
# at HEAD, then asserts:
#
#   1. The hardcoded `auth.yaml` image gets bumped (back-compat is
#      preserved).
#   2. The 7 templated services' VALUES surface (`images.smeTag` in
#      values.yaml) gets bumped to the new SHA — i.e. the chart will
#      render every templated `image:` line at the new SHA on the
#      next release.
#   3. The other unrelated tag fields in values.yaml
#      (`catalystApi.tag`, `console.tag`, etc.) are left untouched.
#   4. A second invocation with a DIFFERENT SHA correctly re-bumps
#      everything to the second SHA (idempotency / no-locking on the
#      first SHA).

set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
deploy_dir="${repo_root}/products/catalyst/chart/templates/sme-services"
values_yaml="${repo_root}/products/catalyst/chart/values.yaml"
image_base="ghcr.io/openova-io/openova/services"

# Sandbox copies — never mutate the live tree.
sandbox=$(mktemp -d)
trap 'rm -rf "$sandbox"' EXIT
mkdir -p "${sandbox}/templates/sme-services"
cp -r "${deploy_dir}"/*.yaml "${sandbox}/templates/sme-services/"
cp "${values_yaml}" "${sandbox}/values.yaml"

sandbox_deploy_dir="${sandbox}/templates/sme-services"
sandbox_values="${sandbox}/values.yaml"

# Apply the workflow's rewrite logic (verbatim copy of the bash
# block that lives in services-build.yaml's `Update deployment
# manifests` step, parameterised on the sandbox paths). If the
# workflow body changes shape, this test must change in lockstep.
rewrite() {
  local SHA="$1"
  for svc in auth catalog gateway tenant domain billing provisioning notification; do
    local FILE="${sandbox_deploy_dir}/${svc}.yaml"
    if [ -f "$FILE" ]; then
      sed -i "s|image: ${image_base}-${svc}:.*|image: ${image_base}-${svc}:${SHA}|" "$FILE"
    fi
  done
  if ! grep -Eq '^  smeTag: "[A-Za-z0-9_.-]*"$' "${sandbox_values}"; then
    echo "FAIL: smeTag line not found in values.yaml"
    return 1
  fi
  sed -i "s|^  smeTag: \"[A-Za-z0-9_.-]*\"$|  smeTag: \"${SHA}\"|" "${sandbox_values}"
}

# Capture pre-state so we can assert which fields changed.
pre_catalystApi_tag=$(awk '/^  catalystApi:/{f=1; next} f && /tag:/{print; exit}' "${sandbox_values}")
pre_console_tag=$(awk '/^  console:/{f=1; next} f && /tag:/{print; exit}' "${sandbox_values}")
pre_marketplace_tag=$(awk '/^  marketplaceApi:/{f=1; next} f && /tag:/{print; exit}' "${sandbox_values}")

# ── Case 1: bump to NEW_SHA_1 ─────────────────────────────────────────
NEW_SHA_1="abc1234"
echo "[services-build-rewrite] Case 1: bump to ${NEW_SHA_1}"
rewrite "${NEW_SHA_1}"

# auth.yaml hardcoded line bumped
if ! grep -q "image: ${image_base}-auth:${NEW_SHA_1}" "${sandbox_deploy_dir}/auth.yaml"; then
  echo "FAIL: auth.yaml hardcoded image not bumped to ${NEW_SHA_1}"
  exit 1
fi

# values.yaml smeTag bumped
if ! grep -q "^  smeTag: \"${NEW_SHA_1}\"$" "${sandbox_values}"; then
  echo "FAIL: values.yaml smeTag not bumped to ${NEW_SHA_1}"
  echo "Live smeTag line:"
  grep "^  smeTag:" "${sandbox_values}"
  exit 1
fi

# 7 templated services — verify their helm-rendered image tag now
# resolves to the new SHA. Templated form is:
#   image: "{{ ... }}/services-<svc>:{{ .Values.images.smeTag }}"
# We do not rewrite those .yaml files directly; we rewrote the
# values.yaml smeTag and that's what the chart consumes at render
# time. Assert the templated-form lines are STILL templated (we did
# not accidentally rewrite them to a hardcoded string) AND assert
# the values.yaml change would feed them.
for svc in catalog gateway tenant domain billing provisioning notification; do
  if ! grep -q '{{ .Values.images.smeTag }}' "${sandbox_deploy_dir}/${svc}.yaml"; then
    echo "FAIL: ${svc}.yaml lost its templated smeTag reference"
    exit 1
  fi
done

# Untouched tag fields preserved
post_catalystApi_tag=$(awk '/^  catalystApi:/{f=1; next} f && /tag:/{print; exit}' "${sandbox_values}")
post_console_tag=$(awk '/^  console:/{f=1; next} f && /tag:/{print; exit}' "${sandbox_values}")
post_marketplace_tag=$(awk '/^  marketplaceApi:/{f=1; next} f && /tag:/{print; exit}' "${sandbox_values}")
if [ "${pre_catalystApi_tag}" != "${post_catalystApi_tag}" ]; then
  echo "FAIL: catalystApi.tag mutated unexpectedly"
  echo "  pre:  ${pre_catalystApi_tag}"
  echo "  post: ${post_catalystApi_tag}"
  exit 1
fi
if [ "${pre_console_tag}" != "${post_console_tag}" ]; then
  echo "FAIL: console.tag mutated unexpectedly"
  exit 1
fi
if [ "${pre_marketplace_tag}" != "${post_marketplace_tag}" ]; then
  echo "FAIL: marketplaceApi.tag mutated unexpectedly"
  exit 1
fi
echo "[services-build-rewrite] Case 1: PASS"

# ── Case 2: re-bump to NEW_SHA_2 (idempotency / second invocation) ────
NEW_SHA_2="def5678"
echo "[services-build-rewrite] Case 2: re-bump to ${NEW_SHA_2}"
rewrite "${NEW_SHA_2}"

if ! grep -q "image: ${image_base}-auth:${NEW_SHA_2}" "${sandbox_deploy_dir}/auth.yaml"; then
  echo "FAIL: auth.yaml hardcoded image not re-bumped to ${NEW_SHA_2}"
  exit 1
fi
if grep -q "image: ${image_base}-auth:${NEW_SHA_1}" "${sandbox_deploy_dir}/auth.yaml"; then
  echo "FAIL: auth.yaml still references first SHA"
  exit 1
fi
if ! grep -q "^  smeTag: \"${NEW_SHA_2}\"$" "${sandbox_values}"; then
  echo "FAIL: values.yaml smeTag not re-bumped to ${NEW_SHA_2}"
  exit 1
fi
if grep -q "^  smeTag: \"${NEW_SHA_1}\"$" "${sandbox_values}"; then
  echo "FAIL: values.yaml smeTag still references first SHA"
  exit 1
fi
echo "[services-build-rewrite] Case 2: PASS"

# ── Case 3: helm-render assertion — actual SME-services Pods carry the
# new SHA after the bump. Renders the catalyst chart with the sandbox
# values.yaml + an enableSme override (the chart's existing dispatch
# knob) and greps the resulting Deployment manifests for the SHA.
# ──────────────────────────────────────────────────────────────────────
echo "[services-build-rewrite] Case 3: helm-render — Pods carry new smeTag"
helm="${HELM_BIN:-helm}"
chart_root="${repo_root}/products/catalyst/chart"

# Build chart deps once into a sandbox so we don't pollute the live
# tree's charts/ directory.
sandbox_chart="${sandbox}/catalyst-chart"
cp -r "${chart_root}" "${sandbox_chart}"
cp "${sandbox_values}" "${sandbox_chart}/values.yaml"
# Carry over the rewritten sme-services templates (auth.yaml's
# hardcoded image line is the one our sed loop touched in Cases 1/2;
# the rest are unchanged but copying the directory wholesale is
# cheaper than per-file conditional logic).
cp -r "${sandbox_deploy_dir}"/*.yaml "${sandbox_chart}/templates/sme-services/"
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

# Drop into a per-svc check: every templated SME service's image must
# now reference NEW_SHA_2.
fail=0
for svc in catalog gateway tenant domain billing provisioning notification; do
  if ! echo "$out" | grep -q "/services-${svc}:${NEW_SHA_2}"; then
    echo "FAIL: helm-rendered ${svc} image does not carry SHA ${NEW_SHA_2}"
    echo "  matched:"
    echo "$out" | grep -E "image:.*/services-${svc}:" | head -3 || true
    fail=1
  fi
done

# auth.yaml is hardcoded — its rendered image must also carry the new SHA.
if ! echo "$out" | grep -q "/services-auth:${NEW_SHA_2}"; then
  echo "FAIL: helm-rendered auth image does not carry SHA ${NEW_SHA_2}"
  fail=1
fi

if [ "$fail" -eq 1 ]; then
  exit 1
fi
echo "[services-build-rewrite] Case 3: PASS"

echo "[services-build-rewrite] All cases PASS"
