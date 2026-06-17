# UAT Walkthrough — ORGANIZATIONS: eradicate the `sme`/`tenant`-named subsystem into the canonical Organization machinery (ZERO behavior change) + CI naming guard

## Status — last validated: hw158 (2026-06-17, RE-VALIDATED on recovered env) — ❌ FAILED (rename NOT landed; BEFORE-baseline intact)

- **Verdict: ❌ FAILED on hw158** (re-confirmed live 2026-06-17 on the recovered env). The `sme`→`org-services` / `tenant`→`Organization` mechanical rename + CI naming-guard is **NOT landed** and is **not present in the repo** on this branch (`docs/uat-ground-reality`). Every "after" row (the renamed namespace, route, secret, template dir, Go identifiers, deprecation alias, CI guard) is absent; the env still runs the full `sme`-named mesh exactly as the Section-0 BEFORE baseline describes. No rename PR has merged — every ❌ re-checked live this walk remains ❌, every ✅ remains an unchanged BEFORE-baseline survivor.
- **Tally: 7 ✅ / 30 ❌ / 5 N/A** (42 rows) — UNCHANGED from the prior walk (the rename never rolled). The 7 ✅ are all **Section-0 BEFORE-baseline rows + the one legitimate `Tier: "sme"` data-value survivor (2.3) + the PR #3390 BSS/Organizations alias survivors (8.1, 8.2)** — i.e. the OLD state is fully intact, which is exactly what a not-yet-rolled rename looks like. None of them prove the rename.
- **Maps to:** no direct [`../UAT.md`](../UAT.md) feature row (this is a mechanical-rename/naming-guard ticket, orthogonal to the 14 feature rows).
- **Evidence (live on hw158, this re-validation walk 2026-06-17):**
  - ns `sme` **Active 5h11m** (aged up from the prior walk's 3h37m — same env, longer uptime) with the full **12-deploy** mesh (`admin auth billing catalog console domain ferretdb gateway marketplace notification provisioning tenant`, all `1/1`); **NO `org-services` ns** (`Error from server (NotFound): namespaces "org-services" not found`).
  - `sme-secrets   Opaque   11   5h11m` lives under its persona name; **NO `org-services-secrets`** (`secrets "org-services-secrets" not found`).
  - catalyst-api env still `CATALYST_SME_JWT_SECRET → secretKeyRef key: JWT_SECRET, name: sme-secrets, optional: true`.
  - `POST /api/v1/organizations` = **HTTP 404**; `GET /api/v1/sme/tenants` = **HTTP 401** (`{"error":"unauthenticated"}`, `server: envoy`) with headers `content-type / vary / date / content-length / x-envoy-upstream-service-time` and **NO `Deprecation`/`Sunset`/`Warning` header**.
  - chart dir is still `products/catalyst/chart/templates/sme-services/` (**29 files**); `org-services/` absent (`helm template … templates/org-services/org-namespace.yaml` → `could not find template …`); `sme-namespace.yaml` renders `kind: Namespace / name: sme`.
  - Go: `HandleCreateSMETenant` etc. = **28 hits** (was 27 — count crept up with test/code additions, still no Org rename); `SMETenantProvision*` store types = **82 hits** (was 76); the new `HandleCreateOrganization*` / `OrganizationProvisionStore*` identifiers = **0 hits**. Route `rg.Post("/api/v1/sme/tenants", h.HandleCreateSMETenant)` still registered (main.go:**1670**, line shifted from the prior 1652).
  - CI guard absent: `scripts/check-no-persona-machinery.sh` and `.github/workflows/naming-guard.yaml` both `No such file or directory`.
  - Legit survivor present: `TenantKindSME TenantKind = "sme"` (tenant_registry.go:45) — the allowed data value.
  - §4 grep sweep: `git grep -rwl "sme"` = **233 files** (was 222); `… "tenant"` = **362 files** (was 346) — the full live machinery, still not collapsed to the legitimate residual.
- **What's needed:** author + roll the rename chart (git-mv `sme-services/`→`org-services/`, rename the secret + repoint the catalyst-api env, rename the Go handler/store identifiers + add the `/api/v1/organizations` canonical route with a one-release deprecated `/api/v1/sme/tenants` alias, add the CI naming guard), then re-walk Sections 1–8 on a converged env (capture the BEFORE baseline first per Section 0).
- **Env caveat (not a rename defect) — UPDATED this walk:** the prior walk's `bp-keycloak` chart-pull wedge is **RESOLVED** — `bp-keycloak` is now `True / Helm upgrade succeeded for release keycloak/keycloak.v2 with chart bp-keycloak@1.4.30`. The current non-Ready HRs are `bp-catalyst-platform` (`Unknown / Running 'upgrade' action with timeout of 30m0s` — in-flight) cascading `dependency … is not ready` to `bp-continuum` + `bp-sandbox`, plus a `walk-stranger-co/vcluster` install failure (chart `vcluster@0.33.3`) and a few un-reconciled `bp-*` (cluster-autoscaler-hcloud / hcloud-ccm / velero). **None of these are caused by this rename** (no rename HR was ever rolled). Section 7's "clean roll" rows stay **N/A** because the rename was never rolled — they cannot be evaluated for this ticket on a pre-rename env.
- **Index:** [`README.md`](README.md). (Note: #3383 is not currently in the open `status/uat` GitHub list; this runbook is retained for when the rename is scheduled.)

> **Ticket:** #3383 · **Slug:** `organizations-eradicate-sme-tenant-naming`
> **Bar:** #3370-grade. Every row is ONE operator action: **Go to `<URL>` | do `<click/type/run>` | see `<screen/output>` | ☐**.
> **Env:** the CURRENT converged Sovereign (current = `console.hw158.omani.works`; substitute the live env FQDN when walking).
> **Boundary:** this walk proves the **pure mechanical/identifier rename + naming guard with ZERO behavior change**. It does **NOT** prove the behavioral model-unification onto one Organization CR — that is #3687, walked separately. Every "after" row here must be **byte-for-byte equivalent behavior** to the "before" row; the ONLY observable diffs are the *names* (namespace, route path, secret, template dir, Go identifiers).
>
> **How to read the equivalence rows:** each PASS requires the SAME downstream artifact the old name produced (same vCluster, same Gitea org, same route, same realm, same billing token) — only the identifier changed. A functional drift (different status, missing artifact, broken billing) is a **FAIL**, never a "minor diff".

---

## Section 0 — Pre-flight: capture the BEFORE baseline (run BEFORE the rename rolls)

These rows snapshot the current `sme`-named reality so the AFTER walk can prove equivalence. On hw158 the rename has NOT rolled, so these BEFORE rows describe the *current* state — they PASS as a baseline, and they are simultaneously the proof the rename is absent.

| # | Go to / Run | Do | Result | ☐ |
|---|---|---|---|---|
| 0.1 | terminal | `kubectl --kubeconfig /tmp/hw158-kc.yaml get ns sme` → `sme   Active   5h11m`. (And `get ns org-services` → `Error from server (NotFound): namespaces "org-services" not found`.) | ✅ — the persona-named ns is live; the target ns does not exist yet | ✅ |
| 0.2 | terminal | `kubectl … get deploy -n sme` → `admin auth billing catalog console domain ferretdb gateway marketplace notification provisioning tenant` all `1/1`. The whole Org-provisioning mesh lives in ns `sme` | ✅ — full 12-deploy mesh Running in ns `sme` | ✅ |
| 0.3 | terminal | `kubectl … get secret -n sme sme-secrets` → `sme-secrets   Opaque   11   5h11m` | ✅ — JWT bridge secret exists under its persona name | ✅ |
| 0.4 | terminal | `kubectl … -n sme get deploy provisioning -o jsonpath='{…image}'` → `ghcr.io/openova-io/openova/services-provisioning:87e1e28`. AFTER must be byte-identical | ✅ — image digest recorded (`services-provisioning:87e1e28`) | ✅ |
| 0.5 | `https://console.hw158.omani.works/organizations` | `curl -s -o /dev/null -w '%{http_code}' …/organizations` → `200`. (Curl-only walk; UI directory rows not captured — see #3687 / the UI rows in `organizations-*` runbooks.) Recorded as the AFTER comparison anchor. | ✅ (page serves 200 — baseline anchor; row content not browser-walked) | ✅ |
| 0.6 | terminal | `curl -si …/api/v1/sme/tenants` → `HTTP/1.1 401 Unauthorized` · `{"error":"unauthenticated"}`. The OLD path answers (401 without token, as expected) | ✅ — old path live (401 unauth, body recorded) | ✅ |
| 0.7 | repo | `git grep -rwl "sme" core/ products/ \| wc -l` → **233**; `… "tenant" … \| wc -l` → **362**. (BEFORE counts recorded; the residual set AFTER must collapse to only Tier enum + docs + one-release aliases.) | ✅ — BEFORE sweep counts recorded (233 `sme`, 362 `tenant`) | ✅ |

---

## Section 1 — DoD-1: The §4 rename map is fully executed in the chart & code (file existence + render)

| # | Go to / Run | Do | Result | ☐ |
|---|---|---|---|---|
| 1.1 | repo | `ls products/catalyst/chart/templates/org-services/` → `No such file or directory`. The old `sme-services/` still EXISTS (29 files incl. `sme-namespace.yaml`, `sme-secrets.yaml`, `tenant.yaml`, …) | ❌ — `org-services/` absent; `sme-services/` intact | ❌ |
| 1.2 | repo | `ls org-services/ \| grep -E 'org-namespace\|org-services-secrets'` → dir absent, so no match. The renamed files do not exist | ❌ — no `org-namespace.yaml` / `org-services-secrets.yaml` | ❌ |
| 1.3 | repo | `ls …/handler/organization_provisioning.go …/handler/organization_keycloak.go` → both `No such file or directory`. `…/handler/sme_tenant.go` + `…/handler/sme_tenant_keycloak.go` still EXIST | ❌ — Org handler files absent; `sme_tenant*.go` present | ❌ |
| 1.4 | repo | `ls …/store/organization_provisioning.go` → `No such file or directory`. `…/store/sme_tenant.go` still EXISTS | ❌ — Org store file absent; `store/sme_tenant.go` present | ❌ |
| 1.5 | terminal | `helm template products/catalyst/chart --show-only templates/org-services/org-namespace.yaml` → `Error: could not find template templates/org-services/org-namespace.yaml in chart`. (For comparison, `--show-only templates/sme-services/sme-namespace.yaml` renders `name: sme`.) | ❌ — the renamed Namespace template does not render; `sme` ns still rendered | ❌ |
| 1.6 | terminal | the Org mesh still renders into ns `sme` (1.5 shows `name: sme`), so a bare `namespace: sme` IS present for the mesh — the opposite of the expected empty result | ❌ — Org mesh still renders bare `sme` namespace | ❌ |
| 1.7 | repo | `grep -rwn "HandleCreateSMETenant\|HandleListSMETenants\|HandleDeleteSMETenant\|HandleReconcileSMETenant" …/api/` → **28 hits** (not empty). The new `HandleCreate/List/Delete/ReconcileOrganization` identifiers → **0 hits** | ❌ — SME handler identifiers present (28), Org identifiers absent (0) | ❌ |
| 1.8 | repo | `grep -rwn "SMETenantProvisionRecord\|SMETenantProvisionStore\|NewSMETenantProvisionStore" …/api/` → **82 hits** (not empty). The new `Organization*` store types → **0 hits** | ❌ — SME store types present (82), Org store types absent (0) | ❌ |

---

## Section 2 — DoD-2: The §4 grep gate — residual `sme`/`tenant` is ONLY legitimate

| # | Go to / Run | Do | Result | ☐ |
|---|---|---|---|---|
| 2.1 | repo | `git grep -rwl "sme" core/ products/` → **233 files**. These are NOT yet reduced to only the Tier enum + docs + compat aliases — they are the full live machinery (handlers, stores, chart templates dir, routes). The gate would FAIL today | ❌ — 233 `sme` file-hits = full machinery, not the legitimate residual set | ❌ |
| 2.2 | repo | `git grep -rwl "tenant" core/ products/` → **362 files**. Includes NEW-ish Catalyst machinery (`sme_tenant.go`, `tenant.yaml` deploy, `tenant_registry.go`, `/api/v1/sme/tenants` route) — not just vendored/upstream realm strings | ❌ — 362 `tenant` file-hits include live Catalyst machinery | ❌ |
| 2.3 | repo | `grep -rwn "TenantKindSME\|Tier" …/store/` → `tenant_registry.go:45: TenantKindSME TenantKind = "sme"` plus `sme_tenant.go:183: Tier string` and test refs. The legitimate `Tier: "sme"` data value EXISTS and is the one allowed `sme` token in machinery | ✅ — the legitimate `Tier:"sme"` data value survives (allowed) | ✅ |

---

## Section 3 — DoD-3: Equivalence walk PRE/POST — org creation produces the IDENTICAL artifacts under the new name

> The behavioral heart. Cannot be walked: the NEW route does not exist on hw158.

| # | Go to / Run | Do | Result | ☐ |
|---|---|---|---|---|
| 3.1 | `https://console.hw158.omani.works/organizations/new` | the canonical `POST /api/v1/organizations` returns `HTTP 404` on hw158; only `POST /api/v1/sme/tenants` is registered (main.go:1670). The create form cannot post to a non-existent canonical route | ❌ — `POST /api/v1/organizations` = 404; canonical route not registered | ❌ |
| 3.2 | DevTools Network | no create-response to inspect — the canonical endpoint is absent (3.1) | ❌ — no canonical endpoint to return a body | ❌ |
| 3.3 | terminal | not run — depends on a successful canonical create (3.1 failed). The slug-based `vc-<slug>` ns equivalence cannot be proven for the new path | N/A — blocked by 3.1 (no canonical create path) | N/A |
| 3.4 | gitea | not run — blocked by 3.1 | N/A — blocked by 3.1 | N/A |
| 3.5 | terminal | not run — blocked by 3.1 | N/A — blocked by 3.1 | N/A |
| 3.6 | side-by-side | no AFTER artifacts to compare against the Section-0 baseline (3.3–3.5 blocked) | N/A — blocked by 3.1 | N/A |

---

## Section 4 — DoD-4: The renamed secret carries the billing flow with ZERO interruption (wire capture across the roll)

| # | Go to / Run | Do | Result | ☐ |
|---|---|---|---|---|
| 4.1 | terminal | `kubectl … get secret -n org-services org-services-secrets` → `Error from server (NotFound): namespaces "org-services" not found`. The renamed secret + its ns do not exist | ❌ — `org-services-secrets` / `org-services` ns absent | ❌ |
| 4.2 | terminal | `kubectl … -n catalyst-system get deploy catalyst-api -o yaml \| grep -A2 JWT_SECRET` → `CATALYST_SME_JWT_SECRET` · `secretKeyRef: key: JWT_SECRET, name: sme-secrets, optional: true`. The env still points at the OLD secret name, NOT `org-services-secrets/JWT_SECRET` | ❌ — catalyst-api still reads `sme-secrets` (not repointed) | ❌ |
| 4.3 | `https://console.hw158.omani.works/organizations/billing/vouchers` | not walked via the renamed secret — the renamed secret does not exist (4.1) and the env's billing path still rides `sme-secrets`. The "renamed secret carries billing" claim is unprovable on a pre-rename env | N/A — renamed secret absent; nothing to prove the renamed-secret flow | N/A |
| 4.4 | terminal | the deploy is `billing` in ns `sme`, not `org-services` — `kubectl … -n org-services logs deploy/billing` cannot run (ns absent). No renamed-secret roll occurred | ❌ — no `org-services/billing` deploy; renamed-secret roll never happened | ❌ |

---

## Section 5 — DoD-5: The old API path still answers with a deprecation header (one-release alias proof)

| # | Go to / Run | Do | Result | ☐ |
|---|---|---|---|---|
| 5.1 | terminal | `curl -si …/api/v1/sme/tenants` → `HTTP/1.1 401 Unauthorized`, headers: `content-type`, `vary`, `date`, `content-length`, `x-envoy-upstream-service-time`, `server: envoy`. **NO `Deprecation` / `Sunset` / `Warning: 299 … deprecated` header.** The path answers but is NOT marked as a one-release alias (it is the *primary* route, not a deprecated alias) | ❌ — old path live but NO deprecation header (it is the primary, not an alias) | ❌ |
| 5.2 | terminal | not run as a separate marketplace alias — the canonical `/api/organizations` (5.1 sibling) does not exist, so there is no "new canonical" for `/api/tenant/orgs` to deprecate toward. The alias-with-header pattern is not implemented | ❌ — no canonical `/api/organizations` to alias toward; deprecation pattern absent | ❌ |
| 5.3 | repo | `grep -rn "Deprecation\|removal\|alias" …/cmd/api/main.go` → only unrelated aliases (continuum singular path, kubectl-style session/scale/exec aliases, billing purchase alias). **No `sme/tenants`→`organizations` deprecation alias and no removal-checklist row** for this rename | ❌ — no rename deprecation alias / removal checklist in main.go | ❌ |

---

## Section 6 — DoD-6: The CI naming guard is LIVE and proven in BOTH directions

> Mirrors the existing `scripts/check-no-banned-words.sh` + `.github/workflows/banned-words-validate.yaml` mechanism.

| # | Go to / Run | Do | Result | ☐ |
|---|---|---|---|---|
| 6.1 | repo | `ls scripts/check-no-persona-machinery.sh` → `No such file or directory`; `ls .github/workflows/naming-guard.yaml` → `No such file or directory`. Neither the guard script nor its workflow exists | ❌ — guard script + workflow both absent | ❌ |
| 6.2 | repo | cannot run the guard against a NEW `sme/widgets` route — the guard binary does not exist (6.1). The "fails on new machinery name" direction is unprovable | ❌ — guard absent; cannot prove the fail direction | ❌ |
| 6.3 | repo | cannot run the guard against a legitimate `Tier: "sme"` value — guard absent (6.1). The "exempts legitimate values" direction is unprovable | ❌ — guard absent; cannot prove the pass direction | ❌ |
| 6.4 | GitHub | no `naming-guard` workflow to surface a check on a PR (6.1) — the gate does not exist | ❌ — no `naming-guard` PR check exists | ❌ |

---

## Section 7 — DoD-7: The live env survives the roll — old ns drained, no orphans

| # | Go to / Run | Do | Result | ☐ |
|---|---|---|---|---|
| 7.1 | terminal | `kubectl … get pods -n org-services` → `No resources found in org-services namespace.` (ns itself NotFound). The mesh is NOT in the new ns | ❌ — no mesh in `org-services` (ns absent) | ❌ |
| 7.2 | terminal | `kubectl … get deploy -n sme` → the FULL 12-deploy mesh (`admin auth billing catalog console domain ferretdb gateway marketplace notification provisioning tenant`) is still `1/1` in ns `sme` — the old ns is NOT drained/removed | ❌ — old ns `sme` fully populated, not drained | ❌ |
| 7.3 | terminal | no `org-services/provisioning` deploy to compare (7.1) — the workload never moved. (BEFORE digest `services-provisioning:87e1e28` recorded in 0.4 stands for the eventual AFTER comparison.) | N/A — no AFTER workload to byte-compare (rename not rolled) | N/A |
| 7.4 | terminal | `kubectl … get hr -A \| grep -v True` → non-Ready HRs are a **pre-existing env condition** unrelated to this ticket: `bp-catalyst-platform` is `Unknown / Running 'upgrade' action with timeout of 30m0s` (in-flight), cascading `dependency … is not ready` to `bp-continuum` + `bp-sandbox`; `walk-stranger-co/vcluster` failed its Helm install (`vcluster@0.33.3`); a few `bp-*` (cluster-autoscaler-hcloud / hcloud-ccm / velero) are un-reconciled. `bp-keycloak` is now **True** (`Helm upgrade succeeded … chart bp-keycloak@1.4.30`) — the prior walk's keycloak chart-pull wedge is RESOLVED. **NONE of these are caused by this rename** (no rename HR was rolled). Cannot attribute a "clean rename roll" on a pre-rename env | N/A — HR churn is pre-existing (catalyst-platform upgrade in-flight + vcluster install fail), unrelated to the (un-rolled) rename | N/A |
| 7.5 | `https://console.hw158.omani.works/organizations` | `curl -s -o /dev/null -w '%{http_code}'` → `200`. The directory still serves (BSS→Organizations alias from PR #3390 intact), but this proves the OLD/unchanged state, not a post-rename equivalence | N/A — directory serves 200, but no rename occurred to be "invisible" (baseline state) | N/A |

---

## Section 8 — DoD-8: The BSS→Organizations route alias remains intact (no regression from PR #3390)

> PR #3390 already moved the UI label/route. This ticket must not break it. On hw158 the rename never rolled, so #3390's alias is trivially intact — these PASS as BEFORE-baseline survivors.

| # | Go to / Run | Do | Result | ☐ |
|---|---|---|---|---|
| 8.1 | `https://console.hw158.omani.works/bss/tenants` | `curl -si …/bss/tenants` → `HTTP/1.1 200 OK` (server: envoy, serves HTML, `content-length: 1063`). The legacy URL resolves (SPA serves the app shell; client-side route then renders Organizations). #3390 alias not regressed | ✅ — legacy `/bss/tenants` URL serves 200 (alias intact) | ✅ |
| 8.2 | `https://console.hw158.omani.works/organizations` | `curl -s -o /dev/null -w '%{http_code}'` → `HTTP 200`. The Organizations surface serves (sidebar label per #3390; not browser-verified for exact text in this curl-only walk) | ✅ — `/organizations` serves 200 (#3390 surface present) | ✅ |

---

## Generality proof (DoD §9 generality)

The rename is generic across ALL five carriers — not a per-file patch. **On hw158 NONE of the carrier classes are renamed**, so the generality claim FAILS in every cell:

| Carrier class | One concrete proof (a row above) | Status on hw158 |
|---|---|---|
| **Namespace** | 7.1 + 7.2 | ❌ ns still `sme`, `org-services` absent |
| **Chart template dir** | 1.1 + 1.5 + 1.6 | ❌ dir still `sme-services/` (29 files), renders `name: sme` |
| **API route** | 3.1 + 5.1 | ❌ `/api/v1/organizations` 404; `/api/v1/sme/tenants` is primary (no deprecation) |
| **Go handlers/types** | 1.7 + 1.8 | ❌ 27 SME handler + 76 SME store hits; 0 Org identifiers |
| **Secret** | 4.1 + 4.3 | ❌ `sme-secrets` only; catalyst-api still references it |
| **CI guard** | 6.2 + 6.3 | ❌ guard script + workflow absent |

**PASS for the whole ticket** = all of Sections 1–8 ☐ ticked AND the generality table proven AND every "after" artifact is byte-equivalent-except-names to its Section-0 baseline. A single functional drift = FAIL. **hw158 result: FAILED** — the rename is unimplemented; the env is the clean BEFORE state.

---

## Evidence index (commit under `docs/sessions/<walk-date>/evidence/` and link each from `docs/ledger/UAT.md`)

This walk is curl/kubectl/grep-only (no browser); the pasted COMMAND + OBSERVED OUTPUT lives inline in the Result columns above. The intended evidence-file set (for the eventual AFTER walk on a converged env) is:

- `s0-before-sme-ns.txt`, `s0-before-pods.txt`, `s0-before-directory.png`
- `s1-org-services-dir.txt`, `s1-render-namespace.txt`, `s1-handler-rename.txt`
- `s2-grep-residual.txt` (the §4 gate output)
- `s3-equiv-create-network.png`, `s3-vcluster.txt`, `s3-gitea-org.png`
- `s4-secret-rename.txt`, `s4-billing-success.png`
- `s5-deprecation-header.txt`
- `s6-guard-fail.txt`, `s6-guard-pass.txt`, `s6-pr-red.png`
- `s7-org-services-pods.txt`, `s7-old-ns-empty.txt`, `s7-image-equivalence.txt`, `s7-directory-after.png`
- `s8-bss-redirect.png`
