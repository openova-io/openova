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

### D1 — The measurement primitive is a usage ledger keyed by `BillingAccount`, not Crossplane

An append-only `usage_records(account, source, resource_id, kind, sku, quantity, unit,
window_start, window_end, region, labels, raw_ref)`, idempotent on (source, resource, window).
Facts first; `rated_lines` and `statements` derive from it via an effective-dated price book.
Crossplane keeps its ADR-0011 role — the inventory/adoption layer that makes cloud resources
visible as Kubernetes objects — and is one collector's source, never the meter. Rationale: a
meter tied to the provisioning engine is blind to everything it did not provision (Tofu-built
substrate, Flux-built vClusters, pre-OpenOva tenants) and has no time axis.

### D2 — `BillingAccount` sits above the Organization

A billed party owns N cost sources: `openova-org`, `huawei-project`, `k8s-namespace`,
`file-import`. An OpenOva-managed customer has an Org source; a standalone tenant has only a
cloud-project source; a tenant that migrates onto the platform keeps the account and gains a
source. `Organization.spec.{planSlug,billingMode,kind}` stay as the Kubernetes-side enforcement
contract and map 1:1 to an account by Org slug.

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

### D4 — `bp-chargeback` is a separate multi-tenant application in this monorepo, not a module of catalyst-api/billing

`products/chargeback/` (chart + image), own CNPG database, own API and UI, own RBAC (operator-admin ·
account-admin · viewer) over a **BillingAccount tree** (operator → accounts → cost centres). It
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

### D8 — Access to standalone tenants = per-tenant read-only IAM agency

One OpenOva account; the tenant (or National Cloud centrally, for accounts it administers)
creates an agency trusting it with `Tenant Guest` + ReadOnly for ECS/EVS/EIP/ELB/NAT/OBS/RDS/DCS/CES/CTS,
all regions, 1-year validity. Onboarding = account name + agency name + project ids in the
chargeback UI. Revocation = the tenant deletes the agency.

### D9 — UI model: one account tree, two lenses — unified from day one (recorded 2026-08-30 after the founder's UI question)

**Decision: NOT two chargeback UIs** (one for the underlying Sovereign/cloud regardless of
OpenOva management, one for Organization-level chargeback that depends on OpenOva capabilities).
**One UI over one `BillingAccount` tree**, in which the Sovereign-level row is the *parent* of the
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

1. **Day 0 — standalone tenant.** A `BillingAccount` with one `huawei-project` source; appears as
   a *Tenant* row; account-owner lens via PIN login. Statements begin the first full period.
2. **Enterprise Projects granted.** The same row gains child rows per EP — department-level
   attribution with no change of screen.
3. **Migration onto OpenOva.** An Organization is created and **linked to the existing account**
   (1:1 by Org slug, D2). The row keeps its identity and history and gains an `openova-org`
   source: Org columns (plan, per-Application lines, tokens) and the **Limits** tab appear;
   cloud lines continue for whatever stays outside the platform. Statements are continuous —
   one account, two sources, one bill.
4. **Fully on-platform.** The `huawei-project` source is retired when the last external resource
   is gone; the row is now an ordinary Organization row. Nothing was re-onboarded and no
   history moved.

The reverse direction is also allowed (an Organization adds an external cloud project as a
second source) — the tree is symmetric because the account, not the Organization, is the root of
identity.

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
| Tenant root | Organization (1:1 by slug, D2) | cloud account (`domain_id`) with its own IAM users |
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

**Unknowns to verify with the operator, not to design around:** (1) does the operator's portal
expose an IdP for federation; (2) can the cloud IAM be used for verify-only login from outside
(option (b) is testable on our own tenant today). Until answered, (c) is the default.

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
- Two account notions (billing `customers` vs chargeback `BillingAccount`) must map 1:1 by Org
  slug; the mapping is owned by chargeback.
- `docs/STATUS.md:65` (`billing — Designed. No code.`) is stale and must be corrected with this
  ADR; `docs/GLOSSARY.md` gains `BillingAccount`, `cost source`, `usage record`, `price book`,
  `statement`; "package" is not a modelled term — the term is **plan**.
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
