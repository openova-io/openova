#!/usr/bin/env bash
# check-kyverno-proxy-images.sh — #3380-D admission-test drift-guard.
#
# WHAT
#   Renders the registry-sensitive bootstrap-kit charts in their Sovereign
#   configuration and asserts every container/initContainer image satisfies
#   the live `harbor-proxy-pull` Kyverno ClusterPolicy's allowed-image globs
#   (platform/kyverno-policies/chart/values.yaml → harborProxyPull.
#   allowedImagePatterns). That policy runs `validationFailureAction:
#   Enforce` on every Sovereign, so an image that does NOT match one of its
#   globs is DENIED at admission the moment the pod is (re)scheduled.
#
# WHY (the wound this guards against)
#   Charts that wrap an upstream subchart routinely leak an upstream
#   registry through a field the umbrella forgot to override. The canonical
#   live case (#3380-D): the loft-sh/vcluster subchart defaulted its k8s
#   distro control-plane images to `registry.k8s.io/kube-*`, which sat
#   un-denied only because the ns had no pod churn — the NEXT image roll
#   would have been Enforce-DENIED. `helm lint` + smoke-render never catch
#   this because the rendered YAML is valid; only matching every image
#   against the Enforce policy's own globs catches it. This is the CI half
#   of the kyverno-policies admission-test gate the ticket asks for — it
#   moves the proof from "discovered live on hw130" to "blocked at PR time".
#
# DESIGN
#   Pure `helm template` + glob match — no kind cluster, no live kyverno,
#   so it is fast (<30s) and flake-free. The two allowed globs are read
#   VERBATIM from the policy values so this guard can never drift from the
#   policy it enforces. A small, explicit allowlist covers the genuinely
#   legitimate non-proxy images (the cutover steps 01-04 that run BEFORE
#   the local Harbor proxy exists, and Helm templated `{{...}}` placeholders
#   that are not real refs).
#
# EXIT
#   0  — every image in every rendered chart matches an allowed glob (or the
#        allowlist).
#   1  — at least one image would be Enforce-DENIED by harbor-proxy-pull.
#   2  — tooling error (helm/yq missing, chart render failed).
#
# Usage: bash scripts/check-kyverno-proxy-images.sh

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

if ! command -v helm >/dev/null 2>&1; then
  echo "FATAL: helm not on PATH" >&2
  exit 2
fi

# ── Allowed globs — read verbatim from the policy so the guard can't drift.
POLICY_VALUES="platform/kyverno-policies/chart/values.yaml"
# Extract the harborProxyPull.allowedImagePatterns list entries. The block is
# small + stable; a focused grep beats a yq dependency on minimal runners.
mapfile -t ALLOWED_GLOBS < <(
  awk '
    /^  harborProxyPull:/ { in_block=1 }
    in_block && /^    allowedImagePatterns:/ { in_list=1; next }
    in_list && /^      - / {
      line=$0
      sub(/^      - /, "", line)
      sub(/[[:space:]]*#.*$/, "", line)            # strip trailing comment
      gsub(/"/, "", line)
      gsub(/[[:space:]]/, "", line)
      print line
      next
    }
    in_list && !/^      - / { in_list=0; in_block=0 }
  ' "$POLICY_VALUES"
)

if [ "${#ALLOWED_GLOBS[@]}" -eq 0 ]; then
  echo "FATAL: could not parse harborProxyPull.allowedImagePatterns from $POLICY_VALUES" >&2
  exit 2
fi
echo "[proxy-images] allowed globs (from $POLICY_VALUES): ${ALLOWED_GLOBS[*]}"

# ── Explicit allowlist for legitimate non-proxy images.
#   - The cutover steps 01-04 use upstream-public images BY DESIGN (they run
#     BEFORE the local Harbor proxy + containerd registries.yaml v2 exist).
#     Those step PodSpecs live in ConfigMaps and are stamped post-handover;
#     the harbor-proxy-pull policy excludes the `catalyst` cutover namespace.
#   - `{{...}}` and `${...}` are Helm/envsubst placeholders, not real refs.
#   - mirror.gcr.io/aquasec/* (#3825, #3913): bp-trivy's operator + scanner
#     images use `mirror.gcr.io` (the Google-run anonymous Docker Hub mirror,
#     reachable from kom4dc). The trivy-operator DYNAMICALLY spawns scan Jobs
#     into the `trivy-system` namespace, which harbor-proxy-pull EXCLUDES
#     (harborProxyPull.extraExcludeNamespaces: [trivy-system]) precisely
#     because the operator cannot re-tag its own generated scan images through
#     the Sovereign Harbor. So these are NOT Enforce-denied — legitimately bare.
#     (bp-trivy's node-collector IS proxied — it pulled from ghcr.io, which is
#     unreachable on kom4dc; only the mirror.gcr.io operator/scanner stay bare.)
ALLOWLIST_REGEX='(\{\{)|(\$\{)|(alpine/k8s)|(alpine/git)|(curlimages/curl)|(library/alpine)|(library/busybox)|(mirror\.gcr\.io/aquasec/)'

# ── Charts to render, with the value that flips their top-level gate on.
#   Format: "<chart-path>|<helm --set args>". These are the registry-sensitive
#   subchart-wrapping bootstrap charts most prone to an upstream-registry leak.
#   Extend this list when a new subchart-wrapping bp-* lands.
#   bp-powerdns is INTENTIONALLY NOT in this list as of 2026-06-13: its
#   pdns-auth-50 + dnsdist-19 images pull from raw `docker.io/powerdns/...`
#   because the Harbor `proxy-docker` project is not bootstrap-pullable on a
#   fresh prov (#3380-D's proxy-docker reroute wedged hw131 + hw132 — powerdns
#   pods never started → no DNS → no wildcard TLS → console down). The
#   harbor-proxy-pull policy EXCLUDES the `powerdns` namespace
#   (platform/kyverno-policies/chart/values.yaml
#   harborProxyPull.extraExcludeNamespaces: [powerdns]), so those docker.io
#   images are NOT Enforce-denied at admission — there is nothing for this
#   render-time guard to assert. Re-add powerdns here only if proxy-docker is
#   made bootstrap-pullable and the namespace exclude is dropped.
CHARTS=(
  "platform/bp-mgmt-vcluster/chart|--set mgmtVcluster.enabled=true"
  "platform/bp-dmz-vcluster/chart|--set dmzVcluster.enabled=true"
  "platform/bp-rtz-vcluster/chart|--set rtzVcluster.enabled=true"
  # bp-alloy (slot 21, #3760 hw161): subchart-wrapping (grafana/alloy 1.8.0).
  # The alloy DaemonSet runs in the un-excluded `alloy` namespace, so its
  # upstream image defaults (docker.io/grafana/alloy + quay.io/prometheus-
  # operator/prometheus-config-reloader) are post-handover Enforce-deniable.
  # Pinned through the Harbor proxy in values.yaml; this gate guards regressions.
  "platform/alloy/chart|"
  # #3913 — the three charts that leaked the LATEST round of fresh-prov
  # ImagePullBackOffs (bp-sigstore policy-controller ghcr.io, bp-trivy node-
  # collector ghcr.io, bp-vpa registry.k8s.io). Now proxied; pinned here so a
  # subchart-bump that re-introduces the upstream default fails at PR time.
  "platform/sigstore/chart|"
  "platform/trivy/chart|"
  "platform/vpa/chart|"
)

matches_any_glob() {
  local img="$1"
  local g
  for g in "${ALLOWED_GLOBS[@]}"; do
    # shellcheck disable=SC2053  # intentional glob match (==, not =~)
    case "$img" in
      $g) return 0 ;;
    esac
  done
  return 1
}

rc=0
for entry in "${CHARTS[@]}"; do
  chart="${entry%%|*}"
  setargs="${entry#*|}"
  if [ ! -d "$chart" ]; then
    echo "[proxy-images] WARN: chart dir $chart absent — skipping" >&2
    continue
  fi
  # Build subchart deps if a Chart.yaml dependencies block is present AND the
  # subchart .tgz is not already vendored in charts/. CI checks out a clean
  # tree (empty charts/) so the build runs there; locally the .tgz may
  # already be present (and the upstream repo may not be `helm repo add`-ed),
  # in which case we reuse the vendored copy rather than re-fetching.
  if grep -q '^dependencies:' "$chart/Chart.yaml" 2>/dev/null; then
    if ! ls "$chart"/charts/*.tgz >/dev/null 2>&1; then
      ( cd "$chart" && helm dependency build >/dev/null 2>&1 ) || {
        echo "FATAL: helm dependency build failed for $chart (add the subchart repo via 'helm repo add' or vendor the .tgz under charts/)" >&2
        exit 2
      }
    fi
  fi
  rendered="$( (cd "$chart" && helm template guard . $setargs 2>/dev/null) )" || {
    echo "FATAL: helm template failed for $chart $setargs" >&2
    exit 2
  }
  # Pull every `image:` value (containers + initContainers render the same key).
  while IFS= read -r img; do
    [ -n "$img" ] || continue
    if printf '%s' "$img" | grep -Eq "$ALLOWLIST_REGEX"; then
      continue
    fi
    if ! matches_any_glob "$img"; then
      echo "DENY: $chart renders image '$img' — matches NO harbor-proxy-pull glob (${ALLOWED_GLOBS[*]}). It would be Enforce-DENIED at admission. Route it through the Harbor proxy-cache (e.g. harbor.openova.io/proxy-<upstream>/...)." >&2
      rc=1
    fi
  done < <(
    printf '%s\n' "$rendered" \
      | grep -oE '^[[:space:]]*-?[[:space:]]*image:[[:space:]]*"?[^"]+' \
      | sed -E 's/^[[:space:]]*-?[[:space:]]*image:[[:space:]]*"?//' \
      | sort -u
  )
done

# ════════════════════════════════════════════════════════════════════════
# PASS 2 — raw cluster-manifest scan (#3903 / #3913 coverage gap).
#
# The curated CHARTS list above + the per-chart `helm template` render only
# cover subchart-wrapping bp-* charts. It does NOT see images hard-coded in
# the raw kustomize manifests under clusters/_template/ (bootstrap-kit/,
# sovereign-tls/, infrastructure/, …) — exactly where the cilium-envoy-tls-
# restart Job's bare `alpine/k8s` (#3904) and the slot-01 cilium sysctl
# DaemonSet's bare `busybox` (#3904) leaked. Those are plain YAML, not Helm,
# so no render is needed — a direct `image:` grep is reliable + fast and
# needs zero subchart deps (so this pass can never FATAL on a CI dep-fetch
# flake). This is the half of the #3903 gap the founder called out: the
# kyverno-proxy check did not scan raw clusters/_template/ manifests.
# ════════════════════════════════════════════════════════════════════════
RAW_DIRS=(clusters/_template)

# Exception allowlist for raw manifests — images that are LEGITIMATELY bare
# by design (documented, do NOT route through proxy):
#   - cilium/* : the CNI is installed (slot 01) BEFORE the local Harbor exists;
#     on every provider the node containerd mirror resolves quay.io→Harbor at
#     the mirror layer, so the chart ref stays bare. Not a leak.
#   - the {{ }} / ${ } placeholders + the curated ALLOWLIST_REGEX bases
#     (alpine/k8s, busybox, curl, …) which are already proxied or templated.
# The cutover steps (self-sovereign-cutover ConfigMap PodSpecs) live under
# platform/, not clusters/_template/, and run in the policy-excluded `catalyst`
# ns — they are out of scope for this raw scan.
RAW_ALLOWLIST_REGEX="${ALLOWLIST_REGEX}|(cilium/)|(quay\.io/cilium)"

while IFS= read -r f; do
  [ -n "$f" ] || continue
  while IFS= read -r img; do
    [ -n "$img" ] || continue
    if printf '%s' "$img" | grep -Eq "$RAW_ALLOWLIST_REGEX"; then
      continue
    fi
    if ! matches_any_glob "$img"; then
      echo "DENY: raw manifest ${f} hard-codes image '$img' — matches NO harbor-proxy-pull glob (${ALLOWED_GLOBS[*]}). On a fresh prov this ImagePullBackOffs (kom4dc containerd only mirrors harbor.openova.io + docker.io) and/or is Enforce-DENIED. Route it through the Harbor proxy-cache (e.g. harbor.openova.io/proxy-<upstream>/...)." >&2
      rc=1
    fi
  done < <(
    grep -oE '^[[:space:]]*-?[[:space:]]*image:[[:space:]]*"?[^"]+' "$f" \
      | sed -E 's/^[[:space:]]*-?[[:space:]]*image:[[:space:]]*"?//' \
      | sed -E "s/'//g" \
      | sort -u
  )
done < <(
  grep -rlE '^[[:space:]]*-?[[:space:]]*image:' "${RAW_DIRS[@]}" 2>/dev/null | sort
)

if [ "$rc" -eq 0 ]; then
  echo "[proxy-images] PASS — every rendered chart image AND every raw cluster-manifest image satisfies harbor-proxy-pull."
else
  echo "[proxy-images] FAIL — at least one image would be Enforce-DENIED by harbor-proxy-pull (#3380-D / #3903)." >&2
fi
exit "$rc"
