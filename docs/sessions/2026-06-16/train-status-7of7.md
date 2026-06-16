# IaC-first train — 7/7 status (durable record, 2026-06-16)

The train = the founder's 2026-06-16 step-by-step review (8 points #0–#8) built as **7 coordinated
cars**, merged as one batch and walked on a fresh **hw150**. All seven are CI-green + mergeable.

## 7 cars — CI-green + mergeable

| Car | PR | head SHA | CI | Ticket (at #3370 bar) | Walkthrough |
|---|---|---|---|---|---|
| T0 | [#3649](https://github.com/openova-io/openova/pull/3649) | `ef80a505f` | ✅ | #3648 | `3648-topology-vocabulary.md` |
| T1 | [#3650](https://github.com/openova-io/openova/pull/3650) | `3b87092da` | ✅ | #3647 | `3647-cutover-bulletproof.md` |
| T2 | [#3655](https://github.com/openova-io/openova/pull/3655) | `ee7ab9dc1` | ✅ | #3654 | `3654-de-hardcode.md` |
| T3 | [#3652](https://github.com/openova-io/openova/pull/3652) | `558fef995` | ✅ | #3646 | `3646-flux-activities.md` |
| T4 | [#3658](https://github.com/openova-io/openova/pull/3658) | `80455cf82` | ✅ | #3656 | `3656-ui-faithfulness.md` |
| T5 | [#3653](https://github.com/openova-io/openova/pull/3653) | `b4d477b99` | ✅ | #3375/#3492 | `3375-topology-dr.md` |
| T6 | [#3651](https://github.com/openova-io/openova/pull/3651) | `355d0f4b2` | ✅ | #3657 | `3657-catalog-edit-iac.md` |

## Founder review item → car (one line each)

| Item | What | Car | PR |
|---|---|---|---|
| #0 | zero external deps after cutover (gate proves it) | T1 | #3650 |
| #1 | catalog editable in place, no chip+popup | T6 (+T0 inline UI) | #3651 / #3649 |
| #2 | AppDetail topology strip contradiction | T0 | #3649 |
| #3 | postgres create + bulletproof gate | T0 (create) + T1 (gate) | #3649 / #3650 |
| #4 | no per-app hardcoding (capability-declared engine) | T2 | #3655 |
| #5 | flux-first: every activity a job + DR-as-job | T3 | #3652 |
| #6 | UI faithfulness (new-instance flash / Open / jobs-stale) | T4 | #3658 |
| #7 | DR switchover live record, generic | T5 | #3653 |
| #8 | IaC single source / catalog-edit→git | T6 | #3651 |

> **On the older "Q1/Q2/Q6" framing:** those are the prior North-Star deliverables
> (every-app-in-a-vCluster, shared-PG many-to-many cards, per-app Open buttons), already shipped on
> hw144/hw146 — NOT part of this 8-point train. Q6's Open-button concern is subsumed by T4 / #6.

## Cleared-or-boarding
**BOARDING** — 7/7 CI-green + mergeable, ready to merge as one batch. Acceptance is the hw150 walk,
not the merge (PR-merge ≠ shipped).

## Cache-cleanup declaration
- The `/opt/registry-*` caches (~20 GB) are **REQUIRED** — the mothership's pull-through mirrors
  (dockerhub/ghcr/quay/kyverno/xpkg + the main `harbor-cache`) for the live `vmi3116389` control plane;
  the ghcr mirror holds the openova-io images.
- **NOT removed; RELOCATED** to `/home/registry-cache/…` via symlink; all 6 containers serve `/v2/` 200;
  mothership unaffected; persists across reboot (symlink + `restart=always`).
- Result: `/` **99% → 40%** (23 GB free); `/home` 51%. **CLEARED.**
- Plus the safe sweep (journal/apt/logs) and the registry relocation; no service touched beyond the
  per-registry brief restart.

## hw149 vs hw150
hw149 is cutover-complete → frozen + #3647-blocked from new images → **no car deploys there**. One fresh
hw150 walks all: pre-cutover phase (T0/T2/T4/T5/T6) + cutover phase (T1/T3).

## Next
Coordinated merge (#3655 → #3658 → #3649 → rest; resolve the CRD `blueprint.yaml` / `catalog.generated`
/ UI overlaps as they stack) → mothership roll settles (0 builds in-flight, pod-age > 5 min) → wipe
hw149 → fire hw150 → walk all seven walkthroughs.
