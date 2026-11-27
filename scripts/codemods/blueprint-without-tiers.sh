#!/bin/sh
# Blueprint data without tiers (NET-222). A codemod, run once over every tracked
# platform/<name>/blueprint.yaml and products/<name>/blueprint.yaml:
#   - a topology placement tier mgmt, dmz or rtz becomes '' (the host: the
#     application controller then writes the release into the Organization's
#     own namespace; no tier namespace mgmt, dmz or rtz exists any more);
#   - spec.defaultPlacement loses its key vcluster, and the key itself goes
#     when no other key is left under it;
#   - spec.allowedPlacements becomes [host].
# The cluster identifiers keep their prefix (mgmt-A, rtz-B): the Blueprint CRD
# (products/catalyst/chart/crds/blueprint.yaml) and
# platform/_schemas/blueprint-topology.json admit only the prefixed form, and
# the cluster registry of the application controller parses it.
#
# Usage, from anywhere in the repository:  sh scripts/codemods/blueprint-without-tiers.sh
# It rewrites the files in place and prints the path of each file it changed.
# A form of these keys it does not know (a flow mapping under defaultPlacement)
# stops it with exit 1 before any file is written. Run again, it changes nothing.
# Afterwards regenerate the catalogue: python3 scripts/generate-catalog.py
set -eu
root=$(git rev-parse --show-toplevel)
cd "$root"
files=$(git ls-files -- ':(glob)platform/*/blueprint.yaml' ':(glob)products/*/blueprint.yaml')

# One file on standard input, the rewritten file on standard output. Exit 3
# names the first line of a form it does not rewrite.
prog='
function ind(s) { match(s, /^ */); return RLENGTH }
function flush() {
  if (keys > 0) printf "%s\n%s", dp, kept
  dp = ""; kept = ""; keys = 0
}
BEGIN { dp = ""; kept = ""; keys = 0; dpi = -1; api = -1 }
{
  line = $0
  if (api >= 0) {
    if (line ~ /^ *- / && ind(line) >= api) next
    api = -1
  }
  if (dp != "") {
    if (line !~ /^ *$/ && ind(line) > dpi) {
      if (line !~ /^ *vcluster:/) kept = kept line "\n"
      if (line !~ /^ *(#|vcluster:)/) keys++
      next
    }
    flush()
  }
  if (line ~ /^ *tier: *["\047]?(mgmt|dmz|rtz)["\047]? *(#.*)?$/) {
    sub(/tier: *["\047]?(mgmt|dmz|rtz)["\047]?/, "tier: \047\047", line)
  } else if (line ~ /^ *defaultPlacement: *$/) {
    dp = line; dpi = ind(line); next
  } else if (line ~ /^ *defaultPlacement:/) {
    printf "%s:%d: defaultPlacement in a form this codemod does not rewrite\n", FILENAME, NR > "/dev/stderr"
    exit 3
  } else if (line ~ /^ *allowedPlacements: *$/) {
    api = ind(line); sub(/allowedPlacements:.*/, "allowedPlacements: [host]", line)
  } else if (line ~ /^ *allowedPlacements: *\[/) {
    sub(/allowedPlacements: *\[[^]]*\]/, "allowedPlacements: [host]", line)
  }
  print line
}
END { flush() }
'

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
n=0
for f in $files; do
  n=$((n + 1))
  awk "$prog" "$f" > "$work/$n" || { echo "blueprint-without-tiers: stopped at $f, nothing written" >&2; exit 1; }
done
n=0
for f in $files; do
  n=$((n + 1))
  if ! cmp -s "$f" "$work/$n"; then
    cat "$work/$n" > "$f"
    echo "$f"
  fi
done
