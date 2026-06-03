# `artifacts/` — designated home for transient files

**Rule (founder directive 2026-06-03): NEVER drop screenshots, logs, test
output, or any throwaway file in the repository root. It is a shame on the
repo.** Everything transient has a home here or under `docs/sessions/`.

| Kind of file | Where it goes | Tracked? |
|---|---|---|
| Throwaway debug screenshots, scratch dumps | `artifacts/screenshots/`, `artifacts/scratch/` | **No** — gitignored |
| Playwright MCP session dumps | `.playwright-mcp/` | No — gitignored |
| Playwright test-runner output | `test-results/` | No — gitignored |
| **Walk / UAT evidence you WANT to keep** (proof a case passed) | `docs/sessions/<YYYY-MM-DD>-<topic>/evidence/` | **Yes** — committed with its session report |

Everything in `artifacts/` except this README is gitignored. If a screenshot
is real acceptance evidence, move it into the dated `docs/sessions/.../evidence/`
folder and commit it there — do not leave it loose in `artifacts/` or root.
