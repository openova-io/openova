#!/usr/bin/env python3
"""Provider-leak scanner: lines that name a cloud provider in shared code may only fall.

  check-provider-leaks.py [--repo DIR] [--check | --update | --report | --self-test]

--check (default): exit 1 if a shared file's count is above its baseline line, or a shared file with a count
has no baseline line. --update: rewrite the baseline from the counts of now; exit 2, writing nothing, if a
count would rise or a path would be added. --report: a totals line, then one per top-level directory.
--self-test: proves on a Git repository of its own that each rule can fail. --repo: default, this repository.

Files: `git ls-files` with an extension in EXTENSIONS, not under docs/, no directory segment in SKIP_SEGMENTS,
no name ending in SKIP_SUFFIXES. A line counts once if TIER1 (any case) or else TIER2 (exact case) matches.
A line whose first non-blank characters are one of COMMENT is a comment line: reported, never gated. A file
is allow (a plug-in path; a directory starting with _ is shared), legacy (counted apart, must reach zero) or
shared. Baseline: one line per shared file with a count above 0, <path><TAB><count>, sorted by path.

Provider packs (infra/providers/<name>/pack.yaml, read with PyYAML when at least one is tracked): every token of
spec.leakTokens joins TIER1 as a word in any case (no letter or digit next to it), except a token that TIER2
counts when written in capitals: such a short cloud acronym keeps the exact-case rule of TIER2. The folders
platform/<chart>/ of every spec.charts replace CHART_ALLOW. With no pack.yaml, TIER1 and CHART_ALLOW apply as
they are. A pack.yaml that cannot be read, or whose spec.leakTokens or spec.charts is not a list of names, ends
every mode with exit 2. TIER1 and TIER2 stay the built-in rules: other guards import them.

Generated files (ruling 147): a file that scripts/generator_registry.py lists (a generator of the job "regenerate
and prove no diff" writes it) is judged at its sources, not as a copy: --check and --update do not count it when
it has sources and each is scanned or lies in a plug-in path. Its lines are counted in those sources. A listed
file with a source outside the scan is counted, with a note. --report keeps every file in its totals line and
names the files the ratchet leaves out on lines of their own, after the directories.
"""
import argparse, importlib.util, os, re, shutil, subprocess, sys, tempfile

BASELINE = "scripts/provider-leak-baseline.tsv"
EXTENSIONS = (".go", ".ts", ".tsx", ".js", ".mjs", ".svelte", ".astro", ".yaml", ".yml", ".tpl", ".tf",
              ".hcl", ".tftpl", ".sh", ".py")
SKIP_SEGMENTS = {"node_modules", "testdata", "fixtures", "__tests__", "test", "tests", "mock", "mocks", "e2e"}
SKIP_SUFFIXES = ("_test.go", ".test.ts", ".test.tsx", ".spec.ts", ".spec.tsx", ".tftest.hcl", "-lock.yaml", ".lock")
COMMENT = ("#", "//", "*", "/*", "{{/*", "<!--")
TIER1 = re.compile(r"huawei|hcloud|hetzner|kom4dc|nationalcloud|hcso|me-east-|myhuaweicloud|\b(fsn1|nbg1|hel1)\b"
                   r"|evs-ssd|212\.72\.", re.IGNORECASE)
TIER2 = re.compile(r"\b(EVS|ELB|EIP|ECS|CCE|HCS|OBS)\b|\b(evs|elb|eip|ecs|cce|hcs)\b|\bobs[_-]")
ALLOW = re.compile(r"^infra/providers/[a-z0-9][a-z0-9-]*/|^clusters/_template/providers/[a-z0-9][a-z0-9-]*/"
                   r"|^products/catalyst/bootstrap/api/internal/providers/(?!all/)[a-z0-9-]+/"
                   r"|^products/chargeback/internal/collector/[a-z0-9-]+/")
# The chart folders of the provider packs when no pack.yaml exists (the fixed commit of the known answer).
CHART_ALLOW = ("platform/hcloud-ccm/", "platform/hcloud-csi/", "platform/huawei-evs-csi/",
               "platform/cluster-autoscaler-hcloud/", "platform/velero-hcs/")
KIT = "clusters/_template/bootstrap-kit/"
LEGACY_FILES = (KIT + "34a-bp-velero-hcs.yaml", KIT + "50-cluster-autoscaler.yaml", KIT + "55-bp-hcloud-ccm.yaml",
                KIT + "55a-bp-hcloud-csi.yaml", KIT + "55b-bp-huawei-evs-csi.yaml",
                ".github/workflows/infra-hetzner-tofu.yaml", ".github/workflows/infra-huawei-tofu.yaml",
                ".github/workflows/preflight-crossplane-hcloud.yaml")
LEGACY_DIRS = ("clusters/_template/infrastructure/hetzner/", "clusters/_template/infrastructure/providers/hetzner/",
               "clusters/_template/sovereign-tls-hetzner/", "products/catalyst/bootstrap/api/internal/hetzner/",
               "products/catalyst/bootstrap/api/internal/objectstorage/hetzner/")

PACK = re.compile(r"^infra/providers/([a-z0-9][a-z0-9-]*)/pack\.yaml$")
TOKEN = re.compile(r"^[a-z0-9][a-z0-9.-]*$")
CHART = re.compile(r"^[a-z0-9]([a-z0-9-]*[a-z0-9])?$")

class PackError(Exception):
    """A pack.yaml cannot be read, or its spec.leakTokens or spec.charts is not a list of names."""

def scanned(path):
    return (path.endswith(EXTENSIONS) and not path.endswith(SKIP_SUFFIXES) and not path.startswith("docs/")
            and not SKIP_SEGMENTS.intersection(path.split("/")[:-1]))

def kind(path, charts=CHART_ALLOW):
    if ALLOW.match(path) or path.startswith(charts):
        return "allow"
    return "legacy" if path in LEGACY_FILES or path.startswith(LEGACY_DIRS) else "shared"

def count_lines(text, tier1=TIER1):
    """(lines, comment lines) that name a provider."""
    hits = [l for l in text.split("\n") if tier1.search(l) or TIER2.search(l)]
    comments = sum(1 for l in hits if l.lstrip().startswith(COMMENT))
    return len(hits) - comments, comments

def read_text(repo, path):
    full = os.path.join(repo, path)
    if os.path.isfile(full) and not os.path.islink(full):
        return open(full, "rb").read().decode("utf-8", "replace")

def pack_rules(repo, paths, reader=read_text):
    """(tier-1 expression, chart folders) from the pack.yaml files among paths; the built-in rules when none."""
    packs = sorted(p for p in paths if PACK.match(p))
    if not packs:
        return TIER1, CHART_ALLOW
    try:
        import yaml
    except ImportError:
        raise PackError("PyYAML is needed to read " + packs[0])
    tokens, charts = set(), set()
    for path in packs:
        text = reader(repo, path)
        try:
            doc = yaml.safe_load(text) if text is not None else None
        except yaml.YAMLError as e:
            raise PackError(f"{path}: not YAML: {e}")
        spec = doc.get("spec") if isinstance(doc, dict) else None
        spec = spec if isinstance(spec, dict) else {}
        words, folders = spec.get("leakTokens"), spec.get("charts", [])
        if not isinstance(words, list) or not words or not all(isinstance(w, str) and TOKEN.match(w) for w in words):
            raise PackError(f"{path}: spec.leakTokens is not a list of lower-case words")
        if not isinstance(folders, list) or not all(isinstance(c, str) and CHART.match(c) for c in folders):
            raise PackError(f"{path}: spec.charts is not a list of folder names")
        tokens.update(words)
        charts.update(folders)
    words = sorted((w for w in tokens if not TIER2.search(w.upper())), key=lambda w: (-len(w), w))
    tier1 = TIER1 if not words else re.compile(
        TIER1.pattern + r"|(?<![a-z0-9])(?:" + "|".join(map(re.escape, words)) + r")(?![a-z0-9])", re.IGNORECASE)
    return tier1, tuple(f"platform/{c}/" for c in sorted(charts))

def scan(repo, reader=read_text):
    """{path: (kind, lines, comment lines)} for every scanned file of `git ls-files`."""
    out = subprocess.run(["git", "-C", repo, "ls-files", "-z"], check=True, stdout=subprocess.PIPE).stdout
    paths = [p for p in out.decode("utf-8", "surrogateescape").split("\0") if p]
    tier1, charts = pack_rules(repo, paths, reader)
    texts = {p: reader(repo, p) for p in paths if scanned(p)}
    return {p: (kind(p, charts),) + count_lines(t, tier1) for p, t in texts.items() if t is not None}

def shared_counts(result):
    return {p: v[1] for p, v in result.items() if v[0] == "shared" and v[1] > 0}

def registry():
    """scripts/generator_registry.py, loaded from beside this file."""
    spec = importlib.util.spec_from_file_location(
        "generator_registry", os.path.join(os.path.dirname(os.path.abspath(__file__)), "generator_registry.py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module

def judged_at_source(repo, reader=read_text):
    """({generated file: generator} the ratchet does not count, [notes on listed files it must count])."""
    reg = registry()
    paths = reg.tracked(repo)
    charts = pack_rules(repo, paths, reader)[1]
    try:
        listed = reg.generated_files(repo, paths, reader)
    except reg.RegistryError as e:
        raise PackError(str(e))
    left_out, notes = {}, []
    for path, (generator, sources) in sorted(listed.items()):
        outside = [s for s in sources if not scanned(s) and kind(s, charts) != "allow"]
        if sources and not outside:
            left_out[path] = generator
        else:
            notes.append(f"note {path}: {generator} writes it, but "
                         + (f"its source {outside[0]} is not scanned" if outside else "it declares no source")
                         + "; it is counted")
    return left_out, notes

def gated_counts(repo):
    """(shared counts without the generated files judged at their sources, those files, notes)."""
    left_out, notes = judged_at_source(repo)
    return {p: n for p, n in shared_counts(scan(repo)).items() if p not in left_out}, left_out, notes

def totals_line(rows):
    t = []
    for k in ("shared", "legacy", "allow"):
        t += [f"{k}_lines={sum(v[1] for v in rows if v[0] == k)}",
              f"{k}_files={sum(1 for v in rows if v[0] == k and v[1] > 0)}"]
    return " ".join(t + [f"shared_comment_lines={sum(v[2] for v in rows if v[0] == 'shared')}"])

def read_baseline(repo):
    base = {}
    try:
        lines = open(os.path.join(repo, BASELINE), encoding="utf-8").read().splitlines()
    except OSError as e:
        sys.exit(f"{BASELINE}: {e}")  # exit code 1
    for n, line in enumerate(lines, 1):
        parts = line.split("\t")
        if len(parts) != 2 or not parts[0] or not parts[1].isdigit():
            print(f"{BASELINE}:{n}: not <path><TAB><count>: {line!r}")
            sys.exit(2)
        base[parts[0]] = int(parts[1])
    return base

def write_baseline(repo, counts):
    with open(os.path.join(repo, BASELINE), "w", encoding="utf-8") as fh:
        fh.write("".join(f"{p}\t{counts[p]}\n" for p in sorted(counts)))

def ratchet(repo, update):
    """--check (exit 1) or --update (exit 2, nothing written) when a shared file is above the baseline or not in it."""
    (counts, left_out, notes), base = gated_counts(repo), read_baseline(repo)
    for p, generator in sorted(left_out.items()):
        print(f"generated {p}: not counted; {generator} writes it and its sources are scanned (ruling 147)")
    for note in notes:
        print(note)
    for p in sorted(p for p in base if counts.get(p, 0) < base[p]):
        print(f"note {p}: {counts.get(p, 0)} lines, the baseline allows {base[p]}; lower it with --update")
    bad = [f"{'REFUSED' if update else 'FAIL'} {p}: {n} lines name a provider; the baseline allows "
           f"{base.get(p, 'none')}" for p, n in sorted(counts.items()) if n > base.get(p, 0)]
    if bad:
        print("\n".join(bad + ["provider-leak update: refused; the baseline is unchanged" if update else
                               "provider-leak check: shared files above the baseline. Move the code into its plug-in "
                               "path or remove it; never raise the baseline."]))
        return 2 if update else 1
    if update:
        write_baseline(repo, counts)
    print(f"provider-leak {'update' if update else 'check'}: ok, {len(counts)} shared files with a count")
    return 0

def report(repo):
    result, tops = scan(repo), {}
    print(totals_line(list(result.values())))
    for p, v in result.items():
        tops.setdefault(p.split("/")[0] if "/" in p else ".", []).append(v)
    for top in sorted(tops):
        print(top, totals_line(tops[top]))
    left_out, notes = judged_at_source(repo)
    for p, generator in sorted(left_out.items()):
        print(f"generated {p} lines={result[p][1] if p in result else 0}: not gated, {generator} writes it")
    for note in notes:
        print(note)
    return 0

def self_test():
    tmp, failed, line = tempfile.mkdtemp(prefix="provider-leaks-"), [], 'if provider == "huawei" {\n'

    def put(files):
        for path, text in files.items():
            os.makedirs(os.path.dirname(os.path.join(tmp, path)), exist_ok=True)
            with open(os.path.join(tmp, path), "w", encoding="utf-8") as fh:
                fh.write(text)
        subprocess.run(["git", "-C", tmp, "add", "-A"], check=True)

    def run(mode):
        return subprocess.run([sys.executable, os.path.abspath(__file__), "--repo", tmp, mode],
                              stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode

    def expect(name, cond):
        print(("ok   " if cond else "FAIL ") + name)
        failed.extend([] if cond else [name])

    try:
        subprocess.run(["git", "init", "-q", tmp], check=True)
        put({"core/shared.go": line, "infra/providers/huawei/plugin.go": line, BASELINE: "",
             "scripts/comment.sh": "# runs on hetzner\necho hi\n"})
        r = scan(tmp)
        expect("counts 1, 0, 0: shared line, plug-in line, comment line", [shared_counts(r).get(p, 0) for p in (
            "core/shared.go", "infra/providers/huawei/plugin.go", "scripts/comment.sh")] == [1, 0, 0])
        expect("--check against an empty baseline exits 1", run("--check") == 1)
        put({"core/tier2.go": "a := ECS\nb := obs_bucket\nc := observe\nd := necessary\n",
             "core/region.go": 'a := "fsn1"\nb := "fsn10"\nc := "212.72.24.20"\nd := HETZNER\n', "docs/a.yaml": line,
             "core/tests/a.go": line, "core/a_test.go": line, "core/a.md": line, "core/_shared/b.go": line,
             "infra/providers/_shared/c.tf": line, "products/catalyst/bootstrap/api/internal/providers/all/a.go": line,
             "products/catalyst/bootstrap/api/internal/providers/huawei/p.go": line,
             "products/chargeback/internal/collector/huawei/c.go": line, "platform/hcloud-ccm/chart/v.yaml": line,
             KIT + "55-bp-hcloud-ccm.yaml": line + "# hcloud\n",
             "clusters/_template/sovereign-tls-hetzner/x.yaml": line})
        r = scan(tmp)
        expect("tier 2 counts ECS and obs_, not observe or necessary", r["core/tier2.go"] == ("shared", 2, 0))
        expect("tier 1 counts fsn1 as a word, 212.72. and any case", r["core/region.go"] == ("shared", 3, 0))
        expect("docs/, a tests segment, _test.go and other extensions are skipped",
               not {"docs/a.yaml", "core/tests/a.go", "core/a_test.go", "core/a.md"} & set(r))
        expect("_shared and providers/all are shared; adapter, collector, chart are allow", [r[p][0] for p in (
            "core/_shared/b.go", "infra/providers/_shared/c.tf", "products/catalyst/bootstrap/api/internal/providers/"
            "all/a.go", "products/catalyst/bootstrap/api/internal/providers/huawei/p.go", "products/chargeback/"
            "internal/collector/huawei/c.go", "platform/hcloud-ccm/chart/v.yaml")] == ["shared"] * 3 + ["allow"] * 3)
        expect("the legacy list is counted apart",
               totals_line(list(r.values())).split()[2:4] == ["legacy_lines=2", "legacy_files=2"])
        write_baseline(tmp, shared_counts(r))
        expect("--check with an exact baseline exits 0", run("--check") == 0)
        expect("the baseline is <path><TAB><count>, sorted",
               open(os.path.join(tmp, BASELINE)).read().startswith("core/_shared/b.go\t1\ncore/region.go\t3\n"))
        put({"core/shared.go": line * 2})
        expect("a rise: --check exits 1 and --update exits 2", (run("--check"), run("--update")) == (1, 2))
        put({"core/shared.go": line, "core/new.go": line})
        expect("an added path: --check exits 1 and --update exits 2", (run("--check"), run("--update")) == (1, 2))
        os.remove(os.path.join(tmp, "core/new.go"))
        put({"core/region.go": 'a := "fsn1"\n'})
        expect("a fall: --check exits 0", run("--check") == 0)
        expect("--update lowers the baseline", run("--update") == 0 and read_baseline(tmp)["core/region.go"] == 1)
        put({BASELINE: "core/shared.go 1\n"})
        expect("--check exits 2 on a malformed baseline", run("--check") == 2)
        expect("--report exits 0", run("--report") == 0)
        flow = ("on:\n  pull_request:\n    paths: ['core/in/*.yaml', 'scripts/gen.py']\njobs:\n  drift:\n"
                "    name: regenerate and prove no diff\n    steps:\n      - run: python3 scripts/gen.py --check\n")
        claim = "// Code generated by scripts/gen.py. DO NOT EDIT.\n"
        put({".github/workflows/drift.yaml": flow, "scripts/gen.py": 'OUT = "core/gen/out.go"\n',
             "core/in/a.yaml": line, "core/gen/out.go": claim + line, "core/hand.go": line,
             "core/stray.go": claim + line, "scripts/other.py": 'OUT = "core/claim.go"\n',
             "core/claim.go": "// Code generated by scripts/other.py. DO NOT EDIT.\n" + line})
        counts, left_out, _ = gated_counts(tmp)
        expect("a generated file with a provider name is not counted; its source is",
               left_out == {"core/gen/out.go": "scripts/gen.py"} and "core/gen/out.go" not in counts
               and counts.get("core/in/a.yaml") == 1)
        expect("the same line in a hand-written shared file is counted", counts.get("core/hand.go") == 1)
        expect("a file that claims a generator the registry does not run is counted", counts.get("core/claim.go") == 1)
        expect("a file that claims a registered generator which does not name it is counted",
               counts.get("core/stray.go") == 1)
        write_baseline(tmp, counts)
        expect("--check passes with no baseline line for the generated file", run("--check") == 0)
        put({".github/workflows/drift.yaml": flow.replace("'scripts/gen.py'", "'scripts/gen.py', 'core/in/*.md'"),
             "core/in/b.md": line})
        counts, left_out, notes = gated_counts(tmp)
        expect("a generated file with a source that is not scanned is counted, with a note",
               counts.get("core/gen/out.go") == 1 and not left_out and len(notes) == 1)
        expect("--check then exits 1", run("--check") == 1)
        put({BASELINE: "", "core/pack.go": "a := Acmecloud_id\nb := acmecloudy\nc := obs\n",
             "platform/acme-csi/v.yaml": "a := acmecloud\n"})
        expect("without a pack.yaml a pack token counts nothing", scan(tmp)["core/pack.go"] == ("shared", 0, 0))
        put({"infra/providers/acme/pack.yaml": "spec:\n  leakTokens: [acmecloud, obs]\n  charts: [acme-csi]\n"})
        r = scan(tmp)
        expect("a pack token counts as a word in any case; an acronym of tier 2 keeps its exact case",
               r["core/pack.go"] == ("shared", 1, 0))
        expect("with a pack.yaml its spec.charts replace the built-in chart folders",
               (r["platform/acme-csi/v.yaml"][0], r[CHART_ALLOW[0] + "chart/v.yaml"][0]) == ("allow", "shared"))
        expect("a pack token in a comment line is reported, not gated",
               count_lines("# acmecloud\n", pack_rules(tmp, ["infra/providers/acme/pack.yaml"])[0]) == (0, 1))
        put({"infra/providers/acme/pack.yaml": "spec:\n  charts: [acme-csi]\n"})
        expect("a pack.yaml without spec.leakTokens: --check and --report exit 2",
               (run("--check"), run("--report")) == (2, 2))
        put({"infra/providers/acme/pack.yaml": "spec:\n  leakTokens: [acmecloud]\n  charts: [Acme CSI]\n"})
        expect("a pack.yaml whose spec.charts is not a list of folder names: --check exits 2", run("--check") == 2)
    finally:
        shutil.rmtree(tmp, ignore_errors=True)
    print(f"self-test: {len(failed)} case(s) failed" if failed else "self-test: every case passed")
    return 1 if failed else 0

def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--repo", default=os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
    mode = ap.add_mutually_exclusive_group()
    for m in ("--check", "--update", "--report", "--self-test"):
        mode.add_argument(m, action="store_true")
    a = ap.parse_args()
    if a.self_test:
        return self_test()
    try:
        return report(a.repo) if a.report else ratchet(a.repo, a.update)
    except PackError as e:
        print(f"provider-leak: {e}")
        return 2

if __name__ == "__main__":
    sys.exit(main())
