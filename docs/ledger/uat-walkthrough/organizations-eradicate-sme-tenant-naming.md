# UAT Walkthrough — ORGANIZATIONS: eradicate the `sme`/`tenant`-named subsystem into the canonical Organization machinery (ZERO behavior change) + CI naming guard

> **Ticket:** #3383 · **Slug:** `organizations-eradicate-sme-tenant-naming`
> **Bar:** #3370-grade. Every row is ONE operator action: **Go to `<URL>` | do `<click/type/run>` | see `<screen/output>` | ☐**.
> **Env:** the CURRENT converged Sovereign (at authoring time `console.hw150.omantel.biz`; substitute the live env FQDN when walking).
> **Boundary:** this walk proves the **pure mechanical/identifier rename + naming guard with ZERO behavior change**. It does **NOT** prove the behavioral model-unification onto one Organization CR — that is #3687, walked separately. Every "after" row here must be **byte-for-byte equivalent behavior** to the "before" row; the ONLY observable diffs are the *names* (namespace, route path, secret, template dir, Go identifiers).
>
> **How to read the equivalence rows:** each PASS requires the SAME downstream artifact the old name produced (same vCluster, same Gitea org, same route, same realm, same billing token) — only the identifier changed. A functional drift (different status, missing artifact, broken billing) is a **FAIL**, never a "minor diff".

---

## Section 0 — Pre-flight: capture the BEFORE baseline (run BEFORE the rename rolls)

These rows snapshot the current `sme`-named reality so the AFTER walk can prove equivalence. Run them on the env **before** the chart that carries this ticket reconciles.

| # | Go to / Run | Do | Expect | ☐ |
|---|---|---|---|---|
| 0.1 | terminal | `kubectl --kubeconfig <kcfg> get ns sme` | `sme   Active` — the persona-named namespace is live | ☐ |
| 0.2 | terminal | `kubectl --kubeconfig <kcfg> get pods -n sme` | `provisioning`, `billing`, `tenant`, `auth`, `marketplace`, `catalog`, `domain`, `notification`, `admin`, `console`, `gateway`, `ferretdb`, `sme-pg-{1,2}` Running — the whole Org-provisioning mesh lives in ns `sme` | ☐ |
| 0.3 | terminal | `kubectl --kubeconfig <kcfg> get secret -n sme sme-secrets` | `sme-secrets   Opaque   11` — the JWT bridge secret exists under its persona name | ☐ |
| 0.4 | terminal | `kubectl --kubeconfig <kcfg> -n sme get deploy provisioning -o jsonpath='{.spec.template.spec.containers[0].image}'` | record the image digest — AFTER must be byte-identical (workload unchanged, only ns name changes) | ☐ |
| 0.5 | `https://console.<fqdn>/organizations` | open the Organizations directory; note the sub-org rows + the parent (Sovereign) row | record the exact rows + badges (kind/tier/billingMode/isolation/status). AFTER directory must render the IDENTICAL rows | ☐ |
| 0.6 | terminal | `curl -s -o /dev/null -w '%{http_code}' https://console.<fqdn>/api/v1/sme/tenants -H "Authorization: Bearer <tok>"` | `200` (or `401` without token) — record the body shape; the OLD path answers today | ☐ |
| 0.7 | repo | `git -C <repo> grep -rwl "sme" core/ products/ \| wc -l` then `... "tenant" ... \| wc -l` | record the BEFORE counts (authoring sweep: 225 `sme`, 398 `tenant` file-hits) — AFTER the residual set must be ONLY the Tier enum + docs/history + one-release aliases | ☐ |

---

## Section 1 — DoD-1: The §4 rename map is fully executed in the chart & code (file existence + render)

| # | Go to / Run | Do | Expect | ☐ |
|---|---|---|---|---|
| 1.1 | repo | `ls <repo>/products/catalyst/chart/templates/org-services/` | the directory EXISTS (git-mv'd from `sme-services/`); the old `sme-services/` is GONE | ☐ |
| 1.2 | repo | `ls <repo>/products/catalyst/chart/templates/org-services/ \| grep -E '^(org-namespace\|org-services-secrets)\.yaml'` | `org-namespace.yaml` + `org-services-secrets.yaml` present (renamed from `sme-namespace.yaml` / `sme-secrets.yaml`) | ☐ |
| 1.3 | repo | `ls <repo>/products/catalyst/bootstrap/api/internal/handler/organization_provisioning.go <repo>/products/catalyst/bootstrap/api/internal/handler/organization_keycloak.go` | both files EXIST; `sme_tenant.go` + `sme_tenant_keycloak.go` are GONE (git mv preserves history) | ☐ |
| 1.4 | repo | `ls <repo>/products/catalyst/bootstrap/api/internal/store/organization_provisioning.go` | EXISTS; `store/sme_tenant.go` GONE | ☐ |
| 1.5 | terminal | `helm template <repo>/products/catalyst/chart --show-only templates/org-services/org-namespace.yaml \| grep -E 'name:|namespace:'` | the rendered Namespace `name: org-services` (NOT `sme`) | ☐ |
| 1.6 | terminal | `helm template <repo>/products/catalyst/chart \| grep -E '^\s+namespace:\s+sme\s*$' \| grep -v org-services` | **empty** — no template renders a bare `namespace: sme` for the Org mesh (Tier-value `sme` strings are not a `namespace:` field) | ☐ |
| 1.7 | repo | `grep -rwn "HandleCreateSMETenant\|HandleListSMETenants\|HandleDeleteSMETenant\|HandleReconcileSMETenant" <repo>/products/catalyst/bootstrap/api/` | **empty** — the exported handler identifiers are renamed to `HandleCreateOrganization` / `HandleListOrganizations` / `HandleDeleteOrganization` / `HandleReconcileOrganization` | ☐ |
| 1.8 | repo | `grep -rwn "SMETenantProvisionRecord\|SMETenantProvisionStore\|NewSMETenantProvisionStore" <repo>/products/catalyst/bootstrap/api/` | **empty** — store types renamed to `OrganizationProvisionRecord` / `OrganizationProvisionStore` / `NewOrganizationProvisionStore` | ☐ |

---

## Section 2 — DoD-2: The §4 grep gate — residual `sme`/`tenant` is ONLY legitimate

| # | Go to / Run | Do | Expect | ☐ |
|---|---|---|---|---|
| 2.1 | repo | `git -C <repo> grep -rwn "sme" core/ products/ clusters/ infra/` (committed as evidence) | EVERY remaining hit is one of: the **`Tier` enum value** (`tier: "sme"`, `TenantKindSME`-replacement), **docs/history/comments**, or a **one-release compat alias** that emits a deprecation header — NOTHING else | ☐ |
| 2.2 | repo | inspect the residual `tenant` hits | EVERY remaining `tenant` is a vendored/upstream ecosystem term (e.g. cert-manager/keycloak realm strings) OR an explicitly-marked compat alias — no NEW Catalyst machinery names `tenant` | ☐ |
| 2.3 | repo | `git -C <repo> grep -rwn "TenantKindSME\|Tier" products/catalyst/bootstrap/api/internal/store/` | the legitimate `Tier: "sme"` data value SURVIVES (a commercial class is data, not architecture) — this is the ONE allowed `sme` token in machinery | ☐ |

---

## Section 3 — DoD-3: Equivalence walk PRE/POST — org creation produces the IDENTICAL artifacts under the new name

> The behavioral heart. Provision the same Organization via the NEW route and prove every downstream artifact is identical (slug-based, persona-neutral) to what the OLD route produced.

| # | Go to / Run | Do | Expect | ☐ |
|---|---|---|---|---|
| 3.1 | `https://console.<fqdn>/organizations/new` | fill the create form: name `uat-equiv`, tier `sme`, submit | the form posts to the **canonical** `POST /api/v1/organizations` (verify in DevTools Network tab — request URL contains `/api/v1/organizations`, NOT `/api/v1/sme/tenants`) | ☐ |
| 3.2 | DevTools Network | inspect the create response | `202`/`200` with the SAME body shape the old endpoint returned (id + provisioning state) — record the org id | ☐ |
| 3.3 | terminal | `kubectl --kubeconfig <kcfg> get ns \| grep vc-uat-equiv` (or the slug-based vCluster ns) | the vCluster namespace appears named by the **slug** (`vc-<slug>` UNCHANGED — persona-neutral), exactly as the old path produced | ☐ |
| 3.4 | `https://gitea.<location-code>.<fqdn>/` | open the Gitea org for `uat-equiv` | the per-Org Gitea org + repo appears, slug-named, identical to old-path output | ☐ |
| 3.5 | terminal | `kubectl --kubeconfig <kcfg> get httproute -A \| grep uat-equiv` | the per-Org route renders, slug-named — same as before | ☐ |
| 3.6 | side-by-side | compare 3.3–3.5 artifacts against the Section-0 BEFORE baseline | **byte-equivalent except names** — same vCluster shape, same Gitea repo layout, same route. ANY missing artifact or different status = FAIL | ☐ |

---

## Section 4 — DoD-4: The renamed secret carries the billing flow with ZERO interruption (wire capture across the roll)

| # | Go to / Run | Do | Expect | ☐ |
|---|---|---|---|---|
| 4.1 | terminal | `kubectl --kubeconfig <kcfg> get secret -n org-services org-services-secrets` | EXISTS (renamed from `sme-secrets`); the chart rendered both names for the one-release window so consumers flip with no downtime | ☐ |
| 4.2 | terminal | `kubectl --kubeconfig <kcfg> -n catalyst-system get deploy catalyst-api -o yaml \| grep -A2 -i "JWT_SECRET\|org-services-secrets"` | the catalyst-api env `CATALYST_SME_JWT_SECRET` secretKeyRef now reads `org-services-secrets/JWT_SECRET` (the bridge secret reference is repointed) | ☐ |
| 4.3 | `https://console.<fqdn>/organizations/billing/vouchers` | open the vouchers/billing surface; issue or list a voucher | the billing call succeeds (HS256 bridge token validates against the renamed secret) — NO `503 CATALYST_SME_JWT_SECRET is not set` | ☐ |
| 4.4 | terminal | `kubectl --kubeconfig <kcfg> -n org-services logs deploy/billing --tail=20 \| grep -i "jwt\|secret\|401\|503"` | no secret-resolution errors across the roll — the billing flow consumed the renamed secret uninterrupted | ☐ |

---

## Section 5 — DoD-5: The old API path still answers with a deprecation header (one-release alias proof)

| # | Go to / Run | Do | Expect | ☐ |
|---|---|---|---|---|
| 5.1 | terminal | `curl -si https://console.<fqdn>/api/v1/sme/tenants -H "Authorization: Bearer <tok>" \| head -20` | `200` AND a `Deprecation: true` (or `Sunset: <date>` / `Warning: 299 ... deprecated`) response header — the alias answers for exactly one release | ☐ |
| 5.2 | terminal | `curl -si "https://<marketplace-host>/api/tenant/orgs" -H "Authorization: Bearer <tok>" \| head -20` | answers with the deprecation header too (marketplace alias) — the new canonical is `POST /api/organizations` | ☐ |
| 5.3 | repo | `grep -rn "Deprecation\|removal\|alias" <repo>/products/catalyst/bootstrap/api/cmd/api/main.go` | the alias routes are registered AND a removal checklist row is filed for next release (the alias is not permanent) | ☐ |

---

## Section 6 — DoD-6: The CI naming guard is LIVE and proven in BOTH directions

> Mirrors the existing `scripts/check-no-banned-words.sh` + `.github/workflows/banned-words-validate.yaml` mechanism.

| # | Go to / Run | Do | Expect | ☐ |
|---|---|---|---|---|
| 6.1 | repo | `ls <repo>/scripts/check-no-persona-machinery.sh` and `ls <repo>/.github/workflows/naming-guard.yaml` | both exist (the guard script + its workflow) | ☐ |
| 6.2 | repo | open a throw-away branch; add a NEW route `rg.Post("/api/v1/sme/widgets", ...)` to a Go file; run `<repo>/scripts/check-no-persona-machinery.sh` | the guard EXITS NON-ZERO and names the offending file:line (a new `sme`/`tenant` route/ns/template-dir/exported-identifier fails the build) | ☐ |
| 6.3 | repo | revert 6.2; add a legitimate `Tier: "sme"` data value in a test; run the guard | the guard EXITS ZERO (the ecosystem-term / data-value exemption passes — proves the guard is scoped to NEW machinery names, not legitimate values) | ☐ |
| 6.4 | GitHub | open the test PR from 6.2 against the repo | the `naming-guard` check appears RED on the PR; reverting it turns it GREEN — the guard gates merges | ☐ |

---

## Section 7 — DoD-7: The live env survives the roll — old ns drained, no orphans

| # | Go to / Run | Do | Expect | ☐ |
|---|---|---|---|---|
| 7.1 | terminal | `kubectl --kubeconfig <kcfg> get pods -n org-services` | the FULL mesh (`provisioning`, `billing`, `tenant`-renamed, `auth`, `marketplace`, `catalog`, `domain`, `notification`, `gateway`, `ferretdb`, pg pair) is Running in the NEW ns — Ready post-roll | ☐ |
| 7.2 | terminal | `kubectl --kubeconfig <kcfg> get all -n sme` | **empty** (or the ns absent) — the old persona-named namespace drained and was removed in the same release; no orphaned workload | ☐ |
| 7.3 | terminal | `kubectl --kubeconfig <kcfg> -n org-services get deploy provisioning -o jsonpath='{.spec.template.spec.containers[0].image}'` | byte-identical to the Section-0.4 BEFORE digest — the workload itself is UNCHANGED; only the namespace name moved | ☐ |
| 7.4 | terminal | `kubectl --kubeconfig <kcfg> get hr -A \| grep -v 'True'` | no bp-* HelmRelease wedged by the rename — the chart roll reconciled clean (zero-touch, no hand-patching) | ☐ |
| 7.5 | `https://console.<fqdn>/organizations` | re-open the directory after the roll | renders the IDENTICAL rows captured in Section 0.5 — the rename was invisible to the operator surface | ☐ |

---

## Section 8 — DoD-8: The BSS→Organizations route alias remains intact (no regression from PR #3390)

> PR #3390 already moved the UI label/route. This ticket must not break it.

| # | Go to / Run | Do | Expect | ☐ |
|---|---|---|---|---|
| 8.1 | `https://console.<fqdn>/bss/tenants` | navigate to the legacy URL | redirects to `/organizations` (alias preserved across the rename) | ☐ |
| 8.2 | `https://console.<fqdn>/organizations` | confirm sidebar item label | reads **Organizations** (not BSS, not Tenants) | ☐ |

---

## Generality proof (DoD §9 generality)

The rename is generic across ALL five carriers — not a per-file patch:

| Carrier class | One concrete proof (a row above) | The generic claim it proves |
|---|---|---|
| **Namespace** | 7.1 + 7.2 | the `sme`→`org-services` rename holds for the WHOLE mesh, not one workload |
| **Chart template dir** | 1.1 + 1.5 + 1.6 | EVERY template under the dir re-renders into the new ns; render byte-diff shows only names changed |
| **API route** | 3.1 + 5.1 | the canonical route serves AND the old path aliases — same pattern for tenants + marketplace orgs |
| **Go handlers/types** | 1.7 + 1.8 | the exported identifier sweep is complete across handler + store packages |
| **Secret** | 4.1 + 4.3 | the renamed secret carries the live billing flow with zero downtime |
| **CI guard** | 6.2 + 6.3 | the guard catches NEW machinery names AND exempts legitimate values — proven in BOTH directions |

**PASS for the whole ticket** = all of Sections 1–8 ☐ ticked AND the generality table proven AND every "after" artifact is byte-equivalent-except-names to its Section-0 baseline. A single functional drift = FAIL.

---

## Evidence index (commit under `docs/sessions/2026-06-16/evidence/` and link each from `docs/ledger/UAT.md`)

- `s0-before-sme-ns.txt`, `s0-before-pods.txt`, `s0-before-directory.png`
- `s1-org-services-dir.txt`, `s1-render-namespace.txt`, `s1-handler-rename.txt`
- `s2-grep-residual.txt` (the §4 gate output)
- `s3-equiv-create-network.png`, `s3-vcluster.txt`, `s3-gitea-org.png`
- `s4-secret-rename.txt`, `s4-billing-success.png`
- `s5-deprecation-header.txt`
- `s6-guard-fail.txt`, `s6-guard-pass.txt`, `s6-pr-red.png`
- `s7-org-services-pods.txt`, `s7-old-ns-empty.txt`, `s7-image-equivalence.txt`, `s7-directory-after.png`
- `s8-bss-redirect.png`
