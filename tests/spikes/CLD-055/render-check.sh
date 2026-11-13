#!/usr/bin/env bash
# CLD-055 spike: prove the render module copies the Huawei fixture exactly.
# With render.tf's own host values, its SHA-256 must equal golden_huawei,
# the value the CI check "golden" (CLD-040) holds for the shared template.
set -euo pipefail
OUT="${OUT:-/tmp/cld055}"; mkdir -p "$OUT"
d="$(cd "$(dirname "$0")" && pwd)"
tofu -chdir="$d/render" init -input=false -no-color >/dev/null
tofu -chdir="$d/render" apply -auto-approve -input=false -no-color -var fixture_hosts=true \
  -state="$OUT/render-check.tfstate" >/dev/null
got=$(tofu -chdir="$d/render" output -state="$OUT/render-check.tfstate" -raw user_data_sha256)
want=$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["golden_huawei"])' \
  "$d/../../../infra/providers/_shared/tests/golden.auto.tfvars.json")
echo "fixture render sha256: $got"
echo "golden_huawei:         $want"
if [ "$got" = "$want" ]; then m=yes; else m=no; fi
echo "CLD055-RESULT render fixture_copy_matches_golden=$m template_sha256=$(sha256sum "$d/../../../infra/providers/_shared/cloudinit-control-plane.tftpl" | cut -c1-16)" | tee -a "$OUT/results.txt"
