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
- 🛑 **`"bcpTopology": "single-region"` is MANDATORY in the body.** Verified at
  `products/catalyst/bootstrap/api/internal/handler/deployments.go:1377-1383`:
  admission rejects with **HTTP 400 before `Validate()`, `writeTfvars()` or tofu
  ever run** when `BcpTopology` is empty AND `len(Regions) < 2`. This is founder
  ruling #4706 ("the fundamental requirement of 2 region mimicking agreement") —
  multi-region is the BCP default and a single-region prov must be a *deliberate,
  declared* act. Nothing pre-defaults the field: `deployments.go:1132` constructs
  the Request without it and no console/UI path sets it (zero `bcpTopology` hits
  in `products/catalyst/bootstrap/ui/src/`). Omitting it costs a round-trip and,
  worse, reads as an infrastructure failure when it is an admission policy.
  **This is a policy checkpoint, not just a field**: the mothership being
  single-region is exactly the deliberate exception #4706 contemplates, but it is
  the founder's own 2-region ruling being consciously set aside for this box.
- **FQDN**: **`hfmp.openova.io`** (proposed 2026-08-04 on founder steer "ideally
  it is supposed to be the openova itself"; label awaiting founder confirm).
  This is the naming canon's own shape — ARCHITECTURE §4 reserves
  `{location-code}.{sovereign-domain}` for mothership control-planes; the table's
  literal examples are `gitea.hfmp.openova.io` / `console.hnmp.openova.io`. Fire
  in **`sovereignDomainMode: "byo"`** with parent zone `openova.io`: byo never
  calls Dynadot at apply (`mapDomainModeForTofu`, provisioner.go:3742) and the
  parent-zone writer only ADDS `console.hfmp.openova.io` etc. records
  (`deployments.go:2197`) — the running mothership's apex hostnames are untouched,
  so the prov is collision-free by construction. The bare apex `openova.io` as
  sovereign FQDN is REJECTED: unsupported shape (first-label identity derivation,
  provisioner.go:3771, and parent-zone writes would target `.io`), and an apex
  prov would rewrite DNS for the very hostnames serving the prov mid-flight. LE
  note: the `*.hfmp.openova.io` wildcard counts against `openova.io`'s
  5-certs/week budget — verify before fire. (`omantel.biz` per the L3 rotation
  was the pre-steer pick; superseded — this box is not a test env, it is the
  mothership candidate.)
- **Law compliance**: ONE environment at a time (founder 2026-07-15) — wipe hw292
  (dep `1c56518035a83e03`, healthy, all extractable proofs banked in
  docs/sessions/2026-08-03..04 + UAT stamps) via the canonical wipe endpoint,
  `python3 scripts/reset-uat.py <env>` BEFORE fire, then fire.
- **Zero-touch**: no hand-patching during convergence; a wedge = a defect to fix
  at source, re-fire. DEBUG-BEFORE-WIPE if it fails (cloud-init log first).

### 3b. Offline IaC validation of the single-region Hetzner path (2026-08-04)

Run against a scratch copy of `infra/` with **synthetic, format-valid dummy
credentials** (no real tokens; nothing was applied, nothing provisioned):

| Step | Result |
|---|---|
| `tofu init -backend=false` | OK — hcloud 1.66.0 + aws 5.100.0 from the lock file |
| `tofu validate` | **Success! The configuration is valid.** |
| `tofu plan -refresh=false` (single-region tfvars) | **Plan: 24 to add, 0 to change, 0 to destroy** — no errors |
| `length(local.secondary_regions)` | **0** — confirms the audit's empty-set claim for single-region, measured not inferred |

🛑 **VACUITY CHECK — the plan being green does NOT clear the cloud-init guardrail.**
Measured directly via `tofu console`:

- `length(nonsensitive(local.worker_cloud_init))` = **4889** bytes vs the 30720 guard
  (`main.tf:1049`) — a real pass with a large margin.
- `length(nonsensitive(local.control_plane_cloud_init))` = **`(known after apply)`**
  — it depends on values not resolvable at plan time. Therefore the tight
  control-plane guard (`length(...) <= 32576`, only **192 B** below Hetzner's
  32768 hard cap, `main.tf:1039`) **was never evaluated by this plan**. A green
  plan is silent about it; the check fires at APPLY.

So the control-plane cloud-init size stays an **apply-time risk**, unchanged by
this exercise. Given the guard has been raised four times (31744 → 32256 →
32512 → 32576) it sits close to the ceiling, and a single-region render has
never been measured. If the prov dies at apply with "Rendered control-plane
cloud-init is N bytes, exceeds 32576", that is this, not an infra fault.

**Two tfvars-shape traps found the hard way** (both cost a failed plan; both
apply to the real POST body):
- `regions[]` element attributes are **camelCase** — `cloudRegion`,
  `controlPlaneSize`, `workerSize`, `workerCount`. snake_case is rejected.
- `parent_domains_yaml` is a **YAML inline array of objects**, e.g.
  `[{name: openova.io, role: primary}, {name: omani.homes, role: org-pool}]`.
  A newline-separated list of bare domains makes `yamldecode` return a scalar
  and `cloudinit-control-plane.tftpl:641` fails with "Iteration over
  non-iterable value" — which reads like a template bug and is not one.

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

### 5.0 Domain strategy — nothing is ever renamed; the front door swings

- New mothership sovereign FQDN: **`hfmp.openova.io`** (rationale in §3; label
  awaiting founder confirm).
- The apex well-known names (`console.openova.io`, `harbor.openova.io`, MX for
  `openova.io`) are a **front door**, distinct from either mothership's identity
  FQDN. Today they resolve to Contabo; elevation swings them to the Hetzner box
  (steps 5–7 below); rollback is the same swing reversed. Neither box is ever
  renamed — the old mothership keeps its identity until retirement, the new one
  is born `hfmp.openova.io` and keeps that identity after elevation.
- Post-elevation, `console.openova.io` being served by the new env makes the
  #5614 hardcoded issuer match by construction (step 7).

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
   The single most concrete failure point, narrowed by the 2026-08-04 code audit: the
   **#5090 dnsdist `hostPort:53` DNS front door**. The mechanism is sound and §854-clean
   (`platform/powerdns/chart/templates/dnsdist.yaml:39-98` — DaemonSet `hostPort: 53`,
   backing Service stays ClusterIP, never a NodePort; enabled only inside the
   `provider == "hetzner"` branch at `cloudinit-control-plane.tftpl:795`), but
   `infra/providers/hetzner/main.tf:199-201` flags it in-code as **LIVE-GATED: unproven
   until a real Hetzner prov confirms `dig @<node> <fqdn>` resolves**. If hostPort binding
   or the eBPF DNAT to the pod misbehaves on real Hetzner hardware, the Sovereign's own
   FQDN never resolves and Phase 1 stalls at ACME/cert issuance **even though k3s and Flux
   converge underneath** — which presents as a confusing healthy-but-unreachable cluster.
   Walk `dig` FIRST, before concluding anything else is broken.
2. **Single-region overlay is empty-set-safe** (audited, no fix needed before firing):
   `main.tf:566-570` filters `local.secondary_regions` on `i > 0`, so every secondary
   `for_each` resource materialises as an empty set — no `regions[1]` indexing, no
   `count = 2`. Cross-region bootstrap-kit components default OFF via envsubst fallback
   (`SOVEREIGN_ENABLE_HOT_STANDBY:-false`, `CONTINUUM_ENABLED:-false`,
   `SOVEREIGN_ENABLE_CNPG_PAIR:-false`, `CILIUM_CLUSTERMESH_PROXY_ENABLED:=false`) and
   render **empty-but-Ready rather than stuck NotReady**, so they cannot wedge the
   all-HRs-Ready convergence gate. No `evs-ssd`/`huaweicloud`/`me-east` leak reaches the
   Hetzner path (`EVS_CSI_ENABLED: "false"` at `cloudinit-control-plane.tftpl:771`).
3. Single cluster = no HA — same posture as Contabo today; accepted, and recoverable later by re-prov'ing multi-region once that tail is paid down.
4. LE budget on `openova.io` unknown-fresh (the FQDN moved under the apex per §3/§5.0) — verify before fire; staging-issuer fallback exists for walk-only phases.
5. ~~Mothership catalyst-api must be scaled up first (#5558 keeps it at 0)~~ — **RETRACTED on live evidence 2026-08-04**: `kubectl -n catalyst get deploy` shows `catalyst-api 1/1` and pod `catalyst-api-6667895dbc-b8hz9` Running for 31h. #5558 describes a past state; no hand-scale is needed. (Flux IS still dead — all four controllers at `replicas=0`, #5573 — but that does not block firing a prov, only GitOps-driven change to the mothership itself.)

**De-risked on 2026-08-04 (live, so the D1 plan drops a step):** the running mothership
image `fad88bdb9` is 430 commits behind main, but **zero of those 430 commits touch
`infra/providers/hetzner/` or `internal/provisioner/`** — and the tofu modules are baked
INTO the image at `/infra/providers/<provider>/` (`provisioner.go:1870,1923`), confirmed
by `kubectl exec` (`hetzner/` present with main.tf, variables.tf, versions.tf, outputs.tf,
cloudinit-worker.tftpl). **So the mothership can fire a current-code Hetzner prov with no
roll first.** A §854 scan of the BAKED module found every nodePort-range number to be a
cloud-init byte-size guardrail against Hetzner's 32768-byte `user_data` cap, or a comment
recording an *eradicated* nodePort — zero live nodePort wiring.

Refs #5618 (closed stale), #5573 #5558 #5567 #5348 (retired-with-box class), #5359 #5511 (deferred-multiregion root), #5614 (issuer hardcode), #5090 #5080 (unproven-live Hetzner commits).
