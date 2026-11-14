# UAT — final report (hw295)

**Generated** by `scripts/gen-uat-final-report.py`. Do not hand-edit — regenerate and diff to prove the numbers were not typed in.

> **Point-in-time hw295 snapshot (superseded).** Every number below is pinned to
> the hw295 walk; live = ✅246/286 on hw305 as of 2026-08-25 (PR #6690).
> Regenerate against the current env: `scripts/gen-uat-final-report.py --env hw305`.

Every acceptance case below carries three things: what the last walk **saw**, how much that observation should be **trusted** today, and the **evidence** it rests on. They are separate columns because they answer different questions — a ✅ at confidence 0.31 passed once, long ago, and is due for re-measurement.

## Headline

| measure | value |
|---|---|
| cases scored | 286 |
| confidence == 0.0 | **18** |
| last walk = FAIL | **19** |
| carrying cross-environment evidence | 245 |

## Cases at confidence 0.0 — the work

| case | epic | confidence | last | clause |
|---|---|---|---|---|
| `115` | apps | **0.0** | FAIL | The Guacamole connections list is NON-EMPTY for a signed-in sovereign-admin — `guacamole_connect |
| `16` | model | **0.0** | FAIL | A customer app page (`/app/<name>`) → Settings/Topology → change topology → Save persists; the c |
| `166` | cutover | **0.0** | FAIL | (After a COMPLETE cutover) the `cutover` group reads all-11-green on `/jobs` — every step Succee |
| `213` | mcp | **0.0** | FAIL | MCP cross-Org `get_application` is REFUSED as **not found** — `-32000`, and the message names on |
| `222` | e2e-journey | **0.0** | FAIL | The agent-created application **converges and appears in the user's Org** (chat-driven app creat |
| `227` | delivery | **0.0** | FAIL | A POST-cutover Sovereign REFUSES a GitHub-side catalog bump: the Blueprint CR does NOT move when |
| `228` | delivery | **0.0** | FAIL | A **re-prov AFTER a wipe** does NOT false-fail on orphaned `catalyst-*` VPCs — the orphan-VPC ja |
| `234` | apps | **0.0** | FAIL | On a funnel Org **that provisioned Stalwart mail** (cart includes `stalwart-mail`, or a mail-bea |
| `60` | topology | **0.0** | FAIL | Catalog New instance → pick `active-hot-standby` → Provision → that app's Topology tab shows a 2 |
| `87` | funnel | **0.0** | FAIL | After Launch, the marketplace redirects to the per-Org console — URL becomes the per-Org console |
| `90` | funnel | **0.0** | FAIL | Terminal acceptance: the purchased WordPress app SERVES at its own FQDN the app FQDN — the live  |
| `95` | funnel | **0.0** | FAIL | The 2nd Org's purchased app serves at its own different-TLD FQDN (the 2nd-Org app) — two Orgs, t |
| `G11` | cutover | **0.0** | FAIL | sovereignty cutover — the **11-step** chain runs to completion and the 10-min deny-egress hold a |
| `G7` | orgs | **0.0** | FAIL | vcluster dual-door walk — both Org doors land a vcluster-isolation Org. |
| `G8` | apps | **0.0** | FAIL | anthropic credential seeded into the agentic runtime — chat works end-to-end. |
| `G9` | apps | **0.0** | FAIL | agentic-run half — the agenity solo agent chats + drives create_application. |
| `R16` | funnel | **0.0** | FAIL | the funnel + BSS Org doors are collapsed onto ONE path — `console.<slug>` lands 200. |
| `R19` | agenity | **0.0** | FAIL | The per-Org **Agenity** workspace StatefulSet reaches Running with its Anthropic credential seed |

## Every case

| case | epic | verdict | confidence | band | box | walk every | last env |
|---|---|---|---|---|---|---|---|
| `1` | model | ✅ | 0.5802 | likely | 3 | 13 | hw294 |
| `2` | model | ✅ | 0.5802 | likely | 3 | 13 | hw294 |
| `3` | model | ✅ | 0.0480 | untrusted | 1 | 2 | hw294 |
| `4` | model | ✅ | 0.0517 | untrusted | 1 | 2 | hw294 |
| `5` | model | ✅ | 0.5802 | likely | 3 | 13 | hw294 |
| `6` | model | ✅ | 0.5802 | likely | 3 | 13 | hw294 |
| `7` | model | ✅ | 0.0233 | untrusted | 0 | 1 | hw294 |
| `8` | model | ✅ | 0.0358 | untrusted | 1 | 2 | hw294 |
| `9` | model | ✅ | 0.5802 | likely | 3 | 13 | hw294 |
| `10` | model | ✅ | 0.5802 | likely | 3 | 13 | hw294 |
| `11` | model | ✅ | 0.5802 | likely | 3 | 13 | hw294 |
| `12` | model | ✅ | 0.5802 | likely | 3 | 13 | hw294 |
| `13` | model | ✅ | 0.5802 | likely | 3 | 13 | hw294 |
| `14` | model | ✅ | 0.5802 | likely | 3 | 13 | hw294 |
| `15` | model | ✅ | 0.0222 | untrusted | 0 | 1 | hw294 |
| `16` | model | ❌ | 0.0000 | untrusted | 0 | 1 | hw295 |
| `17` | model | ✅ | 0.5802 | likely | 3 | 13 | hw294 |
| `18` | model | ✅ | 0.5802 | likely | 3 | 13 | hw294 |
| `19` | model | ⚠️ | 0.0213 | untrusted | 0 | 1 | hw295 |
| `20` | model | ✅ | 0.5802 | likely | 3 | 13 | hw294 |
| `21` | model | ✅ | 0.5802 | likely | 3 | 13 | hw294 |
| `22` | model | ✅ | 0.5802 | likely | 3 | 13 | hw294 |
| `23` | model | ✅ | 0.5802 | likely | 3 | 13 | hw294 |
| `24` | model | ✅ | 0.5802 | likely | 3 | 13 | hw294 |
| `25` | model | ⚠️ | 0.0222 | untrusted | 0 | 1 | hw295 |
| `26` | sso | ✅ | 0.8319 | trusted | 3 | 13 | hw295 |
| `27` | sso | ✅ | 0.8319 | trusted | 3 | 13 | hw295 |
| `28` | sso | ✅ | 0.8319 | trusted | 3 | 13 | hw295 |
| `29` | sso | ☐ | 0.0208 | untrusted | 0 | 1 | hw295 |
| `30` | sso | ✅ | 0.1996 | untrusted | 0 | 1 | hw295 |
| `31` | sso | ✅ | 0.2836 | weak | 0 | 1 | hw295 |
| `32` | sso | ✅ | 0.1440 | untrusted | 0 | 1 | hw295 |
| `33` | sso | ✅ | 0.1163 | untrusted | 0 | 1 | hw295 |
| `34` | sso | ✅ | 0.2836 | weak | 0 | 1 | hw295 |
| `35` | sso | ✅ | 0.1447 | untrusted | 0 | 1 | hw295 |
| `36` | sso | ✅ | 0.1440 | untrusted | 0 | 1 | hw295 |
| `37` | sso | ✅ | 0.1163 | untrusted | 0 | 1 | hw295 |
| `38` | sso | ✅ | 0.0967 | untrusted | 0 | 1 | hw295 |
| `39` | sso | ✅ | 0.2836 | weak | 0 | 1 | hw295 |
| `40` | sso | ✅ | 0.7555 | likely | 2 | 5 | hw295 |
| `41` | sso | ✅ | 0.1834 | untrusted | 0 | 1 | hw295 |
| `42` | sso | ✅ | 0.8319 | trusted | 3 | 13 | hw295 |
| `43` | sso | ✅ | 0.8319 | trusted | 3 | 13 | hw295 |
| `44` | sso | ✅ | 0.8319 | trusted | 3 | 13 | hw295 |
| `45` | sso | ✅ | 0.8319 | trusted | 3 | 13 | hw295 |
| `46` | topology | ✅ | 0.5802 | likely | 3 | 13 | hw294 |
| `47` | topology | ✅ | 0.5802 | likely | 3 | 13 | hw294 |
| `48` | topology | ✅ | 0.0614 | untrusted | 1 | 2 | hw294 |
| `49` | topology | ✅ | 0.5802 | likely | 3 | 13 | hw294 |
| `50` | topology | ✅ | 0.5751 | likely | 3 | 13 | hw294 |
| `51` | topology | ✅ | 0.1319 | untrusted | 0 | 1 | hw295 |
| `52` | topology | ✅ | 0.1319 | untrusted | 0 | 1 | hw295 |
| `53` | topology | ✅ | 0.5802 | likely | 3 | 13 | hw294 |
| `54` | topology | ✅ | 0.5802 | likely | 3 | 13 | hw294 |
| `55` | topology | ✅ | 0.0929 | untrusted | 0 | 1 | hw295 |
| `56` | topology | ✅ | 0.1397 | untrusted | 0 | 1 | hw295 |
| `57` | topology | ✅ | 0.0947 | untrusted | 0 | 1 | hw295 |
| `58` | topology | ✅ | 0.5803 | likely | 3 | 13 | hw294 |
| `59` | topology | ✅ | 0.0517 | untrusted | 1 | 2 | hw294 |
| `60` | topology | ❌ | 0.0000 | untrusted | 0 | 1 | hw295 |
| `61` | topology | ✅ | 0.5803 | likely | 3 | 13 | hw294 |
| `62` | topology | ✅ | 0.0929 | untrusted | 0 | 1 | hw295 |
| `63` | topology | ✅ | 0.0269 | untrusted | 1 | 2 | hw294 |
| `64` | topology | ✅ | 0.1283 | untrusted | 0 | 1 | hw295 |
| `65` | topology | ✅ | 0.1283 | untrusted | 0 | 1 | hw295 |
| `66` | topology | ✅ | 0.1283 | untrusted | 0 | 1 | hw295 |
| `67` | topology | ✅ | 0.0893 | untrusted | 0 | 1 | hw295 |
| `68` | topology | ✅ | 0.5803 | likely | 3 | 13 | hw294 |
| `69` | topology | ✅ | 0.0893 | untrusted | 0 | 1 | hw295 |
| `70` | topology | ✅ | 0.1283 | untrusted | 0 | 1 | hw295 |
| `71` | topology | ✅ | 0.0987 | untrusted | 0 | 1 | hw295 |
| `72` | funnel | ✅ | 0.5803 | likely | 3 | 13 | hw294 |
| `73` | funnel | ✅ | 0.5803 | likely | 3 | 13 | hw294 |
| `74` | funnel | ✅ | 0.5803 | likely | 3 | 13 | hw294 |
| `75` | funnel | ✅ | 0.1033 | untrusted | 1 | 2 | hw294 |
| `76` | funnel | ✅ | 0.5803 | likely | 3 | 13 | hw294 |
| `77` | funnel | ✅ | 0.5803 | likely | 3 | 13 | hw294 |
| `78` | funnel | ✅ | 0.5803 | likely | 3 | 13 | hw294 |
| `79` | funnel | ✅ | 0.5803 | likely | 3 | 13 | hw294 |
| `80` | funnel | ✅ | 0.5803 | likely | 3 | 13 | hw294 |
| `81` | funnel | ✅ | 0.5803 | likely | 3 | 13 | hw294 |
| `82` | funnel | ✅ | 0.5803 | likely | 3 | 13 | hw294 |
| `83` | funnel | ✅ | 0.5803 | likely | 3 | 13 | hw294 |
| `84` | funnel | ✅ | 0.0899 | untrusted | 1 | 2 | hw294 |
| `85` | funnel | ✅ | 0.0899 | untrusted | 1 | 2 | hw294 |
| `86` | funnel | ✅ | 0.0899 | untrusted | 1 | 2 | hw294 |
| `87` | funnel | ❌ | 0.0000 | untrusted | 0 | 1 | hw295 |
| `88` | funnel | ✅ | 0.0899 | untrusted | 1 | 2 | hw294 |
| `89` | funnel | ✅ | 0.5803 | likely | 3 | 13 | hw294 |
| `90` | funnel | ❌ | 0.0000 | untrusted | 0 | 1 | hw295 |
| `91` | funnel | ✅ | 0.0899 | untrusted | 1 | 2 | hw294 |
| `92` | funnel | ✅ | 0.0899 | untrusted | 1 | 2 | hw294 |
| `93` | funnel | ✅ | 0.5803 | likely | 3 | 13 | hw294 |
| `94` | funnel | ✅ | 0.0899 | untrusted | 1 | 2 | hw294 |
| `95` | funnel | ❌ | 0.0000 | untrusted | 0 | 1 | hw295 |
| `96` | placement | ✅ | 0.0899 | untrusted | 1 | 2 | hw294 |
| `97` | placement | ✅ | 0.5751 | likely | 3 | 13 | hw294 |
| `98` | placement | ✅ | 0.0844 | untrusted | 1 | 2 | hw294 |
| `99` | placement | ✅ | 0.5792 | likely | 3 | 13 | hw294 |
| `G1` | adoption | ✅ | 0.1319 | untrusted | 0 | 1 | hw295 |
| `G2` | apps | ⚠️ | 0.0196 | untrusted | 0 | 1 | hw295 |
| `G3` | dr | ✅ | 0.1357 | untrusted | 0 | 1 | hw295 |
| `G4` | adoption | ✅ | 0.7718 | likely | 2 | 5 | hw295 |
| `G5` | janitor | ✅ | 0.7714 | likely | 2 | 5 | hw295 |
| `G6` | model | ✅ | 0.0490 | untrusted | 3 | 13 | hw294 |
| `G7` | orgs | ❌ | 0.0000 | untrusted | 0 | 1 | hw295 |
| `G8` | apps | ❌ | 0.0000 | untrusted | 0 | 1 | hw295 |
| `G9` | apps | ❌ | 0.0000 | untrusted | 0 | 1 | hw295 |
| `M1` | janitor | ✅ | 0.7718 | likely | 2 | 5 | hw295 |
| `M2` | apps | ✅ | 0.5792 | likely | 3 | 13 | hw294 |
| `M3` | network | ✅ | 0.5792 | likely | 3 | 13 | hw294 |
| `M4` | apps | ✅ | 0.5785 | likely | 3 | 13 | hw294 |
| `R1` | janitor | ✅ | 0.7714 | likely | 2 | 5 | hw295 |
| `R2` | network | ✅ | 0.7718 | likely | 2 | 5 | hw295 |
| `R3` | sso | ✅ | 0.5792 | likely | 3 | 13 | hw294 |
| `R4` | sso | ✅ | 0.5792 | likely | 3 | 13 | hw294 |
| `R5` | sso | ✅ | 0.5804 | likely | 3 | 13 | hw294 |
| `R6` | postgres | ✅ | 0.8038 | trusted | 3 | 13 | hw295 |
| `R7` | plane-isolation | ✅ | 0.5804 | likely | 3 | 13 | hw294 |
| `R8` | gitea | ✅ | 0.5792 | likely | 3 | 13 | hw294 |
| `R9` | sso | ✅ | 0.5804 | likely | 3 | 13 | hw294 |
| `W1` | wizard | ⚠️ | 0.0217 | untrusted | 0 | 1 | hw295 |
| `W2` | wizard | ⚠️ | 0.0217 | untrusted | 0 | 1 | hw295 |
| `W3` | wizard | ✅ | 0.7404 | likely | 2 | 5 | hw295 |
| `W4` | wizard | ✅ | 0.5804 | likely | 3 | 13 | hw294 |
| `W5` | wizard | ⚠️ | 0.0217 | untrusted | 0 | 1 | hw294 |
| `100` | placement | ✅ | 0.0409 | untrusted | 1 | 2 | hw294 |
| `101` | placement | ✅ | 0.3221 | weak | 0 | 1 | hw295 |
| `102` | placement | ✅ | 0.0844 | untrusted | 1 | 2 | hw294 |
| `103` | placement | ✅ | 0.0844 | untrusted | 1 | 2 | hw294 |
| `104` | placement | ✅ | 0.5751 | likely | 3 | 13 | hw294 |
| `105` | placement | ✅ | 0.5751 | likely | 3 | 13 | hw294 |
| `106` | placement | ✅ | 0.0844 | untrusted | 1 | 2 | hw294 |
| `107` | placement | ✅ | 0.1615 | untrusted | 3 | 13 | hw294 |
| `108` | placement | ✅ | 0.5751 | likely | 3 | 13 | hw294 |
| `109` | sso | ⚠️ | 0.0400 | untrusted | 0 | 1 | hw295 |
| `110` | sso | ✅ | 0.8342 | trusted | 3 | 13 | hw295 |
| `111` | sso | ✅ | 0.1397 | untrusted | 0 | 1 | hw295 |
| `112` | sso | ✅ | 0.8342 | trusted | 3 | 13 | hw295 |
| `113` | sso | ✅ | 0.8342 | trusted | 3 | 13 | hw295 |
| `114` | sso | ✅ | 0.8342 | trusted | 3 | 13 | hw295 |
| `115` | apps | ❌ | 0.0000 | untrusted | 0 | 1 | hw295 |
| `116` | orgs | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `117` | orgs | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `118` | orgs | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `119` | orgs | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `120` | orgs | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `121` | orgs | ✅ | 0.0563 | untrusted | 1 | 2 | hw294 |
| `122` | orgs | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `123` | catalog | ✅ | 0.0588 | untrusted | 1 | 2 | hw294 |
| `124` | catalog | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `125` | catalog | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `126` | catalog | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `127` | catalog | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `128` | catalog | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `129` | catalog | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `130` | catalog | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `131` | catalog | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `132` | catalog | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `133` | catalog | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `134` | catalog | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `135` | catalog | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `136` | catalog | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `137` | catalog | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `138` | catalog | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `139` | catalog | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `140` | catalog | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `141` | catalog | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `142` | catalog | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `143` | catalog | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `144` | catalog | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `145` | catalog | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `146` | catalog | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `147` | catalog | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `148` | catalog | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `149` | catalog | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `150` | catalog | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `151` | catalog | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `152` | catalog | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `153` | catalog | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `154` | catalog | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `155` | catalog | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `156` | catalog | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `157` | catalog | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `158` | catalog | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `159` | cutover | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `160` | cutover | ✅ | 0.0701 | untrusted | 3 | 13 | hw294 |
| `161` | cutover | ✅ | 0.5798 | likely | 3 | 13 | hw294 |
| `162` | cutover | ✅ | 0.1534 | untrusted | 0 | 1 | hw295 |
| `163` | cutover | ✅ | 0.0701 | untrusted | 3 | 13 | hw294 |
| `164` | cutover | ✅ | 0.0344 | untrusted | 1 | 2 | hw294 |
| `165` | cutover | ✅ | 0.3631 | weak | 0 | 1 | hw295 |
| `166` | cutover | ❌ | 0.0000 | untrusted | 0 | 1 | hw295 |
| `167` | jobs | ✅ | 0.5800 | likely | 3 | 13 | hw294 |
| `168` | jobs | ✅ | 0.5800 | likely | 3 | 13 | hw294 |
| `169` | jobs | ✅ | 0.5800 | likely | 3 | 13 | hw294 |
| `170` | jobs | ✅ | 0.5800 | likely | 3 | 13 | hw294 |
| `171` | jobs | ✅ | 0.5800 | likely | 3 | 13 | hw294 |
| `172` | jobs | ✅ | 0.1485 | untrusted | 0 | 1 | hw295 |
| `173` | jobs | ✅ | 0.5800 | likely | 3 | 13 | hw294 |
| `174` | jobs | ✅ | 0.5425 | likely | 3 | 13 | hw294 |
| `175` | jobs | ✅ | 0.5800 | likely | 3 | 13 | hw294 |
| `176` | jobs | ⚠️ | 0.0208 | untrusted | 0 | 1 | hw295 |
| `177` | jobs | ✅ | 0.0517 | untrusted | 1 | 2 | hw294 |
| `178` | meta | ✅ | 0.0588 | untrusted | 1 | 2 | hw294 |
| `179` | meta | ✅ | 0.5800 | likely | 3 | 13 | hw294 |
| `180` | meta | ✅ | 0.5800 | likely | 3 | 13 | hw294 |
| `181` | meta | ✅ | 0.5800 | likely | 3 | 13 | hw294 |
| `182` | meta | ✅ | 0.5800 | likely | 3 | 13 | hw294 |
| `183` | meta | ✅ | 0.0562 | untrusted | 1 | 2 | hw294 |
| `184` | meta | ✅ | 0.1925 | untrusted | 0 | 1 | hw295 |
| `185` | meta | ✅ | 0.8040 | trusted | 3 | 13 | hw295 |
| `186` | mcp | ✅ | 0.5800 | likely | 3 | 13 | hw294 |
| `187` | topology | ✅ | 0.1485 | untrusted | 0 | 1 | hw295 |
| `188` | topology | ✅ | 0.0929 | untrusted | 0 | 1 | hw295 |
| `189` | topology | ✅ | 0.0795 | untrusted | 0 | 1 | hw295 |
| `190` | jobs | ✅ | 0.5800 | likely | 3 | 13 | hw294 |
| `191` | jobs | ✅ | 0.5800 | likely | 3 | 13 | hw294 |
| `192` | recon | ✅ | 0.0614 | untrusted | 1 | 2 | hw294 |
| `193` | recon | ✅ | 0.5800 | likely | 3 | 13 | hw294 |
| `194` | recon | ✅ | 0.5800 | likely | 3 | 13 | hw294 |
| `195` | recon | ✅ | 0.0614 | untrusted | 1 | 2 | hw294 |
| `196` | recon | ✅ | 0.5751 | likely | 3 | 13 | hw294 |
| `197` | recon | ✅ | 0.1436 | untrusted | 0 | 1 | hw295 |
| `198` | cloud | ✅ | 0.5795 | likely | 3 | 13 | hw294 |
| `199` | cloud | ✅ | 0.5795 | likely | 3 | 13 | hw294 |
| `200` | cloud | ✅ | 0.5795 | likely | 3 | 13 | hw294 |
| `201` | cloud | ✅ | 0.5795 | likely | 3 | 13 | hw294 |
| `202` | cloud | ✅ | 0.5795 | likely | 3 | 13 | hw294 |
| `203` | cloud | ✅ | 0.5795 | likely | 3 | 13 | hw294 |
| `204` | cloud | ✅ | 0.5795 | likely | 3 | 13 | hw294 |
| `205` | fleet | ✅ | 0.5795 | likely | 3 | 13 | hw294 |
| `206` | adoption | ✅ | 0.1534 | untrusted | 0 | 1 | hw295 |
| `207` | adoption | ✅ | 0.1534 | untrusted | 0 | 1 | hw295 |
| `208` | adoption | ✅ | 0.1534 | untrusted | 0 | 1 | hw295 |
| `209` | storage | ✅ | 0.8039 | trusted | 3 | 13 | hw295 |
| `210` | storage | ✅ | 0.8039 | trusted | 3 | 13 | hw295 |
| `211` | mcp | ✅ | 0.2151 | weak | 3 | 13 | hw294 |
| `212` | mcp | ✅ | 0.0330 | untrusted | 1 | 2 | hw294 |
| `213` | mcp | ❌ | 0.0000 | untrusted | 0 | 1 | hw295 |
| `214` | orgs | ✅ | 0.5795 | likely | 3 | 13 | hw294 |
| `215` | orgs | ✅ | 0.5795 | likely | 3 | 13 | hw294 |
| `216` | e2e-journey | ✅ | 0.0399 | untrusted | 1 | 2 | hw294 |
| `217` | e2e-journey | ✅ | 0.0399 | untrusted | 1 | 2 | hw294 |
| `218` | e2e-journey | ⚠️ | 0.0222 | untrusted | 0 | 1 | hw295 |
| `219` | e2e-journey | ⚠️ | 0.0238 | untrusted | 0 | 1 | hw295 |
| `220` | e2e-journey | ⚠️ | 0.0227 | untrusted | 0 | 1 | hw295 |
| `221` | e2e-journey | ⚠️ | 0.0217 | untrusted | 0 | 1 | hw295 |
| `222` | e2e-journey | ❌ | 0.0000 | untrusted | 0 | 1 | hw295 |
| `223` | e2e-journey | ⚠️ | 0.0213 | untrusted | 0 | 1 | hw295 |
| `224` | convergence | ✅ | 0.2151 | weak | 3 | 13 | hw294 |
| `225` | convergence | ✅ | 0.0496 | untrusted | 1 | 2 | hw294 |
| `226` | funnel | ✅ | 0.5795 | likely | 3 | 13 | hw294 |
| `227` | delivery | ❌ | 0.0000 | untrusted | 0 | 1 | hw295 |
| `228` | delivery | ❌ | 0.0000 | untrusted | 0 | 1 | hw295 |
| `229` | delivery | ✅ | 0.2512 | weak | 0 | 1 | hw295 |
| `230` | delivery | ✅ | 0.5795 | likely | 3 | 13 | hw294 |
| `231` | delivery | ✅ | 0.8039 | trusted | 3 | 13 | hw295 |
| `232` | apps | ✅ | 0.1585 | untrusted | 0 | 1 | hw295 |
| `233` | apps | ✅ | 0.0462 | untrusted | 1 | 2 | hw294 |
| `234` | apps | ❌ | 0.0000 | untrusted | 0 | 1 | hw295 |
| `235` | apps | ⚠️ | 0.0371 | untrusted | 0 | 1 | hw295 |
| `236` | apps | ✅ | 0.1440 | untrusted | 0 | 1 | hw295 |
| `237` | spine | ✅ | 0.0924 | untrusted | 0 | 1 | hw295 |
| `238` | postgres | ✅ | 0.0385 | untrusted | 1 | 2 | hw294 |
| `239` | adoption | ✅ | 0.1534 | untrusted | 0 | 1 | hw295 |
| `240` | gateway-4706 | ✅ | 0.8039 | trusted | 3 | 13 | hw295 |
| `241` | gateway-4706 | ⚠️ | 0.0217 | untrusted | 0 | 1 | hw295 |
| `242` | gateway-4706 | ✅ | 0.1333 | untrusted | 1 | 2 | hw294 |
| `243` | gateway-4706 | ✅ | 0.8039 | trusted | 3 | 13 | hw295 |
| `G10` | placement | ✅ | 0.1205 | untrusted | 3 | 13 | hw294 |
| `G11` | cutover | ❌ | 0.0000 | untrusted | 0 | 1 | hw295 |
| `G12` | dr | ✅ | 0.5350 | likely | 3 | 13 | hw294 |
| `R10` | orgs | ✅ | 0.5792 | likely | 3 | 13 | hw294 |
| `R11` | gitea | ✅ | 0.5785 | likely | 3 | 13 | hw294 |
| `R12` | postgres | ✅ | 0.0937 | untrusted | 0 | 1 | hw295 |
| `R13` | convergence | ✅ | 0.1319 | untrusted | 0 | 1 | hw295 |
| `R14` | model | ✅ | 0.5792 | likely | 3 | 13 | hw294 |
| `R15` | funnel | ✅ | 0.5792 | likely | 3 | 13 | hw294 |
| `R16` | funnel | ❌ | 0.0000 | untrusted | 0 | 1 | hw295 |
| `R17` | orgs | ✅ | 0.0302 | untrusted | 1 | 2 | hw294 |
| `R18` | cutover | ✅ | 0.5804 | likely | 3 | 13 | hw294 |
| `R19` | agenity | ❌ | 0.0000 | untrusted | 0 | 1 | hw295 |
| `R20` | delivery | ✅ | 0.8584 | trusted | 4 | 34 | hw295 |
| `R21` | catalog | ✅ | 0.1772 | untrusted | 3 | 13 | hw294 |
| `R22` | plane-isolation | ✅ | 0.2910 | weak | 0 | 1 | hw295 |

## How to reproduce

```
python3 scripts/uat-confidence.py --self-test
python3 scripts/uat-confidence.py --snapshot --env hw295
python3 scripts/gen-uat-final-report.py --env hw295
```

