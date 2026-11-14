#!/usr/bin/env python3
"""check-cloudinit-exit-codes.py - the fatal exit codes of the shared first-boot template.

Contract C8 (docs/contracts/c8-seal-provider.md) reserves the exit code 96
for one step of the shared cloud-init template: the seal step, whose line
is `[ -b <device> ] || exit 96`. The other fatal steps of the template use
the codes 86 to 95, 97 and 98. Exit code 0 is a normal end, not a fatal step.

This guard reads infra/providers/_shared/cloudinit-control-plane.tftpl as
it is in the repository (every branch of the template, comment lines too)
and finds every `exit <number>`. It fails when:

  rule 1  a line has `exit 96` and is not the seal step `[ -b <device> ] || exit 96`;
  rule 2  a line has an exit code that is not 0 and not in the list of contract C8.

So two different failures never share the code 96, and a new step cannot
take a code outside the list. A change that needs another code changes the
list here and in contract C8 in the same pull request.

Usage:
  python3 scripts/check-cloudinit-exit-codes.py              check the shared template
  python3 scripts/check-cloudinit-exit-codes.py FILE...      check these files instead
  python3 scripts/check-cloudinit-exit-codes.py --self-test  prove that each rule can fail

Exit: 0 = no finding (or the self-test passed); 1 = a finding (or a self-test
case failed); 2 = wrong usage or a file that cannot be read.
Needs: Python 3 only.
"""

import os
import re
import sys
import tempfile

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
TEMPLATE = "infra/providers/_shared/cloudinit-control-plane.tftpl"

# The codes of contract C8 for the fatal steps other than the seal step.
ALLOWED = set(range(86, 96)) | {97, 98}
SEAL_CODE = 96

EXIT_RE = re.compile(r"\bexit[ \t]+([0-9]+)\b")
SEAL_RE = re.compile(r"\[ -b [^\]\n]+ \] \|\| exit 96\b")


def findings(text, name):
    """Return the list of findings of one file, as 'name:line: message'."""
    out = []
    for number, line in enumerate(text.splitlines(), 1):
        for match in EXIT_RE.finditer(line):
            code = int(match.group(1))
            if code == 0 or code in ALLOWED:
                continue
            if code == SEAL_CODE:
                if SEAL_RE.search(line):
                    continue
                out.append(
                    "%s:%d: rule 1: exit 96 is reserved for the seal step "
                    "`[ -b <device> ] || exit 96` (contract C8); give this step "
                    "another code of the list, or write `code 96` in a comment" % (name, number)
                )
                continue
            out.append(
                "%s:%d: rule 2: exit %d is not in the list of contract C8 "
                "(0, 86 to 95, 97, 98; 96 for the seal step only)" % (name, number, code)
            )
    return out


def count_exits(text):
    return sum(len(EXIT_RE.findall(line)) for line in text.splitlines())


def check(paths, label):
    total = 0
    problems = []
    for path in paths:
        try:
            with open(path, encoding="utf-8") as handle:
                text = handle.read()
        except OSError as error:
            print("check-cloudinit-exit-codes: cannot read %s: %s" % (path, error), file=sys.stderr)
            return 2
        name = label.get(path, path)
        total += count_exits(text)
        problems.extend(findings(text, name))
    for problem in problems:
        print(problem)
    if problems:
        print("check-cloudinit-exit-codes: %d finding(s)" % len(problems))
        return 1
    print("check-cloudinit-exit-codes: %d exit codes in %d file(s), all in the list of contract C8"
          % (total, len(paths)))
    return 0


CASES = [
    # (name, file content, expected exit code of the check)
    ("the codes of the list pass",
     "  - foo || exit 86\n  - bar || exit 90\n  - baz || exit 95\n"
     "  - a || { sh mark a-FAILED; exit 97; }\n  - b || { sh mark b-FAILED; exit 98; }\n"
     "  - awk '{print $1; exit}'\n  - true; exit 0\n", 0),
    ("the seal step may use 96",
     "  - '[ -b ${seal_volume_device} ] || exit 96'\n", 0),
    ("rule 1: exit 96 in a fatal step fails",
     "  - b || { sh mark b-FAILED; exit 96; }\n", 1),
    ("rule 1: exit 96 after another test than the block-device test fails",
     "  - '[ -f /etc/x ] || exit 96'\n", 1),
    ("rule 1: exit 96 in a comment fails",
     "  # Fail-loud: sentinel parent-bootstrap-FAILED + exit 96.\n", 1),
    ("rule 2: exit 99 fails",
     "  - c || exit 99\n", 1),
    ("rule 2: exit 1 fails",
     "  - d || exit 1\n", 1),
    ("rule 2: exit 85 fails",
     "  - e || exit 85\n", 1),
]


def self_test():
    failed = 0
    with tempfile.TemporaryDirectory() as work:
        for index, (name, content, want) in enumerate(CASES):
            path = os.path.join(work, "case-%d.tftpl" % index)
            with open(path, "w", encoding="utf-8") as handle:
                handle.write(content)
            with open(os.devnull, "w") as quiet:
                saved = sys.stdout
                sys.stdout = quiet
                try:
                    got = check([path], {})
                finally:
                    sys.stdout = saved
            if got == want:
                print("  ok    %s" % name)
            else:
                print("  FAIL  %s: exit %d, want %d" % (name, got, want))
                failed += 1
    if failed:
        print("check-cloudinit-exit-codes: self-test failed: %d of %d cases" % (failed, len(CASES)))
        return 1
    print("check-cloudinit-exit-codes: self-test passed: %d cases" % len(CASES))
    return 0


def main(argv):
    if argv == ["--self-test"]:
        return self_test()
    if any(arg.startswith("-") for arg in argv):
        print("usage: check-cloudinit-exit-codes.py [--self-test | FILE...]", file=sys.stderr)
        return 2
    if argv:
        return check(argv, {})
    path = os.path.join(ROOT, TEMPLATE)
    return check([path], {path: TEMPLATE})


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
