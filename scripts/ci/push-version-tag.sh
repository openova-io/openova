#!/usr/bin/env bash
# push-version-tag.sh - publish a version tag of an image once; a version tag that exists is never moved.
#
# A version tag (one read from a chart's appVersion, a package version or a release number) names
# one image for good. Commit tags and moving tags (latest, main) are not this script's business.
#
# Measured 18 Nov 2026: build-chargeback.yaml pushed ghcr.io/openova-io/chargeback:0.1.67 three
# times (sha256:d326a9d9..., 51c16e5d..., 509d6807...). The runs of a chart-template change and of a
# DESIGN.md change rebuilt the unchanged version, a build is not byte-reproducible, so each run
# moved the tag. A Sovereign's copy followed the moved tag and two gate runs saw two digests for one
# version. Every workflow that publishes a version tag publishes it through this script, and
# scripts/check-version-tags-immutable.py fails a workflow that pushes one any other way.
#
# Usage:
#   push-version-tag.sh <repository>:<version> <repository>@sha256:<digest>
#     The image <repository>@<digest> is already in the registry: the run pushed it under its
#     commit tag, under a moving tag, or by digest. The script reads what <repository>:<version>
#     names now and then:
#       the tag does not exist       tags the digest as <version>, as a carbon copy of the manifest
#                                    (the digest does not change), reads the tag back      -> exit 0
#       the tag exists               pushes nothing, whatever digest it names; a notice
#                                    names the tag and the digest that stays               -> exit 0
#       it cannot be read            pushes nothing; a tag that could not be read is never
#                                    taken for a tag that does not exist                   -> exit 1
#       the read-back differs        the registry did not keep the digest                  -> exit 1
#     Wrong arguments: exit 2.
#     A rebuild is not byte-reproducible, so an existing tag with another digest is the normal
#     result of a run that a non-source change started, and the job stays green. That a source
#     change bumps the version is enforced before merge by
#     scripts/check-appversion-bumped-with-source.sh.
#   push-version-tag.sh --self-test
#     Runs every case above against a stand-in for the registry; contacts no registry.
#
# Tools: docker buildx imagetools (inspect, create) and python3, both on every runner and both
# already used by the build workflows. DOCKER (default: docker), PUSH_VERSION_TAG_ATTEMPTS
# (default: 3) and PUSH_VERSION_TAG_RETRY_SLEEP (seconds, default: 5, doubled after each attempt)
# exist for the self-test.
set -euo pipefail

DOCKER="${DOCKER:-docker}"
ATTEMPTS="${PUSH_VERSION_TAG_ATTEMPTS:-3}"
RETRY_SLEEP="${PUSH_VERSION_TAG_RETRY_SLEEP:-5}"
ME="push-version-tag"

usage() {
  echo "usage: $0 <repository>:<version> <repository>@sha256:<digest> | --self-test" >&2
  exit 2
}

# read_ref <ref> <go-template>: print the template rendered for <ref>.
# Returns 0 when read, 3 when the registry says the tag or repository does not exist, 2 when it
# could not be read after every attempt (the last error is printed on stderr).
read_ref() {
  local ref="$1" format="$2" attempt=1 wait="$RETRY_SLEEP" out
  while :; do
    if out="$("$DOCKER" buildx imagetools inspect "$ref" --format "$format" 2>"$WORK/err")"; then
      printf '%s\n' "$out"
      return 0
    fi
    if grep -qiE 'not found|manifest unknown|name unknown' "$WORK/err"; then
      return 3
    fi
    if [ "$attempt" -ge "$ATTEMPTS" ]; then
      echo "$ME: $ref could not be read after $attempt attempt(s): $(grep . "$WORK/err" | tail -n 1)" >&2
      return 2
    fi
    sleep "$wait"
    wait=$((wait * 2))
    attempt=$((attempt + 1))
  done
}

# digest_of <ref>: print the manifest digest that <ref> names; same return codes as read_ref.
digest_of() {
  local json rc=0
  json="$(read_ref "$1" '{{json .Manifest}}')" || rc=$?
  [ "$rc" -eq 0 ] || return "$rc"
  printf '%s' "$json" | python3 -c 'import json, sys; print(json.load(sys.stdin)["digest"])'
}

publish() {
  local target="$1" source="$2" repo tag src_repo want have rc=0
  # <repository>:<version>: the tag is what follows the last colon, and holds no slash (a colon
  # before a slash belongs to a registry port).
  case "$target" in *@*) usage ;; esac
  tag="${target##*:}"
  repo="${target%:*}"
  case "$tag" in ''|*/*) usage ;; esac
  [ "$repo" != "$target" ] || usage
  src_repo="${source%@*}"
  want="${source#*@}"
  [ "$src_repo" != "$source" ] || usage
  [[ "$want" =~ ^sha256:[0-9a-f]{64}$ ]] || usage
  # The carbon copy happens inside one repository; a copy across repositories is another tool.
  [ "$src_repo" = "$repo" ] || { echo "$ME: $source is not in the repository of $target" >&2; exit 2; }

  have="$(digest_of "$target")" || rc=$?
  case "$rc" in
    0) ;;
    3)
      echo "$ME: $target does not exist; tagging $want"
      "$DOCKER" buildx imagetools create --prefer-index=false --tag "$target" "$source"
      rc=0
      have="$(digest_of "$target")" || rc=$?
      if [ "$rc" -ne 0 ] || [ "$have" != "$want" ]; then
        echo "::error::$ME: after tagging, $target names '${have:-nothing readable}', not $want"
        exit 1
      fi
      echo "$ME: PUSHED $target = $want"
      return 0
      ;;
    *)
      echo "::error::$ME: $target could not be read, so it was not pushed: an unreadable tag is never taken for a missing one"
      exit 1
      ;;
  esac

  # The tag exists: it stays, whatever this run built. A rebuild is not byte-reproducible, so a
  # run that a non-source change started always has another digest; that is no failure. A source
  # change without a version bump is refused before merge, by
  # scripts/check-appversion-bumped-with-source.sh.
  if [ "$have" = "$want" ]; then
    echo "::notice::$ME: KEPT $target stays $have (this run built the same digest); nothing pushed"
  else
    echo "::notice::$ME: KEPT $target stays $have; this run built $want, which keeps only its commit tag. A version tag is never moved; nothing pushed"
  fi
  return 0
}

# ---------------------------------------------------------------- self-test
#
# The stand-in for `docker buildx imagetools` keeps a registry in a folder: tags/<ref> holds the
# digest a tag names, images/<digest> the image configuration of a digest. It logs every create.

FAKE_DOCKER='#!/usr/bin/env bash
reg="$FAKE_REG"
[ "$1 $2" = "buildx imagetools" ] || { echo "fake docker: unexpected: $*" >&2; exit 9; }
cmd="$3"; shift 3
key() { printf "%s" "$1" | tr "/:@" "___"; }
resolve() {
  case "$1" in
    *@sha256:*) d="${1##*@}"; [ -f "$reg/images/$d" ] && echo "$d" ;;
    *) [ -f "$reg/tags/$(key "$1")" ] && cat "$reg/tags/$(key "$1")" ;;
  esac
  return 0
}
case "$cmd" in
  inspect)
    ref="$1"; fmt="$3"
    if [ "$ref" = "${FAKE_DENY:-}" ]; then echo "ERROR: failed to authorize: denied" >&2; exit 1; fi
    d="$(resolve "$ref")"
    [ -n "$d" ] || { echo "ERROR: $ref: not found" >&2; exit 1; }
    case "$fmt" in
      "{{json .Manifest}}") printf "{\"mediaType\":\"application/vnd.oci.image.manifest.v1+json\",\"digest\":\"%s\",\"size\":1}\n" "$d" ;;
      "{{json .Image}}") cat "$reg/images/$d" ;;
      *) echo "fake docker: unexpected format $fmt" >&2; exit 9 ;;
    esac ;;
  create)
    [ "$1" = "--prefer-index=false" ] && [ "$2" = "--tag" ] || { echo "fake docker: unexpected create: $*" >&2; exit 9; }
    d="$(resolve "$4")"
    [ -n "$d" ] || { echo "ERROR: $4: not found" >&2; exit 1; }
    [ -z "${FAKE_CREATE_WRONG:-}" ] || d="$FAKE_CREATE_WRONG"
    echo "$3 $d" >> "$reg/creates.log"
    printf "%s" "$d" > "$reg/tags/$(key "$3")" ;;
  *) echo "fake docker: unexpected command $cmd" >&2; exit 9 ;;
esac
'

self_test() {
  local base problems=0 repo="ghcr.io/x/app" a b c
  base="$(mktemp -d)"
  a="sha256:$(printf 'a%.0s' {1..64})"
  b="sha256:$(printf 'b%.0s' {1..64})"
  c="sha256:$(printf 'c%.0s' {1..64})"
  printf '%s' "$FAKE_DOCKER" > "$base/docker"
  chmod +x "$base/docker"

  # case <name> <want-exit> <want-creates> <want-text> <setup...>: a fresh registry holding the
  # new image $a (configuration A) under the commit tag; <setup> adds to it; the script runs on
  # $repo:1.0.0 from $repo@$a.
  run_case() {
    local name="$1" want_exit="$2" want_creates="$3" want_text="$4" reg out got_exit=0 creates
    shift 4
    reg="$base/$name"
    mkdir -p "$reg/tags" "$reg/images"
    : > "$reg/creates.log"
    printf '{"rootfs":"A"}' > "$reg/images/$a"
    printf '%s' "$a" > "$reg/tags/ghcr.io_x_app_abc1234"
    "$@" "$reg"
    out="$(FAKE_REG="$reg" DOCKER="$base/docker" PUSH_VERSION_TAG_ATTEMPTS=2 PUSH_VERSION_TAG_RETRY_SLEEP=0 \
      bash "$SELF" "${TARGET:-$repo:1.0.0}" "${SOURCE:-$repo@$a}" 2>&1)" || got_exit=$?
    creates="$(wc -l < "$reg/creates.log" | tr -d ' ')"
    if [ "$got_exit" != "$want_exit" ] || [ "$creates" != "$want_creates" ] || ! printf '%s' "$out" | grep -qF -- "$want_text"; then
      echo "$ME: SELF-TEST FAIL case '$name': exit $got_exit (want $want_exit), creates $creates (want $want_creates), text '$want_text' in: $out"
      problems=$((problems + 1))
    else
      echo "$ME: self-test case '$name' ok"
    fi
  }
  nothing() { :; }
  same_digest() { printf '%s' "$a" > "$1/tags/ghcr.io_x_app_1.0.0"; }
  other_image() { printf '{"rootfs":"C"}' > "$1/images/$c"; printf '%s' "$c" > "$1/tags/ghcr.io_x_app_1.0.0"; }
  other_digest() { printf '{"rootfs":"A"}' > "$1/images/$b"; printf '%s' "$b" > "$1/tags/ghcr.io_x_app_1.0.0"; }

  run_case absent 0 1 "PUSHED $repo:1.0.0 = $a" nothing
  run_case same-digest 0 0 "::notice::$ME: KEPT $repo:1.0.0 stays $a (this run built the same digest)" same_digest
  run_case other-image 0 0 "::notice::$ME: KEPT $repo:1.0.0 stays $c; this run built $a" other_image
  run_case other-digest 0 0 "::notice::$ME: KEPT $repo:1.0.0 stays $b; this run built $a" other_digest
  FAKE_DENY="$repo:1.0.0" run_case unreadable 1 0 "never taken for a missing one" nothing
  FAKE_CREATE_WRONG="$c" run_case read-back 1 1 "not $a" nothing
  SOURCE="ghcr.io/x/other@$a" run_case other-repository 2 0 "is not in the repository" nothing
  TARGET="$repo" run_case no-tag 2 0 "usage" nothing
  TARGET="localhost:5000/x/app" SOURCE="localhost:5000/x/app@$a" run_case port-no-tag 2 0 "usage" nothing
  SOURCE="$repo:abc1234" run_case source-not-by-digest 2 0 "usage" nothing

  rm -rf "$base"
  if [ "$problems" -ne 0 ]; then
    echo "$ME: SELF-TEST FAILED: $problems case(s)"
    return 1
  fi
  echo "$ME: self-test ok, 10 cases: absent pushes once; an existing tag (same digest, another image) pushes nothing and passes with a notice; an unreadable tag and a wrong read-back fail; wrong arguments exit 2"
}

SELF="${BASH_SOURCE[0]}"
if [ "${1:-}" = "--self-test" ] && [ $# -eq 1 ]; then
  self_test
  exit $?
fi
[ $# -eq 2 ] || usage
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
publish "$1" "$2"
