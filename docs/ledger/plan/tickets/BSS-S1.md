---
id: BSS-S1
ticket_rev: 1
plan_commit: 04d315fe72ce2e9b95bdb21cfd9bb9db97fa5d42
lane: SPIKE
type: spike
size: S
---

# BSS-S1 — Spike: suite sign-in, suite access to the engine API, and the price list a fresh Sovereign shows

## In plain words

**The problem today.** Three checks of the billing area assume facts that nobody had measured: that the acceptance suite can sign a new storefront customer in without a person, that it can call the billing engine's API with the sovereign scope, and which price list a fresh Sovereign shows before and after its first Organization. The packages that build those checks cannot be written on a guess.

**What changes.** The result record of the spike is in the repository, with the exact calls, the measured answers and what the later packages must take into account. The package is marked done.

**Done means:**

- The file docs/ledger/plan/spikes/BSS-S1.md exists and has an Evidence section.
- The result file states that the storefront sign-in calls work headless with a readable mailbox, and that a Sovereign has no mailbox the gate lane can read.
- The result file states that the single sign-on gate of the billing engine refuses the suite's session as a cookie and as a bearer token, and that the engine shows the owner the scope sovereign behind the gate.
- The result file gives the measured price lists before and after the first Organization: 4 plans, the smallest at 2.490 OMR a month, in both documents.
- No other file of the repository changes, apart from the done marker of the package.

The rest of this ticket is the exact instruction for the model that does the work.

RULES OF CONDUCT:

1. Do one work package at a time. Its ticket is the whole specification.
2. Before you change anything, run the precondition and every pre-flight command. If any output differs from the ticket, stop this package and file the wrong report.
3. Change only files under "Allowed paths". If the work needs any other file, stop this package and file the wrong report.
4. Never edit a chart `version:`, an `appVersion:`, a slot pin, a catalogue pin or an image tag line. CI writes them. If your ticket changes a `blueprint.yaml` or a slot file, run `node scripts/build-catalog.mjs` in `products/catalyst/bootstrap/ui` and commit the two files it writes; do not edit them by hand.
5. Never edit `docs/ledger/plan.yaml`, `docs/ledger/stability.yaml` or any file under `docs/adr/`, unless the ticket lists that exact file. In `docs/ledger/gates.yaml` change only the lines the ticket quotes.
6. Make no design decision. If the ticket does not say which of two things to do, stop this package and file the wrong report.
7. Do not rename, move, reformat or "clean up" anything the ticket does not name.
8. Do not delete or weaken a test, a guard or a check to make it pass. Do not add `|| true`, `continue-on-error`, a skip, or a wider allow-list unless the ticket names that exact line.
9. Do not add a feature flag that is off by default. Do not add a fallback value for data the ticket says exists. Do not add a field, a flag or a setting that nothing in your own change reads.
10. Evidence is a CI check or a gate record. Your own terminal output is not evidence. Never paste an output you did not get from the command.
11. A command that prints a result is not a pass. Read the exit code. Zero is pass; anything else is fail.
12. A check that is pending is not green. Never report a pull request as ready while a check has not finished. You never merge a pull request: the architect reviews and merges every one.
13. Never run a command against a live cluster or a cloud account unless the ticket's lane is GATE and the command is written in the ticket.
14. Never create a NodePort, in a chart or on a cluster, for any reason.
15. Never use `git push --force`, `git reset --hard` on a shared branch, `git add -A`, or `git commit --amend` on a pushed commit.
16. Commit with the identity given in the ticket. Write `Refs #N`, never `Closes #N`. End the pull-request body with `Work-Package: <ID>`. A wrong report is the one exception: its commit and its pull request carry no `Refs` line.
17. Put no secret, token, key or password in a commit, a pull request, an issue or a log.
18. Use the terms in `docs/GLOSSARY.md`. Do not use a banned term. In code, commits and documents cite only work-package identifiers, decision-record numbers and the tracking issue as `Refs #N`.
19. If a step fails, do that step again once. If it fails again, stop this package and file the wrong report. Do not try a different approach.
20. One session does one package: the one the architect named. When it is reported or stopped, give your report as your final message and stop. Never take another package by yourself, and do not ask anyone whether to continue.
21. Report only what you did and which checks passed. Do not write "should work", "expected to pass" or "verified" without naming the check run.
22. If you find a defect that is not in your ticket, do not fix it. Add one line to `docs/ledger/plan/found/<ID>.md` with the file, the line and what you saw.
23. Never open, search or quote any repository other than the platform repository.
24. Never wait with `sleep`. The finish step "checks" has the one command that waits. While your pull request waits for its checks, do no other work.
25. Do not open a pull request with the trailer `Work-Package: PLAN`. Only the architect does.

## 1. Supersede banner
This ticket is the complete specification. It supersedes every earlier comment, issue text and conversation. Any deviation fails the ticket.

## 2. End goal
The spike BSS-S1 has run. Its result record is committed at `docs/ledger/plan/spikes/BSS-S1.md`, the path the plan gives this package. A person who opens it reads the three questions, the experiment as it was run (one lab run in CI and read-only probes of the standing environment), the measured answers, the outcome of each part and what the packages BSS-060, BSS-070, BSS-101 and BSS-106 must take into account. The experiment itself (one lab branch and its run) is not part of this change. This ticket only commits its record.

## 3. The model (the law)
1. The result of a spike is the file `docs/ledger/plan/spikes/<ID>.md` with its done marker, merged by a pull request with `Work-Package: <ID>`.
2. The record of this package is the block `BSS-S1.md` of this ticket, byte for byte. The executor copies it. The executor does not write or change any sentence of it.
3. The record has a section `## Evidence`.
4. No other file changes: not the sign-in service, not the billing engine, not the gate's chart or its slot, not the acceptance suite, not a workflow. The lab changes stayed on their lab branch, which is deleted.

## 4. Measured current state
Commit 04d315fe72ce2e9b95bdb21cfd9bb9db97fa5d42, 2026-11-19. Every command was run from the repository root.
- The record is not there yet. `test -e docs/ledger/plan/spikes/BSS-S1.md; echo $?` prints `1`.
- The folder of spike records exists. `test -d docs/ledger/plan/spikes; echo $?` prints `0`.
- The record cites facts of the repository. Each was checked at this commit:
  - The cap on sign-in code requests: `grep -n -F 'magicIssueRateMax = 5' core/services/auth/handlers/handlers.go` prints `149:	magicIssueRateMax = 5`, and `grep -n -F 'magicRateWindow   = 1 * time.Minute' core/services/auth/handlers/handlers.go` prints `150:	magicRateWindow   = 1 * time.Minute`.
  - The owner is the engine's sovereign-admin: `grep -n -F 'operatorEmails: ${SOVEREIGN_OWNER_EMAIL:-}' clusters/_template/bootstrap-kit/13f-bp-chargeback.yaml` prints `219:      operatorEmails: ${SOVEREIGN_OWNER_EMAIL:-}`.
  - No gate of the slot accepts bearer tokens: `grep -c 'skipJwtBearerTokens' clusters/_template/bootstrap-kit/13c-bp-oidc-gate.yaml` prints `0`.
  - The engine's default offer prices the S package at 29.88 OMR a year (2.490 a month): `grep -c -F '"annual_price": "29.88"' products/chargeback/internal/offer/default-offer.json` prints `1`.
  - The suite reads no mailbox: `git grep -l -i -E 'imap|mailbox' -- tests/acceptance/sovereign | wc -l` prints `0`.
  - The engine image of the pinned chart: `grep -n '^appVersion: 0.1.67' products/chargeback/chart/Chart.yaml` prints `106:appVersion: 0.1.67`.
- The lab ran on origin/main 9ef70b131a of the same day. Its run of the workflow `chargeback console e2e` (dispatched on the branch `lab/BSS-S1-a`, commit f41f65fe9c) is run 44471687544, job 135382451603, green. The branch is deleted: `git ls-remote origin 'refs/heads/lab/BSS-S1*' | wc -l` prints `0`.
- The probes of the standing environment hw307 were `kubectl get` and `kubectl logs` with its region-a kubeconfig and anonymous HTTPS GET requests, 06:16 to 06:18 UTC. Nothing was created or changed there.
- The record holds none of the four words that `scripts/check-no-banned-words.sh` refuses (it exits 0 with the record in the tree), and no line that is only three backticks.
- The record is 133 lines; its SHA-256 is `c47d9f293b177286c641e0b2a2489b49397cf1bc9e47eac0b4312ac580928305`.

## 5. Exact target state
| Item | Before | After |
|---|---|---|
| `docs/ledger/plan/spikes/BSS-S1.md` | absent | the block `BSS-S1.md` of this ticket, 133 lines |
| `docs/ledger/plan/done/BSS-S1.yaml` | absent | the marker file (a finish step writes it) |

The record follows. Step 1 copies it.

<!-- BEGIN BSS-S1.md -->
````markdown
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
````
<!-- END BSS-S1.md -->

## 6. Code paths to read first
- `docs/ledger/plan/spikes/BSS-S2.md` lines 1 to 20: the shape of an earlier spike record of this area (title, date and outcome, question).
- `core/services/auth/handlers/handlers.go` lines 133 to 215: the sign-in code the record measures (time to live, cap per source address, the send).
- `products/chargeback/internal/api/server.go` lines 703 to 770: how the engine finds the caller (its own cookie, else the header the gate forwards).

## 7. History and must-preserve
- Every other record under `docs/ledger/plan/spikes/` stays as it is. Proof: the finish step "changed paths" lists only the record and the marker.
- The sign-in service, the billing engine, the gate chart `platform/oidc-gate/chart`, the slots `clusters/_template/bootstrap-kit/13c-bp-oidc-gate.yaml` and `13f-bp-chargeback.yaml`, the acceptance suite and the workflow `chargeback-e2e.yaml` stay as they are. The record lists what later packages change there. Proof: the finish step "changed paths".

## 8. Access runbook
None needed. This ticket touches the repository only.

## 9. Execution constraints
- Branch: `wp/BSS-S1`, cut from `origin/main`. One branch, one pull request.
- Session: the architect starts one session for one ticket. You do this ticket and no other. After your report you stop.
- Commit identity: every commit is made with `git -c user.name=hatiyildiz -c user.email=269457768+hatiyildiz@users.noreply.github.com commit`. The work is one commit. A further commit is made only to repair a red check or a failure of the fence run, or when the architect's review asks for a change.
- The commit message and the pull-request body cite the tracking issue as `Refs #<issue>`. Never write `Closes` or `Fixes`.
- The pull-request body ends with the line `Work-Package: BSS-S1`.
- Order: the architect starts this ticket only when these packages are merged: `FND-021`, `FND-022`, `FND-035`.
- The plan entry: the checks `work-package / scope`, `work-package / precondition` and `work-package / acceptance` judge by this package's entry in `docs/ledger/plan.yaml` on `main`. (They are the jobs `scope`, `precondition` and `acceptance` of the workflow `work-package`; a command that lists checks prints the job name alone.) The architect writes that entry from this ticket: its `touches` are the paths of field 14, its precondition is field 12, its acceptance commands are those of field 18, and its `ticket_rev` is 1. `work-package / precondition` runs the precondition on the merge base, that is the state before your change. `work-package / acceptance` runs the acceptance commands on your change merged into `main`, each with `bash -euo pipefail -c`. `work-package / scope` judges the changed paths, the budget, the order and the marker file.
- Checks: every check that runs on the head commit of the pull request must finish green. `python3 scripts/plan.py checks --pr <pr> --wait` reads them. Exit code 0: all finished green. Exit code 1: one finished red. Exit code 2: one has not finished. Exit code 4: one has not finished for 90 minutes. Exit code 3: the tool could not read the checks. Only code 0 is green.
- Merge: you do not merge. When every check has finished green you report on the pull request. The architect reviews every pull request, merges it and closes the tracking issue. If the architect asks for a change, the architect starts a session with this ticket again, and you make the change inside the allowed paths as a further commit.
- A file that this ticket embeds with a checksum is never edited by hand. If such a file is wrong, do its copy step again. If its checksum still differs, or a check fails on a file that matches its checksum, the ticket is wrong.
- A file that a tool of this ticket writes from data on `main` (a generator, a baseline command, a tool of the ticket) is never edited by hand either. When an acceptance command, the fence run or a check fails because of its content, run the tool once more if this ticket says that it may be run again. If the ticket does not say so, or the failure comes back, the ticket is wrong.
- Budget: at most 5 files added or modified and 800 changed lines (size S). A deleted file counts as one line and no file. Your own files under `docs/ledger/plan` are not counted.

## 10. Definition of done
- [ ] `docs/ledger/plan/spikes/BSS-S1.md` exists, is not empty and has the section `## Evidence` (A1).
- [ ] The record is the block of this ticket byte for byte (A2).

## 11. Explicitly excluded
- A mailbox for the gate lane, and the change that lets the suite's session through the gate of the billing engine: the result file names them; no package of the plan builds them yet, the architect adds them from this result.
- The onboarding journey of the suite and its sign-in client: package BSS-060.
- The suite's client of the engine API: package BSS-106.
- The default offer of the platform and its numbers: package BSS-101.
- The plan files `docs/ledger/plan.yaml` and `docs/ledger/plan/tickets/**`: only the architect changes them.

## 12. Precondition
**C1.** The record of this spike is not there yet.
```sh
test -e docs/ledger/plan/spikes/BSS-S1.md; echo $?
```
Expected output: `1`

## 13. Pre-flight checks
**P1.** The folder of spike records exists, so step 1 can write into it.
```sh
test -d docs/ledger/plan/spikes; echo $?
```
Expected output: `0`

## 14. Allowed paths
```
docs/ledger/plan/spikes/BSS-S1.md
```

Besides these paths, three files of your own are always allowed: `docs/ledger/plan/done/BSS-S1.yaml` (the marker), `docs/ledger/plan/wrong/BSS-S1-rev1.yaml` (a wrong report) and `docs/ledger/plan/found/BSS-S1.md` (defects you saw and did not fix). No other file may change.

## 15. Forbidden paths
- `docs/ledger/plan/spikes/` other than the file of field 14: records of other spikes, each owned by its own package.
- `core/services/auth/**`, `products/chargeback/**`, `platform/oidc-gate/**`, `clusters/_template/bootstrap-kit/**`, `tests/acceptance/sovereign/**`, `.github/workflows/chargeback-e2e.yaml`: the files the record talks about. The lab changed some of them on its lab branch only; later packages change them on main.
- `docs/ledger/plan.yaml`, `docs/ledger/plan/tickets/**`: only the architect changes them.

## 16. Steps
**How to read an exit code.** Every command line runs in a shell of its own, so an exit code is printed by the same line that produced it: a command of this ticket that ends in `; echo "exit code: $?"` prints it as its last line. Never read it with a second command.

**How long a command may run.** Give every command of this ticket a time limit of 10 minutes: the `timeout` of the Bash tool, 600000. Never wait with `sleep`: the session refuses a long `sleep`. The one step that waits is the finish step "checks", and its command waits by itself.

**Start.** Do these first, in order. If the architect's first message says that you are going on with this package after an interruption or after a review: run `git fetch origin --prune`, run `git switch wp/BSS-S1`, do the step "identity", and go on with the step that the message names.

- **Identity.** Run `gh api user --jq .login`. It must print `hatiyildiz` or `emrahbaysal`. If it prints another name, or fails, this session has no usable GitHub profile: change nothing, push nothing, give that output as your final message and stop.
- **Branch.** Run `git status --porcelain --untracked-files=all | wc -l`. It must print `0`: the checkout is clean. If it prints another number, change nothing, give the output of `git status --porcelain` as your final message and stop. Run `git fetch origin --prune`. Run `git switch -c wp/BSS-S1 origin/main`.
- **Claim.** First see that nobody holds this package: `git ls-remote --exit-code --heads origin wp/BSS-S1 > /dev/null; echo "exit code: $?"`. The last line must be `exit code: 2`: the branch is not on the remote. If it is `exit code: 0`, another session holds this package: change nothing, give that output as your final message and stop. Then run `git push -u origin wp/BSS-S1`. This push is the claim; it changes no file.
- **Claim check.** Run `python3 scripts/plan.py claim-check BSS-S1`. If it prints `keep`: go on. If it prints `yield`: run `git push origin --delete wp/BSS-S1`, give `yield` as your final message and stop. If the command fails because `scripts/plan.py` does not exist, the two bootstrap packages are not merged and this ticket must not start: run `git push origin --delete wp/BSS-S1`, report that, and stop.
- **Pre-flight.** Run the precondition of field 12. Then run every pre-flight command of field 13. An item that states "Expected output" is compared by what it prints, character for character; its exit code does not matter. An item that states "Expected exit code" is compared by its exit code only. If one differs, do not change anything: follow "If this ticket is wrong" at the end of this ticket.
- **Tracking issue.** The architect opened the tracking issue of this package when this ticket was written. Find it: `gh issue list --search 'BSS-S1 in:title' --state open`. Each line it prints holds the number of an issue, its state and its title. The line whose title starts with `BSS-S1 ` is the tracking issue, and its number is `<issue>` in the steps below. If two lines have such a title, take the lower number. If no line has such a title, open the issue yourself: `gh api -X POST repos/openova-io/openova/issues -f title='BSS-S1 Spike: suite sign-in, suite access to the engine API, and the price list a fresh Sovereign shows' -f body='Ticket: docs/ledger/plan/tickets/BSS-S1.md' --jq .number`; the number it prints is `<issue>`. Then mark the issue as started: `printf '{"labels":["status/in-progress"]}' | gh api -X POST repos/openova-io/openova/issues/<issue>/labels --input - --jq '.[].name'`. It prints the labels of the issue, one per line. One of them must be `status/in-progress`.

**Work.** The steps of this package, in order.

1. Create `docs/ledger/plan/spikes/BSS-S1.md` from the block `BSS-S1.md` of this ticket (it stands in field 5). Run:
```sh
awk '/^<!-- BEGIN BSS-S1.md -->$/{f=1;next} /^<!-- END BSS-S1.md -->$/{f=0} f' docs/ledger/plan/tickets/BSS-S1.md | sed '1d;$d' > docs/ledger/plan/spikes/BSS-S1.md
```
2. Run `sha256sum docs/ledger/plan/spikes/BSS-S1.md`. It must print `c47d9f293b177286c641e0b2a2489b49397cf1bc9e47eac0b4312ac580928305  docs/ledger/plan/spikes/BSS-S1.md`. If it differs, do step 1 once more. If it still differs, stop: that is stop condition 3, and in the wrong report `stage` is `step` and `ref` is `step 2`.

The work on files ends here. Go on with the finish steps. Where a finish step tells you to do the copy step of a file again, do steps 1 and 2.

**Finish.** Do these after the last work step, in order.

- **Acceptance run.** Run `python3 scripts/plan.py accept --id BSS-S1; echo "exit code: $?"`. It runs every acceptance command of field 18 the way the check `work-package / acceptance` runs them. The last line must be `exit code: 0`. If it is not, the output names the command that failed: correct your work inside the allowed paths and run it again. A file that this ticket embeds with a checksum is not corrected by hand: do its copy step again instead. A file that a tool of this ticket writes from data on `main` is not corrected by hand either: if this ticket says that the tool may be run again, run it once more; if it does not, follow "If this ticket is wrong" with stage `acceptance`. If a command still fails after one correction, follow "If this ticket is wrong" with stage `acceptance`. This run finds mistakes early. It is not evidence.
- **Marker.** Write the marker file with this one command: `mkdir -p docs/ledger/plan/done && printf 'schema: openova.io/plan-done/v1\nid: BSS-S1\nticket_rev: 1\n' > docs/ledger/plan/done/BSS-S1.yaml`. The file has three lines, each starting at the first column.
- **Changed paths.** Run `git status --porcelain --untracked-files=all | cut -c4-`. It prints one path per line: every file that is changed or new. Each path must be matched by a line of the code block of field 14, or be `docs/ledger/plan/done/BSS-S1.yaml` or `docs/ledger/plan/found/BSS-S1.md`. If one path is not, a file outside the allowed paths changed: follow "If this ticket is wrong".
- **Staging.** Stage every path that the step "changed paths" printed, by name: `git add -- <path> <path>`. Never use `git add -A` and never `git add .`.
- **Commit.** Make the one commit of the work: `git -c user.name=hatiyildiz -c user.email=269457768+hatiyildiz@users.noreply.github.com commit -m 'docs(ledger): result of the spike BSS-S1, storefront sign-in, engine API access and the price list of a fresh Sovereign (BSS-S1)' -m 'Refs #<issue>'`.
- **Body file.** Write the block of field 21 into the file `/tmp/BSS-S1-body.md`: the lines between its two fence lines, without the fence lines, with `<issue>` replaced by the number.
- **Fence run.** Run the scope fence yourself before you push: `python3 scripts/plan.py check-pr --base origin/main --head HEAD --body-file /tmp/BSS-S1-body.md; echo "exit code: $?"`. This is the judgement of the check `work-package / scope`. The last line must be `exit code: 0`. If it is not, the lines above it name each failure. A path outside field 14, a change over the budget or a package of the order line that is not merged: follow "If this ticket is wrong". A failure about the pull-request body: correct `/tmp/BSS-S1-body.md` and run this step again. A failure caused by the content of a file that a tool of this ticket writes from data on `main`: if this ticket says that the tool may be run again, run it once more, stage that file by name, commit with the same identity and the message `fix: repair work-package / scope (BSS-S1)`, and run this step again; if it does not, or if the same failure comes back, follow "If this ticket is wrong" with stage `scope`. Anything else that you can correct inside the allowed paths: correct it, stage the corrected paths by name, commit with the same identity and the message `fix: repair work-package / scope (BSS-S1)`, and run this step again.
- **Push and pull request.** Run `git push origin wp/BSS-S1`. Open the pull request: `gh pr create --base main --head wp/BSS-S1 --title 'docs(ledger): result of the spike BSS-S1, storefront sign-in, engine API access and the price list of a fresh Sovereign (BSS-S1)' --body-file /tmp/BSS-S1-body.md`. The command prints the address of the pull request. The number at its end is `<pr>`.
- **Checks.** Run `python3 scripts/plan.py checks --pr <pr> --wait; echo "exit code: $?"`. The command reads the checks of the head commit. While one has not finished it waits and reads again, for about 90 seconds. The number in the last line is the result. While it is 2, run the same command again. If it is 4, a check has not finished for 90 minutes: follow "If this ticket is wrong" with stage `check` and, as ref, the name of a check that the output marks `unfinished`. If it is 3, the tool could not read the checks: run the command once more, and if it is 3 again follow "If this ticket is wrong" with stage `check` and the ref `checks`.
- **Red check.** If the result is 1: `gh pr checks <pr>` lists the checks with a link each. In the link of the red check, the number after `/runs/` is its run and the number after `/job/` is its job. Read why it failed: `gh api repos/openova-io/openova/actions/jobs/<job>/logs | grep -B 40 -m 1 '##\[error\]' | cut -c30-300`. It prints the 40 lines before the first error line of the job. If it prints nothing, read the end of the log: `gh api repos/openova-io/openova/actions/jobs/<job>/logs | tail -40 | cut -c30-300`. To see whether a check is also red on `main`: `gh api "repos/openova-io/openova/commits/$(git rev-parse origin/main)/check-runs?per_page=100" --paginate --jq '.check_runs[] | select(.name=="<name of the check>") | .conclusion'`; if it prints `failure`, the reason is not in your change. One case comes before the others. If what the log command printed holds the text `DRIFT bp-catalyst-platform` or the text `umbrella-republish gate`, the copy of `main` that your pull request was tested against was taken between two commits of a chart release. The reason is not in your change, and running the check again cannot help, because that tests the same copy. Print the two versions that must agree on `main`: `git fetch -q origin && echo "$(git show origin/main:products/catalyst/chart/Chart.yaml | awk '/^version:/{print $2; exit}') $(git show origin/main:clusters/_template/bootstrap-kit/13-bp-catalyst-platform.yaml | awk '/^      version:/{print $2; exit}')"`. It prints two versions on one line. When they are equal, close and reopen the pull request, which makes GitHub test it against `main` as it is now: `gh api -X PATCH repos/openova-io/openova/pulls/<pr> -f state=closed --jq .state`, then `gh api -X PATCH repos/openova-io/openova/pulls/<pr> -f state=open --jq .state`, and go back to the step "checks". When they differ, or when the same check is red with one of the two texts after the reopening, change nothing and file no wrong report: say it on the pull request with `gh api -X POST repos/openova-io/openova/issues/<pr>/comments -f body='Stopped: a check is red because main is between two commits of a chart release. Nothing was changed. The architect settles this check.' --jq .html_url`, give as your final message the address of the pull request, the name of the red check and the sentence `The architect settles this check.`, and stop. For every other red check, four cases. The reason is not in your change (the check is red on `main` too, or the job failed before it reached your files): change nothing, run `gh run rerun <run>` once, and go back to the step "checks". Your change caused it and the file is one you wrote by hand: correct the mistake inside the allowed paths only, stage the corrected paths by name, make a further commit with the same identity and the message `fix: repair <name of the check> (BSS-S1)`, push, and go back to the step "checks". Your change caused it and the file is one this ticket embeds with a checksum: do not edit it; follow "If this ticket is wrong" with stage `check`. Your change caused it and the file is one that a tool of this ticket writes from data on `main`: do not edit it; if this ticket says that the tool may be run again, run it once more, stage that file by name, make a further commit with the same identity and the message `fix: repair <name of the check> (BSS-S1)`, push, and go back to the step "checks"; if it does not, follow "If this ticket is wrong" with stage `check`. In every case, if the same check is red a second time, follow "If this ticket is wrong" with stage `check`. The check `ci-gate`, where the pull request has one, is red whenever another check is red, so it has a rule of its own. When a check other than `ci-gate` is red, handle that check by the cases above and leave `ci-gate` alone in that round: do not read its log, do not run it again and do not count it as a red check. When `ci-gate` is the only red check, read its log with the log command above. If the log holds a line of the form `FAIL <name>: <conclusion>`, the check it names was red when `ci-gate` read it and has run again since: run `gh run rerun <run>` for `ci-gate` and go back to the step "checks"; the new run waits for the other checks by itself, and this is not counted as the same check being red a second time, up to three times. If the log holds no such line, the reason is not in your change: run `gh run rerun <run>` once for `ci-gate` and go back to the step "checks", and if that happens a second time follow "If this ticket is wrong" with stage `check` and the ref `ci-gate`. Never run `ci-gate` again right after a further commit or after closing and reopening the pull request: it starts again by itself then. In a wrong report with stage `check`, unless this ticket says otherwise: `ref` is the name of the red check, `command` is the log command above with the job number in it, `expected` is `the check finishes green`, and `actual` is what the log command printed.
- **Merge.** If the result is 0: first do the before-merge steps of field 9, if this ticket has any. Then say on the pull request that it is ready: `gh api -X POST repos/openova-io/openova/issues/<pr>/comments -f body='Ready for review. Every check finished green. Acceptance commands A1 to A2 were run by the check work-package / acceptance.' --jq .html_url`. You do not merge.
- **Report.** Give your report as your final message: the address of the pull request, the last output of the step "checks", and the result of each before-merge step of field 9 if this ticket has any. Then stop. The architect reviews the pull request, merges it, closes the tracking issue and starts the next ticket. Do not take another package, and do not ask whether to continue.

## 17. Stop conditions
1. The precondition of field 12 or a pre-flight output of field 13 differs from the expected value.
2. The work needs a file that field 14 does not allow.
3. A step cannot be done as it is written, or the ticket does not say which of two things to do.
4. An acceptance command fails after you have corrected the mistake once, or the same pull-request check is red twice.
5. The change is larger than the budget of field 9.

## 18. Acceptance checks
**A1.** The record exists, is not empty and has its Evidence section. It fails before the work, where the file does not exist.
```sh
test -s docs/ledger/plan/spikes/BSS-S1.md && grep -q '^## Evidence' docs/ledger/plan/spikes/BSS-S1.md
```

**A2.** The record is the block of this ticket, byte for byte. It fails before the work, where the file does not exist, and it fails when the file differs from the block.
```sh
sha256sum docs/ledger/plan/spikes/BSS-S1.md | cut -d' ' -f1 | grep -qx c47d9f293b177286c641e0b2a2489b49397cf1bc9e47eac0b4312ac580928305
```

## 19. Rollback
Revert the squash commit.

## 20. Locks, gate check, budget
Locks: none. Gate check flipped: none. Measured size: 1 file added, 133 lines. The marker file adds 3 lines and is not counted. The budget of the plan entry is 5 files and 800 changed lines.

## 21. Return format
The marker file `docs/ledger/plan/done/BSS-S1.yaml` in the same pull request, and a pull-request body in this form:

```
What changed:
- docs/ledger/plan/spikes/BSS-S1.md: new. The result record of the spike, 133 lines, copied from the ticket.
- docs/ledger/plan/done/BSS-S1.yaml: new. The marker file of this package.

Acceptance: commands A1 to A2 of the ticket, run by the check `work-package / acceptance`.

Refs #<issue>

Work-Package: BSS-S1
```

## If this ticket is wrong

A ticket is wrong when a stop condition of field 17 is met. Do not repair the ticket. Do not try another way. Nothing is merged, so nothing is reverted.

- Leave the branch `wp/BSS-S1` as it is. Do not delete it and do not push to it again. The architect deletes it when the ticket is amended.
- Run `git stash --include-untracked` if you have changes, then `git fetch origin` and `git switch -c wrong/BSS-S1-rev1 origin/main`.
- Run `mkdir -p docs/ledger/plan/wrong`. Create the file `docs/ledger/plan/wrong/BSS-S1-rev1.yaml` with these lines, each starting at the first column and each value filled with a fact:
  ```
  schema: openova.io/plan-wrong/v1
  id: BSS-S1
  ticket_rev: 1
  base_commit: <output of: git rev-parse origin/main>
  stage: <one of: precondition, preflight, step, acceptance, scope, budget, check>
  ref: <the identifier or the step name in this ticket: P3, step 4, A2, changed paths, fence run>
  command: |
    <the command you ran, as you typed it, indented by four spaces>
  expected: |
    <what the ticket says, indented by four spaces>
  actual: |
    <what you got, each line indented by four spaces; at most the last 40 lines>
  reported_at: <output of: date -u +%Y-%m-%dT%H:%M:%SZ>
  ```
- Run `git add -- docs/ledger/plan/wrong/BSS-S1-rev1.yaml`. Commit with the same identity and the message `wrong(BSS-S1): <stage> <ref>`. Run `git push -u origin wrong/BSS-S1-rev1`.
- Open the pull request: `gh pr create --base main --head wrong/BSS-S1-rev1 --title 'wrong(BSS-S1)' --body 'Work-Package: BSS-S1'`. The command prints its address; the number at the end is `<report>`. Do not merge it: the architect does.
- If you have done the step "tracking issue", comment on the issue: `gh api -X POST repos/openova-io/openova/issues/<issue>/comments -f body='Stopped: wrong report in pull request #<report>.' --jq .html_url`. Leave the issue open.
- Give the address of that pull request as your final message and stop. The architect amends this ticket.
