#!/usr/bin/env python3
"""check-root-kustomization-parity.py - each root folder builds the child Kustomizations that the boot
template infra/providers/_shared/cloudinit-control-plane.tftpl renders (<<FLUXBOOTSTRAP) for its branch:
provider (infra/providers/<p>/pack.yaml), region layout (base: no replica region; multi-region: the
primary region; multi-region-follower: any other) and role. The folder is clusters/_template/providers/
<p>/root/<role> if that exists (it serves every layout), else clusters/_template/root/<layout>-<role>.
Template: a line that is only %{ if c ~}, %{ else ~} or %{ endif ~} is dropped, inline %{ if c }..%{ endif }
is evaluated; provider ==/!= "<p>" and COND follow the branch, and so does a bare template input that
every call in infra/providers/<p>/main.tf passes as one literal true or false (CLD-132); any other
condition takes both values, which must agree; comments and the six-space indent go as in the template; ${name} becomes the today.env
value of the key whose ConfigMap line is KEY: "${name}". Folder: kustomize resources (files, folders) and
inline patches without a target ("$patch: delete" removes, else merged); then ${NAME} from today.env.
R1 each folder builds what the template renders for its branch. R2 clusters/_template/root holds the six
<layout>-<role> folders; a provider's root holds one folder per role.
Usage: python3 scripts/check-root-kustomization-parity.py [--self-test]. Exit 0 pass, 1 fail, 2 cannot judge.
"""
import itertools, os, re, shutil, subprocess, sys, tempfile

import yaml

TEMPLATE = "infra/providers/_shared/cloudinit-control-plane.tftpl"
FIXTURE = "tests/fixtures/variables/today.env"
FOLDER = "clusters/_template/root"
OVERLAYS = "clusters/_template/providers"
LAYOUTS = ("base", "multi-region", "multi-region-follower")
ROLES = ("primary", "secondary")
COND = {
    'replica_region_canonical_label == ""': lambda l, r: l == "base",
    "region_canonical_label == primary_region_canonical_label": lambda l, r: l != "multi-region-follower",
    'sovereign_region_role == "primary"': lambda l, r: r == "primary",
    'sovereign_region_role != "primary"': lambda l, r: r != "primary",
}
FIELDS = {"apiVersion", "kind", "resources", "patches"}
PROVIDER_COND = re.compile(r'^provider (==|!=) "([a-z0-9-]+)"$')
DIRECTIVE = re.compile(r"(?m)^%\{ (if .+?|else|endif) ~\}\n|%\{ (if .+?|else|endif) \}")
CM_LINE = re.compile(r'(?m)^ +([A-Z][A-Z0-9_]*): "?\$\{([a-z_][a-z0-9_]*)\}"?$')
VAR = re.compile(r"\$\{([A-Za-z_][A-Za-z0-9_]*)\}")
INPUT = re.compile(r"^[a-z_][a-z0-9_]*$")


class CannotJudge(Exception):
    pass


def subfolders(root, path):
    return sorted(os.listdir(os.path.join(root, path))) if os.path.isdir(os.path.join(root, path)) else []


def branches(root):
    packs = [d for d in subfolders(root, "infra/providers") if os.path.isfile(os.path.join(root, "infra/providers", d, "pack.yaml"))]
    return list(itertools.product(packs, LAYOUTS, ROLES))


def folder_of(root, p, l, r):
    own = "%s/%s/root" % (OVERLAYS, p)
    return own + "/" + r if os.path.isdir(os.path.join(root, own)) else "%s/%s-%s" % (FOLDER, l, r)


def literal(root, p, name):
    """The one literal true/false that every call in infra/providers/<p>/main.tf passes as name, else None."""
    path = os.path.join(root, "infra/providers", p, "main.tf")
    found = set(re.findall(r"(?m)^ *%s *= *(true|false) *$" % re.escape(name), open(path).read())) if os.path.isfile(path) else set()
    if len(found) > 1:
        raise CannotJudge("infra/providers/%s/main.tf passes %s as both true and false" % (p, name))
    return found.pop() == "true" if found else None


def render(text, truth):
    out, stack, on, pos = [], [], True, 0
    for m in DIRECTIVE.finditer(text):
        out.append(text[pos:m.start()] if on else "")
        pos, word = m.end(), m.group(1) or m.group(2)
        if word.startswith("if "):
            stack.append((on, truth(word[3:].strip())))
            on = on and stack[-1][1]
        elif not stack:
            raise CannotJudge("an %s without its if" % word)
        elif word == "else":
            on = stack[-1][0] and not stack[-1][1]
        else:
            on = stack.pop()[0]
    text = "".join(out) + (text[pos:] if on else "")
    if stack or "%{" in text:
        raise CannotJudge("an if without its endif, or a directive of another form")
    return re.sub(r"(?m)^      ", "", re.sub(r"(?m)^[ ]*#( |$).*\n", "", text))


def by_name(docs):
    named = {}
    for d in docs:
        if d.get("kind") == "Kustomization":
            if d["metadata"]["name"] in named:
                raise CannotJudge("two Kustomizations named %s" % d["metadata"]["name"])
            named[d["metadata"]["name"]] = d
    return named


def from_template(root, values):
    m = re.search(r"<<FLUXBOOTSTRAP\n(.*?\n)FLUXBOOTSTRAP\n", open(os.path.join(root, TEMPLATE), encoding="utf-8").read(), re.S)
    if not m:
        raise CannotJudge("%s has no FLUXBOOTSTRAP text" % TEMPLATE)
    text, keys = m.group(1), {}
    for k, name in CM_LINE.findall(text):
        keys.setdefault(name, k)

    def value(v):
        if keys.get(v.group(1)) not in values:
            raise CannotJudge("%s is not a value of sovereign-variables" % v.group(0))
        return values[keys[v.group(1)]]
    conds = {c.strip() for c in re.findall(r"%\{ if (.+?) ~?\}", text)}
    packs = sorted({b[0] for b in branches(root)})
    fixed = {c: {p: literal(root, p, c) for p in packs} for c in conds if INPUT.match(c)}
    fixed = {c: v for c, v in fixed.items() if None not in v.values()}
    other = sorted(c for c in conds if not PROVIDER_COND.match(c) and c not in COND and c not in fixed)
    if len(other) > 10:
        raise CannotJudge("too many conditions: %s" % other)
    result = {}
    for b in branches(root):
        seen = None
        for choice in itertools.product((True, False), repeat=len(other)):
            pick = dict(zip(other, choice))
            body = render(text, lambda c: pick[c] if c in pick else fixed[c][b[0]] if c in fixed else (b[0] == PROVIDER_COND.match(c).group(2)) == (PROVIDER_COND.match(c).group(1) == "==") if PROVIDER_COND.match(c) else COND[c](*b[1:]))
            docs = [VAR.sub(value, d) for d in re.split(r"(?m)^---\n", body) if re.search(r"(?m)^kind: Kustomization$", d)]
            if any("${" in d for d in docs):
                raise CannotJudge("a Kustomization of the template holds an expression the check does not model")
            found = by_name(yaml.safe_load(d) for d in docs)
            if seen is not None and found != seen:
                print("FAIL R1: the conditions %s change the Kustomizations of provider %s, layout %s, role %s" % ((other,) + b))
                return None
            seen = found
        result[b] = seen
    return result


def load(root, path):
    with open(os.path.join(root, path), encoding="utf-8") as f:
        return [d for d in yaml.safe_load_all(f) if d]


def merge(dst, src):
    for k, v in src.items():
        if isinstance(v, dict) and isinstance(dst.get(k), dict):
            merge(dst[k], v)
        else:
            dst[k] = v


def build(root, folder):
    folder = os.path.normpath(folder)
    if not folder.startswith("clusters/_template/"):
        raise CannotJudge("%s is outside clusters/_template" % folder)
    k = load(root, folder + "/kustomization.yaml")[0]
    if set(k) - FIELDS:
        raise CannotJudge("%s/kustomization.yaml holds %s" % (folder, sorted(set(k) - FIELDS)))
    docs, ident = [], lambda d: (d.get("kind"), (d.get("metadata") or {}).get("name"), (d.get("metadata") or {}).get("namespace"))
    for res in k.get("resources") or []:
        p = os.path.normpath(os.path.join(folder, res))
        docs += build(root, p) if os.path.isdir(os.path.join(root, p)) else load(root, p)
    for item in k.get("patches") or []:
        patch = yaml.safe_load(item.get("patch", "")) if set(item) == {"patch"} else None
        hit = [d for d in docs if isinstance(patch, dict) and ident(d) == ident(patch)]
        if len(hit) != 1 or patch.get("$patch", "delete") != "delete":
            raise CannotJudge("%s: a patch that is not one inline patch of one object" % folder)
        if patch.pop("$patch", None):
            docs.remove(hit[0])
        else:
            merge(hit[0], patch)
    return docs


def check(root):
    lines = open(os.path.join(root, FIXTURE), encoding="utf-8").read().splitlines()
    values = dict(x.split("=", 1) for x in lines if x and not x.startswith("#") and "=" in x)
    packs = {b[0] for b in branches(root)}
    wrong = [] if subfolders(root, FOLDER) == sorted("%s-%s" % lr for lr in itertools.product(LAYOUTS, ROLES)) else [FOLDER]
    for p in subfolders(root, OVERLAYS):
        own = "%s/%s/root" % (OVERLAYS, p)
        if os.path.isdir(os.path.join(root, own)) and (p not in packs or subfolders(root, own) != sorted(ROLES)):
            wrong.append(own)
    if wrong:
        print("FAIL R2: %s must hold one folder per <layout>-<role> (clusters/_template/root) or per role (a provider's root)" % wrong)
        return 1
    rendered, failed = from_template(root, values), 0
    if rendered is None:
        return 1
    for b, want in rendered.items():
        where = folder_of(root, *b)
        text = VAR.sub(lambda v: values.get(v.group(1), v.group(0)), yaml.safe_dump_all(build(root, where)))
        if "$" in text:
            raise CannotJudge("%s keeps a $ after substitution" % where)
        got = by_name(yaml.safe_load_all(text))
        for n in sorted(set(want) | set(got)):
            if want.get(n) != got.get(n):
                failed += 1
                print("FAIL R1: %s (provider %s, layout %s): Kustomization %s: template %s, folder %s" % (where, b[0], b[1], n, want.get(n), got.get(n)))
    if not failed:
        print("PASS: %d branches; each root folder builds what the template renders" % len(rendered))
    return 1 if failed else 0


def self_test(repo):
    B, T, ok = FOLDER + "/base-primary/", TEMPLATE, True
    cases = [
        ("the tree as it is", 0, None, None, None),
        ("R1 a child file differs", 1, B + "bootstrap-kit.yaml", "  interval: 5m\n", "  interval: 6m\n"),
        ("R1 a folder patches the wrong path", 1, FOLDER + "/multi-region-primary/kustomization.yaml", "region/multi-region}", "region/base}"),
        ("R1 a folder keeps a document it must delete", 1, FOLDER + "/multi-region-secondary/kustomization.yaml", "{$patch: delete, ", "{"),
        ("R1 a child of the template differs", 1, T, "name: bootstrap-kit-crs\n        namespace: flux-system\n      spec:\n        interval: 5m\n", "name: bootstrap-kit-crs\n        namespace: flux-system\n      spec:\n        interval: 7m\n"),
        ("R1 another condition changes a child", 1, T, "name: bootstrap-kit\n        namespace: flux-system\n      spec:\n        interval: 5m\n", "name: bootstrap-kit\n        namespace: flux-system\n      spec:\n%{ if enable_x ~}\n        interval: 5m\n%{ else ~}\n        interval: 9m\n%{ endif ~}\n"),
        ("R1 a module literal that disagrees with the root folder", 1, "infra/providers/local/main.tf", "render_region_kustomization = true\n", "render_region_kustomization = false\n"),
        ("cannot judge a module that passes an input as true and as false", 2, "infra/providers/local/main.tf", "render_region_kustomization = true\n", "render_region_kustomization = true\n    render_region_kustomization = false\n"),
        ("R2 a folder is missing", 1, FOLDER + "/base-secondary", None, "remove"),
        ("R2 an extra folder", 1, FOLDER + "/extra", None, "create"),
        ("R2 a root folder of no provider", 1, OVERLAYS + "/nosuch/root/primary", None, "create"),
        ("cannot judge a field beyond the model", 2, B + "kustomization.yaml", "kind: Kustomization\n", "kind: Kustomization\nnamespace: flux-system\n"),
        ("cannot judge a value outside sovereign-variables", 2, T, '"${wildcard_cert_issuer}"\n      spec:', '"${no_such_input}"\n      spec:'),
    ]
    for label, expect, path, old, new in cases:
        tmp = tempfile.mkdtemp()
        try:
            for p in [TEMPLATE, FIXTURE] + ["infra/providers/%s/%s" % (p, f) for p in sorted({b[0] for b in branches(repo)}) for f in ("pack.yaml", "main.tf")]:
                os.makedirs(os.path.dirname(os.path.join(tmp, p)), exist_ok=True)
                shutil.copy(os.path.join(repo, p), os.path.join(tmp, p))
            for p in (FOLDER, OVERLAYS):
                shutil.copytree(os.path.join(repo, p), os.path.join(tmp, p))
            if new in ("remove", "create"):
                shutil.rmtree(os.path.join(tmp, path)) if new == "remove" else os.makedirs(os.path.join(tmp, path))
            elif path:
                t = open(os.path.join(tmp, path), encoding="utf-8").read()
                if t.count(old) != 1:
                    raise CannotJudge("self-test %s: the text to change is not there once" % label)
                open(os.path.join(tmp, path), "w", encoding="utf-8").write(t.replace(old, new))
            got = subprocess.run([sys.executable, os.path.abspath(__file__), "--root", tmp], capture_output=True).returncode
        finally:
            shutil.rmtree(tmp)
        ok = ok and got == expect
        print("%s self-test %s: exit %d, expected %d" % ("ok  " if got == expect else "FAIL", label, got, expect))
    return 0 if ok else 1


def main(argv):
    here = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    try:
        if argv and argv != ["--self-test"] and (argv[0] != "--root" or len(argv) != 2):
            raise CannotJudge("usage: check-root-kustomization-parity.py [--self-test]")
        return self_test(here) if argv == ["--self-test"] else check(argv[1] if argv else here)
    except (CannotJudge, OSError, yaml.YAMLError, KeyError, TypeError) as e:
        print("CANNOT JUDGE: %s" % e)
        return 2

if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
