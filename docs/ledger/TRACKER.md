# OpenOva — Status Tracker

Regenerated every 15 min by `/home/openova/bin/refresh-dod-dashboard.sh`. Every `#NNNN` is a clickable GitHub link.

|  |  |
|---|---|
| Last refreshed | `2026-08-10T07:45:02Z` |
| Deploy cron (#799) | ✓ deploy-cron healthy (image-reroll last ran 9m ago) |
| Open issues | 100 |
| Open DoD gates | 0 / 41 |
| Open TBD-* regressions | 0 |
| DoD completion | <img alt="DONE" src="https://img.shields.io/badge/-DONE-2ea043?style=flat-square" /> 41 / 41 = 100% |

**Legend:** <img alt="DONE" src="https://img.shields.io/badge/-DONE-2ea043?style=flat-square" /> done · <img alt="WAITING_PROV" src="https://img.shields.io/badge/-WAITING__PROV-bf8700?style=flat-square" /> fix shipped, awaits prov · <img alt="OPEN" src="https://img.shields.io/badge/-OPEN-cf222e?style=flat-square" /> open · <img alt="DEFERRED" src="https://img.shields.io/badge/-DEFERRED-6e7781?style=flat-square" /> deferred

---

## 1. Customer journey (founder's success criterion)

| # | Step | Status | Blocking issue |
|---|---|---|---|
| 1 | Operator issues voucher | <img alt="DONE" src="https://img.shields.io/badge/-DONE-2ea043?style=flat-square" /> | — |
| 2 | Email arrives in recipient inbox | <img alt="WAITING_PROV" src="https://img.shields.io/badge/-WAITING__PROV-bf8700?style=flat-square" /> | [#1741](https://github.com/openova-io/openova/issues/1741) → blocked by [#1793](https://github.com/openova-io/openova/issues/1793) |
| 3 | Recipient hits redeem landing | <img alt="DONE" src="https://img.shields.io/badge/-DONE-2ea043?style=flat-square" /> | — |
| 4 | Recipient creates org + checkout | <img alt="DONE" src="https://img.shields.io/badge/-DONE-2ea043?style=flat-square" /> | — |
| 5 | tenant.created → Organization CR | <img alt="DONE" src="https://img.shields.io/badge/-DONE-2ea043?style=flat-square" /> | [#1900](https://github.com/openova-io/openova/issues/1900) closed |
| 6 | Organization CR exists | <img alt="DONE" src="https://img.shields.io/badge/-DONE-2ea043?style=flat-square" /> | — |
| 7 | vCluster + Keycloak + Gitea materialize | <img alt="WAITING_PROV" src="https://img.shields.io/badge/-WAITING__PROV-bf8700?style=flat-square" /> | [#1906](https://github.com/openova-io/openova/issues/1906) [#1901](https://github.com/openova-io/openova/issues/1901) closed; t32 verifies |
| 8 | Tenant subdomain HTTPS responds | <img alt="WAITING_PROV" src="https://img.shields.io/badge/-WAITING__PROV-bf8700?style=flat-square" /> | [#1907](https://github.com/openova-io/openova/issues/1907) closed; t32 verifies |

t32 (live now): `console.t32.omani.works` returns HTTP 200 + envoy. Handover fired 2026-05-19T05:05:24Z.

---

## 2. Open-issue blocking graph

**All 100 open issues** grouped by where they sit in the convergence sequence. Each chain runs left-to-right; chains stack vertically.

> 💡 GitHub strips click handlers from rendered mermaid for security — every node label below has a 1:1 entry in the **clickable index** that follows the diagram.

```mermaid
flowchart LR
  classDef epic fill:#1f6feb,stroke:#0d419d,color:#fff,stroke-width:3px
  classDef open  fill:#cf222e,stroke:#a40e26,color:#fff,stroke-width:2px
  classDef wait  fill:#bf8700,stroke:#9a6700,color:#fff,stroke-width:2px
  classDef done  fill:#2ea043,stroke:#1a7f37,color:#fff,stroke-width:2px
  classDef defer fill:#6e7781,stroke:#4f555c,color:#fff,stroke-width:2px

  %% === EPIC: Customer Journey (voucher → tenant → WordPress) ===
  E_CJ(("Customer Journey")):::epic
  E_CJ --> I1793["#1793 SMTP creds missing"]:::wait
  I1793 --> I1741["#1741 Voucher email in 60s"]:::open
  I1741 --> D1842["#1842 D28 Voucher UI + email"]:::wait
  E_CJ --> I1723["#1723 WordPress install"]:::open
  E_CJ --> I1724["#1724 Gitea /git/refs/heads"]:::open
  I1723 --> D1829["#1829 D29 Zero-touch tenant"]:::wait
  I1724 --> D1829

  %% === EPIC: Multi-region Sovereign ===
  E_MR(("Multi-region")):::epic
  E_MR --> I1904["#1904 Fan-out regression"]:::wait
  I1904 --> D1808["#1808 D5 /cloud 3 regions"]:::wait
  I1904 --> D1817["#1817 D16 L1/L2 fan-out"]:::wait
  I1904 --> D1820["#1820 D19 Apps/Cloud counter"]:::open
  I1904 --> D1821["#1821 D20 Jobs region filter"]:::open

  %% === EPIC: Auth & SSO ===
  E_AUTH(("Auth & SSO")):::epic
  E_AUTH --> I1905["#1905 auth.fqdn hijack"]:::open
  I1905 --> D1807["#1807 D4 Keycloak PIN-login"]:::open

  %% === EPIC: Sandbox runtime + audit trail ===
  E_SBX(("Sandbox")):::epic
  E_SBX --> I1899["#1899 Gitea mirror lag"]:::open
  I1899 --> D1747["#1747 D11 Sandbox E2E"]:::wait
  E_SBX --> I1776["#1776 NATS publisher"]:::open
  I1776 --> D1835["#1835 D35 NATS round-trip"]:::wait

  %% === EPIC: Data plane (CNPG + LLM) ===
  E_DATA(("Data plane")):::epic
  E_DATA --> D1831["#1831 D31 CNPG active-hot-standby"]:::open
  E_DATA --> D1834["#1834 D34 newapi LLM gateway"]:::open

  %% === EPIC: SME billing (gateway 503 + purchase) ===
  E_SME(("SME billing")):::epic
  E_SME --> I1748["#1748 C9 voucher list 503"]:::open
  E_SME --> I1749["#1749 C10 voucher issue 503"]:::open
  E_SME --> I1750["#1750 C15 purchase 404"]:::open

  %% === EPIC: Observability + cosmetic ===
  E_OBS(("Observability")):::epic
  E_OBS --> I1728["#1728 E4 Treemap coverage"]:::open
  E_OBS --> I1734["#1734 G5 Hubble inter-region"]:::open
  E_OBS --> I1882["#1882 A28 kubeconfig 409"]:::open

  %% === EPIC: Parked (post-MVP) ===
  E_PARK(("Parked post-MVP")):::epic
  E_PARK --> I1726["#1726 D12 Anthropic OAuth"]:::defer
  E_PARK --> I1727["#1727 D13 anti-abuse"]:::defer
  E_PARK --> I1741b["#1741 Voucher IMAP write-cov"]:::defer

  %% === EPIC: Deferred architectural ===
  E_DEF(("Deferred arch")):::epic
  E_DEF --> D1841["#1841 A6 Provider-mix"]:::defer

  %% === EPIC: Completed today (2026-05-19) ===
  E_DONE(("Completed today")):::epic
  E_DONE --> C1806["#1806 D3 PIN-login ✓"]:::done
  E_DONE --> C1815["#1815 D14 re-login ✓"]:::done
  E_DONE --> C1827["#1827 D26 CSP fonts ✓"]:::done
  E_DONE --> C1773["#1773 D0 redirect ✓"]:::done
  E_DONE --> C1795["#1795 C4-fup toggle ✓"]:::done
  E_DONE --> C1880["#1880 t27 timeout ✓"]:::done

  %% === EPIC: t31 TLS chain VICTORY (2026-05-18/19) ===
  E_TLS(("t31 TLS VICTORY")):::epic
  E_TLS --> P1888["PR #1888 cilium-gw"]:::done
  E_TLS --> P1889["PR #1889 sovereign-tls"]:::done
  E_TLS --> P1892["PR #1892 listeners"]:::done
  E_TLS --> P1894["PR #1894 IaC eval"]:::done
  E_TLS --> P1898["PR #1898 8-annot cap"]:::done

  %% === EPIC: Wave 34 PRs merged today ===
  E_W34(("Wave 34 PRs")):::epic
  E_W34 --> P1903["PR #1903 RBAC"]:::done
  E_W34 --> P1909["PR #1909 HTTPRoute"]:::done
  E_W34 --> P1910["PR #1910 Gitea path"]:::done
  E_W34 --> P1911["PR #1911 NP NS"]:::done
  E_W34 --> P1912["PR #1912 CNP 6443"]:::done
  E_W34 --> P1913["PR #1913 parent_domains"]:::done
  E_W34 --> P1914["PR #1914 SMTP code"]:::done
  E_W34 --> P1915["PR #1915 SMTP chart"]:::done
  E_W34 --> P1916["PR #1916 Gitea mirror"]:::done
  E_W34 --> P1918["PR #1918 NATS scaffold"]:::done
  E_W34 --> P1919["PR #1919 sme-gw CNP"]:::done

  %% --- Click-through to GitHub issues ---
  click I1793 "https://github.com/openova-io/openova/issues/1793" "Open #1793"
  click I1741 "https://github.com/openova-io/openova/issues/1741" "Open #1741"
  click D1842 "https://github.com/openova-io/openova/issues/1842" "Open #1842"
  click I1904 "https://github.com/openova-io/openova/issues/1904" "Open #1904"
  click D1808 "https://github.com/openova-io/openova/issues/1808" "Open #1808"
  click D1817 "https://github.com/openova-io/openova/issues/1817" "Open #1817"
  click D1820 "https://github.com/openova-io/openova/issues/1820" "Open #1820"
  click D1821 "https://github.com/openova-io/openova/issues/1821" "Open #1821"
  click I1723 "https://github.com/openova-io/openova/issues/1723" "Open #1723"
  click I1724 "https://github.com/openova-io/openova/issues/1724" "Open #1724"
  click D1829 "https://github.com/openova-io/openova/issues/1829" "Open #1829"
  click I1905 "https://github.com/openova-io/openova/issues/1905" "Open #1905"
  click D1807 "https://github.com/openova-io/openova/issues/1807" "Open #1807"
  click I1899 "https://github.com/openova-io/openova/issues/1899" "Open #1899"
  click D1747 "https://github.com/openova-io/openova/issues/1747" "Open #1747"
  click I1776 "https://github.com/openova-io/openova/issues/1776" "Open #1776"
  click D1835 "https://github.com/openova-io/openova/issues/1835" "Open #1835"
  click D1831 "https://github.com/openova-io/openova/issues/1831" "Open #1831"
  click D1834 "https://github.com/openova-io/openova/issues/1834" "Open #1834"
  click I1748 "https://github.com/openova-io/openova/issues/1748" "Open #1748"
  click I1749 "https://github.com/openova-io/openova/issues/1749" "Open #1749"
  click I1750 "https://github.com/openova-io/openova/issues/1750" "Open #1750"
  click I1726 "https://github.com/openova-io/openova/issues/1726" "Open #1726"
  click I1727 "https://github.com/openova-io/openova/issues/1727" "Open #1727"
  click I1728 "https://github.com/openova-io/openova/issues/1728" "Open #1728"
  click I1734 "https://github.com/openova-io/openova/issues/1734" "Open #1734"
  click I1741b "https://github.com/openova-io/openova/issues/1741" "Open #1741"
  click I1882 "https://github.com/openova-io/openova/issues/1882" "Open #1882"
  click D1841 "https://github.com/openova-io/openova/issues/1841" "Open #1841"
```

### 🔗 Clickable issue index (mirror of every node above)

**Voucher email:** [#1793](https://github.com/openova-io/openova/issues/1793) SMTP creds → [#1741](https://github.com/openova-io/openova/issues/1741) email reach → [#1842](https://github.com/openova-io/openova/issues/1842) D28 issuance UI

**Multi-region fan-out:** [#1904](https://github.com/openova-io/openova/issues/1904) regression → [#1808](https://github.com/openova-io/openova/issues/1808) D5 cloud · [#1817](https://github.com/openova-io/openova/issues/1817) D16 L1/L2 · [#1820](https://github.com/openova-io/openova/issues/1820) D19 Apps/Cloud counter · [#1821](https://github.com/openova-io/openova/issues/1821) D20 Jobs filter

**D29 customer journey:** [#1723](https://github.com/openova-io/openova/issues/1723) WordPress install · [#1724](https://github.com/openova-io/openova/issues/1724) Gitea path → [#1829](https://github.com/openova-io/openova/issues/1829) D29 zero-touch

**Keycloak SSO:** [#1905](https://github.com/openova-io/openova/issues/1905) auth.fqdn hijack → [#1807](https://github.com/openova-io/openova/issues/1807) D4 PIN-login flow

**Sandbox runtime:** [#1899](https://github.com/openova-io/openova/issues/1899) Gitea mirror lag → [#1747](https://github.com/openova-io/openova/issues/1747) D11 Sandbox E2E

**NATS audit:** [#1776](https://github.com/openova-io/openova/issues/1776) sandbox.requested publisher → [#1835](https://github.com/openova-io/openova/issues/1835) D35 round-trip

**CNPG / newapi:** [#1831](https://github.com/openova-io/openova/issues/1831) D31 active-hot-standby · [#1834](https://github.com/openova-io/openova/issues/1834) D34 newapi LLM gateway

**SME gateway bugs (t32):** [#1748](https://github.com/openova-io/openova/issues/1748) C9 list · [#1749](https://github.com/openova-io/openova/issues/1749) C10 issue · [#1750](https://github.com/openova-io/openova/issues/1750) C15 purchase

**TBDs needing re-walk:** [#1726](https://github.com/openova-io/openova/issues/1726) D12 Anthropic OAuth · [#1727](https://github.com/openova-io/openova/issues/1727) D13 anti-abuse · [#1728](https://github.com/openova-io/openova/issues/1728) E4 treemap · [#1734](https://github.com/openova-io/openova/issues/1734) G5 Hubble · [#1741](https://github.com/openova-io/openova/issues/1741) voucher IMAP · [#1882](https://github.com/openova-io/openova/issues/1882) A28 kubeconfig 409

**Deferred:** [#1841](https://github.com/openova-io/openova/issues/1841) A6 provider-mix canonical

**🟢 Recently completed (clickable):** [#1806](https://github.com/openova-io/openova/issues/1806) D3 PIN-login · [#1815](https://github.com/openova-io/openova/issues/1815) D14 re-login · [#1827](https://github.com/openova-io/openova/issues/1827) D26 CSP fonts · [#1773](https://github.com/openova-io/openova/issues/1773) D0 redirect · [#1795](https://github.com/openova-io/openova/issues/1795) C4-fup toggle · [#1880](https://github.com/openova-io/openova/issues/1880) t27 timeout

**🟢 t31 TLS chain VICTORY (2026-05-18/19):** [PR #1888](https://github.com/openova-io/openova/pull/1888) cilium-gw · [PR #1889](https://github.com/openova-io/openova/pull/1889) sovereign-tls · [PR #1890](https://github.com/openova-io/openova/pull/1890) bake-time secrets · [PR #1892](https://github.com/openova-io/openova/pull/1892) listener wildcards · [PR #1893](https://github.com/openova-io/openova/pull/1893) UserAccess seed · [PR #1894](https://github.com/openova-io/openova/pull/1894) IaC eval fix · [PR #1898](https://github.com/openova-io/openova/pull/1898) 8-annot cap

**🟢 Today's merged PRs (Wave 34, 2026-05-19):** [PR #1903](https://github.com/openova-io/openova/pull/1903) RBAC · [PR #1909](https://github.com/openova-io/openova/pull/1909) HTTPRoute · [PR #1910](https://github.com/openova-io/openova/pull/1910) Gitea path · [PR #1911](https://github.com/openova-io/openova/pull/1911) NP NS · [PR #1912](https://github.com/openova-io/openova/pull/1912) CNP 6443 · [PR #1913](https://github.com/openova-io/openova/pull/1913) parent_domains · [PR #1914](https://github.com/openova-io/openova/pull/1914) SMTP code · [PR #1915](https://github.com/openova-io/openova/pull/1915) SMTP chart · [PR #1916](https://github.com/openova-io/openova/pull/1916) Gitea mirror · [PR #1918](https://github.com/openova-io/openova/pull/1918) NATS scaffold · [PR #1919](https://github.com/openova-io/openova/pull/1919) sme-gateway CNP · [PR #1921](https://github.com/openova-io/openova/pull/1921) newapi DB DSN

**Concurrency cap (2026-05-19 06:59 founder ask):** max 3 parallel sub-agents. Existing 6 complete gracefully; future dispatches respect cap.

### All 100 open items (clickable table)

| # | Title | Bucket |
|---|---|---|
| [#5440](https://github.com/openova-io/openova/issues/5440) | ClusterMesh cross-region service import died during cutover — Harbor/Gitea/new | Other |
| [#5442](https://github.com/openova-io/openova/issues/5442) | harbor-prewarm warms images from the LIVE CLUSTER but charts from the MIRROR's p | Other |
| [#5443](https://github.com/openova-io/openova/issues/5443) | Cutover pivots the marketplace resolver to local Harbor but never mirrors the ca | Other |
| [#5444](https://github.com/openova-io/openova/issues/5444) | Console Blueprint version editor accepts a version that exists in no registry an | Other |
| [#5445](https://github.com/openova-io/openova/issues/5445) | Per-Org postgres mounts its PVC directly at PGDATA — lost+found on ext4 EVS ma | Other |
| [#5450](https://github.com/openova-io/openova/issues/5450) | Sovereign's own Kyverno image-tag-pinned policy rejects its own catalog app mani | Other |
| [#5451](https://github.com/openova-io/openova/issues/5451) | Per-Org console badges dead apps as INSTALLED with live Open buttons — 4/4 ret | Other |
| [#5459](https://github.com/openova-io/openova/issues/5459) | OpenBao SSO lands an authenticated-but-unauthorized session — /sys/internal/ui | Other |
| [#5460](https://github.com/openova-io/openova/issues/5460) | Console does not silently re-establish after session TTL — operator is re-prom | Other |
| [#5461](https://github.com/openova-io/openova/issues/5461) | Sovereign console intermittent 503 (envoy upstream connect timeout, ~17-25% prob | Other |
| [#5465](https://github.com/openova-io/openova/issues/5465) | Sovereign catalyst-api OOMKilled AT the post-#5352 4Gi limit, 63min into fresh h | Other |
| [#5466](https://github.com/openova-io/openova/issues/5466) | newapi SSO dead-ends: /api/oauth/sovereign code exchange returns 403 → no SSO  | Other |
| [#5467](https://github.com/openova-io/openova/issues/5467) | harbor-prewarm logs the first 8 chars of the GHCR PAT on every cutover — line  | Other |
| [#5471](https://github.com/openova-io/openova/issues/5471) | Cilium Gateways report Programmed=False / AddressNotAssigned while serving HTTP  | Other |
| [#5472](https://github.com/openova-io/openova/issues/5472) | Post-cutover sovereignty gap: 16 of 19 deployable marketplace apps resolve chart | Other |
| [#5476](https://github.com/openova-io/openova/issues/5476) | Application -> Environment -> Organization chain does not resolve for half the e | Other |
| [#5480](https://github.com/openova-io/openova/issues/5480) | A16: per-region secret generation on a shared VIP — newapi SESSION_SECRET conf | Other |
| [#5484](https://github.com/openova-io/openova/issues/5484) | Marketplace: redeem rate limiter fires at ~5x budget and penalises bystanders (t | Other |
| [#5485](https://github.com/openova-io/openova/issues/5485) | Observability surfaces inherit the wrong object: reconciler logs match by prefix | Other |
| [#5488](https://github.com/openova-io/openova/issues/5488) | Cutover aborts at secondary-kubeconfigs pre-flight after a catalyst-api restart: | Other |
| [#5489](https://github.com/openova-io/openova/issues/5489) | Four surfaces report a vCluster that does not exist (parent row hardcodes it, ku | Other |
| [#5496](https://github.com/openova-io/openova/issues/5496) | Catalog per-field save silently reverts the previous save — query invalidated  | Other |
| [#5499](https://github.com/openova-io/openova/issues/5499) | harbor-core crashloops on 28P01 blocking the cutover — region-b consumes a rol | Other |
| [#5501](https://github.com/openova-io/openova/issues/5501) | POST /api/v1/organizations reports state:done with all six steps done in 0s whil | Other |
| [#5502](https://github.com/openova-io/openova/issues/5502) | org-controller reports reason=VClusterProvisioning for host-tier Orgs that never | Other |
| [#5504](https://github.com/openova-io/openova/issues/5504) | bp-postgres: initdb owner and managed.roles mint the same role with different cr | Other |
| [#5505](https://github.com/openova-io/openova/issues/5505) | P0: all Namespace creates denied — #5494 flipped 10 policies to Enforce, expos | Other |
| [#5508](https://github.com/openova-io/openova/issues/5508) | Topology tab renders replication lag 0.0 s behind a green pill while the API rep | Other |
| [#5510](https://github.com/openova-io/openova/issues/5510) | Catalog card: a single-field Save silently reverts untouched sibling fields —  | Other |
| [#5513](https://github.com/openova-io/openova/issues/5513) | active-hot-standby renders a 2-region pair over a singleton: empty openova.io/re | Other |
| [#5514](https://github.com/openova-io/openova/issues/5514) | P1: Switch over armed against a phantom standby — replication-status 200s with | Other |
| [#5516](https://github.com/openova-io/openova/issues/5516) | openova-mcp: per-Org bearer carries no deployment_id claim — list/get/create_a | Other |
| [#5527](https://github.com/openova-io/openova/issues/5527) | Cutover: per-Org tenant HelmRepositories outside the pivot's authority — org-t | Other |
| [#5558](https://github.com/openova-io/openova/issues/5558) | Mothership catalyst-api stuck at replicas=0 — Flux does not own spec.replicas, | Other |
| [#5559](https://github.com/openova-io/openova/issues/5559) | catalog-seed ships 6 INERT Blueprint CRs — seeded against charts that were nev | Other |
| [#5561](https://github.com/openova-io/openova/issues/5561) | §854 solver carve-out assumes HTTP-01 solvers 'GC automatically' — 5 have per | Other |
| [#5567](https://github.com/openova-io/openova/issues/5567) | Mothership has NO Kyverno but 7 orphaned webhooks remain — 5 fail-closed, bloc | Other |
| [#5573](https://github.com/openova-io/openova/issues/5573) | Mothership GitOps loop is DEAD — all 4 Flux controllers at replicas=0 + flux-s | Other |
| [#5575](https://github.com/openova-io/openova/issues/5575) | Wizard component catalog is unvalidated: 6 components have no Blueprint, 8 carry | Other |
| [#5591](https://github.com/openova-io/openova/issues/5591) | phase2b bootstrapMode->false flip is primary-region-only: secondary regions keep | Other |
| [#5596](https://github.com/openova-io/openova/issues/5596) | cutoverComplete=true certifies sovereignty while region-B keeps 64/65 HelmReposi | Other |
| [#5598](https://github.com/openova-io/openova/issues/5598) | Guacamole: SSO lands but every session API call returns 403 PERMISSION_DENIED � | Other |
| [#5599](https://github.com/openova-io/openova/issues/5599) | newapi: valid Keycloak authz code rejected — /api/oauth/sovereign returns 403  | Other |
| [#5601](https://github.com/openova-io/openova/issues/5601) | DR Switchover button disabled with 'no caught-up standby' while Continuum report | Other |
| [#5602](https://github.com/openova-io/openova/issues/5602) | Hubble UI: authenticated landing works but data streams never connect (persisten | Other |
| [#5608](https://github.com/openova-io/openova/issues/5608) | Keycloak admin console: group Role-mapping tab crashes ('Cannot read properties  | Other |
| [#5609](https://github.com/openova-io/openova/issues/5609) | active-passive topology is never selectable: all 24 blueprints declaring it have | Other |
| [#5610](https://github.com/openova-io/openova/issues/5610) | Catalog edit surfaces: summary inline editor opens empty (wipe hazard) + YamlEdi | Other |
| [#5611](https://github.com/openova-io/openova/issues/5611) | Cloud list: Volumes claims 0 while 50 EVS block volumes are attached (Storage Cl | Other |
| [#5612](https://github.com/openova-io/openova/issues/5612) | newapi session expires in under an hour and the 'Continue with OpenOva SSO' reco | Other |
| [#5617](https://github.com/openova-io/openova/issues/5617) | Per-Org oidc-gate has a gateway-ingress CNP but no egress policy — under names | Other |
| [#5623](https://github.com/openova-io/openova/issues/5623) | Region-kill: the three shared-pg DR pairs never promote — only bp-cnpg-pair sh | Other |
| [#5634](https://github.com/openova-io/openova/issues/5634) | UAT row 92: funnel discards the 429 rate-limit response — a throttled customer | Other |
| [#5635](https://github.com/openova-io/openova/issues/5635) | Per-Org app FQDN fails ~50% of fresh connections — single-region namespace beh | Other |
| [#5642](https://github.com/openova-io/openova/issues/5642) | catalyst-api OOMKilling in a loop on hw292 (15 restarts, requests 96Mi vs limits | Other |
| [#5650](https://github.com/openova-io/openova/issues/5650) | Post-cutover Sovereign fetches charts.loft.sh every 15m: step-08's deny-egress h | Other |
| [#5750](https://github.com/openova-io/openova/issues/5750) | Guacamole row-35 ERROR page: diagnostic to distinguish known #5358 chart-drift f | Other |
| [#5752](https://github.com/openova-io/openova/issues/5752) | bp-stalwart-tenant: per-Org install door emits empty spec.parameters — domain/ | Other |
| [#5759](https://github.com/openova-io/openova/issues/5759) | sovereign-daytwo-bootstrap --apply REVERTS a completed cutover: 62/69 HelmReposi | Other |
| [#5762](https://github.com/openova-io/openova/issues/5762) | products/catalyst/bootstrap/ui e2e + marketplace customer-journey: triage the 16 | Other |
| [#5767](https://github.com/openova-io/openova/issues/5767) | catalyst-api has no graceful shutdown — SIGTERM kills orphan-release mid-persi | Other |
| [#5796](https://github.com/openova-io/openova/issues/5796) | ESCALATION: uatco-mail-rtz-a annotation directive conflicts with a founder 'No'  | Other |
| [#5799](https://github.com/openova-io/openova/issues/5799) | Funnel E2E walk is gated on retrieving the emailed sign-in PIN — no mail surfa | Other |
| [#5814](https://github.com/openova-io/openova/issues/5814) | Sovereign /apps grid carries no Organization attribution — a customer-launched | Other |
| [#5817](https://github.com/openova-io/openova/issues/5817) | Org detail page title-cases identifiers: slug `uatco` renders `Uatco`, owner `em | Other |
| [#5819](https://github.com/openova-io/openova/issues/5819) | Showback names the parent Organization `hw292.omani.works` while the directory a | Other |
| [#5821](https://github.com/openova-io/openova/issues/5821) | /version reports process START time as `buildTime` with no way to tell — hw292 | Other |
| [#5823](https://github.com/openova-io/openova/issues/5823) | Install wizard never pre-selects the Organization — even on a per-Org console  | Other |
| [#5825](https://github.com/openova-io/openova/issues/5825) | Two identity readers in one header disagree: the sidebar shows the signed-in own | Other |
| [#5827](https://github.com/openova-io/openova/issues/5827) | GET /applications/catalyst-api 404s while GET /applications/catalyst-api/placeme | Other |
| [#5829](https://github.com/openova-io/openova/issues/5829) | bp-openbao: a failed ACL-policy write is swallowed, then the OIDC role names a p | Other |
| [#5831](https://github.com/openova-io/openova/issues/5831) | Nine page components are reachable from nothing but their own tests — includin | Other |
| [#5833](https://github.com/openova-io/openova/issues/5833) | GET /api/v1/organizations double-counts an Organization — the store/CR merge k | Other |
| [#5835](https://github.com/openova-io/openova/issues/5835) | GET /applications/{name}/status 404s for every bootstrap component while its own | Other |
| [#5843](https://github.com/openova-io/openova/issues/5843) | bp-openova-mcp supports mode=organization but nothing ever instantiates a per-Or | Other |
| [#5847](https://github.com/openova-io/openova/issues/5847) | UAT row 5 asserts TIER=sme, a value the Organization CRD 422-rejects — the row | Other |
| [#5848](https://github.com/openova-io/openova/issues/5848) | Org delete leaves an orphaned HTTPRoute: teardown reaps by derived NAME while cr | Other |
| [#5857](https://github.com/openova-io/openova/issues/5857) | Door A stamps every customer Org isolation=vcluster while the GitOps renderer ba | Other |
| [#5867](https://github.com/openova-io/openova/issues/5867) | Two UAT rows need an owner decision and have no tracker issue: row 19 (grid keye | Other |
| [#5871](https://github.com/openova-io/openova/issues/5871) | hw292 live: 11 of 14 Applications are Degraded on a cutoverComplete=true Soverei | Other |
| [#5878](https://github.com/openova-io/openova/issues/5878) | newapi is the only SSO app that does not accept the realm session — bare URL 3 | Other |
| [#5882](https://github.com/openova-io/openova/issues/5882) | OpenBao SSO session lands with no token prompt but its policy cannot list mounts | Other |
| [#5883](https://github.com/openova-io/openova/issues/5883) | A mistyped email at the PIN form persists a Keycloak user — sovereign realm ho | Other |
| [#5887](https://github.com/openova-io/openova/issues/5887) | Console does not silently re-authenticate from a live Keycloak realm session — | Other |
| [#5894](https://github.com/openova-io/openova/issues/5894) | P1: per-Org consoles flap ~50% on TLS handshake reset — affects BOTH customer  | Other |
| [#5895](https://github.com/openova-io/openova/issues/5895) | P1: first visit to a per-Org console dead-ends on a BLANK page — prompt=none s | Other |
| [#5910](https://github.com/openova-io/openova/issues/5910) | Row 95: a purchased app silently vanishes — resolveAppSlugs substitutes the ra | Other |
| [#5919](https://github.com/openova-io/openova/issues/5919) | hw292 cut over on chart 0.1.159 — twelve versions before the #5710 durable piv | Other |
| [#5920](https://github.com/openova-io/openova/issues/5920) | Marketplace still sells 'Sandbox' — a concept retired 2026-06-30 — and a sou | Other |
| [#5921](https://github.com/openova-io/openova/issues/5921) | P0 sovereignty: post-cutover Sovereign sends customer sign-in mail via mail.open | Other |
| [#5926](https://github.com/openova-io/openova/issues/5926) | UAT row 85: checkout renders payment-method tiles + Stripe redirect copy on a fu | Other |
| [#5929](https://github.com/openova-io/openova/issues/5929) | UAT row 164: a terminal Failed Job produces no failed leaf — an unterminated l | Other |
| [#5932](https://github.com/openova-io/openova/issues/5932) | Dashboard treemap: the vCluster grouping dimension keys on the retired #4325 vcl | Other |
| [#5933](https://github.com/openova-io/openova/issues/5933) | Customer Application CRs never carry spec.organizationRef — the #5814 Org-attr | Other |
| [#5934](https://github.com/openova-io/openova/issues/5934) | TopologyTab hard-renders 'n/a — bootstrap component' regardless of the API ans | Other |
| [#5940](https://github.com/openova-io/openova/issues/5940) | Owner opening a voucher-redeem link is shown a signup form — the marketplace c | Other |
| [#5943](https://github.com/openova-io/openova/issues/5943) | Organization DELETE resolves a different identifier than GET — the console's o | Other |
| [#5945](https://github.com/openova-io/openova/issues/5945) | Placement editor offers retired vCluster names (host/mgmt/dmz/rtz) and preselect | Other |
| [#5955](https://github.com/openova-io/openova/issues/5955) | P1: Application reports Ready=True over a database with ZERO ready instances — | Other |
| [#5956](https://github.com/openova-io/openova/issues/5956) | P0: the Anthropic credential in openbao is REVOKED — every health surface repo | Other |

---

## 3. Recently merged PRs (last 36h)

| Merged | PR | Issue closed | Title |
|---|---|---|---|
| 2026-08-10T07:12 | [#5960](https://github.com/openova-io/openova/pull/5960) | #5508 | fix(dr): make the DR panel report what it measures — UAT row |
| 2026-08-10T07:01 | [#5958](https://github.com/openova-io/openova/pull/5958) | #4292 | fix(placement backend): the #4292 tier gate keys on planSlug |
| 2026-08-10T06:14 | [#5957](https://github.com/openova-io/openova/pull/5957) | #5246 | fix(org): per-Org console listeners land in EVERY region, an |
| 2026-08-10T05:54 | [#5954](https://github.com/openova-io/openova/pull/5954) | #5246 | docs(wbs): the structured plan to kill the 78 — partitioned  |
| 2026-08-10T05:54 | [#5953](https://github.com/openova-io/openova/pull/5953) | #3687 | chore(uat): land the convergence-capture script + record cyc |
| 2026-08-09T23:36 | [#5950](https://github.com/openova-io/openova/pull/5950) | #4277 | docs(uat): zero pending — 208 PASS + 78 FAIL = 286, every ro |
| 2026-08-09T23:22 | [#5949](https://github.com/openova-io/openova/pull/5949) | #3687 | docs(uat): no partials — every row is now PASS or FAIL, and  |
| 2026-08-09T22:51 | [#5947](https://github.com/openova-io/openova/pull/5947) | #5614 | docs(uat): final disposition — the SUPERSEDED class is EMPTY |
| 2026-08-09T22:51 | [#5946](https://github.com/openova-io/openova/pull/5946) | #5650 | fix(cutover): refuse a bp-self-sovereign-cutover pin below t |
| 2026-08-09T22:30 | [#5944](https://github.com/openova-io/openova/pull/5944) | #4325 | docs(uat): row 16 walked live — stale SUPERSEDED premise ref |
| 2026-08-09T22:25 | [#5942](https://github.com/openova-io/openova/pull/5942) | #5426 | docs(uat): row 107 walked live — delete cascade removes the  |
| 2026-08-09T22:11 | [#5941](https://github.com/openova-io/openova/pull/5941) | #5113 | docs(uat): row 156 walked end-to-end — Save's verdict is bac |
| 2026-08-09T22:07 | [#5939](https://github.com/openova-io/openova/pull/5939) | #4546 | docs(uat): formal disposition of all 13 SUPERSEDED rows — 3  |
| 2026-08-09T21:48 | [#5938](https://github.com/openova-io/openova/pull/5938) | #3383 | docs(uat): row 5 asserted a schema-forbidden value — correct |
| 2026-08-09T21:49 | [#5937](https://github.com/openova-io/openova/pull/5937) | #4325 | fix(dashboard): group the vCluster treemap layer on a runtim |
| 2026-08-09T20:59 | [#5936](https://github.com/openova-io/openova/pull/5936) | #5836 | fix(console): the Status panel must render a bootstrap compo |
| 2026-08-09T20:59 | [#5935](https://github.com/openova-io/openova/pull/5935) | #5814 | fix(catalyst-api): customer Application CRs must carry spec. |
| 2026-08-09T20:07 | [#5931](https://github.com/openova-io/openova/pull/5931) | #3925 | fix(jobs): an unterminated run must not bury a failed one (U |
| 2026-08-09T20:06 | [#5930](https://github.com/openova-io/openova/pull/5930) | #5246 | fix(catalyst-api): per-Org console listeners span every regi |
| 2026-08-09T21:18 | [#5928](https://github.com/openova-io/openova/pull/5928) | #5924 | docs(uat): 170 → 184 green on live re-walks; retire 13 dead  |
| 2026-08-09T20:07 | [#5927](https://github.com/openova-io/openova/pull/5927) | #3376 | fix(marketplace): hide payment chrome when credit covers the |
| 2026-08-09T19:43 | [#5925](https://github.com/openova-io/openova/pull/5925) | #5513 | fix(application-controller): stop crediting readiness to reg |
| 2026-08-09T18:24 | [#5924](https://github.com/openova-io/openova/pull/5924) | #5919 | docs(uat): per-case evidence audit of all 224 June-green cas |
| 2026-08-09T17:08 | [#5923](https://github.com/openova-io/openova/pull/5923) | docs(ledger): the plan to 100% — built on measured causes |  |
| 2026-08-09T17:03 | [#5922](https://github.com/openova-io/openova/pull/5922) | fix(ledger): restore test-case names in uat-raw.csv |  |
| 2026-08-09T16:59 | [#5918](https://github.com/openova-io/openova/pull/5918) | #3376 | docs(uat): row 92 — the redeem rate-limit fix exists, is pur |
| 2026-08-08T19:54 | [#5917](https://github.com/openova-io/openova/pull/5917) | #5848 | fix(org-controller): reap the per-Org wildcard TLS Secret on |
| 2026-08-08T16:47 | [#5916](https://github.com/openova-io/openova/pull/5916) | #3687 | docs(uat): rows 19 + 33 — backing data measured, both reduce |
| 2026-08-08T16:00 | [#5915](https://github.com/openova-io/openova/pull/5915) | #4459 | docs(uat): R17 — Org delete-cascade leaks two PLATFORM-names |
| 2026-08-08T15:30 | [#5914](https://github.com/openova-io/openova/pull/5914) | #5759 | docs(uat): row 231 walked live on hw292 — half (b) proven at |

---

## 4. Theater-incident log

Caught "fix shipped but actually broken" events + the validation principle that emerged:

| When | Broken PR | Caught by | Resolving PR | Principle codified |
|---|---|---|---|---|
| 2026-05-18 23:19Z | PR [#1875](https://github.com/openova-io/openova/pull/1875) — HR.dependsOn -> Kustomization silently ignored | t27 empirical walk | PR [#1879](https://github.com/openova-io/openova/pull/1879) | **#14** HR.dependsOn cross-kind |
| 2026-05-19 01:08Z | PR [#1892](https://github.com/openova-io/openova/pull/1892) — Python jsonencode != tofu HCL | t29 `tofu plan` failure | PR [#1894](https://github.com/openova-io/openova/pull/1894) | **#15** validate IaC with the IaC evaluator |
| 2026-05-19 02:00Z | PR [#1889](https://github.com/openova-io/openova/pull/1889) — 10 annotations on Gateway, CRD cap 8 | t30 SSA dry-run reject | PR [#1898](https://github.com/openova-io/openova/pull/1898) | (#15 applied) |

**Pattern**: every theater event was caught by an empirical fresh-prov walk, never by static review.

---

## 5. Resources

- [INVIOLABLE-PRINCIPLES](https://github.com/openova-io/openova/blob/main/docs/INVIOLABLE-PRINCIPLES.md) — 15 principles
- Manual refresh: `bash /home/openova/bin/refresh-dod-dashboard.sh`
- Cron: every 15 minutes
