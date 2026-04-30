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

## 7. Verify before claiming done — the deploy chain is the contract

The user has said: "DoD E2E 2-pass GREEN on the current deployed SHA is the ONLY valid proof of done. CI-green + pods-running is not enough." (per auto-memory `feedback_dod_is_the_proof.md`).

**A merged PR is not a delivered feature.** Before claiming any work as "done", run the canonical chain personally — never delegate, never assume. If any step breaks, the feature is NOT delivered.

```
PR merged → CI workflow triggered → workflow succeeded →
artifact published (GHCR/registry) → Flux/CI deploy commit landed →
target cluster reconciled → pod rolled → live endpoint serves new version
```

**Trigger phrases** that fire this protocol — when I see any of these in my own thinking, agent reports, or a question from the user, I MUST run the chain before answering "yes":

- "PR merged" / "auto-deploys" / "CI will pick it up"
- "tests passed" / "lint clean" / "unit tests green" — these are NOT delivery
- "should be live shortly" / "propagating now"
- User asks "is X done?" / "does this work?" / "show me the URL"

**Workflow trigger gotchas to remember** (fail mode discovered 2026-04-30):
- Many `Build & Deploy *` workflows in `openova-io/openova` fire on `workflow_dispatch + cron only`, NOT on push. Path-filtered push triggers must be explicitly added or the workflow won't fire.
- Verify the workflow's `on:` block before assuming a merge will deploy. If it's cron+manual, fire `gh workflow run` after every relevant merge.

**For UI work**: the only acceptable proof of "done" is a Playwright MCP screenshot of the live production endpoint, compared to the spec by me with my own eyes. Not the agent's screenshot. Not the agent's % grade. Mine.

**For chart/code work**: the proof is `kubectl get hr -A` Ready=True OR `curl` returning the expected payload, run after Flux confirms reconcile. Not "the chart packaged successfully."

If I cannot verify (cluster not reachable, no test env), the user-facing message MUST say "structurally complete, runtime-unverified — verification blocked on <X>" — never imply delivery.

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

**Lesson #27 (2026-04-30):** Confused "PR merged" with "feature delivered." Merged 22 catalyst PRs over 6 hours; user reported the UI looked unchanged. Audit found `catalyst-build.yaml` only fires on cron+manual, not push — every merged PR sat unbuilt; production served the original failing image (`:52085db`) the entire session. The fix is the deploy-chain protocol now in §7. The trigger phrase "PR merged" must always be followed by my running the chain.

**Lesson #28 (2026-04-30):** Accepted agent self-grades and "intentional divergence" rationalizations as final state. PR #245 shipped pill-cards labeled "intentional divergence" — user: "is this a joke?" PR #282 was self-graded "88% match" — user: "there are zero circles, all the page is full of old rectangle." The fix: agent self-grades are inadmissible. Parent loads the artifact (mockup + screenshot) and judges with own eyes before merging. See `~/.claude/projects/.../memory/feedback_no_agent_self_grades.md`.

**Lesson #29 (2026-04-30):** Edited `products/catalyst/chart/templates/` paths without auditing both consumers. The same path is read by Sovereign Helm installs (needs valid Helm template) AND by contabo-mkt's plain Flux Kustomization (needs valid raw YAML — chokes on `{{ }}`). Multiple PRs (#246 deleted "stray" kustomization.yaml indexes; #260 added Helm `{{ if }}` to a CRD; #280 deleted "legacy" Traefik ingress files; #281 deleted entire sme-services dir) broke 3 contabo Flux Kustomizations for 5+ hours; console.openova.io served the Traefik default self-signed cert. The fix: before editing any shared chart/manifest path, list every Flux Kustomization + HelmRelease that consumes it. See `feedback_shared_resource_consumer_audit.md`.

**Lesson #30 (2026-04-30):** Resource discipline crashed the Contabo VPS. Dispatched 12 simultaneous Opus subagents on a 4-vCPU/11GB machine; load spiked past 24, OS hard-rebooted, `/tmp` was wiped (lost kubeconfigs + SSH keys). Even after reboot, kept dispatching 4 agents during heavy phases. The fix: maximum 2-4 simultaneous agents on this VPS (depending on workload weight); `uptime && free -h` BEFORE every dispatch; durable artifacts go in `~/.cache/openova/` not `/tmp/`. See `feedback_resource_budget_4_agents.md`.

---

*Part of [OpenOva](https://openova.io). Read this before doing anything.*
