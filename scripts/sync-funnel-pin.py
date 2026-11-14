#!/usr/bin/env python3
"""sync-funnel-pin.py - keep the funnel chart pin in lockstep with a chart bump.

The funnel pin is the Go constant `DefaultHRAppChartVersions` in
core/services/provisioning/gitops/helmrelease_apps.go:

    const DefaultHRAppChartVersions = "openclaw=0.2.20,stalwart-mail=0.1.16,newapi=1.4.157,agenity=0.5.34"

The provisioning service compiles it in as the chart version of every
HelmRelease-shaped funnel app (nothing sets the override
CATALYST_HR_APP_CHART_VERSIONS), and TestDefaultHRAppPins_MatchCatalogSeed
requires each entry to equal the catalog-seed delivery pin of the chart that
app's generator installs. The seed is written by sync-catalog-seed-pin.py in
the release bump of .github/workflows/blueprint-release.yaml; this script is
the matching writer for the constant, called right after it in the same step,
so that one bump is one commit that leaves the drift test green.

The entries are keyed by the funnel app slug, not by the chart name
(`stalwart-mail` installs `bp-stalwart-tenant`). The slug -> chart mapping is
read from the generator itself: the `case "<slug>": out = <fn>(opt)` lines of
generateHelmReleaseApp, and the `chart: bp-<x>` line of <fn>'s HelmRelease
template, the same line the drift test reads from the rendered output. No
table of the mapping is kept here.

Rules (the same as sync-catalog-seed-pin.py follows for the seed):
  * chart without an entry       -> no-op, exit 0 (most charts have none)
  * entry already at <version>   -> no-op, exit 0
  * entry below <version>        -> that entry's version is rewritten; every other
                                    byte of the file stays as it was
  * entry above <version>        -> LEFT UNTOUCHED, with a note in the log and the
                                    step summary (FND-018: a newer release run
                                    already moved it; an older run never moves a
                                    site back). The comparison is version_order of
                                    sync-catalog-seed-pin.py, the release writer's
                                    one version comparison.
  * unexpected file shape        -> exit 2, nothing written: no constant line or
                                    more than one, an entry that is not
                                    <slug>=MAJOR.MINOR.PATCH, a slug twice, a slug
                                    the generator switch does not render, a
                                    generator with no or several chart names, or a
                                    rewrite that cannot be verified. A version to
                                    write that is not MAJOR.MINOR.PATCH is refused
                                    the same way.

Exit codes: 0 updated or clean no-op; 1 usage error; 2 unexpected shape or version.

Usage:
  scripts/sync-funnel-pin.py <chart-name> <version> [--file <path>] [--quiet]
  scripts/sync-funnel-pin.py --self-test
"""

import argparse
import importlib.util
import os
import re
import sys
import tempfile

_REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FUNNEL_REL = os.path.join("core", "services", "provisioning", "gitops", "helmrelease_apps.go")
DEFAULT_GO = os.path.join(_REPO_ROOT, FUNNEL_REL)
CONST = "DefaultHRAppChartVersions"

# The whole constant line. Only its quoted value is ever rewritten.
_RE_CONST = re.compile(r'^(const ' + CONST + r' = ")([^"\n]*)("[ \t]*)$', re.M)
_RE_ENTRY = re.compile(r"^([a-z0-9][a-z0-9-]*)=(\S+)$")
_RE_SWITCH = re.compile(r"^func generateHelmReleaseApp\(", re.M)
_RE_CASE = re.compile(r'^\s*case "([a-z0-9][a-z0-9-]*)":\s*\n\s*out = (generate[A-Za-z0-9_]*)\(opt\)\s*$', re.M)
# The chart line of a HelmRelease template; the drift test's chartRE.
_RE_CHART = re.compile(r'^\s+chart:\s*"?(bp-[a-z0-9-]+)"?\s*$', re.M)


class ShapeError(Exception):
    """The file does not have the shape this writer can rewrite safely."""


def _load_seed_writer():
    """sync-catalog-seed-pin.py, for version_order and note_kept: one comparison for every site."""
    path = os.path.join(os.path.dirname(os.path.abspath(__file__)), "sync-catalog-seed-pin.py")
    spec = importlib.util.spec_from_file_location("sync_catalog_seed_pin", path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


_SEED = _load_seed_writer()


def _func_body(text, name):
    """The text of `func <name>(` up to its closing `}` at column 0."""
    m = re.search(r"^func " + re.escape(name) + r"\(", text, re.M)
    if not m:
        raise ShapeError("generator %s is named in generateHelmReleaseApp but not defined" % name)
    end = re.compile(r"^}\s*$", re.M).search(text, m.end())
    if not end:
        raise ShapeError("generator %s has no closing brace at column 0" % name)
    return text[m.start():end.end()]


def slug_charts(text):
    """slug -> chart, read from the generator switch and each generator's HelmRelease template."""
    m = _RE_SWITCH.search(text)
    if not m:
        raise ShapeError("no func generateHelmReleaseApp: the generator switch cannot be read")
    switch = _func_body(text, "generateHelmReleaseApp")
    out = {}
    for slug, fn in _RE_CASE.findall(switch):
        charts = sorted(set(_RE_CHART.findall(_func_body(text, fn))))
        if len(charts) != 1:
            raise ShapeError("generator %s (slug %s) names %d chart(s) %s; exactly one is expected"
                             % (fn, slug, len(charts), charts))
        out[slug] = charts[0]
    if not out:
        raise ShapeError("generateHelmReleaseApp has no `case \"<slug>\": out = generate...(opt)` line")
    return out


def read_pin(text):
    """(match, [(slug, version), ...]) of the one constant line."""
    hits = list(_RE_CONST.finditer(text))
    if len(hits) != 1:
        raise ShapeError("expected exactly one line `const %s = \"...\"`; found %d" % (CONST, len(hits)))
    entries, seen = [], set()
    for raw in hits[0].group(2).split(","):
        e = _RE_ENTRY.match(raw)
        if not e or not _SEED._RE_SEMVER.match(e.group(2)):
            raise ShapeError("entry %r of %s is not <slug>=MAJOR.MINOR.PATCH" % (raw, CONST))
        if e.group(1) in seen:
            raise ShapeError("slug %s appears twice in %s" % (e.group(1), CONST))
        seen.add(e.group(1))
        entries.append((e.group(1), e.group(2)))
    return hits[0], entries


def sync_pin(text, chart, version):
    """Pure function: (new_text, report) for one chart bump.

    report = {matched: [slug, ...], updated: [(slug, old, new)], kept_newer: [(slug, present)]}
    """
    m, entries = read_pin(text)
    charts = slug_charts(text)
    missing = [s for s, _ in entries if s not in charts]
    if missing:
        raise ShapeError("%s names %s, which generateHelmReleaseApp does not render; the chart of that "
                         "entry cannot be known" % (CONST, ", ".join(missing)))
    report = {"matched": [s for s, _ in entries if charts[s] == chart], "updated": [], "kept_newer": []}
    if not report["matched"]:
        return text, report

    new_entries = []
    for slug, present in entries:
        if charts[slug] != chart or present == version:
            new_entries.append((slug, present))
            continue
        try:
            order = _SEED.version_order(present, version)
        except _SEED.VersionError as exc:
            raise ShapeError("entry %s of %s: %s" % (slug, CONST, exc))
        if order == "higher":
            report["kept_newer"].append((slug, present))
            new_entries.append((slug, present))
            continue
        report["updated"].append((slug, present, version))
        new_entries.append((slug, version))
    if not report["updated"]:
        return text, report

    value = ",".join("%s=%s" % e for e in new_entries)
    new_text = text[:m.start(2)] + value + text[m.end(2):]

    # Verify: the constant reads back as intended, and it is the only line that changed.
    _, got = read_pin(new_text)
    if got != new_entries:
        raise ShapeError("post-write verify failed: %s reads %s, want %s" % (CONST, got, new_entries))
    old_lines, new_lines = text.split("\n"), new_text.split("\n")
    changed = [i for i, (a, b) in enumerate(zip(old_lines, new_lines)) if a != b]
    if len(old_lines) != len(new_lines) or len(changed) != 1:
        raise ShapeError("post-write verify failed: the rewrite touched %d line(s), not the one %s line"
                         % (len(changed), CONST))
    return new_text, report


def _run(chart, version, path, quiet):
    try:
        with open(path) as fh:
            text = fh.read()
    except OSError as exc:
        print("::error title=sync-funnel-pin::cannot read %s: %s" % (path, exc), file=sys.stderr)
        return 2
    try:
        new_text, report = sync_pin(text, chart, version)
    except ShapeError as exc:
        print("::error title=funnel pin shape::%s: %s" % (os.path.relpath(os.path.abspath(path), _REPO_ROOT), exc),
              file=sys.stderr)
        return 2

    rel = os.path.relpath(os.path.abspath(path), _REPO_ROOT)
    if not report["matched"]:
        if not quiet:
            print("INFO: %s has no entry in %s - graceful no-op." % (chart, CONST))
        return 0
    for slug, present in report["kept_newer"]:
        _SEED.note_kept("%s %s %s" % (rel, CONST, slug), present, version)
    if not report["updated"]:
        if not quiet and not report["kept_newer"]:
            print("INFO: %s already pins %s at %s - no-op." % (CONST, chart, version))
        return 0

    d = os.path.dirname(os.path.abspath(path))
    fd, tmp = tempfile.mkstemp(dir=d, prefix=".funnel-pin-", suffix=".tmp")
    try:
        with os.fdopen(fd, "w") as out:
            out.write(new_text)
        os.chmod(tmp, os.stat(path).st_mode & 0o7777)
        os.replace(tmp, path)
    except Exception:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise
    if not quiet:
        for slug, old, new in report["updated"]:
            print("funnel pin: %s %s=%s -> %s (%s)" % (CONST, slug, old, new, rel))
    return 0


# ---------------------------------------------------------------------------------------------- self-test
_FIXTURE = '''package gitops

func generateHelmReleaseApp(appSlug string, opt helmReleaseAppOpts) string {
	var out string
	switch appSlug {
	case "foo":
		out = generateFooHR(opt)
	case "mail":
		out = generateMailHR(opt)
	default:
		return ""
	}
	return out
}

func generateFooHR(opt helmReleaseAppOpts) string {
	return fmt.Sprintf(`%s
spec:
  chart:
    spec:
      chart: bp-foo
      version: "*"
`, helmRepoBlock("bp-foo"))
}

func generateMailHR(opt helmReleaseAppOpts) string {
	return fmt.Sprintf(`%s
spec:
  chart:
    spec:
      chart: bp-mail-tenant
      version: "*"
`, helmRepoBlock("bp-mail-tenant"))
}

// 2026-11-01: foo 1.0.0 -> 1.0.1 (a dated comment; never touched by the writer).
const DefaultHRAppChartVersions = "foo=1.0.1,mail=0.1.9"

const other = "foo=9.9.9"
'''


def _self_test():
    failures = []

    def check(cond, msg):
        print("  %s %s" % ("ok  " if cond else "FAIL", msg))
        if not cond:
            failures.append(msg)

    def refused(fn, msg):
        try:
            fn()
        except ShapeError:
            check(True, msg)
            return
        check(False, msg)

    def diff_lines(a, b):
        al, bl = a.split("\n"), b.split("\n")
        return [i for i, (x, y) in enumerate(zip(al, bl)) if x != y] if len(al) == len(bl) else None

    print("[1] the mapping comes from the generator: slug -> chart")
    check(slug_charts(_FIXTURE) == {"foo": "bp-foo", "mail": "bp-mail-tenant"}, "foo -> bp-foo, mail -> bp-mail-tenant")

    print("[2] only the one chart's entry moves; every other byte stays")
    new, rep = sync_pin(_FIXTURE, "bp-foo", "1.0.2")
    check(rep["updated"] == [("foo", "1.0.1", "1.0.2")], "foo 1.0.1 -> 1.0.2 reported")
    check('const DefaultHRAppChartVersions = "foo=1.0.2,mail=0.1.9"' in new, "the constant reads foo=1.0.2,mail=0.1.9")
    check(diff_lines(_FIXTURE, new) is not None and len(diff_lines(_FIXTURE, new)) == 1, "exactly one line differs")
    check('const other = "foo=9.9.9"' in new and "foo 1.0.0 -> 1.0.1 (a dated" in new, "the other constant and the comment are untouched")
    new_m, rep_m = sync_pin(_FIXTURE, "bp-mail-tenant", "0.1.10")
    check('"foo=1.0.1,mail=0.1.10"' in new_m and rep_m["matched"] == ["mail"],
          "a chart whose slug differs from its name (bp-mail-tenant -> mail) moves its entry, numerically above 0.1.9")

    print("[3] a chart without an entry, or at its version, is a byte no-op")
    same, rep_n = sync_pin(_FIXTURE, "bp-keycloak", "26.0.0")
    check(same == _FIXTURE and rep_n["matched"] == [], "bp-keycloak: no entry, file unchanged")
    same2, rep_s = sync_pin(_FIXTURE, "bp-foo", "1.0.1")
    check(same2 == _FIXTURE and rep_s["updated"] == [], "bp-foo at 1.0.1: file unchanged")
    same3, rep_x = sync_pin(_FIXTURE, "bp-keycloak", "not-a-version")
    check(same3 == _FIXTURE, "a chart without an entry is not judged on its version")

    print("[4] a newer entry is never moved back (FND-018)")
    kept, rep_k = sync_pin(_FIXTURE, "bp-foo", "1.0.0")
    check(kept == _FIXTURE and rep_k["kept_newer"] == [("foo", "1.0.1")], "foo stays at 1.0.1 when the run published 1.0.0")
    kept2, rep_k2 = sync_pin(_FIXTURE.replace("foo=1.0.1", "foo=1.10.0"), "bp-foo", "1.9.9")
    check(rep_k2["updated"] == [] and rep_k2["kept_newer"] == [("foo", "1.10.0")], "1.10.0 is above 1.9.9: compared as numbers")

    print("[5] an unexpected shape fails, nothing is written")
    refused(lambda: sync_pin(_FIXTURE.replace("const DefaultHRAppChartVersions", "var DefaultHRAppChartVersions"), "bp-foo", "1.0.2"),
            "no constant line")
    refused(lambda: sync_pin(_FIXTURE + 'const DefaultHRAppChartVersions = "foo=1.0.1"\n', "bp-foo", "1.0.2"),
            "two constant lines")
    refused(lambda: sync_pin(_FIXTURE.replace("foo=1.0.1,", "foo=1.0.1 ,"), "bp-foo", "1.0.2"), "an entry with a space")
    refused(lambda: sync_pin(_FIXTURE.replace("foo=1.0.1,", "foo=v1.0.1,"), "bp-foo", "1.0.2"), "an entry that is not MAJOR.MINOR.PATCH")
    refused(lambda: sync_pin(_FIXTURE.replace("mail=0.1.9", "foo=0.1.9"), "bp-foo", "1.0.2"), "a slug twice")
    refused(lambda: sync_pin(_FIXTURE.replace("mail=0.1.9", "mail=0.1.9,bar=2.0.0"), "bp-keycloak", "1.0.0"),
            "a slug the generator does not render, also for a chart without an entry")
    refused(lambda: sync_pin(_FIXTURE.replace("      chart: bp-mail-tenant\n", ""), "bp-foo", "1.0.2"),
            "a generator with no chart line")
    refused(lambda: sync_pin(_FIXTURE.replace("      chart: bp-foo\n", "      chart: bp-foo\n      chart: bp-bar\n"), "bp-foo", "1.0.2"),
            "a generator with two chart names")
    refused(lambda: sync_pin(_FIXTURE, "bp-foo", "1.0.2-rc1"), "a run version that is not MAJOR.MINOR.PATCH")

    print("[6] the real file: every entry maps to a chart; a bump moves exactly one line")
    if os.path.exists(DEFAULT_GO):
        real = open(DEFAULT_GO).read()
        _, entries = read_pin(real)
        charts = slug_charts(real)
        check(all(s in charts for s, _ in entries) and len(entries) >= 4,
              "%d entries, each rendered by the generator: %s" % (len(entries), ", ".join("%s->%s" % (s, charts.get(s)) for s, _ in entries)))
        check(charts.get("stalwart-mail") == "bp-stalwart-tenant", "stalwart-mail installs bp-stalwart-tenant")
        slug, cur = entries[0]
        same_r, _ = sync_pin(real, charts[slug], cur)
        check(same_r == real, "%s at its own version %s is a byte no-op" % (charts[slug], cur))
        maj, mnr, pat = cur.split(".")
        up, rep_r = sync_pin(real, charts[slug], "%s.%s.%d" % (maj, mnr, int(pat) + 1))
        check(len(rep_r["updated"]) == 1 and diff_lines(real, up) is not None and len(diff_lines(real, up)) == 1,
              "a bump of %s rewrites one line" % charts[slug])
    else:
        check(False, "the real funnel file exists at %s" % DEFAULT_GO)

    print()
    if failures:
        print("SELF-TEST FAILED: %d assertion(s) failed" % len(failures))
        return 1
    print("SELF-TEST PASSED: all assertions green")
    return 0


def main(argv=None):
    ap = argparse.ArgumentParser(prog="sync-funnel-pin.py",
                                 description="Format-preserving funnel chart pin sync for a chart bump.")
    ap.add_argument("chart", nargs="?", help="chart name, e.g. bp-newapi")
    ap.add_argument("version", nargs="?", help="version to pin, e.g. 1.4.160")
    ap.add_argument("--file", default=DEFAULT_GO, help="path to helmrelease_apps.go")
    ap.add_argument("--quiet", action="store_true", help="suppress INFO output")
    ap.add_argument("--self-test", action="store_true", help="run the embedded tests and exit")
    args = ap.parse_args(argv)
    if args.self_test:
        return _self_test()
    if not args.chart or not args.version:
        ap.error("both <chart-name> and <version> are required (or use --self-test)")
    return _run(args.chart, args.version, args.file, args.quiet)


if __name__ == "__main__":
    sys.exit(main())
