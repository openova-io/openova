# `docs/sessions/templates/` — pre-built session-doc templates

Templates that produce date-stamped session reports under `docs/sessions/`. Each template encodes a structured walk / audit / runbook that has been deemed worth standardizing.

## Index

| Template | Use when | Output path pattern |
|---|---|---|
| [`G117.0-codification-audit-template.md`](G117.0-codification-audit-template.md) | At end of any large EPIC cycle (G91/G112/G113/G117/...) to audit hw86 runtime drift, codify gaps, and swap to clean `:main` images | `docs/sessions/<YYYY-MM-DD>-G117.0-<sov>-codification-audit.md` |

## Adding a new template

1. Write it under this directory. Use a self-describing filename.
2. Add a row to the Index table above.
3. Cite the canonical doc + memory pin that motivated the template.
4. Templates ARE NOT immutable — when a new audit class surfaces a missing section, update the template and bump its version in §1 / §Template version.
