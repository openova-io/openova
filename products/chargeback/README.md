# chargeback — standalone chargeback service (`bp-chargeback`)

**What it is.** A standalone application that meters cloud usage per customer,
rates it against a price book and produces statements. It owns its customers,
its Postgres database, its API and its UI. It is the implementation of
[ADR-0014](../../docs/adr/0014-chargeback-usage-ledger-and-split-deployment.md)
(EPIC #6723): the usage ledger is the measurement primitive, `Customer` is an
app-internal object, and OpenOva is one adapter among others.

**Role in Catalyst.** Application Blueprint. It runs on a Sovereign as the BSS
chargeback component, as a per-Organization Application, or on bare Kubernetes
as an operator's central instance (profile `operator-central`). It has no hard
dependency on Catalyst CRDs; the cloud collector needs only a read-only IAM
user's AK/SK per customer project.

**Canonical docs.** [`docs/ARCHITECTURE.md`](../../docs/ARCHITECTURE.md),
[`docs/DOD.md`](../../docs/DOD.md) §J13 (Billing & quotas),
[`docs/GLOSSARY.md`](../../docs/GLOSSARY.md). Inside this application the buyer
is a *customer*; when a customer is a synced Organization the term Organization
is used in user-facing text.

## OpenOva adapter (lane D — ADR-0014 D2 case 1)

Built into this same binary (`internal/adapter/openova`), running only when
`PROFILE=sovereign` and in-cluster Kubernetes configuration is available
(`ADAPTER_ENABLED` overrides in either direction; nothing runs without a
cluster). The `operator-central` profile never runs any of it — the engine
keeps its D5 invariant of zero Catalyst dependencies.

**Organization → Customer sync.** A dynamic-client list+watch on
`organizations.orgs.openova.io/v1` (absence of the CRD is logged once and
idled on, probing every 5 minutes). Each Organization upserts one customer:
slug = Org slug, `kind=organization`, `org_slug` set, `admin_email` from the
owner roster (`role: owner` preferred, blank-pending when the roster is
empty), `billing_mode` from `spec.billingMode` (real→real,
chargeback→chargeback, showback→showback), status active. GitOps-declared
`spec.costSources[]` (see the Organization CRD,
`products/catalyst/chart/crds/organization.yaml`) become `cost_sources`
rows; a `credentialRef` is resolved read-only from the named Secret in the
Organization's host namespace (= slug) — value either JSON
`{"accessKey","secretKey"}` or `ACCESSKEY:SECRETKEY` — sealed with
`APP_ENCRYPTION_KEY`, linked, and verified through the Huawei verifier.
Deleting an Organization SUSPENDS its customer; nothing is ever deleted —
statements and the usage ledger are billing history.

**Platform collector.** Shared informers on namespaces, pods and PVCs; the
join key is the `openova.io/organization` namespace label (the same key the
sovereign-admin dashboard's `buildPodRows` uses;
`catalyst.openova.io/organization` accepted as the legacy spelling).
Event-driven with an hourly reconciliation pass (ADR-0014 D3a) and a
debounced emit for touched Organizations. Usage windows are sliced by the
same `internal/window` math as the cloud collector — hour-bounded,
idempotent per `(source, resource, sku, window_start)`:

| SKU | Unit | Quantity per hour |
|---|---|---|
| `k8s.vcpu` | vcpu-hour | sum of the pod's container CPU requests (cores) |
| `k8s.mem_gb` | gib-hour | sum of the pod's container memory requests (GiB) |
| `k8s.pvc_gb` | gb-hour | PVC capacity (GB), joined to the Organization by namespace |

One `cost_source` of kind `openova-org` is auto-created per Organization;
records land on it, source kind `openova-org` (the request is the
entitlement the plan quota enforces, so the request is what is billed).

**Billing hook (D6).** Off unless `BILLING_HOOK_URL` is set. After
`POST /statements/{id}/issue` for a customer with `kind=organization` and
`billing_mode=real`, the statement total is posted to
`<BILLING_HOOK_URL>/billing/metering/record`
(`core/services/billing/handlers/metering.go`) as a negative micro-OMR
amount with `metadata.request_id` = the statement id — billing's
`external_ref` — so a re-issue can never debit twice (billing answers
`duplicate: true`). Auth is `Authorization: Bearer $BILLING_HOOK_TOKEN`
(superadmin, per the metering endpoint's contract). A hook failure leaves
the statement issued; issuing is idempotent, so re-POSTing issue repeats the
hook.

**RBAC the chart needs (least privilege).** The adapter is read-only against
the cluster:

| Resource | Verbs | Scope |
|---|---|---|
| `organizations.orgs.openova.io` | get, list, watch | ClusterRole (the CRD is cluster-scoped) |
| `namespaces`, `pods`, `persistentvolumeclaims` | get, list, watch | ClusterRole (Organization namespaces are discovered by label) |
| `secrets` | get | ONLY the credential Secrets `costSources[].credentialRef` names, in Organization host namespaces — grant per-Organization Roles on the named Secrets (RBAC `resourceNames` works for `get`), never a cluster-wide secrets read |

The bp-chargeback chart today ships a zero-RBAC ServiceAccount; the chart
change adding this ClusterRole + the per-Organization Secret Roles (plus the
`ADAPTER_ENABLED` / `BILLING_HOOK_*` env wiring and the image/appVersion
roll) is the follow-up chart release — until it lands, the adapter decides
itself off in-cluster because the API calls are forbidden, and logs why.

## Architecture pointers (one line each; the tables live in ADR-0014)

| Concern | Where it is decided |
|---|---|
| Composability + left-menu mapping (an application plugs into the console) | ADR-0014 D9/D9a + `Blueprint.spec.consoleUI` + Settings → Menu (PR #6724) |
| Entity types per deployment mode | ADR-0014 D2a |
| Billing source-of-truth per customer type (incl. dual-registered) | ADR-0014 D2b |
| Customer intake / import without duplication | ADR-0014 D8a |
| Collection modes (event-driven vs pulled change log vs sampled) | ADR-0014 D3a |
| Profiles `sovereign` / `operator-central` | ADR-0014 D10 |

## Lanes

| Lane | Content | Status |
|---|---|---|
| A (this directory) | Go service: domain + migrations, API, PIN auth, cloud collector (Huawei Cloud Stack Online / Kom4DC), rating, statements, image | shipped |
| B | React + TypeScript UI built into `ui/dist` and embedded by this binary | shipped |
| D | OpenOva adapter (`Customer ← Organization` sync, platform collector, billing hook) | shipped |

## Layout

```
products/chargeback/
├── cmd/chargeback/main.go        entry point: config → migrate → collector → HTTP
├── internal/
│   ├── adapter/openova/          OpenOva adapter (lane D): Organization → Customer sync,
│   │                             platform collector (pods/PVCs → k8s.* usage), billing hook (D6)
│   ├── api/                      /api/v1 handlers, session + PIN auth, authorization, UI serving
│   ├── collector/huawei/         SDK-HMAC-SHA256 signer, gateway client, ECS/EVS/EIP/ELB/NAT listers,
│   │                             CTS change-log poller, CES sampler, kind → SKU mapping
│   ├── config/                   environment → Config
│   ├── window/                   the shared hour-slice window math both collectors bill by
│   ├── crypto/                   envelope encryption (AES-256-GCM, per-secret DEK wrapped by APP_ENCRYPTION_KEY)
│   ├── mail/                     SMTP sender, or log sender when SMTP_HOST is unset
│   ├── metrics/                  dependency-free Prometheus text registry
│   ├── rating/                   price book CSV import, exact decimal math, statement run
│   ├── store/                    Postgres store: embedded migrations, scoped queries
│   └── testdb/                   integration-test database helper
├── ui/embed.go + ui/dist/        embedded front-end (placeholder until lane B lands)
├── Containerfile                 multi-stage, distroless static, uid 65532, read-only rootfs
└── Makefile                      build / vet / test / integration / image / dev-db
```

## Domain

`customers` · `customer_users` · `cost_sources` · `credentials` (secret
envelope-encrypted, never returned by the API, never logged) ·
`resource_inventory` · `usage_records` (append-only facts, idempotent on
`(source, resource, sku, window_start)`) · `price_books` · `price_items` ·
`statements` · `rated_lines` · `invites` · `audit_log` · `sessions` · `pins`.
Migrations are Go-embedded and applied at startup (`store.Migrate`), tracked in
`schema_migrations`.

Compared with the build spec, `price_items` carries one extra nullable column,
`annual_price`, so that changing a price book's `annual_divisor` recomputes the
derived unit prices instead of silently leaving them stale.

## SKUs (Huawei, allocation-based, hourly)

| SKU | Unit | Quantity per hour |
|---|---|---|
| `ecs.<flavor_name>` | instance-hour | 1 (labels: status, flavor, vcpus, ram_mb) |
| `evs.ssd.gb` / `evs.hdd.gb` | gb-hour | volume size (SSD/GP/ESSD → ssd; SAS/SATA → hdd) |
| `eip` | hour | 1 |
| `eip.bandwidth_mbps` | mbps-hour | bandwidth size |
| `elb` | hour | 1 |
| `nat.<spec>` | hour | 1 |
| `ecs.cpu_util` | pct-hour-avg | hourly CES average (informational, never rated) |

`unit_price = annual_price / annual_divisor` (default 8760). Stopped ECS
instances are rated per `price_books.bill_stopped`: `compute` (billed like
running), `storage-only` (no instance charge while stopped), `none` (neither the
instance nor the volumes attached to it while stopped).

## Collector

Every `COLLECT_INTERVAL` (15m) per verified source: list ECS, EVS, EIP, ELB and
NAT (paginated, 15s per call), upsert `resource_inventory` (first_seen /
last_seen / deleted_at, status and flavor transitions in `attrs`), then recompute
usage for every touched UTC hour since the previous tick. Each record is one
contiguous single-status interval inside one hour, so a re-run over the same
hour updates the same row. The first pass backfills from the customer's
`start_date` (at most 31 days).

Every `CTS_POLL_INTERVAL` (5m): read `cts` traces (`trace_type=system`) and
apply `create*` / `delete*` / `resize*` / `stop*` / `start*` operations as exact
boundaries and transitions of the affected resource, then recompute the hours
from the event; `raw_ref` carries the trace id.

Hourly: `ces` `cpu_util` per ECS → `ecs.cpu_util`.

Verification (activation): one signed `GET ecs /v1/{pid}/cloudservers/detail?limit=1`.
`2xx` ⇒ verified; `401`/`403` ⇒ failed with the gateway error code
(`APIGW.0301`, `EPS.0003`, …) in `last_error`; `404 APIGW.0101` ⇒ failed, API not
published.

**Per-source isolation.** Every tick walks the collectable sources one by one,
each step recover-guarded: one source's failure (gateway error, store error,
even a panic) marks only that source — `last_error` set, per-source exponential
backoff (≤ 6h) — and the loop continues with the next source. The tick result is
`ok` when every source succeeded and `partial` otherwise
(`chargeback_collect_ticks_total{step,result}`); per-source outcomes are counted
in `chargeback_collect_sources_total{step,result="ok|error|skipped"}`. A
credential that cannot be decrypted with the current `APP_ENCRYPTION_KEY` flips
its source to `failed` immediately and is **not retried every tick** — the
source rejoins collection only after a new credential is entered via
`POST /sources/{id}/credential`.

## API (`/api/v1`, JSON, cookie `cb_session` HttpOnly SameSite=Lax)

Roles: `operator` (email ∈ `OPERATOR_EMAILS`), `customer-admin`, `customer-viewer`
(from `customer_users`). Operators see everything; customer principals see only
their own customer — every query is filtered by the session's scope, other
customers' ids answer `404`, writes need `customer-admin`.

| Area | Endpoints |
|---|---|
| Auth | `POST /auth/pin/request` · `POST /auth/pin/verify` · `POST /auth/logout` · `GET /auth/me` |
| Customers | `GET/POST /customers` · `POST /customers/import` (multipart CSV, raw CSV or JSON array) · `GET/PATCH /customers/{id}` · `POST /customers/{id}/invite` · `GET/POST /customers/{id}/users` · `DELETE /customers/{id}/users/{email}` · `GET /customers/{id}/audit` |
| Invites (public by token) | `GET /invites/{token}` · `POST /invites/{token}/activate` |
| Sources | `GET/POST /customers/{id}/sources` · `POST /sources/{id}/credential` (rotate + verify) · `POST /sources/{id}/verify` · `DELETE /sources/{id}` |
| Usage | `GET /customers/{id}/usage?from&to&group_by=sku\|resource\|day` · `GET /customers/{id}/inventory` |
| Price books | `GET/POST /pricebooks` · `GET /pricebooks/template.csv` · `GET/PUT /pricebooks/{id}` · `PUT /pricebooks/{id}/items` · `POST /pricebooks/{id}/import` |
| Statements | `POST /statements/run {period, customer_id?}` · `GET /statements[?period]` · `GET /customers/{id}/statements` · `GET /statements/{id}` · `GET /statements/{id}.csv` · `POST /statements/{id}/issue` |
| Operator | `GET /overview` |
| Ops (root) | `GET /healthz` · `GET /readyz` · `GET /metrics` |

Money and quantities are emitted as exact JSON numbers taken from Postgres
`numeric` columns; the service never does money math in floating point.

A customer created by the operator starts `pending`; the first successful
source verification outside the invite flow (`POST /sources/{id}/verify` or a
credential entry that verifies) activates it, with an audit entry recording
`activated via source verification` — otherwise its verified sources would
never be collected. The customer detail and the source verify/rotate responses
carry `collecting: true|false` (customer active ∧ source verified — the exact
gate the collector applies), so the UI can say why nothing flows.

Customer import CSV columns: `slug,name,admin_email,region,project_ids(;-separated),price_book,billing_mode,start_date`.
Price book CSV columns: `sku,unit,annual_price,description` (template at
`/api/v1/pricebooks/template.csv`; a `unit_price` column may be given instead of
`annual_price`).

## Configuration (environment)

| Variable | Default | Meaning |
|---|---|---|
| `DATABASE_URL` | `postgres://chargeback:chargeback@localhost:5432/chargeback?sslmode=disable` | Postgres DSN |
| `APP_ENCRYPTION_KEY` | required | base64 of 32 random bytes; wraps every stored secret (`make key`) |
| `OPERATOR_EMAILS` | empty | comma-separated operator sign-ins |
| `PUBLIC_URL` | `http://localhost:8080` | used in invite links and to mark the cookie Secure |
| `SMTP_HOST/PORT/USER/PASS/FROM` | unset | PIN + invite mail; unset ⇒ logged at info level (development mode) |
| `HUAWEI_ENDPOINT_TEMPLATE` | `https://%s.%s.kom4dc.nationalcloud.om` | `(service, region)` |
| `HUAWEI_INSECURE_TLS` | `true` | on-prem CA |
| `COLLECT_INTERVAL` / `CTS_POLL_INTERVAL` / `CES_INTERVAL` | `15m` / `5m` / `1h` | collector cadences |
| `COLLECTOR_ENABLED` | `true` | set `false` on API-only replicas |
| `PROFILE` | `sovereign` | `sovereign` or `operator-central`; surfaced in `/auth/me` and `/overview` |
| `ADAPTER_ENABLED` | auto | OpenOva adapter override: unset = on when `PROFILE=sovereign` AND in-cluster Kubernetes configuration exists; `true`/`false` force it |
| `BILLING_HOOK_URL` | unset | billing service base URL for the D6 statement hook; unset ⇒ hook off |
| `BILLING_HOOK_TOKEN` | unset | superadmin bearer token `POST /billing/metering/record` requires |
| `LISTEN_ADDR` | `:8080` | |

## Development

```
make test                        # go vet + unit tests (no database needed)
make dev-db                      # local Postgres in a container
make integration                 # unit + integration tests against CHARGEBACK_TEST_DATABASE_URL, one package at a time
make key                         # fresh APP_ENCRYPTION_KEY
APP_ENCRYPTION_KEY=... OPERATOR_EMAILS=you@example.com make run
make image                       # container image (build context = this directory)
```

Integration tests share one database and truncate it between tests, which is
why `make integration` passes `-p 1`.

## Operational notes

- The binary is static, runs as uid 65532 with a read-only root filesystem, and
  writes nothing to disk. State is the Postgres database (CNPG on a Sovereign).
- Rotating `APP_ENCRYPTION_KEY` requires re-wrapping the per-secret DEKs; until
  then sources whose credential cannot be opened report the failure on
  `POST /sources/{id}/verify` and are skipped by the collector.
- Multi-region: the service is stateless; run it against the DR-paired database
  and scale API replicas with `COLLECTOR_ENABLED=false` so only one collector
  runs per database.
- No NodePort anywhere: the chart (follow-up) exposes the service through the
  gateway HTTPRoute like every other Blueprint.
