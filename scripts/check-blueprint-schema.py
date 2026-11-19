#!/usr/bin/env python3
"""Validate every Blueprint file against platform/_schemas/blueprint.schema.json (contract C1).

The files are the ones scripts/generate-catalog.py reads: platform/<name>/blueprint.yaml and
products/<name>/blueprint.yaml. Needs PyYAML and jsonschema (draft 2020-12).

    python3 scripts/check-blueprint-schema.py              one line per invalid file; exit 1 if any
    python3 scripts/check-blueprint-schema.py --self-test  every refused shape is refused, a valid file passes

Exit codes: 0 every file validates (or the self-test passes); 1 a file is invalid (or a self-test case failed);
2 the schema or a file cannot be read, so nothing was judged.
"""
import copy
import glob
import json
import os
import sys

import yaml
from jsonschema import Draft202012Validator

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SCHEMA = "platform/_schemas/blueprint.schema.json"
FILES = ("platform/*/blueprint.yaml", "products/*/blueprint.yaml")


def validator(root):
    with open(os.path.join(root, SCHEMA), encoding="utf-8") as f:
        schema = json.load(f)
    Draft202012Validator.check_schema(schema)
    return Draft202012Validator(schema)


def problems(v, doc):
    return sorted("%s: %s" % ("/".join(str(p) for p in e.absolute_path) or "(root)", e.message) for e in v.iter_errors(doc))


def check(root):
    try:
        v = validator(root)
    except (OSError, ValueError) as e:
        print("check-blueprint-schema: %s cannot be read: %s" % (SCHEMA, e), file=sys.stderr)
        return 2
    files = sorted(os.path.relpath(p, root) for g in FILES for p in glob.glob(os.path.join(root, g)))
    if not files:
        print("check-blueprint-schema: no Blueprint file found: nothing was judged", file=sys.stderr)
        return 2
    bad = 0
    for rel in files:
        try:
            with open(os.path.join(root, rel), encoding="utf-8") as f:
                doc = yaml.safe_load(f)
        except (OSError, yaml.YAMLError) as e:
            print("check-blueprint-schema: %s cannot be read: %s" % (rel, str(e).replace("\n", " ")), file=sys.stderr)
            return 2
        for p in problems(v, doc):
            bad += 1
            print("check-blueprint-schema: %s: %s" % (rel, p), file=sys.stderr)
    if bad:
        print("check-blueprint-schema: %d problems in the Blueprint files; see above" % bad)
        return 1
    print("check-blueprint-schema: all %d Blueprint files validate against %s" % (len(files), SCHEMA))
    return 0


def self_test(root):
    v = validator(root)
    good = {"apiVersion": "catalyst.openova.io/v1", "kind": "Blueprint", "metadata": {"name": "bp-xy", "labels": {"a/b": "c"}},
            "spec": {"version": "1.0.0", "class": "application", "lifecycle": "experimental", "visibility": "listed",
                     "card": {"title": "X", "tags": ["t"]}, "shareable": True, "contextSchema": {"kind": "db", "valuesKey": "dbs"}}}

    def edit(path, value):
        d = copy.deepcopy(good)
        *head, last = path.split(".")
        node = d
        for k in head:
            node = node[k]
        if value is None:
            del node[last]
        else:
            node[last] = value
        return d

    def two(*edits):
        d = copy.deepcopy(good)
        for path, value in edits:
            node = d
            *head, last = path.split(".")
            for k in head:
                node = node[k]
            node[last] = value
        return d

    cases = [
        ("a valid file", good, False),
        ("an apiVersion other than v1", edit("apiVersion", "catalyst.openova.io/v1alpha1"), True),
        ("a name without bp-", edit("metadata.name", "x"), True),
        ("no spec.class", edit("spec.class", None), True),
        ("no spec.lifecycle", edit("spec.lifecycle", None), True),
        ("no spec.visibility", edit("spec.visibility", None), True),
        ("an unknown class", edit("spec.class", "tool"), True),
        ("an unknown lifecycle", edit("spec.lifecycle", "beta"), True),
        ("card.category", edit("spec.card.category", "data"), True),
        ("card.family", edit("spec.card.family", "postgres"), True),
        ("the label catalyst.openova.io/category", edit("metadata.labels", {"catalyst.openova.io/category": "x"}), True),
        ("the label catalyst.openova.io/tier", edit("metadata.labels", {"catalyst.openova.io/tier": "x"}), True),
        ("the label catalyst.openova.io/section", edit("metadata.labels", {"catalyst.openova.io/section": "x"}), True),
        ("spec.backingServices", edit("spec.backingServices", []), True),
        ("spec.placementCapability", edit("spec.placementCapability", "x"), True),
        ("a listed platform-component", edit("spec.class", "platform-component"), True),
        ("an unlisted platform-component", two(("spec.class", "platform-component"), ("spec.visibility", "unlisted")), False),
        ("a listed deprecated Blueprint", edit("spec.lifecycle", "deprecated"), True),
        ("a shareable Blueprint without contextSchema", edit("spec.contextSchema", None), True),
    ]
    failed = 0
    for title, doc, refused in cases:
        ok = bool(problems(v, doc)) == refused
        failed += not ok
        print("%s %s is %s" % ("ok  " if ok else "FAIL", title, "refused" if refused else "accepted"))
    print("self-test: %d of %d cases fail" % (failed, len(cases)) if failed else "self-test: all %d cases pass" % len(cases))
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(self_test(ROOT) if sys.argv[1:] == ["--self-test"] else check(ROOT))
