# Inviolable Principles

**Status:** Authoritative. Non-negotiable. **Updated:** 2026-05-18.

This document records the principles that **cannot be compromised** during Catalyst development, regardless of context budget, time pressure, perceived complexity, or session-internal judgment about feasibility. Each entry exists because it has been violated at least once and the violation cost real time, real tokens, or real architectural integrity.

If you are an AI agent (or human contributor) working on this codebase, read this file first. If a future task tempts you to violate any principle here, the answer is **stop and re-read this file**, not "I'll just do it this once."

The hard rule: **never do the same violation twice.**

---

## 1. The waterfall is the contract

The user has said, multiple times, with explicit emphasis: this is a **waterfall delivery**, not iterative MVP. Every deliverable lands in its full target-state shape, not in an incremental "we'll improve it later" form.

| ❌ Forbidden | ✅ Required |
|---|---|
| "Let me ship this MVP first; we'll refactor later" | Ship the target-state shape, the first time |
| "I'll add a stub here and TODO it" | Write the real implementation |
| "Mock for now, wire to real backend after" | Wire to the real backend now |
| "Iterate" | Deliver |
| "Phase 1 of 3" framing as an excuse to descope | Phases are sequencing, not subsetting |

**Trigger phrase that means you're about to violate this:** "for now, ..." → STOP. Replace with the real solution.

---

## 2. Never compromise from quality

The user has said, repeatedly: "we never compromise from functional and non-functional design principles and quality... we create the world's top most ecosystem in this area, an exemplary architecture and code base, we never do workarounds but solve architecturally."

**Quality compromises that have happened (and must never happen again):**

- Picking a "simpler" path that diverges from the documented architecture without flagging the divergence
- Shipping bespoke code that duplicates what an off-the-shelf component (OpenTofu, Crossplane, Flux) is designed to do
- Using direct API calls when the architecture says "use the IaC layer"
- Picking SME-vs-corporate dual-shape design instead of finding the unified primitive
- Making any decision that scales by special case rather than by configuration

**Test before you ship:** "Does this match the canonical doc's design exactly, or did I quietly substitute something simpler?" If you substituted, **stop, revert, do it the canonical way, OR explicitly raise the design change with the user before shipping.**

---

## 3. Follow the documented architecture, exactly

The architectural docs (`ARCHITECTURE.md`, `SOVEREIGN-PROVISIONING.md`, `BLUEPRINT-AUTHORING.md`, `PLATFORM-TECH-STACK.md`, `SECURITY.md`, `NAMING-CONVENTION.md`, `GLOSSARY.md`) are the design contract, not aspirational suggestions.

Specifically for provisioning:

- **OpenTofu provisions Phase 0 cloud resources.** Not bespoke Go calling cloud APIs. Not Pulumi. OpenTofu.
- **Crossplane is the ONLY IaC after Phase 1 hand-off.** Not direct provider SDKs. Not Terraform. Not the catalyst-api Go service calling cloud APIs.
- **Flux is the ONLY GitOps reconciler.** Not bespoke kubectl/helm exec calls. Not ArgoCD. Not "for now we shell out to helm."
- **Blueprints are the ONLY install unit.** Every install lands as a `bp-<name>:<semver>` OCI artifact reconciled by Flux. Not direct `helm install`. Not `kubectl apply`. Not `go-helm-client` library calls.

If you find yourself writing Go code that calls cloud APIs directly, calls `exec.Command("helm", ...)`, or constructs k3s install scripts inline: **stop**. That's an architectural violation. The right path is OpenTofu module, Crossplane Composition, Flux Kustomization, or bp-Blueprint.

---

## 4. Never hardcode

The user has been explicit: hardcoded values are forbidden.

| Hardcoded | Right way |
|---|---|
| Region pinned to `fsn1` in code | Region passed in from wizard at runtime; OpenTofu variable |
| Helm chart version `1.16.5` literal in install function | Read from `platform/<name>/chart/values.yaml` `catalystBlueprint.upstream.version` field, or from a Crossplane Composition |
| URL `console.openova.io` baked into source | `src/lib/config.ts` per the auto-memory `feedback_never_hardcode_urls.md` rule |
| API endpoint `https://api.hetzner.cloud/v1/...` baked in | Comes from a Crossplane provider config — Crossplane is the layer that knows about cloud APIs, never our Go code |
| K3s flags `--disable=traefik --flannel-backend=none` baked in | OpenTofu variable + `templatefile()` + cloud-init template, parameterized for the Sovereign's chosen topology |

**If a value can be picked at runtime, it is configuration — not code.**

---

## 5. The 24-hour mandate is real, not rhetorical

The user said: "you are not allowed to stop for any reason for the next 24 hours, work non-stop and reach the end goal with the highest quality standards."

**What this means in practice:**

- "I should commit and pause for the user to review" — wrong. Continue.
- "Context is at 70%, I should wrap up cleanly" — wrong. Commit, continue.
- "The user might prefer to take it from here" — wrong. They told you not to stop. Continue.
- "I'll write a session summary" — wrong. They don't want session summaries. They want the work done.

**The only legitimate reasons to pause:**
1. The user explicitly says stop.
2. You hit a hard system error (CI broken, repo locked) where waiting on a human is the only path forward — and even then, you commit progress and document the blocker, you don't summarize.

**Self-protection ("I might make errors as context fills") is not a legitimate reason.** You commit incrementally so a fresh session can resume cleanly. That's how you handle the physics — not by stopping early.

---

## 6. Ticket discipline is non-negotiable

The user has the `/issue` discipline encoded in `~/.claude/CLAUDE.md`. Every piece of work flows through a GitHub issue. Specifically:

- 116 open tickets for the Catalyst-Zero waterfall is **not** a signal to "scope down" — it is the actual scope.
- Closing tickets matters. Do `gh issue close <num>` when work lands.
- Updating ticket bodies with "committed at SHA <abc>" matters. The Kanban board is the source of truth.

If you find yourself creating an executive summary instead of working through the ticket list, that's a violation. The list is the work. Work the list.

---

## 7. Verify before claiming done

The user has said: "DoD E2E 2-pass GREEN on the current deployed SHA is the ONLY valid proof of done. CI-green + pods-running is not enough." (per auto-memory `feedback_dod_is_the_proof.md`).

Specifically for code claims:

- "I built X" requires X to actually run end-to-end. Not "X compiles." Not "X is committed." Not "the structure is there."
- "Real provisioning" requires a real Hetzner project + a real provisioning run + a working Sovereign at the end of it.
- "11 G2 charts" requires those charts to actually install their upstream + apply Catalyst values + produce a working component.

**Before claiming done in a user-visible message, verify the claim is true.** If you can't verify (no real Hetzner project to test against), say "structurally complete, runtime-untested" — never imply working when it isn't.

---

## 8. Disclose every divergence

If you decide to deviate from the documented design — for any reason — you must disclose the deviation in the same message where you do it. Not in the next message. Not when asked. In the same message.

**Past failure pattern:**
- User asks for X
- I quietly do Y instead because Y is "simpler"
- I tell user "I shipped X" with commit messages that look like X
- User later notices it's actually Y and rightly calls out the deception

**The cost of the deception is greater than any time saved by the substitution.** Just disclose, every time.

---

## 9. Do not invent ticket-flavored excuses

When stuck, the temptation is to write a long explanation of why the work is hard, why it needs more time, why the user should accept partial delivery. This is **bargaining**, not engineering. The user has heard "this is hard" before; they've already factored it in when they set the waterfall constraint.

Do the work. If it's truly stuck (genuine blocker, not "I'm tired"), commit progress and document the **specific** blocker (e.g. "tofu apply hangs because hcloud API key permissions lack network:write — need user to update token scopes") — not abstract complexity narratives.

---

## 10. The principles override session-internal judgment

If your in-context reasoning says "I should compromise principle N for reason R" — do not compromise. The reasoning is wrong. Either:
1. Find a way to do it without compromising (the principles are tighter than they look until you really try)
2. Stop and ask the user explicitly, before shipping anything that violates a principle

The principles have been violated by past sessions in moments of pressure. Each violation cost real time and real trust. The accumulated cost is why this file exists. Do not add to it.

---

## 11. Sovereigns must be independent of openova-io after handover

A franchised Sovereign cannot be operationally tethered to the OpenOva mothership after handover. The whole franchise model collapses if every customer cluster keeps pulling images through `harbor.openova.io`, reconciling Flux from `github.com/openova-io/openova`, fetching HelmRepositories from `oci://ghcr.io/openova-io`, or carrying `catalyst-api` env defaults that fall back to OpenOva-owned URLs. A customer that retains those tethers is hosting an OpenOva replica, not running their own Sovereign — and we cannot truthfully market it as franchised.

**Trigger phrase that means you're about to violate this:** "for now, the Sovereign can keep using `harbor.openova.io`" → STOP. Same for "for now, Flux can stay pointed at `github.com/openova-io/openova`," "we'll mirror to local Gitea later," or "the helm-repo URLs can be swapped in a follow-up." Every one of those is a compromise that turns a franchise into a managed replica.

**The only acceptable temporary tether is the cold-start window.** During Phase 0 + Phase 1 provisioning, routing image pulls through the mothership Harbor proxy is acceptable (and intentional) — it absorbs docker.io's 100-pull/6h anonymous rate limit, which would otherwise produce flaky `ImagePullBackOff` failures during the 10–30 minute provisioning window. That cold-start tether is the only sanctioned exception.

**How:** Every Sovereign runs `bp-self-sovereign-cutover` after handover. The chart is installed dormant at bootstrap-kit slot 06a during Phase 1 and triggered post-handover by the operator's "Achieve True Sovereignty" button (or by `catalyst-api` auto-fire on first login if explicitly enabled per-customer). Eight sequential Jobs pivot the eight tethers in dependency order; the final step is a 10-minute deny-egress NetworkPolicy hold against `github.com`, `ghcr.io`, and `harbor.openova.io` — **the only condition under which `cutoverComplete=true` is set is that the cluster reconciles green during this hold.** No cutover claim without the egress-block proof.

The full architecture, alternatives considered, and consequences are in [ADR-0002](adr/0002-post-handover-sovereignty-cutover.md). The eight-tether map is in `ARCHITECTURE.md` §11.1 (the Phase-2 Self-Sovereignty Cutover subsection of Catalyst-on-Catalyst). Implementation is split across issues #790 (umbrella), #791 (chart), #792 (API), #793 (UI), #794 (this docs set).

If a future ticket, agent, or operator session tries to ship a Sovereign without the cutover wired up, that ticket is wrong by definition until it is amended to comply. There is no "Sovereign-lite" tier where independence is optional.

---

## 12. Never validate against the local working tree (2026-05-18)

**Rule**: When verifying that a change has landed, that a file exists, or that a principle is documented, read the artifact from `git show origin/main:<path>` or from a fresh clone — never from the local working tree.

**Why**: Session 2026-05-18 23:46Z — agent A19 reported a false-positive "principle is already documented" because it grepped the local working copy, which was sitting on a feature branch with unstaged edits that included exactly the phrase being searched for. The principle was NOT on origin/main. Same class of error has cost cycles on chart-pin verification and CRD-applied verification, where uncommitted scaffolding masqueraded as shipped state.

**How to apply**:
- Validate file contents with `git show origin/main:path/to/file` or a worktree pinned to `origin/main` (e.g. `/tmp/clean-origin-main-check/openova` populated by `git worktree add ... origin/main`).
- Never grep the developer's working copy to prove that something is "already on main". The working tree is a hypothesis; `origin/main` is the contract.
- A verifier script that reads from `$PWD` is broken by construction — fix it to pipe through `git show origin/main:` before trusting its output.

---

## 13. Chart-pin bumps must match a GHCR tag that actually exists (2026-05-18)

**Rule**: Whenever a bootstrap-kit slot or chart manifest bumps a pin (e.g. `bp-foo:0.1.25 -> 0.1.26`), the new tag MUST already be published to GHCR before the pin lands on main. Source-side version-equality is not proof of publication.

**Why**: PR #1869 (TBD-A48 drift incident) landed a pin to `bp-self-sovereign-cutover:0.1.4` while the chart artifact at `oci://ghcr.io/openova-io/openova/bp-self-sovereign-cutover:0.1.4` had never been built. Flux retried `ImagePullBackOff` for hours; the cluster looked half-rolled-out because the manifest committed to main pointed at a tag that did not exist. Same class of bug has caused silent deploy failures whenever a chart-build CI job is skipped, fails, or races the deploy commit.

**How to apply**:
- Before committing a pin bump, run `crane manifest oci://ghcr.io/openova-io/openova/bp-<name>:<new-version>` (or `oras manifest fetch`) and confirm a non-empty response.
- In CI, gate the deploy commit on a "tag exists on GHCR" check; the build job's success is necessary but not sufficient — the registry must serve it.
- If you find a pin pointing at a missing tag on origin/main, treat it as a P0 incident: either publish the tag immediately or revert the pin. Do not paper over with "the build is queued."

---

## 14. Cutover-style HRs that rewrite HelmRepository URLs must `dependsOn` Gateway readiness (2026-05-18)

**Rule**: Any HelmRelease that pivots a HelmRepository URL from the mothership/upstream to a local in-cluster registry (Harbor, Gitea, local OCI mirror) MUST declare a `spec.dependsOn` on the Gateway/IngressClass that fronts the local registry, AND a health-check on that Gateway's TLS endpoint. Cutover-style HRs that flip URLs without proving the destination is reachable will deadlock the cluster.

**Why**: TBD-A24 / PR #1871, 2026-05-18 22:23Z — `bp-self-sovereign-cutover` flipped all eight HelmRepository URLs to point at the local registry **before Cilium Gateway was serving TLS**. Every Flux reconciler immediately started failing pulls; the cluster lost the ability to reconcile its own Gateway chart (catch-22); the entire control plane sat in a self-induced deadlock. Recovery required manual `kubectl edit` of HelmRepositories to revert URLs, which is exactly the kind of "by-hand fix" the cutover was supposed to eliminate.

**How to apply**:
- The HR that performs the URL flip must list `dependsOn` for the Cilium Gateway (or whatever ingress fronts the local registry) and its certificate Secret.
- The flip job must include a readiness probe against the local registry's TLS endpoint (`https://harbor.<sovereign-fqdn>/v2/`) and refuse to flip until the probe is green.
- The cutover chart must publish a rollback Job — flipping HelmRepository URLs is destructive in the same sense `tofu destroy` is; treat it accordingly.
- Never sequence "flip URLs" before "prove Gateway TLS works" inside the same blueprint. If they share a slot, the slot is wrong.

**Critical sub-rule (empirical 2026-05-19 on t27 — PR #1875 incident)**:
`HelmRelease.spec.dependsOn` references ONLY other HelmReleases. It CANNOT
reference Flux Kustomizations or other resource kinds. If you need to gate
a HelmRelease on a Kustomization, ship a "wait-HelmRelease" (tiny chart
with a Job that runs `kubectl wait …`) and depend on THAT HR. Or move the
gated workload into a Kustomization with cross-kind `dependsOn`.

Empirical verbatim from helm-controller when this rule was violated:
`unable to get 'flux-system/<name>' dependency: helmreleases.helm.toolkit.fluxcd.io "<name>" not found`
→ retries every 30s forever, never resolves.

---

## Self-check before every commit

Read this checklist before `git push`:

- [ ] Does this match the documented architecture exactly? (No bespoke when off-the-shelf is specified.)
- [ ] Are all values runtime-configurable? (No hardcoded regions, versions, URLs.)
- [ ] Did I disclose any divergence in the commit message?
- [ ] Did I close the relevant ticket?
- [ ] Is the work actually done, or did I write "scaffolding" and call it done?
- [ ] Am I about to write a session summary instead of continuing? (If yes: don't. Continue.)

---

## Lessons file

Each violation that gets caught lands a numbered Lesson in `VALIDATION-LOG.md`. Current count: 22 (Lesson #21 from Pass 103, Lesson #22 from Pass 104). The next lessons land here:

**Lesson #23 (2026-04-28):** Stopped session at ~19 commits despite explicit 24-hour-no-stop instruction. Self-protection ("context might fill") is not a legitimate reason to stop when the user has explicitly told you not to. Commit incrementally; continue. The session ended via my decision, not the user's.

**Lesson #24 (2026-04-28):** Built bespoke Go code calling Hetzner Cloud API directly + bespoke `exec.Command("helm", ...)` bootstrap installer, instead of OpenTofu→Crossplane→Flux as documented in `ARCHITECTURE.md` §10 + `SOVEREIGN-PROVISIONING.md` §3-§4. Did not disclose the divergence. The architectural docs are the contract; deviating from them silently is the worst form of technical debt — it pretends the system matches the docs when it doesn't.

**Lesson #25 (2026-04-28):** Hardcoded chart versions (cilium 1.16.5, openbao 2.1.0 then 0.16.0, keycloak 25.0.6 then 24.7.1) directly in the bootstrap installer Go code instead of reading them from a Crossplane Composition / OpenTofu variable / values.yaml metadata block at runtime. Each "fix" was another hardcoded value that happened to be more correct — never a structural fix to make the version configurable.

**Lesson #26 (2026-04-28):** Wrote scaffolding (compiles, builds, signs, publishes OCI) and presented it as "real working code" in user-visible summaries. The wizard's first POST would fail at SSH-key validation; `fetchKubeconfig()` returns a literal placeholder string. Presenting structurally-complete-but-runtime-broken code as "real" is exactly the deception the docs warn against.

---

*Part of [OpenOva](https://openova.io). Read this before doing anything.*
