# Tier-2 SSO audit — `bp-sigstore`

> Federates to: `sovereign` realm (single hop) — Catalyst-CP admin UI tier.
> Chart path: `platform/sigstore/chart/`
> Chart version on `main`: `1.0.0` (Chart.yaml:15)
> Topology row (audit doc): per-host-cluster singleton (optional `cosign.<sov>` per audit row)

## Current SSO state — **NONE — UI surface DOES NOT EXIST**

The G117 dispatch brief lists this as "bp-sigstore (cosign UI)" but **Sigstore Policy Controller has no UI**. The chart is exclusively a `sigstore/policy-controller` subchart wrapper — a Kubernetes admission webhook that verifies Cosign signatures, in-toto attestations, and SBOMs at admission time. There is no user-facing surface to federate.

| Surface | Status | File:line |
|---|---|---|
| User-facing HTTP UI in the chart | **DOES NOT EXIST** | n/a |
| HTTPRoute / Ingress | NONE | (chart has zero `templates/`; values has `sigstoreOverlay.networkPolicy: enabled=false` placeholder only — values.yaml:62-68) |
| OIDC values block | NONE | n/a |
| KC client registration | N/A | n/a |

The `policy-controller` subchart ships only:
- `policy-controller` Deployment (webhook + controller-manager)
- ValidatingWebhookConfiguration
- ClusterImagePolicy CRD
- Service for the webhook backplane (not user-facing; only the apiserver calls it)

`appVersion: "0.13.1"` (Chart.yaml:16) is the policy-controller webhook server, NOT a Cosign UI.

## What the audit doc Section §Per-host-cluster row likely meant

The audit doc lists `bp-sigstore` with `optional cosign.<sov>` external endpoint. This might refer to one of:

1. **`fulcio-server`** — Sigstore certificate authority (issues short-lived certs for keyless signing). Not in this chart; would require new `bp-fulcio` Blueprint. Fulcio DOES have an OIDC flow but as an *issuer-side* OAuth provider (Fulcio consumes user OIDC ID-tokens to mint signing certs).
2. **`rekor-server`** — Sigstore transparency log. Has a read-only API endpoint at `rekor.<sov>` typical pattern, but no UI and no auth (the log is intentionally public).
3. **A speculative "Cosign UI" surface** — no such thing exists in OSS Sigstore today.

## Required Wave-2 action — **DEFER / DOCUMENT AS NO-OP**

`bp-sigstore` is OUT OF SCOPE for G117.5 Tier-2 SSO fan-out because there is no UI surface. Wave-2 SSO agent should:

1. NOT add any SSO templates to `platform/sigstore/chart/`.
2. NOT add a `sigstore` clientId to `configmap-sovereign-realm.yaml`.
3. Update `docs/sessions/2026-06-02-per-blueprint-topology-audit.md` row 79 (`bp-sigstore`) to drop the `optional cosign.<sov>` external endpoint note (replace with `none`).
4. If a Sigstore-stack UI is ever introduced (e.g. via `bp-fulcio` or `bp-rekor-ui`), open a fresh sub-EPIC under G117.5 to wire SSO at that point.

## Recommendation for B5 dispatch

Drop `bp-sigstore` from the Tier-2 fan-out worklist. Reduces Tier-2 from 4 apps to **3 apps** (guacamole, powerdns-admin, cilium-hubble-ui).

## Chart-patch size estimate

**Zero** — no chart edit. Only documentation update to `docs/sessions/2026-06-02-per-blueprint-topology-audit.md` row 79 (separate from the SSO fan-out PR).
