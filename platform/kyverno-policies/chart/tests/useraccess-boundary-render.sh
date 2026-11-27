#!/usr/bin/env bash
# useraccess-boundary-render.sh - the access policy refuses an empty list of
# signers at render (CMP-095, kept by the move into this chart, CMP-086).
#
# templates/baseline/25-useraccess-boundary.yaml lets a requester grant into
# another Organization only when its Organization is in
# compliancePolicies.useraccessBoundary.managementOrgs and its tier in
# .allowedCrossOrgTiers. An empty list would leave no one who may sign such a
# grant, so the template fails the render instead. This test fails when
#   R1 the template does not render with the chart defaults, or renders no
#      ValidatingPolicy useraccess-boundary, or its lists are not the defaults;
#   R2 an empty managementOrgs or allowedCrossOrgTiers renders, or fails for
#      another reason than the template's own message.
#
# Usage: bash useraccess-boundary-render.sh [<chart directory>]
# Exit code: 0 pass. 1 a check failed.
#
# HELM_BIN  The helm command. Default: helm.
set -euo pipefail
export LC_ALL=C

CHART="$(cd "${1:-$(dirname "${BASH_SOURCE[0]}")/..}" && pwd)"
HELM="${HELM_BIN:-helm}"
TEMPLATE="templates/baseline/25-useraccess-boundary.yaml"
TAG="[useraccess-boundary-render]"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

if ! "$HELM" template t "$CHART" --show-only "$TEMPLATE" > "$TMP/render.yaml" 2> "$TMP/err"; then
  cat "$TMP/err"
  echo "$TAG FAIL R1: $TEMPLATE does not render with the chart defaults"
  exit 1
fi
for want in 'kind: ValidatingPolicy' '  name: useraccess-boundary' \
  "      expression: \"['openova-internal']\"" \
  "      expression: \"['owner', 'catalyst-owner']\""; do
  if ! grep -q -x -F "$want" "$TMP/render.yaml"; then
    echo "$TAG FAIL R1: the default render of $TEMPLATE has no line: $want"
    exit 1
  fi
done
echo "$TAG PASS R1 the default render holds the ValidatingPolicy and both default lists"

for key in managementOrgs allowedCrossOrgTiers; do
  if "$HELM" template t "$CHART" --show-only "$TEMPLATE" \
    --set-json "compliancePolicies.useraccessBoundary.$key=[]" > /dev/null 2> "$TMP/empty-$key.err"; then
    echo "$TAG FAIL R2: compliancePolicies.useraccessBoundary.$key=[] rendered; the template must refuse it"
    exit 1
  fi
  if ! grep -q -F "compliancePolicies.useraccessBoundary.$key is empty" "$TMP/empty-$key.err"; then
    cat "$TMP/empty-$key.err"
    echo "$TAG FAIL R2: compliancePolicies.useraccessBoundary.$key=[] failed for another reason"
    exit 1
  fi
  echo "$TAG PASS R2 an empty $key is refused at render"
done
echo "$TAG ALL PASS"
