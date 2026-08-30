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
