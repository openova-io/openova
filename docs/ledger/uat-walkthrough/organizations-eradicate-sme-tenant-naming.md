# #3383 ORGANIZATIONS — eradicate the `sme`/`tenant`-named subsystem (acceptance walk)

**The story:** the platform has ONE Organization machinery and it is NAMED as such. The subsystem that provisions + serves Organizations was named after a single persona — `sme` — across its Kubernetes namespace, chart template directory, API routes, Go handlers, Go types, and a JWT secret; and the GLOSSARY-banned `tenant` named the rest. Both are eradicated into the canonical **Organization** machinery with **ZERO behavior change** — proven by PRE/POST equivalence. `sme` survives **only** as the legitimate `Tier` data value (`tier: "sme"|"corporate"`). A CI naming guard prevents the persona name from re-entering machinery.

This doc is the click-by-click acceptance walk for #3383. **Section 0** = a BEFORE baseline (capture the equivalence target). **Sections 1-9** map 1:1 to the DoD boxes. **Appendix** = automated probes (NOT acceptance — the founder-walkable rows above are).

> **Build status (2026-06-17).** The rename ships on `feat/3383-organizations-rename` (chart `bp-catalyst-platform` 1.4.656; `_template` slot-13 pin in lockstep). It carries **ZERO behavior change** — every change is a rename + a one-release alias + a CI guard. The live walk runs on a fresh prov carrying this pin; the ☐ checkboxes flip with evidence once the carrying env reconciles. The chart `helm template` byte-diff + `git grep` residual + the guard self-test are the binary "is the rename complete + clean" probes the founder runs FIRST (Sections 1-2, 6).

---

## Section 0 — BEFORE baseline (the equivalence target)

Capture these on the prior env (or from the prior chart pin) so Section 3 can prove the POST behavior is byte-equivalent-except-names. This is the equivalence target, not a DoD box.

| # | Capture | How | Note |
|---|---|---|---|
| 0a | Org-create output shape | `POST /api/v1/sme/tenants` (old path) on the prior env → record the created `vc-<slug>` vCluster, Gitea org, route, realm | the rename must reproduce ALL of these identically under the new path |
| 0b | Voucher/billing bridge | `/sme/billing/vouchers` succeeds (no 503) | the renamed secret must carry the SAME `JWT_SECRET` bytes |

---

## Section 1 — §4 map fully executed in chart & code (DoD-1)

| # | Check | Command | Expected | ☐ |
|---|---|---|---|---|
| 1a | chart template dir renamed | `ls products/catalyst/chart/templates/` | `org-services/` exists; `sme-services/` **gone** | ☐ |
| 1b | persona-prefixed chart files renamed | `ls products/catalyst/chart/templates/org-services/` | `org-namespace.yaml` + `org-services-secrets.yaml` present; no `sme-namespace.yaml`/`sme-secrets.yaml` | ☐ |
| 1c | Go handler/store files renamed | `ls products/catalyst/bootstrap/api/internal/{handler,store}/` | `organization_provisioning.go` (handler + store) + `organization_{keycloak,gitops,dns}.go` present; old `sme_tenant*.go` **gone** | ☐ |
| 1d | render uses the new namespace | `helm template cat products/catalyst/chart --set ingress.marketplace.enabled=true \| grep -cP 'namespace:\s*org-services\s*$'` | **42** (non-zero); the mesh renders into `org-services` | ☐ |
| 1e | zero bare `namespace: sme` for the mesh | `helm template … \| grep -cP 'namespace:\s*sme\s*$'` | **0** | ☐ |
| 1f | exported Go machinery identifiers gone | `git grep -n 'HandleCreateSMETenant\|SMETenantProvisionStore' -- core/ products/` | **empty** | ☐ |

---

## Section 2 — §4 grep gate (DoD-2)

| # | Check | Command | Expected | ☐ |
|---|---|---|---|---|
| 2a | no LIVE `namespace: sme` (non-comment) | `git grep -nP '^\s*namespace:\s*sme\s*$' -- core/ products/ clusters/ \| grep -v contabo` | **empty** (the live mesh is `org-services`) | ☐ |
| 2b | no NON-alias `/api/v1/sme/` route | `git grep -nP 'rg\.\w+\("/api/v1/sme/' -- products/ \| grep -v deprecatedAlias` | **empty** (every `sme` route is a one-release alias) | ☐ |
| 2c | residual `sme` is only {Tier value, wire-stable data, docs/history, aliases} | `git grep -wn 'sme' -- core/ products/ clusters/` | every hit is the `Tier` value, a wire-stable identifier (see the wire-stable table in the PR), docs/history, or an annotated alias — **no live machinery** | ☐ |

---

## Section 3 — equivalence walk PRE/POST on the live env (DoD-3)

| # | Action (real human) | Expected | ☐ |
|---|---|---|---|
| 3a | create an Organization via the **canonical** `POST /api/v1/organizations` (DevTools Network confirms the path is `/api/v1/organizations`, **not** `/sme/tenants`) | the response carries the SAME `steps[]` shape as the Section-0 baseline | ☐ |
| 3b | (verify) the produced artifacts match the baseline | the IDENTICAL `vc-<slug>` vCluster, Gitea org, route, and realm as the old path produced (side-by-side vs Section 0) | ☐ |
| 3c | the legacy path still works (alias) | `POST /api/v1/sme/tenants` produces the same result **and** returns a `Deprecation: true` header (Section 5) | ☐ |

---

## Section 4 — billing flow consumes the renamed secret with ZERO interruption (DoD-4)

| # | Action | Expected | ☐ |
|---|---|---|---|
| 4a | (verify) the renamed secret exists | `kubectl -n org-services get secret org-services-secrets` → `Opaque`, has `JWT_SECRET`; the legacy `sme-secrets` ALSO present (one-release alias) with the **same** `JWT_SECRET` bytes | ☐ |
| 4b | catalyst-api secretKeyRef repointed | the catalyst-api `CATALYST_SME_JWT_SECRET` env reads `org-services-secrets/JWT_SECRET` (chart `api-deployment.yaml`) | ☐ |
| 4c | open `console.<fqdn>/organizations/billing/vouchers` | the vouchers surface succeeds — **no** `503 CATALYST_SME_JWT_SECRET is not set` across the roll; wire capture clean | ☐ |

---

## Section 5 — old API paths answer with a deprecation header (DoD-5)

| # | Action | Expected | ☐ |
|---|---|---|---|
| 5a | `curl -i -X POST https://console.<fqdn>/api/v1/sme/tenants …` | response carries `Deprecation: true` + `Sunset: …` + `Link: </api/v1/organizations>; rel="successor-version"`; the request still succeeds | ☐ |
| 5b | `curl -i https://marketplace.<fqdn>/api/tenant/orgs …` | response carries `Deprecation: true` + `Link: </api/organizations>; rel="successor-version"`; still succeeds | ☐ |
| 5c | removal checklist filed | the "alias-removal next release" checklist (below) exists on #3383 | ☐ |

---

## Section 6 — CI naming guard LIVE, proven BOTH directions (DoD-6)

| # | Check | Command | Expected | ☐ |
|---|---|---|---|---|
| 6a | guard script + workflow exist | `ls scripts/check-no-persona-machinery.sh .github/workflows/naming-guard.yaml` | both present | ☐ |
| 6b | guard proves BOTH directions | `bash scripts/check-no-persona-machinery.sh --self-test` | `SELF-TEST OK: new /api/v1/sme/ … REJECTED (RED)` **and** `SELF-TEST OK: Tier "sme" … PASS` → `BOTH directions proven` | ☐ |
| 6c | guard is clean on the working tree | `bash scripts/check-no-persona-machinery.sh` | `OK: no persona-named machinery introduced.` | ☐ |
| 6d | a test PR adding `rg.Post("/api/v1/sme/widgets", …)` | the `naming-guard` workflow goes **RED** on that PR | ☐ |
| 6e | a legitimate `Tier: "sme"` passes | the guard does NOT flag `tier: "sme"` / `TenantKindSME` (the data exemption) | ☐ |

---

## Section 7 — live env survives the roll (DoD-7)

| # | Check | Command | Expected | ☐ |
|---|---|---|---|---|
| 7a | the new namespace is healthy | `kubectl get pods -n org-services` | the full mesh `Ready` (provisioning, billing, auth, marketplace, catalog, domain, notification, admin, console, gateway, ferretdb, sme-pg-*) | ☐ |
| 7b | the old namespace is drained | `kubectl get all -n sme` | empty (the `sme` ns drained/removed in the same release) | ☐ |
| 7c | workload images byte-identical | compare workload image digests pre/post | identical (zero behavior change) | ☐ |
| 7d | no bp-* HR wedged | `kubectl get hr -A \| grep -v True` | none non-Ready attributable to this rename | ☐ |
| 7e | directory renders identical rows | open `console.<fqdn>/organizations` | the same Organization rows as before the roll | ☐ |

---

## Section 8 — BSS→Organizations alias not regressed (DoD-8)

| # | Action | Expected | ☐ |
|---|---|---|---|
| 8a | open `console.<fqdn>/bss/tenants` | redirects to `/organizations` (PR #3390's alias, not regressed) | ☐ |
| 8b | inspect the sidebar | still reads **Organizations** | ☐ |

---

## Section 9 — generality proof (DoD-9)

The rename mechanism applied to ONE carrier (the namespace) is the SAME mechanism applied to all five — proven by the byte-diff + the guard catching ANY new persona-named machinery regardless of carrier.

| Carrier | Old | New | Proven by | ☐ |
|---|---|---|---|---|
| K8s namespace | `sme` | `org-services` | render shows `namespace: org-services`, zero bare `namespace: sme` (1d/1e) | ☐ |
| chart template dir | `templates/sme-services/` | `templates/org-services/` | `ls` (1a); `helm template` byte-diff shows only names/ns changed | ☐ |
| API route | `/api/v1/sme/tenants` | `/api/v1/organizations` (+ alias) | canonical route serves; old path 200 + `Deprecation` header (3/5) | ☐ |
| Go handlers/types | `HandleCreateSMETenant` / `SMETenantProvisionStore` | `HandleCreateOrganization` / `OrganizationProvisionStore` | `git grep` empty (1f); `go build` + `go test -race` green | ☐ |
| Secret | `sme-secrets` | `org-services-secrets` | both rendered with identical `JWT_SECRET`; consumers repointed (4) | ☐ |
| **guard generality** | — | — | the guard catches a NEW persona name on ANY carrier (route/ns/dir/Go id), not per-carrier (6b/6d) | ☐ |

**Generality statement:** a rename that fixes the route but leaves the namespace, or a guard that catches routes but not template dirs, is a per-carrier special-case and FAILS. PASS requires the byte-diff showing only names changed and the guard catching ANY new persona-named machinery regardless of carrier.

---

## Alias-removal checklist (next release — DoD-5c)

When `compat.smeSecretsAlias` flips to `false` and the deprecation window closes, remove (each a one-line revert of an annotated `naming-guard: alias` block):

- [ ] chart: drop the `sme-secrets` dual-render in `templates/org-services/org-services-secrets.yaml` (set `compat.smeSecretsAlias: false` / delete the alias branch)
- [ ] chart: drop the legacy-secret fallback `lookup "sme" "sme-secrets"` in the same template
- [ ] catalyst-api: delete the `deprecatedAlias`-wrapped `/api/v1/sme/{tenants,users,bss,orders,billing,commerce,consumption,organizations}` registrations in `cmd/api/main.go`
- [ ] tenant service: delete the `deprecatedAlias`-wrapped `/tenant/orgs[...]` registrations in `core/services/tenant/handlers/routes.go`
- [ ] gateway: delete the legacy `/api/tenant/*` PathPrefix entries in `core/services/gateway/main.go`
- [ ] marketplace SPA: repoint `/tenant/orgs` → `/organizations` in `core/marketplace/src/lib/api.ts` (it rode the alias until now)
- [ ] re-run the naming guard — it should still pass (the aliases are gone, so no annotation needed)

---

## Appendix — automated probes (NOT acceptance)

Acceptance is the founder walking the clickable rows above. These are the supporting automated probes (CI):

- `go build ./...` + `go test -race ./...` on the catalyst-api module (`products/catalyst/bootstrap/api`) and the touched `core/services/*` modules — GREEN.
- `helm lint products/catalyst/chart` + `helm template … ` — GREEN, renders only names/ns changed.
- `bash scripts/check-no-persona-machinery.sh --self-test` — both directions proven.
- `git grep -n 'HandleCreateSMETenant\|SMETenantProvisionStore' -- core/ products/` — empty.
