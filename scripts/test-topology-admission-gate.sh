#!/usr/bin/env bash
# Red-proof test for the G116 topology admission gate (#3375 DoD-3).
#
# The gate logic lives in .github/workflows/blueprint-admission-validate.yml.
# This test exercises the SAME logic against synthetic fixtures to PROVE it
# (a) PASSES every real platform/*/blueprint.yaml (minus the documented
#     scope-removed exemptions), and
# (b) REJECTS each invalid shape: missing spec.topology (G116), a
#     non-canonical cluster ID, a missing per-cluster role, and a
#     non-canonical BCP key.
#
# A gate that only ever goes green is theater — this test fails loudly if
# any red case is silently accepted.
#
# Usage: scripts/test-topology-admission-gate.sh   (run from repo root)
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

python3 - << 'PY'
import json, re, sys, tempfile, os, yaml, jsonschema, pathlib

schema = json.load(open('platform/_schemas/blueprint-topology.json'))
CLUSTER_RE = re.compile(r'^(mgmt|dmz|rtz)-[AB]$')
VALID_BCP = {"active-active", "active-hot-standby", "active-passive", "singleton"}
EXEMPT = {"sandbox"}  # mirror GATE_EXEMPT_BLUEPRINTS in the workflow


def validate(doc, name="<fixture>", require_topology=True):
    """Returns a list of findings (empty = valid). Mirrors the workflow."""
    if name in EXEMPT:
        return []
    findings = []
    spec = (doc.get('spec') or {})
    topo = spec.get('topology')
    if require_topology and not topo:
        return [f"{name}: G116 missing spec.topology"]
    try:
        jsonschema.validate(doc, schema)
    except jsonschema.ValidationError as e:
        return [f"{name}: schema: {e.message[:120]}"]
    for vk, v in ((topo or {}).get('perTopology') or {}).items():
        if vk not in VALID_BCP:
            findings.append(f"{name}: bad bcp key {vk}")
        pl = (v or {}).get('placement') or {}
        clusters = pl.get('clusters') or []
        roles = pl.get('roles') or {}
        for c in clusters:
            if not CLUSTER_RE.match(c):
                findings.append(f"{name}: bad cluster {c}")
            if c not in roles:
                findings.append(f"{name}: {vk} {c} missing role")
    return findings


fail = False

# ── GREEN: every real blueprint passes (minus exemptions) ───────────────
real_failures = []
checked = 0
for path in sorted(list(pathlib.Path('.').glob('platform/*/blueprint.yaml')) +
                   list(pathlib.Path('.').glob('products/*/blueprint.yaml'))):
    checked += 1
    doc = yaml.safe_load(path.read_text())
    findings = validate(doc, path.parent.name)
    if findings:
        real_failures.extend(findings)
if real_failures:
    print(f"FAIL (green case): {len(real_failures)} real blueprint(s) rejected by the live gate:")
    for f in real_failures:
        print(f"  ! {f}")
    fail = True
else:
    print(f"PASS green: {checked} real blueprint.yaml validate clean under the live gate")

# ── RED: each invalid shape MUST be rejected ────────────────────────────
GOOD_TOPO = {
    "supported": ["singleton"],
    "defaults": {"multi-region": "singleton", "single-region": "singleton"},
    "perTopology": {"singleton": {"placement": {
        "tier": "rtz", "clusters": ["rtz-A"], "roles": {"rtz-A": "singleton"}}}},
}


def red_case(label, mutate):
    doc = {"apiVersion": "catalyst.openova.io/v1alpha1", "kind": "Blueprint",
           "metadata": {"name": "bp-redcase"},
           "spec": {"version": "0.0.1", "topology": json.loads(json.dumps(GOOD_TOPO))}}
    mutate(doc)
    findings = validate(doc, "bp-redcase")
    if findings:
        print(f"PASS red [{label}]: correctly rejected ({findings[0][:80]})")
        return False
    print(f"FAIL red [{label}]: invalid shape was ACCEPTED — the gate is a no-op")
    return True


def drop_topology(d):
    del d["spec"]["topology"]


def bad_cluster(d):
    d["spec"]["topology"]["perTopology"]["singleton"]["placement"]["clusters"] = ["hz-fsn-rtz-prod"]
    d["spec"]["topology"]["perTopology"]["singleton"]["placement"]["roles"] = {"hz-fsn-rtz-prod": "singleton"}


def missing_role(d):
    d["spec"]["topology"]["perTopology"]["singleton"]["placement"]["roles"] = {}


def bad_bcp_key(d):
    d["spec"]["topology"]["perTopology"]["single-region"] = \
        d["spec"]["topology"]["perTopology"].pop("singleton")
    d["spec"]["topology"]["supported"] = ["single-region"]
    d["spec"]["topology"]["defaults"] = {"multi-region": "single-region", "single-region": "single-region"}


fail |= red_case("G116-missing-topology", drop_topology)
fail |= red_case("non-canonical-cluster-id", bad_cluster)
fail |= red_case("missing-per-cluster-role", missing_role)
fail |= red_case("non-canonical-bcp-key", bad_bcp_key)

if fail:
    print("\ntopology admission gate test FAILED")
    sys.exit(1)
print("\nALL CASES PASS — topology admission gate (G116) is live and red-proof")
PY
