# tools/qa-loop

Canonical helpers for the 5-agent QA team's Test Executor role. Each
file in this directory is the SINGLE-SOURCE-OF-TRUTH replacement for
ad-hoc `/tmp/iterN/*` scripts that previous qa-loop iterations
re-invented from scratch every cycle.

When a future iteration discovers a Test-Executor pattern that should
become canon, the rule is: **commit it here under
`tools/qa-loop/`, not under `/tmp/iterN/`.**

## Files

### `playwright-runner.js`

Drives a single headless Chromium against every `executor_method ==
"playwright"` row in a matrix JSON file. Memory-conscious (one
browser, one context, one page reused across all rows — under
~2 GiB RSS so the Coordinator can hold parallel Fix Authors per
`feedback_machine_saturation_3rd_violation.md`).

**The key feature is nav-interrupted recovery** (qa-loop iter-11
Cluster-B fix). The SPA's React route guard often pushes the
operator to `/login` or `/provision/<id>/<page>` mid-`page.goto`,
which Playwright surfaces as:

```
Error: page.goto: Navigation to "https://X" is interrupted by
       another navigation to "https://Y"
```

Iter-10/11 lost ~32 rows to this thrown exception. The new runner
catches the recoverable subclass of nav errors, settles on the
final URL, and re-runs the matrix's `must_contain` /
`must_not_contain` assertions against the recovered body. Rows
that bounced to `/login` get a diagnostic `auth-redirect:` reason
(cookie missing or expired) so the Coordinator can re-mint and
re-run instead of treating them as code bugs.

#### Usage

```bash
node tools/qa-loop/playwright-runner.js \
  --matrix=/path/to/test-matrix.json \
  --cookies=/path/to/cookies.txt \
  --out=/tmp/iter-N-pw-results.jsonl \
  --progress=/tmp/iter-N-progress.log \
  [--filter-category=resources] \
  [--filter-tier=viewer] \
  [--deployment-id=sovereign-omantel.biz] \
  [--timeout-ms=25000] \
  [--networkidle-ms=4000] \
  [--settle-ms=800] \
  [--headed]
```

The runner emits one JSONL line per test row with these fields:

```json
{
  "id": "TC-226",
  "category": "resources",
  "method": "playwright",
  "url": "https://...",
  "verdict": "PASS",
  "reason": "ok",
  "http_code": 200,
  "body_preview": "...",
  "final_url": "https://...",
  "recovered_from_nav_interrupt": false
}
```

`final_url` and `recovered_from_nav_interrupt` are new in iter-11 —
they let the Coordinator distinguish "the SPA bounced but landed on
the right page" (recovered=true, verdict=PASS) from "the SPA bounced
to /login because the cookie expired" (recovered=true, verdict=FAIL,
reason starts with `auth-redirect:`).

#### Tier-scoped runs (qa-loop iter-11 Cluster-A)

Pair this runner with the new `POST /api/v1/auth/test-session`
endpoint (catalyst-api `auth_test_session.go`) to assert the
matrix's tier-boundary 403/200 contract:

```bash
# 1. Mint a viewer-tier session into a fresh cookie jar
curl -fsS -c /tmp/viewer-cookies.txt -X POST \
  https://console.omantel.biz/api/v1/auth/test-session?tier=viewer

# 2. Run only the viewer-tier rows of the matrix
node tools/qa-loop/playwright-runner.js \
  --matrix=/path/to/test-matrix.json \
  --cookies=/tmp/viewer-cookies.txt \
  --filter-tier=viewer \
  --out=/tmp/iter-12-viewer-results.jsonl
```

The endpoint is gated by `CATALYST_TEST_SESSION_ENABLED=true` and
returns 404 on production Sovereigns, so this flow only runs on
QA / chroot Sovereigns where `qaFixtures.testSessionEnabled: true`.
