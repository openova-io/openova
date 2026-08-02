#!/usr/bin/env bash
#
# check-kyverno-nodeport-exclusions.sh — §854 admission carve-out guard (#5564).
#
# WHY THIS EXISTS, AND WHY `kyverno test` CANNOT REPLACE IT
#
# The forbid-nodeport-service ClusterPolicy carves out cert-manager's HTTP-01
# solver Services. #5564: that carve-out had a SECOND clause matching the NAME
# `cm-acme-http-solver-*` with no label requirement. `exclude` is an `any:` list,
# so either clause sufficed — any Service named `cm-acme-http-solver-<anything>`
# was exempt from the NodePort ban with no cert-manager involvement at all. A
# self-service §854 bypass for anyone who can create a Service.
#
# The obvious defence is a kyverno test case asserting that an UNLABELLED
# solver-shaped Service is denied. That defence does not work, and the failure
# mode was measured rather than assumed:
#
#   policy state        expectation   `kyverno test` verdict
#   ------------------  ------------  ---------------------------------
#   glob REMOVED        result: skip  FAILS  ("Want skip, got fail")  <- discriminates
#   glob PRESENT        result: skip  passes
#   glob PRESENT        result: fail  passes                          <- THE PROBLEM
#
# With the bypass restored, the resource is EXCLUDED, and kyverno's matcher
# accepts that as satisfying `result: fail`. So a deny-assertion stays green
# after someone adds an exclusion that neuters it. That is fail-open, and it
# applies to EVERY deny case in the suite, not only this one: any future
# `exclude` clause silently keeps the corresponding test passing.
#
# Hence this guard asserts on the POLICY TEXT, where an exclusion cannot hide.
#
# WHAT IT ENFORCES
#   1. The solver carve-out is by LABEL only — no bare `cm-acme-http-solver-*`
#      name exclusion in either the Helm template or the test's policy copy.
#   2. The label selector IS still present (removing the carve-out entirely
#      would break cert issuance; this guard must not push anyone that way).
#   3. Template and test copy agree, because the suite validates a hand-kept
#      COPY of the policy — so the shipped template can drift from what is
#      tested, and did.
#
# Exit: 0 clean, 1 a bypass/drift is present, 2 self-test failed (fails closed).

set -uo pipefail

TPL="platform/kyverno-policies/chart/templates/baseline/24-forbid-nodeport-service.yaml"
COPY="platform/kyverno-policies/chart/tests/forbid-nodeport-service/policy.yaml"

GLOB_RE='names:'
SOLVER_NAME_RE='cm-acme-http-solver-\*'
LABEL_RE='acme\.cert-manager\.io/http01-solver'

# Strip BOTH comment syntaxes before asserting. `#` line comments are the
# obvious one; the non-obvious one is Helm's `{{/* ... */}}` block, which spans
# lines and is NOT a `#` comment. The policy opens with exactly such a block
# explaining the solver class and naming `cm-acme-http-solver-*` in prose — with
# only `#`-stripping this guard flagged that documentation as a bypass. Failing
# on a file for describing the rule it enforces would train people to delete the
# explanation, which is the opposite of the intent.
strip_comments() {
  python3 - "$1" <<'PY' 2>/dev/null
import re,sys
try:
    s=open(sys.argv[1]).read()
except Exception:
    sys.exit(0)
s=re.sub(r'\{\{-?/\*.*?\*/-?\}\}', '', s, flags=re.S)   # Helm block comments
s=re.sub(r'#[^\n]*', '', s)                             # YAML line comments
sys.stdout.write(s)
PY
}

# Does $1 contain a NAME-based solver exclusion in LIVE policy text?
has_name_bypass() {
  strip_comments "$1" | grep -qE "\"?${SOLVER_NAME_RE}\"?"
}
has_label_carveout() {
  strip_comments "$1" | grep -qE "${LABEL_RE}"
}

# ─── Phase 0 — self-test ────────────────────────────────────────────────
# A text assertion that never fires is worth nothing. Prove both detectors
# discriminate against fixtures before trusting them on real files.
self_test() {
  local d rc=0
  d="$(mktemp -d)"
  printf 'exclude:\n  any:\n    - resources:\n        names:\n          - "cm-acme-http-solver-*"\n' > "$d/bypass.yaml"
  printf 'exclude:\n  any:\n    - resources:\n        selector:\n          matchLabels:\n            acme.cert-manager.io/http01-solver: "true"\n' > "$d/clean.yaml"
  printf '# a comment naming cm-acme-http-solver-* to document the ban\nexclude: {}\n' > "$d/comment.yaml"
  # The case that actually bit: the policy's own Helm block comment names the
  # pattern in prose. Only `#`-stripping flagged that documentation as a bypass.
  printf '{{/*\nSolver Services (`cm-acme-http-solver-*`) are carved out below.\n*/}}\nexclude: {}\n' > "$d/helmcomment.yaml"

  has_name_bypass "$d/bypass.yaml"  || { echo "SELF-TEST FAIL: bypass fixture not detected"; rc=1; }
  has_name_bypass "$d/clean.yaml"   && { echo "SELF-TEST FAIL: clean fixture flagged"; rc=1; }
  has_name_bypass "$d/comment.yaml" && { echo "SELF-TEST FAIL: a '#' COMMENT was treated as a bypass"; rc=1; }
  has_name_bypass "$d/helmcomment.yaml" && { echo "SELF-TEST FAIL: a Helm {{/* */}} COMMENT was treated as a bypass"; rc=1; }
  has_label_carveout "$d/clean.yaml" || { echo "SELF-TEST FAIL: label carve-out not detected"; rc=1; }
  has_label_carveout "$d/bypass.yaml" && { echo "SELF-TEST FAIL: label detector fires on a name-only file"; rc=1; }

  rm -rf "$d"
  [ "$rc" -eq 0 ] && echo "OK — self-test passed (detects a name bypass, ignores comments, sees the label)."
  return "$rc"
}

if [ "${1:-}" = "--self-test" ]; then self_test; exit $?; fi
if ! self_test >/dev/null 2>&1; then
  echo "FAIL: self-test did not pass — refusing to report. Run: $0 --self-test" >&2
  exit 2
fi

EXIT=0
for f in "$TPL" "$COPY"; do
  if [ ! -f "$f" ]; then
    echo "FAIL: expected policy file missing: $f" >&2
    echo "      (a rename needs this guard updated, not deleted)" >&2
    EXIT=1; continue
  fi
  if has_name_bypass "$f"; then
    echo "FAIL: ${f} excludes Services by NAME (cm-acme-http-solver-*) — §854 bypass (#5564)." >&2
    echo "      exclude.any means this clause alone exempts ANY Service with that name," >&2
    echo "      regardless of whether cert-manager created it. Carve out by the label" >&2
    echo "      acme.cert-manager.io/http01-solver only — that is the upstream contract." >&2
    EXIT=1
  fi
  if ! has_label_carveout "$f"; then
    echo "FAIL: ${f} has no solver LABEL carve-out — genuine cert-manager solvers would be" >&2
    echo "      denied and certificate issuance would stall. Restore the matchLabels clause." >&2
    EXIT=1
  fi
done

# Template and test copy must agree: the suite validates the COPY, so drift
# means the shipped policy is untested. This is not hypothetical — the copy
# still carried the bypass after the template was fixed (#5564).
if [ -f "$TPL" ] && [ -f "$COPY" ]; then
  t="$(has_name_bypass "$TPL" && echo bypass || echo clean)"
  c="$(has_name_bypass "$COPY" && echo bypass || echo clean)"
  if [ "$t" != "$c" ]; then
    echo "FAIL: template says '${t}' but the test's policy copy says '${c}'." >&2
    echo "      kyverno test validates the COPY, so the shipped template is unverified." >&2
    EXIT=1
  fi
fi

if [ "$EXIT" -eq 0 ]; then
  echo "OK: §854 solver carve-out is label-only in both the template and the test copy (#5564)."
fi
exit "$EXIT"
