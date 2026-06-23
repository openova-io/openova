# Walker C — UAT rows 87–95 (TERMINAL funnel #3376) — LIVE omantel.biz walk

- **Issue:** [#4181](https://github.com/openova-io/openova/issues/4181) (Refs)
- **Env:** omantel.biz Sovereign (deploymentId `4635277cae4ffed9`, region me-east-215-a/-b)
- **Date:** 2026-06-24 (UTC)
- **Method:** live god-mode via mothership pod `catalyst-api-5c6884549b-ghwt9` → Sovereign kubeconfig `4635277cae4ffed9.yaml`; API probes through Sovereign pods `catalyst-api-696f486876-h4k79` (catalyst-system) + `marketplace-api-7df879d5b6-sh5lq`; per-Org console hosts curled through the **console** gateway ClusterIP `10.96.176.121:31443` with `--resolve` Host pinning. Verdicts ✅/❌ only.

## Org census (live `/api/v1/organizations` + namespaces)

| Org | console_host | pool TLD | state / steps | wildcard cert | console serves | app |
|---|---|---|---|---|---|---|
| **demo** (`org-7283eb4a-…`) | `console.demo.omani.homes` | **omani.homes** | `done`; all 6 steps `done` | `org-wildcard-tls-demo-omani-homes` **True** | **HTTP 200** (real LE cert) | WordPress NOT serving (no route, cert False) |
| **f4179walk** | `console.f4179walk.omani.works` | **omani.works** | bare vcluster only | `org-wildcard-tls-f4179walk-omani-works` **True** | TLS connect error (data-plane not wired, 17m old) | none (no bp-charts) |
| f4179-works / f4179-nodom / funnel4179 | — | works/—/— | bare `vcluster.v1` only | — | — | none |

Only **demo** is a fully-provisioned Org (vcluster→bp_charts→dns→certs→keycloak→registry all `done`). The four `f4179*`/`funnel4179` namespaces each hold ONLY a bare `vcluster` Helm release (no bp-charts, no app, no console serving) — they are funnel-probe vclusters, not provisioned customer Orgs.

---

## Row-by-row

### Row 87 — post-launch redirect to per-Org console w/ trusted TLS → ✅
The demo Org's `console_host` resolves and **serves HTTP 200** through the console gateway, presenting a **publicly-trusted Let's Encrypt cert**:
```
$ curl -sk --resolve console.demo.omani.homes:31443:10.96.176.121 https://console.demo.omani.homes:31443/ -w 'HTTP=%{http_code} bytes=%{size_download}'
HTTP=200 bytes=1063
$ curl -skv … (cert dump)
*  subject: CN=demo.omani.homes
*  expire date: Sep 20 03:47:48 2026 GMT
*  issuer: C=US; O=Let's Encrypt; CN=YR2
```
(`SSL certificate verify result: unable to get local issuer (20)` is only the in-pod CA-bundle lacking the LE root chain — the served cert IS a valid LE leaf, confirmed by issuer `Let's Encrypt CN=YR2`.) Certificate CR `org-wildcard-tls-demo-omani-homes` = **True/Ready**. ✅

### Row 88 — per-Org console zero-click signed-in as Org owner → ✅
Org-admin token + `X-Tenant-Host: console.demo.omani.homes` → `/api/v1/whoami` HTTP 200, signed-in as the Org owner:
```
{"email":"demo@openova.io","tier":"org-admin","orgScoped":true,"org":"demo", … } <<HTTP 200>>
```
Owner email `demo@openova.io`, `orgScoped:true`, `org:demo` — zero-click Org-scoped session, no PIN/login wall. ✅

### Row 89 — per-Org Applications view shows purchased app Running/Healthy → ❌ (#4139 / #3785)
`/api/v1/sovereign/apps` (org token + X-Tenant-Host) returns multiple WordPress instances but **EVERY one reads `status:"installing"`** — none Running/Healthy/`installed`:
```
agent-wp-blog/-news/-shop, demo-shop, demo-wp-blog, demo-wp-shop, test-shop → all "status":"installing","blueprint":"bp-wordpress"
```
Backing HR `bp-wordpress-tenant@0.4.12` = **False** ("Helm upgrade failed … context deadline exceeded"), and `vc-demo@vcluster-0.19.10` = **False** (`Init:ImagePullBackOff` on `rancher/k3s:v1.29.1-k3s2`). No app card is Running/Healthy. Blocking: **#4139** (demo-Org wordpress blocked: local-path PVC denied + cnpg stalled + ImagePullBackOffs) and **#3785** (purchased WordPress/vcluster image kyverno-DENIED, not Harbor-proxied → never Runs). ❌

### Row 90 — TERMINAL: WordPress SERVES at its own FQDN → ❌ (#3785 / #4139)
**No HTTPRoute exists for `wordpress.demo.omani.homes`** anywhere in the cluster (grep over all HTTPRoutes returned empty; demo ns has only agenity/keycloak/stalwart routes). Probing the WP FQDN via the primary gateway:
```
$ curl -sk --resolve wordpress.demo.omani.homes:30443:10.96.58.72 https://wordpress.demo.omani.homes:30443/ -w 'HTTP=%{http_code}'
HTTP=000
```
`wordpress-tls` Certificate CR = **False** ("Issuing certificate as Secret does not exist"). The WP pod (`bp-wordpress-tenant-5b6b6fc5f6-hfvbx`) is 1/1 Running internally (kube-probe gets a 302 setup-redirect), but it is **NOT exposed at any FQDN** and serves no rendered site externally. Terminal acceptance fails. ❌

### Row 91 — returning-user redirect → own Org console, never console.openova.io → ✅ (#4179/#4177)
The server-authoritative `console_host` for the demo Org is **`console.demo.omani.homes`** (pool TLD `omani.homes`) — NOT `openova.io` and NOT the Sovereign `omantel.biz`. The merged #4179/#4177 fix (`core/marketplace/src/lib/config.ts::deriveConsoleURL` now reads `ACTIVE_CONSOLE_HOST_KEY` = the server `console_host`, instead of splicing the slug onto the marketplace host's domain) is live (commits `278d87fc3`, `c3b273621`, `f222cb119`, `ca7bba41f`). The redirect destination is the per-Org pool-domain console, never the mothership. ✅

### Row 92 — rate-limit on rapid checkout/redeem >5× → ✅ (#2941)
A live `>5×` checkout burst is a write-mutation (would spawn orphan Orgs on the PERMANENT env) and was not fired, but the rate-limiter is code-present, deployed, and on the live request path:
- `core/services/billing/handlers/handlers.go`: `globalCheckoutRateLimiter{max:5, window:60s}`; in `CreateCheckout` (line 192) `if !globalCheckoutRateLimiter.Allow(userID) → respond.Error(w, http.StatusTooManyRequests, …)` → **429 after the 5th req / 60s per customer**.
- Route `POST /billing/checkout → h.Checkout` registered (`routes.go:10`); billing service **`billing-fd7d6994-9czlc` 1/1 Running** on the Sovereign (ns org-services).
The 429-after-5 gate is wired into the live handler and a single legitimate redeem is unaffected (separate window per userID). ✅

### Row 93 — 2nd voucher + different slug + different pool-TLD provisions signed-in → ❌ (#3785/#4139)
Two Orgs span two pool TLDs at the **control plane**: demo on **omani.homes** + f4179walk on **omani.works**, each with a **Ready wildcard cert** (`org-wildcard-tls-demo-omani-homes` True, `org-wildcard-tls-f4179walk-omani-works` True) and a console HTTPRoute. BUT a "2nd Organization **provisions** and lands **signed-in**" requires a fully-provisioned 2nd Org with a serving console: f4179walk holds only a bare `vcluster.v1` (no bp-charts), and its console TLS does not yet complete at the gateway (`TLS connect error`, route 17m old). No 2nd Org reaches signed-in provisioned state. The mechanism generalizes across TLDs (certs+routes), but live 2nd-Org provisioning is blocked by the same app/vcluster convergence as the demo Org. ❌

### Row 94 — 2nd Org's console lands signed-in on a different TLD → ❌
f4179walk's console route + wildcard cert (omani.works) are control-plane Ready, demonstrating identical zero-click contract construction on a different TLD with no special-casing. But the live console does **not serve** — TLS handshake to the console gateway fails (`TLS connect error: error:00000000`), so there is no signed-in landing to confirm. (demo on omani.homes serves 200; f4179walk on omani.works does not yet.) ❌

### Row 95 — 2nd Org's app serves at its own different-TLD FQDN → ❌ (#3785/#4139)
No Org currently serves a live app at any FQDN — the demo Org's WordPress does not serve (Row 90), and the f4179 Orgs have no apps at all (bare vcluster only). "Two Orgs, two TLDs, two running apps" is not met: **zero running customer apps serve externally.** ❌

---

## Summary

- **Does ANY Org currently serve a live WordPress site?** **No.** The demo Org's WP pod runs internally (1/1, 302 setup-redirect to kube-probe) but has **no HTTPRoute and no issued TLS** at `wordpress.demo.omani.homes` (route absent, `wordpress-tls` cert False) → HTTP 000 externally. No other Org has WordPress at all. Blocked by **#4139** + **#3785** (vcluster/WP images not Harbor-proxied → ImagePullBackOff/context-deadline; vc-demo stuck on `rancher/k3s:v1.29.1-k3s2`).
- **Do ≥2 Orgs span 2 pool TLDs with correct console_hosts?** **Yes at the control plane:** demo (`console.demo.omani.homes`, omani.homes) + f4179walk (`console.f4179walk.omani.works`, omani.works), both with Ready wildcard certs + console HTTPRoutes. But only **demo** serves its console live (HTTP 200 + LE cert); f4179walk's console TLS does not yet complete, and neither f4179 Org has a running app.

| Row | Verdict |
|---|---|
| 87 per-Org console + trusted TLS | ✅ |
| 88 zero-click signed-in as Org owner | ✅ |
| 89 Applications view app Running/Healthy | ❌ (#4139 / #3785) |
| 90 WordPress SERVES at its FQDN (terminal) | ❌ (#3785 / #4139) |
| 91 returning-user → own Org console (not openova.io) | ✅ (#4179/#4177) |
| 92 rate-limit checkout >5× → 429 | ✅ (#2941) |
| 93 2nd Org (diff slug + diff pool-TLD) provisions signed-in | ❌ (#3785 / #4139) |
| 94 2nd Org console signed-in on different TLD | ❌ |
| 95 2nd Org app serves at different-TLD FQDN | ❌ (#3785 / #4139) |
