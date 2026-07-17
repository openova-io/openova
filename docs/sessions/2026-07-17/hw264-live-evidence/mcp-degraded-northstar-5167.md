# hw264 — bp-openova-mcp DEGRADED on fresh prov (north-star defect #5167)

Live probe 2026-07-17 (~10:00Z), hw264 dep 4585c8a9f92d4e8e:

- Pod `catalyst-system/openova-mcp-bp-openova-mcp` 1/1 Running (chart 0.1.2, HR Ready).
- Startup log: `WARNING: verify=rs256 but OPENOVA_MCP_RS256_PUBKEY_PEM is empty/absent — starting in DEGRADED mode … tools/call is rejected 401`.
- Live secret present: `catalyst-system/catalyst-handover-jwt-public`, key `public.jwk`,
  399 bytes, content head `{"alg":"RS256","e":"AQAB","kty":"RSA"…` — an RSA JWK.
- Deployed env wiring (from the chart defaults): secretKeyRef
  `catalyst-handover-jwt` / `handover-jwt-public.pem`, `optional: true` → the
  referenced secret NEVER exists on a Sovereign → env silently empty.

Root cause: name/key mismatch (chart → never-created PEM secret; platform seeds the
JWK mirror) + format mismatch (binary rejected JWKs). Both fixed root-cause in
PR #5168 (merged): resolver parses RSA JWKs natively (PEM kept), chart values target
the seeded mirror; bp-openova-mcp 0.1.3 / image 0.2.3; #5114 degrade-never-crash
posture unchanged (unparseable table-tested).

Validates on the next fresh prov: MCP log shows RS256 ACTIVE; console-minted owner
session resolves; tools/call RBAC-gates (Agenity→MCP create_application, Pillar 4).
