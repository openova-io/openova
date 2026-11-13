---
name: Work package
about: The tracking Issue of one work package of docs/ledger/plan.yaml. Its ticket file is the instruction.
title: "<ID> <title of the package in docs/ledger/plan.yaml>"
---

## The problem today

<Two or three plain sentences: what is wrong or missing today. No code, no file paths.>

## What changes

<One or two plain sentences: what is different after the work.>

## Done means

- [ ] <One fact that a person can check when the work is done.>
- [ ] <Two to seven such facts in all, each written as an outcome.>

## Where it sits in the plan

- Stage: <the wave of the package>
- Area: <the three letters of the identifier>
- Group: <the epic of the package, or none>
- Size: <XS, S or M>. Kind: <the type of the package>.
- Waits for: <the packages of depends_on and after, or nothing>.
- Who does it: an executing agent, from the full instruction below. The architect reviews the pull request and merges it.

## The full instruction

The file `docs/ledger/plan/tickets/<ID>.md`. It is the complete specification: the measured state of the code, the exact steps, every file to create with its checksum, and when to stop. This Issue only summarises it, so the steps exist once. `scripts/ticket-lint.py` checks the ticket before it is handed out.

<details><summary>Technical details: files, starting check, proof commands</summary>

One pull request, from the branch `wp/<ID>`, with the line `Work-Package: <ID>` in its text.

**Files it changes**

- `<each path of the package's touches, one per line>`

**Before starting.** This command must print `<the expected output of the precondition>`:

```sh
<the precondition command>
```

**Proof.** CI runs these commands on the pull request. Exit code 0 is a pass.

```sh
# A1
<the first acceptance command, then each further one under its own identifier>
```

</details>
