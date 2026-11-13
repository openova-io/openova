# Contracts

A contract is an interface that more than one area builds against. It is fixed
before the work that depends on it starts, and it is published as a file. This
page is the index: one row for each of the nine contracts.

| Contract | Name | Owning area | File |
|---|---|---|---|
| C1 | Blueprint schema | SVC (service model) | not published yet |
| C2 | Cloud profile | CLD (cloud abstraction) | `infra/providers/_contract/cloudprofile.schema.json` and `infra/providers/_contract/pack.schema.json` |
| C3 | Event subjects | CTL (control plane) | `docs/contracts/C3-event-subjects.yaml` and `docs/contracts/C3-alert-changed-v1.schema.json` |
| C4 | Git layout and commit library | CTL (control plane) | `docs/contracts/C4-git-layout-and-commit-library.md` |
| C5 | Upstream variables | SOV (Sovereign lifecycle) | `docs/contracts/c5-upstream-variables.schema.json` and `docs/contracts/c5-kit-keys.txt` |
| C6 | Crossplane 2 conventions | CLD (cloud abstraction) | `docs/contracts/c6-crossplane-conventions.md` |
| C7 | Gate manifest | FND (foundation gate and hygiene) | `docs/ledger/gates.yaml` |
| C8 | Seal provider | SEC (secrets and trust) | `docs/contracts/c8-seal-provider.md` |
| C9 | Mutating-route table | CTL (control plane) | `docs/contracts/C9-mutating-routes.yaml` |

How to read the table:

- The file column gives the path at which the owning area publishes the
  contract. A file is present once the work package that publishes it has
  merged. Until then the row says where it will be.
- "not published yet" means that no path is fixed for that contract.
- The contract file of every area lives in this folder, beside this index.
  There are two exceptions. The gate manifest lives with the plan, in
  `docs/ledger/`. The two schemas of C2 live in `infra/providers/_contract/`,
  because the API image and the provider modules read them there.
- Only the owning area changes a contract file.

C7, the gate manifest, is checked on every pull request by the workflow
`gate-manifest`. Its rules are written at the head of
`scripts/acceptance-gate.py`.

C3 is checked on every pull request by the workflow `check-contracts-ctl`. Its
rules are written at the head of `scripts/check-event-subjects.py`.

C2 is checked on every pull request by the workflow `check-provider-contract`.
Its rules are written at the head of `scripts/check-provider-contract.sh`.
