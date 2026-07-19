# bp-stalwart-tenant

Per-Org (per-vcluster) **dedicated** Stalwart mail server. Implements locked decision **[Q3]** of [EPIC #795](https://github.com/openova-io/openova/issues/795) — every Organization on a Sovereign gets its **own** Stalwart in its tenant namespace, with its **own** domain, **own** MTA reputation, and **own** queue.

**Status:** v0.1.0 (Application Blueprint, scratch chart) | **Updated:** 2026-05-04 (#801)

> NOT the same as the otech-shared `openova.io` Stalwart in `openova-private/clusters/contabo-mkt/apps/stalwart/` — that is the OpenOva-corp mail server. This Blueprint is the **per-Org** mail server that ships inside each Org vcluster.

---

## Why per-tenant (and the trade-off)

Locked in #795: founder explicitly chose this over a shared otech-level multi-domain Stalwart. The trade buys:

- **Stronger isolation** — one Organization's deliverability problem doesn't affect another Organization's MTA reputation.
- **Per-customer DKIM** — each Organization signs with their own key on their own domain.
- **Per-customer queue** — bounce-floods, blocklist hits, rate-limit pushes from one Organization stay in their queue.

Cost: **mail-server resources multiply by N tenants**. Each install = 1 small StatefulSet (100m / 256Mi requests) + 1 PVC (default 20Gi). #795 trade-off table tracks this.

---

## What ships

| Resource | Purpose |
|---|---|
| `StatefulSet` | Stalwart mail server pod, single replica, RocksDB on PVC |
| `Deployment` (webmail) | SnappyMail webmail SPA — the end-user login UI at `mail.<domain>` (#4307) |
| `Service` (Stalwart ×3) | LoadBalancer for SMTP/submission/submissions, LoadBalancer for IMAP/IMAPS, ClusterIP for Stalwart webadmin/JMAP |
| `Service` (webmail) | ClusterIP for the SnappyMail SPA (the `mail.<domain>` HTTPRoute backend) |
| `PVC` (webmail) | SnappyMail per-user settings + sessions (file-based; no database) |
| `HTTPRoute` _or_ `Ingress` | webmail UI at `mail.<domain>` → SnappyMail (Cilium Gateway by default; Traefik fallback). Optional `mailadmin.<domain>` → Stalwart webadmin |
| `ConfigMap` (config) | Stalwart bootstrap `config.toml` — applied when RocksDB is empty |
| `ConfigMap` (webmail domain-seed) | SnappyMail `<domain>.json` pre-seeding this Stalwart as the default login domain |
| `ConfigMap` (dns-records-required) | MX/SPF/DKIM/DMARC the Org admin must publish — surfaced by unified-rbac UI |
| `ExternalSecret` (admin) | Pulls Stalwart admin password from OpenBao |
| `ExternalSecret` (oidc) | Pulls Keycloak client secret from OpenBao |
| `Job` (post-install) | Bootstraps admin principal + send-allow row (idempotent) |
| `NetworkPolicy` | Default-deny + explicit allows for SMTP/IMAP/webmail/Keycloak/PowerDNS/DNS/outbound SMTP |
| `ServiceAccount` | Identity for the Stalwart pod, webmail pod, and the setup Job |

---

## Webmail SPA (SnappyMail) — #4307

Stalwart v0.15.5 is a mail **server** (JMAP/IMAP/SMTP + a webadmin API) — it ships **no end-user webmail SPA**. Routing `mail.<domain>` straight at the Stalwart HTTP listener returns a bare 404 to a browser user (`/`, `/login`, `/webadmin` all 404; only `/jmap/session` returns 200). So the chart bundles **SnappyMail** — a lightweight, single-container PHP webmail that speaks IMAP+SMTP directly with **no database** (config + per-user data live on disk under `/var/lib/snappymail`). SnappyMail is the canonical webmail pairing for Stalwart; Roundcube was the fallback but needs its own MySQL/Postgres DB, a heavier per-Org footprint.

How it wires up:

- A `webmail` Deployment + ClusterIP Service run SnappyMail; the `mail.<domain>` HTTPRoute backend is re-pointed off the Stalwart `-web` Service onto this webmail Service (`webmail.enabled`, default **true**).
- A `domain-seed` ConfigMap renders a SnappyMail `<domain>.json` pointing IMAP at the in-cluster Stalwart `-imap` Service (993 implicit-TLS) and SMTP at the `-smtp` Service (587 STARTTLS, `useAuth`). An initContainer copies it into `_data_/_default_/domains/` so the login page lists this Stalwart server zero-touch.
- Stalwart's own webadmin/JMAP stays reachable in-cluster on the `-web` Service; flip `webmail.adminRoute.enabled=true` to expose it on a separate `mailadmin.<domain>` host.
- Gateway→webmail hop is admitted on port 8888 in both the K8s `NetworkPolicy` and the `CiliumNetworkPolicy` (`fromEntities: [ingress]`).

Acceptance: `mail.<domain>/` → HTTP 200/302 serving the SnappyMail login UI (not 404).

## SSO via Org-vcluster Keycloak

The Stalwart mail server authenticates IMAP/SMTP/JMAP users against the Organization's per-vcluster Keycloak realm — **NOT** the otech-level Keycloak. (The bundled SnappyMail webmail proxies username/password to Stalwart's IMAP/SMTP; OIDC bearer-token login flows are validated by Stalwart's OIDC directory.)

The OIDC client `stalwart` is registered in the Org realm at vcluster provisioning time (handled by [#804](https://github.com/openova-io/openova/issues/804) — tenant provisioning pipeline). The client secret is written to OpenBao at the canonical path:

```
sovereign/<sovereign-fqdn>/stalwart/<tenant>/oidc → property OIDC_CLIENT_SECRET
```

The chart's `oidc-externalsecret.yaml` pulls it down into the Org namespace.

Per-user mailbox provisioning is **event-driven** (per [ADR-0003 §3](../../docs/adr/0003-rbac-newapi-user-create-hook.md)): when the Org admin creates a user via the unified-rbac console, the unified-rbac service POSTs Stalwart's `/api/principal` admin API to create the mailbox. This chart ships only the bootstrap admin principal in the post-install Job — it does **not** loop on the NATS subject by default. Per-tenant overlays may flip `mailboxProvisioner.natsSubscriber.enabled=true` once the Org vcluster's NATS subject is wired.

---

## Domain modes

### Free-subdomain mode (default)

Operator overlay sets `domain.primary: <slug>.<otech-fqdn>` (e.g. `acme.omantel.omani.works`). The chart records the required DNS records in the `*-dns-records-required` ConfigMap and a follow-up controller (in unified-rbac) posts them to the otech PowerDNS API.

### BYO domain mode

Operator overlay sets `domain.primary: acme.com` and `domain.mode: byo`. The records ConfigMap is still emitted; the unified-rbac console UI surfaces them to the Org admin to paste into their public DNS provider. Smoke test in [#804](https://github.com/openova-io/openova/issues/804) asserts the records are reachable post-creation.

---

## Required DNS records (rendered into the ConfigMap)

| Kind | Name | Value template |
|---|---|---|
| MX | `<domain>` | priority 10 → `mail.<domain>` |
| TXT | `<domain>` | `v=spf1 mx <policy>` (default `-all` = hard fail) |
| TXT | `<selector>._domainkey.<domain>` | `v=DKIM1; k=ed25519; p=<DKIM-PUBLIC-KEY>` (the public-key blob is stamped in by the unified-rbac controller after first-boot DKIM mint) |
| TXT | `_dmarc.<domain>` | `v=DMARC1; p=reject; rua=mailto:dmarc@<domain>` (operator-tunable) |

---

## Stalwart `config.toml` gotchas

The bootstrap `config.toml` follows the pattern committed by the openova-private contabo-mkt Stalwart, with two memory-recorded gotchas:

1. **`==` not `=`** in expression matchers (queue routing, sieve conditions, send-allow expressions). A single `=` is **assignment** and **silently never matches** (incident 2026-04-14, huawei.com TLS rule). Every comparison in `templates/config-configmap.yaml` uses `==`. Per-tenant overlays adding queue-routing rules MUST follow the same convention. See [stalwart_expression_syntax.md memory](../../../.claude/projects/-home-openova-repos-openova-private/memory/stalwart_expression_syntax.md).

2. **Group principals need explicit `email-receive`** — Stalwart group principals do NOT inherit `email-receive` from the default `user` role. Without it, every inbound email to the group bounces with `550 5.5.0 This account is not authorized to receive email.` (incident 2026-04-20). The post-install Job's PATCH on the admin principal is the canonical fix; future shared-mailbox additions in tenant overlays MUST PATCH the same field. See [stalwart_send_as.md memory](../../../.claude/projects/-home-openova-repos-openova-private/memory/stalwart_send_as.md).

The bootstrap `config.toml` is **applied only once** — when RocksDB is empty (first install). Subsequent runtime config edits via webadmin or `stalwart-cli` persist in RocksDB and do **not** sync back to the ConfigMap. For disaster recovery, snapshot the running configuration via `stalwart-cli server list-config` and re-render this ConfigMap.

---

## Inbound spam filtering

**Disabled by default** per the founder directive on the corp Stalwart ([feedback_no_spam_filtering.md memory](../../../.claude/projects/-home-openova-repos-openova-private/memory/feedback_no_spam_filtering.md)) — accept everything, filter at the client. Per-Org deployments inherit the same posture; individual Organizations may opt in via webadmin runtime config.

---

## Required values (per-tenant overlay)

```yaml
# per-Org GitOps overlay: <org-slug>/stalwart.yaml
domain:
  primary: "acme.omantel.omani.works"   # or "acme.com" for BYO
  mode: "free-subdomain"                 # or "byo"

keycloak:
  realmURL: "https://auth.acme.omantel.omani.works/realms/org-acme"
  clientID: "stalwart"
  clientSecretName: "stalwart-oidc"
  oidcExternalSecret:
    remoteRef:
      key: "sovereign/omantel.omani.works/stalwart/acme/oidc"

admin:
  externalSecret:
    remoteRef:
      key: "sovereign/omantel.omani.works/stalwart/acme/admin"

dns:
  powerdns:
    enabled: true
    apiURL: "https://pdns.omantel.omani.works/api"
    apiKeySecretName: "powerdns-api-key"
    zone: "omantel.omani.works"
  dmarc:
    rua: "dmarc@acme.omantel.omani.works"
```

---

## Capacity

Default per-tenant: 100m / 256Mi requests, 1 CPU / 1Gi limits, 20Gi PVC. Roughly **50 mailboxes / 5 GB mail spool** comfortably; bump `stalwart.resources` and `persistence.spool.size` per-tenant for larger Organizations. Single replica per tenant — Stalwart RocksDB is single-writer by design at this tier.

---

## Related

- [EPIC #795](https://github.com/openova-io/openova/issues/795) — SME-tenant turnkey experience
- [#796](https://github.com/openova-io/openova/issues/796) — Hook contract (ADR-0003)
- [#802](https://github.com/openova-io/openova/issues/802) — Unified RBAC SME-tier (consumes the dns-records ConfigMap)
- [#804](https://github.com/openova-io/openova/issues/804) — Tenant provisioning pipeline (registers OIDC client + writes secrets)
- [#805](https://github.com/openova-io/openova/issues/805) — End-to-end demo

*Part of [OpenOva](https://openova.io)*
