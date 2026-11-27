#!/usr/bin/env bash
# upgrade-over-deployed.sh - the chart applies over the releases a Sovereign
# holds, as Helm 3 upgrades it, and over a fresh install (SEC-163).
#
# On 27 Nov 2026 the Sovereign ran bp-openbao 1.2.93: the finished Job
# openbao-pki-bootstrap-<hash> stayed in namespace openbao (no time to live,
# no hook, managed by Helm), and the restore drill's scratch store could not
# write its snapshot to /tmp. The case TestOpenBaoAppliesOverEveryState of
# tools/chart-upgrade renders the charts of 1.2.93 and 1.2.94 from the history
# of the repository and the chart under test, builds for every object the
# three-way strategic merge patch Helm builds (k8s.io/apimachinery, as Helm
# v3.18.4), applies it to the live object (the PKI Job as the API server and
# the Job controller keep it after it succeeded) and judges the result: the
# Job keeps its template and gets ttlSecondsAfterFinished 0, the CronJob
# openbao-restore-drill becomes the new render, with a writable /tmp.
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
UPGRADE_CHART="$CHART" UPGRADE_PROOF=required go test -count=1 -v -run '^TestOpenBaoAppliesOverEveryState$' ./...
