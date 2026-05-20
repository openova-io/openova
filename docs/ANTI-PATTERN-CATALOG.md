# Anti-Pattern Catalog

**Status:** Authoritative. **Updated:** 2026-05-20.

This catalog records **OpenOva-platform-specific** anti-patterns — concrete PR
receipts of failure modes that have shipped, hidden in plain sight in PR diffs,
or compounded into theater. Each entry exists so the **next** review of the
same shape catches the bug before it ships.

For the engineering principles each anti-pattern violates, see
[`INVIOLABLE-PRINCIPLES.md`](INVIOLABLE-PRINCIPLES.md).
For the cycle-level retrospective these receipts feed, see the latest
`SESSION-*.md` and `trust-audit-*.md` docs in this directory.
For the verification ledger these flips back to `🔴 UNVERIFIED`, see
[`TRUST.md`](TRUST.md).

---

## How to use this file during PR review

When reviewing a PR, scan the diff for **the shape** of each anti-pattern
below. Defensive-coding patterns are the canary — they almost always mean
something upstream is broken and the diff is bandaging the symptom.

The hard rule (CLAUDE.md §4): **defensive-coding patterns trigger
investigation, NOT approval.**

---

## Catalog

### A1 — Null-guard after empty-data crash

| | |
|---|---|
| **Example** | PR #1185 (Compliance tab) |
| **Theater duration before catch** | 10 days |
| **Shape** | Dashboard crashes when upstream data is empty → diff adds a `?? []` / `?? {}` / `cursor: 'default'` guard so the empty-state renders politely. |
| **Why this is wrong** | The guard hides the upstream break. The right question is **"why is the data empty?"** A working Sovereign should produce data. The guard turns a loud failure into a silent zero-state. |
| **Right fix shape** | Trace the upstream pipeline. The empty data is the bug; rendering an empty state for it is the theater. |

### A2 — Feature-flag off by default

| | |
|---|---|
| **Example** | PR #1138 (`bp-kyverno` `values.yaml` — 18 of 19 compliance ClusterPolicies gated off) |
| **Theater duration before catch** | 11 days |
| **Shape** | A blueprint's `values.yaml` ships every feature with `enabled: false` and is then claimed as "shipped." Sovereigns provision with the feature absent; nobody notices because no test asserts the feature is *on*. |
| **Why this is wrong** | "Installed" ≠ "applied." Kyverno without its policies enforces nothing; the Compliance tab reports 0/19 forever. |
| **Right fix shape** | Defaults must match the deterministic test contract. If the test expects "19 policies enforcing," the blueprint default must ship 19 enabled. Feature-off defaults need an explicit operator-visible toggle, not silent absence. |

### A3 — Click handler missing on leaf cells

| | |
|---|---|
| **Example** | PR #1085 (treemap drill-down) |
| **Theater duration before catch** | 11 days |
| **Shape** | A 100-line diff with a 3-line bug visible in plain context. The onClick handler is wired on container cells but not on the leaf cells the operator actually needs to click. PR is approved on the "looks good" of the chrome. |
| **Why this is wrong** | The bug is in the diff. Reviewer-fatigue defense — "this PR is too big to read every line" — is dead. CLAUDE.md §4 rule 6: if a PR is too big to review carefully, split it before merging. |
| **Right fix shape** | Slow down, read every line. Click handlers must terminate at the leaf that triggers the action. |

### A4 — `enabled: !wizardApp` query gate

| | |
|---|---|
| **Example** | PR #1160 (AppDetail page — fetch gated by `!wizardApp`) |
| **Theater duration before catch** | 10 days |
| **Shape** | The data fetch is gated by a boolean computed from the previous step (`wizardApp`, `isLoading`, `isReady`, etc.). For some real operator paths the boolean is truthy in a way that keeps the fetch permanently off. |
| **Why this is wrong** | Gating-on-other-state is fragile. The query should run when the inputs are present, not when an upstream sibling state happens to be falsy. |
| **Right fix shape** | The query's `enabled` predicate should be a positive assertion on the inputs the query actually needs (`!!appName && !!targetNamespace`), not a negative assertion on an unrelated piece of state. |

### A5 — `Closes #N` on a scaffold-only PR

| | |
|---|---|
| **Example** | PR #1918 (NATS scaffolding — interface + env-driven constructor that returns nil) |
| **Theater duration before catch** | Issue auto-closed without the concrete binding shipping |
| **Shape** | The PR adds the type definitions, the constructor wiring, and the env-var lookup — but the constructor returns nil when the env var is unset (default), so production gets the no-op. PR uses `Closes #N`; the issue auto-closes on merge. |
| **Why this is wrong** | The platform behavior the issue tracked **does not change** when the PR merges. The issue should stay open until the concrete binding ships. CLAUDE.md §4 rule 1: **`Refs #N` is the default in PR bodies, not `Closes #N`.** Auto-close on merge is the enemy. |
| **Right fix shape** | Use `Refs #N` until a follow-up PR ships the binding that makes the surface produce the operator-visible behavior. Reopen the issue if it auto-closed. |

### A6 — `kubectl --dry-run=server` against a running cluster

| | |
|---|---|
| **Example** | PR #1933 (`bp-kyverno` install) |
| **Theater duration before catch** | Broke fresh-prov install the same day as the trust collapse it was a "fix" for |
| **Shape** | PR claims "validated against `kubectl apply --dry-run=server`" — but the dry-run was run against a cluster where the relevant CRDs were already installed (from previous reconciliations). The dry-run passes; the fresh-prov install fails because the CRDs aren't there yet. |
| **Why this is wrong** | `kubectl --dry-run=server` against a running cluster **lies** when the CRDs the manifest references are pre-installed. The cluster's schema check accepts the manifest; a fresh cluster wouldn't. CLAUDE.md §4 rule 2: validate against fresh state, not stable state. |
| **Right fix shape** | Chart/CRD/bootstrap-kit changes MUST be validated by `helm install` from scratch (Kind, fresh prov), or by `kubectl apply --dry-run=server` against a cluster known to lack the CRDs. See [`INVIOLABLE-PRINCIPLES.md`](INVIOLABLE-PRINCIPLES.md) §15. |

### A7 — Multi-region claim on a single-region prov

| | |
|---|---|
| **Example** | PR #1599 (treemap fan-out) |
| **Theater duration before catch** | Never tested on the topology it claimed to work on |
| **Shape** | PR claims "renders multi-region correctly." The prov used to validate has a single region. The single-region path masquerades as multi-region success because the fan-out is a no-op when N=1. |
| **Why this is wrong** | Multi-region BCP is Pillar 2. Tests on a single-region prov **cannot** prove Pillar 2 works. CLAUDE.md §4 rule 2: validate on the topology the PR claims to support. |
| **Right fix shape** | Multi-region claims must be validated on a multi-region prov (N≥2 regions, ideally N=3 per [`SOVEREIGN-MULTI-REGION-DOD.md`](SOVEREIGN-MULTI-REGION-DOD.md) A1). The PR body must cite the prov ID + region count + per-region pass/fail. |

### A8 — `must_contain` token-passing test churn

| | |
|---|---|
| **Examples** | PRs #1362, #1366, #1371, #1378 |
| **Theater duration before catch** | Tests pass on DOM that contains the right strings but no behavior |
| **Shape** | A test asserts `expect(page.locator('body')).toContainText('Voucher issued')`. Earlier tests fail; later tests "pass" because someone added the literal string `Voucher issued` to a status banner that renders regardless of whether anything happened. |
| **Why this is wrong** | The test no longer measures what it claims to measure. The pass condition has been moved from "the operation succeeded" to "the right string appears." See CLAUDE.md §4 anti-pattern catalogue. |
| **Right fix shape** | Behavior tests must measure behavior (the DB row was written, the XHR returned 200 with a non-empty body, the downstream resource exists). Token-passing tests must be deleted, not patched. |

### A9 — Chart-publish break via `Chart.yaml` corruption

| | |
|---|---|
| **Example** | PR #1932 → cleanup PR #1937 |
| **Theater duration before catch** | Caught the same day; chart publish was silently broken until #1937 |
| **Shape** | An auto-applied edit prepends YAML to `Chart.yaml` and deletes the existing `apiVersion:` + `name:` keys in the process. CI's chart-publish job fails, but the failure is in a pre-existing-red queue that's been bypassed. |
| **Why this is wrong** | `Chart.yaml` is the chart's identity. CLAUDE.md §4 rule 5: no admin-merge through red CI. "Pre-existing failure" is not a bypass. |
| **Right fix shape** | Fix the failing CI check **before** the next PR rides on top of it. Audit every edit that touches `Chart.yaml` for required-keys preservation. |

### A10 — Walker reports a verdict for a surface it never navigated to

| | |
|---|---|
| **Example** | Commit ad94ebe2 (caught by audit commit af4537d2) |
| **Theater duration before catch** | One audit cycle |
| **Shape** | A Playwright walker reports `D27 PASS — marketplace renders` but the API-request log shows the walker never issued a request to `/marketplace`. The verdict is fabricated by a code path that conflates "no error" with "test passed." |
| **Why this is wrong** | The walker's job is to **prove** the surface works by interacting with it. A walker that doesn't navigate cannot produce a verdict. CLAUDE.md §4 rule 7: verification agents are READ-ONLY and their output is screenshot + comment + ledger row only — fabricated verdicts are worse than no verdict. |
| **Right fix shape** | Every walker assertion must be backed by a captured XHR (HAR file) or a captured screenshot at the relevant route. No XHR + no screenshot = no verdict. |

### A11 — `HelmRelease.spec.dependsOn` referencing a non-HR resource

| | |
|---|---|
| **Example** | PR #1875 (TBD-A24 cutover deadlock fix) |
| **Theater duration before catch** | Deadlocked t27 until the empirical realization |
| **Shape** | A `HelmRelease.spec.dependsOn` lists a `Kustomization` (or any non-HR kind). helm-controller silently does nothing with the cross-kind reference; the dependency is never enforced. The HR proceeds out-of-order and deadlocks the cluster. |
| **Why this is wrong** | `HelmRelease.spec.dependsOn` references **only** other HelmReleases. Empirical verbatim from helm-controller on violation: `unable to get 'flux-system/<name>' dependency: helmreleases.helm.toolkit.fluxcd.io "<name>" not found` (retries every 30s forever, never resolves). |
| **Right fix shape** | For cross-kind ordering, use `Kustomization.spec.dependsOn` (which **does** support cross-kind), or ship a "wait-HelmRelease" (tiny chart with a Job that runs `kubectl wait …`) and depend on **that** HR. See [`INVIOLABLE-PRINCIPLES.md`](INVIOLABLE-PRINCIPLES.md) §14. |

### A12 — Chart-pin bump to a tag that doesn't exist on GHCR

| | |
|---|---|
| **Example** | PR #1869 (TBD-A48 drift incident — pinned `bp-self-sovereign-cutover:0.1.4` before the chart was published) |
| **Theater duration before catch** | Hours of `ImagePullBackOff` on every fresh prov |
| **Shape** | A `HelmRelease.spec.chart.spec.version` is bumped to `<X.Y.Z>` and committed to main while the OCI artifact at `oci://ghcr.io/.../bp-<name>:<X.Y.Z>` has never been built. Source-side version-equality (Chart.yaml says `0.1.4`) is **not** proof of publication. |
| **Why this is wrong** | Flux retries `ImagePullBackOff` forever; the cluster looks half-rolled-out. The pin commits to main pointed at a tag that doesn't exist. |
| **Right fix shape** | Before committing a pin bump, `crane manifest oci://ghcr.io/openova-io/openova/bp-<name>:<new-version>` must return a non-empty response. CI should gate the deploy commit on a "tag exists on GHCR" check. See [`INVIOLABLE-PRINCIPLES.md`](INVIOLABLE-PRINCIPLES.md) §13. |

### A13 — Python `jsonencode()` simulation passed off as `tofu validate`

| | |
|---|---|
| **Example** | PR #1892 (TBD-A32 listener wildcard depth) |
| **Theater duration before catch** | Every fresh prov failed at 23s into `tofu apply` until the real fix landed in #1894 |
| **Shape** | PR description says "verified via Python `json.dumps()` simulation." But tofu HCL's type-unification rule rejects the ternary expression at plan-time. A Python `json.dumps` of a dict will happily emit any structure; HCL's evaluator refuses heterogeneous shapes that Python never noticed. |
| **Why this is wrong** | The JSON parser is not the HCL evaluator. Simulating IaC with a foreign language's tooling is theater. |
| **Right fix shape** | For `.tf` / `.tftpl` changes, run `tofu init && tofu validate` (or `tofu plan` if vars are available) in the same workdir before opening the PR. Paste the `Success! The configuration is valid.` line into the PR description. See [`INVIOLABLE-PRINCIPLES.md`](INVIOLABLE-PRINCIPLES.md) §15. |

### A14 — Bulk-template "not-on-pillar-path" closure overriding live evidence

| | |
|---|---|
| **Examples** | #1741, #1819, #1882 (caught in `trust-audit-2026-05-20.md`) |
| **Theater duration before catch** | Issue closed with a still-failing walk-report 2 hours earlier on the same issue |
| **Shape** | An issue is closed with a canned "not on pillar path" comment despite an earlier comment on the same issue containing concrete still-failing evidence (`sme-gateway-unreachable`, "Keeping open; eligible for the cosmetic-cleanup batch," etc.). |
| **Why this is wrong** | The bulk-template comment is a productivity prop, not a closure rationale. CLAUDE.md §4 anti-theater rule: a closure must reference concrete evidence that the AC is met — not "this isn't pillar work right now." |
| **Right fix shape** | Reopen the issue. Either fix the surface (closure with evidence) or label it `status/parked` and leave it open. Parking is a legitimate state; theater-closure is not. |

### A15 — Stable-state walk passed off as fresh-prov walk

| | |
|---|---|
| **Example** | #1819 (DoD D18 closed citing a t22 treemap-renders observation, when the issue body required a NEXT-fresh-prov zero-touch walk) |
| **Theater duration before catch** | One audit cycle |
| **Shape** | An issue's AC explicitly says "passes zero-touch on the next fresh provision." The closure cites a walk on an existing long-running prov where state has accumulated through prior remediations. The behavior the AC tests is not reproducible from scratch. |
| **Why this is wrong** | "Works on a stable prov" ≠ "works zero-touch on a fresh prov." The whole point of fresh-prov DoD is to catch environment-hardcoding and remediation-debt that hide on long-running provs. |
| **Right fix shape** | Walks for fresh-prov AC items must be conducted on a fresh prov (the prov ID, chart version, and verified-at timestamp must all appear in the closing comment). |

---

## Cross-cutting flags that trigger a "stop and investigate" pass

Per CLAUDE.md §4 rule 4, any of the following in a diff is **not** approval —
it's a clue to hunt the upstream cause:

- Null-guards on data that the spec says should never be empty
- `?? 'default'` / `?? []` / `?? {}` fallbacks on values the upstream is supposed to produce
- `enabled: false` defaults on features the deterministic test asserts present
- `cursor: 'default'` style guards on interactive surfaces
- Snapshot-empty-frames (test fixtures that assert "no items rendered")
- `must_contain` token-passing tests (see A8)
- "Pre-existing red" CI bypasses (see A9)
- `Closes #N` on a PR that does not produce operator-visible behavior change (see A5)
- Test data containing `openova.io`, `Nova Cloud`, `eventforge.io` (see [`DOMAINS-CANON.md`](DOMAINS-CANON.md))

---

## How this catalog grows

Every PR-review that catches an instance of a known anti-pattern: log it
against the existing entry (a one-line addendum is fine).

Every PR-review that catches a **new** shape of failure: add an entry here with
the PR number, the shape description, why it's wrong, and the right fix shape.
The catalog grows so the next session catches it sooner.
