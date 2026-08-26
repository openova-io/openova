# PATH TO 100% — **hw302 partition** (re-measured 2026-08-21; SUPERSEDED — current env hw305)

> **Point-in-time: hw302 (measured 2026-08-21) — SUPERSEDED. Current env = hw305** (`hw305.omantel.biz`); `UAT.md` on `origin/main` now reads **✅246 · ❌12 · ⚠️6 · ⏳22 of 286** (PR #6690, as of 2026-08-25) — re-measure this partition on hw305. The hw302 figures below (**271/286 green (94.8%)** as measured 2026-08-21, every green backed by live evidence) are retained as history. This partition maps the **15 non-green rows** to their EXACT unblock as measured live on hw302 2026-08-21. Everything below the `---` after this section is hw292/hw293/hw296/hw298-era history — those envs are wiped; read this partition first. **merge ≠ green** (founder rule): a fix on main only clears a row after a walk on an env that booted it.

### The 15 non-green rows on hw302, by unblock

| Rows | Count | Gate (measured live 2026-08-21) | Unblock |
|---|---|---|---|
| **G8, G9, 218, 219, 220, 221, 222, 223** | 8 | **Per-Org Agenity runtime is dead on hw302 — a 4-DEFECT STACK, all deploy-gated (not just the cert).** (a) **#6513** kyverno `probes-present` DENIES the bp-agenity StatefulSet (creds-resync sidecar lacks probes) → workspace never comes up — fixed in 0.5.32 / PR #6514, deploy-gated; (b) **#6352** per-Org GitOps tree missing 4 emitters (bp-agenity/keycloak/wordpress-tenant/continuum); (c) **#6314** per-Org oidc-gate points at `auth.<slug>.<pool>` (no HTTPRoute) → envoy 404; (d) **#6509** two-label-host cert-SAN. The agentic runtime is served at `<app>.<slug>.hw302.omani.works` (two labels below the pool), but the issued cert is the apex `*.hw302.omani.works` (one label, RFC 6125 → cannot match). Live: `curl https://chepherd.uatco.hw302.omani.works` → `(60) SSL no-SAN-match`. Root-caused this session — the per-Org wildcard-cert emitter (`tenant_console_tls.go::reconcileOrgWildcardCert`) is gated on `spec.TenantPublic.ParentDomain`, which internal orgs never set, while the app routes use `TENANT_PARENT_DOMAIN`. Documented on **#6509** (cert-leg comment) alongside the Keycloak-redirect leg. Secondary for G8/G9/220/221: anthropic credential input (#6477). | Land the FULL stack (#6513 probes ✓0.5.32, #6352 emitters, #6314 oidc HTTPRoute, #6509 cert) → **fresh prov** + supply the anthropic credential (#6477). Fixing #6509 alone leaves the StatefulSet denied → runtime still dead. |
| **G11, 166** | 2 | hw302 is **pre-cutover** (`cutoverComplete` not set). Both assert post-cutover sovereignty facts. | A **cutover run** on hw302 → `cutoverComplete=true`. (Proven on prior envs hw291/hw292.) |
| **165** | 1 | Cutover-step Jobs empty-actions cell. Live /jobs has 64 jobs, all `Install …` lifecycle with `jobs-cell-actions-empty-<id>` present — but **zero `cutover-step-*` rows** (pre-cutover), so the clause is vacuous here, not walkable. | Same cutover run (creates the cutover-step Jobs the clause scopes to). |
| **228** | 1 | Orphan-VPC janitor sweep on a re-prov AFTER a wipe. No console surface. | A **wipe + re-prov** cycle. |
| **189** | 1 | Region-b kubeconfig self-heals EIP→private-IP on node **restart**. No console surface; region-b kubectl (212.72.24.43) firewalled from here. | A region-b node restart + region-b cluster access. |
| **235** | 1 | grafana SSO **3/3** fresh logins → 200 (per-tick sso-bridge secret refresh). Needs 3 **independent** sessions; a single shared owner session can't produce them (bare curl → 302 to keycloak). Mechanism half already proven. | Three independent fresh logins on a fresh prov. |
| **225** | 1 | ⚠️ — **real live defect, NOT obsolete.** Per-Org `bp-newapi` HR reads Unknown 'reconciliation in progress' on both Orgs (uatco+uatbeta) after ~10h; the admin-**seed** post-install hook is not Completing. The #4278 SA-vanish bug (admin-promote CronJob borrowing the seed's HOOK SA) is ALREADY fixed on main — the CronJob now has a dedicated release-managed `$cronName` SA — so the current cause is env-specific and needs live Job logs. | **Live-diagnose** the seed-hook failure on hw302 (kubectl-gated); the clause is valid and per-Org bp-newapi is a real feature (`helmrelease_apps.go:61`, in DefaultHRAppChartVersions). NOT an adjudication. |

**Banked this session (2026-08-21):** row **243** ⏳→✅ (PR #6564) — tenant DNS split-horizon re-confirmed by live hw302 dig (7 wildcard/app hosts + apex → primary ELB `212.72.24.85`; console + marketplace → distinct console ELB `212.72.24.1`).

**Net:** 12 of the 15 need one of two infra actions — a **cutover run** (clears 165/166/G11) or **landing #6509 + a fresh prov** (clears the 8 agentic rows). 189/228/235 need restart/wipe/independent-session cycles. None is a console screenshot; none is fabricatable.

---

# PATH TO 100% — **hw292 live, cc=true** (delivery-state re-measured 2026-08-08)

> ⚠️ **The sections below the 2026-08-11 partition are hw292-era and hw292 is WIPED.** The live env is **hw293** (`hw293.omantel.biz`, dep `a0077ba47e3720e5`). Read the 2026-08-11 partition first; treat everything after it as history until re-measured.

> **Source of truth:** [`UAT.md`](UAT.md). **Current env = hw292** (`hw292.omani.works`, dep `1c56518035a83e03`, 2-region Huawei me-east-215-a/-b-1) — **fired 2026-08-03T04:04Z, converged, `cutoverComplete=true` 08:12:30Z**, and G12 region-kill re-proven **6/6 zero-touch** on 2026-08-04 (promotion T0+136s, failback with a clean re-clone, no split-brain — `docs/sessions/2026-08-04/hw292-g12-region-kill/`). hw291 was wiped 2026-07-31 08:54Z after banking cc=true.
> This file maps every non-green row to its gate + owner. A row stays non-green until the hw292 walk verifies it — **merge ≠ green** (founder rule). Nothing below is a walk stamp; this is the *fix map*, and it says exactly which fixes are already inside the image hw292 will boot.

---

## 2026-08-19 — row classification: the ❌ set is NOT 18 open defects

**Scope.** `UAT.md` on `origin/main` reads **18 ❌**. Every one carries an
`hw296` or `hw298` timestamp, and both environments are gone. So the set is a
mix of stale measurements, rows whose code has since landed, and rows blocked on
a provisioning INPUT. Sorting them changes what the next prov has to do.

Measured source-side on `origin/main`, no live env required.

### Already fixed in code — needs only a re-walk

| Row | Evidence |
|---|---|
| **213** | `orgScopedNotFound` ships in `internal/tools/catalogue.go`, and `internal/tools/org_read_class_213_test.go` passes (`rc=0`). The test generalises past the row: it enumerates EVERY Org-reachable tool in `catalogue()`, and `TestOrgReadClass_TableCoversEveryOrgReachableTool` fails if a tool is added without a row. The class is closed, not just the instance. |

### INPUT-gated — a prov without the input re-fails them (#6477)

| Rows | Gate |
|---|---|
| **G8, G9, 220, 221** | `values.yaml:152-154` sets `sovereign.anthropic.apiKey: ""` and `credentialsJson: ""` — the template's own comment reads *"empty by default → founder-supplied"*. Downstream is correct and merged (`seedAnthropicToken` → OpenBao, `generateAgenityHR` ExternalSecret, #6372 `e299dc7e4`); the chain simply has nothing at its head. **Supply the credential BEFORE walking these four — there is NO deployment-POST-body field for it** (adversarially verified `wf_7eb86a00-b1a`: `git grep -i anthropic` over `products/catalyst/bootstrap/api/internal/provisioner/` is empty with a non-vacuous control; the provisioner `Request` struct has no such field). The two REAL paths: **(A) mothership, pre-fire** — populate the mothership Secret + env (`seedSovereignAnthropicCredentials` fires once at kubeconfig postback, `kubeconfig.go:716`; env is a boot snapshot, so the mothership catalyst-api pod must be RESTARTED after the Secret edit); **(B) per-Sovereign, any time after prov, no restart** — `kubectl -n catalyst-system create secret generic sovereign-anthropic-credentials --from-literal=apiKey=… --from-file=credentialsJson=…` on the Sovereign; the seed reconciler live-reads it within ≤10 min (or immediately on any Org-create). Walking them without it measures a known-empty input. |

### Evidence conflates runtime with source — re-read before re-fixing

| Rows | Correction |
|---|---|
| **218, 223** | **CORRECTION to my first note here.** I read these as a storefront-catalog question and checked `visibility: listed` — the wrong surface. The rows are about the **per-Org GitOps emitter**: `bp-agenity` was never emitted into any Org Kustomization. That is fixed and ON main (`helmrelease_apps.go:196` `case "agenity"`, merged `8f95895cb`, verified an ancestor of origin/main). They fail on hw298 only because it is POST-CUTOVER and severed from GitHub, so a merged fix cannot reach it — the #6352 delivery wall. Now stamped ⛔ (delivery-gated); re-walk on an env that can receive main. |

### Related source defect found while classifying (#6475)

Four Blueprints are `visibility: listed` but absent from the catalog-seed under
any key — `anthropic-adapter`, `librechat`, `netbird`, `stalwart-sovereign`
(plus `qa-app`, a QA fixture that should never be customer-visible, and
`sandbox`, being unlisted in #6472). A customer can buy them; the Sovereign has
no Blueprint to install. Guard in PR #6476, deliberately unwired from CI until
the four are dispositioned.

### What this means for the next cycle

Merging code does not move these rows, and neither does editing this file. A
prov re-measures all 18 at once — and of them, 213 should flip on the strength
of code already on main, while G8/G9/220/221 will NOT flip unless the Anthropic
credential is supplied at provision time.

## 2026-08-11 — the ❌ RESIDUE on hw293, partitioned by ROOT CAUSE (61 rows)

**Scope.** `UAT.md` on `origin/main` reads **186 ✅ · 86 ❌ · 14 ☐ = 286** (parsed
with `re.split(r'(?<!\\)\|', line)` → 7 content columns; a bare `split("|")`
gives different offsets and a different count). Eight clusters were already in
flight with other owners and are excluded here: SSO/keycloak broker (30, 31, 33,
34, 35, 37, 39, 219), org-lifecycle, topology+DR (51, 52, 56, 57, G3, R12),
funnel e2e (86, 90, 233, 234, 20, 23), cutover walk (160, 162, 163, 164, 165,
184), MCP/Agenity (211, 212, 213), region-B endpoints split, and
obsolete-assertion adjudication (G3, 115, 241).

**Arithmetic correction.** Six of those excluded IDs — **165, 20, 23, 233, 86,
211** — are NOT ❌ rows at all (they are ✅ or ☐), so the exclusion removes 25
rows, not 31. **86 − 25 = 61 residue rows**, not 55.

**Everything below was measured read-only on hw293 on 2026-08-11**, against both
region kubeconfigs, with the node names checked (`…-me-east-215-a-…` /
`…-me-east-215-b-…`) before any per-region count was believed.

### The deployed artifacts named in the brief were STALE — re-establish them first

> ⚠️ **A deployed sha in this table is stale within ~30 minutes — the deploy
> treadmill rolls that fast.** Re-measure before believing any four-state call
> that turns on one. This table has now been wrong TWICE in one day: it was
> written against `85e2eaf`, corrected to `298e673`, and the artifact actually
> serving `/api/v1/version` at **2026-08-11T03:4xZ** is **`28b532a`**
> (`28b532af0`, #6129, built 03:21:54Z). Method that produced that reading, and
> the only one that works here: **10 PARALLEL fresh-TCP `curl --no-keepalive
> --http1.1`** — 5 answered `28b532a`, 5 returned `no healthy upstream` (the
> region-B split). Sequential retries cannot resample it because HTTP/2 pins one
> socket. Every "IN `85e2eaf`" call below still holds, because `28b532a` is a
> descendant — but re-test, do not assume, for any commit merged after `85e2eaf`.

| component | brief said | actually deployed 2026-08-11 | subject of |
|---|---|---|---|
| Sovereign `catalyst-api` / `catalyst-ui` | `c50f2a5` | **`28b532a`** (re-measured 03:4xZ; was `85e2eaf`, then `298e673`) | every Sovereign-side row |
| Sovereign `organization-controller` | `4d1da63` | **`581e042`** (2026-08-11, #6097) | the per-Org console rows |
| **MOTHERSHIP** `catalyst-api` | not stated | **`b2294fd`** (2026-08-10) | **30 of the 61 rows** |
| MOTHERSHIP `catalyst-ui` | not stated | **`fad88bd`** (2026-08-02) | W1, W2 |

`git merge-base --is-ancestor` against the ACTUAL artifacts, not the stated ones:
`e79715795` (#6078), `9f355d56b` (#6064), `97dc33d3e` (#6051), `5ab5ae2e6`
(#6059), `b89d0d621` (#6073) and `c50f2a57f` (#6083) are **all** ancestors of
`85e2eaf`; `0439fbea8` (#5957) and `581e042c6` (#6097) are both ancestors of
`581e042`. **None of them is an ancestor of the mothership's `b2294fd`.**

### Cluster A — the MOTHERSHIP GitOps loop is DOWN. One mechanism, **30 rows**

This is the single most decision-relevant fact in this partition and it is not
visible from any Sovereign-side read.

```
kubectl -n flux-system get deploy            # MOTHERSHIP
helm-controller          spec=0  available=<none>
notification-controller  spec=0  available=<none>
kustomize-controller     spec=1  available=<none>  unavailable=1
source-controller        spec=1  available=<none>  unavailable=1

kubectl -n flux-system get pod
kustomize-controller-dbc66fb7-zsbcp   0/1  ImagePullBackOff  5h58m
source-controller-696b487d8b-pkxb2    0/1  ImagePullBackOff  5h58m
```

Exact reason, from the pod's own waiting message:

```
Back-off pulling image "ghcr.io/fluxcd/kustomize-controller:v1.8.1":
failed to authorize: failed to fetch oauth token: unexpected status from GET
https://ghcr.io/token?scope=repository%3Afluxcd%2Fkustomize-controller%3Apull
&service=ghcr.io: 403 Forbidden
```

A **403** on the anonymous token endpoint of a PUBLIC upstream repo means a
credential IS being presented and is rejected — the mothership node ghcr
credential is expired or revoked, so containerd never falls back to anonymous.
Two of the four controllers are scaled to zero on top of that. Consequence:
`flux-system/catalyst-platform` last applied `main@sha1:d94d133e…`, the
mothership `catalyst-api` Deployment's last `Progressing` update is
**2026-08-10T12:22:10Z**, and **every `deploy: update catalyst images to <sha>`
commit on `main` is inert for the mother.**

**Four-state: MERGED-BUT-UNDEPLOYED (mothership).** Not one of these 30 rows
needs code written for it, and not one of them can be moved by a Sovereign roll,
a re-walk, or a fresh prov that keeps the same mother.

| sub-cluster | rows | producer that WRITES the surface | why it cannot run |
|---|---|---|---|
| **A1** region-B kubeconfig never forwarded → Sovereign observes 1 of 2 regions | 55, 62, 64, 65, 66, 187, 188, 189 | `products/catalyst/bootstrap/api/internal/handler/deployment_handover_export.go:588` `runSecondaryKubeconfigDelivery`, spawned at `phase1_watch.go:1476` | post-handover hook; mother-side; handover never fired (A4) |
| **A2** crossplane adoption never parameterised | G1, G6, 206, 207, 208, 239 | `post_handover_adoption_apply.go:88` `runPostHandoverAdoptionApply`, spawned at `phase1_watch.go:1587` | same hook, same gate |
| **A3** ~~deployment wizard is MOTHERSHIP-served~~ **REFUTED 2026-08-14 — it is served on the Sovereign too** | W1, W2, W5 | `products/catalyst/bootstrap/ui/src/pages/wizard/steps/` | NOT gated. `console.hw296.omani.works/wizard` renders `Step 1 of 8` with the full 8-step stepper under an authed owner session (anonymous bounces to `/login`, which is why a `curl` probe reads as "no wizard"). All three rows were walked live there on hw296 against `catalyst-ui:031db43` — see their UAT Evidence cells |
| **A4** handover never fired — Phase-1 latched `failed` | G11, 166, 227 | `phase1_watch.go` `markPhase1Done` / `fireHandover` / `shouldConvergedLateRescue` | the #6082/#6083 repair (`c50f2a57f`) + #6101 (`e4c14ad86`) are in the Sovereign image, NOT in `b2294fd` |
| **A5** region-B consumer-hub Secrets never delivered | R13, 32, 36, 67, 69, 70, 111, 235, 236 | `products/catalyst/bootstrap/api/internal/handler/clustermesh.go:2139` `syncSharedPGConsumerHubSecrets`, called at `clustermesh.go:1158` | fix #6073 (`b89d0d621`) re-bases the readiness gate onto the replica's mesh alias — in the Sovereign image, NOT in `b2294fd` |
| **A6** cnpg-pair flip never happens → zero CnpgPairs | 60, 71 | `clustermesh.go:1583` `enableCNPGPairAfterFullMesh`, gated on `countFullyMeshedRegions` (`clustermesh.go:3887`) | mother-side; live region-A HR reads `topology.crossRegion:false`, `mode:singleton`, `instances:1` |

**A5 carries a STALE CAUSE that must be retired.** Nine rows record their gate as
*"what is owed is a bp-postgres chart VERSION BUMP + publish (0.2.18 → 0.2.19)"*.
That is closed: `5ab5ae2e6` (#6059) shipped 0.2.19, all lockstep pin sites read
`0.2.19` (`clusters/_template/bootstrap-kit/16a|16c|16d`,
`platform/postgres/blueprint.yaml:33`, `platform/postgres/chart/Chart.yaml:294`),
and the live HelmRelease reads `desired=0.2.19 applied=0.2.19 Ready=True` in
**both** regions. The chart is delivered and the rows still fail — so the chart
was never the gate. Measured now, with the populated denominator as the control:

```
region A  shared-data  →  9 consumer hub Secrets present
          gitea-database-secret, grafana-database-env, harbor-database-secret,
          keycloak-database-secret, newapi-database-secret,
          openova-flow-database-secret, org-database-secret,
          pda-shared-database-secret, pdns-database-secret
region B  shared-data  →  ZERO of the 9 (only ghcr-pull, harbor-robot-token and
          the CNPG-operator-minted -app/-ca/-replication/-server/-superuser set)

region B  keycloak-0   →  Init:0/1, 11h
          MountVolume.SetUp failed for volume "keycloak-secrets":
          secret "keycloak-database-secret" not found   (x355 over 11h)
```

**Fix (one action, 30 rows):** repair the mothership node ghcr credential, scale
`helm-controller` + `notification-controller` back to 1, let the loop apply the
pending `deploy:` commits so the mother reaches ≥ `85e2eaf`.
**Verify:** `kubectl --kubeconfig <mother> -n catalyst get deploy catalyst-api -o jsonpath='{..image}'`
returns a tag T for which `git merge-base --is-ancestor b89d0d621 T` exits 0,
then `kubectl --kubeconfig <regionB> -n shared-data get secret keycloak-database-secret`
returns the object instead of NotFound.

### Cluster B — **MERGED AND DEPLOYED AND STILL FAILING**: the per-Org console listener fan-out has a MISSING PRODUCER (R16, 87, 95)

The high-value find. Every warrant these three rows are parked under is now
false, and the residual is an ordering defect nobody has filed.

Delivered, verified live rather than assumed:

* `organization-controller` image `581e042` — contains **#5957** (`0439fbea8`,
  the fan-out) **and #6097** (`581e042c6`, the region witness).
* `bp-catalyst-platform` HelmRelease `desired=1.4.1361 applied=1.4.1361
  Ready=True` — so the chart half of #6097 shipped too.
* `CATALYST_CONFIGURED_REGIONS` **is** injected into the org-controller
  container env, and `catalyst-system/sovereign-fqdn.configuredRegions` reads
  `hw-me-east-215-a-rtz-prod,hw-me-east-215-b-rtz-prod` — the witness answers
  **two** regions correctly.
* The namespaced Role `kube-system/catalyst-organization-controller-console-tls`
  exists (created 2026-08-11T00:22:11Z).

And the surface still fails:

```
region A  kube-system/cilium-gateway-console listeners:
  console-https/http, api-https/http, marketplace-https/http,
  + 5 per-Org pairs (hw293walkone, hw293walktwo, uat107org, uat107vc, hw293vch)
region B  kube-system/cilium-gateway-console listeners:
  console-https/http, api-https/http, marketplace-https/http   ← ZERO per-Org
```

The controller is not silent about it — it logs the shortfall every reconcile,
for all five Organizations, verbatim:

```
tenant console TLS reconcile (transient — requeue)
  console listener for "*.hw293walkone.omani.homes": unwired secondary region:
  1 of 1 secondary region(s) have no kubeconfig in
  catalyst/cutover-secondary-kubeconfigs
  (sovereign-fqdn configuredRegions declares 1 remote region(s);
   wired region keys: none)
```

**ROOT CAUSE — a missing producer, searched for before it was named.** The
consumer is `core/controllers/organization/internal/controller/tenant_console_tls_regions.go:301`
`consoleRegionTargets` (secret name const at `:108`), reached from
`organization_controller.go:1071` `reconcileConsoleServing`. The ONLY writer of
`catalyst/cutover-secondary-kubeconfigs` anywhere in the tree is
`products/catalyst/bootstrap/api/internal/handler/cutover_secondary_kubeconfigs.go:300`
`materializeSecondaryKubeconfigsSecret`, and it has exactly ONE call site —
`cutover.go:1531`, **inside `runCutover`**. So on any Sovereign that has not cut
over, that Secret cannot exist, and the per-Org multi-region console fan-out is
unreachable **by construction**. #6097 made the shortfall visible; it did not
supply the input. Both `secondaryKubeconfigsForCutover` (`:174`) and
`onDiskSecondaryKubeconfigPrefixes` (`:228`) already do the resolution — only
the call site is cutover-scoped.

> 🟢 **SUPERSEDED 2026-08-13 — the pre-cutover producer prescribed below ALREADY
> SHIPPED. Do not re-implement it.** `RunSecondaryKubeconfigSecretMaterializer`
> (`products/catalyst/bootstrap/api/internal/handler/secondary_kubeconfig_secret_materializer.go:117`)
> is a level-triggered materializer started **unconditionally** at
> `products/catalyst/bootstrap/api/cmd/api/main.go:1119` — not inside `runCutover`.
> It landed `8c225b9c9` on 2026-08-11 (#6027) and its own startup log reads
> *"the per-Org console credential for a peer region no longer waits on a cutover"*.
>
> The "ONLY writer … exactly ONE call site … unreachable **by construction**"
> analysis above was true when written and is false now. It is left in place
> because the consumer-side citations remain accurate; only the producer verdict
> changed.
>
> **The coupling note below is now the LIVE half.** A materializer can only
> publish what is on disk, so cluster A1's `runSecondaryKubeconfigDelivery` must
> put a PARSEABLE region-b kubeconfig at
> `/var/lib/catalyst/kubeconfigs/<depID>-<regionKey>.yaml`. Check the DISK
> contents first: an empty Secret with a healthy producer means the disk input is
> missing or unparseable — a different defect from "no producer exists", and the
> one a walker should rule out before filing anything.

**Fix (HISTORICAL — shipped, see the note above):** give the Secret a pre-cutover
producer. Materialize it from the same
on-disk `/var/lib/catalyst/kubeconfigs/<depID>-<regionKey>.yaml` set at Phase-1
convergence (or on the org-controller's own reconcile), not only inside
`runCutover`. **Coupling to state:** cluster A1's `runSecondaryKubeconfigDelivery`
is what puts a PARSEABLE region-b kubeconfig on that disk, and today the
Sovereign's copy is unusable (`AddCluster failed … no configuration has been
provided`, every 30s). Both halves must land; neither alone closes R16/87/95.
**Verify:** `kubectl -n catalyst get secret cutover-secondary-kubeconfigs` returns
the object on a pre-cutover Sovereign, then region B's
`kube-system/cilium-gateway-console` lists a `console-https-<slug>` pair, then
`curl --no-keepalive --http1.1` x12 against `console.<slug>.<pool>` returns 12/12
200 (fresh-TCP; HTTP/2 pins one backend and hides a 50% split).

> 🟢 **MOVED ON 2026-08-14 — on hw296 the fan-out WORKED and the failure is now
> one hop downstream (#6255, R16/87/90/95). Read this before re-deriving the
> producer analysis above.** The listeners reached region B's `.spec` — all ten
> of them — so `consoleRegionTargets`, the materializer and the disk kubeconfig
> are no longer the failing leg on that environment. What fails is that region B
> never *published* them: its `cilium-operator` lost the Gateway-API CRD race
> against its OWN apiserver at boot and disabled the controller for the process
> lifetime.
>
> Upstream cilium v1.19.3 `operator/pkg/gateway-api/cell.go:135` wraps CRD
> discovery in a hardcoded `context.WithTimeout(context.Background(),
> 30*time.Second)`. No pflag reaches it (cell.go:182-192), `isTransientError`
> (cell.go:323-346) does not classify the resulting `context deadline exceeded`
> as transient, and the give-up at cell.go:148-155 returns
> `{Enabled:false}, **nil**` — so the hive cell SUCCEEDS, `/healthz` stays green,
> and `initGatewayAPIController` (cell.go:210-213) returns without registering
> the controller. The A/B is same-chart / same-version / same-boot on hw296
> (dep `e689e3b34a75fdec`): region `me-east-215-a` `Invoked
> duration=16.84419141s`, Gateway 10/10; region `me-east-215-b-1` `Invoked
> duration=30.024306733s`, Gateway 10 spec / **6** status. Because the console
> EIP pool spans both regions, every per-Org door answered on exactly 50% of
> fresh TCP connections (15/30, against a 27/30 control on the SAME EIP).
>
> **Fix:** PR #6259, `bp-cilium 1.4.20` — a region-local
> `gateway-api-controller-watchdog` that runs the unbounded CRD retry outside
> the operator process and restarts the local operator once the CRDs are
> Established. The ceiling is upstream Go; there is no flag to set.
>
> **Diagnose in this order on any 2-region Sovereign** — the three legs are
> distinguishable in one command each, and confusing them has already cost
> cycles: (1) listeners absent from region B's `.spec` → the producer chain
> above; (2) present in `.spec`, missing from `.status` → #6255, confirm with
> `kubectl -n kube-system logs deploy/cilium-operator | grep gateway-api` and
> look for `Invoked duration=30.0…s`; (3) present in both and still 50% → a
> genuine edge/datapath split. Note `Programmed=False / AddressNotAssigned` is
> NORMAL in both regions under the §854 hostPort model and is not a signal.
>
> **Superseded here too:** #5511's wildcard/SNI-collision hypothesis. Region A
> admits TWO per-Org wildcards simultaneously and reports 10/10 — #5511's own
> proposed regression check, passing.

### Cluster C — Organization delete cascade (R17): **CLOSED 2026-08-11, walked green**

`97dc33d3e` (#6051) is in `85e2eaf` and `581e042c6` (#6097) is in the running
`organization-controller:852de2b`; the namespaced Role that #6097 added for the
Secret reap **exists live** — `kube-system/Role/catalyst-organization-controller-console-tls`,
creationTimestamp 2026-08-11T00:22:11Z, `secrets: [get, delete]`, bound to
`catalyst-system/catalyst-organization-controller`, delivered at umbrella 1.4.1354
against a live `bp-catalyst-platform@1.4.1366`. The cluster-wide
`secrets: [get,list,watch]` in
`products/catalyst/chart/templates/controllers/organization-controller-clusterrole.yaml:111-113`
is deliberate and is NOT the gap — the delete verb lives in the namespaced Role
by design. That reading is now **pinned** by Case 6 of
`products/catalyst/chart/tests/org-controller-region-witness-contract.sh` (#6129):
RBAC merges additively, so a `delete` added to the cluster-wide rule satisfies the
teardown exactly as well and no functional test can tell the two apart. PR #6096
proposed precisely that widening and is closed as superseded.

**Walked green 2026-08-11T02:30-02:55Z, read-only.** `auth can-i` as the
controller SA in `kube-system`: `delete` **yes** (was `no`), with the scope
control holding — `delete secrets` is **no** in catalyst-system, default and
flux-system. `p474del1`, the Org this cluster recorded wedged in `Terminating`
at 2026-08-10T23:17:42Z, is gone from `kubectl get organizations.orgs.openova.io -A`
(finalizer released) along with its namespace, StatefulSets, HelmReleases,
Certificates and Gateway listener pair; `kube-system/org-wildcard-tls-p474del1-omani-rest`
is reaped, leaving exactly 6 `org-wildcard-tls-*` Secrets against 6 live
Organizations — 1:1, zero orphans. DNS re-measured against `@1.1.1.1`: all
`p474del1` names NXDOMAIN while `console.hw293walkone.omani.homes` and
`console.uat107org.omani.rest` answer NOERROR → `212.72.24.49`.

**The flagged re-wedge risk does not apply — checked, not assumed.** The concern
was that the teardown's `consoleRegionTargets` call would let cluster B's
unwired secondary region hold `firstErr` the way the old 403 did. It cannot:
`teardownTenantConsoleTLS` (`tenant_console_tls.go:630`) accumulates an error only
from `res.Unreachable` (TRANSIENT), never from `res.Unwired` (STRUCTURAL — the
cluster-B condition). The up-path errors on `Unwired` at `:376`; the down-path
deliberately does not, which is why this cascade completed on a Sovereign whose
region B has no wired kubeconfig, and why a fresh delete completes too.

### Cluster D — merged AND deployed on the Sovereign; only a WALK is owed (8 rows)

| rows | fix in the running artifact | what the walk must do |
|---|---|---|
| 7, 15, 25 | `e79715795` (#6078) ⊂ **live `28b532a`** (re-tested 2026-08-11, not carried from the `85e2eaf` call). `OrgRow.plan` now declared at `products/catalyst/bootstrap/ui/src/lib/organizations.api.ts:68`; the seed producer `newApplicationCRFromSeed` (`endpoint_handler.go:2312`) now stamps `spec.organizationRef` | re-render `/organizations/<slug>`, `/apps`, showback |
| 8, G7 | `9f355d56b` (#6064) ⊂ **live `28b532a`** — `ListParentDomains` read the Deployment record while create read the startup seed | `/organizations/new` → the parent-domain select must populate; then door A must land a free-subdomain Org |
| 29 | #5909 aged marker rides in the deployed `catalyst-ui` | inject a stale `catalyst:authed=1` with `catalyst:authed-at` ABSENT, reload the bare console URL, assert silent re-auth rather than the PIN wall |
| 38 | `bp-newapi` 1.4.153 applied; `scripts/check-live-newapi-sso-version.sh` exits 0 | 2nd hit with an established realm session lands `/console`, not `/setup` |

**W5 — CORRECTED 2026-08-11. It does NOT belong in cluster D, and no code I can
ship today moves it. Move it to A3.** Two independent reasons, both checked
rather than inferred:

1. **Its surface is MOTHERSHIP-ONLY, warranted at the route.** The prior entry
   called the mothership gate a footnote on a source fix; it is the whole row.
   `products/catalyst/bootstrap/ui/src/app/router.tsx:766-768` says it in its
   own words — the deployments route is reachable *"via `/sovereign/deployments`
   (Catalyst-Zero) or `/deployments` (**Sovereign build, though that build never
   exposes the wizard**, so this route is effectively Catalyst-Zero only)"* — and
   `:39` keys `isCatalystZero` on `window.location.hostname === 'console.openova.io'`.
   🛑 **THAT INFERENCE IS REFUTED — measured 2026-08-14, do not re-derive it.**
   The comment is about the *deployments* route, not the wizard route, and the
   wizard route is `anonymous-first` rather than absent: on the Sovereign it
   bounces an unauthenticated visitor to `/login`, so a `curl` reads "no
   wizard" while an authed browser gets the real thing. Walked live:
   `console.hw296.omani.works/wizard` renders `Step 1 of 8` and the full
   `Organisation → Topology → Provider → Components → Marketplace → Domain →
   Credentials → Review` stepper under an owner session, served by
   `catalyst-ui:031db43`. W1, W2 and W5 are therefore **not** gated by cluster
   A; all three carry live hw296 evidence in `UAT.md`, and W2 went green there.
2. **The residue is a PRODUCT DECISION, not an unwritten fix.** #5979
   (`05d3f639c`, ⊂ live `28b532a`) already removed five of the six phantom ids
   and, more importantly, added the bound that matters: `KNOWN_UNBUILT` may no
   longer absorb a `tier: 'mandatory'` entry, enforced by a test rather than
   written down. What is left is one id — `specter` — deliberately retained as
   argued, bounded, **non-mandatory** debt at
   `componentGroups.catalog-integrity.5575.test.ts:70-77`, on the stated ground
   that Specter is an OpenOva product with marketplace copy and a documented
   `familyRequires: ['cortex']` cascade, so deleting the card is a product call.

**A real doc contradiction sits underneath it and should be settled first**, because
the wizard cannot be judged against a canon that disagrees with itself:
`docs/ARCHITECTURE.md:259` says *"Specter is a composite Blueprint typically
installed in corporate Sovereigns"*, while `README.md:130` and `docs/STATUS.md:40`
both say Specter is *"a deliverable service, **not** a Blueprint in this layout"*
and that `products/specter/` does not exist. Per the read-order, `STATUS.md` is the
today-truth and `ARCHITECTURE.md` is the target — so both can stand only if the
wizard stops offering, **today**, an id that today's platform cannot install.
Adjudicate that, then delete the card or realize `bp-specter`.

### Cluster E — Agenity: no live model credential, AND no running agent session (R19, G8, G9, 220, 221, 222)

Re-measured live 2026-08-11 in `hw293walktwo`:

```
agenity-anthropic-token                SecretSyncedError  Ready=False
agenity-mcp-bearer                     SecretSynced       Ready=True   ← control
oidc-gate-agenity-hw293walktwo-oidc    SecretSynced       Ready=True   ← control
```

Same namespace, same `ClusterSecretStore vault-region1`, same controller — store,
auth and controller all demonstrably work and exactly one remote path is empty.
The workspace half is NO LONGER absent (`bp-agenity@0.5.22` HR Ready, StatefulSet
1/1), so any evidence resting on "zero agenity HelmReleases" is retired.

**Two independent causes, and seeding the token alone closes neither row set:**
(1) #5956 — the previously seeded OAuth token is REVOKED; #4277 — nothing seeds
the per-Org OpenBao path at Org-create. Producer: `sovereign_anthropic_seed.go`
plus `products/agenity/chart/templates/externalsecret-anthropic*.yaml`.
**Four-state: NEVER WRITTEN** for the per-Org seeder; PR **#5968** (check the
credential's VALIDITY, not its delivery) is **written-but-unmerged**.
(2) The `chepherd` container has logged `alive sessions: 0` every 30s for the
pod's entire life — there is no running solo agent for a prompt to reach even
once a credential arrives.
**Anti-vacuity note for the walker:** the init container `seed-claude-creds`
exits **0** while logging `no credentials.json key in Secret — key-only mode`.
That exit code is a control that cannot fail and must never be read as evidence
the credential landed.

### Cluster F — the Jobs page, two independent defects (172, 176)

* **172** — feed scope. Every HelmRelease the projection ingests comes from
  `List()` in `FluxNamespace = "flux-system"`
  (`products/catalyst/bootstrap/api/internal/helmwatch/helmwatch.go:86`) behind a
  hard `strings.HasPrefix(name, "bp-")` filter (`:1116`, `:1408`, `:1429`,
  `:2107`, `:2627`, `:2690`). A per-Org Application HelmRelease fails BOTH gates.
  **But `a0abba92d` (#6053) IS an ancestor of the live `28b532a`** (re-tested
  2026-08-11), and this row's evidence pre-dates that roll. **Re-walk before
  re-diagnosing.** Read at source rather than trusted from the ancestry: #6053
  does NOT widen the `bp-` filter — it adds a SECOND ingestion leg
  (`helmwatch/jobs_projection.go`) keyed on `catalyst.openova.io/organization`,
  the one label BOTH Application renderers emit (`render/manifests.go:352` and
  `render/fanout.go:218`). It deliberately leaves the informer's Phase-1
  termination gate alone, and the discriminator is chosen so the per-Org
  `vcluster` HR — which carries the different key `openova.io/organization` — is
  NOT swept in. So the mechanism this row's (C) leg named is genuinely addressed,
  not merely version-bumped past.
* **176** — `products/catalyst/bootstrap/ui/src/pages/sovereign/JobsTable.tsx:816`
  gates the Re-run control solely on `isJobRetryable(job.status)`
  (`src/lib/jobs.types.ts:215`), which tests the status VOCABULARY and never
  checks that the row resolves to a concrete retryable Job. #5496's
  name-resolution fix (`8367b627a`) IS deployed and is insufficient.
  **↑ THAT CALL IS SUPERSEDED — CORRECTED 2026-08-11. 176 is NOT the fourth
  state; it is merged AND deployed and owed a RE-WALK.** The correction matters
  because "merged and deployed and still failing" is the state that justifies
  spending an engineer, and this row no longer qualifies. `e79715795` (#6078)
  landed 2026-08-11T02:00Z — AFTER the 2026-08-10 walk that produced the 422 —
  and is an ancestor of the live `28b532a` (re-tested, not carried). It fixes
  the row at its producer, with a control:
  * The 422 was correct-but-unactionable. `syft-sbom` is a SYNTHETIC collapsed
    scanner identity (#3925) — no Kubernetes object carries that name, so
    #5496's `<name>-<digits>` fallback could never bridge to the real
    `syft-grype-bp-syft-grype-<n>`. `SyftScanCronTarget`
    (`helmwatch/reconcilers.go:708`) now maps the identity back to the CronJob
    that seeds it, and `jobs_retry.go:292` re-runs there.
  * **The control that proves this is not "make aggregates return 200":**
    `trivy-security-scan` aggregates one Job per workload with no single backing
    object, and deliberately STILL 422s. `SyftScanCronTarget` returns
    `ok=false` for it by design.
  * The second half of the clause was never the code's fault either. The button
    DOES flip in place — `RetryJobButton.tsx:159` renders `Requesting…` while
    `phase === 'requesting'`, and `:135` renders `✓ Requested` on 2xx. The
    2026-08-10 walk observed at +0.7s and +5s and saw `Re-run` because the 422
    returned in well under 0.7s, so the phase had already advanced to `error`.
    A fast failure is not a missing state. What #6078 changed is that the error
    now ALSO reaches the app-wide notification centre verbatim, instead of only
    a span the table clamps to 28ch and a 5s poll can clear.
  **What the walk must do:** Status=failed → click `Re-run` on the syft-sbom row
  → expect 2xx and `✓ Requested`; then click the `trivy-security-scan` row as the
  negative control and expect the honest 422 to appear in the notification centre.

### Cluster G — a verdict published from ABSENT evidence (197, 63)

Both rows are the same defect class: an absent control rendered as a negative
fact. They are one fix pattern, not two epics.

* **197** — `products/catalyst/bootstrap/ui/src/pages/sovereign/cloud-list/ReconcileTab.tsx:103`.
  `suspended` is `useMemo`'d off the `obj` prop; the mutation's `onSuccess`
  invalidates only `reconciler-logs` and `reconciliation-dag`, never the
  resource-object query that produced `obj`, so the button cannot flip after its
  own action. And `Boolean(obj?.spec?.suspend)` collapses "the object fetch
  failed" into a confident `false`, which `ResourceDetailPage` then renders on
  the `objErr` branch as `SUSPENDED: no`. #6085.
* **63** — `TopologyTab` maps `status.perCluster`'s single `singleton` entry and
  **returns early**, never reaching `status.targets`, which on the same object
  carries the correct Primary/Standby pair. `showDR` goes false and the whole DR
  block is skipped — so an ABSENT control is served where the clause requires a
  **disabled** one with a named reason. An absent control and a disabled one are
  not the same statement, which is precisely what this row exists to forbid.

### Cluster H — nine rows, nine independent causes

| row | cause + the component that WRITES the surface | state |
|---|---|---|
| 16 | **CAUSE SUPERSEDED 2026-08-11 by the re-measurement in PR #6128 — the old entry below is kept only so the change is legible.** ~~the per-Org console's app-detail path calls SOVEREIGN-ADMIN routes and gets `403 org-scoped-forbidden` (`catalog.api.ts:297/540/599` + `pages/sovereign/AppDetail/`); needs an Org-scoped app-detail data path~~. The 403 is no longer what this row dies on. **The measured failure is now a WRITE-SHAPE defect:** Save issues a `PUT`, gets **200**, and bumps `metadata.generation` — so every signal the UI reads says it persisted — while `spec.placement` is written as a bare **string** and `parameters.topology` stays `singleton`. The control that makes the shape the finding rather than a guess: the sibling Application `uat50-ahs-pg` holds `spec.placement` as an **object**. A 200 plus a generation bump is exactly the "verdict from absent evidence" shape cluster G is about — the surface reports success for a write the model did not accept | **merged AND deployed AND still failing** — the ONE true fourth-state row in D/F/H. **FIXED 2026-08-11 (this PR)** at `applications_update.go`: PRESERVE an existing object placement and update `mode` in place; PROMOTE to the object form when the body names a WHERE field; otherwise the scalar stays a scalar. Red-first guard on the VALUE SHAPE plus a string-stays-string CONTROL that shares the suspect property. **Now DEPLOY-GATED then walk** |
| 19 | ADJUDICATED 2026-08-11 (#5867) — NO code change indicated. `/apps` answers BOTH, disambiguated by the `data-card-kind` attribute #6056 shipped: the estate half is one `[data-card-kind="instance"]` card per Application CR (already Application-keyed; pinned Go-side by `sovereign_apps_one_card_per_cr_5429_test.go` and front-end by `AppsPage.one-card-per-application-19.test.tsx`), the remainder is the install catalog, deduped against every Blueprint that has an instance. The clause now names the selector to count | clause corrected — WALK IT: count `[data-card-kind="instance"]` |
| 109 | the Keycloak account console hangs on `Loading the Account Console` while `/account/config` answers 200 — the clause wants a loud legible refusal and gets an indefinite spinner. **STATE CORRECTED 2026-08-11: a producer EXISTS and was written for this exact measurement.** `platform/keycloak/chart/templates/httproute.yaml:69-122` intercepts `/realms/<realm>/account` + `/account/` with a **302 to `https://console.<sovereignFQDN>/settings`**, default-on at `values.yaml:745-747`. The matches are `Exact`, never `PathPrefix`, deliberately — so `/account/config` keeps answering 200 and the anti-vacuity control this row relies on survives the fix. Two causes are RULED OUT by search, not assumption: there is NO custom theme anywhere in the repo (zero `*.ftl`, zero `theme.properties`, zero `loginTheme/accountTheme/adminTheme`), and the realm's `account-console` client is re-declared at `configmap-sovereign-realm.yaml:567-572` ONLY to bind `browser-no-idp` (#3934), not to disable it. The hang is the stock `keycloak.v2` SPA swallowing its own failed bootstrap, which is why an intercept — not a Keycloak setting — is the right fix. The no-link half is enforced in source: `RequiredActionsModal.tsx:121` `const accountUrl = '/settings'` | **merged BUT UNDEPLOYED** — the chart went 1.5.7 → **1.5.8** (`Chart.yaml:472`, slot pin `09-keycloak.yaml:229`); hw293 was measured on **1.5.7**. Re-walk once `bp-keycloak 1.5.8` reconciles; expect `/realms/sovereign/account/` → **302** and `/account/config` → still 200 |
| 218 | **RESOLVED 2026-08-14 (PR #6316) — walked ✅ on hw296, no code was needed.** Both halves were exercised as the Org User on `console.walkthree.omani.trade`: the Agenity card renders in the per-Org `/catalog` from real catalog data, `+ New instance` opens with the Organization pre-selected to `walkthree` and **"Environment: walkthree-prod"** as the derived target, and the wizard completed a real install (`POST /catalyst/v1/apps/instances` → 201) whose CR carries that same `environmentRef`. The earlier entry below described a candidate-filter reading that the walk did not reproduce; it is kept so the change is legible. ~~#5823 deliberately excludes the parent self-org, so a list whose only member IS the parent resolves to zero candidates~~ — that describes the CONSEQUENCE, not the cause, and the predicate it blames is **correct**: `InstancesSection.tsx:531` filters `!o.isParent && !!o.slug`, and `isParent` is set STRUCTURALLY (`organizations.api.ts:198` hardcodes `true` for the parent row, `:268` hardcodes `false` for every sub-Org), so it cannot misclassify `hw293walkone`. The candidate array was genuinely `[parent]` because the sub-Org half returned nothing. **The real cause is one hop upstream:** `GET /api/v1/organizations` answered **403 `org-scoped-forbidden`** to an org-scoped caller, and `bss.api.ts:826-828` swallows a non-OK into `[]` — indistinguishable from "no Orgs". The fix landed in **`fa116941e`** (#6089/#6081): `organization_provisioning.go:954` now confines the response to the caller's own slug (`confineOrgResponsesToSlug`, `:967`) and `org_scope.go:328` admits the path GET-only, leaving POST/DELETE 403. Pinned by `org_directory_scope_6081_test.go:78/:124/:158` | **merged AND deployed** — `fa116941e` re-tested ⊂ live `28b532a`; the 2026-08-10 measurement PRE-DATES it (merged 23:41Z). **RE-WALK, zero code owed** |
| 219 | **THREE independent causes, measured on a clean Application-CR install 2026-08-14 (PR #6316); the install half itself is GREEN.** (1) `ResourceQuota/plan-quota` in `walkthree` is FULL at `limits.cpu=4/4` before the install asks for anything, so `statefulset-controller` answers `FailedCreate ... exceeded quota` x12 and the pod is never created — **#5393**, written by `core/controllers/organization/internal/gitops/manifests.go:120-126`, OPEN and founder-gated. (2) The Application fan-out HelmRelease carries `install: null` / `upgrade: null` / `timeout: null`, so Helm's default 300s applies and one failure sets `Stalled=True RetriesExceeded` forever — the fix is **#6286** (`core/controllers/application/internal/render/fanout.go` + `application_controller.go`, `spec.timeout` 900s + `remediation.retries` 3) and it is NOT in the deployed application-controller, read off a HelmRelease that binary wrote six minutes before the stamp. (3) The expired Anthropic credential of row 220 (below) — control: `g11probe/bp-agenity-0`, a fresh install of the same chart, `Init:CrashLoopBackOff`. The console-reachable leg was a FOURTH cause and is **FIXED** in PR #6316 (#6314). | (1) founder-gated · (2) merged, awaits deploy · (3) needs a credential refresher |
| 220 | **the seeded Anthropic credential is a ~4-hour OAuth ACCESS token and nothing in this repo refreshes it.** `claudeAiOauth.expiresAt` on hw296 decoded to 2026-08-14T04:28:12Z while `refreshTokenExpiresAt` is 2026-09-10 — the refresh material is alive and unspent. Worse, the `anthropicApiKey` property is **byte-identical to that access token** (both 108 bytes, same sha256 prefix, both `sk-ant-oat…`), so both env paths the chart offers carry the same short-lived value and there is no long-lived fallback. The producer is `seedAnthropicToken` (`products/catalyst/bootstrap/api/internal/handler/sovereign_anthropic_seed.go`), whose own doc comment states the design: *"headless claude-code does NOT reliably refresh it, so the operator rotates the env-sourced Secret"*. Measured consequence: the agent answers `API Error: 401 OAuth access token has been revoked` on its own PTY, and `anthropic.onExpiredCredential=fail` (#6163) keeps every FRESH install from starting at all. **THE EXCHANGE ALREADY EXISTS IN THIS REPO, APPLIED TO THE WRONG PATH** — `products/axon/chart/templates/token-refresh-cronjob.yaml` trades the refreshToken for a new accessToken and writes the whole `claudeAiOauth` blob back; nothing equivalent tends OpenBao `catalyst/anthropic/token`, the path every Org's agenity ExternalSecret reads. The model half of this row is GREEN and its clause is NOT stale (`claude-opus-4-7` confirmed three live ways). | needs the axon refresher pointed at `catalyst/anthropic/token` · Refs #4277 #6163 |
| 221 | **one shared cause with row 220 — every other leg is proven.** The agent's Org-scoped bearer (`org: walkthree`, `tier: org-admin`), `create_application` → HTTP 201 → Application CR, and the *"appears provisioning in the user's `/apps`"* leg (the agent-created `uat-agent-alloy` / `uat-agent-wp` render as `walkthree-prod · singleton · INSTALLING` cards in the Org console, asserted on the DOM) were all walked green 2026-08-14 (PR #6316). Only the clause's opening *"the user CHATS with the solo agent"* fails, and it fails on row 220's expired credential. Fix row 220 and this row flips with it — no separate work is indicated. | blocked on row 220 |
| 223 | **RESOLVED 2026-08-14 (PR #6316) — walked ✅ on hw296 with the AGENT'S OWN bearer, and the transport reading below is now out of date.** The Org-scoped tool surface no longer lives only in the pod's stdio subprocess: a per-Org MCP door was installed through the product's own console wizard (`Application/walkthree-mcp`, HR `Ready=True` in 17s, serving `mcp.walkthree.omani.trade/mcp`), and the chart-wired `walkthree/agenity-mcp-bearer` — the credential the solo agent signs tool calls with — opens it (`whoami` → `org_id: walkthree`, `sovereign_admin: false`). The anti-vacuity warning is HONOURED, not evaded: `tools:[]` on an unauthenticated call is exactly why every leg below carries a control. `list_applications` → 21 items, all walkthree's, ZERO of the 17 foreign apps the Sovereign holds; cross-Org `get_application` → `-32000 "... not found in organization \"walkthree\""`, byte-identical in shape to a genuine-miss control and never naming the other Org, against an own-Org 200; cross-Org `create_application` → `-32003 forbidden / 403` for walktwo, walkone AND the Sovereign Org with nothing created, against a vacuity control where the byte-identical own-Org call returns 201. The clause's 403-on-read leg was corrected to the ADR-0013 adjudication in the same PR. Earlier entry kept for legibility. ~~The transport reading stands (the Org-scoped tool surface lives only in the stdio subprocess inside the Agenity pod; `mcp.<sovereign>` verifies against the sovereign handover key), and so does the anti-vacuity warning: **an unauthenticated call returns `tools:[]` identically to a valid Org bearer, so NO verdict may be read off that endpoint.** But the clause's cross-Org `get_application` → **403** leg is now ADJUDICATED in **ADR-0013** (PR #6124) and **#6122**: answering **not-found** is the CORRECT denial shape, because a 403 there would confirm the Application exists in another Organization. The 403 belongs to the WRITE path (`create_application`, `-32003`), where the caller named the Organization themselves. The clause was corrected, not the code | clause corrected (ADR-0013) + walk (needs pod exec) |
| 228 | `janitorDestructive()` (`products/catalyst/bootstrap/api/internal/handler/janitor.go:135`) reads `CATALYST_JANITOR_DESTRUCTIVE`, which appears in ZERO of the 82 env entries on the mothership `catalyst/catalyst-api` Deployment (62 of those 82 begin `CATALYST_` — the control that the scan is not blind). The clause's "janitor log shows the orphaned VPC(s) swept" half cannot be satisfied in the configuration the control plane ships | never written + needs a wipe→re-prov cycle |
| 232 | **CAUSE SUPERSEDED 2026-08-11 by the re-measurement in PR #6128.** ~~`platform/openclaw/chart/values.yaml:287` ships `httpRoute.enabled: false` + an `oidc.issuerURL` placeholder and the sovereign-admin install door passes no parameters, so no route is created and traffic would 503 on the placeholder issuer~~ — that reading blamed the install door, and the door is **not** the defect: the per-Org GitOps overlay DOES supply the route and a real per-Org issuer, correctly. **The measured cause is `plan-quota` STARVATION, and the arithmetic names ONE line.** `platform/newapi/chart/values.yaml:153` gives the bp-newapi app container `limits.cpu: 2` against `requests.cpu: 100m` (`:150`) — and the S plan's ENTIRE quota is `CPU: "2"` (`core/controllers/organization/internal/gitops/manifests.go:121`, rendered as `hard.limits.cpu` at `:499`). A ResourceQuota counts LIMITS, so **one bp-newapi container claims 100% of the smallest plan's cap** before openclaw's own 250m (`platform/openclaw/chart/values.yaml:79-85`) or its CNPG's 500m are asked for. It became a fresh-Org regression in two merged steps: `70f6b07aa` (#5969) added `"openclaw": {"newapi"}` to `impliedHelmReleaseApps` (`helmrelease_apps.go:113`), then `93d824ea4` (#5987) made that HR installable — before those, the 2000m never landed in an Org namespace. The quota rejection surfaces to a User only as an opaque Helm `context deadline exceeded`. **Correction to an earlier draft of this row: do NOT cite a `maxLimitRequestRatio` violation** — #4758 REMOVED that ratio from the vcluster-Org host-namespace LimitRange (`manifests.go:530-543`) precisely because the vcluster syncer's own pods can never satisfy it | not an install-door gap — **never-written resource sizing**. **FIXED 2026-08-11 (this PR)** — `newapi.resources` pinned to `requests.cpu == limits.cpu == 500m` at BOTH producers, `generateNewAPIHR` (`helmrelease_apps.go`, beside the `valkey:` block) and `orgTenantBPNewAPI` (`organization_gitops.go:1442`), enrolled together so the doors cannot drift. Go-only, no chart bump — the 2-CPU default stays correct for the slot-80 Sovereign install, which runs in no ResourceQuota. Raising the S cap was the WRONG lever: it sells a cap the customer did not buy. Guards red-first at each producer + a vacuity check that runs the parser against the CHART DEFAULT and asserts it is rejected. **Now DEPLOY-GATED then walk** |
| 237 | **THIS ENTRY WAS WRONG AND IS RETRACTED 2026-08-11.** It read *"the `spines` CRD is not served … missing producer, verified"* — which is the exact mis-aimed-probe reading `#6111` forbids, promoted to a verified finding. `kubectl get spines` answering `the server doesn't have a resource type` is correct output for a probe at an object that **has never existed**: `git log --all -S"kind: Spine"` returns ZERO commits in the repo's whole history, and `products/catalyst/chart/crds/` ships THIRTEEN CRDs, none of them Spine. A missing producer must be proven by finding no writer, not by a `get` that names the wrong kind. **The spine is a SET of `Application` CRs** (#4212), those CRs are live and Ready, and the back-pointer has a real writer with the row's own ticket number at the edit site: `core/controllers/application/internal/controller/continuum.go:318`, `reconcileContinuumCR` — *"write the `status.continuumRef` back-pointer (#4416 — the consumer-READ that closes the #4212 round-trip)"*. **The real gate is the latched-`failed` deployment record (#6082)** — the same gate as G1, not a CRD gap | **NOT a missing producer** — clause adjudicated (#6111); gated on #6082 with G1 |
| G2 | **THE CLAUSE IS REFUTED, NOT THE PLATFORM — corrected 2026-08-11.** The measurement (zero ExternalSecrets in a funnel Org ns whose `bp-newapi` installed cleanly at 1.4.153) is accurate, and that is the **intended, correct** state: it is what TWO merged commits deliberately produce. `core/services/provisioning/gitops/helmrelease_apps.go:528` (funnel) and `products/catalyst/bootstrap/api/internal/handler/organization_gitops.go:1570` (BSS door) both set `catalystIntegration.enabled: false`, and the comment at `helmrelease_apps.go:512-527` gives the reason: the chart's companion **PushSecret** (default-on, `updatePolicy: Replace`) would overwrite the SOVEREIGN-shared admin-token path with one Org's own key and **401 per-user key issuance for the whole Sovereign** — *"actively destructive, not merely redundant"*. `organization_gitops.go:1530-1569` records the live pre-fix evidence from this very namespace (`g7doora/…-push Errored, 403 permission denied`, ES stuck `SecretSyncedError` while the HR still read Ready). Searched before concluding: the ONLY `catalystIntegration: enabled: true` in the tree is the **Sovereign** slot-80 install (`clusters/_template/bootstrap-kit/80-newapi.yaml:480-493`); `catalyst-newapi-admin-token` is Sovereign-singular by construction. So a per-Org ExternalSecret for it is not an unbuilt feature — it is a defect `93d824ea4` (#5987) and `13c6ffedc` (#6074) removed on purpose | **no code owed — the CLAUSE needs re-authoring** (re-scope to the Sovereign ns `newapi`, or re-author around the real per-Org bearer seam). **Genuine adjacent gap found while proving this:** `openclaw-newapi-controller-token` / key `NEWAPI_KEY` (`helmrelease_apps.go:339`, `organization_gitops.go:1867`) has **NO producer anywhere in the repo** — only two consumers and a README. That, not the admin token, is the real per-Org token gap |

### Residue scoreboard

| cluster | rows | four-state |
|---|---:|---|
| A — mothership GitOps loop down | **30** | merged-but-undeployed (mothership) |
| B — per-Org console fan-out, missing pre-cutover producer | 3 | **merged AND deployed AND still failing** |
| C — Org delete cascade | 1 | merged AND deployed → re-walk |
| D — Sovereign-side fixes delivered | 8 | **7** merged AND deployed → re-walk (re-tested ⊂ live `28b532a`) · **W5 moves to A3** (mothership-served wizard) |
| E — Agenity credential + no agent session | 6 | never written (#4277) + unmerged (#5968) |
| F — Jobs feed / Jobs re-run | 2 | **BOTH re-walk** — 172 (#6053 ⊂ live) · 176 (#6078 ⊂ live) |
| G — verdict from absent evidence | 2 | never written |
| H — nine independent causes | 9 | **re-audited 2026-08-11 — 7-never-written was wrong.** 2 re-walk (218, 232-adjacent) · 1 merged-but-undeployed (109) · 1 **merged AND deployed AND still failing** (16) · 3 clause corrections (223, G2, 237) · 1 decision (19, PR #6124) · 1 owned elsewhere (228, PR #6137) |
| **total** | **61** | |

**What moves the number fastest — re-stated after the D/F/H re-audit.** Cluster A
is still 30 rows behind a single infrastructure repair with zero code. But the
"browser seat" pile is BIGGER than this table said: D contributes 7 (not 8),
F contributes **2** (176 was mis-filed as the fourth state), and H contributes
218 — plus 109 the moment `bp-keycloak` reaches 1.5.8. **The genuinely new
engineering in D/F/H is exactly TWO rows**, not seven:

* **16** — the only true fourth-state row in this scope. `applications_update.go`
  scalarized an object `spec.placement` on every PUT, silently erasing the
  vCluster pivot at HTTP 200. Fixed here with a red-then-green guard plus a
  string-stays-string control.
* **232** — one line of resource sizing (`newapi` `limits.cpu: 2` = the whole S
  plan). Go-only, no chart bump.

Three more rows need a **clause correction, not a commit** — 223 (ADR-0013),
G2 (the zero-ExternalSecrets state is deliberate and correct) and 237 (no `Spine`
kind has ever existed). Those are worth more than they look: each one currently
reads as a platform defect and is not.

Tracked as a single checklist on the consolidated tracking issue rather than as
nine new issues.

---

## The remaining ❌ set is a DEPLOY, not a fix backlog (re-measured 2026-08-08)

This is the single most decision-relevant fact in this file, and it was not
visible until every failing row was classified at once rather than chased one at
a time.

`scripts/classify-uat-delivery-state.py --image fad88bd` — the catalyst-api
actually running on hw292, **built 2026-08-02** — resolves each row's cited PRs
to their merge commits and asks whether each is an ancestor of that artifact.
Across all three non-green tiers:

| tier | DEPLOY-GATED | CODE-BLOCKED | UNKNOWN | total |
|---|---|---|---|---|
| ❌ | **13** | **0** | 0 | 13 |
| ⚠️ | **28** | 3 | 7 | 38 |
| ☐ | **20** | 0 | 6 | 26 |
| **total** | **61** | **3** | **13** | **77** |

**Sixty-one non-green rows are waiting on a roll**, and for those rows writing
further fixes moves nothing.

> ⚠️ **THE SENTENCE THAT USED TO FOLLOW HERE IS NOW FALSE, and it was the most
> decision-relevant line in the file.** It read: *"Every ❌ row is deploy-gated;
> there is no failing row left that needs code."* That was true of the rows
> classified on 2026-08-08 **morning**, and it stopped being true the same day.
> A live walk of hw292 that afternoon surfaced **five defects on the running
> environment that no roll will fix**, because the code to fix them does not
> exist yet. Anyone reading the old sentence would conclude "just deploy" and be
> wrong.

## The ❌ set, classified against the RUNNING artifact (2026-08-08 late)

`scripts/classify-uat-delivery-state.py --image fad88bd` over all 15 ❌ rows:

| verdict | count | rows |
|---|---:|---|
| **DEPLOY-GATED** | **12** | R17, W1, W2, 37, 57, 86, 90, 92, 94, 115, 219, 234 |
| **CODE-BLOCKED** | **0** | — |
| UNKNOWN → resolved below | 3 | 29, 41, 95 |

**No failing row needs code written for it.** Twelve carry fix PRs already merged
and are waiting on the roll.

### The three UNKNOWNs, resolved from first-hand walks (not left ambiguous)

The classifier reports UNKNOWN when a row cites an ISSUE rather than the PR that
closed it. All three were walked live on 2026-08-08, so they are resolved here
rather than deferred:

| row | verdict | evidence |
|---|---|---|
| **29** | ~~needs code~~ → **fix written and merged (#5909), awaiting walk** *(2026-08-08)* | Silent re-auth *works* — proven by dropping `catalyst_session` while keeping the realm session and landing on `/dashboard` with fresh tokens. The real defect is narrower: when `catalyst:authed=1` is stale relative to the cookie, the stale-marker fast path 401s and PIN-walls **instead of falling back to the silent leg that demonstrably works**. Tracked #5887. A genuine TTL expiry produces exactly that state. **Root cause established 2026-08-08:** `router.tsx:287` (`if (hasCatalystSession()) return`) short-circuits BEFORE line 290's `probeWhoamiAndCacheMarker`, so #5460's 401-revocation is unreachable from the one gate that needs it — the stale marker is self-perpetuating at the only site that could clear it, and the #3374 Layer-B silent-SSO leg is skipped with it. Fixed in **#5909** (merged): the marker now carries a written-at stamp and ages out after 5 min; missing/non-numeric/future stamps all read stale. Fail-safe — worst case is one extra `/whoami`, and the `null` branch already fails open. Source-correct, not walked. |
| **41** | ~~needs code~~ → **DEPLOY-GATED** *(corrected 2026-08-08)* | The finding stands: the sovereign realm holds **two** users — the owner and `emrha.baysal@…`, a two-letter transposition, persisted from a typo at the login form. Only a COUNT finds it (`/emrah/` matches both). **But the fix already exists.** `6663dc441` (2026-08-06) — *"defer pin/issue realm write to pin/verify — no unauthenticated principal creation"* (#5720) — moved the realm write out of `HandlePinIssue` into `HandlePinVerify`, gated on a correct PIN, and ships with the guard `auth_pin_issue_no_unverified_realm_write_5720_test.go`. `auth.go:489-499` cites **this exact row and this exact address** as the finding that prompted it. The image `fad88bd` was built **2026-08-02**, four days earlier, so the fix is simply not in it. Tracked #5883 → resolves on the roll. |
| **95** | ~~needs investigation~~ → **mechanism named, fix merged, awaiting walk** *(2026-08-08, #5910/#5911)* | The purchased app never became an Application. Checkout recorded `Apps (1)`, the Org converged (`state=done`, all six steps, vcluster created), the applications list holds 14 apps and none belongs to the new Org — control: the first Org's apps *are* in that same list. **Traced to the write seam:** `resolveAppSlugs` (`handlers.go:363`) substitutes the raw UUID on a catalog miss, so `len(out) == len(in)` and **the cart count survives by construction** — which is why every count-based check read green. `consumer.go:589` then passes it to `GeneratePerOrgAppsTree` *as a slug*, `GetAppSpec` returns a bare `AppSpec{}`, and the generator emits a Deployment with a **null `image` and `containerPort: 0`**. That is invalid to the apiserver, and per the #4389 note in `helmrelease_apps.go` a rejected manifest fails the **whole** `vcluster/apps` Kustomization — so the blast radius is that app **plus every co-installed app in the same apply**. Fix reports the unresolvable case (the only point where the miss is still observable); pass-through behaviour deliberately unchanged. |

### Where the score actually stands, and what the ⛔ tier is — 2026-08-08

```
total rows        286
  ⛔ excluded       36     STONE denominator excludes these by definition
  N/A excluded       2
STONE denominator  248
STONE green        188  =>  75.8%
raw                188/286 =  65.7%

non-green INSIDE STONE:  ❌14  ⚠️39  ☐2  ◑5  =  60
```

**The ⛔ tier is correctly parked, not neglected.** Classifying all 36 by their
recorded blocker:

| count | theme |
|---:|---|
| **23** | SUPERSEDED / obsolete assertion |
| 7 | other |
| 4 | cutover-dependent |
| 1 | env/scope-blocked (G5) |
| 1 | needs-mutation (G8) |

Since ⛔ is excluded from the STONE denominator, **walking these rows cannot move
the score** — and 23 of them are superseded assertions, which is precisely what ⛔
is for. Re-walking the tier would be motion without progress.

### What the 60 non-green STONE rows actually need

Every one has now been walked or classified against the running artifact:

- **14 ❌** — all walked 2026-08-08. 4 are #5894, 9 are inside the 51-HR cascade,
  **1 (R17) is a live defect and its fix shipped this session (PR #5917)**.
- **39 ⚠️** — 30 deploy-gated on the same cascade, 1 code-blocked (225,
  adjudicated), 7 UNKNOWN all resolved or attributed, 1 walked today.
- **2 ☐ / 5 ◑** — all seven stamped 2026-08-08 with named blockers; none is
  "untriaged".

So the honest statement of remaining work is not "60 rows to fix". It is: **one
credential (#5759), one OOM fix awaiting delivery (#5645), a handful of owner
adjudications, and R17 — which is already fixed.**

### THE NUMBER THAT REFRAMES EVERYTHING — 51 HelmReleases, one credential

Measured read-only on hw292, 2026-08-08, counting every HR whose **Ready**
condition (by type, not index) reads `DependencyNotReady`:

```
51  HelmReleases blocked
     14  flux-system/bp-cert-manager
     11  flux-system/bp-cilium
      7  flux-system/bp-cnpg
      6  flux-system/bp-flux
      2  flux-system/bp-keycloak
```

Five dependency names read like five problems. They are **one**:

```
bp-cilium  Ready=False :: SourceNotReady
  HelmChart 'flux-system/flux-system-bp-cilium' is not ready:
  failed to configure login options:
  no auth config for 'ghcr.io' in the docker-registry Secret 'ghcr-pull'
```

That is **#5759** — the half-reverted cutover left Harbor-only `ghcr-pull` auth
against `ghcr.io` chart URLs, so source-controller cannot pull, the HelmChart never
becomes ready, and everything downstream **fails closed**. The other four
"dependencies" are the next layers of the same cascade.

**This is one layer EARLIER than #5640 records.** #5640 says post-cutover Sovereigns
cannot receive newly published *images*. hw292 cannot resolve the *charts* either —
its GitOps loop is closed at the source. Rows previously attributed to "waiting on a
roll" are in fact waiting on a credential.

**All 14 ❌ rows are now walked (2026-08-08) and every one is accounted for:**

| rows | attribution |
|---|---|
| 37, 86, 90, 94 | #5894 fan-out truncation (catalyst-api OOM, #5645 merged/undeployed) |
| W1, W2, 29, 41, 57, 92, 115, 219, 234 | inside the 51-HR cascade — each verified against its own delivering HelmRelease |
| R17 | **the one live defect.** Fix shipped this session (PR #5917, mutation-tested) |

37 additionally collapses into **#5878**: newapi is the only SSO app with zero
oidc-gate references, so rows 37 and 38 are one defect seen from two directions
(first hit vs re-entry), not two.

**51 is not a backlog of 51 fixes. It is one credential and a fan-out.**

### The ⚠️ tier, classified 2026-08-08 — 30 of 38 are the SAME gate

The 38 unwalked ⚠️ rows were classified against the running artifact. They are not a
second backlog:

| verdict | count | note |
|---|---:|---|
| DEPLOY-GATED | **30** | same #5640 gate as the ❌ set |
| CODE-BLOCKED | 1 | 225 — adjudicated, `bp-newapi` is `allowedPlacements: [host, mgmt]`, a per-Org newapi is not a supported configuration |
| UNKNOWN | 7 | all resolved or attributed below |

**The seven UNKNOWNs, closed out:**

- **95** — mechanism named + fix merged (#5910/#5911).
- **184** — no assertion was ever authored (literal `—`); owner adjudication, tracked **#5867**.
- **5** — asserts `TIER=sme`, and `organization.yaml:120` declares `enum: [org, corporate]`, so the CRD **422-rejects** it. `sme` is also a banned term (GLOSSARY → `Organization`). The assertion is obsolete, not the console. Tracked **#5847**.
- **19** — backing data measured: **17 Applications vs 75 HelmReleases**. A 4.4× gap means counting cards settles it; no data-model reasoning needed.
- **33** — `bao.hw292.omani.works` returns **302** (SSO redirect, not the forbidden token prompt). Rendered-state half needs Playwright.
- **G7** — door B **proven by real creation** (`walk-stranger-two`, `isolation=vcluster`, `state=done`, vcluster StatefulSet 1/1). Door A is exactly **#5857**'s subject and is linked there.
- **165, 177** — both require a **genuinely failed** `cutover-step-*` row to exercise the per-row Re-run. A healthy Sovereign cannot produce one, so these are not walkable on a converged env by construction.

**Net:** across ❌ and ⚠️ together, **51 of 52 non-green rows outside ⛔** trace to the
#5640 delivery gate, an owner adjudication, or an env-lifecycle precondition. The lone
exception is **R17** — a live defect, fix sites named, and its HTTPRoute half already
has a PR in flight at **#5848** (whose scope misses the second orphan, flagged there).

### Every ❌ row walked live on hw292 — 2026-08-08 close-out

All fourteen were probed against the running env, each with a control. The result
is that they resolve to **three** causes, not fourteen:

| cause | rows | evidence |
|---|---|---|
| **#5894 fan-out truncation** (region B has zero per-Org listeners; the catalyst-api OOM cuts `orgConsoleTLSTargets` short) | 37, 86, 90, 94 | 10 fresh connections per host: per-Org hosts exactly **50%** reset, sovereign host **0/10**. Purchased apps return **302** (serving) on every request that completes — the funnel is not broken, half the requests never arrive |
| **deploy-gated, fix merged after the image** | R17\*, W1, W2, 29, 41, 57, 92, 115, 219, 234 | ancestry-checked individually; e.g. row 234's Kyverno §854 denial is fixed in `bp-stalwart-tenant` **0.1.14** (`935d23842`) while hw292 runs **0.1.13** — one version short |
| **empty DR CR layer** (#4986 shape) | 57, G3 | `continuums.dr.openova.io` and `cnpgpairs.dr.openova.io` both **installed with zero CRs** while shared-pg/-b/-c are all 3/3 healthy |

\* **R17 is the exception and the one genuinely-live defect found:** Org delete
leaves two orphans in PLATFORM namespaces, which a namespace-scoped cascade cannot
reach — `catalyst-system httproute/catalyst-ui-<slug>-omani-homes` and
`kube-system secret/org-wildcard-tls-<slug>-omani-homes`, both 4d21h after the Org
was deleted. A future Org re-using that subdomain would collide. Fix sites named.

**The leak is compounding while this sits.** catalyst-api on hw292 measured twice
in one session: `restarts 125 → 132`, working set `1900Mi → 3714Mi` of a 4Gi limit.
Seven more OOMKills in ~5 hours, currently at 93% and climbing. That single
uncorrected leak is the proximate cause of four ❌ rows, and its fix (#5645) has
been merged and published since 2026-08-04 without ever executing.

### So the actual shape of the remaining work

- **13 rows** (the 12 + **41**) — one roll, no code.
- **1 row** (29) — root cause established *and* fix merged (#5909); awaiting walk.
- **1 row** (95) — mechanism named *and* fix merged (#5910/#5911); awaiting walk.
- Everything else non-green is ⚠️/⛔, not ❌.

> **As of 2026-08-08 evening, every ❌ row has a named mechanism and either a merged
> fix or a deploy gate. None is unexplained.** All 15 now resolve to one of three
> states: waiting on the roll (13), or fix-written-awaiting-walk (29, 95). The one
> item still genuinely needing code is **#5878 / row 38** — newapi is the only SSO
> app not fronted by oidc-gate (measured: every other slot carries an oidc-gate
> reference, newapi carries zero), so its `/login?expired=true` is NewAPI's own
> application session, which a realm session does not satisfy. That row is ☐, not ❌.
>
> **Nothing in this list is closeable from source.** Every "fix merged" above is
> source-correct and unwalked, and delivery for all of it is gated behind **#5640**.
> The next state change for this file comes from a fresh prov, not another PR.

> **Row 41 moved buckets on 2026-08-08, and the reason generalises.** It was filed
> "needs code" because the *symptom* was verified first-hand and the *source* never
> was. One `git log --diff-filter=A` on the guard file found `6663dc441` — a fix that
> names this row and this address in its own comment, merged four days before the
> walk that "discovered" the problem. **Ten issues have now resolved this way in one
> session** (#5750, #5752, #5767, #5817, #5823, #5825, #5833, #5835, #5720, and
> #5894's #5511/#5645 chain). At that rate the prior should be inverted: on a
> deploy-gated env, assume a fix exists and prove it does not, rather than the
> reverse. The check costs one command and has been right ten times out of ten.
>
> Method caution from the same hour: `git log --grep="(#5720)"` matched the WRONG
> commit — the parentheses are regex groups, so it silently returned an unrelated
> recent commit and produced a confident, wrong ancestry verdict. Anchor on something
> unforgeable instead — the commit that *added the guard file*
> (`--diff-filter=A -- <path>`) — then compare. Verify the subject before the value.

### Why this section supersedes a prior claim in this file

An earlier revision asserted every ❌ row was deploy-gated and **no failing row
needed code**. That was true of the morning's classification and stopped being
true the same afternoon — five live defects surfaced with no PR to cite, so they
classify as UNKNOWN or deploy-gated by construction. The classifier answers
*"is the fix I know about deployed?"*, never *"is there a fix at all?"*. This
table separates the two by pairing every UNKNOWN with a live walk.

## Live defects found by walking, NOT deploy-gated (2026-08-08 afternoon)

These are new code, not pending deployments. Each was found on hw292 as it runs
today, and each is recorded with the measurement that produced it.

> **One of them was not, and the correction is the point.** #5894 sat in this table
> — and, before that, parked as *needs-design* on the issue — on the strength of a
> real measurement and a **wrong causal story**: I had concluded the org-controller
> holds a single `client.Client` and structurally cannot reach region B. It does not
> build that surface at all; catalyst-api does, and #5511 gave it a per-region seam
> back on 2026-07-30, *inside* the running image. Following it to ground turned a
> "needs a topology decision" into "a merged fix (#5645) that has never executed",
> which is a **completely different kind of work** — nobody has to design anything.
> A correct measurement with an unverified mechanism attached is still a wrong row,
> and it points the next reader at the wrong file. The generalisable check: before
> filing a defect as new code, name the component that actually writes the surface
> and `git log --grep` it — a fix may already exist and be sitting one deploy away.

| issue | what breaks | how it was found |
|---|---|---|
| ~~**#5894**~~ **moved out of this table — it IS deploy-gated (2026-08-08)** | per-Org consoles reset the TLS handshake **~50%** of the time, on both Orgs and both pool TLDs | 12 probes per host across 6 hosts: 4 sovereign-domain hosts **0/48 resets**, 2 pool-TLD hosts **exactly 50%** — isolates it to the pool-TLD cert/listener, one region serving it and one not. **Then followed to ground:** region B *has* `cilium-gateway-console` but **zero per-Org listeners**, while region A has both (`uatco`, `walk-stranger-two`) — so #5511's fan-out reached the Gateway and stopped. Neither guard in `orgConsoleTLSTargets` closes on identity (`SOVEREIGN_FQDN=hw292.omani.works`, both regions configured, `isChroot()` true). The truncation is a **catalyst-api OOMKill**: `restarts=125, exit=137, limits=4Gi, requests=96Mi, in-flight 1900Mi`. `orgConsoleTLSTargets` returns the host region as `targets[0]` and appends secondaries after, so a kill during the long per-region admission poll (`org_console_tls.go:266,437`) completes region A and dies before region B — exactly the observed asymmetry. `api-deployment.yaml:1814-1846` already recorded this from **#5642** four days earlier: the leak is `Factory.AddCluster` rebuilding all 42 informers on byte-identical kubeconfigs, ~62 Mi/min, **fixed in #5645 (MERGED) but never executed** because no published image reaches a `cutoverComplete=true` Sovereign — **#5640** |
| **#5895** | first visit to a per-Org console renders a **blank page**; a reload recovers | cold browser stops at the `prompt=none` silent-auth URL with `body_len=0`, unchanged after 25s |
| **#5882** | OpenBao SSO session lands with no token prompt but its policy **cannot list mounts** — Secrets pane renders `403 permission denied` | authenticated shell renders, content pane errors |
| **#5883** | a **mistyped email** at the PIN form persists a Keycloak realm user (`emrha.` alongside `emrah.`) | counting the Users list — a presence check matches both strings and reports green |
| **#5878** | newapi is the only SSO app that does not accept the realm session — bare URL 302s to `/login?expired=true` | 5 apps land signed-in off one session; newapi expires it |
| **row 95** | a purchased app never becomes an Application — checkout recorded `Apps (1)`, the Org converged, no Application exists | control: the first Org's apps **are** in the same list, so the absence is real |

**Why none of these were visible to the classifier.** It resolves a row's *cited
PRs* against the running image. A defect with **no PR yet** has nothing to cite,
so it classifies as deploy-gated-or-unknown and disappears into the "waiting on a
roll" bucket. The classifier answers "is the fix I know about deployed?" — it
cannot answer "is there a fix at all?". That gap is what walking found.

**Method note worth carrying, since it produced four of the six.** Every one of
these needed either **repeated measurement** (a single probe reports the flapping
console as working) or a **control** (the first Org, the sovereign-domain hosts,
the other Org's apps). A negative with no control is not a finding — today it was
wrong eight times out of eight before a control was added.

### The numbers this section carried until 2026-08-08 were wrong, and understated

The previous text read *"DEPLOY-GATED 12 / CODE-BLOCKED 1 (row 20) … 8 more in
⚠️ … so 20 rows across both tiers are waiting on a roll."* The true figures are
13 / 0 and 61. Three defects in the measuring tool produced that, each fixed and
each the same underlying error — **stating a conclusion the evidence does not
support**:

| defect | effect on this file's numbers |
|---|---|
| #5851 — a row citing the **issue** rather than the PR that closed it printed `CODE-BLOCKED / "no PR cited"` | Row 20 was the "lone exception". Its fix merged as **PR #5688 on 2026-08-05**, three days after the artifact was built. It was deploy-gated all along. |
| #5855 — rows were split on **escaped** pipes, truncating Evidence | Every row carrying a `\|` was classified on a **prefix of its own record**. Row 219 was judged on 2 of its 9 cited PRs. This is what understated ⚠️ as 8 rather than 28. |
| #5859 — `deploy` (109 commits) and `ci` (5) were **unrecognised prefixes**, 28.5% of the last 400 on main | Citations fell into a bucket printed *"cited but unmerged"*, asserting a PR exists that does not. Rows 5/6/10/11 read CODE-BLOCKED purely from this. |

Root cause of the third is structural and worth carrying: **squash-merge replaces
the commit subject with the PR title**, so every commit in a multi-change PR
inherits that PR's overall type. Per-commit subject classification therefore
reflects the PR, not the change.

`CODE-BLOCKED` now requires **positive** evidence — a fix present in the artifact
that still fails. Absence of a citation reports `UNKNOWN`, because git alone
cannot separate "unmerged PR" from "tracking issue" from "fix squashed under a
non-fix title".

### ZERO rows require new code — the three CODE-BLOCKED entries are not engineering

The classifier flags 192, 225 and 231 as CODE-BLOCKED, which is literally true —
their cited fixes are inside the running artifact and the rows still fail. But
**CODE-BLOCKED names a measurement, not a remedy**, and reading each row shows
none of the three is waiting on engineering:

| row | classifier says | what it actually needs |
|---|---|---|
| **192** | fix in artifact, still fails | a **contract decision**. The deep-link half PASSES; the link half is absent because `ConvergenceWizard.tsx` was orphaned from the router by `be5477c43`. Drop the wizard-link clause, or delete the dead component — both change what the row tests. Guarded + named by #5831 rather than left silent. |
| **225** | fix in artifact, still fails | a **precondition**. The #4278 mechanism is proven green (SA exists, hook Completes 1/1 in 5s, HR Ready on bp-newapi@1.4.146, CNPG 2/2). hw292 simply has no per-Org bp-newapi — uatco bought wordpress + openclaw + stalwart-mail + agenity. Needs an Org that BUYS newapi: a funnel mutation. |
| **231** | fix in artifact, still fails | **nothing — its reason is stale.** Walk column `repo-render-2026-08-02`; evidence reads "none exists (hw291 wiped, hw292 **unfired**; catalyst ns 0 pods)". hw292 fired 2026-08-03T04:04Z and hit cc=true at 08:12:30Z, ~14h later. Rows 49/50 prove bp-postgres instances exist there with `singleton` as the pre-selected default. Walkable now. |

**So the honest statement is stronger than "61 deploy-gated, 3 code-blocked":
no row in this 286-row ledger requires code to be written.** Every non-green row
is waiting on a roll, a walk, a funnel mutation, or an owner's adjudication.

Row 5 is a case the delivery axis cannot express at all: it reads DEPLOY-GATED
because one clause cites a merged-but-undelivered fix, while a **different**
clause (`TIER=sme`) asserts a value the Organization CRD 422-rejects and never can
satisfy. A row can be simultaneously deploy-gated and assertion-defective; this
tool reports only the delivery dimension. See #5847.

**Correcting this file's own text from earlier the same day.** The paragraph here
previously read "Three ⚠️ rows (192, 225, 231) carry positive code-blocked
evidence" and stopped. That was accurate about the measurement and misleading
about the work — exactly the failure mode the three classifier defects above
produced, repeated one level up: taking a tool's label as a conclusion instead of
reading what the rows say.

**What this changes.** The next action for the non-green set is delivering the
train — not another fix wave. Four separate investigations in one session (R17's
cross-region reaper, W1/W2's wizard defaults, rows 35/115's bp-guacamole
`crossRegion`, row 92's 429 notice) each independently terminated in "the fix is
merged and the running artifact predates it". That is the same finding four
times, which the classifier now surfaces in one command.

**Three traps the script encodes**, all of which produced wrong answers by hand
before being caught:

0. **Do not read the running artifact off `GET /api/v1/version`'s `buildTime`.**
   That field silently falls back to PROCESS START time when the build-time
   ldflag and `CATALYST_BUILD_TIME` are both unset, keeping the same key name —
   so a pod restart reads as a fresh build and flips every DEPLOY-GATED row to a
   false CODE-BLOCKED. `buildTimeSource` (#5821) now names the branch.

1. A cited PR is often a `docs(uat)` **walk record**, not a fix — #5615 and
   #5617 are the walks that recorded rows 219/234 failing. Counting those as
   fixes turns a code-blocked row into a false deploy-gate, so the commit
   *subject* is classified, not merely its presence.
2. The row's verdict is the **6th pipe-delimited cell**, not "does the line
   contain ❌". Several ✅ rows cite ❌ in their evidence prose; matching the
   whole line over-counted failures (W3 is the example — status ✅, prose
   contains ❌).

The script **refuses to guess the running artifact** and exits non-zero without
`--image`/`UAT_IMAGE`, because a wrong artifact inverts every verdict — the same
wrong-subject failure mode as
`feedback_never_declare_an_env_dead_without_probing_its_own_record_fqdn`.

---

## Every non-green row now states its next action (2026-08-08)

With the classifier trustworthy, the 13 UNKNOWN rows were read individually
rather than left as "the tool cannot tell". **None is code-blocked.** What the
whole remaining set actually needs:

| what it needs | rows | count |
|---|---|---|
| **the roll** (fix merged, artifact predates it) | ❌ 13 · ⚠️ 30 · ☐ 20 | **63** |
| **a walk** — precondition now satisfied | 6, 10, 11 | 3 |
| **a funnel mutation** — 2nd voucher + 2nd Org | 93, 94, 95 | 3 |
| **engineering** — the per-Org bp-newapi HR cannot render (#5987) | 225, G2 | 2 |
| **one click** (a write; scheduling, not permission) | 177 | 1 |
| **three login attempts** | 235 | 1 |
| **fault injection** — needs a genuinely failed cutover step | 165 | 1 |
| **an owner adjudication** | 5, 19, 192 | 3 |
| **a walk** — clause authored 2026-08-10, never walked | 184, 241 | 2 |

The adjudications, stated so they can be answered without re-deriving them:

- **row 5** — demands `TIER=sme`, which the CRD 422-rejects (`enum: [org,
  corporate]`), *and* `ISOLATION=vcluster` without the #4292 plan qualifier its
  siblings 10/11 received. Two clauses, neither satisfiable by a correct
  platform. #5847.
- **row 19** (#5867) — **SETTLED 2026-08-11, the clause was amended.** The premise
  ("the grid renders only blueprint SLOTS") did not survive a full read of
  `AppsPage.tsx`: it renders TWO collections, instance cards one-per-Application-CR
  and a catalog remainder that is suppressed for any Blueprint already holding an
  instance. Both halves have been contract-locked since #6056. So the grid was
  never mis-keyed — a walker counting EVERY card in the grid was counting the
  estate and the storefront together. The clause now names
  `[data-card-kind="instance"]` as the set to count, which keeps the original
  anti-HelmRelease intent and makes the row settleable by counting.
- **row 184** — ANSWERED 2026-08-10, no longer an adjudication. It had no
  assertion at all; the slot was retired and replaced one-for-one, so the
  denominator never moved, and the swap is in `uat-retirements.csv`. It now
  asserts the ledger invariant nothing checks: 286 slots, row-ID bijection with
  the canon, clause text identical in both files, every retirement recorded.
  Reset to ☐ — a new clause has never been walked. (The earlier note here said
  "row 186 is already N/A"; that was stale — 186 was itself retired, re-authored
  and then walked green on the replacement.)
- **row 241** — ANSWERED 2026-08-10, same treatment. Its clause asserted a gate
  that #5253 deliberately removed; `phase1_watch.go:1277` writes `ready`
  unconditionally and a probe failure rides the non-fatal `ConsoleDegraded`
  surface instead. Re-authored to assert that the record and the live front door
  AGREE, falsifiable in both directions. Reset to ☐, unwalked.
- **row 225 and G2** — NOT a funnel mutation, which was the previous reading
  here. A per-Org `bp-newapi` IS supported: the funnel path emits the HR with
  `namespace`/`targetNamespace` set to the Org slug and never consults the
  blueprint's `mgmt` placement. What stops it is #5987 — the funnel HR passes no
  `catalystIntegration` values, so `external-secret.yaml:42` fails the render on
  an empty `remoteRef.key`. Proven by `helm template` (exit 1) against a control
  that differs only in that block (exit 0). Both rows stay red until it lands.
- **row 192** — `ConvergenceWizard.tsx` owns the asserted testid and was orphaned
  from the router by `be5477c43`. Drop the clause or delete the component. The
  orphan is now loud rather than silent (#5831).

**Two rows in this table now wait on engineering** — 225 and G2, via #5987,
found by rendering the chart rather than by re-reading the ledger. The line here
previously read "nothing in this table is waiting on engineering", and that was
true only because the defect underneath those two rows had been recorded as a
missing precondition ("an Org that BUYS newapi") instead of measured. The single
largest lever remains the roll: 63 rows move on it, and none of the other
categories exceeds four.

Re-measured after each stamp rather than carried forward — R22 moved UNKNOWN →
DEPLOY-GATED once it cited its own fix PR (#5813), which is why this figure is 63
and not the 62 an earlier draft of this section carried.

---

## Where the ledger actually stands

The ledger was reset for this env — `scripts/reset-uat.py hw292` flushed 135 hw291 evidence cells to ☐/⏳ on 2026-07-31, per the founder's each-new-env-flushes-all-evidence law — and has been re-walked upward from there ever since. It is **no longer** near-zero, so the "a raw tally reads near-zero by design" note that stood here from 2026-07-31 has been retired rather than left to mislead.

**Live tally, `scripts/uat-tally.py` on this commit (2026-08-06): 156 ✅ / 286 data rows = 54.5%** — ✅ 156 · ⚠️ 47 · ◑ 5 · ❌ 19 · ☐ 12 · ⛔ 45 · N/A 2. Read the verdict from the status column only; the tally script does that and a whole-line glyph search does not (it over-reported by 23 rows when last measured, always optimistically).

The **denominator honesty** matters as much as the numerator: 45 rows are ⛔ — assertions superseded by a merged, founder-approved design decision — and they can never go green as written. The largest single block is the 11-row placement family (rows 98–108), voided by #4325's deliberate de-vcluster of the mgmt/rtz/dmz planes. Those are not product faults and must not be counted as gaps; equally, they are not passes. Until the founder rules on rewrite-vs-exclude (see "Not achievable as written" below), 54.5% is the honest raw figure and 156/241 = 64.7% is the honest figure with the ⛔ set excluded from the denominator. Quote whichever, but say which.

The last **walked** tally, on hw291 before the wipe, was **135 ✅ / 281 = 48.0% raw** (`dc18c6d9a`). The last walked north-star remains hw288 at **214 ✅ / 281 = 82.0%** (2026-07-26). Durable structural state — the number that survives a flush — stands at the last evidence-backed value; see the completion matrix, and change it only on walk evidence.

---

## The delivery gate is the whole story this cycle

Every fix listed below is **merged and inside the image hw292 provisions with**. That claim is not an assumption — it is checked mechanically. The catalyst-api / catalyst-ui image pin on `main` is commit `fb41faf`, and a fix is delivered iff:

    git merge-base --is-ancestor <fix-sha> fb41faf

| issue | fix | delivered on hw292 | verification done pre-walk |
|---|---|---|---|
| #5515 derivePattern fails open into `singleton` | PR #5519 → `796e587b2` | ✅ ancestor | `placement.ts:107-108` returns `PATTERN_NOT_REPORTED` when `primaries === 0`, making the `singleton` return unreachable without a Primary; `placement.test.ts` 21/21 incl. a dedicated #5515 block pinning **both** directions |
| #5489 four surfaces report a vCluster that does not exist | PRs #5526 `8523ecf33`, #5490 `54614d3b3`, #5524 `908f76b0e` | ✅ all three ancestors | root fix at `organization_controller.go:1110` — `vclusterStatusFor` keys off the **same** `gitops.BoundaryIsVcluster` predicate that decides authorship, so status cannot disagree with reality; 10 tests green across handler + UI + controller |
| #5477 `replicationLagSeconds: 0` published as a measurement that was never taken | PR #5478 → `c9b2f9f4c` | ✅ — pinned as the controller image itself, `products/continuum/chart/values.yaml:41 tag: "c9b2f9f"` | code present; live re-check of the four Continuum CRs rides the walk |
| #5485 defects 1–3 (reconciler log prefix, showback Job rows, treemap ReplicaSet names) | `a854cb7ae`, `6844b38f6` | ✅ ancestors | defect 1 unit-proven; defects 2+3 **live-verified on hw291 before the wipe** (authed owner API: one platform row, zero itemized Job rows, zero hash names, cost conserves exactly) |
| #5531 gitea OAuth state lost on pod roll | PR #5533 → `fb41faf30` | ✅ (it *is* the tip) | `[session] PROVIDER = db`; bp-gitea **1.2.49 published + signed** after the publish blocker below was fixed |

**Publish blocker found and fixed this cycle:** bp-gitea 1.2.49 silently failed to publish because `httproute-render.sh` piped `echo` into `grep -q` under `pipefail` — `grep -q` closes the pipe on first match, `echo` dies with SIGPIPE 141, and the pipeline reported FAIL **on a passing value**. Herestrings removed the pipe (`bf59fb5f0`); blueprint-release green, 1.2.49 on ghcr. Any chart whose tests use that idiom is exposed to the same false failure.

**Formerly held pending hw292 readiness** (a deploybot roll mid-prov abandons the provisioning — prov-preflight gate 5). hw292 reported ready and the freeze thawed; **both merged**:

| PR | issue | state |
|---|---|---|
| #5534 | #5485 defects 4–6 (suspended HR shows healthy, `?status=` ignored, `/fleet/applications` duplicates) | **MERGED** |
| #5535 | #5488 cutover pre-flight aborts on a recoverable condition | **MERGED** |

Both are in `main` and inside published images — but read Gate 5 below before treating that as delivered to hw292, which it is not.

---

## Gate 1 — Pillar 5 (cutover): mechanism proven, hardening delivered

hw291 reached **`cutoverComplete=true` 2026-07-30T11:03:43Z**, all 11 steps, 600s deny-egress hold green in both regions. G11/166 banked. The gate is no longer "does it work" but "does it survive the awkward cases":

- **#5488** — after a catalyst-api restart (`strategy: Recreate`), the secondary-kubeconfigs pre-flight aborted with `expects 1 secondary region(s) but only 0 kubeconfig(s) are readable (missing/unreadable: )` while the Secret was already correct. Root cause runs deeper than the issue described: `chrootEnsureDeployment` minted a record with an **empty `ID`** under map key `""` with no guard, reachable from ~40 endpoints via unchecked URL params, and `chrootServesDeployment` discriminates on FQDN alone — so `sync.Map.Range` nondeterministically served the poisoned record. Fixed at mint, at resolution, and by accepting an already-materialized Secret. The #5359 fail-loud contract is **unweakened**: genuinely-missing data still aborts before step 1. PR #5535, held.
- **#5391 / #5393** — a per-Org customer app's HelmRelease can still gate the platform's cutover, and plan-s quota misses `bp-keycloak` by 50m CPU / 64Mi (one oidc-gate sidecar). **Founder decision, not an engineering call.**

## Gate 2 — founder-gated credential (#4277)

Rows 219, 220, 222 and G8/G9 need the Anthropic credential; `seedAnthropicToken` loud-skips without it. **No engineering work clears these.** The one genuinely external dependency in the ledger.

## Gate 3 — filed defects with named owners

| rows | defect | state |
|---|---|---|
| 110, 112, 114, 115 | #5389 per-app Open/launch does not land in the app | **root-caused 2026-08-06** (see below); fix in flight. Rows 110/112/114 have since walked ✅ on hw292 — those are *platform* apps on `<app>.<SovereignFQDN>`, which is the one shape that resolves. Row 115 is still ❌ but for an unrelated pair of gates (PIN wall #5642 + zero seeded guacamole connections, #5598), not for the hostname defect |
| 35, R9 | #5358 guacamole blank page after the SSO round-trip completes | **root cause CONFIRMED 2026-08-06 on hw292, read-only kubectl both regions** (#5750): `bp-guacamole` HR pinned `0.2.33` in BOTH regions (`main` is `0.2.37`) — 3 versions behind the merged 0.2.36 crossRegion-singleton fix. Live pods show both regions independently running a full webapp+guacd+CNPG stack, and live logs show region-A and region-B each independently authenticating the SAME principal 9s apart — the exact #5358 per-Pod `HashTokenSessionMap` split. Chart fix is correct + complete on `main` (`crossregion-render.sh` 5/5 PASS); hw292 is stuck pre-fix by the SAME `mirrorResync.enabled: false` severance + #5640 version-skew catch-22 as the 5b image-leg case above (cutover chart frozen at `0.1.159`, the CATALOG leg needed to unfreeze it ships at `0.1.170`). New diagnostic `scripts/check-guacamole-crossregion-drift.sh` deterministically classifies this shape (live-verified: KNOWN-DRIFT) so a future walker doesn't need hours of archaeology to reach the same conclusion. Row 35 stays ❌ — no live delivery landed this pass; re-verifies once hw292 (or #5640) closes the gap ‖ **RE-CONFIRMED INDEPENDENTLY 2026-08-10 by a stronger method, and TWO competing hypotheses are now REFUTED.** Read-only throughout (both regions' Tomcat access logs + in-Pod `curl` against `localhost:8080`; no cluster writes). **(a) The 403 is proven to be an UNKNOWN-TOKEN 403, not a permission verdict** — a deliberately bogus token returns byte-identical `403` with a 192-byte `PERMISSION_DENIED` body, and every failing line in both regions' logs is exactly `403 192`. **(b) The split is visible inside ONE page load, in the SAME second, across BOTH logs** — at 10:48:49 region-A logs `POST /api/tokens` 200 then `connectionGroups/ROOT/tree` 200/200, while region-B logs that same load's two `self/effectivePermissions` calls as 403/403; at 10:53:11 the roles reverse (region-B mints, region-A 403s). **(c) REFUTED — the "tree 200 vs effectivePermissions 403 endpoint asymmetry."** At 10:49:07 region-A returned 403 on `connectionGroups/ROOT/tree` too. The apparent asymmetry is a sampling artifact of comparing two different moments; there is no per-endpoint behaviour, only whichever call lands on the Pod that did not mint the token. **(d) REFUTED — `extension-priority: header, postgresql` as the cause.** With a VALID token, `postgresql/self/effectivePermissions` returns 200 carrying `ADMINISTER` plus all five `CREATE_*`, so the header→postgresql composition resolves the `sovereign-admins` grant correctly; requests against the `header` data source return 404 `Session not associated with authentication provider "header"`, never 403. The extension config AND the DB are both CORRECT — do not spend another pass there. **(e) Reconciles the 2026-08-08 ✅ against the 2026-08-10 ❌ on this row**: the split is PROBABILISTIC per page load, so one green landing is never evidence the defect is gone. **(f) A SECOND latent defect rides the same delivery** — 0.2.33 also predates **0.2.34**'s `api-session-timeout` fix (#5628): the live webapp logs `Sessions will expire after 60 minutes of inactivity` and the generated `guacamole.properties` carries no `api-session-timeout` key at all, so `API_SESSION_TIMEOUT` is inert and any >60-min revisit 403s even after the singleton lands. **(g) Guard vacuity independently verified**: today's `crossregion-render.sh` run against `2211d046c^` (0.2.35) renders **2 Deployments + a CNPG `Cluster` + the seed Job + the enroll CronJob** for `role=secondary`, while 0.2.37 renders Service + 2 NetworkPolicy + CNP only — the load-bearing Case 3 genuinely fails on the unfixed chart. **(h) Row 115 rides this delivery for its PRECONDITION only** — the list cannot render at all while the session splits — but 115's own clause (non-empty list) stays **NEEDS-CODE**: nothing in the repo creates a `guacamole_connection` row (re-verified from source 2026-08-10; live DB `guacamole_connection` = 0, `guacamole_connection_group` = 0). Delivering ≥0.2.36 makes row 35 walkable; it does NOT turn 115 green |
| G12 | #5388 region-kill failback left a data split-brain | first fix merged (cnpg-pair 0.2.23 surfaces peer-probe starvation); re-proof rides hw292 |
| R17 | #5364 org-delete leaves a half-teardown | filed, reopened on runtime evidence |
| 212, 213 | per-Org MCP — #5516 proved Org-scoped reads never worked on any env; fix PR #5522 routes to the own-org seam | **both WALKED on hw293 2026-08-11 — the rows were walk-gated, not code-gated.** No per-Org MCP instance had ever been installed on any env (`kubectl get applications.apps.openova.io -A` matched openova-mcp 0 times), so neither clause had ever been reachable. Two were installed through the shipped org-scoped install API (`POST /api/v1/org/applications` → `stampOpenovaMCPOrgParameters`), and **212 flips ✅** — an Org bearer lists exactly its own Org's estate, with the Sovereign-context listing as the positive control that the omitted apps exist. **213 stays ❌ on a much narrower point:** #5522 also moved the cross-Org read off `ErrForbidden` onto a not-found answer, so `get_application` for another Org's app returns `-32000 not found`, not the **403** the clause asserts (the write path still returns `-32003`/403, so the build can emit one). Contract adjudication filed as **#6122**. #5843 and #6067 were both refuted by this walk |
| — | #5385 deployment health aggregate reads stale-degraded (trust kubectl, not the badge) | filed, affects walk trust |

### #5389 root cause — the endpoint hostname vocabulary cannot express a per-Org app host

Found 2026-08-06, measured live on hw292 (dep `1c56518035a83e03`, `cutoverComplete=true`). The earlier row-114 fix (`bp-newapi` declaring `endpoints: []`) was correct but addressed only a *missing declaration*. The residual defect is deeper: for a per-Org app the declared hostname **evaluates to a host that does not exist**, so the launch button is not mis-styled — it points nowhere.

**Per-Org apps are served on the tenant POOL domain.** Every per-Org hostname actually served on hw292:

```
agenity.uatco.omani.homes
console.uatco.omani.homes
console.r17probe.omani.homes
wordpress.uatco.omani.homes
```

Across all 21 live `HTTPRoute`s in both regions, **zero** hostnames match `<app>.<org>.hw292.omani.works`. Platform apps sit on `<app>.hw292.omani.works` (`gitea.`, `grafana.`, `registry.`, `auth.`, `bao.`, `newapi.`, …) and resolve fine — which is why rows 110/112/114 pass and the per-Org rows do not.

**But every per-Org blueprint declares the SovereignFQDN shape.** 30 `hostnameTemplate` declarations across 26 blueprints use `<app>.{{.OrgSlug}}.{{.SovereignFQDN}}` — including the two apps that are actually live per-Org on hw292:

- `products/agenity/blueprint.yaml:175` → `agenity.{{.OrgSlug}}.{{.SovereignFQDN}}` → evaluates to `agenity.uatco.hw292.omani.works`; **served host is `agenity.uatco.omani.homes`**
- `platform/wordpress-tenant/blueprint.yaml:278` → `{{.AppName}}.{{.OrgSlug}}.{{.SovereignFQDN}}` → evaluates to `wordpress.uatco.hw292.omani.works`; **served host is `wordpress.uatco.omani.homes`**

plus `neo4j` (x2), `llm-gateway`, `librechat`, `opensearch` (x2), `stalwart-tenant` (x3), `valkey`, `milvus`, `matrix`, `temporal`, `langfuse`, `livekit`, `openmeter`, `flink`, `stunner`, `litmus`, `clickhouse`, `ferretdb`, `iceberg`, `kserve`, `strimzi`, `vllm`, `bge`, `anthropic-adapter`.

**No blueprint can currently be written correctly, because the vocabulary has no token for the pool domain.** `evaluateHostnameTemplate` (`products/catalyst/bootstrap/api/internal/handler/endpoint_handler.go:1889`) substitutes exactly three tokens — `{SovereignFQDN}`, `{OrgSlug}`, `{AppName}` (plus their `{{.X}}` aliases) — and both call sites pass the Sovereign FQDN and never the Org's pool domain:

- `:713` — `evaluateHostnameTemplate(ep.HostnameTemplate, h.endpointSovereignFQDN(), org, appName)` (silent-SSO launch URL)
- `:1816` — same three arguments, over `bp.Endpoints` (resolved-endpoint list)

So the per-Org host is not merely mis-templated in 26 files; it is **inexpressible**. Fixing the blueprints alone cannot work — the resolver needs an Org-pool-domain token and the callers need to supply it.

**It also fails open, which is why this reached a walk instead of a 500.** `strings.NewReplacer` leaves an unrecognised token untouched, the result is then `strings.ToLower`-ed, and `buildLaunchURL` emits it as a URL regardless. An unresolvable template therefore produces a confident-looking dead link rather than an error — the same failure-open shape called out in the render-guard and fail-open-CI lessons.

**Separately, `bp-openclaw` reproduces the row-114 dark-button defect exactly.** `platform/openclaw/blueprint.yaml:25` is `visibility: listed`, the chart ships `platform/openclaw/chart/templates/httproute.yaml`, and the blueprint declares **no `endpoints` key at all** (zero occurrences) — a listed, route-bearing app with nothing for the console to render a launch control from.

Full write-up on **#5389**; fix in flight. Do not stamp the per-Org launch rows green on a link that renders — check that the host it points at is one the Sovereign actually serves.

## Gate 4 — the ⚠️ partials

Still the largest actionable group and still the highest-leverage triage target. Two were resolved by adjudication rather than code this cycle, which is the pattern to repeat where the assertion — not the platform — is what is stale:

- **row 22** (showback platform-overhead roll-up) → ✅. Tenant-ns Job pods routing to `__platform__` is the *deliberate, test-encoded* contract of #5493 (`org_consumption_test.go:186`, `:35`, `:63`) and matches the row's own wording ("holding all control-plane/Job workloads"). The predecessor-env symptom — a *named* tenant app inside the platform app list — is structurally unrenderable after the one-shot fold.
- **row 55** (topology single value) stays ⚠️ honestly: all three topology clauses pass, and the residual is a **region** field, not a topology value — host-native singletons carry `primaryRegion: platform-bootstrap-owned-host` because the data layer emits the bootstrap host label as a region. The read-side fix (`b41c93b3c`) is delivered; the data-layer emit is the open half.

## Not achievable as written (⛔)

Placement rows **98–108** are superseded by the #4325 de-vcluster reclassification (founder verdict); row 109 is also ⛔ but for an unrelated reason (the KC account-console REST 401 is an accepted consequence of passwordless-PIN, #688 — its #3642 link was a mis-link and is corrected in the ledger). Plus R1/M1/G5 (janitor), R19 (sandbox — concept removed 2026-06-30), R20 (delivery), 94/95 (funnel). For the ledger to reach 100% these need either rewriting to match the shipped design or formal exclusion from the denominator. **Founder call.**

The 98–108 supersession is **re-anchored to live state 2026-08-06**, not resting on the merged PR alone — this family was re-flipped ❌ once before by a walker who re-asserted the dead expectation (corrected in `d8761bf2b`), so every one of the 11 rows now carries a dated hw292 live re-confirmation and `env=hw292` in place of its wiped-env stamp. Measured on dep `1c56518035a83e03`: across 53 namespaces the only vcluster-related one is `vcluster-system` (the controller ns, itself holding zero workloads), no `mgmt` / `rtz` / `dmz` namespace exists, and all 7 named platform apps run in host namespaces with live pods (`gitea`, `grafana`, `guacamole`, `harbor`, `keycloak`, `newapi`, `openbao`). Row 107 in particular asserts the exact **opposite** of the correct post-#4325 design — "none of the 7 named apps appear under `host`" — when host is precisely where they now correctly belong. **This is verdict classification against a merged decision plus live state; no walk is claimed by any of these 11 rows.**

## §854 disposition — closed, and re-proven this cycle

`scripts/check-no-nodeports.sh` on `main`, 2026-07-31: **zero NodePorts across sources + rendered charts**, every chart rendered. The guard is itself vacuity-tested (#5518, merged): it self-checks that its pattern matches 4/4 banned forms, rejects 2/2 allowed forms, and scans ≥200 files before trusting a pass — so a green result can no longer mean "the guard matched nothing". A raw `grep NodePort` across the repo returns hundreds of hits; every one inspected is the ban's own prose — comments explaining cilium's internal BPF-masquerade requirement, the documented removal of the #4691 fallback, and the guard workflow itself. **Raw grep is not the audit; the render-and-adjudicate guard is.**

**Correction, 2026-08-04 — the enforcement half of that claim held in only ONE region.** This file previously stated flatly that Kyverno `forbid-nodeport-service` "runs Enforce with a fail-closed webhook, made tamper-evident by #5386". Measured live on hw292:

| region | HR `values.compliancePolicies` | ClusterPolicies at Enforce | `forbid-nodeport-service` |
|---|---|---|---|
| region-a | `{"bootstrapMode": false}` | 9 of 25 | **Enforce** |
| region-b | `{}` — never patched | 1 of 25 | **Audit only** |

So on a 2-region Sovereign the NodePort ban was *advisory* in half the fleet: region-b would record a NodePort Service, not block it. The §854 literal scan still passes in both regions (0 NodePorts; 176 svc region-a / 159 region-b **as counted on 2026-08-04** — region-a has since grown to 192, see the 2026-08-06 re-measurement below), so nothing was violating it — the gap is that region-b would not have *stopped* one. Root cause is **#5591**: the Wave 5.90 phase-2b `bootstrapMode` flip reached only the primary region. The source fix is merged (`bb8ceec71` #5592, compile-repaired `ef2d59767` #5619), unit-tested (`TestPolicyEnforceFlip_FlipsEveryRegion_5591`), and delivered in published images — but hw292's phase-2b ran *before* delivery, so its region-b remains unflipped until remediated.

The lesson generalizes past this instance and matches the per-region split class: a security posture verified in one region is not a fleet property. Assert it per-region or do not assert it.

**Update, 2026-08-06 — the gap is now CAUGHT BY A GUARD, not merely described here.** The correction above stated the split but left nothing enforcing it, so the same drift would have gone unnoticed on the next env. `scripts/check-live-nodeports.sh` gained a **Phase 2 enforcement-posture check** (PR **#5696**, merged `712860075`): it reads `forbid-nodeport-service`'s `spec.validationFailureAction` in the cluster it is pointed at and **fails closed** — `Enforce` → pass, `Audit` → FAIL, policy absent → FAIL, any unrecognised action → FAIL. Its verdict classifier is vacuity-self-tested in-script (`Enforce`→PASS, `Audit`→not-PASS, `""`→ABSENT, `weird`→not-PASS), so a degenerate always-pass cannot survive its own file. Phase 1 (the literal scan) passing no longer implies compliance; the script says so in its own failure text.

Re-measured live on hw292 (dep `1c56518035a83e03`) 2026-08-06, and the guard demonstrably discriminates **in both directions on the live fleet** — which is what makes this a proof rather than an assertion:

| region | Services scanned | live NodePorts | `forbid-nodeport-service` | `check-live-nodeports.sh` exit |
|---|---|---|---|---|
| region-a (`me-east-215-a`) | 192 | 0 | **Enforce** | **0 — pass** |
| region-b (`me-east-215-b-1`) | 159 | 0 | **Audit** | **1 — FAIL** |

Both regions are still literally clean (0 NodePorts), and that is exactly the point: the only thing separating them is the *posture*, and only Phase 2 can see it. The source-side every-region flip (#5592/#5619) remains correct and merged — hw292 simply provisioned before delivery, so its region-b stays unflipped until remediated (item 3 below). Fleet enforcement is proven by running the guard **once per region kubeconfig**; running it once against the primary is the exact mistake that hid this for days.

---

## Gate 5 — the delivery chain itself (found 2026-08-04, the largest single cause of stalled progress)

Steps 1–3 of this plan were never the constraint. **The chain from `main` to a running binary was broken in two independent places, and both looked like "the fix didn't work".**

**5a. `build-ui` was failing, so nothing published at all.** Across the last ten `catalyst-build` runs on `main`, six had `build-ui=failure` with `deploy=skipped` — meaning six merges published *no image*. The cause was a genuine race in `ExecPanel.test.tsx` (#5633): the component renders byte-identical markup for `fallback-loading` and `fallback-ready` and dials its socket in a passive effect, so under CPU-starved CI the scheduler yields between commit and effect flush and the assertion runs before the socket exists. Reproduced 8/8 under load, 0/12 after the fix. **Fixed and merged (#5651); two consecutive greens since, each with `deploy=success`.** #5626 and #5633 closed on that evidence.

Behind it sat the reason nobody noticed for two months: the vitest step ran `npm test || echo WARN` from `f61a52ab6` until #5553, so the gate **exited 0 unconditionally**. When it finally told the truth, four source defects surfaced at once. A fail-open gate is worse than no gate — it manufactures the appearance of coverage.

**5b. Even a published image cannot reach a cutover Sovereign.** hw292's local Harbor holds exactly `["fad88bd"]` for catalyst-api — the step-03 prewarm tag, digest byte-identical to the running pod. **18 catalyst-api tags have published to ghcr since; zero arrived.** Three independent severances: `proxy-ghcr` has `registry_id = None` (a push-populated project, not a pull-through cache), `mirrorResync.enabled: false` is the deliberate Principle-14 git severance, and #5644's Day-2 IMAGE leg ships in cutover chart `0.1.161`/`0.1.162` while hw292 runs `0.1.159` — *the remedy cannot pass through the gap it remedies*. Note also that the shipped default `mode: detect` writes the missing set to a ConfigMap and copies nothing; only `mode: warm` delivers, and it needs an operator credential. Tracked as **#5640**.

**Correction, 2026-08-06 (re-measured live on hw292, not read off this doc).** All three severances still hold, and all three are *correct by design* — `proxy-ghcr` carrying no `registry_id` is #4975 deliberately replacing the proxy-cache with a push-mode mirror so step-08's deny-egress hold works at all. The above is also incomplete in a way that matters: it implies the Day-2 reconciler would deliver once it arrives. **It would not.** Its chart and image legs enumerate their work from the **local Gitea mirror**, and post-cutover that mirror is frozen (`mirrorResync.enabled: false`; nothing re-runs step-01). So both legs are structurally a no-op on a steady-state cut-over Sovereign — they can only ever deliver artifacts for pins that *already exist* in the frozen mirror, and then correctly report `ALL_PINS_PRESENT` forever, because every frozen pin genuinely is present. Live: local Gitea `main@7bcd1d59` pinning bp-self-sovereign-cutover `0.1.159` while upstream `main` had reached `0.1.169` — ten chart versions, zero arrived, nothing red. The same freeze is why #5650's `charts.loft.sh` fix (0.1.168) has not landed either. **The git pin is the head of the chain**, and it is what PR #5703 adds: a CATALOG leg inside the same one-shot, scope-bounded, audited operator window, running *first* in each cycle so one request moves the whole chain. The bootstrapping trap is real and is not engineered around — chart 0.1.170 cannot deliver itself to a Sovereign cut over before it, so the first delivery per Sovereign is `scripts/sovereign-daytwo-bootstrap.sh` (re-stamps the cutover's own step-01 + step-03 from PodSpec ConfigMaps already on the cluster), once, after which delivery is in-band.

The practical consequence: **#5642**'s catalyst-api leak fix (`Factory.AddCluster` rebuilt all 42 informers on byte-identical kubeconfigs) is correct, merged, published — and has never run. The live pod still leaks **62.1 Mi/min**, monotonic, 21 restarts at a ~59-minute cadence. Raising the memory limit remains the wrong fix: at that rate an 8Gi ceiling buys three hours instead of one and hides the defect.

## Gate 6 — sovereignty proves the wrong property (#5650)

Step-08's deny-egress hold is genuinely deny-*all* (`defaultDenyEgress: true` since #3678) — an earlier claim in this session that it blocked a named list was wrong and was corrected on the issue. But the hold is **time-boxed and then torn down**, so it proves nothing external was *reachable* for 600s. It cannot prove the Sovereign has no external *dependency*. Live: `vcluster-system/loft` → `https://charts.loft.sh`, interval **15m**, referenced by a live HelmRelease, artifact re-fetched from the public internet ~10h *after* `cutoverComplete=true`. A 15-minute interval straddles a 10-minute window entirely.

Fixed in cutover chart **0.1.162** (PR #5652, merged, **published to ghcr and verified**): a pre-hold lint asserting every non-suspended `HelmRepository`/`GitRepository`/`OCIRepository` resolves to a Sovereign-local host, `allowHosts` empty by default so it fails closed. Its test drives the function extracted from the *render* in both directions and caught a fail-open bug in the first draft, where an uncreatable scratch file made the lint return PASS having examined nothing.

## What actually moves the number next

1. **Walk hw292.** It is live, cc=true, and G12-proven; every "delivered" row above becomes a green stamp only by walking it. This is the only thing that legitimately moves the durable number.
2. **Close the delivery chain (#5640)** — without it, every future fix repeats 5b: correct in source, invisible in production.

> **hw292 cannot be the env that closes it — measured 2026-08-08.** Its
> `bp-self-sovereign-cutover` HR has read `False — dependency 'flux-system/bp-gitea'
> is not ready` for **5d4h**, and it sits at cutover chart `0.1.159`. The Day-2
> reconciler that delivers images ships `enabled: true` only from **`0.1.170`**, and
> the refuse-by-default hardening at **`0.1.176`** — so the delivery mechanism is not
> present, and the chart carrying it would have to arrive through the same severed
> Gitea/Harbor path. The only in-cluster remedy is restoring the stripped `ghcr-pull`
> credential, i.e. **re-tethering — precisely what cutover exists to prevent.** This
> is not a repairable env; it is the catch-22 in its terminal form.
>
> **Therefore the 12 deploy-gated rows need one fresh prov, not twelve fixes.** Their
> code is merged. Pre-flight step 1 is done and the train is **verified ready**:
>
> | slot | version | carries |
> |---|---|---|
> | `bp-self-sovereign-cutover` | `0.1.176` | Day-2 reconciler + refuse-by-default |
> | `bp-catalyst-platform` | `1.4.1330` | — |
> | `catalyst-api` image | `c3f9ab0` (built 2026-08-08) | **#5645** — `merge-base --is-ancestor e3342f977 c3f9ab0` ✅ |
>
> That last row is the one that matters: the image a fresh prov would run **contains
> the OOM fix**, so the fan-out truncation behind #5894 and the 12 rows does not
> reproduce. Firing on this train is worth the walk; firing on anything older is not.
3. **Remediate #5591 on region-b** (one HR patch) so the NodePort ban is enforcing fleet-wide rather than in half of it.
4. Merge #5534 + #5535, now unblocked — the env is ready and the merge freeze is thawed.
