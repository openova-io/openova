#!/usr/bin/env bash
# bp-openbao — ESO push-policy render audit (#5375, UAT row M2).
#
# The ESO auth role must carry a NARROW write policy (`external-secrets-push`)
# on exactly `secret/{data,metadata}/catalyst/newapi/admin-token` (bp-newapi's
# REGION-LOCAL admin-token PushSecret, the M2 region-B fix) plus the ADR-0015
# multi-region shared-material subtree `secret/{data,metadata}/openova/apps/*`.
# Since 1.2.72 (Refs #7067) the policies and roles are declarative desired
# state — ConfigMap openbao-config-desired, applied by the openbao-config
# reconciler — so this test reads them there. Guard rails:
#
#   1. external-secrets-push grants both data + metadata on the newapi path.
#   2. The ESO role attaches BOTH policies (read + push) — dropping either
#      silently re-opens M2 (push 403s) or breaks every ExternalSecret.
#   3. external-secrets-read stays READ-ONLY on the broad tree.
#   4. openova/apps/* is granted, scoped, with no delete and no whole-tree grant.
set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"
"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

render_file=$(mktemp)
trap 'rm -f "$render_file"' EXIT
"$helm" template openbao "$chart_dir" --set autoUnseal.enabled=true > "$render_file"

python3 - "$render_file" <<'PYEOF'
import json, re, sys, yaml
docs = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
cm = next((d for d in docs if d["kind"] == "ConfigMap" and d["metadata"]["name"] == "openbao-config-desired"), None)
if cm is None: sys.exit("FAIL: openbao-config-desired ConfigMap not rendered")
d = cm["data"]
pl = lambda p: json.loads(d.get(p.replace("/", ".") + ".json", "null") or "null")
caps = lambda hcl: {p: c for p, c in re.findall(r'path "([^"]+)"\s*\{\s*capabilities\s*=\s*\[([^\]]*)\]', hcl)}
push = caps((pl("sys/policies/acl/external-secrets-push") or {}).get("policy", ""))
read = caps((pl("sys/policies/acl/external-secrets-read") or {}).get("policy", ""))
role = pl("auth/kubernetes/role/external-secrets") or {}
errs = []
print("[bp-openbao] Case 1: external-secrets-push policy present with both path grants")
for p in ("secret/data/catalyst/newapi/admin-token", "secret/metadata/catalyst/newapi/admin-token"):
    if "create" not in push.get(p, ""): errs.append(f"push policy lacks the grant on {p}")
print("[bp-openbao] Case 2: ESO role attaches read + push policies")
if role.get("policies") != ["external-secrets-read", "external-secrets-push"]: errs.append(f"ESO role policies {role.get('policies')}")
print("[bp-openbao] Case 3: broad ESO read grant stays read-only")
if not read: errs.append("external-secrets-read policy missing")
if any(re.search(r'"(create|update|delete)"', c) for c in read.values()): errs.append("external-secrets-read gained write capabilities")
print("[bp-openbao] Case 4: multi-region shared-material grant (ADR-0015, #7067) — scoped, no delete")
for p in ("secret/data/openova/apps/*", "secret/metadata/openova/apps/*"):
    if "create" not in push.get(p, ""): errs.append(f"push policy lacks the grant on {p}")
if any('"delete"' in c for c in push.values()): errs.append("external-secrets-push gained delete")
if "secret/data/*" in push: errs.append("external-secrets-push widened to the whole KV tree")
if errs:
    print("\n".join("FAIL: " + e for e in errs)); sys.exit(1)
PYEOF
echo "[bp-openbao] eso-push-policy-render: ALL CASES PASSED"
