#!/usr/bin/env python3
"""check-policy-wiring.py - kit slot 27a wires the policy chart to its values document and the hand-over variable.

The slot clusters/_template/bootstrap-kit/27a-kyverno-policies.yaml holds exactly one HelmRelease named
bp-kyverno-policies. Four rules hold for it:
  values-document  spec.valuesFrom has exactly one entry named compliance-policy-values, and that entry is
                   exactly {kind: ConfigMap, name: compliance-policy-values, valuesKey: values.yaml,
                   optional: true}. Other entries are allowed.
  handover         spec.values.compliancePolicies.sovereignConsume is the text ${SOVEREIGN_CONSUME:=origin}.
  install-crds     spec.install.crds is CreateReplace.
  upgrade-crds     spec.upgrade.crds is CreateReplace.

Usage:
  python3 scripts/check-policy-wiring.py              judge the slot
  python3 scripts/check-policy-wiring.py --self-test  prove that every rule can fail
  --root <dir>  judge another checkout (default: the one this script lies in)
Exit codes: 0 every rule holds; 1 a rule is broken, or the slot cannot be read; 2 wrong usage.
Needs Python 3 and PyYAML.
"""
import argparse
import os
import sys
import tempfile

SLOT = "clusters/_template/bootstrap-kit/27a-kyverno-policies.yaml"
RELEASE = "bp-kyverno-policies"
DOCUMENT = {"kind": "ConfigMap", "name": "compliance-policy-values", "valuesKey": "values.yaml", "optional": True}
HANDOVER = "${SOVEREIGN_CONSUME:=origin}"


def findings(root):
    """Return the list of findings for the slot under root. An empty list means every rule holds."""
    import yaml  # imported here so that a missing library is one finding, not a crash
    path = os.path.join(root, SLOT)
    try:
        with open(path, encoding="utf-8") as f:
            docs = [d for d in yaml.safe_load_all(f) if d is not None]
    except (OSError, yaml.YAMLError) as e:
        return ["slot: %s cannot be read: %s" % (SLOT, str(e).splitlines()[0])]
    hrs = [d for d in docs if isinstance(d, dict) and d.get("kind") == "HelmRelease"
           and isinstance(d.get("metadata"), dict) and d["metadata"].get("name") == RELEASE]
    if len(hrs) != 1:
        return ["slot: %s holds %d HelmRelease objects named %s, want 1" % (SLOT, len(hrs), RELEASE)]
    spec = hrs[0].get("spec") if isinstance(hrs[0].get("spec"), dict) else {}
    out = []
    entries = spec.get("valuesFrom") if isinstance(spec.get("valuesFrom"), list) else []
    named = [e for e in entries if isinstance(e, dict) and e.get("name") == DOCUMENT["name"]]
    if len(named) != 1:
        out.append("values-document: spec.valuesFrom has %d entries named %s, want 1" % (len(named), DOCUMENT["name"]))
    elif named[0] != DOCUMENT or named[0].get("optional") is not True:
        out.append("values-document: the entry %s is %r, want %r" % (DOCUMENT["name"], named[0], DOCUMENT))
    values = spec.get("values") if isinstance(spec.get("values"), dict) else {}
    policies = values.get("compliancePolicies") if isinstance(values.get("compliancePolicies"), dict) else {}
    if policies.get("sovereignConsume") != HANDOVER:
        out.append("handover: spec.values.compliancePolicies.sovereignConsume is %r, want %r"
                   % (policies.get("sovereignConsume"), HANDOVER))
    for block in ("install", "upgrade"):
        b = spec.get(block) if isinstance(spec.get(block), dict) else {}
        if b.get("crds") != "CreateReplace":
            out.append("%s-crds: spec.%s.crds is %r, want 'CreateReplace'" % (block, block, b.get("crds")))
    return out


def judge(root):
    try:
        out = findings(root)
    except ImportError:
        out = ["slot: PyYAML is not installed, nothing was judged"]
    for line in out:
        print("check-policy-wiring: FAIL, %s" % line)
    if out:
        return 1
    print("check-policy-wiring: ok, slot 27a reads compliance-policy-values, passes SOVEREIGN_CONSUME "
          "and replaces definitions on install and upgrade")
    return 0


GOOD = """# comment
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-kyverno-policies
  namespace: flux-system
spec:
  values:
    compliancePolicies:
      sovereignConsume: ${SOVEREIGN_CONSUME:=origin}
  valuesFrom:
    - kind: ConfigMap
      name: compliance-policy-values
      valuesKey: values.yaml
      optional: true
  install:
    disableWait: true
    crds: CreateReplace
  upgrade:
    disableWait: true
    crds: CreateReplace
"""

# (name, text of the slot or None for no file, the rule a finding must name or None for a pass)
CASES = [
    ("the good slot passes", GOOD, None),
    ("another valuesFrom entry is allowed", GOOD.replace("  valuesFrom:\n", "  valuesFrom:\n    - kind: Secret\n      name: other\n"), None),
    ("no slot file", None, "slot"),
    ("a slot that is not YAML", GOOD + "  : [\n", "slot"),
    ("no HelmRelease of that name", GOOD.replace("name: bp-kyverno-policies", "name: bp-kyverno"), "slot"),
    ("two HelmReleases of that name", GOOD + "---\n" + GOOD.split("---\n")[1], "slot"),
    ("no valuesFrom", GOOD.replace("  valuesFrom:\n", "  valuesFromX:\n"), "values-document"),
    ("the entry twice", GOOD.replace("  install:\n", "    - kind: ConfigMap\n      name: compliance-policy-values\n"
                                     "      valuesKey: values.yaml\n      optional: true\n  install:\n"), "values-document"),
    ("the entry is a Secret", GOOD.replace("    - kind: ConfigMap", "    - kind: Secret"), "values-document"),
    ("another values key", GOOD.replace("valuesKey: values.yaml", "valuesKey: values.yml"), "values-document"),
    ("not optional", GOOD.replace("optional: true", "optional: false"), "values-document"),
    ("optional as text", GOOD.replace("optional: true", 'optional: "true"'), "values-document"),
    ("the entry has a target path", GOOD.replace("optional: true", "optional: true\n      targetPath: compliancePolicies"), "values-document"),
    ("no hand-over value", GOOD.replace("      sovereignConsume: ${SOVEREIGN_CONSUME:=origin}\n", "      other: x\n"), "handover"),
    ("another default", GOOD.replace(":=origin}", ":=local}"), "handover"),
    ("another variable", GOOD.replace("${SOVEREIGN_CONSUME", "${SOVEREIGN_MODE"), "handover"),
    ("a fixed value", GOOD.replace("${SOVEREIGN_CONSUME:=origin}", "origin"), "handover"),
    ("no crds on install", GOOD.replace("    disableWait: true\n    crds: CreateReplace\n  upgrade:", "    disableWait: true\n  upgrade:"), "install-crds"),
    ("crds Create on install", GOOD.replace("    crds: CreateReplace\n  upgrade:", "    crds: Create\n  upgrade:"), "install-crds"),
    ("no crds on upgrade", GOOD[:GOOD.rindex("    crds: CreateReplace\n")], "upgrade-crds"),
    ("crds Skip on upgrade", GOOD[:GOOD.rindex("    crds: CreateReplace\n")] + "    crds: Skip\n", "upgrade-crds"),
]


def self_test():
    passed = failed = 0
    for name, text, rule in CASES:
        with tempfile.TemporaryDirectory() as root:
            if text is not None:
                os.makedirs(os.path.join(root, os.path.dirname(SLOT)))
                with open(os.path.join(root, SLOT), "w", encoding="utf-8") as f:
                    f.write(text)
            out = findings(root)
        ok = (not out) if rule is None else (len(out) >= 1 and all(o.startswith(rule + ":") for o in out))
        if ok:
            passed += 1
        else:
            failed += 1
            print("self-test: FAIL, case %r: want %s, got %r" % (name, "a pass" if rule is None else "a finding of rule " + rule, out))
    print("self-test: %d cases passed, %d failed" % (passed, failed))
    return 0 if failed == 0 else 1


def main(argv):
    p = argparse.ArgumentParser(prog="check-policy-wiring.py")
    p.add_argument("--self-test", action="store_true")
    p.add_argument("--root", default=os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
    a = p.parse_args(argv)
    return self_test() if a.self_test else judge(a.root)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
