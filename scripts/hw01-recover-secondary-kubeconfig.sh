#!/bin/sh
# Wave 5.75 (#2401) — manual secondary-kubeconfig PUT for existing hw01.
#
# Recovers visibility of region-B's cluster in the Sovereign-console
# Cloud-list UI WITHOUT a full re-prov. Wave 5.74 (#2400) fixes the
# cloud-init for FUTURE provs; this script rescues the EXISTING hw01.
#
# Usage (run from a network position that reaches 212.72.24.49):
#
#   export DEP_ID=974113d27f0e3666
#   export REGION_B_EIP=212.72.24.49
#   export REGION_B_KEY=me-east-215-b
#   export DEPLOY_SSH_KEY=~/.ssh/hw01-deploy-key
#   export MOTHERSHIP_URL=https://console.openova.io/sovereign
#   export BEARER_TOKEN=<copy from `kubectl -n catalyst exec catalyst-api-... -- env | grep KUBECONFIG_BEARER_TOKEN`>
#   ./scripts/hw01-recover-secondary-kubeconfig.sh
#
# After successful POST, /var/lib/catalyst/kubeconfigs/<DEP_ID>-<REGION_B_KEY>.yaml
# appears on mothership PVC; k8sCache.Factory registers the 2nd cluster
# on its next reconcile (5min interval); /cloud shows Clusters=2.
set -eu

: "${DEP_ID:?DEP_ID env required (e.g. 974113d27f0e3666)}"
: "${REGION_B_EIP:?REGION_B_EIP env required}"
: "${REGION_B_KEY:?REGION_B_KEY env required (e.g. me-east-215-b)}"
: "${DEPLOY_SSH_KEY:?DEPLOY_SSH_KEY env required}"
: "${MOTHERSHIP_URL:?MOTHERSHIP_URL env required}"
: "${BEARER_TOKEN:?BEARER_TOKEN env required}"

TMPF=$(mktemp)
trap 'rm -f "$TMPF" "${TMPF}.body"' EXIT

echo "Wave 5.75: pulling region-B kubeconfig from $REGION_B_EIP"
ssh -i "$DEPLOY_SSH_KEY" -o StrictHostKeyChecking=no -o ConnectTimeout=10 \
  "root@${REGION_B_EIP}" "cat /etc/rancher/k3s/k3s.yaml" > "$TMPF"

echo "Wave 5.75: rewriting server URL to $REGION_B_EIP:6443"
sed -i "s|server: https://127.0.0.1:6443|server: https://${REGION_B_EIP}:6443|" "$TMPF"

echo "Wave 5.75: composing JSON envelope"
KUBECONFIG_ESCAPED=$(awk 'BEGIN{ORS=""} {gsub(/\\/,"\\\\"); gsub(/"/,"\\\""); gsub(/\n/,"\\n"); print $0"\\n"}' "$TMPF")
printf '{"deploymentId":"%s","regionKey":"%s","kubeconfigYaml":"%s"}' \
  "$DEP_ID" "$REGION_B_KEY" "$KUBECONFIG_ESCAPED" > "${TMPF}.body"

echo "Wave 5.75: POST to ${MOTHERSHIP_URL}/api/v1/sovereign/secondary-kubeconfig"
HTTP_CODE=$(curl -sk -o /tmp/sk-response.json -w '%{http_code}' -X POST \
  -H "Authorization: Bearer ${BEARER_TOKEN}" \
  -H "Content-Type: application/json" \
  --data-binary "@${TMPF}.body" \
  --max-time 30 \
  "${MOTHERSHIP_URL}/api/v1/sovereign/secondary-kubeconfig" || echo "000")

case "$HTTP_CODE" in
  200|201|204)
    echo "Wave 5.75: SUCCESS — HTTP $HTTP_CODE"
    echo "Wait 5min for k8sCache.Factory next reconcile, then check console.<sov-fqdn>/cloud"
    ;;
  *)
    echo "Wave 5.75: FAIL — HTTP $HTTP_CODE" >&2
    cat /tmp/sk-response.json >&2 2>/dev/null || true
    exit 1
    ;;
esac
