#!/usr/bin/env bash
# bp-cnpg — G65 (#4322 #4143 #4201) render gate for the per-Org webhook-HIJACK
# RBAC fix.
#
# THE VECTOR: the upstream cloudnative-pg subchart includes its `clusterwideRules`
# (which grant get,patch on mutating/validatingwebhookconfigurations at CLUSTER
# scope) in the operator ClusterRole `bp-cnpg-cloudnative-pg` UNCONDITIONALLY —
# even when config.clusterWide=false. So a namespace-scoped, webhook-LESS per-Org
# operator was STILL granted cluster-scoped patch on the cnpg webhook singleton
# and on each restart over-wrote the shared caBundle with its own org-ns leaf
# cert (CA:FALSE) → apiserver x509-fail → every CNPG Cluster create/patch
# rejected cluster-wide (live omantel.biz org-7283eb4a, #4322).
#
# THE FIX: in webhook-LESS mode the per-Org generator sets
# cloudnative-pg.rbac.create=false (subchart RBAC off) and the umbrella's
# templates/per-org-operator-rbac.yaml renders REPLACEMENT RBAC — a namespace
# Role + a MINIMAL ClusterRole (nodes + clusterimagecatalogs reads ONLY, NO
# webhookconfigurations). The per-Org operator becomes STRUCTURALLY INCAPABLE of
# patching the cluster singleton.
#
# This test asserts (helm template only — no live cluster):
#   A. WEBHOOK-LESS per-Org (webhook.create=false + rbac.create=false):
#      - NO rendered RBAC rule anywhere grants *webhookconfigurations (the vector
#        is gone);
#      - the replacement per-Org Role + ClusterRole + bindings DO render and
#        target the operator SA bp-cnpg-cloudnative-pg;
#      - the minimal ClusterRole grants nodes + clusterimagecatalogs ONLY.
#   B. PLATFORM webhook-FULL (defaults): the subchart's own ClusterRole STILL
#      carries the legitimate webhookconfigurations grant (the single platform
#      operator owns the singleton) AND the per-Org replacement RBAC does NOT
#      render.
#
# Usage: bash tests/per-org-operator-rbac.sh [CHART_DIR]
set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

if [ ! -d "$CHART_DIR/charts" ] || ! ls "$CHART_DIR"/charts/cloudnative-pg-*.tgz >/dev/null 2>&1; then
  helm dependency build "$CHART_DIR" >/dev/null 2>&1 || true
fi

fail() { echo "FAIL: $1"; exit 1; }

# ── Case A: WEBHOOK-LESS per-Org install ────────────────────────────────────
helm template bp-cnpg "$CHART_DIR" --namespace org-7283eb4a \
  --set "cloudnative-pg.webhook.mutating.create=false" \
  --set "cloudnative-pg.webhook.validating.create=false" \
  --set "cloudnative-pg.rbac.create=false" \
  --set "cloudnative-pg.crds.create=false" \
  --set "cloudnative-pg.config.clusterWide=false" \
  > "$TMP/perorg.yaml" 2>"$TMP/err" || { echo "helm template FAILED:"; cat "$TMP/err"; exit 1; }

# A1. THE keystone — NO webhookconfigurations grant survives anywhere. This is
#     the whole point: the per-Org operator cannot patch the cluster singleton.
if grep -q "webhookconfigurations" "$TMP/perorg.yaml"; then
  echo "--- offending ---"; grep -n "webhookconfigurations" "$TMP/perorg.yaml" | head
  fail "webhook-less per-Org render STILL grants *webhookconfigurations — the hijack vector is NOT removed (#4322)"
fi
echo "PASS: webhook-less per-Org render grants NO *webhookconfigurations (hijack vector removed)"

# A2. the replacement per-Org RBAC renders (Role + ClusterRole + both bindings).
for obj in \
  "kind: Role" \
  "kind: RoleBinding" \
  "kind: ClusterRole" \
  "kind: ClusterRoleBinding"; do
  awk -v k="$obj" 'BEGIN{RS="\n---\n"} $0 ~ k && /per-org-operator-rbac/ {found=1} END{exit !found}' "$TMP/perorg.yaml" \
    || fail "webhook-less render is missing the replacement ${obj#kind: } (per-org-operator-rbac)"
done
echo "PASS: replacement per-Org Role + ClusterRole + bindings render"

# A3. the bindings target the operator SA bp-cnpg-cloudnative-pg (so the operator
#     Deployment actually receives these permissions).
python3 - "$TMP/perorg.yaml" <<'PY' || exit 1
import sys, yaml
sa_ok = {"RoleBinding": False, "ClusterRoleBinding": False}
for d in yaml.safe_load_all(open(sys.argv[1])):
    if not d or d.get("kind") not in sa_ok: continue
    comp = ((d.get("metadata") or {}).get("labels") or {}).get("catalyst.openova.io/component")
    if comp != "per-org-operator-rbac": continue
    subs = d.get("subjects") or []
    if any(s.get("kind") == "ServiceAccount" and s.get("name") == "bp-cnpg-cloudnative-pg" for s in subs):
        sa_ok[d["kind"]] = True
bad = [k for k, v in sa_ok.items() if not v]
if bad:
    print("FAIL: per-Org", ", ".join(bad), "do(es) not bind SA bp-cnpg-cloudnative-pg")
    sys.exit(1)
print("PASS: per-Org Role/ClusterRole bindings target SA bp-cnpg-cloudnative-pg")
PY

# A4. the minimal ClusterRole grants ONLY nodes + clusterimagecatalogs reads.
python3 - "$TMP/perorg.yaml" <<'PY' || exit 1
import sys, yaml
for d in yaml.safe_load_all(open(sys.argv[1])):
    if not d or d.get("kind") != "ClusterRole": continue
    comp = ((d.get("metadata") or {}).get("labels") or {}).get("catalyst.openova.io/component")
    if comp != "per-org-operator-rbac": continue
    res = set()
    verbs = set()
    for r in d.get("rules") or []:
        res |= set(r.get("resources") or [])
        verbs |= set(r.get("verbs") or [])
    allowed_res = {"nodes", "clusterimagecatalogs"}
    extra = res - allowed_res
    if extra:
        print("FAIL: per-Org ClusterRole grants unexpected cluster resources:", sorted(extra))
        sys.exit(1)
    write_verbs = verbs & {"create", "delete", "patch", "update", "deletecollection"}
    if write_verbs:
        print("FAIL: per-Org ClusterRole grants write verbs at cluster scope:", sorted(write_verbs))
        sys.exit(1)
    print("PASS: per-Org ClusterRole grants only", sorted(res), "with read verbs", sorted(verbs))
    break
else:
    print("FAIL: per-Org ClusterRole not found")
    sys.exit(1)
PY

# ── Case B: PLATFORM webhook-FULL (defaults) ────────────────────────────────
helm template cnpg "$CHART_DIR" --namespace cnpg-system > "$TMP/full.yaml" 2>"$TMP/err" \
  || { echo "helm template FAILED:"; cat "$TMP/err"; exit 1; }

# B1. the platform (single-owner) operator KEEPS the legitimate webhook grant.
grep -q "webhookconfigurations" "$TMP/full.yaml" \
  || fail "platform webhook-FULL render lost the legitimate webhookconfigurations grant — the single operator must own the singleton"
echo "PASS: platform webhook-FULL render keeps the legitimate webhookconfigurations grant"

# B2. the per-Org replacement RBAC must NOT render in platform mode.
if grep -q "per-org-operator-rbac" "$TMP/full.yaml"; then
  echo "--- offending ---"; grep -n "per-org-operator-rbac" "$TMP/full.yaml" | head
  fail "platform webhook-FULL render WRONGLY emits the per-Org replacement RBAC"
fi
echo "PASS: platform webhook-FULL render does NOT emit the per-Org replacement RBAC"

echo "All bp-cnpg per-org-operator-rbac assertions passed (G65 #4322)."
