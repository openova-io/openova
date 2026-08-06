#!/usr/bin/env bash
# check — no credential PREFIX reaches a log line (#5467).
#
# ─────────────────────────────────────────────────────────────────────
# The defect class
# ─────────────────────────────────────────────────────────────────────
# #5467: the step-03 harbor-prewarm cutover Job printed the first 8
# characters of the GHCR PAT into its pod logs on every cutover, on
# every Sovereign:
#
#     echo "... pat=${ghcr_pat:0:8}... (len=${#ghcr_pat})"
#
# Four of those 8 characters were real secret material (the `ghp_` type
# tag is the other four), alongside positive confirmation of the token
# TYPE, its exact LENGTH, and the owning ACCOUNT. Pod logs are not a
# secret store: operators read them, log aggregators ship them, support
# bundles capture them, and a failed cutover gets its logs pasted into
# an issue. The identical mothership branch 14 lines below already did
# it correctly — user + length, no prefix.
#
# The platform rule this enforces: **secret VALUES are never printed;
# lengths and hashes only.** A truncated secret is still a secret.
#
# ─────────────────────────────────────────────────────────────────────
# Why this guard evaluates instead of greps
# ─────────────────────────────────────────────────────────────────────
# A grep for `${ghcr_pat:0:8}` is worthless: it passes the moment the
# line is rewritten to `$(printf '%s' "$pat" | cut -c1-8)`, or
# `${pat::8}`, or `${pat:0:4}${pat:4:4}`, or awk substr. It asserts on
# the KEY (the spelling of one expression) and not on the VALUE (what
# actually lands in the log).
#
# So this guard runs each candidate output statement for real. Every
# credential-named variable in the statement is bound to a 40-character
# SENTINEL, the statement is executed in an isolated bash, and the
# emitted bytes are inspected for sentinel material:
#
#   * ZERO sentinel bytes emitted  → PASS. `(len=${#pat})` prints "40";
#     `user=${user}` prints a non-credential value. Both are the
#     diagnostics an operator actually needs — "was a credential
#     present, was it the right length, for which account" — and none
#     of them costs a byte of the secret.
#
#   * a PARTIAL run of sentinel bytes (>=4 chars, but not the whole
#     sentinel) → FAIL. This is exactly and only the #5467 shape. You
#     cannot emit part of a variable's value by accident: something in
#     the statement truncated it. The detector does not care HOW —
#     :0:N, ::N, cut -c, head -c, awk substr, a sed character class, or
#     an expression nobody has invented yet all land in the same net.
#
#   * the FULL sentinel emitted → NOT failed here, deliberately. Full
#     interpolation is overwhelmingly a *name* being logged
#     (`TLS_SECRET`, `SOURCE_SECRET`, `PAT_SOURCE_KEY` — Kubernetes
#     object names, not values) or a value-producing `printf '%s:%s'`
#     feeding skopeo --src-creds, which is not a log line at all. There
#     are 139 such statements in this repo and flagging them would make
#     this guard noise nobody reads. Partial-run detection is immune to
#     that entire false-positive population by construction: a name
#     variable interpolated whole emits the WHOLE sentinel, never a
#     fragment of it.
#
# Statements that cannot be safely executed (they contain a command
# substitution, so running them would run arbitrary commands) are not
# executed. They fall back to a static truncation-idiom check against
# the credential variable, and are counted in the report so the
# unevaluated population stays visible rather than silently passing.
#
# ─────────────────────────────────────────────────────────────────────
# Waivers
# ─────────────────────────────────────────────────────────────────────
# A genuine false positive takes an inline `# credlog-ok: <reason>` on
# the offending line. Waivers are reported in the summary — an
# unreviewed waiver pile is the next version of this bug.
#
# Self-test: `--self-test` runs the detector over scripts/tests/
# fixtures that ship a known-BAD and a known-GOOD line and asserts the
# detector goes red on one and green on the other. This is the
# vacuity check: a scanner that cannot go red is not a guard.
#
# Refs #5467
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${REPO_ROOT}"

MODE="scan"
case "${1:-}" in
  --self-test) MODE="self-test" ;;
  "") ;;
  *) echo "usage: $0 [--self-test]" >&2; exit 2 ;;
esac

export CREDLOG_MODE="${MODE}"
export CREDLOG_ROOT="${REPO_ROOT}"

python3 - <<'PY'
import os
import re
import subprocess
import sys

ROOT = os.environ["CREDLOG_ROOT"]
MODE = os.environ["CREDLOG_MODE"]

# 40 chars, uppercase-only, no digits (so `${#var}` -> "40" can never be
# mistaken for sentinel material) and no common English trigrams (so a
# 4-char window cannot collide with ordinary log prose).
SENTINEL = "ZQXJVKWFYHBGMPTZQXJVKWFYHBGMPTZQXJVKWFYH"
WINDOW = 4

# Credential-VALUE-shaped variable names. Deliberately broad on the stem
# and narrow on the suffix: `TLS_SECRET` is in scope for evaluation, and
# is then cleared by the partial-run rule rather than by a name
# allowlist that would rot.
CRED_STEM = re.compile(
    r"(?:^|_)(pat|token|secret|password|passwd|pwd|pw|apikey|credential|"
    r"creds?|privkey|jwt|bearer|sessionkey)(?:$|_)",
    re.I,
)
# Also catch camelCase / lowercase joined spellings (ghcr_pat, moth_pat,
# clientSecret, apiKey, rootToken, adminPassword).
CRED_CAMEL = re.compile(
    r"(pat|token|secret|password|passwd|apikey|credential|privkey|jwt|bearer)",
    re.I,
)

VAR_RE = re.compile(
    r"\$\{#?([A-Za-z_][A-Za-z0-9_]*)[^}]*\}|\$([A-Za-z_][A-Za-z0-9_]*)"
)

# Truncation idioms, for the statically-checked (unevaluable) population.
TRUNC_STATIC = re.compile(
    r":\s*-?\d+\s*:\s*-?\d+\}"          # ${v:0:8}
    r"|::\s*-?\d+\}"                     # ${v::8}
    r"|\bcut\s+-c"                       # | cut -c1-8
    r"|\bhead\s+-c"                       # | head -c8
    r"|\bsubstr\s*\("                     # awk substr(v,1,8)
    r"|%\.\d+s"                           # printf '%.8s'
    r"|\.slice\s*\(\s*0"                  # js .slice(0,8)
    r"|\[\s*:\s*\d+\s*\]",                # py v[:8]
)

WAIVER = re.compile(r"#\s*credlog-ok\b")

SKIP_DIRS = {
    ".git", "node_modules", ".claude", "vendor", "dist", "build",
    ".venv", "__pycache__", "graphify-out", "artifacts",
}

SCAN_YAML_UNDER = ("platform/", "products/", "clusters/", "core/",
                   "infra/", ".github/")

# The self-test fixtures are scanned explicitly by --self-test (which walks
# them directly). They must NOT be part of the repo-wide scan: the bad
# fixture is deliberately, permanently defective.
FIXTURE_PREFIX = os.path.join("scripts", "tests", "credlog-fixtures")


def in_scope(rel: str) -> bool:
    if rel.startswith(FIXTURE_PREFIX):
        return False
    if rel.endswith(".sh"):
        return True
    if rel.endswith((".yaml", ".yml", ".tpl", ".tftpl")):
        return rel.startswith(SCAN_YAML_UNDER)
    return False


def collect_files(root: str):
    out = []
    for base, dirs, names in os.walk(root):
        dirs[:] = [d for d in dirs if d not in SKIP_DIRS]
        for n in names:
            rel = os.path.relpath(os.path.join(base, n), root)
            if in_scope(rel):
                out.append(rel)
    return sorted(out)


def is_cred(name: str) -> bool:
    return bool(CRED_STEM.search(name) or CRED_CAMEL.search(name))


ASSIGN_RE = re.compile(
    r"^\s*(?:export\s+|local\s+|readonly\s+|declare\s+-\S+\s+)?"
    r"([A-Za-z_][A-Za-z0-9_]*)=(.+)$"
)


def cred_vars_by_assignment(lines):
    """A variable's NAME is not the only evidence that it holds a secret.

    `GRAFANA_EMPTY=$(extract_secret ... .secret)` holds a Keycloak client
    secret under a name no name-regex will ever guess. So take the
    assignment line as evidence too: if the right-hand side mentions a
    credential, the variable is treated as credential-carrying for the rest
    of the file.

    Widening the CANDIDATE set is nearly free here — the partial-run rule,
    not the name filter, is what actually decides pass/fail. Broad
    selection, sharp verdict."""
    out = set()
    for line in lines:
        if line.lstrip().startswith("#"):
            continue
        m = ASSIGN_RE.match(line)
        if not m:
            continue
        name, rhs = m.group(1), m.group(2)
        if CRED_CAMEL.search(rhs):
            out.add(name)
    return out


def split_statement(line: str, start: int) -> str:
    """Take the echo/printf statement beginning at `start`, stopping at the
    first UNQUOTED shell separator or redirection. Quote-aware so a `;` or
    `|` inside the message text does not truncate the statement."""
    i = start
    n = len(line)
    sq = dq = False
    while i < n:
        c = line[i]
        if c == "\\" and not sq:
            i += 2
            continue
        if c == "'" and not dq:
            sq = not sq
        elif c == '"' and not sq:
            dq = not dq
        elif not sq and not dq:
            if c in ";|&":
                break
            if c in "<>":
                break
        i += 1
    return line[start:i].rstrip()


CMD_START = re.compile(r"(?:^|[;&|(]|\bthen\b|\bdo\b|\belse\b|\{)\s*(echo|printf)(?=\s|$)")


def statements(line: str):
    for m in CMD_START.finditer(line):
        yield m.start(1), split_statement(line, m.start(1))


def evaluate(stmt: str, cred_var: str, other_vars):
    """Bind cred_var to SENTINEL, everything else to a benign value, run the
    statement, return emitted bytes. None if it could not be executed."""
    assigns = ["%s=%s" % (cred_var, SENTINEL)]
    for v in other_vars:
        if v == cred_var:
            continue
        assigns.append("%s=benign" % v)
    script = "\n".join(assigns) + "\n" + stmt + "\n"
    try:
        p = subprocess.run(
            ["bash", "--norc", "--noprofile", "-c", script],
            capture_output=True, text=True, timeout=10,
            env={"PATH": "/usr/bin:/bin", "LC_ALL": "C"},
        )
    except Exception:
        return None
    if p.returncode != 0 and not p.stdout and not p.stderr:
        return None
    return (p.stdout or "") + (p.stderr or "")


def sentinel_bytes(out: str):
    """(full_hit, partial_hit) — partial means a >=WINDOW-char run of the
    sentinel appears but the whole sentinel does not."""
    if SENTINEL in out:
        return True, False
    for i in range(0, len(SENTINEL) - WINDOW + 1):
        if SENTINEL[i:i + WINDOW] in out:
            return False, True
    return False, False


def scan(root: str):
    failures, waivers, unevaluated, checked = [], [], [], 0
    for rel in collect_files(root):
        path = os.path.join(root, rel)
        try:
            with open(path, encoding="utf-8", errors="replace") as fh:
                lines = fh.read().splitlines()
        except OSError:
            continue
        by_assign = cred_vars_by_assignment(lines)
        for lineno, line in enumerate(lines, 1):
            stripped = line.lstrip()
            # A shell/YAML comment cannot emit anything. The #5467
            # postmortem comments quote the defective expression verbatim
            # and must not re-trip the guard that documents them.
            if stripped.startswith("#"):
                continue
            for _, stmt in statements(line):
                names = set()
                for m in VAR_RE.finditer(stmt):
                    names.add(m.group(1) or m.group(2))
                creds = sorted(n for n in names
                               if is_cred(n) or n in by_assign)
                if not creds:
                    continue
                if WAIVER.search(line):
                    waivers.append((rel, lineno, creds, line.strip()))
                    continue
                checked += 1
                unsafe = "$(" in stmt or "`" in stmt
                for cv in creds:
                    if unsafe:
                        if TRUNC_STATIC.search(stmt):
                            failures.append(
                                (rel, lineno, cv, "static: truncation idiom "
                                 "applied to a credential inside a command "
                                 "substitution", stmt))
                        else:
                            unevaluated.append((rel, lineno, cv, stmt))
                        continue
                    out = evaluate(stmt, cv, names)
                    if out is None:
                        unevaluated.append((rel, lineno, cv, stmt))
                        continue
                    full, partial = sentinel_bytes(out)
                    if partial:
                        leaked = "".join(
                            ch for ch in out if ch in set(SENTINEL))
                        failures.append(
                            (rel, lineno, cv,
                             "emitted %d chars of the credential value"
                             % len(leaked), stmt))
    return failures, waivers, unevaluated, checked


def report(failures, waivers, unevaluated, checked, root_label):
    print("=== #5467 credential-prefix logging guard (%s) ===" % root_label)
    print("output statements interpolating a credential-named variable: %d"
          % checked)
    print("statically-checked (command substitution, not executed):    %d"
          % len(unevaluated))
    print("waived (# credlog-ok):                                      %d"
          % len(waivers))
    for rel, lineno, creds, text in waivers:
        print("  WAIVED %s:%d  vars=%s" % (rel, lineno, ",".join(creds)))
        print("         %s" % text[:160])
    if failures:
        print("")
        print("FAIL: %d statement(s) emit part of a credential value into a "
              "log line." % len(failures))
        for rel, lineno, cv, why, stmt in failures:
            print("")
            print("  %s:%d" % (rel, lineno))
            print("    variable : %s" % cv)
            print("    verdict  : %s" % why)
            print("    statement: %s" % stmt[:200])
        print("")
        print("Secret VALUES are never printed — lengths and hashes only.")
        print("Print `(len=${#var})` and the non-secret account/user instead;")
        print("that preserves every bit of the diagnostic (was a credential")
        print("present, is it the right length, whose is it) at zero cost.")
        return 1
    print("")
    print("PASS: no statement emits a fragment of a credential value.")
    return 0


def self_test():
    """Vacuity check. The detector must go RED on a known-bad fixture and
    GREEN on a known-good one. A guard that cannot fail is not a guard."""
    fixtures = os.path.join(ROOT, "scripts", "tests", "credlog-fixtures")
    bad = os.path.join(fixtures, "bad")
    good = os.path.join(fixtures, "good")
    ok = True

    print("=== self-test: detector must go RED on the bad fixture ===")
    f, w, u, c = scan(bad)
    rc = report(f, w, u, c, "fixture/bad")
    if rc == 0:
        print("SELF-TEST FAIL: the bad fixture did NOT trip the detector — "
              "this guard is vacuous.")
        ok = False
    else:
        print("self-test: bad fixture tripped %d finding(s) — detector is "
              "live." % len(f))

    print("")
    print("=== self-test: detector must stay GREEN on the good fixture ===")
    f2, w2, u2, c2 = scan(good)
    rc2 = report(f2, w2, u2, c2, "fixture/good")
    if rc2 != 0:
        print("SELF-TEST FAIL: the good fixture tripped the detector — "
              "length-only logging must never be flagged.")
        ok = False
    else:
        print("self-test: good fixture stayed green over %d credential "
              "statement(s) — the control holds." % c2)

    if c2 == 0:
        print("SELF-TEST FAIL: the good fixture exercised ZERO credential "
              "statements — the control proves nothing.")
        ok = False

    print("")
    if not ok:
        print("SELF-TEST: FAILED")
        return 1
    print("SELF-TEST: PASSED (red on bad, green on good)")
    return 0


if MODE == "self-test":
    sys.exit(self_test())

sys.exit(report(*scan(ROOT), root_label="repo"))
PY
