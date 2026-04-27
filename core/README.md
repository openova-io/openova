# Catalyst Control Plane (`core/`)

The Go application that implements the **Catalyst control plane** — the user-facing UI and the controllers that turn a Kubernetes cluster into a **Sovereign**.

**Status:** Development | **Updated:** 2026-04-27

> **Read first:** [`docs/GLOSSARY.md`](../docs/GLOSSARY.md), [`docs/ARCHITECTURE.md`](../docs/ARCHITECTURE.md). This README assumes that context.

---

## What this is

A single Go application (the `core/` directory) packaged as multiple components of the Catalyst control plane. The same codebase produces:

- **console** — the primary user-facing UI (Astro/Svelte/React frontend served by a Go backend).
- **marketplace** — the public-facing Blueprint card grid.
- **admin** — the `sovereign-admin` operations UI.
- **provisioning** — the service that validates configSchema, composes manifests, commits to Environment Gitea repos.
- **projector** — the CQRS read-side service: NATS JetStream → KV → SSE.
- **catalog-svc** — Blueprint catalog API.
- **workspace-controller** — reconciles the Environment CRD (vcluster + Flux + Gitea + webhook).
- **blueprint-controller** — watches Blueprint repositories.
- **billing** — per-Org metering.

These are deployed as separate workloads but share most of the codebase via internal packages.

---

## What this is **not**

- It is **not** a "bootstrap wizard" + "lifecycle manager" duo. The historical split between those two is gone — both fold into the Catalyst control plane. Phase 0 bootstrap is performed by `catalyst-provisioner` (an OpenOva-hosted service or a customer-deployed Blueprint) running OpenTofu; thereafter, the control plane runs continuously inside the Sovereign.
- It is **not** Crossplane. Crossplane is an infrastructure dependency, deployed alongside.
- It is **not** Backstage. The Catalyst console is purpose-built for Catalyst's data model; we don't reuse Backstage's plugin system.

---

## Directory structure

```
core/
├── apps/
│   ├── console/           # console + marketplace + admin (frontend + Go backend)
│   ├── projector/         # CQRS projector service
│   ├── workspace-controller/
│   ├── blueprint-controller/
│   ├── provisioning/
│   ├── catalog-svc/
│   └── billing/
├── internal/
│   ├── domain/            # Pure business logic, zero infra deps
│   │   ├── sovereign.go
│   │   ├── organization.go
│   │   ├── environment.go
│   │   ├── application.go
│   │   ├── blueprint.go
│   │   └── events.go
│   ├── application/       # Use cases / orchestration
│   ├── adapters/          # Infrastructure adapters
│   │   ├── kubernetes/
│   │   ├── crossplane/
│   │   ├── opentofu/      # bootstrap-only
│   │   ├── gitea/
│   │   ├── jetstream/     # NATS JetStream
│   │   ├── openbao/
│   │   └── keycloak/
│   └── events/            # CloudEvents envelopes
├── pkg/
│   └── apis/v1alpha1/     # CRD types: Sovereign, Organization, Environment, Application, Blueprint, EnvironmentPolicy, SecretPolicy, …
├── ui/                    # Frontend (Astro + Svelte; same codebase serves console / marketplace / admin via routes)
└── deploy/                # Kustomize bases for each control-plane component
```

---

## CRDs (in `pkg/apis/v1alpha1/`)

```yaml
apiVersion: catalyst.openova.io/v1alpha1
kind: Sovereign            # the top-level deployment object
─────────────────────────
kind: Organization         # multi-tenancy unit inside a Sovereign
─────────────────────────
kind: Environment          # {org}-{env_type} scope; vcluster + Gitea repo
─────────────────────────
kind: Application          # an installed Blueprint
─────────────────────────
kind: Blueprint            # registered from a Blueprint repo
─────────────────────────
kind: EnvironmentPolicy    # PR / soak / change-window rules per Env
─────────────────────────
kind: SecretPolicy         # rotation rules
─────────────────────────
kind: Runbook              # auto-remediation runbooks
```

See [`docs/ARCHITECTURE.md`](../docs/ARCHITECTURE.md) for how these compose, and [`docs/BLUEPRINT-AUTHORING.md`](../docs/BLUEPRINT-AUTHORING.md) for the Blueprint CRD spec.

---

## Hexagonal architecture

```
                    ┌───────────────────────┐
   HTTP handlers ──►│                       │──► Kubernetes API
   K8s controllers ►│   Domain (pure Go)    │──► OpenBao
   JetStream subs ─►│                       │──► Gitea
   SSE clients   ◄──│                       │──► Crossplane
                    └───────────────────────┘
                          (zero external deps)
```

The domain layer is pure Go. All I/O goes through adapters in `internal/adapters/`. This keeps the core business logic (Sovereign, Organization, Environment, Application, Blueprint state machines) independent of any specific infrastructure choice — easy to test, easy to swap adapters when a backing technology evolves.

---

## Event-driven core

```go
// Domain emits events
type ApplicationInstallRequested struct {
    Environment string
    Name        string
    Blueprint   BlueprintRef
    Values      ConfigValues
}

// Bus routes to handlers (in-process Go channels for fast path,
// JetStream for durable / cross-service fan-out)
bus.Subscribe(ApplicationInstallRequested{}, func(e Event) {
    plan := planner.Compose(e)
    git.Commit(e.Environment.GiteaRepo, plan.Manifests)
    events.Publish("ws.<env>.application.installing", e)
})
```

The internal in-process bus and the JetStream spine use the same envelope shape (CloudEvents). Local tests run with the in-process bus; production runs with JetStream as the durable transport.

---

## Reconciliation pattern (Manager components)

Standard controller-runtime style:

```go
func (r *EnvironmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    var env catalystv1.Environment
    if err := r.Get(ctx, req.NamespacedName, &env); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }

    // Ensure vcluster
    if err := r.ensureVCluster(ctx, &env); err != nil {
        return ctrl.Result{}, err
    }
    // Ensure Gitea repo
    if err := r.ensureGiteaRepo(ctx, &env); err != nil {
        return ctrl.Result{}, err
    }
    // Bootstrap Flux inside vcluster
    if err := r.ensureFluxBootstrap(ctx, &env); err != nil {
        return ctrl.Result{}, err
    }
    // Wire webhook
    if err := r.ensureWebhook(ctx, &env); err != nil {
        return ctrl.Result{}, err
    }
    return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}
```

Per-CRD reconcilers live under `apps/<controller>/internal/`. Each is its own deployable component to keep blast-radius small (a bug in `blueprint-controller` cannot stall `workspace-controller`).

---

## User journeys (implementation references)

| Journey | Where it lands |
|---|---|
| Sovereign bootstrap | Phase 0 done by `catalyst-provisioner`; this codebase contains the OpenTofu modules under `apps/provisioning/opentofu/` and the post-bootstrap Catalyst install logic. |
| Environment creation | `workspace-controller` reconciles an `Environment` CR. |
| Application install | `apps/provisioning/` validates and commits to the Environment's Gitea repo. Flux (in the vcluster) reconciles. |
| Promotion between Environments | `apps/console/` opens a Gitea PR; `EnvironmentPolicy` controller gates merges. |
| Observability fanout | `apps/projector/` consumes JetStream, writes JetStream KV, fans SSE to console clients. |
| Blueprint registration | `apps/blueprint-controller/` watches Blueprint repos, validates, registers. |

For UX-level detail see [`docs/PERSONAS-AND-JOURNEYS.md`](../docs/PERSONAS-AND-JOURNEYS.md).

---

## Tech stack

| Layer | Technology |
|---|---|
| Language | Go 1.22+ |
| Web framework | Chi (HTTP) + connect-go (RPC) |
| K8s clients | controller-runtime, client-go |
| Frontend | Astro 5 + Svelte 5 + Tailwind 4 |
| Build | Ko (Go containers), Vite (UI) |
| Testing | go testing, Ginkgo for controllers, Playwright for UI |
| Telemetry | OpenTelemetry SDK (traces + metrics + logs) |
| Auth | JWT validation (Keycloak-issued) for users; SPIFFE SVID (transport mTLS) for workloads |

---

## Local development

```bash
# Run console (frontend + Go backend)
cd core/apps/console
go run .

# Run projector
cd core/apps/projector
go run .

# Run a controller against a local kind cluster
cd core/apps/workspace-controller
kind create cluster --name catalyst-dev
go run . --kubeconfig $HOME/.kube/config

# Frontend dev mode
cd core/ui
npm install
npm run dev

# Build all containers
ko build ./apps/...
```

Each `apps/<x>` folder is a separate Go module entrypoint; they share `internal/` and `pkg/` via the parent module.

---

## Deployment

Each component has a Kustomize base under `deploy/<component>/`. The umbrella Blueprint `bp-catalyst-platform` (in `products/catalyst/`) composes all of them into a single deploy unit.

For Sovereign-level provisioning details see [`docs/SOVEREIGN-PROVISIONING.md`](../docs/SOVEREIGN-PROVISIONING.md).

---

*Part of [OpenOva Catalyst](https://github.com/openova-io/openova).*
