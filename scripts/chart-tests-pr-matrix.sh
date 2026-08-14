#!/usr/bin/env bash
# chart-tests-pr-matrix.sh — which charts must run chart/tests/*.sh on THIS PR.
#
# WHY THIS IS A SCRIPT AND NOT INLINE YAML — Refs #6235
# ------------------------------------------------------
# `.github/workflows/chart-tests-pr.yaml` used to compute its matrix inline, and
# its trigger was `platform/*/chart/tests/**` ONLY. A PR that changed chart
# TEMPLATES without touching a test file therefore ran no chart suite at PR
# time: the gate could not fire on the change it exists to gate, which is the
# same class as the fail-open predicate that motivated #6235. The trigger now
# mirrors blueprint-release.yaml (`platform/*/chart/**`, `products/*/chart/**`),
# so the PR gate covers exactly the edits the post-merge gate covers.
#
# Widening the trigger alone would have multiplied CI load: the deploy-bots bump
# `products/catalyst/chart/Chart.yaml` on essentially every PR (41 open PRs carry
# the umbrella at the time of writing), so `products/*/chart/**` would fire the
# 10-script catalyst suite on all of them for a change that cannot alter a
# render. So a chart is dropped from the matrix when its ONLY changed file is
# `chart/Chart.yaml` AND that file's diff touches nothing but `version:` /
# `appVersion:` — which is exactly and only what `scripts/bump-chart-version.sh`
# writes. Any other Chart.yaml edit (a dependency pin, a rename, an annotation)
# keeps the chart in the matrix.
#
# Living in a script rather than in a `run:` block is what makes the predicate
# testable: `--self-test` builds real git fixtures and asserts the three cases
# against THIS code, not against a re-implementation of it.
#
# Usage:
#   chart-tests-pr-matrix.sh --base <ref-or-sha> [--head <ref-or-sha>] [--root <dir>]
#   chart-tests-pr-matrix.sh --self-test
#
# Prints the GitHub Actions matrix JSON on stdout, e.g.
#   {"include":[{"path":"platform/gitea"}]}
# Exit: 0 = computed (possibly empty), 2 = usage/setup error.

set -euo pipefail

BASE=""
HEAD_REF="HEAD"
ROOT="${ROOT:-.}"
SELF_TEST=0

while [ $# -gt 0 ]; do
  case "$1" in
    --base)      BASE="$2"; shift 2 ;;
    --head)      HEAD_REF="$2"; shift 2 ;;
    --root)      ROOT="$2"; shift 2 ;;
    --self-test) SELF_TEST=1; shift ;;
    *) echo "Unknown arg: $1" >&2
       echo "Usage: $0 --base <ref> [--head <ref>] [--root <dir>] | $0 --self-test" >&2
       exit 2 ;;
  esac
done

# ── The predicates, factored out so --self-test exercises the SAME code the
#    real run uses. A self-test over a re-implementation proves nothing.

# changed_chart_files <root> <base> <head> — PR-owned changes under a chart dir.
#
# NOTE: `git diff --name-only ... | grep -E ...` and NOT `grep -qE`. Refs #6235:
# `grep -q` exits on the first match and SIGPIPEs the producer, which under
# `set -o pipefail` reports a FOUND match as non-zero. `grep -E` without `-q`
# reads to EOF, so the producer always finishes.
changed_chart_files() {
  local root="$1" base="$2" head="$3"
  git -C "$root" diff --name-only "${base}...${head}" \
    | grep -E '^(platform|products)/[^/]+/chart/' || true
}

# version_only_chart_yaml <root> <base> <head> <path> — is this Chart.yaml diff
# nothing but a version bump? Content lines only: the +++/--- headers and the
# @@ hunk markers are not content, and a rename/mode change is not either.
version_only_chart_yaml() {
  local root="$1" base="$2" head="$3" path="$4" line
  local saw=0
  while IFS= read -r line; do
    case "$line" in
      '+++'*|'---'*) continue ;;
      '+'*|'-'*)
        saw=1
        # strip the leading +/- then require the key to be version/appVersion
        case "${line#?}" in
          version:*|appVersion:*) ;;
          *) return 1 ;;
        esac
        ;;
    esac
  done < <(git -C "$root" diff -U0 "${base}...${head}" -- "$path")
  [ "$saw" -eq 1 ]
}

# compute_matrix <root> <base> <head> — the whole decision, one function.
compute_matrix() {
  local root="$1" base="$2" head="$3" f chart
  local -a files=()
  while IFS= read -r f; do [ -n "$f" ] && files+=("$f"); done < <(changed_chart_files "$root" "$base" "$head")

  local -a charts=()
  for f in "${files[@]+"${files[@]}"}"; do
    chart="$(printf '%s' "$f" | cut -d/ -f1-2)"
    case " ${charts[*]-} " in *" $chart "*) ;; *) charts+=("$chart") ;; esac
  done

  local -a keep=()
  for chart in "${charts[@]+"${charts[@]}"}"; do
    # this chart's own changed files
    local -a own=()
    for f in "${files[@]+"${files[@]}"}"; do
      case "$f" in "$chart"/chart/*) own+=("$f") ;; esac
    done
    if [ "${#own[@]}" -eq 1 ] && [ "${own[0]}" = "$chart/chart/Chart.yaml" ] \
       && version_only_chart_yaml "$root" "$base" "$head" "${own[0]}"; then
      # A pure version bump cannot change a render. Skip.
      continue
    fi
    # Nothing to run if the chart has no tests/ directory.
    [ -d "$root/$chart/chart/tests" ] || continue
    keep+=("$chart")
  done

  if [ "${#keep[@]}" -eq 0 ]; then
    printf '{"include":[]}\n'
  else
    printf '%s\n' "${keep[@]}" | jq -R -s -c '{include: (split("\n") | map(select(length>0)) | map({path: .}))}'
  fi
}

# ── Self-test — real git fixtures, no network, no cluster ────────────────────
if [ "$SELF_TEST" -eq 1 ]; then
  command -v jq >/dev/null 2>&1 || { echo "SELF-TEST SETUP: jq is required" >&2; exit 2; }
  T="$(mktemp -d)"
  trap 'rm -rf "$T"' EXIT
  RC=0
  chk() { # <label> <want> <got>
    if [ "$2" = "$3" ]; then echo "  ok   $1"
    else echo "  FAIL $1"; echo "         want: $2"; echo "         got : $3"; RC=1; fi
  }

  g() { git -C "$T" "$@" >/dev/null 2>&1; }
  g init -q -b main
  g config user.email t@t.t
  g config user.name t
  mkdir -p "$T/platform/alpha/chart/templates" "$T/platform/alpha/chart/tests" \
           "$T/products/umbrella/chart/templates" "$T/products/umbrella/chart/tests" \
           "$T/platform/notests/chart/templates"
  printf 'version: 1.0.0\nappVersion: 1.0.0\nname: alpha\n'    > "$T/platform/alpha/chart/Chart.yaml"
  printf 'kind: Deployment\n'                                  > "$T/platform/alpha/chart/templates/d.yaml"
  printf '#!/usr/bin/env bash\n'                               > "$T/platform/alpha/chart/tests/t.sh"
  printf 'version: 1.4.1400\nappVersion: 1.4.1400\nname: u\n'  > "$T/products/umbrella/chart/Chart.yaml"
  printf 'kind: Service\n'                                     > "$T/products/umbrella/chart/templates/s.yaml"
  printf '#!/usr/bin/env bash\n'                               > "$T/products/umbrella/chart/tests/t.sh"
  printf 'version: 0.1.0\nname: notests\n'                     > "$T/platform/notests/chart/Chart.yaml"
  printf 'kind: ConfigMap\n'                                   > "$T/platform/notests/chart/templates/c.yaml"
  g add -A; g commit -qm base
  BASE_SHA="$(git -C "$T" rev-parse HEAD)"

  echo "== chart-tests-pr-matrix self-test (git fixtures, no cluster) =="

  # (a) THE DEFECT THIS FIXES: a template-only change must produce a matrix.
  g checkout -q -b a
  printf 'kind: Deployment\n# edited\n' > "$T/platform/alpha/chart/templates/d.yaml"
  g add -A; g commit -qm tmpl
  chk "template-only edit selects the chart" \
      '{"include":[{"path":"platform/alpha"}]}' "$(compute_matrix "$T" "$BASE_SHA" HEAD)"

  # (b) the load carve-out: a pure version bump selects nothing.
  g checkout -q main; g checkout -q -b b
  printf 'version: 1.4.1401\nappVersion: 1.4.1401\nname: u\n' > "$T/products/umbrella/chart/Chart.yaml"
  g add -A; g commit -qm bump
  chk "version-only Chart.yaml bump selects nothing" \
      '{"include":[]}' "$(compute_matrix "$T" "$BASE_SHA" HEAD)"

  # (c) CONTROL for (b) — the carve-out must not swallow a real Chart.yaml edit.
  #     Without this, (b) would also pass for a guard that ignored Chart.yaml
  #     entirely, which is a different and much worse rule.
  g checkout -q main; g checkout -q -b c
  printf 'version: 1.4.1402\nappVersion: 1.4.1402\nname: u\ndependencies:\n  - name: x\n' \
      > "$T/products/umbrella/chart/Chart.yaml"
  g add -A; g commit -qm dep
  chk "Chart.yaml edit beyond version: still selects the chart" \
      '{"include":[{"path":"products/umbrella"}]}' "$(compute_matrix "$T" "$BASE_SHA" HEAD)"

  # (c2) TIGHTER CONTROL for (c). Case (c) adds `dependencies:` AND its child
  #      `  - name: x`; the child alone keeps the chart in the matrix, so (c)
  #      still passes for a carve-out that had been widened to allow
  #      `dependencies:` itself. This case changes exactly ONE other key, on one
  #      line, so nothing but the key test can decide it. Without (c2) the
  #      allow-list could be widened key-by-key and every case above stays green.
  g checkout -q main; g checkout -q -b c2
  printf 'version: 1.4.1404\nappVersion: 1.4.1404\nname: u\ndependencies: []\n' \
      > "$T/products/umbrella/chart/Chart.yaml"
  g add -A; g commit -qm dep-oneline
  chk "one-line non-version key in Chart.yaml still selects the chart" \
      '{"include":[{"path":"products/umbrella"}]}' "$(compute_matrix "$T" "$BASE_SHA" HEAD)"

  # (d) CONTROL for (a) — a version bump RIDING ALONG with a template edit must
  #     still select. The carve-out keys on "Chart.yaml is the ONLY change".
  g checkout -q main; g checkout -q -b d
  printf 'version: 1.4.1403\nappVersion: 1.4.1403\nname: u\n' > "$T/products/umbrella/chart/Chart.yaml"
  printf 'kind: Service\n# edited\n' > "$T/products/umbrella/chart/templates/s.yaml"
  g add -A; g commit -qm both
  chk "template edit + version bump still selects the chart" \
      '{"include":[{"path":"products/umbrella"}]}' "$(compute_matrix "$T" "$BASE_SHA" HEAD)"

  # (e) a tests-only change — the case the OLD filter covered — must still work.
  g checkout -q main; g checkout -q -b e
  printf '#!/usr/bin/env bash\n# edited\n' > "$T/platform/alpha/chart/tests/t.sh"
  g add -A; g commit -qm tests
  chk "tests-only edit still selects the chart (no regression)" \
      '{"include":[{"path":"platform/alpha"}]}' "$(compute_matrix "$T" "$BASE_SHA" HEAD)"

  # (f) a chart with NO tests/ dir has nothing to run.
  g checkout -q main; g checkout -q -b f
  printf 'kind: ConfigMap\n# edited\n' > "$T/platform/notests/chart/templates/c.yaml"
  g add -A; g commit -qm notests
  chk "chart without tests/ selects nothing" \
      '{"include":[]}' "$(compute_matrix "$T" "$BASE_SHA" HEAD)"

  # (g) non-chart change selects nothing.
  g checkout -q main; g checkout -q -b g
  mkdir -p "$T/docs"; printf 'x\n' > "$T/docs/x.md"
  g add -A; g commit -qm docs
  chk "non-chart change selects nothing" \
      '{"include":[]}' "$(compute_matrix "$T" "$BASE_SHA" HEAD)"

  if [ "$RC" -eq 0 ]; then echo "SELF-TEST PASS (8 cases, git fixtures only — no cluster, no network)"
  else echo "SELF-TEST FAIL"; fi
  exit "$RC"
fi

[ -n "$BASE" ] || { echo "--base is required (or use --self-test)" >&2; exit 2; }
compute_matrix "$ROOT" "$BASE" "$HEAD_REF"
