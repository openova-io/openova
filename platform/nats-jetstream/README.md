# NATS JetStream

Catalyst's control-plane event spine. **Catalyst control plane component** (per [`docs/ARCHITECTURE.md`](../../docs/ARCHITECTURE.md) §2.3 — Per-Sovereign supporting services). 3-node JetStream cluster with per-Organization Account isolation.

**Status:** Accepted. Chart wrapper at `chart/`. **Updated:** 2026-04-28.

---

## Why

Per [`docs/ARCHITECTURE.md`](../../docs/ARCHITECTURE.md) §5: every state change in a Sovereign flows through NATS JetStream as the event log + KV store. The projector service consumes JetStream subjects, materializes per-Environment KV state, and fans out to the console via SSE. JetStream replaces what was previously specified as "Redpanda + Valkey" for the control plane — Apache 2.0, native KV, native multi-tenant Accounts (per [`docs/GLOSSARY.md`](../../docs/GLOSSARY.md) — `event-spine`).

**Application-tier event needs** (e.g. an App that wants Kafka or Redis-compatible streaming) remain free to install Strimzi/Kafka or Valkey as Application Blueprints — this is the control plane only.

---

## Subject namespace

Per `ARCHITECTURE.md` §11.2 bullet 4:

- One NATS Account per Catalyst Organization (multi-tenant isolation).
- Subjects within the Account use the prefix `ws.{org}-{env_type}.>` for per-Environment partitioning.
- KV bucket per Environment: `ws-{org}-{env_type}-state/<kind>/<name>`.

---

## Chart

The `chart/` directory wraps the upstream NATS Helm chart with Catalyst-curated values: 3-node cluster, JetStream enabled, file-store PVC, ServiceMonitor for Prometheus.

Installed by the Catalyst bootstrap kit during Phase 0 (per [`docs/RUNBOOKS.md`](../../docs/RUNBOOKS.md) §1 — Fresh provisioning) — before OpenBao (which uses NATS for its own audit log). (SPIRE is not a preceding step: SPIFFE/SPIRE is deferred/opt-in per PR #665; canonical workload identity is Cilium WireGuard + K8s SA TokenReview.)

OCI artifact: `ghcr.io/openova-io/bp-nats-jetstream:1.0.0`.

---

*Part of [OpenOva](https://openova.io)*
