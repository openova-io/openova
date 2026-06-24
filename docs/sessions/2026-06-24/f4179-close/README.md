# #4179 close-gate walk — fresh org `f4179final.omani.works` — 2026-06-24

Session `7b69d699`. Env: omantel.biz Sovereign (deployment `4635277cae4ffed9`), region rtz.
Access: mothership `catalyst-api` pod → Sovereign kubeconfig `/var/lib/catalyst/kubeconfigs/4635277cae4ffed9.yaml`.

## Verdict: 3 of 4 close-gate criteria GREEN. The CORE bug (#4241 SAN-match) is FIXED + live-proven. NOT closing #4179 — criterion 4d (signed-in landing) is blocked by a separate tenant-registry registration gap.

## Preconditions cleared this session

1. **CNPG webhook P0** (`docs/ledger` MEMORY P0). `bp-catalyst-platform` HR was wedged in an x509 rollback loop — the `cnpg-mutating-webhook-configuration` caBundle (`B6:8A…`) had drifted from the cnpg-system operator's serving cert (`65:91…`). Recovery: delete both webhook configs → `driftDetection:enabled` on `flux-system/bp-cnpg` → Flux recreated them with a matching caBundle. Live server-dry-run Cluster create now succeeds. Platform upgrade unblocked.

2. **#4242 image delivery.** org-controller `09dcc10` (the `reconcileTenantConsoleTLS` build) was published to GHCR but never reached the Sovereign: the deploy-bot bumped the image pin `6cdbc50→09dcc10` (`842d82bfa`) **without a chart-version bump** and re-pushed the `1.4.813` OCI tag, so the Sovereign's Flux kept rendering the stale `6cdbc50` content. Durable fix: **PR #4245** bumped the chart `1.4.813→1.4.814` (clean tag). Deploy-bot chain then advanced main to `1.4.815` (still pinning `09dcc10`); the Sovereign HR now targets `1.4.815`. The live org-controller runs `09dcc10` (1/1 Ready).

## Close-gate (fresh signup `f4179final` on `.omani.works`, voucher `VCHF4179FINAL01` 5000 OMR, single-region, WordPress, plan M)

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 4a | Redirect host = `console.<slug>.omani.works` (#4184) | ✅ | "Launch my tenant" redirected to `console.f4179final.omani.works` (page title captured live; the broken `console.<slug>.omantel.biz` was NOT produced) |
| 4b | Resolves to console-ELB EIP (#4236/#4240) | ✅ | `dig console.f4179final.omani.works @8.8.8.8/@1.1.1.1 → 212.72.24.33` (= console-ELB EIP, matches `console.omantel.biz`); org-controller log `tenant-dns: wrote per-Org pool A-records … target:212.72.24.33` |
| 4c | **HTTPS cert SAN COVERS the host** (#4241/#4242) — THE CORE FIX | ✅ | Live SAN `DNS:*.f4179final.omani.works, DNS:f4179final.omani.works` (issuer Let's Encrypt YR1 — prod, trusted). `curl --resolve …:443:212.72.24.33 /jobs → HTTP 200, ssl_verify_result=0`. **BEFORE #4242 the same host served `*.omani.works` (1-label, mismatch); AFTER, it serves the 2-label SAN that matches.** |
| 4d | Lands SIGNED-IN over HTTPS on `/jobs` | ❌ | `console.f4179final.omani.works` serves the login page over the SAN-matching HTTPS cert (browser, no cert error), but the marketplace→console session handoff (`/auth/org-handover`) returns `{"error":"invalid audience: not an org console host"}` — see next-layer below. |

## Next layer (blocks 4d only) — tenant-registry registration gap

The marketplace funnel creates the Org via **org-services `tenant` svc** (`POST /tenant/orgs`, logged `04:16:16 status=201`), which creates the Org CR → org-controller does DNS/TLS/route (all #4179 fixes work). But it does **NOT** register the Org's console host in **catalyst-api's tenant registry** (`/var/lib/catalyst/deployments/-tenant-registry.json`), which `auth/org-handover` → `resolveOrgScope` validates against. That registry is populated only by catalyst-api's OWN provisioning state machine (`organization_provisioning.go` Step 6 `tenant_registered`, reached via `POST /api/v1/org/tenants`). Only the `demo` org (created that way) is registered.

Decisive proof (both probed live on the catalyst-api pod):
- Registered host `console.demo.omani.homes` + dummy token → `{"error":"invalid token"}` (host check PASSES, mechanism works).
- Unregistered host `console.f4179final.omani.works` + dummy token → `{"error":"invalid audience: not an org console host"}` (host check FAILS).

**Fix target:** the org-services `/tenant/orgs` create path (or the org-controller reconcile) must register the per-Org console host in the catalyst-api tenant registry — same `TenantRegistration{Host, TenantID, TenantKind:Org, KeycloakRealmURL,…}` the catalyst-api Step-6 writes. This is the session-handoff registration, distinct from #4179's redirect/DNS/TLS scope.

## Secondary follow-up (not blocking, noted)

The chart's `sovereign-fqdn` ConfigMap key `consoleLBIP` defaults to empty (`global.sovereignConsoleLBIP | default ""`). The org-controller's #4240 DNS writer skips when it's empty. Applied a runtime patch (`consoleLBIP=212.72.24.33`) so new-org DNS writes work; the chart needs `global.sovereignConsoleLBIP` wired per-Sovereign for this to survive a clean reconcile.

## Evidence files
- `evidence/close-gate-evidence.txt` — dig + openssl SAN + curl ssl_verify
- `evidence/f4179final-console-https-loads.png` — pool console login over SAN-matching HTTPS
