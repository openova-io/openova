#!/usr/bin/env bash
# check-provider-contract.sh — the provider contract check (gate check CLD-G03).
#
#   bash scripts/check-provider-contract.sh              run the check
#   bash scripts/check-provider-contract.sh --self-test  prove each rule can fail
#
# The rules are Go tests of two packages below
# products/catalyst/bootstrap/api/internal: cloudprofile (rules 1 to 4) and
# providers/conformance (TestProviderPacks, rules 5 to 15). They read
# infra/providers, or the directory in CLOUDPROFILE_PROVIDERS_DIR and
# CONFORMANCE_PROVIDERS_DIR.
#   1  Every infra/providers/<pack>/profile.yaml is valid against
#      infra/providers/_contract/cloudprofile.schema.json and is named <pack>.
#   2  Every infra/providers/<pack>/pack.yaml is valid against
#      infra/providers/_contract/pack.schema.json and names its own three roots.
#   3  No profile.yaml or pack.yaml holds a value of an installation.
#   4  infra/providers/_contract/contract.json has its form, and each fixture
#      sets every required contract variable and nothing else.
#   5  Every module declares every contract variable with the contract's type
#      and sensitive flag; a required one has no default, an optional one has
#      one, and the contract's default where the contract sets one.
#   6  Every module declares every contract output.
#   7  pack.yaml names exactly the OpenTofu providers the module declares.
#   8  Boot data never replaces a node: a resource that sets user_data lists
#      it in lifecycle.ignore_changes.
#   9  No resource carries a port of the node port range 30000 to 32767.
#  10  tests/contract.tftest.hcl of every module ends, after its marker line,
#      with infra/providers/_contract/contract.tftest.body.hcl byte for byte.
#  11  Every pack has the seal hook seal/ of contract C8
#      (docs/contracts/c8-seal-provider.md), and it declares exactly the
#      inputs and the outputs of the contract.
#  12  The hook's output kind is a kind of the contract and equals
#      spec.seal.kind of the profile; the module emits seal_kind as
#      module.seal.kind.
#  13  The module calls the hook once as module "seal" with source ./seal and
#      declares random_password.k3s_server_token and k3s_agent_token.
#  14  Every call of the shared control-plane template passes every template
#      input of the contract, and the same arguments as every other module.
#  15  The template input gateway_host_network is a literal in every call,
#      true exactly when the profile's spec.network.loadBalancer.mode is
#      external-hostport.
# The other half of gate check CLD-G03, `tofu test` in every module, needs
# OpenTofu and runs in the workflows catalyst-build, infra-hetzner-tofu,
# infra-huawei-tofu and infra-local-tofu.
#
# Exit codes: 0 pass, 1 a rule failed (or, with --self-test, a rule could not
# fail), 2 usage error. No cluster, no credential, no network beyond the Go
# module download.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
API="$ROOT/products/catalyst/bootstrap/api"
PKGS=(./internal/cloudprofile/... ./internal/providers/conformance/...)

run_check() {
  (cd "$API" && go test -count=1 "${PKGS[@]}")
}

# run_on <dir>: the file rules (1 to 15) against a copy of infra/providers.
run_on() {
  (cd "$API" && CLOUDPROFILE_PROVIDERS_DIR="$1" CONFORMANCE_PROVIDERS_DIR="$1" go test -count=1 -run '^(TestPackFiles|TestContractFile|TestProviderPacks)$' "${PKGS[@]}") >/dev/null 2>&1
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
      mkdir -p "$tmp/p/$n/tests"
      cp "$d/profile.yaml" "$d/pack.yaml" "$d"/*.tf "$tmp/p/$n/"
      cp "$d/tests/contract.tftest.hcl" "$tmp/p/$n/tests/"
      if [ -d "$d/seal" ]; then cp -R "$d/seal" "$tmp/p/$n/seal"; fi
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

  copy
  sed -i 's/^variable "org_email" {$/variable "org_email_renamed" {/' "$tmp/p/$first/variables.tf"
  expect fail "rule 5: a module without a contract variable fails"

  copy
  sed -i 's/^    worker_count       = number$/    worker_count       = string/' "$tmp/p/$first/variables.tf"
  expect fail "rule 5: a contract variable of another type fails"

  copy
  sed -i 's/^variable "org_name" {$/&\n  default = "x"/' "$tmp/p/$first/variables.tf"
  expect fail "rule 5: a required contract variable with a default fails"

  copy
  sed -i 's/^output "contract_version" {$/output "contract_version_renamed" {/' "$tmp/p/$first/outputs.tf"
  expect fail "rule 6: a module without a contract output fails"

  copy
  sed -i '/hashicorp\/random$/d' "$tmp/p/$first/pack.yaml"
  expect fail "rule 7: a pack that does not name a provider of its module fails"

  copy
  sed -i -E 's/^(    ignore_changes = \[.*)user_data(.*\])$/\1name\2/' "$tmp"/p/[!_]*/main.tf
  expect fail "rule 8: boot data outside lifecycle.ignore_changes fails"

  copy
  printf 'resource "example_firewall" "selftest" {\n  rule {\n    port = "30053"\n  }\n}\n' >> "$tmp/p/$first/main.tf"
  expect fail "rule 9: a port of the node port range fails"

  copy
  printf '# one more line\n' >> "$tmp/p/$first/tests/contract.tftest.hcl"
  expect fail "rule 10: a contract test that differs from the shared body fails"

  copy
  sed -i '/^  seal: /d' "$tmp/p/$first/profile.yaml"
  expect fail "rule 1: a profile without spec.seal fails"

  copy
  rm -rf "$tmp/p/$first/seal"
  expect fail "rule 11: a pack without a seal hook fails"

  copy
  printf 'variable "extra_input" {\n  type = string\n}\n' >> "$tmp/p/$first/seal/main.tf"
  expect fail "rule 11: a seal hook with an input outside the contract fails"

  copy
  sed -i 's/^variable "region_keys" { type = set(string) }$/variable "region_keys" { type = list(string) }/' "$tmp/p/$first/seal/main.tf"
  expect fail "rule 11: a seal hook input of another type fails"

  copy
  sed -i 's/^output "volume_device" /output "volume_device_renamed" /' "$tmp/p/$first/seal/main.tf"
  expect fail "rule 11: a seal hook without a contract output fails"

  copy
  sed -i '/^  sensitive = true$/d' "$tmp/p/$first/seal/main.tf"
  expect fail "rule 11: a seal hook whose node files are not sensitive fails"

  copy
  sed -i 's/^  seal: {kind: static-node-file}$/  seal: {kind: cloud-key-service}/' "$tmp/p/$first/profile.yaml"
  expect fail "rule 12: a profile whose seal kind differs from the hook fails"

  copy
  sed -i 's/^  value       = module\.seal\.kind$/  value       = "static-node-file"/' "$tmp/p/$first/outputs.tf"
  expect fail "rule 12: a module whose seal_kind is not the hook kind fails"

  copy
  sed -i 's/^module "seal" {$/module "seal_renamed" {/' "$tmp/p/$first/main.tf"
  expect fail "rule 13: a module that does not call the seal hook fails"

  copy
  sed -i 's/^resource "random_password" "k3s_agent_token" {$/resource "random_password" "k3s_agent_token_renamed" {/' "$tmp/p/$first/main.tf"
  expect fail "rule 13: a module without the agent token fails"

  copy
  sed -i '/^ *fail_mark_cmd  *= /d' "$tmp/p/$first/main.tf"
  expect fail "rule 14: a call of the template without a contract input fails"

  copy
  sed -i 's/^\( *gateway_host_network  *= \)false$/\1true/' "$tmp/p/$first/main.tf"
  expect fail "rule 15: a gateway_host_network that differs from the profile fails"

  [ "$failed" = 0 ] || { echo "provider-contract self-test: FAILED"; return 1; }
  echo "provider-contract self-test: ok"
}

case "${1:-}" in
  "") run_check ;;
  --self-test) self_test ;;
  *) echo "usage: $0 [--self-test]" >&2; exit 2 ;;
esac
