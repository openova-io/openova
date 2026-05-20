> **Scope of this file**: repository structure, Catalyst terminology, banned-terms, OpenOva-platform-specific rules, and per-component dev workflow specific to this monorepo.
>
> **Generic engineering principles** for active developer sessions — anti-theater discipline, sub-agent dispatch rules, GitHub disciplines, TBD-V## ticketing, microservice patterns — live in user-global `~/.claude/CLAUDE.md` (auto-loaded by Claude Code in every session).
>
> **OpenOva-platform specifics** — the 5-pillar Definition of Done, the Phase 0 / 1 / 2 deterministic test, domain canon, the anti-pattern catalog, `bp-self-sovereign-cutover`, and `openova-sandbox-mcp` auto-mount — live in `docs/` of this repo. External readers without the user-global file can rely on:
> - [`docs/5-PILLAR-DOD.md`](docs/5-PILLAR-DOD.md) for the end-user Definition of Done + Phase 0/1/2 deterministic test
> - [`docs/DOMAINS-CANON.md`](docs/DOMAINS-CANON.md) for Sovereign and tenant-Org FQDN patterns and forbidden test strings
> - [`docs/ANTI-PATTERN-CATALOG.md`](docs/ANTI-PATTERN-CATALOG.md) for the OpenOva-specific theater receipts surfaced during PR review
> - [`docs/INVIOLABLE-PRINCIPLES.md`](docs/INVIOLABLE-PRINCIPLES.md) for the engineering principles
> - [`docs/IMPLEMENTATION-STATUS.md`](docs/IMPLEMENTATION-STATUS.md) for "what's actually built today"
> - [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) for the design model

---

# OpenOva (Public Repo) — Codebase Guide for Claude

This is the **public, open-source** OpenOva repository. It hosts the Catalyst platform code and Blueprint catalog.

Proprietary content (website source, deployment configs, infra secrets, the running clusters' manifests) lives in `openova-private`.

---

## Read these before doing anything

In order:

1. [`docs/GLOSSARY.md`](docs/GLOSSARY.md) — terminology source of truth. Wins over any other doc.
2. [`docs/IMPLEMENTATION-STATUS.md`](docs/IMPLEMENTATION-STATUS.md) — what's built today vs what's design. Read before claiming any feature exists.
3. [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) — Catalyst target architecture.
4. [`docs/NAMING-CONVENTION.md`](docs/NAMING-CONVENTION.md) — naming patterns.
5. [`docs/5-PILLAR-DOD.md`](docs/5-PILLAR-DOD.md) — the end-user Definition of Done. Every dispatch must move at least one pillar.
6. [`docs/DOMAINS-CANON.md`](docs/DOMAINS-CANON.md) — Sovereign / tenant-Org FQDN patterns and forbidden test strings.
7. [`docs/ANTI-PATTERN-CATALOG.md`](docs/ANTI-PATTERN-CATALOG.md) — OpenOva-specific theater receipts. Scan diffs for these shapes during PR review.
8. [`docs/INVIOLABLE-PRINCIPLES.md`](docs/INVIOLABLE-PRINCIPLES.md) — the 15 inviolable engineering principles.

These define the model + implementation reality + the rules of engagement. Any contradiction in older docs is to be treated as outdated and updated to match these.

---

## Platform-specific rules (OpenOva-only)

These rules are specific to the OpenOva platform and supplement the
**generic engineering rules** in user-global `~/.claude/CLAUDE.md`.

### Definition of Done — 5-pillar end-user contract

Every dispatch must advance at least one of the 5 inseparable pillars or one
deterministic step in Phase 0 / 1 / 2 of [`docs/5-PILLAR-DOD.md`](docs/5-PILLAR-DOD.md):

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
[`docs/DOMAINS-CANON.md`](docs/DOMAINS-CANON.md):

- Test Sovereign: `t<NN>.omani.works` (or `t<NN>.omantel.biz` if LE-rate-limited)
- Tenant Organization: `<orgslug>.omani.homes` (default), `omani.rest`, or `omani.trade`
- Voucher redeem URL: `https://marketplace.t<NN>.omani.works/redeem/?code=<CODE>`

**Forbidden in tests:** `openova.io`, `omantel.openova.io`, `Nova Cloud`, `eventforge.io`.
The legacy `admin.<sovereign-fqdn>` subdomain for voucher operations is dead —
voucher and billing operations live in the operator console's **BSS menu**.

### Anti-theater discipline during PR review

Per [`docs/ANTI-PATTERN-CATALOG.md`](docs/ANTI-PATTERN-CATALOG.md), defensive-coding
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
list in [`docs/5-PILLAR-DOD.md`](docs/5-PILLAR-DOD.md) §Pillar 5 and
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

### Verification ledger — `docs/TRUST.md`

Every claimed-done surface lives in [`docs/TRUST.md`](docs/TRUST.md) in one of
four states: 🔴 UNVERIFIED (default) · 🟢 VERIFIED-PASS · ⛔ VERIFIED-FAIL · 🟡
VERIFIED-PARTIAL. Every PR against a surface flips it back to UNVERIFIED
until re-walked. Verification agents are READ-ONLY — they may not ship PRs to
make their own walks pass.

---

## What Catalyst is

OpenOva (the company) builds **Catalyst** (the platform). A deployed Catalyst is called a **Sovereign**. A Sovereign hosts **Organizations**, which contain **Environments**, which run **Applications**, which are installed from **Blueprints**.

`openova` is a Sovereign run by us (formerly Nova). `omantel` is a Sovereign run by Omantel for SMEs. `bankdhofar` is a Sovereign run by the bank for itself. **Same code in every Sovereign.**

---

## Repo structure

```
openova/
├── core/                   # Catalyst control-plane application (Go)
│   ├── apps/               # target: console/, projector/, environment-controller/, etc.
│   │                       # current: empty .gitkeep + legacy bootstrap/ manager/ placeholders
│   │                       # See core/README.md for the target tree.
│   ├── internal/           # domain, application, adapters, events (placeholder)
│   ├── pkg/apis/           # CRD types: Sovereign, Organization, Environment,
│   │                       # Application, Blueprint, EnvironmentPolicy, SecretPolicy,
│   │                       # Runbook (placeholder; design contract in BLUEPRINT-AUTHORING)
│   ├── ui/                 # frontend (Astro + Svelte) — placeholder
│   └── deploy/             # K8s manifests per control-plane component (placeholder)
├── platform/               # Component Blueprint folders — one folder per upstream OSS project
│   ├── cilium/  cnpg/  flux/  gitea/  keycloak/  openbao/  ...
│   └── ...                 # 56 folders total, each currently README-only
├── products/               # Composite Blueprint folders OpenOva ships
│   ├── catalyst/           # Target: bp-catalyst-platform umbrella (currently only bootstrap/ui scaffold)
│   ├── cortex/             # AI Hub                          (README only)
│   ├── axon/               # SaaS LLM Gateway                (real code: chart/ src/ scripts/)
│   ├── fingate/            # Open Banking                    (README only)
│   ├── fabric/             # Data & Integration              (README only)
│   └── relay/              # Communication                   (README only)
└── docs/                   # Canonical platform documentation
```

Each subfolder of `platform/` and `products/` is the **source of one Blueprint** in this monorepo (canonical layout). CI fans out to per-Blueprint OCI artifacts at `ghcr.io/openova-io/bp-<name>:<semver>` — that's where per-Blueprint isolation lives. There are no separate per-Blueprint Git repositories.

---

## Naming conventions in this repo

- Cluster: `{prov}-{reg}-{bb}-{env_type}` — e.g. `hz-fsn-rtz-prod`
- vcluster: `{org}` (within a cluster) — e.g. `acme`
- Catalyst Environment: `{org}-{env_type}` — e.g. `acme-prod`
- Blueprint: `bp-<name>` — e.g. `bp-wordpress`
- Application: `<purpose>` (within an Environment) — e.g. `marketing-site`

Full table in [`docs/NAMING-CONVENTION.md`](docs/NAMING-CONVENTION.md).

---

## Banned terms

Do not use in any new doc, code, comment, commit message, or UI string:

- "tenant" (as platform terminology) → `Organization`
- "operator" (as a person/entity) → `sovereign-admin` (the role). K8s Operators (controller pattern) are still called Operators.
- "client" (in product UX sense) → `User`. OIDC client and K8s client are fine.
- "module" / "template" (in Catalyst sense) → `Blueprint`. Go modules, Terraform modules, K8s templates, prompt templates etc. are external technologies and are fine.
- "Backstage" → `Catalyst console`. Backstage was decided removed.
- "Synapse" (as the OpenOva product) → `Axon`. Matrix's Synapse server is fine when context is the chat server.
- "Lifecycle Manager" / "Bootstrap wizard" (as separate products) → `Catalyst`.
- "Workspace" (as Catalyst scope OR component name) → `Environment` / `environment-controller`. The controller previously named `workspace-controller` is now `environment-controller`.
- "Instance" (as user-facing object) → `Application`. CRD remains an internal name.

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
