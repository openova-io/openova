# Fire-readiness gate — next prov (hw265) — 2026-07-17

Preflight (`scripts/prov-preflight.sh hw265.omani.works hw265-omani-works`, read-only):

## Infra headroom — ✅ ALL GREEN (2-region fire fits)
- VPC 3/5 used (≥2 free ✅) · EVS 268/400 (≥120 free ✅) · OBS 69/100 (≥3 free ✅)
- catalyst-api rollout settled ✅ · no in-flight CI ✅

## Wipe-before-fire gates — ❌ EXPECTED (hw264 still live)
- 1 active deployment (hw264 `4585c8a9`) → COMPLETELY wipe first (one-env-at-a-time #5111).
- 3 orphaned DOWN EIPs + 9 non-bastion EIPs allocated → after wipe, wait ~10-15min for
  release or firing risks VPC.0532 'EIP pool sold out' (#5126 — the founder's EIP limit).
- foreign live ECS = hw264's own nodes (same wipe-first).

## Fire sequence (gated, janitor-disciplined)
1. Board the full train: deploybot chart image-pin ≥ 6ea8bf1de (delivers #5123 catalyst-api);
   #5137 Continuum DR auto-promotion merged (bonus passenger). Chart pins already on main:
   cnpg-pair 0.2.12 (#5125-D1), openbao 1.2.63 (#5157), openova-mcp 0.1.3 (#5167).
2. Wipe hw264 (autonomous — platform-created, non-bastion): capture cloud-init log first (#3132)
   only if FAILED (it's ready, so straight wipe), then `POST /deployments/{id}/wipe`.
3. Wait ~10-15min → EIP/ECS release to shared pool.
4. Re-run preflight → MUST pass (0 active, EIPs released).
5. `python3 scripts/reset-uat.py hw265` (flush predecessor evidence #3132).
6. Fire: `POST /sovereign/api/v1/deployments` (hw265.omani.works, qaTest=false PROD certs,
   fireCutoverOnHandover=true zero-touch).
7. Zero-touch validate the WHOLE train (esp. MCP RS256 ACTIVE #5167, openbao Deployment #5157,
   cnpg failover-via-HR #5125-D1) → cutoverComplete → region-kill G12 (auto-promote if #5137
   landed) → full ~281-row authed UAT sweep to 100%.

## Delivery-chain note (merged ≠ delivered)
deploybot image pin is at 2e18085 (has #5124) — #5123 (6ea8bf1de) merged AFTER; the fresh
prov's catalyst-api carries #5123 only once deploybot bumps past it. DO NOT fire before that.
