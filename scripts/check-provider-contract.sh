#!/usr/bin/env bash
# check-provider-contract.sh — the provider contract check (gate check CLD-G03).
#
#   bash scripts/check-provider-contract.sh              run the check
#   bash scripts/check-provider-contract.sh --self-test  prove each rule can fail
#
# The rules are Go tests of the package
# products/catalyst/bootstrap/api/internal/cloudprofile. They read
# infra/providers, or the directory in CLOUDPROFILE_PROVIDERS_DIR.
#   1  Every infra/providers/<pack>/profile.yaml is valid against
#      infra/providers/_contract/cloudprofile.schema.json and is named <pack>.
#   2  Every infra/providers/<pack>/pack.yaml is valid against
#      infra/providers/_contract/pack.schema.json and names its own three roots.
#   3  No profile.yaml or pack.yaml holds a value of an installation.
#   4  infra/providers/_contract/contract.json has its form, and each fixture
#      sets every required contract variable and nothing else.
# Work package CLD-090 adds TestProviderPacks and the tofu test of every module.
#
# Exit codes: 0 pass, 1 a rule failed (or, with --self-test, a rule could not
# fail), 2 usage error. No cluster, no credential, no network beyond the Go
# module download.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
API="$ROOT/products/catalyst/bootstrap/api"
PKG="./internal/cloudprofile/..."

run_check() {
  (cd "$API" && go test -count=1 "$PKG")
}

# run_on <dir>: the file rules (1 to 4) against a copy of infra/providers.
run_on() {
  (cd "$API" && CLOUDPROFILE_PROVIDERS_DIR="$1" go test -count=1 -run '^(TestPackFiles|TestContractFile)$' "$PKG") >/dev/null 2>&1
}

self_test() {
  local tmp failed=0 first
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN
  copy() {
    rm -rf "$tmp/p"
    mkdir -p "$tmp/p"
    cp -R "$ROOT/infra/providers/_contract" "$tmp/p/_contract"
    local d n
    for d in "$ROOT"/infra/providers/*/; do
      n="$(basename "$d")"
      case "$n" in _*) continue ;; esac
      mkdir -p "$tmp/p/$n"
      cp "$d/profile.yaml" "$d/pack.yaml" "$tmp/p/$n/"
    done
  }
  expect() { # expect <pass|fail> <name>
    local got=fail
    if run_on "$tmp/p"; then got=pass; fi
    if [ "$got" = "$1" ]; then echo "ok   $2"; else echo "FAIL $2 (got $got, want $1)"; failed=1; fi
  }
  copy
  first="$(cd "$tmp/p" && ls -d [!_]*/ | head -1 | tr -d /)"
  expect pass "the files of this repository pass"

  copy
  printf '  unknownSection: {}\n' >> "$tmp/p/$first/profile.yaml"
  expect fail "rule 1: a profile with an unknown field fails"

  copy
  sed -i '/^  leakTokens:/d' "$tmp/p/$first/pack.yaml"
  expect fail "rule 2: a pack without leakTokens fails"

  copy
  # The literal is assembled at run time, so this file does not name an installation.
  sed -i "s/^  displayName: .*/  displayName: x-$(printf 'me-%s-215' east)/" "$tmp/p/$first/profile.yaml"
  expect fail "rule 3: a profile with an installation value fails"

  copy
  sed -i 's/"name": "deployment_id", "type": "string", "required": true, "sensitive": false, "default": null/"name": "deployment_id", "type": "string", "required": true, "sensitive": false, "default": ""/' "$tmp/p/_contract/contract.json"
  expect fail "rule 4: a required contract variable with a default fails"

  copy
  sed -i '/"deployment_id"/d' "$tmp/p/_contract/fixtures/single-region.tfvars.json"
  expect fail "rule 4: a fixture without a required variable fails"

  [ "$failed" = 0 ] || { echo "provider-contract self-test: FAILED"; return 1; }
  echo "provider-contract self-test: ok"
}

case "${1:-}" in
  "") run_check ;;
  --self-test) self_test ;;
  *) echo "usage: $0 [--self-test]" >&2; exit 2 ;;
esac
