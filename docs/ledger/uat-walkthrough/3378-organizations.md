# #3378 ORGANIZATIONS — user acceptance walk (web UI)

**What the user sees:** ONE **Organizations** menu (replacing BSS/OSS), with the Sovereign itself as the first row, sub-org creation, enter-org support, commerce, and per-app cost — all in the console. **Sign in:** `https://console.<sov>/`.

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| `/dashboard` | Sidebar: Dashboard, Cloud, Apps, Sandbox, Jobs, Compliance, Users, **Organizations**, Settings — and **no** "BSS" | ☐ | |
| `/dashboard` | Click each other item → each opens its existing page unchanged | ☐ | |
| `/organizations` | Intro: "The parent organization — the Sovereign itself — is the first row" | ☐ | |
| `/organizations` | First row = the Sovereign with a **Parent** tag: internal · corporate · showback · vcluster · Active | ☐ | |
| `/organizations` | **Showback** panel shows the parent's total + a per-app consumption table | ☐ | |
| `/organizations/new` | Choose **Internal** → defaults change to showback + namespace; **no** voucher/payment step | ☐ | |
| `/organizations/new` | **Advanced override** lets you change billing mode + isolation | ☐ | |
| `/organizations/new` | Type slug **finance** → submit → success panel; **finance** appears in the directory | ☐ | |
| `/organizations` | ❌ **GAP** — the new internal org is **mis-badged** as customer / real / vcluster | ☐ | |
| `/organizations/billing/billing` | For the parent (showback): a showback notice + consumption panel, **no** payment actions | ☐ | |
| `/organizations/finance` | An **Enter org** button is present (the parent has none) | ☐ | |
| `/organizations/finance` | Click **Enter org** → new tab opens **finance's own console** signed in as a **support** identity; original tab shows "expires ≤60 min" | ☐ | |
| `/organizations/commerce/plans` | **+ New Plan** → fill fields → **Create** → row appears in the table | ☐ | |
| `marketplace.<sov>/plans` | The plan you just created appears in the storefront picker (no redeploy) | ☐ | |
| `/bss`, `/bss/billing`, `/parent-domains` | Each redirects to its new **Organizations** home | ☐ | |

**Gaps:** new sub-orgs mis-badged; org-detail Users/Roles tabs not built yet; Enter-org must actually land logged in (not just redirect).
