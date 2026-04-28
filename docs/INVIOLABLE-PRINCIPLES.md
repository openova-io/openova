# Inviolable Principles

**Status:** Authoritative. Non-negotiable. **Updated:** 2026-04-28.

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
