#!/usr/bin/env bash
# bp-grafana — G115 #2744 datasource + dashboard auto-discovery via sidecar.
#
# Verifies:
#   1. Sidecar discovery is enabled by default for both datasources +
#      dashboards (grafana.sidecar.datasources.enabled +
#      grafana.sidecar.dashboards.enabled).
#   2. 3 starter datasource ConfigMaps render with the correct
#      `grafana_datasource: "1"` label + canonical Service DNS URLs
#      (matching bootstrap-kit slot conventions for bp-mimir/loki/tempo).
#   3. 1 starter dashboard ConfigMap renders with 5 dashboard JSONs,
#      each valid JSON, each carrying the `grafana_dashboard: "1"`
#      sidecar label.
#   4. observabilityStack.enabled=false suppresses ALL datasource +
#      dashboard ConfigMaps (opt-out path).
#   5. Individual per-datasource / per-dashboard opt-out flags work.
#
# Why this guard:
#   Pre-G115 fresh-install Grafana on a Sovereign rendered empty (no
#   datasources, no dashboards) — operators saw a blank UI every fresh
#   prov. Codification means a fresh prov has charts rendering against
#   the LGTM stack the moment Grafana comes up. Regression in
#   templates/datasource-configmaps.yaml or templates/dashboard-
#   configmaps.yaml silently reverts to the empty state and surfaces
#   only on operator walk.

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"
tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT

"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

# Render with defaults once + write to disk for repeated reads by python.
"$helm" template smoke "$chart_dir" 2>/dev/null > "${tmpdir}/default.yaml"

# ── Case 1: Sidecar discovery is enabled in default values ─────────────
echo "[g115-datasources-dashboards] Case 1: sidecar discovery enabled by default"
if ! grep -q 'grafana_datasource' "${tmpdir}/default.yaml"; then
  echo "FAIL: grafana_datasource label not present in default render" >&2
  exit 1
fi
if ! grep -q 'grafana_dashboard' "${tmpdir}/default.yaml"; then
  echo "FAIL: grafana_dashboard label not present in default render" >&2
  exit 1
fi
echo "  PASS"

# ── Case 2: 3 datasource ConfigMaps render with canonical Service DNS ──
echo "[g115-datasources-dashboards] Case 2: 3 datasource ConfigMaps with canonical DNS"
python3 -c "
import yaml, sys
docs = list(yaml.safe_load_all(open('${tmpdir}/default.yaml')))
found = {}
for d in docs:
    if not d or d.get('kind') != 'ConfigMap':
        continue
    if d.get('metadata', {}).get('labels', {}).get('grafana_datasource') == '1':
        for k, v in (d.get('data') or {}).items():
            parsed = yaml.safe_load(v)
            ds = parsed['datasources'][0]
            found[ds['name']] = ds['url']
want = {
    'Prometheus': 'http://mimir-nginx.mimir.svc.cluster.local:80/prometheus',
    'Loki':       'http://loki.loki.svc.cluster.local:3100',
    'Tempo':      'http://tempo.tempo.svc.cluster.local:3200',
}
for name, url in want.items():
    if name not in found:
        print(f'FAIL: datasource \"{name}\" missing', file=sys.stderr); sys.exit(1)
    if found[name] != url:
        print(f'FAIL: datasource \"{name}\" url={found[name]} want={url}', file=sys.stderr); sys.exit(1)
print('  PASS')
" || exit 1

# ── Case 3: Dashboard ConfigMap with 5 valid JSON dashboards ───────────
echo "[g115-datasources-dashboards] Case 3: dashboard ConfigMap with 5 valid JSON dashboards"
python3 -c "
import yaml, json, sys
docs = list(yaml.safe_load_all(open('${tmpdir}/default.yaml')))
expected = {'cluster-overview.json', 'node-exporter-summary.json', 'kube-state-summary.json',
            'openbao-audit.json', 'flux-reconcile-health.json'}
for d in docs:
    if not d or d.get('kind') != 'ConfigMap':
        continue
    if d.get('metadata', {}).get('labels', {}).get('grafana_dashboard') == '1':
        keys = set((d.get('data') or {}).keys())
        if not expected.issubset(keys):
            print(f'FAIL: dashboards missing: {expected - keys}', file=sys.stderr); sys.exit(1)
        for k, v in d['data'].items():
            try:
                parsed = json.loads(v)
                assert 'title' in parsed and 'panels' in parsed and len(parsed['panels']) >= 1
            except (json.JSONDecodeError, AssertionError) as e:
                print(f'FAIL: dashboard \"{k}\" invalid: {e}', file=sys.stderr); sys.exit(1)
        print('  PASS')
        sys.exit(0)
print('FAIL: no grafana_dashboard-labelled ConfigMap found', file=sys.stderr); sys.exit(1)
" || exit 1

# ── Case 4: observabilityStack.enabled=false suppresses all CMs ────────
echo "[g115-datasources-dashboards] Case 4: opt-out path — observabilityStack.enabled=false"
"$helm" template smoke "$chart_dir" --set observabilityStack.enabled=false 2>/dev/null > "${tmpdir}/optout.yaml"
ds_cms=$(python3 -c "
import yaml
docs = list(yaml.safe_load_all(open('${tmpdir}/optout.yaml')))
cnt = 0
for d in docs:
    if d and d.get('kind') == 'ConfigMap':
        labels = d.get('metadata', {}).get('labels', {})
        if labels.get('grafana_datasource') == '1' or labels.get('grafana_dashboard') == '1':
            cnt += 1
print(cnt)
")
if [ "$ds_cms" -ne 0 ]; then
  echo "FAIL: observabilityStack.enabled=false still rendered $ds_cms labelled ConfigMaps" >&2
  exit 1
fi
echo "  PASS (0 starter ConfigMaps rendered)"

# ── Case 5: Per-datasource opt-out — disable loki only ─────────────────
echo "[g115-datasources-dashboards] Case 5: per-datasource opt-out (loki disabled)"
"$helm" template smoke "$chart_dir" --set observabilityStack.datasources.loki.enabled=false 2>/dev/null > "${tmpdir}/noloki.yaml"
loki_count=$(python3 -c "
import yaml
docs = list(yaml.safe_load_all(open('${tmpdir}/noloki.yaml')))
cnt = 0
for d in docs:
    if d and d.get('kind') == 'ConfigMap':
        if d.get('metadata', {}).get('labels', {}).get('grafana_datasource') == '1':
            for k, v in (d.get('data') or {}).items():
                parsed = yaml.safe_load(v)
                if parsed['datasources'][0]['type'] == 'loki':
                    cnt += 1
print(cnt)
")
if [ "$loki_count" -ne 0 ]; then
  echo "FAIL: per-datasource opt-out for loki did not suppress its ConfigMap" >&2
  exit 1
fi
echo "  PASS"

# ── Case 6: Per-dashboard opt-out — disable openbao audit only ────────
echo "[g115-datasources-dashboards] Case 6: per-dashboard opt-out (openbao audit disabled)"
"$helm" template smoke "$chart_dir" --set observabilityStack.dashboards.starter.openbaoAudit=false 2>/dev/null > "${tmpdir}/nobao.yaml"
has_bao=$(python3 -c "
import yaml
docs = list(yaml.safe_load_all(open('${tmpdir}/nobao.yaml')))
for d in docs:
    if d and d.get('kind') == 'ConfigMap':
        if d.get('metadata', {}).get('labels', {}).get('grafana_dashboard') == '1':
            print('found' if 'openbao-audit.json' in (d.get('data') or {}) else 'absent')
            break
")
if [ "$has_bao" != "absent" ]; then
  echo "FAIL: per-dashboard opt-out for openbaoAudit did not suppress its key" >&2
  exit 1
fi
echo "  PASS"

# ── Case 7: G117.5 — kc_idp_hint defense-in-depth in env-override Secret
echo "[g115-datasources-dashboards] Case 7: G117.5 kc_idp_hint defense-in-depth"
if ! grep -q 'GF_AUTH_GENERIC_OAUTH_AUTH_URL' "${tmpdir}/default.yaml"; then
  echo "FAIL: GF_AUTH_GENERIC_OAUTH_AUTH_URL missing from ExternalSecret template" >&2
  exit 1
fi
if ! grep -q 'kc_idp_hint=catalyst-pin' "${tmpdir}/default.yaml"; then
  echo "FAIL: kc_idp_hint=catalyst-pin missing from ExternalSecret template" >&2
  exit 1
fi
echo "  PASS"

echo "[bp-grafana G115 datasources+dashboards] All cases PASS"
