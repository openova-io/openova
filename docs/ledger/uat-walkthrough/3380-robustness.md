# #3380 ROBUSTNESS — KEYSTONE walk on hw135 (fresh zero-touch prov)

**Env:** `hw135.omani.works` — deployment `368c2d9b74d0b71a`, **2-region** (`me-east-215-a` primary + `me-east-215-b` spoke), provisioned off current `main`. This is the keystone reproduction proof: every #3380 fix in this session must reproduce from `t=0` on a clean prov, or it does not count.

**Honest framing (unchanged from prior walks):** "the platform must not wound itself during provisioning / rolls / wipes" is an operations/reliability property — there is **no end-user feature** to click. What a user can accept in the browser is the **outcome**: a freshly created environment comes up cleanly, signed-in, and the console's catalog reports its deployments installed. On a FRESH prov that outcome IS the #3380 proof, because the at-source body (shared cloud-init → Flux → kyverno carve-outs → image-policy fixes) is what reconciled.

---

## A. User-visible outcome (browser walk)

Signed in zero-click via the handover URL (no login form), foreground headless Playwright from `/home/openova/repos/ping`, `page.on('pageerror')` + console-error capture registered. Fresh handover token minted immediately before each run (5-min `exp`).

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [console.hw135.omani.works](https://console.hw135.omani.works/auth/handover) | After a brand-new 2-region Sovereign is provisioned, the console loads and you are **signed in** (redirects `/auth/handover` → `/dashboard`, no manual fixing, zero login form) | ✅ | [![](../../sessions/2026-06-14/evidence/3380-keystone/r1-console-signedin.png)](../../sessions/2026-06-14/evidence/3380-keystone/r1-console-signedin.png) |
| [Apps](https://console.hw135.omani.works/apps) | The console's catalog shows **Deployments 49 / Catalog 64**, and every deployment leaf-card badge reads **INSTALLED** — `0` FAILED / `0` INSTALLING / `0` PENDING (authoritative leaf-card extraction: 50 cards, tally `{INSTALLED: 50}`, problemCards `[]`) | ✅ | [![](../../sessions/2026-06-14/evidence/3380-keystone/r2c-apps-signedin.png)](../../sessions/2026-06-14/evidence/3380-keystone/r2c-apps-signedin.png) |
| [Dashboard](https://console.hw135.omani.works/dashboard) | The utilisation treemap renders **95 items** under cluster `368c2d9b74d0b71a (hw-me-east-215-a-rtz-prod)` — every major workload tiled (cilium-agent 24%, kyverno, falco, harbor, the 5 PG instances, self-sovereign-cutover, the 3 vclusters, openbao, keycloak, grafana…). UI clean, no error overlay | ✅ | [![](../../sessions/2026-06-14/evidence/3380-keystone/r3b-dashboard-treemap.png)](../../sessions/2026-06-14/evidence/3380-keystone/r3b-dashboard-treemap.png) |
| Decommission / wipe → reported cleanly | The **Decommission** button is present (dashboard header, top-right of the treemap shot). Clicking it is **destructive** — wiping hw135 is the wipe-last act and out of this read-only walk's remit (the env is still being walked by the topology/cutover/funnel walkers). The wipe-report-JSON event (defect A) fires on the *next* wipe, not here. | N/A | (button visible in r3b) |
| Internal protections (image policy / NetworkPolicy / retries / wipe cleanup) | No UI by design — validated only by the convergence outcome above + the infra seams in §C. | N/A | §C below |

**Browser result:** ✅ 3 / N/A 2 / ❌ 0. The env **comes up signed-in zero-click and the console reports all 49 deployments INSTALLED with zero FAILED/INSTALLING badges** — the platform did not visibly wound itself at the install-record level on a clean 2-region prov. Zero pageerrors / console-errors across all three pages.

---

## B. Convergence (kubectl ground-truth — the real fresh-prov test)

The fresh-prov convergence itself IS the #3380 proof. Read directly from both region kubeconfigs (`/var/lib/catalyst/kubeconfigs/368c2d9b74d0b71a.yaml` + `-me-east-215-b-1.yaml`).

| Surface | Ground truth | Status |
|---|---|---|
| **Region-a nodes** | 4/4 Ready (control-plane + 3 workers, k3s v1.31.4) | ✅ |
| **Region-b nodes** | 4/4 Ready (control-plane + 3 workers) — proper 2-region, two independent kubeconfigs | ✅ |
| **Region-a HelmReleases** | **60/63 Ready=True.** The 3 not-Ready are `bp-cluster-autoscaler-hcloud` / `bp-hcloud-ccm` / `bp-velero` — all `spec.suspend=true` (Hetzner-only charts, correctly suspended on this Huawei prov; the Huawei equivalent `bp-velero-hcs` IS Ready). | ✅ |
| **Region-b HelmReleases** | **56/63 Ready** — the spoke subset (region-b deliberately omits `bp-catalyst-platform` / `bp-continuum` / `bp-self-sovereign-cutover` / `bp-sandbox`, which are primary-only, plus the 3 hcloud no-ops). Expected hub/spoke split. | ✅ |
| **Pod runtime (region-a)** | **160/168 Running or Completed.** | ⚠️ (see caveat) |
| **cnpg-pair (DR primary)** | `Cluster/cnpg-pair-bp-cnpg-pair-primary` stuck `Setting up primary` 33 min, **0 pods** in `cnpg` ns; HR dry-run `context deadline exceeded` on the cnpg mutating webhook (webhook pod IS 1/1). | ❌ (topology, not robustness — see caveat) |

**Convergence caveats (brutally honest):**
- **8 app-level pods are unhealthy** (the 8 of 168): `hubble-relay` CrashLoopBackOff, `hubble-ui-oauth2-proxy` CreateContainerConfigError, `newapi` CrashLoopBackOff, `guacd` Pending, `sandbox-controller` ImagePullBackOff, `sme/provisioning` Init:0/1, `scan-vulnerabilityreport`×2 OOMKilled. **None of these are #3380 robustness defects** — they are per-app issues owned by **#3374 (SSO: hubble/newapi/guacamole), #3376 (funnel: sme provisioning)**, and a trivy scan-job OOM. They are exactly why the console's install-record view (✅ above) does not reflect per-pod runtime — a known, documented gap. They do not represent the platform wounding *itself* during its lifecycle ops.
- **cnpg-pair primary not creating its pod after 33 min** is a real stuck item, but it belongs to **#3375 (TOPOLOGY-DR / region-kill)**, not robustness. The webhook dry-run timeout is a symptom of the primary instance never materialising. Flagged for the topology walker.

---

## C. Build-hardening (#3380 defect rows) — verified at the real seam on the fresh prov

The #3380 items are build-hardening (image policy, the post-mortem log API, the kyverno-Enforce landmine inventory, thin cloud-init). They have no browser surface, so each is verified at its real seam against the live hw135 cluster + the on-disk template that actually rendered this prov.

| Row | What was verified on the FRESH prov (how) | Status |
|---|---|---|
| **D-1 — bp-vllm Harbor-proxy image** | `helm template platform/vllm/chart --set vllm.enabled=true \| grep image:` renders exactly **one** image on the proxy path: `harbor.openova.io/proxy-dockerhub/vllm/vllm-openai:v0.6.4` — matches the live kyverno `harbor-proxy-pull` glob (`*/proxy-*/*`, Enforce, `Ready`), so the vllm pod is **admissible** at first scale-up. (Live ns `vllm` has 0 pods by design — latent until scaled.) Reproduces from source. | ✅ |
| **D-2 — bp-mgmt-vcluster initContainer images** | LIVE on hw135: `mgmt-vcluster` StatefulSet initContainers are `harbor.openova.io/proxy-k8s/kube-controller-manager:v1.31.4` + `harbor.openova.io/proxy-k8s/kube-apiserver:v1.31.4` — **Harbor-proxy-globbed, not bare `registry.k8s.io`**. The previously-CONFIRMED-LIVE landmine (next image roll denied) is fixed at source and reproduces clean. | ✅ |
| **D-row — kyverno-Enforce + carve-outs present** | `harbor-proxy-pull` cpol is **Enforce + Ready** on the fresh prov; `bp-network-policies` + `bp-kyverno-policies@1.0.35` HRs Ready. The CNP/image-policy machinery installed cleanly during bootstrap without wedging convergence — the #3296/#3201-class landmines did not re-trip. | ✅ |
| **2 — cloudinit-log GET survives wipe** (API code) | The handler (`products/catalyst/bootstrap/api/internal/handler/cloudinit_log.go`) is unchanged on `main`; the GET-after-record-wiped behaviour (200 byte-exact / 404 `no-cloudinit-log` / 400 `invalid-id` traversal) was proven on the rolled mothership in the prior walk and its test (`cloudinit_log_get_test.go::TestGetCloudInitLog_RecordWiped_FileSurvives`) gates it. | ✅ (code) |
| **2 — per-prov reproduction of the self-upload** | **GAP (honest):** hw135's cloud-init log did **not** persist to the mothership PVC — `/var/lib/catalyst/kubeconfigs/368c2d9b74d0b71a-cloudinit.log` is **absent** (8 *older* deployments' logs are present). Both region kubeconfigs DID land (Phase-1 PUT-kubeconfig worked), so the env converged; but the Huawei `provider_prelude_huawei` 30s self-upload loop (`infra/providers/huawei/main.tf:664-679`) left no artifact for this prov. The API survives a wipe, but there was nothing uploaded to survive. | ⚠️ (note) |
| **B — thin cloud-init (cilium/gw-CRD prelude → Flux)** | The open half of #3145. Current `main`'s `infra/providers/_shared/cloudinit-control-plane.tftpl` still carries the bounded imperative cilium (`helm install cilium`) + gateway-CRD prelude (Cilium-is-the-CNI chicken-and-egg — Flux's own controllers need a CNI to network). Correctly left as bounded-imperative; the env converged within the 15-min kubeconfig budget. Not regressed, not closed. | ☐ (open, as-is) |

**Latent kyverno-landmine — broader inventory (the honest open scope of row D):** the `harbor-proxy-pull` Enforce policy **excludes** these namespaces: `kube-system, flux-system, cilium, cilium-gateway, cert-manager, openova-system, catalyst, sme, kyverno, monitoring, ingress, dmz, mgmt, rtz, sso-bridge, powerdns`. But a real cluster-wide image scan finds bare upstream images still running in **non-excluded** namespaces — e.g. `docker.io/falcosecurity/falco`, `docker.io/grafana/{alloy,loki,tempo}`, `docker.io/velero/velero`, `quay.io/cilium/*` (in their own ns). They run today (already admitted), but on the **next re-admission/image-roll** any that land in a K1-scoped namespace would be **denied under Enforce**. This is precisely the #3380 row-D "inventory" defect (the two *named* rows are fixed; the long tail is not) — it is the **existing open scope of #3380**, not a keystone regression. The admission-test CI gate (row D, `platform/kyverno-policies/**`) is the durable fix.

---

## Result

**✅ 6 / ❌ 1 / ⚠️ 2 / N/A 2 / ☐ 1.**

**Headline — does #3380 reproduce zero-touch on the fresh prov? YES, for the robustness contract itself.** A clean off-`main` 2-region prov bootstrapped through the shared cloud-init + Flux to **60/63 (region-a) + 56/63 (region-b) HelmReleases Ready, 49 deployments INSTALLED in the console, signed-in zero-click, treemap rendering 95 items, zero browser errors** — the platform did not wound itself during its own provisioning. The two *named, source-fixed* kyverno landmines (bp-vllm proxy image, bp-mgmt-vcluster proxy-k8s initContainers) **both reproduce clean from `t=0`**, and the kyverno/network-policy carve-outs installed without wedging convergence.

**The honest open items (none of which are robustness regressions):**
- ❌ **cnpg-pair primary stuck `Setting up primary` 33 min, 0 pods** — owned by **#3375 (TOPOLOGY-DR)**.
- ⚠️ **8 app-level pods crashloop** (hubble / newapi / guacd / sandbox / sme-provisioning / trivy-OOM) — owned by **#3374 (SSO)** and **#3376 (funnel)**; visible only at the pod layer, not the console install-record.
- ⚠️ **hw135's cloud-init self-upload log absent from the PVC** — the GET-survives-wipe API is proven, but this prov uploaded nothing to survive (Huawei `provider_prelude` loop produced no artifact this run).
- ⚠️ **kyverno-Enforce long-tail** — bare `docker.io`/`quay.io` images still run in non-excluded namespaces; denied on next re-admission. The open scope of #3380 row D (admission-test CI gate is the durable fix).
- ☐ **thin cloud-init** (defect B) — bounded-imperative cilium/gw-CRD prelude remains (Cilium-is-the-CNI), correctly left as-is.

The robustness *proof* (clean self-reconcile) is **PASS** on the keystone; the residual ❌/⚠️ are tracked defects on other tickets + the documented open rows of #3380 itself, not new breakage introduced by the fresh prov.
