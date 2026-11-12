#!/usr/bin/env bash
# check-kyverno-legacy-policies.sh - no Kyverno engine at 1.20 or later while a legacy policy is in a chart template;
# the number of legacy policies may only fall; the upgrade acknowledgement value never appears.
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
# Rule A: no file git tracks under platform/, products/ or clusters/ holds the text
#   acknowledgeLegacyPoliciesNotEnforced. Each such file is printed.
# Rule B: the legacy policy objects of each file equal its entry under the metric legacy_policy_objects of
#   scripts/baselines/kyverno-legacy.json; a file absent from the baseline is allowed zero. The baseline is
#   {"schema": "openova.io/guard-baseline/v1", "counts": {"legacy_policy_objects": {"<path>": <n>}},
#   "allow": []}: exactly these three keys, one metric, an empty allow list, every entry a whole number
#   of 1 or more. A count above its entry fails; a count below its entry fails too, so a change that
#   removes a legacy policy lowers the baseline in the same change, and a file at zero leaves it.
# Every run that reads the engine version prints the line legacy_policy_objects=<total>.
# Exit code 0: the rules hold. 1: a finding, or the engine version or the baseline cannot be read. 2: wrong usage.
set -euo pipefail

CHART_FILE="platform/kyverno/chart/Chart.yaml"
BASELINE="scripts/baselines/kyverno-legacy.json"
METRIC="legacy_policy_objects"
ACK="acknowledgeLegacyPoliciesNotEnforced"

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

# ack_files <root> - print each tracked file under platform/, products/ or clusters/ that holds $ACK (rule A).
ack_files() {
  local out rc=0
  out="$(git -C "$1" -c core.quotepath=off grep -l -F -e "$ACK" -- platform products clusters)" || rc=$?
  if [ "$rc" -gt 1 ]; then echo "kyverno-legacy-policies: git grep failed with exit code $rc" >&2; return 1; fi
  if [ -n "$out" ]; then printf '%s\n' "$out"; fi
}

# The comparison of rule B. Arguments: <baseline file> <its name> <metric>. Input: the lines of legacy_counts.
COMPARE_PY='
import json, sys
path, name, metric = sys.argv[1:4]
me = "kyverno-legacy-policies: FAIL, "
have = {}
for line in sys.stdin:
    line = line.rstrip("\n")
    if line:
        n, p = line.split(" ", 1)
        have[p] = int(n)
def unreadable(text):
    print(me + name + " " + text)
    sys.exit(1)
try:
    with open(path) as f:
        doc = json.load(f)
except FileNotFoundError:
    unreadable("is missing")
except (OSError, ValueError) as e:
    unreadable("cannot be read: %s" % e)
if not isinstance(doc, dict) or sorted(doc) != ["allow", "counts", "schema"]:
    unreadable("must hold exactly the keys allow, counts and schema")
if doc["schema"] != "openova.io/guard-baseline/v1":
    unreadable("has the schema %s, not openova.io/guard-baseline/v1" % json.dumps(doc["schema"]))
if doc["allow"] != []:
    unreadable("has allow entries; this guard takes none, allow is the empty list")
if not isinstance(doc["counts"], dict) or sorted(doc["counts"]) != [metric]:
    unreadable("must hold counts for exactly the metric " + metric)
allowed = doc["counts"][metric]
if not isinstance(allowed, dict):
    unreadable("must map each path to a number under " + metric)
for p in sorted(allowed):
    if type(allowed[p]) is not int or allowed[p] < 1:
        unreadable("allows %s for %s; every entry is a whole number of 1 or more, and a file at zero leaves the baseline" % (json.dumps(allowed[p]), p))
fails = 0
for p in sorted(set(have) | set(allowed)):
    h, a = have.get(p, 0), allowed.get(p, 0)
    if h > a:
        print(me + "%s holds %d legacy policy objects, the baseline allows %d" % (p, h, a))
        fails += 1
    elif h < a:
        print(me + "%s holds %d legacy policy objects, the baseline still says %d; lower the baseline in the same change" % (p, h, a))
        fails += 1
sys.exit(1 if fails else 0)
'

# check <root> - apply the rules to the tree at <root>. The real run and the self-test both call it.
check() {
  local version raw major minor counts objects files acks fail=0
  version="$(engine_version "$1")" || return 1
  read -r raw major minor <<<"$version"
  counts="$(legacy_counts "$1")"
  objects="$(printf '%s\n' "$counts" | awk 'NF{s+=$1} END{print s+0}')"
  files="$(printf '%s\n' "$counts" | awk 'NF{n++} END{print n+0}')"
  acks="$(ack_files "$1")" || return 1
  if [ -n "$acks" ]; then
    printf '%s\n' "$acks" | awk -v ack="$ACK" '{print "kyverno-legacy-policies: FAIL, " $0 " holds " ack "; that value is forbidden"}'
    fail=1
  fi
  printf '%s\n' "$counts" | python3 -I -c "$COMPARE_PY" "$1/$BASELINE" "$BASELINE" "$METRIC" || fail=1
  echo "$METRIC=$objects"
  if [ "$major" -lt 1 ] || { [ "$major" -eq 1 ] && [ "$minor" -lt 20 ]; }; then
    if [ "$fail" -eq 0 ]; then echo "kyverno-legacy-policies: ok, the engine is $raw, below 1.20; $objects legacy policy objects in $files files"; fi
    return "$fail"
  fi
  if [ "$objects" -eq 0 ]; then
    if [ "$fail" -eq 0 ]; then echo "kyverno-legacy-policies: ok, the engine is $raw and no legacy policy object remains"; fi
    return "$fail"
  fi
  printf '%s\n' "$counts" | awk 'NF{c=$1; sub(/^[0-9]+ /, ""); print "kyverno-legacy-policies: " $0 ": " c " legacy policy objects"}'
  echo "kyverno-legacy-policies: FAIL, the engine is $raw, 1.20 or later, and $objects legacy policy objects in $files files remain; migrate them before the upgrade"
  return 1
}

# t <exit code> <name> <appVersion line, or - for no chart file> <text the output holds, or ""> [<path> <content>]...
# One self-test case in a throw-away repository. A path that starts with "!" is written but not tracked.
# Without a path $BASELINE the case gets the exact baseline of its tree; the content "absent" for
# $BASELINE leaves the baseline out.
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
  if [ ! -e "$dir/$BASELINE" ]; then
    mkdir -p "$(dirname "$dir/$BASELINE")"
    legacy_counts "$dir" | awk -v m="$METRIC" 'BEGIN{printf "{\"schema\": \"openova.io/guard-baseline/v1\", \"allow\": [], \"counts\": {\"%s\": {", m}
      NF{c=$1; sub(/^[0-9]+ /, ""); printf "%s\"%s\": %d", (n++ ? ", " : ""), $0, c} END{print "}}}"}' > "$dir/$BASELINE"
  elif [ "$(cat "$dir/$BASELINE")" = "absent" ]; then
    rm -f "$dir/$BASELINE"
  fi
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
  local O='appVersion: "v1.18.0"' B="$BASELINE" W=platform/a/chart/values.yaml
  local H='{"schema": "openova.io/guard-baseline/v1", "allow": [], "counts": {"legacy_policy_objects": '
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
  # Rule A: the acknowledgement value.
  t 1 "the acknowledgement value under platform" "$O" "FAIL, $W holds $ACK" "$W" "kyverno:"$'\n'"  $ACK: true"
  t 1 "the acknowledgement value under products" "$O" "FAIL, products/c/chart/values.yaml holds $ACK" products/c/chart/values.yaml "$ACK: true"
  t 1 "the acknowledgement value under clusters" "$O" "FAIL, clusters/x/kyverno.yaml holds $ACK" clusters/x/kyverno.yaml "    $ACK: true"
  t 1 "the acknowledgement value with the engine at 1.20 and no legacy object" "$V" "holds $ACK" "$W" "$ACK: true"
  t 0 "the acknowledgement value outside platform, products and clusters" "$O" "" docs/x.md "$ACK" scripts/x.sh "$ACK"
  t 0 "the acknowledgement value in a file git does not track" "$O" "" "!$W" "$ACK: true"
  # Rule B: the baseline.
  t 0 "an exact baseline prints the total" "$O" "$METRIC=3" "$P" "$CP"$'\n---\n'"$CP" products/c/chart/templates/q.yaml "$CP" "$B" "$H"'{"'"$P"'": 2, "products/c/chart/templates/q.yaml": 1}}}'
  t 0 "no legacy object and an empty metric" "$V" "$METRIC=0" "$B" "$H"'{}}}'
  t 1 "one more object than the baseline allows" "$O" "FAIL, $P holds 2 legacy policy objects, the baseline allows 1" "$P" "$CP"$'\n---\n'"$CP" "$B" "$H"'{"'"$P"'": 1}}}'
  t 1 "a new file that the baseline does not name" "$O" "FAIL, $P holds 1 legacy policy objects, the baseline allows 0" "$P" "$CP" "$B" "$H"'{}}}'
  t 1 "an object that left without a lower baseline" "$O" "FAIL, $P holds 1 legacy policy objects, the baseline still says 2" "$P" "$CP" "$B" "$H"'{"'"$P"'": 2}}}'
  t 1 "a file that left and is still in the baseline" "$O" "holds 0 legacy policy objects, the baseline still says 1" "$B" "$H"'{"'"$P"'": 1}}}'
  t 1 "the baseline also holds at 1.20 and later" "$V" "the baseline allows 0" "$P" "$CP" "$B" "$H"'{}}}'
  t 1 "a missing baseline" "$O" "$B is missing" "$P" "$CP" "$B" "absent"
  t 1 "a baseline that is not JSON" "$O" "cannot be read" "$B" "counts: none"
  t 1 "a baseline of another schema" "$O" "has the schema" "$B" '{"schema": "other", "allow": [], "counts": {"legacy_policy_objects": {}}}'
  t 1 "a baseline with an unknown key" "$O" "exactly the keys" "$B" "$H"'{}}, "extra": 1}'
  t 1 "a baseline with an allow entry" "$O" "has allow entries" "$B" '{"schema": "openova.io/guard-baseline/v1", "allow": [{"metric": "legacy_policy_objects", "path": "x", "count": 1, "reason": "kept for a while", "owner": "CMP"}], "counts": {"legacy_policy_objects": {}}}'
  t 1 "a baseline with another metric" "$O" "exactly the metric" "$B" '{"schema": "openova.io/guard-baseline/v1", "allow": [], "counts": {"legacy_policy_objects": {}, "other": {}}}'
  t 1 "a baseline entry of 0" "$O" "every entry is a whole number of 1 or more" "$B" "$H"'{"'"$P"'": 0}}}'
  t 1 "a baseline entry that is text" "$O" "every entry is a whole number of 1 or more" "$P" "$CP" "$B" "$H"'{"'"$P"'": "1"}}}'
  echo "self-test: $PASSED cases passed, $FAILED failed"
  [ "$FAILED" -eq 0 ]
}

case "$*" in
  --self-test) self_test ;;
  "") check "$(cd "$(dirname "$0")/.." && pwd)" ;;
  *) echo "usage: $0 [--self-test]" >&2; exit 2 ;;
esac
