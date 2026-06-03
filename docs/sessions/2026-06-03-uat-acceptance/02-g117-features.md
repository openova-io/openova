# OpenOva UAT Acceptance Document — G117 Features

> **How to read this section.** Each row is one capability the end-user (Org
> member / sovereign-admin) gets from the G117 Application-Lifecycle epic. The
> **How YOU verify it** column gives you runnable steps — a console click-path
> you can walk on `console.hw88.omani.works`, or a `kubectl` / `curl` / `grep`
> you can paste. The **What we tested + evidence** column cites the merged PR,
> the file/line, and the test that already exercises the code path. The
> **Acceptance** column is our honest current state:
>
> - ✅ **code-verified** — landed on `main`, unit/chart/CI tests green, no live
>   walk needed to trust the code path.
> - 🔄 **needs hw88 walk** — code is merged + tested, but the operator-visible
>   silent-login / UI behavior should be re-walked on the live `hw88` Sovereign
>   before final sign-off.
> - ⚠️ **partial** — landed but with a known caveat (noted inline).
> - ⛔ **open** — not shipped.

---

## 2. G117 Application-Lifecycle Features

| # | Capability (what the end-user gets) | How YOU verify it (walk-through steps) | What we tested + evidence | Acceptance |
|---|---|---|---|---|
| 2.1 | **Silent-SSO Launch button.** On an installed app's detail page, a "Launch" button opens the app **already authenticated** — no Keycloak login form, you land inside Grafana / Gitea / Harbor logged in as yourself. | **Console walk:** 1. Sign in to `https://console.hw88.omani.works`. 2. Open an Org → Environment → an installed app (e.g. `grafana`). 3. On the **AppDetail** page click the **Launch** button (`data-testid="btn-launch-app"`). 4. Confirm a new tab opens straight into the app UI with **no** Keycloak login screen (silent-SSO budget <500ms). **Code check:** `grep -n "launch-url\|kc_idp_hint\|prompt=none" products/catalyst/bootstrap/ui/src/pages/sovereign/AppDetail.tsx` — the `LaunchButton` calls `GET /catalyst/v1/apps/{uid}/launch-url`; backend appends `prompt=none&kc_idp_hint=catalyst-pin`. | `products/catalyst/bootstrap/ui/src/pages/sovereign/AppDetail.tsx:972-978,1721-1734` (`LaunchButton`, uid threaded, fallback to direct URL on uid-missing/404/409/503). Backend: `products/catalyst/bootstrap/api/cmd/api/main.go:1410` route → `endpoint_handler.go` `HandleGetLaunchURL`. Tests: `endpoint_handler_test.go:465,488`, `AppDetail.test.tsx`. PRs #2743 / #2744. | 🔄 needs hw88 walk |
| 2.2 | **3-tier SSO fan-out.** Silent-SSO works not just for the marquee apps but across all three tiers: **Tier-1** grafana/gitea/harbor/openbao, **Tier-2** guacamole/powerdns-admin/hubble-ui, **Tier-3** matrix/librechat/etc. — every catalog app you launch lands authenticated. | **Console walk:** repeat the 2.1 Launch walk against one app per tier on `console.hw88.omani.works` (e.g. `harbor` Tier-1, `guacamole` Tier-2). **Hubble-UI note:** hubble-ui had no native OIDC — it now sits behind an **oauth2-proxy** that enforces the OIDC gate (#2967); launching it should redirect through Keycloak silently then render the Hubble map. **Code check:** `grep -n '"clientId"' platform/keycloak/chart/templates/configmap-sovereign-realm.yaml` — confirm grafana/gitea/harbor/openbao (Tier-1), guacamole/powerdns-admin/hubble-ui (Tier-2). | `platform/keycloak/chart/templates/configmap-sovereign-realm.yaml` — Tier-1 clients at lines 420/457/494/531, Tier-2 at 589/626/664; `catalyst-pin` IDP-hint client at 823. Tier-3 AppRegistration reconciler noted at line ~900. Hubble-UI oauth2-proxy enforcement: **PR #2967** (`feat(bp-cilium): Hubble UI oauth2-proxy OIDC enforcement`). PR #2744. | 🔄 needs hw88 walk |
| 2.3 | **Per-Org Keycloak realm + 2-hop SSO.** Each Organization gets its **own** Keycloak realm that federates up to the shared sovereign realm — so Org members authenticate against their Org's realm and SSO still flows through to apps (2-hop broker chain). | **Console walk:** create/open two Orgs on `console.hw88.omani.works`, sign in as a member of Org A, Launch an app — confirm you authenticate via Org A's realm (not the bare sovereign realm) and still land in the app. **Code check:** `grep -rn "dual-token\|provision_org_realm\|tenantRealms" platform/keycloak platform/sso-bridge` — per-Org realm fan-out iterates `tenantRealms[]`; the bridge mints a **dual token** (sovereign + per-Org) to provision the child realm. | Dual-token KC mint: **PR #2918** (`fix(sso-bridge,keycloak): #2914 — dual-token KC mint for per-Org realm provisioning`) — `bp-keycloak 1.4.18` + `bp-sso-bridge 0.2.8`. Chart-tests `g117-fu-2914` + `g117-w2c4` pass. PR #2744. | 🔄 needs hw88 walk |
| 2.4 | **Catalog drill-down + multi-instance.** The Blueprint catalog separates **class** (the Blueprint) from **instance** (an installed app); you can install **multiple instances** of the same Blueprint in one Environment (e.g. two `postgres` apps with different names). | **Console walk:** on `console.hw88.omani.works` open the **Catalog**, click a Blueprint card to drill into the class page, install one instance, then install a **second** instance of the same Blueprint with a different name — confirm both appear as distinct apps. **Code check:** `bash scripts/check-catalog-seed-lockstep.sh` (asserts the 14-row catalog seed stays in lockstep, incl. `silentLogin` + `launchDefault`); the Application CRD carries the multi-instance `instanceId` field. | Catalog-seed lockstep CI guard: **PR #2926** (`#2744 — catalog-seed lockstep CI guard + 6 caught drifts`) + **PR #2927** (`tighten … with silentLogin + launchDefault assertions`). Guard: `.github/workflows/check-catalog-seed-lockstep.yaml` + `scripts/check-catalog-seed-lockstep.sh`. Application CRD multi-instance fields. PRs #2740 / #2745. | ✅ code-verified |
| 2.5 | **Endpoints / Ingress tab + Git-IaC PR pipeline.** Editing an App's endpoints/ingress in the console doesn't mutate the cluster directly — it opens a **pull request** against the per-Org Gitea IaC repo, gated by 3 required status checks, so every change is reviewable + auditable (GitOps). | **Console walk:** on `console.hw88.omani.works` open an App → **Endpoints/Ingress** tab → add/edit an endpoint → save → confirm a PR is opened in that Org's Gitea IaC repo. **Code check:** `grep -n "kyverno-admission\|cert-manager-precheck\|dns-conflict-precheck\|iac-prechecks" core/controllers/organization/internal/iacbootstrap/bootstrap.go` — org-controller's `iacbootstrap` seeds `.gitea/workflows/iac-prechecks.yml` and locks branch-protection on the 3 named checks. | org-controller iac-bootstrap: `core/controllers/organization/internal/iacbootstrap/bootstrap.go:86-88` (locked checks `kyverno-admission` / `cert-manager-precheck` / `dns-conflict-precheck`) + `:362`. Missing pre-check workflow seeded by **PR #2965** (`#2742 — seed Gitea Actions pre-check workflow into per-Org IaC repo`). Controllers auto-roll on runtime-config change: **PR #2928** (`#2742`). PR #2742. | 🔄 needs hw88 walk |
| 2.6 | **Blueprint topology admission.** A malformed Blueprint (e.g. a `defaults.multi-region` value that isn't listed in `supported[]`) is **rejected at the apiserver** with a clear message — bad topology declarations can't get into the cluster. | **kubectl check:** apply a Blueprint whose `spec.topology.defaults.multi-region` is **not** a member of `spec.topology.supported[]`: `kubectl apply -f bad-blueprint.yaml` → expect **HTTP 422 / admission denied** with message `topology.defaults.multi-region must be a member of topology.supported[]`. A valid Blueprint applies cleanly. **Code check:** `grep -n "x-kubernetes-validations" -A2 products/catalyst/chart/crds/blueprint.yaml`. | CEL cross-field rules in CRD: `products/catalyst/chart/crds/blueprint.yaml:353-359` — 3 rules (defaults.multi-region ∈ supported, defaults.single-region ∈ supported, every `perTopology` key ∈ supported). Shipped by **PR #2958** (`#2936 — Blueprint topology cross-field CEL validation`). PR #2936. | ✅ code-verified |

---

### Acceptance summary (for master assembly)

| # | Capability | Acceptance |
|---|---|---|
| 2.1 | Silent-SSO Launch button | 🔄 needs hw88 walk |
| 2.2 | 3-tier SSO fan-out | 🔄 needs hw88 walk |
| 2.3 | Per-Org Keycloak realm + 2-hop SSO | 🔄 needs hw88 walk |
| 2.4 | Catalog drill-down + multi-instance | ✅ code-verified |
| 2.5 | Endpoints/Ingress tab + Git-IaC PR pipeline | 🔄 needs hw88 walk |
| 2.6 | Blueprint topology admission | ✅ code-verified |

**Net:** 2 rows ✅ code-verified (the CRD-admission and catalog-seed-lockstep
paths, which are fully exercised by CI/unit tests), 4 rows 🔄 needs hw88 walk
(the SSO/UI behaviors — code merged + tested, live silent-login walk on the
`hw88` Sovereign owed before final sign-off). 0 ⚠️ partial, 0 ⛔ open.
