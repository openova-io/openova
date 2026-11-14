#!/usr/bin/env bash
# flight-sweep.sh - list what a wiped deployment left in its cloud account. It
# only reads. Usage: scripts/flight-sweep.sh <variables document> | --self-test
# The document is the wiped deployment's tofu.auto.tfvars.json (a destroy keeps
# it). The script builds products/catalyst/bootstrap/api/cmd/flight-sweep and
# runs it on the document; the JSON report goes to standard output.
# Exit codes: 0 nothing was left; 1 leftovers were found; 2 usage error, or the
# document cannot be read or lacks a key; 3 not measured (the program does not
# build, the provider has no lister, a listing failed, any other exit code).
set -uo pipefail

api="$(cd "$(dirname "$0")/.." && pwd)/products/catalyst/bootstrap/api"

code_of() { case "$1" in 0|1|2|3) echo "$1" ;; *) echo 3 ;; esac; }

sweep() { # sweep <variables document>
  local bin rc
  bin="$(mktemp -d)" || return 3
  if ! (cd "$api" && go build -o "$bin/flight-sweep" ./cmd/flight-sweep) >&2; then
    echo "flight-sweep: not measured: the program does not build" >&2
    rm -rf "$bin"; return 3
  fi
  "$bin/flight-sweep" -vars "$1"; rc=$?
  rm -rf "$bin"
  return "$(code_of "$rc")"
}

self_test() {
  local dir failed=0 got
  dir="$(mktemp -d)" || return 1
  expect() { # expect <case> <wanted exit code> <command...>
    local name="$1" want="$2"; shift 2
    "$@" >/dev/null 2>&1; got=$?
    if [ "$got" = "$want" ]; then echo "ok   $name"; else echo "FAIL $name: exit $got, want $want"; failed=1; fi
  }
  printf '{"provider":"none","sovereign_fqdn":"t1.omani.works","deployment_id":"0123abcd"}\n' > "$dir/unknown.json"
  printf '{"provider":"none","sovereign_fqdn":"t1.omani.works"}\n' > "$dir/no-id.json"
  expect "no argument is a usage error" 2 bash "$0"
  expect "two arguments are a usage error" 2 bash "$0" "$dir/unknown.json" extra
  expect "a document that is not there is a usage error" 2 bash "$0" "$dir/absent.json"
  expect "a document without deployment_id is a usage error" 2 bash "$0" "$dir/no-id.json"
  expect "a provider that is not registered is not measured" 3 bash "$0" "$dir/unknown.json"
  for pair in 0:0 1:1 2:2 3:3 4:3 127:3; do
    expect "program exit ${pair%%:*} becomes ${pair##*:}" 0 test "$(code_of "${pair%%:*}")" = "${pair##*:}"
  done
  rm -rf "$dir"
  if [ "$failed" = 0 ]; then echo "self-test: every case passed"; else echo "self-test: a case failed"; fi
  return "$failed"
}

case "$#:${1:-}" in
  1:--self-test) self_test ;;
  1:*) sweep "$1" ;;
  *) echo "usage: $0 <variables document> | --self-test" >&2; exit 2 ;;
esac
