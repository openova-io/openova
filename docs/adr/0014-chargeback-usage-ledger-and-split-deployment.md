# ADR-0014 — Chargeback: usage ledger as the measurement primitive, `bp-chargeback` as a multi-tenant application with split deployment

**Status:** Proposed (founder session 2026-08-30; accept once the §Open-inputs land)
**Date:** 2026-08-30
**Refs:** `docs/sessions/2026-08-30/chargeback-requirements.md` (requirements capture + probe evidence, PR #6711/#6712), ADR-0011 (OpenTofu→Crossplane adoption seam), `docs/BUSINESS-STRATEGY.md` §10 (PAYG / ESA / franchise split), `products/catalyst/chart/crds/organization.yaml` (`planSlug`, `billingMode`, `kind`), Inviolable Principle #3 (Crossplane = day-2 cloud IaC), `docs/DOD.md` §5 J13 (Billing & quotas).

## Context

Two chargeback requirements were put on the table on 2026-08-30 and verified against code:

1. **Native OpenOva chargeback** — (Req 1) rate-limiting of resources by purchased plan;
   (Req 2) pay-as-you-go. Today: plan sale, prepaid voucher credit, CPU/memory ResourceQuota +
   LimitRange and vCluster tier-gate exist and are walked (`core/controllers/organization/internal/gitops/manifests.go:112-213, 477-560`;
   `core/services/billing/`). There is **no time axis on infrastructure consumption**: showback
   is a point-in-time request sum in abstract units (`products/catalyst/bootstrap/api/internal/handler/org_consumption.go:95-105`),
   Alloy ships an empty config so Mimir receives nothing, no `usage_records`, no price book, no
   statements. The only usage-based path is LLM tokens (`core/services/metering-sidecar` → NATS
   `catalyst.usage.recorded` → `credit_ledger`). `Organization.spec.billingMode: chargeback`
   is enum-only.
2. **Chargeback for National Cloud's customers** (National Cloud = Huawei+Omantel = HCSO, one
   party). Two populations: tenants provisioned through OpenOva, and standalone tenants with no
   OpenOva or provisioned before it. Plus a third case: an OpenOva-managed Sovereign's own cloud
   footprint on National Cloud.

Measured on our own National Cloud tenant (Kom4DC `me-east-215`, 2026-08-30): there is **no
tenant-facing billing or metering API** — 48 host/path variants return `404 APIGW.0101 "not
published"` while control routes return `401 APIGW.0301`; authenticated calls to `ecs`, `ces`
(metrics + events), `cts` (API audit trail) and `smn` return `200`; `eps` and `tms` exist but
our user lacks the policy. The operator plane is not reachable from outside. Huawei resources
carry no tags (`infra/providers/huawei/main.tf` — every resource `ignore_changes = [tags]`);
Enterprise Projects are unused.

Crossplane on a live Sovereign runs `provider-opentofu` + `provider-hcloud` only; the single
instantiated XRD is `XCloudAdoption` (ADR-0011), labelled by sovereign + deployment-id, no
Organization label. Controllers create no claims; vClusters, CNPG, DNS, buckets, pods, PVCs and
tokens are not Crossplane objects.

## Decision

### D1 — The measurement primitive is a usage ledger keyed by `Customer`, not Crossplane

An append-only `usage_records(customer, source, resource_id, kind, sku, quantity, unit,
window_start, window_end, region, labels, raw_ref)`, idempotent on (source, resource, window).
Facts first; `rated_lines` and `statements` derive from it via an effective-dated price book.
Crossplane keeps its ADR-0011 role — the inventory/adoption layer that makes cloud resources
visible as Kubernetes objects — and is one collector's source, never the meter. Rationale: a
meter tied to the provisioning engine is blind to everything it did not provision (Tofu-built
substrate, Flux-built vClusters, pre-OpenOva tenants) and has no time axis.

### D2 — Chargeback is a standalone application with its own `Customer` object; OpenOva is one adapter. The platform entity model is unchanged. (2026-08-31, founder: "hosting it on OpenOva is a coincidence")

Proof by the operator's view: for National Cloud, customer A (running its own Sovereign) and customer B (no OpenOva) are identical — buyers with cloud projects.

| Instance | Operator (seller) | Customers (buyers) | Metered |
|---|---|---|---|
| NC central | National Cloud | A, B | A's whole Sovereign footprint + B's VMs = cloud projects |
| A's own Sovereign | A | A's Organizations | A's internal workloads |

| Object | Where it lives | Platform entity? |
|---|---|---|
| `Customer` (buyer; users, cost sources, credentials, rate card, statements) | inside the chargeback application (its own DB) | **no** — app-internal, like posts in WordPress |
| Sovereign, Organization, Environment, Application, User | platform | unchanged |
| `Organization.spec.costSources[]` (optional) | platform, GitOps-declared | field only; synced into the app's `Customer` |

| Adapter (OpenOva-hosted profile) | Without OpenOva |
|---|---|
| `Customer ← Organization` sync, 1:1 by slug | `Customer` from own table / onboarding |
| platform usage collector (K8s watch) | — |
| Keycloak PIN identity | own PIN / OIDC |
| org-controller enforcement hook (D7) | — |

| Relation | Mechanism |
|---|---|
| A's admin on A's Sovereign vs on NC | two accounts = seller role vs buyer role; optional SSO later |
| NC ↔ A's Sovereign | statement exchange only (NC's statement to A = A's cost basis for margin); no shared identity |
| Hosting on OpenOva | a Blueprint like any other; not structural |

#### D2a — Per-mode entity matrix (2026-08-31)

Modes: **M1** = standard Sovereign (any customer instance) · **M2** = OpenOva-operated central chargeback instance (our own National Cloud tenant, PoC) · **M3** = National-Cloud-operated central instance for all its customers.

| Entity / field | M1 | M2 | M3 |
|---|---|---|---|
| Sovereign | present | present | present |
| Organization (`kind: customer`) | present | present | present |
| `Organization.spec.costSources[]` | field present, **empty** | non-empty for cloud-only Organizations | non-empty for most Organizations |
| `platform` Organization (case 3) | present | present, carries our tenant's cloud footprint | present, carries O's Sovereign footprint |
| Environment / Application | present | present or ∅ per Organization | ∅ for cloud-only Organizations |
| User (PIN sign-in) | present | present | present |
| **Tenant** | **not an entity → Organization** | **not an entity → Organization** | **not an entity → Organization** |
| `Customer` (app-internal) | = Organizations (synced) | = Organizations (synced) | = Organizations (synced) or app-onboarded |
| platform collector | on | on | on (idle for cloud-only Organizations) |
| cloud collector | off (no costSources) | on | on |
| enforcement (plan quota) | on | on for Organizations with workloads | on for Organizations with workloads |
| operator of record | sovereign-admin | OpenOva | National Cloud |
| code / chart | one | one | one |

#### D2b — Cross-Sovereign account duplication matrix (2026-08-31)

| Customer type | Account records held in | Billing source-of-truth | Sync / federation required |
|---|---|---|---|
| OpenOva-Sovereign-only customer (an Organization on some Sovereign S, S not on NC) | S: Organization + Users | S's chargeback instance (platform collector) | none |
| NC-only customer B (no OpenOva) | NC central: `Customer` + Users | NC central instance (cloud collector) | none |
| Dual-registered customer A (own Sovereign S_A on NC) | S_A: Organizations + Users (A as **seller**) · NC central: `Customer` A + Users (A as **buyer**) | NC central for A's cloud footprint (what NC charges A) · S_A's instance for A's Organizations (what A charges them) | **no identity sync** — two roles, two accounts; **statement exchange**: NC's statement to A → S_A's instance as cost basis (import via API/CSV); optional SSO later |
| `platform` Organization of S_A (case 3) | S_A only | S_A's instance (cloud collector on A's project) — must reconcile to NC's statement | reconciliation: Σ S_A case-3 lines ≈ NC statement to A; delta = report, not bug |

### D3 — Three cases, two collectors, one money layer

| Case | Collected via | Attributed via | Enforced? |
|---|---|---|---|
| Org on an OpenOva Sovereign | **platform collector**: namespace label `openova.io/organization`, plan emitter (plan-hour per Org), **Kubernetes watch (informers) on pods / PVCs / Application CRs** on the host cluster (the vCluster syncer mirrors pods into host namespace `<slug>`) with an hourly reconciliation pass, token sidecar | Organization CR | yes — plan quota |
| Standalone National Cloud tenant | **cloud collector** | project = account (Enterprise Project if granted) | no |
| Sovereign's own cloud footprint | **cloud collector** (same) | tofu state + deployment record + `CloudAdoption` claims → Sovereign, role, region; name-prefix `catalyst-<slug>-<dep>` fallback | not by us |

#### D3a — Collection modes and rationale (recorded 2026-08-30 after the founder's polling-vs-event question)

Two different things change, and they are collected differently:

| What changes | Case 1 (Org on a Sovereign) | Cases 2/3 (National Cloud tenants, Sovereign footprint) |
|---|---|---|
| **Resource lifecycle** (create / delete / resize / stop / start) | **event-driven**: Kubernetes watch — the collector is told, it never asks | **pull-based change log**: `cts` audit trail (every tenant API call, timestamped), pulled every ~5 min — near-event, exact timestamps, nothing missed |
| **Utilisation** (CPU, disk, bandwidth) | sampled — metrics are a time series by nature | sampled — hourly pull of `ces` metrics (CES stores the datapoints, so an hourly pull loses nothing) |
| **Threshold events** ("bandwidth > 80 % for 1 h", "idle 7 days") | alerting layer | optional **push**: `ces` alarm → `smn` topic → HTTPS webhook — the only true event path on the cloud side, for dashboards/alerts, never for the bill |

Rationale for not pushing on the cloud side: the only push channel is SMN → webhook, which requires a
topic and subscription *inside each tenant's account* plus National Cloud egress to us —
configuration we do not control and that a tenant can silently break. Pulling the change log
needs nothing but the read agency and cannot lose an event; the hourly inventory snapshot
reconciles the ledger regardless. Utilisation is never "event-driven" anywhere — nobody delivers
"CPU went from 40 to 41 %"; the event form of utilisation is a threshold alarm.

The cloud collector therefore **polls the change log, not the inventory**: `cts` traces + `ces` events every
5 min (exact lifecycle hours), hourly list-API snapshot as reconciliation, `ces` metrics for
utilisation. `smn` push is a dashboard optimisation, never a billing basis. An operator-plane
metering feed from National Cloud, if it exists, is an optional upgrade layered above the same
ledger.

### D4 — `bp-chargeback` is a standalone multi-tenant application (own `Customer` model, own DB, own API/UI); OpenOva integration is an adapter, not a dependency

`products/chargeback/` (chart + image), own CNPG database, own API and UI, own RBAC (operator-admin ·
account-admin · viewer) over a **Organization tree** (operator → accounts → cost centres). It
follows the Agenity precedent: one artifact, several placements.

### D5 — Split deployment: one chart, three placements

| Placement | Runs where | Tenants inside | Hard dependencies |
|---|---|---|---|
| Sovereign BSS component | every Sovereign, control-plane placement (`catalyst.openova.io/organization: platform`), linked from `console.<fqdn>/billing` | the Sovereign's Orgs + external cloud accounts | Postgres, OIDC (Keycloak), kube API, NATS |
| Per-Org Application (catalog) | an Organization, for its own cost centres and its own external cloud projects | departments / Environments / Applications | Postgres, OIDC |
| Central instance for National Cloud | their Sovereign (the BSS component *is* the central instance) — or bare Kubernetes (CCE) if Catalyst is refused | all their tenants | Postgres, OIDC, cloud credentials only |

Invariant that makes the third placement honest: **no hard dependency on Catalyst CRDs**. The
platform collector is optional; the cloud collector needs only credentials. A "separate instance
for a customer" is an instance whose root is one account — a placement choice, never a second
code path.

### D6 — Seam with the existing billing service

Chargeback owns accounts, collectors, ledger, price book, rating, statements. Billing keeps
customers, vouchers/credit, orders, checkout, Stripe (the "canonical voucher implementation — do
not redesign" rule holds). Interface = the hook that already exists for tokens:
`POST /billing/metering/record` (`core/services/billing/handlers/metering.go`) and the NATS
subject `catalyst.usage.recorded` — rated lines become credit debits when an Org is prepaid.
Billing never learns about cloud providers; chargeback never touches money. The token sidecar
is re-pointed to emit **quantity** into the ledger; money is derived by rating.

### D7 — Enforcement stays in the organization-controller, from one plan source

The catalog `Plan.IncludedQuotas` (`core/services/catalog/store/store.go:158-173`) becomes the
single source the controller renders from (today: three copies — `seed.go`, `planQuotaTable`,
`isolationForPlan`). Rendering extends to `requests.storage`, `count/*`, a Burstable split for
Flexi, per-plan egress annotations, and an Org-keyed gateway limiter.

### D8 — Access to standalone tenants = per-tenant read-only IAM user (AK/SK), agency only if the operator publishes token exchange

*(Amended 2026-08-30 15:1x after probing: `POST /v3/auth/tokens` — password **and** `assume_role`
agency exchange — and `/v3.0/OS-CREDENTIAL/securitytokens` are all `404 APIGW.0101` on the tenant
gateway; our own provider path has always been AK/SK request signing with no token API and no
agency — the earlier "`agency_domain_name` is in our HCS provider config" claim referred to the
Crossplane `provider-huaweicloud` credentials schema, not to anything we run. Agencies therefore
cannot be assumed from outside the platform as designed.)*

The proven path is the one we already run against our own tenant: the tenant (or the operator
centrally, for accounts it administers) creates a **dedicated read-only IAM user** in its account
— policies `Tenant Guest` + ReadOnly for ECS/EVS/EIP/ELB/NAT/OBS/RDS/DCS/CES/CTS, all regions —
and hands its **AK/SK** to the chargeback service, which stores it encrypted (OpenBao), signs every
request with `SDK-HMAC-SHA256` exactly as `internal/providers/huawei/sigv3.go` does today, and
supports rotation. Onboarding = account name + project ids + AK/SK in the chargeback UI (or an
operator-side bulk import). Revocation = the tenant deletes the IAM user or rotates the key.
If the operator later publishes the IAM token-exchange API (or an internal IAM host exists), the
agency variant becomes a drop-in credential type with no other change.

### D9 — UI model: one account tree, two lenses — unified from day one (recorded 2026-08-30 after the founder's UI question)

**Decision: NOT two chargeback UIs** (one for the underlying Sovereign/cloud regardless of
OpenOva management, one for Organization-level chargeback that depends on OpenOva capabilities).
**One UI over one Organization tree**, in which the Sovereign-level row is the *parent* of the
Organization rows and standalone tenants sit *beside* the Organizations. Unification is a property
of the data model (D2), not a later merge.

**Rationale.** (a) The operator wants one bill per customer and one screen per operator — two UIs
mean two price books, two statement formats and two portals for the same party. (b) Case 3 (the
Sovereign's own cloud footprint) is only useful when it can be *allocated down* to the Org rows
and compared with Org revenue (margin) — that is one screen by definition. (c) A tenant that
migrates onto the platform must keep its row and history; two UIs would force a re-onboarding.
(d) The difference between "cloud-level" and "OpenOva-attributed" cost is a difference of
**columns and one tab**, not of product.

**Navigation model — tree + drill-down + lens.**

```
Operator (any Sovereign operator; National Cloud)
├─ This Sovereign      case 3 — cloud footprint; Allocation → Orgs; Margin    [operator lens only]
├─ Org: <slug>         case 1 — plan, K8s workloads, tokens
│   ├─ Environment: <env>
│   │   └─ Application: <app>
├─ Tenant: <name>      case 2 — cloud resources via IAM agency
│   └─ Enterprise Project: <ep>   (only if EPS is granted)
```

- Every node has the same four views: **Usage** (lines over a period) · **Statements** ·
  **Price / Plan** · **Sources**. Drill-down = click a child row; breadcrumb up; the period
  selector persists across levels.
- Node kind determines the columns and one extra tab:

| Node kind | Usage lines | Extra tab |
|---|---|---|
| Organization (case 1) | plan-hours; vCPU/GiB-hours per Application; PVC GiB-hours; tokens | **Limits** — entitlement vs consumption, live (possible only because the org-controller enforces the plan, D7) |
| Tenant (case 2) | instance-hours by flavor; EVS/OBS GiB-hours; EIP/ELB/NAT hours | none — read-only, nothing to enforce |
| This Sovereign (case 3) | as Tenant | **Allocation** — cloud cost split to Org rows + a platform-overhead line; **Margin** = rated Org revenue − cloud cost |

- Two **lenses** = RBAC scopes over the same tree, not two screens:
  - **Operator lens** — `console.<fqdn>/billing` (sovereign-admin console; today Vouchers ·
    Orders · Revenue, gains **Accounts · Usage · Statements · Price book**). Sees the whole tree;
    Revenue gets its missing cost half from the "This Sovereign" node.
  - **Account-owner lens** — `console.<slug>.<pool>/billing` for an Organization (extends the
    tenant `BillingPage`), or a PIN login scoped to the account node for a standalone tenant
    (no Organization CR needed). Sees its own subtree only.
- Moving between "infrastructure cost" and "OpenOva-attributed cost" is therefore **one
  drill-down**: This Sovereign → Allocation tab → an Org row → that Org's Usage. No toggle, no
  second app.
- Implementation constraint that keeps this honest: the two consoles are different stacks
  (sovereign-admin = React/TSX, Org console = Astro/Svelte), so `bp-chargeback` ships **its own
  UI once** and both consoles route to it under their own hostname (HTTPRoute), scoped by the
  session. One code base, one screen; RBAC decides how much of the tree is visible.

**Convergence path — a customer that starts on the Sovereign/cloud view only.**

1. **Day 0 — standalone tenant.** An Organization with zero workloads and one `huawei-project` cost source;
   appears as a *Tenant* row; org-admin lens via the existing PIN login. Statements begin the first full period.
2. **Enterprise Projects granted.** The same row gains child rows per EP — department-level
   attribution with no change of screen.
3. **Migration onto OpenOva.** The **same Organization** gains Environments/Applications. The row keeps
   its identity and history and gains an `openova-org` source: Org columns (plan, per-Application lines, tokens) and the **Limits** tab appear;
   cloud lines continue for whatever stays outside the platform. Statements are continuous —
   one account, two sources, one bill.
4. **Fully on-platform.** The `huawei-project` source is retired when the last external resource
   is gone; the row is now an ordinary Organization row. Nothing was re-onboarded and no
   history moved.

The reverse direction is also allowed (an Organization adds an external cloud project as a
second source) — the tree is symmetric because the Organization is the root of identity.

### D10 — Deployment profiles: `sovereign` and `operator-central` (recorded 2026-08-30 after the founder's discomfort with the central National Cloud instance)

D5's third placement ("central instance … bare Kubernetes + OIDC") under-specified a genuinely
different **shell**: National Cloud's central instance meters *all* its customers and lets each
customer **log in with its own account** to see its own charging. That is not a different engine —
collectors, ledger, price book, rating, statements, the account tree and the two lenses (D9) are
identical; it is the operator lens over a tree of Tenant rows at scale. It *is* a different set of
requirements at the identity, onboarding, branding, RBAC and scale layer. Decision: one chart and
one code base with two **profiles**, selected by configuration, never by fork.

| Concern | `sovereign` profile | `operator-central` profile |
|---|---|---|
| Tenant root | Organization | Organization with `costSources` (its `domain_id` recorded on the source) |
| Tenant identity | OpenOva Keycloak (PIN) | **the tenant's own account**, in order of preference: (a) federation from the operator's portal IdP (OIDC/SAML) if exposed; (b) *verify-only* against the cloud IAM — username/password/domain → `POST /v3/auth/tokens` → `domain_id` read from the token, credential never stored; (c) account-linking — the read agency the tenant grants (D8) is proof of ownership, bound to an email PIN |
| Tenant users → roles | Org RBAC | IAM group/role claims → account-admin / viewer |
| Onboarding | automatic on Org create | self-service: sign in → "grant this agency" → agency verified → collection starts |
| Operator identity | sovereign-admin | operator staff via *their* SSO; roles finance / support / admin |
| Branding, domain, statements | Sovereign | white-label: `billing.<operator-domain>`, operator's statement template, VAT, legal footer |
| Scale, isolation, audit | tens of Orgs | hundreds of tenants; row-level tenant isolation; per-tenant audit log; per-tenant export/delete |
| Availability | per-Sovereign | across the operator's regions (stateless app + CNPG pair — the existing DR pattern) |
| Catalyst dependency | platform collector on | **none** (D5 invariant) |

**API-first, UI optional.** The engine exposes its full surface (accounts, sources, usage, statements,
price book) as an API; the D9 UI is one client of it. An operator may run the white-labeled UI *or*
embed the engine behind its own portal. This is the hedge that keeps the operator-central profile
from becoming a bespoke product.

**Operator of record.** The `operator-central` profile is **operated by the cloud operator itself**
(National Cloud staff in the operator role, on its own infrastructure — its Sovereign or bare CCE);
OpenOva-hosted operation of the same profile (OpenOva staff in the operator role) is a commercial
option, not a different profile.

**Per-tenant data partitioning (mechanism, not intent):** every table carries `organization`;
Postgres row-level-security policies keyed on the session's Organization scope every query;
statements and exports are stored under a per-Organization prefix; the audit log is per
Organization; delete/export is an Organization-scoped job. The operator lens reads across
Organizations through an explicit sovereign-admin policy, never by bypassing RLS.

**Identity options — probed 2026-08-30 15:1x on the tenant gateway and portal:** (a) the portal
(`console.kom4dc…`) redirects `/.well-known/openid-configuration`, `/saml/metadata`, `/realms`,
`OS-FEDERATION` to its SPA — **no discoverable IdP**; (b) `POST /v3/auth/tokens` (password) is
`404 APIGW.0101` — **verify-only login against the cloud IAM is not available from outside**.
Therefore **(c) is the default and, today, the only path:** account-linking — the read-only IAM
user's AK/SK the tenant hands over (D8) is proof of account ownership (the service verifies it by
one signed `ecs` list call and reads the `domain_id`/project from the response), bound to an email
PIN identity. (a)/(b) remain questions for the operator; if either is published, it becomes an
additional credential/identity type with no other change.

### D11 — Consistency matrix: D1–D10 against the standalone-app framing (2026-08-31)

| Decision | Status vs standalone framing | Amendment |
|---|---|---|
| D1 usage ledger primitive; Crossplane = inventory | valid | key = `Customer` (was Organization) |
| D2 standalone app, app-internal `Customer`, OpenOva = adapter | **is** the framing | — |
| D2a per-mode entity matrix | valid | `BillingAccount` row → `Customer` (synced from Organizations) |
| D2b account duplication | valid | new |
| D3 three cases, two collectors | valid | case 1 = adapter-provided source; cases 2/3 = native |
| D3a collection modes | valid | platform watch = adapter; cloud change-log = native |
| D4 separate application | strengthened | title: standalone, integration is an adapter |
| D5 three placements, one chart | valid | Sovereign BSS = adapter-enabled; central = adapter-disabled |
| D6 seam with billing service (`/billing/metering/record`) | valid | adapter-only (absent without OpenOva) |
| D7 enforcement in org-controller from `Plan.IncludedQuotas` | valid | adapter-only; the app never enforces |
| D8 read-only IAM user + AK/SK per tenant | valid | native credential type |
| D9 UI: one tree, two lenses | valid | tree root = `Customer` set; Org rows appear only where the adapter syncs Organizations; drill-down unchanged |
| D10 profiles `sovereign` / `operator-central`, API-first | valid | profile = adapter on/off + identity + branding |
| `Organization.spec.costSources[]` | valid | GitOps-declared input to the adapter; absent without OpenOva |
| "no new platform entity type" (2026-08-31 a.m.) | valid | `Customer` is app-internal, not a platform kind |

## Alternatives rejected

- **Module inside `core/services/billing`** — couples chargeback to Org CRs and to a Sovereign;
  no per-Org instance, no central instance without Catalyst. Rejected for D4/D5.
- **Crossplane as the meter** (Observe-only MRs per tenant resource) — one OpenTofu Workspace per
  resource per interval is the wrong cost shape; no time axis; blind to non-Crossplane objects;
  the native `provider-huaweicloud` (v0.0.10, no `elb` group) is already rejected by ADR-0011.
- **Adopt Lago / OpenMeter first** — duplicates customers/wallets/invoices already in the billing
  service and adds components to carry through the 11-step cutover and Harbor prewarm. Revisit
  Lago only if invoicing/dunning/tax complexity grows; `bp-openmeter` only if event volume
  outgrows Postgres.
- **Pure event-driven collection (SMN push)** — events get lost or duplicated; needs tenant-side
  configuration and National Cloud egress to us; the cloud's own meter is hourly anyway.
- **Two full stacks per population** — two price books, two statement formats, two portals for
  the same operator; breaks the standalone-tenant → Org migration path.
- **Waiting on an operator-plane metering feed** — not assumable (tenant-facing API disproven;
  operator plane unreachable); it is an upgrade, not a prerequisite.

## Consequences

- One more Blueprint to carry through cutover (step 03 Harbor prewarm, step 11 provider pivot).
- Billing `customers` and chargeback both key on the Organization slug; no second account notion.
- `docs/STATUS.md:65` (`billing — Designed. No code.`) is stale and must be corrected with this
  ADR; `docs/GLOSSARY.md` gains `cost source`, `usage record`, `price book`, `statement`; "package" is not a modelled term — the term is **plan**.
- Chargeback is sellable without the full platform (the wedge into National Cloud) while the
  Sovereign placement gives central chargeback *and* the migration path in one deployment.
- Under the 5-pillar rule this is non-pillar work; it is a contracted partner workstream and a
  client-proposal commitment, so it needs its own EPIC. Without one it loses to the pillars.
- Our numbers are inventory-hours × the operator's list price; if the operator reconciles
  against the vendor's internal meter, small deltas will appear. Acceptable: the operator owns
  the rate card.

## Open inputs (accept the ADR when these land)

From the founder: standalone-tenant scenario (count, service mix, migration intent); deliverable
shape (statements vs legal invoices; which rate card); placement on the partner's Sovereign vs
bare Kubernetes. From National Cloud: operator-plane metering export — yes/no; EPS read policy +
tenants/Sovereigns in Enterprise Projects; cross-account agency test with a second account.

## Shipped-via

- `docs/adr/0014-chargeback-usage-ledger-and-split-deployment.md` (this ADR)
- `docs/sessions/2026-08-30/chargeback-requirements.md` — requirements R1.1–R1.10, R2.1–R2.8, R3.1–R3.8; proof methodology; probe tables
- Follow-ups (not yet shipped): `products/chargeback/` chart + image; platform collector; cloud collector; BSS "Usage / Statements" pages; `Plan.IncludedQuotas` single-source rendering in the organization-controller; GLOSSARY + STATUS updates
