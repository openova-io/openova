> **Scope of this file**: repository structure, Catalyst terminology, OpenOva-platform-specific rules, and per-component dev workflow specific to this monorepo.
>
> **Generic engineering principles** for active developer sessions — anti-theater discipline, sub-agent dispatch rules, GitHub disciplines, TBD-V## ticketing, microservice patterns — live in user-global `~/.claude/CLAUDE.md` (auto-loaded by Claude Code in every session).
>
> **OpenOva-platform specifics** — the 5-pillar Definition of Done, the Phase 0 / 1 / 2 deterministic test, domain canon, the anti-pattern catalog, `bp-self-sovereign-cutover`, and `openova-sandbox-mcp` auto-mount — live in `docs/` of this repo, consolidated under the lean doc strategy into 7 canonical documents + 3 subdirs (per user-global `~/.claude/CLAUDE.md` §11). External readers without the user-global file can rely on:
> - [`docs/GLOSSARY.md`](docs/GLOSSARY.md) — terms + banned-terms (single source of truth)
> - [`docs/STATUS.md`](docs/STATUS.md) — what's actually built today vs design
> - [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) — Catalyst architecture + stack + naming + EPICs + bootstrap-kit slots
> - [`docs/DOD.md`](docs/DOD.md) — 5-pillar + Multi-Region DoD + domains canon + personas/journeys
> - [`docs/PRINCIPLES.md`](docs/PRINCIPLES.md) — 15 Inviolable Principles + anti-pattern catalog
> - [`docs/RUNBOOKS.md`](docs/RUNBOOKS.md) — Blueprint authoring + chart authoring + demo/operations/provisioning runbooks
> - [`docs/SECURITY.md`](docs/SECURITY.md) — security posture + threat model

---

# OpenOva (Public Repo) — Codebase Guide for Claude

This is the **public, open-source** OpenOva repository. It hosts the Catalyst platform code and Blueprint catalog.

Proprietary content (website source, deployment configs, infra secrets, the running clusters' manifests) lives in `openova-private`.

---

## Lean documentation strategy

Per founder direction 2026-05-20 + user-global `~/.claude/CLAUDE.md` §11, this repo's docs are consolidated into **7 canonical files + 3 subdirs**:

- **7 canonical docs** (the only source of truth): `GLOSSARY.md`, `STATUS.md`, `ARCHITECTURE.md`, `DOD.md`, `PRINCIPLES.md`, `RUNBOOKS.md`, `SECURITY.md`.
- **`docs/adr/`** — immutable Architecture Decision Records (numbered, additive-only).
- **`docs/ledger/`** — cron-refreshed live state (`TRUST.md`, `TRACKER.md`).
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
6. [`docs/RUNBOOKS.md`](docs/RUNBOOKS.md) — Blueprint authoring, chart authoring, demo / operations / provisioning runbooks.
7. [`docs/SECURITY.md`](docs/SECURITY.md) — security posture + threat model.

Plus subdirs:
- [`docs/adr/`](docs/adr/) — Architecture Decision Records (start at `README.md` index).
- [`docs/ledger/`](docs/ledger/) — `TRUST.md` (per-surface verification ledger) + `TRACKER.md` (open work).
- [`docs/sessions/`](docs/sessions/) — date-stamped walk runbooks and session reports.
- [`docs/archive/`](docs/archive/) — historical / superseded.

These define the model + implementation reality + the rules of engagement. Any contradiction in older docs is to be treated as outdated and updated to match these.

---

## Platform-specific rules (OpenOva-only)

These rules are specific to the OpenOva platform and supplement the
**generic engineering rules** in user-global `~/.claude/CLAUDE.md`.

### Definition of Done — 5-pillar end-user contract

Every dispatch must advance at least one of the 5 inseparable pillars or one
deterministic step in Phase 0 / 1 / 2 of [`docs/DOD.md`](docs/DOD.md):

1. Marketplace + voucher onboarding (Phase 0 + Phase 1 a–c)
2. Multi-region BCP topology choice at signup (Phase 1 b)
3. Two independent CNPG clusters + region-kill failover (Phase 1 b + orthogonal D31)
4. Sandbox + auto-mounted `openova-sandbox-mcp` with full org knowledge (Phase 2 a–e)
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

### Sovereignty cutover — `bp-self-sovereign-cutover`

A franchised Sovereign is tethered to the OpenOva mothership in 8 places (full
list in [`docs/DOD.md`](docs/DOD.md) §Pillar 5 and
[`docs/adr/0002-post-handover-sovereignty-cutover.md`](docs/adr/0002-post-handover-sovereignty-cutover.md)).
`bp-self-sovereign-cutover` installs dormant at bootstrap-kit slot 06a during
Phase 1 and runs eight sequential Jobs post-handover that pivot all 8 tethers.
The final step is a **10-minute deny-egress NetworkPolicy hold** against
`github.com`, `ghcr.io`, and `harbor.openova.io`. `cutoverComplete=true` is set
only if the cluster reconciles green during this hold. No cutover claim
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
Both files are cron-refreshed.

---

## What Catalyst is

OpenOva (the company) builds **Catalyst** (the platform). A deployed Catalyst is called a **Sovereign**. A Sovereign hosts **Organizations**, which contain **Environments**, which run **Applications**, which are installed from **Blueprints**.

`openova` is a Sovereign run by us (formerly Nova). `omantel` is a Sovereign run by Omantel for SMEs. `bankdhofar` is a Sovereign run by the bank for itself. **Same code in every Sovereign.**

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
├── products/               # Composite Blueprint folders OpenOva ships
│   ├── catalyst/           # bp-catalyst-platform umbrella + bp-* sub-charts
│   ├── cortex/             # AI Hub                          (scaffold)
│   ├── axon/               # SaaS LLM Gateway                (real code: chart/ src/ scripts/)
│   ├── fingate/            # Open Banking                    (scaffold)
│   ├── fabric/             # Data & Integration              (scaffold)
│   └── relay/              # Communication                   (scaffold)
└── docs/                   # Canonical platform documentation (lean strategy — see above)
    ├── adr/                # Architecture Decision Records (immutable, numbered)
    ├── ledger/             # TRUST.md + TRACKER.md (cron-refreshed)
    ├── sessions/           # date-stamped walk runbooks + session reports
    ├── archive/            # historical / superseded
    └── proposals/  runbooks/  lessons-learned/   # legacy subdirs; migrating into the 7 canonical docs
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
- Per `~/.claude/CLAUDE.md`: every issue lifecycles through `status/in-progress` → `status/uat` → `status/completed`. Open an issue before code changes; never close it (only the user does).

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

For Catalyst control-plane code (`core/`):

```bash
cd core/
go test ./...
go build ./apps/...
# UI in core/ui/: npm install, npm run dev
```

CRD types live in `core/pkg/apis/`. Add new types here, regenerate clients, then update the controller in `core/internal/`.
