# North star dashboard

Status on 13 November 2026. The baseline is the platform as it stood on 12 November 2026, after nine months of work by its agents. All work is done by agents; effort is in agent-hours and tokens, from the tasks that were actually run.

## Right now

Measured from GitHub at 07:30 UTC on 13 November 2026. This block is rebuilt automatically every 30 minutes.

| | |
|---|---|
| Tasks merged to the main branch | **109 of 850** |
| Tasks with a written ticket | 104 |
| Being worked on by an agent now | CTL-030 ([#7361](https://github.com/openova-io/openova/pull/7361)), CTL-060 ([#7359](https://github.com/openova-io/openova/pull/7359)), CTL-110 ([#7357](https://github.com/openova-io/openova/pull/7357)), SEC-120 ([#7360](https://github.com/openova-io/openova/pull/7360)) |
| Lab-test results waiting to merge | none |
| Lab tests started, each on its own branch in CI | 26: ACC-020, ACC-030, AIP-010, AIP-020, CLD-050, CLD-055, CMP-S1, CMP-S2, CMP-S3, CMP-S4, CMP-S5, CMP-S6, CTL-050, CTL-055, INF-030, MIG-050, PRD-040, PRD-060, PRD-070, SEC-130, SEC-140, SEC-280, SOV-060, SVC-120, SVC-210, SVC-340 |
| Merged per day | 2026-11-12: 8, 2026-11-13: 101 |
| Proven on the live environment hw307 | **4**: BSS-035, NET-041, NET-070, NET-072. Each was seen working in a browser there and its ticket is closed. hw307 does not follow the main branch: a merged task reaches it by a separate release, and only then counts as proven. |
| Board with every task as a card | [github.com/orgs/openova-io/projects/2](https://github.com/orgs/openova-io/projects/2) |

### The last 25 merged tasks

| Merged (UTC) | Task | What it did | Pull request |
|---|---|---|---|
| 13 Nov 07:20 | [FND-002](plan/tickets/FND-002.md) | docs(ledger): result of the lab test FND-002 | [#7342](https://github.com/openova-io/openova/pull/7342) |
| 13 Nov 06:52 | [MIG-050](plan/tickets/MIG-050.md) | docs(ledger): result of the lab test MIG-050 | [#7329](https://github.com/openova-io/openova/pull/7329) |
| 13 Nov 06:47 | [SVC-340](plan/tickets/SVC-340.md) | docs(ledger): result of the lab test SVC-340 | [#7344](https://github.com/openova-io/openova/pull/7344) |
| 13 Nov 06:47 | [SEC-280](plan/tickets/SEC-280.md) | docs(ledger): result of the lab test SEC-280 | [#7343](https://github.com/openova-io/openova/pull/7343) |
| 13 Nov 06:35 | [SVC-210](plan/tickets/SVC-210.md) | docs(ledger): result of the lab test SVC-210 | [#7328](https://github.com/openova-io/openova/pull/7328) |
| 13 Nov 06:34 | [SEC-140](plan/tickets/SEC-140.md) | docs(ledger): result of the lab test SEC-140 | [#7331](https://github.com/openova-io/openova/pull/7331) |
| 13 Nov 06:34 | [SEC-130](plan/tickets/SEC-130.md) | docs(ledger): result of the lab test SEC-130 | [#7330](https://github.com/openova-io/openova/pull/7330) |
| 13 Nov 06:34 | [INF-030](plan/tickets/INF-030.md) | docs(ledger): result of the lab test INF-030 | [#7333](https://github.com/openova-io/openova/pull/7333) |
| 13 Nov 06:34 | [CLD-050](plan/tickets/CLD-050.md) | docs(ledger): result of the lab test CLD-050 | [#7332](https://github.com/openova-io/openova/pull/7332) |
| 13 Nov 06:34 | [CMP-S2](plan/tickets/CMP-S2.md) | docs(ledger): result of the lab test CMP-S2 | [#7334](https://github.com/openova-io/openova/pull/7334) |
| 13 Nov 06:33 | [CMP-S1](plan/tickets/CMP-S1.md) | docs(ledger): result of the lab test CMP-S1 | [#7311](https://github.com/openova-io/openova/pull/7311) |
| 13 Nov 06:33 | [CMP-S6](plan/tickets/CMP-S6.md) | docs(ledger): result of the lab test CMP-S6 | [#7312](https://github.com/openova-io/openova/pull/7312) |
| 13 Nov 06:33 | [CMP-S5](plan/tickets/CMP-S5.md) | docs(ledger): result of the lab test CMP-S5 | [#7326](https://github.com/openova-io/openova/pull/7326) |
| 13 Nov 06:33 | [SVC-120](plan/tickets/SVC-120.md) | docs(ledger): result of the lab test SVC-120 | [#7327](https://github.com/openova-io/openova/pull/7327) |
| 13 Nov 06:33 | [CTL-050](plan/tickets/CTL-050.md) | docs(ledger): result of the lab test CTL-050 | [#7313](https://github.com/openova-io/openova/pull/7313) |
| 13 Nov 06:33 | [CTL-055](plan/tickets/CTL-055.md) | docs(ledger): result of the lab test CTL-055 | [#7314](https://github.com/openova-io/openova/pull/7314) |
| 13 Nov 06:33 | [SOV-060](plan/tickets/SOV-060.md) | docs(ledger): result of the lab test SOV-060 | [#7335](https://github.com/openova-io/openova/pull/7335) |
| 13 Nov 06:06 | [CLD-161](plan/tickets/CLD-161.md) | feat(cloudprofile): check an installation document and print the ConfigMap that carries it | [#7336](https://github.com/openova-io/openova/pull/7336) |
| 13 Nov 06:06 | [FND-171](plan/tickets/FND-171.md) | chore(api): remove the DR runbook playback and preflight routes | [#7337](https://github.com/openova-io/openova/pull/7337) |
| 13 Nov 05:25 | [NET-100](plan/tickets/NET-100.md) | feat(providers): publish the network registry of a Sovereign and check it | [#7318](https://github.com/openova-io/openova/pull/7318) |
| 13 Nov 05:25 | [BSS-030](plan/tickets/BSS-030.md) | fix(billing): order capture never prices from a second list; gate check BSS-G02 | [#7320](https://github.com/openova-io/openova/pull/7320) |
| 13 Nov 05:25 | [NET-125](plan/tickets/NET-125.md) | refactor(api): read the ClusterMesh state, and a mode switch for the mesh step | [#7319](https://github.com/openova-io/openova/pull/7319) |
| 13 Nov 04:58 | [CLD-075](plan/tickets/CLD-075.md) | docs(generated): generate the tables of the provider packs and their cloud profiles | [#7309](https://github.com/openova-io/openova/pull/7309) |
| 13 Nov 04:58 | [NET-060](plan/tickets/NET-060.md) | test(acceptance): record the network census of every region as gate check NET-G04 | [#7308](https://github.com/openova-io/openova/pull/7308) |
| 13 Nov 04:25 | [FND-061](plan/tickets/FND-061.md) | ci: sign and attest the console and API images through one shared action | [#7304](https://github.com/openova-io/openova/pull/7304) |

### Merged changes a user can see

Of the 109 merged tasks, 54 are documents, decisions and checks that change no running product. The tasks below change a screen. The last column says whether the change has been seen working on hw307. The record of the walk, with screenshots, is [screenshots/north-star](screenshots/north-star/README.md).

| Task | What changes on the screen | Pull request | On hw307 |
|---|---|---|---|
| [FND-094](plan/tickets/FND-094.md) | Remove the Sandbox pages of the console | [#7181](https://github.com/openova-io/openova/pull/7181) | not proven yet |
| [FND-097](plan/tickets/FND-097.md) | Remove the unshipped console, the one job that drives it and the tests that only target it | [#7156](https://github.com/openova-io/openova/pull/7156) | not proven yet |
| [SOV-030](plan/tickets/SOV-030.md) | Wipe refuses a handed-over Sovereign without confirmation | [#7205](https://github.com/openova-io/openova/pull/7205) | not proven yet |
| [SOV-220](plan/tickets/SOV-220.md) | The console's step list comes from the status response | [#7303](https://github.com/openova-io/openova/pull/7303) | not proven yet |
| [NET-041](plan/tickets/NET-041.md) | Remove the firewall claims from the storefront and the console | [#7288](https://github.com/openova-io/openova/pull/7288) | **seen working, ticket closed** |
| [NET-070](plan/tickets/NET-070.md) | Wizard shows what the provisioner builds | [#7256](https://github.com/openova-io/openova/pull/7256) | **seen working, ticket closed** |
| [NET-072](plan/tickets/NET-072.md) | Placement selectors offer the host only | [#7245](https://github.com/openova-io/openova/pull/7245) | **seen working, ticket closed** |
| [SVC-030](plan/tickets/SVC-030.md) | The Catalog page honours visibility and refuses what has no chart | [#7273](https://github.com/openova-io/openova/pull/7273) | not proven yet |
| [BSS-035](plan/tickets/BSS-035.md) | The storefront says when prices are unavailable | [#7202](https://github.com/openova-io/openova/pull/7202) | **seen working, ticket closed** |

### Merged changes that show on the next fresh install

| Task | What changes | Pull request |
|---|---|---|
| [CLD-040](plan/tickets/CLD-040.md) | Run the shared render test in CI and pin a golden render | [#7127](https://github.com/openova-io/openova/pull/7127) |
| [CLD-070](plan/tickets/CLD-070.md) | Decision record and truthful contract documents | [#7129](https://github.com/openova-io/openova/pull/7129) |
| [CLD-075](plan/tickets/CLD-075.md) | Generator for the provider and profile tables | [#7309](https://github.com/openova-io/openova/pull/7309) |
| [CLD-080](plan/tickets/CLD-080.md) | Contract directory: module contract, profile schema, pack schema, conventions, two profiles | [#7297](https://github.com/openova-io/openova/pull/7297) |
| [SEC-020](plan/tickets/SEC-020.md) | Random k3s server token and separate agent token | [#7178](https://github.com/openova-io/openova/pull/7178) |
| [NET-010](plan/tickets/NET-010.md) | Close peer and stale ports on the reference provider | [#7195](https://github.com/openova-io/openova/pull/7195) |
| [NET-020](plan/tickets/NET-020.md) | Disable the k3s kube-proxy | [#7302](https://github.com/openova-io/openova/pull/7302) |
| [NET-031](plan/tickets/NET-031.md) | Stop reflecting the object-storage Secret to retired namespaces | [#7103](https://github.com/openova-io/openova/pull/7103) |
| [NET-100](plan/tickets/NET-100.md) | Network registry output and validator | [#7318](https://github.com/openova-io/openova/pull/7318) |
| [CMP-036](plan/tickets/CMP-036.md) | Kit slot 27a: values document, hand-over variable, definitions on upgrade | [#7277](https://github.com/openova-io/openova/pull/7277) |

A further 36 merged tasks change a running service without a screen of their own; they are proven by a test on the environment.



## What was built before the baseline

This is the ground the plan stands on. It is measured from the repository.

| | |
|---|---|
| Commits on the main branch since February 2026 | **25,978** |
| Pull requests merged | **4,464** |
| Issues closed | 1,929 |
| Blueprint charts | 100 |
| Go source files | 1,877, of which 1,032 are tests |
| Console source files | 1,139 |
| Browser test specs | 56 |
| CI workflows and guard scripts | 133 and 77 |

Against the target of this plan, that work stands at **24% built** across the 14 product areas, from 60% in billing to nothing yet in migration. The plan below is the remaining distance.

| | |
|---|---|
| Built before the baseline | **24%** of the target, by the code audit |
| Tasks of the plan delivered since | **109 of 850** |
| Epics in the backlog | 101, plus single fixes in 14 areas |
| Still to spend | about 555 agent-hours and 556 million tokens |
| Last feature done, six agents in parallel | about 3 Dec (312 running hours at 16 a day) |
| Releases | 64 updates of the environment and 13 fresh installs |

## Where we started and where we are, to scale

The length of a bar is the size of that area, so a long bar is a big area and a short bar a small one. Dark blue is what was already built on 12 November. Green is what has been merged since. Grey is still to do. Sizes are in agent-hours: the green and grey parts are the plan's own estimate; the dark part is scaled from the audit percentage, so it is an estimate of size, not a record of hours spent.

All 14 areas together: 24% was built before the baseline, and the target is now 32.9% built. 74 agent-hours of the plan are merged and 555 are still to do.

![Progress per product area, to scale](dashboard/progress.svg)

| Product area | Built before | Tasks merged | Agent-hours merged | Agent-hours to do | Share of all remaining work |
|---|---:|---:|---:|---:|---:|
| Network and zones | 42% | 13 of 77 | 8 | 48 | 9% |
| Billing | 60% | 5 of 42 | 3 | 30 | 5% |
| Multi-cloud | 29% | 9 of 77 | 6 | 49 | 9% |
| Delivery engine | 23% | 30 of 80 | 21 | 35 | 6% |
| Console and change through Git | 25% | 7 of 74 | 5 | 48 | 9% |
| Independent Sovereign | 25% | 6 of 68 | 4 | 46 | 8% |
| Security and secrets | 10% | 9 of 76 | 7 | 49 | 9% |
| Compliance | 28% | 11 of 58 | 7 | 36 | 6% |
| AI and agents | 22% | 1 of 59 | 1 | 44 | 8% |
| Machines and virtual machines | 17% | 3 of 61 | 2 | 44 | 8% |
| AI hub and open banking | 5% | 7 of 63 | 5 | 43 | 8% |
| Service catalogue | 35% | 6 of 43 | 4 | 28 | 5% |
| Privileged access | 17% | 1 of 38 | 1 | 30 | 5% |
| Migration | 0% | 1 of 34 | 0 | 24 | 4% |

## Every epic, to scale

Every piece of work in the plan, grouped by product area. A large tile is one epic; a small square is one single task that belongs to no epic. The area of a tile is its size in agent-hours, so the big tiles are the big pieces of work. Green is merged: a whole green square is a merged task, and the green part of an epic tile is the share of it already merged. An epic tile shows tasks merged of tasks in the epic, and its hours. The table of every epic is further down.

![Every epic as a tile sized by effort](dashboard/epics.svg)

## The product: where it started and where it is now

Score from 0 to 100 against the target. The red, orange or yellow part is where the area stood on 12 November, measured from the code by the audit: red is a critical gap, orange a serious one, yellow partly there. The green part is what has been added since. Each row reads start → now.

The number for now is the start score plus the share of the remaining distance that is merged, counted in agent-hours. It counts work merged to the main branch. A task counts as proven only when it runs on the live environment, and the block at the top of this page says how much of it does.

![Score per product area, at the start and now](dashboard/scores.svg)

| Product area (start → now) | At the start | Target | Epics | Tasks | Agent-hours to do | Tokens to do, M | Done by |
|---|---|---|---:|---:|---:|---:|---|
| **Independent Sovereign** (25 → 31) | Installs and runs in two regions, but still depends on its parent after hand-over. | Local from first boot, owns its keys, upgrades in place, can create a child Sovereign. | 9 | 68 | 46 | 46 | 27 Nov |
| **Multi-cloud** (29 → 37) | Runs on the National Cloud and one other cloud by special cases in shared code. | A cloud is a pack of files behind one contract. A second cloud and a hyperscaler proven. Managed Kubernetes as an option. | 13 | 77 | 49 | 49 | 1 Dec |
| **Security and secrets** (10 → 21) | The vault runs, but every application credential is still stored in etcd. | Credentials are mounted from the vault into the running workload. Urgent holes closed first. | 7 | 76 | 49 | 49 | 27 Nov |
| **Network and zones** (42 → 50) | The data plane is sound. The network is open by default. | Deny by default, mesh declared in Git, edge nodes and a load balancer per region. | 6 | 77 | 48 | 49 | 3 Dec |
| **Console and change through Git** (25 → 31) | Delivery goes through Git. Most console actions write straight to the cluster. | Every change is a commit. Screens update by push. | 1 | 74 | 48 | 47 | 25 Nov |
| **Service catalogue** (35 → 43) | The bootstrap kit is real. Users cannot choose a cloud's native service. | Every Blueprint has a class. Object storage and PostgreSQL offered as native or self-hosted. | 3 | 43 | 28 | 28 | 28 Nov |
| **Compliance** (28 → 40) | 24 policies and one proven score. | Posture hub, waivers, scanner reports, runtime and resilience views, frameworks. | 8 | 58 | 36 | 36 | 30 Nov |
| **Billing** (60 → 64) | The strongest area. Works on the live environment, structurally tangled. | One plan source, metered model usage, collectors per cloud, standalone option for a cloud operator. | 7 | 42 | 30 | 31 | 29 Nov |
| **AI and agents** (22 → 23) | Four model gateways, one agent workspace, no usable metering. | One gateway with Axon as simulator, an agent runtime, metering, service-management agents. | 7 | 59 | 44 | 44 | 1 Dec |
| **Machines and virtual machines** (17 → 20) | No virtual-machine service exists. | Virtual machines, machine operations over SSH, golden images. | 9 | 61 | 44 | 44 | 3 Dec |
| **Privileged access** (17 → 19) | Access control, approval and recording are not in place yet. | Controlled sessions with approval, recording and review. | 7 | 38 | 30 | 30 | 3 Dec |
| **Migration** (0 → 2) | Nothing exists. | A migration engine with landing on the National Cloud and network extension. | 4 | 34 | 24 | 24 | 2 Dec |
| **AI hub and open banking** (5 → 15) | Scaffolds only. | AI hub with knowledge search and chat. Open banking sandbox, payments, portal. | 7 | 63 | 43 | 44 | 28 Nov |
| **Delivery engine** (23 → 52) | The acceptance suite exists. Very little is proven on the live environment. | Every feature proven by automatic checks on a real environment, twice, before it counts. | 13 | 80 | 35 | 35 | 25 Nov |

## When each product area is worked on

![Roadmap per product area](dashboard/roadmap.svg)

Dates come from a simulation of six agents over the plan's dependencies, 16 running hours a day, the environment updated every 4 hours and one fresh install a day. They are estimates.

## The backlog: every epic

<details><summary><b>Independent Sovereign</b>: 9 epics, 68 tasks, 46 agent-hours, done by 27 Nov</summary>

| Feature (epic) | Tasks | Delivered | Agent-hours | Tokens, M | Starts | Done by |
|---|---:|---:|---:|---:|---|---|
| A Sovereign is local from its first boot | 10 | 1 | 6.6 | 6.6 | 14 Nov | 19 Nov |
| Hand-over in one short ordered step | 9 | 1 | 6.6 | 6.7 | 15 Nov | 22 Nov |
| Release channel and in-place upgrade | 4 | 0 | 3.2 | 3.2 | 19 Nov | 23 Nov |
| Sovereign: single fixes and lab tests | 11 | 4 | 4.0 | 3.8 | 13 Nov | 23 Nov |
| Ties to the parent cut at boot | 5 | 0 | 3.8 | 3.8 | 17 Nov | 24 Nov |
| Independence proven and frozen | 2 | 0 | 1.6 | 1.6 | 25 Nov | 25 Nov |
| Mothership mode: a Sovereign creates a child | 2 | 0 | 1.8 | 1.9 | 25 Nov | 26 Nov |
| Daily operation with the parent switched off | 9 | 0 | 6.4 | 6.4 | 13 Nov | 27 Nov |
| The Sovereign owns its own infrastructure state | 9 | 0 | 6.8 | 6.8 | 23 Nov | 27 Nov |
| Decommission, mothership rebuild, add a region | 7 | 0 | 5.5 | 5.6 | 25 Nov | 27 Nov |

</details>

<details><summary><b>Multi-cloud</b>: 13 epics, 77 tasks, 49 agent-hours, done by 1 Dec</summary>

| Feature (epic) | Tasks | Delivered | Agent-hours | Tokens, M | Starts | Done by |
|---|---:|---:|---:|---:|---|---|
| Dead infrastructure code removed | 3 | 0 | 2.2 | 2.2 | 13 Nov | 16 Nov |
| Code stops naming cloud providers | 4 | 0 | 2.9 | 2.9 | 15 Nov | 18 Nov |
| The console reads the cloud's profile | 4 | 0 | 2.8 | 2.7 | 17 Nov | 18 Nov |
| One boot template with no provider special cases | 3 | 0 | 1.8 | 1.8 | 17 Nov | 21 Nov |
| A local test cloud on every pull request | 4 | 0 | 3.2 | 3.2 | 13 Nov | 22 Nov |
| One overlay per cloud | 7 | 0 | 4.6 | 4.4 | 13 Nov | 22 Nov |
| The provisioning engine no longer names a cloud | 7 | 0 | 5.4 | 5.5 | 17 Nov | 22 Nov |
| The Huawei cloud controller runs inside the Sovereign | 2 | 0 | 1.6 | 1.6 | 23 Nov | 24 Nov |
| Installation settings leave the code | 5 | 1 | 2.8 | 2.7 | 22 Nov | 25 Nov |
| No cloud name left in shared code | 2 | 0 | 1.3 | 1.3 | 17 Nov | 25 Nov |
| Crossplane owns node pools and infrastructure | 7 | 0 | 5.2 | 5.2 | 23 Nov | 27 Nov |
| Second cloud proven (Hetzner, one region) | 4 | 0 | 3.2 | 3.2 | 22 Nov | 27 Nov |
| Cloud: single fixes and lab tests | 21 | 8 | 9.0 | 8.9 | 15 Nov | 28 Nov |
| A hyperscaler proven (AWS), by adding files only | 4 | 0 | 3.2 | 3.2 | 27 Nov | 1 Dec |

</details>

<details><summary><b>Security and secrets</b>: 7 epics, 76 tasks, 49 agent-hours, done by 27 Nov</summary>

| Feature (epic) | Tasks | Delivered | Agent-hours | Tokens, M | Starts | Done by |
|---|---:|---:|---:|---:|---|---|
| Vault upgraded, its seal out of etcd | 4 | 0 | 3.4 | 3.5 | 15 Nov | 22 Nov |
| Credentials injected into workloads, not stored in etcd | 5 | 0 | 4.6 | 4.8 | 22 Nov | 23 Nov |
| Injection rolled out: data and platform services | 6 | 0 | 3.8 | 3.7 | 23 Nov | 23 Nov |
| Third-party charts moved to injection | 5 | 0 | 3.3 | 3.2 | 23 Nov | 24 Nov |
| Injection rolled out: identity | 4 | 0 | 3.0 | 3.0 | 23 Nov | 24 Nov |
| Injection rolled out: remaining charts | 6 | 0 | 3.5 | 3.3 | 23 Nov | 24 Nov |
| Security: urgent fixes and hardening, one by one | 43 | 9 | 25.4 | 25.4 | 13 Nov | 27 Nov |
| External Secrets upgrade | 3 | 0 | 2.2 | 2.2 | 21 Nov | 27 Nov |

</details>

<details><summary><b>Network and zones</b>: 6 epics, 77 tasks, 48 agent-hours, done by 3 Dec</summary>

| Feature (epic) | Tasks | Delivered | Agent-hours | Tokens, M | Starts | Done by |
|---|---:|---:|---:|---:|---|---|
| A web application firewall in the path | 1 | 0 | 0.5 | 0.5 | 14 Nov | 14 Nov |
| Managed Kubernetes (CCE) as a cluster class | 9 | 0 | 7.5 | 7.7 | 13 Nov | 18 Nov |
| Placement without the old tiers | 7 | 0 | 5.0 | 5.0 | 14 Nov | 22 Nov |
| The mesh between regions declared in Git | 4 | 0 | 2.8 | 2.7 | 14 Nov | 25 Nov |
| Deny by default in every platform namespace | 7 | 0 | 5.9 | 6.1 | 15 Nov | 25 Nov |
| Network: port closures, clean-up and single fixes | 43 | 13 | 21.8 | 21.6 | 13 Nov | 27 Nov |
| Edge nodes and a load balancer per region | 6 | 0 | 5.0 | 5.1 | 27 Nov | 3 Dec |

</details>

<details><summary><b>Console and change through Git</b>: 1 epics, 74 tasks, 48 agent-hours, done by 25 Nov</summary>

| Feature (epic) | Tasks | Delivered | Agent-hours | Tokens, M | Starts | Done by |
|---|---:|---:|---:|---:|---|---|
| One pipeline for an Organization, every change through Git | 8 | 1 | 5.0 | 5.0 | 24 Nov | 25 Nov |
| Control plane: every screen writes through Git, one by one | 66 | 6 | 42.8 | 42.3 | 13 Nov | 25 Nov |

</details>

<details><summary><b>Service catalogue</b>: 3 epics, 43 tasks, 28 agent-hours, done by 28 Nov</summary>

| Feature (epic) | Tasks | Delivered | Agent-hours | Tokens, M | Starts | Done by |
|---|---:|---:|---:|---:|---|---|
| Function tests for the core services | 11 | 1 | 7.4 | 7.4 | 13 Nov | 15 Nov |
| Service model: classes on every Blueprint, single fixes | 23 | 5 | 12.6 | 12.4 | 13 Nov | 23 Nov |
| Choose native or self-hosted: object storage | 7 | 0 | 5.9 | 6.0 | 19 Nov | 26 Nov |
| Choose native or self-hosted: PostgreSQL | 2 | 0 | 1.8 | 1.9 | 27 Nov | 28 Nov |

</details>

<details><summary><b>Compliance</b>: 8 epics, 58 tasks, 36 agent-hours, done by 30 Nov</summary>

| Feature (epic) | Tasks | Delivered | Agent-hours | Tokens, M | Starts | Done by |
|---|---:|---:|---:|---:|---|---|
| Policies moved to the new model | 9 | 0 | 6.6 | 6.6 | 15 Nov | 17 Nov |
| Scanner reports | 4 | 0 | 3.0 | 3.0 | 16 Nov | 25 Nov |
| Resilience view | 3 | 0 | 2.5 | 2.5 | 16 Nov | 25 Nov |
| Compliance posture hub | 5 | 1 | 3.4 | 3.5 | 15 Nov | 25 Nov |
| Enforcement mode owned in Git | 5 | 0 | 3.4 | 3.4 | 14 Nov | 25 Nov |
| Waivers and remediation requests | 4 | 0 | 3.2 | 3.2 | 16 Nov | 25 Nov |
| Runtime security view | 2 | 0 | 1.4 | 1.4 | 25 Nov | 25 Nov |
| Compliance frameworks | 3 | 0 | 2.5 | 2.5 | 17 Nov | 25 Nov |
| Compliance: single fixes and screens | 23 | 10 | 9.6 | 9.6 | 13 Nov | 30 Nov |

</details>

<details><summary><b>Billing</b>: 7 epics, 42 tasks, 30 agent-hours, done by 29 Nov</summary>

| Feature (epic) | Tasks | Delivered | Agent-hours | Tokens, M | Starts | Done by |
|---|---:|---:|---:|---:|---|---|
| Billing: model usage metered | 3 | 0 | 2.2 | 2.2 | 26 Nov | 26 Nov |
| Billing: one source for plans and prices | 9 | 0 | 8.0 | 8.2 | 15 Nov | 26 Nov |
| Billing: cost collector plug-ins per cloud | 3 | 0 | 2.5 | 2.5 | 26 Nov | 26 Nov |
| Billing: decisions as events | 3 | 0 | 2.5 | 2.5 | 25 Nov | 27 Nov |
| Billing: one owner per money record | 5 | 0 | 4.3 | 4.5 | 15 Nov | 27 Nov |
| Billing: its secrets secured | 2 | 0 | 1.3 | 1.3 | 14 Nov | 27 Nov |
| Billing: standalone for a cloud operator | 3 | 0 | 2.5 | 2.5 | 27 Nov | 27 Nov |
| Billing: single fixes | 14 | 5 | 6.9 | 7.0 | 13 Nov | 29 Nov |

</details>

<details><summary><b>AI and agents</b>: 7 epics, 59 tasks, 44 agent-hours, done by 1 Dec</summary>

| Feature (epic) | Tasks | Delivered | Agent-hours | Tokens, M | Starts | Done by |
|---|---:|---:|---:|---:|---|---|
| Model usage meter | 3 | 0 | 2.2 | 2.2 | 14 Nov | 15 Nov |
| Model gateway with Axon as simulator | 7 | 0 | 5.2 | 5.2 | 14 Nov | 24 Nov |
| Agent runtime (kagent) | 3 | 0 | 2.2 | 2.2 | 14 Nov | 26 Nov |
| AI platform: single fixes and lab tests | 25 | 1 | 17.9 | 17.9 | 13 Nov | 27 Nov |
| Old gateways retired | 5 | 0 | 3.2 | 3.1 | 25 Nov | 27 Nov |
| Operations records for agents | 8 | 0 | 6.8 | 7.0 | 14 Nov | 27 Nov |
| Service-management agents | 5 | 0 | 4.1 | 4.2 | 27 Nov | 28 Nov |
| Self-hosted model serving, when GPUs exist | 3 | 0 | 2.2 | 2.2 | 28 Nov | 1 Dec |

</details>

<details><summary><b>Machines and virtual machines</b>: 9 epics, 61 tasks, 44 agent-hours, done by 3 Dec</summary>

| Feature (epic) | Tasks | Delivered | Agent-hours | Tokens, M | Starts | Done by |
|---|---:|---:|---:|---:|---|---|
| Machines: lab tests | 8 | 3 | 2.8 | 2.7 | 14 Nov | 16 Nov |
| Machine operations on the platform's own nodes | 11 | 0 | 8.0 | 8.0 | 14 Nov | 27 Nov |
| KubeVirt, if a cloud supports it | 3 | 0 | 2.5 | 2.5 | 15 Nov | 29 Nov |
| Virtual machines | 6 | 0 | 4.5 | 4.5 | 19 Nov | 29 Nov |
| Discovery and ownership of machines | 5 | 0 | 3.6 | 3.5 | 15 Nov | 29 Nov |
| Windows and hypervisor sources | 3 | 0 | 2.8 | 2.8 | 29 Nov | 1 Dec |
| Operations on machines over SSH | 13 | 0 | 11.0 | 11.2 | 25 Nov | 1 Dec |
| Light testing of machines | 5 | 0 | 3.4 | 3.4 | 15 Nov | 1 Dec |
| Native machine engine | 3 | 0 | 2.2 | 2.2 | 15 Nov | 1 Dec |
| Golden images and rotation | 4 | 0 | 3.2 | 3.2 | 29 Nov | 3 Dec |

</details>

<details><summary><b>Privileged access</b>: 7 epics, 38 tasks, 30 agent-hours, done by 3 Dec</summary>

| Feature (epic) | Tasks | Delivered | Agent-hours | Tokens, M | Starts | Done by |
|---|---:|---:|---:|---:|---|---|
| Privileged access: controlled sessions | 8 | 0 | 6.8 | 7.0 | 15 Nov | 28 Nov |
| Privileged access: recording | 2 | 0 | 1.8 | 1.9 | 28 Nov | 29 Nov |
| Privileged access: requests and approvals | 3 | 0 | 2.5 | 2.5 | 28 Nov | 29 Nov |
| Privileged access: SSH and databases | 3 | 0 | 2.5 | 2.5 | 29 Nov | 29 Nov |
| Privileged access: web and graphical workbenches | 3 | 0 | 2.2 | 2.2 | 29 Nov | 30 Nov |
| Privileged access: Windows and RDP | 2 | 0 | 1.6 | 1.6 | 29 Nov | 30 Nov |
| Privileged access: session review | 5 | 0 | 4.1 | 4.2 | 29 Nov | 30 Nov |
| Privileged access: urgent fixes and lab tests | 12 | 1 | 8.2 | 8.2 | 13 Nov | 3 Dec |

</details>

<details><summary><b>Migration</b>: 4 epics, 34 tasks, 24 agent-hours, done by 2 Dec</summary>

| Feature (epic) | Tasks | Delivered | Agent-hours | Tokens, M | Starts | Done by |
|---|---:|---:|---:|---:|---|---|
| Migration engine | 16 | 0 | 12.4 | 12.5 | 28 Nov | 1 Dec |
| Migration: landing on the National Cloud | 1 | 0 | 0.7 | 0.7 | 1 Dec | 1 Dec |
| Migration: screens | 4 | 0 | 3.2 | 3.2 | 1 Dec | 1 Dec |
| Migration: lab tests and single fixes | 8 | 1 | 4.6 | 4.5 | 13 Nov | 2 Dec |
| Migration: network extension | 5 | 0 | 3.6 | 3.5 | 29 Nov | 2 Dec |

</details>

<details><summary><b>AI hub and open banking</b>: 7 epics, 63 tasks, 43 agent-hours, done by 28 Nov</summary>

| Feature (epic) | Tasks | Delivered | Agent-hours | Tokens, M | Starts | Done by |
|---|---:|---:|---:|---:|---|---|
| Open banking: specification kit | 6 | 4 | 1.8 | 1.9 | 13 Nov | 13 Nov |
| Products: preparation | 10 | 3 | 5.4 | 5.5 | 13 Nov | 26 Nov |
| AI hub: knowledge search without GPUs | 12 | 0 | 9.2 | 9.2 | 13 Nov | 27 Nov |
| Open banking: sandbox, account information | 12 | 0 | 9.6 | 9.7 | 13 Nov | 27 Nov |
| Open banking: portal and onboarding | 3 | 0 | 2.2 | 2.2 | 27 Nov | 28 Nov |
| AI hub: connectors, tools, chat, speech | 8 | 0 | 5.9 | 5.9 | 15 Nov | 28 Nov |
| AI hub: GPU runtime and model choice | 5 | 0 | 3.4 | 3.3 | 26 Nov | 28 Nov |
| Open banking: payments, funds confirmation, events | 7 | 0 | 5.7 | 5.8 | 27 Nov | 28 Nov |

</details>

<details><summary><b>Delivery engine</b>: 13 epics, 80 tasks, 35 agent-hours, done by 25 Nov</summary>

| Feature (epic) | Tasks | Delivered | Agent-hours | Tokens, M | Starts | Done by |
|---|---:|---:|---:|---:|---|---|
| The plan as data and the tools that guard it | 3 | 3 | 0.0 | 0 | 13 Nov | 13 Nov |
| Release rails: CI owns versions and the merge queue | 6 | 1 | 3.2 | 3.0 | 14 Nov | 14 Nov |
| Tests run on every pull request | 7 | 5 | 1.2 | 1.1 | 14 Nov | 14 Nov |
| Dead code removed (foundation) | 11 | 7 | 2.3 | 2.2 | 14 Nov | 14 Nov |
| Signed releases | 2 | 1 | 0.9 | 0.9 | 14 Nov | 14 Nov |
| Documents match the code | 6 | 5 | 0.7 | 0.7 | 14 Nov | 14 Nov |
| The test gate: one list of automatic checks and their records | 7 | 4 | 2.0 | 2.0 | 14 Nov | 16 Nov |
| Backup and restore drills | 5 | 1 | 2.9 | 2.9 | 15 Nov | 16 Nov |
| Foundation: single fixes and lab tests | 14 | 3 | 7.4 | 7.3 | 13 Nov | 16 Nov |
| Console regression tests as code | 6 | 0 | 5.2 | 5.4 | 15 Nov | 16 Nov |
| One vulnerability scanner, not two | 3 | 0 | 2.2 | 2.2 | 15 Nov | 17 Nov |
| Observability pipeline | 5 | 0 | 4.1 | 4.2 | 15 Nov | 17 Nov |
| Releases as one automatic workflow | 3 | 0 | 2.0 | 2.0 | 15 Nov | 22 Nov |
| A Sovereign checks itself | 2 | 0 | 1.3 | 1.3 | 17 Nov | 25 Nov |

</details>

## Releases: which tickets ship in which batch

A product change ships in an **update** of the running environment (R). A change to first-boot files ships in a **fresh install** (F), which creates the next environment. The batch of a ticket follows from what it depends on. Environment names and dates are indicative: they move when an environment is replaced earlier or a provisioning fails.

How tickets are proven: 93 in a browser as an end user (Playwright with a screenshot), 470 by a test on the environment, 155 by a fresh install, 132 in CI only.

<details><summary><b>R-001</b> Update of hw307 · 27 tickets · about 13 Nov</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| ACC-020 | Spike: guacd 1.6.0 driven without the web application | test on the environment |
| ACC-030 | Spike: per-session credentials through the mount; object lock | test on the environment |
| AIP-010 | Spike: gateway proof (agentgateway, tests T1 to T8) | test on the environment |
| AIP-020 | Spike: simulation back end proof (agent kit turned inside out) | test on the environment |
| BSS-S2 | Spike: does the operator-hosted cloud export a FOCUS cost file | test on the environment |
| CLD-022 | Delete the infrastructure mutation dialogs from the console | browser, as an end user |
| CLD-055 | Spike: machine kind and reduced kit for the local pack | test on the environment |
| CMP-S3 | Spike: scanner report labels, publication date, benchmark report names, report volume | test on the environment |
| CMP-S4 | Spike: etcd load from report objects | test on the environment |
| CTL-030 | Delete the endpoint pipeline and the two duplicate registrations | browser, as an end user |
| CTL-110 | Commit library gitwrite | test on the environment |
| FND-001 | Spike: the merge queue under release-writer pushes | test on the environment |
| MIG-030 | Spike: a written volume boots on the reference cloud, through a unit Workspace | test on the environment |
| NET-290 | Spike: platform-owned Cilium on the National Cloud's managed Kubernetes | test on the environment |
| NET-300 | Spike: region separation on the reference cloud | test on the environment |
| PRD-040 | Spike: authorisation server fitness, consent carrier and conformance run | test on the environment |
| PRD-060 | Spike: vector search with row-level trimming on the platform's PostgreSQL | test on the environment |
| PRD-070 | Spike: chat front end forwards the user's token | test on the environment |
| PRD-080 | Spike: CPU images for embeddings, rerank, document conversion and transcription | test on the environment |
| PRD-103 | Ledger port and conformance kit | test on the environment |
| SEC-120 | etcd dump check and node hygiene check, as check files | test on the environment |
| SEC-135 | Spike: key-service seal on the reference cloud | test on the environment |
| SEC-271 | Spike: the policy engine verifies a signature with no public log | test on the environment |
| SOV-070 | Spike: a scoped cloud identity per Sovereign on the reference cloud | test on the environment |
| SOV-080 | Spike: whole-state move, encrypted object-store state, zero-change plan, offline restore | test on the environment |
| SOV-090 | Spike: authoritative DNS and outbound mail on the reference cloud | test on the environment |
| SOV-750 | Spike: an auto-scaling group worker pool on the reference cloud | test on the environment |

</details>

<details><summary><b>R-002</b> Update of hw307 · 14 tickets · about 13 Nov · waits for R-001</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| AIP-030 | Spike: agent runtime proof (kagent 1.x on k3s, Cilium, vcluster) | test on the environment |
| CLD-021 | Delete the 24 infrastructure mutation routes and the claim writer | test on the environment |
| CTL-112 | Region scopes of the commit library | test on the environment |
| CTL-160 | Package intent: validate, serialise, commit, wait | test on the environment |
| CTL-240 | Batch G: catalogue writers onto the library | test on the environment |
| MIG-040 | Spike: address preservation while a subnet is split | test on the environment |
| NET-270 | Spike: firewall attachment | test on the environment |
| PRD-102 | Generated types and server interfaces (oapi-codegen, strict server) | test on the environment |
| PRD-202 | cortex store: migrations, roles, trimmed query | test on the environment |
| PRD-502 | Sandbox ledger | test on the environment |
| PRD-503 | Keycloak consent extension | test on the environment |
| SVC-060 | Function-test convention and the data charts | test on the environment |
| SVC-125 | Blueprint proofs check: the function-test result in the gate record | test on the environment |
| SVC-315 | Spike: create a managed PostgreSQL instance through the API on the National Cloud | test on the environment |

</details>

<details><summary><b>R-003</b> Update of hw307 · 19 tickets · about 14 Nov · waits for R-001, R-002</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| AIP-040 | Switch off the header-trusting metering sidecar | test on the environment |
| AIP-101 | Shrink Axon and give it a test rail | test on the environment |
| AIP-201 | Node preparation for the sandbox class, exactly as the spike recorded | test on the environment |
| AIP-221 | Service skeleton, schema with migrations, outbox table, health | test on the environment |
| CLD-142 | Neutral storage gate | test on the environment |
| CTL-035 | Route registration leaves main() | test on the environment |
| CTL-040 | Flux manifests to stable API versions | test on the environment |
| CTL-150 | Intent lock in audit mode, with its report | test on the environment |
| CTL-300 | The running stream is the declared one | test on the environment |
| CTL-420 | Watches in the organization and environment controllers | test on the environment |
| CTL-700 | Pull-request mode of the commit library | test on the environment |
| FND-095 | Remove the Sandbox orchestrator, grants and cutover entries | test on the environment |
| NET-322 | Class file, schema and conformance probe as a suite profile | test on the environment |
| PRD-203 | cortex query process: token check, gateway client, search, definitions loader | test on the environment |
| PRD-501 | Resource server base: store, definitions loader, router with the token check, account-access consent resources | test on the environment |
| PRD-800 | Adapter template for a second bank | test on the environment |
| SVC-070 | Function tests: identity and secrets charts | test on the environment |
| SVC-080 | Function tests: GitOps, registry and certificate charts | test on the environment |
| SVC-090 | Function tests: network, names and edge charts | test on the environment |

</details>

<details><summary><b>R-004</b> Update of hw307 · 22 tickets · about 14 Nov · waits for R-001, R-002, R-003</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| AIP-102 | Tool calls passed through; no shared sessions | test on the environment |
| AIP-222 | Signal intake, grouping, candidate scoring, rule results | test on the environment |
| AIP-224 | Guard library agentguard and its adversarial corpus | test on the environment |
| CTL-038 | The API accepts a Job's service-account token, confined to one Organization | test on the environment |
| CTL-039 | Read routes for the region registry and the Environment list | test on the environment |
| CTL-320 | Durable audit | test on the environment |
| CTL-330 | Delete the Kafka client | test on the environment |
| CTL-340 | Jobs store subscribe; flow stream event-driven; jobs stream route | test on the environment |
| CTL-350 | Applications views from the informer cache | browser, as an end user |
| CTL-355 | Delete the second showback meter | browser, as an end user |
| INF-040 | Chart bp-kured with the node patch configuration | test on the environment |
| INF-110 | Kinds Machine and MachineSource; inventory controller with the node source | test on the environment |
| NET-030 | Retired names lose their policy privileges | test on the environment |
| NET-032 | Reserve platform namespace names as Organization slugs | test on the environment |
| NET-071 | Remove the zone virtual-cluster dialog, tab and stub kind | browser, as an end user |
| NET-080 | Pin the Organization virtual cluster chart | test on the environment |
| NET-133 | A local-affinity decision on every global Service | test on the environment |
| SEC-010 | Close the public route to OpenBao system paths | test on the environment |
| SEC-080 | External Secrets quick scope | test on the environment |
| SOV-035 | Delete the ProvisioningState kind and its unwired store | test on the environment |
| SVC-100 | Function tests: admission, infrastructure and control-plane charts | test on the environment |
| SVC-190 | Delete the Blueprint controller | test on the environment |

</details>

<details><summary><b>R-005</b> Update of hw307 · 13 tickets · about 14 Nov · waits for R-001, R-002, R-003, R-004</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| AIP-103 | Real streaming and real usage | test on the environment |
| AIP-111 | Move the sidecar, take identity from the gateway, publish quantity | test on the environment |
| BSS-040 | Delete core/marketplace-api | browser, as an end user |
| CMP-073 | The bootstrap window follows the hand-over variable | test on the environment |
| CTL-230 | Batch F: resource browser | browser, as an end user |
| FND-035 | The gate workflow | test on the environment |
| INF-112 | Chart templates and image for the inventory controller | test on the environment |
| MIG-020 | Spike: the disk-library read path and change tracking | test on the environment |
| PRD-204 | cortex ingestion of uploads: job table, upload and document routes, index process, text parser, chunker, event | test on the environment |
| PRD-508 | Account-information read operations and the ledger client | test on the environment |
| SEC-073 | Remove the committed database passwords and rotate them | test on the environment |
| SEC-272 | ImageValidatingPolicy for platform images, in audit | test on the environment |
| SOV-120 | The sovereign-ops command, its image, and seed-repo | test on the environment |

</details>

<details><summary><b>F-01</b> Fresh install, creates hw308 · 16 tickets · about 15 Nov · waits for R-001, R-002, R-003, R-004, R-005</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| CLD-030 | Per-Organization databases use the Sovereign's storage class | fresh install |
| CLD-076 | Documents link to the generated provider tables | fresh install |
| CLD-113 | Mesh and topology read the profile | fresh install |
| CTL-345 | One flow path: the flow server and the emitter leave the default kit | fresh install |
| FND-900 | Topology soak fixes (standing package) | fresh install |
| INF-050 | Install kured; check INF-G01 | fresh install |
| NET-011 | API port limited to declared sources | fresh install |
| NET-040 | Remove the unwired firewall and the unused SPIRE chart | fresh install |
| NET-095 | Block the instance metadata address from pods | fresh install |
| NET-190 | Route admission by owner | fresh install |
| NET-323 | Managed cluster on the reference cloud: the cluster and its node pools from the provider pack | fresh install |
| SEC-100 | Delete the abandoned OpenBao multi-region mechanisms | fresh install |
| SEC-105 | Delete Sealed Secrets | fresh install |
| SEC-274 | Delete the second image verifier: kit slot 32 and platform/sigstore | fresh install |
| SOV-020 | One default for the zero-touch switch | fresh install |
| SOV-130 | Substrate window, part 1: modules and cloud-init take the variables document | fresh install |

</details>

<details><summary><b>R-006</b> Update of hw308 · 16 tickets · about 15 Nov · waits for R-001, R-002, R-003, R-005</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| ACC-101 | Delete the fabricated session surface in the API and the console | browser, as an end user |
| AIP-070 | Delete the hollow gateway charts | test on the environment |
| AIP-104 | Limits, queue, time-outs, admin listener | test on the environment |
| AIP-112 | Streamed responses are metered | test on the environment |
| BSS-S1 | Spike: suite sign-in, suite access to the engine API, and the price list a fresh Sovereign shows | test on the environment |
| FND-036 | Sign-in as a gate check | test on the environment |
| FND-073 | The gate runs against the standing environment after each candidate, with no wipe | test on the environment |
| INF-120 | Runner image and node-mode tasks | test on the environment |
| PRD-201 | bp-bge chart to the serving rules | test on the environment |
| PRD-205 | Assistants: models list, answers with citations, residency refusal | test on the environment |
| PRD-211 | Document conversion parser | test on the environment |
| PRD-304 | bp-speech chart on CPU | test on the environment |
| PRD-509 | Internal authorisation API | test on the environment |
| SVC-180 | Screens group by class; the dead branch goes | browser, as an end user |
| SVC-200 | Catalogue edits may change card fields only | test on the environment |
| SVC-220 | Ring labels on the kit | browser, as an end user |

</details>

<details><summary><b>R-007</b> Update of hw308 · 13 tickets · about 15 Nov · waits for R-001, R-002, R-003, R-004, R-005, R-006 and 1 earlier</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| AIP-105 | One owner of the credential | test on the environment |
| AIP-113 | Durable spool, admin routes, scripted backend | test on the environment |
| BSS-060 | Gate check: self-service onboarding | test on the environment |
| CTL-242 | Catalogue edits are stored as the edited card fields only | test on the environment |
| FND-070 | Baseline: one fresh provision of `main` as it stands | test on the environment |
| INF-130 | Kinds ScanPolicy, HardeningBaseline, MachineRun; operations controller | test on the environment |
| INF-512 | Runner test task and its tools | test on the environment |
| NET-150 | Default-deny scaffolding and dial checks | test on the environment |
| NET-170 | Organization fence and isolation proof | test on the environment |
| PRD-206 | cortex screens | browser, as an end user |
| SEC-161 | Reconciler: secrets mounts and accessor substitution | test on the environment |
| SOV-180 | The API and the controllers read the variables and the mode at run time | test on the environment |
| SVC-230 | The wizard shows the generated kit list | browser, as an end user |

</details>

<details><summary><b>R-008</b> Update of hw308 · 11 tickets · about 15 Nov · waits for R-001, R-002, R-003, R-004, R-005, R-007 and 1 earlier</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| BSS-070 | BSS-G01 becomes required | test on the environment |
| CMP-020 | The API tells the truth: live mode, no fixture data, no invented list | test on the environment |
| FND-120 | W0 exit: two consecutive records on one candidate; freeze the topology surface | test on the environment |
| INF-020 | Spike: hardware virtualisation on worker nodes | test on the environment |
| INF-132 | Chart templates for the operations controller; check INF-G02 | test on the environment |
| INF-350 | Spike: change events from the reference cloud | test on the environment |
| INF-710 | Spike: the host security service on the operator-hosted stack | test on the environment |
| NET-081 | One producer of the Organization kubeconfig | test on the environment |
| NET-191 | Organization listeners admit only their Organization | test on the environment |
| SOV-190 | The console reads the mode from the API | browser, as an end user |
| SVC-130 | Enable function tests on the foundation slots | test on the environment |

</details>

<details><summary><b>R-009</b> Update of hw308 · 23 tickets · about 15 Nov · waits for R-001, R-002, R-003, R-006, R-008</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| BSS-101 | Package offer, the default offer, and the controller's table built from it | test on the environment |
| BSS-121 | Checkout books exact amounts in the wallet | test on the environment |
| CMP-022 | The console stops claiming what does not exist | browser, as an end user |
| CMP-032 | The aggregator uses the package: subjects, one judge, seven results; duplicate evaluators deleted | test on the environment |
| CMP-058 | Engine to the 1.19 line | test on the environment |
| CMP-071 | Policy files in every region's folder: scopes, the alignment function, the empty document | test on the environment |
| CTL-170 | Inspection: where chart-seeded Applications live and who reads them | test on the environment |
| CTL-810 | Delete the legacy promoter and failback templates | test on the environment |
| FND-038 | Suite helper: a test Organization and a Blueprint install | test on the environment |
| FND-141 | Flight workflow: one fresh installation with pre-flight and leftover sweep | test on the environment |
| FND-161 | Row map and browser harness | browser, as an end user |
| FND-183 | Scheduled backups of the platform databases | test on the environment |
| FND-184 | Scheduled Git bundles of the Sovereign's repositories | test on the environment |
| FND-191 | Remove the second matcher's job handling from the API | test on the environment |
| FND-192 | Remove the second matcher from the console | browser, as an end user |
| FND-201 | Spike: what the telemetry pipeline delivers today, and which collector stays | test on the environment |
| INF-140 | Vulnerability matching and daily re-matching | test on the environment |
| INF-810 | Blueprints bp-kubevirt and bp-cdi | test on the environment |
| NET-330 | Spike: a bridged secondary network for virtual machines (conditional, dropped until INF-810 is scheduled) | test on the environment |
| PRD-050 | Spike: client certificates on the Organization listener | test on the environment |
| SVC-110 | Function tests: topology charts (after the topology freeze) | test on the environment |
| SVC-135 | The Blueprint proofs check becomes required | test on the environment |
| SVC-140 | Applications get the test stanza (after the topology freeze) | test on the environment |

</details>

<details><summary><b>R-010</b> Update of hw308 · 12 tickets · about 15 Nov · waits for R-001, R-004, R-005, R-008, R-009</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| BSS-124 | The debit port: found by order, no overdraft, engine only | test on the environment |
| CMP-041 | ComplianceModel: definition, data files, chart template, loader, exemptions | test on the environment |
| CMP-061 | Engine values, informer kinds and mode adapter for the current policy kinds | test on the environment |
| CTL-172 | Platform charts take placement from values | test on the environment |
| CTL-820 | Region-loss gate check | test on the environment |
| FND-162 | Console regression: applications grid, application detail, catalogue | browser, as an end user |
| NET-195 | Organization listener: client-certificate validation or TLS passthrough, by the outcome of PRD-050 (conditiona | test on the environment |
| NET-223 | Application controller without the tier pivot | test on the environment |
| PRD-207 | Gate checks for slice A1 | test on the environment |
| PRD-506 | Gate checks for slice B1 | test on the environment |
| SEC-273 | Image verification denies; check SEC-G14 | test on the environment |
| SOV-225 | The release builder, and the root origin check | test on the environment |

</details>

<details><summary><b>R-011</b> Update of hw308 · 7 tickets · about 16 Nov · waits for R-001, R-002, R-003, R-005, R-009, R-010</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| CMP-045 | Posture routes and the change stream | test on the environment |
| CMP-062 | Validate policies to ValidatingPolicy, batch 1 (7 policies) | test on the environment |
| CMP-068 | Pod security: enforce baseline on Organization namespaces | test on the environment |
| FND-144 | The train publishes the release | test on the environment |
| FND-163 | Console regression: jobs, resource views, cloud and infrastructure read pages | browser, as an end user |
| NET-224 | API without the tier gate | test on the environment |
| NET-226 | Organization controller stops writing buildingBlock | test on the environment |

</details>

<details><summary><b>F-02</b> Fresh install, creates hw309 · 17 tickets · about 16 Nov · waits for R-001, R-002, R-003, R-006, R-008, R-009 and 1 earlier</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| ACC-010 | Close the default-install exposure of the access gateway | fresh install |
| ACC-102 | The web application, its database, the seed Job and the CronJob leave the chart | fresh install |
| CLD-023 | Delete the Hetzner-only Compositions, their definitions and orphan manifests | fresh install |
| CLD-086 | local module and reduced kit | fresh install |
| CTL-120 | Fleet intent path and its Kustomization | fresh install |
| CTL-620 | Flux upgrade to the 2.10 line | fresh install |
| FND-037 | Retire the shell verifier | fresh install |
| FND-170 | Remove the outside-witness worker, if the region-loss record does not use it | fresh install |
| FND-182 | Scheduled etcd snapshots to the Sovereign's object store | fresh install |
| FND-203 | One collector pipeline per cluster | fresh install |
| INF-060 | Remove the reboot settings from cloud-init | fresh install |
| NET-012 | Second provider firewall to the same rule | fresh install |
| NET-110 | Cilium base values to target | fresh install |
| NET-324 | First boot on a managed cluster: the platform installs and owns Cilium | fresh install |
| SEC-030 | etcd encryption on at first boot | fresh install |
| SOV-140 | Substrate window, part 2: revocable bootstrap access, peer tokens, node artifacts from the registry | fresh install |
| SOV-150 | Kit sources become substitutions, with a render-equivalence test | fresh install |

</details>

<details><summary><b>R-012</b> Update of hw309 · 5 tickets · about 16 Nov · waits for R-001, R-002, R-004, R-009, R-011</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| CMP-063 | Validate policies to ValidatingPolicy, batch 2 (7 policies) | test on the environment |
| CMP-081 | ComplianceDecision: kind, admission rule, routes, effect on findings | test on the environment |
| CMP-091 | Scanner kinds in the cache; vulnerabilities by workload | test on the environment |
| CMP-111 | Derived checks on events and deadlines; backup recency | test on the environment |
| FND-164 | Console regression: Organizations, users and access, settings | browser, as an end user |

</details>

<details><summary><b>R-013</b> Update of hw309 · 16 tickets · about 16 Nov · waits for R-001, R-002, R-003, R-008, R-009, R-012 and 2 earlier</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| BSS-102 | catalyst-api commits and serves the offer | test on the environment |
| CLD-061 | Crossplane 1.18 to 1.19 | test on the environment |
| CLD-090 | The three modules meet contract version 1; conformance tests | test on the environment |
| CMP-064 | Validate policies to ValidatingPolicy, batch 3 (7 policies) | test on the environment |
| CMP-088 | Scanner to the current line | test on the environment |
| CMP-092 | Configuration audit, secrets in images, risky roles, benchmark | test on the environment |
| CMP-114 | Stability, certificate and delivery checks | test on the environment |
| CTL-032 | Remove kubectl and helm from the API image | test on the environment |
| CTL-122 | Organization intent path, its Kustomization and its account | test on the environment |
| FND-130 | The gate suite runs on the local provider pack on every pull request | test on the environment |
| FND-165 | Console regression: compliance, sovereignty and hand-over pages, storefront pages | browser, as an end user |
| FND-185 | Restore drills as gate checks | test on the environment |
| FND-204 | Alerts leave the cluster as events on the bus | test on the environment |
| INF-055 | Leave the kured slot out of the local reduced kit | test on the environment |
| NET-280 | Spike: overlay profile | test on the environment |
| NET-326 | A managed cluster joins the mesh of a Sovereign | test on the environment |

</details>

<details><summary><b>R-014</b> Update of hw309 · 6 tickets · about 17 Nov · waits for R-001, R-002, R-012, R-013</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| CLD-062 | Crossplane 1.19 to 1.20 | test on the environment |
| CLD-101 | Golden files for the variables the engine writes | test on the environment |
| CMP-065 | Mutate policies to MutatingPolicy | test on the environment |
| CMP-121 | Framework mappings, native benchmarks and per-framework scores | test on the environment |
| CMP-154 | Secrets axis: the signals of the secrets area | test on the environment |
| FND-151 | The suite as an image, with the `self` selection | test on the environment |

</details>

<details><summary><b>R-015</b> Update of hw309 · 3 tickets · about 17 Nov · waits for R-001, R-003, R-014</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| AIP-120 | Agent tokens and the signer key set route | test on the environment |
| CLD-102 | Port interface, capability interfaces, generated registry | test on the environment |
| CMP-066 | Delete what is left: stub, disabled image policy, test-only set; move the access policy home | test on the environment |

</details>

<details><summary><b>R-016</b> Update of hw309 · 7 tickets · about 17 Nov · waits for R-004, R-007, R-010, R-012, R-014, R-015 and 1 earlier</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| AIP-130 | Model contract conformance client and checks | test on the environment |
| AIP-150 | The tool server accepts agent tokens | test on the environment |
| AIP-223 | Judge dispatcher: lanes, deadlines, agent tokens, A2A call, validation, late revision | test on the environment |
| AIP-250 | Spike: agent workspace on the Organization's own sign-in with a short-lived tool identity | test on the environment |
| CLD-103 | Credentials map, validation through the port, storage class from the profile | test on the environment |
| CMP-069 | Engine to the 1.20 line | test on the environment |
| CMP-083 | Engine exception for a waiver on an enforcing policy | test on the environment |

</details>

<details><summary><b>R-017</b> Update of hw309 · 5 tickets · about 17 Nov · waits for F-01, R-001, R-002, R-003, R-004, R-016</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| AIP-155 | Agent workspace: the Organization's own sign-in by mount and a short-lived tool identity, as the spike record  | test on the environment |
| AIP-225 | MCP endpoint and console HTTP routes inside the service | test on the environment |
| CLD-104 | The engine writes neutral variables; adapters write their own | test on the environment |
| CLD-112 | Credential check from the profile; one registry | test on the environment |
| CLD-121 | Providers routes | test on the environment |

</details>

<details><summary><b>F-03</b> Fresh install, creates hw310 · 14 tickets · about 17 Nov · waits for R-002, R-003, R-004, R-009, R-013, R-014 and 3 earlier</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| ACC-103 | The target and grant kinds, their controller and the grant life cycle | fresh install |
| CLD-064 | Convert the adoption Composition to pipeline mode | fresh install |
| CLD-093 | Contract rule: adding a region plans only additions | fresh install |
| CLD-131 | Provider substitutes move from the template into the modules (pure move) | fresh install |
| FND-193 | Remove the chart `bp-syft-grype`, its slot and its pins | fresh install |
| FND-205 | Declared objectives, and the observability gate check | fresh install |
| NET-153 | Allow-list of the hand-over proof's deny-egress object | fresh install |
| NET-210 | Availability zone per region and a separation output | fresh install |
| NET-325 | Public entry on a managed cluster: a load balancer on host ports, no NodePort | fresh install |
| NET-327 | Bootstrap kit profile for a managed cluster | fresh install |
| SEC-040 | Bootstrap secret hygiene on the node and reflector scope | fresh install |
| SOV-160 | Registry projects, the Organization Git token and the two repositories are created at install | fresh install |
| SOV-310 | DNS zones delegated to the Sovereign | fresh install |
| SOV-320 | Sign-in mail without the parent's credential | fresh install |

</details>

<details><summary><b>R-018</b> Update of hw310 · 4 tickets · about 17 Nov · waits for R-017</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| AIP-156 | Delete the long-lived bearer seed | test on the environment |
| AIP-227 | Console read path: reverse proxy /api/v1/ops/* in catalyst-api | test on the environment |
| CLD-105 | Hook variables become capability interfaces | test on the environment |
| CLD-122 | The wizard renders providers, credentials, regions and flavors from the profile | browser, as an end user |

</details>

<details><summary><b>R-019</b> Update of hw310 · 6 tickets · about 18 Nov · waits for R-001, R-002, R-003, R-005, R-008, R-018 and 2 earlier</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| ACC-106 | Session agent and session images | test on the environment |
| CLD-065 | Crossplane 1.20 to 2.4 | test on the environment |
| CLD-111 | Wipe and janitor through capabilities | test on the environment |
| NET-212 | Show the separation; refuse the claim when it is none | browser, as an end user |
| NET-328 | Console: the cluster class is a choice, and features follow its capabilities | browser, as an end user |
| SOV-170 | The Sovereign repository is seeded at bootstrap and serves the root | test on the environment |

</details>

<details><summary><b>R-020</b> Update of hw310 · 4 tickets · about 18 Nov · waits for R-007, R-010, R-011, R-013, R-018, R-019 and 5 earlier</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| CLD-123 | Wipe, decommission and cloud pages without provider names | browser, as an end user |
| NET-329 | The managed class passes the acceptance suite on the National Cloud | test on the environment |
| SOV-230 | closure-copy, and the harbor-prewarm step body replaced by it | test on the environment |
| SOV-760 | Kit keys are set by commit and at provisioning | test on the environment |

</details>

<details><summary><b>R-021</b> Update of hw310 · 1 tickets · about 18 Nov · waits for R-020</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| CLD-124 | Remove the legacy credential aliases | test on the environment |

</details>

<details><summary><b>F-04</b> Fresh install, creates hw311 · 9 tickets · about 19 Nov · waits for R-005, R-006, R-008, R-010, R-011, R-019 and 5 earlier</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| CLD-066 | provider-opentofu v1.1.11 by digest | fresh install |
| CLD-132 | Remaining provider tests become injected inputs | fresh install |
| SEC-060 | Keycloak secrets no longer computable | fresh install |
| SEC-150 | Seal-provider contract and the static node-file implementation | fresh install |
| SOV-175 | The Sovereign object gets its writer | fresh install |
| SOV-330 | The pool-domain manager is used only when a parent allocates a child | fresh install |
| SOV-335 | The vulnerability database is served from the Sovereign's own registry | fresh install |
| SOV-510 | The catalogue is the verified kit artifact | fresh install |
| SOV-765 | Ring switches by commit | fresh install |

</details>

<details><summary><b>R-022</b> Update of hw311 · 1 tickets · about 19 Nov · waits for F-04, R-019</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| SVC-250 | Spike: composite resources in a release; bucket provisioning for an Organization | test on the environment |

</details>

<details><summary><b>R-023</b> Update of hw311 · 1 tickets · about 19 Nov · waits for R-022</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| INF-210 | Spike: composite resource in an Organization's release, changed placement, moved Primary | test on the environment |

</details>

<details><summary><b>R-024</b> Update of hw311 · 1 tickets · about 20 Nov · waits for R-008, R-023</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| SVC-185 | Conditional: a Blueprint declaration that pins an Application to its region | test on the environment |

</details>

<details><summary><b>F-05</b> Fresh install, creates hw312 · 5 tickets · about 21 Nov · waits for R-007, R-010, R-011, R-013, R-019, R-020 and 6 earlier</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| CLD-092 | Seal hook conformance and the profile's seal section | fresh install |
| CLD-133 | Secondary-region suspend keys are set for every provider | fresh install |
| CLD-144 | Provider overlays replace provider-named flags | fresh install |
| SEC-162 | OpenBao 2.7.2, static seal, self-initialisation and declared audit device, as one change | fresh install |
| SOV-240 | The upstream-flip step; steps 5, 6, 7, 9 and 10 deleted | fresh install |

</details>

<details><summary><b>R-025</b> Update of hw312 · 5 tickets · about 21 Nov · waits for F-02, F-03, F-05, R-001, R-002, R-005</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| CLD-145 | Give live Sovereigns their provider-pack Kustomization | test on the environment |
| CMP-076 | Delete the parent's enforce patch | test on the environment |
| SEC-180 | OpenBao snapshot backup and restore drill | test on the environment |
| SEC-190 | Mounts, templated policies and generic roles | test on the environment |
| SOV-260 | Steps 1, 2 and 8 deleted; the sequencer loses the code of all eight | test on the environment |

</details>

<details><summary><b>R-026</b> Update of hw312 · 3 tickets · about 21 Nov · waits for R-004, R-025</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| CLD-143 | One Velero chart | test on the environment |
| SEC-196 | Hand-over identity sovereign-ops and the hand-over inputs Secret | test on the environment |
| SEC-200 | Library chart and namespaced stores for platform namespaces | test on the environment |

</details>

<details><summary><b>R-027</b> Update of hw312 · 1 tickets · about 21 Nov · waits for R-008, R-026</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| SEC-205 | Stores in Organization namespaces | test on the environment |

</details>

<details><summary><b>F-06</b> Fresh install, creates hw313 · 9 tickets · about 22 Nov · waits for R-001, R-002, R-003, R-017, R-020, R-025 and 4 earlier</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| CLD-088 | local pack: profile, adapter, overlay (first files-only proof) | fresh install |
| CLD-146 | Hetzner values leave the shared network slot; storage driver name from the profile | fresh install |
| CLD-162 | The Huawei module and adapter take installation values as inputs | fresh install |
| NET-228 | Cutover branches, generator branch and stale comments | fresh install |
| SEC-155 | Key-service seal on the reference cloud (only if SEC-135 answered YES) | fresh install |
| SEC-211 | Blueprint bp-secrets-store-csi and the injection negative check | fresh install |
| SEC-250 | PKI engine, internal issuer and mesh issuer | fresh install |
| SOV-250 | registry-pivot only removes the origin endpoint | fresh install |
| SOV-520 | release-sync: closure first, then the local tag | fresh install |

</details>

<details><summary><b>R-028</b> Update of hw313 · 16 tickets · about 22 Nov · waits for R-015, R-016, R-019, R-022, R-025, R-026 and 9 earlier</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| ACC-105 | OpenBao roles for the access plane | test on the environment |
| CLD-106 | Operator credentials from mounted files | test on the environment |
| CLD-107 | Command provider-identity and capability IdentityManager | test on the environment |
| CLD-181 | Hetzner pack: inventory by name prefix | test on the environment |
| FND-142 | The upgrade leg | test on the environment |
| INF-310 | Provider capability InventoryLister | test on the environment |
| NET-134 | Mesh role on the PKI engine and the issuer sovereign-mesh-ca | test on the environment |
| PRD-512 | OpenBao: partner client-certificate role and its policy line | test on the environment |
| SEC-192 | Producer identities and the simulation back end's own-path role | test on the environment |
| SEC-212 | First-party proof: the marketplace API reads its signing secret from a mounted file | browser, as an end user |
| SEC-213 | First Application proof: WordPress runs with its database password injected | test on the environment |
| SEC-310 | SSH certificate authority | test on the environment |
| SEC-335 | Spike: injection route for each upstream chart | test on the environment |
| SOV-270 | The proof exercises certificate, mail, Organization and inbound | test on the environment |
| SOV-530 | Offline bundle load | test on the environment |
| SVC-260 | provides, requires, the contract format, the registry and the install API | browser, as an end user |

</details>

<details><summary><b>R-029</b> Update of hw313 · 16 tickets · about 23 Nov · waits for R-008, R-017, R-025, R-026, R-027, R-028 and 5 earlier</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| PRD-504 | Registrar | test on the environment |
| SEC-214 | Marketplace Organization path, single-cluster databases: credentials injected, nothing in Git | test on the environment |
| SEC-231 | Roll-out: platform/postgres | test on the environment |
| SEC-232 | Roll-out: platform/valkey | test on the environment |
| SEC-235 | Roll-out: platform/powerdns | test on the environment |
| SEC-242 | Roll-out: platform/stalwart-tenant | test on the environment |
| SEC-244 | Roll-out: products/agenity | test on the environment |
| SEC-245 | Roll-out: platform/guacamole | test on the environment |
| SEC-246 | Roll-out: products/chargeback master key | test on the environment |
| SEC-247 | Roll-out: seven driver charts with one credential Secret each | test on the environment |
| SEC-344 | Upstream pods to injection: Grafana | test on the environment |
| SOV-405 | The parent provisions with a per-child identity | test on the environment |
| SOV-410 | ownership-pivot, part 1: own key, encrypted state push, plan gate | test on the environment |
| SOV-540 | The refresh refusal and the day-two reconcilers are deleted | test on the environment |
| SVC-270 | In-cluster object storage and the capability probe | browser, as an end user |
| SVC-360 | Delete the unused backing-service generator (after the topology freeze) | test on the environment |

</details>

<details><summary><b>R-030</b> Update of hw313 · 6 tickets · about 23 Nov · waits for R-008, R-026, R-028, R-029</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| SEC-215 | Marketplace Organization path, database pair: credentials out of Git | test on the environment |
| SEC-233 | Roll-out: platform/gitea | test on the environment |
| SEC-234 | Roll-out: platform/harbor | test on the environment |
| SEC-236 | Roll-out: platform/powerdns-admin | test on the environment |
| SEC-290 | Database engine: mount, binder, static and dynamic roles | test on the environment |
| SVC-280 | The dialog offers the implementation; capabilities routes | browser, as an end user |

</details>

<details><summary><b>R-031</b> Update of hw313 · 4 tickets · about 23 Nov · waits for R-028, R-029, R-030</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| SEC-300 | Dynamic database users for one first-party service | test on the environment |
| SEC-342 | Upstream pods to injection: Gitea | test on the environment |
| SEC-343 | Upstream pods to injection: Harbor | test on the environment |
| SEC-345 | Upstream pods to injection: PowerDNS and its administration interface | test on the environment |

</details>

<details><summary><b>F-07</b> Fresh install, creates hw314 · 11 tickets · about 23 Nov · waits for R-008, R-020, R-025, R-026, R-028, R-029 and 8 earlier</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| AIP-106 | bp-axon: chart, Blueprint file and kit slot 82 | fresh install |
| CLD-151 | Huawei plug-in chart; the route job leaves the shared network chart | fresh install |
| CLD-163 | Installation literals leave shared charts and scripts | fresh install |
| CLD-171 | State in the bucket, encrypted; ProviderConfig cloud | fresh install |
| CLD-183 | Second provider: dedicated seal volume on control-plane nodes | fresh install |
| CTL-130 | Receiver and system webhook | fresh install |
| NET-131 | Mesh layer from Git with the shared authority | fresh install |
| SEC-221 | Roll-out: platform/keycloak | fresh install |
| SEC-330 | TLS on the OpenBao listener | fresh install |
| SOV-720 | Kubernetes upgrade by system-upgrade-controller | fresh install |
| SVC-275 | Platform buckets: one kit instance of bp-object-bucket | fresh install |

</details>

<details><summary><b>R-032</b> Update of hw314 · 10 tickets · about 24 Nov · waits for R-013, R-015, R-019, R-026, R-028, R-029 and 6 earlier</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| AIP-107 | Images and structured output in Axon | test on the environment |
| AIP-160 | Simulation credential routes | test on the environment |
| CLD-152 | cloud-controller: load-balancer members and route runs follow Node events | test on the environment |
| CTL-162 | Batch A: Application intent from Git | test on the environment |
| NET-127 | Mesh and encryption evidence without exec | test on the environment |
| NET-161 | Audit run and flow report | test on the environment |
| SEC-222 | Roll-out: platform/sso-bridge | test on the environment |
| SEC-341 | Upstream pods to injection: Keycloak | test on the environment |
| SOV-340 | The parent's standing loops end at hand-over | test on the environment |
| SOV-420 | ownership-pivot, part 2: facts and base Workspace committed, step added to the chain | test on the environment |

</details>

<details><summary><b>R-033</b> Update of hw314 · 12 tickets · about 24 Nov · waits for R-008, R-009, R-010, R-025, R-028, R-032 and 8 earlier</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| CTL-126 | One robot per Organization as committer | test on the environment |
| CTL-174 | Platform placement is committed, not patched | test on the environment |
| CTL-180 | Batch B1: Organization record from Git | test on the environment |
| CTL-210 | Batch C: access grants from Git | test on the environment |
| CTL-224 | Batch D3: ad-hoc schedules from Git | test on the environment |
| CTL-250 | Retire giteapr and the iac repository; guard for Git writers | test on the environment |
| CTL-360 | Live-query hook and removal of the first-rank polls | browser, as an end user |
| NET-162 | Allow rules and enforcement, ring 1 (observability and scanners) | test on the environment |
| SEC-223 | Roll-out: platform/oidc-gate | test on the environment |
| SOV-430 | Parent side of the release: release token, purge, custody scan and report | test on the environment |
| SOV-440 | The offline-key restore drill | test on the environment |
| SOV-740 | Maintenance window on the base Workspace | test on the environment |

</details>

<details><summary><b>R-034</b> Update of hw314 · 14 tickets · about 24 Nov · waits for R-011, R-013, R-028, R-029, R-032, R-033 and 9 earlier</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| CMP-052 | Posture hub: Overview, Workload, Readiness | browser, as an end user |
| CTL-176 | Remove lookup from the platform Application templates | test on the environment |
| CTL-190 | Batch B2: suspend, resume and overage by commit | test on the environment |
| CTL-202 | DNS records as a step of the organization-controller | test on the environment |
| CTL-260 | Controllers' Git writes onto the library | test on the environment |
| CTL-370 | Second-rank polls: Application detail pages | browser, as an end user |
| CTL-372 | Second-rank polls: resources pages | browser, as an end user |
| CTL-374 | Second-rank polls: networking page | browser, as an end user |
| CTL-376 | Second-rank polls: architecture, readiness chip, cloud-list panes | browser, as an end user |
| CTL-610 | Rebuild-from-Git drill | test on the environment |
| INF-170 | Console: Machines | browser, as an end user |
| NET-163 | Allow rules and enforcement, ring 2 (platform services) | test on the environment |
| SEC-224 | Roll-out: products/catalyst | test on the environment |
| SOV-435 | parent-release: key revocation, access revocation, release call, custody proof | test on the environment |

</details>

<details><summary><b>R-035</b> Update of hw314 · 14 tickets · about 25 Nov · waits for R-028, R-029, R-030, R-032, R-033, R-034 and 13 earlier</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| CMP-072 | Mode change from the screen | browser, as an end user |
| CMP-082 | Decisions screen and the request dialogs | browser, as an end user |
| CMP-093 | Security tab | browser, as an end user |
| CMP-101 | Runtime detections by push: side-car to NATS, consumer, view | browser, as an end user |
| CMP-113 | Continuity and the Resilience screen | browser, as an end user |
| CMP-123 | Frameworks screen | browser, as an end user |
| CMP-131 | Daily snapshot, history route and screen; the unread history bucket goes | browser, as an end user |
| CTL-203 | Identity clients as a step of the organization-controller | test on the environment |
| CTL-410 | Application controller on controller-runtime | test on the environment |
| NET-164 | Allow rules and enforcement, ring 3 (identity, data, edge helpers) | test on the environment |
| PRD-212 | Allow file for namespace model-serving | test on the environment |
| SEC-207 | Fence the legacy cluster-wide store | test on the environment |
| SOV-610 | Lifecycle journey L1, zero touch, on the train | test on the environment |
| SOV-910 | Decommission by the owner, outside the cluster | test on the environment |

</details>

<details><summary><b>R-036</b> Update of hw314 · 12 tickets · about 25 Nov · waits for R-014, R-016, R-025, R-033, R-034, R-035 and 8 earlier</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| CMP-053 | Delete the old compliance pages and the old stream frames | browser, as an end user |
| CMP-074 | Lock stage policy to deny | test on the environment |
| CMP-085 | Remediation requests | browser, as an end user |
| CMP-098 | Sensor to the current line | test on the environment |
| CMP-125 | OSCAL export | browser, as an end user |
| CMP-150 | Regions and producers: the Clusters view | browser, as an end user |
| CTL-204 | Console certificate as a step of the organization-controller | test on the environment |
| CTL-415 | Identity values reach every Application of a customer Organization | test on the environment |
| NET-165 | Allow rules and enforcement, ring 4 (kernel) | test on the environment |
| NET-171 | Organization fence: two labelled exceptions for the open-banking product (conditional, dropped until epic PRD- | test on the environment |
| SOV-950 | The residual sequencer moves to a controller (conditional) | test on the environment |
| SOV-970 | Add a region by Git commit | test on the environment |

</details>

<details><summary><b>F-08</b> Fresh install, creates hw315 · 18 tickets · about 25 Nov · waits for R-028, R-029, R-031, R-032, R-034, R-036 and 18 earlier</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| ACC-104 | Session kind and reconciler, kind pod, and the exec certificate scoped to one pod | fresh install |
| AIP-140 | bp-model-gateway: chart and kit slot 81 | fresh install |
| BSS-090 | The engine's bus client and outbox | fresh install |
| CLD-165 | Billing engine: the cloud endpoint is a required setting | fresh install |
| CLD-172 | OpenTofu provider binaries from the Sovereign's registry | fresh install |
| CLD-173 | Facts for unit modules: day2_facts, the facts ConfigMap, the join token | fresh install |
| CLD-182 | Re-prove Hetzner, single region | fresh install |
| CTL-132 | Ten-minute safety interval | fresh install |
| CTL-220 | Batch D1: the marketplace setting from Git | fresh install |
| FND-152 | Chart `bp-conformance` and its slot | fresh install |
| INF-160 | Install bp-machine-operations; flip INF-G02 | fresh install |
| NET-132 | Mesh authentication mode cluster | fresh install |
| NET-166 | Enforce everywhere and delete the old policy charts | fresh install |
| PRD-511 | Keycloak chart option and its kit key | fresh install |
| SOV-436 | No static SSH key on a Sovereign's nodes | fresh install |
| SOV-450 | Read-only adoption and the parent's post-hand-over hooks are deleted | fresh install |
| SOV-725 | k3s default moves to the 1.32 line | fresh install |
| SOV-730 | Cloud credential rotation on schedule and on request | fresh install |

</details>

<details><summary><b>R-037</b> Update of hw315 · 4 tickets · about 25 Nov · waits for R-004, R-005, R-009, R-014, R-035, R-036 and 2 earlier</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| CMP-141 | Organization view | browser, as an end user |
| CMP-152 | Break-glass uses on the Clusters view | browser, as an end user |
| CMP-170 | Efficiency measure | test on the environment |
| CTL-205 | Initial Applications and the storefront path through the API | test on the environment |

</details>

<details><summary><b>R-038</b> Update of hw315 · 16 tickets · about 25 Nov · waits for R-028, R-029, R-032, R-033, R-034, R-037 and 19 earlier</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| AIP-135 | Nightly record for the checks that need a real model | test on the environment |
| AIP-145 | Task routes: embeddings, rerank and transcription, with their metering | test on the environment |
| AIP-170 | Model gateway and tool server resolve inside Organization vclusters | test on the environment |
| AIP-270 | Virtual keys for Organization Applications (built on first need) | test on the environment |
| AIP-275 | The meter refuses a stopped Organization | test on the environment |
| BSS-106 | The engine publishes the offer and catalyst-api commits it | test on the environment |
| BSS-111 | Decisions are recorded with an identifier and a sequence, and can be published | test on the environment |
| CLD-174 | Unit module nodepool for the local and Huawei packs, proven as plain unit Workspaces | test on the environment |
| CTL-207 | Delete the legacy overlay, the pipeline state and the second client | test on the environment |
| CTL-222 | Batch D2: console sidebar overrides from Git | test on the environment |
| CTL-710 | Push for the Organization console and the storefront | browser, as an end user |
| INF-175 | Browser specifications for the machine pages; check INF-G12 | browser, as an end user |
| INF-410 | OpenBao SSH role and sign policy for runners | test on the environment |
| NET-310 | Cilium 1.20 | test on the environment |
| SEC-198 | Remove the transitional cloud-credential Secrets | test on the environment |
| SOV-810 | Mothership mode complete | browser, as an end user |

</details>

<details><summary><b>R-039</b> Update of hw315 · 9 tickets · about 25 Nov · waits for R-003, R-004, R-007, R-008, R-034, R-038 and 2 earlier</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| AIP-181 | Remove the per-Organization gateway and the proxy shell from the funnel | test on the environment |
| AIP-202 | Host-side fence: every namespace of an Organization reaches the model gateway and the tool server; callers of  | test on the environment |
| BSS-107 | An engine without packages adopts the committed offer; publication outcome; no restated prices | test on the environment |
| BSS-113 | Spend limit and budget are evaluated when the ledger is written | test on the environment |
| BSS-150 | Provider modules follow a setting | browser, as an end user |
| BSS-170 | The storefront's application list is generated | test on the environment |
| CLD-175 | NodePool definition with its two Compositions | test on the environment |
| CTL-195 | Entitlement decisions arrive as events | test on the environment |
| CTL-208 | Delete the org-tenants Flux source | test on the environment |

</details>

<details><summary><b>R-040</b> Update of hw315 · 8 tickets · about 26 Nov · waits for R-016, R-028, R-029, R-034, R-038, R-039 and 6 earlier</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| AIP-171 | The signer key set resolves inside Organization virtual clusters | test on the environment |
| AIP-182 | Delete the gateway client, per-user key issuing and the administrator-token seed | test on the environment |
| AIP-203 | Chart platform/kagent, Blueprint file, conformance agent and check | test on the environment |
| BSS-104 | Order capture prices from the committed offer | test on the environment |
| BSS-131 | The engine books model usage events | test on the environment |
| CLD-221 | Object storage, native side: unit module day2/objectbucket and Composition objectbucket-native | test on the environment |
| PRD-090 | Spike: GPU node on the National Cloud gate environment | test on the environment |
| SOV-710 | Node scaling by Git commit | browser, as an end user |

</details>

<details><summary><b>R-041</b> Update of hw315 · 11 tickets · about 26 Nov · waits for R-018, R-030, R-032, R-035, R-038, R-040 and 7 earlier</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| AIP-190 | Delete credential seeding and refresh outside Axon | test on the environment |
| AIP-280 | Traces from gateway and agent runtime to the cluster collector | test on the environment |
| AIP-285 | A usage record lands in the billing engine | test on the environment |
| BSS-105 | The storefront reads one offer | browser, as an end user |
| BSS-109 | The Organization console and the back office read the offer | browser, as an end user |
| BSS-132 | Delete the money path of model usage in order capture | test on the environment |
| BSS-133 | The engine books request counts and audio seconds | test on the environment |
| BSS-202 | Bill file collector (FOCUS) | test on the environment |
| PRD-401 | bp-gpu-runtime chart | test on the environment |
| SOV-820 | The child walk with company hosts blocked | test on the environment |
| SVC-290 | Native object bucket and the swap test | test on the environment |

</details>

<details><summary><b>R-042</b> Update of hw315 · 5 tickets · about 26 Nov · waits for F-02, R-001, R-004, R-007, R-040, R-041</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| BSS-108 | Delete plans and add-ons from the catalog service | test on the environment |
| BSS-203 | Neutral meter class | test on the environment |
| PRD-404 | Allow file for the namespace of bp-gpu-runtime | test on the environment |
| SOV-920 | A replacement mothership is provisioned and proven | test on the environment |
| SVC-300 | Catalogue choices on the Sovereign kind and the admin screen | browser, as an end user |

</details>

<details><summary><b>F-09</b> Fresh install, creates hw316 · 18 tickets · about 27 Nov · waits for R-035, R-038, R-039, R-040, R-041, R-042 and 14 earlier</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| ACC-108 | Admission and the session fence | fresh install |
| AIP-183 | Delete kit slot 80 and what points at it | fresh install |
| AIP-226 | bp-ops-records: chart, Blueprint file, slot 83, caller entry and check | fresh install |
| AIP-320 | Model access for a Sovereign that holds no login: backend kind upstream through the parent's gateway | fresh install |
| BSS-112 | Enforcement runs on events; the HTTP doors are deleted | fresh install |
| BSS-122 | On a Sovereign the engine settles invoices from the wallet | fresh install |
| CLD-176 | Bootstrap unit provisioner-access: the provisioner's API-port rule leaves the bootstrap module | fresh install |
| CLD-177 | Admission rule on the module address of a Workspace | fresh install |
| CLD-184 | Second provider: front doors declared by the module on host ports; the value controller leaves the schema | fresh install |
| CLD-190 | Retire Hetzner (only if CLD-182 fails or the founder chooses retirement) | fresh install |
| CLD-201 | AWS bootstrap module | fresh install |
| INF-145 | Flip INF-G11 once the database is reachable after hand-over | fresh install |
| INF-220 | Unit module day2/virtualmachine for the reference and local packs | fresh install |
| NET-140 | Delete mesh establishment from the API | fresh install |
| NET-181 | Edge node pool, added beside the current entry | fresh install |
| PRD-208 | Embeddings and rerank in the model plane: slot 42, kit key, aliases | fresh install |
| PRD-403 | GPU runtime in the kit: slot 84, two kit keys, check | fresh install |
| SOV-726 | k3s default moves to the 1.33 line | fresh install |

</details>

<details><summary><b>R-043</b> Update of hw316 · 3 tickets · about 27 Nov · waits for R-002, R-009, R-042</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| BSS-301 | Instance identity and the usage statement intake (parent side) | browser, as an end user |
| SOV-930 | Child records and public names move to the replacement | test on the environment |
| SVC-320 | The postgres contract, in-cluster | test on the environment |

</details>

<details><summary><b>R-044</b> Update of hw316 · 15 tickets · about 27 Nov · waits for R-032, R-034, R-036, R-038, R-039, R-043 and 18 earlier</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| AIP-184 | Delete the charts, the bridge sidecar and their builds | test on the environment |
| AIP-185 | Remove the old gateway from the umbrella chart's policies | test on the environment |
| AIP-228 | Console pages: incident list, incident with timeline and actions, judgements | browser, as an end user |
| AIP-241 | Correlator: template, schema, prompt, evaluation set, and the evaluation runner | test on the environment |
| BSS-123 | Delete the recurring card subscription and the second invoice system | browser, as an end user |
| BSS-125 | A credited wallet settles open invoices at once | test on the environment |
| BSS-302 | An instance reports its usage (child side) | browser, as an end user |
| PRD-209 | Publish bp-cortex | test on the environment |
| PRD-405 | The release train provisions a GPU pool and declares gpu | test on the environment |
| PRD-507 | Publish bp-fingate and bp-fingate-sandbox | test on the environment |
| SEC-170 | Existing clusters: tokens, etcd encryption, seal migration, salt | test on the environment |
| SEC-238 | Delete the cross-region Secret copies of the API | test on the environment |
| SEC-262 | External Secrets to 0.16.2 and every manifest to the v1 API | test on the environment |
| SOV-437 | The provisioner's access rule is moved and emptied at hand-over | test on the environment |
| SOV-940 | The Kustomize twins and the transitional keys are deleted | test on the environment |

</details>

<details><summary><b>R-045</b> Update of hw316 · 7 tickets · about 27 Nov · waits for F-08, R-009, R-036, R-044</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| AIP-242 | Investigator | test on the environment |
| BSS-142 | Payment-provider keys leave the database | test on the environment |
| BSS-303 | The standalone installation option is proven in CI | test on the environment |
| PRD-210 | The release train installs the AI hub and declares product-ai-hub | test on the environment |
| PRD-510 | The release train installs open banking and declares product-open-banking | test on the environment |
| SEC-263 | External Secrets to the current 2.x minor | test on the environment |
| SOV-960 | The parent's provisioning watch moves to the same controller (conditional) | test on the environment |

</details>

<details><summary><b>R-046</b> Update of hw316 · 6 tickets · about 27 Nov · waits for R-015, R-025, R-028, R-039, R-040, R-045 and 2 earlier</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| AIP-243 | Scribe; the Blueprint file; the evaluation check | test on the environment |
| PRD-301 | MCP tools for agents | test on the environment |
| PRD-303 | Chat front end with the user's identity | test on the environment |
| PRD-308 | Route that stores a user-supplied credential for an Application | test on the environment |
| PRD-607 | OpenBao: transit signing lines for an Application | test on the environment |
| PRD-701 | Portal: catalogue from the documents, application form, status, sandbox console | browser, as an end user |

</details>

<details><summary><b>R-047</b> Update of hw316 · 4 tickets · about 27 Nov · waits for R-025, R-028, R-036, R-045, R-046</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| AIP-244 | Coordinator and problem-manager, with their rule functions | test on the environment |
| PRD-302 | Sandboxed fetch Job and the Git connector | test on the environment |
| PRD-601 | Domestic payments with detached signatures and idempotency | test on the environment |
| PRD-702 | Bank-admin screens: approvals, pins, delivery replay | browser, as an end user |

</details>

<details><summary><b>R-048</b> Update of hw316 · 6 tickets · about 28 Nov · waits for R-002, R-003, R-047</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| AIP-245 | Fixer; tool propose_change; change-proposal route; change_links | test on the environment |
| PRD-305 | Web connector and scheduled sync | test on the environment |
| PRD-602 | Domestic scheduled payments and standing orders | test on the environment |
| PRD-603 | Funds confirmation and variable recurring payments | test on the environment |
| PRD-605 | Outbox relay and the events operations | test on the environment |
| PRD-703 | Release the portal | test on the environment |

</details>

<details><summary><b>F-10</b> Fresh install, creates hw317 · 7 tickets · about 28 Nov · waits for R-035, R-038, R-039, R-040, R-043, R-044 and 8 earlier</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| ACC-107 | Broker, tunnel and client page: a pod session from the browser | fresh install |
| AIP-291 | bp-vllm with tool-call parser, API key file and model cache on a GPU pool | fresh install |
| CLD-202 | AWS profile, adapter and overlay | fresh install |
| CLD-222 | PostgreSQL, native side: unit module day2/postgresinstance and Composition postgresinstance-native | fresh install |
| INF-230 | VirtualMachine definition, two Compositions, catalogue format | fresh install |
| NET-182 | Gateway listeners bind on edge nodes only | fresh install |
| PRD-306 | Transcription in the model plane: slot 85, kit key, alias | fresh install |

</details>

<details><summary><b>R-049</b> Update of hw317 · 1 tickets · about 28 Nov · waits for R-048</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| PRD-604 | Release payments, funds confirmation and recurring payments | test on the environment |

</details>

<details><summary><b>R-050</b> Update of hw317 · 11 tickets · about 28 Nov · waits for R-032, R-041, R-043, R-044, R-048, R-049 and 14 earlier</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| ACC-111 | Recording store: bucket, ingest, chain, manifest, signature | browser, as an end user |
| ACC-121 | Intent routes for targets and grants | browser, as an end user |
| INF-240 | Blueprint bp-virtual-machine; flip INF-G03 | test on the environment |
| INF-250 | Image catalogue routes | test on the environment |
| INF-320 | Sources declared and cloud-account; remaining rules; check INF-G04 | test on the environment |
| INF-820 | Composition virtualmachine-kubevirt, instance types, profile flag | test on the environment |
| MIG-101 | Module skeleton and API types | test on the environment |
| PRD-307 | The release train switches transcription on and declares model-transcription | test on the environment |
| PRD-402 | Choose the default self-hosted chat model by measurement | test on the environment |
| PRD-606 | Release events | test on the environment |
| SVC-330 | Native PostgreSQL | test on the environment |

</details>

<details><summary><b>R-051</b> Update of hw317 · 9 tickets · about 29 Nov · waits for R-033, R-034, R-036, R-038, R-042, R-050 and 4 earlier</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| ACC-112 | Playback, the player page and the integrity checks | browser, as an end user |
| ACC-122 | Console: targets, requests, approvals, sessions | browser, as an end user |
| BSS-160 | The platform collector meters a virtual machine Application | test on the environment |
| CMP-156 | Machine reports in the read model | test on the environment |
| INF-340 | Console: ownership and inventory health | browser, as an end user |
| INF-610 | Spike: bake by capture on the reference cloud and the local pack | test on the environment |
| INF-830 | Check INF-G10 | test on the environment |
| MIG-111 | `vmcopy`: copy loop, result format, `file` reader, `fetch` | test on the environment |
| MIG-141 | `l2relay` agent: data plane | test on the environment |

</details>

<details><summary><b>R-052</b> Update of hw317 · 7 tickets · about 29 Nov · waits for R-001, R-002, R-005, R-028, R-050, R-051 and 1 earlier</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| ACC-123 | The console pod shell opens an access session | browser, as an end user |
| ACC-131 | Kind ssh with signed certificates | browser, as an end user |
| ACC-201 | Spike: text for review, and the model's fit | test on the environment |
| ACC-230 | Live observation | browser, as an end user |
| MIG-112 | Worker image and conversion | test on the environment |
| MIG-113 | vSphere control and change tracking | test on the environment |
| MIG-146 | `l2relay` control interface and image | test on the environment |

</details>

<details><summary><b>R-053</b> Update of hw317 · 5 tickets · about 29 Nov · waits for R-005, R-007, R-025, R-030, R-050, R-052 and 2 earlier</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| ACC-132 | Kind database: PostgreSQL command-line workbench | browser, as an end user |
| ACC-240 | Platform nodes as SSH targets, without the static key | test on the environment |
| INF-930 | Source type hypervisor | test on the environment |
| MIG-114 | `vddk` reader and the library init contract | test on the environment |
| MIG-121 | Controller manager, `SourceProvider` reconciler, chart templates | test on the environment |

</details>

<details><summary><b>R-054</b> Update of hw317 · 2 tickets · about 29 Nov · waits for R-007, R-053</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| ACC-133 | Organization isolation journey | browser, as an end user |
| MIG-128 | `NetworkMap` and `StorageMap` reconcilers | test on the environment |

</details>

<details><summary><b>F-11</b> Fresh install, creates hw318 · 8 tickets · about 29 Nov · waits for R-006, R-013, R-034, R-038, R-050, R-052 and 3 earlier</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| ACC-260 | Remove the shared-secret leg of the exec proxy | fresh install |
| AIP-292 | Alias document repointed to selfhosted; evaluation sets re-run | fresh install |
| CLD-203 | AWS unit module nodepool and its Composition | fresh install |
| INF-260 | Console: Virtual machines | fresh install |
| INF-330 | Account source on by default, coverage rule; flip INF-G04 | fresh install |
| INF-420 | Runner SSH mode, trust on declared machines, identity rules; check INF-G05 | fresh install |
| MIG-124 | Landing contract on the `local` pack | fresh install |
| NET-183 | Close the old entry | fresh install |

</details>

<details><summary><b>R-055</b> Update of hw318 · 4 tickets · about 29 Nov · waits for R-001, R-052, R-054</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| ACC-202 | Rule file, matcher, findings | browser, as an end user |
| ACC-211 | Kind web: a recorded browser per session | browser, as an end user |
| ACC-221 | Spike: RDP with a directory account checked out for the session | test on the environment |
| MIG-122 | Plan validation | test on the environment |

</details>

<details><summary><b>R-056</b> Update of hw318 · 6 tickets · about 30 Nov · waits for F-11, R-053, R-055</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| ACC-203 | Tool server for the review agents | test on the environment |
| ACC-212 | Sign-in fill for web targets | browser, as an end user |
| ACC-213 | Graphical database tool | browser, as an end user |
| ACC-222 | Kind rdp | browser, as an end user |
| INF-430 | Scans over SSH; flip INF-G05 | test on the environment |
| MIG-120 | Package `landing`: the landing Workspace in Go | test on the environment |

</details>

<details><summary><b>R-057</b> Update of hw318 · 6 tickets · about 30 Nov · waits for R-008, R-051, R-052, R-055, R-056</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| ACC-204 | End and hold; dispositions; review screen | browser, as an end user |
| ACC-205 | Frame tool for the review agents | test on the environment |
| INF-440 | Runner patch task | test on the environment |
| INF-450 | MaintenanceWindow, PatchPolicy and the roll-out logic | test on the environment |
| INF-720 | Capability HostSecurity; engine native for scans; check INF-G09 | test on the environment |
| MIG-123 | `Migration` reconciler: prepare, initial copy, synchronisation | test on the environment |

</details>

<details><summary><b>R-058</b> Update of hw318 · 3 tickets · about 30 Nov · waits for F-03, R-003, R-004, R-036, R-057</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| CMP-155 | Access signals on the posture hub | browser, as an end user |
| INF-455 | Run routes; check INF-G06 | test on the environment |
| MIG-129 | Hooks, events, and the helper sub-commands of cutover | test on the environment |

</details>

<details><summary><b>R-059</b> Update of hw318 · 7 tickets · about 1 Dec · waits for R-025, R-038, R-050, R-052, R-056, R-058 and 2 earlier</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| INF-460 | Enrolment of discovered machines | test on the environment |
| INF-480 | Hardening remediation | test on the environment |
| INF-490 | Policy intent routes and screens | browser, as an end user |
| INF-495 | Hold operations on a machine under migration | test on the environment |
| INF-510 | TestScope, scope rules, generated egress and kill handling | test on the environment |
| INF-910 | Windows Server 2022 and later over SSH: facts, pending-update report, patch | test on the environment |
| MIG-125 | Cutover and landing | test on the environment |

</details>

<details><summary><b>R-060</b> Update of hw318 · 3 tickets · about 1 Dec · waits for R-007, R-025, R-059</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| INF-515 | Kill route; check INF-G07 | test on the environment |
| INF-920 | Windows before 2022 over HTTPS remote management with a rotated password | test on the environment |
| MIG-126 | Rollback, acceptance, cancel, rehearsal | test on the environment |

</details>

<details><summary><b>F-12</b> Fresh install, creates hw319 · 8 tickets · about 1 Dec · waits for R-050, R-051, R-052, R-057, R-058, R-059 and 3 earlier</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| ACC-245 | Machines as SSH targets | fresh install |
| AIP-293 | Delete Axon, slot 82, the credential routes and their guard entries | fresh install |
| CLD-204 | AWS live walk | fresh install |
| INF-465 | Adoption of discovered machines | fresh install |
| INF-470 | VirtualMachineSnapshot and pre-patch snapshots | fresh install |
| INF-620 | Kind MachineImage, capture module, catalogue commit | fresh install |
| INF-730 | Profile section and console display of engine availability | fresh install |
| NET-184 | A load balancer per region | fresh install |

</details>

<details><summary><b>R-061</b> Update of hw319 · 4 tickets · about 1 Dec · waits for R-003, R-013, R-025, R-052, R-059, R-060 and 4 earlier</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| INF-520 | Agent tool and approval screens | browser, as an end user |
| MIG-127 | Hermetic journeys on the local pack; function test | test on the environment |
| MIG-143 | Controller wiring for extensions | test on the environment |
| MIG-151 | Intent routes, credentials route, route-table entries | test on the environment |

</details>

<details><summary><b>R-062</b> Update of hw319 · 3 tickets · about 1 Dec · waits for F-02, R-009, R-028, R-033, R-053, R-061</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| MIG-133 | Live journeys on the reference cloud | test on the environment |
| MIG-152 | Console: Sources and Plans pages | browser, as an end user |
| MIG-156 | Intent round-trip check | test on the environment |

</details>

<details><summary><b>R-063</b> Update of hw319 · 2 tickets · about 1 Dec · waits for R-061, R-062</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| MIG-144 | Extension gate checks | test on the environment |
| MIG-153 | Console: Migrations page | browser, as an end user |

</details>

<details><summary><b>R-064</b> Update of hw319 · 1 tickets · about 2 Dec · waits for R-063</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| MIG-154 | Console: Extensions page | browser, as an end user |

</details>

<details><summary><b>F-13</b> Fresh install, creates hw320 · 5 tickets · about 3 Dec · waits for F-03, F-12, R-034, R-039, R-054, R-058 and 1 earlier</summary>

| Ticket | What it does | Proven by |
|---|---|---|
| ACC-250 | Regional brokers | fresh install |
| INF-630 | Node pools from a baked image | fresh install |
| INF-640 | Engine image-rotation; check INF-G08 | fresh install |
| NET-185 | DNS front end on the edge pool | fresh install |
| NET-200 | Entry failover by DNS | fresh install |

</details>

<details><summary><b>Merged one by one, no deployment</b> · 78 tickets</summary>

| Ticket | What it does | About |
|---|---|---|
| CTL-060 | Decision record for the write path, the lock and the backbone | 13 Nov |
| CLD-141 | Protect provider releases from pruning before they move | 13 Nov |
| NET-321 | Decision record: managed cluster class on the reference cloud | 13 Nov |
| FND-011 | CI bumps chart versions; pull requests may not | 14 Nov |
| NET-090 | Network guards in CI | 14 Nov |
| NET-251 | Decision record: firewall mechanism and the package list of epic NET-250 | 14 Nov |
| AIP-050 | Agent-plane guards | 14 Nov |
| ACC-050 | Truth in the access documents | 14 Nov |
| BSS-141 | The cost-source credential is never a Kubernetes Secret | 14 Nov |
| FND-012 | Stop the tracker commits | 14 Nov |
| FND-098 | Remove two retired chart folders and one dead harness | 14 Nov |
| FND-099 | Remove two custom resources that nothing reads | 14 Nov |
| NET-102 | Registry in the Sovereign status, checked against the live cluster | 14 Nov |
| FND-013 | Merge queue: aggregate check, queue triggers, rulesets and code owners as files | 14 Nov |
| CTL-045 | Publish contract C9 and its tool | 14 Nov |
| FND-015 | Candidate tool and cut workflow | 14 Nov |
| FND-017 | Every bot writer pushes under the release-writer identity; the queue is switched on | 14 Nov |
| FND-056 | Pin the two moving actions; ratchets for actions and base images | 14 Nov |
| FND-057 | The privacy guard fails closed and runs on every branch | 14 Nov |
| MIG-060 | Guard: no vendor disk library in the repository or in public images | 14 Nov |
| SEC-090 | Guards | 14 Nov |
| FND-096 | Remove the Sandbox sources | 14 Nov |
| SVC-040 | Normalise names, API version and visibility | 14 Nov |
| ACC-040 | Access guards with their baselines | 14 Nov |
| FND-910 | Dependency updates (standing package) | 14 Nov |
| FND-062 | Every image and chart build signs through the action | 14 Nov |
| FND-104 | Truth patch of the canonical documents | 14 Nov |
| NET-221 | Delete the four retired charts and their seed entries | 14 Nov |
| MIG-010 | Decision record: migration is a platform product; correct the documents | 14 Nov |
| NET-055 | Canonical documents state the zone, firewall and default-deny design of record 0019 | 14 Nov |
| SVC-150 | One generator, part 1: embedded and console catalogue | 14 Nov |
| SVC-160 | One generator, part 2: the seed becomes generated | 14 Nov |
| FND-071 | Flight checklist: pre-flight gate before every fresh installation, leftover sweep after every wipe | 14 Nov |
| SVC-166 | The umbrella chart's Blueprint file moves beside its chart | 14 Nov |
| SVC-162 | Shelf deletion: README-only folders and scaffold Blueprint files | 14 Nov |
| SVC-164 | Delete the OpenMeter Blueprint | 14 Nov |
| SVC-205 | Lock stage blueprints to deny | 14 Nov |
| SVC-365 | Stop serving v1alpha1 of the Blueprint definition | 14 Nov |
| SVC-170 | Contract C1: class and lifecycle on every Blueprint, with their readers | 14 Nov |
| FND-080 | Set the manifest from the baseline | 15 Nov |
| INF-135 | Lock stage machine-ops in audit | 15 Nov |
| SOV-010 | Record the cutover result of the baseline provision and correct the lifecycle documents | 15 Nov |
| SEC-095 | Exception register, source guard and Secret census | 15 Nov |
| SEC-096 | Census limit set from the baseline; SEC-G11 required | 15 Nov |
| CTL-800 | Decision record 0027: the region-loss contract | 15 Nov |
| NET-222 | Blueprint data without tiers | 15 Nov |
| CTL-815 | One Continuum definition file | 15 Nov |
| FND-202 | Decision record 0035: one telemetry pipeline, alert events, declared objectives | 15 Nov |
| SVC-350 | Promote proven Blueprints to supported | 15 Nov |
| NET-227 | Narrow the kinds | 16 Nov |
| CLD-087 | CI job conformance-local | 16 Nov |
| NET-120 | Hermetic two-cluster mesh test | 16 Nov |
| FND-166 | Retire the ledger tooling | 16 Nov |
| CLD-114 | Adoption map, name lists and schema enums | 17 Nov |
| CLD-211 | Pack tokens and the additive check | 17 Nov |
| SOV-210 | Hand-over scenario, part 1: fixture parent and bootstrap on the hermetic local provider pack | 18 Nov |
| SOV-212 | Hand-over scenario, part 2: parent stub, hand-over run and assertions | 18 Nov |
| SEC-261 | Conformance test for External Secrets against OpenBao | 21 Nov |
| CLD-147 | Remove the prune protection | 21 Nov |
| SEC-115 | SECURITY.md section 5 rewritten to the built state | 22 Nov |
| SOV-265 | Lock stage flux to deny | 23 Nov |
| CTL-212 | Lock stage access to deny | 24 Nov |
| CTL-178 | Lock stage applications to deny | 24 Nov |
| SEC-248 | Census limit lowered after the chart roll-out | 24 Nov |
| CTL-264 | Lock stage flux to deny | 24 Nov |
| SOV-620 | Freeze the proven lifecycle paths | 25 Nov |
| CLD-164 | Installation-literal check | 25 Nov |
| PRD-505 | Authorisation conformance workflow | 25 Nov |
| CLD-212 | Hard-zero directories; retire the old guard | 25 Nov |
| CTL-209 | Lock stage organizations to deny | 25 Nov |
| BSS-103 | The organization-controller reads the committed offer | 26 Nov |
| BSS-201 | Collector interface and registry; the existing collector behind it | 26 Nov |
| MIG-102 | Definitions, chart skeleton, Blueprint file | 28 Nov |
| ACC-140 | Framework mapping as data; runbook | 29 Nov |
| INF-540 | Lock stage machine-ops to deny | 1 Dec |
| INF-530 | Rules of engagement and provider terms in the runbook | 1 Dec |
| MIG-170 | `MigrationWave` | 1 Dec |
| MIG-180 | Runbook and status | 2 Dec |

</details>

## Delivered since the baseline

`ACC-060`, `AIP-060`, `BSS-010`, `BSS-020`, `BSS-030`, `BSS-035`, `BSS-050`, `CLD-010`, `CLD-040`, `CLD-050`, `CLD-063`, `CLD-070`, `CLD-071`, `CLD-075`, `CLD-080`, `CLD-161`, `CMP-005`, `CMP-010`, `CMP-012`, `CMP-014`, `CMP-016`, `CMP-030`, `CMP-036`, `CMP-S1`, `CMP-S2`, `CMP-S5`, `CMP-S6`, `CTL-010`, `CTL-020`, `CTL-025`, `CTL-044`, `CTL-050`, `CTL-055`, `CTL-201`, `FND-002`, `FND-014`, `FND-021`, `FND-022`, `FND-023`, `FND-031`, `FND-032`, `FND-033`, `FND-034`, `FND-040`, `FND-051`, `FND-052`, `FND-053`, `FND-054`, `FND-055`, `FND-061`, `FND-091`, `FND-092`, `FND-093`, `FND-094`, `FND-097`, `FND-101`, `FND-102`, `FND-103`, `FND-105`, `FND-106`, `FND-110`, `FND-171`, `FND-173`, `FND-181`, `INF-010`, `INF-030`, `INF-070`, `MIG-050`, `NET-010`, `NET-020`, `NET-031`, `NET-033`, `NET-041`, `NET-042`, `NET-050`, `NET-052`, `NET-060`, `NET-070`, `NET-072`, `NET-100`, `NET-125`, `PRD-010`, `PRD-020`, `PRD-030`, `PRD-101`, `PRD-104`, `PRD-105`, `PRD-106`, `SEC-005`, `SEC-006`, `SEC-020`, `SEC-071`, `SEC-110`, `SEC-125`, `SEC-130`, `SEC-140`, `SEC-280`, `SOV-030`, `SOV-040`, `SOV-050`, `SOV-060`, `SOV-110`, `SOV-220`, `SVC-010`, `SVC-020`, `SVC-030`, `SVC-120`, `SVC-210`, `SVC-340`.

The same page with hover and filters is the file `north-star-dashboard.html` beside this one: download it and open it in a browser.
