# UAT walkthrough — Make the canonical Organization/Application CR model actually LIVE

> Issue: **#3687** (survivor; folds #3690 #3692 #3694 #3669 #3673 #3677).
> Format: click-by-click UI-walk table — `Go to <URL>` · `Do <action>` · `Expect <screen/CR>` · `☐`.
> Acceptance = the founder walking every row on a **fresh, converged** Sovereign and ticking each box with a screenshot / `kubectl` capture. Every box is binary.
> Live baseline captured on **hw150** (`console.hw150.omantel.biz`, cut-over 2-region prod Sovereign) on 2026-06-16. The "Observed today" column records the failing baseline this ticket closes.

The walk is split into seven lanes, one per folded scenario, plus a cross-lane generality gate. Every lane proves a distinct write/convergence/read seam of the ONE canonical Organization/Application CR model.

---

## Pre-flight (operator state)

| # | Go to | Do | Expect | ☐ |
|---|---|---|---|---|
| P1 | `https://console.<sovereign>/` | Open the console root URL (PIN-logged-in as owner-tier) | Lands on `/dashboard` signed in as `emrah.baysal@openova.io` (`tier=owner`) — no login form | ☐ |
| P2 | terminal | `kubectl get crd \| grep -E 'organizations.orgs\|applications.apps\|environments.catalyst'` | All three CRDs present | ☐ |
| P3 | terminal | `kubectl -n catalyst-system get deploy \| grep -E 'organization\|application\|environment'` | All three controllers `1/1 Running` | ☐ |

---

## Lane A — Signup writes ONE Organization CR, not a FerretDB store.Tenant row  (folds #3690)

The customer must end up as a single `orgs.openova.io/v1 Organization` CR that is the source of truth; the `sme`-namespace store row (if retained) is only a projection of that CR.

| # | Go to | Do | Expect (target) | Observed today (hw150) | ☐ |
|---|---|---|---|---|---|
| A1 | marketplace funnel redeem URL `https://marketplace.<sovereign>/redeem/?code=<CODE>` | Complete signup / checkout for org `acme` | Funnel success page | Funnel success page | ☐ |
| A2 | terminal | `kubectl get organizations.orgs.openova.io -A` | A new `acme` Organization CR exists — it is the write authority | ❌ `No resources found` (signup wrote a `store.Tenant` row via `handlers.go:231 CreateTenant`; CR is a dead async attempt) | ☐ |
| A3 | terminal | `kubectl -n sme exec deploy/ferretdb -- <show tenant row>` (if the row is retained) | The `sme` tenant row carries the CR's UID/owner-ref — proving it is a **projection**, not the authority | ❌ the FerretDB row IS the authority; no CR backs it | ☐ |
| A4 | terminal | `kubectl delete organization acme` then `kubectl get ns acme; kubectl get vcluster -A` | Deleting the CR GC's the vCluster + realm + repos — proves the CR is authoritative | ❌ nothing to delete (no CR); the store row would survive | ☐ |
| A5 | terminal | `git grep -n 'tenant.created' core/services core/controllers products/catalyst/bootstrap/api \| grep -i 'type .*Payload'` | ONE shared payload struct across tenant-service publish + provisioning-service consume + bootstrap-API funnel | ❌ THREE divergent shapes: `handlers.go:264` flat embed, `organization_create.go:61` `tenantCreatedPayload`, `sme_tenant.go:548` `orgShape` | ☐ |
| A6 | `https://console.<sovereign>/organizations` | Open the operator org directory | Lists `acme` (the SAME CR `kubectl` shows) | ❌ console reads its own `GET /sme/tenants` source; `kubectl get organizations -A` = empty | ☐ |

---

## Lane B — Funnel and org-controller converge on ONE record/name/Git location  (folds #3673)

Today the funnel builds a `SMETenantProvisionRecord` (`sme-<uuid>` ns / `vc-<sub>` vCluster, no CR) while the org-controller builds `<slug>` from the CR — two machineries, two artifact sets, no shared key.

| # | Go to | Do | Expect (target) | Observed today (hw150) | ☐ |
|---|---|---|---|---|---|
| B1 | marketplace funnel redeem URL | Complete signup for customer `acme` | Funnel success | Funnel success | ☐ |
| B2 | terminal | `kubectl get organization acme` | A real `Organization` CR exists for the **funnel** tenant | ❌ funnel only writes `SMETenantProvisionRecord`; `nats_bridge.go:160` logs "no matching Organization CR — ack and skip" | ☐ |
| B3 | terminal | `kubectl get ns acme; kubectl get hr -n acme` | Namespace `acme` + vCluster HR — **canonical names** | ❌ funnel makes `sme-<uuid>` ns (`sme_tenant.go:643`) + `vc-acme` vCluster (`:642`) | ☐ |
| B4 | `https://console.<sovereign>/organizations` | Create a department `beta` (console button) | `kubectl get organization beta` — same CR shape as the funnel tenant | the console path reaches the org-controller, but it produces `<slug>` while the funnel produced `sme-<uuid>` — incompatible | ☐ |
| B5 | terminal | `kubectl get gitrepository,kustomization -A` | ONE Flux source reconciles BOTH `acme` (funnel) and `beta` (console) — one location, not two branches | ❌ two sources: `openova@main` + `openova@sme-tenants`; the `sme-tenants` Kustomization is even `path not found` | ☐ |
| B6 | terminal | `kubectl logs deploy/catalyst-organization-controller -n catalyst-system \| grep 'tenant.created'` | The NATS bridge now finds the matching CR (no longer "ack and skip") | ❌ ack-and-skip for every funnel tenant | ☐ |
| B7 | terminal | `git -C <worktree> diff` | The SME parallel store + `sme_tenant_gitops.go` writer are deleted (or reduced to Org-CR-keyed steps) | ❌ both machineries co-exist | ☐ |

---

## Lane C — The Org-CR vCluster is Flux-reconciled (no Ready-over-orphaned-bytes)  (folds #3669)

The org-controller must write the vCluster HR to a repo a Flux source watches, derive `status.vcluster.phase` from a readback, and set `Ready=True` only when the vCluster is actually up.

| # | Go to | Do | Expect (target) | Observed today (hw150) | ☐ |
|---|---|---|---|---|---|
| C1 | `https://console.<sovereign>/organizations` | Click "Create organization", slug `acme`, kind `internal`, submit | Row `acme` appears, status `Provisioning` | Row appears `Provisioning` (but backed by nothing) | ☐ |
| C2 | terminal | `watch kubectl get hr,ns,po -A \| grep acme` | Within 5 min: ns `acme` Active → vcluster HR → vcluster pod `Running` | ❌ ns `acme` never created; HR written to `acme/catalyst-tenant` repo that NO GitRepository watches (`organization_controller.go:288`) | ☐ |
| C3 | terminal | `kubectl get organization acme -o jsonpath='{.status.vcluster.phase}'` | `Ready` (advances only after the vCluster HR is Ready) | ❌ frozen at hardcoded `Provisioning` (`organization_controller.go:375`) | ☐ |
| C4 | terminal | `kubectl get organization acme -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}'` | `True` ONLY after the vCluster HR is Ready AND the namespace exists | ❌ `True` immediately after a Gitea PutFile (`organization_controller.go:390`, message "vCluster HelmRelease + Keycloak group + Gitea Org reconciled") — the status **lies** | ☐ |
| C5 | `/organizations` | Refresh while the vCluster is still coming up | Status sits at `Provisioning` until the pod is Ready, then flips to `Active` | ❌ badge shows green `Active`/`Provisioning` over orphaned bytes | ☐ |
| C6 | terminal | `kubectl get gitrepository,kustomization -A` | A Flux source + Kustomization reconciling the per-Org vCluster manifests is present | ❌ no `catalyst-tenant` GitRepository/Kustomization exists at all | ☐ |

---

## Lane D — The running-Application spine commits to Gitea, not etcd  (folds #3694)

Create/update/Context-append must COMMIT the Application CR to Gitea (then Flux applies); the canonical representation must be non-empty and equal the running-instance count; the loop must survive `catalyst-api` scaled to 0.

| # | Go to | Do | Expect (target) | Observed today (hw150) | ☐ |
|---|---|---|---|---|---|
| D1 | `https://console.<sovereign>/catalog` | Open `bp-wordpress` → **New instance** → name `blog`, Org `acme`, topology `single-region`; Advanced → backing postgres = **Reuse existing → shared-pg**; Provision | Provisioning starts | form renders | ☐ |
| D2 | Gitea `http://<gitea>/acme` | Open the `acme` gitops repo | A commit landed: `applications/blog.yaml` (the Application CR) AND `shared-pg`'s IaC carries a `db/blog` Context row — **both in Git** | ❌ `HandleCreateInstance` writes the CR to etcd via `endpoint_handler.go:898` `.Create`; `wireBackingServices` appends the Context to etcd `spec.parameters` (`endpoint_handler.go:1070`, comment literally calls the etcd write "the declared IaC"); Gitea untouched | ☐ |
| D3 | terminal | `kubectl get application -n acme blog` | The CR exists and was created **from** the Git commit (Flux applied it) | ❌ no Application CRs exist anywhere | ☐ |
| D4 | browser | Open `https://blog.acme.<pool-tld>` | The app is reachable (Flux fanned the commit into a HelmRelease) | n/a — no closed Git loop | ☐ |
| D5 | `/app/blog` | **Settings/Topology** → change to active-active → Save | A SECOND commit to `applications/blog.yaml` (placement diff) in Gitea → a 2nd-region HelmRelease materializes | ❌ `HandleApplicationUpdate` (`applications_update.go:202`) writes the CR to etcd via `.Update`; header claims "Application CR is the source of truth" but the CR lives in etcd, not Git | ☐ |
| D6 | terminal | `kubectl get applications.apps.openova.io -A` | **Non-empty**, equals the running-instance count (≥ the 3 shared-pg engines + gitea/harbor/keycloak/… bootstrap apps), each `bootstrap-owned`-marked, adopted not duplicated | ❌ `No resources found` while 9 postgres engines + ~64 HelmReleases run | ☐ |
| D7 | terminal | Decode `sh.helm.release.v1.shared-pg.v*` (or `helm get manifest shared-pg -n shared-data`) | The shared-PG model is ON: the chart renders the `kind: Application` self-registration CR | ❌ `16a-bp-postgres-shared.yaml:170` `enabled: ${SOVEREIGN_ENABLE_SHARED_PG:=false}` → unset on hw150 → release renders 0 bytes; `application-cr.yaml:54` gate `(ne (toString .Values.enabled) "false")` makes the whole template dead code | ☐ |
| D8 | terminal | `kubectl get cluster.postgresql.cnpg.io -A` before/after | Embedded postgres eliminated for ≥2 consumers (consumers on shared-pg Contexts; embedded `Cluster` CRs gone) | ❌ 9 embedded clusters live: `gitea-pg`, `harbor-pg`, `pdns-pg`, `pda-pg`, `sme-pg`, `newapi-…-pg`, `openova-flow-pg`, `guacamole-pg`, `cnpg-pair-…-primary` — the exact #3370 §4 violation list | ☐ |
| D9 | terminal | `kubectl logs deploy/catalyst-application-controller -n catalyst-system --tail=20` | Watch loop healthy — no `watch channel closed` / `watch error; will retry` spam | ❌ controller logs the retry warning every ~5 min (`application_controller.go:501/563`) and reconciles nothing (no CRs to act on) | ☐ |
| D10 | terminal | `kubectl scale deploy/catalyst-api -n <ns> --replicas=0`; `git push` a parameter bump to `applications/blog.yaml`; wait 5 min | The estate keeps reconciling and the hand-`git push` is applied with `catalyst-api` at 0 — the loop is catalyst-independent | ❌ no Git-resident representation exists; the estate is reconstructed from informers inside catalyst-api | ☐ |

---

## Lane E — Operator Dashboard reads the Organization/Application model, not a raw pod treemap  (folds #3692)

| # | Go to | Do | Expect (target) | Observed today (hw150) | ☐ |
|---|---|---|---|---|---|
| E1 | `https://console.<sovereign>/dashboard` | Read the landing treemap | Layer-1 default = **Organization**; a zero-customer Sovereign shows exactly ONE estate bubble, drillable to `vCluster → Application (CR)` | ❌ "Resource utilisation treemap" of raw infra pods: `cilium-agent 29%`, `falco 14%`, `kyverno 3%`, `mimir`, `harbor`, `sme-pg`; Layer-2=Application is `app.kubernetes.io/instance` of arbitrary pods (`Dashboard.tsx:348`, `dashboard.go:481`) | ☐ |
| E2 | `/dashboard` | Look for ephemeral Job cells | NO Job pod (`cutover-*`, `scan-vulnerabilityreport-*`, `*-snapshot-save-*`) ever appears as a cell | ❌ Job pods render as treemap cells/"applications" | ☐ |
| E3 | `/apps` | Count the cards | One card per `Application` CR; bootstrap-owned platform Applications carry a "Platform" badge; cards are 1:1 with `kubectl get applications -A` | ❌ ~49 cards derived from HelmReleases/pods while `kubectl get applications -A` = 0 (cards not backed by the canonical model) | ☐ |
| E4 | `/organizations/new` → submit one customer Org | After onboarding ONE customer Org, refresh `/dashboard` | A SECOND estate bubble appears AND `kubectl get organizations -A` shows 2 | ❌ with zero existing Organizations every upstream surface already shows raw pods — a real customer is indistinguishable from a platform pod | ☐ |

---

## Lane F — Showback is per-Organization (join-key), not per-pod-to-parent  (folds #3677)

| # | Go to | Do | Expect (target) | Observed today (hw150) | ☐ |
|---|---|---|---|---|---|
| F1 | `https://console.<sovereign>/organizations` | Scroll to "Showback — per-app consumption"; inspect the **Application** column | Only real tenant applications of the selected org | ❌ raw pods cluster-wide: `cnpg-pair-…-primary`, `seaweedfs`, `kyverno`, `falco`, `cilium-agent` + Job pods `cutover-harbor-prewarm-…`, 5× `scan-vulnerabilityreport-…`, `openbao-snapshot-save-…` | ☐ |
| F2 | terminal | `curl -sk .../api/v1/sme/consumption \| jq '.orgs[].apps[].application'` | Zero `batchv1.Job`-owned entries; infra namespaces (`kube-system`, `flux-system`, `trivy-system`, `kyverno`, `cnpg`, `falco`, `cert-manager`) absent from tenant `apps[]` | ❌ ~100 infra-pod rows; `orgForNamespace(_ podRow, parentOrg)` discards its arg and `return parentOrg` (`sme_consumption.go:219-220`); `aggregateConsumption` lists the whole cluster via `labels.Everything()` (`:110`) with no namespace filter | ☐ |
| F3 | terminal | `curl -sk .../api/v1/sme/consumption \| jq '.orgs[].org'` | ≥2 distinct orgs once ≥2 Organizations each own a namespace, each carrying only ITS apps | ❌ exactly ONE org (parent `hw150.omantel.biz`, `isParent:true`); a second org is structurally impossible (constant attribution) | ☐ |
| F4 | `/organizations` | Create a 2nd org + run one app in it (per Lane C), refresh the panel | A SECOND org row appears, attributed only to its own app; control-plane/Job workloads roll up under a single visually-distinct "Platform overhead" line, never inside a tenant's app list | ❌ panel can only ever show the parent with ~100 infra pods | ☐ |
| F5 | terminal | `git -C <worktree> diff sme_consumption.go` | The resolver keys purely on the `openova.io/organization` label + a config-driven exclusion list — zero per-org-name / per-namespace-name special-casing; adding a third org needs no code change | ❌ hardcoded constant; exclusion list absent | ☐ |

---

## Cross-lane generality gate (the holistic proof)

The single model must behave identically across door, kind, blueprint archetype, and cloud — never a special case.

| # | Go to | Do | Expect (target) | ☐ |
|---|---|---|---|---|
| G1 | terminal | Create an org via the **marketplace funnel** AND via the **BSS internal door** (`kind=internal`) | BOTH yield an `Organization` CR through the SAME write path, differing only by `spec` fields (kind/tier); `git grep` shows no door-specific store writes | ☐ |
| G2 | terminal | Drive `HandleCreateInstance` for ONE workload blueprint (`bp-wordpress`), ONE database blueprint (`bp-postgres`), and ONE other backing blueprint (`bp-valkey`/`bp-kafka`/`bp-seaweedfs`), across ONE single-region AND ONE multi-region placement | The SAME create→commit→fan-out→reconcile loop drives all of them; `git diff` shows ZERO blueprint-specific or cloud-specific write code beyond declarations | ☐ |
| G3 | `/app/<name>` for ≥3 archetypes | Open each app page | The IDENTICAL tab strip renders for every archetype; Contexts tab appears only for shareable blueprints; a consumer's Dependencies shows `Depends on: shared-pg / db:<ctx>` | ☐ |
| G4 | terminal | `kubectl get applications.apps.openova.io -A` + `kubectl get organizations.orgs.openova.io -A` on a FRESH converged Sovereign | Both non-empty: one Application CR per running instance, one Organization CR per tenant; the operator console and `kubectl` show the SAME sets | ☐ |
| G5 | terminal | `kubectl get hr -A` before/after the bootstrap Application-CR self-registration lands | **Zero duplicate installs** — the adoption guard ADOPTS already-installed bootstrap instances (status-only, no fan-out) — adoption unit-test cited | ☐ |

---

## Evidence index

Every ticked box gets a screenshot (`/dashboard`, `/organizations`, `/apps`, `/app/$id`, Gitea commit view) or a `kubectl`/`curl`/`git log` capture committed under `docs/sessions/<date>/evidence/` and linked from `docs/ledger/UAT.md`.

**Net acceptance:** on a fresh, converged, cut-over Sovereign — `kubectl get organizations -A` lists every tenant, `kubectl get applications -A` equals the running-instance count, every console mutation lands as a Gitea commit Flux reconciles (catalyst-api can scale to 0), and every operator day-2 business surface (Dashboard / Organizations / Showback / Users / /apps) renders the Organization → Application model with Job/infra pods excluded — not a raw pod-and-Job list.
