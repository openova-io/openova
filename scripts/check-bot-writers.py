#!/usr/bin/env python3
"""check-bot-writers.py - every job that pushes to main pushes with the release-writer key.

Usage, inside the repository: python3 scripts/check-bot-writers.py [--base <commit>] | --self-test
Exit 0: the rules hold; every job that pushes to main is printed. 1: a rule is broken, or a file it
reads is missing or malformed; one line per finding. 2: wrong usage. It reads every workflow and local
composite action. A script is a step's run text without its "#" lines, a line ending in a backslash
joined to the next. A key step has "uses: ./.github/actions/use-release-writer". A push step's script
holds a git push that names main in a refspec or names no refspec, or calls push-chart-version-bump.sh;
or the step uses a local action that pushes. An action pushes without the key when one of its push
steps has no key step before it.
  key      In a job, every push step has a key step before it. A step that uses an action that
           pushes without the key is exempt only in a job named in the list.
  order    In a listed job, no key step comes before such a step: its push would run under the key,
           and its commit does not hold [skip ci].
  skip-ci  Every git commit in a job or action that has a key step, and in push-chart-version-bump.sh,
           holds [skip ci]: a push with the key starts the push workflows, and [skip ci] stops that.
  list     docs/ledger/bot-writers.txt names jobs, "<workflow path> <job id>" per line, "#" starts a
           comment line. A line for a job without an exempt step fails.
  base     (with --base <commit>) The list holds no line that the list at <commit> lacks. When the
           list does not exist at <commit>, the rule passes.
"""
import glob
import os
import re
import subprocess
import sys
import tempfile

LIST = "docs/ledger/bot-writers.txt"
KEY_ACTION = "./.github/actions/use-release-writer"
PUSH_SCRIPT = "scripts/push-chart-version-bump.sh"
SKIP = "[skip ci]"
GIT = r"\bgit(?:\s+-[cC]\s+\S+)*\s+"
PUSH = re.compile(GIT + r"push\b([^;&|)\n]*)")
COMMIT = re.compile(GIT + r"commit\b[^\n]*")


class Bad(Exception):
    """A file the rules read cannot be read. The rules then fail: they never pass on missing evidence."""


def script(text):
    kept = [ln for ln in str(text or "").split("\n") if not ln.lstrip().startswith("#")]
    return re.sub(r"\s*\\\n\s*", " ", "\n".join(kept))


def pushes_main(text):
    if "push-chart-version-bump.sh" in text:
        return True
    for m in PUSH.finditer(text):
        refs = [t.strip("\"'") for t in m.group(1).split() if not t.startswith("-")][1:]
        if not refs or any(re.search(r"(^|[:/])main$", r) for r in refs):
            return True
    return False


def commits_without_skip(steps):
    return [m.group(0).strip() for s in steps for m in COMMIT.finditer(script(s.get("run"))) if SKIP not in m.group(0)]


def load(root, rel):
    import yaml  # imported here so that a missing library is one finding, not a crash
    try:
        with open(os.path.join(root, rel), encoding="utf-8") as f:
            data = yaml.safe_load(f)
    except (OSError, UnicodeDecodeError, yaml.YAMLError) as e:
        raise Bad("%s cannot be read: %s" % (rel, str(e).split("\n")[0]))
    if not isinstance(data, dict):
        raise Bad("%s is not a YAML mapping" % rel)
    return data


def steps_of(unit, rel):
    steps = unit.get("steps") or []
    if not isinstance(steps, list) or not all(isinstance(s, dict) for s in steps):
        raise Bad("%s: the steps are not a list of mappings" % rel)
    return steps


class Repo:
    def __init__(self, root):
        self.root, self.memo = root, {}

    def action(self, uses):
        """The steps of the local composite action that uses names, or None."""
        if not isinstance(uses, str) or not uses.startswith("./.github/actions/"):
            return None
        for name in ("action.yaml", "action.yml"):
            rel = os.path.normpath(uses[2:].rstrip("/") + "/" + name)
            if os.path.isfile(os.path.join(self.root, rel)):
                runs = load(self.root, rel).get("runs") or {}
                return steps_of(runs, rel) if runs.get("using") == "composite" else None
        raise Bad("%s names no action file" % uses)

    def kinds(self, uses):
        if uses not in self.memo:
            self.memo[uses] = []
            self.memo[uses] = self.walk(self.action(uses) or [])
        return self.memo[uses]

    def walk(self, steps):
        """One word per push step: keyed or unkeyed; for a call of a pushing action: keyed-call when the
        action pushes with the key, else exempt-call, or late-call when a key step comes before it."""
        out, keyed = [], False
        for s in steps:
            inner = self.kinds(s.get("uses")) if s.get("uses") else []
            if s.get("uses") == KEY_ACTION:
                keyed = True
            elif inner:
                bare = any(k not in ("keyed", "keyed-call") for k in inner)
                out.append(("late-call" if keyed else "exempt-call") if bare else "keyed-call")
            elif pushes_main(script(s.get("run"))):
                out.append("keyed" if keyed else "unkeyed")
        return out


def read_list(text, where):
    jobs = []
    for n, ln in enumerate(text.split("\n"), 1):
        if ln.strip() and not ln.startswith("#"):
            if len(ln.split()) != 2 or ln != ln.strip():
                raise Bad("%s line %d must be '<workflow path> <job id>'" % (where, n))
            jobs.append(tuple(ln.split()))
    return jobs


def git(root, *args):
    r = subprocess.run(("git",) + args, cwd=root, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if r.returncode != 0:
        raise Bad("git %s failed: %s" % (" ".join(args), r.stderr.decode("utf-8", "replace").strip()))
    return r.stdout.decode("utf-8", "replace")


def check(root, base):
    repo, out, info, exempt = Repo(root), [], [], set()
    try:
        with open(os.path.join(root, LIST), encoding="utf-8") as f:
            listed = read_list(f.read(), LIST)
    except (OSError, UnicodeDecodeError):
        raise Bad("%s is missing or cannot be read" % LIST)
    for rel in sorted(glob.glob(".github/workflows/*.y*ml", root_dir=root)):
        for job, body in sorted((load(root, rel).get("jobs") or {}).items()):
            if not isinstance(body, dict) or "steps" not in body:
                continue
            steps, where = steps_of(body, rel), "%s job %s" % (rel, job)
            kinds = repo.walk(steps)
            if "unkeyed" in kinds:
                out.append("%s pushes to main without the release-writer key: add a step '- uses: %s' before the push" % (where, KEY_ACTION))
            if "late-call" in kinds:
                out.append("%s calls an action that pushes with the workflow token after its key step: move the key step after that call" % where)
            if "exempt-call" in kinds:
                exempt.add((rel, job))
                if (rel, job) not in listed:
                    out.append("%s calls an action that pushes without the key, and %s does not name it" % (where, LIST))
            if any(s.get("uses") == KEY_ACTION for s in steps):
                out += ["%s commits without %s: %s" % (where, SKIP, c) for c in commits_without_skip(steps)]
            if kinds:
                how = ["the release-writer key"] if {"keyed", "keyed-call"} & set(kinds) else []
                info.append("%s: %s" % (where, " and ".join(how + (["the workflow token, listed"] if (rel, job) in exempt else []))))
    pushing = len(info)
    for rel in sorted(glob.glob(".github/actions/*/action.y*ml", root_dir=root)):
        steps = repo.action("./" + os.path.dirname(rel)) or []
        if any(s.get("uses") == KEY_ACTION for s in steps):
            out += ["%s commits without %s: %s" % (rel, SKIP, c) for c in commits_without_skip(steps)]
    if os.path.isfile(os.path.join(root, PUSH_SCRIPT)):
        with open(os.path.join(root, PUSH_SCRIPT), encoding="utf-8") as f:
            out += ["%s commits without %s: %s" % (PUSH_SCRIPT, SKIP, c) for c in commits_without_skip([{"run": f.read()}])]
    out += ["%s names %s job %s, which calls no action that pushes without the key: remove the line" % (LIST, r, j)
            for (r, j) in listed if (r, j) not in exempt]
    if base is not None:
        commit = git(root, "rev-parse", "--verify", "--quiet", base + "^{commit}").strip()
        if git(root, "ls-tree", commit, "--", LIST).strip():
            before = read_list(git(root, "show", "%s:%s" % (commit, LIST)), "%s at %s" % (LIST, base))
            out += ["%s gained the line '%s %s'; the list may only shrink" % (LIST, r, j) for (r, j) in listed if (r, j) not in before]
        else:
            info.append("%s has no %s to compare with" % (base, LIST))
    info.append("%d jobs push to main; %d of them push with the workflow token and are listed" % (pushing, len(exempt)))
    return out, info


def run(root, base, say=print):
    try:
        findings, info = check(root, base)
    except Bad as e:
        findings, info = [str(e)], []
    except ImportError:
        findings, info = ["the Python YAML library is not installed"], []
    for line in findings or info:
        say("bot-writers: %s" % line)
    say("bot-writers: FAILED, %d finding(s)" % len(findings) if findings else "bot-writers: ok")
    return 1 if findings else 0


def self_test():
    """Builds small trees in a temporary folder and shows that every rule can fail and can pass."""
    env = dict((k, v) for k, v in os.environ.items() if not k.startswith("GIT_"))
    env.update(GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_SYSTEM=os.devnull, GIT_AUTHOR_NAME="t", GIT_AUTHOR_EMAIL="t@example.invalid",
               GIT_COMMITTER_NAME="t", GIT_COMMITTER_EMAIL="t@example.invalid")
    passed, failed = [], []
    W, A = ".github/workflows/w.yaml", ".github/actions/bump/action.yaml"
    key = "      - uses: %s\n        with:\n          key: x\n" % KEY_ACTION
    push = "      - run: |\n          git commit -m \"deploy: x [skip ci]\"\n          git push origin HEAD:main\n"
    call = "      - uses: ./.github/actions/bump\n"
    bump = "runs:\n  using: composite\n  steps:\n    - shell: bash\n      run: git commit -m m && git push origin HEAD:main\n"
    keyed_bump = bump.replace("steps:\n", "steps:\n    - uses: %s\n" % KEY_ACTION)
    keyact = "runs:\n  using: composite\n  steps:\n    - shell: bash\n      run: bash scripts/ci/use-release-writer.sh\n"

    def wf(*steps):
        return "on: push\njobs:\n  j:\n    runs-on: x\n    steps:\n      - uses: actions/checkout@v4\n" + "".join(steps)

    def tree(name, files, listed=""):
        root = os.path.join(tmp, name)
        for path, data in dict({LIST: "# jobs\n" + listed, KEY_ACTION[2:] + "/action.yaml": keyact}, **files).items():
            if data is not None:
                os.makedirs(os.path.dirname(os.path.join(root, path)), exist_ok=True)
                with open(os.path.join(root, path), "w", encoding="utf-8") as f:
                    f.write(data)
        return root

    def commit(root, path):
        for args in (("init", "-q"), ("add", "--", path), ("-c", "commit.gpgsign=false", "commit", "-q", "-m", "s"), ("tag", "before")):
            subprocess.run(("git",) + args, cwd=root, env=env, check=True, stdout=subprocess.DEVNULL)

    def case(name, root, code, text, base=None):
        lines = []
        got = run(root, base, lines.append)
        ok = got == code and text in "\n".join(lines)
        (passed if ok else failed).append(name)
        if not ok:
            print("self-test: WRONG: %s: exit code %d, expected %d, with the text %r. Output:\n  %s" % (name, got, code, text, "\n  ".join(lines)))

    with tempfile.TemporaryDirectory() as tmp:
        case("key: a push after a key step, with [skip ci], passes", tree("k1", {W: wf(key, push)}), 0, "w.yaml job j: the release-writer key")
        case("key: a push to main without a key step fails", tree("k2", {W: wf(push)}), 1, "job j pushes to main without the release-writer key")
        case("key: a key step after the push fails", tree("k3", {W: wf(push, key)}), 1, "without the release-writer key")
        case("key: a bare git push counts as a push to main", tree("k4", {W: wf("      - run: git push\n")}), 1, "without the release-writer key")
        case("key: git -C dir push origin main counts", tree("k5", {W: wf("      - run: git -C d push origin main\n")}), 1, "without the release-writer key")
        case("key: a call of push-chart-version-bump.sh counts", tree("k6", {W: wf("      - run: N=$(scripts/push-chart-version-bump.sh c)\n")}), 1, "without the release-writer key")
        other = "      - run: |\n          # git push origin main\n          git push origin \"$s:refs/heads/rc/1\"\n"
        case("key: a push to another branch and a comment are not pushes to main", tree("k7", {W: wf(other)}), 0, "0 jobs push to main")
        case("key: a missing key action fails", tree("k8", {W: wf(key, push), KEY_ACTION[2:] + "/action.yaml": None}), 1, "names no action file")
        case("skip-ci: a commit without [skip ci] in a keyed job fails", tree("s1", {W: wf(key, push.replace(" [skip ci]", ""))}), 1, "without [skip ci]: git commit -m \"deploy: x\"")
        two = "      - run: |\n          git -c user.name=b \\\n            commit -m \"m\"\n          git push origin main\n"
        case("skip-ci: a commit split over two lines is read as one", tree("s2", {W: wf(key, two)}), 1, "without [skip ci]: git -c user.name=b commit -m \"m\"")
        case("skip-ci: a commit in push-chart-version-bump.sh without [skip ci] fails", tree("s3", {W: wf(key, push), PUSH_SCRIPT: "git commit -m x\n"}), 1, "bump.sh commits without")
        case("skip-ci: an action with a key step and a commit without [skip ci] fails", tree("s4", {W: wf(call), A: keyed_bump}), 1, "action.yaml commits without")
        case("key: a call of an action that pushes after its own key step passes", tree("s5", {W: wf(call), A: keyed_bump.replace("-m m", "-m \"m [skip ci]\"")}), 0, "1 jobs push to main; 0 of")
        case("list: a call of an action that pushes without the key fails in a job not listed", tree("l1", {W: wf(call), A: bump}), 1, "and %s does not name it" % LIST)
        case("list: the same call passes in a listed job", tree("l2", {W: wf(call), A: bump}, W + " j\n"), 0, "job j: the workflow token, listed")
        case("list: a line for a job without such a call fails", tree("l3", {W: wf(key, push)}, W + " j\n"), 1, "remove the line")
        case("order: a key step before such a call fails", tree("l4", {W: wf(key, call), A: bump}, W + " j\n"), 1, "after its key step")
        case("order: such a call before the key step, then a keyed push, passes", tree("l5", {W: wf(call, key, push), A: bump}, W + " j\n"), 0, "1 of them push")
        for n, text in enumerate((" %s j\n" % W, "%s\n" % W, "%s j k\n" % W)):
            case("list: the malformed line %r fails" % text, tree("m%d" % n, {W: wf(key, push)}, text), 1, "must be '<workflow path> <job id>'")
        case("list: a missing list fails", tree("m8", {W: wf(key, push), LIST: None}), 1, "is missing or cannot be read")
        case("read: a workflow that is not YAML fails", tree("m9", {W: "jobs: [\n"}), 1, "w.yaml cannot be read")
        case("read: a call of a local action without an action file fails", tree("m10", {W: wf(call)}), 1, "names no action file")
        r = tree("b1", {W: wf(call), A: bump}, W + " j\n")
        commit(r, ".")
        case("base: an equal list passes", r, 0, "1 of them push", base="before")
        tree("b1", {".github/workflows/v.yaml": wf(call)}, W + " j\n.github/workflows/v.yaml j\n")
        case("base: a line the base lacks fails", r, 1, "gained the line '.github/workflows/v.yaml j'", base="before")
        case("base: the same tree without --base passes", r, 0, "2 of them push")
        case("base: an unknown commit fails", r, 1, "git rev-parse", base="no-such-commit")
        r = tree("b2", {W: wf(key, push)})
        commit(r, W)
        case("base: a base without the list passes", r, 0, "before has no %s to compare with" % LIST, base="before")
    print("self-test: FAILED, %d of %d cases wrong" % (len(failed), len(failed) + len(passed)) if failed or not passed else "self-test: %d cases passed" % len(passed))
    return 1 if failed or not passed else 0


def main(argv):
    if argv == ["--self-test"]:
        return self_test()
    base = argv[1] if len(argv) == 2 and argv[0] == "--base" and argv[1] else None
    if argv and base is None:
        print("usage: python3 scripts/check-bot-writers.py [--base <commit>] | --self-test", file=sys.stderr)
        return 2
    try:
        return run(git(os.getcwd(), "rev-parse", "--show-toplevel").rstrip("\n"), base)
    except (Bad, OSError) as e:
        print("bot-writers: %s" % e)
        return 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
