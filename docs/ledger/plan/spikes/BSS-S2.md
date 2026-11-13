# BSS-S2 result: does the operator-hosted cloud export a FOCUS cost file

- Date: 2026-11-13, 07:04 to 07:12 UTC
- Cloud: the operator-hosted Huawei Cloud Stack that the provider module `infra/providers/huawei` is written for. One region. The account that a test environment was installed with.
- **Outcome: no**

## Question

Can a tenant of this cloud get its cost data from the cloud's cost centre as a FinOps FOCUS file? If yes, which columns does the file carry?

## What was run

Read-only probes only. No cloud resource was created. Nothing was changed.

1. **The cloud's API gateway.** Requests were signed with the account's access key, using the same signing method the provider module uses. The gateway gives one of two answers. `404 APIGW.0101` ("The API does not exist or has not been published in the environment") means the gateway has no route for that host name and path. Any other answer means the API is published there.
2. **The console portal.** Unauthenticated GET of `/<app>/` for many app names. A service app the portal hosts answers `200` with the sign-in redirect page. A name the portal does not know answers `302` to the console home.
3. **DNS.** Lookups of the global host names that carry billing in Huawei's public cloud, under the cloud's own domain.

Controls, all in the same run:

- A signed list call on the compute API: `200`. So the key and the signature work.
- The same signature on a made-up path of the compute host: `404 APIGW.0101`.
- A made-up host name: `404 APIGW.0101`.
- The compute path sent to a made-up host and to the `bss` host: `404 APIGW.0101` both times. So the gateway routes by host name and path together. A published API answers only on its own host name.

## Measured values

### API gateway: 64 signed calls, all `404 APIGW.0101`

- Host names: `<name>.<region>.<cloud domain>` for 14 names: `bss`, `bss-intl`, `costcenter`, `costmanagement`, `cost`, `billing`, `cbc`, `account`, `usercenter`, `mo`, `sc`, `metering`, `meter`, `finops`. Every name under the region resolves to the gateway through a wildcard record, so these calls reached the gateway.
- 15 call shapes of Huawei's public Cost Center and Billing Center API:
  - v2: monthly summary, balances, resource fee records, fee records, monthly breakdown, resource records query, cost analysis, service types, customer orders, pay-per-use resources query, on-demand price query
  - v4: cost analysis (`/v4/costs/cost-analysed-bills/query`)
  - v1.0: balances, monthly summary, resource fee records
- Calls were sent both with and without the project header. Every call answered `404 APIGW.0101`. No call answered `401` or `403`. So no cost or billing API is published to a tenant. This is not a lack of rights.

### Console portal: no cost centre or billing app

- 22 service apps exist and answer `200`: `ecm`, `vpc`, `obs`, `iam`, `rds`, `dcs`, `dms`, `cbr`, `ces`, `cts`, `lts`, `aom`, `smn`, `tms`, `eps`, `dns`, `waf`, `hss`, `cfw`, `sfs`, `mrs`, `dew`.
- 43 names a cost or billing app could have all answer `302` to the console home, the same as a made-up name. Among them: `costcenter`, `costmanagement`, `usercenter`, `billing`, `billingcenter`, `bss`, `cbc`, `metering`, `invoice`, `expense`, `finops`, `account`.
- The sign-in host answers `404` for `/usercenter/`, `/costmanagement/`, `/costcenter/` and `/bss/`, the same as a made-up path.

### DNS

- The console host and the sign-in host resolve.
- `account`, `usercenter`, `costcenter`, `bss` and `billing` under the cloud's domain got no answer within 8 s, the same as a made-up name.
- In the public cloud, `account.` serves the Billing Center (`/usercenter/`) and the Cost Center (`/costmanagement/`).

### Columns

None were observed, because the cloud offers no export to a tenant.

For reference only, not measured on this cloud: Huawei's public cloud documents a Cost Center export task with the content type "Cost details (FOCUS 1.0)". The task is created in the console only; no API to create it is documented. It runs daily into an OBS bucket, as csv or Parquet. Its field table lists 47 columns:

- 43 FOCUS 1.0 columns.
- 4 extension columns: `x_CostUnit`, `x_EnterpriseId`, `x_EnterpriseName`, `x_SplitItem`.
- 9 columns are documented as always empty: `ChargeClass`, `ContractedUnitPrice`, `InvoiceIssuerName`, `ListUnitPrice`, `PricingCategory`, `PricingQuantity`, `PricingUnit`, `PublisherName`, `SkuPriceId`.

## Outcome

`no`. The operator-hosted cloud publishes no cost centre to its tenants: no API on the gateway and no app in the console. So there is no FOCUS file to export and no columns to inspect.

## What follows for the plan

- The engine's README does not name bill-file import as the recommended cost input on this cloud. The inventory collector stays the only cost input there, as today. It reads the compute, storage, network, monitoring and trace APIs, which are published.
- No code depends on this answer. BSS-202, the FOCUS file collector, is built as planned for other sources. Its gate check BSS-G11 uses a fixture file, not a file from this cloud.
- Cost on this cloud is still the platform's own metering multiplied by prices the operator enters in the price book. There is no cloud-side bill to reconcile against by API.
- A bill file from this cloud could only come from the cloud operator's own billing system, outside the tenant API. That is a question for the operator's account team: can its billing system deliver a FOCUS file per tenant account? If it can, the file collector imports it by upload. This was not measured.

## Not measured

- The console after sign-in. Apps were judged from the portal's routing before sign-in. A menu link to a host outside the portal would not show up this way.
- The operator's own operations and billing portals. A tenant key cannot reach them.
- The public cloud's FOCUS export. The values above come from its help pages, not from a run.

## Evidence

Outcome: **no**. All values above come from signed and unsigned HTTPS requests and DNS lookups made by hand on 13 November 2026 between 07:04 and 07:12 UTC, from the agents' host in the cloud's network. There is no CI run. No cloud resource was created. The temporary working folder was deleted at the end.

## What later tasks must take into account

Do not plan a cloud-bill import or cost reconciliation for this cloud. Its tenant API gateway publishes no billing or cost API, with error `404 APIGW.0101`, and its console has no billing or cost app.

The probe takes about a minute. Repeat it if the operator announces a cost centre. Once a cost API is published, `APIGW.0101` changes to `401` or `200`.

If Huawei's public cloud ever becomes a substrate, its documented FOCUS 1.0 export fits BSS-202:

- It is created in the console only.
- It runs daily into OBS.
- It has 47 columns, including 4 `x_` extension columns. 9 columns are always empty, including `PricingQuantity` and `PricingUnit`.
- So the collector must take its quantity from `ConsumedQuantity`.

## Evidence

Outcome: **no**. The measurements were made on the reference cloud on 13 November 2026.

- measured by signed calls from the platform's own project; there is no CI run

Resources the test created, and their end state:

- nothing was created

## What later tasks must take into account

Outcome no: the operator-hosted cloud publishes no cost centre to its tenants. 64 signed calls (15 Cost Center and Billing Center API shapes on 14 host names) all returned 404 APIGW.0101, the gateway's "not published" code. Signature controls passed: a compute list call returned 200. In the console, 43 cost and billing app names were all unknown, while 22 real service apps answered. So BSS-202 and BSS-G11 stay as planned and use fixtures. The engine README must not recommend bill-file import for this cloud; the inventory collector stays its only cost input. A FOCUS file from this cloud could only come from the operator's own billing system, outside the API. That is an open question for the operator's account team. Not measured: the console after sign-in, and the operator's own back office. For reference only, from Huawei's public-cloud help pages and not measured on this cloud: the public cloud's console-only daily FOCUS 1.0 export has 47 columns, 9 of them always empty, including PricingQuantity and PricingUnit. Sources: https://support.huaweicloud.com/intl/en-us/usermanual-cost/costcenter_000002_03_07.html and https://support.huaweicloud.com/intl/en-us/usermanual-cost/costcenter_000002_03_04.html. No cloud resource was created. The working folder /home/openova/repos/tw-BSS-S2 was deleted (checked). Work took about 8 minutes.
