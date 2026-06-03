#!/usr/bin/env bash
# sovereign-wipe-preflight.sh — enumerate live Sovereigns + produce the
# per-deployment-ID table mandated by memory L4 BEFORE any wipe.
#
# Reasoning: per project CLAUDE.md §Pre-flight-checklist row "Wipe /
# scale / destroy any Sovereign or namespace", every Sovereign wipe
# MUST be preceded by:
#   1. Read tofu.auto.tfvars.json of every candidate via PVC debug Pod.
#   2. Query Hetzner servers with the token from step 1.
#   3. Produce per-target table with org_name + live infra.
#   4. **Ask founder which rows are wipeable**.
#   5. Only on confirmation, use canonical wipe endpoint per L2.
#
# This script automates steps 1-3. Steps 4-5 are still founder-only.
#
# Memory cross-refs:
#   - feedback_credentials_already_in_cluster.md (PVC mount pattern)
#   - feedback_canonical_wipe_endpoints.md (canonical wipe API)
#   - L4 verbatim: "produce a per-dep-ID table: (id | live Hetzner
#     servers count | org_name | created | parent_domain). Get founder
#     confirmation on which rows are wipeable. Default = keep
#     everything until told otherwise."
#
# Usage:
#   KUBECONFIG=~/.kube/mothership.kubeconfig \
#     scripts/sovereign-wipe-preflight.sh
#
# Optional:
#   PROVIDER=hetzner|huawei   # query only one cloud's live infra
#   OUTPUT_FORMAT=table|json  # default: table
#   DEBUG_POD_TIMEOUT=300     # seconds for kubectl debug Pod readiness
#
# Output: a markdown-formatted table per the L4 mandate, ready to
# paste into a Slack DM or GitHub comment for founder confirmation.
#
# Exit codes:
#   0  — table emitted successfully (regardless of whether any
#        candidates were found)
#   2  — missing required env (KUBECONFIG)
#   3  — failed to enumerate deployments via the PVC
#   4  — failed to query cloud-provider live infra

set -euo pipefail

: "${KUBECONFIG:?required env var (path to mothership kubeconfig)}"
PROVIDER="${PROVIDER:-both}"
OUTPUT_FORMAT="${OUTPUT_FORMAT:-table}"
DEBUG_POD_TIMEOUT="${DEBUG_POD_TIMEOUT:-300}"

DEBUG_POD_NAME="sovereign-wipe-preflight-$$"
trap 'kubectl -n catalyst delete pod "$DEBUG_POD_NAME" --ignore-not-found --wait=false 2>/dev/null || true' EXIT

# Step 1: spin a debug Pod that mounts catalyst-api-deployments PVC.
cat <<POD | kubectl apply -f - >/dev/null
apiVersion: v1
kind: Pod
metadata:
  name: ${DEBUG_POD_NAME}
  namespace: catalyst
spec:
  restartPolicy: Never
  containers:
    - name: probe
      image: busybox:1.36
      command: ["sh", "-c", "sleep ${DEBUG_POD_TIMEOUT}"]
      volumeMounts:
        - name: deps
          mountPath: /deps
          readOnly: true
  volumes:
    - name: deps
      persistentVolumeClaim:
        claimName: catalyst-api-deployments
POD

kubectl -n catalyst wait pod "$DEBUG_POD_NAME" --for=condition=Ready --timeout="${DEBUG_POD_TIMEOUT}s" >/dev/null

# Step 2: enumerate each tofu workdir + read tofu.auto.tfvars.json.
dep_ids="$(kubectl -n catalyst exec "$DEBUG_POD_NAME" -- ls /deps/tofu 2>/dev/null || true)"
if [ -z "$dep_ids" ]; then
  echo "ERROR: no tofu workdirs under /deps/tofu/" >&2
  exit 3
fi

# Step 3: per dep-id, extract the L4-mandated fields + query live cloud infra.
rows=""
for id in $dep_ids; do
  tfvars="$(kubectl -n catalyst exec "$DEBUG_POD_NAME" -- cat "/deps/tofu/$id/tofu.auto.tfvars.json" 2>/dev/null || echo '{}')"
  if [ "$tfvars" = "{}" ] || [ -z "$tfvars" ]; then
    rows+="| $id | ERROR | (tfvars unreadable) | - | - |"$'\n'
    continue
  fi

  org_name="$(echo "$tfvars" | jq -r '.org_name // "(unset)"')"
  parent_domain="$(echo "$tfvars" | jq -r '.sovereign_fqdn // .parent_domains_yaml // "(unset)"' | head -1)"
  prov_name="$(echo "$tfvars" | jq -r '.provider // "hetzner"')"
  created="$(kubectl -n catalyst exec "$DEBUG_POD_NAME" -- stat -c %y "/deps/tofu/$id/tofu.auto.tfvars.json" 2>/dev/null | cut -d. -f1 || echo "-")"

  if [ "$PROVIDER" != "both" ] && [ "$PROVIDER" != "$prov_name" ]; then
    continue
  fi

  # Step 2 (cloud query): count live infra against the token in tfvars.
  live_count="?"
  case "$prov_name" in
    hetzner)
      token="$(echo "$tfvars" | jq -r '.hcloud_token // empty')"
      if [ -n "$token" ]; then
        live_count="$(curl -fsS -H "Authorization: Bearer $token" \
          'https://api.hetzner.cloud/v1/servers' 2>/dev/null | \
          jq -r --arg fqdn "$parent_domain" \
            '[.servers[] | select(.name | contains($fqdn | gsub("\\."; "-")))] | length' \
          2>/dev/null || echo "?")"
      fi
      ;;
    huawei)
      # Huawei IAM token mint is multi-step; skip and surface as "?"
      # so the founder knows to query manually.
      live_count="? (huawei: query manually)"
      ;;
  esac

  rows+="| \`$id\` | $prov_name | $org_name | $parent_domain | $created | $live_count |"$'\n'
done

if [ "$OUTPUT_FORMAT" = "json" ]; then
  echo "$rows" | awk -F'|' 'NF>5 {gsub(/^[ \t]+|[ \t]+$/,"",$2); gsub(/^[ \t]+|[ \t]+$/,"",$3); gsub(/^[ \t]+|[ \t]+$/,"",$4); gsub(/^[ \t]+|[ \t]+$/,"",$5); gsub(/^[ \t]+|[ \t]+$/,"",$6); gsub(/^[ \t]+|[ \t]+$/,"",$7); printf "{\"id\":\"%s\",\"provider\":\"%s\",\"org_name\":\"%s\",\"parent_domain\":\"%s\",\"created\":\"%s\",\"live_infra\":\"%s\"}\n", $2, $3, $4, $5, $6, $7}'
  exit 0
fi

cat <<HEADER

# Sovereign-wipe pre-flight table — $(date -u +%Y-%m-%dT%H:%M:%SZ)

Per memory L4 + project CLAUDE.md §Pre-flight, founder confirmation required
on which rows are wipeable BEFORE any \`POST /sovereign/api/v1/deployments/<id>/wipe\`.

Default: **keep everything until told otherwise**.

| Deployment ID | Provider | Org Name | Parent Domain | Created | Live Infra |
|---|---|---|---|---|---|
HEADER
printf '%s' "$rows"
cat <<FOOTER

Next steps:
  1. Founder reviews above table.
  2. For each ROW to wipe, founder runs:
       curl -X POST -H "Authorization: Bearer <handover-jwt>" \\
            https://console.openova.io/sovereign/api/v1/deployments/<id>/wipe
  3. Default = keep. Do not wipe rows not explicitly named by founder.

Refs #2803 + memory L4 + feedback_canonical_wipe_endpoints.md.
FOOTER
