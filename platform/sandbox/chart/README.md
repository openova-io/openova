# sandbox-controller chart (Wave 1)

Helm chart for the Catalyst-built **sandbox-controller**, the
reconciler for `sandbox.openova.io/v1.Sandbox` CRs (architecture
spec: `products/sandbox/docs/architecture.md` §7).

## What it deploys

| Resource | Purpose |
|---|---|
| `Deployment/sandbox-controller` | controller-runtime manager, leader-election on by default |
| `ServiceAccount/sandbox-controller` | identity used to read Sandbox CRs + patch status |
| `ClusterRole/sandbox-controller` | least-privilege grants — `sandboxes`, `sandboxes/status`, `sandboxes/finalizers`, `leases`, `events` |
| `ClusterRoleBinding/sandbox-controller` | binds the SA to the ClusterRole |

The `Sandbox` CRD ships separately at
`products/catalyst/chart/crds/sandbox.yaml` (PR #1615).

## What Wave 1 reconciles

For each `Sandbox` CR, the controller renders + PutFile-s the
following manifests into the per-Org `catalyst-tenant` Gitea repo at
`sandbox/<owner-uid>/`:

```
sandbox/<owner-uid>/
├── namespace.yaml         # sandbox-<owner-uid>
├── resourcequota.yaml     # mirrors spec.quota
├── serviceaccount.yaml    # the in-namespace `sandbox` SA
├── role.yaml              # pods/deployments/pvc CRUD inside the ns
├── rolebinding.yaml
├── secret.yaml            # placeholder `sandbox-tokens` (filled in Wave 2)
├── pvc-<repo-slug>.yaml   # one per spec.repos[]
└── kustomization.yaml
```

Flux on the host cluster picks them up and reconciles them inside the
Org vcluster (the existing per-Org Flux Kustomization that
organization-controller's vcluster manifests already live under).

## What Wave 1 does NOT do (deferred to Wave 2+)

- pty-server StatefulSet
- openova-sandbox-mcp Deployment
- HTTPRoute / Gateway publishing `<pr>.<app>.<sb-<owner>>.<sov>.openova.io`
- Long-lived org-scoped token issuance + Secret population
- per-repo git-clone initContainer

## Install

The chart is opt-in. In a per-Sovereign overlay:

```yaml
sandbox:
  enabled: true
  image:
    tag: "<git-sha>"       # CI-stamped per Inviolable Principle #4a
  env:
    hostCluster: "hz-fsn-rtz-prod"
    sovereignFQDN: "omantel.omani.works"
```

The chart reuses `Secret/catalyst-gitea-token` (key `token`) that
organization-controller already consumes — single source of truth per
issues #957 / #1052.

## Reference

- Architecture: `products/sandbox/docs/architecture.md` §7
- Template controller: `core/controllers/organization/internal/controller/organization_controller.go`
- Template gitops renderer: `core/controllers/organization/internal/gitops/manifests.go`
- CRD: `products/catalyst/chart/crds/sandbox.yaml` (PR #1615)
- Go types: `core/controllers/sandbox/internal/sandboxapi/types.go`
