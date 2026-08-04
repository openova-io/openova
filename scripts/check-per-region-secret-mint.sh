#!/usr/bin/env bash
#
# check-per-region-secret-mint.sh — A16 guard for #5480.
#
# THE DEFECT THIS CATCHES
#
# A chart template that mints a random secret and guards the mint with a
# LOCAL lookup:
#
#   {{- $existing := lookup "v1" "Secret" .Release.Namespace $name -}}
#   {{- if $existing … }} reuse {{- else }} {{ randAlphaNum 64 }} {{- end }}
#
# is region-blind. On a 2-region Sovereign each region renders independently,
# each `lookup` sees only its OWN cluster, both find nothing, and each mints
# DIFFERENT bytes. With one VIP round-robining the two backends, a session
# minted on region-a is rejected by region-b — the A16 class documented in
# docs/PRINCIPLES.md and proven live on hw291 (#5480: openbao two leaders,
# guacamole 200-then-403 inside a single page load, newapi securecookie
# "the value is not valid").
#
# TWO THINGS THAT LOOK LIKE FIXES AND ARE NOT (verified 2026-08-01, #5480):
#
#   1. `existingSecret` — an OPT-IN escape hatch. Templates render the mint
#      only when it is EMPTY, which is the shipped default. Its presence means
#      an operator MAY supply their own secret, not that generation is
#      region-safe.
#   2. `reflector` annotations — emberstack reflector mirrors a Secret between
#      NAMESPACES inside one cluster. `reflection-allowed-namespaces` cannot
#      cross a region boundary, so it can never reconcile two independently
#      minted values in two clusters.
#
# Neither is accepted as a mitigation by this guard. A template is only
# cleared by ALLOWLIST_SEEDED below, which requires a genuine single-authority
# cross-region seed.
#
# EXIT CODES
#   0  no unmitigated template found (or --self-test passed)
#   1  at least one unmitigated template found
#   2  the detector failed its own self-test (fails CLOSED — a guard that
#      cannot prove it detects is worth nothing; see #5544)

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Templates cleared because they seed from ONE cross-region authority rather
# than minting locally. Each entry must name why, so an unreviewed addition is
# obvious in diff.
ALLOWLIST_SEEDED=(
  # keycloak realm configmaps resolve their client secrets from the
  # sovereign-realm seed rather than randAlphaNum-per-region.
  "platform/keycloak/chart/templates/configmap-sovereign-realm.yaml"
  "platform/keycloak/chart/templates/configmap-tenant-realm.yaml"
)

MINT_RE='randAlphaNum|randAlpha[^N]|randNumeric|derivePassword|uuidv4'
LOOKUP_RE='lookup "v1" "Secret" \.Release\.Namespace'

is_allowlisted() {
  local rel="$1"
  for a in "${ALLOWLIST_SEEDED[@]}"; do
    [ "$rel" = "$a" ] && return 0
  done
  return 1
}

# scan_dir <root> -> prints offending relative paths
scan() {
  local root="$1"
  local f rel
  while IFS= read -r f; do
    case "$f" in */Chart.yaml|*/values.yaml) continue ;; esac
    grep -qE "$MINT_RE"   "$f" 2>/dev/null || continue
    grep -qE "$LOOKUP_RE" "$f" 2>/dev/null || continue
    rel="${f#"$root"/}"
    is_allowlisted "$rel" && continue
    echo "$rel"
  done < <(find "$root/platform" "$root/products" -name '*.yaml' -type f 2>/dev/null | sort)
}

# ---------------------------------------------------------------- self-test
# A detector that has never been shown to fire is indistinguishable from one
# that cannot. Phase 0 builds four fixtures and pins all four verdicts,
# including the two false-mitigation shapes.
self_test() {
  local tmp rc=0
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN
  mkdir -p "$tmp/platform/bad/chart/templates" \
           "$tmp/platform/reflectoronly/chart/templates" \
           "$tmp/platform/existingonly/chart/templates" \
           "$tmp/platform/good/chart/templates" \
           "$tmp/products"

  # (1) the defect: local lookup guarding a random mint
  cat > "$tmp/platform/bad/chart/templates/s.yaml" <<'YAML'
{{- $existing := lookup "v1" "Secret" .Release.Namespace "x" -}}
{{- $v := "" -}}
{{- if $existing }}{{- $v = "reuse" }}{{- else }}{{- $v = randAlphaNum 64 }}{{- end }}
YAML

  # (2) same defect, wearing a reflector annotation (NOT a cross-region fix)
  cat > "$tmp/platform/reflectoronly/chart/templates/s.yaml" <<'YAML'
{{- $existing := lookup "v1" "Secret" .Release.Namespace "x" -}}
{{- $v := ternary "reuse" (randAlphaNum 64) (not (empty $existing)) -}}
metadata:
  annotations:
    reflector.v1.k8s.emberstack.com/reflection-allowed: "true"
YAML

  # (3) same defect, wearing an existingSecret knob (an opt-out, NOT a fix)
  cat > "$tmp/platform/existingonly/chart/templates/s.yaml" <<'YAML'
{{- if not .Values.creds.existingSecret -}}
{{- $existing := lookup "v1" "Secret" .Release.Namespace "x" -}}
{{- $v := ternary "reuse" (randAlphaNum 64) (not (empty $existing)) -}}
{{- end -}}
YAML

  # (4) clean: no random mint at all
  cat > "$tmp/platform/good/chart/templates/s.yaml" <<'YAML'
{{- $existing := lookup "v1" "Secret" .Release.Namespace "x" -}}
data:
  k: {{ .Values.explicit | b64enc }}
YAML

  local out; out="$(scan "$tmp")"

  _want()  { echo "$out" | grep -q "^$1$" || { echo "  SELF-TEST FAIL: expected to FLAG $1"; rc=1; }; }
  _clean() { echo "$out" | grep -q "^$1$" && { echo "  SELF-TEST FAIL: wrongly flagged $1"; rc=1; }; return 0; }

  _want  "platform/bad/chart/templates/s.yaml"
  _want  "platform/reflectoronly/chart/templates/s.yaml"   # reflector ≠ mitigation
  _want  "platform/existingonly/chart/templates/s.yaml"    # existingSecret ≠ mitigation
  _clean "platform/good/chart/templates/s.yaml"

  if [ "$rc" -eq 0 ]; then
    echo "OK — detector self-test passed (flags the mint shape incl. reflector/existingSecret decoys, ignores non-minting templates)."
  fi
  return "$rc"
}

# ---------------------------------------------------------------------- main
if [ "${1:-}" = "--self-test" ]; then
  self_test; exit $?
fi

if ! self_test >/dev/null 2>&1; then
  echo "FAIL: detector self-test did not pass — refusing to report on the repo." >&2
  echo "      Run: $0 --self-test" >&2
  exit 2
fi

mapfile -t OFFENDERS < <(scan "$REPO_ROOT")

echo "check-per-region-secret-mint (#5480 / A16)"
echo "  allowlisted as cross-region seeded: ${#ALLOWLIST_SEEDED[@]}"
echo "  unmitigated templates found:        ${#OFFENDERS[@]}"

if [ "${#OFFENDERS[@]}" -eq 0 ]; then
  echo "OK — no template mints a secret behind a region-blind local lookup."
  exit 0
fi

echo
echo "Each template below mints a random secret guarded only by a LOCAL"
echo "lookup, so a 2-region Sovereign gets two different values:"
for o in "${OFFENDERS[@]}"; do echo "  - $o"; done
echo
echo "Neither an 'existingSecret' knob nor a reflector annotation fixes this"
echo "(see the header of this script, and #5480). The value must be minted by"
echo "ONE authority and consumed by both regions."
exit 1
