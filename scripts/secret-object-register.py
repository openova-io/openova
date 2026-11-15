#!/usr/bin/env python3
"""Guards G10 and G11 of the secret guards, and the matching rule of gate check SEC-G11 (SEC-095).

The exception register scripts/secret-guard-baselines/secret-object-register.yaml
lists every Secret object that the platform allows in a platform namespace.
scripts/check-secret-object-register.sh runs this file; CI runs that script.

G10, the register and the chart sources:
  a  The register parses and has the schema below: every field is present,
     no other field is, every value has its form, every id is unique.
  b  An entry of class U or T names a removal package (three capital letters,
     a dash, three digits) and its reviewBy date is not past. An entry of
     class K has the removal "none" or a package.
  c  Every literal value of the annotation secrets.openova.io/register-id in
     an own chart template is the id of an entry.
  d  Per own chart, the number of secretKeyRef lines (in its templates and
     its values.yaml) and the number of ExternalSecret documents without that
     annotation (in its templates) may not rise above the allow-list
     scripts/secret-guard-baselines/check-secret-object-register.txt, one
     line "<chart>:<secretKeyRef count>:<unannotated count>" per chart. A
     count that falls passes and prints the line to change or delete. Never
     raise or add a line.
  e  A chart whose templates or values.yaml carry the label
     network.openova.io/metadata-access: "true" is the chart of an entry of
     metadataAccess.
G11, the threat model: the lines of docs/SECURITY.md between the line
  <!-- sec-threat-model:begin --> and the line <!-- sec-threat-model:end -->
  equal scripts/secret-guard-baselines/threat-model.md. A change of the table
  changes both files in one pull request.

An own chart is a folder under platform/ or products/ that holds a Chart.yaml
and is not inside a charts/ folder. Comment lines (# or //) are not counted.

The register (one line per field; strings in double quotes):
  schema: openova.io/secret-object-register/v1
  allowedUnmatched: 0        integer >= 0; the census limit of SEC-G11; may only be lowered
  entries:
    - id: <[a-z0-9-]+, unique>
      namespace: <a namespace name | "*" (every platform namespace) | "<organization>">
      name: <pattern matched against the whole Secret name>
      class: <K | U | T>
      consumer: <the object and field that demand a Secret>
      reason: <why it is a Secret>
      removal: <"none" (class K only) | a package identifier>
      reviewBy: <YYYY-MM-DD>
  metadataAccess:            list, may be empty
    - chart: <path of an own chart>
      workload: <name of the Deployment, DaemonSet or StatefulSet>
      reason: <text>
      removal: <"none" | a package identifier>
      reviewBy: <YYYY-MM-DD>
A name pattern may use letters, digits and the signs - _ . ^ $ | ( ) [ ] * + ?
(but not the group "(?") and the escapes \\. and \\-, so that it means the same
in Go (RE2) and Python.

The census (gate check SEC-G11 runs it on each region):
  --census <file>  reads lines "<namespace>\\t<name>\\t<type>", one per Secret of
                   the platform namespaces of a region; skips the types
                   kubernetes.io/service-account-token, helm.sh/release.v1 and
                   bootstrap.kubernetes.io/token; a Secret is matched when an
                   entry has its namespace (or "*") and a name pattern that
                   matches the whole name. Prints one JSON line with the keys
                   allowedUnmatched, judged, skipped, matched, unmatched
                   ("<namespace>/<name>", sorted) and pastReview (ids of class
                   U and T entries whose reviewBy is past). Exit 0, or 2 when
                   the input or the register cannot be read.

Usage, from any folder:
  secret-object-register.py               run G10 and G11 on the tree
  secret-object-register.py --self-test   prove that each rule can fail
  secret-object-register.py --counts      print the allow-list lines of the tree as it is
  secret-object-register.py --census <file>
Exit codes: 0 pass, 1 finding (or failed self-test), 2 usage or setup error.
"""

import datetime
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile

REGISTER = "scripts/secret-guard-baselines/secret-object-register.yaml"
ALLOW_LIST = "scripts/secret-guard-baselines/check-secret-object-register.txt"
THREAT_COPY = "scripts/secret-guard-baselines/threat-model.md"
SECURITY = "docs/SECURITY.md"
THREAT_BEGIN = "<!-- sec-threat-model:begin -->"
THREAT_END = "<!-- sec-threat-model:end -->"
SCHEMA = "openova.io/secret-object-register/v1"
ENTRY_KEYS = ["id", "namespace", "name", "class", "consumer", "reason", "removal", "reviewBy"]
ACCESS_KEYS = ["chart", "workload", "reason", "removal", "reviewBy"]
TOP_KEYS = ["schema", "allowedUnmatched", "entries", "metadataAccess"]
SKIP_TYPES = ("kubernetes.io/service-account-token", "helm.sh/release.v1", "bootstrap.kubernetes.io/token")

PACKAGE = re.compile(r"^[A-Z]{3}-[0-9]{3}$")
ENTRY_ID = re.compile(r"^[a-z0-9-]+$")
NAMESPACE = re.compile(r"^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$")
PATTERN_CHARS = re.compile(r"^(?:[A-Za-z0-9_.^$|()\[\]*+?-]|\\[.-])+$")
DATE = re.compile(r"^[0-9]{4}-[0-9]{2}-[0-9]{2}$")
REGISTER_ID = re.compile(r"secrets\.openova\.io/register-id\"?\s*:\s*(.*)$")
EXTERNAL_KIND = re.compile(r"^\s*kind:\s*ExternalSecret\s*$")
DOC_SEPARATOR = re.compile(r"^---")
METADATA_LABEL = re.compile(r"network\.openova\.io/metadata-access\"?\s*:\s*[\"']?true[\"']?\s*$")
# git runs with -C <root>; a GIT_* variable of the caller would point it at another repository.
GIT_ENV = dict((k, v) for k, v in os.environ.items() if not k.startswith("GIT_"))


class SetupError(Exception):
    pass


# ---------------------------------------------------------------- the tree

def tracked_files(root):
    r = subprocess.run(["git", "-C", root, "ls-files", "-z"], stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=GIT_ENV)
    if r.returncode != 0:
        raise SetupError("git ls-files failed in %s: %s" % (root, r.stderr.decode("utf-8", "replace").strip()))
    return sorted(p for p in r.stdout.decode("utf-8", "replace").split("\0") if p)


def read_text(root, path):
    try:
        with open(os.path.join(root, path), "rb") as f:
            data = f.read()
    except OSError:
        return None
    if b"\0" in data:
        return None
    return data.decode("utf-8", "replace")


def is_comment(line):
    s = line.lstrip()
    return s.startswith("#") or s.startswith("//")


def own_charts(files):
    """Every own chart: a folder under platform/ or products/ with a Chart.yaml, not inside a charts/ folder."""
    out = []
    for p in files:
        if p.endswith("/Chart.yaml") and p.split("/", 1)[0] in ("platform", "products"):
            chart = p[: -len("/Chart.yaml")]
            if "/charts/" not in chart + "/":
                out.append(chart)
    return sorted(out)


def chart_files(files, chart):
    """The templates of a chart and its values.yaml."""
    templates = [p for p in files if p.startswith(chart + "/templates/")]
    values = [p for p in files if p == chart + "/values.yaml"]
    return templates, values


# ---------------------------------------------------------------- the register

def load_register(root):
    """The register as data. A file that is missing or is not YAML is a setup error."""
    text = read_text(root, REGISTER)
    if text is None:
        raise SetupError("the register %s does not exist" % REGISTER)
    try:
        import yaml
    except ImportError:
        raise SetupError("PyYAML is not installed")
    try:
        return yaml.safe_load(text)
    except yaml.YAMLError as e:
        raise SetupError("the register %s is not YAML: %s" % (REGISTER, str(e).replace("\n", " ")))


def as_date(value):
    if isinstance(value, datetime.date) and not isinstance(value, datetime.datetime):
        return value
    if isinstance(value, str) and DATE.match(value):
        try:
            return datetime.date.fromisoformat(value)
        except ValueError:
            return None
    return None


def text_field(where, key, value, bad):
    if not isinstance(value, str) or not value.strip():
        bad("%s: %s must be a text that is not empty" % (where, key))
        return False
    return True


def rule_schema(reg, files):
    """Rule a: the form of the register. Returns failure lines."""
    out = []
    bad = out.append
    if not isinstance(reg, dict):
        return ["FAIL G10 rule a: the register is not a mapping"]
    keys = list(reg)
    if sorted(keys) != sorted(TOP_KEYS):
        bad("the register has the fields %s; it must have exactly %s" % (", ".join(map(str, keys)), ", ".join(TOP_KEYS)))
    if reg.get("schema") != SCHEMA:
        bad("schema must be %s" % SCHEMA)
    n = reg.get("allowedUnmatched")
    if isinstance(n, bool) or not isinstance(n, int) or n < 0:
        bad("allowedUnmatched must be an integer of 0 or more")
    entries = reg.get("entries")
    if not isinstance(entries, list):
        bad("entries must be a list")
        entries = []
    seen = set()
    charts = set(own_charts(files))
    for i, e in enumerate(entries, 1):
        where = "entry %d" % i
        if not isinstance(e, dict):
            bad("%s is not a mapping" % where)
            continue
        if isinstance(e.get("id"), str):
            where = "entry %s" % e["id"]
        if sorted(e) != sorted(ENTRY_KEYS):
            bad("%s has the fields %s; it must have exactly %s" % (where, ", ".join(map(str, e)), ", ".join(ENTRY_KEYS)))
        for k in ("id", "namespace", "name", "class", "consumer", "reason", "removal"):
            if k in e:
                text_field(where, k, e[k], bad)
        i_d = e.get("id")
        if isinstance(i_d, str):
            if not ENTRY_ID.match(i_d):
                bad("%s: the id must match %s" % (where, ENTRY_ID.pattern))
            if i_d in seen:
                bad("%s: the id is used twice" % where)
            seen.add(i_d)
        ns = e.get("namespace")
        if isinstance(ns, str) and ns not in ("*", "<organization>") and not NAMESPACE.match(ns):
            bad("%s: namespace %r is not a namespace name, \"*\" or \"<organization>\"" % (where, ns))
        name = e.get("name")
        if isinstance(name, str) and name:
            if not PATTERN_CHARS.match(name) or "(?" in name:
                bad("%s: name %r uses a sign that RE2 and Python may read differently" % (where, name))
            else:
                try:
                    re.compile(name)
                except re.error as x:
                    bad("%s: name %r is not a valid pattern: %s" % (where, name, x))
        if "class" in e and e.get("class") not in ("K", "U", "T"):
            bad("%s: class must be K, U or T" % where)
        if "reviewBy" in e and as_date(e.get("reviewBy")) is None:
            bad("%s: reviewBy must be a date YYYY-MM-DD" % where)
    access = reg.get("metadataAccess")
    if not isinstance(access, list):
        bad("metadataAccess must be a list (it may be empty)")
        access = []
    for i, a in enumerate(access, 1):
        where = "metadataAccess entry %d" % i
        if not isinstance(a, dict):
            bad("%s is not a mapping" % where)
            continue
        if sorted(a) != sorted(ACCESS_KEYS):
            bad("%s has the fields %s; it must have exactly %s" % (where, ", ".join(map(str, a)), ", ".join(ACCESS_KEYS)))
        for k in ("chart", "workload", "reason", "removal"):
            if k in a:
                text_field(where, k, a[k], bad)
        if isinstance(a.get("chart"), str) and a["chart"] and a["chart"] not in charts:
            bad("%s: chart %r is not an own chart (a folder with a Chart.yaml under platform/ or products/)" % (where, a["chart"]))
        r = a.get("removal")
        if isinstance(r, str) and r and r != "none" and not PACKAGE.match(r):
            bad("%s: removal must be \"none\" or a package identifier" % where)
        if "reviewBy" in a and as_date(a.get("reviewBy")) is None:
            bad("%s: reviewBy must be a date YYYY-MM-DD" % where)
    return ["FAIL G10 rule a: " + x for x in out]


def rule_classes(reg, today):
    """Rule b: removal packages and review dates."""
    out = []
    for e in reg.get("entries") or []:
        if not isinstance(e, dict):
            continue
        where = "entry %s" % e.get("id")
        c, r, d = e.get("class"), e.get("removal"), as_date(e.get("reviewBy"))
        if c in ("U", "T"):
            if not (isinstance(r, str) and PACKAGE.match(r)):
                out.append("%s: class %s must name a removal package, not %r" % (where, c, r))
            if d is not None and d < today:
                out.append("%s: class %s, its reviewBy %s is past; review the entry and set a new date" % (where, c, d.isoformat()))
        elif c == "K" and not (r == "none" or (isinstance(r, str) and PACKAGE.match(r))):
            out.append("%s: class K takes the removal \"none\" or a package identifier, not %r" % (where, r))
    return ["FAIL G10 rule b: " + x for x in out]


def entry_ids(reg):
    return {e.get("id") for e in (reg.get("entries") or []) if isinstance(e, dict)}


def rule_annotations(root, files, reg):
    """Rule c: a literal register-id annotation names an entry."""
    ids = entry_ids(reg)
    out = []
    for chart in own_charts(files):
        templates, _ = chart_files(files, chart)
        for p in templates:
            text = read_text(root, p)
            if text is None:
                continue
            for n, line in enumerate(text.split("\n"), 1):
                if is_comment(line):
                    continue
                m = REGISTER_ID.search(line)
                if not m:
                    continue
                value = m.group(1).strip().strip("\"'")
                if "{{" in value:
                    continue
                if value not in ids:
                    out.append("FAIL G10 rule c: %s:%d: the annotation names %r, which is not the id of a register entry" % (p, n, value))
    return out


def chart_counts(root, files):
    """{chart: (secretKeyRef lines, ExternalSecret documents without the annotation)} for every chart with a count above 0."""
    found = {}
    for chart in own_charts(files):
        templates, values = chart_files(files, chart)
        refs, unannotated = 0, 0
        for p in templates + values:
            text = read_text(root, p)
            if text is None:
                continue
            lines = text.split("\n")
            refs += sum(1 for l in lines if "secretKeyRef" in l and not is_comment(l))
            if p in values:
                continue
            doc = []
            for l in lines + ["---"]:
                if DOC_SEPARATOR.match(l):
                    if any(EXTERNAL_KIND.match(x) for x in doc) and not any(REGISTER_ID.search(x) and not is_comment(x) for x in doc):
                        unannotated += 1
                    doc = []
                else:
                    doc.append(l)
        if refs or unannotated:
            found[chart] = (refs, unannotated)
    return found


def read_allow_list(root):
    text = read_text(root, ALLOW_LIST)
    if text is None:
        raise SetupError("the allow-list %s does not exist" % ALLOW_LIST)
    allowed = {}
    for n, line in enumerate(text.split("\n"), 1):
        if line == "":
            continue
        m = re.match(r"^([^:\s]+):([0-9]+):([0-9]+)$", line)
        if not m or m.group(1) in allowed or (m.group(2) == "0" and m.group(3) == "0"):
            raise SetupError("%s line %d is not a new '<chart>:<secretKeyRef count>:<unannotated count>' line: %r" % (ALLOW_LIST, n, line))
        allowed[m.group(1)] = (int(m.group(2)), int(m.group(3)))
    return allowed


def count_line(chart, c):
    return "%s:%d:%d" % (chart, c[0], c[1])


def rule_counts(root, files, say):
    """Rule d: the per-chart counts against the allow-list."""
    found = chart_counts(root, files)
    allowed = read_allow_list(root)
    out = []
    for chart, c in sorted(found.items()):
        have = allowed.get(chart)
        if have is None:
            out.append("FAIL G10 rule d: %s has %d secretKeyRef line(s) and %d ExternalSecret(s) without the annotation, "
                       "and no line in the allow-list" % (chart, c[0], c[1]))
        elif c[0] > have[0] or c[1] > have[1]:
            out.append("FAIL G10 rule d: %s has %s, the allow-list allows %s" % (chart, count_line(chart, c), count_line(chart, have)))
    for chart, have in sorted(allowed.items()):
        now = found.get(chart, (0, 0))
        if now == (0, 0):
            say("LOWER %s: delete the line '%s'" % (ALLOW_LIST, count_line(chart, have)))
        elif now[0] <= have[0] and now[1] <= have[1] and now != have:
            say("LOWER %s: replace the line '%s' with '%s'" % (ALLOW_LIST, count_line(chart, have),
                                                           count_line(chart, (min(now[0], have[0]), min(now[1], have[1])))))
    return out


def rule_metadata_access(root, files, reg):
    """Rule e: a chart with the metadata-access label has an entry in metadataAccess."""
    listed = {a.get("chart") for a in (reg.get("metadataAccess") or []) if isinstance(a, dict)}
    out = []
    for chart in own_charts(files):
        templates, values = chart_files(files, chart)
        for p in templates + values:
            text = read_text(root, p)
            if text is None:
                continue
            for n, line in enumerate(text.split("\n"), 1):
                if not is_comment(line) and METADATA_LABEL.search(line) and chart not in listed:
                    out.append("FAIL G10 rule e: %s:%d carries the label network.openova.io/metadata-access, "
                               "and metadataAccess has no entry for %s" % (p, n, chart))
    return out


def rule_threat_model(root):
    """G11: the table between the markers of docs/SECURITY.md equals the copy."""
    text = read_text(root, SECURITY)
    if text is None:
        return ["FAIL G11: %s does not exist" % SECURITY]
    lines = text.split("\n")
    if lines.count(THREAT_BEGIN) != 1 or lines.count(THREAT_END) != 1:
        return ["FAIL G11: %s must hold the line %s once and the line %s once" % (SECURITY, THREAT_BEGIN, THREAT_END)]
    b, e = lines.index(THREAT_BEGIN), lines.index(THREAT_END)
    if e < b:
        return ["FAIL G11: in %s the line %s comes before %s" % (SECURITY, THREAT_END, THREAT_BEGIN)]
    table = "\n".join(lines[b + 1:e]) + "\n"
    copy = read_text(root, THREAT_COPY)
    if copy is None:
        return ["FAIL G11: %s does not exist" % THREAT_COPY]
    if table == copy:
        return []
    a, c = table.split("\n"), copy.split("\n")
    n = next((i for i in range(max(len(a), len(c))) if (a[i] if i < len(a) else None) != (c[i] if i < len(c) else None)), 0)
    return ["FAIL G11: the threat model in %s differs from %s at line %d of the table. Change both in one pull request."
            % (SECURITY, THREAT_COPY, n + 1)]


def check(root, today, say):
    """G10 and G11 on the tree at root. Returns the exit code."""
    files = tracked_files(root)
    reg = load_register(root)
    fails = rule_schema(reg, files)
    if not fails:
        fails += rule_classes(reg, today)
        fails += rule_annotations(root, files, reg)
        fails += rule_metadata_access(root, files, reg)
    fails += rule_counts(root, files, say)
    fails += rule_threat_model(root)
    for f in fails:
        say(f)
    if fails:
        say("FAIL G10/G11: %d finding(s). The allow-list and the register may only shrink: remove the new finding; "
            "never raise or add an allow-list line." % len(fails))
        return 1
    say("OK G10: the register holds %d entries and %d metadataAccess entries; no chart is above the allow-list."
        % (len(reg["entries"]), len(reg["metadataAccess"])))
    say("OK G11: the threat model in %s equals %s." % (SECURITY, THREAT_COPY))
    return 0


# ---------------------------------------------------------------- the census

def census(root, path, today):
    """The matching rule of SEC-G11 on one region's list of Secrets. Returns the result as data."""
    reg = load_register(root)
    problems = rule_schema(reg, tracked_files(root))
    if problems:
        raise SetupError("the register is not valid: " + "; ".join(problems))
    try:
        with open(path, encoding="utf-8") as f:
            text = f.read()
    except OSError as e:
        raise SetupError("cannot read %s: %s" % (path, e))
    entries = [(e["id"], e["namespace"], re.compile(e["name"]), e["class"], as_date(e["reviewBy"])) for e in reg["entries"]]
    judged, skipped, matched, unmatched = 0, 0, 0, []
    for n, line in enumerate(text.split("\n"), 1):
        if line == "":
            continue
        parts = line.split("\t")
        if len(parts) != 3 or not parts[0] or not parts[1]:
            raise SetupError("%s line %d is not '<namespace><TAB><name><TAB><type>'" % (path, n))
        ns, name, kind = parts
        if kind in SKIP_TYPES:
            skipped += 1
            continue
        judged += 1
        if any((e_ns == ns or e_ns == "*") and pat.fullmatch(name) for _, e_ns, pat, _, _ in entries):
            matched += 1
        else:
            unmatched.append("%s/%s" % (ns, name))
    past = sorted(i for i, _, _, c, d in entries if c in ("U", "T") and d is not None and d < today)
    return {"allowedUnmatched": reg["allowedUnmatched"], "judged": judged, "skipped": skipped, "matched": matched,
            "unmatched": sorted(unmatched), "pastReview": past}


# ---------------------------------------------------------------- self-test
#
# Each case builds a throw-away git repository and runs the same code as the
# real run on it. No cluster, no network.

CHART = "name: fixture\nversion: 0.1.0\n"
TABLE = "| Actor | Obtains today | Obtains in the target |\n|---|---|---|\n| Reads Git | x | y |\n"
SECURITY_DOC = "# Security\n\n%s\n%s%s\n\nEnd.\n" % (THREAT_BEGIN, TABLE, THREAT_END)
TODAY = datetime.date(2026, 11, 15)


def register_text(entries=None, access="[]", allowed="0", schema=SCHEMA, extra=""):
    if entries is None:
        entries = [entry_text()]
    return ("schema: %s\nallowedUnmatched: %s\n%sentries:\n%smetadataAccess: %s\n"
            % (schema, allowed, extra, "".join(entries), access))


def entry_text(i="pull-login", ns='"*"', name='"^ghcr-pull$"', cls="T", removal="SOV-810", review='"2027-11-01"', drop=None, add=""):
    fields = [("id", i), ("namespace", ns), ("name", name), ("class", cls), ("consumer", '"imagePullSecrets"'),
              ("reason", '"a pull login is a Secret"'), ("removal", removal), ("reviewBy", review)]
    lines = ["%s: %s" % (k, v) for k, v in fields if k != drop]
    return "  - " + "\n    ".join(lines) + "\n" + add


def base_tree():
    return {
        REGISTER: register_text([entry_text(), entry_text("gateway-tls", "kube-system", '"^gw-tls-[a-z0-9-]+$"', "K", "none")]),
        ALLOW_LIST: "platform/a/chart:2:1\n",
        THREAT_COPY: TABLE,
        SECURITY: SECURITY_DOC,
        "platform/a/chart/Chart.yaml": CHART,
        "platform/a/chart/values.yaml": ("env:\n  - valueFrom:\n      secretKeyRef: {name: x, key: y}\n# secretKeyRef in a comment\n"
                                         "extraObjects:\n  - apiVersion: external-secrets.io/v1beta1\n    kind: ExternalSecret\n"),
        "platform/a/chart/templates/d.yaml": "env:\n  - valueFrom:\n      secretKeyRef:\n        name: x\n",
        "platform/a/chart/templates/es.yaml": ("apiVersion: external-secrets.io/v1beta1\nkind: ExternalSecret\nmetadata:\n  name: a\n"
                                               "---\nkind: ExternalSecret\nmetadata:\n  annotations:\n"
                                               "    secrets.openova.io/register-id: gateway-tls\n"
                                               "---\nkind: ExternalSecret\nmetadata:\n  annotations:\n"
                                               "    secrets.openova.io/register-id: {{ .Values.id | quote }}\n"),
        "platform/a/chart/charts/up/Chart.yaml": CHART,
        "platform/a/chart/charts/up/templates/e.yaml": "kind: ExternalSecret\n---\nsecretKeyRef: z\n",
        "platform/b/README.md": "secretKeyRef\nkind: ExternalSecret\n",
        "clusters/x/k.yaml": "secretKeyRef: q\n",
    }


def make_tree(files):
    root = tempfile.mkdtemp(prefix="secret-object-register-")
    for p, content in files.items():
        full = os.path.join(root, p)
        os.makedirs(os.path.dirname(full), exist_ok=True)
        with open(full, "w", encoding="utf-8") as f:
            f.write(content)
    for args in (["init", "-q"], ["add", "-A"]):
        subprocess.run(["git", "-C", root] + args, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env=GIT_ENV)
    return root


def run_case(files):
    out = []
    root = make_tree(files)
    try:
        code = check(root, TODAY, out.append)
    except SetupError as e:
        code, out = 2, [str(e)]
    finally:
        shutil.rmtree(root, ignore_errors=True)
    return code, out


def run_census(files, lines):
    root = make_tree(files)
    try:
        path = os.path.join(root, "census.tsv")
        with open(path, "w", encoding="utf-8") as f:
            f.write(lines)
        try:
            return 0, census(root, path, TODAY)
        except SetupError as e:
            return 2, str(e)
    finally:
        shutil.rmtree(root, ignore_errors=True)


def with_changes(changes):
    t = base_tree()
    for k, v in changes.items():
        if v is None:
            t.pop(k, None)
        else:
            t[k] = v
    return t


def self_test():
    fails = []

    def expect(label, ok, detail=""):
        print("  %s %s" % ("ok  " if ok else "FAIL", label))
        if not ok:
            fails.append(label)
            if detail:
                print("       " + str(detail).replace("\n", "\n       "))

    def fails_with(label, changes, rule):
        code, out = run_case(with_changes(changes))
        expect("fails: " + label, code == 1 and any(rule in l for l in out), "\n".join(out))

    code, out = run_case(base_tree())
    expect("the clean tree passes, and nothing asks to be lowered",
           code == 0 and not any(l.startswith(("LOWER", "FAIL")) for l in out), "\n".join(out))
    root = make_tree(base_tree())
    try:
        counts = chart_counts(root, tracked_files(root))
    finally:
        shutil.rmtree(root, ignore_errors=True)
    expect("rule d counts the own chart only: 2 secretKeyRef lines (values and templates, no comment) and 1 ExternalSecret "
           "without the annotation (an annotated one, one in values.yaml and a vendored one are not counted)",
           counts == {"platform/a/chart": (2, 1)}, counts)

    e0 = entry_text()
    k0 = entry_text("gateway-tls", "kube-system", '"^gw-tls-[a-z0-9-]+$"', "K", "none")
    # rule a
    fails_with("a missing field", {REGISTER: register_text([entry_text(drop="reason"), k0])}, "rule a")
    fails_with("an unknown field of an entry", {REGISTER: register_text([entry_text(add="    owner: x\n"), k0])}, "rule a")
    fails_with("an unknown top-level field", {REGISTER: register_text([e0, k0], extra="note: x\n")}, "rule a")
    fails_with("a wrong schema", {REGISTER: register_text([e0, k0], schema="openova.io/secret-object-register/v2")}, "rule a")
    fails_with("a negative allowedUnmatched", {REGISTER: register_text([e0, k0], allowed="-1")}, "rule a")
    fails_with("allowedUnmatched that is not an integer", {REGISTER: register_text([e0, k0], allowed="true")}, "rule a")
    fails_with("an id used twice", {REGISTER: register_text([e0, e0, k0])}, "rule a")
    fails_with("an id with a capital letter", {REGISTER: register_text([entry_text("Pull"), k0])}, "rule a")
    fails_with("a namespace that is not a namespace name", {REGISTER: register_text([entry_text(ns="Flux_System"), k0])}, "rule a")
    fails_with("a name that is not a pattern", {REGISTER: register_text([entry_text(name='"^a(b$"'), k0])}, "rule a")
    fails_with("a name with a sign RE2 and Python read differently", {REGISTER: register_text([entry_text(name='"^(?i)a$"'), k0])}, "rule a")
    fails_with("an empty consumer", {REGISTER: register_text([entry_text().replace('"imagePullSecrets"', '""'), k0])}, "rule a")
    fails_with("a class other than K, U and T", {REGISTER: register_text([entry_text(cls="X"), k0])}, "rule a")
    fails_with("a reviewBy that is not a date", {REGISTER: register_text([entry_text(review='"next year"'), k0])}, "rule a")
    fails_with("metadataAccess that is not a list", {REGISTER: register_text([e0, k0], access='"none"')}, "rule a")
    access = ('\n  - chart: platform/a/chart\n    workload: w\n    reason: "r"\n    removal: none\n    reviewBy: "2027-11-01"\n')
    fails_with("a metadataAccess entry without a field", {REGISTER: register_text([e0, k0], access=access.replace("    workload: w\n", ""))},
               "rule a")
    fails_with("a metadataAccess entry whose chart is not an own chart",
               {REGISTER: register_text([e0, k0], access=access.replace("platform/a/chart", "platform/a/chart/charts/up"))}, "rule a")
    fails_with("the register is a list", {REGISTER: "- a\n"}, "rule a")
    code, out = run_case(with_changes({REGISTER: "schema: [\n"}))
    expect("a register that is not YAML is a setup error", code == 2, "\n".join(out))
    code, out = run_case(with_changes({REGISTER: None}))
    expect("a missing register is a setup error", code == 2, "\n".join(out))
    # rule b
    fails_with("class T with the removal none", {REGISTER: register_text([entry_text(removal="none"), k0])}, "rule b")
    fails_with("class U with a removal that is not a package", {REGISTER: register_text([entry_text(cls="U", removal="SEC-34"), k0])}, "rule b")
    fails_with("class T whose reviewBy is past", {REGISTER: register_text([entry_text(review='"2026-11-14"'), k0])}, "rule b")
    fails_with("class K with a removal that is not none or a package",
               {REGISTER: register_text([e0, entry_text("gateway-tls", "kube-system", '"^gw$"', "K", "later")])}, "rule b")
    code, out = run_case(with_changes({REGISTER: register_text([entry_text(review='"2026-11-15"'),
                                                                entry_text("gateway-tls", "kube-system", '"^gw$"', "K", "none", '"2020-01-01"')])}))
    expect("a reviewBy of today passes, and a past reviewBy of class K passes", code == 0, "\n".join(out))
    # rule c
    fails_with("a literal annotation value that is no entry id",
               {"platform/a/chart/templates/es.yaml": base_tree()["platform/a/chart/templates/es.yaml"].replace(
                   "register-id: gateway-tls", "register-id: \"gateway-tsl\"")}, "rule c")
    # rule d
    fails_with("one more secretKeyRef line in a template",
               {"platform/a/chart/templates/d.yaml": base_tree()["platform/a/chart/templates/d.yaml"] + "      secretKeyRef: {}\n"}, "rule d")
    fails_with("one more secretKeyRef line in values.yaml",
               {"platform/a/chart/values.yaml": base_tree()["platform/a/chart/values.yaml"] + "secretKeyRef: {}\n"}, "rule d")
    fails_with("one more ExternalSecret without the annotation",
               {"platform/a/chart/templates/es2.yaml": "kind: ExternalSecret\nmetadata:\n  name: b\n"}, "rule d")
    fails_with("an ExternalSecret whose annotation is a comment",
               {"platform/a/chart/templates/es2.yaml": "kind: ExternalSecret\n# secrets.openova.io/register-id: gateway-tls\n"}, "rule d")
    fails_with("a chart with a count and no allow-list line",
               {"products/c/chart/Chart.yaml": CHART, "products/c/chart/templates/x.yaml": "secretKeyRef:\n"}, "rule d")
    code, out = run_case(with_changes({"platform/a/chart/templates/d.yaml": "env: []\n"}))
    expect("a fall passes and asks for the lower line",
           code == 0 and "LOWER %s: replace the line 'platform/a/chart:2:1' with 'platform/a/chart:1:1'" % ALLOW_LIST in out, "\n".join(out))
    code, out = run_case(with_changes({"platform/a/chart/templates/d.yaml": "env: []\n", "platform/a/chart/values.yaml": "x: 1\n",
                                       "platform/a/chart/templates/es.yaml": "kind: ConfigMap\n"}))
    expect("a chart with no count left asks to delete its line",
           code == 0 and "LOWER %s: delete the line 'platform/a/chart:2:1'" % ALLOW_LIST in out, "\n".join(out))
    for label, text in (("a malformed allow-list line", "platform/a/chart:2\n"), ("an allow-list line of two zeros", "platform/a/chart:2:1\nplatform/z/chart:0:0\n"),
                        ("an allow-list line twice", "platform/a/chart:2:1\nplatform/a/chart:2:1\n")):
        code, out = run_case(with_changes({ALLOW_LIST: text}))
        expect("%s is a setup error" % label, code == 2, "\n".join(out))
    code, out = run_case(with_changes({ALLOW_LIST: None}))
    expect("a missing allow-list is a setup error", code == 2, "\n".join(out))
    # rule e
    label = {"platform/a/chart/templates/p.yaml": "metadata:\n  labels:\n    network.openova.io/metadata-access: \"true\"\n"}
    fails_with("the metadata-access label without an entry", label, "rule e")
    code, out = run_case(with_changes(dict(label, **{REGISTER: register_text([e0, k0], access=access)})))
    expect("the metadata-access label with an entry for its chart passes", code == 0, "\n".join(out))
    code, out = run_case(with_changes({"platform/a/chart/templates/p.yaml": "    network.openova.io/metadata-access: \"false\"\n"}))
    expect("the label with the value false needs no entry", code == 0, "\n".join(out))
    # G11
    fails_with("a threat table that differs from the copy", {THREAT_COPY: TABLE.replace("| x |", "| z |")}, "G11")
    fails_with("a threat table without its markers", {SECURITY: "# Security\n\n" + TABLE}, "G11")
    fails_with("a threat table whose end marker comes first", {SECURITY: "%s\n%s%s\n" % (THREAT_END, TABLE, THREAT_BEGIN)}, "G11")
    fails_with("a missing copy of the threat table", {THREAT_COPY: None}, "G11")
    # the census
    secrets = ("flux-system\tghcr-pull\tkubernetes.io/dockerconfigjson\n"
               "gitea\tghcr-pull\tkubernetes.io/dockerconfigjson\n"
               "kube-system\tgw-tls-a-b\tkubernetes.io/tls\n"
               "default\tgw-tls-a-b\tkubernetes.io/tls\n"
               "flux-system\tghcr-pull-old\tOpaque\n"
               "kube-system\tsa-token\tkubernetes.io/service-account-token\n"
               "kube-system\tsh.helm.release.v1.x.v1\thelm.sh/release.v1\n"
               "kube-system\tbootstrap-token-abc\tbootstrap.kubernetes.io/token\n")
    code, got = run_census(base_tree(), secrets)
    want = {"allowedUnmatched": 0, "judged": 5, "skipped": 3, "matched": 3,
            "unmatched": ["default/gw-tls-a-b", "flux-system/ghcr-pull-old"], "pastReview": []}
    expect("census: a namespace or \"*\" with a pattern that matches the whole name matches; three types are skipped", code == 0 and got == want, got)
    code, got = run_census(with_changes({REGISTER: register_text([entry_text(review='"2026-01-01"'), k0], allowed="7")}), "a\tb\tOpaque\n")
    expect("census: an entry of class T past its reviewBy is listed, and allowedUnmatched is read",
           code == 0 and got["pastReview"] == ["pull-login"] and got["allowedUnmatched"] == 7 and got["unmatched"] == ["a/b"], got)
    code, got = run_census(with_changes({REGISTER: register_text([entry_text(name='"ghcr-pull"'), k0])}),
                           "flux-system\tghcr-pull-old\tOpaque\nflux-system\tghcr-pull\tOpaque\n")
    expect("census: a pattern must match the whole name, not a part of it",
           code == 0 and got["unmatched"] == ["flux-system/ghcr-pull-old"] and got["matched"] == 1, got)
    code, got = run_census(base_tree(), "a b Opaque\n")
    expect("census: a line without two tabs is a setup error", code == 2, got)
    code, got = run_census(with_changes({REGISTER: register_text([entry_text(drop="class"), k0])}), "a\tb\tOpaque\n")
    expect("census: an invalid register is a setup error", code == 2, got)
    if fails:
        print("SELF-TEST FAILED: %d case(s)" % len(fails))
        return 1
    print("OK G10/G11: self-test passed; fixtures only, no cluster was contacted.")
    return 0


# ---------------------------------------------------------------- main

def main(argv):
    root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    today = datetime.datetime.now(datetime.timezone.utc).date()
    try:
        if argv == ["--self-test"]:
            return self_test()
        if argv == ["--counts"]:
            for chart, c in sorted(chart_counts(root, tracked_files(root)).items()):
                print(count_line(chart, c))
            return 0
        if len(argv) == 2 and argv[0] == "--census":
            print(json.dumps(census(root, argv[1], today), sort_keys=True))
            return 0
        if argv:
            print("usage: secret-object-register.py [--self-test | --counts | --census <file>]", file=sys.stderr)
            return 2
        return check(root, today, print)
    except SetupError as e:
        print("ERROR secret-object-register: %s" % e, file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
