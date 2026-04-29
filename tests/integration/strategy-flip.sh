#!/usr/bin/env bash
# tests/integration/strategy-flip.sh
#
# Integration test runner for the RollingUpdate -> Recreate strategy
# flip regression (see tests/integration/strategy-flip.yaml).
#
# Why this test exists
# --------------------
# 2026-04-29: the `catalyst` Flux Kustomization on contabo-mkt got
# stuck Ready=False with:
#
#   Deployment.apps "catalyst-api" is invalid:
#     spec.strategy.rollingUpdate: Forbidden:
#       may not be specified when strategy `type` is 'Recreate'
#
# Root cause: the chart's `api-deployment.yaml` declared
# `strategy.type: Recreate` (correct — the deployments PVC is RWO and
# rolling-update would Multi-Attach-Error). But the LIVE Deployment had
# been previously created via `kubectl apply` with the default
# `RollingUpdate` strategy. Server-Side Apply (Flux's default path)
# does not remove fields owned by other field managers — so the
# residual `rollingUpdate.maxSurge=25%` and `maxUnavailable=25%` keys
# remained on the live object after Flux flipped `type` to `Recreate`.
# That post-merge state is what the API validator forbids.
#
# This test:
#
#   1. Stages a Deployment in the bad pre-state (RollingUpdate +
#      maxSurge=25%/maxUnavailable=25%) — exact shape that triggered
#      the contabo-mkt outage.
#   2. Asserts the chart manifest applies via the Client-Side Apply
#      path (`kubectl apply --dry-run=server`), which is the gate the
#      original bug report named.
#   3. Asserts the Server-Side Apply path REPRODUCES the failure mode
#      with the documented error string — proving the regression is
#      real and that the chart's recovery layer is necessary.
#   4. Asserts the chart manifest carries the
#      `kustomize.toolkit.fluxcd.io/force: enabled` annotation —
#      Flux's documented recovery path that delete+recreates the
#      resource on every reconcile when SSA dry-run fails.
#   5. Proves the recovery path itself works: deletes the bad-state
#      Deployment and asserts the chart manifest creates a fresh one
#      cleanly (this is what Flux does internally when it sees the
#      force annotation + a failing SSA dry-run).
#   6. Proves the chart manifest is valid for FRESH INSTALLS: applies
#      it to an empty namespace and asserts it creates the Deployment
#      with no errors. Catches the `$patch: replace`-shaped mistakes
#      that break new clusters.
#
# Exit 0 = pass. Any other exit = fail; the failing assertion is
# logged to stderr.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
FIXTURE="${REPO_ROOT}/tests/integration/strategy-flip.yaml"
TARGET_MANIFEST="${REPO_ROOT}/products/catalyst/chart/templates/api-deployment.yaml"
NAMESPACE="strategy-flip-test"
FRESH_NAMESPACE="strategy-flip-fresh"

log() {
  printf '[strategy-flip] %s\n' "$*" >&2
}

cleanup() {
  log "tearing down namespaces ${NAMESPACE} and ${FRESH_NAMESPACE}"
  kubectl delete namespace "${NAMESPACE}" --ignore-not-found=true --wait=false >/dev/null 2>&1 || true
  kubectl delete namespace "${FRESH_NAMESPACE}" --ignore-not-found=true --wait=false >/dev/null 2>&1 || true
}
trap cleanup EXIT

if [ ! -f "${FIXTURE}" ]; then
  log "ERROR: fixture missing at ${FIXTURE}"
  exit 2
fi
if [ ! -f "${TARGET_MANIFEST}" ]; then
  log "ERROR: chart manifest missing at ${TARGET_MANIFEST}"
  exit 2
fi

# -----------------------------------------------------------------
# step 1 — stage the bad pre-state
# -----------------------------------------------------------------
log "step 1/6 — applying bad-state fixture (RollingUpdate + maxSurge=25%)"
kubectl apply -f "${FIXTURE}" >/dev/null

# Sanity-check the fixture landed with the bad strategy. If the cluster
# silently mutated it (admission webhook rewriting, ratchet enforcement)
# the test would pass for the wrong reason.
ACTUAL_TYPE=$(kubectl get deploy -n "${NAMESPACE}" catalyst-api -o jsonpath='{.spec.strategy.type}')
ACTUAL_MAX_SURGE=$(kubectl get deploy -n "${NAMESPACE}" catalyst-api -o jsonpath='{.spec.strategy.rollingUpdate.maxSurge}')
if [ "${ACTUAL_TYPE}" != "RollingUpdate" ] || [ "${ACTUAL_MAX_SURGE}" != "25%" ]; then
  log "ERROR: fixture not in expected bad state (type=${ACTUAL_TYPE} maxSurge=${ACTUAL_MAX_SURGE})"
  exit 3
fi
log "  bad-state confirmed: type=${ACTUAL_TYPE} maxSurge=${ACTUAL_MAX_SURGE}"

# -----------------------------------------------------------------
# step 2 — Client-Side Apply gate (the user's named verification path)
# -----------------------------------------------------------------
log "step 2/6 — applying chart manifest with --dry-run=server (CSA path)"
APPLY_OUT=$(mktemp)
APPLY_ERR=$(mktemp)
set +e
kubectl apply --dry-run=server -n "${NAMESPACE}" -f "${TARGET_MANIFEST}" \
  >"${APPLY_OUT}" 2>"${APPLY_ERR}"
APPLY_RC=$?
set -e

log "  exit=${APPLY_RC}"
log "  stdout: $(tr '\n' ' ' <"${APPLY_OUT}")"
[ -s "${APPLY_ERR}" ] && log "  stderr: $(tr '\n' ' ' <"${APPLY_ERR}")"

EXPECTED_EXIT=$(kubectl get cm -n "${NAMESPACE}" strategy-flip-assertions -o jsonpath='{.data.expected-exit-code}')
EXPECTED_STDOUT=$(kubectl get cm -n "${NAMESPACE}" strategy-flip-assertions -o jsonpath='{.data.expected-stdout-substring}')
FORBIDDEN_ERR=$(kubectl get cm -n "${NAMESPACE}" strategy-flip-assertions -o jsonpath='{.data.forbidden-error-substring}')

if [ "${APPLY_RC}" != "${EXPECTED_EXIT}" ]; then
  log "FAIL — CSA exit code ${APPLY_RC} != expected ${EXPECTED_EXIT}"
  log "       this is the regression: the chart manifest cannot be applied over a"
  log "       Deployment that pre-existed with default RollingUpdate strategy."
  exit 1
fi
if ! grep -q "${EXPECTED_STDOUT}" "${APPLY_OUT}"; then
  log "FAIL — expected substring not found in CSA stdout"
  log "       expected: ${EXPECTED_STDOUT}"
  exit 1
fi
# Search BOTH stdout and stderr for the forbidden error — Kubernetes
# emits validation errors to stderr but some kubectl wrappers fold to
# stdout, so we cover both.
if grep -q "${FORBIDDEN_ERR}" "${APPLY_OUT}" "${APPLY_ERR}" 2>/dev/null; then
  log "FAIL — forbidden error substring present in CSA path: ${FORBIDDEN_ERR}"
  log "       the strategy-flip regression is back."
  exit 1
fi
log "  CSA path passes — kubectl's 3-way merge handles the strategy flip"

# -----------------------------------------------------------------
# step 3 — Server-Side Apply: prove the regression's failure mode
# -----------------------------------------------------------------
log "step 3/6 — reproducing SSA failure mode (kustomize-controller field manager)"

SSA_OUT=$(mktemp)
SSA_ERR=$(mktemp)
set +e
kubectl apply \
  --server-side \
  --field-manager=kustomize-controller \
  --force-conflicts \
  --dry-run=server \
  -n "${NAMESPACE}" \
  -f "${TARGET_MANIFEST}" \
  >"${SSA_OUT}" 2>"${SSA_ERR}"
SSA_RC=$?
set -e
log "  SSA dry-run exit=${SSA_RC}"
[ -s "${SSA_OUT}" ] && log "  SSA stdout: $(tr '\n' ' ' <"${SSA_OUT}")"
[ -s "${SSA_ERR}" ] && log "  SSA stderr: $(tr '\n' ' ' <"${SSA_ERR}")"

# The whole point of this test step: SSA over a kubectl-client-side-apply
# pre-existing object MUST reject with the documented Forbidden error.
# This proves (a) the regression is reproducible on demand, (b) the
# chart's recovery layer (the Flux force annotation, asserted in step 4)
# is necessary in production, and (c) any future K8s API version that
# silently rounds the merge will fail this test loudly.
if [ "${SSA_RC}" = "0" ]; then
  log "FAIL — SSA dry-run unexpectedly succeeded against bad-state fixture"
  log "       this means the regression's failure mode has changed; review"
  log "       docs/CHART-AUTHORING.md §'Strategy flips on existing Deployments'"
  log "       and update the test if the behavior is intentional."
  exit 1
fi
if ! grep -q "spec.strategy.rollingUpdate: Forbidden" "${SSA_OUT}" "${SSA_ERR}" 2>/dev/null; then
  log "FAIL — SSA failure mode is not the documented one"
  log "       expected: 'spec.strategy.rollingUpdate: Forbidden'"
  log "       got: $(tr '\n' ' ' <"${SSA_ERR}")"
  exit 1
fi
log "  SSA regression mode confirmed: API server emits 'spec.strategy.rollingUpdate: Forbidden'"

# -----------------------------------------------------------------
# step 4 — structural: chart carries the durable Flux remediation
# -----------------------------------------------------------------
log "step 4/6 — verifying chart carries kustomize.toolkit.fluxcd.io/force: enabled"

# Match the YAML KEY form (line begins with whitespace + the annotation
# name + colon + value) — not the same string inside a comment. Without
# the anchor, deleting the real annotation while leaving comment
# references intact would silently pass.
if ! grep -E '^[[:space:]]+kustomize\.toolkit\.fluxcd\.io/force:[[:space:]]+enabled[[:space:]]*$' "${TARGET_MANIFEST}" >/dev/null 2>&1; then
  log "FAIL — chart manifest is missing the SSA-layer Flux force annotation"
  log "       expected: kustomize.toolkit.fluxcd.io/force: enabled in metadata.annotations"
  log "       see docs/CHART-AUTHORING.md §'Strategy flips on existing Deployments'"
  exit 1
fi

# Negative-property: the chart manifest must NOT contain inline
# `$patch: replace` AS A YAML FIELD (vs. inside a YAML comment, where
# the doc explains why we do NOT use it). That directive belongs in a
# Kustomize patches block (consumed at build time), not a base resource
# — at base level the API server's strict-decoding rejects it on CREATE
# with `unknown field "spec.strategy.$patch"`, breaking fresh installs.
#
# The grep deliberately requires whitespace-prefixed `$patch:` (so it
# matches the YAML key form `    $patch: replace`) and excludes lines
# that begin with `#` (YAML comments). Encoding the negative-property
# this way keeps the chart's documentation freely allowed to discuss
# the directive without the test triggering on commentary.
if grep -E '^[[:space:]]+\$patch:[[:space:]]+replace[[:space:]]*$' "${TARGET_MANIFEST}" >/dev/null 2>&1; then
  log "FAIL — chart manifest contains inline \$patch: replace as a YAML key"
  log "       this directive is rejected by kubectl strict-decoding on CREATE"
  log "       (and stripped by Flux SSA anyway). Move it to a Kustomize"
  log "       patches: entry, or rely on the Flux force annotation alone."
  exit 1
fi
log "  Flux force annotation present; no inline \$patch: replace residue"

# -----------------------------------------------------------------
# step 5 — runtime: prove the Flux force-recovery path actually works
# -----------------------------------------------------------------
log "step 5/6 — proving Flux force-recovery path (delete bad-state, re-apply manifest)"

kubectl delete deploy -n "${NAMESPACE}" catalyst-api --wait=true >/dev/null 2>&1
RECREATE_OUT=$(mktemp)
RECREATE_ERR=$(mktemp)
set +e
kubectl apply --dry-run=server -n "${NAMESPACE}" -f "${TARGET_MANIFEST}" \
  >"${RECREATE_OUT}" 2>"${RECREATE_ERR}"
RECREATE_RC=$?
set -e
log "  post-delete apply exit=${RECREATE_RC}"
[ -s "${RECREATE_OUT}" ] && log "  apply stdout: $(tr '\n' ' ' <"${RECREATE_OUT}")"
if [ "${RECREATE_RC}" != "0" ]; then
  log "FAIL — Flux force-recovery equivalent (delete + apply) failed"
  log "       this means even the recovery path cannot land the chart manifest;"
  log "       investigate before merging."
  [ -s "${RECREATE_ERR}" ] && log "  apply stderr: $(tr '\n' ' ' <"${RECREATE_ERR}")"
  exit 1
fi
# Post-delete apply must report "created" not "configured" — proving the
# delete actually removed the resource and the apply landed a fresh one.
if ! grep -q "deployment.apps/catalyst-api created" "${RECREATE_OUT}"; then
  log "FAIL — post-delete apply did not create a fresh Deployment"
  log "       expected: 'deployment.apps/catalyst-api created'"
  log "       got: $(tr '\n' ' ' <"${RECREATE_OUT}")"
  exit 1
fi
log "  Flux force-recovery path verified: fresh creation succeeds post-delete"

# -----------------------------------------------------------------
# step 6 — fresh install gate: the chart must be valid for new clusters
# -----------------------------------------------------------------
log "step 6/6 — fresh-install gate: chart manifest must be valid for new namespaces"

# A separate empty namespace catches the failure mode where someone
# (re)introduces inline `$patch: replace` or any other field that
# strict-decoding rejects. Without this gate, the previous steps could
# pass while the chart silently breaks new clusters that have never
# had a kubectl-client-side-apply Deployment.
kubectl create namespace "${FRESH_NAMESPACE}" >/dev/null 2>&1 || true

FRESH_OUT=$(mktemp)
FRESH_ERR=$(mktemp)
set +e
kubectl apply --server-side --field-manager=kustomize-controller --dry-run=server \
  -n "${FRESH_NAMESPACE}" -f "${TARGET_MANIFEST}" >"${FRESH_OUT}" 2>"${FRESH_ERR}"
FRESH_SSA_RC=$?
set -e
log "  fresh-install SSA dry-run exit=${FRESH_SSA_RC}"
[ -s "${FRESH_OUT}" ] && log "  stdout: $(tr '\n' ' ' <"${FRESH_OUT}")"
[ -s "${FRESH_ERR}" ] && log "  stderr: $(tr '\n' ' ' <"${FRESH_ERR}")"
if [ "${FRESH_SSA_RC}" != "0" ]; then
  log "FAIL — chart manifest cannot be applied to an empty namespace via SSA"
  log "       this would break Flux on a fresh cluster bootstrap"
  exit 1
fi

set +e
kubectl apply --dry-run=server -n "${FRESH_NAMESPACE}" -f "${TARGET_MANIFEST}" \
  >"${FRESH_OUT}" 2>"${FRESH_ERR}"
FRESH_CSA_RC=$?
set -e
log "  fresh-install CSA dry-run exit=${FRESH_CSA_RC}"
if [ "${FRESH_CSA_RC}" != "0" ]; then
  log "FAIL — chart manifest cannot be applied to an empty namespace via CSA"
  log "       this would break operator-driven installs"
  [ -s "${FRESH_ERR}" ] && log "  stderr: $(tr '\n' ' ' <"${FRESH_ERR}")"
  exit 1
fi
log "  fresh-install gate passes: manifest creates clean Deployment via SSA + CSA"

# -----------------------------------------------------------------
log "PASS:"
log "  - CSA path: chart manifest applies cleanly over RollingUpdate-shaped Deployment"
log "  - SSA path: regression mode confirmed; Flux force annotation present"
log "  - Recovery path: Flux force-recovery equivalent (delete + apply) succeeds"
log "  - Fresh install: manifest creates clean Deployment in empty namespace"
log "  - Failure mode + remediation documented in docs/CHART-AUTHORING.md"
exit 0
