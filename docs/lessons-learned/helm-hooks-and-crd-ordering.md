> NOTE (2026-06-03): pending migration into docs/RUNBOOKS.md §3 (Blueprint/chart authoring) per lean-doc strategy.

# Helm hooks + CRD ordering

Operational lessons about Helm post-install/pre-install hook behavior in charts that wrap an upstream subchart which registers CRDs.

## `helm.sh/hook-delete-policy: before-hook-creation` deadlocks on first install when the CRD comes from the same chart's upstream subchart

When a Catalyst-curated wrapper chart (e.g. `bp-external-secrets`, `bp-crossplane`) ships:

1. An upstream subchart that registers a CRD (e.g. `ClusterSecretStore`, `CompositeResourceDefinition`)
2. AND a Catalyst-side template that declares a CR of that kind, gated with `helm.sh/hook: post-install,post-upgrade` + `helm.sh/hook-delete-policy: before-hook-creation`

…the install deadlocks on FIRST reconcile with:

```
Helm install failed for release ...:
  failed post-install: unable to build kubernetes object for deleting hook ...:
  resource mapping not found for name: "<crname>" namespace: ""
  no matches for kind "<Kind>" in version "<group>/<version>"
  ensure CRDs are installed first
```

The reason: `before-hook-creation` runs a kubectl-style lookup of the existing CR before creating the hook resource. That lookup happens BEFORE the upstream subchart's CRDs finish registering (Helm renders the whole release in one pass; admission ordering isn't deterministic enough to make the hook safe). The lookup itself fails because the apiserver has no mapping for the kind yet.

### Live incidents

- **bp-crossplane@1.1.2** — XRDs+Compositions in the same chart as the controller; `no matches for kind CompositeResourceDefinition`. Resolved in PR #247 (split into `bp-crossplane@1.1.3` controller + `bp-crossplane-claims@1.0.0`).
- **bp-external-secrets@1.0.0** — default `ClusterSecretStore` in the same chart as the ESO controller; `no matches for kind ClusterSecretStore`. Hit on otech.omani.works 2026-04-30 18:55Z. Same architectural fix in PR #334 (split into `bp-external-secrets@1.1.0` controller + `bp-external-secrets-stores@1.0.0`).

**Rule**: Never put a CR template with `helm.sh/hook-delete-policy: before-hook-creation` in the same Helm release as the upstream subchart that registers its CRD. Architectural fix: split into a controller chart and a CR chart, sequenced via Flux `dependsOn`. The CR chart needs no hook annotations — by the time its HelmRelease starts, Flux has verified the controller is `Ready=True` and CRDs are guaranteed registered.

**Ref**: #318 #331 #247
