#!/usr/bin/env bash
# bp-openbao: every workload the chart renders with the values of its kit slot
# passes the policy probes-present.
#
# probes-present (platform/kyverno-policies, templates/baseline/04-probes.yaml,
# Enforce once the bootstrap window closes) refuses every create or change of
# a Deployment, StatefulSet or DaemonSet unless each of its containers declares
# a non-empty livenessProbe and readinessProbe. Init and ephemeral containers
# are not judged, nor are the namespaces of its excludeNamespaces. Kit slot 08
# switches the upstream CSI provider on (SEC-211). Its agent sidecar declared
# no probe, so on 25 November every upgrade of bp-openbao 1.2.88 -> 1.2.89 was
# refused ("Every container in DaemonSet/openbao-csi-provider must declare both
# livenessProbe and readinessProbe"), rolled back four times and stalled the
# five releases that wait on it. tests/kyverno-probes-present.sh renders the
# chart defaults, which have no CSI provider, so it did not see this.
#
# The chart is rendered as a Sovereign renders it: slot 08 with the variables
# of tests/fixtures/variables/today.env substituted as Flux's post-build step
# does, then this chart with the values, release name and namespace of the
# slot's HelmRelease; and again with two-region-secondary.env laid over
# today.env. The two valuesFrom inputs (seal stanza, backup bucket) are read on
# the cluster and are left out; neither adds a container.
#
# Cases, on each value set:
#   1. the scope this test copies is the policy's: the template still matches
#      deployments, statefulsets and daemonsets, judges containers only and
#      excludes the namespaces of compliancePolicies.probesPresent, which are
#      read from the policy chart's values.
#   2. every container of every Deployment, StatefulSet and DaemonSet outside
#      those namespaces declares a non-empty livenessProbe and readinessProbe,
#      and the CSI provider DaemonSet is among the judged workloads.
#   3. both probes of the provider container are httpGet /health/ready on port
#      8080, the provider's own health server (its --health-addr default), and
#      no argument moves that server.
#   4. the check can fail: with the agent sidecar on, as main rendered it
#      (openbao.csi.agent.enabled=true), it names exactly
#      DaemonSet/openbao-csi-provider and its container openbao-agent.
#
# CI runs every chart/tests/*.sh as `bash <script> <chart dir>` under the yq of
# the release (v4.44.3). Only plain selections and index access are used, and
# every query of one workload reads a file that holds that one document.
set -euo pipefail

chart_dir="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
repo="$(cd "$chart_dir/../../.." && pwd)"
slot="$repo/clusters/_template/bootstrap-kit/08-openbao.yaml"
policy="$repo/platform/kyverno-policies/chart/templates/baseline/04-probes.yaml"
policy_values="$repo/platform/kyverno-policies/chart/values.yaml"
vars="$repo/tests/fixtures/variables"
helm="${HELM_BIN:-helm}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fail() { echo "FAIL: $1" >&2; exit 1; }

if ! ls "$chart_dir"/charts/*.tgz >/dev/null 2>&1; then
  "$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true
fi

# Case 1
echo "[bp-openbao] case 1: the scope of probes-present"
grep -qF 'resources: ["deployments", "statefulsets", "daemonsets"]' "$policy" \
  || fail "case 1: probes-present no longer matches exactly deployments, statefulsets and daemonsets; copy its new scope here"
grep -qF 'object.spec.template.spec.containers.all(c,' "$policy" \
  || fail "case 1: probes-present no longer judges spec.template.spec.containers only; copy its new scope here"
grep -qF 'excludeNamespacesExpr" .Values.compliancePolicies.probesPresent.excludeNamespaces' "$policy" \
  || fail "case 1: probes-present no longer excludes compliancePolicies.probesPresent.excludeNamespaces; copy its new exclusions here"
excluded="$(yq 'explode(.) | .compliancePolicies.probesPresent.excludeNamespaces[]' "$policy_values")"
[ -n "$excluded" ] || fail "case 1: no excluded namespace read from $policy_values"
echo "  ok: Deployment, StatefulSet, DaemonSet; containers only; $(grep -c . <<<"$excluded") namespaces excluded"

# flux_subst (as platform/kured/chart/tests/render.sh): stdin with the variables
# of the environment substituted as Flux's post-build step does for ${NAME},
# ${NAME:=default} and ${NAME:-default}: the innermost first, until none is
# left; a NAME that is unset or empty takes the default, and without one the
# empty text.
flux_subst() {
  local s name value re='[$][{]([A-Za-z_][A-Za-z0-9_]*)(:[-=]([^${}]*))?[}]'
  s="$(cat)"
  while [[ $s =~ $re ]]; do
    name="${BASH_REMATCH[1]}"
    value="${!name-}"
    [ -n "$value" ] || value="${BASH_REMATCH[3]}"
    s="${s/"${BASH_REMATCH[0]}"/"$value"}"
  done
  printf '%s\n' "$s"
}

# slot_values <dir> <variables file>...: <dir>/values.yaml, release and
# namespace of the slot's HelmRelease, substituted with the files' KEY=VALUE
# lines (the value is the rest of the line; a later file wins).
slot_values() {
  local dir="$1"
  shift
  mkdir -p "$dir"
  (
    for f in "$@"; do
      while IFS= read -r line; do
        case "$line" in '' | '#'*) continue ;; esac
        export "${line%%=*}=${line#*=}"
      done < "$f"
    done
    flux_subst < "$slot" > "$dir/slot.yaml"
  ) || fail "the variables of $* do not substitute into slot 08"
  yq ea -N 'select(.kind == "HelmRelease" and .metadata.name == "bp-openbao") | .spec.values' "$dir/slot.yaml" > "$dir/values.yaml"
  yq ea -N 'select(.kind == "HelmRelease" and .metadata.name == "bp-openbao") | .spec.releaseName' "$dir/slot.yaml" > "$dir/release"
  yq ea -N 'select(.kind == "HelmRelease" and .metadata.name == "bp-openbao") | .spec.targetNamespace' "$dir/slot.yaml" > "$dir/namespace"
  [ "$(cat "$dir/release")" = openbao ] && [ "$(cat "$dir/namespace")" = openbao ] \
    || fail "slot 08 no longer installs release openbao into namespace openbao"
}

# judge <render> <dir>: writes <dir>/checked (Kind/namespace/name of each
# judged workload) and <dir>/offenders ("Kind/name container" for each
# container the policy refuses).
judge() {
  local render="$1" dir="$2" kind ns name n=0
  : > "$dir/checked"
  : > "$dir/offenders"
  [ "$(yq ea '[select(.kind == "Deployment" or .kind == "StatefulSet" or .kind == "DaemonSet") | select(.metadata.namespace == null)] | length' "$render")" = 0 ] \
    || fail "a workload of the render names no namespace; this test cannot judge it against the exclusions"
  while read -r kind ns name; do
    [ -n "$kind" ] || continue
    if grep -qxF "$ns" <<<"$excluded"; then
      echo "  skipped: $kind/$ns/$name (namespace excluded by the policy)"
      continue
    fi
    n=$((n + 1))
    K="$kind" NS="$ns" N="$name" yq ea 'select(.kind == strenv(K) and .metadata.namespace == strenv(NS) and .metadata.name == strenv(N))' "$render" > "$dir/w$n.yaml"
    [ "$(yq '.spec.template.spec.containers | length' "$dir/w$n.yaml")" -gt 0 ] || fail "$kind/$ns/$name renders no container"
    echo "$kind/$ns/$name" >> "$dir/checked"
    # A probe counts when it is a non-empty object, as in the policy. (Under
    # yq v4.44.3 `(.livenessProbe | length) == 0` is false for an absent key,
    # so the predicate is the negation of "both are non-empty".)
    yq '.spec.template.spec.containers[] | select(((.livenessProbe | length) > 0 and (.readinessProbe | length) > 0) | not) | .name' "$dir/w$n.yaml" \
      | sed "s#^#$kind/$name #" >> "$dir/offenders"
  done < <(yq ea -N '[select(.kind == "Deployment" or .kind == "StatefulSet" or .kind == "DaemonSet") | .kind + " " + .metadata.namespace + " " + .metadata.name] | .[]' "$render")
}

check() {
  local label="$1" d="$tmp/$1" probe path port moved
  shift
  echo "[bp-openbao] kit slot 08, $label values"
  slot_values "$d" "$@"
  "$helm" template "$(cat "$d/release")" "$chart_dir" --namespace "$(cat "$d/namespace")" -f "$d/values.yaml" > "$d/render.yaml" \
    || fail "$label: the chart does not render with the values of slot 08"

  # Case 2
  judge "$d/render.yaml" "$d"
  [ ! -s "$d/offenders" ] \
    || fail "$label: probes-present refuses these containers (no livenessProbe or no readinessProbe): $(tr '\n' ';' < "$d/offenders")"
  grep -qxF "DaemonSet/openbao/openbao-csi-provider" "$d/checked" \
    || fail "$label: the CSI provider DaemonSet was not judged (slot 08 no longer switches it on, or its namespace is excluded)"
  echo "  ok: every container of $(grep -c . "$d/checked") workloads declares both probes: $(tr '\n' ' ' < "$d/checked")"

  # Case 3
  yq ea 'select(.kind == "DaemonSet" and .metadata.name == "openbao-csi-provider")' "$d/render.yaml" > "$d/provider.yaml"
  for probe in livenessProbe readinessProbe; do
    path="$(yq ".spec.template.spec.containers[] | select(.name == \"openbao-csi-provider\") | .$probe.httpGet.path" "$d/provider.yaml")"
    port="$(yq ".spec.template.spec.containers[] | select(.name == \"openbao-csi-provider\") | .$probe.httpGet.port" "$d/provider.yaml")"
    [ "$path" = /health/ready ] && [ "$port" = 8080 ] \
      || fail "$label: the $probe of openbao-csi-provider is not httpGet /health/ready on 8080 (path '$path', port '$port')"
  done
  moved="$(yq '.spec.template.spec.containers[] | select(.name == "openbao-csi-provider") | .args[]' "$d/provider.yaml" \
    | grep -E '^--?health-addr=' | grep -vxE -- '--?health-addr=:8080' || true)"
  [ -z "$moved" ] || fail "$label: an argument moves the provider's health server ($moved); its probes dial 8080"
  echo "  ok: both probes of openbao-csi-provider are httpGet /health/ready on 8080"

  # Case 4
  "$helm" template "$(cat "$d/release")" "$chart_dir" --namespace "$(cat "$d/namespace")" -f "$d/values.yaml" \
    --set openbao.csi.agent.enabled=true > "$d/agent-render.yaml" || fail "$label: the render with the agent sidecar errored"
  mkdir -p "$d/agent"
  judge "$d/agent-render.yaml" "$d/agent"
  [ "$(cat "$d/agent/offenders")" = "DaemonSet/openbao-csi-provider openbao-agent" ] \
    || fail "$label: with the agent sidecar on the check names [$(tr '\n' ';' < "$d/agent/offenders")], not DaemonSet/openbao-csi-provider openbao-agent (it cannot fail)"
  echo "  ok: with the agent sidecar on, the check names DaemonSet/openbao-csi-provider openbao-agent"
}

check today "$vars/today.env"
check two-region-secondary "$vars/today.env" "$vars/two-region-secondary.env"

echo "[bp-openbao] kit probes-present: PASS"
