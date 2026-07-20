# PATH TO 100% — hw279-cycle fix map (refreshed 2026-07-20)

> **Source of truth:** [`UAT.md`](UAT.md) — reset 2026-07-20 for the **hw279** fresh prov (dep `059126bb58d5dd81`, `hw279.omani.works`, 2-region me-east-215-a/b, fired 02:06Z). Currently `phase1-watching` → convergence.
> This file maps every non-green row family to its exact fix (issue + PR + code path) and its validation gate. A row stays ❌/☐ in UAT.md until the hw279 walk re-verifies it — **merge ≠ green** (founder rule).
> Prior anchor (hw274, 2026-07-19) superseded — its 8-fix train validated; the hw278 crank exposed the families below.

---

## The hw279 train — fixes merged, ALL validate on the hw279 walk

The hw278 fleet walk (2026-07-19) surfaced 17 ❌ rows. Root-cause analysis (this session, adversarially guarded — 2 fix-agents confirmed already-fixed rather than build redundant PRs) shows they are **overwhelmingly train-lag**: the fix was on `main`, hw278 fired on an older train. hw279 provisions from current `main`, so each validates on its walk.

| # | Defect (issue) | Fix PR / artifact | ❌ rows it un-gates | Validation gate on hw279 |
|---|---|---|---|---|
| 1 | **#5277** catalyst-api `/oidc/*` broker routes dropped when `catalyst-pin-broker-credentials` reflects after boot (no Reloader watch) → cold OIDC broker 404 | **#5278** (`45f733503`, chart 1.4.1185) — add the Secret to the Stakater Reloader annotation (`api-deployment.yaml`) | **SSO cold-broker family: 32, 33, 35, 36, 39, 111, 113, 115, 181, 183** (harbor/openbao/guacamole/pdns-admin/hubble) | cold (fresh-context) SSO lands authenticated first-try; `api.<fqdn>/oidc/*` returns 200 |
| 2 | **#5276** operator console still on #3383-deprecated `/tenant/orgs/*` alias → residual `tenant` tokens | **#5279** (`147f0eddd`, chart 1.4.1186) — console → `/organizations/*`; load-bearing `X-Tenant-Host` grandfathered | **214** | console bundle: 0 user-facing `tenant` tokens |
| 3 | **#5274** cloud graph `perRegionDynamicClient` id-match dropped region-b (full slug vs bare cloud-code kubeconfig) | **#5280** (`6b00c3ac5`, chart 1.4.1187) — boundary-safe region-code token-match, no fabrication (#4811 guard held) | **66** (+ 187 topology) | Cloud→Clusters renders 2/2, both regions |
| 4 | **#5270** cloudadoption endpoint host = NXDOMAIN pseudo-region → ELB observe "error retrieving LoadBalancer" | **#5272** (`e0787d47`, chart 1.3.4) — `hw_api_region` regex strips mimic `-a/-b`; endpoints on `${local.hw_api_region}` | **207** (+206/239) | `adopt-loadbalancer-primary` Workspace Observed=True |
| 5 | **#3996** cloud Reconciliation-lens reconciler nodes had no click drill-in | **#5229** (`0b01f9548`) — `ReconcilerDrillPanel`; `ArchitectureGraphPage.tsx:674` routes `recon:` nodes | **193** | click reconciler node → drill panel opens |
| 6 | **#5084** flux-CRD probe false-positive froze healthy secondary at `secondaryDegraded=true` → Topology false-singleton | **#5107** (`7f7fa34bd`) self-healing verdict + **#5280** region-b enum; Topology already derives from live runtime (`TopologyTab.tsx:188` runtime-first, 0 `secondaryDegraded` hits) | **187** | Topology "active-active" from runtime targets |
| 7 | **#5245** dr-failback re-clone wedged silently (wrong-CA `sslRootCert` on demoted externalCluster → pgbasebackup x509 loop) | **#5275** (`3237db89`, bp-cnpg-pair **0.2.20**) — strip wrong-CA cert + ConsistentSystemID divergence escalation + fail-loud convergence watch | **G12 / 136** (re-clone leg) | region-kill G12: demoted region-a re-clones to streaming (`pg_stat_wal_receiver` non-empty, ConsistentSystemID=True); clean 6/6 |

## Remaining non-green map

| Class | Rows / family | Exact gate | Owner action |
|---|---|---|---|
| **Walk-only** (fix merged, needs the hw279 walk) | the 7 families above (~15 ❌ rows) + the ~65 ⚠️ SSO/render re-stamp family flushed by ledger discipline | hw279 cc=true + authed fleet walk | walk-fleet stamps them; **do not pre-stamp — merge ≠ green** |
| **Uncertain — validate first** | **34** (KC admin console for sovereign realm → login form not silent) | possibly resolves once #5278 restores the broker; if it survives, a distinct KC-admin-client silent-SSO gap | walk hw279 → fix only if it genuinely survives #5278 |
| **Founder-gated** (the ONLY non-engineering gap) | **G8, 185** (Agenity chat driver / MCP `create_application` E2E) | **#4277** — a real Anthropic credential placed in the mothership secret by the founder (`seedAnthropicToken` loud-skips until then) + `bp-agenity` installed in a live Org | founder action |
| **Ledger artifact** | **220** (no stale predecessor-env references) | inherent to preserved ❌ rows carrying wiped-hw278 evidence; clears when a clean env has zero ❌ to preserve | resolves as the walk greens the ❌ set |

## §854 disposition (not a code gap)

**#5088** — the 3 live mothership NodePorts are `cinova/catalog-svc` (⛔ founder never-touch), `iogrid/cm-acme-http-solver` (ephemeral cert-manager solver, symptom of the 4d-stuck `proxy.iogrid.org` challenge, no chart), and `iogrid/proxy-gateway-socks5` (founder iogrid repo). **Zero** chart-sourced NodePorts in this monorepo (§854 CI gate holds). Real fix = `iogrid#844` (founder repo). Stays `status/parked`.
