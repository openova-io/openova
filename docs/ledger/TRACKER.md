# OpenOva — Status Tracker

Regenerated every 15 min by `/home/openova/bin/refresh-dod-dashboard.sh`. Every `#NNNN` is a clickable GitHub link.

|  |  |
|---|---|
| Last refreshed | `2026-08-05T23:15:07Z` |
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
| [#5394](https://github.com/openova-io/openova/issues/5394) | MCP endpoint is ~50% dead: bp-openova-mcp is single-region but mcp.<fqdn> fronts | Other |
| [#5395](https://github.com/openova-io/openova/issues/5395) | gamma-corp is entirely unrouted: all 6 per-Org HTTPRoutes NoMatchingListenerHost | Other |
| [#5396](https://github.com/openova-io/openova/issues/5396) | bp-velero upgrade-crds pre-upgrade hook is Kyverno-rejected (no harbor-proxy ima | Other |
| [#5401](https://github.com/openova-io/openova/issues/5401) | SECURITY: per-Org console /settings renders the Sovereign operator panel to an o | Other |
| [#5404](https://github.com/openova-io/openova/issues/5404) | Operator-console vitest suite is red on main (68 failed / 1876 passed across 9 f | Other |
| [#5406](https://github.com/openova-io/openova/issues/5406) | Harbor SSO session never holds: active/active across both regions behind one hos | Other |
| [#5408](https://github.com/openova-io/openova/issues/5408) | Wizard component model: phantom ids in the deployment payload, cnpg+postgres inv | Other |
| [#5409](https://github.com/openova-io/openova/issues/5409) | PinInput6 maxLength={6} silently truncates a pasted sign-in code before the past | Other |
| [#5410](https://github.com/openova-io/openova/issues/5410) | uptime-kuma is hard-coded to 128Mi in KnownApps and OOMKills permanently (40 res | Other |
| [#5414](https://github.com/openova-io/openova/issues/5414) | newapi loses sessions ~60% of loads: each region mints its own SESSION_SECRET/CR | Other |
| [#5416](https://github.com/openova-io/openova/issues/5416) | oidc-gate mints its cookie secret per region, so each gate rejects the peer's se | Other |
| [#5419](https://github.com/openova-io/openova/issues/5419) | postgres chart: wizard instances collapse onto one Cluster/postgres (_helpers.tp | Other |
| [#5420](https://github.com/openova-io/openova/issues/5420) | Topology tab renders declared placement, not effective perCluster — shows 2 ca | Other |
| [#5421](https://github.com/openova-io/openova/issues/5421) | Marketplace redeem page misses cookie-borne owner sessions — localStorage prob | Other |
| [#5422](https://github.com/openova-io/openova/issues/5422) | Console Overview hardcodes Placement fallback to 'singleton', contradicting the  | Other |
| [#5423](https://github.com/openova-io/openova/issues/5423) | P0: vcluster-tier funnel cart with a HelmRelease-shaped app poisons the whole pe | Other |
| [#5425](https://github.com/openova-io/openova/issues/5425) | WriteTenantOverlay writes the legacy org-tenants path on EVERY Org create — un | Other |
| [#5426](https://github.com/openova-io/openova/issues/5426) | Organization delete never runs the finalizer cascade — the complete teardown h | Other |
| [#5427](https://github.com/openova-io/openova/issues/5427) | bp-velero stalled MissingRollbackTarget after Kyverno denied its upgrade-crds pr | Other |
| [#5429](https://github.com/openova-io/openova/issues/5429) | Apps grid renders two cards per Application CR when spec.helmRelease.name is uns | Other |
| [#5433](https://github.com/openova-io/openova/issues/5433) | bp-postgres defaults the CNPG Cluster name to a bare 'postgres', so multiple rel | Other |
| [#5434](https://github.com/openova-io/openova/issues/5434) | App-detail Dependencies never names the producing instance — backend reads onl | Other |
| [#5435](https://github.com/openova-io/openova/issues/5435) | Banned term 'tenant' reaches the console from a Deployment literally named tenan | Other |
| [#5436](https://github.com/openova-io/openova/issues/5436) | Cutover step-06 exits success while 6 HelmRepositories remain on ghcr — its ow | Other |
| [#5437](https://github.com/openova-io/openova/issues/5437) | P0: cutover re-attempt skips step-01 gitea-mirror on stale success, pivoting Flu | Other |
| [#5439](https://github.com/openova-io/openova/issues/5439) | catalyst-api re-tethers a cut-over Sovereign: orgTenantSharedHelmRepositories ha | Other |
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
| [#5500](https://github.com/openova-io/openova/issues/5500) | Application install returns HTTP 200 for a rejected install (body says 400) —  | Other |
| [#5501](https://github.com/openova-io/openova/issues/5501) | POST /api/v1/organizations reports state:done with all six steps done in 0s whil | Other |
| [#5502](https://github.com/openova-io/openova/issues/5502) | org-controller reports reason=VClusterProvisioning for host-tier Orgs that never | Other |
| [#5504](https://github.com/openova-io/openova/issues/5504) | bp-postgres: initdb owner and managed.roles mint the same role with different cr | Other |
| [#5505](https://github.com/openova-io/openova/issues/5505) | P0: all Namespace creates denied — #5494 flipped 10 policies to Enforce, expos | Other |
| [#5508](https://github.com/openova-io/openova/issues/5508) | Topology tab renders replication lag 0.0 s behind a green pill while the API rep | Other |
| [#5510](https://github.com/openova-io/openova/issues/5510) | Catalog card: a single-field Save silently reverts untouched sibling fields —  | Other |
| [#5513](https://github.com/openova-io/openova/issues/5513) | active-hot-standby renders a 2-region pair over a singleton: empty openova.io/re | Other |
| [#5514](https://github.com/openova-io/openova/issues/5514) | P1: Switch over armed against a phantom standby — replication-status 200s with | Other |
| [#5515](https://github.com/openova-io/openova/issues/5515) | derivePattern fails open: empty target list renders as 'singleton', and runtime- | Other |
| [#5516](https://github.com/openova-io/openova/issues/5516) | openova-mcp: per-Org bearer carries no deployment_id claim — list/get/create_a | Other |
| [#5527](https://github.com/openova-io/openova/issues/5527) | Cutover: per-Org tenant HelmRepositories outside the pivot's authority — org-t | Other |
| [#5558](https://github.com/openova-io/openova/issues/5558) | Mothership catalyst-api stuck at replicas=0 — Flux does not own spec.replicas, | Other |
| [#5559](https://github.com/openova-io/openova/issues/5559) | catalog-seed ships 6 INERT Blueprint CRs — seeded against charts that were nev | Other |
| [#5561](https://github.com/openova-io/openova/issues/5561) | §854 solver carve-out assumes HTTP-01 solvers 'GC automatically' — 5 have per | Other |
| [#5567](https://github.com/openova-io/openova/issues/5567) | Mothership has NO Kyverno but 7 orphaned webhooks remain — 5 fail-closed, bloc | Other |
| [#5568](https://github.com/openova-io/openova/issues/5568) | derivedFromRuntime is a constant, not a derivation — hardcoded true even when  | Other |
| [#5571](https://github.com/openova-io/openova/issues/5571) | /k8s/stream serves ONE region as the whole estate — Cloud NetworkPolicy pages  | Other |
| [#5573](https://github.com/openova-io/openova/issues/5573) | Mothership GitOps loop is DEAD — all 4 Flux controllers at replicas=0 + flux-s | Other |
| [#5575](https://github.com/openova-io/openova/issues/5575) | Wizard component catalog is unvalidated: 6 components have no Blueprint, 8 carry | Other |
| [#5583](https://github.com/openova-io/openova/issues/5583) | deploy-bot chart bumps cover 2 of 5 lockstep sites — every bump turns main red | Other |
| [#5591](https://github.com/openova-io/openova/issues/5591) | phase2b bootstrapMode->false flip is primary-region-only: secondary regions keep | Other |
| [#5596](https://github.com/openova-io/openova/issues/5596) | cutoverComplete=true certifies sovereignty while region-B keeps 64/65 HelmReposi | Other |
| [#5597](https://github.com/openova-io/openova/issues/5597) | openova-uat-bot erases verified UAT rows on a Co-authored-by trailer substring m | Other |
| [#5598](https://github.com/openova-io/openova/issues/5598) | Guacamole: SSO lands but every session API call returns 403 PERMISSION_DENIED � | Other |
| [#5599](https://github.com/openova-io/openova/issues/5599) | newapi: valid Keycloak authz code rejected — /api/oauth/sovereign returns 403  | Other |
| [#5600](https://github.com/openova-io/openova/issues/5600) | Post-cutover Sovereign falsely reads Degraded: secondaryDegraded counts suspende | Other |
| [#5601](https://github.com/openova-io/openova/issues/5601) | DR Switchover button disabled with 'no caught-up standby' while Continuum report | Other |
| [#5602](https://github.com/openova-io/openova/issues/5602) | Hubble UI: authenticated landing works but data streams never connect (persisten | Other |
| [#5608](https://github.com/openova-io/openova/issues/5608) | Keycloak admin console: group Role-mapping tab crashes ('Cannot read properties  | Other |
| [#5609](https://github.com/openova-io/openova/issues/5609) | active-passive topology is never selectable: all 24 blueprints declaring it have | Other |
| [#5610](https://github.com/openova-io/openova/issues/5610) | Catalog edit surfaces: summary inline editor opens empty (wipe hazard) + YamlEdi | Other |
| [#5611](https://github.com/openova-io/openova/issues/5611) | Cloud list: Volumes claims 0 while 50 EVS block volumes are attached (Storage Cl | Other |
| [#5612](https://github.com/openova-io/openova/issues/5612) | newapi session expires in under an hour and the 'Continue with OpenOva SSO' reco | Other |
| [#5613](https://github.com/openova-io/openova/issues/5613) | Dashboard treemap Organization layer is inert: /api/v1/fleet/treemap returns one | Other |
| [#5614](https://github.com/openova-io/openova/issues/5614) | Sovereign rejects its OWN handover token: auth_handover.go hardcodes expectedIss | Other |
| [#5616](https://github.com/openova-io/openova/issues/5616) | Application-create vCluster selector: all 5 options are consumed as a namespace  | Other |
| [#5617](https://github.com/openova-io/openova/issues/5617) | Per-Org oidc-gate has a gateway-ingress CNP but no egress policy — under names | Other |
| [#5623](https://github.com/openova-io/openova/issues/5623) | Region-kill: the three shared-pg DR pairs never promote — only bp-cnpg-pair sh | Other |
| [#5634](https://github.com/openova-io/openova/issues/5634) | UAT row 92: funnel discards the 429 rate-limit response — a throttled customer | Other |
| [#5635](https://github.com/openova-io/openova/issues/5635) | Per-Org app FQDN fails ~50% of fresh connections — single-region namespace beh | Other |
| [#5637](https://github.com/openova-io/openova/issues/5637) | Cilium node encryption is not uniform — the region-A control-plane agent repor | Other |
| [#5639](https://github.com/openova-io/openova/issues/5639) | Per-Org bp-postgres active-hot-standby renders an empty region selector — pod  | Other |
| [#5640](https://github.com/openova-io/openova/issues/5640) | Post-cutover Sovereigns cannot receive newly published images — local Harbor i | Other |
| [#5642](https://github.com/openova-io/openova/issues/5642) | catalyst-api OOMKilling in a loop on hw292 (15 restarts, requests 96Mi vs limits | Other |
| [#5646](https://github.com/openova-io/openova/issues/5646) | Customer-facing provisioning timeline: banned term 'Creating tenant', steps comp | Other |
| [#5649](https://github.com/openova-io/openova/issues/5649) | Org teardown reaps one producer's route name in one region — deleted Orgs leav | Other |
| [#5650](https://github.com/openova-io/openova/issues/5650) | Post-cutover Sovereign fetches charts.loft.sh every 15m: step-08's deny-egress h | Other |
| [#5661](https://github.com/openova-io/openova/issues/5661) | Sovereign wizard cannot fire a single-region prov — Single-region topology cho | Other |
| [#5690](https://github.com/openova-io/openova/issues/5690) | §854/#5348: bp-stalwart-sovereign mail LoadBalancer Service omits explicit node | Other |
| [#5693](https://github.com/openova-io/openova/issues/5693) | §854 guard gap: no check catches a LoadBalancer Service that OMITS explicit nod | Other |

---

## 3. Recently merged PRs (last 36h)

| Merged | PR | Issue closed | Title |
|---|---|---|---|
| 2026-08-05T23:08 | [#5711](https://github.com/openova-io/openova/pull/5711) | #5614 | fix(auth): accept the handover issuer SET, not a single valu |
| 2026-08-05T23:07 | [#5710](https://github.com/openova-io/openova/pull/5710) | #5436 | fix(cutover): step-06 secondary leg must prove the pivot DUR |
| 2026-08-05T22:58 | [#5709](https://github.com/openova-io/openova/pull/5709) | #5597 | fix(uat): never flip a UAT row whose walk post-dates the cit |
| 2026-08-05T22:04 | [#5708](https://github.com/openova-io/openova/pull/5708) | #5609 | fix(console): pin the active-passive topology gate on the re |
| 2026-08-05T22:00 | [#5707](https://github.com/openova-io/openova/pull/5707) | #5599 | fix(bp-newapi): cross-region singleton — one newapi, one Pos |
| 2026-08-05T21:00 | [#5706](https://github.com/openova-io/openova/pull/5706) | #5705 | fix(crd): reject unimplemented hostnameTemplate tokens at ad |
| 2026-08-05T20:21 | [#5705](https://github.com/openova-io/openova/pull/5705) | #5389 | fix(endpoints): guard every hostnameTemplate against unimple |
| 2026-08-05T20:22 | [#5704](https://github.com/openova-io/openova/pull/5704) | #5602 | fix(#5602): hubble UI 400 — ONE Hubble UI per Sovereign over |
| 2026-08-05T20:36 | [#5703](https://github.com/openova-io/openova/pull/5703) | #4975 | feat(cutover): close the head of the post-cutover delivery c |
| 2026-08-05T20:17 | [#5702](https://github.com/openova-io/openova/pull/5702) | #5389 | fix(lockstep): compare endpoints[].hostnameTemplate seed-vs- |
| 2026-08-05T20:29 | [#5701](https://github.com/openova-io/openova/pull/5701) | #5600 | fix(catalyst-api): re-derive the per-region HelmRelease cens |
| 2026-08-05T19:12 | [#5700](https://github.com/openova-io/openova/pull/5700) | #5389 | docs(mandate): a denied tool call is ONE COMMAND, never a bl |
| 2026-08-05T19:32 | [#5699](https://github.com/openova-io/openova/pull/5699) | #4 | fix(endpoints): add {OrgDomain} token so per-Org Open resolv |
| 2026-08-05T19:09 | [#5698](https://github.com/openova-io/openova/pull/5698) | #4325 | docs(uat): live-anchor the 11 superseded placement rows; rec |
| 2026-08-05T19:09 | [#5697](https://github.com/openova-io/openova/pull/5697) | #5378 | fix(#5358): guacamole blank page — ONE Guacamole per Soverei |
| 2026-08-05T18:05 | [#5696](https://github.com/openova-io/openova/pull/5696) | #5592 | fix(854): assert NodePort ban is ENFORCING per region, not j |
| 2026-08-05T14:51 | [#5695](https://github.com/openova-io/openova/pull/5695) | #5575 | test(wizard): fail-closed catalog integrity gate — no phanto |
| 2026-08-05T14:30 | [#5694](https://github.com/openova-io/openova/pull/5694) | #5348 | ci(§854): guard that every LoadBalancer Service template sta |
| 2026-08-05T13:53 | [#5692](https://github.com/openova-io/openova/pull/5692) | #5535 | ci(uat): lock the SSO-flip trigger surface against evidence- |
| 2026-08-05T13:44 | [#5691](https://github.com/openova-io/openova/pull/5691) | #5348 | fix(stalwart-sovereign): explicit nodePort:0 on mail LoadBal |
| 2026-08-05T07:14 | [#5689](https://github.com/openova-io/openova/pull/5689) | #5612 | fix(newapi): route inert SSO recovery button through the wor |
| 2026-08-05T06:35 | [#5688](https://github.com/openova-io/openova/pull/5688) | #5613 | fix(dashboard): treemap Organization layer projects live pod |
| 2026-08-05T06:31 | [#5687](https://github.com/openova-io/openova/pull/5687) | #5571 | fix(catalyst-api): chroot secondary regions no longer drop o |
| 2026-08-05T05:50 | [#5686](https://github.com/openova-io/openova/pull/5686) | #5421 | fix(marketplace): redeem consults the cookie session, not th |
| 2026-08-05T05:51 | [#5685](https://github.com/openova-io/openova/pull/5685) | #5611 | fix(cloud): Volumes page reads live PVs, not a hardcoded emp |
| 2026-08-05T05:11 | [#5684](https://github.com/openova-io/openova/pull/5684) | #5610 | fix(console): clear catalog IaC editor 'unsaved changes' aft |
| 2026-08-05T02:53 | [#5683](https://github.com/openova-io/openova/pull/5683) | #5568 | fix(placement): derivedFromRuntime false on the no-data-plan |
| 2026-08-05T01:50 | [#5682](https://github.com/openova-io/openova/pull/5682) | #5610 | fix(console): summary inline editor opens pre-filled with th |
| 2026-08-05T01:46 | [#5681](https://github.com/openova-io/openova/pull/5681) | #5614 | fix(auth): verify handover issuer via DefaultIssuer(), not a |
| 2026-08-05T02:09 | [#5680](https://github.com/openova-io/openova/pull/5680) | #5467 | fix(cutover): stop harbor-prewarm printing the GHCR PAT pref |

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
