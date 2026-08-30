# Chargeback requirements — capture from the 2026-08-30 founder session

**Status:** DRAFT requirements capture (session artifact, not an ADR). Every "today" claim below
was verified in code during the session; each carries a `file:line` reference. Design
recommendations are marked *proposed*. Open questions that need the operator or the partner
are collected in §5 and are the next input to this document.

**Why this exists:** two chargeback tracks were put on the table —

- **Track 1 — native OpenOva chargeback:** (Req 1) rate-limiting of resources by purchased
  package; (Req 2) pay-as-you-go. Question asked: *for the Sovereign (parent) and the
  Organization (child), what can we provide today?*
- **Track 2 — chargeback for the National Cloud partner's HCSO customers:** Huawei hosts
  HCSO in the national cloud and has no proper chargeback; the partner wants OpenOva to do
  chargeback for *their* customers. Two populations: (a) tenants provisioned through OpenOva
  (falls into Track 1), (b) standalone tenants with no OpenOva, or provisioned before OpenOva.

Related canon: `docs/GLOSSARY.md` (billing = "per-Org metering, invoicing"; Voucher;
Franchisee), `docs/DOD.md` §5 (P10 Billing Admin, J13 Billing & quotas, D27–D29, D35),
`docs/BUSINESS-STRATEGY.md` §10 (PAYG / ESA / franchise split), `docs/adr/0011` (Crossplane
adoption seam). There is **no ADR for the money path** — see §6.

---

## 1. The billing relationships in the model

| Tier | Who bills whom | Where it lives |
|---|---|---|
| **A** | Sovereign operator → its Organizations (customers) | BSS: `console.<fqdn>/billing` (Vouchers · Orders · Revenue) + `Organization.spec.{planSlug,billingMode,kind}` |
| **B** | Organization → its own Environments / Applications (internal cost centres) | `billingMode: chargeback \| showback` on `kind: internal` Orgs; per-app showback rows |
| **C** | OpenOva → Sovereign operator (franchise: $49/vCPU PAYG, ESA ladder, `OV-PRICE-2026-02`) | Marketing/pricing only — nothing in code |

Track 1 is Tiers A and B. Track 2 population (a) is Tier A on the partner's Sovereign;
population (b) is a **new** party: a customer with no Organization CR at all.

**Partner terminology (founder, 2026-08-30):** "Huawei" = Huawei+Omantel = National Cloud =
HCSO — **one party**. This document models exactly two parties on the partner side: **National
Cloud (the operator)** and **the tenant (its customer)**. It never splits ownership or
"who grants" between Huawei and Omantel.

### 1.1 The three chargeback cases (decided 2026-08-30)

| Case | What is metered | Collected via | Attributed via | Enforced? |
|---|---|---|---|---|
| **1. Org on an OpenOva Sovereign** | K8s workloads (pods, PVCs, tokens) | platform (labels, sampler, sidecar) | Organization CR | yes — plan quota |
| **2. Standalone National Cloud tenant** (no OpenOva / pre-OpenOva) | cloud resources (ECS/EVS/EIP/ELB/NAT/OBS/RDS/DCS…) | cloud collector (§3.5) | whole project = one account | no |
| **3. OpenOva-managed Sovereign's own cloud footprint** | cloud resources — same kind as case 2 | **same cloud collector as case 2** | **from the inside**: tofu state + deployment record + ADR-0011 adoption claims map each ECS/EIP/ELB to Sovereign + role (control-plane/worker) + region; name-prefix `catalyst-<slug>-<dep>` fallback | not by us — National Cloud's project quota |

**Design consequence:** two collectors — **platform** (case 1) and **cloud** (cases 2 + 3) —
one ledger, one price book, one statements engine. Case 3 adds only an *attribution map*
OpenOva already holds. It is the hinge between the two billing levels: National Cloud →
Sovereign operator (cloud rate card) and Sovereign operator → Orgs (OpenOva price book).
Holding both lets OpenOva allocate the Sovereign's cloud cost down to Orgs (per-Org share of
worker nodes from the case-1 sampler + a platform-overhead line) and show operator **margin**
= Org revenue − cloud cost — the "procurement basis" number the pricing model hinges on.
Open point: whether each Sovereign gets its own National Cloud project / Enterprise Project
(then case-3 attribution is free at the cloud level) or all share one Kom4DC project (today —
then the internal map is the only split).

---

## 2. Track 1 — native OpenOva chargeback

### 2.1 Req 1 — package-based resource limiting (entitlement)

| ID | Requirement | Today (verified) | Gap |
|---|---|---|---|
| R1.1 | A purchased package caps the Organization's compute | ✅ `planSlug` s/m/l/xl/flexi → `plan-quota` ResourceQuota (requests==limits) + `plan-limits` LimitRange rendered by the org-controller — `core/controllers/organization/internal/gitops/manifests.go:112-126, 477-560`; CRD `products/catalyst/chart/crds/organization.yaml:126-138` | Only CPU + memory. Quota enforcement never asserted by a UAT row; hw302 carries plan-S Orgs only (`docs/ledger/UAT.md` row 5 footnote) |
| R1.2 | Package caps storage | ❌ Seed carries 25/50/100/200 GB per plan (`core/services/catalog/handlers/seed.go:140-153`) but the quota never renders `requests.storage` | Render `requests.storage` + `count/persistentvolumeclaims` from the plan |
| R1.3 | Package caps object counts (pods, services, LBs, EIPs) | ❌ Only the (retired) Sandbox quota has `count/pods` (`core/controllers/sandbox/internal/gitops/manifests.go:207-222`) | Add count quotas; LB/EIP are Sovereign-level, out of Org scope |
| R1.4 | Package caps egress / bandwidth | ❌ nothing | Cilium bandwidth annotations via Kyverno mutate, per plan |
| R1.5 | Package caps API / LLM rate | ❌ every limiter keys on IP or user, never on Org — `core/services/gateway/ratelimit.go:41-110`, `core/services/billing/handlers/handlers.go:44-72` | Org-keyed limiter at the gateway; token caps from `credit_ledger` balance |
| R1.6 | Isolation follows the package | ✅ tier gate: `""`/s/free → host namespace, m/l/xl/flexi → dedicated vCluster (`manifests.go:151-174`, mirrored `organization_provisioning.go:346-412`, UI `organizations.api.ts:139`) | vCluster **size** is not plan-derived (fixed in `platform/bp-*-vcluster/chart/values.yaml`) |
| R1.7 | Flexi is metered, not capped | ⚠️ Flexi renders no ResourceQuota (`manifests.go:206-213`) — unbounded and unmetered | Burstable split: floor(requests) < ceiling(limits); meter it (R2) |
| R1.8 | One source of truth for the package | ❌ three copies: `seed.go` (price/spec), `planQuotaTable` (caps), `isolationForPlan` (UI) | Make catalog `Plan.IncludedQuotas` (`core/services/catalog/store/store.go:158-173` — exists, only sandbox plans use it) the source the controller renders from |
| R1.9 | Package is recurring (monthly) with proration / expiry | ❌ orders are one-shot; `subscriptions` table is Stripe-only; vouchers have **no expiry** (`core/services/billing/store/store.go:263-286`); voucher `plan_tier` is informational, not enforced (`store.go:139-146`) | Plan emitter into the usage ledger (§4.3); voucher validity window |
| R1.10 | Add-ons / bundles price correctly | ⚠️ add-ons priced; `Bundle.Discount` (10/15/20 %) is seeded but never read by `computeOrderTotal` (`handlers.go:1134-1177`); apps are free | Decide whether bundles/apps carry price |

**Prices today (Tier A):** S 5 · M 9 · L 16 · XL 30 · Flexi 0 OMR/mo (`seed.go:140-153`), + 5 OMR
for active-hot-standby (`handlers.go:1116-1117`). Vouchers grant `credit_omr` into
`credit_ledger`; checkout is credit-covered (no Stripe leg walked). Note the pricing-model
memory: at list procurement the Guaranteed plans are underwater because sold vCPU = reserved
vCPU (requests==limits); overcommit is only sellable on Flexi once it is metered.

### 2.2 Req 2 — pay-as-you-go

| ID | Requirement | Today (verified) | Gap |
|---|---|---|---|
| R2.1 | Usage facts are recorded per Org over time | ❌ for infra. Showback `GET /api/v1/org/consumption` is a **point-in-time** sum of pod *requests* in abstract units (cpu-milli 1.0 / GiB 4.0 / GiB-storage 0.25) — `products/catalyst/bootstrap/api/internal/handler/org_consumption.go:95-105` | No time axis, no history, no `usage_records` table |
| R2.2 | Storage is attributed | ⚠️ PVCs are summed only when attached to a pod (`dashboard.go:639-656`); PVCs carry **no** Org label | Kyverno mutate on PVC create (same pattern as `platform/kyverno-policies/.../21-app-label-mutate.yaml`) |
| R2.3 | LLM tokens are metered and charged | ✅ end-to-end: `metering-sidecar` → NATS `catalyst.usage.recorded` → billing `MeteringConsumer` → `credit_ledger` micro-OMR @ 156 µOMR/token (`core/services/metering-sidecar/main.go:56`, `core/services/billing/handlers/metering_consumer.go`, `store.go:281-313`) | Writes **money** directly; should write **quantity** so it can be re-rated |
| R2.4 | Actual utilisation (not just requests) is available | ❌ Alloy ships an **empty** config (`platform/alloy/chart/values.yaml:84-87`) → Mimir receives nothing; no kube-state-metrics; no OpenCost/Kubecost | Populate Alloy scrape + kube-state-metrics; then OpenCost or Mimir-based rating |
| R2.5 | A price book rates usage in currency | ❌ no price book; showback is explicitly "not a billed invoice" | Effective-dated per-Sovereign price book editable in BSS |
| R2.6 | Statements / invoices per period | ❌ `invoices` table is Stripe-shaped, single scalar amount, no line items; no Usage/Statements page (BSS = Vouchers · Orders · Revenue only, `billing-sections.ts:21-25`); `GET /api/v1/org/bss/overview` is a hard-coded zero stub (`org_bss_overview.go:29-33`) | Statements with line items; BSS "Usage / Statements" section |
| R2.7 | Tier B — cost centres inside an Org | ⚠️ enum only; `BillingModeGate.tsx` just hides the payment page | Same ledger, one level down (Environment / Application as cost centre) |
| R2.8 | Tier C — Sovereign reports worker-vCPU-hours to the mothership | ❌ nothing | Same collector, different consumer; also closes the `OV-PRICE-2026-02` "worker nodes only" gap flagged in a client financial model (`openova-private/engagements/`) |

### 2.3 Answer to "what can we provide today, parent vs child"

- **Sovereign → Org (Tier A):** package sale + prepaid credit + compute cap + isolation tier:
  **built and walked** (UAT rows 72–93, 99–100, 116–122 green). Recurring billing, storage /
  count / rate caps, metered PAYG on infra, statements: **not built**.
- **Org → its apps (Tier B):** live showback snapshot per app (UAT rows 21–25): **built**.
  Cost-centre chargeback over time: **not built**.
- **Only genuinely usage-based path today:** LLM tokens.

---

## 3. Track 2 — chargeback for the partner's HCSO customers

### 3.1 Population (a) — tenants provisioned through OpenOva

Identical to Track 1 Tier A on the partner's Sovereign. Additional facts that matter on Huawei:

- **No cloud-side attribution exists.** `common_tags` is defined at
  `infra/providers/huawei/main.tf:171-175` and referenced by **zero** resources; every Huawei
  resource is `lifecycle { ignore_changes = [tags] }` ("tags disabled Wave 5.4 — HCS tag API
  divergence"). "Enterprise Project" / EPS appears **nowhere** in the repo. The only correlator
  is the `catalyst-<slug>-<dep>` name prefix (`main.tf:167`; sweeper fallback
  `internal/providers/huawei/provider.go:864-880`).
- **No Huawei billing/CBC/pricing API is called anywhere** (`internal/providers/`, `infra/`).
- Huawei services touched by OpenTofu: VPC, subnet, peering, secgroups, EIP, NAT, ECS, KPS,
  OBS (via S3), ELB (`main.tf:179-1884`); EVS via the in-cluster CSI, not Tofu.

### 3.2 Population (b) — standalone tenants (no OpenOva / pre-OpenOva)

| ID | Requirement | Notes |
|---|---|---|
| R3.1 | A billed party that is **not** an Organization CR | *Proposed* `BillingAccount` in the billing Postgres with N cost sources (`openova-org`, `huawei-project`, `k8s-namespace`, `file-import`). A tenant that later migrates onto the platform keeps the account and gains a source — continuous statements |
| R3.2 | Inventory + hours of the tenant's Huawei resources | *Proposed* hourly collector using the existing sigv3 client + endpoints (`provider.go:121-129`; `versions.tf:55-90`): ECS `cloudservers/detail` (flavor, status, created), EVS `cloudvolumes/detail`, EIP `publicips`, ELB `/v3/elb/loadbalancers`, NAT, OBS size; plus RDS/DCS/GaussDB/etc. list APIs for the NC PaaS catalogue |
| R3.3 | Rating against the partner's rate card | NC catalogue parsed this session (`openova-private/engagements/national-cloud-program/inputs/national-cloud-price-catalog-2026.xlsx`, "National Cloud Services" sheet, 259 rows): categories Computing 51 · Database 71 · DB-storage 9 · Storage 7 · DR&Backup 3 · Network 21 · Security 36 · O&M 4 · PaaS 40; units Node / set / GB / instance / Cluster; **every line is OMR per YEAR** — no hourly or on-demand SKU exists. Unit prices are partner-confidential and stay in the private workbook. PAYG is therefore a *derivation* (list/8760 or list/12) the partner must bless. (Copied into `openova-private` inputs this session per the engagements convention.) |
| R3.4 | "Stopped ≠ billed?" policy | Collector must record instance `status`; whether a stopped ECS bills compute, storage-only, or nothing is a price-book knob. Raised by a National Cloud client engagement (discovery questions, `openova-private/engagements/`) |
| R3.5 | Statements per tenant per period; export to the partner's ERP | *Proposed* statements + CSV/API export; legal invoicing stays with Omantel unless asked otherwise |
| R3.6 | Tenant-facing read access | *Proposed* PIN login scoped to the BillingAccount (auth is already passwordless) |
| R3.7 | Entitlement vs consumption reporting | Enforcement is **not possible** (we do not own their IAM quotas). National Cloud owns entitlement; it supplies the entitlement, OpenOva reports consumption against it; for a reseller that *is* chargeback |
| R3.8 | Sub-tenant attribution (departments inside a tenant) | Depends on TMS tags / Enterprise Projects working on HCSO — unknown (tags were disabled on HCS Kom4DC) |

### 3.3 Facts about the platform gathered this session

- Platform is **HCSO (Huawei Cloud Stack Online)**, Huawei-operated, regions Muscat `me-east-215`
  + Duqm `me-east-216`, single-AZ each; OpenOva does not own the hypervisors
  (`openova-private/engagements/national-cloud-program/research/HANDOVER.md:57-75`).
- National Cloud owns entitlement, quota, commercials and the platform (one party — §1);
  partner-onboarding paperwork lives in `openova-private/engagements/national-cloud-program/`.
- The private repo holds **no written chargeback ask** from the NC program (0 hits for
  chargeback / showback / Enterprise Project / CBC / tenant billing). The only chargeback-shaped
  promise on paper is one client proposal committing to "PAYG billing with quarterly invoicing
  and consumption reporting" (`openova-private/engagements/`). The Huawei-partnership
  memory records "Chargeback module" as an agreed workstream.
- HCSO native metering/CDR export: **could not be verified** from public docs (Huawei support
  pages require login). First question for Huawei (§5).
- Cloud Eye (CES) is in the NC catalogue — a future source of actual utilisation.

---

### 3.4 Access to tenant data — proof methodology and measured results (Q1)

Two access models, one counterparty:

| Model | Who grants | What we get | Status after probing |
|---|---|---|---|
| **1. Operator-plane metering feed** | National Cloud | its own per-tenant metering/CDR export or billing API | **Tenant-facing billing API: disproven** (below). Operator plane: not reachable from outside — only National Cloud can answer |
| **2. Per-tenant read-only IAM agency** | the tenant (or National Cloud on its behalf for accounts it administers) | API inventory + change log + utilization, identical to how we read our own tenant | **Every required API proven working on our own tenant** (below) |

**Proof methodology — what an HTTP status means on the Kom4DC API gateway** (all endpoints
`<svc>.me-east-215.kom4dc.nationalcloud.om` resolve to one wildcard IP, so DNS proves
nothing; the gateway's error codes do):

| Signal | Meaning | Strength |
|---|---|---|
| `404 APIGW.0101` "API does not exist or has not been published" | no such published API on this environment | disproof — for that path |
| `401 APIGW.0301` "x-auth-token not found" (unauthenticated) | the route is published; a service is registered behind it | existence only — not that it works or that we may use it |
| after AK/SK-signed call: `200` | proven | sufficient |
| after auth: service-level `401/403` (e.g. `EPS.0003 Unauthorized user`) | backend exists and evaluated our identity; policy missing | existence proven, permission to ask for |
| after auth: `404 APIGW.0101` | still not published | disproof |

Controls used: `ecs` (known-good → 401 unauth'd, 200 authed) and a garbage host (known-bad →
404). Credentials: the mothership `catalyst-api` pod's `CATALYST_HUAWEI_{ACCESS_KEY,SECRET_KEY,
PROJECT_ID,REGION}` (Secret `catalyst/huawei-operator-creds`), signed with the same
`SDK-HMAC-SHA256` scheme as `products/catalyst/bootstrap/api/internal/providers/huawei/sigv3.go`;
probe script shredded after use, secrets never printed.

**Measured 2026-08-30 on our own National Cloud tenant (Kom4DC `me-east-215`):**

| Service | Unauth'd | Authenticated | Verdict |
|---|---|---|---|
| `ecs /v1/{pid}/cloudservers/detail` (control) | 401 | **200** — 13 servers, `flavor.vcpus/ram` | inventory works |
| `ces /V1.0/{pid}/metrics` | 401 | **200** — `SYS.ECS cpu_util` per instance | utilization works |
| `ces /V1.0/{pid}/events` | 401 | **200** — system events (`EIPBandwidthOverflow`, …) | event log works |
| `cts /v3/{pid}/traces` | 401 | **200** — API audit trail (`DetachServerVolume`, …) | **lifecycle change log works** (despite CTS being absent from the NC price list — *price-list presence ≠ availability, in both directions*) |
| `smn /v2/{pid}/notifications/topics` | 401 | **200** — 0 topics | push notifications possible |
| `eps /v1.0/enterprise-projects` | 401 | **401 `EPS.0003 Unauthorized user`** | service exists; our user lacks the policy |
| `tms /v1.0/predefine_tags` | 401 | 401 `TMS.0003 Unauthorized user` | exists, not granted — why our Huawei tags are disabled |
| `bss` billing — 10 path variants (`/v2/bills/customer-bills/{monthly-sum,res-fee-records,res-records/query}`, partner-bills, `/v1.0/{domain}/customer/account-mgr/balances`, `/v2/accounts/customer-accounts/balances`, v1.0 monthly-sum, global host, via `iam` host) | 404 | **404 `APIGW.0101`** | **no tenant-facing billing API on National Cloud** |
| `metering.*`, `aom` metrics path, `ces batch-query-metric-data` | 404 | — | not published (path variants may exist) |
| `oc.*`, `manageone.*` (operator portal hosts) | DNS: no record | — | operator plane not exposed externally |

**Consequences:**
- Model 1 is **not assumable**. The one question to National Cloud is now narrow: *"Does your
  operations portal export per-tenant metering (API or scheduled file), and can OpenOva get a
  read account?"* Yes/no.
- Model 2 is **fully buildable today** — it is the access we already have to our own tenant,
  granted N times via IAM agencies (`agency_domain_name` is already in our HCS provider config,
  so agencies exist in this API family; cross-account use on HCSO to be confirmed by one test).
- Second, cheap ask: grant our user the **EPS read policy** and place tenants/Sovereigns in
  Enterprise Projects → attribution below "whole project" without tags (R3.8).

**How model 2 is granted, per tenant:** one OpenOva account (`openova-chargeback` or the
Sovereign's own) → tenant admin creates an IAM **Agency** trusting it (policies `Tenant Guest` +
ReadOnly for ECS/EVS/EIP/ELB/NAT/OBS/RDS/DCS/CES/CTS; all regions; 1-year validity; National
Cloud ships this as a one-page onboarding step) → account name + agency name + project ids
entered in BSS → Billing → Accounts → collector obtains own token, `POST /v3/auth/tokens`
`assume_role{domain_name, agency_name}` scoped to the tenant project → runs §3.5 → revocation =
tenant deletes the agency.

### 3.5 Model 2 collection mechanics — polling vs event-driven (Q2)

Everything below is measured working on our tenant (§3.4); under an agency the same calls run
against the tenant's project.

| Layer | Source | Cadence | Gives |
|---|---|---|---|
| **Change log** | `cts` traces (every create/delete/resize/stop/start API call, timestamped) + `ces` events | poll every 5 min | exact lifecycle → exact billable hours; no missed create-and-delete-within-the-hour |
| **Snapshot** | ECS/EVS/EIP/ELB/NAT/OBS/RDS/DCS list APIs (`flavor`, size, `status`, `created`) | hourly | reconciliation — the ledger self-corrects against anything the log missed |
| **Utilization** | `ces` metrics (`cpu_util`, disk, bandwidth; CES keeps history, so hourly pulls lose nothing) | hourly | usage-based rating later; "stopped ≠ billed?" policy input |
| **Push (optional)** | `smn` topic → HTTPS subscription to an OpenOva webhook | on event | near-real-time dashboards; needs a topic in the tenant account + National Cloud egress to our endpoint — **unverified** |

**Decision: poll the change log, not the inventory.** That is near-event-driven with zero
tenant-side configuration and no lost events; the hourly snapshot makes the ledger
self-correcting. Pure push (SMN) is a dashboard optimisation, not a billing basis — events get
lost or duplicated, and the cloud's own meter is hourly anyway. Cost is trivial: ~10 calls per
tenant per region per 5-minute tick (~3k calls/hour at 100 tenants).

Attribution under model 2 is *project = account* (tags unavailable); Enterprise Projects (if
granted) add the department level.

---

## 4. Proposed architecture (summary of the session recommendation)

**Premise correction — Crossplane is not the meter.** On a live Sovereign Crossplane runs
`provider-opentofu` + `provider-hcloud` only; the single instantiated XRD is `XCloudAdoption`
(ADR-0011): an Observe-only OpenTofu `Workspace` per adopted ELB/ECS/VPC, labelled by sovereign +
deployment-id, **no Organization label**
(`platform/crossplane-claims/chart/templates/compositions/cloudadoption.yaml`,
`products/catalyst/bootstrap/api/internal/provisioner/adoption.go`). Controllers create no
claims (`core/controllers/organization/.../organization_controller.go:67-71`); vClusters, CNPG,
DNS, buckets, pods, PVCs and tokens are not Crossplane objects. ADR-0011 rejected the native
`provider-huaweicloud` for HCS endpoint reasons (checked this session: v0.0.10, last commit
2025-11-12, API groups `ecs eip evs obs rds vpc nat cce iam …` — no `elb`; its credentials
schema does pass `cloud` / `endpoints` / `auth_url` through — an ADR footnote, not a change of
direction). A meter tied to the provisioning engine is blind to everything not provisioned by it
— which is exactly population (b). Crossplane keeps its ADR-0011 role as the **inventory /
adoption** layer and becomes one collector's source.

1. **Ownership graph** — `BillingAccount` above the Organization (R3.1). Org CR keeps
   `planSlug` / `billingMode` / `kind` for K8s-side enforcement.
2. **Usage ledger** — append-only `usage_records(account, source, resource_id, kind, sku,
   quantity, unit, window_start, window_end, region, labels, raw_ref)`, idempotent on
   (source, resource, window). Units: vcpu-hour, gib-hour, gb-month, instance-hour, lb-hour,
   eip-hour, gb-egress, token. Facts first; `rated_lines` and `statements` derive from it.
   `credit_ledger`, vouchers and orders stay as they are ("canonical voucher implementation —
   do not redesign", `docs/RUNBOOKS.md`).
3. **Collectors** (one interface): plan emitter (plan-month/hour per Org); K8s allocation
   sampler from `buildPodRows` (`dashboard.go:471`) hourly — one host-cluster sampler covers
   every per-Org vCluster because the syncer mirrors pods into host namespace `<slug>`; Huawei
   inventory collector (R3.2); Huawei metering import if HCSO exports CDRs; token collector
   (exists, reroute to quantity); `CloudAdoption` claims labelled `billing.openova.io/account`
   *generated from* the collector's inventory for the cloud-view — not used as the meter (one
   Tofu Workspace per resource per hour is the wrong cost shape).
4. **Rating** — effective-dated per-Sovereign price book in BSS with per-account overrides
   (negotiated discounts, ESA commitments); three books to import: OpenOva plans, token price,
   NC catalogue. Oman VAT as a statement line. Build thin in Go inside `core/services/billing`
   (embedded-Go migrations, same pattern as `promo_codes`); evaluate Lago only if
   invoicing/dunning/tax complexity grows, `bp-openmeter` (already a catalogue Blueprint,
   `platform/openmeter/`) only if event volume outgrows Postgres. Every extra component must
   survive the 11-step cutover + Harbor prewarm.
5. **Enforcement** — R1.2–R1.8.

### Phasing (ICE order)

| Phase | Scope | Outcome |
|---|---|---|
| 0 (days) | `billing_accounts`, `usage_records`, `price_book`, `rated_lines`, `statements`; plan emitter; K8s allocation sampler; PVC label mutate; BSS "Usage / Statements" section | Monthly OMR chargeback/showback for Tiers A + B on OpenOva-managed Orgs |
| 1 (1–2 wk) | Huawei inventory collector on the existing client; NC rate-card import; cloud-account onboarding in BSS; pilot with one partner tenant | The Track 2 population-(b) deliverable |
| 2 | R1.2–R1.8 enforcement completion; single-source plan | Req 1 complete |
| 3 | Alloy + kube-state-metrics → Mimir; OpenCost or Mimir-based usage rating; CES utilisation on HCSO; Lago/OpenMeter if warranted; Tier C reporting | True usage-based PAYG |

---

## 5. Open questions — INPUT NEEDED

### 5.1 From the operator (the HCSO scenario was cut off mid-description)

1. **Complete the standalone-tenant scenario.** The description ended at "So I guess they are
   different… here what do you suggest?" Specifically: how many standalone tenants, which
   services they consume (ECS/EVS/OBS only, or the RDS/GaussDB/DCS/PaaS lines too), and
   whether any of them are expected to migrate onto OpenOva later.
2. **What must OpenOva deliver for them** — showback statements, chargeback statements to
   Omantel's cost centres, or legal invoices to the end customer? And against which rate card:
   the NC annual list (derived hourly/monthly), a partner-negotiated card, or one per tenant?
3. **How does OpenOva get read access** to a tenant's resources on HCSO — a per-tenant
   read-only IAM agency granted by the tenant, an operator-level tenant list from Omantel, or a
   metering export from Huawei? (This decides collector (b) vs (c) in §4.3.)
4. **Is this a Sovereign-level BSS capability on the partner's Sovereign (omantel.biz), or a
   standalone OpenOva service?** The recommendation assumes the former.

### 5.2 For National Cloud (one party; design inputs, not blockers)

1. **Operator-plane metering:** does the operations portal export per-tenant metering (API or
   scheduled file), and can OpenOva get a read account? (Tenant-facing billing API is already
   disproven — §3.4.) Yes/no decides model 1.
2. **Agencies:** confirm cross-account IAM agencies work on HCSO (one test with a second
   account). Decides the onboarding step for model 2.
3. **Enterprise Projects:** grant our user the EPS read policy and place tenants / Sovereigns
   in Enterprise Projects (`eps` exists, `EPS.0003` today). Decides R3.8 and case-3 attribution.
4. **Rate derivation:** annual list → PAYG as list/8760 hourly or list/12 monthly; the
   stopped-instance policy (R3.4).
5. **Invoicing:** who issues the legal invoice; export format its ERP needs.

---

## 6. Governance gaps surfaced

- **No ADR for the money path.** Commercial policy sits in `docs/BUSINESS-STRATEGY.md` §10,
  the runtime model in `products/catalyst/chart/crds/organization.yaml`; never reconciled.
  Proposed: `docs/adr/0014-chargeback-usage-ledger.md`.
- `docs/STATUS.md:65` says `billing — 📐 Designed. No code.` while `STATUS.md:149`,
  `GLOSSARY.md:25` and ~25 green UAT rows say otherwise — stale row.
- `docs/ARCHITECTURE.md:1662` references `products/axon/DESIGN.md`, which does not exist; "Axon
  PAYG" is slide-only.
- Under the current 5-pillar rule this work is non-pillar, yet it is a contracted Huawei
  workstream and a client-proposal commitment. It needs to be an explicit EPIC + ADR or it will keep
  losing to the pillars.
- Terminology: neither "package" nor "SKU" is a modelled concept; the code says `plan` /
  `planSlug` and `bundle`. Use "plan" in canon (`docs/GLOSSARY.md`) unless the founder
  decides otherwise.

## 7. Evidence index

- Money DB: `core/services/billing/store/store.go:181-333` (customers, subscriptions, orders,
  invoices, promo_codes, credit_ledger, promo_redemptions — embedded-Go migrations; no SQL files)
- Pricing engine: `core/services/billing/handlers/handlers.go:1116-1177`
- Plan catalogue: `core/services/catalog/handlers/seed.go:140-153`, type `store.go:158-173`
- Quota rendering + tier gate: `core/controllers/organization/internal/gitops/manifests.go:112-213, 477-560, 951-966`
- Showback: `products/catalyst/bootstrap/api/internal/handler/org_consumption.go`, `dashboard.go:471-656`, UI `sovereign/organizations/ShowbackPanel.tsx`
- BSS UI: `products/catalyst/bootstrap/ui/src/pages/sovereign/bss/{VouchersPage,OrdersPage,RevenuePage}.tsx`, `billing-sections.ts`, `lib/bss.api.ts`
- Token metering: `core/services/metering-sidecar/`, `core/services/shared/events/nats.go:44-105`, `core/services/billing/handlers/metering_consumer.go`
- Rate limits: `core/services/gateway/ratelimit.go:41-110`, `core/services/billing/handlers/handlers.go:44-72`
- Labels: namespace `openova.io/organization` (`manifests.go:224-229`); quota `openova.io/plan`; pod `catalyst.openova.io/application` (Kyverno `21-app-label-mutate.yaml`); Application CR `catalyst.openova.io/{organization,blueprint,instance}` (`instances/create.go:376-382`); node `openova.io/region`; **PVCs: none**
- Crossplane: `docs/adr/0011-opentofu-crossplane-adoption-seam.md`; `platform/crossplane-claims/chart/templates/{xrds,compositions}/cloudadoption.yaml`; `clusters/_template/infrastructure/providers/base/provider-opentofu.yaml:81`
- Huawei: `infra/providers/huawei/main.tf:167-175` (name_prefix, unused common_tags), `versions.tf:55-90` (endpoints), `products/catalyst/bootstrap/api/internal/providers/huawei/provider.go:121-129, 839-880`
- Metrics: `platform/alloy/chart/values.yaml:84-87` (empty config), `platform/grafana/chart/values.yaml:414` (Mimir datasource)
- Partner context: `openova-private/engagements/national-cloud-program/research/{HANDOVER,BRIEFING}.md`, `openova-private/engagements/national-cloud-program/inputs/national-cloud-price-catalog-2026.xlsx` (NC list, 259 rows; copied from `/tmp/Price Catalog.xlsx` this session)
