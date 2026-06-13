# #3378 ORGANIZATIONS — user acceptance walk (web UI)

**What the user sees:** ONE **Organizations** menu (replacing BSS/OSS), with the Sovereign itself as the first row, sub-org creation, enter-org support, commerce, and per-app cost. **Sign in:** [console.hw133.omani.works](https://console.hw133.omani.works/).

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [Dashboard](https://console.hw133.omani.works/dashboard) | Sidebar: Dashboard, Cloud, Apps, Sandbox, Jobs, Compliance, Users, **Organizations**, Settings — and **no** "BSS" | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | Click each other item → each opens its existing page unchanged | ☐ | |
| [Organizations](https://console.hw133.omani.works/organizations) | Intro: "The parent organization — the Sovereign itself — is the first row" | ☐ | |
| [Organizations](https://console.hw133.omani.works/organizations) | First row = the Sovereign with a **Parent** tag: internal · corporate · showback · vcluster · Active | ☐ | |
| [Organizations](https://console.hw133.omani.works/organizations) | **Showback** panel shows the parent's total + a per-app consumption table | ☐ | |
| [Create organization](https://console.hw133.omani.works/organizations/new) | Choose **Internal** → defaults change to showback + namespace; **no** voucher/payment step | ☐ | |
| [Create organization](https://console.hw133.omani.works/organizations/new) | **Advanced override** lets you change billing mode + isolation | ☐ | |
| [Create organization](https://console.hw133.omani.works/organizations/new) | Type slug **finance** → submit → success panel; **finance** appears in the directory | ☐ | |
| [Organizations](https://console.hw133.omani.works/organizations) | ❌ **GAP** — the new internal org is **mis-badged** as customer / real / vcluster | ☐ | |
| [Org · Billing](https://console.hw133.omani.works/organizations/billing/billing) | For the parent (showback): a showback notice + consumption panel, **no** payment actions | ☐ | |
| [Org · finance](https://console.hw133.omani.works/organizations/finance) | An **Enter org** button is present (the parent has none) | ☐ | |
| [Org · finance](https://console.hw133.omani.works/organizations/finance) | Click **Enter org** → new tab opens **finance's own console** signed in as a **support** identity; original tab shows "expires ≤60 min" | ☐ | |
| [Commerce · Plans](https://console.hw133.omani.works/organizations/commerce/plans) | **+ New Plan** → fill fields → **Create** → row appears in the table | ☐ | |
| [marketplace · Plans](https://marketplace.hw133.omani.works/plans) | The plan you just created appears in the storefront picker (no redeploy) | ☐ | |
| [/bss](https://console.hw133.omani.works/bss) | Redirects to the new **Organizations** home | ☐ | |
| [/parent-domains](https://console.hw133.omani.works/parent-domains) | Redirects to **Organizations · Domains** | ☐ | |

**Gaps:** new sub-orgs mis-badged; org-detail Users/Roles tabs not built yet; Enter-org must actually land logged in (not just redirect).
