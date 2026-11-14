#!/usr/bin/env python3
"""uat-tally.py — the authoritative row count for docs/ledger/UAT.md.

Why this exists
---------------
The green percentage was being counted ad hoc, by searching each line for a
verdict glyph. That is wrong in a way that always errs OPTIMISTIC, because
evidence prose routinely mentions other verdicts:

    | 191 | ... | ❌ | ...re-walk shows the earlier ✅ no longer holds... |

A whole-line search in priority order finds the ✅ in the *evidence* and scores
the row green, even though its status cell says ❌. Measured 2026-07-27 against
the live ledger: 54 rows carry a glyph in evidence that differs from their
status cell, and the naive count reported 204/280 = 72.9% where the truth was
181/281 = 64.4% — overstated by 23 rows.

An error that only ever inflates the headline is the worst kind to leave
uninstrumented, so the count lives here rather than in whichever shell pipeline
a walker happens to write next.

Rules
-----
* A row counts only if it is a DATA row: pipe-delimited, >= 8 fields, and its
  first cell is neither a header (``#``/``row``/``id``) nor a separator
  (``---``).
* The verdict is read from the STATUS COLUMN (field 6) and nowhere else.
* A status cell with no glyph (e.g. ``N/A``) is reported as N/A, never as green.
* The ledger has been an HTML ``<table>`` since 2026-08-20 (ca3486cf4). Every
  line is routed through ``scripts/uat_html_compat.to_pipe`` first, which
  unfolds each ``<tr id="row-…">`` into the seven-field pipe row the rules above
  describe. Reading the raw HTML counted 0 rows and reported ``0/0 = 0.0%`` on a
  286-row ledger (measured 2026-09-02) — an empty count that looked like a
  number. The adapter is line-preserving, so ``--list`` line numbers still
  point at the ``<tr>``.

Usage:  scripts/uat-tally.py [--uat docs/ledger/UAT.md] [--json] [--check N]
        --check N exits non-zero unless the green count equals N (for CI).
        scripts/uat-tally.py --provenance
        prints the one header line of UAT.md that says where its greens were
        measured: "Measured on <env>: N. Carried from earlier environments: M.
        Newest stamp: <date>."
        scripts/uat-tally.py --tally-line
        prints the tally line of UAT.md: "**Tally (N rows):** ✅ a · ❌ b ·
        ⚠️ c · ⏳ d · ☐ e".
        scripts/uat-tally.py --check-header
        exits 1 unless the tally line and the provenance line in UAT.md equal
        what this tool computes from the rows.
        scripts/uat-tally.py --write-header
        rewrites both lines in UAT.md from the rows (the SSO-flip bot runs it
        after every flip, so a flip can no longer leave them stale).
        scripts/uat-tally.py --self-test
        proves each header rule can fail, and runs the bot's own two commands.

Provenance rules
----------------
* <env> is the environment the ledger title names (`on `hwNNN``), read the way
  scripts/uat-drift-guard.py reads it. A title that names none, or says
  ``pending``, is an error: there is nothing to be measured on.
* N counts the ✅ rows whose stamp (the Result cell's date line) names <env>.
* M counts every other ✅ row, except a row stamped ``repo-…``: that one was
  verified in source and was measured on no environment at all.
* The newest stamp is the latest date found in the stamp of any data row.

Header rules
------------
* The tally line is the one line of UAT.md that starts with ``**Tally (``; the
  provenance line is the one line that starts with ``Measured on ``. Each must
  occur exactly once, or --check-header and --write-header refuse (exit 1) and
  nothing is written.
* The tally line names the data-row count, then ✅ ❌ ⚠️ ⏳ ☐ in that order,
  each with its count even when it is 0, then ◑, ⛔ and N/A in that order, each
  only when its count is not 0. The counts therefore always sum to the row
  count.
* --write-header changes those two lines and no other byte of the file.
"""
import argparse
import collections
import json
import os
import re
import subprocess
import sys
import tempfile

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from uat_html_compat import to_pipe  # noqa: E402  HTML-<table> ledger -> markdown rows

# `⏳` (PENDING) was missing here while every other reader carried it —
# uat-snapshot.py, uat-backfill.py, uat-audit.py and test_uat_table_shape.py all
# list it. So the one glyph `scripts/reset-uat.py` WRITES was the one this tally
# could not name, and 47 carried rows scored `N/A`: not merely uncounted, but
# indistinguishable in the output from a row whose Result cell is unreadable.
# That is the exact hazard UAT.md §Status-icons warns about, landing in the
# script whose numbers get quoted.
GLYPHS = ["✅", "⚠️", "◑", "❌", "☐", "⛔", "⏳"]
REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DEFAULT_UAT = os.path.join(REPO_ROOT, "docs", "ledger", "UAT.md")
SSO_FLIP = os.path.join(REPO_ROOT, "scripts", "uat-sso-flip.py")

# The ledger uses U+2016 inside evidence because a raw pipe would break the
# table. Anything that reads these rows must not "helpfully" split on it.
EVIDENCE_SEP = "‖"

# Header rules: the order of the tally line, and the start of each header line.
TALLY_ALWAYS = ["✅", "❌", "⚠️", "⏳", "☐"]
TALLY_IF_ANY = ["◑", "⛔", "N/A"]
TALLY_PREFIX = "**Tally ("
PROVENANCE_PREFIX = "Measured on "


def classify(line):
    """Return (kind, row_id, verdict). kind is data|header|separator|skip."""
    if not line.startswith("|"):
        return ("skip", None, None)
    cells = [c.strip() for c in line.split("|")]
    if len(cells) < 8:
        return ("skip", None, None)
    first = cells[1]
    if first.lower() in ("#", "row", "id"):
        return ("header", None, None)
    if first and set(first) <= set("-: "):
        return ("separator", None, None)
    status = cells[6]
    verdict = next((g for g in GLYPHS if g in status), "N/A")
    return ("data", first, verdict)


# The title line, and the environment it names, read as uat-drift-guard.py
# header_env() reads them, so the two scripts cannot disagree.
TITLE = re.compile(r"^#\s+UAT\b")
TITLE_ENV = re.compile(r"on\s+`?(hw\d+)", re.IGNORECASE)
STAMP_DATE = re.compile(r"\d{4}-\d{2}-\d{2}")
UNESCAPED_PIPE = re.compile(r"(?<!\\)\|")


def title_env(text):
    """The environment the ledger title names, or None (none named, or pending)."""
    for line in text.split("\n"):
        if TITLE.match(line):
            if re.search(r"\bpending\b", line, re.IGNORECASE):
                return None
            m = TITLE_ENV.search(line)
            return m.group(1).lower() if m else None
    return None


def provenance(text):
    """(env, measured, carried, newest) for a ledger body.

    Raises ValueError when the title names no environment.
    """
    env = title_env(text)
    if env is None:
        raise ValueError("the ledger title names no environment")
    measured = carried = 0
    newest = None
    for line in to_pipe(text).split("\n"):
        kind, _rid, verdict = classify(line.rstrip("\r"))
        if kind != "data":
            continue
        stamp = UNESCAPED_PIPE.split(line)[5].strip().lower()
        for date in STAMP_DATE.findall(stamp):
            if newest is None or date > newest:
                newest = date
        if verdict != "✅":
            continue
        if stamp == env or stamp.startswith(env + "-"):
            measured += 1
        elif not stamp.startswith("repo-"):
            carried += 1
    return env, measured, carried, newest


def provenance_line(text):
    env, measured, carried, newest = provenance(text)
    return (f"Measured on {env}: {measured}. "
            f"Carried from earlier environments: {carried}. "
            f"Newest stamp: {newest or 'none'}.")


def count(text):
    """(counts, rows, meta) for a ledger body."""
    counts = collections.Counter()
    rows = []
    meta = collections.Counter()
    # to_pipe rewrites each <tr> line in place and passes every other line
    # through, so `n` below is still the line in the file on disk.
    for n, line in enumerate(to_pipe(text).split("\n"), 1):
        kind, rid, verdict = classify(line.rstrip("\r"))
        meta[kind] += 1
        if kind == "data":
            counts[verdict] += 1
            rows.append((n, rid, verdict))
    return counts, rows, meta


def tally(path):
    with open(path, encoding="utf-8") as fh:
        text = fh.read()
    return count(text)


def tally_line(text):
    """The tally line of the header, computed from the rows."""
    counts, rows, _meta = count(text)
    parts = [f"{g} {counts[g]}" for g in TALLY_ALWAYS]
    parts += [f"{g} {counts[g]}" for g in TALLY_IF_ANY if counts[g]]
    return f"{TALLY_PREFIX}{len(rows)} rows):** " + " · ".join(parts)


def header_lines(text):
    """{prefix: computed line} for the two header lines. Raises ValueError."""
    return {TALLY_PREFIX: tally_line(text), PROVENANCE_PREFIX: provenance_line(text)}


def header_index(lines):
    """{prefix: line index} of the two header lines. Raises ValueError."""
    found = {}
    for prefix in (TALLY_PREFIX, PROVENANCE_PREFIX):
        hits = [i for i, line in enumerate(lines) if line.startswith(prefix)]
        if len(hits) != 1:
            raise ValueError(f"{len(hits)} lines start with {prefix!r}, expected exactly 1")
        found[prefix] = hits[0]
    return found


def check_header(text):
    """A list of mismatch messages; empty when both header lines are true."""
    lines = text.split("\n")
    where = header_index(lines)
    want = header_lines(text)
    problems = []
    for prefix, i in where.items():
        if lines[i] != want[prefix]:
            problems.append(f"line {i + 1} is   {lines[i]!r}\n"
                            f"  rows give  {want[prefix]!r}")
    return problems


def write_header(text):
    """The ledger body with both header lines recomputed from its rows."""
    lines = text.split("\n")
    where = header_index(lines)
    want = header_lines(text)
    for prefix, i in where.items():
        lines[i] = want[prefix]
    return "\n".join(lines)


# ── --self-test ─────────────────────────────────────────────────────────────
_ROW = ('<tr id="row-{rid}"><td><strong>{rid}</strong><br><sub>{area} · '
        '<a href="https://github.com/openova-io/openova/issues/1">#1</a></sub></td>'
        '<td><sub>{env} — read-only</sub></td>'
        '<td>{verdict}<br><sub>{env}-2026-09-02</sub></td>'
        '<td>assertion for {rid}</td><td>{env}-2026-09-02 {verdict} evidence</td></tr>')


def _ledger(header_tally, header_prov, rows):
    return "\n".join([
        "# UAT — Console regression list, last walked on `hw307.omani.works` (from 2026-09-02)",
        "",
        header_tally,
        "",
        header_prov,
        "",
        "> notes",
        "",
        '<table width="100%">',
        "<thead><tr><th>#</th><th>📷</th><th>Result</th><th>Test case</th><th>Evidence</th></tr></thead>",
        "<tbody>",
        *[_ROW.format(rid=r, area=a, verdict=v, env=e) for r, a, v, e in rows],
        "</tbody>",
        "</table>",
        "",
    ])


_ROWS = [
    ("1", "sso", "✅", "hw307"),
    ("2", "sso", "✅", "hw305"),
    ("3", "sso", "❌", "hw307"),
    ("4", "jobs", "✅", "hw307"),
    ("5", "jobs", "⚠️", "hw307"),
    ("6", "jobs", "⏳", "hw305"),
    ("7", "jobs", "☐", "hw307"),
]
_TRUE_TALLY = "**Tally (7 rows):** ✅ 3 · ❌ 1 · ⚠️ 1 · ⏳ 1 · ☐ 1"
_TRUE_PROV = "Measured on hw307: 2. Carried from earlier environments: 1. Newest stamp: 2026-09-02."


def _run(args):
    return subprocess.run([sys.executable] + args, capture_output=True, text=True)


def self_test():
    fails = []

    def check(name, got, want):
        if got != want:
            fails.append(f"{name}: got {got!r}, want {want!r}")

    good = _ledger(_TRUE_TALLY, _TRUE_PROV, _ROWS)
    # Rule: the order and the always-present zeros.
    check("tally line: order, counts, zeros shown",
          tally_line(_ledger("x", "y", _ROWS[:1] + _ROWS[3:5])),
          "**Tally (3 rows):** ✅ 2 · ❌ 0 · ⚠️ 1 · ⏳ 0 · ☐ 0")
    check("tally line: the fixture", tally_line(good), _TRUE_TALLY)
    # Rule: ◑, ⛔ and N/A appear only when present, after the five, in order.
    extra = _ROWS + [("8", "jobs", "N/A", "hw307"), ("9", "jobs", "⛔", "hw307"),
                     ("10", "jobs", "◑", "hw307")]
    check("tally line: rare verdicts appended in order",
          tally_line(_ledger("x", "y", extra)),
          "**Tally (10 rows):** ✅ 3 · ❌ 1 · ⚠️ 1 · ⏳ 1 · ☐ 1 · ◑ 1 · ⛔ 1 · N/A 1")
    # Rule: a true header passes; a stale line of either kind fails.
    check("check: true header passes", check_header(good), [])
    check("check: stale tally line fails",
          len(check_header(good.replace("✅ 3 ·", "✅ 4 ·", 1))), 1)
    check("check: stale provenance line fails",
          len(check_header(good.replace("hw307: 2.", "hw307: 9.", 1))), 1)
    # Rule: each line must occur exactly once, or the tool refuses.
    for name, bad in (("tally line missing", good.replace(_TRUE_TALLY + "\n", "", 1)),
                      ("provenance line twice", good.replace(_TRUE_PROV, _TRUE_PROV + "\n" + _TRUE_PROV, 1))):
        for fn in (check_header, write_header):
            try:
                fn(bad)
                fails.append(f"{fn.__name__}: {name} must be refused")
            except ValueError:
                pass
    # Rule: a title with no environment is refused, not written as a zero.
    try:
        write_header(good.replace("on `hw307.omani.works`", "", 1))
        fails.append("write_header: a title with no environment must be refused")
    except ValueError:
        pass
    # Rule: --write-header changes the two lines and nothing else.
    stale = _ledger("**Tally (1 rows):** ✅ 0", "Measured on hw1: 0.", _ROWS)
    written = write_header(stale)
    check("write: the result is the true ledger", written, good)
    check("write: idempotent", write_header(written), written)
    changed = [i for i, (a, b) in enumerate(zip(stale.split("\n"), written.split("\n"))) if a != b]
    check("write: only lines 3 and 5 change", changed, [2, 4])

    # The bot's flow, by its own two commands on a file: flip, then rewrite.
    with tempfile.TemporaryDirectory() as d:
        path = os.path.join(d, "UAT.md")
        with open(path, "w", encoding="utf-8") as fh:
            fh.write(good)
        out = _run([SSO_FLIP, "--ref", "manual", "--uat", path])
        check("bot: uat-sso-flip.py exit code", out.returncode, 0)
        with open(path, encoding="utf-8") as fh:
            flipped = fh.read()
        check("bot: the flip turned the two sso greens to open",
              tally_line(flipped), "**Tally (7 rows):** ✅ 1 · ❌ 1 · ⚠️ 1 · ⏳ 1 · ☐ 3")
        # CONTROL: without the rewrite, the flip leaves both lines stale.
        check("bot: after the flip alone, both lines are stale",
              _run([os.path.abspath(__file__), "--uat", path, "--check-header"]).returncode, 1)
        check("bot: after the flip alone, two mismatches", len(check_header(flipped)), 2)
        out = _run([os.path.abspath(__file__), "--uat", path, "--write-header"])
        check("bot: --write-header exit code", out.returncode, 0)
        check("bot: after --write-header, --check-header passes",
              _run([os.path.abspath(__file__), "--uat", path, "--check-header"]).returncode, 0)
        with open(path, encoding="utf-8") as fh:
            lines = fh.read().split("\n")
        check("bot: the tally line follows the rows", lines[2],
              "**Tally (7 rows):** ✅ 1 · ❌ 1 · ⚠️ 1 · ⏳ 1 · ☐ 3")
        check("bot: the provenance line follows the rows", lines[4],
              "Measured on hw307: 1. Carried from earlier environments: 0. Newest stamp: 2026-09-02.")

    if fails:
        print("FAIL")
        for f in fails:
            print("  -", f)
        return 1
    print("ok — uat-tally header rules checked")
    return 0


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--uat", default=DEFAULT_UAT)
    ap.add_argument("--json", action="store_true")
    ap.add_argument("--check", type=int, default=None)
    ap.add_argument("--list", metavar="GLYPH", help="list rows with this verdict")
    ap.add_argument("--provenance", action="store_true",
                    help="print the header line: measured here, carried, newest stamp")
    ap.add_argument("--tally-line", action="store_true",
                    help="print the header line: the row count per verdict")
    ap.add_argument("--check-header", action="store_true",
                    help="exit 1 unless both header lines equal what the rows give")
    ap.add_argument("--write-header", action="store_true",
                    help="rewrite both header lines from the rows")
    ap.add_argument("--self-test", action="store_true",
                    help="prove each header rule can fail, and run the bot's flow")
    args = ap.parse_args()

    if args.self_test:
        return self_test()

    if args.provenance or args.tally_line or args.check_header or args.write_header:
        with open(args.uat, encoding="utf-8") as fh:
            text = fh.read()
        try:
            if args.provenance:
                print(provenance_line(text))
            elif args.tally_line:
                print(tally_line(text))
            elif args.check_header:
                problems = check_header(text)
                if problems:
                    print("FAIL: the header of the ledger does not match its rows; "
                          "run: python3 scripts/uat-tally.py --write-header", file=sys.stderr)
                    for p in problems:
                        print("  " + p, file=sys.stderr)
                    return 1
                print("header: true")
            else:
                new = write_header(text)
                if new == text:
                    print("header: unchanged")
                else:
                    with open(args.uat, "w", encoding="utf-8") as fh:
                        fh.write(new)
                    print("header: rewritten")
        except ValueError as exc:
            print(f"FAIL: {exc}", file=sys.stderr)
            return 1
        return 0

    counts, rows, meta = tally(args.uat)
    total = len(rows)
    green = counts["✅"]
    pct = (100.0 * green / total) if total else 0.0

    if args.json:
        print(json.dumps({
            "total": total, "green": green, "pct": round(pct, 1),
            "counts": dict(counts),
            "headers": meta["header"], "separators": meta["separator"],
        }, ensure_ascii=False))
    elif args.list:
        for n, rid, v in rows:
            if v == args.list:
                print(f"L{n}\t{rid}\t{v}")
    else:
        print(f"UAT {os.path.relpath(args.uat, REPO_ROOT)}")
        print(f"  data rows : {total}   (headers {meta['header']}, separators {meta['separator']})")
        for g in GLYPHS + ["N/A"]:
            if counts[g]:
                print(f"  {g:<3} {counts[g]:>4}")
        print(f"  GREEN     : {green}/{total} = {pct:.1f}%")

    if args.check is not None and green != args.check:
        print(f"FAIL: green={green}, expected {args.check}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
