# ADR-0028 — Commerce and billing: one billing product with two installation options

Status: Accepted
Date: 2026-11-13
Supersedes: ADR-0014
Supersedes: ADR-0001 in part: the retirement of the services `auth`, `tenant`, `catalog`, `provisioning`, `domain` and `gateway` (section 4.3 and items B6 to B11 of section 9.2); they stay as the storefront back end

Written by work package BSS-010. It replaces ADR-0014, which was never accepted, and states decision by decision what of ADR-0014 is kept, changed and withdrawn (section "What happens to ADR-0014"). It also changes one point of ADR-0001 (section "What happens to ADR-0001").

Terms used here:

- **The engine** is the billing product `products/chargeback`, chart and Blueprint `bp-chargeback`. Its product name is Catalyst BSS; its identifiers stay `chargeback`.
- **Order capture** is the service `core/services/billing`.
- **The storefront** is `core/marketplace`.
- **The offer** is the published document that says which packages, features and prices a Sovereign sells.
- **An instance** is one running installation of the engine with its own database.
- **The wallet** is the prepaid balance that order capture keeps per paying customer in its table `credit_ledger`.
- **The debit port** is the route `POST /billing/metering/record` of order capture.

## Context

Measured on `main` at commit `05b29b4e5`, on 2026-11-13.

- ADR-0014 has the status `Proposed` since 2026-08-30. The engine was built from it, but parts of it are not true in the code (see the table under "What happens to ADR-0014").
- The engine is installed on every Sovereign by the bootstrap-kit slot `clusters/_template/bootstrap-kit/13f-bp-chargeback.yaml`, with `adapter.enabled: true` and the profile `sovereign`. The chart's own default is `adapter.enabled: false` (`products/chargeback/chart/values.yaml`). The adapter lives in the engine process, in `products/chargeback/internal/adapter/openova` and `products/chargeback/internal/platform`.
- The plans are written down four times. The catalog seed prices S, M, L and XL at 2490, 4490, 7990 and 13990 baisa (`core/services/catalog/handlers/seed.go`). The engine's plan book prices them at 5, 9, 16 and 30 OMR a month (`products/chargeback/internal/store/planbook.go`). The organization-controller sizes quota from its own table `planQuotaTable` (`core/controllers/organization/internal/gitops/manifests.go`). The Organization definition fixes the plan names as the enum `[s, m, l, xl, flexi]` of `planSlug` (`products/catalyst/chart/crds/organization.yaml`).
- Order capture prices an order that carries `package_sku` from the engine's public packages document, and every other order from the catalog (`priceOrder` in `core/services/billing/handlers/handlers.go`). The storefront uses the catalog alone when the engine's document does not load (`core/marketplace/src/lib/packages.ts`).
- The wallet has two amount columns in `credit_ledger`: `amount_omr` in whole units and `amount_micro_omr` in millionths, with the unique idempotency key `external_ref` (`core/services/billing/store/store.go`). The debit port permits a negative balance and accepts any superadmin or sovereign-admin session (`core/services/billing/handlers/metering.go`).
- The engine suspends and resumes an Organization, and stops and resumes its overage, by HTTP calls to catalyst-api: `POST /api/v1/internal/organizations/{slug}/suspend`, `resume`, `overage-stop` and `overage-resume` (`products/chargeback/internal/platform/platform.go`).
- The engine has no event bus. Its `go.mod` requires `lib/pq` and the Kubernetes client libraries only.
- Model usage reaches money through a side-car. `core/services/metering-sidecar` multiplies the tokens of a request by one flat price (`PRICE_MICRO_OMR_PER_TOKEN`, default 156) and publishes the amount on the subject `catalyst.usage.recorded`. Order capture consumes it into `credit_ledger` (`core/services/billing/handlers/metering_consumer.go`). No token count reaches the engine's usage ledger.
- ADR-0001 retires six services of the storefront stack: `auth`, `tenant`, `catalog`, `provisioning`, `domain` and `gateway`. All six still exist under `core/services/` and are deployed by `products/catalyst/chart/templates/org-services/` (the code of `tenant` runs as the Deployment `organizations`). They carry the storefront's sign-in and order path today.
- `auth` publishes one event, `user.login`, and only to the Kafka topic `auth.events` (`core/services/auth/handlers/handlers.go`). `catalog` creates a Kafka producer at start (`core/services/catalog/main.go`) and publishes nothing with it.
- The engine stores no row-level security policy and no white-label setting: `products/chargeback` holds no `CREATE POLICY` statement. Access is limited by a scope in the application's own queries (`store.Scope`, `products/chargeback/internal/access`).

## Decision

| Decision | What is decided |
|---|---|
| B1 Positioning | One billing product with two installation options, not two products. Option `sovereign`: the engine runs inside the Sovereign with its platform adapter on. Option `operator-central`: the same chart and binary run on their own, without Catalyst, for a cloud operator. A cloud operator's customer that runs its own Sovereign and one that runs none are the same kind of buyer. |
| B2 The seam | The adapter stays in the engine process. Inside a Sovereign the seam is four contracts: the offer flows down, entitlement decisions flow down, model usage events flow in, and wallet debits go across. Each of the four is wired only when the adapter is on, so option `operator-central` carries none of them. No store method of the engine gets a wire form. |
| B3 One owner per entity | Every commerce and money entity has exactly one owner (table "Ownership" below). Where an entity exists twice today, one copy is deleted or reduced to a reference. |
| B4 Money | Order capture owns stored value: the wallet. The engine owns invoices, receivables, payment allocation, credit notes and tax. On a Sovereign the engine holds no stored value. The engine settles an invoice by a wallet debit through the debit port, which is extended and not replaced: it finds the wallet by the order that created the Organization, refuses an overdraft and accepts only the engine's ServiceAccount (BSS-124). Checkout books exact amounts in `amount_micro_omr` (BSS-121). The recurring card subscription and order capture's own invoice table are deleted once wallet settlement (BSS-G07) has passed on two consecutive release trains (BSS-123). |
| B5 One plan source | The offer is authored in the engine and published by projection: the event `catalyst.offer.published.v1`, one committer (catalyst-api), and one ConfigMap `commerce-offer` in the namespace `catalyst-system`, at `clusters/<sovereign-fqdn>/intent/commerce/commerce-offer.yaml` in the fleet repository. Its schema is `docs/contracts/commerce-offer.schema.json` (BSS-101). Every reader (quota, checkout, storefront, consoles) reads the committed offer. A default offer is embedded in one Go package, `core/controllers/pkg/offer`, so that a Sovereign without the engine still has a plan source. The default offer carries the numbers of the catalog seed; spike BSS-S1 first measures what a fresh Sovereign shows. The engine publishes its packages (BSS-106) and an engine without packages adopts the committed offer (BSS-107) before any reader switches to the committed offer (BSS-103, BSS-104, BSS-105). |
| B6 Checkout pricing | Order capture prices from the committed offer, which catalyst-api serves (BSS-102, BSS-104). It never calls the engine to price. There is no second price list and no fallback list (BSS-030). When the offer cannot be read, checkout refuses the order with 503 and the storefront says that prices are unavailable (BSS-035). The order records the version of the offer it was priced from. |
| B7 Entitlement decisions | Suspend, resume, overage stop and overage resume are events `catalyst.entitlement.decision.v1` from the engine's outbox, each with an identifier and a sequence per Organization (BSS-111). catalyst-api, the one committer, writes them into the Organization in Git (CTL-195). The HTTP calls from the engine to catalyst-api and the four internal routes they reach are deleted (BSS-112). A plan change is an order, never an engine decision. |
| B8 Usage ingestion | Inside a Sovereign the engine consumes exactly one usage event: `catalyst.usage.recorded.v1`. Its subject is in the billing area's space of the event registry `docs/contracts/C3-event-subjects.yaml`. The agent plane produces it, one event per model request, and defines its fields; AIP-111 creates its registry entry. The engine adds its consumer with the durable name `chargeback-usage` (BSS-131). The platform collector and the cloud collectors keep writing the usage ledger in process. Between two instances a different document is used: the usage statement (B10). The subject `catalyst.usage.recorded`, without a version, is a different subject; BSS-132 removes it together with the money path in order capture. |
| B9 Model usage | The engine books token counts as quantities per model alias and token class, and rates them from price items `llm.<alias>.<class>` in the source's price book (table "Model usage mapping" below). Model usage counts toward the Organization's spend limit. The per-request wallet debit at one flat price is retired (BSS-132). No price item for a model alias is shipped in the default offer, in a default price book or in a seed. |
| B10 Many instances | Buyer and seller, never master and replica. A child instance reports aggregated quantities upward as usage statements: one per day, a total per SKU, no customer, no resource name, no user and no money, signed with a link secret and delivered over HTTPS through the engine's outbox. The link modes are `none` and `push`. The Sovereign's owner is a customer of the parent instance, and the child instance is one source of that customer (source kind `bss-instance`). No database is federated. Money never crosses an instance boundary as data (BSS-301, BSS-302, BSS-303). |
| B11 Collector plug-in | The collector interface is introduced together with its second implementation, a collector of bill files in the FinOps FOCUS format (BSS-201, BSS-202). Native meter names stay per source, because the price book is per source. A neutral meter class is added beside them and has one reader from its first day: the rule that decides whether a stopped resource is billed (BSS-203). |
| B12 Secrets | The engine's master key and its database credentials come from OpenBao at run time (SEC-246, SEC-300). The cost-source credential stops being a Kubernetes Secret (BSS-141). Payment-provider keys leave the database (BSS-142). |
| B13 Deployment | The commerce ring stays on by default. The engine's provider modules, such as capacity pools, partners and e-invoicing, are shown or hidden by one setting with a default per installation option (BSS-150). Option `operator-central` is proven by a CI test, not by a Sovereign gate check (BSS-303). |
| B14 ADR-0014 | ADR-0014 is superseded by ADR-0028. Its decisions are settled one by one in the table "What happens to ADR-0014". |
| B15 The storefront back end | The six services that ADR-0001 retired are kept as the storefront back end and frozen in scope by a guard (BSS-050). They hold no price, no money and no cluster write once this record is carried out: the catalog's plans and add-ons go (BSS-108), and the provisioning service reaches the cluster through catalyst-api (CTL-205). `core/marketplace-api` is deleted (BSS-040), and so is the OpenMeter Blueprint `platform/openmeter` (SVC-164). Both front ends stay: the Organization console `core/console` and the storefront `core/marketplace` (CTL-710). |
| B16 Kafka-only events | The Kafka-only paths of `auth` and `catalog` are retired. No event subject is registered for them. The publish call of `auth` and the producers of both services go with the Kafka client (CTL-330). |
| B17 Events in the engine | The engine gains one bus client with a transactional outbox (BSS-090). Spend limit and budget are evaluated when the usage ledger is written (BSS-113). Cloud collection stays a poll, as a declared exception: a cloud bill is a measurement taken on a schedule. |

Proof. Fourteen gate checks, `BSS-G01` to `BSS-G14` in `docs/ledger/gates.yaml`, prove these decisions on a running Sovereign. Each is flipped to `required` by the package that delivers its capability. The first two are `BSS-G01` (self-service onboarding: voucher, sign-in, order and a serving Application with no sovereign-admin action; BSS-060 and BSS-070) and `BSS-G02` (one price list; BSS-030). A check counts only if it can fail, so each one states the branch it must exercise.

### Ownership

| Entity | Owner | Stored as | The copy that goes or is reduced |
|---|---|---|---|
| Organization | Git, the fleet repository | the Organization resource | the storefront's `tenant` records become a draft and member list keyed to the Organization slug; the engine's customer of kind `organization` stays a projection written by the adapter |
| Customer (the party that is billed) | the engine | `customers` | order capture's `customers` is reduced to a payer reference for the payment provider: `user_id`, `tenant_id`, `stripe_customer_id`, `email` |
| Partner, partner tier, retail rule | the engine | `partners`, `partner_tiers`, `partner_retail_rules` | none |
| Price book and price item | the engine, one book per source | `price_books`, `price_items` | the prices of the catalog's plans and add-ons; the constant `activeHotStandbyPriceOMR` of order capture; the literal `catalogPlans` of the engine |
| Plan and package matrix as authored | the engine | `features`, `package_entitlements`, `package_settings` | none |
| Plan as published (what is sold and enforced) | Git, `intent/commerce/commerce-offer.yaml` | the ConfigMap `commerce-offer` | the controller's `planQuotaTable`; the enum of `planSlug`; the catalog's plan deck |
| Entitlement as enforced | Git, the Organization: `spec.planSlug`, `spec.commerce`, `spec.suspended`, `spec.suspendReason`, `spec.overageStoppedUntil` | fields that exist | direct patches of the Organization |
| Entitlement decision (suspend, resume, overage stop, overage resume) | the engine | `entitlement_decisions` (new), with `customer_suspensions` and `spend_stops` as state | the HTTP enforcement client and the four internal routes |
| Subscription | Git, the Organization (`spec.planSlug` and `spec.commerce`); a term commitment is a row of the engine's `contracts` | fields that exist | order capture's `subscriptions` |
| Order | order capture | `orders` | none |
| Voucher | order capture | `promo_codes`, `promo_redemptions` | none; its money effect is a wallet credit |
| Wallet (prepaid stored value) | order capture on a Sovereign, one wallet per paying customer, reached from an Organization through its order; the engine's account in option `operator-central`, where no order capture exists | `credit_ledger`, exact amounts in `amount_micro_omr` | the engine's top-up entries on a Sovereign; the whole-unit rounding of checkout |
| Usage record | the engine | `usage_records` | the per-request money debit in `credit_ledger` and its consumer in order capture |
| Rated charge | the engine | `rated_lines` | none |
| Invoice (statement) | the engine | `statements`, `invoice_sequences`, `einvoice_documents` | order capture's `invoices` |
| Receivable | the engine | `account_entries` | none |
| Payment (record of account) | the engine | `payments`, `payment_intents`, `credit_notes` | the payment provider's subscription mirror in order capture; order capture keeps the provider's event log for deduplication |
| Tax | the engine | the tax settings and the frozen tax of each statement | none |
| Capacity pool, shape, placement | the engine, module `capacity` | `capacity_pools`, `sku_shapes`, `capacity_placements` | none |

The Sovereign itself is not a customer. Its own footprint is the engine's one internal source and is never billed. In a parent instance the customer is the Sovereign's owner, and the child instance is a source of that customer. Cloud sources and platform sources are two layers; a price book is assigned per source and its scope equals the source's layer. The price waterfall (list, partner, customer) is computed in rating and is never typed in.

### Model usage mapping

The engine reads these fields of `catalyst.usage.recorded.v1`: the envelope's `id` and `source`, and `data.organization`, `data.resource.kind`, `data.resource.id` (the model alias), `data.occurredAt` and `data.measures`. It deduplicates on `source` and `id`. Each quantity is divided by 1000 and added to the hourly usage record of the SKU below, for the Organization's platform source.

| Measure | SKU |
|---|---|
| `llm.tokens.input` (uncached input, booked as it arrives; nothing is subtracted) | `llm.<alias>.input` |
| `llm.tokens.cache_read` | `llm.<alias>.cached` |
| `llm.tokens.cache_write` | `llm.<alias>.cache_write` |
| `llm.tokens.output` | `llm.<alias>.output` |

A measure with another name is ignored. An event whose `data.resource.kind` is not `llm-model` is stored with the reject reason `unknown-kind`, acknowledged and not booked.

## What happens to ADR-0014

| ADR-0014 decision | Verdict | Now |
|---|---|---|
| D1 usage ledger as the measurement primitive | kept | |
| D2 a standalone application with its own customer; the platform is one adapter | kept | B1, B2 |
| D2a entity matrix per mode | kept as a description | its modes are the two installation options of B1 |
| D2b statement exchange between Sovereign and cloud operator | replaced | the usage statement of B10 |
| D3 and D3a collectors and the polling exception | kept | B17 keeps cloud collection as a poll |
| D3 the token side-car feeds the ledger | replaced | B8 and B9 |
| D4 the engine is its own product with its own database, API and UI | kept | B1 |
| D5 placements 1 (Sovereign component) and 3 (central instance) | kept | the two installation options of B1 |
| D5 placement 2 (one engine per Organization) | withdrawn | |
| D6 the engine never touches money | replaced | B4 |
| D7 one plan source in the catalog | replaced | B5: the committed offer |
| D8 a read-only cloud credential per customer, stored encrypted | kept | OpenBao is the key source (B12) |
| D8a customer intake by form, import and invitation | kept | |
| D9 one console over one account tree, with scopes | kept | |
| D9a decision to screen map | kept | |
| D10 two profiles, `sovereign` and `operator-central` | kept | the two installation options of B1 |
| D10 own identity provider, row-level security and white label | withdrawn as claims | none of the three exists; isolation is by scope in the application's own queries |
| D11 consistency matrix | replaced | this table |

## What happens to ADR-0001

ADR-0001 retires `auth`, `tenant`, `catalog`, `provisioning`, `domain` and `gateway` (section 4.3 and items B6 to B11 of section 9.2). That retirement is withdrawn: the six services stay as the storefront back end, frozen in scope (B15). Every other decision of ADR-0001 stands. ADR-0001 carries one header line that points here.

## Alternatives rejected

- **B1.** Two products with a contract between them: a wire form for the engine's store before any second Sovereign reports, and a second product to carry. Every function switched on in every Sovereign, as today: a Sovereign's owner sees provider functions they did not ask for. Replacing the engine by an open-source billing engine: the candidates are copyleft, gate partner functions behind a paid edition, or lack the partner waterfall and the cloud collector.
- **B2.** Moving the adapter out of the engine into a Sovereign-side service: a refactor with no visible gain while the engine runs in the same cluster. Keeping the HTTP calls that patch the Organization: a write to desired state that bypasses Git.
- **B3, B4.** The engine owning all money, the wallet included: checkout settles the order, the credit spend and the subscription in one database transaction today, and moving the wallet would make that a distributed transaction on the one walked customer journey. A new debit route with a new ledger schema: the ledger already has an exact amount column and an idempotency key, and the route exists; what is wrong is its key, its caller check and its overdraft rule. Both sides keeping a balance: two balances for one customer diverge.
- **B5.** The offer authored as a file in Git and imported by the engine: the package editor that exists writes the database. Readers calling the engine's public route, as today: checkout then depends on the engine being up, and an engine with no packages silently sells from another list. A new custom resource kind for the offer: no new entity types; a ConfigMap with a schema is enough. The default offer as a chart file: the organization-controller could not embed it, so the numbers would exist twice again.
- **B6.** Mounting the ConfigMap into order capture: order capture runs in another namespace, and one copy per namespace is three copies. Order capture reading the engine with a cache: the engine is then in the path of every sale.
- **B7.** The engine committing to Git itself: a second committer with a second credential. A decision type `plan-change`: a plan change is bought, so it starts as an order.
- **B8.** Moving the platform collector to events now: it already writes an idempotent ledger from informers; an event hop adds a failure mode and no function. A general HTTP ingestion route now: it would have no caller until an instance reports from outside. OpenMeter as the meter: it needs Kafka and ClickHouse, and its Blueprint does not start as shipped.
- **B9.** Keeping the side-car that prices at the gateway: one flat price, no token classes, streamed responses not metered. Using the gateway's own cost figure: an estimate, not a basis for an invoice.
- **B10.** One shared database for all Sovereigns of a cloud operator: a Sovereign must be able to bill with the link cut. The parent watching the child's cluster: impossible for a central instance and a breach of the child's independence. The child sending rated money: the parent would have to trust prices it does not set. More than one partner level in one instance: chaining instances gives that without a new ledger model.
- **B11.** An interface with one implementation: the single implementation would shape it. Renaming stored meter names to neutral ones: a data migration on issued financial records for no gain. The meter mapping in the provider pack: the engine must run without the platform repository.
- **B12.** OpenBao transit as an envelope key service for the engine: the stronger pattern, chosen once transit is opened to workloads. A mounted key already takes the key out of etcd.
- **B13.** The engine off by default: a default Sovereign sells and meters today. A licence switch per module: a commercial decision, not built.
- **B14.** Accepting ADR-0014 as written: four of its decisions are not true in the code (D2b, D6, D7 and the claims of D10). Editing it in place: decision records are immutable.
- **B15.** Retiring the six services now: they carry the only walked onboarding, and replacing them is a new storefront sign-in and back end with no rule behind it once prices, money and cluster writes are taken out of them. Leaving them unconstrained: they would grow again.

## Consequences

- Each package that carries a decision names ADR-0028. The packages are those of the epics BSS-100, BSS-110, BSS-120, BSS-130, BSS-140, BSS-200 and BSS-300, and BSS-020, BSS-030, BSS-035, BSS-040, BSS-050, BSS-060, BSS-070, BSS-090, BSS-150, BSS-160 and BSS-170, with the spikes BSS-S1 and BSS-S2.
- Packages of other areas build on it: CTL-195 (the decision consumer), CTL-205 (the storefront path through catalyst-api), CTL-330 (B16), CTL-710 (both front ends stay), AIP-111 (the usage event) and SVC-164 (OpenMeter goes).
- A Sovereign sells, enforces quota and suspends from documents in Git. It does not need the engine to be up to do so; statements and new decisions wait while the engine is down.
- The storefront back end keeps running, under a guard that keeps it from growing.
