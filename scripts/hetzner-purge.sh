#!/usr/bin/env bash
# hetzner-purge.sh — project-wide purge of Catalyst-tagged Hetzner Cloud resources.
#
# Pre-flight runbook for issue #370 (Phase 0a of #369). Cleans up every
# Hetzner resource that previous failed Catalyst provisioning runs left
# behind, so the next Sovereign provision starts from a verifiably empty
# Hetzner project.
#
# This is DIFFERENT from scripts/operator-recover-sovereign.sh:
#   - operator-recover-sovereign.sh is scoped to ONE Sovereign FQDN.
#     Use it when a single in-flight provision fails partway and the
#     operator wants to retry the wizard with the same FQDN.
#   - hetzner-purge.sh is PROJECT-WIDE. It deletes every resource that
#     carries any `catalyst.openova.io/sovereign=*` label, plus any
#     resource whose name matches the deterministic `catalyst-*-*` slug
#     (ssh_keys + safety net for resources where the label was lost).
#     Use it for the Phase-0a pre-flight before omantel runs, or any
#     time the project has accumulated orphans from many failed runs.
#
# Optionally accepts `--fqdn <single-fqdn>` to scope to one Sovereign,
# matching the per-FQDN semantics. Without --fqdn, every Catalyst-tagged
# resource is in scope.
#
# Per ADR-0001 §9.4 — the legacy SME demos run on contabo k3s, not on
# Hetzner. This script touches Hetzner only; SME is fully untouched.
#
# Per docs/INVIOLABLE-PRINCIPLES.md #3 — under normal operation OpenTofu
# owns Phase 0 and `tofu destroy` is the canonical purge path. This
# script is the recovery fallback when the tofu state is gone (Pod
# restart, lost volume, etc.) and orphans must be force-removed.
#
# DRY-RUN by default. Pass --apply to actually delete.
#
# Anchored to the canonical purge logic in:
#   /home/openova/.claude/projects/-home-openova-repos-openova-private/memory/feedback_idempotent_iac_purge.md
#
# Usage:
#   ./scripts/hetzner-purge.sh                                    # dry-run, project-wide
#   ./scripts/hetzner-purge.sh --apply                            # destructive, project-wide
#   ./scripts/hetzner-purge.sh --fqdn omantel.omani.works         # dry-run, single Sovereign
#   ./scripts/hetzner-purge.sh --fqdn omantel.omani.works --apply # destructive, single Sovereign
#   ./scripts/hetzner-purge.sh --apply -y                         # destructive, no confirm prompts (CI)
#
# Required tools:    bash, curl, python3
# Required env vars: HETZNER_API_TOKEN  (read+write project token)

set -euo pipefail

# ── Argument parsing ──────────────────────────────────────────────────

MODE="dry-run"
ASSUME_YES=0
FQDN=""

while [ "$#" -gt 0 ]; do
  case "$1" in
    --apply)    MODE="apply" ;;
    --dry-run)  MODE="dry-run" ;;
    -y|--yes)   ASSUME_YES=1 ;;
    --fqdn)     shift; FQDN="${1:-}" ;;
    --fqdn=*)   FQDN="${1#--fqdn=}" ;;
    -h|--help)
      sed -n '1,40p' "$0"
      exit 0
      ;;
    *) echo "ERR: unknown flag: $1" >&2; exit 2 ;;
  esac
  shift
done

LABEL_KEY="catalyst.openova.io/sovereign"
TS=$(date -u +%Y%m%dT%H%M%SZ)
LOG_FILE="/tmp/hcloud-purge-${TS}.log"

# ── Output helpers ────────────────────────────────────────────────────

bold()   { printf "\033[1m%s\033[0m\n" "$*"; }
red()    { printf "\033[31m%s\033[0m\n" "$*"; }
green()  { printf "\033[32m%s\033[0m\n" "$*"; }
yellow() { printf "\033[33m%s\033[0m\n" "$*"; }
cyan()   { printf "\033[36m%s\033[0m\n" "$*"; }

log() {
  # Append plain text to log file; print to stderr in cyan.
  local msg="$*"
  printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$msg" >> "$LOG_FILE"
}

prefix() {
  if [ "$MODE" = "dry-run" ]; then
    yellow "[DRY-RUN] $*"
  else
    cyan "[APPLY]   $*"
  fi
  log "$*"
}

# ── Banner ────────────────────────────────────────────────────────────

bold "==================================================================="
bold "  OpenOva Catalyst — Hetzner project purge"
bold "==================================================================="
if [ -n "$FQDN" ]; then
  echo "  Scope:             single Sovereign — $FQDN"
  echo "  Label selector:    ${LABEL_KEY}=${FQDN}"
else
  echo "  Scope:             ENTIRE PROJECT — every Catalyst-tagged resource"
  echo "  Label selector:    ${LABEL_KEY}  (any value)"
fi
echo "  Mode:              $MODE"
echo "  Assume-yes:        $([ "$ASSUME_YES" = "1" ] && echo "yes" || echo "no — will confirm per resource")"
echo "  Log file:          $LOG_FILE"
bold "==================================================================="
echo
log "Banner: scope=$([ -n "$FQDN" ] && echo "$FQDN" || echo project-wide) mode=$MODE"

if [ "$MODE" = "dry-run" ]; then
  yellow "  Running in DRY-RUN mode. Nothing will be deleted."
  yellow "  Re-run with --apply to actually purge resources."
else
  red "  Running in APPLY mode. Resources WILL be deleted. CTRL-C now to abort."
  if [ "$ASSUME_YES" = "0" ]; then
    sleep 3
  fi
fi
echo

# ── Pre-flight ────────────────────────────────────────────────────────

HAVE_TOKEN=0
if [ -n "${HETZNER_API_TOKEN:-}" ]; then
  HTTP_CODE=$(curl -sS -o /dev/null -w '%{http_code}' \
    -H "Authorization: Bearer ${HETZNER_API_TOKEN}" \
    "https://api.hetzner.cloud/v1/servers?per_page=1" || true)
  if [ "$HTTP_CODE" = "200" ]; then
    HAVE_TOKEN=1
    green "  HETZNER_API_TOKEN validated."
    log "Token validated (HTTP 200)"
  else
    if [ "$MODE" = "apply" ]; then
      red "ERR: HETZNER_API_TOKEN rejected by Hetzner API (HTTP $HTTP_CODE). Aborting."
      log "Token rejected (HTTP $HTTP_CODE) — abort"
      exit 3
    else
      yellow "  HETZNER_API_TOKEN rejected (HTTP $HTTP_CODE) — dry-run will be name-only preview."
      log "Token rejected (HTTP $HTTP_CODE) — dry-run preview only"
    fi
  fi
else
  if [ "$MODE" = "apply" ]; then
    red "ERR: HETZNER_API_TOKEN is not set. Export the project token and re-run."
    log "Token missing — abort"
    exit 3
  else
    yellow "  HETZNER_API_TOKEN is not set — dry-run will be a name-only preview."
    log "Token missing — dry-run preview only"
  fi
fi
echo

# ── Helpers: list & delete ────────────────────────────────────────────

H="Authorization: Bearer ${HETZNER_API_TOKEN:-NO_TOKEN}"

# Build the label selector. With --fqdn we pin to one value; otherwise
# we use the bare key, which matches any value (Hetzner's documented
# "key exists" semantics).
if [ -n "$FQDN" ]; then
  SEL="label_selector=${LABEL_KEY}=${FQDN}"
else
  SEL="label_selector=${LABEL_KEY}"
fi

# Resources that support label selectors.
KINDS_LABELED="servers load_balancers networks firewalls volumes primary_ips floating_ips"

# Resources that must be matched by name slug instead (Hetzner doesn't
# expose label selectors on these list endpoints).
KINDS_BYNAME="ssh_keys"

list_ids_labelled() {
  local kind="$1"
  curl -sS -H "$H" "https://api.hetzner.cloud/v1/${kind}?${SEL}" |
    python3 -c "
import json, sys
try:
    d = json.load(sys.stdin)
except Exception:
    sys.exit(0)
for s in d.get('${kind}', []):
    print(s['id'], s.get('name', ''))
"
}

list_ids_by_name() {
  # The deterministic slug used by infra/hetzner/main.tf is
  # 'catalyst-<replace(fqdn, ".", "-")>-<role>'. We match prefix
  # 'catalyst-' so we catch every Catalyst-named resource regardless
  # of FQDN. With --fqdn the slug filter is tightened.
  local kind="$1"
  local needle="catalyst-"
  if [ -n "$FQDN" ]; then
    needle="catalyst-$(echo "$FQDN" | tr . -)-"
  fi
  curl -sS -H "$H" "https://api.hetzner.cloud/v1/${kind}" |
    python3 -c "
import json, sys
needle = '${needle}'
try:
    d = json.load(sys.stdin)
except Exception:
    sys.exit(0)
for s in d.get('${kind}', []):
    name = s.get('name', '')
    if name.startswith(needle):
        print(s['id'], name)
"
}

confirm_or_skip() {
  # Returns 0 to proceed, 1 to skip. Auto-yes when -y is set or in dry-run.
  if [ "$MODE" = "dry-run" ] || [ "$ASSUME_YES" = "1" ]; then
    return 0
  fi
  read -r -p "    Delete? [y/N] " ans
  case "$ans" in
    y|Y|yes|YES) return 0 ;;
    *) return 1 ;;
  esac
}

delete_resource() {
  local kind="$1" id="$2" name="$3"
  prefix "DELETE ${kind}/${id} (${name})"
  if [ "$MODE" = "apply" ]; then
    if ! confirm_or_skip; then
      yellow "    skipped"
      log "Skipped ${kind}/${id} (${name}) — operator declined"
      return 0
    fi
    HTTP_CODE=$(curl -sS -o /dev/null -w '%{http_code}' \
      -X DELETE -H "$H" "https://api.hetzner.cloud/v1/${kind}/${id}" || echo "000")
    case "$HTTP_CODE" in
      200|202|204|404)
        green "    ok (HTTP ${HTTP_CODE})"
        log "Deleted ${kind}/${id} (${name}) — HTTP ${HTTP_CODE}"
        ;;
      *)
        red "    WARN: HTTP ${HTTP_CODE}"
        log "WARN delete ${kind}/${id} returned HTTP ${HTTP_CODE}"
        return 1
        ;;
    esac
  fi
  return 0
}

# ── Step 1 — label-selected pass (dependency order) ───────────────────

bold "── Step 1 / 3 — label-selected pass ──────────────────────────────"
ANY_FOUND=0
EXIT_CODE=0

if [ "$HAVE_TOKEN" = "1" ]; then
  for kind in $KINDS_LABELED ; do
    while IFS= read -r line; do
      [ -z "$line" ] && continue
      ANY_FOUND=1
      id=$(echo "$line" | awk '{print $1}')
      name=$(echo "$line" | cut -d' ' -f2-)
      delete_resource "$kind" "$id" "$name" || EXIT_CODE=1
    done < <(list_ids_labelled "$kind")
  done
else
  yellow "  (skipped — no token available)"
  for kind in $KINDS_LABELED ; do
    prefix "QUERY  ${kind}?${SEL}  (would DELETE any matches)"
  done
fi
echo

# ── Step 2 — name-slug pass (ssh_keys + label-less safety net) ────────

bold "── Step 2 / 3 — name-slug pass ───────────────────────────────────"
if [ "$HAVE_TOKEN" = "1" ]; then
  # ssh_keys must always go through name match.
  for kind in $KINDS_BYNAME ; do
    while IFS= read -r line; do
      [ -z "$line" ] && continue
      ANY_FOUND=1
      id=$(echo "$line" | awk '{print $1}')
      name=$(echo "$line" | cut -d' ' -f2-)
      delete_resource "$kind" "$id" "$name" || EXIT_CODE=1
    done < <(list_ids_by_name "$kind")
  done
else
  yellow "  (skipped — no token available)"
  for kind in $KINDS_BYNAME ; do
    prefix "QUERY  ${kind}  (would DELETE any name starting with catalyst-)"
  done
fi
echo

# ── Step 3 — verification sweep ───────────────────────────────────────
#
# Hetzner DELETE often returns 204 even when the resource persists
# (especially firewalls right after a server delete). Re-query without
# the label filter, by-name, and re-delete any lingerers. Skipping this
# caused a "name is already used (uniqueness_error)" failure on the very
# next provision attempt — see feedback_idempotent_iac_purge.md.

bold "── Step 3 / 3 — verification sweep ───────────────────────────────"
if [ "$HAVE_TOKEN" = "1" ]; then
  LINGER=0
  for kind in firewalls networks load_balancers ssh_keys servers volumes primary_ips floating_ips ; do
    while IFS= read -r line; do
      [ -z "$line" ] && continue
      LINGER=1
      id=$(echo "$line" | awk '{print $1}')
      name=$(echo "$line" | cut -d' ' -f2-)
      yellow "  Lingering ${kind}/${id} (${name}) — re-deleting"
      delete_resource "$kind" "$id" "$name" || EXIT_CODE=1
    done < <(list_ids_by_name "$kind")
  done
  if [ "$LINGER" = "0" ]; then
    green "  No lingering Catalyst-named resources. Clean."
    log "Verification sweep clean"
  fi
else
  yellow "  (skipped — no token available)"
fi
echo

# ── Step 4 (forward-compat) — Object Storage buckets ──────────────────
#
# #371 will start provisioning Hetzner Object Storage buckets per
# Sovereign for Harbor + Velero. Today the OpenTofu module does NOT
# create buckets, so this step is a no-op against the current project.
# Wiring it now means once #371 lands, this runbook already cleans up
# bucket orphans without needing a re-edit. Hetzner Object Storage is
# S3-compatible but the Hetzner Cloud API does not (yet, as of cutoff)
# expose bucket lifecycle — bucket deletion goes through the project's
# S3 endpoint with the project's S3 credentials. We skip it here with a
# clear message so the operator knows it isn't auto-handled.

bold "── Step 4 / 4 — Object Storage buckets (forward-compat) ──────────"
yellow "  Hetzner Object Storage bucket lifecycle is NOT exposed via the"
yellow "  Hetzner Cloud API. Once #371 starts auto-provisioning buckets,"
yellow "  delete them via the S3 endpoint:"
yellow "    aws s3 rb s3://catalyst-<fqdn-slug>-* --force \\"
yellow "       --endpoint-url https://<region>.your-objectstorage.com"
yellow "  using the project S3 credentials (separate from HETZNER_API_TOKEN)."
yellow "  Today (#371 not done): no buckets exist. Nothing to do."
echo

# ── Final verification ────────────────────────────────────────────────

bold "── Final verification — list every resource type ─────────────────"
if [ "$HAVE_TOKEN" = "1" ] && [ "$MODE" = "apply" ]; then
  for kind in servers load_balancers networks firewalls volumes primary_ips floating_ips ssh_keys ; do
    COUNT=$(curl -sS -H "$H" "https://api.hetzner.cloud/v1/${kind}?per_page=1" |
      python3 -c "
import json, sys
try:
    d = json.load(sys.stdin)
except Exception:
    print('?')
    sys.exit(0)
print(d.get('meta', {}).get('pagination', {}).get('total_entries', '?'))
")
    cyan "  ${kind}: ${COUNT} total in project"
    log "Final count ${kind}=${COUNT}"
  done
fi
echo

# ── Done ──────────────────────────────────────────────────────────────

bold "==================================================================="
if [ "$MODE" = "dry-run" ]; then
  yellow "  DRY-RUN complete. Re-run with --apply to actually purge."
elif [ "$EXIT_CODE" = "0" ]; then
  if [ "$ANY_FOUND" = "0" ]; then
    green "  PURGE COMPLETE — nothing to delete (project was already clean)."
  else
    green "  PURGE COMPLETE — resources removed."
  fi
  green "  Log file: $LOG_FILE"
else
  red "  PURGE PARTIAL — some deletes returned non-2xx. See log: $LOG_FILE"
fi
bold "==================================================================="

exit $EXIT_CODE
