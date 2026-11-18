#!/bin/sh
# follower-registry-pivot.sh - cutover step 04 (registry-pivot) in every follower region (#7938).
#
# Step 04 is the registry-pivot DaemonSet: on each node it points containerd's
# certs.d at the Sovereign's own registry. The cutover chart installs it where
# the chart runs, the primary region. A follower region runs no cutover release
# (its HelmRelease is suspended there), so before this script its nodes kept the
# mirror their first boot wrote. Measured on hw307 (18 Nov 2026): region b sent
# ghcr.io pulls to the pre-cutover mirror, which did not hold the Sovereign-object
# writer's image, so region b had no Sovereign object (gate checks NET-G08 and
# SOV-G17).
#
# Step 03 runs this script last, after its run guard has certified that the
# registry holds every image every region runs. It runs in the cutover's step 03
# Job and in every release delivery, whose prewarm is the candidate's step 03, so
# an installed Sovereign gets it from the delivery that carries it. For each
# follower kubeconfig (SECONDARY_KUBECONFIGS, the peer account of that region):
#   1. ConfigMap registry-pivot-script: the primary's pivot.sh, byte for byte;
#   2. objects.yaml of the render: the DaemonSet the primary runs, under its own
#      ServiceAccount, its role bound in the namespaces pivot.sh calls, and its
#      status ConfigMap, which asks for v2 (the local registry);
#   3. a wait, up to FOLLOWER_PIVOT_WAIT_SECONDS, until every Ready node of the
#      region has acked v2 in that ConfigMap (pivot.sh writes
#      node.<name>.registriesYaml=v2 once the node's certs.d points at the
#      registry and the registry answers). A node that is not Ready is named in a
#      WARNING; the DaemonSet pivots it when it returns.
# A follower's registry is the primary's: one database, one bucket. A region
# that refuses the objects, or whose nodes do not ack in time, fails the step by
# name. With no follower kubeconfig it does nothing.
set -eu
: "${RELEASE_NAMESPACE:?}" "${FOLLOWER_PIVOT_DIR:=/follower-registry-pivot}" "${PIVOT_SCRIPT:=/registry-pivot-script/pivot.sh}"
: "${FOLLOWER_PIVOT_WAIT_SECONDS:=900}" "${FOLLOWER_PIVOT_POLL_SECONDS:=10}" "${FOLLOWER_PIVOT_WORK:=/tmp/follower-registry-pivot}"
: "${FOLLOWER_PIVOT_STATUS_CM:=cutover-registry-pivot-follower-status}"
kcs="${SECONDARY_KUBECONFIGS:-/secondary-kubeconfigs}"
objs="${FOLLOWER_PIVOT_DIR}/objects.yaml"
say() { echo "[registry-pivot-follower] $*"; }
mkdir -p "${FOLLOWER_PIVOT_WORK}"
set -- "${kcs}"/*.yaml
if [ ! -e "$1" ]; then say "no follower kubeconfig in ${kcs}: nothing to pivot outside the primary"; exit 0; fi
[ -s "${objs}" ] || { say "FATAL: ${objs} is empty: the render carries no follower objects of step 04"; exit 1; }
[ -s "${PIVOT_SCRIPT}" ] || { say "FATAL: ${PIVOT_SCRIPT} is empty: no pivot.sh to run in a follower"; exit 1; }
jq -n --rawfile s "${PIVOT_SCRIPT}" --arg ns "${RELEASE_NAMESPACE}" \
  '{apiVersion: "v1", kind: "ConfigMap", metadata: {name: "registry-pivot-script", namespace: $ns}, data: {"pivot.sh": $s}}' \
  >"${FOLLOWER_PIVOT_WORK}/script-cm.json"
rc=0
for kc in "$@"; do
  region=$(basename "${kc}" .yaml)
  out="${FOLLOWER_PIVOT_WORK}/${region}"
  say "region ${region}: step 04 - every node's containerd to the Sovereign's registry"
  if ! kubectl --kubeconfig "${kc}" apply --server-side --force-conflicts --field-manager=self-sovereign-cutover -f "${FOLLOWER_PIVOT_WORK}/script-cm.json" >"${out}.apply" 2>&1 \
    || ! kubectl --kubeconfig "${kc}" apply --server-side --force-conflicts --field-manager=self-sovereign-cutover -f "${objs}" >>"${out}.apply" 2>&1; then
    say "FATAL: region ${region}: the API server refused step 04's objects: $(grep -v '^[[:space:]]*$' "${out}.apply" | tail -n 2 | tr '\n' ' ' | cut -c1-600)"
    rc=1; continue
  fi
  deadline=$(( $(date +%s) + FOLLOWER_PIVOT_WAIT_SECONDS ))
  while :; do
    kubectl --kubeconfig "${kc}" get nodes -o json >"${out}.nodes" 2>/dev/null || echo '{}' >"${out}.nodes"
    kubectl --kubeconfig "${kc}" -n "${RELEASE_NAMESPACE}" get configmap "${FOLLOWER_PIVOT_STATUS_CM}" -o json >"${out}.status" 2>/dev/null || echo '{}' >"${out}.status"
    ready=$(jq -r '.items[]? | select(any(.status.conditions[]?; .type == "Ready" and .status == "True")) | .metadata.name' "${out}.nodes")
    missing=$(jq -r --arg n "${ready}" '(.data // {}) as $d | $n | split("\n")[] | select(. != "" and $d["node." + . + ".registriesYaml"] != "v2")' "${out}.status")
    [ -z "${ready}" ] || [ -n "${missing}" ] || break
    [ "$(date +%s)" -lt "${deadline}" ] || break
    sleep "${FOLLOWER_PIVOT_POLL_SECONDS}"
  done
  if [ -z "${ready}" ] || [ -n "${missing}" ]; then
    say "FATAL: region ${region}: after ${FOLLOWER_PIVOT_WAIT_SECONDS}s these Ready nodes have not acked v2 in ${RELEASE_NAMESPACE}/${FOLLOWER_PIVOT_STATUS_CM}: $(printf '%s' "${missing:-<no Ready node listed>}" | tr '\n' ' ')"
    rc=1; continue
  fi
  notready=$(jq -r '.items[]? | select(any(.status.conditions[]?; .type == "Ready" and .status == "True") | not) | .metadata.name' "${out}.nodes" | tr '\n' ' ')
  [ -z "${notready}" ] || say "WARNING: region ${region}: node(s) not Ready, pivoted by the DaemonSet when they return: ${notready}"
  say "region ${region}: every Ready node acks v2 ($(printf '%s\n' "${ready}" | grep -c .) node(s))"
done
exit "${rc}"
