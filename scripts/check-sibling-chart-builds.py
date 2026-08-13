#!/usr/bin/env python3
"""Decide whether the umbrella chart may publish, given its sibling matrix jobs.

Refs #6262.

WHY THIS EXISTS
───────────────
`blueprint-release.yaml` builds every changed Blueprint as one `build` matrix
job with `fail-fast: false`. Matrix jobs are independent by design, and the
umbrella (`products/catalyst` → `bp-catalyst-platform`) is just another entry
in that matrix. But the umbrella is not peer to the others: it CARRIES the
catalog seed, and the seed pins every sub-chart by exact version.

So when a sub-chart build fails, nothing stops the umbrella from publishing a
seed pin to a tag that was never pushed. That is not hypothetical — it is the
measured cause of the hw296 cutover dying four times at step 3 of 11:

  1. #6254 merged, moving bp-self-sovereign-cutover to 0.1.183 across the five
     lockstep sites.
  2. In run 31734398570, `build (platform/self-sovereign-cutover)` FAILED, so
     0.1.183 was never pushed — published tags stop at 0.1.182.
  3. In the SAME run, `build (products/catalyst)` succeeded and published the
     umbrella carrying the seed pin `bp-self-sovereign-cutover: 0.1.183`.
  4. hw296's Flux took the new umbrella, repinned the HelmRelease to 0.1.183,
     and parked on `Ready=False SourceNotReady`:
       chart pull error: ... 0.1.183: not found
  5. harbor-prewarm's Phase A0 settled-roll gate then saw its own HelmRelease
     as mid-roll and fail-closed. Step 3 never started.

The repo already owns a gate for the resulting state —
`check-catalog-seed-lockstep.sh --check-ghcr` asserts every seed pin resolves
on ghcr. But it runs in a DIFFERENT workflow, is `continue-on-error` on push
(it has to be: it races this very workflow), and only fails loud on an hourly
cron. By the time the cron fires, Sovereigns have already pulled the umbrella.

The fix is to close the race at its source rather than detect it afterwards:
before the umbrella pushes, wait for its siblings in the SAME run and refuse to
publish if any of them failed. Then the ghcr existence check is no longer
racing anything and can be run as a hard gate in the same step.

WHAT THIS SCRIPT DOES
─────────────────────
Reads the run's job list (`GET /repos/{repo}/actions/runs/{run_id}/jobs`) and
returns one of three verdicts about the sibling `build (...)` jobs:

  exit 0  — every sibling completed successfully; publishing is safe.
  exit 75 — at least one sibling is still queued/in_progress (EX_TEMPFAIL).
            The caller should sleep and re-ask.
  exit 1  — at least one sibling failed/was cancelled/timed out. Publishing the
            umbrella now would ship a pin to a tag that does not exist.
  exit 2  — input error (see the vacuity guard below).

VACUITY GUARD
─────────────
A "no siblings failed" verdict is worthless if the script cannot see the jobs
at all — a renamed job, a changed matrix key, or a paging bug would all yield
an empty sibling set and a confident green. Zero siblings is nonetheless
LEGITIMATE (a push that touches only products/catalyst has none), so emptiness
alone cannot be the tripwire.

The tripwire is instead: **this script must be able to find its OWN job in the
list it was handed.** If `--self-name` is absent from the job names, the name
construction or the API read is broken, and no verdict about siblings is
trustworthy. That fails as an input error (exit 2), never as a pass.
"""

from __future__ import annotations

import argparse
import json
import sys

# EX_TEMPFAIL — "siblings still running, ask again later". Chosen over a bare
# non-zero so a caller can never confuse "wait" with "a sibling failed".
EXIT_OK = 0
EXIT_FAILED_SIBLING = 1
EXIT_INPUT_ERROR = 2
EXIT_WAIT = 75

# Conclusions that mean the sibling did not produce its artifact. `skipped` is
# NOT here: a skipped job published nothing but was never expected to (e.g. the
# chart-version-unchanged short-circuit), so it cannot orphan a seed pin.
BAD_CONCLUSIONS = ("failure", "cancelled", "timed_out", "stale")


def classify(jobs, self_name, prefix):
    """Return (exit_code, lines). Pure — no I/O, so the self-test can drive it."""
    names = [j.get("name", "") for j in jobs]

    if self_name not in names:
        return EXIT_INPUT_ERROR, [
            f"ERROR: could not find this job ({self_name!r}) among the {len(jobs)} "
            "job(s) returned for this run.",
            "The job-name construction or the API read is broken, so any verdict "
            "about sibling jobs would be drawn from a list this script cannot "
            "actually see. Refusing to report a verdict.",
            "Jobs seen: " + (", ".join(repr(n) for n in names) or "(none)"),
        ]

    siblings = [
        j for j in jobs
        if j.get("name", "").startswith(prefix) and j.get("name") != self_name
    ]

    failed = [j for j in siblings if j.get("conclusion") in BAD_CONCLUSIONS]
    if failed:
        lines = [
            f"FAIL: {len(failed)} sibling chart build(s) in this run did not publish:",
        ]
        for j in failed:
            lines.append(f"  {j.get('name')} — conclusion={j.get('conclusion')}")
        lines += [
            "",
            "The umbrella carries the catalog seed, which pins every sub-chart by "
            "exact version. Publishing it now would ship a pin to a tag that was "
            "never pushed, and every Sovereign that reconciles the new umbrella "
            "would park on `chart pull error: ... not found` (#6262).",
            "Fix the sibling build and re-run; the umbrella will publish then.",
        ]
        return EXIT_FAILED_SIBLING, lines

    pending = [j for j in siblings if j.get("status") != "completed"]
    if pending:
        lines = [f"WAIT: {len(pending)} sibling chart build(s) still running:"]
        for j in pending:
            lines.append(f"  {j.get('name')} — status={j.get('status')}")
        return EXIT_WAIT, lines

    return EXIT_OK, [
        f"OK: all {len(siblings)} sibling chart build(s) in this run completed "
        "successfully; every seed pin they own has been pushed.",
    ]


# ──────────────────────────────────────────────────────────────────────
# Self-test — proves each verdict is reachable before the gate is trusted.
# ──────────────────────────────────────────────────────────────────────
SELF = "build (products/catalyst)"
PREFIX = "build ("


def _job(name, status="completed", conclusion="success"):
    return {"name": name, "status": status, "conclusion": conclusion}


def self_test():
    cases = [
        (
            "all siblings green -> publish",
            [_job(SELF), _job("build (platform/cilium)"), _job("build (platform/gitea)")],
            EXIT_OK,
        ),
        (
            "the hw296 shape: one sibling failed -> refuse to publish",
            [_job(SELF), _job("build (platform/self-sovereign-cutover)", conclusion="failure")],
            EXIT_FAILED_SIBLING,
        ),
        (
            "sibling cancelled -> refuse (it published nothing either)",
            [_job(SELF), _job("build (platform/cilium)", conclusion="cancelled")],
            EXIT_FAILED_SIBLING,
        ),
        (
            "sibling still running -> wait, do not guess",
            [_job(SELF), _job("build (platform/cilium)", status="in_progress", conclusion=None)],
            EXIT_WAIT,
        ),
        (
            "sibling queued -> wait",
            [_job(SELF), _job("build (platform/cilium)", status="queued", conclusion=None)],
            EXIT_WAIT,
        ),
        (
            "a failed sibling outranks a still-running one",
            [
                _job(SELF),
                _job("build (platform/cilium)", status="in_progress", conclusion=None),
                _job("build (platform/gitea)", conclusion="failure"),
            ],
            EXIT_FAILED_SIBLING,
        ),
        (
            "skipped sibling is not a failure (it was never going to publish)",
            [_job(SELF), _job("build (platform/cilium)", conclusion="skipped")],
            EXIT_OK,
        ),
        (
            "umbrella alone in the matrix -> legitimately zero siblings",
            [_job(SELF)],
            EXIT_OK,
        ),
        (
            "non-build jobs in the run are not siblings",
            [_job(SELF), _job("detect"), _job("warmup-bastion-harbor", conclusion="failure")],
            EXIT_OK,
        ),
        # ── VACUITY LEG ──
        # If the script cannot see its own job, the list it was handed is not
        # the list it thinks it is. Without this leg, a renamed job or a paging
        # bug would present as "0 siblings, all green" — a confident pass drawn
        # from nothing, which is the exact defect class #6262 was filed under.
        (
            "VACUITY: own job absent -> input error, never a pass",
            [_job("build (platform/cilium)"), _job("build (platform/gitea)")],
            EXIT_INPUT_ERROR,
        ),
        (
            "VACUITY: empty job list -> input error, never a pass",
            [],
            EXIT_INPUT_ERROR,
        ),
    ]

    failures = 0
    for label, jobs, want in cases:
        got, lines = classify(jobs, SELF, PREFIX)
        ok = got == want
        print(f"  [{'PASS' if ok else 'FAIL'}] {label} (want {want}, got {got})")
        if not ok:
            failures += 1
            for line in lines:
                print(f"         {line}")

    if failures:
        print(f"\nSELF-TEST FAILED: {failures}/{len(cases)} case(s).")
        return 1
    print(f"\nSELF-TEST PASSED: {len(cases)}/{len(cases)} cases, all four verdicts reachable.")
    return 0


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--jobs-file", help="JSON file: the `jobs` array from the run-jobs API.")
    ap.add_argument("--self-name", help="Display name of THIS matrix job, e.g. 'build (products/catalyst)'.")
    ap.add_argument("--prefix", default=PREFIX, help="Job-name prefix identifying sibling chart builds.")
    ap.add_argument("--self-test", action="store_true", help="Prove every verdict is reachable, then exit.")
    args = ap.parse_args()

    if args.self_test:
        return self_test()

    if not args.jobs_file or not args.self_name:
        print("ERROR: --jobs-file and --self-name are required (or use --self-test).", file=sys.stderr)
        return EXIT_INPUT_ERROR

    try:
        with open(args.jobs_file, encoding="utf-8") as fh:
            payload = json.load(fh)
    except (OSError, json.JSONDecodeError) as exc:
        print(f"ERROR: could not read {args.jobs_file}: {exc}", file=sys.stderr)
        return EXIT_INPUT_ERROR

    jobs = payload.get("jobs", payload) if isinstance(payload, dict) else payload
    if not isinstance(jobs, list):
        print(f"ERROR: expected a list of jobs in {args.jobs_file}, got {type(jobs).__name__}.", file=sys.stderr)
        return EXIT_INPUT_ERROR

    code, lines = classify(jobs, args.self_name, args.prefix)
    for line in lines:
        print(line)
    return code


if __name__ == "__main__":
    sys.exit(main())
