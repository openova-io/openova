# OpenOva — Status Tracker

Regenerated every 15 min by `/home/openova/bin/refresh-dod-dashboard.sh`. Every `#NNNN` is a clickable GitHub link.

|  |  |
|---|---|
| Last refreshed | `2026-06-21T14:15:02Z` |
| Deploy cron (#799) | ✓ deploy-cron healthy (image-reroll last ran 9m ago) |
| Open issues | 71 |
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

**All 71 open issues** grouped by where they sit in the convergence sequence. Each chain runs left-to-right; chains stack vertically.

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

### All 71 open items (clickable table)

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
| [#3760](https://github.com/openova-io/openova/issues/3760) | fix(org-controller): per-Org vCluster StatefulSet DENIED by kyverno harbor-proxy | Other |
| [#3785](https://github.com/openova-io/openova/issues/3785) | FUNNEL: customer's purchased WordPress image is kyverno-DENIED (not Harbor-proxi | Other |
| [#3821](https://github.com/openova-io/openova/issues/3821) | hw165: Pillar-5 cutover wedged at step 1/11 — cutover-gitea-mirror Job (ns cat | Other |
| [#3824](https://github.com/openova-io/openova/issues/3824) | hw165: openbao-snapshot-save CreateContainerConfigError — seeder can't read rt | Other |
| [#3829](https://github.com/openova-io/openova/issues/3829) | Every application AND activity shows its TRUE live state end-to-end — every ap | Other |
| [#3830](https://github.com/openova-io/openova/issues/3830) | Create-instance fails for EVERY Organization: ensureOrgNamespace uses the Org FQ | Other |
| [#3840](https://github.com/openova-io/openova/issues/3840) | cloud-init: disable IPv6 on Huawei nodes so ghcr image-pull resolution is DETERM | Other |
| [#3844](https://github.com/openova-io/openova/issues/3844) | oidc-gate pods CrashLoop on Huawei/kom4dc: OIDC discovery dials the Sovereign's  | Other |
| [#3850](https://github.com/openova-io/openova/issues/3850) | fix(bp-gitea): SSO oauth source 'openova-sso' is lost when CNPG DB re-inits —  | Other |
| [#3854](https://github.com/openova-io/openova/issues/3854) | fix(bp-catalyst-platform): #3383 rename tail — provisioning-github-token + val | Other |
| [#3859](https://github.com/openova-io/openova/issues/3859) | fix(org-services + per-Org vCluster): sme→org-services rename orphaned 3 platf | Other |
| [#3878](https://github.com/openova-io/openova/issues/3878) | fix(bp-postgres/bp-mgmt-vcluster): deliver the 4 pod-mounted DB secrets natively | Other |
| [#3888](https://github.com/openova-io/openova/issues/3888) | FOUNDATION: thin the bootstrap cloud-init — evict powerdns-api-credentials to  | Other |
| [#3889](https://github.com/openova-io/openova/issues/3889) | Fresh prov lost mid-convergence: unpersisted deployment record + mothership disk | Other |
| [#3891](https://github.com/openova-io/openova/issues/3891) | FOUNDATION: prov-time openbao-KV-seed mechanism → evict provably-safe inline c | Other |
| [#3892](https://github.com/openova-io/openova/issues/3892) | Mothership disk self-maintenance + pre-fire resource preflight gate (durable hw1 | Other |
| [#3895](https://github.com/openova-io/openova/issues/3895) | Jobs page: lifecycle parent label hardcoded "Provision Hetzner" on non-Hetzner ( | Other |
| [#3896](https://github.com/openova-io/openova/issues/3896) | Jobs page: in-cluster convergence Jobs/CronJobs/Kustomizations invisible during  | Other |
| [#3898](https://github.com/openova-io/openova/issues/3898) | Funnel: a crashed/rolled provisioning Pod strands the in-flight provision row � | Other |
| [#3901](https://github.com/openova-io/openova/issues/3901) | fix(bp-openbao): host-token-reviewer-export Job script ${...} vars eaten by Flux | Other |
| [#3903](https://github.com/openova-io/openova/issues/3903) | fix(sovereign-tls): cilium-envoy-tls-restart Job uses bare alpine/k8s — harbor | Other |
| [#3905](https://github.com/openova-io/openova/issues/3905) | Topology placement-edit dropdown shows generic mode descriptions that contradict | Other |
| [#3906](https://github.com/openova-io/openova/issues/3906) | fix(bp-catalyst-platform): PIN-login secret catalyst-openova-kc-credentials hits | Other |
| [#3913](https://github.com/openova-io/openova/issues/3913) | fix(platform): close the un-proxied-image class — harbor-proxy sigstore/trivy/ | Other |
| [#3914](https://github.com/openova-io/openova/issues/3914) | Provisioning UX: fill the ~30-min 'Provision <provider>: Success' void with a li | Other |
| [#3922](https://github.com/openova-io/openova/issues/3922) | Catalog 'New instance' create: HTTP 500 (dotted environmentRef) + topology <sele | Other |
| [#3925](https://github.com/openova-io/openova/issues/3925) | Convergence Monitor — redesign provisioning monitoring into 2 native surfaces  | Other |
| [#3926](https://github.com/openova-io/openova/issues/3926) | fix(bp-self-sovereign-cutover): step-02 harbor-projects wedges every post-#3642  | Other |
| [#3934](https://github.com/openova-io/openova/issues/3934) | SSO-landing regression (#3642 mgmt-vCluster pivot): gitea 500 / harbor login-for | Other |
| [#3942](https://github.com/openova-io/openova/issues/3942) | fix(catalog): bp-wordpress (and any seeded-but-uncatalogued Blueprint) absent fr | Other |
| [#3944](https://github.com/openova-io/openova/issues/3944) | fix(bp-self-sovereign-cutover): step-03 harbor-prewarm stalls 'active' for hours | Other |
| [#3946](https://github.com/openova-io/openova/issues/3946) | fix(bp-catalyst-platform): catalyst-gitea-token-mint pre-install hook Job DENIED | Other |
| [#3948](https://github.com/openova-io/openova/issues/3948) | fix(bp-mgmt-vcluster,bp-newapi): #3642 spine regressions on hw172 — gitea-pg-a | Other |
| [#3952](https://github.com/openova-io/openova/issues/3952) | fix(bp-mgmt-vcluster): #3642 secret+service cascade on hw172 — harbor/seaweedf | Other |
| [#3955](https://github.com/openova-io/openova/issues/3955) | fix(bootstrap): route private OpenOva images through the harbor cache — elimin | Other |
| [#3969](https://github.com/openova-io/openova/issues/3969) | EPIC: Application-centric Placement (Primary/Standby·Hot/Cold, capability-gated | Other |
| [#3970](https://github.com/openova-io/openova/issues/3970) | Unified Cloud view: a lens IS a named chip-set (not a filter layer) — default  | Other |
| [#3971](https://github.com/openova-io/openova/issues/3971) | P0 BLOCKER: every Sovereign's PVCs land on k3s local-path (ephemeral node disk)  | Other |
| [#3974](https://github.com/openova-io/openova/issues/3974) | fix(catalyst-api): slim the 457MB image — UPX the bundled tofu/helm/kubectl/Go | Other |
| [#3978](https://github.com/openova-io/openova/issues/3978) | fix(cloud-view): RESTORE rich per-kind /cloud?view=list — #3970 regressed it t | Other |
| [#3979](https://github.com/openova-io/openova/issues/3979) | Cloud GRAPH view: 4 fixes — default=Graph · converge≤4s-then-STOP · ArchiM | Other |
| [#3980](https://github.com/openova-io/openova/issues/3980) | Cloud GRAPH refinements: default-to-graph, force-sim convergence, merge ArchiMat | Other |
| [#3982](https://github.com/openova-io/openova/issues/3982) | Topology: derive REAL placement from live runtime — every component falsely sh | Other |
| [#3985](https://github.com/openova-io/openova/issues/3985) | ERADICATE the dead 'SME' concept → Organization across ALL code/config/charts  | Other |
| [#3986](https://github.com/openova-io/openova/issues/3986) | Topology placement STILL empty for vCluster-synced apps — #3984 matcher misses | Other |
| [#3987](https://github.com/openova-io/openova/issues/3987) | Cloud list: per-kind k9s pages show 0 objects for many resource types (helmrelea | Other |
| [#3988](https://github.com/openova-io/openova/issues/3988) | EPIC: OpenOva MCP server — RBAC-scoped-per-user thin facade over the shared au | Other |
| [#3990](https://github.com/openova-io/openova/issues/3990) | hw173 in-cluster catalyst-api cannot reach region-b apiserver (DNAT'd EIP) — m | Other |
| [#3991](https://github.com/openova-io/openova/issues/3991) | hw173 in-cluster catalyst-api cannot reach region-b apiserver (DNAT'd EIP) — t | Other |
| [#3996](https://github.com/openova-io/openova/issues/3996) | feat(cloud): lightweight ArgoCD-like reconciler management — drill + logs + re | Other |
| [#3998](https://github.com/openova-io/openova/issues/3998) | fix(cloud): network+security coverage gap — Gateway/HTTPRoute/NetworkPolicy/Ci | Other |
| [#4000](https://github.com/openova-io/openova/issues/4000) | Per-app Topology shows false 'singleton' on multi-region Sovereign — secondary | Other |
| [#4002](https://github.com/openova-io/openova/issues/4002) | ARCH P0: OpenTofu→Crossplane adoption seam never implemented — Crossplane ow | Other |
| [#4003](https://github.com/openova-io/openova/issues/4003) | fix(fleet): /fleet/applications returns 0 apps on a Sovereign with live HelmRele | Other |
| [#4010](https://github.com/openova-io/openova/issues/4010) | EPIC: bp-chepherd — chepherd as a deployable Application (Helm chart) + solo-a | Other |
| [#4018](https://github.com/openova-io/openova/issues/4018) | fix(cloud): Path B — provisioning instantiates Crossplane XRCs so the XRC laye | Other |
| [#4030](https://github.com/openova-io/openova/issues/4030) | fix(bp-huawei-evs-csi): chart 1.0.1 helm install fails — VolumeSnapshotClass r | Other |
| [#4037](https://github.com/openova-io/openova/issues/4037) | bp-huawei-evs-csi: private driver image has no imagePullSecret → CSI driver Im | Other |
| [#4044](https://github.com/openova-io/openova/issues/4044) | fix(bp-huawei-evs-csi): slot 55b must dependsOn bp-reflector — ghcr-pull race  | Other |
| [#4046](https://github.com/openova-io/openova/issues/4046) | fix(huawei-prov): janitor SG-leak + EIP-egress cooldown preflight (stop region-a | Other |
| [#4049](https://github.com/openova-io/openova/issues/4049) | fix(provisioning): bastion Harbor warmup — every image Huawei→Huawei, kill / | Other |
| [#4053](https://github.com/openova-io/openova/issues/4053) | fix(bp-cilium): one broken backend Service poisons the whole gateway CEC → ent | Other |

---

## 3. Recently merged PRs (last 36h)

| Merged | PR | Issue closed | Title |
|---|---|---|---|
| 2026-06-21T13:43 | [#4052](https://github.com/openova-io/openova/pull/4052) | #4037 | fix(bootstrap-kit): gate stateful root slots on BOTH per-pro |
| 2026-06-21T12:03 | [#4051](https://github.com/openova-io/openova/pull/4051) | #4049 | fix(bp-catalyst-platform): bp-chepherd missing required topo |
| 2026-06-21T09:51 | [#4048](https://github.com/openova-io/openova/pull/4048) | #3913 | fix(huawei): route ghcr/quay/gcr/k8s through bastion NAT-byp |
| 2026-06-21T08:16 | [#4045](https://github.com/openova-io/openova/pull/4045) | #4038 | fix(bp-huawei-evs-csi): slot 55b dependsOn bp-reflector — cl |
| 2026-06-21T07:16 | [#4043](https://github.com/openova-io/openova/pull/4043) | #3985 | fix(catalog+settings): eradicate residual Catalyst-Organizat |
| 2026-06-21T07:01 | [#4042](https://github.com/openova-io/openova/pull/4042) | #3987 | fix(cloud-view): scope Nodes to the deployment + evict wiped |
| 2026-06-21T07:01 | [#4041](https://github.com/openova-io/openova/pull/4041) | #3971 | fix(kyverno): exclude CNPG-managed PVCs from pvc-must-be-vel |
| 2026-06-21T07:00 | [#4040](https://github.com/openova-io/openova/pull/4040) | #3988 | feat(mcp): create_application write-tool, RBAC-scoped per Or |
| 2026-06-21T07:00 | [#4039](https://github.com/openova-io/openova/pull/4039) | #4017 | fix(console): flip 8 remaining user-visible 'tenant'→'Organi |
| 2026-06-21T07:01 | [#4038](https://github.com/openova-io/openova/pull/4038) | #4030 | fix(bp-huawei-evs-csi): imagePullSecret for private driver i |
| 2026-06-21T04:14 | [#4036](https://github.com/openova-io/openova/pull/4036) | #4035 | docs(uat): walk-ready acceptance for e2e rows 216-218 (surfa |
| 2026-06-21T04:00 | [#4035](https://github.com/openova-io/openova/pull/4035) | #4034 | docs(uat): enrich e2e rows 221+223 acceptance — concrete MCP |
| 2026-06-21T03:44 | [#4034](https://github.com/openova-io/openova/pull/4034) | #4011 | docs(uat): e2e Chepherd north-star journey walkthrough (rows |
| 2026-06-21T03:28 | [#4033](https://github.com/openova-io/openova/pull/4033) | #4031 | docs(uat): cycle-2c walk runbook — systematic hw177 35-row f |
| 2026-06-21T02:50 | [#4032](https://github.com/openova-io/openova/pull/4032) | #4030 | docs(uat): stamp pending hw177 for cycle-2c re-fire of the # |
| 2026-06-21T02:41 | [#4031](https://github.com/openova-io/openova/pull/4031) | #4022 | fix(bp-huawei-evs-csi): default volumeSnapshotClass off — fi |
| 2026-06-21T07:00 | [#4029](https://github.com/openova-io/openova/pull/4029) | #3969 | feat(console): wire live Continuum DR status into the reacha |
| 2026-06-21T07:00 | [#4028](https://github.com/openova-io/openova/pull/4028) | #3376 | fix(gateway): per-IP burst rate-limit on the voucher-redeem  |
| 2026-06-21T01:37 | [#4022](https://github.com/openova-io/openova/pull/4022) | #3971 | fix(bp-huawei-evs-csi): promote idc driver + EVS_CSI_ENABLED |
| 2026-06-21T07:00 | [#4021](https://github.com/openova-io/openova/pull/4021) | #4000 | fix(topology-ui): bootstrap placement queries empty namespac |
| 2026-06-21T01:07 | [#4020](https://github.com/openova-io/openova/pull/4020) | #4012 | fix(bp-huawei-evs-csi): publish 1.0.0 — add no-upstream anno |
| 2026-06-21T00:58 | [#4019](https://github.com/openova-io/openova/pull/4019) | #3998 | fix(cloud-view): surface the REAL front-door LB from the dep |
| 2026-06-21T00:43 | [#4017](https://github.com/openova-io/openova/pull/4017) | #3985 | fix(console): flip 18 user-visible 'tenant'→'Organization' s |
| 2026-06-21T00:36 | [#4016](https://github.com/openova-io/openova/pull/4016) | #3964 | fix(funnel): unblock coupon→Org→ACTIVE — provisioning image  |
| 2026-06-21T00:19 | [#4015](https://github.com/openova-io/openova/pull/4015) | #4001 | fix(catalyst-api): deliver region-b kubeconfig to in-cluster |
| 2026-06-21T00:04 | [#4014](https://github.com/openova-io/openova/pull/4014) | #3996 | fix(catalyst-rbac): cutover-driver SA can patch Flux reconci |
| 2026-06-21T00:06 | [#4013](https://github.com/openova-io/openova/pull/4013) | #3971 | fix(bp-huawei-evs-csi): idc no-Keystone AK/SK bypass — EVS p |
| 2026-06-21T00:05 | [#4012](https://github.com/openova-io/openova/pull/4012) | #3971 | fix(bootstrap): Huawei EVS CSI blueprint + slot 55b — Huawei |
| 2026-06-21T07:18 | [#4011](https://github.com/openova-io/openova/pull/4011) | #4010 | feat(bp-chepherd): chepherd as an installable Application +  |
| 2026-06-20T19:45 | [#4008](https://github.com/openova-io/openova/pull/4008) | #3988 | docs(uat): extend the canonical 186-case table → 215 with th |

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
