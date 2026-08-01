# Completion matrix — 2026-08-01

Every number here is queried live (`gh issue list`, `git log`, `kubectl`) or read from a
committed artifact. Nothing is estimated, and nothing is re-derived in conversation —
per the durable-number rule the pillar score moves **only** on walk evidence booked into
[`../../ledger/UAT.md`](../../ledger/UAT.md).

## Three denominators — never conflate

They land near each other by coincidence, not because they measure the same thing.

| Measure | Value | Named artifact |
|---|---|---|
| **Durable pillar completion** | **88** | unchanged since the hw291 walk — see "Why 88 has not moved" |
| **EPIC closure** | **16 / 18 = 88.9%** | `gh issue list --label epic --state all` → 18 total, 16 closed, 2 open; detail in [`../2026-07-31/epic-completion-evidence.md`](../2026-07-31/epic-completion-evidence.md) (`c56dd0aed`) |
| **UAT acceptance ledger** | **reset state** | `429a39f76` — reset for the hw292 cycle by `scripts/reset-uat.py` |

The ledger's reset is **correct, not a regression**: the founder's law is that a new env
flushes all prior evidence, enforced mechanically. Last walked figures were **135/281 on
hw291** and a north-star **214/281 on hw288**.

## Why 88 has not moved — proven, not asserted

No Sovereign exists to walk. Verified 2026-08-01:

    $ kubectl --context omantel-region-a get nodes     -> timed out
    $ kubectl --context omantel-region-b get nodes     -> timed out
    $ kubectl --context org-7283        get nodes      -> timed out
    $ kubectl --context demo-vcluster   get nodes      -> connection refused (127.0.0.1:9443)

    $ curl -s -o /dev/null -w '%{http_code}' https://hw291.omani.works/           -> 000
    $ curl -s -o /dev/null -w '%{http_code}' https://hw292.omani.works/           -> 000
    $ curl -s -o /dev/null -w '%{http_code}' https://console.hw291.omani.works/   -> 000

Only the **mothership** answers (`45.151.123.50`, `catalyst-api 1/1`), and it is a
read-only control-plane surface, not a Sovereign acceptance surface.

88 is therefore a **held** number, not a stalled one.

## The two open EPICs

| EPIC | children | open | closed | note |
|---|---|---|---|---|
| **#4212** DR backbone | 16 | **0** | 16 | zero open children; what remains is the Crossplane-adoption architecture call (`status/blocked-ext`). DR half live-Healthy, re-proven by hw291's cutover. |
| **#3969** placement `targets[]` | 16 | 5 | 11 | real frontier is three console-side issues |

#3969's open children, with delivery state by ancestry:

| child | state | named artifact |
|---|---|---|
| #5515 `derivePattern` fails open | fixed + delivered | `796e587b2` ⊂ image `fb41faf`, 21/21 tests |
| #5482 Overview shows a host-cluster label as PRIMARY REGION | read-side delivered; **emit half open** | `b41c93b3c`; emit seam root-caused to `application_controller.go:2593` vs `placement_projection.go:279` (issue comment 2026-08-01) |
| #5422 Overview hardcodes a `singleton` fallback | fix open in PR | **#5536** |
| #5420 Topology renders declared, not effective | fix open in PR | **#5538** |

## Pillar state, each with a named artifact

| # | Pillar | State | Named artifact |
|---|---|---|---|
| 1 | Marketplace + voucher onboarding | money-path E2E proven | `ade93f2a4` — rows 72–90 + 216/217, hw286 2nd-Org purchase |
| 2 | Multi-region BCP choice at signup | proven | hw288-era G12 chain |
| 3 | Two CNPG clusters + region-kill | **6/6 clean, RPO=0** | hw282 G12; #5303 proven end-to-end |
| 4 | Per-Org Agenity + `bp-openova-mcp` | **gated on founder credential** | #4277 — the one true external gate |
| 5 | Sovereign independence post-cutover | **cc=true 11/11** | hw291 `2026-07-30T11:03:43Z`, 600 s deny-egress hold both regions, 0 regressions |

Pillar 4 is the only one gated on something no engineering change can supply.

## Rows walked this cycle (no Sovereign required)

| row | was | now | artifact |
|---|---|---|---|
| **M1** (#4466) janitor log-only full cycle | ⛔ | ✅ | PR **#5546** — live mothership pass `destructive=false reaped=0`, per-item `would-reap` markers |
| **G5** (#4466) dry-run full-cycle observation | ⛔ | ✅ | PR **#5546** — same pass, 7029 ms |
| **R20** (#4464) deploy-bot bumps pins per-line | ⛔ | ✅ | PR **#5546** — full population 859 commits, 854 (99.4%) single-line, zero blanket bumps |
| **R1** (#4454) sweep spares a `ready` Sovereign | ⛔ | **held ⛔** | vacuous with zero `ready` Sovereigns — a zero-reap against zero candidates would pass even if the protect-side inversion were broken |

Two prior evidence notes (G4, G5) were corrected in the same PR: both stated reclamation
that a `destructive:false` pass never performed.

## Built this cycle, all held pending the merge decision

| PR | what |
|---|---|
| #5543 | five handlers returned HTTP 200 over 400/403/502 (#5542) — cited runner `fast_executor.py` exists nowhere in either repo or history |
| #5544 | NodePort guard reported full coverage while skipping 17 of 89 charts; banned-words guard had no vacuity self-test (#5512) |
| #5546 | the three row walks above |
| #5547 | powerdns anycast kept live node ports behind a correct-looking chart (#5348) |
| #5548 | cluster-side §854 guard — catches the drift class no source-side check can see |

Landed directly on main: `666888b12` (reset-uat was destroying UAT assertions),
`bf59fb5f0` (SIGPIPE blocking the bp-gitea publish), `9c546dbf5`, `c56dd0aed`,
`53bdf8052`, `f47870f6d`.

Issues filed from live evidence: **#5542**, **#5545**.

## What converts 88 → 90–91

One thing: the hw292 fire. Then #5515 / #5489 / #5485 / #5482 / #5477 get walked live,
rows stamp, and the number moves on evidence rather than on assertion.

---

## Surface map — which surfaces are live, and what each can actually walk

Recorded because "the cluster is down" was too coarse and got challenged, correctly.
Some surfaces ARE up; they just do not host the object model the open rows assert on.
Measured 2026-08-01.

| Surface | Live? | Evidence | Can it walk a UAT row? |
|---|---|---|---|
| Sovereign kube API (`omantel-region-a/b`, `org-7283`) | **no** | `kubectl get nodes` → timed out (8 s) ×3 | no |
| `demo-vcluster` | **no** | `dial tcp 127.0.0.1:9443: connect: connection refused` | no |
| `hw291/hw292.omani.works`, `console.hw291…` | **no** | `curl -w '%{http_code}'` → **000** ×3 | no |
| `marketplace.openova.io` | **no** | HTTP **503** | no |
| **`console.openova.io/sovereign/`** | **YES** | HTTP **200** | **no — see below** |
| mothership kube API (`45.151.123.50`) | **YES** | `catalyst-api 1/1`, `catalyst-ui 1/1` | yes, for mothership-scope rows only |

### Why the live console still cannot walk #5482 / #5515 / #5489

`console.openova.io/sovereign/` answers 200, so "no surface exists" was wrong. But it
serves a **1063-byte SPA shell** titled `OpenOva Corporate`, and a literal scan of that
response finds **zero** occurrences of `Applications`, `Organizations`, `Environments`,
`PRIMARY REGION`, or `placement`.

The cluster behind it holds **zero** catalyst/openova CRDs:

    $ kubectl get crd -o name | grep -icE 'catalyst|openova|dr\.'
    0
    $ kubectl get applications.catalyst.openova.io -A
    error: the server doesn't have a resource type "applications"

That is the mothership's actual role: it is the **deployment control plane** that
provisions Sovereigns. It does not host the Catalyst object model. #5482 asserts what an
**App detail Overview** renders; with no Application objects and no CRD, there is no such
page to open — not because the surface is down, but because the model those pages render
does not exist on this cluster.

The rows walked this cycle (M1, G5, R20) were reachable precisely because their
assertions are about the **mothership's own** behaviour — the janitor loop and the
deploy-bot's commits — not about Catalyst objects.

**Rule this settles:** a row is walkable iff its assertion's *subject* exists on a
reachable surface. Surface reachability alone is not sufficient, and neither is a
literal "is the cluster up" check.
