#!/usr/bin/env bash
# check-train-coherent.sh — the train-coherence pre-flight for a fresh prov.
#
# WHY THIS EXISTS
# ───────────────
# A fresh Sovereign installs whatever the bootstrap-kit slot pins name, and it
# installs it exactly once. If the train of in-flight chart bumps is incoherent
# when the prov fires, the cycle is spent: the Sovereign comes up carrying the
# WRONG chart and — post-cutover — cannot receive the fix at all, because a
# Sovereign past step-05 is severed from GitHub and step-01's re-mirror is
# suppressed (#6307). So the only place this is cheap to catch is BEFORE the
# fire, on the repo side, which is all this script touches.
#
# The failure mode is not hypothetical. This program has already lost cutover
# attempts to a HOLLOW PIN — an umbrella chart bumped while its bootstrap-kit
# slot pin stayed put, so the OCI artifact published and every fresh Sovereign
# silently installed the previous version behind a green build.
#
# WHAT IT ASSERTS (four checks)
# ─────────────────────────────
#   1. VERSIONS MOVE FORWARD. For every chart the train touches, main's version
#      never regressed across its own history, and no open PR that actually
#      BUMPS that chart claims a version at or below main's — or the same
#      future version as a sibling PR.
#
#   2. NO HOLLOW PIN. Every umbrella bump carried its bootstrap-kit slot pin.
#      DELEGATED, never reimplemented — see "ON DELEGATION" below.
#
#   3. EVERY DELIVERY PIN RESOLVES ON GHCR — with the three states kept apart:
#      PUBLISHED / PENDING-MERGE / PENDING-PUBLISH / MISSING. Collapsing the
#      middle two into "missing" cries wolf on every healthy train; collapsing
#      them into "fine" is how a hollow pin ships. See "THE PUBLISH RACE".
#
#   4. THE FIVE LOCKSTEP SITES AGREE for every bumped chart (#817/#5583):
#        1 <chart>/Chart.yaml                       version:
#        2 <chart-dir>/blueprint.yaml               spec.version
#        3 products/catalyst/chart/templates/catalog-seed/blueprints.yaml
#                                                   spec.version AND source.version
#        4 products/catalyst/bootstrap/api/internal/catalog/blueprints.json
#        5 products/catalyst/bootstrap/ui/src/shared/constants/catalog.generated.ts
#      Sites 4 and 5 are BUILD OUTPUT of products/catalyst/bootstrap/ui/scripts/
#      build-catalog.mjs. This script asserts they match a FRESH REGENERATION
#      rather than reading them as authoritative — a committed generated file is
#      evidence of nothing except that somebody committed it.
#
# ON DELEGATION — why this script calls guards instead of re-checking them
# ───────────────────────────────────────────────────────────────────────
# Checks 2 and 3 invoke the existing guards and propagate their exit codes:
#
#     scripts/check-bootstrap-kit-pin-sync.sh          [--check-ghcr]
#     scripts/check-umbrella-republish-gate.py         --ref <ref>
#     scripts/check-catalog-seed-lockstep.sh           --check-ghcr
#     scripts/check-chart-version-not-claimed-by-open-pr.py --claimed-versions …
#
# Re-handrolling a guard's predicate is a documented failure in this repo: a
# 35-row fix silently became a 190-row ledger wipe because the reimplementation
# reproduced the guard's headline condition and dropped the two clauses that
# NARROW it. Every one of those guards carries narrowing clauses this script
# would otherwise have to re-derive and would eventually get wrong. Two of them
# matter enough to name here, because they are the ones a "simplification" would
# delete first:
#
#   - bp-catalyst-platform's OWN catalog-seed card renders
#     `version: {{ .Chart.Version | quote }}`. It is the umbrella referring to
#     itself, correct-by-construction, and sites 2/3/4 are legitimate NO-OPS for
#     it (it has no blueprint.yaml at all). check-release-lockstep-writer.py
#     carries this as an explicit CONTROL case. Check 4 below honours the same
#     clause, and --self-test pins it as a control so that a future "every site
#     must always move" tightening goes red here instead of in production.
#
#   - check-bootstrap-kit-pin-sync.sh deliberately treats charts with no kit pin
#     (opt-in Application Blueprints) as NOT APPLICABLE. They have no pin to lag.
#
# THE PUBLISH RACE — the thing this script must NOT get wrong
# ───────────────────────────────────────────────────────────
# .github/workflows/blueprint-release.yaml is `on: push → branches: [main]`.
# A chart tag therefore CANNOT exist on GHCR until its bump is ON main. That is
# structural, not a defect. So for an open PR that bumps bp-agenity to 0.5.28,
# "0.5.28 is not on GHCR" is the CORRECT and expected state, and reporting it as
# a missing artifact would make this script useless on exactly the trains it is
# meant to clear. Equally, once that version IS on main and its release run has
# CONCLUDED, an absent tag is a real lost publish (the TBD-A26 shape: versions
# 1.4.180/181 were lost to a startup_failure while the pin bumped normally).
#
# The classifier keeps four states apart and never collapses them:
#     PUBLISHED       tag present on GHCR                          → fine
#     PENDING-MERGE   version not on main yet; cannot publish yet  → fine, reported
#     PENDING-PUBLISH on main, release run still queued/running    → fine, reported
#     MISSING         on main, run concluded, tag still absent     → FAIL
#
# EXIT CODES
# ──────────
#   0  coherent — safe to fire
#   1  incoherent — at least one check failed; do NOT fire
#   2  CANNOT READ — a required input, tool or API call could not be read
#
# Exit 2 is never collapsed into 0. A `gh` call that fails and yields an empty
# list must not read as "nothing wrong": that exact hole let a monitor report
# `pending == 0` while 13 checks were pending. Wherever this script counts
# things, it requires a plausible MINIMUM count before believing a zero.
#
# SHELL HAZARDS THIS SCRIPT AVOIDS ON PURPOSE
# ───────────────────────────────────────────
#   - `grep -q` is BANNED here. Under `set -o pipefail`, `grep -q` exits on the
#     first match; if the producer still has output to write it takes SIGPIPE
#     (rc 141 locally) or EPIPE (rc 1 on the GitHub runner) — byte-identical to
#     an honest miss. Reproduced at 1 KB of trailing output. Use `grep -c
#     … >/dev/null`, which reads to EOF, or capture to a variable first.
#   - A gating command is NEVER piped into `tail`/`head`: the pipe throws away
#     the exit code and the guard becomes decorative. Chain with `&&`.
#   - `gh pr checks --json` does NOT exist in the pinned gh (2.4.0). It prints
#     "unknown flag" and, piped, yields rc 0 — a false green. Not used.
#   - CI pins yq v4.44.3, where `select(…) | … // "default"` over a multi-document
#     render applies the default to every filtered-out document. This script does
#     not parse the seed with yq at all; it reads the COMMITTED source with awk,
#     which is also what lets it see the `{{ … }}` templating that a render hides.
#   - Arithmetic uses `x=$((x+1))`, never `((x++))`, which returns 1 on a
#     zero result and would kill the script under `set -e`.
#
# USAGE
#   scripts/check-train-coherent.sh                 # full pre-flight (needs gh)
#   scripts/check-train-coherent.sh --no-ghcr       # offline: checks 1,2,4 only
#   scripts/check-train-coherent.sh --fire-readiness  # + the fire-readiness table
#   scripts/check-train-coherent.sh --self-test     # fixtures; proves each check fails
#
# Refs #6307 #817 #5583 #5734 #1872

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

SEED_REL="products/catalyst/chart/templates/catalog-seed/blueprints.yaml"
JSON_REL="products/catalyst/bootstrap/api/internal/catalog/blueprints.json"
TS_REL="products/catalyst/bootstrap/ui/src/shared/constants/catalog.generated.ts"
UI_DIR_REL="products/catalyst/bootstrap/ui"
KIT_REL="clusters/_template/bootstrap-kit"

GHCR_ORG="openova-io"
REPO_SLUG="openova-io/openova"
UMBRELLA_DIR="products/catalyst"
UMBRELLA_CHART="bp-catalyst-platform"

# Minimum plausible counts. A result below these means we could not READ the
# input, not that the input is empty. Every one of these has a floor because a
# zero from a failed read is indistinguishable from a zero from a clean tree.
MIN_KIT_SLOTS=10
MIN_SEED_CARDS=20
MIN_OPEN_PRS=1

DO_GHCR=1
DO_FIRE=0
# The baseline that open-PR claims are measured against. This is deliberately a
# REMOTE ref, not the working tree — see tc_version_at_ref's comment.
BASELINE_REF="origin/main"
DO_FETCH=1
SELF_TEST=0
HISTORY_DEPTH=40
TRAIN_OVERRIDE=""
REF="HEAD"

usage() { sed -n '2,140p' "$0"; }

while [ "$#" -gt 0 ]; do
  case "$1" in
    --no-ghcr)        DO_GHCR=0; shift ;;
    --baseline)       BASELINE_REF="${2:-origin/main}"; shift 2 ;;
    --no-fetch)       DO_FETCH=0; shift ;;
    --fire-readiness) DO_FIRE=1; shift ;;
    --self-test)      SELF_TEST=1; shift ;;
    --history-depth)  HISTORY_DEPTH="${2:-40}"; shift 2 ;;
    --charts)         TRAIN_OVERRIDE="${2:-}"; shift 2 ;;
    --ref)            REF="${2:-HEAD}"; shift 2 ;;
    --ghcr-org)       GHCR_ORG="${2:-openova-io}"; shift 2 ;;
    -h|--help)        usage; exit 0 ;;
    *) echo "ERROR: unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

# ── tiny primitives ────────────────────────────────────────────────────────

# `grep -c … >/dev/null` and not `grep -q`: -c reads its input to EOF, so the
# producer can never take SIGPIPE. See the header.
tc_matches() { # tc_matches <string> <ere>   → 0 if it matches
  printf '%s\n' "$1" | grep -cE "$2" >/dev/null
}

tc_semver_valid() { tc_matches "$1" '^[0-9]+\.[0-9]+\.[0-9]+$'; }

# Strictly-greater on semver. `sort -V | tail -1` here is VALUE EXTRACTION
# inside a command substitution, not a gate — tail consumes all of sort's
# output, so there is no discarded exit code and no SIGPIPE.
tc_semver_gt() { # $1 > $2 ?
  [ "$1" != "$2" ] || return 1
  local top
  top="$(printf '%s\n%s\n' "$1" "$2" | sort -V | tail -1)"
  [ "$top" = "$1" ]
}

C_FAIL=0   # a real incoherence  → exit 1
C_READ=0   # could not read      → exit 2

# errexit is GLOBAL shell state, not function-scoped. A helper that ends with a
# bare `set -e` therefore switches errexit back ON inside a caller that had
# deliberately turned it off — and the caller's next non-zero command kills the
# script instead of being measured. That is not theoretical: it silently
# truncated this script's own --self-test after the first CHECK 2 case. Save and
# restore the actual prior state instead of assuming it.
# NOTE: tc_errexit_state only READS. It is called inside $( ), which is a
# subshell — a `set +e` in there would be discarded with the subshell and the
# caller would keep running under errexit. So the caller does its own `set +e`
# on the line after. tc_errexit_restore is called as a plain command, so its
# `set -e` does reach the caller.
tc_errexit_state() { case $- in *e*) echo on ;; *) echo off ;; esac; }
tc_errexit_restore() { if [ "$1" = "on" ]; then set -e; fi; }

fail()  { C_FAIL=$((C_FAIL+1)); echo "  FAIL: $*"; }
unread(){ C_READ=$((C_READ+1)); echo "  CANNOT READ: $*"; }
ok()    { echo "  ok: $*"; }
note()  { echo "  ·  $*"; }

# ── site readers (pure; take a tree root so --self-test can point them at a
#    fixture rather than the real repo) ──────────────────────────────────────

# Site 1 — <chart>/Chart.yaml `version:`
tc_site1() { # <root> <chart_dir>
  local f="$1/$2/chart/Chart.yaml"
  [ -f "$f" ] || { echo ""; return 0; }
  awk '/^version:[[:space:]]/ { gsub(/^version:[[:space:]]*/,""); sub(/[[:space:]]+#.*$/,""); gsub(/^[[:space:]]+|[[:space:]]+$/,""); gsub(/["'"'"']/,""); print; exit }' "$f"
}

# Read a chart's version AT A GIT REF, rather than from the working tree.
#
# This exists because of a real false positive on this script's first live run.
# Check 1b compares open-PR claims — which come from the LIVE GitHub API — and
# it was comparing them against the version in the LOCAL working tree. A feature
# branch is forked from some older main, so the moment anything merges, the
# local baseline is stale and every claim that merely CARRIES the new main value
# looks like a bump above main. Concretely: #6316 merged mid-run, taking main to
# bp-agenity 0.5.28; PRs #6320 and #6325 both carried 0.5.28 while changing no
# agenity file at all; measured against the stale local 0.5.27 they read as two
# PRs claiming the same future version, and the run went red on a collision that
# did not exist.
#
# A live question needs a live baseline. Comparing a fresh fact against a stale
# one is its own bug class, independent of either value being correct.
tc_version_at_ref() { # <ref> <chart_dir>
  git show "$1:$2/chart/Chart.yaml" 2>/dev/null \
    | awk '/^version:[[:space:]]/ { gsub(/^version:[[:space:]]*/,""); sub(/[[:space:]]+#.*$/,""); gsub(/^[[:space:]]+|[[:space:]]+$/,""); gsub(/["'"'"']/,""); print; exit }'
}

tc_chart_name() { # <root> <chart_dir>
  local f="$1/$2/chart/Chart.yaml"
  [ -f "$f" ] || { echo ""; return 0; }
  awk '/^name:[[:space:]]/ { gsub(/^name:[[:space:]]*/,""); gsub(/["'"'"']/,""); print; exit }' "$f"
}

# Site 2 — <chart_dir>/blueprint.yaml `spec.version` (2-space indent under spec:)
# Absent blueprint.yaml is NOT a violation: the umbrella has none, by design.
tc_site2() { # <root> <chart_dir>
  local f="$1/$2/blueprint.yaml"
  [ -f "$f" ] || { echo "__ABSENT__"; return 0; }
  awk '
    /^spec:[[:space:]]*$/ { in_spec=1; next }
    /^[^[:space:]]/       { in_spec=0 }
    in_spec && /^  version:[[:space:]]/ {
      gsub(/^  version:[[:space:]]*/,""); sub(/[[:space:]]+#.*$/,""); gsub(/^[[:space:]]+|[[:space:]]+$/,""); gsub(/["'"'"']/,""); print; exit
    }' "$f"
}

# Sites 3 — the catalog-seed card for <chart>: spec.version and source.version.
#
# Read from the COMMITTED source, deliberately, not from a helm render. The
# render resolves `{{ .Chart.Version | quote }}` into a literal and so destroys
# the one distinction check 4 depends on: whether this card is TEMPLATED (the
# umbrella referring to itself — a legitimate no-op) or a STATIC pin that must
# move in lockstep. Emits "<spec>|<source>", with __TEMPLATED__ for a `{{ }}`
# value and __ABSENT__ when the chart has no seed card at all.
tc_site3() { # <root> <chart_name>
  local f="$1/$SEED_REL"
  [ -f "$f" ] || { echo "__NOFILE__"; return 0; }
  # Split on ---, keep the doc whose `  name:` equals <chart_name>, then read
  # its two version fields.
  awk -v want="$2" '
    function flush() {
      # NOTE: no apostrophes in this awk body — it is inside a single-quoted
      # shell string, and one stray quote silently ends the program.
      # `exit` still runs the END block, which calls flush() a second time, so
      # without this guard the value is printed twice. That happened to survive
      # the caller substring-split, which is exactly the sort of accident that
      # stops being harmless the moment someone reads the whole value (the
      # __ABSENT__ comparison does).
      if (printed) return
      if (name == want) {
        s = (spec == "" ? "__ABSENT__" : spec)
        r = (src  == "" ? "__ABSENT__" : src)
        if (s ~ /\{\{/) s = "__TEMPLATED__"
        if (r ~ /\{\{/) r = "__TEMPLATED__"
        print s "|" r
        printed = 1
        exit
      }
      name=""; spec=""; src=""; inspec=0; insrc=0
    }
    /^---[[:space:]]*$/ { flush(); next }
    /^  name:[[:space:]]/ {
      if (name == "") { n=$0; sub(/^  name:[[:space:]]*/,"",n); gsub(/["'"'"']/,"",n); name=n }
      next
    }
    /^spec:[[:space:]]*$/ { inspec=1; insrc=0; next }
    # Leaving the spec block is signalled ONLY by a real top-level manifest key.
    #
    # This used to read /^[^[:space:]#]/ — "any line starting in column 0 ends
    # spec". That is wrong for this file. Several cards carry a multi-line
    # description whose CONTINUATION LINES sit at column 0, e.g. bp-kserve:
    #
    #     description: "KServe ... stack supporting multi-
    #   framework predictors (vLLM, TorchServe, Triton, SKLearn), inference
    #   graphs. Wraps the upstream chart and ships a
    #     ..."
    #
    # Every wrapped line looked like a new top-level key, so inspec went to 0
    # before source: was ever reached and the delivery pin read __ABSENT__.
    # That reported 10 charts as lockstep failures which are perfectly in sync
    # — a false positive on every card long enough to wrap. These are Kubernetes
    # manifests, so the top-level key set is closed and small: match it exactly
    # instead of inferring structure from indentation.
    /^(apiVersion|kind|metadata|status):/ { inspec=0; insrc=0 }
    inspec && /^  source:[[:space:]]*$/ { insrc=1; next }
    inspec && /^  [a-zA-Z]/ && $0 !~ /^  source:/ { insrc=0 }
    inspec && !insrc && /^  version:[[:space:]]/ {
      v=$0; sub(/^  version:[[:space:]]*/,"",v); sub(/[[:space:]]+#.*$/,"",v); gsub(/^[[:space:]]+|[[:space:]]+$/,"",v); gsub(/["'"'"']/,"",v); if (spec=="") spec=v
    }
    insrc && /^    version:[[:space:]]/ {
      v=$0; sub(/^    version:[[:space:]]*/,"",v); sub(/[[:space:]]+#.*$/,"",v); gsub(/^[[:space:]]+|[[:space:]]+$/,"",v); gsub(/["'"'"']/,"",v); if (src=="") src=v
    }
    END { flush(); if (!printed) print "__ABSENT__" }
  ' "$f"
}

# Bootstrap-kit slot pin for <chart>. Delegated for the VERDICT (check 2);
# read here only so the report and the fire-readiness table can name it.
tc_kit_pin() { # <root> <chart_name>
  local d="$1/$KIT_REL"
  [ -d "$d" ] || { echo "__NOFILE__"; return 0; }
  awk -v want="$2" '
    $1 == "chart:"   { c=$2; gsub(/["'"'"']/,"",c) }
    $1 == "version:" { v=$2; gsub(/["'"'"']/,"",v); if (c == want) { print v; exit } }
  ' "$d"/*.yaml 2>/dev/null || true
}

# ── check 1 decision helpers (pure — fixture-testable) ─────────────────────

# A version series, oldest first, must never decrease.
tc_check_monotonic() { # <file>
  local prev="" cur bad=0 n=0
  while IFS= read -r cur; do
    [ -n "$cur" ] || continue
    n=$((n+1))
    if [ -n "$prev" ] && [ "$cur" != "$prev" ]; then
      if ! tc_semver_gt "$cur" "$prev"; then
        echo "regression: $prev -> $cur"
        bad=$((bad+1))
      fi
    fi
    prev="$cur"
  done < "$1"
  [ "$n" -gt 0 ] || { echo "__EMPTY__"; return 2; }
  [ "$bad" -eq 0 ]
}

# THE LIVE invariant, as distinct from the historical one above.
#
# tc_check_monotonic is a strict predicate: it goes red on ANY backwards step in
# the series. That is the right shape for a self-test, and the wrong shape for a
# fire gate — measured against the real tree it fires on bp-cnpg for a 1.0.2 ->
# 1.0.1 dip from May 2026 that was surpassed long ago (the chart is at 1.0.14).
# Blocking tonight's prov on three-month-old healed history is the cry-wolf
# failure this script warns about for check 3, committed in check 1.
#
# What actually endangers a fresh prov is main being BEHIND ITSELF right now:
# HEAD's version lower than some version main already carried, and therefore
# possibly already published to GHCR under different content (the b76f3ca7b
# shape, where 1.4.1324 published AFTER 1.4.1325 and carried different code).
# So: head-below-max is a hard FAIL; a dip that has since been surpassed is
# reported and does not block.
tc_check_head_not_behind() { # <series-file, oldest first>
  local n max="" head="" cur
  n=0
  while IFS= read -r cur; do
    [ -n "$cur" ] || continue
    n=$((n+1))
    head="$cur"
    if [ -z "$max" ] || tc_semver_gt "$cur" "$max"; then max="$cur"; fi
  done < "$1"
  [ "$n" -gt 0 ] || { echo "__EMPTY__"; return 2; }
  if [ "$head" != "$max" ]; then
    echo "HEAD is $head but main already carried $max"
    return 1
  fi
  return 0
}

# Among open-PR claims STRICTLY ABOVE main's version, every claim must be
# distinct. Claims at or below main are stale carries, not bumps: an un-rebased
# branch carries whatever main held when it forked, and 36 of the 41 open PRs
# are in exactly that state right now. Treating a carry as a claim would make
# this check fire permanently and mean nothing. The authoritative per-PR
# collision verdict belongs to check-chart-version-not-claimed-by-open-pr.py,
# which this script invokes separately.
tc_check_distinct_claims() { # <claims-file: "<pr> <version>"> <main_version>
  local claims="$1" main="$2" bad=0 ahead=0
  local dupes
  dupes="$(awk -v m="$main" '
      { print $2 }
    ' "$claims" | while IFS= read -r v; do
        [ -n "$v" ] || continue
        if tc_semver_gt "$v" "$main"; then echo "$v"; fi
      done | sort | uniq -d)"
  ahead="$(awk '{ print $2 }' "$claims" | while IFS= read -r v; do
        [ -n "$v" ] || continue
        if tc_semver_gt "$v" "$main"; then echo "$v"; fi
      done | sort -u | wc -l)"
  if [ -n "$dupes" ]; then
    local d
    while IFS= read -r d; do
      [ -n "$d" ] || continue
      echo "duplicate future claim $d by PR(s): $(awk -v v="$d" '$2==v{printf "%s ",$1}' "$claims")"
      bad=$((bad+1))
    done <<< "$dupes"
  fi
  echo "__AHEAD__=$ahead"
  [ "$bad" -eq 0 ]
}

# ── check 3 decision helper (pure — fixture-testable) ──────────────────────
#
# Four states, never collapsed. See "THE PUBLISH RACE" in the header.
tc_classify_pin() { # <version> <tags-file> <on_main:yes|no> <wf_state>
  local ver="$1" tags="$2" on_main="$3" wf="$4"
  if [ ! -s "$tags" ]; then echo "UNREADABLE"; return 0; fi
  local hit
  hit="$(grep -cx -- "$ver" "$tags" || true)"
  if [ "${hit:-0}" -gt 0 ]; then echo "PUBLISHED"; return 0; fi
  if [ "$on_main" != "yes" ]; then echo "PENDING-MERGE"; return 0; fi
  case "$wf" in
    completed|concluded) echo "MISSING" ;;
    *)                   echo "PENDING-PUBLISH" ;;
  esac
}

# ── check 3, part two: is there actually a release run? ───────────────────
#
# WHY THIS EXISTS AT ALL. The first cut of this script passed a hardcoded
# wf_state="unknown" into tc_classify_pin, which maps to PENDING-PUBLISH. The
# self-test proved the classifier CAN return MISSING — and the real run could
# never reach it, for any input. That is the dominant defect class in this
# repo (a guard testing a surface that cannot fail), committed inside the very
# check written to prevent it. A branch only a fixture can enter is decoration.
#
# So the state is now MEASURED: find the commit that set the version, ask the
# API for blueprint-release runs at that SHA, and map the answer.
#
# "No run found" is deliberately NOT benign. The TBD-A26 loss (versions
# 1.4.180/181) happened exactly that way: blueprint-release.yaml failed with
# `startup_failure / jobs: []` — no run to observe — while the pin bumped
# normally. Silence there means the publish never started, which is the failure,
# not the absence of one. It is forgiven only inside a short grace window, since
# a commit pushed 30 seconds ago legitimately has no run yet.
GRACE_SECONDS=1800

# Pure, fixture-testable: JSON in, state out.
tc_run_state_from_json() { # <json-file> <commit_age_seconds>
  local f="$1" age="$2" n st
  if [ ! -s "$f" ]; then
    # Unreadable is not "no run" — say so rather than guessing either way.
    echo "unreadable"; return 0
  fi
  n="$(jq -r '.workflow_runs | length' "$f" 2>/dev/null || echo "")"
  if [ -z "$n" ]; then echo "unreadable"; return 0; fi
  if [ "$n" -eq 0 ]; then
    if [ "$age" -lt "$GRACE_SECONDS" ]; then echo "none-young"; else echo "none-old"; fi
    return 0
  fi
  # completed only if EVERY run at this SHA has finished; one still in flight
  # means the publish may yet land.
  st="$(jq -r '[.workflow_runs[].status] | if any(. != "completed") then "running" else "completed" end' "$f" 2>/dev/null || echo "")"
  if [ -z "$st" ]; then echo "unreadable"; else echo "$st"; fi
}

# Map a measured run-state onto the vocabulary tc_classify_pin speaks.
tc_wf_state_word() { # <state-from-json>
  case "$1" in
    completed|none-old) echo "completed" ;;   # the publish had its chance
    running|none-young) echo "running"   ;;   # still may land
    *)                  echo "unreadable";;
  esac
}

# The commit on <ref> that first carried <version> in <chart_dir>. Walks the
# path's history newest-first and returns the OLDEST commit still holding it.
tc_setting_commit() { # <ref> <chart_dir> <version>
  local ref="$1" d="$2" ver="$3" sha last=""
  while IFS= read -r sha; do
    [ -n "$sha" ] || continue
    if [ "$(tc_version_at_ref "$sha" "$d")" = "$ver" ]; then last="$sha"; else break; fi
  done < <(git rev-list --max-count=60 "$ref" -- "$d/chart/Chart.yaml" 2>/dev/null)
  echo "$last"
}

# ── check 4 decision helper (pure — fixture-testable) ──────────────────────
#
# Compares sites 1/2/3 for one chart. Sites 4+5 are proven by regeneration in
# tc_check_generated, not here — a committed build output is evidence of
# nothing except that somebody committed it.
#
# NARROWING CLAUSES (do not delete — --self-test pins both as controls):
#   - blueprint.yaml absent      → sites 2/3 are legitimate no-ops (the umbrella)
#   - seed value is `{{ … }}`    → self-referential, correct-by-construction
tc_check_sites() { # <root> <chart_dir> [chart_name]
  local root="$1" dir="$2" name="${3:-}"
  local v1 v2 s3 spec3 src3 bad=0
  v1="$(tc_site1 "$root" "$dir")"
  [ -n "$name" ] || name="$(tc_chart_name "$root" "$dir")"
  if [ -z "$v1" ]; then echo "__NOCHART__"; return 2; fi

  v2="$(tc_site2 "$root" "$dir")"
  if [ "$v2" = "__ABSENT__" ]; then
    echo "site2 blueprint.yaml: absent (no-op by design)"
  elif [ "$v2" != "$v1" ]; then
    echo "site2 blueprint.yaml spec.version=$v2 != Chart.yaml $v1"
    bad=$((bad+1))
  fi

  s3="$(tc_site3 "$root" "$name")"
  if [ "$s3" = "__ABSENT__" ] || [ "$s3" = "__NOFILE__" ]; then
    echo "site3 catalog-seed: no card (not seeded — nothing to lockstep)"
  else
    spec3="${s3%%|*}"; src3="${s3##*|}"
    if [ "$spec3" = "__TEMPLATED__" ]; then
      echo "site3 spec.version: templated (self-referential, no-op by design)"
    elif [ "$spec3" != "$v1" ]; then
      echo "site3 seed spec.version=$spec3 != Chart.yaml $v1"
      bad=$((bad+1))
    fi
    if [ "$src3" = "__TEMPLATED__" ]; then
      echo "site3 source.version: templated (self-referential, no-op by design)"
    elif [ "$src3" != "$v1" ]; then
      echo "site3 seed source.version=$src3 != Chart.yaml $v1"
      bad=$((bad+1))
    fi
  fi
  [ "$bad" -eq 0 ]
}

# Sites 4+5 — regenerate and prove no diff. The generator preserves the prior
# `generatedAt` when content is unchanged (#5520), so a clean tree regenerates
# byte-identically and a plain `git diff` is the whole assertion. Restores the
# working tree on every exit path.
tc_check_generated() { # <root>
  local root="$1"
  command -v node >/dev/null 2>&1 || { echo "__NONODE__"; return 2; }
  [ -f "$root/$UI_DIR_REL/scripts/build-catalog.mjs" ] || { echo "__NOGEN__"; return 2; }

  local bkp; bkp="$(mktemp -d)"
  cp "$root/$JSON_REL" "$bkp/blueprints.json" 2>/dev/null || true
  cp "$root/$TS_REL"   "$bkp/catalog.generated.ts" 2>/dev/null || true
  # shellcheck disable=SC2064
  trap "cp '$bkp/blueprints.json' '$root/$JSON_REL' 2>/dev/null || true; \
        cp '$bkp/catalog.generated.ts' '$root/$TS_REL' 2>/dev/null || true; \
        rm -rf '$bkp'" RETURN

  if ! ( cd "$root/$UI_DIR_REL" && node scripts/build-catalog.mjs ) >/dev/null 2>&1; then
    echo "__GENFAIL__"; return 2
  fi

  local d1 d2 rc=0
  d1="$(diff -q "$bkp/blueprints.json" "$root/$JSON_REL" >/dev/null 2>&1 && echo same || echo diff)"
  d2="$(diff -q "$bkp/catalog.generated.ts" "$root/$TS_REL" >/dev/null 2>&1 && echo same || echo diff)"
  [ "$d1" = "same" ] || { echo "site4 blueprints.json drifted from a fresh regeneration"; rc=1; }
  [ "$d2" = "same" ] || { echo "site5 catalog.generated.ts drifted from a fresh regeneration"; rc=1; }
  return "$rc"
}

# ── guard delegation ───────────────────────────────────────────────────────
#
# Runs an existing guard and maps its verdict onto ours. Their 2 (cannot read)
# stays a 2 here; it is never softened into a pass. Output is echoed in full —
# never piped into head/tail, which would discard the exit code.
tc_run_guard() { # <label> <cmd...>
  local label="$1"; shift
  local out rc=0 prev
  prev="$(tc_errexit_state)"; set +e
  out="$("$@" 2>&1)"
  rc=$?
  tc_errexit_restore "$prev"
  case "$rc" in
    0) ok "$label — guard passed (rc=0)" ;;
    2) unread "$label — guard could not read its input (rc=2)"
       printf '%s\n' "$out" | sed 's/^/      | /' ;;
    *) fail "$label — guard FAILED (rc=$rc)"
       printf '%s\n' "$out" | sed 's/^/      | /' ;;
  esac
  return 0
}

# ═══════════════════════════════════════════════════════════════════════════
#                                 SELF-TEST
# ═══════════════════════════════════════════════════════════════════════════
#
# Every check gets BOTH a violating fixture (must be non-zero) and a clean
# fixture (must be zero). A guard that has never gone red is decorative, and a
# guard that cannot go green is a harness that always fails — neither is signal.
# Two cases are CONTROLS that must stay GREEN: they encode the narrowing clauses
# a future "tighten it up" edit would delete first.

st_pass=0; st_fail=0
st_case() { # <name> <expect:0|1|2> <cmd...>
  local name="$1" expect="$2"; shift 2
  local rc=0 prev
  prev="$(tc_errexit_state)"; set +e
  # Subshell: a fixture case that trips errexit must be MEASURED as a non-zero
  # result, never allowed to take the harness down with it.
  ( "$@" ) >/dev/null 2>&1
  rc=$?
  tc_errexit_restore "$prev"
  if [ "$rc" = "$expect" ]; then
    st_pass=$((st_pass+1))
    printf '  PASS  %-58s rc=%s (want %s)\n' "$name" "$rc" "$expect"
  else
    st_fail=$((st_fail+1))
    printf '  FAIL  %-58s rc=%s (want %s)\n' "$name" "$rc" "$expect"
  fi
}

st_mkchart() { # <root> <dir> <name> <chart_ver> [bp_ver] [seed_spec] [seed_src]
  local root="$1" dir="$2" name="$3" cv="$4" bp="${5:-}" ss="${6:-}" sr="${7:-}"
  mkdir -p "$root/$dir/chart" "$root/$(dirname "$SEED_REL")"
  printf 'apiVersion: v2\nname: %s\nversion: %s\nappVersion: %s\n' "$name" "$cv" "$cv" \
    > "$root/$dir/chart/Chart.yaml"
  if [ -n "$bp" ]; then
    printf 'apiVersion: catalyst.openova.io/v1alpha1\nkind: Blueprint\nmetadata:\n  name: %s\nspec:\n  version: "%s"\n' \
      "$name" "$bp" > "$root/$dir/blueprint.yaml"
  fi
  if [ -n "$ss" ]; then
    {
      printf -- '---\n'
      printf 'apiVersion: catalyst.openova.io/v1alpha1\nkind: Blueprint\nmetadata:\n  name: %s\n' "$name"
      printf 'spec:\n  version: %s\n  visibility: listed\n' "$ss"
      printf '  manifests:\n    chart: %s\n' "$name"
      printf '  source:\n    kind: HelmRepository\n    type: oci\n    chart: %s\n    version: %s\n' "$name" "$sr"
    } >> "$root/$SEED_REL"
  fi
}

run_self_test() {
  echo "check-train-coherent.sh --self-test"
  echo "Each check gets a violating fixture (must be RED) and a clean fixture"
  echo "(must be GREEN). Controls marked CONTROL must stay GREEN — they pin the"
  echo "narrowing clauses that a blanket 'every site always moves' edit deletes."
  echo

  # NOT `local`: the EXIT trap fires after this function's frame is gone, and
  # under `set -u` a local name in the trap body is an unbound-variable error.
  ST_TMP="$(mktemp -d)"
  trap 'rm -rf "${ST_TMP:-}"' EXIT
  local T="$ST_TMP"

  # ── CHECK 1a — main's own history must never regress ────────────────────
  echo "CHECK 1  versions move forward"
  printf '1.4.1480\n1.4.1482\n1.4.1484\n' > "$T/mono-clean"
  printf '1.4.1325\n1.4.1324\n' > "$T/mono-bad"     # the real b76f3ca7b shape
  : > "$T/mono-empty"
  st_case "1a monotonic: clean series"                 0 tc_check_monotonic "$T/mono-clean"
  st_case "1a monotonic: VIOLATING backwards bump"     1 tc_check_monotonic "$T/mono-bad"
  st_case "1a monotonic: empty input is CANNOT READ"   2 tc_check_monotonic "$T/mono-empty"
  # The LIVE gate, and the narrowing that keeps it from crying wolf. The healed
  # fixture is the real bp-cnpg shape: a dip in May that 1.0.14 long surpassed.
  printf '1.4.1325\n1.4.1324\n' > "$T/head-behind"
  printf '1.0.1\n1.0.2\n1.0.1\n1.0.14\n' > "$T/head-healed"
  printf '1.4.1480\n1.4.1484\n' > "$T/head-clean"
  st_case "1a head: clean series"                            0 tc_check_head_not_behind "$T/head-clean"
  st_case "1a head: VIOLATING HEAD behind a version main had" 1 tc_check_head_not_behind "$T/head-behind"
  st_case "1a head: CONTROL healed dip does NOT block a fire" 0 tc_check_head_not_behind "$T/head-healed"
  st_case "1a head: empty input is CANNOT READ"              2 tc_check_head_not_behind "$T/mono-empty"

  # ── CHECK 1b — two open PRs may not claim the same future version ───────
  # Clean fixture deliberately INCLUDES a stale carry below main, to pin that
  # an un-rebased branch is not a violation (36 of 41 open PRs are in that
  # state; treating them as claims would make this check fire forever).
  printf '6316 1.4.1486\n6320 1.4.1487\n6321 1.4.1484\n5306 1.4.1264\n' > "$T/claims-clean"
  printf '5964 1.4.1332\n5968 1.4.1332\n' > "$T/claims-bad"
  st_case "1b claims: distinct futures + stale carries" 0 tc_check_distinct_claims "$T/claims-clean" 1.4.1484
  st_case "1b claims: VIOLATING duplicate future claim" 1 tc_check_distinct_claims "$T/claims-bad"   1.4.1331
  # REGRESSION PIN — the exact false positive this script produced on its first
  # live run. #6316 merged mid-run taking main to bp-agenity 0.5.28; #6320 and
  # #6325 both CARRIED 0.5.28 and changed no agenity file. Against the FRESH
  # baseline they are carries and the run must stay green; against the STALE
  # one they read as a duplicate future claim and the run goes red on nothing.
  # Both directions are asserted so the baseline can never quietly go stale.
  printf '6320 0.5.28\n6325 0.5.28\n' > "$T/claims-carry"
  st_case "1b claims: CONTROL carries of a FRESH baseline are not a collision" 0 \
    tc_check_distinct_claims "$T/claims-carry" 0.5.28
  st_case "1b claims: the same input against a STALE baseline DOES go red"     1 \
    tc_check_distinct_claims "$T/claims-carry" 0.5.27
  echo

  # ── CHECK 2 — hollow pin: we DELEGATE, so what must be proven here is
  #    that a failing guard is not swallowed. That is the whole risk in a
  #    wrapper: a guard that goes red while the caller reports green.
  echo "CHECK 2  no hollow pin (delegated — propagation is what is tested)"
  printf '#!/bin/sh\necho stub-fail\nexit 1\n' > "$T/g-fail.sh"
  printf '#!/bin/sh\necho stub-unreadable\nexit 2\n' > "$T/g-read.sh"
  printf '#!/bin/sh\necho stub-ok\nexit 0\n' > "$T/g-ok.sh"
  chmod +x "$T/g-fail.sh" "$T/g-read.sh" "$T/g-ok.sh"
  st_case "2 propagation: guard rc=0 → coherent"       0 st_probe_guard "$T/g-ok.sh"
  st_case "2 propagation: VIOLATING guard rc=1 → red"  1 st_probe_guard "$T/g-fail.sh"
  st_case "2 propagation: guard rc=2 → CANNOT READ"    2 st_probe_guard "$T/g-read.sh"
  echo

  # ── CHECK 3 — the four publish states, kept apart ───────────────────────
  echo "CHECK 3  every delivery pin resolves on GHCR (4 states, never collapsed)"
  printf '1.4.1482\n1.4.1484\n' > "$T/tags"
  : > "$T/tags-empty"
  st_case "3 classify: on GHCR → PUBLISHED"                    0 st_expect_class PUBLISHED       1.4.1484 "$T/tags" yes completed
  st_case "3 classify: not on main → PENDING-MERGE"            0 st_expect_class PENDING-MERGE   1.4.1486 "$T/tags" no  none
  st_case "3 classify: on main, run live → PENDING-PUBLISH"    0 st_expect_class PENDING-PUBLISH 1.4.1485 "$T/tags" yes in_progress
  st_case "3 classify: VIOLATING on main, run done → MISSING"  0 st_expect_class MISSING         1.4.1485 "$T/tags" yes completed
  st_case "3 classify: empty tag list → UNREADABLE not 'gone'" 0 st_expect_class UNREADABLE      1.4.1484 "$T/tags-empty" yes completed
  # and the aggregate: only MISSING may turn the run red
  st_case "3 aggregate: a MISSING pin makes the run RED"       1 st_agg_class MISSING
  st_case "3 aggregate: PENDING-MERGE does NOT make it red"    0 st_agg_class PENDING-MERGE
  st_case "3 aggregate: UNREADABLE is CANNOT READ, never pass" 2 st_agg_class UNREADABLE

  # The run-state reader. Without this the MISSING branch above is reachable
  # only from a fixture — which is what the first cut of this script actually
  # shipped, and the reason these cases exist.
  printf '{"workflow_runs":[{"status":"completed"}]}\n'                       > "$T/runs-done"
  printf '{"workflow_runs":[{"status":"completed"},{"status":"in_progress"}]}\n' > "$T/runs-live"
  printf '{"workflow_runs":[]}\n'                                            > "$T/runs-none"
  : > "$T/runs-empty"
  st_case "3 runstate: all runs finished → completed"          0 st_expect_run completed  "$T/runs-done"  99999
  st_case "3 runstate: one still in flight → running"          0 st_expect_run running    "$T/runs-live"  99999
  st_case "3 runstate: NO run + past grace → none-old"         0 st_expect_run none-old   "$T/runs-none"  99999
  st_case "3 runstate: NO run + inside grace → none-young"     0 st_expect_run none-young "$T/runs-none"  10
  st_case "3 runstate: unreadable body is NOT 'no run'"        0 st_expect_run unreadable "$T/runs-empty" 99999
  # and the mapping that makes MISSING reachable from a REAL measurement
  st_case "3 runstate→word: none-old means the publish had its chance" 0 st_expect_word completed  none-old
  st_case "3 runstate→word: none-young is still pending"               0 st_expect_word running    none-young
  st_case "3 END-TO-END: a real none-old measurement yields MISSING"   0 st_missing_reachable "$T/runs-none" "$T/tags"
  echo

  # ── CHECK 4 — the five lockstep sites ───────────────────────────────────
  echo "CHECK 4  the five lockstep sites agree"
  local R="$T/repo-clean"; mkdir -p "$R"
  st_mkchart "$R" "platform/demo" "bp-demo" "1.2.3" "1.2.3" "1.2.3" "1.2.3"
  st_case "4 sites: all five agree"                    0 tc_check_sites "$R" "platform/demo" "bp-demo"

  # REGRESSION PIN 1 — an inline YAML comment after the version. Fourteen
  # charts in the real tree carry one (`version: 1.0.1  # lockstep with ...`),
  # and the first cut reported every single one as a lockstep failure because
  # it compared "1.0.1  # lockstep with ..." against "1.0.1".
  local RCOM="$T/repo-comment"; mkdir -p "$RCOM/platform/demo/chart" "$RCOM/$(dirname "$SEED_REL")"
  printf 'apiVersion: v2\nname: bp-demo\nversion: 1.2.3  # bumped for #1234\n' \
    > "$RCOM/platform/demo/chart/Chart.yaml"
  printf 'apiVersion: catalyst.openova.io/v1alpha1\nkind: Blueprint\nmetadata:\n  name: bp-demo\nspec:\n  version: 1.2.3  # lockstep with chart/Chart.yaml (#1234)\n' \
    > "$RCOM/platform/demo/blueprint.yaml"
  printf -- '---\n' > "$RCOM/$SEED_REL"
  st_case "4 sites: CONTROL inline YAML comment is not a mismatch" 0 \
    tc_check_sites "$RCOM" "platform/demo" "bp-demo"

  # REGRESSION PIN 2 — a seed card whose description wraps to column 0. Ten
  # charts in the real tree do this (bp-kserve, bp-valkey, bp-matrix, ...) and
  # the first cut read their delivery pin as __ABSENT__, because a wrapped
  # description line was mistaken for a new top-level key and ended the spec
  # block before `source:`.
  local RWRAP="$T/repo-wrap"; mkdir -p "$RWRAP/platform/demo/chart" "$RWRAP/$(dirname "$SEED_REL")"
  printf 'apiVersion: v2\nname: bp-demo\nversion: 1.2.3\n' > "$RWRAP/platform/demo/chart/Chart.yaml"
  {
    printf -- '---\n'
    printf 'apiVersion: catalyst.openova.io/v1alpha1\nkind: Blueprint\nmetadata:\n  name: bp-demo\n'
    printf 'spec:\n  version: "1.2.3"\n'
    printf '  card:\n    description: "A long summary that wraps across\n'
    printf 'several lines whose continuation sits at column zero, exactly\n'
    printf 'like bp-kserve and nine other cards in the real seed.\n'
    printf '"\n'
    printf '  source:\n    kind: HelmRepository\n    chart: bp-demo\n    version: "1.2.3"\n'
  } > "$RWRAP/$SEED_REL"
  st_case "4 sites: CONTROL column-0 description wrap still finds source" 0 \
    tc_check_sites "$RWRAP" "platform/demo" "bp-demo"

  local RB="$T/repo-lag"; mkdir -p "$RB"
  st_mkchart "$RB" "platform/demo" "bp-demo" "1.2.3" "1.2.3" "1.2.3" "1.2.2"
  st_case "4 sites: VIOLATING seed source.version lags" 1 tc_check_sites "$RB" "platform/demo" "bp-demo"

  local RC2="$T/repo-bplag"; mkdir -p "$RC2"
  st_mkchart "$RC2" "platform/demo" "bp-demo" "1.2.3" "1.2.2" "1.2.3" "1.2.3"
  st_case "4 sites: VIOLATING blueprint.yaml lags"      1 tc_check_sites "$RC2" "platform/demo" "bp-demo"

  # CONTROL — the umbrella. No blueprint.yaml, and a seed card that renders
  # `{{ .Chart.Version | quote }}`. Sites 2/3 are legitimate no-ops. If someone
  # "hardens" tc_check_sites into asserting every site always moves, THIS goes
  # red — which is the point of pinning it.
  local RU="$T/repo-umbrella"; mkdir -p "$RU/products/catalyst/chart" "$RU/$(dirname "$SEED_REL")"
  printf 'apiVersion: v2\nname: bp-catalyst-platform\nversion: 1.4.1484\n' \
    > "$RU/products/catalyst/chart/Chart.yaml"
  {
    printf -- '---\n'
    printf 'apiVersion: catalyst.openova.io/v1alpha1\nkind: Blueprint\nmetadata:\n  name: bp-catalyst-platform\n'
    printf 'spec:\n  version: {{ .Chart.Version | quote }}\n'
    printf '  source:\n    chart: bp-catalyst-platform\n    version: {{ .Chart.Version | quote }}\n'
  } > "$RU/$SEED_REL"
  st_case "4 sites: CONTROL umbrella templated seed stays GREEN" 0 \
    tc_check_sites "$RU" "products/catalyst" "bp-catalyst-platform"

  # CONTROL — a chart with no seed card at all (opt-in Application Blueprint).
  local RN="$T/repo-nocard"; mkdir -p "$RN"
  st_mkchart "$RN" "platform/optin" "bp-optin" "0.1.0" "0.1.0" "" ""
  printf -- '---\n' > "$RN/$SEED_REL"
  st_case "4 sites: CONTROL no seed card is NOT APPLICABLE"      0 \
    tc_check_sites "$RN" "platform/optin" "bp-optin"

  # A tree with no Chart.yaml at all is CANNOT READ, not a pass.
  local RX="$T/repo-empty"; mkdir -p "$RX"
  st_case "4 sites: missing Chart.yaml is CANNOT READ"           2 \
    tc_check_sites "$RX" "platform/ghost" "bp-ghost"
  echo

  echo "──────────────────────────────────────────────────────────────────────"
  printf 'self-test: %d passed, %d failed\n' "$st_pass" "$st_fail"
  if [ "$st_fail" -gt 0 ]; then
    echo "SELF-TEST FAILED — this guard cannot be trusted to fire." >&2
    return 1
  fi
  echo "SELF-TEST OK — each of the four checks went RED on its violating"
  echo "fixture, GREEN on its clean fixture, and both narrowing controls held."
  return 0
}

# self-test helpers that turn a helper's output into an exit code
st_probe_guard() { # <stub>  → mirror tc_run_guard's verdict as an exit code
  C_FAIL=0; C_READ=0
  tc_run_guard "stub" "$1" >/dev/null 2>&1
  [ "$C_READ" -eq 0 ] || return 2
  [ "$C_FAIL" -eq 0 ] || return 1
  return 0
}

st_expect_class() { # <want> <version> <tags> <on_main> <wf>
  local want="$1"; shift
  local got; got="$(tc_classify_pin "$@")"
  [ "$got" = "$want" ]
}

st_expect_run() { # <want> <json> <age>
  local want="$1"; shift
  [ "$(tc_run_state_from_json "$1" "$2")" = "$want" ]
}

st_expect_word() { # <want> <raw-state>
  [ "$(tc_wf_state_word "$2")" = "$1" ]
}

# The case that proves the chain, not just its links: a MEASURED "no run, past
# grace" must travel all the way through tc_wf_state_word into tc_classify_pin
# and come out MISSING. If any link regresses to a benign default, this fails.
st_missing_reachable() { # <runs-json> <tags-file>
  local raw word got
  raw="$(tc_run_state_from_json "$1" 99999)"
  word="$(tc_wf_state_word "$raw")"
  got="$(tc_classify_pin "9.9.9" "$2" yes "$word")"
  [ "$got" = "MISSING" ]
}

st_agg_class() { # <state> → the aggregate verdict that state must produce
  case "$1" in
    MISSING)                        return 1 ;;
    UNREADABLE)                     return 2 ;;
    PUBLISHED|PENDING-MERGE|PENDING-PUBLISH) return 0 ;;
  esac
  return 2
}

if [ "$SELF_TEST" = "1" ]; then
  run_self_test
  exit $?
fi

# ═══════════════════════════════════════════════════════════════════════════
#                                 REAL RUN
# ═══════════════════════════════════════════════════════════════════════════

cd "$REPO_ROOT"

echo "════════════════════════════════════════════════════════════════════════"
echo " TRAIN-COHERENCE PRE-FLIGHT"
echo " repo:  $REPO_ROOT"
echo " ref:   $REF  ($(git rev-parse --short "$REF" 2>/dev/null || echo '?'))"
echo " ghcr:  $([ "$DO_GHCR" = 1 ] && echo "enabled (org=$GHCR_ORG)" || echo "SKIPPED (--no-ghcr)")"
echo "════════════════════════════════════════════════════════════════════════"
echo

# ── sanity floors: a zero here means we could not read, not that it is empty ─
kit_slots="$(ls -1 "$REPO_ROOT/$KIT_REL"/*.yaml 2>/dev/null | wc -l)"
if [ "$kit_slots" -lt "$MIN_KIT_SLOTS" ]; then
  unread "bootstrap-kit has $kit_slots slot file(s), expected >= $MIN_KIT_SLOTS — refusing to believe this tree"
fi
seed_cards="$(grep -c '^kind: Blueprint' "$REPO_ROOT/$SEED_REL" 2>/dev/null || echo 0)"
if [ "$seed_cards" -lt "$MIN_SEED_CARDS" ]; then
  unread "catalog-seed has $seed_cards card(s), expected >= $MIN_SEED_CARDS — refusing to believe this tree"
fi
[ "$C_READ" -eq 0 ] || { echo; echo "VERDICT: CANNOT READ (exit 2)"; exit 2; }
note "tree floors ok: $kit_slots kit slots, $seed_cards seed cards"
echo

# ── refresh the baseline ───────────────────────────────────────────────────
# main moves under long-running work — #6316 merged while this script's own
# first live run was in flight. A pre-flight that reads a remote-tracking ref
# nobody refreshed is reporting yesterday's train.
if [ "$DO_GHCR" = 1 ] && [ "$DO_FETCH" = 1 ]; then
  if git fetch --quiet origin "${BASELINE_REF#origin/}" 2>/dev/null; then
    note "baseline refreshed: $BASELINE_REF @ $(git rev-parse --short "$BASELINE_REF" 2>/dev/null || echo '?')"
  else
    note "could not fetch $BASELINE_REF — using the remote-tracking ref as it stands (may be stale)"
  fi
else
  note "baseline NOT refreshed (--no-fetch or --no-ghcr): $BASELINE_REF @ $(git rev-parse --short "$BASELINE_REF" 2>/dev/null || echo '?')"
fi
echo

# ── discover the train ─────────────────────────────────────────────────────
# The train is DERIVED, not hardcoded: any chart whose Chart.yaml differs from
# its state at the merge-base of origin/main is in motion locally, plus every
# chart that any open PR bumps above main. On main itself the local delta is
# empty, so discovery leans on the open-PR claims.
declare -a TRAIN_DIRS=()
if [ -n "$TRAIN_OVERRIDE" ]; then
  IFS=',' read -r -a TRAIN_DIRS <<< "$TRAIN_OVERRIDE"
else
  while IFS= read -r d; do
    [ -n "$d" ] || continue
    TRAIN_DIRS+=("$d")
  done < <(
    git ls-files 'platform/*/chart/Chart.yaml' 'products/*/chart/Chart.yaml' 2>/dev/null \
      | sed 's#/chart/Chart.yaml$##' | sort -u
  )
fi
if [ "${#TRAIN_DIRS[@]}" -eq 0 ]; then
  unread "discovered 0 chart directories — cannot proceed"
  echo; echo "VERDICT: CANNOT READ (exit 2)"; exit 2
fi
note "discovered ${#TRAIN_DIRS[@]} chart director(ies) in the tree"
echo

# ───────────────────────────── CHECK 1 ─────────────────────────────────────
echo "── CHECK 1 — versions move forward ─────────────────────────────────────"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

declare -A MAIN_VER=()
declare -A CHART_NAME=()
for d in "${TRAIN_DIRS[@]}"; do
  v="$(tc_site1 "$REPO_ROOT" "$d")"
  n="$(tc_chart_name "$REPO_ROOT" "$d")"
  [ -n "$v" ] || continue
  MAIN_VER["$d"]="$v"
  CHART_NAME["$d"]="$n"
done

# 1a — no chart regressed across its own history on this ref.
mono_bad=0; mono_checked=0
for d in "${TRAIN_DIRS[@]}"; do
  [ -n "${MAIN_VER[$d]:-}" ] || continue
  f="$d/chart/Chart.yaml"
  : > "$TMP/series"
  # oldest first; one version per revision that touched the file
  while IFS= read -r sha; do
    [ -n "$sha" ] || continue
    git show "$sha:$f" 2>/dev/null \
      | awk '/^version:[[:space:]]/ { gsub(/^version:[[:space:]]*/,""); sub(/[[:space:]]+#.*$/,""); gsub(/^[[:space:]]+|[[:space:]]+$/,""); gsub(/["'"'"']/,""); print; exit }' \
      >> "$TMP/series" || true
  done < <(git rev-list --max-count="$HISTORY_DEPTH" "$REF" -- "$f" 2>/dev/null | tac)
  [ -s "$TMP/series" ] || continue
  mono_checked=$((mono_checked+1))
  # Hard gate: is main behind ITSELF right now?
  prev="$(tc_errexit_state)"; set +e
  hout="$(tc_check_head_not_behind "$TMP/series")"; hrc=$?
  tc_errexit_restore "$prev"
  if [ "$hrc" -eq 1 ]; then
    fail "1a ${CHART_NAME[$d]} is BEHIND ITSELF: $hout"
    mono_bad=$((mono_bad+1))
  fi
  # Informational: a dip that has since been surpassed. Reported, never fatal —
  # it cannot endanger a prov fired from today's HEAD.
  prev="$(tc_errexit_state)"; set +e
  out="$(tc_check_monotonic "$TMP/series")"; rc=$?
  tc_errexit_restore "$prev"
  if [ "$rc" -eq 1 ] && [ "$hrc" -eq 0 ]; then
    note "1a ${CHART_NAME[$d]} had a historical dip, since surpassed (HEAD=${MAIN_VER[$d]}): $(printf '%s' "$out" | tr '\n' ';')"
  fi
done
if [ "$mono_checked" -eq 0 ]; then
  unread "1a walked 0 chart histories — git history unreadable?"
else
  [ "$mono_bad" -gt 0 ] || ok "1a no regression across $mono_checked chart histor(ies), depth $HISTORY_DEPTH"
fi

# 1b — open-PR claims. DELEGATED enumerator: check-chart-version-not-claimed-
# by-open-pr.py is the repo's single reader of "what an open PR claims", shared
# with scripts/bump-chart-version.sh so the writer and the checker can never
# disagree. We consume its output; we do not re-derive it.
declare -A AHEAD_CLAIMS=()
if [ "$DO_GHCR" = 1 ]; then
  ENUM="$SCRIPT_DIR/check-chart-version-not-claimed-by-open-pr.py"
  if [ ! -x "$ENUM" ] && [ ! -f "$ENUM" ]; then
    unread "1b enumerator missing: $ENUM"
  else
    for d in "${TRAIN_DIRS[@]}"; do
      [ -n "${MAIN_VER[$d]:-}" ] || continue
      # only ask about charts that actually have open motion: the umbrella and
      # anything an open PR bumps. Asking for all ~67 would be ~67 API sweeps.
      case "$d" in
        products/catalyst|products/agenity|platform/wordpress-tenant|platform/self-sovereign-cutover) ;;
        *) continue ;;
      esac
      prev="$(tc_errexit_state)"; set +e
      claims_raw="$(python3 "$ENUM" --claimed-versions "$d/chart/Chart.yaml" 2>&1)"; rc=$?
      tc_errexit_restore "$prev"
      if [ "$rc" -ne 0 ]; then
        unread "1b enumerator failed for $d (rc=$rc)"
        continue
      fi
      printf '%s\n' "$claims_raw" | awk '/^[0-9]+[[:space:]]+[0-9]+\.[0-9]+\.[0-9]+$/ { print $1, $2 }' \
        > "$TMP/claims.$$" || true
      nclaims="$(wc -l < "$TMP/claims.$$")"
      if [ "$nclaims" -lt "$MIN_OPEN_PRS" ]; then
        unread "1b enumerator returned $nclaims claim(s) for $d — a zero from a failed read is not 'no open PRs'"
        continue
      fi
      # Measure live claims against the LIVE baseline, never the local tree.
      base_ver="$(tc_version_at_ref "$BASELINE_REF" "$d")"
      if [ -z "$base_ver" ]; then
        unread "1b cannot read $d/chart/Chart.yaml at $BASELINE_REF — refusing to compare live claims against an unknown baseline"
        continue
      fi
      if [ "$base_ver" != "${MAIN_VER[$d]}" ]; then
        note "1b ${CHART_NAME[$d]}: working tree is at ${MAIN_VER[$d]} but $BASELINE_REF is at $base_ver — using $base_ver as the baseline"
      fi
      prev="$(tc_errexit_state)"; set +e
      cout="$(tc_check_distinct_claims "$TMP/claims.$$" "$base_ver")"; rc=$?
      tc_errexit_restore "$prev"
      ahead="$(printf '%s\n' "$cout" | awk -F= '/^__AHEAD__=/ { print $2 }')"
      AHEAD_CLAIMS["$d"]="$(awk -v m="${MAIN_VER[$d]}" '{print $1"="$2}' "$TMP/claims.$$" | tr '\n' ' ')"
      if [ "$rc" -ne 0 ]; then
        fail "1b ${CHART_NAME[$d]}: $(printf '%s' "$cout" | grep -v '^__AHEAD__' | tr '\n' ';')"
      else
        ok "1b ${CHART_NAME[$d]}: $BASELINE_REF=$base_ver, ${ahead:-0} distinct future claim(s), no collision"
      fi
    done
  fi
else
  note "1b skipped (--no-ghcr: the claim enumerator needs the GitHub API)"
fi
echo

# ───────────────────────────── CHECK 2 ─────────────────────────────────────
echo "── CHECK 2 — no hollow pin (bootstrap-kit + umbrella republish) ────────"
note "delegating to the existing guards; their predicates are not re-derived here"
tc_run_guard "check-bootstrap-kit-pin-sync.sh (full sweep)" \
  bash "$SCRIPT_DIR/check-bootstrap-kit-pin-sync.sh"
tc_run_guard "check-umbrella-republish-gate.py --ref $REF" \
  python3 "$SCRIPT_DIR/check-umbrella-republish-gate.py" --ref "$REF"
echo

# ───────────────────────────── CHECK 3 ─────────────────────────────────────
echo "── CHECK 3 — every delivery pin resolves on GHCR ───────────────────────"
declare -A PIN_STATE=()
N_PENDING=0
N_MISSING=0
if [ "$DO_GHCR" = 0 ]; then
  note "skipped (--no-ghcr)"
else
  tc_run_guard "check-bootstrap-kit-pin-sync.sh --check-ghcr" \
    bash "$SCRIPT_DIR/check-bootstrap-kit-pin-sync.sh" --check-ghcr --ghcr-org "$GHCR_ORG"
  tc_run_guard "check-catalog-seed-lockstep.sh --check-ghcr" \
    bash "$SCRIPT_DIR/check-catalog-seed-lockstep.sh" --check-ghcr --ghcr-org "$GHCR_ORG"

  # The delegated guards answer "does the pin resolve?". They deliberately do
  # NOT answer "and if not, is that because it merged 90 seconds ago?" — that
  # is this script's job, and getting it wrong in either direction is the whole
  # point of the four-state classifier.
  echo
  note "classifying each train chart's version against GHCR"
  if ! command -v gh >/dev/null 2>&1; then
    unread "gh not on PATH — cannot classify publish state"
  else
    for d in "${TRAIN_DIRS[@]}"; do
      case "$d" in
        products/catalyst|products/agenity|platform/wordpress-tenant|platform/self-sovereign-cutover) ;;
        *) continue ;;
      esac
      name="${CHART_NAME[$d]:-}"; ver="${MAIN_VER[$d]:-}"
      [ -n "$name" ] && [ -n "$ver" ] || continue
      # Two-stage on purpose. bp-catalyst-platform carries ~1500 versions, so a
      # blind `--paginate` costs minutes for that one chart, and a pre-flight
      # nobody is willing to run is worth nothing. The versions API returns
      # NEWEST FIRST, so one page settles the healthy case outright: a hit on
      # page 1 is a definitive PUBLISHED. Only a MISS escalates to the full
      # paginated sweep — because "the tag is absent" is precisely the verdict
      # that must never be reached from a partial read.
      : > "$TMP/tags"
      prev="$(tc_errexit_state)"; set +e
      tags_json="$(gh api "/orgs/$GHCR_ORG/packages/container/$name/versions?per_page=100" 2>/dev/null)"
      grc=$?
      tc_errexit_restore "$prev"
      if [ "$grc" -eq 0 ] && [ -n "$tags_json" ]; then
        printf '%s' "$tags_json" | jq -r '.[].metadata.container.tags[]?' 2>/dev/null \
          | grep -vE '^sha256-' | sort -u > "$TMP/tags" || true
      fi
      first_hit="$(grep -cx -- "$ver" "$TMP/tags" 2>/dev/null || true)"
      ntags="$(wc -l < "$TMP/tags")"
      if [ "$ntags" -eq 0 ]; then
        unread "3 $name: GHCR returned 0 usable tags — treating as unreadable, NOT as 'nothing published'"
        PIN_STATE["$d"]="UNREADABLE"
        continue
      fi
      # is this version on main?
      on_main=no
      if git rev-list --max-count=1 origin/main -- "$d/chart/Chart.yaml" >/dev/null 2>&1; then
        mv_on_main="$(git show origin/main:"$d/chart/Chart.yaml" 2>/dev/null \
          | awk '/^version:[[:space:]]/ { gsub(/^version:[[:space:]]*/,""); sub(/[[:space:]]+#.*$/,""); gsub(/^[[:space:]]+|[[:space:]]+$/,""); gsub(/["'"'"']/,""); print; exit }')"
        [ "$mv_on_main" = "$ver" ] && on_main=yes
      fi
      # ORDER MATTERS HERE, and the first cut had it backwards.
      #
      # It escalated straight from "not on the newest page" to a full paginated
      # sweep — ~3000 version objects for bp-catalyst-platform, minutes per
      # chart — and only then asked why. But "absent from GHCR" is the SYMPTOM;
      # the release run is the CAUSE, it costs one API call, and it is the thing
      # that actually decides the verdict. Measured live: bp-agenity 0.5.28 and
      # bp-catalyst-platform 1.4.1486 both merged 45 minutes earlier with the
      # release run still `queued`. Enumerating three thousand tags to explain
      # that is backwards, and it made the common case — a train with a fresh
      # merge on it, i.e. every train this script exists for — the slow one.
      #
      # So: cheap and direct first, expensive and exhaustive only when the cheap
      # answer says the publish already had its chance. Absence is still never
      # asserted from a partial read; it is just no longer the FIRST thing paid
      # for.
      wf_state="running"
      if [ "${first_hit:-0}" -eq 0 ] && [ "$on_main" = "yes" ]; then
        setter="$(tc_setting_commit "$BASELINE_REF" "$d" "$ver")"
        if [ -z "$setter" ]; then
          unread "3 $name:$ver — cannot locate the commit that set this version; refusing to rule on the publish"
          PIN_STATE["$d"]="UNREADABLE"
          continue
        fi
        age=$(( $(date -u +%s) - $(git show -s --format=%ct "$setter") ))
        : > "$TMP/runs.json"
        prev="$(tc_errexit_state)"; set +e
        gh api "/repos/$REPO_SLUG/actions/workflows/blueprint-release.yaml/runs?head_sha=$setter&per_page=20" \
          > "$TMP/runs.json" 2>/dev/null
        tc_errexit_restore "$prev"
        raw_state="$(tc_run_state_from_json "$TMP/runs.json" "$age")"
        wf_state="$(tc_wf_state_word "$raw_state")"
        note "3 $name:$ver — release run at ${setter:0:9} (age ${age}s): $raw_state"
        if [ "$wf_state" = "unreadable" ]; then
          unread "3 $name:$ver — could not read blueprint-release runs for ${setter:0:9}"
          PIN_STATE["$d"]="UNREADABLE"
          continue
        fi
        # Only now, with the run reported finished, is exhaustive proof of
        # absence worth its cost — and it is REQUIRED, because MISSING is the
        # verdict that must never come from a partial read. A hit here means
        # page 1 was merely out of publication order (the b76f3ca7b shape).
        if [ "$wf_state" = "completed" ]; then
          note "3 $name:$ver — release run finished and the tag is not on the newest page; sweeping all pages to prove absence"
          prev="$(tc_errexit_state)"; set +e
          tags_json="$(gh api "/orgs/$GHCR_ORG/packages/container/$name/versions" --paginate 2>/dev/null)"
          grc=$?
          tc_errexit_restore "$prev"
          if [ "$grc" -eq 0 ] && [ -n "$tags_json" ]; then
            : > "$TMP/tags"
            printf '%s' "$tags_json" | jq -r '.[].metadata.container.tags[]?' 2>/dev/null \
              | grep -vE '^sha256-' | sort -u > "$TMP/tags" || true
          else
            unread "3 $name:$ver — full sweep failed; refusing to call a tag absent from a partial read"
            PIN_STATE["$d"]="UNREADABLE"
            continue
          fi
        fi
      fi
      state="$(tc_classify_pin "$ver" "$TMP/tags" "$on_main" "$wf_state")"
      PIN_STATE["$d"]="$state"
      case "$state" in
        PUBLISHED)       ok   "3 $name:$ver PUBLISHED on ghcr.io/$GHCR_ORG" ;;
        PENDING-MERGE)   note "3 $name:$ver PENDING-MERGE — not on main; blueprint-release is on:push→main so it CANNOT be published yet"
                         N_PENDING=$((N_PENDING+1)) ;;
        PENDING-PUBLISH) note "3 $name:$ver PENDING-PUBLISH — on main, release run still in flight. Remedy: WAIT and re-run. Do NOT re-fire blueprint-release."
                         N_PENDING=$((N_PENDING+1)) ;;
        MISSING)         fail "3 $name:$ver MISSING — on main, release run finished, tag absent after a full sweep (lost publish). Remedy: re-fire blueprint-release on ${setter:0:9}."
                         N_MISSING=$((N_MISSING+1)) ;;
        UNREADABLE)      unread "3 $name:$ver — tag list unreadable" ;;
      esac
    done
  fi
fi
echo

# ───────────────────────────── CHECK 4 ─────────────────────────────────────
echo "── CHECK 4 — the five lockstep sites agree ─────────────────────────────"
sites_bad=0; sites_checked=0
for d in "${TRAIN_DIRS[@]}"; do
  [ -n "${MAIN_VER[$d]:-}" ] || continue
  prev="$(tc_errexit_state)"; set +e
  out="$(tc_check_sites "$REPO_ROOT" "$d" "${CHART_NAME[$d]}")"; rc=$?
  tc_errexit_restore "$prev"
  if [ "$rc" -eq 2 ]; then
    unread "4 $d: $out"
    continue
  fi
  sites_checked=$((sites_checked+1))
  if [ "$rc" -ne 0 ]; then
    sites_bad=$((sites_bad+1))
    fail "4 ${CHART_NAME[$d]} (sites 1-3): $(printf '%s' "$out" | grep -vE 'no-op by design|not seeded' | tr '\n' ';')"
  fi
done
if [ "$sites_checked" -eq 0 ]; then
  unread "4 checked 0 charts — cannot believe this tree"
else
  [ "$sites_bad" -gt 0 ] || ok "4 sites 1-3 agree across $sites_checked chart(s)"
fi

# sites 4+5 — regenerate and prove no diff
prev="$(tc_errexit_state)"; set +e
gout="$(tc_check_generated "$REPO_ROOT")"; grc=$?
tc_errexit_restore "$prev"
case "$grc" in
  0) ok "4 sites 4+5 match a fresh build-catalog.mjs regeneration (byte-identical)" ;;
  2) unread "4 sites 4+5 could not be regenerated: $gout" ;;
  *) fail "4 sites 4+5: $(printf '%s' "$gout" | tr '\n' ';')" ;;
esac
echo

# ── fire-readiness statement ───────────────────────────────────────────────
if [ "$DO_FIRE" = 1 ]; then
  echo "════════════════════════════════════════════════════════════════════════"
  echo " FIRE-READINESS — what a fresh Sovereign installs if fired NOW"
  echo " generated $(date -u +%Y-%m-%dT%H:%M:%SZ) from ref $REF ($(git rev-parse --short "$REF"))"
  echo "════════════════════════════════════════════════════════════════════════"
  printf ' %-28s %-12s %-12s %-16s\n' "CHART" "ON MAIN" "KIT PIN" "GHCR"
  printf ' %-28s %-12s %-12s %-16s\n' "----------------------------" "------------" "------------" "----------------"
  fire_divergent=0
  for d in "${TRAIN_DIRS[@]}"; do
    case "$d" in
      products/catalyst|products/agenity|platform/wordpress-tenant|platform/self-sovereign-cutover) ;;
      *) continue ;;
    esac
    n="${CHART_NAME[$d]:-?}"
    # "ON MAIN" must mean ON MAIN. A fresh prov installs what main carries, not
    # what happens to be checked out in whoever ran this — and the two diverge
    # the moment anything merges. Reading the working tree here would let the
    # statement an operator fires on describe a branch nobody is deploying.
    v="$(tc_version_at_ref "$BASELINE_REF" "$d")"
    if [ -z "$v" ]; then v="(unreadable @ $BASELINE_REF)"; fi
    if [ "$v" != "${MAIN_VER[$d]:-}" ]; then fire_divergent=$((fire_divergent+1)); fi
    kp="$(tc_kit_pin "$REPO_ROOT" "$n")"; [ -n "$kp" ] || kp="(no slot)"
    printf ' %-28s %-12s %-12s %-16s\n' "$n" "$v" "$kp" "${PIN_STATE[$d]:-unchecked}"
  done
  echo
  echo " ON MAIN is read from $BASELINE_REF @ $(git rev-parse --short "$BASELINE_REF" 2>/dev/null || echo '?')"
  echo " AT THE MOMENT THIS TABLE WAS PRINTED. The GHCR column is the verdict"
  echo " check 3 reached for the version it measured minutes earlier — on a busy"
  echo " repo main can move in between (it did, mid-run, more than once while"
  echo " this was being written). If a row's ON MAIN is newer than what check 3"
  echo " named above, the GHCR cell describes the OLDER version: re-run."
  if [ "$fire_divergent" -gt 0 ]; then
    echo " NOTE: $fire_divergent chart(s) differ between this working tree and"
    echo " $BASELINE_REF. The KIT PIN column is read from THIS TREE, so on a"
    echo " divergent checkout the two columns are not describing the same commit."
  fi
  echo
  echo " The fresh Sovereign installs the KIT PIN column. A chart is safe to"
  echo " fire on when ON MAIN == KIT PIN and GHCR == PUBLISHED."
  echo
  if [ "$DO_GHCR" = 1 ]; then
    echo " Still waiting on a merge (cannot be published until then — the release"
    echo " workflow is on:push→branches:[main]):"
    for d in "${TRAIN_DIRS[@]}"; do
      [ -n "${AHEAD_CLAIMS[$d]:-}" ] || continue
      mv="${MAIN_VER[$d]}"
      pend="$(printf '%s' "${AHEAD_CLAIMS[$d]}" | tr ' ' '\n' | awk -F= -v m="$mv" 'NF==2' | while IFS='=' read -r p v; do
        [ -n "$v" ] || continue
        if tc_semver_gt "$v" "$mv"; then printf '#%s→%s ' "$p" "$v"; fi
      done)"
      [ -n "$pend" ] && printf '   %-28s %s\n' "${CHART_NAME[$d]}" "$pend"
    done
  fi
  echo
fi

# ── verdict ────────────────────────────────────────────────────────────────
echo "════════════════════════════════════════════════════════════════════════"
if [ "$C_READ" -gt 0 ]; then
  echo " VERDICT: CANNOT READ — $C_READ input(s) unreadable, $C_FAIL failure(s)."
  echo " This is NOT a pass. Re-run once the unreadable inputs are reachable."
  echo "════════════════════════════════════════════════════════════════════════"
  exit 2
fi
if [ "$C_FAIL" -gt 0 ]; then
  echo " VERDICT: INCOHERENT — $C_FAIL check failure(s). Do NOT fire a fresh prov."
  # Both of these mean "do not fire yet", and the exit code is the same — but
  # the REMEDY is opposite, so the two must never be reported as one thing.
  if [ "$N_MISSING" -gt 0 ]; then
    echo
    echo " $N_MISSING pin(s) are a LOST PUBLISH: the release run finished and the tag"
    echo " is still absent after a full sweep. Waiting will not fix these — re-fire"
    echo " blueprint-release.yaml on the commit that bumped the chart."
  fi
  if [ "$N_PENDING" -gt 0 ] && [ "$N_MISSING" -eq 0 ]; then
    echo
    echo " $N_PENDING pin(s) are simply NOT PUBLISHED YET — the release run is still"
    echo " in flight, or the bump has not merged. Nothing is broken; the train is"
    echo " mid-flight. Re-run this pre-flight once the run finishes. Re-firing"
    echo " blueprint-release now would be the wrong move."
  fi
  echo "════════════════════════════════════════════════════════════════════════"
  exit 1
fi
# A PARTIAL run must never issue a full run's verdict.
#
# --no-ghcr skips check 1b (open-PR claims) and all of check 3 (does the pin
# actually resolve). What remains is real and worth having — it is the whole
# structural half — but "no failures among the checks I ran" is not "safe to
# fire", and printing the same green sentence for both is how a fast mode
# quietly becomes the mode everyone uses. Same discipline as refusing to let a
# failed read read as a pass: say what was measured.
if [ "$DO_GHCR" = 0 ]; then
  echo " VERDICT: PARTIAL PASS — no failures in the checks that ran."
  echo
  echo " NOT a fire clearance. --no-ghcr skipped:"
  echo "   · check 1b — whether two open PRs claim the same chart version"
  echo "   · check 3  — whether the delivery pins resolve on GHCR at all"
  echo " Both need the GitHub API. Re-run without --no-ghcr before firing."
  echo "════════════════════════════════════════════════════════════════════════"
  exit 0
fi
echo " VERDICT: COHERENT — the train is safe to fire."
echo "════════════════════════════════════════════════════════════════════════"
exit 0
