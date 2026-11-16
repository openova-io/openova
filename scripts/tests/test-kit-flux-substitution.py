#!/usr/bin/env python3
"""test-kit-flux-substitution.py - no placeholder of the kit reaches a cluster unsubstituted (Refs #7936).

Flux's post-build substitution does not replace every ${...}. The rule, read from the source of the
kustomize-controller a Sovereign runs (v1.4.0, built with fluxcd/pkg/kustomize v1.13.0 and
fluxcd/pkg/envsubst v1.1.0):

  R1  internal/controller/kustomization_controller.go: a Kustomization without spec.postBuild is not
      substituted at all.
  R2  kustomize/kustomize_varsub.go, SubstituteVariables: a document whose label or annotation
      kustomize.toolkit.fluxcd.io/substitute is "disabled" is taken as it is.
  R3  loadVars: the variables are the data of every substituteFrom ConfigMap or Secret, in order, a later
      source overriding an earlier one; an optional source that does not exist is skipped. Then the inline
      postBuild.substitute overrides them all. A newline in a value is dropped.
  R4  only when that set is not empty (`if len(vars) > 0`) is the document substituted. With an empty set
      ${NAME:=default} reaches the API server as it is: the default never applies.
  R5  varSubstitution: every variable name must match ^[_[:alpha:]][_[:alpha:][:digit:]]*$, or the build fails.
  R6  envsubst: ${NAME} is the value, the empty text when unset; ${NAME<op>default} with op one of
      := :- = - :+ + :? is the value when it is not empty, else the default (lookupFunc maps them all to
      toDefault); "$$" gives "$".

On hw307 (16 Nov 2026) slot 12b's Kustomization sovereign-object substituted only from the optional ConfigMap
sovereign-network-registry, which does not exist before hand-over. By R4 the Job kept the name
sovereign-object-${NETWORK_REGISTRY_REVISION:=bootstrap} and the API server refused it in both regions.

What is rendered, once with the values of the primary region and once with those of a follower region:
  - the bootstrap kit, clusters/_template/bootstrap-kit, with the postBuild of the Kustomization bootstrap-kit
    of the boot template infra/providers/_shared/cloudinit-control-plane.tftpl; its ConfigMap
    sovereign-variables holds tests/fixtures/variables/today.env;
  - every Flux Kustomization the rendered kit defines (its source must be the GitRepository openova): the
    folder of this repository its path names, built from its kustomization.yaml (resources: files and
    folders; patches: one inline strategic merge patch without a target), with its own postBuild, once for
    every combination of its optional sources absent and present.

It fails when a document Flux applies still holds a "${" that its source did not escape as "$${", when a
source has no model here (add one to sources()), when a variable name is refused (R5), when a placeholder has
a form other than R6, or when such a folder defines a Flux Kustomization of its own (not rendered yet). Before it judges the kit it proves on documents of its own that each rule can fail.

    python3 scripts/tests/test-kit-flux-substitution.py      exit 0 pass, 1 fail
Needs Python 3 and PyYAML.
"""
import copy
import itertools
import os
import re
import sys

import yaml

ROOT = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", ".."))
KIT = "clusters/_template/bootstrap-kit"
TEMPLATE = "infra/providers/_shared/cloudinit-control-plane.tftpl"
FIXTURE = "tests/fixtures/variables/today.env"
REGISTRY_GO = "products/catalyst/bootstrap/api/internal/lifecycle/network_registry.go"
DISABLED = "kustomize.toolkit.fluxcd.io/substitute"
FIELDS = {"apiVersion", "kind", "resources", "patches"}
VAR_NAME = re.compile(r"[_A-Za-z][_A-Za-z0-9]*")
VAR = re.compile(r"\$\{([A-Za-z_][A-Za-z0-9_]*)(?:(:=|:-|:\+|:\?|=|-|\+)((?:[^{}]|\{[^{}]*\})*))?\}")
LEFT = re.compile(r"\$\{[^}\n]{0,120}\}?")
ESCAPE = "\0"
fails = []


class CannotJudge(Exception):
    pass


def check(cond, what):
    print(("PASS  " if cond else "FAIL  ") + what)
    if not cond:
        fails.append(what)


def ident(doc):
    meta = doc.get("metadata") or {}
    return "%s/%s" % (doc.get("kind"), meta.get("name"))


def is_flux_kustomization(doc):
    return str(doc.get("apiVersion", "")).startswith("kustomize.toolkit.fluxcd.io/") and doc.get("kind") == "Kustomization"


def disabled(doc):
    meta = doc.get("metadata") or {}
    return (meta.get("annotations") or {}).get(DISABLED) == "disabled" or (meta.get("labels") or {}).get(DISABLED) == "disabled"


def flux_vars(post, models, absent):
    """R3: the variables of a postBuild; absent names the optional sources that do not exist."""
    out = {}
    for ref in (post or {}).get("substituteFrom") or []:
        key = (ref.get("kind"), ref.get("name"))
        if key in absent:
            if not ref.get("optional"):
                raise CannotJudge("%s/%s is not optional, so Flux fails when it is absent" % key)
            continue
        if key not in models:
            raise CannotJudge("no model of the substituteFrom source %s/%s; add one to sources()" % key)
        for k, v in models[key].items():
            out[k] = str(v).replace("\n", "")
    for k, v in ((post or {}).get("substitute") or {}).items():
        out[k] = str(v).replace("\n", "")
    return out


def flux_render(doc, post, values):
    """The document Flux applies, and one line per placeholder or refusal left in it."""
    if disabled(doc):
        return doc, []  # R2
    text = yaml.safe_dump(doc, sort_keys=True, width=10 ** 9)
    if post is None or not values:  # R1, R4
        why = "the Kustomization has no postBuild" if post is None else "no variable is set, so Flux does not substitute"
        return doc, ["%s keeps %s (%s)" % (ident(doc), p, why) for p in sorted(set(LEFT.findall(text)))]
    bad = sorted(k for k in values if not VAR_NAME.fullmatch(k))
    if bad:  # R5
        return doc, ["Flux refuses the variable name(s) %s" % ", ".join(bad)]
    text = text.replace("$$", ESCAPE)
    for _ in range(10):
        def one(m):
            v = values.get(m.group(1), "")
            return v if (m.group(2) is None or v) else m.group(3)
        new = VAR.sub(one, text)
        if new == text:
            break
        text = new
    left = ["%s keeps %s (not a form of R6, or nested too deep)" % (ident(doc), p) for p in sorted(set(LEFT.findall(text)))]
    return yaml.safe_load(text.replace(ESCAPE, "$")), left


def load(path):
    full = os.path.join(ROOT, path)
    if not os.path.isfile(full):
        raise CannotJudge("%s is not a file of the repository" % path)
    with open(full) as f:
        return [d for d in yaml.safe_load_all(f) if d]


def merge(dst, src):
    for k, v in src.items():
        if isinstance(v, dict) and isinstance(dst.get(k), dict):
            merge(dst[k], v)
        else:
            dst[k] = copy.deepcopy(v)


def build(folder):
    """The documents kustomize builds from folder (a path relative to the repository)."""
    folder = os.path.normpath(folder)
    if folder.startswith("..") or os.path.isabs(folder):
        raise CannotJudge("%s is not a folder of this repository" % folder)
    k = load(folder + "/kustomization.yaml")[0]
    extra = set(k) - FIELDS
    if extra:
        raise CannotJudge("%s/kustomization.yaml holds %s; only %s can be judged" % (folder, sorted(extra), sorted(FIELDS)))
    docs = []
    for r in k.get("resources") or []:
        p = os.path.normpath(os.path.join(folder, r))
        docs += build(p) if os.path.isdir(os.path.join(ROOT, p)) else load(p)
    for p in k.get("patches") or []:
        if set(p) != {"patch"}:
            raise CannotJudge("%s: a patch other than one inline strategic merge patch without a target" % folder)
        patch = yaml.safe_load(p["patch"])
        hit = [d for d in docs if ident(d) == ident(patch)]
        if len(hit) != 1:
            raise CannotJudge("%s: the patch of %s matches %d objects" % (folder, ident(patch), len(hit)))
        merge(hit[0], patch)
    return docs


def env(path):
    values = {}
    with open(os.path.join(ROOT, path)) as f:
        for line in f:
            line = line.rstrip("\n")
            if line and not line.startswith("#") and "=" in line:
                k, v = line.split("=", 1)
                values[k] = v
    return values


def registry_keys():
    """The two keys the parent writes into sovereign-network-registry, from the code that writes them."""
    with open(os.path.join(ROOT, REGISTRY_GO)) as f:
        src = f.read()
    keys = [re.search(r'\b%s\s*=\s*"([^"]+)"' % c, src) for c in ("NetworkRegistryKey", "NetworkRegistryRevisionKey")]
    if not all(keys):
        raise CannotJudge("%s no longer names NetworkRegistryKey and NetworkRegistryRevisionKey" % REGISTRY_GO)
    return keys[0].group(1), keys[1].group(1)


def sources(region_values):
    """What each ConfigMap a Kustomization of the kit may substitute from holds when it exists."""
    doc_key, rev_key = registry_keys()
    return {("ConfigMap", "sovereign-variables"): region_values,
            ("ConfigMap", "sovereign-network-registry"): {doc_key: '{"regions": [{"code": "a"}]}', rev_key: "3f2a9c1b0d4e"}}


def boot_kustomization():
    """The Kustomization bootstrap-kit of the boot template, as YAML."""
    with open(os.path.join(ROOT, TEMPLATE)) as f:
        lines = f.read().split("\n")
    hits = [i for i, l in enumerate(lines) if l.strip() == "name: bootstrap-kit"]
    blocks = []
    for i in hits:
        start = next((j for j in range(i, -1, -1) if lines[j].strip().startswith("apiVersion: ")), None)
        end = next((j for j in range(i, len(lines)) if lines[j].strip() == "---"), len(lines))
        if start is None or not lines[start].strip().startswith("apiVersion: kustomize.toolkit.fluxcd.io/"):
            continue
        block = lines[start:end]
        if any("%{" in l or "${" in l for l in block):
            raise CannotJudge("the Kustomization bootstrap-kit of %s holds a template expression" % TEMPLATE)
        indent = len(lines[start]) - len(lines[start].lstrip())
        blocks.append(yaml.safe_load("\n".join(l[indent:] for l in block)))
    blocks = [b for b in blocks if b.get("kind") == "Kustomization"]
    if len(blocks) != 1:
        raise CannotJudge("%s defines %d Kustomizations bootstrap-kit" % (TEMPLATE, len(blocks)))
    return blocks[0]


def self_test():
    """Each rule can fail: the judge below is trusted only if these hold."""
    job = {"apiVersion": "batch/v1", "kind": "Job", "metadata": {"name": "w-${REV:=bootstrap}"}}
    cm = ("ConfigMap", "reg")
    models = {cm: {"REV": "abc"}, ("ConfigMap", "other"): {"REV": "def"}}
    only_cm = {"substituteFrom": [{"kind": "ConfigMap", "name": "reg", "optional": True}]}
    with_inline = dict(only_cm, substitute={"ANY": "1"})

    def run(doc, post, absent=()):
        return flux_render(doc, post, flux_vars(post, models, set(absent)) if post is not None else {})

    out, left = run(job, only_cm, [cm])
    check(left and out["metadata"]["name"] == "w-${REV:=bootstrap}",
          "S1 R4: the only source absent, no inline variable: the default does not apply and the placeholder is found")
    out, left = run(job, with_inline, [cm])
    check(not left and out["metadata"]["name"] == "w-bootstrap", "S2 R4: one inline variable: the default applies")
    out, left = run(job, with_inline)
    check(not left and out["metadata"]["name"] == "w-abc", "S3 R3: the source present: its value applies")
    out, _ = run(job, {"substituteFrom": [{"kind": "ConfigMap", "name": "reg"}], "substitute": {"REV": "pinned"}})
    check(out["metadata"]["name"] == "w-pinned", "S4 R3: an inline variable overrides every substituteFrom source")
    out, _ = run(job, {"substituteFrom": [{"kind": "ConfigMap", "name": "reg"}, {"kind": "ConfigMap", "name": "other"}]})
    check(out["metadata"]["name"] == "w-def", "S5 R3: a later substituteFrom source overrides an earlier one")
    off = copy.deepcopy(job)
    off["metadata"]["annotations"] = {DISABLED: "disabled"}
    out, left = run(off, with_inline)
    lab = copy.deepcopy(job)
    lab["metadata"]["labels"] = {DISABLED: "disabled"}
    out2, left2 = run(lab, with_inline)
    check(not left and not left2 and out["metadata"]["name"] == out2["metadata"]["name"] == "w-${REV:=bootstrap}",
          "S6 R2: an annotation or label substitute: disabled takes the document as it is")
    _, left = run(job, None)
    check(bool(left), "S7 R1: no postBuild: the placeholder is found")
    esc = {"kind": "ConfigMap", "metadata": {"name": "e"}, "data": {"a": "$${KEEP}"}}
    out, left = run(esc, with_inline)
    _, left2 = run(esc, only_cm, [cm])
    check(not left and out["data"]["a"] == "${KEEP}" and bool(left2),
          "S8 R6: $${NAME} gives ${NAME} when Flux substitutes, and stays $${NAME} when it does not")
    _, left = flux_render(job, {}, {"a-b": "1"})
    check(bool(left), "S9 R5: a variable name Flux refuses is found")
    _, left = flux_render({"kind": "X", "metadata": {"name": "x"}, "v": "${#LEN}"}, {}, {"A": "1"})
    check(bool(left), "S10 R6: a form this test does not model is found, not passed")
    try:
        flux_vars({"substituteFrom": [{"kind": "ConfigMap", "name": "unknown"}]}, models, set())
        check(False, "S11 a source without a model cannot be judged")
    except CannotJudge:
        check(True, "S11 a source without a model cannot be judged")


def judge_kustomization(region, ks, models):
    """Render the folder of one Flux Kustomization of the kit, every optional source absent and present."""
    name = "%s/%s" % ((ks.get("metadata") or {}).get("namespace"), (ks.get("metadata") or {}).get("name"))
    spec = ks.get("spec") or {}
    src = spec.get("sourceRef") or {}
    path = str(spec.get("path") or "")
    if src.get("kind") != "GitRepository" or src.get("name") != "openova" or not path.startswith("./"):
        check(False, "F3 %s: Kustomization %s applies %s from %s, which this test cannot render" % (region, name, path, src))
        return
    post = spec.get("postBuild")
    docs = build(path[2:])
    refs = [(r.get("kind"), r.get("name")) for r in (post or {}).get("substituteFrom") or [] if r.get("optional")]
    for absent in itertools.product((False, True), repeat=len(refs)):
        gone = {r for r, a in zip(refs, absent) if a}
        case = ", ".join("%s %s %s" % (k, n, "absent" if (k, n) in gone else "present") for k, n in refs) or "no optional source"
        try:
            values = flux_vars(post, models, gone)
        except CannotJudge as e:
            check(False, "F3 %s, Kustomization %s, %s: %s" % (region, name, case, e))
            continue
        left = []
        names = []
        for d in docs:
            out, lines = flux_render(d, post, values)
            left += lines
            names.append(ident(out))
            if is_flux_kustomization(out):
                left.append("it defines the Flux Kustomization %s, which this test does not render yet; extend it" % ident(out))
        for line in left:
            check(False, "F3 %s, Kustomization %s, %s: %s" % (region, name, case, line))
        if not left:
            check(True, "F3 %s, Kustomization %s, %s: %d documents, no placeholder left (%s)" % (region, name, case, len(docs), ", ".join(sorted(names))))


def main():
    self_test()
    if fails:
        print("%d failed: the self-test, so the kit was not judged" % len(fails))
        return 1
    try:
        boot = boot_kustomization()
    except CannotJudge as e:
        check(False, "F1 %s" % e)
        return 1
    bspec = boot.get("spec") or {}
    check(bspec.get("path") == "./" + KIT and (bspec.get("postBuild") or {}).get("substituteFrom") == [{"kind": "ConfigMap", "name": "sovereign-variables"}],
          "F1 the boot template's Kustomization bootstrap-kit applies ./%s and substitutes from the ConfigMap sovereign-variables" % KIT)
    primary = env(FIXTURE)
    follower = dict(primary, SOVEREIGN_REGION_ROLE="secondary", SECONDARY_HR_SUSPEND="true", SOVEREIGN_REGION_ID="secondary")
    for region, region_values in (("primary", primary), ("follower", follower)):
        try:
            models = sources(region_values)
            post = bspec.get("postBuild")
            values = flux_vars(post, models, set())
            kit = build(KIT)
        except CannotJudge as e:
            check(False, "F2 %s: %s" % (region, e))
            continue
        rendered, left = [], []
        for d in kit:
            out, lines = flux_render(d, post, values)
            rendered.append(out)
            left += lines
        for line in left:
            check(False, "F2 %s, the bootstrap kit: %s" % (region, line))
        if not left:
            check(True, "F2 %s: the bootstrap kit renders %d documents, no placeholder left" % (region, len(rendered)))
        kss = [d for d in rendered if is_flux_kustomization(d)]
        check(bool(kss), "F3 %s: the kit defines %d Flux Kustomization(s)" % (region, len(kss)))
        for ks in kss:
            try:
                judge_kustomization(region, ks, models)
            except CannotJudge as e:
                check(False, "F3 %s, Kustomization %s: %s" % (region, ident(ks), e))
    print("%d failed" % len(fails) if fails else "all passed")
    return 1 if fails else 0


if __name__ == "__main__":
    sys.exit(main())
