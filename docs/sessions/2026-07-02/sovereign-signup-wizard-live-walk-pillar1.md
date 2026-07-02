# Live walk — Sovereign signup wizard (Pillar 1 Marketplace + 1b BCP topology)

**2026-07-02 · console.openova.io/sovereign/wizard · Playwright, unauthenticated (the wizard itself needs no login; "Sign in to view your deployments" is only for the deployments list).**

The mothership's provisioning/onboarding wizard is **live and walkable** — this is direct evidence for DoD **Pillar 1 (Marketplace + voucher onboarding)** and **Pillar 1b (multi-region BCP topology choice at signup)**, which had no live surface while every Sovereign prov was env-blocked. Walked 4 of 8 steps:

## Step 1 — Organisation profile
`sovereign-wizard-step1-org-profile-2026-07-02.png`. Org name (pre-filled "Acme Financial"), HQ (Frankfurt), Industry (Financial Services…), size, and **compliance frameworks** (PCI DSS · ISO 27001 · SOC 2 · GDPR · HIPAA · DORA · NIS2 · FedRAMP) that "shape component defaults."

## Step 2 — Infrastructure topology (**Pillar 1b**)
`sovereign-wizard-step2-bcp-topology-pillar1b-2026-07-02.png`. The multi-region BCP choice at signup, all five tiers rendered with region/cluster/vCluster counts:
- **CITADEL** (Tier-1 Bank) — 4-region, dual-CP MGMT + dual data plane — 4reg/6cls/6vC
- **DUAL** (Enterprise, "← start here") — 2-region, DMZ·RTZ·MGMT per region — 2reg/6cls/6vC
- **ZONED** (Mid-market, selected) — 2-region, DMZ cluster + RTZ·MGMT cluster — 2reg/4cls/6vC, with a Region-1-Primary + Region-2-DR diagram (MGMT in both regions "eliminates single-site management risk")
- **COMPACT** (Starter) — 2-region, all planes as vClusters — 2reg/2cls/6vC
- **SOLO** (Dev/POC) — single region — 1reg/1cls/3vC
- **AIR-GAP** add-on — +1 isolated region, pull-only replication, Specter forensic mode

## Step 3 — Cloud provider per region
`sovereign-wizard-step3-region-provider-cost-2026-07-02.png`. Pre-selected from HQ: Region 1 Hetzner **FSN1** (Falkenstein) + Region 2 Hetzner **NBG1** (Nuremberg), CPX22 control-plane, 2 workers each, live cost estimate **€0.136/hr · €99/mo**. Storage-class field explicitly states *"local-path is not permitted"* — the **#3971** governance guardrail surfaced in the signup UI.

## Step 4 — Platform Components (**Pillar 1 Marketplace**)
`sovereign-wizard-step4-marketplace-components-pillar1-2026-07-02.png`. The live component catalog: **64 components, 45 selected**, family-filtered (cortex · fabric · guardian · insights · relay · spine · surge), each RECOMMENDED/OPTIONAL with **auto-resolved bundled dependencies** (e.g. Grafana → CNPG+Loki+Mimir+Tempo+Keycloak+gateway-api+PostgreSQL; Loki/Mimir/Tempo → SeaweedFS; Falco → Cilium). Cards deep-link to `/sovereign/marketplace/product/<name>` and family portfolios.

## Verdict
Pillar-1 Marketplace + Pillar-1b BCP-topology-at-signup UI is **LIVE and functional on the mothership**, walkable without a converged Sovereign prov. Remaining wizard steps (5–8: compliance mapping, voucher/billing, review → provision) not yet walked this session. This is the first live UI evidence for these pillars since the Sovereign-side envs were env-blocked; the end-to-end signup→provision culmination still depends on the verification prov (env-wall #4695 fixed live; L4-guarded firing pending a fresh head).
