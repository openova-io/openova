# Post-P0 signed-in console walk (2026-06-24, omantel.biz)

After the cnpg-webhook-hijack P0 outage was fixed (catalyst-api 503→up), a signed-in console
walk confirms the customer experience is restored AND validates merged fixes live.

Session: minted RS256 kid-less handover token (Sovereign signer) → `/auth/handover` → `catalyst_session`
cookie → signed in as `qa-walk@omantel.biz`/`billing-walk@omantel.biz` (sovereign-admin). Founder mailbox untouched.

| Screenshot | Proves |
|---|---|
| `console-signedin-postp0.png` | `/dashboard` renders LIVE data post-P0 — Treemap of the real estate (cnpg-pair, seaweedfs, mimir, kyverno, falco, harbor, mgmt-vcluster, keycloak, guacamole, agenity), 115 items, full nav. The API recovered; SSR auth works. Header shows 🟠 DEGRADED (= the #3785 hot-standby-replica HR-False, root-caused). |
| `jobs-stream-4635.png` | `/jobs` for deployment `4635277cae4ffed9` reaches WITHOUT 404, signed in. Shows finite **LIFECYCLE** jobs (Install OpenBao/Gitea/Grafana/guacamole/Harbor/Keycloak/Axon) — **NOT** reconciler-polluted → live-confirms the #4200 Jobs/Recon fix (UAT rows 190/191 ✅). |

Note: the org-scoped demo session (`7283eb4a`) is correctly 403'd on `/deployments/{id}/*` (tenant isolation, validated 2026-06-23 northstar-rbac-rewalk). The sovereign-admin reach (here) is the correct path.
