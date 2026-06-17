# PATH TO 100% — hw159 UAT remediation plan

> **Source of truth:** [`UAT.md`](UAT.md) — hw159 (2026-06-18), **119 ✅ / 61 ❌ / 56 GAP / 5 ☐ = 241 rows.**
> This file maps every non-✅ row to the exact fix (issue + code path + owner) and a priority order.
> "100%" here = **every browser-decidable row ✅** (the 180 PASS+FAIL → all PASS) + a triage decision
> on each of the 56 GAP. A literal 241/241 also requires building UI for, or descoping, the GAP rows.

---

## Bucket A — the 61 ❌ FAILs → 5 root-cause fixes

Most of the 61 fails collapse into 5 roots. Fix the root, the cluster of rows flips.

### A1 · 🛑 vCluster app-placement — `#3642` (flips ~13 ❌, rows `3642-*`)
- **Symptom (walked):** the 7 platform apps (grafana/harbor/keycloak/gitea/openbao/newapi/guacamole) render under the **`host`** block in the Dashboard vCluster-layer treemap; **none under `mgmt`**. North Star #1 ("every app in a vCluster") unmet. `keycloak` app card reads `Namespace: flux-system`, `Placement: singleton`.
- **Root cause:** the host-bridge generator (built in #870) emits the apps but they are NOT relocated into the `mgmt` vCluster — they stay as host-cluster HelmReleases in `flux-system`.
- **Fix — code paths:**
  - `products/catalyst/bootstrap/` host-bridge generator (the #3642/#870 deliverable) — the 7 slots must render INTO the mgmt vCluster namespace, not the host.
  - `core/controllers/application` (application-controller) placement resolution — must honour the vCluster target, not default to host `flux-system`.
  - `clusters/_template/bootstrap-kit/` slot files for the 7 apps — confirm they target the vCluster syncer, not the host.
- **Owner:** platform / bootstrap-kit + application-controller. **Priority: P0** (it's North Star #1).
- **Acceptance:** Dashboard treemap Layer1=vCluster shows the 7 apps under `mgmt`, each app card `Placement` ≠ host `flux-system`.

### A2 · 🛑 Per-Org external serving — `#3376` (flips ~18 ❌, rows `3376-*`)
- **Symptom (walked):** `Acme Corp` Org reaches **ACTIVE** in the directory, but `https://console.acme.omani.homes` AND `https://wordpress.acme.omani.homes` both return **ERR_CONNECTION_REFUSED** — the customer's own console + purchased app do not serve externally. The funnel's terminal "app running in the customer's own Org" is never reached.
- **Root cause (to confirm):** the tenant vCluster's gateway/HTTPRoute + the `console.<slug>.<parent>` DNS record + wildcard cert for the Org subdomain are not provisioned (or not reconciled) when the Org goes ACTIVE; the purchased WordPress app's HelmRelease is not deployed into the tenant vCluster.
- **Fix — code paths:**
  - `core/controllers/organization` (org-controller) — on Org→ACTIVE, ensure tenant gateway + HTTPRoute for `console.<slug>.<parent>` + the app ingress.
  - `core/pool-domain-manager` / external-dns — the `<slug>.<parent>` DNS record must be created + resolve.
  - cert-manager wildcard for the tenant parent domain (`omani.homes`).
  - the tenant gitops repo (`<slug>/catalyst-tenant`) → host Flux Kustomization (the #3687/#3700 per-Org loop) must actually APPLY the purchased-app HR.
- **Owner:** platform / org-controller + pool-domain-manager. **Priority: P0** (Pillar-1 funnel terminal).
- **Acceptance:** `console.acme.omani.homes` lands signed-in; `wordpress.acme.omani.homes` serves the WordPress site.

### A3 · 🛑 Bootstrap-HR runtime topology — `#3375` (flips ~16 ❌, rows `3375-*`)
- **Symptom (walked):** the `shared-pg` (and other bootstrap) **Topology tab is dead at runtime** — declared `singleton` mismatches the Overview tab's `active-hotstandby`, Live status reads `n/a`, replication-lag `n/a (mode)`, no Switchover. Because it's a bootstrap HelmRelease **with no Application CR**, the per-region controller has nothing to project.
- **Root cause:** the 8 `application-cr.yaml` templates added in #3786 hardcode `placement: single-region` (a banned dialect) and/or are not wired so the Topology tab reads a live Application CR. Without a real Application CR carrying the placement, the topology UI shows stale/n-a.
- **Fix — code paths:**
  - `platform/<app>/chart/templates/application-cr.yaml` (the 8 added #3786) — replace hardcoded `placement: single-region` with the real declared mode (`singleton`/`active-hot-standby`), gated correctly; lockstep with `platform/postgres/blueprint.yaml` placementSchema (#3784).
  - `core/controllers/application` — the Topology tab's Live-status must read the Application CR's per-region HR readback (#3687 "status from HR readback").
- **Owner:** platform / chart templates + application-controller. **Priority: P1.**
- **Acceptance:** shared-pg Topology tab shows declared == effective, Live status real (not n/a), Switchover offered where applicable.

### A4 · SSO per-app — `#3374` (flips ~3 ❌, rows `3374-*`)
- **Symptom (walked):** `newapi.hw159` bare URL → `/setup` first-run wizard (+ 1st-hit `upstream-connect-error 111`); `pdns-admin.hw159` → `/login` form with a manual "Sign in using OpenID Connect" button (no silent SSO). **guacamole is now ✅ (jti fixed).**
- **Fix — code paths:**
  - newapi: `platform/newapi/` chart — the first-run `/setup` must be pre-seeded/bypassed so the OIDC chain fires on 1st hit; investigate the upstream-111 (pod not ready / proxy).
  - pdns-admin: `platform/powerdns/` (pdns-admin) OIDC config — enable auto-redirect to the IdP (no manual button), `OIDC_AUTO_LOGIN`-equivalent.
- **Owner:** platform / newapi + powerdns charts. **Priority: P2.**
- **Acceptance:** both bare URLs land signed-in, no setup wizard, no login form.

### A5 · Stale-pin fails — `#3383` + misc (flips ~5–10, free)
- **Symptom (walked):** create-org form still says "SME tenant slug"/"Onboard tenant"; jobs `/jobs` ingests only `lifecycle` Kind (multi-kind GAPs); possible newapi behaviour — all because **hw159 runs the last-PUBLISHED chart `1.4.674`**, while the fixes shipped in **`1.4.677`** (published this session after the publish-gate fix).
- **Fix:** **re-prov on `1.4.677`** — no code change. The slot pin `clusters/_template/bootstrap-kit/13-bp-catalyst-platform.yaml` must be bumped 1.4.674 → 1.4.677 first (it was held at 1.4.674 to unblock hw159).
- **Owner:** orchestrator (me). **Priority: P0 — cheapest, do FIRST.**
- **Acceptance:** fresh prov shows "Organization" wording + multi-kind jobs; re-walk #3383 + #3646 GAP/FAIL rows flip.

---

## Bucket B — the 5 ☐ not-reached → walk them

Rows `3379-*` (cutover view-only). These need the **destructive cutover RUN** + **region-kill switchover** actually triggered (held back on the healthy env).
- **Fix:** on a dedicated prov, fire `bp-self-sovereign-cutover` (8-step + 10-min deny-egress hold) and the region-kill switchover; capture the per-step Jobs tree + `cutoverComplete=true`.
- **Owner:** orchestrator + cutover engine. **Priority: P1** (do on the 1.4.677 re-prov, after walking the non-destructive rows).
- **Acceptance:** the 5 ☐ rows become ✅/❌ with the cutover Jobs tree screenshot.

---

## Bucket C — the 56 GAP → triage (build UI or descope)

Each GAP = "no browser surface for this assertion." Three sub-categories:

| GAP sub-type | count (approx) | Decision |
|---|---|---|
| **Backend/code invariant** (e.g. `store.Tenant` UID owner-ref, shared payload struct, `-race` fixes) | ~30 | **Descope from the *browser* UAT** — these are unit/integration-test rows, not click-tests. Move them to a code-test ledger; do NOT count against browser 100%. (Honest reclassification, not fabrication.) |
| **Feature with no UI yet** (e.g. cron-snapshot row, per-Org showback when Org has 0 apps, treemap Organization layer) | ~18 | **Build the UI surface** — each becomes a small console FE ticket. These legitimately convert GAP → ✅ once built. |
| **Gated on another prov/ticket** (tenant-tier SSO, generality throwaway-app, cutover integrity proofs) | ~8 | **Unblock via the dependency** (the funnel-serving fix A2 unblocks tenant-tier; the cutover run B unblocks integrity proofs). |
- **Owner:** product call on descope vs build. **Priority: P3** (after the FAILs — GAPs don't regress the app, they're coverage gaps).

---

## Priority-ordered execution

| # | Action | Bucket | Flips | Cost |
|---|---|---|---|---|
| 1 | **Bump slot 1.4.674→1.4.677 + fresh prov + re-walk** | A5 | ~10 + clean baseline | low (no code) |
| 2 | **Fix vCluster app-placement** (#3642) | A1 | ~13 | med |
| 3 | **Fix per-Org external serving** (#3376) | A2 | ~18 | high |
| 4 | **Fix bootstrap-HR Application CR placement** (#3375/#3786) | A3 | ~16 | med |
| 5 | **Fire cutover-run + region-kill walk** | B | 5 | low (on the new prov) |
| 6 | **newapi/pdns SSO** (#3374) | A4 | ~3 | low |
| 7 | **GAP triage** — descope backend-invariants, build the ~18 missing UIs | C | ~26 | product call |

**After #1–#6: browser-decidable rows → ~100% PASS** (the meaningful 100%). **#7 closes the literal 241/241** by deciding each GAP.

**Audit caveat (do before trusting the split):** re-check the 56 GAP — any that are "UI is broken so I called it GAP" must convert to ❌. That audit is a precondition for this plan's row counts.

---

_Generated 2026-06-18 from the hw159 walk. Update the flip-counts as each fix lands + is re-walked._
