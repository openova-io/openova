# hw86 Runtime-Patch Audit + Image-Swap Procedure — pre-G117.0 Codification Gate

> **Refs**: parent EPIC #2737 — G117.0 hw86 codification audit (HARD pre-Wave-4 gate).
>
> **Scope**: READ-ONLY enumeration of every live runtime patch currently active on `hw86.omani.works`, cross-referenced to merged-main PR(s) that codify each patch. Produces the image-swap procedure + self-verification curl probes the future G117.0 closeout agent runs to flip hw86 from "patched runtime" to "pure-main" state.
>
> **NOT in scope**: actually applying the image swap, mutating runtime state, closing the gate. This is the inventory + procedure spec only.
>
> **Author**: `p0-474-lonely` (G117 Aux-3) — dispatched 2026-06-02.
> **Source kubeconfig**: `afc8800bc03751c6` deployment record on mothership catalyst-api PVC (per memory L9).

---

## 1. Live state enumeration on hw86 (raw kubectl excerpts)

### 1.1 — `catalyst-api` Deployment image & runtime env overrides

```bash
$ kubectl -n catalyst-system get deploy catalyst-api -o jsonpath='{.spec.template.spec.containers[0].image}'
registry.hw86.omani.works/cache/catalyst-api:f61a52a
```

Image tag `f61a52a` resolves to commit `f61a52ab ci(catalyst-build): emergency unblock — continue past pre-existing UI test failures (Refs #2725 G113)`. This is **pre** the four G113-followup PRs (#2729, #2731, #2733, #2734) whose binaries ship under tags `e0e210a`, `a776b39`, `e1cd849`, `4d9686d` (`4d9686d` = current latest catalyst-api image).

Runtime env overrides discovered (relevant to G113/G117 codification — only the patches, not the full env list):

```yaml
- name: CATALYST_OIDC_PROVIDER_ISSUER_URL
  value: "https://api.hw86.omani.works"        # runtime override; PR #2729 auto-derives this from SOVEREIGN_FQDN
- name: CATALYST_SESSION_COOKIE_DOMAIN
  value: ".hw86.omani.works"                   # runtime override; PR #2734 auto-derives this from SOVEREIGN_FQDN
```

Other env vars (e.g. `SOVEREIGN_FQDN` from sovereign-fqdn ConfigMap, `CATALYST_OIDC_PROVIDER_ENABLED=true`, `CATALYST_PIN_BROKER_CLIENT_ID=catalyst-pin`) are chart-rendered and not workarounds — listed in audit appendix only.

### 1.2 — HelmRelease + Kustomization suspension state

```bash
$ kubectl -n flux-system get hr -o json | jq -r '.items[] | select(.spec.suspend==true) | "\(.metadata.name)\tsuspend=\(.spec.suspend)\tchart=\(.spec.chart.spec.chart)\tversion=\(.spec.chart.spec.version)"'
bp-catalyst-platform           suspend=true   chart=bp-catalyst-platform           version=1.4.430
bp-cluster-autoscaler-hcloud   suspend=true   chart=bp-cluster-autoscaler-hcloud   version=1.3.0
bp-hcloud-ccm                  suspend=true   chart=bp-hcloud-ccm                  version=1.0.0
bp-velero                      suspend=true   chart=bp-velero                      version=1.2.2

$ kubectl -n flux-system get kustomization.kustomize.toolkit.fluxcd.io -o json | jq -r '.items[] | select(.spec.suspend==true) | .metadata.name'
bootstrap-kit
```

**Confirms** the parent-Kustomization + child-HR dual-suspend pattern from `feedback_g113_sso_idr_defaultprovider_fix.md` — both are suspended on hw86 so the manual `kubectl set env` of `CATALYST_*` runtime overrides on `catalyst-api` cannot get reverted by Flux reconcile. `bp-cluster-autoscaler-hcloud` / `bp-hcloud-ccm` / `bp-velero` suspends are unrelated to G117 — they are Hetzner-specific HRs intentionally not reconciled on the Huawei HCS Sovereign (`feedback_hetzner_hrs_unrendered_on_huawei.md` family).

`bp-catalyst-platform` HR `metadata.labels.kustomize.toolkit.fluxcd.io/name = bootstrap-kit` confirms the parent.

### 1.3 — UI bundle `kcBrokerURL` → `xxBrokerURL` sed substitution

```bash
$ kubectl -n catalyst-system exec deploy/catalyst-ui -- grep -o 'kcBrokerURL' /usr/share/nginx/html/assets/index-C1KKL1ZA.js | wc -l
0
$ kubectl -n catalyst-system exec deploy/catalyst-ui -- grep -o 'xxBrokerURL' /usr/share/nginx/html/assets/index-C1KKL1ZA.js | wc -l
2
```

Bundle has been sed-rewritten from `kcBrokerURL` → `xxBrokerURL` at runtime (2 hits = the JSX prop + the destructure or fallback site). This is the cosmetic workaround for "old UI code still consumes kcBrokerURL from the PIN-verify response, but the server stopped emitting it post-#2731". PR #2731 ships server-side; once the matching `catalyst-ui:e1cd849+` image rolls (current is `e1cd849`, so server-side may already match — see §1.5), the bundle no longer references kcBrokerURL and the sed substitution becomes a no-op.

Current `catalyst-ui` image:

```bash
$ kubectl -n catalyst-system describe pod -l app.kubernetes.io/name=catalyst-ui | grep Image:
    Image: ghcr.io/openova-io/openova/catalyst-ui:e1cd849
```

`e1cd849` = PR #2733 commit — the matching UI build IS post-#2731 (which is `a776b39`). So the sed workaround SHOULD be redundant for the UI side — verify post-swap.

### 1.4 — Keycloak `sovereign` realm config (live API queries)

#### 1.4.1 — IDR `defaultProvider=catalyst-pin` config bound

```bash
$ curl -sH "Authorization: Bearer $TOKEN" .../admin/realms/sovereign/authentication/flows/browser/executions | jq '.[] | select(.providerId=="identity-provider-redirector")'
{
  "id": "9a5216b9-7abf-47ea-84cd-3a3d348d6669",
  "requirement": "ALTERNATIVE",
  "displayName": "Identity Provider Redirector",
  "alias": "catalyst-pin-redirector",
  "providerId": "identity-provider-redirector",
  "authenticationConfig": "6f426fa2-5e83-430b-8ed1-e6d1371c495c"
}

$ curl ... /admin/realms/sovereign/authentication/config/6f426fa2-5e83-430b-8ed1-e6d1371c495c
{"id":"6f426fa2-...","alias":"catalyst-pin-redirector","config":{"defaultProvider":"catalyst-pin"}}
```

**This is codified** — chart `bp-keycloak 1.4.9` (slot 09, reconciled green on hw86) ships the same IDR config (per PR #2730). Cross-check: the realm CM contains the line.

```bash
$ kubectl -n keycloak get cm keycloak-sovereign-realm-config -o yaml | grep -E "(catalyst-pin-redirector|authenticatorConfig)" | head -5
          "alias": "catalyst-pin-redirector",
            { "authenticator": "identity-provider-redirector", "authenticatorConfig": "catalyst-pin-redirector", "requirement": "ALTERNATIVE", "priority": 25, "autheticatorFlow": false },
```

#### 1.4.2 — `catalyst-pin` IdP `disableNonce=true` (runtime workaround for #2733)

```bash
$ curl ... /admin/realms/sovereign/identity-provider/instances/catalyst-pin
{
  "alias": "catalyst-pin",
  "providerId": "oidc",
  "config": {
    "disableNonce": "true",            # ← runtime workaround
    "issuer": "https://api.hw86.omani.works",
    "validateSignature": "true",
    "useJwksUrl": "true",
    ...
  },
  "firstBrokerLoginFlowAlias": "first broker login"   # ← NOT "first broker login auto link" (PR #2735 still OPEN)
}
```

Two distinct workarounds visible on the catalyst-pin IdP:
- `disableNonce: "true"` — runtime hack because pre-#2733 catalyst-api didn't echo nonce. Image `e1cd849+` includes #2733, so post image-swap this can be flipped to `false`.
- `firstBrokerLoginFlowAlias: "first broker login"` — using KC's DEFAULT flow (which needs SMTP for account-linking confirmation; SMTP is broken on hw86). PR #2735 (still **OPEN**) codifies a custom `first broker login auto link` flow that pre-binds federated identity and skips SMTP. Until #2735 merges, the per-user federated-identity pre-link workaround (§1.4.3) is the only available workaround.

#### 1.4.3 — Per-user federated-identity pre-link (Bug-6 workaround)

```bash
$ curl ... /admin/realms/sovereign/users?email=emrah.baysal@openova.io | jq '.[0].id'
"266fb25e-5de2-4f08-8f21-34613d26f1d1"

$ curl ... /admin/realms/sovereign/users/266fb25e-.../federated-identity
[
  {
    "identityProvider": "catalyst-pin",
    "userId": "emrah.baysal@openova.io",
    "userName": "emrah.baysal@openova.io"
  }
]
```

The `emrah.baysal@openova.io` user has the catalyst-pin federated identity manually pre-bound, bypassing KC's "first broker login" SMTP confirmation. This is per-user and survives only as long as the realm DB does — but every NEW Sovereign-admin user needs the same pre-link until #2735 merges and bp-keycloak is bumped.

### 1.5 — Tier-1 chart pins on hw86 vs latest published

```
                       hw86 live    GHCR latest    Δ
bp-catalyst-platform   1.4.430      1.4.431        -1 (deploy bot pin lags by 1, normal)
bp-keycloak            1.4.9        1.5.0          -1 minor (1.5.0 is PR #2735 once merged?  TBC)
bp-openbao             1.2.22       1.2.22         even
bp-grafana             1.0.4        1.0.4          even
bp-gitea               1.2.12       1.2.12         even
bp-harbor              1.2.22       1.2.22         even
```

`bp-keycloak 1.4.9` ships #2730 (IDR codification — already live). `bp-keycloak 1.5.0` is published (per GHCR latest); whether it's #2735 codifying first-broker-login auto-link is TBD pending PR #2735 merge. Note `bp-catalyst-platform` is the umbrella chart that pulls in the catalyst-api subchart pin; image swap is independent of HR reconcile.

### 1.6 — Self-verification curl probes — current state

#### Probe A — `Set-Cookie Domain` on `/api/v1/auth/session` (expected: `.hw86.omani.works`)

```bash
$ curl -sk -i -X DELETE https://api.hw86.omani.works/api/v1/auth/session | grep -i set-cookie
set-cookie: catalyst_session=; Path=/; Domain=.hw86.omani.works; Secure; HttpOnly; SameSite=Lax; Max-Age=-1
set-cookie: catalyst_refresh=; Path=/; Domain=.hw86.omani.works; Secure; HttpOnly; SameSite=Lax; Max-Age=-1
```

**PASS** — Domain attribute is `.hw86.omani.works`. Currently driven by the **runtime env override** `CATALYST_SESSION_COOKIE_DOMAIN`; post image-swap to `4d9686d+` the same value is auto-derived from `SOVEREIGN_FQDN` (PR #2734).

#### Probe B — 4-hop SSO chain for Grafana (no cookie)

```bash
$ JAR=$(mktemp); curl -sk -c $JAR -o /dev/null -w "status=%{http_code} loc=%{redirect_url}\n" \
    https://grafana.hw86.omani.works/login/generic_oauth
status=302 loc=https://auth.hw86.omani.works/realms/sovereign/protocol/openid-connect/auth?...

$ curl -sk -b $JAR -c $JAR -o /dev/null -w "status=%{http_code} loc=%{redirect_url}\n" "$LOC"
status=303 loc=https://auth.hw86.omani.works/realms/sovereign/broker/catalyst-pin/login?session_code=...&client_id=grafana&tab_id=...
```

**PASS** — HOP2 returns `303 → /broker/catalyst-pin/login?session_code=…` (the canonical "IDR-delegated" hop), proving the `defaultProvider=catalyst-pin` IDR config is bound and functioning. No `200 OK` login form HTML returned. (gitea / harbor / openbao SSO entry URLs differ per-app — full chain only validated for grafana in this audit; per-app probes left for the W4.D1 closure agent.)

---

## 2. Codification cross-reference table — workaround ↔ PR

| # | Workaround on hw86 (CURRENT) | Expected post-codification (TARGET on pure-main) | Codifying PR | PR status | Cleanup action when chart/image swapped |
|---|---|---|---|---|---|
| **1** | `catalyst-api` image is `f61a52a` (pre-G113-followup builds) | `catalyst-api:4d9686d+` (or newer) | #2729, #2731, #2733, #2734 (all merged main) | MERGED ✅ | Roll `bp-catalyst-platform` reconcile so the chart's `image.tag` lands; OR `kubectl set image deploy/catalyst-api catalyst-api=registry.hw86.omani.works/cache/catalyst-api:4d9686d` after image is pushed to local registry |
| **2** | `CATALYST_OIDC_PROVIDER_ISSUER_URL=https://api.hw86.omani.works` env override | env var absent (Go derives from `SOVEREIGN_FQDN` ConfigMap) | #2729 | MERGED ✅ (`e0e210a`) | After image swap to `4d9686d`: `kubectl set env deploy/catalyst-api CATALYST_OIDC_PROVIDER_ISSUER_URL-` (note trailing `-` to remove) |
| **3** | `CATALYST_SESSION_COOKIE_DOMAIN=.hw86.omani.works` env override | env var absent (Go derives from `SOVEREIGN_FQDN` ConfigMap) | #2734 | MERGED ✅ (`4d9686d`) | After image swap: `kubectl set env deploy/catalyst-api CATALYST_SESSION_COOKIE_DOMAIN-` |
| **4** | catalyst-ui bundle has `xxBrokerURL` (2 hits), no `kcBrokerURL` — runtime sed substitution | Bundle ships clean: 0 hits of EITHER (HandlePinVerify response no longer emits kcBrokerURL) | #2731 | MERGED ✅ (`a776b39`) | catalyst-ui image is ALREADY `e1cd849` (post-#2731). Once `bp-catalyst-platform` is un-suspended and reconciles, the ConfigMap that drives the runtime sed (if any) becomes a no-op AND the next UI image rebuild emits a clean bundle. Verify post-swap with `kubectl exec deploy/catalyst-ui -- grep -c xxBrokerURL /usr/share/nginx/html/assets/index-*.js` returns `0 0` |
| **5** | KC `catalyst-pin` IdP `config.disableNonce: "true"` | `config.disableNonce: "false"` (or unset; default is false per OIDC Core §3.1.3.7) | #2733 | MERGED ✅ (`e1cd849`) | One-shot admin API: `curl -X PUT -H "Authorization: Bearer $T" -H "Content-Type: application/json" .../admin/realms/sovereign/identity-provider/instances/catalyst-pin -d '{"alias":"catalyst-pin","providerId":"oidc","config":{...,"disableNonce":"false"}}'` — OR re-import realm CM (which has disableNonce unset = false-default) via KC `bin/kc.sh import`. Realm CM template in bp-keycloak does NOT currently ship `disableNonce` so a fresh realm import picks up default `false`. |
| **6** | KC IDR `catalyst-pin-redirector` `config.defaultProvider=catalyst-pin` (codified, parity with main) | Same: chart-rendered IDR config remains | #2730 | MERGED ✅ (in `bp-keycloak 1.4.9` on hw86) | **No cleanup needed** — already aligned with main. Listed for completeness. |
| **7** | KC `catalyst-pin` IdP `firstBrokerLoginFlowAlias: "first broker login"` (DEFAULT flow, needs SMTP) | `firstBrokerLoginFlowAlias: "first broker login auto link"` (custom flow, skips SMTP via `idp-auto-link`) | #2735 | **OPEN** ❌ | **BLOCKED until #2735 merges** + `bp-keycloak` chart bump lands on hw86. Until then keep per-user federated-identity pre-link (§1.4.3) as containment. Image swap CAN PROCEED for items 1–6 independently of this row. |
| **8** | KC `sovereign` realm user `emrah.baysal@openova.io` has manual federated-identity pre-link to `catalyst-pin` | Pre-link no longer needed (custom auto-link flow handles new SSO-arriving users automatically) | #2735 | **OPEN** ❌ (codifying mechanism); per-user pre-link is the runtime patch | Once #2735 codified AND bp-keycloak chart bump reconciles, all new SSO-arriving users get auto-linked. Existing pre-link rows can be left in place (harmless) or deleted via `DELETE /admin/realms/sovereign/users/{id}/federated-identity/catalyst-pin`. |
| **9** | `bp-catalyst-platform` HR (slot 13) `spec.suspend: true` | `spec.suspend: false` (Flux drives chart pin) | n/a — suspension is purely runtime | n/a | `kubectl -n flux-system patch hr bp-catalyst-platform --type=merge -p '{"spec":{"suspend":false}}'` after image swap |
| **10** | `bootstrap-kit` Kustomization `spec.suspend: true` | `spec.suspend: false` | n/a | n/a | `kubectl -n flux-system patch kustomization.kustomize.toolkit.fluxcd.io bootstrap-kit --type=merge -p '{"spec":{"suspend":false}}'` (un-suspend in this order: parent Kustomization → HR — once HR is reconcileable, parent can reapply it without override surprises) |

**Summary**:
- **8 of 10 rows** have MERGED codifying PRs (rows 1–6 + 9–10 — rows 9/10 are pure suspend toggles, not patches).
- **2 of 10 rows** (rows 7 + 8) await PR #2735 merge — these are the Bug-6 first-broker-login-auto-link rows.
- Items 1–6 + 9–10 can be cleared in a SINGLE image-swap + un-suspend cycle.
- Items 7–8 require #2735 merge + chart bump first; they are independent of the image-swap rollout.

---

## 3. Image-swap procedure — copy-pasteable, idempotent

> **Prerequisites**:
> 1. kubeconfig at `/tmp/hw86-audit.kubeconfig` (per §1 enumeration step); set `export KUBECONFIG=/tmp/hw86-audit.kubeconfig` for all kubectl below
> 2. Target catalyst-api image tag `4d9686d` (or whatever is current latest on `ghcr.io/openova-io/openova/catalyst-api`) already PUSHED to the local registry `registry.hw86.omani.works/cache/catalyst-api`. Verify with `crane manifest registry.hw86.omani.works/cache/catalyst-api:4d9686d` BEFORE starting.
> 3. PR #2735 STATUS — if **MERGED** + `bp-keycloak 1.5.x` published, swap chart pin too (covered in §3 step 6). If still **OPEN**, defer rows 7/8 cleanup.

```bash
# ─────────────────────────────────────────────────────────────
# Pre-flight: confirm dual-suspend is still in effect
# ─────────────────────────────────────────────────────────────
kubectl -n flux-system get hr bp-catalyst-platform -o jsonpath='{.spec.suspend}{"\n"}'
# Expected: true
kubectl -n flux-system get kustomization.kustomize.toolkit.fluxcd.io bootstrap-kit -o jsonpath='{.spec.suspend}{"\n"}'
# Expected: true

# ─────────────────────────────────────────────────────────────
# Step 1 — swap catalyst-api image to post-#2734 build
# ─────────────────────────────────────────────────────────────
kubectl -n catalyst-system set image deploy/catalyst-api \
  catalyst-api=registry.hw86.omani.works/cache/catalyst-api:4d9686d
kubectl -n catalyst-system rollout status deploy/catalyst-api --timeout=5m

# ─────────────────────────────────────────────────────────────
# Step 2 — remove the two now-redundant runtime env overrides
#          (4d9686d auto-derives both from SOVEREIGN_FQDN)
# ─────────────────────────────────────────────────────────────
kubectl -n catalyst-system set env deploy/catalyst-api \
  CATALYST_OIDC_PROVIDER_ISSUER_URL- \
  CATALYST_SESSION_COOKIE_DOMAIN-
kubectl -n catalyst-system rollout status deploy/catalyst-api --timeout=5m

# ─────────────────────────────────────────────────────────────
# Step 3 — flip KC catalyst-pin IdP disableNonce → false
#          (post-#2733 catalyst-api echoes nonce per OIDC Core)
# ─────────────────────────────────────────────────────────────
KC_PASS=$(kubectl -n keycloak get secret keycloak -o jsonpath='{.data.admin-password}' | base64 -d)
KC_POD=$(kubectl -n keycloak get pod -l app.kubernetes.io/name=keycloak -o name | head -1)
TOKEN=$(kubectl -n keycloak exec $KC_POD -c keycloak -- \
  curl -sk -X POST http://localhost:8080/realms/master/protocol/openid-connect/token \
  -d "username=admin" -d "password=$KC_PASS" -d "grant_type=password" -d "client_id=admin-cli" \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["access_token"])')
# Fetch current IdP spec, patch disableNonce=false, PUT back
kubectl -n keycloak exec $KC_POD -c keycloak -- \
  curl -sk -H "Authorization: Bearer $TOKEN" \
  http://localhost:8080/admin/realms/sovereign/identity-provider/instances/catalyst-pin \
  > /tmp/idp.json
python3 -c "
import json
d = json.load(open('/tmp/idp.json'))
d['config']['disableNonce'] = 'false'
json.dump(d, open('/tmp/idp.json','w'))
"
kubectl -n keycloak cp /tmp/idp.json $KC_POD:/tmp/idp.json -c keycloak
kubectl -n keycloak exec $KC_POD -c keycloak -- \
  curl -sk -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  http://localhost:8080/admin/realms/sovereign/identity-provider/instances/catalyst-pin \
  -d @/tmp/idp.json -w "HTTP=%{http_code}\n"
# Expected: HTTP=204

# ─────────────────────────────────────────────────────────────
# Step 4 — un-suspend bp-catalyst-platform HR (slot 13)
# ─────────────────────────────────────────────────────────────
kubectl -n flux-system patch hr bp-catalyst-platform --type=merge \
  -p '{"spec":{"suspend":false}}'
kubectl -n flux-system wait hr/bp-catalyst-platform --for=condition=Ready --timeout=5m

# ─────────────────────────────────────────────────────────────
# Step 5 — un-suspend parent Kustomization (bootstrap-kit)
# ─────────────────────────────────────────────────────────────
kubectl -n flux-system patch kustomization.kustomize.toolkit.fluxcd.io bootstrap-kit \
  --type=merge -p '{"spec":{"suspend":false}}'
kubectl -n flux-system wait kustomization.kustomize.toolkit.fluxcd.io/bootstrap-kit \
  --for=condition=Ready --timeout=10m

# ─────────────────────────────────────────────────────────────
# Step 6 — (OPTIONAL — only if PR #2735 MERGED + bp-keycloak ≥1.5.0)
#         Bump bp-keycloak chart pin (handled by deploy bot once
#         PR #2735 lands on main; agent verifies pin matches GHCR)
# ─────────────────────────────────────────────────────────────
# Once new chart pin reconciles, the realm CM picks up the
# "first broker login auto link" custom flow and re-binds
# firstBrokerLoginFlowAlias on the catalyst-pin IdP automatically
# (idempotent — re-import on next KC restart, or manual reload
# via /admin/realms/sovereign/clear-realm-cache).

# ─────────────────────────────────────────────────────────────
# Step 7 — verify (next section)
# ─────────────────────────────────────────────────────────────
```

**Idempotency note**: every kubectl step is idempotent — re-running step 1 with the same image tag is a no-op; `set env VAR-` succeeds whether the env var was present or not; HR/Kustomization `patch --type=merge` with `suspend: false` is idempotent. The KC API PUT in step 3 is idempotent on `disableNonce`.

---

## 4. Self-verification curl probes — post-swap green criteria

Run each probe AFTER §3 steps 1–5 complete. All must pass for G117.0 GREEN.

### Probe A — cookie Domain still `.hw86.omani.works` (now from auto-derive, not env)

```bash
curl -sk -i -X DELETE https://api.hw86.omani.works/api/v1/auth/session | grep -i set-cookie
# EXPECT: Set-Cookie: catalyst_session=...; Domain=.hw86.omani.works; ...
# This proves PR #2734's SOVEREIGN_FQDN auto-derive lands the same Domain.
```

### Probe B — 4-hop no-cookie SSO probe per Tier-1 app

```bash
JAR=$(mktemp); rm -f $JAR
for app_url in \
  "https://grafana.hw86.omani.works/login/generic_oauth" \
  "https://gitea.hw86.omani.works/user/oauth2/keycloak" \
  "https://harbor.hw86.omani.works/c/oidc/login" \
  "https://openbao.hw86.omani.works/ui/vault/auth?with=oidc"; do
    echo "--- $app_url ---"
    LOC=$(curl -sk -c $JAR -o /dev/null -w "%{redirect_url}" "$app_url")
    echo "HOP1 loc: $LOC"
    LOC2=$(curl -sk -b $JAR -c $JAR -o /dev/null -w "%{redirect_url}" "$LOC")
    echo "HOP2 loc: $LOC2"
    # EXPECT HOP2 to contain "/broker/catalyst-pin/login?session_code=..."
    echo "$LOC2" | grep -q "broker/catalyst-pin/login.*session_code" && echo "  ✓ IDR delegation OK" || echo "  ✗ IDR delegation FAIL"
done
```

Per-app exact entry URL may need adjustment — the assertion is that HOP2 contains `/broker/catalyst-pin/login?session_code=` (KC's IDR has correctly delegated to catalyst-pin without showing the password form).

### Probe C — UI bundle clean (no `xxBrokerURL` / `kcBrokerURL`)

```bash
kubectl -n catalyst-system exec deploy/catalyst-ui -- \
  sh -c 'grep -c "xxBrokerURL\|kcBrokerURL" /usr/share/nginx/html/assets/index-*.js'
# EXPECT: 0
```

### Probe D — env vars cleared

```bash
kubectl -n catalyst-system get deploy catalyst-api -o json | \
  jq '.spec.template.spec.containers[0].env[] | select(.name | test("OIDC_PROVIDER_ISSUER_URL|SESSION_COOKIE_DOMAIN")) | .name'
# EXPECT: (empty output — no rows)
```

### Probe E — Keycloak `catalyst-pin` IdP `disableNonce: false`

```bash
TOKEN=$(...)   # admin-cli token, same as §3 step 3
kubectl -n keycloak exec keycloak-0 -c keycloak -- \
  curl -sk -H "Authorization: Bearer $TOKEN" \
  http://localhost:8080/admin/realms/sovereign/identity-provider/instances/catalyst-pin \
  | jq -r '.config.disableNonce'
# EXPECT: false  (or empty/null — both mean "default false")
```

### Probe F — HR + Kustomization un-suspended

```bash
kubectl -n flux-system get hr bp-catalyst-platform \
  -o jsonpath='suspend={.spec.suspend} ready={.status.conditions[?(@.type=="Ready")].status}{"\n"}'
# EXPECT: suspend=false ready=True
kubectl -n flux-system get kustomization.kustomize.toolkit.fluxcd.io bootstrap-kit \
  -o jsonpath='suspend={.spec.suspend} ready={.status.conditions[?(@.type=="Ready")].status}{"\n"}'
# EXPECT: suspend=false ready=True
```

### Probe G — full PIN-walk regression (operator step)

The image-swap closeout agent (NOT this read-only audit agent) drives a PIN-login → click-each-of-Tier-1-apps walk and captures screenshots. Each app must land authenticated within 1 hop (no KC username/password form ever shown). Per `feedback_g113_sso_idr_defaultprovider_fix.md` final-flow steps 1–7.

---

## 5. Pass/Fail decision tree for G117.0

```
              ┌───────────────────────────────────────────┐
              │  Run ALL probes A–G after §3 steps 1–5    │
              └───────────────────────────────────────────┘
                                  │
                                  ▼
              ┌───────────────────────────────────────────┐
              │  Probes A,B,C,D,E,F all PASS?             │
              └───────────────────────────────────────────┘
                    │ yes                          │ no
                    ▼                              ▼
       ┌──────────────────────┐         ┌──────────────────────────┐
       │  PR #2735 status?    │         │  ROLLBACK per memory L9  │
       └──────────────────────┘         │  (set image=f61a52a,     │
        │ MERGED        │ OPEN          │  re-apply env overrides, │
        ▼               ▼               │  re-suspend HR+Kustomiz) │
   ┌────────┐    ┌─────────────┐        │  + alert_human           │
   │ G117.0 │    │ G117.0      │        └──────────────────────────┘
   │ GREEN  │    │ AMBER (8 of │
   │ unlock │    │ 10 cleared) │
   │ W4.D1  │    │ — defer 7,8 │
   │ wipe   │    │ to #2735    │
   └────────┘    └─────────────┘
```

**Definition of GREEN** (full unblock): all probes A–G pass + #2735 merged + bp-keycloak ≥1.5.0 chart pin reconciled on hw86.

**Definition of AMBER** (partial unblock — acceptable for W4.D1 wipe if operator approves): probes A–F pass; probe G's full PIN-walk works because of per-user federated-identity pre-link (existing emrah.baysal row); row 7/8 cleanup deferred to a separate #2735-merge cycle that completes BEFORE the wipe.

**Definition of RED** (block W4.D1 wipe): ANY of probes A–E fail post-swap → rollback per memory L9 → re-investigate → alert_human.

---

## 6. Cross-references

- Parent EPIC: [#2737](https://github.com/openova-io/openova/issues/2737) — G117 Application Lifecycle Phase 2
- Sister sub-EPIC: G117.0 closeout agent (to be filed once W3 completes; this audit gates that filing)
- Codifying PRs already merged: [#2729](https://github.com/openova-io/openova/pull/2729) [#2730](https://github.com/openova-io/openova/pull/2730) [#2731](https://github.com/openova-io/openova/pull/2731) [#2733](https://github.com/openova-io/openova/pull/2733) [#2734](https://github.com/openova-io/openova/pull/2734)
- Codifying PR still open: [#2735](https://github.com/openova-io/openova/pull/2735) (Bug-6 first-broker-login auto-link)
- Memory references consumed:
  - `feedback_g113_sso_idr_defaultprovider_fix.md` — full 6-bug catalogue + image-pull rollback pattern + HR+Kustomization dual-suspend pattern
  - `feedback_canonical_wipe_endpoints.md` — destructive wipe is `POST /sovereign/api/v1/deployments/{id}/wipe`, NEVER raw `hcloud server delete`
  - `feedback_credentials_already_in_cluster.md` — kubeconfig source-of-truth on catalyst-api PVC
  - Amnesia anti-pattern **L9** — `/var/lib/catalyst/*` is on PVC, not host root
  - Amnesia anti-pattern **L7** — sub-agent reports are CLAIMS; re-query live state
- Companion docs:
  - `docs/sessions/2026-06-02-g113-sso-closure.md` — runtime walk evidence
  - `docs/sessions/2026-06-02-per-blueprint-topology-audit.md` — per-blueprint topology audit (PR #2736 source-of-truth)

---

## 7. Constraints honored (audit-agent self-check)

- [x] READ-ONLY on hw86 — zero `kubectl apply/patch/edit/delete`, zero KC admin-API `POST`/`PUT`/`DELETE`, zero HR reconciles, zero image swaps performed during this audit.
- [x] No `emrah.baysal` mail creds touched. Diagnostic READ-ONLY of the federated-identity row only.
- [x] Canonical wipe endpoint referenced (POST `/sovereign/api/v1/deployments/{id}/wipe`); no raw `hcloud server delete` mentions in this doc except as a banned pattern.
- [x] Self-verification curl probes are observational — they describe what the closeout agent runs, not what this audit ran.
- [x] Memory pre-flight: L1–L10 read; kubeconfig sourced from PVC per L9 + `feedback_credentials_already_in_cluster.md` (NOT from host root or operator request).

---

*End of audit. Next action: G117.0 closeout agent picks this up after W3 lands the last in-flight codification PRs, runs §3, captures §4 evidence, posts §5 verdict on #2737, files G117.0 sub-EPIC issue.*
