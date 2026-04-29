#!/usr/bin/env bash
# operator-recover-sovereign.sh — idempotent recovery of a partially-provisioned Sovereign.
#
# When a Sovereign provisioning run fails partway (Phase 0 OpenTofu, Phase 1
# bootstrap-kit, or anywhere in between), this script returns the system to a
# clean slate so the operator can re-run Launch in the wizard with the same
# FQDN. It does THREE things, in order:
#
#   1. Purge every Hetzner Cloud resource tagged for that Sovereign.
#   2. Release the PDM allocation for the Sovereign's pool subdomain (if any).
#   3. Mark the catalyst-api deployment record as `cancelled` so the wizard
#      stops streaming events for it and the operator can re-create cleanly.
#
# Resource names in the OpenTofu module are deterministic:
#   catalyst-${replace(sovereign_fqdn, ".", "-")}-{role}
# so re-running Launch with the same FQDN after this script is fully idempotent.
#
# DRY-RUN by default. Pass --apply to actually delete.
#
# Anchored to the canonical purge logic in:
#   /home/openova/.claude/projects/-home-openova-repos-openova-private/memory/feedback_idempotent_iac_purge.md
# and the runbook at:
#   docs/RUNBOOK-OPERATIONS.md §"Recovery procedure"
#
# Usage:
#   ./scripts/operator-recover-sovereign.sh <sovereign-fqdn>          # dry-run
#   ./scripts/operator-recover-sovereign.sh <sovereign-fqdn> --apply  # destructive
#
# Required tools:    bash, curl, python3, kubectl
# Required env vars: HETZNER_API_TOKEN  (read+write project token)
# Optional env vars: PDM_BASE_URL       (default: derived from --pool-domain or omani.works)
#                    POOL_DOMAIN        (default: derived from FQDN's parent zone)
#                    CATALYST_NAMESPACE (default: catalyst)

set -euo pipefail

# ── Argument parsing ──────────────────────────────────────────────────

FQDN="${1:-}"
MODE="dry-run"
shift || true
while [ "$#" -gt 0 ]; do
  case "$1" in
    --apply) MODE="apply" ;;
    --dry-run) MODE="dry-run" ;;
    *) echo "ERR: unknown flag: $1" >&2; exit 2 ;;
  esac
  shift
done

if [ -z "$FQDN" ]; then
  echo "Usage: $0 <sovereign-fqdn> [--apply]" >&2
  echo "Example: $0 omantel.omani.works --apply" >&2
  exit 2
fi

# Slug used by the OpenTofu module to name resources, matching:
#   catalyst-${replace(sovereign_fqdn, ".", "-")}-{role}
SLUG=$(echo "$FQDN" | tr . -)
LABEL_KEY="catalyst.openova.io/sovereign"
NS="${CATALYST_NAMESPACE:-catalyst}"

# Pool domain inference: the parent zone of the FQDN.
# omantel.omani.works -> omani.works
# acme.openova.io     -> openova.io
# acme.bank.com       -> bank.com  (would only matter if PDM manages it)
POOL_DOMAIN="${POOL_DOMAIN:-$(echo "$FQDN" | cut -d. -f2-)}"

# ── Output helpers ────────────────────────────────────────────────────

bold()   { printf "\033[1m%s\033[0m\n" "$*"; }
red()    { printf "\033[31m%s\033[0m\n" "$*"; }
green()  { printf "\033[32m%s\033[0m\n" "$*"; }
yellow() { printf "\033[33m%s\033[0m\n" "$*"; }
cyan()   { printf "\033[36m%s\033[0m\n" "$*"; }

prefix() {
  if [ "$MODE" = "dry-run" ]; then
    yellow "[DRY-RUN] $*"
  else
    cyan "[APPLY]   $*"
  fi
}

# ── Banner ────────────────────────────────────────────────────────────

bold "==================================================================="
bold "  OpenOva Catalyst — Operator Sovereign Recovery"
bold "==================================================================="
echo "  Sovereign FQDN:    $FQDN"
echo "  Resource slug:     catalyst-${SLUG}-*"
echo "  Hetzner label:     ${LABEL_KEY}=${FQDN}"
echo "  Pool parent zone:  ${POOL_DOMAIN}"
echo "  Catalyst NS:       ${NS}"
echo "  Mode:              $MODE"
bold "==================================================================="
echo

if [ "$MODE" = "dry-run" ]; then
  yellow "  Running in DRY-RUN mode. Nothing will be deleted."
  yellow "  Re-run with --apply to actually purge resources."
else
  red "  Running in APPLY mode. Resources WILL be deleted. CTRL-C now to abort."
  sleep 3
fi
echo

# ── Pre-flight ────────────────────────────────────────────────────────

# In dry-run mode we tolerate a missing/invalid token — the operator is just
# previewing what would happen. In apply mode we hard-fail.
HAVE_HETZNER_TOKEN=0
if [ -n "${HETZNER_API_TOKEN:-}" ]; then
  HTTP_CODE=$(curl -sS -o /dev/null -w '%{http_code}' \
    -H "Authorization: Bearer ${HETZNER_API_TOKEN}" \
    "https://api.hetzner.cloud/v1/servers?per_page=1" || true)
  if [ "$HTTP_CODE" = "200" ]; then
    HAVE_HETZNER_TOKEN=1
    green "  HETZNER_API_TOKEN validated — Hetzner inventory will be queried live."
  else
    if [ "$MODE" = "apply" ]; then
      red "ERR: HETZNER_API_TOKEN rejected by Hetzner API (HTTP $HTTP_CODE). Aborting."
      exit 3
    else
      yellow "  HETZNER_API_TOKEN rejected by Hetzner (HTTP $HTTP_CODE) — Step 1 will be a name-only preview."
    fi
  fi
else
  if [ "$MODE" = "apply" ]; then
    red "ERR: HETZNER_API_TOKEN is not set. Export the read+write token for the Sovereign's project."
    exit 3
  else
    yellow "  HETZNER_API_TOKEN is not set — Step 1 will be a name-only preview (set the token to enumerate resources for real)."
  fi
fi
echo

# ── Step 1 — Hetzner purge ────────────────────────────────────────────

bold "── Step 1 / 3 — Hetzner Cloud resource purge ─────────────────────"

H="Authorization: Bearer ${HETZNER_API_TOKEN:-NO_TOKEN}"
SEL="label_selector=${LABEL_KEY}=${FQDN}"
KINDS_LABELED="servers load_balancers networks firewalls volumes primary_ips floating_ips"

list_ids_labelled() {
  local kind="$1"
  curl -sS -H "$H" "https://api.hetzner.cloud/v1/${kind}?${SEL}" |
    python3 -c "import json,sys; d=json.load(sys.stdin); [print(s['id'], s.get('name','')) for s in d.get('${kind}',[])]"
}

list_ids_by_name() {
  # Hetzner ssh_keys (and a few other kinds) don't accept label selectors.
  # Match by deterministic resource-name slug instead.
  local kind="$1"
  curl -sS -H "$H" "https://api.hetzner.cloud/v1/${kind}" |
    python3 -c "
import json, sys
d = json.load(sys.stdin)
items = d.get('${kind}', [])
slug = '${SLUG}'
for s in items:
    name = s.get('name', '')
    if slug in name:
        print(s['id'], name)
"
}

delete_resource() {
  local kind="$1" id="$2" name="$3"
  prefix "DELETE ${kind}/${id} (${name})"
  if [ "$MODE" = "apply" ]; then
    curl -sS -o /dev/null -X DELETE -H "$H" \
      "https://api.hetzner.cloud/v1/${kind}/${id}" || red "  WARN: delete returned non-zero for ${kind}/${id}"
  fi
}

ANY_FOUND=0

if [ "$HAVE_HETZNER_TOKEN" = "1" ]; then
  # Pass 1: label-selected resources, in dependency order (servers first so
  # LB/network/firewall freeing isn't blocked).
  for kind in $KINDS_LABELED ; do
    while IFS= read -r line; do
      [ -z "$line" ] && continue
      ANY_FOUND=1
      id=$(echo "$line" | awk '{print $1}')
      name=$(echo "$line" | cut -d' ' -f2-)
      delete_resource "$kind" "$id" "$name"
    done < <(list_ids_labelled "$kind")
  done

  # Pass 2: ssh_keys (no labels — match by name slug).
  while IFS= read -r line; do
    [ -z "$line" ] && continue
    ANY_FOUND=1
    id=$(echo "$line" | awk '{print $1}')
    name=$(echo "$line" | cut -d' ' -f2-)
    delete_resource "ssh_keys" "$id" "$name"
  done < <(list_ids_by_name "ssh_keys")

  # Pass 3: verification sweep — Hetzner DELETE often returns 204 even when
  # the resource persists (notably firewalls right after a server delete).
  # Re-query without the label filter and re-delete by-id any that linger.
  # Skipping this caused "name is already used (uniqueness_error)" on the
  # next provision attempt. See feedback_idempotent_iac_purge.md.
  echo
  yellow "── Verification sweep (catches Hetzner DELETE-returns-204-but-keeps-resource quirk) ──"
  for kind in firewalls networks load_balancers ssh_keys servers volumes primary_ips floating_ips ; do
    while IFS= read -r line; do
      [ -z "$line" ] && continue
      id=$(echo "$line" | awk '{print $1}')
      name=$(echo "$line" | cut -d' ' -f2-)
      yellow "  Lingering ${kind}/${id} (${name}) — re-deleting"
      delete_resource "$kind" "$id" "$name"
    done < <(list_ids_by_name "$kind")
  done

  if [ "$ANY_FOUND" = "0" ]; then
    green "  No Hetzner resources found for ${FQDN}. Already clean."
  fi
else
  yellow "  (Token not available — listing the resource names that WOULD be inspected.)"
  for kind in $KINDS_LABELED ssh_keys ; do
    prefix "QUERY  ${kind}?label_selector=${LABEL_KEY}=${FQDN}  (would DELETE any matches: catalyst-${SLUG}-*)"
  done
  echo
  yellow "── Verification sweep (would also re-list without label filter and re-delete catalyst-${SLUG}-* names) ──"
  for kind in firewalls networks load_balancers ssh_keys servers volumes primary_ips floating_ips ; do
    prefix "QUERY  ${kind}  (would re-DELETE any name matching slug ${SLUG})"
  done
fi
echo

# ── Step 2 — PDM allocation release ───────────────────────────────────

bold "── Step 2 / 3 — Pool-domain-manager allocation release ───────────"

# Sub-label = first label of the FQDN (omantel.omani.works -> omantel).
SUB=$(echo "$FQDN" | cut -d. -f1)

# Locate PDM. Prefer in-cluster service; fall back to env override.
PDM_BASE_URL="${PDM_BASE_URL:-http://pool-domain-manager.openova-system.svc.cluster.local:8080}"

prefix "DELETE ${PDM_BASE_URL}/api/v1/pool/${POOL_DOMAIN}/release?sub=${SUB}"

if [ "$MODE" = "apply" ]; then
  # Run the curl from inside the cluster so the in-cluster DNS resolves.
  if kubectl -n openova-system get deploy pool-domain-manager >/dev/null 2>&1; then
    kubectl -n openova-system exec deploy/pool-domain-manager -- \
      sh -c "wget -q -O - --method=DELETE --header='Content-Type: application/json' \
        'http://localhost:8080/api/v1/pool/${POOL_DOMAIN}/release?sub=${SUB}' || true" \
      2>/dev/null || true
  else
    yellow "  pool-domain-manager Deployment not found in openova-system; skipping PDM release."
    yellow "  If PDM lives elsewhere, set PDM_BASE_URL and re-run."
  fi
else
  # Dry-run: just check whether the allocation exists.
  if kubectl -n openova-system get deploy pool-domain-manager >/dev/null 2>&1; then
    OUT=$(kubectl -n openova-system exec deploy/pool-domain-manager -- \
      sh -c "wget -q -O - 'http://localhost:8080/api/v1/pool/${POOL_DOMAIN}/check?sub=${SUB}'" \
      2>/dev/null || echo '{}')
    AVAIL=$(echo "$OUT" | python3 -c "import json,sys; d=json.load(sys.stdin) if sys.stdin else {}; print(d.get('available','unknown'))" 2>/dev/null || echo unknown)
    case "$AVAIL" in
      true)  green "  PDM reports ${SUB}.${POOL_DOMAIN} is already AVAILABLE — nothing to release." ;;
      false) yellow "  PDM reports ${SUB}.${POOL_DOMAIN} is currently RESERVED or COMMITTED — release will free it." ;;
      *)     yellow "  PDM check returned: ${OUT}" ;;
    esac
  else
    yellow "  pool-domain-manager Deployment not found in openova-system (skipping check)."
  fi
fi
echo

# ── Step 3 — catalyst-api deployment record ───────────────────────────

bold "── Step 3 / 3 — catalyst-api deployment record cancellation ──────"

if ! kubectl -n "$NS" get deploy catalyst-api >/dev/null 2>&1; then
  yellow "  catalyst-api Deployment not found in namespace ${NS}. Skipping."
else
  # Find the record(s) for this FQDN. Records live at:
  #   /var/lib/catalyst/deployments/<id>.json
  # and contain { "request": { "sovereignFQDN": "...", ... }, "status": "...", ... }
  POD=$(kubectl -n "$NS" get pod -l app.kubernetes.io/name=catalyst-api \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
  if [ -z "$POD" ]; then
    yellow "  No catalyst-api Pod found. Skipping deployment-record cancellation."
  else
    # The catalyst-api container is scratch-based — no python3, no jq, only
    # /bin/sh + the catalyst-api binary. We pull each record file out via
    # `kubectl exec ... cat` and parse the JSON locally.
    DEPLOY_FILES=$(kubectl -n "$NS" exec "$POD" -- sh -c \
      "ls /var/lib/catalyst/deployments/*.json 2>/dev/null || true" 2>/dev/null || true)

    MATCHED_IDS=""
    for f in $DEPLOY_FILES; do
      [ -z "$f" ] && continue
      JSON=$(kubectl -n "$NS" exec "$POD" -- sh -c "cat '$f'" 2>/dev/null || true)
      RECORD_FQDN=$(echo "$JSON" | python3 -c "
import json, sys
try:
  d = json.load(sys.stdin)
  print(d.get('request', {}).get('sovereignFQDN', ''))
except Exception:
  print('')
" 2>/dev/null || echo "")
      RECORD_STATUS=$(echo "$JSON" | python3 -c "
import json, sys
try:
  d = json.load(sys.stdin)
  print(d.get('status', ''))
except Exception:
  print('')
" 2>/dev/null || echo "")
      if [ "$RECORD_FQDN" = "$FQDN" ]; then
        DID=$(basename "$f" .json)
        MATCHED_IDS="${MATCHED_IDS}${DID} ${RECORD_STATUS}\n"
      fi
    done

    if [ -z "$MATCHED_IDS" ]; then
      green "  No catalyst-api deployment records reference ${FQDN}. Nothing to cancel."
    else
      printf '%b' "$MATCHED_IDS" | while IFS= read -r line; do
        [ -z "$line" ] && continue
        DID=$(echo "$line" | awk '{print $1}')
        DSTATUS=$(echo "$line" | awk '{print $2}')
        prefix "Mark deployment ${DID} (current status: ${DSTATUS}) -> status=cancelled"
        if [ "$MODE" = "apply" ]; then
          # Pull, mutate locally on the host (where python3 exists), push back.
          NOW=$(date -u +%Y-%m-%dT%H:%M:%SZ)
          ORIG=$(kubectl -n "$NS" exec "$POD" -- sh -c "cat /var/lib/catalyst/deployments/${DID}.json" 2>/dev/null || true)
          NEW=$(echo "$ORIG" | python3 -c "
import json, sys
d = json.load(sys.stdin)
d['status'] = 'cancelled'
d['finishedAt'] = '${NOW}'
d.setdefault('events', []).append({
    'time': '${NOW}',
    'phase': 'operator-recovery',
    'level': 'warn',
    'message': 'cancelled by scripts/operator-recover-sovereign.sh'
})
print(json.dumps(d, indent=2))
" 2>/dev/null || true)
          if [ -n "$NEW" ]; then
            # Pipe new JSON back into the Pod via stdin -> tee.
            echo "$NEW" | kubectl -n "$NS" exec -i "$POD" -- \
              sh -c "cat > /var/lib/catalyst/deployments/${DID}.json" \
              || red "  WARN: could not rewrite ${DID}.json"
          else
            red "  WARN: could not parse ${DID}.json — skipping rewrite"
          fi
        fi
      done
    fi
  fi
fi
echo

# ── Done ──────────────────────────────────────────────────────────────

bold "==================================================================="
if [ "$MODE" = "dry-run" ]; then
  yellow "  DRY-RUN complete. No changes made."
  yellow "  Re-run with --apply to actually purge."
else
  green "  RECOVERY APPLIED. The operator may now re-run Launch in the wizard"
  green "  with sovereign-fqdn=${FQDN}. Re-runs are fully idempotent because"
  green "  every Hetzner resource is named deterministically off the FQDN."
fi
bold "==================================================================="
exit 0
