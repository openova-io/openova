#!/usr/bin/env python3
"""version_order.py - the release writer's one version comparison (FND-018).

Every step of .github/workflows/blueprint-release.yaml that compares two
versions calls this file, and scripts/sync-funnel-pin.py imports its two
functions, so that every site of a release compares versions the same way.

  python3 scripts/lib/version_order.py <present> <published> [--site <text>]
  python3 scripts/lib/version_order.py --self-test

It prints `higher`, `same` or `lower`: how <present> compares with <published>,
MAJOR, MINOR and PATCH each as a number (1.4.1952 is higher than 1.4.1951,
1.10.0 is higher than 1.9.9). It exits 2 when either is not MAJOR.MINOR.PATCH.
With --site, a `higher` result is also written as a note to the log and to
$GITHUB_STEP_SUMMARY: the site is left alone, because a newer release run
already moved it and an older run never moves a site back.
"""

import argparse
import os
import re
import sys

_RE_SEMVER = re.compile(r"^(\d+)\.(\d+)\.(\d+)$")


class VersionError(ValueError):
    """A version that is not MAJOR.MINOR.PATCH."""


def version_order(present, published):
    """How `present` compares with `published`: 'higher', 'same' or 'lower'.

    MAJOR, MINOR and PATCH are compared as numbers, never as strings. Raises
    VersionError when either value is not MAJOR.MINOR.PATCH.
    """
    keys = []
    for v in (present, published):
        m = _RE_SEMVER.match(v or "")
        if not m:
            raise VersionError("version %r is not MAJOR.MINOR.PATCH" % (v,))
        keys.append(tuple(int(x) for x in m.groups()))
    if keys[0] > keys[1]:
        return "higher"
    if keys[0] == keys[1]:
        return "same"
    return "lower"


def note_kept(site, present, published):
    """Say in the log and in the step summary that a newer site was left alone."""
    msg = ("%s holds %s, above the %s this run published: left as it is "
           "(FND-018)." % (site, present, published))
    print("::notice title=Newer version kept::%s" % msg, file=sys.stderr)
    summary = os.environ.get("GITHUB_STEP_SUMMARY")
    if summary:
        with open(summary, "a") as fh:
            fh.write("- Newer version kept: %s\n" % msg)


def _self_test():
    failures = []

    def check(cond, msg):
        print("  %s %s" % ("ok:  " if cond else "FAIL:", msg))
        if not cond:
            failures.append(msg)

    print("[1] versions compare as numbers, never as strings")
    check(version_order("1.4.1952", "1.4.1951") == "higher", "1.4.1952 is higher than 1.4.1951")
    check(version_order("1.10.0", "1.9.9") == "higher", "1.10.0 is higher than 1.9.9")
    check(version_order("1.9.9", "1.10.0") == "lower", "1.9.9 is lower than 1.10.0")
    check(version_order("2.0.0", "2.0.0") == "same", "2.0.0 is the same as 2.0.0")
    print("[2] a version that is not MAJOR.MINOR.PATCH is refused, on either side")
    for bad in ("1.2", "1.2.3-rc1", "v1.2.3", "", None):
        for args in ((bad, "1.0.0"), ("1.0.0", bad)):
            try:
                version_order(*args)
                check(False, "%r is refused (%r)" % (bad, args))
            except VersionError:
                check(True, "%r is refused (%r)" % (bad, args))
    print("[3] the command line prints the order, notes a kept site, exits 2 on a bad version")
    import contextlib
    import io
    import tempfile
    with tempfile.TemporaryDirectory() as tmp:
        summary = os.path.join(tmp, "summary.md")
        old = os.environ.get("GITHUB_STEP_SUMMARY")
        os.environ["GITHUB_STEP_SUMMARY"] = summary
        try:
            out, err = io.StringIO(), io.StringIO()
            with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
                rc = main(["1.0.1", "1.0.0", "--site", "the site"])
            check(rc == 0 and out.getvalue() == "higher\n", "prints higher and exits 0")
            note = open(summary).read() if os.path.exists(summary) else ""
            check("the site holds 1.0.1, above the 1.0.0" in note, "a higher site is noted in the step summary")
            check("Newer version kept" in err.getvalue(), "a higher site is noted in the log")
            os.remove(summary)
            out, err = io.StringIO(), io.StringIO()
            with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
                rc = main(["1.0.0", "1.0.1", "--site", "the site"])
            check(rc == 0 and out.getvalue() == "lower\n", "prints lower and exits 0")
            check(not os.path.exists(summary), "a lower site writes no note")
            out, err = io.StringIO(), io.StringIO()
            with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
                rc = main(["1.0", "1.0.1"])
            check(rc == 2 and out.getvalue() == "", "a bad version exits 2 and prints no order")
        finally:
            if old is None:
                os.environ.pop("GITHUB_STEP_SUMMARY", None)
            else:
                os.environ["GITHUB_STEP_SUMMARY"] = old

    print()
    if failures:
        print("SELF-TEST FAILED: %d assertion(s) failed" % len(failures))
        return 1
    print("SELF-TEST PASSED: all assertions green")
    return 0


def main(argv=None):
    ap = argparse.ArgumentParser(
        prog="version_order.py",
        description="Print higher, same or lower: how PRESENT compares with PUBLISHED (FND-018).",
    )
    ap.add_argument("present", nargs="?", help="the version a site holds, e.g. 1.4.1952")
    ap.add_argument("published", nargs="?", help="the version this run published, e.g. 1.4.1951")
    ap.add_argument("--site", default=None, help="on `higher`, note that this site was left alone")
    ap.add_argument("--self-test", action="store_true", help="run the embedded tests and exit")
    args = ap.parse_args(argv)

    if args.self_test:
        return _self_test()
    if args.present is None or args.published is None:
        ap.error("both <present> and <published> are required (or use --self-test)")
    try:
        order = version_order(args.present, args.published)
    except VersionError as exc:
        print("::error title=Release writer version::%s" % exc, file=sys.stderr)
        return 2
    if order == "higher" and args.site:
        note_kept(args.site, args.present, args.published)
    print(order)
    return 0


if __name__ == "__main__":
    sys.exit(main())
