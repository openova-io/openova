# hw155 — Master UAT walk runbook (8 canonical TCs + master proof)

> **Env under test:** `hw155.omani.works` — a fresh Huawei **kom4dc 2-VPC mimic** (zones
> `me-east-215-a` / `-b`; "multi-region" = the two VPCs). Control-plane DNS pattern:
> `<component>.hw155.omani.works`. Tenant Org domain: `<orgslug>.omani.homes`.
> **DO NOT START until hw155 has converged** (console HTTP 200 + 0 non-Ready `bp-*` HRs both
> regions, sustained 15 min). This is a checklist to run against a live browser — not prose.
>
> **Sign-in is zero-click.** Type each surface's **bare URL** in a fresh incognito tab → land
> **already signed in as `emrah.baysal@openova.io` (admin)**. The console front door establishes
> the first session via a silent OIDC round-trip (one-time email-OTP through `catalyst-pin` →
> Keycloak `sovereign` realm). NO login form / PIN page / "Sign in with…" button anywhere — a
> redirect that ends on a login screen is a **FAIL** (the #3150 lesson: a 302-to-realm wire-proof
> is NOT acceptance; only a rendered, signed-in admin screen counts).
>
> **Evidence law:** each new env flushes ALL prior evidence. Every ✅ below must trace to a
> **same-day hw155 screenshot** under `docs/sessions/2026-06-17/evidence/`. No ✅ carried from any
> prior env (hw150/hw144/hw130). Absent feature = **FAILED**, never inherited.
>
> **Kubeconfigs:** primary region A `<hw155-a.kubeconfig>`, secondary region B `<hw155-b.kubeconfig>`.
> Grounded in the per-ticket walkthroughs under `docs/ledger/uat-walkthrough/` + the hw150 train
> walk (`docs/sessions/2026-06-16/hw150-train-walk-evidence.md`) + the hw130 #3370 / hw128+hw144
> region-kill LIVE verdicts.

---

## North Stars (founder, verbatim — every PASS/FAIL ties to one)

1. **every app IN a vCluster** (dmz/mgmt/rtz; none on host).
2. **3 shared-PG instances → 3 cards; 6–7 apps bound many-to-many.**
3. **NO login UI anywhere — visiting an app URL lands signed-in as emrah.baysal as ADMIN** (proof = surfing admin panels, no login form).
4. **agreed apps actually multi-region.**

---

## ROW 0 — MASTER PROOF (object model alive at runtime) — GATE BEFORE ANY UI WALK

**Ticket:** #3687 (canonical Org/App CR model live) · **NS:** all four (the model is the spine).

```bash
kubectl --kubeconfig <hw155-a.kubeconfig> \
  get organizations.orgs.openova.io,applications.apps.openova.io -A
```

| # | Step | Observe |
|---|---|---|
| 0.1 | Run the command above against the primary-region kubeconfig | a table prints, NOT `No resources found` |
| 0.2 | Count `applications.apps.openova.io` rows | **≥ the bootstrap apps** (the 3 shared-PG engines + gitea/harbor/keycloak/…), each `bootstrap-owned`-marked, ZERO duplicate HRs |
| 0.3 | Count `organizations.orgs.openova.io` rows | **≥1** (the parent Sovereign Org; ≥2 after TC-01 mints a tenant) |
| 0.4 | `kubectl get crd \| grep -E 'organizations.orgs\|applications.apps\|environments.catalyst'` | all three CRDs present; the 3 controllers `1/1 Running` in `catalyst-system` |

> ✅ **PASS / FAIL (binary, NS#2/all):** the command returns **NON-ZERO rows for BOTH kinds**.
> If `applications.apps.openova.io` = `No resources found` while HelmReleases run, the object model
> is dead at runtime (the hw150 failure) → **HALT. Do not walk the UI rows — they will read a
> non-canonical pod/HR projection.** This is the row-0 gate.

---

## TC-01 — FUNNEL (#3376): marketplace voucher → running app in the customer's OWN Org

**URLs:** `https://console.hw155.omani.works/bss/vouchers` · `https://marketplace.hw155.omani.works/redeem/?code=<CODE>` · terminal `kubectl` on `<hw155-a.kubeconfig>` · `https://console.<slug>.omani.homes`

| # | Click / observe | Expect |
|---|---|---|
| 1 | Operator console → **BSS → Vouchers** (`/bss/vouchers` — NOT a dead `admin.<fqdn>`). Type weak code `1234`, submit; then submit empty to auto-generate; copy `<CODE>` (e.g. `10 OMR`) | weak code **rejected** (entropy gate); a high-entropy voucher row appears `unredeemed 0/1` |
| 2 | Stranger (fresh browser, no session) → `https://marketplace.hw155.omani.works/redeem/?code=<CODE>`. View source / DevTools→Network document HTML | "Voucher valid · N OMR" on **this Sovereign's** brand chrome; HTML contains **no** `console.openova.io`, **no** `omantel.openova.io`, **no** bare `openova.io` |
| 3 | "Sign up to redeem" → `/plans` → pick plan → `/apps` → pick app (e.g. WordPress) → `/addons` → `/bcp` → pick topology → `/review` → `/checkout`, PIN-login, confirm Org slug `<slug>` (e.g. `walmart`) | each step renders; `/bcp` shows **Single-region AND Active-hot-standby** (Pillar-2 choice); the `/checkout` progress timeline **advances** (does not hang at billing — the hw150 `502` is dead) |
| 4 | (operator window) `kubectl get organizations.orgs.openova.io -A` and `kubectl get ns \| grep <slug>` within ~60s of checkout | **≥1 Organization** CR with `<slug>` appears (was `No resources found` on hw150); the Org namespace exists; org-controller fanned out per-Org vCluster + KC group/realm + Gitea org + RBAC |
| 5 | Back in the stranger's browser, follow the post-checkout redirect; open the purchased app's card → **Open** | URL becomes `https://console.<slug>.omani.homes` (per-Org, NOT operator `console.hw155…`, NOT mothership); **signed in zero-click as Org owner**; the purchased app **serves** at its own FQDN inside the Org |

> ✅ **PASS / FAIL (NS#1+NS#3, terminal):** a stranger redeems a voucher and reaches **the purchased
> app RUNNING and serving inside their own Org console at `https://console.<slug>.omani.homes`,
> signed-in with no login form**. Anything short (billing 502, no Org CR, `connection refused` on
> the tenant host, bounce to `console.openova.io/nova`) = **FAIL**.

---

## TC-02 — ORGANIZATIONS (#3378): Organization CR minted + per-Org vCluster Flux-wired

**URLs:** `https://console.hw155.omani.works/organizations` · terminal `kubectl` on `<hw155-a.kubeconfig>`

| # | Click / observe | Expect |
|---|---|---|
| 1 | Console → **Organizations** → "Create organization": slug `acme`, kind `internal`, submit | row `acme` appears, status `Provisioning` |
| 2 | `watch kubectl get hr,ns,po -A \| grep acme` | within ~5 min: ns `acme` Active → vcluster HR → vcluster pod `Running` (NOT an HR written to a repo no GitRepository watches) |
| 3 | `kubectl get organization acme -o jsonpath='{.status.vcluster.phase}'` then `…conditions[?(@.type=="Ready")].status` | `phase: Ready` (advances only after the vCluster HR is Ready) and `Ready: True` **only after** the vCluster pod is up + ns exists — the status must NOT lie green over orphaned bytes (the hw150 `organization_controller.go:375/390` defect) |
| 4 | `kubectl get gitrepository,kustomization -A \| grep -i acme` | a Flux source + Kustomization reconciling the per-Org vCluster manifests is present (the funnel + console paths converge on ONE source — no dangling `sme-tenants` "path not found") |
| 5 | Console → **Organizations** → confirm the directory badge | `acme` badges `internal` correctly (NOT mis-badged `customer/real/vcluster`); the row is the SAME CR `kubectl get organizations -A` shows |

> ✅ **PASS / FAIL (NS#1, binary):** `kubectl get organization acme` shows the CR with
> `status.conditions Ready=True` **and** a live `kubectl get gitrepository,kustomization -A` entry
> reconciling its vCluster — i.e. the Org CR is the authoritative, Flux-wired source, not a status
> that flips green over nothing.

---

## TC-03 — CONTEXTS (#3370): 3 shared-PG cards, 6–7 apps bound many-to-many

**URLs:** `https://console.hw155.omani.works/catalog/bp-postgres` · `/apps` · `/app/shared-pg` (and `-b`, `-c`) · terminal `kubectl`

> **Precondition:** the 3-instance shared-PG model must be ON this prov (`SOVEREIGN_ENABLE_SHARED_PG=true`
> at fire — it was **OFF on hw150**, the one not-met north star). If `shared-data` ns is empty / the
> `bp-postgres-shared{,-b,-c}` HRs render `enabled:false`, this TC **FAILS at the precondition**.

| # | Click / observe | Expect |
|---|---|---|
| 1 | `/catalog/bp-postgres` header | renders `⛓ shareable · db` + `multi-instance` badges; an **Instances** table lists `shared-pg / shared-pg-b / shared-pg-c` each linking to `/app/$id`; a `+ New instance` button; the ad-hoc "Data instances" panel is **ABSENT** |
| 2 | `/apps` → find the postgres instance cards | **THREE separate cards** `shared-pg`, `shared-pg-b`, `shared-pg-c`, each carrying a `⛓ N contexts` badge (NOT one merged card; the hw130 walk showed `⛓ 3 contexts` each) |
| 3 | `/app/shared-pg` → **Contexts** tab | entity-first table, e.g. `db/registry → harbor → harbor-database-secret → ready`, `db/gitea → gitea → … → ready`, `db/keycloak → keycloak → … → ready` (consumers occupy isolated `db/` children — many-to-many) |
| 4 | Repeat step 3 on `/app/shared-pg-b` and `/app/shared-pg-c` | each instance exposes ITS own Contexts set; across the 3 cards **6–7 distinct app consumers** are bound (e.g. registry/gitea/keycloak on one, sme_* / newapi / openova-flow on others) |
| 5 | `kubectl get applications.apps.openova.io -A` and `kubectl get cluster.postgresql.cnpg.io -A` | the 3 shared-pg Application CRs are Ready+bootstrap-owned; **embedded per-app CNPG clusters are eliminated for ≥2 consumers** (the gitea-pg/harbor-pg/keycloak-pg own-clusters gone — not the #3370 §4 violation list) |

> ✅ **PASS / FAIL (NS#2, binary):** `/apps` shows **exactly 3 shared-PG cards**, and across their
> Contexts tabs **6–7 apps are bound many-to-many** (each consumer in its isolated `db/` child) —
> matching the founder's "3 instances → 3 cards, 6-7 apps many-to-many".

---

## TC-04 — PLACEMENT (#3373 / NS#1): every app runs INSIDE a vCluster (none on host)

**URLs:** `https://console.hw155.omani.works/dashboard` (treemap) · `/apps` · terminal `kubectl`

> ⚠️ **Two placement states exist — know which one hw155 fired with BEFORE you grade this row:**
> - **Ratified state (#3373, hw144 5/5 PASS):** 9 apps run in vClusters (mgmt=`loki`/`mimir`/`tempo`/`nats`;
>   dmz=`coraza`; rtz=`valkey`/`seaweedfs`/`vllm`); **5 route/secret apps stay on `host` BY DESIGN**
>   — `keycloak`/`gitea`/`grafana`/`harbor`/`openbao` (+ the 3 shared-PG + `cnpg-pair-…-primary`).
>   Re-homing them needs the loft.sh-Free `sync.toHost.customResources` tether, which **breaks the
>   Pillar-5 deny-egress hold** — so on a ratified prov these on `host` are **conformant, NOT a fail.**
> - **Aspirational state (#3642):** all 7 named apps land in `mgmt` via OSS-native init-manifests
>   CRD registration (no loft.sh license). hw150 was 🟡 here (27 workloads in-vcluster, ~58 platform
>   pods still on host). Grade against THIS only if hw155 fired with the #3642 migration baked in.

| # | Click / observe | Expect |
|---|---|---|
| 1 | `/dashboard` → set the **LAYER 1** grouping combobox → **`vCluster`** | treemap regroups into top-level blocks **`host` · `mgmt` · `rtz` · `dmz`** |
| 2 | Read the **`mgmt`** block tiles | contains at minimum `loki`/`mimir`/`tempo`/`nats` (ratified); the 7 named apps `grafana`/`harbor`/`keycloak`/`gitea`/`openbao`/`newapi`/`guacamole` ALSO appear here IFF the #3642 migration is baked in |
| 3 | Read the **`rtz`** and **`dmz`** blocks | rtz = `valkey`/`seaweedfs`/`vllm`; dmz = `coraza`; tenant apps land in their Org's dmz/rtz |
| 4 | Read the **`host`** block tiles | host carries only substrate (cilium, flux, cnpg-operator, kyverno, vCluster runtimes). On a ratified prov the 5 route/secret apps may legitimately appear here; on an aspirational prov NONE of the 7 named apps should |
| 5 | `/apps` → open the `keycloak` (or any tenant app) card → detail | placement reads its declared vCluster (`mgmt`/`dmz`/`rtz`), matching `placement.yaml` |
| 6 | (appendix, not acceptance) `scripts/audit-placement-conformance.py live --kubeconfig <hw155-a.kubeconfig>` | exits **0** — "zero undeclared host workloads" (declared == actual); pods in a vCluster carry the `-x-<innerNs>-x-mgmt-vcluster` syncer suffix |

> ✅ **PASS / FAIL (NS#1, binary):** `audit-placement-conformance.py live` exits **0** — i.e.
> **every workload is where `placement.yaml` declares it, with zero UNDECLARED host workloads**, and
> the `/dashboard` vCluster grouping renders the four blocks correctly. The bar is *declared ==
> actual*, NOT "literally everything off host" — the 5 ratified route/secret apps on host are a
> conformant declaration, not a violation. An UNDECLARED app squatting on host = **FAIL**.

---

## TC-05 — SSO (#3374 / NS#3): zero-click — each app URL lands signed-in as emrah.baysal admin

**The contract:** fresh **incognito** tab per surface, type the **bare URL** → land **signed-in admin**, zero clicks. Each row carries an app-side gotcha to pre-empt (from the #3150 / #3563 lessons).

> ⚠️ **TWO root-cause gotchas that make "it worked" a lie — verify both are fixed FIRST:**
> 1. **Handover-cookie host-only bug:** the `catalyst_session` cookie must be set with `Domain=.hw155.omani.works`
>    (NOT host-only). If host-only, `api.hw155.omani.works/oidc/auth` never sees it → **every** zero-click chain
>    from a *fresh* incognito session bounces to the PIN wall. "grafana/gitea/harbor work" in a *shared* browser
>    is an artifact — they go RED on a truly fresh session. **Walk every row in its OWN fresh incognito tab.**
> 2. **Grafana cross-host `next` (#3271/#3272):** the OIDC continuation is cross-host
>    (`api.<fqdn>/oidc/auth?…redirect_uri=auth.<fqdn>/realms/sovereign/broker/catalyst-pin/endpoint`); the
>    `sanitizeNextParam` CWE-601 guard rejects the absolute URL → falls back to `/dashboard` → user lands in the
>    **console, not Grafana**. A "signed-in" screen that is the *console* when you typed the *grafana* URL is a **FAIL**.

| # | Bare URL (fresh incognito) | Expect (signed-in admin) + gotcha to verify dead |
|---|---|---|
| 1 | `https://console.hw155.omani.works/` | silent OIDC → `/dashboard` signed-in; **NO** "Sign in" / "Enter email for 6-digit PIN" / "Send code". Avatar menu reads "Signed in as emrah.baysal@openova.io"; `/users` lists exactly that owner row (not "No user access entries yet") |
| 2 | `https://grafana.hw155.omani.works/` | Grafana Home, **no login form** (`disable_login_form`); Administration nav present; `/api/user` → `isGrafanaAdmin=true`. **Gotcha:** must NOT show "Account disabled (400)" (the hw133 fail) |
| 3 | `https://gitea.hw155.omani.works/` | title "emrah.baysal — Dashboard — Catalyst Gitea"; **Site Administration** reachable (admin via `--admin-group sovereign-admins`). **Gotcha:** no `:30443`/`:443` in any redirect (#3310 class); admin page not 404 |
| 4 | `https://registry.hw155.omani.works/` | auto-redirect → `/harbor/projects`, no login form; Administration menu visible; `/api/v2.0/users/current` → `sysadmin:true`. **Gotcha:** the OIDC admin-group→Harbor-sysadmin mapping must promote (hw144 minor: login ✅ but `sysadmin:false`) |
| 5 | `https://bao.hw155.omani.works/ui/` (with the `/ui/` path) | OIDC completes → lands `/ui/vault/secrets` (engines `cubbyhole/`, `secret/`). **Gotcha:** **NO `/ui/vault/auth` token form** (the founder-witnessed fail); no `sso-landing.yaml` shim; `bound_claims.groups` must match + `?with=oidc/` form-normalize correct + callback NOT doubled `/oidc/oidc/callback` |
| 6 | `https://gua​camole.hw155.omani.works/guacamole/` | Guacamole connections list, authenticated. **Gotcha:** WAR serves at `/guacamole/` — `redirect_uri=/` → Tomcat **404** is the historic fail; confirm no login form + an admin/connection list (openid ext; JDBC-perm admin elevation is the known shortfall) |
| 7 | `https://newapi.hw155.omani.works/` **(1st visit)** | custom-OAuth "sovereign" flow → binds identity → `/console` (role=100, admin) |
| 8 | `https://newapi.hw155.omani.works/` **(2nd independent fresh incognito — the #3563 regression)** | lands `/console` again (role=100). **Gotcha:** must NOT show "This OpenOva SSO account has already been bound" or `/login?expired=true`; `localStorage.user` not null — re-login is idempotent across visits |
| 9 | `https://pdns-admin.hw155.omani.works/` | zero clicks → PowerDNS-Admin dashboard. **Gotcha:** `/oidc/authorized` hit **exactly once** — no callback loop (ERR_TOO_MANY_REDIRECTS = fail); no `/oidc/login 500` |
| 10 | `https://auth.hw155.omani.works/admin/sovereign/console/` | Keycloak admin console accepts the owner (realm-admin via `/sovereign-admins` → `realm-management:realm-admin` composite) |
| 11 | `https://openova-flow.hw155.omani.works/` and `https://hubble.hw155.omani.works/` | brief OIDC → app UI authenticated (generic `oidc-gate` pod), no app login form; hubble authenticated (not anonymous JSON / 503) |

> ✅ **PASS / FAIL (NS#3, binary):** **all 5 core surfaces (console, grafana, gitea, harbor,
> openbao) — plus the Open-button grid — land zero-click signed-in admin with an admin-panel
> screenshot**, and openbao shows NO token form, guacamole NO 404, newapi-revisit stays signed-in.
> A redirect ending on ANY login / PIN / "Sign in with…" / 404 / 500 / 503 = **FAIL** for that row.
> (newapi exposes no external HTTPRoute on some provs — if so, mark `N/A` not fail; its re-login is
> internal/M2M.)

---

## TC-06 — TOPOLOGY/DR (#3375 / NS#4): mesh + cnpg-pair + region-kill failover

**URLs:** `https://console.hw155.omani.works/cloud` · `/app/shared-pg` (or `/app/grafana`) → **Topology** tab · terminal on BOTH kubeconfigs · region-kill via **Huawei AK/SK** (NOT a wipe, NOT the bastion).

| # | Click / observe | Expect |
|---|---|---|
| 1 | `/cloud` | region count = **`Cluster 2/2`** (zones `me-east-215-a` + `-b` — the true 2-VPC count, no phantom region) |
| 2 | `/app/shared-pg` → **Topology** tab | Declared `active-hot-standby`; Effective-class + Supported are canonical (`singleton`/`active-hot-standby`/`active-passive`/`active-active`); mode radios gated to supported; a **live** Continuum status + replication-lag number; an **armed** Switchover (honest "no live DR backing" state if no pair) |
| 3 | `kubectl get continuums.dr.openova.io -A` (primary) and on region-B replica `psql -c "select status from pg_stat_wal_receiver"` | a `*-continuum` CR Reconciled; replica `status=streaming` from `<instance>-mesh-rw:5432` — **CNPG cross-region WAL streaming proven** (the hw150 NS#4 proof) |
| 4 | Start a monotonic-id INSERT loop against `<instance>-mesh-rw`; then **HARD-KILL region A** (batch-stop the region-A ECS via Huawei AK/SK — the hw128 mechanism) | ids commit against region-A primary; region-A kube API dead within ~5s |
| 5 | Watch the counter-writer + `/app/shared-pg` Topology through the kill | continuum-controller drives promote (lease expiry → `cnpg promote` → PowerDNS lua flip); switchover completes **≤30s** (hw128: 3s, hw144: ~4s); writer reconnects to new primary; `id = last_id + 1`, **zero gap** |
| 6 | `/app/keycloak` → hit `auth.hw155.omani.works` after the kill; then rejoin region A | keycloak realm/sessions survive (2nd agreed app, generality); rejoin → no split-brain (one primary, rejoined region becomes follower) |

> ✅ **PASS / FAIL (NS#4, binary):** the live **kill → promote walk** completes with **RTO ≤ 30s
> and RPO = 0 (zero transactions lost)** — the counter sequence has no gap and the survivor serves
> read-write. WAL streaming (`pg_stat_wal_receiver.status=streaming`) must be live BEFORE the kill.
> A single-region prov renders no DR machinery → every row vacuously absent = **FAIL** (the PR #1599
> anti-pattern); confirm `/cloud` reads 2/2 first.

---

## TC-07 — ROBUSTNESS (#3380): the install-record is real at RUNTIME, no user-felt crash-loop

**URLs:** `https://console.hw155.omani.works/jobs` · `/apps` · `/dashboard` · terminal on both kubeconfigs.

| # | Click / observe | Expect |
|---|---|---|
| 1 | `/jobs` (the one honest canvas) | ~N activities grouped by parent (**Phase 0 — Infrastructure** · **Cluster Bootstrap** · **Applications**); Succeeded/Running/Failed counts; cutover steps surface as jobs (Crossplane Provider Pivot, Egress Block Test). Every activity is a flux job — **no fabricated rows** |
| 2 | `kubectl get hr -A` both regions | HRs converge AND **every not-Ready HR is a deliberately `spec.suspend=true` chart** — NOT a crash. Region-A expected not-Ready: `bp-cluster-autoscaler-hcloud`, `bp-hcloud-ccm`, `bp-velero` (Hetzner-only, suspended on Huawei; `bp-velero-hcs` IS Ready). Region-B adds the 3 primary-only (`bp-catalyst-platform`, `bp-continuum`, `bp-self-sovereign-cutover`) + `rtz/bp-sandbox` (out of scope) |
| 3 | `/apps` deployments tab and `/dashboard` | header e.g. `Deployments ~49 / Catalog ~64`; ALL deployment cards `INSTALLED` (zero FAILED/INSTALLING/PENDING badges); no app card stuck "Loading status…" looping `404` (the hw150 bootstrap-HR no-CR-status defect — handled by PR #3660) |
| 4 | `kubectl -n sme get pods \| grep -E 'provisioning\|nats\|billing'` | `provisioning-*` `1/1 Running` (no `Init:0/1` CrashLoop); NATS reachable cross-node (the SME→NATS datapath has a netpol carve-out — the #3376 502 root cause); SME/billing back-half NOT crash-looping |
| 5 | Spot-check a scale-up of a proxy-globbed image (e.g. bp-vllm) | no latent kyverno-deny on pull (all images incl. upstream defaults route via the harbor proxy glob) |

> ✅ **PASS / FAIL (NS#1, binary):** on a fresh zero-touch hw155, **`/jobs` shows an honest flux-job
> canvas, all ~49 deployment cards read `INSTALLED` (zero FAILED/INSTALLING/PENDING), every not-Ready
> HR is a deliberately-suspended chart (the expected Hetzner-only / standby-spoke / primary-only set
> above), and no SME/provisioning pod crash-loops.** The bar is *install record == runtime reality*,
> NOT "literally zero not-Ready HRs" — the suspended charts are conformant, not failures. A crash-loop
> or a FAILED card backed by no suspend = **FAIL**. (Note: `bp-sandbox` ImagePullBackOff is a separate
> project, out of scope — never count it against this row.)

---

## TC-08 — CUTOVER (#3379 / NS#5): handover → 11-step → 600s deny-egress → cutoverComplete=true

**URLs:** `https://console.hw155.omani.works/sovereignty` (Settings → Sovereignty card) · `/jobs` · terminal on `<hw155-a.kubeconfig>`.

> The cutover **auto-fires post-handover** (it ran the FULL 11-step set + passed the 600s deny-egress
> sovereignty proof on hw150 — first full run; prior best was step-08 on hw148). This row re-proves
> it LIVE on hw155 and verifies the durable seal + faithful pivots the hw150 false-proof exposed.

| # | Click / observe | Expect |
|---|---|---|
| 1 | Console → Settings → **Sovereignty** card (`/sovereignty`) | `cutoverComplete: true`; all **11 steps green**; the **600s deny-egress hold PASSED**; steady "Sovereign — tethers severed" state with the "Achieve True Sovereignty" CTA hidden |
| 2 | `/jobs` → open step-08 `egress-block-test` | log shows egressDeny applied (`harbor.openova.io`/`ghcr`/`xpkg` blocked) → **fresh pull from LOCAL Harbor succeeds under denied egress** → "no new regressions during 600s window — sovereignty proof PASSED" |
| 3 | `kubectl get cm self-sovereign-cutover-status -n catalyst -o jsonpath='{.data.cutoverComplete}{" "}{.data.registriesYamlActive}'` | `true v2` — the durable flag AND the registry pivot flipped node containerd to local Harbor (hw150 left it `v1` = pivoted nothing, #3671) |
| 4 | `kubectl get secret cutover-complete -n catalyst` and `kubectl get secret ghcr-pull -n flux-system -o jsonpath='{.data.\.dockerconfigjson}' \| base64 -d` | the **sealed** `cutover-complete` secret EXISTS (a `helm upgrade` cannot revert the flag, #3667); `ghcr-pull` keys `registry.hw155.omani.works` / `harbor.<fqdn>`, **NOT `ghcr.io`** (not half-pivoted) |
| 5 | `kubectl get kustomization -n flux-system` and `kubectl get pods -A -o jsonpath=… \| grep -E 'harbor.openova.io\|ghcr.io/openova-io'` | `bootstrap-kit` + `sovereign-tls` **READY=True** at the post-cutover revision; **EMPTY** grep — no live pod pulls from the mothership |
| 6 | (deny-egress active) `kubectl run probe --rm -it --image=curlimages/curl --restart=Never -- curl -m5 https://console.openova.io/healthz` then `… https://kubernetes.default.svc` | `console.openova.io` **FAILS** (broadly denied, #3678) while the apiserver **SUCCEEDS** (no #3640 apiserver-strand — the CCNP must use `enableDefaultDeny:{egress:false}` with an explicit intra-cluster allow set, NOT `endpointSelector:{}+egressDeny` which flips ALL pods to Cilium default-deny → apiserver `10.96.0.1` unreachable → the Job's own kubectl times out → CCNP stranded → env dead); Flux GitRepository → `gitea-http.gitea.svc:3000/openova/openova` (local Gitea, not github) |

> ✅ **PASS / FAIL (NS#5, binary):** `/sovereignty` reads **`cutoverComplete=true` with all 11
> steps green AND the 600s deny-egress hold PASSED**, backed by a sealed `cutover-complete` secret,
> `registriesYamlActive=v2`, `ghcr-pull` keyed to local Harbor (not `ghcr.io`), zero live mothership
> pulls, and a witnessed call-home block of `console.openova.io` while the apiserver stays reachable.
> A green flag without the witnessed egress-block + local-pull proof = **FAIL** (the false-proof thesis).

---

## Evidence index

Every ticked `☐` above gets a same-day hw155 screenshot (or `kubectl`/`curl` capture) under
`docs/sessions/2026-06-17/evidence/`, linked back from `docs/ledger/UAT.md`. PR-merge ≠ done; only a
witnessed on-screen walk on hw155 is done. Row 0 gates everything: if
`kubectl get organizations.orgs.openova.io,applications.apps.openova.io -A` returns zero, STOP and
fix the object-model-LIVE gap before walking any UI row.

---

## PRE-WALK CORRECTIONS (from static audit 2026-06-17, /tmp/hw155-prewalk-audit.md)

**⚠️ ROW-0 master proof — prevent false-halt.** The 3 shared-PG `Application` CRs are Capabilities-skipped at slot-16 install (CRD ships in slot 13: `platform/postgres/chart/templates/application-cr.yaml:54`), so `kubectl get applications.apps.openova.io -A` reads EMPTY for ~15 min post-converge. BEFORE asserting ROW-0, force the post-CRD reconcile:
```
flux reconcile hr -n flux-system bp-postgres-shared
flux reconcile hr -n flux-system bp-postgres-shared-b
flux reconcile hr -n flux-system bp-postgres-shared-c
```
Then re-run the master proof. Organizations CR should be non-empty immediately; Applications after the reconcile.

**TC-08 build-provenance gate (run FIRST, before TC-08 steps).** Confirm hw155 sourced main, not a stale branch:
```
kubectl get hr bp-self-sovereign-cutover -n flux-system -o jsonpath='{.spec.chart.spec.version}'   # MUST be 0.1.75 (not 0.1.73)
kubectl -n catalyst-system get deploy catalyst-api -o jsonpath='{..image}'                          # MUST end :d2e8c02 (post-seal), not :988c77c
```
If either is stale → the env is from the wrong ref; TC-08 steps 3-4 will reproduce the hw150 false-proof.

**TC-08 step-4 fix.** The cutover-complete seal is OpenBao KV-v2, NOT a k8s Secret. Use:
```
kubectl -n openbao exec <bao-pod> -- bao kv get -mount=secret catalyst/cutover-complete
```
`kubectl get secret cutover-complete` ALWAYS returns NotFound (even on a correct env) — do not treat that as FAIL.

**TC-08 expectation.** `cutoverComplete=false` on a fresh converged prov is EXPECTED (handover-gated; API returns 425 until sealed). The operator drives it by clicking **"Achieve True Sovereignty"** (runbook row 6.2). Only assert `=true` AFTER that action + the 600s deny-egress hold.
