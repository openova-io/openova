# Chart Authoring Notes

**Status:** Authoritative.
**Audience:** Anyone editing a `products/<name>/chart/templates/*.yaml` or
`platform/<name>/chart/templates/*.yaml` resource that ships to a Flux-
reconciled cluster.

This document captures sharp edges in the chart-authoring workflow that
have already cost the project a real outage. Each section names a
specific failure mode, a specific reproducer, and the canonical fix —
in the same shape as `docs/INVIOLABLE-PRINCIPLES.md`. Read it before
declaring "done" on any chart that mutates a long-lived resource.

---

## Strategy flips on existing Deployments

### What goes wrong

A chart manifest declares `Deployment.spec.strategy.type: Recreate`.
The cluster already runs a Deployment of the same name that was
created earlier with the default `RollingUpdate` strategy (so
`spec.strategy.rollingUpdate.maxSurge=25%` and `maxUnavailable=25%`
exist on the live object). Flux's kustomize-controller submits the
new manifest via Server-Side Apply with the `kustomize-controller`
field manager. The API server merges, then validates. Validation
rejects with:

```
Deployment.apps "<name>" is invalid:
  spec.strategy.rollingUpdate: Forbidden:
    may not be specified when strategy `type` is 'Recreate'
```

The Flux Kustomization parks at `Ready=False` on every reconcile
until an operator intervenes.

### Why Server-Side Apply does this

SSA's contract is "set the fields you declare." It does NOT remove
fields owned by other field managers. The pre-existing Deployment was
created via `kubectl apply` (CSA), so the
`kubectl-client-side-apply` field manager owns
`.spec.strategy.rollingUpdate.maxSurge` and
`.spec.strategy.rollingUpdate.maxUnavailable`. When kustomize-
controller flips `.spec.strategy.type` to `Recreate`, those rolling-
update fields stay on the object. The post-merge state has both
`type: Recreate` AND `rollingUpdate.*` keys. The API validator forbids
that combination. SSA cannot fix this on its own.

### Why `$patch: replace` is NOT the answer

`$patch: replace` is a Strategic Merge Patch runtime directive. It
does NOT belong in a chart's base resource. Reasons:

1. **API strict-decoding rejects it on CREATE.** `kubectl create`,
   `kubectl apply` to an empty namespace, and `kubectl apply
   --server-side` all return:
   ```
   strict decoding error: unknown field "spec.strategy.$patch"
   ```
   This BREAKS fresh installs — including every new Sovereign
   bootstrap.
2. **Flux SSA rejects it.** The `kustomize-controller` SSA path
   returns `field not declared in schema` on
   `.spec.strategy.$patch`.
3. **It is a runtime directive, not a chart field.** `$patch:
   replace` is processed at SMP merge time by SMP-aware mergers.
   `kustomize build` does NOT consume the directive when it appears
   in a base resource — it passes it through as if it were a normal
   YAML key. The downstream API call then fails as above.

The correct place for `$patch: replace` is inside a Kustomize
`patches:` entry, where the kustomize binary processes it at build
time and emits a clean output that contains no `$patch` key. That is
not what fixes the strategy-flip problem either, because the build-
time output is identical to declaring `strategy.type: Recreate`
directly — it produces the same SSA failure.

### The canonical fix

Annotate the Deployment with the Flux force annotation:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: catalyst-api
  annotations:
    kustomize.toolkit.fluxcd.io/force: enabled
spec:
  replicas: 1
  strategy:
    type: Recreate
  # ...
```

When kustomize-controller's SSA dry-run fails with an Invalid response
on this resource, the controller falls back to delete-and-recreate the
SINGLE annotated resource (not the whole Kustomization). The
recreated Deployment has no residual `rollingUpdate.*` fields — the
regression cannot recur on the rebuilt object. The annotation lives
in Git, version-controlled, applies on every reconcile.

This is **not** a "kubectl delete bandaid." Per
[INVIOLABLE-PRINCIPLES.md](INVIOLABLE-PRINCIPLES.md) #3 (Follow the
documented architecture, exactly — Flux is the ONLY GitOps reconciler)
and #4 (Never hardcode — runtime configuration in Git, not in shell
history): the remediation is declarative, scoped to the resource, and
removed only by editing the chart.

### When you may use this annotation

The Flux force annotation triggers delete + recreate on apply
failure. Use it only on resources that:

- Already declare `strategy.type: Recreate` (so delete-and-recreate is
  the steady-state update path anyway), OR
- Carry no client traffic (a brief unavailability is acceptable), OR
- Are explicitly designed to lose in-process state on every roll.

Do NOT add the annotation to a resource whose default update mode is
`RollingUpdate` and whose pods serve live traffic — you would be
trading off availability against an outcome that better resource
authoring (selectors, immutable-field migrations) could deliver.

### Required test coverage

Every chart that flips `Deployment.spec.strategy.type` MUST be covered
by a test fixture in `tests/integration/strategy-flip.yaml` (or its
equivalent next to a similar regression). The test must:

1. Stage a Deployment with the OLD strategy at the same name.
2. Apply the NEW chart manifest.
3. Assert the apply succeeds via the documented apply path.
4. Assert the chart manifest carries the Flux force annotation.
5. Assert the chart manifest is also valid for fresh install (no
   inline `$patch: replace` or other strict-decoding-violating
   directives).

The current implementation lives at
[`tests/integration/strategy-flip.sh`](../tests/integration/strategy-flip.sh)
and the CI workflow at
`.github/workflows/test-strategy-flip.yaml`. Wire any new strategy-
flip into both.

### Reference incident

- **Date:** 2026-04-29
- **Cluster:** contabo-mkt
- **Resource:** `catalyst/catalyst-api`
- **Symptom:** Kustomization stuck Ready=False for hours; user
  unblocked manually with `kubectl delete deploy catalyst-api -n
  catalyst`. Flux re-created the Deployment from scratch on the next
  reconcile; the `rollingUpdate.*` fields were no longer present and
  the Kustomization went Ready=True.
- **Root cause:** chart's `api-deployment.yaml` declared
  `strategy.type: Recreate`; the live object had been created with
  default RollingUpdate; SSA preserved the rollingUpdate fields under
  the prior field manager.
- **Durable fix:** add `kustomize.toolkit.fluxcd.io/force: enabled`
  annotation to the chart manifest at
  `products/catalyst/chart/templates/api-deployment.yaml`.

---

## Generalizing the lesson

### Other chart fields that can collide on apply

The strategy-flip is one instance of a broader class: fields whose
**old value** and **new value** cannot legally coexist, where the old
value is owned by a non-Flux field manager. The same fix applies to
each of them — annotate the resource with
`kustomize.toolkit.fluxcd.io/force: enabled` and let Flux recover via
delete-and-recreate when SSA dry-run fails.

| Resource kind | Field that triggers an Invalid merge | Notes |
|---|---|---|
| `Deployment` | `spec.strategy.type` Recreate ↔ RollingUpdate | This document. |
| `Deployment` | `spec.selector.matchLabels` change | Selector is immutable post-create. Must recreate. |
| `Service` | `spec.clusterIP` (None ↔ value) | Immutable. Must recreate. |
| `Service` | `spec.type` ClusterIP ↔ NodePort ↔ LoadBalancer | Some transitions invalid; recreate is safe path. |
| `PersistentVolumeClaim` | `spec.accessModes` change after binding | Immutable post-bind. Recreate would lose data — DO NOT add force annotation; instead provision a new PVC under a new name and migrate. |
| `StatefulSet` | `spec.serviceName`, `spec.selector` | Immutable. Must recreate (which loses pod identity). Plan migrations carefully. |
| `Job` | `spec.template.*` after create | Immutable. Recreation is the only path. |

For PVCs and StatefulSets specifically: NEVER add the Flux force
annotation as a default. Data loss is the failure mode. The right
move is a paired migration: provision the new resource under a new
name, copy data, swap references, retire the old.

### Authoring discipline

Before declaring "done" on any chart that touches a long-lived
resource:

1. Run the chart's manifest through `kubectl apply --dry-run=server`
   against an EMPTY namespace. Must succeed (no `$patch:` in the
   spec, no fields the strict decoder rejects).
2. If the resource type appears in the table above, ALSO run
   `kubectl apply --dry-run=server` against a namespace where a
   PRIOR shape of the resource already exists. Must succeed under the
   user's documented apply path; if it fails, add the Flux force
   annotation AND the integration test.
3. Verify the chart's `kustomization.yaml` references all template
   files (catches the "I added a template but forgot to wire it"
   regression).
4. If the resource carries client traffic, document the recreate
   blast radius in the chart's leading comment — operators reading
   the chart need to know an apply may interrupt service.

### Cross-references

- [`docs/INVIOLABLE-PRINCIPLES.md`](INVIOLABLE-PRINCIPLES.md) #3 —
  Follow the documented architecture, exactly. Flux is the ONLY
  GitOps reconciler; remediations live in IaC, not in shell history.
- [`docs/INVIOLABLE-PRINCIPLES.md`](INVIOLABLE-PRINCIPLES.md) #4 —
  Never hardcode. Runtime knobs live in Git as declarative resources,
  not as operator runbook steps.
- Flux docs:
  https://fluxcd.io/flux/components/kustomize/kustomizations/#force
  — official documentation of the
  `kustomize.toolkit.fluxcd.io/force: enabled` annotation.
- `tests/integration/strategy-flip.sh` — the runner that defends the
  Catalyst chart against this regression.
- `tests/integration/strategy-flip.yaml` — the bad-state fixture and
  assertion contract.
- `.github/workflows/test-strategy-flip.yaml` — CI wiring.
