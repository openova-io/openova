# EPIC completion matrix + Importance × Effort backlog (2026-06-27)

Source of truth: `docs/ledger/UAT.md` (origin/main), live env `91dc05917e44d1c1` (omantel.biz) + the
zero-touch validation prov. Headline: **~70% of UAT items are live-verified.** The gap to 100% is
overwhelmingly *merged fixes awaiting a live-proof that needs one of three founder actions* — not unwritten code.

## 1. EPIC completion matrix

| Epic (area) | Governing | Items | ✅ live | ⚠️ merged-pending | ⛔ UI-walled | ❌ fault | ☐ untested | % live |
|---|---|--:|--:|--:|--:|--:|--:|--:|
| catalog | #3668 IaC editor | 37 | 37 | — | — | — | — | **100%** |
| cloud | #3987 Cloud view | 7 | 7 | — | — | — | — | **100%** |
| mcp + sandbox | Pillar-4 | 4 | 4 | — | — | — | — | **100%** |
| storage | #3971 | 2 | 2 | — | — | — | — | **100%** |
| plane-isolation/gitea/fleet | infra | 5 | 5 | — | — | — | — | **100%** |
| jobs | #3646 | 13 | 12 | 1 | — | — | — | 92% |
| sso | #3374 | 24 | 22 | — | — | — | 2 | 92% |
| orgs | #3378/#4293 | 12 | 11 | — | — | — | 1 | 92% |
| funnel (Pillar-1) | #3376 | 27 | 21 | — | — | — | 6 | 78% |
| model/DR | #3687/#4212 | 27 | 19 | 6 | — | 1 | 1 | 70% |
| cutover (Pillar-5) | #3379 | 10 | 7 | — | — | — | 3 | 70% |
| topology | #3375 | 29 | 20 | 9 | — | — | — | 69% |
| meta/guards | — | 9 | 4 | 4 | — | 1 | — | 44% |
| placement | #3969 | 21 | 7 | 12 | — | 1 | 1 | 33% |
| recon | #3958 | 6 | 2 | 4 | — | — | — | 33% |
| apps/e2e-journey | per-Org | 18 | 4 | 7 | 4 | — | 3 | 22% |
| adoption/dr/spine | #4212 crossplane | 9 | 0 | — | 5 | — | 4 | **0%** |

**Legend** — ✅ live-verified · ⚠️ fix merged, live-proof pends a roll/walk · ⛔ behaviour works but behind the SSO login guard (can't assert headless) · ❌ genuine open fault · ☐ not yet walked.

**100% epics (7):** catalog, cloud, MCP/sandbox, storage, plane-isolation, gitea, fleet.
**Genuine open faults total ~3–9 rows** — the rest of the gap is ⚠️/⛔/☐, gated as below.

## 2. Importance × Effort backlog — sorted by importance

Owner key: **F** = only the founder can unblock · **A** = autonomous (me) · **F→A** = founder unblocks, then I walk.

| # | Item | What's pending (lean) | Imp | Effort | Owner | Recommendation |
|--:|---|---|:--:|:--:|:--:|---|
| 1 | **Agentic North Star** (#4111, #4277) | Spawned claude-code agent + per-Org anthropic seed are merged & wired; the live ExternalSecret is `SecretSyncedError` — **no Anthropic key on the platform**. | 🔴 HIGH | 🟢 LOW | **F** | **DO FIRST** — one credential unlocks Pillar-4's agentic run, the product's reason-to-exist. Highest ROI of anything. |
| 2 | **Crossplane adoption / DR backbone** (#4212, #4541, adoption epic 0%) | provider-opentofu can't install: the `proxy-xpkg` pull-through project is missing on the **bastion** Harbor (401). Fix PR #4542 ready; needs `BASTION_HARBOR_ADMIN_TOKEN` provisioned + the workflow run once. | 🔴 HIGH | 🟢 LOW | **F→A** | **DO FIRST** — one token + one workflow run takes the only 0% epic (adoption/dr/spine) off zero. Bastion is off-limits to me by your rule. |
| 3 | **Sovereignty cutover** (#3379, #4527, #4529) | Cutover must reach `cutoverComplete=true` + the 600s deny-egress hold. All fixes merged (#4543 was the last); a re-prov is running now. | 🔴 HIGH | 🟡 MED | **A** | **IN PROGRESS** — autonomous, ~2 hr (image build + prov + cutover). Closes Pillar-5. |
| 4 | **Region-kill + multi-region placement** (#4275 Pillar-3, #3969 EPIC) | DR plumbing is live-healthy (cnpg streaming RPO=0, leases, mesh); the `targets[]` placement model is merged. Both need a **2-region env to walk** — the EIP pool only fits one single-region throwaway. | 🔴 HIGH | 🟡 MED | **F→A** | **SCHEDULE** — EIP quota bump 10→≥16 (request drafted) lets me fire a 2-region prov and walk region-kill + placement to PASS. Lifts placement 33%→~90% and dr/Pillar-3 0%→done. |
| 5 | **topology Topology-tab content** (9 ⚠️) | The per-app Topology tab exists; its content (regions/standby/lag) isn't yet asserted live. | 🟡 MED | 🟢 LOW | **A** | **DO** — cheap authed re-walk; lifts topology 69%→~95%. |
| 6 | **Funnel Pillar-1 completeness** (6 ☐) | Core funnel + voucher issue/redeem proven. Remaining: a *real* voucher-redeem→new-Org, a 2nd Org on a 2nd TLD, post-checkout credit frames. | 🟡 MED | 🟡 MED | **A** | **DEFER / partial** — Pillar-1 core is shipped; these are edge completeness. Walk 1-2, drop the deepest 2nd-Org ones. |
| 7 | **#4539 isolation-label** (cosmetic) | S-plan Org reports `isolation=vcluster` but backs host-ns. Fix merged + rolled; needs a verify-close. | 🟢 LOW | 🟢 LOW | **A** | **DO** — verify-close, ~5 min. |
| 8 | **owner voucher-redirect** (model row 3 ❌) | An authed owner hitting `/redeem` should 302→`/dashboard`; currently renders the public funnel. | 🟢 LOW | 🟢 LOW | **A** | **DO or DROP** — minor UX edge; cheap if done. |
| 9 | **recon view partials** (4 ⚠️) | Reconciler-lens rows pending a content re-walk. | 🟢 LOW | 🟢 LOW | **A** | **DEFER** — low value; batch with the topology re-walk. |
| 10 | **e2e-journey** (4 ⛔) | Multi-step signed-in journey assertions, behind the SSO login guard. | 🟡 MED | 🟡 MED | **A** | **DEFER** — needs the throwaway-mailbox interactive session; data-plane already proven. |
| 11 | **per-Org apps not in cart** (openclaw/mail/postgres ⚠️) | These apps weren't in the demo Org's cart, so not walked. | 🟢 LOW | 🟡 MED | **A** | **DROP** — provisioning apps no one asked for to flip a row is low value. |
| 12 | **meta/guards fault** (1 ❌) | A guard/meta assertion. | 🟢 LOW | 🟢 LOW | **A** | **DROP or DO** — negligible. |

## 3. The 2×2 (where to spend)

```
            LOW EFFORT                         HIGH EFFORT
        ┌─────────────────────────────┬─────────────────────────────┐
 HIGH   │ #1 Anthropic key      (F)   │ #4 Region-kill+placement     │
 IMPORT │ #2 Harbor token       (F→A) │     (F: EIP bump → A: walk)  │
        │ #3 Cutover (running)  (A)   │                              │
        ├─────────────────────────────┼─────────────────────────────┤
 LOW    │ #5 topology re-walk   (A)   │ #6 funnel 2nd-Org edges      │
 IMPORT │ #7 isolation verify   (A)   │ #10 e2e-journey              │
        │ #8 owner-redirect     (A)   │ #11 per-Org extra apps  DROP │
        │ #9/#12 recon/meta     (A)   │                              │
        └─────────────────────────────┴─────────────────────────────┘
```

## 4. Bottom line for prioritisation

- **The 3 founder actions are top-left (high-value, low-founder-effort) and unblock the 3 weakest epics:**
  1. **Anthropic credential** → Pillar-4 agentic (#4111/#4277)
  2. **Bastion-Harbor token** (+ run the #4542 workflow) → the 0% adoption/DR epic (#4212/#4541)
  3. **EIP quota bump 10→≥16** → region-kill + multi-region placement (#4275/#3969)
- **My autonomous remainder is small:** the cutover (running), the topology/isolation/owner-redirect cheap closes (~1 hr total). Then the gated-epic walks once the three levers land.
- **Safe to drop / defer (low ROI):** per-Org apps not in the cart (#11), the deepest 2nd-Org funnel frames (#6), meta/recon partials (#9/#12).

If you do the **3 top-left founder actions**, the autonomous walks take the matrix from ~70% to **~95%+**; the only genuine code faults remaining are a handful of low-value UI edges you may choose to drop outright.
