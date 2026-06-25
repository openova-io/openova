#!/usr/bin/env bash
# bp-cnpg — G64 (#4322) render gate for the durable webhook-caBundle fix.
#
# The cnpg-system operator corrupts the cluster-singleton webhook caBundle with
# a LEAF cert (CA:FALSE) instead of the cnpg-ca-secret CA (CA:TRUE) — upstream
# cloudnative-pg #9817 (fix PR #9819 unmerged) — and re-asserts it hourly, so
# every postgresql.cnpg.io/v1.Cluster create/patch is rejected cluster-wide the
# moment the served leaf and the stale caBundle-leaf diverge. The durable fix:
#   (1) cloudnative-pg.config.data.MANAGE_WEBHOOK_CONFIGURATIONS="false" stops
#       the operator injecting (corrupting) the caBundle at the source.
#   (2) templates/webhook-cabundle-reasserter.yaml writes the real CA from
#       cnpg-ca-secret into both webhook configs (post-install Job + 10m CronJob).
#   (3) webhook-gate-hook.yaml asserts the caBundle is a CA (CA:TRUE), not merely
#       non-empty.
#
# This test asserts (helm template only — no live cluster):
#   A. DEFAULT (webhook-FULL platform install):
#      - operator config ConfigMap carries MANAGE_WEBHOOK_CONFIGURATIONS: "false"
#      - reasserter Job + CronJob + RBAC render, flux-managed labelled
#      - reasserter Job hook-weight (4) < webhook-gate Job hook-weight (5)
#      - reasserter script refuses to write a non-CA (CA:TRUE guard present)
#      - the webhook-gate asserts CA:TRUE (not just non-empty length)
#   B. WEBHOOK-LESS (per-Org install): ZERO reasserter resources.
#   C. EXPLICIT webhookCabundleReasserter.enabled=false: ZERO reasserter, gate stays.
#
# Usage: bash tests/webhook-cabundle-reasserter.sh [CHART_DIR]
set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

if [ ! -d "$CHART_DIR/charts" ] || ! ls "$CHART_DIR"/charts/cloudnative-pg-*.tgz >/dev/null 2>&1; then
  helm dependency build "$CHART_DIR" >/dev/null 2>&1 || true
fi

render() {
  local out="$1"; shift
  helm template bp-cnpg "$CHART_DIR" --namespace cnpg-system "$@" > "$out" 2>"$TMP/err" \
    || { echo "helm template FAILED:"; cat "$TMP/err"; exit 1; }
}
fail() { echo "FAIL: $1"; exit 1; }

# Count ACTUAL reasserter resources (object names), NOT mere substring mentions —
# the webhook-gate Job's diagnostic echo lines reference "webhook-cabundle-
# reasserter", so a loose grep would false-positive. A real resource declares
# `name: bp-cnpg-cabundle-reassert` (the Job/CronJob) or
# `name: bp-cnpg-cabundle-reasserter` (the SA/Role/etc.) at metadata indent.
reasserter_resource_count() { grep -cE '^\s+name: bp-cnpg-cabundle-reassert(er)?\s*$' "$1" || true; }

# ── Case A: DEFAULT — webhook-FULL platform install ─────────────────────────
render "$TMP/full.yaml"

# A1. operator stops managing (corrupting) the caBundle
if ! grep -qE '^\s*MANAGE_WEBHOOK_CONFIGURATIONS:\s*"false"' "$TMP/full.yaml"; then
  fail "operator config is missing MANAGE_WEBHOOK_CONFIGURATIONS: \"false\" (operator would keep corrupting the caBundle)"
fi
echo "PASS: operator config sets MANAGE_WEBHOOK_CONFIGURATIONS=false"

# A2. reasserter resources render
for kind in "kind: Job" "kind: CronJob"; do
  awk -v k="$kind" 'BEGIN{RS="\n---\n"} $0 ~ k && /cabundle-reassert/ {found=1} END{exit !found}' "$TMP/full.yaml" \
    || fail "default render is missing the reasserter ${kind#kind: } (cabundle-reassert)"
done
grep -q "bp-cnpg-cabundle-reasserter" "$TMP/full.yaml" || fail "reasserter RBAC (SA/Role/ClusterRole) missing"
echo "PASS: reasserter Job + CronJob + RBAC render in webhook-FULL mode"

# A3. CronJob schedule is faster than the operator's 1h corruption tick
grep -qE 'schedule:\s*"\*/[0-9]+ \* \* \* \*"' "$TMP/full.yaml" \
  || fail "reasserter CronJob has no sub-hourly schedule (must out-pace the operator's @every 1h corruption)"
echo "PASS: reasserter CronJob has a sub-hourly self-heal schedule"

# A4. reasserter Job + its CronJob are flux-managed (else kyverno denies them)
#     count the managed-by:flux occurrences on the two reasserter workloads.
flux_labels="$(awk 'BEGIN{RS="\n---\n"} /cabundle-reassert/ && /app.kubernetes.io\/managed-by: flux/ && (/kind: Job/||/kind: CronJob/){c++} END{print c+0}' "$TMP/full.yaml")"
[ "$flux_labels" -ge 2 ] || fail "reasserter Job/CronJob missing app.kubernetes.io/managed-by: flux (got $flux_labels, need >=2; kyverno flux-managed would DENY them)"
echo "PASS: reasserter Job + CronJob carry managed-by: flux"

# A5. ordering — reasserter Job (weight 4) runs BEFORE the gate Job (weight 5)
#     so the CA caBundle lands before the gate validates CA:TRUE. Parse with
#     python (portable across awk variants; both reasserter Job+CronJob share a
#     name, so key on kind==Job).
read -r reassert_w gate_w < <(python3 - "$TMP/full.yaml" <<'PY'
import sys, yaml
rw=gw=""
for d in yaml.safe_load_all(open(sys.argv[1])):
    if not d or d.get("kind")!="Job": continue
    name=(d.get("metadata") or {}).get("name","")
    w=((d.get("metadata") or {}).get("annotations") or {}).get("helm.sh/hook-weight","")
    if name=="bp-cnpg-cabundle-reassert": rw=w
    if name=="bp-cnpg-webhook-gate":      gw=w
print(rw or "NA", gw or "NA")
PY
)
[ "$reassert_w" != "NA" ] && [ "$gate_w" != "NA" ] || fail "could not read hook-weights (reassert='$reassert_w' gate='$gate_w')"
[ "$reassert_w" -lt "$gate_w" ] || fail "reasserter hook-weight ($reassert_w) must be < gate hook-weight ($gate_w) so the CA lands before the gate validates it"
echo "PASS: reasserter Job (weight $reassert_w) runs before the gate Job (weight $gate_w)"

# A6. reasserter REFUSES to write a non-CA (the CA:TRUE guard must be in-script)
grep -q "CA:TRUE" "$TMP/full.yaml" || fail "reasserter/gate script lost the CA:TRUE assertion — could write a leaf caBundle"
grep -q "refusing to write a non-CA caBundle" "$TMP/full.yaml" \
  || fail "reasserter script lost its non-CA refusal guard"
echo "PASS: reasserter refuses to write a non-CA caBundle (CA:TRUE guard present)"

# A7. the webhook-gate asserts the caBundle is a CA, not just non-empty
grep -q "caBundle is a CA" "$TMP/full.yaml" \
  || fail "webhook-gate no longer asserts the caBundle is a CA (regressed to non-empty-only)"
if grep -qE 'mut_ca_len.*-gt 100' "$TMP/full.yaml"; then
  fail "webhook-gate still uses the OLD non-empty length check (mut_ca_len -gt 100) — should assert CA:TRUE"
fi
echo "PASS: webhook-gate asserts caBundle is a CA (CA:TRUE), not merely non-empty"

# ── Case B: WEBHOOK-LESS — per-Org install → ZERO reasserter ────────────────
render "$TMP/less.yaml" \
  --set "cloudnative-pg.webhook.mutating.create=false" \
  --set "cloudnative-pg.webhook.validating.create=false"
n="$(reasserter_resource_count "$TMP/less.yaml")"
if [ "$n" -ne 0 ]; then
  echo "--- offending ---"; grep -nE '^\s+name: bp-cnpg-cabundle-reassert(er)?\s*$' "$TMP/less.yaml" | head
  fail "webhook-less render STILL emits $n reasserter resource(s) (flux-managed would deny the un-owned hook Job; per-Org owns no cluster webhook anyway)"
fi
echo "PASS: webhook-less render emits ZERO reasserter resources"

# ── Case C: EXPLICIT reasserter off → no reasserter, gate preserved ─────────
render "$TMP/reassert-off.yaml" --set "webhookCabundleReasserter.enabled=false"
n="$(reasserter_resource_count "$TMP/reassert-off.yaml")"
[ "$n" -eq 0 ] || fail "webhookCabundleReasserter.enabled=false STILL emits $n reasserter resource(s)"
grep -q "bp-cnpg-webhook-gate" "$TMP/reassert-off.yaml" \
  || fail "disabling the reasserter must NOT remove the webhook-gate"
echo "PASS: webhookCabundleReasserter.enabled=false suppresses the reasserter, keeps the gate"

echo "All bp-cnpg webhook-cabundle-reasserter assertions passed (G64 #4322)."
