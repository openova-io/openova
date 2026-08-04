# Hetzner single-region mothership — readiness gate + migration plan (2026-08-04)

> Founder direction (2026-08-04, verbatim intent): 1–2 days remain; provision a new
> OpenOva platform on **Hetzner, single region, single cluster**, elevate it as the
> new mothership, retire the Contabo-based mothership. This document is the
> readiness verdict, the evidence behind it, the rehearsal-prov spec, and the
> elevation runbook. Session evidence: this file + the probes committed alongside.

## 1. The declaration gate (the direct answer)

The product is **ready to deploy the new mothership** the moment all three hold:

| Gate | Test | State 2026-08-04 |
|---|---|---|
| G-A delivery chain | main compiles; catalyst-build → deploy-bot pin cycle green | ✅ `go build ./...` exit 0 on `products/catalyst/bootstrap/api` (verified live; stale P0 #5618 closed with evidence). Guards green 10:34–11:15Z. PR queue = dependabot only. |
| G-B rehearsal prov | ONE fresh **Hetzner single-region** prov converges zero-touch AND passes the mothership-role walk (§4) including **provisioning a throwaway deployment from its own deployments API** | ☐ — the one genuine unknown. Last proven zero-touch Hetzner prov: t38 era (~June). #5090 (dnsdist :53 front door) + #5080 (NodePort eradication) landed on the Hetzner provider since, unproven live. |
| G-C elevation runbook | DNS glue swap plan + Stalwart mail migration + deployments-PVC export + Contabo freeze/rollback window written and walkable | ✅ §5 below |

**Explicitly NOT gates**: the remaining UAT tail and the multi-region issue classes (§2).
100% of the current 286-row ledger is the wrong bar for this configuration — the
ledger measures a 2-region Huawei cutover Sovereign, which the mothership is not.

## 2. Open-backlog audit — what the single-region pivot eliminates

Measured against the live board 2026-08-04 (`gh issue list`, 127 open after #5618 close):

| Class | Count | Disposition under single-region Hetzner mothership |
|---|---|---|
| Multi-region / DR / cutover / per-region-split / ClusterMesh (title-matched union incl. region-b GitOps #5359 family, secret-split #5480 family, standby/switchover #5601/#5513/#5514/#5623, cutover chain #5527/#5596/#5640/#5650) | **40 (~32%)** | **Structurally N/A** — no second region, no ClusterMesh, no cutover (mothership never cuts over; that chain is for franchised Sovereigns). Defer as `deferred-multiregion`; they remain banked capability work for franchise deals. |
| Mothership-snowflake rot (#5573 Flux replicas=0, #5558 catalyst-api replicas=0, #5567 orphaned webhooks, #5348 live NodePorts) | 4+ | **Retired with the Contabo box.** These exist because the mothership predates the product; a product-provisioned mothership cannot re-develop them. |
| Single-region-relevant product defects (wizard W1/W2 #5401/#5575/#5616, timeline #5646/#5501, funnel UX #5634/#5484/#5512, per-app SSO polish #5598/#5599/#5612/#5466, catalog papercuts #5610/#5510/#5496, stalwart-tenant TLS row 234, treemap #5613, bp-postgres roles #5504) | ~35 | **Day-2 GitOps merges** on the new mothership. None blocks a mothership job on day 1. |
| Remainder (CI/docs/guards/misc) | ~48 | Normal backlog cadence. |

UAT ❌-row cross-check: of 19 ❌ rows, 2 are DR-only (57, 64); rows 26/29 (silent
SSO), 90 (funnel app serves), 219 (chepherd converges) walked **green on hw269**
and their hw292 failures sit downstream of the 2-region gateway ~50% flake
(#5511 ← #5359) — expected to clear on single-region with no code change.
The genuinely provider-agnostic residue (W1/W2, 86, 92, 234, 115, 35–38, R17)
is the day-2 list above.

## 3. Rehearsal prov spec (G-B)

- **Wire body**: `POST /sovereign/api/v1/deployments`, `provider: hetzner`
  (the provisioner's default; `len(Regions) < 2 → "single-region"` is first-class,
  `provisioner.go:254`). Single region, single cluster, 1× cpx52 topology per
  `feedback_multiregion_topology_1_cpx52_per_region` sizing.
- **TLD**: `omantel.biz` per the L3 rotation — hw290/291/292 minted wildcard certs
  on `omani.works` this week; LE = 5 certs/week/registered-domain. Do not burn the
  last slot on a rehearsal.
- **Law compliance**: ONE environment at a time (founder 2026-07-15) — wipe hw292
  (dep `1c56518035a83e03`, healthy, all extractable proofs banked in
  docs/sessions/2026-08-03..04 + UAT stamps) via the canonical wipe endpoint,
  `python3 scripts/reset-uat.py <env>` BEFORE fire, then fire.
- **Zero-touch**: no hand-patching during convergence; a wedge = a defect to fix
  at source, re-fire. DEBUG-BEFORE-WIPE if it fails (cloud-init log first).

### 4. Mothership-role walk (the ~30-row core, replaces "walk everything")

1. Prov converges: all HRs Ready, zero NodePorts (`scripts/check-live-nodeports.sh`), CSI storage, no local-path.
2. Funnel: signup → catalog → checkout/voucher → Org provisioned → **app serves at its FQDN** (UAT 78–90 class).
3. PIN login end-to-end (IMAP PIN → RS256 session), console dashboard + apps grid + jobs render.
4. SSO front door: console bare-URL silent landing (rows 26–29 class) + Grafana/Gitea/Harbor landings.
5. DNS: PowerDNS authoritative answers for the sovereign FQDN via the #5090 dnsdist :53 front door — **this is the unproven Hetzner-specific commit; walk it explicitly**.
6. Mail: Stalwart up, PIN mail delivery observed on the wire.
7. **The defining test**: seed Hetzner creds → `POST /sovereign/api/v1/deployments` **from the rehearsal env itself** → throwaway deployment reaches tofu-apply phase → wipe it. The mothership's job is provisioning Sovereigns; prove it from day 0.
8. Known-expected failure, do not chase: handover walks fail on the rehearsal env because `auth_handover.go` hardcodes `expectedIss=console.openova.io` (#5614) — correct behaviour once this env IS console.openova.io.

## 5. Elevation runbook (G-C)

Pre-cutover (while rehearsal converges):
1. Drop Dynadot TTLs on `openova.io` + pool TLDs (`omani.homes/rest/trade`, `omani.works`, `omantel.biz`) to 300s.
2. Export Contabo deployments-PVC records (`/deps/tofu/*/tofu.auto.tfvars.json`, kubeconfigs, handover JWT keys) — the only persistent credential cache. Copy, verify hashes, do NOT print values.
3. Inventory Stalwart mailboxes + DNS (MX/SPF/DKIM/DMARC) for `openova.io`.

Cutover (evening, low traffic):
4. Stalwart migration by **data copy** (founder mailbox password never-touch — no credential reset; coordinate the founder's client re-login only if the copy requires it).
5. Point `openova.io` glue/NS → new PowerDNS; MX swap; verify PIN mail + founder mailbox from the new box before proceeding.
6. Seed deployments API creds (hcloud token, Dynadot key, GHCR pull token, handover JWT keypair) into the new catalyst-api; import the exported deployment records.
7. `console.openova.io` served by the new env — #5614's hardcoded issuer now matches by construction.

Rollback: Contabo stays **frozen read-only for 14 days** (Flux already at
replicas=0 — its frozen state is, for once, an asset). Rollback = revert NS/MX
records; nothing on Contabo is destroyed until day 15.

## 6. Day-1 / Day-2 timeline

| When | Action |
|---|---|
| D1 morning | Wipe hw292 + reset-uat; fire Hetzner rehearsal (§3); start pre-cutover steps 1–3 while converging |
| D1 afternoon | Mothership-role walk (§4) incl. self-provisioning test; fix ONLY walk-blocking defects at source, re-fire if needed |
| D1 evening | If walk clean: this env IS the new mothership candidate (do not prov twice — saves an LE slot and the evidence transfers) |
| D2 day | Residual fixes from walk; UAT re-baseline: mark 40 multi-region issues + DR/cutover rows `deferred-multiregion`; headline number now measures the platform actually run |
| D2 evening | Cutover steps 4–7; Contabo frozen; announce |

## 7. Honest risks (not hidden)

1. **The Hetzner path is ~6 weeks unproven live** — the entire reason G-B exists. Budget one re-fire.
2. Single cluster = no HA — same posture as Contabo today; accepted, and recoverable later by re-prov'ing multi-region once that tail is paid down.
3. LE budget on `omantel.biz` unknown-fresh — verify before fire; staging-issuer fallback exists for walk-only phases.
4. Mothership catalyst-api (Contabo) must be scaled up to fire the prov (#5558 keeps it at 0) — hand-scale is acceptable here; it is the box being retired.

Refs #5618 (closed stale), #5573 #5558 #5567 #5348 (retired-with-box class), #5359 #5511 (deferred-multiregion root), #5614 (issuer hardcode), #5090 #5080 (unproven-live Hetzner commits).
