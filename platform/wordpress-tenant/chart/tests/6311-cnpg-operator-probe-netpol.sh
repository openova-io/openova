#!/usr/bin/env bash
# bp-wordpress-tenant — CNPG-operator status-probe carve-out (#6311, Refs #3988 #4282).
#
# WHAT THIS LOCKS
# ───────────────
# This chart renders its CNPG `Cluster` into the Organization's own namespace,
# which carries the `default-deny-all` + `allow-same-org` NetworkPolicy pair
# stamped by the Organization controller's GitOps tree. `allow-same-org`
# re-opens SAME-NAMESPACE traffic only, so the cnpg-system OPERATOR — a
# different namespace — cannot reach the instance Pod's :8000/pg/status probe.
# CNPG then parks the Cluster at
#     Instance Status Extraction Error: HTTP communication issue
#     readyInstances 1/1 / ConsistentSystemID=False (NotFound)
# i.e. Ready=False while Postgres is 1/1 Running and serving SQL. Measured live
# on hw296 `walktwo/wordpress-db` with quota eliminated as a variable; the
# control `walkone/postgres` — same deny pair, but carrying the #4282 carve-out
# — read `Cluster in healthy state` 3/3.
#
# Every assertion below is VACUITY-PROVED: `--vacuity` re-runs each one against
# a render with exactly ONE behaviour mutated back to the pre-fix shape and
# requires it to FAIL. An assertion that cannot go red is not a test.
#
# Usage: bash tests/6311-cnpg-operator-probe-netpol.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

CNPG_API=(--api-versions "postgresql.cnpg.io/v1")

# ──────────────────────────────────────────────────────────────────────────
# The assertion body, factored out so the SAME code runs against the real
# render and against each mutated render. This is what makes the vacuity
# proof honest: a mutation cannot pass because the check drifted.
# ──────────────────────────────────────────────────────────────────────────
assert_carveout() {  # $1 = rendered manifest path
  python3 - "$1" <<'PYEOF'
import sys, yaml
docs = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d]

nps = [d for d in docs if d.get('kind') == 'NetworkPolicy']
probe = next((d for d in nps
              if d['metadata']['name'].endswith('-allow-cnpg-operator-probe')), None)
assert probe, ("A1 the CNPG-operator probe carve-out NetworkPolicy is not "
               "rendered — the Cluster will park at 'Instance Status "
               "Extraction Error: HTTP communication issue' (#6311)")

# A2 — it must select the CNPG instance Pods by the operator's own label,
#      not by the chart's app labels (the Postgres Pods carry cnpg.io/cluster,
#      never app.kubernetes.io/name: bp-wordpress-tenant).
sel = probe['spec'].get('podSelector', {}).get('matchLabels', {})
cluster = next((d for d in docs if d.get('kind') == 'Cluster'), None)
assert cluster, "A2 no CNPG Cluster rendered — the carve-out would guard nothing"
want = cluster['metadata']['name']
assert sel.get('cnpg.io/cluster') == want, (
    f"A2 podSelector must match cnpg.io/cluster={want!r}, got {sel!r}")

# A3 — the peer must be the OPERATOR NAMESPACE. A same-namespace podSelector
#      would be inert: allow-same-org already covers same-namespace traffic,
#      and the operator is what is being denied.
ing = probe['spec'].get('ingress') or []
assert len(ing) >= 1, "A3 carve-out has no ingress rule"
peers = [p for rule in ing for p in (rule.get('from') or [])]
ns_labels = [p['namespaceSelector']['matchLabels'] for p in peers
             if 'namespaceSelector' in p]
assert any(l.get('kubernetes.io/metadata.name') == 'cnpg-system' for l in ns_labels), (
    f"A3 carve-out must admit the cnpg-system operator namespace, got {ns_labels!r}")
assert not any('podSelector' in p for p in peers), (
    "A3 the carve-out must not add a same-namespace podSelector peer — "
    "allow-same-org already covers that and it would mask a broken operator peer")

# A4 — both probe ports. 8000 is the status endpoint the phase string comes
#      from; 9187 is the metrics endpoint. Opening only one leaves the
#      operator half-blind.
ports = {p['port'] for rule in ing for p in (rule.get('ports') or [])}
assert 8000 in ports, f"A4 operator status port 8000 not opened (got {sorted(ports)})"
assert 9187 in ports, f"A4 operator metrics port 9187 not opened (got {sorted(ports)})"

# A5 — Ingress only, and NARROW. The carve-out must not become a blanket
#      allow: no `namespaceSelector: {}`, and no 5432 (the bp-postgres #4442
#      sharedConsumers shape is wrong here — this Cluster's only consumers
#      are same-namespace and allow-same-org already admits them).
assert probe['spec'].get('policyTypes') == ['Ingress'], (
    f"A5 carve-out must be Ingress-only, got {probe['spec'].get('policyTypes')!r}")
assert not any(p.get('namespaceSelector') == {} for p in peers), (
    "A5 carve-out must not open every namespace (namespaceSelector: {})")
assert 5432 not in ports, (
    "A5 carve-out must not open 5432 — same-namespace consumers are already "
    "admitted by allow-same-org; a broad 5432 rule is the #4442 shared-engine "
    "shape and does not apply to a per-Org install")

# A6 — ADDITIVE. The chart must not have weakened or replaced the pod-level
#      policy it already ships; the WordPress app policy must still be there
#      with its Ingress+Egress restriction intact.
app_np = next((d for d in nps
               if d['metadata']['name'].endswith('-allow-cnpg-operator-probe') is False
               and d['spec'].get('policyTypes') == ['Ingress', 'Egress']), None)
assert app_np, ("A6 the chart's own WordPress NetworkPolicy disappeared — the "
                "carve-out must be additive, never a replacement")

print("  carve-out: podSelector=%s ns=cnpg-system ports=%s policyTypes=%s"
      % (want, sorted(ports), probe['spec']['policyTypes']))
PYEOF
}

# ──────────────────────────────────────────────────────────────────────────
# REAL RENDER — every assertion must pass.
# ──────────────────────────────────────────────────────────────────────────
echo "[6311-netpol] Case 1: default (singleton) render carries the CNPG-operator carve-out"
helm template smoke-wp . "${CNPG_API[@]}" > "$TMP/canonical.yaml"
assert_carveout "$TMP/canonical.yaml"
echo "  PASS"

# ──────────────────────────────────────────────────────────────────────────
# GATE-VACUITY — the render gates must actually gate. Each of these renders
# is a state the chart genuinely supports; the carve-out must be absent from
# every one, for a stated reason.
# ──────────────────────────────────────────────────────────────────────────
echo "[6311-netpol] Case 2: gates — the carve-out renders ONLY where it belongs"
gate_absent() {  # $1 = label  $2.. = extra helm args
  local label="$1"; shift
  helm template smoke-wp . "$@" > "$TMP/gate.yaml"
  if grep -q 'allow-cnpg-operator-probe' "$TMP/gate.yaml"; then
    echo "FAIL: carve-out rendered when it must not — ${label}" >&2
    exit 1
  fi
  echo "  absent as required: ${label}"
}
# No CNPG CRD on the cluster → no Cluster is rendered → a policy selecting it
# would select nothing (and cnpg-cluster.yaml uses the same capability gate).
gate_absent "no postgresql.cnpg.io/v1 capability"
# The Cluster itself is off.
gate_absent "database.cluster.enabled=false" "${CNPG_API[@]}" --set database.cluster.enabled=false
# The whole NetworkPolicy surface is off (non-Cilium dev cluster).
gate_absent "networkPolicy.enabled=false" "${CNPG_API[@]}" --set networkPolicy.enabled=false
# Explicit opt-out for a cluster with no default-deny baseline.
gate_absent "networkPolicy.cnpgOperator.enabled=false" "${CNPG_API[@]}" \
  --set networkPolicy.cnpgOperator.enabled=false
# active-hot-standby: the replica streams WAL cross-cluster over ClusterMesh;
# an Ingress policyType selecting the Cluster Pods would default-deny that
# stream, and a remote-cluster peer cannot be expressed in a k8s NetworkPolicy
# (an ipBlock never matches a ClusterMesh remote endpoint identity). Rendering
# nothing keeps that path byte-identical rather than trading one break for
# another. Mirrors bp-postgres, whose #4282 template is singleton-only too.
gate_absent "database.mode=active-hot-standby" "${CNPG_API[@]}" \
  --set database.mode=active-hot-standby \
  --set pg.activeHotStandby.promotion.mechanism=manual \
  --set pg.activeHotStandby.primaryRegion=hz-fsn-rtz-prod \
  --set pg.activeHotStandby.replicaRegion=hz-hel-rtz-prod
echo "  PASS"

# ──────────────────────────────────────────────────────────────────────────
# VACUITY PROOF — mutate ONE behaviour at a time back to a pre-fix / broken
# shape and require the SAME assertion body to FAIL. Run with --vacuity, or
# as part of the default run (it is cheap: pure `helm template` + text edit).
# ──────────────────────────────────────────────────────────────────────────
echo "[6311-netpol] Case 3: vacuity proof — every assertion goes RED on its own mutation"
mutate_must_fail() {  # $1 = label  $2 = python mutation; must end in dump() or write_text()
  local label="$1" mutation="$2"
  python3 - "$TMP/canonical.yaml" "$TMP/mutated.yaml" <<PYEOF
import sys, yaml
src, dst = sys.argv[1], sys.argv[2]
text = open(src).read()
docs = [d for d in yaml.safe_load_all(text) if d]

def probe():
    return next(d for d in docs if d.get('kind') == 'NetworkPolicy'
                and d['metadata']['name'].endswith('-allow-cnpg-operator-probe'))

def dump():
    open(dst, 'w').write("\n---\n".join(yaml.safe_dump(d) for d in docs))

def write_text():
    open(dst, 'w').write(text)

${mutation}
PYEOF
  if assert_carveout "$TMP/mutated.yaml" >/dev/null 2>&1; then
    echo "FAIL: assertions PASSED against the mutated render — ${label}" >&2
    echo "      That assertion is vacuous: it cannot detect its own defect." >&2
    exit 1
  fi
  echo "  RED as required: ${label}"
}

# A1 — remove the carve-out entirely (this IS the pre-fix chart).
mutate_must_fail "A1 carve-out deleted (pre-fix chart)" \
  "text = text.replace('-allow-cnpg-operator-probe', '-some-other-policy'); write_text()"
# A2 — select the wrong Pods (chart app labels instead of cnpg.io/cluster).
mutate_must_fail "A2 podSelector points at the wrong label" \
  "probe()['spec']['podSelector']['matchLabels'] = {'app.kubernetes.io/name': 'bp-wordpress-tenant'}; dump()"
# A3 — point the peer at the wrong namespace (the exact shape that leaves the
#      operator denied while the policy LOOKS present).
mutate_must_fail "A3 operator namespace wrong" \
  "probe()['spec']['ingress'][0]['from'][0]['namespaceSelector']['matchLabels']['kubernetes.io/metadata.name'] = 'kube-system'; dump()"
# A3 — add an inert same-namespace podSelector peer instead of relying on the
#      operator namespace peer (looks like a fix, changes nothing).
mutate_must_fail "A3 same-namespace podSelector peer added" \
  "probe()['spec']['ingress'][0]['from'].append({'podSelector': {}}); dump()"
# A4 — open only the metrics port, not the status port the phase string comes from.
mutate_must_fail "A4 status port 8000 missing" \
  "probe()['spec']['ingress'][0]['ports'] = [{'port': 9187, 'protocol': 'TCP'}]; dump()"
# A4 — and the converse.
mutate_must_fail "A4 metrics port 9187 missing" \
  "probe()['spec']['ingress'][0]['ports'] = [{'port': 8000, 'protocol': 'TCP'}]; dump()"
# A5 — widen policyTypes (an Egress policyType on the DB Pods would default-deny
#      the instance's own outbound traffic).
mutate_must_fail "A5 policyTypes widened to Ingress+Egress" \
  "probe()['spec']['policyTypes'] = ['Ingress', 'Egress']; dump()"
# A5 — blanket namespaceSelector: {} (the over-broad 'fix').
mutate_must_fail "A5 blanket namespaceSelector: {} added" \
  "probe()['spec']['ingress'].append({'from': [{'namespaceSelector': {}}], 'ports': [{'port': 8000, 'protocol': 'TCP'}]}); dump()"
# A5 — the #4442 shared-consumer 5432 rule, which does not apply per-Org.
mutate_must_fail "A5 broad 5432 consumer rule added" \
  "probe()['spec']['ingress'].append({'from': [{'namespaceSelector': {}}], 'ports': [{'port': 5432, 'protocol': 'TCP'}]}); dump()"
# A6 — the carve-out REPLACES the chart's own app policy instead of adding to it.
mutate_must_fail "A6 chart's own WordPress NetworkPolicy removed" \
  "docs[:] = [d for d in docs if not (d.get('kind') == 'NetworkPolicy' and d['spec'].get('policyTypes') == ['Ingress', 'Egress'])]; dump()"
echo "  PASS"

echo "[6311-netpol] All #6311 CNPG-operator carve-out gates green."
