#!/usr/bin/env bash
# sovereign-daytwo-bootstrap.sh — #5759: RETIRED TO A REFUSAL.
#
# This script used to re-stamp an ALREADY-CUT-OVER Sovereign's OWN step-01
# (gitea-mirror) + step-03 (harbor-prewarm) Jobs, to deliver
# bp-self-sovereign-cutover's Day-2 catalog leg (#5640) to a Sovereign cut
# over before that leg existed. It no longer stamps anything. It detects
# cutoverComplete and REFUSES, loudly, with the evidence below — a refusal
# beats a silent revert.
#
# WHY IT IS RETIRED, not merely "be careful"
# ───────────────────────────────────────────
# Its step-01 re-stamp forced:
#
#   git push --force <local-gitea> 'refs/heads/*:refs/heads/*' 'refs/tags/*:refs/tags/*'
#
# — a raw ref overwrite of the local Gitea mirror's branch from a FRESH clone
# of upstream, not a merge. Once a Sovereign is cutoverComplete=true, step-06
# (helmrepository-patches) has ALREADY committed a durable "cutover: pivot N
# HelmRepository URLs to local Harbor" commit onto that SAME branch — a
# commit that exists ONLY in this Sovereign's local Gitea and is never
# reachable from a fresh clone of the public upstream repo. The force-push
# above does not mask that commit, it DISCARDS it, and Flux's next reconcile
# re-renders every pivoted HelmRepository back onto ghcr.io.
#
# Measured live on hw292 (dep 1c56518035a83e03, cutoverComplete=true since
# 2026-08-03T08:12:04Z): running this script --apply on 2026-08-06 reverted 62
# of 69 region-a HelmRepositories from the local Harbor back to
# oci://ghcr.io/openova-io. Step-06 had ALREADY stripped the ghcr-pull
# credential from flux-system as part of cutover severance (working exactly
# as designed — mirrorResync.enabled: false is the documented MASTER
# SEVERANCE SWITCH), so Flux was left authenticating to a registry it
# deliberately no longer holds credentials for:
#
#   HelmChart 'flux-system/flux-system-bp-cilium' is not ready: unknown build
#   error: failed to configure login options: no auth config for 'ghcr.io' in
#   the docker-registry Secret 'ghcr-pull'
#
# 44 of 72 HelmReleases went not-Ready. There is NO in-cluster remedy: the
# only fix would be restoring the stripped upstream credential, i.e.
# re-tethering the Sovereign — exactly what cutover exists to prevent. Full
# chain: https://github.com/openova-io/openova/issues/5759
#
# `12-daytwo-harbor-pin-reconciler.yaml`'s CATALOG leg (bp-self-sovereign-
# cutover >= 0.1.170) shares the IDENTICAL force-push against the IDENTICAL
# branch of the IDENTICAL local Gitea repo whenever an operator's request
# names CATALOG post-cutover (or every cycle in mode=warm), so it is refused
# the same way now — see that template's own #5759 comment. There is
# therefore no safer in-band alternative to point an operator toward today.
#
# THE PATH FORWARD. A safe re-mirror needs to PRESERVE the local-only pivot
# commit while still advancing to new upstream content — a merge, not an
# overwrite. That mechanism does not exist yet (tracked in #5759). Until it
# ships, a Sovereign that cut over before bp-self-sovereign-cutover 0.1.170
# (and therefore has no Day-2 catalog leg) stays on its cutover-time chart
# pins; that is not silent breakage, it is the deliberate Principle-14 freeze.
#
# Usage (unchanged, for any runbook/cron already invoking it):
#   scripts/sovereign-daytwo-bootstrap.sh --kubeconfig <path> [--namespace catalyst] [--apply]
#
# It ALWAYS refuses once cutoverComplete is 'true', empty or unreadable
# (fail-closed — a Sovereign old enough to need this script is far more
# likely mid/post-cutover than pre-cutover). On a genuinely pre-cutover
# Sovereign (cutoverComplete=false) it exits 0 immediately: this script was
# never for that case either — the normal delivery chain is already intact.
# There is no override flag. --apply is accepted only so an existing
# invocation does not hard-error on an unrecognised argument; it changes
# nothing below.
set -euo pipefail

KUBECONFIG_PATH=""
NS="catalyst"
APPLY=0

while [ $# -gt 0 ]; do
  case "$1" in
    --kubeconfig) KUBECONFIG_PATH="$2"; shift 2 ;;
    --namespace|-n) NS="$2"; shift 2 ;;
    --apply) APPLY=1; shift ;;
    -h|--help) sed -n '1,64p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
[ -n "${KUBECONFIG_PATH}" ] || { echo "ERROR: --kubeconfig is required" >&2; exit 2; }
[ -r "${KUBECONFIG_PATH}" ] || { echo "ERROR: kubeconfig ${KUBECONFIG_PATH} is not readable" >&2; exit 2; }
export KUBECONFIG="${KUBECONFIG_PATH}"

k() { kubectl --request-timeout=60s "$@"; }

echo "== #5759 / #5640 Day-2 bootstrap (retired to a refusal — see file header) =="
echo "-- cluster: $(k config current-context 2>/dev/null || echo unknown), namespace: ${NS}"
[ "${APPLY}" = "1" ] && echo "-- note: --apply no longer changes this script's behaviour (#5759)"

cc=$(k -n "${NS}" get cm self-sovereign-cutover-status -o jsonpath='{.data.cutoverComplete}' 2>/dev/null || true)
echo "-- cutoverComplete=${cc:-unreadable}"

if [ "${cc}" = "false" ]; then
  cat <<EOF

This Sovereign has not cut over (cutoverComplete=false). This script exists
ONLY to serve an ALREADY-CUT-OVER Sovereign, and that case is now permanently
refused (#5759) — so there is nothing safe for it to do here either. The
normal delivery chain (step-01/03 as part of the ORIGINAL cutover, still
ahead of this Sovereign) already applies. No action taken.
EOF
  exit 0
fi

# #5759 — REFUSE-BY-DEFAULT. cc is 'true', empty or unreadable: all three
# treated the same, fail-closed.
cat >&2 <<EOF

ERROR: refusing to run (#5759).

cutoverComplete='${cc:-unreadable}' — expected the literal 'false' to
proceed, which this script no longer permits doing anything useful with
anyway (see the file header). Re-stamping step-01 here would force-push
upstream openova/openova onto this Sovereign's local Gitea mirror with:

  git push --force <local> 'refs/heads/*:refs/heads/*' 'refs/tags/*:refs/tags/*'

That discards step-06's durable HelmRepository-pivot commit and re-tethers
every chart Flux re-renders to ghcr.io with no credential left to reach it
(step-06 already stripped ghcr-pull auth by design). Measured live on hw292
(dep 1c56518035a83e03) 2026-08-06: this exact --apply reverted 62 of 69
region-a HelmRepositories and left 44 of 72 HelmReleases not-Ready,
unrecoverable in-cluster (the only in-cluster remedy would be restoring the
stripped credential, i.e. re-tethering — exactly what cutover exists to
prevent).

There is no override flag for this refusal and none is coming until a
merge-preserving mirror mechanism ships (tracked in #5759). The Sovereign is
UNCHANGED; nothing else ran.
EOF
exit 1
