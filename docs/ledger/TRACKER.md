# OpenOva — Status Tracker

Regenerated every 15 min by `/home/openova/bin/refresh-dod-dashboard.sh`. Every `#NNNN` is a clickable GitHub link.

|  |  |
|---|---|
| Last refreshed | `2026-07-23T20:45:07Z` |
| Deploy cron (#799) | ✓ deploy-cron healthy (image-reroll last ran 9m ago) |
| Open issues | 20 |
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

**All 20 open issues** grouped by where they sit in the convergence sequence. Each chain runs left-to-right; chains stack vertically.

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

### All 20 open items (clickable table)

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
| [#5113](https://github.com/openova-io/openova/issues/5113) | catalog card-form Save commit-leg writes invalid/destructive Blueprint YAML —  | Other |
| [#5140](https://github.com/openova-io/openova/issues/5140) | Agenity /api/v1/sandbox/sessions returns 500 (not 503) on transient backend-unav | Other |
| [#5193](https://github.com/openova-io/openova/issues/5193) | Wipe strands an env un-wipeable after a partial destroy: wipe path lacks the hua | Other |
| [#5265](https://github.com/openova-io/openova/issues/5265) | Post-cutover Day-2 gap: Gitea mirror re-syncs pin bumps but nothing syncs new ch | Other |
| [#5285](https://github.com/openova-io/openova/issues/5285) | Failed deployment's watch-informers flood catalyst-api → starve /wipe + /deplo | Other |
| [#5298](https://github.com/openova-io/openova/issues/5298) | cutover step-03 harbor-prewarm: #5095 DIRECT-fallback push exceeds the 90-min st | Other |
| [#5325](https://github.com/openova-io/openova/issues/5325) | prov-preflight check-5 false-positive: runner-starved queued docs-guard run coun | Other |
| [#5327](https://github.com/openova-io/openova/issues/5327) | catalyst-api janitor silently deletes deployment record + tofu state + kubeconfi | Other |
| [#5328](https://github.com/openova-io/openova/issues/5328) | Permanence: break-glass record-less env wipe — scripts/wipe-recordless-env.sh  | Other |
| [#5334](https://github.com/openova-io/openova/issues/5334) | pdns-admin OIDC: oidc-gate/oauth2-proxy callback returns HTTP 500 on code exchan | Other |
| [#5339](https://github.com/openova-io/openova/issues/5339) | vpc-podcidr-route-reconciler CronJob(*/5) delays cross-region datapath recovery  | Other |
| [#5341](https://github.com/openova-io/openova/issues/5341) | openova-mcp /mcp endpoint: ~40% envoy edge 404 flake despite healthy replica — | Other |

---

## 3. Recently merged PRs (last 36h)

| Merged | PR | Issue closed | Title |
|---|---|---|---|
| 2026-07-23T13:06 | [#5337](https://github.com/openova-io/openova/pull/5337) | #5311 | fix(cnpg-pair): NetworkPolicy carve-out for the continuum-co |
| 2026-07-23T13:08 | [#5336](https://github.com/openova-io/openova/pull/5336) | #5334 | fix(keycloak,oidc-gate): drop `roles` default scope from pdn |
| 2026-07-23T08:58 | [#5335](https://github.com/openova-io/openova/pull/5335) | #5311 | fix(continuum): #5311 standby-probe reads Secret via cached  |
| 2026-07-23T01:25 | [#5333](https://github.com/openova-io/openova/pull/5333) | #5274 | docs(uat): stamp hw285 sweep — 64✅/78◑/3❌ (cc=true, G12 RPO= |
| 2026-07-23T01:26 | [#5332](https://github.com/openova-io/openova/pull/5332) | #5331 | fix(cnpg-pair): dr-failback CONVERGED must verify Consistent |
| 2026-07-23T00:07 | [#5330](https://github.com/openova-io/openova/pull/5330) | #5327 | fix(janitor): failed-record reap preserves destroy capabilit |
| 2026-07-23T00:07 | [#5326](https://github.com/openova-io/openova/pull/5326) | #5325 | fix(preflight): check-5 counts only roll-capable CI (in_prog |
| 2026-07-22T20:38 | [#5324](https://github.com/openova-io/openova/pull/5324) | #5302 | fix(cutover): Case 34 asserts lowercase disableredirect per  |
| 2026-07-21T03:59 | [#5318](https://github.com/openova-io/openova/pull/5318) | #5261 | fix(#5317): read-back-verify replica consumer-hub secret syn |
| 2026-07-21T03:36 | [#5316](https://github.com/openova-io/openova/pull/5316) | #5311 | fix(continuum): observe 2-region standby via primary pg_stat |
| 2026-07-21T03:36 | [#5314](https://github.com/openova-io/openova/pull/5314) | #5086 | test(powerdns): §854 Hetzner DNS front-door render-contract  |
| 2026-07-21T03:36 | [#5313](https://github.com/openova-io/openova/pull/5313) | #5193 | fix(catalyst-api): converge partial wipes — retain tfstate u |
| 2026-07-21T03:36 | [#5312](https://github.com/openova-io/openova/pull/5312) | #5285 | test(catalyst-api): guard markPhase1Done terminal-failed → Q |
| 2026-07-20T23:49 | [#5310](https://github.com/openova-io/openova/pull/5310) | #5095 | fix(cutover): step-03 prewarm proxy-DOWN direct-first routin |
| 2026-07-20T22:56 | [#5309](https://github.com/openova-io/openova/pull/5309) | #5205 | fix(marketplace): render live provisioning-stage timeline in |
| 2026-07-20T22:56 | [#5308](https://github.com/openova-io/openova/pull/5308) | #5305 | fix(organization-controller): stop the reconcile hot-loop th |
| 2026-07-20T22:39 | [#5307](https://github.com/openova-io/openova/pull/5307) | #4432 | fix(catalog-seed): sync 14 stale catalog card versions + gua |
| 2026-07-20T16:43 | [#5303](https://github.com/openova-io/openova/pull/5303) | #4573 | fix(bp-harbor): disable S3 blob-redirect via the correct low |
| 2026-07-23T01:26 | [#5301](https://github.com/openova-io/openova/pull/5301) | #5258 | feat(self-sovereign-cutover): post-cutover Day-2 Harbor pin  |
| 2026-07-20T15:24 | [#5299](https://github.com/openova-io/openova/pull/5299) | #3379 | fix(cutover): raise step-03 harbor-prewarm deadline for the  |
| 2026-07-20T12:29 | [#5293](https://github.com/openova-io/openova/pull/5293) | #5290 | ci(catalyst): guard mothership raw-kustomize build against b |
| 2026-07-20T11:24 | [#5292](https://github.com/openova-io/openova/pull/5292) | #5290 | fix(catalyst): unbreak mothership raw-kustomize (#5290 globa |
| 2026-07-20T10:22 | [#5291](https://github.com/openova-io/openova/pull/5291) | #5289 | docs(ledger): refresh PATH-TO-100 fix-map for the hw281 fres |
| 2026-07-20T10:04 | [#5290](https://github.com/openova-io/openova/pull/5290) | #5289 | fix(catalyst): serve api/console/marketplace on region-b via |
| 2026-07-20T09:14 | [#5288](https://github.com/openova-io/openova/pull/5288) | #897 | fix(cloud): render Volume nodes from live volume.hcloud mana |
| 2026-07-20T09:14 | [#5287](https://github.com/openova-io/openova/pull/5287) | #5254 | fix(catalyst-api): tear down + quarantine k8scache reflector |
| 2026-07-20T09:14 | [#5286](https://github.com/openova-io/openova/pull/5286) | #5282 | feat(ci): fresh-prov image-registry gate + route bp-trivy of |
| 2026-07-20T09:14 | [#5284](https://github.com/openova-io/openova/pull/5284) | #5269 | fix(catalyst-api): Phase-1 watch progress-guard — never hard |
| 2026-07-20T04:47 | [#5282](https://github.com/openova-io/openova/pull/5282) | #4349 | fix(crossplane): route core controller image via harbor.open |
| 2026-07-20T02:20 | [#5280](https://github.com/openova-io/openova/pull/5280) | #4811 | fix(catalyst-api): enumerate region-b cluster when declared  |

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
