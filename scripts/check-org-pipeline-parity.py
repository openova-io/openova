#!/usr/bin/env python3
"""check-org-pipeline-parity.py - the parity file of the Organization pipelines is well formed; print how many steps are missing.

Usage, from anywhere inside the repository:
  python3 scripts/check-org-pipeline-parity.py               check docs/ledger/org-pipeline-parity.md
  python3 scripts/check-org-pipeline-parity.py --self-test   prove on throw-away files that every rule can fail

Exit code 0: the file is well formed. The one line printed is missing=<n>.
Exit code 1: a rule is broken. One line per finding, each starting with
  "check-org-pipeline-parity: FAIL". Nothing else is printed.
Exit code 2: the file cannot be read, or wrong usage.

The rules, exactly.

A table is a header line, the separator line under it and its rows: the lines after the separator
that start with "|", up to the first line that does not. The cells of a line are the parts between
its "|" signs, with spaces stripped. A separator line holds only "|", "-", ":" and spaces. Only two
kinds of table are read; any other table is ignored.

  Entry-point table, header cells exactly:
    Entry point | Called from | Route | Handler | Pipeline that serves it today
  Parity table, header cells exactly:
    Row | Step | Where today | What it creates | Organization-controller | Closed by

R1  The file has exactly one entry-point table, and it has one row or more.
R2  The file has one parity table or more, and they have one row or more in all.
R3  The line under a header is a separator line. Every row starts and ends with "|", has as many
    cells as its header, and has no empty cell.
R4  Row: one capital letter and two digits, for example P01. No two rows of the parity tables
    have the same Row.
R5  Where today: one location, written `<path>:<line>` in backticks. <path> is relative, has no
    "." or ".." part, no space and no backslash; <line> is a number of 1 or more.
R6  Organization-controller: the word missing, or one location as in R5 whose <path> is a file of
    the repository that has at least <line> lines.
R7  Closed by: a work-package identifier, three capital letters, "-" and three digits.

missing=<n> counts the parity rows whose Organization-controller cell is the word missing.
Paths are read from the repository root, which is the folder above the folder of this script.
Only the Python standard library is used.
"""
import os
import re
import sys
import tempfile

NAME = "check-org-pipeline-parity"
PARITY_FILE = "docs/ledger/org-pipeline-parity.md"
ENTRY_HEADER = ["Entry point", "Called from", "Route", "Handler", "Pipeline that serves it today"]
PARITY_HEADER = ["Row", "Step", "Where today", "What it creates", "Organization-controller", "Closed by"]
SEPARATOR_RE = re.compile(r"^[|:\- ]+$")
ROW_RE = re.compile(r"^[A-Z][0-9]{2}$")
LOCATION_RE = re.compile(r"^`([^`\s\\]+):([0-9]+)`$")
PACKAGE_RE = re.compile(r"^[A-Z]{3}-[0-9]{3}$")


def cells_of(line):
    s = line.strip()
    if len(s) < 2 or not s.startswith("|") or not s.endswith("|"):
        return None
    return [c.strip() for c in s[1:-1].split("|")]


def parse_location(text):
    """Return (path, line) for a well-formed location, or None."""
    m = LOCATION_RE.match(text)
    if not m:
        return None
    path, line = m.group(1), int(m.group(2))
    parts = path.split("/")
    if path.startswith("/") or any(p in ("", ".", "..") for p in parts) or line < 1:
        return None
    return path, line


def line_count(root, path):
    full = os.path.join(root, path)
    if not os.path.isfile(full):
        return None
    with open(full, "rb") as fh:
        return len(fh.read().splitlines())


def read_tables(lines):
    """Return (tables, findings). A table is (kind, header line number, rows); a row is (line number, cells or None)."""
    tables, findings = [], []
    i = 0
    while i < len(lines):
        cells = cells_of(lines[i])
        kind = None
        if cells == ENTRY_HEADER:
            kind = "entry"
        elif cells == PARITY_HEADER:
            kind = "parity"
        if kind is None:
            i += 1
            continue
        header_no = i + 1
        if i + 1 >= len(lines) or not SEPARATOR_RE.match(lines[i + 1].strip()) or "-" not in lines[i + 1]:
            findings.append("R3 line %d: the line under the %s table header is not a separator line" % (header_no, kind))
            i += 1
            continue
        rows = []
        j = i + 2
        while j < len(lines) and lines[j].strip().startswith("|"):
            rows.append((j + 1, cells_of(lines[j])))
            j += 1
        tables.append((kind, header_no, rows))
        i = j
    return tables, findings


def check_text(text, root):
    """Return (exit code, printed lines) for the text of a parity file."""
    lines = text.splitlines()
    tables, findings = read_tables(lines)
    entry = [t for t in tables if t[0] == "entry"]
    parity = [t for t in tables if t[0] == "parity"]
    if len(entry) != 1:
        findings.append("R1: the file has %d entry-point tables, it must have exactly 1" % len(entry))
    elif not entry[0][2]:
        findings.append("R1 line %d: the entry-point table has no row" % entry[0][1])
    if not parity:
        findings.append("R2: the file has no parity table")
    elif not any(t[2] for t in parity):
        findings.append("R2: the parity tables have no row")
    for kind, _, rows in entry:
        for no, cells in rows:
            if cells is None or len(cells) != len(ENTRY_HEADER) or any(c == "" for c in cells):
                findings.append("R3 line %d: an entry-point row needs %d cells, none empty" % (no, len(ENTRY_HEADER)))
    seen = {}
    missing = 0
    for kind, _, rows in parity:
        for no, cells in rows:
            if cells is None or len(cells) != len(PARITY_HEADER) or any(c == "" for c in cells):
                findings.append("R3 line %d: a parity row needs %d cells, none empty" % (no, len(PARITY_HEADER)))
                continue
            row, _step, today, _creates, controller, closer = cells
            if not ROW_RE.match(row):
                findings.append("R4 line %d: Row %r is not one capital letter and two digits" % (no, row))
            elif row in seen:
                findings.append("R4 line %d: Row %s is also on line %d" % (no, row, seen[row]))
            else:
                seen[row] = no
            if parse_location(today) is None:
                findings.append("R5 line %d: Where today %r is not one location `<path>:<line>`" % (no, today))
            if controller == "missing":
                missing += 1
            else:
                loc = parse_location(controller)
                if loc is None:
                    findings.append("R6 line %d: Organization-controller %r is neither missing nor one location `<path>:<line>`" % (no, controller))
                else:
                    count = line_count(root, loc[0])
                    if count is None:
                        findings.append("R6 line %d: %s is not a file of the repository" % (no, loc[0]))
                    elif loc[1] > count:
                        findings.append("R6 line %d: %s has %d lines, not %d" % (no, loc[0], count, loc[1]))
            if not PACKAGE_RE.match(closer):
                findings.append("R7 line %d: Closed by %r is not a work-package identifier" % (no, closer))
    if findings:
        return 1, ["%s: FAIL %s" % (NAME, f) for f in findings]
    return 0, ["missing=%d" % missing]


def run(root):
    path = os.path.join(root, PARITY_FILE)
    try:
        with open(path, encoding="utf-8") as fh:
            text = fh.read()
    except (OSError, UnicodeDecodeError) as exc:
        return 2, ["%s: cannot judge: cannot read %s: %s" % (NAME, PARITY_FILE, exc)]
    return check_text(text, root)


ENTRY_OK = (
    "| Entry point | Called from | Route | Handler | Pipeline that serves it today |\n"
    "|---|---|---|---|---|\n"
    "| Console | `ui/a.ts:1` | `POST /x` | `api/h.go:2` | the console pipeline |\n"
)
PARITY_HEAD = (
    "| Row | Step | Where today | What it creates | Organization-controller | Closed by |\n"
    "|---|---|---|---|---|---|\n"
)


def parity_row(row="P01", today="`api/h.go:3`", controller="missing", closer="CTL-202", step="DNS", creates="records"):
    return "| %s | %s | %s | %s | %s | %s |\n" % (row, step, today, creates, controller, closer)


def self_test():
    ok_rows = parity_row("P01") + parity_row("P02", controller="`ctl/c.go:3`")
    cases = [
        ("a well-formed file with one missing row passes", "# t\n\n" + ENTRY_OK + "\n" + PARITY_HEAD + ok_rows, 0, "missing=1"),
        ("a well-formed file with no missing row passes", ENTRY_OK + "\n" + PARITY_HEAD + parity_row("P02", controller="`ctl/c.go:3`"), 0, "missing=0"),
        ("rows of two parity tables are counted together", ENTRY_OK + "\n" + PARITY_HEAD + parity_row("P01") + "\ntext\n\n" + PARITY_HEAD + parity_row("O01"), 0, "missing=2"),
        ("a table of another header is ignored", ENTRY_OK + "\n| A | B |\n|---|---|\n| x | |\n\n" + PARITY_HEAD + ok_rows, 0, "missing=1"),
        ("R1 no entry-point table fails", PARITY_HEAD + ok_rows, 1, "R1: the file has 0 entry-point tables"),
        ("R1 two entry-point tables fail", ENTRY_OK + "\n" + ENTRY_OK + "\n" + PARITY_HEAD + ok_rows, 1, "R1: the file has 2 entry-point tables"),
        ("R1 an entry-point table without a row fails", ENTRY_OK.split("| Console")[0] + "\n" + PARITY_HEAD + ok_rows, 1, "the entry-point table has no row"),
        ("R2 no parity table fails", ENTRY_OK, 1, "R2: the file has no parity table"),
        ("R2 parity tables without a row fail", ENTRY_OK + "\n" + PARITY_HEAD, 1, "R2: the parity tables have no row"),
        ("R3 a header without a separator fails", ENTRY_OK + "\n" + PARITY_HEAD.splitlines()[0] + "\n" + ok_rows, 1, "is not a separator line"),
        ("R3 a parity row with a cell too few fails", ENTRY_OK + "\n" + PARITY_HEAD + "| P01 | DNS | `api/h.go:3` | records | missing |\n", 1, "a parity row needs 6 cells"),
        ("R3 a parity row with an empty cell fails", ENTRY_OK + "\n" + PARITY_HEAD + parity_row(creates=""), 1, "a parity row needs 6 cells"),
        ("R3 a parity row without the closing bar fails", ENTRY_OK + "\n" + PARITY_HEAD + parity_row().rstrip("|\n") + "\n", 1, "a parity row needs 6 cells"),
        ("R3 an entry-point row with an empty cell fails", ENTRY_OK.replace("the console pipeline", "") + "\n" + PARITY_HEAD + ok_rows, 1, "an entry-point row needs 5 cells"),
        ("R4 a malformed Row fails", ENTRY_OK + "\n" + PARITY_HEAD + parity_row("P1"), 1, "R4 line"),
        ("R4 a repeated Row fails", ENTRY_OK + "\n" + PARITY_HEAD + parity_row("P01") + parity_row("P01"), 1, "Row P01 is also on line"),
        ("R5 a Where today without backticks fails", ENTRY_OK + "\n" + PARITY_HEAD + parity_row(today="api/h.go:3"), 1, "R5 line"),
        ("R5 a Where today without a line fails", ENTRY_OK + "\n" + PARITY_HEAD + parity_row(today="`api/h.go`"), 1, "R5 line"),
        ("R5 a Where today with line 0 fails", ENTRY_OK + "\n" + PARITY_HEAD + parity_row(today="`api/h.go:0`"), 1, "R5 line"),
        ("R5 a Where today with a .. part fails", ENTRY_OK + "\n" + PARITY_HEAD + parity_row(today="`../h.go:3`"), 1, "R5 line"),
        ("R5 a Where today with two locations fails", ENTRY_OK + "\n" + PARITY_HEAD + parity_row(today="`api/h.go:3`, `api/h.go:4`"), 1, "R5 line"),
        ("R6 a controller word other than missing fails", ENTRY_OK + "\n" + PARITY_HEAD + parity_row(controller="done"), 1, "R6 line"),
        ("R6 Missing with a capital letter fails", ENTRY_OK + "\n" + PARITY_HEAD + parity_row(controller="Missing"), 1, "R6 line"),
        ("R6 a controller file that does not exist fails", ENTRY_OK + "\n" + PARITY_HEAD + parity_row(controller="`ctl/none.go:1`"), 1, "ctl/none.go is not a file of the repository"),
        ("R6 a controller line past the end of the file fails", ENTRY_OK + "\n" + PARITY_HEAD + parity_row(controller="`ctl/c.go:4`"), 1, "ctl/c.go has 3 lines, not 4"),
        ("R6 a controller location that is a folder fails", ENTRY_OK + "\n" + PARITY_HEAD + parity_row(controller="`ctl:1`"), 1, "ctl is not a file of the repository"),
        ("R6 a controller location with a .. part fails", ENTRY_OK + "\n" + PARITY_HEAD + parity_row(controller="`ctl/../ctl/c.go:1`"), 1, "R6 line"),
        ("R7 a malformed Closed by fails", ENTRY_OK + "\n" + PARITY_HEAD + parity_row(closer="CTL-20"), 1, "R7 line"),
        ("R7 a Closed by in lower case fails", ENTRY_OK + "\n" + PARITY_HEAD + parity_row(closer="ctl-202"), 1, "R7 line"),
    ]
    failed = 0
    with tempfile.TemporaryDirectory() as root:
        os.makedirs(os.path.join(root, "ctl"))
        with open(os.path.join(root, "ctl", "c.go"), "w") as fh:
            fh.write("package c\n\nfunc F() {}\n")
        for name, text, want_code, want_text in cases:
            code, out = check_text(text, root)
            good = code == want_code and any(want_text in line for line in out)
            if code == 0:
                good = good and out == [want_text]
            print("%s %s" % ("ok  " if good else "FAIL", name))
            if not good:
                failed += 1
                print("     got exit %d: %s" % (code, " / ".join(out)))
        code, out = run(root)
        good = code == 2 and len(out) == 1 and out[0].startswith(NAME + ": cannot judge:")
        print("%s %s" % ("ok  " if good else "FAIL", "a missing parity file cannot be judged (exit 2)"))
        if not good:
            failed += 1
            print("     got exit %d: %s" % (code, " / ".join(out)))
    total = len(cases) + 1
    if failed:
        print("%s: self-test FAILED: %d of %d cases not as expected" % (NAME, failed, total))
        return 1
    print("%s: self-test passed: %d of %d cases as expected" % (NAME, total, total))
    return 0


def main(argv):
    if argv == ["--self-test"]:
        return self_test()
    if argv:
        print("usage: python3 scripts/check-org-pipeline-parity.py [--self-test]")
        return 2
    root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    code, out = run(root)
    for line in out:
        print(line)
    return code


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
