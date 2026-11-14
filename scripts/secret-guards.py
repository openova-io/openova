#!/usr/bin/env python3
"""The rules of the secret guards G1 to G7 and G9 (SEC-090).

Each guard is a script scripts/check-<guard>.sh that runs this file with its
guard name. The scripts are what CI runs and what the meta-guard
scripts/check-guards-are-wired.sh sees; this file holds the rules once, so the
self-test runs the same code as the real scan.

Every guard scans the whole tree of tracked files (git ls-files), never only a
diff. A guard with an allow-list compares what it finds with
scripts/secret-guard-baselines/check-<guard>.txt, one "path:count" line per
file:

  - a path whose count rises fails;
  - a path that is not in the allow-list fails;
  - a path whose count falls passes, and the guard prints the line to change
    or delete, so that the allow-list follows the tree down.

The allow-list may only shrink. A guard without an allow-list fails on any
finding.

Usage:
  secret-guards.py <guard>                 scan the tree and compare
  secret-guards.py <guard> --self-test     prove that each rule can fail
  secret-guards.py <guard> --count <path>  print the number of findings under <path>
  secret-guards.py <guard> --list <path>   print "path:count" for each file under <path>

<path> is a file or folder relative to the repository root; "." is the whole
tree. Exit codes: 0 pass, 1 a finding outside the allow-list (or a failed
self-test), 2 usage or setup error.
"""

import os
import re
import shutil
import subprocess
import sys
import tempfile

BASELINE_DIR = "scripts/secret-guard-baselines"
# git runs with -C <root>; a GIT_* variable of the caller (GIT_DIR, GIT_INDEX_FILE)
# would point it at another repository, so none is passed on.
GIT_ENV = dict((k, v) for k, v in os.environ.items() if not k.startswith("GIT_"))

# ---------------------------------------------------------------- the tree


def tracked_files(root):
    """Every tracked file of the repository at root, sorted."""
    r = subprocess.run(["git", "-C", root, "ls-files", "-z"], stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                       env=GIT_ENV)
    if r.returncode != 0:
        raise SetupError("git ls-files failed in %s: %s" % (root, r.stderr.decode("utf-8", "replace").strip()))
    return sorted(p for p in r.stdout.decode("utf-8", "replace").split("\0") if p)


def read_lines(root, path):
    """The lines of a text file, or None for a binary or unreadable file."""
    try:
        with open(os.path.join(root, path), "rb") as f:
            data = f.read()
    except OSError:
        return None
    if b"\0" in data:
        return None
    return data.decode("utf-8", "replace").split("\n")


def chart_roots(files, own_only):
    """Folders under platform/ or products/ that hold a Chart.yaml. A chart
    inside a charts/ folder is a vendored subchart; own_only leaves it out."""
    roots = []
    for p in files:
        if p.endswith("/Chart.yaml") and p.split("/", 1)[0] in ("platform", "products"):
            root = p[: -len("/Chart.yaml")]
            if own_only and "/charts/" in root + "/":
                continue
            roots.append(root + "/templates/")
    return roots


def template_files(files, own_only):
    roots = chart_roots(files, own_only)
    return [p for p in files if any(p.startswith(r) for r in roots)]


def under(files, prefixes):
    return [p for p in files if any(p == x or p.startswith(x + "/") for x in prefixes)]


def is_comment(line):
    s = line.lstrip()
    return s.startswith("#") or s.startswith("//")


# ---------------------------------------------------------------- the rules
#
# A rule takes the lines of one file and returns the 1-based numbers of the
# lines it finds. A guard is a list of (file set, rule) pairs; a file's count
# is the number of distinct lines its rules find.

SECRET_KIND = re.compile(r"^\s*kind:\s*Secret\s*$")
RANDOM_SECRET = re.compile(
    r"randAlphaNum|randAlpha |randNumeric|randAscii|genCA|genPrivateKey"
    r"|genSelfSignedCert|genSignedCert|htpasswd|derivePassword")
CREDENTIAL_WORD = re.compile(r"secret|password|token|seed|salt", re.IGNORECASE)
SECRET_WRITE_CALL = re.compile(r"Secrets\([^)]*\)\.(Create|Update|Patch|Apply)\(")
SECRET_KIND_IN_GO = re.compile(r"^\s*kind: Secret\s*$")
REFLECT_NAMESPACES = re.compile(r'reflection-auto-namespaces"?\s*[:=]\s*(.*)$')
EXTERNAL_OR_PUSH_KIND = re.compile(r"^\s*kind:\s*(ExternalSecret|PushSecret)\s*$")
EXTERNAL_KIND = re.compile(r"^\s*kind:\s*ExternalSecret\s*$")
PROVIDER_CLASS_KIND = re.compile(r"^\s*kind:\s*SecretProviderClass\s*$")
SECRET_OBJECTS_KEY = re.compile(r"^\s*secretObjects:")
LEGACY_STORE_NAME = re.compile(r"^\s*(-\s+)?name:.*vault-region1")
MOUNTED_STORE_NAME = re.compile(r"^\s*(-\s+)?name:.*openbao-mounted")
# Written in parts, so that this file does not hold the words it looks for.
UNSEAL_WORDS = re.compile("openbao-unseal-" + "keys" + "|" + "generate-" + "root")


def lines_matching(pattern, skip_comments=False):
    def rule(lines):
        return [i + 1 for i, l in enumerate(lines)
                if pattern.search(l) and not (skip_comments and is_comment(l))]
    return rule


def in_files_of_kind(kind, pattern):
    """Lines matching pattern, only in a file that also has a line matching kind."""
    def rule(lines):
        if not any(kind.search(l) for l in lines):
            return []
        return [i + 1 for i, l in enumerate(lines) if pattern.search(l) and not is_comment(l)]
    return rule


def derived_credentials(lines):
    """G4: a line with sha256sum (not a checksum/ annotation) whose own line or
    one of the four lines above it names a secret, password, token, seed or salt."""
    found = []
    for i, l in enumerate(lines):
        if "sha256sum" in l and "checksum/" not in l:
            if any(CREDENTIAL_WORD.search(x) for x in lines[max(0, i - 4): i + 1]):
                found.append(i + 1)
    return found


def wide_reflection(lines):
    """G6 (a): a reflection-auto-namespaces value that is empty or holds a
    pattern (.* or [a-z]), so that the reflector copies into every namespace."""
    found = []
    for i, l in enumerate(lines):
        if is_comment(l):
            continue
        m = REFLECT_NAMESPACES.search(l)
        if not m:
            continue
        v = m.group(1).strip().rstrip(",").strip()
        if v in ("", '""', "''") or ".*" in v or "[a-z]" in v:
            found.append(i + 1)
    return found


def own_templates(files):
    return template_files(files, own_only=True)


def all_templates(files):
    return template_files(files, own_only=False)


def go_sources(files):
    return [p for p in files if p.endswith(".go") and not p.endswith("_test.go")]


GUARDS = {
    "no-secret-values-in-git": {
        "title": "G1: no Secret manifest under clusters/",
        "allow_list": True,
        "rules": [(lambda f: under(f, ["clusters"]), lines_matching(SECRET_KIND))],
    },
    "no-chart-random-secrets": {
        "title": "G2: no new generated secret in an own chart template",
        "allow_list": True,
        "rules": [(own_templates, lines_matching(RANDOM_SECRET))],
    },
    "no-plain-secret-manifests": {
        "title": "G3: no new Secret manifest in an own chart template",
        "allow_list": True,
        "rules": [(own_templates, lines_matching(SECRET_KIND))],
    },
    "no-derived-credentials": {
        "title": "G4: no new credential derived by sha256sum in an own chart template",
        "allow_list": True,
        "rules": [(own_templates, derived_credentials)],
    },
    "no-secret-writers": {
        "title": "G5: no new Secret writer in Go code",
        "allow_list": True,
        "rules": [(go_sources, lines_matching(SECRET_WRITE_CALL)),
                  (go_sources, lines_matching(SECRET_KIND_IN_GO))],
    },
    "reflector-scope": {
        "title": "G6: no new wide reflector scope and no new use of the legacy cluster-wide store",
        "allow_list": True,
        "rules": [(lambda f: under(f, ["infra", "clusters"]) + own_templates(f) + go_sources(f), wide_reflection),
                  (own_templates, in_files_of_kind(EXTERNAL_OR_PUSH_KIND, LEGACY_STORE_NAME))],
    },
    "openbao-no-unseal-secret": {
        "title": "G7: no new reference to the OpenBao unseal Secret or to root-token generation",
        "allow_list": True,
        "rules": [(lambda f: [p for p in under(f, ["platform", "products", "clusters", "infra", "core",
                                                    "scripts", "tests", ".github"])
                              if not p.startswith(BASELINE_DIR + "/")],
                   lines_matching(UNSEAL_WORDS))],
    },
    "secret-delivery-lint": {
        "title": "G9: no SecretProviderClass with secretObjects; no ExternalSecret on the store openbao-mounted",
        "allow_list": False,
        "rules": [(all_templates, in_files_of_kind(PROVIDER_CLASS_KIND, SECRET_OBJECTS_KEY)),
                  (all_templates, in_files_of_kind(EXTERNAL_KIND, MOUNTED_STORE_NAME))],
    },
}


class SetupError(Exception):
    pass


def scan(guard, root):
    """{path: [line numbers]} for every file with at least one finding."""
    files = tracked_files(root)
    found = {}
    for select, rule in GUARDS[guard]["rules"]:
        for p in sorted(set(select(files))):
            lines = read_lines(root, p)
            if lines is None:
                continue
            hits = rule(lines)
            if hits:
                found.setdefault(p, set()).update(hits)
    return {p: sorted(v) for p, v in sorted(found.items())}


# ---------------------------------------------------------------- allow-list

def baseline_path(guard):
    return "%s/check-%s.txt" % (BASELINE_DIR, guard)


def read_allow_list(guard, root):
    path = os.path.join(root, baseline_path(guard))
    if not os.path.isfile(path):
        raise SetupError("the allow-list %s does not exist" % baseline_path(guard))
    allowed = {}
    with open(path, encoding="utf-8") as f:
        for n, line in enumerate(f.read().split("\n"), 1):
            if line == "":
                continue
            m = re.match(r"^([^:\s]+):([1-9][0-9]*)$", line)
            if not m or m.group(1) in allowed:
                raise SetupError("%s line %d is not a new 'path:count' line: %r" % (baseline_path(guard), n, line))
            allowed[m.group(1)] = int(m.group(2))
    return allowed


def check(guard, root, say):
    """Scan root and compare with the allow-list. Returns the exit code."""
    g = GUARDS[guard]
    found = scan(guard, root)
    allowed = read_allow_list(guard, root) if g["allow_list"] else {}
    failures = 0
    for p, hits in found.items():
        have = allowed.get(p)
        if have is None:
            say("FAIL %s: %s has %d finding(s) and is not in the allow-list" % (guard, p, len(hits)))
        elif len(hits) > have:
            say("FAIL %s: %s has %d finding(s), the allow-list allows %d" % (guard, p, len(hits), have))
        else:
            continue
        failures += 1
        lines = read_lines(root, p) or []
        for n in hits:
            text = lines[n - 1].strip() if n <= len(lines) else ""
            say("  %s:%d: %s" % (p, n, text[:160]))
    for p, have in sorted(allowed.items()):
        now = len(found.get(p, []))
        if now == 0:
            say("LOWER %s: delete the line '%s:%d'" % (baseline_path(guard), p, have))
        elif now < have:
            say("LOWER %s: replace the line '%s:%d' with '%s:%d'" % (baseline_path(guard), p, have, p, now))
    if failures:
        if g["allow_list"]:
            say("FAIL %s: %d file(s) above the allow-list %s. It may only shrink: remove the new "
                "finding(s); never raise or add a line." % (g["title"], failures, baseline_path(guard)))
        else:
            say("FAIL %s: %d file(s) with a finding. This guard has no allow-list." % (g["title"], failures))
        return 1
    if g["allow_list"]:
        say("OK %s: %d finding(s) in %d file(s), none above the allow-list." % (
            g["title"], sum(len(v) for v in found.values()), len(found)))
    else:
        say("OK %s: no finding." % g["title"])
    return 0


def select_path(found, path):
    path = path.strip("/")
    if path in ("", "."):
        return found
    return {p: v for p, v in found.items() if p == path or p.startswith(path + "/")}


# ---------------------------------------------------------------- self-test
#
# Each guard has a clean fixture tree with one allowed finding (file "allowed")
# and the look-alikes the guard must not count, then one failing case per rule.
# Every case builds a throw-away git repository and runs check() on it: the
# same code as the real scan. No cluster, no network.

CHART = "name: fixture\nversion: 0.1.0\n"
UNSEAL = "openbao-unseal-" + "keys"
ROOT_GEN = "generate-" + "root"

FIXTURES = {
    "no-secret-values-in-git": {
        "allowed": ("clusters/a/old.yaml", "apiVersion: v1\nkind: Secret\nmetadata:\n  name: old\n"),
        "extra_site": "---\nkind: Secret\n",
        "controls": {
            "clusters/a/store.yaml": "kind: SecretStore\n",
            "clusters/a/es.yaml": "kind: ExternalSecret\n",
            "platform/x/chart/Chart.yaml": CHART,
            "platform/x/chart/templates/s.yaml": "kind: Secret\n",
        },
        "bad": [("a Secret manifest under clusters/", {"clusters/b/new.yaml": "apiVersion: v1\nkind:  Secret \n"})],
    },
    "no-chart-random-secrets": {
        "allowed": ("platform/a/chart/templates/s.yaml", "pw: {{ randAlphaNum 16 }}\n"),
        "extra_site": "pw2: {{ randAlphaNum 16 }}\n",
        "controls": {
            "platform/a/chart/Chart.yaml": CHART,
            "platform/a/chart/values.yaml": "x: genCA randAlphaNum\n",
            "platform/a/chart/charts/up/Chart.yaml": CHART,
            "platform/a/chart/charts/up/templates/v.yaml": "pw: {{ randAlphaNum 16 }}\n",
            "platform/a/chart/templates/cm.yaml": "kind: ConfigMap\ndata:\n  alpha: random\n",
            "clusters/a/x.yaml": "pw: {{ randAlphaNum 16 }}\n",
        },
        "bad": [("%s in an own chart template" % t, {"products/b/chart/Chart.yaml": CHART,
                                                     "products/b/chart/templates/n.yaml": "x: {{ %s 8 }}\n" % t.strip()
                                                     if t != "randAlpha " else "x: {{ randAlpha 8 }}\n"})
                for t in ["randAlphaNum", "randAlpha ", "randNumeric", "randAscii", "genCA", "genPrivateKey",
                          "genSelfSignedCert", "genSignedCert", "htpasswd", "derivePassword"]],
    },
    "no-plain-secret-manifests": {
        "allowed": ("platform/a/chart/templates/s.yaml", "apiVersion: v1\nkind: Secret\n"),
        "extra_site": "---\nkind: Secret\n",
        "controls": {
            "platform/a/chart/Chart.yaml": CHART,
            "platform/a/chart/templates/spc.yaml": "kind: SecretProviderClass\n---\nkind: ExternalSecret\n",
            "platform/a/chart/charts/up/Chart.yaml": CHART,
            "platform/a/chart/charts/up/templates/s.yaml": "kind: Secret\n",
            "clusters/a/s.yaml": "kind: Secret\n",
        },
        "bad": [("a Secret manifest in an own chart template",
                 {"products/b/chart/Chart.yaml": CHART, "products/b/chart/templates/n.yaml": "  kind: Secret\n"})],
    },
    "no-derived-credentials": {
        "allowed": ("platform/a/chart/templates/_h.tpl", '{{- $seed := printf "%s" .Release.Name -}}\n{{- $seed | sha256sum -}}\n'),
        "extra_site": "password: {{ .Values.p | sha256sum }}\n",
        "controls": {
            "platform/a/chart/Chart.yaml": CHART,
            "platform/a/chart/templates/d.yaml": 'checksum/secret: {{ include "x" . | sha256sum }}\n',
            "platform/a/chart/templates/far.yaml": "token: x\n1\n2\n3\n4\nv: {{ .Values.v | sha256sum }}\n",
        },
        "bad": [("sha256sum on its own credential line",
                 {"products/b/chart/Chart.yaml": CHART,
                  "products/b/chart/templates/n.yaml": "password: {{ .Values.p | sha256sum }}\n"}),
                ("sha256sum four lines below a credential word",
                 {"products/b/chart/Chart.yaml": CHART,
                  "products/b/chart/templates/m.yaml": "salt: x\n1\n2\n3\nv: {{ .Values.v | sha256sum }}\n"})],
    },
    "no-secret-writers": {
        "allowed": ("core/a/w.go", "package a\n\nfunc f() { c.CoreV1().Secrets(ns).Create(ctx, s, o) }\n"),
        "extra_site": "func g() { c.CoreV1().Secrets(ns).Patch(ctx, n, t, b, o) }\n",
        "controls": {
            "core/a/w_test.go": "package a\n\nfunc t() { c.CoreV1().Secrets(ns).Create(ctx, s, o) }\n",
            "core/a/r.go": "package a\n\nfunc r() { c.CoreV1().Secrets(ns).Get(ctx, n, o) }\n",
            "core/a/doc.md": "kind: Secret\n",
        },
        "bad": [("a Secret write call", {"core/b/n.go": 'package b\n\nfunc u() { k.Secrets("x").Update(ctx, s, o) }\n'}),
                ("a Secret manifest inside Go source", {"core/b/m.go": "package b\n\nconst m = `\nkind: Secret\n`\n"})],
    },
    "reflector-scope": {
        "allowed": ("infra/p/t.tftpl", '  reflector.v1.k8s.emberstack.com/reflection-auto-namespaces: ""\n'),
        "extra_site": '  reflector.v1.k8s.emberstack.com/reflection-auto-namespaces: "org-.*"\n',
        "controls": {
            "clusters/c/ok.yaml": '    reflector.v1.k8s.emberstack.com/reflection-auto-namespaces: "a,b"\n',
            "clusters/c/comment.yaml": '# reflector.v1.k8s.emberstack.com/reflection-auto-namespaces: ""\n',
            "platform/a/chart/Chart.yaml": CHART,
            "platform/a/chart/templates/t.yaml": (
                '    reflector.v1.k8s.emberstack.com/reflection-auto-namespaces: {{ join "," . | quote }}\n'),
            "platform/a/chart/templates/peer.yaml": "kind: ExternalSecret\nspec:\n  secretStoreRef:\n    name: vault-peer\n",
            "platform/a/chart/templates/cm.yaml": "kind: ConfigMap\nmetadata:\n  name: vault-region1\n",
            "docs/x.md": 'reflection-auto-namespaces: ""\n',
        },
        "bad": [("an empty reflector scope", {"clusters/d/n.yaml": '    reflector.v1.k8s.emberstack.com/reflection-auto-namespaces: ""\n'}),
                ("a [a-z] reflector scope", {"platform/a/chart/templates/r.yaml":
                                             '    reflector.v1.k8s.emberstack.com/reflection-auto-namespaces: "[a-z][a-z0-9-]*"\n'}),
                ("an ExternalSecret on the legacy store", {"platform/a/chart/templates/es.yaml":
                                                           'kind: ExternalSecret\nspec:\n  secretStoreRef:\n'
                                                           '    name: {{ .Values.store | default "vault-region1" }}\n'}),
                ("a PushSecret on the legacy store", {"products/b/chart/Chart.yaml": CHART,
                                                      "products/b/chart/templates/ps.yaml":
                                                      "kind: PushSecret\nspec:\n  secretStoreRefs:\n    - name: vault-region1\n"})],
    },
    "openbao-no-unseal-secret": {
        "allowed": ("platform/o/chart/templates/j.yaml", "name: %s\n" % UNSEAL),
        "extra_site": "path: sys/%s/attempt\n" % ROOT_GEN,
        "controls": {
            "docs/x.md": "%s\n" % UNSEAL,
            BASELINE_DIR + "/other.txt": "platform/o/files/%s.sh:3\n" % ROOT_GEN,
        },
        "bad": [("a root-token generation call in a workflow", {".github/workflows/w.yaml": "run: bao operator %s\n" % ROOT_GEN}),
                ("the unseal Secret in a test", {"tests/t.sh": "kubectl get secret %s\n" % UNSEAL})],
    },
    "secret-delivery-lint": {
        "allowed": None,
        "controls": {
            "platform/a/chart/Chart.yaml": CHART,
            "platform/a/chart/templates/spc.yaml": "kind: SecretProviderClass\nspec:\n  provider: openbao\n",
            "platform/a/chart/templates/es.yaml": "kind: ExternalSecret\nspec:\n  secretStoreRef:\n    name: openbao-ns\n",
            "platform/a/chart/templates/cm.yaml": "kind: ConfigMap\ndata:\n  secretObjects: none\n",
        },
        "bad": [("a SecretProviderClass with secretObjects", {"platform/a/chart/templates/sync.yaml":
                                                              "kind: SecretProviderClass\nspec:\n  secretObjects:\n    - secretName: x\n"}),
                ("an ExternalSecret on the store openbao-mounted", {"platform/a/chart/charts/up/Chart.yaml": CHART,
                                                                    "platform/a/chart/charts/up/templates/es.yaml":
                                                                    "kind: ExternalSecret\nspec:\n  secretStoreRef:\n"
                                                                    "    name: openbao-mounted\n"})],
    },
}


def make_tree(files):
    root = tempfile.mkdtemp(prefix="secret-guards-")
    for p, content in files.items():
        full = os.path.join(root, p)
        os.makedirs(os.path.dirname(full), exist_ok=True)
        with open(full, "w", encoding="utf-8") as f:
            f.write(content)
    for args in (["init", "-q"], ["add", "-A"]):
        subprocess.run(["git", "-C", root] + args, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                       env=GIT_ENV)
    return root


def run_case(guard, files):
    """check() on a fixture tree: (exit code, printed lines)."""
    out = []
    root = make_tree(files)
    try:
        code = check(guard, root, out.append)
    finally:
        shutil.rmtree(root, ignore_errors=True)
    return code, out


def self_test(guard):
    fx = FIXTURES[guard]
    fails = []

    def expect(label, ok, detail=""):
        print("  %s %s" % ("ok  " if ok else "FAIL", label))
        if not ok:
            fails.append(label)
            if detail:
                print("       " + detail.replace("\n", "\n       "))

    base = dict(fx["controls"])
    allowed = fx["allowed"]
    if allowed:
        path, content = allowed
        base[path] = content
        base[baseline_path(guard)] = "%s:1\n" % path
        root = make_tree(base)
        try:
            got = scan(guard, root)
        finally:
            shutil.rmtree(root, ignore_errors=True)
        expect("the allowed file is found once and no look-alike is counted",
               list(got) == [path] and len(got[path]) == 1, "found: %r" % got)
        folder = os.path.dirname(path)
        expect("--count and --list select by path",
               len(select_path(got, folder).get(path, [])) == 1 and select_path(got, folder + "x") == {}
               and select_path(got, ".") == got, "found: %r" % got)
        code, out = run_case(guard, base)
        expect("the tree at its allow-list passes", code == 0 and not any(l.startswith("LOWER") for l in out),
               "\n".join(out))
        rise = dict(base)
        rise[path] = content + fx["extra_site"]
        code, out = run_case(guard, rise)
        expect("a rise of an allowed file fails", code == 1, "\n".join(out))
        fall = dict(base)
        del fall[path]
        code, out = run_case(guard, fall)
        expect("a removed finding passes and asks to delete its line",
               code == 0 and "LOWER %s: delete the line '%s:1'" % (baseline_path(guard), path) in out, "\n".join(out))
        broken = dict(base)
        broken[baseline_path(guard)] = "%s:x\n" % path
        code, out = run_case_setup(guard, broken)
        expect("a malformed allow-list line is a setup error", code == 2, "\n".join(out))
    else:
        code, out = run_case(guard, base)
        expect("the clean tree passes", code == 0, "\n".join(out))
    for label, extra in fx["bad"]:
        tree = dict(base)
        tree.update(extra)
        code, out = run_case(guard, tree)
        expect("fails on %s" % label, code == 1, "\n".join(out))
    if fails:
        print("SELF-TEST FAILED: %s (%d case(s))" % (guard, len(fails)))
        return 1
    print("OK %s: self-test passed; fixtures only, no cluster was contacted." % guard)
    return 0


def run_case_setup(guard, files):
    """Like run_case, but a SetupError becomes exit code 2, as in main()."""
    try:
        return run_case(guard, files)
    except SetupError as e:
        return 2, [str(e)]


# ---------------------------------------------------------------- main

def main(argv):
    if not argv or argv[0] not in GUARDS:
        print("usage: secret-guards.py <guard> [--self-test | --count <path> | --list <path>]; guards: %s"
              % ", ".join(sorted(GUARDS)), file=sys.stderr)
        return 2
    guard, rest = argv[0], argv[1:]
    root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    try:
        if rest == ["--self-test"]:
            return self_test(guard)
        if len(rest) == 2 and rest[0] in ("--count", "--list"):
            found = select_path(scan(guard, root), rest[1])
            if rest[0] == "--count":
                print(sum(len(v) for v in found.values()))
            else:
                for p, hits in found.items():
                    print("%s:%d" % (p, len(hits)))
            return 0
        if rest:
            print("unknown arguments: %s" % " ".join(rest), file=sys.stderr)
            return 2
        return check(guard, root, print)
    except SetupError as e:
        print("ERROR %s: %s" % (guard, e), file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
