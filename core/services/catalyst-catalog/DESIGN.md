# catalyst-catalog — EPIC-2 Slice L design notes

EPIC-2 Slice L of #1097. Multi-source Blueprint catalog HTTP REST service
that REPLACES the legacy per-Org catalog (different scope) per ADR-0001 §4.3.

## Module layout (decision)

`core/services/catalyst-catalog/` lives in its OWN Go module —
`github.com/openova-io/openova/core/services/catalyst-catalog`.

Group `core/controllers/` is for **CRD reconcilers**; group
`core/services/` is for **HTTP services**. Catalyst-catalog is a service,
not a controller, so it doesn't share the controllers module. Co-located
Organization services (auth/billing/notification/etc.) each live in their own
`core/services/<name>/` modules — this slice follows that pattern.

We ship under the name `catalyst-catalog/` (rather than `catalog/`) to
disambiguate from the existing `core/services/catalog/` per-Org service. Its
retirement is slice L3, deferred per the EPIC-2 master brief.

## Importing the unified Gitea client

The unified Gitea client (CC2 #1136) is the only sanctioned Gitea HTTP
surface. Catalyst-catalog reuses it via:

1. Promotion: `core/controllers/internal/gitea` →
   `core/controllers/pkg/gitea`. Go's `internal/` packaging rule blocks
   imports from outside the parent sub-tree, which would forbid catalog-
   svc (a sibling Go module) from importing it. Promotion to `pkg/`
   signals "shared library contract" and unblocks cross-module use.
   The 5 Group C controllers (organization, environment, blueprint,
   application, useraccess) were updated atomically in this slice's PR.
2. `replace` directive in `catalyst-catalog/go.mod`:
   `replace github.com/openova-io/openova/core/controllers => ../../controllers`.
   This ties the catalog build to the in-tree version of the Gitea
   client without publishing a versioned tag.

The promotion is documented in `core/controllers/pkg/gitea/DESIGN.md` §6.

This slice ALSO extended the unified Gitea client with two new methods
needed for catalog enumeration (added at the canonical seam, NOT as
catalog-private helpers):

- `ListOrgRepos(ctx, org) ([]Repo, error)` — paginated repo listing
  under an Org. Used to enumerate Blueprint repos in `catalog` and
  `catalog-sovereign` Orgs.
- `ListContents(ctx, org, repo, branch, path) ([]ContentEntry, error)`
  — directory listing. Used to enumerate per-Blueprint dirs in
  `<org>/shared-blueprints`.

## Three sources, priority on collision

| Origin | Repo layout | Visibility |
|---|---|---|
| Public (`catalog` Org) | `catalog/<bp-name>` repo, `blueprint.yaml` at root | Always |
| Sovereign (`catalog-sovereign` Org) | same shape | Always |
| Per-Org private (`<org>/shared-blueprints` repo) | one dir per Blueprint, each with `blueprint.yaml` | Only callers in `<org>` |

**Resolution order**: PRIVATE > SOVEREIGN > PUBLIC. An Org's private
copy of a Blueprint name overrides the sovereign-curated and public
versions. This matches `docs/ARCHITECTURE.md` §5.4.

## Auth model

Catalyst-catalog is intended to run **behind catalyst-api** (Cilium
Gateway HTTPRoute proxy). Catalyst-api validates the Keycloak JWT
against JWKS and forwards the request. Catalog-svc parses the JWT
payload (3 base64url segments + `exp` check) for `Claims.Groups[]` /
`Claims.Org` to compute the caller's visible Orgs.

When `CATALOG_ANONYMOUS_READS=true`, anonymous callers may list public +
sovereign-curated Blueprints (no per-Org private). Default: false.

A future hardening slice can add an optional `CATALOG_JWKS_URL` env-var
to do in-process JWKS validation (e.g. when deployed without the proxy).

## Cache

In-memory LRU + per-entry TTL on every blueprint.yaml read. Default TTL
30s, capacity 1024 entries. Cache key is
`<origin>|<org>|<name>|<version>` — every (source, name) pair is
independent. Invalidation is TTL-only; Gitea-side changes propagate
within at most TTL seconds. This is a deliberate trade-off: a Blueprint
publishing event in Gitea has no webhook to push-invalidate the cache,
and a 30s lag on the catalog is acceptable.

## REST endpoints

```
GET /healthz
GET /api/v1/catalog?org=<slug>
GET /api/v1/catalog/{name}
GET /api/v1/catalog/{name}/versions
GET /api/v1/catalog/{name}/versions/{version}
```

JSON shape (consumed by EPIC-2 Slice I `useCatalog()` hook):

```json
{
  "items": [
    {
      "name": "bp-wordpress",
      "version": "5.6.1",
      "visibility": "listed",
      "card": {
        "title": "WordPress",
        "summary": "...",
        "icon": "wordpress",
        "category": "cms",
        "tags": ["php", "mysql"]
      },
      "placementSchema": {
        "modes": ["single-region", "active-active", "active-hotstandby"],
        "default": "single-region"
      },
      "upgradeFrom": ["5.5.0"],
      "origin": 1,
      "source": "public",
      "org": ""
    }
  ]
}
```

`origin` is an integer enum (1=public, 2=sovereign, 3=org-private);
`source` is the human-readable form. `org` is populated only for
org-private entries.

The `GET /api/v1/catalog/{name}` and `GET /api/v1/catalog/{name}/versions/{version}`
responses include the `raw` field — the full parsed Blueprint manifest
(map[string]interface{}) — so the install flow can render the
`configSchema` form without a second round-trip to Gitea.

## GraphQL — DEFERRED

Per the EPIC-2 master brief, GraphQL ("use gqlgen if not present; fall
back to plain REST if gqlgen pull is heavy") is a deferred follow-up.
catalyst-catalog ships REST-only in this slice. Adding gqlgen would pull
~80MB of indirect deps for a feature no UI consumer has yet asked for.

When the install flow needs nested-fetch shape (e.g. "give me Blueprint
+ all upgrade-target versions in one request") we'll either add a tiny
in-process GraphQL surface (gqlgen) or compose REST calls in the UI's
TanStack Query layer. Decision deferred until a real consumer arrives.

## Per-Org catalog retirement — DEFERRED

Per the EPIC-2 master brief, slice L3 (migrating the per-Org catalog
service's existing routes to catalyst-catalog or documenting them as
retired) is deferred to a follow-up slice. The per-Org `core/services/catalog/`
service continues to operate on its current scope (per-Org, MongoDB-backed)
until that slice lands. **DO NOT remove or modify per-Org catalog code in
the L1+L2 PR.**
