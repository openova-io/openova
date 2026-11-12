# ADR-0024 — The primary substrate moved on 2026-05-22; status of the earlier substrate

Status: Accepted
Date: 2026-11-13

Written by work package CLD-071. On 2026-05-22 the platform stopped provisioning its Sovereigns on Hetzner Cloud and began to provision them on a Huawei Cloud Stack installation. No decision record said so. Three accepted records carry a later note about the move, and no record says what Hetzner is now. This record states both.

Terms used here:

- **Substrate**: the cloud on which the platform provisions a Sovereign, through one OpenTofu module under `infra/providers/`.
- **Primary substrate**: the substrate on which the platform's test Sovereigns run, and on which a change to provisioning is proven first.
- **Earlier substrate**: Hetzner Cloud, the primary substrate before 2026-05-22.
- **Unproven**: the module and the adapter of a substrate are in the repository, but no gate record shows a Sovereign provisioned there from current code.

## Context

Measured on `main` at commit `8599f27c1`, on 2026-11-13.

- 2026-04-28. Commit `e668637bc` added the OpenTofu module `infra/hetzner/`. It was the only module under `infra/` that provisioned a Sovereign. Until 2026-05-22 the platform provisioned on Hetzner Cloud only.
- 2026-05-22. The substrate changed in one day.
  - Commit `dd6e527af` moved the module to `infra/providers/hetzner/`. It added the Go port `CloudProvider` (`products/catalyst/bootstrap/api/internal/providers/provider.go`) with a Hetzner adapter and a Huawei stub.
  - Commit `5a7ecfa2f` added the module `infra/providers/huawei/` and implemented the Huawei adapter (`products/catalyst/bootstrap/api/internal/providers/huawei/`).
  - Commit `1711eed4b` wrote the day into `docs/ledger/TRACKER.md`: the Hetzner environment was wiped, and provisioning moved to a Huawei Cloud Stack installation.
- The work followed the move. Up to commit `8599f27c1`, 161 commits changed `infra/providers/huawei/` and 40 changed `infra/providers/hetzner/`. `docs/STATUS.md` names Huawei Cloud Stack as the delivery target of the OpenTofu modules.
- The move has no decision record. On 2026-08-26 commit `f0ab8a566` added a note to each of the accepted records 0001, 0002 and 0004. The notes call Hetzner legacy and Huawei the current substrate. A note corrects a record; it decides nothing. None of the three notes says whether Hetzner is still supported, or what would retire it.
- Hetzner is still in the repository, and still on the default path:
  - its module `infra/providers/hetzner/`, its adapter `products/catalyst/bootstrap/api/internal/providers/hetzner/`, the clean-up package `products/catalyst/bootstrap/api/internal/hetzner/` and the charts `platform/hcloud-ccm/`, `platform/hcloud-csi/` and `platform/cluster-autoscaler-hcloud/`;
  - the workflow `.github/workflows/infra-hetzner-tofu.yaml`. It validates the module and tests it offline, with mocked cloud providers. It provisions nothing;
  - the provisioning engine falls back to the Hetzner module when a request names no provider (`resolveModulePath` in `products/catalyst/bootstrap/api/internal/provisioner/provisioner.go`);
  - the wizard lists Hetzner first of five clouds, and selects it when no other hint applies (`products/catalyst/bootstrap/ui/src/pages/wizard/steps/StepProvider.tsx`);
  - `ListServers` of the Hetzner adapter returns no inventory.
- ADR-0016 decision K12 decided how Hetzner is proven again or retired: epic CLD-180. Its gate check CLD-G14, "Second provider, single region: full life cycle", is `not-built` in `docs/ledger/gates.yaml`, so no gate record of it exists.

## Decision

1. **A Huawei Cloud Stack installation is the primary substrate, since 2026-05-22.** Huawei Cloud Stack is the on-premises form of Huawei Cloud. It answers the Huawei API at the installation's own endpoints (ADR-0016, decision K11). Test Sovereigns are provisioned there with `infra/providers/huawei/`, and a change to provisioning is proven there first. Public Huawei Cloud is not a target.
2. **Hetzner Cloud is the earlier substrate, and it is unproven.** Its module, adapter and charts stay in the repository as the second provider of the cloud abstraction contract (ADR-0016). Being in the repository is not support. Until Hetzner is proven again, no Sovereign is offered on it, and no document says that one can be provisioned there.
3. **Hetzner leaves that state in one of two ways only.** ADR-0016 decision K12 fixed both. The proof is epic CLD-180; the retirement is work package CLD-190.
   - Proven. Work package CLD-182 provisions one single-region Sovereign on Hetzner from current code, within ten working days and at most three provisions, and gate check CLD-G14 passes. CLD-182 then marks the Hetzner profile `proven`, and CLD-G14 becomes `required`.
   - Retired. If that proof fails, or retirement is decided before it runs, work package CLD-190 deletes the Hetzner pack with its charts and its workflows.
   There is no third way. A passing module test, a document or a run that is not a record of CLD-G14 does not make Hetzner proven.
4. **Until then the platform does not offer Hetzner.** Work package CLD-080 writes the Hetzner profile with `lifecycle: unproven`. The providers routes and the wizard of epic CLD-120 read the profiles, so the wizard stops offering Hetzner. The fall-back to the Hetzner module goes when the provisioning engine becomes provider-neutral (ADR-0016 decision K1, epic CLD-100).
5. **The notes stay, and ADR-0024 wins.** The notes of 2026-08-26 in records 0001, 0002 and 0004 are not changed, because an accepted record is never edited. This record is the decision that those notes describe. Where a document of this repository describes Hetzner as the current, the default or a proven substrate, ADR-0024 wins over it.

## Alternatives rejected

- **Keep the notes as the only record of the move.** Rejected: a note corrects one accepted record. It is not a decision, it is not a header line that a tool reads, and it does not say what happens to the earlier substrate.
- **Rewrite the bodies of records 0001, 0002 and 0004.** Rejected: an accepted record is never edited in place (`docs/adr/README.md`).
- **Treat Hetzner as supported because its module passes its tests.** Rejected: those tests mock the cloud and provision nothing. Only a record of gate check CLD-G14 proves a substrate.
- **Retire Hetzner now, or prove it again before the cloud abstraction contract is built.** Both were rejected by ADR-0016 decision K12. This record does not reopen them.

## Consequences

- Until the work packages of epic CLD-120 have merged, the wizard still shows Hetzner first. That is a defect against decision 4, not a promise of support.
- A document that describes Hetzner as current, as the default or as proven is wrong against ADR-0024, and is corrected when its owner next rewrites it.
- This record changes no code, chart or workflow.

## Shipped via

Work package CLD-071 writes ADR-0024. Epic CLD-180 (work packages CLD-181, CLD-183, CLD-182 and CLD-184) and work package CLD-190 carry the two ways out of the unproven state. Work package CLD-080 and epic CLD-120 stop the offer, and epic CLD-100 removes the fall-back.
