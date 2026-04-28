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

These four together define the model + implementation reality. Any contradiction in older docs is to be treated as outdated and updated to match these.

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
