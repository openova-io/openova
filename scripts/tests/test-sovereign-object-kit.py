#!/usr/bin/env python3
"""test-sovereign-object-kit.py - the writer of the Sovereign object renders in every region (SOV-175).

Renders the bootstrap kit (clusters/_template/bootstrap-kit) twice, with the values of the primary region and with
the values of a follower region, and the folder clusters/_template/sovereign-object twice, before and after the
parent wrote the network registry. It fails unless:

  K1  bp-catalyst-platform (slot 13), which ships the Sovereign kind, is suspended in the follower and not in the
      primary: the gap this slot closes.
  K2  in both regions the kit holds exactly one Flux Kustomization flux-system/sovereign-object; it is not suspended,
      applies ./clusters/_template/sovereign-object from the GitRepository openova with prune, force and without
      wait, substitutes from the ConfigMap sovereign-network-registry, optional, sets at least one inline variable
      and none of the registry's keys inline (an inline variable overrides the ConfigMap), and pins the image
      ghcr.io/openova-io/openova/sovereign-ops to a commit id.
  K3  the folder renders the Sovereign kind as the file products/catalyst/chart/crds/sovereign.yaml with exactly the
      two annotations prune disabled and substitute disabled, a service account, its rights and one Job; nothing else.
  K4  the rights are those the writer uses: sovereigns get, list, create, patch; sovereigns/status get, patch; get of
      the ConfigMaps sovereign-variables and sovereign-network-registry of flux-system.
  K5  the Job runs `sovereign-object` in the image the Kustomization pins, as the service account, with restartPolicy
      Never and a failure policy that ends the Job on exit code 1; it is named sovereign-object-bootstrap before the
      registry exists and sovereign-object-<revision> after.

The substitution is Flux's post-build step (kustomize-controller v1.4.0, fluxcd/pkg kustomize/kustomize_varsub.go):
the variables of the folder are the ConfigMap's keys when it exists, then the inline variables over them; with no
variable at all nothing is substituted, so a default never applies (hw307, 16 Nov 2026, #7936). ${NAME} is the
value or the empty text; ${NAME:=default}, ${NAME:-default}, ${NAME=default}, ${NAME-default} are the value when it
is not empty, else the default; a document with the annotation kustomize.toolkit.fluxcd.io/substitute: disabled is
taken as it is. scripts/tests/test-kit-flux-substitution.py holds the full rule and applies it to the whole kit.
The folder's kustomization.yaml may hold only resources (files) and patches (strategic merge, one object, no
target); anything else fails the test.

    python3 scripts/tests/test-sovereign-object-kit.py      exit 0 pass, 1 fail
Needs Python 3 and PyYAML.
"""
import copy
import os
import re
import sys

import yaml

ROOT = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", ".."))
KIT = "clusters/_template/bootstrap-kit"
FOLDER = "clusters/_template/sovereign-object"
CRD = "products/catalyst/chart/crds/sovereign.yaml"
IMAGE = "ghcr.io/openova-io/openova/sovereign-ops"
DISABLED = "kustomize.toolkit.fluxcd.io/substitute"
VAR = re.compile(r"\$\{([A-Za-z_][A-Za-z0-9_]*)(?:(:?[=-])((?:[^{}]|\{[^{}]*\})*))?\}")
# What the parent writes into the ConfigMap sovereign-network-registry (lifecycle/network_registry.go).
REGISTRY = {"NETWORK_REGISTRY_REVISION": "3f2a9c1b0d4e", "NETWORK_REGISTRY_JSON": "{}"}
fails = []


def check(cond, what):
    print(("PASS  " if cond else "FAIL  ") + what)
    if not cond:
        fails.append(what)


def subst(text, values):
    text = text.replace("$$", "\0")
    for _ in range(5):
        def one(m):
            name, op, default = m.group(1), m.group(2), m.group(3)
            v = values.get(name)
            if op:
                return v if v else default
            return v or ""
        new = VAR.sub(one, text)
        if new == text:
            break
        text = new
    return text.replace("\0", "$")


def render(docs, values):
    out = []
    for d in docs:
        ann = ((d.get("metadata") or {}).get("annotations") or {})
        if ann.get(DISABLED) == "disabled" or not values:
            out.append(d)
        else:
            out.append(yaml.safe_load(subst(yaml.safe_dump(d, sort_keys=True), values)))
    return out


def load(path):
    with open(os.path.join(ROOT, path)) as f:
        return [d for d in yaml.safe_load_all(f) if d]


def env(path):
    values = {}
    with open(os.path.join(ROOT, path)) as f:
        for line in f:
            line = line.rstrip("\n")
            if line and not line.startswith("#") and "=" in line:
                k, v = line.split("=", 1)
                values[k] = v
    return values


def merge(dst, src):
    for k, v in src.items():
        if isinstance(v, dict) and isinstance(dst.get(k), dict):
            merge(dst[k], v)
        else:
            dst[k] = copy.deepcopy(v)


def build_folder():
    k = load(FOLDER + "/kustomization.yaml")[0]
    extra = set(k) - {"apiVersion", "kind", "resources", "patches"}
    check(not extra, "K3 the folder's kustomization.yaml holds only resources and patches (extra: %s)" % sorted(extra))
    docs = []
    for r in k.get("resources") or []:
        p = os.path.normpath(os.path.join(FOLDER, r))
        check(os.path.isfile(os.path.join(ROOT, p)), "K3 the resource %s is a file of the repository" % r)
        docs += load(p)
    for p in k.get("patches") or []:
        check(set(p) == {"patch"}, "K3 a patch is one strategic merge patch without a target")
        patch = yaml.safe_load(p["patch"])
        hit = [d for d in docs if d.get("kind") == patch.get("kind") and d["metadata"]["name"] == patch["metadata"]["name"]]
        check(len(hit) == 1, "K3 the patch of %s/%s matches one object" % (patch.get("kind"), patch["metadata"]["name"]))
        for d in hit:
            merge(d, patch)
    return docs


def kit_docs(values):
    k = load(KIT + "/kustomization.yaml")[0]
    out = {}
    for r in k["resources"]:
        out[r] = render(load(KIT + "/" + r), values)
    return out


def main():
    primary = env("tests/fixtures/variables/today.env")
    follower = dict(primary, SOVEREIGN_REGION_ROLE="secondary", SECONDARY_HR_SUSPEND="true", SOVEREIGN_REGION_ID="secondary")
    inline = {}
    for region, values in (("primary", primary), ("follower", follower)):
        docs = kit_docs(values)
        slot13 = [d for d in docs.get("13-bp-catalyst-platform.yaml", []) if d.get("kind") == "HelmRelease"]
        suspended = bool(slot13) and slot13[0]["spec"].get("suspend") is True
        check(bool(slot13) and suspended == (region == "follower"),
              "K1 %s: bp-catalyst-platform, which ships the Sovereign kind, is %s" % (region, "suspended" if region == "follower" else "not suspended"))
        ks = [(f, d) for f, ds in docs.items() for d in ds if d.get("kind") == "Kustomization"
              and d.get("apiVersion") == "kustomize.toolkit.fluxcd.io/v1" and d["metadata"].get("name") == "sovereign-object"]
        check(len(ks) == 1, "K2 %s: the kit holds one Kustomization sovereign-object (found %d)" % (region, len(ks)))
        if len(ks) != 1:
            continue
        s = ks[0][1]["spec"]
        check(ks[0][1]["metadata"].get("namespace") == "flux-system", "K2 %s: it is in flux-system" % region)
        check(s.get("suspend") in (None, False), "K2 %s: it is not suspended" % region)
        check(s.get("path") == "./" + FOLDER and s.get("sourceRef") == {"kind": "GitRepository", "name": "openova"},
              "K2 %s: it applies ./%s from the GitRepository openova" % (region, FOLDER))
        check(s.get("prune") is True and s.get("force") is True and s.get("wait") is False, "K2 %s: prune, force, no wait" % region)
        check(((s.get("postBuild") or {}).get("substituteFrom") or []) == [{"kind": "ConfigMap", "name": "sovereign-network-registry", "optional": True}],
              "K2 %s: it substitutes from the optional ConfigMap sovereign-network-registry only" % region)
        own = (s.get("postBuild") or {}).get("substitute") or {}
        check(bool(own) and all(isinstance(v, str) for v in own.values()) and not set(own) & set(REGISTRY),
              "K2 %s: it sets an inline variable, so Flux substitutes before the registry exists, and none of the "
              "registry's keys %s (found %s)" % (region, sorted(REGISTRY), sorted(own)))
        inline[region] = own
        imgs = s.get("images") or []
        check(len(imgs) == 1 and imgs[0].get("name") == IMAGE and re.fullmatch(r"[0-9a-f]{7,40}", str(imgs[0].get("newTag"))) is not None
              and set(imgs[0]) == {"name", "newTag"}, "K2 %s: it pins %s to a commit id" % (region, IMAGE))

    folder = build_folder()
    with open(os.path.join(ROOT, CRD)) as f:
        crd = yaml.safe_load(f)
    want = copy.deepcopy(crd)
    want["metadata"]["annotations"] = {"kustomize.toolkit.fluxcd.io/prune": "disabled", DISABLED: "disabled"}
    check(inline.get("primary") == inline.get("follower"), "K2 the inline variables are the same in both regions")
    own = inline.get("primary") or {}
    for case, values, job in (("before the registry", dict(own), "sovereign-object-bootstrap"),
                              ("after the registry", dict(REGISTRY, **own), "sovereign-object-3f2a9c1b0d4e")):
        docs = render(folder, values)
        kinds = sorted("%s/%s" % (d["kind"], d["metadata"]["name"]) for d in docs)
        check(kinds == sorted(["CustomResourceDefinition/sovereigns.catalyst.openova.io", "ServiceAccount/sovereign-object",
                               "ClusterRole/sovereign-object", "ClusterRoleBinding/sovereign-object", "Role/sovereign-object",
                               "RoleBinding/sovereign-object", "Job/" + job]), "K3 %s: the folder renders exactly its objects: %s" % (case, kinds))
        by = {d["kind"]: d for d in docs}
        check(by.get("CustomResourceDefinition") == want, "K3 %s: the Sovereign kind is %s with the two annotations" % (case, CRD))
        cr = by.get("ClusterRole", {}).get("rules")
        check(cr == [{"apiGroups": ["catalyst.openova.io"], "resources": ["sovereigns"], "verbs": ["get", "list", "create", "patch"]},
                     {"apiGroups": ["catalyst.openova.io"], "resources": ["sovereigns/status"], "verbs": ["get", "patch"]}],
              "K4 %s: the cluster rights are the writer's" % case)
        role = by.get("Role", {})
        check(role.get("metadata", {}).get("namespace") == "flux-system" and role.get("rules") == [
            {"apiGroups": [""], "resources": ["configmaps"], "resourceNames": ["sovereign-variables", "sovereign-network-registry"], "verbs": ["get"]}],
            "K4 %s: the ConfigMap rights are the writer's" % case)
        for b in ("ClusterRoleBinding", "RoleBinding"):
            check(by.get(b, {}).get("subjects") == [{"kind": "ServiceAccount", "name": "sovereign-object", "namespace": "flux-system"}],
                  "K4 %s: the %s names the service account" % (case, b))
        j = by.get("Job", {})
        pod = j.get("spec", {}).get("template", {}).get("spec", {})
        c = (pod.get("containers") or [{}])
        check(j.get("metadata", {}).get("namespace") == "flux-system" and len(c) == 1 and c[0].get("image") == IMAGE
              and c[0].get("args") == ["sovereign-object"] and pod.get("serviceAccountName") == "sovereign-object" and pod.get("restartPolicy") == "Never",
              "K5 %s: the Job runs sovereign-object in %s as the service account" % (case, IMAGE))
        check(j.get("spec", {}).get("podFailurePolicy") == {"rules": [{"action": "FailJob", "onExitCodes": {
            "containerName": "sovereign-object", "operator": "In", "values": [1]}}]}, "K5 %s: exit code 1 ends the Job" % case)
    print("%d failed" % len(fails) if fails else "all passed")
    return 1 if fails else 0


if __name__ == "__main__":
    sys.exit(main())
