# Goal-2 walk — fresh-stranger marketplace signup on omantel.biz (the #1 customer journey)

**Date:** 2026-06-24 · **Env:** omantel.biz Sovereign (dep `4635277cae4ffed9`, Huawei me-east-215) · **Driver:** Playwright (fresh context, all storage cleared)

## Verdict

**A fresh signup does NOT land signed-in on the correct pool host.** It is a **live reproduction of #4179 from a brand-new stranger** — the post-signup redirect derives the **DEAD** host `console.g2signup.omantel.biz` (connection-refused), not `console.g2signup.omani.works`.

The funnel UI itself (steps 1–6, passwordless PIN sign-in) works end-to-end. The break is at org-create / landing.

## What was walked (fresh stranger, storage cleared)

| # | Step | Screenshot | Result |
|---|------|-----------|--------|
| 0 | Landing — cleared a stale `hatice.yildiz+f4179walk` session first | `01-marketplace-landing-fresh.png` | clean (no stale account) |
| 1 | Plans — picked **S** | `02-step1-plans.png` | ✅ |
| 2 | Apps — added **WordPress** | `03-step2-apps-wordpress-added.png` | ✅ |
| 3 | Add-ons & domain — **`g2signup` + `.omani.works`** (pool TLD) | `04-step3-addons-domain-omani-works.png` | ✅ |
| 4 | BCP — **Active-hot-standby** (Pillar-2 multi-region) | `05-step4-bcp-active-standby.png` | ✅ |
| 5 | Review — Org URL **`g2signup.omani.works`** | `06-step5-review-omani-works-url.png` | ✅ |
| 6a | Checkout — email `g2signup-stranger@omani.works` → Send code | `07-step6-checkout-email.png` | ✅ |
| 6b | PIN **023144** (read from Valkey) → **signed in** | `08-step6-checkout-signed-in-pin-verified.png` | ✅ |
| 6c | Purchase → org created, but Stripe 503 | `09-step6-checkout-ready-to-purchase.png` | ⚠️ |
| 7 | **Landing on the host the funnel sends new users to** | `10-DEAD-HOST-landing-4179-connection-refused.png` | ❌ #4179 |

## How the PIN was obtained (no founder mailbox touched)

The Sovereign uses passwordless 6-digit email PIN. `services-auth.SendMagicLink` stores the code in **Valkey** at `magic:<email>` (5-min TTL) before emailing it. Read directly — no mailbox creation, no SMTP, no touching `emrah.baysal@`:

```
KUBECONFIG=<sov> kubectl exec -n rtz valkey-primary-0-x-valkey-x-rtz-vcluster -c valkey \
  -- valkey-cli GET "magic:g2signup-stranger@omani.works"
# → 023144
```

(`redis:7-alpine` reader Pod was blocked by the bastion-Harbor mirror 404; the in-cluster Valkey pod ships `valkey-cli`, so exec into it directly.)

## #4179 root cause — precise, from the wire + cluster + git

1. **Frontend is correct.** `POST /api/tenant/orgs` request body carried `parent_domain:"omani.works"` (the cart `tld`). Confirmed on the wire.
2. **Server dropped it.** The 201 response had **no `console_host`** field. The created Org CR `g2signup` has **`tenantPublic:{}`** (empty) and **no HTTPRoute** for `console.g2signup.omani.works`.
3. **Redirect → dead host.** With `console_host` empty, marketplace `deriveConsoleURL` (config.ts) falls back to `console.<slug>.<marketplace-host-apex>` = `console.g2signup.omantel.biz`.
   - `console.g2signup.omantel.biz` → 49.12.16.160 (a Hetzner `*.omantel.biz` wildcard, NOT this Huawei console-ELB 212.72.24.33) → **ERR_CONNECTION_REFUSED**.
   - `console.g2signup.omani.works` → **NXDOMAIN** (never created).
   - Working reference: `console.f4179walk.omani.works` → 212.72.24.33 (console-ELB EIP).
4. **Why the server drops it = a deploy gap, NOT a code bug.** The handler code (`core/services/tenant/handlers/handlers.go` `CreateOrg` + `deriveTenantConsoleHost`) DOES thread `parent_domain` and return `console_host`. That fix is commit **`c3b273621`** (#4184, "thread parent_domain end-to-end", merged 2026-06-23 20:46).
   - Live `services-tenant` image = **`d3c55b4`** (2026-06-22, PRE-fix). `git merge-base --is-ancestor c3b273621 d3c55b4` = **false** → the running image does not contain the fix.
   - `products/catalyst/chart/values.yaml` still pins **`orgTag: "d3c55b4"`** (with a comment already admitting "the services-build deploy job FAILED to bump this orgTag"). `tenant`, `provisioning`, `billing` all render from `orgTag` → all run `d3c55b4`. Only `auth.yaml` (hardcoded tag) was bumped to `c3b2736`.

**Fix:** bump `images.orgTag` `d3c55b4`→`c3b2736` (or newer) in the catalyst chart values and roll org-services on omantel.biz, then re-walk. The code is already merged; this is purely an un-rolled deploy.

## #4192 secure handoff (cookie, no token-in-URL) — not reachable this walk

`consoleHandoffHref` (config.ts, #4182/#4186) redirects post-checkout to `console.<slug>.<sov>/auth/org-handover?token=<sessionJWT>` → catalyst-api mints an HttpOnly `catalyst_session` cookie → 302 to a clean `/jobs` (refresh token never in URL). This logic is correct, but unverifiable here because the derived base host is the dead `console.g2signup.omantel.biz` — there is no signed-in landing to inspect. Verify after the orgTag roll lands the user on `console.g2signup.omani.works`.

## Artifacts left on the Sovereign (not wiped, per instruction)

- Org CR `g2signup` (status: provisioning, never completed — Stripe 503 + #4179). Harmless; wipe later if desired.
- Member user `g2signup-stranger@omani.works`, session JWT in marketplace localStorage `org-token` (role:member).
