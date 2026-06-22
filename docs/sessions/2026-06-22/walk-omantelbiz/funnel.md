# funnel + e2e-journey epics — live walk on omantel.biz — 2026-06-22

## Scope note
This was a **READ-ONLY** verifier walk on the PERMANENT omantel.biz Sovereign. The marketplace funnel (voucher issue → redeem → plan → app → BCP → checkout → provision) is an end-to-end **write** flow that creates real Organizations + vClusters. Driving it on the permanent env is a mutation and was deliberately NOT exercised (guardrail: do not mutate live resources). The funnel rows are therefore marked WARN with the render-level evidence that IS available, to be re-walked by a dedicated funnel-drive pass.

## What was verified read-only
- marketplace.omantel.biz front door: HTTP 200.
- Marketplace bundle: NO `console.openova.io` / `omantel.openova.io` / bare `openova.io` host literal; NO "tenant" banned-term string. (rows 77, 40)
- The marketplace returning-visitor path redirects an unauth visitor into the passwordless 6-digit email PIN flow at `/login` (the auth-once model, North Star #3).
- A customer Organization (`Demo Org`, owner demo@openova.io, console.demo.omani.homes) ALREADY exists in the directory as a customer/org/real/vcluster Organization — i.e. the funnel HAS produced an Org on this env. Its vCluster backing (`vc-demo`) is InstallFailed and `bp-keycloak-0` is ImagePullBackOff (peer-agent-owned), so the terminal "app serves at its own FQDN" half is not yet green.
- console.demo.omani.homes returns 200 with a valid LE cert; navigating it shows the per-Org PIN login (`/login?next=/dashboard`).

## Per-row dispositions (WARN = needs a funnel-drive mutation pass)
- 72-74 voucher form (BSS) — not drilled (mutation-risk).
- 75-77 redeem page brand/no-leak — bundle no-leak verified; live redeem render not drilled.
- 78-83 plan/app/addons/BCP/review — require driving the signup wizard (mutation).
- 84-85 passwordless checkout + voucher-credit — PIN path present; full checkout needs a live signup.
- 86-90 provision timeline → per-Org console → app serves — Demo Org exists but vc-demo backing converging; terminal serve not green.
- 91-95 returning-user redirect + multi-Org generality + rate-limit — need the funnel driven.
- 216 (e2e voucher→Org) — Demo Org present; backing converging.
- 217 (customer PIN flow) — PIN form present; live emailed-code login not exercised. NOTE: during the walk the sovereign catalyst-api rolled once and the auth/pin upstream returned a transient 503 "no healthy upstream" for ~2min, then recovered.
