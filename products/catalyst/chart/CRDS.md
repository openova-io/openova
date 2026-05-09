# Catalyst Chart — CRD Installation Notes

## Provenance

The 9 catalyst-domain CRDs in `crds/` are auto-installed by Helm on
`helm install` and `helm upgrade`:

| File | API group | Kind |
|------|-----------|------|
| `application.yaml` | apps.openova.io | Application |
| `blueprint.yaml` | catalyst.openova.io | Blueprint |
| `continuum.yaml` | dr.openova.io | Continuum |
| `environment.yaml` | catalyst.openova.io | Environment |
| `environmentpolicy.yaml` | catalyst.openova.io | EnvironmentPolicy |
| `organization.yaml` | orgs.openova.io | Organization |
| `provisioningstate.yaml` | catalyst.openova.io | ProvisioningState |
| `runbook.yaml` | catalyst.openova.io | Runbook |
| `secretpolicy.yaml` | catalyst.openova.io | SecretPolicy |

These are watched by the in-tree controllers under
`core/controllers/{environment,organization,application,...}`. If any
of them is missing from the API server, the matching controller logs:

```
controller-runtime.source.EventHandler   if kind is a CRD, it should be installed before calling Start
```

…and never reaches `Starting workers`.

## UserAccess (out-of-tree CRD)

`useraccesses.access.openova.io` is **not** in this chart — it is a
Crossplane Composite Resource backed by an XRD shipped from
`platform/crossplane-claims/chart/templates/xrds/useraccess.yaml`
(per ADR-0001 §3: Crossplane is the day-2 IaC for IAM grants).

A Sovereign that runs `catalyst-useraccess-controller` therefore
requires both this catalyst chart **and** the `crossplane-claims`
chart with `userAccess.enabled=true`.

## Chroot Sovereign install path

On the chroot (single-cluster) Sovereign topology:

- Flux is **suspended** by design — `helm install` and `helm upgrade`
  are not running on a schedule
- Charts are applied operator-style during cutover (qa-loop / install
  scripts), so `helm install --include-crds` (the default) takes care
  of `crds/`
- If a Sovereign was provisioned by `kubectl apply -f` of pre-rendered
  templates (NOT `helm install`), the CRDs in `crds/` are **skipped**
  by `helm template` — apply them explicitly:

```bash
for crd in products/catalyst/chart/crds/*.yaml; do
  kubectl --context=<sovereign> apply -f "$crd"
done

# UserAccess XRD lives in the crossplane-claims chart:
helm template platform/crossplane-claims/chart \
  --set userAccess.enabled=true \
  --show-only templates/xrds/useraccess.yaml \
  | kubectl --context=<sovereign> apply -f -
```

The above sequence was used on 2026-05-09 to recover the omantel
chroot Sovereign after a partial cutover (qa-loop iter-1, Fix #13).

## Future-proofing

For a fully event-driven Sovereign install path, ensure the cutover
driver invokes `helm install/upgrade` (not `kubectl apply -f` of
rendered manifests) so `crds/` is always applied. See
`products/catalyst/bootstrap/cutover/` step-06 onward.
