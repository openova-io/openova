# OpenOva — Status Tracker

Regenerated every 15 min by `/home/openova/bin/refresh-dod-dashboard.sh`. Every `#NNNN` is a clickable GitHub link.

|  |  |
|---|---|
| Last refreshed | `2026-08-30T04:15:03Z` |
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
| [#6077](https://github.com/openova-io/openova/issues/6077) | Job re-run on a collapsed scanner identity row 422s and fails invisibly: syft-sb | Other |
| [#6079](https://github.com/openova-io/openova/issues/6079) | mothership GitOps loop dead: containerd's ghcr pull carries a credential GHCR de | Other |
| [#6081](https://github.com/openova-io/openova/issues/6081) | Per-Org console: the install wizard's Organization list is empty because the dir | Other |
| [#6082](https://github.com/openova-io/openova/issues/6082) | Cutover can never start on hw293: a repaired defect latched the record failed, a | Other |
| [#6084](https://github.com/openova-io/openova/issues/6084) | Silent re-auth on a per-Org console targets auth.<orgslug>.<parent>, which the g | Other |
| [#6085](https://github.com/openova-io/openova/issues/6085) | Reconciliation tab never offers Resume: suspended read is stale after its own ac | Other |
| [#6087](https://github.com/openova-io/openova/issues/6087) | catalyst-pin IdP dials catalyst-api over the Sovereign's own public EIP, so the  | Other |
| [#6090](https://github.com/openova-io/openova/issues/6090) | The Keycloak account console hangs forever on 'Loading the Account Console' inst | Other |
| [#6091](https://github.com/openova-io/openova/issues/6091) | converged-late census counts tenant HelmReleases: a customer app install can vet | Other |
| [#6093](https://github.com/openova-io/openova/issues/6093) | Jobs cutover projection orders the 11 sovereignty steps ALPHABETICALLY — every | Other |
| [#6106](https://github.com/openova-io/openova/issues/6106) | catalyst-pin broker back-channel netpol gap: keycloak cannot reach catalyst-api. | Other |
| [#6107](https://github.com/openova-io/openova/issues/6107) | org-controller counts an UNUSABLE secondary kubeconfig as wired — a contextles | Other |
| [#6108](https://github.com/openova-io/openova/issues/6108) | secondary-kubeconfig delivery ships a torn read: non-atomic write + a non-empty- | Other |
| [#6109](https://github.com/openova-io/openova/issues/6109) | openova-mcp masks the authoritative /whoami verdict behind a stale 'signature is | Other |
| [#6111](https://github.com/openova-io/openova/issues/6111) | UAT adjudication: three clauses assert objects the platform does not have (Conti | Other |
| [#6114](https://github.com/openova-io/openova/issues/6114) | UAT ❌ residue on hw293: 61 rows in 8 root-cause clusters — 30 behind one dea | Other |
| [#6122](https://github.com/openova-io/openova/issues/6122) | UAT row 213: cross-Org get_application answers not-found, not 403 — #5522 move | Other |
| [#6131](https://github.com/openova-io/openova/issues/6131) | Cutover execution tree still has no root: the true first step gitea-mirror keeps | Other |
| [#6135](https://github.com/openova-io/openova/issues/6135) | The Organization create door accepts a declared isolation it never honours: 202  | Other |
| [#6140](https://github.com/openova-io/openova/issues/6140) | hw293: a console-gateway hostname probed on the SHARED gateway's host port retur | Other |
| [#6148](https://github.com/openova-io/openova/issues/6148) | Region-kill failback: recovered region-A returns as an UNFENCED TL=1 primary and | Other |
| [#6149](https://github.com/openova-io/openova/issues/6149) | bp-postgres DR pairs ship a dr-promoter but NO dr-failback — region-A return l | Other |
| [#6155](https://github.com/openova-io/openova/issues/6155) | Showback bills an Organization the CR model does not contain — the join reads  | Other |
| [#6156](https://github.com/openova-io/openova/issues/6156) | DR preflight-02 cannot Fail on a 2-region Sovereign — the standby outage that  | Other |
| [#6163](https://github.com/openova-io/openova/issues/6163) | Agenity credential chain has TWO one-shot freezes: the init container reads once | Other |
| [#6166](https://github.com/openova-io/openova/issues/6166) | catalyst-ui has no priorityClassName, so it loses the slot #6157 freed — mothe | Other |
| [#6169](https://github.com/openova-io/openova/issues/6169) | Funnel-serving + singles residue (#6114 rows 87/90/95/115/218/228/232/234/241):  | Other |
| [#6172](https://github.com/openova-io/openova/issues/6172) | Fresh prov hw294 wedges Phase 1 at 51/67: bp-keycloak 1.5.9 post-install realm i | Other |
| [#6183](https://github.com/openova-io/openova/issues/6183) | specter residue + UAT row W5: remove the phantom wizard component id and the non | Other |
| [#6187](https://github.com/openova-io/openova/issues/6187) | bp-keycloak 1.5.10: the #6172 backchannel anchor is not gated to Sovereign mode, | Other |
| [#6189](https://github.com/openova-io/openova/issues/6189) | P0: mothership owner sign-in dead — Keycloak scaled to 0 behind a green HelmRe | Other |
| [#6191](https://github.com/openova-io/openova/issues/6191) | hw295 reports status=ready while its console gateway EIP (212.72.24.8, the wiped | Other |
| [#6192](https://github.com/openova-io/openova/issues/6192) | Per-app Overview prints stale PLACEMENT 'singleton' while Topology reads runtime | Other |
| [#6194](https://github.com/openova-io/openova/issues/6194) | Wizard StepOrg still says "All fields are pre-filled" after #5401 emptied them � | Other |
| [#6197](https://github.com/openova-io/openova/issues/6197) | P0: mothership Stalwart is killed by its own liveness probe every ~15min — mai | Other |
| [#6200](https://github.com/openova-io/openova/issues/6200) | active-hot-standby preview renders BOTH regions as primary — the role switch m | Other |
| [#6202](https://github.com/openova-io/openova/issues/6202) | Cutover engine re-adopts a prior attempt's FAILED step Job instead of recreating | Other |
| [#6211](https://github.com/openova-io/openova/issues/6211) | harbor-prewarm PUSHes into a Harbor proxy-cache project — the first pass alway | Other |
| [#6214](https://github.com/openova-io/openova/issues/6214) | P0 Pillar-5: step-08's pre-hold Crossplane lint requires step-11's pivot — the | Other |
| [#6225](https://github.com/openova-io/openova/issues/6225) | Re-prov after a wipe false-fails on orphaned catalyst-* VPCs when the post-recla | Other |
| [#6229](https://github.com/openova-io/openova/issues/6229) | /api/v1/version reports a sha from a Deployment env var that a roll never update | Other |
| [#6231](https://github.com/openova-io/openova/issues/6231) | Topology choice does not survive the round trip: the status endpoint flattens th | Other |
| [#6238](https://github.com/openova-io/openova/issues/6238) | main is red: the bp-guacamole auto-bump workflow bumps Chart.yaml without the ca | Other |
| [#6242](https://github.com/openova-io/openova/issues/6242) | fix(funnel): a settled order whose launch call is lost strands the paid Organiza | Other |
| [#6249](https://github.com/openova-io/openova/issues/6249) | Sovereign /apps estate grid renders 11 cards against 10 Application CRs — a He | Other |
| [#6253](https://github.com/openova-io/openova/issues/6253) | harbor-prewarm calls an unpublished chart pin 'genuinely mid-roll' and then refu | Other |
| [#6255](https://github.com/openova-io/openova/issues/6255) | region-B cilium-operator never starts its Gateway-API controller (30s CRD poll l | Other |
| [#6260](https://github.com/openova-io/openova/issues/6260) | Sandbox retirement is half-landed: the reconciler's only install site was delete | Other |
| [#6262](https://github.com/openova-io/openova/issues/6262) | harbor-prewarm blocks on the cutover's OWN hollow pin, and the chart's Case 40 g | Other |
| [#6268](https://github.com/openova-io/openova/issues/6268) | Topology tab shows one target for an active-hot-standby app whose backing pair i | Other |
| [#6272](https://github.com/openova-io/openova/issues/6272) | check-guards-are-wired.sh globs only *.sh — a python guard is orphaned invisib | Other |
| [#6273](https://github.com/openova-io/openova/issues/6273) | harbor-prewarm Phase A0: an Organization's failed app install fail-closes the so | Other |
| [#6278](https://github.com/openova-io/openova/issues/6278) | Funnel + vcluster Org: every per-Org chart rendering a cert-manager Certificate  | Other |
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
| [#6314](https://github.com/openova-io/openova/issues/6314) | bp-agenity per-Org oidc-gate points the browser at auth.<slug>.<pool>, a host wi | Other |
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
| [#6493](https://github.com/openova-io/openova/issues/6493) | cutover step-01 secondary-gitea-mirror Job denied by kyverno flux-managed (missi | Other |
| [#6495](https://github.com/openova-io/openova/issues/6495) | wipe.go: handed-over Huawei Sovereign is un-wipeable (provHint defaults to hetzn | Other |
| [#6501](https://github.com/openova-io/openova/issues/6501) | FUNNEL/agenity: funnel door omits the openova-MCP bearer + RS256 verify-pubkey w | Other |
| [#6506](https://github.com/openova-io/openova/issues/6506) | PIN storm residual: MultiSubscriber MaxDeliver:-1 still redelivers a doomed app_ | Other |
| [#6508](https://github.com/openova-io/openova/issues/6508) | fresh-prov Flux bootstrap stalls ~1h: unauthenticated openova GitRepository clon | Other |
| [#6509](https://github.com/openova-io/openova/issues/6509) | per-Org console login still 400s on fresh prov: #6504 mid-host wildcard console. | Other |
| [#6511](https://github.com/openova-io/openova/issues/6511) | cutover step-01 gitea-mirror halts with DeadlineExceeded on the ~470MB monorepo  | Other |
| [#6513](https://github.com/openova-io/openova/issues/6513) | bp-agenity 0.5.31 creds-resync sidecar lacks liveness/readiness probes — kyver | Other |
| [#6573](https://github.com/openova-io/openova/issues/6573) | reset-uat.py carry-forward silently no-ops on HTML UAT.md (scans raw HTML with a | Other |
| [#6582](https://github.com/openova-io/openova/issues/6582) | bp-postgres@0.2.23 HR upgrade times out (context deadline exceeded) → 16-app c | Other |
| [#6627](https://github.com/openova-io/openova/issues/6627) | Singleton shared-pg netpol: region-B keycloak cross-mesh :5432 DROP → JDBC acq | Other |
| [#6629](https://github.com/openova-io/openova/issues/6629) | Customer-org (vcluster/plan-M) provisioning wedges at bp_charts_installed — CN | Other |
| [#6645](https://github.com/openova-io/openova/issues/6645) | cutover step-01 wedge on hw305: #6490 secondary-region gitea-mirror push fails ' | Other |
| [#6695](https://github.com/openova-io/openova/issues/6695) | dashboard(treemap): converged Sovereign defaults to real resource/health map, no | Other |
| [#6697](https://github.com/openova-io/openova/issues/6697) | console: index.html served without Cache-Control — rolled deploys invisible un | Other |

---

## 3. Recently merged PRs (last 36h)

| Merged | PR | Issue closed | Title |
|---|---|---|---|
| 2026-08-26T18:15 | [#6702](https://github.com/openova-io/openova/pull/6702) | #6695 | feat(jobs): distinct Kind-chip colour per engine, grouped by |
| 2026-08-26T14:06 | [#6701](https://github.com/openova-io/openova/pull/6701) | #6695 | fix(jobs): Kind column badge shows real engine name (re-cut) |
| 2026-08-26T13:52 | [#6700](https://github.com/openova-io/openova/pull/6700) | #4 | feat(jobs): real engine names in /jobs Kind filter + treemap |
| 2026-08-26T13:39 | [#6699](https://github.com/openova-io/openova/pull/6699) | #6695 | feat(console): rename Cloud → Resources (Resources/Jobs re-c |
| 2026-08-26T11:37 | [#6698](https://github.com/openova-io/openova/pull/6698) | #6697 | fix(console): serve index.html with Cache-Control no-cache ( |
| 2026-08-26T09:39 | [#6696](https://github.com/openova-io/openova/pull/6696) | #4731 | feat(dashboard): converged Sovereign treemap defaults to rea |
| 2026-08-26T16:56 | [#6694](https://github.com/openova-io/openova/pull/6694) | #6693 | docs(uat): annotate the 12 pending re-walk rows with per-row |
| 2026-08-26T07:12 | [#6693](https://github.com/openova-io/openova/pull/6693) | #6690 | docs(uat): re-walk the 20 UI-only rows on live hw305 — 7 fli |
| 2026-08-26T05:56 | [#6692](https://github.com/openova-io/openova/pull/6692) | docs: repair 39 broken cross-references |  |
| 2026-08-26T05:36 | [#6691](https://github.com/openova-io/openova/pull/6691) | #665 | docs: full-repo integrity pass — 426 fixes across 92 docs +  |
| 2026-08-25T06:22 | [#6690](https://github.com/openova-io/openova/pull/6690) | docs(uat): correct evidence integrity — 52 filler-screenshot |  |
| 2026-08-24T09:24 | [#6688](https://github.com/openova-io/openova/pull/6688) | docs: fix mermaid sequenceDiagram parse errors (semicolons s |  |
| 2026-08-24T09:06 | [#6687](https://github.com/openova-io/openova/pull/6687) | docs(system-design): fix mermaid parse error in §10 Blueprin |  |
| 2026-08-24T08:25 | [#6686](https://github.com/openova-io/openova/pull/6686) | docs(runbooks): mermaid enrichment to SYSTEM-DESIGN standard |  |
| 2026-08-24T08:26 | [#6685](https://github.com/openova-io/openova/pull/6685) | docs(status): mermaid enrichment to SYSTEM-DESIGN standard |  |
| 2026-08-24T08:22 | [#6684](https://github.com/openova-io/openova/pull/6684) | docs(glossary): mermaid enrichment to SYSTEM-DESIGN standard |  |
| 2026-08-24T08:20 | [#6683](https://github.com/openova-io/openova/pull/6683) | docs(security): mermaid enrichment to SYSTEM-DESIGN standard |  |
| 2026-08-24T08:15 | [#6682](https://github.com/openova-io/openova/pull/6682) | docs(architecture): mermaid enrichment to SYSTEM-DESIGN stan |  |
| 2026-08-24T08:13 | [#6681](https://github.com/openova-io/openova/pull/6681) | docs(protocol): mermaid enrichment to SYSTEM-DESIGN standard |  |
| 2026-08-24T08:14 | [#6680](https://github.com/openova-io/openova/pull/6680) | docs(principles): mermaid enrichment to SYSTEM-DESIGN standa |  |
| 2026-08-24T08:12 | [#6679](https://github.com/openova-io/openova/pull/6679) | docs(dod): mermaid enrichment to SYSTEM-DESIGN standard |  |
| 2026-08-24T07:57 | [#6678](https://github.com/openova-io/openova/pull/6678) | docs(system-design): major rewrite — GitOps engine, streamin |  |
| 2026-08-24T06:23 | [#6677](https://github.com/openova-io/openova/pull/6677) | docs: SYSTEM-DESIGN.md — comprehensive CTO/lead-dev onboardi |  |
| 2026-08-24T05:53 | [#6676](https://github.com/openova-io/openova/pull/6676) | docs(uat): kill the ledger table's horizontal scroll (break  |  |
| 2026-08-24T05:20 | [#6675](https://github.com/openova-io/openova/pull/6675) | #4439 | docs(uat): row 235 — refine ❌ root cause (region-B global-se |
| 2026-08-24T05:03 | [#6674](https://github.com/openova-io/openova/pull/6674) | docs(uat): fix ledger table overflow properly (remove <style |  |
| 2026-08-24T04:30 | [#6673](https://github.com/openova-io/openova/pull/6673) | docs(uat): fix ledger table overflow — table-layout:fixed +  |  |
| 2026-08-24T04:02 | [#6672](https://github.com/openova-io/openova/pull/6672) | #800 | docs(uat): row 14 ⏳→✅ — shared-pg reuse LIVE, multiple consu |
| 2026-08-24T03:48 | [#6671](https://github.com/openova-io/openova/pull/6671) | #3376 | docs(uat): row 93 ⏳→✅ — 2nd-org funnel generality (walkstran |
| 2026-08-24T02:39 | [#6670](https://github.com/openova-io/openova/pull/6670) | #4439 | docs(uat): record hw305 diagnostics in the 4 ⏳ rows (14/93/1 |

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
