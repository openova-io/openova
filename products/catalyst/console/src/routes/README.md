# products/catalyst/console — G117 route scaffolding + Astro bridge

> Wave-0 G117 pre-flight created these `+page.svelte` files as mocked-data
> scaffolds. **Wave-1 (G117.2 + G117.3 + G117.4) bridged them to live
> catalyst-api calls + added the Astro shell** described below.

## Why two file conventions (`+page.svelte` and `*.astro`)?

The G117 architectural plan keeps the SvelteKit-style file names
(`routes/<path>/+page.svelte`) for the Svelte components themselves so a
future migration to SvelteKit proper requires no rename. Each Svelte page
is wrapped in a thin Astro shell under `src/pages/` that:

1. Resolves dynamic route params (`Astro.params.name`, `Astro.params.id`)
2. Applies the global `Layout.astro` (head + fonts + mock-API early boot)
3. Wraps the Svelte component in `<PortalShell>` so every G117 surface
   inherits the same header, breadcrumbs, and design tokens
4. Hydrates the Svelte component with `client:load` so the in-page
   reactivity (filters, dialogs, tabbed views) is interactive on first
   paint

This is the bridge the W0 README anticipated: the Svelte components stay
framework-agnostic (read route params from `window.location.pathname` as
a fallback when no prop is passed), and the Astro shells provide the
build-time prerender + route-param plumbing.

## Route + shell map

| URL                          | Svelte route component                            | Astro shell                                      |
| ---------------------------- | ------------------------------------------------- | ------------------------------------------------ |
| `/`                          | _(none — landing page is direct Astro)_           | `src/pages/index.astro`                          |
| `/catalog/{name}`            | `src/routes/catalog/[name]/+page.svelte`          | `src/pages/catalog/[name]/index.astro`           |
| `/catalog/{name}/new`        | `src/routes/catalog/[name]/new/+page.svelte`      | `src/pages/catalog/[name]/new.astro`             |
| `/apps/{id}`                 | `src/routes/apps/[id]/+page.svelte`               | `src/pages/apps/[id]/index.astro`                |
| `/apps/{id}/endpoints`       | `src/routes/apps/[id]/endpoints/+page.svelte`     | `src/pages/apps/[id]/endpoints.astro`            |

## OpenAPI bindings

Each Svelte route reads/writes via `src/lib/api/catalystApi.ts` — a thin
client mirroring `docs/api/catalyst-api-openapi.yaml` (single source of
truth). The client transparently shortcuts to the in-browser mock backend
(`src/lib/api/mock.ts`) when `window.__MOCK_API__` is populated, which the
mock-bootstrap script in `Layout.astro` does whenever
`PUBLIC_MOCK_API=1`.

| Route                    | catalyst-api endpoints                                                                                  |
| ------------------------ | ------------------------------------------------------------------------------------------------------- |
| `/catalog/{name}`        | `GET /catalog/{blueprint}`, `GET /catalog/{blueprint}/instances`                                        |
| `/catalog/{name}/new`    | `GET /catalog/{blueprint}`, `POST /apps/instances`                                                      |
| `/apps/{id}`             | `GET /apps/{id}`, `GET /apps/{id}/launch-url`                                                           |
| `/apps/{id}/endpoints`   | `GET/POST /apps/{id}/endpoints`, `PATCH/DELETE /apps/{id}/endpoints/{name}`, `GET /apps/{id}/launch-url`|

## Shared components

Living under `src/components/`, inherited by every route per
`feedback_subagents_inherit_design_system.md`:

- **`PortalShell.svelte`** — Header + breadcrumbs + page-title wrapper
- **`LaunchButton.svelte`** — Silent-SSO launcher (G117.4)
- **`TopologyPicker.svelte`** — Radio over blueprint.supportedTopologies (G117.2)
- **`EndpointsTab.svelte`** — Per-Application endpoint CRUD UI (G117.3)

## Running locally

```bash
cd products/catalyst/console
npm install
MOCK_API=1 npm run dev      # mock backend, no live cluster needed
npm run build               # static export (verifies the Astro shells)
MOCK_API=1 npm run test:e2e # Playwright suite
```

## Mock fixtures

`tests/e2e/fixtures/mock-blueprints.yaml` — same shape as the OpenAPI
`CatalogBlueprint`. The build step (`scripts/build-mock-fixture.mjs`)
converts this to `public/mock-fixture.json` so the in-browser bootstrap
can fetch it without a YAML parser at runtime.

The Wave-1 Playwright specs read this fixture by booting the dev server
with `MOCK_API=1`. Wave-2 closeout reruns the same specs against a live
hw86 catalyst-api (no fixture, no MOCK_API) for the operator-walk DoD.

## Live-deployment routing

For static-prerender builds (`output: 'static'`), the Astro shells use
`getStaticPaths()` to enumerate the seeded Blueprint names + Application
IDs from the mock fixture. Live deployments serve via nginx with an SPA
fallback: any path under `/catalog/*` or `/apps/*` that misses static
rewrites to the matching `index.html`, and the Svelte component reads
the real param from `window.location.pathname`.

When the console moves to SSR (next-Wave deliverable), `getStaticPaths`
becomes unnecessary and dynamic params resolve server-side.
