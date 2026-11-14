#!/usr/bin/env python3
"""sync-catalog-seed-pin.py — keep the catalog-seed in lockstep with a chart bump.

Format-preserving updater for the catalog-seed Blueprint entry (or entries) whose
`manifests.chart` == <chart-name>. For every such entry it sets BOTH:

  * the card DISPLAY version  — the 2-space `  version:` line under `spec:`
    (the first 2-space `version:` that precedes the entry's `source:` block), and
  * the delivery pin          — the 4-space `    version:` line inside `source:`

to <version>. Nothing else in the file is touched: sibling entries stay
byte-identical, and no reflow / re-quote of the 160-chart file happens — only
the two target version lines of the matched entry (or entries) are rewritten,
preserving their existing indentation, quoting and trailing whitespace.

Why this exists (issue #5583): a deploy-bot chart bump auto-writes
`platform/<x>/chart/Chart.yaml`, the bootstrap-kit slot pins and
`platform/<x>/blueprint.yaml` spec.version — but NOT this catalog-seed template.
Since the #4432/R21 display-drift guards and the #5520 regenerate-and-prove-no-diff
gate landed, that omission reddens `main` on every bump. This helper is the
missing writer; blueprint-release.yaml runs it (then `build-catalog.mjs`) as one
atomic bump commit.

Behaviour:
  * chart NOT seeded            → no-op, exit 0 (most platform/* leaves ARE seeded;
                                   the products/catalyst umbrella is not)
  * every target line already   → no-op, exit 0
    at <version>
  * a version line templated     → that line is LEFT UNTOUCHED (never rewrite a
    (e.g. `{{ .Chart.Version    `{{ … }}` into a literal — that is the
      | quote }}`)                bp-catalyst-platform self-referential card, #4432)
  * a stale static version line  → rewritten to <version>
  * matched entry, static line   → exit 2 (unexpected shape; fail loud rather
    present but post-write          than write the wrong value — mirrors the
    verify fails                    blueprint-release.yaml defensive steps)

Exit codes:
  0  success (updated) or clean no-op (not seeded / already correct / templated)
  1  usage error
  2  unexpected file shape (matched entry but a rewrite could not be verified)

Usage:
  scripts/sync-catalog-seed-pin.py <chart-name> <version> [--file <path>] [--quiet]
  scripts/sync-catalog-seed-pin.py --self-test

The parser mirrors catalogSeedEntries() /catalogSeedPins() in
tests/e2e/bootstrap-kit/main_test.go and scripts/lib/parse-catalog-seed-pins.awk:
the seed is a Helm template (Go `{{ … }}` directives) so a plain YAML unmarshal
is impossible, but the fields we read are static YAML at a fixed 2-/4-space shape.
"""

import argparse
import os
import re
import sys
import tempfile

# Default location of the catalog-seed template, relative to this script
# (scripts/ -> repo root -> products/catalyst/chart/templates/catalog-seed/).
_REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DEFAULT_SEED = os.path.join(
    _REPO_ROOT,
    "products", "catalyst", "chart", "templates", "catalog-seed", "blueprints.yaml",
)

# Document boundary in the seed stream. Every Blueprint CR opens with `kind: Blueprint`.
_RE_KIND = re.compile(r"^kind:\s*Blueprint\s*$")
# metadata.name — the only 2-space `name:` key (spec has no direct `name` child).
_RE_NAME = re.compile(r"^  name:\s*\"?(bp-[A-Za-z0-9._-]+)\"?\s*$")
# The two block openers we must distinguish (both hold a 4-space `chart:`).
_RE_MANIFESTS = re.compile(r"^  manifests:\s*$")
_RE_SOURCE = re.compile(r"^  source:\s*$")
# A 4-space `chart:` line inside manifests: or source:.
_RE_CHART4 = re.compile(r"^    chart:\s*\"?([A-Za-z0-9][A-Za-z0-9._-]*)\"?\s*$")
# A 2-space `version:` line == card spec.version (only meaningful outside blocks).
_RE_VERSION2 = re.compile(r"^  version:\s*(.*?)\s*$")
# A 4-space `version:` line == delivery source.version (only inside source:).
_RE_VERSION4 = re.compile(r"^    version:\s*(.*?)\s*$")

# Permissive extraction of whatever sits after `version:` on a line (may be a
# static token OR a `{{ … }}` Helm template OR anything else).
_RE_RAW_VERSION = re.compile(r"^\s*version:\s*(.*?)\s*$")
# A version token we are allowed to rewrite must be a real (numeric-leading)
# semver, quoted or bare — never a `{{ … }}` Helm template. This mirrors the
# `[0-9]` anchor the Go guards (reSeedSpecVersion/reSeedVersion) key on. The
# capture group is the bare (unquoted) token, for value comparison.
_RE_STATIC_TOKEN = re.compile(r"^\"?([0-9][0-9A-Za-z._+-]*)\"?$")
# Capture: (prefix incl. `version: ` and any opening quote)(value)(closing quote)(trailing ws)
_RE_VERSION_REWRITE = re.compile(r"^(\s*version:\s*)([\"']?)([^\"'\s]+)([\"']?)(\s*)$")


class SeedShapeError(Exception):
    """Matched the target chart but the file shape defeats a safe rewrite."""


def _parse_entries(lines):
    """Return a list of entry dicts describing every `kind: Blueprint` document.

    Each entry: {name, manifests_chart, source_chart, spec_idx, source_idx}
    where *_idx is the 0-based line index of the version line to edit (or None).
    """
    entries = []
    cur = None
    in_manifests = False
    in_source = False

    def flush():
        nonlocal cur, in_manifests, in_source
        if cur is not None:
            entries.append(cur)
        cur = None
        in_manifests = False
        in_source = False

    for i, ln in enumerate(lines):
        if _RE_KIND.match(ln):
            flush()
            cur = {
                "name": None,
                "manifests_chart": None,
                "source_chart": None,
                "spec_idx": None,
                "source_idx": None,
            }
            continue
        if cur is None:
            continue

        # Block openers (each resets the other).
        if _RE_MANIFESTS.match(ln):
            in_manifests, in_source = True, False
            continue
        if _RE_SOURCE.match(ln):
            in_source, in_manifests = True, False
            continue

        # Any non-blank line indented <4 spaces ends whichever block we're in.
        # (Matches the Go parser's `!strings.HasPrefix(ln, "    ")` rule.)
        if (in_manifests or in_source) and ln.strip() != "" and not ln.startswith("    "):
            in_manifests = in_source = False

        if cur["name"] is None:
            m = _RE_NAME.match(ln)
            if m:
                cur["name"] = m.group(1)

        if in_manifests:
            m = _RE_CHART4.match(ln)
            if m and cur["manifests_chart"] is None:
                cur["manifests_chart"] = m.group(1)
            continue

        if in_source:
            m = _RE_CHART4.match(ln)
            if m and cur["source_chart"] is None:
                cur["source_chart"] = m.group(1)
            m = _RE_VERSION4.match(ln)
            if m and cur["source_idx"] is None:
                cur["source_idx"] = i
            continue

        # Outside any block: the first 2-space `version:` is the card spec.version.
        if cur["spec_idx"] is None and _RE_VERSION2.match(ln):
            cur["spec_idx"] = i

    flush()
    return entries


def _raw_version_value(line):
    """Return the raw text after `version:` on a version line, else None."""
    m = _RE_RAW_VERSION.match(line)
    if not m:
        return None
    return m.group(1)


def _static_token(raw):
    """Return the bare numeric-leading token if `raw` is a static version, else None.

    A templated value like `{{ .Chart.Version | quote }}` returns None so the
    caller leaves it untouched.
    """
    if raw is None:
        return None
    m = _RE_STATIC_TOKEN.match(raw)
    return m.group(1) if m else None


def _current_value(line):
    """Return the bare version token on a static `... version: X` line, else None."""
    return _static_token(_raw_version_value(line))


def _rewrite_line(line, version):
    """Rewrite the version token on `line` to `version`, preserving indent/quote/ws.

    Returns the new line. Raises SeedShapeError if `line` is not a rewritable
    version line.
    """
    m = _RE_VERSION_REWRITE.match(line)
    if not m:
        raise SeedShapeError("not a rewritable version line: %r" % line)
    prefix, oq, _val, cq, trail = m.groups()
    return "%s%s%s%s%s" % (prefix, oq, version, cq, trail)


def sync_seed(text, chart, version):
    """Core pure function. Given the seed file `text`, return (new_text, report).

    report = {
        matched:      int  # entries whose manifests.chart == chart
        changed:      bool
        updated:      [ (1-based line no, 'spec'|'source', old, new) ... ]
        skipped_tmpl: [ (1-based line no, 'spec'|'source', value) ... ]
    }
    Never rewrites a templated (`{{ … }}`) version line. Raises SeedShapeError
    on a matched entry whose static version line cannot be safely rewritten.
    """
    # Preserve a trailing-newline-less file faithfully.
    had_final_nl = text.endswith("\n")
    lines = text.split("\n")
    if had_final_nl:
        # split() leaves a trailing "" element for the final newline; drop it so
        # indices line up with real content lines, re-add on join.
        lines = lines[:-1]

    entries = _parse_entries(lines)
    matched = [e for e in entries if e["manifests_chart"] == chart]

    report = {"matched": len(matched), "changed": False, "updated": [], "skipped_tmpl": []}
    if not matched:
        return text, report

    for e in matched:
        for kind, idx in (("spec", e["spec_idx"]), ("source", e["source_idx"])):
            if idx is None:
                # A matched entry with no card/source version line at all is an
                # unexpected shape — every real seeded entry carries both.
                raise SeedShapeError(
                    "entry for chart %r has no %s version line — the catalog-seed "
                    "shape changed; refusing to write blind" % (chart, kind)
                )
            raw = _raw_version_value(lines[idx])
            if raw is None:
                raise SeedShapeError(
                    "%s version line %d for chart %r is unparseable: %r"
                    % (kind, idx + 1, chart, lines[idx])
                )
            token = _static_token(raw)
            if token is None:
                # Templated (e.g. `{{ .Chart.Version | quote }}`) — never rewrite
                # a Go-template into a literal. This is the bp-catalyst-platform
                # self-referential card (#4432).
                report["skipped_tmpl"].append((idx + 1, kind, raw))
                continue
            if token == version:
                continue
            new_line = _rewrite_line(lines[idx], version)
            report["updated"].append((idx + 1, kind, token, version))
            lines[idx] = new_line
            report["changed"] = True

    if not report["changed"]:
        return text, report

    # Post-write verify: every line we claim to have updated must now read back
    # as `version`, and the ONLY differences vs the original must be those lines.
    for (lineno, kind, _old, new) in report["updated"]:
        got = _current_value(lines[lineno - 1])
        if got != new:
            raise SeedShapeError(
                "post-write verify failed on %s line %d for chart %r: got %r, want %r"
                % (kind, lineno, chart, got, new)
            )

    new_text = "\n".join(lines)
    if had_final_nl:
        new_text += "\n"
    return new_text, report


def _run(chart, version, path, quiet):
    try:
        with open(path, "r") as fh:
            text = fh.read()
    except OSError as exc:
        print("::error title=sync-catalog-seed-pin::cannot read %s: %s" % (path, exc), file=sys.stderr)
        return 2

    try:
        new_text, report = sync_seed(text, chart, version)
    except SeedShapeError as exc:
        print("::error title=catalog-seed shape::%s" % exc, file=sys.stderr)
        return 2

    if report["matched"] == 0:
        if not quiet:
            print("INFO: %s is not in the catalog-seed — graceful no-op." % chart)
        return 0

    if report["skipped_tmpl"] and not quiet:
        for (lineno, kind, val) in report["skipped_tmpl"]:
            print("INFO: %s %s version (line %d) is templated (%s) — left untouched."
                  % (chart, kind, lineno, val))

    if not report["changed"]:
        if not quiet:
            print("INFO: catalog-seed already pins %s at %s (%d entr%s) — no-op."
                  % (chart, version, report["matched"], "y" if report["matched"] == 1 else "ies"))
        return 0

    # Write via temp file + atomic rename so a crash mid-write can't truncate the seed.
    d = os.path.dirname(os.path.abspath(path))
    fd, tmp = tempfile.mkstemp(dir=d, prefix=".catalog-seed-", suffix=".tmp")
    try:
        with os.fdopen(fd, "w") as out:
            out.write(new_text)
        os.replace(tmp, path)
    except Exception:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise

    if not quiet:
        for (lineno, kind, old, new) in report["updated"]:
            print("catalog-seed: %s %s version line %d %s -> %s" % (chart, kind, lineno, old, new))
        print("Synced catalog-seed for %s=%s (%d entr%s, %d line(s))."
              % (chart, version, report["matched"], "y" if report["matched"] == 1 else "ies",
                 len(report["updated"])))
    return 0


# ─────────────────────────────────────────────────────────────────────────────
# --self-test — proves both directions on a synthetic fixture before the helper
# is ever wired into the release pipeline. This is the risky part (multi-entry
# targeting), so it is unit-tested hard: right entry updated, siblings
# byte-identical, stale caught, already-correct + not-seeded + templated no-op.
# ─────────────────────────────────────────────────────────────────────────────
_FIXTURE = """{{- if .Values.catalogSeed.enabled }}
---
apiVersion: catalyst.openova.io/v1
kind: Blueprint
metadata:
  name: bp-foo
spec:
  version: "1.0.0"
  visibility: listed
  card:
    title: "Foo A"
  manifests:
    chart: bp-foo
  source:
    kind: HelmRepository
    chart: bp-foo
    version: "1.0.0"
  topology:
    supported:
    - singleton
---
apiVersion: catalyst.openova.io/v1
kind: Blueprint
metadata:
  name: bp-foo-alt
spec:
  version: "1.0.0"
  visibility: listed
  card:
    title: "Foo B (same chart, second card)"
  manifests:
    chart: bp-foo
  source:
    kind: HelmRepository
    chart: bp-foo
    version: "1.0.0"
---
apiVersion: catalyst.openova.io/v1
kind: Blueprint
metadata:
  name: bp-bar
spec:
  version: "3.4.5"
  visibility: listed
  card:
    title: "Bar sibling — must stay byte-identical"
  manifests:
    chart: bp-bar
  source:
    kind: HelmRepository
    chart: bp-bar
    version: "3.4.5"
---
apiVersion: catalyst.openova.io/v1
kind: Blueprint
metadata:
  name: bp-umbrella
spec:
  version: {{ .Chart.Version | quote }}
  visibility: unlisted
  card:
    title: "Templated umbrella — never rewrite"
  manifests:
    chart: bp-umbrella
  source:
    kind: HelmRepository
    chart: bp-umbrella
    version: {{ .Chart.Version | quote }}
{{- end }}
"""


def _self_test():
    failures = []

    def check(cond, msg):
        if not cond:
            failures.append(msg)
            print("  FAIL: %s" % msg)
        else:
            print("  ok:   %s" % msg)

    orig_lines = _FIXTURE.split("\n")

    def line_of(text, needle):
        for ln in text.split("\n"):
            if needle in ln:
                return ln
        return None

    # ── Direction 1: a stale pin is CAUGHT and both entries are updated ──
    print("[1] stale pin caught; both bp-foo cards updated 1.0.0 -> 2.0.0")
    new_text, rep = sync_seed(_FIXTURE, "bp-foo", "2.0.0")
    check(rep["matched"] == 2, "two entries match manifests.chart == bp-foo (got %d)" % rep["matched"])
    check(rep["changed"] is True, "reported changed=True")
    # 2 entries x 2 lines each = 4 rewrites.
    check(len(rep["updated"]) == 4, "exactly 4 version lines rewritten (got %d)" % len(rep["updated"]))
    check(new_text.count('version: "2.0.0"') == 4, "four '2.0.0' version lines present")
    check(new_text.count('version: "1.0.0"') == 0, "no stale '1.0.0' left for bp-foo")

    # ── Sibling byte-identical: every line NOT belonging to a bp-foo entry is
    #    unchanged. Compare the whole bp-bar + bp-umbrella + preamble regions. ──
    print("[2] sibling entries byte-identical")
    new_lines = new_text.split("\n")
    check(len(new_lines) == len(orig_lines), "line count unchanged (%d)" % len(orig_lines))
    # bp-bar block and bp-umbrella block must match line-for-line.
    changed_idxs = [i for i, (a, b) in enumerate(zip(orig_lines, new_lines)) if a != b]
    # Only the four bp-foo version lines may differ.
    changed_content = sorted(orig_lines[i].strip() for i in changed_idxs)
    check(all(c == 'version: "1.0.0"' for c in changed_content) and len(changed_idxs) == 4,
          "only the 4 bp-foo version lines differ (changed lines: %s)" % changed_idxs)
    check(line_of(new_text, "Bar sibling") is not None, "bp-bar card intact")
    check('version: "3.4.5"' in new_text, "bp-bar version 3.4.5 untouched")

    # ── Templated umbrella never rewritten ──
    print("[3] templated version lines left untouched")
    check(new_text.count("{{ .Chart.Version | quote }}") == 2, "both templated lines preserved")
    _t, rep_t = sync_seed(_FIXTURE, "bp-umbrella", "9.9.9")
    check(rep_t["matched"] == 1, "bp-umbrella matched")
    check(rep_t["changed"] is False, "bp-umbrella no-op (templated)")
    check(len(rep_t["skipped_tmpl"]) == 2, "two templated lines reported skipped")
    check(_t == _FIXTURE, "file byte-identical after templated no-op")

    # ── Direction 2: an already-correct pin is a NO-OP ──
    print("[4] already-correct pin is a no-op")
    _n2, rep2 = sync_seed(new_text, "bp-foo", "2.0.0")
    check(rep2["matched"] == 2 and rep2["changed"] is False, "second run reports no change")
    check(_n2 == new_text, "file byte-identical on idempotent re-run")

    # ── Not-seeded chart is a NO-OP ──
    print("[5] chart absent from seed is a no-op")
    _n3, rep3 = sync_seed(_FIXTURE, "bp-not-here", "1.2.3")
    check(rep3["matched"] == 0 and rep3["changed"] is False, "unseeded chart matched 0, changed False")
    check(_n3 == _FIXTURE, "file byte-identical for unseeded chart")

    # ── Bare (unquoted) version token preserves the bare style ──
    print("[6] quote style preserved (bare stays bare, quoted stays quoted)")
    bare = _FIXTURE.replace('  version: "1.0.0"', "  version: 1.0.0", 1)
    _b, repb = sync_seed(bare, "bp-foo", "2.0.0")
    check("  version: 2.0.0\n" in _b, "bare card version rewritten without adding quotes")
    check(repb["changed"] is True, "bare-style entry reported changed")

    # ── End-to-end via the real file: idempotent no-op must NOT change the file ──
    print("[7] real catalog-seed: syncing an already-correct chart is a byte no-op")
    if os.path.exists(DEFAULT_SEED):
        with open(DEFAULT_SEED) as fh:
            real = fh.read()
        # bp-guacamole is seeded; its spec/source are already in lockstep, so
        # requesting its own current version must be a byte-identical no-op.
        cur = None
        for e in _parse_entries(real.split("\n")):
            if e["manifests_chart"] == "bp-guacamole" and e["source_idx"] is not None:
                cur = _current_value(real.split("\n")[e["source_idx"]])
                break
        if cur:
            _r, repr_ = sync_seed(real, "bp-guacamole", cur)
            check(_r == real and repr_["changed"] is False,
                  "bp-guacamole at its own version %s is a byte no-op" % cur)
            # And a fresh bump moves exactly its lines, siblings byte-identical.
            _r2, repr2 = sync_seed(real, "bp-guacamole", cur + "-selftest")
            rl, r2l = real.split("\n"), _r2.split("\n")
            diff_idx = [i for i, (a, b) in enumerate(zip(rl, r2l)) if a != b]
            check(len(diff_idx) == len(repr2["updated"]) and len(diff_idx) > 0,
                  "bump touches only the reported bp-guacamole lines (%d)" % len(diff_idx))
        else:
            print("  skip: bp-guacamole not resolvable in real seed")
    else:
        print("  skip: real catalog-seed not found at %s" % DEFAULT_SEED)

    print()
    if failures:
        print("SELF-TEST FAILED: %d assertion(s) failed" % len(failures))
        return 1
    print("SELF-TEST PASSED: all assertions green")
    return 0


def main(argv=None):
    ap = argparse.ArgumentParser(
        prog="sync-catalog-seed-pin.py",
        description="Format-preserving catalog-seed version sync for a chart bump (#5583).",
    )
    ap.add_argument("chart", nargs="?", help="chart name, e.g. bp-guacamole")
    ap.add_argument("version", nargs="?", help="version to pin, e.g. 0.2.36")
    ap.add_argument("--file", default=DEFAULT_SEED, help="path to the catalog-seed blueprints.yaml")
    ap.add_argument("--quiet", action="store_true", help="suppress INFO output")
    ap.add_argument("--self-test", action="store_true", help="run the embedded unit tests and exit")
    args = ap.parse_args(argv)

    if args.self_test:
        return _self_test()

    if not args.chart or not args.version:
        ap.error("both <chart-name> and <version> are required (or use --self-test)")

    return _run(args.chart, args.version, args.file, args.quiet)


if __name__ == "__main__":
    sys.exit(main())
