#!/usr/bin/env bash
# check-no-conflict-markers.sh — no tracked file may carry an unresolved
# git merge-conflict marker.
#
# WHY THIS EXISTS
# ---------------
# 2026-08-10, commit ad2d2e9ab: a cherry-pick of cf1a2122d (an UNMERGED
# branch that renamed the same objects a second, different way) was
# committed WITH its conflict markers still in the file. Nine conflict
# blocks landed on main across three chart templates:
#
#   products/catalyst/chart/templates/org-services/configmap.yaml       1
#   products/catalyst/chart/templates/org-services/organization.yaml    5
#   products/catalyst/chart/templates/org-services/serviceaccounts.yaml 3
#
# The cost was not cosmetic. `bp-catalyst-platform` stopped publishing:
# Blueprint Release died at "Run chart integration tests" with
#
#   Error: YAML parse error on bp-catalyst-platform/templates/
#   org-services/configmap.yaml: error converting YAML to JSON:
#   yaml: line 22: could not find expected ':'
#
# The umbrella carries the catalog-seed, so with it stuck at 1.4.1332 NO
# chart pin could reach any Sovereign — hw293's HelmRelease sat
# Ready=False `:1.4.1334: not found`. One unresolved conflict froze the
# whole release train.
#
# WHY NO EXISTING GATE CAUGHT IT
#   * The chart's default-values smoke render PASSED: the whole
#     org-services tree is gated on `.Values.ingress.marketplace.enabled`,
#     which defaults to false, so the broken file was never rendered.
#     Only a test that sets that value true reached it.
#   * The failure surfaced as a line number in the RENDERED output, which
#     points at nothing in the source file — source line 22 is inside a
#     `{{- /* ... */ -}}` comment. The message actively misdirects.
#   * A marker in a Go file, a script, or a doc would not have been caught
#     at all. Nothing in this repo looked for them.
#
# This guard is therefore repo-wide and format-agnostic: it reads bytes,
# not YAML, so it does not care whether a template is gated off, whether
# the file renders, or what language it is in.
#
# DETECTION — deliberately strict, so a pass means something
#   Flagged: a line that BEGINS a conflict (seven '<' then a space or EOL)
#   or ENDS one (seven '>' then a space or EOL). git always writes both
#   with a label, so the trailing-space form is the real shape.
#   A bare seven-'=' line is flagged ONLY inside an open block. On its own
#   it is a Setext heading underline, and this repo has four of them
#   inside Helm comment blocks (platform/hcloud-csi, platform/keycloak,
#   platform/huawei-evs-csi x2, all the "THE FIX\n=======" shape). Those
#   are the control case in the self-test below: they share the suspect
#   property — a line that IS the middle marker — and must not be flagged.
#
# Usage:
#   scripts/check-no-conflict-markers.sh              # scan tracked files
#   scripts/check-no-conflict-markers.sh --self-test-only
#
set -euo pipefail

ROOT="${ROOT:-$(git rev-parse --show-toplevel 2>/dev/null || echo .)}"

# The markers are written with quantifiers, never as seven literal
# characters, so this script does not match itself and can be scanned by
# its own scan like every other tracked file.
START_END_RE='^(<{7}|>{7})([[:space:]]|$)'
ANY_MARKER_RE='^(<{7}|\|{7}|={7}|>{7})([[:space:]]|$)'

# ─── Detector ─────────────────────────────────────────────────────────
# Two phases. Phase 1 finds files that carry a START or END marker —
# those are unambiguous. Phase 2 reports every marker line inside just
# those files, which is what makes the bare '=' line reportable when it
# is genuinely part of a conflict and invisible when it is a heading rule.
# Prints "path:lineno:line" per hit; returns 1 when any hit was printed.
detect() { # $1 = directory to scan
  local dir="$1" candidates rc=0
  candidates="$(grep -rIlE "${START_END_RE}" \
    --exclude-dir=.git --exclude-dir=node_modules --exclude-dir=vendor \
    "${dir}" 2>/dev/null || true)"
  [[ -z "${candidates}" ]] && return 0
  while IFS= read -r f; do
    [[ -z "${f}" ]] && continue
    while IFS= read -r hit; do
      [[ -z "${hit}" ]] && continue
      echo "${f#"${dir}"/}:${hit}"
      rc=1
    done < <(grep -nE "${ANY_MARKER_RE}" "${f}" || true)
  done <<< "${candidates}"
  return "${rc}"
}

# ─── Vacuity self-test ────────────────────────────────────────────────
#
# An absence-assertion reports "clean" both when the thing is absent AND
# when the detector stopped working (#5512); scripts/check-no-nodeports.sh
# has carried this protection since #4765 and every absence guard in this
# repo owes it. POSITIVE fixtures must still be caught, NEGATIVE fixtures
# must still be ignored, or this exits non-zero before scanning anything.
#
# The fixtures are BUILT from repeated characters rather than typed out,
# for the same reason the regexes are: a literal marker in this file would
# make the guard flag its own source.
self_test() {
  local t lt eq gt pipe out
  lt="$(printf '<%.0s' 1 2 3 4 5 6 7)"
  eq="$(printf '=%.0s' 1 2 3 4 5 6 7)"
  gt="$(printf '>%.0s' 1 2 3 4 5 6 7)"
  pipe="$(printf '|%.0s' 1 2 3 4 5 6 7)"
  t="$(mktemp -d)"
  trap 'rm -rf "${t}"' RETURN

  # POSITIVE 1 — a plain merge conflict, the exact ad2d2e9ab shape.
  mkdir -p "${t}/pos1"
  printf '  KEY: a\n%s HEAD\n  KEY: b\n%s\n  KEY: c\n%s cf1a2122d (subject)\n' \
    "${lt}" "${eq}" "${gt}" > "${t}/pos1/conflict.yaml"
  # POSITIVE 2 — diff3 style, which adds a base section between the two.
  mkdir -p "${t}/pos2"
  printf '%s ours\nx\n%s base\ny\n%s\nz\n%s theirs\n' \
    "${lt}" "${pipe}" "${eq}" "${gt}" > "${t}/pos2/conflict.go"
  # NEGATIVE — the CONTROL. Shares the suspect property (a line that is
  # exactly the seven-'=' middle marker) but is a Setext heading rule in a
  # Helm comment, the shape four real charts in this repo use today.
  mkdir -p "${t}/neg"
  printf '{{- /*\nStorageClass hook.\n\nTHE FIX\n%s\nFlip the default class.\n*/ -}}\n' \
    "${eq}" > "${t}/neg/heading.yaml"
  printf 'Title\n%s\nbody\n' "$(printf '=%.0s' 1 2 3)" > "${t}/neg/short.md"

  local fail=0
  for p in pos1 pos2; do
    if out="$(detect "${t}/${p}")"; then
      echo "SELF-TEST FAIL: detector did not flag fixture ${p} — it has stopped working" >&2
      fail=1
    elif [[ -z "${out}" ]]; then
      echo "SELF-TEST FAIL: detector returned nonzero but printed nothing for ${p}" >&2
      fail=1
    fi
  done
  if ! out="$(detect "${t}/neg")"; then
    echo "SELF-TEST FAIL: detector flagged the heading-underline control:" >&2
    echo "${out}" >&2
    fail=1
  fi
  [[ ${fail} -ne 0 ]] && return 1
  echo "self-test PASS: 2 conflict fixtures caught, heading-underline control not flagged"
  return 0
}

self_test || exit 2
[[ "${1:-}" == "--self-test-only" ]] && exit 0

# ─── Scan ─────────────────────────────────────────────────────────────
# Scans the checkout itself so the script works from an exported tree as
# well as a clone; grep -I skips binaries and .git is excluded (MERGE_MSG
# and rebase state are the one place markers legitimately live).
if [[ ! -d "${ROOT}" ]]; then
  echo "FAIL: ROOT '${ROOT}' is not a directory — a guard that looks at" >&2
  echo "      nothing passes at nothing." >&2
  exit 2
fi

if git -C "${ROOT}" rev-parse --git-dir >/dev/null 2>&1; then
  file_count="$(git -C "${ROOT}" ls-files | wc -l | tr -d ' ')"
else
  file_count="$(find "${ROOT}" -type f -not -path '*/.git/*' | wc -l | tr -d ' ')"
fi
if [[ "${file_count}" -eq 0 ]]; then
  echo "FAIL: found zero files to scan — the scan path is wrong." >&2
  exit 2
fi

if hits="$(detect "${ROOT}")"; then
  echo "OK: scanned ${file_count} files under ${ROOT} — no unresolved merge-conflict markers"
  exit 0
fi

echo "${hits}" >&2
cat >&2 <<'MSG'

UNRESOLVED MERGE-CONFLICT MARKER above. A cherry-pick, rebase or merge was
committed without resolving. Markers do not fail loudly where they land:
a Helm template that is gated off still packages, and the render error you
eventually get names a line in the OUTPUT that corresponds to nothing in
the source. On 2026-08-10 nine such blocks froze bp-catalyst-platform at
1.4.1332 and, with it, every chart pin bound for every Sovereign.

Resolve the conflict — pick one side, delete both markers and the middle
rule — then re-run:

  scripts/check-no-conflict-markers.sh
MSG
exit 1
