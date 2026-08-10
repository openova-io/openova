#!/usr/bin/env bash
# bp-catalyst-platform — organization-controller MUST be able to DELETE the
# per-Org wildcard TLS Secret it orphans on teardown (#6092, Refs #5426).
#
# THE SIBLING GAP TO #6051. That fix granted catalyst-api `delete` on
# organizations, so the apiserver Delete now lands and the finalizer cascade
# finally starts. Measured live on hw293 (dep a0077ba47e3720e5) 2026-08-10:
#
#   $ kubectl auth can-i delete organizations.orgs.openova.io \
#       --as=system:serviceaccount:catalyst-system:catalyst-api-cutover-driver
#   yes                                     # was `no` before #6051
#
# The cascade then stalls one hop later, on the OTHER service account:
#
#   organization-controller.reconciler  tenant-networking teardown: console TLS
#   delete Secret kube-system/org-wildcard-tls-p474del1-omani-rest:
#     secrets "org-wildcard-tls-p474del1-omani-rest" is forbidden:
#     User "system:serviceaccount:catalyst-system:catalyst-organization-controller"
#     cannot delete resource "secrets" in API group "" in the namespace "kube-system"
#
# teardownTenantConsoleTLS returns that error, teardownTenantNetworking
# propagates it as firstErr, and organization_controller.go never reaches the
# finalizer strip. Organization `p474del1` therefore held
# deletionTimestamp=2026-08-10T23:17:42Z with finalizer
# `orgs.openova.io/tenant-networking` still attached while the reconciler
# reprinted the same denial every 30s — a CR wedged in Terminating for as long
# as the loop runs, on an Org whose namespace was already gone.
#
# The Secret is not litter. deleteOrgWildcardTLSSecret exists (tenant_console_-
# tls.go:685) because the name is DERIVED from slug+parentDomain, so the next
# Org taking that subdomain lands on a Secret of exactly the expected name
# holding the PREVIOUS Org's certificate. Withholding the verb keeps the
# cross-Org identity leak the delete path was written to close.
#
# organization-controller-clusterrole.yaml granted secrets
# {get,list,watch} and nothing else — one rule, added by #1098 for reading an
# OIDC clientSecretRef, never revisited when the teardown delete landed.
#
# This gate renders the ClusterRole and asserts the EFFECTIVE verb union on
# core/secrets, so a future rule-block reshuffle that drops `delete` fails
# Blueprint Release publish BEFORE the OCI artifact reaches a Sovereign. It is
# deliberately a RENDER assertion, not a grep of the template source: a
# `{{- if }}` that gated the rule off would leave the source string present and
# the live SA still denied.
#
# Least privilege is asserted too — see the NOT-GRANTED section. The controller
# performs exactly ONE Secret mutation in the whole package (that Delete); it
# never creates, updates or patches a Secret, so granting those would be a
# widening this gate must catch.
#
# Usage: bash tests/organization-controller-secret-delete-rbac.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

TEMPLATE="templates/controllers/organization-controller-clusterrole.yaml"

helm template rbacgate . --show-only "${TEMPLATE}" > "$TMP/cr.yaml"

# Vacuity check — a render that produced nothing would make every
# "verb is granted" assertion below trivially unfalsifiable in the other
# direction, and every "verb is NOT granted" assertion pass on air.
if ! grep -q "kind: ClusterRole" "$TMP/cr.yaml"; then
  echo "FAIL: ${TEMPLATE} rendered no ClusterRole — the gate would be testing nothing."
  exit 1
fi
RULE_COUNT=$(grep -c '^\s*- apiGroups:' "$TMP/cr.yaml" || true)
if [ "${RULE_COUNT:-0}" -lt 10 ]; then
  echo "FAIL: rendered ClusterRole has only ${RULE_COUNT} rules — render looks truncated."
  exit 1
fi
echo "render OK: ${RULE_COUNT} rules"

# ── verbs_for <apiGroup> <resource> ───────────────────────────────────
# Prints one verb per line for every rule in the rendered ClusterRole that
# covers <apiGroup>/<resource>. RBAC rules merge additively, so the effective
# grant is the UNION across rules — which is exactly why a source grep for a
# single block is not evidence.
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
  local group="$1" resource="$2" verb="$3" why="${4:-required by the teardown path}"
  if verbs_for "$group" "$resource" | grep -qx "$verb"; then
    echo "PASS: ${group:-core}/${resource} grants '${verb}'"
  else
    echo "FAIL: ${group:-core}/${resource} does NOT grant '${verb}' — ${why}"
    echo "      granted verbs: $(verbs_for "$group" "$resource" | tr '\n' ' ')"
    fail=1
  fi
}

assert_not_granted() {
  local group="$1" resource="$2" verb="$3" why="${4:-no organization-controller code path calls it}"
  if verbs_for "$group" "$resource" | grep -qx "$verb"; then
    echo "FAIL: ${group:-core}/${resource} grants '${verb}' — ${why}"
    fail=1
  else
    echo "PASS: ${group:-core}/${resource} correctly withholds '${verb}'"
  fi
}

echo "── GRANTED — the verbs the teardown actually needs ──"
# The whole point of the #6092 fix.
assert_granted "" secrets delete \
  "deleteOrgWildcardTLSSecret (tenant_console_tls.go:685) is the ONLY closer of the name-derived cross-Org certificate leak, and its error wedges the tenant-networking finalizer forever (hw293 p474del1)."
# The pre-existing grants the same package reads — asserted so a reshuffle that
# adds delete while dropping these still fails.
assert_granted "" secrets get
assert_granted "" secrets list
assert_granted "" secrets watch
# The finalizer strip itself, without which granting the delete changes nothing.
assert_granted orgs.openova.io organizations update \
  "removing the tenant-networking finalizer is a client.Update on the parent Organization (#4462)."

echo "── NOT GRANTED — least privilege, the control on this gate ──"
# A "fix" that widened the SA to '*' on secrets, or bolted on the write verbs
# it never uses, would pass every assertion above. These make the gate
# discriminate rather than merely count verbs.
assert_not_granted "" secrets create \
  "the controller mints no Secret — cert-manager produces the wildcard TLS Secret from the Certificate."
assert_not_granted "" secrets update \
  "the controller never rewrites a Secret; the only mutation in the package is the teardown Delete."
assert_not_granted "" secrets patch \
  "same — no SSA or merge-patch path touches a Secret."
assert_not_granted "" secrets '*' \
  "wildcard verbs on cluster-wide Secrets are never least privilege."

if [ "$fail" -ne 0 ]; then
  echo
  echo "organization-controller-secret-delete-rbac: FAILED"
  exit 1
fi
echo
echo "organization-controller-secret-delete-rbac: all assertions passed"
