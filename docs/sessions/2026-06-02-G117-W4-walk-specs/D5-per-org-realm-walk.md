# G117 W4.D5 — Per-Org realm walk (2 Orgs each with their own Matrix; cross-Org isolation)

> **Goal**: Prove the 2-hop SSO federation (W2.C4): each Org has its own Keycloak realm with a `sovereign-broker` KC OIDC IdP federated to the sovereign realm broker. Tier-3 apps (Matrix) live in per-Org realms. Cross-Org access is hard-denied.
>
> **Pillar**: 1 (per-Org self-serve) + 5 (sovereign independence with no per-tenant tether)
>
> **Cited memory**: `feedback_g113_sso_idr_defaultprovider_fix.md`, `feedback_sso_manual_recovery_procedure.md`, `feedback_chart_credential_persistence_defense.md`

## Pre-flight env state

| # | State | How to verify |
|---|---|---|
| P-1 | Fresh prov, single-region acceptable | Standard |
| P-2 | bp-keycloak chart with per-Org realm template (W2.C4) deployed | `kubectl exec -n keycloak deploy/keycloak -- ls /opt/keycloak/data/import/` includes `tenant-realm.json` template OR runtime template materialized by sso-bridge |
| P-3 | bp-sso-bridge with ProvisionTenantRealm reconciler (W2.C4) deployed | `kubectl get deploy -n catalyst-control-plane bp-sso-bridge -o yaml \| yq '.spec.template.spec.containers[0].image'` matches W2.C4 merged-main SHA |
| P-4 | Two Orgs `acme` + `beta` exist with Environments + IaC repos provisioned (D3 + W2.C3 prereq) | `curl -sf https://gitea.t<NN>.omani.works/api/v1/repos/acme/iac` 200 + same for beta |
| P-5 | bp-matrix Application installed in each Org's vCluster | `kubectl --kubeconfig=<acme-vcluster> get hr -n acme matrix` Ready + same for beta |

## Walk procedure

### Step 1 — Verify per-Org realm provisioning

| # | Action | Expected | Probe |
|---|---|---|---|
| 1.1 | List realms in KC | At least 3: `master` (KC default), `sovereign`, `acme`, `beta` | `curl -sf -H "Authorization: Bearer $KC_ADMIN" https://auth.t<NN>.omani.works/admin/realms \| jq -r '.[].realm'` |
| 1.2 | Verify each Org realm has `sovereign-broker` IdP | For each of acme + beta: GET `/admin/realms/<org>/identity-provider/instances/sovereign-broker` returns 200 with `providerId: keycloak-oidc` | curl per Org |
| 1.3 | Verify each Org realm's IDR has `defaultProvider=sovereign-broker` | `curl -sf -H "Authorization: Bearer $KC_ADMIN" .../admin/realms/<org>/authentication/flows/browser/executions \| jq '.[] \| select(.providerId=="identity-provider-redirector") \| .authenticationConfig'` returns a config UUID; following that config returns `config: {defaultProvider: "sovereign-broker"}` | curl |
| 1.4 | Verify sovereign realm has a corresponding `<org>-broker` Client per Org | `curl -sf -H "Authorization: Bearer $KC_ADMIN" .../admin/realms/sovereign/clients?clientId=acme-broker \| jq '.[0]'` returns Client with redirectUris including `https://auth.t<NN>.omani.works/realms/acme/broker/sovereign-broker/endpoint` | curl |
| 1.5 | Verify `groups` scope present + attached to Matrix client in each Org realm | `curl -sf -H "Authorization: Bearer $KC_ADMIN" .../admin/realms/acme/clients/<matrix-client-uuid>/default-client-scopes \| jq '.[].name'` includes `groups` | curl |

### Step 2 — Verify 2-hop SSO works for acme/matrix

| # | Action | Expected | Probe |
|---|---|---|---|
| 2.1 | Open `https://matrix.acme.t<NN>.omani.works/` in fresh browser context | Redirected to `https://auth.t<NN>.omani.works/realms/acme/protocol/openid-connect/auth?...` (hop 1 — into acme realm) | HAR |
| 2.2 | acme realm's IDR auto-redirects | Next hop: `https://auth.t<NN>.omani.works/realms/sovereign/protocol/openid-connect/auth?...&kc_idp_hint=catalyst-pin` (hop 2 — into sovereign realm with kc_idp_hint for silent SSO) | HAR |
| 2.3 | sovereign realm's IDR auto-redirects via catalyst-pin | catalyst-pin issues authz code → back to sovereign realm → token issued → back to acme realm → token issued → back to Matrix | HAR shows 6-hop chain total (3 for the federation + 3 for token exchange) |
| 2.4 | End state: user lands in Matrix `/welcome` authenticated | User identifier in Matrix = `@<sovereign-username>:acme.t<NN>.omani.works` | Screenshot |
| 2.5 | Wall-clock time-to-tab measured | <800ms (Tier-3 has +300ms budget over Tier-1's 500ms because of the extra hop) | HAR timing |
| 2.6 | Inspect Matrix's id_token | Decoded JWT: `iss=https://auth.t<NN>.omani.works/realms/acme`, `aud=matrix`, `sub=<user-uuid-in-acme-realm>`, `groups=[<acme-groups>]` | jwt.io decode of cookie |

### Step 3 — Same chain for beta/matrix (independent realm)

| # | Action | Expected | Probe |
|---|---|---|---|
| 3.1 | New fresh browser context (no cookies from Step 2) | n/a | n/a |
| 3.2 | Open `https://matrix.beta.t<NN>.omani.works/` | Redirected to `https://auth.t<NN>.omani.works/realms/beta/protocol/openid-connect/auth?...` (NOTE: realm=beta not acme) | HAR |
| 3.3 | Federation hops complete via beta realm's `sovereign-broker` IdP | User lands in Matrix `/welcome` as `@<sovereign-user>:beta.t<NN>.omani.works` | Screenshot |
| 3.4 | Inspect Matrix beta id_token | `iss=...realms/beta`, `aud=matrix`, distinct `sub` from acme realm | jwt.io decode |
| 3.5 | Time-to-tab | <800ms | HAR timing |

### Step 4 — Cross-Org isolation (the critical defense)

| # | Action | Expected | Probe |
|---|---|---|---|
| 4.1 | In browser context from Step 2 (logged into acme/matrix), extract id_token | Token in localStorage or cookie | DevTools |
| 4.2 | Issue Matrix API call against `beta.matrix` with acme's token | HTTP 403 — `error: M_UNAUTHORIZED, error_description: token issued for realm 'acme', target 'beta'` | `curl -H "Authorization: Bearer <acme-token>" https://matrix.beta.t<NN>.omani.works/_matrix/client/r0/sync -w "%{http_code}\n"` returns 403 |
| 4.3 | Try via the Catalyst console proxy | Same 403; console UI does NOT silently swap tokens | Screenshot |
| 4.4 | Send a Matrix room invite from acme-user@acme.matrix to acme-user@beta.matrix | Either: (a) invite delivered but recipient must accept (still demonstrates cross-Org but via the proper Matrix-federation path, NOT auth bypass), OR (b) blocked at server-server federation layer if `bp-matrix.federation: per-org-only` (locked decision TBD; either is acceptable as long as it's intentional) | Matrix logs |
| 4.5 | sovereign realm token (catalyst-admin scope) against beta/matrix | 403 — Sovereign-admin is NOT auto-admin in tenant realms (per separation of duties) | curl |

### Step 5 — Per-Org realm independence (sovereign realm decoupling)

| # | Action | Expected | Probe |
|---|---|---|---|
| 5.1 | Stop sovereign realm's master client temporarily: `curl -X PUT -H "Authorization: Bearer $KC_ADMIN" .../admin/realms/sovereign/clients/<catalyst-pin>/disable` | catalyst-pin client disabled | KC UI |
| 5.2 | New SSO attempt into acme/matrix from a 3rd browser context | Stage 1 (acme realm) reaches the sovereign-broker IdP; sovereign realm rejects the auth because catalyst-pin is disabled | User sees clear error: "Sovereign realm authentication unavailable. Contact Sovereign admin." NOT a generic 500 |
| 5.3 | Re-enable catalyst-pin. Confirm SSO works again. | Same path resumes | per Step 2 probes |
| 5.4 | Try with sovereign realm's discovery endpoint UP but acme realm completely DELETED | Matrix shows clear error: "Authentication realm not found." (404 path through gateway) — NOT a generic 500 OR silent fail | curl |

### Step 6 — Per-Org realm secret rotation

| # | Action | Expected | Probe |
|---|---|---|---|
| 6.1 | Trigger rotation of `acme/sovereign-broker-secret` via the bp-sso-bridge admin API (or `kubectl annotate organization acme catalyst.openova.io/rotate-broker=now`) | New 32-char secret generated; written to OpenBao kv/org/acme/keycloak/sovereign-broker-secret; sovereign realm's `acme-broker` client secret updated; acme realm's sovereign-broker IdP secret updated; all atomic | bp-sso-bridge logs + OpenBao audit log |
| 6.2 | After rotation: new SSO attempt | Still works silently | per Step 2 probes |
| 6.3 | After rotation: existing SSO sessions | Either: (a) sessions remain valid until token expiry (graceful — preferred), OR (b) sessions immediately invalidated (security-first); document which the implementation picks | observation |

## Self-verification curl probes

```bash
# Probe 1 — Realm enumeration
curl -sf -H "Authorization: Bearer $KC_ADMIN" \
  https://auth.t<NN>.omani.works/admin/realms | jq -r '.[].realm'
# Expect: master, sovereign, acme, beta (at minimum)

# Probe 2 — Per-Org realm IDR config
for org in acme beta; do
  CFG_ID=$(curl -sf -H "Authorization: Bearer $KC_ADMIN" \
    https://auth.t<NN>.omani.works/admin/realms/$org/authentication/flows/browser/executions | \
    jq -r '.[] | select(.providerId=="identity-provider-redirector") | .authenticationConfig')
  curl -sf -H "Authorization: Bearer $KC_ADMIN" \
    https://auth.t<NN>.omani.works/admin/realms/$org/authentication/config/$CFG_ID | \
    jq -r '.config.defaultProvider'
done
# Expect: sovereign-broker × 2

# Probe 3 — 2-hop chain for acme/matrix
curl -sIL -c /tmp/acme-cookies "https://matrix.acme.t<NN>.omani.works/" 2>&1 | grep -E "HTTP/|location:" | head -20
# Expect: 6 hops; first redirect to realms/acme, then realms/sovereign with kc_idp_hint, then catalyst-pin, then back through

# Probe 4 — Matrix id_token verification (acme)
ACME_TOKEN=$(curl -sf -b /tmp/acme-cookies https://matrix.acme.t<NN>.omani.works/_matrix/client/r0/sync | jq -r '.access_token')
# Actually fetch a fresh OIDC token via the OIDC client_credentials flow OR extract from a real login
# Then decode:
echo "$ACME_TOKEN" | cut -d. -f2 | base64 -d 2>/dev/null | jq '{iss, aud, sub, groups}'
# Expect: iss=https://auth.t<NN>.omani.works/realms/acme, aud=matrix

# Probe 5 — Cross-Org isolation (acme token against beta)
curl -sf -H "Authorization: Bearer $ACME_TOKEN" \
  https://matrix.beta.t<NN>.omani.works/_matrix/client/r0/sync -w "\nHTTP %{http_code}\n"
# Expect: HTTP 401 or 403

# Probe 6 — sovereign token against beta (privilege escalation defense)
SOV_TOKEN=$(curl -sf -d 'grant_type=password&username=sovereign-admin&password=...&client_id=catalyst-pin' \
  https://auth.t<NN>.omani.works/realms/sovereign/protocol/openid-connect/token | jq -r .access_token)
curl -sf -H "Authorization: Bearer $SOV_TOKEN" \
  https://matrix.beta.t<NN>.omani.works/_matrix/client/r0/sync -w "\nHTTP %{http_code}\n"
# Expect: HTTP 401 or 403 (Sovereign-admin NOT auto-admin in tenant realms)

# Probe 7 — Secret rotation
kubectl annotate organization acme catalyst.openova.io/rotate-broker=$(date +%s) --overwrite
sleep 30
# Verify new secret in OpenBao
kubectl exec -n openbao openbao-0 -- bao kv get -format=json kv/org/acme/keycloak/sovereign-broker-secret | \
  jq '.data.metadata.created_time'
# Expect: recent (within the last 60s)
```

## Evidence on TRUST.md format

```markdown
| 2026-06-0X | t<NN>.omani.works | Per-Org KC realm 2-hop federation + cross-Org isolation | VERIFIED-PASS | g117-w4-d5 | acme + beta realms each with sovereign-broker IdP; matrix.acme + matrix.beta silent-SSO <800ms; cross-Org token rejected 403; rotation atomic | screenshots/g117-w4-d5-{realm-list,acme-sso-chain,beta-sso-chain,cross-org-403,rotation}.png | <walker> |
```

## Expected HTTP codes summary

| Probe | Method | URL | Expected code |
|---|---|---|---|
| Realm list | GET | `/admin/realms` | 200 with ≥4 realms |
| Per-Org IdP fetch | GET | `/admin/realms/<org>/identity-provider/instances/sovereign-broker` | 200 |
| Matrix silent-SSO chain | GET | `https://matrix.<org>.t<NN>.../` | 302×5 then 200 |
| Cross-Org token reject | GET | beta matrix with acme token | 401 or 403 |
| Sovereign token in tenant | GET | beta matrix with sovereign-admin token | 401 or 403 |

## Screenshot capture points

| # | Filename pattern | Moment |
|---|---|---|
| 1 | `<date>-g117-w4-d5-realm-list.png` | Step 1.1 (KC admin UI showing 4+ realms) |
| 2 | `<date>-g117-w4-d5-acme-realm-idr-config.png` | Step 1.3 (UI showing IDR defaultProvider=sovereign-broker) |
| 3 | `<date>-g117-w4-d5-acme-matrix-authenticated.png` | Step 2.4 |
| 4 | `<date>-g117-w4-d5-acme-matrix-id-token-decoded.png` | Step 2.6 (jwt.io paste with iss visible) |
| 5 | `<date>-g117-w4-d5-beta-matrix-authenticated.png` | Step 3.3 |
| 6 | `<date>-g117-w4-d5-cross-org-403.png` | Step 4.2 (curl with 403) |
| 7 | `<date>-g117-w4-d5-sov-token-rejected-in-tenant.png` | Step 4.5 |
| 8 | `<date>-g117-w4-d5-rotation-openbao-audit.png` | Step 6.1 |

## Failure-mode triage

- **Per-Org realm not created on Org/Environment create**: bp-sso-bridge reconciler not honoring Environment-creation event; check W2.C4 ProvisionTenantRealm
- **IDR config missing defaultProvider**: realm template malformed; verify against PR #2730 pattern verbatim
- **6-hop chain shows interactive prompt**: kc_idp_hint not propagated through the federation; check IdP `addReadTokenRoleOnCreate` config + the IDR fork-execution sequence
- **Cross-Org token NOT rejected**: bp-matrix not validating `iss` claim; this is a CRITICAL finding — file P0 immediately
- **Sovereign-admin token accepted in tenant realm**: per-realm role mapping bleeds; check KC client scope mappings
- **Rotation breaks SSO**: atomic update missed a path; OpenBao + KC sovereign-broker + KC tenant-broker secrets must all update in one transaction
- **Realm deletion leaves orphan sovereign-broker client**: organization-controller finalizer missing
