# hw281 — G12 failover 5/6 forensic bundle + region-a #5302 wedge + §854 NodePort enumeration

- **Env**: hw281 (dep `6db2745323dff4aa`, `hw281.omani.works`), Huawei kom4dc `me-east-215`, 2-region.
- **State at capture**: post-cutover (`cutoverComplete=true`); region-kill G12 already executed (region-a's 6 ECS hard-restarted, region-b promoted to RW primary). About to wipe for the next fresh prov — this is the pre-wipe evidence snapshot (LAW #960 DEBUG-BEFORE-WIPE + §854).
- **Capture date**: 2026-07-20 (UTC).
- **Mode**: READ-ONLY. Nothing was modified, patched, restarted, converted, or deleted on any cluster. Only this doc was written.
- **Kubeconfigs**: region-a `/tmp/hw281-a.kubeconfig`, region-b `/tmp/hw281-b.kubeconfig`, mothership `~/.kube/config` (ns `catalyst`).

---

## 1. G12 failover evidence — result still holds (5/6 failover proven)

### 1.1 Region-b is the promoted, writable RW primary (RPO evidence)

Region-b replica-cluster pods (`cnpg.io/cluster=cnpg-pair-bp-cnpg-pair-replica`, ns `cnpg`):

```
NAME                               READY  STATUS   AGE   INSTANCEROLE  ROLE
cnpg-pair-bp-cnpg-pair-replica-1   1/1    Running  4h5m  primary       primary   <- promoted RW primary
cnpg-pair-bp-cnpg-pair-replica-2   1/1    Running  97m   replica       replica
cnpg-pair-bp-cnpg-pair-replica-3   1/1    Running  97m   replica       replica
```

`psql` on the promoted primary (`cnpg-pair-bp-cnpg-pair-replica-1`, container `postgres`):

```
$ psql -tAc "select pg_is_in_recovery(), (select count(*) from g12_hw281)"
f|2
```

- `pg_is_in_recovery() = f` → region-b is **writable** (out of recovery, promoted primary). ✅
- `count(*) from g12_hw281 = 2` → the **2 expected rows** are present: the pre-kill row + the post-promote row. This is the RPO proof — the pre-kill write survived the region-a kill and replicated to region-b, and a post-promote write succeeded against the promoted primary. ✅

### 1.2 No split-brain — region-a has NO writable primary

Region-a (`/tmp/hw281-a.kubeconfig`, ns `cnpg`):

```
$ kubectl -n cnpg get pods -l cnpg.io/cluster=cnpg-pair-bp-cnpg-pair-primary
No resources found in cnpg namespace.

$ kubectl -n cnpg get pods
NAME                                                  READY  STATUS    RESTARTS      AGE
cnpg-pair-bp-cnpg-pair-dr-failback-5987f4b769-xzbwj   2/2    Running   2 (96m ago)   4h18m

$ kubectl -n cnpg get cluster
No resources found in cnpg namespace.
```

Region-a has **no running cnpg primary pod and no cnpg Cluster CR** — only a `dr-failback` deployment pod. Region-a is still wedged (see §2) and therefore cannot be a second writable primary. **No split-brain.** ✅

### 1.3 Region-b serves the control-plane during region-a's degraded state

```
$ curl -sk -o /dev/null -w '%{http_code}' https://console.hw281.omani.works/   (x3)
200
200
200

$ curl -sk -o /dev/null -w '%{http_code}' https://api.hw281.omani.works/healthz (x3)
200
200
200
```

Console and API both return **200 on all 3 probes** while region-a is degraded → control plane is being served from region-b. ✅

**G12 failover verdict: PROVEN (still holds).** Writable region-b primary, 2/2 g12 rows (RPO met), 200s on console+api, no split-brain. The one non-green leg is failback (region-a cold-pull wedge, §2) — consistent with the recorded 5/6 result.

---

## 2. Region-a #5302 wedge evidence (Harbor blob-redirect cold-pull defect)

### 2.1 HelmRelease Ready count

```
$ kubectl --kubeconfig /tmp/hw281-a.kubeconfig get hr -A --no-headers | awk '$4=="True"' | wc -l
0    (ready)
$ kubectl --kubeconfig /tmp/hw281-a.kubeconfig get hr -A --no-headers | wc -l
67   (total)
```

**Region-a HR Ready = 0 / 67.** After the region-kill hard-restart, region-a cannot cold-pull charts/images because Harbor issues an OBS blob-redirect whose TLS cert does not cover the registry FQDN (see §2.2). This is exactly the #5302 wedge (fixed durably by #5303/PR — verify by RENDER not HR value; hw281 predates that fix so the wedge is expected here).

### 2.2 Representative source-controller x509 error (OBS redirect TLS failure)

```
{"level":"error","ts":"2026-07-20T16:19:02.583Z","msg":"Reconciler error","controllerKind":"HelmChart",
 "HelmChart":{"name":"flux-system-bp-crossplane-claims","namespace":"flux-system"},
 "error":"chart pull error: failed to download chart for remote reference:
   failed to get 'oci://registry.hw281.omani.works/openova-io/bp-crossplane-claims:1.3.4':
   failed to copy: httpReadSeeker: failed open: failed to do request:
   Get \"https://obs.me-east-215.kom4dc.nationalcloud.om/catalyst-hw281-omani-works-6db27453/docker/registry/v2/blobs/sha256/79/79d4.../data?X-Amz-...\":
   tls: failed to verify certificate: x509: certificate is valid for
     obs.kom4dc.nationalcloud.om, *.obs-website.me-east-215.kom4dc.nationalcloud.om,
     *.obs.kom4dc.nationalcloud.om, *.obs.me-east-215.kom4dc.nationalcloud.om,
     obs-website.me-east-215.kom4dc.nationalcloud.om, obs.me-east-215.kom4dc.nationalcloud.om,
     not registry.hw281.omani.works"}
```

Harbor 302-redirects the blob GET to the OBS S3 endpoint; the client follows the redirect but validates the presented OBS wildcard cert against the original `registry.hw281.omani.works` host → `x509 ... not registry.hw281.omani.works`. The cold-pull fails, so every region-a HR stays not-Ready.

### 2.3 Live harbor-registry redirect setting

```
$ kubectl --kubeconfig /tmp/hw281-a.kubeconfig -n harbor get cm harbor-registry \
    -o jsonpath='{.data.config\.yml}' | grep -A1 redirect
  redirect:
    disable: false
```

`redirect.disable: false` = blob redirects to OBS are **enabled** — the pre-#5303 state. This is expected on hw281 (env predates the fix). The #5303 fix flips this to `disable: true` (via lowercase `disableredirect` — camelCase is silently dropped by Helm) so Harbor serves blobs directly and the cold-pull TLS mismatch never occurs. The region-kill correctly surfaced this cold-pull defect that the cutover path had masked.

---

## 3. §854 NodePort enumeration (fresh live evidence — no change made)

Command per cluster:
```
kubectl --kubeconfig <kc> get svc -A -o json \
  | jq -c '[.items[]|select(.spec.type=="NodePort")|{ns:.metadata.namespace,name:.metadata.name,ports:[.spec.ports[].nodePort]}]'
```

| Cluster | NodePort Services | Count |
|---|---|---|
| **hw281 region-a** | `[]` | **0** |
| **hw281 region-b** | `[]` | **0** |
| **mothership** | see below | 3 (all founder-gated, non-platform) |

Mothership raw output:
```
[{"ns":"cinova","name":"catalog-svc","ports":[30341]},
 {"ns":"iogrid","name":"cm-acme-http-solver-sh4np","ports":[31866]},
 {"ns":"iogrid","name":"proxy-gateway-socks5","ports":[31080]}]
```

**Conclusion:** The **Catalyst platform runs 0 NodePorts** — both hw281 regions return an empty array, in full compliance with §854 / the absolute NodePort ban. The only NodePorts anywhere are on the mothership and belong to **founder-gated, non-platform** workloads:
- `cinova/catalog-svc` — a **never-touch suspended SME app** (not platform-owned).
- `iogrid/*` (`cm-acme-http-solver-sh4np`, `proxy-gateway-socks5`) — the founder's **separate iogrid repo** (not platform-owned; the acme-http-solver one is a transient cert-manager solver Service).

These were **not touched, converted, or deleted** — evidence-gathering only.

---

## Summary

- **G12 failover: still PROVEN.** Region-b writable (`pg_is_in_recovery=f`), `g12_hw281=2` rows (RPO met: pre-kill + post-promote), console+api all 200×3, **no split-brain** (region-a has no cnpg primary/Cluster CR). Consistent with the 5/6 result — the only non-green leg is failback, blocked by the region-a cold-pull wedge below.
- **Region-a #5302 wedge: confirmed.** HR Ready **0/67**; source-controller `x509 ... not registry.hw281.omani.works` on OBS blob-redirect; `harbor-registry` `redirect.disable: false` (pre-#5303 state, expected — hw281 predates #5303).
- **§854 NodePorts:** mothership **3** (all founder-gated: `cinova/catalog-svc`, `iogrid/*`), region-a **0**, region-b **0**. Platform = 0 NodePorts.

---

## Pre-fire pin audit (agent a217d127, LAW #960)

**Fire verdict: platform bring-up is PIN-READY.** All 64 unique bootstrap-kit slot charts
are published to ghcr (incl. bp-harbor 1.2.43 AND 1.2.44, bp-catalyst-platform 1.4.1167), so
a fresh 2-region prov's platform provisioning + cutover + region-kill G12 will not wedge on a
chart-pull 404.

**Non-blocking finding — 5 broken marketplace cards (pre-existing, Pillar-1 hygiene):**
`bp-prometheus`, `bp-redis`, `bp-clickhouse`, `bp-opensearch`, `bp-n8n` (all `1.0.0`) are
`visibility: listed` in the catalog seed but their charts were NEVER published to ghcr — and
prometheus/redis/n8n have no chart dir in the repo at all (clickhouse/opensearch have a
`platform/<x>/` dir but no `chart/`). A tenant clicking any of these cards would 404 on
instantiation. NOT a fresh-prov blocker (not bootstrap-kit slots, not in UAT.md), so it does not
gate the G12 6/6 crank. Smallest correct fix when addressed: flip the 5 to `visibility: unlisted`
until their charts are built+published. Tracked here rather than as a fresh issue to avoid
open-count inflation on a non-blocker (L8).

**Catalog-seed lockstep lag (non-blocking):** 14 blueprints have spec.version < source.version
but the source.version (the pulled one) is published in every case — catalog metadata lag, not a
pull blocker.
