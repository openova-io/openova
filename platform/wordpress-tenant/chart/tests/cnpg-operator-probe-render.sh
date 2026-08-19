#!/usr/bin/env bash
# bp-wordpress-tenant cnpg-system operator-probe NetworkPolicy render gate
# (#6360 / #4282-class, chart 0.4.24).
#
# WHY THIS EXISTS
# ----------------
# templates/cnpg-cluster.yaml renders a Cluster.postgresql.cnpg.io into the
# Application's OWN per-Organization namespace, where the host cnpg-system
# operator does NOT run. That namespace carries `default-deny-all`
# (Ingress+Egress) + `allow-same-org` (same-namespace podSelector + DNS),
# stamped by the catalyst-tenant-<slug>-apps kustomization. That pair denies the
# cnpg-system OPERATOR (a DIFFERENT namespace) from reaching the instance Pod's
# :8000/pg/status status probe + :9187 metrics endpoint — so even though the
# postgres Pod is Running 1/1 and serving SQL, the operator cannot extract status
# and the Cluster phase stalls at "Instance Status Extraction Error" /
# Ready=False (live-confirmed on walktwo/wordpress-db, UAT row 222). The additive
# templates/networkpolicy-cnpg-operator-probe.yaml restores exactly that one
# reach; mirrors platform/postgres #4282.
#
# WHAT IT ASSERTS (teeth — every claim is on a VALUE, not a key's presence)
# ------------------------------------------------------------------------
#   Case 1 — WITH `--api-versions postgresql.cnpg.io/v1` the render emits EXACTLY
#     ONE NetworkPolicy whose name ends `-cnpg-operator-probe`, and it:
#       - selects instance Pods via podSelector.matchExpressions
#         {key: cnpg.io/cluster, operator: Exists};
#       - is Ingress (policyTypes contains Ingress);
#       - admits ONLY the cnpg-system namespace
#         (from[].namespaceSelector kubernetes.io/metadata.name == cnpg-system);
#       - opens ONLY TCP :8000 (status) + :9187 (metrics).
#   Case 2 — WITHOUT the CNPG api-version the policy is ABSENT (the template is
#     gated on the CNPG CRD being present, so it no-ops on a non-CNPG cluster).
#   Case 3 — networkPolicy.enabled=false (WITH the api-version) omits it too.
#   Case 4 — the cnpgOperator.{namespace,statusPort,metricsPort} overrides flow
#     through to the rendered object (never-hardcoded, Inviolable Principle #4).
#
# Usage: bash tests/cnpg-operator-probe-render.sh [CHART_DIR]
# CI consumes this via blueprint-release.yaml's `tests/*.sh` gate.

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

# Skip helm dep build when charts/ is already vendored (CI populates it before
# this step runs). Pattern lifted from tests/active-hot-standby-render.sh.
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

COMMON_SET=(
  --set "smeDomain=acme.example.local"
  --set "keycloak.realmURL=https://auth.acme.example.local/realms/sme"
  --set "keycloak.clientSecretName=wordpress-oidc"
  --set "adminUser.email=admin@acme.example.local"
)

API_VERSIONS=(
  --api-versions "postgresql.cnpg.io/v1"
)

# Emit a fact bundle for the operator-probe NetworkPolicy in $1; writes to $2.
# Fails (exit 1) if the render itself is unparseable.
probe_facts() {
  python3 - "$1" <<'PYEOF'
import sys, yaml
docs = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
nps = [d for d in docs
       if d.get("kind") == "NetworkPolicy"
       and str((d.get("metadata") or {}).get("name", "")).endswith("-cnpg-operator-probe")]
print(f"COUNT={len(nps)}")
for np in nps:
    spec = np.get("spec") or {}
    sel = (spec.get("podSelector") or {}).get("matchExpressions") or []
    cnpg = next((e for e in sel if e.get("key") == "cnpg.io/cluster"), None)
    print(f"SELECTOR_KEY={'cnpg.io/cluster' if cnpg else ''}")
    print(f"SELECTOR_OP={(cnpg or {}).get('operator','')}")
    print(f"POLICYTYPES={','.join(spec.get('policyTypes') or [])}")
    ingress = spec.get("ingress") or []
    froms, ports = [], []
    for rule in ingress:
        for f in (rule.get("from") or []):
            ns = ((f.get("namespaceSelector") or {}).get("matchLabels") or {})
            v = ns.get("kubernetes.io/metadata.name")
            if v:
                froms.append(v)
        for p in (rule.get("ports") or []):
            ports.append(f"{p.get('protocol','')}:{p.get('port','')}")
    print(f"FROM_NS={','.join(sorted(set(froms)))}")
    print(f"PORTS={','.join(sorted(ports))}")
PYEOF
}

# ── Case 1: WITH the CNPG api-version → present, correct shape ────
echo "[cnpg-probe] Case 1: with postgresql.cnpg.io/v1 the operator-probe NetworkPolicy is present and correct"
helm template smoke-wp . "${COMMON_SET[@]}" "${API_VERSIONS[@]}" \
  > "$TMP/with.yaml" 2> "$TMP/with.err" || {
  echo "FAIL: render (with CNPG api-version) errored:" >&2
  cat "$TMP/with.err" >&2
  exit 1
}
probe_facts "$TMP/with.yaml" > "$TMP/with-facts" || {
  echo "FAIL: render output failed to parse as YAML" >&2
  exit 1
}

WITH_COUNT=$(grep -E '^COUNT=' "$TMP/with-facts" | cut -d= -f2)
if [ "$WITH_COUNT" -ne 1 ]; then
  echo "FAIL: expected exactly 1 *-cnpg-operator-probe NetworkPolicy, got $WITH_COUNT." >&2
  cat "$TMP/with-facts" >&2
  exit 1
fi
grep -qx 'SELECTOR_KEY=cnpg.io/cluster' "$TMP/with-facts" || {
  echo "FAIL: podSelector does not match on the cnpg.io/cluster label." >&2
  cat "$TMP/with-facts" >&2; exit 1; }
grep -qx 'SELECTOR_OP=Exists' "$TMP/with-facts" || {
  echo "FAIL: cnpg.io/cluster selector is not an Exists match (would miss un-valued instance Pods)." >&2
  cat "$TMP/with-facts" >&2; exit 1; }
grep -qE '^POLICYTYPES=(.*,)?Ingress(,.*)?$' "$TMP/with-facts" || {
  echo "FAIL: NetworkPolicy is not an Ingress policy." >&2
  cat "$TMP/with-facts" >&2; exit 1; }
grep -qx 'FROM_NS=cnpg-system' "$TMP/with-facts" || {
  echo "FAIL: ingress.from admits a namespace other than (or instead of) cnpg-system." >&2
  cat "$TMP/with-facts" >&2; exit 1; }
grep -qx 'PORTS=TCP:8000,TCP:9187' "$TMP/with-facts" || {
  echo "FAIL: ports are not exactly TCP:8000 (status) + TCP:9187 (metrics)." >&2
  cat "$TMP/with-facts" >&2; exit 1; }
echo "  PASS"

# ── Case 2: WITHOUT the CNPG api-version → absent ────────────────
echo "[cnpg-probe] Case 2: without the CNPG api-version the operator-probe NetworkPolicy is absent"
helm template smoke-wp . "${COMMON_SET[@]}" \
  > "$TMP/without.yaml" 2> "$TMP/without.err" || {
  echo "FAIL: render (no CNPG api-version) errored:" >&2
  cat "$TMP/without.err" >&2
  exit 1
}
probe_facts "$TMP/without.yaml" > "$TMP/without-facts" || {
  echo "FAIL: render output failed to parse as YAML" >&2
  exit 1
}
WITHOUT_COUNT=$(grep -E '^COUNT=' "$TMP/without-facts" | cut -d= -f2)
if [ "$WITHOUT_COUNT" -ne 0 ]; then
  echo "FAIL: operator-probe NetworkPolicy rendered on a cluster with NO postgresql.cnpg.io/v1 CRD (should be gated off)." >&2
  cat "$TMP/without-facts" >&2
  exit 1
fi
echo "  PASS"

# ── Case 3: networkPolicy.enabled=false → absent (WITH api-version) ─
echo "[cnpg-probe] Case 3: networkPolicy.enabled=false omits the operator-probe NetworkPolicy"
helm template smoke-wp . "${COMMON_SET[@]}" "${API_VERSIONS[@]}" \
  --set "networkPolicy.enabled=false" \
  > "$TMP/off.yaml" 2> "$TMP/off.err" || {
  echo "FAIL: render (networkPolicy disabled) errored:" >&2
  cat "$TMP/off.err" >&2
  exit 1
}
probe_facts "$TMP/off.yaml" > "$TMP/off-facts" || {
  echo "FAIL: render output failed to parse as YAML" >&2
  exit 1
}
OFF_COUNT=$(grep -E '^COUNT=' "$TMP/off-facts" | cut -d= -f2)
if [ "$OFF_COUNT" -ne 0 ]; then
  echo "FAIL: operator-probe NetworkPolicy rendered while networkPolicy.enabled=false." >&2
  cat "$TMP/off-facts" >&2
  exit 1
fi
echo "  PASS"

# ── Case 4: cnpgOperator overrides flow through (never hardcode, #4) ─
echo "[cnpg-probe] Case 4: cnpgOperator namespace/ports overrides reach the rendered object"
helm template smoke-wp . "${COMMON_SET[@]}" "${API_VERSIONS[@]}" \
  --set "networkPolicy.cnpgOperator.namespace=cnpg-operators" \
  --set "networkPolicy.cnpgOperator.statusPort=8010" \
  --set "networkPolicy.cnpgOperator.metricsPort=9188" \
  > "$TMP/ovr.yaml" 2> "$TMP/ovr.err" || {
  echo "FAIL: render (cnpgOperator overrides) errored:" >&2
  cat "$TMP/ovr.err" >&2
  exit 1
}
probe_facts "$TMP/ovr.yaml" > "$TMP/ovr-facts" || {
  echo "FAIL: render output failed to parse as YAML" >&2
  exit 1
}
grep -qx 'FROM_NS=cnpg-operators' "$TMP/ovr-facts" || {
  echo "FAIL: cnpgOperator.namespace override did not reach the ingress.from namespaceSelector." >&2
  cat "$TMP/ovr-facts" >&2; exit 1; }
grep -qx 'PORTS=TCP:8010,TCP:9188' "$TMP/ovr-facts" || {
  echo "FAIL: cnpgOperator status/metrics port overrides did not reach the ingress.ports." >&2
  cat "$TMP/ovr-facts" >&2; exit 1; }
echo "  PASS"

echo "[cnpg-probe] All bp-wordpress-tenant cnpg-system operator-probe render gates green."
