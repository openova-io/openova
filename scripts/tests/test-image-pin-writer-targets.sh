#!/usr/bin/env bash
# test-image-pin-writer-targets.sh — non-vacuity proof for #5451
#
# Drives scripts/check-image-pin-writer-targets.sh against the ACTUAL shapes
# involved in the outage, not invented ones:
#
#   H1  the real pre-fix console-build.yaml deploy step (sed against the
#       Helm-templated org-services/console.yaml) with the real frozen pin
#       images.console.tag = "3c2f7e4"                              -> RED
#   H2  the same fixture with the shipped fix — deploy-bump pointed at
#       values.yaml, where the pin actually lives                   -> GREEN
#   H3  CONTROL. marketplace-build.yaml shares every suspect property with
#       the broken three: same chart, same org-services directory, same
#       Helm-templated `image:` line, same deploy-bump composite action.
#       The ONLY difference is that its writer targets values.yaml. It must
#       stay GREEN, or the guard is just failing everything in that tree
#       and proves nothing about console.                           -> GREEN
#   H4  pin key renamed in values.yaml — the pin becomes unreadable. Absent
#       evidence must be reported as failure, never as a pass.      -> RED
#   H5  a workflow that commits nothing (no deploy-bump `paths:`, no
#       `git add`). A writer with no write target cannot move a pin. -> RED
#   H6  services-build.yaml's hand-rolled `git add products/` publisher,
#       which does not use deploy-bump at all. Proves the second extraction
#       mode is load-bearing rather than decorative.                -> GREEN
#   H7  an empty coverage map. A guard with nothing to check must refuse,
#       not print PASS.                                             -> RED
#
# Hermetic: no network, no token, no live cluster.
#
# Usage: bash scripts/tests/test-image-pin-writer-targets.sh

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/../.." && pwd)"
GUARD="${REPO_ROOT}/scripts/check-image-pin-writer-targets.sh"

if [ ! -f "${GUARD}" ]; then
  echo "FAIL: guard not found at ${GUARD}" >&2
  exit 1
fi

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

FAILED=0
fail() { echo "FAIL: $*" >&2; FAILED=1; }
pass() { echo "PASS: $*"; }

# ---------------------------------------------------------------------------
# Fixture repo. values.yaml carries the real frozen console pin (3c2f7e4,
# 2026-04-28) and the real live marketplace pin (7d0b73e), and the manifests
# carry the real post-#580 Helm-templated `image:` lines.
# ---------------------------------------------------------------------------
build_fixture() {
  local root="$1"
  rm -rf "${root}"
  mkdir -p "${root}/.github/workflows" \
           "${root}/products/catalyst/chart/templates/org-services"

  cat > "${root}/products/catalyst/chart/values.yaml" <<'YAML'
images:
  registry: "ghcr.io"
  organization: "openova-io/openova"
  console:
    tag: "3c2f7e4"
  marketplaceTag: "7d0b73e"
YAML

  # Real post-#580 shape: the SHA is a Helm expression, not a literal.
  local tmpl='          image: "{{ if .Values.global.imageRegistry }}{{ .Values.global.imageRegistry }}{{ else }}{{ .Values.images.registry }}{{ end }}/{{ .Values.images.organization }}'
  printf '%s/console:{{ .Values.images.console.tag }}"\n' "${tmpl}" \
    > "${root}/products/catalyst/chart/templates/org-services/console.yaml"
  printf '%s/marketplace:{{ .Values.images.marketplaceTag }}"\n' "${tmpl}" \
    > "${root}/products/catalyst/chart/templates/org-services/marketplace.yaml"
}

# The pre-fix console-build.yaml deploy job, verbatim in shape.
write_console_workflow_broken() {
  cat > "$1/.github/workflows/console-build.yaml" <<'YAML'
name: Build & Deploy Catalyst Console
on:
  push:
    paths: ['core/console/**']
    branches: [main]
env:
  IMAGE: ghcr.io/openova-io/openova/console
jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - name: Update deployment manifest
        run: |
          SHA=$(echo $GITHUB_SHA | head -c 7)
          FILE="products/catalyst/chart/templates/org-services/console.yaml"
          if [ -f "$FILE" ]; then
            sed -i "s|image: ${IMAGE}:.*|image: ${IMAGE}:${SHA}|" "$FILE"
          fi
      - name: Commit and push
        uses: ./.github/actions/deploy-bump
        with:
          paths: products/catalyst/chart/templates/org-services/console.yaml
          commit-message: "deploy: update Catalyst console image"
YAML
}

write_console_workflow_fixed() {
  cat > "$1/.github/workflows/console-build.yaml" <<'YAML'
name: Build & Deploy Catalyst Console
on:
  push:
    paths: ['core/console/**']
    branches: [main]
env:
  IMAGE: ghcr.io/openova-io/openova/console
jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - name: Bump images.console.tag in values.yaml
        run: |
          bash scripts/bump-values-image-tag.sh \
            products/catalyst/chart/values.yaml console "${SHA_SHORT}"
      - name: Commit and push
        uses: ./.github/actions/deploy-bump
        with:
          paths: products/catalyst/chart/values.yaml
          commit-message: "deploy: update Catalyst console image"
YAML
}

# The CONTROL: marketplace-build.yaml as it has looked since #4885.
write_marketplace_workflow() {
  cat > "$1/.github/workflows/marketplace-build.yaml" <<'YAML'
name: Build & Deploy Catalyst Marketplace
on:
  push:
    paths: ['core/marketplace/**']
    branches: [main]
env:
  IMAGE: ghcr.io/openova-io/openova/marketplace
jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - name: Bump marketplace image tag in values.yaml
        run: |
          sed -i "s|^  marketplaceTag: \"[A-Za-z0-9_.-]*\"$|  marketplaceTag: \"${SHA}\"|" \
            products/catalyst/chart/values.yaml
      - name: Commit and push
        uses: ./.github/actions/deploy-bump
        with:
          paths: products/catalyst/chart/values.yaml
          commit-message: "deploy: update Catalyst marketplace image"
YAML
}

run_guard() {
  local root="$1" map="$2"
  ( cd "${root}" && PIN_MAP="${map}" bash "${GUARD}" 2>&1 )
}

expect() {
  local want="$1" root="$2" map="$3" label="$4"
  local out rc
  out="$(run_guard "${root}" "${map}")"
  rc=$?
  if [ "${want}" = "red" ] && [ "${rc}" -eq 0 ]; then
    fail "${label} — expected exit!=0, got 0. Output:"; echo "${out}" >&2; return
  fi
  if [ "${want}" = "green" ] && [ "${rc}" -ne 0 ]; then
    fail "${label} — expected exit 0, got ${rc}. Output:"; echo "${out}" >&2; return
  fi
  pass "${label} (exit ${rc})"
}

# ---------------------------------------------------------------------------
# H1 — the real defect
# ---------------------------------------------------------------------------
FX="${WORK}/h1"
build_fixture "${FX}"
write_console_workflow_broken "${FX}"
expect red "${FX}" "console|nested|console-build.yaml" \
  "H1 pre-fix console-build.yaml (sed on a templated manifest) is RED"

# The message must name the real cause, not just say 'failed'. Captured into a
# variable rather than piped: under `set -o pipefail` a pipeline inherits the
# guard's non-zero exit and would report a false negative here.
h1_out="$(run_guard "${FX}" "console|nested|console-build.yaml")"
if printf '%s' "${h1_out}" | grep -q 'silent no-op'; then
  pass "H1 failure message names the silent no-op"
else
  fail "H1 — failure message does not identify the no-op write"
fi

# ---------------------------------------------------------------------------
# H2 — the shipped fix
# ---------------------------------------------------------------------------
FX="${WORK}/h2"
build_fixture "${FX}"
write_console_workflow_fixed "${FX}"
expect green "${FX}" "console|nested|console-build.yaml" \
  "H2 fixed console-build.yaml (deploy-bump -> values.yaml) is GREEN"

# ---------------------------------------------------------------------------
# H3 — CONTROL sharing the suspect property
# ---------------------------------------------------------------------------
FX="${WORK}/h3"
build_fixture "${FX}"
write_console_workflow_broken "${FX}"
write_marketplace_workflow "${FX}"
expect green "${FX}" "marketplaceTag|flat|marketplace-build.yaml" \
  "H3 CONTROL marketplace-build.yaml — same chart, same org-services dir, same templated image line, same deploy-bump — stays GREEN"

# and in the same fixture the broken one is still red, so H3 is not green by accident
expect red "${FX}" "$(printf 'console|nested|console-build.yaml\nmarketplaceTag|flat|marketplace-build.yaml')" \
  "H3b both together — the control does not mask the defect"

# ---------------------------------------------------------------------------
# H4 — unreadable pin must fail, not pass
# ---------------------------------------------------------------------------
FX="${WORK}/h4"
build_fixture "${FX}"
write_console_workflow_fixed "${FX}"
cat > "${FX}/products/catalyst/chart/values.yaml" <<'YAML'
images:
  registry: "ghcr.io"
  consoleImage:
    tag: "3c2f7e4"
YAML
expect red "${FX}" "console|nested|console-build.yaml" \
  "H4 renamed pin key — unreadable pin is RED, not a pass"

# ---------------------------------------------------------------------------
# H5 — a workflow that writes nothing
# ---------------------------------------------------------------------------
FX="${WORK}/h5"
build_fixture "${FX}"
cat > "${FX}/.github/workflows/console-build.yaml" <<'YAML'
name: Build Catalyst Console
on:
  push:
    paths: ['core/console/**']
    branches: [main]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
YAML
expect red "${FX}" "console|nested|console-build.yaml" \
  "H5 workflow with no write target is RED"

# ---------------------------------------------------------------------------
# H6 — hand-rolled `git add` publisher (services-build.yaml's shape)
# ---------------------------------------------------------------------------
FX="${WORK}/h6"
build_fixture "${FX}"
cat > "${FX}/products/catalyst/chart/values.yaml" <<'YAML'
images:
  registry: "ghcr.io"
  orgTag: "d1607c4"
YAML
cat > "${FX}/.github/workflows/services-build.yaml" <<'YAML'
name: Build & Deploy Organization services
on:
  push:
    paths: ['core/services/**']
    branches: [main]
jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - name: Rewrite and push
        run: |
          sed -i "s|^  orgTag: \"[A-Za-z0-9_.-]*\"$|  orgTag: \"${SHA}\"|" \
            products/catalyst/chart/values.yaml
          git add products/
          git commit -m "deploy: update org service images"
YAML
expect green "${FX}" "orgTag|flat|services-build.yaml" \
  "H6 hand-rolled 'git add products/' publisher is GREEN"

# ---------------------------------------------------------------------------
# H7 — an empty map must refuse
# ---------------------------------------------------------------------------
FX="${WORK}/h7"
build_fixture "${FX}"
write_console_workflow_fixed "${FX}"
out="$( cd "${FX}" && PIN_MAP="$(printf '\n')" bash "${GUARD}" 2>&1 )"
rc=$?
if [ "${rc}" -eq 0 ]; then
  fail "H7 empty coverage map exited 0 — the guard can be silenced. Output:"; echo "${out}" >&2
else
  pass "H7 empty coverage map is RED (exit ${rc})"
fi

# ===========================================================================
# Part B — scripts/bump-values-image-tag.sh, the writer the fixed workflows
# call. The guard above proves the writer is POINTED at the pin; these prove
# the writer cannot report success without having MOVED it, which is the
# property whose absence caused the outage.
# ===========================================================================
BUMP="${REPO_ROOT}/scripts/bump-values-image-tag.sh"

if [ ! -f "${BUMP}" ]; then
  fail "B0 — scripts/bump-values-image-tag.sh not found"
else
  # B1 — happy path: the field moves and the file re-reads as the new SHA.
  V="${WORK}/b1-values.yaml"
  cat > "${V}" <<'YAML'
images:
  registry: "ghcr.io"
  console:
    tag: "3c2f7e4"
  admin:
    tag: "3c2f7e4"
YAML
  if bash "${BUMP}" "${V}" console 1a2b3c4 >/dev/null 2>&1 \
     && grep -q '^    tag: "1a2b3c4"$' "${V}" \
     && grep -c '^    tag: "3c2f7e4"$' "${V}" | grep -qx 1; then
    pass "B1 bump moves images.console.tag and leaves images.admin.tag alone"
  else
    fail "B1 — bump did not move exactly one field. File now:"; cat "${V}" >&2
  fi

  # B2 — the real 2026-05-02 shape: the pin no longer lives where the writer
  # looks. The writer must go RED, not exit 0 having written nothing.
  V="${WORK}/b2-values.yaml"
  cat > "${V}" <<'YAML'
images:
  registry: "ghcr.io"
  consoleImage:
    tag: "3c2f7e4"
YAML
  before="$(cat "${V}")"
  if bash "${BUMP}" "${V}" console 1a2b3c4 >/dev/null 2>&1; then
    fail "B2 — bump exited 0 against a reshaped field (this is the #5451 defect)"
  elif [ "$(cat "${V}")" != "${before}" ]; then
    fail "B2 — bump failed but still mutated the file"
  else
    pass "B2 reshaped/renamed pin field is RED and the file is untouched"
  fi

  # B3 — missing values.yaml is a failure, not a silent skip. The original
  # workflows wrapped their edit in `if [ -f "$FILE" ]`, which is precisely
  # how a path rename turned into three months of green no-ops.
  if bash "${BUMP}" "${WORK}/does-not-exist.yaml" console 1a2b3c4 >/dev/null 2>&1; then
    fail "B3 — bump exited 0 for a missing values.yaml"
  else
    pass "B3 missing values.yaml is RED, never a skip"
  fi

  # B4 — idempotence: re-running with the SHA already in place is a legitimate
  # success (a retry after a push race), not a failure.
  V="${WORK}/b4-values.yaml"
  cat > "${V}" <<'YAML'
images:
  console:
    tag: "1a2b3c4"
YAML
  if bash "${BUMP}" "${V}" console 1a2b3c4 >/dev/null 2>&1; then
    pass "B4 re-running with the SHA already pinned is GREEN (retry-safe)"
  else
    fail "B4 — idempotent re-run reported failure"
  fi
fi

echo
if [ "${FAILED}" -ne 0 ]; then
  echo "test-image-pin-writer-targets.sh: FAILED"
  exit 1
fi
echo "test-image-pin-writer-targets.sh: all cases passed"
