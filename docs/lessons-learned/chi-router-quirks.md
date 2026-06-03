> NOTE (2026-06-03): pending migration into docs/RUNBOOKS.md §7 (troubleshooting) per lean-doc strategy.

# `chi` router quirks

Operational knowledge about Go's [`go-chi/chi`](https://github.com/go-chi/chi) HTTP router, used as the catalyst-api router. These are non-obvious behaviours that bite anyone authoring routes that accept structured path parameters.

## chi does NOT decode percent-encoded path segments before route matching

When a path parameter can contain RFC 3986 path-safe special characters (`:` `@` `&` `=` `+` `$` `,` `;`), chi treats the percent-encoded form as part of the literal path — it does **not** decode `%3A` back to `:` before checking which route to dispatch.

Concretely, given the route definition:

```go
r.Get("/api/v1/deployments/{depId}/jobs/{jobId}", h.GetJob)
```

and the canonical `jobId` of `"<deploymentId>:<jobName>"` (which legitimately contains `:`):

| URL the client sent | What chi sees | Outcome |
|---|---|---|
| `.../jobs/abc123:install-foo` (raw colon) | `{jobId} = "abc123:install-foo"` | **200 OK** ✓ |
| `.../jobs/abc123%3Ainstall-foo` (encoded) | path doesn't match the route literal | **404 Not Found** ✗ |

This is documented chi behaviour but easy to miss because most routers (Express, Gin's defaults, ASP.NET) DO decode before matching, so a frontend that uses `encodeURIComponent` on every path parameter will round-trip cleanly with those — and silently 404 on chi.

JavaScript clients using `encodeURIComponent` on each path segment will produce the encoded form and 404 every fetch. The breakage is invisible in TypeScript unit tests that stub fetch (no real router in the loop).

**Rule**: When a chi-routed path parameter can legitimately contain RFC 3986 path-safe specials, insert it **raw** into the URL template on the client side, not via `encodeURIComponent`. Reserve encoding for true reserved characters (`?`, `#`) that would change URL parsing. For hex IDs, slugs, or other URL-safe strings the encode is a no-op anyway and provides no value.

Frontend pattern that works against catalyst-api:

```ts
// jobId is "<deploymentId>:<jobName>" — the colon must survive verbatim.
const url =
  `${API_BASE}/v1/deployments/${encodeURIComponent(deploymentId)}` +
  `/jobs/${jobId}`;
```

Defensively encode only segments that are known-safe (deploymentId is a 16-byte hex, so `encodeURIComponent` is a no-op there but provides insurance against future ID-shape changes). Add a regression test that asserts the request URL contains the raw `:` and rejects `%3A` — chi route mismatches don't surface in component-level tests, only at the integration boundary.

**Ref**: #305
