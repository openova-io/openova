# hw292 public-surface TLS + reachability — 2026-08-05 (unauthenticated)
Env: hw292 (dep 1c56518035a83e03), gateway EIP 212.72.24.85. No auth, no cluster access — pure external vantage.

| host | DNS | HTTP | TLS verify | cert expiry |
|---|---|---|---|---|
| console.hw292.omani.works | 212.72.24.85 | 200 | 0 (0=OK) | Nov  1 03:27:25 2026 GMT |
| marketplace.hw292.omani.works | 212.72.24.85 | 200 | 0 (0=OK) | Nov  1 03:27:25 2026 GMT |
| mcp.hw292.omani.works | 212.72.24.74 | 404 | 0 (0=OK) | Nov  1 03:24:59 2026 GMT |
| gitea.hw292.omani.works | 212.72.24.74 | 303 | 0 (0=OK) | Nov  1 03:24:59 2026 GMT |
| harbor.hw292.omani.works | 212.72.24.74 | 301 | 0 (0=OK) | Nov  1 03:27:25 2026 GMT |
| keycloak.hw292.omani.works | 212.72.24.74 | 404 | 0 (0=OK) | Nov  1 03:24:59 2026 GMT |
| grafana.hw292.omani.works | 212.72.24.74 | 302 | 0 (0=OK) | Nov  1 03:27:25 2026 GMT |
| guacamole.hw292.omani.works | 212.72.24.74 | 302 | 0 (0=OK) | Nov  1 03:24:59 2026 GMT |
| newapi.hw292.omani.works | 212.72.24.74 | 200 | 0 (0=OK) | Nov  1 03:27:25 2026 GMT |
| agenity.hw292.omani.works | 212.72.24.74 | 404 | 0 (0=OK) | Nov  1 03:27:25 2026 GMT |
| hubble.hw292.omani.works | 212.72.24.74 | 302 | 0 (0=OK) | Nov  1 03:24:59 2026 GMT |
| openbao.hw292.omani.works | 212.72.24.74 | 404 | 0 (0=OK) | Nov  1 03:24:59 2026 GMT |

## Findings
- **All 12 public front-doors serve valid trusted TLS** (Let's Encrypt, `ssl_verify_result=0`),
  certs healthy to **Nov 1 2026** (~88d). No cert/DNS/reachability fault on the public surface.
- **Two gateway EIPs (console-isolation design):** console + marketplace → `212.72.24.85`;
  every per-app host + mcp → `212.72.24.74`. Matches the dedicated console-EIP/DNS split.
- Bare-`/` 404s on `mcp`/`keycloak`/`agenity`/`openbao` are expected (API/POST-only or
  realm-scoped paths); 301/302/303 on harbor/grafana/guacamole/hubble/gitea are their normal
  redirect-to-login front doors. All TLS-valid + reachable.
- **Scope (honest):** this is the *unauthenticated* public-surface layer only (TLS + DNS +
  reachability + redirect behavior). Functional per-app acceptance (login, content) needs an
  authed session — gated on the walk-auth security verdict (see hardening-breakdown.md §Security gate).
