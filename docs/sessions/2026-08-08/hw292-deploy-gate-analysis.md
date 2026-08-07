# hw292 deploy gate — what 28 UAT rows are actually waiting on

**Measured 2026-08-08.** Every number here comes from a command in this document;
nothing is inferred from a green CI job.

## The gate in one line

hw292 runs `catalyst-api` **`fad88bd`**, built **2026-08-02**. `main` has **127**
`fix:` commits that are not ancestors of it. **28 UAT rows** cite a fix that is
merged but not in the running artifact.

```
$ curl -s https://console.hw292.omani.works/api/v1/version
{"service":"catalyst-api","sha":"fad88bd","gitSha":"fad88bd","version":"0.0.0",
 "chartVersion":"1.4.95","buildTime":"2026-08-07T16:30:01Z","go":"go1.26.5"}

$ git log --oneline fad88bd..main --grep='^fix' | wc -l
127
```

## The 28 rows, by verdict

`scripts/classify-uat-delivery-state.py --image fad88bd --status <glyph>`

| Verdict | DEPLOY-GATED | rows |
|---|---|---|
| ❌ | 12 of 13 | R17, W1, W2, 35, 37, 57, 86, 90, 92, 115, 219, 234 |
| ⚠️ | 16 of 38 | W5, G9, 1, 4, 9, 18, 48, 53, 55, 59, 62, 71, 87, 121, 233, 241 |

**Only ONE ❌ row is CODE-BLOCKED: row 20** — and it is not a code defect either.
`GET /api/v1/fleet/treemap` returns `{"error":"unauthenticated"}` from an
unauthenticated seat, so the probe cannot distinguish "no Org attribution" from
"no access". It needs a session, not a patch.

**The practical consequence: writing more fixes does not move these 28 rows.**
Their fixes are written, merged, and published. What is missing is the roll.

## What is NOT the blocker (checked, so nobody re-checks)

- **The chart is current.** The deploy-bot auto-commits image pins on every merge
  to `main`; the latest is `b898130`, which includes today's work.
- **The artifacts are published.** `bp-openbao:1.2.66` and
  `bp-catalyst-platform:1.4.1330` are both on GHCR — verified against the
  registry, not inferred from a job result:
  ```
  $ gh api "/orgs/openova-io/packages/container/bp-catalyst-platform/versions" \
      -q '[.[].metadata.container.tags[]?]|map(select(test("^1\\.4\\.13")))'
  ["1.4.1330","1.4.1329"]
  ```
  `1.4.1330` was unpublishable for ~3h — a chart test I added in #5813 rendered
  the whole chart with stderr discarded and failed only after
  `helm dependency build`, which is CI's condition and not a clean tree's.
  Fixed in #5832.

## What I could NOT determine, and why it matters

**How hw292 picks up a new chart.** hw292 is post-cutover (`cutoverComplete=true`),
and per ADR-0002 the whole point of cutover is that the Sovereign pulls from its
**local** Gitea/Harbor rather than from GitHub. A bump on `main` therefore does
**not** reach it by the pre-cutover path, and I did not verify the mirror-advance
mechanism from a live probe.

Recording this as an open question rather than guessing a procedure: a runbook
that invents steps is worse than one that names the gap. The operator or a
session with cluster access should confirm the trigger before anyone claims
these 28 rows will convert.

## Second finding: the pod is restarting

`buildTime` on hw292, same `sha`, two probes ~4h apart on 2026-08-07:

| Probe | `sha` | `buildTime` |
|---|---|---|
| ~12:40Z | `fad88bd` | `2026-08-07T12:39:41Z` |
| ~16:31Z | `fad88bd` | `2026-08-07T16:30:01Z` |

The deployment sets no build-time ldflag (`version` is the compiled-in `0.0.0`),
so `buildTime` falls back to **process start**. Two values on one image means the
pod restarted between probes — corroborating #5642 (catalyst-api OOMKilling, 15
restarts).

This is observable **without a session**, which matters: `/api/v1/sovereign/apps`
and the treemap both 401 unauthenticated, and that is what has blocked several
rows from being walked at all. Restart cadence can be sampled by polling
`/api/v1/version` and watching `buildTime` move against a fixed `sha`.

#5822 makes this readable rather than inferred, by adding
`buildTimeSource: "env" | "ldflag" | "process-start"`. Until hw292 rolls onto an
image carrying it, the field still reads like a build timestamp.

## How to re-measure after a roll

```bash
curl -s https://console.hw292.omani.works/api/v1/version          # sha must move off fad88bd
python3 scripts/classify-uat-delivery-state.py --image <new-sha> --status '❌'
python3 scripts/classify-uat-delivery-state.py --image <new-sha> --status '⚠️'
```

Rows that flip from DEPLOY-GATED to absent are the ones the roll converted. Rows
that stay listed carry a fix that is in the artifact and **still fails** — those
are real engineering, and they are the only ones worth writing code against.

**Do not read the artifact age off `/version`'s `buildTime`.** That is trap 0 in
`classify-uat-delivery-state.py` and the reason it takes `--image <commit-ish>`
and dates it from git: on 2026-08-07 the field reported a same-day timestamp for
a five-day-old binary, which would have flipped all 12 DEPLOY-GATED rows to a
false CODE-BLOCKED.
