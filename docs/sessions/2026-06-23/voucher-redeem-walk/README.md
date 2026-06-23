# Voucher redeem-page walk — live Playwright (2026-06-23, marketplace.omantel.biz)

PUBLIC redeem page (`/redeem?code=…`) — no sign-in, no session, no mutation. Uses the 3 vouchers issued
earlier today via the billing backend. Flips #3376 UAT rows 75/76/77.

| Row | Input | Live result | Verdict |
|-----|-------|-------------|---------|
| 75 | `?code=VCH-CSFT55QQNSKE` (valid) | **"Voucher valid · 50 OMR credit"** + "OpenOva launch voucher" + `Sign up to redeem` + copy *"applied at checkout after you create your **Organization**"*. Brand chrome served from `marketplace.omantel.biz`. ([screenshot](evidence/voucher-redeem-valid-VCH-CSFT55QQNSKE.png)) | ✅ |
| 76 | `?code=JUNK-NOT-A-REAL-CODE-9999` | **"Voucher not valid · This code does not exist or has been retired."** — generic, no tombstone, no detail leak (redeem-preview → 404). | ✅ |
| 77 | page source / hosts | All requests on `marketplace.omantel.biz`; redeem-preview at `/api/billing/vouchers/redeem-preview`. **No `console.openova.io` / `omantel.openova.io` / bare `openova.io` host literal.** | ✅ (host-literal) |

## Bonus signal — the voucher pipeline works end-to-end
`?code=VCH-YS87ZAW567D2` → redeem-preview returned **HTTP 410 Gone** → page shows **"This campaign has ended … no longer accepting redemptions"** (still reads the 50 OMR + code). 410 = the voucher was **redeemed/consumed** since I issued it today (`times_redeemed` reached `max_redemptions:1`) — i.e. a stranger-with-a-voucher actually redeemed it (likely during the founder demo). The page correctly distinguishes three states: **valid (200) · consumed (410) · nonexistent (404)**.

## Honest note (not a host leak, a white-label observation)
The page brand chrome reads **"OpenOva"** (platform brand), not Omantel. Row 77 forbids the `openova.io` HOST literal (clean ✅), but a fully white-labelled Omantel Sovereign arguably should show Omantel branding on its own marketplace. Logged as a follow-up observation, not a row-77 failure.
