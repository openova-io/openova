#!/usr/bin/env python3
"""check-doc-state.py - the state markers of the five canonical documents, and two counts that may only fall.

Usage, from anywhere inside the repository:

  python3 scripts/check-doc-state.py                check the markers and the two counts
  python3 scripts/check-doc-state.py --base <ref>   the same, and the baseline file is not higher than at <ref>
  python3 scripts/check-doc-state.py --count        print the two counts, one per line, in the form of the baseline file
  python3 scripts/check-doc-state.py --self-test    prove on throw-away repositories that every rule can fail

Exit code 0: every rule holds. 1: a rule is broken, or what a rule reads is missing or malformed;
one line per finding. 2: wrong usage.

The rules, exactly.

Documents. docs/STATUS.md, docs/ARCHITECTURE.md, docs/SECURITY.md, docs/DOD.md and docs/RUNBOOKS.md.

Sections. A second-level section starts at a line that begins with the three characters "## ".
A line inside a fenced code block is never a heading. A fence opens at a line that starts, after
any spaces, with three or more backticks or three or more tildes, and closes at a line that holds,
after any spaces, only the same character at least as many times, and spaces.

Marker. The first non-empty line after the heading of a section is its marker. It is exactly one of:

  > State: built. Evidence: <item>[, <item>...]
  > State: target. Decision: ADR-NNNN[, ADR-NNNN...]
  > State: rule. Enforced by: <item>[, <item>...]
  > State: rule. Enforced by: nothing

  An item of a built or rule marker is a gate check identifier (three capital letters, "-G", two
  digits) that docs/ledger/gates.yaml lists, or a path from the repository root that exists in
  the checkout (no leading "/", no "." or ".." part, no space). In a built marker a gate check
  must have the state "required". An item of a target marker is ADR-NNNN, and a file
  docs/adr/NNNN-<name>.md exists. In a target section the first non-empty line after the marker
  starts with "Target, not built.". "nothing" stands alone.
  A section whose first non-empty line is not one of the four forms has no valid marker. Such a
  line that starts like a marker (">", "State", ":", in any case and spacing) is also a finding.
  An item that does not resolve is a finding.

Count 1. The sections of the five documents that have no valid marker.
Count 2. The lines of tracked files that match the extended regular expression
  ADR-0001 *§ *(13|11\\.3|11\\.5|3\\.2)
  These cite sections that docs/adr/0001-catalyst-control-plane-architecture.md does not have.
  They are read with git grep over every tracked file of the checkout, except docs/adr, the plan
  file docs/ledger/plan.yaml and the folder docs/ledger/plan, whose tickets quote what they change.

Baseline. docs/ledger/doc-state-baseline.txt holds two lines: the highest allowed count 1 and the
highest allowed count 2. A count above its line is a finding. A pull request that adds a marker
or removes a citation lowers the line; no line ever rises. With --base <ref>, a line that is
higher than the same line of the file at the commit <ref> is a finding; when the file does not
exist at <ref>, that rule has nothing to compare.

Only the Python standard library, PyYAML (to read the gate manifest) and the git command are used.
"""
import glob
import os
import re
import subprocess
import sys
import tempfile

DOCUMENTS = ("docs/STATUS.md", "docs/ARCHITECTURE.md", "docs/SECURITY.md", "docs/DOD.md", "docs/RUNBOOKS.md")
BASELINE = "docs/ledger/doc-state-baseline.txt"
GATES = "docs/ledger/gates.yaml"
CITATION = r"ADR-0001 *§ *(13|11\.3|11\.5|3\.2)"
NOT_SCANNED = ("docs/adr", "docs/ledger/plan.yaml", "docs/ledger/plan")
CHECK_ID = re.compile(r"^[A-Z]{3}-G[0-9]{2}$")
ADR_ID = re.compile(r"^ADR-([0-9]{4})$")
MARKER = re.compile(r"^> State: (?:(built)\. Evidence|(target)\. Decision|(rule)\. Enforced by): ([^\s,]+(?:, [^\s,]+)*)$")
NEAR_MARKER = re.compile(r"^\s*>\s*state\s*:", re.IGNORECASE)
FENCE_OPEN = re.compile(r"^ *(`{3,}|~{3,})")


class Unreadable(Exception):
    """What a rule reads cannot be read. The check then fails: it never passes on missing evidence."""


def git(root, *args, ok=(0,)):
    r = subprocess.run(("git",) + args, cwd=root, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if r.returncode not in ok:
        raise Unreadable("git %s failed: %s" % (" ".join(args), r.stderr.decode("utf-8", "replace").strip()))
    return r.returncode, r.stdout.decode("utf-8", "surrogateescape")


def sections(text):
    """[(line number, heading, [the lines after the heading up to the next second-level heading])]."""
    out, fence = [], None
    for n, line in enumerate(text.split("\n"), 1):
        if fence:
            c, size = fence
            s = line.strip(" ")
            if s and set(s) == {c} and len(s) >= size:
                fence = None
        else:
            m = FENCE_OPEN.match(line)
            if m:
                fence = (m.group(1)[0], len(m.group(1)))
            elif line.startswith("## "):
                out.append((n, line, []))
                continue
        if out:
            out[-1][2].append(line)
    return out


def first_text(lines):
    for i, line in enumerate(lines):
        if line.strip():
            return i, line
    return None, None


class Gates:
    def __init__(self, root):
        self.root, self.states = root, None

    def state(self, cid):
        if self.states is None:
            try:
                import yaml
                with open(os.path.join(self.root, GATES), encoding="utf-8") as f:
                    data = yaml.safe_load(f)
                self.states = dict((c["id"], c["state"]) for c in data["checks"])
            except ImportError:
                raise Unreadable("PyYAML is not installed; it is needed to read %s" % GATES)
            except Exception as e:
                raise Unreadable("%s cannot be read: %s" % (GATES, e))
        return self.states.get(cid)


def resolve(root, gates, kind, item):
    """None when the item resolves, else the reason why it does not."""
    if kind == "target":
        m = ADR_ID.match(item)
        if not m:
            return "is not of the form ADR-NNNN"
        if not glob.glob(os.path.join(root, "docs/adr", m.group(1) + "-*.md")):
            return "names a decision record with no file docs/adr/%s-<name>.md" % m.group(1)
        return None
    if CHECK_ID.match(item):
        state = gates.state(item)
        if state is None:
            return "is not a gate check of %s" % GATES
        if kind == "built" and state != "required":
            return "is a gate check in the state %s; a built marker needs the state required" % state
        return None
    parts = item.split("/")
    if item.startswith("/") or "\\" in item or any(p in ("", ".", "..") for p in parts) or re.search(r"\s", item):
        return "is not a path from the repository root"
    if not os.path.lexists(os.path.join(root, item)):
        return "is a path that does not exist"
    return None


def check_document(root, gates, doc):
    """(number of sections, [(line, heading) without a valid marker], [finding])."""
    try:
        with open(os.path.join(root, doc), encoding="utf-8") as f:
            text = f.read()
    except OSError:
        raise Unreadable("%s is missing" % doc)
    unmarked, findings = [], []
    found = sections(text)
    for n, heading, body in found:
        i, line = first_text(body)
        m = MARKER.match(line) if line is not None else None
        if not m:
            unmarked.append((n, heading))
            if line is not None and NEAR_MARKER.match(line):
                findings.append("%s line %d: the marker is not one of the four forms: %r" % (doc, n + 1 + i, line))
            continue
        kind = m.group(1) or m.group(2) or m.group(3)
        items = m.group(4).split(", ")
        where = "%s line %d" % (doc, n + 1 + i)
        if kind == "rule" and items == ["nothing"]:
            continue
        for item in items:
            if item == "nothing":
                findings.append("%s: \"nothing\" must stand alone in a rule marker" % where)
                continue
            why = resolve(root, gates, kind, item)
            if why:
                findings.append("%s: the item %r %s" % (where, item, why))
        if kind == "target":
            j, after = first_text(body[i + 1:])
            if after is None or not after.startswith("Target, not built."):
                findings.append("%s: a target section must start its text with \"Target, not built.\"" % where)
    return len(found), unmarked, findings


def citations(root, numbered=False):
    """The lines of tracked files outside the excluded paths that cite a missing section of record 0001."""
    args = ["-c", "grep.lineNumber=false", "-c", "color.grep=never", "grep", "-n" if numbered else "-h", "-I", "-E", "-e", CITATION, "--", "."]
    args += [":(exclude)" + p for p in NOT_SCANNED]
    code, out = git(root, *args, ok=(0, 1))
    return [] if code == 1 else [x for x in out.split("\n") if x]


def parse_baseline(text, where):
    lines = text.split("\n")
    if lines and lines[-1] == "":
        lines.pop()
    if len(lines) != 2 or not all(x.isascii() and x.isdigit() for x in lines):
        raise Unreadable("%s must hold two lines, each a number" % where)
    return int(lines[0]), int(lines[1])


def read_baseline(root):
    try:
        with open(os.path.join(root, BASELINE), encoding="utf-8") as f:
            return parse_baseline(f.read(), BASELINE)
    except OSError:
        raise Unreadable("%s is missing" % BASELINE)


def counts(root):
    """(count 1, count 2, the unmarked sections per document, findings of the markers)."""
    gates, per_doc, findings, total = Gates(root), [], [], 0
    for doc in DOCUMENTS:
        n, unmarked, f = check_document(root, gates, doc)
        per_doc.append((doc, n, unmarked))
        findings += f
        total += len(unmarked)
    return total, len(citations(root)), per_doc, findings


def check(root, base=None):
    """([finding], [line of information]). The information comes first: the lists that a finding refers to end it."""
    unmarked, cited, per_doc, findings = counts(root)
    limit_unmarked, limit_cited = read_baseline(root)
    info = ["%s has %d sections, %d without a valid marker" % (doc, n, len(u)) for doc, n, u in per_doc]
    if unmarked > limit_unmarked:
        info.append("the sections without a valid marker:")
        info += ["  %s line %d: %s" % (doc, n, h) for doc, x, u in per_doc for n, h in u]
        findings.append("%d sections have no valid marker; %s allows %d. Put a marker under each new second-level heading. The sections are listed above." % (unmarked, BASELINE, limit_unmarked))
    if cited > limit_cited:
        info.append("the lines that cite a missing section of record 0001:")
        info += ["  " + x for x in citations(root, numbered=True)]
        findings.append("%d lines cite a missing section of record 0001; %s allows %d. The lines are listed above." % (cited, BASELINE, limit_cited))
    info.insert(len(DOCUMENTS), "%d sections without a valid marker (baseline %d), %d lines citing a missing section of record 0001 (baseline %d)" % (unmarked, limit_unmarked, cited, limit_cited))
    if base is not None:
        code, _ = git(root, "rev-parse", "--verify", "--quiet", base + "^{commit}", ok=(0, 1))
        if code != 0:
            raise Unreadable("--base %s is not a commit of this repository" % base)
        code, _ = git(root, "cat-file", "-e", "%s:%s" % (base, BASELINE), ok=(0, 1, 128))
        if code != 0:
            info.append("%s does not exist at %s; there is no earlier baseline to compare" % (BASELINE, base))
        else:
            _, text = git(root, "show", "%s:%s" % (base, BASELINE))
            old = parse_baseline(text, "%s at %s" % (BASELINE, base))
            new = read_baseline(root)
            for k in (0, 1):
                if new[k] > old[k]:
                    findings.append("%s line %d is %d here and %d at %s; a baseline may only fall" % (BASELINE, k + 1, new[k], old[k], base))
            info.append("the baseline at %s is %d and %d" % (base, old[0], old[1]))
    return findings, info


def run(root, base=None, say=print):
    """Runs the check in the repository at root. Returns the exit code."""
    try:
        findings, info = check(root, base)
    except Unreadable as e:
        findings, info = [str(e)], []
    for line in info:
        say("doc-state: %s" % line)
    for line in findings:
        say("doc-state: %s" % line)
    if findings:
        say("doc-state: FAILED, %d finding(s)" % len(findings))
        return 1
    say("doc-state: ok")
    return 0


def run_count(root, say=print):
    try:
        unmarked, cited, _, _ = counts(root)
    except Unreadable as e:
        say("doc-state: %s" % e)
        return 1
    say("%d\n%d" % (unmarked, cited))
    return 0


# ------------------------------------------------------------------------------------------------ self-test
def self_test():
    """Builds small repositories in a temporary folder and shows, for every rule, that it can fail and that it can pass."""
    env = dict((k, v) for k, v in os.environ.items() if not k.startswith("GIT_"))
    env.update(GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_SYSTEM=os.devnull, GIT_AUTHOR_NAME="self-test", GIT_AUTHOR_EMAIL="self-test@example.invalid",
               GIT_COMMITTER_NAME="self-test", GIT_COMMITTER_EMAIL="self-test@example.invalid")
    passed, failed = [], []
    cite = "ADR-0001 " + "\u00a7" + "13"

    def sh(root, *args):
        subprocess.run(args, cwd=root, env=env, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

    def write(root, path, text):
        full = os.path.join(root, path)
        os.makedirs(os.path.dirname(full), exist_ok=True)
        with open(full, "w", encoding="utf-8") as f:
            f.write(text)

    def commit(root):
        sh(root, "git", "add", "-A")
        sh(root, "git", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "state")

    gates = "schema: openova.io/gates/v1\nchecks:\n- id: FND-G01\n  state: required\n- id: FND-G02\n  state: not-built\n"
    good = ("# Title\n\nintro\n\n## Built part\n\n> State: built. Evidence: scripts/tool.sh, FND-G01\n\ntext\n\n"
            "## Target part\n> State: target. Decision: ADR-0016\n\nTarget, not built. More.\n\n"
            "## Rule part\n\n> State: rule. Enforced by: FND-G02, scripts\n\n"
            "## Rule without guard\n> State: rule. Enforced by: nothing\n\n### Sub\n\n```md\n## Not a heading\n```\n")
    plain = "# Title\n\n## One\n\ntext\n\n~~~~\n## fenced\n~~~\n## still fenced\n~~~~\n\n### Three\n##Not a heading\n"
    base_files = {GATES: gates, "docs/adr/0016-x.md": "x\n", "scripts/tool.sh": "x\n", "docs/STATUS.md": good,
                  "docs/ARCHITECTURE.md": plain, "docs/SECURITY.md": "", "docs/DOD.md": "## A\n## B\n", "docs/RUNBOOKS.md": "text\n"}

    def repo(name, changes=None, baseline="3\n0\n", extra=None):
        root = os.path.join(tmp, name)
        os.makedirs(root)
        sh(root, "git", "init", "-q")
        files = dict(base_files)
        files[BASELINE] = baseline
        files.update(changes or {})
        for path, text in files.items():
            if text is not None:
                write(root, path, text)
        commit(root)
        return root

    def case(name, root, code, text="", base=None, count=False):
        lines = []
        got = run_count(root, lines.append) if count else run(root, base, lines.append)
        ok = got == code and text in "\n".join(lines)
        (passed if ok else failed).append(name)
        if not ok:
            print("self-test: WRONG: %s: exit code %d, expected %d, with the text %r. Output:\n  %s" % (name, got, code, text, "\n  ".join(lines)))

    def doc(text):
        return {"docs/STATUS.md": text}

    with tempfile.TemporaryDirectory() as tmp:
        # sections and markers
        r = repo("ok")
        case("the four marker forms resolve; fenced and third-level headings are not sections", r, 0, "3 sections without a valid marker (baseline 3)")
        case("--count prints the two counts", r, 0, "3\n0", count=True)
        case("a document counts each section once", r, 0, "docs/ARCHITECTURE.md has 1 sections, 1 without a valid marker")
        r = repo("over", baseline="2\n0\n")
        case("count 1 above the baseline fails", r, 1, "3 sections have no valid marker; %s allows 2" % BASELINE)
        case("a failure lists the unmarked sections", r, 1, "docs/DOD.md line 2: ## B")
        r = repo("under", baseline="9\n0\n")
        case("count 1 below the baseline passes", r, 0, "doc-state: ok")
        r = repo("late", doc("## A\n\ntext\n> State: rule. Enforced by: nothing\n"), baseline="9\n0\n")
        case("a marker that is not the first non-empty line does not count", r, 0, "docs/STATUS.md has 1 sections, 1 without a valid marker")
        r = repo("late2", doc("## A\n\ntext\n> State: rule. Enforced by: nothing\n"), baseline="2\n0\n")
        case("so the section is unmarked and can break the baseline", r, 1, "4 sections have no valid marker")
        for n, bad in enumerate(("> State: done", "> State: built. Evidence:", "> State: rule. Enforced by: nothing ", "> state: rule. Enforced by: nothing", ">State: rule. Enforced by: nothing",
                                 "> State: built. Evidence: scripts/tool.sh,FND-G01")):
            r = repo("malformed-%d" % n, doc("## A\n\n%s\n" % bad), baseline="9\n0\n")
            case("the malformed marker %r fails" % bad, r, 1, "the marker is not one of the four forms")
        r = repo("missing-path", doc("## A\n> State: built. Evidence: scripts/gone.sh\n"), baseline="9\n0\n")
        case("a built marker naming a missing path fails", r, 1, "'scripts/gone.sh' is a path that does not exist")
        r = repo("rule-path", doc("## A\n> State: rule. Enforced by: scripts/gone.sh\n"), baseline="9\n0\n")
        case("a rule marker naming a missing path fails", r, 1, "'scripts/gone.sh' is a path that does not exist")
        for n, bad in enumerate(("../x", "/scripts/tool.sh", "scripts/./tool.sh", "scripts//tool.sh", "`scripts/tool.sh`")):
            r = repo("bad-path-%d" % n, doc("## A\n> State: built. Evidence: %s\n" % bad), baseline="9\n0\n")
            case("the item %r fails" % bad, r, 1, "is not a path from the repository root" if "`" not in bad else "does not exist")
        r = repo("not-required", doc("## A\n> State: built. Evidence: FND-G02\n"), baseline="9\n0\n")
        case("a built marker naming a check that is not required fails", r, 1, "in the state not-built; a built marker needs the state required")
        r = repo("unknown-check", doc("## A\n> State: rule. Enforced by: FND-G09\n"), baseline="9\n0\n")
        case("a marker naming a check the manifest does not list fails", r, 1, "'FND-G09' is not a gate check of %s" % GATES)
        r = repo("no-gates", dict(doc("## A\n> State: rule. Enforced by: FND-G01\n"), **{GATES: None}), baseline="9\n0\n")
        case("a check named while the manifest is missing fails", r, 1, "%s cannot be read" % GATES)
        r = repo("no-gates-needed", {GATES: None, "docs/STATUS.md": "## A\n> State: rule. Enforced by: scripts\n"}, baseline="9\n0\n")
        case("the manifest is read only when a marker names a check", r, 0, "doc-state: ok")
        r = repo("adr-missing", doc("## A\n> State: target. Decision: ADR-0099\n\nTarget, not built.\n"), baseline="9\n0\n")
        case("a target marker naming a missing record fails", r, 1, "names a decision record with no file docs/adr/0099-<name>.md")
        r = repo("adr-form", doc("## A\n> State: target. Decision: 0016\n\nTarget, not built.\n"), baseline="9\n0\n")
        case("a target item that is not ADR-NNNN fails", r, 1, "is not of the form ADR-NNNN")
        r = repo("target-text", doc("## A\n> State: target. Decision: ADR-0016\n\nThis is built.\n"), baseline="9\n0\n")
        case("a target section that does not start with the words fails", r, 1, "must start its text with \"Target, not built.\"")
        r = repo("target-empty", doc("## A\n> State: target. Decision: ADR-0016\n## B\n> State: rule. Enforced by: nothing\n"), baseline="9\n0\n")
        case("an empty target section fails", r, 1, "must start its text with \"Target, not built.\"")
        r = repo("nothing-mixed", doc("## A\n> State: rule. Enforced by: nothing, scripts\n"), baseline="9\n0\n")
        case("\"nothing\" beside another item fails", r, 1, "\"nothing\" must stand alone")
        r = repo("no-doc", {"docs/DOD.md": None})
        case("a missing document fails", r, 1, "docs/DOD.md is missing")
        case("--count with a missing document fails", r, 1, "docs/DOD.md is missing", count=True)
        # citations
        r = repo("cite", {"a/one.md": "see %s here\nand ADR-0001 \u00a7 11.3\n" % cite, "docs/adr/0002-y.md": cite + "\n",
                          "docs/ledger/plan.yaml": cite + "\n", "docs/ledger/plan/tickets/X.md": cite + "\n", "b/two.md": "ADR-0001 \u00a712 and ADR-0001 \u00a72.7\n"}, baseline="3\n2\n")
        case("citations in tracked files count, one per line", r, 0, "2 lines citing a missing section of record 0001 (baseline 2)")
        case("--count prints the citation count", r, 0, "3\n2", count=True)
        write(r, "c/new.md", cite + "\n")
        case("a file that git does not track is not read", r, 0, "2 lines citing")
        commit(r)
        case("a citation above the baseline fails", r, 1, "3 lines cite a missing section of record 0001; %s allows 2" % BASELINE)
        case("a failure lists the citing lines", r, 1, "  c/new.md:1:")
        # baseline
        for n, text in enumerate(("3\n", "3\nzero\n", "3\n0\n1\n", "-1\n0\n", "")):
            r = repo("baseline-%d" % n, baseline=text)
            case("the malformed baseline %r fails" % text, r, 1, "must hold two lines, each a number")
        r = repo("baseline-missing", baseline=None)
        case("a missing baseline fails", r, 1, "%s is missing" % BASELINE)
        # --base
        r = repo("base", baseline=None)
        write(r, BASELINE, "4\n1\n")
        commit(r)
        case("--base: no baseline at the base commit is nothing to compare", r, 0, "does not exist at HEAD~1", base="HEAD~1")
        write(r, BASELINE, "3\n0\n")
        commit(r)
        case("--base: a lower baseline passes", r, 0, "the baseline at HEAD~1 is 4 and 1", base="HEAD~1")
        case("--base: an equal baseline passes", r, 0, "doc-state: ok", base="HEAD")
        write(r, BASELINE, "3\n2\n")
        case("--base: a line higher than at the base fails", r, 1, "line 2 is 2 here and 0 at HEAD; a baseline may only fall", base="HEAD")
        case("--base: a name that is no commit fails", r, 1, "--base no-such-ref is not a commit", base="no-such-ref")
    if failed or not passed:
        print("self-test: FAILED, %d of %d cases wrong" % (len(failed), len(failed) + len(passed)))
        return 1
    print("self-test: %d cases passed" % len(passed))
    return 0


def main(argv):
    if argv == ["--self-test"]:
        return self_test()
    if argv not in ([], ["--count"]) and not (len(argv) == 2 and argv[0] == "--base" and argv[1]):
        print("usage: python3 scripts/check-doc-state.py [--base <ref> | --count | --self-test]", file=sys.stderr)
        return 2
    try:
        _, top = git(os.getcwd(), "rev-parse", "--show-toplevel")
    except (Unreadable, OSError) as e:
        print("doc-state: %s" % e)
        return 1
    root = top.rstrip("\n")
    if argv == ["--count"]:
        return run_count(root)
    return run(root, argv[1] if argv else None)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
