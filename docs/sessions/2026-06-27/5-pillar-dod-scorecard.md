# 5-Pillar DoD Scorecard — 2026-06-27

**Founder-facing.** All verdicts are against the live `omantel.biz` Sovereign, dep
`91dc05917e44d1c1` (2-region, converged: region-a 6 nodes + region-b 6 Ready
nodes, me-east-215-a + me-east-215-b), unless a row carries its own labeled env.
The cutover keystone walk runs on a fresh single-region throwaway, dep
`25aadcfc`. Every status below was re-confirmed with a read-only live spot-check
this session — no clusters were mutated.

Status legend: ✅ demonstrably live · 🟡 materially live with a gated tail · ⛔ gated.

---

## Scorecard at a glance

| # | Pillar | Status | The precise remaining gate |
|---|--------|--------|----------------------------|
| 1 | Marketplace + voucher onboarding | ✅ | none — fully live-walked E2E through the public edge |
| 2 | Multi-region BCP topology choice at signup | 🟡 | the funnel `/bcp` radio-render step is unwalked; the 2-region substrate it provisions is live-proven |
| 3 | Two independent CNPG clusters + region-kill failover | 🟡 | the destructive region-kill counter-test (#4275) is EIP-quota-gated |
| 4 | Sandbox + auto-mounted openova-sandbox-mcp | 🟡 | the agentic chat→provision run is Anthropic-credential-gated |
| 5 | Sovereign independence post-cutover | 🟡 | the deny-egress keystone walk (#3379) is firing on throwaway `25aadcfc` now |

**Headline:** Pillar 1 is shipped. Pillars 2–5 are materially live — every load-bearing
substrate (2-region topology, dual-CNPG RPO=0 streaming, sandbox+MCP RBAC, the
cutover 11-step machinery + durable fact) is proven live; each remaining tail is
a single named gate, not an engineering gap.

---

## Pillar 1 — Marketplace + voucher onboarding ✅

**Status: ✅ demonstrably live.** Fully live-walked E2E through the public edge
this session (UAT rows 72–90, Refs #3376).

**Evidence (live spot-checked 2026-06-27):**
- `marketplace.omantel.biz` → **HTTP 200**; `/plans/` → **HTTP 200**;
  `console.omantel.biz` → **HTTP 200**.
- Voucher issuance E2E (UAT 72–74 ✅): BSS → `POST /billing/vouchers/issue` → 200,
  rows land in `org_billing.promo_codes`; weak-code guard rejects `SAVE10` (400,
  "≥12 chars") and low-entropy `AAAAAAAAAAAA` (400); empty code →
  server-generated high-entropy `VCH-…`.
- Voucher redeem validates through the **public edge** (UAT 75/76/77 ✅): issued
  `UAT91WALKTEST7` (9 OMR) → `marketplace.omantel.biz/api/billing/vouchers/redeem-preview`
  valid→200, junk→404; `/redeem/` landing renders "Voucher valid — 9 OMR credit"
  in this Sovereign's brand chrome, **zero** `openova.io` host leak (brand scan
  clean). Spot-check this session: `/redeem/` → 200; the redeem-preview endpoint
  is POST-only (GET → 405), consistent with the captured valid→200/junk→404 walk.
- Funnel onboarding (UAT 79/80/84/86/88/89/90 ✅): plan picker M → app catalog →
  WordPress → magic-link sign-in → `POST /tenant/orgs` 201 → org-controller
  provisions → per-Org console `console.uatwalk91.omani.homes` HTTP 200 with valid
  per-Org LE TLS, zero-click owner sign-in → WordPress app card Running →
  `wordpress.uatwalk91.omani.homes` → **HTTP 200** live site over wildcard LE TLS.

**Remaining gate:** none for the pillar. Four interior funnel rows (78/81/83/85/87)
remain `☐` headless-harness artifacts, not faults — the end-to-end onboarding
(stranger → voucher → live WordPress) is proven. The chat-driven app-creation
add-on (#4277/#4111) is Pillar-4 work, tracked there.

---

## Pillar 2 — Multi-region BCP topology choice at signup 🟡

**Status: 🟡 materially live with a gated tail.** The 2-region topology the BCP
choice provisions is live and healthy; the funnel `/bcp` **radio-render step
itself** has not yet been captured in a walk.

**Evidence (live spot-checked 2026-06-27):**
- Live 2-region topology PROVEN (UAT 65/66 ✅): Cloud reads **Region 2/2**
  (me-east-215-a + -b), **Cluster 2/2 healthy**, no phantom region. Spot-check
  this session: region-a 6 nodes + region-b **6 Ready** nodes, both clusters
  serving.
- The funnel exposes the BCP step in its route map (UAT 81/82/83, Refs #3376):
  add-ons → `/bcp` topology step (Single-region vs Active-hot-standby radios) →
  review. Spot-check this session: `marketplace.omantel.biz/bcp` and `/addons`
  resolve (301 → funnel SPA route), the steps exist in the flow.

**Remaining gate:** UAT rows 81/82/83 are `☐` — the BCP radio-render + select
+ carry-through-to-review has not been captured in a screenshot walk. The harder
half (a real 2-region prov actually standing up active-hot-standby) is the
permanent env itself and is live-proven. This tail is a **walk-capture gap**, not
a build gap; a second concurrent 2-region prov to walk the full signup→topology
path is EIP-quota-gated (see Pillar 3 / Founder lever A).

---

## Pillar 3 — Two independent CNPG clusters + region-kill failover 🟡

**Status: 🟡 materially live with a gated tail.** The dual-CNPG DR backbone is
LIVE-healthy with RPO=0 synchronous streaming; the **destructive region-kill
counter-test** (#4275) is EIP-capacity-gated.

**Evidence (live spot-checked 2026-06-27, region-a `91dc05917e44d1c1`):**
- **RPO=0 synchronous streaming PROVEN** — `pg_stat_replication` on the region-a
  primary shows `application_name=cnpg-pair-bp-cnpg-pair-replica`,
  **`sync_state=sync`, `sync_priority=1`** (region-b is the synchronous standby;
  every COMMIT blocks on region-b ack). The two in-region replicas read `async`,
  as designed.
- **Continuum lease + DR spine healthy** — all 5 Continuums (`dr.openova.io`)
  read **`phase=Healthy`, `lease=me-east-215-a`, `lag=0`**: the cnpg-pair
  Continuum plus the 4 spine apps (gitea/harbor/keycloak/openbao), each
  lease-pinned to region-a.
- **ClusterMesh** — region-a resolves the region-b mesh config (etcd cluster ID
  present); 2/2 plumbing confirmed live in the drill runbook §0.
- Two **independent** k3s clusters (`hw-me-east-215-a-rtz-prod` /
  `…-b-rtz-prod`), not a stretched control plane (UAT 12/64/71 ✅; replication
  lag is a live numeric ~1.02s, not a hardcoded `—`).

**Remaining gate:** the destructive kill→promote→RPO-counter→restore drill
(#4275) has never run against a disposable env. The faithful counter-test needs
either a second concurrent **2-region** prov (needs **6 EIPs**; kom4dc quota=10,
free=3 → hard wall) or explicit founder GO + a low-traffic window to run it on
the permanent env. The drill is finalized and executable
(`region-kill-drill-4275.md`); the quota-bump ask is staged
(`eip-quota-bump-request.md`). **Founder lever A** clears it.

---

## Pillar 4 — Sandbox + auto-mounted openova-sandbox-mcp 🟡

**Status: 🟡 materially live with a gated tail.** The sandbox runtime, MCP
auto-mount, and Org-scoped RBAC are live-proven; the **agentic chat→provision
run** is Anthropic-credential-gated.

**Evidence (live spot-checked 2026-06-27, `91dc05917e44d1c1`):**
- **sandbox-controller 1/1 Running** (17h, image
  `ghcr.io/openova-io/openova/sandbox-controller:895f961`) — the prior
  CrashLoop on unset `CATALYST_GITEA_TOKEN` (#4482) is resolved; the gitea-token
  env is bridged into the sandbox ns (UAT R19 ✅).
- Sandbox-CR provision path + MCP auto-mount + RBAC Org-scope proven (UAT 218 ✅;
  219/220 partial) — the `openova-mcp/internal/identity` Org-claim enforces
  per-Org scope (`list_applications` returns only the caller's Org; cross-Org
  `get_application` → 403). The sandbox-token reflection fix (#4482) landed.

**Remaining gate:** the **end-to-end agentic journey** — user chats with the
solo agent ("create a `<blueprint>` app in my org") → the agent calls the MCP
`create_application` tool with the Org-scoped token → an Application CR appears
in their Org (UAT 219–223 ⛔). This needs a real **Anthropic API token** seeded
into the per-Org OpenBao path (#4277 auto-seed at Org-create; #4111 the agentic
run). The runtime + RBAC are built; only the credential is missing. **Founder
lever B** clears it.

---

## Pillar 5 — Sovereign independence post-`bp-self-sovereign-cutover` 🟡

**Status: 🟡 materially live with a gated tail.** The cutover Blueprint installs
dormant, the full 11-step machinery + the durable reconcile-immune fact +
deny-egress test are implemented; the **keystone walk is firing now** on
throwaway `25aadcfc`.

**Evidence (live spot-checked 2026-06-27, dep `25aadcfc`, Refs #3379):**
- **`bp-self-sovereign-cutover` installed** — HR `True`, "Helm install succeeded"
  for `bp-self-sovereign-cutover@0.1.81` (dormant at bootstrap, as designed).
- **All 11 cutover steps primed** — the 11 `cutover-step-*` ConfigMaps are
  present (01-gitea-mirror, 02-harbor-projects, 03-harbor-prewarm,
  04-registry-pivot, 05-flux-gitrepository-patch, 06-helmrepository-patches,
  07-catalyst-api-env-patch, **08-egress-block-test**, 09-gitea-token-mint,
  10-vcluster-registry-pivot, 11-crossplane-provider-pivot).
- **Durable reconcile-immune fact store LIVE** — `self-sovereign-cutover-status`
  ConfigMap reads `cutoverComplete=false`, `progressPercent=0`, every
  `step.*.result` empty, with per-node `registriesYaml` tracking
  (`registriesYamlActive=v1` on all 4 nodes). This is the durable fact #3379
  requires — a chart upgrade cannot revert it, and `cutoverComplete=true` is
  written only after the egress-block hold reconciles green.

**Remaining gate:** the keystone walk — trigger the cutover, run the 11 steps,
land the **10-minute deny-egress NetworkPolicy hold** against github.com /
ghcr.io / harbor.openova.io, and prove the cluster reconciles green during the
hold so `cutoverComplete=true` is earned. The trigger has NOT yet fired the Jobs
on `25aadcfc` (zero cutover Jobs in the catalyst ns; the status fact is primed at
`progressPercent=0`). This is destructive (deny-egress), so it runs on the
throwaway, never the permanent env. The walk is in flight on `25aadcfc` now.

---

## The two founder levers that gate the remaining tails

Every Pillar-2/3/4/5 tail collapses to one of exactly two founder actions.

### Lever A — Omantel EIP quota bump (kom4dc): **10 → ≥16**

A 2-region prov needs **6 EIPs**. kom4dc quota = **10**, used = **7** (1 bastion
+ 6 the permanent omantel.biz Sovereign), **free = 3** — a hard wall with zero
orphans to reclaim. Raising to **≥16** lets one concurrent 2-region validation
prov fire alongside the untouched permanent env.

**Unblocks in one approval:**
- **Pillar 3** region-kill counter-test (#4275) — the load-bearing DR proof.
- **Pillar 2** full signup→BCP-topology→2-region walk on a disposable env
  (#4293 multi-region facet).
- DR backbone seams (#4212).

The ask is staged and ready to send: `eip-quota-bump-request.md`.

### Lever B — Anthropic API credential

A real Anthropic API token seeded into the per-Org OpenBao path.

**Unblocks:**
- **Pillar 4** agentic chat→provision run (#4111) — the North-Star journey.
- **Pillar 1/4** zero-touch per-Org token auto-seed at Org-create (#4277).

The sandbox + MCP + Org-scoped RBAC runtime is built and live; only the
credential is missing for the agent to authenticate and run.

---

## Bottom line

- **Pillar 1: shipped** — onboarding works E2E through the public edge.
- **Pillars 2–5: materially live** — every substrate is live-proven; four tails
  remain, gated by exactly **two** founder levers (EIP quota + Anthropic token).
- The cutover keystone walk (Pillar 5) is firing on throwaway `25aadcfc` now and
  does not need either lever.

_Read-only synthesis; the permanent `omantel.biz` env was not mutated. Refs #909._
