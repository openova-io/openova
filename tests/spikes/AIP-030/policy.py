#!/usr/bin/env python3
"""Policy measurement helpers for lab test AIP-030 (throwaway, spike branch only).

  policy.py enforce VALUES.yaml OUT_OVERRIDES.yaml OUT_CANONICAL.json
      every compliancePolicies.<name>.action is set to Enforce for the measurement;
      the action of each in the chart values is written beside it.
  policy.py canonical RENDERED.yaml    -> {"<ClusterPolicy name>": "<action>"} as rendered
      with bootstrapMode=false (the action a handed-over Sovereign enforces)
  policy.py copy SUFFIX < object.json  -> a create-ready copy named <name>-SUFFIX
  policy.py denials FILE               -> [{"policy","rule"}] from a Kyverno denial text
  policy.py pss FILE                   -> [{"pod","reasons"}] from Pod Security warnings
"""
import json
import re
import sys


def enforce(values, out_overrides, out_canonical):
    import yaml
    v = yaml.safe_load(open(values))
    cp = v.get("compliancePolicies") or {}
    over, canon = {}, {}
    for name, cfg in cp.items():
        if isinstance(cfg, dict) and "action" in cfg:
            canon[name] = cfg.get("action")
            over[name] = {"action": "Enforce"}
    with open(out_overrides, "w") as f:
        yaml.safe_dump({"compliancePolicies": over}, f)
    with open(out_canonical, "w") as f:
        json.dump(canon, f, indent=1)


def canonical(rendered):
    import yaml
    out = {}
    for doc in yaml.safe_load_all(open(rendered)):
        if isinstance(doc, dict) and doc.get("kind") == "ClusterPolicy":
            spec = doc.get("spec") or {}
            out[doc["metadata"]["name"]] = spec.get("validationFailureAction") or "Audit"
    print(json.dumps(out))


def copy(suffix):
    o = json.load(sys.stdin)
    md = o.get("metadata", {})
    keep = {k: md[k] for k in ("namespace", "labels", "annotations") if k in md}
    keep["name"] = (md.get("name", "x")[: 62 - len(suffix)] + "-" + suffix)
    ann = keep.get("annotations") or {}
    ann.pop("kubectl.kubernetes.io/last-applied-configuration", None)
    ann.pop("deployment.kubernetes.io/revision", None)
    keep["annotations"] = ann
    o["metadata"] = keep
    o.pop("status", None)
    print(json.dumps(o))


def denials(path):
    text = open(path, errors="replace").read()
    out, pol = [], None
    if "following policies" not in text:
        print(json.dumps(out))
        return
    for line in text.split("following policies", 1)[1].splitlines():
        m = re.match(r"^([a-z0-9][a-z0-9.-]*):\s*$", line)
        if m:
            pol = m.group(1)
            continue
        m = re.match(r"^\s+([A-Za-z0-9][A-Za-z0-9._-]*):\s", line)
        if m and pol:
            out.append({"policy": pol, "rule": m.group(1)})
    print(json.dumps(out))


def pss(path):
    out = []
    for line in open(path, errors="replace"):
        m = re.match(r"^Warning: ([^:]+): (.*)$", line.strip())
        if m and not m.group(1).startswith("existing pods"):
            out.append({"pod": m.group(1), "reasons": m.group(2)})
    print(json.dumps(out))


if __name__ == "__main__":
    c = sys.argv[1]
    if c == "enforce":
        enforce(*sys.argv[2:5])
    elif c == "canonical":
        canonical(sys.argv[2])
    elif c == "copy":
        copy(sys.argv[2])
    elif c == "denials":
        denials(sys.argv[2])
    elif c == "pss":
        pss(sys.argv[2])
    else:
        sys.exit("unknown command " + c)
