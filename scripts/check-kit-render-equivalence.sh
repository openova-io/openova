#!/usr/bin/env bash
# check-kit-render-equivalence.sh - the kit renders the same before and after a change.
#
# A Sovereign's Flux renders the kit (the trees under clusters/_template) and substitutes the
# values of the ConfigMap flux-system/sovereign-variables into it. A change that turns a fixed
# value of the kit into a substitution must leave every rendered object as it was. This
# script proves that, root by root, by rendering the kit of the working tree and the kit of a
# base commit with the same values and comparing the results.
#
# Roots. Every path that the boot template infra/providers/_shared/cloudinit-control-plane.tftpl
# gives to a Flux Kustomization as "path: ./clusters/_template/<root>", in every branch of its
# template conditions. A root is rendered by walking its kustomization.yaml: each listed file,
# each listed folder (through its own kustomization.yaml), each patch file, and the
# kustomization.yaml itself, so the text of an inline patch is compared too. A kustomization.yaml
# may hold only the fields apiVersion, kind, resources and patches; any other field, a missing
# file and a path outside clusters/_template cannot be judged.
#
# The model of Flux's substitution, per YAML document:
#   - a document whose annotation or label kustomize.toolkit.fluxcd.io/substitute is "disabled"
#     is taken as it is;
#   - any other document is parsed, written again as YAML with sorted keys, substituted, and
#     parsed again, so comments take no part and a quote of the source is not kept (a value
#     that a substitution makes "true" is a boolean, as in Flux's own post-build step);
#   - the forms are ${NAME}, ${NAME:-default}, ${NAME:=default} (the value when it is set and
#     not empty, else the default), ${NAME-default}, ${NAME=default} (the value when it is set,
#     else the default); a default may hold further ${...}; an unset name gives the empty text;
#     "$$" gives "$", so "$${NAME}" stays "${NAME}"; "$NAME" without braces is left as it is;
#     any other form after the name cannot be judged.
# The rendered root is the list of its documents as canonical JSON, each with its file; its
# hash is the SHA-256 of that list.
#
# Values. The file tests/fixtures/variables/today.env holds one line KEY=VALUE per key (blank
# lines and lines starting with # are skipped; the value is the rest of the line, as it is).
# Every root is rendered with two value sets:
#   full  every line of the file;
#   kit   only the keys listed in docs/contracts/c5-kit-keys.txt, which is what a Sovereign
#         carries when its variables hold the kit keys and no contract key.
#
# Usage (from anywhere in the checkout):
#   bash scripts/check-kit-render-equivalence.sh [--base <git-ref>]
#       compare the working tree with <git-ref>; the default is the merge base of HEAD and
#       origin/main. Prints one line per value set and root, then a verdict line.
#   bash scripts/check-kit-render-equivalence.sh --print [--kit <dir>]
#       print "<value set> <root> <sha256> <documents>" for each root of the kit in <dir>
#       (default: clusters/_template of the checkout)
#   bash scripts/check-kit-render-equivalence.sh --self-test
#       prove on a Git repository of its own that each rule can fail
#
# Needs bash, git, Python 3 and PyYAML.
# Exit codes: 0 every root renders the same with both value sets (with --print: every root
# rendered); 1 a root renders differently; 2 something cannot be judged, so nothing was judged.
set -euo pipefail
SELF="${BASH_SOURCE[0]}"
ROOT="$(cd "$(dirname "$SELF")/.." && pwd)"
exec python3 - "$ROOT" "$@" <<'PY'
import hashlib
import io
import json
import os
import re
import subprocess
import sys
import tarfile
import tempfile

ME = "check-kit-render-equivalence"
KIT = "clusters/_template"
TEMPLATE = "infra/providers/_shared/cloudinit-control-plane.tftpl"
FIXTURE = "tests/fixtures/variables/today.env"
KIT_KEYS = "docs/contracts/c5-kit-keys.txt"
DISABLED = "kustomize.toolkit.fluxcd.io/substitute"
KUSTOMIZATION_FIELDS = {"apiVersion", "kind", "resources", "patches"}
NAME = re.compile(r"[A-Za-z_][A-Za-z0-9_]*")
PATH_LINE = re.compile(r"^\s+path: \./clusters/_template/(.+?)\s*$")
ROOT_NAME = re.compile(r"[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*")
DIRECTIVE = re.compile(r"%\{~?\s*(if\b[^}]*?|else|endif)\s*~?\}")


class CannotJudge(Exception):
    pass


def yaml_module():
    try:
        import yaml
    except ImportError:
        raise CannotJudge("PyYAML is not installed (pip install pyyaml)")
    return yaml


# ---- roots

def expand(text):
    """Every string that the template text can give, one per choice of its if, else and endif branches."""
    tokens = []
    pos = 0
    for m in DIRECTIVE.finditer(text):
        tokens.append(("text", text[pos:m.start()]))
        word = m.group(1).split()[0]
        tokens.append((word, None))
        pos = m.end()
    tokens.append(("text", text[pos:]))

    def seq(i, stop):
        outs = [""]
        while i < len(tokens):
            kind, value = tokens[i]
            if kind in stop:
                return outs, i
            if kind == "text":
                outs = [o + value for o in outs]
                i += 1
            elif kind == "if":
                then, i = seq(i + 1, ("else", "endif"))
                other = [""]
                if tokens[i][0] == "else":
                    other, i = seq(i + 1, ("endif",))
                if i >= len(tokens) or tokens[i][0] != "endif":
                    raise CannotJudge("a template condition has no endif: %s" % text)
                i += 1
                outs = [o + b for o in outs for b in then + other]
            else:
                raise CannotJudge("a template condition is not balanced: %s" % text)
        if stop:
            raise CannotJudge("a template condition is not closed: %s" % text)
        return outs, i

    outs, _ = seq(0, ())
    return outs


def roots_of(template_path):
    try:
        lines = open(template_path, encoding="utf-8").read().split("\n")
    except OSError as e:
        raise CannotJudge("%s cannot be read: %s" % (template_path, e))
    found = []
    for line in lines:
        m = PATH_LINE.match(line)
        if m:
            for r in expand(m.group(1)):
                r = r.strip("/")
                if not ROOT_NAME.fullmatch(r) or ".." in r.split("/"):
                    raise CannotJudge("%s names the path %r, which is not a folder name" % (template_path, r))
                if r not in found:
                    found.append(r)
    if not found:
        raise CannotJudge("%s names no path ./clusters/_template/<root>" % template_path)
    return sorted(found)


# ---- values

def read_values(path, keys_path):
    try:
        text = open(path, encoding="utf-8").read()
    except OSError as e:
        raise CannotJudge("%s cannot be read: %s" % (path, e))
    full = {}
    for n, line in enumerate(text.split("\n"), 1):
        if not line.strip() or line.startswith("#"):
            continue
        key, eq, value = line.partition("=")
        if not eq or not NAME.fullmatch(key):
            raise CannotJudge("%s line %d is not KEY=VALUE" % (path, n))
        if key in full:
            raise CannotJudge("%s names %s twice" % (path, key))
        full[key] = value
    try:
        kit_keys = {k.strip() for k in open(keys_path, encoding="utf-8") if k.strip()}
    except OSError as e:
        raise CannotJudge("%s cannot be read: %s" % (keys_path, e))
    return [("full", full), ("kit", {k: v for k, v in full.items() if k in kit_keys})]


# ---- substitution

def closing(text, i):
    """Index of the } that closes the ${ whose content starts at i; nested ${...} are skipped."""
    depth = 0
    while i < len(text):
        if text.startswith("$$", i):
            i += 2
            continue
        if text.startswith("${", i):
            depth += 1
            i += 2
            continue
        if text[i] == "}":
            if depth == 0:
                return i
            depth -= 1
        i += 1
    raise CannotJudge("a ${ is not closed")


def evaluate(expr, values):
    m = NAME.match(expr)
    if not m:
        raise CannotJudge("unsupported substitution ${%s}" % expr)
    name, rest = m.group(0), expr[m.end():]
    if rest == "":
        return values.get(name, "")
    for op in (":-", ":=", "-", "="):
        if rest.startswith(op):
            default = rest[len(op):]
            value = values.get(name)
            if op in (":-", ":="):
                use = value is not None and value != ""
            else:
                use = value is not None
            return value if use else substitute(default, values)
    raise CannotJudge("unsupported substitution ${%s}" % expr)


def substitute(text, values):
    out = []
    i = 0
    while i < len(text):
        if text.startswith("$$", i):
            out.append("$")
            i += 2
        elif text.startswith("${", i):
            end = closing(text, i + 2)
            out.append(evaluate(text[i + 2:end], values))
            i = end + 1
        else:
            out.append(text[i])
            i += 1
    return "".join(out)


# ---- documents

def dumper():
    yaml = yaml_module()

    class Emitter(yaml.emitter.Emitter):
        def choose_scalar_style(self):
            style = super().choose_scalar_style()
            return '"' if style == "'" else style

    class Dumper(Emitter, yaml.serializer.Serializer, yaml.representer.SafeRepresenter, yaml.resolver.Resolver):
        def __init__(self, stream, **kw):
            Emitter.__init__(self, stream, width=kw.get("width"), allow_unicode=kw.get("allow_unicode"))
            yaml.serializer.Serializer.__init__(self)
            yaml.representer.SafeRepresenter.__init__(self, sort_keys=True)
            yaml.resolver.Resolver.__init__(self)

    def text(rep, data):
        return rep.represent_scalar("tag:yaml.org,2002:str", data, style="|" if "\n" in data else None)

    Dumper.add_representer(str, text)
    return Dumper


def disabled(doc):
    meta = doc.get("metadata") if isinstance(doc, dict) else None
    if not isinstance(meta, dict):
        return False
    for part in ("annotations", "labels"):
        marks = meta.get(part)
        if isinstance(marks, dict) and marks.get(DISABLED) == "disabled":
            return True
    return False


class Kit:
    """One kit folder. Each file is read and written again once; each value set only substitutes and parses."""

    def __init__(self, kit):
        if not os.path.isdir(kit):
            raise CannotJudge("%s is not a directory" % kit)
        self.kit = kit
        self.dump_with = dumper()
        self.texts = {}

    def documents(self, rel):
        if rel not in self.texts:
            yaml = yaml_module()
            load = getattr(yaml, "CSafeLoader", yaml.SafeLoader)
            try:
                docs = [d for d in yaml.load_all(open(os.path.join(self.kit, rel), encoding="utf-8"), Loader=load) if d is not None]
            except (OSError, yaml.YAMLError) as e:
                raise CannotJudge("%s cannot be read as YAML: %s" % (rel, str(e).split("\n")[0]))
            self.texts[rel] = [(True, canonical(d)) if disabled(d) else
                               (False, yaml.dump(d, Dumper=self.dump_with, width=1 << 30, allow_unicode=True, default_flow_style=False))
                               for d in docs]
        return self.texts[rel]

    def render(self, rel, values):
        yaml = yaml_module()
        load = getattr(yaml, "CSafeLoader", yaml.SafeLoader)
        out = []
        for fixed, text in self.documents(rel):
            if fixed:
                out.append(text)
                continue
            try:
                out.append(canonical(yaml.load(substitute(text, values), Loader=load)))
            except yaml.YAMLError as e:
                raise CannotJudge("%s is not YAML after substitution: %s" % (rel, str(e).split("\n")[0]))
            except CannotJudge as e:
                raise CannotJudge("%s: %s" % (rel, e))
        return out

    def root(self, root, values):
        rows = []
        for rel in files_of(self.kit, root):
            for n, doc in enumerate(self.render(rel, values)):
                rows.append("%s\t%d\t%s" % (rel, n, doc))
        return hashlib.sha256(("\n".join(rows) + "\n").encode("utf-8")).hexdigest(), rows


def canonical(doc):
    return json.dumps(doc, sort_keys=True, separators=(",", ":"), default=str)


def files_of(kit, root):
    """The files of one root in the order the kustomization.yaml files list them, as paths under the kit."""
    yaml = yaml_module()
    files = []

    def inside(rel):
        rel = os.path.normpath(rel)
        if rel == ".." or rel.startswith("../") or os.path.isabs(rel):
            raise CannotJudge("%s is outside %s" % (rel, KIT))
        return rel

    def walk(d, seen):
        if d in seen:
            raise CannotJudge("%s is listed twice in root %s" % (d, root))
        seen.add(d)
        k_rel = os.path.join(d, "kustomization.yaml")
        k_path = os.path.join(kit, k_rel)
        if not os.path.isfile(k_path):
            raise CannotJudge("%s/%s does not exist" % (KIT, k_rel))
        try:
            k = yaml.safe_load(open(k_path, encoding="utf-8")) or {}
        except yaml.YAMLError as e:
            raise CannotJudge("%s/%s cannot be read: %s" % (KIT, k_rel, str(e).split("\n")[0]))
        if not isinstance(k, dict):
            raise CannotJudge("%s/%s is not a mapping" % (KIT, k_rel))
        extra = sorted(set(k) - KUSTOMIZATION_FIELDS)
        if extra:
            raise CannotJudge("%s/%s uses %s, which this model does not render" % (KIT, k_rel, ", ".join(extra)))
        files.append(k_rel)
        for r in k.get("resources") or []:
            if not isinstance(r, str) or "://" in r:
                raise CannotJudge("%s/%s lists %r, which is not a local path" % (KIT, k_rel, r))
            p = inside(os.path.join(d, r))
            if os.path.isdir(os.path.join(kit, p)):
                walk(p, seen)
            elif os.path.isfile(os.path.join(kit, p)):
                files.append(p)
            else:
                raise CannotJudge("%s/%s lists %s, which does not exist" % (KIT, k_rel, r))
        for patch in k.get("patches") or []:
            if isinstance(patch, dict) and "path" in patch:
                p = inside(os.path.join(d, patch["path"]))
                if not os.path.isfile(os.path.join(kit, p)):
                    raise CannotJudge("%s/%s names patch %s, which does not exist" % (KIT, k_rel, patch["path"]))
                files.append(p)

    walk(os.path.normpath(root), set())
    return files


def render_kit(kit, roots, value_sets):
    k = Kit(kit)
    return {(name, root): k.root(root, values) for name, values in value_sets for root in roots}


# ---- git

def git(repo, *args):
    r = subprocess.run(["git", "-C", repo] + list(args), stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if r.returncode != 0:
        raise CannotJudge("git %s failed: %s" % (" ".join(args), r.stderr.decode("utf-8", "replace").strip()))
    return r.stdout


def kit_at(repo, ref, into):
    sha = git(repo, "rev-parse", "--verify", "--quiet", ref + "^{commit}").decode().strip()
    data = git(repo, "archive", "--format=tar", sha, KIT)
    with tarfile.open(fileobj=io.BytesIO(data)) as t:
        for m in t.getmembers():
            if not (m.isdir() or m.isfile()) or m.name.startswith("/") or ".." in m.name.split("/"):
                raise CannotJudge("%s holds %s, which this model does not read" % (ref, m.name))
        try:
            t.extractall(into, filter="data")
        except TypeError:  # a Python without extraction filters; every member was checked above
            t.extractall(into)
    return sha, os.path.join(into, KIT)


def describe(rows):
    """One label per document of a rendered root: its file, kind, namespace and name."""
    out = {}
    for row in rows:
        rel, n, doc = row.split("\t", 2)
        obj = json.loads(doc)
        meta = obj.get("metadata") if isinstance(obj, dict) else None
        meta = meta if isinstance(meta, dict) else {}
        kind = obj.get("kind", "?") if isinstance(obj, dict) else "?"
        out.setdefault("%s %s %s/%s" % (rel, kind, meta.get("namespace", "-"), meta.get("name", "#" + n)), []).append(doc)
    return out


# ---- commands

def compare(repo, ref):
    roots = roots_of(os.path.join(repo, TEMPLATE))
    value_sets = read_values(os.path.join(repo, FIXTURE), os.path.join(repo, KIT_KEYS))
    with tempfile.TemporaryDirectory() as tmp:
        sha, base_kit = kit_at(repo, ref, tmp)
        base = render_kit(base_kit, roots, value_sets)
        head = render_kit(os.path.join(repo, KIT), roots, value_sets)
    different = 0
    for name, _ in value_sets:
        for root in roots:
            b, h = base[(name, root)], head[(name, root)]
            if b[0] == h[0]:
                print("%-4s %s same %s" % (name, root, h[0]))
                continue
            different += 1
            print("%-4s %s DIFFERENT base=%s head=%s" % (name, root, b[0], h[0]))
            db, dh = describe(b[1]), describe(h[1])
            shown = 0
            for label in sorted(set(db) | set(dh)):
                if db.get(label) != dh.get(label):
                    where = "only at the base" if label not in dh else "only in the working tree" if label not in db else "differs"
                    if shown < 10:
                        print("     %s: %s" % (where, label))
                    shown += 1
            if shown > 10:
                print("     ... and %d more" % (shown - 10))
    if different:
        print("%s: %d rendered roots differ from %s; a change that only moves values into substitutions renders the same" % (ME, different, sha[:12]))
        return 1
    print("%s: every root renders the same at %s and in the working tree, with both value sets" % (ME, sha[:12]))
    return 0


def print_kit(repo, kit):
    roots = roots_of(os.path.join(repo, TEMPLATE))
    value_sets = read_values(os.path.join(repo, FIXTURE), os.path.join(repo, KIT_KEYS))
    rendered = render_kit(kit, roots, value_sets)
    for name, _ in value_sets:
        for root in roots:
            digest, rows = rendered[(name, root)]
            print("%-4s %s %s %d" % (name, root, digest, len(rows)))
    return 0


# ---- self-test: a Git repository of its own; nothing of the checkout is read

TEMPLATE_TEXT = """\
#cloud-config
write_files:
  - content: |
      spec:
        # path: ./clusters/_template/commented-out
        path: ./clusters/_template/apps
        path: ./clusters/_template/edge%{ if provider == "a" }-a%{ endif }
        path: ./clusters/_template/region/%{ if one == "" }base%{ else }%{ if two }multi%{ else }follower%{ endif }%{ endif }
"""

APP = """\
apiVersion: source.toolkit.fluxcd.io/v1
kind: HelmRepository
metadata:
  name: charts
  namespace: flux-system
spec:
  # a comment: ${NOT_SUBSTITUTED}
  url: oci://registry.example.test/charts
  secretRef:
    name: pull-secret
  enabled: "true"
  script: "echo $${HOME} $HOME"
  bare: $FQDN
  zones: ${ZONES}
  filter: ${UNSET_FILTER:-["${FQDN}"]}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: script
  annotations:
    kustomize.toolkit.fluxcd.io/substitute: disabled
data:
  run.sh: |
    echo ${FQDN}
"""

def self_fixture(t):
    def put(rel, text):
        p = os.path.join(t, rel)
        os.makedirs(os.path.dirname(p), exist_ok=True)
        with open(p, "w", encoding="utf-8") as f:
            f.write(text)
    put(TEMPLATE, TEMPLATE_TEXT)
    put(FIXTURE, "# values\nFQDN=alpha.example.test\nPULL_SECRET=pull-secret\nREGISTRY_HOST=registry.example.test\n"
                 "ZONES=[{name: a.example.test}]\nFLAG=true\nOTHER_HOST=other.example.test\nEMPTY=\nBAD=[unclosed\n")
    put(KIT_KEYS, "FQDN\nZONES\nFLAG\nEMPTY\n")
    put(KIT + "/apps/kustomization.yaml", "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - app.yaml\n  - shared\n")
    put(KIT + "/apps/app.yaml", APP)
    put(KIT + "/apps/shared/kustomization.yaml", "resources:\n  - shared.yaml\n")
    put(KIT + "/apps/shared/shared.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: shared\ndata:\n  fqdn: ${FQDN}\n")
    put(KIT + "/edge/kustomization.yaml", "resources:\n  - edge.yaml\n")
    put(KIT + "/edge/edge.yaml", "kind: Gateway\nmetadata:\n  name: edge\nspec:\n  host: edge.${FQDN}\n")
    put(KIT + "/edge-a/kustomization.yaml", "resources:\n  - ../edge\npatches:\n  - patch: |\n      kind: Gateway\n      metadata:\n        name: edge\n        annotations:\n          lb: \"${FQDN}-lb\"\n")
    for r in ("base", "multi", "follower"):
        put(KIT + "/region/%s/kustomization.yaml" % r, "resources:\n  - r.yaml\n")
        put(KIT + "/region/%s/r.yaml" % r, "kind: ConfigMap\nmetadata:\n  name: %s\ndata:\n  region: ${EMPTY:=none}\n" % r)
    run = lambda *a: subprocess.run(["git", "-C", t, "-c", "user.name=self-test", "-c", "user.email=self-test@example.test",
                                     "-c", "commit.gpgsign=false"] + list(a),
                                    stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=True)
    run("init", "-q")
    run("add", "-A")
    run("commit", "-q", "-m", "base")
    return git(t, "rev-parse", "HEAD").decode().strip()


def edit(t, rel, old, new):
    p = os.path.join(t, KIT, rel)
    text = open(p, encoding="utf-8").read()
    if old not in text:
        raise AssertionError("self-test fixture has no %r in %s" % (old, rel))
    with open(p, "w", encoding="utf-8") as f:
        f.write(text.replace(old, new, 1))


CASES = [
    # (what it shows, exit code, text the output holds, change)
    ("an unchanged kit renders the same", 0, "every root renders the same", lambda t: None),
    ("the roots come from every branch of the template conditions, not from a comment", 0,
     "kit  region/follower same", lambda t: None),
    ("a literal turned into a kit key with the same value renders the same", 0, "every root renders the same",
     lambda t: edit(t, "apps/shared/shared.yaml", "fqdn: ${FQDN}", "fqdn: alpha.example.test")),
    ("a literal turned into a key that only the full set holds differs in the kit set", 1,
     "kit  apps DIFFERENT", lambda t: edit(t, "apps/app.yaml", "name: pull-secret", "name: ${PULL_SECRET}")),
    ("a literal turned into a key with the same default renders the same", 0, "every root renders the same",
     lambda t: edit(t, "apps/app.yaml", "name: pull-secret", "name: ${PULL_SECRET:=pull-secret}")),
    ("a literal turned into a key with another value differs", 1, "full apps DIFFERENT",
     lambda t: edit(t, "apps/app.yaml", "oci://registry.example.test/charts", "oci://${OTHER_HOST}/charts")),
    ("the difference names the file and the object", 1, "differs: apps/app.yaml HelmRepository flux-system/charts",
     lambda t: edit(t, "apps/app.yaml", "oci://registry.example.test/charts", "oci://${OTHER_HOST}/charts")),
    ("a plain change of a value differs", 1, "full apps DIFFERENT",
     lambda t: edit(t, "apps/shared/shared.yaml", "name: shared", "name: shared-2")),
    ("a comment change renders the same", 0, "every root renders the same",
     lambda t: edit(t, "apps/app.yaml", "# a comment: ${NOT_SUBSTITUTED}", "# another comment: ${X")),
    ("a quoted value that a substitution turns into a boolean differs", 1, "full apps DIFFERENT",
     lambda t: edit(t, "apps/app.yaml", 'enabled: "true"', 'enabled: "${FLAG}"')),
    ("$$ gives one $, so $$${EMPTY}{HOME} renders as $${HOME} does", 0, "every root renders the same",
     lambda t: edit(t, "apps/app.yaml", "$${HOME}", "$$${EMPTY}{HOME}")),
    ("a bare $NAME is not substituted, so writing its value there differs", 1, "full apps DIFFERENT",
     lambda t: edit(t, "apps/app.yaml", "bare: $FQDN", "bare: alpha.example.test")),
    ("an escaped $${ stays a literal ${, so removing the escape differs", 1, "full apps DIFFERENT",
     lambda t: edit(t, "apps/app.yaml", "$${HOME}", "${HOME}")),
    ("a document with substitution disabled keeps ${...}, so writing the value there differs", 1, "full apps DIFFERENT",
     lambda t: edit(t, "apps/app.yaml", "echo ${FQDN}", "echo alpha.example.test")),
    ("a nested default is substituted", 0, "every root renders the same",
     lambda t: edit(t, "apps/app.yaml", 'filter: ${UNSET_FILTER:-["${FQDN}"]}', 'filter: ["alpha.example.test"]')),
    (":= takes the default for an empty value", 0, "every root renders the same",
     lambda t: edit(t, "region/base/r.yaml", "region: ${EMPTY:=none}", "region: none")),
    ("= keeps an empty value", 1, "full region/base DIFFERENT",
     lambda t: edit(t, "region/base/r.yaml", "region: ${EMPTY:=none}", "region: ${EMPTY=none}")),
    ("the text of an inline patch is compared", 1, "full edge-a DIFFERENT",
     lambda t: edit(t, "edge-a/kustomization.yaml", '"${FQDN}-lb"', '"${FQDN}-lb2"')),
    ("a change in a listed folder is found", 1, "full apps DIFFERENT",
     lambda t: edit(t, "apps/shared/shared.yaml", "fqdn: ${FQDN}", "fqdn: ${OTHER_HOST}")),
    ("a file added to a root differs", 1, "only in the working tree: apps/extra.yaml",
     lambda t: (edit(t, "apps/kustomization.yaml", "  - shared\n", "  - shared\n  - extra.yaml\n"),
                open(os.path.join(t, KIT, "apps/extra.yaml"), "w").write("kind: ConfigMap\nmetadata:\n  name: extra\n"))),
    ("a field of kustomization.yaml that the model does not render cannot be judged", 2, "uses namePrefix",
     lambda t: edit(t, "apps/kustomization.yaml", "kind: Kustomization\n", "kind: Kustomization\nnamePrefix: x-\n")),
    ("a listed file that does not exist cannot be judged", 2, "lists gone.yaml, which does not exist",
     lambda t: edit(t, "apps/kustomization.yaml", "  - app.yaml\n", "  - app.yaml\n  - gone.yaml\n")),
    ("a path outside the kit cannot be judged", 2, "is outside clusters/_template",
     lambda t: edit(t, "apps/kustomization.yaml", "  - shared\n", "  - ../../../x\n")),
    ("an unsupported substitution form cannot be judged", 2, "unsupported substitution ${FQDN:+x}",
     lambda t: edit(t, "apps/shared/shared.yaml", "fqdn: ${FQDN}", "fqdn: ${FQDN:+x}")),
    ("text that is not YAML after substitution cannot be judged", 2, "is not YAML after substitution",
     lambda t: edit(t, "apps/shared/shared.yaml", "fqdn: ${FQDN}", "fqdn: ${BAD}")),
    ("a missing value file cannot be judged", 2, "today.env cannot be read",
     lambda t: os.remove(os.path.join(t, FIXTURE))),
    ("a value line that is not KEY=VALUE cannot be judged", 2, "line 10 is not KEY=VALUE",
     lambda t: open(os.path.join(t, FIXTURE), "a").write("not a value\n")),
    ("a key named twice cannot be judged", 2, "names FQDN twice",
     lambda t: open(os.path.join(t, FIXTURE), "a").write("FQDN=x\n")),
    ("a missing kit key list cannot be judged", 2, "c5-kit-keys.txt cannot be read",
     lambda t: os.remove(os.path.join(t, KIT_KEYS))),
    ("a template without a root cannot be judged", 2, "names no path",
     lambda t: open(os.path.join(t, TEMPLATE), "w").write("#cloud-config\n")),
    ("a root missing in the working tree cannot be judged", 2, "edge-a/kustomization.yaml does not exist",
     lambda t: os.remove(os.path.join(t, KIT, "edge-a/kustomization.yaml"))),
]


def self_test():
    failed = 0
    for name, want, needle, change in CASES:
        with tempfile.TemporaryDirectory() as t:
            ok = True
            got, out = None, ""
            try:
                base = self_fixture(t)
                change(t)
                buf = io.StringIO()
                stdout, sys.stdout = sys.stdout, buf
                try:
                    got = compare(t, base)
                except CannotJudge as e:
                    print("%s: cannot judge: %s" % (ME, e))
                    got = 2
                finally:
                    sys.stdout = stdout
                out = buf.getvalue()
            except Exception as e:  # a broken case is a failed case
                ok, out = False, "%s: %s" % (type(e).__name__, e)
            if got != want or needle not in out:
                ok = False
            if ok:
                print("ok    %s" % name)
            else:
                failed += 1
                print("FAIL  %s" % name)
                print("      wanted exit %s and %r; got exit %s and:" % (want, needle, got))
                print("\n".join("        " + l for l in out.rstrip().split("\n")))
    # --print on the same repository: one line per value set and root.
    with tempfile.TemporaryDirectory() as t:
        self_fixture(t)
        buf = io.StringIO()
        stdout, sys.stdout = sys.stdout, buf
        try:
            code = print_kit(t, os.path.join(t, KIT))
        finally:
            sys.stdout = stdout
        lines = buf.getvalue().rstrip().split("\n")
        name = "--print prints one line per value set and root"
        if code == 0 and len(lines) == 12 and lines[0].startswith("full apps ") and lines[0].endswith(" 5"):
            print("ok    %s" % name)
        else:
            failed += 1
            print("FAIL  %s" % name)
            print("\n".join("        " + l for l in lines))
    total = len(CASES) + 1
    print("%s: self-test %s: %d of %d cases as expected" % (ME, "passed" if failed == 0 else "failed", total - failed, total))
    return 0 if failed == 0 else 1


def main(argv):
    repo = argv[0]
    args = argv[1:]
    mode, ref, kit = "compare", None, None
    while args:
        a = args.pop(0)
        if a == "--self-test" and not args:
            mode = "self-test"
        elif a == "--print":
            mode = "print"
        elif a == "--base" and args:
            ref = args.pop(0)
        elif a == "--kit" and args:
            kit = args.pop(0)
        else:
            raise CannotJudge("unknown or incomplete argument %s (see the head of scripts/%s.sh)" % (a, ME))
    if mode == "self-test":
        return self_test()
    if mode == "print":
        if ref is not None:
            raise CannotJudge("--base does not go with --print")
        return print_kit(repo, os.path.abspath(kit) if kit else os.path.join(repo, KIT))
    if kit is not None:
        raise CannotJudge("--kit goes only with --print")
    if ref is None:
        ref = git(repo, "merge-base", "HEAD", "origin/main").decode().strip()
    return compare(repo, ref)


try:
    sys.exit(main(sys.argv[1:]))
except CannotJudge as e:
    print("%s: cannot judge: %s" % (ME, e))
    sys.exit(2)
PY
