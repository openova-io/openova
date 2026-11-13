"""CLD-055 spike, L4: print the ProviderConfig and the Workspace as one JSON List."""
import base64
import json
import os
import sys

d = sys.argv[1]
module = open(os.path.join(d, "unit.tf")).read()
user_data = open(os.path.join(d, "worker-user-data.yaml"), "rb").read()

provider_config = {
    "apiVersion": "opentofu.upbound.io/v1beta1",
    "kind": "ProviderConfig",
    "metadata": {"name": "cld055-local"},
    "spec": {
        # The kubernetes back end, as the shared ProviderConfig uses (#4620).
        "configuration": 'terraform {\n  backend "kubernetes" {\n    secret_suffix     = "cld055"\n'
        '    namespace         = "crossplane-system"\n    in_cluster_config = true\n  }\n}\n'
    },
}

workspace = {
    "apiVersion": "opentofu.upbound.io/v1beta1",
    "kind": "Workspace",
    "metadata": {"name": "cld055-unit"},
    "spec": {
        "providerConfigRef": {"name": "cld055-local"},
        "forProvider": {
            "source": "Inline",
            "module": module,
            "vars": [
                {"key": "user_data_b64", "value": base64.b64encode(user_data).decode()},
                {"key": "replicas", "value": "1"},
            ],
        },
    },
}

print(json.dumps({"apiVersion": "v1", "kind": "List", "items": [provider_config, workspace]}, indent=2))
