# ADR-0022 — Foundation gate, release train, stability register and execution protocol

Status: Accepted
Date: 2026-11-13
Supersedes: ADR-0015 decision D9, the gate wiring only (every other decision of ADR-0015 stands)

Written by work package FND-106. The decision is carried by two data files, `docs/ledger/gates.yaml` (the gate manifest) and `docs/ledger/plan.yaml` (the plan), and by two tools, `scripts/acceptance-gate.py` and `scripts/plan.py`. Each tool prints its rules at its head. Those rules are the detail of this decision.

## Context

The state of the repository on 2026-11-12, when the decision was taken. Numbers were measured at commit `1de1896b3`.

1. Done was measured by a ledger of about 286 rows in `docs/ledger/UAT.md`. The rows were walked by hand in a console. A green row could be carried from an environment that no longer exists.
2. One machine record of an acceptance run existed: `docs/ledger/acceptance/hw307-20261111T140635Z-4a0d.json`. It was produced on the long-lived environment `hw307`, not on a fresh installation.
3. ADR-0015 decision D9 put the acceptance gate in front of the next deploy bump. The gate tool could run in a mode that only reported, and a token in a commit message opened it (package FND-031 has removed both). A gate in that place can stop the fix that would make it pass.
4. Pull requests wrote chart versions and pins themselves, and a guard forced them to (`scripts/check-chart-version-bumped.sh`). Two open pull requests therefore claimed the same version lines, and a second guard existed only to catch that (`.github/workflows/check-chart-version-claim.yaml`).
5. 13,942 of the 25,982 commits on `main` were tracker refreshes, written every 15 minutes.
6. No file said which parts of the tree were stable and which were being redesigned. The topology surface of ADR-0015 was four days old.
7. There was no plan file, no ticket form, and no rule for what counts as proof of a finished task.

## Decision

### 1. One gate manifest that only moves forward

- `docs/ledger/gates.yaml` lists every gate check. A check is named `<AREA>-G<two digits>`, for example `FND-G03`.
- A check is in one of three states: `not-built`, `expected-fail`, `required`. It moves forward only.
- The work package that delivers a capability sets its named check to `required` in the same pull request. The manifest names that package in `delivered_by`. No other package may do it.
- A check that is `expected-fail` runs in every gate record and never fails a run.
- A check leaves the manifest only with an accepted decision record that carries the line `Retires-Check: <id>`.
- There are no gate levels, and no list of checks that must all pass before work may start.

### 2. One pass rule, bound to a candidate

- A candidate is one commit of `main`, named by the immutable Git ref `rc/<id>`.
- A gate record is the machine record of one run of the suite on one Sovereign: `docs/ledger/acceptance/<env>-<run_id>.json`. A record counts only if a workflow produced it, it carries a candidate, and it lists the chart versions and image digests it ran on.
- Each check declares its kind of proof. `fresh`: the check is about installing, handing over or wiping, and only records of fresh installations prove it. `running`: a running Sovereign at the candidate proves it, whether it was installed fresh or updated in place.
- A check is green when the two newest counting records of its kind carry the same candidate, agree on every chart version and image digest, both pass the check, and the newer one is at most 14 days old (`pass_rule` in the manifest). For a `fresh` check the two records come from two different installations.
- There is no override, no mode that only reports, and no green carried from another environment.
- A change reaches the standing environment by an update in place, never by a wipe. A fresh installation is made when a merged change touches first-boot files, and such installations are batched.

### 3. The gate sits in front of dependents, not in front of the deploy bump

- A package merges when every package it depends on is merged and every gate check those packages set to `required` is green.
- A red check therefore holds only the work that builds on it. A fix for the red check is never held by it.
- Waves are planning words. No tool gates on them.
- This replaces the second point of ADR-0015 decision D9: the suite no longer gates the next deploy bump, and a ledger row is no longer what certifies function. The first point stands: the live acceptance suite, with its journeys, is the proof of function.

### 4. Two standing proofs, one suite

- The suite under `tests/acceptance/sovereign/` is the only conformance suite. It is one suite for three places: `local` (a hermetic provider pack, on every pull request), `cloud` (a Sovereign at a candidate) and `self` (a Job inside any Sovereign, after an install and after each release).
- A release is published only from a candidate whose `required` checks are green. A failing check of any area stops the publication.
- The `self` run writes its report inside the Sovereign and needs nothing outside it.

### 5. The ledger is a regression list, not the measure of done

- `docs/ledger/UAT.md` keeps its rows and its frozen formula (`docs/PROTOCOL.md` section 10). The formula is printed next to the gate verdict, never in place of it.
- Nobody walks the ledger by hand again. A row becomes a browser test, and the browser tests are ordinary gate checks (`FND-G20` to `FND-G23`).
- `docs/ledger/TRACKER.md` is the output of `python3 scripts/plan.py render`. The 15-minute commit stops.

### 6. Release train: CI is the only writer of versions

- No pull request changes a chart `version:`, an `appVersion:`, a Blueprint version, a slot pin, a catalogue pin or an image tag line of something that exists before and after it. CI writes them after the merge. A pull request that adds a new chart sets `version: 0.1.0`.
- `main` gets a merge queue. It moves only to a commit whose checks passed on the merged result. One release-writer identity may push outside the queue, and only release-owned lines and gate records.
- A Sovereign under test follows the candidate ref, so it cannot change under the test.
- At most one kernel component (k3s, Cilium, Flux, Crossplane, OpenBao, Kyverno, CloudNativePG, Keycloak, cert-manager) changes its upstream version between two consecutive candidates. An upgrade is an ordinary package; there is no separate register of dated version risks.
- There is no integration branch beside `main`.

### 7. Stability register and the topology freeze

- `docs/ledger/stability.yaml` names every surface, its paths, its class (`stable`, `settling`, `thrashing`, `frozen`) and the date of its evidence.
- CI fails a change to a frozen path unless the pull-request body carries `Decision: ADR-NNNN` and that record is accepted and carries the line `Unfreezes: <entry id>`.
- At most one package is open per entry of class `thrashing` or `frozen`.
- The topology surface of ADR-0015 (the region file, the topology contract library, Continuum, the region Git layout, the region vocabulary, the journeys of the suite, the Application controller) is closed to plan packages until package FND-120 has merged. Until then only the standing package FND-900 carries fixes to it. Then its entries become frozen. `python3 scripts/plan.py state FND-120` printing `merged` is the only signal of the freeze.

### 8. One closed deletion list

- Withdrawn code is deleted, not marked. Each deleted item has one owning package.
- Every delete package appends its paths to `docs/ledger/deleted-paths.txt`. A guard fails the build if a listed path returns.

### 9. Documentation truth

- Inventories (bootstrap slots, components, custom resources, security controls) are generated from the tree. One workflow fails a pull request whose committed output is stale.
- In `docs/STATUS.md`, `docs/ARCHITECTURE.md`, `docs/SECURITY.md`, `docs/DOD.md` and `docs/RUNBOOKS.md` every second-level section carries a marker: built (with its evidence), target (with a decision record) or rule (with its guard). The count of unmarked sections may only fall.
- Decision records from 0016 on start with plain header lines that a tool can read. `docs/adr/README.md` states the form and allocates the numbers.

### 10. Test, lint and signing rails

- Every test suite runs on pull requests.
- Lint and vulnerability checks fail on new findings only. Trivy is the one vulnerability matcher.
- One shared action signs every image and chart with the release key and attests a bill of materials.

### 11. Execution protocol

- The plan is data. `docs/ledger/plan.yaml` holds every work package: what it depends on, the paths it may touch, its precondition, its acceptance commands, its size. Only the architect changes it, in a pull request whose body carries `Work-Package: PLAN`.
- A work package is one pull request. Its ticket, `docs/ledger/plan/tickets/<ID>.md`, is the complete instruction. The executor follows it literally and makes no design decision.
- Sizes are budgets: XS is 5 files and 150 changed lines, S is 15 and 600, M is 60 and 3,000. Larger work is an epic that lists its packages in order.
- CI judges every work-package pull request with three checks: `work-package / scope` (only the allowed paths changed, the budget is kept, the dependencies are merged), `work-package / precondition` and `work-package / acceptance`.
- Evidence is a CI record or a gate record. Output from a terminal is not evidence.
- The state of a package is derived from the repository: a marker file under `docs/ledger/plan/done/`, a wrong report under `docs/ledger/plan/wrong/`, a branch `wp/<ID>`. It is stored nowhere else.
- A ticket that cannot be followed as written is reported as wrong with facts and is not repaired by the executor. The architect amends it and raises its revision.
- The architect hands out every ticket, reviews every pull request and merges it. The executor never merges. One session executes one ticket.
- Single-writer files are locks in the plan, among them the shared boot file, the bootstrap-kit slots, a provider's main file, the API root, the console router, the Blueprint files and the custom resource definitions. One open package holds a lock at a time.

## Alternatives rejected

- **A fixed list of checks that must all pass before anything merges.** Many of the checks need work that is not built, so the gate could not gate the work that makes it pass.
- **Gate levels that open waves.** A level holds every area behind the slowest check.
- **Keeping the override token and the report mode.** They existed because the old gate could stop its own fix. The new place of the gate removes the reason.
- **An integration branch, with `main` moved only by a cloud run.** Most workflows and every bot writer are keyed on `main`, and one red cloud run would stop all integration. The merge queue keeps `main` green on hermetic checks; the release waits for the cloud.
- **Deleting the ledger.** Its rows that name a console element are a useful regression list.
- **A separate conformance product with its own controller.** A second suite would drift from the gate.
- **Freezing by a sentence in a document.** A rule that nothing can fail is not a rule.
- **Status edited by hand in the plan file.** Every executor would edit one file, which is the version collision again.

## Consequences

- A capability counts as done when its check is `required` and green by the pass rule. A merged pull request alone does not make it done.
- A red check stops the work that depends on it and nothing else.
- Once FND-011 and FND-012 are merged, `main` carries no tracker commits, and no pull request collides with another on a version line.
- The content of the plan, the manifest, the register and the decision records is the architect's. An executor only applies text that a ticket carries. A code-owner review enforces this once the ruleset of the merge queue is applied (FND-013, FND-017).
- Backup and restore, and the observability pipeline, are not decided here. Records 0036 and 0035 are allocated to them.

## Delivered by

| Part | Packages |
|---|---|
| Plan file, plan tool, scope fence, CI-run acceptance | FND-021, FND-022 |
| Ticket form, its linter, the executor rules in `docs/PROTOCOL.md` | FND-023 |
| Gate manifest and verdict tool; the mode and the override removed | FND-031 |
| Gate record version 2, check registry, the checks, the gate workflow | FND-032 to FND-036 |
| Candidate ref; a deployment may name the ref it follows | FND-015, FND-014 |
| CI writes versions; tracker commits stop; merge queue; release-writer identity | FND-011, FND-012, FND-013, FND-017 |
| Stability register and the frozen-path check; the topology freeze | FND-040, FND-120 |
| Baseline, manifest set from it, flight checklist, gate runs on the standing environment | FND-070, FND-080, FND-071, FND-073 |
| Suite on the local provider pack; the release as a workflow; self-conformance | FND-130, FND-141, FND-142, FND-144, FND-151, FND-152 |
| Test, lint and signing rails | FND-051 to FND-057, FND-061, FND-062 |
| Deletions of this area and the deleted-paths guard | FND-091 to FND-099, FND-171, FND-173 |
| Generated inventories, state markers, control matrix, document corrections | FND-101 to FND-105 |
| Console regression as browser tests; the hand-walk tooling retired | FND-161 to FND-166 |

When this decision record was written, eight of these packages were merged: FND-021, FND-022, FND-031, FND-032, FND-014, FND-051, FND-052 and FND-053. The state of every package is printed by `python3 scripts/plan.py state`.
