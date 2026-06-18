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

# 3. hostbridge_block is DEPRECATED (#3804) — host-bridge CRs now live in
#    bootstrap-kit-crs/<slot> (see hostbridge_docs_yaml), NOT in-slot; the
#    function returns None always (retained only so strip/extract can detect a
#    stale in-slot block on a slot authored before the split).
check("hostbridge_block deprecated (returns None)",
      rsp.hostbridge_block(SLOT) is None)

# 3b. hostbridge_docs_yaml renders the hostDocuments as the bootstrap-kit-crs
#     file body — verbatim transcription of the route.
docs = rsp.hostbridge_docs_yaml(SLOT)
check("hostbridge_docs_yaml carries the syncer-mangled backendRef verbatim",
      docs is not None and "grafana-x-grafana-x-mgmt-vcluster" in docs)
check("hostbridge_docs_yaml carries the public hostname (envsubst placeholder intact)",
      "grafana.${SOVEREIGN_FQDN}" in docs)
check("hostbridge_docs_yaml starts each doc with a --- separator",
      docs.lstrip().startswith("---"))

# 4. extract/strip still detect + remove a STALE in-slot block (a slot authored
#    before the #3804 split) — feed a literal marker block.
legacy_block = [rsp.HOSTBRIDGE_BEGIN, "  apiVersion: v1", "  kind: ConfigMap", rsp.HOSTBRIDGE_END]
got = rsp.extract_hostbridge(["  preamble"] + legacy_block + ["  trailer"])
check("extract_hostbridge round-trips a stale in-slot block", got == legacy_block)

# 5. strip removes the stale block + its preceding blank line.
stripped = rsp.strip_hostbridge(["a: 1", "", *legacy_block])
check("strip_hostbridge removes markers + preceding blank",
      stripped == ["a: 1"] and rsp.extract_hostbridge(stripped) is None)

# 6. a host placement (no hostBridge) yields no host-bridge docs.
check("host placement yields no host-bridge docs",
      rsp.hostbridge_docs_yaml(HOST_SLOT) is None)

# 7. booleans render lowercase.
check("yaml_scalar(False) == 'false'", rsp.yaml_scalar(False) == "false")
check("yaml_scalar(True) == 'true'", rsp.yaml_scalar(True) == "true")
check("yaml_scalar passthrough for strings", rsp.yaml_scalar("mgmt") == "mgmt")

if failures:
    print(f"\nFAILED ({len(failures)}): {failures}", file=sys.stderr)
    sys.exit(1)
print("\nhost-bridge generator render-test PASSED")
