# OpenOva — Status Tracker

Regenerated every 15 min by `/home/openova/bin/refresh-dod-dashboard.sh`. Every `#NNNN` is a clickable GitHub link.

|  |  |
|---|---|
| Last refreshed | `2026-08-11T04:15:04Z` |
| Deploy cron (#799) | ✓ deploy-cron healthy (image-reroll last ran 9m ago) |
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
| [#5602](https://github.com/openova-io/openova/issues/5602) | Hubble UI: authenticated landing works but data streams never connect (persisten | Other |
| [#5608](https://github.com/openova-io/openova/issues/5608) | Keycloak admin console: group Role-mapping tab crashes ('Cannot read properties  | Other |
| [#5611](https://github.com/openova-io/openova/issues/5611) | Cloud list: Volumes claims 0 while 50 EVS block volumes are attached (Storage Cl | Other |
| [#5612](https://github.com/openova-io/openova/issues/5612) | newapi session expires in under an hour and the 'Continue with OpenOva SSO' reco | Other |
| [#5623](https://github.com/openova-io/openova/issues/5623) | Region-kill: the three shared-pg DR pairs never promote — only bp-cnpg-pair sh | Other |
| [#5635](https://github.com/openova-io/openova/issues/5635) | Per-Org app FQDN fails ~50% of fresh connections — single-region namespace beh | Other |
| [#5642](https://github.com/openova-io/openova/issues/5642) | catalyst-api OOMKilling in a loop on hw292 (15 restarts, requests 96Mi vs limits | Other |
| [#5650](https://github.com/openova-io/openova/issues/5650) | Post-cutover Sovereign fetches charts.loft.sh every 15m: step-08's deny-egress h | Other |
| [#5752](https://github.com/openova-io/openova/issues/5752) | bp-stalwart-tenant: per-Org install door emits empty spec.parameters — domain/ | Other |
| [#5759](https://github.com/openova-io/openova/issues/5759) | sovereign-daytwo-bootstrap --apply REVERTS a completed cutover: 62/69 HelmReposi | Other |
| [#5762](https://github.com/openova-io/openova/issues/5762) | products/catalyst/bootstrap/ui e2e + marketplace customer-journey: triage the 16 | Other |
| [#5796](https://github.com/openova-io/openova/issues/5796) | ESCALATION: uatco-mail-rtz-a annotation directive conflicts with a founder 'No'  | Other |
| [#5799](https://github.com/openova-io/openova/issues/5799) | Funnel E2E walk is gated on retrieving the emailed sign-in PIN — no mail surfa | Other |
| [#5814](https://github.com/openova-io/openova/issues/5814) | Sovereign /apps grid carries no Organization attribution — a customer-launched | Other |
| [#5817](https://github.com/openova-io/openova/issues/5817) | Org detail page title-cases identifiers: slug `uatco` renders `Uatco`, owner `em | Other |
| [#5819](https://github.com/openova-io/openova/issues/5819) | Showback names the parent Organization `hw292.omani.works` while the directory a | Other |
| [#5821](https://github.com/openova-io/openova/issues/5821) | /version reports process START time as `buildTime` with no way to tell — hw292 | Other |
| [#5823](https://github.com/openova-io/openova/issues/5823) | Install wizard never pre-selects the Organization — even on a per-Org console  | Other |
| [#5825](https://github.com/openova-io/openova/issues/5825) | Two identity readers in one header disagree: the sidebar shows the signed-in own | Other |
| [#5827](https://github.com/openova-io/openova/issues/5827) | GET /applications/catalyst-api 404s while GET /applications/catalyst-api/placeme | Other |
| [#5829](https://github.com/openova-io/openova/issues/5829) | bp-openbao: a failed ACL-policy write is swallowed, then the OIDC role names a p | Other |
| [#5831](https://github.com/openova-io/openova/issues/5831) | Nine page components are reachable from nothing but their own tests — includin | Other |
| [#5833](https://github.com/openova-io/openova/issues/5833) | GET /api/v1/organizations double-counts an Organization — the store/CR merge k | Other |
| [#5835](https://github.com/openova-io/openova/issues/5835) | GET /applications/{name}/status 404s for every bootstrap component while its own | Other |
| [#5843](https://github.com/openova-io/openova/issues/5843) | per-Org GitOps renderer emits no openova-mcp instance at Org-create — decide w | Other |
| [#5847](https://github.com/openova-io/openova/issues/5847) | UAT row 5 asserts TIER=sme, a value the Organization CRD 422-rejects — the row | Other |
| [#5848](https://github.com/openova-io/openova/issues/5848) | Org delete leaves an orphaned HTTPRoute: teardown reaps by derived NAME while cr | Other |
| [#5857](https://github.com/openova-io/openova/issues/5857) | Door A stamps every customer Org isolation=vcluster while the GitOps renderer ba | Other |
| [#5867](https://github.com/openova-io/openova/issues/5867) | Two UAT rows need an owner decision and have no tracker issue: row 19 (grid keye | Other |
| [#5871](https://github.com/openova-io/openova/issues/5871) | hw292 live: 11 of 14 Applications are Degraded on a cutoverComplete=true Soverei | Other |
| [#5878](https://github.com/openova-io/openova/issues/5878) | newapi is the only SSO app that does not accept the realm session — bare URL 3 | Other |
| [#5882](https://github.com/openova-io/openova/issues/5882) | OpenBao SSO session lands with no token prompt but its policy cannot list mounts | Other |
| [#5883](https://github.com/openova-io/openova/issues/5883) | A mistyped email at the PIN form persists a Keycloak user — sovereign realm ho | Other |
| [#5887](https://github.com/openova-io/openova/issues/5887) | Console does not silently re-authenticate from a live Keycloak realm session — | Other |
| [#5894](https://github.com/openova-io/openova/issues/5894) | P1: per-Org consoles flap ~50% on TLS handshake reset — affects BOTH customer  | Other |
| [#5895](https://github.com/openova-io/openova/issues/5895) | P1: first visit to a per-Org console dead-ends on a BLANK page — prompt=none s | Other |
| [#5910](https://github.com/openova-io/openova/issues/5910) | Row 95: a purchased app silently vanishes — resolveAppSlugs substitutes the ra | Other |
| [#5919](https://github.com/openova-io/openova/issues/5919) | hw292 cut over on chart 0.1.159 — twelve versions before the #5710 durable piv | Other |
| [#5920](https://github.com/openova-io/openova/issues/5920) | Marketplace still sells 'Sandbox' — a concept retired 2026-06-30 — and a sou | Other |
| [#5921](https://github.com/openova-io/openova/issues/5921) | P0 sovereignty: post-cutover Sovereign sends customer sign-in mail via mail.open | Other |
| [#5926](https://github.com/openova-io/openova/issues/5926) | UAT row 85: checkout renders payment-method tiles + Stripe redirect copy on a fu | Other |
| [#5929](https://github.com/openova-io/openova/issues/5929) | UAT row 164: a terminal Failed Job produces no failed leaf — an unterminated l | Other |
| [#5932](https://github.com/openova-io/openova/issues/5932) | Dashboard treemap: the vCluster grouping dimension keys on the retired #4325 vcl | Other |
| [#5933](https://github.com/openova-io/openova/issues/5933) | Customer Application CRs never carry spec.organizationRef — the #5814 Org-attr | Other |
| [#5934](https://github.com/openova-io/openova/issues/5934) | TopologyTab hard-renders 'n/a — bootstrap component' regardless of the API ans | Other |
| [#5940](https://github.com/openova-io/openova/issues/5940) | Owner opening a voucher-redeem link is shown a signup form — the marketplace c | Other |
| [#5943](https://github.com/openova-io/openova/issues/5943) | Organization DELETE resolves a different identifier than GET — the console's o | Other |
| [#5945](https://github.com/openova-io/openova/issues/5945) | Placement editor offers retired vCluster names (host/mgmt/dmz/rtz) and preselect | Other |
| [#5955](https://github.com/openova-io/openova/issues/5955) | P1: Application reports Ready=True over a database with ZERO ready instances — | Other |
| [#5956](https://github.com/openova-io/openova/issues/5956) | P0: the Anthropic credential in openbao is REVOKED — every health surface repo | Other |
| [#5975](https://github.com/openova-io/openova/issues/5975) | check-release-lockstep-writer.py leaks multi-GB scratch clones, then reports the | Other |
| [#5981](https://github.com/openova-io/openova/issues/5981) | harbor host answers 301 on half of fresh connections and envoy 404 on the other  | Other |
| [#5982](https://github.com/openova-io/openova/issues/5982) | DR runbook preflight reports Pass for surfaces it never probes — cnpgpair-stre | Other |
| [#5984](https://github.com/openova-io/openova/issues/5984) | guacamole.properties renders the DB password in plaintext — fine on our own in | Other |
| [#5986](https://github.com/openova-io/openova/issues/5986) | sso-uat-flip destroyed 13 correct verdicts on a commit that cannot affect an SSO | Other |
| [#5987](https://github.com/openova-io/openova/issues/5987) | Per-Org bp-newapi cannot install: the funnel HR supplies no catalystIntegration. | Other |
| [#5991](https://github.com/openova-io/openova/issues/5991) | Guacamole connections list is empty by construction: no producer exists, and the | Other |
| [#5993](https://github.com/openova-io/openova/issues/5993) | pod-truth reconciler is vcluster-only: host-tier (plan free/S) pods are invisibl | Other |
| [#6004](https://github.com/openova-io/openova/issues/6004) | P0: fresh prov fails Phase 1 — bp-self-sovereign-cutover Helm release Secret e | Other |
| [#6015](https://github.com/openova-io/openova/issues/6015) | catalyst-api is region-b-blind on a 2-region Sovereign — empty kubeconfigs dir | Other |
| [#6016](https://github.com/openova-io/openova/issues/6016) | region-b wedged at 57/65 — keycloak-database-secret never materialised in regi | Other |
| [#6027](https://github.com/openova-io/openova/issues/6027) | Per-Org console listeners still absent from region B with #5957 deployed — the | Other |
| [#6028](https://github.com/openova-io/openova/issues/6028) | Region B serves 503 on half of all Sovereign front-door connections — HTTPRout | Other |
| [#6031](https://github.com/openova-io/openova/issues/6031) | UAT row 57 has no fix anywhere — bp-cnpg-pair ships cnpgPair.enabled=false, so | Other |
| [#6032](https://github.com/openova-io/openova/issues/6032) | Application reports Ready over ANOTHER release's datastore — a second bp-postg | Other |
| [#6033](https://github.com/openova-io/openova/issues/6033) | active-hot-standby Application reports phase=Ready across 1 region with perClust | Other |
| [#6040](https://github.com/openova-io/openova/issues/6040) | hw293: region-B ClusterMesh never established — a dormant chart's census failu | Other |
| [#6043](https://github.com/openova-io/openova/issues/6043) | bp-catalyst-platform cannot publish: unresolved merge-conflict markers on main f | Other |
| [#6045](https://github.com/openova-io/openova/issues/6045) | hw293: four live defects surfaced by the 2026-08-11 UAT partition — an unpubli | Other |
| [#6058](https://github.com/openova-io/openova/issues/6058) | Flaky required gate: TestSecondaryKubeconfigDelivery_RunsOnFailedDeployment_6015 | Other |
| [#6060](https://github.com/openova-io/openova/issues/6060) | UAT row 63: TopologyTab returns on the status.perCluster rung and never reads st | Other |
| [#6062](https://github.com/openova-io/openova/issues/6062) | UAT row 8: GET /sovereign/parent-domains offers no org-pool parents — the list | Other |
| [#6071](https://github.com/openova-io/openova/issues/6071) | Continuum never reports a standby: leaseClient resolvers are hardcoded to 10.43. | Other |
| [#6072](https://github.com/openova-io/openova/issues/6072) | shared-pg consumer-hub sync: the -mesh-rw readiness gate tests a string that can | Other |
| [#6075](https://github.com/openova-io/openova/issues/6075) | Org detail never renders the purchased plan: spec.planSlug dropped at four layer | Other |
| [#6076](https://github.com/openova-io/openova/issues/6076) | Organizations parent row has an unstable identity: link target flips between /or | Other |
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
| [#6136](https://github.com/openova-io/openova/issues/6136) | Topology-tab Save writes spec.placement as a bare string: the editor's targets[] | Other |
| [#6140](https://github.com/openova-io/openova/issues/6140) | hw293: a console-gateway hostname probed on the SHARED gateway's host port retur | Other |
| [#6145](https://github.com/openova-io/openova/issues/6145) | Organization isolation is reported from the DECLARED field, not the observed bac | Other |
| [#6148](https://github.com/openova-io/openova/issues/6148) | Region-kill failback: recovered region-A returns as an UNFENCED TL=1 primary and | Other |
| [#6149](https://github.com/openova-io/openova/issues/6149) | bp-postgres DR pairs ship a dr-promoter but NO dr-failback — region-A return l | Other |

---

## 3. Recently merged PRs (last 36h)

| Merged | PR | Issue closed | Title |
|---|---|---|---|
| 2026-08-11T04:01 | [#6144](https://github.com/openova-io/openova/pull/6144) | #6054 | fix(catalyst): a usable secondary kubeconfig is not thereby  |
| 2026-08-11T04:02 | [#6138](https://github.com/openova-io/openova/pull/6138) | #5857 | fix(catalyst-api): refuse a declared isolation the plan cann |
| 2026-08-11T03:25 | [#6137](https://github.com/openova-io/openova/pull/6137) | #4466 | fix(catalyst-chart): the janitor's whole env surface was unr |
| 2026-08-11T03:26 | [#6134](https://github.com/openova-io/openova/pull/6134) | #6099 | fix(jobs): the cutover tree had no root — an empty dependsOn |
| 2026-08-11T03:12 | [#6133](https://github.com/openova-io/openova/pull/6133) | #6132 | test(uat): the new self-test mutants must land in the CLAUSE |
| 2026-08-11T02:56 | [#6132](https://github.com/openova-io/openova/pull/6132) | #6111 | docs(uat): finish the #6111 adjudication — G6 gets an assert |
| 2026-08-11T02:58 | [#6130](https://github.com/openova-io/openova/pull/6130) | #6085 | fix(console): stop publishing a DR/suspend verdict from abse |
| 2026-08-11T03:11 | [#6129](https://github.com/openova-io/openova/pull/6129) | #6092 | test(org-delete): pin the cluster-wide secrets rule read-onl |
| 2026-08-11T02:46 | [#6126](https://github.com/openova-io/openova/pull/6126) | #4303 | fix(seed): verify the self-heal instead of announcing it (Re |
| 2026-08-11T02:26 | [#6125](https://github.com/openova-io/openova/pull/6125) | #3840 | fix(bootstrap-kit): the Flux controllers must not be the onl |
| 2026-08-11T02:22 | [#6123](https://github.com/openova-io/openova/pull/6123) | #5206 | docs(uat): rows 212/213 walked on hw293 — the per-Org MCP in |
| 2026-08-11T01:55 | [#6121](https://github.com/openova-io/openova/pull/6121) | #6107 | fix(org): an unusable secondary kubeconfig is a MISSING arti |
| 2026-08-11T01:42 | [#6119](https://github.com/openova-io/openova/pull/6119) | #6004 | fix(guard): a region must not advertise a hostname it cannot |
| 2026-08-11T01:38 | [#6117](https://github.com/openova-io/openova/pull/6117) | #5957 | docs(path-to-100): partition the hw293 UAT failure residue b |
| 2026-08-11T02:24 | [#6116](https://github.com/openova-io/openova/pull/6116) | #6108 | fix(catalyst-api): stop CREATING torn kubeconfigs, not just  |
| 2026-08-11T01:32 | [#6115](https://github.com/openova-io/openova/pull/6115) | #4416 | docs(uat): adjudicate the clauses that assert objects the pl |
| 2026-08-11T01:40 | [#6113](https://github.com/openova-io/openova/pull/6113) | #6055 | docs(uat): hw293 live-walk — 5 cutover/meta rows pass, 162 f |
| 2026-08-11T01:35 | [#6112](https://github.com/openova-io/openova/pull/6112) | #6015 | fix(catalyst-api): the secondary-kubeconfig delivery leg mus |
| 2026-08-11T01:25 | [#6110](https://github.com/openova-io/openova/pull/6110) | #5175 | fix(openova-mcp): surface the authoritative /whoami verdict, |
| 2026-08-11T00:56 | [#6105](https://github.com/openova-io/openova/pull/6105) | #6089 | docs(uat): hw293 SSO re-walk — 9 green, 8 catalyst-pin broke |
| 2026-08-11T01:26 | [#6104](https://github.com/openova-io/openova/pull/6104) | #6015 | fix(catalyst-api): the peer-region credential Secret must no |
| 2026-08-11T00:33 | [#6103](https://github.com/openova-io/openova/pull/6103) | #5958 | docs(uat): rows 100 + 238 + 229 flip on live region reads; R |
| 2026-08-11T00:22 | [#6102](https://github.com/openova-io/openova/pull/6102) | #5583 | fix(scripts): bump-chart-version consults open PR heads befo |
| 2026-08-11T00:05 | [#6101](https://github.com/openova-io/openova/pull/6101) | #6091 | fix(catalyst-api): census the converged-late rescue over the |
| 2026-08-10T23:51 | [#6100](https://github.com/openova-io/openova/pull/6100) | #6092 | test(org-delete): make the refused Secret delete reachable — |
| 2026-08-11T00:59 | [#6099](https://github.com/openova-io/openova/pull/6099) | #3646 | fix(jobs,openbao): the cutover tree was ordered by the alpha |
| 2026-08-10T23:53 | [#6098](https://github.com/openova-io/openova/pull/6098) | #5956 | docs(uat): e2e-journey walked end to end on hw293 — 216/217/ |
| 2026-08-11T00:03 | [#6097](https://github.com/openova-io/openova/pull/6097) | #6027 | fix(org): two per-Org console defects on hw293 — the region  |
| 2026-08-10T23:34 | [#6095](https://github.com/openova-io/openova/pull/6095) | #6051 | docs(uat): row 107 passes on a real delete; R17's cause move |
| 2026-08-11T00:58 | [#6094](https://github.com/openova-io/openova/pull/6094) | #5985 | fix(catalyst-api): the DR runbook preflight must measure wha |

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
