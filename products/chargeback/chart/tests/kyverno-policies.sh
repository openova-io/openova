#!/usr/bin/env bash
# bp-chargeback — the Sovereign's Kyverno policy set, applied to the rendered
# chart (EPIC #6867).
#
# WHAT
#   Renders this chart the way the bootstrap-kit slot 13f and a per-Org install
#   do (platformApi.url unset AND set; adapter.billingHook.callbackSecret set),
#   renders EVERY ClusterPolicy of platform/kyverno-policies/chart at its
#   canonical action (compliancePolicies.bootstrapMode=false), and runs
#   `kyverno apply <policies> --resource <render>` with the kyverno CLI. Any
#   `fail` or `error` result fails this gate.
#
# WHY
#   hw307 2026-09-11, namespace chargeback, PolicyReports 30 pass / 7 fail:
#   otel-injected, prometheus-scrape, topology-spread and secret-not-in-env on
#   Deployment/chargeback, resource-requests on the CNPG instance Pod, and
#   secret-not-in-env on both hook Jobs. Every one of them was decidable from
#   the rendered YAML alone — nothing here needs a cluster — so the proof moves
#   from "discovered live" to "blocked at PR time" (the check-kyverno-proxy-
#   images.sh shape, but with the real policy engine instead of a re-hand-
#   rolled predicate).
#
# HOW THE RENDER IS MADE ADMISSION-FAITHFUL
#   Flux's helm-controller post-renders every object it applies with the labels
#   helm.toolkit.fluxcd.io/name + helm.toolkit.fluxcd.io/namespace, which is
#   what the flux-managed (Enforce) policy keys on. `helm template` does not
#   add them, so this script stamps exactly those two labels (and the release
#   namespace) before applying the policies — the same object the apiserver
#   sees on a Sovereign. Nothing else is altered.
#
# WHAT IT CANNOT SEE
#   The CNPG instance Pod is created by the CNPG operator, not by the chart;
#   the policy that failed on it (resource-requests/requests-pod) reads the
#   Cluster spec's `resources` block, which tests/render-contract.sh asserts.
#
# KYVERNO CLI
#   Pinned below to the Sovereign's Kyverno appVersion (platform/kyverno/chart/
#   Chart.yaml) with the release's published sha256 (checksums.txt on the
#   GitHub release). A `kyverno` on PATH reporting exactly that version is
#   used; otherwise the pinned release is downloaded once into a cache dir.
#   KYVERNO_BIN=<path> overrides both (local runs).
#
# VACUITY GUARDS (each one fails the gate)
#   - the policy set must contain the five policies named above;
#   - the CLI must report > 0 policy rules applied to >= 10 resources (a
#     multi-doc file that also carries ClusterRoles loads ZERO policies on
#     kyverno 1.13-1.18 — silently; this guard is what caught it);
#   - every rendered object must carry the stamped Flux labels.
#
# Usage: tests/kyverno-policies.sh [chart_dir]   (CI passes chart_dir)

set -euo pipefail

KYVERNO_CLI_VERSION="v1.18.0"   # = platform/kyverno/chart/Chart.yaml appVersion (Sovereign Kyverno)
declare -A KYVERNO_CLI_SHA256=(
  ["x86_64"]="3aa7b7aa68732fd6bc5732f1030d0ed12e1b0ffe7dbac5f5aa21fd8695718904"
  ["arm64"]="37697771e1cc92daf73bebde4eb304691af09e07a4278cc82062e829c8475cec"
)
REQUIRED_POLICIES=(prometheus-scrape otel-injected topology-spread secret-not-in-env resource-requests flux-managed)

chart_dir="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
chart_dir="$(cd "$chart_dir" && pwd)"
repo_root="$(cd "$chart_dir/../../.." && pwd)"
policies_chart="$repo_root/platform/kyverno-policies/chart"
kyverno_chart="$repo_root/platform/kyverno/chart/Chart.yaml"
helm="${HELM_BIN:-helm}"
FQDN=hw305.omani.works

fail() { echo "FAIL: $*" >&2; exit 1; }
for tool in "$helm" yq awk sha256sum tar curl; do
  command -v "$tool" >/dev/null 2>&1 || fail "$tool not on PATH"
done
[ -d "$policies_chart/templates/baseline" ] || fail "policy source missing at $policies_chart/templates/baseline"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# ── kyverno CLI ─────────────────────────────────────────────────────────────
sovereign_kyverno="$(awk '/^appVersion:/{gsub(/"/,"",$2); print $2}' "$kyverno_chart")"
if [ "$sovereign_kyverno" != "$KYVERNO_CLI_VERSION" ]; then
  echo "WARN: kyverno CLI pin $KYVERNO_CLI_VERSION differs from the Sovereign's Kyverno $sovereign_kyverno ($kyverno_chart) — bump KYVERNO_CLI_VERSION + KYVERNO_CLI_SHA256 in $0" >&2
fi

resolve_kyverno() {
  if [ -n "${KYVERNO_BIN:-}" ]; then
    [ -x "$KYVERNO_BIN" ] || fail "KYVERNO_BIN=$KYVERNO_BIN is not executable"
    echo "$KYVERNO_BIN"; return
  fi
  if command -v kyverno >/dev/null 2>&1 && kyverno version 2>/dev/null | grep -q "Version: ${KYVERNO_CLI_VERSION#v}$"; then
    command -v kyverno; return
  fi
  local arch
  case "$(uname -m)" in
    x86_64|amd64) arch=x86_64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) fail "no kyverno CLI checksum pinned for $(uname -m); set KYVERNO_BIN" ;;
  esac
  local cache="${KYVERNO_CLI_CACHE:-${XDG_CACHE_HOME:-${HOME:-/tmp}/.cache}/openova/kyverno-cli/$KYVERNO_CLI_VERSION}"
  if [ ! -x "$cache/kyverno" ]; then
    mkdir -p "$cache"
    local tgz="$cache/kyverno-cli.tgz"
    local url="https://github.com/kyverno/kyverno/releases/download/${KYVERNO_CLI_VERSION}/kyverno-cli_${KYVERNO_CLI_VERSION}_linux_${arch}.tar.gz"
    echo "kyverno CLI $KYVERNO_CLI_VERSION not on PATH — fetching $url" >&2
    curl -sSfL --retry 3 --retry-all-errors -o "$tgz" "$url" || fail "download failed: $url"
    echo "${KYVERNO_CLI_SHA256[$arch]}  $tgz" | sha256sum -c - >/dev/null 2>&1 || fail "sha256 mismatch for $url (expected ${KYVERNO_CLI_SHA256[$arch]})"
    tar -xzf "$tgz" -C "$cache" kyverno || fail "could not extract kyverno from $tgz"
    rm -f "$tgz"
  fi
  echo "$cache/kyverno"
}
kyverno="$(resolve_kyverno)"
echo "kyverno CLI: $kyverno ($("$kyverno" version 2>/dev/null | awk '/^Version/{print $2}'))"

# ── policy set: every ClusterPolicy at its canonical action ─────────────────
"$helm" template kp "$policies_chart" --set compliancePolicies.bootstrapMode=false > "$work/policies-all.yaml" 2>"$work/policies.err" \
  || fail "rendering $policies_chart failed: $(head -3 "$work/policies.err")"
# ClusterPolicy documents ONLY — the CLI loads zero policies from a file that
# also carries the chart's ClusterRoles (see VACUITY GUARDS).
yq 'select(.kind == "ClusterPolicy")' "$work/policies-all.yaml" > "$work/policies.yaml"
policy_count="$(grep -c '^kind: ClusterPolicy$' "$work/policies.yaml" || true)"
[ "$policy_count" -ge 20 ] || fail "only $policy_count ClusterPolicies rendered from $policies_chart — expected the full baseline set"
for p in "${REQUIRED_POLICIES[@]}"; do
  yq -e "select(.kind == \"ClusterPolicy\" and .metadata.name == \"$p\") | .metadata.name" "$work/policies.yaml" >/dev/null 2>&1 \
    || fail "policy $p is missing from the rendered set — the gate would pass on nothing"
done
echo "policy set: $policy_count ClusterPolicies (canonical actions), incl. ${REQUIRED_POLICIES[*]}"

# ── chart renders ───────────────────────────────────────────────────────────
render() {
  "$helm" template chargeback "$chart_dir" --namespace chargeback \
    --api-versions cilium.io/v2 --api-versions postgresql.cnpg.io/v1 "$@" 2>/dev/null
}
# helm-controller's post-render labels + the release namespace (see header).
stamp_flux() {
  yq '
    select(.kind != null)
    | .metadata.labels."helm.toolkit.fluxcd.io/name" = "chargeback"
    | .metadata.labels."helm.toolkit.fluxcd.io/namespace" = "chargeback"
    | with(select(.kind != "ClusterRole" and .kind != "ClusterRoleBinding"); .metadata.namespace = (.metadata.namespace // "chargeback"))
  '
}

# A: the Sovereign slot before platform enforcement is wired (platformApi.url unset).
render --set "sovereignFqdn=$FQDN" | stamp_flux > "$work/render-a.yaml"
# B: the Sovereign slot fully wired — adapter on, platform enforcement on,
#    billing callback Secret named, SMTP Secret named, token audience named.
render --set "sovereignFqdn=$FQDN" --set adapter.enabled=true \
  --set platformApi.url=http://catalyst-api.catalyst-system.svc.cluster.local:8080 \
  --set platformApi.tokenAudience=sovereign-admin-api \
  --set adapter.billingHook.callbackSecret=chargeback-billing-callback \
  --set adapter.billingHook.tokenSecret=chargeback-billing-hook \
  --set smtp.existingSecret=chargeback-smtp | stamp_flux > "$work/render-b.yaml"

for r in a b; do
  f="$work/render-$r.yaml"
  docs="$(grep -c '^kind: ' "$f" || true)"
  [ "$docs" -ge 10 ] || fail "render $r produced only $docs objects — the chart did not render"
  stamped="$(grep -c 'helm.toolkit.fluxcd.io/name: chargeback' "$f" || true)"
  [ "$stamped" -eq "$docs" ] || fail "render $r: $stamped of $docs objects carry the Flux post-render label — the stamp step is broken"
done
grep -q 'name: PLATFORM_API_URL' "$work/render-b.yaml" || fail "render b did not wire platformApi — the enforcement env is not under test"
grep -q 'name: PLATFORM_API_URL' "$work/render-a.yaml" && fail "render a wired platformApi with platformApi.url unset"

# ── apply ───────────────────────────────────────────────────────────────────
overall=0
for r in a b; do
  f="$work/render-$r.yaml"
  out="$work/apply-$r.txt"
  set +e
  "$kyverno" apply "$work/policies.yaml" --resource "$f" > "$out" 2>"$work/apply-$r.err"
  rc=$?
  set -e
  applying="$(grep -m1 -oE 'Applying [0-9]+ policy rule\(s\) to [0-9]+ resource\(s\)' "$out" || true)"
  rules="$(grep -oE '^Applying [0-9]+' <<<"$applying" | grep -oE '[0-9]+' || echo 0)"
  resources="$(grep -oE 'to [0-9]+ resource' <<<"$applying" | grep -oE '[0-9]+' || echo 0)"
  summary="$(grep -m1 -E '^pass: [0-9]+, fail: [0-9]+' "$out" || true)"
  [ -n "$summary" ] || { cat "$out" "$work/apply-$r.err" >&2; fail "render $r: kyverno apply printed no summary (rc=$rc)"; }
  n_fail="$(grep -oE 'fail: [0-9]+' <<<"$summary" | grep -oE '[0-9]+')"
  n_err="$(grep -oE 'error: [0-9]+' <<<"$summary" | grep -oE '[0-9]+')"
  n_pass="$(grep -oE 'pass: [0-9]+' <<<"$summary" | grep -oE '[0-9]+')"
  echo "render $r: $applying — $summary"
  # vacuity: the engine must have evaluated real rules against real objects.
  [ "$rules" -gt 0 ] || { overall=1; echo "FAIL render $r: 0 policy rules applied — the policy file did not load" >&2; }
  [ "$resources" -ge 10 ] || { overall=1; echo "FAIL render $r: only $resources resources evaluated" >&2; }
  [ "$n_pass" -gt 0 ] || { overall=1; echo "FAIL render $r: zero passes — nothing was evaluated" >&2; }
  if [ "$n_fail" -ne 0 ] || [ "$n_err" -ne 0 ]; then
    overall=1
    echo "FAIL render $r: $n_fail fail / $n_err error — the Sovereign's PolicyReports would carry these rows:" >&2
    grep -E -A2 '^policy .* -> resource .* (failed|error)' "$out" | sed 's/^/  /' >&2
    [ -s "$work/apply-$r.err" ] && sed 's/^/  stderr: /' "$work/apply-$r.err" >&2
  fi
done

[ "$overall" -eq 0 ] || exit 1
echo "PASS: bp-chargeback satisfies all $policy_count bp-kyverno-policies ClusterPolicies (kyverno CLI $KYVERNO_CLI_VERSION; platformApi unset + set, callbackSecret set; zero fail, zero error)"
