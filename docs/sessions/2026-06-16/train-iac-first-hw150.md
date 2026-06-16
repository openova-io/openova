# Train: "IaC-first standard engine" → walked on hw150

**Founder directive (2026-06-16):** treat the 8 review points holistically, build them as
parallel PRs that merge as **one coordinated train**, test what's live-testable on hw149
before it's wiped, update/extend the UAT walkthroughs so the **final walk runs on hw150**,
and assign subagents to the parallel work.

## The spine (why these are one thing)

IaC in git is the single source of truth. The platform is a standard reconciliation engine.
The UI is a thin, bidirectional skin: it **renders** declared+live state and turns "edit"
into **a write back to the same IaC** (commit → Flux reconciles). Nothing is keyed on an app
name; every activity is a reconciliation of declared state. Each point below is a corollary,
and the train is that spine landed.

## The train cars (PRs) — each a subagent

| Car | Points | Scope | Surface | Subagent |
|----|--------|-------|---------|----------|
| **T0** | 1,2,3 | topology picker ← SupportedTopologies; create canonicalise; inline catalog edit (popup gone) | catalyst-api + UI | ✅ **shipped — PR #3649** |
| **T1 — cutover bulletproof** | 0,3 | registry-pivot reloads k3s so post-cutover image pulls hit local Harbor (#3647); egress-block gate FORCES a pod roll under deny-egress (proves zero external deps) | cutover chart | go/infra agent |
| **T2 — de-hardcode** | 4 | engine-class generic (`producesInstances:<kind>`, not `postgresClass`); newapi admin-seed → a STANDARD `sso-bootstrap` Blueprint contract any app declares | core + charts + UI | go agent |
| **T3 — unified Flux activities** | 5 | one activity canvas: install / DB-provision / cutover / DR-switchover all projected from Flux objects + CRs via per-source bridges (absorbs #3646) | catalyst-api + UI | fullstack agent |
| **T4 — UI faithfulness** | 6 | skeletons + tight invalidation — never assert-then-retract: new-instance flash (singleton gate), Open-button-late, jobs stale-pending | UI | ts agent |
| **T5 — standard Continuum/DR** | 7 | the Continuum CR/controller produces live DR records + drives switchover (the unbuilt layer; #3492/#3375) — generic, not per-app | core controller | go agent |
| **T6 — catalog-edit → git** | 8 | catalog edit COMMITS to the local catalog git (not the commerce overlay); the UI re-reads from IaC, so an advanced user's git edit shows automatically | catalyst-api | go agent |

## Merge discipline (the "same train")

- Tracking epic issue **`train/iac-first-hw150`** lists every car + a shared label `train/hw150`.
- Every car branches off **origin/main**, opens a PR labelled `train/hw150`, `Refs` the epic.
- The train merges as one batch after all cars are green; **then** a single fresh prov (hw150)
  walks the whole thing. PR-merge ≠ shipped — the hw150 walk is the gate.

## Test-before-wipe (hw149, capture while alive)

Already captured live: NS#5 cutoverComplete + 4 proofs; NS#4 region-kill (stream→promote→
preserve→writable); #3647 (new-pod ghcr 401). Still to capture before wipe: the #6 UI repros
(new-instance flash, jobs stale-pending, Open-late) and the #7 "no live Continuum record" for
grafana/gitea — as evidence rows the UAT doc references.

## UAT walkthrough for hw150

Amend `docs/ledger/uat-walkthrough/` into a single **hw150** walk whose sections map 1:1 to the
acceptance of every car: topology create + placement (T0); pod-roll-under-deny-egress (T1);
"no app-name in code" spot-checks (T2); one activity feed incl. a switchover job (T3); zero
flash/stale (T4); a real DR switchover with a live Continuum record (T5); a catalog edit that
lands as a git commit + an out-of-band git edit that shows in the UI (T6).
