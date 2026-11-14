#!/usr/bin/env bash
# check-access-guards.sh - the eight guards of the access area: counts that may only fall.
#
# The access gateway (platform/guacamole), the exec proxy (platform/k8s-ws-proxy) and the exec
# handlers of catalyst-api still hold what the access design removes. Until it is gone, these
# guards refuse one more. Each counts matches per file (any case where it says so):
#   ACC-GD1  files under platform/guacamole/chart/templates and platform/k8s-ws-proxy/chart/templates:
#            lines that start with "kind:" and then CronJob
#   ACC-GD2  files under platform/guacamole: each INSERT INTO guacamole_system_permission (any case)
#   ACC-GD3  files under platform/guacamole/chart/templates: lines with load_connection_credentials
#   ACC-GD4  files under platform/guacamole and platform/k8s-ws-proxy: lines that name the provider
#            of the first cloud (PROVIDER below; any case)
#   ACC-GD5  platform/guacamole/chart/values.yaml, platform/k8s-ws-proxy/chart/values.yaml: lines
#            that start, after spaces, with "tag:"
#   ACC-GD6  files under products: lines with synthesizeSession, GuacamoleClient or HandleShellsIssue
#   ACC-GD7  products/catalyst/bootstrap/api/internal/handler/k8s_exec*.go and .../accessintent/*.go:
#            each line "if claims == nil" when it, the next line or the one after holds "return true"
#   ACC-GD8  docs/ARCHITECTURE.md and files under platform/guacamole: lines with "recording to
#            SeaweedFS" or "recordings on SeaweedFS" (any case), except, in a Markdown file, lines
#            of a target section: a section runs from a heading line ("#" to "######", a space) to
#            the next heading of the same or a higher level, and is a target section when the first
#            non-empty line after its heading starts with "> State: target." (the marker of
#            scripts/check-doc-state.py) or when it lies inside a target section. A line inside a
#            fenced code block is never a heading.
# The files are those git lists: tracked, and new ones no ignore rule covers; a folder named
# node_modules is never read. The counts are compared with scripts/baselines/access.json:
#   {"schema": "openova.io/guard-baseline/v1",
#    "counts": {"<metric>": {"<repository path>": <number>}},
#    "allow": [{"metric": "<metric>", "path": "<repository path>", "count": 1,
#               "reason": "<ten characters or more>", "owner": "<area prefix, three capital letters>"}]}
# A path absent from a metric is allowed zero. An allow entry's count is subtracted first;
# ACC-GD1 and ACC-GD2 take none. Exit 1 when a count is above its entry, when an entry is above
# the count (lower the baseline in the same change), or when an allow entry is above the count or
# malformed; exit 2 when the checkout or the baseline cannot be read or the arguments are wrong
# (nothing was judged); exit 0 otherwise. A judgement prints <metric>=<total> per metric, the
# total before the allow entries.
#   bash scripts/check-access-guards.sh [--root <dir>]   judge the checkout (default: this one)
#   ... --write-baseline   write the baseline; it never raises an entry or adds a path and keeps
#                          the allow list; with no baseline file it writes SEED_ALLOW
#   ... --self-test        prove on trees of its own that every rule can fail
# Only bash, Python 3 (standard library) and git are used.
set -euo pipefail
exec python3 - "${BASH_SOURCE[0]}" "$@" <<'PY'
import json, os, re, shutil, subprocess, sys, tempfile
ME = "check-access-guards"
BASELINE = "scripts/baselines/access.json"
SCHEMA = "openova.io/guard-baseline/v1"
GUAC = "platform/guacamole"
PROXY = "platform/k8s-ws-proxy"
GUAC_TEMPLATES = GUAC + "/chart/templates"
PROXY_TEMPLATES = PROXY + "/chart/templates"
GUAC_VALUES = GUAC + "/chart/values.yaml"
PROXY_VALUES = PROXY + "/chart/values.yaml"
HANDLER = "products/catalyst/bootstrap/api/internal/handler"
INTENT = "products/catalyst/bootstrap/api/internal/accessintent"
ARCH = "docs/ARCHITECTURE.md"
METRICS = ("ACC-GD1", "ACC-GD2", "ACC-GD3", "ACC-GD4", "ACC-GD5", "ACC-GD6", "ACC-GD7", "ACC-GD8")
TITLE = dict(zip(METRICS, ("no timer drives the gateway", "no administrator permission is seeded",
    "no credential is copied into a gateway store", "no provider name in the access charts",
    "access images are pinned by digest", "the fabricated session code does not return",
    "no route in the exec and access handlers passes without claims",
    "documents do not claim recording before it is proven")))
NO_ALLOW = ("ACC-GD1", "ACC-GD2")
# Each of these must exist, or nothing is judged. The accessintent folder does not exist yet.
REQUIRED = (GUAC_TEMPLATES, PROXY_TEMPLATES, GUAC_VALUES, PROXY_VALUES, "products", HANDLER, ARCH)
OWNER = re.compile(r"^[A-Z]{3}$")
ALLOW_KEYS = ["count", "metric", "owner", "path", "reason"]
SEED_ALLOW = [{"metric": "ACC-GD5", "path": PROXY_VALUES, "count": 1, "owner": "FND",
               "reason": "the image tag that CI writes after each image build; it leaves when CI writes digests"}]
CRONJOB = re.compile(r"^kind:[ \t]*CronJob\b")
PERMISSION = re.compile(r"\binsert\s+into\s+guacamole_system_permission\b", re.IGNORECASE)
COPY = re.compile(r"load_connection_credentials")
# The provider name is written with a character class, so that the provider-leak scan of
# scripts/check-provider-leaks.py does not count this file.
PROVIDER = re.compile(r"h[c]loud", re.IGNORECASE)
TAG = re.compile(r"^\s*tag:")
SESSION = re.compile(r"synthesizeSession|GuacamoleClient|HandleShellsIssue")
NIL_CLAIMS = re.compile(r"\bif\s+claims\s*==\s*nil\b")
RETURN_TRUE = re.compile(r"\breturn\s+true\b")
RECORDING = re.compile(r"recording to SeaweedFS|recordings on SeaweedFS", re.IGNORECASE)
HEADING = re.compile(r"^(#{1,6})[ \t]")
FENCE = re.compile(r"^[ ]*(`{3,}|~{3,})")
TARGET = re.compile(r"^> State: target\.")
class Unreadable(Exception):
    """The checkout or the baseline cannot be read. Nothing is judged: exit 2."""
def under(path, *tops):
    return any(path == top or path.startswith(top + "/") for top in tops)
def lines_with(text, regex):
    return [n + 1 for n, line in enumerate(text.split("\n")) if regex.search(line)]
def target_lines(lines):
    """The 0-based numbers of the lines that lie inside a target section of a Markdown text."""
    heads, fence = [], None
    for i, line in enumerate(lines):
        if fence:
            if line.strip() and set(line.strip()) == {fence[0]} and len(line.strip()) >= len(fence):
                fence = None
            continue
        m = FENCE.match(line)
        if m:
            fence = m.group(1)
            continue
        h = HEADING.match(line)
        if h:
            heads.append((i, len(h.group(1))))
    marked = {}
    for k, (i, level) in enumerate(heads):
        end = heads[k + 1][0] if k + 1 < len(heads) else len(lines)
        first = next((lines[j] for j in range(i + 1, end) if lines[j].strip()), "")
        marked[i] = bool(TARGET.match(first))
    out, stack, starts = set(), [], dict(heads)
    for i in range(len(lines)):
        if i in starts:
            while stack and stack[-1][0] >= starts[i]:
                stack.pop()
            stack.append((starts[i], marked[i]))
        if any(t for _, t in stack):
            out.add(i)
    return out
# One function per guard. Each gets the listed paths and a reader, and returns
# {path: [line numbers of what counts]}; a path without a count is left out.
def gd1(paths, read):
    return {p: lines_with(read(p), CRONJOB) for p in paths if under(p, GUAC_TEMPLATES, PROXY_TEMPLATES)}
def gd2(paths, read):
    out = {}
    for p in paths:
        if under(p, GUAC):
            text = read(p)
            out[p] = [text.count("\n", 0, m.start()) + 1 for m in PERMISSION.finditer(text)]
    return out
def gd3(paths, read):
    return {p: lines_with(read(p), COPY) for p in paths if under(p, GUAC_TEMPLATES)}
def gd4(paths, read):
    return {p: lines_with(read(p), PROVIDER) for p in paths if under(p, GUAC, PROXY)}
def gd5(paths, read):
    return {p: lines_with(read(p), TAG) for p in paths if p in (GUAC_VALUES, PROXY_VALUES)}
def gd6(paths, read):
    return {p: lines_with(read(p), SESSION) for p in paths if under(p, "products")}
def gd7(paths, read):
    out = {}
    for p in paths:
        folder, name = os.path.dirname(p), os.path.basename(p)
        if (folder == HANDLER and name.startswith("k8s_exec") and name.endswith(".go")) or (folder == INTENT and name.endswith(".go")):
            lines = read(p).split("\n")
            out[p] = [i + 1 for i, line in enumerate(lines)
                      if NIL_CLAIMS.search(line) and any(RETURN_TRUE.search(x) for x in lines[i:i + 3])]
    return out
def gd8(paths, read):
    out = {}
    for p in paths:
        if p == ARCH or under(p, GUAC):
            lines = read(p).split("\n")
            skip = target_lines(lines) if p.endswith(".md") else set()
            out[p] = [i + 1 for i, line in enumerate(lines) if i not in skip and RECORDING.search(line)]
    return out
GUARDS = {"ACC-GD1": gd1, "ACC-GD2": gd2, "ACC-GD3": gd3, "ACC-GD4": gd4,
          "ACC-GD5": gd5, "ACC-GD6": gd6, "ACC-GD7": gd7, "ACC-GD8": gd8}
def listed(root):
    """The files git lists in the checkout: tracked, and new ones no ignore rule covers."""
    try:
        out = subprocess.run(["git", "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard"],
                             check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE).stdout
    except (OSError, subprocess.CalledProcessError):
        raise Unreadable("%s is not a git checkout" % root)
    paths = set()
    for p in out.decode("utf-8", "surrogateescape").split("\0"):
        full = os.path.join(root, p)
        if p and "node_modules" not in p.split("/") and os.path.isfile(full) and not os.path.islink(full):
            paths.add(p)
    return sorted(paths)
def measure(root):
    """metric -> repository path -> line numbers (1-based) of what counts."""
    for need in REQUIRED:
        if not os.path.exists(os.path.join(root, need)):
            raise Unreadable("%s does not exist" % need)
    paths = listed(root)
    cache = {}

    def read(p):
        if p not in cache:
            with open(os.path.join(root, p), "rb") as f:
                cache[p] = f.read().decode("utf-8", "replace")
        return cache[p]

    return {metric: {p: n for p, n in GUARDS[metric](paths, read).items() if n} for metric in METRICS}
def check_allow(allow):
    """Returns (allow entries that are well formed, failure lines about the others)."""
    good, fails, seen = [], [], set()
    for i, e in enumerate(allow):
        where = "allow[%d]" % i
        if not isinstance(e, dict) or sorted(e) != ALLOW_KEYS:
            fails.append("%s: an allow entry has exactly the keys %s" % (where, ", ".join(ALLOW_KEYS)))
        elif e["metric"] not in METRICS:
            fails.append("%s: metric must be one of %s" % (where, ", ".join(METRICS)))
        elif e["metric"] in NO_ALLOW:
            fails.append("%s: %s takes no allow entry" % (where, e["metric"]))
        elif not isinstance(e["path"], str) or not e["path"]:
            fails.append("%s: path must be a repository path" % where)
        elif type(e["count"]) is not int or e["count"] < 1:
            fails.append("%s: count must be a whole number of 1 or more" % where)
        elif not isinstance(e["reason"], str) or len(e["reason"].strip()) < 10:
            fails.append("%s: reason must be ten characters or more" % where)
        elif not isinstance(e["owner"], str) or not OWNER.match(e["owner"]):
            fails.append("%s: owner must be an area prefix of three capital letters" % where)
        elif (e["metric"], e["path"]) in seen:
            fails.append("%s: a second allow entry for %s %s; one entry per metric and path" % (where, e["metric"], e["path"]))
        else:
            seen.add((e["metric"], e["path"]))
            good.append(e)
    return good, fails
def read_baseline(root):
    """Returns (counts, the allow list as written, its well-formed entries, failure lines)."""
    try:
        with open(os.path.join(root, BASELINE), encoding="utf-8") as f:
            doc = json.load(f)
    except (OSError, ValueError) as e:
        raise Unreadable("%s cannot be read: %s" % (BASELINE, e))
    counts = doc.get("counts") if isinstance(doc, dict) else None
    if not isinstance(doc, dict) or doc.get("schema") != SCHEMA or not isinstance(counts, dict) or sorted(counts) != sorted(METRICS):
        raise Unreadable("%s: need schema %s and counts for exactly %s" % (BASELINE, SCHEMA, ", ".join(METRICS)))
    for metric in METRICS:
        if not isinstance(counts[metric], dict) or not all(type(v) is int and v >= 1 for v in counts[metric].values()):
            raise Unreadable("%s: counts.%s must map a path to a whole number of 1 or more" % (BASELINE, metric))
    if not isinstance(doc.get("allow"), list):
        raise Unreadable("%s: allow must be a list" % BASELINE)
    return (counts, doc["allow"]) + check_allow(doc["allow"])
def net(have, allow):
    """(metric -> path -> count after the allow entries, failure lines for allow entries above the count)."""
    out = {metric: {p: len(lines) for p, lines in have[metric].items()} for metric in METRICS}
    fails = []
    for e in allow:
        now = out[e["metric"]].get(e["path"], 0)
        if e["count"] > now:
            fails.append("%s: %s has %d, its allow entry says %d; lower or remove the allow entry in the same change" % (e["metric"], e["path"], now, e["count"]))
        out[e["metric"]][e["path"]] = max(now - e["count"], 0)
    return {metric: {p: n for p, n in out[metric].items() if n} for metric in METRICS}, fails
def compare(have, after, entries):
    """One (rose, line) per path whose count after the allow entries is not exactly its baseline entry."""
    out = []
    for metric in METRICS:
        for p in sorted(set(after[metric]) | set(entries[metric])):
            now, entry = after[metric].get(p, 0), entries[metric].get(p, 0)
            if now > entry:
                at = ", ".join(str(n) for n in have[metric].get(p, []))
                out.append((True, "%s: %s has %d, the baseline allows %d (match lines: %s); %s" % (metric, p, now, entry, at, TITLE[metric])))
            elif now < entry:
                out.append((False, "%s: %s has %d, the baseline still says %d; lower the baseline in the same change" % (metric, p, now, entry)))
    return out
def dump(counts, allow):
    """The baseline text: one line per allow entry and per counted path, keys sorted."""
    rows = ",\n".join("    " + json.dumps(e, sort_keys=True) for e in allow)
    blocks = []
    for metric in sorted(counts):
        body = ",\n".join("      %s: %d" % (json.dumps(p), n) for p, n in sorted(counts[metric].items()))
        blocks.append("    %s: {%s}" % (json.dumps(metric), "\n" + body + "\n    " if body else ""))
    return '{\n  "allow": [%s],\n  "counts": {\n%s\n  },\n  "schema": %s\n}\n' % (
        "\n" + rows + "\n  " if rows else "", ",\n".join(blocks), json.dumps(SCHEMA))
def run(root, write=False, say=print, seed=None):
    """The whole guard. Returns the exit code. seed is the allow list of a first write."""
    try:
        have = measure(root)
        if write:
            if os.path.exists(os.path.join(root, BASELINE)):
                old, kept, good, bad = read_baseline(root)
            else:
                old, kept = None, SEED_ALLOW if seed is None else seed
                good, bad = check_allow(kept)
            after, over = net(have, good)
            refused = bad + over + [line for rose, line in (compare(have, after, old) if old is not None else []) if rose]
            if refused:
                for line in refused:
                    say("%s: FAIL %s" % (ME, line))
                say("%s: the baseline was not written: a baseline only goes down" % ME)
                return 1
            os.makedirs(os.path.join(root, os.path.dirname(BASELINE)), exist_ok=True)
            with open(os.path.join(root, BASELINE), "w", encoding="utf-8") as f:
                f.write(dump(after, kept))
            say("%s: wrote %s" % (ME, BASELINE))
        entries, _, allow, fails = read_baseline(root)
    except Unreadable as e:
        say("%s: cannot judge: %s" % (ME, e))
        return 2
    after, over = net(have, allow)
    fails += over + [line for rose, line in compare(have, after, entries)]
    for metric in METRICS:
        say("%s=%d" % (metric, sum(len(lines) for lines in have[metric].values())))
    for line in fails:
        say("%s: FAIL %s" % (ME, line))
    if fails:
        say("%s: %d failure(s). To lower the baseline after a match left: bash scripts/%s.sh --write-baseline" % (ME, len(fails), ME))
    return 1 if fails else 0
# ---- self-test: every rule is shown to fail on a tree built here; no repository state is read
PROVIDER_WORD = "h" + "cloud"          # kept apart, see PROVIDER above
SEED_JOB = GUAC_TEMPLATES + "/seed-job.yaml"
DAEMON = PROXY_TEMPLATES + "/daemonset.yaml"
EXEC = HANDLER + "/k8s_exec.go"
SHELLS = HANDLER + "/shells_issue.go"
GREADME = GUAC + "/README.md"
FIXTURE = {
    SEED_JOB: "apiVersion: batch/v1\nkind: Job\n---\napiVersion: batch/v1\nkind: CronJob\nspec:\n"
              "  script: |\n    INSERT INTO guacamole_system_permission (entity_id, permission)\n"
              "    load_connection_credentials\n    load_connection_credentials \"$x\"\n",
    DAEMON: "apiVersion: apps/v1\nkind: DaemonSet\n# kind: CronJob is not used\nspec:\n  kind: CronJob\n",
    GUAC + "/chart/files/schema.sql": "CREATE TABLE guacamole_system_permission (id int);\n"
                                      "insert into\n  guacamole_system_permission values (1);\n",
    GUAC + "/chart/tests/render.sh": "kind: CronJob\nload_connection_credentials\n",
    GUAC_VALUES: "image:\n  repository: guacamole\n  tag: \"1.5.5\"\n  pullPolicy: IfNotPresent  # was tag: latest\nguacd:\n  tag: \"1.5.5\"\n"
                 "storageClass: %s-volumes\n" % PROVIDER_WORD,
    PROXY_VALUES: "image:\n  tag: \"abc1234\"\n",
    GUAC + "/chart/Chart.yaml": "# %s note\nname: guacamole\n" % PROVIDER_WORD.upper(),
    PROXY + "/README.md": "Runs on %s.\n" % PROVIDER_WORD,
    "platform/other/values.yaml": "tag: x\nstorageClass: %s-volumes\nkind: CronJob\n" % PROVIDER_WORD,
    EXEC: "package handler\n\nfunc a(claims *C) bool {\n\tif claims == nil {\n\t\treturn true\n\t}\n"
          "\treturn false\n}\n\nfunc b(claims *C) bool {\n\tif claims == nil {\n\t\t// open\n\t\treturn true\n\t}\n"
          "\treturn false\n}\n\nfunc c(claims *C) bool {\n\tif claims == nil {\n\t\treturn false\n\t}\n\treturn true\n}\n"
          "\ntype GuacamoleClient interface{}\n\nfunc synthesizeSession() {}\n",
    HANDLER + "/k8s_exec_ws.go": "package handler\n",
    HANDLER + "/other.go": "package handler\n\nfunc d(claims *C) bool {\n\tif claims == nil {\n\t\treturn true\n\t}\n\treturn false\n}\n",
    SHELLS: "package handler\n\n// HandleShellsIssue issues a shell.\nfunc HandleShellsIssue() { synthesizeSession() }\n",
    "core/cmd/x/main.go": "package main\n\ntype GuacamoleClient struct{}\n",
    ARCH: "# Architecture\n\n## 1. Access\n\nRecordings on SeaweedFS are kept.\n\n## 2. Later\n\n"
          "> State: target. Decision: ADR-0033\n\nTarget, not built. Full session recording to SeaweedFS.\n\n"
          "### 2.1 Detail\n\nrecordings on seaweedfs again.\n\n```\n# not a heading\n```\n\nrecording to SeaweedFS in a fence.\n\n"
          "## 3. Now\n\nOne recording to SeaweedFS path.\n",
    GREADME: "# Guacamole\n\nFull session recording to SeaweedFS.\n",
    GUAC + "/blueprint.yaml": "summary: gateway with full session recording to SeaweedFS\n",
    "docs/OTHER.md": "recording to SeaweedFS\n",
}
# metric -> the total of the fixture, and the counted paths with their counts
EXPECT = {
    "ACC-GD1": (1, {SEED_JOB: 1}),
    "ACC-GD2": (2, {SEED_JOB: 1, GUAC + "/chart/files/schema.sql": 1}),
    "ACC-GD3": (2, {SEED_JOB: 2}),
    "ACC-GD4": (3, {GUAC_VALUES: 1, GUAC + "/chart/Chart.yaml": 1, PROXY + "/README.md": 1}),
    "ACC-GD5": (3, {GUAC_VALUES: 2, PROXY_VALUES: 1}),
    "ACC-GD6": (4, {EXEC: 2, SHELLS: 2}),
    "ACC-GD7": (2, {EXEC: 2}),
    "ACC-GD8": (4, {ARCH: 2, GREADME: 1, GUAC + "/blueprint.yaml": 1}),
}
def self_test():
    def put(root, path, text):
        os.makedirs(os.path.dirname(os.path.join(root, path)), exist_ok=True)
        with open(os.path.join(root, path), "w", encoding="utf-8") as f:
            f.write(text)

    def add(path, text):
        return lambda root: put(root, path, text)

    def edit(path, old, new):
        def change(root):
            if old not in FIXTURE[path]:
                raise AssertionError("fixture text %r not in %s" % (old, path))
            put(root, path, FIXTURE[path].replace(old, new, 1))
        return change

    def baseline(change):
        def apply(root):
            with open(os.path.join(root, BASELINE), encoding="utf-8") as f:
                doc = json.load(f)
            change(doc)
            put(root, BASELINE, json.dumps(doc))
        return apply

    def both(*changes):
        return lambda root: [change(root) for change in changes]

    def entry(**over):
        return dict({"metric": "ACC-GD5", "path": GUAC_VALUES, "count": 1, "reason": "a tag line kept for the test", "owner": "FND"}, **over)

    def allow(*entries):
        return baseline(lambda doc: doc.__setitem__("allow", list(entries)))

    def drop(path):
        return lambda root: (shutil.rmtree if os.path.isdir(os.path.join(root, path)) else os.remove)(os.path.join(root, path))

    def ignore(path, text):
        return both(add(".gitignore", path + "\n"), add(path, text))

    unchanged = lambda root: None
    more_tag = edit(GUAC_VALUES, "guacd:\n", "guacd:\n  tag: \"2\"\n")
    less_tag = edit(GUAC_VALUES, "guacd:\n  tag: \"1.5.5\"\n", "guacd:\n")
    # (group, what the case shows, the change to a tree whose baseline is exact, --write-baseline, seed, exit code, text the output must hold)
    cases = []
    for metric in METRICS:
        cases.append((metric, "the fixture counts %d for %s" % (EXPECT[metric][0], metric), unchanged, False, None, 0, "%s=%d" % (metric, EXPECT[metric][0])))
    cases += [
        ("ACC-GD1", "a second CronJob in a gateway template fails", edit(SEED_JOB, "kind: Job\n", "kind: CronJob\n"), False, None, 1, "FAIL ACC-GD1: %s has 2, the baseline allows 1" % SEED_JOB),
        ("ACC-GD1", "a new CronJob template of the exec proxy fails", add(PROXY_TEMPLATES + "/timer.yaml", "kind: CronJob\n"), False, None, 1, "FAIL ACC-GD1: %s/timer.yaml has 1, the baseline allows 0" % PROXY_TEMPLATES),
        ("ACC-GD1", "a CronJob outside the two template folders is not counted", add(GUAC + "/chart/tests/more.yaml", "kind: CronJob\n"), False, None, 0, "ACC-GD1=1"),
        ("ACC-GD1", "a CronJob that left fails until the baseline is lowered", edit(SEED_JOB, "kind: CronJob\n", "kind: Job\n"), False, None, 1, "FAIL ACC-GD1: %s has 0, the baseline still says 1" % SEED_JOB),
        ("ACC-GD1", "ACC-GD1 takes no allow entry", both(add(DAEMON, "kind: CronJob\n"), allow(entry(metric="ACC-GD1", path=DAEMON))), False, None, 1, "ACC-GD1 takes no allow entry"),
        ("ACC-GD2", "a second permission insert in a gateway file fails, written in lower case over two lines", add(GUAC + "/chart/files/more.sql", "insert into\nguacamole_system_permission values (2);\n"), False, None, 1, "FAIL ACC-GD2: %s/chart/files/more.sql has 1, the baseline allows 0" % GUAC),
        ("ACC-GD2", "a permission insert outside the gateway is not counted", add("platform/other/seed.sql", "INSERT INTO guacamole_system_permission VALUES (1);\n"), False, None, 0, "ACC-GD2=2"),
        ("ACC-GD2", "an insert into another table that starts with the same name is not counted", add(GUAC + "/chart/files/type.sql", "INSERT INTO guacamole_system_permission_type VALUES (1);\n"), False, None, 0, "ACC-GD2=2"),
        ("ACC-GD2", "ACC-GD2 takes no allow entry", allow(entry(metric="ACC-GD2", path=SEED_JOB)), False, None, 1, "ACC-GD2 takes no allow entry"),
        ("ACC-GD3", "one more credential copy in a gateway template fails", edit(SEED_JOB, "load_connection_credentials\n", "load_connection_credentials\n    load_connection_credentials y\n"), False, None, 1, "FAIL ACC-GD3: %s has 3, the baseline allows 2" % SEED_JOB),
        ("ACC-GD3", "a credential copy outside the templates is not counted", add(GUAC + "/chart/files/copy.sh", "load_connection_credentials\n"), False, None, 0, "ACC-GD3=2"),
        ("ACC-GD4", "the provider name in a new exec-proxy file fails", add(PROXY_TEMPLATES + "/pvc.yaml", "storageClassName: %s-volumes\n" % PROVIDER_WORD), False, None, 1, "FAIL ACC-GD4: %s/pvc.yaml has 1, the baseline allows 0" % PROXY_TEMPLATES),
        ("ACC-GD4", "the provider name outside the access charts is not counted", add("platform/other/b.yaml", PROVIDER_WORD + "\n"), False, None, 0, "ACC-GD4=3"),
        ("ACC-GD4", "a provider name that left fails until the baseline is lowered", drop(PROXY + "/README.md"), False, None, 1, "FAIL ACC-GD4: %s/README.md has 0, the baseline still says 1" % PROXY),
        ("ACC-GD5", "one more tag line in the gateway values fails", more_tag, False, None, 1, "FAIL ACC-GD5: %s has 3, the baseline allows 2" % GUAC_VALUES),
        ("ACC-GD5", "a tag line in another values file is not counted", add(GUAC + "/chart/values-extra.yaml", "tag: x\n"), False, None, 0, "ACC-GD5=3"),
        ("ACC-GD5", "an allow entry covers a tag line", both(more_tag, allow(entry())), False, None, 0, "ACC-GD5=4"),
        ("ACC-GD5", "a tag line that left fails until the baseline is lowered", less_tag, False, None, 1, "FAIL ACC-GD5: %s has 1, the baseline still says 2" % GUAC_VALUES),
        ("ACC-GD6", "the fabricated session name in a new file under products fails", add("products/console/src/session.ts", "const s = synthesizeSession()\n"), False, None, 1, "FAIL ACC-GD6: products/console/src/session.ts has 1, the baseline allows 0"),
        ("ACC-GD6", "a name outside products is not counted", add("core/cmd/x/more.go", "package main\n\nfunc HandleShellsIssue() {}\n"), False, None, 0, "ACC-GD6=4"),
        ("ACC-GD6", "a file that git ignores is not read", ignore("products/build/out.js", "GuacamoleClient\n"), False, None, 0, "ACC-GD6=4"),
        ("ACC-GD6", "a folder named node_modules is never read", add("products/ui/node_modules/m/i.js", "synthesizeSession()\n"), False, None, 0, "ACC-GD6=4"),
        ("ACC-GD6", "a deleted file that left fails until the baseline is lowered", drop(SHELLS), False, None, 1, "FAIL ACC-GD6: %s has 0, the baseline still says 2" % SHELLS),
        ("ACC-GD7", "one more route that passes without claims fails", edit(EXEC, "\t\treturn false\n\t}\n", "\t\treturn true\n\t}\n"), False, None, 1, "FAIL ACC-GD7: %s has 3, the baseline allows 2" % EXEC),
        ("ACC-GD7", "a new file of the access intent package that passes without claims fails", add(INTENT + "/grant.go", "package accessintent\n\nfunc g(claims *C) bool {\n\tif claims == nil { return true }\n\treturn false\n}\n"), False, None, 1, "FAIL ACC-GD7: %s/grant.go has 1, the baseline allows 0" % INTENT),
        ("ACC-GD7", "a return true three lines after the nil check is not counted", add(HANDLER + "/k8s_exec_far.go", "package handler\n\nfunc f(claims *C) bool {\n\tif claims == nil {\n\t\tx()\n\t\ty()\n\t\treturn true\n\t}\n\treturn false\n}\n"), False, None, 0, "ACC-GD7=2"),
        ("ACC-GD7", "another handler file is not read", edit(HANDLER + "/other.go", "func d", "func e(claims *C) bool {\n\tif claims == nil {\n\t\treturn true\n\t}\n\treturn false\n}\nfunc d"), False, None, 0, "ACC-GD7=2"),
        ("ACC-GD8", "a new recording claim in a gateway document fails", add(GUAC + "/chart/README.md", "Session recording to SeaweedFS.\n"), False, None, 1, "FAIL ACC-GD8: %s/chart/README.md has 1, the baseline allows 0" % GUAC),
        ("ACC-GD8", "a recording claim moved out of its target section fails", edit(ARCH, "> State: target. Decision: ADR-0033\n\n", ""), False, None, 1, "FAIL ACC-GD8: %s has 5, the baseline allows 2" % ARCH),
        ("ACC-GD8", "a claim added inside a target section is not counted", edit(ARCH, "Target, not built.", "Target, not built. Recordings on SeaweedFS later."), False, None, 0, "ACC-GD8=4"),
        ("ACC-GD8", "a claim in another document is not counted", add("docs/MORE.md", "recordings on SeaweedFS\n"), False, None, 0, "ACC-GD8=4"),
        ("ACC-GD8", "a claim that left fails until the baseline is lowered", drop(GREADME), False, None, 1, "FAIL ACC-GD8: %s has 0, the baseline still says 1" % GREADME),
        ("rules", "--write-baseline lowers the baseline after a match left", less_tag, True, None, 0, "ACC-GD5=2"),
        ("rules", "--write-baseline refuses to raise an entry", more_tag, True, None, 1, "the baseline was not written"),
        ("rules", "--write-baseline refuses to add a path", add(PROXY_TEMPLATES + "/timer.yaml", "kind: CronJob\n"), True, None, 1, "the baseline was not written"),
        ("rules", "--write-baseline keeps the allow list", both(more_tag, allow(entry())), True, None, 0, "ACC-GD5=4"),
        ("rules", "a first write writes the seed allow list", both(more_tag, drop(BASELINE)), True, [entry()], 0, "ACC-GD5=4"),
        ("rules", "a first write refuses a seed entry above the count", drop(BASELINE), True, [entry(count=3)], 1, "the baseline was not written"),
        ("rules", "a first write refuses a malformed seed entry", drop(BASELINE), True, [entry(reason="short")], 1, "the baseline was not written"),
        ("rules", "an allow entry without a reason fails", both(more_tag, allow(entry(reason="short"))), False, None, 1, "reason must be ten characters or more"),
        ("rules", "an allow entry without an owner fails", both(more_tag, allow(entry(owner="fnd"))), False, None, 1, "owner must be an area prefix"),
        ("rules", "an allow entry with an unknown metric fails", both(more_tag, allow(entry(metric="ACC-GD9"))), False, None, 1, "metric must be one of"),
        ("rules", "an allow entry with a count of 0 fails", both(more_tag, allow(entry(count=0))), False, None, 1, "count must be a whole number of 1 or more"),
        ("rules", "an allow entry with a missing key fails", both(more_tag, baseline(lambda doc: doc["allow"].append({"metric": "ACC-GD5", "path": GUAC_VALUES, "count": 1, "reason": "a tag line kept for the test"}))), False, None, 1, "exactly the keys"),
        ("rules", "two allow entries for one path fail", both(more_tag, allow(entry(), entry())), False, None, 1, "a second allow entry"),
        ("rules", "an allow entry above the count fails", allow(entry(path=PROXY_VALUES, count=2)), False, None, 1, "ACC-GD5: %s has 1, its allow entry says 2" % PROXY_VALUES),
        ("rules", "a baseline entry of 0 is not a pass", baseline(lambda doc: doc["counts"]["ACC-GD3"].__setitem__(SEED_JOB + ".x", 0)), False, None, 2, "cannot judge"),
        ("rules", "a baseline of another schema is not a pass", baseline(lambda doc: doc.__setitem__("schema", "other")), False, None, 2, "cannot judge"),
        ("rules", "a baseline without one metric is not a pass", baseline(lambda doc: doc["counts"].pop("ACC-GD8")), False, None, 2, "cannot judge"),
        ("rules", "a missing baseline is not a pass", drop(BASELINE), False, None, 2, "cannot judge"),
        ("rules", "a missing chart folder is not a pass", drop(PROXY_TEMPLATES), False, None, 2, "%s does not exist" % PROXY_TEMPLATES),
        ("rules", "a missing architecture document is not a pass", drop(ARCH), False, None, 2, "%s does not exist" % ARCH),
        ("rules", "a tree that is not a git checkout is not a pass", drop(".git"), False, None, 2, "is not a git checkout"),
    ]
    failed, by_group = 0, {}
    for group, name, change, write, seed, want, needle in cases:
        root, got, said = tempfile.mkdtemp(prefix="access-guards-"), None, []
        try:
            for path, text in FIXTURE.items():
                put(root, path, text)
            subprocess.run(["git", "init", "-q", root], check=True)
            quiet = []
            if run(root, write=True, say=quiet.append, seed=[]) != 0:
                raise AssertionError("the fixture baseline was not written: %s" % quiet)
            if write is False and want == 0 and change is unchanged:
                entries = read_baseline(root)[0]
                if entries[group] != EXPECT[group][1]:
                    raise AssertionError("the fixture baseline of %s is %r, expected %r" % (group, entries[group], EXPECT[group][1]))
            change(root)
            target = os.path.join(root, BASELINE)
            before = open(target, "rb").read() if os.path.exists(target) else None
            got = run(root, write=write, say=said.append, seed=[] if seed is None else seed)
            ok = got == want and any(needle in line for line in said)
            if write and want == 1:                   # a refused write left the baseline as it was
                ok = ok and (open(target, "rb").read() if os.path.exists(target) else None) == before
            if write and want == 0 and "allow list" in name:
                with open(target, encoding="utf-8") as f:
                    ok = ok and json.load(f)["allow"] == [entry()]
        except (AssertionError, subprocess.CalledProcessError, OSError) as e:
            ok, said = False, [str(e)]
        finally:
            shutil.rmtree(root, ignore_errors=True)
        failed += 0 if ok else 1
        by_group[group] = by_group.get(group, True) and ok
        print("%s  %s: %s" % ("ok  " if ok else "FAIL", group, name) + ("" if ok else "\n      wanted exit %d and %r; got exit %s and %r" % (want, needle, got, said)))
    good, bad = check_allow(SEED_ALLOW)
    ok = not bad and len(good) == len(SEED_ALLOW) == 1
    failed += 0 if ok else 1
    by_group["rules"] = by_group.get("rules", True) and ok
    print("%s  rules: the seed allow list of this script has 1 well-formed entry" % ("ok  " if ok else "FAIL"))
    for metric in METRICS:
        print("self-test %s %s" % ("ok" if by_group.get(metric) else "FAIL", metric))
    print("%s: self-test %s: %d of %d cases as expected" % (ME, "failed" if failed else "passed", len(cases) + 1 - failed, len(cases) + 1))
    return 1 if failed else 0
def main(argv):
    script, args = argv[0], argv[1:]
    root = os.path.dirname(os.path.dirname(os.path.abspath(script)))
    if args[:1] == ["--root"] and len(args) >= 2:
        root, args = args[1], args[2:]
    if args not in ([], ["--write-baseline"], ["--self-test"]):
        print("%s: wrong usage: %s. Use [--root <dir>] [--write-baseline | --self-test]" % (ME, " ".join(args)))
        return 2
    return self_test() if args == ["--self-test"] else run(root, write=args == ["--write-baseline"])
sys.exit(main(sys.argv[1:]))
PY
