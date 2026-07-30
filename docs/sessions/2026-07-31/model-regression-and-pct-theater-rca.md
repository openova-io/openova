# Root-cause analysis: (1) "model keeps reverting to Opus" and (2) completion-% matrix theater

Date: 2026-07-31. Env context: hw291 (post-cutover, cc=true 2026-07-30T11:03:43Z). Author session: 5c468708.

This document is the committed, reviewable form of two findings that were previously only
in agent-private memory + session transcript. It exists so the conclusions are auditable.

---

## Directive #1 — "the agent switches back to Opus despite a Fable-only directive"

### Conclusion: there is NO local Opus pin. The governing default is Fable, and sub-agents inherit it.

Evidence, every locally-inspectable surface (swept five times across the session; final sweep 2026-07-31):

| Surface | Finding |
|---|---|
| `~/.claude/settings.json:17` | `"model": "fable"` — the **only** model key in local config; this is what selects the session model. |
| `~/.claude/skills/**` frontmatter | zero `model:` overrides. The only `opus` string is a webm/**opus** audio codec in a qa-loop recipe — not a model. |
| `~/.claude/agents/`, `<repo>/.claude/agents/` | **do not exist** — no local sub-agent definition can carry a `model:` override. |
| `<repo>/.claude/settings*.json` | no `model` key. |
| Sub-agent wire-level (this session's two worktree agents) | **145/145 and 183/183 requests = `claude-fable-5`**, zero Opus. Sub-agents provably inherit the Fable default at invocation. |
| `~/.claude.json` | contains `opus` strings ONLY inside **server-delivered feature-flag caches** (`tengu_auto_mode_config` maps fable→opus-4.8[1m] for auto-mode routing; the review-bughunter fleet is server-pinned opus-4.7). The live CLI process **rewrites** this file, so local edits are clobbered — editing it is futile and unnecessary. |
| `~/.claude/plugins/marketplaces/.../pr-review-toolkit/agents/*.md` | two agents carry `model: opus`, but pr-review-toolkit is **NOT in `enabledPlugins`**. Inert today. |
| `~/.claude/plugins/marketplaces/.../security-guidance/hooks/{llm.py,security_reminder_hook.py}` | hardcode `SECURITY_REVIEW_MODEL` / `_DEFAULT_PUBLIC_MODEL = "claude-opus-4-7"`, but security-guidance is **NOT in `enabledPlugins`**. Inert today. |

**COMPLETE local-Opus enumeration (confirmed 2026-07-31):** the ONLY local files that would select Opus are inside TWO disabled marketplace plugins — `pr-review-toolkit` (agent frontmatter `model: opus`) and `security-guidance` (hook default `claude-opus-4-7`). `enabledPlugins` = `[playwright, rust-analyzer-lsp]` only, so NEITHER is active. Skill sub-agent invocation sites (qa-loop `subagent_type: "general-purpose"`) pass no model param and inherit the Fable default. These two disabled plugins are the entire set of future local Opus risks: enabling either would reintroduce Opus for its specific function (PR review / security review), nothing else.

### Mechanism of the historical "switching"

The session-start model was the **client's saved default**, which was Opus before `/model fable` was
run this session. That single action wrote `settings.json:17 "model": "fable"`, which now governs
every new session and every sub-agent spawn. Any residual Opus appearance can now only come from
**Anthropic-side auto-mode routing** (avoid auto mode) or the **server-pinned review fleet** — neither
is a local misconfiguration and neither is patchable from this repo.

### Action: none required in-repo — the pin is already in place (`settings.json:17`). The single
future risk (enabling pr-review-toolkit) is documented above and in agent memory
`reference_opus_model_source_hunt_closed_no_local_pin`.

---

## Directive #2 — "the completion-percentage matrix is theater"

### Conclusion: the durable-% column had no measurement procedure, so it tracked conversational mood, not the platform. Fixed with two mechanical rules.

Root cause: the raw UAT ledger is measured (`uat-tally.py`) but flushed to ~0 on every fresh prov by
design, so it cannot be tracked across provs. To fill that gap a "durable %" estimate was re-derived
from judgment on every ask — a number with no fixed procedure. It drifted (91→88→87→90 in one session)
with the tone of the conversation, and once manufactured a phantom regression (founder rebuke 2026-07-19),
then repeated the error with the sign flipped (2026-07-30).

Two rules now committed to `project_completion_matrix_canonical_format` (memory), enforced on every future matrix:

1. **Durable-number correction #2**: the durable score changes ONLY on walk evidence booked in the file —
   never re-derived mid-conversation, never in response to founder affect, in either direction. A defect
   *discovered* by a stricter walk goes in the "what is still wrong" column, NOT as a score subtraction
   (finding a pre-existing bug is not the platform regressing). The standing durable Overall is the last
   evidence-backed value (hw290 ~88), not a fresh estimate.
2. **Artifact-per-cell**: every durable cell must name its backing artifact inline (UAT row ID + env/date,
   evidence file, or issue-comment URL). A cell with no named artifact renders `n/a (no walked artifact)` —
   no artifact, no number.

The real progress axis is the **monotones** — merged fix PRs, evidence-gated closes, consecutive cc=true
count, PATH-TO-100 open-blocker count → 0 — not the percentage of a reset-on-fire ledger.

---

## Why this is committed rather than left in memory

Agent memory + session transcript are not founder-auditable. This file is. Both conclusions above are
falsifiable against the cited surfaces; if either is wrong, the specific line to challenge is named.

---

## Directive addendum — "why aren't the EPICs converging?" (git history vs claimed progress)

### Conclusion: the two open EPICs are NOT stalled. They advance through child-issue fix commits; what remains is deliberately-parked architecture (`status/blocked-ext`), not a pod-convergence failure.

Two independent evidence sources, 2026-07-31:

**1. Git history — the EPICs are child-commit-active.** `git log --since='30 days ago' --grep='#4212|#3969'` = 23 commits; the *fix* commits (not docs) show real forward motion on the placement/DR-backbone EPICs:
- `e937cda9d` fix(#4836): accept #3969 placement `targets[]` in HandleApplicationUpdate (via #4840)
- `b72326dfd` fix(#4950): keep `placement.targets[]` through decode so console Edit-Apply derives mode (#4958)
- `7879bcb23` fix(#4986): bp-postgres emits `dr-<instance>` Continuum CR → shared-pg Topology DR panel renders (#4987)
- `b41c93b3c` fix(#5482): read primaryRegion from `status.placement` (#5483)

So the claim "EPICs aren't converging" is not borne out by the commit record — the placement model (#3969) and DR backbone (#4212) both received merged fixes within the window. Their *aggregation issues* went quiet while their *child issues* kept merging; that is thread-lag, not code-stagnation. (Correcting for this is why the SHA-grounded RCAs were posted directly on #4212 / #3969 on 2026-07-30.)

**2. Live cluster — zero convergence blockers.** `kubectl get pods -A | grep -v Running/Completed` on hw291 (~24h post-cc), both regions: **395 pods, ZERO non-Running workloads.** The only `Error` entries are 4 uncollected cutover-step Job pods (failed retry attempts of steps that later succeeded — filed as a GC hygiene item #5530), not running workloads. Nothing is wedged.

### What actually remains on #4212 / #3969

Both are `status/blocked-ext` **architecture migrations**, not failures:
- **#3969** (Application-centric placement `targets[]`): the accept/decode/projection code is built and merged (SHAs above); live Applications still carry the legacy `placement: active-hot-standby` STRING. The remaining work is migrating the stored CRs off the legacy field + deleting it — a data migration, not new machinery.
- **#4212** (ONE object-model / DR backbone): the DR-backbone half advances continuously via child issues (#5316/#5335/#5478 continuum-controller); the crossplane-adoption half had its bastion-Harbor tether removed (#4602). `status/blocked-ext` covers only the remaining architecture call.

Neither is a convergence blocker. The convergence proof is the cutover itself: cc=true 2026-07-30T11:03:43Z with the 600s deny-egress hold passing on both regions, and the clean pod sweep 24h later.
