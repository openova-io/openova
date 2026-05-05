#!/usr/bin/env bash
# bp-cluster-autoscaler-hcloud — node-bootstrap config smoke test (issue #921).
#
# cluster-autoscaler 1.32.x's Hetzner provider FATALs at startup with
# "HCLOUD_CLUSTER_CONFIG or HCLOUD_CLOUD_INIT is not specified" if the
# umbrella chart fails to wire either env var. This regression has hit
# the chart twice (the v1.0.0 release in fact shipped without either,
# making EVERY Sovereign's autoscaler Pod CrashLoopBackOff invisibly
# behind a `Ready=True` HelmRelease — see issue #921 evidence on
# otech112). This test gates against re-introducing that defect.
#
# Verifies:
#   1. Default render: chart emits the `hetzner-node-config` Secret with
#      `cluster-config` + `cloud-init` keys present (even if empty).
#   2. Default render: upstream Deployment declares HCLOUD_CLUSTER_CONFIG
#      AND HCLOUD_CLOUD_INIT env vars sourced from `hetzner-node-config`
#      (the upstream `extraEnvSecrets` map MUST surface both).
#   3. Default render: HCLOUD_TOKEN env var is still wired (no
#      regression of the existing token-secret flow).
#   4. Populated render: when `clusterAutoscalerHcloud.cloudInit` is set,
#      the rendered Secret carries the value verbatim under the
#      `cloud-init` key (the chart MUST NOT base64-encode again — the
#      upstream Hetzner provider expects the value pre-encoded).
#
# Wired into .github/workflows/blueprint-release.yaml's chart-tests gate
# (see "Run chart integration tests (chart/tests/*.sh)") so a regression
# fails the publish job BEFORE the OCI artifact is pushed.
#
# Usage: bash tests/hetzner-node-config.sh [CHART_DIR]
#   CHART_DIR defaults to the parent directory of this script.

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

# Resolve subcharts only if not already vendored (mirrors the
# observability-toggle.sh convention used by bp-cilium / bp-cert-manager
# / bp-harbor — CI's earlier `helm dependency build` step populates
# chart/charts/ before this runs; locally it's empty).
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

# ── Case 1: default render emits the hetzner-node-config Secret ─────────
echo "[hetzner-node-config] Case 1: default render emits hetzner-node-config Secret"
helm template smoke-cas . > "$TMP/default.yaml"
if ! grep -q "name: hetzner-node-config" "$TMP/default.yaml"; then
  echo "FAIL: default render missing 'hetzner-node-config' Secret." >&2
  echo "      bp-cluster-autoscaler-hcloud MUST render this Secret unconditionally" >&2
  echo "      (issue #921 — see chart/templates/hetzner-node-config-secret.yaml)." >&2
  exit 1
fi
# Both keys must be present so the upstream Deployment's secretKeyRef
# resolves cleanly; empty values are fine and intentional.
if ! grep -qE "^[[:space:]]*cluster-config:" "$TMP/default.yaml"; then
  echo "FAIL: hetzner-node-config Secret missing 'cluster-config' key." >&2
  exit 1
fi
if ! grep -qE "^[[:space:]]*cloud-init:" "$TMP/default.yaml"; then
  echo "FAIL: hetzner-node-config Secret missing 'cloud-init' key." >&2
  exit 1
fi
echo "  PASS"

# ── Case 2: deployment env declares HCLOUD_CLUSTER_CONFIG + HCLOUD_CLOUD_INIT ─
echo "[hetzner-node-config] Case 2: deployment declares HCLOUD_CLUSTER_CONFIG + HCLOUD_CLOUD_INIT"
if ! grep -qE "name: HCLOUD_CLUSTER_CONFIG" "$TMP/default.yaml"; then
  echo "FAIL: deployment missing HCLOUD_CLUSTER_CONFIG env var." >&2
  echo "      Without this, cluster-autoscaler 1.32.x FATALs at startup with" >&2
  echo "      \`HCLOUD_CLUSTER_CONFIG or HCLOUD_CLOUD_INIT is not specified\`" >&2
  echo "      (issue #921). values.yaml extraEnvSecrets MUST keep this entry." >&2
  exit 1
fi
if ! grep -qE "name: HCLOUD_CLOUD_INIT" "$TMP/default.yaml"; then
  echo "FAIL: deployment missing HCLOUD_CLOUD_INIT env var (issue #921)." >&2
  exit 1
fi
echo "  PASS"

# ── Case 3: HCLOUD_TOKEN env var still wired ────────────────────────────
echo "[hetzner-node-config] Case 3: HCLOUD_TOKEN still wired (no regression)"
if ! grep -qE "name: HCLOUD_TOKEN" "$TMP/default.yaml"; then
  echo "FAIL: deployment missing HCLOUD_TOKEN env var — pre-#921 regression." >&2
  exit 1
fi
echo "  PASS"

# ── Case 4: populated render carries cloudInit verbatim ─────────────────
echo "[hetzner-node-config] Case 4: clusterAutoscalerHcloud.cloudInit threads to Secret verbatim"
# A canary value the chart must NOT mutate (the upstream Hetzner provider
# expects the operator-supplied base64 verbatim — re-encoding here would
# yield base64(base64(cloud-init)) which the autoscaler rejects).
canary="I2Nsb3VkLWNvbmZpZw==" # base64('#cloud-config')
if ! helm template smoke-cas . \
      --set "clusterAutoscalerHcloud.cloudInit=${canary}" \
      > "$TMP/populated.yaml" 2> "$TMP/populated.err"; then
  echo "FAIL: populated render failed:" >&2
  cat "$TMP/populated.err" >&2
  exit 1
fi
# Verify the canary is present verbatim in the Secret stringData.
if ! grep -qE "^[[:space:]]*cloud-init:[[:space:]]*\"?${canary}\"?\$" "$TMP/populated.yaml"; then
  echo "FAIL: clusterAutoscalerHcloud.cloudInit was NOT threaded verbatim to the Secret." >&2
  echo "      Expected line: 'cloud-init: \"${canary}\"'" >&2
  echo "      Found:" >&2
  grep -E "cloud-init:" "$TMP/populated.yaml" >&2
  exit 1
fi
echo "  PASS"

echo "[hetzner-node-config] All bp-cluster-autoscaler-hcloud node-config gates green."
