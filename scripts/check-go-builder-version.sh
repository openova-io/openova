#!/usr/bin/env bash
# check-go-builder-version.sh — the Go that builds an image is at least the Go
# its go.mod requires. Refs #7922.
#
# WHY THIS EXISTS
# ---------------
# The official golang images set GOTOOLCHAIN=local, so a builder older than the
# module stops at `go mod download`:
#
#   go: go.mod requires go >= 1.25.0 (running go 1.22.12; GOTOOLCHAIN=local)
#
# A dependency bump raised products/openova-flow/server/go.mod to `go 1.25.0`
# while its Dockerfile still built FROM golang:1.22-alpine. The workflow ran
# vet and tests on pull requests (setup-go lets the go command fetch a newer
# toolchain, so they passed) and built the image only on main, so the image
# stopped building on main for months and no pull request turned red.
#
# THE RULE
# --------
# required  the higher of the go.mod `go` line and its `toolchain` line. The
#           `go` line is the hard minimum. The `toolchain` line is the Go the
#           module's own tests switch to, so the image is held to it as well.
# builder   every build stage of the Dockerfile whose image is golang. Its
#           version is read from the image tag, after the global ARGs (the ARG
#           lines before the first FROM) are put in, the same way docker does:
#             golang:1.25.4-alpine   Go 1.25.4
#             golang:1.25-alpine     the newest 1.25 patch, so it meets 1.25.x
#             golang:1-alpine        the newest Go 1 release
#             golang:alpine          the newest Go release
#           A --build-arg is applied only to an ARG the Dockerfile declares
#           before its first FROM; a literal FROM ignores it, as docker does.
# The check judges the stage twice when --build-arg is given: with those
# arguments (what CI builds) and without any (a plain `docker build`). Each
# must be at least `required`.
#
# Usage:
#   check-go-builder-version.sh --go-mod <go.mod> --dockerfile <Dockerfile> [--build-arg NAME=VALUE ...]
#   check-go-builder-version.sh --builder-version <go.mod>
#       prints the golang image version to build with: <major>.<minor> of
#       `required` (the newest patch of that line, which meets any patch).
#   check-go-builder-version.sh --self-test
#
# Exit codes: 0 pass; 1 a builder is older than go.mod requires, or its image
# cannot be resolved; 2 nothing could be judged (no go line, no golang stage,
# an unreadable version, a wrong argument).

set -euo pipefail
export LC_ALL=C

ME=check-go-builder-version
FLOAT=999999

# To stderr, so a caller that captures stdout never swallows the reason.
die2() { echo "::error title=${ME}::cannot judge: $*" >&2; exit 2; }

# ── go.mod ────────────────────────────────────────────────────────────────

# key_required <version> — "M m p" of a go.mod version (1.25, 1.25.0, go1.25.4,
# 1.21rc1). A missing patch is 0; a pre-release counts as the .0 release.
key_required() {
  local v="${1#go}"
  if [[ "$v" =~ ^([0-9]+)\.([0-9]+)(\.([0-9]+))?([a-z]+[0-9]*)?$ ]]; then
    echo "${BASH_REMATCH[1]} ${BASH_REMATCH[2]} ${BASH_REMATCH[4]:-0}"
  else
    return 1
  fi
}

# ge <key a> <key b> — true when a >= b, numerically, part by part.
ge() {
  local -a a b
  read -r -a a <<<"$1"
  read -r -a b <<<"$2"
  local i
  for i in 0 1 2; do
    if [ "${a[$i]}" -gt "${b[$i]}" ]; then return 0; fi
    if [ "${a[$i]}" -lt "${b[$i]}" ]; then return 1; fi
  done
  return 0
}

# required_of <go.mod> — prints "<version> <M m p> <how>" for the higher of the
# go line and the toolchain line.
required_of() {
  local gomod="$1" goline tc gk tk
  [ -f "$gomod" ] || die2 "${gomod}: no such file"
  goline="$(sed -n 's/^go[[:space:]]\{1,\}\([^[:space:]]*\).*/\1/p' "$gomod" | head -1)"
  [ -n "$goline" ] || die2 "${gomod}: no 'go' line, so the Go it requires cannot be read"
  gk="$(key_required "$goline")" || die2 "${gomod}: the go line '${goline}' is not a Go version"
  tc="$(sed -n 's/^toolchain[[:space:]]\{1,\}\([^[:space:]]*\).*/\1/p' "$gomod" | head -1)"
  if [ -n "$tc" ] && [ "$tc" != "default" ]; then
    tk="$(key_required "$tc")" || die2 "${gomod}: the toolchain line '${tc}' is not a Go version"
    if ge "$tk" "$gk" && [ "$tk" != "$gk" ]; then
      echo "${tc#go}|${tk}|toolchain ${tc}, above go ${goline}"
      return 0
    fi
    echo "${goline}|${gk}|go ${goline}; toolchain ${tc} is not higher"
    return 0
  fi
  echo "${goline}|${gk}|go ${goline}; no toolchain line"
}

# builder_version <go.mod> — <major>.<minor> of the required version.
builder_version() {
  local r k
  r="$(required_of "$1")"
  k="$(echo "$r" | cut -d'|' -f2)"
  echo "$k" | awk '{print $1 "." $2}'
}

# ── Dockerfile ────────────────────────────────────────────────────────────

# key_tag <tag> — "M m p" of a golang image tag; a part the tag leaves open is
# FLOAT (the image is the newest release of that line). Fails on a tag whose
# version cannot be read.
key_tag() {
  local tag="$1" ver
  ver="${tag%%-*}"
  if [[ "$ver" =~ ^([0-9]+)(\.([0-9]+))?(\.([0-9]+))?$ ]]; then
    local M="${BASH_REMATCH[1]}" m="${BASH_REMATCH[3]:-$FLOAT}" p="${BASH_REMATCH[5]:-$FLOAT}"
    [ "$m" = "$FLOAT" ] && p="$FLOAT"
    echo "$M $m $p"
  elif [[ "$ver" =~ ^([0-9]+)\.([0-9]+)(rc|beta)[0-9]+$ ]]; then
    # A pre-release comes before the .0 release of its line.
    echo "${BASH_REMATCH[1]} ${BASH_REMATCH[2]} -1"
  elif [[ "$ver" =~ ^[0-9] ]]; then
    return 1
  else
    # alpine, bookworm, latest: the newest Go release.
    echo "$FLOAT $FLOAT $FLOAT"
  fi
}

# describe_key <M m p> — words for a builder key.
describe_key() {
  local -a k
  read -r -a k <<<"$1"
  if [ "${k[0]}" = "$FLOAT" ]; then echo "the newest Go release"
  elif [ "${k[1]}" = "$FLOAT" ]; then echo "the newest Go ${k[0]} release"
  elif [ "${k[2]}" = "$FLOAT" ]; then echo "the newest Go ${k[0]}.${k[1]} patch"
  elif [ "${k[2]}" = "-1" ]; then echo "a Go ${k[0]}.${k[1]} pre-release"
  else echo "Go ${k[0]}.${k[1]}.${k[2]}"
  fi
}

# golang_stages <Dockerfile> [NAME=VALUE ...] — one line per golang build stage:
#   <line number>|<image as written>|<image after ARG substitution>
# The build arguments apply only to ARGs declared before the first FROM.
golang_stages() {
  local df="$1"; shift
  local -A val=()
  local -a declared=()
  local kv n line img resolved rest name seen_from=0 lineno=0
  declare -A override=()
  for kv in "$@"; do override["${kv%%=*}"]="${kv#*=}"; done
  while IFS= read -r line || [ -n "$line" ]; do
    lineno=$((lineno + 1))
    line="${line#"${line%%[![:space:]]*}"}"
    case "$line" in ''|'#'*) continue ;; esac
    if [ "$seen_from" -eq 0 ] && [[ "$line" =~ ^[Aa][Rr][Gg][[:space:]]+([A-Za-z_][A-Za-z0-9_]*)(=(.*))?$ ]]; then
      n="${BASH_REMATCH[1]}"
      local d="${BASH_REMATCH[3]:-}"
      d="${d%\"}"; d="${d#\"}"; d="${d%\'}"; d="${d#\'}"
      declared+=("$n")
      if [ -n "${override[$n]+x}" ]; then val["$n"]="${override[$n]}"; else val["$n"]="$d"; fi
      continue
    fi
    if [[ "$line" =~ ^[Ff][Rr][Oo][Mm][[:space:]]+(.*)$ ]]; then
      seen_from=1
      rest="${BASH_REMATCH[1]}"
      # Skip options such as --platform=$BUILDPLATFORM.
      while [[ "$rest" =~ ^--[^[:space:]]+[[:space:]]+(.*)$ ]]; do rest="${BASH_REMATCH[1]}"; done
      img="${rest%%[[:space:]]*}"
      resolved="$img"
      for n in "${declared[@]+"${declared[@]}"}"; do
        # ${NAME}, then $NAME up to the first character that cannot be in a name.
        resolved="${resolved//\$\{$n\}/${val[$n]}}"
        local guard=0
        while [[ "$resolved" =~ ^(.*)\$${n}([^A-Za-z0-9_].*)?$ ]] && [ "$guard" -lt 20 ]; do
          resolved="${BASH_REMATCH[1]}${val[$n]}${BASH_REMATCH[2]}"
          guard=$((guard + 1))
        done
      done
      # The repository name is what stands before the tag and the digest.
      local nodigest="${resolved%%@*}" repo
      repo="$nodigest"
      if [[ "${nodigest##*/}" == *:* ]]; then repo="${nodigest%:*}"; fi
      name="${repo##*/}"
      # A stage whose image still holds an unresolved variable is judged (and
      # fails) when the text as written names the golang repository.
      if [ "$name" = "golang" ] || { [[ "$resolved" == *'$'* ]] && [[ "$img" =~ (^|/)golang([:@]|$) ]]; }; then
        echo "${lineno}|${img}|${resolved}"
      fi
    fi
  done < "$df"
}

# judge_stage <go.mod key> <required text> <df> <line|img|resolved> <mode text>
# Prints one ok/FAIL line; returns 1 on FAIL.
judge_stage() {
  local rkey="$1" rtext="$2" df="$3" rec="$4" mode="$5"
  local lineno img resolved tag nodigest bkey
  lineno="${rec%%|*}"; rec="${rec#*|}"
  img="${rec%%|*}"; resolved="${rec#*|}"
  nodigest="${resolved%%@*}"
  if [[ "$resolved" == *'$'* ]]; then
    echo "::error file=${df},line=${lineno},title=${ME}::${mode}: FROM ${img} names a variable the Dockerfile does not declare before its first FROM, so docker leaves it empty."
    echo "  FAIL ${mode}: ${df}:${lineno} FROM ${img} cannot be resolved (an ARG it uses is not declared before the first FROM)"
    return 1
  fi
  if [[ "${nodigest##*/}" == *:* ]]; then tag="${nodigest##*:}"; else tag=""; fi
  if [ -z "$tag" ] && [[ "$resolved" == *@* ]]; then
    die2 "${df}:${lineno}: FROM ${img} is pinned by digest only, so its Go version cannot be read; add the tag before the digest"
  fi
  if [ -z "$tag" ] && [[ "$nodigest" == *: ]]; then tag="-"; fi
  [ -n "$tag" ] || tag="latest"
  if [[ "$tag" == -* ]]; then
    echo "::error file=${df},line=${lineno},title=${ME}::${mode}: FROM ${img} resolves to ${resolved}, an image without a version: an ARG it uses is empty."
    echo "  FAIL ${mode}: ${df}:${lineno} FROM ${img} resolves to ${resolved} (an ARG it uses is empty)"
    return 1
  fi
  bkey="$(key_tag "$tag")" || die2 "${df}:${lineno}: the golang tag '${tag}' is not a version this check can read"
  if ge "$bkey" "$rkey"; then
    echo "  ok   ${mode}: ${df}:${lineno} FROM ${resolved} is $(describe_key "$bkey"), at least Go ${rtext}"
    return 0
  fi
  echo "::error file=${df},line=${lineno},title=${ME}::${mode}: the builder ${resolved} is $(describe_key "$bkey"), older than go.mod requires (Go ${rtext}). go mod download in that image stops with 'go.mod requires go >= ${rtext} (GOTOOLCHAIN=local)'."
  echo "  FAIL ${mode}: ${df}:${lineno} FROM ${resolved} is $(describe_key "$bkey"), older than go.mod requires (Go ${rtext})"
  return 1
}

# check <go.mod> <Dockerfile> [NAME=VALUE ...] — the whole rule; exit status
# 0 pass, 1 fail, 2 cannot judge.
check() {
  local gomod="$1" df="$2"; shift 2
  local r rver rkey rhow rc=0 rec
  [ -f "$df" ] || die2 "${df}: no such file"
  r="$(required_of "$gomod")"
  rver="${r%%|*}"; r="${r#*|}"; rkey="${r%%|*}"; rhow="${r#*|}"
  echo "${ME}: ${gomod} requires Go ${rver} (${rhow})"
  local stages
  stages="$(golang_stages "$df")"
  [ -n "$stages" ] || die2 "${df}: no build stage uses a golang image, so there is no builder Go to judge"
  if [ "$#" -gt 0 ]; then
    local withargs
    withargs="$(golang_stages "$df" "$@")"
    while IFS= read -r rec; do
      judge_stage "$rkey" "$rver" "$df" "$rec" "with --build-arg $*" || rc=1
    done <<<"$withargs"
  fi
  while IFS= read -r rec; do
    judge_stage "$rkey" "$rver" "$df" "$rec" "plain docker build" || rc=1
  done <<<"$stages"
  if [ "$rc" -ne 0 ]; then
    echo "${ME}: FAIL — the image would be built with a Go older than ${gomod} requires."
    echo "  Build from ARG GO_VERSION and pass GO_VERSION=\$(bash scripts/${ME}.sh --builder-version ${gomod})"
    echo "  as a build argument, so go.mod alone chooses the builder."
  else
    echo "${ME}: PASS"
  fi
  return "$rc"
}

# ── Self-test ─────────────────────────────────────────────────────────────
self_test() {
  local RC=0 n=0
  # Global, so the EXIT trap still sees it after this function returns.
  SELFTEST_DIR="$(mktemp -d)"
  trap 'rm -rf "$SELFTEST_DIR"' EXIT
  local T="$SELFTEST_DIR"
  # expect <want exit> <name> <command ...>
  expect() {
    local want="$1" name="$2"; shift 2
    local got=0
    "$@" >"$T/out" 2>&1 || got=$?
    n=$((n + 1))
    if [ "$got" = "$want" ]; then
      echo "  ok   ${name} (exit ${got})"
    else
      echo "  FAIL ${name}: want exit ${want}, got ${got}"
      sed 's/^/       | /' "$T/out"
      RC=1
    fi
  }
  # expect_out <want text> <name> <command ...>
  expect_out() {
    local want="$1" name="$2"; shift 2
    local got
    got="$("$@" 2>/dev/null || true)"
    n=$((n + 1))
    if [ "$got" = "$want" ]; then echo "  ok   ${name} (${got})"
    else echo "  FAIL ${name}: want '${want}', got '${got}'"; RC=1; fi
  }
  mod() { printf 'module example.invalid/m\n\n%s\n' "$2" > "$T/$1"; [ -z "${3:-}" ] || printf '\n%s\n' "$3" >> "$T/$1"; }
  dfile() { printf '%s\n' "${@:2}" > "$T/$1"; }
  run() { bash "$SELF" --go-mod "$T/$1" --dockerfile "$T/$2" "${@:3}"; }

  echo "== ${ME} self-test (fixtures only, no network, no docker) =="

  # The shape on main when this check was written (#7922).
  mod go125 'go 1.25.0'
  dfile main 'FROM golang:1.22-alpine AS build' 'RUN go mod download' 'FROM scratch'
  expect 1 "main's shape: literal golang:1.22 under go 1.25.0 fails" run go125 main
  expect 1 "a build argument does not rescue a literal FROM" run go125 main --build-arg GO_VERSION=1.25

  # The repaired shape.
  dfile fixed 'ARG GO_VERSION=1' 'FROM golang:${GO_VERSION}-alpine AS build' 'FROM scratch'
  expect 0 "ARG GO_VERSION=1 with GO_VERSION=1.25 passed passes" run go125 fixed --build-arg GO_VERSION=1.25
  expect 0 "ARG GO_VERSION=1 without a build argument passes (newest Go 1)" run go125 fixed
  expect 1 "a build argument older than go.mod fails" run go125 fixed --build-arg GO_VERSION=1.24
  dfile olddefault 'ARG GO_VERSION=1.22' 'FROM golang:${GO_VERSION}-alpine AS build'
  expect 1 "an ARG default older than go.mod fails the plain build" run go125 olddefault --build-arg GO_VERSION=1.25
  dfile nodefault 'ARG GO_VERSION' 'FROM golang:${GO_VERSION}-alpine AS build'
  expect 1 "an ARG without default and no build argument fails (golang:-alpine)" run go125 nodefault
  dfile dollar 'ARG GO_VERSION=1.25' 'FROM golang:$GO_VERSION-alpine AS build'
  expect 0 "the \$NAME form is resolved too" run go125 dollar
  dfile undeclared 'FROM golang:${GO_VERSION}-alpine AS build'
  expect 1 "an ARG not declared before FROM is not resolved by a build argument" run go125 undeclared --build-arg GO_VERSION=1.25

  # Patch precision and the toolchain line.
  mod go1253 'go 1.25.3'
  dfile exact1 'FROM golang:1.25.1-alpine AS build'
  expect 1 "golang:1.25.1 under go 1.25.3 fails" run go1253 exact1
  dfile exact3 'FROM golang:1.25.3-alpine AS build'
  expect 0 "golang:1.25.3 under go 1.25.3 passes" run go1253 exact3
  dfile minor 'FROM golang:1.25-alpine AS build'
  expect 0 "golang:1.25 (newest patch) under go 1.25.3 passes" run go1253 minor
  mod tc 'go 1.25.0' 'toolchain go1.26.2'
  expect 1 "golang:1.25 under toolchain go1.26.2 fails" run tc minor
  dfile minor126 'FROM golang:1.26-alpine AS build'
  expect 0 "golang:1.26 under toolchain go1.26.2 passes" run tc minor126
  mod old 'go 1.22'
  expect 0 "golang:1.22 under go 1.22 (no patch) passes" run old main

  # Image spellings.
  dfile registry "FROM --platform=\$BUILDPLATFORM docker.io/library/golang:1.26.11-alpine3.24@sha256:$(printf 'a%.0s' $(seq 64)) AS build"
  mod go126 'go 1.26.0'
  expect 0 "registry path, --platform and digest are read" run go126 registry
  dfile float 'FROM golang:alpine AS build'
  expect 0 "golang:alpine is the newest release" run go125 float
  dfile comment '# FROM golang:1.20-alpine' 'FROM golang:1.25-alpine AS build'
  expect 0 "a commented FROM line is not read" run go125 comment
  dfile twostage 'FROM golang:1.25-alpine AS a' 'FROM golang:1.22-alpine AS b'
  expect 1 "every golang stage is judged, not only the first" run go125 twostage

  # Refusals: nothing to judge is never a pass.
  dfile nogo 'FROM alpine:3.20' 'FROM scratch'
  expect 2 "a Dockerfile without a golang stage cannot be judged" run go125 nogo
  printf 'module example.invalid/m\n' > "$T/nogoline"
  expect 2 "a go.mod without a go line cannot be judged" run nogoline fixed
  dfile digestonly "FROM golang@sha256:$(printf 'b%.0s' $(seq 64)) AS build"
  expect 2 "a golang image pinned by digest only cannot be judged" run go125 digestonly

  # --builder-version.
  expect_out "1.25" "--builder-version of go 1.25.0" bash "$SELF" --builder-version "$T/go125"
  expect_out "1.26" "--builder-version prefers a higher toolchain line" bash "$SELF" --builder-version "$T/tc"
  expect_out "1.22" "--builder-version of go 1.22" bash "$SELF" --builder-version "$T/old"

  if [ "$RC" -eq 0 ]; then echo "SELF-TEST PASS (${n} cases)"; else echo "SELF-TEST FAIL"; fi
  return "$RC"
}

SELF="${BASH_SOURCE[0]}"
GOMOD="" DF="" MODE=check
ARGS=()
while [ $# -gt 0 ]; do
  case "$1" in
    --go-mod)          GOMOD="${2:-}"; shift 2 ;;
    --dockerfile)      DF="${2:-}"; shift 2 ;;
    --build-arg)       [[ "${2:-}" == ?*=* ]] || die2 "--build-arg wants NAME=VALUE"; ARGS+=("$2"); shift 2 ;;
    --builder-version) MODE=builder; GOMOD="${2:-}"; shift 2 ;;
    --self-test)       MODE=selftest; shift ;;
    *) echo "Usage: $0 --go-mod <go.mod> --dockerfile <Dockerfile> [--build-arg NAME=VALUE ...] | --builder-version <go.mod> | --self-test" >&2
       exit 2 ;;
  esac
done

case "$MODE" in
  selftest) self_test ;;
  builder)  [ -n "$GOMOD" ] || die2 "--builder-version wants a go.mod"; builder_version "$GOMOD" ;;
  check)    [ -n "$GOMOD" ] && [ -n "$DF" ] || die2 "--go-mod and --dockerfile are both required"
            check "$GOMOD" "$DF" "${ARGS[@]+"${ARGS[@]}"}" ;;
esac
