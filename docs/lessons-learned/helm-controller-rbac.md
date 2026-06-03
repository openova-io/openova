> NOTE (2026-06-03): pending migration into docs/RUNBOOKS.md §7 (troubleshooting) per lean-doc strategy.

# helm-controller RBAC + parse behavior

Lessons about Flux helm-controller (v1.1.0) operational quirks discovered while bringing up Sovereign-1 (otech.omani.works).

## Flux helm-controller SA needs cluster-admin in flux-system

bp-flux ships a `cluster-reconciler` ClusterRoleBinding that subjects the helm-controller ServiceAccount in `catalyst-system` namespace, but the helm-controller pod actually runs in `flux-system`. Result: helm-controller cannot read/update the `sh.helm.release.v1.<name>.<n>` Secrets it stores in `flux-system`, and every HR transitioning through `pending-install` / `pending-upgrade` / `uninstalling` gets stuck — the user-facing symptom is "Unlocked Helm release ... in pending-install state" with no progress.

Workaround applied at runtime on otech 2026-04-30:

```bash
kubectl create clusterrolebinding flux-system-helm-controller-admin \
  --clusterrole=cluster-admin --serviceaccount=flux-system:helm-controller

kubectl create clusterrolebinding flux-system-kustomize-controller-admin \
  --clusterrole=cluster-admin --serviceaccount=flux-system:kustomize-controller
```

(`kustomize-controller` has the same wrong-namespace mismatch.)

**Rule**: When debugging a stuck Flux HelmRelease, before chasing chart issues, run `kubectl get clusterrolebinding -o json | jq '.items[] | select(.subjects[]?.namespace=="flux-system" and .subjects[]?.name=="helm-controller")'`. If the binding's `roleRef` is anything weaker than `cluster-admin` (or doesn't grant `secrets/{get,list,update,delete}` in flux-system), the controller cannot manage its own state.

**Permanent fix (bp-flux 1.1.3)**: `platform/flux/chart/templates/catalyst-cluster-reconciler-rbac.yaml` ships a Catalyst-managed `catalyst-cluster-reconciler` ClusterRoleBinding that subjects helm-controller AND kustomize-controller in `.Values.catalyst.fluxNamespace` (default `flux-system`) to `cluster-admin`. This is independent of (and authoritative over) the upstream subchart's `cluster-reconciler` binding — if the upstream binding ever drifts again, the Catalyst overlay still holds the cluster correct.

**Ref**: #338, PR fix/338-omantel

---

## helm-controller PARSES every template even when `{{- if }}` gated — values cannot skip parse-time errors

helm-controller v1.1.0's bundled helm SDK parses every template file in a chart at install time, regardless of whether `{{- if .Values.X.enabled }}` would render it. If a template uses a function the parser doesn't define (e.g. `fromToml` from helm 3.13+ on a controller bundling an older SDK), the install fails with `function "X" not defined` even when the value gate is false.

Concrete incident: bp-seaweedfs's upstream subchart `seaweedfs/seaweedfs` 4.22.0 uses `fromToml` in `templates/shared/security-configmap.yaml` gated on `.Values.global.seaweedfs.enableSecurity`. Setting `enableSecurity: false` (the chart's own default) does NOT skip parsing — install still fails. The fix is to remove the offending file from the packaged chart (vendor + strip), not a values override.

**Rule**: When an upstream chart fails with `function "<name>" not defined`, the fix is NEVER a values override. Two correct paths:
1. Vendor the upstream chart into `platform/<name>/chart/charts/<upstream>/`, drop the `dependencies:` declaration in Chart.yaml, physically delete the offending template file. Bump the bp version (vendoring deserves a major-version signal).
2. Upgrade fluxcd/helm-controller to a release whose bundled helm SDK supports the function. Verify which helm SDK ships with which controller version before bumping.

**Ref**: #340
