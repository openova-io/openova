#!/usr/bin/env python3
"""ticket-lint.py - a ticket is complete, leaves no choice open, and equals its plan entry.

The ticket of a work package, docs/ledger/plan/tickets/<ID>.md, is the whole instruction for the
model that does the work. That model makes no design decision. A ticket that lacks a field, leaves
a choice open, or says something other than the plan entry that the checks of the pull request judge
by, is wrong before it is handed out. This tool refuses such a ticket.

    python3 scripts/ticket-lint.py FILE [FILE ...]   one line per violation; prints nothing when every file is clean
    python3 scripts/ticket-lint.py --self-test       one faulty ticket per rule, each refused

The rules. Each is shown to be able to fail by the self-test.
  1. Front matter: the lines id, ticket_rev, plan_commit, lane, type and size between two lines "---".
     id is the file name, plan_commit is 40 hexadecimal characters, size is XS, S or M.
  2. The title line "# <ID> — <title>".
  3. The part in plain words: "## In plain words" with the problem today, what changes, and two to seven
     sentences under "Done means:". No code in it.
  4. RULES OF CONDUCT: rules numbered 1 to 25. When several tickets are read, their rules are word for word the same.
  5. The headings: "## In plain words", the 21 fields "## 1. Supersede banner" to "## 21. Return format" in order,
     each with text, then "## If this ticket is wrong". No other heading of that level.
  6. Command items. Field 12 has one item C1 with a command and a one-line expected output. Field 13 has the items
     P1, P2, ... each with a command and an expected output or exit code. Field 18 has the items A1, A2, ... each with
     a command and no expected value. A command of C1 or of an A item uses no tool the acceptance job lacks.
  7. Field 14 has one code block with one path or glob per line.
  8. The work steps of field 16 are numbered 1, 2, 3 with no gap. They hold no vague phrase, no step that the fixed
     start and finish steps own (branch, staging, commit, push, pull request, marker), and no wait with sleep.
  9. Every embedded file (between "<!-- BEGIN name -->" and "<!-- END name -->") has its SHA-256 in the ticket.
 10. None of the four words that scripts/check-no-banned-words.sh refuses.
 11. Equal to the plan entry in docs/ledger/plan.yaml: title, ticket_rev, lane, type and size; field 12 equals the
     precondition, field 14 equals touches, field 18 equals the acceptance commands.

python3 scripts/plan.py validate applies the same rules to the ticket of every package with ticket_rev 1 or more.
Exit codes: 0 every file is clean; 1 a violation; 3 the tool could not do its work.
The self-test needs the Python standard library only. Reading the plan file needs PyYAML.
"""
import hashlib
import os
import re
import sys

PLAN = "docs/ledger/plan.yaml"
META = ["id", "ticket_rev", "plan_commit", "lane", "type", "size"]
SIZES = ["XS", "S", "M"]
FIELDS = ["Supersede banner", "End goal", "The model (the law)", "Measured current state", "Exact target state",
          "Code paths to read first", "History and must-preserve", "Access runbook", "Execution constraints",
          "Definition of done", "Explicitly excluded", "Precondition", "Pre-flight checks", "Allowed paths",
          "Forbidden paths", "Steps", "Stop conditions", "Acceptance checks", "Rollback", "Locks, gate check, budget",
          "Return format"]
HEADINGS = ["In plain words"] + ["%d. %s" % (n, f) for n, f in enumerate(FIELDS, 1)] + ["If this ticket is wrong"]
VAGUE = ["as needed", "appropriately", "etc.", "similar", "and so on", "if necessary", "as appropriate", "or equivalent", "for example,"]
FIXED_STEPS = ["git push", "gh pr ", "git commit", "git add", "plan/done/", "git switch", "git checkout"]
NOT_IN_ACCEPTANCE = ["kubectl", "helm upgrade", "helm install", "tofu apply", "terraform", "curl", "wget", "gh ", "kyverno", "docker", "cosign", "trivy"]
# The patterns of scripts/check-no-banned-words.sh, case-insensitive.
BANNED = [r"\bMVP\b", r"\biterat(ion|ions|ive|ively)\b", r"\bout[ -_]of[ -_]scope\b", r"\bblocker[s]?\b"]
ITEM = re.compile(r"^\*\*([A-Z]\d+)\.\*\*[ \t]*(.*)$")
FENCE = re.compile(r"^(`{3,}|~{3,})")


def sections(text):
    """The level-two headings outside code blocks, in order, and the text under each."""
    out, cur, buf, fence = [], None, [], None
    for line in text.split("\n"):
        f = FENCE.match(line)
        if f and fence is None:
            fence = f.group(1)[0] * len(f.group(1))
        elif f and line.strip() == fence:
            fence = None
        elif fence is None and line.startswith("## "):
            if cur is not None:
                out.append((cur, "\n".join(buf).strip("\n")))
            cur, buf = line[3:].strip(), []
            continue
        if cur is not None:
            buf.append(line)
    if cur is not None:
        out.append((cur, "\n".join(buf).strip("\n")))
    return out


def prose(text):
    """The text without its code blocks."""
    return re.sub(r"^(`{3,})[^\n]*\n.*?\n\1[ \t]*$", "", text, flags=re.S | re.M)


def items(body, letter):
    """**P1.** why, a ```sh block, then Expected output: `x`, an output block, or Expected exit code: `n`."""
    found, lines, i = [], body.split("\n"), 0
    while i < len(lines):
        m = ITEM.match(lines[i])
        if not m:
            i += 1
            continue
        it = {"id": m.group(1), "text": m.group(2).strip(), "run": None, "expect": None, "kind": None}
        i += 1
        while i < len(lines) and not lines[i].startswith("```") and not ITEM.match(lines[i]):
            it["text"] = (it["text"] + " " + lines[i].strip()).strip()
            i += 1
        if i < len(lines) and re.match(r"^```(sh|bash)?\s*$", lines[i]):
            j = i + 1
            while j < len(lines) and lines[j].rstrip() != "```":
                j += 1
            it["run"] = "\n".join(lines[i + 1:j]).strip("\n")
            i = j + 1
        while i < len(lines) and not lines[i].strip():
            i += 1
        if i < len(lines):
            e1 = re.match(r"^Expected output: `(.*)`\s*$", lines[i])
            e2 = re.match(r"^Expected exit code: `(\d+)`\s*$", lines[i])
            if e1 or e2:
                it["kind"], it["expect"] = ("output", e1.group(1)) if e1 else ("exit", e2.group(1))
                i += 1
            elif re.match(r"^Expected output:\s*$", lines[i]) and i + 1 < len(lines) and lines[i + 1].startswith("```"):
                j = i + 2
                while j < len(lines) and lines[j].rstrip() != "```":
                    j += 1
                it["kind"], it["expect"] = "output", "\n".join(lines[i + 2:j])
                i = j + 1
        found.append(it)
    out = []
    for it in found:
        if not it["id"].startswith(letter):
            out.append("item %s: its identifier must start with %s" % (it["id"], letter))
        if not it["run"]:
            out.append("item %s: it has no command block" % it["id"])
    return found, out


def lint_one(name, text, pkg):
    """Rules 1 to 3 and 5 to 11 for one ticket. pkg is its plan entry, or None. Returns (lines, rules of conduct)."""
    v = []
    fm = re.match(r"^---\n(.*?)\n---\n", text, re.S)
    meta = dict(re.findall(r"^(\w+): (.*)$", fm.group(1), re.M)) if fm else {}
    stem = os.path.basename(name)[:-3] if name.endswith(".md") else name
    if not fm or sorted(meta) != sorted(META):
        v.append("rule 1: the front matter must hold exactly the lines " + ", ".join(META))
    if meta.get("id") != stem:
        v.append("rule 1: id is %r, the file is %s" % (meta.get("id"), stem))
    if not re.match(r"^[0-9a-f]{40}$", meta.get("plan_commit", "")):
        v.append("rule 1: plan_commit is not 40 hexadecimal characters")
    if meta.get("size") not in SIZES:
        v.append("rule 1: size is %r, not XS, S or M" % meta.get("size"))
    t = re.search(r"^# ([A-Z]{3}-(?:[0-9]{3}|S[0-9])) — (.+)$", text, re.M)
    if not t or t.group(1) != stem:
        v.append("rule 2: no title line '# %s — <title>'" % stem)
    secs = sections(text)
    heads = [h for h, _ in secs]
    if heads != HEADINGS:
        v.append("rule 5: the headings are not In plain words, the 21 fields in order and If this ticket is wrong")
    body = dict(secs)
    for h in HEADINGS:
        if h in body and not body[h].strip():
            v.append("rule 5: '## %s' has no text" % h)
    plain = body.get("In plain words", "")
    rules = plain.split("RULES OF CONDUCT:", 1)[1] if "RULES OF CONDUCT:" in plain else ""
    words = plain.split("RULES OF CONDUCT:", 1)[0]
    done = re.findall(r"^- \S", words.split("**Done means:**", 1)[1], re.M) if "**Done means:**" in words else []
    if "**The problem today.** " not in words or "**What changes.** " not in words or not 2 <= len(done) <= 7:
        v.append("rule 3: the part in plain words needs the problem today, what changes and two to seven lines under Done means")
    if "`" in words:
        v.append("rule 3: the part in plain words holds code")
    if [int(n) for n in re.findall(r"^(\d+)\. ", rules, re.M)] != list(range(1, 26)):
        v.append("rule 4: the rules of conduct are not numbered 1 to 25")
    pre, l12 = items(body.get("12. Precondition", ""), "C")
    if len(pre) != 1 or pre[0]["id"] != "C1" or pre[0]["kind"] != "output" or "\n" in (pre[0]["expect"] or "") or l12:
        v.append("rule 6: field 12 must hold one item C1 with a command and a one-line expected output")
    pf, l13 = items(body.get("13. Pre-flight checks", ""), "P")
    v += ["rule 6: field 13: " + x for x in l13]
    if [it["id"] for it in pf] != ["P%d" % n for n in range(1, len(pf) + 1)]:
        v.append("rule 6: field 13: the items are not P1, P2, ... in order")
    v += ["rule 6: field 13: %s has no expected output or exit code" % it["id"] for it in pf if it["kind"] is None]
    acc, l18 = items(body.get("18. Acceptance checks", ""), "A")
    v += ["rule 6: field 18: " + x for x in l18]
    if not acc or [it["id"] for it in acc] != ["A%d" % n for n in range(1, len(acc) + 1)]:
        v.append("rule 6: field 18: the items are not A1, A2, ... in order")
    v += ["rule 6: field 18: %s states an expected value; exit code 0 is the only pass" % it["id"] for it in acc if it["kind"] is not None]
    for it in pre + acc:
        for bad in NOT_IN_ACCEPTANCE:
            if it["run"] and re.search(r"(^|[\s;&|(`$])" + re.escape(bad), it["run"]):
                v.append("rule 6: %s uses '%s', which the acceptance job does not have or may not run" % (it["id"], bad.strip()))
    blocks = re.findall(r"^```[a-z]*\n(.*?)\n```\s*$", body.get("14. Allowed paths", ""), re.S | re.M)
    touches = [x.strip() for x in blocks[0].split("\n") if x.strip()] if len(blocks) == 1 else []
    if len(blocks) != 1 or not touches or any(re.search(r"[?\[\]{}! ]", x) or x.startswith("/") for x in touches):
        v.append("rule 7: field 14 must be one code block with one path or glob per line")
    steps = body.get("16. Steps", "")
    work = steps.split("**Work.**", 1)[1].split("\n**Finish.**", 1)[0] if "**Work.**" in steps and "\n**Finish.**" in steps else ""
    nums = [int(n) for n in re.findall(r"^(\d+)\. ", work, re.M)]
    if not nums or nums != list(range(1, len(nums) + 1)):
        v.append("rule 8: the work steps are not numbered 1, 2, 3 with no gap")
    v += ["rule 8: a work step uses the vague phrase %r" % p for p in VAGUE if p in work.lower()]
    v += ["rule 8: a work step holds %r, which belongs to the fixed start and finish steps" % p for p in FIXED_STEPS if p in work]
    if re.search(r"(^|[\s`;&|(])sleep \d", prose(work)):
        v.append("rule 8: a work step waits with sleep")
    emb = re.findall(r"^<!-- BEGIN (\S+) -->\n(`{3,})[^\n]*\n(.*?)\n\2\n<!-- END \1 -->$", text, re.S | re.M)
    if len(re.findall(r"^<!-- BEGIN \S+ -->$", text, re.M)) != len(emb):
        v.append("rule 9: an embedded file is not one code block between <!-- BEGIN name --> and <!-- END name -->")
    for n, _, content in emb:
        if hashlib.sha256((content + "\n").encode()).hexdigest() not in text:
            v.append("rule 9: the embedded file %s has no SHA-256 in the ticket" % n)
    for p in BANNED:
        if re.search(p, text, re.I):
            v.append("rule 10: the ticket holds a word that scripts/check-no-banned-words.sh refuses (%s)" % p)
    if pkg is None:
        v.append("rule 11: %s is not a package of %s" % (stem, PLAN))
    else:
        want = {"ticket_rev": str(pkg.get("ticket_rev")), "lane": pkg.get("lane"), "type": pkg.get("type"), "size": pkg.get("size")}
        v += ["rule 11: %s is %r, the plan says %r" % (k, meta.get(k), w) for k, w in want.items() if meta.get(k) != w]
        if t and " ".join(t.group(2).split()) != " ".join(str(pkg.get("title", "")).split()):
            v.append("rule 11: the title differs from the plan's title")
        pc = pkg.get("precondition") or {}
        if not pre or pre[0]["run"] != pc.get("run") or pre[0]["expect"] != str(pc.get("expect")):
            v.append("rule 11: field 12 differs from the precondition of the plan")
        if touches != list(pkg.get("touches") or []):
            v.append("rule 11: field 14 differs from the touches of the plan")
        if [(a["id"], a["run"]) for a in acc] != [(a.get("id"), a.get("run")) for a in pkg.get("acceptance") or []]:
            v.append("rule 11: field 18 differs from the acceptance commands of the plan")
    return v, rules.strip()


def lint(files, plan):
    """files: {name: text}. plan: the parsed plan file. One line per violation, '<name>: rule N: ...'."""
    pk = {p.get("id"): p for p in plan.get("packages") or [] if isinstance(p, dict)}
    out, blocks = [], {}
    for name in sorted(files):
        v, rules = lint_one(name, files[name], pk.get(os.path.basename(name)[:-3]))
        out += ["%s: %s" % (name, x) for x in v]
        blocks[name] = rules
    if len(set(blocks.values())) > 1:
        names = sorted(blocks)
        ref = max(names, key=lambda n: (list(blocks.values()).count(blocks[n]), -names.index(n)))
        out += ["%s: rule 4: the rules of conduct differ from those of %s" % (n, ref) for n in names if blocks[n] != blocks[ref]]
    return out


def read_plan(root):
    import yaml                                                # only here: the self-test needs the standard library alone
    with open(os.path.join(root, PLAN), encoding="utf-8") as fh:
        return yaml.load(fh, Loader=getattr(yaml, "CSafeLoader", yaml.SafeLoader))


# ---------------------------------------------------------------------------------------------- self-test
def good_ticket():
    rules = "\n".join("%d. Rule number %d." % (n, n) for n in range(1, 26))
    body = {n: "Text of field %d." % n for n in range(1, 22)}
    body[12] = "**C1.** The file is absent.\n```sh\ntest -e a.txt; echo $?\n```\nExpected output: `1`"
    body[13] = "**P1.** The folder exists.\n```sh\ntest -d docs\n```\nExpected exit code: `0`\n\n**P2.** Two lines.\n```sh\nls\n```\nExpected output:\n```\na\nb\n```"
    body[14] = "```\na.txt\n```\n\nBesides these paths, three files of your own are always allowed."
    body[16] = "**Start.** Fixed text with git push.\n\n**Work.** The steps of this package, in order.\n\n1. Create `a.txt`.\n2. Check it.\n\n**Finish.** Fixed text with git commit."
    body[18] = "**A1.** The file is there.\n```sh\ntest -e a.txt\n```"
    emb = "<!-- BEGIN a.txt -->\n```text\nhello\n```\n<!-- END a.txt -->\nIts SHA-256 is %s." % hashlib.sha256(b"hello\n").hexdigest()
    body[17] = emb
    text = ("---\nid: XXX-001\nticket_rev: 2\nplan_commit: %s\nlane: RAILS\ntype: fix\nsize: XS\n---\n\n# XXX-001 — A title\n\n## In plain words\n\n"
            "**The problem today.** Something is missing.\n\n**What changes.** It is added.\n\n**Done means:**\n\n- One fact.\n- Another fact.\n\n"
            "The rest of this ticket is the exact instruction.\n\nRULES OF CONDUCT:\n\n%s\n\n" % ("0" * 40, rules))
    text += "".join("## %d. %s\n%s\n\n" % (n, f, body[n]) for n, f in enumerate(FIELDS, 1))
    text += "## If this ticket is wrong\n\nStop and report.\n"
    pkg = {"id": "XXX-001", "title": "A title", "ticket_rev": 2, "lane": "RAILS", "type": "fix", "size": "XS", "touches": ["a.txt"],
           "precondition": {"run": "test -e a.txt; echo $?", "expect": "1"}, "acceptance": [{"id": "A1", "run": "test -e a.txt"}]}
    return text, pkg


def self_test():
    text, pkg = good_ticket()
    plan = {"packages": [pkg]}
    name = "docs/ledger/plan/tickets/XXX-001.md"
    bad, count = 0, 0

    def case(title, lines, needle):
        nonlocal bad, count
        count += 1
        passed = (not lines) if needle is None else any(needle in x for x in lines)
        bad += 0 if passed else 1
        print("%s  %s" % ("PASS" if passed else "FAIL", title))
        if not passed:
            print("      got: %s" % lines)

    def edit(old, new, src=None):
        src = text if src is None else src
        assert src.count(old) >= 1, old
        return {name: src.replace(old, new, 1)}
    case("a good ticket has no violation", lint({name: text}, plan), None)
    case("a ticket with code inside an embedded block, a wave name and an exit-code item still passes",
         lint(edit("4. Rule number 4.", "4. Rule number 4 of wave W2."), plan), None)
    word = {"m": "M" + "VP", "i": "itera" + "tion", "o": "out " + "of scope", "b": "block" + "er"}
    cases = [
        ("rule 1: a missing front-matter line", edit("lane: RAILS\n", ""), "rule 1: the front matter"),
        ("rule 1: an id that is not the file name", edit("id: XXX-001", "id: XXX-002"), "rule 1: id is"),
        ("rule 1: a short plan_commit", edit("0" * 40, "0" * 39), "plan_commit"),
        ("rule 1: a size that is not XS, S or M", edit("size: XS", "size: L"), "rule 1: size"),
        ("rule 2: no title line", edit("# XXX-001 — A title", "# A title"), "rule 2"),
        ("rule 3: no problem statement", edit("**The problem today.** ", ""), "rule 3: the part"),
        ("rule 3: one line under Done means", edit("- Another fact.\n", ""), "rule 3: the part"),
        ("rule 3: code in the part in plain words", edit("Something is missing.", "The file `a.txt` is missing."), "holds code"),
        ("rule 4: a rule of conduct is missing", edit("7. Rule number 7.\n", ""), "rule 4: the rules of conduct are not"),
        ("rule 4: two tickets whose rules of conduct differ", dict(edit("5. Rule number 5.", "5. Rule five, reworded."), **{"y/XXX-001.md": text}), "rule 4: the rules of conduct differ"),
        ("rule 5: a field is missing", edit("## 19. Rollback\nText of field 19.\n\n", ""), "rule 5: the headings"),
        ("rule 5: a field is empty", edit("## 19. Rollback\nText of field 19.", "## 19. Rollback\n"), "has no text"),
        ("rule 5: an extra heading", edit("Text of field 2.", "Text of field 2.\n\n## Notes\nMore."), "rule 5: the headings"),
        ("rule 5: a heading inside a code block does not count", edit("Text of field 2.", "Text of field 2.\n```text\n## Notes\n```"), None),
        ("rule 6: two precondition items", edit("Expected output: `1`", "Expected output: `1`\n**C2.** More.\n```sh\ntrue\n```\nExpected output: `0`"), "rule 6: field 12"),
        ("rule 6: a precondition without an expected output", edit("Expected output: `1`", ""), "rule 6: field 12"),
        ("rule 6: a pre-flight item without an expected value", edit("Expected exit code: `0`", ""), "P1 has no expected"),
        ("rule 6: pre-flight items out of order", edit("**P2.** Two lines.", "**P3.** Two lines."), "not P1, P2"),
        ("rule 6: an acceptance item with an expected value", edit("test -e a.txt\n```\n\n## 19", "test -e a.txt\n```\nExpected output: `x`\n\n## 19"), "states an expected value"),
        ("rule 6: an acceptance item without a command", edit("**A1.** The file is there.\n```sh\ntest -e a.txt\n```", "**A1.** The file is there."), "has no command block"),
        ("rule 6: an acceptance command that talks to a cluster", edit("test -e a.txt\n```\n\n## 19", "kubectl get pods\n```\n\n## 19"), "uses 'kubectl'"),
        ("rule 7: two code blocks in field 14", edit("```\na.txt\n```\n", "```\na.txt\n```\n```\nb.txt\n```\n"), "rule 7"),
        ("rule 7: a path with a space", edit("```\na.txt\n```\n", "```\na b.txt\n```\n"), "rule 7"),
        ("rule 8: a gap in the work steps", edit("2. Check it.", "3. Check it."), "rule 8: the work steps"),
        ("rule 8: a vague phrase in a work step", edit("2. Check it.", "2. Check it as needed."), "vague phrase"),
        ("rule 8: a work step that commits", edit("2. Check it.", "2. Run `git commit`."), "fixed start and finish"),
        ("rule 8: a work step that waits with sleep", edit("2. Check it.", "2. Run `sleep 120`."), "waits with sleep"),
        ("rule 9: an embedded file without its checksum", edit(hashlib.sha256(b"hello\n").hexdigest(), "0" * 64), "rule 9: the embedded file"),
        ("rule 9: an embedded file without its end line", edit("<!-- END a.txt -->", ""), "rule 9: an embedded file is not"),
        ("rule 10: the first banned word", edit("Text of field 3.", "Text of the %s." % word["m"]), "rule 10"),
        ("rule 10: the second banned word", edit("Text of field 3.", "The second %s." % word["i"]), "rule 10"),
        ("rule 10: the third banned word", edit("Text of field 3.", "That is %s." % word["o"]), "rule 10"),
        ("rule 10: the fourth banned word", edit("Text of field 3.", "A %s." % word["b"]), "rule 10"),
        ("rule 11: a package the plan does not have", {"docs/ledger/plan/tickets/XXX-002.md": edit("XXX-001", "XXX-002")[name].replace("XXX-001", "XXX-002")}, "is not a package"),
        ("rule 11: a ticket_rev that differs from the plan", edit("ticket_rev: 2", "ticket_rev: 3"), "rule 11: ticket_rev"),
        ("rule 11: a lane that differs from the plan", edit("lane: RAILS", "lane: GATE"), "rule 11: lane"),
        ("rule 11: a title that differs from the plan", edit("— A title", "— Another title"), "rule 11: the title"),
        ("rule 11: a precondition that differs from the plan", edit("Expected output: `1`", "Expected output: `0`"), "rule 11: field 12"),
        ("rule 11: touches that differ from the plan", edit("```\na.txt\n```\n", "```\na.txt\nb.txt\n```\n"), "rule 11: field 14"),
        ("rule 11: an acceptance command that differs from the plan", edit("test -e a.txt\n```\n\n## 19", "test -s a.txt\n```\n\n## 19"), "rule 11: field 18"),
    ]
    for title, files, needle in cases:
        case(title, lint(files, plan), needle)
    print("SELF-TEST %s (%d cases)" % ("OK" if not bad else "FAILED", count))
    return 1 if bad else 0


def main(argv):
    if argv == ["--self-test"]:
        return self_test()
    if not argv or any(a.startswith("-") for a in argv):
        sys.stderr.write("usage: ticket-lint.py FILE [FILE ...] | --self-test\n")
        return 3
    root = os.path.normpath(os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))
    try:
        plan = read_plan(root)
        files = {}
        for a in argv:
            with open(a, encoding="utf-8") as fh:
                files[a] = fh.read()
    except (OSError, ImportError) as e:
        sys.stderr.write("ticket-lint.py: %s\n" % e)
        return 3
    if not isinstance(plan, dict):
        sys.stderr.write("ticket-lint.py: %s is not a mapping\n" % PLAN)
        return 3
    v = lint(files, plan)
    for line in v:
        print(line)
    return 1 if v else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
