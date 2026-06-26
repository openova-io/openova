#!/usr/bin/env bash
# bp-catalyst-platform — gitea-flux-auth-sync hook RBAC-ordering + fail-loud gate (#4447).
#
# The post-install hook templates/catalog-sovereign-flux/gitea-flux-auth-secrets-sync-job.yaml
# is the SOLE creator of the two Flux basic-auth Secrets the catalog-sovereign +
# org-tenants GitRepository sources authenticate with
# (openova-catalog-sovereign-git-auth + openova-org-tenants-git-auth). On the
# fresh prov b9f9590b558262b6 (omantel.biz, 2026-06-26) it exit-0'd having
# created NEITHER secret, so both sources stuck "Source artifact not found" and
# the whole catalog (→ every per-Org provisioning) never converged — Pillar-1
# blocked, manual secret-recreation required (NOT zero-touch).
#
# Two root causes (both guarded here):
#
#   1. RBAC RACE. The Job's pat-reader Role/RoleBinding (the grant that lets it
#      read the minted PAT) was hook-weight 20 — the SAME weight as the Job. Helm
#      applies a same-weight hook batch together with NO ordering guarantee, so
#      the Job pod frequently started polling before its RoleBinding bound → the
#      `kubectl get secret catalyst-gitea-token` read was a silent 403 for the
#      entire poll budget. FIX: the SA + all four RBAC objects are hook-weight 15
#      (strictly < the Job's 20), so Helm installs+waits the RBAC batch Ready
#      BEFORE the Job batch.
#
#   2. SILENT-SWALLOW + exit-0-on-nothing. The PAT read used `2>/dev/null … ||
#      true`, making a 403 indistinguishable from an empty token; the budget then
#      exhausted and the FATAL branch `exit 0`'d unconditionally — reporting
#      Succeeded while creating ZERO secrets. FIX: the read now captures the
#      kubectl exit code; a budget exhausted with ANY read error fails LOUD
#      (exit 1, retry via backoffLimit); only a CLEAN read of an empty token is
#      treated as the benign mint-race (exit 0, self-heals). A post-apply
#      read-back verifies both Secrets exist with a non-empty password.
#
# Nothing in CI rendered this hook and asserted the RBAC ordering, nor exercised
# the read-error-vs-empty branch. This gate fills that hole: a regression that
# re-collapses the RBAC weight back to 20, or re-introduces a blanket exit-0,
# fails Blueprint Release publish BEFORE the OCI artifact reaches a Sovereign.
#
# Test framework: `helm template` + grep on rendered YAML for the weights, plus
# a behavioral drive of the rendered container script against a stub `kubectl`
# (matches the established tests/sovereign-fqdn-lb-ip-contract.sh pattern; picked
# up automatically by .github/workflows/blueprint-release.yaml).
#
# Usage: bash tests/gitea-flux-auth-sync-rbac-order.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

TEMPLATE="templates/catalog-sovereign-flux/gitea-flux-auth-secrets-sync-job.yaml"

# The hook is gated on a Sovereign (global.sovereignFQDN) + marketplace enabled.
helm template smoke . \
  --set global.sovereignFQDN=omantel.biz \
  --set ingress.marketplace.enabled=true \
  --show-only "${TEMPLATE}" > "$TMP/hook.yaml"

# ── Gate 1: RBAC strictly precedes the Job ────────────────────────────────────
# Split the rendered manifest into per-document files and inspect hook-weight by
# kind/name so we assert the SA + RBAC are 15 and the Job is 20.
python3 - "$TMP/hook.yaml" <<'PY'
import sys, yaml
docs = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
by = {}
for d in docs:
    k = d.get("kind")
    name = d.get("metadata", {}).get("name", "")
    w = d.get("metadata", {}).get("annotations", {}).get("helm.sh/hook-weight")
    by[(k, name)] = w

def want(kind, name, weight):
    got = by.get((kind, name))
    if got != weight:
        print(f"FAIL(#4447): {kind}/{name} hook-weight={got!r}, expected {weight!r}", file=sys.stderr)
        sys.exit(1)

# SA + both RBAC pairs MUST be weight 15 (< the Job's 20).
want("ServiceAccount", "catalyst-gitea-flux-auth-sync", "15")
want("Role",        "catalyst-gitea-flux-auth-sync-pat-reader", "15")
want("RoleBinding", "catalyst-gitea-flux-auth-sync-pat-reader", "15")
want("Role",        "catalyst-gitea-flux-auth-sync-writer", "15")
want("RoleBinding", "catalyst-gitea-flux-auth-sync-writer", "15")
# The Job MUST be strictly greater so Helm waits the RBAC batch Ready first.
want("Job", "catalyst-gitea-flux-auth-sync", "20")

job_w = int(by[("Job", "catalyst-gitea-flux-auth-sync")])
for (k, n), w in by.items():
    if k in ("Role", "RoleBinding", "ServiceAccount"):
        if int(w) >= job_w:
            print(f"FAIL(#4447): {k}/{n} weight {w} >= Job weight {job_w} — RBAC not ordered before the Job", file=sys.stderr)
            sys.exit(1)
print("  PASS gate-1: SA + all RBAC are hook-weight 15, strictly before the Job (20)")
PY

# ── Gate 2: behavioral — fail-loud on read-error, self-heal on empty ──────────
# Extract the rendered container script and drive it against a stub kubectl.
python3 - "$TMP/hook.yaml" > "$TMP/script.sh" <<'PY'
import sys, yaml
for d in yaml.safe_load_all(open(sys.argv[1])):
    if d and d.get("kind") == "Job":
        sys.stdout.write(d["spec"]["template"]["spec"]["containers"][0]["args"][0])
PY

# Syntax must be valid POSIX sh (the image runs `sh -c`).
if ! sh -n "$TMP/script.sh"; then
  echo "FAIL(#4447): rendered container script is not valid POSIX sh" >&2
  exit 1
fi
echo "  PASS gate-2a: rendered container script passes 'sh -n'"

mkdir -p "$TMP/bin"
cat > "$TMP/bin/kubectl" <<'EOF'
#!/bin/sh
ARGS="$*"
case "$ARGS" in
  *"auth can-i"*)
    [ "$RBAC" = "deny" ] && { echo "no" >&2; exit 1; }
    echo "yes"; exit 0 ;;
  *"get secret"*"jsonpath={.data.password}"*)
    [ "$VERIFY" = "fail" ] && { echo "" >&2; exit 1; }
    printf 'cGFzcw=='; exit 0 ;;
  *"get secret"*"jsonpath={.data."*)
    case "$SCENARIO" in
      read_error)  echo "Error from server (Forbidden): secrets is forbidden" >&2; exit 1 ;;
      empty_token) echo ""; exit 0 ;;
      success)     printf 'c2hhMXRva2Vu'; exit 0 ;;
    esac ;;
  *"create secret"*) printf 'apiVersion: v1\nkind: Secret\nmetadata: {name: x, namespace: y}\ndata: {username: dXNlcg==, password: cGFzcw==}\n' ;;
  *"label --local"*)    cat ;;
  *"annotate --local"*) cat ;;
  *"apply"*) echo "secret/x applied" ;;
  *) exit 0 ;;
esac
EOF
chmod +x "$TMP/bin/kubectl"
printf '#!/bin/sh\nexit 0\n' > "$TMP/bin/sleep"
chmod +x "$TMP/bin/sleep"

drive() { # SCENARIO RBAC VERIFY
  PATH="$TMP/bin:$PATH" POLL_ATTEMPTS=3 POLL_INTERVAL=0 \
    PAT_NAMESPACE=catalyst-system PAT_SECRET=catalyst-gitea-token PAT_KEY=token \
    CAT_SECRET_NAME=openova-catalog-sovereign-git-auth CAT_SECRET_NS=flux-system CAT_USER=gitea_admin \
    ORG_SECRET_NAME=openova-org-tenants-git-auth ORG_SECRET_NS=flux-system ORG_USER=gitea_admin \
    ORG_REFLECT_NAMESPACES="" \
    SCENARIO="$1" RBAC="${2:-allow}" VERIFY="${3:-ok}" \
    sh "$TMP/script.sh" >/dev/null 2>&1
}

# success → exit 0
if ! drive success allow ok; then
  echo "FAIL(#4447): script exited non-zero on the happy path (token present, verify ok)" >&2; exit 1
fi
# RBAC denied → MUST fail loud (the pre-fix bug silently exit-0'd here)
if drive success deny ok; then
  echo "FAIL(#4447): script exited 0 when RBAC denies the PAT read — must fail LOUD so the Job retries" >&2; exit 1
fi
# Read error for the whole budget → MUST fail loud (the core #4447 403-swallow bug)
if drive read_error allow ok; then
  echo "FAIL(#4447): script exited 0 when the PAT read errors for the whole budget — must fail LOUD, not report Succeeded with zero Secrets" >&2; exit 1
fi
# Genuine empty token (clean read) → benign self-heal exit 0 (preserve #3781)
if ! drive empty_token allow ok; then
  echo "FAIL(#4447): script failed on a CLEAN empty-token read — the benign mint-race must exit 0 and self-heal (#3781)" >&2; exit 1
fi
# Post-apply verify fails → MUST fail loud (task-4 defense in depth)
if drive success allow fail; then
  echo "FAIL(#4447): script exited 0 when post-apply verify shows an absent/empty Secret — must fail LOUD" >&2; exit 1
fi
echo "  PASS gate-2b: fail-loud on RBAC-deny / read-error / verify-fail; self-heal only on a clean empty token"

echo "[gitea-flux-auth-sync] All gates green (#4447)."
