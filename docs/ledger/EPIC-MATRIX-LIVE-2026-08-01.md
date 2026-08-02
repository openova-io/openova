# EPIC completion matrix — regenerated from LIVE queries, 2026-08-01

Every number below was queried at generation time. Nothing is carried forward, estimated, or
inferred. The commands are printed so the table can be reproduced.

```
gh issue list --label epic --state all --limit 40        -> 18 EPICs (16 CLOSED, 2 OPEN)
gh issue list --search "<n> in:body,title" --state all   -> child total per EPIC
gh issue list --search "<n> in:body,title" --state open   -> open children per EPIC
```

## Headline — 16 / 18 EPICs closed = **88.9%**

| # | EPIC | state |
|---|---|---|
| 795 | SME-tenant turnkey experience | CLOSED |
| 825 | Multi-domain Sovereign — N parent domains | CLOSED |
| 1082 | Sovereign-first Organization onboarding | CLOSED |
| 1090 | Sovereign Console routing + auth journey | CLOSED |
| 1094 | Catalyst Phase 0/1 unified roll-out | CLOSED |
| 1095 | EPIC-0 Foundation contracts | CLOSED |
| 1096 | EPIC-1 Compliance — Kyverno + score aggregator | CLOSED |
| 1097 | EPIC-2 Applications — CRDs + controllers | CLOSED |
| 1098 | EPIC-3 RBAC — useraccess-controller + Keycloak | CLOSED |
| 1099 | EPIC-4 Cloud Resources — k9s-on-web | CLOSED |
| 1100 | EPIC-5 Networking — default-deny + Hubble | CLOSED |
| 1101 | EPIC-6 Multi-cluster + Continuum DR | CLOSED |
| 2737 | G117 Application Lifecycle Phase 2 | CLOSED |
| 3188 | Reusable backing-services model | CLOSED |
| 3988 | OpenOva MCP server — RBAC-scoped facade | CLOSED |
| 4010 | bp-chepherd as a deployable Application | CLOSED |
| **3969** | **Application-centric Placement (`targets[]`)** | **OPEN** |
| **4212** | **ONE object-model / DR backbone** | **OPEN** |

## The two open EPICs — child counts queried live

### #4212 — **16 / 16 children closed = 100.0%**, zero open children

The EPIC is open with **nothing left underneath it**. What remains is the Crossplane-adoption
architecture decision itself, not a work item. Its percentage cannot move by shipping code.

### #3969 — **11 / 16 children closed = 68.8%**, five open

This is the only EPIC whose percentage can move on code alone. Each open child, with the artifact
that names its current state:

| child | state | artifact |
|---|---|---|
| **#5422** Overview hardcodes `singleton` fallback | fix open in PR | **PR #5536** |
| **#5420** Topology renders declared, not effective | fix open in PR | **PR #5538** |
| **#5515** derivePattern fails open | **fix authored this session** | `509222c6e` (fix) + `02108f5e6`, `ebf5b6feb` (tests, incl. a real mothership record shape) |
| **#5482** App-detail renders host-cluster as PRIMARY REGION | **divergence pinned this session** | `daa1b1ad4` — executable test proving flat `status.primaryRegion` goes stale on failover while nested tracks the lease |
| **#4212** (cross-listed) | see above | 16/16 children closed |

**Arithmetic that matters:** landing PRs #5536 and #5538 takes #3969 from **11/16 (68.8%)** to
**13/16 (81.3%)**. With #5515's fix and #5482's pinned divergence also merged, it reaches
**15/16 (93.8%)**. That is the single highest-leverage merge available, and all of it is blocked on
merge permission, not on engineering.

## The other two denominators — stated so they are not conflated

| measure | value | why it is what it is |
|---|---|---|
| EPIC closure | **16/18 = 88.9%** | queried above |
| Durable pillar completion | **88** | moves only on walk evidence against a Sovereign; none exists |
| UAT acceptance ledger | **5 / 286 = 1.7%** | reset to the hw292 baseline (`429a39f76`); `scripts/uat-tally.py` |

They are near each other by coincidence. EPIC closure counts *issues*; the pillar number counts
*walked evidence*; the ledger counts *rows*. A 100% EPIC row does not imply a green UAT row — #1090
is CLOSED at 100% while **#5401 remains an open security finding in that exact surface** (per-Org
`/settings` exposing the Sovereign operator panel), escalated this session by `bf4213cf6` when the
live walk showed the fabricated HQ *drives cloud-region selection*.

## Session artifacts, by EPIC

| EPIC | artifact landed 2026-08-01 | SHA / ref |
|---|---|---|
| #3969 | derivePattern reads live regions, not declared specs | `509222c6e` |
| #3969 | #5515 pinned against a real mothership record | `ebf5b6feb` |
| #3969 | #5482 primaryRegion divergence pinned by test | `daa1b1ad4` |
| #1090 | #5401 escalated — fabricated HQ drives placement | `bf4213cf6` |
| #1096 | §854 live audit, 13 findings, root-caused | `9a0d290f5`, `047cf1eb2` → **#5561** |
| #1096 | powerdns `nodePort: 0`, 4-site lockstep | **PR #5560** |
| #1096 | per-region secret-mint guard w/ decoy self-test | **PR #5556** |
| catalog | R21 full-population audit → 6 inert Blueprints | `c74396bd9` → **#5559** |
| infra | mothership `catalyst` at zero pods, root-caused | `075fca9a2` → **#5558** |
| console | wizard Back-control fix + nav contract guard | **PR #5557** → **#5555** |
| CI | vitest gate could never fail a build | `8371c9918` |

## What did NOT move, and why

None of the headline numbers changed today. Every artifact above is a defect found, a guard added,
or a claim corrected — **not a runtime acceptance**, because runtime acceptance needs a Sovereign
and none exists (hw291 wiped, hw292 unfired, mothership `catalyst` at zero pods with the console
503 and zero Catalyst CRDs — three readings across ~3h).

Reporting that plainly is the point. A matrix that moved today would be measuring the wrong thing.
