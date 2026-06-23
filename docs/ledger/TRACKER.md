# OpenOva — Status Tracker

Regenerated every 15 min by `/home/openova/bin/refresh-dod-dashboard.sh`. Every `#NNNN` is a clickable GitHub link.

|  |  |
|---|---|
| Last refreshed | `2026-06-23T13:00:03Z` |
| Deploy cron (#799) | ✓ deploy-cron healthy (image-reroll last ran 9m ago) |
| Open issues | 68 |
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

**All 68 open issues** grouped by where they sit in the convergence sequence. Each chain runs left-to-right; chains stack vertically.

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

### All 68 open items (clickable table)

| # | Title | Bucket |
|---|---|---|
| [#3374](https://github.com/openova-io/openova/issues/3374) | SSO: typing the bare root URL of ANY surface (console + all ~11 external apps +  | Other |
| [#3376](https://github.com/openova-io/openova/issues/3376) | FUNNEL: a stranger with a voucher ends signed-in in their OWN org console with t | Other |
| [#3379](https://github.com/openova-io/openova/issues/3379) | SOVEREIGNTY: cutover earns cutoverComplete=true only via a DURABLE, reconcile-im | Other |
| [#3383](https://github.com/openova-io/openova/issues/3383) | ORGANIZATIONS: one machinery, one name — eradicate the sme/tenant-named subsys | Other |
| [#3581](https://github.com/openova-io/openova/issues/3581) | Regenerate the UAT walkthrough doc set with fresh live evidence on the CURRENT e | Other |
| [#3642](https://github.com/openova-io/openova/issues/3642) | NS#1: migrate the 7 host-placed apps (grafana/harbor/keycloak/gitea/openbao/newa | Other |
| [#3724](https://github.com/openova-io/openova/issues/3724) | CI: cloud-init 32256B size gate must run as a required PR check — #3715 broke  | Other |
| [#3740](https://github.com/openova-io/openova/issues/3740) | bp-cnpg-pair: cross-region replica streams ASYNC (sync targets local region-a HA | Other |
| [#3744](https://github.com/openova-io/openova/issues/3744) | Funnel: credit-covered redeem double-fires provisioning → Gitea sme-tenants re | Other |
| [#3785](https://github.com/openova-io/openova/issues/3785) | FUNNEL: customer's purchased WordPress image is kyverno-DENIED (not Harbor-proxi | Other |
| [#3824](https://github.com/openova-io/openova/issues/3824) | hw165: openbao-snapshot-save CreateContainerConfigError — seeder can't read rt | Other |
| [#3829](https://github.com/openova-io/openova/issues/3829) | Every application AND activity shows its TRUE live state end-to-end — every ap | Other |
| [#3859](https://github.com/openova-io/openova/issues/3859) | fix(org-services + per-Org vCluster): sme→org-services rename orphaned 3 platf | Other |
| [#3878](https://github.com/openova-io/openova/issues/3878) | fix(bp-postgres/bp-mgmt-vcluster): deliver the 4 pod-mounted DB secrets natively | Other |
| [#3888](https://github.com/openova-io/openova/issues/3888) | FOUNDATION: thin the bootstrap cloud-init — evict powerdns-api-credentials to  | Other |
| [#3889](https://github.com/openova-io/openova/issues/3889) | Fresh prov lost mid-convergence: unpersisted deployment record + mothership disk | Other |
| [#3891](https://github.com/openova-io/openova/issues/3891) | FOUNDATION: prov-time openbao-KV-seed mechanism → evict provably-safe inline c | Other |
| [#3892](https://github.com/openova-io/openova/issues/3892) | Mothership disk self-maintenance + pre-fire resource preflight gate (durable hw1 | Other |
| [#3895](https://github.com/openova-io/openova/issues/3895) | Jobs page: lifecycle parent label hardcoded "Provision Hetzner" on non-Hetzner ( | Other |
| [#3896](https://github.com/openova-io/openova/issues/3896) | Jobs page: in-cluster convergence Jobs/CronJobs/Kustomizations invisible during  | Other |
| [#3898](https://github.com/openova-io/openova/issues/3898) | Funnel: a crashed/rolled provisioning Pod strands the in-flight provision row � | Other |
| [#3905](https://github.com/openova-io/openova/issues/3905) | Topology placement-edit dropdown shows generic mode descriptions that contradict | Other |
| [#3914](https://github.com/openova-io/openova/issues/3914) | Provisioning UX: fill the ~30-min 'Provision <provider>: Success' void with a li | Other |
| [#3925](https://github.com/openova-io/openova/issues/3925) | Convergence Monitor — redesign provisioning monitoring into 2 native surfaces  | Other |
| [#3926](https://github.com/openova-io/openova/issues/3926) | fix(bp-self-sovereign-cutover): step-02 harbor-projects wedges every post-#3642  | Other |
| [#3934](https://github.com/openova-io/openova/issues/3934) | SSO-landing regression (#3642 mgmt-vCluster pivot): gitea 500 / harbor login-for | Other |
| [#3944](https://github.com/openova-io/openova/issues/3944) | fix(bp-self-sovereign-cutover): step-03 harbor-prewarm stalls 'active' for hours | Other |
| [#3952](https://github.com/openova-io/openova/issues/3952) | fix(bp-mgmt-vcluster): #3642 secret+service cascade on hw172 — harbor/seaweedf | Other |
| [#3955](https://github.com/openova-io/openova/issues/3955) | fix(bootstrap): route private OpenOva images through the harbor cache — elimin | Other |
| [#3969](https://github.com/openova-io/openova/issues/3969) | EPIC: Application-centric Placement (Primary/Standby·Hot/Cold, capability-gated | Other |
| [#3970](https://github.com/openova-io/openova/issues/3970) | Unified Cloud view: a lens IS a named chip-set (not a filter layer) — default  | Other |
| [#3974](https://github.com/openova-io/openova/issues/3974) | fix(catalyst-api): slim the 457MB image — UPX the bundled tofu/helm/kubectl/Go | Other |
| [#3978](https://github.com/openova-io/openova/issues/3978) | fix(cloud-view): RESTORE rich per-kind /cloud?view=list — #3970 regressed it t | Other |
| [#3979](https://github.com/openova-io/openova/issues/3979) | Cloud GRAPH view: 4 fixes — default=Graph · converge≤4s-then-STOP · ArchiM | Other |
| [#3980](https://github.com/openova-io/openova/issues/3980) | Cloud GRAPH refinements: default-to-graph, force-sim convergence, merge ArchiMat | Other |
| [#3982](https://github.com/openova-io/openova/issues/3982) | Topology: derive REAL placement from live runtime — every component falsely sh | Other |
| [#3985](https://github.com/openova-io/openova/issues/3985) | ERADICATE the dead 'SME' concept → Organization across ALL code/config/charts  | Other |
| [#3986](https://github.com/openova-io/openova/issues/3986) | Topology placement STILL empty for vCluster-synced apps — #3984 matcher misses | Other |
| [#3987](https://github.com/openova-io/openova/issues/3987) | Cloud list: per-kind k9s pages show 0 objects for many resource types (helmrelea | Other |
| [#3988](https://github.com/openova-io/openova/issues/3988) | EPIC: OpenOva MCP server — RBAC-scoped-per-user thin facade over the shared au | Other |
| [#3996](https://github.com/openova-io/openova/issues/3996) | feat(cloud): lightweight ArgoCD-like reconciler management — drill + logs + re | Other |
| [#3998](https://github.com/openova-io/openova/issues/3998) | fix(cloud): network+security coverage gap — Gateway/HTTPRoute/NetworkPolicy/Ci | Other |
| [#4000](https://github.com/openova-io/openova/issues/4000) | Per-app Topology shows false 'singleton' on multi-region Sovereign — secondary | Other |
| [#4002](https://github.com/openova-io/openova/issues/4002) | ARCH P0: OpenTofu→Crossplane adoption seam never implemented — Crossplane ow | Other |
| [#4003](https://github.com/openova-io/openova/issues/4003) | fix(fleet): /fleet/applications returns 0 apps on a Sovereign with live HelmRele | Other |
| [#4010](https://github.com/openova-io/openova/issues/4010) | EPIC: bp-chepherd — chepherd as a deployable Application (Helm chart) + solo-a | Other |
| [#4018](https://github.com/openova-io/openova/issues/4018) | fix(cloud): Path B — provisioning instantiates Crossplane XRCs so the XRC laye | Other |
| [#4046](https://github.com/openova-io/openova/issues/4046) | fix(huawei-prov): janitor SG-leak + EIP-egress cooldown preflight (stop region-a | Other |
| [#4055](https://github.com/openova-io/openova/issues/4055) | Huawei provs ship UNDER-PROVISIONED: 2vCPU m7n.large.8 workers wedge bp-catalyst | Other |
| [#4060](https://github.com/openova-io/openova/issues/4060) | fix(provisioning): customer-Org CNPG pair hardcodes hcloud-volumes → Pillar-3  | Other |
| [#4061](https://github.com/openova-io/openova/issues/4061) | Permanent Sovereign auto-fires sovereignty-cutover on handover — make it a del | Other |
| [#4069](https://github.com/openova-io/openova/issues/4069) | byo-api/PowerDNS DNS-write ignores ConsoleLoadBalancerIP — console/api/marketp | Other |
| [#4071](https://github.com/openova-io/openova/issues/4071) | catalyst-api (Sovereign) cannot watch apps.openova.io/v1 Applications despite CR | Other |
| [#4073](https://github.com/openova-io/openova/issues/4073) | Agentic journey blocked: omantel.biz Sovereign has no parent-domain pool → sub | Other |
| [#4079](https://github.com/openova-io/openova/issues/4079) | Per-cluster HelmRelease name is invalid RFC-1123 (agenity-rtz-A uppercase) + mis | Other |
| [#4084](https://github.com/openova-io/openova/issues/4084) | Cloud view polish: graph must settle in ~3s then STOP (regression) + list view k | Other |
| [#4085](https://github.com/openova-io/openova/issues/4085) | Per-resource Compliance tab renders a table of THAT resource's own findings (not | Other |
| [#4086](https://github.com/openova-io/openova/issues/4086) | Sovereign status reads "Degraded" forever on healthy Huawei Sovereign — health | Other |
| [#4089](https://github.com/openova-io/openova/issues/4089) | console(settings): re-home Parent Domains as a granular #parent-domains section  | Other |
| [#4091](https://github.com/openova-io/openova/issues/4091) | Console: replace bare/ugly YAML & IaC rendering with a sophisticated shared code | Other |
| [#4111](https://github.com/openova-io/openova/issues/4111) | bp-agenity: spawned claude-code agent cannot authenticate or run (no claude bina | Other |
| [#4118](https://github.com/openova-io/openova/issues/4118) | agenity-build: published image can silently ship WITHOUT node/claude-code (Docke | Other |
| [#4143](https://github.com/openova-io/openova/issues/4143) | bp-cnpg multi-operator webhook-cert conflict: per-Org + cnpg-system operators sh | Other |
| [#4158](https://github.com/openova-io/openova/issues/4158) | Secondary region: mgmt-vCluster keycloak (+gitea/harbor/grafana) FailedMount — | Other |
| [#4160](https://github.com/openova-io/openova/issues/4160) | catalog-seed bp-wordpress-tenant pin lagged chart (0.4.4 vs 0.4.12) — fresh pr | Other |
| [#4167](https://github.com/openova-io/openova/issues/4167) | Marketplace storefront leaks banned-term 'Tenant' on the customer funnel front d | Other |
| [#4169](https://github.com/openova-io/openova/issues/4169) | bp-newapi: per-Org install clobbers Sovereign newapi-admin redirectUris in share | Other |
| [#4170](https://github.com/openova-io/openova/issues/4170) | Sovereign-admin cannot issue vouchers from the operator console — voucher page | Other |

---

## 3. Recently merged PRs (last 36h)

| Merged | PR | Issue closed | Title |
|---|---|---|---|
| 2026-06-23T11:23 | [#4173](https://github.com/openova-io/openova/pull/4173) | #745 | fix(bp-agenity): no-cache SPA HTML + #4118 post-build smoke  |
| 2026-06-23T11:13 | [#4172](https://github.com/openova-io/openova/pull/4172) | #3383 | fix(console): unblock sovereign-admin voucher issuance — dro |
| 2026-06-23T11:15 | [#4171](https://github.com/openova-io/openova/pull/4171) | #4169 | fix(bp-newapi): per-Org install no longer clobbers Sovereign |
| 2026-06-23T10:51 | [#4168](https://github.com/openova-io/openova/pull/4168) | #3383 | fix(marketplace): eradicate 'Tenant' banned-term from the st |
| 2026-06-23T08:16 | [#4166](https://github.com/openova-io/openova/pull/4166) | #4002 | docs(uat): re-walk rows 206/207 (#4002 Crossplane) — ⛔ featu |
| 2026-06-23T06:20 | [#4165](https://github.com/openova-io/openova/pull/4165) | #4157 | fix(bp-keycloak): declare browser-no-idp forms subflow so co |
| 2026-06-23T05:44 | [#4164](https://github.com/openova-io/openova/pull/4164) | #4002 | docs(uat): record 2026-06-23 env-state HR convergence + rema |
| 2026-06-23T05:45 | [#4162](https://github.com/openova-io/openova/pull/4162) | #4 | fix(catalyst-api): sync keycloak master-realm admin Secret p |
| 2026-06-23T05:05 | [#4161](https://github.com/openova-io/openova/pull/4161) | #4160 | fix(catalog-seed): bump bp-wordpress-tenant pin 0.4.4->0.4.1 |
| 2026-06-23T04:50 | [#4159](https://github.com/openova-io/openova/pull/4159) | #4086 | fix: converge secondary-region vc-mgmt — cross-region mangle |
| 2026-06-23T04:09 | [#4157](https://github.com/openova-io/openova/pull/4157) | #3642 | fix(bp-keycloak): account-console SSO-landing — non-delegati |
| 2026-06-23T03:41 | [#4156](https://github.com/openova-io/openova/pull/4156) | #4049 | fix(provisioning): bastion-Harbor warmup fires on every bp-* |
| 2026-06-23T00:48 | [#4155](https://github.com/openova-io/openova/pull/4155) | #4152 | fix(bp-wordpress-tenant): reflector populates WORDPRESS_DB_P |
| 2026-06-22T23:52 | [#4154](https://github.com/openova-io/openova/pull/4154) | #4153 | fix(bp-wordpress-tenant): add wordpress.image.pullSecrets fo |
| 2026-06-22T21:57 | [#4153](https://github.com/openova-io/openova/pull/4153) | #800 | feat(bp-wordpress-tenant): custom WordPress image w/ pdo_pgs |
| 2026-06-22T21:12 | [#4151](https://github.com/openova-io/openova/pull/4151) | #4147 | fix(bp-wordpress-tenant): apt sandbox-setuid fails under dro |
| 2026-06-22T18:33 | [#4150](https://github.com/openova-io/openova/pull/4150) | #3373 | fix(org-provisioning): per-Org bp-newapi overlay wires never |
| 2026-06-22T18:24 | [#4149](https://github.com/openova-io/openova/pull/4149) | #817 | fix(bp-stalwart-tenant): converge per-Org Stalwart on fresh  |
| 2026-06-22T17:44 | [#4147](https://github.com/openova-io/openova/pull/4147) | #3971 | fix(bp-wordpress-tenant): wp-plugin-install init runs as roo |
| 2026-06-22T18:24 | [#4146](https://github.com/openova-io/openova/pull/4146) | #4053 | fix(SSO): per-Org realm registers catalyst-ui + Org keycloak |
| 2026-06-22T15:38 | [#4142](https://github.com/openova-io/openova/pull/4142) | #4140 | fix(#4139): bp-wordpress-tenant active-hot-standby CNPG sync |
| 2026-06-22T15:23 | [#4141](https://github.com/openova-io/openova/pull/4141) | #3985 | fix(#4139): unblock bp-wordpress-tenant publish — repair #39 |
| 2026-06-22T15:16 | [#4140](https://github.com/openova-io/openova/pull/4140) | #3971 | fix(#4139): demo-Org convergence — empty wpContent.storageCl |
| 2026-06-22T16:26 | [#4138](https://github.com/openova-io/openova/pull/4138) | #4110 | fix(openova-mcp): org-admin tier → create_application lands  |
| 2026-06-22T14:37 | [#4137](https://github.com/openova-io/openova/pull/4137) | #3263 | fix(bp-valkey): Bitnami valkey/os-shell images via on-Sovere |
| 2026-06-22T12:10 | [#4121](https://github.com/openova-io/openova/pull/4121) | #4112 | fix(catalyst): SECURITY #4110 — host-anchor Org-console scop |
| 2026-06-22T11:03 | [#4120](https://github.com/openova-io/openova/pull/4120) | #4110 | fix(catalyst): Org console — project the Org's OWN apps + ki |
| 2026-06-22T10:13 | [#4117](https://github.com/openova-io/openova/pull/4117) | #4110 | feat(catalyst+mcp): Org-scoped Application install — custome |
| 2026-06-22T08:50 | [#4115](https://github.com/openova-io/openova/pull/4115) | #4114 | fix(agenity): make the chat->provision North Star work live  |
| 2026-06-22T08:38 | [#4112](https://github.com/openova-io/openova/pull/4112) | #4110 | fix(catalyst): SECURITY — customer Org console must be Org-s |

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
