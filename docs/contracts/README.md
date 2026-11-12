# Contracts

A contract is an interface that more than one area builds against. It is fixed
before the work that depends on it starts, and it is published as a file. This
page is the index: one row for each of the nine contracts.

| Contract | Name | Owning area | File |
|---|---|---|---|
| C1 | Blueprint schema | SVC (service model) | not published yet |
| C2 | Cloud profile | CLD (cloud abstraction) | not published yet |
| C3 | Event subjects | CTL (control plane) | `docs/contracts/C3-event-subjects.yaml` |
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
- The contract file of every area lives in this folder, beside this index. The
  gate manifest is the one exception: it lives with the plan, in
  `docs/ledger/`.
- Only the owning area changes a contract file.

C7, the gate manifest, is checked on every pull request by the workflow
`gate-manifest`. Its rules are written at the head of
`scripts/acceptance-gate.py`.
