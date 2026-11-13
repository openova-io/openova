#!/usr/bin/env python3
"""SOV-060 spike: render /etc/rancher/k3s/registries.yaml (JSON is valid YAML).

Usage: ROBOT_USER=... ROBOT_PASS=... registries.py MODE

The design form (the born-local node registry rule of the plan): every canonical name
has two endpoints, the Sovereign's own registry first and the origin second,
with the proxy-project rewrite; one entry for the local registry host; one
entry for the origin host (local first, origin second); and a wildcard "*"
whose only endpoint is the local registry. k3s runs with
--disable-default-registry-endpoint.

To measure one behaviour per row on one node, some canonical names get a
different first endpoint (a name that does not resolve, an address that
drops packets, a Harbor alias with a wrong password, stubs that answer 503
or 401). The fall-through mechanism is the same for every name.

MODES
  design     the form above
  e6-edited  design, with the origin removed from quay.io (E6)
  nowild     design without the "*" entry (control C1)
  dragonfly  quay.io: node-local Dragonfly proxy, local registry, origin (E5)
"""
import json
import os
import sys

MODE = sys.argv[1]
LOCAL = "https://harbor.local.test"
LOCAL_BADAUTH = "https://harbor-badauth.local.test"
ORIGIN = "https://origin.parent.test:5443"
DRAGONFLY = "http://127.0.0.1:4001"
CA = "/etc/sov060/ca.crt"
USER = os.environ["ROBOT_USER"]
PASS = os.environ["ROBOT_PASS"]


def mirror(endpoints, project=None):
    entry = {"endpoint": list(endpoints)}
    if project:
        entry["rewrite"] = {"(.*)": project + "/$1"}
    return entry


def tls():
    return {"tls": {"ca_file": CA}}


def login(password=None):
    return {"auth": {"username": USER, "password": password or PASS}, "tls": {"ca_file": CA}}


mirrors = {
    # E1 (local refuses), E2 (empty project), E3 (local holds the image), E7c (Harbor core down)
    "quay.io": mirror([LOCAL, ORIGIN], "proxy-quay"),
    # E2b: the project itself does not exist yet on the local registry
    "ghcr.io": mirror([LOCAL, ORIGIN], "proxy-ghcr"),
    # E1 variant: the first endpoint's name does not resolve
    "gcr.io": mirror(["https://nx.local.test", ORIGIN], "proxy-gcr"),
    # E1 variant: the first endpoint drops packets (connect times out)
    "registry.k8s.io": mirror(["https://blackhole.local.test", ORIGIN], "proxy-k8s"),
    # E1 variant: the first endpoint presents a trusted certificate for other names
    "k8s.gcr.io": mirror(["https://wrongcert.local.test:7445", ORIGIN], "proxy-k8sgcr"),
    # E1 variant: the first endpoint presents a self-signed certificate
    "cgr.dev": mirror(["https://untrusted.local.test:7446", ORIGIN], "proxy-cgr"),
    # E7a: the local Harbor, reached under a name whose login is wrong (401)
    "public.ecr.aws": mirror([LOCAL_BADAUTH, ORIGIN], "proxy-ecr"),
    # E7 extra: the same wrong login against a PRIVATE local project (Harbor's own 401)
    "nvcr.io": mirror([LOCAL_BADAUTH, ORIGIN], "proxy-priv"),
    # E7b: the first endpoint answers 503 to everything
    "docker.io": mirror(["https://stub.local.test:7443", ORIGIN], "proxy-docker"),
    # E7d: the first endpoint answers 401 and refuses every token
    "registry.gitlab.com": mirror(["https://stub.local.test:7444", ORIGIN], "proxy-gitlab"),
    # Not a real registry: k3s renders here the one-endpoint form of quay.io
    # that E6b copies into place on the running node.
    "pivot-render.invalid": mirror([LOCAL], "proxy-quay"),
    # Host entry 1: names addressed through the local registry host
    "harbor.local.test": mirror([LOCAL]),
    # Host entry 2: names addressed through the origin host, local first
    "origin.parent.test:5443": mirror([LOCAL, ORIGIN]),
    # Wildcard: every other name is asked of the local registry only
    "*": mirror([LOCAL]),
}

configs = {
    "harbor.local.test": login(),
    "harbor-badauth.local.test": login("wrong-password"),
    "origin.parent.test:5443": tls(),
    "stub.local.test:7443": tls(),
    "stub.local.test:7444": tls(),
    "nx.local.test": tls(),
    "blackhole.local.test": tls(),
    "wrongcert.local.test:7445": tls(),
    "untrusted.local.test:7446": tls(),
}

if MODE == "design":
    pass
elif MODE == "e6-edited":
    mirrors["quay.io"] = mirror([LOCAL], "proxy-quay")
elif MODE == "nowild":
    del mirrors["*"]
elif MODE == "dragonfly":
    mirrors = {
        "quay.io": mirror([DRAGONFLY, LOCAL, ORIGIN], "proxy-quay"),
        "harbor.local.test": mirror([LOCAL]),
        "origin.parent.test:5443": mirror([LOCAL, ORIGIN]),
        "*": mirror([LOCAL]),
    }
    # No login for the proxy endpoint 127.0.0.1:4001: containerd keys a login
    # by the endpoint host and would present it to whatever token service that
    # endpoint's challenge names. The local projects are public, so none is needed.
    configs = {
        "harbor.local.test": login(),
        "origin.parent.test:5443": tls(),
    }
elif MODE in ("dragonfly-after", "dragonfly-handwritten"):
    # dragonfly-after: the fallback order for "E5 no" (local registry, proxy, origin).
    # dragonfly-handwritten: no quay.io entry at all; its hosts.toml is written
    # by hand, and k3s leaves alone a directory it does not generate.
    mirrors = {
        "quay.io": mirror([LOCAL, DRAGONFLY, ORIGIN], "proxy-quay"),
        "harbor.local.test": mirror([LOCAL]),
        "origin.parent.test:5443": mirror([LOCAL, ORIGIN]),
        "*": mirror([LOCAL]),
    }
    if MODE == "dragonfly-handwritten":
        del mirrors["quay.io"]
    configs = {
        "harbor.local.test": login(),
        "origin.parent.test:5443": tls(),
    }
else:
    sys.exit("unknown mode " + MODE)

print(json.dumps({"mirrors": mirrors, "configs": configs}, indent=2))
