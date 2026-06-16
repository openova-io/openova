#!/usr/bin/env python3
"""Render-test for the #3642 host-bridge generator logic in
scripts/render-slot-placement.py.

Pure-function unit checks (no filesystem writes) covering:
  * effective_values merges hostBridge.suppress over the slot's values
  * hostbridge_block transcribes hostDocuments verbatim between markers
  * extract_hostbridge round-trips that block
  * strip_hostbridge removes the block (markers + preceding blank)
  * booleans render lowercase (yaml_scalar)
  * a host placement (no hostBridge) yields no block

Run: python3 scripts/tests/render_slot_placement_hostbridge_test.py
Exit 0 on pass, 1 on any failure.
"""
import importlib.util
import os
import sys

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
SCRIPT = os.path.join(REPO, "scripts", "render-slot-placement.py")

spec = importlib.util.spec_from_file_location("rsp", SCRIPT)
rsp = importlib.util.module_from_spec(spec)
spec.loader.exec_module(rsp)

failures = []


def check(name, cond):
    if cond:
        print(f"  ok  {name}")
    else:
        failures.append(name)
        print(f"  FAIL {name}")


# ── fixtures ────────────────────────────────────────────────────────
ROUTE = {
    "apiVersion": "gateway.networking.k8s.io/v1",
    "kind": "HTTPRoute",
    "metadata": {"name": "grafana", "namespace": "grafana"},
    "spec": {
        "parentRefs": [{"name": "cilium-gateway", "namespace": "kube-system"}],
        "hostnames": ["grafana.${SOVEREIGN_FQDN}"],
        "rules": [{"backendRefs": [
            {"name": "grafana-x-grafana-x-mgmt-vcluster", "namespace": "mgmt", "port": 80}]}],
    },
}
SLOT = {
    "file": "25-grafana.yaml", "hr": "bp-grafana", "vcluster": "mgmt",
    "namespace": "grafana",
    "values": {"sso.sovereignFqdn": "${SOVEREIGN_FQDN}"},
    "hostBridge": {
        "suppress": {"gateway.enabled": False, "sso.enabled": False},
        "hostDocuments": [ROUTE],
    },
}
HOST_SLOT = {"file": "16-cnpg.yaml", "hr": "bp-cnpg", "vcluster": "host",
             "namespace": "cnpg-system"}


# 1. effective_values = slot.values + suppress (suppress wins).
ev = rsp.effective_values(SLOT)
check("effective_values merges suppress + slot values",
      ev == {"sso.sovereignFqdn": "${SOVEREIGN_FQDN}",
             "gateway.enabled": False, "sso.enabled": False})

# 2. suppress overrides a colliding slot value.
collide = dict(SLOT)
collide["values"] = {"gateway.enabled": True}
check("suppress wins on key collision",
      rsp.effective_values(collide)["gateway.enabled"] is False)

# 3. hostbridge_block wraps in markers + transcribes the route.
block = rsp.hostbridge_block(SLOT)
check("block starts/ends with markers",
      block[0] == rsp.HOSTBRIDGE_BEGIN and block[-1] == rsp.HOSTBRIDGE_END)
check("block carries the syncer-mangled backendRef verbatim",
      any("grafana-x-grafana-x-mgmt-vcluster" in l for l in block))
check("block carries the public hostname (envsubst placeholder intact)",
      any("grafana.${SOVEREIGN_FQDN}" in l for l in block))

# 4. extract round-trips the same block (markers inclusive).
got = rsp.extract_hostbridge(["  preamble"] + block + ["  trailer"])
check("extract_hostbridge round-trips the block", got == block)

# 5. strip removes the block + its preceding blank line.
with_block = ["a: 1", "", *block, ]
stripped = rsp.strip_hostbridge(with_block)
check("strip_hostbridge removes markers + preceding blank",
      stripped == ["a: 1"] and rsp.extract_hostbridge(stripped) is None)

# 6. a host placement (no hostBridge) yields no block.
check("host placement yields no host-bridge block",
      rsp.hostbridge_block(HOST_SLOT) is None)

# 7. booleans render lowercase.
check("yaml_scalar(False) == 'false'", rsp.yaml_scalar(False) == "false")
check("yaml_scalar(True) == 'true'", rsp.yaml_scalar(True) == "true")
check("yaml_scalar passthrough for strings", rsp.yaml_scalar("mgmt") == "mgmt")

if failures:
    print(f"\nFAILED ({len(failures)}): {failures}", file=sys.stderr)
    sys.exit(1)
print("\nhost-bridge generator render-test PASSED")
