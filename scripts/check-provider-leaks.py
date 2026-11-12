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
"""
import argparse, os, re, shutil, subprocess, sys, tempfile

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
# The chart folders of the provider packs, until each pack.yaml lists them under spec.charts.
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

def scanned(path):
    return (path.endswith(EXTENSIONS) and not path.endswith(SKIP_SUFFIXES) and not path.startswith("docs/")
            and not SKIP_SEGMENTS.intersection(path.split("/")[:-1]))

def kind(path):
    if ALLOW.match(path) or path.startswith(CHART_ALLOW):
        return "allow"
    return "legacy" if path in LEGACY_FILES or path.startswith(LEGACY_DIRS) else "shared"

def count_lines(text):
    """(lines, comment lines) that name a provider."""
    hits = [l for l in text.split("\n") if TIER1.search(l) or TIER2.search(l)]
    comments = sum(1 for l in hits if l.lstrip().startswith(COMMENT))
    return len(hits) - comments, comments

def read_text(repo, path):
    full = os.path.join(repo, path)
    if os.path.isfile(full) and not os.path.islink(full):
        return open(full, "rb").read().decode("utf-8", "replace")

def scan(repo, reader=read_text):
    """{path: (kind, lines, comment lines)} for every scanned file of `git ls-files`."""
    out = subprocess.run(["git", "-C", repo, "ls-files", "-z"], check=True, stdout=subprocess.PIPE).stdout
    texts = {p: reader(repo, p) for p in out.decode("utf-8", "surrogateescape").split("\0") if p and scanned(p)}
    return {p: (kind(p),) + count_lines(t) for p, t in texts.items() if t is not None}

def shared_counts(result):
    return {p: v[1] for p, v in result.items() if v[0] == "shared" and v[1] > 0}

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
    counts, base = shared_counts(scan(repo)), read_baseline(repo)
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
    return report(a.repo) if a.report else ratchet(a.repo, a.update)

if __name__ == "__main__":
    sys.exit(main())
