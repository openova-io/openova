#!/usr/bin/env bash
# bp-catalyst-platform — organization-controller region-witness render gate (#6027).
#
# WHY THIS GATE EXISTS
# --------------------
# The per-Org console listener pair (`console-https-<slug>` /
# `console-http-<slug>`) must land in EVERY region the console ELB pool forwards
# to, or roughly 1/N of customer TLS connections reach a region with no listener
# for that SNI and are reset. #5246/#5957 built that fan-out and guarded it with
# a witness of how many regions exist, so "no secondary kubeconfig wired" could
# never silently read as "no secondary region".
#
# That witness was the Cilium ClusterMesh config Secret alone, and it has a
# window in which it cannot answer: the Secret does not exist until the mesh is
# ESTABLISHED. Measured read-only on hw293 dep a0077ba47e3720e5 on 2026-08-11:
#
#   kube-system/cilium-clustermesh              -> NotFound in BOTH regions
#   region A kube-system/cilium-gateway-console -> console-https-hw293walkone,
#                                                  console-https-hw293walktwo
#   region B kube-system/cilium-gateway-console -> the three Sovereign pairs,
#                                                  ZERO per-Org listeners
#   catalyst-system/sovereign-fqdn              -> configuredRegions =
#                                    hw-me-east-215-a-rtz-prod,hw-me-east-215-b-rtz-prod
#
# Both Organizations nevertheless read fully provisioned. UAT rows R16/87/90/95
# measured the customer half: 6/12, 8/12 and 5/12 fresh-TCP
# `curl(35) Connection reset by peer` on a per-Org host, against an apex control
# on the SAME VIP that answers every time.
#
# The second witness is `configuredRegions` — written at bootstrap, independent
# of ClusterMesh AND of the cutover chart that owns the kubeconfig bridge. It
# reaches the controller ONLY as a kubelet-injected env, so if this template
# wiring is dropped or renamed the Go-side witness silently reads empty and the
# defect returns with every unit test still green. That is what this gate holds.
#
# Test framework: pure `helm template` + grep/awk on rendered YAML, matching
# tests/sovereign-fqdn-lb-ip-contract.sh. Picked up automatically by
# .github/workflows/blueprint-release.yaml ("Run chart integration tests
# (chart/tests/*.sh)").
#
# Usage: bash tests/org-controller-region-witness-contract.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

ORG_TEMPLATE="templates/controllers/organization-controller-deployment.yaml"
CM_TEMPLATE="templates/sovereign-fqdn-configmap.yaml"

# The live hw293 region list — the exact value the witness must carry end to end.
REGION_A="hw-me-east-215-a-rtz-prod"
REGION_B="hw-me-east-215-b-rtz-prod"

# render_org <outfile> — render the org-controller Deployment with a 2-region
# Sovereign configured, the shape the witness has to survive.
render_org() {
  helm template smoke-org . \
    --set global.sovereignFQDN=hw293.omantel.biz \
    --set "sovereign.configuredRegions[0]=${REGION_A}" \
    --set "sovereign.configuredRegions[1]=${REGION_B}" \
    --show-only "${ORG_TEMPLATE}" > "$1"
}

echo "[org-region-witness] Case 1: the PRODUCER renders configuredRegions with both regions (#6027)"
helm template smoke-cm . \
  --set global.sovereignFQDN=hw293.omantel.biz \
  --set "sovereign.configuredRegions[0]=${REGION_A}" \
  --set "sovereign.configuredRegions[1]=${REGION_B}" \
  --show-only "${CM_TEMPLATE}" > "$TMP/cm.yaml"

# Assert on the VALUE, not on the key existing: an empty `configuredRegions: ""`
# renders the key and would pass a key-only grep while telling the controller
# there are no regions at all — the exact silent-zero this gate exists to stop.
CFG_VALUE="$(awk -F': ' '/^  configuredRegions:/{print $2}' "$TMP/cm.yaml" | tr -d '"')"
if [ -z "${CFG_VALUE}" ]; then
  echo "FAIL: sovereign-fqdn renders configuredRegions EMPTY on a 2-region Sovereign — the region witness carries no regions and the per-Org listener fan-out silently expects zero secondaries (#6027)" >&2
  exit 1
fi
if [ "${CFG_VALUE}" != "${REGION_A},${REGION_B}" ]; then
  echo "FAIL: configuredRegions rendered '${CFG_VALUE}', want '${REGION_A},${REGION_B}' — the witness would under-report the region count (#6027)" >&2
  exit 1
fi
echo "  PASS (configuredRegions = ${CFG_VALUE})"

# assert_env <rendered.yaml> <ENV_NAME> <configmap> <key>
#
# Parses the rendered Deployment and asserts the named env exists as a real
# entry in the container's env LIST, resolving to the given ConfigMap key.
#
# It deliberately does NOT grep the rendered text. The template documents this
# env in a YAML comment, and `helm template` passes comments through, so a
# text grep for the env NAME passes on a chart whose env entry has been deleted
# and only the comment remains — proven by this gate's own Case 4, which caught
# exactly that in the first draft of this file.
assert_env() {
  python3 - "$1" "$2" "$3" "$4" <<'PY'
import sys, yaml
path, name, cm, key = sys.argv[1:5]
docs = [d for d in yaml.safe_load_all(open(path)) if d]
envs = []
for d in docs:
    if d.get("kind") != "Deployment":
        continue
    for c in d["spec"]["template"]["spec"]["containers"]:
        envs.extend(c.get("env") or [])
hit = next((e for e in envs if e.get("name") == name), None)
if hit is None:
    sys.exit(
        f"FAIL: {name} is not an env entry on the organization-controller container "
        f"(the container declares {len(envs)} envs: {sorted(e.get('name','') for e in envs)}). "
        "tenant_console_tls_regions.go then reads an empty witness, a 2-region Sovereign "
        "resolves to a one-region target set, and every Organization goes green over a "
        "region whose Gateway never received the per-Org listener (#6027)"
    )
ref = (hit.get("valueFrom") or {}).get("configMapKeyRef") or {}
# Assert BOTH halves: the right ConfigMap with the wrong key resolves empty and
# is indistinguishable from the env being absent entirely.
if ref.get("name") != cm:
    sys.exit(f"FAIL: {name} sources from ConfigMap {ref.get('name')!r}, want {cm!r} — witness wiring drifted (#6027)")
if ref.get("key") != key:
    sys.exit(f"FAIL: {name} reads key {ref.get('key')!r}, want {key!r} — it would resolve empty and read as a single-region Sovereign (#6027)")
PY
}

echo "[org-region-witness] Case 2: the CONSUMER wires CATALYST_CONFIGURED_REGIONS from that same ConfigMap key"
render_org "$TMP/org.yaml"
assert_env "$TMP/org.yaml" CATALYST_CONFIGURED_REGIONS sovereign-fqdn configuredRegions
echo "  PASS (org-controller CATALYST_CONFIGURED_REGIONS ← sovereign-fqdn/configuredRegions)"

echo "[org-region-witness] Case 3: CONTROL — the pre-existing console-LB wiring is untouched"
# Discriminates Case 2 from a template that grew a blanket envFrom: this env is
# asserted independently and must still be individually wired.
assert_env "$TMP/org.yaml" CATALYST_TENANT_CONSOLE_LB_IPV4 sovereign-fqdn consoleLBIP
echo "  PASS (consoleLBIP wiring intact)"

echo "[org-region-witness] Case 4: VACUITY — the Case-2 assertion must FAIL on a template with the witness stripped"
# A render guard that has never been watched failing is decorative. Strip the
# env block from a COPY of the chart, re-render, and require the same assertion
# to go red. If it stays green the guard is testing something that cannot fail.
VAC="$TMP/vacuity"
mkdir -p "$VAC"
cp -r . "$VAC/chart" 2>/dev/null || true
python3 - "$VAC/chart/${ORG_TEMPLATE}" <<'PY'
import re, sys
p = sys.argv[1]
s = open(p).read()
# Remove the CATALYST_CONFIGURED_REGIONS env entry (name + its valueFrom block).
out = re.sub(
    r"\n\s*- name: CATALYST_CONFIGURED_REGIONS\n(?:\s+.*\n)*?\s+optional: true\n",
    "\n",
    s,
    count=1,
)
if out == s:
    sys.exit("vacuity harness could not strip CATALYST_CONFIGURED_REGIONS — the gate cannot prove it fails")
open(p, "w").write(out)
PY
( cd "$VAC/chart" && helm template smoke-vac . \
    --set global.sovereignFQDN=hw293.omantel.biz \
    --set "sovereign.configuredRegions[0]=${REGION_A}" \
    --set "sovereign.configuredRegions[1]=${REGION_B}" \
    --show-only "${ORG_TEMPLATE}" ) > "$TMP/org-stripped.yaml"

# Run the REAL Case-2 assertion against the stripped render. It must fail; if it
# passes, Case 2 is decorative and cannot detect the wiring being removed.
if assert_env "$TMP/org-stripped.yaml" CATALYST_CONFIGURED_REGIONS sovereign-fqdn configuredRegions 2>/dev/null; then
  echo "FAIL: vacuity check — the Case-2 assertion still PASSED against a template with CATALYST_CONFIGURED_REGIONS stripped, so it cannot detect the wiring being removed (#6027)" >&2
  exit 1
fi
# And the control must still pass on the same stripped render, proving the
# vacuity harness removed only the env under test.
assert_env "$TMP/org-stripped.yaml" CATALYST_TENANT_CONSOLE_LB_IPV4 sovereign-fqdn consoleLBIP
echo "  PASS (stripping the env makes Case 2 go red while the control stays green — the gate can fail)"

echo "[org-region-witness] All gates green."
