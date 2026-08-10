#!/usr/bin/env bash
# bp-catalyst-platform — catalyst-api MUST be able to DELETE an Organization (#5426).
#
# Measured live on hw293 2026-08-10:
#
#   $ kubectl auth can-i delete organizations.orgs.openova.io \
#       --as=system:serviceaccount:catalyst:catalyst-api-cutover-driver
#   no
#   $ kubectl auth can-i create organizations.orgs.openova.io --as=<same SA>
#   yes
#
# DELETE /api/v1/organizations/{id} answered 204 No Content while the
# Organization CR kept deletionTimestamp: null and its
# `orgs.openova.io/tenant-networking` finalizer, resourceVersion frozen.
# 138 seconds later the namespace and its vcluster StatefulSet were BACK
# under a new uid — the surviving CR's per-Org Flux sources rebuilt them.
#
# templates/clusterrole-cutover-driver.yaml granted the SA
# get/list/watch on organizations (#3978 block) and, separately, create
# (#4921 block). Nothing anywhere in the file granted delete, so the ONE
# apiserver call that fires the org-controller finalizer cascade — the
# only complete teardown of an Organization's namespace / vCluster /
# Keycloak realm / per-Org Flux sources / gateway listeners — was 403'd
# on every console delete.
#
# This gate renders the ClusterRole and asserts the rule set actually
# grants `delete` on organizations, so a future rule-block reshuffle that
# drops the verb fails Blueprint Release publish BEFORE the OCI artifact
# reaches a Sovereign. It is deliberately a RENDER assertion, not a grep
# of the template source: a `{{- if }}` that gates the rule off would
# leave the source string present and the live SA still denied.
#
# Least privilege is asserted too — see the NOT-GRANTED section: the
# cascade's own Flux teardown (per_org_flux.go teardownPerOrgFlux) runs
# under the organization-controller SA, which already carries delete on
# kustomizations + gitrepositories
# (templates/controllers/organization-controller-clusterrole.yaml). The
# catalyst-api SA must NOT be widened to those.
#
# Usage: bash tests/cutover-driver-organization-delete-rbac.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

TEMPLATE="templates/clusterrole-cutover-driver.yaml"

helm template rbacgate . --show-only "${TEMPLATE}" > "$TMP/cr.yaml"

# Vacuity check — a render that produced nothing would make every
# "verb is granted" assertion below trivially unfalsifiable in the
# other direction, and every "verb is NOT granted" assertion pass on air.
if ! grep -q "kind: ClusterRole" "$TMP/cr.yaml"; then
  echo "FAIL: ${TEMPLATE} rendered no ClusterRole — the gate would be testing nothing."
  exit 1
fi
RULE_COUNT=$(grep -c '^\s*- apiGroups:' "$TMP/cr.yaml" || true)
if [ "${RULE_COUNT:-0}" -lt 20 ]; then
  echo "FAIL: rendered ClusterRole has only ${RULE_COUNT} rules — render looks truncated."
  exit 1
fi

# ── verbs_for <apiGroup> <resource> ───────────────────────────────────
# Prints one verb per line for every rule in the rendered ClusterRole
# that covers <apiGroup>/<resource>. RBAC rules merge additively, so the
# effective grant is the UNION across rules — which is exactly why a
# source grep for a single block is not evidence.
verbs_for() {
  local group="$1" resource="$2"
  python3 - "$TMP/cr.yaml" "$group" "$resource" <<'PY'
import sys, yaml
path, group, resource = sys.argv[1], sys.argv[2], sys.argv[3]
verbs = set()
for doc in yaml.safe_load_all(open(path)):
    if not doc or doc.get("kind") != "ClusterRole":
        continue
    for rule in doc.get("rules") or []:
        groups = rule.get("apiGroups") or []
        resources = rule.get("resources") or []
        if group not in groups and "*" not in groups:
            continue
        if resource not in resources and "*" not in resources:
            continue
        verbs.update(rule.get("verbs") or [])
for v in sorted(verbs):
    print(v)
PY
}

fail=0

assert_granted() {
  local group="$1" resource="$2" verb="$3" why="${4:-required by the delete path}"
  if verbs_for "$group" "$resource" | grep -qx "$verb"; then
    echo "PASS: ${group}/${resource} grants '${verb}'"
  else
    echo "FAIL: ${group}/${resource} does NOT grant '${verb}' — ${why}"
    echo "      granted verbs: $(verbs_for "$group" "$resource" | tr '\n' ' ')"
    fail=1
  fi
}

assert_not_granted() {
  local group="$1" resource="$2" verb="$3" why="${4:-not needed by any catalyst-api code path}"
  if verbs_for "$group" "$resource" | grep -qx "$verb"; then
    echo "FAIL: ${group}/${resource} grants '${verb}' — ${why}"
    fail=1
  else
    echo "PASS: ${group}/${resource} correctly withholds '${verb}'"
  fi
}

echo "── GRANTED — the verbs the delete operation actually needs ──"
# The whole point of the #5426 fix.
assert_granted orgs.openova.io organizations delete \
  "HandleDeleteOrganization's apiserver Delete is the ONLY trigger for the org-controller finalizer cascade; without it the console DELETE tears down nothing (hw293)."
# The pre-existing grants the delete path also reads/writes — asserted so
# a reshuffle that adds delete while dropping these still fails.
assert_granted orgs.openova.io organizations get
assert_granted orgs.openova.io organizations list
assert_granted orgs.openova.io organizations create

echo "── NOT GRANTED — least privilege, the control on this gate ──"
# A "fix" that widened the SA to '*' or bolted delete onto the Flux kinds
# would pass every assertion above. These make the gate discriminate.
assert_not_granted kustomize.toolkit.fluxcd.io kustomizations delete \
  "the per-Org Flux teardown runs under the organization-controller SA, which already carries this verb; catalyst-api never deletes a Kustomization."
assert_not_granted source.toolkit.fluxcd.io gitrepositories delete \
  "same — organization-controller-clusterrole.yaml already grants it; catalyst-api never deletes a GitRepository."
assert_not_granted orgs.openova.io organizations '*' \
  "wildcard verbs on the tenancy root are never least privilege."

if [ "$fail" -ne 0 ]; then
  echo
  echo "cutover-driver-organization-delete-rbac: FAILED"
  exit 1
fi
echo
echo "cutover-driver-organization-delete-rbac: all assertions passed"
