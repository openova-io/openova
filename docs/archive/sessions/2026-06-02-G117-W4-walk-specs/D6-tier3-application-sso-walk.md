# G117 W4.D6 — Tier-3 Application SSO walk (Matrix + LibreChat + Temporal-UI silent-launch from Catalyst console)

> **Goal**: Prove the Launch button (G117.4) on the Catalyst console drives silent SSO into Tier-3 tenant apps — Matrix, LibreChat, Temporal-UI — each living in a per-Org realm (W2.C4) federated to the sovereign realm.
>
> **Pillar**: 1 (operator self-serve) + 4 (sandbox + auto-mounted MCP — Tier-3 apps include LibreChat which is the AI surface) + 5 (tenant-realm sovereignty)
>
> **Cited memory**: `feedback_g113_sso_idr_defaultprovider_fix.md`, `feedback_sso_manual_recovery_procedure.md`, `feedback_chart_credential_persistence_defense.md`

## Pre-flight env state

| # | State | How to verify |
|---|---|---|
| P-1 | Fresh prov OR existing prov with D1+D5 PASSED | TRUST.md |
| P-2 | Org `acme` exists with: per-Org realm (W2.C4), iac repo (W2.C3), Tier-3 apps installed via Catalyst console — Matrix, LibreChat, Temporal | `kubectl get applications -n acme \| grep -E "matrix\|librechat\|temporal"` shows 3 rows Ready |
| P-3 | Launch button + silent-SSO URL minting (G117.4 / W1.B4) deployed | `curl -sf https://api.t<NN>.omani.works/catalyst/v1/healthz \| jq '.features.launchUrl'` returns `true` |
| P-4 | Each Tier-3 app's Endpoint has `ssoEnabled: true, launchDefault: true` | `kubectl get application matrix -n acme -o yaml \| yq '.status.endpoints[]'` |

## Walk procedure

### Step 1 — Authenticate at console once (baseline)

| # | Action | Expected | Probe |
|---|---|---|---|
| 1.1 | Open `https://console.t<NN>.omani.works/orgs/acme` in fresh browser context, sign in as `acme-admin@example.com` (Org-admin role) | Authenticated; user-menu shows acme-admin | Screenshot |
| 1.2 | Note: D5 already proved per-Org realm exists; this walk leverages that | n/a | n/a |

### Step 2 — Launch into acme/Matrix (Tier-3 #1)

| # | Action | Expected | Probe |
|---|---|---|---|
| 2.1 | Navigate to `/orgs/acme/apps/<matrix-app-id>` in console | Application detail page renders with: instance metadata, per-cluster status, Endpoints tab, Launch button | Screenshot |
| 2.2 | Click Launch button | `GET /apps/<id>/launch-url` fires; returns URL with `prompt=none&kc_idp_hint=catalyst-pin` for the Matrix Endpoint | Network HAR; Launch URL captured |
| 2.3 | New tab opens to Matrix | URL pattern: `https://matrix.acme.t<NN>.omani.works/?prompt=none&kc_idp_hint=catalyst-pin` (or Matrix-specific OIDC entry path) | HAR |
| 2.4 | 6-hop SSO chain completes (per D5 chain expectations) | Hops: Matrix → realms/acme → realms/sovereign (with kc_idp_hint) → catalyst-pin authz → back through tokens × 2 | HAR with explicit redirect chain |
| 2.5 | Time-to-tab measured | <800ms (Tier-3 budget = Tier-1 + 300ms for extra federation hop) | Playwright timing |
| 2.6 | Matrix `/welcome` renders authenticated | User is `@acme-admin:acme.t<NN>.omani.works` | Screenshot |

### Step 3 — Launch into acme/LibreChat (Tier-3 #2)

| # | Action | Expected | Probe |
|---|---|---|---|
| 3.1 | Back in console, navigate to LibreChat Application | Same UI shape | Screenshot |
| 3.2 | Click Launch | New tab with `prompt=none&kc_idp_hint=catalyst-pin` | HAR |
| 3.3 | 6-hop chain completes; LibreChat dashboard renders | User is the same Org-admin; LibreChat session token in cookies | Screenshot |
| 3.4 | Time-to-tab | <800ms (or <500ms if cookies cached from Step 2 — measure both cold + warm) | Playwright timing |
| 3.5 | Verify Sandbox MCP auto-mount works in LibreChat | (Pillar 4 cross-check — out of scope for D6 itself but cited in OPENAPI for completeness) — open a chat, ask "what is the catalyst-api endpoint here?", assistant responds with the prov's actual FQDN | LibreChat screenshot |

### Step 4 — Launch into acme/Temporal-UI (Tier-3 #3)

| # | Action | Expected | Probe |
|---|---|---|---|
| 4.1 | Back in console, navigate to Temporal Application | Same UI shape | Screenshot |
| 4.2 | Click Launch | New tab with `prompt=none&kc_idp_hint=catalyst-pin` | HAR |
| 4.3 | 6-hop chain completes; Temporal Web UI renders | List of workflows visible; user identity in top-right matches | Screenshot |
| 4.4 | Time-to-tab | <800ms cold; <500ms warm | Playwright timing |

### Step 5 — One-shot Launch URL contract

| # | Action | Expected | Probe |
|---|---|---|---|
| 5.1 | Click Launch on Matrix again. Copy URL from new tab's address bar BEFORE the SSO redirect chain happens (use `await page.url()` immediately after `popup`) | URL with one-shot token + 60s expiry | DevTools |
| 5.2 | In a separate incognito context, paste the SAME URL within 60s | Either: (a) one-shot already burned → 401 with "re-authenticate via console" message, OR (b) redirected to interactive login | curl |
| 5.3 | Try the same URL after 60s | 401 expired-token | curl |

### Step 6 — Multi-Tier session continuity

| # | Action | Expected | Probe |
|---|---|---|---|
| 6.1 | All 3 Tier-3 tabs still open from Steps 2-4 | n/a | n/a |
| 6.2 | Sign out of console | `POST /protocol/openid-connect/logout` fires; cascades to per-Org realm via back-channel logout | DevTools |
| 6.3 | Reload each of the 3 Tier-3 tabs | Each redirects to login (acme realm) | Screenshots |
| 6.4 | Sign back in at console; re-click Launch on Matrix | Silent SSO completes (<500ms warm) | Playwright |

### Step 7 — Verify Endpoint with `ssoEnabled: false` rejects Launch (negative test)

| # | Action | Expected | Probe |
|---|---|---|---|
| 7.1 | Install a Tier-3 app whose default Endpoint has `ssoEnabled: false` (or temporarily mutate one via D3-style rename: edit `librechat` endpoint to set `ssoEnabled: false`) | Endpoint state updates after PR merge | per D3 |
| 7.2 | Click Launch on that app in console | API returns 409 per OpenAPI: `Error{code: "sso-not-enabled", message: "..."}` | curl |
| 7.3 | UI surfaces clear message: "This endpoint does not support silent SSO. Open the URL manually and authenticate." | Toast + clickable plain URL fallback | Screenshot |

## Self-verification curl probes

```bash
# Probe 1 — Launch URL endpoint behavior
APP_ID=$(kubectl get application -n acme matrix -o jsonpath='{.metadata.uid}')
RESP=$(curl -sf -H "Authorization: Bearer $ORG_TOKEN" \
  "https://api.t<NN>.omani.works/catalyst/v1/apps/$APP_ID/launch-url")
echo $RESP | jq
# Expect: {url: "https://matrix.acme.t<NN>...?prompt=none&kc_idp_hint=catalyst-pin&...", expiresAt: "<ISO+60s>", endpoint: "matrix"}

# Probe 2 — Launch URL is one-shot (replay test)
URL=$(echo $RESP | jq -r .url)
# First use:
curl -sIL "$URL" 2>&1 | grep "HTTP/" | head -10
# Second use (within 60s):
sleep 5
curl -sIL "$URL" 2>&1 | grep "HTTP/" | head -10
# Expect: First chain completes; second returns 401 or redirects to interactive login

# Probe 3 — All 3 Tier-3 apps silent-SSO
for app in matrix librechat temporal; do
  APP_ID=$(kubectl get application -n acme $app -o jsonpath='{.metadata.uid}')
  RESP=$(curl -sf -H "Authorization: Bearer $ORG_TOKEN" \
    "https://api.t<NN>.omani.works/catalyst/v1/apps/$APP_ID/launch-url")
  URL=$(echo $RESP | jq -r .url)
  TIME=$({ time curl -sLI "$URL" -o /dev/null; } 2>&1 | grep real | awk '{print $2}')
  echo "$app: $TIME"
done
# Expect: each <800ms

# Probe 4 — 6-hop chain verification for Matrix
curl -sIL "$URL_FOR_MATRIX" 2>&1 | grep -E "HTTP/|location:" | wc -l
# Expect: ≥ 6 (3 redirects × 2 for the request/response pair counting) i.e. ≥6 lines

# Probe 5 — Negative: ssoEnabled=false
# (Patch the endpoint via D3 procedure first, then:)
RESP=$(curl -sf -H "Authorization: Bearer $ORG_TOKEN" \
  "https://api.t<NN>.omani.works/catalyst/v1/apps/$APP_ID_LIBRECHAT/launch-url" \
  -w "%{http_code}")
echo $RESP
# Expect: HTTP 409 with body Error{code: "sso-not-enabled"}

# Probe 6 — Back-channel logout cascade
# After Step 6.2 (console logout):
curl -sIL -b /tmp/matrix-cookies "https://matrix.acme.t<NN>.omani.works/" 2>&1 | grep "HTTP/" | head -3
# Expect: redirect to login (NOT 200 — session must have been invalidated)
```

## Evidence on TRUST.md format

```markdown
| 2026-06-0X | t<NN>.omani.works | Tier-3 Launch button silent-SSO (Matrix+LibreChat+Temporal) | VERIFIED-PASS | g117-w4-d6 | 3 Tier-3 apps launch silently from console <800ms cold/<500ms warm; one-shot URL contract enforced; back-channel logout cascades; ssoEnabled=false rejected 409 | screenshots/g117-w4-d6-{matrix,librechat,temporal,launch-url,one-shot-rejected,logout-cascade}.png | <walker> |
```

## Expected HTTP codes summary

| Probe | Method | URL | Expected code |
|---|---|---|---|
| Get launch URL | GET | `/api/apps/<id>/launch-url` | 200 |
| Get launch URL — ssoEnabled=false | GET | same | 409 |
| Launch URL chain (Matrix) | GET | minted URL | 302×5 then 200 |
| Launch URL replay (one-shot) | GET | same URL second time within 60s | 401 |
| Launch URL after 60s | GET | same URL after 60s | 401 |
| Back-channel logout cascade | GET | tenant app after console logout | 302 → login (NOT 200) |

## Screenshot capture points

| # | Filename pattern | Moment |
|---|---|---|
| 1 | `<date>-g117-w4-d6-console-org-acme-apps-page.png` | Step 1.1 |
| 2 | `<date>-g117-w4-d6-matrix-launched-authenticated.png` | Step 2.6 |
| 3 | `<date>-g117-w4-d6-librechat-launched-authenticated.png` | Step 3.3 |
| 4 | `<date>-g117-w4-d6-librechat-mcp-auto-mounted.png` | Step 3.5 (pillar 4 cross-check) |
| 5 | `<date>-g117-w4-d6-temporal-launched-authenticated.png` | Step 4.3 |
| 6 | `<date>-g117-w4-d6-launch-url-response.png` | Step 5.1 (DevTools showing minted URL) |
| 7 | `<date>-g117-w4-d6-launch-url-replay-rejected.png` | Step 5.2 |
| 8 | `<date>-g117-w4-d6-logout-cascade-all-3-redirect.png` | Step 6.3 (3 tabs showing login screens after console logout) |
| 9 | `<date>-g117-w4-d6-sso-not-enabled-409.png` | Step 7.2 |

## Failure-mode triage

- **Launch button missing on Application page**: G117.4 / W1.B4 scaffold not wired to real API; check console route file
- **Launch URL response missing kc_idp_hint**: catalyst-api launch handler bug; verify per OpenAPI spec
- **6-hop chain shows interactive prompt at any hop**: per-Org realm IDR misconfigured (D5 should have caught this; cross-reference D5 if D6 fails here)
- **Time-to-tab > 800ms cold**: profile each hop with HAR; bp-keycloak Pod might be cold-starting (check `kubectl get pod -n keycloak`)
- **One-shot URL accepted on replay**: token cache backend not respecting single-use; check catalyst-api launch handler implementation
- **Back-channel logout does NOT cascade**: KC client `backchannel_logout_url` not configured for Tier-3 apps; bp-sso-bridge reconciler missing this on per-Org realm Client registration
- **LibreChat MCP not auto-mounted**: pillar 4 cross-failure; openova-sandbox-mcp configmap not provisioned for the Org
- **ssoEnabled=false returns 200 with the URL anyway**: catalyst-api `/launch-url` gate not honoring the Endpoint flag; would be a CRITICAL severity finding
