#!/usr/bin/env bash
# sovereign-3x-zero-touch-cycle.sh — G104 (Refs #2671) zero-touch
# certification gate. Drives 3 consecutive provision→verify→wipe
# cycles against the mothership catalyst-api and certifies the
# Sovereign topology IFF every step of every cycle passes WITHOUT
# manual intervention on tracked resources.
#
# Founder mandate (#2671):
#   "ensure 3 in a row correct provisioning and again while wiping
#    ensure correct garbage collections"
#
# What "zero-touch" means:
#   - The verifier never runs `kubectl set/patch/edit/rollout restart`
#     against any of the 86-check tracked resources between Phase 1
#     ready and the verifier's pass verdict.
#   - The wipe step must hit VerifiedZeroOrphans=true (G103 #2670)
#     each time — no residual catalyst-* resources besides bastion-*.
#   - The full cycle must run end-to-end via this single script, no
#     operator hand-holding.
#
# Usage:
#   ./scripts/sovereign-3x-zero-touch-cycle.sh \
#       <provider> <primary-region> <secondary-region>
#
# Examples:
#   # canonical HCS 2-region:
#   ./scripts/sovereign-3x-zero-touch-cycle.sh \
#       huawei me-east-215-a me-east-215-b
#
#   # canonical Hetzner 2-region:
#   ./scripts/sovereign-3x-zero-touch-cycle.sh \
#       hetzner fsn1 hel1
#
# Pre-reqs (the script bails fast if any are missing):
#   - jq, curl, python3 on the bastion
#   - SOVEREIGN_API_URL env (default: https://console.openova.io)
#   - SOVEREIGN_API_TOKEN env (handover JWT for /sovereign/api/v1/*)
#   - sovereign-dod-verify.sh in the same scripts/ dir
#   - kubectl context = mothership (the dod-verify script needs this)
#
# Exit codes:
#   0  CERTIFIED — all 3 cycles GREEN, all 3 wipes zero-orphan.
#   1  FAILED  — at least one cycle / wipe failed; per-cycle diagnostics
#                printed to stdout. The verifier surfaces:
#                  - Which cycle failed at which step
#                  - The orphans left behind (if a wipe failed)
#                  - The dod-verify summary (if a verify failed)
#                Re-run from cycle 1 only after operator-fix.
#   2  USAGE / pre-req error.
#
# Per the G104 acceptance contract (#2671), this script is the canonical
# certification surface for a topology. A topology is not "ready for
# customers" until this script returns 0 on a fresh prov.

set -uo pipefail

# ── Args + pre-flight ────────────────────────────────────────────────
PROVIDER="${1:-}"
PRIMARY_REGION="${2:-}"
SECONDARY_REGION="${3:-}"

if [[ -z "$PROVIDER" || -z "$PRIMARY_REGION" || -z "$SECONDARY_REGION" ]]; then
    cat >&2 <<EOF
G104 3xZT verifier (Refs #2671)

Usage:
  $0 <provider> <primary-region> <secondary-region>

Examples:
  $0 huawei me-east-215-a me-east-215-b
  $0 hetzner fsn1 hel1
EOF
    exit 2
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DOD_VERIFY="${SCRIPT_DIR}/sovereign-dod-verify.sh"
if [[ ! -x "$DOD_VERIFY" ]]; then
    echo "ERROR: missing or non-executable companion script: $DOD_VERIFY" >&2
    exit 2
fi

SOVEREIGN_API_URL="${SOVEREIGN_API_URL:-https://console.openova.io}"
if [[ -z "${SOVEREIGN_API_TOKEN:-}" ]]; then
    echo "ERROR: SOVEREIGN_API_TOKEN env is required (mothership handover JWT)" >&2
    exit 2
fi

for tool in jq curl python3 kubectl; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        echo "ERROR: required tool missing on PATH: $tool" >&2
        exit 2
    fi
done

# ── State tracking ───────────────────────────────────────────────────
declare -a CYCLE_RESULTS  # "PASS" / "FAIL: <step>"
declare -a CYCLE_DEP_IDS  # per-cycle deployment id
declare -a CYCLE_WIPE_JSON  # per-cycle wipe response (for diagnostics)

# Wall-clock budgets per step. Tuned to the canonical fresh-prov
# observed times across hw30+ provs (Phase 0 + Phase 1 + cutover total
# ~25 min on healthy mothership). Operator can override via env.
PROV_TIMEOUT_S="${PROV_TIMEOUT_S:-2400}"   # 40m — Phase 0 + Phase 1 + cutover
WIPE_TIMEOUT_S="${WIPE_TIMEOUT_S:-1800}"   # 30m — destroy + provider purge + verify
POLL_INTERVAL_S="${POLL_INTERVAL_S:-30}"

# Default Org metadata for the 3 throwaway provs. The dod-verify
# contract requires real domains so the wildcard cert chain resolves;
# follow the canonical test-Sovereign domains (per
# feedback_canonical_end_user_dod memory): t<NN>.omani.works rotated
# with omantel.biz when LE rate-limits.
PARENT_DOMAIN="${PARENT_DOMAIN:-omani.works}"
ORG_PREFIX="${ORG_PREFIX:-g104zt}"

# ── Helper: human-time stamp for log lines ──────────────────────────
ts() { date -u +"%Y-%m-%dT%H:%M:%SZ"; }
log() { echo "[$(ts)] $*"; }

# ── Helper: POST a fresh deployment ──────────────────────────────────
#
# Returns the deployment id on stdout. Body shape mirrors the wizard's
# StepProvider payload (provider + 2 regions + canonical default SKUs)
# so the catalyst-api side runs through the exact same Validate +
# writeTfvars + Provision path a real install does.
post_deployment() {
    local cycle_n="$1"
    local sov_fqdn="${ORG_PREFIX}-${cycle_n}.${PARENT_DOMAIN}"
    local org_name="G104ZT Cycle ${cycle_n}"
    local org_email="ops+g104zt-${cycle_n}@${PARENT_DOMAIN}"

    log "POST /sovereign/api/v1/deployments — cycle ${cycle_n} (fqdn=${sov_fqdn})"

    # SKU defaults match the canonical multi-region test topology
    # (Tier-B 2-region). Operator can override via env.
    local primary_cp_sku="${PROV_CP_SKU:-cpx32}"
    local primary_w_sku="${PROV_W_SKU:-cpx32}"
    if [[ "$PROVIDER" == "huawei" ]]; then
        primary_cp_sku="${PROV_CP_SKU:-s7n.xlarge.2}"
        primary_w_sku="${PROV_W_SKU:-s7n.xlarge.2}"
    fi

    local body
    body=$(jq -n \
        --arg fqdn "$sov_fqdn" \
        --arg org "$org_name" \
        --arg mail "$org_email" \
        --arg provider "$PROVIDER" \
        --arg primary "$PRIMARY_REGION" \
        --arg secondary "$SECONDARY_REGION" \
        --arg cp "$primary_cp_sku" \
        --arg w "$primary_w_sku" \
        '{
            sovereignFQDN: $fqdn,
            sovereignDomainMode: "pool",
            sovereignPoolDomain: $primary,
            orgName: $org,
            orgEmail: $mail,
            provider: $provider,
            haEnabled: false,
            marketplaceEnabled: true,
            bcpTopology: "active-hotstandby",
            regions: [
                {provider: $provider, cloudRegion: $primary,   controlPlaneSize: $cp, workerSize: $w, workerCount: 2},
                {provider: $provider, cloudRegion: $secondary, controlPlaneSize: $cp, workerSize: $w, workerCount: 2}
            ]
        }')

    local resp
    resp=$(curl --silent --show-error --max-time 60 \
        -X POST \
        -H "Authorization: Bearer ${SOVEREIGN_API_TOKEN}" \
        -H "Content-Type: application/json" \
        --data "$body" \
        "${SOVEREIGN_API_URL}/sovereign/api/v1/deployments") || {
        log "  curl POST failed"
        return 1
    }

    local dep_id
    dep_id=$(echo "$resp" | jq -r '.id // empty')
    if [[ -z "$dep_id" ]]; then
        log "  catalyst-api rejected the POST body:"
        echo "$resp" | head -20 >&2
        return 1
    fi
    log "  deployment id: ${dep_id}"
    echo "$dep_id"
}

# ── Helper: wait for provision to reach Phase 1 ready + cutover ─────
wait_for_ready() {
    local dep_id="$1"
    local waited=0
    log "polling /deployments/${dep_id} until phase1Outcome=ready + cutoverComplete=true (max ${PROV_TIMEOUT_S}s)"
    while (( waited < PROV_TIMEOUT_S )); do
        local snap
        snap=$(curl --silent --max-time 20 \
            -H "Authorization: Bearer ${SOVEREIGN_API_TOKEN}" \
            "${SOVEREIGN_API_URL}/sovereign/api/v1/deployments/${dep_id}") || {
            sleep "$POLL_INTERVAL_S"
            waited=$((waited + POLL_INTERVAL_S))
            continue
        }
        local phase1
        phase1=$(echo "$snap" | jq -r '.result.phase1Outcome // .phase1Outcome // empty')
        local cutover
        cutover=$(echo "$snap" | jq -r '.cutoverComplete // .result.cutoverComplete // false')
        local status
        status=$(echo "$snap" | jq -r '.status // empty')
        log "  status=${status} phase1=${phase1} cutoverComplete=${cutover} (t=${waited}s)"

        if [[ "$phase1" == "ready" && "$cutover" == "true" ]]; then
            log "  REACHED ready+cutover at t=${waited}s"
            return 0
        fi
        if [[ "$phase1" == "failed" || "$phase1" == "timeout" || "$phase1" == "flux-not-reconciling" ]]; then
            log "  Phase 1 reached terminal-fail state: ${phase1}"
            return 1
        fi
        sleep "$POLL_INTERVAL_S"
        waited=$((waited + POLL_INTERVAL_S))
    done
    log "  TIMEOUT — Phase 1 did not converge within ${PROV_TIMEOUT_S}s"
    return 1
}

# ── Helper: run sovereign-dod-verify.sh + capture verdict ───────────
run_dod_verify() {
    local dep_id="$1"
    log "running sovereign-dod-verify.sh ${dep_id}"
    if "$DOD_VERIFY" "$dep_id"; then
        log "  DoD verify GREEN"
        return 0
    fi
    log "  DoD verify FAILED (see above for per-check ❌ lines)"
    return 1
}

# ── Helper: POST /wipe + assert VerifiedZeroOrphans=true ────────────
run_wipe_with_zero_orphan_gate() {
    local dep_id="$1"
    log "POST /deployments/${dep_id}/wipe (G103 zero-orphan gate)"
    local resp
    resp=$(curl --silent --show-error --max-time "$WIPE_TIMEOUT_S" \
        -X POST \
        -H "Authorization: Bearer ${SOVEREIGN_API_TOKEN}" \
        "${SOVEREIGN_API_URL}/sovereign/api/v1/deployments/${dep_id}/wipe?force=true") || {
        log "  curl POST /wipe failed"
        return 1
    }

    CYCLE_WIPE_JSON+=("$resp")

    local verified
    verified=$(echo "$resp" | jq -r '.verifiedZeroOrphans // false')
    local tofu
    tofu=$(echo "$resp" | jq -r '.tofuDestroyed // false')
    local err_count
    err_count=$(echo "$resp" | jq -r '.errors // [] | length')

    log "  tofuDestroyed=${tofu} verifiedZeroOrphans=${verified} errors=${err_count}"

    if [[ "$verified" != "true" ]]; then
        log "  G103 contract VIOLATED — ResidualOrphans:"
        echo "$resp" | jq '.residualOrphans // {}' | head -30
        return 1
    fi
    log "  G103 contract HONOURED (zero orphans)"
    return 0
}

# ── Cycle driver ─────────────────────────────────────────────────────
run_cycle() {
    local n="$1"
    log "════════════════════════════════════════════════════════"
    log "  G104 zero-touch cycle ${n}/3 START"
    log "════════════════════════════════════════════════════════"

    local dep_id
    dep_id=$(post_deployment "$n") || {
        CYCLE_RESULTS+=("FAIL: POST deployment")
        CYCLE_DEP_IDS+=("")
        return 1
    }
    CYCLE_DEP_IDS+=("$dep_id")

    if ! wait_for_ready "$dep_id"; then
        CYCLE_RESULTS+=("FAIL: Phase 1 did not converge (dep=${dep_id})")
        return 1
    fi

    if ! run_dod_verify "$dep_id"; then
        CYCLE_RESULTS+=("FAIL: DoD verify (dep=${dep_id})")
        return 1
    fi

    if ! run_wipe_with_zero_orphan_gate "$dep_id"; then
        CYCLE_RESULTS+=("FAIL: zero-orphan wipe (dep=${dep_id})")
        return 1
    fi

    CYCLE_RESULTS+=("PASS")
    log "════════════════════════════════════════════════════════"
    log "  G104 zero-touch cycle ${n}/3 GREEN (dep=${dep_id})"
    log "════════════════════════════════════════════════════════"
    return 0
}

# ── Main ─────────────────────────────────────────────────────────────
log "G104 zero-touch certification — provider=${PROVIDER} primary=${PRIMARY_REGION} secondary=${SECONDARY_REGION}"
log "  PROV_TIMEOUT_S=${PROV_TIMEOUT_S}  WIPE_TIMEOUT_S=${WIPE_TIMEOUT_S}  POLL_INTERVAL_S=${POLL_INTERVAL_S}"

overall=0
for n in 1 2 3; do
    if ! run_cycle "$n"; then
        overall=1
        log "ABORTING — cycle ${n} failed; re-run from cycle 1 after operator-fix"
        break
    fi
done

echo
echo "═══════════════════════════════════════════════════════════════"
echo "G104 zero-touch certification SUMMARY"
echo "═══════════════════════════════════════════════════════════════"
for i in "${!CYCLE_RESULTS[@]}"; do
    n=$((i + 1))
    printf "  Cycle %d (dep=%s): %s\n" "$n" "${CYCLE_DEP_IDS[$i]:-n/a}" "${CYCLE_RESULTS[$i]}"
done
echo

if [[ "$overall" -eq 0 && "${#CYCLE_RESULTS[@]}" -eq 3 ]]; then
    echo "✅ CERTIFIED — 3/3 zero-touch cycles GREEN, 3/3 wipes zero-orphan."
    echo "   Topology ${PROVIDER}/${PRIMARY_REGION}+${SECONDARY_REGION} is ready for customers."
    exit 0
fi

echo "❌ NOT CERTIFIED — at least one cycle failed."
echo "   Operator-fix the surfaced issue, then re-run from cycle 1."
exit 1
