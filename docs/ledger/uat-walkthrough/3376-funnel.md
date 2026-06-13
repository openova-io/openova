## #3376 FUNNEL

**End goal:** a stranger holding a voucher ends **signed in inside their own org's console with the purchased app RUNNING.** Terminal evidence = WordPress serving inside `console.<orgslug>.omani.homes`, reached zero-click as the org owner. Org-created / provisioning-started / handover-minted are NOT the finish line.

### Phase A — Operator mints the voucher (BSS, DoD-6)
| # | Go to | Do | Expect | Source | ☐ |
|---|---|---|---|---|---|
| 1-3 | operator `/billing` | "Vouchers" → enter ≥16-char code, 10 OMR, Add/Update | weak code (`<16`) REJECTED 400; valid row "10 OMR · active" | `BillingPage.svelte:236-326`; entropy gate `vouchers.go:98-108` | ☐ |
| 4b | wire | burst checkout/redeem >5× | HTTP 429 rate-limit (DoD-7) | `handlers.go:42-77` | ☐ |

### Phase B — Stranger redeems the link
| 5-7 | `marketplace.<sov>/redeem?code=...` | auto-validate → "Voucher valid", "10 OMR credit" → "Sign up to redeem" | green card, code stashed in localStorage → `/plans` | `redeem.astro:88-177`; `vouchers.go:328-372` | ☐ |

### Phase C — 6-step wizard
| 8-22 | `/plans`→`/apps`→`/addons`→`/bcp`→`/review`→`/checkout` | pick S plan → add **WordPress** → subdomain `walkmart` + `.omani.homes` → topology → Review → Checkout | each step advances; "✓ walkmart.omani.homes available"; BCP single/active-hot-standby choice | `PlanStep/AppsStep/AddonsStep/BCPStep/ReviewStep.svelte` | ☐ |

### Phase D — Magic-link sign-in
| 23-27 | `/checkout` | type email → "Send sign-in code" → enter 6-digit code from inbox | code emailed (crypto/rand); verify → signed-in as customer | `CheckoutStep.svelte:513-596`; `auth/handlers.go:142-267` | ☐ |

### Phase E — Tenant naming + voucher applied + purchase
| 28-31b | `/checkout` launch panel | tenant name, confirm subdomain, voucher pre-filled | "−OMR 10 credit" → **"✓ Credit covers this order — Due now OMR 0"**, CTA "Launch my tenant" | `CheckoutStep.svelte:602-795` | ☐ |
| 32-34b | click **Launch my tenant** | createTenant → checkout → voucher consumed | 201 `{slug,status:provisioning}`; voucher "Used" 0→1; `paid_by_credit:true` | `tenant/handlers.go:200-271`; `billing/handlers.go:263-332` | ☐ |

### Phase F — Tenant Org PROVISIONS (the measured-broken wire)
| # | Action | Do | Expect | Source | ☐ |
|---|---|---|---|---|---|
| 35 | wire | `POST /provisioning/start` | 2xx (NOT 502). **THIS SESSION (hw133): `GET /api/billing/balance` → 502; billing SME service CrashLoops on NATS i/o timeout — SME→NATS cross-node datapath broken → the funnel cannot complete.** | `provisioning/handlers.go:135-159` | ☐ |
| 36 | kubectl | `kubectl -n catalyst get secret tofu-phase0-archive` | EXISTS (mother→child push wire alive; cutover 425-gated without it) | issue §4 | ☐ |
| 37 | kubectl | `sme/provisioning` pod | Ready (the 21h `Init:0/1` dies). **THIS SESSION (hw133): provisioning `Init:0/1`; billing/domain/notification/tenant CrashLoopBackOff.** | issue §3 | ☐ |
| 38-39 | console `/jobs` + dig | steps advance to READY; `dig walmart.omani.homes` | vCluster + WordPress HR Ready; resolves to the Sovereign LB (not stale `49.12.16.160`) | `provisioning/handlers.go:392-591`; `pool-domain-manager` | ☐ |

### Phase G/H — Lands signed-in in OWN org console + app RUNNING (the finish line)
| # | Go to | Expect | Source | ☐ |
|---|---|---|---|---|
| 40-41 | redirect → `console.walmart.omani.homes` | org console renders signed-in as owner, zero-click, no login UI | `config.ts:93-175`. **GAP — per-Org zero-click SSO landing lives in the SSO ticket; verify it lands signed-in or it's an SSO gap blocking DoD-5.** | ☐ |
| 42-45 | `console.walmart.omani.homes/apps` → WordPress → Open → the org URL | **WordPress SERVING** over valid TLS, `curl -I` 200 (the TERMINAL screenshot) | DoD-5 | ☐ |

**The DoD is NOT met until Rows 42-45 are witnessed.** **Gaps (this session, live):** billing/balance 502 (SME→NATS cross-node datapath); the whole back-half (provision → app running) unproven; per-Org SSO landing to verify; the tofu-archive wire gates everything.
