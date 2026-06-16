# 3654 — The engine is standard: every capability Blueprint-declared, zero per-app code — UAT walk

> **Ticket:** [#3654](https://github.com/openova-io/openova/issues/3654) · **Car:** T2 · **PR:** #3655 · **Train:** `train/hw150`
>
> **What this proves:** the "operator produces instances" and "SSO admin bootstrap" capabilities are
> **declared in the Blueprint** and the engine acts on the declaration identically for every app — a
> NON-postgres operator gets the same treatment by declaring `producesInstances`, with **zero** postgres-
> specific code. "All the concepts are applicable for all" (founder #4).
>
> **Format law:** UI rows + a code-grep generality proof (the only honest way to prove "no per-app code").
> Replace `<fqdn>`/`<JWT>`. Tick **☑**/**☒**.

**Sign-in (once).** `https://console.<fqdn>/auth/handover?token=<JWT>` → signed in, no login form.

## Section 1 — A NON-postgres operator gets the engine treatment, by declaration

| Step | Go to (URL) | Do | Expect | ☐ |
|---|---|---|---|---|
| 1.1 | `/catalog/bp-valkey` (or another operator declaring `producesInstances`) | open the class page | it shows the **engine-card / "+ New instance"** treatment — the same as postgres | ☐ |
| 1.2 | (same) | **+ New instance** → name → Create | an instance is produced via the generic `ProducerRegistry` path — no postgres-specific branch | ☐ |

## Section 2 — The apps that had the capability still work (behaviour preserved)

| Step | Go to | Do | Expect | ☐ |
|---|---|---|---|---|
| 2.1 | `/catalog/bp-postgres` | + New instance | still produces instances (the generalization preserved it) | ☐ |
| 2.2 | a `bp-newapi` instance | open it after install | its first admin is seeded via the **standard** `sso.bootstrap` path (not a newapi-specific Job) | ☐ |

## Section 3 — Generality proof: ZERO app-name in the generalized path

| Step | Action (operator / reviewer) | Do | Expect | ☐ |
|---|---|---|---|---|
| 3.1 | repo | `git grep -nE '== "postgres"' core/controllers/pkg/backingservice` | **0 matches** on the produces-instances path | ☐ |
| 3.2 | repo | `git grep -n 'newapi' platform/newapi/chart/templates/admin-sso-seed-job.yaml` | no app-name **equality branch** — values resolve from `.Values.sso` | ☐ |
| 3.3 | the new operator's `blueprint.yaml` | read it | the ONLY thing added vs postgres is the `producesInstances` declaration — `git diff` shows no engine code per-app | ☐ |

## Appendix — automated (NOT acceptance)
- `go test ./core/controllers/pkg/backingservice` green (postgres preserved; redis/mongodb fail loudly via the registry).
- `helm template` bp-cnpg + bp-newapi clean; standard `sso.adminGroup`/`bootstrap` drive the rendered seed, legacy fallback works.
