#!/usr/bin/env bash
# bp-gateway-api re-vendor helper.
#
# Pulls a fresh `standard-install.yaml` from the upstream
# kubernetes-sigs/gateway-api release tagged at the version recorded in
# `Chart.yaml`'s `catalyst.openova.io/upstream-gateway-api-version`
# annotation, splits it into per-CRD template files, and injects
# `helm.sh/resource-policy: keep` into each CRD's metadata.annotations
# block.
#
# Usage:
#   1. Edit Chart.yaml: bump `version:` and the
#      `catalyst.openova.io/upstream-gateway-api-version` annotation to
#      the new upstream tag (e.g. v1.3.0).
#   2. From the repo root: `bash platform/gateway-api/chart/scripts/regenerate.sh`
#   3. Inspect the diff under platform/gateway-api/chart/templates/ and
#      commit alongside the version bump.
#
# This is a developer tool, NOT a chart integration test — the CI gate is
# tests/crd-render.sh.

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
chart_yaml="$CHART_DIR/Chart.yaml"
templates_dir="$CHART_DIR/templates"

if [ ! -f "$chart_yaml" ]; then
  echo "::error::Chart.yaml not found at $chart_yaml"
  exit 1
fi

upstream_ver="$(awk -F'"' '/catalyst.openova.io\/upstream-gateway-api-version:/ {print $2}' "$chart_yaml")"
if [ -z "$upstream_ver" ]; then
  echo "::error::Could not read catalyst.openova.io/upstream-gateway-api-version from $chart_yaml"
  exit 1
fi

url="https://github.com/kubernetes-sigs/gateway-api/releases/download/${upstream_ver}/experimental-install.yaml"
echo "Pulling $url"
tmp=$(mktemp)
curl -sLf "$url" -o "$tmp"
echo "  → $(wc -l < "$tmp") lines"

python3 - "$tmp" "$templates_dir" <<'PYEOF'
import re, sys
src_path, out_dir = sys.argv[1], sys.argv[2]
src = open(src_path).read()
docs = re.split(r'\n---\n', src)

# Dynamic per-CRD file naming: every CRD in the experimental bundle lands
# as <plural>.yaml. The EXPERIMENTAL channel is REQUIRED (not standard):
# cilium checks for tlsroutes.gateway.networking.k8s.io at operator
# startup and disables its gateway controller when absent.

header = '''{{/*
  Vendored from kubernetes-sigs/gateway-api {{ index .Chart.Annotations "catalyst.openova.io/upstream-gateway-api-version" }}
  experimental-install.yaml. Do NOT hand-edit annotations / spec — re-vendor
  via the script in tests/regenerate.sh when bumping the upstream version
  (also bump catalyst.openova.io/upstream-gateway-api-version in Chart.yaml).

  helm.sh/resource-policy: keep is added under metadata.annotations so a
  Helm uninstall does NOT delete the CRD. Gateway API CRDs are foundational
  cluster-scoped infrastructure; deleting them on uninstall would break
  every HTTPRoute on the cluster simultaneously.
*/}}
'''

written = []
for d in docs:
    if 'kind: CustomResourceDefinition' not in d:
        continue
    m = re.search(r'^  name: (\S+)', d, re.MULTILINE)
    if not m:
        continue
    name = m.group(1)
    suffix = name.split('.', 1)[0]
    out = re.sub(
        r'^(  annotations:\n)',
        r'\1    helm.sh/resource-policy: keep\n',
        d, count=1, flags=re.MULTILINE,
    ).strip() + '\n'
    target = f"{out_dir}/{suffix}.yaml"
    with open(target, 'w') as f:
        f.write(header)
        f.write(out)
    written.append(target)

print("\n".join(f"  ✓ {t}" for t in written))
PYEOF

rm -f "$tmp"
echo "Re-vendor complete. Inspect with: git diff $templates_dir/"
