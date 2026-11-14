# OpenOva — Status Tracker

Regenerated every 15 min by `/home/openova/bin/refresh-dod-dashboard.sh`. Every `#NNNN` is a clickable GitHub link.

|  |  |
|---|---|
| Last refreshed | `2026-11-14T19:00:04Z` |
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
| [#7550](https://github.com/openova-io/openova/issues/7550) | CLD-023 Delete the Hetzner-only Compositions, their definitions and orphan manif | Other |
| [#7551](https://github.com/openova-io/openova/issues/7551) | CTL-340 Jobs store subscribe; flow stream event-driven; jobs stream route | Other |
| [#7552](https://github.com/openova-io/openova/issues/7552) | FND-017 The workflows that push to main themselves push under the release-writer | Other |
| [#7553](https://github.com/openova-io/openova/issues/7553) | FND-104 Truth patch of the canonical documents | Other |
| [#7554](https://github.com/openova-io/openova/issues/7554) | SEC-161 Reconciler: secrets mounts and accessor substitution | Other |
| [#7558](https://github.com/openova-io/openova/issues/7558) | INF-110 Kinds Machine and MachineSource; inventory controller with the node sour | Other |
| [#7568](https://github.com/openova-io/openova/issues/7568) | NET-011 API port limited to declared sources | Other |
| [#7576](https://github.com/openova-io/openova/issues/7576) | fix(ci): the secret guard for the OpenBao unseal key fails on main since two tas | Other |
| [#7634](https://github.com/openova-io/openova/issues/7634) | fix(ci): the dependency bot opened 60 pull requests at once and filled the CI qu | Other |
| [#7641](https://github.com/openova-io/openova/issues/7641) | SVC-070 Function tests: identity and secrets charts | Other |
| [#7643](https://github.com/openova-io/openova/issues/7643) | FND-035 The gate workflow | Other |
| [#7644](https://github.com/openova-io/openova/issues/7644) | FND-062 Every image and chart build signs through the action | Other |
| [#7645](https://github.com/openova-io/openova/issues/7645) | NET-135 The plane-isolation policies admit the cross-region file sync and the sn | Other |
| [#7654](https://github.com/openova-io/openova/issues/7654) | ACC-040 Access guards with their baselines | Other |
| [#7655](https://github.com/openova-io/openova/issues/7655) | FND-107 The bot that flips acceptance rows keeps the tally and provenance lines  | Other |
| [#7656](https://github.com/openova-io/openova/issues/7656) | NET-055 Canonical documents state the zone, firewall and default-deny design of  | Other |
| [#7657](https://github.com/openova-io/openova/issues/7657) | PRD-800 Adapter template for a second bank | Other |
| [#7658](https://github.com/openova-io/openova/issues/7658) | SEC-127 The k3s bootstrap-flags test pins its checkout action by commit hash and | Other |
| [#7665](https://github.com/openova-io/openova/issues/7665) | ACC-050 Truth in the access documents | Other |
| [#7666](https://github.com/openova-io/openova/issues/7666) | AIP-041 The quota model of an Organization follows the gateway Pod without the m | Other |
| [#7667](https://github.com/openova-io/openova/issues/7667) | CTL-038 The API accepts a Job's service-account token, confined to one Organizat | Other |
| [#7668](https://github.com/openova-io/openova/issues/7668) | FND-019 The release writer moves the pin of a chart that several kit slots insta | Other |
| [#7669](https://github.com/openova-io/openova/issues/7669) | FND-024 The deploy-bump action and its 20 callers push under the release-writer  | Other |
| [#7670](https://github.com/openova-io/openova/issues/7670) | FND-900 Topology soak fixes (standing package) | Other |
| [#7671](https://github.com/openova-io/openova/issues/7671) | INF-112 Chart templates and image for the inventory controller | Other |
| [#7672](https://github.com/openova-io/openova/issues/7672) | MIG-010 Decision record: migration is a platform product; correct the documents | Other |
| [#7673](https://github.com/openova-io/openova/issues/7673) | PRD-501 Resource server base: store, definitions loader, router with the token c | Other |
| [#7674](https://github.com/openova-io/openova/issues/7674) | PRD-503 Keycloak consent extension | Other |
| [#7675](https://github.com/openova-io/openova/issues/7675) | SEC-073 Remove the committed database passwords and rotate them | Other |
| [#7676](https://github.com/openova-io/openova/issues/7676) | SOV-335 The variables document carries the path of the vulnerability database | Other |
| [#7677](https://github.com/openova-io/openova/issues/7677) | SVC-080 Function tests: GitOps, registry and certificate charts | Other |
| [#7678](https://github.com/openova-io/openova/issues/7678) | SVC-090 Function tests: network, names and edge charts | Other |
| [#7689](https://github.com/openova-io/openova/issues/7689) | The admission rule for ExternalSecret keys refuses every request on a running cl | Other |
| [#7692](https://github.com/openova-io/openova/issues/7692) | FND-039 The suite writes the provision block of a cloud record | Other |
| [#7698](https://github.com/openova-io/openova/issues/7698) | AIP-221 Service skeleton, schema with migrations, outbox table, health | Other |
| [#7699](https://github.com/openova-io/openova/issues/7699) | BSS-141 The cost-source credential is never a Kubernetes Secret | Other |
| [#7700](https://github.com/openova-io/openova/issues/7700) | CTL-420 Watches in the organization and environment controllers | Other |
| [#7701](https://github.com/openova-io/openova/issues/7701) | NET-071 Remove the zone virtual-cluster dialog, tab and stub kind | Other |
| [#7702](https://github.com/openova-io/openova/issues/7702) | NET-221 Delete the four retired charts and their seed entries | Other |
| [#7703](https://github.com/openova-io/openova/issues/7703) | SOV-338 A kept variables document is brought to the keys of the contract before  | Other |
| [#7705](https://github.com/openova-io/openova/issues/7705) | FND-041 The suite selects only the checks that are Go files of the suite | Other |
| [#7714](https://github.com/openova-io/openova/issues/7714) | CLD-067 Gate check CLD-G06 as a suite file | Other |
| [#7715](https://github.com/openova-io/openova/issues/7715) | FND-071 Leftover lister: a read-only listing of what a wiped deployment left in  | Other |
| [#7716](https://github.com/openova-io/openova/issues/7716) | INF-040 Chart bp-kured with the node patch configuration | Other |
| [#7717](https://github.com/openova-io/openova/issues/7717) | NET-102 Registry in the Sovereign status, checked against the live cluster | Other |
| [#7720](https://github.com/openova-io/openova/issues/7720) | CLD-076 Documents link to the generated provider tables | Other |
| [#7721](https://github.com/openova-io/openova/issues/7721) | CTL-061 A decision record names the one writer of Environment objects and the pa | Other |
| [#7722](https://github.com/openova-io/openova/issues/7722) | FND-025 The rulesets name the release-writer key; a push outside the queue is ju | Other |
| [#7723](https://github.com/openova-io/openova/issues/7723) | FND-036 Sign-in as a gate check | Other |
| [#7724](https://github.com/openova-io/openova/issues/7724) | FND-074 Pre-flight: the fires on a parent domain in the last seven days | Other |
| [#7725](https://github.com/openova-io/openova/issues/7725) | SOV-144 The post-back failure codes leave the exit code that the seal contract r | Other |
| [#7726](https://github.com/openova-io/openova/issues/7726) | SOV-151 Kit pull Secret names and the hand-over switch read the Sovereign's vari | Other |
| [#7738](https://github.com/openova-io/openova/issues/7738) | AIP-229 Operations records: PostgreSQL store with migration 0001 and the ops-rec | Other |
| [#7739](https://github.com/openova-io/openova/issues/7739) | BSS-040 Delete core/marketplace-api | Other |
| [#7740](https://github.com/openova-io/openova/issues/7740) | CLD-030 Per-Organization databases use the Sovereign's storage class | Other |
| [#7741](https://github.com/openova-io/openova/issues/7741) | CLD-089 Local pack: the console in the kit core, its address and an owner sessio | Other |
| [#7742](https://github.com/openova-io/openova/issues/7742) | CTL-039 Read routes for the region registry and the Environment list | Other |
| [#7743](https://github.com/openova-io/openova/issues/7743) | FND-059 The guards that run on every pull request share a few jobs | Other |
| [#7744](https://github.com/openova-io/openova/issues/7744) | FND-072 Flight checklist in the lifecycle script: the sweep command and the fire | Other |
| [#7745](https://github.com/openova-io/openova/issues/7745) | INF-120 Runner image and node-mode tasks | Other |
| [#7746](https://github.com/openova-io/openova/issues/7746) | PRD-508 Account-information read operations and the ledger client | Other |
| [#7760](https://github.com/openova-io/openova/issues/7760) | FND-063 Release key pair: platform images carry a key signature | Other |
| [#7761](https://github.com/openova-io/openova/issues/7761) | INF-050 Install kured | Other |
| [#7762](https://github.com/openova-io/openova/issues/7762) | NET-032 Reserve platform namespace names as Organization slugs | Other |
| [#7763](https://github.com/openova-io/openova/issues/7763) | PRD-513 Resource server: PostgreSQL consent store with migration 0001, the defin | Other |
| [#7764](https://github.com/openova-io/openova/issues/7764) | SOV-020 One default for the zero-touch switch | Other |
| [#7765](https://github.com/openova-io/openova/issues/7765) | SOV-142 New nodes fetch k3s, Flux and Gateway API install files from the registr | Other |
| [#7766](https://github.com/openova-io/openova/issues/7766) | SOV-161 The pull robot of the local registry joins the pull secret at install | Other |
| [#7767](https://github.com/openova-io/openova/issues/7767) | SOV-336 The registry install creates the proxy project of the vulnerability data | Other |
| [#7768](https://github.com/openova-io/openova/issues/7768) | the release job writes an attestation name as the runtime image tag of the openc | Other |
| [#7772](https://github.com/openova-io/openova/issues/7772) | AIP-042 Check AIP-G01 judges running containers only | Other |
| [#7773](https://github.com/openova-io/openova/issues/7773) | CTL-330 The services stop building the Kafka client | Other |
| [#7774](https://github.com/openova-io/openova/issues/7774) | CTL-345 The flow endpoints answer only from the local store; check CTL-G12 | Other |
| [#7775](https://github.com/openova-io/openova/issues/7775) | FND-073 The gate runs twice against the standing environment once it carries the | Other |
| [#7776](https://github.com/openova-io/openova/issues/7776) | SVC-040 Blueprint files move to apiVersion catalyst.openova.io/v1 | Other |
| [#7785](https://github.com/openova-io/openova/issues/7785) | SEC-103 Remove the SealedSecret templates from the guacamole and netbird charts | Other |
| [#7792](https://github.com/openova-io/openova/issues/7792) | CLD-061 Crossplane 1.18 to 1.19 | Other |
| [#7793](https://github.com/openova-io/openova/issues/7793) | CTL-331 The shared events package is Kafka-free | Other |
| [#7794](https://github.com/openova-io/openova/issues/7794) | CTL-350 Applications views from the informer cache | Other |
| [#7795](https://github.com/openova-io/openova/issues/7795) | The image build of main fails its console smoke test since 14 Nov 2026 07:40 UTC | Other |
| [#7796](https://github.com/openova-io/openova/issues/7796) | FND-075 The gate record carries the pre-flight numbers: quota headroom and the l | Other |
| [#7797](https://github.com/openova-io/openova/issues/7797) | NET-095 Block the instance metadata address from pods | Other |
| [#7798](https://github.com/openova-io/openova/issues/7798) | NET-110 Cilium base values to target | Other |
| [#7799](https://github.com/openova-io/openova/issues/7799) | SVC-041 Blueprint names bp-<folder>, explicit visibility, and the two missing Bl | Other |
| [#7803](https://github.com/openova-io/openova/issues/7803) | CTL-320 Durable audit | Other |
| [#7804](https://github.com/openova-io/openova/issues/7804) | FND-131 A run without journeys asserts only what the pack holds: discovery of th | Other |
| [#7805](https://github.com/openova-io/openova/issues/7805) | INF-130 Kinds ScanPolicy, HardeningBaseline, MachineRun; operations controller | Other |
| [#7806](https://github.com/openova-io/openova/issues/7806) | SEC-150 Seal-provider contract and the static node-file implementation | Other |
| [#7807](https://github.com/openova-io/openova/issues/7807) | SOV-135 The mothership registry name is a proxy name served at the root, with an | Other |
| [#7813](https://github.com/openova-io/openova/issues/7813) | CLD-090 The three modules meet contract version 1; conformance tests | Other |
| [#7814](https://github.com/openova-io/openova/issues/7814) | CTL-333 The org-services chart stops setting REDPANDA_BROKERS | Other |
| [#7815](https://github.com/openova-io/openova/issues/7815) | NET-013 The gate dials the API port from an address in no list | Other |
| [#7816](https://github.com/openova-io/openova/issues/7816) | PRD-509 Internal authorisation API | Other |
| [#7820](https://github.com/openova-io/openova/issues/7820) | CTL-346 Kit slots 56 and 57 (flow server and emitter) leave the kit | Other |
| [#7821](https://github.com/openova-io/openova/issues/7821) | CTL-355 Delete the second showback meter | Other |
| [#7822](https://github.com/openova-io/openova/issues/7822) | INF-051 Check INF-G01: coordinated reboot, as a suite file | Other |
| [#7823](https://github.com/openova-io/openova/issues/7823) | SEC-104 Unwire bootstrap-kit slot 05 | Other |
| [#7824](https://github.com/openova-io/openova/issues/7824) | SOV-337 The scanner reads the vulnerability database from the Sovereign's own re | Other |
| [#7829](https://github.com/openova-io/openova/issues/7829) | The release bump never writes the funnel chart pin, so the pin drift test fails  | Other |
| [#7831](https://github.com/openova-io/openova/issues/7831) | FND-070 Baseline: one fresh provision of main as it stands | Other |

---

## 3. Recently merged PRs (last 36h)

| Merged | PR | Issue closed | Title |
|---|---|---|---|
| 2026-11-14T18:53 | [#7828](https://github.com/openova-io/openova/pull/7828) | #7760 | ci: sign every platform image and chart with the release key |
| 2026-11-14T18:48 | [#7827](https://github.com/openova-io/openova/pull/7827) | #7763 | feat(fingate): PostgreSQL consent store, definitions informe |
| 2026-11-14T18:26 | [#7826](https://github.com/openova-io/openova/pull/7826) | #7772 | fix(acceptance): check AIP-G01 skips Pods that have ended (A |
| 2026-11-14T18:24 | [#7825](https://github.com/openova-io/openova/pull/7825) | #7087 | docs(ledger): plan update |
| 2026-11-14T17:49 | [#7819](https://github.com/openova-io/openova/pull/7819) | #7087 | docs(ledger): plan update |
| 2026-11-14T18:50 | [#7817](https://github.com/openova-io/openova/pull/7817) | #7716 | feat(kured): chart bp-kured for coordinated node reboots and |
| 2026-11-14T17:18 | [#7812](https://github.com/openova-io/openova/pull/7812) | #7087 | docs(ledger): plan update |
| 2026-11-14T16:55 | [#7810](https://github.com/openova-io/openova/pull/7810) | #7678 | test(charts): function tests for the network, names and edge |
| 2026-11-14T16:54 | [#7809](https://github.com/openova-io/openova/pull/7809) | #7796 | test(gate): the gate record carries the pre-flight numbers a |
| 2026-11-14T16:56 | [#7808](https://github.com/openova-io/openova/pull/7808) | #7766 | feat(sovereign): the pull robot of the local registry joins  |
| 2026-11-14T16:29 | [#7802](https://github.com/openova-io/openova/pull/7802) | #7087 | docs(ledger): plan update |
| 2026-11-14T16:16 | [#7800](https://github.com/openova-io/openova/pull/7800) | #7795 | fix(ci): the console smoke test reads every JavaScript file  |
| 2026-11-14T16:12 | [#7791](https://github.com/openova-io/openova/pull/7791) | #7087 | docs(ledger): plan update |
| 2026-11-14T16:25 | [#7789](https://github.com/openova-io/openova/pull/7789) | #7764 | fix(cutover): the zero-touch switch falls back to true in sl |
| 2026-11-14T17:29 | [#7787](https://github.com/openova-io/openova/pull/7787) | #7739 | chore: delete the unused marketplace API module, its chart t |
| 2026-11-14T15:45 | [#7786](https://github.com/openova-io/openova/pull/7786) | #7722 | ci: the rulesets name the release-writer deploy key, and the |
| 2026-11-14T15:37 | [#7784](https://github.com/openova-io/openova/pull/7784) | #7087 | docs(ledger): plan update |
| 2026-11-14T15:42 | [#7783](https://github.com/openova-io/openova/pull/7783) | #7742 | fix(api): serve the region registry and the Environment list |
| 2026-11-14T16:38 | [#7782](https://github.com/openova-io/openova/pull/7782) | #7665 | docs(guacamole): stop stating session recording as present ( |
| 2026-11-14T15:36 | [#7780](https://github.com/openova-io/openova/pull/7780) | #7767 | feat(harbor): the registry install creates the pull-through  |
| 2026-11-14T15:41 | [#7779](https://github.com/openova-io/openova/pull/7779) | #7762 | fix: reserve the platform namespace names as Organization sl |
| 2026-11-14T15:35 | [#7778](https://github.com/openova-io/openova/pull/7778) | #7725 | fix(cloud-init): the parent-bootstrap failure exits 98 and a |
| 2026-11-14T15:40 | [#7777](https://github.com/openova-io/openova/pull/7777) | #7744 | feat(lifecycle): sweep the cloud account after a wipe and re |
| 2026-11-14T15:04 | [#7771](https://github.com/openova-io/openova/pull/7771) | #7087 | docs(ledger): plan update |
| 2026-11-14T18:20 | [#7770](https://github.com/openova-io/openova/pull/7770) | #2851 | fix(ci): pin the openclaw runtime image by its commit tag, n |
| 2026-11-14T14:52 | [#7759](https://github.com/openova-io/openova/pull/7759) | #7087 | docs(ledger): plan update |
| 2026-11-14T15:02 | [#7757](https://github.com/openova-io/openova/pull/7757) | #7674 | feat(keycloak): add the open-banking consent extension and i |
| 2026-11-14T15:01 | [#7756](https://github.com/openova-io/openova/pull/7756) | #7667 | feat(api): admit the service-account token of the conformanc |
| 2026-11-14T15:01 | [#7753](https://github.com/openova-io/openova/pull/7753) | #7724 | feat(preflight): refuse a fifth fire on one parent domain in |
| 2026-11-14T15:00 | [#7752](https://github.com/openova-io/openova/pull/7752) | #7723 | test(gate): the sign-in probe runs in the gate workflow as a |

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
