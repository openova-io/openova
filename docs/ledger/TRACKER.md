# OpenOva — Status Tracker

Regenerated every 15 min by `/home/openova/bin/refresh-dod-dashboard.sh`. Every `#NNNN` is a clickable GitHub link.

|  |  |
|---|---|
| Last refreshed | `2026-07-07T05:45:06Z` |
| Deploy cron (#799) | ✓ deploy-cron healthy (image-reroll last ran 9m ago) |
| Open issues | 57 |
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

**All 57 open issues** grouped by where they sit in the convergence sequence. Each chain runs left-to-right; chains stack vertically.

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

### All 57 open items (clickable table)

| # | Title | Bucket |
|---|---|---|
| [#3379](https://github.com/openova-io/openova/issues/3379) | SOVEREIGNTY: cutover earns a durable, revert-immune cutoverComplete=true under a | Other |
| [#3969](https://github.com/openova-io/openova/issues/3969) | EPIC: Application-centric Placement (targets[] · Primary/Standby·Hot/Cold · c | Other |
| [#4111](https://github.com/openova-io/openova/issues/4111) | bp-agenity North Star (chat→provision, Pillar 4): mechanism merged+wired — B | Other |
| [#4212](https://github.com/openova-io/openova/issues/4212) | EPIC: ONE object-model/DR backbone — DR/spine half LIVE-Healthy (spine Applica | Other |
| [#4275](https://github.com/openova-io/openova/issues/4275) | PILLAR-3 ACCEPTANCE: region-kill failover counter-test (D31) — kill region-a,  | Other |
| [#4277](https://github.com/openova-io/openova/issues/4277) | FUNNEL/agenity: per-Org openbao anthropic/token auto-seed at Org-create (zero-to | Other |
| [#4541](https://github.com/openova-io/openova/issues/4541) | fix(harbor): create the missing proxy-xpkg/proxy-ecr proxy-cache projects on the | Other |
| [#4552](https://github.com/openova-io/openova/issues/4552) | Per-app Topology tab: arm the deferred manual Switchover with a confirm + RPO/he | Other |
| [#4569](https://github.com/openova-io/openova/issues/4569) | Funnel TLD choice (omani.rest/.trade) is silently overridden to the single Sover | Other |
| [#4600](https://github.com/openova-io/openova/issues/4600) | fix(crossplane): remove artificial bastion-Harbor (harbor.openova.io/proxy-xpkg) | Other |
| [#4604](https://github.com/openova-io/openova/issues/4604) | bp-agenity: durable external-HTTPS egress CNP + standalone-claude openova MCP di | Other |
| [#4620](https://github.com/openova-io/openova/issues/4620) | fix(crossplane): provider-opentofu package keeps literal ${XPKG_REGISTRY:=xpkg.u | Other |
| [#4623](https://github.com/openova-io/openova/issues/4623) | Fresh-prov convergence residuals on 8fd457a8: loki-sc-rules CrashLoop (probe mis | Other |
| [#4632](https://github.com/openova-io/openova/issues/4632) | fix(bp-self-sovereign-cutover): auto-trigger 425 handler exits without retry → | Other |
| [#4635](https://github.com/openova-io/openova/issues/4635) | Make sovereignty-cutover trigger LEVEL-triggered (reconcile-until-done), not an  | Other |
| [#4636](https://github.com/openova-io/openova/issues/4636) | wipe verifyZeroOrphans misses UNBOUND/nameless EIPs — leaves orphan EIPs after | Other |
| [#4637](https://github.com/openova-io/openova/issues/4637) | cutover wedges at 54% catalyst-api-env-patch — registry-pivot routes the kubel | Other |
| [#4639](https://github.com/openova-io/openova/issues/4639) | bp-dragonfly — P2P registry distribution: replace bastion proxy-cache + per-cl | Other |
| [#4652](https://github.com/openova-io/openova/issues/4652) | bp-self-sovereign-cutover: complete the registry-pivot dfdaemon→Harbor leg (ce | Other |
| [#4656](https://github.com/openova-io/openova/issues/4656) | bp-cilium: VXLAN-over-WireGuard MTU stacking — cilium_wg0 (1290) < pod+VXLAN ( | Other |
| [#4660](https://github.com/openova-io/openova/issues/4660) | bp-self-sovereign-cutover: gitea-admin-secret Helm-lookup bridge renders nil at  | Other |
| [#4662](https://github.com/openova-io/openova/issues/4662) | Decouple cutover auto-fire (fireCutoverOnHandover) from qaTestEnabled — prod-c | Other |
| [#4666](https://github.com/openova-io/openova/issues/4666) | self-sovereign-cutover step-06 Phase-3a wedges at pct=45: pivoted HelmRepository | Other |
| [#4674](https://github.com/openova-io/openova/issues/4674) | cutover step-04 all-nodes-ack gate is fragile: ONE flaky-back-to-source dfdaemon | Other |
| [#4675](https://github.com/openova-io/openova/issues/4675) | P0 prevention: pre-flight gate trusted the false-empty deployments API → fired | Other |
| [#4677](https://github.com/openova-io/openova/issues/4677) | ROOT CAUSE: wipe=tofu-destroy leaks ALL runtime CSI EVS volumes (315 orphan / ~5 | Other |
| [#4682](https://github.com/openova-io/openova/issues/4682) | Sovereign Cilium Gateway serves :30443 NodePort-range + ClusterIP, not :443 Load | Other |
| [#4683](https://github.com/openova-io/openova/issues/4683) | Operator console /sovereign/deployments is owner-scoped by session email — a s | Other |
| [#4685](https://github.com/openova-io/openova/issues/4685) | CI: Controller-image-tag freshness guard is a false-red on EVERY PR — GITHUB_T | Other |
| [#4686](https://github.com/openova-io/openova/issues/4686) | #4682 Wave-D3 gap: gateway CiliumLoadBalancerIPPool not wired on Huawei — gate | Other |
| [#4688](https://github.com/openova-io/openova/issues/4688) | cutover steps 02/03 still reach Harbor via internal svc name (harbor-core.harbor | Other |
| [#4692](https://github.com/openova-io/openova/issues/4692) | fix(cloud-init): kom4dc cilium-bootstrap DNS-wedge — /etc/hosts pin-loop resol | Other |
| [#4706](https://github.com/openova-io/openova/issues/4706) | Sovereign not genuinely-ready on convergence: console EIP unpooled (000) + false | Other |
| [#4723](https://github.com/openova-io/openova/issues/4723) | fix(cutover): auto-trigger Job DeadlineExceeded on fresh prov — script budgets | Other |
| [#4727](https://github.com/openova-io/openova/issues/4727) | fix(cutover): hostNetwork-era wedges — step-06 Programmed gate can NEVER pass  | Other |
| [#4731](https://github.com/openova-io/openova/issues/4731) | Dashboard treemap: Progress + Kind as first-class layers of the EXISTING editabl | Other |
| [#4732](https://github.com/openova-io/openova/issues/4732) | Per-Org console front door defect chain (TLS trio unrecoverable, stale #4075 por | Other |
| [#4739](https://github.com/openova-io/openova/issues/4739) | hw220 UAT walk fault wave — hourly :443 outage loop (TTL'd envoy-restart Job), | Other |
| [#4746](https://github.com/openova-io/openova/issues/4746) | Fresh 2-region prov falsely marked 'failed': phase1 ready-census fires OutcomeRe | Other |
| [#4750](https://github.com/openova-io/openova/issues/4750) | hw221 fresh-prov residual faults: mimir OBS bucket missing (metrics down) + clus | Other |
| [#4752](https://github.com/openova-io/openova/issues/4752) | Intermittent fresh-prov 0-HR wedge ROOT-CAUSED: cloud-init aborts (runcmd exit 9 | Other |
| [#4758](https://github.com/openova-io/openova/issues/4758) | Customer-Org funnel-picked app never serves: broken duplicate 'tenant-<slug>-app | Other |
| [#4761](https://github.com/openova-io/openova/issues/4761) | provisioning gitops: generateAppsSyncKustomization hardcodes sourceRef flux-syst | Other |
| [#4764](https://github.com/openova-io/openova/issues/4764) | wipe: canonical destroy leaves a dangling console/tenant DNS record (console.<fq | Other |
| [#4765](https://github.com/openova-io/openova/issues/4765) | Zero NodePorts: kill powerdns-anycast NodePort + clustermesh nodePort dial (LB-I | Other |
| [#4773](https://github.com/openova-io/openova/issues/4773) | P1: 5,726 orphaned XUserAccess composites leaking on hw224 — Claims still crea | Other |
| [#4784](https://github.com/openova-io/openova/issues/4784) | hw225 region-B: clustermesh global-service backend sync not propagating (region- | Other |
| [#4785](https://github.com/openova-io/openova/issues/4785) | Per-Org vcluster CNP bootstrap deadlock — coredns + flux denied reaching vclus | Other |
| [#4788](https://github.com/openova-io/openova/issues/4788) | Crossplane ProviderConfig on dead tf.upbound.io group — adopt-* Workspaces can | Other |
| [#4811](https://github.com/openova-io/openova/issues/4811) | region-b clustermesh dead: #4765 dials :2379 on shared CP-EIP where k3s embedded | Other |
| [#4813](https://github.com/openova-io/openova/issues/4813) | Funnel-provisioned customer Org lands namespace-isolated, not vcluster (+ false  | Other |
| [#4814](https://github.com/openova-io/openova/issues/4814) | Cloud graph renders WorkerNode 24/24 + Region 2/2 as healthy while region-b is a | Other |
| [#4817](https://github.com/openova-io/openova/issues/4817) | hw228 fresh-prov anomalies on a0478e3 train: kubeconfig-PUT-back never fires (40 | Other |
| [#4818](https://github.com/openova-io/openova/issues/4818) | Cloud-view LB 'Front door' mislabels the §854-compliant hostPort datapath as '( | Other |
| [#4820](https://github.com/openova-io/openova/issues/4820) | Cloud list: selecting HTTPRoutes/NetworkPolicies/CiliumNetworkPolicies redirects | Other |
| [#4821](https://github.com/openova-io/openova/issues/4821) | Funnel customer-Org provisioning stalls at dns:pending + console_host ignores fu | Other |
| [#4827](https://github.com/openova-io/openova/issues/4827) | WordPress per-Org app pod churns (~3-min rollout loop) → intermittent HTTP 500 | Other |

---

## 3. Recently merged PRs (last 36h)

| Merged | PR | Issue closed | Title |
|---|---|---|---|
| 2026-07-07T01:38 | [#4831](https://github.com/openova-io/openova/pull/4831) | #4811 | fix(#4811): cloud-aware ClusterMesh dial port (mothership se |
| 2026-07-07T00:10 | [#4830](https://github.com/openova-io/openova/pull/4830) | #4811 | test(#4811): pin clustermesh endpoint dial-port authoritativ |
| 2026-07-06T21:29 | [#4829](https://github.com/openova-io/openova/pull/4829) | #4811 | fix(#4811): retriable startup ClusterMesh reconcile on trans |
| 2026-07-06T19:53 | [#4828](https://github.com/openova-io/openova/pull/4828) | #4785 | fix(provisioning): kill WordPress per-Org double-reconcile c |
| 2026-07-06T18:48 | [#4826](https://github.com/openova-io/openova/pull/4826) | #4792 | fix(#4785): register Gateway-API HTTPRoute CRD in per-Org vc |
| 2026-07-06T12:34 | [#4822](https://github.com/openova-io/openova/pull/4822) | #4804 | fix(#4821): customer-Org vcluster — replicateServices.fromHo |
| 2026-07-06T11:09 | [#4819](https://github.com/openova-io/openova/pull/4819) | #4765 | fix(#4818): cloud-view LB front door — hostPort datapath, no |
| 2026-07-06T05:53 | [#4816](https://github.com/openova-io/openova/pull/4816) | #4811 | fix(#4814): cloud-graph counts honest for standby-absent sec |
| 2026-07-05T21:17 | [#4812](https://github.com/openova-io/openova/pull/4812) | #4808 | fix(#4620): Huawei cloud-credentials empty hcloud-token plac |
| 2026-07-05T18:19 | [#4810](https://github.com/openova-io/openova/pull/4810) | #3374 | fix(ci): uat-sso-flip consolidated-ledger fallback — unbreak |
| 2026-07-05T17:37 | [#4809](https://github.com/openova-io/openova/pull/4809) | #4807 | fix(ui): OrgConsumption.isPlatform — unbreak catalyst-build |
| 2026-07-05T17:32 | [#4808](https://github.com/openova-io/openova/pull/4808) | #4620 | fix(#4620): adoption consolidated — k8s backend + anchor + f |
| 2026-07-05T16:58 | [#4807](https://github.com/openova-io/openova/pull/4807) | #4656 | fix(#4656): pod-CIDR peering routes — the native-routing dat |
| 2026-07-05T14:41 | [#4806](https://github.com/openova-io/openova/pull/4806) | #4739 | fix(funnel): render bp-newapi HR (openclaw LLM gateway + row |
| 2026-07-05T14:41 | [#4805](https://github.com/openova-io/openova/pull/4805) | #4803 | fix(funnel): wire openclaw HR oidc.internalIssuerURL (comple |
| 2026-07-05T14:41 | [#4804](https://github.com/openova-io/openova/pull/4804) | #4792 | fix(org-controller): sync host keycloak Service into vcluste |
| 2026-07-05T14:40 | [#4803](https://github.com/openova-io/openova/pull/4803) | #4739 | fix(openclaw): internal-JWKS seam to dodge NAT-EIP hairpin ( |
| 2026-07-05T14:39 | [#4801](https://github.com/openova-io/openova/pull/4801) | #4739 | fix(topology): detect per-Org vcluster via StatefulSet (vClu |
| 2026-07-05T14:40 | [#4800](https://github.com/openova-io/openova/pull/4800) | #4739 | fix(console): worker-nodes tile excludes control-planes (nod |
| 2026-07-05T18:03 | [#4798](https://github.com/openova-io/openova/pull/4798) | #4739 | fix(provisioning): funnel-door apps sourceRef flux-system→op |
| 2026-07-05T07:46 | [#4795](https://github.com/openova-io/openova/pull/4795) | #4794 | chore(crossplane): republish bp-crossplane-claims with #4794 |
| 2026-07-05T06:43 | [#4794](https://github.com/openova-io/openova/pull/4794) | #4739 | fix(adoption): plant empty cross-cloud key so CloudAdoption  |
| 2026-07-05T06:43 | [#4793](https://github.com/openova-io/openova/pull/4793) | #4739 | fix(mimir): make-buckets Job survives slow kom4dc MinIO firs |
| 2026-07-05T05:53 | [#4792](https://github.com/openova-io/openova/pull/4792) | #4785 | fix(org-controller): sync gateway-api HTTPRoutes into per-Or |
| 2026-07-05T04:36 | [#4791](https://github.com/openova-io/openova/pull/4791) | #4739 | fix(console): eradicate residual user-facing tenant leaks (r |
| 2026-07-05T04:36 | [#4790](https://github.com/openova-io/openova/pull/4790) | #4786 | fix(provisioning): mirror per-Org kubeconfig into tenant ns  |
| 2026-07-05T04:36 | [#4789](https://github.com/openova-io/openova/pull/4789) | #4781 | fix(crossplane): ProviderConfig GVK tf→opentofu (adopt-* Wor |
| 2026-07-06T06:31 | [#4787](https://github.com/openova-io/openova/pull/4787) | #4765 | fix(cilium): clustermesh-proxy — cross-region ClusterMesh di |
| 2026-07-05T03:30 | [#4786](https://github.com/openova-io/openova/pull/4786) | #4785 | fix(org-controller): per-Org vcluster CNP bootstrap deadlock |
| 2026-07-05T00:07 | [#4782](https://github.com/openova-io/openova/pull/4782) | #4781 | docs(uat): rows 206/207/239 deep-walk finding (GVK fix #4781 |

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
