# BSS-S1 result: storefront sign-in, the engine API with the sovereign scope, and the price list of a fresh Sovereign

- Date: 2026-11-19, 06:16 to 06:27 UTC
- Where: a lab run in CI (fresh databases, the real binaries built from origin/main `9ef70b131a`), and read-only probes of the standing environment hw307 (region a)
- **Outcome:**
  - (a) storefront sign-in: **does not work as planned**. The sign-in calls work headless when the suite can read the mailbox the code is sent to. A Sovereign has no mail service of its own, and the gate lane holds no mailbox it can read.
  - (b) engine API with the sovereign scope: **does not work as planned**. The single sign-on gate in front of the engine does not accept the suite's session, as a cookie or as a bearer token. Behind the gate, the engine shows the scope `sovereign` for the owner's address.
  - (c) price list: **the same before and after the first Organization**. Both documents list 4 plans, the smallest at 2.490 OMR a month. The engine writes its default offer at its first start, so no Organization is needed for the engine's list.

## Question

Three facts that the checks of the billing area stand on:

- (a) Can the acceptance suite sign a new storefront customer in on a fresh Sovereign, with no person involved?
- (b) Can the suite call the engine's API (`https://chargeback.<sovereign-fqdn>/api/v1/...`) with the sovereign scope, using the sovereign-admin session it already holds?
- (c) Which price list does a fresh Sovereign show before its first Organization exists, and after that Organization has been synced? Measured for both documents: the engine's `GET /api/v1/public/packages` and the storefront catalog's `GET /api/catalog/plans`.

## What was run

### 1. Lab run in CI

One lab branch, `lab/BSS-S1-a` (commit `f41f65fe9c`), origin/main `9ef70b131a` plus one lab commit. It was never merged and has been deleted. The lab commit replaced the workflow `chargeback-e2e.yaml` with one job, added the runner script `scripts/lab-bss-s1.sh`, one Go test file that runs the adapter's own `SyncOrganization` against a given database, and a 30-line program that mints a session with the suite's own minter (`tests/acceptance/sovereign/internal/session`). The workflow ran by `workflow_dispatch`: run **44471687544**, job 135382451603, on a GitHub-hosted runner, green. Every measured value below is a line `M <key> <value>` of that job's log; the artifact `bss-s1-lab` holds the raw answers.

The job started service containers: Postgres 16, Valkey 8, MongoDB 7 (in place of the FerretDB that the catalog reads on a Sovereign), and Mailpit as the mail server. It built five binaries from the commit: the engine (`products/chargeback/cmd/chargeback`), the storefront's sign-in service (`core/services/auth`), the catalog (`core/services/catalog`), the storefront gateway (`core/services/gateway`) and the minter. Then:

1. **(c) before.** The engine started on an empty database with the settings the chart gives it on a Sovereign: `PROFILE=sovereign`, `DEFAULT_OFFER=true`, `TRUSTED_FORWARD_AUTH_HEADER=X-Forwarded-Email`, `OPERATOR_EMAILS=<owner>`; the adapter and the collector were off because the runner has no Kubernetes API. The catalog started on an empty database and the gateway in front of it. Each document was read once with `curl`, and the `customers` table was counted.
2. **(c) the first Organization.** The Go test called the adapter's `SyncOrganization` for one Organization (plan `s`, one owner) against the engine's database. That is the code the adapter runs when it sees an Organization. Both documents were read again 64 seconds later. Then the engine was restarted, and the engine's document was read a third time.
3. **(c) the other order.** On a second empty database, the Organization was synced first and the engine started after it. Its document was read.
4. **(b) the engine.** `GET /api/v1/me` was sent to the engine directly: with no header, with `X-Forwarded-Email` of the owner, with that of an address that has no role, and with the owner and `X-Forwarded-Groups: sovereign-admins`.
5. **(b) the gate.** The gate's image, `oauth2-proxy v7.6.0`, was started with the arguments of the live gate `oidc-gate-chargeback` (copied from hw307, see part 2), with the issuer endpoints pointed at an address that does not answer. A session was minted with the suite's minter for the issuer `https://console.<fqdn>` and the owner's address. `GET /api/v1/me` was sent through the gate with no credential, with the session as the cookie `catalyst_session`, with the session as `Authorization: Bearer`, and with a forged `X-Forwarded-Email`, each with `Accept: application/json` and some without. A second gate had in addition `--skip-jwt-bearer-tokens=true`, the chart's optional setting `skipJwtBearerTokens`.
6. **(a) sign-in.** Through the gateway, as the storefront calls it: `POST /api/auth/magic-link` for a new address, then the code was read from the mail server's API, `POST /api/auth/verify` with the address and the code, `GET /api/auth/me` with the returned token. Then seven more `magic-link` calls for other new addresses from the same source address.

### 2. Read-only probes of hw307

With the region-a kubeconfig file of hw307, only `kubectl get` and `kubectl logs`, and anonymous HTTPS `GET` requests. Nothing was created or changed. No sign-in was made and no code was requested.

- Every Deployment, StatefulSet, DaemonSet and HelmRelease, searched for a mail server.
- The environment of the Deployments `org-services/auth`, `chargeback/chargeback` and the arguments of `oidc-gate/oidc-gate-chargeback`, and the two keys `SMTP_HOST` and `SMTP_FROM` of the Secret `org-services-secrets`.
- The log line "default offer" of each engine pod.
- The Organizations (`organizations.orgs.openova.io`).
- `GET https://marketplace.hw307.omani.works/api/catalog/plans`, `GET https://chargeback.hw307.omani.works/api/v1/public/packages`, and `GET https://chargeback.hw307.omani.works/api/v1/me` with no credential and with a `catalyst_session` cookie that no key signs.

## Measured values

### (c) Price lists

The engine's document, every read of the lab run (`c.engine.*`):

| Read | Status | Packages | S / M / L / XL a month (OMR) | Smallest |
|---|---|---|---|---|
| Fresh database, no Organization | 200 | 4 | 2.490 / 4.490 / 7.990 / 13.990 | plan.s 2.490 |
| 64 s after the first Organization was synced | 200 | 4 | 2.490 / 4.490 / 7.990 / 13.990 | plan.s 2.490 |
| After a restart of the engine | 200 | 4 | 2.490 / 4.490 / 7.990 / 13.990 | plan.s 2.490 |
| Organization synced before the engine's first start | 200 | 4 | 2.490 / 4.490 / 7.990 / 13.990 | plan.s 2.490 |

- In every read the book is `OpenOva plans`, the currency OMR, 19 features. The shapes are the same in every read: 1, 2, 4 and 8 vCPU; 2, 4, 8 and 16 GB of memory; 25, 50, 100 and 250 GB of disk. `Cache-Control: public, max-age=60`.
- The engine's first start on the empty database logged `"msg":"default offer","state":"applied","wrote":true,"detail":"8 price item(s) added, 27 feature(s) created, 76 cell(s), 4 package setting(s), 31 icon(s) into OpenOva plans"`. The restart logged the same detail with `"wrote":false`, and the table `default_offer` holds `applied`. The other order (Organization first) logged the same detail.
- The sync of the first Organization left the plans book at 12 items and changed no price: `plan.s` keeps the hourly price 0.00341096, which is 2.490 × 12 ÷ 8,760. The Organization sync alone, on an empty database, makes a book of the 4 plan items at these same prices.

The catalog's document (`c.catalog.*`), before and after the sync: status 200, 4 plans, all of them generic (no product), `s` 2490, `m` 4490, `l` 7990, `xl` 13990 baisa a month, for 1, 2, 4 and 8 vCPU. Smallest: `s`, 2490 baisa, which is 2.490 OMR. `Cache-Control: public, max-age=300`.

hw307 (06:17 UTC): one Organization exists, `hw307-omani-works`, created 2026-11-15T17:05:10Z. The engine's document: 200, 4 packages, `plan.s` at 2.490 with 1 vCPU, 2 GB and 25 GB. The catalog's document: 200, 4 plans, `s` at 2490. Both engine pods (image `chargeback:0.1.67`) logged `"state":"applied"` with the same detail as the lab run. So the standing environment shows what the lab run shows.

### (b) The engine API

The engine directly (`b.engine.*`):

| Request to `GET /api/v1/me` | Status | Answer |
|---|---|---|
| no header | 401 | |
| `X-Forwarded-Email: <owner>` | 200 | `"role": "operator"`, `"scopes": ["sovereign"]` |
| `X-Forwarded-Email: <address with no role>` | 401 | |
| owner and `X-Forwarded-Groups: sovereign-admins` | 200 | `"scopes": ["sovereign"]` |

The owner has the scope `sovereign` because the chart puts the owner's address into `OPERATOR_EMAILS` (`clusters/_template/bootstrap-kit/13f-bp-chargeback.yaml`, `operatorEmails: ${SOVEREIGN_OWNER_EMAIL:-}`). hw307 runs the same: `OPERATOR_EMAILS` is the owner's address, `TRUSTED_FORWARD_AUTH_HEADER=X-Forwarded-Email`, `PROFILE=sovereign`, `ADAPTER_ENABLED=true`, `DEFAULT_OFFER=true`.

The gate (`b.gate.*`). The minted session carries `iss https://console.<fqdn>`, the owner's `email`, `email_verified: true`, `typ: session`, and no `aud`.

| Request through the gate | Status |
|---|---|
| no credential, `Accept: application/json` | 401 |
| no credential, a browser request | 302 to `https://auth.<fqdn>/realms/sovereign/protocol/openid-connect/auth?...&kc_idp_hint=catalyst-pin...` |
| the session as cookie `catalyst_session`, JSON / browser | 401 / the same 302 |
| the session as `Authorization: Bearer` | 401 |
| a forged `X-Forwarded-Email: <owner>` | 401, answered by the gate |
| `GET /api/v1/public/packages`, no credential | 200 (a skip-auth route) |
| second gate with `--skip-jwt-bearer-tokens=true`, the session as bearer | 401; the gate logged `unable to verify bearer token`: it checks a bearer against the keys of the realm's issuer `https://auth.<fqdn>/realms/sovereign` only |

The live gate on hw307 runs `oauth2-proxy:v7.6.0` with `--cookie-name=_oidc_gate_chargeback`, the five `--skip-auth-route` values above and no `--skip-jwt-bearer-tokens`, `--extra-jwt-issuers` or `--pass-authorization-header`. Its answers to the anonymous probes were the same as in the lab: 401 with `Accept: application/json`, 302 to the same sign-in address without it, and 401 with the unsigned `catalyst_session` cookie.

### (a) Storefront sign-in

Through the gateway, with a mail server the suite can read (`a.*`):

| Call | Status | Answer |
|---|---|---|
| `POST /api/auth/magic-link` `{"email": "<new address>"}` | 200 | `{"expires_in_sec": 600, "message": "check your email"}` |
| the mail | | subject "Your login code", one 6-digit code, in the mailbox within 1 s |
| `POST /api/auth/verify` `{"email": "<address>", "code": "<code>"}` | 200 | fields `refresh_token`, `token`, `user` |
| `GET /api/auth/me` with `Authorization: Bearer <token>` | 200 | the new address, role `member` |
| seven more `magic-link` calls from the same source address, other new addresses | 200, 200, 200, 200, 429, 429, 429 | |

So the sign-in service lets at most 5 code requests through per source address and minute (`magicIssueRateMax = 5`, `magicRateWindow = 1 * time.Minute` in `core/services/auth/handlers/handlers.go`), and a code lives 10 minutes.

On hw307: no Deployment, StatefulSet, DaemonSet or HelmRelease is a mail server. The sign-in service `org-services/auth` takes `SMTP_HOST`, `SMTP_PORT`, `SMTP_FROM`, `SMTP_USER` and `SMTP_PASS` from the Secret `org-services-secrets`, whose `SMTP_HOST` is `mail.openova.io` and `SMTP_FROM` is `noreply@openova.io`: the mail service of the parent, which the chart names as the default (`clusters/_template/bootstrap-kit/13-bp-catalyst-platform.yaml`, the comment on `sovereign.smtp.*`). The suite reads no mail: no file of `tests/acceptance/sovereign` speaks IMAP or reads a mailbox, and its only credentials are the kubeconfigs, `CATALYST_SESSION` and the key it mints a session with.

## Answer

- **(a) Does not work as planned.** The calls are known and work headless (table above). The code goes by mail to the address the customer typed, through the parent's mail service; the Sovereign has no mail service of its own, and the gate lane has no mailbox it can read. The smallest change that makes the sign-in testable with no back door: **a mailbox for the gate lane** on a mail domain that the parent's mail service delivers to, read over IMAP with a credential that only the gate lane holds; the suite signs in with one new address of that mailbox per run (a sub-address or a catch-all), and asks for at most 5 codes a minute from its source address. The sign-in service does not change.
- **(b) Does not work as planned.** The gate accepts only its own cookie `_oidc_gate_chargeback`, which it sets after a sign-in through the realm `sovereign`. It does not read `catalyst_session`, and it does not accept the session as a bearer token, even with `skipJwtBearerTokens` set. A forged `X-Forwarded-Email` does not get through. Behind the gate the engine shows the owner the scope `sovereign`, so the engine needs no change. What lets the suite through the gate is a package of its own; two candidates, neither measured: (1) the suite follows the browser's sign-in with a cookie jar (gate, the realm with `kc_idp_hint=catalyst-pin`, the console with its session, back to the gate's callback); (2) the gate accepts the console session as a bearer token from a second issuer (`--extra-jwt-issuers`), which needs an `aud` claim that the session does not carry today.
- **(c) The list does not change.** A fresh Sovereign of this release (chart `bp-chargeback` 0.1.68, image `chargeback:0.1.67`) shows the same price list before and after its first Organization: 4 plans, S 2.490, M 4.490, L 7.990, XL 13.990 OMR a month, in the engine's document and in the catalog's document. The engine writes its default offer (`products/chargeback/internal/offer/default-offer.json`) at its first start, and the Organization sync prices the plan items from that same offer (`products/chargeback/internal/store/planbook.go`). The list at 5 / 9 / 16 / 30 that the plan expected after the first sync does not exist in this release. The engine's document answers 200 from its first start, not 404.

## What later tasks must take into account

- **BSS-060** (gate check of self-service onboarding) cannot sign its customer in until a package gives the gate lane a mailbox (answer (a)). Its sign-in calls are those of the table in (a), against `https://marketplace.<sovereign-fqdn>/api`: `POST /auth/magic-link`, read the code, `POST /auth/verify`, then `Authorization: Bearer <token>`. It must stay within 5 code requests a minute per source address. The price document lists its packages from the engine's first start, so the price-list state it records is `published` before the journey begins.
- **BSS-070** (BSS-G01 required): a record with `price_list_state` other than `published` is a defect of the engine's start, not a missing Organization sync: the engine publishes before any Organization exists.
- **BSS-106** (the engine publishes the offer): its engine client cannot pass the gate with the suite's session until a package lets that session through (answer (b)). Behind the gate it reads `GET /api/v1/me` and gets `"scopes": ["sovereign"]` for the owner. The public routes `/api/v1/public/*` need no session.
- **BSS-101** (the default offer of the platform): the numbers the engine ships and serves today are the catalog's: S 2.490, M 4.490, L 7.990, XL 13.990 OMR a month for 1/2/4/8 vCPU, 2/4/8/16 GB, 25/50/100/250 GB. The catalog and the engine agree at the start, and both carry a disk size.

## Not measured

- The browser sign-in through the realm, driven headless (candidate 1 of answer (b)). It needs a sign-in on a running Sovereign. That creates a session in the realm, and the first time a brokered user, so it was not done on the standing environment.
- Delivery of a code through the parent's mail service to a mailbox, and whether that service takes sub-addresses. No code was requested on hw307.
- Region b of hw307, and the published images of the sign-in service and the catalog. The lab built them from origin/main. For the engine, the files that decide (c) and (b) have not changed since the commit that built `chargeback:0.1.67` (`2c6f654680`): `git log 2c6f654680..9ef70b131a` over `internal/offer`, `internal/store/defaultoffer.go`, `internal/store/planbook.go`, `internal/api/packages.go`, `internal/api/auth.go`, `internal/api/server.go`, `internal/adapter/openova/orgsync.go`, `internal/config/config.go` and `cmd/chargeback/main.go` lists no commit.
- The catalog's database on a Sovereign is FerretDB; the lab used MongoDB 7. The catalog fills an empty database with its seed at start (`core/services/catalog/main.go`, `SeedIfEmpty`), whichever of the two answers.

## Evidence

Outcome: **(a) does not work as planned, (b) does not work as planned, (c) the list does not change: 4 plans, smallest 2.490 OMR, before and after the first Organization.**

- CI: workflow `chargeback console e2e` (`.github/workflows/chargeback-e2e.yaml` of the lab branch), run 44471687544, job 135382451603, conclusion success, 2026-11-19 06:22 to 06:27 UTC, lab commit `f41f65fe9c` on origin/main `9ef70b131a`. The values above are the job log's lines `M c.engine.*`, `M c.catalog.*`, `M b.engine.*`, `M b.gate.*` and `M a.*`, and the test lines `LAB plans-book item ...`. The artifact `bss-s1-lab` of that run holds every answer body and log.
- hw307: `kubectl get` and `kubectl logs` with its region-a kubeconfig, and anonymous `curl` requests, 2026-11-19 06:16 to 06:18 UTC, from the agents' host. Nothing was created or changed.
- The lab branch `lab/BSS-S1-a` was deleted after the run.
