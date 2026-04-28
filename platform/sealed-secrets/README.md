# Sealed Secrets

Transient bootstrap-only secret transport. **Catalyst control plane** (per [`docs/PLATFORM-TECH-STACK.md`](../../docs/PLATFORM-TECH-STACK.md) §3.3 — Security and policy). Used during Phase 0 of Sovereign provisioning to ship initial bootstrap secrets through GitOps; archived/disabled after OpenBao + ESO replace it.

**Status:** Accepted. Chart wrapper at `chart/`. **Updated:** 2026-04-28.

---

## Why transient

Per `docs/SOVEREIGN-PROVISIONING.md` §3 (Phase 0 Bootstrap kit):

```
e. Sealed Secrets (transient, only for bootstrap secrets)
```

Sealed Secrets is the standard pattern for "secrets in Git for the first 60 seconds of a cluster's life". After Phase 1 hand-off (per §4), the canonical Catalyst secret backend is OpenBao + ExternalSecrets Operator (ESO). Sealed Secrets stays installed but unused — the controller scales to 0 and the kubeseal CLI is no longer used.

Long-term cluster secrets follow the OpenBao path of `org/<org>/env/<env_type>/...` and are materialized into K8s Secrets via ESO `ExternalSecret` CRs.

---

## Chart

The `chart/` directory wraps the upstream Sealed Secrets Helm chart with Catalyst-curated values: minimal resources (controller is bootstrap-only), no UI.

OCI artifact: `ghcr.io/openova-io/bp-sealed-secrets:1.0.0`.

---

*Part of [OpenOva](https://openova.io)*
