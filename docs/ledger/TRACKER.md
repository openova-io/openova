# OpenOva — Status Tracker

Regenerated every 15 min by `/home/openova/bin/refresh-dod-dashboard.sh`. Every `#NNNN` is a clickable GitHub link.

|  |  |
|---|---|
| Last refreshed | `2026-11-12T19:45:04Z` |
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
| [#6280](https://github.com/openova-io/openova/issues/6280) | Phase A0 cannot list Organization namespaces — the #6273 partition ships witho | Other |
| [#6288](https://github.com/openova-io/openova/issues/6288) | A catalyst-api roll rewinds an in-flight cutover to step 1 — safe only if the  | Other |
| [#6289](https://github.com/openova-io/openova/issues/6289) | The cutover engine has no Lease — an outgoing catalyst-api Pod can drive the s | Other |
| [#6294](https://github.com/openova-io/openova/issues/6294) | harbor-prewarm enumerates the same destination twice and the redundant push fail | Other |
| [#6297](https://github.com/openova-io/openova/issues/6297) | bp-alloy slot 21: the Sovereign's own telemetry DaemonSet is denied on re-admiss | Other |
| [#6298](https://github.com/openova-io/openova/issues/6298) | Seven workflows run an unpinned azure/setup-helm — including check-no-nodeport | Other |
| [#6306](https://github.com/openova-io/openova/issues/6306) | noreply@openova.io burns its 25/hour send budget in 83s — PIN sign-in blocked  | Other |
| [#6307](https://github.com/openova-io/openova/issues/6307) | Post-pivot cutover deadlock: a step that terminal-fails past step-05 can never r | Other |
| [#6309](https://github.com/openova-io/openova/issues/6309) | No cutover step pivots the per-Org <slug>/catalyst-tenant repos — G11 cannot c | Other |
| [#6311](https://github.com/openova-io/openova/issues/6311) | bp-wordpress-tenant cannot converge in ANY Org: no cnpg-system carve-out blinds  | Other |
| [#6312](https://github.com/openova-io/openova/issues/6312) | wizard step 1 still pre-fills the fabricated company's industry — the select h | Other |
| [#6317](https://github.com/openova-io/openova/issues/6317) | Agenity Anthropic credential expires every ~5h and nothing refreshes it — rows | Other |
| [#6318](https://github.com/openova-io/openova/issues/6318) | Build bp-specter — realize the AIOps component the wizard catalog used to offe | Other |
| [#6319](https://github.com/openova-io/openova/issues/6319) | PIN sign-in is dead program-wide: an app_ready event storm NAKs against a 25/hou | Other |
| [#6324](https://github.com/openova-io/openova/issues/6324) | Per-Org bp-newapi quota guards model one 500m container; the ResourceQuota admit | Other |
| [#6336](https://github.com/openova-io/openova/issues/6336) | Fresh prov wedges at 0 HelmReleases: bootstrap GitRepository clones 938MB of his | Other |
| [#6339](https://github.com/openova-io/openova/issues/6339) | Region-b converges to 52/67 then stalls: keycloak and harbor blocked on missing  | Other |
| [#6344](https://github.com/openova-io/openova/issues/6344) | placement projection joins Pods to an Application by NAME COINCIDENCE — apps w | Other |
| [#6347](https://github.com/openova-io/openova/issues/6347) | placement projection emits TWO Primaries for an active-hot-standby app, plus a d | Other |
| [#6352](https://github.com/openova-io/openova/issues/6352) | per-Org GitOps tree is missing four emitters — bp-agenity (Pillar 4), bp-keycl | Other |
| [#6360](https://github.com/openova-io/openova/issues/6360) | Post-cutover Sovereign sells marketplace apps it cannot install — proxy-docker | Other |
| [#6362](https://github.com/openova-io/openova/issues/6362) | MCP accepts only handover-signed bearers — a signed-in User cannot reach it; r | Other |
| [#6364](https://github.com/openova-io/openova/issues/6364) | No vcluster is ever installed — bootstrap-kit slots 54/58/59 that the controll | Other |
| [#6374](https://github.com/openova-io/openova/issues/6374) | bp-agenity credentialWait: 0 silently renders as 300/5 via sprig default — 'fa | Other |
| [#6475](https://github.com/openova-io/openova/issues/6475) | Storefront sells four Blueprints the catalog-seed does not carry — purchasable | Other |
| [#6477](https://github.com/openova-io/openova/issues/6477) | G8/G9/220/221 are INPUT-gated: the Anthropic credential is founder-supplied and  | Other |
| [#6482](https://github.com/openova-io/openova/issues/6482) | bp-harbor + bp-powerdns: chart-default images route through the Harbor they serv | Other |
| [#6483](https://github.com/openova-io/openova/issues/6483) | bp-stalwart-tenant 0.1.15: install fails in vcluster Orgs — no matches for kin | Other |
| [#6485](https://github.com/openova-io/openova/issues/6485) | hw300 (dep c16642c6b17f1721): cyclic re-bootstrap after fire — reaches ready+2 | Other |
| [#6487](https://github.com/openova-io/openova/issues/6487) | Cutover step-01 gitea-mirror wedges: full clone of grown monorepo fails + error  | Other |
| [#6490](https://github.com/openova-io/openova/issues/6490) | cutover step-06 fatal on 2-region kom4dc: region-B Flux GitRepository can't ls-r | Other |
| [#6495](https://github.com/openova-io/openova/issues/6495) | wipe.go: handed-over Huawei Sovereign is un-wipeable (provHint defaults to hetzn | Other |
| [#6501](https://github.com/openova-io/openova/issues/6501) | FUNNEL/agenity: funnel door omits the openova-MCP bearer + RS256 verify-pubkey w | Other |
| [#6506](https://github.com/openova-io/openova/issues/6506) | PIN storm residual: MultiSubscriber MaxDeliver:-1 still redelivers a doomed app_ | Other |
| [#6508](https://github.com/openova-io/openova/issues/6508) | fresh-prov Flux bootstrap stalls ~1h: unauthenticated openova GitRepository clon | Other |
| [#6509](https://github.com/openova-io/openova/issues/6509) | per-Org console login still 400s on fresh prov: #6504 mid-host wildcard console. | Other |
| [#6511](https://github.com/openova-io/openova/issues/6511) | cutover step-01 gitea-mirror halts with DeadlineExceeded on the ~470MB monorepo  | Other |
| [#6573](https://github.com/openova-io/openova/issues/6573) | reset-uat.py carry-forward silently no-ops on HTML UAT.md (scans raw HTML with a | Other |
| [#6582](https://github.com/openova-io/openova/issues/6582) | bp-postgres@0.2.23 HR upgrade times out (context deadline exceeded) → 16-app c | Other |
| [#6627](https://github.com/openova-io/openova/issues/6627) | Singleton shared-pg netpol: region-B keycloak cross-mesh :5432 DROP → JDBC acq | Other |
| [#6629](https://github.com/openova-io/openova/issues/6629) | Customer-org (vcluster/plan-M) provisioning wedges at bp_charts_installed — CN | Other |
| [#6645](https://github.com/openova-io/openova/issues/6645) | cutover step-01 wedge on hw305: #6490 secondary-region gitea-mirror push fails ' | Other |
| [#6695](https://github.com/openova-io/openova/issues/6695) | dashboard(treemap): converged Sovereign defaults to real resource/health map, no | Other |
| [#6697](https://github.com/openova-io/openova/issues/6697) | console: index.html served without Cache-Control — rolled deploys invisible un | Other |
| [#6703](https://github.com/openova-io/openova/issues/6703) | epic: unified Jobs/Resources surface (chips + list + dependency-DAG + CronJob sc | Other |
| [#6723](https://github.com/openova-io/openova/issues/6723) | EPIC: bp-chargeback — standalone chargeback application + console menu mapping | Other |
| [#6727](https://github.com/openova-io/openova/issues/6727) | feat(jobs): map phase-gating deps in the /jobs DAG so no job floats | Other |
| [#6742](https://github.com/openova-io/openova/issues/6742) | fix(jobs): graph shows open-ended reconcilers the finite list excludes → chip  | Other |
| [#6744](https://github.com/openova-io/openova/issues/6744) | fix(jobs): list badges show reducer first-paint counts ("OpenTofu 64 / HelmRelea | Other |
| [#6747](https://github.com/openova-io/openova/issues/6747) | fix(jobs): prune childless phase-container nodes from the graph (stray "Reconcil | Other |
| [#6749](https://github.com/openova-io/openova/issues/6749) | fix(jobs): /jobs + dashboard treemap block 73-98s per call — synchronous multi | Other |
| [#6751](https://github.com/openova-io/openova/issues/6751) | Huawei Sovereign: 4 provider/role-gated HRs show blank READY (suspend-by-design) | Other |
| [#6753](https://github.com/openova-io/openova/issues/6753) | bp-postgres: consumer -mesh-rw write alias does not follow a #6149 automatic DR  | Other |
| [#6754](https://github.com/openova-io/openova/issues/6754) | 2-region cutover wedges at step-01: secondary-mirror auth depends on drifting pe | Other |
| [#6759](https://github.com/openova-io/openova/issues/6759) | fix(bp-newapi): admin-promote CronJob pins nonexistent cloudnative-pg/postgresql | Other |
| [#6760](https://github.com/openova-io/openova/issues/6760) | fix(bp-mimir/bp-vpa/bp-external-secrets): #6757 chart bumps fail to UPGRADE live | Other |
| [#6761](https://github.com/openova-io/openova/issues/6761) | gitea chart over the #6004 payload ceiling (91.86%) — blocks publishing 1.2.50 | Other |
| [#6764](https://github.com/openova-io/openova/issues/6764) | 2-region cutover cannot complete: secondary region runs its own EMPTY Harbor ins | Other |
| [#6773](https://github.com/openova-io/openova/issues/6773) | mothership iogrid: 14 digest-pinned service images lost in Harbor blob-store wip | Other |
| [#6776](https://github.com/openova-io/openova/issues/6776) | 3 catalog charts pin the nonexistent cloudnative-pg/postgresql:16.4 (latent ErrI | Other |
| [#6778](https://github.com/openova-io/openova/issues/6778) | hw306 — fresh 2-region prov train: manifest → wipe hw305 → fire → cutove | Other |
| [#6799](https://github.com/openova-io/openova/issues/6799) | NAT-EIP rotation at prov time drifts tofu state (rotated SNAT rule/EIP outside s | Other |
| [#6803](https://github.com/openova-io/openova/issues/6803) | Harbor proxy-cache serves an index whose child manifests are gone — invisible  | Other |
| [#6815](https://github.com/openova-io/openova/issues/6815) | A region-A worker has never joined k3s and nothing reports it — Phase 1 counts | Other |
| [#6825](https://github.com/openova-io/openova/issues/6825) | Every self-upgrade takes the Sovereign's console and API down for 2-3 minutes � | Other |
| [#6832](https://github.com/openova-io/openova/issues/6832) | kyverno pvc-volume-expansion errors on every PVC — the policy's APICall has no | Other |
| [#6839](https://github.com/openova-io/openova/issues/6839) | Region promotion must flip secondary_region_lb_weight — hot-standby has no aut | Other |
| [#6840](https://github.com/openova-io/openova/issues/6840) | hw307 primary region silently runs 4 of 5 provisioned workers — w9b4787 ACTIVE | Other |
| [#6843](https://github.com/openova-io/openova/issues/6843) | chargeback silently discards all outbound mail on a Sovereign (invite links, sta | Other |
| [#6844](https://github.com/openova-io/openova/issues/6844) | SSO gap audit: verify whether registry/harbor and newapi have zero-click SSO (gi | Other |
| [#6849](https://github.com/openova-io/openova/issues/6849) | Platform Applications are named spine-* on live Sovereigns, colliding with the S | Other |
| [#6850](https://github.com/openova-io/openova/issues/6850) | Sovereign cloud cost is collected but never allocated: ADR-0014's 'cloud cost sp | Other |
| [#6853](https://github.com/openova-io/openova/issues/6853) | chargeback meters only 5 of 16 provisionable resource kinds — everything else  | Other |
| [#6855](https://github.com/openova-io/openova/issues/6855) | chargeback bills the whole Huawei project to one customer — bastion-openova is | Other |
| [#6857](https://github.com/openova-io/openova/issues/6857) | Four extended listers 400 against the real Huawei gateway — limit=200 rejected | Other |
| [#6859](https://github.com/openova-io/openova/issues/6859) | scope_token is unsettable, so the #6855 project-scoping fix is inert — every s | Other |
| [#6861](https://github.com/openova-io/openova/issues/6861) | Every Blueprint PR races the deploy-bot for the umbrella chart version — auto- | Other |
| [#6867](https://github.com/openova-io/openova/issues/6867) | chargeback: bring cost analysis to cloud-provider level — overview renders zer | Other |
| [#6870](https://github.com/openova-io/openova/issues/6870) | Sovereign console sign-out redirects the browser to keycloak.keycloak.svc.cluste | Other |
| [#6874](https://github.com/openova-io/openova/issues/6874) | shared-pg DR failback: the divergence re-clone never re-renders (helm in-sync),  | Other |
| [#6963](https://github.com/openova-io/openova/issues/6963) | BSS contracts and plans: the gaps from the founder's training walk (row-level li | Other |
| [#6971](https://github.com/openova-io/openova/issues/6971) | BSS packages and the entitlement matrix: Included / Optional / Not offered per p | Other |
| [#7067](https://github.com/openova-io/openova/issues/7067) | ADR-0015: region-symmetric GitOps + one topology contract + native topology tran | Other |
| [#7087](https://github.com/openova-io/openova/issues/7087) | North star: the transition plan as data, the gate manifest and the first work in | Other |
| [#7095](https://github.com/openova-io/openova/issues/7095) | NET-030 Retired names lose their policy privileges | Other |
| [#7116](https://github.com/openova-io/openova/issues/7116) | CTL-025 Guard: the handler package and main may only shrink | Other |
| [#7117](https://github.com/openova-io/openova/issues/7117) | FND-091 Remove committed binaries and root clutter; add the hygiene guard | Other |
| [#7135](https://github.com/openova-io/openova/issues/7135) | FND-092 Stop building the Sandbox images | Other |
| [#7140](https://github.com/openova-io/openova/issues/7140) | BSS-010 Decision record: commerce and billing | Other |
| [#7141](https://github.com/openova-io/openova/issues/7141) | CLD-063 Mirror the Crossplane packages | Other |
| [#7142](https://github.com/openova-io/openova/issues/7142) | CMP-010 Gate checks: the score moves by push; the shown mode is the engine's mod | Other |
| [#7143](https://github.com/openova-io/openova/issues/7143) | CTL-010 Guard: no new direct cluster write | Other |
| [#7144](https://github.com/openova-io/openova/issues/7144) | CTL-044 Publish contracts C3 and C4 | Other |
| [#7145](https://github.com/openova-io/openova/issues/7145) | FND-023 Ticket template, linter and the executor rules in PROTOCOL.md | Other |
| [#7146](https://github.com/openova-io/openova/issues/7146) | FND-054 Console lint ratchet; the switched-off suite is deleted | Other |
| [#7147](https://github.com/openova-io/openova/issues/7147) | FND-097 Remove the unshipped console, the one job that drives it and the tests t | Other |
| [#7148](https://github.com/openova-io/openova/issues/7148) | NET-033 The definition-of-done verifier stops requiring retired releases | Other |
| [#7149](https://github.com/openova-io/openova/issues/7149) | NET-050 Zone decision record | Other |
| [#7150](https://github.com/openova-io/openova/issues/7150) | SVC-010 Decision record and glossary edits | Other |
| [#7151](https://github.com/openova-io/openova/issues/7151) | SVC-020 Service-model ratchet guard | Other |

---

## 3. Recently merged PRs (last 36h)

| Merged | PR | Issue closed | Title |
|---|---|---|---|
| 2026-11-12T19:39 | [#7159](https://github.com/openova-io/openova/pull/7159) | #7144 | feat(contracts): publish contracts C3 and C4 with the event- |
| 2026-11-12T19:38 | [#7158](https://github.com/openova-io/openova/pull/7158) | #7143 | chore(ci): guard that catalyst-api and core/services gain no |
| 2026-11-12T19:31 | [#7157](https://github.com/openova-io/openova/pull/7157) | #7087 | docs(ledger): plan update: repaired tickets NET-050 and CTL- |
| 2026-11-12T19:42 | [#7156](https://github.com/openova-io/openova/pull/7156) | #7147 | chore: remove the unshipped console scaffold, its test job a |
| 2026-11-12T19:41 | [#7155](https://github.com/openova-io/openova/pull/7155) | #7151 | ci: add the service-model ratchet guard for rules G1 to G9 ( |
| 2026-11-12T19:41 | [#7154](https://github.com/openova-io/openova/pull/7154) | #7148 | fix(scripts): stop the shell verifier requiring the three re |
| 2026-11-12T19:41 | [#7153](https://github.com/openova-io/openova/pull/7153) | #7150 | docs(adr): record the service model and narrow the glossary  |
| 2026-11-12T19:42 | [#7152](https://github.com/openova-io/openova/pull/7152) | #7141 | ci: mirror the Crossplane packages into harbor.openova.io by |
| 2026-11-12T19:15 | [#7139](https://github.com/openova-io/openova/pull/7139) | #7087 | docs(ledger): plan update: tickets FND-054 and FND-097, furt |
| 2026-11-12T18:45 | [#7138](https://github.com/openova-io/openova/pull/7138) | #7135 | ci: stop building the Sandbox images (FND-092) |
| 2026-11-12T18:38 | [#7137](https://github.com/openova-io/openova/pull/7137) | #7133 | chore(core): remove the type mirror that no Go module import |
| 2026-11-12T18:38 | [#7136](https://github.com/openova-io/openova/pull/7136) | #7134 | docs(contracts): publish contract C8, the seal provider and  |
| 2026-11-12T18:31 | [#7132](https://github.com/openova-io/openova/pull/7132) | #7126 | test(ci): prove the k3s bootstrap flags on a runner with thr |
| 2026-11-12T18:22 | [#7131](https://github.com/openova-io/openova/pull/7131) | #7087 | docs(ledger): plan update: the identity step accepts either  |
| 2026-11-12T18:11 | [#7130](https://github.com/openova-io/openova/pull/7130) | #7124 | feat(gate): checks for candidate identity, zero touch, nodes |
| 2026-11-12T18:18 | [#7129](https://github.com/openova-io/openova/pull/7129) | #7123 | docs(adr): record the cloud abstraction contract and correct |
| 2026-11-12T18:10 | [#7128](https://github.com/openova-io/openova/pull/7128) | #7125 | docs(generated): inventories of slots, components and custom |
| 2026-11-12T18:18 | [#7127](https://github.com/openova-io/openova/pull/7127) | #7122 | test(cloud-init): run the shared render test in CI and pin a |
| 2026-11-12T18:02 | [#7121](https://github.com/openova-io/openova/pull/7121) | #7087 | docs(ledger): plan update: tickets CLD-040, CLD-070, FND-033 |
| 2026-11-12T17:55 | [#7120](https://github.com/openova-io/openova/pull/7120) | #7117 | chore: remove committed programs and root clutter, add the r |
| 2026-11-12T17:37 | [#7119](https://github.com/openova-io/openova/pull/7119) | #7118 | docs(adr): decision records 0022, 0023 and 0025, the allocat |
| 2026-11-12T17:33 | [#7115](https://github.com/openova-io/openova/pull/7115) | #7087 | docs(ledger): plan update: tickets FND-091, FND-106 and CTL- |
| 2026-11-12T16:58 | [#7114](https://github.com/openova-io/openova/pull/7114) | #7087 | docs(ledger): the dashboard's baseline shows what was built  |
| 2026-11-12T16:54 | [#7113](https://github.com/openova-io/openova/pull/7113) | #7087 | docs(ledger): the north-star dashboard as a page GitHub rend |
| 2026-11-12T17:04 | [#7112](https://github.com/openova-io/openova/pull/7112) | #7109 | feat(gate): check registry and version 2 record in the accep |
| 2026-11-12T17:04 | [#7111](https://github.com/openova-io/openova/pull/7111) | #7108 | feat(catalyst-api): let a deployment request name the Git re |
| 2026-11-12T16:49 | [#7110](https://github.com/openova-io/openova/pull/7110) | #7087 | docs(ledger): the north-star dashboard as one offline page |
| 2026-11-12T16:48 | [#7107](https://github.com/openova-io/openova/pull/7107) | #7087 | docs(ledger): plan update: tickets FND-014 and FND-032, mana |
| 2026-11-12T14:38 | [#7105](https://github.com/openova-io/openova/pull/7105) | #7093 | fix(ci): auth, shared and the whole billing module are teste |
| 2026-11-12T14:22 | [#7104](https://github.com/openova-io/openova/pull/7104) | #7094 | chore(ci): Go lint ratchet on changed lines (FND-053) |

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
