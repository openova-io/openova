#!/usr/bin/env bash
# upgrade-over-failed.sh - the chart applies over the state a failed upgrade
# left, as Helm 3 upgrades it, and over a fresh install.
#
# On 27 Nov 2026 bp-huawei-evs-csi 1.0.13 could not be applied in either region
# of the Sovereign: 1.0.12 had failed after it patched the DaemonSet, so Helm
# diffed 1.0.13 against 1.0.11 (the last deployed release, no registrar probe)
# while the live registrar carried 1.0.12's httpGet probes, and the patch kept
# httpGet beside the new exec. The case TestHuaweiEVSCSIAppliesOverEveryState
# of tools/chart-upgrade renders the charts of 1.0.11, 1.0.12 and 1.0.13 from
# the history of the repository and the chart under test, builds for every
# object the three-way strategic merge patch Helm builds (k8s.io/apimachinery,
# as Helm v3.18.4), applies it to the live object and judges the result as the
# API server does. It also checks that it reproduces the refusal the Sovereign
# reported, word for word.
#
# Usage:
#   bash upgrade-over-failed.sh           the chart above this file
#   bash upgrade-over-failed.sh <chart>   that chart directory
#
# Exit code: 0 pass, 1 fail. Needs go, helm 3 and git with the full history
# (a shallow checkout fails it).
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHART="$(cd "${1:-$here/..}" && pwd)"
root="$(git -C "$here" rev-parse --show-toplevel)"
cd "$root/tools/chart-upgrade"
UPGRADE_CHART="$CHART" UPGRADE_PROOF=required go test -count=1 -v -run '^TestHuaweiEVSCSIAppliesOverEveryState$' ./...
