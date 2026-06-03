# G117 Playwright spec scaffolds

> Pre-staged Playwright spec scaffolds for the G117 sub-EPICs. **Not in the
> live suite yet.** Wave-1.B5 / Wave-2 / W4 promote these into
> `products/catalyst/console/tests/e2e/` once the corresponding feature
> ships.

See the companion audit:
[`docs/sessions/2026-06-02-G117-playwright-coverage-audit.md`](../2026-06-02-G117-playwright-coverage-audit.md).

## Layout

| Dir | Sub-EPIC | Promotion gate |
|---|---|---|
| `g117.1/` | G117.1 admission webhook | Wave-1.B5 once webhook lands |
| `g117.3/` | G117.3 Endpoints tab + PR pipeline | Wave-2 once UI + Gitea PR API land |
| `g117.4/` | G117.4 Launch button SSO fallback + wallclock | Wave-2 (fallback) / W4 (wallclock) |
| `g117.5/` | G117.5 SSO fan-out + per-Org realm + cross-Org isolation | W4 (live hw86) |
| `g117.6/` | G117.6 application-controller multi-instance + topology fanout | W4 (live hw86) |
| `regression/` | Pre-G117 baseline (PIN / voucher / Sandbox MCP) | W4 |

## Skip-gate pattern

Every scaffold guards live-mode assertions behind `test.skip(condition,
'reason')` with an env flag. This lets the scaffolds be imported into the
live suite without breaking the baseline green; you flip the env flag once
the upstream feature lands.

| Env flag | Effect |
|---|---|
| `G117_ADMISSION_LIVE=1` | Run G117.1 admission webhook specs |
| `G117_GITEA_LIVE=1` | Run G117.3 PR-pipeline specs |
| `G117_RENAME_LIVE=1` | Run G117.3 endpoint-rename specs |
| `G117_LIVE_SOVEREIGN=1` | Run W4 live-Sovereign specs |
| `G117_KUBECTL_HARNESS=1` | Run specs that need a kubectl harness via `/diag` |
| `G117_KC_ADMIN_TOKEN=<token>` | Run specs that read KC admin API |
| `G117_USER_A_PASS=<pass>` | Run cross-Org token-exchange spec |
| `G117_OPERATOR_PIN=<pin>` | Run regression PIN-login spec |
| `G117_SOV_FQDN=t01.omani.works` | Run regression voucher-redeem spec |

## Promotion checklist (per scaffold)

When promoting a scaffold from here into `tests/e2e/`:

1. `git mv docs/sessions/2026-06-02-G117-playwright-scaffolds/<dir>/<file>.spec.ts products/catalyst/console/tests/e2e/<file>.spec.ts`
2. Strip the `test.skip(!FLAG, ...)` guard if the feature is mock-mode-coverable now.
3. Add fixture rows to `tests/e2e/fixtures/mock-blueprints.yaml` for any new IDs the spec needs.
4. Re-run `npm run test:e2e` locally → all green before pushing.
5. Update `docs/ledger/TRACKER.md` row for the sub-EPIC.

## Why not land these in `tests/e2e/` now?

Per task scope:

> READ-ONLY on existing `tests/e2e/` content — do not modify existing specs.
> New spec scaffolds land under `docs/sessions/.../scaffolds/` (NOT `tests/e2e/`) —
> Wave-1.B5 promotes them when ready.

The scaffolds also reference `data-testid` selectors that do NOT exist in
the codebase yet (e.g. `endpoint-pr-link`, `nav-sandbox`, `pin-rate-limit`).
Landing them in `tests/e2e/` today would either need a `test.skip` blanket
or would block on missing UI surfaces — neither helps W4. Pre-staging here
keeps the live suite at 12/12 green and gives Wave-1.B5 a complete
unskip-and-rename checklist.
