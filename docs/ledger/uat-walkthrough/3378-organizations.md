# #3378 ORGANIZATIONS — user acceptance walk (100% web UI)

**What the user sees:** ONE **Organizations** menu (replacing the old BSS/OSS), with the Sovereign itself as the first row, the ability to create sub-orgs, enter an org for support, run commerce, and see per-app cost — all in the console. **Sign in:** `https://console.<sov>/`.

_Each line = one browser step. `☐`/`✅`/`❌`._

### The single menu (scope wall intact)
- [ ] The left sidebar shows: Dashboard, Cloud, Apps, Sandbox, Jobs, Compliance, Users, **Organizations**, Settings — and **no** "BSS" entry.
- [ ] Click each of the other 8 items in turn → each opens its existing page unchanged.
- [ ] Click **Organizations** → the Organizations directory opens.

### The directory, parent first
- [ ] The directory intro reads "The parent organization — the Sovereign itself — is the first row".
- [ ] The first row is the Sovereign (its domain) with a **Parent** tag, and columns: Kind **internal**, Tier **corporate**, Billing **showback**, Isolation **vcluster**, Status **Active**.
- [ ] A **Create organization** button is present, plus a sub-nav: Commerce · Plans, Add-ons, Bundles, Industries, Apps, Billing, Domains.

### Showback works on day one
- [ ] On the Organizations page, a **Showback — per-app consumption** panel shows the parent org's total (units · CPU · memory · storage) and a per-app table with each app's share.

### Create a sub-organization (internal department)
- [ ] **Create organization** → choose **Internal** → the defaults change to **showback** billing + **namespace** isolation, and **no voucher/payment step** appears.
- [ ] Open **Advanced override** → you can change billing mode and isolation.
- [ ] Type a slug (e.g. **finance**) → submit → a success panel renders, and **finance** appears in the directory.
- [ ] ❌ **GAP** — the new internal org is **mis-badged** in the directory as customer / real / vcluster (the directory hardcodes those), instead of internal / showback / namespace.

### Mode-aware billing
- [ ] **Organizations → Billing** → for the parent (showback) it shows a **showback notice + the consumption panel** with **no** payment actions (payment never leaks for a showback org).

### Enter an org for support (audited, time-boxed)
- [ ] Click into the **finance** org's detail page → an **Enter org** button is present (the parent has none).
- [ ] Click **Enter org** → a new tab opens **finance's own console**, signed in as a **support** identity (not the owner), and the original tab shows "Support session … expires <≤60 min>".
- [ ] ❌ acceptance note — the new tab must actually **land logged in** (not just redirect); verify it shows the org console, not a login page.

### Commerce editing reflects in the storefront
- [ ] **Organizations → Commerce → Plans** → **+ New Plan** → fill the fields → **Create** → the plan appears in the table.
- [ ] Open the marketplace storefront's plan picker → the plan you just created appears there (no redeploy).
- [ ] Edit its price → it updates in both the table and the storefront.

### Redirects from old paths
- [ ] Open `/bss`, `/bss/billing`, `/parent-domains` → each redirects to its new `Organizations` home.

**Gaps:** new sub-orgs mis-badged in the directory; the org-detail Users/Roles tabs aren't built yet (old `/sme/users` still in place); Enter-org acceptance depends on the org-side handover landing logged in.
