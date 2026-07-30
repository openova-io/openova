# OpenOva — Status Tracker

Regenerated every 15 min by `/home/openova/bin/refresh-dod-dashboard.sh`. Every `#NNNN` is a clickable GitHub link.

|  |  |
|---|---|
| Last refreshed | `2026-07-30T05:00:07Z` |
| Deploy cron (#799) | ✓ deploy-cron healthy (image-reroll last ran 9m ago) |
| Open issues | 97 |
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

**All 97 open issues** grouped by where they sit in the convergence sequence. Each chain runs left-to-right; chains stack vertically.

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

### All 97 open items (clickable table)

| # | Title | Bucket |
|---|---|---|
| [#3969](https://github.com/openova-io/openova/issues/3969) | EPIC: Application-centric Placement (targets[] · Primary/Standby·Hot/Cold · c | Other |
| [#4111](https://github.com/openova-io/openova/issues/4111) | bp-agenity North Star (chat→provision, Pillar 4): mechanism merged+wired — B | Other |
| [#4212](https://github.com/openova-io/openova/issues/4212) | EPIC: ONE object-model/DR backbone — DR/spine half LIVE-Healthy (spine Applica | Other |
| [#4277](https://github.com/openova-io/openova/issues/4277) | FUNNEL/agenity: per-Org openbao anthropic/token auto-seed at Org-create (zero-to | Other |
| [#4639](https://github.com/openova-io/openova/issues/4639) | bp-dragonfly — P2P registry distribution: replace bastion proxy-cache + per-cl | Other |
| [#4901](https://github.com/openova-io/openova/issues/4901) | cnpg-pair Continuum doesn't surface standby-absent condition (health/lag stay gr | Other |
| [#5086](https://github.com/openova-io/openova/issues/5086) | Hetzner Sovereign DNS has no working front door since bp-powerdns 1.2.18 §854 f | Other |
| [#5088](https://github.com/openova-io/openova/issues/5088) | §854: 4 live NodePort Services on the mothership cluster (founder #1 ban) — s | Other |
| [#5265](https://github.com/openova-io/openova/issues/5265) | Post-cutover Day-2 gap: Gitea mirror re-syncs pin bumps but nothing syncs new ch | Other |
| [#5328](https://github.com/openova-io/openova/issues/5328) | Permanence: break-glass record-less env wipe — scripts/wipe-recordless-env.sh  | Other |
| [#5341](https://github.com/openova-io/openova/issues/5341) | openova-mcp /mcp endpoint: ~40% envoy edge 404 flake despite healthy replica — | Other |
| [#5348](https://github.com/openova-io/openova/issues/5348) | 🛑 §854: 3 live NodePort services on the mothership (cinova, iogrid, cert-man | Other |
| [#5349](https://github.com/openova-io/openova/issues/5349) | 🛑 §-compliance + durability: mothership runs entirely on local-path StorageC | Other |
| [#5358](https://github.com/openova-io/openova/issues/5358) | SSO: Guacamole intermittently rejects the valid Keycloak id_token (invalid/old n | Other |
| [#5364](https://github.com/openova-io/openova/issues/5364) | Org-CR deletion orphans the org Namespace + host-deployed bp-keycloak when per-O | Other |
| [#5370](https://github.com/openova-io/openova/issues/5370) | CI red on main: sandbox-mcp-server Go-toolchain latent break + intermittent char | Other |
| [#5373](https://github.com/openova-io/openova/issues/5373) | per-Org realm flag (CATALYST_PER_ORG_REALM_ENABLED) is DELIBERATELY dormant —  | Other |
| [#5385](https://github.com/openova-io/openova/issues/5385) | Deployment health aggregate is stale-degraded + counts suspended HRs inconsisten | Other |
| [#5388](https://github.com/openova-io/openova/issues/5388) | P0: region-kill failback leaves a DATA SPLIT-BRAIN — G12 leg 6/6 (legs 1-5 pas | Other |
| [#5389](https://github.com/openova-io/openova/issues/5389) | P1: per-app Open/launch button does not land the user in the app (rows 110/112/1 | Other |
| [#5391](https://github.com/openova-io/openova/issues/5391) | Cutover: a Stalled/RetriesExceeded per-Org HelmRelease permanently blocks the So | Other |
| [#5393](https://github.com/openova-io/openova/issues/5393) | Per-Org plan quota: vcluster control-plane overhead (1500m) is billed to the cus | Other |
| [#5394](https://github.com/openova-io/openova/issues/5394) | MCP endpoint is ~50% dead: bp-openova-mcp is single-region but mcp.<fqdn> fronts | Other |
| [#5395](https://github.com/openova-io/openova/issues/5395) | gamma-corp is entirely unrouted: all 6 per-Org HTTPRoutes NoMatchingListenerHost | Other |
| [#5396](https://github.com/openova-io/openova/issues/5396) | bp-velero upgrade-crds pre-upgrade hook is Kyverno-rejected (no harbor-proxy ima | Other |
| [#5397](https://github.com/openova-io/openova/issues/5397) | 9 catalog apps pinned to :latest are denied by the Sovereign's own Kyverno image | Other |
| [#5400](https://github.com/openova-io/openova/issues/5400) | SECURITY: per-Org bp-keycloak releases clobber the platform's OIDC pin-broker cr | Other |
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
| [#5453](https://github.com/openova-io/openova/issues/5453) | backing-services pod matcher hardcodes the inner vCluster namespace as 'apps' � | Other |
| [#5456](https://github.com/openova-io/openova/issues/5456) | UAT drift guard red on main: ledger H1 says 'pending hw289' while the live env i | Other |
| [#5459](https://github.com/openova-io/openova/issues/5459) | OpenBao SSO lands an authenticated-but-unauthorized session — /sys/internal/ui | Other |
| [#5460](https://github.com/openova-io/openova/issues/5460) | Console does not silently re-establish after session TTL — operator is re-prom | Other |
| [#5461](https://github.com/openova-io/openova/issues/5461) | Sovereign console intermittent 503 (envoy upstream connect timeout, ~17-25% prob | Other |
| [#5465](https://github.com/openova-io/openova/issues/5465) | Sovereign catalyst-api OOMKilled AT the post-#5352 4Gi limit, 63min into fresh h | Other |
| [#5466](https://github.com/openova-io/openova/issues/5466) | newapi SSO dead-ends: /api/oauth/sovereign code exchange returns 403 → no SSO  | Other |
| [#5467](https://github.com/openova-io/openova/issues/5467) | harbor-prewarm logs the first 8 chars of the GHCR PAT on every cutover — line  | Other |
| [#5468](https://github.com/openova-io/openova/issues/5468) | Cutover step-03 fails at the #5442 A3-guard on hw291: 4 chart-declared images un | Other |
| [#5471](https://github.com/openova-io/openova/issues/5471) | Cilium Gateways report Programmed=False / AddressNotAssigned while serving HTTP  | Other |
| [#5472](https://github.com/openova-io/openova/issues/5472) | Post-cutover sovereignty gap: 16 of 19 deployable marketplace apps resolve chart | Other |
| [#5475](https://github.com/openova-io/openova/issues/5475) | Catalog API contract: chartRef double bp- prefix, origin enum collision, hero mi | Other |
| [#5476](https://github.com/openova-io/openova/issues/5476) | Application -> Environment -> Organization chain does not resolve for half the e | Other |
| [#5477](https://github.com/openova-io/openova/issues/5477) | Continuum publishes replicationLagSeconds: 0 as a measurement when the standby w | Other |
| [#5480](https://github.com/openova-io/openova/issues/5480) | A16: per-region secret generation on a shared VIP — newapi SESSION_SECRET conf | Other |
| [#5482](https://github.com/openova-io/openova/issues/5482) | App detail Overview renders a host-cluster label as PRIMARY REGION — handler r | Other |
| [#5484](https://github.com/openova-io/openova/issues/5484) | Marketplace: redeem rate limiter fires at ~5x budget and penalises bystanders (t | Other |
| [#5485](https://github.com/openova-io/openova/issues/5485) | Observability surfaces inherit the wrong object: reconciler logs match by prefix | Other |
| [#5487](https://github.com/openova-io/openova/issues/5487) | Valkey is reachable and writable from every pod (rate-limit counters included);  | Other |
| [#5488](https://github.com/openova-io/openova/issues/5488) | Cutover aborts at secondary-kubeconfigs pre-flight after a catalyst-api restart: | Other |
| [#5489](https://github.com/openova-io/openova/issues/5489) | Four surfaces report a vCluster that does not exist (parent row hardcodes it, ku | Other |
| [#5491](https://github.com/openova-io/openova/issues/5491) | §854 NodePort prohibition is enforced in Audit mode — the policy records viol | Other |
| [#5496](https://github.com/openova-io/openova/issues/5496) | Catalog per-field save silently reverts the previous save — query invalidated  | Other |
| [#5499](https://github.com/openova-io/openova/issues/5499) | harbor-core crashloops on 28P01 blocking the cutover — region-b consumes a rol | Other |
| [#5500](https://github.com/openova-io/openova/issues/5500) | Application install returns HTTP 200 for a rejected install (body says 400) —  | Other |
| [#5501](https://github.com/openova-io/openova/issues/5501) | POST /api/v1/organizations reports state:done with all six steps done in 0s whil | Other |
| [#5502](https://github.com/openova-io/openova/issues/5502) | org-controller reports reason=VClusterProvisioning for host-tier Orgs that never | Other |
| [#5504](https://github.com/openova-io/openova/issues/5504) | bp-postgres: initdb owner and managed.roles mint the same role with different cr | Other |
| [#5505](https://github.com/openova-io/openova/issues/5505) | P0: all Namespace creates denied — #5494 flipped 10 policies to Enforce, expos | Other |
| [#5508](https://github.com/openova-io/openova/issues/5508) | Topology tab renders replication lag 0.0 s behind a green pill while the API rep | Other |
| [#5509](https://github.com/openova-io/openova/issues/5509) | Guacamole: postgresql-shared/self/effectivePermissions 403s deterministically fo | Other |
| [#5510](https://github.com/openova-io/openova/issues/5510) | Catalog card: a single-field Save silently reverts untouched sibling fields —  | Other |
| [#5511](https://github.com/openova-io/openova/issues/5511) | cilium-gateway-console silently drops both *.uatcorp listeners (8 declared, 6 in | Other |
| [#5512](https://github.com/openova-io/openova/issues/5512) | Marketplace redeem: forbidden-host guard evaded by string concatenation — moth | Other |
| [#5513](https://github.com/openova-io/openova/issues/5513) | active-hot-standby renders a 2-region pair over a singleton: empty openova.io/re | Other |
| [#5514](https://github.com/openova-io/openova/issues/5514) | P1: Switch over armed against a phantom standby — replication-status 200s with | Other |
| [#5515](https://github.com/openova-io/openova/issues/5515) | derivePattern fails open: empty target list renders as 'singleton', and runtime- | Other |
| [#5516](https://github.com/openova-io/openova/issues/5516) | openova-mcp: per-Org bearer carries no deployment_id claim — list/get/create_a | Other |
| [#5517](https://github.com/openova-io/openova/issues/5517) | check-no-nodeports guard was fail-open: no vacuity check, so SCAN_ROOTS drift or | Other |
| [#5520](https://github.com/openova-io/openova/issues/5520) | Committed blueprints.json + catalog.generated.ts are stale vs build-catalog.mjs  | Other |

---

## 3. Recently merged PRs (last 36h)

| Merged | PR | Issue closed | Title |
|---|---|---|---|
| 2026-07-29T20:48 | [#5498](https://github.com/openova-io/openova/pull/5498) | #4845 | fix(jobs): resolve controller-generated Job names so Re-run  |
| 2026-07-29T20:17 | [#5497](https://github.com/openova-io/openova/pull/5497) | #5449 | fix(catalog): invalidate the cache key the query is actually |
| 2026-07-29T19:05 | [#5495](https://github.com/openova-io/openova/pull/5495) | #5442 | fix(bp-self-sovereign-cutover): map cgr.dev + the litmuschao |
| 2026-07-29T18:56 | [#5494](https://github.com/openova-io/openova/pull/5494) | #5088 | fix(kyverno): enforce the §854 NodePort ban instead of merel |
| 2026-07-29T18:56 | [#5493](https://github.com/openova-io/openova/pull/5493) | #5485 | fix(showback,treemap): collapse one-shot Job rows, name Appl |
| 2026-07-29T18:27 | [#5492](https://github.com/openova-io/openova/pull/5492) | #2003 | fix(bp-valkey): scope NetworkPolicy ingress to the declared  |
| 2026-07-29T18:27 | [#5490](https://github.com/openova-io/openova/pull/5490) | #5489 | fix(console): stop asserting a vCluster and an Environment t |
| 2026-07-29T16:53 | [#5486](https://github.com/openova-io/openova/pull/5486) | #5485 | fix(catalyst-api): reconciler log matcher must respect the t |
| 2026-07-29T17:05 | [#5483](https://github.com/openova-io/openova/pull/5483) | #5482 | fix(catalyst-api): read primaryRegion from status.placement, |
| 2026-07-29T16:12 | [#5481](https://github.com/openova-io/openova/pull/5481) | #4975 | fix(cutover): mirror litellm-database single-platform past i |
| 2026-07-29T16:09 | [#5479](https://github.com/openova-io/openova/pull/5479) | #5449 | fix(catalog): chartRef double prefix, origin enum collision, |
| 2026-07-29T15:54 | [#5478](https://github.com/openova-io/openova/pull/5478) | #5477 | fix(continuum): never publish replicationLagSeconds that was |
| 2026-07-29T15:44 | [#5474](https://github.com/openova-io/openova/pull/5474) | #5473 | fix(bp-postgres): replication-source Service must select the |
| 2026-07-29T14:15 | [#5470](https://github.com/openova-io/openova/pull/5470) | #5442 | fix(catalog): unlist bp-nemo-guardrails — the image it pins  |
| 2026-07-29T14:01 | [#5469](https://github.com/openova-io/openova/pull/5469) | #5442 | fix(cutover): chart-image enumerator no longer greps its own |
| 2026-07-29T11:38 | [#5464](https://github.com/openova-io/openova/pull/5464) | #5460 | fix(console): revoke stale authed marker on 401 + run the si |
| 2026-07-29T10:49 | [#5463](https://github.com/openova-io/openova/pull/5463) | #5388 | fix(cnpg-pair): dr-failback surfaces peer-probe starvation o |
| 2026-07-29T10:14 | [#5462](https://github.com/openova-io/openova/pull/5462) | #5450 | test(gitops): render-time image-tag-pinned guard — KnownApps |
| 2026-07-29T09:52 | [#5458](https://github.com/openova-io/openova/pull/5458) | #5353 | docs(uat): re-walk row 40 live on hw290 after the #5430 flip |
| 2026-07-27T11:41 | [#5457](https://github.com/openova-io/openova/pull/5457) | #5449 | fix(console): catalog detail hero reads the live CR, not the |
| 2026-07-27T11:27 | [#5455](https://github.com/openova-io/openova/pull/5455) | #960 | fix(uat): count UAT rows by status column — ad-hoc tally ove |
| 2026-07-27T10:54 | [#5454](https://github.com/openova-io/openova/pull/5454) | #4290 | fix(provisioning): match vCluster pods for any inner namespa |
| 2026-07-27T10:45 | [#5452](https://github.com/openova-io/openova/pull/5452) | #5445 | fix(console): badge apps NOT SERVING when the workload is de |
| 2026-07-27T10:31 | [#5448](https://github.com/openova-io/openova/pull/5448) | #5443 | fix(cutover): harbor-prewarm mirrors the marketplace's chart |
| 2026-07-27T10:15 | [#5447](https://github.com/openova-io/openova/pull/5447) | #5397 | fix(catalog): pin 4 registry-verified floating image tags th |
| 2026-07-27T10:15 | [#5446](https://github.com/openova-io/openova/pull/5446) | #5445 | fix(provisioning): mount the per-Org postgres PVC at a subPa |
| 2026-07-27T10:16 | [#5441](https://github.com/openova-io/openova/pull/5441) | #5237 | fix(cutover): re-take the gitea-mirror snapshot on a new att |
| 2026-07-27T10:16 | [#5438](https://github.com/openova-io/openova/pull/5438) | #5359 | fix(cutover): step-06 reads back what it pivoted instead of  |
| 2026-07-27T10:16 | [#5432](https://github.com/openova-io/openova/pull/5432) | #3370 | fix(console): one card per Application CR — suppress fanned- |
| 2026-07-27T10:16 | [#5431](https://github.com/openova-io/openova/pull/5431) | #4384 | fix(catalyst-api): gate the legacy org-tenants overlay write |

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
