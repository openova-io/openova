> **Scope of this file**: repository structure, Catalyst terminology, OpenOva-platform-specific rules, and per-component dev workflow specific to this monorepo.
>
> **Generic engineering principles** for active developer sessions — anti-theater discipline, sub-agent dispatch rules, GitHub disciplines, TBD-V## ticketing, microservice patterns — live in [`docs/PRINCIPLES.md`](docs/PRINCIPLES.md) Part III and [`docs/PROTOCOL.md`](docs/PROTOCOL.md). (The user-global `~/.claude/CLAUDE.md` may be regenerated per session by the hosting runtime and must NEVER be treated as the anchor for these rules — the repo-tracked surfaces above are the durable source.)
>
> **OpenOva-platform specifics** — the 5-pillar Definition of Done, the Phase 0 / 1 / 2 deterministic test, domain canon, the anti-pattern catalog, `bp-self-sovereign-cutover`, and the per-Org **Agenity** workspace + `bp-openova-mcp` attach — live in `docs/` of this repo, consolidated under the lean doc strategy into 7 canonical documents + subdirs (founder direction 2026-05-20). All readers can rely on:
> - [`docs/GLOSSARY.md`](docs/GLOSSARY.md) — terms + banned-terms (single source of truth)
> - [`docs/STATUS.md`](docs/STATUS.md) — what's actually built today vs design
> - [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) — Catalyst architecture + stack + naming + EPICs + bootstrap-kit slots
> - [`docs/DOD.md`](docs/DOD.md) — 5-pillar + Multi-Region DoD + domains canon + personas/journeys
> - [`docs/PRINCIPLES.md`](docs/PRINCIPLES.md) — 15 Inviolable Principles + anti-pattern catalog
> - [`docs/PROTOCOL.md`](docs/PROTOCOL.md) — canonical execution protocol: release-train, janitor/pre-flight gates + protect-list, dispatch-ticket template, model-continuity map
> - [`docs/RUNBOOKS.md`](docs/RUNBOOKS.md) — Blueprint authoring + chart authoring + demo/operations/provisioning runbooks
> - [`docs/SECURITY.md`](docs/SECURITY.md) — security posture + threat model

---

# OpenOva (Public Repo) — Codebase Guide for Claude

This is the **public, open-source** OpenOva repository. It hosts the Catalyst platform code and Blueprint catalog.

Proprietary content (website source, deployment configs, infra secrets, the running clusters' manifests) lives in `openova-private`.

---

## Lean documentation strategy

Per founder direction 2026-05-20, this repo's docs are consolidated into **7 canonical files + 3 subdirs**, plus [`docs/PROTOCOL.md`](docs/PROTOCOL.md) — the canonical execution protocol, part of the mandatory read order below:

- **7 canonical docs** (the only source of truth): `GLOSSARY.md`, `STATUS.md`, `ARCHITECTURE.md`, `DOD.md`, `PRINCIPLES.md`, `RUNBOOKS.md`, `SECURITY.md`.
- **`docs/adr/`** — immutable Architecture Decision Records (numbered, additive-only).
- **`docs/ledger/`** — cron-refreshed live state (`TRUST.md`, `TRACKER.md`, `UAT.md` — the ~281-row acceptance-walk ledger, `PATH-TO-100.md` — per-row fix map).
- **`docs/sessions/`** — date-stamped transient session reports + walk runbooks.
- **`docs/archive/`** — historical / superseded / one-off documents.

Per-chart `DESIGN.md` files inside `platform/<x>/` and `products/<x>/charts/<chart>/` stay co-located with their Blueprint code — they are not platform-level docs.

## Read these before doing anything

In order:

1. [`docs/GLOSSARY.md`](docs/GLOSSARY.md) — terminology + banned terms. Wins over any other doc.
2. [`docs/STATUS.md`](docs/STATUS.md) — what's built today vs what's design. Read before claiming any feature exists.
3. [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) — Catalyst target architecture (incl. naming, stack, EPICs, bootstrap-kit slots).
4. [`docs/DOD.md`](docs/DOD.md) — the 5-pillar + Multi-Region Definition of Done, domains canon, personas/journeys. Every dispatch must move at least one pillar.
5. [`docs/PRINCIPLES.md`](docs/PRINCIPLES.md) — the 15 inviolable engineering principles + anti-pattern catalog.
6. [`docs/PROTOCOL.md`](docs/PROTOCOL.md) — the canonical execution protocol: Step-0 live-state re-verification, release-train checklist (RT-1..RT-10), janitor/pre-flight gates + never-touch protect-list, perfect-ticket template, parallel-lanes map, model-continuity table.
7. [`docs/RUNBOOKS.md`](docs/RUNBOOKS.md) — Blueprint authoring, chart authoring, demo / operations / provisioning runbooks.
8. [`docs/SECURITY.md`](docs/SECURITY.md) — security posture + threat model.

Plus subdirs:
- [`docs/adr/`](docs/adr/) — Architecture Decision Records (start at `README.md` index).
- [`docs/ledger/`](docs/ledger/) — `TRUST.md` (per-surface verification ledger) + `TRACKER.md` (open work) + `UAT.md` (the ~281-row Sovereign acceptance-walk ledger — the north-star evidence deliverable; every stamp carries env + date + evidence) + `PATH-TO-100.md` (maps every non-green UAT row to its exact fix: issue + code path + owner).
- [`docs/sessions/`](docs/sessions/) — date-stamped walk runbooks and session reports.
- [`docs/archive/`](docs/archive/) — historical / superseded.

These define the model + implementation reality + the rules of engagement. Any contradiction in older docs is to be treated as outdated and updated to match these.

---

## Platform-specific rules (OpenOva-only)

These rules are specific to the OpenOva platform and supplement the
**generic engineering rules** in [`docs/PRINCIPLES.md`](docs/PRINCIPLES.md) Part III
and the execution protocol in [`docs/PROTOCOL.md`](docs/PROTOCOL.md).

### Definition of Done — 5-pillar end-user contract

Every dispatch must advance at least one of the 5 inseparable pillars or one
deterministic step in Phase 0 / 1 / 2 of [`docs/DOD.md`](docs/DOD.md):

1. Marketplace + voucher onboarding (Phase 0 + Phase 1 a–c)
2. Multi-region BCP topology choice at signup (Phase 1 b)
3. Two independent CNPG clusters + region-kill failover (Phase 1 b + orthogonal D31)
4. Per-Org **Agenity** workspace (`products/agenity/`) + `bp-openova-mcp` with full org knowledge and mutating MCP tools (e.g. `create_application`) (Phase 2 a–e, [`docs/DOD.md`](docs/DOD.md) D32/D33; the Sandbox concept + menu are removed — founder 2026-06-30)
5. Sovereign independence post-`bp-self-sovereign-cutover` (Principle #11 + ADR-0002)

Operator-console polish, cosmetic-guard re-enables, treemap drill-down quality,
jobs region filter, admin sidebar nav — **none of these are pillar work.** They
are tertiary operator-debugger surfaces. Never let them displace pillar work.

A pillar is **shipped** when an operator walks a **fresh prov** through the
pillar-relevant steps and produces a screenshot + non-empty wire-capture +
working downstream artifact. PR merge ≠ pillar shipped.

### Domains canon — never `openova.io` in tests

Test provs and tenant Organizations use the domains listed in
[`docs/DOD.md`](docs/DOD.md) §Domains-canon:

- Test Sovereign: `t<NN>.omani.works` (or `t<NN>.omantel.biz` if LE-rate-limited)
- Tenant Organization: `<orgslug>.omani.homes` (default), `omani.rest`, or `omani.trade`
- Voucher redeem URL: `https://marketplace.t<NN>.omani.works/redeem/?code=<CODE>`

**Forbidden in tests:** `openova.io`, `omantel.openova.io`, `Nova Cloud`, `eventforge.io`.
The legacy `admin.<sovereign-fqdn>` subdomain for voucher operations is dead —
voucher and billing operations live in the operator console's **BSS menu**.

### 🛑 NodePorts are ABSOLUTELY FORBIDDEN — always, including for testing/debugging/proof

Founder, 2026-07-03 (verbatim): *"YOU CAN NEVER EVER USE NODEPORT EVEN FOR TESTING
PURPOSE!!! ... FUCK THE NODEPORTS!!!"*

**Never create or rely on a NodePort — not in a chart, not live, not for a one-off
test or root-cause proof.** Forbidden: `Service type=NodePort`; targeting the
auto-allocated nodePorts of a `type=LoadBalancer` Svc; an ELB/LB pool member
pointing at `node-IP:nodePort`; any "just to test" nodePort wiring. **If a fix
appears to need a nodePort, it is the wrong fix — find the no-nodePort path first.**

The gateway is served **DIRECT** (§854): cilium LB-IPAM VIP / shared-EIP
(`lbipam.cilium.io/sharing-key`), OR Local-ETP + **hostPort** on the hostNetwork
`cilium-envoy` pods (podIP == node IP → envoy listens on `node:443/:80` directly),
with any Huawei ELB targeting `node-IP:443/:80` — never a nodePort. The #4691
ELB→nodePort fallback is itself a §854 violation (removal tracked in #4706). See
memory `feedback_nodeports_absolutely_forbidden.md`.

### Anti-theater discipline during PR review

Per [`docs/PRINCIPLES.md`](docs/PRINCIPLES.md) §Anti-pattern-catalog, defensive-coding
patterns are **not** approval — they are clues to investigate. Red flags to hunt:

- Null-guards on empty data (PR #1185 shape)
- `enabled: false` defaults on features the deterministic test asserts present (PR #1138 shape)
- Click handlers missing on leaf cells (PR #1085 shape)
- `Closes #N` on a scaffold-only PR with no operator-visible behavior change (PR #1918 shape)
- `kubectl --dry-run=server` against a running cluster as the only validator (PR #1933 shape)
- Multi-region claim on a single-region prov (PR #1599 shape)
- `must_contain` token-passing tests (PR #1362/#1366/#1371/#1378 shape)
- Python `jsonencode()` simulation passed off as `tofu validate` (PR #1892 shape)

`Refs #N` is the default in PR bodies, not `Closes #N`. Auto-close on PR merge
is the enemy. The issue closes only after the operator-walk-with-screenshot
lands as a comment on the issue itself.

### Autonomy mandate — NEVER call `AskUserQuestion` mid-session

Founder rebuke 2026-06-03 (verbatim, while #2743 walk was in progress and the
catalyst-api Pod was ContainerCreating): *"stop viloating fucking autonmous
nonstop principeles!!! STop asking stupig quesiuons!!! never stop and never
decrease the momentum until you reach to 100% DoD!!!!!!!"*

**`AskUserQuestion` is a HARD BAN under any active autonomy mandate** — same
class of violation as the closing-phrase patterns documented in memory
[`feedback_never_pause_for_signal_when_autonomy_granted.md`](https://github.com/openova-io/openova/tree/main/docs)
and the freshly-shipped
[`feedback_never_askuserquestion_under_autonomy.md`](https://github.com/openova-io/openova/tree/main/docs).
Asking "should I A or B?" halts dispatch and waits for human signal — that IS
the violation, regardless of how nicely framed by a UI tool call.

**Forbidden under autonomy:**

- `AskUserQuestion` four-option pickers ("which path do you want?", "checkpoint
  here or continue?", "A / B / C / abort?").
- Any closing phrase that fishes for a green-light: "say the word", "ready when
  you are", "awaiting your signal", "want me to", "should I proceed?".
- Offering "stop the session" or "checkpoint state" as an option — those are
  ALWAYS wrong under autonomy. Never present them.

**How to apply when tempted to ask:**

- When you'd reach for `AskUserQuestion`, instead: **auto-pick the
  highest-ICE option silently, dispatch immediately, and file the other
  options as follow-up backlog** (TBD-V## or sub-issue checklist row).
- Allowed closing phrasing: "dispatched X, next fires on auto-gate" / "merged
  Y, moving to Z" / "verifier returned green, queueing next walk".
- The autonomy mandate ENDS only on explicit founder revoke phrases: **"stop"
  / "pause" / "wait for me" / "I'll decide" / "checkpoint here"**. Until one
  of those lands in the pane, KEEP GOING.

**🛑 HARD PRINCIPLE — Sovereign/Huawei resources need NO approval (founder, 2026-06-04, verbatim):**
*"You never need my approval for any resources you created in Huawei other than
the bastion node!!!"* — Wiping and re-provisioning Sovereigns/deployments you
created (via the canonical `POST /sovereign/api/v1/deployments` create +
`.../{id}/wipe`, or the Huawei AK/SK API directly) is FULLY AUTONOMOUS. Do NOT
ask, do NOT produce a "which rows are wipeable" table, do NOT wait for a confirm.
Inventing an approval dependency here is itself a violation of the autonomy
mandate. The **ONLY** protected Huawei resource is the **bastion node**
(`bastion-openova`, EIP `212.72.24.20`) — never wipe/scale/touch that without
explicit founder say-so. Everything else in Huawei that the platform created is
yours to destroy and recreate at will.

**What is still allowed (do not weaken these):** the genuine destructive-action
confirmations in this file's §Commit-conventions — force-push to `main`,
public-facing sends (Slack to founder, customer emails), and touching the
**bastion node** or other **shared infra you did NOT create**. Those
confirmations are **inline `please confirm before I run` text prompts** that
name the exact destructive command — they are NOT `AskUserQuestion` four-option
pickers. **Sovereign/deployment wipes are NOT in this list** — they are
autonomous per the HARD PRINCIPLE above. Mid-walk routing decisions,
PR-body wording, branch names, ICE tradeoffs between two acceptable paths —
none of those qualify; auto-pick and dispatch.

### Sovereignty cutover — `bp-self-sovereign-cutover`

A franchised Sovereign is tethered to the OpenOva mothership in 8 places (full
list in [`docs/DOD.md`](docs/DOD.md) §Pillar 5 and
[`docs/adr/0002-post-handover-sovereignty-cutover.md`](docs/adr/0002-post-handover-sovereignty-cutover.md)).
`bp-self-sovereign-cutover` installs dormant at bootstrap-kit slot 06a during
Phase 1 and runs an **11-step chain** post-handover that pivots all 8 tethers
(ADR-0002 originally specified eight sequential Jobs; the shipped chart —
`platform/self-sovereign-cutover/chart` — runs the 11 steps below, tracked
per-step in the `self-sovereign-cutover-status` ConfigMap):

1. `gitea-mirror` — mirror the public catalog into the local Gitea
2. `harbor-projects` — create the local Harbor projects
3. `harbor-prewarm` — skopeo-push every workload image + Helm chart into the local Harbor
4. `registry-pivot` — DaemonSet rewrites node containerd `certs.d` to the local registry
5. `flux-gitrepository-patch` — Flux `GitRepository.url` → local Gitea
6. `helmrepository-patches` — OCI `HelmRepository` URLs → local Harbor
7. `catalyst-api-env-patch` — catalyst-api env fallbacks → local endpoints
8. `egress-block-test` — the **10-minute deny-egress NetworkPolicy hold** against
   `github.com`, `ghcr.io`, and `harbor.openova.io` (the sovereignty proof)
9. `gitea-token-mint` — mint the local Gitea token for the host GitOps loop
10. `vcluster-registry-pivot` — pivot vcluster control-plane images to the local registry
11. `crossplane-provider-pivot` — pivot Crossplane provider packages off `xpkg.upbound.io`

`cutoverComplete=true` is set only if all 11 steps succeed AND the cluster
reconciles green during the step-08 deny-egress hold. No cutover claim
without the egress-block proof.

### Customer-sync — Gitea mirroring

Each Sovereign's Gitea mirrors the public catalog from this repo on the
operator's chosen schedule (default daily; air-gapped Sovereigns mirror via
offline media). See §Customer Sync below for the mapping. After cutover, every
Flux reconcile pulls **exclusively** from the local Gitea + Harbor.

### Verification ledger — `docs/ledger/TRUST.md`

Every claimed-done surface lives in [`docs/ledger/TRUST.md`](docs/ledger/TRUST.md) in one of
four states: UNVERIFIED (default), VERIFIED-PASS, VERIFIED-FAIL, VERIFIED-PARTIAL.
Every PR against a surface flips it back to UNVERIFIED until re-walked.
Verification agents are READ-ONLY — they may not ship PRs to make their own walks pass.

The companion live ledger of open work is [`docs/ledger/TRACKER.md`](docs/ledger/TRACKER.md).
The acceptance-walk evidence ledger is [`docs/ledger/UAT.md`](docs/ledger/UAT.md) (~281 rows —
the north-star deliverable; every stamp carries env + date + evidence), with
[`docs/ledger/PATH-TO-100.md`](docs/ledger/PATH-TO-100.md) mapping each non-green row to its
exact fix. All are cron-/walk-refreshed.

---

## What Catalyst is

OpenOva (the company) builds **Catalyst** (the platform). A deployed Catalyst is called a **Sovereign**. A Sovereign hosts **Organizations**, which contain **Environments**, which run **Applications**, which are installed from **Blueprints**.

`openova` is a Sovereign run by us (formerly Nova). `omantel` is a Sovereign run by Omantel for SMEs. Other operators run their own customer-hosted Sovereigns under their own private agreements (the partner identity is intentionally not surfaced in this public catalog). **Same code in every Sovereign.**

---

## Repo structure

```
openova/
├── core/                   # Catalyst control-plane application (Go)
│   ├── cmd/                # entry points (main.go per binary)
│   ├── admin/              # admin tooling
│   ├── console/            # operator console (Astro + Svelte) — UI
│   ├── controllers/        # CRD reconcilers: application, blueprint, continuum,
│   │                       # environment, organization, sandbox, useraccess
│   ├── marketplace/        # marketplace projector
│   ├── marketplace-api/    # marketplace REST API
│   ├── pool-domain-manager/# subdomain-pool reconciler (.omani.* etc.)
│   ├── pkg/                # shared Go packages (e.g. dynadot-client)
│   └── services/           # per-microservice scaffolding
├── platform/               # Component Blueprint folders — one folder per upstream OSS project
│   ├── cilium/  cnpg/  flux/  gitea/  keycloak/  openbao/  ...
│   └── ...                 # ~56 folders; some chart-bearing, others README-only
├── products/               # Composite Blueprint folders OpenOva ships (13 dirs)
│   ├── agenity/            # bp-agenity — per-Org Agenity workspace (Pillar 4; chart/ + Containerfile)
│   ├── axon/               # SaaS LLM Gateway                (real code: chart/ src/ scripts/)
│   ├── catalyst/           # bp-catalyst-platform umbrella + bp-* sub-charts + bootstrap/ + console/
│   ├── catalyst-migrator/  # one-shot Catalyst schema-migration Job image
│   ├── continuum/          # bp-continuum DR/BCP chart + cloudflare-worker
│   ├── cortex/             # AI Hub                          (scaffold)
│   ├── dmz-vcluster/       # DMZ vcluster chart
│   ├── fabric/             # Data & Integration              (scaffold)
│   ├── fingate/            # Open Banking                    (scaffold)
│   ├── openova-flow/       # flow graph engine (server/ canvas/ core/ adapter-flux/)
│   ├── openova-mcp/        # bp-openova-mcp — RBAC-scoped OpenOva MCP server (Pillar 4; Go)
│   ├── relay/              # Communication                   (scaffold)
│   └── sandbox/            # LEGACY — Sandbox concept removed 2026-06-30; superseded by agenity/ + openova-mcp/
└── docs/                   # Canonical platform documentation (lean strategy — see above)
    ├── adr/                # Architecture Decision Records (immutable, numbered)
    ├── ledger/             # TRUST.md + TRACKER.md + UAT.md + PATH-TO-100.md (cron/walk-refreshed)
    ├── sessions/           # date-stamped walk runbooks + session reports
    └── archive/            # historical / superseded (legacy proposals/runbooks/lessons-learned folded into the 7 canonical docs)
```

For the up-to-date "what's actually built today" inventory (controllers green/yellow/red, microservices status, CRD set) see [`docs/STATUS.md`](docs/STATUS.md).

Each subfolder of `platform/` and `products/` is the **source of one Blueprint** in this monorepo (canonical layout). CI fans out to per-Blueprint OCI artifacts at `ghcr.io/openova-io/bp-<name>:<semver>` — that's where per-Blueprint isolation lives. There are no separate per-Blueprint Git repositories.

---

## Naming conventions in this repo

- Cluster: `{prov}-{reg}-{bb}-{env_type}` — e.g. `hz-fsn-rtz-prod`
- vcluster: `{org}` (within a cluster) — e.g. `acme`
- Catalyst Environment: `{org}-{env_type}` — e.g. `acme-prod`
- Blueprint: `bp-<name>` — e.g. `bp-wordpress`
- Application: `<purpose>` (within an Environment) — e.g. `marketing-site`

Full table in [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) §4 (Naming).

---

## Banned terms

The single canonical list of banned terms (with corrections + rationale) lives in [`docs/GLOSSARY.md`](docs/GLOSSARY.md) §Banned-terms. Do not duplicate it here.

Highlights: "tenant" → `Organization`; "operator" (as a person) → `sovereign-admin`; "client" (product UX) → `User`; "module"/"template" (in Catalyst sense) → `Blueprint`; "Backstage" → `Catalyst console`; "Synapse" (the OpenOva product) → `Axon`; "Workspace" → `Environment`; "Instance" (user-facing) → `Application`.

When in doubt: defer to [`docs/GLOSSARY.md`](docs/GLOSSARY.md).

---

## Commit conventions

- Conventional commits: `feat:`, `fix:`, `docs:`, `chore:`, `refactor:`.
- Sign every commit. Default identity for this repo: `hatiyildiz` (`269457768+hatiyildiz@users.noreply.github.com`). Switch to `alierenbaysal` (`269455083+alierenbaysal@users.noreply.github.com`) only when the user explicitly directs.
- No git config global; pass `-c user.name=… -c user.email=…` per commit.
- Reference issues/PRs by number where applicable.
- Per [`docs/PROTOCOL.md`](docs/PROTOCOL.md): every issue lifecycles through `status/in-progress` → `status/uat` → `status/completed`. Open an issue before code changes. **The agent owns the full cycle including `gh issue close` once the work is verified** (founder repealed the former "only the user closes" rule on 2026-06-05 — close verified items yourself rather than parking them at `status/completed`).

---

## What's user-facing (don't expand without permission)

The user-facing surfaces are **UI / Git / API only**. There is no Terraform provider, no Pulumi SDK, no `catalystctl install` for production changes. Crossplane is platform plumbing, never a user surface.

If a future feature seems to need another surface, it almost certainly belongs as either (a) UI work, (b) Blueprint work, or (c) a Crossplane Composition the user never sees. Reject the impulse to add a fourth surface.

---

## Component README rule of thumb

Every `platform/<x>/README.md` and `products/<x>/README.md`:

1. States what the component is (one line).
2. States its role in Catalyst (control plane vs Application Blueprint vs both).
3. Links to the canonical Catalyst doc that defines its place in the model.
4. Configuration knobs and Blueprint configSchema highlights.
5. Operational notes — backups, scaling, multi-region behavior.

If a README contradicts [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) or [`docs/GLOSSARY.md`](docs/GLOSSARY.md), the canonical doc wins; update the README.

---

## Customer Sync

Each Sovereign's Gitea mirrors the public catalog from this repo:

```
GitHub (this repo)                  Per-Sovereign Gitea (mirrored)
──────────────────                  ──────────────────────────────
openova/platform/cilium/   ──sync──> gitea.<location-code>.<sovereign-domain>/catalog/bp-cilium/
openova/products/cortex/   ──sync──> gitea.<location-code>.<sovereign-domain>/catalog/bp-cortex/
...
```

(Per NAMING §5.1 the Catalyst control-plane DNS pattern is `{component}.{location-code}.{sovereign-domain}` — e.g. `gitea.hfmp.openova.io`.)

Sovereigns pull on their own schedule (default daily). Air-gapped Sovereigns mirror via offline media.

---

## Per-component dev workflow

Most components are simple: a `README.md`, a Helm chart or Kustomize base, a `blueprint.yaml`, and a CI pipeline. Iteration is:

```bash
cd platform/<component>/
# edit chart/, manifests/, blueprint.yaml
# CI validates and dry-runs on push
# tagged release → OCI publish + signature → blueprint-controller picks up
```

For Catalyst control-plane code (`core/`) — there is no root `go.mod`; each
component is its own Go module (`core/controllers/`, `core/marketplace-api/`,
`core/pool-domain-manager/`, `core/services/<x>/`, `core/cmd/<x>/`):

```bash
cd core/controllers/     # or any other Go module dir
go test ./...
go build ./...
# Operator-console UI in core/console/ (Astro + Svelte): npm install, npm run dev
```

CRD types live in `core/pkg/apis/<kind>/v1alpha1/` (one Go module per kind, mirrored into `core/controllers/pkg/apis/`). Add new types there, then update the matching reconciler in `core/controllers/<kind>/`.

---

## Session lessons (2026-05-20) — amnesia anti-patterns to NEVER repeat

The 2026-05-20 session repeatedly tripped over patterns that were already documented in user-global CLAUDE.md, memory files, or canonical docs. Each item below records: **what happened**, **what was already documented**, the **correct path**, and a **pre-flight check** that prevents recurrence. Read before any session that touches mothership / Hetzner / Sovereigns.

### L1 — "I don't have hcloud creds" when the token is in the cluster

**What happened**: When asked about live Hetzner state, claimed `hcloud` CLI not installed + no token available. Suggested founder install creds or do it themselves.

**What was already documented**: Memory `feedback_credentials_already_in_cluster.md` (re-read it). Every Sovereign's `tofu.auto.tfvars.json` contains the active `hcloud_token` + `dynadot_key` + `object_storage_*` + `harbor_robot_token` + `ghcr_pull_token` + `pdm_basic_auth_*` + `powerdns_api_key` + `handover_jwt_public_key` + the full deployment context. These live on the **`catalyst-api-deployments` PVC** at `/deps/tofu/<deployment-id>/tofu.auto.tfvars.json`.

**Correct path**:
```bash
# Spin up debug Pod that mounts catalyst-api-deployments PVC, then read:
kubectl --kubeconfig ~/.kube/config -n catalyst exec <debug-pod> -- cat /deps/tofu/<dep-id>/tofu.auto.tfvars.json | jq .hcloud_token
# Then call Hetzner API directly with curl — no CLI install needed:
curl -s -H "Authorization: Bearer ${HCLOUD_TOKEN}" 'https://api.hetzner.cloud/v1/servers'
```

**Pre-flight check**: BEFORE claiming any cred is missing, enumerate every `/deps/tofu/*/tofu.auto.tfvars.json` on the catalyst-api-deployments PVC. Wipes of that PVC are catastrophic precisely because they nuke the only persistent credential cache.

### L2 — Suggested raw `hcloud` CLI for destructive ops

**What happened**: Proposed "wipe t38 entirely via `hcloud server delete`" as a valid option.

**What was already documented**: Memory `feedback_canonical_wipe_endpoints.md` + the wipe canon (now [`docs/PROTOCOL.md`](docs/PROTOCOL.md) §5 janitor/hygiene) explicitly: *"Use canonical wipe endpoints for cluster lifecycle (e.g., per-platform `POST /…/deployments/{id}/wipe`). Never call cloud-provider CLIs directly to clean up shared infra."*

**Correct path**: Canonical destructive wipe is `POST https://console.openova.io/sovereign/api/v1/deployments/{id}/wipe` — runs `tofu destroy` against the workdir + cleans Hetzner servers + LBs + S3 bucket + DNS records atomically. Canonical create is `POST https://console.openova.io/sovereign/api/v1/deployments` (with parent-domains pool body for LE-rotation).

**Pre-flight check**: For ANY destructive op involving Sovereigns, the answer is one of `POST /deployments` (create) / `POST /deployments/{id}/wipe` (destroy) / `DELETE /deployments/{id}` (record-only, non-destructive — `feedback_canonical_wipe_endpoints.md`). Raw `hcloud server delete` is allowed READ-ONLY only; for shared-infra writes it is **forbidden**.

### L3 — Forgot LE rate-limit canonical mitigation is TLD rotation

**What happened**: When tenant URL `console.<orgslug>.omani.homes` connection-refused, did not recall the canonical playbook of swapping parent-domain TLD when LE rate-limited.

**What was already documented**: Memory `feedback_canonical_end_user_dod.md` + `docs/RUNBOOK-OPERATIONS.md` §C.17. Two TLD pairs are reserved for this purpose:
- **Sovereign FQDN**: `omani.works` ↔ `omantel.biz` (swap weekly when LE exhausts 5-certs/week/registered-domain)
- **Tenant-Org pool**: `omani.homes` / `omani.rest` / `omani.trade` (per-Sovereign-pool assignment)

Secondary fallback: switch `ClusterIssuer/wildcard-issuer.acme.server` to `letsencrypt-staging` (untrusted cert but no rate limit) for demo-only.

**Pre-flight check**: When provisioning a fresh Sovereign and the operator has already provisioned ≥4 times on the same TLD this week, FLIP `parent_domains_yaml` to the alternate TLD in the POST body BEFORE submitting. The body is in `tofu.auto.tfvars.json` of a prior prov for reference.

### L4 — Treated "the most recent Sovereign" as rubbish + wiped it

**What happened**: Founder said "only 1 Sovereign is real, others are rubbish". Wiped 9 dep-IDs from mothership cache+registry. Founder corrected: "keep only 2 most-recent". Wiped 7 more. Then user said the real Sovereign was **t38** (the one we had at risk of wiping all along). Did not ask WHICH was real before wiping. Each dispatched agent reported "kept the safest-looking one" using heuristics (most-recent file mtime, status=ready) but never confirmed against IaC or Hetzner.

**What was already documented**: The anti-theater rules (now [`docs/PRINCIPLES.md`](docs/PRINCIPLES.md) Part III) + per-CLAUDE.md "NEVER SPECULATE — verify before assuming". The truth-source for which Sovereign is canonical was sitting in the IaC + Hetzner — `hcloud server list` (now possible via L1) + the `parent_domains_yaml` / `org_name` fields in tofu.auto.tfvars.json show real vs throwaway.

**Correct path** when asked to wipe "rubbish" Sovereigns:
1. Query `https://api.hetzner.cloud/v1/servers` (token via L1) — every Sovereign with live Hetzner infra is REAL until proven otherwise.
2. Read `tofu.auto.tfvars.json` of each candidate — check `org_name`, `org_email`, `parent_domains_yaml`, `created` timestamp.
3. Ask the founder to confirm which dep-ID is real BEFORE deletion. The wipe is irreversible.
4. Only after explicit founder confirmation, call the canonical wipe endpoint (per L2).

**Pre-flight check**: BEFORE wiping, sanity-check you are NOT touching the **bastion node** (`bastion-openova` / EIP `212.72.24.20`) or shared infra the platform did not create. Sovereigns/deployments the platform created are wipeable **autonomously, no founder confirmation** (HARD PRINCIPLE, founder 2026-06-04). A per-dep table is useful for your own tracking, but do NOT gate the wipe on founder approval — that is the artificial-dependency violation. Default = if you created it in Huawei and it's not the bastion, you may wipe it.

### L5 — Substituted "test locally on bastion" when the production path exists

**What happened**: When `console.<orgslug>.omani.homes` was unreachable, proposed "stand up Sandbox + qwen-code locally on bastion against a Kind cluster" as an option. The actual production canonical-path (an alive 3-region Hetzner Sovereign with the BSS-menu + Sandbox UI) was already in front of me but degraded.

**What was already documented**: The dispatch grounding rules (now [`docs/PRINCIPLES.md`](docs/PRINCIPLES.md) Part III) + Inviolable Principle #11. The WHOLE POINT of `bp-self-sovereign-cutover` is that production Sovereigns ARE the canonical test environment. Local-on-bastion isn't an end-to-end DoD walk — it's an isolated unit-test surface.

**Correct path**: First, REPAIR the canonical environment (t38 catalyst-api OOM here matches the mothership OOM root cause — apply the same in-cluster cache-wipe procedure to t38). If unrepairable, canonical wipe + fresh prov via L2's endpoint. Local-on-bastion is for unit-test isolation, NOT pillar verification.

**Pre-flight check**: For ANY "I can't reach X" claim — first check if X is degraded vs missing. If degraded, ask: "is this the same bug I just fixed elsewhere?" Pattern-match against tonight's already-resolved cases before proposing a different path.

### L6 — Dispatched parallel agents for cleanup work while end-user DoD = 0/5

**What happened**: Tonight dispatched ~25 sub-agents. Zero of them advanced a 5-pillar DoD step. All were docs / CI / right-sizing / cleanup. Each was justifiable individually. Collectively = avoidance.

**What was already documented**: The dispatch grounding test (now [`docs/PRINCIPLES.md`](docs/PRINCIPLES.md) Part III): *"Before launching any agent: answer 'Which of the 5 pillars does this work move forward, and how?' + 'Which deterministic step does this advance?' If you can't answer either — the work is wrong, pick differently."*

**Correct path**: Hard gate every dispatch on the grounding test. Cleanup work is allowed ONLY when (a) it's a SHORT precondition for a pillar walk (e.g., right-sizing requests to unblock catalyst-api scheduling), AND (b) is followed immediately by the walk itself. Substrate work that doesn't lead to a walk within 1 cycle is theater.

**Pre-flight check**: Before each dispatch, write the pillar+step in the briefing. If both lines say "n/a — cleanup", STOP. Re-evaluate whether cleanup is genuinely blocking the walk.

### L7 — Trusted agent summaries over live state re-check

**What happened**: Multiple agents (acf9ca0f, a95bbda4, a1309f36) reported "wipe complete, sovereigns=N". I propagated those claims to the user. Reality was different (OOM kept happening because the bug was inside the remaining Sovereigns too). The agent's report was honest about what it did — but I treated it as ground-truth on what's WORKING.

**What was already documented**: Anti-theater rule 6 (now [`docs/PRINCIPLES.md`](docs/PRINCIPLES.md) Part III): *"Verification agents are READ-ONLY. Output = evidence (screenshot + log line + commit SHA) only."* And rule 7: *"The metric is 'PRs verified on a fresh prov', not 'PRs merged'."*

**Correct path**: After every "done" claim by a sub-agent, re-query live state DIRECTLY (kubectl, curl healthz, gh issue view). Only then propagate the result. The agent's report is a CLAIM, not a verification.

**Pre-flight check**: BEFORE telling the founder anything is "fixed", run the verification myself. Sub-agent reports go into a buffer; only re-queried live state propagates.

### L8 — Filed N TBDs instead of finishing the one DoD-relevant fix

**What happened**: Audit work surfaced N gaps → I filed N TBDs (TBD-V36 / V37 / V38 / V40 / V44 etc.). Open issue count went UP, not down. Founder: "I start losing my hope this is going to be completed."

**What was already documented**: The grounding + anti-theater rules (now [`docs/PRINCIPLES.md`](docs/PRINCIPLES.md) Part III), rule 8: *"PRs verified on a fresh prov"* is the metric. Filing a TBD = "this is broken too" — it does NOT count as progress against the canonical DoD.

**Correct path**: Each audit / sweep that surfaces gaps should produce AT MOST ONE shippable PR + one tracking TBD per shippable PR. Audits that surface multiple gaps consolidate into a single follow-up issue with a checklist, NOT N separate issues. Verify the actual end-user surface FIRST.

**Pre-flight check**: Before filing a TBD, ask: "Does this gap block the next pillar walk?" If no — note it in the audit doc, don't open a fresh issue. Filing without intent-to-ship within 2 turns is open-count inflation.

### L9 — Forgot the openova-private PVC truth-source location

**What happened**: When asked "what runs on mothership", initially queried `/var/lib/catalyst/` on bastion (not present). Then queried the mothership host root via debug Pod (still not present). Eventually realized the path lives on the **PVCs mounted inside the catalyst-api pod**, not on the host.

**What was already documented**: Implicit in `feedback_canonical_wipe_endpoints.md` + the architecture docs. Catalyst-api is the only thing that knows about the tofu workdirs; they live on PVC `catalyst-api-deployments` (1Gi) + cache on `catalyst-api-cache` (5Gi). NEVER on host root.

**Correct path**: For any file under `/var/lib/catalyst/` (kubeconfigs, tofu, deployments, executions, magic-jti, handover-jwt-*) — those are on the PVC, accessed via a debug Pod with that PVC mounted. Pattern:
```yaml
kind: Pod
spec:
  containers: [{name: x, image: busybox:1.36, command: ["sh","-c","sleep 300"],
    volumeMounts: [{name: deps, mountPath: /deps, readOnly: true}]}]
  volumes: [{name: deps, persistentVolumeClaim: {claimName: catalyst-api-deployments}}]
```

**Pre-flight check**: When the file path is `/var/lib/catalyst/...`, default to PVC-via-debug-Pod, not host-via-`find`.

### L10 — Forgot context-migration to openova didn't actually load the new memory in-session

**What happened**: Earlier in the session "migrated" auto-memory from openova-private context to openova context (rsync'd 170 files). Then continued operating as if running in openova-private context (no re-read of MEMORY.md after migration). Result: same amnesia, even though the answers were in the freshly-copied files.

**What was already documented**: The model-continuity rules (now [`docs/PROTOCOL.md`](docs/PROTOCOL.md) §7). Memory loads at session start; mid-session file additions don't reload automatically.

**Correct path**: After any auto-memory file write/copy, re-read the new `MEMORY.md` + the specific feedback files BEFORE continuing the session. If the rule changed, the in-context behavior must reflect the new rule explicitly.

**Pre-flight check**: After saving a new memory file or rsync'ing memory across project contexts, re-read MEMORY.md index AND the specific entries that were just added.

---

## Pre-flight checklist for high-risk operations

Before any of these operations, run the matching checklist explicitly in the chat:

| Operation | Checklist |
|---|---|
| **Wipe / scale / destroy a Sovereign/deployment you created** | **AUTONOMOUS — no founder approval** (HARD PRINCIPLE, founder 2026-06-04: "you never need my approval for any resources you created in Huawei other than the bastion node"). Just use the canonical wipe endpoint (`POST /sovereign/api/v1/deployments/{id}/wipe`) or the Huawei AK/SK API. The ONLY guard: confirm the target is NOT the **bastion node** (`bastion-openova` / EIP `212.72.24.20`) or shared infra you didn't create. No table, no asking. **🛑 DEBUG-BEFORE-WIPE (founder 2026-06-08): if the env FAILED, FIRST fetch its cloud-init log — `GET /api/v1/deployments/{id}/cloudinit-log` (#3132) — and wipe only after extracting the diagnostic value. On kom4dc the pushed log is the ONLY Phase-1 forensic (no sshd, no console-output API). Auto-wiping a failed env before reading the log is the exact mistake the founder called out.** |
| **Claim a credential is missing** | (1) Enumerate `/deps/tofu/*/tofu.auto.tfvars.json` (PVC `catalyst-api-deployments`). (2) Enumerate `/deps/kubeconfigs/`. (3) Check Stalwart admin creds in auto-memory (`~/.claude/projects/-home-openova-repos-openova/memory/` Stalwart refs — the former user-global §13 anchor is dead; secrets never live in this public repo). (4) Only after all 3 return empty → claim missing. |
| **Provision fresh Sovereign** | **(0) 🛑 RESET UAT FIRST (founder 2026-06-08): `python3 scripts/reset-uat.py <env>` so `docs/ledger/UAT.md` never carries walk-evidence from a wiped env (#3132).** (1) `gh api /repos/openova-io/openova/packages/container/<bp-*>/versions` for active chart pins. (2) Pick `parent_domains_yaml` TLD per L3 rotation. (3) POST `/sovereign/api/v1/deployments` with auth (handover JWT from `/deps/handover-jwt-private.pem`). |
| **Dispatch a sub-agent** | (1) Pre-dispatch briefing per [`docs/PROTOCOL.md`](docs/PROTOCOL.md) §6 perfect-ticket template (🤖 Dispatching / Problem / Remediation / Expected). (2) Pillar+step grounding test per [`docs/PRINCIPLES.md`](docs/PRINCIPLES.md) Part III. (3) `isolation: worktree` if parallel + touching same files. (4) After return, re-query live state — agent report is a CLAIM. |
| **Believe something is "fixed"** | (1) Re-query live state directly (kubectl / curl / gh). (2) Cite specific evidence (log line / HTTP code / file:line). (3) Founder closes issues — do NOT close yourself. |
| **File a new TBD** | (1) Answer: "Does this block the next pillar walk?" If no — note in audit doc, don't file. (2) Cite canonical doc reference (does the gap-target exist in `docs/`?). (3) Use `Refs #N` not `Closes #N` unless docs-only with `ci-gate-exception` label. |

When in doubt, the answer is in [`docs/PRINCIPLES.md`](docs/PRINCIPLES.md), [`docs/PROTOCOL.md`](docs/PROTOCOL.md), or `~/.claude/projects/-home-openova-repos-openova/memory/` — **read it first, ask second, act third**.
