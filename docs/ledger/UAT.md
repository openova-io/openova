# UAT — OpenOva Catalyst (living document — date-agnostic, continuously updated)

> **Standard User-Acceptance-Test walk** — the real OpenOva Catalyst end-user journeys filled into the UAT standard template ([`UAT-TEMPLATE.md`](UAT-TEMPLATE.md)).
>
> **Golden rule — this document is 100% the end-user's experience.** Every step is something a person does with their thumb or mouse on the shipped UI. No terminal, no `kubectl`, no API calls, no log greps, no code reading — those are the dev team's job (see Appendix A). A non-technical tester must be able to follow every row verbatim.
>
> **Walk order = the deterministic test** ([`../DOD.md`](../DOD.md) §2) — the ONLY order a fresh prov can actually be walked in: handover lands the operator first (D0), the operator issues the voucher (Phase 0), only then can a customer redeem it (Phase 1), day-2 operator journeys follow, the region-kill (D31) and the sovereignty cutover (§7) come last because both mutate the environment.
>
> **Scope ruling (founder, 2026-06-03):** Pillar 4 / Sandbox is **excluded** from this UAT. Pillars 1, 2, 3, 5 are covered in full. Wire-level proofs (counter-test, egress capture, HR state) are companions in Appendix A — they are **automated, NOT acceptance**.

---

## Metadata

| Field | Value |
|---|---|
| **Product / release** | OpenOva Catalyst — Sovereign `<sovereign-fqdn>` (active target: fresh **2-region active-hot-standby** prov **`hw93.omantel.biz`**, dep `1d4baac3d99337cc`, regions `me-east-215-a` + `-b`, carrying #3037–#3044. Prior hw91 (cutover wedge) + hw92 (single-region, broke on gitea-pg) both **wiped** 2026-06-04 — autonomous per the no-approval-needed principle) |
| **Build under test** | catalog `main` with the complete fresh-prov hardening chain merged: #2982 / #2985 / #2989 / #2999 / #2988+#3006 / #3004 / #3007 / #3008+#2940, **plus the 2026-06-04 convergence layer**: #3000 (Flux `remediation.retries: -1`) / #3012 (cutover Phase-3 issuer + console-URL pivots) / #3016–#3036 (controller-pin freshness, phase1-watch ready-gate #3018, catalog-seed CRD vocab, the sso-bridge-reconciler 5-layer saga 0.2.9–0.2.14, the gitea-OIDC-hook 6-layer saga 1.2.17–1.2.22) / **#3037** (harbor digest-reset) / **#3038** (vcluster reachable-kubeconfig + bp-sandbox dependsOn ns) / **#3039** (cutover `consolePublicURL` overlay — the Step-07 FATAL) / #3040 (comment accuracy) / **#3043** (vcluster kubeconfig cert-SAN — drop `.svc`) / **#3044** (vcluster-HR `storageNamespace`=target) |
| **Environment** | `https://console.<sovereign-fqdn>` (operator console) · `https://marketplace.<sovereign-fqdn>` (customer marketplace) · `https://console.<orgslug>.<pool-tld>` (tenant console; pool = `omani.homes` / `omani.rest` / `omani.trade`) |
| **Surface(s)** | Responsive web only. No native mobile app ships. |
| **Tester** | founder walk (or read-only Playwright verification agent — never an agent that ships fixes) |
| **Walk date** | **in flight** — 2-region prov `hw93` provisioning; walk begins on a serving console with **zero manual intervention**. Provisioning itself is now walked from the mothership console (Part 0). |
| **Overall verdict** | ☐ **NOT WALKED (2-region prov in flight)** — hw91 (cutover wedge) + hw92 (single-region; surfaced #3043 vcluster-SAN + #3044 storageNamespace, both fixed, then broke on a gitea-pg storage glitch) both wiped. Fresh **2-region** `hw93` provisioning, carrying #3037–#3044. Provisioning watched from the mothership console (Part 0); walk runs on console-200. |

**Result legend** (exactly one per Result cell): ✅ PASS *(evidence required)* · ❌ FAIL *(file defect, leave issue open)* · ⛔ BLOCKED *(couldn't attempt)* · ⏭️ N/A · ☐ NOT WALKED.

**Rules:** no ✅ without a committed screenshot link (`docs/sessions/<date>/evidence/`); walk top-to-bottom; the executor is **read-only** on the product and **never closes the issue**; report the screen you saw, not the one you expected; "looks right from the code" is banned; any manual cluster intervention fails the environment (fix at catalog source only).

---

## Status log — fresh-prov exposure runs (hw91 → hw92 → hw93)

> The walk has **not started** because no environment has yet reconciled to a serving console zero-touch. This log records what hw91 exposed, in order. Per founder doctrine each item is a catalog-source fix; hw91 was used read-only to expose, never hand-patched.

| When (UTC, 2026-06-04) | What | Resolution |
|---|---|---|
| ~00:15 | hw91 flipped `status=ready` at 39/54 HRs with console TCP-closed — **premature ready** | phase1-watcher hardened: `ready` only on explicit OutcomeReady, timeout → failed (#3018 → PR #3020) |
| ~01:00–05:00 | **bp-sso-bridge** had never worked on any fresh prov — its `kubectl` silently hit `localhost:8080` since G91.1, masked by `2>/dev/null` as "discovered 0 HRs" | 5-layer fix 0.2.9–0.2.14: SA-mount kubeconfig synthesis, bounded calls, phase narration, CiliumNetworkPolicy `toEntities: [kube-apiserver]`, KC `manage-clients` role |
| ~03:00–06:00 | **gitea OIDC post-install hook** (a never-executed path) yielded one defect per abstraction layer | 6-layer fix 1.2.17–1.2.22: DB-password ref, wait budgets, patient retry loop, real `--use-custom-urls` flag, `--auto-discover-url`, in-cluster KC discovery (the bootstrap-circle breaker) |
| ~06:30 | catalyst-platform install failed on catalog-seed CRD vocab (placement `clusters[]` + `cnpg-pair` enum) | PR #3036, validated via live `--dry-run=server` (14/14 seed CRs pass) |
| ~07:00 | **bp-harbor** stuck `installFailures=6` with a 9 h-stale ESO-webhook error — helm-controller backoff wedge | digest-reset marker bump 1.2.24 (**#3037**) |
| ~07:35 | bootstrap-kit health gated on two **host-namespace** HRs (`dmz/bp-coraza`, `rtz/bp-sandbox`) invisible to `-n flux-system` counts: vcluster exported `localhost:8443` kubeconfig + a namespace-less `dependsOn` | **#3038** — `exportKubeConfig.server` pinned to service DNS; `namespace: flux-system` on the dependsOn |
| ~07:20–08:15 | **The terminal blocker.** Cutover auto-fired at bootstrap (`trigger.auto`, founder rule) and FATAL'd at Step 07 on a missing `sovereign.consolePublicURL` overlay (a #3012 regression) → engine halted → registry tether **half-pivoted** (ghcr.io creds stripped, URLs not rewritten) → every fresh chart pull 401'd → HR count regressed 51→44 | **#3039** — `consolePublicURL: https://console.${SOVEREIGN_FQDN}` overlay (the missing lockstep half of #3012); #3040 corrects the mechanism comment |
| ~08:15 | hw91 is **unrecoverable**: the cutover that would heal the registry is gated on harbor → vcluster → the broken registry it would heal (chicken-and-egg). Confirmed failed env. | Fix stack independently reviewed **SOUND**; acceptance requires a **fresh prov**. |
| ~11:25 | **Fresh prov fired autonomously — no wipe.** Hetzner path proven cred-blocked (mothership carries only omantel Huawei creds). Single-region Huawei fits the HCS quota *headroom* alongside hw91, so hw91 is preserved (not wiped) and no destructive action was needed. | `POST /sovereign/api/v1/deployments` → **HTTP 201**, dep `f093724ef6899045`, `hw92.omantel.biz`, single-region `me-east-215-a`, carrying #3037/#3038/#3039. |
| ~11:25 → ~13:30 | hw92 (single-region) surfaced two more never-reached defects after the cutover layer: vcluster kubeconfig used the `.svc` cert-SAN form (TLS x509 mismatch on bp-coraza/bp-sandbox) and the vcluster HRs had no `storageNamespace` (release-store `namespaces "dmz"/"rtz" not found`). | **#3043** + **#3044** — both fixed at catalog source + validated live on hw92 (coraza→True). Then hw92's gitea-pg CNPG instance lost its initialized data dir (`pgdata: no such file or directory`) — a one-off storage glitch (4 sibling CNPG clusters healthy). |
| ~13:35 | **Course-correct (founder).** Single-region can't walk Pillars 2/3 (multi-region). hw91 + hw92 both **wiped autonomously** (no approval needed for Huawei resources except the bastion — now hard-coded in CLAUDE.md). | `POST .../{id}/wipe` ×2 → `status=wiped`. |
| ~15:08Z | **Watch #4.** HR 49/56; bootstrap-kit=True; bp-gitea=True; gitea-pg=Creating a new replica; img-stuck=2; console.hw93=000. Slow ghcr pulls draining; gitea-pg progressing. | Per every-tick rule. |
| ~15:11Z | **Watch #5 — last gate, self-healing.** HR 49/56; **bootstrap-kit=True** (first prov EVER to pass it — hw91/hw92 never did); bp-gitea=True. console.hw93=000. Final step `sovereign-tls`=False: postBuild needs ConfigMap `sovereign-tls-vars` (created by bp-catalyst-platform, still installing=installing); transient ordering, Flux self-heals on retry — NO hand-fix. cilium-gateway already Programmed=True. console-200 imminent once catalyst-platform finishes → ConfigMap → cert. | Per every-tick rule. |
| ~14:55 | **Watch #3 — slow-ghcr blip, self-resolved.** HR 47/56; bp-gitea=Unknown, gitea-pg=Creating a new replica; pods still ImagePull-stuck=2; console.hw93=000. The 45/56 plateau was a **throttled ghcr egress** — `ghcr.io/cloudnative-pg/postgresql:16` (253 MB) took **15m49s** to pull through the HCS NAT, not a hard failure. Transient per the HCS-early-prov lesson (no fix shipped). **FINDING:** a kyverno `harbor-proxy-pull/image-must-be-from-harbor-proxy` policy warns on the ghcr cnpg image pre-cutover (non-blocking — image pulled). Slow anon ghcr pulls on a fresh prov are a real soft-spot (candidate: imagePullSecret on cnpg, or pre-mirror). | Watching from mothership console; doc per every-tick rule. |
| ~14:35 | **hw93 converging cleanly (10-min watch #2).** HR **27/56 Ready** and climbing — NO vcluster-SAN / storageNamespace wedge (the layers hw92 stuck on), validating #3043/#3044 live. Non-ready = normal mid-convergence cohort (crossplane/cnpg/external-secrets/catalyst-platform). bootstrap-kit + sovereign-tls still False → console `000`. Mothership catalyst-api flapping (Error restarts, not OOM; recovers) — worked around, does not affect hw93's own convergence. | Watching from `console.openova.io/sovereign`; doc refreshed. |
| ~13:40 | **Fresh 2-region prov fired.** `active-hot-standby`, regions `me-east-215-a` + `-b`, carrying #3037–#3044. | `POST /sovereign/api/v1/deployments` → **HTTP 201**, dep `1d4baac3d99337cc`, `hw93.omantel.biz`. Now watched from the mothership console (Part 0); doc refreshed ~every 10 min. |

**UAT issue status (all four code-complete + reviewer VERIFIED-PASS, awaiting only the live walk):** #2742 (TC-18 endpoint→PR propagation), #2744 (TC-17 Tier-1 silent-SSO), #2940 (TC-26 post-cutover regression / 18-tether audit — re-verified code-complete 2026-06-04), #2951 (cutover Step-6 per-Blueprint defaults). All remain `status/uat`; none walked, because every surface returns HTTP `000` (gateway never bound — 5 Playwright timeouts + 4 curl probes logged). They close only after the founder's walk-with-screenshot, per [`../../CLAUDE.md`](../../CLAUDE.md) Rule 6.

---

## Coverage contract

Every committed end-user behavior for pillars 1, 2, 3, 5 maps to a TC row below. Cross-reference:

| Committed behavior | Source of commitment | TC |
|---|---|---|
| Handover auto-redirect, no FQDN typing | DoD **D0** | TC-01 |
| PIN login chain | DoD D2/D3/D4 | TC-02 |
| Operator pre-seeded owner-tier; settings real values; route hygiene | DoD D21/D22/D23/D24 | TC-03 |
| Marketplace enabled, zero-touch | DoD **D27** | TC-04 |
| One-click voucher issuance + email | DoD **D28**, §5.4 O1–O2 | TC-05 |
| Canonical redeem URL + validity card (+ bad-code rejection, #2941) | DoD Phase 1a, Pillar 1 | TC-06 |
| Plan + app selection | Pillar 1, §5.5 | TC-07 |
| Free-subdomain picker from operator pool | DoD **D30** | TC-08 |
| BCP topology choice **at signup** | **Pillar 2** (acceptance row) | TC-09 |
| Zero-touch org provisioning off a voucher | DoD **D29**, #2000 integrity | TC-10 |
| Org online: tenant console + TLS + first app SSO "Open" | DoD Phase 1c/2a, §5.5 step 7 | TC-11 |
| Operator sees the new tenant (BSS) | DoD §5.4 O3 | TC-12 |
| Multi-region dashboard + cloud views | DoD D5/D15/D16 | TC-13 |
| Jobs terminal + region filter | DoD D6/D20 | TC-14 |
| Catalog class/instance split | #2741 (G117) | TC-15 |
| **3 coexisting instances** of one Blueprint | #2737 DoD #5, #2745 | TC-16 |
| Launch silent-SSO — Tier-1 sweep (grafana, gitea, harbor, openbao) | #2743, #2744, G113 | TC-17 |
| Endpoint edit → governed Git PR → **full propagation < 2 min** | #2742, #2737 DoD #4, ADR-0009 | TC-18 |
| RBAC surfaces render real data | EPIC-3, D21 | TC-19 |
| Operator-facing hostnames all serve | DoD **D25** | TC-20 |
| **Cross-Org realm isolation** (two Orgs, distinct sessions) | #2744, #2737 DoD #6 | TC-21 |
| CNPG-backed app, active-hot-standby, both regions visible | DoD **D31** (UI side), Pillar 3 | TC-22 |
| Region-kill: app stays reachable (RTO ≤ 30 s) | DoD D31 + §6, Pillar 3 | TC-23 |
| "Achieve True Sovereignty" CTA + modal | Pillar 5, DoD §7 step 2 | TC-24 |
| Cutover completes: badge flips, egress self-test green | Pillar 5, DoD §7 steps 3–5 | TC-25 |
| **Post-cutover regression** (PIN, SSO, tenant, marketplace still work) | #2940 (the `iss`-tether attack) | TC-26 |

Deliberate exclusions are listed in Appendix B with reasons. Anything user-facing and committed that is NOT in this table or Appendix B is a defect of this document — file it.

---

## Part 0 — Provisioning, watched from the mothership console *(founder directive 2026-06-04)*

> **The end-user watches their own Sovereign being born.** Before handover (D0), the operator who ordered the Sovereign watches it provision **from the mothership web console** at `https://console.openova.io/sovereign`. This is a first-class UAT surface, walked **like the end-user** — no `kubectl`, no API: the operator sees exactly what the mothership shows them. While a prov is in flight, the executor refreshes this view and updates this document **every ~10 minutes**.
>
> **Live executor watch — hw93 (dep `1d4baac3d99337cc`), 2026-06-04 ~14:20Z:** signed into `console.openova.io/sovereign` as the operator, watched the provisioning. **Cloud view confirms the full 2-region topology** — `Cloud: huawei`, `Region 2/2` (`me-east-215-a` + `-b`), `Cluster 2/2`, `WorkerNode 8/8` (4 per region), `LoadBalancer 2/2`, `Network 2/2` ([screenshot](../sessions/2026-06-04/evidence/hw93-mothership-cloud-2regions.png)). Jobs view: tofu init/plan/apply/output + cluster-bootstrap **Succeeded**, Apps/Cutover/Handover **Pending** ([screenshot](../sessions/2026-06-04/evidence/hw93-mothership-jobs-2region.png)). **DEFECT (cosmetic):** the Jobs parent group is labelled **"Provision Hetzner"** for a **Huawei** deployment — provider-label bug, infra is genuinely Huawei (Cloud chip = `huawei`).

### TC-00a — The deployment appears and shows the RIGHT topology *(web · operator · mothership console)*

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | `https://console.openova.io/sovereign` | Sign in (operator PIN), open the deployments list | Your in-flight deployment listed with `status: provisioning` | ☐ | — |
| 2 | The deployment row | Open it (`/sovereign/provision/<dep-id>`) | Header shows the Sovereign FQDN + **the BCP topology you ordered** (e.g. `active-hot-standby`) | ☐ | — |
| 3 | The provision overview | Read the **regions** | **Exactly the regions ordered** — an active-hot-standby Sovereign MUST show **2 regions** (e.g. `me-east-215-a` + `-b`), NOT 1. *(A single-region prov here = wrong topology, ❌ — founder caught this 2026-06-04.)* | ✅ (executor-observed) | [cloud-2regions](../sessions/2026-06-04/evidence/hw93-mothership-cloud-2regions.png) |

### TC-00b — The provisioning jobs run and complete per-region *(web · operator · mothership console)*

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | `/sovereign/provision/<dep-id>/jobs` | Read the jobs list | Jobs grouped/labelled **per region** — every install job present for **each** region (an `active-hot-standby` Sovereign shows each job for both regions) | ☐ | — |
| 2 | A specific job (e.g. `jobs/install-velero-hcs`) | Open it | Job detail/log streams; **both regions** represented (not just one) | ☐ | — |
| 3 | The jobs view over ~the prov window | Refresh every ~10 min | Jobs advance to success; no job stuck failed; the doc Status-log is updated each refresh | ☐ | — |

### TC-00c — Convergence → handover hand-off *(web · operator · mothership console)*

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | The provision view | Wait through convergence | `status` advances `provisioning → … → ready` only when the console is genuinely serving (no premature ready, #3018) | ☐ | — |
| 2 | On `ready` | Follow the handover hand-off | Auto-redirect into the Sovereign console (continues at **TC-01**), zero FQDN typing | ☐ | — |

---

## Part I — Handover & operator first-touch

### TC-01 — Handover auto-redirect *(web · operator · D0)*

- **Persona:** P2 `sovereign-admin` who just submitted the provision wizard, watching the progress page.
- **Goal:** *"As the operator, when my Sovereign is ready I want to land on its console automatically, without copying or typing any FQDN."*
- **Preconditions:** deployment `38ae2b82dc325354` provisioning; operator signed in on the mothership.

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | [console.openova.io/sovereign/provision/38ae2b82dc325354](https://console.openova.io/sovereign/provision/38ae2b82dc325354) | Watch the provisioning progress page (no clicks) | Per-region stage rows advance; **no stage stuck Pending after handover fires**; `Apps` / `Handover` rows reach Succeeded (or n/a) | ☐ | — |
| 2 | Same page, at `ready` | Do nothing — wait | Browser **auto-redirects** to the Sovereign Console (`/auth/handover?token=…` → console) — you never type `hw91.omantel.biz` | ☐ | — |

- **Journey verdict:** ☐

### TC-02 — Operator PIN sign-in *(web · operator · D2/D3/D4)*

- **Preconditions:** TC-01 done, or operator opens the console URL directly in a fresh browser.

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | [console.hw91.omantel.biz](https://console.hw91.omantel.biz) | Open the URL | Page loads over **publicly-trusted TLS** (no browser warning) and shows **"Sign in"** with *"Enter your email to receive a 6-digit PIN."* | ☐ | — |
| 2 | Sign in | Type the operator email, tap **Send code** | Advances to the PIN entry screen, email shown | ☐ | — |
| 3 | PIN entry | Type / paste the 6-digit PIN from the email | Auto-submits on the 6th digit → lands on **/dashboard**, authenticated | ☐ | — |
| 4 | /dashboard | Refresh the browser (F5) | Still signed in — no re-PIN within session TTL (D14) | ☐ | — |

- **UI source:** `LoginPage.tsx:101` ("Sign in" + subtitle), `VerifyPinPage.tsx` (6-digit auto-submit).
- **Journey verdict:** ☐

### TC-03 — First-touch sanity *(web · operator · D21/D22/D23/D24)*

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | /dashboard | Look at the URL bar after login | You are on **/dashboard** — NOT `/wizard` (D23) | ☐ | — |
| 2 | Sidebar | Scan the sidebar entries | NO mothership-only views: no fleet dashboard, no "+ New deployment" (D24) | ☐ | — |
| 3 | [/users](https://console.hw91.omantel.biz/users) | Open **User Access** | The operator who just PIN-logged-in is listed with **tier=owner** and their email — list NOT empty (D21) | ☐ | — |
| 4 | [/settings](https://console.hw91.omantel.biz/settings) | Open **Settings** | Real values for Region, Capacity, Control-plane size, Created, Deployment ID, Pool subdomain — no `—` placeholders, no "API PENDING" (D22) | ☐ | — |

- **UI source:** `UserAccessListPage.tsx` ("User Access"), `SettingsPage.tsx`.
- **Journey verdict:** ☐

---

## Part II — Phase 0: operator issues a voucher (Pillar 1)

### TC-04 — Marketplace is live *(web · operator · D27)*

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | [marketplace.hw91.omantel.biz](https://marketplace.hw91.omantel.biz) | Open the URL | Marketplace landing renders with a **non-empty catalog** — not 404, not a "marketplace disabled" stub (zero-touch default, D27) | ☐ | — |

- **Journey verdict:** ☐

### TC-05 — Issue a voucher from BSS *(web · operator · D28, Phase 0)*

- **Goal:** *"As the operator, I issue a prepaid voucher to a customer in one click — no terminal, no API."*

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | Sidebar | Tap **BSS** | **"BSS — Business Support Systems"** landing: KPI strip + section grid incl. **Vouchers** and **Tenants** | ☐ | — |
| 2 | [/bss/vouchers](https://console.hw91.omantel.biz/bss/vouchers) | Tap **+ Issue voucher** | Modal **"Issue voucher"** with fields: **Code** (e.g. `LAUNCH2026`), **Credit (OMR)**, **Description (optional)**, **Max redemptions (optional)**, **Recipient email (optional)** | ☐ | — |
| 3 | Issue voucher modal | Fill Code + Credit + the test recipient email, tap **Issue voucher** | Button shows "Issuing…", modal closes, new row appears in the voucher table with status **Active** | ☐ | — |
| 4 | Recipient inbox | Open the voucher email | Email arrived via the **Sovereign's own SMTP**; link is exactly `https://marketplace.hw91.omantel.biz/redeem/?code=<CODE>` (slash before `?` mandatory) | ☐ | — |

- **UI source:** `BssLandingPage.tsx`, `VouchersPage.tsx:136` (list), `:529-615` (modal fields verbatim).
- **Journey verdict:** ☐

---

## Part III — Phase 1: customer onboarding (Pillars 1 + 2)

### TC-06 — Redeem the voucher *(web · customer · Phase 1a)*

- **Persona:** P5 SME customer (Ahmed) on his phone, opening the email.

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | [marketplace…/redeem/?code=`<CODE>`](https://marketplace.hw91.omantel.biz/redeem) | Open the redeem link from the email | **"Voucher valid"** card with the **OMR credit** amount + the code | ☐ | — |
| 2 | Redeem page (negative path) | Edit the URL to a garbage code and reload | **"Voucher not valid"** state — clear message, no crash, no credit shown (#2941 hardening) | ☐ | — |
| 3 | Back on the valid voucher card | Tap **Sign up to redeem** | Advances to **Pick a plan** (/plans); code carried to checkout | ☐ | — |

- **UI source:** `redeem.astro` (states: "No voucher code" / "Voucher not valid" / "Campaign ended" / "Voucher valid").
- **Journey verdict:** ☐

### TC-07 — Pick plan and apps *(web · customer · Pillar 1)*

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | [/plans](https://marketplace.hw91.omantel.biz/plans) | On **"Pick a plan"**, tap a plan card, tap Continue | Advances to **"Build your stack"** (/apps) | ☐ | — |
| 2 | [/apps](https://marketplace.hw91.omantel.biz/apps) | Select at least one **Postgres-backed** app (the canonical bundle), tap Continue | Advances to **"Setup & extras"** (/addons) | ☐ | — |

- **UI source:** `PlanStep.svelte` ("Pick a plan"), `AppsStep.svelte` ("Build your stack").
- **Journey verdict:** ☐

### TC-08 — Choose the free subdomain *(web · customer · D30)*

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | [/addons](https://marketplace.hw91.omantel.biz/addons) | On **"Setup & extras"**, find **Your domain** | Subdomain field + a **pool picker** offering the operator-curated TLDs (`omani.homes` / `omani.rest` / `omani.trade`) — pool from config, not hardcoded (D30) | ☐ | — |
| 2 | Your domain | Type a 2-character subdomain | Inline rejection ("at least 3 characters") — Continue stays blocked | ☐ | — |
| 3 | Your domain | Type a valid subdomain (e.g. `muscatpharmacy`), pick any **Optional extras**, Continue | Subdomain accepted; advances to **Business continuity** (/bcp) | ☐ | — |

- **UI source:** `AddonsStep.svelte` ("Setup & extras", "Optional extras").
- **Journey verdict:** ☐

### TC-09 — Choose BCP topology at signup *(web · customer · **Pillar 2 acceptance**)*

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | [/bcp](https://marketplace.hw91.omantel.biz/bcp) | Read the step | Heading **"Business continuity"** + *"Pick how your database should survive a regional outage"*; two topology cards: **Single-region** and **Active-hot-standby** | ☐ | — |
| 2 | Topology cards | Tap **Active-hot-standby** | **Primary region** and **Replica region** pickers appear, each with real region names | ☐ | — |
| 3 | Region pickers (negative path) | Pick the SAME region for both | Inline error **"Primary and replica must differ"** — Continue blocked | ☐ | — |
| 4 | Region pickers | Pick two different regions, Continue | Advances to **"Review & launch"** (/review) | ☐ | — |

- **UI source:** `BCPStep.svelte` (all quoted strings verbatim).
- **Journey verdict:** ☐ *(This is the Pillar-2 acceptance surface — BCP chosen at signup, never a Day-2 upgrade.)*

### TC-10 — Review, checkout, create the Organization *(web · customer · D29)*

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | [/review](https://marketplace.hw91.omantel.biz/review) | On **"Review & launch"**, check **Your stack**, **Plan**, **Expected usage**, **Tenant** | All reflect the choices made in TC-07/08/09, incl. the two regions and the voucher credit | ☐ | — |
| 2 | /review | Tap Checkout | Advances to **"Checkout"** | ☐ | — |
| 3 | [/checkout](https://marketplace.hw91.omantel.biz/checkout) | Sign in: type the customer email, request the code, enter the 6-digit PIN | PIN accepted; the voucher **credit is applied** to the total | ☐ | — |
| 4 | Checkout | Confirm | **"Setting up your tenant"** progress, then **"Your tenant is ready!"** — never "Provisioning failed" (D29: zero operator touch) | ☐ | — |

- **UI source:** `ReviewStep.svelte` ("Review & launch", "Your stack", "Plan", "Expected usage", "Tenant"), `CheckoutStep.svelte` ("Checkout", "Setting up your tenant", "Your tenant is ready!").
- **Journey verdict:** ☐

### TC-11 — The Organization is online *(web · customer · Phase 1c + 2a)*

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | "Your tenant is ready!" | Follow the tenant link (or auto-redirect) | Lands on `https://console.<orgslug>.<pool-tld>` with **publicly-trusted TLS** on the chosen subdomain (D30 closure) | ☐ | — |
| 2 | Tenant console | PIN-login as the customer | **Dashboard renders** (Phase 2a) — not an empty/error page | ☐ | — |
| 3 | Tenant apps view | Look at the app cards | The apps chosen in TC-07 appear as cards with status badges that go green (~minutes) | ☐ | — |
| 4 | A green app card | Tap **Open** | The app itself opens, **already signed in** via the Org's realm — no separate login form (§5.5 step 7) | ☐ | — |

- **Journey verdict:** ☐

### TC-12 — Operator sees the new tenant *(web · operator · O3)*

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | [/bss/tenants](https://console.hw91.omantel.biz/bss/tenants) | Open **BSS → Tenants** | The new Organization appears with its chosen pool subdomain | ☐ | — |
| 2 | [/bss/vouchers](https://console.hw91.omantel.biz/bss/vouchers) | Find the issued voucher row, expand it | Drawer shows **Redemptions** incremented exactly once (#2000: decrement only on checkout success) | ☐ | — |

- **Journey verdict:** ☐

---

## Part IV — Operator day-2 console (G117 + D-gates)

### TC-13 — Multi-region dashboard & cloud views *(web · operator · D5/D15/D16)*

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | [/dashboard](https://console.hw91.omantel.biz/dashboard) | Set Layer-1 = Cluster | **One bubble per region** (2 on hw91) — not a single merged Sovereign bubble (D16) | ☐ | — |
| 2 | /dashboard | Set Layer-2 = Namespace | Namespace bubbles render **within** each cluster bubble | ☐ | — |
| 3 | [/cloud?view=graph](https://console.hw91.omantel.biz/cloud?view=graph) | Read the kind chips | **All regions** present, no stuck spinners (D5); no kind chip shows `0/0` for resources that exist (D15) | ☐ | — |
| 4 | /cloud, any resource cell | Click a leaf cell | Drill-down opens the resource detail — clicks work on leaves (PR #1085 anti-pattern guard) | ☐ | — |

- **Journey verdict:** ☐

### TC-14 — Jobs are terminal and region-filterable *(web · operator · D6/D20)*

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | [/jobs](https://console.hw91.omantel.biz/jobs) | Open **Jobs** | **0 pending, 0 running** — every job in a terminal state (D6, post-convergence) | ☐ | — |
| 2 | /jobs | Read job rows | Per-region prefixes visible on a multi-region Sovereign; the filter narrows to one region (D20) | ☐ | — |

- **Journey verdict:** ☐

### TC-15 — Catalog: class page and instance table *(web · operator · #2741)*

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | [/apps](https://console.hw91.omantel.biz/apps) | Open **Applications**, switch to the **Catalog** tab | Blueprint **class** cards (one per Blueprint, NOT one per instance) | ☐ | — |
| 2 | A class card (e.g. Grafana) | Click it | **Catalog detail**: Blueprint header + **supported-topology list** + an **instance table** + a **"+ New instance"** affordance | ☐ | — |

- **UI source:** `AppsPage.tsx:6` (tabs "Deployments"/"Catalog"), `CatalogDetail.tsx` ("+ New instance").
- **Journey verdict:** ☐

### TC-16 — Three coexisting instances of one Blueprint *(web · operator · #2737 DoD #5)*

- **Goal:** *"As the operator, I can run three Grafanas side-by-side, each with its own URL and storage."*

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | Catalog detail (Grafana) | Tap **+ New instance**, complete the install form, repeat **twice more** (3 total) | Each install accepted — no name-collision crash; 409 with a clear message only on a genuinely duplicate name | ☐ | — |
| 2 | Catalog detail (Grafana) | Read the instance table | **3 rows**, each with its own name + status | ☐ | — |
| 3 | Each instance row | Open each instance's detail → its endpoint | **3 distinct URLs**, each serving its own Grafana (change a dashboard in one — the others unchanged) | ☐ | — |

- **Journey verdict:** ☐

### TC-17 — Launch silent-SSO: Tier-1 sweep *(web · operator · #2743/#2744)*

- **Goal:** *"Every Launch click opens the app already signed-in — no Keycloak form, ever."*

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | App detail — **Grafana** | Tap **Launch →** | New tab opens **already signed in** (silent SSO `prompt=none&kc_idp_hint=catalyst-pin`) — no login form, < ~1 s | ☐ | — |
| 2 | App detail — **Gitea** | Tap **Launch →** | Same: signed-in Gitea, no form | ☐ | — |
| 3 | App detail — **Harbor** | Tap **Launch →** | Same: signed-in Harbor, no form | ☐ | — |
| 4 | App detail — **OpenBao** | Tap **Launch →** | OpenBao opens authenticated via OIDC (architectural note: no kc_idp_hint pin — one redirect hop allowed, but **no credential prompt**) | ☐ | — |

- **UI source:** `AppDetail.tsx` LaunchButton ("Launch →", aria "Launch app via silent SSO").
- **Coverage note:** Tier-2/Tier-3 apps (13 more, #2744) follow the same contract; walk any 2 as a sample and record which.
- **Journey verdict:** ☐

### TC-18 — Endpoint edit → governed PR → full propagation *(web · operator · #2742, #2737 DoD #4)*

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | App detail → Connection | Rename an endpoint (e.g. `grafana` → `metrics`), save | UI confirms the change was submitted **as a Git PR** — not applied silently | ☐ | — |
| 2 | Gitea (via TC-17 step 2 session) | Open the Org's `iac` repo → Pull requests | The PR exists with **3 named checks**: `kyverno-admission`, `cert-manager-precheck`, `dns-conflict-precheck` | ☐ | — |
| 3 | The PR | Watch checks complete | All 3 green → PR **auto-merges** | ☐ | — |
| 4 | Browser, ≤ 2 min after merge | Open `https://metrics.<…>` (the NEW name) | New FQDN serves with **valid TLS** and **silent SSO still works** (redirect_uri followed the rename) — full propagation, not just PR-open | ☐ | — |

- **Journey verdict:** ☐

### TC-19 — RBAC surfaces *(web · operator · EPIC-3)*

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | [/rbac/roles](https://console.hw91.omantel.biz/rbac/roles) | Open **Keycloak Roles** | Real role catalog renders (not empty/error) | ☐ | — |
| 2 | [/rbac/matrix](https://console.hw91.omantel.biz/rbac/matrix) | Open **Access matrix** | Subjects × roles grid with the owner-tier operator visible | ☐ | — |
| 3 | [/users/new](https://console.hw91.omantel.biz/users/new) | Create a second operator user (Name, Email, Roles), save | Success toast → user appears in **User Access** list | ☐ | — |

- **Journey verdict:** ☐

### TC-20 — Operator-facing hostname sweep *(web · operator · D25)*

Visit each URL in a tab; each must render **its app page** — not 404, not a blank stub, not a dev hostname:

| # | URL | Must see | Result | Evidence |
|---|---|---|---|---|
| 1 | `https://keycloak.hw91.omantel.biz` | Keycloak page | ☐ | — |
| 2 | `https://openbao.hw91.omantel.biz` | OpenBao UI | ☐ | — |
| 3 | `https://grafana.hw91.omantel.biz` | Grafana (login or SSO) | ☐ | — |
| 4 | `https://gitea.hw91.omantel.biz` | Gitea | ☐ | — |
| 5 | `https://harbor.hw91.omantel.biz` / `registry.…` | Harbor | ☐ | — |
| 6 | `https://guacamole.hw91.omantel.biz` | Guacamole | ☐ | — |
| 7 | `https://marketplace.hw91.omantel.biz` | Marketplace | ☐ | — |

- **Journey verdict:** ☐

### TC-21 — Cross-Org realm isolation *(web · two customers · #2744, #2737 DoD #6)*

- **Preconditions:** A second voucher (repeat TC-05) redeemed into a **second Organization** (repeat TC-06…TC-11 minimally).

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | Browser profile A | Sign in to Org-A's console | Org-A dashboard | ☐ | — |
| 2 | Same profile A | Open Org-B's console URL | **A fresh sign-in is demanded** — Org-A's session does NOT open Org-B (separate realms) | ☐ | — |
| 3 | Profile A, Org-A's Grafana vs Org-B's Grafana | Open both app URLs | Org-A's opens signed-in; Org-B's demands sign-in — per-Org SSO isolation holds | ☐ | — |

- **Journey verdict:** ☐

---

## Part V — Pillar 3: region-kill failover (D31)

### TC-22 — Install a CNPG-backed app in active-hot-standby *(web · customer · D31 setup)*

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | Tenant marketplace/catalog | Pick a CNPG-backed app (e.g. WordPress/Ghost), choose **active hot-standby**, install | Install starts; app card progresses to green | ☐ | — |
| 2 | App detail → Topology | Read the placement | The app shows under **both regions** — primary + replica, topology reads **active-hot-standby** | ☐ | — |
| 3 | The app's FQDN | Open it | App serves with trusted TLS | ☐ | — |

- **Journey verdict:** ☐

### TC-23 — Region-kill: the app survives *(web · customer+operator · D31)*

- **Preconditions:** TC-22 green. The **kill itself** is a dev-team action via the cloud-provider API (a REAL region kill — Appendix A.1); the walker observes only URLs.

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | The app's FQDN | Confirm it serves, note the time | HTTP 200, content loads | ☐ | — |
| 2 | (Dev team kills the primary region) | Keep refreshing the app URL | Service resumes within **≤ 30 s** — same FQDN, no manual DNS change | ☐ | — |
| 3 | Operator /dashboard | Read the region bubbles | Failed region shows unhealthy; surviving region carries the app | ☐ | — |
| 4 | The app | Log in / use it | Data written before the kill is **all present** (zero loss — wire proof in Appendix A.1) | ☐ | — |

- **Journey verdict:** ☐

---

## Part VI — Pillar 5: sovereignty cutover (§7)

> **Known coverage finding (filed during doc authoring):** the production console does **not yet mount** the Sovereignty card — the "Achieve True Sovereignty" CTA exists only at the layout-free harness route `/sovereignty/preview` (#793; `router.tsx:1140-1142`, widget `SovereigntyCard.tsx`). Until the card is mounted on a production page (Dashboard or Settings), TC-24 step 1 walks the harness route and the missing mount is an ❌ against the committed §7 step-2 surface.

### TC-24 — Trigger the cutover *(web · operator · §7 step 2)*

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | The Sovereignty surface (production mount; fallback `/sovereignty/preview`) | Read the card | **"Sovereignty status"** with badge **"Soft-tethered to mothership"** + primary CTA **"Achieve True Sovereignty"** | ☐ | — |
| 2 | The card | Tap **Achieve True Sovereignty** | An explanation modal opens first — the cutover is **irreversible**; nothing starts without confirmation | ☐ | — |
| 3 | The modal | Confirm | Progress card appears; steps advance (e.g. **Mirrored commit**, **Harbor projects**, …, **Egress test**) — 8 sequential steps, no manual touch | ☐ | — |

- **UI source:** `SovereigntyCard.tsx` (badge texts, CTA, `data-testid="cutover-start-button"`, step labels).
- **Journey verdict:** ☐

### TC-25 — Cutover completes *(web · operator · §7 steps 4–5)*

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | Progress card | Wait through the final step | **"Egress test"** runs the 10-minute deny-egress hold and passes | ☐ | — |
| 2 | Sovereignty card | Read the badge | Flips to **"Independent"** — `cutoverComplete=true` with the hold timestamp | ☐ | — |

- **Journey verdict:** ☐ *(Wire-level egress + HR-green proofs: Appendix A.2 — companions, not substitutes.)*

### TC-26 — Post-cutover regression: nothing broke *(web · operator+customer · #2940)*

- **Goal:** *"Independence must not cost a single working flow"* — the #2940 attack is a cutover that 'succeeds' while tokens still carry a mothership issuer, silently breaking SSO.

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | Fresh browser | PIN-login at console.hw91.omantel.biz | Works exactly as in TC-02 — the PIN/JWT issuer is now the Sovereign's own | ☐ | — |
| 2 | App detail — Grafana | Tap **Launch →** | Silent SSO still works — no login form, no redirect loop | ☐ | — |
| 3 | Tenant console | Customer PIN-login + open an app | Tenant flows unaffected | ☐ | — |
| 4 | Marketplace | Open /redeem with a fresh voucher | Voucher flow still works end-to-end post-cutover | ☐ | — |

- **Journey verdict:** ☐

---

## Summary

| Part | TCs | Verdict |
|---|---|---|
| I — Handover & first-touch | TC-01…03 | ☐ |
| II — Phase 0 voucher | TC-04…05 | ☐ |
| III — Phase 1 customer | TC-06…12 | ☐ |
| IV — Operator day-2 | TC-13…21 | ☐ |
| V — Pillar 3 failover | TC-22…23 | ☐ |
| VI — Pillar 5 cutover | TC-24…26 | ☐ |

**Overall verdict:** ☐ **NOT WALKED (prov in flight)** — hw91 joined the failed-env class (cutover Step-07 half-pivot, unrecoverable). Fresh single-region Huawei prov `hw92` (dep `f093724ef6899045`) fired autonomously **without a wipe**, carrying #3037/#3038/#3039, converging zero-touch toward console-200. The walk begins the moment it serves. Failed environments are read-only exposure only, never acceptance.

---

## Appendix A — wire-level companions (automated, **NOT acceptance**)

These are dev-team evidence procedures that accompany specific TCs. They never replace a UI row.

- **A.1 — D31 counter-test** (companion to TC-23): monotonic `INSERT … RETURNING id` writer @100 ms through the kill; pass = no gap/replay, RTO ≤ 30 s; failover by `failover-controller` from the Continuum CR, never manual DNS. Kill modes: cloud-API instance destroy or NetworkPolicy region isolation. (DoD §6.)
- **A.2 — Egress-block proof** (companion to TC-25): Hubble/NetworkPolicy capture showing zero allowed flows to `github.com` / `ghcr.io` / `harbor.openova.io` for the full 10-minute hold + HelmReleases all Ready at minute 0 and minute 10. (DoD §7.)
- **A.3 — Convergence gates D8–D12** (precondition to the whole walk): all HRs Ready, ClusterMesh OK, inter-region pod-to-pod, LB-only services.
- **A.4 — D34 newapi**: org-scoped JWT → `POST /v1/chat/completions` round-trip to the partner-hosted Qwen backend, no cloud-LLM egress.
- **A.5 — D35 NATS**: voucher→redeem publish/consume round-trip ≤ 2 s.

## Appendix B — committed but deliberately not walked here

| Item | Why excluded | Tracking |
|---|---|---|
| Pillar 4 — Sandbox + `openova-sandbox-mcp` (Phase 2b–2e, D32/D33) | **Founder ruling 2026-06-03**: out of this UAT's scope | DoD §1 Pillar 4; regression contract: `tests/e2e/sandbox-mcp-contract.sh` (#2930) |
| Corporate Git journeys (J7–J9: Blueprint authoring, promotion approvals — Layla §5.7) | Git surface is design-stage per STATUS.md §5 — not walkable | DoD §5.7 |
| Compliance scorecard depth (EPIC-1 dashboards beyond render) | Needs policy/Falco data seeded; tertiary operator-debugger surface per CLAUDE.md §pillar-rules | `/sre/compliance`, `/sec/compliance` routes exist |
| Voucher rate-limit/entropy hardening beyond the bad-code row | Rate-limit behavior not UI-observable deterministically | #2941 |
| Tier-2/Tier-3 silent-SSO full 17-app sweep | Tier-1 (4 apps) + 2-app sample walked in TC-17; full sweep is a Playwright regression, not a human walk | #2744 |

---

*Authored 2026-06-03 (rev 2). Sources: DoD §§1-7 (gates D0–D35), STATUS.md, ARCHITECTURE.md §10, G117 EPIC #2737 family, TRUST.md ledger; all UI labels verified against `core/marketplace/src/` and `products/catalyst/bootstrap/ui/src/` at main.*
