# Engineering Principles

> **What this is:** the inviolable engineering rules for OpenOva platform work, plus the catalog of anti-patterns we've caught and how to recognize them.
>
> **Authority:** Permanent canon. Reviewed PRs only. Generic cross-project engineering principles live in user-global `~/.claude/CLAUDE.md` §3-§4 — this file holds only what is OpenOva-platform specific or refines a generic rule for this codebase.
>
> **Pointers:**
> - [`docs/DOD.md`](DOD.md) — the end-user Definition of Done (the 5 pillars + Phase 0/1/2 deterministic test).
> - [`docs/ARCHITECTURE.md`](ARCHITECTURE.md) — the system shape this work has to compose into.
> - [`docs/GLOSSARY.md`](GLOSSARY.md) — terminology source of truth.
> - [`docs/ledger/TRUST.md`](TRUST.md) — verification ledger for claimed-done surfaces.

The hard rule: **never do the same violation twice.** If a future task tempts you to violate any principle here, the answer is *stop and re-read this file*, not "I'll just do it this once."

---

## Part I — Inviolable Principles (1-15)

### 1. The waterfall is the contract

This is a waterfall delivery, not iterative MVP. Every deliverable lands in its full target-state shape, not in an incremental "we'll improve it later" form.

| Forbidden | Required |
|---|---|
| "Let me ship this MVP first; we'll refactor later" | Ship the target-state shape, the first time |
| "I'll add a stub here and TODO it" | Write the real implementation |
| "Mock for now, wire to real backend after" | Wire to the real backend now |
| "Iterate" | Deliver |
| "Phase 1 of 3" framing as an excuse to descope | Phases are sequencing, not subsetting |

**Trigger phrase that means you're about to violate this:** "for now, ..." → STOP. Replace with the real solution.

---

### 2. Never compromise from quality

We never compromise from functional and non-functional design principles. We solve architecturally; we do not work around. The platform must remain an exemplary architecture and code base.

Quality compromises that have happened and must never happen again:

- Picking a "simpler" path that diverges from the documented architecture without flagging the divergence.
- Shipping bespoke code that duplicates what an off-the-shelf component (OpenTofu, Crossplane, Flux) is designed to do.
- Using direct API calls when the architecture says "use the IaC layer."
- Picking SME-vs-corporate dual-shape design instead of finding the unified primitive.
- Making any decision that scales by special case rather than by configuration.

**Test before you ship:** "Does this match the canonical doc's design exactly, or did I quietly substitute something simpler?" If you substituted, stop, revert, and do it the canonical way — or explicitly raise the design change before shipping.

---

### 3. Follow the documented architecture, exactly

The architectural docs (`ARCHITECTURE.md`, `SOVEREIGN-PROVISIONING.md`, `RUNBOOKS.md`, `ARCHITECTURE.md`, `SECURITY.md`, `ARCHITECTURE.md`, `GLOSSARY.md`) are the design contract, not aspirational suggestions.

Specifically for provisioning:

- **OpenTofu provisions Phase 0 cloud resources.** Not bespoke Go calling cloud APIs. Not Pulumi. OpenTofu.
- **Crossplane is the ONLY IaC after Phase 1 hand-off.** Not direct provider SDKs. Not Terraform. Not the `catalyst-api` Go service calling cloud APIs.
- **Flux is the ONLY GitOps reconciler.** Not bespoke kubectl/helm exec calls. Not ArgoCD. Not shelling out to `helm`.
- **Blueprints are the ONLY install unit.** Every install lands as a `bp-<name>:<semver>` OCI artifact reconciled by Flux. Not direct `helm install`. Not `kubectl apply`. Not `go-helm-client` library calls.

If you find yourself writing Go code that calls cloud APIs directly, calls `exec.Command("helm", ...)`, or constructs k3s install scripts inline: stop. That's an architectural violation. The right path is an OpenTofu module, a Crossplane Composition, a Flux Kustomization, or a `bp-` Blueprint.

---

### 4. Never hardcode

Hardcoded values are forbidden. If a value can be picked at runtime, it is configuration — not code.

| Hardcoded | Right way |
|---|---|
| Region pinned to `fsn1` in code | Region passed in from wizard at runtime; OpenTofu variable |
| Helm chart version `1.16.5` literal in install function | Read from `platform/<name>/chart/values.yaml` `catalystBlueprint.upstream.version` field, or from a Crossplane Composition |
| URL `console.openova.io` baked into source | `src/lib/config.ts`, runtime-loaded |
| API endpoint `https://api.hetzner.cloud/v1/...` baked in | Provided by a Crossplane provider config — Crossplane is the layer that knows about cloud APIs, never our Go code |
| K3s flags `--disable=traefik --flannel-backend=none` baked in | OpenTofu variable + `templatefile()` + cloud-init template, parameterized for the Sovereign's chosen topology |

---

### 5. Continuous execution; commit progress instead of stopping

The user's 24-hour-no-stop mandate is operational, not rhetorical. Continue working through context-fill, queue-saturation, and intermediate fatigue. Commit incrementally so a fresh session can resume cleanly.

The only legitimate reasons to pause:

1. The user explicitly says stop.
2. You hit a hard system error (CI broken, repo locked) where waiting on a human is the only path forward — and even then, you commit progress and document the blocker, you do not summarize.

Self-protection ("I might make errors as context fills") is not a legitimate reason. Commit progress and continue.

---

### 6. Ticket discipline is non-negotiable

Every piece of work flows through a GitHub issue (`/issue` first, per user-global `~/.claude/CLAUDE.md` §1).

- A long open-ticket count is not a signal to "scope down" — it is the actual scope.
- Updating ticket bodies with "committed at SHA <abc>" matters. The Kanban board is the source of truth.
- Issue lifecycle: `status/in-progress` → `status/uat` → `status/completed`. Only the user closes issues, and only after operator-walk-with-screenshot evidence lands on the issue itself.

If you find yourself writing an executive summary instead of working through the ticket list, that is a violation. The list is the work. Work the list.

---

### 7. Verify before claiming done

DoD E2E 2-pass GREEN on the current deployed SHA is the only valid proof of done. CI-green plus pods-running is not enough.

- "I built X" requires X to actually run end-to-end. Not "X compiles." Not "X is committed." Not "the structure is there."
- "Real provisioning" requires a real Hetzner project, a real provisioning run, and a working Sovereign at the end of it.
- A blueprint claim requires the blueprint to actually install its upstream, apply Catalyst values, and produce a working component.

Before claiming done in any user-visible message, verify the claim is true. If you cannot verify, say "structurally complete, runtime-untested" — never imply working when it is not.

---

### 8. Disclose every divergence

If you decide to deviate from the documented design — for any reason — you must disclose the deviation in the same message where you do it. Not in the next message. Not when asked. In the same message.

The cost of an undisclosed substitution is always greater than any time saved by it. Disclose, every time.

---

### 9. Do not invent ticket-flavored excuses

When stuck, the temptation is to write a long explanation of why the work is hard. This is bargaining, not engineering.

Do the work. If it is truly stuck (genuine blocker, not "I'm tired"), commit progress and document the *specific* blocker — e.g. "`tofu apply` hangs because hcloud API key permissions lack `network:write` — need user to update token scopes." Never abstract complexity narratives.

---

### 10. The principles override session-internal judgment

If your in-context reasoning says "I should compromise principle N for reason R" — do not compromise. The reasoning is wrong. Either:

1. Find a way to do it without compromising (the principles are tighter than they look until you really try).
2. Stop and ask the user explicitly, before shipping anything that violates a principle.

---

### 11. Sovereigns must be independent of openova-io after handover

A franchised Sovereign cannot be operationally tethered to the OpenOva mothership after handover. The whole franchise model collapses if every customer cluster keeps pulling images through `harbor.openova.io`, reconciling Flux from `github.com/openova-io/openova`, fetching HelmRepositories from `oci://ghcr.io/openova-io`, or carrying `catalyst-api` env defaults that fall back to OpenOva-owned URLs. A customer that retains those tethers is hosting an OpenOva replica, not running their own Sovereign — and we cannot truthfully market it as franchised.

**Trigger phrase that means you're about to violate this:** "for now, the Sovereign can keep using `harbor.openova.io`" → STOP. Same for "Flux can stay pointed at `github.com/openova-io/openova`," "we'll mirror to local Gitea later," or "the helm-repo URLs can be swapped in a follow-up." Each of those turns a franchise into a managed replica.

**The only acceptable temporary tether is the cold-start window.** During Phase 0 + Phase 1 provisioning, routing image pulls through the mothership Harbor proxy is acceptable (and intentional) — it absorbs `docker.io`'s 100-pull / 6h anonymous rate limit, which would otherwise produce flaky `ImagePullBackOff` failures during the 10–30 minute provisioning window. That cold-start tether is the only sanctioned exception.

**How:** Every Sovereign runs `bp-self-sovereign-cutover` after handover. The chart installs dormant at bootstrap-kit slot 06a during Phase 1 and is triggered post-handover by the sovereign-admin's "Achieve True Sovereignty" button (or by `catalyst-api` auto-fire on first login if explicitly enabled per-customer). Eight sequential Jobs pivot the eight tethers in dependency order; the final step is a 10-minute deny-egress NetworkPolicy hold against `github.com`, `ghcr.io`, and `harbor.openova.io`. **The only condition under which `cutoverComplete=true` is set is that the cluster reconciles green during this hold.** No cutover claim without the egress-block proof.

Full architecture, alternatives considered, and consequences live in [ADR-0002](adr/0002-post-handover-sovereignty-cutover.md). The eight-tether map is in `ARCHITECTURE.md` §11.1.

There is no "Sovereign-lite" tier where independence is optional. A ticket that ships a Sovereign without the cutover wired up is wrong by definition until amended.

---

### 12. Never validate against the local working tree

When verifying that a change has landed, that a file exists, or that a principle is documented, read the artifact from `git show origin/main:<path>` or from a fresh clone — never from the local working tree.

The working tree is a hypothesis; `origin/main` is the contract. A verifier script that reads from `$PWD` is broken by construction — fix it to pipe through `git show origin/main:` before trusting its output. A worktree pinned to `origin/main` (e.g. populated by `git worktree add ... origin/main`) is also acceptable.

Never grep the developer's working copy to prove something is "already on main."

---

### 13. Chart-pin bumps must match a GHCR tag that actually exists

Whenever a bootstrap-kit slot or chart manifest bumps a pin (e.g. `bp-foo:0.1.25 → 0.1.26`), the new tag MUST already be published to GHCR before the pin lands on main. Source-side version-equality is not proof of publication.

How to apply:

- Before committing a pin bump, run `crane manifest oci://ghcr.io/openova-io/openova/bp-<name>:<new-version>` (or `oras manifest fetch`) and confirm a non-empty response.
- In CI, gate the deploy commit on a "tag exists on GHCR" check. A build job's success is necessary but not sufficient — the registry must serve the artifact.
- If you find a pin pointing at a missing tag on `origin/main`, treat it as a P0 incident: either publish the tag immediately or revert the pin. Do not paper over with "the build is queued."

---

### 14. Cutover-style HRs that rewrite HelmRepository URLs must `dependsOn` Gateway readiness

Any HelmRelease that pivots a HelmRepository URL from the mothership/upstream to a local in-cluster registry (Harbor, Gitea, local OCI mirror) MUST declare a `spec.dependsOn` on the Gateway / IngressClass that fronts the local registry, AND a health-check on that Gateway's TLS endpoint. Cutover-style HRs that flip URLs without proving the destination is reachable will deadlock the cluster.

How to apply:

- The HR that performs the URL flip lists `dependsOn` for the Cilium Gateway (or whatever ingress fronts the local registry) and its certificate Secret.
- The flip Job includes a readiness probe against the local registry's TLS endpoint (`https://harbor.<sovereign-fqdn>/v2/`) and refuses to flip until the probe is green.
- The cutover chart publishes a rollback Job — flipping HelmRepository URLs is destructive in the same sense `tofu destroy` is; treat it accordingly.
- Never sequence "flip URLs" before "prove Gateway TLS works" inside the same blueprint. If they share a slot, the slot is wrong.

**Critical sub-rule:** `HelmRelease.spec.dependsOn` references ONLY other HelmReleases. It cannot reference Flux `Kustomization`s or other resource kinds. If you need to gate a HelmRelease on a Kustomization, ship a "wait-HelmRelease" (tiny chart with a Job that runs `kubectl wait …`) and depend on THAT HR — or move the gated workload into a Kustomization with cross-kind `dependsOn`.

Empirical verbatim from helm-controller when this rule is violated:

```
unable to get 'flux-system/<name>' dependency: helmreleases.helm.toolkit.fluxcd.io "<name>" not found
```

It retries every 30 s forever and never resolves.

---

### 15. Validate IaC changes with the IaC evaluator, not a JSON / jq / Python simulation

For `.tf` / `.tftpl` changes, validate with real `tofu validate` (or `tofu plan`). For Helm chart changes, validate with real `helm template` (or `helm install --dry-run`). For Kubernetes manifests, validate via `kubectl apply --dry-run=server` against a cluster known to lack the CRDs (or via `helm install` from scratch on Kind).

Python `jsonencode()` simulations, `jq` greps, and hand-rolled "I rendered the template in my head" checks are insufficient — they accept structurally-different shapes that the IaC evaluator's stricter type system rejects at plan-time. A Python `json.dumps` of a dict-shaped value will happily emit any structure; HCL's evaluator unifies types across ternary branches and refuses heterogeneous shapes that Python never noticed.

How to apply:

- Edit `.tf`? Run `tofu init && tofu validate` (or `tofu plan` if vars are available) in the same workdir before opening the PR. Paste the `Success! The configuration is valid.` line into the PR description.
- Edit a `.tftpl`? Render it through tofu's `templatefile()` in a throwaway `.tf` (or via `tofu console`), THEN run `tofu validate` against the consuming module. Don't simulate `templatefile` in Python.
- Edit a Helm template? Run `helm template <chart> --values <prod-values>` AND pipe the output through `kubectl apply --dry-run=server -f -` against a real kube-apiserver. Don't eyeball the rendered YAML.
- Edit a CRD-conformant manifest? Let the kube-apiserver validate via `--dry-run=server` — NOT `--dry-run=client`, which skips schema checks.
- The PR description must show the validator's actual stdout, not a sentence describing what you expected it to say.

**Anti-pattern:** "I ran `python3 -c 'import json; print(json.dumps({...}))'` and it parsed, so the tftpl is correct." This is theater — the JSON parser is not the HCL evaluator.

---

## Part II — Anti-pattern catalog (A1-A15)

These are OpenOva-platform-specific anti-patterns — concrete PR / issue receipts of failure modes that have shipped, hidden in plain sight in diffs, or compounded into theater. Each entry exists so the *next* review of the same shape catches the bug before it ships.

**How to use this catalog during PR review:** scan the diff for the *shape* of each anti-pattern below. Defensive-coding patterns are the canary — they almost always mean something upstream is broken and the diff is bandaging the symptom. Per user-global `~/.claude/CLAUDE.md` §3, defensive-coding patterns trigger investigation, NOT approval.

### A1 — Null-guard after empty-data crash

| | |
|---|---|
| **Receipt** | PR #1185 (Compliance tab) |
| **Shape** | Dashboard crashes when upstream data is empty → diff adds a `?? []` / `?? {}` / `cursor: 'default'` guard so the empty-state renders politely. |
| **Why this is wrong** | The guard hides the upstream break. The right question is "why is the data empty?" A working Sovereign should produce data. The guard turns a loud failure into a silent zero-state. |
| **Right fix shape** | Trace the upstream pipeline. The empty data is the bug; rendering an empty state for it is the theater. |

### A2 — Feature-flag off by default

| | |
|---|---|
| **Receipt** | PR #1138 (`bp-kyverno` `values.yaml` — 18 of 19 compliance ClusterPolicies gated off) |
| **Shape** | A blueprint's `values.yaml` ships every feature with `enabled: false` and is then claimed as "shipped." Sovereigns provision with the feature absent; nobody notices because no test asserts the feature is on. |
| **Why this is wrong** | "Installed" ≠ "applied." Kyverno without its policies enforces nothing; the Compliance tab reports 0/19 forever. |
| **Right fix shape** | Defaults must match the deterministic test contract. If the test expects "19 policies enforcing," the blueprint default must ship 19 enabled. Feature-off defaults need an explicit operator-visible toggle, not silent absence. |

### A3 — Click handler missing on leaf cells

| | |
|---|---|
| **Receipt** | PR #1085 (treemap drill-down) |
| **Shape** | A 100-line diff with a 3-line bug visible in plain context. The `onClick` handler is wired on container cells but not on the leaf cells the operator actually needs to click. PR is approved on the "looks good" of the chrome. |
| **Why this is wrong** | The bug is in the diff. Reviewer-fatigue defense — "this PR is too big to read every line" — is dead. If a PR is too big to review carefully, split it before merging. |
| **Right fix shape** | Slow down, read every line. Click handlers must terminate at the leaf that triggers the action. |

### A4 — `enabled: !wizardApp` query gate

| | |
|---|---|
| **Receipt** | PR #1160 (AppDetail page — fetch gated by `!wizardApp`) |
| **Shape** | The data fetch is gated by a boolean computed from the previous step (`wizardApp`, `isLoading`, `isReady`, etc.). For some real operator paths the boolean is truthy in a way that keeps the fetch permanently off. |
| **Why this is wrong** | Gating-on-other-state is fragile. The query should run when the inputs are present, not when an upstream sibling state happens to be falsy. |
| **Right fix shape** | The query's `enabled` predicate should be a positive assertion on the inputs the query actually needs (`!!appName && !!targetNamespace`), not a negative assertion on an unrelated piece of state. |

### A5 — `Closes #N` on a scaffold-only PR

| | |
|---|---|
| **Receipt** | PR #1918 (NATS scaffolding — interface + env-driven constructor that returns nil) |
| **Shape** | The PR adds the type definitions, the constructor wiring, and the env-var lookup — but the constructor returns nil when the env var is unset (default), so production gets the no-op. PR uses `Closes #N`; the issue auto-closes on merge. |
| **Why this is wrong** | The platform behavior the issue tracked does not change when the PR merges. The issue should stay open until the concrete binding ships. `Refs #N` is the default in PR bodies, not `Closes #N`. Auto-close on merge is the enemy. |
| **Right fix shape** | Use `Refs #N` until a follow-up PR ships the binding that makes the surface produce the operator-visible behavior. Reopen the issue if it auto-closed. |

### A6 — `kubectl --dry-run=server` against a running cluster

| | |
|---|---|
| **Receipt** | PR #1933 (`bp-kyverno` install) |
| **Shape** | PR claims "validated against `kubectl apply --dry-run=server`" — but the dry-run was run against a cluster where the relevant CRDs were already installed (from previous reconciliations). The dry-run passes; the fresh-prov install fails because the CRDs aren't there yet. |
| **Why this is wrong** | `kubectl --dry-run=server` against a running cluster lies when the CRDs the manifest references are pre-installed. The cluster's schema check accepts the manifest; a fresh cluster wouldn't. Validate against fresh state, not stable state. |
| **Right fix shape** | Chart / CRD / bootstrap-kit changes MUST be validated by `helm install` from scratch (Kind, fresh prov), or by `kubectl apply --dry-run=server` against a cluster known to lack the CRDs. See Principle §15. |

### A7 — Multi-region claim on a single-region prov

| | |
|---|---|
| **Receipt** | PR #1599 (treemap fan-out) |
| **Shape** | PR claims "renders multi-region correctly." The prov used to validate has a single region. The single-region path masquerades as multi-region success because the fan-out is a no-op when N=1. |
| **Why this is wrong** | Multi-region BCP is a 5-Pillar DoD pillar. Tests on a single-region prov cannot prove it works. Validate on the topology the PR claims to support. |
| **Right fix shape** | Multi-region claims must be validated on a multi-region prov (N≥2 regions, ideally N=3 per [`DOD.md`](DOD.md) A1). The PR body must cite the prov ID + region count + per-region pass/fail. |

### A8 — `must_contain` token-passing test churn

| | |
|---|---|
| **Receipt** | PRs #1362, #1366, #1371, #1378 |
| **Shape** | A test asserts `expect(page.locator('body')).toContainText('Voucher issued')`. Earlier tests fail; later tests "pass" because someone added the literal string `Voucher issued` to a status banner that renders regardless of whether anything happened. |
| **Why this is wrong** | The test no longer measures what it claims to measure. The pass condition has been moved from "the operation succeeded" to "the right string appears." |
| **Right fix shape** | Behavior tests must measure behavior (the DB row was written, the XHR returned 200 with a non-empty body, the downstream resource exists). Token-passing tests must be deleted, not patched. |

### A9 — Chart-publish break via `Chart.yaml` corruption

| | |
|---|---|
| **Receipt** | PR #1932 → cleanup PR #1937 |
| **Shape** | An auto-applied edit prepends YAML to `Chart.yaml` and deletes the existing `apiVersion:` and `name:` keys in the process. CI's chart-publish job fails, but the failure is in a pre-existing-red queue that has been bypassed. |
| **Why this is wrong** | `Chart.yaml` is the chart's identity. No admin-merge through red CI. "Pre-existing failure" is not a bypass. |
| **Right fix shape** | Fix the failing CI check *before* the next PR rides on top of it. Audit every edit that touches `Chart.yaml` for required-keys preservation. |

### A10 — Walker reports a verdict for a surface it never navigated to

| | |
|---|---|
| **Receipt** | Walker commit `ad94ebe2` (caught by audit commit `af4537d2`) |
| **Shape** | A Playwright walker reports `D27 PASS — marketplace renders` but the API-request log shows the walker never issued a request to `/marketplace`. The verdict is fabricated by a code path that conflates "no error" with "test passed." |
| **Why this is wrong** | The walker's job is to *prove* the surface works by interacting with it. A walker that doesn't navigate cannot produce a verdict. Verification agents are READ-ONLY and their output is screenshot + comment + ledger row only — fabricated verdicts are worse than no verdict. |
| **Right fix shape** | Every walker assertion must be backed by a captured XHR (HAR file) or a captured screenshot at the relevant route. No XHR + no screenshot = no verdict. |

### A11 — `HelmRelease.spec.dependsOn` referencing a non-HR resource

| | |
|---|---|
| **Receipt** | PR #1875 (cutover deadlock fix) |
| **Shape** | A `HelmRelease.spec.dependsOn` lists a `Kustomization` (or any non-HR kind). `helm-controller` silently does nothing with the cross-kind reference; the dependency is never enforced. The HR proceeds out-of-order and deadlocks the cluster. |
| **Why this is wrong** | `HelmRelease.spec.dependsOn` references only other HelmReleases. Empirical verbatim from `helm-controller` on violation: `unable to get 'flux-system/<name>' dependency: helmreleases.helm.toolkit.fluxcd.io "<name>" not found` — retries every 30 s forever, never resolves. |
| **Right fix shape** | For cross-kind ordering, use `Kustomization.spec.dependsOn` (which *does* support cross-kind), or ship a "wait-HelmRelease" (tiny chart with a Job that runs `kubectl wait …`) and depend on that HR. See Principle §14. |

### A12 — Chart-pin bump to a tag that doesn't exist on GHCR

| | |
|---|---|
| **Receipt** | PR #1869 (`bp-self-sovereign-cutover:0.1.4` pinned before the chart was published) |
| **Shape** | A `HelmRelease.spec.chart.spec.version` is bumped to `<X.Y.Z>` and committed to main while the OCI artifact at `oci://ghcr.io/.../bp-<name>:<X.Y.Z>` has never been built. Source-side version-equality (`Chart.yaml` says `0.1.4`) is not proof of publication. |
| **Why this is wrong** | Flux retries `ImagePullBackOff` forever; the cluster looks half-rolled-out. The pin commits to main pointed at a tag that doesn't exist. |
| **Right fix shape** | Before committing a pin bump, `crane manifest oci://ghcr.io/openova-io/openova/bp-<name>:<new-version>` must return a non-empty response. CI should gate the deploy commit on a "tag exists on GHCR" check. See Principle §13. |

### A13 — Python `jsonencode()` simulation passed off as `tofu validate`

| | |
|---|---|
| **Receipt** | PR #1892 → real fix PR #1894 (listener wildcard depth) |
| **Shape** | PR description says "verified via Python `json.dumps()` simulation." But tofu HCL's type-unification rule rejects the ternary expression at plan-time. A Python `json.dumps` of a dict will happily emit any structure; HCL's evaluator refuses heterogeneous shapes that Python never noticed. |
| **Why this is wrong** | The JSON parser is not the HCL evaluator. Simulating IaC with a foreign language's tooling is theater. |
| **Right fix shape** | For `.tf` / `.tftpl` changes, run `tofu init && tofu validate` (or `tofu plan` if vars are available) in the same workdir before opening the PR. Paste the `Success! The configuration is valid.` line into the PR description. See Principle §15. |

### A14 — Bulk-template "not-on-pillar-path" closure overriding live evidence

| | |
|---|---|
| **Receipt** | Issues #1741, #1819, #1882 |
| **Shape** | An issue is closed with a canned "not on pillar path" comment despite an earlier comment on the same issue containing concrete still-failing evidence (`sme-gateway-unreachable`, "Keeping open; eligible for the cosmetic-cleanup batch," etc.). |
| **Why this is wrong** | The bulk-template comment is a productivity prop, not a closure rationale. A closure must reference concrete evidence that the acceptance criteria are met — not "this isn't pillar work right now." |
| **Right fix shape** | Reopen the issue. Either fix the surface (closure with evidence) or label it `status/parked` and leave it open. Parking is a legitimate state; theater-closure is not. |

### A15 — Stable-state walk passed off as fresh-prov walk

| | |
|---|---|
| **Receipt** | Issue #1819 (DoD D18 closed citing a treemap-renders observation when the issue body required a next-fresh-prov zero-touch walk) |
| **Shape** | An issue's acceptance criteria explicitly say "passes zero-touch on the next fresh provision." The closure cites a walk on an existing long-running prov where state has accumulated through prior remediations. The behavior the AC tests is not reproducible from scratch. |
| **Why this is wrong** | "Works on a stable prov" ≠ "works zero-touch on a fresh prov." The whole point of fresh-prov DoD is to catch environment-hardcoding and remediation-debt that hide on long-running provs. |
| **Right fix shape** | Walks for fresh-prov AC items must be conducted on a fresh prov (the prov ID, chart version, and verified-at timestamp must all appear in the closing comment). |

---

## Cross-cutting flags that trigger a "stop and investigate" pass

Any of the following in a diff is *not* approval — it is a clue to hunt the upstream cause:

- Null-guards on data that the spec says should never be empty.
- `?? 'default'` / `?? []` / `?? {}` fallbacks on values the upstream is supposed to produce.
- `enabled: false` defaults on features the deterministic test asserts present.
- `cursor: 'default'` style guards on interactive surfaces.
- Snapshot-empty-frames (test fixtures that assert "no items rendered").
- `must_contain` token-passing tests (see A8).
- "Pre-existing red" CI bypasses (see A9).
- `Closes #N` on a PR that does not produce operator-visible behavior change (see A5).
- Test data containing `openova.io`, `Nova Cloud`, `eventforge.io` (see [`DOD.md`](DOD.md)).

---

## Banned words (founder direction 2026-06-01)

The following words are **banned from every commit message, PR body,
issue title, issue body, ledger row, comment, code identifier, and
chat reply**. We are in the final stage of the product — every
artifact must carry the language of a shipping product, not the
language of a backlog or a debate:

- **MVP** — there is no "minimum viable" product. We ship the ultimate
  product. If the canonical surface requires N features, all N ship.
- **Iteration** — refactor or "next pass" is not work; **target-state shape
  ships in one PR** (per Principle 1 + 14). If something is genuinely a
  multi-PR ship, label it as `chain` or `series`, not "iteration".
- **out of scope** — every gap is in scope. The only legal response to
  a gap is "ship it" or "ship a follow-up PR". Never "out of scope".
- **Blocker** — there are no blockers. There are problems with
  identified next actions. If you find yourself wanting to write
  "blocked on X", write "next action: do X" instead.

Each banned word has a canonical replacement listed above. The CI
guard at `scripts/check-no-banned-words.sh` (planned) will fail any
commit/PR containing them. Until that lands, reviewers and dispatching
leads flag them at PR-comment time.

This list is **additive only** — words enter, they do not leave.

---

## Part III — How these relate

Part I (Principles 1-15) defines *what to do*. Part II (anti-patterns A1-A15) is the catalog of *how violations manifest in PRs* — concrete shapes you can grep for during review.

The generic cross-project anti-theater rules (defensive-coding-as-clue, `Refs #N` vs `Closes #N`, no admin-merge through red CI, validate on fresh state, reviewer-fatigue defense is dead, verification agents are READ-ONLY) live in user-global `~/.claude/CLAUDE.md` §3. The catalog above is the OpenOva-platform-specific *application* of those generic rules to this codebase's surfaces (Catalyst control plane, Blueprints, Sovereign provisioning, cutover, deterministic test).

Mapping (Anti-pattern → Principle it violates):

| Anti-pattern | Violates |
|---|---|
| A1 (null-guard after empty crash) | §1 (waterfall, no "for now"), §7 (verify before claiming done) |
| A2 (feature-flag off by default) | §1, §7 |
| A3 (missing leaf-cell click) | §7 |
| A4 (`enabled: !wizardApp`) | §1, §2 (no workarounds) |
| A5 (`Closes #N` on scaffold) | §6 (ticket discipline), §7 |
| A6 (`--dry-run=server` against stable cluster) | §15 (real validators only) |
| A7 (multi-region claim on single-region prov) | §7 |
| A8 (`must_contain` token tests) | §7 |
| A9 (`Chart.yaml` corruption + red-CI bypass) | user-global §3 (no admin-merge through red CI) |
| A10 (walker fabricates verdict) | §7, user-global §3 (verification agents READ-ONLY) |
| A11 (`HelmRelease.dependsOn` non-HR) | §14 |
| A12 (chart-pin bump to missing tag) | §13 |
| A13 (Python sim passed off as `tofu validate`) | §15 |
| A14 (bulk-template closure overrides evidence) | §6, §7 |
| A15 (stable-state walk passed off as fresh-prov) | §7, user-global §2 DoD (operator-walk on fresh prov) |

When you catch a new shape of failure in a PR review, add an A16+ entry here with the PR / issue number, the shape, why it's wrong, and the right fix shape. The catalog grows so the next session catches the same shape sooner.

---

## Self-check before every commit

- Does this match the documented architecture exactly? (No bespoke when off-the-shelf is specified.)
- Are all values runtime-configurable? (No hardcoded regions, versions, URLs.)
- Did I disclose any divergence in the commit message?
- Is the work actually done, or did I write "scaffolding" and call it done?
- Did I scan the diff for the A1-A15 shapes?
- Is my PR body using `Refs #N` (default) rather than `Closes #N`?

---

*Part of [OpenOva](https://openova.io). Read this before doing anything.*
