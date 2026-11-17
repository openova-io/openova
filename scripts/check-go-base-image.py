#!/usr/bin/env python3
"""check-go-base-image.py - the golang base image of a container file is not older than its module.

Inside a golang image GOTOOLCHAIN is local, so `go mod download` refuses a module whose `go` line
names a newer Go than the image holds ("go.mod requires go >= 1.25.0 (running go 1.23.12;
GOTOOLCHAIN=local)"). A pull request does not see this: its run builds with setup-go on the
runner, which downloads the newer Go by itself, and it skips the image build. So a dependency
update that raises a `go` line merged green, and the image build was red on main afterwards.
This guard compares the two on the pull request itself, without building an image.

What it reads:
  container files  every tracked file, and every new file Git does not ignore, whose name starts
                   with Containerfile or Dockerfile. Comment lines are skipped and lines that end
                   in a backslash are joined.
  golang stages    a FROM line whose image is golang: the last part of the repository path is
                   golang, with or without a registry in front (docker.io/library/golang). Options
                   such as --platform=... are skipped. ${NAME} and $NAME in the image are replaced
                   by the default of an `ARG NAME=<default>` line before the first FROM. A
                   build argument that a workflow passes is not read: the default is what a
                   plain build of the file uses.
  base version     the digits at the start of the tag: 1 in golang:1-alpine, 1.25 in
                   golang:1.25-alpine, 1.25.14 in golang:1.25.14-bookworm@sha256:<digest>.
  module           the go.mod files the stage copies with COPY or ADD, relative to the build
                   context: a source whose last part is go.mod, and the go.mod at the top of a
                   copied folder (such as a module that a replace directive names). The build
                   context is the `context:` of every workflow step whose `file:` names the
                   container file, both literal. When no workflow names it, it is the nearest
                   folder, from the file's own up to the repository root, that holds the source.
                   A stage that copies no go.mod but the whole context (COPY . <dir>) is read
                   from its last WORKDIR: the nearest go.mod at or above that folder of the
                   context. Any other stage is read from the nearest go.mod at or above the
                   container file's folder.

The rule:
  1. The base version is at least the highest `go` line of the stage's go.mod files. A base tag
     with a major only (golang:1-alpine) is the newest release of that major, so only the major
     is compared; a base tag with major.minor only (golang:1.25-alpine) is the newest patch of
     that release, so only major.minor is compared; a base tag with a patch
     (golang:1.25.0-alpine) is compared in full.

A stage whose go.mod is copied from another stage (COPY --from=<stage> ... go.mod) builds a
module that is not in the repository. It is printed as `skip` and not compared.

The guard never passes on what it could not measure. Exit code 2, and nothing is judged, when:
a container file or a workflow cannot be read; a variable of a golang FROM has no ARG default;
a golang tag starts with no version; a go.mod the stage copies is not in its build context; a
go.mod has no `go` line; no go.mod is found for a golang stage; or the tree has no golang stage.

Usage (from the repository root):
  python3 scripts/check-go-base-image.py              judge the tree
  python3 scripts/check-go-base-image.py --self-test  show that every rule can fail and can pass
Exit codes: 0 pass, 1 a base is older than its module, 2 the tree cannot be measured.
"""
import glob
import os
import re
import subprocess
import sys
import tempfile

import yaml

FROM_RE = re.compile(r"^FROM\s+(.*)$", re.IGNORECASE)
ARG_RE = re.compile(r"^ARG\s+([A-Za-z_][A-Za-z0-9_]*)(?:=(.*))?$", re.IGNORECASE)
COPY_RE = re.compile(r"^(COPY|ADD)\s+(.*)$", re.IGNORECASE)
WORKDIR_RE = re.compile(r"^WORKDIR\s+(.*)$", re.IGNORECASE)
VAR_RE = re.compile(r"\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)")
TAG_VERSION_RE = re.compile(r"^(\d+)(?:\.(\d+)(?:\.(\d+))?)?")
GO_LINE_RE = re.compile(r"^go\s+(\d+)\.(\d+)(?:\.(\d+))?\s*(?://.*)?$")

class SetupError(Exception):
    """The tree cannot be measured."""

def is_container_file(path):
    return os.path.basename(path).startswith(("Containerfile", "Dockerfile"))

def git(root, *args):
    proc = subprocess.run(["git", "-C", root, *args], capture_output=True)
    return proc.returncode, proc.stdout, proc.stderr.decode("utf-8", "replace").strip()

def tree_files(root):
    code, out, err = git(root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
    if code != 0:
        raise SetupError(f"cannot list the files of {root}: {err}")
    return sorted({p for p in out.decode("utf-8", "replace").split("\0") if p})

def read(root, path):
    try:
        with open(os.path.join(root, path), encoding="utf-8", errors="replace") as handle:
            return handle.read()
    except OSError as exc:
        raise SetupError(f"cannot read {path}: {exc.strerror}") from None

def instructions(text):
    """(line number, instruction) with comments dropped and continued lines joined."""
    out, buf, start = [], "", 0
    for number, raw in enumerate(text.splitlines(), 1):
        line = raw.strip()
        if not buf and (not line or line.startswith("#")):
            continue
        if not buf:
            start = number
        if line.endswith("\\"):
            buf += line[:-1] + " "
            continue
        buf += line
        out.append((start, buf.strip()))
        buf = ""
    if buf:
        out.append((start, buf.strip()))
    return out

def words(rest):
    """The words of a COPY or ADD after its options, in shell or JSON form."""
    rest = rest.strip()
    opts = []
    while rest.startswith("--"):
        word, _, rest = rest.partition(" ")
        opts.append(word)
        rest = rest.strip()
    if rest.startswith("["):
        try:
            parts = yaml.safe_load(rest)
        except yaml.YAMLError:
            parts = None
        if isinstance(parts, list) and all(isinstance(p, str) for p in parts):
            return opts, parts
    return opts, rest.split()

def normal(path):
    path = os.path.normpath(path.lstrip("/")) if path not in ("", ".", "./") else ""
    return "" if path == "." else path

def version_text(version):
    return ".".join(str(n) for n in version if n is not None)

def golang_image(image):
    """(repository, tag) of a golang image, or None for another image."""
    name = image.split("@", 1)[0]
    repo, tag = name, ""
    if ":" in name.rsplit("/", 1)[-1]:
        repo, tag = name.rsplit(":", 1)
    if repo.rsplit("/", 1)[-1].lower() != "golang":
        return None
    return repo, tag

def go_line(root, path):
    for line in read(root, path).splitlines():
        match = GO_LINE_RE.match(line.strip())
        if match:
            return tuple(int(g) if g is not None else None for g in match.groups())
    raise SetupError(f"{path} has no go line")

def workflow_contexts(root, files):
    """{container file: set of build contexts} from the literal file/context pairs of workflows."""
    found = {}
    for path in files:
        if not (path.startswith(".github/workflows/") and path.count("/") == 2
                and path.endswith((".yml", ".yaml"))):
            continue
        try:
            doc = yaml.safe_load(read(root, path))
        except yaml.YAMLError as exc:
            raise SetupError(f"cannot read {path}: {exc}") from None
        jobs = doc.get("jobs") if isinstance(doc, dict) else None
        for job in (jobs or {}).values():
            for step in (job.get("steps") or []) if isinstance(job, dict) else []:
                given = step.get("with") if isinstance(step, dict) else None
                if not isinstance(given, dict):
                    continue
                cfile, ctx = given.get("file"), given.get("context")
                if not (isinstance(cfile, str) and isinstance(ctx, str)) or "${{" in cfile + ctx:
                    continue
                found.setdefault(normal(cfile), set()).add((normal(ctx), path))
    return found

def ancestors(path):
    """The folders from the folder of path up to the repository root."""
    out, folder = [], os.path.dirname(path)
    while True:
        out.append(folder)
        if not folder:
            return out
        folder = os.path.dirname(folder)

def matches(root, ctx, source):
    rel = normal(os.path.join(ctx, source.lstrip("/")))
    if any(ch in source for ch in "*?["):
        return sorted(normal(os.path.relpath(p, root)) for p in glob.glob(os.path.join(root, rel))
                      if os.path.isfile(p))
    return [rel] if os.path.isfile(os.path.join(root, rel)) else []

def nearest_go_mod(root, folder, stop=""):
    """The nearest go.mod at or above folder, not above stop."""
    while True:
        candidate = normal(os.path.join(folder, "go.mod"))
        if os.path.isfile(os.path.join(root, candidate)):
            return candidate
        if folder == stop or not folder:
            return None
        folder = os.path.dirname(folder)

def stages(path, text):
    """Every golang stage of a container file, as a dict."""
    args, seen_from, out, current = {}, False, [], None
    for number, instr in instructions(text):
        match = FROM_RE.match(instr)
        if match:
            seen_from = True
            parts = [w for w in match.group(1).split() if not w.startswith("--")]
            if not parts:
                current = None
                continue
            image = parts[0]
            for var in VAR_RE.finditer(image):
                name = var.group(1) or var.group(2)
                if args.get(name) is None:
                    raise SetupError(f"{path}:{number}: the FROM image {image} uses ${name}, "
                                     "and no ARG before the first FROM gives it a default")
            image = VAR_RE.sub(lambda m: args[m.group(1) or m.group(2)], image)
            golang = golang_image(image)
            if golang is None:
                current = None
                continue
            version = TAG_VERSION_RE.match(golang[1])
            if not version:
                raise SetupError(f"{path}:{number}: the golang tag of {image} starts with no version")
            current = {"line": number, "image": image,
                       "version": tuple(int(g) if g is not None else None for g in version.groups()),
                       "sources": [], "folders": [], "from_stage": None, "whole": None, "workdir": "/go",
                       "last_workdir": None}
            out.append(current)
            continue
        match = ARG_RE.match(instr)
        if match and not seen_from:
            default = match.group(2)
            args[match.group(1)] = default.strip().strip("\"'") if default is not None else None
            continue
        if current is None:
            continue
        match = WORKDIR_RE.match(instr)
        if match:
            current["workdir"] = os.path.normpath(os.path.join(current["workdir"], match.group(1).strip()))
            current["last_workdir"] = current["workdir"]
            continue
        match = COPY_RE.match(instr)
        if not match:
            continue
        opts, parts = words(match.group(2))
        if len(parts) < 2:
            continue
        sources, dest = parts[:-1], parts[-1]
        stage_from = next((o.split("=", 1)[1] for o in opts if o.lower().startswith("--from=")), None)
        mods = [s for s in sources if os.path.basename(s.rstrip("/")) == "go.mod"]
        if stage_from is not None:
            if mods and current["from_stage"] is None:
                current["from_stage"] = stage_from
            continue
        current["sources"].extend((number, s) for s in mods)
        current["folders"].extend(s for s in sources if s not in mods and not any(ch in s for ch in "*?["))
        if any(s in (".", "./") for s in sources):
            current["whole"] = os.path.normpath(os.path.join(current["workdir"], dest))
            current["last_workdir"] = None
    return out

def module_files(root, path, stage, contexts):
    """The go.mod files of a stage, or None for a stage whose module comes from another stage."""
    if stage["sources"]:
        found, ctxs = [], set()
        for number, source in stage["sources"]:
            if contexts:
                for ctx, workflow in sorted(contexts):
                    hit = matches(root, ctx, source)
                    if not hit:
                        raise SetupError(f"{path}:{number}: {source} is not in the build context "
                                         f"'{ctx or '.'}' that {workflow} gives")
                    found.extend(hit)
                    ctxs.add(ctx)
            else:
                ctx = next((c for c in ancestors(path) if matches(root, c, source)), None)
                if ctx is None:
                    raise SetupError(f"{path}:{number}: {source} is in no folder from the file's own up to the root")
                found.extend(matches(root, ctx, source))
                ctxs.add(ctx)
        for ctx in sorted(ctxs):
            for folder in stage["folders"]:
                candidate = normal(os.path.join(ctx, folder.lstrip("/"), "go.mod"))
                if os.path.isdir(os.path.join(root, ctx, folder.lstrip("/"))) and os.path.isfile(os.path.join(root, candidate)):
                    found.append(candidate)
        return sorted(set(found))
    if stage["from_stage"] is not None:
        return None
    if stage["whole"] is not None:
        workdir = stage["last_workdir"] or stage["whole"]
        rel = os.path.relpath(workdir, stage["whole"])
        if not rel.startswith(".."):
            ctxs = sorted(c for c, _ in contexts) or [os.path.dirname(path)]
            found = [nearest_go_mod(root, normal(os.path.join(c, rel)), stop=c) for c in ctxs]
            if all(found):
                return sorted(set(found))
    near = nearest_go_mod(root, os.path.dirname(path))
    if near is None:
        raise SetupError(f"{path}:{stage['line']}: no go.mod is found for the golang stage {stage['image']}")
    return [near]

def padded(version):
    return (version[0], version[1] or 0, version[2] or 0)

def older(base, need):
    """True when the base version is older than the go line."""
    if base[1] is None:
        return base[0] < need[0]
    if base[2] is None:
        return base[:2] < need[:2]
    return padded(base) < padded(need)

def run(root):
    """(exit code, printed lines)."""
    lines, failed, total, skipped, files_seen = [], 0, 0, 0, 0
    try:
        files = tree_files(root)
        contexts = workflow_contexts(root, files)
        judged = []
        for path in files:
            if not is_container_file(path):
                continue
            if not os.path.isfile(os.path.join(root, path)):
                raise SetupError(f"cannot read {path}: it is not a regular file")
            found = stages(path, read(root, path))
            if found:
                files_seen += 1
            for stage in found:
                mods = module_files(root, path, stage, contexts.get(path, set()))
                need = None if mods is None else max((go_line(root, m) for m in mods), key=padded)
                judged.append((path, stage, mods, need))
        if not judged:
            raise SetupError("no container file of the tree has a golang stage, so nothing was measured")
    except SetupError as exc:
        return 2, [f"go-base-image: ERROR {exc}"]
    for path, stage, mods, need in judged:
        where = f"{path}:{stage['line']}"
        base = version_text(stage["version"])
        if mods is None:
            lines.append(f"go-base-image: skip {where} the go.mod comes from the stage {stage['from_stage']}, "
                         "not from the repository")
            skipped += 1
            continue
        total += 1
        need_text = version_text(need)
        if older(stage["version"], need):
            failed += 1
            lines.append(f"go-base-image: FAIL {where} golang {base} is older than go {need_text} of {', '.join(mods)}")
        else:
            lines.append(f"go-base-image: ok {where} golang {base} builds go {need_text} of {', '.join(mods)}")
    if failed:
        lines.append(f"go-base-image: FAIL {failed} of {total} judged golang stages have a base older than their module")
        return 1, lines
    lines.append(f"go-base-image: ok, {total} golang stages judged and {skipped} skipped, in {files_seen} container files")
    return 0, lines

# ---------------------------------------------------------------------------------------------
# Self-test. It builds trees of its own in a temporary folder and runs the same functions.
# ---------------------------------------------------------------------------------------------

def _repo(tmp, name, files):
    root = os.path.join(tmp, name)
    os.makedirs(root)
    code, _, err = git(root, "init", "-q")
    if code != 0:
        raise RuntimeError(f"self-test could not run git init: {err}")
    for path, text in files.items():
        full = os.path.join(root, path)
        os.makedirs(os.path.dirname(full), exist_ok=True)
        with open(full, "w", encoding="utf-8") as handle:
            handle.write(text)
    return root

def self_test():
    cases = []
    mod = lambda v: f"module example.com/x\n\ngo {v}\n"
    wf = lambda cfile, ctx: ("on: push\njobs:\n  b:\n    runs-on: x\n    steps:\n"
                             f"      - uses: docker/build-push-action@v6\n        with:\n"
                             f"          context: {ctx}\n          file: {cfile}\n")
    plain = "FROM {img} AS build\nWORKDIR /src\nCOPY go.mod go.sum ./\nRUN go mod download\nFROM scratch\n"
    one = lambda img, v="1.25.0", head="": {"a/Dockerfile": head + plain.format(img=img), "a/go.mod": mod(v)}
    trees = [
        # (name, files, wanted exit code, a text the output must hold)
        ("rule 1 passes when the base equals the go line", one("golang:1.25-alpine"), 0,
         "ok a/Dockerfile:1 golang 1.25 builds go 1.25.0 of a/go.mod"),
        ("rule 1 fails when the base is older than the go line", one("docker.io/library/golang:1.23-alpine"), 1,
         "FAIL a/Dockerfile:1 golang 1.23 is older than go 1.25.0 of a/go.mod"),
        ("rule 1 compares the patch when the tag names one", one("golang:1.25.0-alpine", "1.25.3"), 1,
         "golang 1.25.0 is older than go 1.25.3"),
        ("rule 1 compares the major only when the tag names a major only, and passes",
         one("golang:${GO_VERSION}-alpine", head="ARG GO_VERSION=1\n"), 0, "golang 1 builds go 1.25.0"),
        ("rule 1 compares the major only when the tag names a major only, and fails",
         one("golang:1-alpine", "2.0"), 1, "golang 1 is older than go 2.0"),
        ("rule 1 passes on a digest-pinned tag with a patch", one("golang:1.25.14-bookworm@sha256:" + "b" * 64), 0,
         "golang 1.25.14 builds go 1.25.0"),
        ("rule 1 reads the default of an ARG before the first FROM, and fails",
         one("golang:${GO_VERSION}-bookworm", head="ARG GO_VERSION=1.22\n"), 1, "golang 1.22 is older than go 1.25.0"),
        ("rule 1 reads the default of an ARG before the first FROM, and passes",
         one("golang:$V-bookworm", head="ARG V=1.25\n"), 0, "golang 1.25 builds go 1.25.0"),
        ("rule 1 takes the highest go line of every go.mod the stage copies",
         {"core/x/Containerfile": "FROM golang:1.24-alpine AS b\nCOPY core/x/go.mod core/x/\nCOPY core/y/go.mod core/y/\n",
          "core/x/go.mod": mod("1.22"), "core/y/go.mod": mod("1.25.0")}, 1,
         "golang 1.24 is older than go 1.25.0 of core/x/go.mod, core/y/go.mod"),
        ("rule 1 counts the go.mod at the top of a copied folder",
         {"core/x/Containerfile": "FROM golang:1.25-alpine AS b\nCOPY core/x/go.mod core/x/\nCOPY core/shared/ core/shared/\n",
          "core/x/go.mod": mod("1.25.0"), "core/shared/go.mod": mod("1.26"), "core/shared/a.go": "package a\n"}, 1,
         "golang 1.25 is older than go 1.26 of core/shared/go.mod, core/x/go.mod"),
        ("rule 1 resolves a source in the build context a workflow gives",
         {"core/x/Containerfile": "FROM golang:1.25-alpine\nCOPY core/x/go.mod core/x/\n", "core/x/go.mod": mod("1.25.0"),
          ".github/workflows/b.yaml": wf("core/x/Containerfile", ".")}, 0, "ok core/x/Containerfile:1"),
        ("a source missing from the build context a workflow gives is an error",
         {"core/x/Containerfile": "FROM golang:1.25-alpine\nCOPY go.mod ./\n", "core/x/go.mod": mod("1.25.0"),
          ".github/workflows/b.yaml": wf("core/x/Containerfile", ".")}, 2, "is not in the build context '.'"),
        ("rule 1 finds the build context in a folder above the file",
         {"svc/auth/Containerfile": "FROM golang:1.23-alpine\nCOPY auth/go.mod auth/go.sum auth/\n",
          "svc/auth/go.mod": mod("1.26")}, 1, "golang 1.23 is older than go 1.26 of svc/auth/go.mod"),
        ("rule 1 joins continued lines and skips comments",
         {"a/Dockerfile": "# FROM golang:1.20\nFROM --platform=$BUILDPLATFORM \\\n  golang:1.21-alpine AS b\nCOPY go.mod \\\n  ./\n",
          "a/go.mod": mod("1.22")}, 1, "FAIL a/Dockerfile:2 golang 1.21 is older than go 1.22"),
        ("rule 1 reads a copy of the whole context from the last WORKDIR",
         {"p/Containerfile": "FROM golang:1.23-alpine AS m\nWORKDIR /build\nCOPY . .\nWORKDIR /build/q/cmd\n",
          "q/go.mod": mod("1.25.0"), ".github/workflows/b.yaml": wf("p/Containerfile", ".")}, 1,
         "golang 1.23 is older than go 1.25.0 of q/go.mod"),
        ("rule 1 falls back to the nearest go.mod above the file",
         {"m/c/Dockerfile": "FROM golang:1.25-alpine\nRUN go build ./...\n", "m/go.mod": mod("1.25.0")}, 0,
         "golang 1.25 builds go 1.25.0 of m/go.mod"),
        ("a go.mod copied from another stage is skipped and printed",
         {"a/Containerfile": "FROM alpine AS src\nFROM golang:1.20-alpine AS b\nCOPY --from=src /src/go.mod ./\n"
                             "FROM golang:1.25-alpine\nCOPY go.mod ./\n", "a/go.mod": mod("1.25.0")}, 0,
         "skip a/Containerfile:2 the go.mod comes from the stage src"),
        ("an image that is not golang is not judged", one("golang:1.25", head="FROM node:22 AS ui\nFROM mygolang:1.1\n"), 0,
         "ok, 1 golang stages judged and 0 skipped, in 1 container files"),
        ("an ARG without a default is an error", one("golang:${V}", head="ARG V\n"), 2, "no ARG before the first FROM gives it a default"),
        ("an ARG after the first FROM is not a default", one("golang:${V}", head="FROM alpine\nARG V=1.25\n"), 2, "gives it a default"),
        ("a tag without a version is an error", one("golang:alpine"), 2, "starts with no version"),
        ("an image without a tag is an error", one("golang"), 2, "starts with no version"),
        ("a go.mod without a go line is an error", {**one("golang:1.25"), "a/go.mod": "module example.com/x\n"}, 2, "has no go line"),
        ("a golang stage without any go.mod is an error", {"a/Dockerfile": "FROM golang:1.25\nRUN go version\n"}, 2,
         "no go.mod is found for the golang stage"),
        ("a copied go.mod that does not exist is an error", {"a/Dockerfile": plain.format(img="golang:1.25")}, 2, "go.mod is in no folder"),
        ("a tree without a golang stage is an error", {"a/Dockerfile": "FROM node:22\n"}, 2, "no container file of the tree has a golang stage"),
        ("a workflow that is not YAML is an error", {**one("golang:1.25"), ".github/workflows/b.yaml": "jobs: [\n"}, 2,
         "cannot read .github/workflows/b.yaml"),
    ]
    with tempfile.TemporaryDirectory() as tmp:
        for index, (name, files, want, text) in enumerate(trees):
            code, lines = run(_repo(tmp, f"t{index}", files))
            got = (code, any(text in line for line in lines))
            cases.append((name, got == (want, True), got, (want, True), lines))
        root = _repo(tmp, "link", one("golang:1.25"))
        os.makedirs(os.path.join(root, "b"))
        os.symlink("missing", os.path.join(root, "b", "Dockerfile"))
        code, lines = run(root)
        got = (code, any("is not a regular file" in line for line in lines))
        cases.append(("a container file that cannot be read is an error", got == (2, True), got, (2, True), lines))
    failed = [c for c in cases if not c[1]]
    for name, _, got, want, lines in failed:
        print(f"go-base-image self-test: FAIL {name}: got {got!r}, want {want!r}")
        for line in lines:
            print(f"    {line}")
    if failed:
        print(f"go-base-image self-test: {len(failed)} of {len(cases)} cases failed")
        return 1
    print(f"go-base-image self-test: {len(cases)} cases passed")
    return 0

def main(argv):
    if argv == ["--self-test"]:
        return self_test()
    if argv:
        print("usage: check-go-base-image.py [--self-test]")
        return 2
    code, lines = run(".")
    print("\n".join(lines))
    return code

if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
