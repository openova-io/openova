# EPIC completion — evidence-backed, 2026-07-31

Every number below is queried live from GitHub (`gh issue list --label epic`) or from `git log`, not estimated. Written because "what percent are the EPICs at" had been answered from the durable completion matrix, which measures *pillars*, not EPICs — a different denominator, and the distinction matters.

## The EPIC set: 18 labelled, 16 closed, 2 open

    gh issue list --repo openova-io/openova --label epic --state all

| EPIC | state |
|---|---|
| #4212 ONE object-model / DR backbone | **OPEN** (`status/blocked-ext`) |
| #3969 Application-centric Placement (`targets[]`) | **OPEN** (`status/blocked-ext`) |
| #4010 bp-chepherd · #3988 OpenOva MCP · #3188 shared backing-services · #2737 G117 lifecycle Phase 2 | closed |
| #1101 Multi-cluster + Continuum DR · #1100 Networking · #1099 Cloud Resources · #1098 RBAC · #1097 Applications · #1096 Compliance · #1095 Foundation contracts · #1094 Phase 0/1 roll-out | closed |
| #1090 console routing/auth audit · #1082 Sovereign-first onboarding · #825 Multi-domain Sovereign · #795 SME-tenant turnkey | closed |

**16 / 18 = 88.9% of labelled EPICs closed.** The two open ones are both `status/blocked-ext` — deliberately parked architecture calls, not stalled delivery. Note the coincidence and do **not** conflate: this 88.9% is EPIC-closure and the durable pillar figure (~88) are different measurements that happen to land near each other.

## The two open EPICs, by child issue

Counted with `gh issue list --search "<n> in:body,title"` over all states:

| EPIC | referencing issues | open | closed | child completion |
|---|---|---|---|---|
| **#4212** DR backbone | 16 | **0** | 16 | **16/16 = 100%** |
| **#3969** placement `targets[]` | 16 | 5 | 11 | **11/16 = 68.8%** |

**#4212 has zero open children.** Every issue that references it is closed; what remains is the crossplane-adoption architecture call itself, which is why it carries `status/blocked-ext`. Its DR half is live-Healthy and was re-proven by hw291's cutover.

**#3969's five open children** are the honest remaining surface, and four of the five are the *same defect class* — a placement value being asserted rather than derived:

| child | what it is | fix state |
|---|---|---|
| #5515 | `derivePattern` fails open → empty target list renders `singleton` | **fixed + delivered** (`796e587b2` ⊂ `fb41faf`), 21/21 tests |
| #5482 | Overview renders a host-cluster label as PRIMARY REGION | read-side **fixed + delivered** (`b41c93b3c`); data-layer emit open |
| #5422 | Console Overview hardcodes a `singleton` Placement fallback | open |
| #5420 | Topology renders *declared* placement, not *effective* per-cluster | open |
| #4212 | the sibling EPIC (cross-reference, not a child) | — |

So #3969's real remaining work is **three console-side issues**, two of which (#5422, #5420) are the same "declared vs effective" seam that #5515 and #5482 already fixed on their own surfaces. That is a coherent, small, nameable frontier — not an open-ended EPIC.

## Child-fix commit evidence (last 30 days)

    git log --oneline --since='30 days ago' --grep='#3969\|#4212'   → 23 commits

The *fix* commits (excluding docs) show the EPICs advancing through their children while the aggregation issues stayed quiet:

- `e937cda9d` fix(#4836): accept #3969 placement `targets[]` in `HandleApplicationUpdate`
- `b72326dfd` fix(#4950): keep `placement.targets[]` through decode so console Edit-Apply derives mode
- `b41c93b3c` fix(#5482): read `primaryRegion` from `status.placement`, not a key the CR lacks
- `7879bcb23` fix(#4986): bp-postgres emits `dr-<instance>` Continuum CR → shared-pg DR panel renders

**Thread-lag, not code-stagnation:** an EPIC issue with no recent comments while four of its children merge fixes looks stalled on the board and is not. This is the reading error the durable-number rules exist to prevent — measure the children and the commits, not the parent's comment timestamp.

## What is honestly not measured here

EPIC closure is **not** the same as the acceptance ledger. A closed EPIC does not mean its UAT rows are green — the ledger is currently in its reset state pending the hw292 walk (see [`PATH-TO-100.md`](../../ledger/PATH-TO-100.md)). Three separate measurements, three separate denominators:

1. **EPIC closure** — 16/18 = 88.9% (this document)
2. **UAT ledger** — reset pending hw292; last walked 135/281 on hw291, north-star 214/281 on hw288
3. **Durable pillar completion** — the completion matrix, moved only by walk evidence

Quoting any one of these as "the completion percentage" without naming which is exactly the ambiguity that produced the drifting numbers the founder called out.
