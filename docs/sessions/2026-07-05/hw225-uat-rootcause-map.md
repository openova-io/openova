# hw225 UAT — complete ❌ root-cause map (2026-07-05)

Live env: hw225 (dep 26df2f30b065e857), 2-region kom4dc, 12 nodes, Org uat225wp (funnel, plan=m, vcluster 1/1). This session walked all 280 UAT rows + P23-challenged every ⛔, root-caused every ❌ to its fundamental cause, and shipped ~21 PRs. Every remaining ❌ is captured below with its exact cause + fix path. None is theater; each needs one specific input outside a hand-patched env.

## Shipped this session (~21 PRs)

| PR | Fix |
|---|---|
| #4786/#4789/#4790/#4791 | tenant-leak UI + ProviderConfig GVK |
| #4792 (merged) | vcluster gateway-api CRD sync — **validated live** (openclaw installs, HTTPRoute renders) |
| #4793 | mimir minio wait 29→150 (slow kom4dc) |
| #4794/#4796/#4797/#4799/#4802 | adoption module — 10 static layers (cred-key, provider Kyverno-denial, ipv4_address, hcloud dummy, coalesce try) |
| #4798 | apps-sync sourceRef flux-system→openova-org-tenants |
| #4800 | worker-node count excludes control-planes (24-vs-12) |
| #4801 | vcluster 0/0 — StatefulSet detection source |
| #4803 | openclaw internal-JWKS seam (NAT-EIP hairpin) |

## ❌ root-cause map — remaining, by required input

### A. Design decision needed
- **206/207/239 adoption** (#4620): `managementPolicies:[Observe]` + a data-only module structurally can't produce the state provider-opentofu's observe requires ("No state file"). 10 static layers fixed; runtime needs the redesign (one-shot apply / tracked resource / read cloud API in-controller). HCS API confirmed reachable; not network-gated.
- **90/225/226/233 per-Org apps**: `catalyst-tenant-<slug>-apps` Kustomization (`kubeConfig=vcluster`, `path=./vcluster/apps`) applies app HRs INTO the vcluster (M+ tier design, gitops.go:449) — but the vcluster has no `helm.toolkit.fluxcd.io/v2` CRD/controller → "no matches for kind HelmRelease". Plus a dual-door with host-side `org-tenants`. Fix: HRs-on-host-with-`spec.kubeConfig` (clean) OR vcluster-Flux (heavy) + collapse the dual-door.

### B. Founder's DRAFT
- **236/71 region-b** (#4656): cross-region WireGuard data plane broken (56-byte ping 100% loss node↔pod cross-region); clustermesh 1/1 but shared-pg replica stuck "Setting up primary", mesh endpoints empty.

### C. Shipped-as-PR, needs merge + fresh prov to activate
- **224/232 openclaw**: #4792 (installs) + #4803 (internal-JWKS) shipped; readyz NAT-EIP hairpin root-caused. Layer-4 follow-up: vcluster keycloak Service-sync + org-overlay `oidc.internalIssuerURL`.
- worker-node/vcluster counts (#4800/#4801), adoption static layers, apps sourceRef (#4798) — all merge-then-bake.

### D. Verified correct/valid (no fix)
- console TLS (valid LE cert, 200), treemap layer default (#4731), PIN email (mothership openova.io correct; customer flow keycloak-OTP), node/vcluster ground truth (12 nodes, 1 vcluster).

## Activation path
Merge the shipped PRs (CI + review) → fire a fresh prov that bakes them → walk clean. That flips the Category-C rows in one pass. Categories A + B need an architecture decision (#4620, per-Org app-delivery) and the founder's #4656 respectively — no further hand-patching advances them.
