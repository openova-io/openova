#!/usr/bin/env bash
# check-gateway-lb-active-standby.sh — the members of the reference cloud's
# gateway and console balancers carry a weight the provider can apply.
#
# The ruling (CLD-G12): the cloud's balancer admits BOTH regions' entry points;
# which region serves is decided inside the platform (Gateway API routes, the
# secondary edge of kit slot 13e, ADR-0015 placement), not at the balancer. So
# every member of every region carries the SAME positive weight.
#
# #6837 asked for weight 0 on the secondary region's members. It never took
# effect: the huaweicloud provider sends the weight through utils.ValueIgnoreEmpty
# on create and on update (resource_huaweicloud_elb_member.go lines 202 and 371),
# which drops a 0; the cloud applies its default of 1 and every second plan
# showed 24 in-place changes. This guard therefore fails when
#   1. a member resource carries no weight, or a weight that is not
#      `.primary ? local.gateway_lb_primary_weight : var.secondary_region_lb_weight`;
#   2. a member's weight is written as 0 anywhere in its expression;
#   3. the primary weight is not a positive integer, or the default of
#      secondary_region_lb_weight differs from it;
#   4. the validation of secondary_region_lb_weight admits 0.
#
# The name of the file is the name of #6837's guard, kept so its workflow, its
# path filters and the provider-leak baseline keep pointing at it.
#
# Usage: check-gateway-lb-active-standby.sh [main.tf variables.tf]
#        check-gateway-lb-active-standby.sh --self-test
set -euo pipefail

if [[ "${1:-}" == "--self-test" ]]; then
  self="$0"
  tf=infra/providers/huawei/main.tf
  vars=infra/providers/huawei/variables.tf
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  bad=0
  expect() { # expect <pass|fail> <label> <main.tf> <variables.tf>
    if bash "$self" "$3" "$4" > "$tmp/out.txt" 2>&1; then got=pass; else got=fail; fi
    if [[ "$got" == "$1" ]]; then echo "  self-test ok: $2 -> $got"; else
      echo "SELF-TEST FAILED: $2 -> $got, expected $1"; cat "$tmp/out.txt"; bad=1; fi
  }
  expect pass "the tree as it is" "$tf" "$vars"
  sed -E '/^variable "secondary_region_lb_weight"/,/^}/ s/^(  default     = )1$/\10/' "$vars" > "$tmp/v-default0.tf"
  expect fail "secondary default 0" "$tf" "$tmp/v-default0.tf"
  sed -E '/^variable "secondary_region_lb_weight"/,/^}/ s/^(  default     = )1$/\12/' "$vars" > "$tmp/v-default2.tf"
  expect fail "secondary default 2, primary 1 (weights differ)" "$tf" "$tmp/v-default2.tf"
  sed -E 's/secondary_region_lb_weight >= 1 /secondary_region_lb_weight >= 0 /' "$vars" > "$tmp/v-admits0.tf"
  expect fail "validation admits 0" "$tf" "$tmp/v-admits0.tf"
  sed -E '0,/: var\.secondary_region_lb_weight$/s//: 0/' "$tf" > "$tmp/m-literal0.tf"
  expect fail "a member weight written as 0" "$tmp/m-literal0.tf" "$vars"
  sed -E 's/^(  gateway_lb_primary_weight = )1$/\10/' "$tf" > "$tmp/m-primary0.tf"
  expect fail "primary weight 0" "$tmp/m-primary0.tf" "$vars"
  awk '/^  weight        = local\.gateway_lb_members/ && !done {done=1; next} {print}' "$tf" > "$tmp/m-noweight.tf"
  expect fail "a member without a weight" "$tmp/m-noweight.tf" "$vars"
  for f in v-default0 v-default2 v-admits0; do
    cmp -s "$vars" "$tmp/$f.tf" && { echo "SELF-TEST FAILED: mutant $f changed nothing"; bad=1; }
  done
  for f in m-literal0 m-primary0 m-noweight; do
    cmp -s "$tf" "$tmp/$f.tf" && { echo "SELF-TEST FAILED: mutant $f changed nothing"; bad=1; }
  done
  [[ $bad -eq 0 ]] || exit 1
  echo "self-test ok — the guard passes the tree and rejects every mutant"
  exit 0
fi

TF="${1:-infra/providers/huawei/main.tf}"
VARS="${2:-infra/providers/huawei/variables.tf}"
fail=0

for f in "$TF" "$VARS"; do
  [[ -f "$f" ]] || { echo "FAIL: $f not found"; exit 1; }
done

# --- 1 and 2. every member resource carries the one weight expression, never a 0
mapfile -t members < <(grep -nE '^resource "huaweicloud_elb_member" "' "$TF" | cut -d: -f1)
if [[ ${#members[@]} -eq 0 ]]; then
  echo "FAIL: no balancer member resources found in $TF — the guard would pass vacuously"
  exit 1
fi
echo "scanning ${#members[@]} balancer member resource(s) in $TF"

for start in "${members[@]}"; do
  name=$(sed -n "${start}p" "$TF" | sed -E 's/^resource "[^"]+" "([^"]+)".*/\1/')
  # body = from the resource line to the first line that is exactly "}"
  body=$(awk -v s="$start" 'NR>=s{print; if (NR>s && $0=="}") exit}' "$TF")
  wline=$(grep -E '^[[:space:]]*weight[[:space:]]*=' <<<"$body" || true)
  if [[ -z "$wline" ]]; then
    echo "FAIL: member \"$name\" has no weight"
    fail=1
    continue
  fi
  if grep -qE '(=|\?|:)[[:space:]]*0+([^0-9.]|$)' <<<"$wline"; then
    echo "FAIL: member \"$name\" writes a weight of 0 — the provider drops it and the cloud keeps 1: $wline"
    fail=1
    continue
  fi
  if ! grep -qE 'weight *= *[][a-z_.]*\.primary \? local\.gateway_lb_primary_weight : var\.secondary_region_lb_weight$' <<<"$wline"; then
    echo "FAIL: member \"$name\" weight is not '.primary ? local.gateway_lb_primary_weight : var.secondary_region_lb_weight': $wline"
    fail=1
    continue
  fi
  echo "  ok: $name"
done

# --- 3. the primary weight is a positive integer and the secondary default equals it
primary=$(awk '/^[[:space:]]*gateway_lb_primary_weight[[:space:]]*=/{print $3; exit}' "$TF")
default=$(awk '/^variable "secondary_region_lb_weight"/{f=1} f && /default *=/{print $3; exit}' "$VARS")
if ! [[ "$primary" =~ ^[1-9][0-9]*$ ]]; then
  echo "FAIL: local.gateway_lb_primary_weight is '$primary' in $TF, expected a positive integer"
  fail=1
fi
if [[ -z "$default" ]]; then
  echo "FAIL: variable secondary_region_lb_weight not found (or has no default) in $VARS"
  fail=1
elif [[ "$default" != "$primary" ]]; then
  echo "FAIL: secondary_region_lb_weight defaults to '$default', the primary weight is '$primary': every member of both regions carries the same positive weight"
  fail=1
else
  echo "  ok: both regions' members default to weight $primary"
fi

# --- 4. the validation of the variable refuses 0
block=$(awk '/^variable "secondary_region_lb_weight"/{f=1} f{print} f && /^}/{exit}' "$VARS")
cond=$(grep -E '^[[:space:]]*condition[[:space:]]*=' <<<"$block" || true)
if [[ -z "$cond" ]]; then
  echo "FAIL: variable secondary_region_lb_weight has no validation in $VARS"
  fail=1
elif ! grep -qE 'var\.secondary_region_lb_weight >= [1-9]' <<<"$cond" || grep -qE 'var\.secondary_region_lb_weight >= 0([^0-9.]|$)' <<<"$cond"; then
  echo "FAIL: the validation of secondary_region_lb_weight admits 0: $cond"
  fail=1
else
  echo "  ok: the validation of secondary_region_lb_weight refuses 0"
fi

if [[ $fail -ne 0 ]]; then
  echo "FAILED — a balancer member would carry a weight the provider cannot apply, or the regions' weights differ."
  exit 1
fi
echo "PASS — every balancer member of both regions carries the same positive weight."
