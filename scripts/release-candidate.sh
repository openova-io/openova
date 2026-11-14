#!/usr/bin/env bash
# release-candidate.sh - cut, verify and prune release candidates.
#
# A candidate is the branch ref rc/<id> on origin. It points at one commit of main and
# carries no commit of its own. <id> is <UTC yyyymmddThhmmssZ>-<first 7 hex of the commit>,
# for example 20261120T060000Z-4be7a11. The tool never moves a ref that exists, and it
# writes no file into the working tree.
#
#   cut [--commit <sha>] [--dry-run]
#     Default commit: origin/main. Runs scripts/check-train-coherent.sh in a checkout of the
#     commit. Compares the upstream version of each kernel component with the newest earlier
#     candidate. Creates and pushes rc/<id>. With --dry-run it pushes nothing.
#     Exit 0: created (or, with --dry-run, would be created); <id> is the only line on stdout.
#     Exit 1: the pins are not coherent (the coherence output names the pin).
#     Exit 2: more than one kernel component moved (named on stderr).
#     Exit 3: the ref rc/<id> exists.
#     Exit 4: the tool could not run (argument, commit not on origin/main, unreadable
#             input, failed fetch or push).
#   verify <id>
#     Exit 0: the ref exists, its commit ends <id> and is an ancestor of origin/main, and
#     scripts/check-train-coherent.sh exits 0 there; the commit is the only line on stdout.
#     Exit 1: anything else.
#   prune [--dry-run]
#     Deletes the candidate refs that no gate record names, keeping the newest ten. A gate
#     record is a file docs/ledger/acceptance/*.json on origin/main; it names a candidate by
#     candidate.id. Prints each deleted id. Exit 0: done. Exit 4: could not run.
#
# Kernel components and where their upstream version is read at a commit:
#   k3s: the default of variable "k3s_version" in infra/providers/*/variables.tf
#   cilium flux crossplane openbao kyverno cnpg keycloak cert-manager:
#     dependencies[*].version in platform/<component>/chart/Chart.yaml
#
# Two variables exist only for scripts/test-release-candidate.sh. No workflow sets them.
#   RC_COHERENCE_CMD  replaces the coherence command; it runs with bash -c in the checkout.
#   RC_CUT_TIME       fixes the time part of <id> (yyyymmddThhmmssZ).
set -euo pipefail

KERNEL_CHARTS="cilium flux crossplane openbao kyverno cnpg keycloak cert-manager"
ID_RE='^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{7}$'
KEEP=10

say() { echo "release-candidate: $*" >&2; }
die() { say "$*"; exit 4; }

ROOT="$(git rev-parse --show-toplevel 2>/dev/null)" || die "not inside a Git checkout"
cd "$ROOT"

TMP=""
cleanup() {
  if [ -n "$TMP" ]; then
    if [ -d "$TMP/tree" ]; then git worktree remove --force "$TMP/tree" >/dev/null 2>&1 || true; fi
    rm -rf "$TMP"
    git worktree prune >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

fetch() {
  git fetch --quiet --prune origin '+refs/heads/main:refs/remotes/origin/main' \
    '+refs/heads/rc/*:refs/remotes/origin/rc/*'
}

# Candidate ids known on origin, oldest first. Refs under rc/ of another shape are ignored.
candidates() {
  git for-each-ref --format='%(refname:strip=4)' refs/remotes/origin/rc/ | { grep -E "$ID_RE" || true; } | sort
}

# coherence <sha>: run the coherence command in a checkout of <sha> and return its exit code.
# COHERENCE_RAN is 0 when the command could not be started at all.
COHERENCE_RAN=0
coherence() {
  local rc=0
  COHERENCE_RAN=0
  TMP="$(mktemp -d)"
  git worktree add --quiet --detach "$TMP/tree" "$1" >&2 || return 1
  if [ -n "${RC_COHERENCE_CMD:-}" ]; then
    COHERENCE_RAN=1
    (cd "$TMP/tree" && bash -c "$RC_COHERENCE_CMD") >&2 || rc=$?
  elif [ -f "$TMP/tree/scripts/check-train-coherent.sh" ]; then
    COHERENCE_RAN=1
    (cd "$TMP/tree" && bash scripts/check-train-coherent.sh) >&2 || rc=$?
  else
    say "scripts/check-train-coherent.sh does not exist at $1"
    rc=1
  fi
  cleanup
  TMP=""
  return "$rc"
}

# kernel <sha>: one line per kernel component, "<component> <version>"; "-" when unreadable.
kernel() {
  python3 - "$1" "$KERNEL_CHARTS" <<'PY'
import re, subprocess, sys
import yaml
sha, charts = sys.argv[1], sys.argv[2].split()
def show(path):
    r = subprocess.run(["git", "show", f"{sha}:{path}"], capture_output=True, text=True)
    return r.stdout if r.returncode == 0 else None
files = subprocess.run(["git", "ls-tree", "-r", "--name-only", sha, "--", "infra/providers"],
                       capture_output=True, text=True, check=True).stdout.split()
k3s = set()
for f in files:
    if re.fullmatch(r"infra/providers/[^/]+/variables\.tf", f):
        m = re.search(r'(?ms)^variable "k3s_version" \{\n(.*?)^\}', show(f) or "")
        d = re.search(r'(?m)^\s*default\s*=\s*"([^"]+)"', m.group(1)) if m else None
        if d:
            k3s.add(d.group(1))
print("k3s", ",".join(sorted(k3s)) or "-")
for c in charts:
    text, deps = show(f"platform/{c}/chart/Chart.yaml"), []
    try:
        doc = yaml.safe_load(text) if text else None
        deps = sorted(f"{d['name']}={d['version']}" for d in (doc or {}).get("dependencies") or [])
    except (yaml.YAMLError, KeyError, TypeError, AttributeError):
        deps = []
    print(c, ",".join(deps) or "-")
PY
}

cmd_cut() {
  local commit="refs/remotes/origin/main" dry=0 sha t id prev rc moved n old new
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --commit) [ "$#" -ge 2 ] || die "--commit needs a value"; commit="$2"; shift 2 ;;
      --dry-run) dry=1; shift ;;
      *) die "unknown argument to cut: $1" ;;
    esac
  done
  t="${RC_CUT_TIME:-$(date -u +%Y%m%dT%H%M%SZ)}"
  [[ "$t" =~ ^[0-9]{8}T[0-9]{6}Z$ ]] || die "the time $t is not yyyymmddThhmmssZ"
  fetch || die "git fetch origin failed"
  sha="$(git rev-parse --verify --quiet "${commit}^{commit}")" || die "cannot resolve $commit"
  git merge-base --is-ancestor "$sha" refs/remotes/origin/main || die "commit $sha is not on origin/main"
  id="$t-${sha:0:7}"
  say "candidate rc/$id at commit $sha"

  rc=0; coherence "$sha" || rc=$?
  [ "$COHERENCE_RAN" -eq 1 ] || die "the coherence command could not run at $sha"
  if [ "$rc" -ne 0 ]; then say "refused: the pins at $sha are not coherent (exit $rc)"; exit 1; fi

  new="$(kernel "$sha")" || die "cannot read the kernel versions at $sha"
  if printf '%s\n' "$new" | grep -E ' -$' >&2; then die "the kernel version above cannot be read at $sha"; fi
  prev="$(candidates | { grep -v -x -F "$id" || true; } | tail -n 1)"
  if [ -z "$prev" ]; then
    say "no earlier candidate: nothing to compare the kernel versions with"
  else
    old="$(kernel "refs/remotes/origin/rc/$prev")" || die "cannot read the kernel versions of rc/$prev"
    moved="$(awk 'NR == FNR {v[$1] = $2; next} v[$1] != $2 {print $1 " (" v[$1] " -> " $2 ")"}' \
      <(printf '%s\n' "$old") <(printf '%s\n' "$new"))"
    n="$(printf '%s' "$moved" | grep -c . || true)"
    if [ "$n" -gt 1 ]; then
      say "refused: $n kernel components moved since rc/$prev, at most one may:"
      printf '%s\n' "$moved" | sed 's/^/  /' >&2
      exit 2
    fi
    say "kernel components moved since rc/$prev: ${moved:-none}"
  fi

  rc=0; git ls-remote --exit-code origin "refs/heads/rc/$id" >/dev/null || rc=$?
  if [ "$rc" -eq 0 ]; then say "refused: rc/$id exists"; exit 3; fi
  [ "$rc" -eq 2 ] || die "cannot read the refs of origin"
  if [ "$dry" -eq 1 ]; then
    say "dry run: rc/$id would point at $sha; nothing pushed"
  else
    git push --quiet --force-with-lease="refs/heads/rc/$id:" origin "$sha:refs/heads/rc/$id" >&2 \
      || die "the push of rc/$id was refused"
    say "created rc/$id"
  fi
  echo "$id"
}

cmd_verify() {
  local id="${1:-}" sha rc
  [ "$#" -eq 1 ] || { say "usage: verify <id>"; exit 1; }
  [[ "$id" =~ $ID_RE ]] || { say "not a candidate identifier: $id"; exit 1; }
  fetch || { say "git fetch origin failed"; exit 1; }
  sha="$(git rev-parse --verify --quiet "refs/remotes/origin/rc/$id^{commit}")" \
    || { say "rc/$id does not exist"; exit 1; }
  [ "${sha:0:7}" = "${id##*-}" ] || { say "rc/$id points at $sha, which does not end the id"; exit 1; }
  git merge-base --is-ancestor "$sha" refs/remotes/origin/main \
    || { say "rc/$id points at $sha, which is not on origin/main"; exit 1; }
  rc=0; coherence "$sha" || rc=$?
  [ "$rc" -eq 0 ] || { say "the pins at $sha are not coherent (exit $rc)"; exit 1; }
  say "rc/$id verified"
  echo "$sha"
}

cmd_prune() {
  local dry=0 named all keep x
  case "${1:-}" in
    "") ;;
    --dry-run) dry=1 ;;
    *) die "unknown argument to prune: $1" ;;
  esac
  fetch || die "git fetch origin failed"
  named="$(python3 - <<'PY'
import json, subprocess, sys
ref, top = "refs/remotes/origin/main", "docs/ledger/acceptance/"
names = subprocess.run(["git", "ls-tree", "--name-only", ref, top], capture_output=True, text=True, check=True).stdout.split()
for f in names:
    if f.endswith(".json"):
        try:
            rec = json.loads(subprocess.run(["git", "show", f"{ref}:{f}"], capture_output=True, text=True, check=True).stdout)
        except (json.JSONDecodeError, subprocess.CalledProcessError):
            sys.exit(f"release-candidate: cannot read the gate record {f}")
        cand = rec.get("candidate") if isinstance(rec, dict) else None
        if isinstance(cand, dict) and cand.get("id"):
            print(cand["id"])
PY
)" || die "refused to prune: a gate record cannot be read"
  all="$(candidates)"
  keep="$(printf '%s\n' "$all" | tail -n "$KEEP")"
  for x in $all; do
    if printf '%s\n' "$keep" "$named" | grep -x -F -c "$x" >/dev/null; then continue; fi
    if [ "$dry" -eq 1 ]; then
      say "dry run: would delete rc/$x"
    else
      git push --quiet origin --delete "refs/heads/rc/$x" >&2 || die "the deletion of rc/$x was refused"
      say "deleted rc/$x"
    fi
    echo "$x"
  done
}

case "${1:-}" in
  cut) shift; cmd_cut "$@" ;;
  verify) shift; cmd_verify "$@" ;;
  prune) shift; cmd_prune "$@" ;;
  -h|--help) sed -n '2,35p' "$0" ;;
  *) say "usage: release-candidate.sh cut [--commit <sha>] [--dry-run] | verify <id> | prune [--dry-run]"; exit 4 ;;
esac
