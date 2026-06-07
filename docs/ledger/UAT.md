# UAT — OpenOva Catalyst — fresh acceptance page (hw100)

> **Tests-first.** Fresh **2-region** prov **hw100** replaces wiped hw99. Every row is `☐` **pending** until walked LIVE on the **production React tree** (`products/catalyst/bootstrap/ui/`) on the real Sovereign — never a mock, never the dead Svelte console (`products/catalyst/console/`), never a CLOSED/merged status. A row earns its evidence link ONLY when it is actually walked. Authored 2026-06-08 after hw99 (1 functional cluster + falsified 2-region evidence) was wiped on founder order. **Sandbox is OUT of scope.**
>
> Sources: 5-pillar DoD (`../DOD.md`); G117 EPIC #2737 (`#2740/#2741/#2742/#2743/#2744/#2745/#2674`); topology matrix `../sessions/2026-06-02-per-blueprint-topology-audit.md`.
>
> Legend: ✅ pass · ◑ partial · ❌ fail · ⛔ blocked · ☐ pending (default).

| # | Test group | Tested page | Test case (what you do) | What you must see | Result | Evidence |
|---|---|---|---|---|---|---|
| **Pillar 1 — Marketplace + voucher onboarding → Organization** |||||||
| TC-01 | Marketplace storefront | `marketplace.hw100.<dom>/` | Open the storefront | "Build your tenant" renders, non-empty, branded | ☐ | — |
| TC-02 | Operator issues voucher | `console.hw100.<dom>/bss/vouchers` | Operator: +Issue voucher → code+credit → submit | Voucher appears in the table, active | ☐ | — |
| TC-03 | Voucher email | recipient inbox | Open the voucher email | Delivered via the **Sovereign's own SMTP** with the redeem link | ☐ | — |
| TC-04 | Redeem voucher | `marketplace.hw100.<dom>/redeem?code=…` | Open the redeem link | "Voucher valid" + OMR credit; a garbage code → "not valid" | ☐ | — |
| TC-05 | Pick plan | `…/plans` | Pick a plan card | Advances to app picker | ☐ | — |
| TC-06 | Pick apps | `…/apps` | Select a Postgres-backed app | Advances to setup/extras | ☐ | — |
| TC-07 | Choose subdomain | `…/addons` | Type a valid subdomain | Pool picker offers a free domain; subdomain accepted | ☐ | — |
| TC-08 | Checkout (credit-only) | `…/checkout` | Sign in (email→PIN), confirm | Voucher **credit applied**, no card required; "Setting up your tenant" | ☐ | — |
| TC-09 | Organization created | "Your tenant is ready" | Follow the tenant link | Lands on `console.<orgslug>.<pool>` — real dashboard, not an error | ☐ | — |
| TC-10 | Tenant first login | tenant console | Customer PIN-login | Dashboard renders (Phase 2a) | ☐ | — |
| **Pillar 2 — Multi-region BCP topology chosen at signup** |||||||
| TC-11 | BCP at signup | `marketplace.hw100.<dom>/bcp` | Choose **active-hot-standby**, pick **two different** regions | Same-region rejected; two distinct regions accepted; provisions BOTH in one pass | ☐ | — |
| TC-12 | Cloud view = 2 REAL regions | `console.hw100.<dom>/cloud?view=graph` | Open the Cloud view | **2 regions, 2 clusters with REAL nodes in each** (not an empty 2nd-region VPC shell — the hw99 failure) | ☐ | — |
| **Pillar 3 — Two independent CNPG clusters + region-kill failover** |||||||
| TC-13 | CNPG pair across regions | tenant app detail → Topology | Install a CNPG-backed app; read placement | One CNPG cluster **per region**, synchronous `ReplicaCluster` over ClusterMesh; both regions shown | ☐ | — |
| TC-14 | Region-kill failover | the app's FQDN | Dev kills the primary region; keep refreshing | Service resumes **≤30 s**, same FQDN; surviving region healthy; **0 transactions lost** | ☐ | — |
| **Pillar 5 — Sovereign independence (`bp-self-sovereign-cutover`)** |||||||
| TC-15 | Trigger cutover | `console.hw100.<dom>/settings` → Sovereignty | "Soft-tethered" → tap "Achieve True Sovereignty" → confirm | Progress card; 8 tether-pivot steps advance | ☐ | — |
| TC-16 | Egress-block proof | progress card | Wait through the final step | **10-min deny-egress** hold vs github.com/ghcr.io/harbor.openova.io; stays green → badge **"Independent"**, `cutoverComplete=true` | ☐ | — |
| TC-17 | Post-cutover resilience | tenant console + an app | PIN-login + tap **Open** | Both still work, now pulling exclusively from local Gitea/Harbor | ☐ | — |
| **G117 — Application lifecycle (EPIC #2737) — corrected contract** |||||||
| TC-G1 | Catalog class ≠ instance | `console.hw100.<dom>/apps` | Catalog tab → click a class card | **CLASS page** `/catalog/$bp` — distinct (instances-list + New-instance only, no single-instance tabs) | ☐ | — |
| TC-G1 | Catalog class ≠ instance | `/apps` | Deployments tab → click an instance | **INSTANCE page** `/app/$id` — that one instance only; **NO "New instance"**; the two clicks NEVER open the same page | ☐ | — |
| TC-G2 | Multi-instance children | `/catalog/$bp` | "+ New instance" ×3, distinct names | All accepted, no collision; class page lists all N, each → its own `/app/$id` | ☐ | — |
| TC-G3 | Open = 1-click silent SSO | `/app/$id` (external-facing) | One click on **Open** | New tab **already signed in** (`prompt=none&kc_idp_hint=catalyst-pin`); no Open on non-external apps; works grafana/gitea/harbor/openbao | ☐ | — |
| TC-G4 | Endpoints tab editable | `/app/$id` → Endpoints | Add alias / edit / delete | EDITABLE; each mutation → Git-IaC PR (3 checks) → auto-merge → new FQDN serves TLS+SSO ≤2 min | ☐ | — |
| TC-G5 | Per-app topology honored | `/app/$id` → Topology | Install reps of each class on 2-region | active-active→N active HRs; hot-standby→primary+passive; passive→warm; singleton→1 HR+warning; matches declared `spec.topology` (10/14/20/44) | ☐ | — |
| TC-G6 | vCluster containment | `/app/$id` → placement | Read each app's vCluster | Apps run **inside** mgmt/dmz/rtz; only substrate prereqs on host; **no app Blueprint on host** | ☐ | — |
| TC-G7 | Region-kill (ties TC-G5) | — | See TC-14 | Covered by TC-14 | ☐ | — |
