#!/usr/bin/env bash
# api-credentials-kept.sh - SEC-239: the upgrade from bp-powerdns 1.2.33 puts
# helm.sh/resource-policy: keep on the live Secret powerdns-api-credentials and
# keeps its two values; a later release that does not render it leaves it in
# place. Runs the case TestPowerDNSCredentialsKeptOverDeployed of
# tools/chart-upgrade (Helm v3.18.4's three-way patch over the deployed and the
# live Secret). Usage: bash api-credentials-kept.sh [chart_dir]. Exit 0 pass,
# 1 fail. Needs go, helm 3, git with the full history and the dependency's
# chart repository.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHART="$(cd "${1:-$here/..}" && pwd)"
out="$(mktemp)"
trap 'rm -f "$out"' EXIT
cd "$(git -C "$here" rev-parse --show-toplevel)/tools/chart-upgrade"
rc=0
NO_COLOR=1 UPGRADE_CHART="$CHART" UPGRADE_PROOF=required go test -count=1 -v -run '^TestPowerDNSCredentialsKeptOverDeployed$' ./... > "$out" 2>&1 || rc=$?
cat "$out"
[ "$rc" -eq 0 ] || { echo "FAIL: go test exited $rc" >&2; exit 1; }
# A -run pattern that matches no test passes with "no tests to run".
grep -q '^--- PASS: TestPowerDNSCredentialsKeptOverDeployed ' "$out" || { echo "FAIL: the case did not run" >&2; exit 1; }
