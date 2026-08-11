#!/usr/bin/env bash
# test-bump-chart-version-open-pr-claims.sh — non-vacuity proof for #5583/#5734
#
# WHAT THIS PINS
# --------------
# scripts/bump-chart-version.sh used to compute
#
#     next = bump_patch(max(origin/main version, origin/main appVersion))
#
# reading the baseline from origin/main ONLY. It had no view of what other open
# branches already claim, so ANY TWO PRs rebased in the same window land on the
# SAME version, deterministically. That happened five times on 2026-08-10:
# #6059/#6068 twice, #6089/#6068, and a three-way #6099/#6096/#6068 all
# computing 1.4.1353.
#
# The damage is not the merge conflict. Whichever PR merges first publishes that
# artifact to ghcr; the second then merges carrying DIFFERENT content under a
# version already published, the registry keeps the first push, and the second
# PR's change renders clean, passes every gate, and ships nothing.
#
# scripts/check-chart-version-not-claimed-by-open-pr.py already detects that —
# but only at CI time, after the rebase and the push. The writer that CREATES
# the collision could not see what the checker can. This test pins the fix:
# the writer now consults the same enumerator the checker uses.
#
#   W1  THE DECISIVE CASE. origin/main at 1.4.1352; two OPEN PRs both carry
#       1.4.1353 on their heads. Pre-fix the writer returns 1.4.1353 — the
#       collision it is supposed to prevent. Post-fix it must return 1.4.1354.
#   W2  CONTROL — no open PRs at all: the answer must be UNCHANGED from the
#       pre-fix arithmetic (1.4.1353). The fix adds a constraint; it must not
#       change the arithmetic.
#   W3  CONTROL — an open PR exists but never bumped this chart (its head
#       carries main's own 1.4.1352): still 1.4.1353. A sibling only binds
#       when it actually claims ahead.
#   W4  FAIL LOUD — PR enumeration fails: non-zero exit AND the working copy
#       must be left untouched. Never a silent fall back to the main-only
#       answer, which is exactly the colliding behaviour.
#   W5  FAIL LOUD — `gh` not on PATH at all: same.
#   W6  OPT-OUT — --allow-unchecked-siblings with enumeration broken: exit 0,
#       the main-only answer, and a loud warning on stderr.
#   W7  An open PR whose head branch was DELETED from origin is unmergeable,
#       so its version claim is void — not a hole. Must not wedge the writer.
#   W8  A cross-repository (fork) PR head cannot be read from origin, so its
#       claim is UNKNOWN — that IS a hole. Must fail loud.
#   W9  A shallow, single-branch clone — what actions/checkout@v4 produces by
#       default — must still read sibling claims. `git fetch origin <branch>`
#       exits 0 there but leaves NO origin/<branch> ref, which would resolve
#       every sibling to "no claim" and pass by comparing nothing.
#   W10 The existing write contract survives: both `version:` and
#       `appVersion:` are written, in lockstep, to the computed value.
#
# Usage: bash scripts/tests/test-bump-chart-version-open-pr-claims.sh

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/../.." && pwd)"
BUMP="${REPO_ROOT}/scripts/bump-chart-version.sh"
CLAIMS="${REPO_ROOT}/scripts/check-chart-version-not-claimed-by-open-pr.py"

for f in "${BUMP}"; do
  [ -x "${f}" ] || chmod +x "${f}"
done

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

FAILED=0
fail() { echo "FAIL: $*" >&2; FAILED=1; }
pass() { echo "PASS: $*"; }

CHART_PATH="products/catalyst/chart/Chart.yaml"

# ── fixture: a bare "origin" with main at 1.4.1352 and three branches ────────
UPSTREAM="${WORK}/upstream.git"
git init -q -b main --bare "${UPSTREAM}"

SEED="${WORK}/seed"
git clone -q "${UPSTREAM}" "${SEED}" 2>/dev/null
mkdir -p "${SEED}/$(dirname "${CHART_PATH}")"
write_chart() {
  # $1 = repo dir, $2 = version
  cat > "$1/${CHART_PATH}" <<YAML
apiVersion: v2
name: bp-catalyst-platform
version: $2
appVersion: $2
YAML
}
gitc() { git -C "$1" -c user.name=seed -c user.email=seed@test.local "${@:2}"; }

write_chart "${SEED}" 1.4.1352
gitc "${SEED}" add -A
gitc "${SEED}" commit -qm "seed: 1.4.1352/1.4.1352"
gitc "${SEED}" push -q -u origin main

# two sibling branches that BOTH claim 1.4.1353 — the real 2026-08-10 shape
for b in pr-a pr-b; do
  gitc "${SEED}" checkout -q -b "${b}" main
  write_chart "${SEED}" 1.4.1353
  gitc "${SEED}" commit -qam "${b}: claim 1.4.1353"
  gitc "${SEED}" push -q origin "${b}"
done
# a branch that touches something else and never bumps the chart
gitc "${SEED}" checkout -q -b pr-nochart main
echo "docs" > "${SEED}/NOTES.md"
gitc "${SEED}" add -A
gitc "${SEED}" commit -qm "pr-nochart: no chart change"
gitc "${SEED}" push -q origin pr-nochart
gitc "${SEED}" checkout -q main

# ── stub gh: answers `gh pr list ... --json ...` from a file ─────────────────
BIN="${WORK}/bin"
mkdir -p "${BIN}"
cat > "${BIN}/gh" <<EOF
#!/usr/bin/env bash
# stub gh for the fixture — the fixture's "origin" is a local bare repo, so the
# real gh (which would answer about openova-io/openova) is meaningless here.
RC_FILE="${WORK}/gh.rc"
if [ -f "\${RC_FILE}" ]; then
  echo "stub gh: forced failure (enumeration unavailable)" >&2
  exit "\$(cat "\${RC_FILE}")"
fi
if [ "\${1:-}" = "pr" ] && [ "\${2:-}" = "list" ]; then
  cat "${WORK}/pr-list.json"
  exit 0
fi
echo "stub gh: unexpected invocation: \$*" >&2
exit 1
EOF
chmod +x "${BIN}/gh"

set_prs() { printf '%s\n' "$1" > "${WORK}/pr-list.json"; rm -f "${WORK}/gh.rc"; }
break_gh() { printf '%s\n' "$1" > "${WORK}/gh.rc"; }

# ── harness ─────────────────────────────────────────────────────────────────
# Each case gets a FRESH clone, because bump-chart-version.sh writes into the
# working tree it is handed.
CASE_N=0
run_bump() {
  # $1 = clone flags ("" or "shallow"), rest = extra args to bump
  CASE_N=$((CASE_N + 1))
  RUN_DIR="${WORK}/run${CASE_N}"
  if [ "$1" = "shallow" ]; then
    git clone -q --depth=1 --single-branch --branch main "file://${UPSTREAM}" "${RUN_DIR}"
  else
    git clone -q "${UPSTREAM}" "${RUN_DIR}"
  fi
  shift
  ( cd "${RUN_DIR}" && PATH="${BIN}:${PATH}" "${BUMP}" "$@" "${CHART_PATH}" ) \
    > "${RUN_DIR}.stdout" 2> "${RUN_DIR}.stderr"
  RUN_RC=$?
  RUN_OUT="$(cat "${RUN_DIR}.stdout")"
  return 0
}

file_version() { awk '/^version:/{print $2; exit}' "$1/${CHART_PATH}"; }
file_appversion() { awk '/^appVersion:/{print $2; exit}' "$1/${CHART_PATH}"; }

echo "=============================================================="
echo "W1 — DECISIVE: two OPEN PRs both already claim 1.4.1353"
echo "     origin/main is 1.4.1352. Pre-fix answer: 1.4.1353 (collision)."
echo "=============================================================="
set_prs '[{"number":9001,"headRefName":"pr-a","isCrossRepository":false},
          {"number":9002,"headRefName":"pr-b","isCrossRepository":false}]'
run_bump full
echo "--- stderr ---"; cat "${RUN_DIR}.stderr"
echo "--- exit=${RUN_RC} next=${RUN_OUT} ---"
if [ "${RUN_RC}" -ne 0 ]; then
  fail "W1 — writer exited ${RUN_RC}; it must produce a free version, not refuse."
elif [ "${RUN_OUT}" = "1.4.1353" ]; then
  fail "W1 — writer returned 1.4.1353, the version BOTH open PRs already claim. This is the #5583 collision: whichever merges first publishes it, the second ships nothing."
elif [ "${RUN_OUT}" != "1.4.1354" ]; then
  fail "W1 — expected 1.4.1354 (next patch above the highest open-PR claim 1.4.1353), got '${RUN_OUT}'."
else
  pass "W1 — writer stepped over both open claims: 1.4.1352 baseline + claims{1.4.1353} -> 1.4.1354."
fi
echo

echo "=============================================================="
echo "W10 — the existing write contract still holds on that run"
echo "=============================================================="
W1_DIR="${WORK}/run1"
GOT_V="$(file_version "${W1_DIR}")"; GOT_A="$(file_appversion "${W1_DIR}")"
echo "written: version=${GOT_V} appVersion=${GOT_A}"
if [ "${GOT_V}" != "${RUN_OUT}" ] || [ "${GOT_A}" != "${RUN_OUT}" ]; then
  fail "W10 — both fields must be written in lockstep to ${RUN_OUT}; got version=${GOT_V} appVersion=${GOT_A}."
else
  pass "W10 — version and appVersion both written to ${RUN_OUT}, in lockstep."
fi
echo

echo "=============================================================="
echo "W2 — CONTROL: no open PRs. The arithmetic must be UNCHANGED."
echo "=============================================================="
set_prs '[]'
run_bump full
echo "--- exit=${RUN_RC} next=${RUN_OUT} ---"
if [ "${RUN_RC}" -ne 0 ] || [ "${RUN_OUT}" != "1.4.1353" ]; then
  echo "--- stderr ---"; cat "${RUN_DIR}.stderr"
  fail "W2 — with no siblings the answer must stay exactly today's 1.4.1353 (exit ${RUN_RC}, got '${RUN_OUT}'). The fix must ADD a constraint, not change the arithmetic."
else
  pass "W2 — no open PRs: 1.4.1352 -> 1.4.1353, identical to the pre-fix behaviour."
fi
echo

echo "=============================================================="
echo "W3 — CONTROL: an open PR that never bumped this chart"
echo "=============================================================="
set_prs '[{"number":9003,"headRefName":"pr-nochart","isCrossRepository":false}]'
run_bump full
echo "--- exit=${RUN_RC} next=${RUN_OUT} ---"
if [ "${RUN_RC}" -ne 0 ] || [ "${RUN_OUT}" != "1.4.1353" ]; then
  echo "--- stderr ---"; cat "${RUN_DIR}.stderr"
  fail "W3 — a sibling carrying main's own version claims nothing ahead; the answer must stay 1.4.1353 (exit ${RUN_RC}, got '${RUN_OUT}')."
else
  pass "W3 — sibling at main's version does not inflate the bump."
fi
echo

echo "=============================================================="
echo "W4 — FAIL LOUD: enumeration fails -> non-zero, nothing written"
echo "=============================================================="
break_gh 1
run_bump full
echo "--- stderr ---"; cat "${RUN_DIR}.stderr"
echo "--- exit=${RUN_RC} stdout='${RUN_OUT}' ---"
LEFT_V="$(file_version "${RUN_DIR}")"
if [ "${RUN_RC}" -eq 0 ]; then
  fail "W4 — writer exited 0 with the enumeration broken. It returned '${RUN_OUT}' having consulted nothing: a version writer that silently degrades to the colliding behaviour is worse than one that stops."
elif [ "${LEFT_V}" != "1.4.1352" ]; then
  fail "W4 — writer refused but still wrote ${LEFT_V} into the working copy; it must leave the tree untouched."
else
  pass "W4 — enumeration failure is fatal (exit ${RUN_RC}) and the working copy is untouched."
fi
echo

echo "=============================================================="
echo "W5 — FAIL LOUD: gh not on PATH at all"
echo "=============================================================="
# A PATH built from symlinks to exactly the tools the writer needs, and no gh.
# Hardcoding /usr/bin:/bin would not do: on a GitHub runner gh sits in /usr/bin
# next to git, so the case would silently test nothing.
NOGH_BIN="${WORK}/nogh-bin"
mkdir -p "${NOGH_BIN}"
for t in bash env git awk sed tr cut cat rm mktemp dirname; do
  SRC="$(command -v "${t}" 2>/dev/null || true)"
  [ -n "${SRC}" ] && ln -sf "${SRC}" "${NOGH_BIN}/${t}"
done
SRC="$(command -v python3 2>/dev/null || true)"
[ -n "${SRC}" ] && ln -sf "${SRC}" "${NOGH_BIN}/python3"
if PATH="${NOGH_BIN}" command -v gh >/dev/null 2>&1; then
  fail "W5 VACUITY — gh is still reachable under the stripped PATH; this case cannot test the absence it exists to test."
else
  echo "vacuity check: gh is NOT reachable under the stripped PATH."
fi
CASE_N=$((CASE_N + 1))
RUN_DIR="${WORK}/run${CASE_N}"
git clone -q "${UPSTREAM}" "${RUN_DIR}"
( cd "${RUN_DIR}" && PATH="${NOGH_BIN}" "${BUMP}" "${CHART_PATH}" ) \
  > "${RUN_DIR}.stdout" 2> "${RUN_DIR}.stderr"
RC5=$?
echo "--- stderr ---"; cat "${RUN_DIR}.stderr"
echo "--- exit=${RC5} ---"
if [ "${RC5}" -eq 0 ]; then
  fail "W5 — writer exited 0 with no gh available; absence of the enumerator must be fatal, not a silent main-only answer."
else
  pass "W5 — missing gh is fatal (exit ${RC5})."
fi
echo

echo "=============================================================="
echo "W6 — OPT-OUT: --allow-unchecked-siblings, loudly"
echo "=============================================================="
break_gh 1
run_bump full --allow-unchecked-siblings
echo "--- stderr ---"; cat "${RUN_DIR}.stderr"
echo "--- exit=${RUN_RC} next=${RUN_OUT} ---"
if [ "${RUN_RC}" -ne 0 ] || [ "${RUN_OUT}" != "1.4.1353" ]; then
  fail "W6 — the explicit opt-out must still produce the main-only answer 1.4.1353 (exit ${RUN_RC}, got '${RUN_OUT}')."
elif ! grep -qi "warning" "${RUN_DIR}.stderr"; then
  fail "W6 — the opt-out took effect with no loud warning on stderr. An unchecked bump must announce itself."
else
  pass "W6 — opt-out produces the main-only answer AND warns loudly."
fi
rm -f "${WORK}/gh.rc"
echo

echo "=============================================================="
echo "W7 — an open PR whose head branch was DELETED from origin"
echo "     (unmergeable -> its claim is void, not an unknown)"
echo "=============================================================="
set_prs '[{"number":9004,"headRefName":"branch-deleted-from-origin","isCrossRepository":false}]'
run_bump full
echo "--- exit=${RUN_RC} next=${RUN_OUT} ---"
if [ "${RUN_RC}" -ne 0 ] || [ "${RUN_OUT}" != "1.4.1353" ]; then
  echo "--- stderr ---"; cat "${RUN_DIR}.stderr"
  fail "W7 — a PR whose head no longer exists on origin can never merge, so it claims nothing. The writer must not wedge on it (exit ${RUN_RC}, got '${RUN_OUT}')."
else
  pass "W7 — deleted head branch treated as a void claim, writer proceeds."
fi
echo

echo "=============================================================="
echo "W8 — a CROSS-REPOSITORY (fork) PR head is unreadable from"
echo "     origin, so its claim is UNKNOWN -> fail loud"
echo "=============================================================="
set_prs '[{"number":9005,"headRefName":"some-fork-branch","isCrossRepository":true}]'
run_bump full
echo "--- stderr ---"; cat "${RUN_DIR}.stderr"
echo "--- exit=${RUN_RC} stdout='${RUN_OUT}' ---"
if [ "${RUN_RC}" -eq 0 ]; then
  fail "W8 — a fork PR's claim cannot be read from origin. Proceeding as if it claimed nothing is the silent-hole shape; this must be fatal."
else
  pass "W8 — an unreadable fork head is reported as an unknown claim and is fatal (exit ${RUN_RC})."
fi
echo

echo "=============================================================="
echo "W9 — shallow, single-branch clone (actions/checkout@v4 default)"
echo "     must STILL see the sibling claims"
echo "=============================================================="
# `git fetch origin pr-a` exits 0 in such a clone but creates NO origin/pr-a,
# because remote.origin.fetch only maps main. A reader that resolves siblings
# through origin/<branch> silently sees no claims and passes on nothing.
set_prs '[{"number":9001,"headRefName":"pr-a","isCrossRepository":false},
          {"number":9002,"headRefName":"pr-b","isCrossRepository":false}]'
run_bump shallow
echo "--- stderr ---"; cat "${RUN_DIR}.stderr"
echo "--- exit=${RUN_RC} next=${RUN_OUT} ---"
if [ "${RUN_RC}" -ne 0 ]; then
  fail "W9 — writer exited ${RUN_RC} on a shallow single-branch clone; that is the default CI checkout shape."
elif [ "${RUN_OUT}" != "1.4.1354" ]; then
  fail "W9 — on a shallow single-branch clone the writer returned '${RUN_OUT}'; 1.4.1353 here means the sibling heads resolved to nothing and the constraint was vacuous."
else
  pass "W9 — sibling claims are read correctly from a shallow single-branch clone."
fi
echo

echo "=============================================================="
echo "W11 — the enumerator's own self-test still passes"
echo "=============================================================="
python3 "${CLAIMS}" --self-test
RC11=$?
if [ "${RC11}" -ne 0 ]; then
  fail "W11 — check-chart-version-not-claimed-by-open-pr.py --self-test exited ${RC11}."
else
  pass "W11 — enumerator self-test green."
fi
echo

if [ "${FAILED}" -ne 0 ]; then
  echo "=============================================================="
  echo "RESULT: FAILED"
  echo "=============================================================="
  exit 1
fi
echo "=============================================================="
echo "RESULT: all cases passed"
echo "=============================================================="
