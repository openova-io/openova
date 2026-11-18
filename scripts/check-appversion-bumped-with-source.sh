#!/usr/bin/env bash
# check-appversion-bumped-with-source.sh — #6841, #7938
#
# A product chart pins its image by `appVersion`. Change the product's SOURCE
# without bumping appVersion and the build re-pushes the SAME mutable tag; every
# node with that tag cached keeps running the OLD binary under `IfNotPresent`.
# The chart version bump is not enough — it delivers new VALUES to an old image.
#
# Measured on hw307 (#6841): chart went 0.1.3 -> 0.1.4 carrying a new env var,
# appVersion stayed 0.1.2, and the pods kept running chargeback:0.1.2 from a
# 20-hour-old layer. TRUSTED_FORWARD_AUTH_HEADER was set correctly and read by
# a binary that had never heard of it, so SSO silently did nothing.
#
# Since #7938 a build never pushes a version tag that exists
# (scripts/ci/push-version-tag.sh): a build whose version was not bumped keeps
# the old image under that version and publishes nothing new under it. So this
# gate, before merge, is the ONLY place where a source change without a bump is
# refused. It must cover every image whose version tag a build workflow
# publishes from source in this repository. A product is found two ways:
#   - by scan: every folder products/<p>/ or products/<p>/<q>/ that holds a
#     Containerfile and an appVersion in its chart/Chart.yaml (agenity, axon,
#     chargeback, chargeback-docrender, openova-mcp);
#   - by declaration: a tracked file image-version.conf in the folder of a
#     product says what the scan cannot see, one `key: value` per line:
#       version-file: the file that holds the version (default <folder>/chart/Chart.yaml)
#       version-key:  the key of the version line     (default appVersion)
#       sources:      the paths the image is built from (default <folder>)
#       pins:         file:KEY entries whose value is source too (a pinned outside ref)
#     A declaration replaces the scan of its folder. The declaration lives with
#     its product, so this shared gate names none. Today: catalyst-migrator and
#     the storage driver under platform/ (versions in the env of their build
#     workflows), agenity (also built from openova-mcp, the shared auth module
#     and a pinned ref) and openova-mcp (also built from the shared auth module).
#     A declaration that names a file or path that does not exist fails the gate.
# Not covered, on purpose: the mirror workflows (build-bp-guacamole,
# build-bp-newapi, mirror-crossplane-packages) build nothing from this
# repository; their version tag IS the upstream version.
#
# A source file is a file whose name ends in .go .ts .tsx .js .jsx .mjs .svelte
# .sql .mod .sum .json .svg .css .html, or a Containerfile or Dockerfile, under a
# source path but not under its chart/, README/ or docs/, and not under the path
# of another product nested in it (chargeback-docrender inside chargeback).
# .json/.svg/.css/.html count since #7938: embedded files (the default offer, the
# icons, the document templates) are part of the binary. A pin counts when its
# value changed.
#
# Usage:
#   check-appversion-bumped-with-source.sh [<base> [<head>]]   (default origin/main HEAD)
#   check-appversion-bumped-with-source.sh --self-test         fixture repository, every kind of product can fail
set -euo pipefail

SRC_RE='\.(go|ts|tsx|js|jsx|mjs|svelte|sql|mod|sum|json|svg|css|html)$|(^|/)(Containerfile|Dockerfile)$'

# value_at <rev> <file> <key>: the value of the first `key:` line of file at rev, unquoted.
value_at() {
  { git show "$1:$2" 2>/dev/null || true; } | { grep -E "^[[:space:]]*$3:" || true; } | head -1 \
    | sed -E "s/^[[:space:]]*$3:[[:space:]]*//; s/[[:space:]]+#.*$//; s/[[:space:]]*$//; s/^[\"']//; s/[\"']$//"
}

# decl <image-version.conf> <key>: the value of one key of a declaration.
decl() {
  { grep -E "^$2:" "$1" || true; } | head -1 | sed -E "s/^$2:[[:space:]]*//; s/[[:space:]]+#.*$//; s/[[:space:]]*$//"
}

# name_of <folder>: products/chargeback/docrender -> chargeback-docrender.
name_of() {
  local d="${1#products/}"
  d="${d#platform/}"
  printf '%s' "${d//\//-}"
}

judge() {
  local base="$1" head="$2" fail=0 checked=0 entries=() roots=() declared=" "
  local chart prod name n v k p conf dir pins entry paths key vfile own path f src before after pin pfile pkey other skip

  while IFS= read -r conf; do
    [[ -n "$conf" && -f "$conf" ]] || continue
    dir=$(dirname "$conf")
    vfile=$(decl "$conf" version-file)
    vfile="${vfile:-$dir/chart/Chart.yaml}"
    key=$(decl "$conf" version-key)
    key="${key:-appVersion}"
    paths=$(decl "$conf" sources)
    paths="${paths:-$dir}"
    pins=$(decl "$conf" pins)
    for p in "$vfile" $paths; do
      [[ -e "$p" ]] || { echo "FAIL: $conf names $p, and it does not exist — update the declaration"; fail=1; }
    done
    for pin in $pins; do
      [[ -f "${pin%%:*}" ]] || { echo "FAIL: $conf pins ${pin%%:*}, and it does not exist — update the declaration"; fail=1; }
    done
    entries+=("$(name_of "$dir")|$vfile|$key|$paths|$pins")
    declared="$declared$dir "
  done < <(git ls-files -- '*image-version.conf' 2>/dev/null || true)

  for chart in products/*/chart/Chart.yaml products/*/*/chart/Chart.yaml; do
    [[ -f "$chart" ]] || continue
    prod=$(dirname "$(dirname "$chart")")
    [[ "$declared" != *" $prod "* ]] || continue
    # only products that actually build an image (have a Containerfile)
    [[ -f "$prod/Containerfile" ]] || continue
    grep -qE '^appVersion:' "$chart" || continue
    entries+=("$(name_of "$prod")|$chart|appVersion|$prod|")
  done
  for entry in "${entries[@]}"; do
    IFS='|' read -r n v k paths pins <<< "$entry"
    roots+=("${paths%% *}")
  done

  for entry in "${entries[@]}"; do
    IFS='|' read -r name vfile key paths pins <<< "$entry"
    own="${paths%% *}"
    checked=$((checked+1))
    src=""
    # shellcheck disable=SC2086
    while IFS= read -r f; do
      [[ -n "$f" ]] || continue
      skip=""
      for path in $paths; do
        [[ "$f" =~ ^$path/(chart|README|docs)/ ]] && skip=1
      done
      for other in "${roots[@]}"; do
        [[ "$other" != "$own" && "$f" == "$other/"* ]] && for path in $paths; do
          [[ "$other" == "$path/"* ]] && skip=1
        done
      done
      [[ -z "$skip" ]] || continue
      [[ "$f" =~ $SRC_RE ]] && src="$src$f "
    done < <(git diff --name-only "$base" "$head" -- $paths 2>/dev/null || true)
    for pin in $pins; do
      pfile="${pin%%:*}" pkey="${pin#*:}"
      if [[ "$(value_at "$base" "$pfile" "$pkey")" != "$(value_at "$head" "$pfile" "$pkey")" ]]; then
        src="$src$pfile:$pkey "
      fi
    done
    [[ -z "$src" ]] && { echo "  ok: $name — no image-affecting source change"; continue; }

    before=$(value_at "$base" "$vfile" "$key")
    # read BOTH sides from their commits — reading `after` from the working tree
    # makes the gate pass on a commit that never bumped it (caught in review).
    after=$(value_at "$head" "$vfile" "$key")
    if [[ -z "$after" ]]; then
      after=$(grep -E "^[[:space:]]*$key:" "$vfile" | head -1 | sed -E "s/^[[:space:]]*$key:[[:space:]]*//; s/[[:space:]]+#.*$//; s/[[:space:]]*$//; s/^[\"']//; s/[\"']$//")
    fi

    if [[ -z "$before" ]]; then
      echo "  ok: $name — new (no baseline $key)"
    elif [[ "$before" == "$after" ]]; then
      echo "FAIL: $name changed image-affecting source but $key stayed $after ($vfile)."
      echo "      A build never pushes a version tag that exists (#7938), so this change"
      echo "      would never be published: bump $key in $vfile."
      echo "      changed: $(echo "$src" | tr ' ' '\n' | grep . | head -3 | tr '\n' ' ')"
      fail=1
    else
      echo "  ok: $name — $key $before -> $after alongside source changes"
    fi
  done

  if [[ $checked -eq 0 ]]; then
    echo "FAIL: no image-building product was examined — the gate would pass vacuously"
    return 1
  fi
  if [[ $fail -ne 0 ]]; then
    echo "FAILED — a source change would not reach a cluster."
    return 1
  fi
  echo "PASS — every product whose source changed also bumped its version ($checked product(s) examined)."
}

# ---------------------------------------------------------------- self-test
#
# A fixture repository holds a product of every kind: scanned, nested, declared with its version in a
# workflow env, declared with more sources and a pin. Each case changes it from one base commit and
# runs this same script on base..HEAD.

self_test() {
  local fx problems=0 self
  self="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
  fx="$(mktemp -d)"
  (
    cd "$fx"
    git init -q .
    put() { mkdir -p "$(dirname "$1")"; printf '%s\n' "$2" > "$1"; }
    put products/chargeback/Containerfile 'FROM scratch'
    put products/chargeback/chart/Chart.yaml $'version: 0.1.5\nappVersion: 0.1.1'
    put products/chargeback/chart/templates/x.yaml 'kind: x'
    put products/chargeback/main.go 'package main'
    put products/chargeback/internal/offer/default-offer.json '{}'
    put products/chargeback/DESIGN.md '# design'
    put products/chargeback/docrender/Containerfile 'FROM scratch'
    put products/chargeback/docrender/chart/Chart.yaml $'version: 0.2.0\nappVersion: "0.2.0"'
    put products/chargeback/docrender/main.go 'package main'
    put products/catalyst-migrator/Dockerfile 'FROM alpine'
    put .github/workflows/build-catalyst-migrator.yaml $'env:\n  DEFAULT_RELEASE_TAG: \'0.1.0\''
    put products/catalyst-migrator/image-version.conf $'# the release tag lives in the build workflow\nversion-file: .github/workflows/build-catalyst-migrator.yaml\nversion-key: DEFAULT_RELEASE_TAG'
    put platform/driver-x/driver-src/main.go 'package main'
    put .github/workflows/build-driver-x.yaml $'env:\n  DRIVER_TAG: v0.1.11-idc.1  # the driver tag'
    put platform/driver-x/image-version.conf $'version-file: .github/workflows/build-driver-x.yaml\nversion-key: DRIVER_TAG\nsources: platform/driver-x/driver-src'
    put products/openova-mcp/Containerfile 'FROM scratch'
    put products/openova-mcp/chart/Chart.yaml 'appVersion: 1.0.0'
    put products/openova-mcp/chart/values.yaml 'a: 1'
    put products/openova-mcp/main.go 'package main'
    put products/agenity/Containerfile 'FROM scratch'
    put products/agenity/chart/Chart.yaml 'appVersion: 0.9.0'
    put products/agenity/image-version.conf $'sources: products/agenity products/openova-mcp core/services/shared/auth\npins: .github/workflows/agenity-build.yaml:AGENITY_REF'
    put products/openova-mcp/image-version.conf 'sources: products/openova-mcp core/services/shared/auth'
    put core/services/shared/auth/auth.go 'package auth'
    put .github/workflows/agenity-build.yaml $'env:\n  AGENITY_REF: aaaa'
    git add -A
    git -c user.name=t -c user.email=t@example.invalid commit -q -m base
    git tag base
  )
  edit() { printf '// changed\n' >> "$fx/$1"; }
  setv() { sed -i -E "s/^([[:space:]]*$2:[[:space:]]*).*/\\1$3/" "$fx/$1"; }

  # check <name> <want: pass|fail> <products that must FAIL> <products that must not FAIL> <change...>
  check() {
    local name="$1" want="$2" must="$3" mustnot="$4" out rc=0 got n bad=""
    shift 4
    git -C "$fx" checkout -q -f base
    git -C "$fx" checkout -q -B "case-$name"
    "$@"
    git -C "$fx" add -A
    git -C "$fx" -c user.name=t -c user.email=t@example.invalid commit -q --allow-empty -m "$name"
    out="$(cd "$fx" && bash "$self" base HEAD 2>&1)" || rc=$?
    got=pass
    [[ $rc -eq 0 ]] || got=fail
    [[ "$got" == "$want" ]] || bad="exit $rc ($got, want $want)"
    for n in $must; do
      grep -qF "FAIL: $n changed" <<< "$out" || bad="$bad; no failure of $n"
    done
    for n in $mustnot; do
      if grep -qF "FAIL: $n changed" <<< "$out"; then bad="$bad; $n failed"; fi
    done
    if [[ -n "$bad" ]]; then
      echo "SELF-TEST FAIL case '$name': $bad"
      printf '%s\n' "$out" | sed 's/^/      /'
      problems=$((problems+1))
    else
      echo "  self-test case '$name' ok ($got)"
    fi
  }
  c_cb_src() { edit products/chargeback/main.go; }
  c_cb_json() { edit products/chargeback/internal/offer/default-offer.json; }
  c_cb_chart_docs() { edit products/chargeback/chart/templates/x.yaml; edit products/chargeback/DESIGN.md; }
  c_cb_bumped() { edit products/chargeback/main.go; setv products/chargeback/chart/Chart.yaml appVersion 0.1.2; }
  c_dr_src() { edit products/chargeback/docrender/main.go; }
  c_dr_bumped() { edit products/chargeback/docrender/main.go; setv products/chargeback/docrender/chart/Chart.yaml appVersion '"0.2.1"'; }
  c_mig_src() { edit products/catalyst-migrator/Dockerfile; }
  c_mig_bumped() { edit products/catalyst-migrator/Dockerfile; setv .github/workflows/build-catalyst-migrator.yaml DEFAULT_RELEASE_TAG "'0.1.1'"; }
  c_drv_src() { edit platform/driver-x/driver-src/main.go; }
  c_drv_bumped() { edit platform/driver-x/driver-src/main.go; setv .github/workflows/build-driver-x.yaml DRIVER_TAG 'v0.1.11-idc.2  # the driver tag'; }
  c_mcp_src() { edit products/openova-mcp/main.go; }
  c_mcp_bumped() { edit products/openova-mcp/main.go; setv products/openova-mcp/chart/Chart.yaml appVersion 1.0.1; setv products/agenity/chart/Chart.yaml appVersion 0.9.1; }
  c_mcp_chart() { edit products/openova-mcp/chart/values.yaml; }
  c_auth() { edit core/services/shared/auth/auth.go; }
  c_pin() { setv .github/workflows/agenity-build.yaml AGENITY_REF bbbb; }
  c_pin_bumped() { setv .github/workflows/agenity-build.yaml AGENITY_REF bbbb; setv products/agenity/chart/Chart.yaml appVersion 0.9.1; }
  c_rot() { rm "$fx/.github/workflows/build-catalyst-migrator.yaml"; }

  check chargeback-source fail "chargeback" "chargeback-docrender" c_cb_src
  check chargeback-embedded-json fail "chargeback" "" c_cb_json
  check chargeback-chart-and-document pass "" "chargeback" c_cb_chart_docs
  check chargeback-bumped pass "" "chargeback" c_cb_bumped
  check docrender-source fail "chargeback-docrender" "chargeback" c_dr_src
  check docrender-bumped pass "" "chargeback-docrender chargeback" c_dr_bumped
  check migrator-dockerfile fail "catalyst-migrator" "" c_mig_src
  check migrator-bumped pass "" "catalyst-migrator" c_mig_bumped
  check driver-source fail "driver-x" "" c_drv_src
  check driver-bumped pass "" "driver-x" c_drv_bumped
  check openova-mcp-source fail "openova-mcp agenity" "" c_mcp_src
  check openova-mcp-and-agenity-bumped pass "" "openova-mcp agenity" c_mcp_bumped
  check openova-mcp-chart-only pass "" "openova-mcp agenity" c_mcp_chart
  check shared-auth fail "openova-mcp agenity" "" c_auth
  check agenity-pin fail "agenity" "openova-mcp" c_pin
  check agenity-pin-bumped pass "" "agenity" c_pin_bumped
  check declaration-names-a-missing-file fail "" "" c_rot
  # The output is read whole first: under pipefail, `bash ... | grep -q` reports the script's own
  # exit 1, not the match.
  local rot head_out
  rot="$(cd "$fx" && bash "$self" base HEAD 2>&1 || true)"
  if ! grep -qF "does not exist — update the declaration" <<< "$rot"; then
    echo "SELF-TEST FAIL case 'declaration-names-a-missing-file': no declaration failure"
    problems=$((problems+1))
  fi
  # The head commit is judged, not the working tree: a bump made one commit later does not count.
  git -C "$fx" checkout -q -f base
  git -C "$fx" checkout -q -B case-head-not-tree
  c_cb_src
  git -C "$fx" -c user.name=t -c user.email=t@example.invalid commit -q -am "source, no bump"
  setv products/chargeback/chart/Chart.yaml appVersion 0.1.2
  git -C "$fx" -c user.name=t -c user.email=t@example.invalid commit -q -am "bump"
  head_out="$(cd "$fx" && bash "$self" base HEAD~1 2>&1 || true)"
  if grep -qF "FAIL: chargeback changed" <<< "$head_out"; then
    echo "  self-test case 'head-not-working-tree' ok (fail)"
  else
    echo "SELF-TEST FAIL case 'head-not-working-tree': the bump in the working tree was counted for the head commit"
    problems=$((problems+1))
  fi

  rm -rf "$fx"
  if [[ $problems -ne 0 ]]; then
    echo "SELF-TEST FAILED: $problems case(s)"
    return 1
  fi
  echo "self-test ok: 18 cases; every kind of product fails on a source change without a bump and passes with one"
}

if [[ "${1:-}" == "--self-test" ]]; then
  self_test
  exit $?
fi
judge "${1:-origin/main}" "${2:-HEAD}"
