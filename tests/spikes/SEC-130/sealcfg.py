#!/usr/bin/env python3
"""ConfigMap openbao-seal-config for lab test SEC-130.

  sealcfg.py NAMESPACE KEY_ID

Three files, loaded by `bao server` as an extra configuration directory:
  seal.hcl        seal "static", key read from /openbao/seal/openbao-seal-<KEY_ID>.key
  initialize.hcl  self-initialisation: kubernetes auth mount + its config, the
                  openbao-config-admin policy (config-admin-policy.hcl, encoded
                  as one HCL string the way a Helm `toJson` would) and the
                  openbao-config-admin role bound to openbao-config in NAMESPACE
  audit.hcl       a declared file audit device writing to stdout
Prints YAML.
"""
import json
import os
import sys

import yaml

ns, key_id = sys.argv[1], sys.argv[2]
policy = open(os.path.join(os.path.dirname(os.path.abspath(__file__)), "config-admin-policy.hcl")).read()

seal = f'''seal "static" {{
  current_key_id = "{key_id}"
  current_key    = "file:///openbao/seal/openbao-seal-{key_id}.key"
}}
'''

initialize = f'''initialize "platform" {{
  request "auth-kubernetes" {{
    operation = "update"
    path      = "sys/auth/kubernetes"
    data      = {{ type = "kubernetes" }}
  }}
  request "auth-kubernetes-config" {{
    operation = "update"
    path      = "auth/kubernetes/config"
    data      = {{ kubernetes_host = "https://kubernetes.default.svc" }}
  }}
  request "policy-config-admin" {{
    operation = "update"
    path      = "sys/policies/acl/openbao-config-admin"
    data      = {{ policy = {json.dumps(policy)} }}
  }}
  request "role-config-admin" {{
    operation = "update"
    path      = "auth/kubernetes/role/openbao-config-admin"
    data = {{
      bound_service_account_names      = ["openbao-config"]
      bound_service_account_namespaces = ["{ns}"]
      token_policies                   = ["openbao-config-admin"]
      token_ttl                        = "15m"
      token_max_ttl                    = "1h"
    }}
  }}
}}
'''

audit = '''audit "file" "stdout" {
  description = "Audit to standard output; collected by the log pipeline."
  options {
    file_path = "stdout"
  }
}
'''

cm = {
    "apiVersion": "v1",
    "kind": "ConfigMap",
    "metadata": {"name": "openbao-seal-config", "namespace": ns},
    "data": {"seal.hcl": seal, "initialize.hcl": initialize, "audit.hcl": audit},
}
print(yaml.safe_dump(cm, sort_keys=False, width=float("inf")))
