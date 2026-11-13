#!/usr/bin/env python3
"""Spike CMP-S4 workload set: real images that platform charts pin today.

Each image reference is the chart's pin with the Harbor proxy prefix replaced
by the upstream registry it proxies (the runner cannot reach the platform's
Harbor). One namespace per component, as a Blueprint gets its own; in each a
ServiceAccount, a Role, a RoleBinding, a ConfigMap and a Service, as a chart
ships them. Containers run `sleep` where the image has a shell; images without
one crash-loop, which does not change what the scanner or the policy engine
report (both read the workload object, not the running process).

  --images              print the image references, one per line
  --available FILE      print the manifests for the workloads whose images are
                        all listed in FILE (one reference per line)
  --expected FILE       print the number of containers (vulnerability reports
                        expected) for those workloads
"""
import argparse
import sys

import yaml

# (name, kind, [(container, image)], [(initContainer, image)], source in the repo)
WORKLOADS = [
    ("keycloak", "Deployment", [("keycloak", "docker.io/bitnamilegacy/keycloak:26.3.3-debian-12-r0")],
     [("volume-permissions", "docker.io/bitnamilegacy/os-shell:12-debian-12-r50")], "platform/keycloak"),
    ("grafana", "Deployment", [("grafana", "docker.io/grafana/grafana:12.3.1")], [], "platform/grafana"),
    ("harbor-core", "Deployment", [("core", "docker.io/goharbor/harbor-core:v2.14.3")], [], "platform/harbor"),
    ("harbor-trivy", "Deployment", [("trivy", "docker.io/goharbor/trivy-adapter-photon:v2.14.3")], [], "platform/harbor"),
    ("cnpg", "Deployment", [("manager", "ghcr.io/cloudnative-pg/cloudnative-pg:1.29.0")], [], "platform/cnpg"),
    ("otel", "Deployment", [("collector", "docker.io/otel/opentelemetry-collector-contrib:0.150.1")], [], "platform/opentelemetry"),
    ("oidc-gate", "Deployment", [("oauth2-proxy", "quay.io/oauth2-proxy/oauth2-proxy:v7.6.0")], [], "platform/oidc-gate"),
    ("reloader", "Deployment", [("reloader", "ghcr.io/stakater/reloader:v1.4.16")], [], "platform/reloader"),
    ("velero", "Deployment", [("velero", "docker.io/velero/velero:v1.18.0")], [], "platform/velero-hcs"),
    ("catalyst-api", "Deployment", [("api", "ghcr.io/openova-io/openova/catalyst-api:43ecb86")], [], "products/catalyst"),
    ("catalyst-ui", "Deployment", [("ui", "ghcr.io/openova-io/openova/catalyst-ui:43ecb86")], [], "products/catalyst"),
    ("postgres", "StatefulSet", [("postgresql", "docker.io/bitnamilegacy/postgresql:17.6.0-debian-12-r0"),
                                 ("metrics", "docker.io/bitnamilegacy/postgres-exporter:0.17.1-debian-12-r15")], [],
     "platform/keycloak (postgresql subchart)"),
    ("valkey", "StatefulSet", [("valkey", "docker.io/bitnamilegacy/valkey:8.1.3-debian-12-r3")], [], "platform/valkey"),
    ("loki", "StatefulSet", [("loki", "docker.io/grafana/loki:3.6.7")], [], "platform/loki"),
    ("vcluster", "StatefulSet", [("syncer", "ghcr.io/loft-sh/vcluster:0.23.0")], [], "platform/bp-rtz-vcluster"),
    ("openbao", "StatefulSet", [("openbao", "quay.io/openbao/openbao:2.1.0")], [], "platform/openbao"),
    ("stalwart", "StatefulSet", [("stalwart", "docker.io/stalwartlabs/stalwart:v0.15.5")], [], "platform/stalwart-tenant"),
    ("falco", "DaemonSet", [("falco", "docker.io/falcosecurity/falco:0.43.1")], [], "platform/falco"),
    ("alloy", "DaemonSet", [("alloy", "docker.io/grafana/alloy:v1.16.0"),
                            ("config-reloader", "quay.io/prometheus-operator/prometheus-config-reloader:v0.81.0")], [],
     "platform/alloy"),
    ("ops", "CronJob", [("kubectl", "docker.io/alpine/k8s:1.31.4")], [], "platform/self-sovereign-cutover"),
    ("landing", "Pod", [("nginx", "docker.io/library/nginx:1.27-alpine")], [], "platform/openbao (ssoLanding)"),
]

# Deployments rolled three times in phase R (pod-template annotation change)
ROLLED = ["grafana", "keycloak", "harbor-core"]  # images with a shell: the pods run, so each roll completes


def container(name, image):
    return {"name": name, "image": image, "imagePullPolicy": "IfNotPresent",
            "command": ["sh", "-c", "sleep 2147483647"],
            "resources": {"requests": {"cpu": "1m", "memory": "8Mi"}}}


def pod_spec(name, cs, ics, restart="Always"):
    s = {"serviceAccountName": name, "restartPolicy": restart, "terminationGracePeriodSeconds": 1,
         "containers": [container(c, i) for c, i in cs]}
    if ics:
        s["initContainers"] = [dict(container(c, i), command=["sh", "-c", "true"]) for c, i in ics]
    return s


def manifests(w):
    name, kind, cs, ics, src = w
    ns = f"s4-{name}"
    lab = {"app": name, "cmps4": "workload"}
    out = [
        {"apiVersion": "v1", "kind": "Namespace", "metadata": {"name": ns, "labels": {"cmps4": "workload"}}},
        {"apiVersion": "v1", "kind": "ServiceAccount", "metadata": {"name": name, "namespace": ns}},
        {"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "Role", "metadata": {"name": name, "namespace": ns},
         "rules": [{"apiGroups": [""], "resources": ["configmaps", "secrets"], "verbs": ["get", "list", "watch"]}]},
        {"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "RoleBinding", "metadata": {"name": name, "namespace": ns},
         "roleRef": {"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": name},
         "subjects": [{"kind": "ServiceAccount", "name": name, "namespace": ns}]},
        {"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": f"{name}-config", "namespace": ns},
         "data": {"source": src, "config.yaml": "log:\n  level: info\n"}},
    ]
    if kind != "CronJob":
        out.append({"apiVersion": "v1", "kind": "Service", "metadata": {"name": name, "namespace": ns},
                    "spec": {"selector": {"app": name}, "ports": [{"port": 8080, "targetPort": 8080}]}})
    tmpl = {"metadata": {"labels": lab}, "spec": pod_spec(name, cs, ics)}
    md = {"name": name, "namespace": ns, "labels": lab}
    if kind == "Deployment":
        out.append({"apiVersion": "apps/v1", "kind": kind, "metadata": md,
                    "spec": {"replicas": 1, "selector": {"matchLabels": {"app": name}}, "template": tmpl}})
    elif kind == "StatefulSet":
        out.append({"apiVersion": "apps/v1", "kind": kind, "metadata": md,
                    "spec": {"replicas": 1, "serviceName": name, "selector": {"matchLabels": {"app": name}},
                             "template": tmpl}})
    elif kind == "DaemonSet":
        out.append({"apiVersion": "apps/v1", "kind": kind, "metadata": md,
                    "spec": {"selector": {"matchLabels": {"app": name}}, "template": tmpl}})
    elif kind == "CronJob":
        js = pod_spec(name, [(c, i) for c, i in cs], ics, restart="Never")
        js["containers"][0]["command"] = ["sh", "-c", "date"]
        out.append({"apiVersion": "batch/v1", "kind": kind, "metadata": md,
                    "spec": {"schedule": "*/30 * * * *", "successfulJobsHistoryLimit": 1, "failedJobsHistoryLimit": 1,
                             "jobTemplate": {"spec": {"template": {"metadata": {"labels": lab}, "spec": js}}}}})
    elif kind == "Pod":
        out.append({"apiVersion": "v1", "kind": "Pod", "metadata": md, "spec": pod_spec(name, cs, ics)})
    return out


def available(path):
    have = {l.strip() for l in open(path) if l.strip()}
    return [w for w in WORKLOADS if all(i in have for _, i in w[2] + w[3])]


def main():
    ap = argparse.ArgumentParser()
    g = ap.add_mutually_exclusive_group(required=True)
    g.add_argument("--images", action="store_true")
    g.add_argument("--available")
    g.add_argument("--expected")
    g.add_argument("--rolled")
    a = ap.parse_args()
    if a.images:
        for i in sorted({i for w in WORKLOADS for _, i in w[2] + w[3]}):
            print(i)
    elif a.available:
        docs = [d for w in available(a.available) for d in manifests(w)]
        yaml.safe_dump_all(docs, sys.stdout, sort_keys=False)
    elif a.expected:
        print(sum(len(w[2]) + len(w[3]) for w in available(a.expected)))
    elif a.rolled:
        names = {w[0] for w in available(a.rolled)}
        print(" ".join(n for n in ROLLED if n in names))


if __name__ == "__main__":
    main()
