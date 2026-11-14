#!/usr/bin/env bash
# check-console-bundle.sh - the JavaScript the sovereign-admin console serves
# holds every bootstrap-kit Blueprint name.
#
# The Provision page reads BOOTSTRAP_KIT from catalog.generated.ts, which the
# prebuild script writes from clusters/_template/bootstrap-kit/. A build whose
# context lacks that folder produces an empty list and a page with no
# Blueprint bubbles. This check fails such a build.
#
# It reads EVERY *.js file of the assets folder, not one named file. The
# bundler decides which chunk holds which module, and that changes with the
# code: on 14 Nov 2026 a top-level await moved the route tree, and the list
# with it, out of index-*.js into router-*.js. The check that read only the
# first index-*.js then failed every image build of main although the console
# still served all ten names.
#
# Usage:
#   check-console-bundle.sh <assets-dir>    check the *.js files of the folder
#   check-console-bundle.sh --self-test     show that every rule can fail
#
# Two callers, one list:
#   .github/workflows/catalyst-build.yaml     the assets folder of the built image
#   .github/workflows/test-ungated-trees.yaml the Vite build output, on pull requests
#
# Exit codes: 0 all names present, 1 a rule failed, 2 usage error.

set -euo pipefail

# The names the Provision page must be able to show. A task that removes one
# of these components from the bootstrap kit must remove its name here.
NAMES="bp-cilium bp-cert-manager bp-flux bp-crossplane bp-sealed-secrets
bp-spire bp-nats-jetstream bp-openbao bp-keycloak bp-gitea"

# Lowest total size, in bytes, of all *.js files of a real build.
MIN_TOTAL_BYTES=100000

check_dir() {
  local dir="$1" total=0 file size failed=0 bp holders

  if [ ! -d "$dir" ]; then
    echo "Console bundle check FAILED: $dir is not a folder"
    return 1
  fi

  local files=()
  while IFS= read -r file; do
    files+=("$file")
  done < <(find "$dir" -type f -name '*.js' | LC_ALL=C sort)

  if [ "${#files[@]}" -eq 0 ]; then
    echo "Console bundle check FAILED: no *.js file under $dir"
    return 1
  fi

  for file in "${files[@]}"; do
    size=$(wc -c < "$file")
    total=$((total + size))
  done
  echo "Console bundle: ${#files[@]} JavaScript files, $total bytes in total"
  if [ "$total" -lt "$MIN_TOTAL_BYTES" ]; then
    echo "Console bundle check FAILED: bundle suspiciously small ($total bytes, lowest allowed $MIN_TOTAL_BYTES)"
    return 1
  fi

  for bp in $NAMES; do
    holders=""
    for file in "${files[@]}"; do
      if grep -q -F -- "$bp" "$file"; then
        holders="$holders ${file#"$dir"/}"
      fi
    done
    if [ -z "$holders" ]; then
      echo "Console bundle check FAILED: ${bp} is in no JavaScript file"
      failed=1
    else
      echo "Console bundle OK: ${bp} in${holders}"
    fi
  done
  return "$failed"
}

self_test() {
  local tmp rc=0 bp
  tmp=$(mktemp -d)
  # shellcheck disable=SC2064
  trap "rm -rf '$tmp'" EXIT

  # Padding that holds no Blueprint name, large enough for the size rule.
  pad() { head -c "$1" /dev/zero | tr '\0' 'x'; }

  expect() { # expect <wanted exit code> <case name> <dir>
    local want="$1" name="$2" dir="$3" got=0
    check_dir "$dir" > "$tmp/out.txt" 2>&1 || got=$?
    if [ "$got" -ne "$want" ]; then
      echo "SELF-TEST FAIL: $name: exit $got, wanted $want"
      sed 's/^/    /' "$tmp/out.txt"
      rc=1
    else
      echo "self-test ok: $name (exit $got)"
    fi
  }

  # 1. All names in the entry chunk: pass.
  mkdir -p "$tmp/one"
  { pad 120000; echo "$NAMES"; } > "$tmp/one/index-a.js"
  expect 0 "all names in index-*.js" "$tmp/one"

  # 2. All names in another chunk, none in index-*.js: pass. This is the
  #    layout of 14 Nov 2026 that the old one-file check failed on.
  mkdir -p "$tmp/split"
  pad 120000 > "$tmp/split/index-a.js"
  echo "$NAMES" > "$tmp/split/router-b.js"
  expect 0 "all names in router-*.js, none in index-*.js" "$tmp/split"

  # 3. Names spread over two chunks: pass.
  mkdir -p "$tmp/spread"
  { pad 120000; echo "bp-cilium bp-cert-manager bp-flux bp-crossplane bp-sealed-secrets"; } > "$tmp/spread/index-a.js"
  echo "bp-spire bp-nats-jetstream bp-openbao bp-keycloak bp-gitea" > "$tmp/spread/router-b.js"
  expect 0 "names spread over two chunks" "$tmp/spread"

  # 4. Each name missing in turn: fail.
  for bp in $NAMES; do
    mkdir -p "$tmp/no-$bp"
    { pad 120000; echo "$NAMES" | tr ' ' '\n' | grep -v -x -F -- "$bp"; } > "$tmp/no-$bp/index-a.js"
    expect 1 "$bp missing" "$tmp/no-$bp"
  done

  # 5. Large bundle with no name at all (the empty BOOTSTRAP_KIT build): fail.
  mkdir -p "$tmp/nonames"
  pad 120000 > "$tmp/nonames/index-a.js"
  expect 1 "large bundle with no name" "$tmp/nonames"

  # 6. All names present but the bundle is too small: fail.
  mkdir -p "$tmp/small"
  echo "$NAMES" > "$tmp/small/index-a.js"
  expect 1 "all names, bundle too small" "$tmp/small"

  # 7. No *.js file: fail. The names sit in a file that is not served as
  #    JavaScript, so they must not count.
  mkdir -p "$tmp/nojs"
  { pad 120000; echo "$NAMES"; } > "$tmp/nojs/index-a.css"
  expect 1 "no *.js file" "$tmp/nojs"

  # 8. Empty folder and missing folder: fail.
  mkdir -p "$tmp/empty"
  expect 1 "empty folder" "$tmp/empty"
  expect 1 "missing folder" "$tmp/does-not-exist"

  if [ "$rc" -ne 0 ]; then
    echo "check-console-bundle self-test FAILED"
    return 1
  fi
  echo "check-console-bundle self-test passed"
}

case "${1:-}" in
  --self-test) self_test ;;
  ""|-h|--help)
    echo "Usage: $0 <assets-dir> | --self-test" >&2
    exit 2 ;;
  *)
    if [ $# -ne 1 ]; then
      echo "Usage: $0 <assets-dir> | --self-test" >&2
      exit 2
    fi
    check_dir "$1" ;;
esac
