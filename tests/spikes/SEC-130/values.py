#!/usr/bin/env python3
"""Values for the upstream openbao chart, for lab test SEC-130.

  values.py today   TAG   the `openbao:` block of platform/openbao/chart/values.yaml
                          as the bootstrap-kit slot runs it (one replica, no
                          affinity), image from quay.io, injector off.
  values.py upgrade TAG   `today` plus the one value chart 0.30.7 needs to upgrade
                          a release made by chart 0.16.0 in place:
                          podManagementPolicy kept at Parallel (immutable field).
  values.py new     TAG   `upgrade` plus the wiring under test: the seal directory
                          of the node mounted read-only at /openbao/seal, the
                          ConfigMap openbao-seal-config loaded as an extra
                          configuration directory, node selector and toleration.

Prints JSON (a valid values file).
"""
import json
import os
import sys

import yaml

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "..", ".."))
mode, tag = sys.argv[1], sys.argv[2]
v = yaml.safe_load(open(os.path.join(ROOT, "platform/openbao/chart/values.yaml")))["openbao"]
s = v["server"]
s["image"] = {"registry": "quay.io", "repository": "openbao/openbao", "tag": tag}
s["ha"]["replicas"] = 1          # clusters/_template/bootstrap-kit/08-openbao.yaml
s["affinity"] = ""               # same file
v["injector"]["enabled"] = False  # not part of the question; saves a pod
if mode in ("upgrade", "new"):
    s["podManagementPolicy"] = "Parallel"
if mode == "new":
    s["extraArgs"] = "-config=/openbao/userconfig/openbao-seal-config/"
    s["extraVolumes"] = [{"type": "configMap", "name": "openbao-seal-config"}]
    s["volumes"] = [{"name": "seal", "hostPath": {"path": "/var/lib/catalyst/seal", "type": "Directory"}}]
    s["volumeMounts"] = [{"name": "seal", "mountPath": "/openbao/seal", "readOnly": True}]
    s["nodeSelector"] = {"catalyst.openova.io/role": "control-plane"}
    s["tolerations"] = [{"operator": "Exists", "effect": "NoSchedule"}]
if mode not in ("today", "upgrade", "new"):
    sys.exit("mode must be today, upgrade or new")
print(json.dumps(v, indent=1))
