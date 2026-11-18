#!/usr/bin/env python3
"""check-plan-additive.py - a plan that adds a region creates resources and changes none.

Adding a region to a Sovereign must leave the regions it already has as they are. This check
reads an OpenTofu plan in its JSON form, the output of `tofu show -json <plan file>`, and
judges it by three rules:
  1. Every managed resource in resource_changes has the action create or no-op. Any other
     action fails: update, delete, a replacement (delete and create in either order), forget.
  2. The plan creates at least one managed resource. A plan that creates nothing did not add a
     region, so it proves nothing.
  3. No managed resource in resource_drift was deleted outside OpenTofu. Such a resource shows
     as a create in resource_changes, but it is made again, not added.
Data sources (mode data) are not judged: reading one changes nothing. Outputs are not judged.
A file that is not a plan (no format_version, no planned_values) or a plan that errored is
refused with exit code 2.

Usage (from the repository root):
  python3 scripts/check-plan-additive.py <plan.json>   judge the plan; - reads standard input
  python3 scripts/check-plan-additive.py --self-test   show that every rule can fail and pass
Output: one line `NOT ADDITIVE <address>: <reason>` for each resource that breaks a rule, then
one summary line. Exit codes: 0 the plan only adds; 1 a rule failed; 2 wrong usage or not a plan.
"""
import json
import os
import subprocess
import sys
import tempfile

ALLOWED = (["create"], ["no-op"])


class NotAPlan(Exception):
    """The input cannot be judged: it is not the JSON form of an OpenTofu plan."""


def load(text):
    """The plan object of a JSON text. Raises NotAPlan when the text is not a plan."""
    try:
        plan = json.loads(text)
    except ValueError as e:
        raise NotAPlan("not JSON: %s" % e)
    if not isinstance(plan, dict) or "format_version" not in plan or "planned_values" not in plan:
        raise NotAPlan("not a plan: format_version or planned_values is missing")
    if plan.get("errored") is True:
        raise NotAPlan("the plan errored, so it is not complete")
    for key in ("resource_changes", "resource_drift"):
        if not isinstance(plan.get(key, []), list):
            raise NotAPlan("not a plan: %s is not a list" % key)
    return plan


def managed(entries):
    """The entries of a plan list that are managed resources."""
    return [e for e in entries if isinstance(e, dict) and e.get("mode") == "managed"]


def actions(entry):
    return list(((entry.get("change") or {}).get("actions")) or [])


def judge(plan):
    """(exit code, lines) for a plan object: 0 when the plan only adds, 1 otherwise."""
    lines = []
    changes = managed(plan.get("resource_changes", []))
    for e in changes:
        a = actions(e)
        if a not in ALLOWED:
            lines.append("NOT ADDITIVE %s: action %s" % (e.get("address", "?"), ",".join(a) or "none"))
    for e in managed(plan.get("resource_drift", [])):
        if "delete" in actions(e):
            lines.append("NOT ADDITIVE %s: deleted outside OpenTofu, so the plan makes it again" % e.get("address", "?"))
    created = sum(1 for e in changes if actions(e) == ["create"])
    unchanged = sum(1 for e in changes if actions(e) == ["no-op"])
    if created == 0:
        lines.append("NOT ADDITIVE: the plan creates no resource, so it adds no region")
    if lines:
        lines.append("check-plan-additive: FAIL, %d finding(s); %d to create, %d unchanged" % (len(lines), created, unchanged))
        return 1, lines
    lines.append("check-plan-additive: ok, %d to create, %d unchanged" % (created, unchanged))
    return 0, lines


def run(argv):
    if len(argv) != 1 or argv[0].startswith("--"):
        print("usage: check-plan-additive.py <plan.json> | - | --self-test", file=sys.stderr)
        return 2
    try:
        if argv[0] == "-":
            text = sys.stdin.read()
        else:
            with open(argv[0], encoding="utf-8") as fh:
                text = fh.read()
        plan = load(text)
    except (OSError, NotAPlan) as e:
        print("check-plan-additive: %s" % e, file=sys.stderr)
        return 2
    code, lines = judge(plan)
    for line in lines:
        print(line)
    return code


def self_test():
    def rc(address, acts, mode="managed"):
        return {"address": address, "mode": mode, "change": {"actions": acts}}

    def plan(changes, drift=None, **more):
        p = {"format_version": "1.2", "planned_values": {}, "resource_changes": changes}
        if drift is not None:
            p["resource_drift"] = drift
        p.update(more)
        return p

    base = [rc("a.old", ["no-op"]), rc("a.new", ["create"]), rc("data.x.y", ["read"], mode="data")]
    cases = [
        ("pass: creates and unchanged resources, a data source read", plan(base), 0),
        ("pass: a drift that only updates is not judged", plan(base, [rc("a.old", ["update"])]), 0),
        ("rule 1: an update fails", plan(base + [rc("a.x", ["update"])]), 1),
        ("rule 1: a delete fails", plan(base + [rc("a.x", ["delete"])]), 1),
        ("rule 1: a replacement (delete, create) fails", plan(base + [rc("a.x", ["delete", "create"])]), 1),
        ("rule 1: a replacement (create, delete) fails", plan(base + [rc("a.x", ["create", "delete"])]), 1),
        ("rule 1: a forget fails", plan(base + [rc("a.x", ["forget"])]), 1),
        ("rule 1: a resource without actions fails", plan(base + [{"address": "a.x", "mode": "managed"}]), 1),
        ("rule 2: a plan that creates nothing fails", plan([rc("a.old", ["no-op"])]), 1),
        ("rule 2: a plan without resource_changes fails", plan([]), 1),
        ("rule 3: a resource deleted outside OpenTofu fails", plan(base, [rc("a.old", ["delete"])]), 1),
    ]
    failed = 0
    for name, p, want in cases:
        got, lines = judge(load(json.dumps(p)))
        ok = got == want and (want == 0 or any(l.startswith("NOT ADDITIVE") for l in lines))
        print("%s %s" % ("ok  " if ok else "FAIL", name))
        failed += 0 if ok else 1
    refused = [
        ("refused: a text that is not JSON", "{"),
        ("refused: a state, not a plan", json.dumps({"format_version": "1.0", "values": {}})),
        ("refused: a plan that errored", json.dumps(plan(base, errored=True))),
        ("refused: resource_changes that is not a list", json.dumps(plan({}))),
    ]
    for name, text in refused:
        try:
            load(text)
            ok = False
        except NotAPlan:
            ok = True
        print("%s %s" % ("ok  " if ok else "FAIL", name))
        failed += 0 if ok else 1
    # The command itself, on files: exit codes 0, 1 and 2.
    with tempfile.TemporaryDirectory() as d:
        for name, content, want in (
            ("command: a plan that only adds exits 0", json.dumps(plan(base)), 0),
            ("command: a plan with an update exits 1", json.dumps(plan(base + [rc("a.x", ["update"])])), 1),
            ("command: a file that is not a plan exits 2", "not a plan", 2),
        ):
            path = os.path.join(d, "plan.json")
            with open(path, "w", encoding="utf-8") as fh:
                fh.write(content)
            got = subprocess.run([sys.executable, os.path.abspath(__file__), path], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode
            ok = got == want
            print("%s %s" % ("ok  " if ok else "FAIL", name))
            failed += 0 if ok else 1
    print("check-plan-additive self-test: %s" % ("ok" if failed == 0 else "FAILED"))
    return 0 if failed == 0 else 1


if __name__ == "__main__":
    if sys.argv[1:] == ["--self-test"]:
        sys.exit(self_test())
    sys.exit(run(sys.argv[1:]))
