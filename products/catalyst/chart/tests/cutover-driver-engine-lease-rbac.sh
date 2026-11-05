#!/usr/bin/env bash
# bp-catalyst-platform — catalyst-api MUST be able to hold the cutover engine
# Lease (#6289).
#
# Measured live on hw307 2026-11-05 (region A, catalyst-api SA):
#
#   $ kubectl auth can-i create leases.coordination.k8s.io -n catalyst \
#       --as=system:serviceaccount:catalyst-system:catalyst-api-cutover-driver
#   no
#   $ kubectl auth can-i update leases.coordination.k8s.io -n catalyst --as=<same SA>
#   no
#
# The engine now fences itself with coordination.k8s.io/Lease
# `self-sovereign-cutover-engine` in the cutover namespace: /start and the
# startup resume acquire it, the drain hook releases it, a lost renewal stops
# the engine. Without these verbs the Lease API answers Forbidden and the
# engine runs DEGRADED (unfenced, loudly) — so a rule-block reshuffle that
# drops them would silently re-open the #6289 two-drivers window on every
# fresh Sovereign. This gate renders the ClusterRole and asserts the
# effective grant, not the template source.
#
# Least privilege: `list`/`watch`/`patch` on leases are NOT granted — the
# engine Gets by name, Creates, Updates and Deletes exactly one Lease.
#
# Usage: bash tests/cutover-driver-engine-lease-rbac.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

TEMPLATE="templates/clusterrole-cutover-driver.yaml"
helm template rbacgate . --show-only "${TEMPLATE}" > "$TMP/cr.yaml"

if ! grep -q "kind: ClusterRole" "$TMP/cr.yaml"; then
  echo "FAIL: ${TEMPLATE} rendered no ClusterRole — the gate would be testing nothing."
  exit 1
fi
RULE_COUNT=$(grep -c '^\s*- apiGroups:' "$TMP/cr.yaml" || true)
if [ "${RULE_COUNT:-0}" -lt 20 ]; then
  echo "FAIL: rendered ClusterRole has only ${RULE_COUNT} rules — render looks truncated."
  exit 1
fi

verbs_for() {
  local group="$1" resource="$2"
  python3 - "$TMP/cr.yaml" "$group" "$resource" <<'PY'
import sys, yaml
path, group, resource = sys.argv[1], sys.argv[2], sys.argv[3]
verbs = set()
for doc in yaml.safe_load_all(open(path)):
    if not doc or doc.get("kind") != "ClusterRole":
        continue
    if doc.get("metadata", {}).get("name") != "catalyst-api-cutover-driver":
        continue
    for rule in doc.get("rules", []) or []:
        if group in (rule.get("apiGroups") or []) and resource in (rule.get("resources") or []):
            # A rule that pins resourceNames cannot grant create (POST has no
            # name) — such a rule is not evidence for create.
            for v in rule.get("verbs") or []:
                if v == "create" and rule.get("resourceNames"):
                    continue
                verbs.add(v)
print("\n".join(sorted(verbs)))
PY
}

LEASE_VERBS="$(verbs_for coordination.k8s.io leases)"
FAIL=0
for want in create get update delete; do
  if ! grep -qx "$want" <<<"$LEASE_VERBS"; then
    echo "FAIL: catalyst-api-cutover-driver does not grant '${want}' on coordination.k8s.io/leases (#6289) — effective verbs: $(tr '\n' ' ' <<<"$LEASE_VERBS")"
    FAIL=1
  else
    echo "  ok   — leases: ${want} granted"
  fi
done
for notwant in list watch patch; do
  if grep -qx "$notwant" <<<"$LEASE_VERBS"; then
    echo "FAIL: catalyst-api-cutover-driver grants '${notwant}' on leases — wider than the engine needs"
    FAIL=1
  else
    echo "  ok   — leases: ${notwant} not granted (least privilege)"
  fi
done

# The create rule must not carry resourceNames (feedback_rbac_create_no_resourcenames.md).
if python3 - "$TMP/cr.yaml" <<'PY'
import sys, yaml
for doc in yaml.safe_load_all(open(sys.argv[1])):
    if not doc or doc.get("kind") != "ClusterRole" or doc["metadata"]["name"] != "catalyst-api-cutover-driver":
        continue
    for rule in doc.get("rules", []) or []:
        if "leases" in (rule.get("resources") or []) and "create" in (rule.get("verbs") or []) and rule.get("resourceNames"):
            sys.exit(1)
sys.exit(0)
PY
then echo "  ok   — the leases create rule carries no resourceNames"; else echo "FAIL: leases create rule carries resourceNames (403 on every POST)"; FAIL=1; fi

# The engine's holder identity must be wired on the api Deployment.
helm template envgate . --set global.sovereignFQDN=hw307.omani.works --show-only templates/api-deployment.yaml > "$TMP/dep.yaml"
if python3 - "$TMP/dep.yaml" <<'PY'
import sys, yaml
ok = False
for doc in yaml.safe_load_all(open(sys.argv[1])):
    if not doc or doc.get("kind") != "Deployment":
        continue
    for c in doc["spec"]["template"]["spec"]["containers"]:
        for e in c.get("env") or []:
            if e.get("name") == "CATALYST_POD_NAME" and ((e.get("valueFrom") or {}).get("fieldRef") or {}).get("fieldPath") == "metadata.name":
                ok = True
sys.exit(0 if ok else 1)
PY
then echo "  ok   — api-deployment wires CATALYST_POD_NAME from metadata.name"; else echo "FAIL: api-deployment does not wire CATALYST_POD_NAME (the Lease holder identity, #6289)"; FAIL=1; fi

if [ "$FAIL" -ne 0 ]; then exit 1; fi
echo "PASS: catalyst-api-cutover-driver can hold the cutover engine Lease (#6289)"
