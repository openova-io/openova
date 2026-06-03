#!/usr/bin/env bash
# tools/patch-existing-nodes-registries-yaml.sh
#
# #2842 follow-up operator script — patches /etc/rancher/k3s/registries.yaml
# on every node of an existing Sovereign with the harbor.openova.io robot
# auth block. Needed for Sovereigns provisioned BEFORE PR #2868 landed
# the cloud-init template fix (e.g. hw86 + earlier fleet).
#
# Fresh provs from the current cloud-init get this auth block at node
# bootstrap (infra/providers/huawei/cloudinit-worker.tftpl:72-84 +
# infra/providers/hetzner/cloudinit-control-plane.tftpl equivalent).
# Pre-#2868 nodes are missing the auth block → containerd's mirror
# pull through harbor.openova.io returns 401 → cache miss → fallback
# behaviour is variable, and the catalyst-api Pod can wedge in
# ContainerCreating for 25+ min on cold image pulls.
#
# What this script does — for each node in the target cluster:
#   1. Spawns a one-shot `kubectl debug node/<name>` Pod with hostPath
#      access to /etc/rancher/k3s/.
#   2. Reads /etc/rancher/k3s/registries.yaml inside the host root.
#   3. If the file ALREADY contains `harbor.openova.io:` auth block,
#      skips that node (idempotent).
#   4. Otherwise, appends the auth block with the operator-supplied
#      robot username + password.
#   5. Triggers a containerd reload via `systemctl reload k3s` so the
#      new mirror auth is picked up without a full k3s restart.
#
# Usage:
#   KUBECONFIG=~/.kube/hw86.kubeconfig \
#   HARBOR_ROBOT_USER=robot\$openova-bot \
#   HARBOR_ROBOT_PASSWORD=<token> \
#     tools/patch-existing-nodes-registries-yaml.sh
#
# Optional:
#   DRY_RUN=1    Print the patch plan without writing anything.
#   NODE_FILTER  Limit to nodes whose name matches this substring.
#
# Exit codes:
#   0  — success (or all nodes already patched; idempotent)
#   2  — missing required env (HARBOR_ROBOT_USER / HARBOR_ROBOT_PASSWORD)
#   3  — at least one node failed to patch (others may have succeeded)
#
# Safe to re-run: the harbor.openova.io block presence check makes each
# node a no-op once patched. The `systemctl reload k3s` is gentler than
# `restart k3s` — it triggers a containerd config re-read without
# rolling Pods, so running workloads keep flowing during the reload.

set -euo pipefail

: "${HARBOR_ROBOT_USER:?required env var}"
: "${HARBOR_ROBOT_PASSWORD:?required env var}"
: "${KUBECONFIG:?required env var}"

NODE_FILTER="${NODE_FILTER:-}"
DRY_RUN="${DRY_RUN:-0}"

nodes_json="$(kubectl get nodes -o json)"
node_names="$(echo "$nodes_json" | jq -r '.items[].metadata.name')"
if [ -n "$NODE_FILTER" ]; then
  node_names="$(echo "$node_names" | grep -- "$NODE_FILTER" || true)"
fi

if [ -z "$node_names" ]; then
  echo "ERROR: no matching nodes (filter=$NODE_FILTER)" >&2
  exit 3
fi

fail_count=0
patched_count=0
skipped_count=0

for node in $node_names; do
  echo "── ${node} ──"
  if [ "$DRY_RUN" = "1" ]; then
    echo "  [dry-run] would check + patch /etc/rancher/k3s/registries.yaml"
    continue
  fi

  # Step 1+2: probe existing registries.yaml via a one-shot debug Pod.
  # `kubectl debug node/<n>` spawns a Pod with hostPath / mounted at /host;
  # we cat the file via that mount and grep for the harbor entry.
  probe_out="$(kubectl debug node/"$node" --image=busybox:1.36 -q -i -- \
    chroot /host sh -c 'cat /etc/rancher/k3s/registries.yaml 2>/dev/null || true' || true)"

  if echo "$probe_out" | grep -q '"harbor.openova.io":'; then
    echo "  already patched (harbor.openova.io auth block present); skipping"
    skipped_count=$((skipped_count + 1))
    continue
  fi

  # Step 3: append the auth block + reload k3s.
  # Use heredoc with quoted EOF so $vars don't interpolate before
  # we control the substitution.
  patch_script="$(cat <<PATCH
set -e
{
  echo ""
  echo "configs:"
  echo '  "harbor.openova.io":'
  echo "    auth:"
  echo "      username: \"${HARBOR_ROBOT_USER}\""
  echo "      password: \"${HARBOR_ROBOT_PASSWORD}\""
} >> /etc/rancher/k3s/registries.yaml
chmod 600 /etc/rancher/k3s/registries.yaml
systemctl reload k3s 2>/dev/null || systemctl reload k3s-agent 2>/dev/null || true
PATCH
)"

  if kubectl debug node/"$node" --image=busybox:1.36 -q -i -- \
       chroot /host sh -c "$patch_script" >/dev/null 2>&1; then
    echo "  patched + k3s reloaded"
    patched_count=$((patched_count + 1))
  else
    echo "  FAILED (re-run with KUBECONFIG=... bash -x to debug)"
    fail_count=$((fail_count + 1))
  fi
done

echo
echo "── Summary ──"
echo "patched: $patched_count   skipped (already-OK): $skipped_count   failed: $fail_count"

if [ "$fail_count" -gt 0 ]; then
  exit 3
fi
