#!/usr/bin/env python3
"""Spike CLD-050 E7 fallback: convert a mode-Resources Composition to
mode Pipeline with one function-patch-and-transform step whose input
`resources` is the existing list (the CLD-064 recipe). Used only when the
Crossplane CLI's `beta convert pipeline-composition` is unavailable.
Sets the defaults the function needs explicitly: patch type
FromCompositeFieldPath, string transform type Format.

convert.py IN.yaml OUT.yaml
"""
import sys

import yaml


def main():
    comp = yaml.safe_load(open(sys.argv[1]))
    spec = comp["spec"]
    resources = spec.pop("resources")
    for res in resources:
        for p in res.get("patches", []) or []:
            p.setdefault("type", "FromCompositeFieldPath")
            for t in p.get("transforms", []) or []:
                if t.get("type") == "string" and "fmt" in t.get("string", {}):
                    t["string"].setdefault("type", "Format")
    step_input = {"apiVersion": "pt.fn.crossplane.io/v1beta1", "kind": "Resources", "resources": resources}
    for key in ("patchSets", "environment"):
        if key in spec:
            step_input[key] = spec.pop(key)
    spec["mode"] = "Pipeline"
    spec["pipeline"] = [{
        "step": "patch-and-transform",
        "functionRef": {"name": "function-patch-and-transform"},
        "input": step_input,
    }]
    yaml.safe_dump(comp, open(sys.argv[2], "w"), sort_keys=False)


if __name__ == "__main__":
    main()
