#!/usr/bin/env bash
# bp-keycloak — #4163 realm-import authentication-flow subflow-resolution guard.
#
# Guards the regression caught live on dep 4635277cae4ffed9 region-B
# (2026-06-23, chart 1.4.37): #4157 added a SECOND top-level browser flow
# `browser-no-idp` (builtIn:false) that referenced the standard `forms`
# subflow. keycloak-config-cli resolves `forms` from the server's BUILT-INS
# only for builtIn:true flows; for a CUSTOM (builtIn:false) flow it requires
# every referenced subflow to be EXPLICITLY declared as a non-top-level flow
# in the SAME realm JSON. With `forms` undeclared, the import aborts in 2.6s:
#
#   ERROR KeycloakConfigRunner : Error during Keycloak import:
#     Non-toplevel flow not found: forms
#   de.adorsys.keycloak.config.exception.ImportProcessingException:
#     Non-toplevel flow not found: forms
#
# → the bp-keycloak post-upgrade hook fails → the whole mgmt-vCluster SSO
# tier (oidc-gate / sso-bridge / grafana / gitea / powerdns-admin / newapi)
# stays Ready=False on `dependency 'mgmt/bp-keycloak' is not ready`.
#
# This is a CONTENT error the chart-render tests never caught because none
# of them runs the full config-cli import. This guard reproduces config-cli's
# subflow-resolution rule STATICALLY at render time, across EVERY realm
# template the chart can emit:
#
#   1. configmap-sovereign-realm.yaml  — central `sovereign` realm
#   2. configmap-tenant-realm.yaml     — SME single-realm mode
#   3. configmap-per-org-realms.yaml   — per-Org realm fan-out
#
# Invariant: for EVERY realm JSON, EVERY authenticationFlow with
# builtIn=false, EVERY execution that is a sub-flow reference (autheticatorFlow
# true + a flowAlias) MUST point at a flow declared in the SAME realm JSON
# with topLevel=false. (config-cli resolves built-in subflows ONLY for
# builtIn:true flows — never for custom flows — so a custom flow referencing
# an undeclared alias is always an import-time failure.)
#
# We deliberately do NOT exempt the well-known built-in alias `forms`: the
# whole point of #4163 is that config-cli does NOT auto-resolve it for a
# custom flow, so this guard must flag it exactly as config-cli does.
#
# Usage: bash tests/realm-flow-subflow-resolution.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
helm="${HELM_BIN:-helm}"

"$helm" dependency build "$CHART_DIR" >/dev/null 2>&1 || true

SOV_FQDN="smoke.omani.works"

# check_render <label> <helm-extra-args...>
# Renders the chart, extracts EVERY realm-import JSON from EVERY ConfigMap
# whose data carries a *-realm.json key, and asserts subflow resolution.
check_render() {
  local label="$1"; shift
  local render
  if ! render=$("$helm" template smoke "$CHART_DIR" \
        --set sovereignFQDN="${SOV_FQDN}" "$@" 2>/dev/null); then
    echo "FAIL [$label]: helm template failed to render" >&2
    exit 1
  fi

  echo "$render" | python3 -c "
import sys, yaml, json

label = '''$label'''
docs = list(yaml.safe_load_all(sys.stdin))

# Collect every realm-import JSON the render emits (any ConfigMap data value
# whose key ends in -realm.json and parses as a realm with authenticationFlows).
realms = []  # (configmap-name, data-key, parsed-realm)
for d in docs:
    if not d or d.get('kind') != 'ConfigMap':
        continue
    name = d.get('metadata', {}).get('name', '<unknown>')
    for k, v in (d.get('data') or {}).items():
        if not k.endswith('-realm.json'):
            continue
        try:
            parsed = json.loads(v)
        except Exception as e:
            print(f'FAIL [{label}]: ConfigMap {name} key {k} is not valid JSON: {e}', file=sys.stderr)
            sys.exit(1)
        if isinstance(parsed, dict) and 'authenticationFlows' in parsed:
            realms.append((name, k, parsed))

if not realms:
    # Some render modes legitimately emit zero realm JSONs (e.g. the per-Org
    # template with tenantRealms:[]). That is not a failure for this guard.
    print(f'  [{label}] no realm-import JSON emitted — nothing to check')
    sys.exit(0)

failures = []
for name, key, realm in realms:
    realm_name = realm.get('realm', '<no-realm-name>')
    flows = {f['alias']: f for f in realm.get('authenticationFlows', [])}
    declared_nontoplevel = {a for a, f in flows.items() if not f.get('topLevel', False)}
    for alias, flow in flows.items():
        if flow.get('builtIn', False):
            # config-cli resolves built-in subflows from the server for a
            # builtIn:true flow, so its references are not our concern.
            continue
        for ex in flow.get('authenticationExecutions', []):
            # A sub-flow reference is an execution with autheticatorFlow true
            # AND a flowAlias (Keycloak's misspelled key is intentional).
            if ex.get('autheticatorFlow') and 'flowAlias' in ex:
                fa = ex['flowAlias']
                if fa not in declared_nontoplevel:
                    failures.append(
                        f'{name}/{key} realm={realm_name}: CUSTOM flow '
                        f'\"{alias}\" (builtIn:false) references subflow '
                        f'\"{fa}\" which is NOT declared as a non-top-level '
                        f'flow in the same realm JSON -> config-cli would '
                        f'throw \"Non-toplevel flow not found: {fa}\"'
                    )

if failures:
    print(f'FAIL [{label}]: {len(failures)} unresolved custom-flow subflow reference(s):', file=sys.stderr)
    for f in failures:
        print(f'  - {f}', file=sys.stderr)
    sys.exit(1)

print(f'  PASS [{label}]: {len(realms)} realm JSON(s) — every builtIn:false '
      f'flow resolves its subflows to declared non-top-level flows')
"
}

echo "=== #4163 realm-flow subflow-resolution guard ==="

# Case 1 — sovereign realm (default mothership mode). Exercises the
# browser-no-idp / browser-no-idp-forms / browser-no-idp-conditional-otp
# chain #4157+#4163 introduced.
check_render "sovereign-realm (default)"

# Case 2 — SME single-tenant realm mode. Tenant mode requires the full
# realmName/parentDomain/subdomain/adminEmail set (Inviolable Principle #4 —
# never hardcode; the chart guard-fails on a bare enable).
check_render "tenant-realm" \
  --set realmConfig.tenant.enabled=true \
  --set realmConfig.tenant.realmName=sme-acme \
  --set realmConfig.tenant.displayName="Acme SME" \
  --set realmConfig.tenant.parentDomain=omantel.omani.works \
  --set realmConfig.tenant.subdomain=acme \
  --set realmConfig.tenant.adminEmail=admin@acme.example \
  --set gateway.host=keycloak.acme.omantel.omani.works

# Case 3 — per-Org realm fan-out. Declare one Org (key is `slug`) so the
# per-Org template actually emits a realm JSON to inspect.
check_render "per-org realms" \
  --set 'tenantRealms[0].slug=acme' \
  --set 'tenantRealms[0].displayName=Acme Corp'

# ── Explicit assertions on the #4163 fix itself (sovereign realm) ──────
echo "[#4163] asserting browser-no-idp now resolves + OTP survives"
realm_json=$("$helm" template smoke "$CHART_DIR" --set sovereignFQDN="${SOV_FQDN}" 2>/dev/null \
  | python3 -c "
import sys, yaml, json
for d in yaml.safe_load_all(sys.stdin):
    if d and d.get('kind')=='ConfigMap' and 'sovereign-realm-config' in d.get('metadata',{}).get('name',''):
        print(d['data']['sovereign-realm.json']); break
")
if [ -z "${realm_json}" ]; then
  echo "FAIL: sovereign-realm-config ConfigMap did not render" >&2
  exit 1
fi

assert_jq() {
  local desc="$1" jqexpr="$2" want="$3"
  local got
  got=$(printf '%s' "$realm_json" | python3 -c "import sys,json; print(json.dumps(json.load(sys.stdin)))" | jq -r "$jqexpr")
  if [ "$got" != "$want" ]; then
    echo "FAIL: $desc — expected '$want', got '$got'" >&2
    exit 1
  fi
  echo "  PASS: $desc = $got"
}

# browser-no-idp points at the DECLARED forms subflow, not the bare built-in.
assert_jq "browser-no-idp -> declared forms subflow" \
  '.authenticationFlows[] | select(.alias=="browser-no-idp") | .authenticationExecutions[] | select(.autheticatorFlow==true) | .flowAlias' \
  "browser-no-idp-forms"

# browser-no-idp-forms is declared, non-top-level, custom.
assert_jq "browser-no-idp-forms declared non-top-level" \
  '.authenticationFlows[] | select(.alias=="browser-no-idp-forms") | "\(.topLevel)/\(.builtIn)"' \
  "false/false"

# OTP MUST survive: the conditional-otp subflow carries auth-otp-form.
assert_jq "browser-no-idp-conditional-otp keeps auth-otp-form" \
  '.authenticationFlows[] | select(.alias=="browser-no-idp-conditional-otp") | (.authenticationExecutions[] | select(.authenticator=="auth-otp-form") | .authenticator)' \
  "auth-otp-form"

# The username/password form is REQUIRED (not weakened to optional).
assert_jq "browser-no-idp-forms username-password REQUIRED" \
  '.authenticationFlows[] | select(.alias=="browser-no-idp-forms") | (.authenticationExecutions[] | select(.authenticator=="auth-username-password-form") | .requirement)' \
  "REQUIRED"

# The original `browser` flow is UNTOUCHED: still builtIn:true, still
# references the bare built-in `forms` (do NOT break SSO-app delegation).
assert_jq "original browser flow stays builtIn:true" \
  '.authenticationFlows[] | select(.alias=="browser") | .builtIn' \
  "true"
assert_jq "original browser flow still references built-in forms" \
  '.authenticationFlows[] | select(.alias=="browser") | (.authenticationExecutions[] | select(.autheticatorFlow==true) | .flowAlias)' \
  "forms"

echo "=== #4163 PASS: subflow resolution guarded across all realm templates ==="
