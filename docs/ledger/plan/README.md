# The transition plan: every task

843 tasks in 8 stages. One task is one pull request of one, three or five days. A stage is a planning word: what decides when a task may run is the tasks it waits for.

## How to read this

- Open a stage. Inside it the tasks are grouped by area, then by group. Rows are in the order the tasks can run.
- Each row says what the task does, what it waits for and how it is proven. Under each group a fold lists the files each task changes.
- "ticket" opens the instruction for the executing model, where it is written. A ticket is written shortly before its task runs, against the code as it then stands.
- 9 tasks have their ticket: [FND-021](tickets/FND-021.md), [FND-022](tickets/FND-022.md), [FND-031](tickets/FND-031.md), [FND-051](tickets/FND-051.md), [FND-052](tickets/FND-052.md), [FND-053](tickets/FND-053.md), [NET-030](tickets/NET-030.md), [NET-031](tickets/NET-031.md), [CMP-014](tickets/CMP-014.md).

## The stages, in order

| Stage | What happens in it | Tasks |
|---|---|---|
| [Stage 1 (W0): Secure and measure](stage-1-W0.md) | Rails, a baseline measurement, deletions, security fixes, truth in documents. No new architecture. | 202 |
| [Stage 2 (W1a): Trust floor](stage-2-W1a.md) | Injected credentials, seal, deny by default, signing, backup and restore. | 118 |
| [Stage 3 (W1b): Born-local Sovereign](stage-3-W1b.md) | Local from first boot, hand-over to Crossplane, in-place upgrade, child provisioning. | 91 |
| [Stage 4 (W1c): Write path and events](stage-4-W1c.md) | Changes born in Git, webhook-triggered apply, screens by push. | 47 |
| [Stage 5 (W1d): Contracts](stage-5-W1d.md) | Provider contract, service model, generated catalogues, second-provider proof. | 51 |
| [Stage 6 (W2): First modules](stage-6-W2.md) | Compliance screens, billing, the model gateway and agent runtime, access, machine provisioning. | 147 |
| [Stage 7 (W3): Dependent modules](stage-7-W3.md) | Machine operations, exemplar agents, migration, managed Kubernetes, more native services. | 118 |
| [Stage 8 (W4): Products](stage-8-W4.md) | AI hub and open banking. | 69 |

## Tasks per area and stage

| Area | S1 | S2 | S3 | S4 | S5 | S6 | S7 | S8 | All |
|---|---|---|---|---|---|---|---|---|---|
| FND Foundation gate and execution rules | 53 | 12 | 3 | 5 |  | 7 |  |  | 80 |
| CLD Cloud abstraction | 14 | 4 | 26 |  | 25 | 2 | 6 |  | 77 |
| SOV Sovereign lifecycle | 10 | 17 | 33 |  | 1 | 1 | 6 |  | 68 |
| SEC Secrets and root of trust | 21 | 50 | 2 |  | 1 | 2 |  |  | 76 |
| NET Network, zones and cluster classes | 24 | 20 | 4 | 2 | 6 | 7 | 4 | 3 | 70 |
| SVC Service model | 17 |  |  | 2 | 16 | 7 | 1 |  | 43 |
| CTL Control plane | 13 | 4 | 10 | 38 |  | 9 |  |  | 74 |
| CMP Compliance and posture | 11 |  | 13 |  |  | 30 | 4 |  | 58 |
| BSS Commerce and billing | 10 | 1 |  |  |  | 23 | 7 | 1 | 42 |
| AIP Agent plane | 7 | 4 |  |  |  | 23 | 20 | 5 | 59 |
| INF Infrastructure plane | 7 | 6 |  |  | 2 | 16 | 24 | 6 | 61 |
| MIG Migration from VMware | 6 |  |  |  |  |  | 28 |  | 34 |
| ACC Access plane | 6 |  |  |  |  | 17 | 15 |  | 38 |
| PRD Later products | 3 |  |  |  |  | 3 | 3 | 54 | 63 |

## What happens first

1. The plan, the check list and the written tickets enter the platform repository.
2. `FND-021` builds the tool that reads the plan and says which task is next. `FND-022` builds the guard in CI that refuses a change that leaves the files of its task. Nothing else starts before both are merged.
3. After that, every task whose "waits for" is merged can start. Three executors work side by side; two tasks may run together when neither waits for the other and they change no common file.
4. A capability counts as done when its automatic check passes on two fresh installations in a row.

