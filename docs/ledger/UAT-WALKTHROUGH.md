# UAT WALK-THROUGH — the agreement document (2026-06-14)

**Purpose.** This is the click-by-click verification protocol for every founder-approved ticket. It is authored from the **real merged code** (every row cites `file:line`), NOT from claims. Once the founder **agrees on this document**, the walk-execution agents run each section on the live env and flip each `☐` to ✅ (witnessed) or ❌ (failed) with evidence. **No row may be marked done without the cited observation.**

**How to read a row.** `| # | Go to (URL/route) | Do (exact click/type) | Expect (exact screen/text/state) | Source (file:line) | ☐ |`
- **Source** cites the real code that makes the row true.
- **`NO UI FOUND — gap`** = the DoD criterion has no operator-visible surface in the code today; it is a genuine gap, walked at the kubectl/Git layer and flagged for the founder.
- **`AUTOMATED, NOT ACCEPTANCE`** = a CI/unit gate, demoted per the founder's rule ("this is only a unit test!!!"). Acceptance is the founder/agent walking the clickable + kubectl steps.
- A **302-to-realm is never a pass** for SSO — only a rendered, logged-in screen counts.

**Acceptance bar.** Every walk runs on a **fresh, zero-touch, converged** Sovereign (no hand-patching). Evidence (screenshot + wire-capture + kubectl) lands under `docs/sessions/<date>/evidence/` and is linked from `docs/ledger/UAT.md`. PR-merge ≠ done; only a witnessed walk is done.

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
