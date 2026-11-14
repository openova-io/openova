#!/usr/bin/env python3
"""scan-baseline.py - a change may add no vulnerability and no secret finding.

The workflow .github/workflows/security-scan.yaml runs Trivy twice on every pull request and
every merge group: once on the merge base with main, once on the change. Trivy is the one
matcher, for vulnerabilities and for secrets. Each run writes a Trivy JSON report. This script
compares the two reports. The merge base is the baseline: there is no stored baseline file.

Usage:

  python3 scripts/scan-baseline.py compare <merge-base report> <change report>
  python3 scripts/scan-baseline.py --self-test

Exit code 0: the change adds no finding. 1: the change adds at least one finding; one line per
new finding. 2: wrong usage, or a report that cannot be read or is not a Trivy JSON report
(schema version 2).

The rules, exactly.

A finding has an identifier.
  vulnerability   the target file, the package name and the vulnerability identifier
                  (Target, PkgName, VulnerabilityID). The installed version is not part of it,
                  so a version change that keeps a known vulnerability adds nothing.
  secret          the target file and the rule identifier (Target, RuleID). The line number is
                  not part of it, so a secret that moves inside its file adds nothing.

Findings are counted per identifier. The change adds a finding when it holds an identifier
more often than the merge base does. A second secret of the same rule in the same file is
therefore new. A finding that the change removes is not reported.

A finding of a file that the change moves or renames is new, because its target changes.

The secret text is never printed: a new secret is named by its file, its line and its rule.

Only the Python standard library is used.
"""
import contextlib
import io
import json
import os
import sys
import tempfile
from collections import Counter

EXIT_OK = 0
EXIT_NEW = 1
EXIT_USAGE = 2


class ReportError(Exception):
    """A report cannot be read, or it is not a Trivy JSON report of schema version 2."""


def load_report(path):
    try:
        with open(path, encoding="utf-8") as fh:
            data = json.load(fh)
    except OSError as exc:
        raise ReportError(f"{path}: cannot be read: {exc.strerror}")
    except ValueError as exc:
        raise ReportError(f"{path}: is not JSON: {exc}")
    return check_report(data, path)


def check_report(data, name):
    if not isinstance(data, dict):
        raise ReportError(f"{name}: is not a Trivy JSON report (the top level is not an object)")
    if data.get("SchemaVersion") != 2:
        raise ReportError(f"{name}: is not a Trivy JSON report of schema version 2")
    results = data.get("Results")
    if results is None:
        results = []
    if not isinstance(results, list):
        raise ReportError(f"{name}: Results is not a list")
    for result in results:
        if not isinstance(result, dict) or not isinstance(result.get("Target"), str):
            raise ReportError(f"{name}: a result has no Target")
    return data


def findings(report):
    """Return a list of (identifier, description) pairs, one per finding."""
    out = []
    for result in report.get("Results") or []:
        target = result["Target"]
        for vuln in result.get("Vulnerabilities") or []:
            ident = ("vulnerability", target, vuln.get("PkgName", ""), vuln.get("VulnerabilityID", ""))
            text = (f"vulnerability {ident[3]} in package {ident[2]} "
                    f"{vuln.get('InstalledVersion', '')}, severity {vuln.get('Severity', '')}, in {target}")
            out.append((ident, text))
        for secret in result.get("Secrets") or []:
            ident = ("secret", target, secret.get("RuleID", ""))
            text = (f"secret of rule {ident[2]} ({secret.get('Title', '')}), "
                    f"severity {secret.get('Severity', '')}, in {target} line {secret.get('StartLine', '')}")
            out.append((ident, text))
    return out


def new_findings(base, head):
    """Return the descriptions of the findings that head adds to base, in a stable order."""
    base_count = Counter(ident for ident, _ in findings(base))
    head_list = findings(head)
    head_count = Counter(ident for ident, _ in head_list)
    out = []
    for ident in sorted(head_count):
        extra = head_count[ident] - base_count.get(ident, 0)
        if extra <= 0:
            continue
        texts = sorted(text for i, text in head_list if i == ident)
        if base_count.get(ident, 0) == 0:
            out.extend(texts)
        else:
            out.append(f"{extra} more than at the merge base: " + "; ".join(texts))
    return out


def compare(base_path, head_path):
    try:
        base = load_report(base_path)
        head = load_report(head_path)
    except ReportError as exc:
        print(f"scan-baseline: {exc}")
        return EXIT_USAGE
    added = new_findings(base, head)
    print(f"scan-baseline: merge base {len(findings(base))} finding(s), "
          f"change {len(findings(head))} finding(s), {len(added)} new")
    for line in added:
        print(f"NEW {line}")
    return EXIT_NEW if added else EXIT_OK


# ---------------------------------------------------------------------------------------------
# Self-test. Every case builds its reports in memory or in a temporary directory.

def _vuln(vid, pkg, version="1.0.0", severity="HIGH"):
    return {"VulnerabilityID": vid, "PkgName": pkg, "InstalledVersion": version, "Severity": severity}


def _secret(rule, line, title="Rule title"):
    return {"RuleID": rule, "Title": title, "Severity": "CRITICAL", "StartLine": line, "EndLine": line,
            "Match": "SHOULD-NEVER-BE-PRINTED"}


def _report(*results):
    return {"SchemaVersion": 2, "ArtifactType": "repository", "Results": list(results)}


def _vulns(target, *vulns):
    return {"Target": target, "Class": "lang-pkgs", "Type": "gomod", "Vulnerabilities": list(vulns)}


def _secrets(target, *secrets):
    return {"Target": target, "Class": "secret", "Secrets": list(secrets)}


BASE = _report(
    _vulns("svc/go.mod", _vuln("CVE-2000-0001", "example.invalid/a", "1.0.0")),
    _secrets("svc/creds.env", _secret("rule-one", 3)),
)

CASES = [
    # (name, base, head, number of new findings expected)
    ("the same report adds nothing", BASE, BASE, 0),
    ("a new vulnerability identifier is new", BASE, _report(
        _vulns("svc/go.mod", _vuln("CVE-2000-0001", "example.invalid/a"), _vuln("CVE-2000-0002", "example.invalid/a")),
        _secrets("svc/creds.env", _secret("rule-one", 3))), 1),
    ("a known vulnerability in a new package is new", BASE, _report(
        _vulns("svc/go.mod", _vuln("CVE-2000-0001", "example.invalid/a"), _vuln("CVE-2000-0001", "example.invalid/b")),
        _secrets("svc/creds.env", _secret("rule-one", 3))), 1),
    ("a known vulnerability in a new target is new", BASE, _report(
        _vulns("svc/go.mod", _vuln("CVE-2000-0001", "example.invalid/a")),
        _vulns("other/go.mod", _vuln("CVE-2000-0001", "example.invalid/a")),
        _secrets("svc/creds.env", _secret("rule-one", 3))), 1),
    ("a version change that keeps a known vulnerability adds nothing", BASE, _report(
        _vulns("svc/go.mod", _vuln("CVE-2000-0001", "example.invalid/a", "1.0.1")),
        _secrets("svc/creds.env", _secret("rule-one", 3))), 0),
    ("a second copy of a vulnerable package in one target is new", BASE, _report(
        _vulns("svc/go.mod", _vuln("CVE-2000-0001", "example.invalid/a", "1.0.0"),
               _vuln("CVE-2000-0001", "example.invalid/a", "2.0.0")),
        _secrets("svc/creds.env", _secret("rule-one", 3))), 1),
    ("a secret of a new rule is new", BASE, _report(
        _vulns("svc/go.mod", _vuln("CVE-2000-0001", "example.invalid/a")),
        _secrets("svc/creds.env", _secret("rule-one", 3), _secret("rule-two", 9))), 1),
    ("a second secret of the same rule in the same file is new", BASE, _report(
        _vulns("svc/go.mod", _vuln("CVE-2000-0001", "example.invalid/a")),
        _secrets("svc/creds.env", _secret("rule-one", 3), _secret("rule-one", 9))), 1),
    ("a known secret in a new file is new", BASE, _report(
        _vulns("svc/go.mod", _vuln("CVE-2000-0001", "example.invalid/a")),
        _secrets("svc/creds.env", _secret("rule-one", 3)),
        _secrets("svc/more.env", _secret("rule-one", 1))), 1),
    ("a secret that moves inside its file adds nothing", BASE, _report(
        _vulns("svc/go.mod", _vuln("CVE-2000-0001", "example.invalid/a")),
        _secrets("svc/creds.env", _secret("rule-one", 40))), 0),
    ("a removed finding adds nothing", BASE, _report(
        _vulns("svc/go.mod", _vuln("CVE-2000-0001", "example.invalid/a"))), 0),
    ("a report without results adds nothing", BASE, {"SchemaVersion": 2, "ArtifactType": "repository"}, 0),
    ("every finding is new against a report without results",
     {"SchemaVersion": 2, "ArtifactType": "repository"}, BASE, 2),
]

BAD_REPORTS = [
    ("a list is not a report", []),
    ("schema version 1 is refused", {"SchemaVersion": 1, "Results": []}),
    ("a report without a schema version is refused", {"Results": []}),
    ("Results that are not a list are refused", {"SchemaVersion": 2, "Results": {}}),
    ("a result without a Target is refused", {"SchemaVersion": 2, "Results": [{"Class": "secret"}]}),
]


def self_test():
    failures = []
    passed = 0

    def check(ok, name):
        nonlocal passed
        if ok:
            passed += 1
        else:
            failures.append(name)

    for name, base, head, want in CASES:
        got = len(new_findings(check_report(base, "base"), check_report(head, "head")))
        check(got == want, f"{name}: {got} new finding(s), want {want}")

    for name, data in BAD_REPORTS:
        try:
            check_report(data, "bad")
        except ReportError:
            check(True, name)
        else:
            check(False, f"{name}: the report was accepted")

    # The command line, with real files: exit codes 0, 1 and 2, and no secret text printed.
    with tempfile.TemporaryDirectory() as tmp:
        def write(name, data):
            path = os.path.join(tmp, name)
            with open(path, "w", encoding="utf-8") as fh:
                if isinstance(data, str):
                    fh.write(data)
                else:
                    json.dump(data, fh)
            return path

        base_path = write("base.json", BASE)
        head_path = write("head.json", CASES[6][2])
        bad_path = write("bad.json", "not json")
        missing_path = os.path.join(tmp, "missing.json")

        def run(*args):
            buf = io.StringIO()
            with contextlib.redirect_stdout(buf):
                code = main(list(args))
            return code, buf.getvalue()

        code, out = run("compare", base_path, base_path)
        check(code == EXIT_OK, f"compare of a report with itself: exit {code}, want 0")
        code, out = run("compare", base_path, head_path)
        check(code == EXIT_NEW, f"compare with a new finding: exit {code}, want 1")
        check("NEW secret of rule rule-two" in out and "line 9" in out,
              "compare with a new finding: the new secret is not named with its rule and line")
        check("SHOULD-NEVER-BE-PRINTED" not in out, "compare printed the text of a secret")
        code, out = run("compare", bad_path, head_path)
        check(code == EXIT_USAGE, f"compare of a file that is not JSON: exit {code}, want 2")
        code, out = run("compare", base_path, missing_path)
        check(code == EXIT_USAGE, f"compare of a missing file: exit {code}, want 2")
        code, out = run("compare", base_path)
        check(code == EXIT_USAGE, f"compare with one report: exit {code}, want 2")
        code, out = run("record")
        check(code == EXIT_USAGE, f"an unknown command: exit {code}, want 2")

    for name in failures:
        print(f"self-test FAILED: {name}")
    if failures:
        return 1
    print(f"self-test: {passed} cases passed")
    return 0


def main(argv):
    if argv == ["--self-test"]:
        return self_test()
    if len(argv) == 3 and argv[0] == "compare":
        return compare(argv[1], argv[2])
    print("usage: scan-baseline.py compare <merge-base report> <change report> | --self-test")
    return EXIT_USAGE


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
