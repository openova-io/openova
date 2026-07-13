# OpenOva — Status Tracker

Regenerated every 15 min by `/home/openova/bin/refresh-dod-dashboard.sh`. Every `#NNNN` is a clickable GitHub link.

|  |  |
|---|---|
| Last refreshed | `2026-07-13T15:15:02Z` |
| Deploy cron (#799) | ✓ deploy-cron healthy (image-reroll last ran 9m ago) |
| Open issues | 38 |
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

**All 38 open issues** grouped by where they sit in the convergence sequence. Each chain runs left-to-right; chains stack vertically.

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

### All 38 open items (clickable table)

| # | Title | Bucket |
|---|---|---|
| [#3379](https://github.com/openova-io/openova/issues/3379) | SOVEREIGNTY: cutover earns a durable, revert-immune cutoverComplete=true under a | Other |
| [#3969](https://github.com/openova-io/openova/issues/3969) | EPIC: Application-centric Placement (targets[] · Primary/Standby·Hot/Cold · c | Other |
| [#4111](https://github.com/openova-io/openova/issues/4111) | bp-agenity North Star (chat→provision, Pillar 4): mechanism merged+wired — B | Other |
| [#4212](https://github.com/openova-io/openova/issues/4212) | EPIC: ONE object-model/DR backbone — DR/spine half LIVE-Healthy (spine Applica | Other |
| [#4277](https://github.com/openova-io/openova/issues/4277) | FUNNEL/agenity: per-Org openbao anthropic/token auto-seed at Org-create (zero-to | Other |
| [#4635](https://github.com/openova-io/openova/issues/4635) | Make sovereignty-cutover trigger LEVEL-triggered (reconcile-until-done), not an  | Other |
| [#4639](https://github.com/openova-io/openova/issues/4639) | bp-dragonfly — P2P registry distribution: replace bastion proxy-cache + per-cl | Other |
| [#4674](https://github.com/openova-io/openova/issues/4674) | cutover step-04 all-nodes-ack gate is fragile: ONE flaky-back-to-source dfdaemon | Other |
| [#4675](https://github.com/openova-io/openova/issues/4675) | P0 prevention: pre-flight gate trusted the false-empty deployments API → fired | Other |
| [#4677](https://github.com/openova-io/openova/issues/4677) | ROOT CAUSE: wipe=tofu-destroy leaks ALL runtime CSI EVS volumes (315 orphan / ~5 | Other |
| [#4682](https://github.com/openova-io/openova/issues/4682) | Sovereign Cilium Gateway serves :30443 NodePort-range + ClusterIP, not :443 Load | Other |
| [#4683](https://github.com/openova-io/openova/issues/4683) | Operator console /sovereign/deployments is owner-scoped by session email — a s | Other |
| [#4686](https://github.com/openova-io/openova/issues/4686) | #4682 Wave-D3 gap: gateway CiliumLoadBalancerIPPool not wired on Huawei — gate | Other |
| [#4688](https://github.com/openova-io/openova/issues/4688) | cutover steps 02/03 still reach Harbor via internal svc name (harbor-core.harbor | Other |
| [#4752](https://github.com/openova-io/openova/issues/4752) | Intermittent fresh-prov 0-HR wedge ROOT-CAUSED: cloud-init aborts (runcmd exit 9 | Other |
| [#4764](https://github.com/openova-io/openova/issues/4764) | wipe: canonical destroy leaves a dangling console/tenant DNS record (console.<fq | Other |
| [#4765](https://github.com/openova-io/openova/issues/4765) | Zero NodePorts: kill powerdns-anycast NodePort + clustermesh nodePort dial (LB-I | Other |
| [#4773](https://github.com/openova-io/openova/issues/4773) | P1: 5,726 orphaned XUserAccess composites leaking on hw224 — Claims still crea | Other |
| [#4788](https://github.com/openova-io/openova/issues/4788) | Crossplane ProviderConfig on dead tf.upbound.io group — adopt-* Workspaces can | Other |
| [#4858](https://github.com/openova-io/openova/issues/4858) | fix: mothership self-hosting image deadlock — DiskPressure image-GC + harbor.o | Other |
| [#4889](https://github.com/openova-io/openova/issues/4889) | Apps grid + AppDetail header show spine/bootstrap apps FAILED (source: Applicati | Other |
| [#4896](https://github.com/openova-io/openova/issues/4896) | Catalog Edit-IaC YamlEditor Commit broken: dry-run 400 name-mismatch (metadata.n | Other |
| [#4901](https://github.com/openova-io/openova/issues/4901) | cnpg-pair Continuum doesn't surface standby-absent condition (health/lag stay gr | Other |
| [#4923](https://github.com/openova-io/openova/issues/4923) | DR replication-status endpoint returns SYNTHESIZED Hetzner placeholder, not live | Other |
| [#4982](https://github.com/openova-io/openova/issues/4982) | Cutover step-CMs resource-policy:keep make chart-fix bumps inert mid-cutover + h | Other |
| [#5007](https://github.com/openova-io/openova/issues/5007) | cutover: fresh-node local-registry pull breaks when public DNS wildcard shadows  | Other |
| [#5012](https://github.com/openova-io/openova/issues/5012) | hw242: region-B bootstrap stalled after CNI (bare cluster) → mesh 0/0, shared- | Other |
| [#5014](https://github.com/openova-io/openova/issues/5014) | cutover driver: Job-watch channel loss marks a SUCCEEDED step failed and halts t | Other |
| [#5015](https://github.com/openova-io/openova/issues/5015) | harbor alias-host entry (harbor.<fqdn>) breaks OIDC callback: state set on alias | Other |
| [#5018](https://github.com/openova-io/openova/issues/5018) | Edit-IaC commits are silently inert: full-CR editor writes only the unwatched pe | Other |
| [#5019](https://github.com/openova-io/openova/issues/5019) | Jobs surface gaps: /jobs omits all 65 bootstrap-kit install rows (no install kin | Other |
| [#5026](https://github.com/openova-io/openova/issues/5026) | Cutover step-07 pod-spec sweep leaves harbor.openova.io (mothership Harbor = den | Other |
| [#5028](https://github.com/openova-io/openova/issues/5028) | Wipe leaks runtime CSI/EVS volumes → kom4dc 400-volume quota fills after ~3 pr | Other |
| [#5030](https://github.com/openova-io/openova/issues/5030) | Cutover step-03 harbor-prewarm skips durable copy on proxy-cache dest pull-throu | Other |
| [#5032](https://github.com/openova-io/openova/issues/5032) | cutover-contract.sh false-fails on CI: printf|grep SIGPIPE under set -o pipefail | Other |
| [#5034](https://github.com/openova-io/openova/issues/5034) | console_isolation: console.<fqdn> DNS points to shared gateway EIP (404) not the | Other |
| [#5036](https://github.com/openova-io/openova/issues/5036) | Cutover step-08 ref-host lint (#5027) fails on ~40 workloads' harbor.openova.io/ | Other |
| [#5042](https://github.com/openova-io/openova/issues/5042) | Fresh-prov bootstrap wedge: cloud-init flux-install stage silently doesn't compl | Other |

---

## 3. Recently merged PRs (last 36h)

| Merged | PR | Issue closed | Title |
|---|---|---|---|
| 2026-07-12T20:54 | [#5041](https://github.com/openova-io/openova/pull/5041) | #5018 | fix(#4896): Edit-IaC commit writes the repo the read path re |
| 2026-07-12T20:54 | [#5040](https://github.com/openova-io/openova/pull/5040) | #4923 | fix(catalyst-api): replication-status reports verified stand |
| 2026-07-12T19:00 | [#5039](https://github.com/openova-io/openova/pull/5039) | #4781 | test(crossplane): CI guard pins adoption-seam GVKs to opento |
| 2026-07-12T18:31 | [#5038](https://github.com/openova-io/openova/pull/5038) | #5036 | fix(cutover): step-08 ref-host lint is redirect-aware — HOST |
| 2026-07-12T18:11 | [#5037](https://github.com/openova-io/openova/pull/5037) | #4688 | fix(cutover): steps 02/03 dial in-cluster Harbor Service, no |
| 2026-07-12T14:14 | [#5035](https://github.com/openova-io/openova/pull/5035) | #5034 | fix(pdm): route marketplace.<fqdn> at the console LB in the  |
| 2026-07-12T11:17 | [#5033](https://github.com/openova-io/openova/pull/5033) | #5030 | fix(ci): here-strings kill cutover-contract.sh SIGPIPE false |
| 2026-07-12T10:59 | [#5031](https://github.com/openova-io/openova/pull/5031) | #4994 | fix(cutover): step-03 prewarm durable Harbor artifact-API de |
| 2026-07-12T09:59 | [#5029](https://github.com/openova-io/openova/pull/5029) | #5028 | fix(catalyst-api): reap orphaned CSI/EVS volumes on wipe wit |
| 2026-07-12T06:57 | [#5027](https://github.com/openova-io/openova/pull/5027) | #5008 | fix(#5026): guarantee mothership-Harbor host pivot + step-08 |
| 2026-07-12T02:37 | [#5025](https://github.com/openova-io/openova/pull/5025) | #5017 | fix(#5017): bp-falco routes falco + falcoctl images via the  |
| 2026-07-12T02:37 | [#5024](https://github.com/openova-io/openova/pull/5024) | #5017 | fix(#5017 #5022 #5014): provider-CIDR population + step-08 D |
| 2026-07-12T02:37 | [#5023](https://github.com/openova-io/openova/pull/5023) | #3188 | fix(#5022): strategy Recreate for single-replica RWO-backed  |
| 2026-07-11T23:46 | [#5021](https://github.com/openova-io/openova/pull/5021) | #5020 | fix(#5017): residual raw-ref images via Harbor proxy + harbo |
| 2026-07-11T23:10 | [#5020](https://github.com/openova-io/openova/pull/5020) | #11 | fix(#5017): route 4 residual raw-external-ref Blueprint imag |
| 2026-07-11T22:41 | [#5016](https://github.com/openova-io/openova/pull/5016) | #5011 | fix(#5011): step-01 gitea-mirror pushes explicit refspecs —  |
| 2026-07-11T20:53 | [#5013](https://github.com/openova-io/openova/pull/5013) | #3379 | docs(sessions): 2026-07-12 completion matrix + month-cycle r |
| 2026-07-11T19:13 | [#5010](https://github.com/openova-io/openova/pull/5010) | #4973 | fix(#4973 #4975 #4961): comprehensive pod-spec image sweep i |
| 2026-07-11T19:02 | [#5009](https://github.com/openova-io/openova/pull/5009) | #4872 | fix(#4872): version-aware batched OBS bucket empty+delete so |
| 2026-07-11T17:15 | [#5008](https://github.com/openova-io/openova/pull/5008) | #5007 | fix(#5007): cutover pins registry.<fqdn> on nodes + pdm reap |
| 2026-07-11T16:11 | [#5006](https://github.com/openova-io/openova/pull/5006) | #3668 | docs(uat): hw241 catalog 126/128/131/133/134 ✅ (67%) |
| 2026-07-11T15:55 | [#5005](https://github.com/openova-io/openova/pull/5005) | #4896 | docs(uat): hw241 operator-console re-walk — 29 rows ✅ (65% g |
| 2026-07-11T15:06 | [#5004](https://github.com/openova-io/openova/pull/5004) | #5003 | fix(#5003): per-Org vcluster HelmRelease self-heals cold Har |
| 2026-07-11T14:33 | [#5002](https://github.com/openova-io/openova/pull/5002) | #4999 | fix(#4999): funnel 2nd-Org honors chosen pool-TLD + console  |
| 2026-07-11T14:13 | [#5001](https://github.com/openova-io/openova/pull/5001) | #5000 | fix(console #5000): sign-out menu on the sovereign sidebar u |
| 2026-07-11T14:06 | [#4998](https://github.com/openova-io/openova/pull/4998) | #4994 | fix(#4994 #4996): harden cutover step-03 harbor-prewarm + st |
| 2026-07-11T13:40 | [#4997](https://github.com/openova-io/openova/pull/4997) | #3668 | fix(#4990): prefix-tolerant catalog Get — seed-only Blueprin |
| 2026-07-11T12:48 | [#4995](https://github.com/openova-io/openova/pull/4995) | #4993 | fix(#4993): host-native app HTTPRoute for vcluster-tier Orgs |
| 2026-07-11T10:58 | [#4992](https://github.com/openova-io/openova/pull/4992) | #4964 | fix(#4991): org-controller delivers per-Org provisioning RBA |
| 2026-07-11T10:04 | [#4989](https://github.com/openova-io/openova/pull/4989) | #4987 | fix(#4988): lockstep catalog-seed bp-postgres spec.version 0 |

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
