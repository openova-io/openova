# OpenOva — Status Tracker

Regenerated every 15 min by `/home/openova/bin/refresh-dod-dashboard.sh`. Every `#NNNN` is a clickable GitHub link.

|  |  |
|---|---|
| Last refreshed | `2026-11-13T07:15:03Z` |
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
| [#7165](https://github.com/openova-io/openova/issues/7165) | CMP-016 An optional informer kind starts when its definition arrives | Other |
| [#7166](https://github.com/openova-io/openova/issues/7166) | FND-034 Checks: NodePort, front doors, mesh, encryption, clean wipe; journeys as | Other |
| [#7167](https://github.com/openova-io/openova/issues/7167) | FND-093 Remove the Sandbox surface of the API | Other |
| [#7168](https://github.com/openova-io/openova/issues/7168) | FND-094 Remove the Sandbox pages of the console | Other |
| [#7169](https://github.com/openova-io/openova/issues/7169) | FND-101 Truth reset of the status surfaces | Other |
| [#7170](https://github.com/openova-io/openova/issues/7170) | FND-103 State markers and their ratchet | Other |
| [#7171](https://github.com/openova-io/openova/issues/7171) | INF-010 Guard for the machine plane | Other |
| [#7172](https://github.com/openova-io/openova/issues/7172) | NET-010 Close peer and stale ports on the reference provider | Other |
| [#7173](https://github.com/openova-io/openova/issues/7173) | PRD-030 Product guard at today's values (rules G-P1 to G-P3) | Other |
| [#7174](https://github.com/openova-io/openova/issues/7174) | SEC-020 Random k3s server token and separate agent token | Other |
| [#7185](https://github.com/openova-io/openova/issues/7185) | ACC-060 Decision record for the access plane | Other |
| [#7186](https://github.com/openova-io/openova/issues/7186) | BSS-030 Order capture never prices from a second list | Other |
| [#7188](https://github.com/openova-io/openova/issues/7188) | CLD-010 Provider-leak scanner with a falling baseline | Other |
| [#7189](https://github.com/openova-io/openova/issues/7189) | CTL-020 Guard: no new polling loop | Other |
| [#7190](https://github.com/openova-io/openova/issues/7190) | FND-105 Security control matrix: format and generator | Other |
| [#7191](https://github.com/openova-io/openova/issues/7191) | FND-110 Guard: no Kyverno engine at 1.20 while a legacy policy exists | Other |
| [#7192](https://github.com/openova-io/openova/issues/7192) | NET-042 Retire the two legacy per-Sovereign overlays | Other |
| [#7193](https://github.com/openova-io/openova/issues/7193) | SOV-030 Wipe refuses a handed-over Sovereign without confirmation | Other |
| [#7194](https://github.com/openova-io/openova/issues/7194) | SVC-060 Function-test convention and the data charts | Other |
| [#7197](https://github.com/openova-io/openova/issues/7197) | FND-171 Remove the fabricated playback route and its preflight route | Other |
| [#7209](https://github.com/openova-io/openova/issues/7209) | AIP-060 Decision record: the model plane | Other |
| [#7210](https://github.com/openova-io/openova/issues/7210) | BSS-020 Truth in the status document and the engine's README | Other |
| [#7211](https://github.com/openova-io/openova/issues/7211) | BSS-050 Ratchets of this area | Other |
| [#7212](https://github.com/openova-io/openova/issues/7212) | CLD-071 Decision record 0024: the primary substrate moved on 2026-05-22; status  | Other |
| [#7213](https://github.com/openova-io/openova/issues/7213) | CMP-005 Decision record: the compliance plane | Other |
| [#7214](https://github.com/openova-io/openova/issues/7214) | CMP-012 Guard: legacy policy count may only fall; the upgrade acknowledgement va | Other |
| [#7215](https://github.com/openova-io/openova/issues/7215) | FND-040 Stability register and the frozen-path check | Other |
| [#7216](https://github.com/openova-io/openova/issues/7216) | FND-055 Vulnerability and secret scans, new findings only | Other |
| [#7217](https://github.com/openova-io/openova/issues/7217) | SEC-006 Decision record 0018: secrets and trust | Other |
| [#7218](https://github.com/openova-io/openova/issues/7218) | SEC-071 Ratchet on passwords the provisioning service writes to Git | Other |
| [#7219](https://github.com/openova-io/openova/issues/7219) | SEC-110 Truth in the security documents | Other |
| [#7231](https://github.com/openova-io/openova/issues/7231) | INF-070 Decision record for the infrastructure plane | Other |
| [#7233](https://github.com/openova-io/openova/issues/7233) | NET-052 Decision record 0026: NodePort ban and the gateway serving model | Other |
| [#7236](https://github.com/openova-io/openova/issues/7236) | PRD-010 Remove the unbacked open-banking claims from the Keycloak README | Other |
| [#7237](https://github.com/openova-io/openova/issues/7237) | PRD-020 Decision record: later products | Other |
| [#7238](https://github.com/openova-io/openova/issues/7238) | SOV-040 Guard G1: source-address ratchet | Other |
| [#7260](https://github.com/openova-io/openova/issues/7260) | CLD-080 Contract directory: module contract, profile schema, pack schema, conven | Other |
| [#7261](https://github.com/openova-io/openova/issues/7261) | CMP-030 Package internal/compliance: types and pure score functions | Other |
| [#7262](https://github.com/openova-io/openova/issues/7262) | CMP-036 Kit slot 27a: values document, hand-over variable, definitions on upgrad | Other |
| [#7263](https://github.com/openova-io/openova/issues/7263) | CTL-201 Parity file for the Organization pipelines | Other |
| [#7264](https://github.com/openova-io/openova/issues/7264) | FND-181 Decision record 0036: backup and restore | Other |
| [#7265](https://github.com/openova-io/openova/issues/7265) | NET-125 Mesh status read, and a mode switch, in the API | Other |
| [#7266](https://github.com/openova-io/openova/issues/7266) | PRD-101 Vendor the six public documents and write the profile | Other |
| [#7267](https://github.com/openova-io/openova/issues/7267) | SOV-050 Guards G2, G3, G4, G7: shrink ratchets | Other |
| [#7268](https://github.com/openova-io/openova/issues/7268) | SOV-110 Contract C5: schema, kit key list, the upstream package and its validato | Other |
| [#7269](https://github.com/openova-io/openova/issues/7269) | SVC-030 The Catalog page honours visibility and refuses what has no chart | Other |
| [#7281](https://github.com/openova-io/openova/issues/7281) | CLD-075 Generator for the provider and profile tables | Other |
| [#7282](https://github.com/openova-io/openova/issues/7282) | NET-100 Network registry output and validator | Other |
| [#7283](https://github.com/openova-io/openova/issues/7283) | PRD-104 Consent package | Other |
| [#7284](https://github.com/openova-io/openova/issues/7284) | PRD-105 Token package | Other |
| [#7285](https://github.com/openova-io/openova/issues/7285) | PRD-106 CI workflow for the fingate module | Other |
| [#7291](https://github.com/openova-io/openova/issues/7291) | FND-061 One signing action, used by the control-plane images first | Other |
| [#7292](https://github.com/openova-io/openova/issues/7292) | NET-020 Disable the k3s kube-proxy | Other |
| [#7293](https://github.com/openova-io/openova/issues/7293) | NET-060 Read-only census of the network posture | Other |
| [#7294](https://github.com/openova-io/openova/issues/7294) | NET-090 Network guards in CI | Other |
| [#7295](https://github.com/openova-io/openova/issues/7295) | SOV-220 The console's step list comes from the status response | Other |
| [#7317](https://github.com/openova-io/openova/issues/7317) | CLD-161 Installation document for the installation in use | Other |
| [#7322](https://github.com/openova-io/openova/issues/7322) | PRD-102 Generated types and server interfaces (oapi-codegen, strict server) | Other |
| [#7323](https://github.com/openova-io/openova/issues/7323) | PRD-103 Ledger port and conformance kit | Other |
| [#7349](https://github.com/openova-io/openova/issues/7349) | CLD-022 Delete the infrastructure mutation dialogs from the console | Other |
| [#7350](https://github.com/openova-io/openova/issues/7350) | CTL-030 Delete the endpoint pipeline and the two duplicate registrations | Other |
| [#7351](https://github.com/openova-io/openova/issues/7351) | CTL-060 Decision record for the write path, the lock and the backbone | Other |
| [#7352](https://github.com/openova-io/openova/issues/7352) | CTL-110 Commit library gitwrite | Other |
| [#7353](https://github.com/openova-io/openova/issues/7353) | SEC-120 etcd dump check and node hygiene check, as check files | Other |
| [#7354](https://github.com/openova-io/openova/issues/7354) | SVC-125 Blueprint proofs check: the function-test result in the gate record | Other |

---

## 3. Recently merged PRs (last 36h)

| Merged | PR | Issue closed | Title |
|---|---|---|---|
| 2026-11-13T07:08 | [#7348](https://github.com/openova-io/openova/pull/7348) | #7087 | docs(ledger): plan update |
| 2026-11-13T06:47 | [#7344](https://github.com/openova-io/openova/pull/7344) | #7087 | docs(ledger): result of the lab test SVC-340 (SVC-340) |
| 2026-11-13T06:47 | [#7343](https://github.com/openova-io/openova/pull/7343) | #7087 | docs(ledger): result of the lab test SEC-280 (SEC-280) |
| 2026-11-13T06:14 | [#7338](https://github.com/openova-io/openova/pull/7338) | #7087 | docs(ledger): plan update |
| 2026-11-13T06:06 | [#7337](https://github.com/openova-io/openova/pull/7337) | #7197 | chore(api): remove the DR runbook playback and preflight rou |
| 2026-11-13T06:06 | [#7336](https://github.com/openova-io/openova/pull/7336) | #7317 | feat(cloudprofile): check an installation document and print |
| 2026-11-13T06:33 | [#7335](https://github.com/openova-io/openova/pull/7335) | #7087 | docs(ledger): result of the lab test SOV-060 (SOV-060) |
| 2026-11-13T06:34 | [#7334](https://github.com/openova-io/openova/pull/7334) | #7087 | docs(ledger): result of the lab test CMP-S2 (CMP-S2) |
| 2026-11-13T06:34 | [#7333](https://github.com/openova-io/openova/pull/7333) | #7087 | docs(ledger): result of the lab test INF-030 (INF-030) |
| 2026-11-13T06:34 | [#7332](https://github.com/openova-io/openova/pull/7332) | #7087 | docs(ledger): result of the lab test CLD-050 (CLD-050) |
| 2026-11-13T06:35 | [#7331](https://github.com/openova-io/openova/pull/7331) | #7087 | docs(ledger): result of the lab test SEC-140 (SEC-140) |
| 2026-11-13T06:34 | [#7330](https://github.com/openova-io/openova/pull/7330) | #7087 | docs(ledger): result of the lab test SEC-130 (SEC-130) |
| 2026-11-13T06:52 | [#7329](https://github.com/openova-io/openova/pull/7329) | #7087 | docs(ledger): result of the lab test MIG-050 (MIG-050) |
| 2026-11-13T06:35 | [#7328](https://github.com/openova-io/openova/pull/7328) | #7087 | docs(ledger): result of the lab test SVC-210 (SVC-210) |
| 2026-11-13T06:33 | [#7327](https://github.com/openova-io/openova/pull/7327) | #7087 | docs(ledger): result of the lab test SVC-120 (SVC-120) |
| 2026-11-13T06:33 | [#7326](https://github.com/openova-io/openova/pull/7326) | #7087 | docs(ledger): result of the lab test CMP-S5 (CMP-S5) |
| 2026-11-13T05:38 | [#7325](https://github.com/openova-io/openova/pull/7325) | #7087 | docs(ledger): plan update |
| 2026-11-13T05:37 | [#7324](https://github.com/openova-io/openova/pull/7324) | #7087 | docs(sessions): hw307 browser walk of six north-star screen  |
| 2026-11-13T05:19 | [#7321](https://github.com/openova-io/openova/pull/7321) | #7087 | docs(ledger): plan update |
| 2026-11-13T05:25 | [#7320](https://github.com/openova-io/openova/pull/7320) | #7186 | fix(billing): order capture never prices from a second list; |
| 2026-11-13T05:25 | [#7319](https://github.com/openova-io/openova/pull/7319) | #7265 | refactor(api): read the ClusterMesh state, and a mode switch |
| 2026-11-13T05:25 | [#7318](https://github.com/openova-io/openova/pull/7318) | #7282 | feat(providers): publish the network registry of a Sovereign |
| 2026-11-13T05:11 | [#7316](https://github.com/openova-io/openova/pull/7316) | #7087 | docs(ledger): plan update |
| 2026-11-13T05:04 | [#7315](https://github.com/openova-io/openova/pull/7315) | #7087 | docs(ledger): dashboard refreshed: 85 tasks merged |
| 2026-11-13T06:33 | [#7314](https://github.com/openova-io/openova/pull/7314) | #7087 | docs(ledger): result of the lab test CTL-055 (CTL-055) |
| 2026-11-13T06:33 | [#7313](https://github.com/openova-io/openova/pull/7313) | #7087 | docs(ledger): result of the lab test CTL-050 (CTL-050) |
| 2026-11-13T06:33 | [#7312](https://github.com/openova-io/openova/pull/7312) | #7087 | docs(ledger): result of the lab test CMP-S6 (CMP-S6) |
| 2026-11-13T06:33 | [#7311](https://github.com/openova-io/openova/pull/7311) | #7087 | docs(ledger): result of the lab test CMP-S1 (CMP-S1) |
| 2026-11-13T05:00 | [#7310](https://github.com/openova-io/openova/pull/7310) | #7087 | docs(ledger): plan update |
| 2026-11-13T04:58 | [#7309](https://github.com/openova-io/openova/pull/7309) | #7281 | docs(generated): generate the tables of the provider packs a |

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
