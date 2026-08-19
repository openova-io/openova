# Train manifest — hw300.omani.works (fired 2026-08-19)

**Why this fire exists:** hw299 (dep `e13300fc41a33a57`) was destroyed out-of-band ~06:35Z
(see `lane-b-prov-decision-hw299-kept.md` + the UAT header). One env at a time: hw299's
canonical wipe re-fired 07:05Z; this fire proceeds only after the record flips `wiped`,
the ~15-min release lag, and a PRE-FLIGHT PASS line below.

## RT-1..5 — what this train carries

- **Chart pins (published, re-queried at fire time):** `bp-catalyst-platform 1.4.1520`,
  `bp-self-sovereign-cutover 0.1.191` (ghcr heads; cutover floor ≥0.1.171 per #5919 satisfied).
- **Passengers vs hw299 (fired from main @ 2026-08-16 08:14Z):** everything merged to main
  since — including guacamole producer `8423fa355` + catalog 0.2.43 pin (`8d8857c84`),
  agenity emitter orgTag `37fe649`, stalwart-tenant catalog 0.1.15, #6137 janitor env,
  #6286 fanout timeout. The catalog carries every fix the 8h-plan walk needs; the open
  cutover-chart PRs (0.1.193/0.1.194) are NOT passengers (unpublished, post-cutover-only rows).
- **Create body:** derived field-for-field from hw299's record request; changes only:
  `sovereignFQDN/subdomain/pool → hw300 / omani.works` (LE rotation per RUNBOOKS §0.3:
  hw298+hw299 both burned omantel.biz certs; omani.works last used by hw296 on 08-13),
  `objectStorageBucket → catalyst-hw300-omani-works`. 2-region me-east-215-a/-b,
  cp m7n.xlarge.8, workers 5× m7n.2xlarge.8 per region, marketplaceEnabled=true.
- **Auth:** owner RS256 bearer self-signed with the mothership handover key (key read from
  PVC, shredded after mint; bearer exp 2h). GET /deployments with it → 200 (verified).
- **Anthropic seed:** mothership env is a boot snapshot; no fresh founder blob has landed
  this window → PATH A intentionally SKIPPED (silent no-op is acceptable); PATH B
  (per-Sovereign `catalyst-system/sovereign-anthropic-credentials` Secret, live-read ≤10 min)
  remains the plan for G8/G9/220/221 once the founder's fresh blob arrives.

## Gates

| Gate | Status |
|---|---|
| hw299 record `wiped` | pending at manifest write — fire blocks on it |
| Wipe-release lag ~15 min | timed from record flip |
| `scripts/prov-preflight.sh hw300.omani.works catalyst-hw300-omani-works` (BEARER + HW_TFVARS) | PRE-FLIGHT PASS line appended below at run time |
| Fires today | 1 of max 2 (RT-8) — the 06:3x hw299 destroy was not a fire |

## Evidence appendix (appended at execution)
