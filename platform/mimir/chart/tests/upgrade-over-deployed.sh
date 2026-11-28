#!/usr/bin/env bash
# upgrade-over-deployed.sh - the MinIO root password is generated and read from
# the environment, and the chart applies over the release a Sovereign holds, as
# Helm 3 upgrades it, and over a fresh install (SEC-253).
#
# Until SEC-253 the password was the chart's fixed literal, in Secret
# mimir-minio and in Mimir's configuration. The case TestMimirAppliesOverEveryState
# of tools/chart-upgrade renders the chart of 1.0.14 (the pin of kit slot 23)
# from the history of the repository and the chart under test and judges:
#   - the new render holds the literal nowhere and no Secret; its ExternalSecret
#     writes Secret mimir-minio-password from the value its PushSecret
#     generates; Mimir's three S3 stanzas read ${MINIO_ROOT_PASSWORD}; MinIO and
#     every Mimir component take it from that Secret, with the reload annotation;
#   - for every object, the three-way strategic merge patch Helm builds
#     (k8s.io/apimachinery, as Helm v3.18.4) applies to the live object and
#     gives the new render; the upgrade changes the Pod template of MinIO and of
#     every Mimir component, so all of them restart on the new value together;
#   - the bucket Job's script, run with its projected files and a stand-in mc,
#     logs in as grafana-mimir with the password of the Secret's file.
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
UPGRADE_CHART="$CHART" UPGRADE_PROOF=required go test -count=1 -v -run '^TestMimirAppliesOverEveryState$' ./...
