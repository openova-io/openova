#!/usr/bin/env bash
# upgrade-over-deployed.sh - the chart applies over the release a Sovereign
# holds, as Helm 3 upgrades it, and over a fresh install (NET-187).
#
# On 27 Nov 2026 the Sovereign ran bp-powerdns 1.2.33: dnsdist sent every
# query to port 5353 of the powerdns Service, which declares only 53 and 8081,
# so no query reached PowerDNS. The case TestPowerDNSAppliesOverEveryState of
# tools/chart-upgrade renders the chart of 1.2.33 and the chart under test, each
# with the block dnsdist of kit slot 11 at its commit (the host-port DaemonSet),
# builds for every object the three-way strategic merge patch Helm builds
# (k8s.io/apimachinery, as Helm v3.18.4), applies it to the live object and
# judges the result: every object becomes the new render, dnsdist's one backend
# is port 53, the Pods of the DaemonSet dnsdist roll, and no Secret gets a new
# value.
#
# Usage:
#   bash upgrade-over-deployed.sh           the chart above this file
#   bash upgrade-over-deployed.sh <chart>   that chart directory
#
# Exit code: 0 pass, 1 fail. Needs go, helm 3, git with the full history (a
# shallow checkout fails it) and the chart repository of the dependency.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHART="$(cd "${1:-$here/..}" && pwd)"
root="$(git -C "$here" rev-parse --show-toplevel)"
cd "$root/tools/chart-upgrade"
UPGRADE_CHART="$CHART" UPGRADE_PROOF=required go test -count=1 -v -run '^TestPowerDNSAppliesOverEveryState$' ./...
