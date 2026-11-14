#!/usr/bin/env python3
"""check-products.py - the product guard: the AI-hub charts keep the rules G-P1, G-P2 and G-P3.

Each rule is one function, one list of folders it scans, and one fixture. It counts, per file,
the lines that break the rule:

  G-P1  hard zero. Every file under the chart templates of the model servers
        (platform/vllm, platform/bge): a line that declares the kind HTTPRoute, Ingress,
        Gateway or GRPCRoute. A model server is reached through the platform's model
        gateway only, never on a route of its own.
  G-P2  hard zero. Every file under platform/vllm, platform/bge and platform/librechat:
        a line that names a model-provider host (api.openai.com, api.anthropic.com,
        generativelanguage.googleapis.com), in any letter case. A model is reached
        through the platform's model gateway, never directly.
  G-P3  ratchet. Every file under the chart templates of platform/vllm, platform/bge and
        platform/librechat: a line that contains secretKeyRef. A credential is mounted as a
        file, never read from a Secret object into an environment variable. The count of
        each file may only fall; a new file starts at zero.

The counts are compared with scripts/baselines/products.json:

  {"schema": "openova.io/guard-baseline/v1",
   "counts": {"G-P1": {}, "G-P2": {}, "G-P3": {"<repository path>": <number>}},
   "allow": []}

  counts  per rule, per file, the number of lines allowed. A file absent from the map is
          allowed zero. Every number is 1 or more. A hard-zero rule has no entry at all.
  allow   always the empty list. This guard takes no allow entry: every count falls to
          zero, or a zero holds.

The rule, for every rule and every file:

  the count is above its entry                 exit 1
  the entry is above the count                 exit 1  (lower the baseline in the same change)
  a hard-zero rule has a baseline entry        exit 1
  the allow list is not empty                  exit 1

When it judges, it prints one line <rule>=<total> per rule. A baseline only goes down: once
the baseline file exists, --write-baseline refuses to raise an entry or to add a file, and it
never writes an entry for a hard-zero rule.

A rule over folders that do not exist cannot fail. So a scanned folder that is missing, or
that holds no file, is never a pass: nothing is judged and the exit code is 2.

A later rule is added as one more function with its folders and its fixture, registered in
RULES, and one more key in the counts of the baseline. No other rule changes.

Usage:
  python3 scripts/check-products.py                    compare; print <rule>=<total> per rule
  python3 scripts/check-products.py --write-baseline   write the baseline from the tree
  python3 scripts/check-products.py --self-test        prove that every rule can fail
  --root <dir>  measure another checkout (default: the one this script lies in)

Exit codes: 0 no count rose and the baseline is exact; 1 a rule is broken;
2 a scanned folder or the baseline cannot be read, so nothing was judged.
It needs Python 3 and nothing else.
"""
import argparse
import json
import os
import re
import shutil
import sys
import tempfile

ME = "check-products"
BASELINE = "scripts/baselines/products.json"
SCHEMA = "openova.io/guard-baseline/v1"
TOP_KEYS = ["allow", "counts", "schema"]


class Unreadable(Exception):
    """A scanned folder or the baseline cannot be read. Nothing is judged: exit 2."""


class Rule(object):
    """One rule: its name, the folders it scans, how it counts, and the fixture of its self-test.

    measure(root) returns repository path -> line numbers (1-based) of the lines that break
    the rule; files without such a line are left out.
    fixture["base"]: files that make every folder of the rule exist and break no rule.
    fixture["fail"]: (what, files) cases; each must make the guard fail on this rule.
    fixture["pass"]: (what, files) cases; each must leave the guard green.
    """

    def __init__(self, name, folders, measure, hard_zero, why, fixture):
        self.name, self.folders, self.measure = name, folders, measure
        self.hard_zero, self.why, self.fixture = hard_zero, why, fixture


def files_under(root, folder):
    """Repository paths of every file under folder, every sub-directory included, sorted."""
    top = os.path.join(root, folder)
    if not os.path.isdir(top):
        raise Unreadable("%s is not a directory" % folder)
    out = []
    for here, dirs, names in os.walk(top):
        dirs.sort()
        for name in names:
            if os.path.isfile(os.path.join(here, name)):
                out.append(os.path.relpath(os.path.join(here, name), root).replace(os.sep, "/"))
    if not out:
        raise Unreadable("%s holds no file" % folder)
    return sorted(out)


def scan(root, folders, pattern):
    """path -> line numbers of the lines of the files under folders that match pattern."""
    found = {}
    for folder in folders:
        for path in files_under(root, folder):
            with open(os.path.join(root, path), "rb") as f:
                lines = f.read().split(b"\n")
            hits = [n + 1 for n, line in enumerate(lines) if pattern.search(line)]
            if hits:
                found[path] = hits
    return found


# ---- G-P1: a model server publishes no route of its own. Hard zero.

G_P1_FOLDERS = ("platform/vllm/chart/templates", "platform/bge/chart/templates")
G_P1_RE = re.compile(rb"^\s*(?:-\s+)?kind:\s*[\"']?(HTTPRoute|Ingress|Gateway|GRPCRoute)[\"']?\s*(?:#.*)?$")


def g_p1(root):
    return scan(root, G_P1_FOLDERS, G_P1_RE)


G_P1 = Rule("G-P1", G_P1_FOLDERS, g_p1, True,
            "a model server is reached through the model gateway, never on a route of its own",
            {"base": {"platform/vllm/chart/templates/service.yaml": "kind: Service\n",
                      "platform/bge/chart/templates/service.yaml": "kind: Service\n"},
             "fail": [("an HTTPRoute in a model server's templates",
                       {"platform/vllm/chart/templates/route.yaml": "apiVersion: gateway.networking.k8s.io/v1\nkind: HTTPRoute\n"}),
                      ("an Ingress in a sub-folder of the templates",
                       {"platform/bge/chart/templates/extra/ingress.yaml": "  kind: Ingress\n"}),
                      ("a quoted Gateway kind",
                       {"platform/bge/chart/templates/gateway.yaml": "kind: \"Gateway\"  # edge\n"}),
                      ("a GRPCRoute as a list item",
                       {"platform/vllm/chart/templates/list.yaml": "items:\n  - kind: GRPCRoute\n"})],
             "pass": [("an Ingress of the chat front end is not a model server's",
                       {"platform/librechat/chart/templates/ingress.yaml": "kind: Ingress\n"}),
                      ("a route kind in values.yaml is not a template",
                       {"platform/vllm/chart/values.yaml": "kind: HTTPRoute\n"}),
                      ("a GatewayClass is not a route",
                       {"platform/vllm/chart/templates/class.yaml": "kind: GatewayClass\n"}),
                      ("a route kind in a comment declares nothing",
                       {"platform/bge/chart/templates/notes.yaml": "# kind: Ingress\n"})]})


# ---- G-P2: no model-provider host name. Hard zero.

G_P2_FOLDERS = ("platform/vllm", "platform/bge", "platform/librechat")
G_P2_RE = re.compile(rb"api\.openai\.com|api\.anthropic\.com|generativelanguage\.googleapis\.com", re.IGNORECASE)


def g_p2(root):
    return scan(root, G_P2_FOLDERS, G_P2_RE)


G_P2 = Rule("G-P2", G_P2_FOLDERS, g_p2, True,
            "a model is reached through the model gateway, never at a provider's host",
            {"base": {"platform/vllm/README.md": "A model server.\n",
                      "platform/bge/README.md": "An embedding server.\n",
                      "platform/librechat/README.md": "A chat front end.\n"},
             "fail": [("an OpenAI host in a values file",
                       {"platform/librechat/chart/values.yaml": "baseURL: https://api.openai.com/v1\n"}),
                      ("an Anthropic host in a README",
                       {"platform/bge/README.md": "It calls api.anthropic.com.\n"}),
                      ("a Google host written in capitals in a template",
                       {"platform/vllm/chart/templates/configmap.yaml": "url: https://GenerativeLanguage.GoogleAPIs.com\n"})],
             "pass": [("the platform's own gateway host",
                       {"platform/librechat/chart/values.yaml": "baseURL: http://gateway.example.svc/v1\n"}),
                      ("a provider's name without its host",
                       {"platform/librechat/README.md": "It speaks the OpenAI-compatible interface.\n"}),
                      ("a provider host outside the scanned folders",
                       {"platform/other/README.md": "api.openai.com\n"})]})


# ---- G-P3: a chart reads no Secret object into an environment variable. Ratchet.

G_P3_FOLDERS = ("platform/vllm/chart/templates", "platform/bge/chart/templates", "platform/librechat/chart/templates")
G_P3_RE = re.compile(rb"secretKeyRef")


def g_p3(root):
    return scan(root, G_P3_FOLDERS, G_P3_RE)


G_P3 = Rule("G-P3", G_P3_FOLDERS, g_p3, False,
            "a credential is mounted as a file, so this count only falls",
            {"base": {"platform/vllm/chart/templates/deployment.yaml": "kind: Deployment\n",
                      "platform/bge/chart/templates/deployment.yaml": "kind: Deployment\n",
                      "platform/librechat/chart/templates/deployment.yaml": "kind: Deployment\n"},
             "fail": [("a secretKeyRef in a model server's template",
                       {"platform/vllm/chart/templates/deployment.yaml": "kind: Deployment\n              secretKeyRef:\n"}),
                      ("a secretKeyRef in a new template of the chat front end",
                       {"platform/librechat/chart/templates/extra/job.yaml": "# reads it via secretKeyRef\n"})],
             "pass": [("a secretKeyRef in values.yaml is not a template",
                       {"platform/bge/chart/values.yaml": "secretKeyRef: x\n"}),
                      ("a Secret mounted as a file is not a secretKeyRef",
                       {"platform/bge/chart/templates/deployment.yaml": "kind: Deployment\nvolumes:\n  - secret:\n      secretName: x\n"}),
                      ("a secretKeyRef in a chart outside the scan",
                       {"platform/other/chart/templates/deployment.yaml": "secretKeyRef:\n"})]})


RULES = [G_P1, G_P2, G_P3]
NAMES = [r.name for r in RULES]


def measure(root):
    """rule name -> repository path -> line numbers."""
    return {r.name: r.measure(root) for r in RULES}


def read_baseline(root):
    """Returns (counts, failure lines). Raises Unreadable when the file cannot be judged."""
    try:
        with open(os.path.join(root, BASELINE), encoding="utf-8") as f:
            doc = json.load(f)
    except (OSError, ValueError) as e:
        raise Unreadable("%s cannot be read: %s" % (BASELINE, e))
    if not isinstance(doc, dict) or sorted(doc) != TOP_KEYS:
        raise Unreadable("%s: need exactly the keys %s" % (BASELINE, ", ".join(TOP_KEYS)))
    counts = doc["counts"]
    if doc["schema"] != SCHEMA or not isinstance(counts, dict) or sorted(counts) != sorted(NAMES):
        raise Unreadable("%s: need schema %s and counts for exactly %s" % (BASELINE, SCHEMA, ", ".join(NAMES)))
    for name in NAMES:
        entries = counts[name]
        if not isinstance(entries, dict) or not all(isinstance(k, str) and k and type(v) is int and v >= 1
                                                    for k, v in entries.items()):
            raise Unreadable("%s: counts.%s must map a path to a whole number of 1 or more" % (BASELINE, name))
    if not isinstance(doc["allow"], list):
        raise Unreadable("%s: allow must be a list" % BASELINE)
    fails = []
    if doc["allow"]:
        fails.append("allow: this guard takes no allow entry; a count falls to zero, or a zero holds")
    for r in RULES:
        if r.hard_zero and counts[r.name]:
            fails.append("%s: is a hard zero; the baseline holds no entry for it (found: %s)"
                         % (r.name, ", ".join(sorted(counts[r.name]))))
    return counts, fails


def compare(have, counts):
    """One (rose, line) per rule and path whose count is not exactly its baseline entry."""
    out = []
    for r in RULES:
        for path in sorted(set(have[r.name]) | set(counts[r.name])):
            now, entry = len(have[r.name].get(path, [])), counts[r.name].get(path, 0)
            if now > entry:
                at = ", ".join(str(n) for n in have[r.name][path])
                out.append((True, "%s: %s has %d, the baseline allows %d (match lines: %s); %s"
                            % (r.name, path, now, entry, at, r.why)))
            elif now < entry:
                out.append((False, "%s: %s has %d, the baseline still says %d; lower the baseline in the same change"
                            % (r.name, path, now, entry)))
    return out


def run(root, write=False, say=print):
    """The whole guard. Returns the exit code."""
    try:
        have = measure(root)
        if write:
            target = os.path.join(root, BASELINE)
            rises = []
            for r in RULES:
                if r.hard_zero:
                    rises += ["%s: %s has %d; %s is a hard zero" % (r.name, p, len(n), r.name)
                              for p, n in sorted(have[r.name].items())]
            if os.path.exists(target):
                old, _ = read_baseline(root)
                rises += [line for rose, line in compare(have, old) if rose]
            if rises:
                for line in rises:
                    say("%s: FAIL %s" % (ME, line))
                say("%s: the baseline was not written: a baseline only goes down" % ME)
                return 1
            counts = {r.name: ({} if r.hard_zero else {p: len(n) for p, n in have[r.name].items()}) for r in RULES}
            os.makedirs(os.path.dirname(target), exist_ok=True)
            with open(target, "w", encoding="utf-8") as f:
                f.write(json.dumps({"schema": SCHEMA, "counts": counts, "allow": []}, indent=2, sort_keys=True) + "\n")
            say("%s: wrote %s" % (ME, BASELINE))
        counts, fails = read_baseline(root)
    except Unreadable as e:
        say("%s: cannot judge: %s" % (ME, e))
        return 2
    fails += [line for _, line in compare(have, counts)]
    for name in NAMES:
        say("%s=%d" % (name, sum(len(n) for n in have[name].values())))
    for line in fails:
        say("%s: FAIL %s" % (ME, line))
    if fails:
        say("%s: %d failure(s). To lower the baseline after a line left: python3 scripts/%s.py --write-baseline"
            % (ME, len(fails), ME))
    return 1 if fails else 0


# ---- self-test: every rule is shown to fail on a tree built here; no repository state is read

def self_test():
    p3 = G_P3.folders[0] + "/zz-generic.yaml"         # a ratchet file of G-P3 with two lines

    def put(root, path, text):
        os.makedirs(os.path.dirname(os.path.join(root, path)), exist_ok=True)
        with open(os.path.join(root, path), "w", encoding="utf-8") as f:
            f.write(text)

    def files(mapping):
        def apply(root):
            for path, text in mapping.items():
                put(root, path, text)
        return apply

    def baseline(change):
        def apply(root):
            with open(os.path.join(root, BASELINE), encoding="utf-8") as f:
                doc = json.load(f)
            change(doc)
            put(root, BASELINE, json.dumps(doc))
        return apply

    def gone(path):
        return lambda root: os.remove(os.path.join(root, path))

    two = "env:\n  - valueFrom:\n      secretKeyRef: a\n  - valueFrom:\n      secretKeyRef: b\n"
    one = "env:\n  - valueFrom:\n      secretKeyRef: a\n"
    three = two + "  - valueFrom:\n      secretKeyRef: c\n"
    route = files({G_P1.folders[0] + "/route.yaml": "kind: HTTPRoute\n"})
    # (what the case shows, the change to a tree whose baseline is exact, --write-baseline, exit code, text the output must hold)
    cases = [
        ("an unchanged tree passes and prints every rule", lambda root: None, False, 0, "G-P3=2"),
        ("the hard-zero rules print their total", lambda root: None, False, 0, "G-P1=0"),
    ]
    for r in RULES:
        for what, mapping in r.fixture["fail"]:
            cases.append(("%s fails: %s" % (r.name, what), files(mapping), False, 1,
                          "FAIL %s: %s has 1, the baseline allows 0" % (r.name, sorted(mapping)[0])))
        for what, mapping in r.fixture["pass"]:
            cases.append(("%s passes: %s" % (r.name, what), files(mapping), False, 0, "%s=" % r.name))
    cases += [
        ("one more line in a counted file fails", files({p3: three}), False, 1, "FAIL G-P3: %s has 3, the baseline allows 2" % p3),
        ("a line that left without a lower baseline fails", files({p3: one}), False, 1, "FAIL G-P3: %s has 1, the baseline still says 2" % p3),
        ("a file whose last line left fails until it leaves the baseline", gone(p3), False, 1, "FAIL G-P3: %s has 0, the baseline still says 2" % p3),
        ("--write-baseline lowers the baseline after a line left", files({p3: one}), True, 0, "G-P3=1"),
        ("--write-baseline refuses to raise an entry", files({p3: three}), True, 1, "the baseline was not written"),
        ("--write-baseline refuses to add a file", files({G_P3.folders[-1] + "/new.yaml": "secretKeyRef:\n"}), True, 1, "the baseline was not written"),
        ("--write-baseline refuses a line of a hard-zero rule", route, True, 1, "G-P1 is a hard zero"),
        ("a baseline entry for a hard-zero rule fails", baseline(lambda d: d["counts"]["G-P1"].__setitem__(G_P1.folders[0] + "/route.yaml", 1)), False, 1, "G-P1: is a hard zero"),
        ("an allow entry fails", baseline(lambda d: d.__setitem__("allow", [{"metric": "G-P3", "path": p3, "count": 2, "reason": "kept for a while", "owner": "PRD"}])), False, 1, "takes no allow entry"),
        ("a baseline entry of 0 is not a pass", baseline(lambda d: d["counts"]["G-P3"].__setitem__(p3, 0)), False, 2, "cannot judge"),
        ("a baseline of another schema is not a pass", baseline(lambda d: d.__setitem__("schema", "other")), False, 2, "cannot judge"),
        ("a baseline without a rule's counts is not a pass", baseline(lambda d: d["counts"].pop("G-P2")), False, 2, "counts for exactly"),
        ("a baseline with counts of an unknown rule is not a pass", baseline(lambda d: d["counts"].__setitem__("G-P9", {})), False, 2, "counts for exactly"),
        ("a baseline with an unknown key is not a pass", baseline(lambda d: d.__setitem__("extra", 1)), False, 2, "need exactly the keys"),
        ("a missing baseline is not a pass", gone(BASELINE), False, 2, "cannot judge"),
        ("a missing scanned folder is not a pass", lambda root: shutil.rmtree(os.path.join(root, "platform/bge")), False, 2, "platform/bge/chart/templates is not a directory"),
        ("a scanned folder without a file is not a pass",
         lambda root: [os.remove(os.path.join(root, G_P1.folders[1], n)) for n in os.listdir(os.path.join(root, G_P1.folders[1]))],
         False, 2, "%s holds no file" % G_P1.folders[1]),
    ]
    failed = 0
    for name, change, write, want, needle in cases:
        root = tempfile.mkdtemp(prefix="check-products-")
        got, said = None, []
        try:
            for r in RULES:
                files(r.fixture["base"])(root)
            put(root, p3, two)
            quiet = []
            if run(root, write=True, say=quiet.append) != 0:
                raise AssertionError("the fixture baseline was not written: %s" % quiet)
            change(root)
            target = os.path.join(root, BASELINE)
            before = open(target, "rb").read() if os.path.exists(target) else None
            got = run(root, write=write, say=said.append)
            ok = got == want and any(needle in line for line in said)
            if write and want == 1:                   # a refused write left the baseline as it was: the tree still fails
                ok = ok and open(target, "rb").read() == before and run(root, say=quiet.append) == 1
        except AssertionError as e:
            ok, said = False, [str(e)]
        finally:
            shutil.rmtree(root, ignore_errors=True)
        failed += 0 if ok else 1
        print("%s  %s" % ("ok  " if ok else "FAIL", name) + ("" if ok else "\n      wanted exit %d and %r; got exit %s and %r" % (want, needle, got, said)))
    print("%s: self-test %s: %d of %d cases as expected" % (ME, "failed" if failed else "passed", len(cases) - failed, len(cases)))
    return 1 if failed else 0


def main(argv=None):
    parser = argparse.ArgumentParser(description="The product guard: rules G-P1, G-P2 and G-P3.")
    parser.add_argument("--root", default=os.path.dirname(os.path.dirname(os.path.abspath(__file__))), help="the checkout to measure")
    parser.add_argument("--write-baseline", action="store_true", help="write %s from the tree; never raises an entry" % BASELINE)
    parser.add_argument("--self-test", action="store_true", help="prove that every rule can fail")
    args = parser.parse_args(argv)
    return self_test() if args.self_test else run(args.root, write=args.write_baseline)


if __name__ == "__main__":
    sys.exit(main())
