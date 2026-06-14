# Wave-3 Operations Runbook — hw135 (keystone fresh prov)

> **Status**: READ-ONLY assessment + operator runbook. Author did not mutate hw135.
> **Env**: hw135 / deployment `368c2d9b74d0b71a` / fqdn `hw135.omani.works` / 2-region (`me-east-215-a` primary + `me-east-215-b` secondary) / status `ready`, converged zero-touch.
> **Refs**: #3375 (region-kill / North Star 4), #3379 (cutover / Pillar 5), #3376 → #3374 (funnel back-half / per-Org realm).
> **Assessed**: 2026-06-14 ~05:05Z.

## How to (re)fetch kubeconfigs

```bash
POD=$(kubectl --kubeconfig ~/.kube/config -n catalyst get pods --no-headers | grep '^catalyst-api-' | grep ' Running ' | head -1 | awk '{print $1}')
# primary region (me-east-215-a)
kubectl --kubeconfig ~/.kube/config -n catalyst exec "$POD" -- cat /var/lib/catalyst/kubeconfigs/368c2d9b74d0b71a.yaml > /tmp/kc-w3.yaml
# secondary region (me-east-215-b)
kubectl --kubeconfig ~/.kube/config -n catalyst exec "$POD" -- cat /var/lib/catalyst/kubeconfigs/368c2d9b74d0b71a-me-east-215-b-1.yaml > /tmp/kc-w3-b.yaml
```

Mothership API token (RS256 JWT, key `/tmp/hw-priv.pem`):
```bash
export KEY=/tmp/hw-priv.pem
TOK=$(python3 -c 'import jwt,time,os;k=open(os.environ["KEY"]).read();n=int(time.time());print(jwt.encode({"email":"emrah.baysal@openova.io","sub":"emrah.baysal@openova.io","realm_access":{"roles":["catalyst-owner"]},"tier":"owner","iat":n,"exp":n+900},k,algorithm="RS256"))')
curl -sk -H "Authorization: Bearer $TOK" https://console.openova.io/sovereign/api/v1/deployments/368c2d9b74d0b71a
# (or: scripts/sovereign-lifecycle.sh _tok)
```

---

## Executive readiness summary

| Op | Readiness | Blocker | Depends on |
|----|-----------|---------|------------|
| **#3375 region-kill** | ❌ **BLOCKED** | `cnpg-pair` primary cannot initdb — the `mcluster.cnpg.io` / `mpod.cnpg.io` admission webhook (`cnpg-webhook-service.cnpg-system.svc:443`) is **unreachable from the kube-apiserver datapath** (`context deadline exceeded`). No cnpg-pair data exists → nothing to fail over. | — (independent) |
| **#3379 cutover** | ⚠️ **READY-but-not-handed-over** | Chart installed + dormant (correct). Gate holds (`tofu-phase0-archive` not sealed). Needs handover fired to un-gate. To witness the *real* deny-egress hold, `egressTest.enforceCIDRBlock` must be flipped to `true` (currently `false` → passive assertion only). | — (independent of region-kill) |
| **#3376 funnel** | ❌ **BLOCKED on #3379** | `sme/provisioning` pod stuck `Init:0/1` on `wait-for-cutover-token`; `openova-sme-tenants` GitRepository `False` (missing `openova-sme-tenants-git-auth` secret). The token is minted by **cutover Step 09** → funnel cannot complete until cutover runs. | **#3379 cutover MUST run first** |

**Execution order**: `#3375 region-kill` is independent and can run in parallel with cutover prep. `#3379 cutover` MUST precede `#3376 funnel` (hard wire: Step 09 mints the provisioning Gitea token). Recommended: **fix the cnpg-pair webhook datapath first** (unblocks #3375), run **#3375 region-kill** in parallel with **#3379 cutover**, then **#3376 funnel** once cutover Step 09 lands.

---

# OP 1 — #3375 Region-kill (North Star 4)

### Readiness: ❌ BLOCKED — cnpg-pair never initialises

Live evidence (2026-06-14 ~05:00Z):

| Surface | State |
|---------|-------|
| `bp-continuum` HR | ✅ `InstallSucceeded` (chart `bp-continuum@0.1.5`) |
| `continuum-controller` pod (catalyst-system, on primary) | ✅ Running |
| Continuum CRD `continuums.dr.openova.io` | ✅ present |
| **Continuum CR** | ❌ **none exists** (`No resources found`) — `continuum.enabled` defaults false (matches hw128 memory) |
| `bp-cnpg-pair` HR | ❌ `Ready=False StateError` — webhook `mcluster.cnpg.io` dry-run `context deadline exceeded` |
| cnpg-pair PRIMARY cluster (region-a) | ❌ `Setting up primary`, 0 instances, initdb Job Running 0/1 for 22m+, PVC `Pending` |
| cnpg-pair REPLICA cluster (region-b) | ❌ `Setting up primary` (its own primary, also can't initdb); `spec.replica.enabled=true source=...-primary` ✅ correctly configured |
| **WAL streaming a→b** | ❌ **NOT streaming** — no primary instance exists in either region |

### Root cause (verified, not speculation)

The cnpg-pair primary instance Pod is **never created**. The cnpg operator log shows it perpetually `"A job is currently running. Waiting"` on `cnpg-pair-bp-cnpg-pair-primary-1-initdb`, and that initdb Job emits, live and ongoing:

```
Warning  FailedCreate  job/cnpg-pair-bp-cnpg-pair-primary-1-initdb
  Error creating: Timeout: request did not complete within requested timeout - context deadline exceeded
```

This is a **webhook-reachability failure on the kube-apiserver → `cnpg-webhook-service.cnpg-system.svc:443` (endpoint `10.42.3.3:9443`) datapath**:
- The cnpg validating/mutating webhooks all have `failurePolicy=Fail timeout=10` (`mcluster.cnpg.io`, `vcluster.cnpg.io`, `mpod`/`mbackup`/etc).
- A plain `kubectl run busybox` **in `cnpg-system` also timed out** (`Error from server (Timeout): context deadline exceeded`) → confirms the webhook intercepting that namespace's creates is unreachable, not a one-off.
- A `kubectl create configmap` in `cnpg` ns **succeeded** → kyverno's resource webhook path is fine; the failure is **specific to the cnpg webhook service**.
- The webhook pod (`cnpg-cloudnative-pg-845c788c6b-vz5wb`, `10.42.3.3`) is Running on node `…-me-east-215-a-w3d1be9` (zone `me-east-215-a`, **same VPC** as the CP node `10.149.1.33`) → this is an **intra-region, same-VPC Cilium datapath gap**, NOT the kom4dc cross-VPC peering issue.
- `bp-network-policies` (chart 1.0.4) is installed; `baseline-default-deny` CNP exists in **catalyst-system** (endpointSelector `{}`, scoped to that ns). It does NOT select cnpg-system pods, and cnpg-system has **no CNP at all** → so this is a Cilium datapath/connectivity problem (apiserver→ClusterIP→webhook-pod on that node), not a policy denial.
- Timeline: all OTHER cnpg clusters (gitea-pg, harbor-pg, openova-flow-pg, sme-pg) are READY — they initdb'd 53–69m ago, BEFORE the webhook became unreachable. cnpg-pair (created ~22m ago) hit the broken window.

### Remediation (operator, before the walk can run)

**Goal**: restore apiserver → `cnpg-webhook-service` reachability so the initdb Pod admits. Try in order, re-check `kubectl --kubeconfig /tmp/kc-w3.yaml -n cnpg get events --field-selector reason=FailedCreate | tail` after each:

1. **Bounce the cnpg webhook pod** (forces a fresh endpoint + Cilium endpoint re-program):
   ```bash
   kubectl --kubeconfig /tmp/kc-w3.yaml -n cnpg-system rollout restart deploy cnpg-cloudnative-pg
   kubectl --kubeconfig /tmp/kc-w3.yaml -n cnpg-system rollout status deploy cnpg-cloudnative-pg
   ```
2. **If still timing out, restart the cilium agent on the webhook's node** (`…-w3d1be9`) to re-program the datapath:
   ```bash
   kubectl --kubeconfig /tmp/kc-w3.yaml -n kube-system delete pod -l k8s-app=cilium \
     --field-selector spec.nodeName=catalyst-hw135-omani-works-368c2d9b-me-east-215-a-w3d1be9
   ```
3. **Verify reachability** with an in-cluster probe (run from a node that schedules, e.g. catalyst-system which has datapath):
   ```bash
   kubectl --kubeconfig /tmp/kc-w3.yaml -n cnpg-system run whtest --rm -it \
     --image=harbor.openova.io/proxy-docker/library/busybox:1.36 --restart=Never -- \
     sh -c 'nc -zvw3 cnpg-webhook-service.cnpg-system.svc 443'
   ```
   (If pod creation *itself* times out, the webhook is still down — repeat 1–2.)
4. Once the webhook answers, the cnpg operator will create the primary Pod on its next reconcile; the PVC binds (`WaitForFirstConsumer` resolves) and the cluster goes READY. Then confirm cross-region WAL:
   ```bash
   kubectl --kubeconfig /tmp/kc-w3.yaml   -n cnpg get cluster cnpg-pair-bp-cnpg-pair-primary
   kubectl --kubeconfig /tmp/kc-w3-b.yaml -n cnpg get cluster cnpg-pair-bp-cnpg-pair-replica
   # replica should report streaming from the primary; check pg_stat_replication on primary
   ```

> **DEBUG-BEFORE-WIPE**: do NOT wipe hw135 to "fix" this — capture the cloud-init log (`GET /api/v1/deployments/368c2d9b74d0b71a/cloudinit-log`) and the cilium-agent log on `…-w3d1be9` first. This webhook-datapath gap is the actual Wave-3 finding; a wipe loses it.

### Walk steps (once cnpg-pair is replicating)

This follows the **hw128 PASS pattern** (`docs/sessions/2026-06-11/hw128-region-kill-walk-PASS.md`, PR #3306): 3s promote, zero data loss. Promotion is **operator-driven** — `autoFailover` defaults false; the continuum-controller lives on the primary (the cluster that dies), so the manual patch is canonical.

1. **Create test data in region-a (primary)**:
   ```bash
   PRIMARY=$(kubectl --kubeconfig /tmp/kc-w3.yaml -n cnpg get pods -l cnpg.io/cluster=cnpg-pair-bp-cnpg-pair-primary,role=primary -o name | head -1)
   kubectl --kubeconfig /tmp/kc-w3.yaml -n cnpg exec "$PRIMARY" -- psql -U postgres -c \
     "CREATE TABLE IF NOT EXISTS walk_proof(id serial primary key, t timestamptz default now(), note text); INSERT INTO walk_proof(note) VALUES ('pre-kill hw135 region-a');"
   ```
2. **Confirm the row replicated to region-b (<5s expected)**:
   ```bash
   REPLICA=$(kubectl --kubeconfig /tmp/kc-w3-b.yaml -n cnpg get pods -l cnpg.io/cluster=cnpg-pair-bp-cnpg-pair-replica -o name | head -1)
   kubectl --kubeconfig /tmp/kc-w3-b.yaml -n cnpg exec "$REPLICA" -- psql -U postgres -c "SELECT * FROM walk_proof;"
   ```
3. **Kill region-a** (canonical = Huawei `BatchStopServersRequest type=HARD` on the region-a ECS ids; Huawei AK/SK is server-side on the mothership — `secret catalyst/huawei-operator-creds`; platform-created servers → autonomous). The region-a ECS ids are the 4 nodes `…-me-east-215-a-cp1-029afb`, `…-a-w3d1be9`, `…-a-w7b51e0`, `…-a-wdfac75`. Use the Huawei SDK (`huaweicloudsdkecs.BatchStopServersRequest`) from the bastion/mothership, OR `kubectl --kubeconfig /tmp/kc-w3.yaml cordon` + scale the cnpg-pair primary to 0 as a software-only mimic.
4. **Promote region-b** (the documented hw128 one-command patch, ~3s RTO):
   ```bash
   kubectl --kubeconfig /tmp/kc-w3-b.yaml -n cnpg patch cluster cnpg-pair-bp-cnpg-pair-replica \
     --type=merge -p '{"spec":{"replica":{"enabled":false}}}'
   # verify promotion:
   kubectl --kubeconfig /tmp/kc-w3-b.yaml -n cnpg exec "$REPLICA" -- psql -U postgres -c "SELECT pg_is_in_recovery();"   # expect: f
   ```
5. **Verify data survived + accept writes post-kill**:
   ```bash
   kubectl --kubeconfig /tmp/kc-w3-b.yaml -n cnpg exec "$REPLICA" -- psql -U postgres -c \
     "SELECT * FROM walk_proof; INSERT INTO walk_proof(note) VALUES ('post-kill hw135 region-b'); SELECT * FROM walk_proof;"
   ```

### Acceptance evidence to capture
- `pg_is_in_recovery() = f` on region-b within seconds of the promote patch (timestamp the kill + the promote).
- `walk_proof` pre-kill row present on region-b through the kill + a NEW post-kill INSERT accepted (write-before / read-after table).
- The Huawei ECS `BatchStop` API response (or cordon/scale evidence) showing region-a hard-stopped.
- Optionally a `Continuum` CR with `status.lastSwitchover` / `status.phase` reflecting the promotion (if the CR is created — see note below).

> **Note on Continuum CR**: there is currently NO Continuum CR. Promotion via the direct CNPG patch (above) is the proven path and does not require it. If the walk should exercise the Continuum controller, create a CR with `spec.applicationRef` pointing at an `active-hotstandby` Application, `spec.primaryRegion`, `spec.hotStandbyRegions`, `autoFailover:false` (admission refuses non-active-hotstandby placement). Switchover trigger fields: `spec.primaryRegion` flip + the Application page UI (#1101). The controller only surfaces an alarm when `autoFailover:false` — it does not auto-promote.

### Biggest risk
The webhook-datapath fix (remediation step 1–2) may not stick if the underlying Cilium issue recurs on the next pod reschedule — verify cnpg-pair reaches READY and stays READY for several minutes before starting the kill, or the "promote" will have no replicating standby to promote.

---

# OP 2 — #3379 Cutover (Pillar 5)

### Readiness: ⚠️ READY (dormant, correctly) — needs handover to fire

Live evidence:

| Surface | State |
|---------|-------|
| `bp-self-sovereign-cutover` HR | ✅ `InstallSucceeded` (chart `bp-self-sovereign-cutover@0.1.56`) — installed dormant |
| `ghcr-pull` secret keys (registry pivot check) | ✅ `['ghcr.io']` — **NOT half-pivoted** (the #3052 bootstrap gate is holding) |
| cutover Jobs in catalyst ns | ✅ none stamped (correct — gate returned 425 at bootstrap) |
| `tofu-phase0-archive` (handover marker) | ❌ not sealed (handover not done — correct for a not-yet-handed-over prov) |

This is exactly the intended pre-handover state. The bootstrap auto-trigger (`templates/10-auto-trigger-job.yaml`) hit `/api/v1/internal/cutover/trigger`, which returned **425 Too Early** and stayed dormant.

### How the gate + handover works (verified in source)

- **Trigger route** (no session, SA-token TokenReview): `products/catalyst/bootstrap/api/cmd/api/main.go:987` → `HandleCutoverInternalTrigger` (`cutover_internal.go:268`).
- **425 gate** (`cutover_internal.go:442-453`): for `source=="internal"` AND `requireHandoverForCutover()` (default true), it calls `sovereignHandoverComplete(ctx)`, which reads OpenBao KV-v2 `secret/catalyst/tofu-phase0-archive` (`cutover_internal.go:166-178`). Not present → `cutoverSpawnHandoverIncomplete` → HTTP **425** (`cutover_internal.go:519-523`). **The marker is an OpenBao secret, NOT a kube Secret.**
- **Handover seals it**: `ReceiveTofuArchive` (`handover.go:311`) writes `secret/catalyst/tofu-phase0-archive` via `PutKVv2` (`handover.go:368`) and **immediately auto-fires the cutover with `source="handover"`** (gate-skipped) at `handover.go:417-437` (gated by `fireCutoverOnHandover()`, default true).
- **What POSTs the archive** (the mothership→Sovereign call to `https://api.hw135.omani.works/api/v1/handover/tofu-archive`):
  1. Zero-touch path: the mothership Phase-1 watcher auto-fires `fireHandover` → `exportTofuArchiveToChild` (`phase1_watch.go:1191/1299`) when the new Sovereign reports `bp-catalyst-platform Ready=True`.
  2. Operator wizard path: `FinaliseHandover` (`handover.go:138`) → `postTofuArchive` (`handover.go:454`), behind RequireSession (`main.go:1242`).
  3. Operator re-mint recovery: `MintHandoverToken` (`handover_jwt.go:67/204`).

### Exact mechanism to hand over hw135 + fire the 11-step cutover

**Option A — operator-driven handover (recommended for a witnessed walk)**. Hit the mother-side finalise so the archive is pushed to hw135, which seals the marker and auto-fires the cutover:
```bash
export KEY=/tmp/hw-priv.pem
TOK=$(python3 -c 'import jwt,time,os;k=open(os.environ["KEY"]).read();n=int(time.time());print(jwt.encode({"email":"emrah.baysal@openova.io","sub":"emrah.baysal@openova.io","realm_access":{"roles":["catalyst-owner"]},"tier":"owner","iat":n,"exp":n+900},k,algorithm="RS256"))')
# Mother-side finalise (synchronous wizard path) — pushes tofu-archive to the child:
curl -sk -X POST -H "Authorization: Bearer $TOK" -H "Content-Type: application/json" \
  https://console.openova.io/sovereign/api/v1/handover/finalise/368c2d9b74d0b71a
```
> Confirm the exact mother-side finalise route/shape against `handover.go:138` (`FinaliseHandover`, registered at `main.go:1242` on the catalyst-api binary) — the wizard `POST /api/v1/handover/finalise/{id}` is the synchronous orchestrator. If the mothership exposes it under `/sovereign/...`, adjust the path. The child receiver is always `POST https://api.hw135.omani.works/api/v1/handover/tofu-archive`.

**Option B — direct child trigger (if already handed over)**: once `tofu-phase0-archive` is sealed, the operator CTA `POST /api/v1/sovereign/cutover/start` (`source="operator"`, gate-skipped, behind session) on hw135 fires it. Use Option A for a fresh prov since it seals the marker as a side-effect.

### The 11 cutover steps (chart `platform/self-sovereign-cutover/chart/templates/`)
`01` gitea-mirror → `02` harbor-projects → `03` harbor-prewarm → `04` registry-pivot-daemonset → `05` flux-gitrepository-patch → `06` helmrepository-patches → `07` catalyst-api-env-patch → `08` **egress-block-test (the 10-min hold)** → `09` gitea-token-mint (**mints the funnel provisioning token**) → `10` vcluster-registry-pivot → `11` crossplane-provider-pivot + mirror-resync-cronjob.

### The 10-min deny-egress hold — CIDR vs FQDN (verified)

Step 08 (`08-egress-block-test-job.yaml` + `values.yaml`):
- `egressTest.durationSeconds: 600` (the 10-min survival window); `stepTimeouts.egressBlockTestSeconds: 900` (600 deny + assert buffer).
- **`egressTest.enforceCIDRBlock: false` by DEFAULT** → Step 08 only does the **passive architectural assertion** (GitRepository + every HelmRepository + catalyst-api env are local-Gitea/Harbor) plus a 600s Flux readiness poll. **It does NOT apply a deny NetworkPolicy in the default config.**
- To witness the **real deny-egress hold** the memory describes, set `egressTest.enforceCIDRBlock: true` in the 06a overlay / cutover values. When true, Step 08 resolves `egressTest.blockedDomains` → public IPs and applies a **scoped CiliumNetworkPolicy `egressDeny` `toCIDR`** for the window. **So the block is CIDR-based** (Cilium 1.16 can't FQDN-match in `egressDeny`; the FQDNs `github.com`, `ghcr.io`, `harbor.openova.io`, `xpkg.upbound.io` are resolved to /32s).
- `cutoverComplete=true` is set only if every HelmRelease + Kustomization stays Ready through the window.

> **Decision for the walk**: to satisfy the #3379 acceptance ("survives the 10-min deny-egress hold"), flip `enforceCIDRBlock:true` BEFORE firing. Per the chart comment, this is opt-in precisely because a mis-resolved CIDR overlapping a live dependency FAILs Step-08 → cutover aborts → half-pivoted unrecoverable prov. This is the single biggest cutover risk (below). If a passive-assertion pass is acceptable for this walk, leave it false and capture the survival-poll evidence instead.

### Acceptance evidence to capture
- `tofu-phase0-archive` sealed in OpenBao (or `sovereignHandoverComplete` true) post-finalise.
- Cutover Jobs `cutover-step-01` … `cutover-step-11` Complete in catalyst ns; `bp-self-sovereign-cutover` egress-block-test Job **succeeded** (not Failed).
- `ghcr-pull` secret now keys `registry.hw135.omani.works`, GitRepository `openova` url = local Gitea, catalyst-api `CATALYST_GITOPS_REPO_URL` = local Gitea — captured DURING and AFTER the 600s window.
- If `enforceCIDRBlock:true`: the applied CiliumNetworkPolicy `egressDeny toCIDR` present for the window + all HRs/Kustomizations Ready throughout + the policy removed at end-of-test.
- `cutoverComplete=true` on the deployment record / cutover status ConfigMap.

### Biggest risk
Firing Step-08 with `enforceCIDRBlock:true` when a `blockedDomains` IP overlaps a live dependency CIDR → Step-08 FAILs → cutover engine aborts mid-pivot → **half-pivoted unrecoverable prov** (the exact #3039/#3052 failure class). Mitigate: confirm the resolved `blockedDomains` /32s don't collide with any in-cluster/dependency CIDR before firing; have the wipe-and-reprov path ready if it aborts.

---

# OP 3 — #3376 Funnel back-half → #3374 per-Org realm

### Readiness: ❌ BLOCKED on #3379 cutover (Step 09 token) + a missing git-auth secret

Live evidence (sme ns):

| Service | State |
|---------|-------|
| admin, auth, billing, catalog, console, domain, ferretdb, gateway, marketplace, notification, tenant | ✅ Running (35m) |
| **provisioning** | ❌ `Init:0/1` — init container `wait-for-cutover-token` looping `annotation not yet set (current: '')` |
| sme-pg (1+2) | ✅ Running |
| `openova-sme-tenants` GitRepository | ❌ `False` — `secret "openova-sme-tenants-git-auth" not found` (#3457 / #806 chain) |
| marketplace-api (catalyst-system) | ✅ Running |

### Root cause (verified)

1. **provisioning gates on the cutover token**. The `wait-for-cutover-token` init container (source confirmed: it polls Secret `sme/provisioning-github-token` for annotation `catalyst.openova.io/token-source=self-sovereign-cutover-step-09`). That annotation is written by **cutover Step 09** (`09-gitea-token-mint-job.yaml:272,283`), which mints the real Gitea API token. Until cutover runs, provisioning stays `Init:0/1`, its Service has 0 ready endpoints, and any `provisioning/start` 502s. **→ the funnel back-half cannot complete until #3379 cutover Step 09 lands.** (This is the "one unbuilt wire" called out in `tofu_archive_handover_export.go:5-24`.)
2. **`openova-sme-tenants` GitRepository is `False`** because `flux-system/openova-sme-tenants-git-auth` doesn't exist yet (the local-Gitea PAT secret the gitops chain #806/#3457 mirrors). Pre-cutover the SME gitops repo URL is github (`CATALYST_GITOPS_REPO_URL` default `https://github.com/openova-io/openova`); post-cutover it flips to local Gitea (`marketplace_settings.go:236`). The git-auth secret is part of the same Step-09/handover token wiring.

### Funnel walk steps (run AFTER #3379 cutover completes Step 09)

1. **Verify the gate cleared** (provisioning Ready, GitRepository Ready):
   ```bash
   kubectl --kubeconfig /tmp/kc-w3.yaml -n sme get pod -l app=provisioning      # expect 1/1 Running
   kubectl --kubeconfig /tmp/kc-w3.yaml -n flux-system get gitrepository openova-sme-tenants  # expect Ready=True
   kubectl --kubeconfig /tmp/kc-w3.yaml -n sme get secret provisioning-github-token \
     -o jsonpath='{.metadata.annotations.catalyst\.openova\.io/token-source}'   # expect self-sovereign-cutover-step-09
   ```
2. **Operator mints a voucher** (BSS menu in the operator console, or billing API):
   ```bash
   # admin endpoint, behind session — issue a voucher:
   POST https://console.hw135.omani.works/.../billing/vouchers/issue  {amount/plan, recipient_email?}
   ```
   The redeem URL form is `https://marketplace.hw135.omani.works/redeem/?code=<CODE>` (built by `notification/templates/templates.go:295`).
3. **Stranger redeems at marketplace** → the public landing calls `POST /api/billing/vouchers/redeem-preview` (public route, `gateway/main.go:75` → `billing/handlers/vouchers.go:328`) which **validates without consuming**. Actual redemption is a **checkout side-effect**: the voucher is applied transactionally inside `POST /billing/checkout` via the `promo_code` field (`billing/handlers/routes.go:47-53`). There is no standalone "redeem-consume" endpoint.
4. **Tenant Organization provisions** via `POST /api/v1/sme/tenants` → `HandleCreateSMETenant` (`sme_tenant.go:509`). For the marketplace funnel `kind="customer"`. It persists a pending `SMETenantProvisionRecord` (`TenantNamespace: sme-<id>`, `VClusterName: vc-<subdomain>`) and fires the 7-step `runSMETenantPipeline` (`sme_tenant.go:825`): Step 1 **commits a per-tenant Kustomize overlay** (HelmReleases for bp-keycloak/bp-cnpg/bp-wordpress-tenant/bp-openclaw/bp-stalwart-tenant + vcluster) to the gitops repo and **pushes** (`WriteTenantOverlay`, `sme_tenant.go:859-873`) → Flux reconciles → Steps 2–7: bp-charts → DNS (PowerDNS) → certs → keycloak-client verify → tenant-registry → done. **It does not `kubectl apply` an Organization CR directly — it pushes gitops** (to local Gitea post-cutover; this is why the Step-09 token + git-auth secret are prerequisites).
5. **Stranger lands signed-in with the app running** at `https://console.<subdomain>.<tenant-domain>` — zero-login (URL → signed in, per North Star 3). The app (e.g. WordPress) runs inside the tenant vcluster.

### Which produces the per-Org realm (#3374)

Per-Org isolation IS a **dedicated per-Org realm**, not the shared `sovereign` realm with per-Org clients — created via the path that runs:
- **Marketplace/SME-tenant funnel path** (the one this walk drives): the per-tenant realm is named **`sme-<subdomain>`** and is **materialised by the bp-keycloak chart's `bootstrap` block inside the tenant's vcluster** (committed to gitops at Step 1, `sme_tenant_keycloak.go:97`, `sme_tenant.go:971,980`). The orchestrator step `ProvisionSMEClients` (`sme_tenant_keycloak.go:74`) is a **verification poll** that the chart-bootstrapped clients (`catalyst-ui, wordpress, openclaw, stalwart`) exist (direct creation only as a `CATALYST_SME_KC_DIRECT_PROVISION=true` fallback).
- **Organization-CR path** (newer #3084, if an Organization CR is created): `reconcilePerOrgRealm` (`core/controllers/organization/internal/controller/per_org_realm.go:71`) → `EnsureRealm` POSTs `/admin/realms` with realm named exactly `<slug>` (`keycloak.go:262,279,355`) at Organization reconcile time; finalizer `orgs.openova.io/per-org-realm` deletes it on teardown.

### Acceptance evidence to capture
- provisioning pod `1/1 Running` + `openova-sme-tenants` GitRepository `Ready=True` after cutover Step 09.
- Screenshot: operator issues voucher in BSS menu → voucher code.
- Screenshot: stranger opens `marketplace.hw135.omani.works/redeem/?code=<CODE>` → redeem landing.
- The `SMETenantProvisionRecord` advancing through the 7 pipeline steps to `done`; the per-tenant gitops overlay committed + Flux reconciling it; the `sme-<subdomain>` realm present in Keycloak (or `<slug>` realm via the Org-CR path).
- Screenshot: stranger lands on `console.<subdomain>.<tenant-domain>` **already signed in** with the app running (no login UI).

### Biggest risk
Hard dependency inversion: the funnel **cannot** complete until cutover Step 09 mints the provisioning token. If #3379 cutover aborts (Step 08 risk above), the funnel stays blocked. Secondary risk: the `openova-sme-tenants-git-auth` secret (#3457/#806) must be present for the tenant gitops push to succeed — confirm it lands as part of the handover/Step-09 wiring, else the pipeline Step 1 push 401s.

---

## Dependency graph + recommended execution order

```
#3375 region-kill ─────────────────┐  (independent; gated only on the cnpg-pair webhook-datapath fix)
                                    │
#3379 cutover ──────────► #3376 funnel ──────────► #3374 per-Org realm
  (independent;            (HARD dep: Step 09        (produced as a side-effect
   gate held, needs        mints provisioning         of tenant provision)
   handover fired)         token + git-auth)
```

1. **First**: fix the cnpg-pair webhook datapath (OP-1 remediation) — unblocks region-kill.
2. **Parallel**: run **#3375 region-kill** (once cnpg-pair replicates) AND prep/fire **#3379 cutover** (independent surfaces, different namespaces).
3. **After cutover Step 09 lands**: run **#3376 funnel** (provisioning unblocks) → **#3374 per-Org realm** falls out of the tenant provision.

**Parallelizable**: OP-1 (region-kill) ∥ OP-2 (cutover). **Strictly serial**: OP-2 → OP-3.
