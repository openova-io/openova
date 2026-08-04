# scripts/lib/parse-catalog-seed-pins.awk
#
# Emit one `name|visibility|source.chart|source.version` row per Blueprint in a
# RENDERED catalog-seed document stream
# (products/catalyst/chart/templates/catalog-seed/blueprints.yaml after
# `helm template`).
#
# Why a line scan and not yq: `yq eval-all 'select(.kind == "Blueprint") | ...'`
# over this ~6000-line, 80-document stream takes ~3 minutes on a CI runner —
# slow enough that a gate built on it gets disabled. The `source:` blocks are
# static YAML with a fixed 2-/4-space shape, so a scoped line scan is both
# sufficient and robust. This mirrors catalogSeedPins() in
# tests/e2e/bootstrap-kit/main_test.go, which line-parses the same blocks for
# the same reason.
#
# Field anchors (all at a fixed indent, verified against every seeded entry):
#   `  name:`        — 2-space, the only 2-space `name:` key (metadata.name;
#                      `name` is not a direct child of `spec`)
#   `  visibility:`  — 2-space, spec.visibility
#   `  source:`      — 2-space, opens the delivery block
#   `    chart:`     — 4-space inside source
#   `    version:`   — 4-space inside source
#
# Refs #5559 (UAT row R21).

function flush() {
  if (name != "") print name "|" vis "|" chart "|" ver
  name = ""; vis = ""; chart = ""; ver = ""; in_source = 0
}

/^---[[:space:]]*$/ { flush(); next }

/^  name:[[:space:]]/ {
  if (name == "") { name = $2; gsub(/["']/, "", name) }
  next
}

/^  visibility:[[:space:]]/ { vis = $2; gsub(/["']/, "", vis); next }

/^  source:[[:space:]]*$/ { in_source = 1; next }

# Any other 2-space key closes the source block.
/^  [A-Za-z]/ { in_source = 0 }

in_source && /^    chart:[[:space:]]/   { chart = $2; gsub(/["']/, "", chart); next }
in_source && /^    version:[[:space:]]/ { ver   = $2; gsub(/["']/, "", ver);   next }

END { flush() }
