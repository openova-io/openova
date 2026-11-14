#!/usr/bin/env python3
"""check-lock-stages.py - guard G6: a stage of the intent lock never moves back.

The intent lock (products/catalyst/chart/templates/intent-lock/) has one
binding per stage, catalyst-intent-lock-<stage>, whose action is audit
(["Audit", "Warn"]) or deny (["Deny"]). This guard renders the bindings the
way a Sovereign gets them: the chart's defaults plus the intentLock values of
the bootstrap-kit HelmRelease, clusters/_template/bootstrap-kit/
13-bp-catalyst-platform.yaml, when it sets any. It compares the render with
the baseline scripts/baselines/lock-stages.json, which holds one line per
stage: its action.

  Rule 1: every rendered stage has a baseline line, and every baseline line
          a rendered stage. A stage is neither added nor dropped silently.
  Rule 2: every stage renders with the action of its baseline line. A stage
          at deny in the baseline that renders at audit has moved back.
          A stage that renders at deny while the baseline says audit fails
          too: the change that switches it writes deny on its line, so the
          switch is recorded and cannot be undone.

Usage:
  python3 scripts/check-lock-stages.py              judge the tree
  python3 scripts/check-lock-stages.py --self-test  prove that every rule can fail
Exit codes: 0 pass; 1 a rule failed; 2 nothing was judged (no helm, a render
error, an unreadable baseline, wrong arguments).
"""
import json
import os
import subprocess
import sys
import tempfile

import yaml

ME = "check-lock-stages"
CHART = "products/catalyst/chart"
KIT = "clusters/_template/bootstrap-kit/13-bp-catalyst-platform.yaml"
BASELINE = "scripts/baselines/lock-stages.json"
SCHEMA = "openova.io/lock-stages/v1"
PREFIX = "catalyst-intent-lock-"
ACTIONS = {"audit": ["Audit", "Warn"], "deny": ["Deny"]}


def cannot(msg):
    print(f"{ME}: cannot judge: {msg}")
    sys.exit(2)


def rendered_stages(text):
    """Map stage -> action from rendered bindings; an unknown action is kept as its list."""
    out = {}
    for doc in yaml.safe_load_all(text):
        if not doc or doc.get("kind") != "ValidatingAdmissionPolicyBinding":
            continue
        name = doc["metadata"]["name"]
        if not name.startswith(PREFIX):
            continue
        acts = doc["spec"].get("validationActions")
        out[name[len(PREFIX):]] = next((a for a, v in ACTIONS.items() if v == acts), str(acts))
    return out


def judge(rendered, baseline):
    """Return the findings, one line each; empty when both rules hold."""
    bad = []
    for stage in sorted(set(rendered) - set(baseline)):
        bad.append(f"rule 1: stage {stage} renders but has no line in {BASELINE}")
    for stage in sorted(set(baseline) - set(rendered)):
        bad.append(f"rule 1: stage {stage} has a line in {BASELINE} but renders no binding")
    for stage in sorted(set(rendered) & set(baseline)):
        if rendered[stage] != baseline[stage]:
            bad.append(f"rule 2: stage {stage} renders {rendered[stage]}; its baseline line says {baseline[stage]}")
    return bad


def read_baseline(path):
    try:
        with open(path) as f:
            data = json.load(f)
    except (OSError, ValueError) as e:
        cannot(f"{path}: {e}")
    stages = data.get("stages") if isinstance(data, dict) else None
    if not isinstance(stages, dict) or not stages or data.get("schema") != SCHEMA:
        cannot(f"{path} is not a {SCHEMA} document with stages")
    for stage, action in stages.items():
        if action not in ACTIONS:
            cannot(f"{path}: stage {stage} has action {action!r}; it must be audit or deny")
    return stages


def render(root):
    try:
        with open(os.path.join(root, KIT)) as f:
            docs = [d for d in yaml.safe_load_all(f) if d]
    except (OSError, yaml.YAMLError) as e:
        cannot(f"{KIT}: {e}")
    releases = [d for d in docs if d.get("kind") == "HelmRelease"]
    if len(releases) != 1:
        cannot(f"{KIT} holds {len(releases)} HelmReleases, want 1")
    kit = (releases[0].get("spec", {}).get("values") or {}).get("intentLock")
    cmd = ["helm", "template", "lock", os.path.join(root, CHART), "--show-only", "templates/intent-lock/bindings.yaml"]
    with tempfile.NamedTemporaryFile("w", suffix=".yaml") as values:
        if kit is not None:
            yaml.safe_dump({"intentLock": kit}, values)
            values.flush()
            cmd += ["-f", values.name]
        try:
            proc = subprocess.run(cmd, capture_output=True, text=True, check=False)
        except OSError as e:
            cannot(f"helm: {e}")
    if proc.returncode != 0:
        cannot(f"helm template failed: {proc.stderr.strip()}")
    return rendered_stages(proc.stdout)


def binding(stage, acts):
    return f"---\nkind: ValidatingAdmissionPolicyBinding\nmetadata: {{name: {PREFIX}{stage}}}\nspec: {{validationActions: {json.dumps(acts)}}}\n"


def self_test():
    audit, deny = ACTIONS["audit"], ACTIONS["deny"]
    cases = [
        ("all stages at their baseline action", binding("a", audit) + binding("b", deny), {"a": "audit", "b": "deny"}, ""),
        ("rule 1: a stage renders without a baseline line", binding("a", audit) + binding("c", audit), {"a": "audit"}, "rule 1: stage c renders"),
        ("rule 1: a baseline line without a rendered stage", binding("a", audit), {"a": "audit", "b": "deny"}, "rule 1: stage b has a line"),
        ("rule 2: a deny stage moved back to audit", binding("a", audit) + binding("b", audit), {"a": "audit", "b": "deny"}, "rule 2: stage b renders audit"),
        ("rule 2: a stage at deny that the baseline does not record", binding("a", deny), {"a": "audit"}, "rule 2: stage a renders deny"),
        ("rule 2: an action that is neither audit nor deny", binding("a", ["Warn"]), {"a": "audit"}, "rule 2: stage a renders ['Warn']"),
    ]
    failed = 0
    for name, text, base, want in cases:
        got = judge(rendered_stages(text), base)
        ok = (not got) if not want else any(line.startswith(want) for line in got)
        print(f"{'ok  ' if ok else 'FAIL'} {name}: {got or 'pass'}")
        failed += 0 if ok else 1
    if failed:
        print(f"{ME}: self-test FAILED ({failed} case(s))")
        return 1
    print(f"{ME}: self-test passed ({len(cases)} cases)")
    return 0


def main(argv):
    if argv == ["--self-test"]:
        return self_test()
    if argv:
        cannot("usage: check-lock-stages.py [--self-test]")
    root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    baseline = read_baseline(os.path.join(root, BASELINE))
    bad = judge(render(root), baseline)
    for line in bad:
        print(f"{ME}: FAIL, {line}")
    if bad:
        return 1
    print(f"{ME}: PASS, {len(baseline)} stages render with their baseline action")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
