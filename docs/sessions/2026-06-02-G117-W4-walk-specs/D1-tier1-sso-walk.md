# G117 W4.D1 — Tier-1 SSO walk (Grafana + Gitea + Harbor + OpenBao silent-launch on fresh prov)

> **Goal**: Prove all 4 Tier-1 SSO-capable apps log in silently (<500ms per app) on a fresh single-region Sovereign with ZERO manual KC/realm config — i.e. the G91/G112/G113 chain (manual recovery procedure) is fully codified into Wave-1/2 changes and no longer requires manual intervention.
>
> **Pillar**: 1 (marketplace + voucher onboarding) + 5 (sovereign independence — proven by single-region cutover-then-walk)
>
> **Cited memory**: `feedback_sso_manual_recovery_procedure.md`, `feedback_g113_sso_idr_defaultprovider_fix.md`, `feedback_chart_credential_persistence_defense.md`

## Pre-flight env state (must hold before step 1)

| # | State | How to verify |
|---|---|---|
| P-1 | Fresh prov via canonical `POST /sovereign/api/v1/deployments` — single-region (`regions: [{code: "fsn1-a"}]`); LE TLD selected per rotation; tagged `g117-w4-d1` | `curl -sf https://api.openova.io/sovereign/api/v1/deployments/<id>` returns `status: ready` |
| P-2 | `bp-self-sovereign-cutover` Job completed; `cutoverComplete=true` | `kubectl get sovereign t<NN> -o yaml \| yq '.status.cutoverComplete'` returns `true` |
| P-3 | Sovereign-admin user provisioned with default password (NOT mothership-managed); operator should rotate to a long random within the first minute of the walk | Check that `auth.t<NN>.omani.works` 200s + login page shows `operator@t<NN>.omani.works` |
| P-4 | All 4 Tier-1 apps reconciled to Ready in `catalyst-control-plane` namespace | `kubectl get hr -n catalyst-control-plane -l app.kubernetes.io/name in (grafana,gitea,harbor,openbao)` shows 4 rows with `Ready=True` |
| P-5 | NO manual `kubectl edit` ever applied to KC realm or to any SSO Secret since prov start | `kubectl get events -n keycloak --sort-by='.lastTimestamp' \| grep -v Created` shows zero `Patched\|Updated` events |

If any pre-flight FAILs: STOP. The walk cannot proceed honestly because the codification work is incomplete.

## Walk procedure

### Step 1 — Authenticate against Catalyst console once

| # | Action | Expected | Evidence |
|---|---|---|---|
| 1.1 | Open `https://console.t<NN>.omani.works/` in a clean browser context (no cookies) | Redirected to `https://auth.t<NN>.omani.works/realms/sovereign/protocol/openid-connect/auth?...` | Screenshot of KC sovereign-realm login page; URL bar shows `realms/sovereign` |
| 1.2 | Enter sovereign-admin creds; submit | Redirected back to `https://console.t<NN>.omani.works/` authenticated | Screenshot of console home with sovereign-admin in the user-menu |
| 1.3 | Inspect cookies | `catalyst_session` cookie present with `Secure; HttpOnly; SameSite=Lax`; KC `KEYCLOAK_SESSION` cookie present on `auth.t<NN>.omani.works` | `document.cookie` output (redact values) |

### Step 2 — Silent-SSO into Grafana (Tier-1 #1)

| # | Action | Expected | Curl probe / Evidence |
|---|---|---|---|
| 2.1 | Click Grafana in console BSS sidebar (or navigate to `/apps/<grafana-app-id>` and click the Launch button per G117.4) | New tab opens with URL `https://grafana.t<NN>.omani.works/?prompt=none&kc_idp_hint=catalyst-pin` | `await page.waitForEvent('popup')` in Playwright; HAR capture |
| 2.2 | Tab navigates through 4-hop chain | Hops: (a) grafana → auth.t<NN>/realms/sovereign/protocol/openid-connect/auth?prompt=none&kc_idp_hint=catalyst-pin (b) sovereign realm IDR auto-redirects to catalyst-pin provider (c) catalyst-pin issues authz code (d) grafana exchanges code → session | HAR shows exactly 4 hops; no interactive prompt | `curl -sLI -b /tmp/cookies https://grafana.t<NN>.omani.works/ \| grep -E "HTTP/|location:"` shows 4-hop chain |
| 2.3 | Time-to-tab measured | <500ms wall-clock from click to Grafana dashboard rendered | Playwright `performance.now()` delta; HAR `pageref` timing |
| 2.4 | Inspect Grafana session | Logged in as `operator@t<NN>` with role `Admin` (mapped from `groups: [sovereign-admins]` claim) | Screenshot of Grafana top-right user menu |

### Step 3 — Silent-SSO into Gitea (Tier-1 #2)

| # | Action | Expected | Probe |
|---|---|---|---|
| 3.1 | Back to console. Navigate to Gitea Application; click Launch | New tab opens with `https://gitea.t<NN>.omani.works/user/oauth2/catalyst-pin?prompt=none` | HAR |
| 3.2 | 4-hop chain completes | No interactive prompt; user lands at Gitea `/dashboard` | `curl -sLI -b /tmp/cookies https://gitea.t<NN>.omani.works/ -w "%{http_code}\n"` returns `200` |
| 3.3 | Time-to-tab | <500ms | Playwright timing |
| 3.4 | Inspect Gitea session | User exists in Gitea DB with `gitea-admin: true` (KC `gitea-admins` role → Gitea admin mapping) | `curl -sf -H "Authorization: token $UI_TOKEN" https://gitea.t<NN>.omani.works/api/v1/user \| jq '.is_admin'` returns `true` |

### Step 4 — Silent-SSO into Harbor (Tier-1 #3)

| # | Action | Expected | Probe |
|---|---|---|---|
| 4.1 | Back to console. Navigate to Harbor Application; click Launch | New tab opens with `https://harbor.t<NN>.omani.works/c/oidc/login?prompt=none` | HAR |
| 4.2 | Harbor's EXT_ENDPOINT correctly patched | Harbor's `core` Pod env shows `EXT_ENDPOINT=https://harbor.t<NN>.omani.works` (not the in-cluster Service name; this is the patch from the manual-recovery procedure that MUST be codified by Wave-1/2) | `kubectl exec -n harbor harbor-core-0 -- env \| grep EXT_ENDPOINT` returns the public FQDN |
| 4.3 | 4-hop chain completes | User lands at Harbor `/projects` | `curl -sLI -b /tmp/cookies https://harbor.t<NN>.omani.works/c/oidc/login -w "%{http_code}\n"` returns `200` |
| 4.4 | Time-to-tab | <500ms | Playwright timing |

### Step 5 — Silent-SSO into OpenBao (Tier-1 #4)

| # | Action | Expected | Probe |
|---|---|---|---|
| 5.1 | Back to console. Navigate to OpenBao Application; click Launch | New tab opens with `https://vault.t<NN>.omani.works/ui/vault/auth?with=oidc&prompt=none` | HAR |
| 5.2 | OpenBao OIDC method enabled (not the default token method) | `vault auth list` shows `oidc/` with `accessor: auth_oidc_<hash>` | `curl -sf -H "X-Vault-Token: $ADMIN" https://vault.t<NN>.omani.works/v1/sys/auth \| jq 'keys[]'` includes `oidc/` |
| 5.3 | OpenBao root regen happened during bootstrap (codification target — manual procedure today) | `kubectl logs -n openbao openbao-0 \| grep "root token generated"` shows the auto-regen event from a bootstrap Job, NOT a manual `bao operator generate-root` invocation | Bootstrap log lines + matching Job CR with `OwnerReference: openbao-root-regen` |
| 5.4 | 4-hop chain completes | User lands at OpenBao UI as `operator@t<NN>` with `sovereign-admins` group → mapped to `admin` policy | Screenshot |
| 5.5 | Time-to-tab | <500ms | Playwright timing |

### Step 6 — Cross-app session continuity (the punchline)

| # | Action | Expected | Probe |
|---|---|---|---|
| 6.1 | While Grafana + Gitea + Harbor + OpenBao tabs all still open and authenticated, navigate console to a 5th Tier-1 app if one is added (none today; reserved) | n/a | n/a |
| 6.2 | Close all 4 Tier-1 tabs. Re-click Launch on Grafana. | New tab opens, silent SSO completes in <300ms (faster because cookies cached) | Playwright timing |
| 6.3 | Sign out of Catalyst console via top-right menu | `POST /protocol/openid-connect/logout` fires; all 4 Tier-1 apps' subsequent requests get a 401 + redirect to login | Cypress/Playwright multi-tab check |

## Self-verification curl probes (must all return as documented)

```bash
# Probe 1 — Catalyst session cookie shape
curl -sIv -c /tmp/cookies -L https://console.t<NN>.omani.works/auth/login 2>&1 | grep -i "set-cookie"
# Expect: catalyst_session=<base64>; Path=/; Secure; HttpOnly; SameSite=Lax

# Probe 2 — Grafana silent SSO chain (4 hops; -L follows redirects)
curl -sIL -b /tmp/cookies "https://grafana.t<NN>.omani.works/?prompt=none&kc_idp_hint=catalyst-pin" 2>&1 | grep -E "HTTP/|location:" | head -20
# Expect: 4 hops; final HTTP/2 200; no `location: .*login` interactive prompt

# Probe 3 — Gitea silent SSO
curl -sIL -b /tmp/cookies "https://gitea.t<NN>.omani.works/user/oauth2/catalyst-pin?prompt=none" 2>&1 | grep -E "HTTP/|location:" | head -20

# Probe 4 — Harbor EXT_ENDPOINT correctness
kubectl exec -n harbor -c core deploy/harbor-core -- env | grep -E "EXT_ENDPOINT="
# Expect: EXT_ENDPOINT=https://harbor.t<NN>.omani.works (NOT http://harbor-core:80)

# Probe 5 — OpenBao OIDC enabled
curl -sf -H "X-Vault-Token: $(kubectl get secret -n openbao openbao-root -o jsonpath='{.data.token}' | base64 -d)" \
  https://vault.t<NN>.omani.works/v1/sys/auth | jq -r 'to_entries[] | .key'
# Expect: includes "oidc/"
```

## Evidence on TRUST.md format

After walk: append to `docs/ledger/TRUST.md`:

```markdown
| 2026-06-0X | t<NN>.omani.works | Tier-1 SSO (Grafana+Gitea+Harbor+OpenBao silent-launch) | VERIFIED-PASS | g117-w4-d1 | screenshots/g117-w4-d1-{grafana,gitea,harbor,openbao}.png + HAR /var/log/walks/g117-w4-d1.har | <walker handle> |
```

If ANY of the 4 apps fails the time-to-tab budget or any 4-hop chain shows an interactive prompt: VERIFIED-FAIL, do NOT close the EPIC, file a follow-up.

## Screenshot capture points

| # | Filename pattern | Moment |
|---|---|---|
| 1 | `<date>-g117-w4-d1-console-home-authenticated.png` | After Step 1.2 |
| 2 | `<date>-g117-w4-d1-grafana-silent-sso.png` | After Step 2.4 (Grafana dashboard rendered) |
| 3 | `<date>-g117-w4-d1-gitea-silent-sso.png` | After Step 3.2 (Gitea dashboard rendered) |
| 4 | `<date>-g117-w4-d1-harbor-silent-sso.png` | After Step 4.3 (Harbor projects page rendered) |
| 5 | `<date>-g117-w4-d1-openbao-silent-sso.png` | After Step 5.4 (OpenBao UI rendered with admin policy) |
| 6 | `<date>-g117-w4-d1-multi-tab-still-active.png` | After Step 6.2 (all 4 tabs visible, all authenticated) |
| 7 | `<date>-g117-w4-d1-logout-cascade.png` | After Step 6.3 (4 tabs all showing login pages after logout) |

## Expected HTTP codes summary

| Probe | Method | URL | Expected code |
|---|---|---|---|
| Console login | GET | `/auth/login` | 302 → KC then 200 |
| Grafana launch | GET | `https://grafana.t<NN>.../?prompt=none&kc_idp_hint=catalyst-pin` | 302×3 then 200 |
| Gitea launch | GET | `https://gitea.t<NN>.../user/oauth2/catalyst-pin?prompt=none` | 302×3 then 200 |
| Harbor launch | GET | `https://harbor.t<NN>.../c/oidc/login?prompt=none` | 302×3 then 200 |
| OpenBao launch | GET | `https://vault.t<NN>.../ui/vault/auth?with=oidc&prompt=none` | 302×3 then 200 |
| Logout | POST | `/protocol/openid-connect/logout` | 302 → 200 |
| Post-logout Grafana | GET | `https://grafana.t<NN>.../` | 302 → KC login (NOT in-app) |

## Failure-mode triage

- **If 4-hop chain shows 5+ hops**: codification gap; check `kcBrokerURL` not re-emitted (PR #2731 must be honored)
- **If interactive prompt surfaces**: IDR `defaultProvider=catalyst-pin` missing on the sovereign realm — verify configmap-sovereign-realm.yaml in current chart
- **If time-to-tab >500ms**: profile via HAR; common culprit = cert-manager Certificate not yet Ready (Probe 4 alternative test)
- **If Harbor login lands on `harbor-core:80` URL**: EXT_ENDPOINT patch not codified
- **If OpenBao login redirects to root-token prompt**: bootstrap regen Job not codified
