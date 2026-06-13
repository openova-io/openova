## #3378 ORGANIZATIONS

**Model:** ONE `Organizations` menu replacing BSS + the never-built OSS — parent-first directory, the internal door, audited Enter-org impersonation, commerce catalog, mode-aware billing (real/chargeback/showback), parent self-showback day one, domain pools, redirect map — while the scope wall (Dashboard/Cloud/Apps/Sandbox/Jobs/Compliance/Users/Settings) stays byte-identical.

### A. Scope wall
| A1-A5 | `console.<sov>/dashboard` | sidebar = Dashboard,Cloud,Apps,Sandbox,Jobs,Compliance,Users,**Organizations**,Settings; NO `BSS`; the 8 others byte-identical; click Organizations → `/organizations` | `SovereignSidebar.tsx:52-142,193-196` | ☐ |

### B. Directory — parent first citizen
| # | Go to | Expect | Source | ☐ |
|---|---|---|---|---|
| B1-B4 | `/organizations` | title + intro "The parent organization — the Sovereign itself — is the first row"; table cols Organization\|Kind\|Tier\|Billing\|Isolation\|Status; FIRST row = parent (FQDN + `Parent` pill): `internal·corporate·showback·vcluster·Active` | `OrganizationsDirectoryPage.tsx:93-251`; `organizations.api.ts:132-147` | ☐ |
| B5 | `/organizations` | directory NEVER blank (parent seeded even if sub-org fetch fails) | `organizations.api.ts:181-189` | ☐ |
| B7 | `/organizations` | section nav: Commerce·Plans, Add-ons, Bundles, Industries, Apps, Billing, Domains | `OrganizationsDirectoryPage.tsx:116-130` | ☐ |

### C. Parent self-showback (B3 feed)
| C1-C4 | `/organizations` showback panel | heading "Showback — per-app consumption"; org line "(parent — your own estate) · units · CPU · mem · storage"; per-app table (Application\|Namespace\|CPU\|Mem\|Share); ~100% to parent | `ShowbackPanel.tsx:48-104`; `sme_consumption.go:122-221` | ☐ |

### D. Internal door
| # | Go to | Do | Expect | Source | ☐ |
|---|---|---|---|---|---|
| D1-D7 | `/organizations/new` | choose **internal** | defaults flip `showback`+`namespace`; NO voucher step; Advanced override = billing/isolation selects; slug `finance` → submit | `CreateTenantPage.tsx:224-359`; `organizations.api.ts:81-88` | ☐ |
| D9 | `/organizations` | the `finance` row badges | **GAP (PARTIAL) — `subOrgRowFromTenant` hardcodes every sub-org to `customer/real/vcluster` (`organizations.api.ts:156-172`); an internal org mis-badges until the tenants feed surfaces spec fields.** | ☐ |

### E. Mode-aware billing
| E1-E5 | `/organizations/billing/billing` | BillingModeGate → showback notice + panel, NO payment actions; real-mode half rides FUNNEL #3376 (don't fabricate); fallback defaults to showback | `BillingModeGate.tsx:40-72` | ☐ |

### F. Enter org — audited impersonation
| # | Go to | Do | Expect | Source | ☐ |
|---|---|---|---|---|---|
| F1-F2 | `/organizations/$org` | parent has NO Enter-org button; sub-org HAS it | `OrganizationDetailPage.tsx:67-71`; `EnterOrgButton.tsx:51-64` | ☐ |
| F4-F6 | click **Enter org** | POST `/organizations/finance/enter` → opens handover URL → **org's own console lands logged in as `support+<op>@finance...`** (NOT a wire-302); confirm line "expires <≤60min>" | `sme_enter_org.go:119-179`; `EnterOrgButton.tsx:36-73` | ☐ |
| F8-F9 | catalyst-api logs | audit line "enter-org: support session minted" with initiatedBy/org/ttl; ≤60min | `sme_enter_org.go:45-177` | ☐ |
| F10-F12 | negative | enter-parent → 400; non-admin → 403; signer unwired → 503 | `sme_enter_org.go:83-117` | ☐ |

### G/H. Commerce CRUD (+ #3156 regression)
| G1-G8 | `/organizations/commerce/plans` | create/edit/delete a plan; it surfaces in the storefront `?product=generic`; **a `product_slug:sandbox` plan does NOT appear in the generic picker** | `CommerceEditorPage.tsx`; `catalog/handlers.go:265-280` | ☐ |
| H1-H5 | addons/bundles/industries/apps | full CRUD; bundles/industries multi-select from `/catalog/apps`,`/catalog/bundles` | `CommerceEditorPage.tsx:256-286` | ☐ |

### I. Redirect map + Domains
| I1-I8 | `/bss*`,`/sme/tenants/new`,`/parent-domains` | each redirects to its `/organizations/*` home | `router.tsx:1733-1758` | ☐ |
| I10 | `/sme/users`,`/sme/roles` | **NOT redirected (by design)** — org-detail users/roles tabs not built yet; confirm they still render | `router.tsx:1538-1546,1741-1744` | ☐ |

**Gaps:** (1) directory badge fidelity PARTIAL (sub-orgs hardcoded customer/real/vcluster); (2) `/sme/users`+`/roles` un-redirected, org-detail tabs not built (deferred); (3) Enter-org acceptance = lands-logged-in, depends on the org-side handover redemption + cookie-domain fix.
