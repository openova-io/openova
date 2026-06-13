# UAT WALK-THROUGH — the agreement document (2026-06-14)

**Purpose.** This is the **end-user web-UI acceptance test** for every founder-approved ticket. **100% browser — no terminal, no kubectl, no git, no curl.** The end user never touches a shell; every step is **open a URL → click/type → see a screen**. Once the founder **agrees on this document**, the walk-execution agents run each section in a real browser and flip each `☐` to ✅ (witnessed on screen) or ❌ (failed on screen) with a screenshot.

**Table format (mandated).** Each section is a 4-column table — **Tested page · Description · Status · Evidence**. Four narrow columns fit without horizontal scrolling (the developer `file:line` column is gone — it's irrelevant to a user). **Every "Tested page" cell is a clickable link** to the actual live page on the current env (**hw133.omani.works**) — click it and the page opens. On each wipe + re-walk the links flip to the new env. The walk agent fills **Status** (✅/❌) and pastes the screenshot link into **Evidence**.
- **`GAP`** in a row = the feature has **no web-UI surface** — so a user cannot accept it. That is itself a finding (a feature with no UI is not user-acceptance-testable); it is **not** a reason to drop to a terminal check.
- A **redirect that ends on a login screen is a fail** for SSO — only a rendered, signed-in screen counts.

**Acceptance bar.** Every walk runs in a browser against a **fresh, zero-touch, converged** Sovereign (no hand-patching). Evidence = **screenshots** under `docs/sessions/<date>/evidence/`, linked from `docs/ledger/UAT.md`. PR-merge ≠ done; only a witnessed on-screen walk is done.

---

## 🔴 WALK RESULTS — live on hw133.omani.works, 2026-06-14 (browser walk, 188 screenshots)

Each ticket walked in a real browser per its section; evidence under `docs/sessions/2026-06-14/evidence/<ticket>/`.

| Ticket | ✅ | ❌ | Headline finding |
|---|---|---|---|
| **#3370** CONTEXTS | 7 | 12 | Contexts table works; but **PostgreSQL has no catalog card**, the **3 shared-PG have no Deployment cards**, the **new-instance backing/reuse wizard is absent**, gitea Dependencies doesn't show the binding, valkey Contexts empty |
| **#3373** PLACEMENT | 43 | 20 | Proven on the treemap: **only 6 of 26 vCluster-targeted apps are actually in their vCluster** — keycloak/gitea/harbor/grafana/openbao/3×shared-PG/valkey/seaweedfs are **still on host** (migration incomplete) |
| **#3374** SSO | 11 | 11 | console/grafana/gitea/harbor/guacamole/keycloak/marketplace land signed-in; **openbao (postMessage), pdns-admin (500), openova-flow (500 — NEW regression), hubble (503), newapi (uninitialized) all broken**; gitea admin = 404; guacamole no admin |
| **#3375** TOPOLOGY/DR | 0 | 87 | **The "Topology" tab is a generic placement EDITOR, not a declaration/DR display** — it never reads back any app's matrix topology; **there is no Switchover button at all**; many apps "not part of this deployment". No row is user-verifiable |
| **#3376** FUNNEL | 6 | 11 | Wizard front + email-code send work; **`/api/billing/vouchers/redeem-preview` → 502** (billing down) kills the funnel; voucher invalid, tenant never created, `walmart.omani.homes` connection-refused |
| **#3378** ORGANIZATIONS | 13 | 3 | Menu/parent/showback/internal-door/redirects work; **mis-badge confirmed live** (Internal org persists as customer/real/vcluster); **Enter-org lands blank** (sub-org not deployed); console plans table 404 |
| **#3379** SOVEREIGNTY | 4 | 2 | Console + apps work (baseline holds); **no cutover UI/indicator exists**; cutover has never run on this env |
| **#3380** ROBUSTNESS | 2 | 2 (+2 ☐) | 49/49 show INSTALLED, but that's the install-record, not runtime; **grafana login "Account disabled" (400)**, SME/billing back-half crash-loop a user feels; wipe row destructive (not walked) |

**Net: ≈ 86 ✅ / 148 ❌.** The walk confirmed the founder's read — most of the headline delivery is broken or unbuilt on the live env, with #3375 (no topology UI at all) and #3376 (funnel dead at billing) the worst.

---

## ⚠️ CROSS-TICKET GAP SUMMARY — read this first (the honest "not-delivered" state)

The code-read authoring surfaced these genuine gaps. Each is a row in its section that will FAIL the walk. This is the real delivery state, not the optimistic one.

### #3370 CONTEXTS (CLOSED — but the walk re-opens these)
- **DataInstances panel NOT deleted** from the SME tenant console (`core/console/src/components/DataInstances.svelte` still wired at `AppsPage.svelte:9,443-445`) — DoD §2.8 "deleted in this ticket" fails a strict reading.
- **Reuse of the 3 bootstrap shared-PG instances is blocked in-console** (`endpoint_handler.go:1031-1037`) — the DoD-6 reuse walk must target a console-created instance.
- **shared-pg-c mapping shortfall**: only `sme_*` Contexts declared; `newapi` + `openova-flow` Contexts NOT on shared-pg-c (DoD §5/§8 target unmet).
- **Embedded DBs not eliminated**: `pdns-pg` (no opt-out gate), `openova-flow-pg`, `newapi-pg` remain own-cluster.

### #3373 PLACEMENT (status/uat — 3 hard UI gaps)
- **NO placement selector UI** — `grep` of `core/console/src` + `core/marketplace/src` returns zero; DoD-2 (default-vs-advanced screenshots) is **unsatisfiable today**. (Server-side data exists; only the UI is missing.)
- **NO console display of an instance's placement** (`AppDetail.svelte` has no `status.placement` consumer) — DoD-3 unsatisfiable.
- **§4 table NOT fully executed** — several non-minimum apps still `vcluster: host` (keycloak REVERTED to host after the hw130 outage); `--strict-target` fails by design.
- **OSS vCluster Pro-gating** — route-bearing apps can't fully re-home on OSS (founder decision: license / host-bridge / host-side).

### #3374 SSO (status/uat — operator tier partially live, the rest unbuilt/unwalked)
- **openbao / pdns-admin / guacamole**: source-shipped bare-URL zero-click interceptions exist but are **NOT live-walked**; this session's hw133 walk found **openbao postMessage error** + **pdns-admin /oidc/login 500** still broken.
- **newapi**: no explicit `sovereign-admins`→admin mapping in chart values (only client registration).
- **guacamole**: admin-elevation gap — openid extension only, **no JDBC permission store** (`rolesFromGroup` intent unimplemented, Chart comment line 103-104).
- **hubble**: no admin panel; raw-chart default `auth: none` (security gap if the overlay's `oidc` default is missing).
- **per-Org realm tier: DORMANT/UNBUILT** — `EnsureRealm` creates only a bare realm (no owner-seed, no catalyst-pin IdP, no per-app client). DoD-7 genuinely unbuilt, gated on FUNNEL.
- The **regression probe** component must be located/verified shipped.

### #3375 TOPOLOGY/DR (status/uat — substrate real, region-kill unproven)
- **console `callerTier` not threaded** (`AppDetail.tsx:715-719`) → the live Switchover button shows disabled "Owner tier required".
- **openbao promotion half** (`vault operator raft transition-to-primary`) delegated to bp-continuum — possibly **unwired** (NO MECHANISM gap candidate).
- **switchover authz returns HTTP 200 (body error:403)**, not a real 403 — don't misread as success.
- **ALL cross-region machinery is default-OFF** — a single-region prov renders none of it; the walk needs a genuine 2-region overlay-enabled prov or every region-kill row is vacuously absent (the PR #1599 anti-pattern).

### #3376 FUNNEL (status/in-progress — the back-half is broken)
- **`GET /api/billing/balance` → 502** (this session, hw133): the billing SME service **CrashLoops** because it can't reach NATS — **SME→NATS cross-node datapath times out** (publisher on cp1, NATS on workers; no netpol, healthy NATS). This blocks the voucher + Purchase + tenant provision.
- **The mother→child tofu-archive wire** gates `provisioning/start` (502 until it lands).
- **The whole finish line is unproven** — "purchased app RUNNING in the customer's own org console" has never been walked.

### #3378 ORGANIZATIONS (CLOSED — two honesty flags)
- **Directory badge fidelity (PARTIAL)**: `subOrgRowFromTenant` hardcodes every sub-org to `customer/real/vcluster` (`organizations.api.ts:156-172`); a created `internal` org mis-badges until the tenants feed surfaces spec fields.
- **`/sme/users` + `/sme/roles` not redirected** — org-detail users/roles tabs not built (deferred by design).

### #3379 SOVEREIGNTY (status/in-progress — 3 divergences from the DoD wording)
- The engine runs **11 steps, not the ADR's 8**.
- The "deny-egress NetworkPolicy" is a **CIDR block, not FQDN** (Cilium 1.16 limit); only engages when `enforceCIDRBlock: true` (the overlay sets it; chart default false = denies nothing = invalid proof).
- Cutover has **never fired** (no env handed over) — the whole walk is unexecuted.

### #3380 ROBUSTNESS (status/in-progress — several Build items open)
- **Thin cloud-init NOT done** — imperative cilium/gateway-CRD prelude still in `cloudinit-control-plane.tftpl:687`.
- **bp-vllm not in the proxy-image render matrix** (latent kyverno-deny on scale-up).
- **sovereign-tls dependsOn-narrowing has no drift guard**.
- **powerdns-api-tls diagnosis OPEN**.
- **cilium-envoy xDS detector NOT found in-repo** (the Build item is open).
- **cloud-init kubeconfig-capture API false-negative** — `GET /cloudinit-log` 404s even when the file exists on the PVC (this session: the kubeconfig was on the PVC the whole time; the API lied "not captured").
- **NATS chart has NO NetworkPolicy at all** — the SME→NATS cross-node datapath (the #3376 502 root cause) has no carve-out.

---

## Table of contents
1. [#3370 CONTEXTS](#3370-contexts)
2. [#3373 PLACEMENT](#3373-placement)
3. [#3374 SSO](#3374-sso)
4. [#3375 TOPOLOGY/DR](#3375-topologydr)
5. [#3376 FUNNEL](#3376-funnel)
6. [#3378 ORGANIZATIONS](#3378-organizations)
7. [#3379 SOVEREIGNTY](#3379-sovereignty)
8. [#3380 ROBUSTNESS](#3380-robustness)

> The 8 full per-ticket walk sections follow. Each was authored by a dedicated agent reading that ticket's full DoD + the real chart/UI/controller code. The detailed tables (≈350 click-by-click rows total) are maintained in the per-ticket section files under `docs/ledger/uat-walkthrough/` and summarized here. **Founder: review the gap summary above + the per-section DoD coverage below, then order the walk.**

---

_Per-ticket sections are committed alongside this index. See the gap summary for the binding honest state; the section tables carry the exact click-by-click rows + file:line sources for the walk agents._
