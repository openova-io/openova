# Session 2026-05-17 / 18 — Convergence Report

**Window:** 2026-05-17 evening → 2026-05-18 early morning UTC
**PR range:** #1597 → #1632 (~36 numbered PRs, plus the auto-generated `deploy:` collector commits between them)
**Bastion:** `bastion.openova.io` (`vmi3305700`, 11 GiB RAM, 6 vCPU)
**Charts shipped:** 1.4.153 → 1.4.160 (eight version bumps in one window)
**Theme:** finish founder bugs, drop iframes from BSS, scaffold the Sandbox product end-to-end, and wire the SME event/identity/DNS plumbing that 31-gate convergence had been silently skipping over.

---

## TL;DR

- ~22 user-facing PRs grouped into nine waves (Waves 1–9 + Sandbox W1–W5 + a "convergence cleanup" wave).
- Two OOM incidents on the bastion forced the move from "max-6 flat agent count" → "weighted-slot accounting". Test executors now cost 1.5 slots each, Fix-Authors 1.0; dispatcher refuses to launch if the sum would exceed 6.
- One single TypeScript regression in `CloudPage.tsx` (Wave 8) silently held back Wave 5 + Wave 6 + Sandbox UI from reaching fresh provs for several hours. We did not notice until a chart bump (1.4.155 → 1.4.156, PR #1617) was required to "republish with fresh UI bytes". Lesson now pinned: **always check Build & Deploy Catalyst CI status after a merge**.
- The NATS broker bridge (PR #1626) closed the **publish leg** of the SME event flow (tenant/billing dispatchers now publish to NATS). The **consume leg** — Sandbox controller + Organization reconciler subscribing to `catalyst.tenant.created` / `catalyst.order.placed` — is queued for the next wave.
- Four new DoD gates appended: **D32 Sandbox CRD installable**, **D33 Sandbox agent catalogue picker**, **D34 newapi LLM gateway**, **D35 NATS broker round-trip** — see `SOVEREIGN-MULTI-REGION-DOD.md`.

---

## Timeline by wave

### Wave 1 — D17 list-view route fix (#1597)

Hot off the morning's t142 walk: `/cloud?view=list&kind=<X>` still bounced to `/dashboard` when the kind filter was set. Bare-minimum route guard cleanup — five lines plus a Playwright snapshot.

PR: **#1597** `fix(ui): D17 — /cloud?view=list&kind=<X> no longer redirects to /dashboard`

### Wave 2 — Founder-bug families B / C / D / E / F / G (#1598-#1604)

The Family pass over the founder's bug-list from the t142 walk. Each family is one PR over multiple files; the `chart 1.4.153→1.4.154` collector (#1604) republishes all UI/api images together so the chart values bump is atomic.

| PR | Family | Subject |
|---|---|---|
| #1598 | F | BSS routes mounted at `/console/bss/*` with RBAC menu gating (founder #1) |
| #1599 | D | Treemap fan-out for cluster / region / vcluster / family + Layer-1 default |
| #1600 | C | `ResourceDetailPage` real data + tab nav (founder #5) |
| #1601 | G | 6 singletons (C8-001/C8-005/C9-006/C10-002/C10-003/C7-007) |
| #1602 | E | Compliance UI (Kyverno + Falco + SBOM + framework filter) |
| #1603 | B | AppDetail status sync (HR → UI wire + correct ns/label) |
| #1604 | — | Chart 1.4.153 → 1.4.154 collector |

The 6-PR parallel fan-out triggered the **first OOM incident** of the night (see "OOM Incidents" below).

### Wave 5 — UX polish + chart 1.4.155 (#1605)

Single squashed PR: sidebar reorder, BSS icon swap, marketplace surface promoted from inline component to `SettingsCard`. Chart 1.4.154 → 1.4.155.

PR: **#1605** `feat(ui): Wave 5 — UX polish (sidebar reorder + BSS icon + marketplace as SettingsCard) + chart 1.4.155`

### Wave 6 — BSS native port (Option B step 1) (#1606-#1614)

Founder ruling earlier in the day: drop the iframe seam in BSS. Each BSS sub-page (Landing / Orders / Billing / Revenue / Vouchers / Tenants) is rewritten natively in the Sovereign Console as a React page, sharing the PortalShell + design tokens, and the iframe shell is deleted.

Six PRs landed in two waves of three (parallel within wave, serial between waves) to stay under the 6-slot ceiling.

| PR | Page |
|---|---|
| #1606 | BSS native landing (Option B step 1, kills iframe seam) |
| #1607 / #1608 | Orders (two PRs because the first squash-merged with an unrelated CI bump; #1608 is the canonical one) |
| #1611 / #1613 | Revenue (KPI + chart + breakdown — re-shipped after a rebase) |
| #1612 | Billing |
| #1609 | Vouchers (table + Issue modal) |
| #1614 | Tenants (drops iframe, native table) |

### Wave 7 — Critical-path hotfix: bp-hcloud-csi (#1610)

Slot 17a's `bp-hcloud-csi` had a chicken-and-egg with harbor — the CSI pulled its provisioner image from harbor, but harbor needed PVCs to come up. Resolved in #1610 by removing slot 17a entirely (Hetzner-CCM handles volume mount once we drop the explicit CSI install).

PR: **#1610** `fix(bootstrap-kit): remove bp-hcloud-csi slot 17a — chicken-and-egg with harbor (Wave 7 critical-path hotfix)`

### Wave 8 — CloudPage TypeScript hotfix (#1616)

**This is the wave that silently held back everything Wave 5 + Wave 6 had merged.**

`CloudPage.tsx` had `kindCounts` typed against a hand-maintained literal union of resource kinds. Wave 5 + Wave 6 had landed cleanly individually, but the chart collector (#1604) introduced two new kinds via the Compliance UI (`policyreports`, `clusterpolicyreports`) that were not in the union. The Catalyst UI Docker build failed in CI with a TypeScript error — silently, because CI failure stops at the image-build step and doesn't open a noisy issue. No image was pushed to ghcr.io, so the chart values reference (which we'd already bumped in #1604/#1605/#1615) pointed at SHA tags that didn't exist. Every chart-values bump after #1604 inherited the same problem, but the chart still synthesised fine — Flux on every fresh prov pulled `ImagePullBackOff`.

We caught it only when a manual `kubectl get pods -n catalyst-system` on a verifier prov showed the UI Pod stuck `ImagePullBackOff`. Tracing back: the ghcr.io tag genuinely did not exist.

PR: **#1616** `fix(ui): Wave 8 hotfix — CloudPage kindCounts adds policyreports + clusterpolicyreports (unblocks UI build)`

Lesson (now pinned in memory): **after any PR that merges, check `gh run list --workflow="Build & Deploy Catalyst" --limit 1` and confirm `conclusion=success` before bumping chart values that reference the same SHA**. A broken UI build does NOT fail the chart bump — it just leaves a dangling image reference.

### Wave 9 — Chart 1.4.155 → 1.4.156 collector (#1617)

After #1616 landed, the UI image finally re-published to ghcr.io. To force every downstream Flux Kustomization to roll its pods (image tag is still the same SHA, but the underlying image is now the corrected one — kubelet may have cached the broken pull), we bumped chart minor.

PR: **#1617** `chore(release): chart 1.4.155→1.4.156 — Wave 9 collector republishes with fresh UI bytes`

### Sandbox Wave 1-5 — full product scaffold (#1615 / #1618 / #1619 / #1621 / #1622 / #1632)

Six PRs across one product directory. Architecture is described in `products/sandbox/docs/architecture.md` (shipped 2026-05-15).

| PR | Subject |
|---|---|
| #1615 | Wave 1 step 1: CRD `sandbox.openova.io/v1.Sandbox` |
| #1618 | Wave 2: pty-server + openova-sandbox-mcp scaffold |
| #1619 | Wave 1b: newapi proxy + BYOS + org-scoped JWT (in `core/services/auth` + new `core/services/newapi`) |
| #1621 | Wave 3: Sandbox UI (landing + session host + BYOS settings) inside the Sovereign Console |
| #1622 | Wave 1: controller + chart scaffold (chart templates `sandbox-controller.yaml`, `sandbox-rbac.yaml`) |
| #1632 | CI: build workflows for controller + pty-server + mcp-server (so the chart can actually deploy) |

**The CI workflow PR (#1632) was the missing piece** — the chart referenced `ghcr.io/openova-io/openova/sandbox-{controller,pty-server,mcp-server}:<sha>` but no workflow ever pushed those images. #1632 added three `.github/workflows/build-sandbox-*.yml` files. Same lesson as Wave 8: a chart values bump that references an image is meaningless if the build pipeline doesn't push the image.

### Convergence cleanup wave (#1623-#1631)

Nine PRs after the Sandbox work, surfacing every SME / tenant / DNS / event / identity gap that 31-gate convergence had been working around silently.

| PR | Layer | Subject |
|---|---|---|
| #1623 | marketplace | Persist subdomain TLD across wizard steps |
| #1624 | bootstrap-kit | Install vcluster CRDs + controller on Sovereign (gates Org → vCluster spawn) |
| #1625 | catalyst-api | Wire `/api/v1/sme/billing/vouchers/{list,issue,revoke}` proxy |
| #1626 | sme (broker bridge) | Wire tenant + billing event dispatchers to NATS (was Redpanda-only, blocking convergence) |
| #1627 | marketplace | Post-purchase redirect to Sovereign-local console (was hardcoded to mothership) |
| #1628 | billing | Skip Stripe when voucher covers 100% of total (unblocks fully-paid voucher checkout) |
| #1629 | domain | Per-tenant DNS reconciler — `<slug>.<pool-domain>` resolves to Sovereign LB (was mothership) |
| #1630 | catalyst-api | Mint HS256 token on SME proxy calls (was forwarding incompatible RS256) |
| #1631 | sandbox+bootstrap-kit | newapi Sovereign install (Bank Dhofar Qwen wired for Sandbox) |

PR #1626 is the **broker publish leg**. The matching **consume leg** is still open: the Sandbox controller and Organization reconciler need to subscribe to `catalyst.tenant.created` / `catalyst.order.placed` and react. Today they still poll Catalyst-API. See "Remaining convergence gaps" below.

---

## OOM incidents and the move to weighted slots

### Incident 1 — Wave 2 family fan-out (~21:00 UTC)

Dispatched six Fix-Authors for Families B/C/D/E/F/G simultaneously, all sync-Agent. Aggregate RSS ramped to 9.3 GiB; the parent `claude-code` PID 218043 was OOM-killed by the kernel at ~9.7 GiB. Recovered with `claude --resume`, then re-dispatched the same six but **serialised them** in 2 waves of 3.

Memory cost per agent observed in this incident:
- Fix-Author (touches ~10 files): 1.1–1.3 GiB RSS at peak
- Test-Executor (Playwright walk, chrome headed): 1.6–1.9 GiB RSS at peak

### Incident 2 — Sandbox parallel dispatch (~02:00 UTC)

Dispatched 4 Fix-Authors (Sandbox W1 + W1b + W2 + W3) plus 2 Test-Executors (verifier walks of t142) simultaneously. Flat count = 6 = within the 6-slot ceiling. RSS peaked at 8.9 GiB → no OOM, but the verifier Playwright sessions stalled (browser tab couldn't get a fresh allocation, sat in CPU-spin) for ~3 minutes.

### Resolution — weighted slots

New dispatcher accounting (now mirrored in `~/.claude/projects/-home-openova-repos-openova-private/memory/feedback_max_6_parallel_agents.md`):

| Agent kind | Weight |
|---|---|
| Fix-Author (focused, ≤15 files) | 1.0 |
| Fix-Author (large refactor, >25 files) | 1.5 |
| Test-Executor (Playwright walk) | 1.5 |
| Research/sub-grep agent (read-only) | 0.5 |

Hard rule: sum of weights at any instant ≤ 6.0. Dispatcher refuses to launch if the next agent would push over. If workload requires more, serialise into waves whose weight-sum each stays ≤ 6.

Wave 6 BSS port (six PRs) is the worked example: split into two waves of 3 PRs each (weight 3.0 per wave) with sequential gates between, total wall-clock 17min instead of an all-in-parallel that would have OOM'd.

---

## The CloudPage TypeScript regression — the silent multi-hour stall

Timeline:

| Time (UTC) | Event |
|---|---|
| ~22:00 | Wave 5 (#1605) merges. Chart 1.4.154 → 1.4.155. UI image tag updated in chart values. |
| ~22:05 | "Build & Deploy Catalyst" workflow fires for #1605's HEAD SHA. UI Docker build fails with TypeScript error in `CloudPage.tsx` (`kindCounts` literal-union missing two kinds from Family E's Compliance UI #1602). Workflow result = failure. No ghcr.io tag pushed. **No human checks the workflow result.** |
| ~22:15 → ~01:30 | Wave 6 BSS pages (#1606-#1614) all merge with their own deploy-collector commits. Each collector bumps the UI image tag in chart values to the latest SHA. The UI image at every one of those SHAs **does not exist on ghcr.io** because the build keeps failing for the same `kindCounts` reason. |
| ~01:30 | Verifier prov spun up. Catalyst-UI Pod stuck `ImagePullBackOff` on every fresh prov in the window. |
| ~01:45 | Root cause traced: `gh run list` shows the consecutive "Build & Deploy Catalyst" failures, all citing TS2322 on `CloudPage.tsx:284`. |
| ~02:00 | #1616 ships the type fix. |
| ~02:15 | #1617 bumps chart 1.4.155 → 1.4.156 to roll all pods on a fresh image. |

**Cost:** 3.5 hours of "convergence is broken on fresh prov, why?" investigation that turned out to be a single missing union member.

**Mitigation now baked in:** After every PR that merges, run

```bash
gh run list --workflow="Build & Deploy Catalyst" --limit 3 --json conclusion,headSha,headBranch
```

and confirm the latest matching SHA has `conclusion=success` before bumping chart values that reference that SHA. The CI status check is now a hard prerequisite for any chart-values commit, not an after-the-fact verification.

---

## The broker publish/consume split (PR #1626)

The SME (Service Management Engine) plane has two services that emit events: the tenant service emits `catalyst.tenant.created` / `catalyst.tenant.updated`, and the billing service emits `catalyst.order.placed` / `catalyst.invoice.paid`. Pre-#1626, both used Redpanda dispatchers — but Redpanda was only installed as a dev convenience; on Sovereign we standardised on NATS JetStream (per ADR-0001 §6).

PR #1626 (`fix(sme): wire tenant + billing event dispatchers to NATS`) replaces the Redpanda dispatcher with the NATS one. **This is the publish leg only.** Events now flow:

```
tenant-service ─POST(catalyst.tenant.created)─> NATS JetStream <subject>
billing-service ─POST(catalyst.order.placed)─> NATS JetStream <subject>
```

The **consume leg** is still polled-over-API:

- The Organization controller (`core/controllers/organization`) polls Catalyst-API `/api/v1/orgs/pending` every 5s to find new tenants.
- The Sandbox controller (`products/sandbox`, shipped this session in #1615/#1622) polls similarly.

The follow-up PR (queued, not in this session) wires both controllers to **subscribe** to NATS:

```
NATS catalyst.tenant.created ─> Org controller (Reconcile + Spawn vcluster)
NATS catalyst.order.placed   ─> Sandbox controller (Reconcile + Spawn Sandbox)
```

This is gated on **D35** in the new DoD additions.

---

## Remaining convergence gaps after this session

1. **newapi Sovereign-side auth.**
   #1619 + #1631 shipped the newapi proxy on the Sovereign and wired Bank Dhofar Qwen as a backend. Identity is org-scoped JWT (HS256, minted by `core/services/auth`). The Sovereign-side ingress for `newapi.<fqdn>` is up but the **JWT validation** on the Sovereign side currently trusts any HS256 with the right `iss`; the matching key-rotation flow + JWKS endpoint are NOT yet shipped. Untested on a fresh prov.

2. **vCluster CRD on the Sovereign.**
   #1624 installs the vcluster CRDs + controller on the Sovereign at bootstrap. But the controller's RBAC has not been audited for Sovereign-vs-mothership scope, and the Organization controller's reconcile still references the mothership vcluster API in two paths (`organization_controller.go:312`, `organization_controller.go:478`). Will block D29 zero-touch on the first tenant-create on a fresh Sovereign.

3. **Sandbox-marketplace wiring.**
   The Sandbox CRD (#1615) and controller (#1622) exist. The Sandbox UI (#1621) is mounted at `/console/sandbox`. But the marketplace **does not list Sandbox as a purchasable product yet** — there is no `marketplace-entry` YAML for it. End-to-end "tenant clicks 'Buy Sandbox' → controller spawns a Sandbox" is not wired. This is the natural test for D32 + D33.

4. **D31 active-hot-standby tenant install end-to-end walk.**
   Chart renders correctly (verified via `helm template` on 2026-05-17, memo `feedback_d31_chart_verification.md`). But the tenant-marketplace WordPress entry that wires `pg.activeHotStandby.enabled=true` into the install payload has not been shipped. Tenant on the Sovereign cannot today pick "WordPress with cross-region replication" — only "WordPress" with a single CNPG instance.

5. **D35 NATS broker round-trip.**
   PR #1626 closes the publish leg. The consume leg is open (see above). Convergence is not declared until a fresh prov can issue a voucher, redeem it, and the resulting `catalyst.tenant.created` event reaches the Org controller via NATS (not via polling) within 2s.

---

## PR roster (this session)

```
#1597  fix(ui): D17 — /cloud?view=list&kind=<X> no longer redirects to /dashboard
#1598  feat(ui): Family F — BSS in Sovereign Console
#1599  fix(catalyst-api+ui): Family D — treemap fan-out
#1600  fix(ui): Family C — ResourceDetailPage real data + tab nav
#1601  fix(multi): Family G — 6 singletons
#1602  feat(ui+api): Family E — Compliance UI (Kyverno+Falco+SBOM)
#1603  fix(catalyst-api+ui): Family B — AppDetail status sync
#1604  chore(release): chart 1.4.153→1.4.154
#1605  feat(ui): Wave 5 — UX polish + chart 1.4.155
#1606  feat(ui): Wave 6 PR 1 — BSS native landing
#1607  feat(ui): Wave 6 PR 3 — BSS Orders (superseded)
#1608  feat(ui): Wave 6 PR 3 — BSS Orders native
#1609  feat(ui): Wave 6 PR 5 — BSS Vouchers native
#1610  fix(bootstrap-kit): remove bp-hcloud-csi slot 17a (Wave 7)
#1611  feat(ui): Wave 6 PR 4 — BSS Revenue (superseded)
#1612  feat(ui): Wave 6 PR 2 — BSS Billing native
#1613  feat(ui): Wave 6 PR 4 — BSS Revenue native
#1614  feat(ui): Wave 6 PR 6 — BSS Tenants native
#1615  feat(sandbox): Wave 1 step 1 — CRD sandbox.openova.io/v1.Sandbox
#1616  fix(ui): Wave 8 hotfix — CloudPage kindCounts (unblocks UI build)
#1617  chore(release): chart 1.4.155→1.4.156
#1618  feat(sandbox): Wave 2 — pty-server + openova-sandbox-mcp scaffold
#1619  feat(sandbox+auth+newapi): Wave 1b — newapi proxy + BYOS + org-scoped JWT
#1621  feat(ui): Wave 3 — Sandbox UI (landing + session host + BYOS settings)
#1622  feat(sandbox): Wave 1 — controller + chart scaffold
#1623  fix(marketplace): persist subdomain TLD across wizard steps
#1624  fix(bootstrap-kit): install vcluster CRDs + controller on Sovereign
#1625  fix(catalyst-api): wire /api/v1/sme/billing/vouchers proxy
#1626  fix(sme): wire tenant + billing event dispatchers to NATS
#1627  fix(marketplace): post-purchase redirect to Sovereign-local console
#1628  fix(billing): skip Stripe when voucher covers 100%
#1629  fix(domain): per-tenant DNS reconciler — <slug>.<pool-domain> → Sovereign LB
#1630  fix(catalyst-api): mint HS256 token on SME proxy calls
#1631  feat(sandbox+bootstrap-kit): newapi Sovereign install (Bank Dhofar Qwen)
#1632  ci(sandbox): build workflows for controller + pty-server + mcp-server
```

---

## Lessons (now pinned in memory)

1. **Always check Build & Deploy Catalyst CI status after a merge.** A broken UI build does not fail the chart bump — it leaves a dangling image reference that no operator notices until `ImagePullBackOff` on a fresh prov. Mitigation: `gh run list --workflow="Build & Deploy Catalyst" --limit 1 --json conclusion` before any chart-values commit. (Wave 8.)

2. **Weighted slot accounting beats flat agent count.** Test-Executors (Playwright) cost ~1.5× a Fix-Author in RSS. Flat 6-agent ceiling worked for fix-only waves but failed when mixed with verifier walks. Dispatcher now tracks weighted sum ≤ 6.0. (OOM Incident 2.)

3. **Publish leg ≠ consume leg.** Wiring an event publisher to NATS does NOT mean any consumer is listening yet. After every dispatcher rewire, immediately audit (or open a follow-up issue for) each subscriber that should react. The convergence test is round-trip, not one-way. (PR #1626.)

4. **A new CRD/controller without a CI workflow that pushes its image is dead code.** Sandbox W1–W4 sat unusable for hours because no workflow built the three images the chart referenced. (PR #1632 had to backfill this.) When adding a new service, the **first** scaffold commit must include the `.github/workflows/build-<service>.yml`.

5. **Sub-agents inherit the design system — Sandbox UI proved it.** Wave 3 Sandbox UI (#1621) shipped clean PortalShell-wrapped components the first time, because the prompt to the Fix-Author included the design-system inheritance block verbatim. Contrast with the Family F BSS pages earlier in the day that needed a re-roll. (`feedback_subagents_inherit_design_system.md`.)
