#!/usr/bin/env bash
# generate-blueprint-deps.sh — emit blueprint-deps.generated.json from
# clusters/_template/bootstrap-kit/*.yaml.
#
# Single source of truth = Flux HelmRelease `dependsOn`. The wizard's
# componentGroups.ts MUST NOT carry hand-maintained dependencies. This
# script parses every bootstrap-kit HelmRelease and emits:
#
#   {
#     "<component-id>": ["<dep-id>", ...],
#     ...
#   }
#
# Where `<component-id>` is the bp-<name> with the `bp-` prefix stripped
# (matches componentGroups.ts ids: e.g. `keycloak`, `harbor`, `external-secrets`).
#
# Run pre-build (CI + dev). Output committed at:
#   products/catalyst/bootstrap/ui/src/data/blueprint-deps.generated.json
#
# A check job in CI re-runs this and asserts no drift between the
# committed file and the YAML source — blocking PRs that diverge.
set -euo pipefail

REPO_ROOT="$(git -C "$(dirname "$0")/.." rev-parse --show-toplevel)"
KIT_DIR="${REPO_ROOT}/clusters/_template/bootstrap-kit"
OUT_FILE="${REPO_ROOT}/products/catalyst/bootstrap/ui/src/data/blueprint-deps.generated.json"

cd "${KIT_DIR}"

declare -A DEPS

for f in *.yaml; do
  [ -f "$f" ] || continue
  hr=$(awk 'BEGIN{capture=0} /^kind: HelmRelease/{capture=1} capture && /^  name:/{print $2;exit}' "$f")
  [ -z "${hr}" ] && continue
  raw_deps=$(awk '/^  dependsOn:/{flag=1;next} flag && /^  [a-z]/{flag=0} flag && /- name:/{print $3}' "$f")
  # Strip bp- prefix for both the key and each dep — wizard ids are bare.
  key="${hr#bp-}"
  list=""
  for d in $raw_deps; do
    bare="${d#bp-}"
    list="${list}${bare}\n"
  done
  DEPS["${key}"]="${list}"
done

# Emit JSON. Keys sorted for stable diffs.
{
  echo "{"
  first=1
  for k in $(echo "${!DEPS[@]}" | tr ' ' '\n' | sort); do
    [ ${first} -eq 0 ] && echo ","
    first=0
    printf '  "%s": [' "${k}"
    inner=""
    while IFS= read -r d; do
      [ -z "$d" ] && continue
      inner="${inner}\"${d}\", "
    done <<<"$(echo -e "${DEPS[$k]}")"
    inner="${inner%, }"
    printf "%s]" "${inner}"
  done
  echo
  echo "}"
} > "${OUT_FILE}.tmp"

mv "${OUT_FILE}.tmp" "${OUT_FILE}"
echo "Wrote ${OUT_FILE}"
