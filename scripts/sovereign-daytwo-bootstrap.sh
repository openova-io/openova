#!/usr/bin/env bash
# sovereign-daytwo-bootstrap.sh — the ONE-TIME operator action that gives an
# ALREADY-CUT-OVER Sovereign its first post-cutover delivery (Refs #5640).
#
# WHY THIS EXISTS — the bootstrapping trap, stated plainly
# ───────────────────────────────────────────────────────
# The in-band fix for #5640 is the Day-2 reconciler's CATALOG leg, which ships
# INSIDE `bp-self-sovereign-cutover`. The version of that chart a Sovereign runs
# is decided by the bootstrap-kit pin in its LOCAL GITEA MIRROR. Post-cutover
# that mirror is frozen (`mirrorResync.enabled: false`, the deliberate
# Principle-14 severance). So on a Sovereign cut over BEFORE the catalog leg
# existed, the remedy cannot pass through the gap it remedies: the new chart is
# published, and the Sovereign has no path to learn that it exists.
#
# Measured on hw292 (dep 1c56518035a83e03, cutoverComplete=true, 2026-08-06):
# local Gitea bootstrap-kit slot 06a pinned bp-self-sovereign-cutover 0.1.159
# while upstream main had reached 0.1.169.
#
# There is no way to close that loop from the repo side, and pretending
# otherwise would mean shipping a background auto-pull from ghcr — precisely the
# Principle-11 violation the design refuses. The honest answer is that the FIRST
# delivery is operator-initiated. This script is that one action, and it is the
# LAST one: once the Sovereign is on a chart carrying the catalog leg, every
# subsequent delivery is the in-band, audited, one-shot `CATALOG` request and
# this script is never needed again.
#
# WHAT IT DOES — and what it deliberately does not
# ────────────────────────────────────────────────
# It uses ONLY resources already present on the Sovereign: it re-stamps the
# cutover's OWN step-01 (gitea-mirror) and step-03 (harbor-prewarm) Jobs from
# the `cutover-step-*` PodSpec ConfigMaps the chart installed. It writes no new
# manifests, patches nothing, and introduces no new credential path. Step-01
# re-syncs the local Gitea mirror (moving the bootstrap-kit pins); step-03
# warms the newly-pinned charts + images into the local Harbor. Flux then rolls
# the cutover HelmRelease onto the new chart, which brings the catalog leg with
# it.
#
# It does NOT repoint any Flux source, and it does NOT re-enable the banned
# resync CronJob. After it finishes, the Sovereign still reconciles EXCLUSIVELY
# from its local Gitea + local Harbor.
#
# Step-01's push uses explicit refspecs and never prunes, so the sovereign-local
# refs step-06/07 pushed (HelmRepository pivots, catalyst-api env) survive.
#
# NOTE ON CREDENTIALS. Step-06 strips the cutover's own ghcr auth by design, so
# step-03 may not be able to pull PRIVATE openova-io packages here. That is the
# intended shape — delivery is scoped to a credential the operator owns. If
# step-03 reports auth failures, supply an upstream credential the same way the
# in-band path does (daytwoReconciler.warm.credentialSecret) and re-run.
#
# Usage:
#   scripts/sovereign-daytwo-bootstrap.sh --kubeconfig <path> [--namespace catalyst] [--apply]
#
# DRY-RUN BY DEFAULT. Without --apply it only measures and prints the plan.
set -euo pipefail

KUBECONFIG_PATH=""
NS="catalyst"
APPLY=0
# The first chart version whose Day-2 reconciler carries the CATALOG leg. At or
# above this, the in-band one-shot request replaces this script entirely.
CATALOG_LEG_MIN_VERSION="0.1.170"

while [ $# -gt 0 ]; do
  case "$1" in
    --kubeconfig) KUBECONFIG_PATH="$2"; shift 2 ;;
    --namespace|-n) NS="$2"; shift 2 ;;
    --apply) APPLY=1; shift ;;
    -h|--help) sed -n '1,55p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
[ -n "${KUBECONFIG_PATH}" ] || { echo "ERROR: --kubeconfig is required" >&2; exit 2; }
[ -r "${KUBECONFIG_PATH}" ] || { echo "ERROR: kubeconfig ${KUBECONFIG_PATH} is not readable" >&2; exit 2; }
export KUBECONFIG="${KUBECONFIG_PATH}"

k() { kubectl --request-timeout=60s "$@"; }
die() { echo "ERROR: $*" >&2; exit 1; }

echo "== #5640 Day-2 bootstrap =="
echo "-- cluster: $(k config current-context 2>/dev/null || echo unknown), namespace: ${NS}"

# 1. This is a POST-cutover remedy. On a pre-cutover Sovereign the normal
#    delivery chain is intact and this script is both unnecessary and wrong.
cc=$(k -n "${NS}" get cm self-sovereign-cutover-status -o jsonpath='{.data.cutoverComplete}' 2>/dev/null || true)
[ "${cc}" = "true" ] \
  || die "cutoverComplete is '${cc:-absent}', not 'true'. This Sovereign is not cut over, so its delivery chain is not severed and this bootstrap does not apply."
echo "-- cutoverComplete=true"

# 2. Installed chart version. If the catalog leg is already present, the in-band
#    path exists and using this script instead would be a needless out-of-band
#    action.
ver=$(k -n flux-system get hr bp-self-sovereign-cutover -o jsonpath='{.spec.chart.spec.version}' 2>/dev/null || true)
[ -n "${ver}" ] || die "could not read the bp-self-sovereign-cutover HelmRelease version in flux-system"
echo "-- installed cutover chart: ${ver} (catalog leg lands in ${CATALOG_LEG_MIN_VERSION})"

# catalog leg present iff CATALOG_LEG_MIN_VERSION <= ver, i.e. MIN sorts first.
oldest=$(printf '%s\n%s\n' "${ver}" "${CATALOG_LEG_MIN_VERSION}" | sort -V | head -1)
if [ "${oldest}" = "${CATALOG_LEG_MIN_VERSION}" ]; then
  cat <<EOF

This Sovereign already runs ${ver}, which carries the Day-2 catalog leg.
No out-of-band action is needed — use the in-band, audited, one-shot request:

  kubectl -n ${NS} create configmap self-sovereign-cutover-daytwo-request \\
    --from-literal=requestId=\$(date -u +%Y-%m-%d)-catalog-refresh \\
    --from-literal=requestedBy=<you@example.com> \\
    --from-literal=scope=CATALOG

The reconciler consumes that requestId once, syncs the mirror, delivers the
newly-pinned charts and images in the SAME window, and closes.
EOF
  exit 0
fi

# 3. The step PodSpec ConfigMaps must be present — they are what we re-stamp.
for cm in cutover-step-01-gitea-mirror cutover-step-03-harbor-prewarm; do
  k -n "${NS}" get cm "${cm}" >/dev/null 2>&1 || die "ConfigMap ${NS}/${cm} is absent — cannot re-stamp the step without the chart's own PodSpec"
done
echo "-- step-01 + step-03 PodSpec ConfigMaps present"

mirror_rev() { k -n flux-system get gitrepo openova -o jsonpath='{.status.artifact.revision}' 2>/dev/null || true; }
echo "-- local Gitea mirror revision (before): $(mirror_rev)"

if [ "${APPLY}" != "1" ]; then
  cat <<EOF

DRY RUN — nothing was changed. Re-run with --apply to execute:

  1. stamp Job cutover-daytwo-bootstrap-01 from ${NS}/cutover-step-01-gitea-mirror
     (re-syncs the local Gitea mirror; moves the frozen bootstrap-kit pins)
  2. stamp Job cutover-daytwo-bootstrap-03 from ${NS}/cutover-step-03-harbor-prewarm
     (warms the newly-pinned charts + images into the local Harbor)

Flux then rolls bp-self-sovereign-cutover ${ver} -> the newly-pinned version,
which carries the catalog leg. From that point delivery is in-band and this
script is never needed again.
EOF
  exit 0
fi

stamp() {   # stamp <job-name> <podspec-cm> <deadline-seconds>
  local job="$1" cm="$2" deadline="$3" spec
  spec=$(k -n "${NS}" get cm "${cm}" -o jsonpath='{.data.podSpec}')
  [ -n "${spec}" ] || die "${cm} carries no podSpec"
  k -n "${NS}" delete job "${job}" --ignore-not-found >/dev/null 2>&1 || true
  {
    echo "apiVersion: batch/v1"
    echo "kind: Job"
    echo "metadata:"
    echo "  name: ${job}"
    echo "  labels:"
    echo "    app.kubernetes.io/managed-by: sovereign-daytwo-bootstrap"
    echo "    catalyst.openova.io/issue: \"5640\""
    echo "spec:"
    echo "  backoffLimit: 1"
    echo "  template:"
    echo "    spec:"
    printf '%s\n' "${spec}" | sed 's/^/      /'
  } | k -n "${NS}" apply -f - >/dev/null
  echo "-- stamped Job ${NS}/${job}; waiting up to ${deadline}s"
  if ! k -n "${NS}" wait --for=condition=complete "job/${job}" --timeout="${deadline}s" 2>/dev/null; then
    echo "!! ${job} did not report complete. Last 40 log lines:" >&2
    k -n "${NS}" logs "job/${job}" --tail=40 2>&1 | sed 's/^/   /' >&2
    die "${job} failed — the Sovereign is UNCHANGED and still running on its current pins"
  fi
  echo "-- ${job} complete"
}

stamp cutover-daytwo-bootstrap-01 cutover-step-01-gitea-mirror 1200
echo "-- local Gitea mirror revision (after): $(mirror_rev)"
stamp cutover-daytwo-bootstrap-03 cutover-step-03-harbor-prewarm 5400

cat <<EOF

Done. Flux will now move bp-self-sovereign-cutover off ${ver} onto the newly
mirrored pin. Watch it with:

  kubectl -n flux-system get hr bp-self-sovereign-cutover -w

Once it reports the new version, the Day-2 reconciler's catalog leg is present
and every future delivery is the in-band one-shot request (scope=CATALOG).
EOF
