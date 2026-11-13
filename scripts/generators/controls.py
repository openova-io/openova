#!/usr/bin/env python3
"""controls.py - the security control matrix, generated from the control file.

It reads   docs/ledger/controls.yaml   (the controls)
           docs/ledger/gates.yaml      (the state of a gate check that a control names)
           docs/adr/                   (that a decision a control names exists)
It writes  docs/generated/security-controls.md

Usage, from any directory of a full checkout:
  python3 scripts/generators/controls.py              write the file
  python3 scripts/generators/controls.py --check      write nothing; exit 1 when the file on disk differs
  python3 scripts/generators/controls.py --self-test  prove every rule on a throw-away tree

Exit codes: 0 done or current; 1 the file is stale, or a self-test case failed;
2 an input is missing, cannot be read, or breaks a rule of the control file.

The control file, exactly:

  schema: openova.io/controls/v1      required, this value
  controls:                           required, a list of one or more controls
    - id: secrets-at-rest             required, lower-case words joined by "-", unique
      claim: "..."                    required, one line that ends with "."
      mechanism: "..."                required, one line
      exists: false                   required, true or false: code or a chart for the mechanism exists
      default_on: false               required, true or false: installed and enforcing on a default Sovereign
      mode: none                      required, none, audit or enforce
      guard: scripts/check-x.sh       optional, a path of a CI guard that exists
      gate_check: SEC-G01             optional, a gate check identifier listed in docs/ledger/gates.yaml
      decision: ADR-0022              optional, ADR-NNNN with a file docs/adr/NNNN-<name>.md

  An optional key that is absent or null means none. No other key is allowed, at the top or in
  a control. A control whose exists is false has default_on false and mode none.

A control prints as "built" only when default_on is true and gate_check names a check whose
state in docs/ledger/gates.yaml is "required". Every other control prints as "target".
"""
import difflib
import glob
import os
import re
import sys
import tempfile

try:
    import yaml
except ImportError:
    sys.stderr.write("controls.py needs PyYAML: pip install pyyaml\n")
    sys.exit(2)

SCRIPT = "scripts/generators/controls.py"
SOURCE = "docs/ledger/controls.yaml"
GATES = "docs/ledger/gates.yaml"
OUTPUT = "docs/generated/security-controls.md"
SCHEMA = "openova.io/controls/v1"
REQUIRED = ("id", "claim", "mechanism", "exists", "default_on", "mode")
OPTIONAL = ("guard", "gate_check", "decision")
MODES = ("none", "audit", "enforce")
CONTROL_ID = re.compile(r"^[a-z0-9]+(-[a-z0-9]+)*$")
CHECK_ID = re.compile(r"^[A-Z]{3}-G[0-9]{2}$")
DECISION = re.compile(r"^ADR-([0-9]{4})$")


class InputError(Exception):
    """An input is missing, cannot be read, or breaks a rule of the control file."""


def load(root, relative):
    try:
        with open(os.path.join(root, relative), encoding="utf-8") as handle:
            return yaml.safe_load(handle)
    except OSError as error:
        raise InputError("%s: cannot be read: %s" % (relative, error.strerror))
    except yaml.YAMLError as error:
        raise InputError("%s: is not valid YAML: %s" % (relative, str(error).splitlines()[0]))


def one_line(value):
    return isinstance(value, str) and value.strip() == value and value != "" and "\n" not in value


def plain_path(value):
    parts = value.split("/")
    return (not value.startswith("/") and " " not in value
            and all(part not in ("", ".", "..") for part in parts))


def read_controls(root):
    data = load(root, SOURCE)
    if not isinstance(data, dict):
        raise InputError("%s: is not a mapping" % SOURCE)
    unknown = sorted(str(key) for key in data if key not in ("schema", "controls"))
    if unknown:
        raise InputError("%s: unknown key %s" % (SOURCE, ", ".join(unknown)))
    if data.get("schema") != SCHEMA:
        raise InputError("%s: schema is not %s" % (SOURCE, SCHEMA))
    controls = data.get("controls")
    if not isinstance(controls, list) or not controls:
        raise InputError("%s: controls is not a list of one or more controls" % SOURCE)
    seen = set()
    gates = None
    for number, control in enumerate(controls, 1):
        where = "%s: control %d" % (SOURCE, number)
        if not isinstance(control, dict):
            raise InputError("%s is not a mapping" % where)
        missing = [field for field in REQUIRED if field not in control]
        extra = sorted(str(key) for key in control if key not in REQUIRED + OPTIONAL)
        if missing:
            raise InputError("%s lacks %s" % (where, ", ".join(missing)))
        if extra:
            raise InputError("%s has unknown key %s" % (where, ", ".join(extra)))
        name = control["id"]
        if not isinstance(name, str) or not CONTROL_ID.match(name):
            raise InputError("%s: id is not lower-case words joined by -" % where)
        where = "%s: control %s" % (SOURCE, name)
        if name in seen:
            raise InputError("%s: the id is used twice" % where)
        seen.add(name)
        if not one_line(control["claim"]) or not control["claim"].endswith("."):
            raise InputError("%s: claim is not one line that ends with a full stop" % where)
        if not one_line(control["mechanism"]):
            raise InputError("%s: mechanism is not one line" % where)
        for field in ("exists", "default_on"):
            if not isinstance(control[field], bool):
                raise InputError("%s: %s is not true or false" % (where, field))
        if control["mode"] not in MODES:
            raise InputError("%s: mode is not none, audit or enforce" % where)
        if not control["exists"] and (control["default_on"] or control["mode"] != "none"):
            raise InputError("%s: exists is false, so default_on must be false and mode none" % where)
        guard = control.get("guard")
        if guard is not None:
            if not isinstance(guard, str) or not plain_path(guard) or not os.path.isfile(os.path.join(root, guard)):
                raise InputError("%s: guard is not a path of a file that exists" % where)
        decision = control.get("decision")
        if decision is not None:
            match = DECISION.match(decision) if isinstance(decision, str) else None
            if not match or not glob.glob(os.path.join(root, "docs", "adr", match.group(1) + "-*.md")):
                raise InputError("%s: decision is not ADR-NNNN with a file docs/adr/NNNN-<name>.md" % where)
        check = control.get("gate_check")
        if check is not None:
            if not isinstance(check, str) or not CHECK_ID.match(check):
                raise InputError("%s: gate_check is not a gate check identifier" % where)
            if gates is None:
                gates = read_gates(root)
            if check not in gates:
                raise InputError("%s: gate_check %s is not listed in %s" % (where, check, GATES))
    return controls, gates or {}


def read_gates(root):
    data = load(root, GATES)
    checks = data.get("checks") if isinstance(data, dict) else None
    if not isinstance(checks, list):
        raise InputError("%s: has no list of checks" % GATES)
    states = {}
    for check in checks:
        if isinstance(check, dict) and isinstance(check.get("id"), str):
            states[check["id"]] = check.get("state")
    return states


def is_built(control, gates):
    return control["default_on"] is True and control.get("gate_check") is not None \
        and gates.get(control["gate_check"]) == "required"


def cell(value):
    if value is None:
        return "-"
    if isinstance(value, bool):
        return "yes" if value else "no"
    return str(value).replace("|", "\\|")


def generate(root):
    controls, gates = read_controls(root)
    built = sum(1 for control in controls if is_built(control, gates))
    lines = [
        "Generated by %s. Do not edit." % SCRIPT,
        "",
        "# Security controls",
        "",
        "One row for each control in `%s`, in the order of that file." % SOURCE,
        "To change this file, change `%s` and run `python3 %s`." % (SOURCE, SCRIPT),
        "The check `generated-docs` fails a pull request in which this file is stale.",
        "",
        "- State: `built` when the control is on by default and its gate check has the state `required` in `%s`. Every other control is `target`." % GATES,
        "- Exists: code or a chart for the mechanism is in this repository.",
        "- On by default: installed and enforcing on a default Sovereign, with no further step.",
        "- Mode: what the mechanism does on a default Sovereign where it is installed. `enforce`: it refuses or prevents what the claim rules out. `audit`: it only reports it. `none`: it does neither. When installed parts act in different modes, the weaker mode is written.",
        "- Guard: a CI guard that fails a pull request which weakens the control.",
        "- A dash means none.",
        "",
        "Built: %d of %d. Target: %d of %d." % (built, len(controls), len(controls) - built, len(controls)),
        "",
        "| State | Control | Claim | Mechanism | Exists | On by default | Mode | Guard | Gate check | Decision |",
        "|---|---|---|---|---|---|---|---|---|---|",
    ]
    for control in controls:
        lines.append("| %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |" % (
            "built" if is_built(control, gates) else "target", cell(control["id"]), cell(control["claim"]),
            cell(control["mechanism"]), cell(control["exists"]), cell(control["default_on"]), cell(control["mode"]),
            cell(control.get("guard")), cell(control.get("gate_check")), cell(control.get("decision"))))
    return "\n".join(lines) + "\n"


def write(root):
    text = generate(root)
    path = os.path.join(root, OUTPUT)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8", newline="\n") as handle:
        handle.write(text)
    print("wrote: %s" % OUTPUT)
    return 0


def check(root):
    text = generate(root)
    try:
        with open(os.path.join(root, OUTPUT), encoding="utf-8", newline="") as handle:
            on_disk = handle.read()
    except OSError:
        on_disk = None
    if on_disk == text:
        print("current: %s" % OUTPUT)
        return 0
    if on_disk is None:
        print("%s does not exist" % OUTPUT)
    else:
        diff = list(difflib.unified_diff(on_disk.splitlines(), text.splitlines(), "on disk", "generated", lineterm="", n=0))
        for line in diff[:30]:
            print(line[:240])
        if len(diff) > 30:
            print("... %d more lines of difference" % (len(diff) - 30))
    print("stale: %s. Run: python3 %s  and commit that file." % (OUTPUT, SCRIPT))
    return 1


# ---------------------------------------------------------------------------
# Self-test: every rule is shown on a throw-away tree, and the check is shown to fail.

def put(root, relative, text):
    path = os.path.join(root, relative)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as handle:
        handle.write(text)


def control(name, **change):
    item = {"id": name, "claim": "The %s holds." % name, "mechanism": "mechanism of %s" % name,
            "exists": True, "default_on": True, "mode": "enforce"}
    item.update(change)
    return item


def put_controls(root, controls, schema=SCHEMA, **extra):
    data = {"schema": schema, "controls": controls}
    data.update(extra)
    put(root, SOURCE, yaml.safe_dump(data, sort_keys=False))


def self_test():
    failures = []

    def case(name, ok):
        print("%s  %s" % ("ok  " if ok else "FAIL", name))
        if not ok:
            failures.append(name)

    def refused(root):
        try:
            generate(root)
        except InputError:
            return True
        return False

    with tempfile.TemporaryDirectory() as root:
        put(root, GATES, "schema: openova.io/gates/v1\nchecks:\n- id: AAA-G01\n  state: required\n"
                         "- id: AAA-G02\n  state: expected-fail\n")
        put(root, "docs/adr/0007-a-decision.md", "# 0007\n")
        put(root, "scripts/check-a.sh", "#!/bin/sh\n")
        good = [
            control("proven", gate_check="AAA-G01", guard="scripts/check-a.sh", decision="ADR-0007"),
            control("check-not-required", gate_check="AAA-G02"),
            control("no-check"),
            control("off-by-default", default_on=False, gate_check="AAA-G01", mode="audit"),
            control("absent", exists=False, default_on=False, mode="none", claim="A | B holds.", guard=None),
        ]
        put_controls(root, good)
        text = generate(root)
        rows = [line for line in text.splitlines() if line.startswith("| ")]
        case("the first line says who generates the file",
             text.splitlines()[0] == "Generated by scripts/generators/controls.py. Do not edit.")
        case("on by default with a required gate check prints built, with every field",
             "| built | proven | The proven holds. | mechanism of proven | yes | yes | enforce | scripts/check-a.sh | AAA-G01 | ADR-0007 |" in text)
        case("a gate check that is not required prints target",
             "| target | check-not-required |" in text)
        case("on by default without a gate check prints target", "| target | no-check |" in text)
        case("a required gate check without on by default prints target", "| target | off-by-default |" in text)
        case("a pipe is escaped; an optional field that is null or absent is a dash",
             "| target | absent | A \\| B holds. | mechanism of absent | no | no | none | - | - | - |" in text)
        case("one header row and the controls in the order of the file",
             [row.split(" | ")[1] for row in rows[1:]] == ["proven", "check-not-required", "no-check", "off-by-default", "absent"])
        case("the summary line counts built and target", "Built: 1 of 5. Target: 4 of 5." in text)

        write(root)
        case("--check passes on a fresh output", check(root) == 0)
        put(root, GATES, "schema: openova.io/gates/v1\nchecks:\n- id: AAA-G01\n  state: expected-fail\n"
                         "- id: AAA-G02\n  state: expected-fail\n")
        case("a gate check that leaves the state required makes the file stale", check(root) == 1)
        put(root, GATES, "schema: openova.io/gates/v1\nchecks:\n- id: AAA-G01\n  state: required\n"
                         "- id: AAA-G02\n  state: expected-fail\n")
        case("--check passes again when the input is back", check(root) == 0)
        os.remove(os.path.join(root, OUTPUT))
        case("--check fails when the file does not exist", check(root) == 1)

        bad = {
            "a schema that is not openova.io/controls/v1": dict(schema="openova.io/controls/v2"),
            "an unknown key at the top": dict(extra="x"),
            "an empty list of controls": dict(controls=[]),
            "a control without a field": dict(controls=[{k: v for k, v in control("x").items() if k != "mode"}]),
            "a control with an unknown field": dict(controls=[dict(control("x"), owner="y")]),
            "an id that is not lower-case words joined by -": dict(controls=[control("Not_valid")]),
            "an id used twice": dict(controls=[control("x"), control("x")]),
            "a claim without a full stop": dict(controls=[control("x", claim="no full stop")]),
            "a claim of two lines": dict(controls=[control("x", claim="one.\ntwo.")]),
            "an empty mechanism": dict(controls=[control("x", mechanism="")]),
            "exists that is not true or false": dict(controls=[control("x", exists="yes")]),
            "default_on that is not true or false": dict(controls=[control("x", default_on=1)]),
            "a mode outside none, audit and enforce": dict(controls=[control("x", mode="warn")]),
            "exists false with default_on true": dict(controls=[control("x", exists=False, mode="none")]),
            "exists false with a mode": dict(controls=[control("x", exists=False, default_on=False, mode="audit")]),
            "a guard that does not exist": dict(controls=[control("x", guard="scripts/missing.sh")]),
            "a guard that is not a plain path": dict(controls=[control("x", guard="scripts/../scripts/check-a.sh")]),
            "a gate check that is not an identifier": dict(controls=[control("x", gate_check="AAA-01")]),
            "a gate check the manifest does not list": dict(controls=[control("x", gate_check="AAA-G09")]),
            "a decision that is not ADR-NNNN": dict(controls=[control("x", decision="0007")]),
            "a decision without its file": dict(controls=[control("x", decision="ADR-0008")]),
        }
        for name, change in bad.items():
            arguments = {"controls": change.pop("controls", good)}
            arguments.update(change)
            put_controls(root, **arguments)
            case("refused: %s" % name, refused(root))

        put(root, SOURCE, "schema: openova.io/controls/v1\ncontrols: [broken\n")
        case("refused: a control file that is not valid YAML", refused(root))
        os.remove(os.path.join(root, SOURCE))
        case("refused: a missing control file", refused(root))
        put_controls(root, good)
        os.remove(os.path.join(root, GATES))
        case("refused: a missing gate manifest when a control names a gate check", refused(root))
        put_controls(root, [control("no-check")])
        case("the gate manifest is not read when no control names a gate check", not refused(root))

    if failures:
        print("self-test: %d case(s) FAILED" % len(failures))
        return 1
    print("self-test: all cases passed")
    return 0


def main(argv):
    root = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
    if argv == ["--self-test"]:
        return self_test()
    if argv not in ([], ["--check"]):
        sys.stderr.write("usage: python3 %s [--check | --self-test]\n" % SCRIPT)
        return 2
    try:
        return check(root) if argv else write(root)
    except InputError as error:
        sys.stderr.write("%s: %s\n" % (SCRIPT, error))
        return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
