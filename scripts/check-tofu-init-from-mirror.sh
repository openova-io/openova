#!/usr/bin/env bash
# check-tofu-init-from-mirror.sh - tofu init installs providers only from the OCI provider
# mirror, with every outside host unreachable (SOV-342).
#
# The provider pod of provider-opentofu runs `tofu init` for each Workspace with the CLI
# configuration crossplane-system/opentofu-cli-config. This script shows offline what that
# configuration does. It cuts the inline module out of the CloudAdoption composition, builds
# a stub package of each version the ConfigMap opentofu-providers lists (the command
# tools/guards/cmd/tofu-providers: stub, then pack), serves them as an OCI registry over TLS
# on 127.0.0.1 (tofu-providers serve), points the configuration at it (the origin host
# replaced by the served address, $$ read as Flux writes it, the credential file replaced by
# a local one) and runs tofu init with HTTPS_PROXY and HTTP_PROXY set to a closed local port
# and NO_PROXY=127.0.0.1, so no outside host can be reached.
#
# Usage:
#   bash scripts/check-tofu-init-from-mirror.sh [--config FILE]
#       lint FILE (default: the key .tofurc of the kit's ConfigMap opentofu-cli-config) and
#       run init of the composition's module from the served mirror with it.
#   bash scripts/check-tofu-init-from-mirror.sh --self-test
#       four named cases with the kit's configuration, one line each, "ok - " or "not ok - ":
#       init passes at the listed versions; init fails, naming the provider, when the mirror
#       lacks one, and reaches no other host; the lint refuses a direct block; init fails
#       when the credential file does not exist.
#
# The lint refuses a configuration without an oci_mirror block, or with a direct,
# network_mirror, filesystem_mirror or dev_overrides block.
#
# Tools: go (builds tools/guards/cmd/tofu-providers), yq, and a linux/amd64 host. OpenTofu
# is TOFU when it is set (1.10 or later: the first release that reads an OCI provider
# mirror), else release 1.10.8, the one the image of provider-opentofu v1.1.11 installs,
# fetched once with curl into TOFU_CACHE (default: the folder check-tofu-init-from-mirror
# under XDG_CACHE_HOME or ~/.cache), checked against its SHA-256 and unpacked with unzip.
# A missing tool, an older OpenTofu or a failed fetch ends the run with exit code 2; nothing
# is skipped.
#
# Exit codes: 0 every check passed; 1 a check failed; 2 the tree or a tool cannot be used.
set -euo pipefail
export LC_ALL=C
ME=check-tofu-init-from-mirror
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
KIT_CONFIG=clusters/_template/infrastructure/providers/base/opentofu-cli-config.yaml
LIST=clusters/_template/infrastructure/providers/base/opentofu-providers.yaml
COMPOSITION=platform/crossplane-claims/chart/templates/compositions/cloudadoption.yaml
TOFU_RELEASE=1.10.8
TOFU_SHA256=b330c2f41e416403817b34e611b8ea6622c77d918c2478de3aa060353288530e
CLOSED=127.0.0.1:9

die() {
  echo "$ME: $*" >&2
  exit 2
}

need() {
  command -v "$1" > /dev/null 2>&1 || die "$1 is not on the PATH"
}

WORK="$(mktemp -d)"
PIDS=()
cleanup() {
  local p
  for p in "${PIDS[@]}"; do
    kill "$p" 2> /dev/null || true
  done
  rm -rf "$WORK"
}
trap cleanup EXIT

# tofu_bin sets TOFU_BIN to an OpenTofu of release 1.10 or later.
tofu_bin() {
  local cache zip got first
  if [ -n "${TOFU:-}" ]; then
    TOFU_BIN="$TOFU"
  else
    [ "$(uname -s)/$(uname -m)" = Linux/x86_64 ] || die "the fetched OpenTofu and the stub packages are linux/amd64; set TOFU on another host"
    cache="${TOFU_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/check-tofu-init-from-mirror}/tofu-$TOFU_RELEASE"
    TOFU_BIN="$cache/tofu"
    if [ ! -x "$TOFU_BIN" ]; then
      need curl
      need unzip
      mkdir -p "$cache"
      zip="$cache/tofu_${TOFU_RELEASE}_linux_amd64.zip"
      curl -fsSL --retry 3 -o "$zip" "https://github.com/opentofu/opentofu/releases/download/v$TOFU_RELEASE/tofu_${TOFU_RELEASE}_linux_amd64.zip" ||
        die "cannot fetch OpenTofu $TOFU_RELEASE"
      got="$(sha256sum "$zip")"
      got="${got%% *}"
      if [ "$got" != "$TOFU_SHA256" ]; then
        rm -f "$zip"
        die "OpenTofu $TOFU_RELEASE: SHA-256 $got, want $TOFU_SHA256"
      fi
      unzip -q -o "$zip" tofu -d "$cache" || die "cannot unpack $zip"
      rm -f "$zip"
    fi
  fi
  [ -x "$TOFU_BIN" ] || die "OpenTofu $TOFU_BIN cannot be run"
  first="$("$TOFU_BIN" version 2> /dev/null)" || die "$TOFU_BIN version failed"
  read -r first <<< "$first"
  [[ $first =~ ^OpenTofu\ v([0-9]+)\.([0-9]+)\. ]] || die "$TOFU_BIN version printed '$first'"
  if [ "${BASH_REMATCH[1]}" -lt 1 ] || { [ "${BASH_REMATCH[1]}" -eq 1 ] && [ "${BASH_REMATCH[2]}" -lt 10 ]; }; then
    die "$first is older than 1.10, which reads no OCI provider mirror"
  fi
}

# lint prints why a CLI configuration is refused and returns 1, or returns 0.
lint() {
  local text
  text="$(sed -e 's/#.*$//' -e 's#//.*$##' "$1")"
  if ! grep -q -E '(^|[^a-z_])oci_mirror[[:space:]]*[{]' <<< "$text"; then
    echo "$1 has no oci_mirror block"
    return 1
  fi
  if grep -q -E '(^|[^a-z_])(direct|network_mirror|filesystem_mirror|dev_overrides)[[:space:]]*[{]' <<< "$text"; then
    echo "$1 has a direct, network_mirror, filesystem_mirror or dev_overrides block: a provider could come from outside the mirror"
    return 1
  fi
  return 0
}

# setup builds the tool, reads the list, cuts out the module and packs every listed version.
setup() {
  local line src ver dig ns ty zip n
  need go
  need yq
  tofu_bin
  mkdir -p "$WORK/home" "$WORK/zips"
  (cd "$ROOT/tools/guards" && go build -o "$WORK/tofu-providers" ./cmd/tofu-providers) || die "cannot build tools/guards/cmd/tofu-providers"
  BIN="$WORK/tofu-providers"
  yq '.data.providers' "$ROOT/$LIST" > "$WORK/list" || die "cannot read $LIST"
  declare -g -A LISTED=()
  SOURCES=()
  while read -r src ver dig; do
    [ -n "$src" ] || continue
    [[ $src =~ ^registry\.opentofu\.org/([a-z0-9-]+)/([a-z0-9-]+)$ ]] || die "$LIST: '$src' is not a source address"
    ns="${BASH_REMATCH[1]}"
    ty="${BASH_REMATCH[2]}"
    LISTED[$src]="$ver"
    SOURCES+=("$src")
    zip="$("$BIN" stub -out "$WORK/zips" -source "$src" -version "$ver")" || die "stub $src $ver failed"
    "$BIN" pack -out "$WORK/layout/$ns/$ty" -version "$ver" "$zip" > /dev/null || die "pack $src $ver failed"
  done < "$WORK/list"
  [ "${#SOURCES[@]}" -gt 0 ] || die "$LIST lists no package"
  n="$(grep -c -E '^ *module: [|]' "$ROOT/$COMPOSITION")" || true
  [ "$n" = 1 ] || die "$COMPOSITION holds $n inline modules, not 1"
  mkdir -p "$WORK/module"
  awk '!f && /^ *module: [|]/ { k = match($0, /[^ ]/); f = 1; next }
       f { if ($0 ~ /[^ ]/) { i = match($0, /[^ ]/); if (i <= k) exit; if (m == 0 || i < m) m = i }
           l[++n] = $0 }
       END { for (j = 1; j <= n; j++) print substr(l[j], m) }' "$ROOT/$COMPOSITION" > "$WORK/module/main.tf"
  grep -q -E '^ *required_providers' "$WORK/module/main.tf" || die "the inline module of $COMPOSITION has no required_providers block"
}

# repo_path prints the repository path of a source under the configuration's template.
repo_path() {
  local tmpl ns ty
  tmpl="$(sed -e 's/\$\$/$/g' "$1")"
  tmpl="$(sed -n 's/.*repository_template[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' <<< "$tmpl")"
  [ -n "$tmpl" ] || return 1
  [[ $2 =~ ^registry\.opentofu\.org/([a-z0-9-]+)/([a-z0-9-]+)$ ]] || return 1
  ns="${BASH_REMATCH[1]}"
  ty="${BASH_REMATCH[2]}"
  tmpl="${tmpl#*/}"
  tmpl="${tmpl//'${namespace}'/$ns}"
  printf '%s\n' "${tmpl//'${type}'/$ty}"
}

# serve starts the mirror for the configuration $2 with every listed source but $3;
# it sets ADDR and CA. $1 names its files.
serve() {
  local tag="$1" config="$2" leave="${3:-}" src repo args=() fd
  for src in "${SOURCES[@]}"; do
    [ "$src" != "$leave" ] || continue
    repo="$(repo_path "$config" "$src")" || die "$config: no repository_template to map $src"
    args+=("$repo=$WORK/layout/${src#registry.opentofu.org/}")
  done
  mkfifo "$WORK/$tag.addr"
  "$BIN" serve -ca-out "$WORK/$tag.ca" -addr-out "$WORK/$tag.addr" "${args[@]}" > "$WORK/$tag.serve.log" 2>&1 &
  PIDS+=("$!")
  exec {fd}<> "$WORK/$tag.addr"
  if ! read -r -t 60 ADDR <&"$fd"; then
    die "the mirror did not start: $(cat "$WORK/$tag.serve.log")"
  fi
  exec {fd}<&-
  CA="$WORK/$tag.ca"
}

# render writes the configuration $1 as Flux delivers it, pointed at ADDR and at the
# credential file $2, to $3.
render() {
  sed -e 's/\$\$/$/g' \
    -e "s#\\(repository_template[[:space:]]*=[[:space:]]*\"\\)[^/\"]*/#\\1$ADDR/#" \
    -e "s#docker_style_config_files[[:space:]]*=.*#docker_style_config_files = [\"$2\"]#" "$1" > "$3"
}

# init runs tofu init of a fresh copy of the module with the configuration $1, every
# outside host behind the closed port; output to $2. It returns tofu's exit code.
init() {
  local dir
  dir="$(mktemp -d "$WORK/run.XXXX")"
  cp "$WORK/module/main.tf" "$dir/main.tf"
  INIT_DIR="$dir"
  env -i PATH="$PATH" HOME="$WORK/home" TF_CLI_CONFIG_FILE="$1" SSL_CERT_FILE="$CA" \
    HTTPS_PROXY="http://$CLOSED" HTTP_PROXY="http://$CLOSED" https_proxy="http://$CLOSED" http_proxy="http://$CLOSED" \
    NO_PROXY=127.0.0.1 no_proxy=127.0.0.1 \
    "$TOFU_BIN" -chdir="$dir" init -input=false -no-color -backend=false > "$2" 2>&1
}


# installed prints "<source> <version>" for each provider of the lock file tofu wrote.
installed() {
  awk '/^provider "/ { s = $2; gsub(/"/, "", s) } /^  version / { v = $3; gsub(/"/, "", v); print s, v }' "$INIT_DIR/.terraform.lock.hcl"
}

# init_passes runs init with the configuration $1 and checks the installed versions. It
# returns 1 with the reason in WHY, or 0 with FIRST, the first source installed. It runs
# in this shell: the mirror it starts is stopped on exit.
init_passes() {
  local src ver count=0
  FIRST=""
  serve pass "$1"
  echo '{"auths":{}}' > "$WORK/config.json"
  render "$1" "$WORK/config.json" "$WORK/pass.tofurc"
  if ! init "$WORK/pass.tofurc" "$WORK/pass.log"; then
    WHY="tofu init failed: $(tail -n 20 "$WORK/pass.log")"
    return 1
  fi
  installed > "$WORK/installed"
  while read -r src ver; do
    if [ "${LISTED[$src]:-}" != "$ver" ]; then
      WHY="tofu installed $src $ver; $LIST lists ${LISTED[$src]:-no version of it}"
      return 1
    fi
    [ -n "$FIRST" ] || FIRST="$src"
    count=$((count + 1))
  done < "$WORK/installed"
  if [ "$count" = 0 ]; then
    WHY="tofu installed no provider"
    return 1
  fi
}

say_case() { # $1 0 or 1, $2 the case, $3 the reason
  if [ "$1" = 0 ]; then
    echo "ok - $2"
  else
    echo "not ok - $2: $3"
    FAILED=1
  fi
}

# kit_config sets CFG to the key .tofurc of the kit's ConfigMap, written to a file.
kit_config() {
  CFG="$WORK/kit.tofurc"
  yq '.data.".tofurc"' "$ROOT/$KIT_CONFIG" > "$CFG" || die "cannot read $KIT_CONFIG"
  [ -s "$CFG" ] && [ "$(cat "$CFG")" != null ] || die "$KIT_CONFIG has no key .tofurc"
}

self_test() {
  local rc name
  FAILED=0
  setup
  kit_config

  name="init of the inline module of the CloudAdoption composition installs each provider at its listed version from the served mirror, every outside host unreachable"
  rc=0
  WHY=""
  init_passes "$CFG" || rc=1
  say_case "$rc" "$name" "$WHY"

  name="init fails and names the provider when the mirror lacks it, and reaches no other host"
  rc=0
  WHY=""
  if [ -z "$FIRST" ]; then
    rc=1
    WHY="the first case installed nothing to leave out"
  else
    serve absent "$CFG" "$FIRST"
    render "$CFG" "$WORK/config.json" "$WORK/absent.tofurc"
    if init "$WORK/absent.tofurc" "$WORK/absent.log"; then
      rc=1
      WHY="tofu init passed without ${FIRST#registry.opentofu.org/}"
    elif ! grep -q -F "${FIRST#registry.opentofu.org/}" "$WORK/absent.log"; then
      rc=1
      WHY="the failure does not name ${FIRST#registry.opentofu.org/}: $(tail -n 5 "$WORK/absent.log")"
    elif grep -q -F "$CLOSED" "$WORK/absent.log"; then
      rc=1
      WHY="tofu tried a host outside the mirror: $(grep -F "$CLOSED" "$WORK/absent.log")"
    fi
  fi
  say_case "$rc" "$name" "$WHY"

  name="the lint refuses a CLI configuration with a direct block"
  rc=0
  WHY=""
  sed -e 's/^\( *\)provider_installation *{/&\n\1  direct {}/' "$CFG" > "$WORK/direct.tofurc"
  if ! lint "$CFG" > "$WORK/lint.out"; then
    rc=1
    WHY="the lint refuses the kit's configuration: $(cat "$WORK/lint.out")"
  elif ! grep -q -E '^ *direct [{][}]' "$WORK/direct.tofurc"; then
    rc=1
    WHY="no direct block could be added to the kit's configuration"
  elif lint "$WORK/direct.tofurc" > "$WORK/lint.out"; then
    rc=1
    WHY="the lint passed a configuration with a direct block"
  fi
  say_case "$rc" "$name" "$WHY"

  name="init fails when the configuration names a credential file that does not exist"
  rc=0
  WHY=""
  serve nocred "$CFG"
  render "$CFG" "$WORK/absent/config.json" "$WORK/nocred.tofurc"
  if init "$WORK/nocred.tofurc" "$WORK/nocred.log"; then
    rc=1
    WHY="tofu init passed without its credential file"
  elif ! grep -q -F "$WORK/absent/config.json" "$WORK/nocred.log"; then
    rc=1
    WHY="the failure does not name the credential file: $(tail -n 5 "$WORK/nocred.log")"
  fi
  say_case "$rc" "$name" "$WHY"

  return "$FAILED"
}

main() {
  if [ "$#" = 1 ] && [ "$1" = --self-test ]; then
    self_test
    return
  fi
  if [ "$#" = 2 ] && [ "$1" = --config ]; then
    [ -r "$2" ] || die "$2 cannot be read"
    setup
    CFG="$2"
  elif [ "$#" = 0 ]; then
    setup
    kit_config
  else
    die "usage: $0 [--config FILE] | --self-test"
  fi
  if ! lint "$CFG" > "$WORK/lint.out"; then
    echo "not ok - $(cat "$WORK/lint.out")"
    return 1
  fi
  echo "ok - $CFG: one oci_mirror and no other installation method"
  WHY=""
  if ! init_passes "$CFG"; then
    echo "not ok - $WHY"
    return 1
  fi
  echo "ok - init of the inline module of $COMPOSITION installs its providers at the listed versions from the served mirror, every outside host unreachable"
}

main "$@"
