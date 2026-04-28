# Sovereign wizard routing smoke test

Closes [#142](https://github.com/openova-io/openova/issues/142).

Verifies that the chart-rendered routing for `https://console.openova.io/sovereign/`
wires together end-to-end:

1. `GET /sovereign/` returns the wizard SPA shell (200, HTML).
2. SPA fallback works — `GET /sovereign/wizard/credentials` also returns the shell
   so the wizard's React-Router can take over client-side.
3. `POST /sovereign/api/v1/subdomains/check` round-trips through Traefik's
   `strip-sovereign` middleware → catalyst-ui nginx `/api/` reverse-proxy →
   catalyst-api Service (DNS sourced from `values.routing.catalystApi.serviceDNS`,
   never hardcoded — see `docs/INVIOLABLE-PRINCIPLES.md` §4).

## Run modes

### Mock (CI default, no cluster needed)

```bash
cd tests/e2e/sovereign-routing
npm install
npx playwright install chromium
USE_MOCK=1 npm test
```

`USE_MOCK=1` intercepts every network call with canned responses that mirror
the real chart-rendered nginx + catalyst-api behaviour. Fast, deterministic,
and proves the SPA's wiring (basename, `API_BASE`) without depending on a
deployed environment.

### Live cluster (post-Group-C cutover)

```bash
SOVEREIGN_BASE_URL=https://console.openova.io \
SOVEREIGN_BASE_PATH=/sovereign \
  npx playwright test
```

Or against a Sovereign:

```bash
SOVEREIGN_BASE_URL=https://console.omantel.omani.works \
  npx playwright test
```

URLs flow from env vars per the never-hardcode rule. The defaults match the
chart's `values.yaml` (`ingress.host` + `routing.basePath`).
