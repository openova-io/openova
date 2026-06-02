# ADR-0009 — Per-Organization IaC repo bootstrap on Org creation

> Status: **Accepted** (G117 Wave-0 pre-flight, 2026-06-02)
>
> ADR numbering preserves slots 0005-0008 for in-flight EPICs (G92/G105/G108/G112 candidates) — Wave-0 picks 0009 deliberately so this ADR can land without ordering coupling. Wave-1 may renumber if the in-flight ADRs collapse out of scope; renumbering this one is a pure-rename refactor.

## Context

G117 (Application Lifecycle Phase 2) needs every Organization to own a Git repo at `gitea.<sov>/<org>/iac` that stores the Org's declarative state:

- `apps/` — one folder per Application instance, holding the per-instance overrides + endpoint manifests
- `envs/` — Environment definitions (`<org>-prod`, `<org>-staging`, etc.)
- `policies/` — Org-local Kyverno / Cilium / NetworkPolicy overrides
- `kustomization.yaml` — Flux's entry point

The Endpoints tab (G117.3) and the new-instance dialog (G117.2) both **write through this repo via PR**, not directly through catalyst-api. The pipeline is:

```
console UI ─POST→ catalyst-api ─PR→ gitea.<sov>/<org>/iac ─CI→ Kyverno+cert-mgr+DNS-conflict→ auto-merge ─Flux→ cluster
```

The PR boundary is intentional: it gives the Sovereign-admin an auditable trail, enables manual review when policy gates fail, and means the cluster state is **always** reconstructible from Git.

## Decision

On Organization create (organization-controller reconcile), the controller calls a new catalyst-api endpoint `POST /catalyst/v1/orgs/{org}/iac-bootstrap` which:

1. Creates the Gitea Org `<org>` (idempotent; Gitea returns 422 if already exists).
2. Creates the repo `<org>/iac` with the default tree (below).
3. Creates a robot user `<org>-iac-bot` scoped to that single repo (`write:repository`, no Org-wide push).
4. Enables branch protection on `main` requiring three named status checks:
   - `kyverno-admission`
   - `cert-manager-precheck`
   - `dns-conflict-precheck`
5. Installs a `bp-flux-org-subscription` HelmRelease into the management vCluster pointing at this repo's `kustomization.yaml`.

### Default tree

```
/
├── README.md                ← auto-generated; explains the PR-pipeline contract
├── kustomization.yaml       ← lists apps/ + envs/ + policies/ as resources
├── apps/
│   └── .gitkeep
├── envs/
│   └── .gitkeep
└── policies/
    └── .gitkeep
```

### Robot account scope

The robot user `<org>-iac-bot`:
- Is a **per-Org**, not Sovereign-wide, account.
- Has `write` on `<org>/iac` only — explicitly NOT `repo:admin`, NOT `org:admin`.
- Cannot push to `<org>/iac/main` directly (branch protection forces a PR).
- Token is stored as a Kubernetes Secret in the org's namespace (`<org>-iac-bot-token`).
- catalyst-api reads it via External-Secrets from OpenBao when opening PRs.

### Branch protection wired to the three pre-checks

The PR-pipeline gate (decision #4) requires three named status checks before merge. catalyst-api configures them via Gitea's `/repos/{org}/{repo}/branch_protections` API. The three precheck names are LOCKED — any Wave-1 author touching the pipeline must keep them in sync:

| Check name | Provided by | Fails when |
|---|---|---|
| `kyverno-admission` | per-Org Kyverno CI workflow | Manifest violates a ClusterPolicy |
| `cert-manager-precheck` | catalyst-api at PR open time | Hostname conflicts with an existing Certificate |
| `dns-conflict-precheck` | catalyst-api at PR open time | Hostname already pinned to a different Service in PowerDNS |

### Flux subscription

A single HelmRelease named `flux-org-<org>-subscription` lives in the management vCluster's `<org>` namespace. Its values:

```yaml
gitRepository:
  url: https://gitea.<sov>/<org>/iac.git
  ref: { branch: main }
  secretRef:
    name: <org>-iac-bot-token
kustomization:
  path: ./
  prune: true
  interval: 1m
```

## Alternatives considered

1. **No per-Org repo; everything in the public openova/openova repo** — rejected: tenant Apps would mix with platform code; auditability collapses; tenants can't be granted PR rights.
2. **Per-Org repo, but on the mothership Gitea (not the Sovereign's local Gitea)** — rejected: post-cutover (ADR-0002) the Sovereign must run independent of any mothership; this would re-introduce a mothership tether.
3. **No PR pipeline; direct API writes** — rejected: defeats the auditability + policy-gate goals; admins can't see what changed before it landed.

## Consequences

- Adds one HelmRelease per Organization to the mgmt cluster (~minor footprint).
- A failing Kyverno policy now blocks the PR — operators must fix the policy or fix the PR; either way the cluster state remains in last-known-good.
- catalyst-api owns the robot-account secrets; rotation must be wired through External-Secrets just like other Sovereign-scoped secrets (G91/G112/G113 pattern).
- Wave-1 (G117.3) authors must write the three precheck implementations as named Gitea Actions workflows; without them, the branch protection's status-check requirement traps every PR.

## Executable

A bash script at `tools/bootstrap-org-iac-repo.sh` performs steps 1-4 standalone (without going through catalyst-api). Idempotent. Useful for:

- Bootstrapping the very first Org on a fresh Sovereign before catalyst-api is fully reconciled
- Manual recovery if catalyst-api's bootstrap call failed mid-way
- Integration tests (Wave-1 author can call it directly from the Playwright fixture setup)

Signature: `tools/bootstrap-org-iac-repo.sh --org <slug> --sov-fqdn <fqdn>` (GITEA_TOKEN from env).
