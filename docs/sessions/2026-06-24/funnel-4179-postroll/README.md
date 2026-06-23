# Funnel #4179 — post-roll fresh-signup walk (omantel.biz, 2026-06-24)

Validation of the #4179 fix after PR #4213 rolled `orgTag d3c55b4→c3b2736`
(the merged #4184 `parent_domain` thread) to the live `services-tenant` on
omantel.biz. Walk done in a CLEAN browser context (all storage cleared — a
stale `g2signup` active-org session was purged before the walk).

## Roll confirmed (STEP 1)

- `catalyst-build` on merge `493131f` completed success → deploy-bot bumped
  `Chart.yaml 1.4.806→1.4.807` (commit `2c7fcfa5e`) + a second auto-bot bumped
  the bootstrap-kit HR pin `1.4.806→1.4.807` (commit `4387b968d`).
- `Blueprint Release` published chart `bp-catalyst-platform:1.4.807`.
- Sovereign Flux reconciled; live `org-services/tenant` deploy rolled
  **`services-tenant:d3c55b4` → `services-tenant:c3b2736`** (pod
  `tenant-84fc9f856c-x9jdn`, rollout success).

## Funnel walk (STEP 2) — new slug `qa4179walk` on `.omani.works`

| # | step | result | screenshot |
|---|------|--------|-----------|
| 1 | marketplace landing (fresh, storage cleared) | OK, this Sovereign brand | `01` |
| 2 | plans → M | M plan selected | `02` |
| 3 | apps → WordPress | added (1 selected) | `03` |
| 4 | domain → `qa4179walk.omani.works` | "✓ available", pool TLD | `04` |
| 5 | BCP → Active-hot-standby | selected | `05` |
| 6 | review | WordPress / M / URL `qa4179walk.omani.works` | `06` |
| 7 | checkout email `qa4179@omani.works` → PIN `293326` (read from rtz Valkey `magic:`) → signed in | "My account: qa4179" | `07`,`08` |
| 7b | voucher `UAT215WALKER72` (5000 OMR credit) → "Credit covers this order — 0 OMR due" → button = "Launch my tenant" | credit-covered path, no Stripe | `09` |
| 8 | Launch → redirect | **lands on the POOL host but DNS fails** | `10` |

## THE VERDICT (STEP 3)

**The #4179 redirect-derivation fix WORKS:**
- `POST /api/tenant/orgs` request carried **`parent_domain: "omani.works"`**.
- Response **201 Created**.
- Org CR `qa4179walk` stores **`spec.tenantPublic.parentDomain: omani.works`**
  + `subdomain: qa4179walk` (status `Ready=True`: vCluster HR Ready + Keycloak
  group + Gitea Org reconciled) — the empty `tenantPublic:{}` from the pre-roll
  g2signup repro is FIXED.
- The marketplace redirected to the **POOL host**
  `https://console.qa4179walk.omani.works/auth/org-handover?token=…` (the secure
  #4192 cookie-handoff endpoint) — **NOT** the dead `console.qa4179walk.omantel.biz`.

**But the Org does NOT land signed-in** → `net::ERR_NAME_NOT_RESOLVED`:
`console.qa4179walk.omani.works` has no DNS A-record (absent on authoritative
`ns1.openova.io` + in the mothership PowerDNS `pdns` DB).

**Root cause (separate bug, #4215):** the org-tenant DNS provisioner in
`products/catalyst/bootstrap/api/cmd/api/main.go` read `CATALYST_POWERDNS_URL`
(a name set nowhere) instead of the canonical `CATALYST_POWERDNS_API_URL`, so
`NewPowerDNSWriter` got an empty baseURL → nil → `NoopOrganizationDNSProvisioner`
permanently. New Orgs never got their `console.<slug>.<pool>` A-record.
Mothership log: `org-tenant: powerdns env unset; using no-op DNS provisioner`
while both `CATALYST_POWERDNS_API_URL` + `CATALYST_POWERDNS_API_KEY` were
populated on the pod. Fix in PR #4216 (`Refs #4215 #4179`).

**#4179 NOT closed** — funnel DoD step 8 (land SIGNED-IN on
`console.<slug>.omani.works/jobs`) is unmet until #4215/#4216 rolls and the
pool host resolves.
