# Walkability triage of every unwalked (☐) UAT row — 2026-08-02

Produced so "the remaining rows need an environment" is a **number with a method**, not an assertion.
Every ☐ row was classified by what its assertion actually requires.

| class | count | what it needs |
|---|---|---|
| MOTHER / console-UI | 86 | the console serving — `console.openova.io` is **503** (catalyst ns = 0 pods, #5558) |
| SOVEREIGN | 16 | a converged Sovereign — hw291 wiped, hw292 unfired |
| RUNTIME | 9 | any live cluster with the object present |
| decidable from repo/registry | **0 after this session's walks** | — |
| unclassified, hand-inspected | 21 | see below |

## The 21 unclassified rows were inspected by hand, and three were decidable

Rows **47 / 48 / 49** assert the topology `<select>` vocabulary. That lives in source, so it is
decidable with no environment at all — and all three were walked to ✅ this session.

The other 18 resolve to console-UI or Sovereign-runtime on inspection (treemap rendering, showback
attribution, wizard steps, reconciler drill-in, CSI storageclass, CloudAdoption sync, DNS
split-horizon). None is decidable today.

## Why this matters for the ledger's honesty

`GREEN 11/286 = 3.8%` is low, and the triage explains *why* without excusing it: **111 of 131
remaining rows are gated on a surface that does not exist right now**, and the repo/registry-decidable
pool is genuinely exhausted. The number moves when hw292 fires, not before.

The converse is the useful half: it also proves no decidable row is being left unwalked. Anything
that could be decided from the repo has been.

## Method

`scripts/`-free one-off classification over `docs/ledger/UAT.md`, keyed on the assertion text:
UI verbs (click/render/reload/sign-in) → console; Organization/Application/cutover/DR → Sovereign;
Ready/Running/Healthy/HTTP → runtime; chart/template/manifest → repo. Unclassified rows were read
individually rather than bucketed by default, which is how 47/48/49 were found.
