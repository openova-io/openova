#!/usr/bin/env python3
"""ADR-0015 D9 deploy gate: the next deploy-bot bump needs the last delivery's
acceptance run on the gate environment to have PASSED.

  acceptance-gate.py <env> [--mode enforce|report] [--override-text <commit msg>]
  acceptance-gate.py --self-test

Reads docs/ledger/acceptance/<env>-*.json (written by
`scripts/acceptance-sovereign.sh <env> --record`) and takes the NEWEST `run`
record (run ids sort by time).

  enforce  exit 1 unless that record's result is PASS. A delivery that exists
           to FIX a failing run would otherwise deadlock behind the gate it is
           fixing, so the literal token `ACCEPTANCE-OVERRIDE:` followed by a
           reason in the commit message (the squash-merged PR body) lets that
           one bump through, loudly.
  report   never blocks; prints the same verdict. This is the mode until the
           first PASS exists on the env — it says NOT GATING in so many words,
           so an absent measurement can never read as a green gate.

Mode comes from --mode, else ACCEPTANCE_GATE_MODE, else `report`.
Wired into .github/workflows/catalyst-build.yaml as the `acceptance-gate` job
that `deploy` needs.
"""
import glob
import json
import os
import re
import sys
import tempfile

REPO = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))
OVERRIDE = re.compile(r"ACCEPTANCE-OVERRIDE:\s*(\S.{4,})")


def newest_run(acceptance_dir, env):
    best = None
    for p in sorted(glob.glob(os.path.join(acceptance_dir, f"{env}-*.json"))):
        try:
            rec = json.load(open(p, encoding="utf-8"))
        except (OSError, ValueError):
            continue
        if rec.get("env") != env or rec.get("mode") != "run":
            continue
        if best is None or rec.get("run_id", "") > best[1].get("run_id", ""):
            best = (p, rec)
    return best


def decide(acceptance_dir, env, mode, override_text):
    """(exit_code, message lines)."""
    found = newest_run(acceptance_dir, env)
    lines = []
    if found is None:
        verdict = f"NO acceptance run recorded for {env} — function on {env} is UNMEASURED"
        passed = False
    else:
        p, rec = found
        passed = rec.get("result") == "PASS"
        js = " ".join(f"{k}={v}" for k, v in sorted((rec.get("journeys") or {}).items()))
        verdict = (f"latest run {rec.get('run_id')} on {env}: {rec.get('result')} ({js}) "
                   f"finished {rec.get('finished_at')} — {os.path.relpath(p, REPO) if p.startswith(REPO) else p}")
    lines.append(verdict)
    if mode != "enforce":
        lines.append("mode=report — NOT GATING. Set repo variable ACCEPTANCE_GATE_MODE=enforce once a PASS exists.")
        return 0, lines
    if passed:
        lines.append("mode=enforce — gate OPEN: the last delivery passed D9.")
        return 0, lines
    m = OVERRIDE.search(override_text or "")
    if m:
        lines.append(f"mode=enforce — gate OVERRIDDEN for this bump: {m.group(1).strip()[:200]}")
        return 0, lines
    lines.append("mode=enforce — gate CLOSED: run `scripts/acceptance-sovereign.sh %s --record` to PASS, "
                 "or write `ACCEPTANCE-OVERRIDE: <reason>` in the PR body of a delivery that fixes the failing run." % env)
    return 1, lines


def self_test():
    bad = 0

    def check(label, ok, detail=""):
        nonlocal bad
        print(f"  {'PASS' if ok else 'FAIL'}  {label}")
        if not ok:
            print(f"        got: {detail}")
            bad += 1

    with tempfile.TemporaryDirectory() as d:
        rc, _ = decide(d, "hw900", "enforce", "")
        check("enforce + no record -> CLOSED (unmeasured is not green)", rc == 1)
        rc, msg = decide(d, "hw900", "report", "")
        check("report + no record -> open but says NOT GATING", rc == 0 and any("NOT GATING" in m for m in msg), msg)

        def put(run_id, result, mode="run", env="hw900"):
            json.dump({"env": env, "mode": mode, "run_id": run_id, "result": result, "journeys": {"J1": result}},
                      open(os.path.join(d, f"{env}-{run_id}.json"), "w"))

        put("20261108T100000Z-aaaa", "PASS")
        rc, _ = decide(d, "hw900", "enforce", "")
        check("CONTROL: enforce + latest PASS -> OPEN", rc == 0)
        put("20261108T110000Z-bbbb", "FAIL")
        rc, _ = decide(d, "hw900", "enforce", "")
        check("a NEWER FAIL closes the gate over an older PASS", rc == 1)
        rc, _ = decide(d, "hw900", "enforce", "fix: x\n\nACCEPTANCE-OVERRIDE: fixes the J3 failure of run bbbb")
        check("an override with a reason opens it for one bump", rc == 0)
        rc, _ = decide(d, "hw900", "enforce", "ACCEPTANCE-OVERRIDE:")
        check("an override WITHOUT a reason does not", rc == 1)
        put("20261108T120000Z-cccc", "PASS", mode="discover")
        rc, _ = decide(d, "hw900", "enforce", "")
        check("a newer DISCOVER record does not reopen it", rc == 1)
        put("20261108T130000Z-dddd", "PASS", env="hw901")
        rc, _ = decide(d, "hw900", "enforce", "")
        check("another env's PASS does not reopen it", rc == 1)
    return bad


def main(argv):
    if argv[1:2] == ["--self-test"]:
        print("self-test:")
        n = self_test()
        print("\nSELF-TEST OK" if n == 0 else f"\nSELF-TEST FAILED ({n})")
        return 1 if n else 0
    if len(argv) < 2:
        print(__doc__, file=sys.stderr)
        return 2
    env = argv[1]
    mode = os.environ.get("ACCEPTANCE_GATE_MODE", "report") or "report"
    override = os.environ.get("ACCEPTANCE_OVERRIDE_TEXT", "")
    i = 2
    while i < len(argv):
        if argv[i] == "--mode":
            mode, i = argv[i + 1], i + 2
        elif argv[i] == "--override-text":
            override, i = argv[i + 1], i + 2
        else:
            print(f"unknown argument {argv[i]}", file=sys.stderr)
            return 2
    if mode not in ("enforce", "report"):
        print(f"--mode must be enforce|report, got {mode!r}", file=sys.stderr)
        return 2
    rc, lines = decide(os.path.join(REPO, "docs", "ledger", "acceptance"), env, mode, override)
    for ln in lines:
        print(ln)
    summary = os.environ.get("GITHUB_STEP_SUMMARY")
    if summary:
        with open(summary, "a", encoding="utf-8") as fh:
            fh.write("### ADR-0015 D9 acceptance gate\n\n" + "\n\n".join(lines) + "\n")
    return rc


if __name__ == "__main__":
    sys.exit(main(sys.argv))
