#!/usr/bin/env bash
# check-kyverno-legacy-policies.sh - no Kyverno engine at 1.20 or later while a legacy policy is in a chart template.
#
# Kyverno 1.20 stops executing the legacy kinds ClusterPolicy and Policy of the API group kyverno.io.
# An engine upgrade to 1.20 with such a policy still in a chart leaves the policy installed and doing
# nothing, with no error. This guard fails that upgrade until the policies are migrated.
#
#   bash scripts/check-kyverno-legacy-policies.sh              check the repository
#   bash scripts/check-kyverno-legacy-policies.sh --self-test  prove on throw-away repositories that the rule can fail
#
# Engine version: the one line of platform/kyverno/chart/Chart.yaml that starts with "appVersion:". Its
#   value, without quotes and a leading "v", starts with <major>.<minor>, two whole numbers. A missing
#   file, no such line, two such lines or another value is a finding.
# Legacy policy object: read are the files git tracks under platform/ or products/ inside a folder named
#   templates. A file is cut into documents at each line that starts with "---". A document with a line
#   that starts with "apiVersion: kyverno.io/" holds one object per line "kind: ClusterPolicy" or
#   "kind: Policy". Values may be quoted and lines may end in spaces. Indented and comment lines never
#   count. The kinds of the API group policies.kyverno.io are not legacy.
# Finding: the engine version is 1.20 or later and a legacy policy object exists. Each such file is printed.
# Exit code 0: the rule holds. 1: a finding, or the engine version cannot be read. 2: wrong usage.
set -euo pipefail

CHART_FILE="platform/kyverno/chart/Chart.yaml"

# engine_version <root> - print "<value> <major> <minor>", or a message on stderr and return 1.
engine_version() {
  local file="$1/$CHART_FILE" lines raw value minor
  if [ ! -f "$file" ]; then echo "kyverno-legacy-policies: $CHART_FILE is missing" >&2; return 1; fi
  lines="$(awk '/^appVersion:/{n++} END{print n+0}' "$file")"
  if [ "$lines" != "1" ]; then echo "kyverno-legacy-policies: $CHART_FILE has $lines lines that start with appVersion:, not one" >&2; return 1; fi
  raw="$(awk '/^appVersion:/{sub(/^appVersion:[ \t]*/, ""); sub(/[ \t]+$/, ""); print}' "$file")"
  value="$(printf '%s' "$raw" | tr -d "\"'")"
  value="${value#v}"
  if ! printf '%s\n' "$value" | awk '/^[0-9]+\.[0-9]+([^0-9]|$)/{ok=1} END{exit ok ? 0 : 1}'; then
    echo "kyverno-legacy-policies: $CHART_FILE has appVersion $raw, which does not start with two whole numbers" >&2; return 1
  fi
  minor="$(printf '%s' "${value#*.}" | awk '{match($0, /^[0-9]+/); print substr($0, 1, RLENGTH)}')"
  printf '%s %s %s\n' "$raw" "$((10#${value%%.*}))" "$((10#$minor))"
}

# legacy_counts <root> - print "<objects> <path>" for each tracked template file that holds a legacy policy object.
legacy_counts() {
  git -C "$1" -c core.quotepath=off ls-files -- platform products \
    | awk '/^(platform|products)\/(.*\/)?templates\//' | ROOT="$1" awk '{
        file = ENVIRON["ROOT"] "/" $0; total = 0; api = 0; kinds = 0
        while ((getline line < file) > 0) {
          if (line ~ /^---/) { if (api) total += kinds; api = 0; kinds = 0; continue }
          if (line ~ /^apiVersion:[ \t]*["\047]?kyverno\.io\//) api = 1
          if (line ~ /^kind:[ \t]*["\047]?(ClusterPolicy|Policy)["\047]?[ \t]*$/) kinds++
        }
        close(file)
        if (api) total += kinds
        if (total > 0) print total, $0
      }'
}

# check <root> - apply the rule to the tree at <root>. The real run and the self-test both call it.
check() {
  local version raw major minor counts objects files
  version="$(engine_version "$1")" || return 1
  read -r raw major minor <<<"$version"
  counts="$(legacy_counts "$1")"
  objects="$(printf '%s\n' "$counts" | awk 'NF{s+=$1} END{print s+0}')"
  files="$(printf '%s\n' "$counts" | awk 'NF{n++} END{print n+0}')"
  if [ "$major" -lt 1 ] || { [ "$major" -eq 1 ] && [ "$minor" -lt 20 ]; }; then
    echo "kyverno-legacy-policies: ok, the engine is $raw, below 1.20; $objects legacy policy objects in $files files"
    return 0
  fi
  if [ "$objects" -eq 0 ]; then echo "kyverno-legacy-policies: ok, the engine is $raw and no legacy policy object remains"; return 0; fi
  printf '%s\n' "$counts" | awk 'NF{c=$1; sub(/^[0-9]+ /, ""); print "kyverno-legacy-policies: " $0 ": " c " legacy policy objects"}'
  echo "kyverno-legacy-policies: FAIL, the engine is $raw, 1.20 or later, and $objects legacy policy objects in $files files remain; migrate them before the upgrade"
  return 1
}

# t <exit code> <name> <appVersion line, or - for no chart file> <text the output holds, or ""> [<path> <content>]...
# One self-test case in a throw-away repository. A path that starts with "!" is written but not tracked.
PASSED=0 FAILED=0
CP=$'apiVersion: kyverno.io/v1\nkind: ClusterPolicy\nmetadata:\n  name: a'
t() {
  local want="$1" name="$2" app="$3" text="$4" dir path got out
  shift 4
  dir="$(mktemp -d)"
  git -C "$dir" init -q
  if [ "$app" != "-" ]; then
    mkdir -p "$dir/platform/kyverno/chart"
    printf 'apiVersion: v2\nname: bp-kyverno\nversion: 1.0.0\n%s\n' "$app" > "$dir/$CHART_FILE"
  fi
  while [ "$#" -ge 2 ]; do
    path="${1#!}"
    mkdir -p "$(dirname "$dir/$path")"
    printf '%s\n' "$2" > "$dir/$path"
    [ "$1" = "$path" ] && git -C "$dir" add -- "$path"
    shift 2
  done
  got=0
  out="$(check "$dir" 2>&1)" || got=$?
  rm -rf "$dir"
  if [ "$got" = "$want" ] && { [ -z "$text" ] || printf '%s\n' "$out" | grep -q -F -- "$text"; }; then
    PASSED=$((PASSED + 1))
  else
    FAILED=$((FAILED + 1))
    echo "self-test FAILED: $name: expected exit code $want${text:+ and the text \"$text\"}, got $got:"
    printf '%s\n' "$out" | sed 's/^/    /'
  fi
}

self_test() {
  local P=platform/a/chart/templates/p.yaml V='appVersion: "v1.20.0"'
  t 1 "v1.20.0 with a ClusterPolicy" "$V" "$P: 1 legacy" "$P" "$CP"
  t 0 "v1.18.0 with a ClusterPolicy" 'appVersion: "v1.18.0"' "below 1.20; 1 legacy policy objects in 1 files" "$P" "$CP"
  t 0 "v1.19.9 with a ClusterPolicy" 'appVersion: "v1.19.9"' "" "$P" "$CP"
  t 0 "v1.2.0 is below 1.20" 'appVersion: "v1.2.0"' "" "$P" "$CP"
  t 1 "v1.100.0 is above 1.20" 'appVersion: "v1.100.0"' "" "$P" "$CP"
  t 1 "v2.0.0 with a ClusterPolicy" 'appVersion: "v2.0.0"' "" "$P" "$CP"
  t 0 "v0.30.0 is below 1.20" 'appVersion: "v0.30.0"' "" "$P" "$CP"
  t 1 "1.20.1 in single quotes, no v" "appVersion: '1.20.1'" "" "$P" "$CP"
  t 1 "v1.21.0-rc.1 unquoted, spaces at the end" 'appVersion: v1.21.0-rc.1  ' "" "$P" "$CP"
  t 1 "a namespaced Policy in a sub-folder" "$V" "templates/sub/ns.yaml" platform/b/chart/templates/sub/ns.yaml $'apiVersion: kyverno.io/v2beta1\nkind: Policy'
  t 1 "a ClusterPolicy under products" "$V" "products/c/chart/templates/q.yaml" products/c/chart/templates/q.yaml "$CP"
  t 1 "kind before apiVersion, quoted" "$V" "" "$P" $'kind: "ClusterPolicy"\napiVersion: "kyverno.io/v1"'
  t 0 "the new kinds only" "$V" "no legacy policy object remains" "$P" $'apiVersion: policies.kyverno.io/v1\nkind: ValidatingPolicy\n---\napiVersion: kyverno.io/v2\nkind: PolicyException'
  t 0 "policies.kyverno.io is not kyverno.io" "$V" "" "$P" $'apiVersion: policies.kyverno.io/v1\nkind: Policy'
  t 0 "a Policy of another group after a kyverno.io document" "$V" "" "$P" $'apiVersion: kyverno.io/v2\nkind: PolicyException\n---\napiVersion: audit.k8s.io/v1\nkind: Policy'
  t 0 "an indented kind line and a comment line" "$V" "" "$P" $'{{/*\napiVersion: kyverno.io/v1\n  kind: ClusterPolicy\n# kind: ClusterPolicy\n*/}}'
  t 0 "outside a templates folder" "$V" "" platform/a/chart/tests/p.yaml "$CP" platform/a/chart/values.yaml "$CP"
  t 0 "outside platform and products" "$V" "" clusters/x/templates/p.yaml "$CP"
  t 0 "a file git does not track" "$V" "" "!$P" "$CP"
  t 0 "objects are counted per document" 'appVersion: "v1.18.0"' "3 legacy policy objects in 2 files" "$P" "$CP"$'\n---\n'"$CP" products/c/chart/templates/q.yaml "$CP"
  t 1 "two documents in one file" "$V" "p.yaml: 2 legacy" "$P" "$CP"$'\n---\n'"$CP"
  t 1 "no engine chart" - "is missing" "$P" "$CP"
  t 1 "no appVersion line" 'version: 3.8.0' "has 0 lines"
  t 1 "two appVersion lines" $'appVersion: "v1.18.0"\nappVersion: "v1.20.0"' "has 2 lines"
  t 1 "an appVersion that is not a version" 'appVersion: "latest"' "does not start with two whole numbers"
  t 1 "an appVersion with one number" 'appVersion: "v1"' "does not start with two whole numbers"
  echo "self-test: $PASSED cases passed, $FAILED failed"
  [ "$FAILED" -eq 0 ]
}

case "$*" in
  --self-test) self_test ;;
  "") check "$(cd "$(dirname "$0")/.." && pwd)" ;;
  *) echo "usage: $0 [--self-test]" >&2; exit 2 ;;
esac
