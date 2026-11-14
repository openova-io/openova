#!/usr/bin/env python3
"""check-function-test.py - checks the function test of one chart in a helm render.

The chart contract is docs/RUNBOOKS.md section 4.5 ("Function test of a chart").
A chart's render test pipes `helm template` into this script:

  helm template <release> <chart dir> | python3 scripts/check-function-test.py --chart <name> --release <release>
  helm template ... | python3 scripts/check-function-test.py --chart <name> --release <release> --absent
  python3 scripts/check-function-test.py --self-test

<name> is the `name` of the chart's Chart.yaml. The documents that come from the file
<name>/templates/tests/function-test.yaml are found by the "# Source:" line that helm
template writes above each document. A subchart's file is never one of them.

Rules (default mode), each named in the failure line:
  FT1   the file renders at least one document
  FT2   every document of the file has the annotation helm.sh/hook: test
  FT3   every document of the file has helm.sh/hook-delete-policy: before-hook-creation,hook-succeeded
  FT4   the file renders exactly one Pod, and else only ServiceAccount, Role, RoleBinding,
        NetworkPolicy and CiliumNetworkPolicy
  FT5   the Pod is named <release>-function-test
  FT6   the Pod has the label catalyst.openova.io/function-test: "true"
  FT7   spec.restartPolicy is Never
  FT8   spec.activeDeadlineSeconds is 300
  FT9   every container and init container requests cpu and memory
  FT10  every container and init container names its image with a tag other than latest, or a digest
  FT11  the commands of the Pod contain "FUNCTION-TEST pass <name> " and "FUNCTION-TEST fail <name> "
  FT12  no document outside the file has the label catalyst.openova.io/function-test
With --absent the file must render nothing, and no document may have that label (FT13).

Exit codes: 0 every rule holds; 1 a rule is broken; 2 the input cannot be read.
Needs Python 3 and PyYAML.
"""
import argparse
import re
import sys

ME = "check-function-test"
LABEL = "catalyst.openova.io/function-test"
DELETE_POLICY = "before-hook-creation,hook-succeeded"
HELPERS = ("ServiceAccount", "Role", "RoleBinding", "NetworkPolicy", "CiliumNetworkPolicy")
SOURCE = re.compile(r"^# Source: (.+?)\s*$", re.M)
SPLIT = re.compile(r"^---\s*$", re.M)


class Unreadable(Exception):
    pass


def documents(text):
    """[(source path or "", document)] for every YAML document of a helm render."""
    import yaml
    out = []
    for chunk in SPLIT.split(text):
        if not chunk.strip():
            continue
        m = SOURCE.search(chunk)
        try:
            doc = yaml.safe_load(chunk)
        except yaml.YAMLError as e:
            raise Unreadable("a document cannot be read: %s" % str(e).replace("\n", " "))
        if doc is None:
            continue
        if not isinstance(doc, dict):
            raise Unreadable("a document is not a mapping")
        out.append((m.group(1) if m else "", doc))
    return out


def meta(doc, key):
    v = (doc.get("metadata") or {}).get(key) or {}
    return v if isinstance(v, dict) else {}


def image_ok(image):
    if not isinstance(image, str) or not image:
        return False
    if "@sha256:" in image:
        return True
    last = image.rsplit("/", 1)[-1]
    return ":" in last and last.rsplit(":", 1)[1] not in ("", "latest")


def check(text, chart, release, absent):
    """The list of failure lines; empty when every rule holds."""
    own = "%s/templates/tests/function-test.yaml" % chart
    docs = documents(text)
    mine = [d for s, d in docs if s == own]
    others = [d for s, d in docs if s != own]
    bad = []
    if absent:
        if mine or any(meta(d, "labels").get(LABEL) is not None for _, d in docs):
            bad.append("FT13 %s rendered a function test where it must render nothing" % own)
        return bad
    if not mine:
        return ["FT1 %s rendered nothing" % own]
    for d in mine:
        what = "%s/%s" % (d.get("kind"), (d.get("metadata") or {}).get("name"))
        ann = meta(d, "annotations")
        if str(ann.get("helm.sh/hook", "")).strip() != "test":
            bad.append("FT2 %s has no annotation helm.sh/hook: test" % what)
        if str(ann.get("helm.sh/hook-delete-policy", "")).replace(" ", "") != DELETE_POLICY:
            bad.append("FT3 %s has no annotation helm.sh/hook-delete-policy: %s" % (what, DELETE_POLICY))
    pods = [d for d in mine if d.get("kind") == "Pod"]
    strays = [d.get("kind") for d in mine if d.get("kind") not in HELPERS + ("Pod",)]
    if len(pods) != 1 or strays:
        bad.append("FT4 the file renders %d Pod(s) and these other kinds: %s" % (len(pods), ", ".join(map(str, strays)) or "none"))
    for pod in pods[:1]:
        spec = pod.get("spec") or {}
        name = (pod.get("metadata") or {}).get("name")
        if name != "%s-function-test" % release:
            bad.append("FT5 the Pod is named %r, not %r" % (name, "%s-function-test" % release))
        if str(meta(pod, "labels").get(LABEL)) != "true":
            bad.append('FT6 the Pod has no label %s: "true"' % LABEL)
        if spec.get("restartPolicy") != "Never":
            bad.append("FT7 spec.restartPolicy is %r, not Never" % spec.get("restartPolicy"))
        if spec.get("activeDeadlineSeconds") != 300:
            bad.append("FT8 spec.activeDeadlineSeconds is %r, not 300" % spec.get("activeDeadlineSeconds"))
        containers = (spec.get("initContainers") or []) + (spec.get("containers") or [])
        if not containers:
            bad.append("FT9 the Pod has no container")
        words = []
        for c in containers:
            req = ((c.get("resources") or {}).get("requests") or {})
            if not req.get("cpu") or not req.get("memory"):
                bad.append("FT9 container %s does not request cpu and memory" % c.get("name"))
            if not image_ok(c.get("image")):
                bad.append("FT10 container %s has image %r: no tag, or the tag latest" % (c.get("name"), c.get("image")))
            words += [str(w) for w in (c.get("command") or []) + (c.get("args") or [])]
        joined = "\n".join(words)
        for verdict in ("pass", "fail"):
            if "FUNCTION-TEST %s %s " % (verdict, chart) not in joined:
                bad.append("FT11 the commands never print 'FUNCTION-TEST %s %s <reason>'" % (verdict, chart))
    if any(meta(d, "labels").get(LABEL) is not None for d in others):
        bad.append("FT12 a document outside %s has the label %s" % (own, LABEL))
    return bad


# ---------------------------------------------------------------- self-test

GOOD_POD = """apiVersion: v1
kind: Pod
metadata:
  name: rel-function-test
  labels:
    catalyst.openova.io/function-test: "true"
  annotations:
    helm.sh/hook: test
    helm.sh/hook-delete-policy: before-hook-creation,hook-succeeded
spec:
  restartPolicy: Never
  activeDeadlineSeconds: 300
  containers:
    - name: function-test
      image: registry.example/a/b:1.2
      command: ["/bin/sh", "-c"]
      args:
        - |
          echo "FUNCTION-TEST fail bp-x no answer"
          echo "FUNCTION-TEST pass bp-x answered"
      resources:
        requests:
          cpu: 10m
          memory: 32Mi
"""
GOOD_SA = """apiVersion: v1
kind: ServiceAccount
metadata:
  name: rel-function-test
  annotations:
    helm.sh/hook: test
    helm.sh/hook-delete-policy: before-hook-creation,hook-succeeded
"""
OTHER = """apiVersion: v1
kind: ConfigMap
metadata:
  name: rel-config
"""


def render(*parts):
    """parts: (source path, document text)."""
    return "".join("---\n# Source: %s\n%s" % p for p in parts)


OWN = "bp-x/templates/tests/function-test.yaml"
GOOD = render(("bp-x/templates/config.yaml", OTHER), (OWN, GOOD_SA), (OWN, GOOD_POD))
SUBCHART_TEST = render(("bp-x/charts/up/templates/tests/t.yaml", GOOD_POD.replace(
    '  labels:\n    catalyst.openova.io/function-test: "true"\n', "").replace("rel-function-test", "rel-up-test")))

# (case, render, absent, the rule that must fail; None = must pass)
CASES = [
    ("a good render passes", GOOD, False, None),
    ("a subchart test Pod without the label is ignored", GOOD + SUBCHART_TEST, False, None),
    ("a NetworkPolicy beside the Pod is allowed", GOOD + render((OWN, GOOD_SA.replace("apiVersion: v1\nkind: ServiceAccount", "apiVersion: networking.k8s.io/v1\nkind: NetworkPolicy"))), False, None),
    ("FT1 no function test rendered", render(("bp-x/templates/config.yaml", OTHER)), False, "FT1"),
    ("FT1 a subchart's file is not the chart's file", render(
        ("bp-x/charts/up/templates/tests/function-test.yaml", GOOD_POD)), False, "FT1"),
    ("FT2 a document without the test hook", GOOD.replace("helm.sh/hook: test\n    helm.sh/hook-delete", "helm.sh/hook: post-install\n    helm.sh/hook-delete", 1), False, "FT2"),
    ("FT3 a document without the delete policy", GOOD.replace("before-hook-creation,hook-succeeded", "before-hook-creation", 1), False, "FT3"),
    ("FT4 two Pods", GOOD + render((OWN, GOOD_POD)), False, "FT4"),
    ("FT4 a Secret in the file", GOOD + render((OWN, GOOD_SA.replace("kind: ServiceAccount", "kind: Secret"))), False, "FT4"),
    ("FT5 a wrong Pod name", GOOD.replace("name: rel-function-test\n  labels", "name: rel-test\n  labels"), False, "FT5"),
    ("FT6 the Pod without the label", GOOD.replace('function-test: "true"', 'function-test: "false"'), False, "FT6"),
    ("FT7 restartPolicy OnFailure", GOOD.replace("restartPolicy: Never", "restartPolicy: OnFailure"), False, "FT7"),
    ("FT8 a deadline of 600", GOOD.replace("activeDeadlineSeconds: 300", "activeDeadlineSeconds: 600"), False, "FT8"),
    ("FT9 no memory request", GOOD.replace("          memory: 32Mi\n", ""), False, "FT9"),
    ("FT10 the tag latest", GOOD.replace("a/b:1.2", "a/b:latest"), False, "FT10"),
    ("FT10 no tag", GOOD.replace("a/b:1.2", "a/b"), False, "FT10"),
    ("FT11 no fail line", GOOD.replace("FUNCTION-TEST fail bp-x", "echo failed"), False, "FT11"),
    ("FT11 the pass line names another chart", GOOD.replace("FUNCTION-TEST pass bp-x", "FUNCTION-TEST pass bp-y"), False, "FT11"),
    ("FT12 a second labelled Pod outside the file", GOOD + render(("bp-x/templates/other.yaml", GOOD_POD)), False, "FT12"),
    ("FT13 absent: nothing rendered passes", render(("bp-x/templates/config.yaml", OTHER)), True, None),
    ("FT13 absent: a rendered test fails", GOOD, True, "FT13"),
]


def self_test():
    failed = 0
    for name, text, absent, rule in CASES:
        bad = check(text, "bp-x", "rel", absent)
        if rule is None:
            good = not bad
        else:
            good = any(line.startswith(rule + " ") for line in bad)
        failed += not good
        print("%s %s%s" % ("ok" if good else "FAIL", name, "" if good else ": " + "; ".join(bad)))
    try:
        check("a: [\n", "bp-x", "rel", False)
        print("FAIL input that is not YAML is refused")
        failed += 1
    except Unreadable:
        print("ok input that is not YAML is refused")
    total = len(CASES) + 1
    if failed:
        print("%s: self-test FAILED: %d of %d cases not as expected" % (ME, failed, total))
        return 1
    print("%s: self-test passed: %d of %d cases as expected" % (ME, total, total))
    return 0


def main(argv):
    p = argparse.ArgumentParser(description="Checks the function test of one chart in a helm render read from stdin.")
    p.add_argument("--chart", help="the name of the chart, as in its Chart.yaml")
    p.add_argument("--release", help="the release name given to helm template")
    p.add_argument("--absent", action="store_true", help="the function test must render nothing")
    p.add_argument("--self-test", action="store_true")
    a = p.parse_args(argv)
    if a.self_test:
        return self_test()
    if not a.chart or not a.release:
        p.error("--chart and --release are required")
    try:
        bad = check(sys.stdin.read(), a.chart, a.release, a.absent)
    except (Unreadable, ImportError) as e:
        print("%s: cannot judge: %s" % (ME, e))
        return 2
    for line in bad:
        print("%s: FAIL %s" % (ME, line))
    if bad:
        return 1
    print("%s: ok %s release %s: %s" % (ME, a.chart, a.release, "no function test, as expected" if a.absent else "the function test meets the contract"))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
