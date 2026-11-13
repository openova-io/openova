#!/usr/bin/env python3
"""Spike CMP-S4: static inventory of what one region's bootstrap kit declares.

For every HelmRelease listed in clusters/_template/bootstrap-kit/
kustomization.yaml: take the chart from this repository (all 68 are local),
build its dependencies, render it with the HelmRelease's inline values
(`${VAR:=default}` -> default, `${VAR}` -> "placeholder"; valuesFrom is not
read), and count:

  subjects     objects the scanner reports on: Deployment, StatefulSet,
               DaemonSet, CronJob, Job, Pod (hook objects counted apart)
  containers   containers + initContainers of those subjects (one
               vulnerability, SBOM and exposed-secret report each)
  others       objects the scanner's config audit / RBAC assessment and the
               policy engine report on (Service, ConfigMap, Role, ...)
  cnpg         CloudNativePG Cluster objects and their instance count (the
               operator creates one Pod per instance at runtime)

Static rendering is a floor: Pods that operators create at runtime (CNPG
instances, vcluster-synced Pods, Jobs a controller starts) are not in it.
"""
import collections
import glob
import json
import os
import re
import subprocess
import sys
import tempfile

import yaml

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "..", ".."))
KIT = os.path.join(ROOT, "clusters", "_template", "bootstrap-kit")
OUT = os.environ.get("OUT", os.path.join(ROOT, "out"))
SUBJECTS = ("Deployment", "StatefulSet", "DaemonSet", "CronJob", "Job", "Pod", "ReplicaSet")
OTHERS = ("Service", "ConfigMap", "Secret", "ServiceAccount", "Role", "RoleBinding", "ClusterRole",
          "ClusterRoleBinding", "NetworkPolicy", "CiliumNetworkPolicy", "Ingress", "PersistentVolumeClaim",
          "PodDisruptionBudget", "HorizontalPodAutoscaler", "Namespace", "CustomResourceDefinition")
API_VERSIONS = [
    "monitoring.coreos.com/v1", "cert-manager.io/v1", "gateway.networking.k8s.io/v1",
    "gateway.networking.k8s.io/v1beta1", "postgresql.cnpg.io/v1", "cilium.io/v2", "cilium.io/v2alpha1",
    "kyverno.io/v1", "kyverno.io/v2", "external-secrets.io/v1beta1", "external-secrets.io/v1",
    "source.toolkit.fluxcd.io/v1", "helm.toolkit.fluxcd.io/v2", "policy/v1", "autoscaling/v2",
    "networking.k8s.io/v1", "apiextensions.k8s.io/v1", "opentelemetry.io/v1beta1", "opentelemetry.io/v1alpha1",
    "pkg.crossplane.io/v1", "apiextensions.crossplane.io/v1", "velero.io/v1", "autoscaling.k8s.io/v1",
    "snapshot.storage.k8s.io/v1", "aquasecurity.github.io/v1alpha1", "bitnami.com/v1alpha1",
]


# Variables whose value is a list or an object: a string placeholder makes the
# chart's `range` fail. The parent-domain list takes the provisioner's own
# single-zone default (infra/providers/hetzner/main.tf, parent_domains_yaml).
TYPED = {"PARENT_DOMAINS_YAML": '[{name: "t1.omani.works", role: "primary"}]'}


def plain(m):
    v = m.group(1)
    if v in TYPED:
        return TYPED[v]
    if v.endswith("_YAML") or v.endswith("_JSON"):
        return "[]"
    return "placeholder"


def subst(text):
    # innermost first, so ${A:=${B}} becomes ${A:=placeholder} and then placeholder;
    # an empty default becomes "placeholder" (a schema may require the field)
    while True:
        new = re.sub(r"\$\{([A-Za-z0-9_]+)\}", plain, text)
        new = re.sub(r"\$\{([A-Za-z0-9_]+):[=-]([^}$]*)\}",
                     lambda m: m.group(2) if m.group(2) != "" else plain(m), new)
        if new == text:
            return text
        text = new


def chart_dirs():
    m = {}
    for f in glob.glob(os.path.join(ROOT, "platform", "*", "chart", "Chart.yaml")) + \
            glob.glob(os.path.join(ROOT, "products", "*", "chart", "Chart.yaml")):
        try:
            m[yaml.safe_load(open(f))["name"]] = os.path.dirname(f)
        except Exception:
            pass
    return m


def run(cmd, log, timeout=300):
    try:
        r = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout)
    except subprocess.TimeoutExpired:
        log.write(f"TIMEOUT {' '.join(cmd)}\n")
        return 1, ""
    log.write(f"$ {' '.join(cmd)}\n{r.stderr[-3000:]}\n")
    return r.returncode, r.stdout


def build_deps(d, log):
    deps = (yaml.safe_load(open(os.path.join(d, "Chart.yaml"))) or {}).get("dependencies") or []
    if not deps:
        return
    if run(["helm", "dependency", "build", d], log)[0] == 0:
        return
    if run(["helm", "dependency", "update", d], log)[0] == 0:
        return
    os.makedirs(os.path.join(d, "charts"), exist_ok=True)
    for dep in deps:
        repo, name, ver = dep.get("repository", ""), dep["name"], str(dep.get("version", ""))
        if "bitnami" in repo:
            run(["helm", "pull", f"oci://registry-1.docker.io/bitnamicharts/{name}", "--version", ver,
                 "-d", os.path.join(d, "charts")], log)
        elif repo.startswith("file://"):
            run(["helm", "package", os.path.normpath(os.path.join(d, repo[7:])), "-d", os.path.join(d, "charts")], log)
        elif repo.startswith("oci://"):
            run(["helm", "pull", f"{repo}/{name}", "--version", ver, "-d", os.path.join(d, "charts")], log)
        else:
            run(["helm", "pull", name, "--repo", repo, "--version", ver, "-d", os.path.join(d, "charts")], log)


def pod_spec(doc):
    k, s = doc.get("kind"), doc.get("spec") or {}
    if k == "Pod":
        return s
    if k == "CronJob":
        return (((s.get("jobTemplate") or {}).get("spec") or {}).get("template") or {}).get("spec") or {}
    return (s.get("template") or {}).get("spec") or {}


def add_repos(dirs, log):
    seen = set()
    for d in dirs:
        for dep in (yaml.safe_load(open(os.path.join(d, "Chart.yaml"))) or {}).get("dependencies") or []:
            u = dep.get("repository", "")
            if u.startswith("http") and u not in seen:
                seen.add(u)
                run(["helm", "repo", "add", f"dep{len(seen)}", u], log)
    run(["helm", "repo", "update"], log, timeout=600)


def main():
    os.makedirs(OUT, exist_ok=True)
    log = open(os.path.join(OUT, "inventory-render.log"), "w")
    charts = chart_dirs()
    add_repos(sorted(set(charts.values())), log)
    kit = yaml.safe_load(open(os.path.join(KIT, "kustomization.yaml")))
    rows, fails = [], []
    tot = collections.Counter()
    for res in kit["resources"]:
        text = subst(open(os.path.join(KIT, res)).read())
        for hr in yaml.safe_load_all(text):
            if not hr or hr.get("kind") != "HelmRelease":
                continue
            spec = hr["spec"]
            name = spec["chart"]["spec"]["chart"]
            rel = spec.get("releaseName") or hr["metadata"]["name"]
            ns = spec.get("targetNamespace") or hr["metadata"].get("namespace", "default")
            d = charts.get(name)
            if not d:
                fails.append((res, name, "no local chart"))
                continue
            build_deps(d, log)
            with tempfile.NamedTemporaryFile("w", suffix=".yaml", delete=False) as vf:
                yaml.safe_dump(spec.get("values") or {}, vf)
            cmd = ["helm", "template", rel, d, "-n", ns, "-f", vf.name, "--kube-version", "v1.31.4"]
            for av in API_VERSIONS:
                cmd += ["--api-versions", av]
            rc, outp = run(cmd, log)
            if rc != 0:
                fails.append((res, name, "render failed"))
                continue
            c = collections.Counter()
            outp = re.sub(r"[ \t]+$", "", outp, flags=re.M)  # a trailing tab is not valid YAML
            try:
                docs = [x for x in yaml.safe_load_all(outp) if isinstance(x, dict) and x.get("kind")]
            except yaml.YAMLError as e:
                fails.append((res, name, f"yaml: {e}"[:200]))
                continue
            for doc in docs:
                k = doc["kind"]
                hook = "helm.sh/hook" in ((doc.get("metadata") or {}).get("annotations") or {})
                if k in SUBJECTS:
                    ps = pod_spec(doc)
                    n = len(ps.get("containers") or []) + len(ps.get("initContainers") or [])
                    if hook:
                        c["hookSubjects"] += 1
                        c["hookContainers"] += n
                    else:
                        c[f"subject:{k}"] += 1
                        c["subjects"] += 1
                        c["containers"] += n
                        if k in ("Deployment", "StatefulSet"):
                            r = (doc.get("spec") or {}).get("replicas")
                            c["podsDeclared"] += r if isinstance(r, int) else 1
                        elif k == "Pod":
                            c["podsDeclared"] += 1
                        elif k == "DaemonSet":
                            c["daemonSets"] += 1
                elif k == "Cluster" and str(doc.get("apiVersion", "")).startswith("postgresql.cnpg.io"):
                    inst = (doc.get("spec") or {}).get("instances", 1)
                    c["cnpgClusters"] += 1
                    c["cnpgInstances"] += inst if isinstance(inst, int) else 1
                elif k in OTHERS and not hook:
                    c[k] += 1
                c["objects"] += 1
            rows.append({"file": res, "chart": name, "release": rel, "namespace": ns, **c})
            tot.update(c)
    with open(os.path.join(OUT, "inventory.json"), "w") as f:
        json.dump({"rows": rows, "failures": fails, "total": tot}, f, indent=1, sort_keys=True)
    w = sys.stdout.write
    w(f"{'file':44} {'chart':32} {'subj':>5} {'cont':>5} {'pods':>5} {'ds':>3} {'cnpg':>4} {'svc':>4} {'cm':>4} "
      f"{'role':>4} {'crole':>5} {'hook':>4}\n")
    for r in rows:
        w(f"{r['file'][:44]:44} {r['chart'][:32]:32} {r.get('subjects', 0):>5} {r.get('containers', 0):>5} "
          f"{r.get('podsDeclared', 0):>5} {r.get('daemonSets', 0):>3} {r.get('cnpgInstances', 0):>4} "
          f"{r.get('Service', 0):>4} {r.get('ConfigMap', 0):>4} {r.get('Role', 0):>4} {r.get('ClusterRole', 0):>5} "
          f"{r.get('hookSubjects', 0):>4}\n")
    w(f"rendered {len(rows)} HelmReleases, failed {len(fails)}\n")
    for fl in fails:
        w(f"  FAILED {fl}\n")
    w("TOTAL " + json.dumps(dict(sorted(tot.items()))) + "\n")


if __name__ == "__main__":
    main()
