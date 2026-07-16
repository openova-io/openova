# hw261 — Funnel walk (Pillars 1 + 2), live browser evidence

**Env**: `hw261` (dep-family hw261, `*.hw261.omani.works`) — the zero-touch multi-region
Huawei kom4dc Sovereign that reached `cutoverComplete=true` (11/11, deny-egress held,
self-sovereign) and passed the region-kill G12 test (zero-tx-loss failover + clean recovery).

**Walk type**: unauthenticated customer-onboarding funnel (Playwright, real browser, live TLS).
**Date**: 2026-07-16.

---

## What was walked

| Surface | URL | Result |
|---|---|---|
| Marketplace front door | `https://marketplace.hw261.omani.works/` | **200** — hero "Build your cloud Organization in under 5 minutes"; canonical 6-step funnel nav (1 Plans · 2 Apps · 3 Add-ons · 4 BCP · 5 Review · 6 Checkout); "Every app is FREE — you only pay for the infrastructure"; 4 value props (∞ Unlimited apps · 🔒 Isolated Organization · 🌐 Free subdomains · ⚡ One-click deploy). |
| Funnel step 1 — Plans | `.../plans/` | **200** — "Pick a plan"; 5 tiers **S / M / L / XL / Flexi** priced in **OMR** (S 5.000, M 9.000 "Popular"+pre-Selected, L 16.000, XL 30.000, Flexi 2/CU); full comparison matrix (vCPU/RAM/Disk/Bandwidth/Backup retention/SSL/SSO/SLA/Support); "Continue to Stack →". |
| Funnel step 4 — BCP (Pillar 2) | `.../bcp/` | **200** — "Business continuity — Pick how your database should survive a regional outage"; two topology radios: **Single-region** (FREE, default-checked — one Postgres cluster, backups via add-on, recovery in hours) and **Active-hot-standby** (+OMR 5.000/mo — "Primary + synchronous replica across two distinct regions over Cilium ClusterMesh. RTO 30s, RPO 5s. Zero-tx-loss failover when a region goes dark."). |
| Console front door | `https://console.hw261.omani.works/` → `/login` | **200** — **passwordless-PIN** login ("Enter your email to receive a 6-digit PIN", scoped to `console.hw261.omani.works`). North-star #3 confirmed: PIN login, **not** auto-admin. |

Screenshots (this session's Playwright output dir):
- `hw261-funnel-bcp-pillar2-topology-choice.png`
- `hw261-console-login-passwordless-pin.png`

---

## Why this matters — Pillars 1 + 2 + 3 are coherent end-to-end

The **Pillar-2 signup promise** on the BCP step — *"Primary + synchronous replica across two
distinct regions over Cilium ClusterMesh. RTO 30s, RPO 5s. Zero-tx-loss failover when a region
goes dark"* — maps **exactly** to the **Pillar-3 region-kill G12** result proven live on this
same Sovereign: region-a killed (real ECS SHUTOFF), region-b promoted, the pre-kill sentinel row
survived (zero-tx-loss), console stayed 200, and the pair recovered to its original topology
zero-touch. The customer-facing offer is backed by a demonstrated capability, not a claim.

## Not covered by this walk (held for the authenticated wave)

- Owner-authenticated console (dashboard / cloud view / jobs / organizations / showback) — needs
  a PIN-minted owner session.
- Per-Org **Agenity** workspace UI (Pillar 4) — `openova-mcp` pod confirmed Running 1/1 in
  `catalyst-system`, `org-services` ns Active; the browser walk of the workspace + mutating MCP
  tools (e.g. `create_application`) is the remaining Pillar-4 leg.
- Full checkout → Organization-active provisioning (Pillar 1 completion).

These are the defined next legs of the final UAT sweep on live hw261.
