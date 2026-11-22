# OpenOva — Status Tracker

Regenerated every 15 min by `/home/openova/bin/refresh-dod-dashboard.sh`. Every `#NNNN` is a clickable GitHub link.

|  |  |
|---|---|
| Last refreshed | `2026-11-22T23:15:03Z` |
| Deploy cron (#799) | ✓ deploy-cron healthy (image-reroll last ran 10m ago) |
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
| [#8232](https://github.com/openova-io/openova/issues/8232) | FND-156 The conformance account exists on every Sovereign | Other |
| [#8235](https://github.com/openova-io/openova/issues/8235) | FND-207 A check stays green on the newest valid pair until a newer record says o | Other |
| [#8238](https://github.com/openova-io/openova/issues/8238) | FND-079 The gate has a scoped access to the parent | Other |
| [#8244](https://github.com/openova-io/openova/issues/8244) | CLD-111 Wipe and janitor through capabilities | Other |
| [#8247](https://github.com/openova-io/openova/issues/8247) | FND-078 The gate's cluster jobs run as a Job on the parent | Other |
| [#8254](https://github.com/openova-io/openova/issues/8254) | INF-512 Runner test task: arguments, target check and Nuclei (profile network-pa | Other |
| [#8256](https://github.com/openova-io/openova/issues/8256) | CLD-065 Crossplane 1.20 to 2.4 | Other |
| [#8257](https://github.com/openova-io/openova/issues/8257) | SVC-163 The Blueprint files carry the seed's configSchema | Other |
| [#8264](https://github.com/openova-io/openova/issues/8264) | CLD-066 provider-opentofu v1.1.11 by digest | Other |
| [#8265](https://github.com/openova-io/openova/issues/8265) | INF-513 Runner test task: the ZAP baseline scan and its SARIF report (profile we | Other |
| [#8270](https://github.com/openova-io/openova/issues/8270) | SVC-160 One generator, part 2: the seed becomes generated | Other |
| [#8273](https://github.com/openova-io/openova/issues/8273) | SVC-161 The seed sync script is deleted with its three importers and four order  | Other |
| [#8277](https://github.com/openova-io/openova/issues/8277) | SVC-162 Shelf deletion: README-only folders and scaffold Blueprint files | Other |
| [#8280](https://github.com/openova-io/openova/issues/8280) | AIP-030 Spike: agent runtime proof (kagent 1.x on k3s, Cilium, vcluster) | Other |
| [#8284](https://github.com/openova-io/openova/issues/8284) | SVC-170 Contract C1: class and lifecycle on every Blueprint, with their readers | Other |
| [#8287](https://github.com/openova-io/openova/issues/8287) | BSS-S1 Spike: suite sign-in, suite access to the engine API, and the price list  | Other |
| [#8292](https://github.com/openova-io/openova/issues/8292) | INF-710 Spike: the host security service on the operator-hosted stack | Other |
| [#8293](https://github.com/openova-io/openova/issues/8293) | NET-270 Spike: firewall attachment | Other |
| [#8298](https://github.com/openova-io/openova/issues/8298) | SVC-250 Spike: composite resources in a release; bucket provisioning for an Orga | Other |
| [#8302](https://github.com/openova-io/openova/issues/8302) | PRD-061 Spike: vector search with the readable principals on the chunk row | Other |
| [#8306](https://github.com/openova-io/openova/issues/8306) | NET-251 Decision record: firewall mechanism and the package list of epic NET-250 | Other |
| [#8311](https://github.com/openova-io/openova/issues/8311) | AIP-201 Node preparation for the sandbox class, exactly as the spike recorded | Other |
| [#8312](https://github.com/openova-io/openova/issues/8312) | BSS-060 Gate check: self-service onboarding | Other |
| [#8313](https://github.com/openova-io/openova/issues/8313) | SOV-321 Spike: the Sovereign's own mail server on a running Sovereign | Other |
| [#8316](https://github.com/openova-io/openova/issues/8316) | INF-020 Spike: hardware virtualisation on worker nodes | Other |
| [#8317](https://github.com/openova-io/openova/issues/8317) | SOV-521 The release route promotes one region per request and rolls a region bac | Other |
| [#8320](https://github.com/openova-io/openova/issues/8320) | PRD-202 cortex store: migrations, roles, trimmed query | Other |
| [#8326](https://github.com/openova-io/openova/issues/8326) | AIP-031 Spike: one sandbox substrate shared by Organizations, with grants scoped | Other |
| [#8334](https://github.com/openova-io/openova/issues/8334) | PRD-203 cortex query process: token check, gateway client, search, definitions l | Other |
| [#8341](https://github.com/openova-io/openova/issues/8341) | NET-255 Journey WAF, its corpus and gate check NET-G15 as expected-fail | Other |
| [#8346](https://github.com/openova-io/openova/issues/8346) | NET-330 Spike: a bridged secondary network for virtual machines (conditional, dr | Other |
| [#8352](https://github.com/openova-io/openova/issues/8352) | AIP-223 Judge dispatcher: lanes, deadlines, agent tokens, A2A call, validation,  | Other |
| [#8355](https://github.com/openova-io/openova/issues/8355) | NET-280 Spike: overlay profile | Other |
| [#8356](https://github.com/openova-io/openova/issues/8356) | SVC-220 Ring labels on the kit | Other |
| [#8358](https://github.com/openova-io/openova/issues/8358) | FND-076 The standing environment is updated through the release route by a workf | Other |
| [#8359](https://github.com/openova-io/openova/issues/8359) | INF-210 Spike: composite resource in an Organization's release, changed placemen | Other |
| [#8363](https://github.com/openova-io/openova/issues/8363) | AIP-151 Tool answers are fitted and made inert | Other |
| [#8364](https://github.com/openova-io/openova/issues/8364) | AIP-250 Spike: agent workspace on the Organization's own sign-in with a short-li | Other |
| [#8365](https://github.com/openova-io/openova/issues/8365) | SOV-154 First boot applies one root Kustomization; its children come from Git | Other |
| [#8368](https://github.com/openova-io/openova/issues/8368) | SOV-322 Sign-in mail from the Sovereign's own server: kit slot, settings, and th | Other |
| [#8385](https://github.com/openova-io/openova/issues/8385) | SEC-152 Shared template writes the seal key files and the seal profile; seal vol | Other |
| [#8386](https://github.com/openova-io/openova/issues/8386) | SVC-164 Delete the OpenMeter Blueprint | Other |
| [#8389](https://github.com/openova-io/openova/issues/8389) | SOV-323 Relay mode: sign-in mail through a relay with a credential for that chil | Other |
| [#8391](https://github.com/openova-io/openova/issues/8391) | AIP-225 MCP endpoint and console HTTP routes inside the service | Other |
| [#8396](https://github.com/openova-io/openova/issues/8396) | FND-157 A gate run reads the storefront's sign-in code from a mailbox on the Sov | Other |
| [#8398](https://github.com/openova-io/openova/issues/8398) | PRD-204 cortex ingestion of uploads: job table, upload and document routes, inde | Other |
| [#8402](https://github.com/openova-io/openova/issues/8402) | SOV-149 The image pre-pull leaves first boot | Other |
| [#8410](https://github.com/openova-io/openova/issues/8410) | AIP-130 Model contract conformance client | Other |
| [#8414](https://github.com/openova-io/openova/issues/8414) | SOV-341 The kubeconfig route serves the primary region's file that the parent ho | Other |
| [#8415](https://github.com/openova-io/openova/issues/8415) | SVC-166 The umbrella chart's Blueprint file moves beside its chart | Other |
| [#8418](https://github.com/openova-io/openova/issues/8418) | SOV-522 The release stage applies the provider pivot again where a candidate cha | Other |
| [#8423](https://github.com/openova-io/openova/issues/8423) | AIP-131 Model checks AIP-G02 to AIP-G05 as expected-fail | Other |
| [#8426](https://github.com/openova-io/openova/issues/8426) | CLD-133 Secondary-region suspend keys are set for every provider | Other |
| [#8427](https://github.com/openova-io/openova/issues/8427) | NET-252 Lab test: HTTPS through the front listener, and cilium-envoy memory with | Other |
| [#8433](https://github.com/openova-io/openova/issues/8433) | SOV-132 Nodes boot with a local-first registry file, a wildcard entry and the pu | Other |
| [#8436](https://github.com/openova-io/openova/issues/8436) | SVC-205 Lock stage blueprints to deny | Other |
| [#8441](https://github.com/openova-io/openova/issues/8441) | AIP-070 Delete the hollow gateway charts | Other |
| [#8445](https://github.com/openova-io/openova/issues/8445) | AIP-227 Console read path: reverse proxy /api/v1/ops/* in catalyst-api | Other |
| [#8446](https://github.com/openova-io/openova/issues/8446) | PRD-211 Document conversion parser | Other |
| [#8449](https://github.com/openova-io/openova/issues/8449) | SVC-136 The foundation function tests pass on a fresh installation | Other |
| [#8452](https://github.com/openova-io/openova/issues/8452) | SOV-242 The cutover repeats a failed step a few times with a growing pause, then | Other |
| [#8456](https://github.com/openova-io/openova/issues/8456) | SEC-162 OpenBao 2.7.2, static seal, self-initialisation and declared audit devic | Other |
| [#8466](https://github.com/openova-io/openova/issues/8466) | PRD-205 Assistants: models list, answers with citations, residency refusal | Other |
| [#8472](https://github.com/openova-io/openova/issues/8472) | PRD-206 cortex screens | Other |
| [#8475](https://github.com/openova-io/openova/issues/8475) | SEC-153 Flux renders bootstrap Secrets from one seed: robot token, pool-domain l | Other |
| [#8483](https://github.com/openova-io/openova/issues/8483) | NET-213 The console states the region separation | Other |
| [#8487](https://github.com/openova-io/openova/issues/8487) | SOV-162 The registry finds and heals a cached image index whose children are gon | Other |
| [#8490](https://github.com/openova-io/openova/issues/8490) | SOV-523 A delivery of the release route resumes after one eviction of any of its | Other |
| [#8498](https://github.com/openova-io/openova/issues/8498) | SEC-154 Flux renders bootstrap Secrets from one seed: pull login, object storage | Other |
| [#8500](https://github.com/openova-io/openova/issues/8500) | AIP-152 The tool server reads its bearer from a file at every call | Other |
| [#8501](https://github.com/openova-io/openova/issues/8501) | AIP-153 The token route admits the workspace of each Organization for its own Or | Other |
| [#8509](https://github.com/openova-io/openova/issues/8509) | AIP-154 An agent token acts for a person of tier admin, for create_application o | Other |
| [#8517](https://github.com/openova-io/openova/issues/8517) | AIP-155 Agent workspace: the Organization's own sign-in by mount and a short-liv | Other |
| [#8528](https://github.com/openova-io/openova/issues/8528) | SVC-137 The function tests wait for their subject in their own region within the | Other |
| [#8532](https://github.com/openova-io/openova/issues/8532) | SOV-136 Nodes share the images they hold: the k3s embedded registry mirror is on | Other |
| [#8533](https://github.com/openova-io/openova/issues/8533) | SOV-137 The registry pivot keeps the nodes' shared mirror first and starts with  | Other |
| [#8534](https://github.com/openova-io/openova/issues/8534) | SOV-342 The provider pod installs OpenTofu providers only from an OCI mirror of  | Other |
| [#8536](https://github.com/openova-io/openova/issues/8536) | INF-056 Check INF-G01 restarts the node of the registry's front and requires eve | Other |
| [#8538](https://github.com/openova-io/openova/issues/8538) | SOV-138 No single Pod on the registry's pull path: Harbor's front, core and regi | Other |
| [#8547](https://github.com/openova-io/openova/issues/8547) | The harbor chart's admin-secret test cannot fail on a broken pipe: six printf |  | Other |
| [#8551](https://github.com/openova-io/openova/issues/8551) | SVC-139 The function tests of bp-openbao, bp-reloader and bp-valkey wait for the | Other |
| [#8556](https://github.com/openova-io/openova/issues/8556) | SOV-343 The cutover copies the OpenTofu provider packages into the Sovereign and | Other |
| [#8557](https://github.com/openova-io/openova/issues/8557) | SVC-138 Every foundation release has a function test that check SVC-G06 can read | Other |
| [#8560](https://github.com/openova-io/openova/issues/8560) | CLD-116 The wipe removes first what its own destroy cannot remove on the referen | Other |
| [#8564](https://github.com/openova-io/openova/issues/8564) | SOV-155 A roll of the registry never removes both copies: harbor-registry rolls  | Other |
| [#8569](https://github.com/openova-io/openova/issues/8569) | SOV-344 The sovereignty proof counts the cloud Workspaces, and the tether list n | Other |
| [#8574](https://github.com/openova-io/openova/issues/8574) | SOV-139 A cloud-free harness drains the registry's node with the outside blocked | Other |
| [#8577](https://github.com/openova-io/openova/issues/8577) | NET-103 Gate check NET-G18: full-size transfers between two workers over the nod | Other |
| [#8578](https://github.com/openova-io/openova/issues/8578) | NET-121 A cloud-free test moves full-size packets between two workers through th | Other |
| [#8586](https://github.com/openova-io/openova/issues/8586) | INF-057 Check INF-G01 requires every moved workload back within 180 seconds of l | Other |
| [#8594](https://github.com/openova-io/openova/issues/8594) | NET-185 The Sovereign's name servers answer on public port 53, over UDP and TCP, | Other |
| [#8601](https://github.com/openova-io/openova/issues/8601) | INF-054 The node maintenance window is a setting of the sovereign-admin, at nigh | Other |
| [#8604](https://github.com/openova-io/openova/issues/8604) | SEC-261 Conformance test for External Secrets against OpenBao | Other |
| [#8607](https://github.com/openova-io/openova/issues/8607) | SVC-180 Screens group by class; the dead branch goes | Other |
| [#8609](https://github.com/openova-io/openova/issues/8609) | BSS-070 BSS-G01 becomes required | Other |
| [#8613](https://github.com/openova-io/openova/issues/8613) | PRD-201 bp-bge chart to the serving rules | Other |
| [#8617](https://github.com/openova-io/openova/issues/8617) | NET-081 One producer of the Organization kubeconfig | Other |
| [#8618](https://github.com/openova-io/openova/issues/8618) | SEC-060 Keycloak secrets no longer computable | Other |
| [#8634](https://github.com/openova-io/openova/issues/8634) | SEC-190 Mounts, templated policies and generic roles | Other |
| [#8646](https://github.com/openova-io/openova/issues/8646) | SEC-211 Blueprint bp-secrets-store-csi and the injection negative check | Other |

---

## 3. Recently merged PRs (last 36h)

| Merged | PR | Issue closed | Title |
|---|---|---|---|
| 2026-11-22T20:04 | [#8645](https://github.com/openova-io/openova/pull/8645) | #7087 | docs(ledger): plan update |
| 2026-11-22T16:27 | [#8616](https://github.com/openova-io/openova/pull/8616) | #7087 | docs(ledger): plan update |
| 2026-11-22T15:57 | [#8615](https://github.com/openova-io/openova/pull/8615) | #7087 | docs(ledger): plan update |
| 2026-11-22T12:27 | [#8612](https://github.com/openova-io/openova/pull/8612) | #7087 | docs(ledger): plan update |
| 2026-11-22T17:02 | [#8611](https://github.com/openova-io/openova/pull/8611) | #8598 | fix(provisioner): when the install's pre-flight replaces a r |
| 2026-11-22T12:33 | [#8610](https://github.com/openova-io/openova/pull/8610) | #8607 | refactor(console): screens group by class and the dead branc |
| 2026-11-22T12:04 | [#8608](https://github.com/openova-io/openova/pull/8608) | #7087 | docs(ledger): plan update |
| 2026-11-22T11:45 | [#8606](https://github.com/openova-io/openova/pull/8606) | #7087 | docs(ledger): plan update |
| 2026-11-22T11:55 | [#8605](https://github.com/openova-io/openova/pull/8605) | #8604 | test(external-secrets): conformance test of External Secrets |
| 2026-11-22T11:20 | [#8603](https://github.com/openova-io/openova/pull/8603) | #7087 | docs(ledger): plan update |
| 2026-11-22T12:02 | [#8602](https://github.com/openova-io/openova/pull/8602) | #8601 | fix(kured): the node maintenance window is a setting of the  |
| 2026-11-21T17:50 | [#8600](https://github.com/openova-io/openova/pull/8600) | #7087 | docs(ledger): plan update |
| 2026-11-21T16:10 | [#8599](https://github.com/openova-io/openova/pull/8599) | fix(gate): check NET-G07 proves the platform's denial in a s |  |
| 2026-11-21T16:12 | [#8598](https://github.com/openova-io/openova/pull/8598) | #6837 | fix(providers): the reference cloud's balancer members carry |
| 2026-11-21T13:56 | [#8597](https://github.com/openova-io/openova/pull/8597) | #8558 | fix(cutover): step 03 copies the OpenTofu provider packages  |
| 2026-11-21T13:22 | [#8596](https://github.com/openova-io/openova/pull/8596) | fix(ci): the acceptance job of a work package has OpenTofu,  |  |
| 2026-11-21T16:57 | [#8595](https://github.com/openova-io/openova/pull/8595) | #8594 | fix(net): the name servers answer on public port 53 over UDP |
| 2026-11-21T09:49 | [#8593](https://github.com/openova-io/openova/pull/8593) | #7087 | docs(ledger): plan update |
| 2026-11-21T10:34 | [#8592](https://github.com/openova-io/openova/pull/8592) | #8584 | fix(cutover): an Organization gets the Sovereign's own chart |
| 2026-11-21T08:04 | [#8591](https://github.com/openova-io/openova/pull/8591) | #8567 | fix(organization): the smallest plan's quota holds what ever |
| 2026-11-21T07:45 | [#8590](https://github.com/openova-io/openova/pull/8590) | #8586 | test(gate): check INF-G01 requires every moved workload back |
| 2026-11-21T07:39 | [#8589](https://github.com/openova-io/openova/pull/8589) | #5646 | fix(organization): the mirror of an Organization's kubeconfi |
| 2026-11-21T07:16 | [#8588](https://github.com/openova-io/openova/pull/8588) | fix(gate): the zero-change plan check packs the provider con |  |
| 2026-11-21T07:23 | [#8587](https://github.com/openova-io/openova/pull/8587) | #8570 | fix(cutover): step 08 fails at once when its roll cannot fin |
| 2026-11-21T07:09 | [#8585](https://github.com/openova-io/openova/pull/8585) | #7087 | docs(ledger): plan update |
| 2026-11-21T07:06 | [#8584](https://github.com/openova-io/openova/pull/8584) | #3526 | fix(cutover): an Organization written while the cutover runs |
| 2026-11-21T05:49 | [#8583](https://github.com/openova-io/openova/pull/8583) | #7087 | docs(ledger): plan update |
| 2026-11-21T05:49 | [#8582](https://github.com/openova-io/openova/pull/8582) | #8577 | test(gate): check NET-G18 proves full-size transfers between |
| 2026-11-21T05:25 | [#8581](https://github.com/openova-io/openova/pull/8581) | #7087 | docs(ledger): plan update |
| 2026-11-21T07:14 | [#8580](https://github.com/openova-io/openova/pull/8580) | #8567 | test(cilium): a cloud-free job moves 64 KB between two worke |

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
