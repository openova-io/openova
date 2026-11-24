# e2e/gate — the browser harness of the gate (FND-161)

The console regression list (`docs/ledger/UAT.md`) becomes browser tests here, one folder per gate check.
The harness runs in the live job of `.github/workflows/foundation-gate.yaml`, after the suite, against the
candidate Sovereign; no Sovereign runs it.

## A check is a folder

- A gate check whose `implementation` in `docs/ledger/gates.yaml` is a folder under
  `products/catalyst/bootstrap/ui/e2e/gate/` or `core/marketplace/e2e/gate/` is one Playwright project, named
  by the check id, that runs every `*.spec.ts` of the folder (`checks.ts`). It runs when the check is
  `expected-fail` or `required` and lists `cloud` in `runs_in`. A new check is a new folder and a manifest
  entry; the workflow is not edited for it, and no suite file registers it (rule 3 of
  `scripts/acceptance-gate.py`).
- The harness writes one result file per check (`reporter.ts`), which `acceptance-gate.py merge-check` adds
  to the gate record. The check passes only when every one of its tests ran and passed: a skipped test, a
  test marked to fail and a folder without a test are FAIL.
- A test's title starts with its ledger row id and a space: `test('13 Catalog grid renders the Blueprint
  cards', ...)`. The row map `docs/ledger/uat-row-map.csv` names the file of each converted row, and
  `cd tools/guards && go run ./cmd/check-uat-row-map` holds it.

## What a specification imports

```ts
import { test, expect, consoleURL, openHost, typeIntoCanvas, regionKubectl } from '../fixtures.ts'
```

| Name | What it gives |
|---|---|
| `page`, `context` | Signed in as the run's user (sovereign-admins and sovereign-viewers); `baseURL` is the console. |
| `test.use({ identity })` | `'run'`, `'second'` (a second member of sovereign-admins, tier admin) or `'viewer'` (sovereign-viewers only). |
| `signIn(identity)` | A further browser context signed in as that identity, closed after the test. |
| `openHost(page, sub, path)` | Another host of the same Sovereign in a new page of the same context: the same sign-on. |
| `typeIntoCanvas(locator, text)`, `pressInCanvas(locator, key)` | Keyboard input to a canvas element. |
| `regionKubectl(region, args)` | `kubectl` with the kubeconfig the gate workflow holds for region `a` or `b`; `{code, stdout, stderr}`. Only inside the gate workflow. |

A specification under `core/marketplace/e2e/gate/` imports only from this folder's `fixtures.ts` (by a
relative path), so that one copy of Playwright, this folder's, loads it.

## Running it

- The gate workflow: `bash e2e/gate/run.sh <check id>...` with the inputs of `env.ts` and
  `playwright.config.ts`. The identities are created for the run in the realm `sovereign` and deleted after
  it; their sessions are minted with the Sovereign's own signer.
- The self-test, from `products/catalyst/bootstrap/ui` after `npm ci` and the Chromium install:
  `bash e2e/gate/self-test/run.sh`. It runs the harness for real against a local server for every host of a
  stand-in domain. The typecheck: `npx tsc --noEmit -p e2e/gate`.
- Traces of failed tests stay on the runner (a trace holds the session cookie of its requests); the
  workflow uploads the result files, the screenshots and the error contexts.
