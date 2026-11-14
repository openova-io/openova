# ADR-0039 — Environment objects have one writer: Flux applies them from files that the Sovereign's own API commits

Status: Accepted
Date: 2026-11-14

Written by work package CTL-061. It settles what ADR-0021 left open for the kind `Environment` (`catalyst.openova.io/v1`): who writes these objects, where their desired state lives in Git, which work package moves the two writers of today, and which work package moves stage `environments` of the intent lock from `audit` to `deny`. It takes in point 5 of lab test CTL-050 (`docs/ledger/plan/spikes/CTL-050.md`, section "S6: what creates Environment objects").

Terms used here:

- **Writer region**: the region whose Gitea accepts commits and whose controllers run (ADR-0021).
- **Control-plane Environment**: the Environment of a Sovereign's own platform, named by the Sovereign's name followed by `-cp`, and its control-plane Organization. The spine Applications (OpenBao, Keycloak, Harbor, Gitea) point to them.

## Context

Measured on `main` at commit `a6ff00206`, on 2026-11-14.

- No route creates an Environment object. No chart renders one, except one quality-assurance fixture of the platform chart that is off by default and that helm-controller applies. Two writers create them in the cluster (lab test CTL-050, section S6). With stage `environments` at `deny`, both were refused.
- **The organization-controller.** `ensureEnvironment` in `core/controllers/organization/internal/controller/environment_ensure.go` creates `<slug>-<envType>` with a plain create (line 218). It never changes an Environment that exists. It sets a controller owner reference to the Organization, with the Organization's UID (lines 204 to 210). `core/controllers/organization/internal/controller/organization_controller.go` calls it only when the vCluster of the Organization is ready (line 925). In that state the condition `Ready` of the Organization is `True`, or `False` with the reason `ProvisioningIncomplete` (lines 984 to 990). The controller's role may create Environments (`products/catalyst/chart/templates/controllers/organization-controller-clusterrole.yaml`).
- **The parent's API, in the background.** `products/catalyst/bootstrap/api/internal/handler/post_handover_spine_apps.go` applies, with the field manager `catalyst-api/spine-application-producer`, the control-plane Organization of a child Sovereign (line 816) and then its control-plane Environment, owned by that Organization (line 891). The API that runs this code is the one that provisioned the child. It reaches the child's cluster with the kubeconfig that the child posted back at first boot. It runs when Phase 1 ends (`products/catalyst/bootstrap/api/internal/handler/phase1_watch.go`, `products/catalyst/bootstrap/api/internal/handler/phase1_converged_late.go`) and at its own start-up for every child that was handed over (`products/catalyst/bootstrap/api/internal/handler/deployments.go`).
- The Application controller holds an Application at `EnvironmentMissing` until its Environment exists, and at `OrganizationMissing` until the Organization of that Environment exists (`core/controllers/application/internal/controller/application_controller.go`, lines 798 and 815).
- ADR-0021 decision C6 lists Environment as a kind of the intent lock and rejects listing the organization-controller and the API as writers (its alternative e). Its lab point 5 keeps stage `environments` at `audit` until a later decision record gives Environment objects one writer. Contract C4 (`docs/contracts/C4-git-layout-and-commit-library.md`, section 2.1) reserves `intent/environments/<name>.yaml` "only if the spike CTL-050 finds a route that creates them".
- The API already watches Organization objects through its informer cache (`products/catalyst/bootstrap/api/internal/k8scache/kinds.go`).

## Decision

| # | Decision |
|---|---|
| E1 | **One writer: Flux.** In the cluster, Environment objects are created, changed and deleted only by Flux: kustomize-controller applies them from the fleet repository through the Kustomization `sovereign-intent` of the writer region. The only author of those files is the Sovereign's own API, through `gitwrite` with the scope `FleetIntent(<fqdn>)`. No controller, no other API and no parent Sovereign writes an Environment object. The environment-controller keeps writing only the status and the finalizers. |
| E2 | **Path.** One Environment per file, at `clusters/<fqdn>/intent/environments/<name>.yaml` of the fleet repository, with the serialisation, the label and the annotation of contract C4, section 2.1. The folder `intent/environments/` is created: CTL-182 creates it with its first file, and in contract C4 it replaces the condition "only if the spike CTL-050 finds a route that creates them" by its own identifier. |
| E3 | **The Environment of an Organization.** The API commits `<slug>-<envType>` when its informer sees the vCluster of the Organization ready: the condition `Ready` is `True`, or `False` with the reason `ProvisioningIncomplete`. It commits only when the file is absent, and it never rewrites a file. The object is the one that `ensureEnvironment` creates when CTL-182 starts, without the owner reference. So an Application still waits at `EnvironmentMissing` until the vCluster is ready. |
| E4 | **The control-plane Organization and Environment.** The Sovereign's own API commits both at its start-up, each only when its file is absent: the Organization at `intent/organizations/<slug>.yaml`, the path of CTL-180, and the Environment at `intent/environments/<name>.yaml`. Their names and spec fields are those that the producer computes today (`spineOrganizationSlug`, `spineEnvironmentRef`, `ensureSpineOrganization` and `ensureSpineEnvironment` in `post_handover_spine_apps.go`), without owner references. The parent stops writing them. |
| E5 | **No owner reference.** The file of an Environment carries no owner reference. An Environment lives as long as its file. The commit that deletes the file of an Organization also deletes the files of its Environments, in the same atomic commit, and the Kustomization prunes them. An Environment that exists today keeps its owner reference when it is adopted. Because the files leave in one commit, the garbage collector and the prune remove the same objects. |
| E6 | **Adoption, then deletion.** CTL-182 first commits a file for each Environment that a writer of today created and that has no file yet. A byte-equal file applied by Flux keeps `metadata.generation` (lab test CTL-050, point 6), and the report of the lock then names no manager other than Flux for stage `environments` (ADR-0021, lab point 4). In the same package it deletes the create of `environment_ensure.go` and its call, and the two applies of the Organization and the Environment in `post_handover_spine_apps.go`. |
| E7 | **Deny.** Work package CTL-213 moves stage `environments` from `audit` to `deny`. It runs after CTL-182 and CTL-151, and only when the stage is at zero in the gate records of two consecutive release trains (ADR-0021 C6, ADR-0022). It also removes the verb `create` on `environments` from the role of the organization-controller (`products/catalyst/chart/templates/controllers/organization-controller-clusterrole.yaml`). No role of the API gains a write verb on `environments`. Because CTL-182 also moves the control-plane Organization, CTL-209 (stage `organizations`) runs after CTL-182. |

## Alternatives rejected

**E1.** (a) List the organization-controller and the API as writers: two writers of intent outside Git (ADR-0021, C6 alternative e). (b) The organization-controller writes the Environment into the file that it renders for the home region of the Organization, `regions/<region-id>/orgs/<slug>.yaml`: that file is applied in the home region, but the Application controller runs in the writer region, so a home region that is not the writer region would hold the Environment where nothing reads it. (c) The platform chart renders the Environments: an Environment exists per Organization, and the chart does not know the Organizations. (d) The parent keeps the control-plane objects and commits them into the child's repository: a Sovereign that is handed over runs on its own, without its parent (ADR-0002).

**E3.** (a) Commit the Environment with the file of the Organization, when the Organization is created: the Environment would exist before the vCluster is ready, and Applications would pass `EnvironmentMissing` and start against a vCluster that is not up. (b) The organization-controller keeps creating it: rejected under E1.

**E4.** (a) A route that commits them: no person asks for them, and they exist on every Sovereign from its start. (b) Commit them in the commit of the parent that creates the child: the child's fleet repository is not the parent's to write after the hand-over, and a Sovereign that exists today would never get the files.

**E5.** (a) Keep an owner reference that the environment-controller adds after creation (metadata stays open under C6): a second writer of the object, and the garbage collector would delete an Environment whose file still exists, which Flux would then create again. (b) Write the UID into the file: it is not known before the object exists, and it changes when a cluster is rebuilt from Git (ADR-0021, C21).

**E7.** (a) Switch the stage in CTL-209 or CTL-212: each of those packages switches one stage, and each stage has its own condition. (b) Deny before the writers move: both writers are refused (lab point 5).

## Consequences

- Until CTL-182 merges, Environment objects keep the two writers of today, under stage `environments` at `audit`.
- After CTL-182, no Sovereign creates an Environment in the cluster of another Sovereign.
- The quality-assurance fixture of the platform chart stays as it is: helm-controller is a Flux identity, so the lock admits it.
- This decision changes nothing about the owner reference that a spine Application carries to its Environment.

## Shipped via

Work packages in `docs/ledger/plan.yaml`: CTL-061 (ADR-0039), CTL-180 (the Organization file and its path), CTL-182 (the Environment files, adoption, and the deletion of both writers), CTL-151 (the report of the lock and its check), CTL-213 (stage `environments` to `deny`) and CTL-209 (stage `organizations` to `deny`, after CTL-182).
