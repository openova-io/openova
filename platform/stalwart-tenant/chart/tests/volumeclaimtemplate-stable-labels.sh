#!/usr/bin/env bash
# bp-stalwart-tenant — volumeClaimTemplate label-churn guard.
#
# ROOT CAUSE (live evidence, hw292 funnel Org `uatco`, 2026-08-06):
#
# The StatefulSet's `volumeClaimTemplates[0].metadata.labels` renders via
# the FULL `bp-stalwart-tenant.labels` helper (templates/_helpers.tpl),
# which embeds `helm.sh/chart: bp-stalwart-tenant-<Chart.Version>` and
# `app.kubernetes.io/version: <Chart.AppVersion>`.
#
# `spec.volumeClaimTemplates` is IMMUTABLE post-create on a StatefulSet —
# the Kubernetes API only allows patches to 'replicas', 'ordinals',
# 'template', 'updateStrategy', 'persistentVolumeClaimRetentionPolicy' and
# 'minReadySeconds'. Baking the CHART VERSION into that immutable block
# means EVERY future chart bump (e.g. the unrelated #5615 §854 nodeport
# fix, 0.1.13 -> 0.1.14) changes the label value on the next `helm
# upgrade`, and the apiserver rejects the patch with:
#   "cannot patch ... with kind StatefulSet: ... Forbidden: updates to
#    statefulset spec for fields other than 'replicas', 'ordinals',
#    'template', 'updateStrategy', 'persistentVolumeClaimRetentionPolicy'
#    and 'minReadySeconds' are forbidden"
#
# This is the mechanical reason bp-stalwart-tenant "has NO in-place
# upgrade path" (memory
# reference_stalwart_tenant_no_inplace_upgrade_and_splan_bundle_sizing.md)
# — and it means a per-Org Stalwart release that already has a
# StatefulSet on disk (even one materialised by a FAILED install, as on
# uatco/hw292: HelmRelease uatco-mail-rtz-a stayed pinned at
# bp-stalwart-tenant@0.1.13 with a live StatefulSet pod stuck
# ContainerCreating on the missing stalwart-tls secret) can NEVER
# reconcile forward to a fixed chart version — the upgrade always fails
# on this same immutable-field rejection, independent of whatever the
# chart bump actually fixes.
#
# THE GUARD: render the chart twice — once at its current Chart.yaml
# version, once at a synthetic "next" version/appVersion — and assert
# the StatefulSet's volumeClaimTemplates block is BYTE-IDENTICAL across
# the two renders. If it isn't, ANY future chart version bump is
# guaranteed to break `helm upgrade` for every Org whose StatefulSet
# already exists.
#
# Refs #5615 #960

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"

if ! command -v yq >/dev/null 2>&1; then
  echo "[stalwart-tenant/volumeclaimtemplate-stable-labels] SKIP — yq not installed."
  exit 0
fi

workdir="$(mktemp -d)"
trap 'rm -rf "${workdir}"' EXIT

render_a="${workdir}/render-current.yaml"
render_b="${workdir}/render-bumped.yaml"
bumped_chart="${workdir}/bumped-chart"

# Render A: chart as-is (whatever Chart.Version/AppVersion main currently
# pins). default values — the StatefulSet renders unconditionally
# (stalwart.enabled defaults true; only Certificate/Ingress gate on an
# empty tenant domain), so no values file is required.
helm template smoke "${chart_dir}" --namespace smoke > "${render_a}"

# Render B: a byte-copy of the chart with ONLY Chart.yaml's version +
# appVersion bumped, simulating the next release. rsync/cp -a would also
# copy charts/*.tgz (Chart.lock deps) — copy the whole tree so the
# dependency resolves identically.
cp -a "${chart_dir}" "${bumped_chart}"
sed -i \
  -e 's/^version: .*/version: 9.9.9-guard-probe/' \
  -e 's/^appVersion: .*/appVersion: "9.9.9-guard-probe"/' \
  "${bumped_chart}/Chart.yaml"
helm template smoke "${bumped_chart}" --namespace smoke > "${render_b}"

vct_a="${workdir}/vct-a.yaml"
vct_b="${workdir}/vct-b.yaml"
yq eval 'select(.kind == "StatefulSet") | .spec.volumeClaimTemplates' "${render_a}" > "${vct_a}"
yq eval 'select(.kind == "StatefulSet") | .spec.volumeClaimTemplates' "${render_b}" > "${vct_b}"

if [ ! -s "${vct_a}" ] || [ "$(cat "${vct_a}")" = "null" ]; then
  echo "::error title=Stalwart volumeClaimTemplate guard::StatefulSet or its volumeClaimTemplates did not render — chart shape changed, update this guard."
  exit 1
fi

if ! diff -u "${vct_a}" "${vct_b}" > "${workdir}/diff.txt"; then
  echo "::error title=Stalwart volumeClaimTemplate immutable-label churn::volumeClaimTemplates[].metadata differs across a chart version bump — this WILL break every future 'helm upgrade' with 'Forbidden: updates to statefulset spec ... forbidden' for any Org whose StatefulSet already exists (e.g. hw292 funnel Org uatco, stuck on bp-stalwart-tenant@0.1.13)."
  cat "${workdir}/diff.txt"
  echo "[stalwart-tenant/volumeclaimtemplate-stable-labels] FAIL"
  exit 1
fi

echo "[stalwart-tenant/volumeclaimtemplate-stable-labels] volumeClaimTemplates identical across a chart version bump — StatefulSet upgrades will not hit the immutable-field rejection."
echo "[stalwart-tenant/volumeclaimtemplate-stable-labels] PASS"
