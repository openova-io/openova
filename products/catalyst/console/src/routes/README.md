# products/catalyst/console — G117 route scaffolding

> Created by Wave-0 G117 pre-flight. These files are **mocked-data scaffolds** the Wave-1 console authors (G117.2 + G117.3 + G117.4) fill in.

## Why `+page.svelte` and not Astro?

The G117 architectural plan picks SvelteKit-style file-based routing for new console surfaces because:

1. Per-route data-fetching with type-safe `load()` is cleaner than Astro's per-page `<script>` block when each page calls 2-4 catalyst-api endpoints.
2. Slot-component nesting (drill-down → Application → Endpoints tab) maps naturally to nested `+page.svelte` files.
3. The Wave-1 plan migrates the existing Astro pages incrementally — these new G117 surfaces ship under the new convention to avoid mixed migration.

If the Wave-1 authors decide to keep Astro, the same Svelte components can be wrapped in `*.astro` shells under `src/pages/` — the mock fixtures + API contracts here are framework-agnostic.

## Route table

| Path | File | Backed by API |
|---|---|---|
| `/catalog/:name` | `catalog/[name]/+page.svelte` | `GET /catalog/{blueprint}` + `GET /catalog/{blueprint}/instances` |
| `/catalog/:name/new` | `catalog/[name]/new/+page.svelte` | `POST /apps/instances` |
| `/apps/:id` | `apps/[id]/+page.svelte` | `GET /apps/{id}` + `GET /apps/{id}/launch-url` |
| `/apps/:id/endpoints` | `apps/[id]/endpoints/+page.svelte` | `GET/POST/PATCH/DELETE /apps/{id}/endpoints[/{name}]` |

## Mock fixtures

`tests/e2e/fixtures/mock-blueprints.yaml` — same shape as the OpenAPI `CatalogBlueprint`. The dev server reads this when the `MOCK_API=1` env var is set, so a Wave-1 author can run `MOCK_API=1 npm run dev` and see the surfaces render with no live catalyst-api needed.
