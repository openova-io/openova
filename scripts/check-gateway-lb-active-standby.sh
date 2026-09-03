#!/usr/bin/env bash
# check-gateway-lb-active-standby.sh — #6837
#
# The Huawei gateway + console ELBs pool EVERY node of EVERY region. Under the
# provider-default member weight (1) and ROUND_ROBIN, that is an even split
# across regions — active/active. The founder's posture is active / HOT-STANDBY:
# 100% of north-south traffic lands in the primary region by default.
#
# This guard fails if any gateway/console ELB member resource stops weighting
# its secondary-region members, or if the default posture stops being 0.
#
# It does NOT forbid active/active: `secondary_region_lb_weight` is the knob and
# an operator may set it. What it forbids is losing the knob, or silently
# shipping a DEFAULT that splits traffic across regions again.
set -euo pipefail

TF="${1:-infra/providers/huawei/main.tf}"
VARS="${2:-infra/providers/huawei/variables.tf}"
fail=0

for f in "$TF" "$VARS"; do
  [[ -f "$f" ]] || { echo "FAIL: $f not found"; exit 1; }
done

# --- 1. every ELB member resource must carry the weight expression ----------
mapfile -t members < <(grep -nE '^resource "huaweicloud_elb_member" "' "$TF" | cut -d: -f1)
if [[ ${#members[@]} -eq 0 ]]; then
  echo "FAIL: no huaweicloud_elb_member resources found in $TF — guard would pass vacuously"
  exit 1
fi
echo "scanning ${#members[@]} huaweicloud_elb_member resource(s) in $TF"

for start in "${members[@]}"; do
  name=$(sed -n "${start}p" "$TF" | sed -E 's/.*"huaweicloud_elb_member" "([^"]+)".*/\1/')
  # body = from the resource line to the first line that is exactly "}"
  body=$(awk -v s="$start" 'NR>=s{print; if (NR>s && $0=="}") exit}' "$TF")
  if ! grep -q 'weight *=' <<<"$body"; then
    echo "FAIL: huaweicloud_elb_member \"$name\" has no weight — its members default to 1 (even cross-region split)"
    fail=1
    continue
  fi
  if ! grep -q 'secondary_region_lb_weight' <<<"$body"; then
    echo "FAIL: huaweicloud_elb_member \"$name\" sets a weight that does not derive from var.secondary_region_lb_weight"
    fail=1
    continue
  fi
  if ! grep -qE 'weight *=.*\.primary \?' <<<"$body"; then
    echo "FAIL: huaweicloud_elb_member \"$name\" weight is not conditioned on the member's .primary flag"
    fail=1
    continue
  fi
  echo "  ok: $name"
done

# --- 2. the DEFAULT posture must be hot-standby (0) -------------------------
default=$(awk '/^variable "secondary_region_lb_weight"/{f=1} f && /default *=/{print $3; exit}' "$VARS")
if [[ -z "$default" ]]; then
  echo "FAIL: variable secondary_region_lb_weight not found (or has no default) in $VARS"
  fail=1
elif [[ "$default" != "0" ]]; then
  echo "FAIL: secondary_region_lb_weight default is '$default', expected 0 (active/hot-standby)."
  echo "      A non-zero default sends live traffic to a region whose Blueprints may be suspended (#6827)."
  fail=1
else
  echo "  ok: secondary_region_lb_weight defaults to 0 (active/hot-standby)"
fi

if [[ $fail -ne 0 ]]; then
  echo "FAILED — gateway ELB would split north-south traffic across regions by default."
  exit 1
fi
echo "PASS — gateway + console ELB members are active/hot-standby by default."
