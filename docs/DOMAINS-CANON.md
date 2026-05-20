# Domains Canon

**Status:** Authoritative. **Updated:** 2026-05-20.

This document is the **single source of truth** for FQDN patterns used in
Catalyst test provs and tenant Organizations. Every test, walk, agent
dispatch, and provisioning request must use the patterns below.

For the surfaces these domains front, see [`5-PILLAR-DOD.md`](5-PILLAR-DOD.md)
Phase 0 / 1 / 2.
For the multi-region DNS architecture see
[`MULTI-REGION-DNS.md`](MULTI-REGION-DNS.md) and
[`PLATFORM-POWERDNS.md`](PLATFORM-POWERDNS.md).
For the naming primitives (clusters, vClusters, Organizations, Environments,
Applications) see [`NAMING-CONVENTION.md`](NAMING-CONVENTION.md).

---

## Test-Sovereign FQDNs

| Layer | Pattern | Notes |
|---|---|---|
| Sovereign (test) | `t<NN>.omani.works` | `<NN>` increments with every fresh prov (t39, t40, …). |
| Sovereign (test fallback) | `t<NN>.omantel.biz` | Use when `omani.works` hits a Let's Encrypt rate limit. Swap weekly. |
| Sovereign Console | `console.t<NN>.omani.works` | Operator-facing console UI. |
| Marketplace | `marketplace.t<NN>.omani.works` | Customer-facing storefront for the operator-curated catalog. |
| Operator services | `keycloak.t<NN>.omani.works`, `openbao.t<NN>.omani.works`, `openova-flow.t<NN>.omani.works`, `prometheus.t<NN>.omani.works`, `mimir.t<NN>.omani.works`, `loki.t<NN>.omani.works`, `tempo.t<NN>.omani.works`, `argo.t<NN>.omani.works`, `workspaces.t<NN>.omani.works`, `harbor.t<NN>.omani.works`, `registry.t<NN>.omani.works`, `guacamole.t<NN>.omani.works` | Per [`SOVEREIGN-MULTI-REGION-DOD.md`](SOVEREIGN-MULTI-REGION-DOD.md) D25. |

**Voucher operations live in the operator console's BSS menu, NOT in any
`admin.<sovereign-fqdn>` subdomain.** The legacy `admin.*` references in older
docs and agents are outdated.

---

## Tenant-Organization FQDNs

Tenant Organizations receive a free subdomain from an **operator-curated pool**
allocated at signup. The pool population is defined in
`core/services/parent-domain/sovereign_parent_domains.go` (the canonical Go
source — pool TLDs are not hardcoded in tests).

| Pattern | Example | Notes |
|---|---|---|
| `<orgslug>.omani.homes` | `acme.omani.homes` | **Default** — first NS-ready entry in registration order per `core/services/sme/sme_tenant.go:514-521`. |
| `<orgslug>.omani.rest` | `acme.omani.rest` | Pool alternate. |
| `<orgslug>.omani.trade` | `acme.omani.trade` | Pool alternate. **Note: singular `trade`, not `trades`** — earlier docs that said `omani.trades` are wrong. |

The tenant console URL pattern is `console.<orgslug>.<pool-tld>` — e.g.
`console.acme.omani.homes`. Additional tenant-installed apps are reachable
at `<newapp>.<orgslug>.<pool-tld>` — e.g. `notes.acme.omani.homes`.

---

## Voucher redeem URL

The canonical voucher-email link pattern (per
`core/services/notification/templates/templates.go`):

```
https://marketplace.t<NN>.omani.works/redeem/?code=<CODE>
```

**The slash before `?` is mandatory** — both URL ends are part of the
Phase 1 step 1a contract and must be byte-for-byte stable.

---

## Forbidden in tests

The following strings must **never** appear in test code, test data,
operator-walk runbooks, fresh-prov provisioning bodies, or any artifact
that exercises the 5-Pillar deterministic path:

- `openova.io` — and any subdomain (`console.openova.io`, `marketplace.openova.io`, etc.)
- `omantel.openova.io` — legacy operator-sample FQDN, dead
- `eventforge.io` — never an OpenOva domain; never the canonical app name
- `Nova Cloud` — never the operator brand for the test stack

`openova.io` is reserved for the **OpenOva marketing site** (the
`openova-private/website/` repo) and the **mothership control plane** during
Phase 0 + Phase 1 cold-start. After
[`bp-self-sovereign-cutover`](adr/0002-post-handover-sovereignty-cutover.md)
runs, every reference to `openova.io` from a franchised Sovereign is a
Principle #11 violation.

---

## Domain hygiene checks

`docs/trust-audit-*.md` and PR review hunt for:

1. **`openova.io` leaks in test data** — any `*_test.go` / `*.spec.ts` /
   `*.feature` literal containing `openova.io` is a leak.
2. **Hardcoded operator FQDNs** — any code path that pins the operator domain
   to a literal instead of reading it from a runtime parameter
   (`SOVEREIGN_FQDN`, `--sovereign-fqdn`, etc.). See
   [`INVIOLABLE-PRINCIPLES.md`](INVIOLABLE-PRINCIPLES.md) §4 (never hardcode).
3. **Tenant-Org URL pattern drift** — any path that emits
   `<orgslug>.openova.io` or `<orgslug>.<sovereign-fqdn>` instead of
   `<orgslug>.<pool-tld>`. The pool TLD is the source of truth.
4. **`admin.<sovereign-fqdn>` references** — voucher and billing operations
   live in the BSS menu inside the operator console; an `admin.*` subdomain
   means a stale reference.

When in doubt, defer to [`GLOSSARY.md`](GLOSSARY.md) and the Go source files
named above.
