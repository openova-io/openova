#!/usr/bin/env bash
# e2e/gate/run.sh — run the browser harness of the gate (FND-161) for the checks named on the command line.
#
#   bash e2e/gate/run.sh <check id>...     (no id: every check e2e/gate/checks.ts selects)
#
# The inputs are the environment variables of e2e/gate/env.ts and e2e/gate/playwright.config.ts. It writes
# one result file per check into GATE_RESULTS and exits with Playwright's exit code (0: every test passed).
set -euo pipefail
cd "$(dirname "$0")/../.."
GATE_CHECKS="$(IFS=,; printf '%s' "$*")"
export GATE_CHECKS
exec npx playwright test -c e2e/gate/playwright.config.ts
