#!/usr/bin/env bash
# bump-values-image-tag.sh — Refs #5451
#
# Write a new SHA tag into a nested `images.<key>.tag` field of
# products/catalyst/chart/values.yaml, FAIL-CLOSED, and prove the write
# actually landed before returning success.
#
# WHY THIS EXISTS
# ---------------
# `console-build.yaml`, `admin-build.yaml` and `marketplace-api-build.yaml`
# each carried a deploy step of exactly this shape:
#
#     FILE="products/catalyst/chart/templates/org-services/console.yaml"
#     if [ -f "$FILE" ]; then
#       sed -i "s|image: ${IMAGE}:.*|image: ${IMAGE}:${SHA}|" "$FILE"
#     fi
#
# On 2026-05-02, commit 83ec889f0 (PR #580, "add global.imageRegistry to
# remaining bp-* charts") rewrote that manifest's `image:` line into a Helm
# expression and moved the mutable SHA into values.yaml `images.console.tag`:
#
#     image: "{{ ... }}/{{ .Values.images.organization }}/console:{{ .Values.images.console.tag }}"
#
# From that day the sed pattern matched ZERO lines. `sed -i` with no match
# exits 0. The deploy-bump composite action then saw an empty diff and pushed
# nothing. Every console build stayed GREEN and shipped its image to GHCR while
# the chart pin never moved — `images.console.tag` sat at "3c2f7e4" (2026-04-28)
# for three and a half months, so on hw293 (dep a0077ba47e3720e5)
# org-services/console ran April's binary and nine merged per-Organization
# console fixes were dark. Same story for admin (pin 3c2f7e4, GHCR 06ea9d4) and
# marketplace-api (pin 3c2f7e4, GHCR 0d9531a).
#
# The class was already known: catalyst-build.yaml's own comments name #580 as
# the cause of the identical InvalidImageName break on catalyst-api, and
# marketplace-build.yaml was migrated off the sed in #4885. The sweep simply
# never reached these three.
#
# THE TWO PROPERTIES THAT MATTER, and why they are here rather than inline
# ------------------------------------------------------------------------
#   1. FAIL-CLOSED on a reshaped field. If `images.<key>.tag` is missing,
#      renamed, or no longer a quoted scalar, this exits non-zero instead of
#      writing nothing and returning 0. A contributor who re-templates the pin
#      gets a red build, not another silent freeze.
#   2. POST-CONDITION on the write. After the edit, the field is re-read and
#      must equal the requested SHA. A mutation that matched nothing can no
#      longer be reported as success — which is the single property whose
#      absence caused this outage.
#
# Both live in one tested script instead of three copies of awk in three
# workflows, so the next chart reshape cannot fix two call sites and miss one.
#
# Usage:
#   scripts/bump-values-image-tag.sh <values.yaml> <key> <sha>
#
# Example:
#   scripts/bump-values-image-tag.sh products/catalyst/chart/values.yaml console 1a2b3c4
#
# Exit codes:
#   0 — the field now holds <sha> (verified by re-reading the file).
#   1 — bad usage, file missing, field missing/reshaped, or the write did not land.

set -euo pipefail

usage() {
  echo "usage: $0 <values.yaml> <images-key> <sha>" >&2
  exit 1
}

[ "$#" -eq 3 ] || usage

VALUES="$1"
KEY="$2"
SHA="$3"

[ -n "${VALUES}" ] && [ -n "${KEY}" ] && [ -n "${SHA}" ] || usage

if [ ! -f "${VALUES}" ]; then
  echo "::error title=values.yaml not found::${VALUES} does not exist; refusing to auto-bump." >&2
  exit 1
fi

if ! printf '%s' "${SHA}" | grep -Eq '^[A-Za-z0-9_.-]+$'; then
  echo "::error title=invalid SHA::refusing to write '${SHA}' into ${VALUES} images.${KEY}.tag." >&2
  exit 1
fi

# Read the CURRENT value of images.<key>.tag. An empty result means the field
# is absent or reshaped, and is a hard failure — never a no-op.
read_tag() {
  awk -v k="${KEY}" '
    /^images:/            { in_images = 1; next }
    in_images && /^[^ ]/  { exit }
    in_images && $0 ~ "^  " k ":[[:space:]]*$" { in_key = 1; next }
    in_key && /^  [^ ]/   { exit }
    in_key && /^    tag:/ {
      if (match($0, /"[^"]*"/)) { print substr($0, RSTART + 1, RLENGTH - 2) }
      exit
    }
  ' "$1"
}

before="$(read_tag "${VALUES}")"

if [ -z "${before}" ]; then
  echo "::error title=images.${KEY}.tag missing or unparseable::Expected a nested" \
       "'  ${KEY}:' block with an indented '    tag: \"<sha>\"' line inside the" \
       "'images:' map of ${VALUES}. The pin could not be located, so refusing to" \
       "auto-bump rather than silently shipping a stale image (Refs #5451)." >&2
  exit 1
fi

if [ "${before}" = "${SHA}" ]; then
  echo "images.${KEY}.tag already at ${SHA} in ${VALUES} — nothing to do."
  exit 0
fi

tmp="${VALUES}.bump.$$"
awk -v k="${KEY}" -v sha="${SHA}" '
  /^images:/            { in_images = 1; print; next }
  in_images && /^[^ ]/  { in_images = 0; in_key = 0 }
  in_images && $0 ~ "^  " k ":[[:space:]]*$" { in_key = 1; print; next }
  in_key && /^  [^ ]/   { in_key = 0 }
  in_key && /^    tag:/ && !done {
    sub(/"[^"]*"/, "\"" sha "\"")
    done = 1
    print
    next
  }
  { print }
' "${VALUES}" > "${tmp}"

mv "${tmp}" "${VALUES}"

# POST-CONDITION. This is the assertion whose absence is the whole defect:
# a mutation that matched nothing must not be able to exit 0.
after="$(read_tag "${VALUES}")"

if [ "${after}" != "${SHA}" ]; then
  echo "::error title=image pin bump did not land::${VALUES} images.${KEY}.tag is" \
       "'${after}' after the rewrite, expected '${SHA}'. The edit matched nothing" \
       "or matched the wrong line. Refusing to report success on a no-op write" \
       "(Refs #5451)." >&2
  exit 1
fi

echo "Bumped ${VALUES} images.${KEY}.tag: ${before} -> ${after}"
